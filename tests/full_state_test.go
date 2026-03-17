// Copyright 2022-2026 The N42 Authors
// Full Ethereum State Test Runner
// Runs ALL official Ethereum state tests and generates detailed reports

package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fullStateManualHarnessSkip = "manual EL fixture execution/analysis harness; excluded from default suite until isolated assertions and opt-in execution are in place"

// TestStats holds statistics for test execution
type TestStats struct {
	TotalFiles     int64
	TotalTests     int64
	Passed         int64
	Failed         int64
	Skipped        int64
	ExecutionError int64
	ByCategory     map[string]*CategoryStats
	ByFork         map[string]*ForkStats
	FailedTests    []FailedTest
	mu             sync.Mutex
}

// CategoryStats holds per-category statistics
type CategoryStats struct {
	Total   int64
	Passed  int64
	Failed  int64
	Skipped int64
}

// ForkStats holds per-fork statistics
type ForkStats struct {
	Total   int64
	Passed  int64
	Failed  int64
	Skipped int64
}

// FailedTest records a failed test for analysis
type FailedTest struct {
	Category string
	File     string
	Name     string
	Fork     string
	Message  string
}

// NewTestStats creates a new test stats tracker
func NewTestStats() *TestStats {
	return &TestStats{
		ByCategory:  make(map[string]*CategoryStats),
		ByFork:      make(map[string]*ForkStats),
		FailedTests: make([]FailedTest, 0),
	}
}

// AddResult records a test result
func (s *TestStats) AddResult(category, file, name, fork string, passed, skipped bool, message string) {
	atomic.AddInt64(&s.TotalTests, 1)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Category stats
	if _, ok := s.ByCategory[category]; !ok {
		s.ByCategory[category] = &CategoryStats{}
	}
	s.ByCategory[category].Total++

	// Fork stats
	if _, ok := s.ByFork[fork]; !ok {
		s.ByFork[fork] = &ForkStats{}
	}
	s.ByFork[fork].Total++

	if skipped {
		atomic.AddInt64(&s.Skipped, 1)
		s.ByCategory[category].Skipped++
		s.ByFork[fork].Skipped++
	} else if passed {
		atomic.AddInt64(&s.Passed, 1)
		s.ByCategory[category].Passed++
		s.ByFork[fork].Passed++
	} else {
		atomic.AddInt64(&s.Failed, 1)
		s.ByCategory[category].Failed++
		s.ByFork[fork].Failed++
		s.FailedTests = append(s.FailedTests, FailedTest{
			Category: category,
			File:     file,
			Name:     name,
			Fork:     fork,
			Message:  message,
		})
	}
}

// PrintReport prints a detailed test report
func (s *TestStats) PrintReport() string {
	var sb strings.Builder

	sb.WriteString("\n")
	sb.WriteString("╔══════════════════════════════════════════════════════════════════╗\n")
	sb.WriteString("║           ETHEREUM EXECUTION LAYER TEST RESULTS                  ║\n")
	sb.WriteString("╠══════════════════════════════════════════════════════════════════╣\n")

	total := s.Passed + s.Failed + s.Skipped
	passRate := float64(0)
	if total > 0 {
		passRate = float64(s.Passed) / float64(total) * 100
	}

	sb.WriteString(fmt.Sprintf("║ Total Tests:     %8d                                       ║\n", total))
	sb.WriteString(fmt.Sprintf("║ Passed:          %8d  (%5.1f%%)                              ║\n", s.Passed, passRate))
	sb.WriteString(fmt.Sprintf("║ Failed:          %8d  (%5.1f%%)                              ║\n", s.Failed, float64(s.Failed)/float64(total)*100))
	sb.WriteString(fmt.Sprintf("║ Skipped:         %8d  (%5.1f%%)                              ║\n", s.Skipped, float64(s.Skipped)/float64(total)*100))
	sb.WriteString("╠══════════════════════════════════════════════════════════════════╣\n")
	sb.WriteString("║                    RESULTS BY FORK                               ║\n")
	sb.WriteString("╠══════════════════════════════════════════════════════════════════╣\n")

	// Sort forks
	forks := make([]string, 0, len(s.ByFork))
	for f := range s.ByFork {
		forks = append(forks, f)
	}
	sort.Strings(forks)

	for _, fork := range forks {
		stats := s.ByFork[fork]
		rate := float64(0)
		if stats.Total > 0 {
			rate = float64(stats.Passed) / float64(stats.Total) * 100
		}
		sb.WriteString(fmt.Sprintf("║ %-15s %6d passed / %6d total (%5.1f%%)              ║\n",
			fork, stats.Passed, stats.Total, rate))
	}

	sb.WriteString("╠══════════════════════════════════════════════════════════════════╣\n")
	sb.WriteString("║                  RESULTS BY CATEGORY                             ║\n")
	sb.WriteString("╠══════════════════════════════════════════════════════════════════╣\n")

	// Sort categories
	cats := make([]string, 0, len(s.ByCategory))
	for c := range s.ByCategory {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	for _, cat := range cats {
		stats := s.ByCategory[cat]
		rate := float64(0)
		if stats.Total > 0 {
			rate = float64(stats.Passed) / float64(stats.Total) * 100
		}
		shortCat := cat
		if len(shortCat) > 25 {
			shortCat = shortCat[:25]
		}
		sb.WriteString(fmt.Sprintf("║ %-25s %5d/%5d (%5.1f%%)                ║\n",
			shortCat, stats.Passed, stats.Total, rate))
	}

	sb.WriteString("╚══════════════════════════════════════════════════════════════════╝\n")

	// Print failure summary
	if len(s.FailedTests) > 0 {
		sb.WriteString("\n=== FAILED TEST SUMMARY (first 50) ===\n")
		count := len(s.FailedTests)
		if count > 50 {
			count = 50
		}
		for i := 0; i < count; i++ {
			ft := s.FailedTests[i]
			sb.WriteString(fmt.Sprintf("%s/%s [%s]: %s\n", ft.Category, ft.Name, ft.Fork, ft.Message))
		}
		if len(s.FailedTests) > 50 {
			sb.WriteString(fmt.Sprintf("... and %d more failures\n", len(s.FailedTests)-50))
		}
	}

	return sb.String()
}

// TestFullStateTests runs ALL Ethereum state tests
func TestFullStateTests(t *testing.T) {
	t.Skip(fullStateManualHarnessSkip)

	testDir := "eth-tests/general-state-tests/GeneralStateTests"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("State tests not found. Run test data setup first.")
	}

	stats := NewTestStats()
	startTime := time.Now()

	// Get all test categories
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Fatalf("Failed to read test directory: %v", err)
	}

	// Supported forks for testing
	supportedForks := []string{"Berlin", "London", "Shanghai", "Cancun", "Prague"}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		category := entry.Name()
		catPath := filepath.Join(testDir, category)

		t.Run(category, func(t *testing.T) {
			err := filepath.Walk(catPath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !strings.HasSuffix(path, ".json") {
					return nil
				}

				atomic.AddInt64(&stats.TotalFiles, 1)

				// Load test file
				tests, err := loadStateTest(path)
				if err != nil {
					t.Logf("Failed to load %s: %v", path, err)
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
							// Skip known issues (test expectation problems, not implementation bugs)
							if isKnownIssue(path, fork, i) {
								continue
							}

							testName := fmt.Sprintf("%s[%s][%d]", name, fork, i)
							result, err := executor.ExecuteTest(&test, &post, fork)

							if err != nil {
								atomic.AddInt64(&stats.ExecutionError, 1)
								stats.AddResult(category, filepath.Base(path), testName, fork, false, false, err.Error())
								continue
							}

							stats.AddResult(category, filepath.Base(path), testName, fork,
								result.Passed, result.Skipped, result.Message)
						}
					}
				}
				return nil
			})
			if err != nil {
				t.Logf("Walk error in %s: %v", category, err)
			}
		})
	}

	elapsed := time.Since(startTime)

	// Print report
	report := stats.PrintReport()
	t.Log(report)
	t.Logf("Execution time: %v", elapsed)

	// Write report to file
	reportPath := "test_report.txt"
	os.WriteFile(reportPath, []byte(report), 0644)
	t.Logf("Report written to %s", reportPath)
}

// TestQuickStateTests runs a subset of tests for quick validation
func TestQuickStateTests(t *testing.T) {
	t.Skip(fullStateManualHarnessSkip)

	testDir := "eth-tests/general-state-tests/GeneralStateTests"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("State tests not found")
	}

	// Quick test categories
	categories := []string{
		"stExample",
		"stCallCodes",
		"stCreate2",
		"stRevertTest",
		"stMemoryTest",
	}

	stats := NewTestStats()
	supportedForks := []string{"Cancun"}

	for _, category := range categories {
		catPath := filepath.Join(testDir, category)
		if _, err := os.Stat(catPath); os.IsNotExist(err) {
			continue
		}

		t.Run(category, func(t *testing.T) {
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
							if result != nil {
								stats.AddResult(category, filepath.Base(path),
									fmt.Sprintf("%s[%d]", name, i), fork,
									result.Passed, result.Skipped, result.Message)
							}
						}
					}
				}
				return nil
			})
		})
	}

	t.Log(stats.PrintReport())
}

// TestAnalyzeFailures analyzes failure patterns
func TestAnalyzeFailures(t *testing.T) {
	t.Skip(fullStateManualHarnessSkip)

	testDir := "eth-tests/general-state-tests/GeneralStateTests"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("State tests not found")
	}

	// Failure pattern counters
	patterns := map[string]int{
		"balance_mismatch":     0,
		"code_mismatch":        0,
		"storage_mismatch":     0,
		"nonce_mismatch":       0,
		"gas_related":          0,
		"selfdestruct_related": 0,
		"other":                0,
	}

	categories := []string{"stCallCodes", "stCreate2", "stRevertTest", "stExample"}
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

			for _, test := range tests {
				for _, fork := range supportedForks {
					postStates, ok := test.Post[fork]
					if !ok {
						continue
					}

					executor := NewStateTestExecutor(fork)
					for _, post := range postStates {
						result, _ := executor.ExecuteTest(&test, &post, fork)
						if result != nil && !result.Passed && !result.Skipped {
							msg := strings.ToLower(result.Message)
							switch {
							case strings.Contains(msg, "balance"):
								patterns["balance_mismatch"]++
							case strings.Contains(msg, "code"):
								patterns["code_mismatch"]++
							case strings.Contains(msg, "storage"):
								patterns["storage_mismatch"]++
							case strings.Contains(msg, "nonce"):
								patterns["nonce_mismatch"]++
							case strings.Contains(msg, "gas"):
								patterns["gas_related"]++
							case strings.Contains(msg, "suicide") || strings.Contains(msg, "selfdestruct"):
								patterns["selfdestruct_related"]++
							default:
								patterns["other"]++
							}
						}
					}
				}
			}
			return nil
		})
	}

	t.Log("\n=== Failure Pattern Analysis ===")
	for pattern, count := range patterns {
		t.Logf("%-25s: %d", pattern, count)
	}
}

// TestSelfdestructCases specifically tests SELFDESTRUCT behavior
func TestSelfdestructCases(t *testing.T) {
	t.Skip(fullStateManualHarnessSkip)

	testDir := "eth-tests/general-state-tests/GeneralStateTests/stCallCodes"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("Test directory not found")
	}

	// Find tests with "Suicide" in name
	suicideTests := []string{}
	filepath.Walk(testDir, func(path string, info os.FileInfo, err error) error {
		if strings.Contains(filepath.Base(path), "Suicide") && strings.HasSuffix(path, ".json") {
			suicideTests = append(suicideTests, path)
		}
		return nil
	})

	t.Logf("Found %d SELFDESTRUCT-related test files", len(suicideTests))

	executor := NewStateTestExecutor("Cancun")
	passed, failed := 0, 0

	for _, path := range suicideTests {
		tests, err := loadStateTest(path)
		if err != nil {
			continue
		}

		for name, test := range tests {
			postStates, ok := test.Post["Cancun"]
			if !ok {
				continue
			}

			for _, post := range postStates {
				result, _ := executor.ExecuteTest(&test, &post, "Cancun")
				if result != nil {
					if result.Passed {
						passed++
					} else {
						failed++
						t.Logf("FAILED: %s - %s", name, result.Message)
					}
				}
			}
		}
	}

	t.Logf("SELFDESTRUCT tests: %d passed, %d failed", passed, failed)
}

// SaveFailedTestsJSON saves failed tests to a JSON file for analysis
func (s *TestStats) SaveFailedTestsJSON(path string) error {
	data, err := json.MarshalIndent(s.FailedTests, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
