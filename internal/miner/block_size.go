// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Block-size budgeting for the block-building path.
//
// A sealed block must be able to travel to every validator. Both delivery paths
// carry the block as ETH-standard RLP and check the UNCOMPRESSED byte length
// against a hard p2p bound before snappy compression:
//
//	gossip      BlockChain.SealedBlock -> p2p.BroadcastBlock -> EncodeGossip
//	            -> "gossip message exceeds max gossip size"
//	direct push BlockChain.directPushBlock -> EncodeWithMaxLength
//	            -> "encoded message size N exceeds max chunk size"
//
// If a leader seals a block above that bound, NO follower ever receives it.
// HotStuff voting is import-gated (see consensus/hotstuff/proposal.go: a replica
// only votes once the block is imported), so no follower can vote, no quorum
// forms, the view times out, and the chain livelocks permanently — a restart
// does not help, because the leader deterministically rebuilds the same
// oversized block from the same mempool.
//
// The fix is to never build such a block: fillTransactions accounts for the
// RLP-encoded size of every transaction it packs and stops before crossing a
// safety threshold derived from the p2p bound.
package miner

import (
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/lib/rlp"
)

const (
	// blockSizeSafetyNum/blockSizeSafetyDen is the fraction of the p2p wire
	// bound a locally built block may occupy (90%). The remaining 10% absorbs
	// everything the builder cannot measure exactly at packing time.
	blockSizeSafetyNum = 9
	blockSizeSafetyDen = 10

	// blockNonTxReserve is a fixed allowance, on top of the measured header
	// size, for block parts that only materialise after fillTransactions
	// returns: the consensus seal written into Header.Extra, the finalize
	// rewards / verifiers lists, an optional zk proof, and the outer RLP list
	// framing. Deliberately generous — under-packing costs throughput, but
	// over-packing costs liveness.
	blockNonTxReserve = 32 << 10

	// minPackableTxSize is the smallest plausible RLP-encoded transaction. Once
	// less than this remains in the budget, packing stops instead of walking the
	// rest of the priority queue for a tx that cannot fit.
	minPackableTxSize = 100

	// maxSizeSkipStreak bounds how many consecutive size-driven account skips
	// the packing loop tolerates before declaring the block full. Without it a
	// block whose remaining budget is just under the typical transaction size
	// would walk (and re-encode) the entire pending queue every build. A few
	// skips still let genuinely smaller transactions top the block up.
	maxSizeSkipStreak = 8
)

// blockSizeLimiter tracks how many RLP bytes the block under construction has
// already committed to, and answers whether another payload still fits.
//
// It is intentionally conservative: every accounted size is an upper bound on
// the bytes the payload contributes to rlp(block), so a limiter that says "fits"
// can never produce an oversized block.
type blockSizeLimiter struct {
	// budget is the total number of transaction-payload bytes available.
	budget int
	// used is the number of transaction-payload bytes already committed.
	used int
	// skipStreak counts consecutive candidates rejected for size.
	skipStreak int
}

// newBlockSizeLimiter derives a packing budget for a block with the given
// header. The budget scales with encoder.MaxWireMessageSize, so raising
// N42_MAX_GOSSIP_MB (network-wide) automatically raises the packing budget.
func newBlockSizeLimiter(header *block.Header) *blockSizeLimiter {
	limit := encoder.MaxWireMessageSize()
	// uint64 -> int without overflow on 32-bit builds; the bound is bytes.
	safe := int(limit / blockSizeSafetyDen * blockSizeSafetyNum)
	if safe < 0 {
		safe = 0
	}

	overhead := blockNonTxReserve + encodedHeaderSize(header)
	budget := safe - overhead
	if budget < 0 {
		budget = 0
	}
	return &blockSizeLimiter{budget: budget}
}

// encodedHeaderSize returns the RLP-encoded size of header, or a conservative
// upper bound if it cannot be encoded yet.
func encodedHeaderSize(header *block.Header) int {
	if header == nil {
		return maxPlausibleHeaderSize
	}
	enc, err := rlp.EncodeToBytes(header)
	if err != nil {
		return maxPlausibleHeaderSize
	}
	return len(enc)
}

// maxPlausibleHeaderSize bounds a header whose exact encoding is unavailable:
// ~600 bytes of fixed fields plus room for a consensus seal in Extra.
const maxPlausibleHeaderSize = 8 << 10

// txPayloadSize returns the number of bytes a transaction contributes to
// rlp(block): its EIP-2718 raw encoding plus the RLP string header that wraps it
// as an element of the block's transaction list.
func txPayloadSize(tx *transaction.Transaction) (int, error) {
	// Memoized: admit() consults this once per candidate per build, and a full
	// re-encode per call was ~300ms of a 450ms fillTx on a 22,857-tx block.
	n, err := tx.EncodedSize()
	if err != nil {
		return 0, err
	}
	return n + rlpStringOverhead(n), nil
}

// bundlePayloadSize returns the combined block-payload size of an atomic bundle.
func bundlePayloadSize(txs []*transaction.Transaction) (int, error) {
	total := 0
	for _, tx := range txs {
		size, err := txPayloadSize(tx)
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

// rlpStringOverhead returns an upper bound on the RLP prefix bytes for a string
// of n bytes. (A single byte below 0x80 needs no prefix; returning 1 there is
// safe because it only over-estimates.)
func rlpStringOverhead(n int) int {
	switch {
	case n < 56:
		return 1
	case n < 1<<8:
		return 2
	case n < 1<<16:
		return 3
	case n < 1<<24:
		return 4
	default:
		return 5
	}
}

// packDecision is what the transaction-packing loop should do with a candidate.
type packDecision int

const (
	// packAccept: the transaction fits and may be executed.
	packAccept packDecision = iota
	// packSkipAccount: the transaction cannot be included in this block. The
	// loop must skip the whole sending account (Pop, not Shift) so a later nonce
	// is never packed without its predecessor, then keep filling from the other
	// accounts — a smaller transaction may still fit.
	packSkipAccount
	// packStop: too little budget remains for any transaction; stop packing.
	packStop
)

// admit is the single size gate of the packing loop: it reports whether tx can
// join the block being built, and how much payload budget it would consume.
// This is the exact decision fillTransactions applies, so a test exercising
// admit exercises the production path.
func (l *blockSizeLimiter) admit(tx *transaction.Transaction) (int, packDecision, error) {
	if l.exhausted() || l.skipStreak >= maxSizeSkipStreak {
		return 0, packStop, nil
	}
	size, err := txPayloadSize(tx)
	if err != nil {
		// Not a size rejection — an unencodable transaction must not count
		// towards the "block is full" streak.
		return 0, packSkipAccount, err
	}
	if !l.fits(size) {
		l.skipStreak++
		return size, packSkipAccount, nil
	}
	return size, packAccept, nil
}

// tooLargeForAnyBlock reports whether a payload of this size can never fit a
// block, no matter how empty — an operator-visible condition, not backpressure.
func (l *blockSizeLimiter) tooLargeForAnyBlock(size int) bool {
	return size > l.budget
}

// fits reports whether size more payload bytes still stay inside the budget.
func (l *blockSizeLimiter) fits(size int) bool {
	return l.used+size <= l.budget
}

// add commits size payload bytes to the block.
func (l *blockSizeLimiter) add(size int) {
	l.used += size
	l.skipStreak = 0
}

// exhausted reports whether so little budget remains that no transaction can
// plausibly fit — the signal to stop walking the priority queue.
func (l *blockSizeLimiter) exhausted() bool {
	return l.budget-l.used < minPackableTxSize
}

// remaining returns the unused payload budget in bytes (diagnostics only).
func (l *blockSizeLimiter) remaining() int {
	if r := l.budget - l.used; r > 0 {
		return r
	}
	return 0
}
