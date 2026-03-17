// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ethereum Execution Layer State Test Runner
// Runs official Ethereum state tests against the N42 EVM implementation

package tests

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

// StateTest represents an Ethereum state test
type StateTest struct {
	Env         stEnv                         `json:"env"`
	Pre         map[string]stAccount          `json:"pre"`
	Transaction stTransaction                 `json:"transaction"`
	Post        map[string][]stPostStateEntry `json:"post"`
}

type stEnv struct {
	CurrentCoinbase   string `json:"currentCoinbase"`
	CurrentDifficulty string `json:"currentDifficulty"`
	CurrentGasLimit   string `json:"currentGasLimit"`
	CurrentNumber     string `json:"currentNumber"`
	CurrentTimestamp  string `json:"currentTimestamp"`
	CurrentBaseFee    string `json:"currentBaseFee,omitempty"`
	CurrentRandom     string `json:"currentRandom,omitempty"`
}

type stAccount struct {
	Balance string            `json:"balance"`
	Code    string            `json:"code"`
	Nonce   string            `json:"nonce"`
	Storage map[string]string `json:"storage"`
}

type stTransaction struct {
	Data                 []string `json:"data"`
	GasLimit             []string `json:"gasLimit"`
	GasPrice             string   `json:"gasPrice,omitempty"`
	MaxFeePerGas         string   `json:"maxFeePerGas,omitempty"`
	MaxPriorityFeePerGas string   `json:"maxPriorityFeePerGas,omitempty"`
	Nonce                string   `json:"nonce"`
	SecretKey            string   `json:"secretKey"`
	To                   string   `json:"to"`
	Value                []string `json:"value"`
}

type stPostStateEntry struct {
	Hash    string `json:"hash"`
	Logs    string `json:"logs"`
	Indexes struct {
		Data  int `json:"data"`
		Gas   int `json:"gas"`
		Value int `json:"value"`
	} `json:"indexes"`
	ExpectException string `json:"expectException,omitempty"`
}

// TestResult holds the result of a single test
type TestResult struct {
	Name   string
	Fork   string
	Passed bool
	Error  string
}

// StateTestRunner runs Ethereum state tests
type StateTestRunner struct {
	testDir string
	results []TestResult
	passed  int
	failed  int
	skipped int
}

// NewStateTestRunner creates a new test runner
func NewStateTestRunner(testDir string) *StateTestRunner {
	return &StateTestRunner{
		testDir: testDir,
		results: make([]TestResult, 0),
	}
}

// LoadTest loads a single state test from a JSON file
func LoadTest(path string) (map[string]StateTest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var tests map[string]StateTest
	if err := json.Unmarshal(data, &tests); err != nil {
		return nil, err
	}
	return tests, nil
}

// SupportedForks returns the list of forks we support testing
func SupportedForks() []string {
	return []string{
		"Frontier",
		"Homestead",
		"EIP150",
		"EIP158",
		"Byzantium",
		"Constantinople",
		"ConstantinopleFix",
		"Istanbul",
		"Berlin",
		"London",
		"Paris",
		"Shanghai",
		"Cancun",
		"Prague",
	}
}

// IsSupportedFork checks if a fork is supported
func IsSupportedFork(fork string) bool {
	for _, f := range SupportedForks() {
		if f == fork {
			return true
		}
	}
	return false
}

// GetChainConfig returns the chain config for a given fork
func GetChainConfig(fork string) *params.ChainConfig {
	config := &params.ChainConfig{
		ChainID:   big.NewInt(1),
		Consensus: params.EtHashConsensus,
	}

	// Set fork blocks based on fork name
	switch fork {
	case "Frontier":
		// Genesis state - no forks enabled
	case "Homestead":
		config.HomesteadBlock = big.NewInt(0)
	case "EIP150":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
	case "EIP158":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
	case "Byzantium":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
	case "Constantinople", "ConstantinopleFix":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
		config.ConstantinopleBlock = big.NewInt(0)
		config.PetersburgBlock = big.NewInt(0)
	case "Istanbul":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
		config.ConstantinopleBlock = big.NewInt(0)
		config.PetersburgBlock = big.NewInt(0)
		config.IstanbulBlock = big.NewInt(0)
	case "Berlin":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
		config.ConstantinopleBlock = big.NewInt(0)
		config.PetersburgBlock = big.NewInt(0)
		config.IstanbulBlock = big.NewInt(0)
		config.BerlinBlock = big.NewInt(0)
	case "London":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
		config.ConstantinopleBlock = big.NewInt(0)
		config.PetersburgBlock = big.NewInt(0)
		config.IstanbulBlock = big.NewInt(0)
		config.BerlinBlock = big.NewInt(0)
		config.LondonBlock = big.NewInt(0)
	case "Paris":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
		config.ConstantinopleBlock = big.NewInt(0)
		config.PetersburgBlock = big.NewInt(0)
		config.IstanbulBlock = big.NewInt(0)
		config.BerlinBlock = big.NewInt(0)
		config.LondonBlock = big.NewInt(0)
		config.MergeNetsplitBlock = big.NewInt(0)
	case "Shanghai":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
		config.ConstantinopleBlock = big.NewInt(0)
		config.PetersburgBlock = big.NewInt(0)
		config.IstanbulBlock = big.NewInt(0)
		config.BerlinBlock = big.NewInt(0)
		config.LondonBlock = big.NewInt(0)
		config.MergeNetsplitBlock = big.NewInt(0)
		config.ShanghaiBlock = big.NewInt(0)
	case "Cancun":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
		config.ConstantinopleBlock = big.NewInt(0)
		config.PetersburgBlock = big.NewInt(0)
		config.IstanbulBlock = big.NewInt(0)
		config.BerlinBlock = big.NewInt(0)
		config.LondonBlock = big.NewInt(0)
		config.MergeNetsplitBlock = big.NewInt(0)
		config.ShanghaiBlock = big.NewInt(0)
		config.CancunBlock = big.NewInt(0)
	case "Prague":
		config.HomesteadBlock = big.NewInt(0)
		config.TangerineWhistleBlock = big.NewInt(0)
		config.SpuriousDragonBlock = big.NewInt(0)
		config.ByzantiumBlock = big.NewInt(0)
		config.ConstantinopleBlock = big.NewInt(0)
		config.PetersburgBlock = big.NewInt(0)
		config.IstanbulBlock = big.NewInt(0)
		config.BerlinBlock = big.NewInt(0)
		config.LondonBlock = big.NewInt(0)
		config.MergeNetsplitBlock = big.NewInt(0)
		config.ShanghaiBlock = big.NewInt(0)
		config.CancunBlock = big.NewInt(0)
		config.PragueTime = big.NewInt(0)
	}

	return config
}

// ParseHexOrDecimal parses a hex or decimal string to uint64
func ParseHexOrDecimal(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}

	var val uint64
	var err error
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
		_, err = fmt.Sscanf("0x"+trimmed, "0x%x", &val)
	} else {
		_, err = fmt.Sscanf(s, "%d", &val)
	}
	return val, err
}

// ParseBigInt parses a hex or decimal string to big.Int
func ParseBigInt(s string) (*big.Int, error) {
	if s == "" || s == "0x" || s == "0X" {
		return big.NewInt(0), nil
	}

	val := new(big.Int)
	var ok bool
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		_, ok = val.SetString(s[2:], 16)
	} else {
		_, ok = val.SetString(s, 10)
	}
	if !ok {
		return nil, fmt.Errorf("invalid integer: %s", s)
	}
	return val, nil
}

const ethStateManualHarnessSkip = "manual/placeholder EL fixture harness; excluded from default suite until assertions are implemented"

// TestEthStateTests runs all Ethereum state tests
func TestEthStateTests(t *testing.T) {
	t.Skip(ethStateManualHarnessSkip)

	testDir := "eth-tests/general-state-tests"

	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("Ethereum tests not found. Run: git clone https://github.com/ethereum/tests.git eth-tests/general-state-tests")
	}

	// Define test categories to run
	categories := []string{
		"GeneralStateTests/stArgsZeroOneBalance",
		"GeneralStateTests/stCallCodes",
		"GeneralStateTests/stCallCreateCallCodeTest",
		"GeneralStateTests/stCreate2",
		"GeneralStateTests/stDelegatecallTestHomestead",
		"GeneralStateTests/stEIP150singleCodeGasPrices",
		"GeneralStateTests/stEIP150Specific",
		"GeneralStateTests/stMemoryTest",
		"GeneralStateTests/stPreCompiledContracts",
		"GeneralStateTests/stRecursiveCreate",
		"GeneralStateTests/stRevertTest",
		"GeneralStateTests/stSolidityTest",
		"GeneralStateTests/stStackTests",
		"GeneralStateTests/stTransactionTest",
	}

	runner := NewStateTestRunner(testDir)

	for _, category := range categories {
		catPath := filepath.Join(testDir, category)
		if _, err := os.Stat(catPath); os.IsNotExist(err) {
			t.Logf("Category not found: %s", category)
			continue
		}

		t.Run(category, func(t *testing.T) {
			filepath.Walk(catPath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !strings.HasSuffix(path, ".json") {
					return nil
				}

				tests, err := LoadTest(path)
				if err != nil {
					t.Logf("Failed to load test %s: %v", path, err)
					return nil
				}

				for name, test := range tests {
					t.Run(name, func(t *testing.T) {
						for fork, postStates := range test.Post {
							if !IsSupportedFork(fork) {
								runner.skipped++
								continue
							}

							for _, post := range postStates {
								result := runStateTest(t, name, fork, &test, &post)
								runner.results = append(runner.results, result)
								if result.Passed {
									runner.passed++
								} else {
									runner.failed++
								}
							}
						}
					})
				}
				return nil
			})
		})
	}

	// Print summary
	t.Logf("\n=== Test Summary ===")
	t.Logf("Passed:  %d", runner.passed)
	t.Logf("Failed:  %d", runner.failed)
	t.Logf("Skipped: %d", runner.skipped)
	t.Logf("Total:   %d", runner.passed+runner.failed+runner.skipped)
}

// runStateTest runs a single state test case
func runStateTest(t *testing.T, name string, fork string, test *StateTest, post *stPostStateEntry) TestResult {
	result := TestResult{
		Name:   name,
		Fork:   fork,
		Passed: false,
	}

	// Skip if exception is expected (for now)
	if post.ExpectException != "" {
		result.Passed = true
		result.Error = "skipped: expected exception"
		return result
	}

	// Get chain config for this fork
	_ = GetChainConfig(fork)

	// TODO: Implement full state test execution
	// 1. Create in-memory state DB
	// 2. Apply pre-state
	// 3. Execute transaction
	// 4. Compare post-state root hash

	// For now, mark as passed (placeholder)
	result.Passed = true
	return result
}

// TestEIP2537BLS runs EIP-2537 BLS12-381 precompile tests
func TestEIP2537BLS(t *testing.T) {
	t.Skip(ethStateManualHarnessSkip)

	testDir := "eth-tests/execution-spec-tests/tests/prague/eip2537_bls_12_381_precompiles/vectors"

	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("Execution spec tests not found")
	}

	testFiles := []string{
		"add_G1_bls.json",
		"add_G2_bls.json",
		"mul_G1_bls.json",
		"mul_G2_bls.json",
		"msm_G1_bls.json",
		"msm_G2_bls.json",
		"pairing_check_bls.json",
		"map_fp_to_G1_bls.json",
		"map_fp2_to_G2_bls.json",
	}

	for _, file := range testFiles {
		path := filepath.Join(testDir, file)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Logf("Test file not found: %s", path)
			continue
		}

		t.Run(file, func(t *testing.T) {
			// Load and run BLS test vectors
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("Failed to read test file: %v", err)
			}

			var vectors []map[string]interface{}
			if err := json.Unmarshal(data, &vectors); err != nil {
				t.Fatalf("Failed to parse test file: %v", err)
			}

			t.Logf("Loaded %d test vectors from %s", len(vectors), file)
			// TODO: Execute each test vector against BLS precompiles
		})
	}
}

// TestEIP7702SetCode runs EIP-7702 Set Code transaction tests
func TestEIP7702SetCode(t *testing.T) {
	t.Skip(ethStateManualHarnessSkip)
}

// TestEIP6110Deposits runs EIP-6110 deposit tests
func TestEIP6110Deposits(t *testing.T) {
	t.Skip(ethStateManualHarnessSkip)
}

// TestEIP7002Withdrawals runs EIP-7002 withdrawal request tests
func TestEIP7002Withdrawals(t *testing.T) {
	t.Skip(ethStateManualHarnessSkip)
}

// TestTransactionValidation runs transaction validation tests
func TestTransactionValidation(t *testing.T) {
	t.Skip(ethStateManualHarnessSkip)

	testDir := "eth-tests/general-state-tests/TransactionTests"

	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("Transaction tests not found")
	}

	categories := []string{
		"ttAddress",
		"ttData",
		"ttEIP1559",
		"ttEIP2930",
		"ttGasLimit",
		"ttNonce",
		"ttSignature",
	}

	for _, cat := range categories {
		catPath := filepath.Join(testDir, cat)
		t.Run(cat, func(t *testing.T) {
			filepath.Walk(catPath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !strings.HasSuffix(path, ".json") {
					return nil
				}
				t.Logf("Found test: %s", filepath.Base(path))
				return nil
			})
		})
	}
}

// Placeholder types for compilation
var _ = types.Address{}
var _ = uint256.Int{}
var _ = block.Header{}
var _ = vm.Config{}
