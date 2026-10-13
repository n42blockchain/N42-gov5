package qmdb

import (
	"encoding/binary"
	"fmt"
)

// GetChecked distinguishes an absent live key from an unreadable entry. The
// execution-state reader must not interpret a cold-store/index failure as an
// empty account or zero slot. Legacy Get remains available to existing callers.
// Like Tree itself, this method requires external serialization.
func (t *Tree) GetChecked(key Hash) ([]byte, bool, error) {
	var slot uint64
	var present bool
	if idx, ok := t.idx.(interface {
		GetChecked(Hash) (uint64, bool, error)
	}); ok {
		var err error
		slot, present, err = idx.GetChecked(key)
		if err != nil {
			return nil, false, fmt.Errorf("QMDB index lookup: %w", err)
		}
	} else {
		slot, present = t.idx.Get(key)
	}
	if !present {
		return nil, false, nil
	}
	if slot >= t.nextSlot {
		return nil, false, fmt.Errorf("QMDB index points beyond entry log: slot %d", slot)
	}
	if slot >= t.entriesBase {
		i := slot - t.entriesBase
		if i >= uint64(len(t.entries)) {
			return nil, false, fmt.Errorf("QMDB indexed entry missing at slot %d", slot)
		}
		e := t.entries[i]
		if e.keyHash != key || !e.active {
			return nil, false, fmt.Errorf("QMDB indexed entry is mismatched or inactive at slot %d", slot)
		}
		return e.value, true, nil
	}
	if t.cold == nil {
		return nil, false, fmt.Errorf("QMDB cold reader unavailable for indexed slot %d", slot)
	}
	var kh Hash
	var value []byte
	var ok bool
	if cold, checked := t.cold.(interface {
		ColdEntryChecked(uint64) (Hash, []byte, bool, error)
	}); checked {
		var err error
		kh, value, ok, err = cold.ColdEntryChecked(slot)
		if err != nil {
			return nil, false, fmt.Errorf("QMDB cold read at slot %d: %w", slot, err)
		}
	} else {
		kh, value, ok = t.cold.ColdEntry(slot)
	}
	if !ok || kh != key {
		return nil, false, fmt.Errorf("QMDB indexed cold entry missing or mismatched at slot %d", slot)
	}
	return value, true, nil
}

func (c getterCold) ColdEntryChecked(slot uint64) (Hash, []byte, bool, error) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], slot)
	v, err := c.g.GetOne(EntryTable, b[:])
	if err != nil {
		return Hash{}, nil, false, err
	}
	if len(v) == 0 {
		return Hash{}, nil, false, nil
	}
	if len(v) < 32 {
		return Hash{}, nil, false, fmt.Errorf("truncated QMDB entry: %d bytes", len(v))
	}
	var kh Hash
	copy(kh[:], v[:32])
	value := make([]byte, len(v)-32)
	copy(value, v[32:])
	return kh, value, true, nil
}

// Prefix collision checks can themselves need a cold read. An unresolved
// holder is not evidence that the requested key is absent.
func (t *truncIndex) GetChecked(k Hash) (uint64, bool, error) {
	if slot, ok := t.ovf[k]; ok {
		return slot, true, nil
	}
	slot, ok := t.m[prefixOf(k)]
	if !ok {
		return 0, false, nil
	}
	switch t.holderStatus(slot, k) {
	case holderIsKey:
		return slot, true, nil
	case holderIsOther:
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("QMDB index holder cannot be read at slot %d", slot)
	}
}
