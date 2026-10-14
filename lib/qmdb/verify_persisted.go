package qmdb

import "fmt"

// VerifyPersistedValues authenticates every live persisted entry against the
// frozen leaves of a loaded forest. LoadFrom can reproduce the root using leaf
// blobs even when entry values are missing or changed. Offline exporters need
// this stronger check before using those values to authenticate plain tables.
// Visit slots in order so at most one evicted twig is hydrated at a time.
// The caller must keep g pinned to the snapshot used by LoadFrom.
func (t *Tree) VerifyPersistedValues(g Getter) error {
	if t.LiveBits() != t.LiveCount() {
		return fmt.Errorf("qmdb: live bits and indexed entries disagree")
	}
	for id, tw := range t.twigs {
		if tw == nil || tw.live == 0 {
			continue
		}
		if err := t.verifyPersistedTwig(g, id); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tree) verifyPersistedTwig(g Getter, id int) error {
	tw := t.twigs[id]
	evicted := tw.nodes == nil
	if err := t.ensureHydrated(id); err != nil {
		return err
	}
	if evicted {
		defer func() { tw.nodes = nil }()
	}
	for local := uint64(0); local < TwigSize; local++ {
		if !tw.bit(local) {
			continue
		}
		slot := uint64(id)*TwigSize + local
		if slot >= t.nextSlot {
			return fmt.Errorf("qmdb: live bit beyond entry log at slot %d", slot)
		}
		encoded, err := g.GetOne(EntryTable, be8(slot))
		if err != nil {
			return fmt.Errorf("qmdb: read live slot %d: %w", slot, err)
		}
		if len(encoded) < 32 {
			return fmt.Errorf("qmdb: live entry missing or truncated at slot %d", slot)
		}
		var key Hash
		copy(key[:], encoded[:32])
		if hashLeaf(key, encoded[32:]) != tw.nodes[TwigSize+local] {
			return fmt.Errorf("qmdb: live entry hash mismatch at slot %d", slot)
		}
		if indexed, ok := t.idx.Get(key); !ok || indexed != slot {
			return fmt.Errorf("qmdb: live entry index mismatch at slot %d", slot)
		}
	}
	return nil
}
