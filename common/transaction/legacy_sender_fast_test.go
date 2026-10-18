// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// TestLegacySenderFastMatchesBig: the uint64 legacy recovery returns what
// the big.Int path returns -- sender or error -- for real signatures and
// for every boundary of V: unprotected values, the chain id derivation,
// the recovery value's 8-bit limit and a V below 2*chainId+8.
func TestLegacySenderFastMatchesBig(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	to := types.Address{9}
	for _, cid := range []int64{0, 1, 5, 1337, 1 << 40} {
		chainID := big.NewInt(cid)
		signers := []Signer{NewEIP155Signer(chainID), NewEIP2930Signer(chainID), NewLondonSigner(chainID)}
		signed, err := SignTx(NewTx(&LegacyTx{Nonce: 3, GasPrice: uint256.NewInt(7), Gas: 21000, To: &to, Value: uint256.NewInt(5)}),
			NewEIP155Signer(chainID), key)
		if err != nil {
			t.Fatal(err)
		}
		_, R, S := signed.RawSignatureValues()
		mul := uint64(2 * cid)
		vs := []uint64{0, 1, 2, 26, 27, 28, 29, 30, 34, 35, 36, 37, 255, 256,
			mul, mul + 7, mul + 8, mul + 8 + 27, mul + 35, mul + 36, mul + 37, mul + 8 + 255, mul + 8 + 256, math.MaxUint64}
		for _, v := range vs {
			for _, sg := range signers {
				tx := NewTx(&LegacyTx{Nonce: 3, GasPrice: uint256.NewInt(7), Gas: 21000, To: &to, Value: uint256.NewInt(5),
					V: uint256.NewInt(v), R: R.Clone(), S: S.Clone()})
				fastAddr, fastErr := sg.Sender(tx)
				legacySenderFastOffForTest = true
				slowTx := NewTx(&LegacyTx{Nonce: 3, GasPrice: uint256.NewInt(7), Gas: 21000, To: &to, Value: uint256.NewInt(5),
					V: uint256.NewInt(v), R: R.Clone(), S: S.Clone()})
				slowAddr, slowErr := sg.Sender(slowTx)
				legacySenderFastOffForTest = false
				if fastAddr != slowAddr || (fastErr == nil) != (slowErr == nil) || (fastErr != nil && fastErr.Error() != slowErr.Error()) {
					t.Fatalf("chain %d %T v=%d: fast (%x, %v), big (%x, %v)", cid, sg, v, fastAddr, fastErr, slowAddr, slowErr)
				}
			}
		}
		// The real signature recovers its key (chain id 0 signs unprotected,
		// whose Homestead hash is another).
		want := crypto.PubkeyToAddress(key.PublicKey)
		for _, sg := range signers {
			if cid == 0 {
				break
			}
			if got, err := sg.Sender(NewTx(signed.inner.copy())); err != nil || got != want {
				t.Fatalf("chain %d %T: sender %x, %v; want %x", cid, sg, got, err, want)
			}
		}
	}
}

func TestLegacySenderFastAllocs(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := types.Address{9}
	sg := NewLondonSigner(big.NewInt(1337))
	signed, err := SignTx(NewTx(&LegacyTx{Nonce: 3, GasPrice: uint256.NewInt(7), Gas: 21000, To: &to, Value: uint256.NewInt(5)}), sg, key)
	if err != nil {
		t.Fatal(err)
	}
	fast := testing.AllocsPerRun(50, func() { _, _ = sg.Sender(signed) })
	legacySenderFastOffForTest = true
	slow := testing.AllocsPerRun(50, func() { _, _ = sg.Sender(signed) })
	legacySenderFastOffForTest = false
	t.Logf("allocations per legacy recovery: %v (big.Int path %v)", fast, slow)
	if fast >= slow {
		t.Fatalf("fast path allocates %v, big.Int path %v", fast, slow)
	}
}
