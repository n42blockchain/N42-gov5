package rawdb

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
)

type appliedMarkerGetter struct {
	kv.Getter
	value []byte
	err   error
}

func (g appliedMarkerGetter) GetOne(string, []byte) ([]byte, error) { return g.value, g.err }

func TestReadQMDBAppliedExactEncoding(t *testing.T) {
	for n := 0; n <= 48; n++ {
		value := make([]byte, n)
		if n == 40 {
			binary.BigEndian.PutUint64(value, 123)
			value[39] = 7
		}
		number, hash, ok, err := ReadQMDBApplied(appliedMarkerGetter{value: value})
		switch n {
		case 0:
			if ok || err != nil {
				t.Fatalf("absent marker: ok=%v err=%v", ok, err)
			}
		case 40:
			if !ok || err != nil || number != 123 || hash[31] != 7 {
				t.Fatalf("valid marker: number=%d hash=%x ok=%v err=%v", number, hash, ok, err)
			}
		default:
			if ok || err == nil {
				t.Fatalf("malformed marker length %d accepted or treated as absent", n)
			}
		}
	}
	want := errors.New("marker read failed")
	if _, _, ok, err := ReadQMDBApplied(appliedMarkerGetter{value: make([]byte, 40), err: want}); ok || !errors.Is(err, want) {
		t.Fatalf("read failure lost: ok=%v err=%v", ok, err)
	}
}
