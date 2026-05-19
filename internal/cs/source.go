package cs

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// ErrDeepReorg is returned when a Source cannot serve a block needed
// for the unwind range. Callers MUST treat this as fatal — silent
// no-op leaves PlainState in a partially-unwound state (the V4-class
// drift bug observed historically at e.g. block 12,501,844, fixed in
// ethel/reorg.go's pre-flight sanity check).
//
// Practical interpretation: a reorg has been requested whose target
// is older than the warm CS retention window. Recovery options:
//   1. Re-execute via EVM from a known snapshot at or before target.
//   2. Reload the full archive from a server's blake2b-verified bundle.
// Either is operator-level intervention; deep reorgs beyond the
// retention window are exceptional events (>> PoS finality).
var ErrDeepReorg = errors.New("cs: block outside source window — deep reorg requires snapshot reload or EVM re-execute")

// Source is the unified read interface for changeset data backing the
// Reorg (unwind) and forward-replay paths in internal/ethel.
//
// Implementations:
//   - FreezerSource: reads from the full acctcs/storcs freezer
//     (back-compat for callers that haven't adopted the warm tier)
//   - *Warm: reads from a pruned warm tier (cs.Warm + meta.json)
//     produced by cmd/n42-cs-prune; implements Source directly
//   - TieredSource: tries each underlying source in declared order;
//     ErrDeepReorg if none can serve the block
//
// Contract:
//   - RetrieveX returns (nil, nil) when the block exists in the source
//     range but has no changes for that domain (mirrors freezer.Retrieve)
//   - RetrieveX returns (_, ErrDeepReorg) if the block is outside the
//     source window
//   - RetrieveX returns (_, other_err) for I/O / decode failures
//   - Available(blk) is consistent: true iff RetrieveX would not return
//     ErrDeepReorg
type Source interface {
	RetrieveAccount(blk uint64) ([]byte, error)
	RetrieveStorage(blk uint64) ([]byte, error)
	Available(blk uint64) bool
	WindowDescription() string
}

// FreezerSource wraps a full freezer and exposes the entire item range
// [0, table.Items()) as available. This is the back-compat behavior
// for callers that have not adopted the warm tier — Reorg-via-freezer
// behaves identically to the pre-tier implementation.
type FreezerSource struct {
	fr *freezer.Freezer
}

// NewFreezerSource constructs a FreezerSource over a (typically
// read-write) freezer that already has acctcs + storcs tables open.
func NewFreezerSource(fr *freezer.Freezer) *FreezerSource {
	return &FreezerSource{fr: fr}
}

func (s *FreezerSource) RetrieveAccount(blk uint64) ([]byte, error) {
	tbl := s.fr.Table(freezer.TableAccountChanges)
	if tbl == nil {
		return nil, nil
	}
	if blk >= tbl.Items() {
		return nil, ErrDeepReorg
	}
	return tbl.Retrieve(blk)
}

func (s *FreezerSource) RetrieveStorage(blk uint64) ([]byte, error) {
	tbl := s.fr.Table(freezer.TableStorageChanges)
	if tbl == nil {
		return nil, nil
	}
	if blk >= tbl.Items() {
		return nil, ErrDeepReorg
	}
	return tbl.Retrieve(blk)
}

func (s *FreezerSource) Available(blk uint64) bool {
	tbl := s.fr.Table(freezer.TableAccountChanges)
	if tbl == nil {
		return false
	}
	return blk < tbl.Items()
}

func (s *FreezerSource) WindowDescription() string {
	tbl := s.fr.Table(freezer.TableAccountChanges)
	if tbl == nil {
		return "freezer(no acctcs)"
	}
	return fmt.Sprintf("freezer[0, %d)", tbl.Items())
}
