// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Body half of transaction hash announcements: a node that sees an announced
// hash it does not hold asks the announcer for the body here.
//
// Transactions used to travel as full bodies on the gossip mesh, which put
// every transaction's bytes on every mesh edge -- on a 7-node mesh, roughly six
// times more wire traffic than the transaction needed. At saturation that made
// moving transactions cost about 39% of a node's CPU (network egress, pubsub,
// and pool ingest) while executing them cost under 0.5%. Announcing 32 bytes
// and fetching the body once, from one peer, moves that cost onto the edges
// that actually need it.

package sync

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/log"
)

const (
	// maxPooledTxRequest bounds one request. It also bounds the responder's
	// work and its response size, so it is enforced on both sides.
	maxPooledTxRequest = 4096

	// pooledTxRequestTimeout covers writing the request and reading the whole
	// response. Generous relative to a mesh hop: the responder may be building
	// a block when the request lands.
	pooledTxRequestTimeout = 15 * time.Second
)

// pooledTxsRequest is the wire form of a request: a 2-byte big-endian count
// followed by that many 32-byte hashes. Raw rather than SSZ or RLP because the
// shape is fixed and this is the highest-frequency request in the node.
func encodePooledTxsRequest(hashes []types.Hash) []byte {
	buf := make([]byte, 2+len(hashes)*types.HashLength)
	binary.BigEndian.PutUint16(buf, uint16(len(hashes)))
	off := 2
	for i := range hashes {
		off += copy(buf[off:], hashes[i][:])
	}
	return buf
}

func readPooledTxsRequest(r io.Reader) ([]types.Hash, error) {
	var cnt [2]byte
	if _, err := io.ReadFull(r, cnt[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(cnt[:]))
	if n == 0 {
		return nil, fmt.Errorf("empty pooled transaction request")
	}
	if n > maxPooledTxRequest {
		return nil, fmt.Errorf("pooled transaction request of %d exceeds the %d limit", n, maxPooledTxRequest)
	}
	raw := make([]byte, n*types.HashLength)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, err
	}
	hashes := make([]types.Hash, n)
	for i := 0; i < n; i++ {
		copy(hashes[i][:], raw[i*types.HashLength:])
	}
	return hashes, nil
}

// pooledTxsByHashStreamHandler serves transaction bodies for requested hashes.
//
// The response is a single chunk holding an RLP list of encoded transactions,
// not one chunk per transaction. Cutting syscalls is the point of the whole
// change: a per-transaction chunk would put the write-syscall cost straight
// back, just on a different protocol.
//
// Hashes the pool does not hold are simply absent from the response. The
// requester matches by recomputed hash, so a short or reordered response is
// not a correctness problem, and a peer that has since mined or evicted a
// transaction is not misbehaving.
func (s *Service) pooledTxsByHashStreamHandler(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	if s.cfg.txPool == nil {
		return
	}
	SetStreamReadDeadline(stream, ttfbTimeout)
	hashes, err := readPooledTxsRequest(stream)
	if err != nil {
		log.Debug("pooled txs: bad request", "peer", stream.Conn().RemotePeer().String()[:12], "err", err)
		return
	}

	bodies := make([][]byte, 0, len(hashes))
	for _, h := range hashes {
		tx := s.cfg.txPool.GetTx(h)
		if tx == nil {
			continue
		}
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			continue
		}
		bodies = append(bodies, enc)
	}

	data, err := rlp.EncodeToBytes(bodies)
	if err != nil {
		log.Debug("pooled txs: encode failed", "err", err)
		return
	}
	// Check the cap before committing anything to the stream. A responder that
	// writes a header it cannot finish leaves the requester unable to tell a
	// protocol error from corruption -- that exact shape stranded a validator
	// on the block path (see WriteBlockChunk).
	if uint64(len(data)) > encoder.MaxChunkSize {
		log.Debug("pooled txs: response over cap, dropping", "bytes", len(data), "requested", len(hashes))
		return
	}
	SetStreamWriteDeadline(stream, defaultWriteDuration)
	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		return
	}
	if _, err := encoder.EncodeWithMaxLengthLimit(stream, &rawSSZBytes{data: data}, encoder.MaxChunkSize); err != nil {
		log.Debug("pooled txs: write failed", "err", err)
	}
}

// requestPooledTxs asks one peer for the bodies of the given hashes and adds
// what comes back to the pool. Returns the number accepted.
func (s *Service) requestPooledTxs(pid peer.ID, hashes []types.Hash) int {
	if len(hashes) == 0 || s.cfg.txPool == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(s.ctx, pooledTxRequestTimeout)
	defer cancel()

	protoID := protocol.ID(p2p.RPCPooledTxsByHashTopicV1 + s.cfg.p2p.Encoding().ProtocolSuffix())
	stream, err := s.cfg.p2p.Host().NewStream(ctx, pid, protoID)
	if err != nil {
		log.Debug("pooled txs: stream failed", "peer", pid.String()[:12], "err", err)
		return 0
	}
	defer func() { _ = stream.Close() }()

	SetStreamWriteDeadline(stream, defaultWriteDuration)
	if _, err := stream.Write(encodePooledTxsRequest(hashes)); err != nil {
		return 0
	}
	// Do NOT CloseWrite: the handler reads a fixed-size request and then
	// replies on the same stream, so closing the write half tears it down
	// before the response arrives.
	SetStreamReadDeadline(stream, respTimeout)
	var code [1]byte
	if _, err := io.ReadFull(stream, code[:]); err != nil {
		return 0
	}
	if code[0] != responseCodeSuccess {
		return 0
	}
	raw := &rawSSZBytes{}
	if err := encoder.DecodeWithMaxLengthLimit(stream, raw, encoder.MaxChunkSize); err != nil {
		log.Debug("pooled txs: decode failed", "peer", pid.String()[:12], "err", err)
		return 0
	}
	var bodies [][]byte
	if err := rlp.DecodeBytes(raw.data, &bodies); err != nil {
		log.Debug("pooled txs: malformed response", "peer", pid.String()[:12], "err", err)
		return 0
	}

	txs := make([]*transaction.Transaction, 0, len(bodies))
	for _, enc := range bodies {
		tx, err := transaction.DecodeEthereumTransaction(enc)
		if err != nil {
			continue // one bad body must not discard the rest
		}
		txs = append(txs, tx)
	}
	if len(txs) == 0 {
		return 0
	}
	// The pool re-derives the sender from the signature and rejects anything
	// that does not validate, so nothing here trusts the responder beyond it
	// having supplied bytes worth checking.
	accepted := 0
	for i, err := range s.cfg.txPool.AddRemotes(txs) {
		if err == nil {
			accepted++
		} else {
			log.Debug("pooled txs: rejected by pool", "hash", txs[i].Hash(), "err", err)
		}
	}
	return accepted
}
