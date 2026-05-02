// get-code: look up bytecode in N42 Code table by codeHash, OR scan
// PlainState account for an addr, extract codeHash, and look it up.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	dir := flag.String("dir", "", "n42 datadir")
	addrCSV := flag.String("addrs", "", "comma-separated 20-byte addresses (hex)")
	hashCSV := flag.String("hashes", "", "comma-separated 32-byte codeHashes (hex) to look up directly")
	flag.Parse()

	if *dir == "" || (*addrCSV == "" && *hashCSV == "") {
		fmt.Fprintln(os.Stderr, "usage: get-code --dir <DATADIR> [--addrs ADDR,...] [--hashes HASH,...]")
		os.Exit(2)
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	if *hashCSV != "" {
		for _, h := range strings.Split(*hashCSV, ",") {
			h = strings.TrimPrefix(strings.TrimSpace(h), "0x")
			hb, err := hex.DecodeString(h)
			if err != nil || len(hb) != 32 {
				fmt.Printf("[%s] bad hash\n", h)
				continue
			}
			code, err := tx.GetOne(modules.Code, hb)
			if err != nil {
				fmt.Printf("[%x] err: %v\n", hb, err)
				continue
			}
			if len(code) == 0 {
				fmt.Printf("[%x] NOT IN Code table\n", hb)
			} else {
				fmt.Printf("[%x] len=%d prefix=%x\n", hb, len(code), code[:min(20, len(code))])
			}
		}
	}

	if *addrCSV != "" {
		for _, a := range strings.Split(*addrCSV, ",") {
			a = strings.TrimPrefix(strings.TrimSpace(a), "0x")
			ab, err := hex.DecodeString(a)
			if err != nil || len(ab) != 20 {
				fmt.Printf("[%s] bad addr\n", a)
				continue
			}
			acct, err := tx.GetOne(modules.Account, ab)
			if err != nil {
				fmt.Printf("[%x] account err: %v\n", ab, err)
				continue
			}
			if acct == nil {
				fmt.Printf("[%x] account NOT IN PlainState\n", ab)
				continue
			}
			fmt.Printf("[%x] account: %d bytes raw=%x\n", ab, len(acct), acct)
			// Try every 32-byte alignment as codeHash candidate
			found := false
			for i := 0; i+32 <= len(acct); i++ {
				cand := acct[i : i+32]
				code, _ := tx.GetOne(modules.Code, cand)
				if len(code) > 0 {
					fmt.Printf("    codeHash=%x → code len=%d, prefix=%x\n",
						cand, len(code), code[:min(20, len(code))])
					found = true
				}
			}
			if !found {
				fmt.Printf("    no code in any 32-byte substring of account encoding\n")
			}
			fmt.Println()
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
