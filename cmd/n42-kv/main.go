// n42-kv is a multi-subcommand tool that replaces the former one-off probe
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
	{"account", "dump an account record from N42 PlainState (nonce/balance/codeHash/code length)", runAccount},
	{"storage", "print current value of (address, slot) pairs in a Plain Account/Storage MDBX", runStorage},
	{"storage-prefix", "dump every Storage row whose key starts with a 20-byte address", runStoragePrefix},
	{"storage-seek", "seek to an (addr, slot) key in Storage and print existence and value", runStorageSeek},
	{"header-root", "print header.Root (stateRoot) of a block from an MDBX chaindata", runHeaderRoot},
	{"acct-probe", "dump one address's account state from N42 and reth side by side", runAcctProbe},
	{"sto-probe", "read one storage slot from N42's Storage table (DupSort+AutoConv)", runStoProbe},
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: n42-kv <subcommand> [flags]\n\nsubcommands:\n")
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
