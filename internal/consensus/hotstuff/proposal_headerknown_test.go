package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestOnBlockHeaderKnownZeroParentIsNoop exercises the early return when no
// parent hash is known yet: nothing should be recorded.
func TestOnBlockHeaderKnownZeroParentIsNoop(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, _ := newTestEngine(t, setup, 0)

	blockHash := hashFromByte(1)
	if err := follower.onBlockHeaderKnown(blockHash, types.Hash{}, 0); err != nil {
		t.Fatalf("onBlockHeaderKnown: %v", err)
	}
	if _, known := follower.importedParents[blockHash]; known {
		t.Fatal("expected no parent to be recorded for a zero parentHash")
	}
	if len(follower.headerKnownFIFO) != 0 {
		t.Fatalf("headerKnownFIFO len = %d, want 0", len(follower.headerKnownFIFO))
	}
}

// TestOnBlockHeaderKnownEvictsOldestPreservingImported drives
// onBlockHeaderKnown past MaxImportedBlocks and confirms the oldest header
// entry is evicted from importedParents UNLESS it is separately protected by
// checkedBlocks or importedBlocks (the eviction's own guard).
func TestOnBlockHeaderKnownEvictsOldestPreservingImported(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, _ := newTestEngine(t, setup, 0)

	protected := hashFromByte(1)
	follower.importedBlocks[protected] = true // will occupy FIFO slot 0 but must survive eviction
	if err := follower.onBlockHeaderKnown(protected, hashFromByte(9000), 0); err != nil {
		t.Fatalf("onBlockHeaderKnown(protected): %v", err)
	}

	// Fill past capacity with fresh, unprotected hashes.
	for i := 0; i < MaxImportedBlocks; i++ {
		h := hashFromByte(i + 100)
		if err := follower.onBlockHeaderKnown(h, hashFromByte(i+1), 0); err != nil {
			t.Fatalf("onBlockHeaderKnown(%d): %v", i, err)
		}
	}

	if len(follower.headerKnownFIFO) != MaxImportedBlocks {
		t.Fatalf("headerKnownFIFO len = %d, want %d", len(follower.headerKnownFIFO), MaxImportedBlocks)
	}
	// The protected hash's own FIFO slot was evicted, but its importedParents
	// entry must survive because importedBlocks[protected] is still true.
	if _, known := follower.importedParents[protected]; !known {
		t.Fatal("expected the protected (still-imported) hash's parent to survive eviction")
	}
}

// TestOnBlockHeaderKnownEvictsUnprotectedParent confirms that when the
// oldest header-known entry is NOT protected by checkedBlocks/importedBlocks,
// eviction actually deletes its importedParents entry.
func TestOnBlockHeaderKnownEvictsUnprotectedParent(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, _ := newTestEngine(t, setup, 0)

	victim := hashFromByte(1)
	if err := follower.onBlockHeaderKnown(victim, hashFromByte(9000), 0); err != nil {
		t.Fatalf("onBlockHeaderKnown(victim): %v", err)
	}
	for i := 0; i < MaxImportedBlocks; i++ {
		h := hashFromByte(i + 100)
		if err := follower.onBlockHeaderKnown(h, hashFromByte(i+1), 0); err != nil {
			t.Fatalf("onBlockHeaderKnown(%d): %v", i, err)
		}
	}
	if _, known := follower.importedParents[victim]; known {
		t.Fatal("expected the unprotected victim's parent entry to be evicted")
	}
}

// TestOnBlockCheckedEvictsOldestFIFOEntry drives onBlockChecked past
// MaxImportedBlocks and confirms the oldest checked entry is evicted from
// checkedBlocks, and from importedParents too when it is not separately
// tracked by importedBlocks.
func TestOnBlockCheckedEvictsOldestFIFOEntry(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, _ := newTestEngine(t, setup, 0)

	victim := hashFromByte(1)
	if err := follower.onBlockChecked(victim, hashFromByte(9000)); err != nil {
		t.Fatalf("onBlockChecked(victim): %v", err)
	}
	for i := 0; i < MaxImportedBlocks; i++ {
		h := hashFromByte(i + 100)
		if err := follower.onBlockChecked(h, hashFromByte(i+1)); err != nil {
			t.Fatalf("onBlockChecked(%d): %v", i, err)
		}
	}

	if follower.checkedBlocks[victim] {
		t.Fatal("expected the victim to be evicted from checkedBlocks")
	}
	if _, known := follower.importedParents[victim]; known {
		t.Fatal("expected the victim's parent entry to be evicted (not separately imported)")
	}
	if len(follower.checkedFIFO) != MaxImportedBlocks {
		t.Fatalf("checkedFIFO len = %d, want %d", len(follower.checkedFIFO), MaxImportedBlocks)
	}
}

// TestOnBlockCheckedEvictsButPreservesImportedParent confirms that when the
// oldest checked entry is also independently tracked by importedBlocks, its
// importedParents entry survives eviction from checkedBlocks/checkedFIFO.
func TestOnBlockCheckedEvictsButPreservesImportedParent(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, _ := newTestEngine(t, setup, 0)

	protected := hashFromByte(1)
	follower.importedBlocks[protected] = true
	if err := follower.onBlockChecked(protected, hashFromByte(9000)); err != nil {
		t.Fatalf("onBlockChecked(protected): %v", err)
	}
	for i := 0; i < MaxImportedBlocks; i++ {
		h := hashFromByte(i + 100)
		if err := follower.onBlockChecked(h, hashFromByte(i+1)); err != nil {
			t.Fatalf("onBlockChecked(%d): %v", i, err)
		}
	}

	if follower.checkedBlocks[protected] {
		t.Fatal("expected protected hash to be evicted from checkedBlocks regardless")
	}
	if _, known := follower.importedParents[protected]; !known {
		t.Fatal("expected the protected hash's parent entry to survive because it is still imported")
	}
}
