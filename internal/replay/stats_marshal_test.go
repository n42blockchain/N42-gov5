// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestStatsMarshalJSON(t *testing.T) {
	s := NewStats()
	s.FromBlock = 1
	s.ToBlock = 100
	s.CurrentBlock = 50
	s.BlocksProcessed.Store(49)
	s.TxTotal.Store(500)
	s.TxReplayed.Store(480)
	s.skipReason("evm_error")
	s.skipReason("evm_error")
	s.skipReason("unknown_reason") // not in the preset map: silently dropped

	data, err := s.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded["fromBlock"].(float64) != 1 {
		t.Fatalf("fromBlock = %v, want 1", decoded["fromBlock"])
	}
	if decoded["blocksProcessed"].(float64) != 49 {
		t.Fatalf("blocksProcessed = %v, want 49", decoded["blocksProcessed"])
	}
	skipReasons, ok := decoded["skipReasons"].(map[string]interface{})
	if !ok {
		t.Fatalf("skipReasons not a map: %v", decoded["skipReasons"])
	}
	if skipReasons["evm_error"].(float64) != 2 {
		t.Fatalf("skipReasons[evm_error] = %v, want 2", skipReasons["evm_error"])
	}
	if _, ok := decoded["elapsed"]; !ok {
		t.Fatal("elapsed field missing")
	}
}

func TestStatsSkipReasonUnknownIsNoop(t *testing.T) {
	s := NewStats()
	preset := len(s.SkipReasons)
	// Must not panic for a reason outside the preset map.
	s.skipReason("not_a_real_reason")
	if len(s.SkipReasons) != preset {
		t.Fatalf("SkipReasons grew to %d entries, want the preset %d", len(s.SkipReasons), preset)
	}
}

// Every class txErrorClass returns is a preset counter.
func TestTxErrorClassesArePreset(t *testing.T) {
	s := NewStats()
	for _, msg := range []string{
		"max fee per gas less than block base fee: address 0x1, maxFeePerGas: 1, baseFee: 7",
		"insufficient funds for gas * price + value",
		"nonce too high",
		"intrinsic gas too low",
		"gas limit reached",
		"transaction type not supported",
		"something else",
	} {
		c := txErrorClass(errors.New(msg))
		if _, ok := s.SkipReasons[c]; !ok {
			t.Fatalf("%q -> %q, not a preset counter", msg, c)
		}
	}
	if c := txErrorClass(errors.New("max fee per gas less than block base fee")); c != "err_fee_below_base_fee" {
		t.Fatalf("fee below base fee classified %q", c)
	}
}
