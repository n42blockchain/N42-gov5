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
// APOA JSON-RPC API for signer and voting inspection.
// API wraps a consensus.ConsensusChainReader and the *Apoa engine so
// RPC handlers can fetch signer sets and historical snapshots for a
// given block. resolveHeader maps nil or LatestBlockNumber to the
// chain head, matching the tag semantics used by admin and debug calls.

package apoa

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
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// API is a user facing jsonrpc API to allow controlling the signer and voting
// mechanisms of the proof-of-authority scheme.
type API struct {
	chain consensus.ConsensusChainReader
	apoa  *Apoa
}

// resolveHeader returns the header for the given block number, or the current
// block header when number is nil or LatestBlockNumber.
func (api *API) resolveHeader(number *jsonrpc.BlockNumber) block.IHeader {
	if number == nil || *number == jsonrpc.LatestBlockNumber {
		current := api.chain.CurrentBlock()
		if current == nil {
			return nil
		}
		return current.Header()
	}
	return api.chain.GetHeaderByNumber(uint256.NewInt(uint64(number.Int64())))
}

// snapshotSigners returns the authorized signer list (as avmutil.Address) from
// the snapshot at the given block number and hash.
func (api *API) snapshotSigners(number uint64, hash types.Hash) ([]avmutil.Address, error) {
	snap, err := api.apoa.snapshot(api.chain, number, hash, nil)
	if err != nil {
		return nil, err
	}
	signers := snap.signers()
	result := make([]avmutil.Address, len(signers))
	for i, signer := range signers {
		result[i] = *avmtypes.FromastAddress(&signer)
	}
	return result, nil
}

// GetSnapshot retrieves the state snapshot at a given block.
func (api *API) GetSnapshot(number *jsonrpc.BlockNumber) (*Snapshot, error) {
	header := api.resolveHeader(number)
	if header == nil {
		return nil, errUnknownBlock
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, err
	}
	return api.apoa.snapshot(api.chain, headerNumber.Uint64(), header.Hash(), nil)
}

// GetSnapshotAtHash retrieves the state snapshot at a given block.
func (api *API) GetSnapshotAtHash(hash types.Hash) (*Snapshot, error) {
	header, _ := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, err
	}
	return api.apoa.snapshot(api.chain, headerNumber.Uint64(), header.Hash(), nil)
}

// GetSigners retrieves the list of authorized signers at the specified block.
func (api *API) GetSigners(number *jsonrpc.BlockNumber) ([]avmutil.Address, error) {
	header := api.resolveHeader(number)
	if header == nil {
		return nil, errUnknownBlock
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, err
	}
	return api.snapshotSigners(headerNumber.Uint64(), header.Hash())
}

// GetSignersAtHash retrieves the list of authorized signers at the specified block.
func (api *API) GetSignersAtHash(hash types.Hash) ([]avmutil.Address, error) {
	header, _ := api.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, errUnknownBlock
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, err
	}
	return api.snapshotSigners(headerNumber.Uint64(), header.Hash())
}

// Proposals returns the current proposals the node tries to uphold and vote on.
func (api *API) Proposals() map[avmutil.Address]bool {
	api.apoa.lock.RLock()
	defer api.apoa.lock.RUnlock()

	proposals := make(map[avmutil.Address]bool)
	for address, auth := range api.apoa.proposals {
		proposals[*avmtypes.FromastAddress(&address)] = auth
	}
	return proposals
}

// Propose injects a new authorization proposal that the signer will attempt to
// push through.
func (api *API) Propose(address avmutil.Address, auth bool) {
	api.apoa.lock.Lock()
	defer api.apoa.lock.Unlock()

	api.apoa.proposals[*avmtypes.ToastAddress(&address)] = auth
}

// Discard drops a currently running proposal, stopping the signer from casting
// further votes (either for or against).
func (api *API) Discard(address avmutil.Address) {
	api.apoa.lock.Lock()
	defer api.apoa.lock.Unlock()

	delete(api.apoa.proposals, *avmtypes.ToastAddress(&address))
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
		current   = api.chain.CurrentBlock()
		optimals  = 0
	)
	if current == nil {
		return nil, errUnknownBlock
	}
	header := current.Header()
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, err
	}
	snap, err := api.apoa.snapshot(api.chain, headerNumber.Uint64(), header.Hash(), nil)
	if err != nil {
		return nil, err
	}
	var (
		signers = snap.signers()
		end     = headerNumber.Uint64()
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
		if h == nil {
			return nil, fmt.Errorf("missing block %d", n)
		}
		blk := api.chain.GetBlock(h.Hash(), n)
		if blk == nil {
			return nil, fmt.Errorf("missing block %d", n)
		}
		if blk.Difficulty().Cmp(diffInTurn) == 0 {
			optimals++
		}
		sealer, err := api.apoa.Author(h)
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

// GetSigner returns the signer for a specific apoa block.
// Can be called with either a blocknumber, blockhash or an rlp encoded blob.
// The RLP encoded blob can either be a block or a header.
func (api *API) GetSigner(rlpOrBlockNr *blockNumberOrHashOrRLP) (types.Address, error) {
	if len(rlpOrBlockNr.RLP) == 0 {
		blockNrOrHash := rlpOrBlockNr.BlockNumberOrHash
		var header block.IHeader
		if blockNrOrHash == nil {
			current := api.chain.CurrentBlock()
			if current != nil {
				header = current.Header()
			}
		} else if hash, ok := blockNrOrHash.Hash(); ok {
			header, _ = api.chain.GetHeaderByHash(hash)
		} else if number, ok := blockNrOrHash.Number(); ok {
			header = api.chain.GetHeaderByNumber(uint256.NewInt(uint64(number.Int64())))
		}
		if header == nil {
			return types.Address{}, fmt.Errorf("missing block %v", blockNrOrHash.String())
		}
		return api.apoa.Author(header)
	}

	return types.Address{}, errors.New("do not support rlp")
}
