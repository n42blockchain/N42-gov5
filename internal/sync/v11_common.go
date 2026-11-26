// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Shared plumbing of the v1.1 sync protocols (N42 sync standard §5):
// stream registration, request reading, per-peer/global stream quotas
// (§6.3) and capability discovery through the identify protocol list (§5.0).

package sync

import (
	"context"
	"runtime/debug"
	"strings"
	gosync "sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/log"
)

// v11StreamQuota bounds concurrent serving streams of one class, per peer
// and in total (§6.3: ranges 2/8, state 2/16).
type v11StreamQuota struct {
	mu      gosync.Mutex
	perPeer int
	global  int
	total   int
	byPeer  map[peer.ID]int
}

func newV11StreamQuota(perPeer, global int) *v11StreamQuota {
	return &v11StreamQuota{perPeer: perPeer, global: global, byPeer: make(map[peer.ID]int)}
}

// acquire takes a slot for pid; ok false means over quota.
func (q *v11StreamQuota) acquire(pid peer.ID) (release func(), ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.total >= q.global || q.byPeer[pid] >= q.perPeer {
		return nil, false
	}
	q.total++
	q.byPeer[pid]++
	return func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.total--
		if q.byPeer[pid]--; q.byPeer[pid] <= 0 {
			delete(q.byPeer, pid)
		}
	}, true
}

var (
	v11RangeQuota = newV11StreamQuota(2, 8)
	v11StateQuota = newV11StreamQuota(2, 16)
	v11SmallQuota = newV11StreamQuota(4, 64) // commit_qc, qmdb_status
)

// v11RetryAfterMs is the RATE_LIMITED retry hint (decimal milliseconds).
const v11RetryAfterMs = "1000"

// v11ChunkTimeout is the per-chunk write deadline of §6.2:
// 10 s + len / (1 MiB/s).
func v11ChunkTimeout(n int) time.Duration {
	return 10*time.Second + time.Duration(n)*time.Second/(1<<20)
}

// v11Handler serves one request: req is the decoded request payload.
type v11Handler func(ctx context.Context, pid peer.ID, req []byte, stream network.Stream)

// registerV11 registers a raw stream handler for a v1.1 protocol. maxReq
// bounds the request payload (refused before allocation).
func (s *Service) registerV11(proto string, maxReq uint64, quota *v11StreamQuota, h v11Handler) {
	s.cfg.p2p.SetStreamHandler(proto, func(stream network.Stream) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("v1.1 sync handler panic", "protocol", proto, "err", r, "stack", string(debug.Stack()))
			}
			_ = stream.Reset() // no-op after a clean Close
		}()
		pid := stream.Conn().RemotePeer()
		if s.cfg.p2p.Peers().IsBad(pid) {
			return
		}
		_ = stream.SetReadDeadline(time.Now().Add(ttfbTimeout))
		req, err := syncwire.ReadFramed(stream, maxReq)
		if err != nil {
			writeV11(stream, syncwire.AppendError(nil, syncwire.CodeInvalidRequest, err.Error()))
			return
		}
		release, ok := quota.acquire(pid)
		if !ok {
			writeV11(stream, syncwire.AppendError(nil, syncwire.CodeRateLimited, v11RetryAfterMs))
			_ = stream.Close()
			return
		}
		defer release()
		ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
		defer cancel()
		h(ctx, pid, req, stream)
		_ = stream.Close()
	})
}

// writeV11 writes bytes under the per-chunk deadline.
func writeV11(stream network.Stream, b []byte) bool {
	_ = stream.SetWriteDeadline(time.Now().Add(v11ChunkTimeout(len(b))))
	_, err := stream.Write(b)
	return err == nil
}

// v11Unsupported remembers peers whose multistream refused a v1.1 protocol
// (marked v1-only for an hour, §5.0).
var v11Unsupported struct {
	mu    gosync.Mutex
	until map[string]time.Time // pid + protocol
}

func v11MarkUnsupported(pid peer.ID, proto string) {
	v11Unsupported.mu.Lock()
	defer v11Unsupported.mu.Unlock()
	if v11Unsupported.until == nil {
		v11Unsupported.until = make(map[string]time.Time)
	}
	if len(v11Unsupported.until) > 4096 {
		v11Unsupported.until = make(map[string]time.Time)
	}
	v11Unsupported.until[string(pid)+proto] = time.Now().Add(time.Hour)
}

// peerSpeaks reports whether pid listed proto in identify and has not
// refused it in the last hour.
func peerSpeaks(h host.Host, pid peer.ID, proto string) bool {
	if h == nil {
		return false
	}
	v11Unsupported.mu.Lock()
	until, marked := v11Unsupported.until[string(pid)+proto]
	v11Unsupported.mu.Unlock()
	if marked && time.Now().Before(until) {
		return false
	}
	got, err := h.Peerstore().SupportsProtocols(pid, protocol.ID(proto))
	return err == nil && len(got) > 0
}

// openV11 opens a stream for proto, writes the framed request and closes
// the write side. A multistream refusal marks the peer.
func openV11(ctx context.Context, h host.Host, pid peer.ID, proto string, req []byte) (network.Stream, error) {
	stream, err := h.NewStream(ctx, pid, protocol.ID(proto))
	if err != nil {
		if isProtocolNotSupported(err) {
			v11MarkUnsupported(pid, proto)
		}
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := syncwire.WriteFramed(stream, req); err != nil {
		_ = stream.Reset()
		return nil, err
	}
	if err := stream.CloseWrite(); err != nil {
		_ = stream.Reset()
		return nil, err
	}
	_ = stream.SetReadDeadline(time.Now().Add(ttfbTimeout))
	return stream, nil
}

func isProtocolNotSupported(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "protocols not supported") || strings.Contains(msg, "protocol not supported")
}

// hostOf returns the libp2p host of a p2p provider, if it exposes one.
func hostOf(p interface{}) host.Host {
	if hp, ok := p.(interface{ Host() host.Host }); ok {
		return hp.Host()
	}
	return nil
}

func v11CodeErr(c syncwire.Chunk) error {
	return &syncwire.CodeError{Code: c.Code, Msg: string(c.Payload)}
}

// v11Entry is one v1.1 protocol this node serves.
type v11Entry struct {
	proto  string
	maxReq uint64
	quota  *v11StreamQuota
	h      v11Handler
}

// v11Handlers lists the v1.1 servers (registered next to the v1 ones).
func (s *Service) v11Handlers() []v11Entry {
	return []v11Entry{
		{syncwire.ProtocolBodiesByRangeV2, syncwire.RangeRequestLen, v11RangeQuota, s.bodiesByRangeV2Handler},
		{syncwire.ProtocolCommitQC, syncwire.CommitQCRequestLen, v11SmallQuota, s.commitQCHandler},
	}
}

func (s *Service) registerV11Handlers() {
	for _, e := range s.v11Handlers() {
		s.registerV11(e.proto, e.maxReq, e.quota, e.h)
	}
}
