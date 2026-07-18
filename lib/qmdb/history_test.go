// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"testing"
)

// mapHistoryStore is the in-RAM HistoryStore for tests.
type mapHistoryStore struct {
	stamps   map[uint64][]byte
	blockPos map[uint64]uint64
	posKeys  []uint64 // sorted block numbers with a blockPos record
	keyVers  map[Hash][]uint64
	topBand  map[uint64][]byte
	bandKeys []uint64
}

func newMapHistoryStore() *mapHistoryStore {
	return &mapHistoryStore{
		stamps:   map[uint64][]byte{},
		blockPos: map[uint64]uint64{},
		keyVers:  map[Hash][]uint64{},
		topBand:  map[uint64][]byte{},
	}
}

func (m *mapHistoryStore) PutDeathStamps(twigID uint64, blob []byte) error {
	m.stamps[twigID] = append([]byte(nil), blob...)
	return nil
}
func (m *mapHistoryStore) GetDeathStamps(twigID uint64) ([]byte, error) {
	return m.stamps[twigID], nil
}
func (m *mapHistoryStore) PutBlockPos(block, nextSlot uint64) error {
	if _, ok := m.blockPos[block]; !ok {
		m.posKeys = append(m.posKeys, block)
		sort.Slice(m.posKeys, func(i, j int) bool { return m.posKeys[i] < m.posKeys[j] })
	}
	m.blockPos[block] = nextSlot
	return nil
}
func (m *mapHistoryStore) FloorBlockPos(block uint64) (uint64, bool, error) {
	i := sort.Search(len(m.posKeys), func(i int) bool { return m.posKeys[i] > block })
	if i == 0 {
		return 0, false, nil
	}
	return m.blockPos[m.posKeys[i-1]], true, nil
}
func (m *mapHistoryStore) AppendKeyVersion(keyHash Hash, pos uint64) error {
	m.keyVers[keyHash] = append(m.keyVers[keyHash], pos)
	return nil
}
func (m *mapHistoryStore) KeyVersions(keyHash Hash) ([]uint64, error) {
	return m.keyVers[keyHash], nil
}
func (m *mapHistoryStore) PutTopBand(block uint64, packed []byte) error {
	if _, ok := m.topBand[block]; !ok {
		m.bandKeys = append(m.bandKeys, block)
		sort.Slice(m.bandKeys, func(i, j int) bool { return m.bandKeys[i] < m.bandKeys[j] })
	}
	m.topBand[block] = append([]byte(nil), packed...)
	return nil
}
func (m *mapHistoryStore) FloorTopBand(block uint64) ([]byte, bool, error) {
	i := sort.Search(len(m.bandKeys), func(i int) bool { return m.bandKeys[i] > block })
	if i == 0 {
		return nil, false, nil
	}
	return m.topBand[m.bandKeys[i-1]], true, nil
}

func hKey(i uint64) Hash {
	return sha256.Sum256(binary.LittleEndian.AppendUint64([]byte("hist-key"), i))
}
func hVal(i, round uint64) []byte {
	return fmt.Appendf(nil, "hist-val-%d-%d", i, round)
}

// TestProofAtHeightFullScan drives a multi-twig, multi-block workload with
// overwrites and deletes, records history, and verifies at EVERY height that
// (a) the reconstructed root equals the root recorded live at that block, and
// (b) sampled keys' membership proofs verify against it (or absence is
// correctly reported).
func TestProofAtHeightFullScan(t *testing.T) {
	tr := New()
	store := newMapHistoryStore()
	tr.SetHistoryRecorder(NewHistoryRecorder(store))

	const nKeys = 3000 // spans >1 twig with churn
	const nBlocks = 40
	liveAt := make([]map[uint64][]byte, nBlocks+1) // block -> key i -> value
	rootAt := make([]Hash, nBlocks+1)

	cur := map[uint64][]byte{}
	for b := uint64(1); b <= nBlocks; b++ {
		if err := tr.BeginBlock(b); err != nil {
			t.Fatalf("begin %d: %v", b, err)
		}
		// Deterministic churn: create, overwrite, delete.
		for i := uint64(0); i < 200; i++ {
			k := (b*131 + i*17) % nKeys
			switch {
			case i%10 == 9 && len(cur) > 50: // delete some existing
				tr.Delete(hKey(k))
				delete(cur, k)
			default:
				v := hVal(k, b)
				tr.Set(hKey(k), v)
				cur[k] = v
			}
		}
		if err := tr.EndBlock(); err != nil {
			t.Fatalf("end %d: %v", b, err)
		}
		rootAt[b] = tr.Root()
		snap := make(map[uint64][]byte, len(cur))
		for k, v := range cur {
			snap[k] = v
		}
		liveAt[b] = snap
	}

	for h := uint64(1); h <= nBlocks; h++ {
		// Root reconstruction via an absent key (cheap path).
		absent := sha256.Sum256([]byte("definitely-absent"))
		_, root, found, err := tr.ProofAtHeight(absent, h)
		if err != nil {
			t.Fatalf("h=%d absent: %v", h, err)
		}
		if found {
			t.Fatalf("h=%d: absent key reported found", h)
		}
		if root != rootAt[h] {
			t.Fatalf("h=%d root mismatch:\n want=%x\n got =%x", h, rootAt[h], root)
		}
		// Sampled present keys.
		checked := 0
		for k, v := range liveAt[h] {
			proof, root2, found, err := tr.ProofAtHeight(hKey(k), h)
			if err != nil {
				t.Fatalf("h=%d key=%d: %v", h, k, err)
			}
			if !found {
				t.Fatalf("h=%d key=%d: live key not found", h, k)
			}
			if root2 != rootAt[h] {
				t.Fatalf("h=%d key=%d root mismatch", h, k)
			}
			if !bytes.Equal(proof.Value, v) {
				t.Fatalf("h=%d key=%d value mismatch: want %q got %q", h, k, v, proof.Value)
			}
			if !VerifyProof(rootAt[h], proof) {
				t.Fatalf("h=%d key=%d proof does not verify", h, k)
			}
			if checked++; checked >= 5 {
				break
			}
		}
		// A key deleted before h must be absent.
		for k := uint64(0); k < nKeys; k++ {
			if _, live := liveAt[h][k]; !live {
				_, _, found, err := tr.ProofAtHeight(hKey(k), h)
				if err != nil {
					t.Fatalf("h=%d dead key=%d: %v", h, k, err)
				}
				if found {
					// Only keys that EVER existed matter; hKey(k) may never
					// have been set — found=false either way.
					t.Fatalf("h=%d key=%d reported live but is not", h, k)
				}
				break
			}
		}
	}
}

// TestReloadDiscardsPendingDeathStamps pins the latent-ghost fix: death stamps
// recorded by a block whose execution then fails (recordDeath ran, FlushHistory
// did NOT) must not survive a reload and get flushed at the failed block's
// height — that would stamp a still-live slot dead in the historical index.
func TestReloadDiscardsPendingDeathStamps(t *testing.T) {
	tr := New()
	store := newMapStore()
	hstore := newMapHistoryStore()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))
	tr.SetHistoryRecorder(NewHistoryRecorder(hstore))

	// Block 1: seed a key, flush + settle history, "commit".
	if err := tr.BeginBlock(1); err != nil {
		t.Fatalf("begin 1: %v", err)
	}
	tr.Set(hKey(1), hVal(1, 1))
	if err := tr.EndBlock(); err != nil {
		t.Fatalf("end 1: %v", err)
	}
	next, _, err := tr.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("flush 1: %v", err)
	}
	if err := tr.FlushHistory(); err != nil {
		t.Fatalf("flush history 1: %v", err)
	}

	// Block 2 executes and kills the key (recordDeath populates stampDelta) but
	// then "fails": no FlushHistory, no commit.
	if err := tr.BeginBlock(2); err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	tr.Delete(hKey(1))
	if len(tr.hist.stampDelta) == 0 {
		t.Fatal("precondition: block 2 delete should have recorded a pending death stamp")
	}

	// Recovery reload from the block-1 layout on disk.
	if err := tr.LoadFrom(store); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(tr.hist.stampDelta) != 0 {
		t.Fatalf("reload left %d ghost death stamp(s) pending", len(tr.hist.stampDelta))
	}

	// A subsequent clean block must not carry block-2's ghost death: flush and
	// confirm the key is still provable-live at block 1.
	_ = next
	proof, root, found, err := tr.ProofAtHeight(hKey(1), 1)
	if err != nil {
		t.Fatalf("proof at h=1: %v", err)
	}
	if !found {
		t.Fatal("key seeded at block 1 must still be live at block 1 after the discarded failure")
	}
	if !VerifyProof(root, proof) {
		t.Fatal("proof does not verify")
	}
}

// TestProofAtHeightSurvivesEvictionAndFlush: history reconstruction must work
// with the production memory tiers active (flush + entry eviction + twig-node
// eviction), reading frozen leaves back from blobs/cold rows.
func TestProofAtHeightSurvivesEvictionAndFlush(t *testing.T) {
	tr := New()
	store := newMapStore()
	hstore := newMapHistoryStore()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))
	tr.SetHistoryRecorder(NewHistoryRecorder(hstore))

	rootAt := map[uint64]Hash{}
	flushed := uint64(0)
	const nBlocks = 24
	for b := uint64(1); b <= nBlocks; b++ {
		if err := tr.BeginBlock(b); err != nil {
			t.Fatalf("begin: %v", err)
		}
		for i := uint64(0); i < 400; i++ {
			k := (b*37 + i) % 2500
			if i%7 == 6 {
				tr.Delete(hKey(k))
			} else {
				tr.Set(hKey(k), hVal(k, b))
			}
		}
		if err := tr.EndBlock(); err != nil {
			t.Fatalf("end: %v", err)
		}
		rootAt[b] = tr.Root()
		if b%6 == 0 {
			next, _, err := tr.FlushTo(store, flushed)
			if err != nil {
				t.Fatalf("flush: %v", err)
			}
			flushed = next
			tr.EvictThrough(next)
			tr.EvictTwigsThrough(next)
		}
	}

	for h := uint64(1); h <= nBlocks; h += 3 {
		k := (h*37 + 1) % 2500 // set in block h, likely still live at h
		proof, root, found, err := tr.ProofAtHeight(hKey(k), h)
		if err != nil {
			t.Fatalf("h=%d: %v", h, err)
		}
		if root != rootAt[h] {
			t.Fatalf("h=%d root mismatch:\n want=%x\n got =%x", h, rootAt[h], root)
		}
		if found && !VerifyProof(root, proof) {
			t.Fatalf("h=%d proof does not verify", h)
		}
	}
}
