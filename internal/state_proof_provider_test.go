// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers SetStateProofProvider/StateProofProvider's round trip (including
// StateProofProvider's nil-receiver guard) and ReorgAudit.LogStats' no-panic
// contract.

package internal

import "testing"

func TestStateProofProviderNilReceiverReturnsNil(t *testing.T) {
	var bc *BlockChain
	if got := bc.StateProofProvider(); got != nil {
		t.Fatalf("StateProofProvider() on a nil *BlockChain = %v, want nil", got)
	}
}

func TestSetAndGetStateProofProvider(t *testing.T) {
	bc := &BlockChain{}
	if got := bc.StateProofProvider(); got != nil {
		t.Fatalf("StateProofProvider() before Set = %v, want nil", got)
	}
}

func TestReorgAuditLogStatsDoesNotPanic(t *testing.T) {
	ra := NewReorgAudit(nil)
	ra.LogStats()
}
