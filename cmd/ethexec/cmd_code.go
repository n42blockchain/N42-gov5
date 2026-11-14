// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ethexec contract-code import subcommand.

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
)

// unwrapRethBytecode decodes reth's stored Bytecodes value into the raw
// EVM bytecode that hashes to key (the codeHash).
//
// Reth stores LegacyAnalyzedBytecode for ordinary contracts; the on-disk
// payload is [4B BE length] [N bytes bytecode + 33B revm padding] [jump
// table bitmap]. Writing the raw stored bytes into N42's Code table
// produces entries whose keccak256(value) != key, which witness-replay
// catches and refuses (correctly — EVM would see EOA-mode and drift).
//
// Strategy mirrors cmd/code-fix: probe candidate slice lengths near the
// length hint at v[0:4], then expand. Returns nil when no slice in v
// hashes to key (caller should skip the entry rather than corrupt the
// target Code table).
func unwrapRethBytecode(v, key []byte) []byte {
	// Fast path: value is already the raw bytecode.
	if bytes.Equal(keccak256(v), key) {
		return v
	}
	if len(v) < 4 {
		return nil
	}
	l32 := int(binary.BigEndian.Uint32(v[:4]))
	// Strategy 1: lengths within 33 bytes of the BE uint32 hint.
	for _, off := range []int{0, -1, 1, -33, -32, -34, 33, -43, 43} {
		n := l32 + off
		if n > 0 && 4+n <= len(v) {
			slice := v[4 : 4+n]
			if bytes.Equal(keccak256(slice), key) {
				return slice
			}
		}
	}
	// Strategy 2: expand search around l32 by ±256.
	for off := 2; off <= 256; off++ {
		for _, sign := range []int{-1, 1} {
			n := l32 + off*sign
			if n > 0 && 4+n <= len(v) {
				slice := v[4 : 4+n]
				if bytes.Equal(keccak256(slice), key) {
					return slice
				}
			}
		}
	}
	// Strategy 3: bounded full sweep at offset 4 for stragglers.
	maxLen := len(v) - 4
	if maxLen > 4096 {
		maxLen = 4096
	}
	for n := 1; n <= maxLen; n++ {
		diff := n - l32
		if diff < 0 {
			diff = -diff
		}
		if diff <= 266 {
			continue // already tried
		}
		slice := v[4 : 4+n]
		if bytes.Equal(keccak256(slice), key) {
			return slice
		}
	}
	// Strategy 4: small prefix offsets when length hint isn't at v[0:4].
	for _, prefix := range []int{1, 2, 3, 5, 8} {
		if prefix >= len(v) {
			continue
		}
		slice := v[prefix:]
		if bytes.Equal(keccak256(slice), key) {
			return slice
		}
	}
	return nil
}

// runCodeImport copies Reth Bytecodes table into one or more ethexec MDBX
// Code tables. Supports comma-separated target paths to populate both
// primary and shadow in one pass.
func runCodeImport(c *cli.Context) error {
	rethPath := c.String("reth-db")
	targets := strings.Split(c.String("target"), ",")

	// Open Reth readonly.
	logger := log2.New()
	rethDB, err := mdbx.NewMDBX(logger).
		Path(rethPath).
		Label(kv.ChainDB).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["Bytecodes"] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open reth: %w", err)
	}
	defer rethDB.Close()

	rtx, err := rethDB.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()

	// Count first.
	cursor, err := rtx.Cursor("Bytecodes")
	if err != nil {
		return err
	}
	var total uint64
	for k, _, e := cursor.First(); k != nil && e == nil; k, _, e = cursor.Next() {
		total++
	}
	cursor.Close()
	log.Info("Reth Bytecodes counted", "entries", total)

	// Open all target MDBX databases.
	var dbs []kv.RwDB
	for _, p := range targets {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := os.MkdirAll(p, 0755); err != nil {
			return err
		}
		d, err := mdbx.NewMDBX(logger).
			Path(p).
			Label(kv.ChainDB).
			PageSize(4096).
			MapSize(64 * datasize.GB).
			GrowthStep(2 * datasize.GB).
			DirtySpace(uint64(512 * datasize.MB)).
			DBVerbosity(kv.DBVerbosityLvl(2)).
			Open(context.Background())
		if err != nil {
			return fmt.Errorf("open target %s: %w", p, err)
		}
		dbs = append(dbs, d)
		defer d.Close()
	}

	// Begin write txs on all targets.
	var wtxs []kv.RwTx
	for _, d := range dbs {
		wt, err := d.BeginRw(context.Background())
		if err != nil {
			for _, prev := range wtxs {
				prev.Rollback()
			}
			return err
		}
		wtxs = append(wtxs, wt)
	}

	// Stream copy: Reth Bytecodes → all target Code tables.
	cursor, err = rtx.Cursor("Bytecodes")
	if err != nil {
		for _, wt := range wtxs {
			wt.Rollback()
		}
		return err
	}

	var n, nUnwrapped, nSkipped uint64
	var totalBytes uint64
	var lastKey []byte
	for k, v, e := cursor.First(); k != nil && e == nil; k, v, e = cursor.Next() {
		// Reth wraps EVM bytecode in LegacyAnalyzedBytecode (4B length +
		// bytecode + padding + jump table); writing the raw value would
		// produce entries whose keccak(value) != key. Decode first and
		// verify; skip rather than corrupt the target on undecodable
		// entries (rare — typically EIP-7702 delegations or future formats).
		bc := unwrapRethBytecode(v, k)
		if bc == nil {
			nSkipped++
			if nSkipped <= 10 {
				log.Warn("code-import: undecodable reth bytecode, skipping",
					"key", fmt.Sprintf("%x", k), "v_len", len(v))
			}
			lastKey = append(lastKey[:0], k...)
			n++
			continue
		}
		if len(bc) != len(v) {
			nUnwrapped++
		}
		for _, wt := range wtxs {
			if err := wt.Put("Code", k, bc); err != nil {
				cursor.Close()
				for _, w := range wtxs {
					w.Rollback()
				}
				return fmt.Errorf("put Code: %w", err)
			}
		}
		lastKey = append(lastKey[:0], k...)
		n++
		totalBytes += uint64(len(k) + len(bc))
		if n%100_000 == 0 {
			log.Info("  importing", "progress", n, "total", total,
				"unwrapped", nUnwrapped, "skipped", nSkipped,
				"dataMB", totalBytes>>20)
		}
		// Commit every 500K to avoid oversized txs.
		if n%500_000 == 0 {
			cursor.Close()
			for _, wt := range wtxs {
				if err := wt.Commit(); err != nil {
					return fmt.Errorf("commit at %d: %w", n, err)
				}
			}
			wtxs = wtxs[:0]
			for _, d := range dbs {
				wt, err := d.BeginRw(context.Background())
				if err != nil {
					return err
				}
				wtxs = append(wtxs, wt)
			}
			// Reopen cursor and seek past last processed key.
			cursor, err = rtx.Cursor("Bytecodes")
			if err != nil {
				for _, wt := range wtxs {
					wt.Rollback()
				}
				return err
			}
			k, v, e = cursor.Seek(lastKey)
			if k != nil && e == nil && bytes.Equal(k, lastKey) {
				k, v, e = cursor.Next() // skip the one we already wrote
			}
			if k == nil {
				break
			}
			continue // re-enter loop with this k,v
		}
	}
	cursor.Close()

	// Final commit.
	for i, wt := range wtxs {
		if err := wt.Commit(); err != nil {
			return fmt.Errorf("final commit target %d: %w", i, err)
		}
	}

	log.Info("Code import complete",
		"entries", n,
		"unwrapped", nUnwrapped,
		"skipped", nSkipped,
		"dataMB", totalBytes>>20,
		"targets", len(dbs))
	return nil
}
