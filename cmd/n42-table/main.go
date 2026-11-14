// n42-table is a multi-subcommand tool that replaces the former one-off probe
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
	{"diff", "compare one table between two MDBX dirs (positional: dirA dirB table)", runDiff},
	{"keydiff", "compare key sets of a table between two MDBX dirs (positional)", runKeydiff},
	{"stat", "per-table entry/size stats of an MDBX dir (positional: dir)", runStat},
	{"sum", "checksum of the named tables of an MDBX dir (positional: dir table...)", runSum},
	{"size", "chaindata table entry counts via cursor walk", runSize},
	{"mdbx-stat", "read-only entries / page-count / byte-size per table of an MDBX database", runMdbxStat},
	{"clear", "ClearBucket a named table in a chaindata MDBX env (writes)", runClear},
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: n42-table <subcommand> [flags]\n\nsubcommands:\n")
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
