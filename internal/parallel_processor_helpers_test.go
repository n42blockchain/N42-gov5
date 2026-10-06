// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers small pure helpers in parallel_processor.go (parallelWorkers'
// env override, the takeTxResults/giveTxResults free-list, deferredFeeRecipients,
// touchesAny) and StateProcessor.LastPhases/SetSlotRecorder, plus
// DeriveShaV2's delegation.

package internal

import (
	"os"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestParallelWorkersEnvOverride(t *testing.T) {
	prev, had := os.LookupEnv("N42_PARALLEL_WORKERS")
	defer func() {
		if had {
			os.Setenv("N42_PARALLEL_WORKERS", prev)
		} else {
			os.Unsetenv("N42_PARALLEL_WORKERS")
		}
	}()

	os.Unsetenv("N42_PARALLEL_WORKERS")
	if got := parallelWorkers(); got != 32 {
		t.Fatalf("parallelWorkers() unset = %d, want 32", got)
	}
	os.Setenv("N42_PARALLEL_WORKERS", "8")
	if got := parallelWorkers(); got != 8 {
		t.Fatalf("parallelWorkers() = %d, want 8", got)
	}
	os.Setenv("N42_PARALLEL_WORKERS", "not-a-number")
	if got := parallelWorkers(); got != 32 {
		t.Fatalf("parallelWorkers() invalid = %d, want fallback 32", got)
	}
	os.Setenv("N42_PARALLEL_WORKERS", "-5")
	if got := parallelWorkers(); got != 32 {
		t.Fatalf("parallelWorkers() negative = %d, want fallback 32", got)
	}
}

func TestTakeAndGiveTxResultsReusesCapacity(t *testing.T) {
	r := takeTxResults(4)
	if len(r) != 4 {
		t.Fatalf("takeTxResults(4) len = %d, want 4", len(r))
	}
	r[0].gasUsed = 21000
	giveTxResults(r)

	// A subsequent take for a smaller size should reuse the freed capacity
	// (and the slot must have been cleared).
	r2 := takeTxResults(2)
	if len(r2) != 2 {
		t.Fatalf("takeTxResults(2) len = %d, want 2", len(r2))
	}
	if r2[0].gasUsed != 0 {
		t.Fatalf("takeTxResults() returned a dirty slot: gasUsed = %d, want 0 (cleared)", r2[0].gasUsed)
	}
	giveTxResults(r2)

	// A request too large for any freed slice falls back to a fresh allocation.
	r3 := takeTxResults(1000)
	if len(r3) != 1000 {
		t.Fatalf("takeTxResults(1000) len = %d, want 1000", len(r3))
	}
	giveTxResults(r3)
}

func TestDeferredFeeRecipientsDeduplicatesCollector(t *testing.T) {
	coinbase := types.HexToAddress("0xc0ffee")
	header := &block.Header{Coinbase: coinbase}

	// No fee collector configured: just the coinbase.
	got := deferredFeeRecipients(&params.ChainConfig{}, header)
	if len(got) != 1 || got[0] != coinbase {
		t.Fatalf("deferredFeeRecipients(no collector) = %v, want [%s]", got, coinbase.Hex())
	}

	// Distinct collector: both appear.
	collector := types.HexToAddress("0xdead")
	cfg := &params.ChainConfig{Eip1559FeeCollector: &collector}
	got = deferredFeeRecipients(cfg, header)
	if len(got) != 2 || got[0] != coinbase || got[1] != collector {
		t.Fatalf("deferredFeeRecipients(distinct collector) = %v, want [%s %s]", got, coinbase.Hex(), collector.Hex())
	}

	// Collector equal to the fee recipient: must not duplicate.
	cfgSame := &params.ChainConfig{Eip1559FeeCollector: &coinbase}
	got = deferredFeeRecipients(cfgSame, header)
	if len(got) != 1 {
		t.Fatalf("deferredFeeRecipients(collector == recipient) = %v, want a single entry", got)
	}
}

func TestTouchesAny(t *testing.T) {
	from := types.HexToAddress("0x1")
	to := types.HexToAddress("0x2")
	other := types.HexToAddress("0x3")
	txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)

	if !touchesAny([]*transaction.Transaction{txn}, []types.Address{from}) {
		t.Fatalf("touchesAny(matches sender) = false, want true")
	}
	if !touchesAny([]*transaction.Transaction{txn}, []types.Address{to}) {
		t.Fatalf("touchesAny(matches recipient) = false, want true")
	}
	if touchesAny([]*transaction.Transaction{txn}, []types.Address{other}) {
		t.Fatalf("touchesAny(no match) = true, want false")
	}
	if touchesAny(nil, []types.Address{from}) {
		t.Fatalf("touchesAny(no txs) = true, want false")
	}
}

func TestStateProcessorLastPhasesAndSlotRecorder(t *testing.T) {
	p := &StateProcessor{}
	if got := p.LastPhases(); got != (ProcessPhases{}) {
		t.Fatalf("LastPhases() before any Process() = %+v, want zero value", got)
	}

	want := ProcessPhases{Exec: 7}
	p.lastPhases.Store(&want)
	if got := p.LastPhases(); got != want {
		t.Fatalf("LastPhases() = %+v, want %+v", got, want)
	}

	// SetSlotRecorder has no observable getter; this only confirms it does
	// not panic and accepts a nil recorder (the common "disabled" case).
	p.SetSlotRecorder(nil)
	if p.slotRecorder != nil {
		t.Fatalf("SetSlotRecorder(nil) left a non-nil recorder")
	}
}

func TestDeriveShaV2DelegatesToCommonHash(t *testing.T) {
	list := transaction.Transactions{}
	got := DeriveShaV2(list)
	want := hash.DeriveShaV2(list)
	if got != want {
		t.Fatalf("DeriveShaV2(empty) = %s, want %s (delegate to common/hash)", got.Hex(), want.Hex())
	}
}
