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

package rawdb

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"google.golang.org/protobuf/proto"
)

type rawdbBlockStub struct {
	number *uint256.Int
}

func (b *rawdbBlockStub) Header() block.IHeader                           { return b }
func (b *rawdbBlockStub) Body() block.IBody                               { return nil }
func (b *rawdbBlockStub) Transaction(types.Hash) *transaction.Transaction { return nil }
func (b *rawdbBlockStub) Transactions() []*transaction.Transaction        { return nil }
func (b *rawdbBlockStub) Number64() *uint256.Int                          { return b.number }
func (b *rawdbBlockStub) BaseFee64() *uint256.Int                         { return uint256.NewInt(0) }
func (b *rawdbBlockStub) Difficulty() *uint256.Int                        { return uint256.NewInt(1) }
func (b *rawdbBlockStub) Time() uint64                                    { return 0 }
func (b *rawdbBlockStub) GasLimit() uint64                                { return 0 }
func (b *rawdbBlockStub) GasUsed() uint64                                 { return 0 }
func (b *rawdbBlockStub) Nonce() uint64                                   { return 0 }
func (b *rawdbBlockStub) Coinbase() types.Address                         { return types.Address{} }
func (b *rawdbBlockStub) ParentHash() types.Hash                          { return types.Hash{} }
func (b *rawdbBlockStub) TxHash() types.Hash                              { return types.Hash{} }
func (b *rawdbBlockStub) Hash() types.Hash                                { return types.Hash{} }
func (b *rawdbBlockStub) ToProtoMessage() proto.Message                   { return nil }
func (b *rawdbBlockStub) FromProtoMessage(proto.Message) error            { return nil }
func (b *rawdbBlockStub) Marshal() ([]byte, error)                        { return nil, nil }
func (b *rawdbBlockStub) Unmarshal([]byte) error                          { return nil }
func (b *rawdbBlockStub) StateRoot() types.Hash                           { return types.Hash{} }
func (b *rawdbBlockStub) WithSeal(block.IHeader) *block.Block             { return nil }

func TestTdStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	hash, td := types.Hash{}, uint256.NewInt(314)

	// Read non-existent TD
	entry, err := ReadTd(tx, hash, 0)
	if err != nil {
		t.Fatalf("ReadTd failed: %v", err)
	}
	if entry != nil {
		t.Fatalf("Non existent TD returned: %v", entry)
	}

	// Write and verify
	if err := WriteTd(tx, hash, 0, td); err != nil {
		t.Fatalf("WriteTd failed: %v", err)
	}
	entry, err = ReadTd(tx, hash, 0)
	if err != nil {
		t.Fatalf("ReadTd failed: %v", err)
	}
	if entry == nil {
		t.Fatal("Stored TD not found")
	}
	if entry.Cmp(td) != 0 {
		t.Fatalf("Retrieved TD mismatch: have %v, want %v", entry, td)
	}

	// Delete and verify
	if err := TruncateTd(tx, 0); err != nil {
		t.Fatalf("TruncateTd failed: %v", err)
	}
	entry, err = ReadTd(tx, hash, 0)
	if err != nil {
		t.Fatalf("ReadTd failed: %v", err)
	}
	if entry != nil {
		t.Fatalf("Deleted TD returned: %v", entry)
	}

	// Write TD at block 100, verify block 101 returns nil
	zeroTd := uint256.NewInt(0)
	if err := WriteTd(tx, hash, 100, zeroTd); err != nil {
		t.Fatalf("WriteTd failed: %v", err)
	}

	entry, err = ReadTd(tx, hash, 101)
	if err != nil {
		t.Fatalf("ReadTd at block 101 failed: %v", err)
	}
	if entry != nil {
		t.Fatalf("ReadTd at non-existent block 101 returned: %v", entry)
	}

	entry, err = ReadTd(tx, hash, 100)
	if err != nil {
		t.Fatalf("ReadTd at block 100 failed: %v", err)
	}
	if entry.Cmp(zeroTd) != 0 {
		t.Fatalf("TD at block 100 mismatch: have %v, want %v", entry, zeroTd)
	}
}

func TestRequireBlockNumberRejectsNilNumber(t *testing.T) {
	_, err := requireBlockNumber(&rawdbBlockStub{}, "block number unavailable")
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("requireBlockNumber() error = %v", err)
	}
}

func TestRequireHeaderNumberRejectsNilNumber(t *testing.T) {
	_, err := requireHeaderNumber(&block.Header{}, "header number unavailable")
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("requireHeaderNumber() error = %v", err)
	}
}
