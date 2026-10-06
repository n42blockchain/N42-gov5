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
	"crypto/ecdsa"
	"testing"

	lru "github.com/hashicorp/golang-lru"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

func newSigCache(t *testing.T) *lru.ARCCache {
	t.Helper()
	c, err := lru.NewARC(16)
	if err != nil {
		t.Fatalf("lru.NewARC: %v", err)
	}
	return c
}

// signHeader signs the header's SealHash with key and appends the seal
// signature to Extra, mimicking what Apoa.Seal does in production.
func signHeader(t *testing.T, header *block.Header, key *ecdsa.PrivateKey) {
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

func newVoteHeader(t *testing.T, number uint64, coinbase types.Address, authorize bool, key *ecdsa.PrivateKey) *block.Header {
	t.Helper()
	h := &block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(2),
		Coinbase:   coinbase,
		Extra:      make([]byte, extraVanity),
	}
	if authorize {
		copy(h.Nonce[:], nonceAuthVote)
	} else {
		copy(h.Nonce[:], nonceDropVote)
	}
	signHeader(t, h, key)
	return h
}

func TestSnapshotCopyIsIndependent(t *testing.T) {
	signer := types.HexToAddress("0x01")
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, []types.Address{signer})
	snap.cast(types.HexToAddress("0x02"), true)

	cpy := snap.copy()
	cpy.cast(types.HexToAddress("0x03"), true)

	if _, ok := snap.Tally[types.HexToAddress("0x03")]; ok {
		t.Fatalf("expected copy mutation not to leak back into original")
	}
	if len(snap.Signers) != len(cpy.Signers) {
		t.Fatalf("expected signer sets to match in size")
	}
}

func TestSnapshotValidVoteCastUncast(t *testing.T) {
	signer := types.HexToAddress("0x01")
	target := types.HexToAddress("0x02")
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, []types.Address{signer})

	if !snap.validVote(target, true) {
		t.Fatalf("expected authorize-vote on a non-signer to be valid")
	}
	if snap.validVote(signer, true) {
		t.Fatalf("expected authorize-vote on an existing signer to be invalid")
	}

	if !snap.cast(target, true) {
		t.Fatalf("expected cast to succeed")
	}
	if snap.cast(signer, true) {
		t.Fatalf("expected cast on an already-authorized signer to be rejected")
	}
	if snap.Tally[target].Votes != 1 {
		t.Fatalf("expected 1 vote recorded, got %d", snap.Tally[target].Votes)
	}
	// Casting again should increment, not overwrite.
	snap.cast(target, true)
	if snap.Tally[target].Votes != 2 {
		t.Fatalf("expected 2 votes recorded, got %d", snap.Tally[target].Votes)
	}

	if !snap.uncast(target, true) {
		t.Fatalf("expected uncast to succeed")
	}
	if snap.Tally[target].Votes != 1 {
		t.Fatalf("expected 1 vote remaining, got %d", snap.Tally[target].Votes)
	}
	// Uncasting with the wrong authorize flag should fail.
	if snap.uncast(target, false) {
		t.Fatalf("expected uncast with mismatched authorize flag to fail")
	}
	// Uncasting a dangling address should fail.
	if snap.uncast(types.HexToAddress("0x99"), true) {
		t.Fatalf("expected uncast of unknown address to fail")
	}
	// Uncast the last vote: entry should be removed entirely.
	snap.uncast(target, true)
	if _, ok := snap.Tally[target]; ok {
		t.Fatalf("expected tally entry removed after last vote uncast")
	}
}

func TestSnapshotSignersSortedAscending(t *testing.T) {
	a := types.HexToAddress("0x02")
	b := types.HexToAddress("0x01")
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, []types.Address{a, b})
	got := snap.signers()
	if len(got) != 2 || got[0] != b || got[1] != a {
		t.Fatalf("expected signers sorted ascending, got %v", got)
	}
}

func TestSnapshotInturnEmptySigners(t *testing.T) {
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, nil)
	if snap.inturn(0, types.HexToAddress("0x01")) {
		t.Fatalf("expected inturn to be false with no signers (division-by-zero guard)")
	}
}

func TestSnapshotInturnRotation(t *testing.T) {
	a := types.HexToAddress("0x01")
	b := types.HexToAddress("0x02")
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, []types.Address{a, b})
	// signers() sorts ascending: [a, b] since 0x01 < 0x02.
	if !snap.inturn(0, a) {
		t.Fatalf("expected signer a in-turn at block 0")
	}
	if !snap.inturn(1, b) {
		t.Fatalf("expected signer b in-turn at block 1")
	}
}

func TestSnapshotApplyNoHeadersReturnsSelf(t *testing.T) {
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 5, types.Hash{}, nil)
	got, err := snap.apply(nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got != snap {
		t.Fatalf("expected apply with no headers to return the same snapshot")
	}
}

func TestSnapshotApplyRejectsNonSequentialChain(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, []types.Address{signer})

	h1 := newVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)
	h3 := newVoteHeader(t, 3, types.HexToAddress("0x02"), true, key) // gap: skips 2

	if _, err := snap.apply([]block.IHeader{h1, h3}); err == nil {
		t.Fatalf("expected error for non-sequential voting chain")
	}
}

func TestSnapshotApplyRejectsWrongStartNumber(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 5, types.Hash{}, []types.Address{signer})

	h := newVoteHeader(t, 1, types.HexToAddress("0x02"), true, key) // should be 6
	if _, err := snap.apply([]block.IHeader{h}); err == nil {
		t.Fatalf("expected error for wrong starting block number")
	}
}

func TestSnapshotApplyRejectsUnauthorizedSigner(t *testing.T) {
	key, _ := crypto.GenerateKey()
	other := types.HexToAddress("0x01") // different from key's derived address
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, []types.Address{other})

	h := newVoteHeader(t, 1, types.HexToAddress("0x02"), true, key)
	if _, err := snap.apply([]block.IHeader{h}); err != errUnauthorizedSigner {
		t.Fatalf("expected errUnauthorizedSigner, got %v", err)
	}
}

func TestSnapshotApplyAuthorizeVotePasses(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	newGuy := types.HexToAddress("0x02")
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, []types.Address{signer})

	h := newVoteHeader(t, 1, newGuy, true, key)
	next, err := snap.apply([]block.IHeader{h})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := next.Signers[newGuy]; !ok {
		t.Fatalf("expected new signer authorized after single-signer majority vote")
	}
	if next.Number != 1 {
		t.Fatalf("expected snapshot number advanced to 1, got %d", next.Number)
	}
}

func TestSnapshotApplyRejectsRecentlySigned(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	other := types.HexToAddress("0x03")
	snap := newSnapshot(&params.CliqueConfig{Period: 1, Epoch: 30000}, newSigCache(t), 0, types.Hash{}, []types.Address{signer, other})

	h1 := newVoteHeader(t, 1, types.HexToAddress("0x04"), true, key)
	h2 := newVoteHeader(t, 2, types.HexToAddress("0x05"), true, key) // same signer again, limit=2 -> recently signed
	if _, err := snap.apply([]block.IHeader{h1, h2}); err != errRecentlySigned {
		t.Fatalf("expected errRecentlySigned, got %v", err)
	}
}

func TestSnapshotStoreAndLoadRoundTrip(t *testing.T) {
	signer := types.HexToAddress("0x01")
	cfg := &params.CliqueConfig{Period: 1, Epoch: 30000}
	snap := newSnapshot(cfg, newSigCache(t), 3, types.HexToHash("0xabc"), []types.Address{signer})

	db := apoaTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, db)
	if err := snap.store(tx); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	roTx := memdb.BeginRw(t, db)
	loaded, err := loadSnapshot(cfg, newSigCache(t), roTx, types.HexToHash("0xabc"))
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if loaded.Number != 3 || len(loaded.Signers) != 1 {
		t.Fatalf("unexpected loaded snapshot: %+v", loaded)
	}
}
