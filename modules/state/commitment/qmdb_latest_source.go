// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The head-state account source behind N42_STATE_WRITE_QMDB_ONLY=1.
//
// With it installed (modules.SetLatestAccountSource) the plain writer stops
// maintaining the address-keyed `Account` table for post-genesis blocks, and
// every latest read -- block execution, the miner's build, the txpool's nonce
// and balance, RPC `latest`, the history fallback for an account unchanged
// since the queried block -- is answered from the live QMDB tree through
// QMDBRootComputer.Lookup. The value is the same bytes the table held
// (round 21: zero mismatches in a million comparisons), filtered by the
// EIP-161 policy the table applies (qmdb_state_reader.go, emptyByPlainPolicy).
//
// What this mode does NOT serve, and therefore breaks, until each is moved
// off the table:
//
//   - snap-sync SERVING (internal/sync/rpc_snap.go iterates `Account`);
//   - the state-dump and rebuild tools that cursor the table
//     (cmd/n42 statecmd, rebuild_*, migratecmd, n42-state-verify);
//   - internal/snapshot's account count;
//   - the MPT root computer's reads (a chain committing with MPT never
//     installs this source; the node refuses the flag on one).
//
// Each sees a table frozen at the block the flag was first enabled. That is
// why this is a typed-out environment lever for a measurement round and not
// a config default: docs/QS_BLOCK_TIME_BUDGET.md round 26 is the measurement.

package commitment

import (
	"context"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// QMDBAccountFrozenAtKey is the QMDBMeta row holding the first block whose
// `Account` rows were never written. Set once, at the first enablement, and
// never advanced: a repair replays AccountChangeSet from here to the head.
var QMDBAccountFrozenAtKey = []byte("accountFrozenAt")

// QMDBOnlyAccountWrites reports N42_STATE_WRITE_QMDB_ONLY=1. Read once at
// node start; the node installs the source or refuses to start.
func QMDBOnlyAccountWrites() bool {
	switch os.Getenv("N42_STATE_WRITE_QMDB_ONLY") {
	case "1", "true", "TRUE", "yes", "on":
		return true
	}
	return false
}

// QMDBLatestAccountSource implements modules.AccountSource over the live
// tree of a QMDBRootComputer. db is opened for a fresh read transaction
// only when the caller's own transaction lags the head (see Lookup).
type QMDBLatestAccountSource struct {
	rc *QMDBRootComputer
	db kv.RoDB
}

// NewQMDBLatestAccountSource returns a source over rc's live tree.
func NewQMDBLatestAccountSource(rc *QMDBRootComputer, db kv.RoDB) *QMDBLatestAccountSource {
	return &QMDBLatestAccountSource{rc: rc, db: db}
}

// LatestAccount returns the encoded head-state account, or nil when the
// account does not exist by the plain table's definition.
func (s *QMDBLatestAccountSource) LatestAccount(g kv.Getter, addr []byte) ([]byte, error) {
	if len(addr) != types.AddressLength {
		return nil, fmt.Errorf("qmdb latest account: %d-byte key", len(addr))
	}
	kh := qmdb.Hash(AccountKeyHash(types.BytesToAddress(addr)))
	enc, found, evicted := s.rc.Lookup(kh, g)
	if !found && evicted {
		// The caller's transaction predates the flush that persisted this
		// entry. A transaction begun now sees it: the owner holds the lock
		// across commit and eviction, so under the read lock the index and
		// the store agree.
		if s.db == nil {
			return nil, fmt.Errorf("qmdb latest account: live entry evicted and no database to fault it from")
		}
		tx, err := s.db.BeginRo(context.Background())
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		enc, found, evicted = s.rc.Lookup(kh, tx)
		if !found && evicted {
			return nil, fmt.Errorf("qmdb latest account: live entry %x not recoverable from the store", kh[:8])
		}
	}
	if !found || len(enc) == 0 {
		return nil, nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	if emptyByPlainPolicy(&a) {
		return nil, nil // EIP-161 removed it from the table; report what the table would
	}
	return enc, nil
}

var _ modules.AccountSource = (*QMDBLatestAccountSource)(nil)

// LookupSource serves QMDBStateReader point reads for a reader on ANOTHER
// goroutine than the tree's owner: every read goes through
// QMDBRootComputer.Lookup under the reader lock and faults evicted entries
// through the caller's own transaction. It is what a Block-STM worker uses
// instead of the tree's raw Get (which faults through the owner's attached
// transaction and is not safe from a second goroutine).
type LookupSource struct {
	rc   *QMDBRootComputer
	cold kv.Getter
}

// NewLookupSource returns a source over rc for a reader holding cold, a read
// transaction opened on the calling goroutine after the last commit.
func NewLookupSource(rc *QMDBRootComputer, cold kv.Getter) *LookupSource {
	return &LookupSource{rc: rc, cold: cold}
}

// Get implements the reader's value source.
func (s *LookupSource) Get(keyHash qmdb.Hash) ([]byte, bool) {
	v, found, evicted := s.rc.Lookup(keyHash, s.cold)
	if !found && evicted {
		// Only possible with a transaction older than the last flush, which
		// a worker opened during execution cannot be; keep it visible.
		log.Warn("qmdb lookup source: live entry evicted and not visible to the reader's transaction", "keyHash", fmt.Sprintf("%x", keyHash[:8]))
	}
	return v, found
}
