// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestBundlerAPI_NilServiceGuards(t *testing.T) {
	api := NewBundlerAPI(nil)

	if _, err := api.SendUserOperation(context.Background(), SendUserOperationArgs{}, types.Address{}); err == nil {
		t.Fatal("SendUserOperation(nil service) error = nil, want error")
	}
	if _, err := api.EstimateUserOperationGas(context.Background(), SendUserOperationArgs{}, types.Address{}); err == nil {
		t.Fatal("EstimateUserOperationGas(nil service) error = nil, want error")
	}
	if _, err := api.GetUserOperationByHash(context.Background(), types.Hash{}); err == nil {
		t.Fatal("GetUserOperationByHash(nil service) error = nil, want error")
	}
	if got := api.SupportedEntryPoints(context.Background()); got != nil {
		t.Fatalf("SupportedEntryPoints(nil service) = %v, want nil", got)
	}
}

func TestSendUserOperationArgs_ToUserOperation(t *testing.T) {
	args := SendUserOperationArgs{Sender: types.Address{1}}
	op := args.toUserOperation()
	if op == nil {
		t.Fatal("toUserOperation() = nil")
	}
	if op.Sender != args.Sender {
		t.Fatalf("toUserOperation().Sender = %v, want %v", op.Sender, args.Sender)
	}
}
