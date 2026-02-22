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

package vm

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/n42blockchain/N42/common/crypto/dilithium/mode2"
	"github.com/n42blockchain/N42/common/crypto/dilithium/mode3"
	"github.com/n42blockchain/N42/common/crypto/falcon"
	"github.com/n42blockchain/N42/common/types"
)

func TestPQPrecompileAddresses(t *testing.T) {
	tests := []struct {
		name    string
		addr    types.Address
		want    byte
	}{
		{"Falcon", PQFalconVerifyAddr, 0x14},
		{"Dilithium2", PQDilithium2VerifyAddr, 0x15},
		{"Dilithium3", PQDilithium3VerifyAddr, 0x16},
		{"SQIsign", PQSQIsignVerifyAddr, 0x17},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.addr[19] != tt.want {
				t.Errorf("Address last byte = 0x%02x, want 0x%02x", tt.addr[19], tt.want)
			}
		})
	}
}

func TestFalconVerifyGas(t *testing.T) {
	c := &falconVerify{}
	input := make([]byte, FalconPublicKeySize+PQMessageSize+FalconMaxSignatureSize)

	gas := c.RequiredGas(input)
	if gas != FalconVerifyGas {
		t.Errorf("RequiredGas() = %d, want %d", gas, FalconVerifyGas)
	}
}

func TestFalconVerifyValidSignature(t *testing.T) {
	// Generate key pair
	pk, sk, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	// Create a 32-byte message
	message := make([]byte, 32)
	rand.Read(message)

	// Sign the message
	sig, err := falcon.Sign(sk, message)
	if err != nil {
		t.Fatalf("Failed to sign: %v", err)
	}

	// Build precompile input: pubkey || message || signature
	input := make([]byte, 0, FalconPublicKeySize+32+len(sig))
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	// Run precompile
	c := &falconVerify{}
	output, err := c.Run(input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Check output is success (0x01)
	expected := types.LeftPadBytes([]byte{1}, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected success (0x01), got %x", output)
	}
}

func TestFalconVerifyInvalidSignature(t *testing.T) {
	// Generate key pair
	pk, _, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	// Create a 32-byte message
	message := make([]byte, 32)
	rand.Read(message)

	// Create invalid signature
	invalidSig := make([]byte, 100)
	rand.Read(invalidSig)

	// Build precompile input
	input := make([]byte, 0, FalconPublicKeySize+32+len(invalidSig))
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, invalidSig...)

	// Run precompile
	c := &falconVerify{}
	output, err := c.Run(input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Check output is failure (0x00)
	expected := make([]byte, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected failure (0x00), got %x", output)
	}
}

func TestFalconVerifyWrongKey(t *testing.T) {
	// Generate two key pairs
	pk1, _, _ := falcon.GenerateKey(rand.Reader)
	_, sk2, _ := falcon.GenerateKey(rand.Reader)

	// Create message
	message := make([]byte, 32)
	rand.Read(message)

	// Sign with sk2
	sig, _ := falcon.Sign(sk2, message)

	// Build input with pk1 (wrong key)
	input := make([]byte, 0, FalconPublicKeySize+32+len(sig))
	input = append(input, pk1.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	// Run precompile
	c := &falconVerify{}
	output, _ := c.Run(input)

	// Check output is failure
	expected := make([]byte, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected failure (wrong key), got %x", output)
	}
}

func TestFalconVerifyWrongMessage(t *testing.T) {
	// Generate key pair
	pk, sk, _ := falcon.GenerateKey(rand.Reader)

	// Create two different messages
	message1 := make([]byte, 32)
	message2 := make([]byte, 32)
	rand.Read(message1)
	rand.Read(message2)

	// Sign message1
	sig, _ := falcon.Sign(sk, message1)

	// Build input with message2 (wrong message)
	input := make([]byte, 0, FalconPublicKeySize+32+len(sig))
	input = append(input, pk.Bytes()...)
	input = append(input, message2...)
	input = append(input, sig...)

	// Run precompile
	c := &falconVerify{}
	output, _ := c.Run(input)

	// Check output is failure
	expected := make([]byte, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected failure (wrong message), got %x", output)
	}
}

func TestFalconVerifyInputTooShort(t *testing.T) {
	c := &falconVerify{}

	tests := []struct {
		name  string
		input []byte
	}{
		{"empty", []byte{}},
		{"too short for pubkey", make([]byte, FalconPublicKeySize-1)},
		{"missing message", make([]byte, FalconPublicKeySize)},
		{"missing signature", make([]byte, FalconPublicKeySize+32)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := c.Run(tt.input)
			if err != nil {
				t.Fatalf("Run should not return error, got %v", err)
			}
			expected := make([]byte, 32)
			if !bytes.Equal(output, expected) {
				t.Errorf("Expected failure (0x00), got %x", output)
			}
		})
	}
}

func TestDilithium2VerifyValidSignature(t *testing.T) {
	// Generate key pair
	pk, sk, err := mode2.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	// Create a 32-byte message
	message := make([]byte, 32)
	rand.Read(message)

	// Sign the message
	sig := make([]byte, mode2.SignatureSize)
	mode2.SignTo(sk, message, sig)

	// Build precompile input: pubkey || message || signature
	input := make([]byte, 0, mode2.PublicKeySize+32+mode2.SignatureSize)
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	// Run precompile
	c := &dilithium2Verify{}
	output, err := c.Run(input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Check output is success (0x01)
	expected := types.LeftPadBytes([]byte{1}, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected success (0x01), got %x", output)
	}
}

func TestDilithium2VerifyInvalidSignature(t *testing.T) {
	pk, _, err := mode2.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	message := make([]byte, 32)
	rand.Read(message)

	// Create invalid signature (random bytes)
	invalidSig := make([]byte, mode2.SignatureSize)
	rand.Read(invalidSig)

	input := make([]byte, 0, mode2.PublicKeySize+32+mode2.SignatureSize)
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, invalidSig...)

	c := &dilithium2Verify{}
	output, err := c.Run(input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	expected := make([]byte, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected failure (0x00), got %x", output)
	}
}

func TestDilithium2VerifyWrongKey(t *testing.T) {
	pk1, _, _ := mode2.GenerateKey(rand.Reader)
	_, sk2, _ := mode2.GenerateKey(rand.Reader)

	message := make([]byte, 32)
	rand.Read(message)

	sig := make([]byte, mode2.SignatureSize)
	mode2.SignTo(sk2, message, sig)

	input := make([]byte, 0, mode2.PublicKeySize+32+mode2.SignatureSize)
	input = append(input, pk1.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	c := &dilithium2Verify{}
	output, _ := c.Run(input)

	expected := make([]byte, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected failure (wrong key), got %x", output)
	}
}

func TestDilithium2VerifyInputTooShort(t *testing.T) {
	c := &dilithium2Verify{}

	tests := []struct {
		name  string
		input []byte
	}{
		{"empty", []byte{}},
		{"too short for pubkey", make([]byte, Dilithium2PublicKeySize-1)},
		{"missing signature", make([]byte, Dilithium2PublicKeySize+32)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := c.Run(tt.input)
			if err != nil {
				t.Fatalf("Run should not return error, got %v", err)
			}
			expected := make([]byte, 32)
			if !bytes.Equal(output, expected) {
				t.Errorf("Expected failure (0x00), got %x", output)
			}
		})
	}
}

func TestDilithium3VerifyValidSignature(t *testing.T) {
	pk, sk, err := mode3.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	message := make([]byte, 32)
	rand.Read(message)

	sig := make([]byte, mode3.SignatureSize)
	mode3.SignTo(sk, message, sig)

	input := make([]byte, 0, mode3.PublicKeySize+32+mode3.SignatureSize)
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	c := &dilithium3Verify{}
	output, err := c.Run(input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	expected := types.LeftPadBytes([]byte{1}, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected success (0x01), got %x", output)
	}
}

func TestDilithium3VerifyInvalidSignature(t *testing.T) {
	pk, _, err := mode3.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	message := make([]byte, 32)
	rand.Read(message)

	invalidSig := make([]byte, mode3.SignatureSize)
	rand.Read(invalidSig)

	input := make([]byte, 0, mode3.PublicKeySize+32+mode3.SignatureSize)
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, invalidSig...)

	c := &dilithium3Verify{}
	output, err := c.Run(input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	expected := make([]byte, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected failure (0x00), got %x", output)
	}
}

func TestDilithium3VerifyWrongKey(t *testing.T) {
	pk1, _, _ := mode3.GenerateKey(rand.Reader)
	_, sk2, _ := mode3.GenerateKey(rand.Reader)

	message := make([]byte, 32)
	rand.Read(message)

	sig := make([]byte, mode3.SignatureSize)
	mode3.SignTo(sk2, message, sig)

	input := make([]byte, 0, mode3.PublicKeySize+32+mode3.SignatureSize)
	input = append(input, pk1.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	c := &dilithium3Verify{}
	output, _ := c.Run(input)

	expected := make([]byte, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected failure (wrong key), got %x", output)
	}
}

func TestSQIsignVerifyReturnsNotImplemented(t *testing.T) {
	c := &sqisignVerify{}
	// Even with valid-length input, SQIsign should return errPQNotImplemented
	input := make([]byte, SQIsignPublicKeySize+PQMessageSize+SQIsignSignatureSize)
	output, err := c.Run(input)
	if err != errPQNotImplemented {
		t.Errorf("Expected errPQNotImplemented, got %v", err)
	}
	if output != nil {
		t.Errorf("Expected nil output, got %x", output)
	}
}

func TestDilithium2VerifyGas(t *testing.T) {
	c := &dilithium2Verify{}
	input := make([]byte, Dilithium2PublicKeySize+PQMessageSize+Dilithium2SignatureSize)

	gas := c.RequiredGas(input)
	if gas != Dilithium2VerifyGas {
		t.Errorf("RequiredGas() = %d, want %d", gas, Dilithium2VerifyGas)
	}
}

func TestDilithium3VerifyGas(t *testing.T) {
	c := &dilithium3Verify{}
	input := make([]byte, Dilithium3PublicKeySize+PQMessageSize+Dilithium3SignatureSize)

	gas := c.RequiredGas(input)
	if gas != Dilithium3VerifyGas {
		t.Errorf("RequiredGas() = %d, want %d", gas, Dilithium3VerifyGas)
	}
}

func TestSQIsignVerifyGas(t *testing.T) {
	c := &sqisignVerify{}
	input := make([]byte, SQIsignPublicKeySize+PQMessageSize+SQIsignSignatureSize)

	gas := c.RequiredGas(input)
	if gas != SQIsignVerifyGas {
		t.Errorf("RequiredGas() = %d, want %d", gas, SQIsignVerifyGas)
	}
}

func TestGetPQPrecompiles(t *testing.T) {
	precompiles := GetPQPrecompiles()

	if len(precompiles) != 4 {
		t.Errorf("Expected 4 PQ precompiles, got %d", len(precompiles))
	}

	expectedAddrs := []types.Address{
		PQFalconVerifyAddr,
		PQDilithium2VerifyAddr,
		PQDilithium3VerifyAddr,
		PQSQIsignVerifyAddr,
	}

	for _, addr := range expectedAddrs {
		if _, ok := precompiles[addr]; !ok {
			t.Errorf("Missing precompile at address %x", addr)
		}
	}
}

func TestAddPQPrecompiles(t *testing.T) {
	// Start with a base set
	base := map[types.Address]PrecompiledContract{
		types.BytesToAddress([]byte{1}): &ecrecover{},
	}

	// Add PQ precompiles
	combined := AddPQPrecompiles(base)

	// Should have 1 + 4 = 5 precompiles
	if len(combined) != 5 {
		t.Errorf("Expected 5 combined precompiles, got %d", len(combined))
	}

	// Verify original is not modified
	if len(base) != 1 {
		t.Error("Original map was modified")
	}

	// Verify all PQ precompiles are present
	for addr := range PrecompiledContractsPQ {
		if _, ok := combined[addr]; !ok {
			t.Errorf("Missing PQ precompile at address %x", addr)
		}
	}
}

func TestIsPQPrecompile(t *testing.T) {
	tests := []struct {
		addr   types.Address
		expect bool
	}{
		{PQFalconVerifyAddr, true},
		{PQDilithium2VerifyAddr, true},
		{PQDilithium3VerifyAddr, true},
		{PQSQIsignVerifyAddr, true},
		{types.BytesToAddress([]byte{1}), false},  // ecrecover
		{types.BytesToAddress([]byte{0x0a}), false}, // point evaluation
	}

	for _, tt := range tests {
		if got := IsPQPrecompile(tt.addr); got != tt.expect {
			t.Errorf("IsPQPrecompile(%x) = %v, want %v", tt.addr, got, tt.expect)
		}
	}
}

func TestRunPrecompiledContractWithFalcon(t *testing.T) {
	// Generate key pair
	pk, sk, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	// Create message and sign
	message := make([]byte, 32)
	rand.Read(message)
	sig, _ := falcon.Sign(sk, message)

	// Build input
	input := make([]byte, 0, FalconPublicKeySize+32+len(sig))
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	// Run via RunPrecompiledContract
	c := &falconVerify{}
	suppliedGas := uint64(10000)
	output, remainingGas, err := RunPrecompiledContract(c, input, suppliedGas)
	if err != nil {
		t.Fatalf("RunPrecompiledContract failed: %v", err)
	}

	// Check gas consumption
	expectedGas := suppliedGas - FalconVerifyGas
	if remainingGas != expectedGas {
		t.Errorf("Remaining gas = %d, want %d", remainingGas, expectedGas)
	}

	// Check output
	expected := types.LeftPadBytes([]byte{1}, 32)
	if !bytes.Equal(output, expected) {
		t.Errorf("Expected success (0x01), got %x", output)
	}
}

func TestRunPrecompiledContractOutOfGas(t *testing.T) {
	c := &falconVerify{}
	input := make([]byte, FalconPublicKeySize+32+100)

	// Supply less gas than required
	suppliedGas := uint64(FalconVerifyGas - 1)
	_, _, err := RunPrecompiledContract(c, input, suppliedGas)
	if err != ErrOutOfGas {
		t.Errorf("Expected ErrOutOfGas, got %v", err)
	}
}

func BenchmarkDilithium2Verify(b *testing.B) {
	pk, sk, _ := mode2.GenerateKey(rand.Reader)
	message := make([]byte, 32)
	rand.Read(message)
	sig := make([]byte, mode2.SignatureSize)
	mode2.SignTo(sk, message, sig)

	input := make([]byte, 0, mode2.PublicKeySize+32+mode2.SignatureSize)
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	c := &dilithium2Verify{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Run(input)
	}
}

func BenchmarkDilithium3Verify(b *testing.B) {
	pk, sk, _ := mode3.GenerateKey(rand.Reader)
	message := make([]byte, 32)
	rand.Read(message)
	sig := make([]byte, mode3.SignatureSize)
	mode3.SignTo(sk, message, sig)

	input := make([]byte, 0, mode3.PublicKeySize+32+mode3.SignatureSize)
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	c := &dilithium3Verify{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Run(input)
	}
}

func BenchmarkFalconVerify(b *testing.B) {
	// Generate key pair
	pk, sk, _ := falcon.GenerateKey(rand.Reader)

	// Create message and sign
	message := make([]byte, 32)
	rand.Read(message)
	sig, _ := falcon.Sign(sk, message)

	// Build input
	input := make([]byte, 0, FalconPublicKeySize+32+len(sig))
	input = append(input, pk.Bytes()...)
	input = append(input, message...)
	input = append(input, sig...)

	c := &falconVerify{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Run(input)
	}
}
