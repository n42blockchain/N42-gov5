// n42-eth-sim is an end-to-end simulator for the n42-eth
// distribution pipeline. It models:
//
//   1. A publisher that grows an archive by N blocks per tick and
//      publishes both a full release and a delta from the previous
//      release every tick.
//
//   2. One or more client datadirs (per mode: minimal, full, archive)
//      that bootstrap from the publisher's first release and then
//      delta-apply on every subsequent tick.
//
// Two run shapes:
//
//   parallel (default): one publisher, all selected modes run as
//     concurrent clients against it. Tests multi-tenant publisher
//     correctness.
//
//   sequential (--sequential): runs the simulation three times in
//     a row, one per mode, with fresh state each time. Tests each
//     mode's bootstrap+sync flow in isolation.
//
// Simulates "public mirror distribution" via the file:// Source.
// No network, no chain — only the file-level fetch/verify/apply
// mechanics under load.
//
// Usage:
//
//	n42-eth-sim --duration 12m --tick 30s --root /tmp/n42-sim
//	n42-eth-sim --duration 12m --tick 30s --sequential
//	n42-eth-sim --duration 12m --tick 30s --modes full
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/cmd/n42-eth-sim/scenario"
)

func main() {
	root := flag.String("root", filepath.Join(os.TempDir(), "n42-eth-sim"), "scratch root (cleared on start)")
	duration := flag.Duration("duration", 12*time.Minute, "total simulation duration (per mode if --sequential)")
	tick := flag.Duration("tick", 30*time.Second, "publisher tick interval")
	clean := flag.Bool("clean", true, "remove --root at start")
	modesCSV := flag.String("modes", "minimal,full,archive", "comma-separated client modes to track")
	sequential := flag.Bool("sequential", false, "run one --duration phase per mode in turn (instead of all concurrently)")
	flag.Parse()

	if *clean {
		_ = os.RemoveAll(*root)
	}
	if err := os.MkdirAll(*root, 0o755); err != nil {
		die("mkdir root: %v", err)
	}

	modes := splitCSV(*modesCSV)
	if len(modes) == 0 {
		die("--modes must not be empty")
	}

	if *sequential {
		runSequential(*root, modes, *duration, *tick)
		return
	}
	runOne(*root, modes, *duration, *tick, os.Stdout)
}

// runSequential runs `runOne` once per mode, each into its own
// subdir. The aggregate report at the end summarises all three.
func runSequential(root string, modes []string, duration, tick time.Duration) {
	type result struct {
		mode      string
		root      string
		err       error
		pubH      uint64
		cliH      uint64
		totBytes  int64
		ticks     int
		elapsed   time.Duration
		verifyOK  bool
	}
	results := make([]result, 0, len(modes))

	for _, mode := range modes {
		fmt.Printf("\n============================================================\n")
		fmt.Printf(" SEQUENTIAL PHASE: mode = %s   duration = %s   tick = %s\n", mode, duration, tick)
		fmt.Printf("============================================================\n\n")
		sub := filepath.Join(root, "phase-"+mode)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			die("mkdir %s: %v", sub, err)
		}
		t0 := time.Now()
		r := result{mode: mode, root: sub}
		r.pubH, r.cliH, r.totBytes, r.ticks, r.verifyOK, r.err =
			runOneInternal(sub, []string{mode}, duration, tick, os.Stdout)
		r.elapsed = time.Since(t0)
		results = append(results, r)
	}

	fmt.Println("\n============================================================")
	fmt.Println(" SEQUENTIAL RUN — AGGREGATE REPORT")
	fmt.Println("============================================================")
	fmt.Printf("\n%-10s  %-10s  %-10s  %-14s  %-6s  %-10s  %-10s\n",
		"mode", "pub_height", "cli_height", "total_bytes", "ticks", "elapsed", "verify")
	fmt.Printf("%-10s  %-10s  %-10s  %-14s  %-6s  %-10s  %-10s\n",
		"----", "----------", "----------", "-----------", "-----", "-------", "------")
	allOK := true
	for _, r := range results {
		v := "OK"
		if !r.verifyOK || r.err != nil {
			v = "FAIL"
			allOK = false
		}
		fmt.Printf("%-10s  %-10d  %-10d  %-14d  %-6d  %-10s  %-10s\n",
			r.mode, r.pubH, r.cliH, r.totBytes, r.ticks,
			r.elapsed.Truncate(time.Second), v)
	}
	fmt.Println()
	if allOK {
		fmt.Println("ALL PHASES OK")
		return
	}
	fmt.Println("ONE OR MORE PHASES FAILED")
	os.Exit(2)
}

func runOne(root string, modes []string, duration, tick time.Duration, out io.Writer) {
	_, _, _, _, _, err := runOneInternal(root, modes, duration, tick, out)
	if err != nil {
		die("sim: %v", err)
	}
}

// runOneInternal executes a single sim run and returns the key
// metrics. Used both by `runOne` (CLI single-phase) and
// `runSequential` (aggregated reporting).
func runOneInternal(root string, modes []string, duration, tick time.Duration, out io.Writer) (
	pubH uint64, cliH uint64, totBytes int64, ticks int, verifyOK bool, err error) {

	sim, err := scenario.NewWithModes(root, modes)
	if err != nil {
		return 0, 0, 0, 0, false, fmt.Errorf("new scenario: %w", err)
	}

	t0 := time.Now()
	deadline := t0.Add(duration)
	fmt.Fprintf(out, "  root      : %s\n", root)
	fmt.Fprintf(out, "  duration  : %s\n", duration)
	fmt.Fprintf(out, "  tick      : %s\n", tick)
	fmt.Fprintf(out, "  modes     : %s\n", strings.Join(modes, ","))
	fmt.Fprintf(out, "  publisher : %s\n\n", sim.PublishedDir())

	// First tick: initialise publisher + bootstrap client(s).
	if err := sim.PublisherTick(); err != nil {
		return 0, 0, 0, 0, false, fmt.Errorf("publisher tick 0: %w", err)
	}
	if err := sim.BootstrapClients(); err != nil {
		return 0, 0, 0, 0, false, fmt.Errorf("bootstrap clients: %w", err)
	}
	reportTick(out, sim, 0, time.Since(t0), 0, 0)

	tickN := 1
	for time.Now().Before(deadline) {
		if remain := time.Until(deadline); remain < tick {
			if remain <= 0 {
				break
			}
			time.Sleep(remain)
		} else {
			time.Sleep(tick)
		}

		t1 := time.Now()
		if err := sim.PublisherTick(); err != nil {
			return 0, 0, 0, 0, false, fmt.Errorf("publisher tick %d: %w", tickN, err)
		}

		var wg sync.WaitGroup
		errs := make(map[string]error)
		var mu sync.Mutex
		for _, mode := range sim.ActiveModes() {
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
		for m, e := range errs {
			fmt.Fprintf(os.Stderr, "  client %s tick %d failed: %v\n", m, tickN, e)
		}
		reportTick(out, sim, tickN, time.Since(t0), time.Since(t1), len(errs))
		tickN++
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "=== final state ===")
	sim.PrintFinalReport(out)

	verr := sim.VerifyAllClients()
	verifyOK = verr == nil
	if verr != nil {
		fmt.Fprintf(out, "\nFINAL VERIFY FAILED: %v\n", verr)
	} else {
		fmt.Fprintln(out, "\nfinal verify: all clients OK at publisher height")
	}

	st := sim.Status()
	pubH = st.PublisherHeight
	for _, m := range sim.ActiveModes() {
		c := st.Clients[m]
		cliH = c.Height
		totBytes = c.TotalBytes
	}
	ticks = tickN
	return
}

func reportTick(out io.Writer, sim *scenario.Scenario, tickN int, elapsed, tickDur time.Duration, errs int) {
	st := sim.Status()
	fmt.Fprintf(out, "tick %3d  t+%-9s  pub_h=%-7d  ",
		tickN, elapsed.Truncate(time.Second), st.PublisherHeight)
	for _, mode := range sim.ActiveModes() {
		c := st.Clients[mode]
		fmt.Fprintf(out, "%s={h=%d Δfiles=%d Δbytes=%d}  ",
			mode, c.Height, c.LastDeltaFiles, c.LastDeltaBytes)
	}
	if errs > 0 {
		fmt.Fprintf(out, "ERRS=%d", errs)
	}
	if tickDur > 0 {
		fmt.Fprintf(out, "  tickDur=%s", tickDur.Truncate(time.Millisecond))
	}
	fmt.Fprintln(out)
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
