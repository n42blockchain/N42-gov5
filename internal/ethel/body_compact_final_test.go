// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// TestBodySegmentFlagsShortData covers bodySegmentFlags's < 2 byte guard.
func TestBodySegmentFlagsShortData(t *testing.T) {
	if got := bodySegmentFlags(nil); got != 0 {
		t.Fatalf("bodySegmentFlags(nil) = %x, want 0", got)
	}
	if got := bodySegmentFlags([]byte{0x01}); got != 0 {
		t.Fatalf("bodySegmentFlags(1 byte) = %x, want 0", got)
	}
}

// TestReadOneFrameErrors covers readOneFrame's three failure branches: the
// disk read failing, the zstd decompress failing, and the decompressed bytes
// not being a valid body segment.
func TestReadOneFrameErrors(t *testing.T) {
	blocks := synthFrameFixture(6)
	enc, dec := newTestZstdCodec(t)
	framed := encodeBodySegmentFramed(blocks, 1, enc, 2)
	fi, err := parseBodyFrameIndex(framed)
	if err != nil {
		t.Fatal(err)
	}

	failingReadAt := func(p []byte, off int64) error { return errShortRead }
	if _, _, err := readOneFrame(failingReadAt, 0, fi, 0, dec); err == nil {
		t.Fatal("expected readAt failure to propagate")
	}

	// A readAt that succeeds but hands back garbage must fail at decompress.
	garbageReadAt := func(p []byte, off int64) error {
		for i := range p {
			p[i] = 0xEE
		}
		return nil
	}
	if _, _, err := readOneFrame(garbageReadAt, 0, fi, 0, dec); err == nil {
		t.Fatal("expected decompress failure for garbage frame bytes")
	}

	// A readAt that returns a validly-decompressible but too-short payload
	// must fail at decodeBodySegment.
	shortEnc, shortDec := newTestZstdCodec(t)
	defer shortDec.Close()
	tooShort := shortEnc.EncodeAll([]byte{0x01, 0x02}, nil) // < 16 bytes raw
	badFi := &bodyFrameIndex{entries: []bodyFrameEntry{{compOffset: 0, compLen: uint32(len(tooShort)), blockStart: 0, blockCount: 1}}, dataStart: 0}
	badReadAt := func(p []byte, off int64) error {
		copy(p, tooShort)
		return nil
	}
	if _, _, err := readOneFrame(badReadAt, 0, badFi, 0, shortDec); err == nil {
		t.Fatal("expected decodeBodySegment failure for a too-short decompressed payload")
	}
}

// TestReadFrameHeaderReadAtError covers readFrameHeader's error path when the
// body-reading readAt call (the second one, for the frame-index body itself)
// fails after the 8-byte header succeeded.
func TestReadFrameHeaderReadAtError(t *testing.T) {
	blocks := synthFrameFixture(6)
	enc, _ := newTestZstdCodec(t)
	framed := encodeBodySegmentFramed(blocks, 1, enc, 2)

	calls := 0
	readAt := func(p []byte, off int64) error {
		calls++
		if calls == 1 {
			return copyFrom(framed, p, off)
		}
		return errShortRead
	}
	if _, err := readFrameHeader(readAt, 0, uint32(len(framed))); err == nil {
		t.Fatal("expected an error when the frame-index body read fails")
	}
}

func copyFrom(src, dst []byte, off int64) error {
	n := copy(dst, src[off:])
	if n < len(dst) {
		return errShortRead
	}
	return nil
}

// TestStartFrameAheadSkipsWhenAlreadyArmed covers the "already in flight for
// the exact same seg+frame" early-return branch: two reads landing in the
// same frame must not restart its successor's read-ahead.
func TestStartFrameAheadSkipsWhenAlreadyArmed(t *testing.T) {
	const frameSize = 4
	dir := t.TempDir()
	writeOneFramedSegmentStore(t, dir, frameSize*4, frameSize)

	r, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if sa := r.frameAhead; sa != nil {
			<-sa.done
		}
		r.Close()
	}()

	// Both reads land in frame 0, so each arms (or re-observes) frame 1 as
	// the successor: the second call must hit the same-slot skip rather than
	// joining and restarting it.
	if _, err := r.ReadBody(0); err != nil {
		t.Fatalf("read 0: %v", err)
	}
	firstSlot := r.frameAhead
	if _, err := r.ReadBody(1); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if r.frameAhead != firstSlot {
		t.Fatal("startFrameAhead restarted an already-armed slot for the same seg+frame")
	}
}

// TestBodyCompactStageRunEnsureTableError covers Run's EnsureTable failure
// path: pointing a freezer at a path whose directory has since been removed
// makes EnsureTable's NewFreezerTable call fail when it tries to create the
// bodies table.
func TestBodyCompactStageRunEnsureTableError(t *testing.T) {
	dir := t.TempDir()
	f, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("freezer.New: %v", err)
	}
	defer f.Close()

	// Replace the freezer's directory with a regular file: EnsureTable's
	// NewFreezerTable will try to os.MkdirAll(path) to create the bodies
	// table files and must fail, since path now names a file.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove freezer dir: %v", err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
		t.Fatalf("replace freezer dir with a file: %v", err)
	}

	stage := NewBodyCompactStage(f, filepath.Join(t.TempDir(), "out"))
	if err := stage.Run(context.Background()); err == nil {
		t.Fatal("expected Run to fail opening the bodies table once its directory is a file")
	}
}

// TestBodyCompactStageRunMkdirError covers Run's os.MkdirAll failure path: an
// output directory whose parent component is a regular file can never be
// created.
func TestBodyCompactStageRunMkdirError(t *testing.T) {
	const n = 1
	block := makeTestBlocks()[0]
	f := newBodyFreezer(t, n, func(i int) []byte { return buildGethBodyBytes(t, block) })
	defer f.Close()

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	outDir := filepath.Join(blocker, "out") // blocker is a file, not a dir

	stage := NewBodyCompactStage(f, outDir)
	if err := stage.Run(context.Background()); err == nil {
		t.Fatal("expected Run to fail creating an output dir under a file")
	} else if !errors.Is(err, os.ErrExist) && !os.IsExist(unwrapPathError(err)) {
		// Not all platforms report the same errno here; just confirm failure
		// (already asserted above) rather than over-specifying the message.
		t.Logf("MkdirAll error (informational): %v", err)
	}
}

func unwrapPathError(err error) error {
	for err != nil {
		if pe, ok := err.(*os.PathError); ok {
			return pe.Err
		}
		err = errors.Unwrap(err)
	}
	return err
}

// TestLoadSegmentErrors covers loadSegment's out-of-range guard and its
// propagation of a decode error from awaitAhead/decodeSegment.
func TestLoadSegmentErrors(t *testing.T) {
	dir := t.TempDir()
	writeOneSegmentStore(t, dir, []byte{0xff, 0xff, 0xff, 0xff}) // not valid zstd

	r, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	if err := r.loadSegment(int64(r.segments)); err == nil {
		t.Fatal("expected an out-of-range error from loadSegment")
	}
	if err := r.loadSegment(0); err == nil {
		t.Fatal("expected loadSegment to surface the corrupt segment's decode error")
	}
}
