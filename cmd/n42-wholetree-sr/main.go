// n42-wholetree-sr reproduces a failing block's incremental root offline and
// pinpoints whether the bug is in storage roots or the account-trie combine,
// WITHOUT modifying the chaindata (RW tx, rolled back). It applies the captured
// dirty set (N42_DUMPDIRTY) to the post-parent state, then:
//   - incremental root (retain dirty, marked) vs full-descent root (retain-all);
//   - per-account storage-root check (incremental whole-tree vs fresh per-account);
//   - account-trie node diff (incremental updates vs full nodes).
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"flag"
	"fmt"
	"math/bits"
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

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "chaindata (RW, rolled back)")
	dump := flag.String("dump", `C:/tmp/dirty157.gob`, "dirty-set dump")
	flag.Parse()

	f, err := os.Open(*dump)
	ck(err, "open dump")
	var d commitment.DirtyDump
	ck(gob.NewDecoder(f).Decode(&d), "decode dump")
	f.Close()
	fmt.Printf("loaded %d dirty accts, %d storage-accts\n", len(d.Accounts), len(d.Storage))

	db, err := mdbx.NewMDBX(log.New()).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).WithTableCfg(cfg).Open(context.Background())
	ck(err, "open db")
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	ck(err, "beginRw")
	defer tx.Rollback() // never commit

	// Apply the dirty set exactly as TrieRootComputer.ComputeRoot does:
	// accounts (Put/Delete HashedAccount), storage (Put/Delete HashedStorage),
	// retain list WITH the "created" marker.
	rl := trie.NewRetainList(0)
	for _, da := range d.Accounts {
		ah := append([]byte(nil), da.AddrHash[:]...)
		if len(da.Value) == 0 {
			ck(tx.Delete("HashedAccount", ah), "del acct")
			// delete this account's storage (deleteAccountStorage)
			delAcctStorage(tx, da.AddrHash[:])
		} else {
			ck(tx.Put("HashedAccount", ah, append([]byte(nil), da.Value...)), "put acct")
		}
		rl.AddKeyWithMarker(ah, true)
	}
	interest := map[string]bool{}
	for _, da := range d.Storage {
		interest[string(da.AddrHash[:])] = true
		for _, ds := range da.Slots {
			var k [64]byte
			copy(k[:32], da.AddrHash[:])
			copy(k[32:], ds.SlotHash[:])
			if len(ds.Value) == 0 {
				ck(tx.Delete("HashedStorage", k[:]), "del slot")
			} else {
				ck(tx.Put("HashedStorage", k[:], append([]byte(nil), ds.Value...)), "put slot")
			}
			rl.AddKeyWithMarker(k[:], true)
		}
	}

	// --- incremental: capture account-node updates + per-account storage roots ---
	type sem struct {
		st, tr, ha uint16
		hashes     []byte
	}
	incAcc := map[string]sem{}
	delAcc := map[string]bool{}
	incAccSC := func(keyHex []byte, st, tr, ha uint16, hashes, root []byte) error {
		k := string(append([]byte(nil), keyHex...))
		if st == 0 {
			delAcc[k] = true
		} else {
			incAcc[k] = sem{st, tr, ha, append([]byte(nil), hashes...)}
		}
		return nil
	}
	incStoSR := map[string][]byte{}
	incStoSC := func(acc, keyHex []byte, st, tr, ha uint16, hashes, root []byte) error {
		if len(keyHex) == 0 && len(acc) == 32 && interest[string(acc)] && len(root) == 32 {
			incStoSR[string(acc)] = append([]byte(nil), root...)
		}
		return nil
	}
	li := trie.NewFlatDBTrieLoader("wt-inc", rl, incAccSC, incStoSC, false)
	rootInc, err := li.CalcTrieRoot(tx, nil)
	ck(err, "inc CalcTrieRoot")

	// --- full descent (retain-all) — ground truth root + account nodes ---
	parse := func(v []byte) sem {
		ha := binary.BigEndian.Uint16(v[4:6])
		nh := bits.OnesCount16(ha)
		return sem{binary.BigEndian.Uint16(v[0:2]), binary.BigEndian.Uint16(v[2:4]), ha, append([]byte(nil), v[6:6+32*nh]...)}
	}
	eq := func(a sem, st, tr, ha uint16, hashes []byte) bool {
		return a.st == st && a.tr == tr && a.ha == ha && bytes.Equal(a.hashes, hashes)
	}
	taC, _ := tx.Cursor("TrieAccount")
	defer taC.Close()
	wrongU, wrongD, missed, fullN, logged := 0, 0, 0, 0, 0
	fullAccSC := func(keyHex []byte, st, tr, ha uint16, hashes, root []byte) error {
		if st == 0 {
			return nil
		}
		fullN++
		k := string(keyHex)
		if s, ok := incAcc[k]; ok {
			if !eq(s, st, tr, ha, hashes) {
				wrongU++
				if logged < 20 {
					logged++
					fmt.Printf("WRONG-UPDATE key=%x len=%d\n  inc  st=%04x tr=%04x ha=%04x nh=%d\n  full st=%04x tr=%04x ha=%04x nh=%d\n", keyHex, len(keyHex), s.st, s.tr, s.ha, len(s.hashes)/32, st, tr, ha, len(hashes)/32)
				}
			}
			return nil
		}
		if delAcc[k] {
			wrongD++
			if logged < 20 {
				logged++
				fmt.Printf("WRONG-DELETE key=%x (full still has it)\n", keyHex)
			}
			return nil
		}
		fk, vv, _ := taC.SeekExact(keyHex)
		if fk == nil || len(vv) < 6 {
			missed++
			if logged < 20 {
				logged++
				fmt.Printf("MISSED key=%x (full has node, cached missing)\n", keyHex)
			}
			return nil
		}
		cs := parse(vv)
		if !eq(cs, st, tr, ha, hashes) {
			missed++
			if logged < 20 {
				logged++
				fmt.Printf("MISSED-UPDATE key=%x len=%d\n  cached st=%04x tr=%04x ha=%04x nh=%d\n  full   st=%04x tr=%04x ha=%04x nh=%d\n", keyHex, len(keyHex), cs.st, cs.tr, cs.ha, len(cs.hashes)/32, st, tr, ha, len(hashes)/32)
			}
		}
		return nil
	}
	fullStoSR := map[string][]byte{}
	fullStoSC := func(acc, keyHex []byte, st, tr, ha uint16, hashes, root []byte) error {
		if len(keyHex) == 0 && len(acc) == 32 && interest[string(acc)] && len(root) == 32 {
			fullStoSR[string(acc)] = append([]byte(nil), root...)
		}
		return nil
	}
	lf := trie.NewFlatDBTrieLoader("wt-full", trie.NewRetainList(1<<30), fullAccSC, fullStoSC, false)
	rootFull, err := lf.CalcTrieRoot(tx, nil)
	ck(err, "full CalcTrieRoot")

	// Compare incremental vs full storage roots for dirty storage accounts.
	stoBad, stoMissInc := 0, 0
	for _, da := range d.Storage {
		k := string(da.AddrHash[:])
		full, okF := fullStoSR[k]
		if !okF {
			continue
		}
		inc, okI := incStoSR[k]
		if !okI {
			stoMissInc++
			continue
		}
		if !bytes.Equal(inc, full) {
			stoBad++
			if stoBad <= 20 {
				fmt.Printf("STO-ROOT DIVERGE acct=%x slots=%d  inc=%x  full=%x\n", da.AddrHash[:8], len(da.Slots), inc[:8], full[:8])
			}
		}
	}
	fmt.Printf("storage-root check: diverge=%d  missing-inc=%d (of %d interest)\n", stoBad, stoMissInc, len(fullStoSR))

	fmt.Printf("\nincremental root = %x\nfull-descent root = %x   match=%v\n", rootInc[:12], rootFull[:12], rootInc == rootFull)
	fmt.Printf("ACCOUNT-trie: full nodes=%d  wrong-update=%d  wrong-delete=%d  missed=%d\n", fullN, wrongU, wrongD, missed)
	if wrongU == 0 && wrongD == 0 && missed == 0 {
		fmt.Println(">>> account-trie nodes ALL MATCH → divergence is in storage roots (or none); check incStoSR vs fresh")
	} else {
		fmt.Println(">>> account-trie DIVERGES → bug is in the account-trie combine (like block 156's insert)")
	}
	fmt.Printf("captured %d incremental storage roots (interest accts)\n", len(incStoSR))
}

func delAcctStorage(tx kv.RwTx, ah []byte) {
	c, _ := tx.Cursor("HashedStorage")
	var del [][]byte
	for k, _, e := c.Seek(ah); k != nil; k, _, e = c.Next() {
		if e != nil || len(k) < 32 || !bytes.Equal(k[:32], ah) {
			break
		}
		del = append(del, append([]byte(nil), k...))
	}
	c.Close()
	for _, k := range del {
		tx.Delete("HashedStorage", k)
	}
}

func ck(e error, what string) {
	if e != nil {
		fmt.Fprintln(os.Stderr, "FATAL", what, e)
		os.Exit(1)
	}
}
