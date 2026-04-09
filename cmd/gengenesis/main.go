// Standalone tool to generate Ethereum mainnet genesis.json from go-ethereum's
// RLP-encoded genesis alloc data. Build: go build ./cmd/gengenesis
package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: gengenesis <output.json>\n")
		os.Exit(1)
	}

	var p []struct {
		Addr    *big.Int
		Balance *big.Int
		Misc    *struct {
			Nonce uint64
			Code  []byte
			Slots []struct {
				Key common.Hash
				Val common.Hash
			}
		} `rlp:"optional"`
	}
	if err := rlp.NewStream(strings.NewReader(mainnetAllocData), 0).Decode(&p); err != nil {
		fmt.Fprintf(os.Stderr, "rlp decode: %v\n", err)
		os.Exit(1)
	}

	type allocEntry struct {
		Balance string            `json:"balance"`
		Code    string            `json:"code,omitempty"`
		Nonce   uint64            `json:"nonce,omitempty"`
		Storage map[string]string `json:"storage,omitempty"`
	}

	alloc := make(map[string]allocEntry, len(p))
	for _, account := range p {
		addr := common.BigToAddress(account.Addr)
		entry := allocEntry{Balance: account.Balance.String()}
		if account.Misc != nil {
			entry.Nonce = account.Misc.Nonce
			if len(account.Misc.Code) > 0 {
				entry.Code = fmt.Sprintf("0x%x", account.Misc.Code)
			}
			if len(account.Misc.Slots) > 0 {
				entry.Storage = make(map[string]string, len(account.Misc.Slots))
				for _, s := range account.Misc.Slots {
					entry.Storage[s.Key.Hex()] = s.Val.Hex()
				}
			}
		}
		alloc[addr.Hex()] = entry
	}

	out := map[string]interface{}{"alloc": alloc}
	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "json marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[1], data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d accounts to %s\n", len(alloc), os.Args[1])
}
