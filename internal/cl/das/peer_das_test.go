// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.2.0 unit tests — NoopPeerDas interface adherence and the
// safe-default contract the forkchoice cherry-pick relies on.

//go:build n42el

package das

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
)

func TestNoopPeerDas_SatisfiesInterface(t *testing.T) {
	// Compile-time guard duplicated as a runtime spot-check.
	var _ PeerDas = NewNoopPeerDas(nil)
}

func TestNoopPeerDas_IsDataAvailable_AlwaysTrue(t *testing.T) {
	// Pre-Fusaka mainnet blocks have no DAS data; fork-choice MUST
	// accept them. Regression guard: if this ever returns false, the
	// fork-choice store will reject every block on a non-DAS chain.
	n := NewNoopPeerDas(nil)
	ok, err := n.IsDataAvailable(0, depcommon.Hash{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Errorf("NoopPeerDas.IsDataAvailable must return true pre-Fusaka")
	}
}

func TestNoopPeerDas_PassiveMethodsReturnSafeDefaults(t *testing.T) {
	n := NewNoopPeerDas(nil)
	if n.IsBlobAlreadyRecovered(depcommon.Hash{}) {
		t.Errorf("IsBlobAlreadyRecovered must return false")
	}
	if n.IsColumnOverHalf(0, depcommon.Hash{}) {
		t.Errorf("IsColumnOverHalf must return false")
	}
	if n.IsArchivedMode() {
		t.Errorf("IsArchivedMode must return false")
	}
	if err := n.Prune(64); err != nil {
		t.Errorf("Prune must be a no-op (err=%v)", err)
	}
	n.UpdateValidatorsCustody(1)
	n.SetForkChoice(nil)
}

func TestNoopPeerDas_ActiveMethodsReportNotWired(t *testing.T) {
	n := NewNoopPeerDas(nil)
	ctx := context.Background()

	if err := n.DownloadColumnsAndRecoverBlobs(ctx, nil); !errors.Is(err, errPeerDASNotWired) {
		t.Errorf("DownloadColumnsAndRecoverBlobs: %v, want errPeerDASNotWired", err)
	}
	if err := n.DownloadOnlyCustodyColumns(ctx, nil); !errors.Is(err, errPeerDASNotWired) {
		t.Errorf("DownloadOnlyCustodyColumns: %v, want errPeerDASNotWired", err)
	}
	if err := n.TryScheduleRecover(0, depcommon.Hash{}); !errors.Is(err, errPeerDASNotWired) {
		t.Errorf("TryScheduleRecover: %v, want errPeerDASNotWired", err)
	}
	if err := n.SyncColumnDataLater((*cltypes.SignedBeaconBlock)(nil)); !errors.Is(err, errPeerDASNotWired) {
		t.Errorf("SyncColumnDataLater: %v, want errPeerDASNotWired", err)
	}
}
