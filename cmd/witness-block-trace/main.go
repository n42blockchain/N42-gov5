// witness-block-trace: replay one block via the witness path and dump
// per-tx CumulativeGasUsed alongside canonical receipts so we can see
// which tx (and how much) the witness-replay path drifted from
// mainnet.
//
// Inputs:
//
//	--hb-dir       headerc/bodyc or geth headers+bodies freezer dir
//	--witness-dir  freezer dir holding the block_witness compact table
//	--receipts-dir freezer dir holding receipts (for canonical compare)
//	--datadir      MDBX datadir with the Code table
//	--senders-dir  optional pre-computed senders freezer
//	--block        block to trace
//
// On a 40-gas-style mismatch this emits the offending tx's hash, type,
// gasLimit, calldata size, and per-tx delta — enough to identify
// whether the issue is intrinsic gas, access-list pre-charge, refund
// cap, or an opcode-level drift.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/c2h5oh/datasize"

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

func main() {
	hbDir := flag.String("hb-dir", "", "headers+bodies freezer dir (headerc/bodyc or geth)")
	witnessDir := flag.String("witness-dir", "", "block_witness freezer dir (defaults to hb-dir)")
	receiptsDir := flag.String("receipts-dir", "", "canonical receipts freezer dir (optional, for compare)")
	datadir := flag.String("datadir", "", "MDBX datadir with Code table (optional when --codes-freezer is set)")
	codesDir := flag.String("codes-freezer", "", "Optional codes.cidx + codes.NNNN.cdat dir (auto-detects <hb-dir>/codes.cidx)")
	sendersDir := flag.String("senders-dir", "", "pre-computed senders freezer (optional)")
	blockNum := flag.Uint64("block", 0, "block to trace")
	flag.Parse()
	if *hbDir == "" || *blockNum == 0 {
		fmt.Fprintln(os.Stderr, "usage: --hb-dir D --block N [--datadir D] [--codes-freezer D] [--witness-dir D] [--receipts-dir D] [--senders-dir D]")
		os.Exit(1)
	}
	if *witnessDir == "" {
		*witnessDir = *hbDir
	}
	// Auto-detect codes.cidx in hb-dir.
	if *codesDir == "" {
		if _, err := os.Stat(filepath.Join(*hbDir, "codes.cidx")); err == nil {
			*codesDir = *hbDir
		}
	}
	if *datadir == "" && *codesDir == "" {
		fmt.Fprintln(os.Stderr, "ERROR: provide either --datadir (MDBX with Code table) or --codes-freezer (codes.cidx dir)")
		os.Exit(1)
	}

	modules.N42Init()
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

	// 1. Open header+body source — auto-detect headerc/bodyc vs geth by
	// presence of headerc.cidx; the picker logic mirrors
	// openHeadersBodiesSource in witness_replay_source.go.
	hdr, body, err := readHeaderBody(*hbDir, *blockNum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read hb:", err)
		os.Exit(1)
	}
	fmt.Printf("block %d: hash=%s coinbase=%s gasUsed=%d gasLimit=%d txs=%d\n",
		*blockNum, hdr.Hash().Hex(), hdr.Coinbase.Hex(), hdr.GasUsed, hdr.GasLimit, len(body.Transactions))

	// 2. Open witness table.
	witnessTbl, err := freezer.NewFreezerTableCompressedReadOnly(*witnessDir, freezer.TableBlockWitness, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open witness:", err)
		os.Exit(1)
	}
	defer witnessTbl.Close()
	witnessTbl.ForceBatchSize(freezer.BatchSize)
	witnessData, err := witnessTbl.Retrieve(*blockNum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "retrieve witness:", err)
		os.Exit(1)
	}
	fmt.Printf("witness: %d bytes\n", len(witnessData))

	// 3. Open Code MDBX read-only (optional when --codes-freezer is set).
	logger := log2.New()
	var codeTx kv.Tx
	if *datadir != "" {
		if _, statErr := os.Stat(filepath.Join(*datadir, "mdbx.dat")); statErr == nil {
			codeDB, err := mdbx.NewMDBX(logger).Path(*datadir).Label(kv.ChainDB).Accede().Readonly().MapSize(4 * datasize.TB).Open(context.Background())
			if err != nil {
				fmt.Fprintln(os.Stderr, "open mdbx:", err)
				os.Exit(1)
			}
			defer codeDB.Close()
			codeTx, err = codeDB.BeginRo(context.Background())
			if err != nil {
				fmt.Fprintln(os.Stderr, "ro tx:", err)
				os.Exit(1)
			}
			defer codeTx.Rollback()
		}
	}
	// 3b. Open codes-freezer (optional).
	var codesReader *ethel.CodesFreezerReader
	if *codesDir != "" {
		var err error
		codesReader, err = ethel.NewCodesFreezerReader(*codesDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "open codes-freezer:", err)
			os.Exit(1)
		}
		defer codesReader.Close()
		fmt.Printf("codes-freezer: %d entries\n", codesReader.Items())
	}

	// 4. Senders (optional).
	var senders []types.Address
	if *sendersDir != "" {
		sTbl, err := freezer.NewFreezerTableCompressedReadOnly(*sendersDir, freezer.TableSenders, "c")
		if err != nil {
			fmt.Fprintln(os.Stderr, "open senders:", err)
			os.Exit(1)
		}
		defer sTbl.Close()
		sTbl.ForceBatchSize(freezer.BatchSize)
		raw, err := sTbl.Retrieve(*blockNum)
		if err == nil {
			for i := 0; i+20 <= len(raw); i += 20 {
				var a types.Address
				copy(a[:], raw[i:i+20])
				senders = append(senders, a)
			}
		}
		fmt.Printf("senders: %d entries\n", len(senders))
	}

	// 5. Replay through witness.
	r := ethel.NewWitnessReplayReader(witnessData, codeTx)
	if codesReader != nil {
		r.SetCodesFreezer(codesReader)
	}
	ibs := state.New(r)
	chainCfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(chainCfg)
	uncles := make([]block.IHeader, len(body.Uncles))
	for i, u := range body.Uncles {
		uncles[i] = u
	}
	result, err := ethel.ProcessBlock(chainCfg, engine, hdr, body.Transactions, uncles, body.Withdrawals, ibs, nil, senders, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ProcessBlock: %v\n", err)
		os.Exit(1)
	}

	// 6. Print our per-tx gas + canonical compare if available.
	fmt.Printf("\nN42 gasUsed: %d (header want %d)  diff=%+d\n",
		result.GasUsed, hdr.GasUsed, int64(hdr.GasUsed)-int64(result.GasUsed))

	var gethReceipts block.Receipts
	if *receiptsDir != "" {
		rcptTbl, err := freezer.NewFreezerTableCompressedReadOnly(*receiptsDir, freezer.TableReceipts, "c")
		if err == nil {
			defer rcptTbl.Close()
			rcptTbl.ForceBatchSize(freezer.BatchSize)
			raw, err := rcptTbl.Retrieve(*blockNum)
			if err == nil {
				gethReceipts, _ = ethel.DecodeGethReceipts(raw)
			}
		}
	}

	fmt.Printf("\n%-4s %-8s %-12s %-12s %-8s %s\n", "idx", "type", "n42_cum", "geth_cum", "diff", "txHash / to")
	var prevN42, prevGeth uint64
	for i, rcpt := range result.Receipts {
		n42Cum := rcpt.CumulativeGasUsed
		n42Tx := n42Cum - prevN42
		var gethTx, gethCum uint64
		var diff int64
		if i < len(gethReceipts) {
			gethCum = gethReceipts[i].CumulativeGasUsed
			gethTx = gethCum - prevGeth
			diff = int64(gethTx) - int64(n42Tx)
			prevGeth = gethCum
		}
		prevN42 = n42Cum
		toStr := "create"
		if i < len(body.Transactions) && body.Transactions[i].To() != nil {
			toStr = body.Transactions[i].To().Hex()
		}
		hashStr := ""
		if i < len(body.Transactions) {
			h := body.Transactions[i].Hash()
			hashStr = h.Hex()[:18] + "…"
		}
		marker := ""
		if diff != 0 {
			marker = " ★"
		}
		fmt.Printf("%-4d %-8d %-12d %-12d %+8d %s %s%s\n",
			i, rcpt.Type, n42Tx, gethTx, diff, hashStr, toStr, marker)
	}
}

func readHeaderBody(dir string, n uint64) (*block.Header, *ethel.GethBodyResult, error) {
	if _, err := os.Stat(dir + "/headerc.cidx"); err == nil {
		hr, err := ethel.OpenHeaderCompact(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("open headerc: %w", err)
		}
		defer hr.Close()
		br, err := ethel.OpenBodyCompact(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("open bodyc: %w", err)
		}
		defer br.Close()
		hdr, err := hr.ReadHeader(n)
		if err != nil {
			return nil, nil, fmt.Errorf("read headerc header: %w", err)
		}
		db, err := br.ReadBody(n)
		if err != nil {
			return nil, nil, fmt.Errorf("read bodyc body: %w", err)
		}
		// Decode uncle headers from raw RLP (uncles are nested in body
		// RLP, not snappy-wrapped — match witness_replay_source.body).
		var uncles []*block.Header
		if len(db.UncleRLP) > 0 {
			uncles = make([]*block.Header, len(db.UncleRLP))
			for i, raw := range db.UncleRLP {
				h, err := ethel.DecodeUncleHeader(raw)
				if err != nil {
					return nil, nil, fmt.Errorf("uncle %d: %w", i, err)
				}
				uncles[i] = h
			}
		}
		return hdr, &ethel.GethBodyResult{
			Transactions: db.Txs,
			Uncles:       uncles,
			Withdrawals:  db.Withdrawals,
		}, nil
	}
	// Fall through: geth ancient.
	f, err := freezer.NewReadOnly(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("open geth freezer: %w", err)
	}
	defer f.Close()
	hData, err := f.Ancient(freezer.TableHeaders, n)
	if err != nil {
		return nil, nil, fmt.Errorf("geth header: %w", err)
	}
	hdr, err := ethel.DecodeGethHeader(hData)
	if err != nil {
		return nil, nil, fmt.Errorf("decode geth header: %w", err)
	}
	bData, err := f.Ancient(freezer.TableBodies, n)
	if err != nil {
		return nil, nil, fmt.Errorf("geth body: %w", err)
	}
	body, err := ethel.DecodeGethBody(bData)
	if err != nil {
		return nil, nil, fmt.Errorf("decode geth body: %w", err)
	}
	return hdr, body, nil
}
