// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package sync_pb

import (
	"bytes"
	"testing"
)

// FuzzSSZGetAccountRangeRequest feeds random bytes to GetAccountRangeRequest
// UnmarshalSSZ, ensuring it never panics.
func FuzzSSZGetAccountRangeRequest(f *testing.F) {
	// Seed: empty
	f.Add([]byte{})
	// Seed: too short
	f.Add([]byte{0x01, 0x02, 0x03})
	// Seed: valid minimal encoding (3 empty bytes fields + uint64)
	// Each bytes field: 4-byte length prefix (0) = 4 bytes, total = 3*4 + 8 = 20 bytes
	valid := make([]byte, 20)
	f.Add(valid)
	// Seed: with actual data in root field
	buf := make([]byte, 0, 100)
	msg := &GetAccountRangeRequest{
		Root:     bytes.Repeat([]byte{0xAA}, 32),
		Start:    bytes.Repeat([]byte{0x00}, 32),
		End:      bytes.Repeat([]byte{0xFF}, 32),
		MaxBytes: 1024,
	}
	encoded, _ := msg.MarshalSSZ()
	f.Add(encoded)
	_ = buf

	f.Fuzz(func(t *testing.T, data []byte) {
		m := new(GetAccountRangeRequest)
		_ = m.UnmarshalSSZ(data)
	})
}

// FuzzSSZGetAccountRangeRequestRoundtrip marshals a GetAccountRangeRequest and
// unmarshals it back, verifying the roundtrip produces identical results.
func FuzzSSZGetAccountRangeRequestRoundtrip(f *testing.F) {
	f.Add([]byte{0xAA}, []byte{0x00}, []byte{0xFF}, uint64(1024))
	f.Add(
		bytes.Repeat([]byte{0xBB}, 32),
		bytes.Repeat([]byte{0x01}, 32),
		bytes.Repeat([]byte{0xFE}, 32),
		uint64(65536),
	)
	f.Add([]byte{}, []byte{}, []byte{}, uint64(0))

	f.Fuzz(func(t *testing.T, root, start, end []byte, maxBytes uint64) {
		// Limit sizes to avoid excessive memory
		if len(root) > 1024 || len(start) > 1024 || len(end) > 1024 {
			return
		}

		original := &GetAccountRangeRequest{
			Root:     root,
			Start:    start,
			End:      end,
			MaxBytes: maxBytes,
		}

		encoded, err := original.MarshalSSZ()
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		decoded := new(GetAccountRangeRequest)
		err = decoded.UnmarshalSSZ(encoded)
		if err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if !bytes.Equal(original.Root, decoded.Root) {
			t.Fatalf("Root mismatch: %x != %x", original.Root, decoded.Root)
		}
		if !bytes.Equal(original.Start, decoded.Start) {
			t.Fatalf("Start mismatch: %x != %x", original.Start, decoded.Start)
		}
		if !bytes.Equal(original.End, decoded.End) {
			t.Fatalf("End mismatch: %x != %x", original.End, decoded.End)
		}
		if original.MaxBytes != decoded.MaxBytes {
			t.Fatalf("MaxBytes mismatch: %d != %d", original.MaxBytes, decoded.MaxBytes)
		}
	})
}

// FuzzSSZAccountRangeResponse feeds random bytes to AccountRangeResponse
// UnmarshalSSZ, ensuring it never panics.
func FuzzSSZAccountRangeResponse(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00, 0x00, 0x00, 0x00}) // count = 0, but missing NextStart/Completed
	// Valid empty response
	resp := &AccountRangeResponse{
		Accounts:  nil,
		NextStart: []byte{},
		Completed: true,
	}
	encoded, _ := resp.MarshalSSZ()
	f.Add(encoded)

	// Response with one account
	resp2 := &AccountRangeResponse{
		Accounts: []*AccountEntry{
			{Address: bytes.Repeat([]byte{0x01}, 20), Account: bytes.Repeat([]byte{0x02}, 64)},
		},
		NextStart: bytes.Repeat([]byte{0x03}, 32),
		Completed: false,
	}
	encoded2, _ := resp2.MarshalSSZ()
	f.Add(encoded2)

	f.Fuzz(func(t *testing.T, data []byte) {
		m := new(AccountRangeResponse)
		_ = m.UnmarshalSSZ(data)
	})
}

// FuzzSSZGetStorageRangeRequest feeds random bytes to GetStorageRangeRequest
// UnmarshalSSZ, ensuring it never panics.
func FuzzSSZGetStorageRangeRequest(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 10))
	msg := &GetStorageRangeRequest{
		Root:     bytes.Repeat([]byte{0xAA}, 32),
		Account:  bytes.Repeat([]byte{0xBB}, 20),
		Start:    bytes.Repeat([]byte{0x00}, 32),
		End:      bytes.Repeat([]byte{0xFF}, 32),
		MaxBytes: 2048,
	}
	encoded, _ := msg.MarshalSSZ()
	f.Add(encoded)

	f.Fuzz(func(t *testing.T, data []byte) {
		m := new(GetStorageRangeRequest)
		_ = m.UnmarshalSSZ(data)
	})
}

// FuzzSSZGetCodeRequest feeds random bytes to GetCodeRequest
// UnmarshalSSZ, ensuring it never panics.
func FuzzSSZGetCodeRequest(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 4)) // count = 0
	msg := &GetCodeRequest{
		Hashes: [][]byte{
			bytes.Repeat([]byte{0xAA}, 32),
			bytes.Repeat([]byte{0xBB}, 32),
		},
	}
	encoded, _ := msg.MarshalSSZ()
	f.Add(encoded)

	f.Fuzz(func(t *testing.T, data []byte) {
		m := new(GetCodeRequest)
		_ = m.UnmarshalSSZ(data)
	})
}

// FuzzSSZCodeResponse feeds random bytes to CodeResponse
// UnmarshalSSZ, ensuring it never panics.
func FuzzSSZCodeResponse(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 4))
	resp := &CodeResponse{
		Codes: []*CodeEntry{
			{Hash: bytes.Repeat([]byte{0xCC}, 32), Code: []byte{0x60, 0x00, 0x60, 0x00}},
		},
	}
	encoded, _ := resp.MarshalSSZ()
	f.Add(encoded)

	f.Fuzz(func(t *testing.T, data []byte) {
		m := new(CodeResponse)
		_ = m.UnmarshalSSZ(data)
	})
}

// FuzzSSZStorageRangeResponse feeds random bytes to StorageRangeResponse
// UnmarshalSSZ, ensuring it never panics.
func FuzzSSZStorageRangeResponse(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 4))
	resp := &StorageRangeResponse{
		Slots: []*StorageEntry{
			{Key: bytes.Repeat([]byte{0x01}, 32), Value: bytes.Repeat([]byte{0x02}, 32)},
		},
		NextStart: bytes.Repeat([]byte{0x03}, 32),
		Completed: true,
	}
	encoded, _ := resp.MarshalSSZ()
	f.Add(encoded)

	f.Fuzz(func(t *testing.T, data []byte) {
		m := new(StorageRangeResponse)
		_ = m.UnmarshalSSZ(data)
	})
}

// FuzzSSZGetStorageRangeRequestRoundtrip marshals and unmarshals a
// GetStorageRangeRequest, verifying roundtrip correctness.
func FuzzSSZGetStorageRangeRequestRoundtrip(f *testing.F) {
	f.Add([]byte{0xAA}, []byte{0xBB}, []byte{0x00}, []byte{0xFF}, uint64(4096))
	f.Add([]byte{}, []byte{}, []byte{}, []byte{}, uint64(0))

	f.Fuzz(func(t *testing.T, root, account, start, end []byte, maxBytes uint64) {
		if len(root) > 1024 || len(account) > 1024 || len(start) > 1024 || len(end) > 1024 {
			return
		}

		original := &GetStorageRangeRequest{
			Root:     root,
			Account:  account,
			Start:    start,
			End:      end,
			MaxBytes: maxBytes,
		}

		encoded, err := original.MarshalSSZ()
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		decoded := new(GetStorageRangeRequest)
		err = decoded.UnmarshalSSZ(encoded)
		if err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if !bytes.Equal(original.Root, decoded.Root) {
			t.Fatalf("Root mismatch")
		}
		if !bytes.Equal(original.Account, decoded.Account) {
			t.Fatalf("Account mismatch")
		}
		if !bytes.Equal(original.Start, decoded.Start) {
			t.Fatalf("Start mismatch")
		}
		if !bytes.Equal(original.End, decoded.End) {
			t.Fatalf("End mismatch")
		}
		if original.MaxBytes != decoded.MaxBytes {
			t.Fatalf("MaxBytes mismatch: %d != %d", original.MaxBytes, decoded.MaxBytes)
		}
	})
}
