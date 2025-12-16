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

// bench_rpc.go - RPC Benchmark Tool for N42
//
// This tool benchmarks key RPC methods and reports latency statistics.
//
// Usage:
//   go run bench_rpc.go -url http://localhost:8545 -n 100
//
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// BenchConfig holds benchmark configuration
type BenchConfig struct {
	URL        string
	Iterations int
	Methods    []string
	Timeout    time.Duration
}

// RPCRequest represents a JSON-RPC request
type RPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

// RPCResponse represents a JSON-RPC response
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      int             `json:"id"`
}

// RPCError represents a JSON-RPC error
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MethodStats holds statistics for a method
type MethodStats struct {
	Method    string
	Count     int
	Errors    int
	Latencies []time.Duration
	P50       time.Duration
	P95       time.Duration
	P99       time.Duration
	Min       time.Duration
	Max       time.Duration
	Avg       time.Duration
}

// Default RPC methods to benchmark
var defaultMethods = []string{
	"eth_blockNumber",
	"eth_chainId",
	"eth_getBlockByNumber",
	"eth_gasPrice",
	"eth_getBalance",
	"eth_getTransactionCount",
	"eth_call",
	"eth_getLogs",
}

// Method parameters map
var methodParams = map[string][]interface{}{
	"eth_blockNumber":         {},
	"eth_chainId":             {},
	"eth_getBlockByNumber":    {"latest", false},
	"eth_gasPrice":            {},
	"eth_getBalance":          {"0x0000000000000000000000000000000000000000", "latest"},
	"eth_getTransactionCount": {"0x0000000000000000000000000000000000000000", "latest"},
	"eth_call": {map[string]string{
		"to":   "0x0000000000000000000000000000000000000000",
		"data": "0x",
	}, "latest"},
	"eth_getLogs": {map[string]interface{}{
		"fromBlock": "latest",
		"toBlock":   "latest",
		"address":   []string{},
		"topics":    []string{},
	}},
}

func main() {
	// Parse flags
	url := flag.String("url", "http://localhost:8545", "RPC URL")
	iterations := flag.Int("n", 100, "Number of iterations per method")
	methodsStr := flag.String("methods", "", "Comma-separated list of methods (empty for default)")
	timeout := flag.Duration("timeout", 30*time.Second, "Request timeout")
	output := flag.String("output", "", "Output file (empty for stdout)")
	flag.Parse()

	// Configure methods
	methods := defaultMethods
	if *methodsStr != "" {
		methods = strings.Split(*methodsStr, ",")
	}

	config := &BenchConfig{
		URL:        *url,
		Iterations: *iterations,
		Methods:    methods,
		Timeout:    *timeout,
	}

	// Run benchmarks
	results := runBenchmarks(config)

	// Output results
	var out io.Writer = os.Stdout
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}

	printResults(out, config, results)
}

func runBenchmarks(config *BenchConfig) map[string]*MethodStats {
	results := make(map[string]*MethodStats)
	client := &http.Client{Timeout: config.Timeout}

	fmt.Fprintf(os.Stderr, "Running benchmarks against %s\n", config.URL)
	fmt.Fprintf(os.Stderr, "Iterations per method: %d\n", config.Iterations)
	fmt.Fprintf(os.Stderr, "Methods: %v\n\n", config.Methods)

	for _, method := range config.Methods {
		stats := &MethodStats{
			Method:    method,
			Latencies: make([]time.Duration, 0, config.Iterations),
		}

		params, ok := methodParams[method]
		if !ok {
			params = []interface{}{}
		}

		fmt.Fprintf(os.Stderr, "Benchmarking %s...", method)

		for i := 0; i < config.Iterations; i++ {
			latency, err := callRPC(client, config.URL, method, params)
			if err != nil {
				stats.Errors++
			} else {
				stats.Latencies = append(stats.Latencies, latency)
			}
			stats.Count++

			// Progress indicator
			if (i+1)%10 == 0 {
				fmt.Fprintf(os.Stderr, ".")
			}
		}

		// Calculate statistics
		calculateStats(stats)
		results[method] = stats

		fmt.Fprintf(os.Stderr, " done (errors: %d)\n", stats.Errors)
	}

	return results
}

func callRPC(client *http.Client, url, method string, params []interface{}) (time.Duration, error) {
	req := RPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	latency := time.Since(start)

	if err != nil {
		return latency, err
	}
	defer resp.Body.Close()

	var rpcResp RPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return latency, err
	}

	if rpcResp.Error != nil {
		return latency, fmt.Errorf("RPC error: %s", rpcResp.Error.Message)
	}

	return latency, nil
}

func calculateStats(stats *MethodStats) {
	if len(stats.Latencies) == 0 {
		return
	}

	// Sort latencies
	sorted := make([]time.Duration, len(stats.Latencies))
	copy(sorted, stats.Latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// Calculate percentiles
	stats.Min = sorted[0]
	stats.Max = sorted[len(sorted)-1]
	stats.P50 = percentile(sorted, 50)
	stats.P95 = percentile(sorted, 95)
	stats.P99 = percentile(sorted, 99)

	// Calculate average
	var total time.Duration
	for _, l := range sorted {
		total += l
	}
	stats.Avg = total / time.Duration(len(sorted))
}

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted) - 1) * p / 100
	return sorted[index]
}

func printResults(out io.Writer, config *BenchConfig, results map[string]*MethodStats) {
	fmt.Fprintf(out, "================================================================================\n")
	fmt.Fprintf(out, "N42 RPC Benchmark Results\n")
	fmt.Fprintf(out, "================================================================================\n")
	fmt.Fprintf(out, "URL:        %s\n", config.URL)
	fmt.Fprintf(out, "Iterations: %d\n", config.Iterations)
	fmt.Fprintf(out, "Time:       %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(out, "================================================================================\n\n")

	// Table header
	fmt.Fprintf(out, "%-30s %8s %8s %10s %10s %10s %10s %10s\n",
		"Method", "Count", "Errors", "Min", "Avg", "P50", "P95", "P99")
	fmt.Fprintf(out, "%s\n", strings.Repeat("-", 110))

	// Results
	for _, method := range config.Methods {
		stats := results[method]
		if stats == nil {
			continue
		}

		fmt.Fprintf(out, "%-30s %8d %8d %10s %10s %10s %10s %10s\n",
			stats.Method,
			stats.Count,
			stats.Errors,
			formatDuration(stats.Min),
			formatDuration(stats.Avg),
			formatDuration(stats.P50),
			formatDuration(stats.P95),
			formatDuration(stats.P99),
		)
	}

	fmt.Fprintf(out, "\n================================================================================\n")

	// Performance summary
	fmt.Fprintf(out, "\nPerformance Summary:\n")
	fmt.Fprintf(out, "-------------------\n")

	// Check against targets
	targets := map[string]time.Duration{
		"eth_blockNumber":      20 * time.Millisecond,
		"eth_chainId":          5 * time.Millisecond,
		"eth_getBlockByNumber": 200 * time.Millisecond,
		"eth_gasPrice":         20 * time.Millisecond,
		"eth_getBalance":       100 * time.Millisecond,
		"eth_call":             500 * time.Millisecond,
		"eth_getLogs":          1000 * time.Millisecond,
	}

	for method, target := range targets {
		stats := results[method]
		if stats == nil {
			continue
		}

		status := "✓ OK"
		if stats.P95 > target {
			status = fmt.Sprintf("✗ SLOW (target: %s)", target)
		}

		fmt.Fprintf(out, "  %-30s P95=%10s  %s\n", method, formatDuration(stats.P95), status)
	}
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.2fµs", float64(d.Microseconds()))
	}
	if d < time.Second {
		return fmt.Sprintf("%.2fms", float64(d.Milliseconds()))
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

