// n42-eth-sim is an end-to-end simulator for the n42-eth
// distribution pipeline. It models:
//
//   1. A publisher that grows an archive by N blocks per tick and
//      publishes both a full release and a delta from the previous
//      release every tick.
//
//   2. Three client datadirs (one per mode: minimal, full, archive)
//      that bootstrap from the publisher's first release and then
//      delta-apply on every subsequent tick.
//
// Simulates "public mirror distribution" via the file:// Source
// in cmd/n42-eth-snapshot/snapshot. No network, no chain — only
// the file-level fetch/verify/apply mechanics under load.
//
// Default scenario: --duration 12m --tick 30s → 24 ticks. Each
// tick adds 1000 fake blocks of growth, mutates the head freezer
// file, occasionally rotates a new .cdat. Per-tick payload is
// small (~10 KB) so the sim is CPU- and disk-cheap; it exercises
// the orchestration, not the data volume.
//
// Usage:
//
//	n42-eth-sim --duration 12m --tick 30s --root /tmp/n42-sim
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/n42blockchain/N42/cmd/n42-eth-sim/scenario"
)

func main() {
	root := flag.String("root", filepath.Join(os.TempDir(), "n42-eth-sim"), "scratch root (cleared on start)")
	duration := flag.Duration("duration", 12*time.Minute, "total simulation duration")
	tick := flag.Duration("tick", 30*time.Second, "publisher tick interval")
	clean := flag.Bool("clean", true, "remove --root at start")
	flag.Parse()

	if *clean {
		_ = os.RemoveAll(*root)
	}
	if err := os.MkdirAll(*root, 0o755); err != nil {
		die("mkdir root: %v", err)
	}

	sim, err := scenario.New(*root)
	if err != nil {
		die("new scenario: %v", err)
	}

	t0 := time.Now()
	deadline := t0.Add(*duration)

	fmt.Printf("=== n42-eth-sim ===\n")
	fmt.Printf("  root      : %s\n", *root)
	fmt.Printf("  duration  : %s\n", *duration)
	fmt.Printf("  tick      : %s\n", *tick)
	fmt.Printf("  publisher : %s\n", sim.PublishedDir())
	fmt.Println()

	// First tick: initialise publisher + bootstrap all clients.
	if err := sim.PublisherTick(); err != nil {
		die("publisher tick 0: %v", err)
	}
	if err := sim.BootstrapClients(); err != nil {
		die("bootstrap clients: %v", err)
	}
	report(sim, 0, time.Since(t0), 0, 0)

	tickN := 1
	for time.Now().Before(deadline) {
		if remain := time.Until(deadline); remain < *tick {
			if remain <= 0 {
				break
			}
			time.Sleep(remain)
		} else {
			time.Sleep(*tick)
		}

		t1 := time.Now()
		if err := sim.PublisherTick(); err != nil {
			die("publisher tick %d: %v", tickN, err)
		}

		// Each client applies the new delta — in parallel for speed.
		var wg sync.WaitGroup
		errs := make(map[string]error)
		var mu sync.Mutex
		for _, mode := range scenario.AllModes {
			mode := mode
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := sim.ClientApplyDelta(mode); err != nil {
					mu.Lock()
					errs[mode] = err
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		for mode, err := range errs {
			fmt.Fprintf(os.Stderr, "  client %s tick %d failed: %v\n", mode, tickN, err)
		}

		report(sim, tickN, time.Since(t0), time.Since(t1), len(errs))
		tickN++
	}

	fmt.Println()
	fmt.Println("=== final state ===")
	sim.PrintFinalReport(os.Stdout)

	if err := sim.VerifyAllClients(); err != nil {
		fmt.Fprintf(os.Stderr, "\nFINAL VERIFY FAILED: %v\n", err)
		os.Exit(2)
	}
	fmt.Println("\nfinal verify: all clients OK at publisher height")
}

func report(sim *scenario.Scenario, tickN int, elapsed, tickDur time.Duration, errs int) {
	st := sim.Status()
	fmt.Printf("tick %3d  t+%-9s  pub_h=%-7d  ",
		tickN, elapsed.Truncate(time.Second), st.PublisherHeight)
	for _, mode := range scenario.AllModes {
		c := st.Clients[mode]
		fmt.Printf("%s={h=%d Δfiles=%d Δbytes=%d}  ",
			mode, c.Height, c.LastDeltaFiles, c.LastDeltaBytes)
	}
	if errs > 0 {
		fmt.Printf("ERRS=%d", errs)
	}
	if tickDur > 0 {
		fmt.Printf("  tickDur=%s", tickDur.Truncate(time.Millisecond))
	}
	fmt.Println()
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
