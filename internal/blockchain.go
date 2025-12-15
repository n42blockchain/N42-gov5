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

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/contracts/deposit"
	"github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/internal/p2p"
	"google.golang.org/protobuf/proto"

	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/api/protocol/msg_proto"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
)

var (
	ErrKnownBlock           = errors.New("block already known")
	ErrUnknownAncestor      = errors.New("unknown ancestor")
	ErrPrunedAncestor       = errors.New("pruned ancestor")
	ErrFutureBlock          = errors.New("block in the future")
	ErrInvalidNumber        = errors.New("invalid block number")
	ErrInvalidTerminalBlock = errors.New("insertion is interrupted")
	errChainStopped         = errors.New("blockchain is stopped")
	errInsertionInterrupted = errors.New("insertion is interrupted")
	errBlockDoesNotExist    = errors.New("block does not exist in blockchain")
)
var (
	headBlockGauge       = prometheus.GetOrCreateCounter("chain_head_block", true)
	blockInsertTimer     = prometheus.GetOrCreateHistogram("chain_inserts")
	blockValidationTimer = prometheus.GetOrCreateHistogram("chain_validation")
	blockExecutionTimer  = prometheus.GetOrCreateHistogram("chain_execution")
	blockWriteTimer      = prometheus.GetOrCreateHistogram("chain_write")
)

type WriteStatus byte

const (
	NonStatTy   WriteStatus = iota //
	CanonStatTy                    //
	SideStatTy                     //
)

const (
	//maxTimeFutureBlocks
	blockCacheLimit     = 1024
	receiptsCacheLimit  = 32
	maxFutureBlocks     = 256
	maxTimeFutureBlocks = 5 * 60 // 5 min

	headerCacheLimit = 1024
	tdCacheLimit     = 1024
	numberCacheLimit = 2048
)

type BlockChain struct {
	chainConfig  *params.ChainConfig
	ctx          context.Context
	cancel       context.CancelFunc
	genesisBlock block.IBlock
	blocks       []block.IBlock
	headers      []block.IHeader
	currentBlock atomic.Pointer[block.Block]
	//state        *statedb.StateDB
	ChainDB kv.RwDB
	engine  consensus.Engine

	insertLock    chan struct{}
	latestBlockCh chan block.IBlock
	lock          sync.Mutex

	peers map[peer.ID]bool

	chBlocks chan block.IBlock

	p2p p2p.P2P

	errorCh chan error

	process Processor

	wg sync.WaitGroup //

	procInterrupt int32 // insert chain
	futureBlocks  *lru.Cache[types.Hash, *block.Block]
	receiptCache  *lru.Cache[types.Hash, []*block.Receipt]
	blockCache    *lru.Cache[types.Hash, *block.Block]

	headerCache *lru.Cache[types.Hash, *block.Header]
	numberCache *lru.Cache[types.Hash, uint64]
	tdCache     *lru.Cache[types.Hash, *uint256.Int]

	forker    *ForkChoice
	validator Validator
}

type insertStats struct {
	queued, processed, ignored int
	usedGas                    uint64
	lastIndex                  int
	startTime                  time.Time
}

func (bc *BlockChain) Engine() consensus.Engine {
	return bc.engine
}

func NewBlockChain(ctx context.Context, genesisBlock block.IBlock, engine consensus.Engine, db kv.RwDB, p2p p2p.P2P, config *params.ChainConfig) (common.IBlockChain, error) {
	c, cancel := context.WithCancel(ctx)
	var current *block.Block
	_ = db.View(c, func(tx kv.Tx) error {
		current = rawdb.ReadCurrentBlock(tx)
		if current == nil {
			current = genesisBlock.(*block.Block)
		}
		return nil
	})

	blockCache, _ := lru.New[types.Hash, *block.Block](blockCacheLimit)
	futureBlocks, _ := lru.New[types.Hash, *block.Block](maxFutureBlocks)
	receiptsCache, _ := lru.New[types.Hash, []*block.Receipt](receiptsCacheLimit)
	tdCache, _ := lru.New[types.Hash, *uint256.Int](tdCacheLimit)
	numberCache, _ := lru.New[types.Hash, uint64](numberCacheLimit)
	headerCache, _ := lru.New[types.Hash, *block.Header](headerCacheLimit)
	bc := &BlockChain{
		chainConfig:  config, // Chain & network configuration
		genesisBlock: genesisBlock,
		blocks:       []block.IBlock{},
		//currentBlock:  current,
		ChainDB:       db,
		ctx:           c,
		cancel:        cancel,
		insertLock:    make(chan struct{}, 1),
		peers:         make(map[peer.ID]bool),
		chBlocks:      make(chan block.IBlock, 100),
		errorCh:       make(chan error),
		p2p:           p2p,
		latestBlockCh: make(chan block.IBlock, 50),
		engine:        engine,
		blockCache:    blockCache,
		tdCache:       tdCache,
		futureBlocks:  futureBlocks,
		receiptCache:  receiptsCache,

		numberCache: numberCache,
		headerCache: headerCache,
	}

	bc.currentBlock.Store(current)
	headBlockGauge.Set(current.Number64().Uint64())
	bc.forker = NewForkChoice(bc, nil)
	//bc.process = avm.NewVMProcessor(ctx, bc, engine)
	bc.process = NewStateProcessor(config, bc, engine)
	bc.validator = NewBlockValidator(config, bc, engine)

	return bc, nil
}

func (bc *BlockChain) Config() *params.ChainConfig {
	return bc.chainConfig
}

func (bc *BlockChain) CurrentBlock() block.IBlock {
	return bc.currentBlock.Load()
}

func (bc *BlockChain) Blocks() []block.IBlock {
	return bc.blocks
}

func (bc *BlockChain) InsertHeader(headers []block.IHeader) (int, error) {
	// TODO: Implement header-only insertion for light client support
	return 0, errors.New("InsertHeader not implemented")
}

func (bc *BlockChain) GenesisBlock() block.IBlock {
	return bc.genesisBlock
}

func (bc *BlockChain) Start() error {
	bc.wg.Add(3)
	go bc.runLoop()
	go bc.updateFutureBlocksLoop()
	return nil
}

func (bc *BlockChain) AddPeer(hash string, remoteBlock uint64, peerID peer.ID) error {
	if bc.genesisBlock.Hash().String() != hash {
		return fmt.Errorf("failed to addPeer, err: genesis block different")
	}
	if _, ok := bc.peers[peerID]; ok {
		return fmt.Errorf("failed to addPeer, err: the peer already exists")
	}

	log.Debugf("local heigth:%d --> remote height: %d", bc.CurrentBlock().Number64(), remoteBlock)

	bc.peers[peerID] = true
	//if remoteBlock > bc.currentBlock.Number64().Uint64() {
	//	bc.syncChain(remoteBlock, peerID)
	//}

	return nil
}

func (bc *BlockChain) GetReceipts(blockHash types.Hash) (block.Receipts, error) {
	rtx, err := bc.ChainDB.BeginRo(bc.ctx)
	if err != nil {
		return nil, err
	}
	defer rtx.Rollback()
	return rawdb.ReadReceiptsByHash(rtx, blockHash)
}

func (bc *BlockChain) GetLogs(blockHash types.Hash) ([][]*block.Log, error) {
	receipts, err := bc.GetReceipts(blockHash)
	if err != nil {
		return nil, err
	}

	logs := make([][]*block.Log, len(receipts))
	for i, receipt := range receipts {
		logs[i] = receipt.Logs
	}
	return logs, nil
}

// InsertBlock inserts blocks into the chain.
// Deprecated: Use InsertChain instead. This method is kept for interface compatibility.
func (bc *BlockChain) InsertBlock(blocks []block.IBlock, isSync bool) (int, error) {
	return 0, fmt.Errorf("deprecated: use InsertChain instead, got %d blocks", len(blocks))
}

func (bc *BlockChain) LatestBlockCh() (block.IBlock, error) {
	select {
	case <-bc.ctx.Done():
		return nil, fmt.Errorf("the main chain is closed")
	case blk, ok := <-bc.latestBlockCh:
		if !ok {
			return nil, fmt.Errorf("the main chain is closed")
		}

		return blk, nil
	}
}

func (bc *BlockChain) runLoop() {
	defer func() {
		bc.wg.Done()
		bc.cancel()
		bc.StopInsert()
		close(bc.errorCh)
		bc.wg.Wait()
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

// updateFutureBlocksLoop
func (bc *BlockChain) updateFutureBlocksLoop() {
	futureTimer := time.NewTicker(2 * time.Second)
	defer futureTimer.Stop()
	defer bc.wg.Done()
	for {
		select {
		case <-futureTimer.C:
			if bc.futureBlocks.Len() > 0 {
				blocks := make([]block.IBlock, 0, bc.futureBlocks.Len())
				for _, key := range bc.futureBlocks.Keys() {
					if value, ok := bc.futureBlocks.Get(key); ok {
						blocks = append(blocks, value)
					}
				}
				sort.Slice(blocks, func(i, j int) bool {
					return blocks[i].Number64().Cmp(blocks[j].Number64()) < 0
				})

				if blocks[0].Number64().Uint64() > bc.CurrentBlock().Number64().Uint64()+1 {
					continue
				}

				if n, err := bc.InsertChain(blocks); nil != err {
					log.Warn("insert future block failed", err)
				} else {
					for _, k := range bc.futureBlocks.Keys() {
						bc.futureBlocks.Remove(k)
					}
					log.Infof("insert %d future block success, for %d to %d", n, blocks[0].Number64().Uint64(), blocks[n-1].Number64().Uint64())
				}

			}
		case <-bc.ctx.Done():
			return
		}
	}
}

func (bc *BlockChain) runNewBlockMessage() {
	newBlockCh := make(chan msg_proto.NewBlockMessageData, 10)
	sub := event.GlobalEvent.Subscribe(newBlockCh)
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

				//if err := bc.InsertBlock([]*block_proto.Block{blk}); err != nil {
				//	log.Errorf("failed insert block into block chain, number:%d, err: %v", blk.Header.Number, err)
				//}
				_ = db.Update(bc.ctx, func(tx kv.RwTx) error {
					rawdb.WriteBlock(tx, blk.(*block.Block))
					rawdb.WriteHeadBlockHash(tx, blk.Hash())
					_ = rawdb.ReadCurrentBlock(tx)
					return nil
				})
			}
		case msg, ok := <-newBlockCh:
			if ok {
				blk := block.Block{}
				if err := blk.FromProtoMessage(msg.Block); err == nil {
					_ = db.Update(bc.ctx, func(tx kv.RwTx) error {
						rawdb.WriteBlock(tx, &blk)
						rawdb.WriteHeadBlockHash(tx, blk.Hash())
						_ = rawdb.ReadCurrentBlock(tx)
						return nil
					})
				}
			}
		}
	}
}

func (bc *BlockChain) syncChain(remoteBlock uint64, peerID peer.ID) {
	/*sync chain
	 */
	//if remoteBlock < bc.currentBlock.Header.Number {
	//	return
	//}
	//var startNumber uint64
	//if bc.currentBlock.Header.Number == 0 {
	//	startNumber = bc.currentBlock.Header.Number
	//}
	log.Debugf("syncChain.......")
}

func (bc *BlockChain) GetHeader(h types.Hash, number *uint256.Int) block.IHeader {
	// Short circuit if the header's already in the cache, retrieve otherwise
	if header, ok := bc.headerCache.Get(h); ok {
		return header
	}

	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return nil
	}
	defer tx.Rollback()
	header := rawdb.ReadHeader(tx, h, number.Uint64())
	if nil == header {
		return nil
	}

	bc.headerCache.Add(h, header)
	return header
}

func (bc *BlockChain) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		log.Error("cannot open chain db", "err", err)
		return nil
	}
	defer tx.Rollback()

	hash, err := rawdb.ReadCanonicalHash(tx, number.Uint64())
	if nil != err {
		log.Error("cannot open chain db", "err", err)
		return nil
	}
	if hash == (types.Hash{}) {
		return nil
	}

	//return bc.GetHeader(hash, number)
	if header, ok := bc.headerCache.Get(hash); ok {
		return header
	}
	header := rawdb.ReadHeader(tx, hash, number.Uint64())
	if nil == header {
		return nil
	}
	bc.headerCache.Add(hash, header)
	return header
}

func (bc *BlockChain) GetHeaderByHash(h types.Hash) (block.IHeader, error) {
	number := bc.GetBlockNumber(h)
	if number == nil {
		return nil, nil
	}

	return bc.GetHeader(h, uint256.NewInt(*number)), nil
}

// GetCanonicalHash returns the canonical hash for a given block number
func (bc *BlockChain) GetCanonicalHash(number *uint256.Int) types.Hash {
	//block, err := bc.GetBlockByNumber(number)
	//if nil != err {
	//	return types.Hash{}
	//}
	//
	//return block.Hash()
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return types.Hash{}
	}
	defer tx.Rollback()

	hash, err := rawdb.ReadCanonicalHash(tx, number.Uint64())
	if nil != err {
		return types.Hash{}
	}
	return hash
}

// GetBlockNumber retrieves the block number belonging to the given hash
// from the cache or database
func (bc *BlockChain) GetBlockNumber(hash types.Hash) *uint64 {
	if cached, ok := bc.numberCache.Get(hash); ok {
		return &cached
	}
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return nil
	}
	defer tx.Rollback()
	number := rawdb.ReadHeaderNumber(tx, hash)
	if number != nil {
		bc.numberCache.Add(hash, *number)
	}
	return number
}

func (bc *BlockChain) GetBlockByHash(h types.Hash) (block.IBlock, error) {
	number := bc.GetBlockNumber(h)
	if nil == number {
		return nil, errBlockDoesNotExist
	}
	return bc.GetBlock(h, *number), nil
}

func (bc *BlockChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	var hash types.Hash
	bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		hash, _ = rawdb.ReadCanonicalHash(tx, number.Uint64())
		return nil
	})

	if hash == (types.Hash{}) {
		return nil, nil
	}
	return bc.GetBlock(hash, number.Uint64()), nil
}

func (bc *BlockChain) NewBlockHandler(payload []byte, peer peer.ID) error {

	var nweBlock msg_proto.NewBlockMessageData
	if err := proto.Unmarshal(payload, &nweBlock); err != nil {
		log.Errorf("failed unmarshal to msg, from peer:%s", peer)
		return err
	} else {
		var blk block.Block
		if err := blk.FromProtoMessage(nweBlock.GetBlock()); err == nil {
			bc.chBlocks <- &blk
		}
	}
	return nil
}

func (bc *BlockChain) SetEngine(engine consensus.Engine) {
	bc.engine = engine
}

func (bc *BlockChain) GetBlocksFromHash(hash types.Hash, n int) (blocks []block.IBlock) {
	var number *uint64
	if num, ok := bc.numberCache.Get(hash); ok {
		number = &num
	} else {
		bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
			number = rawdb.ReadHeaderNumber(tx, hash)
			return nil
		})
		if number == nil {
			return nil
		}
		bc.numberCache.Add(hash, *number)
	}

	for i := 0; i < n; i++ {
		blk := bc.GetBlock(hash, *number)
		if blk == nil {
			break
		}

		blocks = append(blocks, blk)
		hash = blk.ParentHash()
		*number--
	}
	return blocks
}

func (bc *BlockChain) GetBlock(hash types.Hash, number uint64) block.IBlock {
	if hash == (types.Hash{}) {
		return nil
	}

	if blk, ok := bc.blockCache.Get(hash); ok {
		return blk
	}

	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return nil
	}
	defer tx.Rollback()
	blk := rawdb.ReadBlock(tx, hash, number)
	if blk == nil {
		return nil
	}
	bc.blockCache.Add(hash, blk)
	return blk
	//header, err := rawdb.ReadHeaderByHash(tx, hash)
	//if err != nil {
	//	return nil
	//}
	//
	//if hash != header.Hash() {
	//	log.Error("Failed to get block, the hash is differ", "hash", hash.String(), "headerHash", header.Hash().String())
	//	return nil
	//}
	//
	//body, err := rawdb.ReadBlockByHash(tx, header.Hash())
	//if err != nil {
	//	log.Error("Failed to get block body", "err", err)
	//	return nil
	//}

	//return block.NewBlock(header, body.Transactions())
}

func (bc *BlockChain) SealedBlock(b block.IBlock) error {
	pbBlock := b.ToProtoMessage()
	//_ = bc.pubsub.Publish(message.GossipBlockMessage, pbBlock)
	return bc.p2p.Broadcast(context.TODO(), pbBlock)
}

// StopInsert stop insert
func (bc *BlockChain) StopInsert() {
	atomic.StoreInt32(&bc.procInterrupt, 1)
}

// insertStopped returns true after StopInsert has been called.
func (bc *BlockChain) insertStopped() bool {
	return atomic.LoadInt32(&bc.procInterrupt) == 1
}

// HasBlockAndState
func (bc *BlockChain) HasBlockAndState(hash types.Hash, number uint64) bool {
	blk := bc.GetBlock(hash, number)
	if blk == nil {
		return false
	}
	return bc.HasState(blk.Hash())
}

// HasState
func (bc *BlockChain) HasState(hash types.Hash) bool {
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return false
	}
	defer tx.Rollback()
	is, err := rawdb.IsCanonicalHash(tx, hash)
	if nil != err {
		return false
	}
	return is
}

func (bc *BlockChain) HasBlock(hash types.Hash, number uint64) bool {
	var flag bool
	if bc.blockCache.Contains(hash) {
		return true
	}

	bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		flag = rawdb.HasHeader(tx, hash, number)
		return nil
	})

	return flag
}

// GetTd
func (bc *BlockChain) GetTd(hash types.Hash, number *uint256.Int) *uint256.Int {

	if td, ok := bc.tdCache.Get(hash); ok {
		return td
	}

	var td *uint256.Int
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {

		ptd, err := rawdb.ReadTd(tx, hash, number.Uint64())
		if nil != err {
			return err
		}
		td = ptd
		return nil
	})

	bc.tdCache.Add(hash, td)
	return td
}

func (bc *BlockChain) skipBlock(err error) bool {
	if !errors.Is(err, ErrKnownBlock) {
		return false
	}
	return true
}

// InsertChain
func (bc *BlockChain) InsertChain(chain []block.IBlock) (int, error) {
	if len(chain) == 0 {
		return 0, nil
	}
	//
	for i := 1; i < len(chain); i++ {
		block, prev := chain[i], chain[i-1]
		if block.Number64().Cmp(uint256.NewInt(0).Add(prev.Number64(), uint256.NewInt(1))) != 0 || block.ParentHash() != prev.Hash() {
			log.Error("Non contiguous block insert",
				"number", block.Number64().String(),
				"hash", block.Hash(),
				"parent", block.ParentHash(),
				"prev number", prev.Number64(),
				"prev hash", prev.Hash(),
			)
			return 0, fmt.Errorf("non contiguous insert: item %d is #%s [%x..], item %d is #%s [%x..] (parent [%x..])", i-1, prev.Number64().String(),
				prev.Hash().Bytes()[:4], i, block.Number64().String(), block.Hash().Bytes()[:4], block.ParentHash().Bytes()[:4])
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
		if lastCanon != nil && bc.CurrentBlock().Hash() == lastCanon.Hash() {
			// todo
			// event.GlobalEvent.Send(&common.ChainHighestBlock{Block: lastCanon, Inserted: true})
		}
	}()

	// Start the parallel header verifier
	headers := make([]block.IHeader, len(chain))
	seals := make([]bool, len(chain))

	for i, blk := range chain {
		headers[i] = blk.Header()
		seals[i] = true
	}
	abort, results := bc.engine.VerifyHeaders(bc, headers, seals)
	defer close(abort)

	// Peek the error for the first block to decide the directing import logic
	it := newInsertIterator(chain, results, bc.validator)
	blk, err := it.next()
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
				// Switch to import mode if the forker says the reorg is necessary
				// and also the block is not on the canonical chain.
				// In eth2 the forker always returns true for reorg decision (blindly trusting
				// the external consensus engine), but in order to prevent the unnecessary
				// reorgs when importing known blocks, the special case is handled here.
				if blk.Number64().Uint64() > current.Number64().Uint64() || bc.GetCanonicalHash(blk.Number64()) != blk.Hash() {
					break
				}
			}
			log.Debug("Ignoring already known block", "number", blk.Number64(), "hash", blk.Hash())
			stats.ignored++
			blk, err = it.next()
		}
		// The remaining blocks are still known blocks, the only scenario here is:
		// During the fast sync, the pivot point is already submitted but rollback
		// happens. Then node resets the head full block to a lower height via `rollback`
		// and leaves a few known blocks in the database.
		//
		// When node runs a fast sync again, it can re-import a batch of known blocks via
		// `insertChain` while a part of them have higher total difficulty than current
		// head full block(new pivot point).
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
	// First block is pruned
	case errors.Is(err, ErrPrunedAncestor):
		// First block is pruned, insert as sidechain and reorg only if TD grows enough
		log.Debug("Pruned ancestor, inserting as sidechain", "number", blk.Number64(), "hash", blk.Hash())
		return bc.insertSideChain(blk, it)

	// First block is future, shove it (and all children) to the future queue (unknown ancestor)
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

		// If there are any still remaining, mark as ignored
		return it.index, err

	// Some other error(except ErrKnownBlock) occurred, abort.
	// ErrKnownBlock is allowed here since some known blocks
	// still need re-execution to generate snapshots that are missing
	case err != nil && !errors.Is(err, ErrKnownBlock):
		bc.futureBlocks.Remove(blk.Hash())
		stats.ignored += len(it.chain)
		bc.reportBlock(blk, nil, err)
		return it.index, err
	}

	evmRecord := func(ctx context.Context, db kv.RwDB, blockNr uint64, f func(tx kv.Tx, ibs *state.IntraBlockState, reader state.StateReader, writer state.WriterWithChangeSets) (map[types.Address]*uint256.Int, error)) (*state.IntraBlockState, map[types.Address]*uint256.Int, error) {
		tx, err := db.BeginRo(ctx)
		if nil != err {
			return nil, nil, err
		}
		defer tx.Rollback()

		stateReader := state.NewPlainStateReader(tx)
		ibs := state.New(stateReader)
		stateWriter := state.NewNoopWriter()

		var nopay map[types.Address]*uint256.Int
		nopay, err = f(tx, ibs, stateReader, stateWriter)
		if nil != err {
			return nil, nil, err
		}

		return ibs, nopay, nil
	}

	for ; blk != nil && err == nil || errors.Is(err, ErrKnownBlock); blk, err = it.next() {
		// If the chain is terminating, stop processing blocks
		if bc.insertStopped() {
			log.Debug("Abort during block processing")
			break
		}

		log.Tracef("Current block: number=%v, hash=%v, difficult=%v | Insert block block: number=%v, hash=%v, difficult= %v",
			bc.CurrentBlock().Number64(), bc.CurrentBlock().Hash(), bc.CurrentBlock().Difficulty(), blk.Number64(), blk.Hash(), blk.Difficulty())
		// Retrieve the parent block and it's state to execute on top
		start := time.Now()

		var receipts block.Receipts
		var logs []*block.Log
		var usedGas uint64
		ibs, nopay, err := evmRecord(bc.ctx, bc.ChainDB, blk.Number64().Uint64(), func(tx kv.Tx, ibs *state.IntraBlockState, reader state.StateReader, writer state.WriterWithChangeSets) (map[types.Address]*uint256.Int, error) {
			getHeader := func(hash types.Hash, number uint64) *block.Header {
				return rawdb.ReadHeader(tx, hash, number)
			}
			blockHashFunc := GetHashFn(blk.Header().(*block.Header), getHeader)

			var err error
			var nopay map[types.Address]*uint256.Int

			pstart := time.Now()
			receipts, nopay, logs, usedGas, err = bc.process.Process(blk.(*block.Block), ibs, reader, writer, blockHashFunc)
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

			blockExecutionTimer.Observe(float64(ptime)) // The time spent on EVM processing
			blockValidationTimer.Observe(float64(vtime))
			return nopay, nil
		})
		if nil != err {
			return it.index, err
		}

		wstart := time.Now()
		var status WriteStatus
		status, err = bc.writeBlockWithState(blk, receipts, ibs, nopay)
		if err != nil {
			return it.index, err
		}
		blockWriteTimer.Observe(float64(time.Since(wstart)))
		blockInsertTimer.Observe(float64(time.Since(start)))
		// Report the import stats before returning the various results
		stats.processed++
		stats.usedGas += usedGas

		switch status {
		case CanonStatTy:
			log.Trace("Inserted new block ", "number ", blk.Number64(), "hash", blk.Hash(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(),
				"elapsed", time.Since(start).Seconds(),
				"root", blk.StateRoot())

			if len(logs) > 0 {
				event.GlobalEvent.Send(common.NewLogsEvent{Logs: logs})
			}

			lastCanon = blk

		case SideStatTy:
			log.Debug("Inserted forked block", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(),
				"root", blk.StateRoot())

		default:
			// This in theory is impossible, but lets be nice to our future selves and leave
			// a log, instead of trying to track down blocks imports that don't emit logs.
			log.Warn("Inserted block with unknown status", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(),
				"root", blk.StateRoot())
		}
	}

	// Any blocks remaining here? The only ones we care about are the future ones
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

// insertSideChain
func (bc *BlockChain) insertSideChain(blk block.IBlock, it *insertIterator) (int, error) {
	var (
		externTd  uint256.Int
		lastBlock = blk
		current   = bc.CurrentBlock()
	)
	err := ErrPrunedAncestor
	for ; blk != nil && errors.Is(err, ErrPrunedAncestor); blk, err = it.next() {
		// Check the canonical state root for that number
		if number := blk.Number64(); current.Number64().Cmp(number) >= 0 {
			canonical, err := bc.GetBlockByNumber(number)
			if nil != err {
				return 0, err
			}

			if canonical != nil && canonical.Hash() == blk.Hash() {
				// Not a sidechain block, this is a re-import of a canon block which has it's state pruned

				// Collect the TD of the block. Since we know it's a canon one,
				// we can get it directly, and not (like further below) use
				// the parent and then add the block on top
				pt := bc.GetTd(blk.Hash(), blk.Number64())
				externTd = *pt
				continue
			}
			if canonical != nil && canonical.StateRoot() == blk.StateRoot() {
				// This is most likely a shadow-state attack. When a fork is imported into the
				// database, and it eventually reaches a block height which is not pruned, we
				// just found that the state already exist! This means that the sidechain block
				// refers to a state which already exists in our canon chain.
				//
				// If left unchecked, we would now proceed importing the blocks, without actually
				// having verified the state of the previous blocks.
				log.Warn("Sidechain ghost-state attack detected", "number", blk.Number64(), "sideroot", blk.StateRoot(), "canonroot", canonical.StateRoot())

				// If someone legitimately side-mines blocks, they would still be imported as usual. However,
				// we cannot risk writing unverified blocks to disk when they obviously target the pruning
				// mechanism.
				return it.index, errors.New("sidechain ghost-state attack")
			}
		}
		if externTd.Cmp(uint256.NewInt(0)) == 0 {
			externTd = *bc.GetTd(blk.ParentHash(), uint256.NewInt(0).Sub(blk.Number64(), uint256.NewInt(1)))
		}
		externTd = *externTd.Add(&externTd, blk.Difficulty())

		if !bc.HasBlock(blk.Hash(), blk.Number64().Uint64()) {
			start := time.Now()
			if err := bc.WriteBlockWithoutState(blk); err != nil {
				return it.index, err
			}
			log.Debug("Injected sidechain block", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(),
				"root", blk.StateRoot())
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
	// Import all the pruned blocks to make the state available
	var (
		blocks []block.IBlock
	)
	for i := len(hashes) - 1; i >= 0; i-- {
		// Append the next block to our batch
		block := bc.GetBlock(hashes[i], numbers[i])

		blocks = append(blocks, block)

		// If memory use grew too large, import and continue. Sadly we need to discard
		// all raised events and logs from notifications since we're too heavy on the
		// memory here.
		if len(blocks) >= 2048 {
			log.Info("Importing heavy sidechain segment", "blocks", len(blocks), "start", blocks[0].Number64(), "end", block.Number64())
			if _, err := bc.insertChain(blocks); err != nil {
				return 0, err
			}
			blocks = blocks[:0]
			// If the chain is terminating, stop processing blocks
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

// recoverAncestors
func (bc *BlockChain) recoverAncestors(blk block.IBlock) (types.Hash, error) {
	var (
		hashes  []types.Hash
		numbers []uint256.Int
		parent  = blk
	)
	for parent != nil && !bc.HasState(parent.Hash()) {
		hashes = append(hashes, parent.Hash())
		numbers = append(numbers, *parent.Number64())
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

// WriteBlockWithoutState without state
func (bc *BlockChain) WriteBlockWithoutState(blk block.IBlock) (err error) {
	if bc.insertStopped() {
		return errInsertionInterrupted
	}
	//if err := bc.state.WriteTD(blk.Hash(), td); err != nil {
	//	return err
	//}
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, blk.(*block.Block)); err != nil {
			return err
		}
		return nil
	})
}

func (bc *BlockChain) WriteBlockWithState(blk block.IBlock, receipts []*block.Receipt, ibs *state.IntraBlockState, nopay map[types.Address]*uint256.Int) error {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	_, err := bc.writeBlockWithState(blk, receipts, ibs, nopay)
	return err
}

// writeBlockWithState
func (bc *BlockChain) writeBlockWithState(blk block.IBlock, receipts []*block.Receipt, ibs *state.IntraBlockState, nopay map[types.Address]*uint256.Int) (status WriteStatus, err error) {
	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		//ptd := bc.GetTd(blk.ParentHash(), blk.Number64().Sub(uint256.NewInt(1)))
		ptd, err := rawdb.ReadTd(tx, blk.ParentHash(), uint256.NewInt(0).Sub(blk.Number64(), uint256.NewInt(1)).Uint64())
		if nil != err {
			log.Errorf("ReadTd failed err: %v", err)
		}
		if ptd == nil {
			return consensus.ErrUnknownAncestor
		}

		//if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		externTd := uint256.NewInt(0).Add(ptd, blk.Difficulty())
		if err := rawdb.WriteTd(tx, blk.Hash(), blk.Number64().Uint64(), externTd); nil != err {
			return err
		}
		log.Trace("writeTd:", "number", blk.Number64().Uint64(), "hash", blk.Hash(), "td", externTd.Uint64())
		if len(receipts) > 0 {
			//if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
			if err := rawdb.AppendReceipts(tx, blk.Number64().Uint64(), receipts); nil != err {
				log.Errorf("rawdb.AppendReceipts failed err= %v", err)
				return err
			}
		}
		if err := rawdb.WriteBlock(tx, blk.(*block.Block)); err != nil {
			return err
		}

		stateWriter := state.NewPlainStateWriter(tx, tx, blk.Number64().Uint64())
		if err := ibs.CommitBlock(bc.chainConfig.Rules(blk.Number64().Uint64()), stateWriter); nil != err {
			return err
		}

		if err := stateWriter.WriteChangeSets(); err != nil {
			return fmt.Errorf("writing changesets for block %d failed: %w", blk.Number64().Uint64(), err)
		}

		if err := stateWriter.WriteHistory(); err != nil {
			return fmt.Errorf("writing history for block %d failed: %w", blk.Number64().Uint64(), err)
		}

		if nil != nopay {
			for addr, v := range nopay {
				rawdb.PutAccountReward(tx, addr, v)
			}
		}

		return nil
	}); nil != err {
		return NonStatTy, err
	}

	reorg, err := bc.forker.ReorgNeeded(bc.CurrentBlock().Header(), blk.Header())
	if nil != err {
		return NonStatTy, err
	}
	if reorg {
		// Reorganise the chain if the parent is not the head block
		if blk.ParentHash() != bc.CurrentBlock().Hash() {
			if err := bc.reorg(nil, bc.CurrentBlock(), blk); err != nil {
				return NonStatTy, err
			}
		}
		status = CanonStatTy
	} else {
		status = SideStatTy
	}
	// Set new head.
	if status == CanonStatTy {
		if err := bc.writeHeadBlock(nil, blk); nil != err {
			log.Errorf("failed to save lates blocks, err: %v", err)
			return NonStatTy, err
		}
	}
	//
	if _, ok := bc.futureBlocks.Get(blk.Hash()); ok {
		bc.futureBlocks.Remove(blk.Hash())
	}
	return status, nil
}

// writeHeadBlock head
func (bc *BlockChain) writeHeadBlock(tx kv.RwTx, blk block.IBlock) error {
	var err error
	var notExternalTx bool
	if nil == tx {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if nil != err {
			return err
		}
		defer tx.Rollback()
		notExternalTx = true
	}

	rawdb.WriteHeadBlockHash(tx, blk.Hash())
	rawdb.WriteTxLookupEntries(tx, blk.(*block.Block))

	if err = rawdb.WriteCanonicalHash(tx, blk.Hash(), blk.Number64().Uint64()); nil != err {
		return err
	}

	bc.currentBlock.Store(blk.(*block.Block))
	headBlockGauge.Set(blk.Number64().Uint64())
	if notExternalTx {
		if err = tx.Commit(); nil != err {
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

// ReorgNeeded
func (bc *BlockChain) ReorgNeeded(current block.IBlock, header block.IBlock) bool {
	switch current.Number64().Cmp(header.Number64()) {
	case 1:
		return false
	case 0:
		return current.Difficulty().Cmp(uint256.NewInt(2)) != 0
	}
	return true
}

// SetHead set new head
func (bc *BlockChain) SetHead(head uint64) error {
	newHeadBlock, err := bc.GetBlockByNumber(uint256.NewInt(head))
	if err != nil {
		return nil
	}
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		return rawdb.WriteHeadHeaderHash(tx, newHeadBlock.Hash())
	})
}

// AddFutureBlock checks if the block is within the max allowed window to get
// accepted for future processing, and returns an error if the block is too far
// ahead and was not added.
//
// TODO after the transition, the future block shouldn't be kept. Because
// it's not checked in the Geth side anymore.
func (bc *BlockChain) AddFutureBlock(blk block.IBlock) error {
	max := uint64(time.Now().Unix() + maxTimeFutureBlocks)
	if blk.Time() > max {
		return fmt.Errorf("future block timestamp %v > allowed %v", blk.Time(), max)
	}
	if blk.Difficulty().Uint64() == 0 {
		// Never add PoS blocks into the future queue
		return nil
	}

	log.Info("add future block", "hash", blk.Hash(), "number", blk.Number64().Uint64(), "stateRoot", blk.StateRoot(), "txs", len(blk.Body().Transactions()))
	bc.futureBlocks.Add(blk.Hash(), blk.(*block.Block))
	return nil
}

// writeKnownBlock updates the head block flag with a known block
// and introduces chain reorg if necessary.
func (bc *BlockChain) writeKnownBlock(tx kv.RwTx, block block.IBlock) error {
	var notExternalTx bool
	var err error
	if nil == tx {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if nil != err {
			return err
		}
		defer tx.Rollback()
		notExternalTx = true
	}

	current := bc.CurrentBlock()
	if block.ParentHash() != current.Hash() {
		if err := bc.reorg(tx, current, block); err != nil {
			return err
		}
	}
	if err = bc.writeHeadBlock(tx, block); nil != err {
		return err
	}
	if notExternalTx {
		if err = tx.Commit(); nil != err {
			return err
		}
	}
	return nil
}

// reorg takes two blocks, an old chain and a new chain and will reconstruct the
// blocks and inserts them to be part of the new canonical chain and accumulates
// potential missing transactions and post an event about them.
// Note the new head block won't be processed here, callers need to handle it
// externally.
func (bc *BlockChain) reorg(tx kv.RwTx, oldBlock, newBlock block.IBlock) error {
	var (
		newChain    block.Blocks
		oldChain    block.Blocks
		commonBlock block.IBlock

		deletedTxs []types.Hash
		addedTxs   []types.Hash
	)
	// Reduce the longer chain to the same number as the shorter one
	if oldBlock.Number64().Uint64() > newBlock.Number64().Uint64() {
		// Old chain is longer, gather all transactions and logs as deleted ones
		for ; oldBlock != nil && oldBlock.Number64().Uint64() != newBlock.Number64().Uint64(); oldBlock = bc.GetBlock(oldBlock.ParentHash(), oldBlock.Number64().Uint64()-1) {
			oldChain = append(oldChain, oldBlock)
			for _, tx := range oldBlock.Transactions() {
				hash := tx.Hash()
				deletedTxs = append(deletedTxs, hash)
			}
		}
	} else {
		// New chain is longer, stash all blocks away for subsequent insertion
		for ; newBlock != nil && newBlock.Number64() != oldBlock.Number64(); newBlock = bc.GetBlock(newBlock.ParentHash(), newBlock.Number64().Uint64()-1) {
			newChain = append(newChain, newBlock)
		}
	}
	if oldBlock == nil {
		return fmt.Errorf("invalid old chain")
	}
	if newBlock == nil {
		return fmt.Errorf("invalid new chain")
	}

	var useExternalTx bool
	var err error
	if tx == nil {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if nil != err {
			return err
		}
		defer tx.Rollback()
		useExternalTx = false
	}

	// Both sides of the reorg are at the same number, reduce both until the common
	// ancestor is found
	for {
		// If the common ancestor was found, bail out
		if oldBlock.Hash() == newBlock.Hash() {
			commonBlock = oldBlock
			break
		}
		// Remove an old block as well as stash away a new block
		oldChain = append(oldChain, oldBlock)
		for _, t := range oldBlock.Transactions() {
			h := t.Hash()
			deletedTxs = append(deletedTxs, h)
		}
		newChain = append(newChain, newBlock)

		// Step back with both chains
		//oldBlock = bc.GetBlock(oldBlock.ParentHash(), oldBlock.Number64().Uint64()-1)
		oldBlock = rawdb.ReadBlock(tx, oldBlock.ParentHash(), oldBlock.Number64().Uint64()-1)
		if oldBlock == nil {
			return fmt.Errorf("invalid old chain")
		}
		//newBlock = bc.GetBlock(newBlock.ParentHash(), newBlock.Number64().Uint64()-1)
		newBlock = rawdb.ReadBlock(tx, newBlock.ParentHash(), newBlock.Number64().Uint64()-1)
		if newBlock == nil {
			return fmt.Errorf("invalid new chain")
		}
	}

	// Ensure the user sees large reorgs
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
		// Special case happens in the post merge stage that current head is
		// the ancestor of new head while these two blocks are not consecutive
		log.Info("Extend chain", "add", len(newChain), "number", newChain[0].Number64(), "hash", newChain[0].Hash())
	} else {
		// len(newChain) == 0 && len(oldChain) > 0
		// rewind the canonical chain to a lower point.
		log.Error("Impossible reorg, please file an issue", "oldnum", oldBlock.Number64(), "oldhash", oldBlock.Hash(), "oldblocks", len(oldChain), "newnum", newBlock.Number64(), "newhash", newBlock.Hash(), "newblocks", len(newChain))
	}
	// Insert the new chain(except the head block(reverse order)),
	// taking care of the proper incremental order.
	for i := len(newChain) - 1; i >= 1; i-- {
		// Insert the block in the canonical way, re-writing history
		bc.writeHeadBlock(tx, newChain[i])

		// Collect the new added transactions.
		for _, t := range newChain[i].Transactions() {
			h := t.Hash()
			addedTxs = append(addedTxs, h)
		}
	}

	//return bc.ChainDB.Update(bc.ctx, func(txw kv.RwTx) error {
	// Delete useless indexes right now which includes the non-canonical
	// transaction indexes, canonical chain indexes which above the head.
	for _, t := range types.HashDifference(deletedTxs, addedTxs) {
		rawdb.DeleteTxLookupEntry(tx, t)
	}

	// Delete all hash markers that are not part of the new canonical chain.
	// Because the reorg function does not handle new chain head, all hash
	// markers greater than or equal to new chain head should be deleted.
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
		if err = tx.Commit(); nil != err {
			return err
		}
	}

	return nil
}
func (bc *BlockChain) Close() error {
	bc.Quit()
	return nil
}

func (bc *BlockChain) Quit() <-chan struct{} {
	return bc.ctx.Done()
}

func (bc *BlockChain) DB() kv.RwDB {
	return bc.ChainDB
}

func (bc *BlockChain) StateAt(tx kv.Tx, blockNr uint64) *state.IntraBlockState {
	reader := state.NewPlainState(tx, blockNr+1)
	return state.New(reader)
}

func (bc *BlockChain) GetDepositInfo(address types.Address) (*uint256.Int, *uint256.Int) {
	var info *deposit.Info
	bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		info = deposit.GetDepositInfo(tx, address)
		return nil
	})
	if nil == info {
		return nil, nil
	}
	return info.RewardPerBlock, info.MaxRewardPerEpoch
}

func (bc *BlockChain) GetAccountRewardUnpaid(account types.Address) (*uint256.Int, error) {
	var value *uint256.Int
	var err error
	bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		value, err = rawdb.GetAccountReward(tx, account)
		return nil
	})
	return value, err
}
