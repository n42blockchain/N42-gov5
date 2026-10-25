package txlookup

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestCommittedInventoryIgnoresMissingRangeHint(t *testing.T) {
	dir := t.TempDir()
	first := [][]types.Hash{{hashFor(5, 0)}, {hashFor(6, 0)}}
	second := [][]types.Hash{{hashFor(7, 0)}}
	if err := BuildSegmentFromSource(context.Background(), dir, 5, 7, memSource(5, first)); err != nil {
		t.Fatal(err)
	}
	if err := BuildSegmentFromSource(context.Background(), dir, 7, 8, memSource(7, second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, rangesFileName)); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.SetVerifier(func(blockNum uint64, txHash types.Hash) (uint64, bool) {
		return 0, blockNum >= 5 && blockNum < 8 && txHash == hashFor(blockNum, 0)
	})
	if first, end := svc.Coverage(); first != 5 || end != 8 {
		t.Fatalf("physical coverage = [%d,%d), want [5,8)", first, end)
	}
	for n := uint64(5); n < 8; n++ {
		got, err := svc.Lookup(nil, hashFor(n, 0))
		if err != nil || got == nil || *got != n {
			t.Fatalf("block %d lookup after lost hint = %v, %v", n, got, err)
		}
	}
}

func TestCommittedInventoryRejectsCorruptPayload(t *testing.T) {
	dir := t.TempDir()
	if err := BuildSegmentFromSource(context.Background(), dir, 9, 10,
		memSource(9, [][]types.Hash{{hashFor(9, 0)}})); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "txindex.0000.cdat")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	var bad [8]byte
	binary.LittleEndian.PutUint64(bad[:], 2) // index still has one key
	if _, err := f.WriteAt(bad[:], 4+8); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if svc, err := NewService(dir); err == nil {
		svc.Close()
		t.Fatal("corrupt transaction count advanced sealed coverage")
	}
}
