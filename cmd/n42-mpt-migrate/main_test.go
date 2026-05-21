// Integration test for n42-mpt-migrate: build two source MDBX envs
// via mptbuild, run the migration, verify the unified dest has both
// trie buckets correctly populated + Meta state_roots preserved.
package main

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/mptbuild"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func mkAddrN(seed uint32) []byte {
	a := make([]byte, 20)
	a[0] = byte(seed >> 24)
	a[1] = byte(seed >> 16)
	a[2] = byte(seed >> 8)
	a[3] = byte(seed)
	return a
}

// buildSourceMPT builds one source MDBX dir with the given trie
// table populated by mptbuild from synthetic entries.
func buildSourceMPT(t *testing.T, dir, table string, extractor mptbuild.Extractor, entries [][2][]byte) [32]byte {
	t.Helper()
	tmp := t.TempDir()
	tgt := &mptbuild.MDBXTarget{
		DBPath:    dir,
		Table:     table,
		MapSizeGB: 1,
	}
	res, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: entries},
		Target:    tgt,
		Extractor: extractor,
		TmpDir:    filepath.Join(tmp, "etl"),
		BufMB:     1,
	})
	tgt.Close()
	if err != nil {
		t.Fatalf("build %s: %v", table, err)
	}
	return res.StateRoot
}

func makeAccountEntries(n int) [][2][]byte {
	out := make([][2][]byte, n)
	for i := 0; i < n; i++ {
		out[i] = [2][]byte{
			mkAddrN(uint32(i)),
			make([]byte, 64), // 64 bytes — ensures leaf is hashed, not inlined
		}
		for k := range out[i][1] {
			out[i][1][k] = byte((i + k) * 7 ^ 0x55)
		}
	}
	return out
}

func makeStorageEntries(n int) [][2][]byte {
	out := make([][2][]byte, n)
	for i := 0; i < n; i++ {
		addr := mkAddrN(uint32(i))
		// reth PlainStorageState DupSort layout: key=addr, value=slot32||u256
		v := make([]byte, 32+8)
		for k := 0; k < 32; k++ {
			v[k] = byte(i * 13)
		}
		v[35] = byte(i)
		out[i] = [2][]byte{addr, v}
	}
	return out
}

func TestMigrate_EndToEnd(t *testing.T) {
	tmp := t.TempDir()
	srcAcct := filepath.Join(tmp, "src-acct")
	srcStor := filepath.Join(tmp, "src-stor")
	dst := filepath.Join(tmp, "dst")

	// Build two small source envs.
	acctEntries := makeAccountEntries(100)
	storEntries := makeStorageEntries(80)
	acctRoot := buildSourceMPT(t, srcAcct, "AccountsTrie", mptbuild.NewAccountExtractor(), acctEntries)
	storRoot := buildSourceMPT(t, srcStor, "StoragesTrie", mptbuild.NewStorageExtractor(), storEntries)

	t.Logf("source roots: acct=%x  stor=%x", acctRoot[:8], storRoot[:8])

	// Run the migrate binary.
	exe := buildMigrateBinary(t)
	out, err := exec.Command(exe,
		"--src-accounts", srcAcct,
		"--src-storage", srcStor,
		"--dst", dst,
		"--src-mapsize-gb", "1",
		"--dst-mapsize-gb", "2",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("migrate exec failed: %v\n%s", err, out)
	}
	t.Logf("migrate output:\n%s", out)

	// Open unified dest and verify both buckets + Meta.
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dst).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(2 * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["AccountsTrie"] = kv.TableCfgItem{}
			d["StoragesTrie"] = kv.TableCfgItem{}
			d["Meta"] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Meta state roots preserved.
	gotAcctRoot, _ := tx.GetOne("Meta", []byte("accounts:state_root"))
	gotStorRoot, _ := tx.GetOne("Meta", []byte("storage:state_root"))
	if hex.EncodeToString(gotAcctRoot) != hex.EncodeToString(acctRoot[:]) {
		t.Errorf("accounts:state_root: got %x want %x", gotAcctRoot, acctRoot[:])
	}
	if hex.EncodeToString(gotStorRoot) != hex.EncodeToString(storRoot[:]) {
		t.Errorf("storage:state_root: got %x want %x", gotStorRoot, storRoot[:])
	}

	// Meta migration timestamp present.
	if v, _ := tx.GetOne("Meta", []byte("accounts:migrated_at")); len(v) == 0 {
		t.Error("accounts:migrated_at missing")
	}
	if v, _ := tx.GetOne("Meta", []byte("storage:migrated_at")); len(v) == 0 {
		t.Error("storage:migrated_at missing")
	}

	// Bucket entry counts match what build produced.
	mtx := tx.(*mdbxkv.MdbxTx)
	acctStat, _ := mtx.BucketStat("AccountsTrie")
	storStat, _ := mtx.BucketStat("StoragesTrie")
	t.Logf("dst AccountsTrie: %d entries", acctStat.Entries)
	t.Logf("dst StoragesTrie: %d entries", storStat.Entries)
	if acctStat.Entries == 0 {
		t.Error("AccountsTrie empty in dst")
	}
	if storStat.Entries == 0 {
		t.Error("StoragesTrie empty in dst")
	}

	// Verify source vs dest entry counts match by re-opening source.
	srcAcctDB, _ := mdbxkv.NewMDBX(logger).
		Path(srcAcct).Label(kv.ChainDB).PageSize(4096).MapSize(1 * datasize.GB).Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg { d["AccountsTrie"] = kv.TableCfgItem{}; return d }).
		Open(context.Background())
	defer srcAcctDB.Close()
	srcTx, _ := srcAcctDB.BeginRo(context.Background())
	defer srcTx.Rollback()
	srcStat, _ := srcTx.(*mdbxkv.MdbxTx).BucketStat("AccountsTrie")
	if srcStat.Entries != acctStat.Entries {
		t.Errorf("AccountsTrie entry count drift: src=%d dst=%d",
			srcStat.Entries, acctStat.Entries)
	}
}

func TestMigrate_DryRunDoesNotWrite(t *testing.T) {
	tmp := t.TempDir()
	srcAcct := filepath.Join(tmp, "src-acct")
	srcStor := filepath.Join(tmp, "src-stor")
	dst := filepath.Join(tmp, "dst")

	_ = buildSourceMPT(t, srcAcct, "AccountsTrie", mptbuild.NewAccountExtractor(), makeAccountEntries(50))
	_ = buildSourceMPT(t, srcStor, "StoragesTrie", mptbuild.NewStorageExtractor(), makeStorageEntries(50))

	exe := buildMigrateBinary(t)
	if _, err := exec.Command(exe,
		"--src-accounts", srcAcct,
		"--src-storage", srcStor,
		"--dst", dst,
		"--src-mapsize-gb", "1",
		"--dry-run",
	).CombinedOutput(); err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	// dst should be empty (no mdbx.dat created).
	if _, err := os.Stat(filepath.Join(dst, "mdbx.dat")); err == nil {
		t.Error("dry-run wrote mdbx.dat — should not")
	}
}

func TestMigrate_MissingSource(t *testing.T) {
	tmp := t.TempDir()
	exe := buildMigrateBinary(t)
	out, err := exec.Command(exe,
		"--src-accounts", filepath.Join(tmp, "does-not-exist"),
		"--src-storage", filepath.Join(tmp, "neither"),
		"--dst", filepath.Join(tmp, "dst"),
	).CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure for missing sources, got success:\n%s", out)
	}
}

// buildMigrateBinary compiles n42-mpt-migrate to a temp file.
func buildMigrateBinary(t *testing.T) string {
	t.Helper()
	exeName := "n42-mpt-migrate-test"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	exe := filepath.Join(t.TempDir(), exeName)
	cmd := exec.Command("go", "build", "-o", exe, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return exe
}
