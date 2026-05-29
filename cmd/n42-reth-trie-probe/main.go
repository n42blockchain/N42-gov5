// n42-reth-trie-probe inspects the key-length distribution of reth's
// AccountsTrie and StoragesTrie tables to determine whether reth stores the
// "empty-path root" record that N42's FlatDBTrieLoader cursors rely on:
//
//   - AccountsTrie  : a keylen-0 record == the global account-trie root
//                     (carries the top-level tree_mask). If absent, AccTrieCursor
//                     starts from the first non-empty path and may miss top-level
//                     siblings — the account-trie analogue of the storage #150.
//   - StoragesTrie  : a keylen-32 record (addrHash only, empty nibble path) ==
//                     a per-contract storage-trie root. reth is KNOWN to omit
//                     these (P6 synthesizes them). Used here as a control: if the
//                     probe correctly reports "0 keylen-32" we trust its
//                     AccountsTrie verdict.
//
// Read-only on the reth DB.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	rethAccountsTrie = "AccountsTrie"
	rethStoragesTrie = "StoragesTrie"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d[rethAccountsTrie] = kv.TableCfgItem{}
	d[rethStoragesTrie] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	rethDir := flag.String("reth", `D:/reth2k/db`, "reth db (read-only)")
	sample := flag.Int("sample", 2000000, "max records to scan per table")
	flag.Parse()

	logger := log.New()
	rdb, err := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer rdb.Close()
	tx, _ := rdb.BeginRo(context.Background())
	defer tx.Rollback()

	// --- AccountsTrie: key = nibble path (StoredNibbles). keylen-0 == root. ---
	fmt.Println("=== AccountsTrie (plain) key-length distribution ===")
	{
		c, _ := tx.Cursor(rethAccountsTrie)
		lenHist := map[int]int{}
		var n int
		var firstKey []byte
		for k, _, e := c.First(); k != nil && n < *sample; k, _, e = c.Next() {
			if e != nil {
				fmt.Fprintln(os.Stderr, "acc iter:", e)
				break
			}
			if firstKey == nil {
				firstKey = append([]byte(nil), k...)
			}
			lenHist[len(k)]++
			n++
		}
		c.Close()
		fmt.Printf("  scanned=%d firstKeyLen=%d firstKey=%x\n", n, len(firstKey), firstKey)
		for l := 0; l <= 8; l++ {
			if cnt, ok := lenHist[l]; ok {
				fmt.Printf("  keylen %d: %d\n", l, cnt)
			}
		}
		if lenHist[0] > 0 {
			fmt.Println("  => reth STORES keylen-0 account-trie root (AccTrieCursor safe)")
		} else {
			fmt.Println("  => reth OMITS keylen-0 account-trie root (AccTrieCursor may need P6-style synth)")
		}
	}

	// --- StoragesTrie: DupSort. dup-value subkey path; control for the probe. ---
	fmt.Println("\n=== StoragesTrie (DupSort) dup-value subkey-path-length (control) ===")
	{
		c, _ := tx.CursorDupSort(rethStoragesTrie)
		var n, emptyPath int
		pathHist := map[int]int{}
		for k, v, e := c.First(); k != nil && n < *sample; k, v, e = c.Next() {
			if e != nil {
				fmt.Fprintln(os.Stderr, "sto iter:", e)
				break
			}
			if len(k) != 32 || len(v) < 65 {
				continue
			}
			pl := int(v[64]) // StoredNibblesSubKey length byte
			pathHist[pl]++
			if pl == 0 {
				emptyPath++
			}
			n++
		}
		c.Close()
		fmt.Printf("  scanned=%d emptyPathRecords(keylen-32 equiv)=%d\n", n, emptyPath)
		for l := 0; l <= 4; l++ {
			if cnt, ok := pathHist[l]; ok {
				fmt.Printf("  subkey-path-len %d: %d\n", l, cnt)
			}
		}
		if emptyPath == 0 {
			fmt.Println("  => reth OMITS per-contract storage roots (matches known #150 / P6 synth) — probe trustworthy")
		} else {
			fmt.Println("  => reth has some empty-path storage records (unexpected)")
		}
	}
}
