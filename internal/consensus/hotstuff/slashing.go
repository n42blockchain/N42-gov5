// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Slashing detection and evidence persistence for HotStuff-2.
// Detects validator equivocation (two votes in the same view for
// different blocks) and surround-vote violations. Evidence is stored
// via modules/rawdb keyed by the offending validator and block height
// so governance can later apply slashing penalties on chain.

package hotstuff

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// EquivocationRecord is a persisted proof of double-voting.
type EquivocationRecord struct {
	View      ViewNumber
	Validator ValidatorIndex
	PrevHash  types.Hash
	NewHash   types.Hash
}

// LoadEquivocationEvidence reads all persisted equivocation evidence from MDBX.
func LoadEquivocationEvidence(tx kv.Tx) ([]EquivocationRecord, error) {
	c, err := tx.Cursor(modules.HotStuffState)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	prefix := []byte("equivocation/")
	var records []EquivocationRecord
	for k, v, err := c.Seek(prefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		key := string(k)
		if !strings.HasPrefix(key, "equivocation/") {
			break
		}
		if len(v) < 64 {
			continue
		}
		var view uint64
		var validator uint32
		if n, _ := fmt.Sscanf(key, "equivocation/%d/%d", &view, &validator); n != 2 {
			continue // skip malformed keys
		}

		var rec EquivocationRecord
		rec.View = ViewNumber(view)
		rec.Validator = ValidatorIndex(validator)
		copy(rec.PrevHash[:], v[:32])
		copy(rec.NewHash[:], v[32:64])
		records = append(records, rec)
	}
	return records, nil
}

// ClearEquivocationEvidence removes a specific evidence entry.
func ClearEquivocationEvidence(tx kv.RwTx, view ViewNumber, validator ValidatorIndex) error {
	key := fmt.Appendf(nil, "equivocation/%d/%d", view, validator)
	return tx.Delete(modules.HotStuffState, key)
}

// SlashingExecutor reads persisted equivocation evidence and slashes validator deposits.
type SlashingExecutor struct {
	db     kv.RwDB
	valSet func() *ValidatorSet
}

// NewSlashingExecutor creates a slashing executor.
func NewSlashingExecutor(db kv.RwDB, valSetFn func() *ValidatorSet) *SlashingExecutor {
	return &SlashingExecutor{db: db, valSet: valSetFn}
}

// ProcessPendingSlashing reads evidence, resolves addresses, and slashes deposits.
// Returns count of slashed validators.
func (se *SlashingExecutor) ProcessPendingSlashing(ctx context.Context) (int, error) {
	vs := se.valSet()
	if vs == nil {
		return 0, fmt.Errorf("validator set not available")
	}

	slashed := 0
	if err := se.db.Update(ctx, func(tx kv.RwTx) error {
		records, err := LoadEquivocationEvidence(tx)
		if err != nil {
			return err
		}
		for _, rec := range records {
			addr, err := vs.GetAddress(rec.Validator)
			if err != nil {
				log.Warn("slashing: validator index not found", "index", rec.Validator)
				continue
			}

			if err := rawdb.DeleteDeposit(tx, addr); err != nil {
				log.Error("slashing: failed to delete deposit", "addr", addr, "err", err)
				continue
			}

			// Record slash event for audit.
			slashKey := fmt.Appendf(nil, "slashed/%d/%d", rec.View, rec.Validator)
			var slashVal [8]byte
			binary.LittleEndian.PutUint64(slashVal[:], uint64(rec.View))
			if err := tx.Put(modules.HotStuffState, slashKey, slashVal[:]); err != nil {
				return err
			}

			if err := ClearEquivocationEvidence(tx, rec.View, rec.Validator); err != nil {
				return err
			}

			log.Warn("validator slashed for equivocation",
				"validator", addr.Hex(),
				"view", rec.View,
				"prevHash", rec.PrevHash.Hex(),
				"newHash", rec.NewHash.Hex())
			slashed++
		}
		return nil
	}); err != nil {
		return slashed, err
	}
	return slashed, nil
}
