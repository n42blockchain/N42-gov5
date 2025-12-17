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
	"fmt"
	"strings"

	"github.com/n42blockchain/N42/common/ens"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

// ENSAPI provides ENS (Ethereum Name Service) resolution methods
type ENSAPI struct {
	api *BlockChainAPI
}

// NewENSAPI creates a new ENS API instance
func NewENSAPI(api *BlockChainAPI) *ENSAPI {
	return &ENSAPI{api: api}
}

// =============================================================================
// ENS Resolution Methods
// =============================================================================

// ResolveName resolves an ENS name to an Ethereum address
// This is the primary method for forward resolution
func (e *ENSAPI) ResolveName(ctx context.Context, name string) (types.Address, error) {
	if name == "" {
		return types.Address{}, ens.ErrInvalidName
	}

	// Normalize the name
	name = ens.Normalize(name)

	// Validate the name
	if !ens.IsValidName(name) {
		return types.Address{}, ens.ErrInvalidName
	}

	// Calculate the namehash
	node := ens.Namehash(name)

	// Get chain ID for registry address
	chainID := e.api.ChainId().ToInt().Uint64()
	registryAddr := ens.GetRegistryAddress(chainID)

	// Call registry.resolver(node) to get resolver address
	resolverAddr, err := e.getResolver(ctx, registryAddr, node)
	if err != nil {
		return types.Address{}, err
	}

	if resolverAddr == (types.Address{}) {
		return types.Address{}, ens.ErrNoResolver
	}

	// Call resolver.addr(node) to get the address
	return e.resolveAddr(ctx, resolverAddr, node)
}

// ResolveAddress performs reverse resolution: address -> name
func (e *ENSAPI) ResolveAddress(ctx context.Context, addr types.Address) (string, error) {
	// Calculate reverse node
	reverseNode := ens.ReverseNode(addr)

	// Get chain ID for registry address
	chainID := e.api.ChainId().ToInt().Uint64()
	registryAddr := ens.GetRegistryAddress(chainID)

	// Get resolver for reverse node
	resolverAddr, err := e.getResolver(ctx, registryAddr, reverseNode)
	if err != nil {
		return "", err
	}

	if resolverAddr == (types.Address{}) {
		return "", ens.ErrNoResolver
	}

	// Call resolver.name(reverseNode) to get the name
	return e.resolveName(ctx, resolverAddr, reverseNode)
}

// GetContentHash gets the content hash for an ENS name
func (e *ENSAPI) GetContentHash(ctx context.Context, name string) (hexutil.Bytes, error) {
	name = ens.Normalize(name)
	if !ens.IsValidName(name) {
		return nil, ens.ErrInvalidName
	}

	node := ens.Namehash(name)
	chainID := e.api.ChainId().ToInt().Uint64()
	registryAddr := ens.GetRegistryAddress(chainID)

	resolverAddr, err := e.getResolver(ctx, registryAddr, node)
	if err != nil {
		return nil, err
	}

	if resolverAddr == (types.Address{}) {
		return nil, ens.ErrNoResolver
	}

	return e.getContentHash(ctx, resolverAddr, node)
}

// GetTextRecord gets a text record for an ENS name
func (e *ENSAPI) GetTextRecord(ctx context.Context, name string, key string) (string, error) {
	name = ens.Normalize(name)
	if !ens.IsValidName(name) {
		return "", ens.ErrInvalidName
	}

	node := ens.Namehash(name)
	chainID := e.api.ChainId().ToInt().Uint64()
	registryAddr := ens.GetRegistryAddress(chainID)

	resolverAddr, err := e.getResolver(ctx, registryAddr, node)
	if err != nil {
		return "", err
	}

	if resolverAddr == (types.Address{}) {
		return "", ens.ErrNoResolver
	}

	return e.getTextRecord(ctx, resolverAddr, node, key)
}

// GetOwner gets the owner of an ENS name
func (e *ENSAPI) GetOwner(ctx context.Context, name string) (types.Address, error) {
	name = ens.Normalize(name)
	if !ens.IsValidName(name) {
		return types.Address{}, ens.ErrInvalidName
	}

	node := ens.Namehash(name)
	chainID := e.api.ChainId().ToInt().Uint64()
	registryAddr := ens.GetRegistryAddress(chainID)

	return e.getOwner(ctx, registryAddr, node)
}

// GetResolver gets the resolver address for an ENS name
func (e *ENSAPI) GetResolver(ctx context.Context, name string) (types.Address, error) {
	name = ens.Normalize(name)
	if !ens.IsValidName(name) {
		return types.Address{}, ens.ErrInvalidName
	}

	node := ens.Namehash(name)
	chainID := e.api.ChainId().ToInt().Uint64()
	registryAddr := ens.GetRegistryAddress(chainID)

	return e.getResolver(ctx, registryAddr, node)
}

// Namehash calculates the namehash of an ENS name
func (e *ENSAPI) Namehash(ctx context.Context, name string) (types.Hash, error) {
	if name == "" {
		return types.Hash{}, nil
	}
	return ens.Namehash(name), nil
}

// IsValidName checks if an ENS name is valid
func (e *ENSAPI) IsValidName(ctx context.Context, name string) (bool, error) {
	return ens.IsValidName(name), nil
}

// =============================================================================
// Internal Helper Methods
// =============================================================================

// getResolver calls registry.resolver(node)
func (e *ENSAPI) getResolver(ctx context.Context, registryAddr types.Address, node types.Hash) (types.Address, error) {
	// Build call data: resolver(bytes32)
	callData := make([]byte, 4+32)
	copy(callData[:4], ens.ResolverSelector[:])
	copy(callData[4:], node[:])

	// Execute call
	result, err := e.ethCall(ctx, registryAddr, callData)
	if err != nil {
		return types.Address{}, err
	}

	if len(result) < 32 {
		return types.Address{}, ens.ErrResolverNotFound
	}

	return types.BytesToAddress(result[12:32]), nil
}

// getOwner calls registry.owner(node)
func (e *ENSAPI) getOwner(ctx context.Context, registryAddr types.Address, node types.Hash) (types.Address, error) {
	// Build call data: owner(bytes32)
	callData := make([]byte, 4+32)
	copy(callData[:4], ens.OwnerSelector[:])
	copy(callData[4:], node[:])

	result, err := e.ethCall(ctx, registryAddr, callData)
	if err != nil {
		return types.Address{}, err
	}

	if len(result) < 32 {
		return types.Address{}, ens.ErrResolutionFailed
	}

	return types.BytesToAddress(result[12:32]), nil
}

// resolveAddr calls resolver.addr(node)
func (e *ENSAPI) resolveAddr(ctx context.Context, resolverAddr types.Address, node types.Hash) (types.Address, error) {
	// Build call data: addr(bytes32)
	callData := make([]byte, 4+32)
	copy(callData[:4], ens.AddrSelector[:])
	copy(callData[4:], node[:])

	result, err := e.ethCall(ctx, resolverAddr, callData)
	if err != nil {
		return types.Address{}, err
	}

	if len(result) < 32 {
		return types.Address{}, ens.ErrResolutionFailed
	}

	return types.BytesToAddress(result[12:32]), nil
}

// resolveName calls resolver.name(node)
func (e *ENSAPI) resolveName(ctx context.Context, resolverAddr types.Address, node types.Hash) (string, error) {
	// Build call data: name(bytes32)
	callData := make([]byte, 4+32)
	copy(callData[:4], ens.NameSelector[:])
	copy(callData[4:], node[:])

	result, err := e.ethCall(ctx, resolverAddr, callData)
	if err != nil {
		return "", err
	}

	// Decode ABI-encoded string
	return decodeABIString(result)
}

// getContentHash calls resolver.contenthash(node)
func (e *ENSAPI) getContentHash(ctx context.Context, resolverAddr types.Address, node types.Hash) ([]byte, error) {
	// Build call data: contenthash(bytes32)
	callData := make([]byte, 4+32)
	copy(callData[:4], ens.ContenthashSelector[:])
	copy(callData[4:], node[:])

	result, err := e.ethCall(ctx, resolverAddr, callData)
	if err != nil {
		return nil, err
	}

	// Decode ABI-encoded bytes
	return decodeABIBytes(result)
}

// getTextRecord calls resolver.text(node, key)
func (e *ENSAPI) getTextRecord(ctx context.Context, resolverAddr types.Address, node types.Hash, key string) (string, error) {
	// Build call data: text(bytes32, string)
	// This is more complex due to dynamic string encoding
	keyBytes := []byte(key)
	callData := make([]byte, 4+32+32+32+len(keyBytes))

	copy(callData[:4], ens.TextSelector[:])
	copy(callData[4:36], node[:])

	// String offset (64 = 0x40)
	callData[67] = 0x40

	// String length
	callData[99] = byte(len(keyBytes))

	// String data
	copy(callData[100:], keyBytes)

	result, err := e.ethCall(ctx, resolverAddr, callData)
	if err != nil {
		return "", err
	}

	return decodeABIString(result)
}

// ethCall performs a read-only call to a contract
func (e *ENSAPI) ethCall(ctx context.Context, to types.Address, data []byte) ([]byte, error) {
	// This would use the underlying eth_call implementation
	// For now, return an error indicating the feature needs blockchain integration
	return nil, fmt.Errorf("ENS resolution requires blockchain state access - ensure node is synced")
}

// =============================================================================
// ABI Decoding Helpers
// =============================================================================

// decodeABIString decodes an ABI-encoded string
func decodeABIString(data []byte) (string, error) {
	if len(data) < 64 {
		return "", nil
	}

	// Get offset (first 32 bytes)
	// Get length (next 32 bytes after offset)
	length := int(data[63])
	if length == 0 {
		return "", nil
	}

	if len(data) < 64+length {
		return "", ens.ErrResolutionFailed
	}

	return strings.TrimRight(string(data[64:64+length]), "\x00"), nil
}

// decodeABIBytes decodes an ABI-encoded bytes
func decodeABIBytes(data []byte) ([]byte, error) {
	if len(data) < 64 {
		return nil, nil
	}

	// Get length
	length := int(data[63])
	if length == 0 {
		return nil, nil
	}

	if len(data) < 64+length {
		return nil, ens.ErrResolutionFailed
	}

	result := make([]byte, length)
	copy(result, data[64:64+length])
	return result, nil
}

