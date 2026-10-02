// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY with even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Covers Reward.SetRewards/buildRewards/getAccountRewardUnpaid/
// setAccountRewardUnpaid against a real rawdb-backed tx: canonical hash,
// header, block (with a verifier), and a deposit record sized to land in
// the "fifty deposit" reward tier.

package apos

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestRewardSetRewardsPaysDepositedVerifier(t *testing.T) {
	db := aposTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, db)

	addr := types.HexToAddress("0x09")
	header := &block.Header{Number: uint256.NewInt(1)}
	blk, ok := block.NewBlock(header, nil).(*block.Block)
	if !ok {
		t.Fatalf("expected *block.Block")
	}
	blk.Body().(*block.Body).Verifiers = []*block.Verify{{Address: addr}}

	if err := rawdb.WriteCanonicalHash(tx, header.Hash(), 1); err != nil {
		t.Fatalf("WriteCanonicalHash: %v", err)
	}
	if err := rawdb.WriteBlock(tx, blk); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	// 50*params.N deposit lands in the "fifty deposit" reward tier (see
	// contracts/deposit/contract.go's depositEther switch). 50*1e18
	// overflows uint64, so multiply within uint256 instead.
	depositAmount := new(uint256.Int).Mul(uint256.NewInt(50), uint256.NewInt(uint64(params.N)))
	// GetDeposit validates the stored bytes as a real BLS public key
	// (bls.PublicKeyFromBytes), so an all-zero key is rejected and the
	// deposit lookup silently fails -- a real key is required here.
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatalf("bls.RandKey: %v", err)
	}
	var pubkey types.PublicKey
	if err := pubkey.SetBytes(sk.PublicKey().Marshal()); err != nil {
		t.Fatalf("pubkey.SetBytes: %v", err)
	}
	if err := rawdb.PutDeposit(tx, addr, pubkey, *depositAmount); err != nil {
		t.Fatalf("PutDeposit: %v", err)
	}

	// number=2, rewardEpoch=1 -> endNumber=1, so buildRewards only walks
	// block 1 (avoiding the same number==rewardEpoch underflow noted for
	// AccumulateRewards in reward_pipeline_test.go).
	r := NewReward(&params.ChainConfig{Apos: &params.APosConfig{RewardEpoch: 1, RewardLimit: big.NewInt(1)}})
	rewards, err := r.SetRewards(tx, uint256.NewInt(2), true)
	if err != nil {
		t.Fatalf("SetRewards: %v", err)
	}
	if len(rewards) != 1 || rewards[0].Account != addr {
		t.Fatalf("expected one reward for %v, got %+v", addr, rewards)
	}

	unpaid, err := r.getAccountRewardUnpaid(tx, addr)
	if err != nil {
		t.Fatalf("getAccountRewardUnpaid: %v", err)
	}
	if unpaid == nil || unpaid.Sign() != 0 {
		t.Fatalf("expected unpaid reset to zero once the (tiny) reward limit is met, got %v", unpaid)
	}
}

func TestRewardSetAccountRewardUnpaidRoundTrip(t *testing.T) {
	db := aposTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, db)

	addr := types.HexToAddress("0x0a")
	r := &Reward{}
	if err := r.setAccountRewardUnpaid(tx, addr, uint256.NewInt(77)); err != nil {
		t.Fatalf("setAccountRewardUnpaid: %v", err)
	}
	got, err := r.getAccountRewardUnpaid(tx, addr)
	if err != nil {
		t.Fatalf("getAccountRewardUnpaid: %v", err)
	}
	if got == nil || got.Cmp(uint256.NewInt(77)) != 0 {
		t.Fatalf("expected 77, got %v", got)
	}
}
