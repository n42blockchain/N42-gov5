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
//
// Exercises the full verifyHeader -> snapshot -> verifySeal pipeline with
// real secp256k1-signed headers, plus Finalize/FinalizeAndAssemble over a
// real IntraBlockState and the read-only API surface.

package apoa

import (
	"crypto/ecdsa"
	"testing"

	lru "github.com/hashicorp/golang-lru"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// newARC returns a fresh signature cache, mirroring the one Apoa.New builds.
func newARC(t *testing.T) (*lru.ARCCache, error) {
	t.Helper()
	return lru.NewARC(inmemorySnapshots)
}

// apoaTChain is a minimal in-memory consensus.ConsensusChainReader backed by
// maps keyed on block number and hash, enough to drive verifyHeader's
// cascading-field and snapshot lookups.
type apoaTChain struct {
	byNumber map[uint64]block.IHeader
	byHash   map[types.Hash]block.IHeader
	current  block.IBlock
}

func newApoaTChain() *apoaTChain {
	return &apoaTChain{byNumber: map[uint64]block.IHeader{}, byHash: map[types.Hash]block.IHeader{}}
}

func (c *apoaTChain) add(h block.IHeader) {
	n, _ := requireHeaderNumber(h, "")
	c.byNumber[n.Uint64()] = h
	c.byHash[h.Hash()] = h
}

func (c *apoaTChain) Config() *params.ChainConfig { return &params.ChainConfig{} }
func (c *apoaTChain) CurrentBlock() block.IBlock  { return c.current }
func (c *apoaTChain) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	h, ok := c.byNumber[number.Uint64()]
	if !ok || h.Hash() != hash {
		return nil
	}
	return h
}
func (c *apoaTChain) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	return c.byNumber[number.Uint64()]
}
func (c *apoaTChain) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	return c.byHash[hash], nil
}
func (c *apoaTChain) GetTd(types.Hash, *uint256.Int) *uint256.Int { return nil }
func (c *apoaTChain) GetBlock(hash types.Hash, number uint64) block.IBlock {
	h := c.GetHeader(hash, uint256.NewInt(number))
	if h == nil {
		return nil
	}
	return block.NewBlockFromReceipt(h, nil, nil, nil, nil)
}
func (c *apoaTChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	h, ok := c.byNumber[number.Uint64()]
	if !ok {
		return nil, nil
	}
	return block.NewBlockFromReceipt(h, nil, nil, nil, nil), nil
}

var _ consensus.ConsensusChainReader = (*apoaTChain)(nil)

// apoaTGenesis builds an unsigned checkpoint (number 0) header whose extra
// data advertises a single signer, as expected by Apoa.snapshot's genesis
// bootstrap path.
func apoaTGenesis(signer types.Address) *block.Header {
	extra := make([]byte, extraVanity)
	extra = append(extra, signer[:]...)
	extra = append(extra, make([]byte, extraSeal)...)
	return &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(1),
		Extra:      extra,
		Time:       1000,
	}
}

// apoaTChild builds a signed, non-checkpoint child header at number+1 over
// parent, with the difficulty set according to in-turn/out-of-turn rotation
// for a single-signer snapshot (matches the real Prepare/calcDifficulty
// contract: inturn -> diffInTurn, else diffNoTurn).
func apoaTChild(t *testing.T, parent *block.Header, key *ecdsa.PrivateKey, inTurn bool) *block.Header {
	t.Helper()
	diff := uint256.NewInt(1)
	if inTurn {
		diff = uint256.NewInt(2)
	}
	n := new(uint256.Int).Add(parent.Number, uint256.NewInt(1))
	h := &block.Header{
		Number:     n,
		Difficulty: diff,
		ParentHash: parent.Hash(),
		Time:       parent.Time + 1,
		Extra:      make([]byte, extraVanity+extraSeal),
		GasLimit:   1_000_000,
	}
	copy(h.Nonce[:], nonceDropVote)
	signHeader(t, h, key)
	return h
}

func TestApoaVerifyHeaderPipelinePasses(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := apoaTNewSnapshotDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 30000}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newApoaTChain()
	genesis := apoaTGenesis(signer)
	chain.add(genesis)

	// signers() == [signer] (single entry), offset 0: inturn(1, signer) == (1%1==0) == true.
	h1 := apoaTChild(t, genesis, key, true)
	chain.add(h1)

	if err := eng.VerifyHeader(chain, h1, true); err != nil {
		t.Fatalf("VerifyHeader(h1): %v", err)
	}

	h2 := apoaTChild(t, h1, key, true)
	chain.add(h2)
	if err := eng.VerifyHeader(chain, h2, true); err != nil {
		t.Fatalf("VerifyHeader(h2): %v", err)
	}
}

func TestApoaVerifyHeaderPipelineRejectsWrongDifficulty(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := apoaTNewSnapshotDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 30000}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newApoaTChain()
	genesis := apoaTGenesis(signer)
	chain.add(genesis)

	// Wrong: single signer is in-turn at block 1, but we claim out-of-turn difficulty.
	h1 := apoaTChild(t, genesis, key, false)
	chain.add(h1)

	if err := eng.VerifyHeader(chain, h1, true); err != errWrongDifficulty {
		t.Fatalf("expected errWrongDifficulty, got %v", err)
	}
}

func TestApoaVerifyHeadersBatch(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := apoaTNewSnapshotDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 30000}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newApoaTChain()
	genesis := apoaTGenesis(signer)
	chain.add(genesis)
	h1 := apoaTChild(t, genesis, key, true)
	chain.add(h1)
	h2 := apoaTChild(t, h1, key, true)
	chain.add(h2)

	_, results := eng.VerifyHeaders(chain, []block.IHeader{h1, h2}, []bool{true, true})
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("VerifyHeaders[%d]: %v", i, err)
		}
	}
}

func TestApoaVerifyUncles(t *testing.T) {
	db := memdb.NewTestDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })
	if err := eng.VerifyUncles(nil, nil); err != nil {
		t.Fatalf("VerifyUncles: %v", err)
	}
}

func TestApoaRewardsIsNoOp(t *testing.T) {
	db := memdb.NewTestDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })
	rewards, err := eng.Rewards(nil, nil, nil, false)
	if err != nil || rewards != nil {
		t.Fatalf("Rewards: expected nil,nil; got %v,%v", rewards, err)
	}
}

func TestApoaFinalizeSetsStateRoot(t *testing.T) {
	stateDB := apoaTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, stateDB)
	ibs := state.New(state.NewPlainState(tx, 1))

	db := memdb.NewTestDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	header := &block.Header{Number: uint256.NewInt(1)}
	rewards, unpaid, err := eng.Finalize(nil, header, ibs, nil, nil)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if rewards != nil || unpaid != nil {
		t.Fatalf("expected no rewards/unpaid from PoA finalize")
	}
	if header.Root == (types.Hash{}) {
		t.Fatalf("expected Finalize to set a non-zero state root")
	}
}

func TestApoaFinalizeRejectsWrongHeaderType(t *testing.T) {
	db := memdb.NewTestDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	_, _, err := eng.Finalize(nil, fakeIHeader{}, nil, nil, nil)
	if err == nil {
		t.Fatalf("expected error for non *block.Header input")
	}
}

// fakeIHeader is a block.IHeader implementation that is not *block.Header,
// used to exercise the type-assertion guard rails in Finalize/verifySeal/etc.
type fakeIHeader struct{ block.IHeader }

func TestApoaFinalizeAndAssembleBuildsBlock(t *testing.T) {
	stateDB := apoaTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, stateDB)
	ibs := state.New(state.NewPlainState(tx, 1))

	db := memdb.NewTestDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	header := &block.Header{Number: uint256.NewInt(1)}
	blk, rewards, unpaid, err := eng.FinalizeAndAssemble(nil, header, ibs, nil, nil, nil)
	if err != nil {
		t.Fatalf("FinalizeAndAssemble: %v", err)
	}
	if blk == nil {
		t.Fatalf("expected assembled block")
	}
	if rewards != nil || unpaid != nil {
		t.Fatalf("expected no rewards/unpaid from PoA finalize")
	}
}

func TestApoaAuthorizeSetsSignerAndFn(t *testing.T) {
	db := memdb.NewTestDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 100}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	addr := types.HexToAddress("0x01")
	called := false
	eng.Authorize(addr, func(accounts.Account, string, []byte) ([]byte, error) {
		called = true
		return nil, nil
	})

	eng.lock.RLock()
	gotSigner := eng.signer
	fn := eng.signFn
	eng.lock.RUnlock()

	if gotSigner != addr {
		t.Fatalf("expected signer %v, got %v", addr, gotSigner)
	}
	if fn == nil {
		t.Fatalf("expected signFn to be set")
	}
	if _, err := fn(accounts.Account{}, "", nil); err != nil {
		t.Fatalf("signFn: %v", err)
	}
	if !called {
		t.Fatalf("expected signFn to have been invoked")
	}
}

func TestApoaSnapshotLoadsFromDiskCheckpoint(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := apoaTNewSnapshotDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 30000}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newApoaTChain()
	genesis := apoaTGenesis(signer)
	chain.add(genesis)

	snap, err := eng.snapshot(chain, 0, genesis.Hash(), nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, ok := snap.Signers[signer]; !ok {
		t.Fatalf("expected genesis signer in snapshot")
	}

	// Drop the in-memory cache and force a disk reload via loadSnapshot by
	// calling snapshot again directly against the same hash; the checkpoint
	// branch persists to disk on first computation (see apoa.go:snapshot),
	// and a fresh ARC cache forces the on-disk path to be exercised.
	eng.recents, _ = newARC(t)
	reloaded, err := eng.snapshot(chain, 0, genesis.Hash(), nil)
	if err != nil {
		t.Fatalf("snapshot (reload): %v", err)
	}
	if reloaded.Number != 0 {
		t.Fatalf("expected reloaded snapshot at number 0, got %d", reloaded.Number)
	}
}
