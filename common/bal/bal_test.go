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

package bal

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func u256(hex string) *uint256.Int {
	return uint256.MustFromHex(hex)
}

func slot(n uint64) types.Hash {
	var h types.Hash
	uint256.NewInt(n).WriteToSlice(h[:])
	return h
}

// TestBuilderHashMatchesFixture rebuilds the access list of EEST v21.0.2
// amsterdam/eip7928 test_bal_code_changes (block 1), fed to the builder out of
// order, and checks the hash committed in the fixture header.
func TestBuilderHashMatchesFixture(t *testing.T) {
	b := NewBuilder()

	coinbase := types.HexToAddress("0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba")
	sender := types.HexToAddress("0xf6c3a9edc1afa0ad5b720e4d42e1437c43d3b3ff")
	factory := types.HexToAddress("0xca5f5dc38fb090e840c55d0518d300e78ca58bf9")
	created := types.HexToAddress("0xf9e6021ec1a54b4226275c9842fdfe0f849a7cf4")
	history := types.HexToAddress("0x0000f90827f1c53a10cb7a02335b175320002935")
	beacon := types.HexToAddress("0x000f3df6d732807ef1319fb7b8bb8522d0beac02")

	// Index 0: the beacon-root and history system calls.
	b.StorageRead(beacon, slot(0x200b))
	b.StorageWrite(0, beacon, slot(0x0c), u256("0xc"))
	b.StorageWrite(0, history, slot(0), u256("0x94256f1f7a0a32aec6c8adab45fc35254781c5ddaecdf7b8444eed77cb3d4c9b"))

	// Index 1: the transaction.
	b.NonceChange(1, created, 1)
	b.CodeChange(1, created, []byte{0x00})
	b.NonceChange(1, sender, 1)
	b.BalanceChange(1, sender, u256("0x33b2e3c9fd0803ce7df9f9a"))
	b.NonceChange(1, factory, 2)
	b.BalanceChange(1, coinbase, u256("0x9b685"))

	// Index 2: the request system calls read four slots of each queue.
	for _, addr := range []string{
		"0x0000bff46984e3725691fa540a8c7589300d8282",
		"0x00000961ef480eb55e80d19ad83579a64c007002",
		"0x000064d678505ad48f8ccb093bc65613800e8282",
		"0x0000bbddc7ce488642fb579f8b00f3a590007251",
	} {
		for s := uint64(3); ; s-- {
			b.StorageRead(types.HexToAddress(addr), slot(s))
			if s == 0 {
				break
			}
		}
	}

	list := b.Build()
	got, err := list.Hash()
	if err != nil {
		t.Fatal(err)
	}
	want := types.HexToHash("0x237b77d11defcc5d159e3ef93b91e2149bdec9c783f06d910eb6a9f2e60a7a21")
	if got != want {
		t.Fatalf("hash %s, want %s", got.Hex(), want.Hex())
	}
	if n := list.ItemCount(); n != 10+4*4+2+1 {
		t.Fatalf("item count %d, want %d", n, 10+4*4+2+1)
	}
}

// TestEmptyListHash checks the empty list hashes to keccak256(rlp([])), the
// value an Amsterdam genesis header carries.
func TestEmptyListHash(t *testing.T) {
	got, err := BlockAccessList{}.Hash()
	if err != nil {
		t.Fatal(err)
	}
	want := types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347")
	if got != want {
		t.Fatalf("hash %s, want %s", got.Hex(), want.Hex())
	}
}

// TestWriteSupersedesRead checks a slot read before and after being written
// is listed only as a write, and that a read-only slot stays a read.
func TestWriteSupersedesRead(t *testing.T) {
	b := NewBuilder()
	addr := types.HexToAddress("0x01")
	b.StorageRead(addr, slot(1))
	b.StorageRead(addr, slot(2))
	b.StorageWrite(1, addr, slot(1), u256("0x5"))
	b.StorageRead(addr, slot(1))
	list := b.Build()
	if len(list) != 1 || len(list[0].StorageChanges) != 1 || len(list[0].StorageReads) != 1 {
		t.Fatalf("unexpected list %+v", list)
	}
	if list[0].StorageReads[0].Uint64() != 2 {
		t.Fatalf("read slot %s, want 2", list[0].StorageReads[0].Hex())
	}
}

// TestValidateSize checks the item limit is gasLimit / ItemCost, inclusive.
func TestValidateSize(t *testing.T) {
	b := NewBuilder()
	b.StorageRead(types.HexToAddress("0x01"), slot(1))
	list := b.Build() // two items
	if err := list.ValidateSize(2 * ItemCost); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	if err := list.ValidateSize(2*ItemCost - 1); err == nil {
		t.Fatal("over the limit: accepted")
	}
}
