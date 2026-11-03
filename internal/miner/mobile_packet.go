// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/streamverify"
	"github.com/n42blockchain/N42/internal/vm"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

var errNotNativeHeader = errors.New("mobileverify: sealed block header is not a native header")

// mobilePacketJob is what the leader keeps of a block's execution until the
// block is sealed: the transactions and the read-log recorder. The packet itself
// is built from the SEALED header, never from the header the block was
// assembled with.
//
// The BLS seal is written in place into the reserved trailing bytes of
// header.Extra after assembly, and Header.Hash() covers all of Extra. A packet
// built at assembly time therefore carried (and was keyed by) a hash no sealed
// block has, so /mobileverify/packet/<block hash> never found it and a verifier
// comparing the packet's header with the header it knows saw a different one.
type mobilePacketJob struct {
	txs     transaction.Transactions
	rec     *streamverify.ReadLogRecorder
	started atomic.Bool

	// What rebuilding a diverged read log needs (readLog): the build's chain
	// rules, engine and fee recipient, a header source for BLOCKHASH, and the
	// gas the build's execution of txs used, which the rebuild must match.
	config    *params.ChainConfig
	engine    consensus.Engine
	coinbase  types.Address
	getHeader func(hash types.Hash, number uint64) *block.Header
	gasUsed   uint64
}

func newMobilePacketJob(txs transaction.Transactions, rec *streamverify.ReadLogRecorder) *mobilePacketJob {
	if rec == nil {
		return nil
	}
	return &mobilePacketJob{txs: txs, rec: rec}
}

// mobilePacketJobFor is the packet job for a finished build: env's included
// transactions and receipts, rec the recorder the build read through.
func (w *worker) mobilePacketJobFor(env *environment, rec *streamverify.ReadLogRecorder) *mobilePacketJob {
	j := newMobilePacketJob(env.txs, rec)
	if j == nil {
		return nil
	}
	j.config, j.engine, j.coinbase = w.chainConfig, w.engine, env.coinbase
	if n := len(env.receipts); n > 0 && env.receipts[n-1] != nil {
		j.gasUsed = env.receipts[n-1].CumulativeGasUsed
	}
	chain := w.chain
	j.getHeader = func(hash types.Hash, number uint64) *block.Header {
		h, _ := chain.GetHeader(hash, uint256.NewInt(number)).(*block.Header)
		return h
	}
	return j
}

// readLog returns the recorder whose log goes into the packet. A build that
// threw reads away (rec.Diverged) does not have a log verifiers can consume
// by position; it is rebuilt by re-executing the block's transactions, block
// start first, the way a verifier does, over the values the build recorded
// from the parent state. Nothing here touches the database: the build's
// read transaction is long closed when the sealed block arrives. A read the
// build never recorded, or a re-execution that disagrees with the build,
// fails the packet: no packet is better than one every device rejects.
func (j *mobilePacketJob) readLog(hdr *block.Header) (*streamverify.ReadLogRecorder, error) {
	if !j.rec.Diverged() {
		return j.rec, nil
	}
	if j.config == nil || j.engine == nil {
		return nil, errors.New("mobileverify: read log diverged and the job cannot rebuild it")
	}
	start := time.Now()
	rec, recorded := j.rec.ReplayRecorder()
	ibs := state.New(rec)
	ibs.SetHeight(hdr.Number.Uint64())
	if err := internal.ProcessExecutionBlockStart(hdr.ParentBeaconRoot, j.config, ibs, hdr, j.engine); err != nil {
		return nil, fmt.Errorf("mobileverify: read log rebuild: block start: %w", err)
	}
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	if j.getHeader != nil {
		hashFn = internal.GetHashFn(hdr, j.getHeader)
	}
	coinbase := j.coinbase
	gp := new(common.GasPool).AddGas(hdr.GasLimit)
	noop := state.NewNoopWriter()
	var used uint64
	for i, tx := range j.txs {
		ibs.Prepare(tx.Hash(), types.Hash{}, i)
		if _, _, err := internal.ApplyTransaction(j.config, hashFn, j.engine, &coinbase, gp, ibs, noop, hdr, tx, &used, vm.Config{}); err != nil {
			if rerr := recorded.Err(); rerr != nil {
				err = rerr
			}
			return nil, fmt.Errorf("mobileverify: read log rebuild: tx %d: %w", i, err)
		}
	}
	if err := recorded.Err(); err != nil {
		return nil, fmt.Errorf("mobileverify: read log rebuild: %w", err)
	}
	if used != j.gasUsed {
		return nil, fmt.Errorf("mobileverify: read log rebuild used %d gas, the build used %d", used, j.gasUsed)
	}
	log.Debug("mobileverify: read log rebuilt", "txs", len(j.txs), "reads", len(rec.Log()), "buildReads", len(j.rec.Log()), "elapsed", time.Since(start))
	return rec, nil
}

// buildSealed builds the packet for a sealed block. The tx slice and the
// recorder are immutable once the build is done, and the header is copied so
// the caller's block is never touched.
func (j *mobilePacketJob) buildSealed(sealed block.IBlock) (*evmsdk.StreamPacket, uint64, error) {
	hdr, ok := sealed.Header().(*block.Header)
	if !ok {
		return nil, 0, errNotNativeHeader
	}
	num, err := requireBlockNumber(sealed, "sealed block number unavailable")
	if err != nil {
		return nil, 0, err
	}
	rec, err := j.readLog(block.CopyHeader(hdr))
	if err != nil {
		return nil, 0, err
	}
	pkt, err := streamverify.BuildStreamPacket(block.CopyHeader(hdr), j.txs, rec)
	if err != nil {
		return nil, 0, err
	}
	return pkt, num.Uint64(), nil
}

// launch builds the packet off the caller's goroutine (construction walks the
// whole read log, ~35-75 ms on a full block, plus a re-execution when the log
// must be rebuilt) and hands it to the sink. At most once per job; best
// effort, packet production never fails a block.
func (j *mobilePacketJob) launch(sealed block.IBlock, sink func(*evmsdk.StreamPacket, uint64)) {
	if j == nil || sink == nil || !j.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		pkt, num, err := j.buildSealed(sealed)
		if err != nil {
			log.Warn("mobileverify: packet build failed", "err", err)
			return
		}
		sink(pkt, num)
	}()
}
