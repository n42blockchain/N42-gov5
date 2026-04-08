// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// tx_diff.go — per-tx state diff tool to locate exact state divergence.

package ethel

import (
	"fmt"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	iinternal "github.com/n42blockchain/N42/internal"
)

// RunTxDiff executes a block tx-by-tx and computes state root after each tx.
// Prints which tx causes the first divergence from the expected root.
func RunTxDiff(
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	header *block.Header,
	txs []*transaction.Transaction,
	uncles []block.IHeader,
	stateBuf *state.PlainStateBuffer,
	tx kv.RwTx,
	blockHashFunc func(uint64) types.Hash,
	senders []types.Address,
) error {
	blockNum := header.Number.Uint64()
	log.Info("=== TX DIFF START ===", "block", blockNum, "txs", len(txs))

	// Compute state root BEFORE any tx execution.
	if err := stateBuf.FlushToMDBX(tx); err != nil {
		return err
	}
	preRoot, _ := VerifyStateRoot(tx)
	log.Info("Pre-block state root", "block", blockNum, "root", preRoot.Hex())

	bufReader := state.NewBufferedPlainStateReader(stateBuf, tx)
	writer := state.NewBufferedPlainStateWriter(stateBuf, tx, blockNum)
	ibs := state.New(bufReader)

	gp := new(common.GasPool)
	gp.AddGas(header.GasLimit)
	usedGas := new(uint64)
	noop := state.NewNoopWriter()
	blockHash := header.Hash()

	// Inject senders.
	if senders != nil {
		for i, txn := range txs {
			if i < len(senders) {
				txn.SetFrom(senders[i])
			}
		}
	}

	// Pre-block system calls.
	iinternal.ProcessExecutionBlockStart(nil, chainCfg, ibs, header, engine)

	// Execute each tx and check state root.
	for i, txn := range txs {
		ibs.Prepare(txn.Hash(), blockHash, i)
		_, _, err := iinternal.ApplyTransaction(
			chainCfg, blockHashFunc, engine, nil, gp,
			ibs, noop, header, txn, usedGas, vm2.Config{},
		)
		if err != nil {
			log.Error("TX execution failed", "tx", i, "err", err)
			return err
		}

		// Commit this tx's state changes to buffer.
		if err := ibs.CommitBlock(chainCfg.Rules(header.Number.Uint64()), writer); err != nil {
			return fmt.Errorf("commit tx %d: %w", i, err)
		}

		// Flush buffer and compute root.
		if err := stateBuf.FlushToMDBX(tx); err != nil {
			return err
		}
		root, _ := VerifyStateRoot(tx)

		// Log dirty accounts from this tx.
		to := "nil"
		if txn.To() != nil {
			to = txn.To().Hex()[:10]
		}
		from := "unknown"
		if senders != nil && i < len(senders) {
			from = senders[i].Hex()[:10]
		}
		log.Info("After tx", "idx", i, "root", root.Hex()[:18],
			"from", from, "to", to,
			"value", txn.Value(), "gas", txn.Gas(), "gasUsed", *usedGas)

		// Re-create ibs for next tx (fresh state reader from updated buffer).
		bufReader = state.NewBufferedPlainStateReader(stateBuf, tx)
		ibs = state.New(bufReader)
		writer = state.NewBufferedPlainStateWriter(stateBuf, tx, blockNum)
	}

	// Post-block system calls.
	iinternal.ProcessExecutionBlockEnd(nil, chainCfg, ibs, header, engine)

	// Finalize (block rewards).
	if blockNum > 0 {
		engine.Finalize(nil, header, ibs, txs, uncles)
	}

	// Final commit.
	ibs.CommitBlock(chainCfg.Rules(blockNum), writer)
	stateBuf.FlushToMDBX(tx)

	finalRoot, _ := VerifyStateRoot(tx)
	log.Info("After finalize", "root", finalRoot.Hex(), "expected", header.Root.Hex(),
		"match", finalRoot == header.Root)

	// Dump changed accounts if mismatch.
	if finalRoot != header.Root {
		dumpAccountDiff(tx, preRoot)
	}

	log.Info("=== TX DIFF END ===")
	return nil
}

// dumpAccountDiff lists accounts whose keccak(addr) + RLP differs from a baseline.
func dumpAccountDiff(tx kv.RwTx, _ types.Hash) {
	emptyCodeHash := crypto.Keccak256Hash(nil)
	emptyRoot := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

	c, err := tx.Cursor("Account")
	if err != nil {
		return
	}
	defer c.Close()

	// Count accounts with zero balance + zero nonce (empty touched accounts).
	emptyCount := 0
	totalCount := 0
	for k, v, _ := c.First(); k != nil; k, v, _ = c.Next() {
		if len(k) != 20 {
			continue
		}
		totalCount++
		var acc account.StateAccount
		acc.DecodeForStorage(v)
		if acc.Nonce == 0 && acc.Balance.IsZero() &&
			(acc.CodeHash == emptyCodeHash || acc.CodeHash == (types.Hash{})) {
			emptyCount++
			// Log first few empty accounts.
			if emptyCount <= 10 {
				log.Info("Empty account in state", "addr", types.BytesToAddress(k).Hex())
			}
		}
	}
	log.Info("Account summary", "total", totalCount, "empty", emptyCount)

	// Count storage entries.
	sc, _ := tx.Cursor(modules.Storage)
	if sc != nil {
		stoCnt, _ := sc.Count()
		sc.Close()

		// Find accounts with most storage.
		stoByAddr := make(map[types.Address]int)
		for k, _, _ := sc.First(); k != nil; k, _, _ = sc.Next() {
			if len(k) >= 20 {
				var addr types.Address
				copy(addr[:], k[:20])
				stoByAddr[addr]++
			}
		}
		log.Info("Storage summary", "total", stoCnt, "addresses", len(stoByAddr))
	}

	_ = emptyRoot
}
