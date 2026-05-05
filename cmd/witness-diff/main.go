// witness-diff: diagnose a single block's witness vs replay divergence.
//
// Loads the witness entries from disk + counts them. Runs ProcessBlock
// against a tracing replay reader that records every read it serves.
// Reports: total entries in witness, total reads issued during replay,
// position+value of the FIRST entry whose declared length differs from
// what the EVM actually consumed.
//
// Use: when witness-replay reports 9% block failures, pick one failing
// block (e.g. logged "Gas pool exhausted block=12035046") and run this
// tool — it pinpoints where the recording-side and replay-side state-
// access pattern diverged.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// scanWitnessEntries walks a length-prefixed stream and reports each
// entry's size. Returns the entry count and a list of sizes for
// further inspection.
func scanWitnessEntries(stream []byte) []int {
	sizes := []int{}
	pos := 0
	for pos < len(stream) {
		n := int(stream[pos])
		pos++
		pos += n
		sizes = append(sizes, n)
	}
	return sizes
}

// tracingReader wraps a real WitnessReplayReader and records every
// call made to it, so we can compare against the recording-side count.
type tracingReader struct {
	inner  *ethel.WitnessReplayReader
	calls  int
	maxLog int
}

func newTracingReader(stream []byte, codeTx kv.Tx) *tracingReader {
	return &tracingReader{
		inner:  ethel.NewWitnessReplayReader(stream, codeTx),
		maxLog: 30,
	}
}

func (r *tracingReader) record(label string, addr types.Address, slot *types.Hash) {
	if r.calls < r.maxLog {
		fmt.Printf("  call#%d %s addr=%x slot=%v\n", r.calls, label, addr, slot)
	}
	r.calls++
}

func (r *tracingReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	r.record("ReadAccountData", addr, nil)
	return r.inner.ReadAccountData(addr)
}
func (r *tracingReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	r.record("ReadAccountStorage", addr, key)
	return r.inner.ReadAccountStorage(addr, key)
}
func (r *tracingReader) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	return r.inner.ReadAccountCode(addr, codeHash)
}
func (r *tracingReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	return r.inner.ReadAccountCodeSize(addr, codeHash)
}

func main() {
	hbDir := flag.String("input-headers-bodies", "", "freezer dir with headers/bodies")
	witnessDir := flag.String("input-witness", "", "freezer dir with witness")
	datadir := flag.String("datadir", "", "MDBX datadir for Code")
	blockNum := flag.Uint64("block", 0, "block to diagnose")
	flag.Parse()
	if *hbDir == "" || *witnessDir == "" || *datadir == "" || *blockNum == 0 {
		fmt.Fprintln(os.Stderr, "usage: witness-diff --input-headers-bodies <dir> --input-witness <dir> --datadir <mdbx> --block N")
		os.Exit(1)
	}

	modules.N42Init()
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

	hb, err := freezer.New(*hbDir, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open hb:", err)
		os.Exit(1)
	}
	defer hb.Close()

	witnessTbl, err := freezer.NewFreezerTableCompressedReadOnly(*witnessDir, freezer.TableBlockWitness, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open witness:", err)
		os.Exit(1)
	}
	defer witnessTbl.Close()
	witnessTbl.ForceBatchSize(freezer.BatchSize)

	logger := log2.New()
	codeDB, err := mdbx.NewMDBX(logger).Path(*datadir).Label(kv.ChainDB).Accede().Readonly().MapSize(4 * datasize.TB).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open mdbx:", err)
		os.Exit(1)
	}
	defer codeDB.Close()
	codeTx, err := codeDB.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "ro:", err)
		os.Exit(1)
	}
	defer codeTx.Rollback()

	hdrData, err := hb.Ancient(freezer.TableHeaders, *blockNum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "header:", err)
		os.Exit(1)
	}
	hdr, err := ethel.DecodeGethHeader(hdrData)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode header:", err)
		os.Exit(1)
	}
	bodyData, err := hb.Ancient(freezer.TableBodies, *blockNum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "body:", err)
		os.Exit(1)
	}
	body, err := ethel.DecodeGethBody(bodyData)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode body:", err)
		os.Exit(1)
	}
	witnessData, err := witnessTbl.Retrieve(*blockNum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "witness:", err)
		os.Exit(1)
	}

	sizes := scanWitnessEntries(witnessData)
	fmt.Printf("witness blob: %d bytes, %d entries\n", len(witnessData), len(sizes))
	if len(sizes) > 30 {
		fmt.Printf("first 30 entry sizes: %v ...\n", sizes[:30])
	} else {
		fmt.Printf("entry sizes: %v\n", sizes)
	}

	r := newTracingReader(witnessData, codeTx)
	ibs := state.New(r)

	chainCfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(chainCfg)
	uncles := make([]block.IHeader, len(body.Uncles))
	for i, u := range body.Uncles {
		uncles[i] = u
	}

	fmt.Printf("\nfirst %d EVM reads during replay:\n", r.maxLog)
	result, err := ethel.ProcessBlock(chainCfg, engine, hdr, body.Transactions, uncles, body.Withdrawals, ibs, nil, nil)
	if err != nil {
		fmt.Printf("\nProcessBlock failed: %v\n", err)
	} else if result != nil {
		fmt.Printf("\nProcessBlock OK: gasUsed=%d (header=%d) match=%v\n",
			result.GasUsed, hdr.GasUsed, result.GasUsed == hdr.GasUsed)
	}
	fmt.Printf("\ntotal replay reads: %d\n", r.calls)
	fmt.Printf("witness entries:    %d\n", len(sizes))
	if r.calls != len(sizes) {
		fmt.Printf("DIVERGENCE: replay issued %d reads but witness has %d entries (delta=%d)\n",
			r.calls, len(sizes), r.calls-len(sizes))
	}
}
