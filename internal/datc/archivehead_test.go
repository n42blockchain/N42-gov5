// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"context"
	"testing"

	log "github.com/n42blockchain/N42/lib/log/v3"
)

// TestArchiveHead covers archiveHead's happy path and its two error paths
// (no DB at the directory at all, and a DB with no DatcMeta/head key).
func TestArchiveHead(t *testing.T) {
	modulesInit()

	// Missing DB entirely: openArchiveDB fails.
	if _, err := archiveHead(t.TempDir()); err == nil {
		t.Fatal("expected an error opening a non-existent archive dir")
	}

	dir := t.TempDir()
	db, err := openDatcDB(log.New(), dir, 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	// DB exists but DatcMeta/head is unset: archiveHead must error.
	db.Close()
	db, err = openDatcDB(log.New(), dir, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := archiveHead(dir); err == nil {
		t.Fatal("expected an error for missing DatcMeta/head")
	}

	// Write DatcMeta/head and check the happy path.
	db, err = openDatcDB(log.New(), dir, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var v [8]byte
	v[6], v[7] = 0x01, 0x2c // 300
	if err := tx.Put(tDatcMeta, []byte("head"), v[:]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	head, err := archiveHead(dir)
	if err != nil {
		t.Fatalf("archiveHead: %v", err)
	}
	if head != 300 {
		t.Fatalf("expected head=300, got %d", head)
	}
}

// TestOpenArchiveMissingDirErrors covers OpenArchive's not-an-archive guard
// (the mdbx.dat stat check), which is cheap to exercise without a full
// archive on disk.
func TestOpenArchiveMissingDirErrors(t *testing.T) {
	modulesInit()
	if _, err := OpenArchive(t.TempDir(), ArchiveOptions{Readers: 1}); err == nil {
		t.Fatal("expected an error opening an empty directory as an archive")
	}
}
