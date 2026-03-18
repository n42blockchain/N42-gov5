// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// BLS12-381 precompile tests based on EIP-2537 test vectors

package tests

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

// BLSTestCase represents a single BLS precompile test case
type BLSTestCase struct {
	Input       string `json:"Input"`
	Name        string `json:"Name"`
	Expected    string `json:"Expected"`
	Gas         uint64 `json:"Gas"`
	NoBenchmark bool   `json:"NoBenchmark"`
}

// BLS precompile addresses per EIP-2537
var blsPrecompileAddresses = map[string]types.Address{
	"add_G1":        types.BytesToAddress([]byte{0x0b}), // BLS12_G1ADD
	"mul_G1":        types.BytesToAddress([]byte{0x0c}), // Active scalar/MSM entry
	"msm_G1":        types.BytesToAddress([]byte{0x0c}), // BLS12_G1MSM
	"multiexp_G1":   types.BytesToAddress([]byte{0x0c}), // Legacy alias
	"add_G2":        types.BytesToAddress([]byte{0x0d}), // BLS12_G2ADD
	"mul_G2":        types.BytesToAddress([]byte{0x0e}), // Active scalar/MSM entry
	"msm_G2":        types.BytesToAddress([]byte{0x0e}), // BLS12_G2MSM
	"multiexp_G2":   types.BytesToAddress([]byte{0x0e}), // Legacy alias
	"pairing":       types.BytesToAddress([]byte{0x0f}), // BLS12_PAIRING
	"map_fp_to_G1":  types.BytesToAddress([]byte{0x10}), // BLS12_MAP_FP_TO_G1
	"map_fp2_to_G2": types.BytesToAddress([]byte{0x11}), // BLS12_MAP_FP2_TO_G2
}

// getPrecompileFromFilename determines which precompile to use based on filename
func getPrecompileFromFilename(filename string) (types.Address, bool) {
	filename = strings.ToLower(filename)
	for key, addr := range blsPrecompileAddresses {
		if strings.Contains(filename, strings.ToLower(key)) {
			return addr, true
		}
	}
	return types.Address{}, false
}

func TestGetPrecompileFromFilenameRecognizesMSMVectors(t *testing.T) {
	t.Parallel()

	testCases := map[string]types.Address{
		"msm_G1_bls.json": types.BytesToAddress([]byte{0x0c}),
		"msm_G2_bls.json": types.BytesToAddress([]byte{0x0e}),
	}

	for filename, want := range testCases {
		filename := filename
		want := want
		t.Run(filename, func(t *testing.T) {
			t.Parallel()

			got, ok := getPrecompileFromFilename(filename)
			if !ok {
				t.Fatalf("expected filename %q to resolve", filename)
			}
			if got != want {
				t.Fatalf("getPrecompileFromFilename(%q)=%s, want %s", filename, got.Hex(), want.Hex())
			}
		})
	}
}

func TestBLSPrecompiles(t *testing.T) {
	blsTestDir := filepath.Join("eth-tests", "execution-spec-tests", "tests", "prague", "eip2537_bls_12_381_precompiles", "vectors")

	// Check if directory exists
	if _, err := os.Stat(blsTestDir); os.IsNotExist(err) {
		t.Skipf("BLS test directory not found: %s", blsTestDir)
		return
	}

	// Get all JSON test files
	files, err := filepath.Glob(filepath.Join(blsTestDir, "*.json"))
	if err != nil {
		t.Fatalf("Failed to glob test files: %v", err)
	}

	if len(files) == 0 {
		t.Skipf("No BLS test files found in %s", blsTestDir)
		return
	}

	for _, file := range files {
		file := file
		filename := filepath.Base(file)

		// Skip fail- prefixed tests for now (these test error conditions)
		if strings.HasPrefix(filename, "fail-") {
			continue
		}

		t.Run(filename, func(t *testing.T) {
			// Load test cases
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("Failed to read test file: %v", err)
			}

			var testCases []BLSTestCase
			if err := json.Unmarshal(data, &testCases); err != nil {
				t.Fatalf("Failed to parse test file: %v", err)
			}

			// Get precompile address
			addr, found := getPrecompileFromFilename(filename)
			if !found {
				t.Skipf("Unknown precompile for file: %s", filename)
				return
			}

			// Get precompile contract from BLS map
			precompile, exists := vm.PrecompiledContractsBLS[addr]
			if !exists {
				t.Fatalf("Precompile not found at address %s", addr.Hex())
			}

			for _, tc := range testCases {
				t.Run(tc.Name, func(t *testing.T) {
					// Decode input
					input, err := hex.DecodeString(tc.Input)
					if err != nil {
						t.Fatalf("Failed to decode input: %v", err)
					}

					// Check gas
					requiredGas := precompile.RequiredGas(input)
					if requiredGas != tc.Gas {
						t.Fatalf("RequiredGas() mismatch: got %d, want %d", requiredGas, tc.Gas)
					}

					// Run precompile
					result, err := precompile.Run(input)
					if err != nil {
						t.Fatalf("Precompile execution failed: %v", err)
					}

					// Decode expected output
					expected, err := hex.DecodeString(tc.Expected)
					if err != nil {
						t.Fatalf("Failed to decode expected output: %v", err)
					}

					// Compare results
					if !bytesEqual(result, expected) {
						t.Errorf("Output mismatch:\n  got:  %x\n  want: %x", result, expected)
					}
				})
			}
		})
	}
}

// TestBLSG1Add specifically tests BLS G1 ADD precompile
func TestBLSG1Add(t *testing.T) {
	testFile := filepath.Join("eth-tests", "execution-spec-tests", "tests", "prague", "eip2537_bls_12_381_precompiles", "vectors", "add_G1_bls.json")

	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skipf("Test file not found: %s", testFile)
		return
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read test file: %v", err)
	}

	var testCases []BLSTestCase
	if err := json.Unmarshal(data, &testCases); err != nil {
		t.Fatalf("Failed to parse test file: %v", err)
	}

	precompile := vm.PrecompiledContractsBLS[types.BytesToAddress([]byte{0x0b})]
	if precompile == nil {
		t.Fatal("BLS G1 ADD precompile not found")
	}

	passed := 0
	failed := 0

	for _, tc := range testCases {
		input, _ := hex.DecodeString(tc.Input)
		expected, _ := hex.DecodeString(tc.Expected)
		if gas := precompile.RequiredGas(input); gas != tc.Gas {
			t.Fatalf("FAIL %s: gas mismatch: got %d, want %d", tc.Name, gas, tc.Gas)
		}

		result, err := precompile.Run(input)
		if err != nil {
			t.Logf("FAIL %s: execution error: %v", tc.Name, err)
			failed++
			continue
		}

		if bytesEqual(result, expected) {
			passed++
		} else {
			t.Logf("FAIL %s: output mismatch", tc.Name)
			failed++
		}
	}

	t.Logf("BLS G1 ADD: %d passed, %d failed", passed, failed)
	if failed > 0 {
		t.Errorf("Some BLS G1 ADD tests failed")
	}
}
