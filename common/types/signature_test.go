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

package types

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

func TestSignatureBasicMethods(t *testing.T) {
	var sig Signature
	for i := range sig {
		sig[i] = byte(i % 256)
	}

	// Test Hex
	hexStr := sig.Hex()
	if len(hexStr) != 2+SignatureLength*2 { // "0x" + 192 hex chars
		t.Errorf("Hex() length = %d, want %d", len(hexStr), 2+SignatureLength*2)
	}
	if hexStr[:2] != "0x" {
		t.Error("Hex() should start with '0x'")
	}

	// Test String (same as Hex)
	str := sig.String()
	if str != hexStr {
		t.Error("String() should be same as Hex()")
	}

	// Test Size
	if sig.Size() != SignatureLength {
		t.Errorf("Size() = %d, want %d", sig.Size(), SignatureLength)
	}

	// Test Bytes
	b := sig.Bytes()
	if len(b) != SignatureLength {
		t.Errorf("Bytes() length = %d, want %d", len(b), SignatureLength)
	}
}

func TestSignatureSetBytes(t *testing.T) {
	var sig Signature

	// Valid input
	validBytes := make([]byte, SignatureLength)
	for i := range validBytes {
		validBytes[i] = byte(i % 256)
	}
	err := sig.SetBytes(validBytes)
	if err != nil {
		t.Errorf("SetBytes() with valid input error = %v", err)
	}

	// Verify bytes were set correctly
	if !bytes.Equal(sig[:], validBytes) {
		t.Error("SetBytes() didn't set bytes correctly")
	}

	// Invalid input - too short
	err = sig.SetBytes(make([]byte, SignatureLength-1))
	if err == nil {
		t.Error("SetBytes() should fail with too short input")
	}

	// Invalid input - too long
	err = sig.SetBytes(make([]byte, SignatureLength+1))
	if err == nil {
		t.Error("SetBytes() should fail with too long input")
	}
}

func TestSignatureMarshalUnmarshal(t *testing.T) {
	var original Signature
	for i := range original {
		original[i] = byte(i % 256)
	}

	// Test Marshal
	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if len(data) != SignatureLength {
		t.Errorf("Marshal() length = %d, want %d", len(data), SignatureLength)
	}

	// Test Unmarshal
	var recovered Signature
	err = recovered.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if recovered != original {
		t.Error("Unmarshal() didn't recover original signature")
	}
}

func TestSignatureTextMarshal(t *testing.T) {
	var sig Signature
	sig[0] = 0xab
	sig[1] = 0xcd

	// Test MarshalText
	text, err := sig.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	if len(text) == 0 {
		t.Error("MarshalText() returned empty")
	}

	// Test UnmarshalText
	var recovered Signature
	err = recovered.UnmarshalText(text)
	if err != nil {
		t.Fatalf("UnmarshalText() error = %v", err)
	}
	if recovered != sig {
		t.Error("UnmarshalText() didn't recover original signature")
	}
}

func TestSignatureJSONMarshal(t *testing.T) {
	var sig Signature
	for i := range sig {
		sig[i] = byte(i % 256)
	}

	// Test JSON marshal
	data, err := json.Marshal(sig)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Test JSON unmarshal
	var recovered Signature
	err = json.Unmarshal(data, &recovered)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if recovered != sig {
		t.Error("JSON round-trip failed")
	}
}

func TestSignatureFormat(t *testing.T) {
	var sig Signature
	sig[0] = 0xab
	sig[1] = 0xcd

	tests := []struct {
		format string
	}{
		{"%v"},
		{"%s"},
		{"%x"},
		{"%X"},
		{"%#x"},
		{"%q"},
		{"%d"},
		{"%z"}, // invalid format
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			result := fmt.Sprintf(tt.format, sig)
			if len(result) == 0 {
				t.Error("Format returned empty string")
			}
		})
	}
}

func TestPublicKeyBasicMethods(t *testing.T) {
	var pk PublicKey
	for i := range pk {
		pk[i] = byte(i)
	}

	// Test Hex
	hexStr := pk.Hex()
	if len(hexStr) != 2+PublicKeyLength*2 { // "0x" + 96 hex chars
		t.Errorf("Hex() length = %d, want %d", len(hexStr), 2+PublicKeyLength*2)
	}
	if hexStr[:2] != "0x" {
		t.Error("Hex() should start with '0x'")
	}

	// Test String (same as Hex)
	str := pk.String()
	if str != hexStr {
		t.Error("String() should be same as Hex()")
	}

	// Test Size
	if pk.Size() != PublicKeyLength {
		t.Errorf("Size() = %d, want %d", pk.Size(), PublicKeyLength)
	}

	// Test Bytes
	b := pk.Bytes()
	if len(b) != PublicKeyLength {
		t.Errorf("Bytes() length = %d, want %d", len(b), PublicKeyLength)
	}
}

func TestPublicKeySetBytes(t *testing.T) {
	var pk PublicKey

	// Valid input
	validBytes := make([]byte, PublicKeyLength)
	for i := range validBytes {
		validBytes[i] = byte(i)
	}
	err := pk.SetBytes(validBytes)
	if err != nil {
		t.Errorf("SetBytes() with valid input error = %v", err)
	}

	// Verify bytes were set correctly
	if !bytes.Equal(pk[:], validBytes) {
		t.Error("SetBytes() didn't set bytes correctly")
	}

	// Invalid input - too short
	err = pk.SetBytes(make([]byte, PublicKeyLength-1))
	if err == nil {
		t.Error("SetBytes() should fail with too short input")
	}

	// Invalid input - too long
	err = pk.SetBytes(make([]byte, PublicKeyLength+1))
	if err == nil {
		t.Error("SetBytes() should fail with too long input")
	}
}

func TestPublicKeyMarshalUnmarshal(t *testing.T) {
	var original PublicKey
	for i := range original {
		original[i] = byte(i)
	}

	// Test Marshal
	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if len(data) != PublicKeyLength {
		t.Errorf("Marshal() length = %d, want %d", len(data), PublicKeyLength)
	}

	// Test Unmarshal
	var recovered PublicKey
	err = recovered.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if recovered != original {
		t.Error("Unmarshal() didn't recover original public key")
	}
}

func TestPublicKeyTextMarshal(t *testing.T) {
	var pk PublicKey
	pk[0] = 0xab
	pk[1] = 0xcd

	// Test MarshalText
	text, err := pk.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	if len(text) == 0 {
		t.Error("MarshalText() returned empty")
	}

	// Test UnmarshalText
	var recovered PublicKey
	err = recovered.UnmarshalText(text)
	if err != nil {
		t.Fatalf("UnmarshalText() error = %v", err)
	}
	if recovered != pk {
		t.Error("UnmarshalText() didn't recover original public key")
	}
}

func TestPublicKeyJSONMarshal(t *testing.T) {
	var pk PublicKey
	for i := range pk {
		pk[i] = byte(i)
	}

	// Test JSON marshal
	data, err := json.Marshal(pk)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Test JSON unmarshal
	var recovered PublicKey
	err = json.Unmarshal(data, &recovered)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if recovered != pk {
		t.Error("JSON round-trip failed")
	}
}

func TestPublicKeyFormat(t *testing.T) {
	var pk PublicKey
	pk[0] = 0xab
	pk[1] = 0xcd

	tests := []struct {
		format string
	}{
		{"%v"},
		{"%s"},
		{"%x"},
		{"%X"},
		{"%#x"},
		{"%q"},
		{"%d"},
		{"%z"}, // invalid format
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			result := fmt.Sprintf(tt.format, pk)
			if len(result) == 0 {
				t.Error("Format returned empty string")
			}
		})
	}
}

func TestConstants(t *testing.T) {
	if SignatureLength != 96 {
		t.Errorf("SignatureLength = %d, want 96", SignatureLength)
	}
	if PublicKeyLength != 48 {
		t.Errorf("PublicKeyLength = %d, want 48", PublicKeyLength)
	}
}
