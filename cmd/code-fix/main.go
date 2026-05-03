// code-fix: detect and unwrap reth's LegacyAnalyzedBytecode wrapper in
// the Code table. Walks every entry (key=codeHash, value=stored_bytes)
// and checks if keccak256(value) == key. If not, tries to extract the
// original bytecode by parsing reth's compact format and writes back
// the unwrapped bytes.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func keccak(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}

func main() {
	dir := flag.String("dir", "", "n42 datadir")
	dryRun := flag.Bool("dry-run", false, "report only, do not write")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: code-fix --dir <DATADIR> [--dry-run]")
		os.Exit(1)
	}

	logger := log.New()
	// MapSize 4 TB so write doesn't hit MDBX_MAP_FULL on big Code tables.
	// Drop Accede() because it short-circuits SetGeometry — the MapSize
	// arg would be silently ignored otherwise.
	db, err := mdbx.NewMDBX(logger).
		Path(*dir).
		Label(kv.ChainDB).
		MapSize(4 * datasize.TB).
		GrowthStep(2 * datasize.GB).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	c, err := tx.Cursor(modules.Code)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	type fix struct {
		key, value []byte
	}
	var (
		total     int
		alreadyOK int
		fixed     int
		failed    int
		fixes     []fix
	)

	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			os.Exit(1)
		}
		total++

		// Check if already correct
		h := keccak(v)
		if equalBytes(h, k) {
			alreadyOK++
			continue
		}

		// Try to extract original bytecode from reth's compact format.
		// Empirical structure (USDT analyzed): [4 bytes BE length] [N bytes
		// bytecode + padding] [jump table bitmap].
		// Strategy: try various slice candidates and check hash.
		original := tryExtract(v, k)
		if original != nil {
			fixes = append(fixes, fix{key: append([]byte{}, k...), value: original})
			fixed++
		} else {
			failed++
			if failed <= 5 {
				prefix := v[:min(20, len(v))]
				fmt.Printf("  FAIL: codeHash=%x len=%d prefix=%x\n", k, len(v), prefix)
			}
		}

		if total%100_000 == 0 {
			fmt.Printf("  scanned %d total (alreadyOK=%d fixed=%d failed=%d)\n",
				total, alreadyOK, fixed, failed)
		}
	}
	c.Close()

	fmt.Printf("=== summary ===\ntotal=%d alreadyOK=%d fixed=%d failed=%d\n",
		total, alreadyOK, fixed, failed)

	if *dryRun {
		fmt.Println("dry-run, no writes")
		return
	}
	if len(fixes) == 0 {
		fmt.Println("no fixes needed")
		return
	}

	cw, err := tx.RwCursor(modules.Code)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rwcursor:", err)
		os.Exit(1)
	}
	for i, f := range fixes {
		if err := cw.Put(f.key, f.value); err != nil {
			fmt.Fprintln(os.Stderr, "put:", err)
			os.Exit(1)
		}
		if (i+1)%100_000 == 0 {
			fmt.Printf("  wrote %d/%d\n", i+1, len(fixes))
		}
	}
	cw.Close()
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "commit:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d fixes\n", len(fixes))
}

// tryExtract searches for a byte range whose keccak256 matches expectedHash.
// Strategy: O(1) per entry — try a handful of fixed-offset candidates
// based on observed reth LegacyAnalyzedBytecode format.
//
//	Format: [4B BE length] [N bytes bytecode (incl. 33B revm padding)] [jump table]
//	where length ∈ {original_len, original_len+1, original_len-32, original_len-33}.
func tryExtract(v []byte, expectedHash []byte) []byte {
	if len(v) < 4 {
		// Try exact match.
		if equalBytes(keccak(v), expectedHash) {
			return append([]byte{}, v...)
		}
		return nil
	}
	l32 := int(binary.BigEndian.Uint32(v[:4]))
	// Strategy 1: try lengths near l32 (the BE uint32 hint at offset 0).
	for _, off := range []int{0, -1, 1, -33, -32, -34, +33, -43, +43} {
		n := l32 + off
		if n > 0 && 4+n <= len(v) {
			slice := v[4 : 4+n]
			if equalBytes(keccak(slice), expectedHash) {
				return append([]byte{}, slice...)
			}
		}
	}
	// Strategy 2: expand search around l32 (covers other small offsets).
	maxRange := 256
	for off := 2; off <= maxRange; off++ {
		for _, sign := range []int{-1, 1} {
			n := l32 + off*sign
			if n > 0 && 4+n <= len(v) {
				slice := v[4 : 4+n]
				if equalBytes(keccak(slice), expectedHash) {
					return append([]byte{}, slice...)
				}
			}
		}
	}
	// Strategy 3: full sweep from L=1 to L=len-4 at offset 4 (slow but
	// guaranteed for any code that starts at offset 4).
	maxLen := len(v) - 4
	if maxLen > 4096 { // cap to avoid quadratic blow-up on huge entries
		maxLen = 4096
	}
	for n := 1; n <= maxLen; n++ {
		// Skip lengths already tried in Strategy 1/2.
		diff := n - l32
		if diff < 0 {
			diff = -diff
		}
		if diff <= maxRange+10 {
			continue
		}
		slice := v[4 : 4+n]
		if equalBytes(keccak(slice), expectedHash) {
			return append([]byte{}, slice...)
		}
	}
	// Strategy 4: try various small prefix offsets.
	for _, prefix := range []int{0, 1, 2, 3, 5, 8} {
		if prefix >= len(v) {
			continue
		}
		slice := v[prefix:]
		if equalBytes(keccak(slice), expectedHash) {
			return append([]byte{}, slice...)
		}
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
