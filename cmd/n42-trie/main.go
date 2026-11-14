// n42-trie is a multi-subcommand tool that replaces the former one-off probe
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
	{"stat", "TrieAccount/TrieStorage record counts, bytes and node-shape classes", runStat},
	{"probe", "structural shape of a migrated chaindata's TrieOfAccounts/TrieOfStorage", runProbe},
	{"fulldiff", "full N42-native trie rebuild over a reth-migrated DB, diff vs stored nodes", runFulldiff},
	{"recorddiff", "rebuild one account's storage subtree from HashedStorage, diff records", runRecorddiff},
	{"compress", "measure compressibility of reth AccountsTrie/StoragesTrie tables", runCompress},
	{"branch-anatomy", "byte-level dissection of reth trie branch nodes", runBranchAnatomy},
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: n42-trie <subcommand> [flags]\n\nsubcommands:\n")
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
