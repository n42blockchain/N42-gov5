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

package api

import (
	"context"
	"math/big"
	"sort"
	"sync"

	lru "github.com/hashicorp/golang-lru"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

const sampleNumber = 3 // Number of transactions sampled in a block

// Oracle recommends gas prices based on the content of recent
// blocks. Suitable for both light and full clients.
type Oracle struct {
	backend     common.IBlockChain
	miner       common.IMiner
	lastHead    types.Hash
	lastPrice   *big.Int
	maxPrice    *big.Int
	ignorePrice *big.Int
	cacheLock   sync.RWMutex
	fetchLock   sync.Mutex

	checkBlocks, percentile           int
	maxHeaderHistory, maxBlockHistory int
	historyCache                      *lru.Cache
	//
	chainConfig *params.ChainConfig
}

// NewOracle returns a new gasprice oracle which can recommend suitable
// gasprice for newly created transaction.
func NewOracle(backend common.IBlockChain, miner common.IMiner, chainConfig *params.ChainConfig, params conf.GpoConfig) *Oracle {
	blocks := params.Blocks
	if blocks < 1 {
		blocks = 1
		log.Warn("Sanitizing invalid gasprice oracle sample blocks", "provided", params.Blocks, "updated", blocks)
	}
	percent := params.Percentile
	if percent < 0 {
		percent = 0
		log.Warn("Sanitizing invalid gasprice oracle sample percentile", "provided", params.Percentile, "updated", percent)
	} else if percent > 100 {
		percent = 100
		log.Warn("Sanitizing invalid gasprice oracle sample percentile", "provided", params.Percentile, "updated", percent)
	}
	maxPrice := params.MaxPrice
	if maxPrice == nil || maxPrice.Int64() <= 0 {
		maxPrice = conf.DefaultMaxPrice
		log.Warn("Sanitizing invalid gasprice oracle price cap", "provided", params.MaxPrice, "updated", maxPrice)
	}
	ignorePrice := params.IgnorePrice
	if ignorePrice == nil || ignorePrice.Int64() <= 0 {
		ignorePrice = conf.DefaultIgnorePrice
		log.Warn("Sanitizing invalid gasprice oracle ignore price", "provided", params.IgnorePrice, "updated", ignorePrice)
	} else if ignorePrice.Int64() > 0 {
		log.Debug("Gas oracle ignore threshold", "value", ignorePrice)
	}
	maxHeaderHistory := params.MaxHeaderHistory
	if maxHeaderHistory < 1 {
		maxHeaderHistory = 1
		log.Warn("Sanitizing invalid gasprice oracle max header history", "provided", params.MaxHeaderHistory, "updated", maxHeaderHistory)
	}
	maxBlockHistory := params.MaxBlockHistory
	if maxBlockHistory < 1 {
		maxBlockHistory = 1
		log.Warn("Sanitizing invalid gasprice oracle max block history", "provided", params.MaxBlockHistory, "updated", maxBlockHistory)
	}

	cache, _ := lru.New(2048)

	highestBlockCh := make(chan common.ChainHighestBlock, 10)
	highestSub, err := event.GlobalEvent.Subscribe(highestBlockCh)
	if err != nil {
		log.Warn("failed to subscribe to ChainHighestBlock events for gas price cache invalidation", "err", err)
	}

	// Start goroutine to handle block updates for cache invalidation
	// Note: The goroutine will exit when the subscription is closed
	if highestSub != nil {
		go func() {
			defer close(highestBlockCh)
			var lastHead types.Hash
			for {
				select {
				case ev, ok := <-highestBlockCh:
					if !ok {
						return
					}
					if ev.Block.ParentHash() != lastHead {
						cache.Purge()
					}
					lastHead = ev.Block.Hash()
				case <-highestSub.Err():
					return
				}
			}
		}()
	}

	return &Oracle{
		backend:          backend,
		miner:            miner,
		lastPrice:        params.Default,
		maxPrice:         maxPrice,
		ignorePrice:      ignorePrice,
		checkBlocks:      blocks,
		percentile:       percent,
		maxHeaderHistory: maxHeaderHistory,
		maxBlockHistory:  maxBlockHistory,
		historyCache:     cache,
		chainConfig:      chainConfig,
	}
}

// SuggestTipCap returns a tip cap so that newly created transaction can have a
// very high chance to be included in the following blocks.
//
// Note, for legacy transactions and the legacy eth_gasPrice RPC call, it will be
// necessary to add the basefee to the returned number to fall back to the legacy
// behavior.
func (oracle *Oracle) SuggestTipCap(ctx context.Context, chainConfig *params.ChainConfig) (*big.Int, error) {
	currentBlock := oracle.backend.CurrentBlock()
	if currentBlock == nil {
		return new(big.Int).Set(oracle.lastPrice), nil
	}
	head := currentBlock.Header()
	var headHash types.Hash
	if head == nil {
		headHash = types.Hash{}
	} else {
		headHash = types.Hash(head.Hash())
	}

	// If the latest gasprice is still available, return it.
	oracle.cacheLock.RLock()
	lastHead, lastPrice := oracle.lastHead, oracle.lastPrice
	oracle.cacheLock.RUnlock()
	if headHash == lastHead {
		return new(big.Int).Set(lastPrice), nil
	}
	oracle.fetchLock.Lock()
	defer oracle.fetchLock.Unlock()

	// Try checking the cache again, maybe the last fetch fetched what we need
	oracle.cacheLock.RLock()
	lastHead, lastPrice = oracle.lastHead, oracle.lastPrice
	oracle.cacheLock.RUnlock()
	if headHash == lastHead {
		return new(big.Int).Set(lastPrice), nil
	}
	var (
		sent, exp int
		number    = head.Number64().Uint64()
		result    = make(chan blockResult, oracle.checkBlocks)
		quit      = make(chan struct{})
		results   []*big.Int
	)
	for sent < oracle.checkBlocks && number > 0 {
		go oracle.getBlockValues(ctx, avmtypes.MakeSigner(chainConfig, big.NewInt(int64(number))), number, sampleNumber, oracle.ignorePrice, result, quit)
		sent++
		exp++
		number--
	}
	for exp > 0 {
		res := <-result
		if res.err != nil {
			close(quit)
			return new(big.Int).Set(lastPrice), res.err
		}
		exp--
		// Nothing returned. There are two special cases here:
		// - The block is empty
		// - All the transactions included are sent by the miner itself.
		// In these cases, use the latest calculated price for sampling.
		if len(res.values) == 0 {
			res.values = []*big.Int{lastPrice}
		}
		// Besides, in order to collect enough data for sampling, if nothing
		// meaningful returned, try to query more blocks. But the maximum
		// is 2*checkBlocks.
		if len(res.values) == 1 && len(results)+1+exp < oracle.checkBlocks*2 && number > 0 {
			go oracle.getBlockValues(ctx, avmtypes.MakeSigner(chainConfig, big.NewInt(int64(number))), number, sampleNumber, oracle.ignorePrice, result, quit)
			sent++
			exp++
			number--
		}
		results = append(results, res.values...)
	}
	price := lastPrice
	if len(results) > 0 {
		sort.Sort(bigIntArray(results))
		price = results[(len(results)-1)*oracle.percentile/100]
	}
	if price.Cmp(oracle.maxPrice) > 0 {
		price = new(big.Int).Set(oracle.maxPrice)
	}
	oracle.cacheLock.Lock()
	oracle.lastHead = headHash
	oracle.lastPrice = price
	oracle.cacheLock.Unlock()

	return new(big.Int).Set(price), nil
}

type blockResult struct {
	values []*big.Int
	err    error
}

// getBlockPrices calculates the lowest transaction gas price in a given block
// and sends it to the result channel. If the block is empty or all transactions
// are sent by the miner itself(it doesn't make any sense to include this kind of
// transaction prices for sampling), nil gasprice is returned.
func (oracle *Oracle) getBlockValues(ctx context.Context, signer avmtypes.Signer, blockNum uint64, limit int, ignoreUnder *big.Int, result chan blockResult, quit chan struct{}) {
	block, err := oracle.backend.GetBlockByNumber(uint256.NewInt(uint64(jsonrpc.BlockNumber(blockNum))))
	if block == nil {
		select {
		case result <- blockResult{nil, err}:
		case <-quit:
		}
		return
	}
	// Sort the transaction by effective tip in ascending sort.
	txs := make([]*transaction.Transaction, len(block.Transactions()))
	copy(txs, block.Transactions())
	sorter := newSorter(txs, block.BaseFee64())
	sort.Sort(sorter)

	var prices []*big.Int
	for _, tx := range sorter.txs {
		tip, err := tx.EffectiveGasTip(block.BaseFee64())
		if err != nil || tip == nil {
			continue
		}
		if ignoreUnder != nil {
			ignoreUnderx, overflow := uint256.FromBig(ignoreUnder)
			if !overflow && tip.Cmp(ignoreUnderx) == -1 {
				continue
			}
		}
		sender := tx.From()
		if sender == nil {
			continue
		}
		if *sender != block.Coinbase() {
			prices = append(prices, tip.ToBig())
			if len(prices) >= limit {
				break
			}
		}
	}
	select {
	case result <- blockResult{prices, nil}:
	case <-quit:
	}
}

type txSorter struct {
	txs     []*transaction.Transaction
	baseFee *uint256.Int
}

func newSorter(txs []*transaction.Transaction, baseFee *uint256.Int) *txSorter {
	return &txSorter{
		txs:     txs,
		baseFee: baseFee,
	}
}

func (s *txSorter) Len() int { return len(s.txs) }
func (s *txSorter) Swap(i, j int) {
	s.txs[i], s.txs[j] = s.txs[j], s.txs[i]
}
func (s *txSorter) Less(i, j int) bool {
	// It's okay to discard the error because a tx would never be
	// accepted into a block with an invalid effective tip.
	tip1, _ := s.txs[i].EffectiveGasTip(s.baseFee)
	tip2, _ := s.txs[j].EffectiveGasTip(s.baseFee)
	x, _ := uint256.FromBig(tip2.ToBig())
	return tip1.Cmp(x) < 0
}

type bigIntArray []*big.Int

func (s bigIntArray) Len() int           { return len(s) }
func (s bigIntArray) Less(i, j int) bool { return s[i].Cmp(s[j]) < 0 }
func (s bigIntArray) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
