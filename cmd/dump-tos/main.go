// dump-tos: read-only dump of TrieStorage (TrieOfStorage) records + HashedStorage
// leaf distribution for a given account addrHash, to inspect cached storage-trie
// node shapes (block-160 incremental-root bug, task #130).
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
)

const (
	tblTrieStorage   = "TrieStorage"   // kv.TrieOfStorage
	tblHashedStorage = "HashedStorage" // kv.HashedStorage
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d[tblTrieStorage] = kv.TableCfgItem{Flags: kv.DupSort}
	d[tblHashedStorage] = kv.TableCfgItem{
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                64,
		DupToLen:                  32,
	}
	return d
}

func nib(b []byte) string {
	const hd = "0123456789abcdef"
	out := make([]byte, len(b))
	for i, n := range b {
		out[i] = hd[n&0xf]
	}
	return string(out)
}

func main() {
	datadir := flag.String("datadir", `D:/N42-hashed/chaindata`, "chaindata dir (read-only)")
	acct := flag.String("acct", "", "account addrHash (64 hex)")
	flag.Parse()

	ah, err := hex.DecodeString(*acct)
	if err != nil || len(ah) != 32 {
		fmt.Fprintln(os.Stderr, "need --acct = 64 hex chars")
		os.Exit(1)
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*datadir).Label(kv.ChainDB).
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

	// 1. Dump TrieStorage records for this account.
	fmt.Printf("=== TrieStorage records for %s ===\n", *acct)
	c, err := tx.Cursor(tblTrieStorage)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor tos:", err)
		os.Exit(1)
	}
	type rec struct {
		path string
		v    []byte
	}
	var recs []rec
	for k, v, e := c.Seek(ah); k != nil && len(k) >= 32 && string(k[:32]) == string(ah); k, v, e = c.Next() {
		if e != nil {
			fmt.Fprintln(os.Stderr, "iter tos:", e)
			break
		}
		recs = append(recs, rec{nib(k[32:]), append([]byte(nil), v...)})
	}
	c.Close()
	sort.Slice(recs, func(i, j int) bool {
		if len(recs[i].path) != len(recs[j].path) {
			return len(recs[i].path) < len(recs[j].path)
		}
		return recs[i].path < recs[j].path
	})
	for _, r := range recs {
		hs, ht, hh, hashes, root := trie.UnmarshalTrieNode(r.v)
		rh := "-"
		if len(root) > 0 {
			rh = hex.EncodeToString(root)
		}
		line := fmt.Sprintf("path=%-4s hs=%04x ht=%04x hh=%04x nH=%2d rh=%s", "'"+r.path+"'", hs, ht, hh, len(hashes)/32, rh)
		for i := 0; i+32 <= len(hashes); i += 32 {
			line += " " + hex.EncodeToString(hashes[i:i+4])
		}
		fmt.Println(line)
	}
	fmt.Printf("(total %d TrieStorage records)\n", len(recs))

	// 2. HashedStorage leaf distribution by first two nibbles.
	fmt.Printf("\n=== HashedStorage leaves for %s (first-2-nibble histogram) ===\n", *acct)
	sc, err := tx.Cursor(tblHashedStorage)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor hs:", err)
		os.Exit(1)
	}
	hist := map[string]int{}
	total := 0
	var leaves24 []string
	for k, _, e := sc.Seek(ah); k != nil && len(k) >= 32 && string(k[:32]) == string(ah); k, _, e = sc.Next() {
		if e != nil {
			fmt.Fprintln(os.Stderr, "iter hs:", e)
			break
		}
		if len(k) < 64 {
			continue
		}
		slot := k[32:64]
		p2 := nib(slot[:1]) // first nibble
		hist[p2]++
		total++
		// collect leaves under path 2,4 (slot nibble0=2, nibble1=4 → slot[0]==0x24)
		if slot[0] == 0x24 {
			leaves24 = append(leaves24, hex.EncodeToString(slot[:6]))
		}
	}
	sc.Close()
	keys := make([]string, 0, len(hist))
	for kk := range hist {
		keys = append(keys, kk)
	}
	sort.Strings(keys)
	for _, kk := range keys {
		fmt.Printf("  nibble %s : %d leaves\n", kk, hist[kk])
	}
	fmt.Printf("(total %d leaves)\n", total)
	fmt.Printf("leaves under path 2,4 (slot[0]==0x24): %d → %v\n", len(leaves24), leaves24)
}
