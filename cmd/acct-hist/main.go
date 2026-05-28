// acct-hist: read an account's current nonce from HashedAccounts + its per-block
// pre-block nonce history from AccountChangeSet (reth-style #129 changesets).
// Diagnoses "nonce too low" — pins the block where N42 over-incremented a nonce.
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d[modules.HashedAccounts] = kv.TableCfgItem{}
	d[modules.AccountChangeSet] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func dec(v []byte) (nonce uint64, ok bool) {
	if len(v) == 0 {
		return 0, false
	}
	var a account.StateAccount
	if err := a.DecodeForStorageV2(v); err == nil {
		return a.Nonce, true
	}
	var b account.StateAccount
	if err := b.DecodeForStorage(v); err == nil {
		return b.Nonce, true
	}
	return 0, false
}

func main() {
	datadir := flag.String("datadir", `D:/N42-hashed/chaindata`, "chaindata (read-only)")
	addrHex := flag.String("addr", "4308DDce9F21dD5f2aed1422f8Bd6D494a7BaA10", "20-byte address hex")
	from := flag.Uint64("from", 25097900, "from block")
	to := flag.Uint64("to", 25098395, "to block (exclusive)")
	flag.Parse()

	ab, err := hex.DecodeString(*addrHex)
	if err != nil || len(ab) != 20 {
		fmt.Fprintln(os.Stderr, "need --addr = 40 hex chars")
		os.Exit(1)
	}
	var addr types.Address
	copy(addr[:], ab)
	addrHash := crypto.Keccak256(addr[:])

	db, err := mdbx.NewMDBX(log.New()).Path(*datadir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
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

	// Max committed block (state tip) = last AccountChangeSet key.
	if cur, e := tx.Cursor(modules.AccountChangeSet); e == nil {
		if k, _, e2 := cur.Last(); e2 == nil && len(k) >= 8 {
			fmt.Printf("STATE TIP (max AccountChangeSet block) = %d\n", binary.BigEndian.Uint64(k[:8]))
		}
		cur.Close()
	}

	// Current state.
	enc, _ := tx.GetOne(modules.HashedAccounts, addrHash)
	n, ok := dec(enc)
	fmt.Printf("CURRENT HashedAccounts[%s]: nonce=%d ok=%v raw=%x\n", *addrHex, n, ok, enc)

	// Per-block pre-values from AccountChangeSet.
	fmt.Printf("=== AccountChangeSet pre-block nonce for %s in [%d,%d) ===\n", *addrHex, *from, *to)
	count := 0
	err = changeset.ForRange(tx, modules.AccountChangeSet, *from, *to, func(blockN uint64, k, v []byte) error {
		if len(k) >= 20 && string(k[:20]) == string(addr[:]) {
			pn, pok := dec(v)
			fmt.Printf("  block %d: preNonce=%d ok=%v (created=%v) raw=%x\n", blockN, pn, pok, len(v) == 0, v)
			count++
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ForRange:", err)
	}
	fmt.Printf("(touched in %d blocks in range)\n", count)
}
