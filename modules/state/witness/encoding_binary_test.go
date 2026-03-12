package witness

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// TestBinaryEncoding_EmptyWitness verifies round-trip of an empty witness.
func TestBinaryEncoding_EmptyWitness(t *testing.T) {
	w := &BlockWitness{
		ParentRoot: types.HexToHash("0xabcdef0000000000000000000000000000000000000000000000000000000000"),
	}

	data, err := EncodeBinaryWitness(w)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBinaryWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ParentRoot != w.ParentRoot {
		t.Fatal("ParentRoot mismatch")
	}
	if len(decoded.AccountProofs) != 0 {
		t.Fatalf("expected 0 account proofs, got %d", len(decoded.AccountProofs))
	}
	if len(decoded.StorageProofs) != 0 {
		t.Fatalf("expected 0 storage proofs, got %d", len(decoded.StorageProofs))
	}
	if len(decoded.Codes) != 0 {
		t.Fatalf("expected 0 codes, got %d", len(decoded.Codes))
	}
}

// TestBinaryEncoding_WithAccountProof verifies round-trip of a witness
// with one account proof.
func TestBinaryEncoding_WithAccountProof(t *testing.T) {
	keyHash := jmt.Hash{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	w := &BlockWitness{
		ParentRoot: types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		AccountProofs: []KeyProof{
			{
				KeyHash:     keyHash,
				OriginalKey: []byte{0xaa, 0xbb, 0xcc},
				Value:       []byte{0x01, 0x02, 0x03, 0x04},
				Proof: jmt.Proof{
					Key:   keyHash,
					Value: []byte{0x01, 0x02, 0x03, 0x04},
					Path: []jmt.ProofEntry{
						{NodeData: []byte{10, 20, 30, 40, 50}, Nibble: 5},
						{NodeData: []byte{60, 70}, Nibble: -1},
					},
				},
			},
		},
	}

	data, err := EncodeBinaryWitness(w)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBinaryWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded.AccountProofs) != 1 {
		t.Fatalf("expected 1 account proof, got %d", len(decoded.AccountProofs))
	}

	ap := decoded.AccountProofs[0]
	if ap.KeyHash != keyHash {
		t.Fatal("KeyHash mismatch")
	}
	if !bytes.Equal(ap.OriginalKey, []byte{0xaa, 0xbb, 0xcc}) {
		t.Fatal("OriginalKey mismatch")
	}
	if !bytes.Equal(ap.Value, []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatal("Value mismatch")
	}
	if len(ap.Proof.Path) != 2 {
		t.Fatalf("expected 2 path entries, got %d", len(ap.Proof.Path))
	}
	if ap.Proof.Path[0].Nibble != 5 {
		t.Fatalf("Path[0].Nibble: got %d, want 5", ap.Proof.Path[0].Nibble)
	}
	if ap.Proof.Path[1].Nibble != -1 {
		t.Fatalf("Path[1].Nibble: got %d, want -1", ap.Proof.Path[1].Nibble)
	}
}

// TestBinaryEncoding_NilValue verifies round-trip of exclusion proofs (nil value).
func TestBinaryEncoding_NilValue(t *testing.T) {
	w := &BlockWitness{
		AccountProofs: []KeyProof{
			{
				KeyHash:     jmt.Hash{0xFF},
				OriginalKey: []byte{0x01},
				Value:       nil, // exclusion proof
				Proof: jmt.Proof{
					Key:   jmt.Hash{0xFF},
					Value: nil,
				},
			},
		},
	}

	data, err := EncodeBinaryWitness(w)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBinaryWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.AccountProofs[0].Value != nil {
		t.Fatal("expected nil Value for exclusion proof")
	}
	if decoded.AccountProofs[0].Proof.Value != nil {
		t.Fatal("expected nil Proof.Value for exclusion proof")
	}
}

// TestBinaryEncoding_WithCodes verifies code round-trip.
func TestBinaryEncoding_WithCodes(t *testing.T) {
	codeHash := types.HexToHash("0xaabbccdd00000000000000000000000000000000000000000000000000000000")
	codeBytes := []byte{0x60, 0x00, 0x60, 0x00, 0xFD} // PUSH0 PUSH0 REVERT

	w := &BlockWitness{
		Codes: []*state.HashCode{
			{Hash: codeHash, Code: codeBytes},
		},
	}

	data, err := EncodeBinaryWitness(w)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBinaryWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded.Codes) != 1 {
		t.Fatalf("expected 1 code, got %d", len(decoded.Codes))
	}
	if decoded.Codes[0].Hash != codeHash {
		t.Fatal("code hash mismatch")
	}
	if !bytes.Equal(decoded.Codes[0].Code, codeBytes) {
		t.Fatal("code bytes mismatch")
	}
}

// TestBinaryEncoding_FullWitness verifies round-trip with all field types
// populated (accounts, storage, codes).
func TestBinaryEncoding_FullWitness(t *testing.T) {
	w := &BlockWitness{
		ParentRoot: types.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999"),
		AccountProofs: []KeyProof{
			{
				KeyHash:     jmt.Hash{1},
				OriginalKey: make([]byte, 20), // address length
				Value:       []byte{0xAA},
				Proof: jmt.Proof{
					Key:   jmt.Hash{1},
					Value: []byte{0xAA},
					Path:  []jmt.ProofEntry{{NodeData: []byte{0x01}, Nibble: 0}},
				},
			},
			{
				KeyHash:     jmt.Hash{2},
				OriginalKey: make([]byte, 20),
				Value:       []byte{0xBB, 0xCC},
				Proof: jmt.Proof{
					Key:   jmt.Hash{2},
					Value: []byte{0xBB, 0xCC},
				},
			},
		},
		StorageProofs: []KeyProof{
			{
				KeyHash:     jmt.Hash{3},
				OriginalKey: make([]byte, 52), // address(20) + slot(32)
				Value:       []byte{0xDD},
				Proof: jmt.Proof{
					Key:   jmt.Hash{3},
					Value: []byte{0xDD},
				},
			},
		},
		Codes: []*state.HashCode{
			{Hash: types.Hash{0x10}, Code: []byte{0x60, 0x00}},
			{Hash: types.Hash{0x20}, Code: []byte{0x60, 0x01, 0x60, 0x00, 0x55}},
		},
	}

	data, err := EncodeBinaryWitness(w)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBinaryWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ParentRoot != w.ParentRoot {
		t.Fatal("ParentRoot mismatch")
	}
	if len(decoded.AccountProofs) != 2 {
		t.Fatalf("expected 2 account proofs, got %d", len(decoded.AccountProofs))
	}
	if len(decoded.StorageProofs) != 1 {
		t.Fatalf("expected 1 storage proof, got %d", len(decoded.StorageProofs))
	}
	if len(decoded.Codes) != 2 {
		t.Fatalf("expected 2 codes, got %d", len(decoded.Codes))
	}
}

// TestBinaryEncoding_DecodeTooShort verifies error for truncated data.
func TestBinaryEncoding_DecodeTooShort(t *testing.T) {
	_, err := DecodeBinaryWitness([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
}

// TestBinaryEncoding_DecodeTooLarge verifies max size enforcement.
func TestBinaryEncoding_DecodeTooLarge(t *testing.T) {
	data := make([]byte, MaxWitnessSize+1)
	_, err := DecodeBinaryWitness(data)
	if err == nil {
		t.Fatal("expected error for oversized data")
	}
}

// TestBinaryEncoding_RealJMTProof verifies binary encoding with proofs
// generated from a real JMT tree.
func TestBinaryEncoding_RealJMTProof(t *testing.T) {
	store := jmt.NewMemStore()
	tree := jmt.New(store)

	// Insert some data.
	key1 := jmt.HashKey([]byte("account-1"))
	key2 := jmt.HashKey([]byte("account-2"))

	if err := tree.Put(key1, []byte("value-1")); err != nil {
		t.Fatal(err)
	}
	if err := tree.Put(key2, []byte("value-2")); err != nil {
		t.Fatal(err)
	}
	if err := tree.Flush(); err != nil {
		t.Fatal(err)
	}

	root := tree.Root()

	proof1, err := tree.GetProof(key1)
	if err != nil {
		t.Fatal(err)
	}
	proof2, err := tree.GetProof(key2)
	if err != nil {
		t.Fatal(err)
	}

	var parentRoot types.Hash
	copy(parentRoot[:], root[:])

	w := &BlockWitness{
		ParentRoot: parentRoot,
		AccountProofs: []KeyProof{
			{
				KeyHash:     proof1.Key,
				OriginalKey: []byte("account-1"),
				Value:       proof1.Value,
				Proof:       *proof1,
			},
			{
				KeyHash:     proof2.Key,
				OriginalKey: []byte("account-2"),
				Value:       proof2.Value,
				Proof:       *proof2,
			},
		},
	}

	// Binary round-trip.
	data, err := EncodeBinaryWitness(w)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBinaryWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	// Verify decoded witness still passes Merkle verification.
	if err := VerifyWitness(parentRoot, decoded); err != nil {
		t.Fatalf("decoded binary witness should still verify: %v", err)
	}
}

// TestBinaryVsJSONConsistency verifies that binary and JSON encodings
// produce equivalent witnesses after decode.
func TestBinaryVsJSONConsistency(t *testing.T) {
	w := &BlockWitness{
		ParentRoot: types.HexToHash("0x7777777777777777777777777777777777777777777777777777777777777777"),
		AccountProofs: []KeyProof{
			{
				KeyHash:     jmt.Hash{0xAA, 0xBB},
				OriginalKey: []byte{0x01, 0x02, 0x03},
				Value:       []byte{0x04, 0x05},
				Proof: jmt.Proof{
					Key:   jmt.Hash{0xAA, 0xBB},
					Value: []byte{0x04, 0x05},
					Path:  []jmt.ProofEntry{{NodeData: []byte{0x10, 0x20}, Nibble: 3}},
				},
			},
		},
	}

	// JSON round-trip.
	jsonData, err := w.Encode() // uses JSON
	if err != nil {
		t.Fatal(err)
	}
	jsonDecoded, err := DecodeBlockWitness(jsonData)
	if err != nil {
		t.Fatal(err)
	}

	// Binary round-trip.
	binData, err := EncodeBinaryWitness(w)
	if err != nil {
		t.Fatal(err)
	}
	binDecoded, err := DecodeBinaryWitness(binData)
	if err != nil {
		t.Fatal(err)
	}

	// Compare the two decoded results.
	if jsonDecoded.ParentRoot != binDecoded.ParentRoot {
		t.Fatal("ParentRoot differs between JSON and binary")
	}
	if len(jsonDecoded.AccountProofs) != len(binDecoded.AccountProofs) {
		t.Fatal("AccountProofs count differs")
	}
	if jsonDecoded.AccountProofs[0].KeyHash != binDecoded.AccountProofs[0].KeyHash {
		t.Fatal("KeyHash differs between JSON and binary")
	}
	if !bytes.Equal(jsonDecoded.AccountProofs[0].Value, binDecoded.AccountProofs[0].Value) {
		t.Fatal("Value differs between JSON and binary")
	}

	// Binary should be more compact.
	if len(binData) >= len(jsonData) {
		t.Logf("binary=%d, json=%d (binary not smaller — may be expected for tiny witnesses)", len(binData), len(jsonData))
	}
}

// TestBinaryEncoding_ReadBytesMaxWitnessSize verifies MaxWitnessSize enforcement
// in readBytes when a field length exceeds the maximum.
func TestBinaryEncoding_ReadBytesMaxWitnessSize(t *testing.T) {
	// Craft a minimal witness with an account proof whose OriginalKey length exceeds MaxWitnessSize.
	buf := make([]byte, 32+4+32+4) // parentRoot + numAcctProofs(1) + keyHash + originalKeyLen
	// parentRoot: zeros
	binary.LittleEndian.PutUint32(buf[32:36], 1) // 1 account proof
	// keyHash: zeros (next 32 bytes)
	// originalKeyLen: set to MaxWitnessSize + 1
	binary.LittleEndian.PutUint32(buf[68:72], uint32(MaxWitnessSize)+1)

	_, err := DecodeBinaryWitness(buf)
	if err == nil {
		t.Fatal("expected error for field size exceeding MaxWitnessSize")
	}
}

// TestBinaryEncoding_LargeNumProofs verifies decoding handles large proof counts
// without allocating unbounded memory.
func TestBinaryEncoding_LargeNumProofs(t *testing.T) {
	// Set numAccountProofs to a huge number, but data is too short.
	buf := make([]byte, 32+4+4+4) // parentRoot + 3 counts
	binary.LittleEndian.PutUint32(buf[32:36], 1000000) // 1M proofs

	_, err := DecodeBinaryWitness(buf)
	if err == nil {
		t.Fatal("expected error for data too short for claimed proof count")
	}
}

// TestBinaryEncoding_MultipleStorageProofs verifies round-trip with multiple storage proofs.
func TestBinaryEncoding_MultipleStorageProofs(t *testing.T) {
	w := &BlockWitness{
		StorageProofs: []KeyProof{
			{
				KeyHash:     jmt.Hash{1},
				OriginalKey: make([]byte, 52),
				Value:       []byte{0xAA},
				Proof: jmt.Proof{
					Key:   jmt.Hash{1},
					Value: []byte{0xAA},
				},
			},
			{
				KeyHash:     jmt.Hash{2},
				OriginalKey: make([]byte, 52),
				Value:       nil, // exclusion proof
				Proof: jmt.Proof{
					Key:   jmt.Hash{2},
					Value: nil,
				},
			},
			{
				KeyHash:     jmt.Hash{3},
				OriginalKey: make([]byte, 52),
				Value:       []byte{0xBB, 0xCC},
				Proof: jmt.Proof{
					Key:   jmt.Hash{3},
					Value: []byte{0xBB, 0xCC},
					Path:  []jmt.ProofEntry{{NodeData: []byte{1, 2}, Nibble: 0}},
				},
			},
		},
	}

	data, err := EncodeBinaryWitness(w)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBinaryWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded.StorageProofs) != 3 {
		t.Fatalf("expected 3 storage proofs, got %d", len(decoded.StorageProofs))
	}
	if decoded.StorageProofs[1].Value != nil {
		t.Fatal("expected nil value for exclusion proof at index 1")
	}
	if !bytes.Equal(decoded.StorageProofs[2].Value, []byte{0xBB, 0xCC}) {
		t.Fatal("value mismatch at index 2")
	}
}

// TestVerifyBlockStatelessWithExec_Valid verifies a valid witness returns a reader.
func TestVerifyBlockStatelessWithExec_Valid(t *testing.T) {
	store := jmt.NewMemStore()
	tree := jmt.New(store)

	// Use a 20-byte address as OriginalKey (required by WitnessStateReader).
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	// Encode a valid StateAccount (107 bytes, required by WitnessStateReader).
	acct := &account.StateAccount{
		Initialised: true,
		Nonce:       1,
		Balance:     *uint256.NewInt(1000),
		Incarnation: 1,
	}
	acctValue := commitment.EncodeAccountValue(acct)

	key1 := jmt.HashKey(addr[:])
	if err := tree.Put(key1, acctValue); err != nil {
		t.Fatal(err)
	}
	if err := tree.Flush(); err != nil {
		t.Fatal(err)
	}

	root := tree.Root()
	proof1, err := tree.GetProof(key1)
	if err != nil {
		t.Fatal(err)
	}

	var parentRoot types.Hash
	copy(parentRoot[:], root[:])

	w := &BlockWitness{
		ParentRoot: parentRoot,
		AccountProofs: []KeyProof{
			{
				KeyHash:     proof1.Key,
				OriginalKey: addr[:],
				Value:       proof1.Value,
				Proof:       *proof1,
			},
		},
	}

	reader, err := VerifyBlockStatelessWithExec(parentRoot, w)
	if err != nil {
		t.Fatalf("valid witness should return reader: %v", err)
	}
	if reader == nil {
		t.Fatal("expected non-nil WitnessStateReader")
	}
}

// TestVerifyBlockStatelessWithExec_RootMismatch verifies parent root mismatch.
func TestVerifyBlockStatelessWithExec_RootMismatch(t *testing.T) {
	w := &BlockWitness{
		ParentRoot: types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
	}
	wrongRoot := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	_, err := VerifyBlockStatelessWithExec(wrongRoot, w)
	if err == nil {
		t.Fatal("expected error for parent root mismatch")
	}
}

// TestVerifyBlockStateless_WithTxsParam verifies txs parameter is accepted.
func TestVerifyBlockStateless_WithTxsParam(t *testing.T) {
	store := jmt.NewMemStore()
	tree := jmt.New(store)

	// Use a 20-byte address as OriginalKey (required by WitnessStateReader).
	addr := types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	// Encode a valid StateAccount (107 bytes).
	acct := &account.StateAccount{
		Initialised: true,
		Nonce:       5,
		Balance:     *uint256.NewInt(500),
		Incarnation: 1,
	}
	acctValue := commitment.EncodeAccountValue(acct)

	key1 := jmt.HashKey(addr[:])
	if err := tree.Put(key1, acctValue); err != nil {
		t.Fatal(err)
	}
	if err := tree.Flush(); err != nil {
		t.Fatal(err)
	}

	root := tree.Root()
	proof1, err := tree.GetProof(key1)
	if err != nil {
		t.Fatal(err)
	}

	var parentRoot types.Hash
	copy(parentRoot[:], root[:])

	w := &BlockWitness{
		ParentRoot: parentRoot,
		AccountProofs: []KeyProof{
			{
				KeyHash:     proof1.Key,
				OriginalKey: addr[:],
				Value:       proof1.Value,
				Proof:       *proof1,
			},
		},
	}

	// txs parameter is reserved but should be accepted.
	_, err = VerifyBlockStateless(parentRoot, w, [][]byte{[]byte("tx1"), []byte("tx2")})
	if err != nil {
		t.Fatalf("should accept txs parameter: %v", err)
	}
}
