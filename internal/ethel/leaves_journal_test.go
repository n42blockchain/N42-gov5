package ethel

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
)

func TestEncodeLeavesJournal_Empty(t *testing.T) {
	data := EncodeLeavesJournal(nil, nil, nil, nil)
	if len(data) != 8 { // 4 acc count + 4 addr count
		t.Errorf("empty: got %d bytes, want 8", len(data))
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
	)

	numAcc := binary.LittleEndian.Uint32(data[0:4])
	if numAcc != 1 {
		t.Errorf("acc count: got %d, want 1", numAcc)
	}
	t.Logf("Accounts-only journal: %d bytes", len(data))
}

func TestEncodeLeavesJournal_StorageGrouped(t *testing.T) {
	stoCS := changeset.NewStorageChangeSet()

	// Same address, 3 slots
	makeKey := func(addrByte, slotByte byte) []byte {
		key := make([]byte, 54)
		key[19] = addrByte
		key[21] = 1 // inc=1
		key[53] = slotByte
		return key
	}
	stoCS.Add(makeKey(0x42, 0x01), []byte{0xAA})
	stoCS.Add(makeKey(0x42, 0x02), []byte{0xBB})
	stoCS.Add(makeKey(0x42, 0x03), nil) // deleted

	// Different address, 1 slot
	stoCS.Add(makeKey(0x99, 0x01), []byte{0xFF})

	data := EncodeLeavesJournal(nil, stoCS,
		nil,
		func(a types.Address, k types.Hash) []byte { return []byte{0xCC} },
	)

	// Skip 4 bytes acc count, then read addr count
	addrCount := binary.LittleEndian.Uint32(data[4:8])
	if addrCount != 2 {
		t.Errorf("addr groups: got %d, want 2", addrCount)
	}
	t.Logf("Storage journal (grouped): %d bytes, %d groups", len(data), addrCount)
}
