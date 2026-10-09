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

package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/log"
)

// Frame messages share the ingest listener with the legacy batch feed. A
// batch starts with its transaction count (<= maxBatchSize); a frame starts
// with frameMagic ("FRM1" as little-endian bytes), which no batch count can
// equal, followed by the encoded frame (internal/frame). The server answers
// every frame with FrameReplySize bytes: a status byte and the FrameHash.
const (
	frameMagic     uint32 = 0x314D5246
	FrameReplySize        = 1 + types.HashLength
)

// FrameMagic is the 4-byte little-endian prefix that selects the frame path.
const FrameMagic = frameMagic

// Frame reply status codes.
const (
	FrameAccepted       byte = 0 // stored and queued for the pool
	FrameDuplicate      byte = 1 // already stored; nothing done
	FrameBusy           byte = 2 // bridge queue full or pool at hard cap; retry later
	FrameBadSignature   byte = 3 // gateway signature does not verify
	FrameUnknownGateway byte = 4 // gateway not in the allowlist
	FrameMalformed      byte = 5 // payload layout invalid; connection kept if framing held
	FrameDisabled       byte = 6 // frame ingest is off on this node; connection closed
)

// EnvFrameGateways lists the gateway addresses (comma-separated hex) whose
// frames this node admits. Empty disables frame ingest.
const EnvFrameGateways = "N42_FRAME_GATEWAYS"

var errFramesDisabled = errors.New("ingest: frame ingest disabled")

// FramePool is the batch admission the P1a bridge feeds.
type FramePool interface {
	AddLocals(txs []*transaction.Transaction) []error
}

// FrameConfig configures the frame path. Gateways must be non-empty.
type FrameConfig struct {
	Gateways []types.Address
	Store    *store.Store              // nil = store.NewFromEnv()
	Pool     FramePool                 // bridge target; nil = store only
	Signer   func() transaction.Signer // signer the pool validates with
	Workers  int                       // bridge workers (default 4)
	Queue    int                       // bridge queue depth in frames (default 256)
}

// ParseGateways parses a comma-separated list of hex addresses.
func ParseGateways(v string) ([]types.Address, error) {
	var out []types.Address
	for _, f := range strings.Split(v, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !types.IsHexAddress(f) {
			return nil, fmt.Errorf("ingest: bad gateway address %q", f)
		}
		out = append(out, types.HexToAddress(f))
	}
	return out, nil
}

// GatewaysFromEnv reads N42_FRAME_GATEWAYS.
func GatewaysFromEnv() ([]types.Address, error) {
	return ParseGateways(os.Getenv(EnvFrameGateways))
}

// FrameStats is a snapshot of the frame path's counters.
type FrameStats struct {
	Accepted, Duplicate, Busy, BadSig, UnknownGateway, Malformed uint64
	TxsAccepted                                                  uint64 // txs in accepted frames
	Bridged, BridgeRejected, BridgeBadTx                         uint64 // P1a bridge
	BridgeFrames                                                 uint64 // frames the bridge finished
	Store                                                        store.Stats
}

type bridgeJob struct {
	hash types.Hash
	f    *frame.Frame
}

type frameIngest struct {
	cfg   FrameConfig
	allow map[types.Address]struct{}
	queue chan bridgeJob

	accepted, duplicate, busy, badSig, unknownGW, malformed, txsAccepted atomic.Uint64
	bridged, bridgeRejected, bridgeBadTx, bridgeFrames                   atomic.Uint64
}

// EnableFrames turns on the gateway-signed frame path. Must be called
// before Start.
func (s *Server) EnableFrames(cfg FrameConfig) error {
	if len(cfg.Gateways) == 0 {
		return errors.New("ingest: frame ingest needs at least one gateway")
	}
	if cfg.Store == nil {
		cfg.Store = store.NewFromEnv()
	}
	if cfg.Workers < 1 {
		cfg.Workers = 4
	}
	if cfg.Queue < 1 {
		cfg.Queue = 256
	}
	fi := &frameIngest{
		cfg:   cfg,
		allow: make(map[types.Address]struct{}, len(cfg.Gateways)),
		queue: make(chan bridgeJob, cfg.Queue),
	}
	for _, g := range cfg.Gateways {
		fi.allow[g] = struct{}{}
	}
	s.frames = fi
	return nil
}

// FrameStore returns the frame store, or nil when frame ingest is off.
func (s *Server) FrameStore() *store.Store {
	if s.frames == nil {
		return nil
	}
	return s.frames.cfg.Store
}

// FrameStats returns the frame path's counters (zero when disabled).
func (s *Server) FrameStats() FrameStats {
	fi := s.frames
	if fi == nil {
		return FrameStats{}
	}
	return FrameStats{
		Accepted:       fi.accepted.Load(),
		Duplicate:      fi.duplicate.Load(),
		Busy:           fi.busy.Load(),
		BadSig:         fi.badSig.Load(),
		UnknownGateway: fi.unknownGW.Load(),
		Malformed:      fi.malformed.Load(),
		TxsAccepted:    fi.txsAccepted.Load(),
		Bridged:        fi.bridged.Load(),
		BridgeRejected: fi.bridgeRejected.Load(),
		BridgeBadTx:    fi.bridgeBadTx.Load(),
		BridgeFrames:   fi.bridgeFrames.Load(),
		Store:          fi.cfg.Store.Stats(),
	}
}

func (fi *frameIngest) start(ctx context.Context) {
	if fi.cfg.Pool != nil {
		for i := 0; i < fi.cfg.Workers; i++ {
			go fi.bridgeWorker(ctx)
		}
	}
	go fi.statsLoop(ctx)
	log.Info("Ingest frame path enabled", "gateways", len(fi.allow), "storeBudget", fi.cfg.Store.Stats().Budget,
		"bridgeWorkers", fi.cfg.Workers, "bridgeQueue", fi.cfg.Queue, "bridge", fi.cfg.Pool != nil)
}

// readFrame reads one frame after its magic word and returns the reply. A
// non-nil error means the stream can no longer be framed and the connection
// must close after the reply is sent.
//
// The per-frame cost on this goroutine is the read (one allocation sized by
// the header, one copy from the socket), one keccak and one ecrecover. The
// P1a bridge runs elsewhere and is reached by a non-blocking send: a full
// queue answers FrameBusy rather than stalling the connection.
func (s *Server) readFrame(r io.Reader) (reply [FrameReplySize]byte, err error) {
	fi := s.frames
	var hdr [frame.HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		reply[0] = FrameMalformed
		return reply, err
	}
	if fi == nil {
		reply[0] = FrameDisabled
		return reply, errFramesDisabled
	}
	h, err := frame.ParseHeader(hdr[:])
	if err != nil {
		fi.malformed.Add(1)
		reply[0] = FrameMalformed
		return reply, err
	}
	raw := make([]byte, h.Size())
	copy(raw, hdr[:])
	if _, err := io.ReadFull(r, raw[frame.HeaderSize:]); err != nil {
		reply[0] = FrameMalformed
		return reply, err
	}
	f, err := frame.Decode(raw)
	if err != nil {
		// The header's lengths held, so the stream is still framed.
		fi.malformed.Add(1)
		reply[0] = FrameMalformed
		return reply, nil
	}
	if _, ok := fi.allow[f.Gateway]; !ok {
		fi.unknownGW.Add(1)
		reply[0] = FrameUnknownGateway
		return reply, nil
	}
	hash, err := f.Verify()
	copy(reply[1:], hash[:])
	if err != nil {
		fi.badSig.Add(1)
		reply[0] = FrameBadSignature
		return reply, nil
	}
	st := fi.cfg.Store
	if st.Has(hash) {
		fi.duplicate.Add(1)
		reply[0] = FrameDuplicate
		return reply, nil
	}
	if fi.cfg.Pool != nil {
		if s.pool != nil && s.hardCap > 0 {
			if pending, _, _, _ := s.pool.Stats(); pending > s.hardCap {
				fi.busy.Add(1)
				reply[0] = FrameBusy
				return reply, nil
			}
		}
		select {
		case fi.queue <- bridgeJob{hash: hash, f: f}:
		default:
			// Not stored either, so the client's retry is not a duplicate.
			fi.busy.Add(1)
			reply[0] = FrameBusy
			return reply, nil
		}
	}
	if !st.PutEntry(hash, store.Entry{Raw: raw, Gas: frame.GasEstimate(f), Txs: int(f.TxCount)}) {
		// Lost a race with the same frame on another connection, or the
		// frame exceeds the whole budget; either way nothing is lost.
		fi.duplicate.Add(1)
		reply[0] = FrameDuplicate
		return reply, nil
	}
	fi.accepted.Add(1)
	fi.txsAccepted.Add(uint64(f.TxCount))
	reply[0] = FrameAccepted
	return reply, nil
}

// bridgeWorker is the P1a-TEMPORARY bridge from the frame store into the
// legacy transaction pool, so the existing miner can build blocks from
// frame-ingested transactions until P1b puts frame hashes in block bodies
// and execution reads frames directly. Each frame is decoded, its senders
// are recovered in one pass on this worker (filling the process-wide sender
// cache, so block import later hits it), and the whole frame enters the
// pool in a single AddLocals batch. Delete this, the queue and FramePool
// when P1b lands.
func (fi *frameIngest) bridgeWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-fi.queue:
			fi.bridgeFrame(job)
		}
	}
}

func (fi *frameIngest) bridgeFrame(job bridgeJob) {
	f := job.f
	var signer transaction.Signer
	if fi.cfg.Signer != nil {
		signer = fi.cfg.Signer()
	}
	raws := f.Txs()
	txs := make([]*transaction.Transaction, 0, len(raws))
	for _, raw := range raws {
		var tx *transaction.Transaction
		var err error
		switch f.TxType {
		case frame.TxTypeNative:
			tx = new(transaction.Transaction)
			err = tx.Unmarshal(raw)
		default:
			tx, err = transaction.DecodeEthereumTransaction(raw)
		}
		if err != nil {
			fi.bridgeBadTx.Add(1)
			continue
		}
		if signer != nil {
			from, err := transaction.Sender(signer, tx)
			if err != nil {
				fi.bridgeBadTx.Add(1)
				continue
			}
			tx.SetFrom(from)
		}
		txs = append(txs, tx)
	}
	if len(txs) > 0 {
		var ok uint64
		for _, err := range fi.cfg.Pool.AddLocals(txs) {
			if err == nil {
				ok++
			}
		}
		fi.bridged.Add(ok)
		fi.bridgeRejected.Add(uint64(len(txs)) - ok)
	}
	fi.bridgeFrames.Add(1)
}

func (fi *frameIngest) statsLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	var last uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a := fi.accepted.Load()
			if a == last {
				continue
			}
			st := fi.cfg.Store.Stats()
			log.Info("ingest frames", "accepted", a, "acceptedDelta", a-last, "txs", fi.txsAccepted.Load(),
				"dup", fi.duplicate.Load(), "busy", fi.busy.Load(), "badSig", fi.badSig.Load(),
				"bridged", fi.bridged.Load(), "bridgeRejected", fi.bridgeRejected.Load(), "bridgeQueue", len(fi.queue),
				"storeFrames", st.Frames, "storeBytes", st.Bytes, "evicted", st.Evicted)
			last = a
		}
	}
}
