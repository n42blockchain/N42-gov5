package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// These bytes are emitted only after the initial fixture load has failed. They
// outlive TempDir cleanup in the controlled test log; no load result is retried
// as a successful fixture, and the caller always receives the original error.
type fixtureQMDBRow struct {
	Key   []byte `json:"key_base64"`
	Value []byte `json:"value_base64"`
}

type fixtureQMDBDiagnostic struct {
	SchemaVersion       int                         `json:"schema_version"`
	Stage               string                      `json:"stage"`
	OriginalError       string                      `json:"original_error"`
	Genesis             json.RawMessage             `json:"genesis"`
	Tables              map[string][]fixtureQMDBRow `json:"tables"`
	CaptureError        string                      `json:"capture_error,omitempty"`
	CopiedLoadError     string                      `json:"copied_load_error,omitempty"`
	CopiedRoot          *types.Hash                 `json:"copied_root,omitempty"`
	AllocRecomputeError string                      `json:"alloc_recompute_error,omitempty"`
	AllocRecomputedRoot *types.Hash                 `json:"alloc_recomputed_root,omitempty"`
}

func (d fixtureQMDBDiagnostic) GetOne(table string, key []byte) ([]byte, error) {
	for _, row := range d.Tables[table] {
		if bytes.Equal(row.Key, key) {
			return bytes.Clone(row.Value), nil
		}
	}
	return nil, nil
}

func captureFixtureQMDBDiagnostic(tx kv.Tx, genesis *conf.Genesis, original error) fixtureQMDBDiagnostic {
	d := fixtureQMDBDiagnostic{SchemaVersion: 1, Stage: "captured", OriginalError: original.Error(), Tables: make(map[string][]fixtureQMDBRow)}
	var err error
	d.Genesis, err = json.Marshal(genesis)
	if err != nil {
		d.CaptureError = fmt.Sprintf("genesis: %v", err)
		return d
	}
	for _, table := range []string{modules.Account, modules.Storage, qmdb.MetaTable, qmdb.TwigTable, qmdb.LeavesTable, qmdb.EntryTable} {
		cursor, err := tx.Cursor(table)
		if err != nil {
			d.CaptureError = fmt.Sprintf("cursor %s: %v", table, err)
			return d
		}
		d.Tables[table] = []fixtureQMDBRow{}
		for key, value, readErr := cursor.First(); ; key, value, readErr = cursor.Next() {
			if readErr != nil {
				cursor.Close()
				d.CaptureError = fmt.Sprintf("read %s: %v", table, readErr)
				return d
			}
			if key == nil {
				break
			}
			d.Tables[table] = append(d.Tables[table], fixtureQMDBRow{Key: bytes.Clone(key), Value: bytes.Clone(value)})
		}
		cursor.Close()
	}
	return d
}

func enrichFixtureQMDBDiagnostic(tx kv.Tx, d fixtureQMDBDiagnostic) fixtureQMDBDiagnostic {
	d.Stage = "recomputed"
	fresh := commitment.NewQMDBRootComputer()
	fresh.SetCold(d)
	if err := fresh.LoadFrom(d); err != nil {
		d.CopiedLoadError = err.Error()
	} else {
		root := fresh.Root()
		d.CopiedRoot = &root
	}
	fresh.SetCold(nil)
	if root, err := internal.QMDBGenesisRoot(tx); err != nil {
		d.AllocRecomputeError = err.Error()
	} else {
		d.AllocRecomputedRoot = &root
	}
	return d
}

func loadExecutionFixtureRoot(tx kv.Tx, root *commitment.QMDBRootComputer, genesis *conf.Genesis, emit func(fixtureQMDBDiagnostic)) error {
	root.SetCold(tx)
	if err := root.LoadFrom(tx); err != nil {
		d := captureFixtureQMDBDiagnostic(tx, genesis, err)
		emit(d) // Persist exact input bytes before any supplemental computation.
		if d.CaptureError == "" {
			emit(enrichFixtureQMDBDiagnostic(tx, d))
		}
		return err
	}
	return nil
}

func fixtureQMDBDiagnosticDB(t *testing.T) (kv.RwDB, *conf.Genesis) {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	raw, err := os.ReadFile("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		t.Fatal(err)
	}
	genesis.Alloc = conf.GenesisAlloc{types.HexToAddress("0x1234"): {Balance: "0x100000"}}
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		_, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).Write(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return db, &genesis
}

type fixtureQMDBReadError struct {
	kv.Tx
	err error
}

func (tx fixtureQMDBReadError) GetOne(table string, key []byte) ([]byte, error) {
	if table == qmdb.MetaTable {
		return nil, tx.err
	}
	return tx.Tx.GetOne(table, key)
}

func TestFixtureQMDBDiagnosticPreservesOriginalErrorAndCopiesTables(t *testing.T) {
	db, genesis := fixtureQMDBDiagnosticDB(t)
	original := errors.New("fixture initial metadata read sentinel")
	var captured fixtureQMDBDiagnostic
	var stages []string
	err := db.View(context.Background(), func(tx kv.Tx) error {
		return loadExecutionFixtureRoot(fixtureQMDBReadError{Tx: tx, err: original}, commitment.NewQMDBRootComputer(), genesis, func(d fixtureQMDBDiagnostic) {
			stages = append(stages, d.Stage)
			if len(stages) == 1 && (d.CopiedRoot != nil || d.CopiedLoadError != "" || len(d.Tables) != 6) {
				t.Fatal("raw capture was not emitted before recomputation")
			}
			captured = d
		})
	})
	if len(stages) != 2 || stages[0] != "captured" || stages[1] != "recomputed" {
		t.Fatalf("diagnostic publication order: %v", stages)
	}
	if !errors.Is(err, original) || captured.OriginalError != err.Error() || captured.CaptureError != "" {
		t.Fatalf("original error not preserved: %v %+v", err, captured)
	}
	if len(captured.Tables) != 6 || captured.CopiedLoadError != "" || captured.CopiedRoot == nil || captured.AllocRecomputedRoot == nil || *captured.CopiedRoot != *captured.AllocRecomputedRoot {
		t.Fatalf("copied diagnostic did not independently load fixture: %+v", captured)
	}
	encoded, err := json.Marshal(captured)
	if err != nil || !json.Valid(encoded) || !bytes.Contains(encoded, []byte("fixture initial metadata read sentinel")) {
		t.Fatalf("diagnostic JSON lost error: %s %v", encoded, err)
	}
	rows := captured.Tables[qmdb.TwigTable]
	if len(rows) != 1 || len(rows[0].Value) == 0 {
		t.Fatal("twig bytes absent")
	}
	value, _ := captured.GetOne(qmdb.TwigTable, rows[0].Key)
	value[0] ^= 1
	unchanged, _ := captured.GetOne(qmdb.TwigTable, rows[0].Key)
	if bytes.Equal(value, unchanged) {
		t.Fatal("getter exposed captured bytes for mutation")
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error { return tx.Delete(qmdb.TwigTable, rows[0].Key) }); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(captured)
	if !bytes.Equal(encoded, after) {
		t.Fatal("copied bytes changed after original transaction/database mutation")
	}
}

func TestFixtureQMDBDiagnosticRetainsMalformedTwigAndOriginalFailure(t *testing.T) {
	db, genesis := fixtureQMDBDiagnosticDB(t)
	var malformed []byte
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		cursor, err := tx.Cursor(qmdb.TwigTable)
		if err != nil {
			return err
		}
		defer cursor.Close()
		key, value, err := cursor.First()
		if err != nil {
			return err
		}
		if len(value) < 64 {
			return fmt.Errorf("missing fixture twig metadata")
		}
		malformed = bytes.Clone(value)
		malformed[0] ^= 1
		return tx.Put(qmdb.TwigTable, bytes.Clone(key), malformed)
	}); err != nil {
		t.Fatal(err)
	}
	var captured fixtureQMDBDiagnostic
	err := db.View(context.Background(), func(tx kv.Tx) error {
		return loadExecutionFixtureRoot(tx, commitment.NewQMDBRootComputer(), genesis, func(d fixtureQMDBDiagnostic) { captured = d })
	})
	if err == nil || captured.OriginalError != err.Error() || captured.CopiedLoadError != err.Error() || captured.CopiedRoot != nil || captured.CaptureError != "" {
		t.Fatalf("malformed fixture did not retain original and copied errors: %v %+v", err, captured)
	}
	if rows := captured.Tables[qmdb.TwigTable]; len(rows) != 1 || !bytes.Equal(rows[0].Value, malformed) {
		t.Fatal("malformed metadata not captured exactly")
	}
	if captured.AllocRecomputedRoot == nil || captured.AllocRecomputeError != "" {
		t.Fatal("independent alloc recomputation missing")
	}
}
