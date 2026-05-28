// acct-stats samples the migrated N42 Account table and reports the
// byte-size distribution of account values, to evaluate encoding
// compression (MarshalV2 vs reth-2.2 Compact vs theoretical optimum).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func uvarintSize(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}

func main() {
	dir := flag.String("dir", `D:/N42-eth1177-test/chaindata`, "chaindata path")
	limit := flag.Uint64("limit", 5_000_000, "sample first N accounts (0=all)")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	c, err := tx.Cursor("Account")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	var (
		total       uint64
		contracts   uint64
		zeroNonce   uint64
		zeroBalance uint64
		nonceLenHist [10]uint64
		balLenHist   [33]uint64
		sumCurrent   uint64 // N42 MarshalV2 bytes (value only)
		sumReth      uint64 // reth Compact bytes (value only)
		sumOptimal   uint64 // theoretical: 1B header(packs nonce-len 3b+bal-len 6b? use 2B) — model as min
	)

	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			break
		}
		if len(k) != 20 {
			continue
		}
		var a account.StateAccount
		if err := a.DecodeForStorageV2(v); err != nil {
			continue
		}
		total++

		nLen := 0
		if a.Nonce > 0 {
			b := a.Nonce
			for b > 0 {
				nLen++
				b >>= 8
			}
		} else {
			zeroNonce++
		}
		if nLen < len(nonceLenHist) {
			nonceLenHist[nLen]++
		}

		bb := a.Balance.Bytes32()
		start := 0
		for start < 32 && bb[start] == 0 {
			start++
		}
		bLen := 32 - start
		if a.Balance.IsZero() {
			bLen = 0
			zeroBalance++
		}
		if bLen < len(balLenHist) {
			balLenHist[bLen]++
		}

		hasCode := !account.IsEmptyCodeHash(a.CodeHash)
		if hasCode {
			contracts++
		}

		// Current N42 MarshalV2: 1(fieldBits) + nonce_uvarint + (1+balLen) + (32 if code)
		cur := 1
		if a.Nonce > 0 {
			cur += uvarintSize(a.Nonce)
		}
		if bLen > 0 {
			cur += 1 + bLen
		}
		if hasCode {
			cur += 32
		}
		sumCurrent += uint64(cur)

		// reth 2.2 Compact: 2(header packs lengths) + nonce_bytes + bal_bytes + (33 if code: varuint(32)+32)
		rth := 2 + nLen + bLen
		if hasCode {
			rth += 33
		}
		sumReth += uint64(rth)

		// Theoretical optimal: 1B header packing nonce-present(1b)+nonceLen(3b: 0-8)+balLen(... need 6b)
		// Can't fit balLen(6b)+nonceLen(3b)+codeFlag in 1B (10b). Model optimum = 1B header(nonce-len 3b + code 1b + 4b spare)
		// with balance length in a nibble-extended scheme: assume 1B header + raw nonce + raw bal + (32 if code).
		opt := 1 + nLen + bLen
		if hasCode {
			opt += 32
		}
		sumOptimal += uint64(opt)

		if *limit > 0 && total >= *limit {
			break
		}
	}

	fmt.Printf("=== Account value encoding stats (sampled %d accounts) ===\n", total)
	fmt.Printf("contracts (has codeHash): %d (%.2f%%)\n", contracts, 100*float64(contracts)/float64(total))
	fmt.Printf("zero-nonce: %d (%.2f%%)   zero-balance: %d (%.2f%%)\n",
		zeroNonce, 100*float64(zeroNonce)/float64(total), zeroBalance, 100*float64(zeroBalance)/float64(total))
	fmt.Println("nonce byte-len histogram:")
	for i, n := range nonceLenHist {
		if n > 0 {
			fmt.Printf("  %d bytes: %d (%.1f%%)\n", i, n, 100*float64(n)/float64(total))
		}
	}
	fmt.Println("balance byte-len histogram:")
	for i, n := range balLenHist {
		if n > 0 {
			fmt.Printf("  %2d bytes: %d (%.1f%%)\n", i, n, 100*float64(n)/float64(total))
		}
	}
	fmt.Printf("\n=== total VALUE bytes (excl. 20B key), %d accounts ===\n", total)
	fmt.Printf("N42 MarshalV2 : %d bytes  (avg %.2f B/acct)\n", sumCurrent, float64(sumCurrent)/float64(total))
	fmt.Printf("reth 2.2      : %d bytes  (avg %.2f B/acct)\n", sumReth, float64(sumReth)/float64(total))
	fmt.Printf("theoretical   : %d bytes  (avg %.2f B/acct, 1B header + raw fields)\n", sumOptimal, float64(sumOptimal)/float64(total))
	fmt.Printf("N42 vs reth   : %+.2f%% \n", 100*(float64(sumCurrent)-float64(sumReth))/float64(sumReth))
}
