// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"filippo.io/edwards25519"
)

func TestAltSigPolicyReferenceVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/alt_sig_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Encoded, Sender string }
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		raw, err := hex.DecodeString(strings.TrimPrefix(v.Encoded, "0x"))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := DecodeAltSigTransactionOffline(raw)
		if err != nil {
			t.Fatal(err)
		}
		sender, err := VerifyAltSigOffline(tx, 94)
		if err != nil || !strings.EqualFold(sender.Hex(), v.Sender) {
			t.Fatalf("valid reference policy: %v %s", err, sender.Hex())
		}
		if _, err := VerifyAltSigOffline(tx, 95); err == nil {
			t.Fatal("wrong chain accepted")
		}
		bad := tx.inner.copy().(*AltSigTx)
		bad.Nonce++
		if _, err := VerifyAltSigOffline(NewTx(bad), 94); err == nil {
			t.Fatal("modified payload accepted")
		}
		bad = tx.inner.copy().(*AltSigTx)
		bad.Signature[0] ^= 1
		if _, err := VerifyAltSigOffline(NewTx(bad), 94); err == nil {
			t.Fatal("bad R accepted")
		}
		bad = tx.inner.copy().(*AltSigTx)
		for i := 32; i < 64; i++ {
			bad.Signature[i] = 0xff
		}
		if _, err := VerifyAltSigOffline(NewTx(bad), 94); err == nil {
			t.Fatal("noncanonical S accepted")
		}
	}
}

// This order-eight generator is the maintained library's cofactor-test fixture:
// filippo.io/edwards25519 v1.1.1 extra_test.go TestMultByCofactor.
// Enumerating its subgroup exercises all eight small-order points without an
// incomplete hand-written identity blacklist. Generate all representable y+p
// and sign-bit aliases too; every candidate still represents a small-order point.
func TestAltSigPolicyAllSmallOrderPointsAndAliases(t *testing.T) {
	encoded, _ := hex.DecodeString("26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85")
	generator, err := new(edwards25519.Point).SetBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	point := edwards25519.NewIdentityPoint()
	seen := map[string]bool{}
	modulus := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	for i := 0; i < 8; i++ {
		canonical := point.Bytes()
		seen[string(canonical)] = true
		for _, sign := range []byte{0, 128} {
			base := bytes.Clone(canonical)
			base[31] = (base[31] & 127) | sign
			for alias := 0; alias < 2; alias++ {
				candidate := bytes.Clone(base)
				if alias == 1 {
					candidate[31] &= 127
					for a, b := 0, 31; a < b; a, b = a+1, b-1 {
						candidate[a], candidate[b] = candidate[b], candidate[a]
					}
					y := new(big.Int).SetBytes(candidate)
					y.Add(y, modulus)
					if y.BitLen() > 255 {
						continue
					}
					y.FillBytes(candidate)
					for a, b := 0, 31; a < b; a, b = a+1, b-1 {
						candidate[a], candidate[b] = candidate[b], candidate[a]
					}
					candidate[31] |= sign
				}
				decoded, err := new(edwards25519.Point).SetBytes(candidate)
				if err != nil {
					t.Fatal("alias fixture", err)
				}
				if new(edwards25519.Point).MultByCofactor(decoded).Equal(edwards25519.NewIdentityPoint()) != 1 {
					t.Fatal("fixture is not small order")
				}
				if err := validateAltSigPublicKey(candidate); err == nil {
					t.Fatalf("small-order encoding accepted: %x", candidate)
				}
			}
		}
		point.Add(point, generator)
	}
	if len(seen) != 8 || point.Equal(edwards25519.NewIdentityPoint()) != 1 {
		t.Fatal("fixture does not generate exactly eight torsion points")
	}
	// Demonstrate why bare stdlib verification cannot serve as the protocol gate.
	var forged AltSigTx
	forged.PublicKey[0] = 1
	forged.Signature[0] = 1
	hash, _ := AltSigSigningHash(&forged)
	if !ed25519.Verify(forged.PublicKey[:], hash[:], forged.Signature[:]) {
		t.Fatal("stdlib acceptance probe changed; review rule assumptions")
	}
	if _, err := VerifyAltSigOffline(NewTx(&forged), 0); err == nil {
		t.Fatal("identity forgery passed protocol policy")
	}
}

func TestAltSigPolicyInvalidPointsAndPrivateKeys(t *testing.T) {
	if err := validateAltSigPublicKey(make([]byte, 31)); err == nil {
		t.Fatal("short public key")
	}
	invalid := make([]byte, 32)
	invalid[0] = 2 // y=2 has no curve x.
	if _, err := new(edwards25519.Point).SetBytes(invalid); err == nil {
		t.Fatal("invalid-point fixture unexpectedly decodes")
	}
	if err := validateAltSigPublicKey(invalid); err == nil {
		t.Fatal("invalid curve point")
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	key[63] ^= 1
	if _, err := SignAltTx(&AltSigTx{}, key); err == nil {
		t.Fatal("inconsistent private-key public suffix accepted")
	}
	if _, err := SignAltTx(&AltSigTx{}, key[:63]); err == nil {
		t.Fatal("bad private-key length accepted")
	}
	if _, err := VerifyAltSigOffline(nil, 0); err == nil {
		t.Fatal("nil transaction accepted")
	}
	if _, err := VerifyAltSigOffline(NewTx(&LegacyTx{}), 0); err == nil {
		t.Fatal("wrong carrier type accepted")
	}
}
