// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package packetd builds gov5 StreamPacket V2 packets for blocks of another
// client (the n42-rs node), so a fleet without a gov5 leader can serve
// phones. It re-executes each block with gov5's own execution through a
// streamverify.ReadLogRecorder over a state reader answered at the parent
// block (RPCStateReader), exactly as the leader rebuilds a diverged read log
// (internal/miner/mobile_packet.go readLog) and as the phone replays it
// (cmd/evmsdk ExecuteAndVerifyV2): block-start system calls, then each
// transaction. The read log therefore comes out in Go IntraBlockState order
// by construction.
package packetd

import (
	"fmt"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/streamverify"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Built is a packet and what its construction saw.
type Built struct {
	Packet    *evmsdk.StreamPacket
	Reads     int // read-log entries
	Bytecodes int
	GasUsed   uint64
}

// errReader is a reader that keeps the first read it could not answer
// (RPCStateReader, streamverify.RecordedStateReader).
type errReader interface{ Err() error }

// BuildPacket re-executes hdr's transactions over parent, the state at the
// block's parent, and returns the block's StreamPacket.
//
// engine nil means apos.NewFaker(), the phone's engine. hashFn answers
// BLOCKHASH (the leader answers it from its chain; the phone with zero);
// nil answers zero. BLOCKHASH answers are not part of the read log.
//
// The packet is refused (a PermanentError) when the re-execution fails, a
// read could not be answered, the gas used differs from the header's, or the
// receipts root it computes is not the header's: the phone checks exactly
// that root, and no packet is better than one every device rejects.
func BuildPacket(cfg *params.ChainConfig, engine consensus.Engine, hdr *block.Header, txs transaction.Transactions, parent state.StateReader, hashFn func(uint64) types.Hash) (*Built, error) {
	if cfg == nil || hdr == nil || hdr.Number == nil || parent == nil {
		return nil, fmt.Errorf("packetd: missing chain config, header, number or reader")
	}
	if cfg.IsDeferredExecution(hdr.Time) {
		// The header then carries the parent's execution (gas used, receipts
		// root); the phone compares this block's receipts with it and fails.
		return nil, permanent("packetd: block %d is under deferred execution; its header carries the parent's receipts root", hdr.Number.Uint64())
	}
	if engine == nil {
		engine = apos.NewFaker()
	}
	if hashFn == nil {
		hashFn = func(uint64) types.Hash { return types.Hash{} }
	}
	n := hdr.Number.Uint64()
	readErr := func() error {
		if er, ok := parent.(errReader); ok {
			return er.Err()
		}
		return nil
	}

	rec := streamverify.NewReadLogRecorder(parent, nil)
	ibs := state.New(rec)
	ibs.SetHeight(n)
	if err := internal.ProcessExecutionBlockStart(hdr.ParentBeaconRoot, cfg, ibs, hdr, engine); err != nil {
		if rerr := readErr(); rerr != nil {
			return nil, fmt.Errorf("packetd: block %d start: %w", n, rerr)
		}
		return nil, permanent("packetd: block %d start: %v", n, err)
	}
	coinbase := hdr.Coinbase
	gp := new(common.GasPool).AddGas(hdr.GasLimit)
	noop := state.NewNoopWriter()
	receipts := make([]*block.Receipt, 0, len(txs))
	var used uint64
	for i, tx := range txs {
		ibs.Prepare(tx.Hash(), types.Hash{}, i)
		receipt, _, err := internal.ApplyTransaction(cfg, hashFn, engine, &coinbase, gp, ibs, noop, hdr, tx, &used, vm.Config{})
		if err != nil {
			if rerr := readErr(); rerr != nil {
				// The node failed a read: transient, retry the block.
				return nil, fmt.Errorf("packetd: block %d tx %d: %w", n, i, rerr)
			}
			return nil, permanent("packetd: block %d tx %d: %v", n, i, err)
		}
		if receipt != nil {
			receipts = append(receipts, receipt)
		}
	}
	if rerr := readErr(); rerr != nil {
		return nil, fmt.Errorf("packetd: block %d: %w", n, rerr)
	}
	if err := ibs.Error(); err != nil {
		return nil, fmt.Errorf("packetd: block %d: state: %w", n, err)
	}
	if used != hdr.GasUsed {
		return nil, permanent("packetd: block %d re-execution used %d gas, the header says %d", n, used, hdr.GasUsed)
	}
	if root := internal.ReceiptsRootFor(cfg, n, receipts); root != hdr.ReceiptHash {
		return nil, permanent("packetd: block %d re-execution receipts root %v, the header says %v", n, root, hdr.ReceiptHash)
	}
	pkt, err := streamverify.BuildStreamPacket(block.CopyHeader(hdr), txs, rec)
	if err != nil {
		return nil, permanent("packetd: block %d: %v", n, err)
	}
	return &Built{Packet: pkt, Reads: len(rec.Log()), Bytecodes: len(pkt.Bytecodes), GasUsed: used}, nil
}
