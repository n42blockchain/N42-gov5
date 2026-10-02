package txlookup

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestService_Stats(t *testing.T) {
	const blocks = 4
	dir := t.TempDir()
	perBlock := make([][]types.Hash, blocks)
	for b := range perBlock {
		perBlock[b] = []types.Hash{hashFor(uint64(b), 0)}
	}
	if err := BuildSegmentFromSource(context.Background(), dir, 0, blocks, memSource(0, perBlock)); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	stats := svc.Stats()
	if stats == "" {
		t.Fatal("expected non-empty Stats() string")
	}
	t.Logf("Stats: %s", stats)
}

func TestService_Lookup_L0HotPath(t *testing.T) {
	// L0 (MDBX) hit should short-circuit before any L1 segment is consulted.
	dir := t.TempDir()
	// Build a service with zero segments (empty dir is fine; NewService
	// tolerates no existing segments via cscompact.OpenSegmentStore).
	emptyDir := t.TempDir()
	if err := BuildSegmentFromSource(context.Background(), emptyDir, 0, 1, memSource(0, [][]types.Hash{{hashFor(0, 0)}})); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc, err := NewService(emptyDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	db := memdb.New(dir)
	defer db.Close()

	txHash := hashFor(777, 0)
	wantBlock := uint64(777)

	rwTx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	encoded := []byte{
		byte(wantBlock >> 56), byte(wantBlock >> 48), byte(wantBlock >> 40), byte(wantBlock >> 32),
		byte(wantBlock >> 24), byte(wantBlock >> 16), byte(wantBlock >> 8), byte(wantBlock),
	}
	if err := rwTx.Put(modules.TxLookup, txHash[:], encoded); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := rwTx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer roTx.Rollback()

	got, err := svc.Lookup(roTx, txHash)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil || *got != wantBlock {
		t.Fatalf("Lookup (L0) = %v, want %d", got, wantBlock)
	}
}

func TestService_Lookup_NilTxUsesL1Only(t *testing.T) {
	const blocks = 3
	dir := t.TempDir()
	perBlock := make([][]types.Hash, blocks)
	for b := range perBlock {
		perBlock[b] = []types.Hash{hashFor(uint64(b), 0)}
	}
	if err := BuildSegmentFromSource(context.Background(), dir, 0, blocks, memSource(0, perBlock)); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	got, err := svc.Lookup(nil, hashFor(1, 0))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil || *got != 1 {
		t.Fatalf("Lookup(nil tx) = %v, want 1", got)
	}
}
