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

// The tombstone portable snapshot (n42-rs docs/QMDB_COMPACTION_DESIGN.md
// section 6, "v2" there). It is the v1 slot log with one more header field
// and records that may drop a dead slot's payload:
//
//	header  v1's header, magic N42QMDB\x03, then tombstones u64 after
//	        entry_count (the number of kind-2 and kind-3 records)
//	record  [slot u64][kind u8] then
//	        kind 0  [key 32][len u32][value]  dead slot with its payload
//	        kind 1  [key 32][len u32][value]  live slot
//	        kind 2  [leaf_hash 32]            dead slot, payload dropped
//	        kind 3  [count u16][leaf_root 32] a whole dead twig of tombstones:
//	                slot % 2048 == 0, count == 2048; the next record is at
//	                slot + count
//	trailer Blake3 of every byte before it
//
// The version byte is 3, not 2: N42QMDB\x02 is the leaf-form layout
// (portable_v2.go, WritePortableSnapshotV2), and two layouts must not share
// a magic.
var portableSnapshotMagicTombstone = [8]byte{'N', '4', '2', 'Q', 'M', 'D', 'B', 3}

// Record kinds of the tombstone layout.
const (
	PortableKindDead      byte = 0
	PortableKindLive      byte = 1
	PortableKindTombstone byte = 2
	PortableKindDeadTwig  byte = 3
)

const (
	portableTombstoneHeaderSize = portableSnapshotHeaderSize + 8
	portableTombstoneRecordSize = 8 + 1 + 32
	portableDeadTwigRecordSize  = 8 + 1 + 2 + 32
)

// PortableFormat names a portable snapshot layout by its magic's version byte.
type PortableFormat byte

const (
	PortableFormatV1        PortableFormat = 1 // every slot's key and value
	PortableFormatLeafForm  PortableFormat = 2 // per-twig leaves + live entries (write-only)
	PortableFormatTombstone PortableFormat = 3 // the tombstone layout above
)

func (f PortableFormat) String() string {
	switch f {
	case PortableFormatV1:
		return "v1"
	case PortableFormatLeafForm:
		return "leaf-form"
	case PortableFormatTombstone:
		return "tombstone"
	}
	return fmt.Sprintf("unknown(%d)", byte(f))
}

// DetectPortableFormat returns the layout named by encoded's magic.
func DetectPortableFormat(encoded []byte) (PortableFormat, bool) {
	if len(encoded) < 8 {
		return 0, false
	}
	var magic [8]byte
	copy(magic[:], encoded[:8])
	switch magic {
	case portableSnapshotMagic:
		return PortableFormatV1, true
	case portableSnapshotMagicV2:
		return PortableFormatLeafForm, true
	case portableSnapshotMagicTombstone:
		return PortableFormatTombstone, true
	}
	return 0, false
}

// ErrHashOnlySlot matches every *HashOnlySlotError.
var ErrHashOnlySlot = errors.New("qmdb: slot keeps only its leaf hash")

// HashOnlySlotError is the refusal of a path that needs a slot's key and
// value (an undo revival, a v1 export, a flush without a leaf store) on a
// slot imported from a tombstone.
type HashOnlySlotError struct {
	Slot uint64
	Op   string
}

func (e *HashOnlySlotError) Error() string {
	return fmt.Sprintf("qmdb: slot %d keeps only its leaf hash (imported from a tombstone); %s needs its key and value", e.Slot, e.Op)
}

func (e *HashOnlySlotError) Is(target error) bool { return target == ErrHashOnlySlot }

// isHashOnly reports whether slot is a resident hash-only slot.
func (t *Tree) isHashOnly(slot uint64) bool {
	if slot < t.entriesBase {
		return false
	}
	i := slot - t.entriesBase
	return i < uint64(len(t.entries)) && t.entries[i].hashOnly
}

// HasHashOnlySlots reports whether any resident slot keeps only its leaf hash.
func (t *Tree) HasHashOnlySlots() bool {
	for i := range t.entries {
		if t.entries[i].hashOnly {
			return true
		}
	}
	return false
}

// PortableVerification is what ImportPortableSnapshot checked.
type PortableVerification struct {
	Format     PortableFormat
	Metadata   PortableSnapshotMetadata
	LiveCount  uint64
	Tombstones uint64 // kind-2 and kind-3 records (0 for v1)
}

// ImportPortableSnapshot authenticates a v1 or tombstone portable snapshot,
// binds it to (chainID, genesisHash) and rebuilds its tree, whose root must
// be the claimed one. A kind-0/1 record imports as in v1; a tombstone becomes
// a hash-only slot whose leaf is the stored hash; a kind-3 run becomes a
// pruned twig carrying only its leaf root (all bits clear), its slots
// hash-only. The leaf-form layout (N42QMDB\x02) has no reader.
func ImportPortableSnapshot(encoded []byte, chainID uint64, genesisHash Hash) (*Tree, *PortableVerification, error) {
	format, ok := DetectPortableFormat(encoded)
	if !ok {
		return nil, nil, errors.New("qmdb: unsupported portable snapshot version")
	}
	if format == PortableFormatLeafForm {
		return nil, nil, errors.New("qmdb: the leaf-form portable snapshot (N42QMDB\\x02) has no importer")
	}
	headerSize := portableSnapshotHeaderSize
	if format == PortableFormatTombstone {
		headerSize = portableTombstoneHeaderSize
	}
	if len(encoded) < headerSize+portableSnapshotDigestSize {
		return nil, nil, errors.New("qmdb: portable snapshot is truncated")
	}
	payload := encoded[:len(encoded)-portableSnapshotDigestSize]
	digest := blake3.Sum256(payload)
	if !bytes.Equal(digest[:], encoded[len(payload):]) {
		return nil, nil, errors.New("qmdb: portable snapshot content hash mismatch")
	}
	reader := portableReader{data: payload}
	if _, err := reader.take(8); err != nil {
		return nil, nil, err
	}
	var md PortableSnapshotMetadata
	var err error
	if md.ChainID, err = reader.u64(); err != nil {
		return nil, nil, err
	}
	if err = reader.hash(&md.GenesisHash); err != nil {
		return nil, nil, err
	}
	if md.BlockNumber, err = reader.u64(); err != nil {
		return nil, nil, err
	}
	if err = reader.hash(&md.BlockHash); err != nil {
		return nil, nil, err
	}
	if err = reader.hash(&md.Root); err != nil {
		return nil, nil, err
	}
	if md.NextSlot, err = reader.u64(); err != nil {
		return nil, nil, err
	}
	entryCount, err := reader.u64()
	if err != nil {
		return nil, nil, err
	}
	var claimedTombstones uint64
	if format == PortableFormatTombstone {
		if claimedTombstones, err = reader.u64(); err != nil {
			return nil, nil, err
		}
	}
	if md.ChainID != chainID {
		return nil, nil, fmt.Errorf("qmdb: portable snapshot chain id %d does not equal expected %d", md.ChainID, chainID)
	}
	if md.GenesisHash != genesisHash {
		return nil, nil, errors.New("qmdb: portable snapshot genesis hash does not match the expected chain")
	}
	if entryCount != md.NextSlot {
		return nil, nil, fmt.Errorf("qmdb: portable entry count %d does not equal next slot %d", entryCount, md.NextSlot)
	}
	// Every record covers at least its bytes' worth of slots: a kind-3 run
	// covers 2048 slots in 43 bytes, anything else one slot in >= 41.
	if maxSlots := uint64(reader.remaining()/portableDeadTwigRecordSize+1) * TwigSize; format == PortableFormatTombstone && entryCount > maxSlots ||
		format == PortableFormatV1 && entryCount > uint64(reader.remaining()/portableSnapshotEntrySize) {
		return nil, nil, errors.New("qmdb: portable entry count exceeds remaining bytes")
	}

	t := New()
	t.nextSlot = md.NextSlot
	t.entries = make([]entry, md.NextSlot)
	verification := &PortableVerification{Format: format, Metadata: md}
	for expected := uint64(0); expected < entryCount; {
		slot, err := reader.u64()
		if err != nil {
			return nil, nil, err
		}
		if slot != expected {
			return nil, nil, fmt.Errorf("qmdb: portable slot log is not contiguous: expected %d, got %d", expected, slot)
		}
		kind, err := reader.byte()
		if err != nil {
			return nil, nil, err
		}
		local := slot % TwigSize
		switch {
		case kind == PortableKindDead || kind == PortableKindLive:
			var keyHash Hash
			if err := reader.hash(&keyHash); err != nil {
				return nil, nil, err
			}
			valueLen, err := reader.u32()
			if err != nil {
				return nil, nil, err
			}
			if valueLen > maxPortableValueSize {
				return nil, nil, fmt.Errorf("qmdb: portable slot %d value is too large: %d", slot, valueLen)
			}
			raw, err := reader.take(int(valueLen))
			if err != nil {
				return nil, nil, err
			}
			value := append([]byte{}, raw...)
			tw := t.twigFor(slot)
			tw.nodes[TwigSize+local] = hashLeaf(keyHash, value)
			active := kind == PortableKindLive
			t.entries[slot] = entry{keyHash: keyHash, value: value, active: active}
			if active {
				if _, dup := t.idx.Get(keyHash); dup {
					return nil, nil, fmt.Errorf("qmdb: portable snapshot has duplicate active key %x", keyHash)
				}
				tw.setBit(local, true)
				tw.live++
				t.idx.Put(keyHash, slot)
				verification.LiveCount++
			}
			t.markTwigDirty(tw)
		case format == PortableFormatTombstone && kind == PortableKindTombstone:
			var leaf Hash
			if err := reader.hash(&leaf); err != nil {
				return nil, nil, err
			}
			tw := t.twigFor(slot)
			tw.nodes[TwigSize+local] = leaf
			t.entries[slot] = entry{hashOnly: true}
			t.markTwigDirty(tw)
			verification.Tombstones++
		case format == PortableFormatTombstone && kind == PortableKindDeadTwig:
			count, err := reader.u16()
			if err != nil {
				return nil, nil, err
			}
			var leafRoot Hash
			if err := reader.hash(&leafRoot); err != nil {
				return nil, nil, err
			}
			if local != 0 || count != TwigSize || slot+TwigSize > entryCount {
				return nil, nil, fmt.Errorf("qmdb: portable whole-dead-twig run at slot %d of %d slots does not cover exactly one twig from its start", slot, count)
			}
			id := int(slot / TwigSize)
			for len(t.twigs) < id {
				t.twigs = append(t.twigs, newTwig())
			}
			tw := newBareTwig()
			tw.leafRoot = leafRoot
			tw.root = hashNode(leafRoot, zeroBitsRoot)
			tw.pruned = true
			t.twigs = append(t.twigs, tw)
			for s := slot; s < slot+TwigSize; s++ {
				t.entries[s] = entry{hashOnly: true}
			}
			verification.Tombstones++
			expected += TwigSize
			continue
		case format == PortableFormatV1:
			return nil, nil, fmt.Errorf("qmdb: portable slot %d has invalid active flag %d", slot, kind)
		default:
			return nil, nil, fmt.Errorf("qmdb: portable slot %d has invalid record kind %d", slot, kind)
		}
		expected++
	}
	if reader.remaining() != 0 {
		return nil, nil, fmt.Errorf("qmdb: portable snapshot has %d trailing bytes", reader.remaining())
	}
	if verification.Tombstones != claimedTombstones {
		return nil, nil, fmt.Errorf("qmdb: portable snapshot header claims %d tombstone records, the stream has %d", claimedTombstones, verification.Tombstones)
	}
	t.upRebuild = true
	t.rootDirty = true
	if root := t.Root(); root != md.Root {
		return nil, nil, fmt.Errorf("qmdb: portable snapshot root %x does not match its slot log (%x)", md.Root, root)
	}
	return t, verification, nil
}

// PortableSlotSource reads t's slots for WritePortableSnapshot (v1). A
// hash-only slot is refused with a *HashOnlySlotError: v1 carries every
// slot's key and value.
func (t *Tree) PortableSlotSource() PortableSlotSource {
	return func(slot uint64) (SlotEntry, error) {
		if t.isHashOnly(slot) {
			return SlotEntry{}, &HashOnlySlotError{Slot: slot, Op: "a v1 portable export"}
		}
		keyHash, value, ok := t.slotRow(slot, t.cold)
		if !ok {
			return SlotEntry{}, fmt.Errorf("qmdb: slot %d has no entry row for a v1 portable export", slot)
		}
		active, _ := t.SlotActive(slot)
		return SlotEntry{Slot: slot, KeyHash: keyHash, Value: value, Active: active}, nil
	}
}

// slotRow is a slot's payload as an exporter reads it: from the resident
// window, else from cold (the persisted entry log); ok is false for a
// hash-only slot and for a dead slot whose row was reclaimed.
func (t *Tree) slotRow(slot uint64, cold ColdReader) (keyHash Hash, value []byte, ok bool) {
	if slot >= t.entriesBase {
		i := slot - t.entriesBase
		if i < uint64(len(t.entries)) {
			e := t.entries[i]
			// markPruned zeroes a pruned twig's rows: a zero row is no row.
			if e.hashOnly || (e.keyHash == Hash{} && e.value == nil && !e.active) {
				return Hash{}, nil, false
			}
			return e.keyHash, e.value, true
		}
		return Hash{}, nil, false
	}
	if cold == nil {
		return Hash{}, nil, false
	}
	return cold.ColdEntry(slot)
}

// WritePortableSnapshotTombstone streams the tombstone layout of t, whose
// root must equal metadata.Root. A slot with its row is written as kind 0/1;
// a dead slot without one (hash-only, or a reclaimed row) as a tombstone
// carrying its frozen leaf. A full, wholly dead twig whose slots all lack
// rows is written as one kind-3 record when wholeDeadTwigs is set, and must
// be when the node keeps no leaves for it (a hollow twig). g supplies cold
// rows and leaf blobs and may be nil for an in-memory tree. The records are
// produced twice -- once to count the tombstones the header carries.
func WritePortableSnapshotTombstone(w io.Writer, metadata PortableSnapshotMetadata, t *Tree, g Getter, wholeDeadTwigs bool) (int64, error) {
	if t == nil {
		return 0, errors.New("qmdb: nil tree for the tombstone portable snapshot")
	}
	if t.Root() != metadata.Root {
		return 0, fmt.Errorf("qmdb: tree root %x is not the snapshot root %x", t.Root(), metadata.Root)
	}
	if t.nextSlot != metadata.NextSlot {
		return 0, fmt.Errorf("qmdb: tree next slot %d is not the snapshot next slot %d", t.nextSlot, metadata.NextSlot)
	}
	counter := &countingWriter{w: io.Discard}
	tombstones, err := t.writeTombstoneRecords(counter, metadata, g, wholeDeadTwigs)
	if err != nil {
		return 0, err
	}
	hasher := blake3.New(32, nil)
	payload := &countingWriter{w: io.MultiWriter(w, hasher)}
	header := make([]byte, 0, portableTombstoneHeaderSize)
	header = append(header, portableSnapshotMagicTombstone[:]...)
	header = binary.LittleEndian.AppendUint64(header, metadata.ChainID)
	header = append(header, metadata.GenesisHash[:]...)
	header = binary.LittleEndian.AppendUint64(header, metadata.BlockNumber)
	header = append(header, metadata.BlockHash[:]...)
	header = append(header, metadata.Root[:]...)
	header = binary.LittleEndian.AppendUint64(header, metadata.NextSlot)
	header = binary.LittleEndian.AppendUint64(header, metadata.NextSlot)
	header = binary.LittleEndian.AppendUint64(header, tombstones)
	if _, err := payload.Write(header); err != nil {
		return payload.n, err
	}
	if _, err := t.writeTombstoneRecords(payload, metadata, g, wholeDeadTwigs); err != nil {
		return payload.n, err
	}
	n, err := w.Write(hasher.Sum(nil))
	return payload.n + int64(n), err
}

func (t *Tree) writeTombstoneRecords(w io.Writer, metadata PortableSnapshotMetadata, g Getter, wholeDeadTwigs bool) (uint64, error) {
	var leafStore LeafStore = t.leafStore
	if leafStore == nil && g != nil {
		leafStore = LeafStoreFromGetter(g)
	}
	cold := t.cold
	if cold == nil && g != nil {
		cold = ColdReaderFromGetter(g)
	}
	scratch := new([2 * TwigSize]Hash)
	var tombstones uint64
	var fixed [8 + 1 + 32 + 4]byte
	for id := 0; id < len(t.twigs); id++ {
		tw := t.twigs[id]
		if tw == nil {
			return tombstones, fmt.Errorf("qmdb: twig %d is not loaded", id)
		}
		base := uint64(id) * TwigSize
		appended := metadata.NextSlot - base
		if appended > TwigSize {
			appended = TwigSize
		}
		var leaves *[2 * TwigSize]Hash
		switch {
		case tw.nodes != nil:
			leaves = tw.nodes
		case leafStore != nil:
			clear(scratch[:])
			if li, ok := leafStore.(LeafStoreInto); ok && li.LeavesInto(id, scratch) {
				leaves = scratch
			} else if blob, ok := leafStore.Leaves(id); ok && len(blob) == TwigSize*32 {
				for i := 0; i < TwigSize; i++ {
					copy(scratch[TwigSize+i][:], blob[i*32:(i+1)*32])
				}
				leaves = scratch
			}
			if leaves != nil {
				for start := TwigSize - 1; start >= 1; start-- {
					scratch[start] = hashNode(scratch[start*2], scratch[start*2+1])
				}
				if scratch[1] != tw.leafRoot {
					return tombstones, fmt.Errorf("qmdb: twig %d leaf blob does not reproduce its frozen leaf root", id)
				}
			}
		}
		full := appended == TwigSize && tw.live == 0
		if full && (leaves == nil || wholeDeadTwigs) {
			rowless := true
			for s := base; s < base+TwigSize && rowless; s++ {
				_, _, has := t.slotRow(s, cold)
				rowless = !has
			}
			if rowless || leaves == nil {
				binary.LittleEndian.PutUint64(fixed[:8], base)
				fixed[8] = PortableKindDeadTwig
				binary.LittleEndian.PutUint16(fixed[9:11], TwigSize)
				leafRoot := tw.leafRoot
				if tw.nodes != nil && !tw.dirty {
					leafRoot = tw.nodes[1]
				}
				copy(fixed[11:43], leafRoot[:])
				if _, err := w.Write(fixed[:43]); err != nil {
					return tombstones, err
				}
				tombstones++
				continue
			}
		}
		for local := uint64(0); local < appended; local++ {
			slot := base + local
			binary.LittleEndian.PutUint64(fixed[:8], slot)
			live := tw.bit(local)
			keyHash, value, has := t.slotRow(slot, cold)
			if has {
				if len(value) > maxPortableValueSize {
					return tombstones, fmt.Errorf("qmdb: portable slot %d value is too large: %d", slot, len(value))
				}
				fixed[8] = PortableKindDead
				if live {
					fixed[8] = PortableKindLive
				}
				copy(fixed[9:41], keyHash[:])
				binary.LittleEndian.PutUint32(fixed[41:45], uint32(len(value)))
				if _, err := w.Write(fixed[:45]); err != nil {
					return tombstones, err
				}
				if _, err := w.Write(value); err != nil {
					return tombstones, err
				}
				continue
			}
			if live {
				return tombstones, fmt.Errorf("qmdb: live slot %d has no entry row", slot)
			}
			if leaves == nil {
				return tombstones, fmt.Errorf("qmdb: dead slot %d has neither a row nor a leaf hash", slot)
			}
			fixed[8] = PortableKindTombstone
			copy(fixed[9:41], leaves[TwigSize+local][:])
			if _, err := w.Write(fixed[:41]); err != nil {
				return tombstones, err
			}
			tombstones++
		}
	}
	return tombstones, nil
}
