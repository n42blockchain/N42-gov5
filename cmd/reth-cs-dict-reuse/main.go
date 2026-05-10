// reth-cs-dict-reuse measures the actual reuse rate of addresses and
// contract codeHashes in reth's AccountChangeSets / StorageChangeSets
// over rolling block windows. The output answers two questions before
// any V1 dict-codec rollout:
//
//  1. How many bytes of addr / codeHash are written per window vs. how
//     many UNIQUE values appear? (= raw compression headroom)
//  2. What is the top-K coverage (Pareto curve)? Useful to decide between
//     fixed-width ids (3B) and variable-length ids (1B for hot, 3B tail).
//
// The tool walks both changeset tables for a configurable [head-W, head]
// range, accumulates per-window histograms, and prints:
//
//   - total writes, unique values, reuse ratio
//   - byte volume comparison: raw vs 3B fixed id vs 1+2+3B var id
//   - top-K Pareto coverage
//
// codeHash extraction is heuristic: if a value's last 32 bytes are non-zero
// and len(value) >= 33, those 32 bytes are treated as a codeHash. Empty /
// EOA accounts have len < 33 (compact V2 has no codeHash field) so they
// are silently skipped.
package main

import (
	"context"
	"encoding/binary"
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

const (
	tblAcct = "AccountChangeSets"
	tblStor = "StorageChangeSets"
)

func tableCfg(d kv.TableCfg) kv.TableCfg {
	d[tblAcct] = kv.TableCfgItem{Flags: kv.DupSort}
	d[tblStor] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

type stats struct {
	label string

	totalWrites uint64
	freq        map[[32]byte]uint64 // value -> occurrence count
	keyLen      int                 // 20 for addr (low 20 bytes meaningful), 32 for hash
}

func newStats(label string, keyLen int) *stats {
	return &stats{label: label, freq: make(map[[32]byte]uint64), keyLen: keyLen}
}

func (s *stats) add(b []byte) {
	if len(b) < s.keyLen {
		return
	}
	var k [32]byte
	copy(k[32-s.keyLen:], b[:s.keyLen])
	s.freq[k]++
	s.totalWrites++
}

func (s *stats) report() {
	if s.totalWrites == 0 {
		fmt.Printf("=== %s ===\n  empty window\n", s.label)
		return
	}
	uniq := uint64(len(s.freq))
	reuse := float64(s.totalWrites) / float64(uniq)

	rawBytes := s.totalWrites * uint64(s.keyLen)

	// Fixed 3B id encoding: each occurrence = 3B id. Dict header = uniq × (keyLen+3) for forward + reverse.
	// (Forward: id->raw = keyLen bytes. Reverse: raw->id = 3B for index. Counter ignored.)
	dictHeader := uniq * (uint64(s.keyLen) + 3)
	fixed3B := s.totalWrites*3 + dictHeader

	// Variable-length id (1B for top 128, 2B for next 16K, 3B for the rest).
	// 1B prefix bit pattern: 0xxxxxxx → 7 bits (128 ids)
	// 2B prefix: 10xxxxxx xxxxxxxx → 14 bits (16384 ids)
	// 3B prefix: 11xxxxxx xxxxxxxx xxxxxxxx → 22 bits (4M ids)
	counts := make([]uint64, 0, uniq)
	for _, c := range s.freq {
		counts = append(counts, c)
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i] > counts[j] })

	var varBytes uint64
	for i, c := range counts {
		switch {
		case i < 128:
			varBytes += c * 1
		case i < 128+16384:
			varBytes += c * 2
		default:
			varBytes += c * 3
		}
	}
	varBytes += dictHeader

	fmt.Printf("=== %s ===\n", s.label)
	fmt.Printf("  total writes : %d\n", s.totalWrites)
	fmt.Printf("  unique vals  : %d\n", uniq)
	fmt.Printf("  reuse ratio  : %.2f×\n", reuse)
	fmt.Printf("  raw size     : %s  (%dB × %d)\n", humanMiB(rawBytes), s.keyLen, s.totalWrites)
	fmt.Printf("  3B fixed id  : %s  (save %.1f%%, dict header %s)\n",
		humanMiB(fixed3B), 100*(1-float64(fixed3B)/float64(rawBytes)),
		humanMiB(dictHeader))
	fmt.Printf("  varlen 1/2/3B: %s  (save %.1f%%)\n",
		humanMiB(varBytes), 100*(1-float64(varBytes)/float64(rawBytes)))

	// Top-K Pareto
	checkpoints := []int{1, 10, 100, 1_000, 10_000, 100_000}
	fmt.Println("  top-K coverage:")
	var cum uint64
	cpIdx := 0
	for i, c := range counts {
		cum += c
		if cpIdx < len(checkpoints) && i+1 == checkpoints[cpIdx] {
			fmt.Printf("    top %-7d : %12d / %d writes  (%5.1f%%)\n",
				checkpoints[cpIdx], cum, s.totalWrites,
				100*float64(cum)/float64(s.totalWrites))
			cpIdx++
		}
	}
	for ; cpIdx < len(checkpoints); cpIdx++ {
		if uniq >= uint64(checkpoints[cpIdx]) {
			fmt.Printf("    top %-7d : %12d / %d writes  (%5.1f%%)\n",
				checkpoints[cpIdx], cum, s.totalWrites,
				100*float64(cum)/float64(s.totalWrites))
		}
	}
}

func humanMiB(b uint64) string {
	if b < 1<<20 {
		return fmt.Sprintf("%.2f KiB", float64(b)/1024)
	}
	if b < 1<<30 {
		return fmt.Sprintf("%.2f MiB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30))
}

// codeHashFromOldAccount extracts the 32B codeHash from a V2-encoded
// account value if present. Returns nil for EOAs / empty accounts.
//
// V2 layout: [fieldBits 1B][nonce uvarint][balLen 1B + balance N][codeHash 32B if bit3].
// Heuristic: if len >= 33 and the last 32 bytes are non-zero, treat as
// codeHash. This catches contract entries reliably; EOA entries have
// len < 33 because the compact encoding omits codeHash.
func codeHashFromOldAccount(v []byte) []byte {
	if len(v) < 33 {
		return nil
	}
	tail := v[len(v)-32:]
	allZero := true
	for _, b := range tail {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil
	}
	return tail
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	headBlock := flag.Uint64("head", 24766147, "head block (inclusive)")
	windows := flag.String("windows", "10000,100000,216000",
		"comma-separated window sizes in blocks (10K=1 freezer file, 216K=30 days)")
	progressEvery := flag.Duration("progress", 15*time.Second, "progress interval")
	flag.Parse()

	var winSizes []uint64
	for _, s := range splitCSV(*windows) {
		v, err := parseUint(s)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad window:", err)
			os.Exit(1)
		}
		winSizes = append(winSizes, v)
	}
	if len(winSizes) == 0 {
		fmt.Fprintln(os.Stderr, "no windows specified")
		os.Exit(1)
	}

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
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	for _, win := range winSizes {
		startBlock := *headBlock - win + 1
		endBlock := *headBlock

		fmt.Printf("\n############################################\n")
		fmt.Printf("# Window: %d blocks  [%d, %d]\n", win, startBlock, endBlock)
		fmt.Printf("############################################\n")

		acctAddr := newStats(fmt.Sprintf("AccountCS addr (W=%d)", win), 20)
		acctCH := newStats(fmt.Sprintf("AccountCS codeHash (W=%d)", win), 32)
		storAddr := newStats(fmt.Sprintf("StorageCS addr (W=%d)", win), 20)

		// AccountCS: key=block(8B), value=addr(20B) + V2 acct
		{
			cdup, err := tx.CursorDupSort(tblAcct)
			if err != nil {
				fmt.Fprintln(os.Stderr, "acct cursor:", err)
				os.Exit(1)
			}
			seekKey := make([]byte, 8)
			binary.BigEndian.PutUint64(seekKey, startBlock)
			startT := time.Now()
			lastT := startT
			var rows uint64
			for k, v, err := cdup.Seek(seekKey); k != nil; k, v, err = cdup.Next() {
				if err != nil {
					break
				}
				if len(k) < 8 {
					continue
				}
				blk := binary.BigEndian.Uint64(k[:8])
				if blk > endBlock {
					break
				}
				if len(v) < 20 {
					continue
				}
				acctAddr.add(v[:20])
				if ch := codeHashFromOldAccount(v[20:]); ch != nil {
					acctCH.add(ch)
				}
				rows++
				if time.Since(lastT) >= *progressEvery {
					lastT = time.Now()
					elapsed := lastT.Sub(startT).Seconds()
					fmt.Fprintf(os.Stderr, "  [W=%d] acct rows=%d uniq_addr=%d uniq_ch=%d rate=%.0f/s\n",
						win, rows, len(acctAddr.freq), len(acctCH.freq),
						float64(rows)/elapsed)
				}
			}
			cdup.Close()
		}

		// StorageCS: key=block(8B)+addr(20B), DUPSORT slots
		{
			cdup, err := tx.CursorDupSort(tblStor)
			if err != nil {
				fmt.Fprintln(os.Stderr, "stor cursor:", err)
				os.Exit(1)
			}
			seekKey := make([]byte, 8)
			binary.BigEndian.PutUint64(seekKey, startBlock)
			startT := time.Now()
			lastT := startT
			var keys uint64
			// NextNoDup: each (block, addr) is one address group occurrence.
			for k, _, err := cdup.Seek(seekKey); k != nil; k, _, err = cdup.NextNoDup() {
				if err != nil {
					break
				}
				if len(k) < 28 {
					continue
				}
				blk := binary.BigEndian.Uint64(k[:8])
				if blk > endBlock {
					break
				}
				storAddr.add(k[8:28])
				keys++
				if time.Since(lastT) >= *progressEvery {
					lastT = time.Now()
					elapsed := lastT.Sub(startT).Seconds()
					fmt.Fprintf(os.Stderr, "  [W=%d] stor groups=%d uniq_addr=%d rate=%.0f/s\n",
						win, keys, len(storAddr.freq), float64(keys)/elapsed)
				}
			}
			cdup.Close()
		}

		acctAddr.report()
		acctCH.report()
		storAddr.report()
	}
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' || r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func parseUint(s string) (uint64, error) {
	var v uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("bad digit in %q", s)
		}
		v = v*10 + uint64(r-'0')
	}
	return v, nil
}
