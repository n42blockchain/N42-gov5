// n42-isolate: for each dirty storage account, recompute its storage root two
// ways on a SINGLE-account in-memory trie copied from the real state —
// incremental (cached TrieStorage + retain dirty slots, marked) vs full (cleared
// TrieStorage). Accounts where they differ are reproducible-in-isolation storage
// cursor bugs; if none differ here but the whole-tree run does, the bug is
// cross-account (shared cursor) instead.
package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d["HashedAccount"] = kv.TableCfgItem{}
	d["HashedStorage"] = kv.TableCfgItem{Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 64, DupToLen: 32}
	d["TrieAccount"] = kv.TableCfgItem{}
	d["TrieStorage"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func ck(e error, w string) {
	if e != nil {
		fmt.Fprintln(os.Stderr, "FATAL", w, e)
		os.Exit(1)
	}
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "state (RO) = parent of the failing block")
	dump := flag.String("dump", `C:/tmp/dirty157.gob`, "dirty-set dump")
	flag.Parse()

	f, err := os.Open(*dump)
	ck(err, "open dump")
	var d commitment.DirtyDump
	ck(gob.NewDecoder(f).Decode(&d), "decode")
	f.Close()
	fmt.Printf("loaded %d storage-accts\n", len(d.Storage))

	src, err := mdbx.NewMDBX(log.New()).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().WithTableCfg(cfg).Open(context.Background())
	ck(err, "open src")
	defer src.Close()
	stx, _ := src.BeginRo(context.Background())
	defer stx.Rollback()

	mem := mdbx.NewMDBX(log.New()).InMem(os.TempDir() + "/n42-iso157").MapSize(8 * datasize.GB).WithTableCfg(cfg).MustOpen()
	defer mem.Close()

	bad, checked := 0, 0
	for _, da := range d.Storage {
		ah := da.AddrHash[:]
		av, _ := stx.GetOne("HashedAccount", ah)
		if len(av) == 0 {
			continue
		}
		rl := trie.NewRetainList(0)
		wtx, err := mem.BeginRw(context.Background())
		ck(err, "beginRw")
		for _, t := range []string{"HashedAccount", "HashedStorage", "TrieStorage", "TrieAccount"} {
			ck(wtx.ClearBucket(t), "clear")
		}
		ck(wtx.Put("HashedAccount", append([]byte(nil), ah...), append([]byte(nil), av...)), "put acct")
		sc, _ := stx.Cursor("HashedStorage")
		for k, v, e := sc.Seek(ah); k != nil; k, v, e = sc.Next() {
			if e != nil || len(k) != 64 || !bytes.Equal(k[:32], ah) {
				break
			}
			ck(wtx.Put("HashedStorage", append([]byte(nil), k...), append([]byte(nil), v...)), "put sto")
		}
		sc.Close()
		for _, ds := range da.Slots {
			var k [64]byte
			copy(k[:32], ah)
			copy(k[32:], ds.SlotHash[:])
			if len(ds.Value) == 0 {
				ck(wtx.Delete("HashedStorage", k[:]), "del dirty")
			} else {
				ck(wtx.Put("HashedStorage", k[:], append([]byte(nil), ds.Value...)), "put dirty")
			}
			rl.AddKeyWithMarker(k[:], true)
		}
		tc, _ := stx.Cursor("TrieStorage")
		for k, v, e := tc.Seek(ah); k != nil; k, v, e = tc.Next() {
			if e != nil || len(k) < 32 || !bytes.Equal(k[:32], ah) {
				break
			}
			ck(wtx.Put("TrieStorage", append([]byte(nil), k...), append([]byte(nil), v...)), "put trie")
		}
		tc.Close()
		ck(wtx.Commit(), "commit")

		rtx, _ := mem.BeginRo(context.Background())
		li := trie.NewFlatDBTrieLoader("iso-inc", rl, nil, nil, false)
		rootInc, e1 := li.CalcTrieRoot(rtx, nil)
		rtx.Rollback()

		wtx2, _ := mem.BeginRw(context.Background())
		ck(wtx2.ClearBucket("TrieStorage"), "clear2")
		ck(wtx2.Commit(), "commit2")
		rtx2, _ := mem.BeginRo(context.Background())
		lf := trie.NewFlatDBTrieLoader("iso-full", trie.NewRetainList(0), nil, nil, false)
		rootFull, e2 := lf.CalcTrieRoot(rtx2, nil)
		rtx2.Rollback()

		checked++
		if e1 != nil || e2 != nil {
			fmt.Printf("acct=%x ERR inc=%v full=%v\n", da.AddrHash[:6], e1, e2)
			continue
		}
		if rootInc != rootFull {
			bad++
			fmt.Printf("BAD acct=%x slots=%d inc=%x full=%x\n", da.AddrHash[:8], len(da.Slots), rootInc[:8], rootFull[:8])
		}
	}
	fmt.Printf("\nchecked=%d bad=%d\n", checked, bad)
	if bad > 0 {
		fmt.Println(">>> reproducible PER-ACCOUNT → storage cursor bug isolatable in a single-account trie")
	} else {
		fmt.Println(">>> per-account ALL OK → whole-tree divergence is CROSS-account (shared cursor)")
	}
}
