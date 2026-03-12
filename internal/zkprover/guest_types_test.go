package zkprover

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"
)

// TestGuestInput_EncodeDecodeRoundTrip verifies that GuestInput survives
// a full encode→decode cycle with all fields populated.
func TestGuestInput_EncodeDecodeRoundTrip(t *testing.T) {
	input := &GuestInput{
		ChainID:      42,
		BlockHeader:  []byte("block-header-proto-bytes"),
		ParentHeader: []byte("parent-header-proto-bytes"),
		Transactions: [][]byte{
			[]byte("tx0-proto-bytes"),
			[]byte("tx1-proto-bytes"),
			[]byte("tx2-proto-bytes"),
		},
		Witness: []byte("binary-witness-data"),
		ForkConfig: ForkConfig{
			IsHomestead:      true,
			IsEIP150:         true,
			IsEIP155:         true,
			IsEIP158:         true,
			IsByzantium:      true,
			IsConstantinople: true,
			IsPetersburg:     true,
			IsIstanbul:       true,
			IsBerlin:         true,
			IsLondon:         true,
			IsShanghai:       true,
			IsCancun:         false,
			IsBeijing:        false,
		},
	}

	data, err := EncodeGuestInput(input)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeGuestInput(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ChainID != input.ChainID {
		t.Fatalf("ChainID: got %d, want %d", decoded.ChainID, input.ChainID)
	}
	if !bytes.Equal(decoded.BlockHeader, input.BlockHeader) {
		t.Fatal("BlockHeader mismatch")
	}
	if !bytes.Equal(decoded.ParentHeader, input.ParentHeader) {
		t.Fatal("ParentHeader mismatch")
	}
	if len(decoded.Transactions) != len(input.Transactions) {
		t.Fatalf("Transactions count: got %d, want %d", len(decoded.Transactions), len(input.Transactions))
	}
	for i := range input.Transactions {
		if !bytes.Equal(decoded.Transactions[i], input.Transactions[i]) {
			t.Fatalf("Transaction[%d] mismatch", i)
		}
	}
	if !bytes.Equal(decoded.Witness, input.Witness) {
		t.Fatal("Witness mismatch")
	}
	if decoded.ForkConfig != input.ForkConfig {
		t.Fatalf("ForkConfig mismatch: got %+v, want %+v", decoded.ForkConfig, input.ForkConfig)
	}
}

// TestGuestInput_EmptyTransactions verifies round-trip with zero transactions.
func TestGuestInput_EmptyTransactions(t *testing.T) {
	input := &GuestInput{
		ChainID:      1,
		BlockHeader:  []byte("h"),
		ParentHeader: []byte("p"),
		Transactions: nil,
		Witness:      []byte("w"),
	}

	data, err := EncodeGuestInput(input)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeGuestInput(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded.Transactions) != 0 {
		t.Fatalf("expected 0 transactions, got %d", len(decoded.Transactions))
	}
}

// TestGuestInput_EmptyFields verifies round-trip with empty byte slices.
func TestGuestInput_EmptyFields(t *testing.T) {
	input := &GuestInput{
		ChainID:      0,
		BlockHeader:  []byte{},
		ParentHeader: []byte{},
		Transactions: [][]byte{{}},
		Witness:      []byte{},
	}

	data, err := EncodeGuestInput(input)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeGuestInput(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ChainID != 0 {
		t.Fatalf("ChainID: got %d, want 0", decoded.ChainID)
	}
	if len(decoded.Transactions) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(decoded.Transactions))
	}
}

// TestGuestInput_AllForkFlags verifies each fork flag is independently encoded.
func TestGuestInput_AllForkFlags(t *testing.T) {
	flags := []struct {
		name   string
		setter func(*ForkConfig)
		getter func(*ForkConfig) bool
	}{
		{"IsHomestead", func(f *ForkConfig) { f.IsHomestead = true }, func(f *ForkConfig) bool { return f.IsHomestead }},
		{"IsEIP150", func(f *ForkConfig) { f.IsEIP150 = true }, func(f *ForkConfig) bool { return f.IsEIP150 }},
		{"IsEIP155", func(f *ForkConfig) { f.IsEIP155 = true }, func(f *ForkConfig) bool { return f.IsEIP155 }},
		{"IsEIP158", func(f *ForkConfig) { f.IsEIP158 = true }, func(f *ForkConfig) bool { return f.IsEIP158 }},
		{"IsByzantium", func(f *ForkConfig) { f.IsByzantium = true }, func(f *ForkConfig) bool { return f.IsByzantium }},
		{"IsConstantinople", func(f *ForkConfig) { f.IsConstantinople = true }, func(f *ForkConfig) bool { return f.IsConstantinople }},
		{"IsPetersburg", func(f *ForkConfig) { f.IsPetersburg = true }, func(f *ForkConfig) bool { return f.IsPetersburg }},
		{"IsIstanbul", func(f *ForkConfig) { f.IsIstanbul = true }, func(f *ForkConfig) bool { return f.IsIstanbul }},
		{"IsBerlin", func(f *ForkConfig) { f.IsBerlin = true }, func(f *ForkConfig) bool { return f.IsBerlin }},
		{"IsLondon", func(f *ForkConfig) { f.IsLondon = true }, func(f *ForkConfig) bool { return f.IsLondon }},
		{"IsShanghai", func(f *ForkConfig) { f.IsShanghai = true }, func(f *ForkConfig) bool { return f.IsShanghai }},
		{"IsCancun", func(f *ForkConfig) { f.IsCancun = true }, func(f *ForkConfig) bool { return f.IsCancun }},
		{"IsBeijing", func(f *ForkConfig) { f.IsBeijing = true }, func(f *ForkConfig) bool { return f.IsBeijing }},
		{"IsPrague", func(f *ForkConfig) { f.IsPrague = true }, func(f *ForkConfig) bool { return f.IsPrague }},
		{"IsPectra", func(f *ForkConfig) { f.IsPectra = true }, func(f *ForkConfig) bool { return f.IsPectra }},
		{"IsOsaka", func(f *ForkConfig) { f.IsOsaka = true }, func(f *ForkConfig) bool { return f.IsOsaka }},
		{"IsFusaka", func(f *ForkConfig) { f.IsFusaka = true }, func(f *ForkConfig) bool { return f.IsFusaka }},
		{"IsNano", func(f *ForkConfig) { f.IsNano = true }, func(f *ForkConfig) bool { return f.IsNano }},
		{"IsMoran", func(f *ForkConfig) { f.IsMoran = true }, func(f *ForkConfig) bool { return f.IsMoran }},
		{"IsEip1559FeeCollector", func(f *ForkConfig) { f.IsEip1559FeeCollector = true }, func(f *ForkConfig) bool { return f.IsEip1559FeeCollector }},
		{"IsParlia", func(f *ForkConfig) { f.IsParlia = true }, func(f *ForkConfig) bool { return f.IsParlia }},
		{"IsAura", func(f *ForkConfig) { f.IsAura = true }, func(f *ForkConfig) bool { return f.IsAura }},
	}

	for _, flag := range flags {
		t.Run(flag.name, func(t *testing.T) {
			fc := ForkConfig{}
			flag.setter(&fc)

			input := &GuestInput{
				ChainID:      1,
				BlockHeader:  []byte("h"),
				ParentHeader: []byte("p"),
				Witness:      []byte("w"),
				ForkConfig:   fc,
			}

			data, err := EncodeGuestInput(input)
			if err != nil {
				t.Fatal(err)
			}

			decoded, err := DecodeGuestInput(data)
			if err != nil {
				t.Fatal(err)
			}

			if !flag.getter(&decoded.ForkConfig) {
				t.Fatalf("expected %s to be true after round-trip", flag.name)
			}

			// Verify exactly 1 flag is set by comparing with a ForkConfig with only this flag set.
			expected := ForkConfig{}
			flag.setter(&expected)
			if decoded.ForkConfig != expected {
				t.Fatalf("expected only %s to be set, got %+v", flag.name, decoded.ForkConfig)
			}
		})
	}
}

// TestGuestInput_DecodeTooShort verifies proper error for truncated input.
func TestGuestInput_DecodeTooShort(t *testing.T) {
	_, err := DecodeGuestInput([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

// TestGuestInput_DecodeNil verifies proper error for nil input.
func TestGuestInput_DecodeNil(t *testing.T) {
	_, err := DecodeGuestInput(nil)
	if err == nil {
		t.Fatal("expected error for nil data")
	}
}

// TestGuestInput_LargePayload verifies round-trip with realistic payload sizes.
func TestGuestInput_LargePayload(t *testing.T) {
	rng := rand.New(rand.NewSource(12345))

	header := make([]byte, 512)
	rng.Read(header)
	parent := make([]byte, 512)
	rng.Read(parent)
	witness := make([]byte, 4096)
	rng.Read(witness)

	txs := make([][]byte, 50)
	for i := range txs {
		txs[i] = make([]byte, 200+rng.Intn(800))
		rng.Read(txs[i])
	}

	input := &GuestInput{
		ChainID:      1337,
		BlockHeader:  header,
		ParentHeader: parent,
		Transactions: txs,
		Witness:      witness,
		ForkConfig: ForkConfig{
			IsHomestead: true, IsEIP150: true, IsEIP155: true, IsEIP158: true,
			IsByzantium: true, IsConstantinople: true, IsPetersburg: true,
			IsIstanbul: true, IsBerlin: true, IsLondon: true,
		},
	}

	data, err := EncodeGuestInput(input)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeGuestInput(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ChainID != 1337 {
		t.Fatalf("ChainID: got %d, want 1337", decoded.ChainID)
	}
	if len(decoded.Transactions) != 50 {
		t.Fatalf("Transactions: got %d, want 50", len(decoded.Transactions))
	}
	for i, tx := range decoded.Transactions {
		if !bytes.Equal(tx, txs[i]) {
			t.Fatalf("Transaction[%d] data mismatch", i)
		}
	}
}

// TestGuestOutput_EncodeDecodeRoundTrip verifies GuestOutput round-trip.
func TestGuestOutput_EncodeDecodeRoundTrip(t *testing.T) {
	output := &GuestOutput{
		PostStateRoot: [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
			17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		ReceiptsRoot: [32]byte{32, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17,
			16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		GasUsed: 21000,
		Success: true,
		Error:   "",
	}

	data, err := EncodeGuestOutput(output)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeGuestOutput(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.PostStateRoot != output.PostStateRoot {
		t.Fatal("PostStateRoot mismatch")
	}
	if decoded.ReceiptsRoot != output.ReceiptsRoot {
		t.Fatal("ReceiptsRoot mismatch")
	}
	if decoded.GasUsed != output.GasUsed {
		t.Fatalf("GasUsed: got %d, want %d", decoded.GasUsed, output.GasUsed)
	}
	if decoded.Success != output.Success {
		t.Fatalf("Success: got %v, want %v", decoded.Success, output.Success)
	}
	if decoded.Error != output.Error {
		t.Fatalf("Error: got %q, want %q", decoded.Error, output.Error)
	}
}

// TestGuestOutput_WithError verifies GuestOutput with error string.
func TestGuestOutput_WithError(t *testing.T) {
	output := &GuestOutput{
		Success: false,
		Error:   "tx 3 execution failed: out of gas",
	}

	data, err := EncodeGuestOutput(output)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeGuestOutput(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Success != false {
		t.Fatal("expected Success=false")
	}
	if decoded.Error != "tx 3 execution failed: out of gas" {
		t.Fatalf("Error mismatch: got %q", decoded.Error)
	}
}

// TestGuestOutput_DecodeTooShort verifies error for truncated output data.
func TestGuestOutput_DecodeTooShort(t *testing.T) {
	_, err := DecodeGuestOutput(make([]byte, 10))
	if err == nil {
		t.Fatal("expected error for truncated output data")
	}
}

// TestGuestInput_MaxFieldSize verifies maxGuestFieldSize enforcement.
func TestGuestInput_MaxFieldSize(t *testing.T) {
	// Craft data with a BlockHeader field length exceeding maxGuestFieldSize.
	buf := make([]byte, 8+4) // chainID (8) + blockHeader length (4)
	binary.LittleEndian.PutUint64(buf[:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], maxGuestFieldSize+1)

	_, err := DecodeGuestInput(buf)
	if err == nil {
		t.Fatal("expected error for field exceeding maxGuestFieldSize")
	}
}

// TestGuestInput_FieldSizeAtLimit verifies maxGuestFieldSize boundary.
func TestGuestInput_FieldSizeAtLimit(t *testing.T) {
	// Field length exactly at limit — should fail because buffer is too short.
	buf := make([]byte, 8+4)
	binary.LittleEndian.PutUint64(buf[:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], maxGuestFieldSize) // at limit, but no data follows

	_, err := DecodeGuestInput(buf)
	if err == nil {
		t.Fatal("expected error for field at limit with truncated data")
	}
}

// TestGuestInput_AllForksFull verifies round-trip with all 22 fork flags set to true.
func TestGuestInput_AllForksFull(t *testing.T) {
	input := &GuestInput{
		ChainID:      1,
		BlockHeader:  []byte("h"),
		ParentHeader: []byte("p"),
		Witness:      []byte("w"),
		ForkConfig: ForkConfig{
			IsHomestead: true, IsEIP150: true, IsEIP155: true, IsEIP158: true,
			IsByzantium: true, IsConstantinople: true, IsPetersburg: true,
			IsIstanbul: true, IsBerlin: true, IsLondon: true,
			IsShanghai: true, IsCancun: true, IsBeijing: true,
			IsPrague: true, IsPectra: true, IsOsaka: true, IsFusaka: true,
			IsNano: true, IsMoran: true, IsEip1559FeeCollector: true,
			IsParlia: true, IsAura: true,
		},
	}

	data, err := EncodeGuestInput(input)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeGuestInput(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ForkConfig != input.ForkConfig {
		t.Fatalf("all-true ForkConfig mismatch: got %+v", decoded.ForkConfig)
	}
}

// TestGuestInput_BitmaskEncoding verifies the uint32 bitmask for fork flags.
func TestGuestInput_BitmaskEncoding(t *testing.T) {
	input := &GuestInput{
		ChainID:      1,
		BlockHeader:  []byte("h"),
		ParentHeader: []byte("p"),
		Witness:      []byte("w"),
		ForkConfig:   ForkConfig{IsAura: true}, // bit 21
	}

	data, err := EncodeGuestInput(input)
	if err != nil {
		t.Fatal(err)
	}

	// Fork flags are the last 4 bytes.
	flagBytes := data[len(data)-4:]
	flags := binary.LittleEndian.Uint32(flagBytes)
	expected := uint32(1 << 21)
	if flags != expected {
		t.Fatalf("bitmask: got %032b, want %032b", flags, expected)
	}
}

// TestGuestOutput_ZeroValues verifies round-trip with all-zero output.
func TestGuestOutput_ZeroValues(t *testing.T) {
	output := &GuestOutput{}

	data, err := EncodeGuestOutput(output)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeGuestOutput(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.PostStateRoot != ([32]byte{}) {
		t.Fatal("expected zero PostStateRoot")
	}
	if decoded.GasUsed != 0 {
		t.Fatal("expected zero GasUsed")
	}
	if decoded.Success {
		t.Fatal("expected Success=false for zero value")
	}
}
