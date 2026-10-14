package qmdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

type failedCheckedGetter struct{ err error }

func (g failedCheckedGetter) GetOne(string, []byte) ([]byte, error) { return nil, g.err }

type failedCheckedIndex struct {
	Index
	err error
}

func (i failedCheckedIndex) GetChecked(Hash) (uint64, bool, error) { return 0, false, i.err }

func TestCheckedReadsMatchLiveValuesAcrossEvictionAndChurn(t *testing.T) {
	tr, plain, store := New(), New(), newMapStore()
	tr.SetCold(ColdReaderFromGetter(store))
	var flushed uint64
	for round := uint64(0); round < 8; round++ {
		for i := uint64(0); i < 300; i++ {
			k := key((i + round*113) % 700)
			if (i+round)%7 == 0 {
				tr.Delete(k)
				plain.Delete(k)
			} else {
				tr.Set(k, val(round+i))
				plain.Set(k, val(round+i))
			}
		}
		flushed = flushAndEvict(t, tr, store, flushed)
		for i := uint64(0); i < 701; i++ {
			got, ok, err := tr.GetChecked(key(i))
			want, present := plain.Get(key(i))
			if err != nil || ok != present || !bytes.Equal(got, want) {
				t.Fatalf("round %d key %d: checked read differs: %v", round, i, err)
			}
		}
		if tr.Root() != plain.Root() {
			t.Fatal("reads changed commitment")
		}
	}
}

func TestCheckedReadRejectsUnusableIndexedEntries(t *testing.T) {
	for _, defect := range []string{"absent", "short", "wrong-key", "io", "no-reader", "index-io", "out-of-range", "inactive", "resident-key"} {
		t.Run(defect, func(t *testing.T) {
			tr, store := New(), newMapStore()
			k := key(1)
			tr.Set(k, val(5))
			tr.SetCold(ColdReaderFromGetter(store))
			failure := errors.New("injected storage read failure")
			if defect == "inactive" {
				tr.entries[0].active = false
			} else if defect == "resident-key" {
				tr.entries[0].keyHash = key(2)
			} else if defect == "out-of-range" {
				tr.idx.Put(k, tr.nextSlot)
			} else if defect == "index-io" {
				tr.idx = failedCheckedIndex{tr.idx, failure}
			} else {
				flushAndEvict(t, tr, store, 0)
				var slot [8]byte
				binary.BigEndian.PutUint64(slot[:], 0)
				switch defect {
				case "absent":
					delete(store[EntryTable], string(slot[:]))
				case "short":
					store[EntryTable][string(slot[:])] = []byte{1}
				case "wrong-key":
					store[EntryTable][string(slot[:])][0] ^= 0xff
				case "io":
					tr.SetCold(ColdReaderFromGetter(failedCheckedGetter{failure}))
				case "no-reader":
					tr.SetCold(nil)
				}
			}
			_, ok, err := tr.GetChecked(k)
			if err == nil || ok {
				t.Fatal("unreadable indexed account must not be returned as absent or valid")
			}
			if (defect == "io" || defect == "index-io") && !errors.Is(err, failure) {
				t.Fatal("underlying read error was lost")
			}
		})
	}
}

func TestCheckedTruncIndexDistinguishesCollisionAndUnreadableHolder(t *testing.T) {
	k1, k2 := keyWithPrefix(7, 1), keyWithPrefix(7, 2)
	slots := fakeSlots{0: k1}
	idx := NewTruncIndex(slots.resolve, 1).(*truncIndex)
	idx.Put(k1, 0)
	if _, ok, err := idx.GetChecked(k2); ok || err != nil {
		t.Fatal("resolved prefix collision should be an ordinary absence")
	}
	delete(slots, 0)
	if _, ok, err := idx.GetChecked(k1); ok || err == nil {
		t.Fatal("unresolved holder must not masquerade as an absent key")
	}
}
