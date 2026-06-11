// n42-datc — prototype of Depth-Adaptive Temporal Checkpointing (DATC) for
// full-history EIP-1186 proofs (design: docs/ethel/eip1186-mpt-proof-storage-research.md §6).
//
// build mode replays the acctcs/storcs changeset freezer from genesis, maintains
// the erigon-layout state trie incrementally (TrieRootComputer), verifies EVERY
// block's root against the real header (headerc freezer) — the gold correctness
// gate — and writes the DATC temporal records:
//
//	DatcAccNode / DatcStorNode : (path, epochIdx) → MarshalTrieNode bytes at
//	                              epoch end (empty = tombstone)
//	DatcAccChg  / DatcStorChg  : (depth, path, epochIdx, block, childNibble)
//	                              → nil — which child changed when (window index)
//	DatcLeafA   / DatcLeafS    : (hashedKey, block) → value (empty = deleted)
//	                              — the leaf history (key-major changesets)
//
// Per-level epoch length E_d = clamp(α·16^d / C̄, 1, 2^22): every node sees ~α
// changes per its own epoch, equalizing the change rate across depths.
//
// Usage:
//
//	n42-datc build --changesets D:/N42-eth1177/chain/freezer \
//	  --headers D:/n42-eth1/chain/freezer --out D:/n42-datc \
//	  --end 2000000 --alpha 16 --cbar 20
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// DATC table names (prototype-local; registered via WithTableCfg).
const (
	tDatcAccNode = "DatcAccNode"
	tDatcStoNode = "DatcStorNode"
	tDatcAccChg  = "DatcAccChg"
	tDatcStoChg  = "DatcStorChg"
	tDatcLeafA   = "DatcLeafA"
	tDatcLeafS   = "DatcLeafS"
	tDatcMeta    = "DatcMeta"
)

const maxChgDepth = 8 // change-index depth cap (deeper levels use fringe rebuild)

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+f+"\n", a...)
	os.Exit(1)
}

func openCS(dir, name string) *freezer.FreezerTable {
	t, err := freezer.NewFreezerTableReadOnly(dir, name, "c")
	if err != nil {
		die("open %s: %v", name, err)
	}
	t.ForceBatchSize(freezer.BatchSize)
	t.SetCompressed(true)
	return t
}

// epochSchedule holds per-depth epoch lengths.
type epochSchedule struct{ e [maxChgDepth + 1]uint64 }

func newSchedule(alpha, cbar float64) epochSchedule {
	var s epochSchedule
	for d := 0; d <= maxChgDepth; d++ {
		e := alpha * pow16(d) / cbar
		if e < 1 {
			e = 1
		}
		if e > 1<<22 {
			e = 1 << 22
		}
		s.e[d] = uint64(e)
	}
	return s
}

func pow16(d int) float64 {
	v := 1.0
	for i := 0; i < d; i++ {
		v *= 16
	}
	return v
}

func (s epochSchedule) epochOf(d int, block uint64) uint64 { return block / s.e[d] }

// nibblesOf expands bytes to one-nibble-per-byte (erigon keyHex form, no terminator).
func nibblesOf(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[2*i] = x >> 4
		out[2*i+1] = x & 0x0f
	}
	return out
}

func main() {
	if len(os.Args) < 2 {
		die("usage: n42-datc build|verify [flags]")
	}
	if os.Args[1] == "verify" {
		runVerify(os.Args[2:])
		return
	}
	if os.Args[1] != "build" {
		die("usage: n42-datc build|verify [flags]")
	}
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	csDir := fs.String("changesets", `D:/N42-eth1177/chain/freezer`, "acctcs/storcs freezer dir")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir (root verification)")
	out := fs.String("out", "", "output MDBX dir")
	endBlock := fs.Uint64("end", 2_000_000, "end block (exclusive)")
	startBlock := fs.Uint64("start", 0, "start block (resume; state must match)")
	alpha := fs.Float64("alpha", 16, "target changes per node per epoch")
	cbar := fs.Float64("cbar", 20, "assumed average changed keys per block")
	batch := fs.Uint64("batch", 50_000, "blocks per MDBX commit")
	mapGB := fs.Int("map.gb", 1024, "MDBX map size GB")
	_ = fs.Parse(os.Args[2:])
	if *out == "" {
		die("--out required")
	}

	logger := log.New()
	acctTbl := openCS(*csDir, "acctcs")
	defer acctTbl.Close()
	storTbl := openCS(*csDir, "storcs")
	defer storTbl.Close()
	hdrs, err := ethel.OpenHeaderCompact(*hdrDir)
	if err != nil {
		die("open headerc: %v", err)
	}
	defer hdrs.Close()

	avail := uint64(acctTbl.Items())
	if *endBlock > avail {
		*endBlock = avail
	}
	if *endBlock > hdrs.MaxBlock() {
		*endBlock = hdrs.MaxBlock()
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for name, item := range kv.ChaindataTablesCfg {
				d[name] = item
			}
			for _, t := range []string{tDatcAccNode, tDatcStoNode, tDatcAccChg, tDatcStoChg, tDatcLeafA, tDatcLeafS, tDatcMeta} {
				d[t] = kv.TableCfgItem{}
			}
			return d
		}).Open(context.Background())
	if err != nil {
		die("open out mdbx: %v", err)
	}
	defer db.Close()

	sched := newSchedule(*alpha, *cbar)
	fmt.Printf("DATC build: blocks [%d, %d) α=%.0f C̄=%.0f\n  epochs/depth: ", *startBlock, *endBlock, *alpha, *cbar)
	for d := 0; d <= maxChgDepth; d++ {
		fmt.Printf("d%d=%d ", d, sched.e[d])
	}
	fmt.Println()

	b := &builder{
		sched: sched, db: db, hdrs: hdrs,
		acctTbl: acctTbl, storTbl: storTbl,
		accDirty: make(map[string]struct{}, 1<<16),
		stoDirty: make(map[string]struct{}, 1<<16),
	}
	if err := b.run(*startBlock, *endBlock, *batch); err != nil {
		die("%v", err)
	}
}

// builder drives the per-block replay + DATC record writing.
type builder struct {
	sched   epochSchedule
	db      kv.RwDB
	hdrs    *ethel.HeaderCompactReader
	acctTbl *freezer.FreezerTable
	storTbl *freezer.FreezerTable

	// Per-level pending changed-path sets since each level's last epoch flush.
	// Key = nibble path (account trie) / domainKey40+nibble path (storage).
	accDirty map[string]struct{}
	stoDirty map[string]struct{}

	leafAPuts, leafSPuts, chgPuts, nodePuts uint64
}

func (b *builder) run(start, end, batchBlocks uint64) error {
	t0 := time.Now()
	var trc *commitment.TrieRootComputer
	var blocksDone uint64

	for lo := start; lo < end; lo += batchBlocks {
		hi := lo + batchBlocks
		if hi > end {
			hi = end
		}
		tx, err := b.db.BeginRw(context.Background())
		if err != nil {
			return err
		}
		trc = commitment.NewTrieRootComputer()
		trc.SetRwTx(tx)

		for n := lo; n < hi; n++ {
			// Block 0 (genesis alloc) builds the trie from scratch (legacy full
			// rebuild); every later block runs incrementally against TrieOf*.
			trc.SetIncremental(n > 0)
			if err := b.block(tx, trc, n); err != nil {
				tx.Rollback()
				return fmt.Errorf("block %d: %w", n, err)
			}
			// Epoch boundary flush per level: after block n, levels whose epoch
			// ends at n persist their changed nodes' current TrieOf* bytes.
			for d := 0; d <= maxChgDepth; d++ {
				if (n+1)%b.sched.e[d] == 0 {
					if err := b.flushEpoch(tx, d, b.sched.epochOf(d, n)); err != nil {
						tx.Rollback()
						return fmt.Errorf("epoch flush d=%d block %d: %w", d, n, err)
					}
				}
			}
			blocksDone++
		}
		if hi == end {
			// Final flush of all partial epochs + meta.
			for d := 0; d <= maxChgDepth; d++ {
				if err := b.flushEpoch(tx, d, b.sched.epochOf(d, hi-1)); err != nil {
					tx.Rollback()
					return err
				}
			}
			meta := make([]byte, 8+8+8)
			binary.BigEndian.PutUint64(meta[0:], hi)
			binary.BigEndian.PutUint64(meta[8:], uint64(b.sched.e[0]))
			binary.BigEndian.PutUint64(meta[16:], uint64(maxChgDepth))
			if err := tx.Put(tDatcMeta, []byte("head"), meta); err != nil {
				tx.Rollback()
				return err
			}
			var sb []byte
			for d := 0; d <= maxChgDepth; d++ {
				sb = binary.BigEndian.AppendUint64(sb, b.sched.e[d])
			}
			if err := tx.Put(tDatcMeta, []byte("sched"), sb); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}

		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		bps := float64(blocksDone) / time.Since(t0).Seconds()
		fmt.Printf("  block %d / %d  %.0f blk/s  heap=%dMB  leafA=%d leafS=%d chg=%d nodes=%d\n",
			hi, end, bps, m.HeapAlloc>>20, b.leafAPuts, b.leafSPuts, b.chgPuts, b.nodePuts)
	}
	fmt.Printf("DATC build done: %d blocks in %s\n", blocksDone, time.Since(t0).Round(time.Second))
	return nil
}

// block applies one block's changesets, verifies the root against the real
// header, and records leaf history + change-index entries.
func (b *builder) block(tx kv.RwTx, trc *commitment.TrieRootComputer, n uint64) error {
	accBlob, err := b.acctTbl.Retrieve(n)
	if err != nil {
		return fmt.Errorf("acctcs: %w", err)
	}
	stoBlob, err := b.storTbl.Retrieve(n)
	if err != nil {
		return fmt.Errorf("storcs: %w", err)
	}

	dirtyA := make(map[types.Address]*account.StateAccount)
	dirtyS := make(map[types.Address]map[types.Hash]*uint256.Int)

	if len(accBlob) > 0 {
		entries, err := ethel.DecodeAccountChanges(accBlob)
		if err != nil {
			return fmt.Errorf("decode acctcs: %w", err)
		}
		for _, e := range entries {
			if len(e.NewValue) == 0 {
				dirtyA[e.Address] = nil
				continue
			}
			var acct account.StateAccount
			if err := acct.DecodeForStorage(e.NewValue); err != nil {
				return fmt.Errorf("decode account %x: %w", e.Address, err)
			}
			dirtyA[e.Address] = &acct
		}
	}
	if len(stoBlob) > 0 {
		entries, err := ethel.DecodeStorageChanges(stoBlob)
		if err != nil {
			return fmt.Errorf("decode storcs: %w", err)
		}
		for _, e := range entries {
			var addr types.Address
			var slot types.Hash
			copy(addr[:], e.CompositeKey[:20])
			copy(slot[:], e.CompositeKey[20:])
			// Stale pre-SELFDESTRUCT entries: account deleted this block.
			if a, ok := dirtyA[addr]; ok && a == nil {
				continue
			}
			inner, ok := dirtyS[addr]
			if !ok {
				inner = make(map[types.Hash]*uint256.Int, 8)
				dirtyS[addr] = inner
			}
			if len(e.NewValue) == 0 {
				inner[slot] = nil // delete
			} else {
				v := new(uint256.Int).SetBytes(e.NewValue)
				inner[slot] = v
			}
		}
	}

	if len(dirtyA) == 0 && len(dirtyS) == 0 {
		return nil // empty block: trie unchanged, nothing to record
	}

	root, err := trc.ComputeRoot(dirtyA, dirtyS)
	if err != nil {
		return fmt.Errorf("ComputeRoot: %w", err)
	}
	hdr, err := b.hdrs.ReadHeader(n)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	if root != hdr.Root {
		return fmt.Errorf("ROOT MISMATCH: computed %x != header %x", root, hdr.Root)
	}

	// Record leaf history + change-index entries + pending changed paths.
	var blk8 [8]byte
	binary.BigEndian.PutUint64(blk8[:], n)
	for addr, acct := range dirtyA {
		addrHash := crypto.Keccak256(addr[:])
		var val []byte
		if acct != nil {
			val = acct.MarshalV2()
		}
		if err := tx.Put(tDatcLeafA, append(append([]byte{}, addrHash...), blk8[:]...), val); err != nil {
			return err
		}
		b.leafAPuts++
		b.recordChange(tx, false, nil, nibblesOf(addrHash), n)
	}
	for addr, slots := range dirtyS {
		addrHash := crypto.Keccak256(addr[:])
		domain := make([]byte, 40)
		copy(domain, addrHash)
		// incarnation 0 (matches TrieRootComputer composite keys)
		for slot, v := range slots {
			slotHash := crypto.Keccak256(slot[:])
			composite := make([]byte, 0, 72+8)
			composite = append(composite, domain...)
			composite = append(composite, slotHash...)
			var val []byte
			if v != nil && !v.IsZero() {
				bb := v.Bytes32()
				s := 0
				for s < 31 && bb[s] == 0 {
					s++
				}
				val = append([]byte{}, bb[s:]...)
			}
			if err := tx.Put(tDatcLeafS, append(composite, blk8[:]...), val); err != nil {
				return err
			}
			b.leafSPuts++
			b.recordChange(tx, true, domain, nibblesOf(slotHash), n)
		}
	}
	return nil
}

// recordChange writes the per-level change-index entries for one dirty key and
// marks the ancestor paths pending for their next epoch flush.
func (b *builder) recordChange(tx kv.RwTx, storage bool, domain []byte, keyNibbles []byte, n uint64) {
	chgTable, dirty := tDatcAccChg, b.accDirty
	if storage {
		chgTable, dirty = tDatcStoChg, b.stoDirty
	}
	maxD := maxChgDepth
	if maxD > len(keyNibbles)-1 {
		maxD = len(keyNibbles) - 1
	}
	for d := 0; d <= maxD; d++ {
		epoch := b.sched.epochOf(d, n)
		// change-index key: depth(1) | domain | path(d) | epoch(4) | block(4) | child(1)
		k := make([]byte, 0, 1+len(domain)+d+4+4+1)
		k = append(k, byte(d))
		k = append(k, domain...)
		k = append(k, keyNibbles[:d]...)
		k = binary.BigEndian.AppendUint32(k, uint32(epoch))
		k = binary.BigEndian.AppendUint32(k, uint32(n))
		k = append(k, keyNibbles[d])
		_ = tx.Put(chgTable, k, nil)
		b.chgPuts++
		// pending path for epoch flush: domain | path(d)
		pk := make([]byte, 0, len(domain)+d)
		pk = append(pk, domain...)
		pk = append(pk, keyNibbles[:d]...)
		dirty[string(pk)] = struct{}{}
	}
}

// flushEpoch persists the epoch-end node bytes for every path changed during
// the closing epoch of level d, reading the CURRENT TrieOf* rows.
func (b *builder) flushEpoch(tx kv.RwTx, d int, epoch uint64) error {
	flushOne := func(storage bool, pending map[string]struct{}) error {
		srcTable, dstTable := modules.TrieOfAccounts, tDatcAccNode
		domLen := 0
		if storage {
			srcTable, dstTable = modules.TrieOfStorage, tDatcStoNode
			domLen = 40
		}
		for pk := range pending {
			path := []byte(pk)
			if len(path)-domLen != d {
				continue // belongs to another level
			}
			// Full-key read works for both tables: the kv layer auto-converts
			// DupSort keys exactly as trie_root_computer's own Put/Delete do.
			node, err := tx.GetOne(srcTable, path)
			if err != nil {
				return err
			}
			// DATC node record: pathLen(1) | domain|path | epoch(4) → node bytes
			// (nil = tombstone). The length prefix keeps different path lengths
			// from interleaving with epoch bytes, so a floor-seek over epochs of
			// ONE path never lands on another path's record.
			k := make([]byte, 0, 1+len(path)+4)
			k = append(k, byte(len(path)))
			k = append(k, path...)
			k = binary.BigEndian.AppendUint32(k, uint32(epoch))
			if err := tx.Put(dstTable, k, node); err != nil {
				return err
			}
			b.nodePuts++
			delete(pending, pk)
		}
		return nil
	}
	if err := flushOne(false, b.accDirty); err != nil {
		return err
	}
	return flushOne(true, b.stoDirty)
}

