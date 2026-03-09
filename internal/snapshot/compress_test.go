package snapshot

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestEncodeBatch_Empty(t *testing.T) {
	compressed, err := EncodeBatch(nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := DecodeBatch(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestEncodeBatch_Roundtrip(t *testing.T) {
	original := []BatchEntry{
		{Key: []byte("address-1"), Value: []byte("account-data-1")},
		{Key: []byte("address-2"), Value: []byte("account-data-2")},
		{Key: []byte("address-3"), Value: []byte("account-data-3")},
	}

	compressed, err := EncodeBatch(original)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBatch(compressed)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("entry count: want %d, got %d", len(original), len(decoded))
	}
	for i := range original {
		if !bytes.Equal(decoded[i].Key, original[i].Key) {
			t.Errorf("entry[%d] key: want %x, got %x", i, original[i].Key, decoded[i].Key)
		}
		if !bytes.Equal(decoded[i].Value, original[i].Value) {
			t.Errorf("entry[%d] value: want %x, got %x", i, original[i].Value, decoded[i].Value)
		}
	}
}

func TestEncodeBatch_LargeData(t *testing.T) {
	// Simulate 1000 accounts, each with 20-byte key + 100-byte value.
	entries := make([]BatchEntry, 1000)
	for i := range entries {
		key := make([]byte, 20)
		val := make([]byte, 100)
		rand.Read(key)
		rand.Read(val)
		entries[i] = BatchEntry{Key: key, Value: val}
	}

	compressed, err := EncodeBatch(entries)
	if err != nil {
		t.Fatal(err)
	}

	// Check compression ratio. 1000 * (20 + 100) = 120000 raw bytes.
	rawSize := 1000 * (20 + 100 + 8) // +8 for length prefixes per entry
	t.Logf("Raw ~%d bytes, compressed %d bytes, ratio %.2fx",
		rawSize, len(compressed), float64(rawSize)/float64(len(compressed)))

	decoded, err := DecodeBatch(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1000 {
		t.Fatalf("expected 1000 entries, got %d", len(decoded))
	}
	for i := range entries {
		if !bytes.Equal(decoded[i].Key, entries[i].Key) {
			t.Errorf("entry[%d] key mismatch", i)
		}
		if !bytes.Equal(decoded[i].Value, entries[i].Value) {
			t.Errorf("entry[%d] value mismatch", i)
		}
	}
}

func TestEncodeBatch_RepeatedData_GoodCompression(t *testing.T) {
	// Repeated data should compress very well.
	entries := make([]BatchEntry, 500)
	for i := range entries {
		key := make([]byte, 20)
		key[0] = byte(i >> 8)
		key[1] = byte(i)
		// All values are the same — simulates many empty accounts.
		entries[i] = BatchEntry{Key: key, Value: bytes.Repeat([]byte{0}, 64)}
	}

	compressed, err := EncodeBatch(entries)
	if err != nil {
		t.Fatal(err)
	}

	rawSize := 500 * (20 + 64 + 8)
	ratio := float64(rawSize) / float64(len(compressed))
	t.Logf("Raw ~%d bytes, compressed %d bytes, ratio %.2fx", rawSize, len(compressed), ratio)

	if ratio < 2.0 {
		t.Errorf("expected compression ratio >= 2x for repeated data, got %.2fx", ratio)
	}

	decoded, err := DecodeBatch(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 500 {
		t.Fatalf("expected 500 entries, got %d", len(decoded))
	}
}

func TestCompressRaw_Roundtrip(t *testing.T) {
	data := bytes.Repeat([]byte("hello world snapshot data "), 100)
	compressed := CompressRaw(data)
	if len(compressed) >= len(data) {
		t.Errorf("expected compression, got %d >= %d", len(compressed), len(data))
	}

	decompressed, err := DecompressRaw(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decompressed, data) {
		t.Error("decompressed data does not match original")
	}
}

func TestDecodeBatch_CorruptData(t *testing.T) {
	_, err := DecodeBatch([]byte{0xFF, 0xFE, 0xFD})
	if err == nil {
		t.Error("expected error for corrupt data, got nil")
	}
}

func TestDecodeBatch_NilInput(t *testing.T) {
	entries, err := DecodeBatch(nil)
	if err != nil {
		t.Fatal(err)
	}
	if entries != nil {
		t.Errorf("expected nil for nil input, got %d entries", len(entries))
	}
}

func BenchmarkEncodeBatch(b *testing.B) {
	entries := make([]BatchEntry, 4096)
	for i := range entries {
		key := make([]byte, 20)
		val := make([]byte, 80)
		rand.Read(key)
		rand.Read(val)
		entries[i] = BatchEntry{Key: key, Value: val}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		EncodeBatch(entries)
	}
}

func BenchmarkDecodeBatch(b *testing.B) {
	entries := make([]BatchEntry, 4096)
	for i := range entries {
		key := make([]byte, 20)
		val := make([]byte, 80)
		rand.Read(key)
		rand.Read(val)
		entries[i] = BatchEntry{Key: key, Value: val}
	}
	compressed, _ := EncodeBatch(entries)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		DecodeBatch(compressed)
	}
}
