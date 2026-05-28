// n42-trie-probe inspects the structural shape of a migrated N42 chaindata's
// TrieOfAccounts/TrieOfStorage tables: key-length histograms and, crucially,
// whether the empty-path "account.root" records (TrieOfStorage key length 32 =
// addrHash only) exist. Erigon's incremental FlatDBTrieLoader relies on those
// root records (invariant: "TrieStorage record of account.root must have +1
// hash"); reth may omit them, which breaks incremental root updates.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d["HashedAccount"] = kv.TableCfgItem{}
	d["HashedStorage"] = kv.TableCfgItem{Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 64, DupToLen: 32}
	d["TrieAccount"] = kv.TableCfgItem{}
	d["TrieStorage"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func bitsOnes16(x int) int {
	c := 0
	for i := 0; i < 16; i++ {
		if x&(1<<i) != 0 {
			c++
		}
	}
	return c
}

func histo(tx kv.Tx, table string, max int) (map[int]uint64, uint64) {
	h := map[int]uint64{}
	var total uint64
	c, err := tx.Cursor(table)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor", table, err)
		os.Exit(1)
	}
	defer c.Close()
	for k, _, e := c.First(); k != nil; k, _, e = c.Next() {
		if e != nil {
			break
		}
		total++
		h[len(k)]++
		if max > 0 && total >= uint64(max) {
			break
		}
	}
	return h, total
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "N42 chaindata dir")
	max := flag.Int("max", 0, "stop after N entries per table (0 = all)")
	flag.Parse()

	db, err := mdbx.NewMDBX(log.New()).Path(*dir).Label(kv.ChainDB).
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

	// Check, per keylen, how many TrieStorage records carry the "+1" own-hash
	// (rootHash) — i.e. OnesCount(hasHash)+1 == len(hashes)/32. erigon attaches
	// it only to the account.root; if reth-migrated keylen-33+ records also have
	// it, the no-root loader path will mistake them for the storage root.
	{
		c, err := tx.Cursor("TrieStorage")
		if err == nil {
			withRoot := map[int]uint64{}
			seen := map[int]uint64{}
			var n uint64
			for k, v, e := c.First(); k != nil; k, v, e = c.Next() {
				if e != nil {
					break
				}
				n++
				kl := len(k)
				seen[kl]++
				if len(v) >= 6 {
					hasHash := int(uint16(v[4])<<8 | uint16(v[5]))
					nh := (len(v) - 6) / 32
					if bitsOnes16(hasHash)+1 == nh {
						withRoot[kl]++
					}
				}
				if *max > 0 && n >= uint64(*max) {
					break
				}
			}
			c.Close()
			fmt.Println("=== TrieStorage +1-rootHash by keylen (withRoot/total) ===")
			ls := make([]int, 0, len(seen))
			for l := range seen {
				ls = append(ls, l)
			}
			sort.Ints(ls)
			for _, l := range ls {
				fmt.Printf("  keylen %3d : %d / %d have +1 rootHash\n", l, withRoot[l], seen[l])
			}
		}
	}

	for _, table := range []string{"TrieAccount", "TrieStorage"} {
		h, total := histo(tx, table, *max)
		lens := make([]int, 0, len(h))
		for l := range h {
			lens = append(lens, l)
		}
		sort.Ints(lens)
		fmt.Printf("=== %s: total=%d (scanned) ===\n", table, total)
		for _, l := range lens {
			fmt.Printf("  keylen %3d : %d\n", l, h[l])
		}
	}
}
