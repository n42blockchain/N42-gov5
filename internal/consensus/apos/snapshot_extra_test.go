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
	"crypto/ecdsa"
	"testing"

	lru "github.com/hashicorp/golang-lru"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

func newAposSigCache(t *testing.T) *lru.ARCCache {
	t.Helper()
	c, err := lru.NewARC(16)
	if err != nil {
		t.Fatalf("lru.NewARC: %v", err)
	}
	return c
}

func signAposHeader(t *testing.T, header *block.Header, key *ecdsa.PrivateKey) {
	t.Helper()
	if len(header.Extra) < extraSeal {
		header.Extra = append(header.Extra, make([]byte, extraSeal-len(header.Extra))...)
	}
	sighash, err := crypto.Sign(SealHash(header).Bytes(), key)
	if err != nil {
		t.Fatalf("crypto.Sign: %v", err)
	}
	copy(header.Extra[len(header.Extra)-extraSeal:], sighash)
}

func newAposVoteHeader(t *testing.T, number uint64, coinbase types.Address, authorize bool, key *ecdsa.PrivateKey) *block.Header {
	t.Helper()
	h := &block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(2),
		Coinbase: coinbase,
		// One byte beyond extraVanity+extraSeal so encodeSigHeader's strip
		// condition (len(extra) > extraVanity+extraSeal) applies consistently
		// both when signing (zero-filled seal) and when verifying (real seal).
		Extra: make([]byte, extraVanity+extraSeal+1),
	}
	if authorize {
		copy(h.Nonce[:], nonceAuthVote)
	} else {
		copy(h.Nonce[:], nonceDropVote)
	}
	signAposHeader(t, h, key)
	return h
}

func TestAposSnapshotValidVoteCastUncast(t *testing.T) {
	signer := types.HexToAddress("0x01")
	other1 := types.HexToAddress("0x02")
	other2 := types.HexToAddress("0x03")
	target := types.HexToAddress("0x04")
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 0, types.Hash{}, []types.Address{signer, other1, other2})

	if !snap.validVote(target, true) {
		t.Fatalf("expected authorize-vote on a non-signer to be valid")
	}
	if snap.validVote(signer, true) {
		t.Fatalf("expected authorize-vote on an existing signer to be invalid")
	}
	if !snap.cast(target, true) {
		t.Fatalf("expected cast to succeed")
	}
	if snap.Tally[target].Votes != 1 {
		t.Fatalf("expected 1 vote, got %d", snap.Tally[target].Votes)
	}
	if !snap.uncast(target, true) {
		t.Fatalf("expected uncast to succeed")
	}
	if _, ok := snap.Tally[target]; ok {
		t.Fatalf("expected tally entry removed after last vote uncast")
	}
	if snap.uncast(target, true) {
		t.Fatalf("expected uncast of dangling vote to fail")
	}
}

func TestAposSnapshotCopyIndependent(t *testing.T) {
	signer := types.HexToAddress("0x01")
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 0, types.Hash{}, []types.Address{signer})
	snap.cast(types.HexToAddress("0x02"), true)

	cpy := snap.copy()
	cpy.cast(types.HexToAddress("0x03"), true)
	if _, ok := snap.Tally[types.HexToAddress("0x03")]; ok {
		t.Fatalf("expected copy mutation isolated from original")
	}
}

func TestAposSnapshotSignersSortedAscending(t *testing.T) {
	a := types.HexToAddress("0x02")
	b := types.HexToAddress("0x01")
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 0, types.Hash{}, []types.Address{a, b})
	got := snap.signers()
	if len(got) != 2 || got[0] != b || got[1] != a {
		t.Fatalf("expected ascending signer order, got %v", got)
	}
}

func TestAposSnapshotInturnEmptySigners(t *testing.T) {
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 0, types.Hash{}, nil)
	if snap.inturn(0, types.HexToAddress("0x01")) {
		t.Fatalf("expected inturn false with no signers")
	}
}

func TestAposSnapshotApplyNoHeadersReturnsSelf(t *testing.T) {
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 5, types.Hash{}, nil)
	got, err := snap.apply(nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got != snap {
		t.Fatalf("expected apply with no headers to return the same snapshot")
	}
}

func TestAposSnapshotApplyRejectsNonSequentialChain(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 0, types.Hash{}, []types.Address{signer})

	h1 := newAposVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)
	h3 := newAposVoteHeader(t, 3, types.HexToAddress("0x02"), true, key)
	if _, err := snap.apply([]block.IHeader{h1, h3}); err == nil {
		t.Fatalf("expected error for non-sequential chain")
	}
}

// Note: with 3+ signers, apply() enforces errUnauthorizedSigner for an unknown
// signer (the single-node replay bypass only applies when len(Signers) <= 2).
func TestAposSnapshotApplyRejectsUnauthorizedSignerWithMultipleSigners(t *testing.T) {
	key, _ := crypto.GenerateKey()
	a := types.HexToAddress("0x01")
	b := types.HexToAddress("0x02")
	c := types.HexToAddress("0x03")
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 0, types.Hash{}, []types.Address{a, b, c})

	h := newAposVoteHeader(t, 1, types.HexToAddress("0x09"), true, key)
	if _, err := snap.apply([]block.IHeader{h}); err != errUnauthorizedSigner {
		t.Fatalf("expected errUnauthorizedSigner, got %v", err)
	}
}

func TestAposSnapshotApplySingleNodeBypassAllowsUnknownSigner(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := types.HexToAddress("0x01")
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 0, types.Hash{}, []types.Address{signer})

	// key's derived address is NOT `signer`, but since len(Signers) <= 2 the
	// replay bypass should allow it through rather than erroring.
	h := newAposVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)
	next, err := snap.apply([]block.IHeader{h})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if next.Number != 1 {
		t.Fatalf("expected snapshot advanced to block 1, got %d", next.Number)
	}
}

func TestAposSnapshotApplyAuthorizeVotePasses(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	newGuy := types.HexToAddress("0x05")
	snap := newSnapshot(&params.APosConfig{Period: 1, Epoch: 30000}, newAposSigCache(t), 0, types.Hash{}, []types.Address{signer})

	h := newAposVoteHeader(t, 1, newGuy, true, key)
	next, err := snap.apply([]block.IHeader{h})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := next.Signers[newGuy]; !ok {
		t.Fatalf("expected new signer authorized")
	}
}
