package block

import (
	"encoding/binary"
	"testing"
)

// maxLenPrefix is the uvarint 2^64-1: int(n) is -1, which passed the
// "len(p) < n" guard of every take helper and panicked on the slice.
var maxLenPrefix = binary.AppendUvarint(nil, ^uint64(0))

func TestCompactHeaderRejectsNegativeLength(t *testing.T) {
	data := []byte{compactHeaderMarker, compactHeaderVersion}
	flags := uint32(hcfExtra)
	data = append(data, byte(flags), byte(flags>>8), byte(flags>>16))
	data = append(data, make([]byte, 64)...) // parent hash, root
	for i := 0; i < 3; i++ {                 // number, gas limit, time
		data = append(data, 0)
	}
	data = append(data, maxLenPrefix...)
	if err := new(Header).Unmarshal(data); err == nil {
		t.Fatal("compact header with an overflowing extra length was accepted")
	}
}

func TestCompactBodyRejectsOverflowingLength(t *testing.T) {
	for _, n := range []uint64{^uint64(0), 1<<63 - 1} {
		data := append([]byte{compactBodyMarker, compactBodyVersion, 1}, binary.AppendUvarint(nil, n)...)
		data = append(data, 0)
		if err := new(Body).UnmarshalCompact(data); err == nil {
			t.Fatalf("compact body with tx length %d was accepted", n)
		}
	}
}

func TestCompactLogsRejectsOverflowingLength(t *testing.T) {
	for _, n := range []uint64{^uint64(0), 1<<63 - 1} {
		data := []byte{compactLogsMarker, compactLogsVersion, 1}
		data = append(data, make([]byte, 20)...) // address
		data = append(data, 0)                   // topic count
		data = append(data, binary.AppendUvarint(nil, n)...)
		data = append(data, 0)
		if err := new(Logs).unmarshalCompact(data); err == nil {
			t.Fatalf("compact logs with data length %d was accepted", n)
		}
	}
}

func TestCompactReceiptsRejectsOverflowingCount(t *testing.T) {
	data := append([]byte{compactReceiptsMarker, compactReceiptsVersion}, maxLenPrefix...)
	if err := new(Receipts).unmarshalCompact(data); err == nil {
		t.Fatal("compact receipts with an overflowing count were accepted")
	}
}
