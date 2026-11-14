// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Downloader reorg handling, canonical-head seeding and header link validation.

//go:build n42el

package eldevp2p

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// isReorg reports whether the link failure at block n is a REORG (our committed
// head n-1 is orphaned) rather than a gap/missing-parent. True iff canonical(n-1)
// exists and differs from hdr's parent — i.e. a competing block at n builds on a
// different n-1 than the one we committed.
func (d *Downloader) isReorg(tx kv.Tx, hdr *block.Header, n uint64) bool {
	if n == 0 {
		return false
	}
	parentCanon, err := rawdb.ReadCanonicalHash(tx, n-1)
	if err != nil || parentCanon == (types.Hash{}) {
		return false // no committed n-1 → a gap, not a reorg
	}
	return hdr.ParentHash != parentCanon
}

// handleReorg unwinds exactly ONE orphaned head block using its in-memory
// reverse-diff (restoring the warm overlay to the block's pre-state), clears its
// canonical mapping, and rolls the progress marker back by one. The coordinator
// then re-fetches the canonical block at that height; a depth-1 reorg links
// immediately, a deeper one re-enters here and unwinds another block, bounded by
// the journal depth (N=128, far below finality). This is the minimal-mode
// substitute for erigon-style changeset unwind (geth's in-memory-diff model).
func (d *Downloader) handleReorg(ctx context.Context) {
	d.reorgPending = false
	if d.hashedCanonical {
		// Archive (hashed-canonical) nodes carry per-block changesets — unwind
		// via those instead of the (restart-volatile) in-memory journal.
		d.handleReorgHashed(ctx)
		return
	}
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: reorg begin tx failed", "err", err)
		return
	}
	defer tx.Rollback()

	num, hash, ok, uerr := d.adapter.UnwindOverlayBlock(tx)
	if uerr != nil {
		log.Error("eldevp2p: reorg reverse-diff restore failed", "err", uerr)
		return
	}
	if !ok {
		// Journal exhausted: the reorg is deeper than reorgJournalDepth, i.e.
		// below finality — this does not happen on mainnet. Halt live-follow
		// rather than corrupt state; the operator re-syncs.
		log.Error("eldevp2p: reorg deeper than reverse-diff journal — cannot unwind; halting (re-sync required)",
			"journalDepth", d.adapter.ReorgJournalDepth())
		d.reorgFailed = true
		d.reorgFailReason = "reorg deeper than reverse-diff journal"
		return
	}
	if terr := rawdb.TruncateCanonicalHash(tx, num, true); terr != nil {
		log.Error("eldevp2p: reorg truncate canonical failed", "num", num, "err", terr)
		return
	}
	// commitBatch writes the progress marker (= num-1) and commits the tx, which
	// carries the restored warm state + cleared canonical atomically.
	if cerr := d.commitBatch(tx, num-1); cerr != nil {
		log.Error("eldevp2p: reorg commit failed", "num", num, "err", cerr)
		return
	}
	// The DB is now durable — only now finalize the journal pop. A failure in
	// TruncateCanonicalHash or commitBatch above rolled the tx back with the
	// journal entry intact, so the next attempt restores from it cleanly.
	d.adapter.CommitOverlayUnwind(num)
	d.buffer.prune(num - 1) // drop any stale buffered blocks at/below the unwound head
	log.Info("eldevp2p: reorg unwound one block", "orphan", num, "orphanHash", hash.Hex(),
		"newHead", num-1, "journalDepth", d.adapter.ReorgJournalDepth())
}

// handleReorgHashed unwinds exactly ONE orphaned head block on a
// hashed-canonical (archive) node via its recorded changeset — the freezer
// blob when the cs sink covers it, the legacy MDBX changesets otherwise. The
// unwind rewrites HashedAccounts/HashedStorage + TrieOf* incrementally and
// the recomputed root is verified against the parent header before anything
// commits. Unlike the minimal-mode reverse-diff journal this survives
// restarts, so a tip reorg that lands while the node is down (journalDepth=0
// on boot) no longer halts sync.
func (d *Downloader) handleReorgHashed(ctx context.Context) {
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: reorg begin tx failed", "err", err)
		return
	}
	defer tx.Rollback()

	head := ethel.ReadProgress(tx)
	if head == 0 {
		log.Error("eldevp2p: reorg with no head marker — halting")
		d.reorgFailed = true
		d.reorgFailReason = "reorg with no head marker"
		return
	}
	var provider commitment.BlockChangesProvider
	if d.csSink != nil {
		provider = d.csSink.BlockChangesProvider()
	}
	root, err := commitment.UnwindHashedCanonicalWith(tx, head, head-1, provider)
	if err != nil {
		log.Error("eldevp2p: changeset unwind failed — halting (re-sync required)",
			"block", head, "err", err)
		d.reorgFailed = true
		d.reorgFailReason = fmt.Sprintf("changeset unwind failed at block %d: %v", head, err)
		return
	}
	// Verify the unwound state against the parent header's root — an unwind
	// that does not land exactly on the parent state must not commit.
	parentCanon, err := rawdb.ReadCanonicalHash(tx, head-1)
	if err != nil || parentCanon == (types.Hash{}) {
		log.Error("eldevp2p: reorg unwind: no canonical parent", "block", head-1)
		return
	}
	if hdr := rawdb.ReadHeader(tx, parentCanon, head-1); hdr == nil || root != hdr.Root {
		var want string
		if hdr != nil {
			want = hdr.Root.Hex()
		}
		log.Error("eldevp2p: reorg unwind root mismatch — halting",
			"block", head-1, "computed", root.Hex(), "want", want)
		d.reorgFailed = true
		d.reorgFailReason = fmt.Sprintf("reorg unwind root mismatch at block %d", head-1)
		return
	}
	if terr := rawdb.TruncateCanonicalHash(tx, head, true); terr != nil {
		log.Error("eldevp2p: reorg truncate canonical failed", "num", head, "err", terr)
		return
	}
	// TrieOf* is now at head-1 (the unwind maintained it); rewind the staged
	// Merkle frontier with it so boundary math never underflows.
	if d.staged && d.lastMerkle >= head {
		var v [8]byte
		binary.BigEndian.PutUint64(v[:], head-1)
		if err := tx.Put(progressTable, merkleProgressKey, v[:]); err != nil {
			log.Error("eldevp2p: reorg merkle marker rewind failed", "err", err)
			return
		}
		d.lastMerkle = head - 1
	}
	// The unwind rewrote hashed state outside the read cache's invalidation
	// hooks — drop it before any re-execution reads through it.
	d.adapter.PurgeHashedReadCache()
	// Re-align the cs freezer sink to the unwound head: the replacement block
	// appends at head, which may sit below the tables' previously adopted
	// origin (SetStartItem re-declares it on the emptied tables).
	if d.csSink != nil {
		if rerr := d.csSink.Rewind(head - 1); rerr != nil {
			log.Error("eldevp2p: cs sink rewind failed — halting", "err", rerr)
			return
		}
	}
	if cerr := d.commitBatch(tx, head-1); cerr != nil {
		log.Error("eldevp2p: reorg commit failed", "num", head, "err", cerr)
		return
	}
	d.buffer.prune(head - 1)
	log.Info("eldevp2p: reorg unwound one block via changesets", "orphan", head,
		"newHead", head-1, "root", root.Hex())
}

// seedCanonicalHead indexes canonical[localHead] from the headerc-stored hash
// when the canonical table has no entry for it.
//
// A datadir produced by n42-migrate-reth-hashed (or by snapshot-direct) carries
// state at its head but no canonical index — the migration writes state tables
// and the progress marker, nothing else. localCatchUp then only ever indexes the
// blocks it executes (localHead+1 onward), so canonical[localHead] stays empty,
// and the peer loop's first parent-link check fails forever with "missing
// canonical parent hash at <head>" while the reorder buffer fills and every
// round imports 0. With no local bodyc — an archive datadir seeded with headerc
// alone, per the runbook — localCatchUp returns before executing anything, so
// nothing writes it at all.
//
// The hash comes from headerc's stored h.Hash() (SetHash), which is
// authoritative: the columnar header's stripped fields make a recompute wrong.
// It is not taken on trust — the very next thing that reads it is the
// parent-link check against what peers serve, which reports a mismatch rather
// than accepting a bad value.
func (d *Downloader) seedCanonicalHead(ctx context.Context, hr *ethel.HeaderCompactReader) {
	local, err := d.localHead(ctx)
	if err != nil || local == 0 {
		return
	}
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: canonical head seed — begin tx", "err", err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	existing, err := rawdb.ReadCanonicalHash(tx, local)
	if err != nil {
		log.Warn("eldevp2p: canonical head seed — read", "block", local, "err", err)
		return
	}
	if existing != (types.Hash{}) {
		return // already indexed
	}
	hdr, herr := hr.ReadHeader(local)
	if herr != nil || hdr == nil {
		log.Warn("eldevp2p: canonical head seed — headerc has no header at local head",
			"block", local, "err", herr)
		return
	}
	hash := hdr.Hash()
	if werr := rawdb.WriteCanonicalHash(tx, hash, local); werr != nil {
		log.Warn("eldevp2p: canonical head seed — write", "block", local, "err", werr)
		return
	}
	if cerr := tx.Commit(); cerr != nil {
		log.Warn("eldevp2p: canonical head seed — commit", "block", local, "err", cerr)
		return
	}
	committed = true
	log.Info("eldevp2p: seeded canonical index at local head (migrated datadir had none)",
		"block", local, "hash", hash.Hex())
}

func (d *Downloader) validateHeaderLink(tx kv.Tx, hdr *block.Header, n uint64) error {
	if hdr == nil {
		return errors.New("nil header")
	}
	if n == 0 {
		return nil
	}
	parentHash, err := rawdb.ReadCanonicalHash(tx, n-1)
	if err != nil {
		return fmt.Errorf("read parent canonical hash %d: %w", n-1, err)
	}
	if parentHash == (types.Hash{}) {
		return fmt.Errorf("missing canonical parent hash at %d", n-1)
	}
	if hdr.ParentHash != parentHash {
		return fmt.Errorf("parent mismatch: got %s want %s", hdr.ParentHash.Hex(), parentHash.Hex())
	}
	if existing, err := rawdb.ReadCanonicalHash(tx, n); err != nil {
		return fmt.Errorf("read existing canonical hash %d: %w", n, err)
	} else if existing != (types.Hash{}) && existing != hdr.Hash() {
		return fmt.Errorf("canonical hash collision at %d: existing %s new %s",
			n, existing.Hex(), hdr.Hash().Hex())
	}
	return nil
}
