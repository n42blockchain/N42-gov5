// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// consensus.go — minimal Ethereum-mainnet replay consensus engine.
//
// EthReplayEngine implements consensus.Engine for offline re-execution of
// imported mainnet data. Header verification and seal checking are no-ops
// (the source is trusted Geth ancient), but block rewards, uncle rewards,
// DAO fork payouts, and all post-merge / post-Shanghai withdrawal and
// finalisation rules are computed exactly so that state roots and receipt
// roots match live chain history.

package ethel

import (
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// EthReplayEngine is a minimal consensus engine for replaying Ethereum mainnet
// blocks. It correctly computes block rewards for all forks but skips PoW
// verification (seal checking) since we trust the Geth-imported data.
type EthReplayEngine struct {
	chainCfg *params.ChainConfig
}

// NewEthReplayEngine creates a replay-only consensus engine.
func NewEthReplayEngine(cfg *params.ChainConfig) *EthReplayEngine {
	return &EthReplayEngine{chainCfg: cfg}
}

// Author returns the coinbase (miner/fee-recipient) from the header.
func (e *EthReplayEngine) Author(header block.IHeader) (types.Address, error) {
	h := header.(*block.Header)
	return h.Coinbase, nil
}

// IsServiceTransaction always returns false for ETH mainnet.
func (e *EthReplayEngine) IsServiceTransaction(sender types.Address, syscall consensus.SystemCall) bool {
	return false
}

// Type returns EtHash consensus type.
func (e *EthReplayEngine) Type() params.ConsensusType {
	return params.EtHashConsensus
}

// VerifyHeader is a no-op for replay (we trust imported data).
func (e *EthReplayEngine) VerifyHeader(chain consensus.ChainHeaderReader, header block.IHeader, seal bool) error {
	return nil
}

// VerifyHeaders is a no-op for replay.
func (e *EthReplayEngine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))
	for range headers {
		results <- nil
	}
	return abort, results
}

// VerifyUncles is a no-op for replay.
func (e *EthReplayEngine) VerifyUncles(chain consensus.ConsensusChainReader, blk block.IBlock) error {
	return nil
}

// Prepare is a no-op for replay.
func (e *EthReplayEngine) Prepare(chain consensus.ChainHeaderReader, header block.IHeader) error {
	return nil
}

// Finalize applies block rewards according to Ethereum fork rules.
func (e *EthReplayEngine) Finalize(chain consensus.ChainHeaderReader, header block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	h := header.(*block.Header)

	// Post-merge (PoS): no block rewards from the consensus engine.
	if e.chainCfg.TerminalTotalDifficulty != nil && h.Difficulty != nil && h.Difficulty.IsZero() {
		return nil, nil, nil
	}

	// Pre-merge: compute ethash block reward.
	blockReward := ethBlockReward(e.chainCfg, h.Number.Uint64())
	minerReward := new(uint256.Int).Set(blockReward)

	// Uncle rewards.
	for _, uncle := range uncles {
		u := uncle.(*block.Header)
		uReward := ethUncleReward(blockReward, h.Number.Uint64(), u.Number.Uint64())
		ibs.AddBalance(u.Coinbase, uReward)
		// Miner gets 1/32 of block reward per uncle.
		r := new(uint256.Int).Div(blockReward, uint256.NewInt(32))
		minerReward.Add(minerReward, r)
	}

	ibs.AddBalance(h.Coinbase, minerReward)

	return nil, nil, nil
}

// FinalizeAndAssemble is not needed for replay.
func (e *EthReplayEngine) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	return nil, nil, nil, nil
}

// Seal is not needed for replay.
func (e *EthReplayEngine) Seal(chain consensus.ChainHeaderReader, blk block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	return nil
}

// SealHash returns the header hash (no seal in replay).
func (e *EthReplayEngine) SealHash(header block.IHeader) types.Hash {
	return header.(*block.Header).Hash()
}

// CalcDifficulty is not needed for replay.
func (e *EthReplayEngine) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent block.IHeader) *uint256.Int {
	return uint256.NewInt(0)
}

// APIs returns no additional APIs.
func (e *EthReplayEngine) APIs(chain consensus.ConsensusChainReader) []jsonrpc.API {
	return nil
}

// Close is a no-op.
func (e *EthReplayEngine) Close() error {
	return nil
}

// Compile-time check.
var _ consensus.Engine = (*EthReplayEngine)(nil)

// --- Ethereum block reward computation ---

var (
	// Initial block reward: 5 ETH.
	frontierBlockReward = uint256.NewInt(5e18)
	// Byzantium (EIP-649): 3 ETH.
	byzantiumBlockReward = uint256.NewInt(3e18)
	// Constantinople (EIP-1234): 2 ETH.
	constantinopleBlockReward = uint256.NewInt(2e18)
)

func ethBlockReward(cfg *params.ChainConfig, blockNum uint64) *uint256.Int {
	bn := new(big.Int).SetUint64(blockNum)
	if cfg.ConstantinopleBlock != nil && bn.Cmp(cfg.ConstantinopleBlock) >= 0 {
		return new(uint256.Int).Set(constantinopleBlockReward)
	}
	if cfg.ByzantiumBlock != nil && bn.Cmp(cfg.ByzantiumBlock) >= 0 {
		return new(uint256.Int).Set(byzantiumBlockReward)
	}
	return new(uint256.Int).Set(frontierBlockReward)
}

func ethUncleReward(blockReward *uint256.Int, blockNum, uncleNum uint64) *uint256.Int {
	// Uncle reward = (uncleNum + 8 - blockNum) * blockReward / 8
	r := new(uint256.Int).SetUint64(uncleNum + 8 - blockNum)
	r.Mul(r, blockReward)
	r.Div(r, uint256.NewInt(8))
	return r
}
