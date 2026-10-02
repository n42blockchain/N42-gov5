// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers ProcessBeaconBlockRootWithWriter's delegation (including its
// nil-input short circuits) and CollectPragueExecutionRequests' delegation
// to ProcessExecutionBlockEnd (nil-chainConfig short circuit).

package internal

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

func TestProcessBeaconBlockRootWithWriterNilInputsAreNoop(t *testing.T) {
	if err := ProcessBeaconBlockRootWithWriter(nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("ProcessBeaconBlockRootWithWriter(all nil) = %v, want nil", err)
	}
	root := types.HexToHash("0x1")
	if err := ProcessBeaconBlockRootWithWriter(&root, nil, nil, &block.Header{}, state.NewNoopWriter()); err != nil {
		t.Fatalf("ProcessBeaconBlockRootWithWriter(nil chainConfig) = %v, want nil", err)
	}
}

func TestCollectPragueExecutionRequestsNilChainConfigIsNoop(t *testing.T) {
	got, err := CollectPragueExecutionRequests(nil, nil, nil, nil, nil)
	if err != nil || got != nil {
		t.Fatalf("CollectPragueExecutionRequests(nil chainConfig) = (%v, %v), want (nil, nil)", got, err)
	}
}
