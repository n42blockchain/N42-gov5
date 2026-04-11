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
// Genesis configuration for the v2 replay engine. Defines HardForkAlloc
// (balance injections originally placed at block 7601200) and
// SystemContract (EIP-2935 history storage and Pectra system contracts)
// so the rebuilt chain embeds them at block 0.

package replay

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// HardForkAlloc is a genesis balance injection (moved from hardfork_alloc.json).
type HardForkAlloc struct {
	Address types.Address
	Amount  *uint256.Int
}

// DefaultHardForkAllocs returns allocations moved from block 7601200 to genesis.
func DefaultHardForkAllocs() []HardForkAlloc {
	amount, _ := uint256.FromHex("0x9B18AB5DF7180B6B8000000")
	return []HardForkAlloc{
		{Address: types.HexToAddress("0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091"), Amount: amount},
	}
}

// SystemContract defines a pre-deployed system contract for genesis.
type SystemContract struct {
	Address types.Address
	Code    []byte
	Nonce   uint64
}

// DefaultSystemContracts returns Prague/Pectra system contracts to pre-deploy.
func DefaultSystemContracts() []SystemContract {
	return []SystemContract{
		{ // EIP-2935 History Storage
			Address: types.HexToAddress("0x0000F90827F1C53a10CB7A02335B175320002935"),
			Code:    types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe1460575767ffffffffffffffff5765015150a06020527f0167ffffffffffffffff8111615058578060005b905b60008360408203523390523661003f57610000565b6020357f806101c557610000576000604035523290"),
			Nonce:   1,
		},
		{ // EIP-7002 Withdrawal Requests
			Address: types.HexToAddress("0x00000961EF480EB55E80D19AD83579A64C007002"),
			Code:    types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe14604457602036146024575f5ffd5b620180005f350680515f80fd5b5f35801560495762018000153560495763ffffffff60023516545f5260205ff35b5f5ffd"),
			Nonce:   1,
		},
		{ // EIP-7251 Consolidation Requests
			Address: types.HexToAddress("0x0000BBDDC7CE488642FB579F8B00F3A590007251"),
			Code:    types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe14604457602036146024575f5ffd5b620180005f350680515f80fd5b5f35801560495762018000153560495763ffffffff60023516545f5260205ff35b5f5ffd"),
			Nonce:   1,
		},
		{ // EIP-4788 Beacon Roots
			Address: types.HexToAddress("0x000F3df6D732807Ef1319fB7B8bB8522d0Beac02"),
			Code:    types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe14604d57602036146024575f5ffd5b5f35801560495762001fff810690815414603c575f5ffd5b62001fff01545f5260205ff35b5f5ffd5b62001fff42064281555f359062001fff015500"),
			Nonce:   1,
		},
	}
}

// InitGenesisState applies hard-fork allocs and system contracts to genesis state.
func InitGenesisState(ibs *state.IntraBlockState) {
	for _, alloc := range DefaultHardForkAllocs() {
		if !ibs.Exist(alloc.Address) {
			ibs.CreateAccount(alloc.Address, false)
		}
		ibs.AddBalance(alloc.Address, alloc.Amount)
	}
	for _, sc := range DefaultSystemContracts() {
		if !ibs.Exist(sc.Address) {
			ibs.CreateAccount(sc.Address, true)
		}
		ibs.SetNonce(sc.Address, sc.Nonce)
		ibs.SetCode(sc.Address, sc.Code)
	}
}
