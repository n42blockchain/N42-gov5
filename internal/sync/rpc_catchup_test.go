// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestObservedBlockNumberPromotesLaterCommittedHash(t *testing.T) {
	svc := &Service{
		ctx:                 context.Background(),
		committedTargets:    make(map[types.Hash]struct{}),
		observedBlockNumber: make(map[types.Hash]uint64),
	}
	// Keep the queue owner occupied so promotion records the target without
	// entering the P2P range-fetch path this focused unit test does not wire.
	svc.catchUpInProgress.Store(true)

	hash := types.Hash{0x42}
	svc.observeCatchUpBlock(hash, 1234) // direct-push arrives before CommitQC
	svc.CatchUpToHash(hash)             // the exact hash is committed later

	deadline := time.Now().Add(time.Second)
	for svc.catchUpTarget.Load() != 1234 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := svc.catchUpTarget.Load(); got != 1234 {
		t.Fatalf("coalesced catch-up target = %d, want 1234", got)
	}
	svc.committedTargetLock.Lock()
	_, stillPending := svc.committedTargets[hash]
	svc.committedTargetLock.Unlock()
	if stillPending {
		t.Fatal("observed committed hash remained pending after promotion")
	}
}
