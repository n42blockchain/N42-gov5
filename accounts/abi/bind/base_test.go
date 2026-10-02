// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package bind

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

const testTransferEventABI = `[{
	"anonymous": false,
	"inputs": [
		{"indexed": true, "name": "from", "type": "address"},
		{"indexed": true, "name": "to", "type": "address"},
		{"indexed": false, "name": "value", "type": "uint256"}
	],
	"name": "Transfer",
	"type": "event"
}]`

func mustTransferABI(t *testing.T) abi.ABI {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(testTransferEventABI))
	if err != nil {
		t.Fatalf("abi.JSON failed: %v", err)
	}
	return parsed
}

func TestMetaDataGetAbi(t *testing.T) {
	md := &MetaData{ABI: testTransferEventABI}
	got, err := md.GetAbi()
	if err != nil {
		t.Fatalf("GetAbi: %v", err)
	}
	if _, ok := got.Events["Transfer"]; !ok {
		t.Fatal("expected Transfer event in parsed ABI")
	}

	// Second call should hit the cached path (m.ab != nil) and return the
	// same pointer.
	got2, err := md.GetAbi()
	if err != nil {
		t.Fatalf("GetAbi (cached): %v", err)
	}
	if got != got2 {
		t.Fatal("expected GetAbi to return the cached ABI pointer on second call")
	}

	bad := &MetaData{ABI: "{not valid json"}
	if _, err := bad.GetAbi(); err == nil {
		t.Fatal("expected error for malformed ABI JSON")
	}
}

func buildTransferLog(t *testing.T, parsed abi.ABI, from, to types.Address, value *big.Int) block.Log {
	t.Helper()
	ev := parsed.Events["Transfer"]

	nonIndexed := ev.Inputs.NonIndexed()
	packedData, err := nonIndexed.Pack(value)
	if err != nil {
		t.Fatalf("pack event data: %v", err)
	}

	var fromTopic, toTopic types.Hash
	copy(fromTopic[12:], from[:])
	copy(toTopic[12:], to[:])

	return block.Log{
		Topics: []types.Hash{ev.ID, fromTopic, toTopic},
		Data:   packedData,
	}
}

func TestUnpackLogSuccess(t *testing.T) {
	parsed := mustTransferABI(t)
	c := NewBoundContract(types.Address{}, parsed, nil, nil, nil)

	from := types.Address{1}
	to := types.Address{2}
	value := big.NewInt(42)
	log := buildTransferLog(t, parsed, from, to, value)

	var out struct {
		From  types.Address
		To    types.Address
		Value *big.Int
	}
	if err := c.UnpackLog(&out, "Transfer", log); err != nil {
		t.Fatalf("UnpackLog: %v", err)
	}
	if out.From != from || out.To != to {
		t.Fatalf("UnpackLog addresses mismatch: got from=%v to=%v", out.From, out.To)
	}
	if out.Value.Cmp(value) != 0 {
		t.Fatalf("UnpackLog value mismatch: got %v want %v", out.Value, value)
	}
}

func TestUnpackLogIntoMapSuccess(t *testing.T) {
	parsed := mustTransferABI(t)
	c := NewBoundContract(types.Address{}, parsed, nil, nil, nil)

	from := types.Address{3}
	to := types.Address{4}
	value := big.NewInt(7)
	log := buildTransferLog(t, parsed, from, to, value)

	out := make(map[string]interface{})
	if err := c.UnpackLogIntoMap(out, "Transfer", log); err != nil {
		t.Fatalf("UnpackLogIntoMap: %v", err)
	}
	if out["from"] != from || out["to"] != to {
		t.Fatalf("UnpackLogIntoMap addresses mismatch: %+v", out)
	}
}

func TestUnpackLogValidationErrors(t *testing.T) {
	parsed := mustTransferABI(t)
	c := NewBoundContract(types.Address{}, parsed, nil, nil, nil)

	// No topics at all.
	if err := c.UnpackLog(&struct{}{}, "Transfer", block.Log{}); err == nil {
		t.Error("expected error for log with no topics")
	}

	// Event not present in ABI.
	someHash := types.Hash{1}
	if err := c.UnpackLog(&struct{}{}, "NoSuchEvent", block.Log{Topics: []types.Hash{someHash}}); err == nil {
		t.Error("expected error for unknown event name")
	}

	// Topic0 doesn't match the event's ID.
	ev := parsed.Events["Transfer"]
	wrongTopic := ev.ID
	wrongTopic[0] ^= 0xff
	if err := c.UnpackLog(&struct{}{}, "Transfer", block.Log{Topics: []types.Hash{wrongTopic}}); err == nil {
		t.Error("expected error for event signature mismatch")
	}
}

func TestEnsureContext(t *testing.T) {
	if ctx := ensureContext(nil); ctx == nil {
		t.Fatal("ensureContext(nil) should return a non-nil context")
	}
	type ctxKey string
	custom := context.WithValue(context.Background(), ctxKey("k"), "v")
	if got := ensureContext(custom); got != custom {
		t.Fatal("ensureContext should pass through a non-nil context unchanged")
	}
}
