// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
)

// buildFallbackFixture seals one tiny era (span = 256) holding empty
// blocks with real header/evidence codecs, registers it as the ancient
// store, and returns the canonical hashes. The hot DB keeps only the
// CanonicalHeader rows (as the seal pipeline does) — everything else
// must come from the era.
func buildFallbackFixture(t *testing.T) (hashes []types.Hash, span uint64) {
	t.Helper()
	span = 256
	dir := t.TempDir()
	w, err := ancientera.NewWriter(dir, ancientera.ClassChain, 0, span, 94, [32]byte{0xAA}, "test")
	if err != nil {
		t.Fatal(err)
	}
	we, err := ancientera.NewWriter(dir, ancientera.ClassExec, 0, span, 94, [32]byte{0xAA}, "test")
	if err != nil {
		t.Fatal(err)
	}
	wa, err := ancientera.NewWriter(dir, ancientera.ClassAux, 0, span, 94, [32]byte{0xAA}, "test")
	if err != nil {
		t.Fatal(err)
	}

	hashes = make([]types.Hash, span)
	var parent types.Hash
	for n := uint64(0); n < span; n++ {
		hdr := &block.Header{
			ParentHash: parent,
			Number:     uint256.NewInt(n),
			Time:       1_700_000_000 + n,
			Extra:      make([]byte, 32),
		}
		hash := hdr.Hash()
		hashes[n] = hash
		parent = hash
		hdrRaw := hdr.MarshalCompact()
		if len(hdrRaw) == 0 {
			t.Fatal("empty compact header")
		}
		ev := &ConsensusEvidence{View: n, BlockHash: hash, SignerCount: 0}
		chainEntry := ancientera.BlockEntry{
			ancientera.TblCanonicalHash: hash[:],
			ancientera.TblHeader:        hdrRaw,
			ancientera.TblEvidence:      ev.Marshal(),
		}
		// Empty block: baseTxId = 2n, txAmount = 2 (two system slots).
		bodyRaw := modules.BodyStorageValue(2*n, 2)
		execEntry := ancientera.BlockEntry{ancientera.TblBody: bodyRaw}
		auxEntry := ancientera.BlockEntry{}

		var h32 [32]byte
		copy(h32[:], hash[:])
		if err := w.AddBlock(chainEntry, h32); err != nil {
			t.Fatal(err)
		}
		if err := we.AddBlock(execEntry, h32); err != nil {
			t.Fatal(err)
		}
		if err := wa.AddBlock(auxEntry, h32); err != nil {
			t.Fatal(err)
		}
	}
	for _, wr := range []*ancientera.Writer{w, we, wa} {
		if _, _, err := wr.Finalize([32]byte{}); err != nil {
			t.Fatal(err)
		}
	}
	m, err := ancientera.RebuildManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(dir); err != nil {
		t.Fatal(err)
	}
	store, health, err := ancientera.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if health.Sealed != 3 || len(health.ChainGaps) != 0 {
		t.Fatalf("fixture health: %+v", health)
	}
	SetAncientStore(store)
	t.Cleanup(func() {
		SetAncientStore(nil)
		store.Close()
	})
	return hashes, span
}

func TestAncientFallbackReads(t *testing.T) {
	hashes, span := buildFallbackFixture(t)
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Hot DB has only the canonical index (as after seal).
	for n := uint64(0); n < span; n++ {
		if err := tx.Put(modules.HeaderCanonical, modules.EncodeBlockNumber(n), hashes[n][:]); err != nil {
			t.Fatal(err)
		}
	}

	const n = uint64(123)

	// Header falls back to the era (and only for the canonical hash).
	hdr := ReadHeader(tx, hashes[n], n)
	if hdr == nil || hdr.Number.Uint64() != n {
		t.Fatalf("ReadHeader via era failed: %+v", hdr)
	}
	if got := ReadHeaderRAW(tx, types.Hash{0xBB}, n); got != nil {
		t.Fatal("non-canonical hash must miss")
	}

	// Body assembly via era: empty block → zero real txs.
	body := ReadCanonicalBodyWithTransactions(tx, hashes[n], n)
	if body == nil {
		t.Fatal("ReadCanonicalBodyWithTransactions via era failed")
	}
	if len(body.Transactions()) != 0 {
		t.Fatalf("expected empty block, got %d txs", len(body.Transactions()))
	}

	// Whole-block read (header + body).
	blk := ReadBlock(tx, hashes[n], n)
	if blk == nil || blk.Number64().Uint64() != n {
		t.Fatal("ReadBlock via era failed")
	}

	// HasBlock consults the era exec class.
	if !HasBlock(tx, hashes[n], n) {
		t.Fatal("HasBlock must see sealed blocks")
	}

	// Evidence falls back to the era chain class.
	ev, err := ReadConsensusEvidence(tx, n)
	if err != nil || ev == nil {
		t.Fatalf("ReadConsensusEvidence via era: ev=%v err=%v", ev, err)
	}
	if ev.View != n || ev.BlockHash != hashes[n] {
		t.Fatalf("evidence content mismatch: %+v", ev)
	}

	// Receipts: empty block → nil, without error.
	if rc := ReadRawReceipts(tx, n); rc != nil {
		t.Fatalf("expected nil receipts for empty block, got %d", len(rc))
	}
}

// TestAncientFallbackDetached: with no store, sealed-range reads miss
// cleanly (no panics, plain not-found semantics).
func TestAncientFallbackDetached(t *testing.T) {
	SetAncientStore(nil)
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if hdr := ReadHeader(tx, types.Hash{0x01}, 5); hdr != nil {
		t.Fatal("expected miss")
	}
	if HasBlock(tx, types.Hash{0x01}, 5) {
		t.Fatal("expected no block")
	}
}
