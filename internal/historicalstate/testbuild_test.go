package historicalstate

import (
	"sort"
	"testing"

	"github.com/n42blockchain/N42/internal/history"
)

// mustBuildSyntheticAccountStore builds account.{mphf,idx,kv} under dir
// from a (addr20 → []Change) map. Used by unit tests so we don't depend
// on a production store.
func mustBuildSyntheticAccountStore(t *testing.T, dir string, data map[[20]byte][]history.Change) {
	t.Helper()
	if len(data) == 0 {
		t.Fatal("empty input")
	}
	w, err := history.NewMPHFWriter(history.MPHFWriterOpts{
		BaseDir:  dir,
		Prefix:   "account",
		PageSize: 4,
		TmpDir:   dir + "/tmp",
		KeyCount: len(data),
		EtlBufMB: 1,
	})
	if err != nil {
		t.Fatalf("NewMPHFWriter: %v", err)
	}

	keys := make([][20]byte, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		for k := 0; k < 20; k++ {
			if keys[i][k] != keys[j][k] {
				return keys[i][k] < keys[j][k]
			}
		}
		return false
	})

	for _, k := range keys {
		blob := history.PackHistory(nil, data[k])
		if err := w.Append(k[:], blob); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("MPHFWriter.Close: %v", err)
	}
}

// mustBuildSyntheticStorageStore is the storage equivalent — key is the
// 52-byte addr||slot composite.
func mustBuildSyntheticStorageStore(t *testing.T, dir string, data map[[52]byte][]history.Change) {
	t.Helper()
	if len(data) == 0 {
		t.Fatal("empty input")
	}
	w, err := history.NewMPHFWriter(history.MPHFWriterOpts{
		BaseDir:  dir,
		Prefix:   "storage",
		PageSize: 4,
		TmpDir:   dir + "/tmp-stor",
		KeyCount: len(data),
		EtlBufMB: 1,
	})
	if err != nil {
		t.Fatalf("NewMPHFWriter: %v", err)
	}

	keys := make([][52]byte, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		for k := 0; k < 52; k++ {
			if keys[i][k] != keys[j][k] {
				return keys[i][k] < keys[j][k]
			}
		}
		return false
	})

	for _, k := range keys {
		blob := history.PackHistory(nil, data[k])
		if err := w.Append(k[:], blob); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("MPHFWriter.Close: %v", err)
	}
}
