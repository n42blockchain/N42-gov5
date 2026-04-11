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
// MevAPI exposes MEV bundle submission methods via JSON-RPC.
// BundleSubmitter abstracts the miner builder.BundlePool so the API
// can be wired into tests or standalone bundlers. SendBundleArgs is
// the Flashbots-compatible request payload accepted by eth_sendBundle
// for searchers submitting atomic transaction groups.

package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/miner/builder"
)

// BundleSubmitter is the interface for submitting MEV bundles.
type BundleSubmitter interface {
	BundlePool() *builder.BundlePool
}

// MevAPI provides MEV-related JSON-RPC methods.
type MevAPI struct {
	submitter BundleSubmitter
}

// NewMevAPI creates a new MEV API service.
func NewMevAPI(submitter BundleSubmitter) *MevAPI {
	return &MevAPI{submitter: submitter}
}

// SendBundleArgs represents the arguments for eth_sendBundle.
type SendBundleArgs struct {
	Txs               [][]byte     `json:"txs"`               // Raw signed transactions
	BlockNumber       uint64       `json:"blockNumber"`        // Target block number
	MinTimestamp       uint64       `json:"minTimestamp"`       // Optional minimum timestamp
	MaxTimestamp       uint64       `json:"maxTimestamp"`       // Optional maximum timestamp
	RevertingTxHashes []types.Hash `json:"revertingTxHashes"`  // Tx hashes allowed to revert
}

// SendBundleResult is the response for eth_sendBundle.
type SendBundleResult struct {
	BundleHash types.Hash `json:"bundleHash"`
}

// SendBundle submits a bundle of transactions for MEV extraction.
// The bundle will be executed atomically at the top of the target block.
func (api *MevAPI) SendBundle(ctx context.Context, args SendBundleArgs) (*SendBundleResult, error) {
	if api.submitter == nil {
		return nil, errors.New("miner not available")
	}

	pool := api.submitter.BundlePool()
	if pool == nil {
		return nil, errors.New("bundle pool not available")
	}

	if len(args.Txs) == 0 {
		return nil, errors.New("bundle must contain at least one transaction")
	}

	// Decode raw transactions.
	txs := make([]*transaction.Transaction, 0, len(args.Txs))
	for i, raw := range args.Txs {
		var tx transaction.Transaction
		if err := tx.Unmarshal(raw); err != nil {
			return nil, fmt.Errorf("failed to decode transaction %d: %w", i, err)
		}
		txs = append(txs, &tx)
	}

	bundle := &builder.Bundle{
		Txs:               txs,
		BlockNumber:        args.BlockNumber,
		MinTimestamp:        args.MinTimestamp,
		MaxTimestamp:        args.MaxTimestamp,
		RevertingTxHashes:  args.RevertingTxHashes,
	}

	if err := pool.AddBundle(bundle); err != nil {
		return nil, err
	}

	// Compute bundle hash as Keccak256 of concatenated tx hashes.
	var hashData []byte
	for _, tx := range txs {
		hashData = append(hashData, tx.Hash().Bytes()...)
	}
	bundleHash := types.BytesToHash(crypto.Keccak256(hashData))

	return &SendBundleResult{BundleHash: bundleHash}, nil
}
