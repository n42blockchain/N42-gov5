// n42-eth-snapshot is the client-side counterpart to n42-eth-manifest.
// Subcommands:
//
//	verify    walk a manifest and re-hash every file
//	mode      detect which distribution mode a datadir contains
//	fetch     copy missing files from a source into a datadir
//	upgrade   verb form of fetch that targets a higher tier
//	downgrade remove files that the target mode does not list
//
// Sources can be local directories (default) or HTTP URLs.
// Verification is blake2b-256 per file plus the top-level
// manifest_id (sha256 of the sorted file list).
//
// Spec: docs/ethel/n42-eth-client-distribution.md
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/cmd/n42-eth-snapshot/snapshot"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "verify":
		runVerify(os.Args[2:])
	case "mode":
		runMode(os.Args[2:])
	case "fetch":
		runFetch(os.Args[2:])
	case "upgrade":
		runUpgrade(os.Args[2:])
	case "downgrade":
		runDowngrade(os.Args[2:])
	case "delta":
		runDelta(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `n42-eth-snapshot — client snapshot tool

USAGE
    n42-eth-snapshot <subcommand> [flags]

SUBCOMMANDS
    verify         hash every file in a datadir against its manifest
    mode           detect maximal mode (minimal/full/archive) in a datadir
    fetch          copy missing files from --source into --datadir for --mode
    upgrade        fetch the delta needed to move from current to --to mode
    downgrade      remove files not in --to mode's manifest
    delta plan     show what files a delta from --source would fetch
    delta apply    apply an incremental delta from --source
    help           this message

Try:
    n42-eth-snapshot verify --datadir /var/lib/n42
    n42-eth-snapshot mode   --datadir /var/lib/n42
    n42-eth-snapshot fetch  --source file:///mnt/snapshots --datadir /var/lib/n42 --mode archive`)
}

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	datadir := fs.String("datadir", ".", "archive datadir to verify")
	manifestPath := fs.String("manifest", "", "manifest JSON; defaults to <datadir>/manifest-<detected-mode>.json")
	parallel := fs.Int("parallel", 0, "concurrent hashers (0 = NumCPU/2)")
	_ = fs.Parse(args)

	report, err := snapshot.Verify(*datadir, *manifestPath, *parallel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		os.Exit(1)
	}
	report.Print(os.Stdout)
	if !report.OK {
		os.Exit(2)
	}
}

func runMode(args []string) {
	fs := flag.NewFlagSet("mode", flag.ExitOnError)
	datadir := fs.String("datadir", ".", "archive datadir")
	_ = fs.Parse(args)

	det, err := snapshot.DetectMode(*datadir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mode: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("mode=%s  height=%d  intact=%v\n", det.Mode, det.Height, det.Intact)
	if len(det.MissingSections) > 0 {
		fmt.Printf("  missing sections for next tier: %v\n", det.MissingSections)
	}
}

func runFetch(args []string) {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	source := fs.String("source", "", "source: file:///path/to/snapshots or http(s)://host/")
	datadir := fs.String("datadir", "", "destination datadir")
	mode := fs.String("mode", "archive", "target mode (minimal|full|archive)")
	includeSenders := fs.Bool("include-senders", false, "also fetch the senders opt-in pack")
	dryRun := fs.Bool("dry-run", false, "list files that would be fetched, but don't transfer")
	parallel := fs.Int("parallel", 4, "concurrent transfers")
	_ = fs.Parse(args)

	if *source == "" || *datadir == "" {
		fmt.Fprintln(os.Stderr, "--source and --datadir are required")
		os.Exit(2)
	}

	report, err := snapshot.Fetch(*source, *datadir, *mode, *includeSenders, *dryRun, *parallel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch: %v\n", err)
		os.Exit(1)
	}
	report.Print(os.Stdout)
	if !report.OK {
		os.Exit(2)
	}
}

func runUpgrade(args []string) {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	source := fs.String("source", "", "source: file:///path/to/snapshots or http(s)://host/")
	datadir := fs.String("datadir", "", "destination datadir")
	to := fs.String("to", "archive", "target mode (minimal|full|archive)")
	includeSenders := fs.Bool("include-senders", false, "also fetch the senders opt-in pack")
	parallel := fs.Int("parallel", 4, "concurrent transfers")
	_ = fs.Parse(args)

	if *source == "" || *datadir == "" {
		fmt.Fprintln(os.Stderr, "--source and --datadir are required")
		os.Exit(2)
	}

	report, err := snapshot.Upgrade(*source, *datadir, *to, *includeSenders, *parallel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: %v\n", err)
		os.Exit(1)
	}
	report.Print(os.Stdout)
	if !report.OK {
		os.Exit(2)
	}
}

func runDelta(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "delta requires a sub-subcommand (plan|apply)")
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("delta", flag.ExitOnError)
	source := fs.String("source", "", "source: file:///path or http(s)://host/")
	datadir := fs.String("datadir", "", "destination datadir")
	mode := fs.String("mode", "archive", "mode (minimal|full|archive)")
	parallel := fs.Int("parallel", 4, "concurrent transfers (apply only)")
	_ = fs.Parse(rest)
	if *source == "" || *datadir == "" {
		fmt.Fprintln(os.Stderr, "--source and --datadir are required")
		os.Exit(2)
	}

	switch sub {
	case "plan":
		plan, _, err := snapshot.PlanDelta(*source, *datadir, *mode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plan: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("delta plan: mode=%s from=%d → to=%d\n", plan.Mode, plan.FromHeight, plan.ToHeight)
		fmt.Printf("  local manifest_id    : %s\n", plan.LocalManifestID)
		fmt.Printf("  baseline manifest_id : %s\n", plan.BaselineManifestID)
		fmt.Printf("  applicable           : %v\n", plan.Applicable)
		if !plan.Applicable {
			fmt.Printf("  reason               : %s\n", plan.Reason)
		}
		fmt.Printf("  files to fetch       : %d\n", plan.FilesToFetch)
		fmt.Printf("  bytes to fetch       : %.2f GB\n", float64(plan.BytesToFetch)/1024/1024/1024)
	case "apply":
		rep, err := snapshot.ApplyDelta(*source, *datadir, *mode, *parallel)
		if err != nil {
			rep.Print(os.Stderr)
			fmt.Fprintf(os.Stderr, "apply: %v\n", err)
			os.Exit(1)
		}
		rep.Print(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown delta sub-subcommand: %s\n", sub)
		os.Exit(2)
	}
}

func runDowngrade(args []string) {
	fs := flag.NewFlagSet("downgrade", flag.ExitOnError)
	datadir := fs.String("datadir", "", "destination datadir")
	to := fs.String("to", "minimal", "target mode (minimal|full|archive)")
	deleteExtra := fs.Bool("delete-extra", false, "actually delete (default: dry-run report only)")
	_ = fs.Parse(args)

	if *datadir == "" {
		fmt.Fprintln(os.Stderr, "--datadir is required")
		os.Exit(2)
	}

	report, err := snapshot.Downgrade(*datadir, *to, *deleteExtra)
	if err != nil {
		fmt.Fprintf(os.Stderr, "downgrade: %v\n", err)
		os.Exit(1)
	}
	report.Print(os.Stdout)
}
