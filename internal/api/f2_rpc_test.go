package api

import (
	"testing"

	"github.com/holiman/uint256"

	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/bodyf2"
)

func TestNewRPCTransactionFromF2(t *testing.T) {
	to := types.Address{9}
	var from types.Address
	from[0] = 1
	ftx := &bodyf2.F2Tx{
		Type: 2, From: from, To: &to, Nonce: 7, Gas: 50000,
		Value: uint256.NewInt(1500), GasFeeCap: uint256.NewInt(30), GasTipCap: uint256.NewInt(2),
		Data: []byte{0xde, 0xad},
		Access: []bodyf2.F2AccessTuple{
			{Address: types.Address{3}, StorageKeys: [][32]byte{{1}, {2}}},
		},
	}
	var bh types.Hash
	bh[0] = 0xab
	r := newRPCTransactionFromF2(ftx, bh, 12345, 4)

	if uint64(r.Type) != 2 || uint64(r.Nonce) != 7 || uint64(r.Gas) != 50000 {
		t.Errorf("scalar fields: type=%d nonce=%d gas=%d", r.Type, r.Nonce, r.Gas)
	}
	if r.Value.ToInt().Uint64() != 1500 {
		t.Errorf("value = %v", r.Value.ToInt())
	}
	if r.To == nil {
		t.Error("To should be set")
	}
	if r.GasFeeCap == nil || r.GasTipCap == nil {
		t.Error("dynamic-fee caps should be set for type 2")
	}
	if r.Accesses == nil || len(*r.Accesses) != 1 || len((*r.Accesses)[0].StorageKeys) != 2 {
		t.Errorf("access list not mapped: %+v", r.Accesses)
	}
	if r.BlockHash == nil || r.BlockNumber == nil || r.TransactionIndex == nil {
		t.Error("block position fields should be set")
	}
	// The defining F2 property: no signature, no canonical hash.
	if r.V != nil || r.R != nil || r.S != nil {
		t.Error("F2 tx must have nil V/R/S")
	}
	if r.Hash != (avmcommon.Hash{}) {
		t.Errorf("F2 tx must have zero Hash, got %x", r.Hash)
	}
}
