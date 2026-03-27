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

package vm

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// Content-Addressed Storage (CAS) precompile address.
var ContentStoreAddress = types.HexToAddress("0x0000000000000000000000000000000000000300")

// CAS precompile map (N42-specific, independent of standard forks).
var PrecompiledContractsCAS = map[types.Address]PrecompiledContract{
	ContentStoreAddress: &contentStore{},
}

// ContentStoreDB provides read/write access to the content-addressed storage.
// Implemented by the MDBX-backed adapter wired in at block processing time.
type ContentStoreDB interface {
	ContentGet(hash []byte) ([]byte, error)
	ContentPut(hash []byte, data []byte) error
	ContentHas(hash []byte) (bool, error)
}

// CAS function selectors (first byte of input).
const (
	casStore  byte = 0x00 // store(data) → hash
	casLoad   byte = 0x01 // load(hash) → data
	casExists byte = 0x02 // exists(hash) → bool
	casSize   byte = 0x03 // size(hash) → uint256
)

var (
	errCASDataTooLarge   = errors.New("content store: data exceeds max size")
	errCASInvalidInput   = errors.New("content store: invalid input")
	errCASNotFound       = errors.New("content store: hash not found")
	errCASWriteInStatic  = errors.New("content store: write in static context")
	errCASNoDB           = errors.New("content store: database not available")
)

// contentStore implements the content-addressed storage precompile.
//
// This is an N42-specific precompile that provides native content-addressed
// blob storage at the EVM level. Data is stored by keccak256 hash and is
// immutable (content-addressed guarantees idempotent writes).
//
// Unlike SSTORE (32 bytes per slot, ~690 gas/byte), CAS supports up to 24KB
// per entry at ~200 gas/byte for writes and ~50 gas/byte for reads.
type contentStore struct{}

func (c *contentStore) RequiredGas(input []byte) uint64 {
	if len(input) < 1 {
		return params.ContentExistsGas // minimum
	}
	switch input[0] {
	case casStore:
		dataLen := uint64(len(input) - 1)
		return params.ContentStoreBaseGas + params.ContentStorePerByteGas*dataLen
	case casLoad:
		// Worst-case: charge for max size read, refund unused in RunStateful
		return params.ContentLoadBaseGas + params.ContentLoadPerByteGas*uint64(params.ContentStoreMaxSize)
	case casExists:
		return params.ContentExistsGas
	case casSize:
		return params.ContentSizeGas
	default:
		return params.ContentExistsGas
	}
}

func (c *contentStore) Run(input []byte) ([]byte, error) {
	// Stateless Run is not supported — this precompile requires ContentStoreDB.
	return nil, errCASNoDB
}

// RunWithDB executes the precompile with database access.
// Called from the EVM's stateful precompile dispatch path.
func (c *contentStore) RunWithDB(db ContentStoreDB, input []byte, readOnly bool) ([]byte, error) {
	if db == nil {
		return nil, errCASNoDB
	}
	if len(input) < 1 {
		return nil, errCASInvalidInput
	}

	switch input[0] {
	case casStore:
		return c.runStore(db, input[1:], readOnly)
	case casLoad:
		return c.runLoad(db, input[1:])
	case casExists:
		return c.runExists(db, input[1:])
	case casSize:
		return c.runSize(db, input[1:])
	default:
		return nil, fmt.Errorf("content store: unknown selector 0x%02x", input[0])
	}
}

func (c *contentStore) runStore(db ContentStoreDB, data []byte, readOnly bool) ([]byte, error) {
	if readOnly {
		return nil, errCASWriteInStatic
	}
	if len(data) > params.ContentStoreMaxSize {
		return nil, errCASDataTooLarge
	}

	hash := crypto.Keccak256Hash(data)

	// Idempotent: if data already exists, skip write
	has, err := db.ContentHas(hash[:])
	if err != nil {
		return nil, err
	}
	if !has {
		if err := db.ContentPut(hash[:], data); err != nil {
			return nil, err
		}
	}
	return hash[:], nil
}

func (c *contentStore) runLoad(db ContentStoreDB, input []byte) ([]byte, error) {
	if len(input) < 32 {
		return nil, errCASInvalidInput
	}
	hash := input[:32]
	data, err := db.ContentGet(hash)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, errCASNotFound
	}
	return data, nil
}

func (c *contentStore) runExists(db ContentStoreDB, input []byte) ([]byte, error) {
	if len(input) < 32 {
		return nil, errCASInvalidInput
	}
	has, err := db.ContentHas(input[:32])
	if err != nil {
		return nil, err
	}
	result := make([]byte, 32)
	if has {
		result[31] = 1
	}
	return result, nil
}

func (c *contentStore) runSize(db ContentStoreDB, input []byte) ([]byte, error) {
	if len(input) < 32 {
		return nil, errCASInvalidInput
	}
	data, err := db.ContentGet(input[:32])
	if err != nil {
		return nil, err
	}
	if data == nil {
		return make([]byte, 32), nil // size 0 for non-existent
	}
	size := uint64(len(data))
	result := make([]byte, 32)
	result[24] = byte(size >> 56)
	result[25] = byte(size >> 48)
	result[26] = byte(size >> 40)
	result[27] = byte(size >> 32)
	result[28] = byte(size >> 24)
	result[29] = byte(size >> 16)
	result[30] = byte(size >> 8)
	result[31] = byte(size)
	return result, nil
}
