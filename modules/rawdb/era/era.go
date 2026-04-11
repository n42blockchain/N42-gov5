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
//
// EraE archive format constants, Header and IndexEntry types.
// Defines MagicBytes ("eraE"), Version 1, HeaderSize 14,
// IndexEntrySize 16 and FooterSize 8. EncodeHeader/DecodeHeader
// serialize the 4+2+8 magic||version||networkID header. Sentinel
// errors ErrInvalidMagic, ErrInvalidVersion, ErrBlockNotFound
// and ErrEmptyArchive surface decode and lookup failures.

package era

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// EraE is a standardized archive format for historical execution layer data.
// Format: Header(14B) + Records(variable) + Index(16B per entry) + Footer(8B)

const (
	MagicBytes     = "eraE"
	Version        = 1
	HeaderSize     = 14 // 4 magic + 2 version + 8 networkID
	IndexEntrySize = 16 // 8 blockNumber + 8 fileOffset
	FooterSize     = 8  // 4 indexOffset(uint32 would be too small, use int64) — actually 8: count(uint64)
)

var (
	ErrInvalidMagic   = errors.New("era: invalid magic bytes")
	ErrInvalidVersion = errors.New("era: unsupported version")
	ErrHeaderTooShort = errors.New("era: header data too short")
	ErrBlockNotFound  = errors.New("era: block not found in archive")
	ErrEmptyArchive   = errors.New("era: archive contains no records")
)

// Header is the file header for an EraE archive.
type Header struct {
	Magic     [4]byte
	Version   uint16
	NetworkID uint64
}

// IndexEntry maps a block number to a file offset within the archive.
type IndexEntry struct {
	BlockNumber uint64
	FileOffset  int64
}

// EncodeHeader serialises an EraE header for the given network ID.
func EncodeHeader(networkID uint64) []byte {
	buf := make([]byte, HeaderSize)
	copy(buf[0:4], MagicBytes)
	binary.BigEndian.PutUint16(buf[4:6], Version)
	binary.BigEndian.PutUint64(buf[6:14], networkID)
	return buf
}

// DecodeHeader parses an EraE header from raw bytes.
func DecodeHeader(data []byte) (*Header, error) {
	if len(data) < HeaderSize {
		return nil, ErrHeaderTooShort
	}
	var h Header
	copy(h.Magic[:], data[0:4])
	if string(h.Magic[:]) != MagicBytes {
		return nil, fmt.Errorf("%w: got %q", ErrInvalidMagic, string(h.Magic[:]))
	}
	h.Version = binary.BigEndian.Uint16(data[4:6])
	if h.Version != Version {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidVersion, h.Version)
	}
	h.NetworkID = binary.BigEndian.Uint64(data[6:14])
	return &h, nil
}
