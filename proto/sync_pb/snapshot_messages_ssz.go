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

// SSZ encoding for snapshot protocol messages.
// Uses the same binary helpers defined in snap_messages_ssz.go.

// =============================================================================
// GetSnapshotInfoRequest (empty)
// =============================================================================

func (m *GetSnapshotInfoRequest) MarshalSSZ() ([]byte, error) { return nil, nil }

func (m *GetSnapshotInfoRequest) MarshalSSZTo(buf []byte) ([]byte, error) { return buf, nil }

func (m *GetSnapshotInfoRequest) UnmarshalSSZ(buf []byte) error { return nil }

func (m *GetSnapshotInfoRequest) SizeSSZ() int { return 0 }

// =============================================================================
// SnapshotInfoEntry
// =============================================================================

func (m *SnapshotInfoEntry) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *SnapshotInfoEntry) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.BlockNumber)
	dst = putUint64(dst, m.AccountCount)
	dst = putUint64(dst, m.StorageCount)
	dst = putUint64(dst, m.CodeCount)
	return dst, nil
}

func (m *SnapshotInfoEntry) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.BlockNumber, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.AccountCount, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.StorageCount, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.CodeCount, _, err = readUint64(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *SnapshotInfoEntry) SizeSSZ() int { return 32 } // 4 * 8 bytes

// =============================================================================
// SnapshotInfoResponse
// =============================================================================

func (m *SnapshotInfoResponse) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *SnapshotInfoResponse) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, uint32(len(m.Snapshots)))
	for _, entry := range m.Snapshots {
		entryBytes, err := entry.MarshalSSZ()
		if err != nil {
			return nil, err
		}
		dst = putBytes(dst, entryBytes)
	}
	return dst, nil
}

func (m *SnapshotInfoResponse) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	var count uint32
	if count, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if count > maxArrayCount {
		return errTooMany
	}
	m.Snapshots = make([]*SnapshotInfoEntry, count)
	for i := uint32(0); i < count; i++ {
		var entryBytes []byte
		if entryBytes, off, err = readBytes(buf, off); err != nil {
			return err
		}
		m.Snapshots[i] = new(SnapshotInfoEntry)
		if err = m.Snapshots[i].UnmarshalSSZ(entryBytes); err != nil {
			return err
		}
	}
	return nil
}

func (m *SnapshotInfoResponse) SizeSSZ() int {
	size := 4
	for _, entry := range m.Snapshots {
		size += sizeBytes(nil) + entry.SizeSSZ()
	}
	return size
}

// =============================================================================
// CompressedBatchResponse
// =============================================================================

func (m *CompressedBatchResponse) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *CompressedBatchResponse) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, m.EntryCount)
	dst = putBool(dst, m.Completed)
	dst = putBytes(dst, m.NextStart)
	dst = putBytes(dst, m.Data)
	return dst, nil
}

func (m *CompressedBatchResponse) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.EntryCount, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if m.Completed, off, err = readBool(buf, off); err != nil {
		return err
	}
	if m.NextStart, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Data, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *CompressedBatchResponse) SizeSSZ() int {
	return 4 + 1 + sizeBytes(m.NextStart) + sizeBytes(m.Data)
}

// =============================================================================
// GetSnapshotAccountRangeRequest
// =============================================================================

func (m *GetSnapshotAccountRangeRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *GetSnapshotAccountRangeRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.SnapshotBlock)
	dst = putBytes(dst, m.Start)
	dst = putBytes(dst, m.End)
	dst = putUint64(dst, m.MaxBytes)
	return dst, nil
}

func (m *GetSnapshotAccountRangeRequest) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.SnapshotBlock, off, err = readUint64(buf, off); err != nil {
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

func (m *GetSnapshotAccountRangeRequest) SizeSSZ() int {
	return 8 + sizeBytes(m.Start) + sizeBytes(m.End) + 8
}

// =============================================================================
// GetSnapshotStorageRangeRequest
// =============================================================================

func (m *GetSnapshotStorageRangeRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *GetSnapshotStorageRangeRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.SnapshotBlock)
	dst = putBytes(dst, m.Account)
	dst = putUint64(dst, uint64(m.Incarnation))
	dst = putBytes(dst, m.Start)
	dst = putBytes(dst, m.End)
	dst = putUint64(dst, m.MaxBytes)
	return dst, nil
}

func (m *GetSnapshotStorageRangeRequest) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.SnapshotBlock, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.Account, off, err = readBytes(buf, off); err != nil {
		return err
	}
	var inc uint64
	if inc, off, err = readUint64(buf, off); err != nil {
		return err
	}
	m.Incarnation = uint16(inc)
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

func (m *GetSnapshotStorageRangeRequest) SizeSSZ() int {
	return 8 + sizeBytes(m.Account) + 8 + sizeBytes(m.Start) + sizeBytes(m.End) + 8
}

// =============================================================================
// GetChangeSetRangeRequest
// =============================================================================

func (m *GetChangeSetRangeRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *GetChangeSetRangeRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.FromBlock)
	dst = putUint64(dst, m.ToBlock)
	dst = putUint64(dst, m.MaxBytes)
	return dst, nil
}

func (m *GetChangeSetRangeRequest) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.FromBlock, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.ToBlock, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.MaxBytes, _, err = readUint64(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *GetChangeSetRangeRequest) SizeSSZ() int { return 24 }

// =============================================================================
// ChangeSetEntry
// =============================================================================

func (m *ChangeSetEntry) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *ChangeSetEntry) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.BlockNumber)
	dst = putBytes(dst, m.Address)
	dst = putBytes(dst, m.Data)
	return dst, nil
}

func (m *ChangeSetEntry) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.BlockNumber, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.Address, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Data, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *ChangeSetEntry) SizeSSZ() int {
	return 8 + sizeBytes(m.Address) + sizeBytes(m.Data)
}

// =============================================================================
// ChangeSetRangeResponse
// =============================================================================

func (m *ChangeSetRangeResponse) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *ChangeSetRangeResponse) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, uint32(len(m.Entries)))
	for _, entry := range m.Entries {
		entryBytes, err := entry.MarshalSSZ()
		if err != nil {
			return nil, err
		}
		dst = putBytes(dst, entryBytes)
	}
	dst = putBool(dst, m.Completed)
	return dst, nil
}

func (m *ChangeSetRangeResponse) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	var count uint32
	if count, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if count > maxArrayCount {
		return errTooMany
	}
	m.Entries = make([]*ChangeSetEntry, count)
	for i := uint32(0); i < count; i++ {
		var entryBytes []byte
		if entryBytes, off, err = readBytes(buf, off); err != nil {
			return err
		}
		m.Entries[i] = new(ChangeSetEntry)
		if err = m.Entries[i].UnmarshalSSZ(entryBytes); err != nil {
			return err
		}
	}
	if m.Completed, _, err = readBool(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *ChangeSetRangeResponse) SizeSSZ() int {
	size := 4
	for _, entry := range m.Entries {
		size += sizeBytes(nil) + entry.SizeSSZ()
	}
	size += 1 // Completed
	return size
}
