// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// verify_proof_run_test.go exercises runVerify/runProof/loadQuerier through
// the CLI entry points themselves (parsed flags), using --internal-roots so
// no headerc freezer is needed (DatcRoots is the build's own oracle). die()
// calls os.Exit, so the FAIL path is only exercised in a subprocess.
package datc

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 1<<20)
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				break
			}
		}
		done <- string(buf)
	}()
	fn()
	os.Stdout = orig
	w.Close()
	out := <-done
	r.Close()
	return out
}

func TestRunVerifyInternalRootsHappyPath(t *testing.T) {
	f := newRunFixture(t)
	out := captureStdout(t, func() {
		runVerify([]string{"--out", f.archiveDir, "--internal-roots", "--samples", "5", "--seed", "3", "--map.gb", "4"})
	})
	if !strings.Contains(out, "historical roots reconstructed byte-exact") {
		t.Fatalf("unexpected verify output: %s", out)
	}
}

func TestRunVerifyAtExactHeight(t *testing.T) {
	f := newRunFixture(t)
	out := captureStdout(t, func() {
		runVerify([]string{"--out", f.archiveDir, "--internal-roots", "--at", "100", "--map.gb", "4"})
	})
	if !strings.Contains(out, "N=100") {
		t.Fatalf("expected --at 100 to be sampled, got: %s", out)
	}
}

func TestRunProofInternalRootsHappyPath(t *testing.T) {
	f := newRunFixture(t)
	addr := f.sc.big[0]
	var slot0, slot1 types.Hash
	for s := range f.sc.storageAt(addr, 200) {
		slot0 = s
		break
	}
	out := captureStdout(t, func() {
		runProof([]string{
			"--out", f.archiveDir, "--internal-roots",
			"--addr", addr.Hex(),
			"--slots", slot0.Hex() + "," + slot1.Hex(),
			"--at", "200", "--map.gb", "4",
		})
	})
	if !strings.Contains(out, "proof VERIFIED against DatcRoots root") {
		t.Fatalf("unexpected proof output: %s", out)
	}
	if !strings.Contains(out, `"blockNumber": 200`) {
		t.Fatalf("expected blockNumber 200 in JSON output, got: %s", out)
	}
}

func TestRunProofAbsentAccount(t *testing.T) {
	f := newRunFixture(t)
	out := captureStdout(t, func() {
		runProof([]string{"--out", f.archiveDir, "--internal-roots", "--addr", f.sc.absent.Hex(), "--at", "10", "--map.gb", "4"})
	})
	if !strings.Contains(out, "proof VERIFIED") {
		t.Fatalf("unexpected proof output for an absent account: %s", out)
	}
}

// TestLoadQuerierWrapper covers loadQuerier (the defaultFrameCache-bound
// wrapper runVerify/runProof/runBench actually call); loadQuerierCache
// itself is already exercised directly by the e2e harness.
func TestLoadQuerierWrapper(t *testing.T) {
	f := newRunFixture(t)
	db, err := openArchiveDB(f.archiveDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q, head, err := loadQuerier(tx, f.archiveDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if head != uint64(len(f.sc.blocks)) {
		t.Fatalf("head=%d, want %d", head, len(f.sc.blocks))
	}
}

// --- subprocess: runVerify must die() on a tampered DatcRoots entry ---

// buildTinyArchive builds a small, non-leaf-seg DATC archive directly (no
// derive-ns/derive-acc-parts) — enough for a DatcRoots tamper check, far
// cheaper than the shared 360-block fixture.
func buildTinyArchive(t *testing.T) (*scenario, string) {
	t.Helper()
	sc := generate(genCfg{blocks: 20, eoas: 30, bigN: 1, bigSlots: 20, smallN: 4, smSlots: 6, seed: 11})
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	db, err := openDatcDB(log.New(), out, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, db, sc)
	o := e2eOpts{sched: schedE0is1, batch: 10, stoCache: 32}
	if err := newTestBuilder(t, db, out, sc, o, 0).run(0, uint64(len(sc.blocks)), o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}
	db.Close()
	return sc, out
}

func TestRunVerifyDieOnTamperedRoot(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "verifytamper" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRunVerifyDieOnTamperedRoot$")
		cmd.Env = append(os.Environ(), "DATC_HELPER=verifytamper")
		out, err := cmd.CombinedOutput()
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("expected ExitError, got %v (output: %s)", err, out)
		}
		if exitErr.ExitCode() != 1 {
			t.Fatalf("expected exit code 1, got %d (output: %s)", exitErr.ExitCode(), out)
		}
		if !strings.Contains(string(out), "root mismatch") {
			t.Fatalf("expected a root-mismatch die(), got: %s", out)
		}
		return
	}
	_, out := buildTinyArchive(t)
	db, err := openDatcDB(log.New(), out, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var rk [8]byte
	binary.BigEndian.PutUint64(rk[:], 5)
	var bogus [32]byte
	bogus[0] = 0xFF
	if err := tx.Put(tDatcRoots, rk[:], bogus[:]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	runVerify([]string{"--out", out, "--internal-roots", "--at", "5", "--map.gb", "2"})
}
