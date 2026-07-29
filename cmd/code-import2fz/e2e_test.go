// e2e_test.go — round-trip through the real binary: build a small MDBX Code
// table, export it, then read the codes back by hash through the production
// reader. Covers the part unit tests can't: that the writer's codes.hidx /
// codes.hoff layout is the one internal/ethel actually parses.

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

func TestExportHashIndexRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the exporter binary and opens an MDBX")
	}
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "db")
	outdir := filepath.Join(tmp, "out")

	// Distinct bytecodes; the key is keccak(code), as in both source schemas.
	codes := [][]byte{
		{0x60, 0x80, 0x60, 0x40, 0x52},
		{0xef, 0x01, 0x00, 0xaa, 0xbb, 0xcc, 0xdd, 0xee}, // 7702 delegation designator
		{0x00},
		make([]byte, 4096), // large enough to actually exercise zstd
	}
	for i := range codes[3] {
		codes[3][i] = byte(i)
	}
	want := make(map[types.Hash][]byte, len(codes))

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(dbPath).Label(kv.ChainDB).
		WithTableCfg(func(defaults kv.TableCfg) kv.TableCfg {
			defaults["Code"] = kv.TableCfgItem{}
			return defaults
		}).Open(context.Background())
	if err != nil {
		t.Fatalf("open mdbx: %v", err)
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, c := range codes {
			h := crypto.Keccak256Hash(c)
			want[h] = c
			if err := tx.Put("Code", h[:], c); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("populate: %v", err)
	}
	db.Close()

	bin := filepath.Join(tmp, "code-import2fz.exe")
	build := exec.Command("go", "build", "-tags", "nosqlite,noboltdb", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build exporter: %v\n%s", err, out)
	}
	run := exec.Command(bin, "--db", dbPath, "--outdir", outdir, "--addr-index=false")
	runOut, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("run exporter: %v\n%s", err, runOut)
	}
	t.Logf("exporter output:\n%s", runOut)

	for _, name := range []string{ethel.CodesHashIndexFile, ethel.CodesHashOffsetsFile} {
		if _, err := os.Stat(filepath.Join(outdir, name)); err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
	}

	r, err := ethel.NewCodesFreezerReader(outdir)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer r.Close()
	if !r.HasHashIndex() {
		t.Fatal("reader did not pick up the hash index")
	}

	for h, code := range want {
		got, err := r.GetCodeByHash(h)
		if err != nil {
			t.Fatalf("GetCodeByHash(%x): %v", h[:6], err)
		}
		if string(got) != string(code) {
			t.Fatalf("GetCodeByHash(%x): got %d bytes, want %d", h[:6], len(got), len(code))
		}
	}

	// A hash outside the build set may land on an occupied slot — the MPHF keeps
	// no keys. Whatever comes back must fail the caller's keccak check, which is
	// the invariant the whole keyless design rests on.
	absent := crypto.Keccak256Hash([]byte("not in the set"))
	got, err := r.GetCodeByHash(absent)
	if err != nil {
		t.Fatalf("GetCodeByHash(absent): %v", err)
	}
	if len(got) > 0 && crypto.Keccak256Hash(got) == absent {
		t.Fatal("out-of-set hash returned code that verifies — impossible")
	}
}
