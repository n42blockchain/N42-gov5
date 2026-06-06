// n42-hist-from-freezer builds accthist + storhist RecSplit history segments
// directly from the N42 acctcs/storcs columnar changeset freezer — the N42-native
// counterpart to `ethexec history-build` (which reads an Erigon MDBX). Output is
// byte-identical in format (cscompact SegmentStore + RecSplit + delta-varint),
// so the full/archive tiers can ship historical state-at-height indexes without
// an Erigon source.
//
// Usage:
//
//	n42-hist-from-freezer --changesets D:/N42-eth1177/chain/freezer --out D:/n42-hist [--end N]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func openCS(dir, name string) (*freezer.FreezerTable, error) {
	t, err := freezer.NewFreezerTableReadOnly(dir, name, "c")
	if err != nil {
		return nil, err
	}
	t.ForceBatchSize(freezer.BatchSize)
	t.SetCompressed(true)
	return t, nil
}

func main() {
	csDir := flag.String("changesets", "", "dir with acctcs.* + storcs.* changeset freezer")
	out := flag.String("out", "", "output dir (writes chain/freezer/accthist.* + storhist.*)")
	end := flag.Uint64("end", 0, "end block (0 = freezer frozen height)")
	flag.Parse()
	if *csDir == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-hist-from-freezer --changesets <dir> --out <dir> [--end N]")
		os.Exit(1)
	}
	outFz := *out + "/chain/freezer"
	if err := os.MkdirAll(outFz, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	acctTbl, err := openCS(*csDir, "acctcs")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer acctTbl.Close()
	storTbl, err := openCS(*csDir, "storcs")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer storTbl.Close()

	endBlock := *end
	if endBlock == 0 {
		endBlock = uint64(acctTbl.Items()) // frozen count = highest block + 1
	}
	fmt.Printf("building accthist+storhist from %s → %s, end=%d\n", *csDir, outFz, endBlock)

	ctx := context.Background()

	// accthist: key = 20B account address, from acctcs.
	acctKeys := func(bn uint64) ([][]byte, error) {
		blob, err := acctTbl.Retrieve(bn)
		if err != nil || len(blob) == 0 {
			return nil, err
		}
		changes, err := ethel.DecodeAccountChanges(blob)
		if err != nil {
			return nil, err
		}
		keys := make([][]byte, len(changes))
		for i := range changes {
			a := changes[i].Address
			keys[i] = a[:]
		}
		return keys, nil
	}
	if err := cscompact.NewAccountHistoryBuilder(nil, outFz).BuildFromBlockKeys(ctx, 0, endBlock, acctKeys); err != nil {
		fmt.Fprintln(os.Stderr, "accthist:", err)
		os.Exit(1)
	}
	fmt.Println("accthist done")

	// storhist: key = 52B addr||slot composite, from storcs.
	storKeys := func(bn uint64) ([][]byte, error) {
		blob, err := storTbl.Retrieve(bn)
		if err != nil || len(blob) == 0 {
			return nil, err
		}
		changes, err := ethel.DecodeStorageChanges(blob)
		if err != nil {
			return nil, err
		}
		keys := make([][]byte, len(changes))
		for i := range changes {
			keys[i] = changes[i].CompositeKey
		}
		return keys, nil
	}
	if err := cscompact.NewStorageHistoryBuilder(nil, outFz).BuildFromBlockKeys(ctx, 0, endBlock, storKeys); err != nil {
		fmt.Fprintln(os.Stderr, "storhist:", err)
		os.Exit(1)
	}
	fmt.Println("storhist done")
}
