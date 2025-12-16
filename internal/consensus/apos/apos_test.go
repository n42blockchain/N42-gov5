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
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
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

// =============================================================================
// Snapshot Tests (结构体测试)
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

	snap.Signers[signer1] = struct{}{}
	snap.Signers[signer2] = struct{}{}

	if len(snap.Signers) != 2 {
		t.Errorf("Should have 2 signers, got %d", len(snap.Signers))
	}

	if _, ok := snap.Signers[signer1]; !ok {
		t.Error("signer1 should be in Signers")
	}
	if _, ok := snap.Signers[signer2]; !ok {
		t.Error("signer2 should be in Signers")
	}

	t.Logf("✓ Snapshot Signers map works correctly")
}

// =============================================================================
// Faker Tests
// =============================================================================

func TestNewFaker(t *testing.T) {
	faker := NewFaker()

	if faker == nil {
		t.Fatal("NewFaker should not return nil")
	}

	// 验证 Faker 实现了 consensus.Engine 接口
	var _ consensus.Engine = faker

	t.Logf("✓ NewFaker works correctly")
}

func TestFakerImplementsEngine(t *testing.T) {
	faker := NewFaker()

	// 验证 Faker 实现了 consensus.Engine 接口的所有方法
	var _ consensus.Engine = faker

	t.Logf("✓ Faker implements consensus.Engine correctly")
}

// =============================================================================
// API Tests
// =============================================================================

func TestMinedBlockStruct(t *testing.T) {
	block := MinedBlock{
		BlockNumber: uint256.NewInt(1000),
		Timestamp:   1234567890,
	}

	if block.BlockNumber.Uint64() != 1000 {
		t.Error("MinedBlock BlockNumber mismatch")
	}
	if block.Timestamp != 1234567890 {
		t.Error("MinedBlock Timestamp mismatch")
	}

	t.Logf("✓ MinedBlock struct works correctly")
}

func TestMinedBlockResponseStruct(t *testing.T) {
	resp := MinedBlockResponse{
		MinedBlocks:        []MinedBlock{},
		CurrentBlockNumber: uint256.NewInt(1000),
	}

	if resp.CurrentBlockNumber.Uint64() != 1000 {
		t.Error("MinedBlockResponse CurrentBlockNumber mismatch")
	}

	t.Logf("✓ MinedBlockResponse struct works correctly")
}

func TestVerifiedBlockResponseStruct(t *testing.T) {
	resp := VerifiedBlockResponse{
		Total: uint256.NewInt(20),
	}

	if resp.Total.Uint64() != 20 {
		t.Error("VerifiedBlockResponse Total mismatch")
	}

	t.Logf("✓ VerifiedBlockResponse struct works correctly")
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

func BenchmarkNewFaker(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NewFaker()
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
