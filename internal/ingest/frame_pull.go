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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/log"
)

// FRQ1: a node missing frames a block names pulls them from peers over the
// ingest listener.
//
//	request  "FRQ1" magic u32 | count u16 (1..MaxPullHashes) | count x FrameHash
//	reply    per requested hash, in order:
//	         status u8 | FrameHash 32 B | (status 0 only) length u32 | frame
//
// Status is FrameAccepted (0, found) or FrameNotFound. A peer serves from
// its frame store and then its Frames table. The puller admits every frame
// it receives exactly as FRM1 does (allowlisted gateway, signature) and
// checks it hashes to what it asked for.
const (
	frameReqMagic uint32 = 0x31515246 // "FRQ1"
	// MaxPullHashes bounds one FRQ1 request.
	MaxPullHashes = 4096
	// FrameNotFound answers a requested hash the peer does not hold.
	FrameNotFound byte = 7
)

// FrameReqMagic is the 4-byte little-endian prefix of a pull request.
const FrameReqMagic = frameReqMagic

// EnvFramePullPeers lists the ingest addresses (host:port, comma-separated)
// of the validators a node pulls missing frames from.
const EnvFramePullPeers = "N42_FRAME_PULL_PEERS"

// PullPeersFromEnv reads N42_FRAME_PULL_PEERS.
func PullPeersFromEnv() []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(EnvFramePullPeers), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// lookupFrame finds a frame by hash in the store, then the configured
// durable lookup (the Frames table).
func (fi *frameIngest) lookupFrame(h types.Hash) ([]byte, bool) {
	if raw, ok := fi.cfg.Store.Get(h); ok {
		return raw, true
	}
	if fi.cfg.Lookup != nil {
		return fi.cfg.Lookup(h)
	}
	return nil, false
}

// serveFrameRequest answers one FRQ1 request after its magic word.
func (s *Server) serveFrameRequest(rw io.ReadWriter) error {
	var cnt [2]byte
	if _, err := io.ReadFull(rw, cnt[:]); err != nil {
		return err
	}
	n := int(binary.LittleEndian.Uint16(cnt[:]))
	if n == 0 || n > MaxPullHashes {
		return fmt.Errorf("ingest: frame request of %d hashes", n)
	}
	req := make([]byte, n*types.HashLength)
	if _, err := io.ReadFull(rw, req); err != nil {
		return err
	}
	fi := s.frames
	if fi == nil {
		return errFramesDisabled
	}
	var out []byte
	served := 0
	for i := 0; i < n; i++ {
		var h types.Hash
		copy(h[:], req[i*types.HashLength:])
		raw, ok := fi.lookupFrame(h)
		if !ok {
			out = append(out, FrameNotFound)
			out = append(out, h[:]...)
			continue
		}
		out = append(out, FrameAccepted)
		out = append(out, h[:]...)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(raw))) // #nosec G115 -- bounded by MaxFrameSize
		out = append(out, raw...)
		served++
	}
	fi.pullServed.Add(uint64(served))
	_, err := rw.Write(out)
	return err
}

// admitPulled verifies a pulled frame as FRM1 admission does, checks it is
// the frame asked for, and stores it. It returns false for anything that
// fails; a frame the store already holds or refuses as a resend is still
// good to use.
func (fi *frameIngest) admitPulled(want types.Hash, raw []byte) bool {
	f, err := frame.Decode(raw)
	if err != nil {
		return false
	}
	if _, ok := fi.allow[f.Gateway]; !ok {
		return false
	}
	h, err := f.Verify()
	if err != nil || h != want {
		return false
	}
	fi.cfg.Store.PutEntry(h, store.Entry{Raw: raw, Gas: frame.GasEstimate(f), Txs: int(f.TxCount)})
	return true
}

// PullFrames asks every peer for the hashes concurrently and returns the
// frames that arrived and passed admission. It returns early once every
// hash is found; ctx bounds the whole pull.
func (s *Server) PullFrames(ctx context.Context, peers []string, hashes []types.Hash) map[types.Hash][]byte {
	got := make(map[types.Hash][]byte, len(hashes))
	fi := s.frames
	if fi == nil || len(peers) == 0 || len(hashes) == 0 {
		return got
	}
	if len(hashes) > MaxPullHashes {
		hashes = hashes[:MaxPullHashes]
	}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		done = make(chan struct{})
		once sync.Once
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, peer := range peers {
		wg.Add(1)
		go func(peer string) {
			defer wg.Done()
			err := pullFrom(ctx, peer, hashes, func(h types.Hash, raw []byte) {
				mu.Lock()
				_, have := got[h]
				mu.Unlock()
				if have || !fi.admitPulled(h, raw) {
					return
				}
				mu.Lock()
				got[h] = raw
				complete := len(got) == len(hashes)
				mu.Unlock()
				if complete {
					once.Do(func() { close(done) })
				}
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Debug("frame pull failed", "peer", peer, "err", err)
			}
		}(peer)
	}
	all := make(chan struct{})
	go func() { wg.Wait(); close(all) }()
	select {
	case <-done:
	case <-all:
	case <-ctx.Done():
	}
	cancel()
	<-all
	mu.Lock()
	defer mu.Unlock()
	fi.pulled.Add(uint64(len(got)))
	out := make(map[types.Hash][]byte, len(got))
	for h, raw := range got {
		out[h] = raw
	}
	return out
}

// pullFrom runs one FRQ1 exchange with peer and hands every found frame to
// found, in reply order.
func pullFrom(ctx context.Context, peer string, hashes []types.Hash, found func(types.Hash, []byte)) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", peer)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	req := make([]byte, 0, 6+len(hashes)*types.HashLength)
	req = binary.LittleEndian.AppendUint32(req, frameReqMagic)
	req = binary.LittleEndian.AppendUint16(req, uint16(len(hashes))) // #nosec G115 -- bounded by MaxPullHashes
	for _, h := range hashes {
		req = append(req, h[:]...)
	}
	if _, err := conn.Write(req); err != nil {
		return err
	}
	var head [1 + types.HashLength]byte
	for range hashes {
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			return err
		}
		var h types.Hash
		copy(h[:], head[1:])
		if head[0] != FrameAccepted {
			continue
		}
		var l [4]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return err
		}
		n := binary.LittleEndian.Uint32(l[:])
		if n > frame.MaxFrameSize {
			return fmt.Errorf("ingest: pulled frame of %d bytes", n)
		}
		raw := make([]byte, n)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return err
		}
		found(h, raw)
	}
	return nil
}
