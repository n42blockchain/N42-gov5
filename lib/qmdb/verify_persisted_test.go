package qmdb

import (
	"errors"
	"testing"
)

type verifyFailedGetter struct{ err error }

func (g verifyFailedGetter) GetOne(string, []byte) ([]byte, error) { return nil, g.err }

func TestVerifyPersistedValuesDetectsCorruptionHiddenByLeafBlobs(t *testing.T) {
	for _, defect := range []string{"healthy", "value", "missing", "short", "index", "io"} {
		t.Run(defect, func(t *testing.T) {
			tr, store := New(), newMapStore()
			tr.SetLeafStore(LeafStoreFromGetter(store))
			for i := uint64(0); i < TwigSize+1; i++ {
				tr.Set(key(i), val(i))
			}
			root := tr.Root()
			if _, _, err := tr.FlushTo(store, 0); err != nil {
				t.Fatal(err)
			}
			// The persisted frozen leaves still reproduce root after entry damage.
			switch defect {
			case "value":
				store[EntryTable][string(be8(0))][32] ^= 0xff
			case "missing":
				delete(store[EntryTable], string(be8(0)))
			case "short":
				store[EntryTable][string(be8(0))] = []byte{1}
			}
			loaded := New()
			if err := loaded.LoadFrom(store); err != nil {
				t.Fatal(err)
			}
			if loaded.Root() != root {
				t.Fatal("fixture must retain the authenticated forest root")
			}
			resident := loaded.ResidentTwigLeaves()
			var getter Getter = store
			failure := errors.New("injected read failure")
			if defect == "io" {
				getter = verifyFailedGetter{failure}
			}
			if defect == "index" {
				loaded.idx.Put(key(0), 1)
			}
			err := loaded.VerifyPersistedValues(getter)
			if (err == nil) != (defect == "healthy") {
				t.Fatalf("defect=%s err=%v", defect, err)
			}
			if defect == "io" && !errors.Is(err, failure) {
				t.Fatalf("lost read error: %v", err)
			}
			if loaded.Root() != root || loaded.ResidentTwigLeaves() != resident {
				t.Fatal("verification changed root or retained evicted twig nodes")
			}
		})
	}
}
