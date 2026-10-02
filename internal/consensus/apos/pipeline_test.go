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
// Exercises the full verifyHeader -> snapshot -> verifySeal pipeline with
// real secp256k1-signed headers, plus Finalize/FinalizeAndAssemble over a
// real IntraBlockState.

package apos

import (
	"crypto/ecdsa"
	"testing"

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

// aposTChain is a minimal in-memory consensus.N42ChainHeaderReader backed by
// maps keyed on block number and hash.
type aposTChain struct {
	byNumber map[uint64]block.IHeader
	byHash   map[types.Hash]block.IHeader
	current  block.IBlock

	depositLow, depositMax map[types.Address]*uint256.Int
	unpaid                 map[types.Address]*uint256.Int
}

func newAposTChain() *aposTChain {
	return &aposTChain{
		byNumber:   map[uint64]block.IHeader{},
		byHash:     map[types.Hash]block.IHeader{},
		depositLow: map[types.Address]*uint256.Int{},
		depositMax: map[types.Address]*uint256.Int{},
		unpaid:     map[types.Address]*uint256.Int{},
	}
}

func (c *aposTChain) add(h block.IHeader) {
	n, _ := requireHeaderNumber(h, "")
	c.byNumber[n.Uint64()] = h
	c.byHash[h.Hash()] = h
}

func (c *aposTChain) Config() *params.ChainConfig { return &params.ChainConfig{} }
func (c *aposTChain) CurrentBlock() block.IBlock  { return c.current }
func (c *aposTChain) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	h, ok := c.byNumber[number.Uint64()]
	if !ok || h.Hash() != hash {
		return nil
	}
	return h
}
func (c *aposTChain) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	return c.byNumber[number.Uint64()]
}
func (c *aposTChain) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	return c.byHash[hash], nil
}
func (c *aposTChain) GetTd(types.Hash, *uint256.Int) *uint256.Int { return nil }
func (c *aposTChain) GetBlock(hash types.Hash, number uint64) block.IBlock {
	h := c.GetHeader(hash, uint256.NewInt(number))
	if h == nil {
		return nil
	}
	return block.NewBlock(h, nil)
}
func (c *aposTChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	h, ok := c.byNumber[number.Uint64()]
	if !ok {
		return nil, nil
	}
	return block.NewBlock(h, nil), nil
}
func (c *aposTChain) GetDepositInfo(address types.Address) (*uint256.Int, *uint256.Int) {
	return c.depositLow[address], c.depositMax[address]
}
func (c *aposTChain) GetAccountRewardUnpaid(account types.Address) (*uint256.Int, error) {
	return c.unpaid[account], nil
}

var _ consensus.N42ChainHeaderReader = (*aposTChain)(nil)
var _ consensus.ConsensusChainReader = (*aposTChain)(nil)

func aposTGenesis(signer types.Address) *block.Header {
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

func aposTChild(t *testing.T, parent *block.Header, key *ecdsa.PrivateKey, inTurn bool) *block.Header {
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
		Extra:      make([]byte, extraVanity+extraSeal+1),
		GasLimit:   parent.GasLimit,
		GasUsed:    0,
	}
	if h.GasLimit == 0 {
		h.GasLimit = 5_000_000
	}
	copy(h.Nonce[:], nonceDropVote)
	signAposHeader(t, h, key)
	return h
}

func TestAposVerifyHeaderPipelinePasses(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := aposTNewSnapshotDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 30000}, db, &params.ChainConfig{}).(*APos)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newAposTChain()
	genesis := aposTGenesis(signer)
	genesis.GasLimit = 5_000_000
	chain.add(genesis)

	h1 := aposTChild(t, genesis, key, true)
	chain.add(h1)
	if err := eng.VerifyHeader(chain, h1, true); err != nil {
		t.Fatalf("VerifyHeader(h1): %v", err)
	}

	h2 := aposTChild(t, h1, key, true)
	chain.add(h2)
	if err := eng.VerifyHeader(chain, h2, true); err != nil {
		t.Fatalf("VerifyHeader(h2): %v", err)
	}
}

func TestAposVerifyHeaderPipelineRejectsWrongDifficulty(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := aposTNewSnapshotDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 30000}, db, &params.ChainConfig{}).(*APos)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newAposTChain()
	genesis := aposTGenesis(signer)
	genesis.GasLimit = 5_000_000
	chain.add(genesis)

	h1 := aposTChild(t, genesis, key, false)
	chain.add(h1)

	if err := eng.VerifyHeader(chain, h1, true); err != errWrongDifficulty {
		t.Fatalf("expected errWrongDifficulty, got %v", err)
	}
}

func TestAposVerifyHeadersBatch(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := aposTNewSnapshotDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 30000}, db, &params.ChainConfig{}).(*APos)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newAposTChain()
	genesis := aposTGenesis(signer)
	genesis.GasLimit = 5_000_000
	chain.add(genesis)
	h1 := aposTChild(t, genesis, key, true)
	chain.add(h1)
	h2 := aposTChild(t, h1, key, true)
	chain.add(h2)

	_, results := eng.VerifyHeaders(chain, []block.IHeader{h1, h2}, []bool{true, true})
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("VerifyHeaders[%d]: %v", i, err)
		}
	}
}

func TestAposFinalizeNoRewardConfig(t *testing.T) {
	stateDB := aposTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, stateDB)
	ibs := state.New(state.NewPlainState(tx, 1))

	db := memdb.NewTestDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 100}, db, &params.ChainConfig{}).(*APos)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newAposTChain()
	header := &block.Header{Number: uint256.NewInt(1)}
	rewards, unpaid, err := eng.Finalize(chain, header, ibs, nil, nil)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(rewards) != 0 {
		t.Fatalf("expected no rewards without reward config, got %v", rewards)
	}
	_ = unpaid
	if header.Root == (types.Hash{}) {
		t.Fatalf("expected Finalize to set a non-zero state root")
	}
}

func TestAposFinalizeRejectsNonN42Chain(t *testing.T) {
	db := memdb.NewTestDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 100}, db, &params.ChainConfig{}).(*APos)
	t.Cleanup(func() { _ = eng.Close() })

	header := &block.Header{Number: uint256.NewInt(1)}
	// nil chain does not satisfy consensus.N42ChainHeaderReader.
	_, _, err := eng.Finalize(nil, header, nil, nil, nil)
	if err == nil {
		t.Fatalf("expected error for non-N42 chain reader")
	}
}

func TestAposFinalizeAndAssembleBuildsBlock(t *testing.T) {
	stateDB := aposTNewSnapshotDB(t)
	tx := memdb.BeginRw(t, stateDB)
	ibs := state.New(state.NewPlainState(tx, 1))

	db := memdb.NewTestDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 100}, db, &params.ChainConfig{}).(*APos)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newAposTChain()
	header := &block.Header{Number: uint256.NewInt(1)}
	blk, rewards, _, err := eng.FinalizeAndAssemble(chain, header, ibs, nil, nil, nil)
	if err != nil {
		t.Fatalf("FinalizeAndAssemble: %v", err)
	}
	if blk == nil {
		t.Fatalf("expected assembled block")
	}
	if len(rewards) != 0 {
		t.Fatalf("expected no rewards without reward config")
	}
}

func TestAposAuthorizeSetsSignerAndFn(t *testing.T) {
	db := memdb.NewTestDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 100}, db, &params.ChainConfig{}).(*APos)
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
		t.Fatalf("expected signFn set")
	}
	if _, err := fn(accounts.Account{}, "", nil); err != nil {
		t.Fatalf("signFn: %v", err)
	}
	if !called {
		t.Fatalf("expected signFn invoked")
	}
}

func TestAposVerifyUncles(t *testing.T) {
	db := memdb.NewTestDB(t)
	eng := New(&params.APosConfig{Period: 1, Epoch: 100}, db, &params.ChainConfig{}).(*APos)
	t.Cleanup(func() { _ = eng.Close() })
	if err := eng.VerifyUncles(nil, nil); err != nil {
		t.Fatalf("VerifyUncles: %v", err)
	}
}
