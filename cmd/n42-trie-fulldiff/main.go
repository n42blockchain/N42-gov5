// n42-trie-fulldiff does a full N42-native trie rebuild (retain-everything,
// descend from leaves) over a reth-migrated chaindata, and for every node N42
// emits it compares — SEMANTICALLY (hasState/hasTree/hasHash masks + child
// hashes, ignoring the optional +1 own-hash and the keylen-32 storage root
// record that N42 adds) — against the reth-migrated node at the same path.
//
// Goal: locate the scale-level reth↔erigon trie difference that makes N42's
// incremental loader produce a wrong root while a full leaf-rebuild is correct.
// onlyN42 (path N42 emits but reth lacks) and maskDiff/hashDiff counts pinpoint
// which node classes diverge.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync/atomic"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d["HashedAccount"] = kv.TableCfgItem{}
	d["HashedStorage"] = kv.TableCfgItem{Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 64, DupToLen: 32}
	d["TrieAccount"] = kv.TableCfgItem{}
	d["TrieStorage"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

// semantic compare: N42-emitted (masks+childHashes) vs reth-stored node bytes.
// returns: 0 match, 1 onlyN42 (reth missing), 2 maskDiff, 3 hashDiff
func cmpNode(rethVal []byte, hasState, hasTree, hasHash uint16, hashes []byte) int {
	if rethVal == nil {
		return 1
	}
	rs, rt, rh, rHashes, _ := trie.UnmarshalTrieNode(rethVal)
	if rs != hasState || rt != hasTree || rh != hasHash {
		return 2
	}
	if !bytes.Equal(rHashes, hashes) {
		return 3
	}
	return 0
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "reth-migrated N42 chaindata")
	maxLog := flag.Int("maxlog", 20, "max example diffs to log per table")
	flag.Parse()

	db, err := mdbx.NewMDBX(log.New()).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().WithTableCfg(cfg).Open(context.Background())
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

	// counters[table][result] ; lens[table][result][keylen]
	var accMatch, accOnlyN42, accMask, accHash, accEmpty int64
	var stoMatch, stoOnlyN42, stoMask, stoHash, stoEmpty int64
	accByLen := map[int]int64{}
	stoByLen := map[int]int64{}
	accLogged, stoLogged := 0, 0

	accCollector := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if len(keyHex) == 0 || hasState == 0 {
			atomic.AddInt64(&accEmpty, 1)
			return nil // skip empty-path + delete markers (cursor canUse=false)
		}
		rv, _ := tx.GetOne("TrieAccount", keyHex)
		switch cmpNode(rv, hasState, hasTree, hasHash, hashes) {
		case 0:
			accMatch++
		case 1:
			accOnlyN42++
			accByLen[len(keyHex)]++
			if accLogged < *maxLog {
				fmt.Printf("ACC onlyN42 keylen=%d path=%x state=%016b tree=%016b hash=%016b\n", len(keyHex), keyHex, hasState, hasTree, hasHash)
				accLogged++
			}
		case 2:
			accMask++
			accByLen[len(keyHex)]++
			if accLogged < *maxLog {
				rs, rt, rh, _, _ := trie.UnmarshalTrieNode(rv)
				fmt.Printf("ACC maskDiff keylen=%d path=%x N42(s=%016b t=%016b h=%016b) reth(s=%016b t=%016b h=%016b)\n", len(keyHex), keyHex, hasState, hasTree, hasHash, rs, rt, rh)
				accLogged++
			}
		case 3:
			accHash++
			accByLen[len(keyHex)]++
		}
		return nil
	}
	storCollector := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		full := append(append(make([]byte, 0, len(accWithInc)+len(keyHex)), accWithInc...), keyHex...)
		if len(keyHex) == 0 || hasState == 0 { // keylen-32 account.root / delete marker
			atomic.AddInt64(&stoEmpty, 1)
			return nil
		}
		rv, _ := tx.GetOne("TrieStorage", full)
		switch cmpNode(rv, hasState, hasTree, hasHash, hashes) {
		case 0:
			stoMatch++
		case 1:
			stoOnlyN42++
			stoByLen[len(keyHex)]++
			if stoLogged < *maxLog {
				fmt.Printf("STO onlyN42 keylenPath=%d acct=%x path=%x state=%016b tree=%016b hash=%016b\n", len(keyHex), accWithInc[:4], keyHex, hasState, hasTree, hasHash)
				stoLogged++
			}
		case 2:
			stoMask++
			stoByLen[len(keyHex)]++
			if stoLogged < *maxLog {
				rs, rt, rh, _, _ := trie.UnmarshalTrieNode(rv)
				fmt.Printf("STO maskDiff acct=%x path=%x N42(s=%016b t=%016b h=%016b) reth(s=%016b t=%016b h=%016b)\n", accWithInc[:4], keyHex, hasState, hasTree, hasHash, rs, rt, rh)
				stoLogged++
			}
		case 3:
			stoHash++
			stoByLen[len(keyHex)]++
		}
		return nil
	}

	fmt.Fprintln(os.Stderr, "full N42-native rebuild (retain-everything) + semantic diff vs reth nodes...")
	rl := trie.NewRetainList(1 << 30)
	loader := trie.NewFlatDBTrieLoader("fulldiff", rl, accCollector, storCollector, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CalcTrieRoot:", err)
		os.Exit(1)
	}
	fmt.Printf("\nroot=%x\n", root[:])
	fmt.Printf("ACC  match=%d onlyN42=%d maskDiff=%d hashDiff=%d emptyPathSkipped=%d\n", accMatch, accOnlyN42, accMask, accHash, accEmpty)
	fmt.Printf("STO  match=%d onlyN42=%d maskDiff=%d hashDiff=%d emptyPathSkipped=%d\n", stoMatch, stoOnlyN42, stoMask, stoHash, stoEmpty)
	dump := func(name string, m map[int]int64) {
		ls := make([]int, 0, len(m))
		for l := range m {
			ls = append(ls, l)
		}
		sort.Ints(ls)
		for _, l := range ls {
			fmt.Printf("  %s diff-by-keylen %d: %d\n", name, l, m[l])
		}
	}
	dump("ACC", accByLen)
	dump("STO", stoByLen)
}
