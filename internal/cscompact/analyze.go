// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// analyze.go scans Erigon MDBX changeset tables and reports data patterns
// for compression design. READ-ONLY access only.

package cscompact

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// Erigon v2 changeset table names.
const (
	ErigonAccountChangeSet = "AccountChangeSet"
	ErigonStorageChangeSet = "StorageChangeSet"
	ErigonAccountHistory   = "AccountsHistory"
	ErigonStorageHistory   = "StoragesHistory"
)

// AnalysisResult holds profiling data from changeset analysis.
type AnalysisResult struct {
	// Account changeset stats
	AccTotalEntries uint64
	AccTotalBytes   uint64
	AccAvgKeyLen    float64
	AccAvgValLen    float64
	AccUniqueAddrs  int
	AccZeroValues   uint64 // entries with empty/zero value (account deleted)

	// Storage changeset stats
	StoTotalEntries uint64
	StoTotalBytes   uint64
	StoAvgKeyLen    float64
	StoAvgValLen    float64
	StoUniqueAddrs  int
	StoZeroValues   uint64

	// Top address frequency
	TopAccAddrs []AddrFreq
	TopStoAddrs []AddrFreq
}

type AddrFreq struct {
	Addr  [20]byte
	Count uint64
}

// AnalyzeChangesets scans Erigon changeset tables (READ-ONLY).
// sampleBlocks: how many blocks to scan (0 = all).
func AnalyzeChangesets(tx kv.Tx, sampleBlocks uint64) (*AnalysisResult, error) {
	result := &AnalysisResult{}

	// List all tables to confirm we have the right DB.
	log.Info("Scanning Erigon MDBX tables...")

	// Account changeset analysis.
	log.Info("Analyzing AccountChangeSet...")
	if err := analyzeAccountCS(tx, sampleBlocks, result); err != nil {
		return nil, fmt.Errorf("account cs: %w", err)
	}

	// Storage changeset analysis.
	log.Info("Analyzing StorageChangeSet...")
	if err := analyzeStorageCS(tx, sampleBlocks, result); err != nil {
		return nil, fmt.Errorf("storage cs: %w", err)
	}

	return result, nil
}

func analyzeAccountCS(tx kv.Tx, sampleBlocks uint64, result *AnalysisResult) error {
	cursor, err := tx.Cursor(ErigonAccountChangeSet)
	if err != nil {
		return err
	}
	defer cursor.Close()

	addrCount := make(map[[20]byte]uint64)
	var totalKeyBytes, totalValBytes uint64
	var entries, blocks uint64
	var lastBlock uint64 = ^uint64(0)

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return err
		}
		entries++
		totalKeyBytes += uint64(len(k))
		totalValBytes += uint64(len(v))

		// Erigon AccountChangeSet: key = blockNum(8B), value = addr(20B) + oldValue
		// DupSort table: key is blockNum, values are multiple (addr+oldValue) pairs.
		if len(k) >= 8 {
			blockNum := binary.BigEndian.Uint64(k[:8])
			if blockNum != lastBlock {
				blocks++
				lastBlock = blockNum
			}
		}

		// Extract address from value (first 20 bytes).
		if len(v) >= 20 {
			var addr [20]byte
			copy(addr[:], v[:20])
			addrCount[addr]++
			if len(v) == 20 {
				result.AccZeroValues++ // no old value = account didn't exist
			}
		}

		if sampleBlocks > 0 && blocks >= sampleBlocks {
			break
		}

		if entries%10_000_000 == 0 {
			log.Info("  AccountCS scan", "entries", entries, "blocks", blocks,
				"uniqueAddrs", len(addrCount))
		}
	}

	result.AccTotalEntries = entries
	result.AccTotalBytes = totalKeyBytes + totalValBytes
	if entries > 0 {
		result.AccAvgKeyLen = float64(totalKeyBytes) / float64(entries)
		result.AccAvgValLen = float64(totalValBytes) / float64(entries)
	}
	result.AccUniqueAddrs = len(addrCount)

	// Top 10 addresses by frequency.
	result.TopAccAddrs = topN(addrCount, 10)

	log.Info("AccountCS analysis complete",
		"entries", entries, "blocks", blocks,
		"uniqueAddrs", len(addrCount),
		"avgKeyLen", fmt.Sprintf("%.1f", result.AccAvgKeyLen),
		"avgValLen", fmt.Sprintf("%.1f", result.AccAvgValLen),
		"zeroValues", result.AccZeroValues,
		"totalBytes", fmt.Sprintf("%.1f GiB", float64(result.AccTotalBytes)/1e9))

	return nil
}

func analyzeStorageCS(tx kv.Tx, sampleBlocks uint64, result *AnalysisResult) error {
	cursor, err := tx.Cursor(ErigonStorageChangeSet)
	if err != nil {
		return err
	}
	defer cursor.Close()

	addrCount := make(map[[20]byte]uint64)
	var totalKeyBytes, totalValBytes uint64
	var entries, blocks uint64
	var lastBlock uint64 = ^uint64(0)

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return err
		}
		entries++
		totalKeyBytes += uint64(len(k))
		totalValBytes += uint64(len(v))

		// Erigon StorageChangeSet: key = blockNum(8B) + addr(20B) + incarnation(8B)
		// value = storageKey(32B) + oldValue
		if len(k) >= 8 {
			blockNum := binary.BigEndian.Uint64(k[:8])
			if blockNum != lastBlock {
				blocks++
				lastBlock = blockNum
			}
		}

		// Extract address from key (bytes 8-28).
		if len(k) >= 28 {
			var addr [20]byte
			copy(addr[:], k[8:28])
			addrCount[addr]++
		}

		if len(v) <= 32 {
			result.StoZeroValues++ // only key, no old value or small value
		}

		if sampleBlocks > 0 && blocks >= sampleBlocks {
			break
		}

		if entries%10_000_000 == 0 {
			log.Info("  StorageCS scan", "entries", entries, "blocks", blocks,
				"uniqueAddrs", len(addrCount))
		}
	}

	result.StoTotalEntries = entries
	result.StoTotalBytes = totalKeyBytes + totalValBytes
	if entries > 0 {
		result.StoAvgKeyLen = float64(totalKeyBytes) / float64(entries)
		result.StoAvgValLen = float64(totalValBytes) / float64(entries)
	}
	result.StoUniqueAddrs = len(addrCount)
	result.TopStoAddrs = topN(addrCount, 10)

	log.Info("StorageCS analysis complete",
		"entries", entries, "blocks", blocks,
		"uniqueAddrs", len(addrCount),
		"avgKeyLen", fmt.Sprintf("%.1f", result.StoAvgKeyLen),
		"avgValLen", fmt.Sprintf("%.1f", result.StoAvgValLen),
		"zeroValues", result.StoZeroValues,
		"totalBytes", fmt.Sprintf("%.1f GiB", float64(result.StoTotalBytes)/1e9))

	return nil
}

func topN(m map[[20]byte]uint64, n int) []AddrFreq {
	result := make([]AddrFreq, 0, n)
	for addr, count := range m {
		if len(result) < n {
			result = append(result, AddrFreq{addr, count})
		} else {
			// Find min in result.
			minIdx := 0
			for i := 1; i < len(result); i++ {
				if result[i].Count < result[minIdx].Count {
					minIdx = i
				}
			}
			if count > result[minIdx].Count {
				result[minIdx] = AddrFreq{addr, count}
			}
		}
	}
	return result
}
