// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package txindexer keeps the transaction-lookup index off the consensus path:
// newest blocks in memory, older ones in mmap'd RecSplit segments, oldest in
// the MDBX table they were always written to.
//
// It lives outside the blockchain package on purpose. internal/txlookup reaches
// internal/ethel for the eth-el body decoder, and ethel imports the blockchain
// package, so having the blockchain package own this would close an import
// cycle. Keeping the index behind an interface the blockchain package declares
// for itself is also the better shape: committing a block should not need to
// know what an index is.
//
// CommitToCanonical used to write one MDBX row per transaction, keyed by
// transaction hash, inside the write transaction that advances the committed
// head. Measured across 193 committed blocks at a 480M gas ceiling: 2,700 ms in
// mdbx_txn_commit against 149 ms of work, roughly 77% of a 3.53 s block cycle.
// 22,857 hash-keyed rows touch nearly that many separate B-tree leaves and
// every one has to be written out and fsynced.
//
// Writing the same rows off the consensus path did not help -- MDBX has one
// writer, so the index transaction blocked block building instead and
// throughput fell from 6,476 to 4,025 TPS. The write has to stop, not move.
//
// Enabled by N42_TXINDEX_TAIL=1. Off, nothing here runs and the commit path is
// byte-for-byte what it was.

package txindexer

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Chain is the block access the indexer needs.
type Chain interface {
	CurrentBlock() block.IBlock
	GetBlockByNumber(number *uint256.Int) (block.IBlock, error)
}

// Indexer owns the two newer tiers.
type Indexer struct {
	chain       Chain
	db          kv.RwDB
	ctx         context.Context
	txIndexDir  string
	txTail      *txlookup.Tail
	txSegments  *txlookup.Service
	txIndexMu   sync.RWMutex
	txIndexStop chan struct{}
	txIndexDone chan struct{}
}

// New returns an indexer that is not started. Start reports whether the tier
// came up; when it did not, the caller keeps writing the MDBX table.
func New(ctx context.Context, chain Chain, db kv.RwDB, segmentDir string) *Indexer {
	return &Indexer{ctx: ctx, chain: chain, db: db, txIndexDir: segmentDir}
}

// Enabled reports whether the tier is switched on and running.
func (x *Indexer) Enabled() bool { return x != nil && x.txTail != nil }

const (
	// txIndexSealMinTx is how many transactions a segment is worth building
	// for. Small enough that a seal is seconds of background work, large
	// enough that segments do not proliferate.
	txIndexSealMinTx = 1_000_000

	// txIndexSealMaxBlocks seals on block count too, whichever comes first.
	//
	// What a restart costs is set by blocks, not transactions: rebuilding the
	// tail re-reads every unsealed block in full to recover its hashes, and
	// that measured 1.25 GB of allocation -- the largest single source in the
	// node. A transaction threshold alone leaves that unbounded when blocks
	// are small, which is exactly the ordinary case.
	txIndexSealMaxBlocks = 256

	// txIndexKeepBlocks stay in the tail behind the seal point. Sealing reads
	// their hashes and building takes time; a block has to stay answerable
	// throughout, and the newest are the ones most likely to be asked about.
	txIndexKeepBlocks = 64

	// txIndexSealInterval is how often the sealer looks for work.
	txIndexSealInterval = 15 * time.Second

	// txIndexMaxRebuildBlocks bounds what a restart will re-read into the tail
	// before it starts sealing instead. Without a bound a node that was down
	// for a long time would try to hold every unsealed block in memory.
	txIndexMaxRebuildBlocks = 4096
)

// txIndexStartKey records the first block whose lookup entries went to the
// tail instead of the table. Blocks below it are in the table and must stay
// readable from there; blocks above it exist only in the tail or a segment, so
// a restart that could not tell them apart would lose them.
var txIndexStartKey = []byte("txindex-tail-start")

// txIndexEnabled reports whether the tail tier is switched on.
func txIndexEnabled() bool {
	v, _ := strconv.ParseBool(os.Getenv("N42_TXINDEX_TAIL"))
	return v
}

// LookupTx implements rawdb.TxLookupResolver over the tail and the segments.
func (x *Indexer) LookupTx(txHash types.Hash) (uint64, bool) {
	if x.txTail == nil {
		return 0, false
	}
	if n, ok := x.txTail.Lookup(txHash); ok {
		return n, true
	}
	if x.txSegments == nil {
		return 0, false
	}
	n, err := x.txSegments.Lookup(nil, txHash)
	if err != nil || n == nil {
		return 0, false
	}
	return *n, true
}

// txSegmentVerifier rejects a segment's phantom hits.
//
// The segments' existence fingerprint is 8 bits, so roughly one out-of-set
// hash in 256 resolves in a segment that does not hold it, and the
// newest-first probe would stop there — measured at 28 wrong blocks in 7,680
// transactions across three segments. Confirming the candidate against the
// block's own transactions is what makes a multi-segment answer trustworthy.
func (x *Indexer) txSegmentVerifier(blockNum uint64, txHash types.Hash) (uint64, bool) {
	blk, err := x.chain.GetBlockByNumber(uint256.NewInt(blockNum))
	if err != nil || blk == nil {
		return 0, false
	}
	for i, tx := range blk.Transactions() {
		if tx.Hash() == txHash {
			return uint64(i), true
		}
	}
	return 0, false
}

// initTxIndex prepares the tail tier. A failure here leaves the chain on the
// table-only path rather than starting with a lookup that answers nothing.
func (x *Indexer) Start() bool {
	if x == nil || !txIndexEnabled() {
		return false
	}
	if x.txIndexDir == "" {
		log.Warn("txindex tail requested but no segment directory set; staying on the table")
		return false
	}
	if err := os.MkdirAll(x.txIndexDir, 0755); err != nil {
		log.Error("txindex tail disabled: cannot create segment dir", "dir", x.txIndexDir, "err", err)
		return false
	}
	svc, err := txlookup.NewService(x.txIndexDir)
	if err != nil {
		log.Error("txindex tail disabled: cannot open segments", "err", err)
		return false
	}
	svc.SetVerifier(x.txSegmentVerifier)

	head := x.chain.CurrentBlock()
	if head == nil {
		log.Error("txindex tail disabled: no chain head")
		svc.Close()
		return false
	}
	headNum := head.Number64().Uint64()

	start, err := x.txIndexStart(headNum)
	if err != nil {
		log.Error("txindex tail disabled: cannot establish start block", "err", err)
		svc.Close()
		return false
	}
	// Sealed segments already cover up to their end; only what follows needs
	// rebuilding.
	if sealed := txlookup.SealedEnd(x.txIndexDir); sealed > start {
		start = sealed
	}

	x.txSegments = svc
	x.txTail = txlookup.NewTail()
	x.txIndexStop = make(chan struct{})
	x.txIndexDone = make(chan struct{})

	if err := x.rebuildTxTail(start, headNum); err != nil {
		log.Error("txindex tail disabled: rebuild failed", "from", start, "to", headNum, "err", err)
		x.txTail = nil
		x.txSegments = nil
		close(x.txIndexStop)
		close(x.txIndexDone)
		svc.Close()
		return false
	}

	rawdb.SetTxLookupResolver(x)
	go x.txIndexSealLoop()
	log.Info("txindex tail enabled",
		"dir", x.txIndexDir, "segments", svc.SegmentCount(),
		"tailBlocks", x.txTail.Len(), "tailTxs", x.txTail.TxCount(), "from", start, "head", headNum)
	return true
}

// txIndexStart returns the first block whose entries belong to the tail,
// recording it the first time the tier is switched on.
//
// It must be recorded rather than inferred. Blocks written before the switch
// are in the MDBX table; blocks after it are only in the tail or a segment. A
// restart that guessed would either re-read blocks that are already indexed or,
// worse, skip blocks that are indexed nowhere.
func (x *Indexer) txIndexStart(headNum uint64) (uint64, error) {
	var start uint64
	err := x.db.Update(x.ctx, func(tx kv.RwTx) error {
		v, err := tx.GetOne(modules.ChainConfig, txIndexStartKey)
		if err != nil {
			return err
		}
		if len(v) == 8 {
			start = binary.BigEndian.Uint64(v)
			return nil
		}
		start = headNum + 1
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], start)
		return tx.Put(modules.ChainConfig, txIndexStartKey, buf[:])
	})
	return start, err
}

// rebuildTxTail refills the tail from blocks that are already durable.
//
// This is why the tail does not need to be persisted: it is derivable. When
// the gap is larger than the tail should hold, the older part is sealed into
// segments first rather than held in memory.
func (x *Indexer) rebuildTxTail(start, head uint64) error {
	if start > head {
		return nil
	}
	for head-start+1 > txIndexMaxRebuildBlocks {
		end := start + txIndexMaxRebuildBlocks
		log.Info("txindex: sealing a backlog range before rebuilding the tail",
			"from", start, "to", end-1)
		if err := txlookup.BuildSegmentFromSource(x.ctx, x.txIndexDir, start, end, x.blockTxSource); err != nil {
			return fmt.Errorf("seal backlog %d-%d: %w", start, end, err)
		}
		start = end
	}
	for n := start; n <= head; n++ {
		hashes, err := x.blockTxSource(n)
		if err != nil {
			return err
		}
		x.txTail.Add(n, hashes)
	}
	return nil
}

// blockTxSource reads one block's transaction hashes in block order.
func (x *Indexer) blockTxSource(n uint64) ([]types.Hash, error) {
	blk, err := x.chain.GetBlockByNumber(uint256.NewInt(n))
	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, fmt.Errorf("block %d missing while indexing", n)
	}
	txs := blk.Transactions()
	out := make([]types.Hash, len(txs))
	for i, tx := range txs {
		out[i] = tx.Hash()
	}
	return out, nil
}

// noteTxIndex records a committed block's transactions. Called after the
// commit transaction, so it is never on the critical path.
// Add records a committed block. Called after the commit transaction, so it is
// never on the critical path.
func (x *Indexer) Add(number uint64, hashes []types.Hash) {
	if x != nil && x.txTail != nil {
		x.txTail.Add(number, hashes)
	}
}

func (x *Indexer) txIndexSealLoop() {
	defer close(x.txIndexDone)
	t := time.NewTicker(txIndexSealInterval)
	defer t.Stop()
	for {
		select {
		case <-x.txIndexStop:
			return
		case <-x.ctx.Done():
			return
		case <-t.C:
			x.sealTxIndexOnce()
		}
	}
}

// sealTxIndexOnce builds at most one segment. Sealing then dropping, in that
// order and never the reverse: a block dropped from the tail before it is in a
// segment is findable nowhere.
func (x *Indexer) sealTxIndexOnce() {
	start, end, ok := x.txTail.SealRange(txIndexSealMinTx, txIndexSealMaxBlocks, txIndexKeepBlocks)
	if !ok {
		return
	}
	t0 := time.Now()
	if err := txlookup.BuildSegmentFromSource(x.ctx, x.txIndexDir, start, end, x.txTail.Source()); err != nil {
		// Keep the blocks in the tail and retry next tick; they stay
		// answerable in the meantime.
		log.Error("txindex seal failed", "from", start, "to", end, "err", err)
		return
	}
	x.txTail.DropBelow(end)
	if err := x.reopenTxSegments(); err != nil {
		log.Error("txindex: sealed but could not reopen segments", "err", err)
		return
	}
	log.Info("txindex sealed", "from", start, "to", end-1,
		"tailBlocks", x.txTail.Len(), "took", time.Since(t0).Truncate(time.Millisecond))
}

// reopenTxSegments swaps in a reader that includes the segment just written.
func (x *Indexer) reopenTxSegments() error {
	svc, err := txlookup.NewService(x.txIndexDir)
	if err != nil {
		return err
	}
	svc.SetVerifier(x.txSegmentVerifier)
	x.txIndexMu.Lock()
	old := x.txSegments
	x.txSegments = svc
	x.txIndexMu.Unlock()
	if old != nil {
		// Readers hold no reference past a Lookup call, and a lookup that
		// raced the swap already returned.
		go func() {
			time.Sleep(time.Minute)
			old.Close()
		}()
	}
	return nil
}

// stopTxIndex ends the sealer. The tail is not flushed: it is derivable from
// blocks that are already durable, and the next start rebuilds it.
func (x *Indexer) Stop() {
	if x == nil || x.txIndexStop == nil {
		return
	}
	select {
	case <-x.txIndexStop:
	default:
		close(x.txIndexStop)
	}
	<-x.txIndexDone
	rawdb.SetTxLookupResolver(nil)
}
