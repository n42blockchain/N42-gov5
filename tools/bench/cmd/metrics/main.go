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

// bench_metrics.go - Metrics Collection Tool for N42
//
// This tool collects system and node metrics for baseline comparison.
//
// Usage:
//   go run bench_metrics.go -datadir /path/to/n42/data -pprof http://localhost:6060
//
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Metrics holds all collected metrics
type Metrics struct {
	Timestamp    time.Time       `json:"timestamp"`
	System       SystemMetrics   `json:"system"`
	Node         NodeMetrics     `json:"node"`
	Database     DatabaseMetrics `json:"database"`
	Memory       MemoryMetrics   `json:"memory"`
	Goroutines   int             `json:"goroutines"`
	Version      string          `json:"version"`
	CollectError []string        `json:"collect_errors,omitempty"`
}

// SystemMetrics holds system-level metrics
type SystemMetrics struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	NumCPU      int    `json:"num_cpu"`
	GoVersion   string `json:"go_version"`
	Hostname    string `json:"hostname,omitempty"`
}

// NodeMetrics holds N42 node metrics
type NodeMetrics struct {
	BlockNumber   string `json:"block_number,omitempty"`
	ChainID       string `json:"chain_id,omitempty"`
	PeerCount     int    `json:"peer_count,omitempty"`
	Syncing       bool   `json:"syncing,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
}

// DatabaseMetrics holds database metrics
type DatabaseMetrics struct {
	TotalSize    int64  `json:"total_size_bytes"`
	TotalSizeStr string `json:"total_size_human"`
	Files        int    `json:"files"`
	LargestFile  string `json:"largest_file,omitempty"`
	LargestSize  int64  `json:"largest_size_bytes,omitempty"`
}

// MemoryMetrics holds memory metrics from pprof
type MemoryMetrics struct {
	Alloc        uint64 `json:"alloc_bytes"`
	TotalAlloc   uint64 `json:"total_alloc_bytes"`
	Sys          uint64 `json:"sys_bytes"`
	HeapAlloc    uint64 `json:"heap_alloc_bytes"`
	HeapSys      uint64 `json:"heap_sys_bytes"`
	HeapIdle     uint64 `json:"heap_idle_bytes"`
	HeapInuse    uint64 `json:"heap_inuse_bytes"`
	HeapReleased uint64 `json:"heap_released_bytes"`
	HeapObjects  uint64 `json:"heap_objects"`
	StackInuse   uint64 `json:"stack_inuse_bytes"`
	StackSys     uint64 `json:"stack_sys_bytes"`
	NumGC        uint32 `json:"num_gc"`
}

func main() {
	datadir := flag.String("datadir", "", "N42 data directory")
	pprofURL := flag.String("pprof", "", "pprof endpoint URL (e.g., http://localhost:6060)")
	rpcURL := flag.String("rpc", "http://localhost:8545", "RPC URL")
	output := flag.String("output", "", "Output file (empty for stdout)")
	flag.Parse()

	metrics := collectMetrics(*datadir, *pprofURL, *rpcURL)

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

	// Pretty print JSON
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(metrics); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding metrics: %v\n", err)
		os.Exit(1)
	}

	// Print summary to stderr
	printSummary(metrics)
}

func collectMetrics(datadir, pprofURL, rpcURL string) *Metrics {
	metrics := &Metrics{
		Timestamp: time.Now(),
		Version:   "1.0.0",
	}

	// System metrics
	metrics.System = SystemMetrics{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
		GoVersion: runtime.Version(),
	}
	if hostname, err := os.Hostname(); err == nil {
		metrics.System.Hostname = hostname
	}

	// Database metrics
	if datadir != "" {
		dbMetrics, err := collectDatabaseMetrics(datadir)
		if err != nil {
			metrics.CollectError = append(metrics.CollectError, fmt.Sprintf("database: %v", err))
		} else {
			metrics.Database = *dbMetrics
		}
	}

	// Node metrics from RPC
	if rpcURL != "" {
		nodeMetrics, err := collectNodeMetrics(rpcURL)
		if err != nil {
			metrics.CollectError = append(metrics.CollectError, fmt.Sprintf("node: %v", err))
		} else {
			metrics.Node = *nodeMetrics
		}
	}

	// Memory metrics from pprof
	if pprofURL != "" {
		memMetrics, goroutines, err := collectMemoryMetrics(pprofURL)
		if err != nil {
			metrics.CollectError = append(metrics.CollectError, fmt.Sprintf("memory: %v", err))
		} else {
			metrics.Memory = *memMetrics
			metrics.Goroutines = goroutines
		}
	}

	return metrics
}

func collectDatabaseMetrics(datadir string) (*DatabaseMetrics, error) {
	metrics := &DatabaseMetrics{}

	// Use du command for total size (more accurate for MDBX)
	cmd := exec.Command("du", "-sb", datadir)
	output, err := cmd.Output()
	if err == nil {
		parts := strings.Fields(string(output))
		if len(parts) > 0 {
			fmt.Sscanf(parts[0], "%d", &metrics.TotalSize)
		}
	}

	// Walk directory for file count and largest file
	var largestSize int64
	var largestFile string

	err = filepath.Walk(datadir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if !info.IsDir() {
			metrics.Files++
			if info.Size() > largestSize {
				largestSize = info.Size()
				largestFile = path
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	metrics.LargestFile = largestFile
	metrics.LargestSize = largestSize
	metrics.TotalSizeStr = formatBytes(metrics.TotalSize)

	return metrics, nil
}

func collectNodeMetrics(rpcURL string) (*NodeMetrics, error) {
	metrics := &NodeMetrics{}
	client := &http.Client{Timeout: 10 * time.Second}

	// eth_blockNumber
	if result, err := rpcCall(client, rpcURL, "eth_blockNumber", []interface{}{}); err == nil {
		if s, ok := result.(string); ok {
			metrics.BlockNumber = s
		}
	}

	// eth_chainId
	if result, err := rpcCall(client, rpcURL, "eth_chainId", []interface{}{}); err == nil {
		if s, ok := result.(string); ok {
			metrics.ChainID = s
		}
	}

	// net_peerCount
	if result, err := rpcCall(client, rpcURL, "net_peerCount", []interface{}{}); err == nil {
		if s, ok := result.(string); ok {
			fmt.Sscanf(s, "0x%x", &metrics.PeerCount)
		}
	}

	// eth_syncing
	if result, err := rpcCall(client, rpcURL, "eth_syncing", []interface{}{}); err == nil {
		if b, ok := result.(bool); ok {
			metrics.Syncing = b
		} else if result != nil {
			metrics.Syncing = true
		}
	}

	// web3_clientVersion
	if result, err := rpcCall(client, rpcURL, "web3_clientVersion", []interface{}{}); err == nil {
		if s, ok := result.(string); ok {
			metrics.ClientVersion = s
		}
	}

	return metrics, nil
}

func collectMemoryMetrics(pprofURL string) (*MemoryMetrics, int, error) {
	// Get runtime stats from debug/vars
	varsURL := fmt.Sprintf("%s/debug/vars", pprofURL)
	resp, err := http.Get(varsURL)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, 0, err
	}

	metrics := &MemoryMetrics{}
	goroutines := 0

	// Parse memstats
	if memstats, ok := data["memstats"].(map[string]interface{}); ok {
		if v, ok := memstats["Alloc"].(float64); ok {
			metrics.Alloc = uint64(v)
		}
		if v, ok := memstats["TotalAlloc"].(float64); ok {
			metrics.TotalAlloc = uint64(v)
		}
		if v, ok := memstats["Sys"].(float64); ok {
			metrics.Sys = uint64(v)
		}
		if v, ok := memstats["HeapAlloc"].(float64); ok {
			metrics.HeapAlloc = uint64(v)
		}
		if v, ok := memstats["HeapSys"].(float64); ok {
			metrics.HeapSys = uint64(v)
		}
		if v, ok := memstats["HeapIdle"].(float64); ok {
			metrics.HeapIdle = uint64(v)
		}
		if v, ok := memstats["HeapInuse"].(float64); ok {
			metrics.HeapInuse = uint64(v)
		}
		if v, ok := memstats["HeapReleased"].(float64); ok {
			metrics.HeapReleased = uint64(v)
		}
		if v, ok := memstats["HeapObjects"].(float64); ok {
			metrics.HeapObjects = uint64(v)
		}
		if v, ok := memstats["StackInuse"].(float64); ok {
			metrics.StackInuse = uint64(v)
		}
		if v, ok := memstats["StackSys"].(float64); ok {
			metrics.StackSys = uint64(v)
		}
		if v, ok := memstats["NumGC"].(float64); ok {
			metrics.NumGC = uint32(v)
		}
	}

	// Get goroutines count
	goroutinesURL := fmt.Sprintf("%s/debug/pprof/goroutine?debug=0", pprofURL)
	resp2, err := http.Get(goroutinesURL)
	if err == nil {
		defer resp2.Body.Close()
		// Count lines for goroutine count (rough estimate)
		body, _ := io.ReadAll(resp2.Body)
		goroutines = len(strings.Split(string(body), "\n\n"))
	}

	return metrics, goroutines, nil
}

func rpcCall(client *http.Client, url, method string, params []interface{}) (interface{}, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})

	resp, err := client.Post(url, "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Result interface{} `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, fmt.Errorf("%s", result.Error.Message)
	}
	return result.Result, nil
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func printSummary(metrics *Metrics) {
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "================================================================================\n")
	fmt.Fprintf(os.Stderr, "N42 Metrics Summary\n")
	fmt.Fprintf(os.Stderr, "================================================================================\n")
	fmt.Fprintf(os.Stderr, "Timestamp:     %s\n", metrics.Timestamp.Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "System:        %s/%s (%d CPUs)\n", metrics.System.OS, metrics.System.Arch, metrics.System.NumCPU)

	if metrics.Node.BlockNumber != "" {
		fmt.Fprintf(os.Stderr, "Block Number:  %s\n", metrics.Node.BlockNumber)
	}
	if metrics.Node.ChainID != "" {
		fmt.Fprintf(os.Stderr, "Chain ID:      %s\n", metrics.Node.ChainID)
	}
	if metrics.Node.ClientVersion != "" {
		fmt.Fprintf(os.Stderr, "Client:        %s\n", metrics.Node.ClientVersion)
	}

	if metrics.Database.TotalSize > 0 {
		fmt.Fprintf(os.Stderr, "Database Size: %s (%d files)\n", metrics.Database.TotalSizeStr, metrics.Database.Files)
	}

	if metrics.Memory.HeapAlloc > 0 {
		fmt.Fprintf(os.Stderr, "Heap Alloc:    %s\n", formatBytes(int64(metrics.Memory.HeapAlloc)))
		fmt.Fprintf(os.Stderr, "Heap Inuse:    %s\n", formatBytes(int64(metrics.Memory.HeapInuse)))
		fmt.Fprintf(os.Stderr, "Sys Memory:    %s\n", formatBytes(int64(metrics.Memory.Sys)))
	}

	if metrics.Goroutines > 0 {
		fmt.Fprintf(os.Stderr, "Goroutines:    %d\n", metrics.Goroutines)
	}

	if len(metrics.CollectError) > 0 {
		fmt.Fprintf(os.Stderr, "Errors:        %v\n", metrics.CollectError)
	}
	fmt.Fprintf(os.Stderr, "================================================================================\n")
}

