// reth-acct-dump prints the raw PlainAccountState bytes for a given address.
// Used to verify the assumed compact-encoding layout against ground truth.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const tblAcct = "PlainAccountState"

func cfg(d kv.TableCfg) kv.TableCfg {
	d[tblAcct] = kv.TableCfgItem{}
	return d
}

func main() {
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	addrStr := flag.String("addr", "", "20B addr hex (no 0x prefix)")
	flag.Parse()

	if *addrStr == "" {
		fmt.Fprintln(os.Stderr, "--addr required")
		os.Exit(2)
	}
	addr, err := hex.DecodeString(*addrStr)
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "addr must be 20B hex")
		os.Exit(2)
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*rethDir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
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
	v, err := tx.GetOne(tblAcct, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "get:", err)
		os.Exit(1)
	}
	if v == nil {
		fmt.Printf("addr=%s: NOT FOUND in PlainAccountState\n", *addrStr)
		return
	}
	fmt.Printf("addr=%s len=%d raw=0x%x\n", *addrStr, len(v), v)
	if len(v) >= 2 {
		flags := uint16(v[0]) | (uint16(v[1]) << 8)
		fmt.Printf("flags(LE u16)=0x%04x: nonceLen=%d balanceLen=%d hasCodeHash=%v\n",
			flags, flags&0x0f, (flags>>4)&0x3f, flags&(1<<10) != 0)
	}
}
