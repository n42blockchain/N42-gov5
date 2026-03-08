package sync_pb

import (
	"bytes"
	"testing"
)

func TestGetAccountRangeRequest_SSZRoundtrip(t *testing.T) {
	original := &GetAccountRangeRequest{
		Root:     bytes.Repeat([]byte{0xAB}, 32),
		Start:    bytes.Repeat([]byte{0x01}, 20),
		End:      bytes.Repeat([]byte{0xFF}, 20),
		MaxBytes: 512 * 1024,
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(GetAccountRangeRequest)
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded.Root, original.Root) {
		t.Errorf("Root mismatch")
	}
	if !bytes.Equal(decoded.Start, original.Start) {
		t.Errorf("Start mismatch")
	}
	if !bytes.Equal(decoded.End, original.End) {
		t.Errorf("End mismatch")
	}
	if decoded.MaxBytes != original.MaxBytes {
		t.Errorf("MaxBytes: got %d, want %d", decoded.MaxBytes, original.MaxBytes)
	}
}

func TestGetAccountRangeRequest_EmptyFields(t *testing.T) {
	original := &GetAccountRangeRequest{
		Root:     bytes.Repeat([]byte{0x00}, 32),
		Start:    nil,
		End:      nil,
		MaxBytes: 0,
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(GetAccountRangeRequest)
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}

	if len(decoded.Start) != 0 {
		t.Errorf("Start should be empty, got %d bytes", len(decoded.Start))
	}
	if len(decoded.End) != 0 {
		t.Errorf("End should be empty, got %d bytes", len(decoded.End))
	}
}

func TestAccountRangeResponse_SSZRoundtrip(t *testing.T) {
	original := &AccountRangeResponse{
		Accounts: []*AccountEntry{
			{
				Address: bytes.Repeat([]byte{0x01}, 20),
				Account: []byte("account-data-1"),
			},
			{
				Address: bytes.Repeat([]byte{0x02}, 20),
				Account: []byte("account-data-2"),
			},
		},
		NextStart: bytes.Repeat([]byte{0x03}, 20),
		Completed: false,
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(AccountRangeResponse)
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}

	if len(decoded.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(decoded.Accounts))
	}
	if !bytes.Equal(decoded.Accounts[0].Address, original.Accounts[0].Address) {
		t.Errorf("Account[0] address mismatch")
	}
	if !bytes.Equal(decoded.Accounts[1].Account, original.Accounts[1].Account) {
		t.Errorf("Account[1] data mismatch")
	}
	if !bytes.Equal(decoded.NextStart, original.NextStart) {
		t.Errorf("NextStart mismatch")
	}
	if decoded.Completed != false {
		t.Errorf("Completed should be false")
	}
}

func TestAccountRangeResponse_Completed(t *testing.T) {
	original := &AccountRangeResponse{
		Accounts:  nil,
		NextStart: nil,
		Completed: true,
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(AccountRangeResponse)
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}

	if len(decoded.Accounts) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(decoded.Accounts))
	}
	if decoded.Completed != true {
		t.Errorf("Completed should be true")
	}
}

func TestGetStorageRangeRequest_SSZRoundtrip(t *testing.T) {
	original := &GetStorageRangeRequest{
		Root:     bytes.Repeat([]byte{0xCC}, 32),
		Account:  bytes.Repeat([]byte{0xDD}, 20),
		Start:    bytes.Repeat([]byte{0x01}, 54),
		End:      bytes.Repeat([]byte{0xFF}, 54),
		MaxBytes: 1024 * 1024,
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(GetStorageRangeRequest)
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded.Root, original.Root) {
		t.Errorf("Root mismatch")
	}
	if !bytes.Equal(decoded.Account, original.Account) {
		t.Errorf("Account mismatch")
	}
	if decoded.MaxBytes != original.MaxBytes {
		t.Errorf("MaxBytes mismatch")
	}
}

func TestStorageRangeResponse_SSZRoundtrip(t *testing.T) {
	original := &StorageRangeResponse{
		Slots: []*StorageEntry{
			{
				Key:   []byte("storage-key-1"),
				Value: []byte("storage-value-1"),
			},
		},
		NextStart: nil,
		Completed: true,
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(StorageRangeResponse)
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}

	if len(decoded.Slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(decoded.Slots))
	}
	if !bytes.Equal(decoded.Slots[0].Key, original.Slots[0].Key) {
		t.Errorf("slot key mismatch")
	}
	if !bytes.Equal(decoded.Slots[0].Value, original.Slots[0].Value) {
		t.Errorf("slot value mismatch")
	}
	if !decoded.Completed {
		t.Errorf("Completed should be true")
	}
}

func TestGetCodeRequest_SSZRoundtrip(t *testing.T) {
	original := &GetCodeRequest{
		Hashes: [][]byte{
			bytes.Repeat([]byte{0xAA}, 32),
			bytes.Repeat([]byte{0xBB}, 32),
			bytes.Repeat([]byte{0xCC}, 32),
		},
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(GetCodeRequest)
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}

	if len(decoded.Hashes) != 3 {
		t.Fatalf("expected 3 hashes, got %d", len(decoded.Hashes))
	}
	for i := range original.Hashes {
		if !bytes.Equal(decoded.Hashes[i], original.Hashes[i]) {
			t.Errorf("hash[%d] mismatch", i)
		}
	}
}

func TestCodeResponse_SSZRoundtrip(t *testing.T) {
	original := &CodeResponse{
		Codes: []*CodeEntry{
			{
				Hash: bytes.Repeat([]byte{0x11}, 32),
				Code: []byte("contract-bytecode-data-here"),
			},
			{
				Hash: bytes.Repeat([]byte{0x22}, 32),
				Code: bytes.Repeat([]byte{0xFF}, 1024),
			},
		},
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(CodeResponse)
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}

	if len(decoded.Codes) != 2 {
		t.Fatalf("expected 2 codes, got %d", len(decoded.Codes))
	}
	if !bytes.Equal(decoded.Codes[0].Hash, original.Codes[0].Hash) {
		t.Errorf("code[0] hash mismatch")
	}
	if !bytes.Equal(decoded.Codes[0].Code, original.Codes[0].Code) {
		t.Errorf("code[0] data mismatch")
	}
	if !bytes.Equal(decoded.Codes[1].Code, original.Codes[1].Code) {
		t.Errorf("code[1] data mismatch")
	}
}

func TestSizeSSZ_Consistency(t *testing.T) {
	msgs := []interface {
		MarshalSSZ() ([]byte, error)
		SizeSSZ() int
	}{
		&GetAccountRangeRequest{
			Root: bytes.Repeat([]byte{0x01}, 32), Start: []byte{1, 2, 3}, End: nil, MaxBytes: 100,
		},
		&AccountRangeResponse{
			Accounts: []*AccountEntry{{Address: []byte{1}, Account: []byte{2, 3}}},
			Completed: true,
		},
		&GetStorageRangeRequest{
			Root: bytes.Repeat([]byte{0x01}, 32), Account: []byte{1}, MaxBytes: 200,
		},
		&StorageRangeResponse{
			Slots: []*StorageEntry{{Key: []byte{1}, Value: []byte{2}}},
		},
		&GetCodeRequest{Hashes: [][]byte{{1, 2, 3}}},
		&CodeResponse{Codes: []*CodeEntry{{Hash: []byte{1}, Code: []byte{2, 3}}}},
	}

	for i, m := range msgs {
		data, err := m.MarshalSSZ()
		if err != nil {
			t.Fatalf("msg[%d] MarshalSSZ failed: %v", i, err)
		}
		if len(data) != m.SizeSSZ() {
			t.Errorf("msg[%d] SizeSSZ=%d but MarshalSSZ produced %d bytes", i, m.SizeSSZ(), len(data))
		}
	}
}
