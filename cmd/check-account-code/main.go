// check-account-code: look up an account's codeHash in Account table, then
// check if that codeHash has bytecode in Code table.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

var emptyCodeHash = []byte{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}

func main() {
	dir := flag.String("dir", "", "datadir")
	addrHex := flag.String("addr", "", "20B addr hex")
	flag.Parse()
	if *dir == "" || *addrHex == "" {
		fmt.Fprintln(os.Stderr, "usage: check-account-code --dir <MDBX> --addr <20B hex>")
		os.Exit(1)
	}
	addr, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr")
		os.Exit(1)
	}
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	acctEnc, err := tx.GetOne(modules.Account, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "get account:", err)
		os.Exit(1)
	}
	if acctEnc == nil {
		fmt.Printf("account %x: NOT PRESENT\n", addr)
		return
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(acctEnc); err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
		os.Exit(1)
	}
	fmt.Printf("account %x:\n", addr)
	fmt.Printf("  balance: %s\n", a.Balance.String())
	fmt.Printf("  nonce:   %d\n", a.Nonce)
	fmt.Printf("  codeHash: %x\n", a.CodeHash)
	if bytes.Equal(a.CodeHash[:], emptyCodeHash) {
		fmt.Printf("  → empty code (EOA)\n")
		return
	}

	code, err := tx.GetOne(modules.Code, a.CodeHash[:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "get code:", err)
		os.Exit(1)
	}
	if code == nil {
		fmt.Printf("  → Code for %x NOT FOUND in Code table ❌\n", a.CodeHash)
	} else {
		fmt.Printf("  → Code present: %d bytes\n", len(code))
	}
}
