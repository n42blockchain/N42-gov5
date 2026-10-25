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
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
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
	// explicitEnabled overrides the legacy environment switch only for an
	// execution owner that passed a typed startup option.
	explicitEnabled *bool
	fatalErr        atomic.Pointer[sealFailure]
	resolverRemove  func() bool
	fatalSeal       chan error
	anchorIdentity  types.Hash
}

type sealFailure struct{ err error }

// New returns an indexer that is not started. Start reports whether the tier
// came up; when it did not, the caller keeps writing the MDBX table.
func New(ctx context.Context, chain Chain, db kv.RwDB, segmentDir string) *Indexer {
	return &Indexer{ctx: ctx, chain: chain, db: db, txIndexDir: segmentDir, fatalSeal: make(chan error, 1)}
}

// FatalSeal reports a seal publication failure to an owner, which must stop
// accepting new blocks rather than let a non-sealing in-memory tail grow.
func (x *Indexer) FatalSeal() <-chan error { return x.fatalSeal }

// FatalError is sticky even after FatalSeal has been received. A node cannot
// safely retry startup with an indexer whose sealing already failed.
func (x *Indexer) FatalError() error {
	if x == nil {
		return nil
	}
	if failure := x.fatalErr.Load(); failure != nil {
		return failure.err
	}
	return nil
}

func (x *Indexer) poisonSeal(err error) {
	if x.fatalErr.CompareAndSwap(nil, &sealFailure{err: err}) {
		select {
		case x.fatalSeal <- err:
		default:
		}
	}
}

// OpenExplicit starts the lookup tier under a typed standalone-owner option.
// A prior tail watermark makes an off startup unsafe: those blocks were never
// written to the MDBX TxLookup table, so disabling the tier would hide them.
// The ordinary node uses OpenFromEnvironment for the same restart safety.
func OpenExplicit(ctx context.Context, chain Chain, db kv.RwDB, segmentDir string, enabled bool) (*Indexer, error) {
	return openChecked(ctx, chain, db, segmentDir, enabled, true)
}

// OpenFromEnvironment preserves the ordinary node's legacy environment
// switch, while refusing to start without the tier after a tail watermark
// exists. Once the table was skipped, an unavailable tier is not optional.
func OpenFromEnvironment(ctx context.Context, chain Chain, db kv.RwDB, segmentDir string) (*Indexer, error) {
	return openChecked(ctx, chain, db, segmentDir, txIndexEnabled(), false)
}

func openChecked(ctx context.Context, chain Chain, db kv.RwDB, segmentDir string, enabled, explicit bool) (*Indexer, error) {
	watermark, err := tailWatermark(ctx, db)
	if err != nil {
		return nil, err
	}
	if !enabled {
		if watermark != 0 {
			return nil, fmt.Errorf("transaction lookup tail was previously enabled at block %d; restart with --txindex-tail or rebuild lookup entries", watermark)
		}
		return nil, nil
	}
	x := New(ctx, chain, db, segmentDir)
	if explicit {
		x.explicitEnabled = &enabled
	}
	if !x.Start() {
		return nil, errors.New("transaction lookup tail failed to start; refusing to serve an incomplete lookup index")
	}
	return x, nil
}

func tailWatermark(ctx context.Context, db kv.RwDB) (uint64, error) {
	if db == nil {
		return 0, errors.New("transaction lookup database required")
	}
	var mark uint64
	err := db.View(ctx, func(tx kv.Tx) error {
		value, err := tx.GetOne(modules.ChainConfig, txIndexStartKey)
		if err != nil {
			return err
		}
		if len(value) == 0 {
			return nil
		}
		if len(value) != 8 {
			return errors.New("transaction lookup tail watermark is malformed")
		}
		mark = binary.BigEndian.Uint64(value)
		if mark == 0 {
			return errors.New("transaction lookup tail watermark is zero")
		}
		return nil
	})
	return mark, err
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

	// txIndexKeepTx bounds the same keep-behind by transactions. 64 blocks of
	// 163,000 transactions are 10.4M hashes, ~1.1 GB of heap on the qs fleet
	// (round 35zzo); the newest million transactions stay in the tail either way.
	txIndexKeepTx = 1_000_000

	// txIndexSealMaxPerTick is how many seal batches one tick may run back to
	// back. One segment per 15 s tick sealed ~67k transactions a second while a
	// full-block flood commits ~116k; the loop stops as soon as nothing is
	// sealable.
	txIndexSealMaxPerTick = 8

	// txIndexSealWorkers is how many segments one batch builds concurrently.
	// One builder seals a 1M-transaction segment in ~4 s, ~250k transactions a
	// second, and the qs fleet commits 290k: the tail grew without bound, to
	// 2 GB of a node's heap at +150 s (S97). Builds of different ranges are
	// independent; only appending them to the store is ordered.
	txIndexSealWorkers = 3

	// txIndexSealInterval is how often the sealer looks for work. Short, so
	// the tail holds about one segment's worth beyond the keep-behind: a
	// 15 s tick let 4M transactions pile up between looks at 290k a second.
	txIndexSealInterval = 2 * time.Second

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

// txIndexKeepBlocksVar / txIndexSealMinTxVar (S60, docs/QS_BLOCK_TIME_BUDGET.md
// 6fb) override txIndexKeepBlocks (64) / txIndexSealMinTx (1_000_000) for the
// qs bench's own memory experiments: N42_TXINDEX_KEEP_BLOCKS shrinks how many
// of the newest blocks stay in the in-memory tail behind the seal point
// (64 blocks of 163,000-tx blocks is ~1.1 GB, 6dm); N42_TXINDEX_SEAL_MIN_TX
// exists because SealRangeKeepTx's own sealable range grows as keepBlocks
// shrinks (fewer of the newest blocks are held back), so a smaller keep
// window can also change how many transactions accumulate before a seal
// actually fires -- exposed so keepBlocks can be lowered without also
// having to accept whatever seal cadence that produces. Unset = today,
// byte-for-byte identical (both env reads are no-ops when unset or
// invalid).
func txIndexKeepBlocksVar() int {
	return txIndexEnvInt("N42_TXINDEX_KEEP_BLOCKS", txIndexKeepBlocks)
}

// txIndexSealWorkersVar overrides txIndexSealWorkers (N42_TXINDEX_SEAL_WORKERS).
func txIndexSealWorkersVar() int {
	return txIndexEnvInt("N42_TXINDEX_SEAL_WORKERS", txIndexSealWorkers)
}

func txIndexSealMinTxVar() int {
	return txIndexEnvInt("N42_TXINDEX_SEAL_MIN_TX", txIndexSealMinTx)
}

func txIndexEnvInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// LookupTx implements rawdb.TxLookupResolver over the tail and the segments.
func (x *Indexer) LookupTx(txHash types.Hash) (uint64, bool) {
	n, ok, _ := x.LookupTxWithError(txHash)
	return n, ok
}

// LookupTxWithError preserves physical read failures for rawdb callers.
func (x *Indexer) LookupTxWithError(txHash types.Hash) (uint64, bool, error) {
	if x.txTail == nil {
		return 0, false, nil
	}
	if n, ok := x.txTail.Lookup(txHash); ok {
		return n, true, nil
	}
	// Keep the reader alive for the whole lookup. Reopen and Stop take the
	// write lock before closing a service, so its mmap cannot disappear under
	// an RPC already using it.
	x.txIndexMu.RLock()
	defer x.txIndexMu.RUnlock()
	if x.txSegments == nil {
		return 0, false, nil
	}
	n, err := x.txSegments.Lookup(nil, txHash)
	if err != nil {
		return 0, false, err
	}
	if n == nil {
		return 0, false, nil
	}
	return *n, true, nil
}

// txSegmentVerifier rejects a segment's phantom hits.
//
// The segments' existence fingerprint is 8 bits, so roughly one out-of-set
// hash in 256 resolves in a segment that does not hold it, and the
// newest-first probe would stop there — measured at 28 wrong blocks in 7,680
// transactions across three segments. Confirming the candidate against the
// block's own transactions is what makes a multi-segment answer trustworthy.
func (x *Indexer) txSegmentVerifierWithError(blockNum uint64, txHash types.Hash) (uint64, bool, error) {
	blk, err := x.chain.GetBlockByNumber(uint256.NewInt(blockNum))
	if err != nil {
		return 0, false, fmt.Errorf("read canonical block %d for tx lookup: %w", blockNum, err)
	}
	if blk == nil {
		return 0, false, fmt.Errorf("canonical block %d missing during tx lookup", blockNum)
	}
	for i, tx := range blk.Transactions() {
		if tx.Hash() == txHash {
			return uint64(i), true, nil
		}
	}
	return 0, false, nil
}

func (x *Indexer) txPositionVerifier(blockNum, txIndex uint64, txHash types.Hash) (bool, error) {
	blk, err := x.chain.GetBlockByNumber(uint256.NewInt(blockNum))
	if err != nil {
		return false, fmt.Errorf("read canonical block %d for tx lookup: %w", blockNum, err)
	}
	if blk == nil {
		return false, fmt.Errorf("canonical block %d missing during tx lookup", blockNum)
	}
	txs := blk.Transactions()
	if txIndex >= uint64(len(txs)) {
		return false, fmt.Errorf("canonical block %d transaction index %d out of range", blockNum, txIndex)
	}
	return txs[txIndex].Hash() == txHash, nil
}

type anchorChain interface {
	Config() *params.ChainConfig
	GenesisBlock() block.IBlock
}

func (x *Indexer) establishAnchorIdentity() error {
	chain, ok := x.chain.(anchorChain)
	if !ok || chain.Config() == nil || chain.GenesisBlock() == nil {
		return fmt.Errorf("explicit txindex owner requires chain config and genesis block")
	}
	identity, err := txlookup.CanonicalChainIdentity(chain.Config().ChainID, chain.GenesisBlock().Hash())
	if err != nil {
		return err
	}
	genesis, err := x.canonicalHash(0)
	if err != nil {
		return err
	}
	if genesis != chain.GenesisBlock().Hash() {
		return fmt.Errorf("canonical genesis differs from owner genesis")
	}
	x.anchorIdentity = identity
	return nil
}

func (x *Indexer) canonicalHash(n uint64) (types.Hash, error) {
	if x.db == nil {
		return types.Hash{}, fmt.Errorf("canonical database unavailable")
	}
	var hash types.Hash
	err := x.db.View(x.ctx, func(tx kv.Tx) error {
		var err error
		hash, err = rawdb.ReadCanonicalHash(tx, n)
		return err
	})
	return hash, err
}

func (x *Indexer) durableCanonicalHead() (uint64, error) {
	if x.db == nil {
		return 0, fmt.Errorf("canonical database unavailable")
	}
	var head uint64
	err := x.db.View(x.ctx, func(tx kv.Tx) error {
		value, err := tx.GetOne(modules.HeadBlockKey, []byte("HotStuffCommittedHead"))
		if err != nil {
			return err
		}
		if len(value) == 0 {
			chain, ok := x.chain.(anchorChain)
			if !ok || chain.GenesisBlock() == nil {
				return fmt.Errorf("durable committed head missing")
			}
			genesis := chain.GenesisBlock().Hash()
			headValue, err := tx.GetOne(modules.HeadBlockKey, []byte(modules.HeadBlockKey))
			if err != nil {
				return err
			}
			canonical, err := rawdb.ReadCanonicalHash(tx, 0)
			if err != nil {
				return err
			}
			genesisNumber, err := tx.GetOne(modules.HeaderNumber, genesis[:])
			if err != nil {
				return err
			}
			if len(headValue) != len(genesis) || types.BytesToHash(headValue) != genesis || canonical != genesis {
				return fmt.Errorf("durable committed head missing outside genesis")
			}
			if len(genesisNumber) != 8 || binary.BigEndian.Uint64(genesisNumber) != 0 {
				return fmt.Errorf("genesis header number missing")
			}
			return nil
		}
		if len(value) != 32 {
			return fmt.Errorf("durable committed head malformed")
		}
		hash := types.BytesToHash(value)
		if hash == (types.Hash{}) {
			return fmt.Errorf("durable committed head zero")
		}
		value, err = tx.GetOne(modules.HeaderNumber, hash[:])
		if err != nil {
			return err
		}
		if len(value) != 8 {
			return fmt.Errorf("durable committed head number missing")
		}
		head = binary.BigEndian.Uint64(value)
		canonical, err := rawdb.ReadCanonicalHash(tx, head)
		if err != nil {
			return err
		}
		if canonical != hash {
			return fmt.Errorf("durable committed head is not canonical")
		}
		return nil
	})
	return head, err
}

func (x *Indexer) segmentAnchor(end uint64) (txlookup.SegmentAnchor, error) {
	head, err := x.durableCanonicalHead()
	if err != nil {
		return txlookup.SegmentAnchor{}, err
	}
	if end == 0 || end-1 > head {
		return txlookup.SegmentAnchor{}, fmt.Errorf("txindex segment end %d exceeds durable canonical head %d", end, head)
	}
	hash, err := x.canonicalHash(end - 1)
	if err != nil {
		return txlookup.SegmentAnchor{}, err
	}
	if hash == (types.Hash{}) {
		return txlookup.SegmentAnchor{}, fmt.Errorf("canonical block %d missing", end-1)
	}
	return txlookup.SegmentAnchor{Identity: x.anchorIdentity, EndCanonicalHash: hash}, nil
}

func (x *Indexer) checkBuiltAnchor(seg *txlookup.BuiltSegment) error {
	anchor, ok := seg.Anchor()
	if !ok || anchor.Identity != x.anchorIdentity {
		return fmt.Errorf("built segment missing owner anchor")
	}
	head, err := x.durableCanonicalHead()
	if err != nil {
		return err
	}
	if seg.EndBlock == 0 || seg.EndBlock-1 > head {
		return fmt.Errorf("built segment exceeds durable canonical head")
	}
	actual, err := x.canonicalHash(seg.EndBlock - 1)
	if err != nil {
		return err
	}
	if actual == (types.Hash{}) || actual != anchor.EndCanonicalHash {
		return fmt.Errorf("canonical hash changed before txindex publication at %d", seg.EndBlock-1)
	}
	return nil
}

// initTxIndex prepares the tail tier. A failure here leaves the chain on the
// table-only path rather than starting with a lookup that answers nothing.
// warnDisabledWithWatermark shouts when the node boots WITHOUT the tail tier
// although a watermark says the tier was on before.
//
// That combination silently loses transaction lookups. Blocks produced while
// the tier was on went to the volatile tail, which is deliberately not
// persisted because the next start re-derives it -- but only if the tier is
// on. Booting with it off skips the rebuild, and the window between the last
// sealed segment and the previous shutdown is then indexed NOWHERE: not in a
// segment, and not in the MDBX table either, because the table path was
// bypassed while the tier was live. Re-enabling later does not fix it on its
// own -- the rebuild resumes from SealedEnd, which has since moved past the
// window.
//
// Cheap to detect, invisible otherwise: it cost a ~330-block hole in
// eth_getTransactionByHash across six nodes before anyone noticed.
func (x *Indexer) warnDisabledWithWatermark() {
	if x.db == nil {
		return
	}
	var mark uint64
	if err := x.db.View(x.ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.ChainConfig, txIndexStartKey)
		if err == nil && len(v) == 8 {
			mark = binary.BigEndian.Uint64(v)
		}
		return err
	}); err != nil || mark == 0 {
		return
	}
	log.Warn("txindex tail tier OFF although this node ran with it ON before "+
		"(N42_TXINDEX_TAIL unset?): transaction lookups above the watermark may "+
		"be answerable nowhere. Re-enable it, or re-index the affected range.",
		"watermark", mark)
}
func (x *Indexer) Start() bool {
	if x == nil {
		return false
	}
	enabled := txIndexEnabled()
	if x.explicitEnabled != nil {
		enabled = *x.explicitEnabled
	}
	if !enabled {
		x.warnDisabledWithWatermark()
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
	head := x.chain.CurrentBlock()
	if head == nil {
		log.Error("txindex tail disabled: no chain head")
		return false
	}
	headNum := head.Number64().Uint64()
	if headNum == ^uint64(0) {
		log.Error("txindex tail disabled: canonical head cannot be incremented")
		return false
	}
	if x.explicitEnabled != nil {
		durable, err := x.durableCanonicalHead()
		if err != nil {
			log.Error("txindex tail disabled: no durable canonical head", "err", err)
			return false
		}
		headNum = durable
		if headNum == ^uint64(0) {
			log.Error("txindex tail disabled: durable canonical head cannot be incremented")
			return false
		}
		if err := x.establishAnchorIdentity(); err != nil {
			log.Error("txindex tail disabled: cannot establish canonical identity", "err", err)
			return false
		}
	}
	var svc *txlookup.Service
	var err error
	if x.explicitEnabled != nil {
		svc, err = txlookup.NewAnchoredService(x.txIndexDir, x.anchorIdentity, x.canonicalHash, headNum)
	} else {
		svc, err = txlookup.NewLegacyService(x.txIndexDir)
	}
	if err != nil {
		log.Error("txindex tail disabled: cannot open segments", "err", err)
		return false
	}
	svc.SetVerifierWithError(x.txSegmentVerifierWithError)
	svc.SetPositionVerifier(x.txPositionVerifier)

	start, err := x.txIndexStart(headNum)
	if err != nil {
		log.Error("txindex tail disabled: cannot establish start block", "err", err)
		svc.Close()
		return false
	}
	// Sealed segments already cover up to their end; only what follows needs
	// rebuilding.
	first, sealed := svc.Coverage()
	if sealed > headNum+1 || (sealed > 0 && first > start) {
		log.Error("txindex tail disabled: segment inventory does not cover the watermark/head",
			"first", first, "sealed", sealed, "start", start, "head", headNum)
		svc.Close()
		return false
	}
	if sealed > start {
		start = sealed
	}

	x.txSegments = svc
	x.txTail = txlookup.NewTail()
	x.txIndexStop = make(chan struct{})
	x.txIndexDone = make(chan struct{})

	sealedBacklog, err := x.rebuildTxTail(start, headNum)
	if err != nil {
		log.Error("txindex tail disabled: rebuild failed", "from", start, "to", headNum, "err", err)
		x.txTail = nil
		x.txSegments = nil
		close(x.txIndexStop)
		close(x.txIndexDone)
		svc.Close()
		return false
	}
	// The backlog segments were written after svc enumerated the directory, so
	// svc cannot see them. Swap in a reader that can -- before the resolver is
	// published, so no lookup is ever served against the stale list.
	if sealedBacklog {
		if rerr := x.reopenTxSegments(); rerr != nil {
			log.Error("txindex tail disabled: cannot reopen segments after backlog seal",
				"err", rerr)
			x.txTail = nil
			x.txSegments = nil
			close(x.txIndexStop)
			close(x.txIndexDone)
			svc.Close()
			return false
		}
	}

	remove, ok := rawdb.TryInstallTxLookupResolver(x)
	if !ok {
		log.Error("txindex tail disabled: another lookup resolver owns this process")
		active := x.txSegments
		x.txTail = nil
		x.txSegments = nil
		close(x.txIndexStop)
		close(x.txIndexDone)
		active.Close()
		return false
	}
	x.resolverRemove = remove
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
			if start == 0 || start > headNum+1 {
				return fmt.Errorf("invalid transaction lookup watermark %d for head %d", start, headNum)
			}
			return nil
		}
		if len(v) != 0 {
			return fmt.Errorf("malformed transaction lookup watermark: %d bytes", len(v))
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
// It returns whether it sealed any backlog segment. Those segments are
// written AFTER x.txSegments was opened, so the caller must reopen the
// segment reader before serving lookups -- without that the freshly sealed
// range is findable nowhere until the NEXT restart happens to enumerate it.
// Observed live: a ~330-block window of eth_getTransactionByHash returning
// null on six nodes, healed only by a third restart.
func (x *Indexer) rebuildTxTail(start, head uint64) (sealed bool, err error) {
	if start > head {
		return false, nil
	}
	for head-start+1 > txIndexMaxRebuildBlocks {
		end := start + txIndexMaxRebuildBlocks
		log.Info("txindex: sealing a backlog range before rebuilding the tail",
			"from", start, "to", end-1)
		var err error
		if x.explicitEnabled != nil {
			var anchor txlookup.SegmentAnchor
			anchor, err = x.segmentAnchor(end)
			if err == nil {
				err = txlookup.BuildSegmentFromSourceAnchored(x.ctx, x.txIndexDir, start, end, x.blockTxSource, anchor, x.checkBuiltAnchor)
			}
		} else {
			err = txlookup.BuildSegmentFromSource(x.ctx, x.txIndexDir, start, end, x.blockTxSource)
		}
		if err != nil {
			return sealed, fmt.Errorf("seal backlog %d-%d: %w", start, end, err)
		}
		sealed = true
		start = end
	}
	for n := start; n <= head; n++ {
		hashes, err := x.blockTxSource(n)
		if err != nil {
			return sealed, err
		}
		x.txTail.Add(n, hashes)
	}
	return sealed, nil
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
// never on the critical path. The tail takes ownership of hashes: the caller
// builds a fresh slice per block and does not touch it again.
func (x *Indexer) Add(number uint64, hashes []types.Hash) {
	if x != nil && x.txTail != nil {
		x.txTail.AddOwned(number, hashes)
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
			for i := 0; i < txIndexSealMaxPerTick && x.sealTxIndexOnce(); i++ {
				select {
				case <-x.txIndexStop:
					return
				case <-x.ctx.Done():
					return
				default:
				}
			}
		}
	}
}

// sealTxIndexOnce builds up to txIndexSealWorkers segments concurrently,
// appends them in block order and reports whether it appended any.
// Sealing then dropping, in that order and never the reverse: a block dropped
// from the tail before it is in a segment is findable nowhere.
func (x *Indexer) sealTxIndexOnce() bool {
	if x.FatalError() != nil {
		return false
	}
	ranges := x.txTail.SealRanges(txIndexSealMinTxVar(), txIndexSealMaxBlocks, txIndexKeepBlocksVar(), txIndexKeepTx, txIndexSealWorkersVar())
	if len(ranges) == 0 {
		return false
	}
	t0 := time.Now()
	built := make([]*txlookup.BuiltSegment, len(ranges))
	errs := make([]error, len(ranges))
	src := x.txTail.Source()
	anchors := make([]txlookup.SegmentAnchor, len(ranges))
	if x.explicitEnabled != nil {
		var err error
		for i, r := range ranges {
			anchors[i], err = x.segmentAnchor(r.End)
			if err != nil {
				x.poisonSeal(err)
				return false
			}
		}
		// The explicit owner has append-only canonical history. Pin the tail
		// after reading anchors and recheck each anchor before publication.
		src, err = x.txTail.SnapshotSource(ranges[0].Start, ranges[len(ranges)-1].End)
		if err != nil {
			x.poisonSeal(err)
			return false
		}
	}
	var wg sync.WaitGroup
	for i, r := range ranges {
		wg.Add(1)
		go func(i int, r txlookup.BlockRange, anchor txlookup.SegmentAnchor) {
			defer wg.Done()
			if x.explicitEnabled != nil {
				built[i], errs[i] = txlookup.BuildSegmentFileAnchored(x.ctx, x.txIndexDir, r.Start, r.End, src, anchor)
			} else {
				built[i], errs[i] = txlookup.BuildSegmentFile(x.ctx, x.txIndexDir, r.Start, r.End, src)
			}
		}(i, r, anchors[i])
	}
	wg.Wait()
	// A failed build can be an out-of-space condition. Stop this owner instead
	// of retrying forever while the unsealed tail grows without a bound.
	for i, err := range errs {
		if err != nil {
			log.Error("txindex seal failed", "from", ranges[i].Start, "to", ranges[i].End, "err", err)
			for _, b := range built {
				b.Discard()
			}
			// An uncommitted build is safe to retry for the ordinary node,
			// preserving its existing sealer behavior. The explicit owner must
			// stop rather than let a permanently failing tail grow unbounded.
			if x.explicitEnabled != nil {
				x.poisonSeal(err)
			}
			return false
		}
	}
	var n int
	var err error
	if x.explicitEnabled != nil {
		n, err = txlookup.CommitBuiltSegmentsChecked(x.txIndexDir, built, x.checkBuiltAnchor)
	} else {
		n, err = txlookup.CommitBuiltSegments(x.txIndexDir, built)
	}
	if err != nil {
		log.Error("txindex seal: append failed", "appended", n, "of", len(built), "err", err)
		// A failed sync or rename has an ambiguous publication outcome. Keep
		// the full tail answerable and do not append the same range again.
		x.poisonSeal(err)
		return false
	}
	if n == 0 {
		return false
	}
	end := ranges[n-1].End
	if err := x.reopenTxSegments(); err != nil {
		log.Error("txindex: sealed but could not reopen segments", "err", err)
		x.poisonSeal(err)
		return false
	}
	// The new reader now covers every block being dropped. A concurrent
	// lookup either finds it in the tail or in the published segment reader.
	x.txTail.DropBelow(end)
	log.Info("txindex sealed", "from", ranges[0].Start, "to", end-1, "segments", n,
		"tailBlocks", x.txTail.Len(), "tailTxs", x.txTail.TxCount(), "took", time.Since(t0).Truncate(time.Millisecond))
	return n == len(ranges)
}

// reopenTxSegments swaps in a reader that includes the segment just written.
func (x *Indexer) reopenTxSegments() error {
	var svc *txlookup.Service
	var err error
	if x.explicitEnabled != nil {
		head, herr := x.durableCanonicalHead()
		if herr != nil {
			return herr
		}
		svc, err = txlookup.NewAnchoredService(x.txIndexDir, x.anchorIdentity, x.canonicalHash, head)
	} else {
		svc, err = txlookup.NewLegacyService(x.txIndexDir)
	}
	if err != nil {
		return err
	}
	svc.SetVerifierWithError(x.txSegmentVerifierWithError)
	svc.SetPositionVerifier(x.txPositionVerifier)
	x.txIndexMu.Lock()
	old := x.txSegments
	x.txSegments = svc
	if old != nil {
		// Acquiring the write lock waited for every lookup using old. New
		// lookups will observe svc, so old can now be closed synchronously.
		old.Close()
	}
	x.txIndexMu.Unlock()
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
	if x.resolverRemove != nil {
		x.resolverRemove()
	}
	// Release the segment mmaps. Detaching the resolver prevents new calls;
	// the write lock waits for calls that loaded the resolver just before it
	// was detached.
	x.txIndexMu.Lock()
	svc := x.txSegments
	x.txSegments = nil
	if svc != nil {
		svc.Close()
	}
	x.txIndexMu.Unlock()
}
