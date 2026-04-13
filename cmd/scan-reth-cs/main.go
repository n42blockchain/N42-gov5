package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// Scan reth's AccountChangeSets / StorageChangeSets and count update frequency.
//
// Reth tables (from reth source):
//   AccountChangeSets:  DUPSORT  key=BlockNumber(8B BE)  value=AccountBeforeTx(20B addr + ...)
//   StorageChangeSets:  DUPSORT  key=BlockNumberAddress(28B = 8B BE block + 20B addr)  value=StorageEntry(32B slot + value)

const (
	tblAcctCS = "AccountChangeSets"
	tblStorCS = "StorageChangeSets"
)

func tableCfg(defaults kv.TableCfg) kv.TableCfg {
	defaults[tblAcctCS] = kv.TableCfgItem{Flags: kv.DupSort}
	defaults[tblStorCS] = kv.TableCfgItem{Flags: kv.DupSort}
	return defaults
}

func main() {
	dbPath := flag.String("db", "d:/reth2k/db", "reth MDBX path")
	what := flag.String("what", "account", "account or storage")
	maxBlock := flag.Uint64("max", 0, "max block (0=all)")
	thr := flag.Int("thr", 6, "threshold for hot data count")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(tableCfg).
		Open(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "tx: %v\n", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	if *what == "account" {
		scanAccount(tx, *maxBlock, *thr)
	} else {
		scanStorage(tx, *maxBlock, *thr)
	}
}

// Sharded by addr[0] to avoid Go map's pathological behavior at huge sizes.
const numShards = 256

func scanAccount(tx kv.Tx, maxBlock uint64, threshold int) {
	cursor, err := tx.Cursor(tblAcctCS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cursor: %v\n", err)
		os.Exit(1)
	}
	defer cursor.Close()

	// 256 shards keyed by uint64 hash (first 8B of addr).
	// Tradeoff: ~0.01% collisions for 200M unique addrs, acceptable for histogram.
	shards := make([]map[uint64]uint32, numShards)
	for i := range shards {
		shards[i] = make(map[uint64]uint32, 2_000_000)
	}
	t0 := time.Now()
	var total uint64
	lastReport := t0

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			fmt.Fprintf(os.Stderr, "iter: %v\n", err)
			os.Exit(1)
		}
		if len(k) < 8 || len(v) < 20 {
			continue
		}
		if maxBlock > 0 {
			b := uint64(k[0])<<56 | uint64(k[1])<<48 | uint64(k[2])<<40 | uint64(k[3])<<32 |
				uint64(k[4])<<24 | uint64(k[5])<<16 | uint64(k[6])<<8 | uint64(k[7])
			if b > maxBlock {
				break
			}
		}
		// Use first 8 bytes of addr as hash key.
		hash := uint64(v[0])<<56 | uint64(v[1])<<48 | uint64(v[2])<<40 | uint64(v[3])<<32 |
			uint64(v[4])<<24 | uint64(v[5])<<16 | uint64(v[6])<<8 | uint64(v[7])
		shards[v[0]][hash]++
		total++
		if time.Since(lastReport) > 5*time.Second {
			uniq := 0
			for _, s := range shards {
				uniq += len(s)
			}
			fmt.Fprintf(os.Stderr, "  scanned %d entries, %d uniq addrs, %.0f/s\n",
				total, uniq, float64(total)/time.Since(t0).Seconds())
			lastReport = time.Now()
		}
	}
	uniq := 0
	for _, s := range shards {
		uniq += len(s)
	}
	fmt.Fprintf(os.Stderr, "scan done: %d entries, %d uniq addrs, %v\n", total, uniq, time.Since(t0))

	hist := map[int]int{}
	var hotCount int
	for _, s := range shards {
		for _, c := range s {
			hist[bucket(int(c))]++
			if int(c) > threshold {
				hotCount++
			}
		}
	}
	fmt.Printf("\n=== Account update distribution ===\n")
	fmt.Printf("Total updates:    %d\n", total)
	fmt.Printf("Unique addresses: %d\n", uniq)
	fmt.Printf("Avg updates/addr: %.2f\n", float64(total)/float64(uniq))
	fmt.Printf("\nUpdate count distribution:\n")
	keys := make([]int, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		fmt.Printf("  %s: %d (%.2f%%)\n", bucketLabel(k), hist[k], float64(hist[k])*100/float64(uniq))
	}
	fmt.Printf("\n>%d updates: %d (%.2f%%)\n", threshold, hotCount, float64(hotCount)*100/float64(uniq))
}

func scanStorage(tx kv.Tx, maxBlock uint64, threshold int) {
	cursor, err := tx.Cursor(tblStorCS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cursor: %v\n", err)
		os.Exit(1)
	}
	defer cursor.Close()

	// 256 shards keyed by uint64 hash (xor of first 8B addr and first 8B slot).
	shards := make([]map[uint64]uint32, numShards)
	for i := range shards {
		shards[i] = make(map[uint64]uint32, 8_000_000)
	}
	t0 := time.Now()
	var total uint64
	lastReport := t0

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			fmt.Fprintf(os.Stderr, "iter: %v\n", err)
			os.Exit(1)
		}
		if len(k) < 28 || len(v) < 32 {
			continue
		}
		if maxBlock > 0 {
			b := uint64(k[0])<<56 | uint64(k[1])<<48 | uint64(k[2])<<40 | uint64(k[3])<<32 |
				uint64(k[4])<<24 | uint64(k[5])<<16 | uint64(k[6])<<8 | uint64(k[7])
			if b > maxBlock {
				break
			}
		}
		// Hash: addr first 8B XOR slot first 8B XOR slot last 8B.
		addr := k[8:28]
		slot := v[:32]
		h := uint64(addr[0])<<56 | uint64(addr[1])<<48 | uint64(addr[2])<<40 | uint64(addr[3])<<32 |
			uint64(addr[4])<<24 | uint64(addr[5])<<16 | uint64(addr[6])<<8 | uint64(addr[7])
		s1 := uint64(slot[0])<<56 | uint64(slot[1])<<48 | uint64(slot[2])<<40 | uint64(slot[3])<<32 |
			uint64(slot[4])<<24 | uint64(slot[5])<<16 | uint64(slot[6])<<8 | uint64(slot[7])
		s2 := uint64(slot[24])<<56 | uint64(slot[25])<<48 | uint64(slot[26])<<40 | uint64(slot[27])<<32 |
			uint64(slot[28])<<24 | uint64(slot[29])<<16 | uint64(slot[30])<<8 | uint64(slot[31])
		hash := h ^ s1 ^ s2
		shards[addr[0]][hash]++
		total++
		if time.Since(lastReport) > 5*time.Second {
			uniq := 0
			for _, s := range shards {
				uniq += len(s)
			}
			fmt.Fprintf(os.Stderr, "  scanned %d entries, %d uniq slots, %.0f/s\n",
				total, uniq, float64(total)/time.Since(t0).Seconds())
			lastReport = time.Now()
		}
	}
	uniq := 0
	for _, s := range shards {
		uniq += len(s)
	}
	fmt.Fprintf(os.Stderr, "scan done: %d entries, %d uniq slots, %v\n", total, uniq, time.Since(t0))

	hist := map[int]int{}
	var hotCount int
	for _, s := range shards {
		for _, c := range s {
			hist[bucket(int(c))]++
			if int(c) > threshold {
				hotCount++
			}
		}
	}
	fmt.Printf("\n=== Storage update distribution ===\n")
	fmt.Printf("Total updates: %d\n", total)
	fmt.Printf("Unique slots:  %d\n", uniq)
	fmt.Printf("Avg updates/slot: %.2f\n", float64(total)/float64(uniq))
	fmt.Printf("\nUpdate count distribution:\n")
	keys := make([]int, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		fmt.Printf("  %s: %d (%.2f%%)\n", bucketLabel(k), hist[k], float64(hist[k])*100/float64(uniq))
	}
	fmt.Printf("\n>%d updates: %d (%.2f%%)\n", threshold, hotCount, float64(hotCount)*100/float64(uniq))
}

func bucket(c int) int {
	switch {
	case c == 1:
		return 1
	case c == 2:
		return 2
	case c == 3:
		return 3
	case c <= 5:
		return 5
	case c <= 7:
		return 7
	case c <= 10:
		return 10
	case c <= 50:
		return 50
	case c <= 100:
		return 100
	case c <= 1000:
		return 1000
	default:
		return 100000
	}
}

func bucketLabel(b int) string {
	switch b {
	case 1:
		return "1     "
	case 2:
		return "2     "
	case 3:
		return "3     "
	case 5:
		return "4-5   "
	case 7:
		return "6-7   "
	case 10:
		return "8-10  "
	case 50:
		return "11-50 "
	case 100:
		return "51-100"
	case 1000:
		return "101-1k"
	default:
		return ">1k   "
	}
}
