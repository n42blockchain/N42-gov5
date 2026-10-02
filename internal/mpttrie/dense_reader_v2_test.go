package mpttrie

import (
	"testing"

	"github.com/n42blockchain/N42/lib/trie"
)

const denseV2TestTable = "AccountsDenseV2"

func writeDenseV2TestDB(t *testing.T, key string, val []byte) string {
	t.Helper()
	dir := writeDenseV2TestDBMulti(t, map[string][]byte{key: val})
	return dir
}

func writeDenseV2TestDBMulti(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	// Reuse the generic dense-table writer, just against the V2 table name.
	return writeDenseTestDBNamed(t, denseV2TestTable, entries)
}

func TestDenseReader_GetV2_WithLeafMarkerExpansion(t *testing.T) {
	// One account whose keccak(addr) we control.
	var addr [20]byte
	addr[0] = 0x7
	keccakAddr := keccak256Bytes(addr[:])
	value := []byte{0xde, 0xad, 0xbe, 0xef}

	mock := &mockSingleLeafLookup{
		keys:   [][32]byte{keccakAddr},
		values: [][]byte{value},
	}

	// Branch at the empty path, single child at nibble = first nibble
	// of keccakAddr, encoded as a V2 leaf marker (treeMask bit unset).
	digit := int(keccakAddr[0] >> 4)
	const stride = 33
	stateMask := uint16(1 << digit)
	treeMask := uint16(0) // no tree bit => leaf => gets marker-encoded
	slotData := make([]byte, stride)
	slotData[0] = 0xa0 // placeholder; marshal replaces leaf slots with marker
	enc := trie.MarshalTrieNodeDenseV2(stateMask, treeMask, 0, slotData, nil)

	dir := writeDenseV2TestDB(t, "", enc)
	r, err := OpenDense(dir, denseV2TestTable)
	if err != nil {
		t.Fatalf("OpenDense: %v", err)
	}
	defer r.Close()

	branch, ok, err := r.GetV2(nil, false, mock)
	if err != nil {
		t.Fatalf("GetV2: %v", err)
	}
	if !ok {
		t.Fatal("expected GetV2 to find the entry")
	}
	if branch.StateMask != stateMask {
		t.Fatalf("StateMask = %x, want %x", branch.StateMask, stateMask)
	}
	slot := branch.Slots[digit]
	if slot == nil || len(slot) != 33 || slot[0] != 0xa0 {
		t.Fatalf("expected expanded 33-byte hash slot, got %v", slot)
	}
}

func TestDenseReader_GetV2_Miss(t *testing.T) {
	dir := writeDenseV2TestDB(t, "", nil)
	// Remove the "" key entirely by writing no entries at all instead.
	dir = writeDenseV2TestDBMulti(t, nil)

	r, err := OpenDense(dir, denseV2TestTable)
	if err != nil {
		t.Fatalf("OpenDense: %v", err)
	}
	defer r.Close()

	_, ok, err := r.GetV2([]byte{1, 2}, false, nil)
	if err != nil {
		t.Fatalf("GetV2: %v", err)
	}
	if ok {
		t.Fatal("expected miss for unknown path")
	}
}

func TestDenseReader_GetV2_MarkerWithoutBase(t *testing.T) {
	const stride = 33
	stateMask := uint16(1 << 2)
	treeMask := uint16(0)
	slotData := make([]byte, stride)
	slotData[0] = 0xa0
	enc := trie.MarshalTrieNodeDenseV2(stateMask, treeMask, 0, slotData, nil)

	dir := writeDenseV2TestDB(t, "", enc)
	r, err := OpenDense(dir, denseV2TestTable)
	if err != nil {
		t.Fatalf("OpenDense: %v", err)
	}
	defer r.Close()

	// base == nil must surface an error rather than panic.
	_, _, err = r.GetV2(nil, false, nil)
	if err == nil {
		t.Fatal("expected error when base SingleLeafLookup is nil for a marker slot")
	}
}

func TestEncodeUintBE(t *testing.T) {
	cases := []struct {
		in   uint64
		want int
	}{
		{0, 1},
		{0xff, 1},
		{0x100, 2},
		{0xffff, 2},
		{0x10000, 3},
		{0xffffff, 3},
		{0x1000000, 4},
	}
	for _, c := range cases {
		got := encodeUintBE(c.in)
		if len(got) != c.want {
			t.Errorf("encodeUintBE(%#x) len = %d, want %d", c.in, len(got), c.want)
		}
	}
}
