// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// cs_freezer.go — live-import changesets in the freezer, not MDBX.
//
// Archive data-layout decision (docs/ethel/archive-data-layout.md): the state
// MDBX stays bounded to the hashed state itself; per-block account/storage
// changesets are append-forever streams and belong in acctcs/storcs freezer
// segment files (the same Erigon-V2 blob format the ethexec replay pipeline
// writes), and MDBX history indexes are not maintained on the live path at
// all (history is offline-derived). The first archive catch-up run grew
// chaindata 163 -> 256 GB in 142k blocks largely from the embedded
// changeset/history stream; this moves it out.
//
// Pieces:
//
//   - CSFreezerSink: owns the acctcs/storcs FreezerTables, accumulates one
//     encoded blob pair per block and appends+fsyncs them at the importer's
//     commit boundary BEFORE the MDBX tx commits, keeping the invariant
//     freezer-head >= MDBX-head. A crash re-executes the tail and the
//     re-appends overwrite (FreezerTable.Append truncate-then-append).
//   - CSFreezerWriter: the WriterWithChangeSets used by hashed-canonical
//     execution when the sink is wired. Collection is the stock
//     ChangeSetWriter; WriteChangeSets encodes the V2 blobs (post-block
//     values resolved from the hashed tables, which IntermediateRoot has
//     already written in the same tx) and hands them to the sink;
//     WriteHistory is a no-op.
//   - BuildRetainListMerged: the staged-Merkle retain-list builder that reads
//     freezer blobs where covered and falls back to the legacy MDBX
//     changesets for older blocks (transition-safe for datadirs whose early
//     history predates the sink).

package ethel

import (
	"fmt"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// CSFreezerSink appends per-block changeset blobs to acctcs/storcs.
type CSFreezerSink struct {
	acct *freezer.FreezerTable
	stor *freezer.FreezerTable
	fz   *freezer.Freezer

	mu   sync.Mutex
	next uint64 // block number the next Add must carry
	acc  [][]byte
	sto  [][]byte
	base uint64 // block number of acc[0]/sto[0]
}

// NewCSFreezerSink opens (creating if absent) the acctcs/storcs tables and
// aligns them to the executed head: a fresh pair adopts head+1 as its
// mid-history start (cidx start field); an existing pair ahead of head is
// truncated back (freezer >= MDBX invariant after a crash). A pair that ends
// BELOW head+1 has a gap this sink cannot fill (those blocks' changesets
// exist only in MDBX or a witness replay) — that is an error, the operator
// must backfill or wipe the tables.
func NewCSFreezerSink(fz *freezer.Freezer, head uint64) (*CSFreezerSink, error) {
	acct, err := fz.EnsureTable(freezer.TableAccountChanges, "c")
	if err != nil {
		return nil, fmt.Errorf("cs sink: open acctcs: %w", err)
	}
	stor, err := fz.EnsureTable(freezer.TableStorageChanges, "c")
	if err != nil {
		return nil, fmt.Errorf("cs sink: open storcs: %w", err)
	}
	want := head + 1
	for _, t := range []*freezer.FreezerTable{acct, stor} {
		if err := alignCSTable(t, want); err != nil {
			return nil, err
		}
	}
	log.Info("cs freezer sink aligned", "start", want,
		"acctcsStart", acct.StartItem(), "storcsStart", stor.StartItem())
	return &CSFreezerSink{acct: acct, stor: stor, fz: fz, next: want, base: want}, nil
}

// alignCSTable brings one cs table to "next append = want":
//   - zero-entry table (fresh, or reorg-emptied): (re-)declare origin = want
//   - table ahead of want: truncate the excess (freezer >= MDBX invariant
//     after a crash; the importer re-executes and re-appends)
//   - table whose entries end BELOW want: an un-fillable gap — error
func alignCSTable(t *freezer.FreezerTable, want uint64) error {
	items := t.Items()
	switch {
	case items == t.StartItem(): // empty at any origin
		if err := t.SetStartItem(want); err != nil {
			return fmt.Errorf("cs sink: set start: %w", err)
		}
	case items >= want && t.StartItem() < want:
		if items > want {
			if err := t.TruncateHead(want); err != nil {
				return fmt.Errorf("cs sink: truncate to %d: %w", want, err)
			}
		}
	case t.StartItem() >= want:
		// Entries exist but wholly at/above want (deep unwind below the
		// table's origin): drop them all, then re-declare the origin.
		if err := t.TruncateHead(t.StartItem()); err != nil {
			return fmt.Errorf("cs sink: empty table: %w", err)
		}
		if err := t.SetStartItem(want); err != nil {
			return fmt.Errorf("cs sink: re-origin: %w", err)
		}
	default: // items < want with entries: a gap the sink must not paper over
		return fmt.Errorf("cs sink: table head %d below wanted %d (gap) — backfill via witness replay or wipe", items, want)
	}
	return nil
}

// Rewind re-aligns the sink after a reorg unwind moved the executed head to
// newHead: pending blobs are dropped and both tables are brought to
// "next append = newHead+1" (truncating, or re-declaring the origin when the
// unwind went below it).
func (s *CSFreezerSink) Rewind(newHead uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acc = s.acc[:0]
	s.sto = s.sto[:0]
	want := newHead + 1
	for _, t := range []*freezer.FreezerTable{s.acct, s.stor} {
		if err := alignCSTable(t, want); err != nil {
			return err
		}
	}
	s.next, s.base = want, want
	log.Info("cs freezer sink rewound", "nextAppend", want)
	return nil
}

// Add queues block blk's encoded blobs. Blocks must arrive in order; a
// REWIND (blk <= a previously queued number, after the importer rolled its
// batch back and re-executes) drops the queued tail from blk onward — any
// already-appended entries are simply overwritten at the next Flush by
// FreezerTable.Append's truncate-then-append.
func (s *CSFreezerSink) Add(blk uint64, acc, sto []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case blk == s.next:
	case blk < s.next:
		if blk >= s.base {
			keep := blk - s.base
			s.acc = s.acc[:keep]
			s.sto = s.sto[:keep]
		} else {
			// Rewound past everything queued: restart the pending window.
			s.acc = s.acc[:0]
			s.sto = s.sto[:0]
			s.base = blk
		}
	default:
		return fmt.Errorf("cs sink: block %d out of order (want %d)", blk, s.next)
	}
	s.acc = append(s.acc, acc)
	s.sto = append(s.sto, sto)
	s.next = blk + 1
	return nil
}

// Flush appends every queued blob and fsyncs the freezer. Call BEFORE the
// MDBX commit that covers the same blocks.
func (s *CSFreezerSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.acc {
		blk := s.base + uint64(i)
		if err := s.acct.Append(blk, s.acc[i]); err != nil {
			return fmt.Errorf("cs sink: append acctcs %d: %w", blk, err)
		}
		if err := s.stor.Append(blk, s.sto[i]); err != nil {
			return fmt.Errorf("cs sink: append storcs %d: %w", blk, err)
		}
	}
	s.acc = s.acc[:0]
	s.sto = s.sto[:0]
	s.base = s.next
	if err := s.acct.Sync(); err != nil {
		return err
	}
	return s.stor.Sync()
}

// BlockChangesProvider adapts the sink's tables to the unwind path: block n's
// pre-value maps decoded from the freezer blobs, or found=false when n is
// outside the tables' coverage (pre-sink blocks unwind via MDBX changesets).
func (s *CSFreezerSink) BlockChangesProvider() commitment.BlockChangesProvider {
	return func(n uint64) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int, bool, error) {
		if n < s.acct.StartItem() || n >= s.acct.Items() {
			return nil, nil, false, nil
		}
		accBlob, err := s.acct.Retrieve(n)
		if err != nil {
			return nil, nil, false, err
		}
		accChanges, err := DecodeAccountChanges(accBlob)
		if err != nil {
			return nil, nil, false, err
		}
		accounts := make(map[types.Address]*account.StateAccount, len(accChanges))
		for i := range accChanges {
			if len(accChanges[i].OldValue) == 0 {
				accounts[accChanges[i].Address] = nil // created in n → unwind deletes
				continue
			}
			acc := new(account.StateAccount)
			if err := acc.DecodeForStorageV2(accChanges[i].OldValue); err != nil {
				return nil, nil, false, fmt.Errorf("decode acctcs old %d: %w", n, err)
			}
			accounts[accChanges[i].Address] = acc
		}
		stoBlob, err := s.stor.Retrieve(n)
		if err != nil {
			return nil, nil, false, err
		}
		stoChanges, err := DecodeStorageChanges(stoBlob)
		if err != nil {
			return nil, nil, false, err
		}
		storage := make(map[types.Address]map[types.Hash]*uint256.Int)
		for i := range stoChanges {
			k := stoChanges[i].CompositeKey
			if len(k) < length.Addr+length.Hash {
				continue
			}
			var addr types.Address
			var slot types.Hash
			copy(addr[:], k[:length.Addr])
			copy(slot[:], k[length.Addr:length.Addr+length.Hash])
			val := new(uint256.Int)
			if len(stoChanges[i].OldValue) > 0 {
				val.SetBytes(stoChanges[i].OldValue)
			}
			if storage[addr] == nil {
				storage[addr] = make(map[types.Hash]*uint256.Int)
			}
			storage[addr][slot] = val
		}
		return accounts, storage, true, nil
	}
}

// CSFreezerWriter is the hashed-canonical WriterWithChangeSets that routes
// changesets to the freezer sink. Code persistence matches
// HashedCanonicalWriter; history-index writes are dropped by design.
type CSFreezerWriter struct {
	*state.ChangeSetWriter
	tx       kv.RwTx
	blockNum uint64
	sink     *CSFreezerSink
}

func NewCSFreezerWriter(tx kv.RwTx, blockNum uint64, sink *CSFreezerSink) *CSFreezerWriter {
	return &CSFreezerWriter{
		ChangeSetWriter: state.NewChangeSetWriterPlain(tx, blockNum),
		tx:              tx,
		blockNum:        blockNum,
		sink:            sink,
	}
}

func (w *CSFreezerWriter) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	if len(code) == 0 {
		return nil
	}
	return w.tx.Put(modules.Code, codeHash[:], code)
}

// WriteChangeSets encodes the block's V2 blobs and queues them on the sink.
// Post-block values are read from the hashed tables — IntermediateRoot has
// already written the forward state in this same tx by the time the adapter
// calls this (see executePayloadDetailed ordering).
func (w *CSFreezerWriter) WriteChangeSets() error {
	accCS, err := w.GetAccountChanges()
	if err != nil {
		return err
	}
	stoCS, err := w.GetStorageChanges()
	if err != nil {
		return err
	}
	accBlob := EncodeAccountChanges(accCS, func(addr types.Address) []byte {
		v, _ := w.tx.GetOne(modules.HashedAccounts, crypto.Keccak256(addr[:]))
		if len(v) == 0 {
			return nil
		}
		return v
	})
	stoBlob := EncodeStorageChanges(stoCS, func(addr types.Address, slot types.Hash) []byte {
		var comp [64]byte
		copy(comp[:32], crypto.Keccak256(addr[:]))
		copy(comp[32:], crypto.Keccak256(slot[:]))
		v, _ := w.tx.GetOne(modules.HashedStorage, comp[:])
		return v
	})
	return w.sink.Add(w.blockNum, accBlob, stoBlob)
}

// WriteHistory is a no-op: MDBX history indexes are not maintained on the
// live path (archive data layout — history is offline-derived to freezer
// segments).
func (w *CSFreezerWriter) WriteHistory() error { return nil }

// BuildRetainListMerged builds the staged-Merkle dirty RetainList for blocks
// [from,to], reading acctcs/storcs freezer blobs where covered and the legacy
// MDBX AccountChangeSet/StorageChangeSet for older blocks. Marker semantics
// match commitment.BuildRetainListFromChangesets exactly.
func BuildRetainListMerged(tx kv.Tx, fz *freezer.Freezer, from, to uint64) (*trie.RetainList, int, int, error) {
	rl := trie.NewRetainList(0)
	seenAcc := make(map[types.Address]struct{})
	seenSto := make(map[string]struct{})

	addAcc := func(addr types.Address) {
		if _, ok := seenAcc[addr]; ok {
			return
		}
		seenAcc[addr] = struct{}{}
		rl.AddKeyWithMarker(crypto.Keccak256(addr[:]), true)
	}
	addSto := func(compositePlain []byte) { // addr(20) || slot(32)
		ck := string(compositePlain)
		if _, ok := seenSto[ck]; ok {
			return
		}
		seenSto[ck] = struct{}{}
		var comp [64]byte
		copy(comp[:32], crypto.Keccak256(compositePlain[:length.Addr]))
		copy(comp[32:], crypto.Keccak256(compositePlain[length.Addr:length.Addr+length.Hash]))
		rl.AddKeyWithMarker(comp[:], true)
	}

	// Freezer coverage [csStart, csHead) — both tables are appended in
	// lockstep by the sink, so acctcs bounds stand for both.
	var csStart, csHead uint64
	var acct, stor *freezer.FreezerTable
	if fz != nil {
		acct = fz.Table(freezer.TableAccountChanges)
		stor = fz.Table(freezer.TableStorageChanges)
		if acct != nil && stor != nil {
			csStart, csHead = acct.StartItem(), acct.Items()
		}
	}

	for blk := from; blk <= to; blk++ {
		if acct != nil && blk >= csStart && blk < csHead {
			accBlob, err := acct.Retrieve(blk)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("retain: acctcs %d: %w", blk, err)
			}
			changes, err := DecodeAccountChanges(accBlob)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("retain: decode acctcs %d: %w", blk, err)
			}
			for i := range changes {
				addAcc(changes[i].Address)
			}
			stoBlob, err := stor.Retrieve(blk)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("retain: storcs %d: %w", blk, err)
			}
			sChanges, err := DecodeStorageChanges(stoBlob)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("retain: decode storcs %d: %w", blk, err)
			}
			for i := range sChanges {
				addSto(sChanges[i].CompositeKey)
			}
			continue
		}
		// Legacy MDBX changesets for pre-sink blocks.
		if err := changeset.ForRange(tx, modules.AccountChangeSet, blk, blk+1, func(_ uint64, k, _ []byte) error {
			if len(k) >= length.Addr {
				var addr types.Address
				copy(addr[:], k[:length.Addr])
				addAcc(addr)
			}
			return nil
		}); err != nil {
			return nil, 0, 0, err
		}
		if err := changeset.ForRange(tx, modules.StorageChangeSet, blk, blk+1, func(_ uint64, k, _ []byte) error {
			if len(k) >= length.Addr+length.Hash {
				addSto(k[:length.Addr+length.Hash])
			}
			return nil
		}); err != nil {
			return nil, 0, 0, err
		}
	}
	return rl, len(seenAcc), len(seenSto), nil
}
