package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
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
		heads, err := readQMDBProofHeads(nil, tx)
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

// TestQMDBProofSurvivesTxRotation is the regression for the cached-tree /
// expired-tx bug: the provider caches the loaded forest across requests, but
// each RPC request runs in its own read tx that is rolled back afterwards. A
// second proof at the same head must re-point the tree's cold store at the new
// tx and succeed, not fault against the first (now-closed) tx.
func TestQMDBProofSurvivesTxRotation(t *testing.T) {
	db := newRealignTestDB(t)
	addr := types.HexToAddress("0x00000000000000000000000000000000000000aa")

	var root types.Hash
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		acct := account.NewAccount()
		acct.Nonce = 7
		acct.Balance = *uint256.NewInt(1_000)
		enc := make([]byte, acct.EncodingLengthForStorage())
		acct.EncodeForStorage(enc)
		if err := tx.Put(modules.Account, addr[:], enc); err != nil {
			return err
		}
		r, err := SeedQMDBGenesis(tx)
		if err != nil {
			return err
		}
		root = r
		hdr := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(1), Root: root}
		rawdb.WriteHeader(tx, hdr)
		if err := rawdb.WriteCanonicalHash(tx, hdr.Hash(), 1); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, hdr.Hash())
		return rawdb.WriteQMDBApplied(tx, 1, hdr.Hash())
	}); err != nil {
		t.Fatal(err)
	}

	p := NewQMDBStateProofProvider(nil)
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)

	// Serve two proofs, each in its own read tx that is rolled back — exactly
	// the RPC lifecycle. Before the fix the second call faulted on the first
	// call's closed tx.
	for i := 0; i < 2; i++ {
		if err := db.View(context.Background(), func(tx kv.Tx) error {
			out, err := p.AccountProof(tx, addr, latest)
			if err != nil {
				return err
			}
			if len(out) == 0 {
				t.Fatalf("proof %d: empty result for a present account", i)
			}
			return nil
		}); err != nil {
			t.Fatalf("proof %d: %v", i, err)
		}
	}
}

// seedQMDBProofChain writes one account, seeds the QMDB forest and records
// headers 0 and 1 (both carrying the seeded root) with 1 as committed and
// applied head. Returns the account address.
func seedQMDBProofChain(t *testing.T, db kv.RwDB) types.Address {
	t.Helper()
	addr := types.HexToAddress("0x00000000000000000000000000000000000000bb")
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		acct := account.NewAccount()
		acct.Nonce = 3
		acct.Balance = *uint256.NewInt(5)
		enc := make([]byte, acct.EncodingLengthForStorage())
		acct.EncodeForStorage(enc)
		if err := tx.Put(modules.Account, addr[:], enc); err != nil {
			return err
		}
		root, err := SeedQMDBGenesis(tx)
		if err != nil {
			return err
		}
		var parent types.Hash
		for n := uint64(0); n <= 1; n++ {
			hdr := &block.Header{Number: uint256.NewInt(n), Difficulty: uint256.NewInt(1), Root: root, ParentHash: parent}
			rawdb.WriteHeader(tx, hdr)
			if err := rawdb.WriteCanonicalHash(tx, hdr.Hash(), n); err != nil {
				return err
			}
			parent = hdr.Hash()
			if n == 1 {
				rawdb.WriteHeadBlockHash(tx, hdr.Hash())
				if err := rawdb.WriteQMDBApplied(tx, 1, hdr.Hash()); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestQMDBProofOutsideWindowIsExplicitError pins the fix for silent
// unverifiable output: a height the undo window cannot reach (and an
// unresolvable block) must surface as a StateProofUnavailableError carrying
// the provable range, never as an empty/placeholder proof.
func TestQMDBProofOutsideWindowIsExplicitError(t *testing.T) {
	db := newRealignTestDB(t)
	addr := seedQMDBProofChain(t, db)
	p := NewQMDBStateProofProvider(nil)

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		_, err := p.AccountProof(tx, addr, jsonrpc.BlockNumberOrHashWithNumber(0))
		var ue *StateProofUnavailableError
		if !errors.As(err, &ue) {
			t.Fatalf("height 0 (no undo record): err = %v, want StateProofUnavailableError", err)
		}
		if !ue.HasRequested || ue.Requested != 0 || !ue.HasRange || ue.Earliest != 1 || ue.Latest != 1 {
			t.Fatalf("unexpected error payload: %+v", ue)
		}
		if code := ue.ErrorCode(); code != -32000 {
			t.Fatalf("error code = %d", code)
		}
		_, err = p.StorageProof(tx, addr, types.Hash{}, jsonrpc.BlockNumberOrHashWithNumber(99))
		if !errors.As(err, &ue) {
			t.Fatalf("future height: err = %v, want StateProofUnavailableError", err)
		}
		r, err := p.ProvableRange(tx)
		if err != nil {
			return err
		}
		if r.Earliest != 1 || r.Latest != 1 {
			t.Fatalf("provable range = %+v, want 1..1", r)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d := p.Descriptor()
	if d.SupportsHistorical || d.HistoricalMode != StateProofHistoricalRecent || d.RecentWindow != QMDBUndoWindow() {
		t.Fatalf("descriptor overstates history support: %+v", d)
	}
}
