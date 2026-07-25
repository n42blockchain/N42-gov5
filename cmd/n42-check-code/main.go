// n42-check-code scans a hashed-canonical datadir's HashedAccount table and
// verifies that every account's non-empty CodeHash has a matching Code-table
// entry. A missing entry means eth-el would see an empty contract at that
// address (cheap CALL / wrong EXTCODESIZE) → under-counted gas. Read-only.
//
//	n42-check-code --dir D:/N42-hashed/chaindata
package main

import (
	"context"
	"encoding/hex"
	"strings"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "hashed-canonical chaindata dir")
	addr := flag.String("addr", "", "if set, look up just this 0x address's account+code (skip full scan)")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).Accede().Readonly().
		MapSize(4 * datasize.TB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["HashedAccount"] = kv.TableCfgItem{}
			d["Code"] = kv.TableCfgItem{}
			return d
		}).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		panic(err)
	}
	defer tx.Rollback()

	if *addr != "" {
		ab, derr := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(*addr, "0x"), "0X"))
		if derr != nil || len(ab) != 20 {
			fmt.Fprintln(os.Stderr, "bad --addr (want 0x + 20 bytes)")
			os.Exit(1)
		}
		ah := crypto.Keccak256(ab) // HashedAccount key = keccak(address)
		v, gerr := tx.GetOne("HashedAccount", ah)
		if gerr != nil {
			panic(gerr)
		}
		if len(v) == 0 {
			fmt.Printf("addr %s: NO HashedAccount entry (empty / untouched at this state)\n", *addr)
			return
		}
		var acc account.StateAccount
		if uerr := acc.DecodeForStorageV2(v); uerr != nil {
			fmt.Println("decode account:", uerr)
			return
		}
		empty := account.IsEmptyCodeHash(acc.CodeHash)
		fmt.Printf("addr %s: nonce=%d balance=%s codeHash=%s emptyCode=%v\n",
			*addr, acc.Nonce, acc.Balance.String(), hex.EncodeToString(acc.CodeHash[:]), empty)
		if !empty {
			code, cerr := tx.GetOne("Code", acc.CodeHash[:])
			if cerr != nil {
				panic(cerr)
			}
			fmt.Printf("  Code table: present=%v len=%d\n", len(code) > 0, len(code))
		}
		return
	}

	c, err := tx.Cursor("HashedAccount")
	if err != nil {
		panic(err)
	}
	defer c.Close()

	// distinct non-empty codeHash -> #accounts referencing it, + one sample addrHash
	refs := make(map[types.Hash]uint64)
	sampleAddr := make(map[types.Hash]string)
	var total, withCode, decodeFail uint64
	for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
		total++
		var a account.StateAccount
		if derr := a.DecodeForStorageV2(v); derr != nil {
			decodeFail++
			continue
		}
		if account.IsEmptyCodeHash(a.CodeHash) {
			continue
		}
		withCode++
		refs[a.CodeHash]++
		if _, ok := sampleAddr[a.CodeHash]; !ok {
			sampleAddr[a.CodeHash] = hex.EncodeToString(k)
		}
		if total%50_000_000 == 0 {
			fmt.Fprintf(os.Stderr, "  scanned=%d withCode=%d distinctCH=%d\n", total, withCode, len(refs))
		}
	}
	fmt.Printf("HashedAccount total=%d withCode=%d distinctCodeHashes=%d decodeFail=%d\n",
		total, withCode, len(refs), decodeFail)

	// verify each distinct codeHash exists in Code
	type miss struct {
		ch    types.Hash
		accts uint64
		addr  string
	}
	var missing []miss
	var missingAccts uint64
	for ch, n := range refs {
		val, gerr := tx.GetOne("Code", ch[:])
		if gerr != nil {
			panic(gerr)
		}
		if len(val) == 0 {
			missing = append(missing, miss{ch, n, sampleAddr[ch]})
			missingAccts += n
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].accts > missing[j].accts })
	fmt.Printf("MISSING codeHashes=%d referencedBy=%d accounts\n", len(missing), missingAccts)
	for i, m := range missing {
		if i >= 30 {
			fmt.Printf("  ... and %d more\n", len(missing)-30)
			break
		}
		fmt.Printf("  missing codeHash=%s refByAccts=%d sampleAddrHash=%s\n",
			hex.EncodeToString(m.ch[:]), m.accts, m.addr)
	}
}
