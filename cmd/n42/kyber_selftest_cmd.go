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

	pke "github.com/n42blockchain/N42/crypto/pke/kyber/kyber512"
)

// kyberSelftestCommand exercises crypto/pke/kyber/kyber512 (the
// Kyber512.CPAPKE public key encryption of the NIST PQC round 3 submission,
// which crypto/kem/kyber is built on). Nothing in the node uses Kyber yet;
// this command is its entry point outside the crypto tree.
var kyberSelftestCommand = &cli.Command{
	Name:  "kyber-selftest",
	Usage: "Self-test of crypto/pke/kyber/kyber512 (Kyber512 CPA-PKE): key, encrypt/decrypt and pack/unpack round trips",
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
		st, err := kyberSelftest(master, rounds)
		if err != nil {
			return err
		}
		fmt.Printf("kyber512 CPA-PKE self-test passed: rounds=%d seed=%s\n", rounds, hex.EncodeToString(master))
		fmt.Printf("sizes: public key %d, private key %d, ciphertext %d, plaintext %d bytes\n",
			pke.PublicKeySize, pke.PrivateKeySize, pke.CiphertextSize, pke.PlaintextSize)
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
// of crypto/pke/kyber/kyber512 from master, and checks that keys are
// deterministic in their seed and survive pack/unpack.
func kyberSelftest(master []byte, rounds int) (kyberSelftestStats, error) {
	var st kyberSelftestStats
	for i := 0; i < rounds; i++ {
		keySeed := kyberDerive(master, i, 'k', pke.KeySeedSize)
		encSeed := kyberDerive(master, i, 'e', pke.EncryptionSeedSize)
		pt := kyberDerive(master, i, 'm', pke.PlaintextSize)

		t0 := time.Now()
		pk, sk := pke.NewKeyFromSeed(keySeed)
		st.keygen += time.Since(t0)

		ct := make([]byte, pke.CiphertextSize)
		t0 = time.Now()
		pk.EncryptTo(ct, pt, encSeed)
		st.encrypt += time.Since(t0)

		got := make([]byte, pke.PlaintextSize)
		t0 = time.Now()
		sk.DecryptTo(got, ct)
		st.decrypt += time.Since(t0)
		if !bytes.Equal(got, pt) {
			return st, fmt.Errorf("kyber-selftest: round %d: decrypted plaintext differs", i)
		}

		// Pack / unpack round trip.
		pkb := make([]byte, pke.PublicKeySize)
		skb := make([]byte, pke.PrivateKeySize)
		pk.Pack(pkb)
		sk.Pack(skb)
		var pk2 pke.PublicKey
		var sk2 pke.PrivateKey
		pk2.Unpack(pkb)
		sk2.Unpack(skb)
		ct2 := make([]byte, pke.CiphertextSize)
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
		other := kyberDerive(master, i, 'o', pke.EncryptionSeedSize)
		pk.EncryptTo(ct2, pt, other)
		if bytes.Equal(ct2, ct) {
			return st, fmt.Errorf("kyber-selftest: round %d: ciphertext does not depend on the encryption seed", i)
		}
		sk.DecryptTo(got, ct2)
		if !bytes.Equal(got, pt) {
			return st, fmt.Errorf("kyber-selftest: round %d: second ciphertext decrypts differently", i)
		}

		// The same seed must give the same key pair.
		pk3, sk3 := pke.NewKeyFromSeed(keySeed)
		pkb3 := make([]byte, pke.PublicKeySize)
		skb3 := make([]byte, pke.PrivateKeySize)
		pk3.Pack(pkb3)
		sk3.Pack(skb3)
		if !bytes.Equal(pkb3, pkb) || !bytes.Equal(skb3, skb) {
			return st, fmt.Errorf("kyber-selftest: round %d: key pair is not deterministic in its seed", i)
		}
	}
	return st, nil
}
