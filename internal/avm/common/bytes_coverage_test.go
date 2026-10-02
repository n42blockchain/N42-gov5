package common

import (
	"bytes"
	"testing"
)

func TestBytes2Hex(t *testing.T) {
	if got := Bytes2Hex([]byte{0xDE, 0xAD}); got != "dead" {
		t.Errorf("Bytes2Hex() = %q, want %q", got, "dead")
	}
}

func TestHex2BytesFixed(t *testing.T) {
	if got := Hex2BytesFixed("dead", 2); !bytes.Equal(got, []byte{0xDE, 0xAD}) {
		t.Errorf("Hex2BytesFixed exact length = %x", got)
	}
	if got := Hex2BytesFixed("deadbeef", 2); !bytes.Equal(got, []byte{0xBE, 0xEF}) {
		t.Errorf("Hex2BytesFixed truncate = %x, want last 2 bytes", got)
	}
	if got := Hex2BytesFixed("dead", 4); !bytes.Equal(got, []byte{0x00, 0x00, 0xDE, 0xAD}) {
		t.Errorf("Hex2BytesFixed pad = %x", got)
	}
}

func TestTrimLeftZeroes(t *testing.T) {
	if got := TrimLeftZeroes([]byte{0, 0, 1, 2}); !bytes.Equal(got, []byte{1, 2}) {
		t.Errorf("TrimLeftZeroes = %x", got)
	}
	if got := TrimLeftZeroes([]byte{0, 0, 0}); len(got) != 0 {
		t.Errorf("TrimLeftZeroes all-zero = %x, want empty", got)
	}
}
