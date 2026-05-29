// n42-reth-eip2935-repro reproduces #150 with REAL reth data (not synthetic).
//
// It extracts the EIP-2935 account's storage subtree from a reth DB —
// HashedStorages (leaves), StoragesTrie (verbatim BranchNodeCompact nodes),
// HashedAccounts (the account) — and loads them into a FRESH N42 MDBX in two
// shapes:
//
//	A) verbatim reth trie + leaves   (== migration's verbatim import)
//	B) leaves only                   (full-rebuild baseline)
//
// Then it runs the SAME incremental dirty (change one existing slot) on A
// using the cached reth-format trie, and computes the full-rebuild root on B,
// and compares. If A != B, that's #150 on genuine reth nodes — and the diff
// pins which node class (inline/sparse mask) the cursor mis-handles.
//
// Read-only on the reth DB; the test MDBX is a throwaway temp dir.
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

const (
	rethHashedAccounts = "HashedAccounts"
	rethHashedStorages = "HashedStorages"
	rethStoragesTrie   = "StoragesTrie"
)

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[rethHashedAccounts] = kv.TableCfgItem{}
	d[rethHashedStorages] = kv.TableCfgItem{Flags: kv.DupSort}
	d[rethStoragesTrie] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func n42Cfg(d kv.TableCfg) kv.TableCfg {
	d[modules.HashedAccounts] = kv.TableCfgItem{}
	d[modules.HashedStorage] = kv.TableCfgItem{
		Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 64, DupToLen: 32,
	}
	d[modules.TrieOfAccounts] = kv.TableCfgItem{}
	d[modules.TrieOfStorage] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

// decodeRethAccount: reth Compact account → (nonce, balance, codeHash).
func decodeRethAccount(v []byte) (nonce uint64, balance uint256.Int, codeHash types.Hash, ok bool) {
	if len(v) < 2 {
		return
	}
	flags := uint16(v[0]) | uint16(v[1])<<8
	nonceLen := int(flags & 0x0f)
	balLen := int((flags >> 4) & 0x3f)
	hasHash := (flags>>10)&1 == 1
	want := 2 + nonceLen + balLen
	if hasHash {
		want += 32
	}
	if len(v) != want {
		return
	}
	p := 2
	var nb [8]byte
	if nonceLen > 0 {
		copy(nb[8-nonceLen:], v[p:p+nonceLen])
		nonce = binary.BigEndian.Uint64(nb[:])
	}
	p += nonceLen
	var bb [32]byte
	if balLen > 0 {
		copy(bb[32-balLen:], v[p:p+balLen])
	}
	balance.SetBytes(bb[:])
	p += balLen
	if hasHash {
		copy(codeHash[:], v[p:p+32])
	}
	return nonce, balance, codeHash, true
}

type slotKV struct {
	slotHash [32]byte
	val      []byte
}
type trieKV struct {
	path []byte // nibble path
	node []byte // verbatim reth BranchNodeCompact
}

func main() {
	rethDir := flag.String("reth", `D:/reth2k/db`, "reth db (read-only)")
	testDir := flag.String("test", `D:/N42-repro-test`, "throwaway test MDBX dir")
	addrHex := flag.String("addr", "0000F90827F1C53a10cb7A02335B175320002935", "EIP-2935 raw address (40 hex)")
	startBlock := flag.Uint64("start-block", 25188782, "first self-sync block (EIP-2935 writes slot=(block-1)%8191 each block)")
	rounds := flag.Int("rounds", 2755, "incremental rounds (self-sync blocks to simulate)")
	flag.Parse()

	addrBytes, _ := hex.DecodeString(*addrHex)
	if len(addrBytes) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr")
		os.Exit(1)
	}
	var addr types.Address
	copy(addr[:], addrBytes)
	addrHash := crypto.Keccak256(addr[:])
	fmt.Printf("addr=%x addrHash=%x\n", addr[:], addrHash)

	logger := log.New()
	rdb, err := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(rethCfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	rtx, _ := rdb.BeginRo(context.Background())

	// --- extract account ---
	acctVal, _ := rtx.GetOne(rethHashedAccounts, addrHash)
	if acctVal == nil {
		fmt.Fprintln(os.Stderr, "EIP-2935 account not found in reth HashedAccounts")
		os.Exit(1)
	}
	nonce, bal, codeHash, ok := decodeRethAccount(acctVal)
	if !ok {
		fmt.Fprintln(os.Stderr, "decode account failed")
		os.Exit(1)
	}
	if codeHash == (types.Hash{}) {
		codeHash = crypto.Keccak256Hash(nil)
	}
	acct := &account.StateAccount{Initialised: true, Nonce: nonce, Balance: bal, CodeHash: codeHash}
	fmt.Printf("account: nonce=%d bal=%s codeHash=%x\n", nonce, bal.String(), codeHash[:8])

	// --- extract leaves (HashedStorages dup under addrHash) ---
	var leaves []slotKV
	sc, _ := rtx.CursorDupSort(rethHashedStorages)
	for v, e := sc.SeekBothRange(addrHash, nil); v != nil && e == nil; _, v, e = sc.NextDup() {
		if len(v) < 32 {
			continue
		}
		var sh [32]byte
		copy(sh[:], v[:32])
		leaves = append(leaves, slotKV{sh, append([]byte(nil), v[32:]...)})
	}
	sc.Close()
	fmt.Printf("leaves: %d slots\n", len(leaves))

	// --- extract verbatim trie nodes (StoragesTrie dup under addrHash) ---
	var nodes []trieKV
	tc, _ := rtx.CursorDupSort(rethStoragesTrie)
	for v, e := tc.SeekBothRange(addrHash, nil); v != nil && e == nil; _, v, e = tc.NextDup() {
		if len(v) < 65 {
			continue
		}
		sub := v[:65]
		node := v[65:]
		pl := int(sub[64])
		if pl > 64 {
			continue
		}
		nodes = append(nodes, trieKV{append([]byte(nil), sub[:pl]...), append([]byte(nil), node...)})
	}
	tc.Close()
	rtx.Rollback()
	rdb.Close()
	fmt.Printf("verbatim trie nodes: %d\n", len(nodes))

	// A: verbatim reth trie + leaves (incremental). B: leaves only (full rebuild).
	dbA := openTest(*testDir+"-A", logger)
	defer dbA.Close()
	defer os.RemoveAll(*testDir + "-A")
	loadState(dbA, addrHash, acct, leaves, nodes) // with verbatim reth trie
	dbB := openTest(*testDir+"-B", logger)
	defer dbB.Close()
	defer os.RemoveAll(*testDir + "-B")
	loadState(dbB, addrHash, acct, leaves, nil) // leaves only

	trcA := commitment.NewTrieRootComputer()
	trcA.SetIncremental(true)
	trcB := commitment.NewTrieRootComputer()
	trcB.SetIncremental(false)

	fmt.Printf("running %d rounds from block %d (real EIP-2935 ring-buffer slots)...\n", *rounds, *startBlock)
	for r := 0; r < *rounds; r++ {
		blk := *startBlock + uint64(r)
		ringSlot := (blk - 1) % 8191
		var slot types.Hash
		binary.BigEndian.PutUint64(slot[24:], ringSlot)
		val := new(uint256.Int).SetUint64(blk*1000 + 7) // deterministic per-block value
		dS := map[types.Address]map[types.Hash]*uint256.Int{addr: {slot: val}}

		rootA := computeOne(dbA, trcA, dS)
		rootB := computeOne(dbB, trcB, dS)
		if rootA != rootB {
			fmt.Printf("\n>>> round %d (block %d, ringSlot %d, slotHash %x): MISMATCH\n",
				r, blk, ringSlot, crypto.Keccak256(slot[:])[:8])
			fmt.Printf("    A(verbatim reth incremental) = %x\n", rootA)
			fmt.Printf("    B(leaves full rebuild)       = %x\n", rootB)
			fmt.Printf(">>> #150 REPRODUCED on real reth EIP-2935 data at round %d\n", r)
			return
		}
		if r%500 == 0 {
			fmt.Printf("  round %d ok root=%x\n", r, rootA[:8])
		}
	}
	fmt.Printf(">>> %d rounds all MATCH — verbatim reth + multi-round incremental == full rebuild\n", *rounds)
}

func openTest(dir string, logger log.Logger) kv.RwDB {
	os.RemoveAll(dir)
	db, err := mdbx.NewMDBX(logger).Path(dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(8 * datasize.GB).GrowthStep(256 * datasize.MB).
		WithTableCfg(n42Cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open test:", err)
		os.Exit(1)
	}
	return db
}

func loadState(db kv.RwDB, addrHash []byte, acct *account.StateAccount, leaves []slotKV, nodes []trieKV) {
	tx, _ := db.BeginRw(context.Background())
	_ = tx.Put(modules.HashedAccounts, addrHash, acct.MarshalV2())
	for _, lf := range leaves {
		ck := append(append([]byte(nil), addrHash...), lf.slotHash[:]...)
		_ = tx.Put(modules.HashedStorage, ck, lf.val)
	}
	for _, nd := range nodes {
		k := append(append([]byte(nil), addrHash...), nd.path...)
		_ = tx.Put(modules.TrieOfStorage, k, nd.node)
	}
	_ = tx.Commit()
}

func computeOne(db kv.RwDB, trc *commitment.TrieRootComputer, dS map[types.Address]map[types.Hash]*uint256.Int) types.Hash {
	tx, _ := db.BeginRw(context.Background())
	defer tx.Rollback()
	trc.SetRwTx(tx)
	root, err := trc.ComputeRoot(nil, dS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ComputeRoot:", err)
		os.Exit(1)
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "commit:", err)
		os.Exit(1)
	}
	return root
}
