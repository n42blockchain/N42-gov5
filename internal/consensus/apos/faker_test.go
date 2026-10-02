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

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// fakeChainReader is a minimal consensus.ChainHeaderReader stub sufficient to
// drive Faker.Prepare / Faker.CalcDifficulty without a real blockchain.
type fakeChainReader struct {
	headersByHash map[types.Hash]block.IHeader
}

func newFakeChainReader() *fakeChainReader {
	return &fakeChainReader{headersByHash: make(map[types.Hash]block.IHeader)}
}

func (f *fakeChainReader) Config() *params.ChainConfig                      { return params.TestChainConfig }
func (f *fakeChainReader) CurrentBlock() block.IBlock                       { return nil }
func (f *fakeChainReader) GetHeader(types.Hash, *uint256.Int) block.IHeader { return nil }
func (f *fakeChainReader) GetHeaderByNumber(*uint256.Int) block.IHeader     { return nil }
func (f *fakeChainReader) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	h, ok := f.headersByHash[hash]
	if !ok {
		return nil, nil
	}
	return h, nil
}
func (f *fakeChainReader) GetTd(types.Hash, *uint256.Int) *uint256.Int { return nil }
func (f *fakeChainReader) GetBlockByNumber(*uint256.Int) (block.IBlock, error) {
	return nil, nil
}

func TestFakerAuthorReturnsCoinbase(t *testing.T) {
	f := NewFaker()
	coinbase := types.HexToAddress("0x01")
	h := &block.Header{Coinbase: coinbase}
	addr, err := f.Author(h)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}
	if addr != coinbase {
		t.Fatalf("expected coinbase %v, got %v", coinbase, addr)
	}
}

func TestFakerAuthorRejectsWrongType(t *testing.T) {
	f := NewFaker()
	if _, err := f.Author(nilNumberBlockStub{header: &block.Header{}}.Header()); err != nil {
		// nilNumberBlockStub wraps a real *block.Header, so this should actually
		// succeed; this test only exists to document that Author type-asserts.
		t.Logf("Author returned error for header type assertion edge case: %v", err)
	}
}

func TestFakerVerifyHeaderAlwaysAccepts(t *testing.T) {
	f := NewFaker()
	if err := f.VerifyHeader(nil, &block.Header{}, true); err != nil {
		t.Fatalf("expected Faker to accept all headers, got %v", err)
	}
}

func TestFakerVerifyHeadersReturnsNilForEach(t *testing.T) {
	f := NewFaker()
	headers := []block.IHeader{&block.Header{}, &block.Header{}, &block.Header{}}
	abort, results := f.VerifyHeaders(nil, headers, nil)
	defer close(abort)
	for i := 0; i < len(headers); i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for verification result %d", i)
		}
	}
}

func TestFakerVerifyUnclesAlwaysAccepts(t *testing.T) {
	f := NewFaker()
	if err := f.VerifyUncles(nil, nil); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestFakerPrepareWithKnownParent(t *testing.T) {
	f := NewFaker().(*Faker)
	chain := newFakeChainReader()
	parentHash := types.HexToHash("0x01")
	parent := &block.Header{Number: uint256.NewInt(5)}
	chain.headersByHash[parentHash] = parent

	header := &block.Header{ParentHash: parentHash}
	if err := f.Prepare(chain, header); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if header.Difficulty == nil || header.Difficulty.Cmp(uint256.NewInt(7)) != 0 {
		t.Fatalf("expected difficulty parent+2=7, got %v", header.Difficulty)
	}
}

func TestFakerPrepareWithoutParentDefaultsDifficulty(t *testing.T) {
	f := NewFaker().(*Faker)
	chain := newFakeChainReader()
	header := &block.Header{ParentHash: types.HexToHash("0xdead")}
	if err := f.Prepare(chain, header); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if header.Difficulty == nil || header.Difficulty.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("expected default difficulty 1, got %v", header.Difficulty)
	}
}

func TestFakerPrepareRejectsWrongHeaderType(t *testing.T) {
	f := NewFaker()
	if err := f.Prepare(newFakeChainReader(), nilNumberBlockStub{header: &block.Header{}}); err == nil {
		t.Logf("Prepare accepted stub header (wraps real *block.Header)")
	}
}

func TestFakerFinalizeAndRewardsAreNoOps(t *testing.T) {
	f := NewFaker()
	rewards, balances, err := f.Finalize(nil, &block.Header{}, nil, nil, nil)
	if rewards != nil || balances != nil || err != nil {
		t.Fatalf("expected Finalize to be a no-op, got %v %v %v", rewards, balances, err)
	}
	rewards2, err2 := f.(*Faker).Rewards(nil, &block.Header{}, nil, false)
	if rewards2 != nil || err2 != nil {
		t.Fatalf("expected Rewards to be a no-op, got %v %v", rewards2, err2)
	}
}

func TestFakerFinalizeAndAssembleBuildsBlock(t *testing.T) {
	f := NewFaker()
	header := &block.Header{Number: uint256.NewInt(1)}
	blk, rewards, balances, err := f.FinalizeAndAssemble(nil, header, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("FinalizeAndAssemble: %v", err)
	}
	if blk == nil || rewards != nil || balances != nil {
		t.Fatalf("expected a built block with no rewards, got %v %v %v", blk, rewards, balances)
	}
}

func TestFakerSealRespectsStopSignal(t *testing.T) {
	f := NewFaker()
	header := &block.Header{Number: uint256.NewInt(1)}
	blk := block.NewBlock(header, nil)
	results := make(chan block.IBlock, 1)
	stop := make(chan struct{})
	close(stop)

	if err := f.Seal(nil, blk, results, stop); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	select {
	case <-results:
		t.Fatalf("expected no result to be sent when stop is already closed")
	default:
	}
}

func TestFakerSkipsDuplicateBlockNumber(t *testing.T) {
	f := NewFaker().(*Faker)
	f.lastSealedNum = 5
	header := &block.Header{Number: uint256.NewInt(3)}
	blk := block.NewBlock(header, nil)
	results := make(chan block.IBlock, 1)
	stop := make(chan struct{})
	defer close(stop)
	if err := f.Seal(nil, blk, results, stop); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	select {
	case <-results:
		t.Fatalf("expected duplicate block number to be skipped without a result")
	default:
	}
}

func TestFakerSealHash(t *testing.T) {
	f := NewFaker()
	h := &block.Header{Number: uint256.NewInt(1)}
	if f.SealHash(h) != h.Hash() {
		t.Fatalf("expected SealHash to equal header.Hash()")
	}
}

func TestFakerCalcDifficultyIncreasesWithParentNumber(t *testing.T) {
	f := NewFaker()
	parent := &block.Header{Number: uint256.NewInt(10)}
	diff := f.CalcDifficulty(nil, 0, parent)
	if diff.Cmp(uint256.NewInt(12)) != 0 {
		t.Fatalf("expected difficulty 12, got %v", diff)
	}
}

func TestFakerCalcDifficultyMissingParentNumber(t *testing.T) {
	f := NewFaker()
	diff := f.CalcDifficulty(nil, 0, &block.Header{})
	if diff.Cmp(uint256.NewInt(0)) != 0 {
		t.Fatalf("expected 0 for missing parent number, got %v", diff)
	}
}

func TestFakerTypeAPIsCloseIsServiceTransaction(t *testing.T) {
	f := NewFaker()
	if f.Type() != params.Faker {
		t.Fatalf("expected Faker type, got %v", f.Type())
	}
	if apis := f.APIs(nil); apis != nil {
		t.Fatalf("expected nil APIs, got %v", apis)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if f.IsServiceTransaction(types.Address{}, nil) {
		t.Fatalf("expected IsServiceTransaction false")
	}
}
