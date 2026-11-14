// n42-code is a multi-subcommand tool that replaces the former one-off probe
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
	{"check", "print the Code table row count of a datadir's MDBX", runCheck},
	{"check-account", "look up an account's codeHash and check its bytecode in the Code table", runCheckAccount},
	{"get", "look up bytecode by codeHash or via an account in PlainState", runGet},
	{"copy", "copy the Code table from one MDBX to another (writes; positional --src/--dst)", runCopy},
	{"fix", "detect and unwrap reth LegacyAnalyzedBytecode wrappers in the Code table (writes)", runFix},
	{"check-hashed", "verify every HashedAccount codeHash has a Code-table entry", runCheckHashed},
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: n42-code <subcommand> [flags]\n\nsubcommands:\n")
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
