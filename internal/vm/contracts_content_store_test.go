package vm

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// mockContentStoreDB is an in-memory implementation for testing.
type mockContentStoreDB struct {
	data map[[32]byte][]byte
}

func newMockContentStoreDB() *mockContentStoreDB {
	return &mockContentStoreDB{data: make(map[[32]byte][]byte)}
}

func (m *mockContentStoreDB) ContentGet(hash []byte) ([]byte, error) {
	var key [32]byte
	copy(key[:], hash)
	data, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *mockContentStoreDB) ContentPut(hash []byte, data []byte) error {
	var key [32]byte
	copy(key[:], hash)
	cp := make([]byte, len(data))
	copy(cp, data)
	m.data[key] = cp
	return nil
}

func (m *mockContentStoreDB) ContentHas(hash []byte) (bool, error) {
	var key [32]byte
	copy(key[:], hash)
	_, ok := m.data[key]
	return ok, nil
}

func TestContentStoreStoreAndLoad(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	testData := []byte("hello world content-addressed storage")

	// Store
	input := append([]byte{casStore}, testData...)
	hash, err := cs.RunWithDB(db, input, false)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	expectedHash := crypto.Keccak256Hash(testData)
	if !bytes.Equal(hash, expectedHash[:]) {
		t.Fatalf("hash mismatch: got %x, want %x", hash, expectedHash)
	}

	// Load
	loadInput := append([]byte{casLoad}, hash...)
	loaded, err := cs.RunWithDB(db, loadInput, false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(loaded, testData) {
		t.Fatalf("load mismatch: got %s, want %s", loaded, testData)
	}
}

func TestContentStoreExists(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	testData := []byte("test exists")
	hash := crypto.Keccak256(testData)

	// Before store: should not exist
	input := append([]byte{casExists}, hash...)
	result, err := cs.RunWithDB(db, input, false)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if result[31] != 0 {
		t.Fatal("should not exist before store")
	}

	// Store it
	cs.RunWithDB(db, append([]byte{casStore}, testData...), false)

	// After store: should exist
	result, err = cs.RunWithDB(db, input, false)
	if err != nil {
		t.Fatalf("exists after store: %v", err)
	}
	if result[31] != 1 {
		t.Fatal("should exist after store")
	}
}

func TestContentStoreSize(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	testData := []byte("size test data 12345")

	storeInput := append([]byte{casStore}, testData...)
	hash, _ := cs.RunWithDB(db, storeInput, false)

	sizeInput := append([]byte{casSize}, hash...)
	result, err := cs.RunWithDB(db, sizeInput, false)
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	size := uint64(result[31]) | uint64(result[30])<<8 | uint64(result[29])<<16 | uint64(result[28])<<24
	if size != uint64(len(testData)) {
		t.Fatalf("size = %d, want %d", size, len(testData))
	}
}

func TestContentStoreMaxSize(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	bigData := make([]byte, params.ContentStoreMaxSize+1)
	input := append([]byte{casStore}, bigData...)
	_, err := cs.RunWithDB(db, input, false)
	if err != errCASDataTooLarge {
		t.Fatalf("expected errCASDataTooLarge, got %v", err)
	}
}

func TestContentStoreStaticCallRejectsStore(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	input := append([]byte{casStore}, []byte("data")...)
	_, err := cs.RunWithDB(db, input, true) // readOnly = true
	if err != errCASWriteInStatic {
		t.Fatalf("expected errCASWriteInStatic, got %v", err)
	}
}

func TestContentStoreLoadNotFound(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	fakeHash := make([]byte, 32)
	input := append([]byte{casLoad}, fakeHash...)
	_, err := cs.RunWithDB(db, input, false)
	if err != errCASNotFound {
		t.Fatalf("expected errCASNotFound, got %v", err)
	}
}

func TestContentStoreIdempotent(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	testData := []byte("idempotent data")
	input := append([]byte{casStore}, testData...)

	hash1, _ := cs.RunWithDB(db, input, false)
	hash2, _ := cs.RunWithDB(db, input, false)
	if !bytes.Equal(hash1, hash2) {
		t.Fatal("idempotent store should return same hash")
	}
}

func TestContentStoreEmptyData(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	input := []byte{casStore} // empty data
	hash, err := cs.RunWithDB(db, input, false)
	if err != nil {
		t.Fatalf("store empty: %v", err)
	}
	if len(hash) != 32 {
		t.Fatalf("hash len = %d, want 32", len(hash))
	}
}

func TestContentStoreInvalidSelector(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}
	input := []byte{0xFF}
	_, err := cs.RunWithDB(db, input, false)
	if err == nil {
		t.Fatal("expected error for invalid selector")
	}
}

func TestContentStoreInvalidInput(t *testing.T) {
	db := newMockContentStoreDB()
	cs := &contentStore{}

	// Load with short hash
	_, err := cs.RunWithDB(db, []byte{casLoad, 0x01, 0x02}, false)
	if err != errCASInvalidInput {
		t.Fatalf("expected errCASInvalidInput, got %v", err)
	}

	// Empty input
	_, err = cs.RunWithDB(db, []byte{}, false)
	if err != errCASInvalidInput {
		t.Fatalf("expected errCASInvalidInput for empty, got %v", err)
	}
}

func TestContentStoreNilDB(t *testing.T) {
	cs := &contentStore{}
	_, err := cs.RunWithDB(nil, []byte{casStore, 0x01}, false)
	if err != errCASNoDB {
		t.Fatalf("expected errCASNoDB, got %v", err)
	}
}

func TestContentStoreGasCalculation(t *testing.T) {
	cs := &contentStore{}
	tests := []struct {
		name     string
		input    []byte
		expected uint64
	}{
		{"store 100 bytes", append([]byte{casStore}, make([]byte, 100)...), params.ContentStoreBaseGas + 100*params.ContentStorePerByteGas},
		{"store 0 bytes", []byte{casStore}, params.ContentStoreBaseGas},
		{"load", append([]byte{casLoad}, make([]byte, 32)...), params.ContentLoadBaseGas + uint64(params.ContentStoreMaxSize)*params.ContentLoadPerByteGas},
		{"exists", append([]byte{casExists}, make([]byte, 32)...), params.ContentExistsGas},
		{"size", append([]byte{casSize}, make([]byte, 32)...), params.ContentSizeGas},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cs.RequiredGas(tt.input)
			if got != tt.expected {
				t.Errorf("RequiredGas = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestContentStoreAddress(t *testing.T) {
	expected := "0x0000000000000000000000000000000000000300"
	if ContentStoreAddress.Hex() != expected {
		t.Fatalf("address = %s, want %s", ContentStoreAddress.Hex(), expected)
	}
}
