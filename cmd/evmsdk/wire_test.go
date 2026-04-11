// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"errors"
	"testing"
)

func TestIsV2WireFormat(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"valid magic", []byte{0x4E, 0x32, 0x01, 0x00}, true},
		{"valid magic with extra data", []byte{0x4E, 0x32, 0x01, 0x01, 0xFF}, true},
		{"wrong first byte", []byte{0x00, 0x32, 0x01, 0x00}, false},
		{"wrong second byte", []byte{0x4E, 0x00, 0x01, 0x00}, false},
		{"both bytes wrong", []byte{0x00, 0x00, 0x01, 0x00}, false},
		{"too short single byte", []byte{0x4E}, false},
		{"empty data", []byte{}, false},
		{"nil data", nil, false},
		{"just magic bytes", []byte{0x4E, 0x32}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsV2WireFormat(tt.data)
			if got != tt.want {
				t.Errorf("IsV2WireFormat(%v) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestWireHeaderRoundtrip(t *testing.T) {
	encoded := EncodeWireHeader(WireVersion1, 0x00)

	if len(encoded) != WireHeaderSize {
		t.Fatalf("EncodeWireHeader() len = %d, want %d", len(encoded), WireHeaderSize)
	}

	hdr, payload, err := DecodeWireHeader(encoded)
	if err != nil {
		t.Fatalf("DecodeWireHeader() error = %v", err)
	}

	if hdr.Version != WireVersion1 {
		t.Errorf("Version = %d, want %d", hdr.Version, WireVersion1)
	}
	if hdr.Flags != 0x00 {
		t.Errorf("Flags = 0x%02x, want 0x00", hdr.Flags)
	}
	if len(payload) != 0 {
		t.Errorf("payload len = %d, want 0", len(payload))
	}
}

func TestWireHeaderWithFlags(t *testing.T) {
	encoded := EncodeWireHeader(WireVersion1, FlagHasBytecodes)

	hdr, _, err := DecodeWireHeader(encoded)
	if err != nil {
		t.Fatalf("DecodeWireHeader() error = %v", err)
	}

	if !hdr.HasBytecodes() {
		t.Error("HasBytecodes() = false, want true")
	}

	// Verify flag byte value.
	if hdr.Flags != FlagHasBytecodes {
		t.Errorf("Flags = 0x%02x, want 0x%02x", hdr.Flags, FlagHasBytecodes)
	}

	// Without flag.
	encodedNoFlag := EncodeWireHeader(WireVersion1, 0x00)
	hdrNoFlag, _, err := DecodeWireHeader(encodedNoFlag)
	if err != nil {
		t.Fatalf("DecodeWireHeader() error = %v", err)
	}
	if hdrNoFlag.HasBytecodes() {
		t.Error("HasBytecodes() = true, want false")
	}
}

func TestWireHeaderErrors(t *testing.T) {
	t.Run("too short", func(t *testing.T) {
		_, _, err := DecodeWireHeader([]byte{0x4E, 0x32})
		if !errors.Is(err, ErrWireTooShort) {
			t.Errorf("error = %v, want %v", err, ErrWireTooShort)
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, _, err := DecodeWireHeader([]byte{})
		if !errors.Is(err, ErrWireTooShort) {
			t.Errorf("error = %v, want %v", err, ErrWireTooShort)
		}
	})

	t.Run("invalid magic", func(t *testing.T) {
		_, _, err := DecodeWireHeader([]byte{0x00, 0x00, 0x01, 0x00})
		if !errors.Is(err, ErrWireInvalidMagic) {
			t.Errorf("error = %v, want %v", err, ErrWireInvalidMagic)
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		_, _, err := DecodeWireHeader([]byte{0x4E, 0x32, 0xFF, 0x00})
		if !errors.Is(err, ErrWireUnsupportedVersion) {
			t.Errorf("error = %v, want %v", err, ErrWireUnsupportedVersion)
		}
	})

	t.Run("version zero", func(t *testing.T) {
		_, _, err := DecodeWireHeader([]byte{0x4E, 0x32, 0x00, 0x00})
		if !errors.Is(err, ErrWireUnsupportedVersion) {
			t.Errorf("error = %v, want %v", err, ErrWireUnsupportedVersion)
		}
	})
}

func TestWireHeaderPayloadPreserved(t *testing.T) {
	extra := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	data := append(EncodeWireHeader(WireVersion1, 0x00), extra...)

	_, payload, err := DecodeWireHeader(data)
	if err != nil {
		t.Fatalf("DecodeWireHeader() error = %v", err)
	}

	if len(payload) != len(extra) {
		t.Fatalf("payload len = %d, want %d", len(payload), len(extra))
	}
	for i := range extra {
		if payload[i] != extra[i] {
			t.Errorf("payload[%d] = 0x%02x, want 0x%02x", i, payload[i], extra[i])
		}
	}
}
