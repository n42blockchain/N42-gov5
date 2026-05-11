// stor-spotcheck-at-block harvests mainnet (addr, slot)→value ground truth
// from reth's StorageChangeSets in the range (block, rethEnd] and compares
// each against n42's Storage table. Counterpart to acct-spotcheck-at-block.
//
// Reth StorageChangeSets layout (DupSort + AutoDupSort 40+32):
//   physical key = block(8B BE) || addr(20B)            // 28B
//   dup value    = slot(32B) || compact-BE U256 prev    // 32 + 0..32 B
// First sighting of any (addr, slot) at block N > snapshotBlock implies
// that (addr, slot) was untouched between snapshotBlock+1 and N-1, so
// the recorded prev value IS the state at snapshotBlock.
//
// Memory: 50-200M (addr,slot) entries × ~52 + 32 B each. On a 47K-block
// window, expect 10-50M unique pairs ≈ 1-5 GB heap. Bounded.
package main

import (
	"bytes"
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

const (
	n42Sto       = "Storage"
	rethStorCSet = "StorageChangeSets"
)

func n42Cfg(d kv.TableCfg) kv.TableCfg {
	d[n42Sto] = kv.TableCfgItem{}
	return d
}
func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[rethStorCSet] = kv.TableCfgItem{
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                72,
		DupToLen:                  40,
	}
	return d
}

func main() {
	n42Dir := flag.String("n42", `d:\rebuilt-state`, "n42 datadir (Storage table)")
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	snapBlock := flag.Uint64("block", 24998143, "n42 head block — state snapshot anchor")
	rethEnd := flag.Uint64("reth-end", 25045128, "reth head block")
	maxDiffs := flag.Int("max-diffs", 30, "stop after this many diffs")
	flag.Parse()

	logger := log.New()
	rethDB, err := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(rethCfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer rethDB.Close()
	rethTx, _ := rethDB.BeginRo(context.Background())
	defer rethTx.Rollback()

	// Phase 1: harvest first prevValue per (addr, slot).
	// Key is [addr(20) || slot(32)] = 52 bytes.
	// Use AsRaw via raw cursor — we read the cursor in the form MDBX
	// returns the DupSort table (physical key + value bytes).
	// Since AutoDupSortKeysConversion=true, cursor.First/Next returns
	// reconstructed 72-byte logical k (block||addr||slot) and the
	// post-slot trailing bytes as v.
	cur, err := rethTx.CursorDupSort(rethStorCSet)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer cur.Close()

	mainnetAt := make(map[[52]byte][]byte, 50_000_000)

	seek := make([]byte, 8)
	binary.BigEndian.PutUint64(seek, *snapBlock+1)
	t0 := time.Now()
	scanned := 0
	k, v, err := cur.Seek(seek)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}
	for k != nil {
		if len(k) < 8 {
			k, v, _ = cur.Next()
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk > *rethEnd {
			break
		}
		scanned++
		// AutoDupSort reconstructs logical k=72B. v then has 0..32B prev.
		// But if reader doesn't reconstruct, k is 28B (block+addr) and
		// v is 32B slot + 0..32B prev. Handle both shapes.
		var key [52]byte
		var prev []byte
		switch {
		case len(k) == 72: // reconstructed: block(8)+addr(20)+slot(32)
			copy(key[:20], k[8:28])
			copy(key[20:], k[28:60])
			prev = v
		case len(k) == 28 && len(v) >= 32: // raw DupSort: k=block+addr, v=slot+prev
			copy(key[:20], k[8:28])
			copy(key[20:], v[:32])
			prev = v[32:]
		default:
			k, v, _ = cur.Next()
			continue
		}
		if _, ok := mainnetAt[key]; !ok {
			cp := make([]byte, len(prev))
			copy(cp, prev)
			mainnetAt[key] = cp
		}
		k, v, err = cur.Next()
		if err != nil {
			break
		}
		if scanned%5_000_000 == 0 {
			fmt.Fprintf(os.Stderr, "  scanned=%d unique=%d elapsed=%v\n",
				scanned, len(mainnetAt), time.Since(t0).Truncate(time.Second))
		}
	}
	fmt.Fprintf(os.Stderr, "Phase 1 done: reth storage changeset rows scanned=%d unique_pairs=%d elapsed=%v\n",
		scanned, len(mainnetAt), time.Since(t0).Truncate(time.Second))

	// Phase 2: for each unique (addr, slot), lookup n42 Storage and compare.
	n42DB, err := mdbx.NewMDBX(logger).Path(*n42Dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(n42Cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open n42:", err)
		os.Exit(1)
	}
	defer n42DB.Close()
	n42Tx, _ := n42DB.BeginRo(context.Background())
	defer n42Tx.Rollback()

	t1 := time.Now()
	checked := 0
	diffs := 0
	missingN42 := 0
	extraN42 := 0
	var diffKeys [][52]byte
	for key, mainVal := range mainnetAt {
		checked++
		n42Val, err := n42Tx.GetOne(n42Sto, key[:])
		if err != nil {
			continue
		}
		// Normalize: trim leading zeros from mainnet value (reth stores
		// already-trimmed BE). n42 stores trimmed too. So direct compare.
		mainTrim := mainVal
		// Trim leading zeros defensively.
		for len(mainTrim) > 0 && mainTrim[0] == 0 {
			mainTrim = mainTrim[1:]
		}
		if len(mainTrim) == 0 && n42Val == nil {
			continue // both empty / not present
		}
		if len(mainTrim) > 0 && n42Val == nil {
			missingN42++
			if diffs < *maxDiffs {
				fmt.Printf("[MISSING in n42] addr=%x slot=%x mainnet_prev=%x\n",
					key[:20], key[20:], mainTrim)
				diffs++
			}
			continue
		}
		if len(mainTrim) == 0 && len(n42Val) > 0 {
			extraN42++
			if diffs < *maxDiffs {
				fmt.Printf("[EXTRA in n42 ] addr=%x slot=%x n42_val=%x\n",
					key[:20], key[20:], n42Val)
				diffs++
			}
			continue
		}
		if !bytes.Equal(mainTrim, n42Val) {
			if diffs < *maxDiffs {
				fmt.Printf("[VAL DIFF    ] addr=%x slot=%x\n  mainnet=%x\n  n42    =%x\n",
					key[:20], key[20:], mainTrim, n42Val)
				diffs++
			}
			diffKeys = append(diffKeys, key)
		}
	}
	fmt.Printf("\n=== summary ===\n")
	fmt.Printf("mainnet ground-truth (addr,slot) pairs: %d\n", len(mainnetAt))
	fmt.Printf("checked: %d\n", checked)
	fmt.Printf("VAL DIFFs printed: %d (total set has more if maxDiffs hit)\n", len(diffKeys))
	fmt.Printf("missing-in-n42: %d (mainnet had value, n42 has no row)\n", missingN42)
	fmt.Printf("extra-in-n42: %d (mainnet had nothing, n42 has value)\n", extraN42)
	fmt.Printf("phase2 elapsed: %v\n", time.Since(t1).Truncate(time.Second))
}
