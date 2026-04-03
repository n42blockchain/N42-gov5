package ethel

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/modules/changeset"
)

func TestEncodeAccountChanges(t *testing.T) {
	cs := changeset.NewAccountChangeSet()
	addr := make([]byte, 20)
	addr[19] = 0x42
	cs.Add(addr, []byte{0x03, 0x05, 0x02, 0x03, 0xe8}) // V2: nonce=5, balance=1000

	data := EncodeAccountChanges(cs)
	if data == nil {
		t.Fatal("nil encoding")
	}

	count := binary.LittleEndian.Uint16(data[0:2])
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
	// addr(20) + valLen(1) + value(5) = 26 bytes + 2 header = 28
	if len(data) != 28 {
		t.Errorf("size: got %d, want 28", len(data))
	}
	t.Logf("Account changeset: %d bytes for 1 entry", len(data))
}

func TestEncodeStorageChanges_Grouped(t *testing.T) {
	cs := changeset.NewStorageChangeSet()

	// Same address, 3 different slots
	makeKey := func(addrByte, slotByte byte) []byte {
		key := make([]byte, 54) // addr(20)+inc(2)+slot(32)
		key[19] = addrByte
		key[20] = 0    // incarnation high
		key[21] = 1    // incarnation low = 1
		key[53] = slotByte
		return key
	}

	cs.Add(makeKey(0x42, 0x01), []byte{0xAA})
	cs.Add(makeKey(0x42, 0x02), []byte{0xBB, 0xCC})
	cs.Add(makeKey(0x42, 0x03), []byte{})  // deleted slot

	// Different address, 1 slot
	cs.Add(makeKey(0x99, 0x01), []byte{0xFF})

	data := EncodeStorageChanges(cs)
	if data == nil {
		t.Fatal("nil encoding")
	}

	addrCount := binary.LittleEndian.Uint16(data[0:2])
	if addrCount != 2 {
		t.Errorf("addr groups: got %d, want 2", addrCount)
	}

	// Compare with naive flat encoding
	naiveSize := 2 + 4*(4+54) + (1 + 2 + 0 + 1) // count + 4 entries with keyLen/valLen overhead
	t.Logf("Grouped: %d bytes, Naive would be: ~%d bytes, Saved: ~%d%%",
		len(data), naiveSize, (naiveSize-len(data))*100/naiveSize)
}

func TestEncodeStorageChanges_Empty(t *testing.T) {
	data := EncodeStorageChanges(nil)
	if data != nil {
		t.Error("nil input should produce nil output")
	}
}
