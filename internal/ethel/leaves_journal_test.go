package ethel

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
)

func TestEncodeLeavesJournal_Empty(t *testing.T) {
	data := EncodeLeavesJournal(nil, nil, nil, nil, nil)
	// 4 acc count + 4 addr count + 4 wipe count = 12
	if len(data) != 12 {
		t.Errorf("empty: got %d bytes, want 12", len(data))
	}
	accs, stos, wipes, err := DecodeLeavesJournal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 0 || len(stos) != 0 || len(wipes) != 0 {
		t.Errorf("expected empty, got %d accs %d stos %d wipes", len(accs), len(stos), len(wipes))
	}
}

func TestEncodeLeavesJournal_AccountsOnly(t *testing.T) {
	accCS := changeset.NewAccountChangeSet()
	addr := make([]byte, 20)
	addr[19] = 0x42
	accCS.Add(addr, []byte{0x01})

	acc := &account.StateAccount{Nonce: 10}
	acc.Balance.SetUint64(999)

	data := EncodeLeavesJournal(accCS, nil,
		func(a types.Address) *account.StateAccount { return acc },
		nil,
		nil,
	)

	numAcc := binary.LittleEndian.Uint32(data[0:4])
	if numAcc != 1 {
		t.Errorf("acc count: got %d, want 1", numAcc)
	}

	// Verify plain addr is stored (not hashed).
	if data[4+19] != 0x42 {
		t.Errorf("expected plain addr byte 0x42, got 0x%02x", data[4+19])
	}

	// Roundtrip decode.
	accs, stos, _, err := DecodeLeavesJournal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accs))
	}
	if accs[0].Address[19] != 0x42 {
		t.Errorf("decoded addr mismatch")
	}
	if accs[0].Value == nil {
		t.Errorf("expected non-nil value")
	}
	if len(stos) != 0 {
		t.Errorf("expected 0 storage, got %d", len(stos))
	}
	t.Logf("Accounts-only journal: %d bytes", len(data))
}

func TestEncodeLeavesJournal_AccountDeleted(t *testing.T) {
	accCS := changeset.NewAccountChangeSet()
	addr := make([]byte, 20)
	addr[19] = 0x01
	accCS.Add(addr, []byte{0x01})

	data := EncodeLeavesJournal(accCS, nil,
		func(a types.Address) *account.StateAccount { return nil }, // deleted
		nil,
		nil,
	)

	accs, _, _, err := DecodeLeavesJournal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 1 || accs[0].Value != nil {
		t.Errorf("expected deleted account (nil value)")
	}
}

func TestEncodeLeavesJournal_StorageGrouped(t *testing.T) {
	stoCS := changeset.NewStorageChangeSet()

	// Plain composite key: addr(20) + slot(32) = 52 bytes.
	makeKey := func(addrByte, slotByte byte) []byte {
		key := make([]byte, 52)
		key[19] = addrByte
		key[51] = slotByte
		return key
	}
	stoCS.Add(makeKey(0x42, 0x01), []byte{0xAA})
	stoCS.Add(makeKey(0x42, 0x02), []byte{0xBB})
	stoCS.Add(makeKey(0x42, 0x03), nil) // deleted

	// Different address
	stoCS.Add(makeKey(0x99, 0x01), []byte{0xFF})

	data := EncodeLeavesJournal(nil, stoCS,
		nil,
		func(a types.Address, k types.Hash) []byte { return []byte{0xCC} },
		nil,
	)

	addrCount := binary.LittleEndian.Uint32(data[4:8])
	if addrCount != 2 {
		t.Errorf("addr groups: got %d, want 2", addrCount)
	}

	// Roundtrip decode.
	accs, stos, _, err := DecodeLeavesJournal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(accs))
	}
	if len(stos) != 4 {
		t.Fatalf("expected 4 storage slots, got %d", len(stos))
	}
	// First 3 belong to addr 0x42, last to 0x99.
	if stos[0].Address[19] != 0x42 || stos[3].Address[19] != 0x99 {
		t.Errorf("address mismatch")
	}
	// Plain slot keys (not hashed).
	if stos[0].Slot[31] != 0x01 || stos[1].Slot[31] != 0x02 {
		t.Errorf("slot key mismatch")
	}
	t.Logf("Storage journal (grouped): %d bytes, %d slots", len(data), len(stos))
}

func TestEncodeLeavesJournal_Wipes(t *testing.T) {
	wipes := []types.Address{
		types.HexToAddress("0x1111111111111111111111111111111111111111"),
		types.HexToAddress("0x2222222222222222222222222222222222222222"),
	}
	data := EncodeLeavesJournal(nil, nil, nil, nil, wipes)

	accs, stos, decoded, err := DecodeLeavesJournal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 0 || len(stos) != 0 {
		t.Errorf("expected no accs/stos")
	}
	if len(decoded) != 2 {
		t.Fatalf("expected 2 wipes, got %d", len(decoded))
	}
	if decoded[0] != wipes[0] || decoded[1] != wipes[1] {
		t.Errorf("wipe addresses mismatch")
	}
}
