// reth-value-stats scans reth's PlainAccountState and PlainStorageState and
// reports value-length histograms plus total-size estimates for several
// storage layouts:
//
//	(A) fixed-length (pad to max)        : N * max_len
//	(B) fixed-length at p99             : N * p99 + small overflow side-table
//	(C) variable + 4B offset table       : N * 4 + sum(len)   [segment <= 4 GiB]
//	(D) variable + 8B offset table       : N * 8 + sum(len)
//	(E) RecSplit MPHF only (1.8 bit/key) : reference floor
//
// Reth layout:
//
//	PlainAccountState:  key = addr(20B)        value = compact account (variable, ~5..100 B)
//	PlainStorageState DUPSORT:
//	  key = addr(20B)
//	  value = slot(32B) || compact U256        (logical value len = len(v) - 32, 1..32 B)
//
// For PlainStorageState we strip the leading 32B slot prefix when computing
// value-length statistics — only the U256 bytes count toward the snapshot
// payload.
package main

import (
	"context"
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
	tblAcct = "PlainAccountState"
	tblStor = "PlainStorageState"
)

func tableCfg(d kv.TableCfg) kv.TableCfg {
	d[tblAcct] = kv.TableCfgItem{}
	d[tblStor] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

type stats struct {
	count    uint64
	totalLen uint64
	maxLen   int
	minLen   int
	hist     [256]uint64 // value length histogram (length in bytes, capped at 255)
}

func (s *stats) add(l int) {
	s.count++
	s.totalLen += uint64(l)
	if s.maxLen == 0 && s.minLen == 0 {
		s.minLen = l
	}
	if l > s.maxLen {
		s.maxLen = l
	}
	if l < s.minLen {
		s.minLen = l
	}
	if l > 255 {
		l = 255
	}
	s.hist[l]++
}

// percentile returns smallest length L such that >= p fraction of entries have length <= L.
func (s *stats) percentile(p float64) int {
	if s.count == 0 {
		return 0
	}
	target := uint64(float64(s.count) * p)
	var cum uint64
	for i := 0; i < 256; i++ {
		cum += s.hist[i]
		if cum >= target {
			return i
		}
	}
	return 255
}

func (s *stats) sumUpToLen(L int) uint64 {
	var sum uint64
	for i := 0; i <= L && i < 256; i++ {
		sum += s.hist[i] * uint64(i)
	}
	return sum
}

func (s *stats) countAboveLen(L int) uint64 {
	var c uint64
	for i := L + 1; i < 256; i++ {
		c += s.hist[i]
	}
	return c
}

func (s *stats) sumAboveLen(L int) uint64 {
	var sum uint64
	for i := L + 1; i < 256; i++ {
		sum += s.hist[i] * uint64(i)
	}
	return sum
}

func gib(x uint64) string {
	return fmt.Sprintf("%.2f GiB", float64(x)/(1<<30))
}
func mib(x uint64) string {
	return fmt.Sprintf("%.2f MiB", float64(x)/(1<<20))
}

func report(label string, s *stats) {
	if s.count == 0 {
		fmt.Printf("\n=== %s ===\nempty\n", label)
		return
	}
	avg := float64(s.totalLen) / float64(s.count)

	fmt.Printf("\n=== %s ===\n", label)
	fmt.Printf("entries        : %d\n", s.count)
	fmt.Printf("total value B  : %d  (%s)\n", s.totalLen, gib(s.totalLen))
	fmt.Printf("min / avg / max: %d / %.2f / %d\n", s.minLen, avg, s.maxLen)
	for _, p := range []float64{0.50, 0.90, 0.95, 0.99, 0.999} {
		fmt.Printf("p%-5s len      : %d\n", fmt.Sprintf("%.1f", p*100), s.percentile(p))
	}

	fmt.Println("\nlen-histogram (bytes -> count, density)")
	for i := 0; i <= s.maxLen && i < 256; i++ {
		if s.hist[i] == 0 {
			continue
		}
		fmt.Printf("  %3dB : %12d   %6.3f%%\n",
			i, s.hist[i], float64(s.hist[i])*100/float64(s.count))
	}

	// MPHF baseline (1.8 bit/key)
	mphfBytes := uint64(float64(s.count) * 1.8 / 8)

	fmt.Println("\nlayout cost comparisons (value bytes only; MPHF bits/key = 1.8)")
	fmt.Printf("  MPHF only (no payload)               : %s\n", gib(mphfBytes))

	// Layout A: fixed at max
	A := uint64(s.maxLen) * s.count
	fmt.Printf("  (A) fixed @ max=%dB                   : %s + MPHF %s = %s\n",
		s.maxLen, gib(A), gib(mphfBytes), gib(A+mphfBytes))

	// Layout B: fixed at p99 + overflow
	for _, p := range []struct {
		p     float64
		label string
	}{{0.99, "p99"}, {0.995, "p99.5"}, {0.999, "p99.9"}} {
		L := s.percentile(p.p)
		fixedPart := uint64(L) * s.count
		over := s.countAboveLen(L)
		// Overflow side-table: 8B (key offset/seq) + value bytes (with 1B len prefix)
		overheadOverflow := over*9 + s.sumAboveLen(L)
		total := fixedPart + overheadOverflow
		fmt.Printf("  (B) fixed @ %s=%dB + overflow(%d)    : fixed %s + overflow %s = %s + MPHF %s = %s\n",
			p.label, L, over, gib(fixedPart), gib(overheadOverflow), gib(fixedPart+overheadOverflow), gib(mphfBytes),
			gib(total+mphfBytes))
	}

	// Layout C: variable + 4B offset
	C := s.count*4 + s.totalLen
	fmt.Printf("  (C) varlen + 4B offset (seg<=4GiB)   : offsets %s + values %s = %s + MPHF %s = %s\n",
		gib(s.count*4), gib(s.totalLen), gib(C), gib(mphfBytes), gib(C+mphfBytes))

	// Layout D: variable + 8B offset
	D := s.count*8 + s.totalLen
	fmt.Printf("  (D) varlen + 8B offset               : offsets %s + values %s = %s + MPHF %s = %s\n",
		gib(s.count*8), gib(s.totalLen), gib(D), gib(mphfBytes), gib(D+mphfBytes))

	// Inline length-prefix (1B len + value), no offset table — but lookup needs offset table OR sequential scan.
	inlineNoOffset := s.totalLen + s.count
	fmt.Printf("  (E) varlen + 1B-len-prefix only      : %s   (lookup needs offset table -> add option C/D)\n",
		gib(inlineNoOffset))
}

func scanTable(tx kv.Tx, table string, stripPrefix int, isDupsort bool) (*stats, error) {
	var (
		c   kv.Cursor
		err error
	)
	if isDupsort {
		c, err = tx.CursorDupSort(table)
	} else {
		c, err = tx.Cursor(table)
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()

	s := &stats{}
	startT := time.Now()
	lastT := startT
	var bytes uint64
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		_ = k
		l := len(v) - stripPrefix
		if l < 0 {
			l = 0
		}
		s.add(l)
		bytes += uint64(len(v))
		if time.Since(lastT) >= 15*time.Second {
			lastT = time.Now()
			elapsed := lastT.Sub(startT).Seconds()
			fmt.Fprintf(os.Stderr, "  %s: rows=%d bytes=%.1fGB rate=%.0f rows/s\n",
				table, s.count, float64(bytes)/(1<<30),
				float64(s.count)/elapsed)
		}
	}
	fmt.Fprintf(os.Stderr, "  %s done: rows=%d in %v\n", table, s.count, time.Since(startT))
	return s, nil
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	skipAcct := flag.Bool("skip-acct", false, "skip PlainAccountState")
	skipStor := flag.Bool("skip-stor", false, "skip PlainStorageState")
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

	if !*skipAcct {
		fmt.Fprintln(os.Stderr, "scanning PlainAccountState...")
		s, err := scanTable(tx, tblAcct, 0, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "acct scan:", err)
			os.Exit(1)
		}
		report("PlainAccountState (value = compact account)", s)
	}

	if !*skipStor {
		fmt.Fprintln(os.Stderr, "scanning PlainStorageState...")
		// strip 32B slot prefix to count only U256 bytes
		s, err := scanTable(tx, tblStor, 32, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, "stor scan:", err)
			os.Exit(1)
		}
		report("PlainStorageState (value = compact U256, 32B slot prefix stripped)", s)
	}
}
