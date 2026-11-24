// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// The Profile-1 allow-list is exactly {0x00, 0x01, 0x02, 0x04}: blob (0x03),
// post-quantum (0x05), the N42 range 0x50-0x5F and every other byte are out.
func TestProfile1TxTypeAllowed(t *testing.T) {
	for typ := 0; typ <= 0xff; typ++ {
		want := typ == 0x00 || typ == 0x01 || typ == 0x02 || typ == 0x04
		if got := Profile1TxTypeAllowed(uint8(typ)); got != want {
			t.Fatalf("type 0x%02x: allowed %v, want %v", typ, got, want)
		}
	}
}

// AsMessage carries the transaction's type; NewMessage reads as legacy.
func TestMessageTxType(t *testing.T) {
	to := types.HexToAddress("0x2222222222222222222222222222222222222222")
	for _, inner := range []TxData{
		&LegacyTx{GasPrice: uint256.NewInt(1), To: &to, Value: uint256.NewInt(0)},
		&DynamicFeeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1), To: &to, Value: uint256.NewInt(0)},
		&SetCodeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1), To: &to, Value: uint256.NewInt(0)},
	} {
		tx := NewTx(inner)
		tx.SetFrom(to)
		msg, err := tx.AsMessage(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if msg.TxType() != tx.Type() {
			t.Fatalf("message type 0x%02x, want 0x%02x", msg.TxType(), tx.Type())
		}
	}
	if m := NewMessage(to, &to, 0, uint256.NewInt(0), 0, nil, nil, nil, nil, nil, nil, nil, false, false); m.TxType() != LegacyTxType {
		t.Fatalf("NewMessage type 0x%02x, want legacy", m.TxType())
	}
}
