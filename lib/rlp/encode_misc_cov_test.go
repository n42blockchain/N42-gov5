package rlp

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	libcommon "github.com/n42blockchain/N42/lib/common"
)

func TestWriteHelper(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, []byte("hello")); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if buf.String() != "hello" {
		t.Fatalf("Write wrote %q, want %q", buf.String(), "hello")
	}

	// Write through an *encbuf should also succeed (outer.Write branch).
	eb := &encbuf{}
	eb.reset()
	if err := Write(eb, []byte("x")); err != nil {
		t.Fatalf("Write via encbuf error: %v", err)
	}
}

func TestIntLenExcludingHead(t *testing.T) {
	if got := IntLenExcludingHead(0); got != 0 {
		t.Errorf("IntLenExcludingHead(0) = %d, want 0", got)
	}
	if got := IntLenExcludingHead(0x7f); got != 0 {
		t.Errorf("IntLenExcludingHead(0x7f) = %d, want 0", got)
	}
	if got := IntLenExcludingHead(0x80); got != 1 {
		t.Errorf("IntLenExcludingHead(0x80) = %d, want 1", got)
	}
	if got := IntLenExcludingHead(1 << 32); got != 5 {
		t.Errorf("IntLenExcludingHead(1<<32) = %d, want 5", got)
	}
}

func TestBigIntLenExcludingHead(t *testing.T) {
	if got := BigIntLenExcludingHead(big.NewInt(5)); got != 0 {
		t.Errorf("BigIntLenExcludingHead(5) = %d, want 0", got)
	}
	if got := BigIntLenExcludingHead(big.NewInt(300)); got != 2 {
		t.Errorf("BigIntLenExcludingHead(300) = %d, want 2", got)
	}
}

func TestUint256LenExcludingHead(t *testing.T) {
	if got := Uint256LenExcludingHead(uint256.NewInt(5)); got != 0 {
		t.Errorf("Uint256LenExcludingHead(5) = %d, want 0", got)
	}
	big := uint256.NewInt(0).Lsh(uint256.NewInt(1), 200)
	if got := Uint256LenExcludingHead(big); got == 0 {
		t.Errorf("Uint256LenExcludingHead(big) should be non-zero")
	}
}

func TestEncodeInt(t *testing.T) {
	buf := make([]byte, 9)
	cases := []uint64{1, 0x7f, 0x80, 1 << 16, 1 << 40, ^uint64(0)}
	for _, v := range cases {
		var out bytes.Buffer
		if err := EncodeInt(v, &out, buf); err != nil {
			t.Fatalf("EncodeInt(%d) error: %v", v, err)
		}
		if out.Len() == 0 {
			t.Fatalf("EncodeInt(%d) wrote nothing", v)
		}
	}
}

func TestEncodeBigInt(t *testing.T) {
	buf := make([]byte, 40)
	cases := []*big.Int{nil, big.NewInt(0), big.NewInt(5), big.NewInt(300), new(big.Int).Lsh(big.NewInt(1), 200)}
	for _, v := range cases {
		var out bytes.Buffer
		if err := EncodeBigInt(v, &out, buf); err != nil {
			t.Fatalf("EncodeBigInt(%v) error: %v", v, err)
		}
		if out.Len() == 0 {
			t.Fatalf("EncodeBigInt(%v) wrote nothing", v)
		}
	}
}

func TestEncodeString(t *testing.T) {
	buf := make([]byte, 40)
	cases := [][]byte{
		{},
		{0x01},
		{0x80},
		[]byte("ab"),
		make([]byte, 100),
	}
	for _, s := range cases {
		var out bytes.Buffer
		if err := EncodeString(s, &out, buf); err != nil {
			t.Fatalf("EncodeString(len=%d) error: %v", len(s), err)
		}
	}
}

func TestEncodeStringSizePrefix(t *testing.T) {
	buf := make([]byte, 16)
	var out bytes.Buffer
	if err := EncodeStringSizePrefix(10, &out, buf); err != nil {
		t.Fatalf("EncodeStringSizePrefix(10) error: %v", err)
	}
	out.Reset()
	if err := EncodeStringSizePrefix(1000, &out, buf); err != nil {
		t.Fatalf("EncodeStringSizePrefix(1000) error: %v", err)
	}
}

func TestEncodeOptionalAddress(t *testing.T) {
	buf := make([]byte, 24)
	var out bytes.Buffer
	if err := EncodeOptionalAddress(nil, &out, buf); err != nil {
		t.Fatalf("EncodeOptionalAddress(nil) error: %v", err)
	}
	if out.Len() != 1 || out.Bytes()[0] != 128 {
		t.Fatalf("EncodeOptionalAddress(nil) = %v, want [128]", out.Bytes())
	}

	out.Reset()
	addr := libcommon.Address{1, 2, 3}
	if err := EncodeOptionalAddress(&addr, &out, buf); err != nil {
		t.Fatalf("EncodeOptionalAddress(addr) error: %v", err)
	}
	if out.Len() != 21 {
		t.Fatalf("EncodeOptionalAddress(addr) len = %d, want 21", out.Len())
	}
}
