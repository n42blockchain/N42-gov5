// diff-cs: compare n42 freezer changesets (storcs + acctcs) against reth's
// StorageChangeSets + AccountChangeSets over a block range.
//
// Output: per-block diff summary + any (addr, slot) missing/extra on either
// side. For account diffs, only report block-level counts (account encodings
// differ between n42 and reth).
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const (
	tblStorCS = "StorageChangeSets"
	tblAcctCS = "AccountChangeSets"
)

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[tblStorCS] = kv.TableCfgItem{Flags: kv.DupSort}
	d[tblAcctCS] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

type storKey struct {
	addr [20]byte
	slot [32]byte
}

func main() {
	n42Dir := flag.String("n42", `d:\N42-24m\chain\freezer`, "n42 freezer dir")
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive)")
	toBlock := flag.Uint64("to", 100, "end block (inclusive)")
	showAll := flag.Bool("show-all", false, "show every diff slot, not just summary")
	flag.Parse()

	// --- Open n42 freezer ---
	stor, err := freezer.NewFreezerTableReadOnly(*n42Dir, "storcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer stor.Close()
	stor.ForceBatchSize(freezer.BatchSize)
	stor.SetCompressed(true)

	acct, err := freezer.NewFreezerTableReadOnly(*n42Dir, "acctcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer acct.Close()
	acct.ForceBatchSize(freezer.BatchSize)
	acct.SetCompressed(true)

	// --- Open reth MDBX ---
	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*rethDir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(rethCfg).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	fmt.Printf("Comparing blocks [%d, %d]\n", *fromBlock, *toBlock)
	fmt.Printf("%-8s %-8s %-8s  %s\n", "block", "n42_sto", "reth_sto", "diff")

	totN42Sto, totRethSto := 0, 0
	totN42Acct, totRethAcct := 0, 0

	for b := *fromBlock; b <= *toBlock; b++ {
		// --- n42 storcs at block b ---
		n42Sto := decodeN42Storage(stor, b)
		// --- n42 acctcs at block b ---
		n42Acct := decodeN42Account(acct, b)
		// --- reth Storage at block b ---
		rethSto := decodeRethStorage(tx, b)
		// --- reth Account count at block b ---
		rethAcct := countRethAccount(tx, b)

		totN42Sto += len(n42Sto)
		totRethSto += len(rethSto)
		totN42Acct += n42Acct
		totRethAcct += rethAcct

		// Diff storage
		missing, extra := diffSto(n42Sto, rethSto)
		diffStr := ""
		if len(missing) > 0 || len(extra) > 0 || n42Acct != rethAcct {
			diffStr = fmt.Sprintf("sto_miss=%d sto_extra=%d acct(n42=%d reth=%d)",
				len(missing), len(extra), n42Acct, rethAcct)
		}
		if diffStr != "" || *showAll {
			fmt.Printf("%-8d %-8d %-8d  %s\n", b, len(n42Sto), len(rethSto), diffStr)
		}
		if *showAll {
			for _, k := range missing {
				fmt.Printf("   [n42 missing] addr=%x slot=%x\n", k.addr, k.slot)
			}
			for _, k := range extra {
				fmt.Printf("   [n42 extra]   addr=%x slot=%x\n", k.addr, k.slot)
			}
		} else if len(missing) > 0 || len(extra) > 0 {
			// Print first few.
			for i, k := range missing {
				if i >= 3 {
					fmt.Printf("    ... and %d more missing\n", len(missing)-3)
					break
				}
				fmt.Printf("    [n42 missing] addr=%x slot=%x\n", k.addr, k.slot)
			}
			for i, k := range extra {
				if i >= 3 {
					fmt.Printf("    ... and %d more extra\n", len(extra)-3)
					break
				}
				fmt.Printf("    [n42 extra]   addr=%x slot=%x\n", k.addr, k.slot)
			}
		}
	}
	fmt.Printf("\n=== totals ===\n")
	fmt.Printf("n42  storage rows: %d  account rows: %d\n", totN42Sto, totN42Acct)
	fmt.Printf("reth storage rows: %d  account rows: %d\n", totRethSto, totRethAcct)
}

func decodeN42Storage(tbl *freezer.FreezerTable, block uint64) map[storKey]struct{} {
	out := map[storKey]struct{}{}
	data, err := tbl.Retrieve(block)
	if err != nil || len(data) < 2 {
		return out
	}
	addrCount := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	for g := 0; g < addrCount; g++ {
		if pos+22 > len(data) {
			break
		}
		var k storKey
		copy(k.addr[:], data[pos:pos+20])
		pos += 20
		slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		for s := 0; s < slotCount; s++ {
			if pos+34 > len(data) {
				return out
			}
			copy(k.slot[:], data[pos:pos+32])
			pos += 32
			oldLen := int(data[pos])
			pos++
			if oldLen > 32 || pos+oldLen+1 > len(data) {
				return out
			}
			pos += oldLen
			newLen := int(data[pos])
			pos++
			if newLen > 32 || pos+newLen > len(data) {
				return out
			}
			pos += newLen
			out[k] = struct{}{}
		}
	}
	return out
}

func decodeN42Account(tbl *freezer.FreezerTable, block uint64) int {
	data, err := tbl.Retrieve(block)
	if err != nil || len(data) < 2 {
		return 0
	}
	count := int(binary.LittleEndian.Uint16(data[0:2]))
	return count
}

func decodeRethStorage(tx kv.Tx, block uint64) map[storKey]struct{} {
	out := map[storKey]struct{}{}
	c, err := tx.Cursor(tblStorCS)
	if err != nil {
		return out
	}
	defer c.Close()
	seek := make([]byte, 8)
	binary.BigEndian.PutUint64(seek, block)
	for k, v, err := c.Seek(seek); k != nil; k, v, err = c.Next() {
		if err != nil {
			break
		}
		if len(k) < 28 || len(v) < 32 {
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk != block {
			break
		}
		var key storKey
		copy(key.addr[:], k[8:28])
		copy(key.slot[:], v[:32])
		out[key] = struct{}{}
	}
	return out
}

func countRethAccount(tx kv.Tx, block uint64) int {
	c, err := tx.Cursor(tblAcctCS)
	if err != nil {
		return 0
	}
	defer c.Close()
	seek := make([]byte, 8)
	binary.BigEndian.PutUint64(seek, block)
	n := 0
	for k, _, err := c.Seek(seek); k != nil; k, _, err = c.Next() {
		if err != nil {
			break
		}
		if len(k) < 8 {
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk != block {
			break
		}
		n++
	}
	return n
}

func diffSto(n42 map[storKey]struct{}, reth map[storKey]struct{}) (missing, extra []storKey) {
	for k := range reth {
		if _, ok := n42[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range n42 {
		if _, ok := reth[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		return lessKey(missing[i], missing[j])
	})
	sort.Slice(extra, func(i, j int) bool {
		return lessKey(extra[i], extra[j])
	})
	return
}

func lessKey(a, b storKey) bool {
	cmp := strings.Compare(string(a.addr[:]), string(b.addr[:]))
	if cmp != 0 {
		return cmp < 0
	}
	return strings.Compare(string(a.slot[:]), string(b.slot[:])) < 0
}

var _ = hex.EncodeToString
