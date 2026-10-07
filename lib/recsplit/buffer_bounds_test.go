package recsplit

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unsafe"
)

func TestGolombRiceWriteBoundedBuffers(t *testing.T) {
	for _, words := range [][]uint64{nil, {0x0123456789abcdef}, {1, 2, 3}} {
		g := GolombRice{data: words}
		var out bytes.Buffer
		if err := g.Write(&out); err != nil {
			t.Fatal(err)
		}
		if got := binary.BigEndian.Uint64(out.Bytes()[:8]); got != uint64(len(words)) {
			t.Fatalf("count %d", got)
		}
		raw := unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(words))), len(words)*8)
		if !bytes.Equal(out.Bytes()[8:], raw) {
			t.Fatal("native word encoding changed")
		}
	}
}
