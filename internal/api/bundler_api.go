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
// BundlerAPI implements the ERC-4337 account abstraction JSON-RPC.
// Wraps bundler.BundlerService and defines SendUserOperationArgs, the
// wire-level representation of a UserOperation with sender, nonce,
// init/call data, gas caps, fee fields, paymaster data and signature.
// Converts between the RPC struct and the internal vm.UserOperation.

package api

import (
	"context"
	"errors"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/bundler"
	"github.com/n42blockchain/N42/internal/vm"
)

// BundlerAPI implements ERC-4337 bundler JSON-RPC methods.
type BundlerAPI struct {
	service *bundler.BundlerService
}

// NewBundlerAPI creates a new bundler API service.
func NewBundlerAPI(service *bundler.BundlerService) *BundlerAPI {
	return &BundlerAPI{service: service}
}

// SendUserOperationArgs represents the arguments for eth_sendUserOperation.
type SendUserOperationArgs struct {
	Sender               types.Address `json:"sender"`
	Nonce                *uint256.Int  `json:"nonce"`
	InitCode             hexutil.Bytes `json:"initCode"`
	CallData             hexutil.Bytes `json:"callData"`
	CallGasLimit         *uint256.Int  `json:"callGasLimit"`
	VerificationGasLimit *uint256.Int  `json:"verificationGasLimit"`
	PreVerificationGas   *uint256.Int  `json:"preVerificationGas"`
	MaxFeePerGas         *uint256.Int  `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *uint256.Int  `json:"maxPriorityFeePerGas"`
	PaymasterAndData     hexutil.Bytes `json:"paymasterAndData"`
	Signature            hexutil.Bytes `json:"signature"`
}

// toUserOperation converts API args to internal UserOperation.
func (args *SendUserOperationArgs) toUserOperation() *vm.UserOperation {
	return &vm.UserOperation{
		Sender:               args.Sender,
		Nonce:                args.Nonce,
		InitCode:             args.InitCode,
		CallData:             args.CallData,
		CallGasLimit:         args.CallGasLimit,
		VerificationGasLimit: args.VerificationGasLimit,
		PreVerificationGas:   args.PreVerificationGas,
		MaxFeePerGas:         args.MaxFeePerGas,
		MaxPriorityFeePerGas: args.MaxPriorityFeePerGas,
		PaymasterAndData:     args.PaymasterAndData,
		Signature:            args.Signature,
	}
}

// SendUserOperation validates and submits a UserOperation to the bundler mempool.
// Returns the UserOperation hash.
func (api *BundlerAPI) SendUserOperation(ctx context.Context, args SendUserOperationArgs, entryPoint types.Address) (types.Hash, error) {
	if api.service == nil {
		return types.Hash{}, errors.New("bundler service not available")
	}
	op := args.toUserOperation()
	return api.service.SendUserOperation(op, entryPoint)
}

// EstimateUserOperationGasResult contains gas estimation results.
type EstimateUserOperationGasResult struct {
	PreVerificationGas   hexutil.Uint64 `json:"preVerificationGas"`
	VerificationGasLimit hexutil.Uint64 `json:"verificationGasLimit"`
	CallGasLimit         hexutil.Uint64 `json:"callGasLimit"`
}

// EstimateUserOperationGas estimates the gas values for a UserOperation.
func (api *BundlerAPI) EstimateUserOperationGas(ctx context.Context, args SendUserOperationArgs, entryPoint types.Address) (*EstimateUserOperationGasResult, error) {
	if api.service == nil {
		return nil, errors.New("bundler service not available")
	}
	op := args.toUserOperation()
	est, err := api.service.EstimateUserOperationGas(op)
	if err != nil {
		return nil, err
	}
	return &EstimateUserOperationGasResult{
		PreVerificationGas:   hexutil.Uint64(est.PreVerificationGas),
		VerificationGasLimit: hexutil.Uint64(est.VerificationGasLimit),
		CallGasLimit:         hexutil.Uint64(est.CallGasLimit),
	}, nil
}

// UserOperationResult represents a UserOperation returned by GetUserOperationByHash.
type UserOperationResult struct {
	Sender               types.Address  `json:"sender"`
	Nonce                *uint256.Int   `json:"nonce"`
	InitCode             hexutil.Bytes  `json:"initCode"`
	CallData             hexutil.Bytes  `json:"callData"`
	CallGasLimit         *uint256.Int   `json:"callGasLimit"`
	VerificationGasLimit *uint256.Int   `json:"verificationGasLimit"`
	PreVerificationGas   *uint256.Int   `json:"preVerificationGas"`
	MaxFeePerGas         *uint256.Int   `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *uint256.Int   `json:"maxPriorityFeePerGas"`
	PaymasterAndData     hexutil.Bytes  `json:"paymasterAndData"`
	Signature            hexutil.Bytes  `json:"signature"`
	EntryPoint           types.Address  `json:"entryPoint"`
}

// GetUserOperationByHash returns a pending UserOperation by hash.
func (api *BundlerAPI) GetUserOperationByHash(ctx context.Context, hash types.Hash) (*UserOperationResult, error) {
	if api.service == nil {
		return nil, errors.New("bundler service not available")
	}
	op, found := api.service.GetUserOperation(hash)
	if !found {
		return nil, nil // Not found returns null per spec.
	}

	eps := api.service.SupportedEntryPoints()
	ep := types.Address{}
	if len(eps) > 0 {
		ep = eps[0]
	}

	return &UserOperationResult{
		Sender:               op.Sender,
		Nonce:                op.Nonce,
		InitCode:             op.InitCode,
		CallData:             op.CallData,
		CallGasLimit:         op.CallGasLimit,
		VerificationGasLimit: op.VerificationGasLimit,
		PreVerificationGas:   op.PreVerificationGas,
		MaxFeePerGas:         op.MaxFeePerGas,
		MaxPriorityFeePerGas: op.MaxPriorityFeePerGas,
		PaymasterAndData:     op.PaymasterAndData,
		Signature:            op.Signature,
		EntryPoint:           ep,
	}, nil
}

// SupportedEntryPoints returns the list of EntryPoint addresses supported by this bundler.
func (api *BundlerAPI) SupportedEntryPoints(ctx context.Context) []types.Address {
	if api.service == nil {
		return nil
	}
	return api.service.SupportedEntryPoints()
}
