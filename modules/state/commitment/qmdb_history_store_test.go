// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

func histKey(i uint64) qmdb.Hash {
	return sha256.Sum256(binary.LittleEndian.AppendUint64([]byte("mdbx-hist-key"), i))
}

// TestQMDBHistoryMDBXStoreFullScan mirrors the lib-level full-scan equivalence
// test through the MDBX-backed HistoryStore (real tables, DupSort key-version
// index, floor cursors) and the computer's block brackets + FlushTo cadence.
func TestQMDBHistoryMDBXStoreFullScan(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init() // register the N42 table list (history tables included)
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	rc := NewQMDBRootComputer()
	rc.EnableHistory(tx)

	const nBlocks = 30
	rootAt := make([]qmdb.Hash, nBlocks+1)
	liveAt := make([]map[uint64][]byte, nBlocks+1)
	cur := map[uint64][]byte{}

	for b := uint64(1); b <= nBlocks; b++ {
		if err := rc.BeginBlockHistory(b); err != nil {
			t.Fatalf("begin %d: %v", b, err)
		}
		for i := uint64(0); i < 300; i++ {
			k := (b*97 + i*13) % 2200
			if i%9 == 8 && len(cur) > 40 {
				rc.Tree().Delete(histKey(k))
				delete(cur, k)
			} else {
				v := fmt.Appendf(nil, "v-%d-%d", k, b)
				rc.Tree().Set(histKey(k), v)
				cur[k] = v
			}
		}
		if err := rc.EndBlockHistory(); err != nil {
			t.Fatalf("end %d: %v", b, err)
		}
		rootAt[b] = rc.Tree().Root()
		snap := make(map[uint64][]byte, len(cur))
		for k, v := range cur {
			snap[k] = v
		}
		liveAt[b] = snap
		if b%10 == 0 {
			if _, err := rc.FlushTo(tx); err != nil { // also flushes death stamps
				t.Fatalf("flush: %v", err)
			}
		}
	}

	for h := uint64(1); h <= nBlocks; h += 2 {
		absent := sha256.Sum256([]byte("no-such-key"))
		_, root, found, err := rc.ProofAtHeight(absent, h)
		if err != nil {
			t.Fatalf("h=%d absent: %v", h, err)
		}
		if found || root != rootAt[h] {
			t.Fatalf("h=%d root mismatch (found=%v):\n want=%x\n got =%x", h, found, rootAt[h], root)
		}
		checked := 0
		for k, v := range liveAt[h] {
			proof, root2, found, err := rc.ProofAtHeight(histKey(k), h)
			if err != nil {
				t.Fatalf("h=%d k=%d: %v", h, k, err)
			}
			if !found || root2 != rootAt[h] || !bytes.Equal(proof.Value, v) {
				t.Fatalf("h=%d k=%d bad proof (found=%v)", h, k, found)
			}
			if !qmdb.VerifyProof(rootAt[h], proof) {
				t.Fatalf("h=%d k=%d proof does not verify", h, k)
			}
			if checked++; checked >= 4 {
				break
			}
		}
	}
}
