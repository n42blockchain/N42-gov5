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
	if len(data) != 8 { // 4 bytes acc count + 4 bytes sto count
		t.Errorf("empty journal: got %d bytes, want 8", len(data))
	}
	accCount := binary.LittleEndian.Uint32(data[0:4])
	stoCount := binary.LittleEndian.Uint32(data[4:8])
	if accCount != 0 || stoCount != 0 {
		t.Errorf("counts: acc=%d sto=%d, want 0,0", accCount, stoCount)
	}
}

func TestEncodeLeavesJournal_WithData(t *testing.T) {
	accCS := changeset.NewAccountChangeSet()
	addr := make([]byte, 20)
	addr[19] = 0x42
	accCS.Add(addr, []byte{0x01, 0x02})

	stoCS := changeset.NewStorageChangeSet()
	stoKey := make([]byte, 54) // addr(20)+inc(2)+slot(32)
	stoKey[19] = 0x42
	stoKey[53] = 0x01
	stoCS.Add(stoKey, []byte{0xFF})

	acc := &account.StateAccount{Nonce: 10}
	acc.Balance.SetUint64(999)

	data := EncodeLeavesJournal(accCS, stoCS,
		func(addr types.Address) *account.StateAccount { return acc },
		func(addr types.Address, key types.Hash) []byte { return []byte{0xAB} },
	)

	if len(data) < 8 {
		t.Fatalf("journal too short: %d bytes", len(data))
	}
	accCount := binary.LittleEndian.Uint32(data[0:4])
	if accCount != 1 {
		t.Errorf("acc count: got %d want 1", accCount)
	}
	t.Logf("Journal: %d bytes, %d acc, entries OK", len(data), accCount)
}
