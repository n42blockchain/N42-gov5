// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Downloader local catch-up and range execution.

//go:build n42el

package eldevp2p

import (
	"context"
	"os"
	"sort"
	"strconv"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// commitInterval bounds how many blocks accumulate in one batch tx before a
// commit+head-advance. A failure mid-batch rolls back at most this many
// (already-committed checkpoints are durable). Small enough that a kill loses
// little, large enough to amortise fsync over many blocks.
const commitInterval = 256

// executeRange assembles + executes blocks STRICTLY in ascending order starting
// just past local, stopping at the first missing body or execution failure.
// Blocks execute into ONE shared batch tx (a.SetBatchTx) with NO per-block
// commit; the head marker is written INTO that same tx and the batch is
// committed every commitInterval blocks (and once more for the tail). This
// makes block-state and head-advance ATOMIC — a kill can never leave state
// ahead of the head — and replaces per-block fsync with one commit per batch.
// Returns the number of durably-committed blocks and the new (committed) head.
// localCatchUp executes [stateHead+1, localFreezerHead] DIRECTLY from local
// headerc+bodyc — NO peer download, NO MDBX header seeding, NO field
// reconstruction — the "already have the data, don't re-download" fast path for a
// state-height datadir (hashed-canonical / minimal / full) catching up to its own
// frozen tip. Blocks above the local freezer head (the live tip) are left to the
// peer loop.
//
// BLOCKHASH resolves FREEZER-DIRECT: the adapter's headerHashReader returns each
// ancestor's stored h.Hash() straight from headerc (the ethexec/witness-block-
// trace model), so the columnar-stripped ParentHash/UncleHash/Bloom fields are
// irrelevant to BLOCKHASH and no window seed / recompute is needed. The canonical
// index is written straight from the headerc header's stored hash (its stripped
// fields make a recompute wrong; h.Hash() is authoritative). Old finalized blocks
// never reorg → journal disabled. No local bodyc (leaves / archive-cs) → no-op.
func (d *Downloader) localCatchUp(ctx context.Context, hr *ethel.HeaderCompactReader) {
	if hr == nil {
		return // no headerc → no freezer-direct source, nothing to execute locally
	}
	// Do this before the bodyc check: a datadir with no local bodies still needs
	// its canonical head indexed, and that is the only thing standing between a
	// freshly migrated datadir and the peer loop.
	d.seedCanonicalHead(ctx, hr)

	br, err := ethel.OpenBodyCompact(d.freezerDir)
	if err != nil {
		log.Info("eldevp2p: local catch-up skipped — no local bodyc", "dir", d.freezerDir, "err", err)
		return
	}
	defer br.Close()

	local, err := d.localHead(ctx)
	if err != nil {
		return
	}
	start := local

	// The freezer-direct BLOCKHASH source (hr) is installed on the adapter by the
	// coordinator and stays there for the peer path too. Old finalized blocks
	// never reorg — no reverse-diff journal needed.
	d.adapter.SetJournalActive(false)

	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: local catch-up begin tx", "err", err)
		return
	}
	d.adapter.SetBatchTx(tx)
	txOpen := true
	defer func() {
		d.adapter.SetBatchTx(nil)
		if txOpen {
			tx.Rollback()
		}
	}()

	// EIP-2935 (ProcessExecutionBlockStart → vm.StoreParentBlockHash) writes
	// header.ParentHash into STATE, so it's a direct execution input — unlike
	// BLOCKHASH (freezer-direct) it can't be sourced externally. headerc strips
	// ParentHash, so reconstruct it from the prior block's canonical hash (same
	// freezer-direct source: ReadHeader(n-1).Hash()). This is the ONLY field
	// execution needs: UncleHash isn't an execution input, Bloom is filled by
	// execution, and canonical[n] is written from h.Hash() (SetHash) below.
	prevHash := types.Hash{}
	if p, perr := hr.ReadHeader(local); perr == nil && p != nil {
		prevHash = p.Hash()
	}

	committed := local
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n := local + 1
		hdr, herr := hr.ReadHeader(n)
		if herr != nil || hdr == nil {
			break // reached local header head
		}
		db, berr := br.ReadBody(n)
		if berr != nil || db == nil {
			break // reached local body head — hand off to peers
		}
		canonical := hdr.Hash()   // headerc stored canonical hash (SetHash)
		hdr.ParentHash = prevHash // EIP-2935 execution input (see above)
		ws := make([]*api.Withdrawal, len(db.Withdrawals))
		for i, w := range db.Withdrawals {
			ws[i] = &api.Withdrawal{
				Index:          hexutil.Uint64(w.Index),
				ValidatorIndex: hexutil.Uint64(w.Validator),
				Address:        w.Address,
				Amount:         hexutil.Uint64(w.Amount),
			}
		}
		blk, ok := block.NewBlock(hdr, db.Txs).(*block.Block)
		if !ok {
			log.Warn("eldevp2p: local catch-up assemble failed", "block", n)
			break
		}
		okRes, root, xerr := d.adapter.ExecutePayloadFromTrustedColumnar(blk, ws)
		if xerr != nil {
			log.Warn("eldevp2p: local catch-up exec error — handing to peers", "block", n, "err", xerr)
			break
		}
		if !okRes {
			log.Warn("eldevp2p: local catch-up payload invalid — handing to peers",
				"block", n, "computedRoot", root.Hex(), "declaredRoot", hdr.Root.Hex())
			break
		}
		// Write canonical[n] straight from the stored headerc hash — the columnar
		// header's fields are stripped so a recompute would be wrong; h.Hash() is
		// the authoritative canonical value. This is what the peer handoff's
		// parent-link check reads for canonical[localFreezerHead].
		if werr := rawdb.WriteCanonicalHash(tx, canonical, n); werr != nil {
			log.Warn("eldevp2p: local catch-up canonical write failed", "block", n, "err", werr)
			break
		}
		local = n
		prevHash = canonical // next block's EIP-2935 ParentHash input
		if local-committed >= uint64(commitInterval) {
			if err := d.commitBatch(tx, local); err != nil {
				log.Error("eldevp2p: local catch-up commit failed", "head", local, "err", err)
				tx.Rollback()
				txOpen = false
				return
			}
			committed = local
			if tx, err = d.exec.RwDB().BeginRw(ctx); err != nil {
				log.Warn("eldevp2p: local catch-up reopen tx", "err", err)
				txOpen = false
				return
			}
			d.adapter.SetBatchTx(tx)
		}
	}
	// Flush the tail (or roll back an empty tx).
	if local > committed {
		if err := d.commitBatch(tx, local); err != nil {
			log.Error("eldevp2p: local catch-up tail commit failed", "head", local, "err", err)
			tx.Rollback()
			txOpen = false
			return
		}
		txOpen = false
	} else {
		tx.Rollback()
		txOpen = false
	}
	if local > start {
		log.Info("eldevp2p: local catch-up complete (freezer-direct BLOCKHASH, no peer download)",
			"from", start+1, "head", local)
	}
}

func (d *Downloader) executeRange(ctx context.Context, local uint64, headers []*block.Header, bodies map[uint64]devp2p.BlockBody) (uint64, uint64) {
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].Number64().Uint64() < headers[j].Number64().Uint64()
	})

	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: begin batch tx", "err", err)
		return 0, local
	}
	d.adapter.SetBatchTx(tx)
	defer d.adapter.SetBatchTx(nil)

	committed := local // durably committed head
	newHead := local   // executed-but-uncommitted head
	want := local + 1

	// commit cadence (env-overridable; N42_COMMIT_INTERVAL=1 commits every block so
	// the parallel-EVM shadow benchmark reads a current committed parent).
	ci := uint64(commitInterval)
	if v := os.Getenv("N42_COMMIT_INTERVAL"); v != "" {
		if n, e := strconv.ParseUint(v, 10, 64); e == nil && n > 0 {
			ci = n
		}
	}

	for headerIndex, hdr := range headers {
		n := hdr.Number64().Uint64()
		if n < want {
			continue
		}
		if n != want {
			break // gap — refetch next round
		}
		body, ok := bodies[n]
		if !ok {
			break // body missing — refetch next round
		}
		if err := d.validateHeaderLink(tx, hdr, n); err != nil {
			// Parent mismatch at n (canonical(n-1) present but != hdr.parent) means
			// our head n-1 was orphaned by a reorg. Commit the valid work done so
			// far, flush its diffs, and signal the coordinator to unwind the
			// orphaned head via the in-memory reverse-diff journal (minimal mode
			// has no on-disk changesets). This is the depth-1 post-merge case; a
			// deeper reorg unwinds one block per round until the branch links.
			if d.isReorg(tx, hdr, n) {
				if newHead > committed {
					if cerr := d.commitBatch(tx, newHead); cerr == nil {
						committed = newHead
						d.adapter.FlushReorgJournal()
					} else {
						tx.Rollback()
						d.adapter.DiscardReorgJournal()
					}
				} else {
					tx.Rollback()
					d.adapter.DiscardReorgJournal()
				}
				d.reorgPending = true
				log.Warn("eldevp2p: reorg detected — unwinding orphaned head",
					"block", n, "parent", hdr.ParentHash.Hex(), "ourHead", committed)
				return committed - local, committed
			}
			log.Warn("eldevp2p: header link invalid — refetch",
				"block", n, "hash", hdr.Hash().Hex(), "parent", hdr.ParentHash.Hex(), "err", err)
			d.buffer.drop(n)
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		blk, withdrawals, aerr := assembleBlock(hdr, body)
		if aerr != nil {
			log.Warn("eldevp2p: assemble block failed", "block", n, "err", aerr)
			d.buffer.drop(n)
			break
		}
		if got := hash.DeriveShaErigon(rawTxList(unwrapWireTxs(body.Transactions))); got != hdr.TxHash {
			log.Warn("eldevp2p: body txRoot mismatch — refetch",
				"block", n, "got", got.Hex(), "want", hdr.TxHash.Hex())
			d.buffer.drop(n)
			break
		}
		// Withdrawals feed execution but are only committed to by the header,
		// so a body whose withdrawals do not match is the peer's fault:
		// refetch it rather than executing it and rejecting a valid block.
		if !wireWithdrawalsMatchHeader(hdr, body.Withdrawals) {
			log.Warn("eldevp2p: body withdrawalsRoot mismatch — refetch", "block", n)
			d.buffer.drop(n)
			break
		}
		// EIP-7934: from Osaka the block's RLP encoding is capped (geth
		// ValidateBody). A padded body is refetched; a block that is itself
		// oversized is invalid.
		if refetch, serr := checkWireBlockSize(d.exec.ChainConfig(), hdr, body); serr != nil {
			if refetch {
				log.Warn("eldevp2p: oversized body — refetch", "block", n, "err", serr)
				d.buffer.drop(n)
				break
			}
			log.Warn("eldevp2p: block invalid", "block", n, "err", serr)
			d.reportInvalidAncestor(rejectedBranchTip(headers, headerIndex), hdr.ParentHash)
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		// Per-block root verification (ExecutePayloadFromWire computes the
		// incremental root and checks it against the wire header.Root). State
		// commits into the shared batch tx with NO per-block commit.
		ok, root, xerr := d.adapter.ExecutePayloadFromWire(blk, withdrawals)
		if xerr != nil {
			log.Warn("eldevp2p: ExecutePayloadFromWire error", "block", n, "err", xerr)
			d.reportInvalidAncestor(rejectedBranchTip(headers, headerIndex), hdr.ParentHash)
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		if !ok {
			log.Warn("eldevp2p: payload invalid", "block", n,
				"computedRoot", root.Hex(), "declaredRoot", hdr.Root.Hex(),
				"parentHash", hdr.ParentHash.Hex(), "uncleHash", hdr.UncleHash.Hex(),
				"bloomZero", hdr.Bloom == (block.Bloom{}), "hdrHash", hdr.Hash().Hex())
			if os.Getenv("N42_RCPTDIFF") != "" {
				d.dumpMainnetReceipts(ctx, n, hdr.Hash())
			}
			d.reportInvalidAncestor(rejectedBranchTip(headers, headerIndex), hdr.ParentHash)
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		newHead = n
		want++

		if newHead-committed >= ci {
			if err := d.commitBatch(tx, newHead); err != nil {
				log.Error("eldevp2p: batch commit failed", "head", newHead, "err", err)
				tx.Rollback() // never leak the write tx (BeginRw would be MDBX_BUSY forever)
				d.adapter.DiscardReorgJournal()
				return committed - local, committed
			}
			committed = newHead
			d.adapter.FlushReorgJournal() // promote this batch's diffs into the ring
			if tx, err = d.exec.RwDB().BeginRw(ctx); err != nil {
				log.Warn("eldevp2p: reopen batch tx", "err", err)
				return committed - local, committed
			}
			d.adapter.SetBatchTx(tx)
		}
	}

	// Flush the tail (or roll back an empty tx).
	if newHead > committed {
		if err := d.commitBatch(tx, newHead); err != nil {
			log.Error("eldevp2p: batch commit (tail) failed", "head", newHead, "err", err)
			// commitBatch can fail BEFORE tx.Commit (cs freezer flush) — the
			// write tx must not leak or every later BeginRw is MDBX_BUSY.
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		committed = newHead
		d.adapter.FlushReorgJournal()
	} else {
		tx.Rollback()
		d.adapter.DiscardReorgJournal()
	}
	return committed - local, committed
}
