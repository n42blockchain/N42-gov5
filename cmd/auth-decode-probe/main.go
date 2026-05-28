// auth-decode-probe: decode a raw EIP-7702 SetCode tx hex blob with N42's RLP
// decoder and print every Authorization tuple's (chainId, address, nonce, V).
// Used to verify wire decode against mainnet (mainnet tx 0xb3b0848e... in block
// 25,191,537 has 26 auths all signed by the same signer — if N42 prints 26
// different (chainId, address, nonce) tuples, the bug is in the RLP layout).
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/transaction"
)

func main() {
	hexPath := flag.String("hex", "", "file containing raw tx hex (no 0x prefix)")
	flag.Parse()
	if *hexPath == "" {
		fmt.Fprintln(os.Stderr, "--hex required")
		os.Exit(2)
	}
	data, err := os.ReadFile(*hexPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}
	raw, err := hex.DecodeString(string(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hex decode: %v\n", err)
		os.Exit(1)
	}
	tx, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rlp decode: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("type=%d hash=%s\n", tx.Type(), tx.Hash().Hex())
	auths := tx.AuthList()
	if auths == nil {
		fmt.Println("(no auth list)")
		return
	}
	fmt.Printf("auth count: %d\n", len(auths))
	for i, a := range auths {
		if a == nil {
			fmt.Printf("[%d] nil\n", i)
			continue
		}
		fmt.Printf("[%2d] chainId=%s address=%x nonce=%d V=%s R=%s S=%s\n",
			i, &a.ChainID, a.Address, a.Nonce, a.V, a.R, a.S)
		signer, rerr := a.RecoverSigner()
		if rerr != nil {
			fmt.Printf("     recover ERR: %v\n", rerr)
		} else {
			fmt.Printf("     signer=%x\n", signer)
		}
	}
}
