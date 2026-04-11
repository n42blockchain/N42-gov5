// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// V2 wire-format header parser for the mobile SDK protocol.
// Declares WireMagic0 / WireMagic1 ("N2"), WireVersion1,
// WireHeaderSize (4 bytes) and the FlagHasBytecodes flag.
// Exposes ErrWireTooShort / ErrWireInvalidMagic /
// ErrWireUnsupportedVersion so callers can reject stale or
// corrupt transport frames before decoding a StreamPacket.

package evmsdk

import (
	"errors"
	"fmt"
)

// V2 Wire Format Header:
//
//	[0x4E, 0x32] magic ("N2")
//	[version]    1 byte (0x01)
//	[flags]      1 byte (0x00 = no bytecodes, 0x01 = has bytecodes)
//	Total: 4 bytes
const (
	WireMagic0       = 0x4E // 'N'
	WireMagic1       = 0x32 // '2'
	WireVersion1     = 0x01
	WireHeaderSize   = 4
	FlagHasBytecodes = 0x01
)

var (
	ErrWireTooShort           = errors.New("wire: data too short for V2 header")
	ErrWireInvalidMagic       = errors.New("wire: invalid magic bytes, expected 0x4E32 (\"N2\")")
	ErrWireUnsupportedVersion = errors.New("wire: unsupported wire version")
)

// WireHeader represents a decoded V2 wire format header.
type WireHeader struct {
	Version uint8
	Flags   uint8
}

// HasBytecodes returns true if the header flags indicate bytecodes are present.
func (h *WireHeader) HasBytecodes() bool {
	return h.Flags&FlagHasBytecodes != 0
}

// IsV2WireFormat checks if data starts with the V2 magic bytes "N2".
func IsV2WireFormat(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	return data[0] == WireMagic0 && data[1] == WireMagic1
}

// EncodeWireHeader writes a V2 wire header to a new byte slice.
func EncodeWireHeader(version, flags uint8) []byte {
	buf := make([]byte, WireHeaderSize)
	buf[0] = WireMagic0
	buf[1] = WireMagic1
	buf[2] = version
	buf[3] = flags
	return buf
}

// DecodeWireHeader parses a V2 wire header from data.
// Returns the parsed header and the remaining payload after the header.
func DecodeWireHeader(data []byte) (*WireHeader, []byte, error) {
	if len(data) < WireHeaderSize {
		return nil, nil, ErrWireTooShort
	}

	if data[0] != WireMagic0 || data[1] != WireMagic1 {
		return nil, nil, ErrWireInvalidMagic
	}

	version := data[2]
	if version != WireVersion1 {
		return nil, nil, fmt.Errorf("%w: got %d, expected %d", ErrWireUnsupportedVersion, version, WireVersion1)
	}

	hdr := &WireHeader{
		Version: version,
		Flags:   data[3],
	}
	return hdr, data[WireHeaderSize:], nil
}
