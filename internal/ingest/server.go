// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

// Package ingest provides a binary TCP server for high-throughput transaction
// injection. It is intended for stress testing and benchmarking only.
package ingest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// TxPool is the interface for submitting transactions.
type TxPool interface {
	AddLocal(tx *transaction.Transaction) error
	Stats() (pending, pendingAddrs, queued, queuedAddrs int)
}

// Server accepts binary-encoded transactions over TCP for high-throughput injection.
//
// Wire format per batch:
//
//	[4B LE num_txs] [2B LE tx_len, tx_bytes, 20B sender] x num_txs
//
// The sender field is pre-recovered by the stress tool to skip ECDSA verification.
//
// WARNING: This server trusts the provided sender address. It is intended for
// controlled testing environments only. Do NOT expose on public networks.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	pool     TxPool
	addr     string

	// Backpressure gates
	softTarget int // resume accepting when pool drops below this
	hardCap    int // reject all when pool exceeds this

	// Metrics
	injected atomic.Uint64
	rejected atomic.Uint64
	batches  atomic.Uint64

	// Hint-only mode: decode, recover the sender into the process-wide
	// sender cache, drop the transaction. See EnableHintOnly.
	hintOnly    bool
	hintSigner  transaction.Signer
	hintWorkers int
	hintQueue   chan *transaction.Transaction
	hinted      atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
}

// EnableHintOnly makes the endpoint a sender pre-recovery feed. Nothing
// reaches the pool: each transaction is decoded, its sender is recovered
// from the signature across `workers` goroutines (which memoises it in the
// process-wide sender cache keyed by transaction hash), and the object is
// dropped. The client's 20-byte sender field is ignored -- the cache may
// only ever hold signature-recovered results, because the import's sender
// verification trusts it. A follower fed the transactions the leader is
// filling its blocks from then imports with every recovery a cache hit
// (round 35zh: recover was 460 ms of a 1.44 s import at 163k; the same work
// done here, ahead of the block, is off the critical path). Must be called
// before Start.
func (s *Server) EnableHintOnly(signer transaction.Signer, workers int) {
	if workers < 1 {
		workers = 1
	}
	s.hintOnly = true
	s.hintSigner = signer
	s.hintWorkers = workers
	s.hintQueue = make(chan *transaction.Transaction, 65536)
}

// Hinted reports how many senders the hint-only mode has recovered.
func (s *Server) Hinted() uint64 { return s.hinted.Load() }

func (s *Server) hintWorker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case tx := <-s.hintQueue:
			if _, err := transaction.Sender(s.hintSigner, tx); err != nil {
				s.rejected.Add(1)
				continue
			}
			s.hinted.Add(1)
		}
	}
}

// NewServer creates a new ingest server. It does not start listening until
// Start is called.
func NewServer(addr string, pool TxPool, softTarget, hardCap int) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		pool:       pool,
		addr:       addr,
		softTarget: softTarget,
		hardCap:    hardCap,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start begins listening for TCP connections. It returns an error if the
// listener cannot be created.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("ingest: listen %s: %w", s.addr, err)
	}
	s.listener = ln
	log.Info("Ingest server started", "addr", ln.Addr().String(), "hintOnly", s.hintOnly, "hintWorkers", s.hintWorkers)

	if s.hintOnly {
		for i := 0; i < s.hintWorkers; i++ {
			go s.hintWorker()
		}
	}
	go s.acceptLoop(ln)
	return nil
}

// Stop shuts down the server and closes the listener.
func (s *Server) Stop() {
	s.cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	log.Info("Ingest server stopped")
}

// Addr returns the listener address, or empty string if not listening.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Stats returns the cumulative injected, rejected, and batch counts.
func (s *Server) Stats() (injected, rejected, batches uint64) {
	return s.injected.Load(), s.rejected.Load(), s.batches.Load()
}

// acceptLoop accepts connections until the context is cancelled.
// The listener is passed as a parameter to avoid racing with Stop().
func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
			}
			// Transient error; continue.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			// Listener closed.
			return
		}
		go s.handleConn(conn)
	}
}

// handleConn processes a single client connection. Each connection may
// contain multiple batches, processed sequentially until the connection
// closes or an error occurs.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		accepted, err := s.readBatch(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, net.ErrClosed) {
				log.Warn("Ingest connection error", "err", err)
			}
			return
		}

		// Write 4-byte LE accepted count back to the client.
		var resp [4]byte
		binary.LittleEndian.PutUint32(resp[:], accepted)
		if _, err := conn.Write(resp[:]); err != nil {
			return
		}
	}
}

// maxBatchSize limits the number of transactions per batch to prevent
// resource exhaustion.
const maxBatchSize = 100_000

// maxTxSize limits the byte size of a single encoded transaction.
// Set below the uint16 ceiling (65535) to reject oversized payloads
// that still fit the 2-byte wire length field.
const maxTxSize = 48 * 1024 // 48 KiB

// readBatch reads and processes one batch of transactions from the
// connection. It returns the number of successfully injected transactions.
func (s *Server) readBatch(r io.Reader) (uint32, error) {
	// Read num_txs (4 bytes LE).
	var numBuf [4]byte
	if _, err := io.ReadFull(r, numBuf[:]); err != nil {
		return 0, err
	}
	numTxs := binary.LittleEndian.Uint32(numBuf[:])

	if numTxs == 0 {
		s.batches.Add(1)
		return 0, nil
	}
	if numTxs > maxBatchSize {
		return 0, fmt.Errorf("ingest: batch size %d exceeds max %d", numTxs, maxBatchSize)
	}

	// Check backpressure before processing the batch.
	if s.hintOnly {
		return s.readHintBatch(r, numTxs)
	}
	pending, _, _, _ := s.pool.Stats()
	if pending > s.hardCap {
		// Drain the batch from the connection to keep framing consistent,
		// but reject all transactions.
		if err := s.drainBatch(r, numTxs); err != nil {
			return 0, err
		}
		s.rejected.Add(uint64(numTxs))
		s.batches.Add(1)
		return 0, nil
	}

	var accepted uint32
	var senderBuf [20]byte
	txBuf := make([]byte, 0, 1024) // reusable buffer for tx bytes

	for i := uint32(0); i < numTxs; i++ {
		// Read tx_len (2 bytes LE).
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return accepted, err
		}
		txLen := binary.LittleEndian.Uint16(lenBuf[:])
		if int(txLen) > maxTxSize {
			return accepted, fmt.Errorf("ingest: tx size %d exceeds max %d", txLen, maxTxSize)
		}

		// Read tx bytes, reusing the buffer when capacity allows.
		if cap(txBuf) >= int(txLen) {
			txBuf = txBuf[:txLen]
		} else {
			txBuf = make([]byte, txLen)
		}
		if _, err := io.ReadFull(r, txBuf); err != nil {
			return accepted, err
		}

		// Read 20-byte sender address.
		if _, err := io.ReadFull(r, senderBuf[:]); err != nil {
			return accepted, err
		}

		// Decode the transaction.
		tx := new(transaction.Transaction)
		if err := tx.Unmarshal(txBuf); err != nil {
			s.rejected.Add(1)
			continue
		}

		// Set the pre-recovered sender.
		var sender types.Address
		copy(sender[:], senderBuf[:])
		tx.SetFrom(sender)

		// Submit to pool.
		if err := s.pool.AddLocal(tx); err != nil {
			s.rejected.Add(1)
			continue
		}

		s.injected.Add(1)
		accepted++
	}

	s.batches.Add(1)
	return accepted, nil
}

// drainBatch reads and discards a full batch from the reader to keep the
// wire protocol framing consistent after a backpressure rejection.
// readHintBatch is readBatch for the hint-only mode: same wire format, the
// sender field is read and ignored, and the decoded transaction goes to the
// recovery workers instead of the pool. The reply counts the transactions
// queued.
func (s *Server) readHintBatch(r io.Reader, numTxs uint32) (uint32, error) {
	var queued uint32
	var senderBuf [20]byte
	for i := uint32(0); i < numTxs; i++ {
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return queued, err
		}
		txLen := binary.LittleEndian.Uint16(lenBuf[:])
		if int(txLen) > maxTxSize {
			return queued, fmt.Errorf("ingest: tx size %d exceeds max %d", txLen, maxTxSize)
		}
		// A fresh buffer per transaction: Unmarshal may keep references.
		txBuf := make([]byte, txLen)
		if _, err := io.ReadFull(r, txBuf); err != nil {
			return queued, err
		}
		if _, err := io.ReadFull(r, senderBuf[:]); err != nil {
			return queued, err
		}
		tx := new(transaction.Transaction)
		if err := tx.Unmarshal(txBuf); err != nil {
			s.rejected.Add(1)
			continue
		}
		select {
		case s.hintQueue <- tx:
			queued++
		case <-s.ctx.Done():
			return queued, net.ErrClosed
		}
	}
	s.batches.Add(1)
	return queued, nil
}

func (s *Server) drainBatch(r io.Reader, numTxs uint32) error {
	for i := uint32(0); i < numTxs; i++ {
		// Read tx_len.
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return err
		}
		txLen := binary.LittleEndian.Uint16(lenBuf[:])

		// Discard tx_bytes + 20-byte sender.
		discard := int64(txLen) + 20
		if _, err := io.CopyN(io.Discard, r, discard); err != nil {
			return err
		}
	}
	return nil
}
