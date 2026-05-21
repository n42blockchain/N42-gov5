package mptproof

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/mptbuild"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const productionChaindataDir = `D:\n42-chaindata`

// buildUnifiedTestDB synthesises a tiny unified env containing both
// AccountsTrie + StoragesTrie + Meta (with prefixed keys), matching
// the layout produced by cmd/n42-mpt-migrate.
func buildUnifiedTestDB(t *testing.T, nAccts, nStor int) (string, map[[20]byte][]byte) {
	t.Helper()
	tmp := t.TempDir()

	// Build accounts source (separate env), capture root.
	srcAcct := filepath.Join(tmp, "src-acct")
	acctEntries, acctValues := makeAccountEntriesAndValues(nAccts)
	acctRoot := buildOneTrie(t, srcAcct, "AccountsTrie", mptbuild.NewAccountExtractor(), acctEntries)

	// Same for storage.
	srcStor := filepath.Join(tmp, "src-stor")
	storEntries := makeStorageEntries(nStor)
	storRoot := buildOneTrie(t, srcStor, "StoragesTrie", mptbuild.NewStorageExtractor(), storEntries)

	// Construct unified env manually (without invoking cmd/n42-mpt-migrate
	// to keep this test self-contained).
	dst := filepath.Join(tmp, "unified")
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dst).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(2 * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["AccountsTrie"] = kv.TableCfgItem{}
			d["StoragesTrie"] = kv.TableCfgItem{}
			d["Meta"] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open unified: %v", err)
	}

	// Stream both source bucket → dest with prefix on Meta keys.
	for _, m := range []struct {
		src, table, prefix string
		root               [32]byte
	}{
		{srcAcct, "AccountsTrie", "accounts:", acctRoot},
		{srcStor, "StoragesTrie", "storage:", storRoot},
	} {
		srcDB, err := mdbxkv.NewMDBX(logger).
			Path(m.src).Label(kv.ChainDB).PageSize(4096).MapSize(1 * datasize.GB).Readonly().
			WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
				d[m.table] = kv.TableCfgItem{}
				d["Meta"] = kv.TableCfgItem{}
				return d
			}).
			Open(context.Background())
		if err != nil {
			t.Fatalf("open src %s: %v", m.src, err)
		}

		srcTx, _ := srcDB.BeginRo(context.Background())
		dstTx, _ := db.BeginRw(context.Background())
		dstCur, _ := dstTx.RwCursor(m.table)
		c, _ := srcTx.Cursor(m.table)
		for k, v, err := c.First(); err == nil && k != nil; k, v, err = c.Next() {
			dstCur.Append(k, v)
		}
		c.Close()
		dstCur.Close()
		dstTx.Put("Meta", []byte(m.prefix+"state_root"), m.root[:])
		dstTx.Put("Meta", []byte(m.prefix+"built_at"), []byte(time.Now().UTC().Format(time.RFC3339)))
		dstTx.Commit()
		srcTx.Rollback()
		srcDB.Close()
	}
	db.Close()

	return dst, acctValues
}

func makeAccountEntriesAndValues(n int) ([][2][]byte, map[[20]byte][]byte) {
	values := make(map[[20]byte][]byte, n)
	entries := make([][2][]byte, n)
	for i := 0; i < n; i++ {
		var a [20]byte
		a[0] = byte(uint32(i) >> 24)
		a[1] = byte(uint32(i) >> 16)
		a[2] = byte(uint32(i) >> 8)
		a[3] = byte(uint32(i))
		v := make([]byte, 64)
		for k := range v {
			v[k] = byte((i + k) * 7 ^ 0x55)
		}
		values[a] = v
		entries[i] = [2][]byte{a[:], v}
	}
	return entries, values
}

func makeStorageEntries(n int) [][2][]byte {
	out := make([][2][]byte, n)
	for i := 0; i < n; i++ {
		var addr [20]byte
		addr[0] = byte(uint32(i) >> 24)
		addr[1] = byte(uint32(i) >> 16)
		addr[2] = byte(uint32(i) >> 8)
		addr[3] = byte(uint32(i))
		v := make([]byte, 32+8)
		for k := 0; k < 32; k++ {
			v[k] = byte(i * 13)
		}
		v[35] = byte(i)
		out[i] = [2][]byte{addr[:], v}
	}
	return out
}

func buildOneTrie(t *testing.T, dir, table string, ex mptbuild.Extractor, entries [][2][]byte) [32]byte {
	t.Helper()
	tgt := &mptbuild.MDBXTarget{DBPath: dir, Table: table, MapSizeGB: 1}
	res, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: entries},
		Target:    tgt,
		Extractor: ex,
		TmpDir:    filepath.Join(t.TempDir(), "etl"),
		BufMB:     1,
	})
	tgt.Close()
	if err != nil {
		t.Fatalf("build %s: %v", table, err)
	}
	return res.StateRoot
}

// ===========================================================================
// Synthetic unified-env tests
// ===========================================================================

func TestGenerator_UnifiedEnv_BothTriesReachable(t *testing.T) {
	dst, values := buildUnifiedTestDB(t, 50, 30)

	leaves := &MapLeafSource{Accounts: values}
	g, err := New(Config{
		ChaindataDir: dst,
		Leaves:       leaves,
	})
	if err != nil {
		t.Fatalf("New unified: %v", err)
	}
	defer g.Close()

	// Pick a known address and verify proof carries the right leaf.
	var addr [20]byte
	addr[3] = 5
	proof, err := g.LatestAccountProof(addr)
	if err != nil {
		t.Fatalf("LatestAccountProof: %v", err)
	}
	if !proof.LeafFound {
		t.Error("expected LeafFound for known addr in unified env")
	}
	t.Logf("acct proof from unified env: root=%x leaf=%d bytes hops=%d",
		proof.StateRoot[:6], len(proof.LeafValue), len(proof.Walk.Hops))

	// Storage proof reaches the storage trie.
	var slot [32]byte
	storProofs, err := g.LatestStorageProofs(addr, [][32]byte{slot})
	if err != nil {
		t.Fatalf("LatestStorageProofs: %v", err)
	}
	t.Logf("stor proof from unified env: root=%x walk-outcome=%v",
		storProofs[0].StateRoot[:6], storProofs[0].Walk.Outcome)
}

func TestGenerator_UnifiedEnv_StateRootsMatchPerBucketMeta(t *testing.T) {
	dst, _ := buildUnifiedTestDB(t, 100, 50)

	leaves := &MapLeafSource{}
	g, err := New(Config{ChaindataDir: dst, Leaves: leaves})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	acctRoot, err := g.accountsMPT.StateRoot()
	if err != nil {
		t.Fatalf("acct StateRoot: %v", err)
	}
	storRoot, err := g.storageMPT.StateRoot()
	if err != nil {
		t.Fatalf("stor StateRoot: %v", err)
	}
	t.Logf("unified env: accounts root=%x  storage root=%x",
		acctRoot[:8], storRoot[:8])
	if acctRoot == storRoot {
		t.Fatal("accounts root == storage root — meta prefix isn't working")
	}
	var zero [32]byte
	if acctRoot == zero || storRoot == zero {
		t.Fatal("a root is zero — meta key prefix wiring broken")
	}
}

func TestGenerator_LegacyTwoEnvStillWorks(t *testing.T) {
	tmp := t.TempDir()
	srcAcct := filepath.Join(tmp, "acct")
	srcStor := filepath.Join(tmp, "stor")
	acctEntries, values := makeAccountEntriesAndValues(50)
	_ = buildOneTrie(t, srcAcct, "AccountsTrie", mptbuild.NewAccountExtractor(), acctEntries)
	storEntries := makeStorageEntries(20)
	_ = buildOneTrie(t, srcStor, "StoragesTrie", mptbuild.NewStorageExtractor(), storEntries)

	leaves := &MapLeafSource{Accounts: values}
	g, err := New(Config{
		AccountsTrieDir: srcAcct,
		StorageTrieDir:  srcStor,
		Leaves:          leaves,
	})
	if err != nil {
		t.Fatalf("legacy two-env New: %v", err)
	}
	defer g.Close()

	var addr [20]byte
	addr[3] = 7
	proof, err := g.LatestAccountProof(addr)
	if err != nil {
		t.Fatalf("legacy two-env LatestAccountProof: %v", err)
	}
	if !proof.LeafFound {
		t.Error("legacy two-env: leaf not found")
	}
}

// ===========================================================================
// Production unified-env test (against real D:\n42-chaindata)
// ===========================================================================

func TestProduction_UnifiedChaindata_USDC(t *testing.T) {
	if _, err := os.Stat(filepath.Join(productionChaindataDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present; run cmd/n42-mpt-migrate first", productionChaindataDir)
	}
	if _, err := os.Stat(filepath.Join(productionRethDB, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB)
	}
	leafSrc, err := NewRethLeafSource(productionRethDB, "PlainAccountState", "PlainStorageState", 4096)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{
		ChaindataDir: productionChaindataDir,
		HistoryDir:   productionHistoryDir,
		Leaves:       leafSrc,
	})
	if err != nil {
		t.Fatalf("open production unified: %v", err)
	}
	defer g.Close()

	// Verify both roots match what was migrated.
	acctRoot, _ := g.accountsMPT.StateRoot()
	storRoot, _ := g.storageMPT.StateRoot()
	wantAcct := "7812409463082683ff0a0b5cef75d87f8dfe313f006ea70cf8246f4521700033"
	wantStor := "c44e11b0c6fc1032dcb7a96b3150dd93d6d3d9c4681b0ff962ece29ff306dfc8"
	if hex.EncodeToString(acctRoot[:]) != wantAcct {
		t.Errorf("accounts root: got %x want %s", acctRoot, wantAcct)
	}
	if hex.EncodeToString(storRoot[:]) != wantStor {
		t.Errorf("storage root: got %x want %s", storRoot, wantStor)
	}

	// USDC proof from unified env.
	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)
	var slot0 [32]byte

	bundle, err := g.LatestProof(usdc, [][32]byte{slot0})
	if err != nil {
		t.Fatalf("LatestProof USDC from unified: %v", err)
	}
	t.Logf("unified env USDC:")
	t.Logf("  acct root=%x leaf=%d bytes hops=%d siblings=%d",
		bundle.Account.StateRoot[:6], len(bundle.Account.LeafValue),
		len(bundle.Account.Walk.Hops), len(bundle.Account.Walk.CollectSiblings()))
	t.Logf("  stor root=%x slot0=%d bytes hops=%d siblings=%d",
		bundle.Storages[0].StateRoot[:6], len(bundle.Storages[0].LeafValue),
		len(bundle.Storages[0].Walk.Hops), len(bundle.Storages[0].Walk.CollectSiblings()))
	if !bundle.Account.LeafFound {
		t.Error("USDC account leaf not found via unified env")
	}
}
