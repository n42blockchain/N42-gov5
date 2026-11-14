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
//
// BlockChain core lifecycle and chain-insert orchestration. Owns the
// canonical head pointer, manages import locks, drives header /
// block validation through BlockValidator and StateProcessor, and
// integrates the freezer, pruner and state commitment layers. Types
// and constants live in blockchain_types.go; read and write helper
// methods live in blockchain_reader.go and blockchain_write.go.

package internal

// This file contains the core lifecycle and chain-insert orchestration logic.
// Types and constants: blockchain_types.go
// Write methods: blockchain_write.go
// Read methods: blockchain_reader.go

import (
	"context"
	"errors"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Constructor
// =============================================================================

func NewBlockChain(ctx context.Context, genesisBlock block.IBlock, engine consensus.Engine, db kv.RwDB, p2p p2p.P2P, config *params.ChainConfig) (common.IBlockChain, error) {
	c, cancel := context.WithCancel(ctx)
	// Select the transaction-root encoding for this chain. Both Ethereum EL
	// commitment schemes use the standard MPT root over RLP transactions
	// (block.TxRoot -> DeriveShaErigon); native N42 schemes retain the legacy
	// proto keccak-concat root for historical hash continuity. Must be set
	// before any block is produced or validated.
	block.UseEthereumTxRoot = config != nil && (config.StateScheme == string(params.StateCommitmentPresetQMDB) || config.StateScheme == string(params.StateCommitmentPresetEthereumMPT))
	block.TxRootBlake3Time = txRootBlake3Time(config)
	block.DeferredExecutionTime = deferredExecutionTime(config)
	if block.DeferredExecutionTime != 0 && !nativeQMDBChain(config) {
		cancel()
		return nil, errors.New("deferredExecutionTime is set but the chain is not the native QMDB chain: the stored execution results and the applied marker it relies on exist only there")
	}
	concreteGenesis, err := requireConcreteBlock(genesisBlock, "unexpected genesis block type")
	if err != nil {
		cancel()
		return nil, err
	}
	if _, err := requireBlockNumber(concreteGenesis, "genesis block number unavailable"); err != nil {
		cancel()
		return nil, err
	}
	var current *block.Block

	if err := db.View(c, func(tx kv.Tx) error {
		current = rawdb.ReadCurrentBlock(tx)
		if current == nil {
			current = concreteGenesis
		}
		return nil
	}); err != nil {
		log.Warnf("failed to read current block from database, using genesis: %v", err)
		current = concreteGenesis
	}
	currentNumber, err := requireBlockNumber(current, "current block number unavailable")
	if err != nil {
		cancel()
		return nil, err
	}

	blockCache, _ := lru.New[types.Hash, *block.Block](blockCacheLimit)
	futureBlocks, _ := lru.New[types.Hash, *block.Block](maxFutureBlocks)
	receiptsCache, _ := lru.New[types.Hash, []*block.Receipt](receiptsCacheLimit)
	tdCache, _ := lru.New[types.Hash, *uint256.Int](tdCacheLimit)
	numberCache, _ := lru.New[types.Hash, uint64](numberCacheLimit)
	headerCache, _ := lru.New[types.Hash, *block.Header](headerCacheLimit)
	witnessCache, _ := lru.New[types.Hash, *witness.BlockWitness](witnessCacheLimit)

	bc := &BlockChain{
		chainConfig:   config,
		genesisBlock:  genesisBlock,
		blocks:        []block.IBlock{},
		ChainDB:       db,
		ctx:           c,
		cancel:        cancel,
		insertLock:    make(chan struct{}, 1),
		peers:         make(map[peer.ID]bool),
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
		witnessCache:  witnessCache,
	}

	bc.currentBlock.Store(current)
	headBlockGauge.Set(currentNumber.Uint64())
	headGasUsedGauge.Set(current.GasUsed())
	headGasLimitGauge.Set(current.GasLimit())
	headTransactionsGauge.Set(uint64(len(current.Transactions())))
	bc.forker = NewForkChoice(bc, nil)
	bc.process = NewStateProcessor(config, bc, engine)
	bc.validator = NewBlockValidator(config, bc, engine)

	return bc, nil
}

func init() {
	envswitch.Declare("N42_BLOCK_GOSSIP_FALLBACK", envswitch.Config, "Default on: SealedBlock's gossip fallback runs unconditionally; 0 gossips only with no direct peers.")
	envswitch.Declare("N42_TXROOT_BLAKE3_TIME", envswitch.BenchOnly, "Overrides the chain's txRootBlake3Time on a bench chain.")
	envswitch.Declare("N42_DEFERRED_EXECUTION_TIME", envswitch.BenchOnly, "Overrides the chain's deferredExecutionTime on a bench chain.")
}

func init() {
	envswitch.Declare("N42_BLOCK_GOSSIP_FALLBACK", envswitch.Config, "Default on: SealedBlock's gossip fallback runs unconditionally; 0 gossips only with no direct peers.")
	envswitch.Declare("N42_TXROOT_BLAKE3_TIME", envswitch.BenchOnly, "Overrides the chain's txRootBlake3Time on a bench chain.")
	envswitch.Declare("N42_DEFERRED_EXECUTION_TIME", envswitch.BenchOnly, "Overrides the chain's deferredExecutionTime on a bench chain.")
}
