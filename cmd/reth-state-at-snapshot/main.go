// reth-state-at-snapshot reconstructs mainnet's account state at the
// "snapshot" block by walking reth's AccountChangeSets in the range
// (snapshotBlock, rethHead] and recording the prevValue at each address's
// first appearance.
//
// Logic: a changeset entry at block N stores (addr, prevValue), where
// prevValue = state of addr just before processing block N. If addr
// was untouched in (snapshotBlock, N-1], that prevValue == state of
// addr at snapshotBlock. So the FIRST appearance of any addr in the
// range gives ground-truth mainnet state at snapshotBlock for that addr.
//
// Coverage = the set of addresses that were modified between
// snapshotBlock+1 and rethHead. This is finite (hundreds of thousands)
// and provides a fast spot-check against n42's PlainState at
// snapshotBlock without re-running rebuild-state.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const tbl = "AccountChangeSets"

func cfg(d kv.TableCfg) kv.TableCfg {
	d[tbl] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	snapBlock := flag.Uint64("snapshot", 24998143, "block whose state to reconstruct")
	endBlock := flag.Uint64("end", 25045128, "scan up to this block (reth head)")
	outFile := flag.String("out", "reth-state-at-snapshot.bin", "output file: addr(20B) || prevLen(2B LE) || prev(prevLen B) per record")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*rethDir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	cur, _ := tx.CursorDupSort(tbl)
	defer cur.Close()

	out, err := os.Create(*outFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create out:", err)
		os.Exit(1)
	}
	defer out.Close()

	// seen: address → already captured. Dedup so we only record the
	// FIRST sighting per address (= the state closest to snapshotBlock).
	seen := make(map[[20]byte]struct{}, 1_000_000)

	seek := make([]byte, 8)
	binary.BigEndian.PutUint64(seek, *snapBlock+1)
	k, v, err := cur.Seek(seek)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}

	t0 := time.Now()
	lastLog := t0
	scanned := 0
	written := 0
	var lenBuf [2]byte

	for k != nil {
		if len(k) < 8 {
			k, v, err = cur.Next()
			_ = err
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk > *endBlock {
			break
		}
		scanned++
		if len(v) >= 20 {
			var addr [20]byte
			copy(addr[:], v[:20])
			if _, ok := seen[addr]; !ok {
				seen[addr] = struct{}{}
				prev := v[20:]
				if len(prev) > 0xffff {
					prev = prev[:0xffff]
				}
				out.Write(addr[:])
				binary.LittleEndian.PutUint16(lenBuf[:], uint16(len(prev)))
				out.Write(lenBuf[:])
				out.Write(prev)
				written++
			}
		}
		k, v, err = cur.Next()
		if err != nil {
			break
		}
		if time.Since(lastLog) > 10*time.Second {
			lastLog = time.Now()
			fmt.Fprintf(os.Stderr, "  scanned=%d unique=%d written=%d elapsed=%v\n",
				scanned, len(seen), written, time.Since(t0).Truncate(time.Second))
		}
	}
	fmt.Printf("\n=== done ===\nscanned=%d unique_addrs=%d written=%d elapsed=%v out=%s\n",
		scanned, len(seen), written, time.Since(t0).Truncate(time.Millisecond), *outFile)
}
