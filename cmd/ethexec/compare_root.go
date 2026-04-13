// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// compare-root helper: cross-check state roots at a given block.
// Computes the root via the traditional MPT FlatDBTrieLoader over
// HashedAccounts / HashedStorage and via the JMT / BMT commitment
// backends from lib/commitment, then prints a side-by-side diff.
// Used by the ethexec debug flow when a replayed block disagrees
// with the expected header state root.

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	libcommit "github.com/n42blockchain/N42/lib/commitment"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// runCompareRoot computes the state root at a given block using two methods:
//
//  1. Traditional MPT (FlatDBTrieLoader over HashedAccounts/HashedStorage)
//  2. HPH (HexPatriciaHashed via MPTRootComputer) bulk-rebuilt from PlainState
//
// and compares both against the header root at that block.
//
// Assumes PlainState in --datadir is already populated up to --block
// (e.g. via `ethexec rebuild-state ...`).
func runCompareRoot(c *cli.Context) error {
	datadir := c.String("datadir")
	ancientPath := c.String("ancient")
	block := c.Uint64("block")
	mode := c.String("mode")

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(2 * datasize.TB).
		GrowthStep(4 * datasize.GB).
		DirtySpace(uint64(2 * datasize.GB)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	ctx, cancel := withShutdown()
	defer cancel()

	// Header root from geth ancient.
	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open ancient: %w", err)
	}
	defer f.Close()
	hdrData, err := f.Ancient(freezer.TableHeaders, block)
	if err != nil {
		return fmt.Errorf("read header %d: %w", block, err)
	}
	hdr, err := ethel.DecodeGethHeader(hdrData)
	if err != nil {
		return fmt.Errorf("decode header %d: %w", block, err)
	}
	headerRoot := hdr.Root

	fmt.Printf("\n=== Comparing state root @ block %d ===\n", block)
	fmt.Printf("Header root: %s\n\n", headerRoot.Hex())

	var (
		rootMPT, rootHPH types.Hash
		dHash, dMPT, dHPH time.Duration
		mpAllocMB, hpAllocMB uint64
	)

	if mode == "mpt" || mode == "both" {
		log.Info("=== Method A: Traditional MPT (FlatDBTrieLoader) ===")
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)

		// Phase 1: (re)populate HashedAccounts/HashedStorage from PlainState.
		// CalcStateRoot requires these tables to be up-to-date.
		tHash := time.Now()
		{
			tx, err := db.BeginRw(ctx)
			if err != nil {
				return err
			}
			if err := ethel.RebuildHashedState(tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("rebuild hashed state: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit hashed state: %w", err)
			}
		}
		dHash = time.Since(tHash)
		log.Info("HashedState rebuilt", "elapsed", dHash.Truncate(time.Millisecond))

		// Phase 2: CalcStateRoot clears + rebuilds TrieOfAccounts/TrieOfStorage.
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return err
		}
		tMPT := time.Now()
		r, err := ethel.CalcStateRoot(tx)
		dMPT = time.Since(tMPT)
		tx.Rollback()
		if err != nil {
			return fmt.Errorf("CalcStateRoot: %w", err)
		}
		rootMPT = r
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		mpAllocMB = (m1.Alloc - m0.Alloc) / 1e6
		log.Info("Method A done",
			"root", rootMPT.Hex(),
			"hashedStateBuild", dHash.Truncate(time.Millisecond),
			"calcTrieRoot", dMPT.Truncate(time.Millisecond),
			"total", (dHash + dMPT).Truncate(time.Millisecond),
			"allocDeltaMB", mpAllocMB)
	}

	if mode == "hph" || mode == "both" {
		log.Info("=== Method B: HPH (MPTRootComputer bulk rebuild) ===")
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)

		t0 := time.Now()
		r, err := computeHPHFromPlainState(ctx, db)
		dHPH = time.Since(t0)
		if err != nil {
			return fmt.Errorf("HPH compute: %w", err)
		}
		rootHPH = r
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		hpAllocMB = (m1.Alloc - m0.Alloc) / 1e6
		log.Info("Method B done",
			"root", rootHPH.Hex(),
			"elapsed", dHPH.Truncate(time.Millisecond),
			"allocDeltaMB", hpAllocMB)
	}

	// Comparison table.
	fmt.Println()
	fmt.Println("=== Results ===")
	fmt.Printf("Header        : %s\n", headerRoot.Hex())
	if mode == "mpt" || mode == "both" {
		ok := rootMPT == headerRoot
		fmt.Printf("Method A MPT  : %s  match=%v\n", rootMPT.Hex(), ok)
		fmt.Printf("  hashedState : %s\n", dHash.Truncate(time.Millisecond))
		fmt.Printf("  calcTrieRoot: %s\n", dMPT.Truncate(time.Millisecond))
		fmt.Printf("  total       : %s   allocDelta=%dMB\n", (dHash + dMPT).Truncate(time.Millisecond), mpAllocMB)
	}
	if mode == "hph" || mode == "both" {
		ok := rootHPH == headerRoot
		fmt.Printf("Method B HPH  : %s  match=%v\n", rootHPH.Hex(), ok)
		fmt.Printf("  elapsed     : %s   allocDelta=%dMB\n", dHPH.Truncate(time.Millisecond), hpAllocMB)
	}
	if mode == "both" {
		fmt.Println()
		aTotal := dHash + dMPT
		if dHPH < aTotal {
			fmt.Printf("HPH is %.2fx faster than MPT (incl. hash)\n", float64(aTotal)/float64(dHPH))
		} else {
			fmt.Printf("MPT is %.2fx faster than HPH\n", float64(dHPH)/float64(aTotal))
		}
		if dHPH < dMPT {
			fmt.Printf("HPH is %.2fx faster than MPT CalcTrieRoot alone\n", float64(dMPT)/float64(dHPH))
		} else {
			fmt.Printf("MPT CalcTrieRoot alone is %.2fx faster than HPH\n", float64(dHPH)/float64(dMPT))
		}
	}
	return nil
}

// computeHPHFromPlainState bulk-loads PlainState into HPH in address-sorted
// chunks. Each chunk interleaves one address's account + all its storage slots,
// so HPH folds each account's storage subtrie exactly once per chunk.
//
// Memory profile: ModeDirect's internal dedup map is unbounded in size, so the
// naive "touch everything, call Process once" approach would blow ~13GB on a
// full 8M-block state (260M keys). Instead we call Process() every
// hphChunkKeys touches — HashSort clears the dedup map and reinits the ETL
// collector on each call, while HPH's internal trie state persists across
// calls (its per-block incremental design). Peak memory is bounded to one
// chunk's worth of dedup + ETL buffers (~50MB).
//
// hphChunkKeys is the soft upper bound on touched keys between Process calls.
// Chunk boundaries align on address, so one large-storage address may exceed
// this bound; that's fine.
const hphChunkKeys = 500_000

func computeHPHFromPlainState(ctx context.Context, db kv.RwDB) (types.Hash, error) {
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return types.Hash{}, err
	}
	defer tx.Rollback()

	reader := commitment.NewPlainStateMPTReader(tx)
	computer := commitment.NewMPTRootComputer()
	computer.SetStateReader(reader)
	trie := computer.Trie()

	tmpdir, err := os.MkdirTemp("", "hph-bulk-*")
	if err != nil {
		return types.Hash{}, err
	}
	defer os.RemoveAll(tmpdir)

	updates := libcommit.NewUpdates(libcommit.ModeDirect, tmpdir, libcommit.KeyToHexNibbleHash)

	accCur, err := tx.Cursor(modules.Account)
	if err != nil {
		return types.Hash{}, err
	}
	defer accCur.Close()

	// Separate cursor for storage prefix seek per address. Using a second
	// transaction cursor (not the same tx.Cursor instance) keeps the account
	// iterator position stable across storage probes.
	stoCur, err := tx.Cursor(modules.Storage)
	if err != nil {
		return types.Hash{}, err
	}
	defer stoCur.Close()

	var (
		accCount     int
		stoCount     int
		chunkKeys    int
		chunkIdx     int
		lastRoot     []byte
		lastLog      = time.Now()
	)

	flush := func() error {
		if chunkKeys == 0 {
			return nil
		}
		chunkIdx++
		r, err := trie.Process(ctx, updates, "compare-root", nil, libcommit.WarmupConfig{})
		if err != nil {
			return fmt.Errorf("hph process chunk %d: %w", chunkIdx, err)
		}
		lastRoot = r
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		log.Info("HPH chunk processed",
			"chunk", chunkIdx,
			"keys", chunkKeys,
			"accounts", accCount,
			"storage", stoCount,
			"allocMB", ms.Alloc/1e6)
		chunkKeys = 0
		return nil
	}

	for k, v, err := accCur.First(); k != nil; k, v, err = accCur.Next() {
		if err != nil {
			return types.Hash{}, err
		}
		if len(k) != 20 {
			continue
		}
		// Touch account.
		updates.TouchPlainKey(string(k), v, updates.TouchAccount)
		accCount++
		chunkKeys++

		// Touch all storage slots for this address via a prefix seek.
		prefix := k
		for sk, sv, err := stoCur.Seek(prefix); sk != nil; sk, sv, err = stoCur.Next() {
			if err != nil {
				return types.Hash{}, err
			}
			if len(sk) < 20 || !bytesEqualPrefix(sk, prefix) {
				break
			}
			if len(sk) != 52 {
				continue
			}
			updates.TouchPlainKey(string(sk), sv, updates.TouchStorage)
			stoCount++
			chunkKeys++
		}

		if time.Since(lastLog) > 15*time.Second {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			log.Info("HPH ingest progress",
				"accounts", accCount,
				"storage", stoCount,
				"chunkKeys", chunkKeys,
				"allocMB", ms.Alloc/1e6)
			lastLog = time.Now()
		}

		if chunkKeys >= hphChunkKeys {
			if err := flush(); err != nil {
				return types.Hash{}, err
			}
		}
	}

	// Final partial chunk.
	if err := flush(); err != nil {
		return types.Hash{}, err
	}

	log.Info("HPH: all chunks processed", "chunks", chunkIdx, "accounts", accCount, "storage", stoCount)

	// lastRoot is the root after the final Process call — that's the complete root.
	if lastRoot == nil {
		// Empty state (no accounts touched). Ask HPH for the current root.
		r, err := trie.RootHash()
		if err != nil {
			return types.Hash{}, err
		}
		lastRoot = r
	}

	var h types.Hash
	copy(h[:], lastRoot)
	return h, nil
}

// bytesEqualPrefix reports whether k starts with prefix.
func bytesEqualPrefix(k, prefix []byte) bool {
	if len(k) < len(prefix) {
		return false
	}
	for i := range prefix {
		if k[i] != prefix[i] {
			return false
		}
	}
	return true
}
