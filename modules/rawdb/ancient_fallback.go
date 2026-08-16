// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Era-store read fallback for the qs native chain. The node registers a
// read-only ancientera.Store at boot (SetAncientStore); the low-level
// rawdb readers then transparently serve sealed ranges from era files
// when the hot MDBX no longer holds them. Pruned/degraded ranges keep
// returning "not found" exactly like a pruned chain — callers never see
// era internals.
//
// Only canonical data exists in eras: requests for non-canonical hashes
// in sealed ranges miss (HotStuff finality makes those unreachable).

package rawdb

import (
	"encoding/binary"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
)

var ancientStorePtr atomic.Pointer[ancientera.Store]

// SetAncientStore registers the node's era store (nil to detach).
func SetAncientStore(s *ancientera.Store) {
	if s == nil {
		ancientStorePtr.Store(nil)
		return
	}
	ancientStorePtr.Store(s)
}

// AncientStore returns the registered era store, or nil.
func AncientStore() *ancientera.Store {
	return ancientStorePtr.Load()
}

// ancientSealed reports whether the era store covers block num.
func ancientSealed(num uint64) *ancientera.Store {
	s := AncientStore()
	if s == nil || num >= s.SealedEnd() {
		return nil
	}
	return s
}

// ancientHeaderRAW serves ReadHeaderRAW misses. Returns nil unless the
// requested hash is the canonical hash of the sealed block.
func ancientHeaderRAW(hash types.Hash, number uint64) []byte {
	s := ancientSealed(number)
	if s == nil {
		return nil
	}
	ch, hdr, _, err := s.Chain(number)
	if err != nil || types.Hash(ch) != hash {
		return nil
	}
	return hdr
}

// ancientCanonicalBody serves ReadCanonicalBodyWithTransactions misses:
// rebuilds the full canonical body (real transactions only) from the
// exec era. The hash must match the hot DB's canonical hash — the
// CanonicalHeader table is always kept in full.
func ancientCanonicalBody(db kv.Getter, hash types.Hash, number uint64) *block.Body {
	s := ancientSealed(number)
	if s == nil {
		return nil
	}
	canonical, err := ReadCanonicalHash(db, number)
	if err != nil || canonical != hash {
		return nil
	}
	rec, err := s.Exec(number)
	if err != nil {
		if !ancientera.IsPruned(err) {
			log.Warn("ancient exec read failed", "block", number, "err", err)
		}
		return nil
	}
	if len(rec.BodyRaw) < 12 {
		return nil
	}
	txAmount := binary.BigEndian.Uint32(rec.BodyRaw[8:])
	if txAmount < 2 || uint32(len(rec.Txs)) != txAmount-2 {
		log.Warn("ancient body tx count mismatch", "block", number,
			"stored", len(rec.Txs), "expect", int64(txAmount)-2)
		return nil
	}
	body := new(block.Body)
	if len(rec.Txs) > 0 {
		body.Txs = make([]*transaction.Transaction, 0, len(rec.Txs))
		for i, raw := range rec.Txs {
			tx, err := decodeStoredTransaction(raw)
			if err != nil {
				log.Warn("ancient tx decode failed", "block", number, "idx", i, "err", err)
				return nil
			}
			body.Txs = append(body.Txs, tx)
		}
	}
	// Verifiers/Rewards live in tables the seal pipeline retains in hot
	// MDBX; the body must carry them — rewards are committed in the
	// header (WithdrawalsHash), so dropping them here would serve bodies
	// that no longer match their header commitment.
	if verifies, err := ReadVerifies(db, hash, number); err == nil {
		body.Verifiers = verifies
	} else {
		log.Trace("ancient body: skipping verifiers", "block", number, "err", err)
	}
	if rewards, err := ReadRewards(db, hash, number); err == nil {
		body.Rewards = rewards
	} else {
		log.Trace("ancient body: skipping rewards", "block", number, "err", err)
	}
	return body
}

// ancientHasBody reports body existence for HasBlock. Only the canonical
// hash of a sealed block has a body — a has-true for any other hash
// would invert the HasBlock/ReadBlock contract.
func ancientHasBody(hash types.Hash, number uint64) bool {
	s := ancientSealed(number)
	if s == nil {
		return false
	}
	if s.State(ancientera.ClassExec, number) != ancientera.RangeAvailable {
		return false
	}
	ch, _, _, err := s.Chain(number)
	return err == nil && types.Hash(ch) == hash
}

// ancientHasHeader serves HasHeader for sealed canonical blocks.
func ancientHasHeader(hash types.Hash, number uint64) bool {
	s := ancientSealed(number)
	if s == nil {
		return false
	}
	ch, _, _, err := s.Chain(number)
	return err == nil && types.Hash(ch) == hash
}

// ancientRawReceipts serves ReadRawReceipts misses.
func ancientRawReceipts(number uint64) block.Receipts {
	s := ancientSealed(number)
	if s == nil {
		return nil
	}
	rec, err := s.Exec(number)
	if err != nil || len(rec.ReceiptsRaw) == 0 {
		return nil
	}
	var receipts block.Receipts
	if err := receipts.Unmarshal(rec.ReceiptsRaw); err != nil {
		log.Warn("ancient receipts decode failed", "block", number, "err", err)
		return nil
	}
	return receipts
}

// AncientEarliestExecBlock exposes the era store's earliest available
// execution block for the P2P range-request gate (0 = all available or
// no store attached).
func AncientEarliestExecBlock() uint64 {
	s := AncientStore()
	if s == nil {
		return 0
	}
	return s.EarliestExecBlock()
}

// ancientEvidenceRAW serves ReadConsensusEvidence misses.
func ancientEvidenceRAW(number uint64) []byte {
	s := ancientSealed(number)
	if s == nil {
		return nil
	}
	_, _, ev, err := s.Chain(number)
	if err != nil {
		return nil
	}
	return ev
}
