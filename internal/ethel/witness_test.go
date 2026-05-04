package ethel

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

type mockReader struct{}

func (m *mockReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	if addr[0] == 0x11 {
		acc := &account.StateAccount{Nonce: 5}
		acc.Balance.SetUint64(1000)
		return acc, nil
	}
	return nil, nil // absent
}
func (m *mockReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	if key[0] == 0x01 {
		return []byte{0x42, 0xAB}, nil
	}
	return nil, nil
}
func (m *mockReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error) {
	return []byte{0x60, 0x00}, nil // some code
}
func (m *mockReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 2, nil
}

var _ state.StateReader = (*mockReader)(nil)

// TestWitnessV2Format verifies the sorted-map wire format: a version
// byte, then a varint-counted list of (addr, encoded-account) pairs in
// ascending addr order, then a varint-counted list of
// (addr, slot, raw-value) triples in ascending (addr, slot) order. Each
// entry's value carries an explicit varint length so deletions/empty
// reads are encoded as len=0 in-place. Order of recording is irrelevant.
func TestWitnessV2Format(t *testing.T) {
	w := NewWitnessStateReader(&mockReader{})

	// Record reads in arbitrary order — the encoded blob must come out
	// sorted regardless.
	w.ReadAccountStorage(types.Address{0x11}, &types.Hash{0x99}) // absent
	w.ReadAccountStorage(types.Address{0x11}, &types.Hash{0x01}) // present
	w.ReadAccountData(types.Address{0x22})                       // absent
	w.ReadAccountData(types.Address{0x11})                       // present
	w.ReadAccountCode(types.Address{0x11}, types.Hash{})         // not recorded

	data := w.Encode()

	if len(data) == 0 || data[0] != witnessFormatV2 {
		t.Fatalf("missing/wrong version byte: %x", data[:1])
	}
	pos := 1

	// Account section: 2 entries (0x11..., 0x22... — sorted asc).
	accCount, n := binary.Uvarint(data[pos:])
	pos += n
	if accCount != 2 {
		t.Fatalf("acc_count: want 2, got %d", accCount)
	}

	// First account: 0x11... (present, len > 0).
	if data[pos] != 0x11 {
		t.Fatalf("first acc addr: want 0x11..., got %x", data[pos:pos+20])
	}
	pos += 20
	encLen, n := binary.Uvarint(data[pos:])
	pos += n
	if encLen == 0 {
		t.Fatal("first account should be present (len > 0)")
	}
	pos += int(encLen)

	// Second account: 0x22... (absent, len == 0).
	if data[pos] != 0x22 {
		t.Fatalf("second acc addr: want 0x22..., got %x", data[pos:pos+20])
	}
	pos += 20
	encLen, n = binary.Uvarint(data[pos:])
	pos += n
	if encLen != 0 {
		t.Fatalf("absent account should have len=0, got %d", encLen)
	}

	// Storage section: 2 entries for addr 0x11... — slot 0x01 then 0x99 (sorted asc).
	stoCount, n := binary.Uvarint(data[pos:])
	pos += n
	if stoCount != 2 {
		t.Fatalf("sto_count: want 2, got %d", stoCount)
	}

	// First storage entry: addr 0x11..., slot 0x01... (present, [0x42 0xAB]).
	pos += 20 // addr
	if data[pos] != 0x01 {
		t.Fatalf("first sto slot: want 0x01..., got %x", data[pos:pos+32])
	}
	pos += 32
	valLen, n := binary.Uvarint(data[pos:])
	pos += n
	if valLen != 2 || data[pos] != 0x42 || data[pos+1] != 0xAB {
		t.Fatalf("first sto val: want [42 AB] (len=2), got len=%d %x", valLen, data[pos:pos+int(valLen)])
	}
	pos += int(valLen)

	// Second storage entry: addr 0x11..., slot 0x99... (absent, len=0).
	pos += 20
	if data[pos] != 0x99 {
		t.Fatalf("second sto slot: want 0x99..., got %x", data[pos:pos+32])
	}
	pos += 32
	valLen, n = binary.Uvarint(data[pos:])
	pos += n
	if valLen != 0 {
		t.Fatalf("absent storage should have len=0, got %d", valLen)
	}

	if pos != len(data) {
		t.Errorf("stream has %d unaccounted bytes (code leaked?)", len(data)-pos)
	}
}

func TestWitnessReset(t *testing.T) {
	w := NewWitnessStateReader(&mockReader{})
	w.ReadAccountData(types.Address{0x11})
	if len(w.Encode()) == 0 {
		t.Fatal("should have data")
	}
	w.Reset()
	if len(w.Encode()) != 0 {
		t.Fatal("should be empty after reset")
	}
}
