// Copyright 2022-2026 The N42 Authors
// Detailed failure analysis for Ethereum state tests

package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// FailurePattern tracks failure patterns
type FailurePattern struct {
	Pattern string
	Count   int
	Example string
}

// TestDetailedFailureAnalysis analyzes failure patterns in depth
func TestDetailedFailureAnalysis(t *testing.T) {
	testDir := "eth-tests/general-state-tests/GeneralStateTests"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("State tests not found")
	}

	// Track failure patterns
	patterns := make(map[string]*FailurePattern)
	gasDiffStats := make(map[string]int64) // track gas differences

	// Categories to analyze
	categories := []string{
		"stEIP1559",
		"stEIP2930",
		"stSStoreTest",
		"stRefundTest",
		"stSelfBalance",
		"stShift",
		"stStaticFlagEnabled",
		"stReturnDataTest",
		"stPreCompiledContracts",
	}

	supportedForks := []string{"Cancun"}

	for _, category := range categories {
		catPath := filepath.Join(testDir, category)
		if _, err := os.Stat(catPath); os.IsNotExist(err) {
			continue
		}

		filepath.Walk(catPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || !strings.HasSuffix(path, ".json") {
				return nil
			}

			tests, err := loadStateTest(path)
			if err != nil {
				return nil
			}

			for name, test := range tests {
				for _, fork := range supportedForks {
					postStates, ok := test.Post[fork]
					if !ok {
						continue
					}

					executor := NewStateTestExecutor(fork)
					for i, post := range postStates {
						result, _ := executor.ExecuteTest(&test, &post, fork)
						if result != nil && !result.Passed && !result.Skipped {
							// Categorize the failure
							msg := result.Message
							var patternKey string

							switch {
							case strings.Contains(msg, "balance mismatch"):
								// Extract balance difference info
								patternKey = fmt.Sprintf("%s:balance_mismatch", category)
								// Try to parse balance values
								if strings.Contains(msg, "got 0x") && strings.Contains(msg, "want 0x") {
									patternKey = fmt.Sprintf("%s:balance_mismatch", category)
								}
							case strings.Contains(msg, "code mismatch"):
								patternKey = fmt.Sprintf("%s:code_mismatch", category)
							case strings.Contains(msg, "storage mismatch"):
								patternKey = fmt.Sprintf("%s:storage_mismatch", category)
							case strings.Contains(msg, "nonce mismatch"):
								patternKey = fmt.Sprintf("%s:nonce_mismatch", category)
							case strings.Contains(msg, "intrinsic gas"):
								patternKey = fmt.Sprintf("%s:intrinsic_gas", category)
							default:
								patternKey = fmt.Sprintf("%s:other", category)
							}

							if patterns[patternKey] == nil {
								patterns[patternKey] = &FailurePattern{
									Pattern: patternKey,
									Example: fmt.Sprintf("%s[%s][%d]: %s", name, fork, i, msg),
								}
							}
							patterns[patternKey].Count++
						}
					}
				}
			}
			return nil
		})
	}

	// Sort and print patterns
	var sortedPatterns []*FailurePattern
	for _, p := range patterns {
		sortedPatterns = append(sortedPatterns, p)
	}
	sort.Slice(sortedPatterns, func(i, j int) bool {
		return sortedPatterns[i].Count > sortedPatterns[j].Count
	})

	t.Log("\n=== DETAILED FAILURE PATTERN ANALYSIS ===\n")
	for _, p := range sortedPatterns {
		t.Logf("%-50s: %5d failures", p.Pattern, p.Count)
		t.Logf("  Example: %s\n", truncateString(p.Example, 200))
	}

	_ = gasDiffStats
}

// TestAnalyzeEIP2930Failures specifically analyzes EIP-2930 access list failures
func TestAnalyzeEIP2930Failures(t *testing.T) {
	testDir := "eth-tests/general-state-tests/GeneralStateTests/stEIP2930"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("EIP-2930 tests not found")
	}

	t.Log("\n=== EIP-2930 ACCESS LIST TEST ANALYSIS ===\n")

	passed, failed := 0, 0
	var failedTests []string

	filepath.Walk(testDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(path, ".json") {
			return nil
		}

		tests, err := loadStateTest(path)
		if err != nil {
			return nil
		}

		for name, test := range tests {
			postStates, ok := test.Post["Cancun"]
			if !ok {
				continue
			}

			executor := NewStateTestExecutor("Cancun")
			for i, post := range postStates {
				result, _ := executor.ExecuteTest(&test, &post, "Cancun")
				if result != nil {
					if result.Passed {
						passed++
					} else if !result.Skipped {
						failed++
						if len(failedTests) < 10 {
							failedTests = append(failedTests, fmt.Sprintf("%s[%d]: %s", name, i, result.Message))
						}
					}
				}
			}
		}
		return nil
	})

	t.Logf("EIP-2930 Results: %d passed, %d failed\n", passed, failed)
	t.Log("\nSample failures:")
	for _, f := range failedTests {
		t.Logf("  - %s\n", f)
	}
}

// TestAnalyzeEIP1153Failures analyzes transient storage (TLOAD/TSTORE) failures
func TestAnalyzeEIP1153Failures(t *testing.T) {
	testDir := "eth-tests/general-state-tests/GeneralStateTests/Cancun"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("Cancun tests not found")
	}

	t.Log("\n=== EIP-1153 TRANSIENT STORAGE TEST ANALYSIS ===\n")

	passed, failed := 0, 0
	var failedTests []string

	// Find transient storage tests
	filepath.Walk(testDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(path, ".json") {
			return nil
		}
		if !strings.Contains(path, "transient") && !strings.Contains(path, "EIP1153") {
			return nil
		}

		tests, err := loadStateTest(path)
		if err != nil {
			return nil
		}

		for name, test := range tests {
			postStates, ok := test.Post["Cancun"]
			if !ok {
				continue
			}

			executor := NewStateTestExecutor("Cancun")
			for i, post := range postStates {
				result, _ := executor.ExecuteTest(&test, &post, "Cancun")
				if result != nil {
					if result.Passed {
						passed++
					} else if !result.Skipped {
						failed++
						if len(failedTests) < 10 {
							failedTests = append(failedTests, fmt.Sprintf("%s[%d]: %s", name, i, result.Message))
						}
					}
				}
			}
		}
		return nil
	})

	t.Logf("EIP-1153 Results: %d passed, %d failed\n", passed, failed)
	t.Log("\nSample failures:")
	for _, f := range failedTests {
		t.Logf("  - %s\n", f)
	}
}

// TestAnalyzePrecompileFailures analyzes precompile test failures
func TestAnalyzePrecompileFailures(t *testing.T) {
	testDir := "eth-tests/general-state-tests/GeneralStateTests/stPreCompiledContracts"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("Precompile tests not found")
	}

	t.Log("\n=== PRECOMPILE TEST ANALYSIS ===\n")

	// Group by precompile address
	precompileResults := make(map[string]struct{ passed, failed int })

	filepath.Walk(testDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(path, ".json") {
			return nil
		}

		tests, err := loadStateTest(path)
		if err != nil {
			return nil
		}

		filename := filepath.Base(path)
		// Extract precompile type from filename
		precompileType := "other"
		switch {
		case strings.Contains(filename, "ecrecover"):
			precompileType = "ecrecover"
		case strings.Contains(filename, "sha256"):
			precompileType = "sha256"
		case strings.Contains(filename, "ripemd"):
			precompileType = "ripemd160"
		case strings.Contains(filename, "identity"):
			precompileType = "identity"
		case strings.Contains(filename, "modexp"):
			precompileType = "modexp"
		case strings.Contains(filename, "bn256") || strings.Contains(filename, "alt_bn128"):
			precompileType = "bn256"
		case strings.Contains(filename, "blake2"):
			precompileType = "blake2f"
		}

		for _, test := range tests {
			postStates, ok := test.Post["Cancun"]
			if !ok {
				continue
			}

			executor := NewStateTestExecutor("Cancun")
			for _, post := range postStates {
				result, _ := executor.ExecuteTest(&test, &post, "Cancun")
				if result != nil {
					stats := precompileResults[precompileType]
					if result.Passed {
						stats.passed++
					} else if !result.Skipped {
						stats.failed++
					}
					precompileResults[precompileType] = stats
				}
			}
		}
		return nil
	})

	t.Log("Precompile Results by Type:")
	for ptype, stats := range precompileResults {
		total := stats.passed + stats.failed
		rate := float64(stats.passed) / float64(total) * 100
		t.Logf("  %-15s: %d/%d (%.1f%%)\n", ptype, stats.passed, total, rate)
	}
}

// TestDebugSingleTest runs a single test with detailed output
func TestDebugSingleTest(t *testing.T) {
	// Change this path to debug a specific test
	testPath := "eth-tests/general-state-tests/GeneralStateTests/stEIP2930/variedContext.json"
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		t.Skip("Test file not found")
	}

	tests, err := loadStateTest(testPath)
	if err != nil {
		t.Fatalf("Failed to load test: %v", err)
	}

	for name, test := range tests {
		t.Logf("\n=== Test: %s ===\n", name)

		// Print transaction info
		t.Logf("Transaction:")
		t.Logf("  GasLimit: %v", test.Transaction.GasLimit)
		t.Logf("  GasPrice: %v", test.Transaction.GasPrice)
		t.Logf("  Value: %v", test.Transaction.Value)

		// Run test
		postStates, ok := test.Post["Cancun"]
		if !ok {
			t.Log("No Cancun post state")
			continue
		}

		executor := NewStateTestExecutor("Cancun")
		for i, post := range postStates {
			result, err := executor.ExecuteTest(&test, &post, "Cancun")
			if err != nil {
				t.Logf("[%d] Error: %v", i, err)
				continue
			}
			t.Logf("[%d] Result: passed=%v, message=%s", i, result.Passed, result.Message)
		}
	}
}

// Helper function to truncate strings
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TestAnalyzeGasDifferences analyzes the actual gas differences
func TestAnalyzeGasDifferences(t *testing.T) {
	testDir := "eth-tests/general-state-tests/GeneralStateTests"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("State tests not found")
	}

	// Sample some failures to understand gas differences
	categories := []string{"stSStoreTest", "stRefundTest"}

	t.Log("\n=== GAS DIFFERENCE ANALYSIS ===\n")

	for _, category := range categories {
		catPath := filepath.Join(testDir, category)
		if _, err := os.Stat(catPath); os.IsNotExist(err) {
			continue
		}

		t.Logf("\n--- Category: %s ---\n", category)

		count := 0
		filepath.Walk(catPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || !strings.HasSuffix(path, ".json") || count >= 5 {
				return nil
			}

			tests, err := loadStateTest(path)
			if err != nil {
				return nil
			}

			for name, test := range tests {
				postStates, ok := test.Post["Cancun"]
				if !ok {
					continue
				}

				executor := NewStateTestExecutor("Cancun")
				for i, post := range postStates {
					result, _ := executor.ExecuteTest(&test, &post, "Cancun")
					if result != nil && !result.Passed && !result.Skipped {
						if strings.Contains(result.Message, "balance mismatch") {
							t.Logf("Test: %s[%d]", name, i)
							t.Logf("  File: %s", filepath.Base(path))
							t.Logf("  Message: %s", result.Message)
							count++
							if count >= 5 {
								return nil
							}
						}
					}
				}
			}
			return nil
		})
	}
}

// TestExportFailedTests exports failed tests to JSON for further analysis
func TestExportFailedTests(t *testing.T) {
	testDir := "eth-tests/general-state-tests/GeneralStateTests"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("State tests not found")
	}

	type FailedTestInfo struct {
		Category string `json:"category"`
		File     string `json:"file"`
		Name     string `json:"name"`
		Fork     string `json:"fork"`
		Message  string `json:"message"`
	}

	var failedTests []FailedTestInfo

	categories := []string{"stEIP2930", "stEIP1559", "stSStoreTest", "stRefundTest"}
	supportedForks := []string{"Cancun"}

	for _, category := range categories {
		catPath := filepath.Join(testDir, category)
		if _, err := os.Stat(catPath); os.IsNotExist(err) {
			continue
		}

		filepath.Walk(catPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || !strings.HasSuffix(path, ".json") {
				return nil
			}

			tests, err := loadStateTest(path)
			if err != nil {
				return nil
			}

			for name, test := range tests {
				for _, fork := range supportedForks {
					postStates, ok := test.Post[fork]
					if !ok {
						continue
					}

					executor := NewStateTestExecutor(fork)
					for i, post := range postStates {
						result, _ := executor.ExecuteTest(&test, &post, fork)
						if result != nil && !result.Passed && !result.Skipped {
							if len(failedTests) < 1000 { // Limit export size
								failedTests = append(failedTests, FailedTestInfo{
									Category: category,
									File:     filepath.Base(path),
									Name:     fmt.Sprintf("%s[%d]", name, i),
									Fork:     fork,
									Message:  result.Message,
								})
							}
						}
					}
				}
			}
			return nil
		})
	}

	// Write to file
	data, _ := json.MarshalIndent(failedTests, "", "  ")
	os.WriteFile("failed_tests.json", data, 0644)
	t.Logf("Exported %d failed tests to failed_tests.json", len(failedTests))
}
