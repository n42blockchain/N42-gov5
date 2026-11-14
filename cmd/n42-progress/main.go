// n42-progress is a multi-subcommand tool that replaces the former one-off probe
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
	{"read", "print the ethel progress marker for a datadir (flags)", runRead},
	{"show", "read the ethel progress marker from MDBX (--datadir D)", runShow},
	{"set", "write the ethel progress marker to MDBX (--datadir D --block N [--key K])", runSet},
	{"write", "set the ethel progress marker for a datadir (flags)", runWrite},
	{"read-ethel", "print all known progress markers in an MDBX env (read-only)", runReadEthel},
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: n42-progress <subcommand> [flags]\n\nsubcommands:\n")
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
