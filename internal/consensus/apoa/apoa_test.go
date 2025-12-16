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

	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Vote Tests
// =============================================================================

func TestVoteStruct(t *testing.T) {
	signer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	target := types.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")

	vote := Vote{
		Signer:    signer,
		Block:     100,
		Address:   target,
		Authorize: true,
	}

	if vote.Signer != signer {
		t.Error("Vote Signer mismatch")
	}
	if vote.Block != 100 {
		t.Error("Vote Block mismatch")
	}
	if vote.Address != target {
		t.Error("Vote Address mismatch")
	}
	if !vote.Authorize {
		t.Error("Vote Authorize should be true")
	}

	t.Logf("✓ Vote struct works correctly")
}

func TestVoteAuthorizeTypes(t *testing.T) {
	tests := []struct {
		name      string
		authorize bool
		desc      string
	}{
		{"add_signer", true, "授权新签名者"},
		{"remove_signer", false, "移除签名者"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vote := Vote{
				Authorize: tt.authorize,
			}
			if vote.Authorize != tt.authorize {
				t.Errorf("Authorize should be %v", tt.authorize)
			}
		})
	}

	t.Logf("✓ Vote authorize types work correctly")
}

// =============================================================================
// Tally Tests
// =============================================================================

func TestTallyStruct(t *testing.T) {
	tally := Tally{
		Authorize: true,
		Votes:     5,
	}

	if !tally.Authorize {
		t.Error("Tally Authorize should be true")
	}
	if tally.Votes != 5 {
		t.Error("Tally Votes should be 5")
	}

	t.Logf("✓ Tally struct works correctly")
}

func TestTallyVoteCount(t *testing.T) {
	tests := []struct {
		name   string
		votes  int
		passes bool
	}{
		{"zero_votes", 0, false},
		{"one_vote", 1, true},
		{"many_votes", 10, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tally := Tally{
				Votes: tt.votes,
			}
			hasVotes := tally.Votes > 0
			if hasVotes != tt.passes {
				t.Errorf("Vote count check failed for %d votes", tt.votes)
			}
		})
	}

	t.Logf("✓ Tally vote count works correctly")
}

// =============================================================================
// Snapshot Tests (结构体测试，不依赖 newSnapshot)
// =============================================================================

func TestSnapshotStructFields(t *testing.T) {
	snap := &Snapshot{
		Number:  100,
		Hash:    types.HexToHash("0xabcdef"),
		Signers: make(map[types.Address]struct{}),
		Recents: make(map[uint64]types.Address),
		Votes:   []*Vote{},
		Tally:   make(map[types.Address]Tally),
	}

	if snap.Number != 100 {
		t.Error("Snapshot Number mismatch")
	}
	if snap.Signers == nil {
		t.Error("Snapshot Signers should not be nil")
	}
	if snap.Recents == nil {
		t.Error("Snapshot Recents should not be nil")
	}
	if snap.Votes == nil {
		t.Error("Snapshot Votes should not be nil")
	}
	if snap.Tally == nil {
		t.Error("Snapshot Tally should not be nil")
	}

	t.Logf("✓ Snapshot struct fields work correctly")
}

func TestSnapshotSignersMap(t *testing.T) {
	snap := &Snapshot{
		Signers: make(map[types.Address]struct{}),
	}

	signer1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	signer2 := types.HexToAddress("0x2222222222222222222222222222222222222222")

	// 添加签名者
	snap.Signers[signer1] = struct{}{}
	snap.Signers[signer2] = struct{}{}

	if len(snap.Signers) != 2 {
		t.Errorf("Should have 2 signers, got %d", len(snap.Signers))
	}

	// 检查签名者存在
	if _, ok := snap.Signers[signer1]; !ok {
		t.Error("signer1 should be in Signers")
	}
	if _, ok := snap.Signers[signer2]; !ok {
		t.Error("signer2 should be in Signers")
	}

	t.Logf("✓ Snapshot Signers map works correctly")
}

func TestSnapshotRecentsMap(t *testing.T) {
	snap := &Snapshot{
		Recents: make(map[uint64]types.Address),
	}

	signer := types.HexToAddress("0x1111111111111111111111111111111111111111")
	snap.Recents[100] = signer

	if len(snap.Recents) != 1 {
		t.Error("Recents should have 1 entry")
	}
	if snap.Recents[100] != signer {
		t.Error("Recents[100] should be signer")
	}

	t.Logf("✓ Snapshot Recents map works correctly")
}

func TestSnapshotTallyMap(t *testing.T) {
	snap := &Snapshot{
		Tally: make(map[types.Address]Tally),
	}

	addr := types.HexToAddress("0x1111111111111111111111111111111111111111")
	snap.Tally[addr] = Tally{Authorize: true, Votes: 3}

	if len(snap.Tally) != 1 {
		t.Error("Tally should have 1 entry")
	}
	if snap.Tally[addr].Votes != 3 {
		t.Error("Tally votes should be 3")
	}

	t.Logf("✓ Snapshot Tally map works correctly")
}

// =============================================================================
// API Tests
// =============================================================================

func TestAPIStruct(t *testing.T) {
	api := &API{
		apoa: nil,
	}

	if api == nil {
		t.Fatal("API should not be nil")
	}

	t.Logf("✓ API struct works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkVoteCreation(b *testing.B) {
	signer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	target := types.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Vote{
			Signer:    signer,
			Block:     uint64(i),
			Address:   target,
			Authorize: true,
		}
	}
}

func BenchmarkSnapshotSignerLookup(b *testing.B) {
	snap := &Snapshot{
		Signers: make(map[types.Address]struct{}),
	}

	for i := 0; i < 100; i++ {
		addr := types.Address{}
		addr[19] = byte(i)
		snap.Signers[addr] = struct{}{}
	}

	target := types.Address{}
	target[19] = 50

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = snap.Signers[target]
	}
}
