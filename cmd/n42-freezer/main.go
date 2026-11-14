// n42-freezer is a multi-subcommand tool that replaces the former one-off probe
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
	{"heads", "print every table's coverage in a freezer directory (arg: dir)", runHeads},
	{"info", "dump items() per table of a freezer dir, raw and compressed", runInfo},
	{"items", "print item counts of freezer tables (positional)", runItems},
	{"retrim", "rewrite a per-item freezer table's cidx to trim it (writes)", runRetrim},
	{"block-senders-list", "ecrecover a geth-ancient block's txs and print (idx, sender, nonce)", runBlockSendersList},
	{"receipts-probe", "time reading N receipts from a compressed freezer table", runReceiptsProbe},
	{"dump-tx", "print a single tx from a geth ancient block body", runDumpTx},
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: n42-freezer <subcommand> [flags]\n\nsubcommands:\n")
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
