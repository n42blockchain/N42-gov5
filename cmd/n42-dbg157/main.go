// n42-dbg157: dump ONE failing storage account's exact trie shape, slots, and
// dirty set, then run incremental (trace) vs full so the storage-cursor bug is
// visible on a tiny case.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
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

func ck(e error, w string) {
	if e != nil {
		fmt.Fprintln(os.Stderr, "FATAL", w, e)
		os.Exit(1)
	}
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "")
	dump := flag.String("dump", `C:/tmp/dirty157.gob`, "")
	prefix := flag.String("acct", "54af0c0062d4664f", "addrHash prefix hex")
	trace := flag.Bool("trace", false, "loader trace")
	flag.Parse()

	pfx, _ := hex.DecodeString(*prefix)
	f, _ := os.Open(*dump)
	var d commitment.DirtyDump
	ck(gob.NewDecoder(f).Decode(&d), "decode")
	f.Close()

	var ah []byte
	var dirty []commitment.DirtySlot
	for _, da := range d.Storage {
		if bytes.HasPrefix(da.AddrHash[:], pfx) {
			ah = append([]byte(nil), da.AddrHash[:]...)
			dirty = da.Slots
			break
		}
	}
	if ah == nil {
		fmt.Println("acct not found in dump")
		os.Exit(1)
	}
	fmt.Printf("acct addrHash=%x  dirty slots=%d\n", ah, len(dirty))

	src, err := mdbx.NewMDBX(log.New()).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().WithTableCfg(cfg).Open(context.Background())
	ck(err, "open src")
	defer src.Close()
	stx, _ := src.BeginRo(context.Background())
	defer stx.Rollback()

	fmt.Println("=== TrieStorage nodes (156-state) for this account ===")
	tc, _ := stx.Cursor("TrieStorage")
	for k, v, e := tc.Seek(ah); k != nil; k, v, e = tc.Next() {
		if e != nil || len(k) < 32 || !bytes.Equal(k[:32], ah) {
			break
		}
		path := k[32:]
		if len(v) >= 6 {
			st := binary.BigEndian.Uint16(v[0:2])
			tr := binary.BigEndian.Uint16(v[2:4])
			ha := binary.BigEndian.Uint16(v[4:6])
			nh := (len(v) - 6) / 32
			plus1 := bits.OnesCount16(ha)+1 == nh
			fmt.Printf("  path=%x (len %d)  st=%04x tr=%04x ha=%04x  hashes=%d  +1own=%v\n", path, len(path), st, tr, ha, nh, plus1)
			// For the [07] node (single nibble 7), dump its cached child hashes
			// for the suspect children 2,4,9,b,f to compare against inc/full.
			if len(path) == 1 && path[0] == 0x07 {
				hashesStart := 6
				if plus1 {
					hashesStart = 6 + 32 // +1 own-hash is first
				}
				hh := v[hashesStart:]
				idx := 0
				for nib := 0; nib < 16; nib++ {
					if ha&(1<<uint(nib)) == 0 {
						continue
					}
					if (idx+1)*32 <= len(hh) {
						mark := ""
						if nib == 2 || nib == 4 || nib == 9 || nib == 0xb || nib == 0xf {
							mark = "  <-- suspect (inc-wrong)"
						}
						fmt.Printf("    [07] child %x cachedHash=%x%s\n", nib, hh[idx*32:idx*32+6], mark)
					}
					idx++
				}
			}
		}
	}
	tc.Close()

	var nSlots int
	sc, _ := stx.Cursor("HashedStorage")
	for k, _, e := sc.Seek(ah); k != nil; k, _, e = sc.Next() {
		if e != nil || len(k) != 64 || !bytes.Equal(k[:32], ah) {
			break
		}
		nSlots++
	}
	sc.Close()
	fmt.Printf("total slots (pre-157) = %d\n", nSlots)

	fmt.Println("=== 157 dirty slots (INSERT vs MODIFY) ===")
	for _, ds := range dirty {
		var comp [64]byte
		copy(comp[:32], ah)
		copy(comp[32:], ds.SlotHash[:])
		old, _ := stx.GetOne("HashedStorage", comp[:])
		kind := "SET"
		if len(ds.Value) == 0 {
			kind = "DELETE"
		}
		existed := "INSERT(new)"
		if len(old) > 0 {
			existed = fmt.Sprintf("MODIFY(old=%x)", old)
		}
		fmt.Printf("  slotHash=%x %s %s newval=%x\n", ds.SlotHash[:8], kind, existed, ds.Value)
	}

	// reproduce inc vs full on a single-account memdb
	mem := mdbx.NewMDBX(log.New()).InMem(os.TempDir() + "/n42-dbg157").MapSize(8 * datasize.GB).WithTableCfg(cfg).MustOpen()
	defer mem.Close()
	rl := trie.NewRetainList(0)
	wtx, _ := mem.BeginRw(context.Background())
	av, _ := stx.GetOne("HashedAccount", ah)
	wtx.Put("HashedAccount", append([]byte(nil), ah...), append([]byte(nil), av...))
	sc2, _ := stx.Cursor("HashedStorage")
	for k, v, e := sc2.Seek(ah); k != nil; k, v, e = sc2.Next() {
		if e != nil || len(k) != 64 || !bytes.Equal(k[:32], ah) {
			break
		}
		wtx.Put("HashedStorage", append([]byte(nil), k...), append([]byte(nil), v...))
	}
	sc2.Close()
	for _, ds := range dirty {
		var k [64]byte
		copy(k[:32], ah)
		copy(k[32:], ds.SlotHash[:])
		if len(ds.Value) == 0 {
			wtx.Delete("HashedStorage", k[:])
		} else {
			wtx.Put("HashedStorage", k[:], append([]byte(nil), ds.Value...))
		}
		rl.AddKeyWithMarker(k[:], true)
	}
	tc2, _ := stx.Cursor("TrieStorage")
	for k, v, e := tc2.Seek(ah); k != nil; k, v, e = tc2.Next() {
		if e != nil || len(k) < 32 || !bytes.Equal(k[:32], ah) {
			break
		}
		wtx.Put("TrieStorage", append([]byte(nil), k...), append([]byte(nil), v...))
	}
	tc2.Close()
	wtx.Commit()

	type sem struct {
		st, tr, ha uint16
		hashes     string
	}
	mkSC := func(m map[string]sem) trie.StorageHashCollector2 {
		return func(acc, keyHex []byte, st, tr, ha uint16, hashes, root []byte) error {
			m[string(append([]byte(nil), keyHex...))] = sem{st, tr, ha, string(hashes)}
			return nil
		}
	}
	incNodes, fullNodes := map[string]sem{}, map[string]sem{}

	fmt.Println("=== INCREMENTAL (cached TrieStorage + retain dirty) ===")
	rtx, _ := mem.BeginRo(context.Background())
	li := trie.NewFlatDBTrieLoader("inc", rl, nil, mkSC(incNodes), *trace)
	rootInc, _ := li.CalcTrieRoot(rtx, nil)
	rtx.Rollback()

	wtx2, _ := mem.BeginRw(context.Background())
	wtx2.ClearBucket("TrieStorage")
	wtx2.Commit()
	fmt.Println("=== FULL (cleared TrieStorage) ===")
	rtx2, _ := mem.BeginRo(context.Background())
	lf := trie.NewFlatDBTrieLoader("full", trie.NewRetainList(0), nil, mkSC(fullNodes), *trace)
	rootFull, _ := lf.CalcTrieRoot(rtx2, nil)
	rtx2.Rollback()

	fmt.Printf("\ninc=%x\nfull=%x\nmatch=%v\n", rootInc[:], rootFull[:], rootInc == rootFull)

	// storage-node diff: full is ground truth
	fmt.Println("=== STORAGE-NODE DIFF (full vs incremental) ===")
	diff, miss := 0, 0
	for k, fv := range fullNodes {
		iv, ok := incNodes[k]
		if !ok {
			miss++ // unchanged (cached) — expected; not printed
			continue
		}
		if iv != fv {
			diff++
			fmt.Printf("DIFF path=%x len=%d  st-eq=%v tr-eq=%v ha-eq=%v\n", []byte(k), len(k), iv.st == fv.st, iv.tr == fv.tr, iv.ha == fv.ha)
			ih, fh := []byte(iv.hashes), []byte(fv.hashes)
			// map each stored hash slot to its child nibble (set bits of ha, in order)
			ha := fv.ha
			idx := 0
			for nib := 0; nib < 16; nib++ {
				if ha&(1<<uint(nib)) == 0 {
					continue
				}
				lo, hi := idx*32, idx*32+32
				if hi <= len(ih) && hi <= len(fh) {
					if !bytes.Equal(ih[lo:hi], fh[lo:hi]) {
						fmt.Printf("    child nibble %x DIFFERS  inc=%x full=%x\n", nib, ih[lo:lo+6], fh[lo:lo+6])
					}
				}
				idx++
			}
		}
	}
	// also print ALL incremental-emitted node paths (the dirty paths it recomputed)
	fmt.Printf("incremental emitted %d nodes at paths:\n", len(incNodes))
	for k := range incNodes {
		fmt.Printf("  inc-path=%x\n", []byte(k))
	}
	extra := 0
	for k := range incNodes {
		if _, ok := fullNodes[k]; !ok {
			extra++
			fmt.Printf("EXTRA-IN-INC path=%x\n", []byte(k))
		}
	}
	fmt.Printf("storage nodes: full=%d inc=%d  diff=%d missing-in-inc=%d extra-in-inc=%d\n", len(fullNodes), len(incNodes), diff, miss, extra)
}
