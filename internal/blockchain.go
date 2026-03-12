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

package internal

// This file contains the core lifecycle and chain-insert orchestration logic.
// Types and constants: blockchain_types.go
// Write methods: blockchain_write.go
// Read methods: blockchain_reader.go

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/msg_proto"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/modules/state/snapshot"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Constructor
// =============================================================================

func NewBlockChain(ctx context.Context, genesisBlock block.IBlock, engine consensus.Engine, db kv.RwDB, p2p p2p.P2P, config *params.ChainConfig) (common.IBlockChain, error) {
	c, cancel := context.WithCancel(ctx)
	var current *block.Block

	if err := db.View(c, func(tx kv.Tx) error {
		current = rawdb.ReadCurrentBlock(tx)
		if current == nil {
			current = genesisBlock.(*block.Block)
		}
		return nil
	}); err != nil {
		log.Warnf("failed to read current block from database, using genesis: %v", err)
		current = genesisBlock.(*block.Block)
	}

	blockCache, _ := lru.New[types.Hash, *block.Block](blockCacheLimit)
	futureBlocks, _ := lru.New[types.Hash, *block.Block](maxFutureBlocks)
	receiptsCache, _ := lru.New[types.Hash, []*block.Receipt](receiptsCacheLimit)
	tdCache, _ := lru.New[types.Hash, *uint256.Int](tdCacheLimit)
	numberCache, _ := lru.New[types.Hash, uint64](numberCacheLimit)
	headerCache, _ := lru.New[types.Hash, *block.Header](headerCacheLimit)

	bc := &BlockChain{
		chainConfig:   config,
		genesisBlock:  genesisBlock,
		blocks:        []block.IBlock{},
		ChainDB:       db,
		ctx:           c,
		cancel:        cancel,
		insertLock:    make(chan struct{}, 1),
		peers:         make(map[peer.ID]bool),
		chBlocks:      make(chan block.IBlock, 100),
		errorCh:       make(chan error, 1),
		p2p:           p2p,
		latestBlockCh: make(chan block.IBlock, 50),
		engine:        engine,
		blockCache:    blockCache,
		tdCache:       tdCache,
		futureBlocks:  futureBlocks,
		receiptCache:  receiptsCache,
		numberCache:   numberCache,
		headerCache:   headerCache,
	}

	bc.currentBlock.Store(current)
	headBlockGauge.Set(current.Number64().Uint64())
	headGasUsedGauge.Set(current.GasUsed())
	headGasLimitGauge.Set(current.GasLimit())
	headTransactionsGauge.Set(uint64(len(current.Transactions())))
	bc.forker = NewForkChoice(bc, nil)
	bc.process = NewStateProcessor(config, bc, engine)
	bc.validator = NewBlockValidator(config, bc, engine)

	return bc, nil
}

// SetParallelEVM enables or disables Block-STM parallel transaction execution.
func (bc *BlockChain) SetParallelEVM(enabled bool) {
	bc.parallelEVM = enabled
	if enabled {
		log.Info("Block-STM parallel EVM execution enabled")
	}
}

// SetPrefetch enables or disables state prefetching before block execution.
func (bc *BlockChain) SetPrefetch(enabled bool) {
	bc.prefetchEnabled = enabled
	if enabled {
		log.Info("State prefetching enabled")
	}
}

// SetFreezer attaches the ancient data freezer to the blockchain.
// It accepts any implementation of freezer.FreezerAPI. If the concrete type
// is *freezer.Freezer, an AncientReader is also created for high-level access.
func (bc *BlockChain) SetFreezer(f freezer.FreezerAPI) {
	bc.freezer = f
	if concrete, ok := f.(*freezer.Freezer); ok {
		bc.ancientReader = freezer.NewAncientReader(concrete)
	}
	log.Info("Ancient data freezer attached", "frozen", f.Frozen())
}

// SetExExManager attaches an Execution Extensions manager to the blockchain.
// The manager will receive notifications after each block commit or revert.
func (bc *BlockChain) SetExExManager(m *exex.Manager) {
	bc.exexManager = m
	log.Info("ExEx manager attached to blockchain")
}

// ExExManager returns the attached ExEx manager, or nil if not configured.
func (bc *BlockChain) ExExManager() *exex.Manager {
	return bc.exexManager
}

// SetSnapshotTree attaches a snapshot acceleration tree to the blockchain.
// The tree caches recent block state diffs for fast reads without DB access.
func (bc *BlockChain) SetSnapshotTree(tree *snapshot.Tree) {
	bc.snapshotTree = tree
	log.Info("Snapshot acceleration tree attached")
}

// SnapshotTree returns the attached snapshot tree, or nil if not configured.
func (bc *BlockChain) SnapshotTree() *snapshot.Tree {
	return bc.snapshotTree
}

// SetJMTCommitment enables the Jellyfish Merkle Tree state commitment.
func (bc *BlockChain) SetJMTCommitment(c *commitment.JMTCommitment) {
	bc.jmtCommitment = c
	bc.jmtEnabled = true
	log.Info("JMT state commitment enabled (Blake3)")
}

// JMTCommitment returns the JMT commitment layer, or nil if not enabled.
func (bc *BlockChain) JMTCommitment() *commitment.JMTCommitment {
	return bc.jmtCommitment
}

// IsJMTEnabled returns true if JMT state commitment is active.
func (bc *BlockChain) IsJMTEnabled() bool {
	return bc.jmtEnabled
}

// Freezer returns the attached freezer, or nil if not configured.
func (bc *BlockChain) Freezer() freezer.FreezerAPI {
	return bc.freezer
}

// AncientReader returns the ancient reader, or nil if no freezer is attached.
func (bc *BlockChain) AncientReader() *freezer.AncientReader {
	return bc.ancientReader
}

// =============================================================================
// Lifecycle
// =============================================================================

func (bc *BlockChain) Start() error {
	bc.wg.Add(3)
	go bc.runLoop()
	go bc.updateFutureBlocksLoop()
	go bc.runNewBlockMessage()
	return nil
}

func (bc *BlockChain) Close() error {
	bc.cancel()
	bc.wg.Wait()
	if bc.freezer != nil {
		if err := bc.freezer.Close(); err != nil {
			log.Warn("Failed to close freezer", "err", err)
		}
	}
	return nil
}

// StopInsert signals that block insertion should be aborted.
func (bc *BlockChain) StopInsert() {
	atomic.StoreInt32(&bc.procInterrupt, 1)
}

func (bc *BlockChain) insertStopped() bool {
	return atomic.LoadInt32(&bc.procInterrupt) == 1
}

// =============================================================================
// Background Loops
// =============================================================================

func (bc *BlockChain) runLoop() {
	defer func() {
		bc.cancel()
		bc.StopInsert()
		bc.wg.Done()
	}()

	for {
		select {
		case <-bc.ctx.Done():
			return
		case err, ok := <-bc.errorCh:
			if ok {
				log.Errorf("receive error from action, err:%v", err)
				return
			}
		}
	}
}

func (bc *BlockChain) updateFutureBlocksLoop() {
	futureTimer := time.NewTicker(2 * time.Second)
	defer futureTimer.Stop()
	defer bc.wg.Done()

	for {
		select {
		case <-futureTimer.C:
			bc.processFutureBlocks()
		case <-bc.ctx.Done():
			return
		}
	}
}

func (bc *BlockChain) processFutureBlocks() {
	if bc.futureBlocks.Len() == 0 {
		return
	}

	blocks := make([]block.IBlock, 0, bc.futureBlocks.Len())
	for _, key := range bc.futureBlocks.Keys() {
		if value, ok := bc.futureBlocks.Get(key); ok {
			blocks = append(blocks, value)
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Number64().Cmp(blocks[j].Number64()) < 0
	})

	if len(blocks) == 0 || blocks[0].Number64().Uint64() > bc.CurrentBlock().Number64().Uint64()+1 {
		return
	}

	if n, err := bc.InsertChain(blocks); err != nil {
		log.Warn("insert future block failed", err)
	} else {
		for _, k := range bc.futureBlocks.Keys() {
			bc.futureBlocks.Remove(k)
		}
		if n > 0 {
			log.Infof("insert %d future block success, for %d to %d", n, blocks[0].Number64().Uint64(), blocks[n-1].Number64().Uint64())
		}
	}
}

func (bc *BlockChain) runNewBlockMessage() {
	defer bc.wg.Done()
	newBlockCh := make(chan *msg_proto.NewBlockMessageData, 10)
	sub, err := event.GlobalEvent.Subscribe(newBlockCh)
	if err != nil {
		log.Error("failed to subscribe to NewBlockMessageData events", "err", err)
		return
	}
	defer sub.Unsubscribe()
	db := bc.ChainDB

	for {
		select {
		case <-bc.ctx.Done():
			return
		case err := <-sub.Err():
			log.Errorf("failed subscribe new block at blockchain err :%v", err)
			return
		case blk, ok := <-bc.chBlocks:
			if ok {
				bc.persistBlock(db, blk, "chBlocks")
			}
		case msg, ok := <-newBlockCh:
			if ok {
				var blk block.Block
				if err := blk.FromProtoMessage(msg.Block); err == nil {
					bc.persistBlock(db, &blk, "newBlockCh")
				}
			}
		}
	}
}

// persistBlock writes a block to the database and updates head hash.
func (bc *BlockChain) persistBlock(db kv.RwDB, blk block.IBlock, source string) {
	if err := db.Update(bc.ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, blk.(*block.Block)); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, blk.Hash())
		return nil
	}); err != nil {
		log.Errorf("failed to write block from %s: %v", source, err)
	}
}

// =============================================================================
// Peer and P2P
// =============================================================================

func (bc *BlockChain) AddPeer(hash string, remoteBlock uint64, peerID peer.ID) error {
	if bc.genesisBlock.Hash().String() != hash {
		return errors.New("failed to addPeer, err: genesis block different")
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if _, ok := bc.peers[peerID]; ok {
		return errors.New("failed to addPeer, err: the peer already exists")
	}

	log.Debugf("local height:%d --> remote height: %d", bc.CurrentBlock().Number64(), remoteBlock)
	bc.peers[peerID] = true
	return nil
}

func (bc *BlockChain) NewBlockHandler(payload []byte, peer peer.ID) error {
	var nweBlock msg_proto.NewBlockMessageData
	if err := proto.Unmarshal(payload, &nweBlock); err != nil {
		log.Errorf("failed unmarshal to msg, from peer:%s", peer)
		return err
	}

	var blk block.Block
	if err := blk.FromProtoMessage(nweBlock.GetBlock()); err == nil {
		select {
		case bc.chBlocks <- &blk:
		case <-bc.ctx.Done():
			return bc.ctx.Err()
		}
	}
	return nil
}

func (bc *BlockChain) SealedBlock(b block.IBlock) error {
	return bc.p2p.Broadcast(bc.ctx, b.ToProtoMessage())
}

func (bc *BlockChain) SetEngine(engine interface{}) {
	if e, ok := engine.(consensus.Engine); ok {
		bc.engine = e
	}
}

// =============================================================================
// Chain Queries (simple)
// =============================================================================

func (bc *BlockChain) LatestBlockCh() (block.IBlock, error) {
	select {
	case <-bc.ctx.Done():
		return nil, errors.New("the main chain is closed")
	case blk, ok := <-bc.latestBlockCh:
		if !ok {
			return nil, errors.New("the main chain is closed")
		}
		return blk, nil
	}
}

// InsertHeader is not implemented (light client mode unsupported).
func (bc *BlockChain) InsertHeader(headers []block.IHeader) (int, error) {
	return 0, errors.New("InsertHeader not implemented: light client mode is not yet supported")
}

// InsertBlock is deprecated; use InsertChain.
func (bc *BlockChain) InsertBlock(blocks []block.IBlock, isSync bool) (int, error) {
	return 0, fmt.Errorf("deprecated: use InsertChain instead, got %d blocks", len(blocks))
}

func (bc *BlockChain) skipBlock(err error) bool {
	return errors.Is(err, ErrKnownBlock)
}

// ReorgNeeded determines if a reorg should occur based on block numbers and difficulty.
func (bc *BlockChain) ReorgNeeded(current block.IBlock, header block.IBlock) bool {
	switch current.Number64().Cmp(header.Number64()) {
	case 1:
		return false
	case 0:
		return current.Difficulty().Cmp(header.Difficulty()) < 0
	}
	return true
}

// SetHead resets the canonical head to a specific block number.
func (bc *BlockChain) SetHead(head uint64) error {
	newHeadBlock, err := bc.GetBlockByNumber(uint256.NewInt(head))
	if err != nil {
		return err
	}
	if newHeadBlock == nil {
		return fmt.Errorf("block #%d not found", head)
	}
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		return rawdb.WriteHeadHeaderHash(tx, newHeadBlock.Hash())
	})
}

// AddFutureBlock queues a block for future processing if it is within the allowed window.
// PoS blocks (difficulty == 0) are never queued.
func (bc *BlockChain) AddFutureBlock(blk block.IBlock) error {
	max := uint64(time.Now().Unix() + maxTimeFutureBlocks)
	if blk.Time() > max {
		return fmt.Errorf("future block timestamp %v > allowed %v", blk.Time(), max)
	}
	if blk.Difficulty().Uint64() == 0 {
		return nil
	}
	log.Info("add future block", "hash", blk.Hash(), "number", blk.Number64().Uint64(), "stateRoot", blk.StateRoot(), "txs", len(blk.Body().Transactions()))
	bc.futureBlocks.Add(blk.Hash(), blk.(*block.Block))
	return nil
}

// =============================================================================
// Chain Insertion
// =============================================================================

// InsertChain validates contiguity and inserts blocks into the canonical chain.
func (bc *BlockChain) InsertChain(chain []block.IBlock) (int, error) {
	if len(chain) == 0 {
		return 0, nil
	}
	for i := 1; i < len(chain); i++ {
		blk, prev := chain[i], chain[i-1]
		if blk.Number64().Cmp(uint256.NewInt(0).Add(prev.Number64(), uint256.NewInt(1))) != 0 || blk.ParentHash() != prev.Hash() {
			log.Error("Non contiguous block insert",
				"number", blk.Number64().String(), "hash", blk.Hash(),
				"parent", blk.ParentHash(),
				"prev number", prev.Number64(), "prev hash", prev.Hash(),
			)
			return 0, fmt.Errorf("non contiguous insert: item %d is #%s [%x..], item %d is #%s [%x..] (parent [%x..])", i-1, prev.Number64().String(),
				prev.Hash().Bytes()[:4], i, blk.Number64().String(), blk.Hash().Bytes()[:4], blk.ParentHash().Bytes()[:4])
		}
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	return bc.insertChain(chain)
}

func (bc *BlockChain) insertChain(chain []block.IBlock) (int, error) {
	if bc.insertStopped() {
		return 0, nil
	}

	var (
		stats     = insertStats{startTime: time.Now()}
		lastCanon block.IBlock
	)
	defer func() {
		_ = lastCanon
	}()

	// Parallel header verification
	headers := make([]block.IHeader, len(chain))
	seals := make([]bool, len(chain))
	for i, blk := range chain {
		headers[i] = blk.Header()
		seals[i] = true
	}
	abort, results := bc.engine.VerifyHeaders(bc, headers, seals)
	defer close(abort)

	it := newInsertIterator(chain, results, bc.validator)
	blk, err := it.next()

	// Skip already-known blocks
	if bc.skipBlock(err) {
		var (
			reorg   bool
			current = bc.CurrentBlock()
		)
		for blk != nil && bc.skipBlock(err) {
			reorg, err = bc.forker.ReorgNeeded(current.Header(), blk.Header())
			if err != nil {
				return it.index, err
			}
			if reorg {
				if blk.Number64().Uint64() > current.Number64().Uint64() || bc.GetCanonicalHash(blk.Number64()) != blk.Hash() {
					break
				}
			}
			log.Debug("Ignoring already known block", "number", blk.Number64(), "hash", blk.Hash())
			stats.ignored++
			blk, err = it.next()
		}
		for blk != nil && bc.skipBlock(err) {
			log.Debug("Writing previously known block", "number", blk.Number64(), "hash", blk.Hash())
			if err := bc.writeKnownBlock(nil, blk); err != nil {
				return it.index, err
			}
			lastCanon = blk
			blk, err = it.next()
		}
	}

	switch {
	case errors.Is(err, ErrPrunedAncestor):
		log.Debug("Pruned ancestor, inserting as sidechain", "number", blk.Number64(), "hash", blk.Hash())
		return bc.insertSideChain(blk, it)

	case errors.Is(err, ErrFutureBlock) || (errors.Is(err, ErrUnknownAncestor) && bc.futureBlocks.Contains(it.first().ParentHash())):
		for blk != nil && (it.index == 0 || errors.Is(err, ErrUnknownAncestor)) {
			log.Debug("Future block, postponing import", "number", blk.Number64(), "hash", blk.Hash())
			if err := bc.AddFutureBlock(blk); err != nil {
				return it.index, err
			}
			blk, err = it.next()
		}
		stats.queued += it.processed()
		stats.ignored += it.remaining()
		return it.index, err

	case err != nil && !errors.Is(err, ErrKnownBlock):
		bc.futureBlocks.Remove(blk.Hash())
		stats.ignored += len(it.chain)
		bc.reportBlock(blk, nil, err)
		return it.index, err
	}

	// Process blocks
	evmRecord := func(ctx context.Context, db kv.RwDB, blockNr uint64, f func(tx kv.Tx, ibs *state.IntraBlockState, reader state.StateReader, writer state.WriterWithChangeSets) (map[types.Address]*uint256.Int, error)) (*state.IntraBlockState, map[types.Address]*uint256.Int, error) {
		tx, err := db.BeginRo(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback()

		var stateReader state.StateReader = state.NewPlainStateReader(tx)
		if cache := layered.ExtractCache(db); cache != nil {
			stateReader = state.NewCachedStateReader(stateReader, cache)
		}
		ibs := state.New(stateReader)
		stateWriter := state.NewNoopWriter()

		nopay, err := f(tx, ibs, stateReader, stateWriter)
		if err != nil {
			return nil, nil, err
		}
		return ibs, nopay, nil
	}

	for ; blk != nil && (err == nil || errors.Is(err, ErrKnownBlock)); blk, err = it.next() {
		if bc.insertStopped() {
			log.Debug("Abort during block processing")
			break
		}

		log.Tracef("Current block: number=%v, hash=%v, difficult=%v | Insert block block: number=%v, hash=%v, difficult= %v",
			bc.CurrentBlock().Number64(), bc.CurrentBlock().Hash(), bc.CurrentBlock().Difficulty(), blk.Number64(), blk.Hash(), blk.Difficulty())

		start := time.Now()
		var receipts block.Receipts
		var logs []*block.Log
		var usedGas uint64

		ibs, nopay, err := evmRecord(bc.ctx, bc.ChainDB, blk.Number64().Uint64(), func(tx kv.Tx, ibs *state.IntraBlockState, reader state.StateReader, writer state.WriterWithChangeSets) (map[types.Address]*uint256.Int, error) {
			getHeader := func(hash types.Hash, number uint64) *block.Header {
				return rawdb.ReadHeader(tx, hash, number)
			}
			blockHashFunc := GetHashFn(blk.Header().(*block.Header), getHeader)

			// Start state prefetching in background if cache is available.
			if bc.prefetchEnabled {
				prefetcher := NewStatePrefetcher(bc.chainConfig)
				prefetcher.Prefetch(blk.(*block.Block), reader)
				defer prefetcher.Close()
			}

			pstart := time.Now()
			var nopay map[types.Address]*uint256.Int
			if bc.parallelEVM {
				if sp, ok := bc.process.(*StateProcessor); ok {
					receipts, nopay, logs, usedGas, err = sp.ProcessParallel(blk.(*block.Block), ibs, reader, writer, blockHashFunc)
				} else {
					receipts, nopay, logs, usedGas, err = bc.process.Process(blk.(*block.Block), ibs, reader, writer, blockHashFunc)
				}
			} else {
				receipts, nopay, logs, usedGas, err = bc.process.Process(blk.(*block.Block), ibs, reader, writer, blockHashFunc)
			}
			if err != nil {
				bc.reportBlock(blk, receipts, err)
				return nil, err
			}
			ptime := time.Since(pstart)

			vstart := time.Now()
			if err := bc.validator.ValidateState(blk, ibs, receipts, usedGas); err != nil {
				bc.reportBlock(blk, receipts, err)
				return nil, err
			}
			vtime := time.Since(vstart)

			blockExecutionTimer.Observe(float64(ptime))
			blockValidationTimer.Observe(float64(vtime))
			return nopay, nil
		})
		if err != nil {
			return it.index, err
		}

		wstart := time.Now()
		status, err := bc.writeBlockWithState(blk, receipts, ibs, nopay)
		if err != nil {
			return it.index, err
		}
		blockWriteTimer.Observe(float64(time.Since(wstart)))
		blockInsertTimer.Observe(float64(time.Since(start)))

		stats.processed++
		stats.usedGas += usedGas

		switch status {
		case CanonStatTy:
			log.Trace("Inserted new block ", "number ", blk.Number64(), "hash", blk.Hash(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(),
				"elapsed", time.Since(start).Seconds(), "root", blk.StateRoot())
			if len(logs) > 0 {
				event.GlobalEvent.Send(common.NewLogsEvent{Logs: logs})
			}
			lastCanon = blk
		case SideStatTy:
			log.Debug("Inserted forked block", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		default:
			log.Warn("Inserted block with unknown status", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		}
	}

	// Queue remaining future blocks
	if blk != nil && errors.Is(err, ErrFutureBlock) {
		if err := bc.AddFutureBlock(blk); err != nil {
			return it.index, err
		}
		blk, err = it.next()
		for ; blk != nil && errors.Is(err, ErrUnknownAncestor); blk, err = it.next() {
			if err := bc.AddFutureBlock(blk); err != nil {
				return it.index, err
			}
			stats.queued++
		}
	}
	stats.ignored += it.remaining()
	return it.index, err
}

// =============================================================================
// Sidechain Insertion
// =============================================================================

func (bc *BlockChain) insertSideChain(blk block.IBlock, it *insertIterator) (int, error) {
	var (
		externTd  uint256.Int
		lastBlock = blk
		current   = bc.CurrentBlock()
	)
	err := ErrPrunedAncestor

	for ; blk != nil && errors.Is(err, ErrPrunedAncestor); blk, err = it.next() {
		if number := blk.Number64(); current.Number64().Cmp(number) >= 0 {
			canonical, err := bc.GetBlockByNumber(number)
			if err != nil {
				return 0, err
			}
			if canonical != nil && canonical.Hash() == blk.Hash() {
				pt := bc.GetTd(blk.Hash(), blk.Number64())
				if pt == nil {
					log.Warn("Missing td for canonical block, skipping sidechain processing", "hash", blk.Hash(), "number", blk.Number64())
					return 0, nil
				}
				externTd = *pt
				continue
			}
			if canonical != nil && canonical.StateRoot() == blk.StateRoot() {
				log.Warn("Sidechain ghost-state attack detected", "number", blk.Number64(), "sideroot", blk.StateRoot(), "canonroot", canonical.StateRoot())
				return it.index, errors.New("sidechain ghost-state attack")
			}
		}
		if externTd.Cmp(uint256.NewInt(0)) == 0 {
			parentTd := bc.GetTd(blk.ParentHash(), uint256.NewInt(0).Sub(blk.Number64(), uint256.NewInt(1)))
			if parentTd == nil {
				log.Warn("Missing td for parent block, skipping sidechain processing", "parentHash", blk.ParentHash(), "number", blk.Number64())
				return 0, nil
			}
			externTd = *parentTd
		}
		externTd = *externTd.Add(&externTd, blk.Difficulty())

		if !bc.HasBlock(blk.Hash(), blk.Number64().Uint64()) {
			start := time.Now()
			if err := bc.writeBlockWithTd(blk, externTd.Clone()); err != nil {
				return it.index, err
			}
			log.Debug("Injected sidechain block", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		} else {
			bc.tdCache.Add(blk.Hash(), externTd.Clone())
		}
		lastBlock = blk
	}

	reorg, err := bc.forker.ReorgNeeded(current.Header(), lastBlock.Header())
	if err != nil {
		return it.index, err
	}
	if !reorg {
		localTd := bc.GetTd(current.Hash(), current.Number64())
		log.Info("Sidechain written to disk", "start", it.first().Number64(), "end", it.previous().Number64(), "sidetd", externTd, "localtd", localTd)
		return it.index, err
	}

	var (
		hashes  []types.Hash
		numbers []uint64
	)
	parent := it.previous()
	for parent != nil && !bc.HasState(parent.StateRoot()) {
		hashes = append(hashes, parent.Hash())
		numbers = append(numbers, parent.Number64().Uint64())
		parent = bc.GetHeader(parent.(*block.Header).ParentHash, uint256.NewInt(0).Sub(parent.Number64(), uint256.NewInt(1)))
	}
	if parent == nil {
		return it.index, errors.New("missing parent")
	}

	var blocks []block.IBlock
	for i := len(hashes) - 1; i >= 0; i-- {
		b := bc.GetBlock(hashes[i], numbers[i])
		blocks = append(blocks, b)

		if len(blocks) >= 2048 {
			log.Info("Importing heavy sidechain segment", "blocks", len(blocks), "start", blocks[0].Number64(), "end", b.Number64())
			if _, err := bc.insertChain(blocks); err != nil {
				return 0, err
			}
			blocks = blocks[:0]
			if bc.insertStopped() {
				log.Debug("Abort during blocks processing")
				return 0, nil
			}
		}
	}
	if len(blocks) > 0 {
		log.Info("Importing sidechain segment", "start", blocks[0].Number64(), "end", blocks[len(blocks)-1].Number64())
		return bc.insertChain(blocks)
	}
	return 0, nil
}

// =============================================================================
// Ancestor Recovery
// =============================================================================

func (bc *BlockChain) recoverAncestors(blk block.IBlock) (types.Hash, error) {
	var (
		hashes  []types.Hash
		numbers []uint256.Int
		parent  = blk
	)
	for parent != nil && !bc.HasState(parent.Hash()) {
		hashes = append(hashes, parent.Hash())
		numbers = append(numbers, *parent.Number64())
		if parent.Number64().IsZero() {
			break
		}
		parent = bc.GetBlock(parent.ParentHash(), parent.Number64().Uint64()-1)
	}
	if parent == nil {
		return types.Hash{}, errors.New("missing parent")
	}
	for i := len(hashes) - 1; i >= 0; i-- {
		var b block.IBlock
		if i == 0 {
			b = blk
		} else {
			b = bc.GetBlock(hashes[i], numbers[i].Uint64())
		}
		if _, err := bc.insertChain([]block.IBlock{b}); err != nil {
			return b.ParentHash(), err
		}
	}
	return blk.Hash(), nil
}

// =============================================================================
// Reorg
// =============================================================================

// reorg reconstructs the canonical chain when a fork is detected.
// Instrumented with ReorgAudit for observability.
func (bc *BlockChain) reorg(tx kv.RwTx, oldBlock, newBlock block.IBlock) error {
	audit := GetReorgAudit()
	auditEvent := audit.StartReorg(oldBlock, newBlock)

	var (
		newChain    block.Blocks
		oldChain    block.Blocks
		commonBlock block.IBlock
		deletedTxs  []types.Hash
		addedTxs    []types.Hash
	)
	defer func() {
		audit.EndReorg(auditEvent, commonBlock, oldChain, newChain, len(deletedTxs), len(addedTxs), nil)
	}()

	// Reduce the longer chain to match the shorter one
	if oldBlock.Number64().Uint64() > newBlock.Number64().Uint64() {
		for ; oldBlock != nil && oldBlock.Number64().Uint64() != newBlock.Number64().Uint64(); oldBlock = bc.GetBlock(oldBlock.ParentHash(), oldBlock.Number64().Uint64()-1) {
			oldChain = append(oldChain, oldBlock)
			for _, tx := range oldBlock.Transactions() {
				deletedTxs = append(deletedTxs, tx.Hash())
			}
			if oldBlock.Number64().IsZero() {
				break
			}
		}
	} else {
		for ; newBlock != nil && newBlock.Number64() != oldBlock.Number64(); newBlock = bc.GetBlock(newBlock.ParentHash(), newBlock.Number64().Uint64()-1) {
			newChain = append(newChain, newBlock)
			if newBlock.Number64().IsZero() {
				break
			}
		}
	}
	if oldBlock == nil {
		return errors.New("invalid old chain")
	}
	if newBlock == nil {
		return errors.New("invalid new chain")
	}

	var useExternalTx bool
	var err error
	if tx == nil {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
	} else {
		useExternalTx = true
	}

	// Walk back both sides to find the common ancestor
	for {
		if oldBlock.Hash() == newBlock.Hash() {
			commonBlock = oldBlock
			break
		}
		oldChain = append(oldChain, oldBlock)
		for _, t := range oldBlock.Transactions() {
			deletedTxs = append(deletedTxs, t.Hash())
		}
		newChain = append(newChain, newBlock)

		if oldBlock.Number64().IsZero() || newBlock.Number64().IsZero() {
			return errors.New("reached genesis without finding common ancestor")
		}
		oldBlock = rawdb.ReadBlock(tx, oldBlock.ParentHash(), oldBlock.Number64().Uint64()-1)
		if oldBlock == nil {
			return errors.New("invalid old chain")
		}
		newBlock = rawdb.ReadBlock(tx, newBlock.ParentHash(), newBlock.Number64().Uint64()-1)
		if newBlock == nil {
			return errors.New("invalid new chain")
		}
	}

	// Log the reorg
	if len(oldChain) > 0 && len(newChain) > 0 {
		logFn := log.Info
		msg := "Chain reorg detected"
		if len(oldChain) > 63 {
			msg = "Large chain reorg detected"
			logFn = log.Warn
		}
		logFn(msg, "number", commonBlock.Number64(), "hash", commonBlock.Hash(),
			"drop", len(oldChain), "dropfrom", oldChain[0].Hash(), "add", len(newChain), "addfrom", newChain[0].Hash())
	} else if len(newChain) > 0 {
		log.Info("Extend chain", "add", len(newChain), "number", newChain[0].Number64(), "hash", newChain[0].Hash())
	} else {
		log.Error("Impossible reorg, please file an issue", "oldnum", oldBlock.Number64(), "oldhash", oldBlock.Hash(), "oldblocks", len(oldChain), "newnum", newBlock.Number64(), "newhash", newBlock.Hash(), "newblocks", len(newChain))
	}

	// Insert new chain blocks (except head) in proper order
	for i := len(newChain) - 1; i >= 1; i-- {
		if err := bc.writeHeadBlock(tx, newChain[i]); err != nil {
			return fmt.Errorf("failed to write head block during reorg at %d: %w", newChain[i].Number64().Uint64(), err)
		}
		for _, t := range newChain[i].Transactions() {
			addedTxs = append(addedTxs, t.Hash())
		}
	}

	// Clean up stale tx indexes
	for _, t := range types.HashDifference(deletedTxs, addedTxs) {
		rawdb.DeleteTxLookupEntry(tx, t)
	}

	// Delete stale canonical hash markers
	number := commonBlock.Number64().Uint64()
	if len(newChain) > 1 {
		number = newChain[1].Number64().Uint64()
	}
	for i := number + 1; ; i++ {
		hash, _ := rawdb.ReadCanonicalHash(tx, i)
		if hash == (types.Hash{}) {
			break
		}
		rawdb.TruncateCanonicalHash(tx, i, false)
	}

	if !useExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// reportBlock logs a bad block error.
func (bc *BlockChain) reportBlock(blk block.IBlock, receipts []*block.Receipt, err error) {
	var receiptString string
	for i, receipt := range receipts {
		receiptString += fmt.Sprintf("\t %d: cumulative: %v gas: %v contract: %v status: %v tx: %v logs: %v bloom: %x state: %x\n",
			i, receipt.CumulativeGasUsed, receipt.GasUsed, receipt.ContractAddress.String(),
			receipt.Status, receipt.TxHash.String(), "Logs", receipt.Bloom, receipt.PostState)
	}
	log.Error(fmt.Sprintf(`
########## BAD BLOCK #########

Number: %v
Hash: %#x
%v

Error: %v
##############################
`, blk.Number64().String(), blk.Hash(), receiptString, err))
}

func (bc *BlockChain) syncChain(remoteBlock uint64, peerID peer.ID) {
	log.Debugf("syncChain.......")
}
