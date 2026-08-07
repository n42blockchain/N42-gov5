// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"lukechampine.com/blake3"
)

var portableSnapshotMagic = [8]byte{'N', '4', '2', 'Q', 'M', 'D', 'B', 1}

const (
	portableSnapshotDigestSize = 32
	portableSnapshotHeaderSize = 8 + 8 + 32 + 8 + 32 + 32 + 8 + 8
	portableSnapshotEntrySize  = 8 + 1 + 32 + 4
	maxPortableValueSize       = 16 << 20
)

// PortableSnapshot is the cross-client QMDB bootstrap object. Entries must
// contain every append slot in order, including inactive slots whose frozen
// leaf hashes remain part of the split commitment.
type PortableSnapshot struct {
	ChainID     uint64
	GenesisHash Hash
	BlockNumber uint64
	BlockHash   Hash
	Root        Hash
	NextSlot    uint64
	Entries     []SlotEntry
}

// PortableSnapshotMetadata is the fixed checkpoint header used by the
// streaming exporter, which does not materialize the complete slot log.
type PortableSnapshotMetadata struct {
	ChainID     uint64
	GenesisHash Hash
	BlockNumber uint64
	BlockHash   Hash
	Root        Hash
	NextSlot    uint64
}

// PortableSlotSource returns exactly one occupied entry for the requested
// append slot. Callers are invoked monotonically from zero to NextSlot-1.
type PortableSlotSource func(slot uint64) (SlotEntry, error)

// MarshalPortableSnapshot encodes a deterministic, self-checking bootstrap.
// The final Blake3 digest covers the complete preceding payload.
func MarshalPortableSnapshot(snapshot *PortableSnapshot) ([]byte, error) {
	if err := validatePortableSnapshot(snapshot); err != nil {
		return nil, err
	}
	capacity := portableSnapshotHeaderSize + portableSnapshotDigestSize
	for _, entry := range snapshot.Entries {
		capacity += portableSnapshotEntrySize + len(entry.Value)
	}
	var out bytes.Buffer
	out.Grow(capacity)
	_, err := WritePortableSnapshot(&out, PortableSnapshotMetadata{
		ChainID:     snapshot.ChainID,
		GenesisHash: snapshot.GenesisHash,
		BlockNumber: snapshot.BlockNumber,
		BlockHash:   snapshot.BlockHash,
		Root:        snapshot.Root,
		NextSlot:    snapshot.NextSlot,
	}, func(slot uint64) (SlotEntry, error) {
		return snapshot.Entries[slot], nil
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// WritePortableSnapshot streams the exact portable v1 byte layout to w while
// computing its trailing Blake3 digest. It keeps only one entry in memory.
func WritePortableSnapshot(w io.Writer, metadata PortableSnapshotMetadata, source PortableSlotSource) (int64, error) {
	if source == nil {
		return 0, errors.New("qmdb: nil portable slot source")
	}
	hasher := blake3.New(32, nil)
	payload := &countingWriter{w: io.MultiWriter(w, hasher)}
	if err := writePortableHeader(payload, metadata); err != nil {
		return payload.n, err
	}
	for expected := uint64(0); expected < metadata.NextSlot; expected++ {
		entry, err := source(expected)
		if err != nil {
			return payload.n, err
		}
		if entry.Slot != expected {
			return payload.n, fmt.Errorf("qmdb: portable slot log is not contiguous: expected %d, got %d", expected, entry.Slot)
		}
		if len(entry.Value) > maxPortableValueSize {
			return payload.n, fmt.Errorf("qmdb: portable slot %d value is too large: %d", entry.Slot, len(entry.Value))
		}
		var fixed [portableSnapshotEntrySize]byte
		binary.LittleEndian.PutUint64(fixed[:8], entry.Slot)
		if entry.Active {
			fixed[8] = 1
		}
		copy(fixed[9:41], entry.KeyHash[:])
		binary.LittleEndian.PutUint32(fixed[41:45], uint32(len(entry.Value)))
		if _, err := payload.Write(fixed[:]); err != nil {
			return payload.n, err
		}
		if _, err := payload.Write(entry.Value); err != nil {
			return payload.n, err
		}
	}
	digest := hasher.Sum(nil)
	n, err := w.Write(digest)
	return payload.n + int64(n), err
}

func writePortableHeader(w io.Writer, metadata PortableSnapshotMetadata) error {
	header := make([]byte, 0, portableSnapshotHeaderSize)
	header = append(header, portableSnapshotMagic[:]...)
	header = binary.LittleEndian.AppendUint64(header, metadata.ChainID)
	header = append(header, metadata.GenesisHash[:]...)
	header = binary.LittleEndian.AppendUint64(header, metadata.BlockNumber)
	header = append(header, metadata.BlockHash[:]...)
	header = append(header, metadata.Root[:]...)
	header = binary.LittleEndian.AppendUint64(header, metadata.NextSlot)
	header = binary.LittleEndian.AppendUint64(header, metadata.NextSlot)
	_, err := w.Write(header)
	return err
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (w *countingWriter) Write(data []byte) (int, error) {
	n, err := w.w.Write(data)
	w.n += int64(n)
	return n, err
}

// UnmarshalPortableSnapshot verifies the content hash and rejects sparse,
// reordered, oversized, cross-version, or trailing data before returning it.
func UnmarshalPortableSnapshot(encoded []byte) (*PortableSnapshot, error) {
	if len(encoded) < portableSnapshotHeaderSize+portableSnapshotDigestSize {
		return nil, errors.New("qmdb: portable snapshot is truncated")
	}
	payload := encoded[:len(encoded)-portableSnapshotDigestSize]
	digest := blake3.Sum256(payload)
	if !bytes.Equal(digest[:], encoded[len(payload):]) {
		return nil, errors.New("qmdb: portable snapshot content hash mismatch")
	}
	reader := portableReader{data: payload}
	magic, err := reader.take(len(portableSnapshotMagic))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(magic, portableSnapshotMagic[:]) {
		return nil, errors.New("qmdb: unsupported portable snapshot version")
	}
	snapshot := &PortableSnapshot{}
	if snapshot.ChainID, err = reader.u64(); err != nil {
		return nil, err
	}
	if err = reader.hash(&snapshot.GenesisHash); err != nil {
		return nil, err
	}
	if snapshot.BlockNumber, err = reader.u64(); err != nil {
		return nil, err
	}
	if err = reader.hash(&snapshot.BlockHash); err != nil {
		return nil, err
	}
	if err = reader.hash(&snapshot.Root); err != nil {
		return nil, err
	}
	if snapshot.NextSlot, err = reader.u64(); err != nil {
		return nil, err
	}
	entryCount, err := reader.u64()
	if err != nil {
		return nil, err
	}
	if entryCount != snapshot.NextSlot {
		return nil, fmt.Errorf("qmdb: portable entry count %d does not equal next slot %d", entryCount, snapshot.NextSlot)
	}
	if entryCount > uint64(reader.remaining()/portableSnapshotEntrySize) {
		return nil, errors.New("qmdb: portable entry count exceeds remaining bytes")
	}
	snapshot.Entries = make([]SlotEntry, 0, int(entryCount))
	for expected := uint64(0); expected < entryCount; expected++ {
		slot, readErr := reader.u64()
		if readErr != nil {
			return nil, readErr
		}
		if slot != expected {
			return nil, fmt.Errorf("qmdb: portable slot log is not contiguous: expected %d, got %d", expected, slot)
		}
		active, readErr := reader.byte()
		if readErr != nil {
			return nil, readErr
		}
		if active > 1 {
			return nil, fmt.Errorf("qmdb: portable slot %d has invalid active flag %d", slot, active)
		}
		entry := SlotEntry{Slot: slot, Active: active == 1}
		if readErr = reader.hash(&entry.KeyHash); readErr != nil {
			return nil, readErr
		}
		valueLen, readErr := reader.u32()
		if readErr != nil {
			return nil, readErr
		}
		if valueLen > maxPortableValueSize {
			return nil, fmt.Errorf("qmdb: portable slot %d value is too large: %d", slot, valueLen)
		}
		value, readErr := reader.take(int(valueLen))
		if readErr != nil {
			return nil, readErr
		}
		entry.Value = append([]byte(nil), value...)
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	if reader.remaining() != 0 {
		return nil, fmt.Errorf("qmdb: portable snapshot has %d trailing bytes", reader.remaining())
	}
	return snapshot, nil
}

func validatePortableSnapshot(snapshot *PortableSnapshot) error {
	if snapshot == nil {
		return errors.New("qmdb: nil portable snapshot")
	}
	if snapshot.NextSlot != uint64(len(snapshot.Entries)) {
		return fmt.Errorf("qmdb: portable snapshot has %d entries but next slot is %d", len(snapshot.Entries), snapshot.NextSlot)
	}
	for expected, entry := range snapshot.Entries {
		if entry.Slot != uint64(expected) {
			return fmt.Errorf("qmdb: portable slot log is not contiguous: expected %d, got %d", expected, entry.Slot)
		}
		if len(entry.Value) > maxPortableValueSize {
			return fmt.Errorf("qmdb: portable slot %d value is too large: %d", entry.Slot, len(entry.Value))
		}
	}
	return nil
}

type portableReader struct {
	data []byte
	pos  int
}

func (r *portableReader) remaining() int { return len(r.data) - r.pos }

func (r *portableReader) take(size int) ([]byte, error) {
	if size < 0 || size > r.remaining() {
		return nil, errors.New("qmdb: portable snapshot is truncated")
	}
	out := r.data[r.pos : r.pos+size]
	r.pos += size
	return out, nil
}

func (r *portableReader) byte() (byte, error) {
	data, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return data[0], nil
}

func (r *portableReader) u32() (uint32, error) {
	data, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(data), nil
}

func (r *portableReader) u64() (uint64, error) {
	data, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(data), nil
}

func (r *portableReader) hash(out *Hash) error {
	data, err := r.take(len(out))
	if err != nil {
		return err
	}
	copy(out[:], data)
	return nil
}
