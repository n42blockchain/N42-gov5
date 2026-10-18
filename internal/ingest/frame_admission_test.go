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
	"net"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// TestFrameIngestBusyAtCap: with the store full of frames no committed
// block holds, a new frame is answered Busy and not stored, a resend of a
// stored frame is still a duplicate, and once a commit past the keep
// margin frees room the same frame is accepted.
func TestFrameIngestBusyAtCap(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	txs, _ := signedTxs(t, 30, 100) // 6 frames of 500
	frames := packFrames(t, gw, txs)
	if len(frames) != 6 {
		t.Fatalf("packed %d frames", len(frames))
	}
	// Signed transactions vary by a byte or two (R and S lose leading
	// zeros), so frame 3 may be longer than frame 0: size the budget so
	// three frames fit, a fourth does not, and frames 1-3 fit once frame 0
	// is evicted, whatever the lengths.
	budget := int64(len(frames[1]) + len(frames[2]) + max(len(frames[0]), len(frames[3])))
	st := store.NewWithKeep(budget, 2)
	srv := startFrameServer(t, nil, FrameConfig{
		Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)},
		Store:    st,
	})
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var hashes []types.Hash
	for i := 0; i < 3; i++ {
		status, h := sendFrame(t, conn, frames[i])
		if status != FrameAccepted {
			t.Fatalf("frame %d: status %d", i, status)
		}
		hashes = append(hashes, h)
	}
	status, h3 := sendFrame(t, conn, frames[3])
	if status != FrameBusy || st.Has(h3) {
		t.Fatalf("frame at cap: status %d stored %v", status, st.Has(h3))
	}
	if status, _ := sendFrame(t, conn, frames[0]); status != FrameDuplicate {
		t.Fatalf("resend at cap: status %d", status)
	}
	fs := srv.FrameStats()
	if fs.Busy != 1 || fs.Accepted != 3 || fs.Duplicate != 1 || fs.Store.Frames != 3 {
		t.Fatalf("stats %+v", fs)
	}
	// Block 10 takes frame 0 and commits; heads 11 and 12 pass the margin.
	st.Include(hashes[:1], 10)
	st.Commit(10, hashes[:1])
	st.Commit(11, nil)
	if status, _ := sendFrame(t, conn, frames[3]); status != FrameBusy {
		t.Fatalf("inside the keep margin: status %d", status)
	}
	st.Commit(12, nil)
	if status, _ := sendFrame(t, conn, frames[3]); status != FrameAccepted {
		t.Fatalf("after the keep margin: status %d", status)
	}
	if status, _ := sendFrame(t, conn, frames[0]); status != FrameDuplicate {
		t.Fatalf("evicted committed frame resent: status %d", status)
	}
}

// TestFramePullBypassesCap: a frame pulled for a proposal is admitted even
// when the local store is full.
func TestFramePullBypassesCap(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	gwAddr := crypto.PubkeyToAddress(gw.PublicKey)
	txs, _ := signedTxs(t, 10, 100)
	raws := packFrames(t, gw, txs)
	h0, _ := frame.HashOf(raws[0])
	h1, _ := frame.HashOf(raws[1])

	peerStore := store.New(1 << 24)
	peerStore.Put(h1, raws[1])
	peer := startFrameServer(t, nil, FrameConfig{Gateways: []types.Address{gwAddr}, Store: peerStore})

	// Room for either frame alone (their lengths differ by a byte or two),
	// never for both.
	local := store.New(int64(max(len(raws[0]), len(raws[1]))))
	if !local.Put(h0, raws[0]) {
		t.Fatal("seed put")
	}
	me := startFrameServer(t, nil, FrameConfig{Gateways: []types.Address{gwAddr}, Store: local})
	if local.Admit(h1, store.Entry{Raw: raws[1]}, false) != store.Busy {
		t.Fatal("local store not full")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := me.PullFrames(ctx, []string{peer.Addr()}, []types.Hash{h1})
	if len(got) != 1 || !local.Has(h1) {
		t.Fatalf("pull at cap: got %d stored %v", len(got), local.Has(h1))
	}
	if s := local.Stats(); s.Bypassed != 1 {
		t.Fatalf("stats %+v", s)
	}
}
