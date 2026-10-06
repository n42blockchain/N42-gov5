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
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func TestAposVerifyUnclesAlwaysAccepts(t *testing.T) {
	apos := newTestAPos(t)
	if err := apos.VerifyUncles(nil, nil); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAposAuthorizeSetsSignerAndSignFn(t *testing.T) {
	apos := newTestAPos(t)
	signer := types.HexToAddress("0x01")
	called := false
	apos.Authorize(signer, func(accounts.Account, string, []byte) ([]byte, error) {
		called = true
		return nil, nil
	})
	if apos.signer != signer {
		t.Fatalf("expected signer set, got %v", apos.signer)
	}
	_, _ = apos.signFn(accounts.Account{}, "", nil)
	if !called {
		t.Fatalf("expected signFn to be invoked")
	}
}

func TestAposVerifyHeaderRejectsFutureTimestamp(t *testing.T) {
	apos := newTestAPos(t)
	h := &block.Header{
		Number: uint256.NewInt(1),
		Time:   uint64(time.Now().Add(time.Hour).Unix()),
		Extra:  make([]byte, extraVanity),
	}
	copy(h.Nonce[:], nonceDropVote)
	if err := apos.VerifyHeader(nil, h, false); err == nil {
		t.Fatalf("expected error for a header timestamped in the future")
	}
}

func TestAposVerifyHeaderRejectsGenesis(t *testing.T) {
	apos := newTestAPos(t)
	h := &block.Header{Number: uint256.NewInt(0), Extra: make([]byte, extraVanity)}
	if err := apos.VerifyHeader(nil, h, false); err != errUnknownBlock {
		t.Fatalf("expected errUnknownBlock for genesis, got %v", err)
	}
}

func TestAposVerifyHeaderRejectsBadNonce(t *testing.T) {
	apos := newTestAPos(t)
	h := &block.Header{
		Number: uint256.NewInt(1),
		Time:   1,
		Extra:  make([]byte, extraVanity),
	}
	// Nonce left as zero value, which is NOT nonceAuthVote (all 0xff) nor
	// nonceDropVote is all-zero... actually nonceDropVote IS all-zero, so use
	// a nonce that matches neither.
	h.Nonce[0] = 0x42
	if err := apos.VerifyHeader(nil, h, false); err != errInvalidVote {
		t.Fatalf("expected errInvalidVote, got %v", err)
	}
}

func TestAposVerifyHeaderRejectsMissingVanity(t *testing.T) {
	apos := newTestAPos(t)
	h := &block.Header{
		Number: uint256.NewInt(1),
		Time:   1,
		Extra:  []byte{0x01}, // too short
	}
	copy(h.Nonce[:], nonceDropVote)
	if err := apos.VerifyHeader(nil, h, false); err != errMissingVanity {
		t.Fatalf("expected errMissingVanity, got %v", err)
	}
}

func TestAposVerifyHeaderRejectsCheckpointNonzeroBeneficiary(t *testing.T) {
	apos := newTestAPos(t)
	h := &block.Header{
		Number:   uint256.NewInt(apos.config.Epoch), // checkpoint block
		Time:     1,
		Extra:    make([]byte, extraVanity),
		Coinbase: types.HexToAddress("0x01"),
	}
	copy(h.Nonce[:], nonceDropVote)
	if err := apos.VerifyHeader(nil, h, false); err != errInvalidCheckpointBeneficiary {
		t.Fatalf("expected errInvalidCheckpointBeneficiary, got %v", err)
	}
}

func TestAposSealRejectsGenesis(t *testing.T) {
	apos := newTestAPos(t)
	header := &block.Header{Number: uint256.NewInt(0)}
	blk := block.NewBlock(header, nil)
	if err := apos.Seal(nil, blk, make(chan block.IBlock, 1), make(chan struct{})); err != errUnknownBlock {
		t.Fatalf("expected errUnknownBlock for genesis seal, got %v", err)
	}
}
