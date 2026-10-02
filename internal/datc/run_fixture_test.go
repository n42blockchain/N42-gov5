// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// run_fixture_test.go — the upstream freezer-input fixture the run* CLI
// workers (verify/proof/bench/diag/merge/weekly) need and the e2e harness in
// datc_e2e_test.go does not produce.
//
// datc_e2e_test.go's generate()/newTestBuilder()/writeFwd() build a DATC
// archive from per-block changesets staged in the MDBX tFwdAcctCS/tFwdStorCS
// tables — that is enough for the builder and the querier (nodeHashAt,
// proofPath, ...), and buildTestArchive in archive_test.go turns that into a
// finished leaf-seg archive with derive-ns/derive-acc-parts applied, which is
// what the library reader (Archive) needs.
//
// The run* workers read from two further upstream inputs that neither
// harness produces:
//
//  1. a headerc freezer (internal/ethel.HeaderCompactReader): runVerify and
//     runProof accept --internal-roots to read the expected root from the
//     build's own DatcRoots table instead (no headerc needed), but runBench
//     and runDiag have no such escape hatch — runBench always reads
//     --headers for its root oracle, and runDiag always reads --changesets.
//  2. the acctcs/storcs changeset freezer tables (internal/ethel's
//     EncodeAccountChanges/EncodeStorageChanges wire format, read back via
//     internal/ethel.DecodeAccountChanges/DecodeStorageChanges): runBench
//     samples (address, height) touches from them, runDiag dumps one
//     block's entries from them, and the decode pipeline in pipeline.go
//     decodes blocks from a builder's b.acctTbl/b.storTbl, which only the
//     real build path (not the e2e fwdMode harness) populates.
//
// Both are synthesized here directly from the same scenario the e2e harness
// uses, via each package's own exported write path:
//   - headerc: a geth-format RLP header (15 legacy fields, snappy-compressed,
//     matching ethel.DecodeGethHeader's expected wire shape) is appended
//     directly to a freezer.TableHeaders table, then
//     ethel.NewHeaderCompactStage(...).Run(...) converts it exactly as the
//     real pipeline would.
//   - acctcs/storcs: ethel.EncodeAccountChanges / EncodeStorageChanges over a
//     modules/changeset.ChangeSet built from the scenario's per-block deltas,
//     appended directly to freezer.TableAccountChanges/TableStorageChanges
//     (the same tables internal/datc's own openCS() reads).
package datc

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/golang/snappy"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// persistentTempDir allocates a directory under GOTMPDIR that outlives any
// single test's T (unlike t.TempDir(), which is removed when that specific
// test finishes). The shared run-worker fixture below is built once per test
// binary invocation and read by many tests in turn, so it needs a directory
// whose lifetime is the process, not one test.
func persistentTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("GOTMPDIR"), prefix)
	if err != nil {
		t.Fatal(err)
	}
	persistentDirsMu.Lock()
	persistentDirs = append(persistentDirs, dir)
	persistentDirsMu.Unlock()
	return dir
}

// persistentDirs collects every directory handed out by persistentTempDir so
// TestMain can remove them when the test binary exits; each archive fixture
// preallocates a ~2 GB MDBX map, and leaving them behind filled GOTMPDIR.
var (
	persistentDirsMu sync.Mutex
	persistentDirs   []string
)

func TestMain(m *testing.M) {
	code := m.Run()
	persistentDirsMu.Lock()
	for _, d := range persistentDirs {
		os.RemoveAll(d)
	}
	persistentDirsMu.Unlock()
	os.Exit(code)
}

// gethHeaderRLP mirrors the 15 legacy fields ethel.decodeHeaderFields expects
// in order (see internal/ethel/rlp_decode.go) — enough to round-trip a
// stateRoot through ethel.OpenHeaderCompact without a real geth ancient store.
type gethHeaderRLP struct {
	ParentHash  types.Hash
	UncleHash   types.Hash
	Coinbase    types.Address
	Root        types.Hash
	TxHash      types.Hash
	ReceiptHash types.Hash
	Bloom       [256]byte
	Difficulty  *big.Int
	Number      *big.Int
	GasLimit    uint64
	GasUsed     uint64
	Time        uint64
	Extra       []byte
	MixDigest   types.Hash
	Nonce       [8]byte
}

// writeHeaderCompact builds a headerc store (ethel's compact header format)
// covering sc.roots[0:len(sc.blocks)], with Root set to the scenario's
// reference root at each height — the exact oracle runVerify/runProof/
// runBench compare against.
func writeHeaderCompact(t *testing.T, sc *scenario) string {
	t.Helper()
	fzDir := filepath.Join(persistentTempDir(t, "datc-gethfz-"), "gethfz")
	fz, err := freezer.New(fzDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	ht, err := fz.EnsureTable(freezer.TableHeaders, "c")
	if err != nil {
		t.Fatal(err)
	}
	for n, root := range sc.roots {
		h := gethHeaderRLP{Root: root, Difficulty: big.NewInt(1), Number: big.NewInt(int64(n))}
		raw, err := rlp.EncodeToBytes(&h)
		if err != nil {
			t.Fatalf("encode header %d: %v", n, err)
		}
		if err := ht.Append(uint64(n), snappy.Encode(nil, raw)); err != nil {
			t.Fatalf("append header %d: %v", n, err)
		}
	}
	if err := fz.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen so Frozen() picks up the headers table's item count (New()
	// recomputes it from the canonical tables on disk; a live Freezer only
	// updates it on EnsureTable/Freeze, neither of which Append alone does).
	fz2, err := freezer.New(fzDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(persistentTempDir(t, "datc-headerc-"), "headerc")
	if err := ethel.NewHeaderCompactStage(fz2, outDir).Run(context.Background()); err != nil {
		t.Fatalf("header compact: %v", err)
	}
	if err := fz2.Close(); err != nil {
		t.Fatal(err)
	}
	return outDir
}

// writeChangesetFreezer builds acctcs/storcs freezer tables from sc's
// per-block deltas, in ethel's unified-changes wire format. Old values are
// not reconstructed faithfully (the run* workers under test only read the
// NEW side through DecodeForStorage/leafFloor comparisons already covered by
// the e2e harness; old values here only have to decode without error).
func writeChangesetFreezer(t *testing.T, sc *scenario) string {
	t.Helper()
	dir := persistentTempDir(t, "datc-csfz-")
	fz, err := freezer.New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	acctT, err := fz.EnsureTable(freezer.TableAccountChanges, "c")
	if err != nil {
		t.Fatal(err)
	}
	storT, err := fz.EnsureTable(freezer.TableStorageChanges, "c")
	if err != nil {
		t.Fatal(err)
	}
	for n, gb := range sc.blocks {
		acs := changeset.NewAccountChangeSet()
		for addr := range gb.accs {
			old := []byte{}
			if n > 0 {
				if a := sc.accountAt(addr, uint64(n-1)); a != nil {
					old = a.MarshalV2()
				}
			}
			if err := acs.Add(types.CopyBytes(addr[:]), old); err != nil {
				t.Fatal(err)
			}
		}
		newAcc := map[types.Address][]byte{}
		for addr, a := range gb.accs {
			if a != nil {
				newAcc[addr] = a.MarshalV2()
			}
		}
		accBlob := ethel.EncodeAccountChanges(acs, func(addr types.Address) []byte { return newAcc[addr] })

		scs := changeset.NewStorageChangeSet()
		newSto := map[types.Address]map[types.Hash][]byte{}
		for addr, m := range gb.slots {
			inner := newSto[addr]
			if inner == nil {
				inner = map[types.Hash][]byte{}
				newSto[addr] = inner
			}
			for slot, v := range m {
				var key [52]byte
				copy(key[:20], addr[:])
				copy(key[20:], slot[:])
				if err := scs.Add(key[:], []byte{}); err != nil {
					t.Fatal(err)
				}
				inner[slot] = v
			}
		}
		stoBlob := ethel.EncodeStorageChanges(scs, func(addr types.Address, slot types.Hash) []byte {
			return newSto[addr][slot]
		})

		if err := acctT.Append(uint64(n), accBlob); err != nil {
			t.Fatalf("append acctcs %d: %v", n, err)
		}
		if err := storT.Append(uint64(n), stoBlob); err != nil {
			t.Fatalf("append storcs %d: %v", n, err)
		}
	}
	if err := fz.Close(); err != nil {
		t.Fatal(err)
	}

	// openCS (main.go) forces batch+compressed read mode, matching the
	// on-disk shape freezer.CompactAll produces for the real mainnet
	// acctcs/storcs — not the one-item-per-Append layout above. Compact
	// into a fresh directory so runBench/runDiag read the fixture exactly
	// as they would a real archive's changesets.
	compacted := persistentTempDir(t, "datc-csfz-compact-")
	if err := freezer.CompactTable(dir, compacted, freezer.TableAccountChanges, "c"); err != nil {
		t.Fatalf("compact acctcs: %v", err)
	}
	if err := freezer.CompactTable(dir, compacted, freezer.TableStorageChanges, "c"); err != nil {
		t.Fatalf("compact storcs: %v", err)
	}
	return compacted
}

// buildFixtureArchive is buildTestArchive (archive_test.go) with a
// process-lifetime output directory instead of t.TempDir() — the shared
// run-worker fixture is read by many tests across the package, well after
// the test that first built it has finished and its own TempDir was removed.
func buildFixtureArchive(t *testing.T) (*scenario, string) {
	t.Helper()
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := persistentTempDir(t, "datc-archive-")
	db, err := openDatcDB(log.New(), out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, db, sc)
	o := e2eOpts{sched: schedE0is1, leafSeg: true, batch: 50, stoCache: 64}
	if err := newTestBuilder(t, db, out, sc, o, 0).run(0, uint64(len(sc.blocks)), o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}
	db.Close()
	stageBin = 8
	defer func() { stageBin = 1024 }()
	var contracts []deriveContract
	for a, d := range map[types.Address]int{sc.big[0]: 2, sc.big[1]: 3, sc.small[2]: 1} {
		h := keccak(a[:])
		contracts = append(contracts, deriveContract{h[:], d})
	}
	if err := deriveNS(out, out, contracts, 4, 16, deriveOpts{}); err != nil {
		t.Fatalf("derive-ns: %v", err)
	}
	if err := deriveAccParts(out, out, []uint64{40, 120, 250}, 4); err != nil {
		t.Fatalf("derive-acc-parts: %v", err)
	}
	return sc, out
}

// runFixture bundles every input the run* CLI workers need for the shared
// e2e scenario: a finished leaf-seg DATC archive (buildTestArchive, which
// also runs derive-ns/derive-acc-parts), a headerc freezer, and an
// acctcs/storcs changeset freezer.
type runFixture struct {
	sc         *scenario
	archiveDir string
	headersDir string
	csDir      string
}

var cachedRunFixture *runFixture

// newRunFixture returns the shared fixture, building it once per test binary
// run (the scenario is 360 blocks / ~3000 accounts — building the archive and
// the two freezers again for every worker test would dominate the suite's
// wall time for no additional coverage).
func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	if cachedRunFixture != nil {
		return cachedRunFixture
	}
	sc, archiveDir := buildFixtureArchive(t)
	cachedRunFixture = &runFixture{
		sc:         sc,
		archiveDir: archiveDir,
		headersDir: writeHeaderCompact(t, sc),
		csDir:      writeChangesetFreezer(t, sc),
	}
	return cachedRunFixture
}

// openFixtureTx opens a fresh read-only MDBX tx + querier over the fixture
// archive, for tests that want direct querier-method access (foldAtTraced,
// subtreeLeaves, ...) rather than going through a run* CLI entry point.
func (f *runFixture) openQuerier(t *testing.T) (*querier, kv.Tx, func()) {
	t.Helper()
	db, err := openArchiveDB(f.archiveDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	q, _, err := loadQuerierCache(tx, f.archiveDir, 0, leafFrameCache)
	if err != nil {
		tx.Rollback()
		db.Close()
		t.Fatal(err)
	}
	return q, tx, func() {
		q.Close()
		tx.Rollback()
		db.Close()
	}
}
