// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package apos

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

func newTestAPos(t *testing.T) *APos {
	t.Helper()
	db := memdb.NewTestDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 0}, db, params.TestChainConfig)
	apos, ok := eng.(*APos)
	if !ok {
		t.Fatalf("expected *APos")
	}
	return apos
}

func TestAposNewFillsDefaultEpoch(t *testing.T) {
	apos := newTestAPos(t)
	if apos.config.Epoch != epochLength {
		t.Fatalf("expected default epoch %d, got %d", epochLength, apos.config.Epoch)
	}
	if err := apos.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestAposTypeAndIsServiceTransaction(t *testing.T) {
	apos := newTestAPos(t)
	if apos.Type() != params.CliqueConsensus {
		t.Fatalf("expected CliqueConsensus type, got %v", apos.Type())
	}
	if apos.IsServiceTransaction(types.Address{}, nil) {
		t.Fatalf("expected IsServiceTransaction to always be false")
	}
}

func TestAposAuthorRecoversSigner(t *testing.T) {
	apos := newTestAPos(t)
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	h := newAposVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)

	got, err := apos.Author(h)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}
	if got != signer {
		t.Fatalf("expected recovered signer %v, got %v", signer, got)
	}
}

func TestAposCalcDifficultyPureFunction(t *testing.T) {
	a := types.HexToAddress("0x01")
	b := types.HexToAddress("0x02")
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 100}, newAposSigCache(t), 0, types.Hash{}, []types.Address{a, b})

	if got := calcDifficulty(snap, b); got.Cmp(diffInTurn) != 0 {
		t.Fatalf("expected in-turn difficulty for b, got %v", got)
	}
	if got := calcDifficulty(snap, a); got.Cmp(diffNoTurn) != 0 {
		t.Fatalf("expected no-turn difficulty for a, got %v", got)
	}
}

func TestAposSealHashMethodMatchesPackageFunc(t *testing.T) {
	apos := newTestAPos(t)
	key, _ := crypto.GenerateKey()
	h := newAposVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)
	if apos.SealHash(h) != SealHash(h) {
		t.Fatalf("expected method SealHash to match package SealHash")
	}
}

func TestAposSealHashAndProtoStableForSameHeader(t *testing.T) {
	key, _ := crypto.GenerateKey()
	h := newAposVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)
	if SealHash(h) != SealHash(h) {
		t.Fatalf("expected deterministic SealHash")
	}
	p1 := APosProto(h)
	p2 := APosProto(h)
	if len(p1) == 0 || string(p1) != string(p2) {
		t.Fatalf("expected deterministic non-empty proto bytes")
	}
}

func TestAposDbViewAndUpdate(t *testing.T) {
	apos := newTestAPos(t)
	var sawView, sawUpdate bool
	if err := apos.dbView(func(tx kv.Tx) error {
		sawView = true
		return nil
	}); err != nil {
		t.Fatalf("dbView: %v", err)
	}
	if err := apos.dbUpdate(func(tx kv.RwTx) error {
		sawUpdate = true
		return nil
	}); err != nil {
		t.Fatalf("dbUpdate: %v", err)
	}
	if !sawView || !sawUpdate {
		t.Fatalf("expected both dbView and dbUpdate callbacks invoked")
	}
}

func TestAposCountDepositorNoDeposits(t *testing.T) {
	apos := newTestAPos(t)
	if got := apos.CountDepositor(); got != 0 {
		t.Fatalf("expected 0 depositors on a fresh db, got %d", got)
	}
}

func TestAposAPIsNamespaceAuthenticated(t *testing.T) {
	apos := newTestAPos(t)
	apis := apos.APIs(nil)
	if len(apis) != 1 || apis[0].Namespace != "apos" || !apis[0].Authenticated {
		t.Fatalf("expected a single authenticated apos-namespaced API, got %+v", apis)
	}
}

func TestAposSetBlockChain(t *testing.T) {
	apos := newTestAPos(t)
	apos.SetBlockChain(nil)
	if apos.bc != nil {
		t.Fatalf("expected bc to be set to nil as passed")
	}
}

func TestEncodeSigHeaderRejectsNothingForShortExtra(t *testing.T) {
	// encodeSigHeader strips only when Extra is longer than vanity+seal; a short
	// Extra should still encode without panicking or erroring.
	h := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		Extra:      []byte{0x01},
	}
	if hash := SealHash(h); hash == (types.Hash{}) {
		t.Fatalf("expected non-zero SealHash for header with short extra")
	}
}
