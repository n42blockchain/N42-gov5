// n42-trie-stat measures TrieAccount + TrieStorage capacity (record count +
// logical key+value bytes) of a chaindata, and classifies storage nodes by
// shape (leaf-only hasTree=0&&hasHash=0, with-+1-own-hash), so the
// reth-shape-vs-N42-native difference can be quantified.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/bits"
	"os"

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

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "chaindata dir")
	flag.Parse()
	db, err := mdbx.NewMDBX(log.New()).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	for _, table := range []string{"TrieAccount", "TrieStorage"} {
		c, _ := tx.Cursor(table)
		var n, kb, vb uint64
		var leafOnly, leafOnlyBytes, withRoot uint64
		for k, v, e := c.First(); k != nil; k, v, e = c.Next() {
			if e != nil {
				break
			}
			n++
			kb += uint64(len(k))
			vb += uint64(len(v))
			if len(v) >= 6 {
				hasTree := binary.BigEndian.Uint16(v[2:4])
				hasHash := binary.BigEndian.Uint16(v[4:6])
				nh := (len(v) - 6) / 32
				if hasTree == 0 && hasHash == 0 {
					leafOnly++
					leafOnlyBytes += uint64(len(k) + len(v))
				}
				if bits.OnesCount16(hasHash)+1 == nh {
					withRoot++
				}
			}
		}
		c.Close()
		fmt.Printf("=== %s ===\n", table)
		fmt.Printf("  records=%d  keyBytes=%.2fGB  valBytes=%.2fGB  total=%.2fGB\n",
			n, float64(kb)/1e9, float64(vb)/1e9, float64(kb+vb)/1e9)
		fmt.Printf("  leaf-only(tree=0&hash=0)=%d (%.2fGB)  with-+1-rootHash=%d\n",
			leafOnly, float64(leafOnlyBytes)/1e9, withRoot)
	}
}
