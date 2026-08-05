// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// TestEncodedSizeMatchesEncoding pins the memo: the cached size must equal the
// real encoding's length, and a second call must serve the cached value.
func TestEncodedSizeMatchesEncoding(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	chainID := uint256.NewInt(94)
	to := types.Address{0xaa}
	signer := LatestSignerForChainID(big.NewInt(94))
	tx, err := SignNewTx(key, signer, &DynamicFeeTx{
		ChainID: chainID, Nonce: 7, GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1e10), Gas: 21000, To: &to,
		Value: uint256.NewInt(5), Data: []byte{0xde, 0xad},
	})
	if err != nil {
		t.Fatal(err)
	}

	enc, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tx.EncodedSize()
	if err != nil {
		t.Fatal(err)
	}
	if got != len(enc) {
		t.Fatalf("EncodedSize=%d, real encoding is %d bytes", got, len(enc))
	}
	// Second call: cached.
	if again, err := tx.EncodedSize(); err != nil || again != got {
		t.Fatalf("cached EncodedSize=%d err=%v, want %d", again, err, got)
	}
}
