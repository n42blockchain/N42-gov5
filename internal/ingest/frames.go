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
	FrameAccepted       byte = 0 // stored in the frame store
	FrameDuplicate      byte = 1 // already stored, or already in a block; nothing done
	FrameBusy           byte = 2 // reserved: not produced since the P1a bridge was removed
	FrameBadSignature   byte = 3 // gateway signature does not verify
	FrameUnknownGateway byte = 4 // gateway not in the allowlist
	FrameMalformed      byte = 5 // payload layout invalid; connection kept if framing held
	FrameDisabled       byte = 6 // frame ingest is off on this node; connection closed
)

// EnvFrameGateways lists the gateway addresses (comma-separated hex) whose
// frames this node admits. Empty disables frame ingest.
const EnvFrameGateways = "N42_FRAME_GATEWAYS"

var errFramesDisabled = errors.New("ingest: frame ingest disabled")

// FrameConfig configures the frame path. A frame is admitted when its
// signature verifies and its signer is in Gateways or, with IsValidator
// set, is a current validator (a leader's local frames of pool
// transactions). At least one of the two must be given.
type FrameConfig struct {
	Gateways []types.Address
	// IsValidator reports whether addr is in the current validator set
	// (nil = gateways only). Called per frame; must be cheap and safe for
	// concurrent use.
	IsValidator func(addr types.Address) bool
	Store       *store.Store // nil = store.NewFromEnv()
	// Lookup serves FRQ1 pulls for frames no longer in the store (the
	// Frames table); nil = store only.
	Lookup func(types.Hash) ([]byte, bool)
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
	PullServed, Pulled                                           uint64 // FRQ1 frames served / pulled and admitted
	Store                                                        store.Stats
}

type frameIngest struct {
	cfg   FrameConfig
	allow map[types.Address]struct{}

	accepted, duplicate, busy, badSig, unknownGW, malformed, txsAccepted atomic.Uint64
	pullServed, pulled                                                   atomic.Uint64
}

// EnableFrames turns on the gateway-signed frame path. Must be called
// before Start.
func (s *Server) EnableFrames(cfg FrameConfig) error {
	if len(cfg.Gateways) == 0 && cfg.IsValidator == nil {
		return errors.New("ingest: frame ingest needs at least one gateway or the validator set")
	}
	if cfg.Store == nil {
		cfg.Store = store.NewFromEnv()
	}
	fi := &frameIngest{
		cfg:   cfg,
		allow: make(map[types.Address]struct{}, len(cfg.Gateways)),
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
		PullServed:     fi.pullServed.Load(),
		Pulled:         fi.pulled.Load(),
		Store:          fi.cfg.Store.Stats(),
	}
}

// signerAllowed reports whether frames signed by addr are admitted: an
// allowlisted gateway or a current validator.
func (fi *frameIngest) signerAllowed(addr types.Address) bool {
	if _, ok := fi.allow[addr]; ok {
		return true
	}
	return fi.cfg.IsValidator != nil && fi.cfg.IsValidator(addr)
}

func (fi *frameIngest) start(ctx context.Context) {
	go fi.statsLoop(ctx)
	log.Info("Ingest frame path enabled", "gateways", len(fi.allow), "validators", fi.cfg.IsValidator != nil, "storeBudget", fi.cfg.Store.Stats().Budget)
}

// readFrame reads one frame after its magic word and returns the reply. A
// non-nil error means the stream can no longer be framed and the connection
// must close after the reply is sent.
//
// The per-frame cost on this goroutine is the read (one allocation sized by
// the header, one copy from the socket), one keccak, one ecrecover and the
// gas-estimate walk. The frame then waits in the store for a block builder
// (frame-referenced blocks, P1b); nothing reaches the transaction pool.
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
	if !fi.signerAllowed(f.Gateway) {
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
				"dup", fi.duplicate.Load(), "badSig", fi.badSig.Load(),
				"pulled", fi.pulled.Load(), "pullServed", fi.pullServed.Load(),
				"storeFrames", st.Frames, "storeBytes", st.Bytes, "evicted", st.Evicted, "removed", st.Removed)
			last = a
		}
	}
}
