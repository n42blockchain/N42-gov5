// block-senders-list reads a block's body from geth ancient, runs
// ecrecover on every transaction, and prints (idx, sender, nonce). Used
// to verify that an existing senders archive captured every sender
// correctly — when senders archive shows only 1 entry for an address but
// reth's PlainAccountState implies the address sent multiple txs in the
// same block, we need ground truth on whether the body really has
// multiple txs from that address.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

func main() {
	ancient := flag.String("ancient", `d:\geth\geth\chaindata\ancient\chain`, "geth ancient dir")
	block := flag.Uint64("block", 0, "block number")
	filterAddr := flag.String("addr", "", "if set, only print txs whose sender matches this 20B hex")
	flag.Parse()

	f, err := freezer.NewReadOnly(*ancient)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open ancient:", err)
		os.Exit(1)
	}
	defer f.Close()

	hData, err := f.Ancient(freezer.TableHeaders, *block)
	if err != nil {
		fmt.Fprintln(os.Stderr, "header:", err)
		os.Exit(1)
	}
	hdr, err := ethel.DecodeGethHeader(hData)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode header:", err)
		os.Exit(1)
	}

	bData, err := f.Ancient(freezer.TableBodies, *block)
	if err != nil {
		fmt.Fprintln(os.Stderr, "body:", err)
		os.Exit(1)
	}
	body, err := ethel.DecodeGethBody(bData)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode body:", err)
		os.Exit(1)
	}

	signer := transaction.MakeSigner(params.EthereumMainnetChainConfig, hdr.Number.ToBig())

	wanted := strings.ToLower(strings.TrimPrefix(*filterAddr, "0x"))
	totalMatch := 0
	fmt.Printf("block %d has %d txs\n", *block, len(body.Transactions))
	for i, tx := range body.Transactions {
		sender, err := transaction.Sender(signer, tx)
		if err != nil {
			fmt.Printf("  [%3d] SENDER RECOVER FAIL: %v\n", i, err)
			continue
		}
		senderHex := fmt.Sprintf("%x", sender[:])
		if wanted != "" && !strings.EqualFold(senderHex, wanted) {
			continue
		}
		toStr := "create"
		if tx.To() != nil {
			to := *tx.To()
			toStr = fmt.Sprintf("0x%x", to[:])
		}
		fmt.Printf("  [%3d] sender=0x%s nonce=%d to=%s type=%d hash=%s\n",
			i, senderHex, tx.Nonce(), toStr, tx.Type(), tx.Hash().Hex())
		totalMatch++
	}
	if wanted != "" {
		fmt.Printf("matches for addr=%s: %d\n", wanted, totalMatch)
	}
	_ = types.Hash{}
}
