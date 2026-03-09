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

// SSZ-compatible binary encoding for blob sidecar protocol messages.
// Uses the same length-prefixed binary helpers defined in snap_messages_ssz.go.

// =============================================================================
// BlobSidecarsByRangeRequest
// =============================================================================

func (m *BlobSidecarsByRangeRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *BlobSidecarsByRangeRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.StartBlock)
	dst = putUint64(dst, m.Count)
	return dst, nil
}

func (m *BlobSidecarsByRangeRequest) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.StartBlock, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.Count, _, err = readUint64(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *BlobSidecarsByRangeRequest) SizeSSZ() int { return 16 }

// =============================================================================
// BlobIdentifierMsg
// =============================================================================

func (m *BlobIdentifierMsg) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *BlobIdentifierMsg) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putBytes(dst, m.BlockRoot)
	dst = putUint64(dst, m.Index)
	return dst, nil
}

func (m *BlobIdentifierMsg) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.BlockRoot, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Index, _, err = readUint64(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *BlobIdentifierMsg) SizeSSZ() int {
	return sizeBytes(m.BlockRoot) + 8
}

// =============================================================================
// BlobSidecarsByRootRequest
// =============================================================================

func (m *BlobSidecarsByRootRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *BlobSidecarsByRootRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, uint32(len(m.Identifiers)))
	for _, id := range m.Identifiers {
		entryBytes, err := id.MarshalSSZ()
		if err != nil {
			return nil, err
		}
		dst = putBytes(dst, entryBytes)
	}
	return dst, nil
}

func (m *BlobSidecarsByRootRequest) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	var count uint32
	if count, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if count > maxArrayCount {
		return errTooMany
	}
	m.Identifiers = make([]*BlobIdentifierMsg, count)
	for i := uint32(0); i < count; i++ {
		var entryBytes []byte
		if entryBytes, off, err = readBytes(buf, off); err != nil {
			return err
		}
		m.Identifiers[i] = new(BlobIdentifierMsg)
		if err = m.Identifiers[i].UnmarshalSSZ(entryBytes); err != nil {
			return err
		}
	}
	return nil
}

func (m *BlobSidecarsByRootRequest) SizeSSZ() int {
	size := 4 // count
	for _, id := range m.Identifiers {
		size += sizeBytes(nil) + id.SizeSSZ()
	}
	return size
}

// =============================================================================
// BlobSidecarMsg
// =============================================================================

func (m *BlobSidecarMsg) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *BlobSidecarMsg) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.Index)
	dst = putBytes(dst, m.Blob)
	dst = putBytes(dst, m.KZGCommitment)
	dst = putBytes(dst, m.KZGProof)
	dst = putUint64(dst, m.BlockNumber)
	dst = putBytes(dst, m.BlockHash)
	dst = putBytes(dst, m.TxHash)
	return dst, nil
}

func (m *BlobSidecarMsg) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.Index, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.Blob, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.KZGCommitment, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.KZGProof, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.BlockNumber, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.BlockHash, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.TxHash, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

func (m *BlobSidecarMsg) SizeSSZ() int {
	return 8 + sizeBytes(m.Blob) + sizeBytes(m.KZGCommitment) + sizeBytes(m.KZGProof) +
		8 + sizeBytes(m.BlockHash) + sizeBytes(m.TxHash)
}

// =============================================================================
// BlobSidecarsResponse
// =============================================================================

func (m *BlobSidecarsResponse) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *BlobSidecarsResponse) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint32(dst, uint32(len(m.Sidecars)))
	for _, sc := range m.Sidecars {
		entryBytes, err := sc.MarshalSSZ()
		if err != nil {
			return nil, err
		}
		dst = putBytes(dst, entryBytes)
	}
	return dst, nil
}

func (m *BlobSidecarsResponse) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	var count uint32
	if count, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if count > maxArrayCount {
		return errTooMany
	}
	m.Sidecars = make([]*BlobSidecarMsg, count)
	for i := uint32(0); i < count; i++ {
		var entryBytes []byte
		if entryBytes, off, err = readBytes(buf, off); err != nil {
			return err
		}
		m.Sidecars[i] = new(BlobSidecarMsg)
		if err = m.Sidecars[i].UnmarshalSSZ(entryBytes); err != nil {
			return err
		}
	}
	return nil
}

func (m *BlobSidecarsResponse) SizeSSZ() int {
	size := 4 // count
	for _, sc := range m.Sidecars {
		size += sizeBytes(nil) + sc.SizeSSZ()
	}
	return size
}
