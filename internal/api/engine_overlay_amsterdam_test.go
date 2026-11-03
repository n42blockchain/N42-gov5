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
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/tests/eest"
)

// An Amsterdam header commits to its EIP-7928 block access list hash and
// its EIP-7843 slot number. The expected hash is the genesis block of the
// EEST v21.0.2 fixture eip7843_slotnum/slotnum/slotnum_call_contexts.json.
func TestEthCompatibleHeaderHashAmsterdam(t *testing.T) {
	cfg, err := eest.ChainConfig("Amsterdam", nil)
	if err != nil {
		t.Fatalf("chain config: %v", err)
	}
	var (
		zeroHash     types.Hash
		withdrawals  = hash.EmptyRootHash
		requestsHash = types.HexToHash("0xe3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
		balHash      = types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347")
		blobGasUsed  uint64
		excessBlob   uint64
		slot         uint64
	)
	header := &block.Header{
		Root:                types.HexToHash("0x10f2b833c9c771419e30da1c15392b5fbf1317f3e9cefe21cc146840eda33c73"),
		TxHash:              hash.EmptyRootHash,
		ReceiptHash:         hash.EmptyRootHash,
		Difficulty:          uint256.NewInt(0),
		Number:              uint256.NewInt(0),
		GasLimit:            0x07270e00,
		Extra:               []byte{0},
		BaseFee:             uint256.NewInt(7),
		WithdrawalsHash:     &withdrawals,
		BlobGasUsed:         &blobGasUsed,
		ExcessBlobGas:       &excessBlob,
		ParentBeaconRoot:    &zeroHash,
		RequestsHash:        &requestsHash,
		BlockAccessListHash: &balHash,
		SlotNumber:          &slot,
	}
	want := types.HexToHash("0x7e80c46cb350775d7edc127c3f5dba5e73ee94786ed5444c8bb24299ede0ccf6")
	if got := ethCompatibleHeaderHash(header, cfg); got != want {
		t.Fatalf("Amsterdam header hash = %s, want %s", got.Hex(), want.Hex())
	}
}
