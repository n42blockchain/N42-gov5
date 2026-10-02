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
// Covers the apos API methods that only need a chain reader fake: GetSigners,
// GetSignersAtHash, Proposals/Propose/Discard, Status, GetSigner, and
// GetSnapshot(AtHash). GetRewards/GetDepositInfo/GetBlockRewards/
// GetMinedBlock/VerifiedBlock/GetAccountRewardUnpaid are left uncovered --
// they go through api.apos.dbView + rpchelper.GetCanonicalBlockNumber and
// the deposit contract's storage layout, which need a populated rawdb
// (canonical hash index, deposit contract state) rather than a plain chain
// reader fake; out of scope for this pass.

package apos

import (
	"testing"

	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func aposTBuildChain(t *testing.T) (*aposTChain, *APos, types.Address) {
	t.Helper()
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
	chain.current = block.NewBlock(h1, nil)

	return chain, eng, signer
}

func TestAposAPIGetSigners(t *testing.T) {
	chain, eng, signer := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	signers, err := api.GetSigners(nil)
	if err != nil {
		t.Fatalf("GetSigners: %v", err)
	}
	if len(signers) != 1 || signers[0] != *(*avmutil.Address)(&signer) {
		t.Fatalf("unexpected signers: %v", signers)
	}
}

func TestAposAPIGetSignersAtHash(t *testing.T) {
	chain, eng, _ := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	h1 := chain.byNumber[1]
	signers, err := api.GetSignersAtHash(h1.Hash())
	if err != nil {
		t.Fatalf("GetSignersAtHash: %v", err)
	}
	if len(signers) != 1 {
		t.Fatalf("unexpected signers: %v", signers)
	}
}

func TestAposAPIGetSignersAtHashMissing(t *testing.T) {
	chain, eng, _ := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	if _, err := api.GetSignersAtHash(types.HexToHash("0xdead")); err != errUnknownBlock {
		t.Fatalf("expected errUnknownBlock, got %v", err)
	}
}

func TestAposAPIProposalsProposeDiscard(t *testing.T) {
	_, eng, _ := aposTBuildChain(t)
	api := &API{apos: eng}

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

func TestAposAPIStatus(t *testing.T) {
	chain, eng, _ := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	status, err := api.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.NumBlocks != 0 {
		t.Fatalf("expected NumBlocks clamped to 0 this close to genesis, got %d", status.NumBlocks)
	}
	if status.SigningStatus == nil {
		t.Fatalf("expected non-nil signing status")
	}
}

func TestAposAPIStatusRejectsMissingCurrentBlock(t *testing.T) {
	chain, eng, _ := aposTBuildChain(t)
	chain.current = nil
	api := &API{chain: chain, apos: eng}

	if _, err := api.Status(); err != errUnknownBlock {
		t.Fatalf("expected errUnknownBlock, got %v", err)
	}
}

func TestAposAPIGetSignerCurrentBlock(t *testing.T) {
	chain, eng, signer := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	got, err := api.GetSigner(&blockNumberOrHashOrRLP{})
	if err != nil {
		t.Fatalf("GetSigner: %v", err)
	}
	if got != signer {
		t.Fatalf("expected signer %v, got %v", signer, got)
	}
}

func TestAposAPIGetSignerByNumber(t *testing.T) {
	chain, eng, signer := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(1))
	got, err := api.GetSigner(&blockNumberOrHashOrRLP{BlockNumberOrHash: &bnh})
	if err != nil {
		t.Fatalf("GetSigner: %v", err)
	}
	if got != signer {
		t.Fatalf("expected signer %v, got %v", signer, got)
	}
}

func TestAposAPIGetSignerByHash(t *testing.T) {
	chain, eng, signer := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

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

// Note: calling GetSigner with a nil BlockNumberOrHash *and* a nil current
// block panics on a nil-pointer dereference (blockNrOrHash.String() in the
// "missing block" error path) -- same latent defect as apoa's GetSigner,
// documented there; not exercised here for the same reason.
func TestAposAPIGetSignerMissingBlock(t *testing.T) {
	chain, eng, _ := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(999))
	if _, err := api.GetSigner(&blockNumberOrHashOrRLP{BlockNumberOrHash: &bnh}); err == nil {
		t.Fatalf("expected error for missing block")
	}
}

func TestAposAPIGetSignerRejectsRLP(t *testing.T) {
	_, eng, _ := aposTBuildChain(t)
	api := &API{apos: eng}

	_, err := api.GetSigner(&blockNumberOrHashOrRLP{RLP: []byte{0x01}})
	if err == nil || err.Error() != "do not support rlp" {
		t.Fatalf("expected rlp-unsupported error, got %v", err)
	}
}

func TestAposAPIGetSnapshotByNumber(t *testing.T) {
	chain, eng, signer := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	n := jsonrpc.BlockNumber(1)
	snap, err := api.GetSnapshot(&n)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if _, ok := snap.Signers[signer]; !ok {
		t.Fatalf("expected signer in snapshot")
	}
}

func TestAposAPIGetSnapshotAtHash(t *testing.T) {
	chain, eng, signer := aposTBuildChain(t)
	api := &API{chain: chain, apos: eng}

	h1 := chain.byNumber[1]
	snap, err := api.GetSnapshotAtHash(h1.Hash())
	if err != nil {
		t.Fatalf("GetSnapshotAtHash: %v", err)
	}
	if _, ok := snap.Signers[signer]; !ok {
		t.Fatalf("expected signer in snapshot")
	}
}
