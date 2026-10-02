// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestZeroHashesNode covers the FULL / DIFF / leaf branches of zeroHashes.
func TestZeroHashesNode(t *testing.T) {
	// FULL record: tag + masks(6) + hashes. Everything from offset 7 on is
	// zeroed; the tag+masks prefix is left untouched.
	full := make([]byte, 20)
	full[0] = nodeRecFull
	for i := range full {
		if i > 0 {
			full[i] = byte(0xAA + i)
		}
	}
	out := zeroHashes(full, true, tDatcAccNode)
	for i := 0; i < 7; i++ {
		if out[i] != full[i] {
			t.Fatalf("prefix byte %d mutated: got %x want %x", i, out[i], full[i])
		}
	}
	for i := 7; i < len(out); i++ {
		if out[i] != 0 {
			t.Fatalf("hash byte %d not zeroed: %x", i, out[i])
		}
	}

	// DIFF record: tag + 8-byte header, zero from offset 9 on.
	diff := make([]byte, 16)
	diff[0] = nodeRecDiff
	for i := range diff {
		if i > 0 {
			diff[i] = byte(0x55 + i)
		}
	}
	out2 := zeroHashes(diff, true, tDatcStoNode)
	for i := 0; i < 9; i++ {
		if out2[i] != diff[i] {
			t.Fatalf("diff prefix byte %d mutated", i)
		}
	}
	for i := 9; i < len(out2); i++ {
		if out2[i] != 0 {
			t.Fatalf("diff hash byte %d not zeroed", i)
		}
	}

	// Leaf tables: value passed through unchanged.
	leaf := []byte{0x01, 0x02, 0x03}
	out3 := zeroHashes(leaf, false, tDatcLeafA)
	if string(out3) != string(leaf) {
		t.Fatalf("leaf value mutated: got %x want %x", out3, leaf)
	}
	out4 := zeroHashes(leaf, false, tDatcLeafS)
	if string(out4) != string(leaf) {
		t.Fatalf("leaf-S value mutated: got %x want %x", out4, leaf)
	}

	// Empty input returned as-is.
	if got := zeroHashes(nil, true, tDatcAccNode); len(got) != 0 {
		t.Fatalf("expected empty passthrough, got %x", got)
	}
}

// TestFloorRoot exercises the exact-hit, floor-via-Prev, and not-found paths
// of floorRoot against a real DatcRoots table.
func TestFloorRoot(t *testing.T) {
	modulesInit()
	dir := t.TempDir()
	db, err := openDatcDB(log.New(), dir, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	roots := map[uint64][32]byte{
		10: {1},
		20: {2},
		40: {4},
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for blk, h := range roots {
		var k [8]byte
		binary.BigEndian.PutUint64(k[:], blk)
		if err := tx.Put(tDatcRoots, k[:], h[:]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rtx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rtx.Rollback()

	// Below the first recorded root: not found.
	if _, ok, err := floorRoot(rtx, 5); err != nil || ok {
		t.Fatalf("expected not-found below first root, got ok=%v err=%v", ok, err)
	}
	// Exact hit at 20.
	h, ok, err := floorRoot(rtx, 20)
	if err != nil || !ok || !bytes.Equal(h[:], rootSlice(roots, 20)) {
		t.Fatalf("exact hit at 20: h=%x ok=%v err=%v", h, ok, err)
	}
	// Between 20 and 40 -> floors to 20.
	h, ok, err = floorRoot(rtx, 35)
	if err != nil || !ok || !bytes.Equal(h[:], rootSlice(roots, 20)) {
		t.Fatalf("floor at 35: h=%x ok=%v err=%v", h, ok, err)
	}
	// Above the last recorded root -> floors to 40 (Seek finds nothing, Last used).
	h, ok, err = floorRoot(rtx, 1000)
	if err != nil || !ok || !bytes.Equal(h[:], rootSlice(roots, 40)) {
		t.Fatalf("floor above last: h=%x ok=%v err=%v", h, ok, err)
	}
}

// TestHasAnyStorage covers both branches (present / absent) of hasAnyStorage.
func TestHasAnyStorage(t *testing.T) {
	modulesInit()
	dir := t.TempDir()
	db, err := openDatcDB(log.New(), dir, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var ah [32]byte
	ah[0] = 0xAB
	key := append(append([]byte{}, ah[:]...), make([]byte, 32)...)

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Put(modules.HashedStorage, key, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rtx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rtx.Rollback()

	ok, err := hasAnyStorage(rtx, ah)
	if err != nil || !ok {
		t.Fatalf("expected storage present, got ok=%v err=%v", ok, err)
	}

	var other [32]byte
	other[0] = 0xCD
	ok, err = hasAnyStorage(rtx, other)
	if err != nil || ok {
		t.Fatalf("expected no storage for unrelated addrHash, got ok=%v err=%v", ok, err)
	}
}

// --- die()/os.Exit subprocess re-exec pattern -----------------------------

// TestDieHelper is re-executed by TestDieExitsNonZero with DATC_HELPER=1 to
// observe die()'s exit code and stderr message without touching the real
// process.
func TestDieHelper(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "1" {
		t.Skip("run via TestDieExitsNonZero")
	}
	die("boom %d", 42)
}

func TestDieExitsNonZero(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestDieHelper$")
	cmd.Env = append(os.Environ(), "DATC_HELPER=1")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
	}
	if !contains(string(out), "boom 42") {
		t.Fatalf("expected stderr to contain %q, got %q", "boom 42", out)
	}
}

// runHelperSubprocess re-execs the test binary with -test.run=<pattern> and
// the given extra env vars, returning the process exit code and combined
// output. Shared by every die()/os.Exit subprocess test in this package.
func runHelperSubprocess(t *testing.T, pattern string, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+pattern)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %v (output: %s)", err, out)
	}
	return exitErr.ExitCode(), string(out)
}

func rootSlice(m map[uint64][32]byte, k uint64) []byte {
	v := m[k]
	return v[:]
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestOpenCSMissingExits covers openCS's die() path when the freezer table
// does not exist at all (NewFreezerTableReadOnly fails).
func TestOpenCSHelper(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "2" {
		t.Skip("run via TestOpenCSMissingExits")
	}
	_ = openCS(os.Getenv("DATC_HELPER_DIR"), "nosuchtable")
}

func TestOpenCSMissingExits(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenCSHelper$")
	cmd.Env = append(os.Environ(), "DATC_HELPER=2", "DATC_HELPER_DIR="+dir)
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
	}
	if !contains(string(out), "open nosuchtable") {
		t.Fatalf("expected stderr to mention the failed table, got %q", out)
	}
}

var _ = kv.ChainDB
