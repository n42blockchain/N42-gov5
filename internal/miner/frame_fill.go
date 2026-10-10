// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/internal/miner/builder"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// EnvFrameBlockMaxFrames caps the frames in one block (default 1024).
const EnvFrameBlockMaxFrames = "N42_FRAME_BLOCK_MAX_FRAMES"

func frameBlockMaxFrames() int {
	if v, err := strconv.Atoi(os.Getenv(EnvFrameBlockMaxFrames)); err == nil && v > 0 {
		return v
	}
	return 1024
}

// framePick is the builder's choice of frames for one block.
type framePick struct {
	hashes []types.Hash
	raws   [][]byte
	gas    uint64 // sum of the frames' admission gas estimates
	txs    int
}

// pickFrames takes frames from the store in arrival order (the store's
// ring, oldest first: never map or hash order), so each gateway's frames
// are consumed as a prefix and one sender's consecutive nonces, spread
// over consecutive frames, stay contiguous. It skips frames a written
// block holds (store pins), those an unwritten ancestor holds (inFlight)
// and those signed by exclude (this node's own local frames; zero = none),
// and stops at the first frame whose gas estimate does not fit the budget
// or at maxFrames. Stopping rather than skipping keeps blocks in arrival
// order.
func pickFrames(st *store.Store, inFlight func(types.Hash) bool, exclude types.Address, budget uint64, maxFrames int) framePick {
	var p framePick
	st.ScanFillable(func(h types.Hash, e store.Entry) bool {
		if inFlight != nil && inFlight(h) {
			return true
		}
		if exclude != (types.Address{}) {
			if gw, ok := frame.GatewayOf(e.Raw); ok && gw == exclude {
				return true
			}
		}
		if len(p.hashes) >= maxFrames || p.gas+e.Gas > budget {
			return false
		}
		p.hashes = append(p.hashes, h)
		p.raws = append(p.raws, e.Raw)
		p.gas += e.Gas
		p.txs += e.Txs
		return true
	})
	return p
}

// fillFrames fills a frame-referenced block: whole frames from the frame
// store, executed under the skip rule. The parallel path is the leader's
// copy of the followers' Block-STM import; the serial path commits frame by
// frame and drops a frame that hits a failure outside the skip rule.
func (w *worker) fillFrames(env *environment, ibs *state.IntraBlockState, getHeader func(hash types.Hash, number uint64) *block.Header,
	commitTx func(*transaction.Transaction) error, parallelOK bool, tl *fillTimeline) error {
	bc, ok := w.chain.(*internal.BlockChain)
	if !ok || bc.FrameStore() == nil {
		return nil
	}
	header := env.header
	number := header.Number.Uint64()
	t0 := time.Now()
	st := bc.FrameStore()
	inFlight := bc.FramesInFlight()
	budget := env.gasPool.Gas()
	maxFrames := frameBlockMaxFrames()
	// Pool transactions first, as this node's local frames, up to the pool
	// gas budget; gateway frames fill the rest.
	local, own := w.fillLocalFrames(env, ibs, st, inFlight, budget, maxFrames)
	gw := pickFrames(st, inFlight, own, budget-local.gas, maxFrames-len(local.hashes))
	pick := local
	pick.hashes = append(pick.hashes, gw.hashes...)
	pick.raws = append(pick.raws, gw.raws...)
	pick.gas += gw.gas
	pick.txs += gw.txs
	if len(pick.hashes) == 0 {
		return nil
	}
	dPick := time.Since(t0)
	tDecode := time.Now()
	per, dropped, err := frame.DecodeFramesEach(pick.raws, 0)
	if err != nil {
		// Admitted frames passed the layout check; this cannot happen
		// short of memory corruption, and is not worth a block.
		log.Error("miner: frame decode failed", "err", err)
		return nil
	}
	flat := make([]*transaction.Transaction, 0, pick.txs)
	cachedSenders := 0
	for i, p := range per {
		// Senders recovered at admission (P2a); the build recovers the rest.
		cachedSenders += frame.ApplySenders(p, st.Senders(pick.hashes[i]))
		flat = append(flat, p...)
	}
	dDecode := time.Since(tDecode)
	tExec := time.Now()
	finish := func(hashes []types.Hash, raws [][]byte, path string, skipped int, reasons *internal.FrameSkipCounts) error {
		// Marked in flight when the block is assembled (commit), not here: a
		// build discarded by a new head must leave its frames fillable.
		env.frameHashes = hashes
		env.frameData = raws
		if tl != nil {
			tl.FillLoopDoneAt = time.Now()
			tl.FillExecDur = time.Since(tExec)
			tl.PendingTxCount = len(flat)
		}
		log.Info("miner: frame fill", "n", number, "path", path, "frames", len(hashes), "picked", len(pick.hashes),
			"localFrames", len(local.hashes), "localTxs", local.txs,
			"txs", len(env.txs), "skipped", skipped, "undecodable", dropped, "cachedSenders", cachedSenders, "gasEst", pick.gas, "gasUsed", header.GasUsed,
			"storeFrames", bc.FrameStore().Len(), "pick", dPick, "decode", dDecode, "exec", time.Since(tExec))
		internal.LogFrameSkips("build", number, len(env.txs), skipped, reasons)
		return nil
	}

	if parallelOK && parallelFillEnabled() && env.tcount == 0 && len(flat) > 4 {
		run, perr := bc.BuildParallelFrames(header, flat, ibs, internal.GetHashFn(header, getHeader))
		if perr == nil {
			if err := env.gasPool.SubGas(run.UsedGas); err != nil {
				return err
			}
			env.txs = append(env.txs, run.Included...)
			env.receipts = append(env.receipts, run.Receipts...)
			env.tcount += len(run.Included)
			header.GasUsed += run.UsedGas
			return finish(pick.hashes, pick.raws, "parallel", run.Skipped, &run.Reasons)
		}
		if !errors.Is(perr, internal.ErrParallelNotApplicable) && !errors.Is(perr, internal.ErrFrameTxNotSkippable) {
			return perr
		}
		log.Debug("miner: frame fill falls back to serial", "n", number, "reason", perr)
	}

	signer := transaction.MakeSignerWithTimestamp(w.chainConfig, header.Number.ToBig(), header.Time)
	var (
		hashes  []types.Hash
		raws    [][]byte
		skipped int
		reasons internal.FrameSkipCounts
	)
	for fi, txs := range per {
		snap := ibs.Snapshot()
		gasSnap, usedSnap := env.gasPool.Gas(), header.GasUsed
		nTxs, nRcpt, tcount, skipSnap, reasonSnap := len(env.txs), len(env.receipts), env.tcount, skipped, reasons
		ok := true
		for _, tx := range txs {
			err := commitTx(tx)
			if err == nil {
				env.tcount++
				continue
			}
			if internal.IsFrameSkippable(err, tx, signer) {
				env.txs = append(env.txs, tx)
				env.receipts = append(env.receipts, internal.SkippedTxReceipt(tx, header, env.tcount, header.GasUsed))
				env.tcount++
				skipped++
				reasons.Add(err)
				continue
			}
			log.Warn("miner: frame dropped from block", "n", number, "frame", pick.hashes[fi], "tx", tx.Hash(), "err", err)
			ok = false
			break
		}
		if !ok {
			ibs.RevertToSnapshot(snap)
			env.gasPool = new(common.GasPool).AddGas(gasSnap)
			header.GasUsed = usedSnap
			env.txs, env.receipts, env.tcount, skipped, reasons = env.txs[:nTxs], env.receipts[:nRcpt], tcount, skipSnap, reasonSnap
			continue
		}
		hashes = append(hashes, pick.hashes[fi])
		raws = append(raws, pick.raws[fi])
	}
	if len(hashes) == 0 {
		return nil
	}
	return finish(hashes, raws, "serial", skipped, &reasons)
}

// EnvFrameBlockPoolGas is the gas a frame block gives the transaction pool,
// packed into the leader's local frames ahead of the gateway frames
// (default: a tenth of the block gas limit). Pool transactions that do not
// fit wait for the next block; gas the pool does not use goes to gateway
// frames.
const EnvFrameBlockPoolGas = "N42_FRAME_BLOCK_POOL_GAS"

func framePoolGas(gasLimit uint64) uint64 {
	if v, err := strconv.ParseUint(os.Getenv(EnvFrameBlockPoolGas), 10, 64); err == nil && v > 0 {
		return v
	}
	return gasLimit / 10
}

// localFramePruneDepth: a local frame built this many blocks ago that no
// block took (its build was discarded) leaves the store; its transactions
// are still in the pool and are framed again.
const localFramePruneDepth = 64

// localFrameState is the worker's local-frame bookkeeping.
type localFrameState struct {
	signer atomic.Pointer[frame.Signer]
	seq    atomic.Uint64

	mu    sync.Mutex
	built map[types.Hash]uint64 // FrameHash -> number of the block built
	// Lag mode (P2a): the pool transactions of each built local frame, and
	// those a block not yet executed holds (tx hash -> block number).
	txsOf       map[types.Hash][]types.Hash
	inFlightTxs map[types.Hash]uint64
}

// SetLocalFrameSigner sets the key the leader frames pool transactions
// under when frame blocks are on (nil = no local frames: the pool is not
// mined in frame blocks).
func (w *worker) SetLocalFrameSigner(s *frame.Signer) {
	if w.localFrames.seq.Load() == 0 {
		// Seeded from the clock so a restarted node does not rebuild a
		// frame hash its previous run already used.
		w.localFrames.seq.CompareAndSwap(0, uint64(time.Now().UnixNano())) // #nosec G115 -- positive
	}
	w.localFrames.signer.Store(s)
}

// localFrame is one frame packed from pool transactions.
type localFrame struct {
	hash types.Hash
	raw  []byte
	gas  uint64
	txs  []*transaction.Transaction
}

// packLocalFrames packs pending pool transactions into frames signed by s,
// in the pool fill's order (price and nonce), each frame at most
// frame.MaxTxs transactions and frame.MaxPayload bytes, until the gas
// limits of the packed transactions reach budget. An account whose next
// transaction does not fit the remaining budget, cannot pay baseFee, or has
// no frame encoding is passed over with its later transactions, as the pool
// fill does.
func packLocalFrames(s *frame.Signer, nextSeq func() uint64, pending map[types.Address][]*transaction.Transaction,
	baseFee *uint256.Int, budget uint64) ([]localFrame, error) {
	if s == nil || len(pending) == 0 || budget < params.TxGas {
		return nil, nil
	}
	txSet := builder.NewTxByPriceAndNonce(pending, baseFee)
	var (
		out      []localFrame
		encs     [][]byte
		txs      []*transaction.Transaction
		gas      uint64
		payload  int
		remain   = budget
		flushErr error
	)
	flush := func() {
		if len(encs) == 0 || flushErr != nil {
			return
		}
		raw, h, err := frame.EncodeWith(s, nextSeq(), frame.TxTypeEthereum, encs)
		if err != nil {
			flushErr = err
			return
		}
		out = append(out, localFrame{hash: h, raw: raw, gas: gas, txs: txs})
		encs, txs, gas, payload = nil, nil, 0, 0
	}
	for remain >= params.TxGas && flushErr == nil {
		tx := txSet.Peek()
		if tx == nil {
			break
		}
		if tx.Gas() > remain {
			txSet.Pop()
			continue
		}
		feeCap := tx.GasFeeCap()
		if feeCap == nil {
			feeCap = tx.GasPrice()
		}
		if baseFee != nil && feeCap != nil && feeCap.Cmp(baseFee) < 0 {
			txSet.Pop()
			continue
		}
		enc, err := tx.EthEncoded()
		if err != nil || len(enc) == 0 || len(enc) > 0xFFFF {
			txSet.Pop()
			continue
		}
		if len(encs) == frame.MaxTxs || payload+2+len(enc) > frame.MaxPayload {
			flush()
		}
		encs = append(encs, enc)
		txs = append(txs, tx)
		gas += tx.Gas()
		payload += 2 + len(enc)
		remain -= tx.Gas()
		txSet.Shift()
	}
	flush()
	return out, flushErr
}

// fillLocalFrames frames the pool's pending transactions for this block:
// stale ones (already mined on this build's parent state) are trimmed, the
// rest packed under the local signer up to the pool gas budget, and each
// frame is put into the frame store, where the followers pull it from
// (FRQ1) like any frame. It returns the frames and the local signer's
// address (zero when there is no signer), which the gateway pick excludes.
func (w *worker) fillLocalFrames(env *environment, ibs *state.IntraBlockState, st *store.Store,
	inFlight func(types.Hash) bool, budget uint64, maxFrames int) (framePick, types.Address) {
	var p framePick
	s := w.localFrames.signer.Load()
	if s == nil || w.txsPool == nil || maxFrames <= 0 {
		return p, types.Address{}
	}
	number := env.header.Number.Uint64()
	w.pruneLocalFrames(st, inFlight, number)
	poolGas := framePoolGas(env.header.GasLimit)
	if poolGas > budget {
		poolGas = budget
	}
	pending := w.txsPool.Pending(false)
	if len(pending) == 0 {
		return p, s.Address
	}
	if block.LagMode() {
		// Execution runs behind: the pool still holds what unexecuted
		// blocks' local frames took.
		var committed uint64
		if cur := w.chain.CurrentBlock(); cur != nil && cur.Number64() != nil {
			committed = cur.Number64().Uint64()
		}
		w.dropInFlightPending(pending, committed)
	}
	trimStalePending(ibs.GetStateReader(), pending)
	frames, err := packLocalFrames(s, func() uint64 { return w.localFrames.seq.Add(1) }, pending, env.header.BaseFee, poolGas)
	if err != nil {
		log.Warn("miner: local frames not built", "n", number, "err", err)
	}
	for _, f := range frames {
		if len(p.hashes) >= maxFrames {
			break
		}
		if inFlight != nil && inFlight(f.hash) {
			// An unwritten ancestor holds the identical frame; its
			// transactions are not this block's to include.
			break
		}
		// Local frames bypass the store's budget (bounded by the pool gas
		// of one block): the pool must not starve behind gateway frames.
		if st.Admit(f.hash, store.Entry{Raw: f.raw, Gas: f.gas, Txs: len(f.txs)}, true) != store.Stored && !st.Has(f.hash) {
			// Refused (a block already took it): the followers could not
			// pull it, so it is not used.
			break
		}
		w.localFrames.mu.Lock()
		if w.localFrames.built == nil {
			w.localFrames.built = make(map[types.Hash]uint64)
		}
		w.localFrames.built[f.hash] = number
		if block.LagMode() {
			if w.localFrames.txsOf == nil {
				w.localFrames.txsOf = make(map[types.Hash][]types.Hash)
			}
			hs := make([]types.Hash, len(f.txs))
			for i, tx := range f.txs {
				hs[i] = tx.Hash()
			}
			w.localFrames.txsOf[f.hash] = hs
		}
		w.localFrames.mu.Unlock()
		p.hashes = append(p.hashes, f.hash)
		p.raws = append(p.raws, f.raw)
		p.gas += f.gas
		p.txs += len(f.txs)
	}
	return p, s.Address
}

// pruneLocalFrames drops local frames built localFramePruneDepth blocks
// before number that no unwritten block holds. A frame a written block took
// has left the store already; removing it again is a no-op.
func (w *worker) pruneLocalFrames(st *store.Store, inFlight func(types.Hash) bool, number uint64) {
	lf := &w.localFrames
	lf.mu.Lock()
	var drop []types.Hash
	for h, n := range lf.built {
		if n+localFramePruneDepth >= number {
			continue
		}
		delete(lf.built, h)
		delete(lf.txsOf, h)
		if inFlight == nil || !inFlight(h) {
			drop = append(drop, h)
		}
	}
	lf.mu.Unlock()
	if len(drop) > 0 {
		st.Remove(drop)
	}
}
