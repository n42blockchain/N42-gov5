// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ancientera

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// testSpan keeps fixtures fast: 4 frames of 64 blocks.
const testSpan = 4 * FrameBlocks

func testHash(n uint64) (h [32]byte) {
	binary.BigEndian.PutUint64(h[:8], n)
	h[31] = 0xEE
	return h
}

func buildTestEra(t *testing.T, dir string, class Class, era uint64) (string, *EraMeta) {
	t.Helper()
	w, err := NewWriter(dir, class, era, testSpan, 94, testHash(0), "unit-test")
	if err != nil {
		t.Fatal(err)
	}
	start := era * testSpan
	for n := start; n < start+testSpan; n++ {
		e := BlockEntry{}
		switch class {
		case ClassChain:
			h := testHash(n)
			e[TblCanonicalHash] = h[:]
			e[TblHeader] = []byte(fmt.Sprintf("header-%d", n))
			e[TblEvidence] = []byte(fmt.Sprintf("qc-%d", n))
		case ClassExec:
			e[TblBody] = []byte(fmt.Sprintf("body-%d", n))
			if n%3 != 0 { // some blocks empty
				e[TblTxs] = EncodeU32List([][]byte{[]byte("tx1"), []byte(fmt.Sprintf("tx2-%d", n))})
				e[TblReceipts] = []byte(fmt.Sprintf("rcpt-%d", n))
				e[TblLogs] = EncodeLogs([]LogRec{{TxID: 0, Data: []byte("log0")}})
			}
		case ClassAux:
			if n%2 == 0 {
				e[TblWitness] = []byte(fmt.Sprintf("wit-%d", n))
			}
			e[TblAcctCS] = EncodeU32List([][]byte{[]byte(fmt.Sprintf("acs-%d", n))})
			e[TblStorCS] = EncodeKVPairs([]KVPair{{Suffix: []byte("addr-inc"), Value: []byte(fmt.Sprintf("scs-%d", n))}})
		}
		if err := w.AddBlock(e, testHash(n)); err != nil {
			t.Fatal(err)
		}
	}
	var parent [32]byte
	if start > 0 {
		parent = testHash(start - 1)
	}
	path, meta, err := w.Finalize(parent)
	if err != nil {
		t.Fatal(err)
	}
	return path, meta
}

func writeManifestFor(t *testing.T, dir string) *Manifest {
	t.Helper()
	m, err := RebuildManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(dir); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEraRoundTripAllClasses(t *testing.T) {
	dir := t.TempDir()
	for _, cl := range Classes {
		buildTestEra(t, dir, cl, 0)
	}
	writeManifestFor(t, dir)
	s, h, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if h.Sealed != 3 || len(h.Degraded) != 0 || len(h.ChainGaps) != 0 {
		t.Fatalf("health: %+v", h)
	}
	if s.SealedEnd() != testSpan {
		t.Fatalf("sealedEnd=%d", s.SealedEnd())
	}
	for n := uint64(0); n < testSpan; n++ {
		hash, hdr, ev, err := s.Chain(n)
		if err != nil {
			t.Fatalf("chain %d: %v", n, err)
		}
		if hash != testHash(n) || string(hdr) != fmt.Sprintf("header-%d", n) || string(ev) != fmt.Sprintf("qc-%d", n) {
			t.Fatalf("chain %d content mismatch", n)
		}
		ex, err := s.Exec(n)
		if err != nil {
			t.Fatalf("exec %d: %v", n, err)
		}
		if string(ex.BodyRaw) != fmt.Sprintf("body-%d", n) {
			t.Fatalf("body %d mismatch", n)
		}
		if n%3 != 0 {
			if len(ex.Txs) != 2 || string(ex.Txs[1]) != fmt.Sprintf("tx2-%d", n) {
				t.Fatalf("txs %d mismatch: %d", n, len(ex.Txs))
			}
			if len(ex.Logs) != 1 || ex.Logs[0].TxID != 0 {
				t.Fatalf("logs %d mismatch", n)
			}
		} else if len(ex.Txs) != 0 || ex.ReceiptsRaw != nil {
			t.Fatalf("empty block %d should have no txs/receipts", n)
		}
		ax, err := s.Aux(n)
		if err != nil {
			t.Fatalf("aux %d: %v", n, err)
		}
		if n%2 == 0 && string(ax.Witness) != fmt.Sprintf("wit-%d", n) {
			t.Fatalf("witness %d mismatch", n)
		}
		if len(ax.AcctCS) != 1 || string(ax.AcctCS[0]) != fmt.Sprintf("acs-%d", n) {
			t.Fatalf("acctcs %d mismatch", n)
		}
		if len(ax.StorCS) != 1 || string(ax.StorCS[0].Suffix) != "addr-inc" || string(ax.StorCS[0].Value) != fmt.Sprintf("scs-%d", n) {
			t.Fatalf("storcs %d mismatch", n)
		}
	}
	// Beyond sealed horizon.
	if st := s.State(ClassChain, testSpan); st != RangeNotSealed {
		t.Fatalf("state past horizon = %v", st)
	}
}

// TestEraDeterminism: building the same content twice yields identical
// bytes (creator excluded from payload; here creator equal too).
func TestEraDeterminism(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	p1, m1 := buildTestEra(t, d1, ClassExec, 0)
	p2, m2 := buildTestEra(t, d2, ClassExec, 0)
	b1, err := os.ReadFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(p2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatal("same content produced different era bytes")
	}
	if m1.PayloadBlake3 != m2.PayloadBlake3 {
		t.Fatal("payload hash differs")
	}
}

// TestEraChainLinkage: era 1 must link to era 0 via parent hash.
func TestEraChainLinkage(t *testing.T) {
	dir := t.TempDir()
	buildTestEra(t, dir, ClassChain, 0)
	buildTestEra(t, dir, ClassChain, 1)
	writeManifestFor(t, dir)
	s, h, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(h.ChainGaps) != 0 {
		t.Fatalf("unexpected chain gaps: %v", h.ChainGaps)
	}
	if s.SealedEnd() != 2*testSpan {
		t.Fatalf("sealedEnd=%d", s.SealedEnd())
	}
}

// TestCorruptionFrameQuarantine: a flipped byte inside a frame is caught
// at read time, the file quarantines, and the range degrades to pruned.
func TestCorruptionFrameQuarantine(t *testing.T) {
	dir := t.TempDir()
	path, _ := buildTestEra(t, dir, ClassExec, 0)
	buildTestEra(t, dir, ClassChain, 0)
	writeManifestFor(t, dir)

	// Flip one byte in the middle of the frame region.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(headMagic)+10] ^= 0xFF
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	s, h, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Light check does not hash payload, so the file still opens...
	if h.Sealed != 2 {
		t.Fatalf("sealed=%d", h.Sealed)
	}
	// ...but the first read of the damaged frame quarantines it.
	_, rerr := s.Exec(0)
	if !IsPruned(rerr) {
		t.Fatalf("expected pruned-degradation, got %v", rerr)
	}
	if len(s.Quarantined()) != 1 {
		t.Fatalf("quarantine: %v", s.Quarantined())
	}
	// Chain class unaffected.
	if _, _, _, err := s.Chain(0); err != nil {
		t.Fatalf("chain read should survive: %v", err)
	}
	// Scrub catches it too.
	r, err := OpenReader(path)
	if err == nil {
		if verr := r.VerifyPayload(); verr == nil {
			t.Fatal("VerifyPayload missed the corruption")
		}
		r.Close()
	}
}

// TestMissingOptionalFile: deleting an exec era degrades to pruned with
// a health warning; chain reads keep working.
func TestMissingOptionalFile(t *testing.T) {
	dir := t.TempDir()
	execPath, _ := buildTestEra(t, dir, ClassExec, 0)
	buildTestEra(t, dir, ClassChain, 0)
	writeManifestFor(t, dir)
	if err := os.Remove(execPath); err != nil {
		t.Fatal(err)
	}
	s, h, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(h.Degraded) != 1 || len(h.ChainGaps) != 0 {
		t.Fatalf("health: %+v", h)
	}
	if st := s.State(ClassExec, 0); st != RangePruned {
		t.Fatalf("exec state = %v", st)
	}
	if _, err := s.Exec(0); !IsPruned(err) {
		t.Fatalf("expected ErrPruned, got %v", err)
	}
	if _, _, _, err := s.Chain(0); err != nil {
		t.Fatalf("chain read: %v", err)
	}
}

// TestMissingChainFileIsCritical: a missing chain era is a ChainGap.
func TestMissingChainFileIsCritical(t *testing.T) {
	dir := t.TempDir()
	chainPath, _ := buildTestEra(t, dir, ClassChain, 0)
	writeManifestFor(t, dir)
	if err := os.Remove(chainPath); err != nil {
		t.Fatal(err)
	}
	s, h, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(h.ChainGaps) != 1 {
		t.Fatalf("expected chain gap, health: %+v", h)
	}
}

// TestManifestRebuildFromFooters: with the manifest deleted, OpenStore
// reconstructs it from era footers.
func TestManifestRebuildFromFooters(t *testing.T) {
	dir := t.TempDir()
	for _, cl := range Classes {
		buildTestEra(t, dir, cl, 0)
	}
	writeManifestFor(t, dir)
	if err := os.Remove(filepath.Join(dir, ManifestName)); err != nil {
		t.Fatal(err)
	}
	s, h, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if h.Sealed != 3 {
		t.Fatalf("rebuild health: %+v", h)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
		t.Fatal("manifest not re-saved after rebuild")
	}
}

// TestPrunedManifestEntry: an entry marked pruned is silent (no warning).
func TestPrunedManifestEntry(t *testing.T) {
	dir := t.TempDir()
	execPath, _ := buildTestEra(t, dir, ClassExec, 0)
	buildTestEra(t, dir, ClassChain, 0)
	m := writeManifestFor(t, dir)
	e := m.Lookup(ClassExec, 0)
	e.Status = StatusPruned
	if _, err := m.Save(dir); err != nil {
		t.Fatal(err)
	}
	os.Remove(execPath)
	s, h, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(h.Degraded) != 0 || h.Pruned != 1 {
		t.Fatalf("health: %+v", h)
	}
	if _, err := s.Exec(0); !IsPruned(err) {
		t.Fatalf("expected ErrPruned, got %v", err)
	}
}

// TestTruncatedFileRejected: a file cut mid-payload fails the light
// check (footer unreadable) and degrades.
func TestTruncatedFileRejected(t *testing.T) {
	dir := t.TempDir()
	path, _ := buildTestEra(t, dir, ClassExec, 0)
	buildTestEra(t, dir, ClassChain, 0)
	writeManifestFor(t, dir)
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, b[:len(b)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	s, h, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(h.Degraded) != 1 {
		t.Fatalf("health: %+v", h)
	}
	if _, err := s.Exec(1); !IsPruned(err) {
		t.Fatalf("expected ErrPruned, got %v", err)
	}
}
