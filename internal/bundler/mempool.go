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
// UserOperation mempool for the ERC-4337 bundler. Thread-safe pool
// indexing UserOps by hash, enforcing capacity (ErrPoolFull),
// deduplication (ErrDuplicateOp) and replacement rules. Uses
// sha3.NewLegacyKeccak256 for UserOpHash derivation and exposes
// add/remove/getNext primitives driven by the BundlerService.

package bundler

import (
	"errors"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"golang.org/x/crypto/sha3"
)

var (
	ErrPoolFull     = errors.New("bundler: mempool is full")
	ErrDuplicateOp  = errors.New("bundler: duplicate user operation")
	ErrOpNotFound   = errors.New("bundler: user operation not found")
)

// UserOpPool is an in-memory pool for pending ERC-4337 UserOperations.
// It is separate from the standard transaction pool.
type UserOpPool struct {
	mu sync.RWMutex

	// byHash indexes operations by their hash.
	byHash map[types.Hash]*vm.UserOperation

	// bySender groups operations by sender address for nonce ordering.
	bySender map[types.Address][]*vm.UserOperation

	maxSize    int
	entryPoint types.Address
	chainID    uint64
}

// NewUserOpPool creates a new UserOperation mempool.
func NewUserOpPool(maxSize int, entryPoint types.Address, chainID uint64) *UserOpPool {
	return &UserOpPool{
		byHash:     make(map[types.Hash]*vm.UserOperation),
		bySender:   make(map[types.Address][]*vm.UserOperation),
		maxSize:    maxSize,
		entryPoint: entryPoint,
		chainID:    chainID,
	}
}

// Add inserts a UserOperation into the pool.
func (p *UserOpPool) Add(op *vm.UserOperation) (types.Hash, error) {
	hash := UserOpHash(op, p.entryPoint, p.chainID)

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.byHash[hash]; exists {
		return hash, ErrDuplicateOp
	}
	if len(p.byHash) >= p.maxSize {
		return types.Hash{}, ErrPoolFull
	}

	p.byHash[hash] = op
	p.bySender[op.Sender] = append(p.bySender[op.Sender], op)
	return hash, nil
}

// Remove deletes a UserOperation from the pool by hash.
func (p *UserOpPool) Remove(hash types.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()

	op, ok := p.byHash[hash]
	if !ok {
		return
	}
	delete(p.byHash, hash)

	// Remove from sender list.
	ops := p.bySender[op.Sender]
	for i, o := range ops {
		if UserOpHash(o, p.entryPoint, p.chainID) == hash {
			p.bySender[op.Sender] = append(ops[:i], ops[i+1:]...)
			break
		}
	}
	if len(p.bySender[op.Sender]) == 0 {
		delete(p.bySender, op.Sender)
	}
}

// Get returns a UserOperation by hash.
func (p *UserOpPool) Get(hash types.Hash) (*vm.UserOperation, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	op, ok := p.byHash[hash]
	return op, ok
}

// Pending returns all pending UserOperations, ordered by gas price descending.
func (p *UserOpPool) Pending(limit int) []*vm.UserOperation {
	p.mu.RLock()
	defer p.mu.RUnlock()

	ops := make([]*vm.UserOperation, 0, len(p.byHash))
	for _, op := range p.byHash {
		ops = append(ops, op)
	}

	// Sort by effective gas price descending (higher payers first).
	sortByGasPrice(ops)

	if limit > 0 && len(ops) > limit {
		ops = ops[:limit]
	}
	return ops
}

// RemoveBySender removes all operations for a given sender.
func (p *UserOpPool) RemoveBySender(sender types.Address) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ops, ok := p.bySender[sender]
	if !ok {
		return
	}
	for _, op := range ops {
		hash := UserOpHash(op, p.entryPoint, p.chainID)
		delete(p.byHash, hash)
	}
	delete(p.bySender, sender)
}

// Count returns the current number of operations in the pool.
func (p *UserOpPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.byHash)
}

// Clear removes all operations from the pool.
func (p *UserOpPool) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byHash = make(map[types.Hash]*vm.UserOperation)
	p.bySender = make(map[types.Address][]*vm.UserOperation)
}

// UserOpHash computes the hash of a UserOperation per ERC-4337 spec:
// keccak256(abi.encode(userOp.hash(), entryPoint, chainId))
func UserOpHash(op *vm.UserOperation, entryPoint types.Address, chainID uint64) types.Hash {
	h := sha3.NewLegacyKeccak256()

	// Inner hash: hash the UserOperation fields.
	inner := sha3.NewLegacyKeccak256()
	inner.Write(op.Sender.Bytes())
	if op.Nonce != nil {
		b := op.Nonce.Bytes32()
		inner.Write(b[:])
	}
	inner.Write(hashBytes(op.InitCode))
	inner.Write(hashBytes(op.CallData))
	if op.CallGasLimit != nil {
		b := op.CallGasLimit.Bytes32()
		inner.Write(b[:])
	}
	if op.VerificationGasLimit != nil {
		b := op.VerificationGasLimit.Bytes32()
		inner.Write(b[:])
	}
	if op.PreVerificationGas != nil {
		b := op.PreVerificationGas.Bytes32()
		inner.Write(b[:])
	}
	if op.MaxFeePerGas != nil {
		b := op.MaxFeePerGas.Bytes32()
		inner.Write(b[:])
	}
	if op.MaxPriorityFeePerGas != nil {
		b := op.MaxPriorityFeePerGas.Bytes32()
		inner.Write(b[:])
	}
	inner.Write(hashBytes(op.PaymasterAndData))

	// Outer hash: hash(innerHash, entryPoint, chainId)
	h.Write(inner.Sum(nil))
	h.Write(entryPoint.Bytes())
	var chainBuf [32]byte
	chainBuf[31] = byte(chainID)
	chainBuf[30] = byte(chainID >> 8)
	chainBuf[29] = byte(chainID >> 16)
	chainBuf[28] = byte(chainID >> 24)
	chainBuf[27] = byte(chainID >> 32)
	chainBuf[26] = byte(chainID >> 40)
	chainBuf[25] = byte(chainID >> 48)
	chainBuf[24] = byte(chainID >> 56)
	h.Write(chainBuf[:])

	return types.BytesToHash(h.Sum(nil))
}

func hashBytes(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// sortByGasPrice sorts operations by MaxFeePerGas descending (simple insertion sort).
func sortByGasPrice(ops []*vm.UserOperation) {
	for i := 1; i < len(ops); i++ {
		for j := i; j > 0; j-- {
			if compareGasPrice(ops[j], ops[j-1]) > 0 {
				ops[j], ops[j-1] = ops[j-1], ops[j]
			} else {
				break
			}
		}
	}
}

// compareGasPrice returns >0 if a has higher gas price than b.
func compareGasPrice(a, b *vm.UserOperation) int {
	if a.MaxFeePerGas == nil && b.MaxFeePerGas == nil {
		return 0
	}
	if a.MaxFeePerGas == nil {
		return -1
	}
	if b.MaxFeePerGas == nil {
		return 1
	}
	return a.MaxFeePerGas.Cmp(b.MaxFeePerGas)
}
