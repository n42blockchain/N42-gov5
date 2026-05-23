// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package execution_client

import (
	"context"
	"math/big"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
	"github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types"
	"github.com/n42blockchain/N42/internal/cl/depshim/types"
	"github.com/n42blockchain/N42/internal/cl/depshim/typesproto"
)

var errContextExceeded = "rpc error: code = DeadlineExceeded desc = context deadline exceeded"

// ExecutionEngine is used only for syncing up very close to chain tip and to stay in sync.
// It pretty much mimics engine API.

//go:generate mockgen -typed=true -source=./interface.go -destination=./execution_engine_mock.go -package=execution_client . ExecutionEngine
type ExecutionEngine interface {
	NewPayload(ctx context.Context, payload *cltypes.Eth1Block, beaconParentRoot *common.Hash, versionedHashes []common.Hash, executionRequestsList []hexutil.Bytes) (PayloadStatus, error)
	ForkChoiceUpdate(ctx context.Context, finalized, safe, head common.Hash, attributes *engine_types.PayloadAttributes, version clparams.StateVersion) ([]byte, error)
	SupportInsertion() bool
	InsertBlocks(ctx context.Context, blocks []*types.Block, wait bool) error
	InsertBlock(ctx context.Context, block *types.Block) error
	CurrentHeader(ctx context.Context) (*types.Header, error)
	IsCanonicalHash(ctx context.Context, hash common.Hash) (bool, error)
	Ready(ctx context.Context) (bool, error)
	// Range methods
	GetBodiesByRange(ctx context.Context, start, count uint64) ([]*types.RawBody, error)
	GetBodiesByHashes(ctx context.Context, hashes []common.Hash) ([]*types.RawBody, error)
	HasBlock(ctx context.Context, hash common.Hash) (bool, error)
	// Snapshots
	FrozenBlocks(ctx context.Context) uint64
	HasGapInSnapshots(ctx context.Context) bool
	// Block production
	GetAssembledBlock(ctx context.Context, id []byte, version clparams.StateVersion) (*cltypes.Eth1Block, *engine_types.BlobsBundle, *typesproto.RequestsBundle, *big.Int, error)

	// Blobs
	GetBlobs(ctx context.Context, versionedHashes []common.Hash, version clparams.StateVersion) (blobs [][]byte, proofs [][][]byte, err error)
}
