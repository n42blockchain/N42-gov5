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

	ctx    context.Context
	cancel context.CancelFunc
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
	log.Info("Ingest server started", "addr", ln.Addr().String())

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

// maxTxSize limits the byte size of a single transaction.
const maxTxSize = 128 * 1024 // 128 KiB

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

		// Read tx bytes.
		txBytes := make([]byte, txLen)
		if _, err := io.ReadFull(r, txBytes); err != nil {
			return accepted, err
		}

		// Read 20-byte sender address.
		if _, err := io.ReadFull(r, senderBuf[:]); err != nil {
			return accepted, err
		}

		// Decode the transaction.
		tx := new(transaction.Transaction)
		if err := tx.Unmarshal(txBytes); err != nil {
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
