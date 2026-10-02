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
// Covers Prepare's full field-assembly path (including a pending proposal
// vote) and blockNumberOrHashOrRLP.UnmarshalJSON's two decode branches.

package apoa

import (
	"encoding/json"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

func TestApoaPrepareCastsPendingVote(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := apoaTNewSnapshotDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 30000}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })
	eng.Authorize(signer, nil)

	chain := newApoaTChain()
	genesis := apoaTGenesis(signer)
	chain.add(genesis)

	target := types.HexToAddress("0x42")
	api := &API{chain: chain, apoa: eng}
	api.Propose(avmutil.Address(target), true)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		ParentHash: genesis.Hash(),
	}
	if err := eng.Prepare(chain, header); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if header.Coinbase != target {
		t.Fatalf("expected Prepare to cast the pending vote onto Coinbase, got %v", header.Coinbase)
	}
	if header.Nonce != (block.BlockNonce{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) {
		t.Fatalf("expected authorize nonce, got %v", header.Nonce)
	}
	if len(header.Extra) != extraVanity+extraSeal {
		t.Fatalf("expected extra data sized for vanity+seal, got %d bytes", len(header.Extra))
	}
	if header.Time <= genesis.Time {
		t.Fatalf("expected Prepare to advance the timestamp past the parent's")
	}
}

func TestApoaPrepareRejectsUnknownAncestor(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)

	db := apoaTNewSnapshotDB(t)
	eng := New(&params.CliqueConfig{Period: 1, Epoch: 30000}, db).(*Apoa)
	t.Cleanup(func() { _ = eng.Close() })

	chain := newApoaTChain()
	genesis := apoaTGenesis(signer)
	chain.add(genesis)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		ParentHash: types.HexToHash("0xdead"), // not in chain
	}
	if err := eng.Prepare(chain, header); err == nil {
		t.Fatalf("expected error for unknown ancestor")
	}
}

func TestBlockNumberOrHashOrRLPUnmarshalJSONNumber(t *testing.T) {
	var sb blockNumberOrHashOrRLP
	if err := json.Unmarshal([]byte(`"0x1"`), &sb); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if sb.BlockNumberOrHash == nil {
		t.Fatalf("expected BlockNumberOrHash to be populated")
	}
	n, ok := sb.BlockNumberOrHash.Number()
	if !ok || n != 1 {
		t.Fatalf("expected number 1, got %v ok=%v", n, ok)
	}
}

func TestBlockNumberOrHashOrRLPUnmarshalJSONRLP(t *testing.T) {
	var sb blockNumberOrHashOrRLP
	// A JSON string that can't parse as a BlockNumberOrHash (too many hex
	// bytes to be a plausible block tag/hash) falls through to the raw-RLP
	// decode path.
	if err := json.Unmarshal([]byte(`"0x1234567890abcdef1234"`), &sb); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if len(sb.RLP) == 0 {
		t.Fatalf("expected RLP bytes to be populated")
	}
}

func TestBlockNumberOrHashOrRLPUnmarshalJSONInvalid(t *testing.T) {
	var sb blockNumberOrHashOrRLP
	if err := json.Unmarshal([]byte(`123`), &sb); err == nil {
		t.Fatalf("expected error for non-string, non-object JSON")
	}
}
