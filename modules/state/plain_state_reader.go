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
// PlainStateReader for reading un-hashed account/storage tables.
// ReadAccountData fetches and decodes an encoded StateAccount from
// modules.Account via DecodeForStorage.
// ReadAccountStorage composes a plain storage key using
// modules.PlainGenerateCompositeStorageKey and reads modules.Storage.
// The reader is stateless and holds only a kv.Getter reference.

package state

import (
	"bytes"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// CodeSource is an address-indexed bytecode lookup, typically backed by
// the codes.cidx + codes.NNNN.cdat freezer produced by code-import2fz.
// Used by PlainStateReader to skip the MDBX Code table on minimal-client
// cold starts (the bundle ships codes.cdat but no Code MDBX table; the
// 15 GB Code population step happens only as forward-replay deploys new
// contracts past the bundle snapshot).
//
// GetCode returns nil with no error when the address is not present.
// Implementations must be goroutine-safe — PlainStateReader is shared
// across executor workers.
type CodeSource interface {
	GetCode(address types.Address) ([]byte, error)
}

// PlainStateReader reads data from so called "plain state".
// Data in the plain state is stored using un-hashed account/storage items
// as opposed to the "normal" state that uses hashes of merkle paths to store items.
type PlainStateReader struct {
	db      kv.Getter
	codeSrc CodeSource // optional; set via SetCodeSource
}

func NewPlainStateReader(db kv.Getter) *PlainStateReader {
	return &PlainStateReader{
		db: db,
	}
}

// SetCodeSource wires up a bytecode source consulted before the MDBX
// Code table. nil disables the source. Wired by the CLI when a
// codes.cidx is detected under the datadir.
func (r *PlainStateReader) SetCodeSource(src CodeSource) {
	r.codeSrc = src
}

func (r *PlainStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	enc, err := r.db.GetOne(modules.Account, address.Bytes())
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		return nil, nil
	}
	var a account.StateAccount
	if err = a.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *PlainStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())
	enc, err := r.db.GetOne(modules.Storage, compositeKey)
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		return nil, nil
	}
	return enc, nil
}

func (r *PlainStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if bytes.Equal(codeHash[:], emptyCodeHash) {
		return nil, nil
	}
	// Try codes.cdat first (address-keyed) — saves a 15 GB MDBX Code
	// table population step for fresh clients running off a bundle.
	// The freezer is a snapshot at bundle-end so a contract redeployed
	// during forward-replay (SELFDESTRUCT + new CREATE) returns the OLD
	// bytecode here; verify keccak matches the requested codeHash, fall
	// through to MDBX if not. keccak on ~1 KB bytecode is ~200ns on
	// Ryzen (~5 GB/s) — negligible vs MDBX GetOne lookup cost.
	if r.codeSrc != nil {
		code, err := r.codeSrc.GetCode(address)
		if err != nil {
			return nil, err
		}
		if len(code) > 0 && crypto.Keccak256Hash(code) == codeHash {
			return code, nil
		}
	}
	code, err := r.db.GetOne(modules.Code, codeHash[:])
	if err != nil {
		return nil, err
	}
	if len(code) == 0 {
		return nil, nil
	}
	return code, nil
}

func (r *PlainStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	return len(code), err
}
