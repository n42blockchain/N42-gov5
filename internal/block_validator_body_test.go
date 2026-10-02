// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers NewBlockValidator's constructor and ValidateBody's error paths not
// exercised by block_validator_test.go: known-block short circuit, tx-root
// mismatch, unknown ancestor, and pruned ancestor (parent stored but not
// canonical/applied).

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestNewBlockValidatorConstructor(t *testing.T) {
	cfg := &params.ChainConfig{}
	bc := &BlockChain{}
	v := NewBlockValidator(cfg, bc, nil)
	if v == nil || v.config != cfg || v.bc != bc || v.engine != nil {
		t.Fatalf("NewBlockValidator() = %+v, did not wire fields correctly", v)
	}
}

func TestValidateBodyKnownBlockShortCircuits(t *testing.T) {
	bc, _, child := newReaderTestChain(t)
	v := NewBlockValidator(&params.ChainConfig{}, bc, nil)

	if err := v.ValidateBody(child); err != ErrKnownBlock {
		t.Fatalf("ValidateBody(known block) = %v, want ErrKnownBlock", err)
	}
}

func TestValidateBodyTxRootMismatch(t *testing.T) {
	bc, genesis, _ := newReaderTestChain(t)
	v := NewBlockValidator(&params.ChainConfig{}, bc, nil)

	from := types.HexToAddress("0x1")
	to := types.HexToAddress("0x2")
	txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
	bad := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(2),
		ParentHash: genesis.Hash(),
		Difficulty: uint256.NewInt(1),
		TxHash:     types.HexToHash("0xbad"), // deliberately wrong
	}, []*transaction.Transaction{txn}).(*block.Block)

	err := v.ValidateBody(bad)
	if err == nil {
		t.Fatalf("ValidateBody(tx root mismatch) = nil, want an error")
	}
}

func TestValidateBodyGenesisShortCircuits(t *testing.T) {
	bc, genesis, _ := newReaderTestChain(t)
	v := NewBlockValidator(&params.ChainConfig{}, bc, nil)
	// genesis is already known/applied in this fixture, so build a distinct
	// unknown number-0 block (its own parent hash, no stored state) to reach
	// the `blockNum == 0` short circuit instead of the known-block one.
	g2 := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(1),
		Extra:      []byte{0x01},
		TxHash:     block.TxRootAt(nil, 0),
	}, nil).(*block.Block)
	if g2.Hash() == genesis.Hash() {
		t.Fatal("test setup: g2 collided with genesis hash")
	}
	if err := v.ValidateBody(g2); err != nil {
		t.Fatalf("ValidateBody(genesis-number block) = %v, want nil", err)
	}
}

func TestValidateBodyUnknownAncestor(t *testing.T) {
	bc, _, _ := newReaderTestChain(t)
	v := NewBlockValidator(&params.ChainConfig{}, bc, nil)

	orphan := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(50),
		ParentHash: types.HexToHash("0xneverexisted"),
		Difficulty: uint256.NewInt(1),
		TxHash:     block.TxRootAt(nil, 0),
	}, nil).(*block.Block)

	if err := v.ValidateBody(orphan); err != ErrUnknownAncestor {
		t.Fatalf("ValidateBody(orphan) = %v, want ErrUnknownAncestor", err)
	}
}

func TestValidateBodyPrunedAncestor(t *testing.T) {
	bc, genesis, _ := newReaderTestChain(t)
	v := NewBlockValidator(&params.ChainConfig{}, bc, nil)

	// A parent block that is stored (header+body) but never made canonical:
	// HasBlock(parent) is true, HasState(parent) (canonical-hash membership)
	// is false, and the nil engine makes canonicalByCommitOnly() false, so
	// ValidateBody must fall through to ErrPrunedAncestor.
	nonCanonicalParent := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(5),
		ParentHash: genesis.Hash(),
		Difficulty: uint256.NewInt(1),
		Extra:      []byte{0x02},
		TxHash:     block.TxRootAt(nil, 0),
	}, nil).(*block.Block)
	if err := bc.ChainDB.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteBlock(tx, nonCanonicalParent)
	}); err != nil {
		t.Fatal(err)
	}

	child := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(6),
		ParentHash: nonCanonicalParent.Hash(),
		Difficulty: uint256.NewInt(1),
		TxHash:     block.TxRootAt(nil, 0),
	}, nil).(*block.Block)

	if err := v.ValidateBody(child); err != ErrPrunedAncestor {
		t.Fatalf("ValidateBody(non-canonical parent) = %v, want ErrPrunedAncestor", err)
	}
}
