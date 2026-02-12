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

package math

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
)

func TestHexOrDecimal256(t *testing.T) {
	// Test NewHexOrDecimal256
	h := NewHexOrDecimal256(100)
	if h == nil {
		t.Fatal("NewHexOrDecimal256 returned nil")
	}

	// Test MarshalText
	text, err := h.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	if len(text) == 0 {
		t.Error("MarshalText() returned empty")
	}

	// Test UnmarshalText with hex
	var h2 HexOrDecimal256
	err = h2.UnmarshalText([]byte("0x64"))
	if err != nil {
		t.Fatalf("UnmarshalText() hex error = %v", err)
	}
	bigH2 := (*big.Int)(&h2)
	if bigH2.Cmp(big.NewInt(100)) != 0 {
		t.Error("UnmarshalText() hex didn't parse correctly")
	}

	// Test UnmarshalText with decimal
	var h3 HexOrDecimal256
	err = h3.UnmarshalText([]byte("200"))
	if err != nil {
		t.Fatalf("UnmarshalText() decimal error = %v", err)
	}
	bigH3 := (*big.Int)(&h3)
	if bigH3.Cmp(big.NewInt(200)) != 0 {
		t.Error("UnmarshalText() decimal didn't parse correctly")
	}

	// Test UnmarshalText with invalid input
	var h4 HexOrDecimal256
	err = h4.UnmarshalText([]byte("invalid"))
	if err == nil {
		t.Error("UnmarshalText() should fail with invalid input")
	}

	// Test MarshalText with nil
	var nilH *HexOrDecimal256
	text, err = nilH.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() nil error = %v", err)
	}
	if string(text) != "0x0" {
		t.Errorf("MarshalText() nil = %s, want 0x0", text)
	}
}

func TestDecimal256(t *testing.T) {
	// Test NewDecimal256
	d := NewDecimal256(100)
	if d == nil {
		t.Fatal("NewDecimal256 returned nil")
	}

	// Test String
	str := d.String()
	if str != "100" {
		t.Errorf("String() = %s, want 100", str)
	}

	// Test String with nil
	var nilD *Decimal256
	str = nilD.String()
	if str != "0" {
		t.Errorf("String() nil = %s, want 0", str)
	}

	// Test MarshalText
	text, err := d.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	if string(text) != "100" {
		t.Errorf("MarshalText() = %s, want 100", text)
	}

	// Test UnmarshalText
	var d2 Decimal256
	err = d2.UnmarshalText([]byte("200"))
	if err != nil {
		t.Fatalf("UnmarshalText() error = %v", err)
	}

	// Test UnmarshalText with invalid input
	var d3 Decimal256
	err = d3.UnmarshalText([]byte("invalid"))
	if err == nil {
		t.Error("UnmarshalText() should fail with invalid input")
	}
}

func TestParseBig256(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *big.Int
		wantOk  bool
	}{
		{"empty string", "", big.NewInt(0), true},
		{"decimal", "100", big.NewInt(100), true},
		{"hex lowercase", "0x64", big.NewInt(100), true},
		{"hex uppercase", "0X64", big.NewInt(100), true},
		{"large decimal", "115792089237316195423570985008687907853269984665640564039457584007913129639935", MaxBig256, true},
		{"invalid", "xyz", nil, false},
		{"too large", "115792089237316195423570985008687907853269984665640564039457584007913129639936", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseBig256(tt.input)
			if ok != tt.wantOk {
				t.Errorf("ParseBig256() ok = %v, want %v", ok, tt.wantOk)
			}
			if tt.wantOk && got.Cmp(tt.want) != 0 {
				t.Errorf("ParseBig256() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMustParseBig256(t *testing.T) {
	// Test valid input
	v := MustParseBig256("100")
	if v.Cmp(big.NewInt(100)) != 0 {
		t.Error("MustParseBig256() returned wrong value")
	}

	// Test invalid input (should panic)
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustParseBig256() should panic with invalid input")
		}
	}()
	MustParseBig256("invalid")
}

func TestBigPow(t *testing.T) {
	tests := []struct {
		a, b int64
		want *big.Int
	}{
		{2, 0, big.NewInt(1)},
		{2, 1, big.NewInt(2)},
		{2, 10, big.NewInt(1024)},
		{3, 3, big.NewInt(27)},
		{10, 5, big.NewInt(100000)},
	}

	for _, tt := range tests {
		got := BigPow(tt.a, tt.b)
		if got.Cmp(tt.want) != 0 {
			t.Errorf("BigPow(%d, %d) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestBigMax(t *testing.T) {
	tests := []struct {
		x, y, want *big.Int
	}{
		{big.NewInt(1), big.NewInt(2), big.NewInt(2)},
		{big.NewInt(10), big.NewInt(5), big.NewInt(10)},
		{big.NewInt(7), big.NewInt(7), big.NewInt(7)},
	}

	for _, tt := range tests {
		got := BigMax(tt.x, tt.y)
		if got.Cmp(tt.want) != 0 {
			t.Errorf("BigMax(%v, %v) = %v, want %v", tt.x, tt.y, got, tt.want)
		}
	}
}

func TestBigMin(t *testing.T) {
	tests := []struct {
		x, y, want *big.Int
	}{
		{big.NewInt(1), big.NewInt(2), big.NewInt(1)},
		{big.NewInt(10), big.NewInt(5), big.NewInt(5)},
		{big.NewInt(7), big.NewInt(7), big.NewInt(7)},
	}

	for _, tt := range tests {
		got := BigMin(tt.x, tt.y)
		if got.Cmp(tt.want) != 0 {
			t.Errorf("BigMin(%v, %v) = %v, want %v", tt.x, tt.y, got, tt.want)
		}
	}
}

func TestMin256(t *testing.T) {
	tests := []struct {
		x, y *uint256.Int
		want *uint256.Int
	}{
		{uint256.NewInt(1), uint256.NewInt(2), uint256.NewInt(1)},
		{uint256.NewInt(10), uint256.NewInt(5), uint256.NewInt(5)},
		{uint256.NewInt(7), uint256.NewInt(7), uint256.NewInt(7)},
	}

	for _, tt := range tests {
		got := Min256(tt.x, tt.y)
		if got.Cmp(tt.want) != 0 {
			t.Errorf("Min256(%v, %v) = %v, want %v", tt.x, tt.y, got, tt.want)
		}
	}
}

func TestFirstBitSet(t *testing.T) {
	tests := []struct {
		input *big.Int
		want  int
	}{
		{big.NewInt(0), 0},
		{big.NewInt(1), 0},
		{big.NewInt(2), 1},
		{big.NewInt(4), 2},
		{big.NewInt(8), 3},
		{big.NewInt(10), 1}, // 1010 in binary
	}

	for _, tt := range tests {
		got := FirstBitSet(tt.input)
		if got != tt.want {
			t.Errorf("FirstBitSet(%v) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestPaddedBigBytes(t *testing.T) {
	tests := []struct {
		input *big.Int
		n     int
		want  int // expected length
	}{
		{big.NewInt(1), 32, 32},
		{big.NewInt(256), 32, 32},
		{big.NewInt(256), 1, 2}, // input larger than n
	}

	for _, tt := range tests {
		got := PaddedBigBytes(tt.input, tt.n)
		if len(got) != tt.want {
			t.Errorf("PaddedBigBytes(%v, %d) length = %d, want %d", tt.input, tt.n, len(got), tt.want)
		}
	}
}

func TestByte(t *testing.T) {
	bigint := big.NewInt(0x12345678)

	tests := []struct {
		padlength int
		n         int
		want      byte
	}{
		{32, 31, 0x78},
		{32, 30, 0x56},
		{32, 29, 0x34},
		{32, 28, 0x12},
		{32, 0, 0x00},
		{32, 100, 0x00}, // n >= padlength
	}

	for _, tt := range tests {
		got := Byte(bigint, tt.padlength, tt.n)
		if got != tt.want {
			t.Errorf("Byte(%v, %d, %d) = 0x%02x, want 0x%02x", bigint, tt.padlength, tt.n, got, tt.want)
		}
	}
}

func TestReadBits(t *testing.T) {
	bigint := big.NewInt(0x12345678)
	buf := make([]byte, 8)

	ReadBits(bigint, buf)

	expected := []byte{0x00, 0x00, 0x00, 0x00, 0x12, 0x34, 0x56, 0x78}
	if !bytes.Equal(buf, expected) {
		t.Errorf("ReadBits() = %x, want %x", buf, expected)
	}
}

func TestU256(t *testing.T) {
	// Small number - no change
	x := big.NewInt(100)
	got := U256(new(big.Int).Set(x))
	if got.Cmp(x) != 0 {
		t.Errorf("U256(%v) = %v, want %v", x, got, x)
	}

	// Number larger than 256 bits gets masked
	large := new(big.Int).SetBytes(bytes.Repeat([]byte{0xff}, 33))
	got = U256(new(big.Int).Set(large))
	if got.Cmp(tt256m1) != 0 {
		t.Errorf("U256(large) should be masked to 256 bits")
	}
}

func TestU256Bytes(t *testing.T) {
	tests := []struct {
		input *big.Int
	}{
		{big.NewInt(0)},
		{big.NewInt(1)},
		{big.NewInt(1000000)},
	}

	for _, tt := range tests {
		got := U256Bytes(new(big.Int).Set(tt.input))
		if len(got) != 32 {
			t.Errorf("U256Bytes(%v) length = %d, want 32", tt.input, len(got))
		}
	}
}

func TestS256(t *testing.T) {
	tests := []struct {
		name  string
		input *big.Int
		want  *big.Int
	}{
		{"zero", big.NewInt(0), big.NewInt(0)},
		{"positive", big.NewInt(1), big.NewInt(1)},
		{"just below half", new(big.Int).Sub(tt255, big.NewInt(1)), new(big.Int).Sub(tt255, big.NewInt(1))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := S256(tt.input)
			if got.Cmp(tt.want) != 0 {
				t.Errorf("S256() = %v, want %v", got, tt.want)
			}
		})
	}

	// Test negative conversion (x >= 2^255)
	x := new(big.Int).Set(tt255)
	got := S256(x)
	expected := new(big.Int).Sub(tt255, tt256)
	if got.Cmp(expected) != 0 {
		t.Errorf("S256(2^255) = %v, want %v", got, expected)
	}
}

func TestExp(t *testing.T) {
	tests := []struct {
		base     *big.Int
		exponent *big.Int
		want     *big.Int
	}{
		{big.NewInt(2), big.NewInt(0), big.NewInt(1)},
		{big.NewInt(2), big.NewInt(10), big.NewInt(1024)},
		{big.NewInt(3), big.NewInt(3), big.NewInt(27)},
	}

	for _, tt := range tests {
		// Make copies since Exp modifies inputs
		base := new(big.Int).Set(tt.base)
		exp := new(big.Int).Set(tt.exponent)
		got := Exp(base, exp)
		if got.Cmp(tt.want) != 0 {
			t.Errorf("Exp(%v, %v) = %v, want %v", tt.base, tt.exponent, got, tt.want)
		}
	}
}
