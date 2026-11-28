// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Profile-1 block path of replay v2 (see profile1.go). A replayed block, gap
// blocks included, is executed the way a profile-1 node executes an imported
// block (internal.StateProcessor.Process): block-start system calls (EIP-4788,
// EIP-2935), the transactions, the block-end requests calls (EIP-6110 log
// scan, EIP-7002, EIP-7251), then the hotstuff dev block reward to the
// coinbase and the dev faucet. Transactions the re-derived context refuses
// are left out of the block and counted (Stats.SkipReasons err_*).

package replay

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// p1SrcTimeKey (QMDBMeta) holds the source time of the last replayed source
// block: the gap-fill reference, since profile-1 target times are slot times.
var p1SrcTimeKey = []byte("replay_p1_src_time")

// profile1 reports whether the replay re-derives blocks to profile 1.
func (e *EngineV2) profile1() bool { return e.cfg.Profile1Genesis != nil }

// p1Init builds the sealer once the target genesis exists and, when the
// target already holds replayed blocks, restores the carried state from the
// head: its executed result, its CommitQC and the source time.
func (e *EngineV2) p1Init(tx kv.Tx) error {
	if e.p1 != nil {
		return nil
	}
	genesis := rawdb.ReadHeaderByNumber(tx, 0)
	if genesis == nil {
		return errors.New("replay profile 1: target has no genesis header")
	}
	s, err := NewP1Sealer(e.cfg.ChainConfig, genesis, e.cfg.Profile1Keys)
	if err != nil {
		return err
	}
	head := uint64(0)
	if v, _ := tx.GetOne(modules.QMDBMeta, []byte("replay_chain_head")); len(v) >= 8 {
		head = binary.BigEndian.Uint64(v)
	}
	if head > 0 {
		hh, err := rawdb.ReadCanonicalHash(tx, head)
		if err != nil || hh == (types.Hash{}) {
			return fmt.Errorf("replay profile 1: head %d has no canonical hash", head)
		}
		exec, ok, err := rawdb.ReadExecutedResult(tx, hh)
		if err != nil || !ok {
			return fmt.Errorf("replay profile 1: no executed result for head %d: %v", head, err)
		}
		st, err := hotstuff.LoadConsensusState(tx)
		if err != nil || st == nil || st.LastCommittedQC.BlockHash != hh {
			return fmt.Errorf("replay profile 1: persisted CommitQC does not name head %d: %v", head, err)
		}
		qc := st.LastCommittedQC
		s.Resume(exec, &qc)
		e.p1LastQC = &qc
		if v, _ := tx.GetOne(modules.QMDBMeta, p1SrcTimeKey); len(v) >= 8 {
			e.p1SrcTime = binary.BigEndian.Uint64(v)
		}
	}
	e.p1 = s
	return nil
}

// p1Flush persists, with the batch, what a restarted replay and a live
// validator continue from: the consensus state locked on the head's CommitQC
// and the last source time.
func (e *EngineV2) p1Flush(tx kv.RwTx) error {
	if e.p1LastQC == nil {
		return nil
	}
	if err := hotstuff.SaveConsensusState(tx, ContinuationState(e.p1LastQC)); err != nil {
		return fmt.Errorf("save continuation consensus state: %w", err)
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], e.p1SrcTime)
	return tx.Put(modules.QMDBMeta, p1SrcTimeKey, b[:])
}

// p1Rewards pays the hotstuff dev block reward as Finalize does on a
// profile-1 chain (adapter.go: coinbase, then the dev faucet). A profile-1
// genesis has no APoS reward config, so the APoS reward function pays nothing.
func p1Rewards(cfg *params.ChainConfig, ibs *state.IntraBlockState, coinbase types.Address) []*block.Reward {
	hs := cfg.HotStuff
	if hs == nil || !hs.DevBlockReward.Positive() {
		return nil
	}
	amount := hs.DevBlockReward.Amount()
	ibs.AddBalance(coinbase, amount)
	rewards := []*block.Reward{{Address: coinbase, Amount: amount}}
	if fa := hs.DevFaucetAddress; fa != nil && *fa != (types.Address{}) {
		famount := hs.DevBlockReward.Amount()
		ibs.AddBalance(*fa, famount)
		rewards = append(rewards, &block.Reward{Address: *fa, Amount: famount})
	}
	return rewards
}

// p1Block replays one block as profile 1: src is the source block (nil for a
// gap block), srcTime its source time. It writes the block, its receipts,
// committee evidence and executed result, and returns the block.
func (e *EngineV2) p1Block(dstTx kv.RwTx, histAgg *state.HistoryAggregator, qmdbRC *commitment.QMDBRootComputer,
	number uint64, parentHash types.Hash, src *block.Block, srcTime uint64) (*block.Block, error) {

	cfg := e.cfg.ChainConfig
	parent := rawdb.ReadHeader(dstTx, parentHash, number-1)
	if parent == nil {
		return nil, fmt.Errorf("replay profile 1: parent %d %x not in target", number-1, parentHash[:8])
	}
	var srcHeader *block.Header
	var txs []*transaction.Transaction
	if src != nil {
		srcHeader = src.Header().(*block.Header)
		txs = src.Transactions()
		if srcHeader.MobileRegistryRoot != nil {
			root := *srcHeader.MobileRegistryRoot
			e.lastRegistryRoot = &root
		}
	}
	// B11: the root is always present; before the source anchored any root
	// the registry is empty and its root is zero (the genesis value, A6, and
	// what an empty mobileverify registry reports).
	var registry types.Hash
	if e.lastRegistryRoot != nil {
		registry = *e.lastRegistryRoot
	}
	h, err := e.p1.Context(parent, number, srcTime, registry)
	if err != nil {
		return nil, err
	}
	pbr := rawdb.ParentBeaconRoot(dstTx, number)
	h.ParentBeaconRoot = &pbr

	// State plumbing as in the legacy path: PlainState -> cache -> witness.
	if e.replayCache == nil {
		e.replayCache = layered.NewShardedCache(256, 512*1024)
	}
	cachedReader := state.NewCachedStateReader(state.NewPlainStateReader(dstTx), e.replayCache)
	if e.witnessReader == nil {
		e.witnessReader = NewWitnessStateReader(cachedReader)
	} else {
		e.witnessReader.inner = cachedReader
		e.witnessReader.Reset()
	}
	csWriter := state.NewPlainStateWriter(dstTx, dstTx, number)
	csWriter.SetHistoryAggregator(histAgg)
	w := state.NewCachedStateWriter(csWriter, e.replayCache)
	ibs := state.New(e.witnessReader)
	ibs.SetRootComputer(qmdbRC)
	if e.cfg.QMDBHistory {
		if err := qmdbRC.BeginBlockHistory(number); err != nil {
			return nil, fmt.Errorf("qmdb history begin block %d: %w", number, err)
		}
	}
	rules := cfg.RulesWithTimestamp(number, h.Time)

	if err := internal.ProcessBeaconBlockRootWithWriter(&pbr, cfg, ibs, h, w); err != nil {
		return nil, fmt.Errorf("block %d start (EIP-4788): %w", number, err)
	}
	if err := internal.ProcessPragueBlockStartWithWriter(cfg, ibs, h, w); err != nil {
		return nil, fmt.Errorf("block %d start (EIP-2935): %w", number, err)
	}
	applied, receipts, _, gasFigure := e.replayTxs(ibs, w, txs, types.Hash{}, h, dstTx)
	if err := e.p1RepairErr; err != nil {
		return nil, fmt.Errorf("block %d: %w", number, err)
	}
	requests, err := internal.ProcessExecutionBlockEndWithWriter(receipts, cfg, ibs, h, nil, w)
	if err != nil {
		return nil, fmt.Errorf("block %d end (requests): %w", number, err)
	}
	rewards := p1Rewards(cfg, ibs, h.Coinbase)
	if e.cfg.Profile1SourceRewards && src != nil {
		if bd, ok := src.Body().(*block.Body); ok && bd != nil {
			for _, r := range bd.Rewards {
				if r.Amount != nil && !r.Amount.IsZero() {
					ibs.AddBalance(r.Address, r.Amount)
					rewards = append(rewards, &block.Reward{Address: r.Address, Amount: r.Amount.Clone()})
				}
			}
		}
	}
	if err := ibs.FinalizeTx(rules, w); err != nil {
		return nil, fmt.Errorf("finalize block %d: %w", number, err)
	}
	if err := csWriter.WriteChangeSets(); err != nil {
		return nil, fmt.Errorf("write changesets block %d: %w", number, err)
	}
	if err := csWriter.WriteHistory(); err != nil {
		return nil, fmt.Errorf("write history block %d: %w", number, err)
	}
	ibs.ApplyUnchangedAccountRule(cfg, h.Time)
	root := ibs.IntermediateRoot()
	if e.cfg.QMDBHistory {
		if err := qmdbRC.EndBlockHistory(); err != nil {
			return nil, fmt.Errorf("qmdb history end block %d: %w", number, err)
		}
	}
	if e.cfg.QMDBUndoWindow > 0 {
		undo := qmdbRC.TakeUndo()
		if undo == nil {
			undo = &qmdb.BlockUndo{PrevNextSlot: qmdbRC.Tree().NextSlot()}
		}
		if err := rawdb.WriteQMDBUndo(dstTx, number, undo); err != nil {
			return nil, fmt.Errorf("write qmdb undo block %d: %w", number, err)
		}
		if number > uint64(e.cfg.QMDBUndoWindow) {
			if err := rawdb.PruneQMDBUndoBelow(dstTx, number-uint64(e.cfg.QMDBUndoWindow)); err != nil {
				return nil, fmt.Errorf("prune qmdb undo window: %w", err)
			}
		}
	}

	// Header: the block's own transactions and rewards; everything else was
	// fixed before execution (deferred fields carry the parent's result).
	h.TxHash = block.TxRootAt(applied, h.Time)
	wh := hash.DeriveSha(block.Rewards(rewards))
	h.WithdrawalsHash = &wh
	if err := e.p1.Seal(h, pbr); err != nil {
		return nil, err
	}
	blk := block.NewBlockFromReceipt(h, applied, nil, receipts, rewards).(*block.Block)
	if blk.Hash() != h.Hash() {
		return nil, fmt.Errorf("replay profile 1: block %d assembly changed the sealed header", number)
	}
	exec := internal.ExecutedResultFor(cfg, number, root, receipts, gasFigure, internal.ExecutionRequestsHash(requests))

	ce := e.resealer.BuildCE(number, blk.Hash(), h.ReceiptHash)
	if err := rawdb.WriteConsensusEvidence(dstTx, number, ce); err != nil {
		return nil, fmt.Errorf("write consensus evidence block %d: %w", number, err)
	}
	if err := e.writeNewBlock(dstTx, blk, number); err != nil {
		return nil, err
	}
	if len(receipts) > 0 {
		if err := rawdb.WriteReceipts(dstTx, number, receipts); err != nil {
			return nil, fmt.Errorf("write receipts block %d: %w", number, err)
		}
	}
	if err := rawdb.WriteExecutedResult(dstTx, blk.Hash(), exec); err != nil {
		return nil, fmt.Errorf("write executed result block %d: %w", number, err)
	}
	e.p1LastQC = e.p1.Commit(h, exec)

	// Outcome check against the source: the source receipts root in the
	// source's own encoding. A mismatch means the re-derived context changed
	// an outcome (status, gas or logs) of this block.
	if srcHeader != nil {
		if hash.DeriveSha(block.Receipts(receipts)) == srcHeader.ReceiptHash && len(applied) == len(txs) {
			e.stats.ReceiptMatch.Add(1)
		} else {
			e.stats.ReceiptMismatch.Add(1)
		}
		e.stats.BlocksProcessed.Add(1)
		if len(applied) == 0 {
			e.stats.BlocksEmpty.Add(1)
		}
	}
	e.stats.GasUsedTotal.Add(gasFigure)

	var witnessKey [8]byte
	binary.BigEndian.PutUint64(witnessKey[:], number)
	witnessData := e.witnessReader.Serialize()
	if witnessData == nil {
		witnessData = []byte{}
	}
	if err := dstTx.Put(modules.BlockWitness, witnessKey[:], witnessData); err != nil {
		return nil, fmt.Errorf("write block witness %d: %w", number, err)
	}
	return blk, nil
}
