// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// TestWitnessBlockReplay re-executes a single block by replaying its
// witness stream and verifies the resulting gas, transaction root and
// receipt root all match the geth ancient header. Default block is
// 7,000,000; override with WITNESS_BLOCK=N. Paths can be overridden with
// N42_WITNESS_DIR / N42_MDBX_DIR / N42_GETH_ANCIENT.
func TestWitnessBlockReplay(t *testing.T) {
	var targetBlock uint64 = 7000000
	if v := os.Getenv("WITNESS_BLOCK"); v != "" {
		var n uint64
		_, _ = fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			targetBlock = n
		}
	}

	witnessDir := envOrDefault("N42_WITNESS_DIR", `d:\n42-eth037\chain\freezer`)
	mdbxDir := envOrDefault("N42_MDBX_DIR", `d:\n42-eth037`)
	gethAncient := envOrDefault("N42_GETH_ANCIENT", `d:\geth\geth\chaindata\ancient\chain`)

	for _, p := range []string{witnessDir, mdbxDir, gethAncient} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("not found: %s", p)
		}
	}

	// Geth freezer for header + body.
	inputF, err := freezer.New(gethAncient, 0)
	if err != nil {
		t.Fatalf("open geth freezer: %v", err)
	}
	defer inputF.Close()

	// Witness freezer (read-only) at eth037.
	witnessTbl, err := freezer.NewFreezerTableCompressedReadOnly(witnessDir, freezer.TableBlockWitness, "c")
	if err != nil {
		t.Fatalf("open witness table: %v", err)
	}
	defer witnessTbl.Close()
	witnessTbl.ForceBatchSize(freezer.BatchSize)

	if targetBlock >= witnessTbl.Items() {
		t.Fatalf("witness has only %d items, need %d", witnessTbl.Items(), targetBlock+1)
	}

	witnessData, err := witnessTbl.Retrieve(targetBlock)
	if err != nil {
		t.Fatalf("retrieve witness %d: %v", targetBlock, err)
	}

	// MDBX (read-only) for Code table.
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(mdbxDir).
		Label(kv.ChainDB).
		Readonly().
		Open(context.Background())
	if err != nil {
		t.Fatalf("open mdbx: %v", err)
	}
	defer db.Close()

	codeTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("begin ro: %v", err)
	}
	defer codeTx.Rollback()

	// Read header + body from geth.
	headerData, err := inputF.Ancient(freezer.TableHeaders, targetBlock)
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	header, err := DecodeGethHeader(headerData)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}

	bodyData, err := inputF.Ancient(freezer.TableBodies, targetBlock)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body, err := DecodeGethBody(bodyData)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got := hash.DeriveShaErigon(transaction.EthTransactions(body.Transactions)); got != header.TxHash {
		t.Errorf("transaction root MISMATCH: got=%s want=%s", got.Hex(), header.TxHash.Hex())
	}

	// Replay using witness stream.
	reader := &witnessReplayReader{stream: witnessData, codeTx: codeTx}
	ibs := state.New(reader)

	uncles := make([]block.IHeader, len(body.Uncles))
	for i, u := range body.Uncles {
		uncles[i] = u
	}

	// BLOCKHASH opcode reads the previous 256 block hashes from geth ancient.
	hashCache := make(map[uint64]types.Hash)
	blockHashFunc := func(n uint64) types.Hash {
		refNum := header.Number.Uint64()
		if n >= refNum {
			return types.Hash{}
		}
		if h, ok := hashCache[n]; ok {
			return h
		}
		hd, err := inputF.Ancient(freezer.TableHeaders, n)
		if err != nil {
			return types.Hash{}
		}
		hh, err := DecodeGethHeader(hd)
		if err != nil {
			return types.Hash{}
		}
		hashCache[n] = hh.Hash()
		return hashCache[n]
	}

	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	result, err := ProcessBlock(chainCfg, engine, header, body.Transactions, uncles, body.Withdrawals, ibs, blockHashFunc, nil)
	if err != nil {
		t.Fatalf("process block: %v", err)
	}

	if result.GasUsed != header.GasUsed {
		t.Errorf("gas mismatch: got %d, want %d", result.GasUsed, header.GasUsed)
	}

	if got := EthReceiptHash(result.Receipts); got != header.ReceiptHash {
		t.Errorf("RECEIPT HASH MISMATCH:\n  got  %s\n  want %s",
			got.Hex(), header.ReceiptHash.Hex())
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
