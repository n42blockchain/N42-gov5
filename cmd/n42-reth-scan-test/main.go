// n42-reth-scan-test does a full ScanAccounts and reports any panic.
// Isolated repro: does a 386M-entry RethLeafSource scan complete
// cleanly on this machine? If yes, the USDC panic is in the proof-
// generation logic. If no, it's in the leaf source / MDBX layer.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/n42blockchain/N42/internal/mptproof"
)

func main() {
	rethDB := flag.String("reth", `D:\reth2k\db`, "")
	flag.Parse()

	src, err := mptproof.NewRethLeafSource(*rethDB, "PlainAccountState", "PlainStorageState", 4096)
	if err != nil {
		fatal("NewRethLeafSource: %v", err)
	}
	defer src.Close()

	var (
		count   uint64
		lastLog = time.Now()
		t0      = time.Now()
	)
	fmt.Printf("starting full scan of PlainAccountState...\n")

	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\n\n=== PANIC ===\n%v\n", r)
			fmt.Fprintf(os.Stderr, "after %d rows in %s\n", count, time.Since(t0).Truncate(time.Second))
			fmt.Fprintf(os.Stderr, "\nSTACK:\n%s", debug.Stack())
			os.Stderr.Sync()
			os.Stdout.Sync()
			os.Exit(2)
		}
	}()

	err = src.ScanAccounts(func(addr [20]byte, value []byte) error {
		count++
		if time.Since(lastLog) > 5*time.Second {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			fmt.Fprintf(os.Stderr, "scanned %d rows (%.1f M/sec)  heap=%d MB\n",
				count, float64(count)/time.Since(t0).Seconds()/1e6,
				m.HeapInuse>>20)
			lastLog = time.Now()
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan err: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ scan complete: %d rows in %s\n", count, time.Since(t0).Truncate(time.Second))
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
