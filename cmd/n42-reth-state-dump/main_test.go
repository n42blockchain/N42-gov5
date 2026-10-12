package main

import (
	"bytes"
	"encoding/json"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestQuantity(t *testing.T) {
	for _, tc := range []struct {
		input []byte
		want  string
	}{
		{nil, "0x0"},
		{[]byte{0, 0}, "0x0"},
		{[]byte{0, 1}, "0x1"},
		{[]byte{0x12, 0x34}, "0x1234"},
	} {
		if got := quantity(tc.input); got != tc.want {
			t.Fatalf("quantity(%x) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestWriteAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	want := []byte("complete\n")
	if err := writeAtomic(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits: os.OpenFile(..., 0600) reports 0666
	// back, so assert the mode only where it is actually enforced. The write
	// itself still asks for 0600 on every platform.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestDumpAccountUsesRethInitStateFields(t *testing.T) {
	item := dumpAccount{
		Address: "0x0000000000000000000000000000000000000001",
		Balance: "0x2a",
		Nonce:   "0x3",
		Code:    "0x6000",
		Storage: map[string]string{
			"0x0000000000000000000000000000000000000000000000000000000000000004": "0x0000000000000000000000000000000000000000000000000000000000000005",
		},
	}
	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(item); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"address", "balance", "nonce", "code", "storage"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("missing %q in %s", field, out.String())
		}
	}
}

func TestExportHeadReplayWithoutAppliedMarker(t *testing.T) {
	previous := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, err := exportHead(tx); err == nil {
		t.Fatal("missing head accepted")
	}
	var hash types.Hash
	hash[31] = 7
	rawdb.WriteHeadBlockHash(tx, hash)
	if err := rawdb.WriteHeaderNumber(tx, hash, 42); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exportHead(tx); err == nil {
		t.Fatal("noncanonical head accepted")
	}
	if err := rawdb.WriteCanonicalHash(tx, hash, 42); err != nil {
		t.Fatal(err)
	}
	number, got, err := exportHead(tx)
	if err != nil || number != 42 || got != [32]byte(hash) {
		t.Fatalf("fallback: %d %x %v", number, got, err)
	}
	if err := rawdb.WriteQMDBApplied(tx, 42, [32]byte(hash)); err != nil {
		t.Fatal(err)
	}
	number, got, err = exportHead(tx)
	if err != nil || number != 42 || got != [32]byte(hash) {
		t.Fatalf("marker: %d %x %v", number, got, err)
	}
}

func TestExportRootRejectsMismatch(t *testing.T) {
	var root qmdb.Hash
	root[31] = 1
	if err := verifyExportRoot(root, types.Hash(root)); err != nil {
		t.Fatal(err)
	}
	if err := verifyExportRoot(root, types.Hash{}); err == nil {
		t.Fatal("root mismatch accepted")
	}
}
