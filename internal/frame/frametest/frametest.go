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

// Package frametest builds gateway-signed frames of real signed transfers
// for tests across packages.
package frametest

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
)

// Keys returns n deterministic private keys.
func Keys(tb testing.TB, n int, salt byte) []*ecdsa.PrivateKey {
	tb.Helper()
	out := make([]*ecdsa.PrivateKey, n)
	for i := range out {
		seed := crypto.Keccak256([]byte{salt, byte(i), byte(i >> 8), byte(i >> 16)})
		k, err := crypto.ToECDSA(seed)
		if err != nil {
			tb.Fatal(err)
		}
		out[i] = k
	}
	return out
}

// Builder signs transfers from a fixed sender set, tracking each sender's
// next nonce, and packs them into frames signed by Gateway.
type Builder struct {
	Gateway  *ecdsa.PrivateKey
	ChainID  *big.Int
	Senders  []*ecdsa.PrivateKey
	Nonces   []uint64
	Gas      uint64
	FeeCap   uint64
	To       types.Address
	seq      uint64
	next     int
	signer   transaction.Signer
	chainU64 *uint256.Int
}

// NewBuilder returns a builder over senders with every nonce at zero.
func NewBuilder(gw *ecdsa.PrivateKey, chainID *big.Int, senders []*ecdsa.PrivateKey) *Builder {
	return &Builder{
		Gateway: gw, ChainID: chainID, Senders: senders, Nonces: make([]uint64, len(senders)),
		Gas: 21000, FeeCap: 1e10, To: types.HexToAddress("0x00000000000000000000000000000000000000ee"),
		signer: transaction.LatestSignerForChainID(chainID), chainU64: uint256.MustFromBig(chainID),
	}
}

// Tx signs the next transfer (senders round-robin). nonceDelta shifts the
// nonce used without advancing the tracked one (negative = stale).
func (b *Builder) Tx(tb testing.TB, nonceDelta int64) *transaction.Transaction {
	tb.Helper()
	i := b.next
	b.next = (b.next + 1) % len(b.Senders)
	nonce := uint64(int64(b.Nonces[i]) + nonceDelta)
	if nonceDelta == 0 {
		b.Nonces[i]++
	}
	to := b.To
	tx, err := transaction.SignNewTx(b.Senders[i], b.signer, &transaction.DynamicFeeTx{
		ChainID:   b.chainU64,
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(b.FeeCap),
		Gas:       b.Gas,
		To:        &to,
		Value:     uint256.NewInt(1),
	})
	if err != nil {
		tb.Fatal(err)
	}
	return tx
}

// Frame packs txs into one Ethereum-typed frame.
func (b *Builder) Frame(tb testing.TB, txs []*transaction.Transaction) ([]byte, types.Hash) {
	tb.Helper()
	enc := make([][]byte, len(txs))
	for i, tx := range txs {
		e, err := tx.EthEncoded()
		if err != nil {
			tb.Fatal(err)
		}
		enc[i] = e
	}
	b.seq++
	raw, h, err := frame.Encode(b.Gateway, b.seq, frame.TxTypeEthereum, enc)
	if err != nil {
		tb.Fatal(err)
	}
	return raw, h
}

// Frames builds n frames of perFrame valid transfers each.
func (b *Builder) Frames(tb testing.TB, n, perFrame int) (raws [][]byte, hashes []types.Hash, txs []*transaction.Transaction) {
	tb.Helper()
	for f := 0; f < n; f++ {
		batch := make([]*transaction.Transaction, perFrame)
		for i := range batch {
			batch[i] = b.Tx(tb, 0)
		}
		raw, h := b.Frame(tb, batch)
		raws = append(raws, raw)
		hashes = append(hashes, h)
		txs = append(txs, batch...)
	}
	return raws, hashes, txs
}
