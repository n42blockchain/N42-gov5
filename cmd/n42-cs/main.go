// n42-cs is a multi-subcommand tool that replaces the former one-off probe
// binaries (see docs/TOOLS.md for the old-name to subcommand mapping).
package main

import (
	"fmt"
	"os"
)

type subcommand struct {
	name string
	desc string
	run  func(args []string)
}

var subcommands = []subcommand{
	{"storcs-grep", "grep a hex byte pattern across all storcs freezer blobs", runStorcsGrep},
	{"storcs-diag", "extract and inspect one block's storcs entry from a compressed freezer", runStorcsDiag},
	{"storcs-extract-wipes", "extract per-block storage 'wipe' entries from a storcs freezer table", runStorcsExtractWipes},
	{"storcs-bytes-breakdown", "sample storcs blocks, break physical bytes down by component", runStorcsBytesBreakdown},
	{"acctcs-grep", "grep a hex byte pattern across all acctcs freezer blobs", runAcctcsGrep},
	{"acctcs-extract-wipes", "list contract addresses whose storage must be prefix-wiped, per block", runAcctcsExtractWipes},
	{"inspect-acctcs", "scan acctcs to find entries with codeHash bit set", runInspectAcctcs},
	{"cmp", "compare two n42 freezer dirs block-by-block on acctcs/storcs/witness", runCmp},
	{"diff-cs", "compare n42 freezer changesets against reth Storage/AccountChangeSets", runDiffCs},
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: n42-cs <subcommand> [flags]\n\nsubcommands:\n")
	for _, s := range subcommands {
		fmt.Fprintf(os.Stderr, "  %-24s %s\n", s.name, s.desc)
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	for _, s := range subcommands {
		if s.name == os.Args[1] {
			s.run(os.Args[2:])
			return
		}
	}
	fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
	usage()
	os.Exit(2)
}
