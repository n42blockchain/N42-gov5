// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// weekly.go — the weekly update as one command.
//
//	n42-datc weekly --out A --headers H --changesets C [--queries strata.json] -- <build flags>
//
// It runs, in order, and stops at the first step that fails:
//
//	build           resume the archive to the new tip (the flags after "--"
//	                are the archive's own build flags; --out/--headers/
//	                --changesets/--end are supplied here)
//	derive-ns       --from E: the exact storage records of the new blocks
//	derive-plan     which contracts grew past the listing threshold this week
//	derive-ns       --contracts: those, in full (rungs, partitions, records)
//	verify-ns       --from E: storage roots through the reader vs the history
//	verify          --samples: account roots vs the headers
//	bench           the stratified queries (--queries) and a mixed sample of the
//	                new blocks, every proof verified, gated on --max-ms
//
// Each step is this same binary run as a child process, so a step's own
// fatal-error handling and its output stay exactly what they are when run by
// hand, and the log shows the commands to repeat one. The steps after the
// build only write under leafseg/ (through a spill and a merge); a failure
// there leaves the spill in leafspill/ and the archive as it was.
//
// A finalize must never run twice over the same spill (rows would be merged in
// twice), which is what an automatic restart after a crash does. So weekly
// takes a lock file, refuses to start over a leftover spill, and must not be
// wrapped in a restart loop.
package datc

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// archiveHead reads DatcMeta/head: the first block the archive does not cover.
func archiveHead(dir string) (uint64, error) {
	db, err := openArchiveDB(dir, 2048)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(tDatcMeta, []byte("head"))
	if err != nil || len(v) < 8 {
		return 0, fmt.Errorf("DatcMeta/head missing (%v)", err)
	}
	return beUint64(v), nil
}

// weeklyStep is one child command.
type weeklyStep struct {
	name string
	args []string
	skip string // non-empty: why the step is not run
	// needsList: the step reads derive-plan's list and is skipped when that
	// came out empty.
	needsList bool
}

// weeklyPlan lists the steps after the build for an archive that grew from
// block `from`. newList is the file derive-plan writes; listNonEmpty is asked
// when the plan reaches the step that depends on it.
func weeklyPlan(out, headers, changesets, queries string, from uint64, workers int, maxMs float64, newList string) []weeklyStep {
	f := strconv.FormatUint(from, 10)
	w := strconv.Itoa(workers)
	ms := strconv.FormatFloat(maxMs, 'f', -1, 64)
	steps := []weeklyStep{
		{name: "extend the exact ladder", args: []string{"derive-ns", "--out", out, "--from", f, "--workers", w}},
		{name: "plan newly large contracts", args: []string{"derive-plan", "--out", out, "--list", newList}},
		{name: "derive newly large contracts", args: []string{"derive-ns", "--out", out, "--contracts", newList, "--workers", w}, needsList: true},
		{name: "verify storage roots through the reader", args: []string{"verify-ns", "--out", out, "--from", f, "--contracts", "400"}},
		{name: "verify account roots", args: []string{"verify", "--out", out, "--headers", headers, "--samples", "50"}},
	}
	if queries != "" {
		steps = append(steps, weeklyStep{name: "bench: stratified queries", args: []string{"bench", "--out", out, "--headers", headers,
			"--queries", queries, "--parallel", "8", "--gate-max-ms", ms}})
	} else {
		steps = append(steps, weeklyStep{name: "bench: stratified queries", skip: "no --queries file"})
	}
	steps = append(steps, weeklyStep{name: "bench: the new blocks", args: []string{"bench", "--out", out, "--headers", headers,
		"--changesets", changesets, "--from", f, "--samples", "500", "--slots", "2", "--mode", "mixed", "--parallel", "8", "--gate-max-ms", ms}})
	return steps
}

func runWeekly(args []string) {
	fs := flag.NewFlagSet("weekly", flag.ExitOnError)
	out := fs.String("out", "", "the archive")
	headers := fs.String("headers", "", "headerc freezer dir")
	changesets := fs.String("changesets", "", "acctcs/storcs freezer dir")
	queries := fs.String("queries", "", "stratified bench queries (bench-plan); skipped when empty")
	from := fs.Uint64("from", 0, "previous head; 0 = the archive's DatcMeta/head before the build")
	skipBuild := fs.Bool("skip-build", false, "the build already ran (then --from is required)")
	workers := fs.Int("workers", 32, "derive-ns workers")
	maxMs := fs.Float64("max-ms", 1000, "fail a bench when a verified proof took longer than this")
	dryRun := fs.Bool("dry-run", false, "print the commands, run nothing")
	_ = fs.Parse(args)
	buildFlags := fs.Args() // everything after "--"
	if *out == "" || *headers == "" || *changesets == "" {
		die("weekly needs --out, --headers and --changesets")
	}
	if *skipBuild && *from == 0 {
		die("--skip-build needs --from (the head before the build ran)")
	}
	self, err := os.Executable()
	if err != nil {
		die("executable: %v", err)
	}

	if !*dryRun {
		if spills, _ := filepath.Glob(filepath.Join(*out, leafSpillDir, "*.zspill")); len(spills) > 0 {
			die("%s holds %d spill file(s): an earlier build, derive-ns or finalize did not finish.\n"+
				"Find out which (a finalize that was interrupted must NOT simply be repeated) before running weekly.",
				filepath.Join(*out, leafSpillDir), len(spills))
		}
		lock := filepath.Join(*out, "weekly.lock")
		lf, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			die("%s exists: another weekly run is active, or one died — check, then remove it by hand (%v)", lock, err)
		}
		fmt.Fprintf(lf, "pid %d, started %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
		lf.Close()
		defer os.Remove(lock)
	}

	run := func(name string, argv []string) {
		fmt.Printf("\n=== weekly: %s\n    %s %s\n", name, self, strings.Join(argv, " "))
		if *dryRun {
			return
		}
		t0 := time.Now()
		cmd := exec.Command(self, argv...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Remove(filepath.Join(*out, "weekly.lock"))
			die("weekly: step %q failed (%v). Nothing after it ran. Fix the cause and repeat from this step by hand:\n    %s %s",
				name, err, self, strings.Join(argv, " "))
		}
		fmt.Printf("=== weekly: %s — done in %s\n", name, time.Since(t0).Truncate(time.Second))
	}

	prev := *from
	if prev == 0 {
		if prev, err = archiveHead(*out); err != nil {
			die("head: %v", err)
		}
	}
	if !*skipBuild {
		argv := append([]string{"build", "--out", *out, "--headers", *headers, "--changesets", *changesets, "--end", "99999999"}, buildFlags...)
		run("resume the build to the new tip", argv)
	}
	head := prev
	if !*dryRun {
		if head, err = archiveHead(*out); err != nil {
			die("head: %v", err)
		}
		if head <= prev {
			fmt.Printf("\nweekly: the archive head is still %d — no new blocks, nothing to do.\n", head)
			return
		}
	}
	newList := filepath.Join(*out, "weekly-new-contracts.txt")
	for _, st := range weeklyPlan(*out, *headers, *changesets, *queries, prev, *workers, *maxMs, newList) {
		switch {
		case st.skip != "":
			fmt.Printf("\n=== weekly: %s — skipped (%s)\n", st.name, st.skip)
		case st.needsList && !*dryRun && !fileNonEmpty(newList):
			fmt.Printf("\n=== weekly: %s — skipped (no contract crossed the threshold)\n", st.name)
		default:
			run(st.name, st.args)
		}
	}
	if *dryRun {
		fmt.Printf("\nweekly: dry run — nothing was executed (previous head %d).\n", prev)
		return
	}
	fmt.Printf("\nweekly: archive %s extended %d -> %d; every step passed.\n", *out, prev, head)
}

func fileNonEmpty(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}
