// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lukechampine.com/blake3"
)

// The fixture set n42-rs writes (crates/n42/twig-core/testdata/portable_v2,
// copied byte for byte): one tree with a whole-dead twig (twig 0, compacted:
// every slot a tombstone), a half-dead compacted twig (twig 1: 1024 live, 1024
// tombstones) and an uncompacted partial twig (twig 2: 50 dead slots with
// their payload). v1.bin is the uncompacted tree's v1 export, v2.bin the
// compacted tree's tombstone export with twig 0 as one kind-3 record, and
// v2-tombstones.bin the same with kind 2 only.
const portableV2Dir = "testdata/portable_v2"

var (
	fixtureChainID = uint64(1143)
	fixtureGenesis = filledHash(0x11)
)

func filledHash(b byte) Hash {
	var h Hash
	for i := range h {
		h[i] = b
	}
	return h
}

func readPortableV2Fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(portableV2Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureRoot(t *testing.T) Hash {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimSpace(string(readPortableV2Fixture(t, "root.hex"))))
	if err != nil || len(raw) != 32 {
		t.Fatalf("root.hex: %v", err)
	}
	return Hash(raw)
}

func TestPortableV2FixturesMatchTheirManifest(t *testing.T) {
	manifest := string(readPortableV2Fixture(t, "SHA256SUMS"))
	lines := strings.Split(strings.TrimSpace(manifest), "\n")
	if len(lines) != 4 {
		t.Fatalf("manifest has %d lines, want 4", len(lines))
	}
	for _, line := range lines {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("manifest line %q", line)
		}
		got := sha256.Sum256(readPortableV2Fixture(t, name))
		if hex.EncodeToString(got[:]) != sum {
			t.Fatalf("%s: sha256 %x, manifest %s", name, got, sum)
		}
	}
}

func TestImportPortableV2FixturesToOneRoot(t *testing.T) {
	root := fixtureRoot(t)
	cases := []struct {
		name       string
		format     PortableFormat
		tombstones uint64
	}{
		{"v1.bin", PortableFormatV1, 0},
		{"v2.bin", PortableFormatTombstone, 1 + 1024},
		{"v2-tombstones.bin", PortableFormatTombstone, 2048 + 1024},
	}
	for _, c := range cases {
		tree, v, err := ImportPortableSnapshot(readPortableV2Fixture(t, c.name), fixtureChainID, fixtureGenesis)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if tree.Root() != root || v.Metadata.Root != root {
			t.Fatalf("%s: root %x, want %x", c.name, tree.Root(), root)
		}
		if v.Format != c.format || v.Tombstones != c.tombstones || v.LiveCount != 2348 || v.Metadata.NextSlot != 5470 {
			t.Fatalf("%s: %+v", c.name, v)
		}
		if tree.LiveCount() != 2348 {
			t.Fatalf("%s: live %d", c.name, tree.LiveCount())
		}
		if tree.HasHashOnlySlots() != (c.tombstones > 0) {
			t.Fatalf("%s: hash-only slots %v", c.name, tree.HasHashOnlySlots())
		}
	}
	// The leaf-form layout and any other magic are not read.
	leafForm := append([]byte{}, readPortableV2Fixture(t, "v2.bin")...)
	leafForm[7] = 2
	if _, _, err := ImportPortableSnapshot(leafForm, fixtureChainID, fixtureGenesis); err == nil {
		t.Fatal("imported a leaf-form magic")
	}
	if _, _, err := ImportPortableSnapshot(readPortableV2Fixture(t, "v2.bin"), 1, fixtureGenesis); err == nil {
		t.Fatal("imported for another chain")
	}
	// The v1-only decoder stops at the magic.
	if _, err := UnmarshalPortableSnapshot(readPortableV2Fixture(t, "v2-tombstones.bin")); err == nil {
		t.Fatal("v1 decoder accepted the tombstone layout")
	}
}

func exportV1(tree *Tree) ([]byte, error) {
	var buf bytes.Buffer
	_, err := WritePortableSnapshot(&buf, tree.metadataForTest(), tree.PortableSlotSource())
	return buf.Bytes(), err
}

func (t *Tree) metadataForTest() PortableSnapshotMetadata {
	return PortableSnapshotMetadata{
		ChainID:     fixtureChainID,
		GenesisHash: fixtureGenesis,
		BlockNumber: 42,
		BlockHash:   filledHash(0x22),
		Root:        t.Root(),
		NextSlot:    t.NextSlot(),
	}
}

func exportTombstone(t *testing.T, tree *Tree, whole bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	n, err := WritePortableSnapshotTombstone(&buf, tree.metadataForTest(), tree, nil, whole)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(buf.Len()) {
		t.Fatalf("wrote %d, buffer %d", n, buf.Len())
	}
	return buf.Bytes()
}

func TestPortableV2ExportsReproduceTheFixturesAndV1IsRefusedOnTombstones(t *testing.T) {
	v1 := readPortableV2Fixture(t, "v1.bin")
	v2 := readPortableV2Fixture(t, "v2.bin")
	v2Tombstones := readPortableV2Fixture(t, "v2-tombstones.bin")

	// From the v1 import: v1 again, byte for byte.
	plain, _, err := ImportPortableSnapshot(v1, fixtureChainID, fixtureGenesis)
	if err != nil {
		t.Fatal(err)
	}
	again, err := exportV1(plain)
	if err != nil {
		t.Fatalf("v1 export of a tree without tombstones: %v", err)
	}
	if !bytes.Equal(again, v1) {
		t.Fatal("v1 re-export differs from v1.bin")
	}
	// Its tombstone export carries no tombstones and imports to the root.
	plainV2 := exportTombstone(t, plain, true)
	if _, v, err := ImportPortableSnapshot(plainV2, fixtureChainID, fixtureGenesis); err != nil || v.Tombstones != 0 {
		t.Fatalf("tombstone export of the uncompacted tree: %v %+v", err, v)
	}
	if len(plainV2) != len(v1)+8 {
		t.Fatalf("tombstone export of the uncompacted tree is %d bytes, v1 %d", len(plainV2), len(v1))
	}

	for _, c := range []struct {
		name   string
		source []byte
	}{{"v2.bin", v2}, {"v2-tombstones.bin", v2Tombstones}} {
		tree, _, err := ImportPortableSnapshot(c.source, fixtureChainID, fixtureGenesis)
		if err != nil {
			t.Fatal(err)
		}
		// v1 needs every slot's key and value: refused at the first tombstone.
		_, err = exportV1(tree)
		var hashOnly *HashOnlySlotError
		if !errors.Is(err, ErrHashOnlySlot) || !errors.As(err, &hashOnly) || hashOnly.Slot != 0 {
			t.Fatalf("%s: v1 export of a tombstoned tree: %v", c.name, err)
		}
		// The tombstone exports reproduce the fixtures. A kind-3 twig has no
		// leaves left, so it is written as kind 3 either way.
		if got := exportTombstone(t, tree, true); !bytes.Equal(got, v2) {
			t.Fatalf("%s: whole-dead-twig export differs from v2.bin", c.name)
		}
		want := v2Tombstones
		if c.name == "v2.bin" {
			want = v2
		}
		if got := exportTombstone(t, tree, false); !bytes.Equal(got, want) {
			t.Fatalf("%s: tombstone-only export differs", c.name)
		}
	}
}

func TestATombstonedTreeRefusesRevivalAndFlushButExtends(t *testing.T) {
	root := fixtureRoot(t)
	for _, name := range []string{"v2.bin", "v2-tombstones.bin"} {
		tree, _, err := ImportPortableSnapshot(readPortableV2Fixture(t, name), fixtureChainID, fixtureGenesis)
		if err != nil {
			t.Fatal(err)
		}
		for _, slot := range []uint64{5, 2048} {
			undo := &BlockUndo{PrevNextSlot: tree.NextSlot(), Entries: []UndoEntry{{Slot: slot, KeyHash: filledHash(9), Value: []byte{1}}}}
			if err := tree.ApplyUndo(undo); !errors.Is(err, ErrHashOnlySlot) {
				t.Fatalf("%s: revival of tombstoned slot %d: %v", name, slot, err)
			}
			if tree.Root() != root {
				t.Fatalf("%s: a refused undo changed the root", name)
			}
		}
		if _, _, err := tree.FlushTo(newMapStore(), 0); !errors.Is(err, ErrHashOnlySlot) {
			t.Fatalf("%s: flush without a leaf store: %v", name, err)
		}
		// The imported state is a state: the same block on the v1 import
		// lands on the same root.
		plain, _, err := ImportPortableSnapshot(readPortableV2Fixture(t, "v1.bin"), fixtureChainID, fixtureGenesis)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range []*Tree{tree, plain} {
			tr.Set(filledHash(0x77), []byte{1, 2, 3})
			tr.Delete(fixtureKey(2049))
		}
		if tree.Root() != plain.Root() || tree.Root() == root {
			t.Fatalf("%s: extended roots %x / %x", name, tree.Root(), plain.Root())
		}
		if _, ok := tree.Get(fixtureKey(2049)); ok {
			t.Fatalf("%s: deleted key still readable", name)
		}
	}
}

// fixtureKey is n42-rs's fixture key: n big-endian, then 0xab.
func fixtureKey(n uint64) Hash {
	h := filledHash(0xab)
	binary.BigEndian.PutUint64(h[:8], n)
	return h
}

func reseal(data []byte) []byte {
	out := append([]byte{}, data[:len(data)-portableSnapshotDigestSize]...)
	digest := blake3.Sum256(out)
	return append(out, digest[:]...)
}

func findRecord(t *testing.T, data []byte, slot uint64, kind byte) int {
	t.Helper()
	var needle [9]byte
	binary.LittleEndian.PutUint64(needle[:8], slot)
	needle[8] = kind
	at := bytes.Index(data, needle[:])
	if at < 0 {
		t.Fatalf("no record of kind %d at slot %d", kind, slot)
	}
	return at
}

func TestPortableTombstoneRejections(t *testing.T) {
	// A wrong tombstone fails the root check (resealed, so the digest holds).
	bad := append([]byte{}, readPortableV2Fixture(t, "v2-tombstones.bin")...)
	bad[findRecord(t, bad, 2048, PortableKindTombstone)+9] ^= 1
	if _, _, err := ImportPortableSnapshot(reseal(bad), fixtureChainID, fixtureGenesis); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("wrong tombstone: %v", err)
	}
	// Unsealed, the digest catches it first.
	if _, _, err := ImportPortableSnapshot(bad, fixtureChainID, fixtureGenesis); err == nil || !strings.Contains(err.Error(), "content hash") {
		t.Fatalf("unsealed flip: %v", err)
	}
	// A wrong whole-twig leaf root, too.
	bad = append([]byte{}, readPortableV2Fixture(t, "v2.bin")...)
	bad[findRecord(t, bad, 0, PortableKindDeadTwig)+11] ^= 1
	if _, _, err := ImportPortableSnapshot(reseal(bad), fixtureChainID, fixtureGenesis); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("wrong leaf root: %v", err)
	}
	// The header's tombstone count must match the records.
	bad = append([]byte{}, readPortableV2Fixture(t, "v2-tombstones.bin")...)
	bad[portableSnapshotHeaderSize] ^= 1
	if _, _, err := ImportPortableSnapshot(reseal(bad), fixtureChainID, fixtureGenesis); err == nil || !strings.Contains(err.Error(), "tombstone records") {
		t.Fatalf("tombstone count lie: %v", err)
	}

	// Hand-built streams.
	header := func(entries, tombstones uint64) []byte {
		h := append([]byte{}, portableSnapshotMagicTombstone[:]...)
		h = binary.LittleEndian.AppendUint64(h, fixtureChainID)
		h = append(h, fixtureGenesis[:]...)
		h = binary.LittleEndian.AppendUint64(h, 7)
		g := filledHash(0x22)
		h = append(h, g[:]...)
		h = append(h, make([]byte, 32)...)
		h = binary.LittleEndian.AppendUint64(h, entries)
		h = binary.LittleEndian.AppendUint64(h, entries)
		return binary.LittleEndian.AppendUint64(h, tombstones)
	}
	live := func(slot uint64) []byte {
		r := binary.LittleEndian.AppendUint64(nil, slot)
		r = append(r, PortableKindLive)
		k := filledHash(byte(slot))
		r = append(r, k[:]...)
		r = binary.LittleEndian.AppendUint32(r, 1)
		return append(r, 7)
	}
	deadTwig := func(slot uint64, count uint16) []byte {
		r := binary.LittleEndian.AppendUint64(nil, slot)
		r = append(r, PortableKindDeadTwig)
		r = binary.LittleEndian.AppendUint16(r, count)
		return append(r, make([]byte, 32)...)
	}
	seal := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		digest := blake3.Sum256(out)
		return append(out, digest[:]...)
	}
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"kind 3 not at a twig start", seal(header(TwigSize+1, 1), live(0), deadTwig(1, TwigSize)), "does not cover exactly one twig"},
		{"kind 3 over part of a twig", seal(header(2*TwigSize, 1), deadTwig(0, 1024)), "does not cover exactly one twig"},
		{"kind 3 past the slot log", seal(header(100, 1), deadTwig(0, TwigSize)), "does not cover exactly one twig"},
		{"next record not at slot+count", seal(header(TwigSize+1, 1), deadTwig(0, TwigSize), live(TwigSize-1)), "not contiguous"},
		{"kind 4", seal(header(1, 0), append(live(0)[:8:8], append([]byte{4}, live(0)[9:]...)...)), "invalid record kind"},
		{"well formed, zero root", seal(header(TwigSize+1, 1), deadTwig(0, TwigSize), live(TwigSize)), "does not match"},
	} {
		if _, _, err := ImportPortableSnapshot(c.data, fixtureChainID, fixtureGenesis); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v (want %q)", c.name, err, c.want)
		}
	}
}

// reclaimedRows is a tombstone stream as a node that reclaimed its dead rows
// writes it: every kind-0 record (a dead slot with its payload) becomes a
// kind-2 tombstone carrying the leaf hash of that payload. A leaf-store
// FlushTo drops dead slots' rows by design, so a reloaded tree has no other
// form to give.
func reclaimedRows(t *testing.T, data []byte) []byte {
	t.Helper()
	body := data[:len(data)-portableSnapshotDigestSize]
	out := append([]byte{}, body[:portableTombstoneHeaderSize]...)
	added := uint64(0)
	for at := portableTombstoneHeaderSize; at < len(body); {
		slot, kind := body[at:at+8], body[at+8]
		switch kind {
		case PortableKindDead, PortableKindLive:
			var keyHash Hash
			copy(keyHash[:], body[at+9:at+41])
			n := int(binary.LittleEndian.Uint32(body[at+41 : at+45]))
			value := body[at+45 : at+45+n]
			if kind == PortableKindDead {
				leaf := hashLeaf(keyHash, value)
				out = append(out, slot...)
				out = append(out, PortableKindTombstone)
				out = append(out, leaf[:]...)
				added++
			} else {
				out = append(out, body[at:at+45+n]...)
			}
			at += 45 + n
		case PortableKindTombstone:
			out = append(out, body[at:at+41]...)
			at += 41
		case PortableKindDeadTwig:
			out = append(out, body[at:at+43]...)
			at += 43
		default:
			t.Fatalf("record kind %d at %d", kind, at)
		}
	}
	count := out[portableTombstoneHeaderSize-8 : portableTombstoneHeaderSize]
	binary.LittleEndian.PutUint64(count, binary.LittleEndian.Uint64(count)+added)
	return reseal(append(out, make([]byte, portableSnapshotDigestSize)...))
}

func exportTombstoneFrom(t *testing.T, tree *Tree, g Getter, whole bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := WritePortableSnapshotTombstone(&buf, tree.metadataForTest(), tree, g, whole); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Each fixture import is flushed through a leaf store and reloaded: the same
// root, the live keys served from the persisted log, v1 refused at the first
// slot left without a row, the tombstone exports equal to the fixtures (dead
// payload rows reclaimed), revival of a tombstoned slot refused, and the
// reloaded tree extends to the same root as an in-memory one.
func TestImportedTreesPersistThroughALeafStoreAndReload(t *testing.T) {
	root := fixtureRoot(t)
	wantWhole := reclaimedRows(t, readPortableV2Fixture(t, "v2.bin"))
	wantKind2 := reclaimedRows(t, readPortableV2Fixture(t, "v2-tombstones.bin"))
	if bytes.Equal(wantWhole, readPortableV2Fixture(t, "v2.bin")) {
		t.Fatal("the fixture has no dead payload to reclaim")
	}
	for _, name := range []string{"v1.bin", "v2.bin", "v2-tombstones.bin"} {
		tree, _, err := ImportPortableSnapshot(readPortableV2Fixture(t, name), fixtureChainID, fixtureGenesis)
		if err != nil {
			t.Fatal(err)
		}
		store := newMapStore()
		tree.SetLeafStore(LeafStoreFromGetter(store))
		if _, _, err := tree.FlushTo(store, 0); err != nil {
			t.Fatalf("%s: flush with a leaf store: %v", name, err)
		}
		tree.CommitFlush()
		for round := 0; round < 2; round++ {
			reloaded := New()
			if err := reloaded.LoadFrom(store); err != nil {
				t.Fatalf("%s round %d: reload: %v", name, round, err)
			}
			if reloaded.Root() != root || reloaded.NextSlot() != 5470 || reloaded.LiveCount() != 2348 {
				t.Fatalf("%s round %d: root %x next %d live %d", name, round, reloaded.Root(), reloaded.NextSlot(), reloaded.LiveCount())
			}
			for _, n := range []uint64{2049, 4300} {
				if v, ok := reloaded.Get(fixtureKey(n)); !ok || !bytes.Equal(v, []byte{1, byte(n % 251)}) {
					t.Fatalf("%s: key %d reads %x %v", name, n, v, ok)
				}
			}
			if _, ok := reloaded.Get(fixtureKey(5)); ok {
				t.Fatalf("%s: a key of the dead twig reads", name)
			}
			_, err = exportV1(reloaded)
			var hashOnly *HashOnlySlotError
			if !errors.As(err, &hashOnly) || hashOnly.Slot != 0 {
				t.Fatalf("%s: v1 export after reload: %v", name, err)
			}
			if got := exportTombstoneFrom(t, reloaded, store, true); !bytes.Equal(got, wantWhole) {
				t.Fatalf("%s round %d: whole-dead-twig export after reload differs", name, round)
			}
			want := wantKind2
			if name == "v2.bin" {
				want = wantWhole // twig 0 never had leaves here: kind 3 either way
			}
			if got := exportTombstoneFrom(t, reloaded, store, false); !bytes.Equal(got, want) {
				t.Fatalf("%s round %d: tombstone-only export after reload differs", name, round)
			}
			undo := &BlockUndo{PrevNextSlot: reloaded.NextSlot(), Entries: []UndoEntry{{Slot: 5, KeyHash: fixtureKey(5), Value: []byte{0, 5}}}}
			if name == "v2.bin" {
				if err := reloaded.ApplyUndo(undo); !errors.Is(err, ErrHashOnlySlot) {
					t.Fatalf("%s: revival in the hollow twig after reload: %v", name, err)
				}
				if reloaded.Root() != root {
					t.Fatalf("%s: a refused undo changed the root", name)
				}
			}
			// The reloaded tree flushes again (round 1 reloads that flush).
			reloaded.SetLeafStore(LeafStoreFromGetter(store))
			if _, _, err := reloaded.FlushTo(store, reloaded.NextSlot()); err != nil {
				t.Fatalf("%s: flush after reload: %v", name, err)
			}
			reloaded.CommitFlush()
		}
		extended := New()
		if err := extended.LoadFrom(store); err != nil {
			t.Fatal(err)
		}
		plain, _, err := ImportPortableSnapshot(readPortableV2Fixture(t, "v1.bin"), fixtureChainID, fixtureGenesis)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range []*Tree{extended, plain} {
			tr.Set(filledHash(0x77), []byte{1, 2, 3})
			tr.Delete(fixtureKey(2049))
		}
		if extended.Root() != plain.Root() || extended.Root() == root {
			t.Fatalf("%s: extended roots %x / %x", name, extended.Root(), plain.Root())
		}
	}
}
