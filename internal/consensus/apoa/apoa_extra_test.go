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

package apoa

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

func TestNewFillsDefaultEpoch(t *testing.T) {
	db := memdb.NewTestDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 0}, db)
	apoa, ok := eng.(*Apoa)
	if !ok {
		t.Fatalf("expected *Apoa")
	}
	if apoa.config.Epoch != epochLength {
		t.Fatalf("expected default epoch %d, got %d", epochLength, apoa.config.Epoch)
	}
	if err := apoa.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestApoaTypeAndIsServiceTransaction(t *testing.T) {
	db := memdb.NewTestDB(t)
	apoa := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	if apoa.Type() != params.CliqueConsensus {
		t.Fatalf("expected CliqueConsensus type, got %v", apoa.Type())
	}
	if apoa.IsServiceTransaction(types.Address{}, nil) {
		t.Fatalf("expected IsServiceTransaction to always be false")
	}
}

func TestApoaAuthorRecoversSigner(t *testing.T) {
	db := memdb.NewTestDB(t)
	apoa := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)

	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	h := newVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)

	got, err := apoa.Author(h)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}
	if got != signer {
		t.Fatalf("expected recovered signer %v, got %v", signer, got)
	}
}

func TestCalcDifficultyPureFunction(t *testing.T) {
	a := types.HexToAddress("0x01")
	b := types.HexToAddress("0x02")
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 100}, newSigCache(t), 0, types.Hash{}, []types.Address{a, b})

	// signers() ascending: [a, b]; inturn(1, b) should be true (1 % 2 == 1, offset of b is 1).
	if got := calcDifficulty(snap, b); got.Cmp(diffInTurn) != 0 {
		t.Fatalf("expected in-turn difficulty for b at next block, got %v", got)
	}
	if got := calcDifficulty(snap, a); got.Cmp(diffNoTurn) != 0 {
		t.Fatalf("expected no-turn difficulty for a at next block, got %v", got)
	}
}

func TestSealHashAndApoaProtoRejectMissingSignature(t *testing.T) {
	h := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		Extra:      []byte{0x01}, // too short for a signature
	}
	if hash := SealHash(h); hash != (types.Hash{}) {
		t.Fatalf("expected zero hash when signature is missing, got %v", hash)
	}
	if proto := ApoaProto(h); proto != nil {
		t.Fatalf("expected nil proto bytes when signature is missing")
	}
}

func TestSealHashStableForSameHeader(t *testing.T) {
	key, _ := crypto.GenerateKey()
	h := newVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)
	h1 := SealHash(h)
	h2 := SealHash(h)
	if h1 != h2 {
		t.Fatalf("expected SealHash to be deterministic for the same header")
	}
}

func TestApoaMethodSealHashMatchesPackageFunc(t *testing.T) {
	db := memdb.NewTestDB(t)
	apoa := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	key, _ := crypto.GenerateKey()
	h := newVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)
	if apoa.SealHash(h) != SealHash(h) {
		t.Fatalf("expected method SealHash to match package-level SealHash")
	}
}

func TestApoaAPIsNamespace(t *testing.T) {
	db := memdb.NewTestDB(t)
	apoa := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	apis := apoa.APIs(nil)
	if len(apis) != 1 || apis[0].Namespace != "apoa" {
		t.Fatalf("expected single apoa-namespaced API, got %+v", apis)
	}
}

func TestVerifyHeaderRejectsWrongType(t *testing.T) {
	db := memdb.NewTestDB(t)
	apoa := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	if err := apoa.VerifyHeader(nil, nilNumberBlockStub{header: &block.Header{}}.Header(), false); err == nil {
		t.Fatalf("expected error for header missing number")
	}
}
