package api

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
)

func TestRPCMarshalHeaderAllowsNilNumberAndDifficulty(t *testing.T) {
	header := &block.Header{}

	fields := RPCMarshalHeader(header)
	if fields == nil {
		t.Fatal("RPCMarshalHeader() returned nil")
	}

	number, ok := fields["number"].(*hexutil.Big)
	if !ok || number == nil || number.ToInt().Sign() != 0 {
		t.Fatalf("RPCMarshalHeader() number = %#v", fields["number"])
	}

	difficulty, ok := fields["difficulty"].(*hexutil.Big)
	if !ok || difficulty == nil || difficulty.ToInt().Sign() != 0 {
		t.Fatalf("RPCMarshalHeader() difficulty = %#v", fields["difficulty"])
	}
}

func TestRPCMarshalHeaderPreservesExplicitValues(t *testing.T) {
	header := &block.Header{
		Number:     uint256.NewInt(11),
		Difficulty: uint256.NewInt(22),
	}

	fields := RPCMarshalHeader(header)
	if got := fields["number"].(*hexutil.Big).ToInt().Uint64(); got != 11 {
		t.Fatalf("RPCMarshalHeader() number = %d, want 11", got)
	}
	if got := fields["difficulty"].(*hexutil.Big).ToInt().Uint64(); got != 22 {
		t.Fatalf("RPCMarshalHeader() difficulty = %d, want 22", got)
	}
}
