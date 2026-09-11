// Command qs-acct-probe compares, on a STOPPED node's chaindata, the plain
// `Account` row and the QMDB tree value for the dev faucet and the coinbases of
// the last N canonical blocks, and the reloaded tree root against the head's
// state root. Round 26's diagnostic.
//
//	qs-acct-probe -datadir /data/blockchain/qs-node0/chaindata [-n 7]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/qmdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func main() {
	dir := flag.String("datadir", "", "chaindata dir")
	n := flag.Int("n", 7, "coinbases of the last n canonical blocks")
	extra := flag.String("addr", "42e9819036f61bF665D5f727E8C03121f12f586e", "extra address (dev faucet)")
	flag.Parse()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.NewMDBX(log2.New()).Path(*dir).Label(kv.ChainDB).Readonly().Open(context.Background())
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

	headHash := rawdb.ReadHeadBlockHash(tx)
	head, err := rawdb.ReadHeaderByHash(tx, headHash)
	if err != nil || head == nil {
		fmt.Fprintln(os.Stderr, "head header:", err)
		os.Exit(1)
	}
	num := head.Number.Uint64()
	fmt.Printf("head %d %x stateRoot %x\n", num, headHash[:6], head.Root[:8])

	rc := commitment.NewQMDBRootComputer()
	rc.SetCold(tx)
	if err := rc.LoadFrom(tx); err != nil {
		fmt.Fprintln(os.Stderr, "qmdb load:", err)
		os.Exit(1)
	}
	root := rc.Root()
	fmt.Printf("qmdb reloaded root %x  matches head: %v\n", root[:8], root == head.Root)

	addrs := []types.Address{types.HexToAddress(*extra)}
	for i := 0; i < *n; i++ {
		h, err := rawdb.ReadCanonicalHash(tx, num-uint64(i))
		if err != nil {
			continue
		}
		hdr := rawdb.ReadHeader(tx, h, num-uint64(i))
		if hdr == nil {
			continue
		}
		seen := false
		for _, a := range addrs {
			if a == hdr.Coinbase {
				seen = true
			}
		}
		if !seen {
			addrs = append(addrs, hdr.Coinbase)
		}
	}
	dec := func(enc []byte) string {
		if len(enc) == 0 {
			return "absent"
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(enc); err != nil {
			return "decode-error"
		}
		return fmt.Sprintf("nonce=%d bal=%s", a.Nonce, a.Balance.String())
	}
	for _, a := range addrs {
		plain, _ := tx.GetOne(modules.Account, a[:])
		q, _ := rc.Tree().Get(qmdb.Hash(commitment.AccountKeyHash(a)))
		p, t := dec(plain), dec(q)
		mark := "same"
		if p != t {
			mark = "DIFFER"
		}
		fmt.Printf("%x  plain: %-40s  qmdb: %-40s  %s\n", a.Bytes(), p, t, mark)
	}
	if hs, err := rawdb.ReadHeadersByNumber(tx, num+1); err == nil {
		for _, h := range hs {
			fmt.Printf("stored header at %d: %x parent %x root %x coinbase %x time %d\n", num+1, h.Hash().Bytes()[:6], h.ParentHash[:6], h.Root[:8], h.Coinbase, h.Time)
		}
		if len(hs) == 0 {
			fmt.Printf("no stored headers at %d\n", num+1)
		}
	}
	if v, err := tx.GetOne(modules.QMDBMeta, commitment.QMDBAccountFrozenAtKey); err == nil && len(v) == 8 {
		fmt.Printf("accountFrozenAt row present: %x\n", v)
	}
}
