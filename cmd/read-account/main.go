// read-account: dump an account record from N42's PlainState DB
// (nonce / balance / codeHash / code length).
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	dir := flag.String("dir", `d:\n42-eth037`, "datadir")
	addrHex := flag.String("addr", "0000000000007f150bd6f54c40a34d7c3d5e9f56", "address (hex, 20 bytes)")
	flag.Parse()

	addr, err := hex.DecodeString(*addrHex)
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr:", err)
		os.Exit(1)
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).Readonly().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open mdbx:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin ro:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	enc, err := tx.GetOne(modules.Account, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "get account:", err)
		os.Exit(1)
	}
	if enc == nil {
		fmt.Printf("addr=%s -> NOT PRESENT\n", *addrHex)
		return
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
		os.Exit(1)
	}
	fmt.Printf("addr=%s nonce=%d balance=%s codeHash=%x\n", *addrHex, a.Nonce, a.Balance.String(), a.CodeHash)

	if a.CodeHash != ([32]byte{}) {
		code, err := tx.GetOne(modules.Code, a.CodeHash[:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "get code:", err)
			os.Exit(1)
		}
		if code == nil {
			fmt.Printf("code: MISSING (codeHash %x not in code table)\n", a.CodeHash)
		} else {
			fmt.Printf("code: %d bytes, first 32: %x\n", len(code), code[:min(32, len(code))])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
