// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Persistent anchor log for the mobile attestation accumulator
// (docs/mobile-attestation-design.md §3.3). Each epoch's committed root
// is recorded under the epoch number — a durable, per-node,
// cross-checkable record of the registry's on-chain-anchorable roots.
// This is NOT the state-root-committed anchor (the consensus-path step is
// deferred); it is the honest persistent middle ground.

package rawdb

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// MobileAnchorRecord is one epoch's persisted accumulator anchor.
type MobileAnchorRecord struct {
	Epoch     uint64
	Root      types.Hash
	HeadBlock uint64
	TimeMs    uint64
}

const mobileAnchorValueLen = 32 + 8 + 8

// WriteMobileAnchor records an epoch's accumulator root.
func WriteMobileAnchor(tx kv.RwTx, rec MobileAnchorRecord) error {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], rec.Epoch)
	val := make([]byte, mobileAnchorValueLen)
	copy(val[:32], rec.Root[:])
	binary.BigEndian.PutUint64(val[32:40], rec.HeadBlock)
	binary.BigEndian.PutUint64(val[40:48], rec.TimeMs)
	return tx.Put(modules.MobileRegistryAnchors, key[:], val)
}

// ReadMobileAnchor returns the anchor recorded for an epoch, or ok=false.
func ReadMobileAnchor(tx kv.Tx, epoch uint64) (MobileAnchorRecord, bool) {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], epoch)
	val, err := tx.GetOne(modules.MobileRegistryAnchors, key[:])
	if err != nil || len(val) != mobileAnchorValueLen {
		return MobileAnchorRecord{}, false
	}
	rec := MobileAnchorRecord{Epoch: epoch}
	copy(rec.Root[:], val[:32])
	rec.HeadBlock = binary.BigEndian.Uint64(val[32:40])
	rec.TimeMs = binary.BigEndian.Uint64(val[40:48])
	return rec, true
}

// RecentMobileAnchors returns up to n most-recent anchors (highest epoch
// first) by scanning backward from the last key.
func RecentMobileAnchors(tx kv.Tx, n int) ([]MobileAnchorRecord, error) {
	c, err := tx.Cursor(modules.MobileRegistryAnchors)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out := make([]MobileAnchorRecord, 0, n)
	for k, v, err := c.Last(); k != nil; k, v, err = c.Prev() {
		if err != nil {
			return nil, err
		}
		if len(k) != 8 || len(v) != mobileAnchorValueLen {
			continue
		}
		rec := MobileAnchorRecord{Epoch: binary.BigEndian.Uint64(k)}
		copy(rec.Root[:], v[:32])
		rec.HeadBlock = binary.BigEndian.Uint64(v[32:40])
		rec.TimeMs = binary.BigEndian.Uint64(v[40:48])
		out = append(out, rec)
		if len(out) >= n {
			break
		}
	}
	return out, nil
}
