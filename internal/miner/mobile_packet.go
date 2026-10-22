// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"errors"
	"sync/atomic"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal/streamverify"
	log "github.com/n42blockchain/N42/lib/log/v3"
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
}

func newMobilePacketJob(txs transaction.Transactions, rec *streamverify.ReadLogRecorder) *mobilePacketJob {
	if rec == nil {
		return nil
	}
	return &mobilePacketJob{txs: txs, rec: rec}
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
	pkt, err := streamverify.BuildStreamPacket(block.CopyHeader(hdr), j.txs, j.rec)
	if err != nil {
		return nil, 0, err
	}
	return pkt, num.Uint64(), nil
}

// launch builds the packet off the caller's goroutine (construction walks the
// whole read log, ~35-75 ms on a full block) and hands it to the sink. At most
// once per job; best effort, packet production never fails a block.
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
