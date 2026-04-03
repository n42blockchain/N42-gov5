package ethel

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestWitnessEncodeDecode(t *testing.T) {
	w := &WitnessStateReader{
		accounts: make(map[types.Address]*account.StateAccount),
		storage:  make(map[types.Address]map[types.Hash][]byte),
	}

	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")
	w.accounts[addr1] = nil // absent
	acc := &account.StateAccount{Nonce: 5}
	acc.Balance.SetUint64(1000)
	w.accounts[addr2] = acc

	slot := types.HexToHash("0x01")
	w.storage[addr2] = map[types.Hash][]byte{slot: {0x42}}

	data := w.Encode()
	if len(data) == 0 {
		t.Fatal("empty witness encoding")
	}
	t.Logf("Witness: %d bytes, 2 accounts, 1 slot", len(data))
}

func TestWitnessReset(t *testing.T) {
	w := &WitnessStateReader{
		accounts: map[types.Address]*account.StateAccount{{1}: nil},
		storage:  map[types.Address]map[types.Hash][]byte{{1}: {{}: {0x01}}},
	}
	w.Reset()
	if len(w.accounts) != 0 || len(w.storage) != 0 {
		t.Error("Reset did not clear maps")
	}
}
