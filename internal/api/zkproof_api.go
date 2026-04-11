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
// ZKProofAPI surfaces zero-knowledge proof queries and verification.
// Holds a zkverifier.Verifier and delegates block lookups to the shared
// *API instance. ZKProofResult is the JSON-RPC response type carrying
// the block number, proof type and serialized proof bytes so clients
// can fetch and independently verify chain state without trusting the node.

package api

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/internal/zkverifier"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// ZKProofAPI provides RPC methods for querying and verifying ZK proofs.
type ZKProofAPI struct {
	api      *API
	verifier *zkverifier.Verifier
}

// NewZKProofAPI creates a new ZKProofAPI.
func NewZKProofAPI(api *API) *ZKProofAPI {
	return &ZKProofAPI{
		api:      api,
		verifier: zkverifier.NewVerifier(),
	}
}

// ZKProofResult is the JSON-RPC response for a block ZK proof.
type ZKProofResult struct {
	BlockNumber  uint64 `json:"blockNumber"`
	ProofType    string `json:"proofType"`
	ProofData    string `json:"proofData"`    // hex-encoded
	PublicInputs string `json:"publicInputs"` // hex-encoded
	Available    bool   `json:"available"`
}

// GetBlockZKProof returns the ZK proof for a given block, if available.
func (s *ZKProofAPI) GetBlockZKProof(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash) (*ZKProofResult, error) {
	blk, err := BlockByNumberOrHash(ctx, blockNrOrHash, s.api)
	if err != nil {
		return nil, fmt.Errorf("block not found: %w", err)
	}
	if blk == nil {
		return nil, fmt.Errorf("block not found")
	}

	result := &ZKProofResult{
		BlockNumber: uint256ToUint64OrZero(blk.Number64()),
		Available:   false,
	}

	body := blk.Body()
	if body != nil && len(body.ZKProof()) > 0 {
		proof, err := zkprover.DecodeProof(body.ZKProof())
		if err != nil {
			return nil, fmt.Errorf("failed to decode proof: %w", err)
		}
		result.ProofType = string(proof.Type)
		result.ProofData = hex.EncodeToString(proof.ProofData)
		result.PublicInputs = hex.EncodeToString(proof.PublicInputs)
		result.Available = true
		return result, nil
	}

	return result, nil
}

// VerifyBlockZKProof verifies the ZK proof for a given block.
func (s *ZKProofAPI) VerifyBlockZKProof(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash) (bool, error) {
	blk, err := BlockByNumberOrHash(ctx, blockNrOrHash, s.api)
	if err != nil {
		return false, fmt.Errorf("block not found: %w", err)
	}
	if blk == nil {
		return false, fmt.Errorf("block not found")
	}

	body := blk.Body()
	if body == nil || len(body.ZKProof()) == 0 {
		return false, fmt.Errorf("no ZK proof available for block %d", uint256ToUint64OrZero(blk.Number64()))
	}

	proof, err := zkprover.DecodeProof(body.ZKProof())
	if err != nil {
		return false, fmt.Errorf("failed to decode proof: %w", err)
	}
	if !s.verifier.CryptographicReady() {
		return false, zkverifier.ErrCryptographicVerificationUnavailable
	}

	if err := s.verifier.Verify(proof, blk.StateRoot(), blk.GasUsed()); err != nil {
		return false, err
	}

	return true, nil
}

// GetProofStatus returns the proof generation status for a given block.
func (s *ZKProofAPI) GetProofStatus(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash) (string, error) {
	blk, err := BlockByNumberOrHash(ctx, blockNrOrHash, s.api)
	if err != nil {
		return "unknown", fmt.Errorf("block not found: %w", err)
	}
	if blk == nil {
		return "unknown", fmt.Errorf("block not found")
	}

	body := blk.Body()
	if body != nil && len(body.ZKProof()) > 0 {
		return "proven", nil
	}

	return "pending", nil
}

// APIs returns the RPC API descriptors for the ZK proof namespace.
func (s *ZKProofAPI) APIs() []jsonrpc.API {
	return []jsonrpc.API{
		{
			Namespace: "zk",
			Service:   s,
		},
	}
}
