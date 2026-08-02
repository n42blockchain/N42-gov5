// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// A process ID is not an identity. The operating system reuses IDs, and on a
// host running many short-lived helper processes a stopped node's ID is often
// live again within seconds. Until the PID file also recorded the process start
// time, a stale file naming a reused ID made the node refuse to start with
// "already running" — during a fleet roll that took three validators down in a
// row and dropped the chain below quorum.

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestPIDFileRecordsStartTime: a file written by this build carries the start
// time, so the identity check has something to compare against.
func TestPIDFileRecordsStartTime(t *testing.T) {
	if _, ok := processStartTime(os.Getpid()); !ok {
		t.Skip("platform does not report process start time")
	}
	dataDir := t.TempDir()
	cleanup, err := acquirePIDFile(dataDir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	rec, err := readPIDRecord(filepath.Join(dataDir, pidFileName))
	if err != nil {
		t.Fatal(err)
	}
	if rec.pid != os.Getpid() {
		t.Fatalf("pid = %d, want %d", rec.pid, os.Getpid())
	}
	if !rec.hasStart {
		t.Fatal("start time was not recorded")
	}
	want, _ := processStartTime(os.Getpid())
	if rec.startTime != want {
		t.Fatalf("start time = %d, want %d", rec.startTime, want)
	}
}

// TestReusedPIDIsTreatedAsStale is the regression. The file names a live
// process — this one — but with a start time that cannot be its own, which is
// exactly what a reused ID looks like. The node must start.
func TestReusedPIDIsTreatedAsStale(t *testing.T) {
	if _, ok := processStartTime(os.Getpid()); !ok {
		t.Skip("platform does not report process start time")
	}
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, pidFileName)
	real, _ := processStartTime(os.Getpid())
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", os.Getpid(), real-1)), 0600); err != nil {
		t.Fatal(err)
	}

	cleanup, err := acquirePIDFile(dataDir, os.Getpid())
	if err != nil {
		t.Fatalf("acquire over a reused-PID record failed: %v", err)
	}
	defer cleanup()

	rec, err := readPIDRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec.startTime != real {
		t.Fatalf("file was not replaced: start time %d, want %d", rec.startTime, real)
	}
}

// TestMatchingRecordStillBlocks: the same identity must still be refused, or
// the check would let two nodes share a data directory.
func TestMatchingRecordStillBlocks(t *testing.T) {
	dataDir := t.TempDir()
	cleanup, err := acquirePIDFile(dataDir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if _, err := acquirePIDFile(dataDir, os.Getpid()+1); !errors.Is(err, errPIDFileInUse) {
		t.Fatalf("second acquire error = %v, want errPIDFileInUse", err)
	}
}

// TestLegacyPIDFileStillReadable: a file from a build that recorded only the
// ID must keep working — an upgrade must not strand a node on a file it wrote
// itself moments earlier.
func TestLegacyPIDFileStillReadable(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, pidFileName)

	// Legacy live record: ID only, process running -> still refuses.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquirePIDFile(dataDir, os.Getpid()+1); !errors.Is(err, errPIDFileInUse) {
		t.Fatalf("legacy live record: err = %v, want errPIDFileInUse", err)
	}

	// Legacy stale record: ID only, process gone -> replaced.
	if err := os.WriteFile(path, []byte("1073741824\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanup, err := acquirePIDFile(dataDir, os.Getpid())
	if err != nil {
		t.Fatalf("legacy stale record: %v", err)
	}
	defer cleanup()
	if got, _ := readPIDFile(path); got != os.Getpid() {
		t.Fatalf("legacy stale record was not replaced: pid = %d", got)
	}
}
