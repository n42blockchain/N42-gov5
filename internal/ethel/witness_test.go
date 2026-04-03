package ethel

import (
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
func (m *mockReader) ReadAccountStorage(addr types.Address, inc uint16, key *types.Hash) ([]byte, error) {
	if key[0] == 0x01 {
		return []byte{0x42, 0xAB}, nil
	}
	return nil, nil
}
func (m *mockReader) ReadAccountCode(types.Address, uint16, types.Hash) ([]byte, error) {
	return []byte{0x60, 0x00}, nil // some code
}
func (m *mockReader) ReadAccountCodeSize(types.Address, uint16, types.Hash) (int, error) {
	return 2, nil
}
func (m *mockReader) ReadAccountIncarnation(types.Address) (uint16, error) { return 0, nil }

var _ state.StateReader = (*mockReader)(nil)

func TestWitnessStream(t *testing.T) {
	w := NewWitnessStateReader(&mockReader{})

	// Access absent account → [0x00]
	w.ReadAccountData(types.Address{0x22})
	// Access present account → [len][V2 data...]
	w.ReadAccountData(types.Address{0x11})
	// Access absent storage → [0x00]
	w.ReadAccountStorage(types.Address{0x11}, 0, &types.Hash{0x99})
	// Access present storage → [len][value...]
	w.ReadAccountStorage(types.Address{0x11}, 0, &types.Hash{0x01})
	// Code read → NOT recorded
	w.ReadAccountCode(types.Address{0x11}, 0, types.Hash{})

	data := w.Encode()

	// Parse stream
	pos := 0
	// Entry 0: absent account
	if data[pos] != 0 {
		t.Fatalf("entry 0: want len=0, got %d", data[pos])
	}
	pos++

	// Entry 1: present account
	accLen := int(data[pos])
	pos++
	if accLen == 0 {
		t.Fatal("entry 1: want non-zero account")
	}
	t.Logf("account V2: %d bytes = %x", accLen, data[pos:pos+accLen])
	pos += accLen

	// Entry 2: absent storage
	if data[pos] != 0 {
		t.Fatalf("entry 2: want len=0, got %d", data[pos])
	}
	pos++

	// Entry 3: present storage [0x42, 0xAB]
	stoLen := int(data[pos])
	pos++
	if stoLen != 2 {
		t.Fatalf("entry 3: want len=2, got %d", stoLen)
	}
	if data[pos] != 0x42 || data[pos+1] != 0xAB {
		t.Fatalf("entry 3: want [42 AB], got %x", data[pos:pos+2])
	}
	pos += stoLen

	// Should be at end (code NOT recorded).
	if pos != len(data) {
		t.Errorf("stream has %d extra bytes (code leaked?)", len(data)-pos)
	}

	t.Logf("Stream: %d bytes, 4 entries, no code ✓", len(data))
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
