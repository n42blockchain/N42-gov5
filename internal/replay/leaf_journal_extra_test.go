// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestLeafEntryKeyIsLegacyZero(t *testing.T) {
	e := &LeafEntry{Tag: TagAccount, Address: types.Address{0x01}}
	if got := e.Key(); got != (types.Hash{}) {
		t.Fatalf("LeafEntry.Key() = %x, want zero hash (deprecated stub)", got)
	}
}

func TestLeafJournalFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.bin")
	j, err := NewLeafJournal(path)
	if err != nil {
		t.Fatalf("NewLeafJournal: %v", err)
	}
	defer j.Close()

	if err := j.WriteBlock(1, []LeafEntry{{Tag: TagAccount, Address: types.Address{0x01}}}); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	if err := j.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// A reader opened now (before Close) must see the flushed bytes.
	r, err := NewLeafJournalReader(path)
	if err != nil {
		t.Fatalf("NewLeafJournalReader: %v", err)
	}
	defer r.Close()
	be, err := r.ReadBlock()
	if err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	if be.BlockNum != 1 || len(be.Entries) != 1 {
		t.Fatalf("ReadBlock = %+v, want block 1 with 1 entry", be)
	}
}

// TestLeafJournalFlushAndWriteBlockStickyError covers the sticky-error
// short-circuit in Flush/WriteBlock once the journal has failed once.
func TestLeafJournalFlushAndWriteBlockStickyError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal2.bin")
	j, err := NewLeafJournal(path)
	if err != nil {
		t.Fatalf("NewLeafJournal: %v", err)
	}

	// Close the underlying file out from under the journal to force the
	// next write to fail and set the sticky error.
	if err := j.f.Close(); err != nil {
		t.Fatalf("close underlying file: %v", err)
	}
	// The buffered writer may not surface the error until enough bytes are
	// pushed through; write past the 1MB buffer to force a flush.
	big := make([]byte, 2<<20)
	if _, werr := j.w.Write(big); werr == nil {
		t.Skip("buffered writer did not surface the closed-file error; environment-dependent")
	} else {
		j.err = werr
	}

	if err := j.WriteBlock(2, nil); err == nil {
		t.Fatal("WriteBlock after a sticky error should fail")
	}
	if err := j.Flush(); err == nil {
		t.Fatal("Flush after a sticky error should fail")
	}
}
