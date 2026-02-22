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

package apos

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/contracts/deposit"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/turbo/rpchelper"
)

const maxSearchBlock = 1000

// MinedBlock represents a single block mined by a validator.
type MinedBlock struct {
	BlockNumber *uint256.Int `json:"blockNumber"`
	Timestamp   uint64       `json:"timestamp"`
	Reward      *uint256.Int `json:"reward"`
}

// MinedBlockResponse is the response for GetMinedBlock queries.
type MinedBlockResponse struct {
	MinedBlocks        []MinedBlock `json:"minedBlocks"`
	CurrentBlockNumber *uint256.Int `json:"currentBlockNumber"`
}

type VerifiedBlockResponse struct {
	MinedBlocks []MinedBlock `json:"minedBlocks"`
	Total       *uint256.Int `json:"totalBlocks"`
}

// API is a user facing jsonrpc API to allow controlling the signer and voting
// mechanisms of the proof-of-authority scheme.
type API struct {
	chain consensus.ConsensusChainReader
	apos  *APos
}

// GetSnapshot retrieves the state snapshot at a given block.
func (api *API) GetSnapshot(number *jsonrpc.BlockNumber) (*Snapshot, error) {
	header := api.resolveHeader(number)
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.apos.snapshot(api.chain, header.Number64().Uint64(), header.Hash(), nil)
}

// GetSnapshotAtHash retrieves the state snapshot at a given block.
func (api *API) GetSnapshotAtHash(hash types.Hash) (*Snapshot, error) {
	header, _ := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.apos.snapshot(api.chain, header.Number64().Uint64(), header.Hash(), nil)
}

// GetSigners retrieves the list of authorized signers at the specified block.
func (api *API) GetSigners(number *jsonrpc.BlockNumber) ([]avmutil.Address, error) {
	header := api.resolveHeader(number)
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.snapshotSigners(header)
}

// GetSignersAtHash retrieves the list of authorized signers at the specified block.
func (api *API) GetSignersAtHash(hash types.Hash) ([]avmutil.Address, error) {
	header, _ := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	return api.snapshotSigners(header)
}

// resolveHeader returns the header for the given block number, defaulting to the current block.
func (api *API) resolveHeader(number *jsonrpc.BlockNumber) block.IHeader {
	if number == nil || *number == jsonrpc.LatestBlockNumber {
		return api.chain.CurrentBlock().Header()
	}
	return api.chain.GetHeaderByNumber(uint256.NewInt(uint64(number.Int64())))
}

// snapshotSigners retrieves the snapshot at the given header and returns the signer list.
func (api *API) snapshotSigners(header block.IHeader) ([]avmutil.Address, error) {
	snap, err := api.apos.snapshot(api.chain, header.Number64().Uint64(), header.Hash(), nil)
	if err != nil {
		return nil, err
	}
	signers := snap.signers()
	ethSigners := make([]avmutil.Address, len(signers))
	for i, signer := range signers {
		ethSigners[i] = *avmtypes.FromastAddress(&signer)
	}
	return ethSigners, nil
}

// Proposals returns the current proposals the node tries to uphold and vote on.
func (api *API) Proposals() map[avmutil.Address]bool {
	api.apos.lock.RLock()
	defer api.apos.lock.RUnlock()

	proposals := make(map[avmutil.Address]bool)
	for address, auth := range api.apos.proposals {
		proposals[*avmtypes.FromastAddress(&address)] = auth
	}
	return proposals
}

// Propose injects a new authorization proposal that the signer will attempt to
// push through.
func (api *API) Propose(address avmutil.Address, auth bool) {
	api.apos.lock.Lock()
	defer api.apos.lock.Unlock()

	api.apos.proposals[*avmtypes.ToastAddress(&address)] = auth
}

// Discard drops a currently running proposal, stopping the signer from casting
// further votes (either for or against).
func (api *API) Discard(address avmutil.Address) {
	api.apos.lock.Lock()
	defer api.apos.lock.Unlock()

	delete(api.apos.proposals, *avmtypes.ToastAddress(&address))
}

type status struct {
	InturnPercent float64               `json:"inturnPercent"`
	SigningStatus map[types.Address]int `json:"sealerActivity"`
	NumBlocks     uint64                `json:"numBlocks"`
}

// Status returns the status of the last N blocks,
// - the number of active signers,
// - the number of signers,
// - the percentage of in-turn blocks
func (api *API) Status() (*status, error) {
	var (
		numBlocks = uint64(64)
		header    = api.chain.CurrentBlock().Header()
		diff      = uint64(0)
		optimals  = 0
	)
	snap, err := api.apos.snapshot(api.chain, header.Number64().Uint64(), header.Hash(), nil)
	if err != nil {
		return nil, err
	}
	var (
		signers = snap.signers()
		end     = header.Number64().Uint64()
		start   = end - numBlocks
	)
	if numBlocks > end {
		start = 1
		numBlocks = end - start
	}
	// Security: prevent division by zero when chain is at genesis or block 1
	if numBlocks == 0 {
		return &status{
			InturnPercent: 0,
			SigningStatus: make(map[types.Address]int),
			NumBlocks:     0,
		}, nil
	}
	signStatus := make(map[types.Address]int)
	for _, s := range signers {
		signStatus[s] = 0
	}
	for n := start; n < end; n++ {
		h := api.chain.GetHeaderByNumber(uint256.NewInt(n))
		block := api.chain.GetBlock(h.Hash(), n)
		if h == nil {
			return nil, fmt.Errorf("missing block %d", n)
		}
		if block.Difficulty().Cmp(diffInTurn) == 0 {
			optimals++
		}
		diff += block.Difficulty().Uint64()
		sealer, err := api.apos.Author(h)
		if err != nil {
			return nil, err
		}
		signStatus[sealer]++
	}
	return &status{
		InturnPercent: float64(100*optimals) / float64(numBlocks),
		SigningStatus: signStatus,
		NumBlocks:     numBlocks,
	}, nil
}

type blockNumberOrHashOrRLP struct {
	*jsonrpc.BlockNumberOrHash
	RLP hexutil.Bytes `json:"rlp,omitempty"`
}

func (sb *blockNumberOrHashOrRLP) UnmarshalJSON(data []byte) error {
	bnOrHash := new(jsonrpc.BlockNumberOrHash)
	// Try to unmarshal bNrOrHash
	if err := bnOrHash.UnmarshalJSON(data); err == nil {
		sb.BlockNumberOrHash = bnOrHash
		return nil
	}
	// Try to unmarshal RLP
	var input string
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	blob, err := hexutil.Decode(input)
	if err != nil {
		return err
	}
	sb.RLP = blob
	return nil
}

// GetSigner returns the signer for a specific Apos block.
// Can be called with either a blocknumber, blockhash or an rlp encoded blob.
// The RLP encoded blob can either be a block or a header.
func (api *API) GetSigner(rlpOrBlockNr *blockNumberOrHashOrRLP) (types.Address, error) {
	if len(rlpOrBlockNr.RLP) == 0 {
		blockNrOrHash := rlpOrBlockNr.BlockNumberOrHash
		var header block.IHeader
		if blockNrOrHash == nil {
			header = api.chain.CurrentBlock().Header()
		} else if hash, ok := blockNrOrHash.Hash(); ok {
			header, _ = api.chain.GetHeaderByHash(hash)
		} else if number, ok := blockNrOrHash.Number(); ok {
			header = api.chain.GetHeaderByNumber(uint256.NewInt(uint64(number.Int64())))
		}
		if header == nil {
			return types.Address{}, fmt.Errorf("missing block %v", blockNrOrHash.String())
		}
		return api.apos.Author(header)
	}

	return types.Address{}, errors.New("do not support rlp")
}

// GetRewards retrieves reward history for the given address within a block range.
func (api *API) GetRewards(address avmutil.Address, from jsonrpc.BlockNumberOrHash, to jsonrpc.BlockNumberOrHash) (*RewardResponse, error) {
	var (
		resolvedFromBlock *uint256.Int
		resolvedToBlock   *uint256.Int
	)

	if err := api.apos.dbView(func(tx kv.Tx) error {
		var err error
		resolvedFromBlock, _, err = rpchelper.GetCanonicalBlockNumber(from, tx)
		if err != nil {
			return err
		}
		resolvedToBlock, _, err = rpchelper.GetCanonicalBlockNumber(to, tx)
		return err
	}); err != nil {
		return nil, err
	}

	rewardService := newReward(api.apos.chainConfig)
	return rewardService.GetRewards(*avmtypes.ToastAddress(&address), resolvedFromBlock, resolvedToBlock, api.chain.GetBlockByNumber)
}

// GetDepositInfo retrieves deposit information for the given address.
func (api *API) GetDepositInfo(address avmutil.Address) (*deposit.Info, error) {
	addr := *avmtypes.ToastAddress(&address)
	var info *deposit.Info

	err := api.apos.dbView(func(tx kv.Tx) error {
		info = deposit.GetDepositInfo(tx, addr)
		return nil
	})

	return info, err
}

// GetBlockRewards retrieves the rewards for a specific block.
func (api *API) GetBlockRewards(blockNr jsonrpc.BlockNumberOrHash) (resp []*block.Reward, err error) {
	api.apos.dbView(func(tx kv.Tx) error {
		resolvedBlockNr, hash, resolveErr := rpchelper.GetCanonicalBlockNumber(blockNr, tx)
		if resolveErr != nil {
			err = resolveErr
			return resolveErr
		}
		blk := rawdb.ReadBlock(tx, hash, resolvedBlockNr.Uint64())
		if blk == nil {
			err = errors.New("cannot find block body")
			return err
		}
		resp = blk.Body().Reward()
		return nil
	})
	return resp, err
}

// getHeader resolves a header from a BlockNumberOrHash reference.
func (api *API) getHeader(from jsonrpc.BlockNumberOrHash) block.IHeader {
	if blockNr, ok := from.Number(); ok {
		switch {
		case blockNr == jsonrpc.LatestBlockNumber || blockNr == jsonrpc.PendingBlockNumber:
			return api.chain.CurrentBlock().Header()
		case blockNr < jsonrpc.EarliestBlockNumber:
			return api.chain.GetHeaderByNumber(uint256.NewInt(0))
		default:
			return api.chain.GetHeaderByNumber(uint256.NewInt(uint64(blockNr.Int64())))
		}
	}
	if hash, ok := from.Hash(); ok {
		header, _ := api.chain.GetHeaderByHash(hash)
		return header
	}
	return api.chain.CurrentBlock().Header()
}

// GetMinedBlock retrieves blocks mined by the given address, searching backward from the specified block.
func (api *API) GetMinedBlock(address avmutil.Address, from jsonrpc.BlockNumberOrHash, wantCount uint64) (*MinedBlockResponse, error) {
	addr := *avmtypes.ToastAddress(&address)

	var depositInfo *deposit.Info
	api.apos.dbView(func(tx kv.Tx) error {
		depositInfo = deposit.GetDepositInfo(tx, addr)
		return nil
	})
	if depositInfo == nil {
		return nil, errors.New("address does not have deposit info")
	}

	currentHeader := api.getHeader(from)
	if currentHeader == nil {
		return nil, errUnknownBlock
	}

	currentBlock := api.chain.GetBlock(currentHeader.Hash(), currentHeader.Number64().Uint64())
	minedBlocks := make([]MinedBlock, 0, wantCount)
	var searchCount int
	var findCount uint64

	for {
		blockNum := currentHeader.Number64().Uint64()
		for _, verify := range currentBlock.Body().Verifier() {
			if addr == verify.Address {
				minedBlocks = append(minedBlocks, MinedBlock{
					BlockNumber: currentBlock.Number64(),
					Timestamp:   currentBlock.Time(),
					Reward:      depositInfo.RewardPerBlock,
				})
				findCount++
				if findCount >= wantCount {
					goto Finish
				}
			}
		}
		searchCount++
		if searchCount >= maxSearchBlock {
			break
		}
		// Prevent underflow at genesis block
		if blockNum == 0 {
			break
		}
		currentHeader = api.chain.GetHeaderByNumber(new(uint256.Int).SubUint64(currentBlock.Number64(), 1))
		if currentHeader == nil {
			break
		}
		currentBlock = api.chain.GetBlock(currentHeader.Hash(), currentHeader.Number64().Uint64())
	}

Finish:
	return &MinedBlockResponse{
		MinedBlocks:        minedBlocks,
		CurrentBlockNumber: currentBlock.Number64(),
	}, nil
}

// VerifiedBlock retrieves blocks verified by the given address within a range.
func (api *API) VerifiedBlock(address avmutil.Address, from jsonrpc.BlockNumberOrHash, wantCount uint64, to *jsonrpc.BlockNumber) (*VerifiedBlockResponse, error) {
	addr := *avmtypes.ToastAddress(&address)

	if to.Int64() <= 0 {
		return nil, errors.New("'To' block number must be greater than 0")
	}

	var depositInfo *deposit.Info
	api.apos.dbView(func(tx kv.Tx) error {
		depositInfo = deposit.GetDepositInfo(tx, addr)
		return nil
	})
	if depositInfo == nil {
		return nil, errors.New("address does not have deposit info")
	}

	currentHeader := api.getHeader(from)
	if currentHeader == nil {
		return nil, errUnknownBlock
	}

	currentBlock := api.chain.GetBlock(currentHeader.Hash(), currentHeader.Number64().Uint64())
	minedBlocks := make([]MinedBlock, 0, wantCount)
	var searchCount int
	var findCount uint64

	for {
		blockNum := currentHeader.Number64().Uint64()
		for _, verify := range currentBlock.Body().Verifier() {
			if addr == verify.Address {
				minedBlocks = append(minedBlocks, MinedBlock{
					BlockNumber: currentBlock.Number64(),
					Timestamp:   currentBlock.Time(),
					Reward:      depositInfo.RewardPerBlock,
				})
				findCount++
			}
		}
		if findCount >= wantCount {
			break
		}
		if uint64(*to) == currentBlock.Number64().Uint64() {
			break
		}
		searchCount++
		if searchCount >= int(api.apos.config.RewardEpoch) {
			break
		}
		// Prevent underflow at genesis block
		if blockNum == 0 {
			break
		}
		currentHeader = api.chain.GetHeaderByNumber(new(uint256.Int).SubUint64(currentBlock.Number64(), 1))
		if currentHeader == nil {
			break
		}
		currentBlock = api.chain.GetBlock(currentHeader.Hash(), currentHeader.Number64().Uint64())
	}

	return &VerifiedBlockResponse{
		MinedBlocks: minedBlocks,
		Total:       uint256.NewInt(findCount),
	}, nil
}

func (api *API) GetAccountRewardUnpaid(address types.Address) (val *uint256.Int, err error) {
	rewardService := newReward(api.apos.chainConfig)

	api.apos.dbView(func(tx kv.Tx) error {
		val, err = rewardService.getAccountRewardUnpaid(tx, address)
		return err
	})
	return
}
