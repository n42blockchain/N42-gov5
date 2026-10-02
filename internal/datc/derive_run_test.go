// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// derive_run_test.go covers the thin CLI wrappers around the already-tested
// deriveNS/deriveAccParts internals (runDeriveNS, runDeriveAccParts,
// runDerivePlan), plus rlpOpenPayload (proof.go's small pure helper).
package datc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func TestRlpOpenPayload(t *testing.T) {
	payload := []byte("hello world, this needs more than 55 bytes to use the long string form")
	enc := rlpStr(payload)
	got := rlpOpenPayload(enc)
	if string(got) != string(payload) {
		t.Fatalf("rlpOpenPayload: got %q, want %q", got, payload)
	}
	short := rlpStr([]byte("x"))
	if got := rlpOpenPayload(short); string(got) != "x" {
		t.Fatalf("rlpOpenPayload(short): got %q", got)
	}
	if got := rlpOpenPayload([]byte{0xC0}); got != nil {
		t.Fatalf("rlpOpenPayload on a list header must fail (not a string): got %v", got)
	}
}

// deriveFixtureCopy builds a dedicated small leaf-seg archive (separate from
// the shared run-worker fixture) because runDeriveNS/runDeriveAccParts write
// into the archive dir, and the shared fixture is read by many other tests.
func deriveFixtureCopy(t *testing.T) (*scenario, string) {
	t.Helper()
	sc := generate(genCfg{blocks: 40, eoas: 60, bigN: 2, bigSlots: 30, smallN: 6, smSlots: 8, seed: 21})
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	db, err := openDatcDB(log.New(), out, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, db, sc)
	o := e2eOpts{sched: schedE0is1, leafSeg: true, batch: 20, stoCache: 32}
	if err := newTestBuilder(t, db, out, sc, o, 0).run(0, uint64(len(sc.blocks)), o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}
	db.Close()
	return sc, out
}

func TestRunDerivePlanAndNSAndAccParts(t *testing.T) {
	sc, out := deriveFixtureCopy(t)

	// runDerivePlan's own list format ("<addrHash> <depth>") can legitimately
	// come out depth=0 for a tiny scenario (every contract folds cheaply),
	// which runDeriveNS rejects — so it is exercised for its own coverage
	// (census scan, threshold/unit math, listing) against a throwaway file,
	// and runDeriveNS gets a hand-built, always-valid contracts file.
	planList := filepath.Join(t.TempDir(), "plan.txt")
	captureStdout(t, func() {
		runDerivePlan([]string{"--out", out, "--list", planList, "--threshold-ms", "0"})
	})
	if _, err := os.Stat(planList); err != nil {
		t.Fatalf("derive-plan did not write its list file: %v", err)
	}

	nsContracts := filepath.Join(t.TempDir(), "ns.txt")
	h := keccak(sc.big[0][:])
	if err := os.WriteFile(nsContracts, []byte(fmt.Sprintf("%x 2\n", h[:])), 0o644); err != nil {
		t.Fatal(err)
	}

	stageBin = 8
	defer func() { stageBin = 1024 }()
	captureStdout(t, func() {
		runDeriveNS([]string{"--out", out, "--contracts", nsContracts, "--workers", "4"})
	})

	captureStdout(t, func() {
		runDeriveAccParts([]string{"--out", out, "--stages", "10,20,30", "--workers", "4"})
	})
}
