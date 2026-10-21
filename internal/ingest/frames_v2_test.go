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

// checkEntryHashes: the store kept one entry hash per transaction, each the
// transaction's hash.
func checkEntryHashes(t *testing.T, st *store.Store, h types.Hash, raw []byte) {
	t.Helper()
	e, ok := st.GetEntry(h)
	if !ok {
		t.Fatal("frame not stored")
	}
	txs, dropped, err := frame.DecodeTxs(raw)
	if err != nil || dropped != 0 || len(e.TxHashes) != len(txs) {
		t.Fatalf("entry hashes %d for %d txs (%v)", len(e.TxHashes), len(txs), err)
	}
	for i, tx := range txs {
		if e.TxHashes[i] != tx.Hash() {
			t.Fatalf("entry hash %d is not the tx hash", i)
		}
	}
}

// TestFrameIngestHashV2: under frame hash v2 a pushed frame is acked with
// its v2 hash, stored under it with its entry hashes, and a pull admits
// it under the same hash; a frame signed under v1 is refused.
func TestFrameIngestHashV2(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	gwAddr := crypto.PubkeyToAddress(gw.PublicKey)
	txs, _ := signedTxs(t, 10, 20)
	v1 := packFrames(t, gw, txs[:50])[0]
	defer frame.SetHashV2ForTest(true)()
	frames := packFrames(t, gw, txs)

	peerStore := store.New(64 << 20)
	srv := startFrameServer(t, nil, FrameConfig{Gateways: []types.Address{gwAddr}, Store: peerStore})
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if status, _ := sendFrame(t, conn, v1); status != FrameBadSignature {
		t.Fatalf("v1 frame under v2: status %d", status)
	}
	raw := frames[0]
	status, h := sendFrame(t, conn, raw)
	if status != FrameAccepted {
		t.Fatalf("status %d", status)
	}
	if want, _ := frame.HashOf(raw); h != want || h == crypto.Keccak256Hash(raw[:len(raw)-frame.SigSize]) {
		t.Fatal("ack is not the v2 hash")
	}
	checkEntryHashes(t, peerStore, h, raw)

	local := store.New(64 << 20)
	me := startFrameServer(t, nil, FrameConfig{Gateways: []types.Address{gwAddr}, Store: local})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got := me.PullFrames(ctx, []string{srv.Addr()}, []types.Hash{h}); len(got) != 1 {
		t.Fatalf("pulled %d", len(got))
	}
	checkEntryHashes(t, local, h, raw)
}
