package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestReadQMDBProofHeadsSeparatesAppliedFromCommitted(t *testing.T) {
	db := newRealignTestDB(t)
	committed := &block.Header{
		Number:     uint256.NewInt(9),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x09},
	}
	applied := &block.Header{
		Number:     uint256.NewInt(10),
		ParentHash: committed.Hash(),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x10},
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, committed)
		rawdb.WriteHeader(tx, applied)
		if err := rawdb.WriteCanonicalHash(tx, committed.Hash(), 9); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, committed.Hash())
		return rawdb.WriteQMDBApplied(tx, 10, applied.Hash())
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		heads, err := readQMDBProofHeads(tx)
		if err != nil {
			return err
		}
		if heads.committed != 9 || heads.applied != 10 {
			t.Fatalf("heads = committed %d, applied %d; want 9, 10", heads.committed, heads.applied)
		}
		if heads.appliedRoot != applied.Root {
			t.Fatalf("applied root = %s, want %s", heads.appliedRoot, applied.Root)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveQMDBProofTargetRejectsNonCanonicalHash(t *testing.T) {
	db := newRealignTestDB(t)
	canonical := &block.Header{Number: uint256.NewInt(7), Difficulty: uint256.NewInt(1), Extra: []byte{1}}
	sibling := &block.Header{Number: uint256.NewInt(7), Difficulty: uint256.NewInt(1), Extra: []byte{2}}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, canonical)
		rawdb.WriteHeader(tx, sibling)
		return rawdb.WriteCanonicalHash(tx, canonical.Hash(), 7)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		canonicalTarget := jsonrpc.BlockNumberOrHashWithHash(canonical.Hash(), true)
		if n, ok := resolveProofTarget(tx, canonicalTarget, 7); !ok || n != 7 {
			t.Fatalf("canonical target = %d, %v; want 7, true", n, ok)
		}
		siblingTarget := jsonrpc.BlockNumberOrHashWithHash(sibling.Hash(), false)
		if n, ok := resolveProofTarget(tx, siblingTarget, 7); ok {
			t.Fatalf("non-canonical sibling target unexpectedly resolved to %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
