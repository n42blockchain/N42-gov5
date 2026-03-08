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

package sync_pb

// Snap sync protocol message types for state range transfer.
// These types implement fastssz.Marshaler/Unmarshaler for P2P encoding.

// GetAccountRangeRequest requests a range of accounts from the flat state.
type GetAccountRangeRequest struct {
	Root     []byte `ssz-size:"32"` // state root of pivot block
	Start    []byte `ssz-max:"20"` // inclusive start address (empty = table start)
	End      []byte `ssz-max:"20"` // exclusive end address (empty = table end)
	MaxBytes uint64 //              soft response size limit
}

// AccountEntry is a single account in a range response.
type AccountEntry struct {
	Address []byte `ssz-max:"20"`    // 20-byte address
	Account []byte `ssz-max:"1024"` // encoded account data
}

// AccountRangeResponse returns a batch of accounts and continuation info.
type AccountRangeResponse struct {
	Accounts  []*AccountEntry `ssz-max:"4096"` // account entries
	NextStart []byte          `ssz-max:"20"`   // next start key (empty if done)
	Completed bool            //                true if range exhausted
}

// GetStorageRangeRequest requests storage slots for a specific account.
type GetStorageRangeRequest struct {
	Root    []byte `ssz-size:"32"` // state root of pivot block
	Account []byte `ssz-max:"20"` // account address
	Start   []byte `ssz-max:"54"` // start key: address(20)+incarnation(2)+storageKey(32)
	End     []byte `ssz-max:"54"` // end key
	MaxBytes uint64
}

// StorageEntry is a single storage slot in a range response.
type StorageEntry struct {
	Key   []byte `ssz-max:"54"` // composite key
	Value []byte `ssz-max:"32"` // storage value
}

// StorageRangeResponse returns a batch of storage slots.
type StorageRangeResponse struct {
	Slots     []*StorageEntry `ssz-max:"4096"`
	NextStart []byte          `ssz-max:"54"` // next start key (empty if done)
	Completed bool
}

// GetCodeRequest requests contract bytecodes by hash.
type GetCodeRequest struct {
	Hashes [][]byte `ssz-max:"256,32"` // list of code hashes (each 32 bytes)
}

// CodeEntry is a single code response.
type CodeEntry struct {
	Hash []byte `ssz-max:"32"`    // code hash
	Code []byte `ssz-max:"65536"` // contract bytecode (max 64KB)
}

// CodeResponse returns requested contract bytecodes.
type CodeResponse struct {
	Codes []*CodeEntry `ssz-max:"256"`
}
