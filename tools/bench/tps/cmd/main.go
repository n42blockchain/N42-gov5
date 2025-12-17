// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TPS Extreme Benchmark Command
//
// Usage:
//   go run ./tools/bench/tps/cmd -txcount=3000000 -workers=0 -batch=10000
//
// Options:
//   -txcount    Number of transactions (default: 3000000)
//   -workers    Number of workers (0=auto, default: 0)
//   -batch      Batch size (default: 10000)
//   -lockfree   Use lock-free executor (default: false)
//   -verbose    Verbose output (default: false)

package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/tools/bench/tps"
)

func main() {
	// Command line flags
	txCount := flag.Int("txcount", 3000000, "Number of transactions to execute")
	workers := flag.Int("workers", 0, "Number of workers (0=auto-detect)")
	batchSize := flag.Int("batch", 10000, "Batch size for each worker")
	lockFree := flag.Bool("lockfree", false, "Use lock-free executor for maximum TPS")
	verbose := flag.Bool("verbose", false, "Enable verbose output")
	preWarm := flag.Bool("prewarm", true, "Pre-warm state before benchmark")
	
	flag.Parse()
	
	// Print system info
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║             N42 TPS Extreme Benchmark Tool                   ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("System Information:\n")
	fmt.Printf("  CPU Cores:        %d\n", runtime.NumCPU())
	fmt.Printf("  GOMAXPROCS:       %d\n", runtime.GOMAXPROCS(0))
	fmt.Printf("  Go Version:       %s\n", runtime.Version())
	fmt.Printf("  OS/Arch:          %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println()
	
	// Create config
	config := &tps.BenchConfig{
		TxCount:        *txCount,
		Workers:        *workers,
		BatchSize:      *batchSize,
		InitialBalance: uint256.NewInt(1e18),  // 1 ETH
		TransferAmount: uint256.NewInt(1),     // 1 wei
		Verbose:        *verbose,
		PreWarm:        *preWarm,
	}
	
	// Validate
	if config.TxCount <= 0 {
		fmt.Fprintf(os.Stderr, "Error: txcount must be positive\n")
		os.Exit(1)
	}
	if config.BatchSize <= 0 {
		fmt.Fprintf(os.Stderr, "Error: batch must be positive\n")
		os.Exit(1)
	}
	
	// Run benchmark
	var result *tps.ExecutionResult
	var err error
	
	if *lockFree {
		fmt.Println("Mode: Lock-Free (Maximum Theoretical TPS)")
		fmt.Println()
		result, err = tps.RunLockFreeBenchmark(config)
	} else {
		fmt.Println("Mode: Standard (With Transaction Generation & Signing)")
		fmt.Println()
		result, err = tps.RunBenchmark(config)
	}
	
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	
	// Print summary
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║                      BENCHMARK SUMMARY                        ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("  ┌─────────────────────────────────────────────────────────┐\n")
	fmt.Printf("  │  Total TPS:    %12.2f transactions/second          │\n", result.TPS)
	fmt.Printf("  │  Transactions: %12d                               │\n", result.TxCount)
	fmt.Printf("  │  Duration:     %12v                               │\n", result.Duration.Round(time.Millisecond))
	fmt.Printf("  │  Avg Latency:  %12v                               │\n", result.AvgLatency)
	fmt.Printf("  └─────────────────────────────────────────────────────────┘\n")
	fmt.Println()
	
	// Calculate theoretical block stats
	blockTime := float64(12) // 12 seconds standard block time
	txPerBlock := result.TPS * blockTime
	
	fmt.Printf("  Projected Performance (12s block time):\n")
	fmt.Printf("    - Transactions per block: %.0f\n", txPerBlock)
	fmt.Printf("    - Daily transactions:     %.0f million\n", result.TPS * 86400 / 1e6)
	fmt.Println()
	
	// Performance comparison
	fmt.Printf("  Performance Comparison:\n")
	fmt.Printf("    - Ethereum (PoS):  ~15-30 TPS\n")
	fmt.Printf("    - N42 (this test): %.0f TPS\n", result.TPS)
	fmt.Printf("    - Speedup:         %.1fx\n", result.TPS / 20)
	fmt.Println()
}

