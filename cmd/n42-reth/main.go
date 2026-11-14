// n42-reth is a multi-subcommand tool that replaces the former one-off probe
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
	{"acct-dump", "print raw PlainAccountState bytes for an address", runAcctDump},
	{"acct-history", "walk AccountChangeSets for an address, print (block, prevValue)", runAcctHistory},
	{"acct-history-bitmap", "dump AccountsHistory block list (roaring bitmap) for an address", runAcctHistoryBitmap},
	{"addr-block", "count rows / sample slots for (block, addr) in StorageChangeSets", runAddrBlock},
	{"codehash-rewrite", "rewrite PlainAccountState with dict-indexed codeHash, measure size delta", runCodehashRewrite},
	{"codehash-uniq", "unique codeHash count, skew and dictionary savings analysis", runCodehashUniq},
	{"cs-cumulative", "rolling cumulative unique active addresses / slots over N days", runCsCumulative},
	{"cs-dict-reuse", "address / codeHash reuse rate over rolling block windows", runCsDictReuse},
	{"cs-dump-block", "dump all AccountChangeSet entries for one block", runCsDumpBlock},
	{"cs-head", "last block number in Account/StorageChangeSets", runCsHead},
	{"cs-range", "earliest and latest block in Account/StorageChangeSets", runCsRange},
	{"cs-timeseries", "per-day changeset activity over a recent block window", runCsTimeseries},
	{"history-stats", "modification-count distribution of Accounts/StoragesHistory", runHistoryStats},
	{"probe", "dump first N entries of a MDBX table to inspect layout", runProbe},
	{"slot-history", "scan StorageChangeSets for one (addr, slot) over a block range", runSlotHistory},
	{"snapshot-export", "export PlainAccount/StorageState into RecSplit-indexed snapshot files", runSnapshotExport},
	{"state-at-snapshot", "reconstruct account state at the snapshot block from changesets", runStateAtSnapshot},
	{"tables-list", "list every MDBX table with entry counts", runTablesList},
	{"value-stats", "value-length histograms and layout size estimates", runValueStats},
	{"scan-cs", "changeset update-count distribution (hot data threshold)", runScanCs},
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: n42-reth <subcommand> [flags]\n\nsubcommands:\n")
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
