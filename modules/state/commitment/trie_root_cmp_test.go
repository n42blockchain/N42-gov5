package commitment

import (
	"context"
	"os"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// TestCmpRethVsN42Nodes compares reth-migrated TrieStorage records for one
// contract account against the records N42 builds from the SAME HashedStorage
// slots. Reveals reth↔erigon node-materialization differences (different branch
// paths / bytes) that break the ported incremental loader on real reth data.
//
//	N42_REAL_DB=D:/N42-hashed/chaindata go test ./modules/state/commitment -run TestCmpRethVsN42Nodes -v
func TestCmpRethVsN42Nodes(t *testing.T) {
	dir := os.Getenv("N42_REAL_DB")
	if dir == "" {
		t.Skip("set N42_REAL_DB to a reth-migrated chaindata dir")
	}
	cfg := func(d kv.TableCfg) kv.TableCfg {
		d["HashedAccount"] = kv.TableCfgItem{}
		d["HashedStorage"] = kv.TableCfgItem{Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 64, DupToLen: 32}
		d["TrieStorage"] = kv.TableCfgItem{Flags: kv.DupSort}
		return d
	}
	db, err := mdbx.NewMDBX(log2.New()).Path(dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	emptyCH := crypto.Keccak256Hash(nil)
	// Find first account whose storage trie has a deep (keylen>=34) sub-branch,
	// so the comparison is meaningful.
	var addrHash types.Hash
	{
		tc, _ := tx.Cursor("TrieStorage")
		for k, _, e := tc.First(); k != nil; k, _, e = tc.Next() {
			if e != nil {
				break
			}
			if len(k) >= 34 {
				copy(addrHash[:], k[:32])
				break
			}
		}
		tc.Close()
	}
	if addrHash == (types.Hash{}) {
		t.Skip("no deep storage trie found")
	}
	t.Logf("account addrHash=%x", addrHash[:])

	// Read its account value + all storage slots (logical 64B keys via AutoConv).
	var acctVal []byte
	acctVal, _ = tx.GetOne("HashedAccount", addrHash[:])
	slots := map[types.Hash]*uint256.Int{}
	{
		sc, _ := tx.Cursor("HashedStorage")
		for k, v, e := sc.Seek(addrHash[:]); k != nil; k, v, e = sc.Next() {
			if e != nil {
				break
			}
			if len(k) != 64 || string(k[:32]) != string(addrHash[:]) {
				break
			}
			var sh types.Hash
			copy(sh[:], k[32:64])
			val := new(uint256.Int).SetBytes(v)
			slots[sh] = val
		}
		sc.Close()
	}
	// reth-built node paths.
	rethPaths := map[string][]byte{}
	{
		tc, _ := tx.Cursor("TrieStorage")
		for k, v, e := tc.Seek(addrHash[:]); k != nil; k, v, e = tc.Next() {
			if e != nil {
				break
			}
			if len(k) < 32 || string(k[:32]) != string(addrHash[:]) {
				break
			}
			rethPaths[string(k[32:])] = append([]byte(nil), v...)
		}
		tc.Close()
	}
	t.Logf("slots=%d rethStorageNodes=%d", len(slots), len(rethPaths))

	// N42-built: put the SAME account+slots, run incremental=false, read back.
	// NOTE the slots are keyed by slotHash already (post-keccak), so feed a
	// pseudo-account whose keccak(addr)==addrHash is impossible; instead build
	// directly via the hashed tables using a custom path: write HashedStorage
	// with composite key addrHash+slotHash, then CalcTrieRoot.
	mdb := memdb.NewTestDB(t)
	mtx, _ := mdb.BeginRw(context.Background())
	defer mtx.Rollback()
	if len(acctVal) > 0 {
		_ = mtx.Put("HashedAccount", addrHash[:], acctVal)
	} else {
		var a account.StateAccount
		a.Initialised = true
		a.CodeHash = types.BytesHash([]byte("x"))
		if a.CodeHash == emptyCH {
		}
		_ = mtx.Put("HashedAccount", addrHash[:], a.MarshalV2())
	}
	for sh, v := range slots {
		var ck [64]byte
		copy(ck[:32], addrHash[:])
		copy(ck[32:], sh[:])
		b := v.Bytes32()
		st := 0
		for st < 31 && b[st] == 0 {
			st++
		}
		_ = mtx.Put("HashedStorage", ck[:], b[st:])
	}
	// Build TrieOf* via the same FlatDBTrieLoader the computer uses.
	trc := NewTrieRootComputer()
	trc.SetRwTx(mtx)
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(map[types.Address]*account.StateAccount{}, map[types.Address]map[types.Hash]*uint256.Int{}); err != nil {
		t.Fatalf("build: %v", err)
	}
	n42Paths := map[string][]byte{}
	{
		tc, _ := mtx.Cursor("TrieStorage")
		for k, v, e := tc.First(); k != nil; k, v, e = tc.Next() {
			if e != nil {
				break
			}
			if len(k) < 32 || string(k[:32]) != string(addrHash[:]) {
				continue
			}
			n42Paths[string(k[32:])] = append([]byte(nil), v...)
		}
		tc.Close()
	}
	t.Logf("n42StorageNodes=%d", len(n42Paths))

	// Diff path sets.
	var onlyReth, onlyN42, bytesDiffer int
	for p := range rethPaths {
		if _, ok := n42Paths[p]; !ok {
			onlyReth++
			t.Logf("  ONLY-RETH path=%x", []byte(p))
		}
	}
	for p, nv := range n42Paths {
		rv, ok := rethPaths[p]
		if !ok {
			onlyN42++
			t.Logf("  ONLY-N42  path=%x", []byte(p))
			continue
		}
		if string(rv) != string(nv) {
			bytesDiffer++
			t.Logf("  BYTES-DIFF path=%x rethLen=%d n42Len=%d", []byte(p), len(rv), len(nv))
		}
	}
	t.Logf("RESULT onlyReth=%d onlyN42=%d bytesDiffer=%d (reth=%d n42=%d)", onlyReth, onlyN42, bytesDiffer, len(rethPaths), len(n42Paths))
}
