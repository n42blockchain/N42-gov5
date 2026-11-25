// p1-many-keys derives the profile-1 test keys of validators 0..N-1 and, when a
// genesis file is given, checks every BLS public key against its validator set.
//
// Usage: p1-many-keys [-genesis genesis.json] [-n 100]
// Output, one line per index: <i> <bls-secret-hex> <bls-pub-hex> <net-key-hex> <peer-id>
// BLS secret = 0x00 || sha256("n42-profile1-test-validator-<i>")[1:].
// Network key: indices 0..4 are the fixed keys of run-p1mixed.sh (0x11.., 0x22.., ...);
// i >= 5 is sha256("n42-p1many-net-<i>").
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/crypto/bls"
)

func main() {
	gen := flag.String("genesis", "", "genesis file to check BLS public keys against")
	n := flag.Int("n", 100, "number of validators")
	flag.Parse()
	var want []string
	if *gen != "" {
		raw, err := os.ReadFile(*gen)
		if err != nil {
			fatal(err)
		}
		var g struct {
			Config struct {
				Hotstuff struct {
					Validators []struct {
						BlsKey string `json:"blsKey"`
					} `json:"validators"`
				} `json:"hotstuff"`
			} `json:"config"`
		}
		if err := json.Unmarshal(raw, &g); err != nil {
			fatal(err)
		}
		for _, v := range g.Config.Hotstuff.Validators {
			want = append(want, strings.ToLower(v.BlsKey))
		}
	}
	bad := 0
	for i := 0; i < *n; i++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("n42-profile1-test-validator-%d", i)))
		sec := append([]byte{0}, h[1:]...)
		sk, err := bls.SecretKeyFromBytes(sec)
		if err != nil {
			fatal(err)
		}
		pub := "0x" + hex.EncodeToString(sk.PublicKey().Marshal())
		var nk []byte
		if i < 5 {
			nk = bytes.Repeat([]byte{byte(0x11 * (i + 1))}, 32)
		} else {
			x := sha256.Sum256([]byte(fmt.Sprintf("n42-p1many-net-%d", i)))
			nk = x[:]
		}
		pk, err := crypto.UnmarshalSecp256k1PrivateKey(nk)
		if err != nil {
			fatal(err)
		}
		id, err := peer.IDFromPrivateKey(pk)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("%d %s %s %s %s\n", i, hex.EncodeToString(sec), pub, hex.EncodeToString(nk), id)
		if want != nil && (i >= len(want) || want[i] != pub) {
			bad++
			fmt.Fprintf(os.Stderr, "MISMATCH validator %d\n", i)
		}
	}
	if want != nil {
		fmt.Fprintf(os.Stderr, "checked %d: %d mismatches (genesis has %d)\n", *n, bad, len(want))
		if bad > 0 {
			os.Exit(1)
		}
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
