// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// HotStuffReconfigAPI exposes admin RPC methods for validator set changes.
// Wraps a closure returning the active hotstuff.HotStuff engine so the
// API remains usable even when consensus is not currently running.
// ProposeAddValidator ingests a 48-byte BLS12-381 public key in hex and
// enqueues the change to take effect at the next epoch boundary.

package api

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/n42blockchain/N42/crypto/bls/blst"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

// HotStuffReconfigAPI provides admin RPC methods for proposing validator
// set changes in the HotStuff-2 BFT consensus engine.
type HotStuffReconfigAPI struct {
	getEngine func() *hotstuff.HotStuff
}

// NewHotStuffReconfigAPI creates the reconfig API.
// getEngine is a closure that returns the HotStuff engine (nil if not available).
func NewHotStuffReconfigAPI(getEngine func() *hotstuff.HotStuff) *HotStuffReconfigAPI {
	return &HotStuffReconfigAPI{getEngine: getEngine}
}

// ProposeAddValidator proposes adding a new validator at the next epoch boundary.
// blsPubKeyHex is the 48-byte BLS12-381 public key in hex (with or without 0x prefix).
// The change takes effect only after it is committed by the current validator set.
func (api *HotStuffReconfigAPI) ProposeAddValidator(ctx context.Context, address types.Address, blsPubKeyHex string) error {
	hs := api.getEngine()
	if hs == nil || hs.Engine() == nil {
		return fmt.Errorf("HotStuff consensus engine not available")
	}
	rm := hs.Engine().ReconfigManager()
	if rm == nil {
		return fmt.Errorf("ReconfigurationManager not initialized")
	}

	keyHex := strings.TrimPrefix(blsPubKeyHex, "0x")
	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return fmt.Errorf("invalid BLS public key hex: %w", err)
	}
	if len(keyBytes) != 48 {
		return fmt.Errorf("BLS public key must be 48 bytes, got %d", len(keyBytes))
	}
	pubKey, err := blst.PublicKeyFromBytes(keyBytes)
	if err != nil {
		return fmt.Errorf("invalid BLS public key: %w", err)
	}

	return rm.ProposeAddValidator(address, pubKey)
}

// ProposeRemoveValidator proposes removing a validator at the next epoch boundary.
func (api *HotStuffReconfigAPI) ProposeRemoveValidator(ctx context.Context, address types.Address) error {
	hs := api.getEngine()
	if hs == nil || hs.Engine() == nil {
		return fmt.Errorf("HotStuff consensus engine not available")
	}
	rm := hs.Engine().ReconfigManager()
	if rm == nil {
		return fmt.Errorf("ReconfigurationManager not initialized")
	}
	return rm.ProposeRemoveValidator(address)
}

// PendingReconfigChanges returns the current pending reconfiguration state.
func (api *HotStuffReconfigAPI) PendingReconfigChanges(ctx context.Context) map[string]interface{} {
	hs := api.getEngine()
	if hs == nil || hs.Engine() == nil {
		return map[string]interface{}{"error": "HotStuff not available"}
	}
	rm := hs.Engine().ReconfigManager()
	if rm == nil {
		return map[string]interface{}{"error": "ReconfigurationManager not initialized"}
	}
	return map[string]interface{}{
		"pendingAdds":    rm.PendingAddCount(),
		"pendingRemoves": rm.PendingRemoveCount(),
		"committed":      rm.IsCommitted(),
		"hasPending":     rm.HasPendingChanges(),
	}
}
