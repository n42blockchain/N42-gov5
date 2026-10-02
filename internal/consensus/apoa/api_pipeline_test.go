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
// Covers the API methods that only need a chain reader fake: GetSigners,
// GetSignersAtHash, Proposals/Propose/Discard, Status, and GetSigner.

package apoa

import (
	"testing"

	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// apoaTBuildChain builds a two-block chain (genesis + h1) signed by a single
// signer and returns the chain, engine, and signer address for reuse across
// the API tests below.
func apoaTBuildChain(t *testing.T) (*apoaTChain, *Apoa, types.Address) {
	t.Helper()
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
	chain.current = block.NewBlock(h1, nil)

	return chain, eng, signer
}

func TestApoaAPIGetSigners(t *testing.T) {
	chain, eng, signer := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	signers, err := api.GetSigners(nil)
	if err != nil {
		t.Fatalf("GetSigners: %v", err)
	}
	if len(signers) != 1 || signers[0] != *(*avmutil.Address)(&signer) {
		t.Fatalf("unexpected signers: %v", signers)
	}
}

func TestApoaAPIGetSignersAtHash(t *testing.T) {
	chain, eng, _ := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	h1, ok := chain.byNumber[1]
	if !ok {
		t.Fatalf("expected h1 present")
	}
	signers, err := api.GetSignersAtHash(h1.Hash())
	if err != nil {
		t.Fatalf("GetSignersAtHash: %v", err)
	}
	if len(signers) != 1 {
		t.Fatalf("unexpected signers: %v", signers)
	}
}

func TestApoaAPIGetSignersAtHashMissing(t *testing.T) {
	chain, eng, _ := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	if _, err := api.GetSignersAtHash(types.HexToHash("0xdead")); err != errUnknownBlock {
		t.Fatalf("expected errUnknownBlock, got %v", err)
	}
}

func TestApoaAPIProposalsProposeDiscard(t *testing.T) {
	_, eng, _ := apoaTBuildChain(t)
	api := &API{apoa: eng}

	addr := avmutil.Address(types.HexToAddress("0x02"))
	api.Propose(addr, true)

	proposals := api.Proposals()
	if len(proposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(proposals))
	}
	for _, auth := range proposals {
		if !auth {
			t.Fatalf("expected authorize=true")
		}
	}

	api.Discard(addr)
	if len(api.Proposals()) != 0 {
		t.Fatalf("expected proposal removed after Discard")
	}
}

func TestApoaAPIStatus(t *testing.T) {
	chain, eng, signer := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

status, err := api.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	// So close to genesis, Status's window-clamp (numBlocks=min(64,end-1))
	// collapses to zero blocks; the method still has to return without
	// error and with an initialized (non-nil) status rather than panic on
	// the division-by-zero it guards against.
	if status.NumBlocks != 0 {
		t.Fatalf("expected NumBlocks clamped to 0 this close to genesis, got %d", status.NumBlocks)
	}
	if status.SigningStatus == nil {
		t.Fatalf("expected non-nil (possibly empty) signing status")
	}
	_ = signer
}

func TestApoaAPIStatusRejectsMissingCurrentBlock(t *testing.T) {
	chain, eng, _ := apoaTBuildChain(t)
	chain.current = nil
	api := &API{chain: chain, apoa: eng}

	if _, err := api.Status(); err != errUnknownBlock {
		t.Fatalf("expected errUnknownBlock, got %v", err)
	}
}

func TestApoaAPIGetSignerCurrentBlock(t *testing.T) {
	chain, eng, signer := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	got, err := api.GetSigner(&blockNumberOrHashOrRLP{})
	if err != nil {
		t.Fatalf("GetSigner: %v", err)
	}
	if got != signer {
		t.Fatalf("expected signer %v, got %v", signer, got)
	}
}

func TestApoaAPIGetSignerByNumber(t *testing.T) {
	chain, eng, signer := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(1))
	got, err := api.GetSigner(&blockNumberOrHashOrRLP{BlockNumberOrHash: &bnh})
	if err != nil {
		t.Fatalf("GetSigner: %v", err)
	}
	if got != signer {
		t.Fatalf("expected signer %v, got %v", signer, got)
	}
}

func TestApoaAPIGetSignerByHash(t *testing.T) {
	chain, eng, signer := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	h1 := chain.byNumber[1]
	bnh := jsonrpc.BlockNumberOrHashWithHash(h1.Hash(), false)
	got, err := api.GetSigner(&blockNumberOrHashOrRLP{BlockNumberOrHash: &bnh})
	if err != nil {
		t.Fatalf("GetSigner: %v", err)
	}
	if got != signer {
		t.Fatalf("expected signer %v, got %v", signer, got)
	}
}

// TestApoaAPIGetSignerMissingBlock covers the "missing block" error path
// with an explicit (non-nil) BlockNumberOrHash pointing at a block number
// that doesn't exist in the chain. Note: GetSigner has a latent nil-pointer
// defect on this same error path when called with a nil BlockNumberOrHash
// AND a nil current block (api.go's `blockNrOrHash.String()` call derefs a
// nil *jsonrpc.BlockNumberOrHash) -- not exercised here since it would
// panic the test process; left unfixed per instructions (non-test code).
func TestApoaAPIGetSignerMissingBlock(t *testing.T) {
	chain, eng, _ := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(999))
	if _, err := api.GetSigner(&blockNumberOrHashOrRLP{BlockNumberOrHash: &bnh}); err == nil {
		t.Fatalf("expected error for missing block")
	}
}

func TestApoaAPIGetSignerRejectsRLP(t *testing.T) {
	_, eng, _ := apoaTBuildChain(t)
	api := &API{apoa: eng}

	_, err := api.GetSigner(&blockNumberOrHashOrRLP{RLP: []byte{0x01}})
	if err == nil || err.Error() != "do not support rlp" {
		t.Fatalf("expected rlp-unsupported error, got %v", err)
	}
}

func TestApoaAPIResolveHeaderByNumber(t *testing.T) {
	chain, eng, _ := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	n := jsonrpc.BlockNumber(1)
	h := api.resolveHeader(&n)
	if h == nil {
		t.Fatalf("expected header for block 1")
	}
	gotNumber, err := requireHeaderNumber(h, "")
	if err != nil || gotNumber.Uint64() != 1 {
		t.Fatalf("resolveHeader returned wrong block: %+v, err=%v", h, err)
	}
}

func TestApoaAPIGetSnapshotByNumber(t *testing.T) {
	chain, eng, signer := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	n := jsonrpc.BlockNumber(1)
	snap, err := api.GetSnapshot(&n)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if _, ok := snap.Signers[signer]; !ok {
		t.Fatalf("expected signer in snapshot")
	}
}

func TestApoaAPIGetSnapshotAtHash(t *testing.T) {
	chain, eng, signer := apoaTBuildChain(t)
	api := &API{chain: chain, apoa: eng}

	h1 := chain.byNumber[1]
	snap, err := api.GetSnapshotAtHash(h1.Hash())
	if err != nil {
		t.Fatalf("GetSnapshotAtHash: %v", err)
	}
	if _, ok := snap.Signers[signer]; !ok {
		t.Fatalf("expected signer in snapshot")
	}
}
