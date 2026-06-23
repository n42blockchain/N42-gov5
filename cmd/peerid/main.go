// Command peerid derives the libp2p peer ID from a hex secp256k1 private key,
// matching the derivation N42's p2p layer uses (internal/p2p/options.go:
// crypto.UnmarshalSecp256k1PrivateKey -> peer.IDFromPublicKey). Used to
// pre-compute static-peer multiaddrs for a local hotstuff testnet.
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func main() {
	for _, h := range os.Args[1:] {
		h = strings.TrimPrefix(strings.TrimSpace(h), "0x")
		b, err := hex.DecodeString(h)
		if err != nil {
			fmt.Fprintf(os.Stderr, "decode %s: %v\n", h, err)
			os.Exit(1)
		}
		priv, err := crypto.UnmarshalSecp256k1PrivateKey(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "unmarshal %s: %v\n", h, err)
			os.Exit(1)
		}
		id, err := peer.IDFromPublicKey(priv.GetPublic())
		if err != nil {
			fmt.Fprintf(os.Stderr, "peerid %s: %v\n", h, err)
			os.Exit(1)
		}
		fmt.Printf("%s %s\n", h[:8], id.String())
	}
}
