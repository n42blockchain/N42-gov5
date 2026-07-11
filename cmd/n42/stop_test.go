// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPIDFileLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	pid := os.Getpid()
	cleanup, err := acquirePIDFile(dataDir, pid)
	if err != nil {
		t.Fatal(err)
	}
	path := pidFilePath(dataDir)
	got, err := readPIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != pid {
		t.Fatalf("PID file = %d, want %d", got, pid)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PID file still exists after cleanup: %v", err)
	}
}

func TestPIDFileRejectsLiveDoubleStart(t *testing.T) {
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

func TestPIDFileReplacesDeadStalePID(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, pidFileName)
	const stalePID = 1 << 30
	if err := os.WriteFile(path, []byte(strconv.Itoa(stalePID)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cleanup, err := acquirePIDFile(dataDir, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, err := readPIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != os.Getpid() {
		t.Fatalf("replacement PID = %d, want %d", got, os.Getpid())
	}
}

func TestRemoveOwnedPIDFilePreservesNewOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), pidFileName)
	if err := os.WriteFile(path, []byte("12345\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedPIDFile(path, 54321); err != nil {
		t.Fatal(err)
	}
	if got, err := readPIDFile(path); err != nil || got != 12345 {
		t.Fatalf("new owner PID file changed: pid=%d err=%v", got, err)
	}
}
