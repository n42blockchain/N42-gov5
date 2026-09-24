// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S39 (docs/QS_BLOCK_TIME_BUDGET.md 6dr/6ds): 6dr found that on a
// hand-over leader, "block push: arrived"(v-1) -> import_end(v-1) is 566 ms
// of which hdr+body+proc+write account for only ~200 ms -- ~366 ms is
// unattributed, with no line covering it. This file carries unix-ms stamps
// for the named hand-off points a follower's block-push receive path
// passes through on its way to InsertChain and beyond, so the existing
// per-block "blockimport phases" line (internal/blockchain.go) can report
// them without a new per-block info log. Each setter is a single plain
// field write (no lock): callers gate the call on their own package-level
// N42_CONTENTION_DIAG switch (one var per package, matching every other
// shared-name switch in this campaign), so the cost when the diag is off
// is exactly zero -- these methods are not even called.
//
// Written by, in pipeline order:
//   internal/sync's readFirstChunkedBlock  -- SetRxEndTMs, SetDecStamps
//   internal/sync's deferredCheck          -- SetCheckStamps
//   internal/sync's blockPushStreamHandler -- SetQueueTMs (just before InsertChain)
//   internal's insertChain loop            -- SetInsertStartTMs (the
//     "MISSING-STAMP" 6dr asked for: the start of InsertChain's OWN
//     per-block processing, distinct from the socket-receipt "arrived"
//     stamp and from whatever lock/queue wait sits between SetQueueTMs and
//     here -- the gap between the two is that wait, named by having both
//     points rather than a separate duration field)

package block

// SetRxEndTMs records when this block's raw chunk bytes finished arriving
// off the wire (encoder.DecodeWithMaxLengthLimit's own completion), before
// the RLP decode into this Block begins.
func (b *Block) SetRxEndTMs(t int64) { b.rxEndTMs = t }

// SetDecStamps records the full RLP decode's own start/end (decodeChunkedBlockReusePool
// or decodeChunkedBlock), bracketing the possibly-160k-transaction decode
// that follows the header peek/notify.
func (b *Block) SetDecStamps(start, end int64) { b.decStartTMs, b.decEndTMs = start, end }

// SetCheckStamps records CheckDeferredBlock's own call start/end
// (deferredCheck in internal/sync/rpc_block_push.go). A block whose parent
// was not yet applied may run this more than once (the retry path); the
// LATEST attempt's stamps are what is kept, matching "the deferred check
// that actually ran" rather than the first attempt.
func (b *Block) SetCheckStamps(start, end int64) { b.chkStartTMs, b.chkEndTMs = start, end }

// SetQueueTMs records the instant this block is handed to InsertChain,
// immediately before the call -- the far side of whatever lock/queue wait
// sits between here and SetInsertStartTMs.
func (b *Block) SetQueueTMs(t int64) { b.qTMs = t }

// SetInsertStartTMs records the start of InsertChain's OWN per-block
// processing (internal's insertChain loop, captured at the same point the
// existing dHdr/dBody accounting already anchors to) -- 6dr's own
// "MISSING-STAMP", distinct from the block-push handler's socket-receipt
// "arrived" stamp.
func (b *Block) SetInsertStartTMs(t int64) { b.insStartTMs = t }

// ImportStamps returns every stamp SetRxEndTMs/SetDecStamps/SetCheckStamps/
// SetQueueTMs/SetInsertStartTMs recorded, in pipeline order. Any stamp
// whose setter was never called (a block that took a different path, or
// N42_CONTENTION_DIAG was off at that hand-off) reads back as 0 -- callers
// must not treat a 0 as "this hand-off took no time", only as "not
// recorded here".
func (b *Block) ImportStamps() (rxEnd, decStart, decEnd, chkStart, chkEnd, q, insStart int64) {
	return b.rxEndTMs, b.decStartTMs, b.decEndTMs, b.chkStartTMs, b.chkEndTMs, b.qTMs, b.insStartTMs
}
