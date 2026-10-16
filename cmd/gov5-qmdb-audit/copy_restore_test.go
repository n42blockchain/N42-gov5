package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

func restoreFixture(t *testing.T) (string, *auditedGenesis, auditReport) {
	t.Helper()
	modules.N42Init()
	open := func(dir string) kv.RwDB {
		db, err := mdbx.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).MapSize(256 * datasize.MB).GrowthStep(4 * datasize.MB).Open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	candidateDir := t.TempDir()
	db := open(candidateDir)
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
	})
	gdb := open(t.TempDir())
	t.Cleanup(gdb.Close)
	beneficiary := types.Address{0: 0xee, 19: 9}
	cfg := params.NormalizeConsensus(&params.ChainConfig{ChainID: big.NewInt(4205001), StateScheme: "qmdb", SpuriousDragonBlock: big.NewInt(0), HotStuff: &params.HotStuffConfig{Validators: []params.HotStuffValidatorConfig{{Address: beneficiary.Hex()}}}})
	var target types.Address
	var targetKey qmdb.Hash
	var root types.Hash
	seed := func(tx kv.RwTx) error {
		for i := range 6000 {
			var addr types.Address
			binary.BigEndian.PutUint64(addr[12:], uint64(i+1))
			a := account.NewAccount()
			a.Balance.SetUint64(1000000)
			if err := tx.Put(modules.Account, addr[:], a.MarshalV2()); err != nil {
				return err
			}
			key := qmdb.Hash(commitment.AccountKeyHash(addr))
			if i == 0 || bytes.Compare(key[:], targetKey[:]) < 0 {
				target, targetKey = addr, key
			}
		}
		var err error
		root, err = internal.SeedQMDBGenesis(tx)
		return err
	}
	for _, d := range []kv.RwDB{db, gdb} {
		if err := d.Update(t.Context(), seed); err != nil {
			t.Fatal(err)
		}
	}
	genesisHeader := &block.Header{Number: uint256.NewInt(0), Difficulty: uint256.NewInt(0), Root: root}
	g := &auditedGenesis{Genesis: conf.Genesis{Config: cfg}, Anchor: chainAnchor{0, genesisHeader.Hash(), root}, SHA256: "fixture-genesis"}
	gtx, err := gdb.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g.Tx = gtx
	t.Cleanup(gtx.Rollback)
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		for id := range uint64(2) {
			if err := tx.Delete(qmdb.LeavesTable, position(id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rc := commitment.NewQMDBRootComputer()
	if err := db.View(t.Context(), func(tx kv.Tx) error { rc.SetCold(tx); defer rc.SetCold(nil); return rc.LoadFrom(tx) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		defer rc.SetCold(nil)
		updated := account.NewAccount()
		updated.Nonce = 1
		updated.Balance.SetUint64(999999)
		empty := account.NewAccount()
		empty.Reset()
		var err error
		root, err = rc.ComputeRoot(map[types.Address]*account.StateAccount{target: &updated, beneficiary: &empty}, nil)
		if err != nil {
			return err
		}
		if err := tx.Put(modules.Account, target[:], updated.MarshalV2()); err != nil {
			return err
		}
		if _, err := rc.FlushTo(tx); err != nil {
			return err
		}
		h := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), ParentHash: genesisHeader.Hash(), Root: root, Coinbase: beneficiary, GasUsed: 21000}
		rawdb.WriteHeader(tx, genesisHeader)
		rawdb.WriteHeader(tx, h)
		if err := rawdb.WriteCanonicalHash(tx, genesisHeader.Hash(), 0); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, h.Hash(), 1); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, h.Hash())
		if err := rawdb.WriteQMDBApplied(tx, 1, [32]byte(h.Hash())); err != nil {
			return err
		}
		return rawdb.WriteChainConfig(tx, genesisHeader.Hash(), cfg)
	}); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	var proof auditReport
	if err := db.View(t.Context(), func(tx kv.Tx) error { var err error; proof, err = auditBoundSnapshot(tx, g); return err }); err != nil {
		t.Fatal(err)
	}
	proof.Passed = true
	db.Close()
	db = nil
	path := filepath.Join(candidateDir, "mdbx.dat")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := observeData(path, 0600)
	if err != nil {
		t.Fatal(err)
	}
	proof.DatabaseSHA256 = before.SHA256
	return candidateDir, g, proof
}

func TestCandidateRestorePersistsExactlyTwoRowsAndPhysicallyReopens(t *testing.T) {
	dir, g, proof := restoreFixture(t)
	before, err := observeData(filepath.Join(dir, "mdbx.dat"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(t.TempDir(), "restore-intent.json")
	out, err := restoreCandidate(t.Context(), dir, before, proof, g, "fixture-proof", intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Passed || !out.Audit.Passed || out.Audit.Applied != proof.Applied || out.Audit.Canonical != proof.Canonical || out.Audit.Forest.Root != proof.Forest.Root || out.Audit.Forest.LiveKeys != 6001 || out.Audit.Forest.NextSlot != 6002 || len(out.Audit.Forest.MissingGenesis) != 0 || len(out.Audit.Forest.EmptyBeneficiaries) != 1 || !sameOtherRows(out.BeforeTables, out.AfterTables, true) {
		t.Fatalf("incomplete physical restore proof: %+v", out)
	}
	if out.After.SHA256 == before.SHA256 || out.After.Mode != 0400 || out.After.Inode != before.Inode {
		t.Fatal("missing physical change or wrong file identity/mode")
	}
	if _, err := os.Stat(intent); err != nil {
		t.Fatal("durable intent missing", err)
	}
	// An existing restored candidate is never silently retried or overwritten.
	if _, err := restoreCandidate(t.Context(), dir, before, proof, g, "fixture-proof", intent, nil); err == nil {
		t.Fatal("reused restored candidate")
	}
	after, err := observeData(before.Path, 0400)
	if err != nil || after != out.After {
		t.Fatal("failed retry changed candidate", err)
	}
}

type failSecondLeafPut struct {
	kv.RwTx
	puts int
}

func (tx *failSecondLeafPut) Put(table string, key, value []byte) error {
	if table == qmdb.LeavesTable {
		tx.puts++
		if tx.puts == 2 {
			return errors.New("injected second leaf write failure")
		}
	}
	return tx.RwTx.Put(table, key, value)
}

func TestCandidateSecondPutFailureRollsBackWholeTransaction(t *testing.T) {
	db := auditTestDB(t)
	var before []tableFingerprint
	if err := db.View(t.Context(), func(tx kv.Tx) error { var err error; before, err = logicalFingerprints(t.Context(), tx); return err }); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	injected := &failSecondLeafPut{RwTx: tx}
	if err := insertAbsentLeaves(injected, [][]byte{{1}, {2}}); err == nil {
		t.Fatal("injected write did not fail")
	}
	tx.Rollback()
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		for id := range uint64(2) {
			present, err := tx.Has(qmdb.LeavesTable, position(id))
			if err != nil {
				return err
			}
			if present {
				t.Fatal("partial leaf write survived rollback")
			}
		}
		after, err := logicalFingerprints(t.Context(), tx)
		if err != nil {
			return err
		}
		if !sameOtherRows(before, after, false) {
			t.Fatal("failed restore changed other rows/sequences")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateFingerprintDetectsNonTargetRowsAndSequenceChanges(t *testing.T) {
	db := auditTestDB(t)
	var before []tableFingerprint
	capture := func() []tableFingerprint {
		t.Helper()
		var out []tableFingerprint
		if err := db.View(t.Context(), func(tx kv.Tx) error { var err error; out, err = logicalFingerprints(t.Context(), tx); return err }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before = capture()
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(modules.DatabaseInfo, []byte("unrelated"), []byte("changed")) }); err != nil {
		t.Fatal(err)
	}
	if sameOtherRows(before, capture(), false) {
		t.Fatal("fingerprint ignored unrelated logical row")
	}
	before = capture()
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { _, err := tx.IncrementSequence(modules.Account, 1); return err }); err != nil {
		t.Fatal(err)
	}
	if sameOtherRows(before, capture(), false) {
		t.Fatal("fingerprint ignored sequence change")
	}
}

func TestCandidateRestoreRejectsExistingLeafBeforeAnyWrite(t *testing.T) {
	dir, g, proof := restoreFixture(t)
	db, err := openCandidateDB(t.Context(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := g.Tx.GetOne(qmdb.LeavesTable, position(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(qmdb.LeavesTable, position(0), blob) }); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := observeData(filepath.Join(dir, "mdbx.dat"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(t.TempDir(), "restore-intent.json")
	if _, err := restoreCandidate(t.Context(), dir, before, proof, g, "fixture-proof", intent, nil); err == nil {
		t.Fatal("accepted existing target row")
	}
	after, err := observeData(before.Path, 0600)
	if err != nil || after != before {
		t.Fatal("rejected restore changed candidate", err)
	}
	if _, err := os.Lstat(intent); !os.IsNotExist(err) {
		t.Fatal("restore wrote intent before rejecting invalid baseline")
	}
}

func TestRestoreCandidateRejectsRuntimeAuditAndSymlinkPaths(t *testing.T) {
	for _, path := range []string{"/data/blockchain/gov5-performance-5m/runtime/s192r1/e/chaindata", "/data/blockchain/gov5-work/validation/x/audit-copy/chaindata", "relative/restore-candidate/chaindata"} {
		if _, err := candidateDataFile(path); err == nil {
			t.Fatalf("accepted writable path %s", path)
		}
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := observeData(alias, 0600); err == nil {
		t.Fatal("accepted symlink data")
	}
	link := filepath.Join(dir, "hardlink")
	if err := os.Link(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := observeData(target, 0600); err == nil {
		t.Fatal("accepted shared inode")
	}
}

func TestCandidateRestoreRejectsChangedProofAndBlobBeforeWrite(t *testing.T) {
	dir, g, proof := restoreFixture(t)
	before, err := observeData(filepath.Join(dir, "mdbx.dat"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*auditReport)
	}{
		{"not-passed", func(p *auditReport) { p.Passed = false }},
		{"wrong-file-sha", func(p *auditReport) { p.DatabaseSHA256 = "different" }},
		{"wrong-genesis", func(p *auditReport) { p.Genesis.Root[0] ^= 1 }},
		{"wrong-blob", func(p *auditReport) { p.Forest.MissingGenesis[0].SHA256 = "different" }},
		{"wrong-id", func(p *auditReport) { p.Forest.MissingGenesis[0].TwigID = 2 }},
	} {
		changed := proof
		changed.Forest.MissingGenesis = append([]leafCandidate(nil), proof.Forest.MissingGenesis...)
		tc.change(&changed)
		intent := filepath.Join(t.TempDir(), "restore-intent.json")
		if _, err := restoreCandidate(t.Context(), dir, before, changed, g, "fixture-proof", intent, nil); err == nil {
			t.Fatalf("accepted %s", tc.name)
		}
		after, err := observeData(before.Path, 0600)
		if err != nil || after != before {
			t.Fatalf("%s changed data: %v", tc.name, err)
		}
		if _, err := os.Lstat(intent); !os.IsNotExist(err) {
			t.Fatalf("%s began write intent", tc.name)
		}
	}
}
