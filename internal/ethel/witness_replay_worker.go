// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_worker.go — per-block parallel replay logic.
//
// A worker pulls a (header, body, witness, senders) tuple from blockCh,
// re-executes the block against a witness-backed StateReader + a
// per-block WitnessCapturingWriter, verifies receipts root + gasUsed,
// encodes acctcs/storcs/receipts bytes, and pushes the result to
// resultCh. Workers are fully independent — each holds its own MDBX
// RoTx for the Code table and its own per-block writer state.

package ethel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// WitnessJob is the input handed to a worker for one block. All fields
// are owned by the worker once received; no shared mutable state.
type WitnessJob struct {
	BlockNum    uint64
	Header      *block.Header
	Body        *GethBodyResult
	Witness     []byte
	Senders     []types.Address
	BlockHashFn func(uint64) types.Hash

	// inputBytes is the decoded-memory reservation held while this job waits
	// in the reservoir or executes. Workers release it only after execution,
	// so the producer's high/low watermarks include in-flight work.
	inputReservoir *replayInputReservoir
	inputBytes     int64
}

func (j *WitnessJob) releaseInputReservation() {
	if j.inputReservoir != nil {
		j.inputReservoir.release(j.inputBytes)
		j.inputReservoir = nil
		j.inputBytes = 0
	}
}

// WitnessResult is what a worker produces per block. Output bytes are
// ready for outputBatcher.addEntry; the encoder ran inline so the
// aggregator only needs to sequence them. GasUsed and TxCount let the
// aggregator log throughput in mgas/s and tx/s without re-reading
// headers.
type WitnessResult struct {
	BlockNum     uint64
	GasUsed      uint64
	TxCount      uint32
	ReceiptBytes []byte
	AcctCSBytes  []byte
	StoCSBytes   []byte
	WipesBytes   []byte
	WitnessBytes []byte
	// Writer is set only in Capture mode: it holds the per-block post-state
	// (accNewVals/stoNewVals/wipedAddrs) so a caller (e.g. the mobile overlay)
	// can read the changeset directly without re-encoding to cdat bytes.
	Writer *WitnessCapturingWriter
	Err    error
}

// ReplayMode bundles the per-worker switches that control whether to
// verify gas / emit output cdat bytes. Bundling avoids parameter
// sprawl through runWitnessWorker → replayWitnessBlock.
type ReplayMode struct {
	SkipVerify bool
	NoOutput   bool
	// Capture commits the block through a WitnessCapturingWriter and returns
	// it via WitnessResult.Writer WITHOUT encoding acctcs/storcs/receipts —
	// the lightweight path a minimal/mobile client uses to harvest the
	// post-state changeset. Implies output (overrides NoOutput's NoopWriter).
	Capture bool
}

// runWitnessWorker pulls jobs from blockCh, replays each, pushes
// WitnessResult to resultCh. Returns when blockCh closes or ctx
// cancels. codeDB and codes are alternative bytecode sources;
// pass nil for either to disable that source. At least one must be
// non-nil for contract execution to work.
func runWitnessWorker(
	ctx context.Context,
	id int,
	blockCh <-chan WitnessJob,
	resultCh chan<- WitnessResult,
	codeDB kv.RoDB,
	codes *CodesFreezerReader,
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	mode ReplayMode,
) {
	var codeTx kv.Tx
	if codeDB != nil {
		var err error
		codeTx, err = codeDB.BeginRo(ctx)
		if err != nil {
			select {
			case resultCh <- WitnessResult{Err: fmt.Errorf("worker %d: BeginRo: %w", id, err)}:
			case <-ctx.Done():
			}
			return
		}
		defer codeTx.Rollback()
	}

	// One IBS per worker, reused across blocks via Reset + SetStateReader.
	// Without reuse, every block allocated a fresh IBS plus a fresh
	// stateObject for every touched address — pprof showed the
	// stateObjectPool.New func at 18% of all allocs because the per-block
	// IBS was discarded with its objects, never returning them to the
	// pool. Reset returns objects to pool before the next block touches
	// them, so the pool actually recycles.
	reader := NewWitnessReplayReader(nil, codeTx)
	if codes != nil {
		reader.SetCodesFreezer(codes)
	}
	ibs := state.New(reader)
	// Replay consumes a block's logs once, at receipt-root verification, and
	// discards the block. That lets logs be bump-allocated per block.
	ibs.SetLogArena(true)
	if mode.NoOutput && !mode.Capture {
		// Validation consumes receipts/gas only and deliberately skips
		// CommitBlock below. Avoid retaining the duplicate block-level storage
		// originals that exist solely to build a later write set.
		ibs.SetDiscardBlockChanges(true)
	}

	// Select on ctx during receive so workers exit promptly when the
	// pipeline cancels (early error before the reader goroutine can
	// close blockCh — without this, codeTx held here deadlocks the
	// caller's defer codeDB.Close()).
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-blockCh:
			if !ok {
				return
			}
			res := replayWitnessBlock(job, codeTx, chainCfg, engine, mode, ibs, reader)
			if res.Err != nil {
				diagnoseReplayFailure(job, res.Err, codeTx, chainCfg, engine, mode, reader)
			}
			job.releaseInputReservation()
			// Drop decoded input references before a potentially blocking result
			// send. The reservoir has made the bytes available for refill.
			job = WitnessJob{}
			select {
			case resultCh <- res:
			case <-ctx.Done():
				return
			}
		}
	}
}

// replayWitnessBlock is the per-block replay path. The supplied ibs +
// reader are owned by the caller (worker); this function rebinds them
// to the new block's witness stream and resets in-place to put the
// previous block's stateObjects back into the pool.
func replayWitnessBlock(
	job WitnessJob,
	codeTx kv.Tx,
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	mode ReplayMode,
	ibs *state.IntraBlockState,
	reader *WitnessReplayReader,
) WitnessResult {
	res := WitnessResult{
		BlockNum: job.BlockNum,
		GasUsed:  job.Header.GasUsed,
		TxCount:  uint32(len(job.Body.Transactions)),
	}
	if !mode.NoOutput {
		res.WitnessBytes = job.Witness
	}

	if len(job.Body.Transactions) == 0 && job.Header.GasUsed != 0 {
		res.Err = fmt.Errorf("block %d: empty body but header.GasUsed=%d",
			job.BlockNum, job.Header.GasUsed)
		return res
	}

	// Empty witness + non-empty body means the record-side archive doesn't
	// cover this block (ethexec stopped before reaching it, or ran with
	// --no-witness past some checkpoint). Without state-read values the
	// EVM sees every account as absent (nonce=0, balance=0); the first tx
	// fails preCheck with a confusing "nonce too high: state=0" that
	// hides the real cause — witness truncation. Report it directly so
	// the user knows where to rerun --end or re-record from.
	// Empty body + empty witness is legitimate (uncle-only / withdrawal-
	// only blocks pre-Cancun didn't read any state).
	if len(job.Witness) == 0 && len(job.Body.Transactions) > 0 {
		res.Err = fmt.Errorf("block %d: witness stream is empty but body has %d txs — record-side archive ends before this block (or was recorded with --no-witness); rerun with --end %d to stop at the last good block, or re-record witness up to your desired range",
			job.BlockNum, len(job.Body.Transactions), job.BlockNum)
		return res
	}

	// Block 0 is handled by the pipeline (genesis encoding requires a
	// fresh chaindata, not the user's potentially-populated MDBX); the
	// reader skips block 0 and the aggregator injects pre-encoded
	// bytes directly. Worker should never see block 0.
	if job.BlockNum == 0 {
		res.Err = fmt.Errorf("worker received block 0 — pipeline should have intercepted")
		return res
	}

	reader.Reset(job.Witness)
	ibs.Reset()

	var writer *WitnessCapturingWriter
	var stateWriter state.WriterWithChangeSets
	if mode.NoOutput && !mode.Capture {
		stateWriter = state.NewNoopWriter()
	} else {
		writer = NewWitnessCapturingWriter()
		stateWriter = writer
	}

	uncles := make([]block.IHeader, len(job.Body.Uncles))
	for i, u := range job.Body.Uncles {
		uncles[i] = u
	}

	result, err := ProcessBlock(
		chainCfg, engine, job.Header,
		job.Body.Transactions, uncles, job.Body.Withdrawals,
		ibs, job.BlockHashFn, job.Senders, stateWriter,
	)
	// IBS swallows StateReader errors via setError → savedErr; ProcessBlock
	// itself never checks. A failing ReadAccountCode (e.g. codes-freezer
	// missing a contract) silently coerces a contract to EOA and shifts
	// every subsequent SLOAD off the witness stream — manifests blocks
	// later as garbage account data ("nonce too high"). Prefer the IBS
	// error since it's the real root cause; the ProcessBlock err is just
	// the downstream symptom.
	if ibsErr := ibs.Error(); ibsErr != nil {
		if err != nil {
			res.Err = fmt.Errorf("block %d: state reader error (downstream symptom: %v): %w", job.BlockNum, err, ibsErr)
		} else {
			res.Err = fmt.Errorf("block %d: state reader error swallowed by IBS: %w", job.BlockNum, ibsErr)
		}
		return res
	}
	if err != nil {
		res.Err = fmt.Errorf("block %d: ProcessBlock: %w", job.BlockNum, err)
		return res
	}

	if !mode.SkipVerify {
		if result.GasUsed != job.Header.GasUsed {
			res.Err = fmt.Errorf("block %d: gas mismatch: got %d want %d",
				job.BlockNum, result.GasUsed, job.Header.GasUsed)
			return res
		}
		// Receipt-root verification — gas-only matched but receipts diverged
		// would slip silently otherwise. Skipped pre-Byzantium because mainnet
		// stored a 32-byte PostState root in the receipt's first field; we
		// only carry Status, so EthReceiptHash can't reproduce the canonical
		// root for those blocks (same constraint Reth has — recoverable
		// only via re-execution against full state).
		if chainCfg.IsByzantium(job.Header.Number.Uint64()) {
			if got := EthReceiptHash(result.Receipts); got != job.Header.ReceiptHash {
				res.Err = fmt.Errorf("block %d: receipt root mismatch: got %s want %s",
					job.BlockNum, got.Hex(), job.Header.ReceiptHash.Hex())
				return res
			}
		}
	}

	if mode.NoOutput && !mode.Capture {
		return res
	}

	rules := chainCfg.Rules(job.Header.Number.Uint64())
	if err := ibs.CommitBlock(rules, writer); err != nil {
		res.Err = fmt.Errorf("block %d: CommitBlock: %w", job.BlockNum, err)
		return res
	}

	// Capture path: hand the writer (post-state changeset) back without the
	// cdat encoding the bulk pipeline needs. The mobile overlay reads
	// writer.PostState() directly.
	if mode.Capture {
		res.Writer = writer
		return res
	}

	acctsCS, err := writer.ChangeSetWriter().GetAccountChanges()
	if err != nil {
		res.Err = fmt.Errorf("block %d: GetAccountChanges: %w", job.BlockNum, err)
		return res
	}
	stoCS, err := writer.ChangeSetWriter().GetStorageChanges()
	if err != nil {
		res.Err = fmt.Errorf("block %d: GetStorageChanges: %w", job.BlockNum, err)
		return res
	}
	res.AcctCSBytes = EncodeAccountChanges(acctsCS, writer.AccountNewValue)
	res.StoCSBytes = EncodeStorageChanges(stoCS, writer.StorageNewValue)
	res.WipesBytes = writer.WipedAddrsBytes()
	res.ReceiptBytes = EncodeReceiptsCompact(result.Receipts)
	return res
}

// diagnoseReplayFailure re-executes a failed block once, on a fresh
// IntraBlockState, before the failure is reported. A full-archive run at 256
// workers failed block 12,854,611 with a nonce one lower than expected while
// the same binary at 128 workers passed the same block, and no shorter window
// reproduces it. The one fact that decides where to look next is whether the
// failure is deterministic given the exact inputs this worker received: if the
// retry passes, per-process shared state corrupted the first attempt; if it
// fails identically, the inputs (body, witness, senders) were wrong on arrival.
// The original error is still returned by the caller; this only adds evidence.
func diagnoseReplayFailure(
	job WitnessJob,
	firstErr error,
	codeTx kv.Tx,
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	mode ReplayMode,
	reader *WitnessReplayReader,
) {
	witnessDigest := sha256.Sum256(job.Witness)
	fresh := state.New(reader)
	fresh.SetLogArena(true)
	if mode.NoOutput && !mode.Capture {
		fresh.SetDiscardBlockChanges(true)
	}
	retry := replayWitnessBlock(job, codeTx, chainCfg, engine, mode, fresh, reader)
	verdict := "retry PASSED — failure is not a function of this worker's inputs (per-process shared state)"
	if retry.Err != nil {
		verdict = "retry FAILED — deterministic for the inputs this worker received"
		if retry.Err.Error() != firstErr.Error() {
			verdict += " (with a DIFFERENT error)"
		}
	}
	log.Error("Replay failure diagnostic",
		"block", job.BlockNum,
		"header", job.Header.Hash().Hex(),
		"txs", len(job.Body.Transactions),
		"senders", len(job.Senders),
		"witness_bytes", len(job.Witness),
		"witness_sha256", hex.EncodeToString(witnessDigest[:8]),
		"first_err", firstErr,
		"retry_err", retry.Err,
		"verdict", verdict)
}
