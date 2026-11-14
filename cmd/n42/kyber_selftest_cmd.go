// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/urfave/cli/v2"

	legacy "github.com/n42blockchain/N42/crypto/kyber/kyber512"
	pke "github.com/n42blockchain/N42/crypto/pke/kyber/kyber512"
)

// kyberSelftestCommand exercises crypto/kyber/kyber512 (the Kyber512.CPAPKE
// public key encryption of the NIST PQC round 3 submission). The package
// has no other importer; this command is its only entry point and checks
// it against crypto/pke/kyber/kyber512, the copy the KEM is built on.
var kyberSelftestCommand = &cli.Command{
	Name:  "kyber-selftest",
	Usage: "Self-test of crypto/kyber/kyber512 (Kyber512 CPA-PKE): round trips and cross-check with crypto/pke/kyber",
	Flags: []cli.Flag{
		&cli.IntFlag{
			Name:  "rounds",
			Usage: "Number of key pairs to generate and round-trip",
			Value: 100,
		},
		&cli.StringFlag{
			Name:  "seed",
			Usage: "Hex seed the round seeds are derived from (default: random)",
		},
		&cli.BoolFlag{
			Name:  "compare",
			Usage: "Also check byte equality with crypto/pke/kyber/kyber512 and decrypt across the two",
			Value: true,
		},
	},
	Action: func(ctx *cli.Context) error {
		rounds := ctx.Int("rounds")
		if rounds <= 0 {
			return errors.New("kyber-selftest: --rounds must be positive")
		}
		var master []byte
		if s := ctx.String("seed"); s != "" {
			b, err := hex.DecodeString(s)
			if err != nil {
				return fmt.Errorf("kyber-selftest: --seed: %w", err)
			}
			master = b
		} else {
			master = make([]byte, 32)
			if _, err := rand.Read(master); err != nil {
				return err
			}
		}
		st, err := kyberSelftest(master, rounds, ctx.Bool("compare"))
		if err != nil {
			return err
		}
		fmt.Printf("kyber512 CPA-PKE self-test passed: rounds=%d compare=%v seed=%s\n", rounds, ctx.Bool("compare"), hex.EncodeToString(master))
		fmt.Printf("sizes: public key %d, private key %d, ciphertext %d, plaintext %d bytes\n",
			legacy.PublicKeySize, legacy.PrivateKeySize, legacy.CiphertextSize, legacy.PlaintextSize)
		fmt.Printf("per operation: keygen %v, encrypt %v, decrypt %v\n",
			st.keygen/time.Duration(rounds), st.encrypt/time.Duration(rounds), st.decrypt/time.Duration(rounds))
		return nil
	},
}

type kyberSelftestStats struct {
	keygen, encrypt, decrypt time.Duration
}

// kyberDerive returns n bytes for round i and a label, from the master seed.
func kyberDerive(master []byte, i int, label byte, n int) []byte {
	out := make([]byte, 0, n+sha256.Size)
	var ctr [9]byte
	binary.BigEndian.PutUint64(ctr[:8], uint64(i))
	ctr[8] = label
	for c := byte(0); len(out) < n; c++ {
		h := sha256.New()
		h.Write(master)
		h.Write(ctr[:])
		h.Write([]byte{c})
		out = h.Sum(out)
	}
	return out[:n]
}

// kyberSelftest runs rounds key generations, encryptions and decryptions
// of crypto/kyber/kyber512 from master, and with compare checks that
// crypto/pke/kyber/kyber512 produces the same keys and ciphertexts and
// that each decrypts the other's ciphertext.
func kyberSelftest(master []byte, rounds int, compare bool) (kyberSelftestStats, error) {
	var st kyberSelftestStats
	for i := 0; i < rounds; i++ {
		keySeed := kyberDerive(master, i, 'k', legacy.KeySeedSize)
		encSeed := kyberDerive(master, i, 'e', legacy.EncryptionSeedSize)
		pt := kyberDerive(master, i, 'm', legacy.PlaintextSize)

		t0 := time.Now()
		pk, sk := legacy.NewKeyFromSeed(keySeed)
		st.keygen += time.Since(t0)

		ct := make([]byte, legacy.CiphertextSize)
		t0 = time.Now()
		pk.EncryptTo(ct, pt, encSeed)
		st.encrypt += time.Since(t0)

		got := make([]byte, legacy.PlaintextSize)
		t0 = time.Now()
		sk.DecryptTo(got, ct)
		st.decrypt += time.Since(t0)
		if !bytes.Equal(got, pt) {
			return st, fmt.Errorf("kyber-selftest: round %d: decrypted plaintext differs", i)
		}

		// Pack / unpack round trip.
		pkb := make([]byte, legacy.PublicKeySize)
		skb := make([]byte, legacy.PrivateKeySize)
		pk.Pack(pkb)
		sk.Pack(skb)
		var pk2 legacy.PublicKey
		var sk2 legacy.PrivateKey
		pk2.Unpack(pkb)
		sk2.Unpack(skb)
		ct2 := make([]byte, legacy.CiphertextSize)
		pk2.EncryptTo(ct2, pt, encSeed)
		if !bytes.Equal(ct2, ct) {
			return st, fmt.Errorf("kyber-selftest: round %d: unpacked public key encrypts differently", i)
		}
		sk2.DecryptTo(got, ct)
		if !bytes.Equal(got, pt) {
			return st, fmt.Errorf("kyber-selftest: round %d: unpacked private key decrypts differently", i)
		}

		// A different encryption seed must give a different ciphertext
		// that still decrypts.
		other := kyberDerive(master, i, 'o', legacy.EncryptionSeedSize)
		pk.EncryptTo(ct2, pt, other)
		if bytes.Equal(ct2, ct) {
			return st, fmt.Errorf("kyber-selftest: round %d: ciphertext does not depend on the encryption seed", i)
		}
		sk.DecryptTo(got, ct2)
		if !bytes.Equal(got, pt) {
			return st, fmt.Errorf("kyber-selftest: round %d: second ciphertext decrypts differently", i)
		}

		if !compare {
			continue
		}
		if legacy.PublicKeySize != pke.PublicKeySize || legacy.PrivateKeySize != pke.PrivateKeySize ||
			legacy.CiphertextSize != pke.CiphertextSize || legacy.PlaintextSize != pke.PlaintextSize {
			return st, errors.New("kyber-selftest: parameter sizes differ from crypto/pke/kyber/kyber512")
		}
		rpk, rsk := pke.NewKeyFromSeed(keySeed)
		rpkb := make([]byte, pke.PublicKeySize)
		rskb := make([]byte, pke.PrivateKeySize)
		rpk.Pack(rpkb)
		rsk.Pack(rskb)
		if !bytes.Equal(rpkb, pkb) || !bytes.Equal(rskb, skb) {
			return st, fmt.Errorf("kyber-selftest: round %d: key pair differs from crypto/pke/kyber/kyber512", i)
		}
		rct := make([]byte, pke.CiphertextSize)
		rpk.EncryptTo(rct, pt, encSeed)
		if !bytes.Equal(rct, ct) {
			return st, fmt.Errorf("kyber-selftest: round %d: ciphertext differs from crypto/pke/kyber/kyber512", i)
		}
		rsk.DecryptTo(got, ct)
		if !bytes.Equal(got, pt) {
			return st, fmt.Errorf("kyber-selftest: round %d: crypto/pke/kyber/kyber512 does not decrypt this ciphertext", i)
		}
		sk.DecryptTo(got, rct)
		if !bytes.Equal(got, pt) {
			return st, fmt.Errorf("kyber-selftest: round %d: this package does not decrypt crypto/pke/kyber/kyber512's ciphertext", i)
		}
	}
	return st, nil
}
