package replay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestLeafJournalV2_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.journal")

	// Write
	j, err := NewLeafJournal(path)
	if err != nil {
		t.Fatal(err)
	}

	addr1 := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	addr2 := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	slot := types.HexToHash("0x01")

	entries := []LeafEntry{
		{Tag: TagAccount, Address: addr1, Value: []byte{1, 2, 3}},
		{Tag: TagAccount, Address: addr2, Value: nil}, // deletion
		{Tag: TagStorage, Address: addr1, Incarnation: 1, Slot: slot, Value: []byte{4, 5}},
	}

	if err := j.WriteBlock(42, entries); err != nil {
		t.Fatal(err)
	}
	// Empty block
	if err := j.WriteBlock(43, nil); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// Read back
	r, err := NewLeafJournalReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Block 42
	b, err := r.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if b.BlockNum != 42 {
		t.Fatalf("expected block 42, got %d", b.BlockNum)
	}
	if len(b.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(b.Entries))
	}

	// Check account entry
	e0 := b.Entries[0]
	if e0.Tag != TagAccount {
		t.Fatalf("entry 0: expected TagAccount, got %d", e0.Tag)
	}
	if e0.Address != addr1 {
		t.Fatalf("entry 0: address mismatch")
	}
	if len(e0.Value) != 3 || e0.Value[0] != 1 {
		t.Fatalf("entry 0: value mismatch: %v", e0.Value)
	}

	// Check deletion
	e1 := b.Entries[1]
	if e1.Tag != TagAccount || e1.Address != addr2 || e1.Value != nil {
		t.Fatalf("entry 1: expected deleted account for addr2")
	}

	// Check storage entry
	e2 := b.Entries[2]
	if e2.Tag != TagStorage {
		t.Fatalf("entry 2: expected TagStorage, got %d", e2.Tag)
	}
	if e2.Address != addr1 {
		t.Fatalf("entry 2: address mismatch")
	}
	if e2.Incarnation != 1 {
		t.Fatalf("entry 2: incarnation mismatch: %d", e2.Incarnation)
	}
	if e2.Slot != slot {
		t.Fatalf("entry 2: slot mismatch")
	}
	if len(e2.Value) != 2 || e2.Value[0] != 4 {
		t.Fatalf("entry 2: value mismatch: %v", e2.Value)
	}

	// Block 43 (empty)
	b2, err := r.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if b2.BlockNum != 43 || len(b2.Entries) != 0 {
		t.Fatalf("expected empty block 43, got %d entries", len(b2.Entries))
	}

	// EOF
	_, err = r.ReadBlock()
	if err == nil {
		t.Fatal("expected EOF")
	}

	// File size sanity check
	fi, _ := os.Stat(path)
	t.Logf("Journal file size: %d bytes", fi.Size())
}
