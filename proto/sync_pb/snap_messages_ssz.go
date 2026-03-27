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

// SSZ-compatible binary encoding for snap sync protocol messages.
// Uses a simple length-prefixed binary format that satisfies the
// fastssz.Marshaler and fastssz.Unmarshaler interfaces.

import (
	"encoding/binary"
	"errors"
)

var (
	errShortBuf    = errors.New("snap_ssz: buffer too short")
	errOverflow    = errors.New("snap_ssz: length overflow")
	errTooMany     = errors.New("snap_ssz: array count exceeds limit")
)

// maxArrayCount is the maximum number of entries allowed in a deserialized
// array to prevent OOM from malicious messages.
const maxArrayCount = 16384

// maxBytesLen is the maximum length of a single bytes field to prevent
// unbounded memory allocation on deserialization.
const maxBytesLen = 2 * 1024 * 1024 // 2 MB

// --- binary encoding helpers ---

func putUint64(dst []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return append(dst, b...)
}

func putBool(dst []byte, v bool) []byte {
	if v {
		return append(dst, 1)
	}
	return append(dst, 0)
}

func putBytes(dst, data []byte) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(len(data)))
	dst = append(dst, b...)
	return append(dst, data...)
}

func readUint64(buf []byte, offset int) (uint64, int, error) {
	if offset+8 > len(buf) {
		return 0, offset, errShortBuf
	}
	return binary.LittleEndian.Uint64(buf[offset : offset+8]), offset + 8, nil
}

func readBool(buf []byte, offset int) (bool, int, error) {
	if offset+1 > len(buf) {
		return false, offset, errShortBuf
	}
	return buf[offset] != 0, offset + 1, nil
}

func readBytes(buf []byte, offset int) ([]byte, int, error) {
	if offset+4 > len(buf) {
		return nil, offset, errShortBuf
	}
	n := binary.LittleEndian.Uint32(buf[offset : offset+4])
	offset += 4
	if n > maxBytesLen {
		return nil, offset, errOverflow
	}
	end := offset + int(n)
	if end > len(buf) || end < offset { // overflow check
		return nil, offset, errShortBuf
	}
	data := make([]byte, n)
	copy(data, buf[offset:end])
	return data, end, nil
}

func readUint32(buf []byte, offset int) (uint32, int, error) {
	if offset+4 > len(buf) {
		return 0, offset, errShortBuf
	}
	return binary.LittleEndian.Uint32(buf[offset : offset+4]), offset + 4, nil
}

func putUint32(dst []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return append(dst, b...)
}

func sizeBytes(data []byte) int { return 4 + len(data) }

// =============================================================================
// GetAccountRangeRequest
// =============================================================================

func (m *GetAccountRangeRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *GetAccountRangeRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putBytes(dst, m.Root)
	dst = putBytes(dst, m.Start)
	dst = putBytes(dst, m.End)
	dst = putUint64(dst, m.MaxBytes)
	return dst, nil
}

func (m *GetAccountRangeRequest) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.Root, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Start, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.End, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.MaxBytes, _, err = readUint64(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *GetAccountRangeRequest) SizeSSZ() int {
	return sizeBytes(m.Root) + sizeBytes(m.Start) + sizeBytes(m.End) + 8
}

// =============================================================================
// AccountEntry
// =============================================================================

func (m *AccountEntry) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *AccountEntry) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putBytes(dst, m.Address)
	dst = putBytes(dst, m.Account)
	return dst, nil
}

func (m *AccountEntry) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.Address, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Account, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *AccountEntry) SizeSSZ() int {
	return sizeBytes(m.Address) + sizeBytes(m.Account)
}

// =============================================================================
// AccountRangeResponse
// =============================================================================

func (m *AccountRangeResponse) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *AccountRangeResponse) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, uint32(len(m.Accounts)))
	for _, entry := range m.Accounts {
		entryBytes, err := entry.MarshalSSZ()
		if err != nil {
			return nil, err
		}
		dst = putBytes(dst, entryBytes)
	}
	dst = putBytes(dst, m.NextStart)
	dst = putBool(dst, m.Completed)
	return dst, nil
}

func (m *AccountRangeResponse) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	var count uint32
	if count, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if count > maxArrayCount {
		return errTooMany
	}
	m.Accounts = make([]*AccountEntry, count)
	for i := uint32(0); i < count; i++ {
		var entryBytes []byte
		if entryBytes, off, err = readBytes(buf, off); err != nil {
			return err
		}
		m.Accounts[i] = new(AccountEntry)
		if err = m.Accounts[i].UnmarshalSSZ(entryBytes); err != nil {
			return err
		}
	}
	if m.NextStart, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Completed, _, err = readBool(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *AccountRangeResponse) SizeSSZ() int {
	size := 4 // account count
	for _, entry := range m.Accounts {
		size += sizeBytes(nil) + entry.SizeSSZ() // length prefix + entry
	}
	size += sizeBytes(m.NextStart) + 1 // NextStart + Completed
	return size
}

// =============================================================================
// GetStorageRangeRequest
// =============================================================================

func (m *GetStorageRangeRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *GetStorageRangeRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putBytes(dst, m.Root)
	dst = putBytes(dst, m.Account)
	dst = putBytes(dst, m.Start)
	dst = putBytes(dst, m.End)
	dst = putUint64(dst, m.MaxBytes)
	return dst, nil
}

func (m *GetStorageRangeRequest) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.Root, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Account, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Start, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.End, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.MaxBytes, _, err = readUint64(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *GetStorageRangeRequest) SizeSSZ() int {
	return sizeBytes(m.Root) + sizeBytes(m.Account) + sizeBytes(m.Start) + sizeBytes(m.End) + 8
}

// =============================================================================
// StorageEntry
// =============================================================================

func (m *StorageEntry) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *StorageEntry) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putBytes(dst, m.Key)
	dst = putBytes(dst, m.Value)
	return dst, nil
}

func (m *StorageEntry) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.Key, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Value, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *StorageEntry) SizeSSZ() int {
	return sizeBytes(m.Key) + sizeBytes(m.Value)
}

// =============================================================================
// StorageRangeResponse
// =============================================================================

func (m *StorageRangeResponse) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *StorageRangeResponse) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, uint32(len(m.Slots)))
	for _, entry := range m.Slots {
		entryBytes, err := entry.MarshalSSZ()
		if err != nil {
			return nil, err
		}
		dst = putBytes(dst, entryBytes)
	}
	dst = putBytes(dst, m.NextStart)
	dst = putBool(dst, m.Completed)
	return dst, nil
}

func (m *StorageRangeResponse) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	var count uint32
	if count, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if count > maxArrayCount {
		return errTooMany
	}
	m.Slots = make([]*StorageEntry, count)
	for i := uint32(0); i < count; i++ {
		var entryBytes []byte
		if entryBytes, off, err = readBytes(buf, off); err != nil {
			return err
		}
		m.Slots[i] = new(StorageEntry)
		if err = m.Slots[i].UnmarshalSSZ(entryBytes); err != nil {
			return err
		}
	}
	if m.NextStart, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Completed, _, err = readBool(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *StorageRangeResponse) SizeSSZ() int {
	size := 4
	for _, entry := range m.Slots {
		size += sizeBytes(nil) + entry.SizeSSZ()
	}
	size += sizeBytes(m.NextStart) + 1
	return size
}

// =============================================================================
// GetCodeRequest
// =============================================================================

func (m *GetCodeRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *GetCodeRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, uint32(len(m.Hashes)))
	for _, h := range m.Hashes {
		dst = putBytes(dst, h)
	}
	return dst, nil
}

func (m *GetCodeRequest) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	var count uint32
	if count, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if count > maxArrayCount {
		return errTooMany
	}
	m.Hashes = make([][]byte, count)
	for i := uint32(0); i < count; i++ {
		if m.Hashes[i], off, err = readBytes(buf, off); err != nil {
			return err
		}
	}
	return nil
}

func (m *GetCodeRequest) SizeSSZ() int {
	size := 4
	for _, h := range m.Hashes {
		size += sizeBytes(h)
	}
	return size
}

// =============================================================================
// CodeEntry
// =============================================================================

func (m *CodeEntry) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *CodeEntry) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putBytes(dst, m.Hash)
	dst = putBytes(dst, m.Code)
	return dst, nil
}

func (m *CodeEntry) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.Hash, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Code, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *CodeEntry) SizeSSZ() int {
	return sizeBytes(m.Hash) + sizeBytes(m.Code)
}

// =============================================================================
// CodeResponse
// =============================================================================

func (m *CodeResponse) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *CodeResponse) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, uint32(len(m.Codes)))
	for _, entry := range m.Codes {
		entryBytes, err := entry.MarshalSSZ()
		if err != nil {
			return nil, err
		}
		dst = putBytes(dst, entryBytes)
	}
	return dst, nil
}

func (m *CodeResponse) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	var count uint32
	if count, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if count > maxArrayCount {
		return errTooMany
	}
	m.Codes = make([]*CodeEntry, count)
	for i := uint32(0); i < count; i++ {
		var entryBytes []byte
		if entryBytes, off, err = readBytes(buf, off); err != nil {
			return err
		}
		m.Codes[i] = new(CodeEntry)
		if err = m.Codes[i].UnmarshalSSZ(entryBytes); err != nil {
			return err
		}
	}
	return nil
}

func (m *CodeResponse) SizeSSZ() int {
	size := 4
	for _, entry := range m.Codes {
		size += sizeBytes(nil) + entry.SizeSSZ()
	}
	return size
}
