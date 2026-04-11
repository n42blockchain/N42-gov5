// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 1 of the mobile parallel verification track: re-execute a block
// from a StreamPacket and compare the computed receipts root against the
// header. The orchestrator wires together:
//
//	StreamReplayStateReader   — cursor-based StateReader from packet read log
//	state.IntraBlockState     — real concrete state machine
//	internal.ApplyTransaction — same EVM the producer side ran
//	EthReceiptHash            — Ethereum-standard receipts root
//
// On success returns the computed receipts root, which must equal
// header.ReceiptHash. The caller is then expected to BLS-sign a
// VerificationReceipt over the canonical (block_hash, number, root) tuple.

package evmsdk

import (
	"errors"
	"fmt"

	common2 "github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// ErrReceiptRootMismatch is returned when the computed receipts root
// does not equal the value baked into the block header. This is the
// signal that re-execution disagreed with the producer.
var ErrReceiptRootMismatch = errors.New("verify_v2: computed receipts root does not match header")

// V2VerifyResult holds the outcome of a successful re-execution.
type V2VerifyResult struct {
	BlockHash            types.Hash
	BlockNumber          uint64
	TxCount              int
	UncachedBytecodes    int // newly inserted from this packet
	WitnessReadLogLen    int
	ComputedReceiptsRoot types.Hash
}

// ExecuteAndVerifyV2 re-executes the block in the packet and verifies
// the computed receipts root matches header.ReceiptHash.
//
// On success returns a V2VerifyResult; on any failure (decode error,
// EVM error, root mismatch) returns a wrapped error. The shared global
// code cache is mutated to insert the packet's uncached bytecodes; the
// caller is expected to use a long-lived cache so subsequent packets
// benefit from the additions.
func ExecuteAndVerifyV2(pkt *StreamPacket, codes *CodeCache, chainCfg *params.ChainConfig) (*V2VerifyResult, error) {
	if pkt == nil {
		return nil, errors.New("verify_v2: nil packet")
	}
	if codes == nil {
		return nil, errors.New("verify_v2: nil code cache")
	}
	if chainCfg == nil {
		chainCfg = params.MainnetChainConfig
	}

	// 1. Decode header from the embedded protobuf bytes.
	var header block.Header
	if err := header.Unmarshal(pkt.HeaderRLP); err != nil {
		return nil, fmt.Errorf("verify_v2: header unmarshal: %w", err)
	}
	if header.Number == nil {
		return nil, errors.New("verify_v2: header has nil block number")
	}
	blockNumber := header.Number.Uint64()

	// 2. Fail-fast on packet/header hash mismatch BEFORE we touch the
	// shared code cache or decode anything else. A bad packet must not
	// pollute the verifier's cache.
	if computed := header.Hash(); computed != pkt.BlockHash {
		return nil, fmt.Errorf("verify_v2: header hash %x != packet block hash %x", computed, pkt.BlockHash)
	}

	// 3. Decode transactions (protobuf — N42 internal format).
	txs := make(transaction.Transactions, 0, len(pkt.Transactions))
	for i, raw := range pkt.Transactions {
		t := &transaction.Transaction{}
		if err := t.Unmarshal(raw); err != nil {
			return nil, fmt.Errorf("verify_v2: tx %d unmarshal: %w", i, err)
		}
		txs = append(txs, t)
	}

	// 4. Insert packet bytecodes into the shared cache.
	uncached := 0
	for _, e := range pkt.Bytecodes {
		if _, hit := codes.Get(e.Hash); !hit {
			codes.Insert(e.Hash, e.Code)
			uncached++
		}
	}

	// 5. Decode read log + build cursor-based state reader.
	readLog, err := DecodeReadLog(pkt.ReadLogData)
	if err != nil {
		return nil, fmt.Errorf("verify_v2: read log decode: %w", err)
	}
	reader := NewStreamReplayStateReader(readLog, codes)
	ibs := state.New(reader)
	ibs.SetHeight(blockNumber)

	if chainCfg.DAOForkSupport && chainCfg.DAOForkBlock != nil &&
		chainCfg.DAOForkBlock.Cmp(header.Number.ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	// blockHash hook for BLOCKHASH opcode. The cursor-based reader
	// records BlockHash entries in the read log; future iteration can
	// thread them through here. For now we return zero — non-zero usage
	// of BLOCKHASH within a verified block will silently produce a
	// receipts-root mismatch, which is the right failure mode.
	blockHashFunc := func(uint64) types.Hash { return types.Hash{} }

	gasPool := new(common2.GasPool)
	gasPool.AddGas(header.GasLimit)
	usedGas := new(uint64)
	cfg := vm.Config{}
	engine := apos.NewFaker()
	noop := state.NewNoopWriter()

	receipts := make([]*block.Receipt, 0, len(txs))
	for i, tx := range txs {
		ibs.Prepare(tx.Hash(), pkt.BlockHash, i)
		receipt, _, txErr := internal.ApplyTransaction(
			chainCfg, blockHashFunc, engine, nil, gasPool,
			ibs, noop, &header, tx, usedGas, cfg,
		)
		if txErr != nil {
			return nil, fmt.Errorf("verify_v2: tx %d: %w", i, txErr)
		}
		if rerr := reader.Err(); rerr != nil {
			return nil, fmt.Errorf("verify_v2: tx %d state reader: %w", i, rerr)
		}
		if receipt != nil {
			receipts = append(receipts, receipt)
		}
	}

	// 6. Compute receipts root and compare against header.
	computedRoot := EthReceiptHash(receipts)
	if computedRoot != header.ReceiptHash {
		return nil, fmt.Errorf("%w: header=%x computed=%x", ErrReceiptRootMismatch, header.ReceiptHash, computedRoot)
	}

	return &V2VerifyResult{
		BlockHash:            pkt.BlockHash,
		BlockNumber:          blockNumber,
		TxCount:              len(txs),
		UncachedBytecodes:    uncached,
		WitnessReadLogLen:    len(readLog),
		ComputedReceiptsRoot: computedRoot,
	}, nil
}
