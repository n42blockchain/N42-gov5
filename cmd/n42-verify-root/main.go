// n42-verify-root computes the state root of a migrated N42 chaindata
// using the cached intermediate-hash trie (TrieOfAccounts/TrieOfStorage)
// WITHOUT recomputing leaves. It runs FlatDBTrieLoader.CalcTrieRoot with an
// empty RetainList, so every subtree resolves to its cached IH hash —
// the loader only reads the top-level cached nodes and combines them.
// Result is compared against the expected header stateRoot.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d["HashedAccount"] = kv.TableCfgItem{}
	d["HashedStorage"] = kv.TableCfgItem{
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                64,
		DupToLen:                  32,
	}
	d["TrieAccount"] = kv.TableCfgItem{}
	d["TrieStorage"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "N42 chaindata dir")
	expect := flag.String("expect", "", "expected stateRoot hex (optional)")
	retainHex := flag.String("retain-hex", "", "nibble prefix to RETAIN (forces re-combining that subtree's cached IH).")
	retainContracts := flag.Int("retain-contracts", 0, "retain N contract accounts down to leaf (forces SeekToAccount; uses cached storage IH only).")
	retainStorage := flag.Int("retain-storage", 0, "retain N real storage slots (addrHash++slotHash, 128 nibbles): forces the loader to DESCEND those accounts' storage tries, reading HashedStorage leaves with 32B keys and recomputing storage roots — replicates block-execution's dirty-storage path. Root must still match.")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	// Empty RetainList → canUse returns true everywhere → every subtree uses
	// its cached IH hash; only the top-level cached nodes are read + combined.
	rl := trie.NewRetainList(0)
	if *retainHex != "" {
		nib := make([]byte, len(*retainHex))
		for i := 0; i < len(*retainHex); i++ {
			c := (*retainHex)[i]
			switch {
			case c >= '0' && c <= '9':
				nib[i] = c - '0'
			case c >= 'a' && c <= 'f':
				nib[i] = c - 'a' + 10
			case c >= 'A' && c <= 'F':
				nib[i] = c - 'A' + 10
			default:
				fmt.Fprintln(os.Stderr, "bad retain-hex nibble:", string(c))
				os.Exit(1)
			}
		}
		rl.AddHex(nib)
		fmt.Printf("retaining nibble prefix %q\n", *retainHex)
	}
	if *retainContracts > 0 {
		c, err := tx.Cursor("HashedAccount")
		if err != nil {
			fmt.Fprintln(os.Stderr, "cursor HashedAccount:", err)
			os.Exit(1)
		}
		found := 0
		emptyCH := crypto.Keccak256Hash(nil)
		for k, v, e := c.First(); k != nil && found < *retainContracts; k, v, e = c.Next() {
			if e != nil {
				break
			}
			var a account.StateAccount
			if a.DecodeForStorage(v) != nil {
				continue
			}
			if a.CodeHash == emptyCH {
				continue // EOA, no storage trie
			}
			// retain this contract's full addrHash (32B → 64 nibbles)
			nib := make([]byte, 64)
			for i := 0; i < 32; i++ {
				nib[i*2] = k[i] >> 4
				nib[i*2+1] = k[i] & 0x0f
			}
			rl.AddHex(nib)
			found++
		}
		c.Close()
		fmt.Printf("retaining %d contract accounts down to leaf → forces SeekToAccount storage walk (32B keys)\n", found)
	}
	if *retainStorage > 0 {
		// AutoDupSortKeysConversion presents the logical 64B key
		// (addrHash++slotHash) on a plain Cursor.
		c, err := tx.Cursor("HashedStorage")
		if err != nil {
			fmt.Fprintln(os.Stderr, "cursor HashedStorage:", err)
			os.Exit(1)
		}
		found := 0
		for k, _, e := c.First(); k != nil && found < *retainStorage; k, _, e = c.Next() {
			if e != nil {
				break
			}
			if len(k) != 64 {
				continue
			}
			addrHash := k[:32]
			slotHash := k[32:64]
			nib := make([]byte, 128)
			for i := 0; i < 32; i++ {
				nib[i*2] = addrHash[i] >> 4
				nib[i*2+1] = addrHash[i] & 0x0f
			}
			for i := 0; i < 32; i++ {
				nib[64+i*2] = slotHash[i] >> 4
				nib[64+i*2+1] = slotHash[i] & 0x0f
			}
			rl.AddHex(nib)
			found++
		}
		c.Close()
		fmt.Printf("retaining %d storage slots (addrHash++slotHash) → forces storage-trie DESCENT + HashedStorage 32B reads\n", found)
	}

	t0 := time.Now()
	loader := trie.NewFlatDBTrieLoader("verify-root", rl, nil, nil, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CalcTrieRoot:", err)
		os.Exit(1)
	}
	fmt.Printf("stateRoot = %s  (elapsed %s)\n", root.Hex(), time.Since(t0).Truncate(time.Millisecond))
	if *expect != "" {
		if root.Hex() == *expect {
			fmt.Println("MATCH ✓ — migrated trie root equals expected header stateRoot")
		} else {
			fmt.Printf("MISMATCH ✗ — expected %s\n", *expect)
			os.Exit(2)
		}
	}
}
