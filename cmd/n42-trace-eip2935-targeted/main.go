// n42-trace-eip2935-targeted: 1-slot dirty descent into a specific level-1
// subtree under EIP-2935. Reads the first HashedStorage slot whose hashed-key
// nibble matches --nib, adds JUST that slot to the RetainList (marker=true),
// then runs CalcTrieRoot. Used to bisect "is level-1 emit at path=<nib>
// correct from leaves or stale-from-cache" — pair with
// N42_TRACE_STORCOLL_TOP=1.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d["HashedAccount"] = kv.TableCfgItem{}
	d["HashedStorage"] = kv.TableCfgItem{Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 64, DupToLen: 32}
	d["TrieAccount"] = kv.TableCfgItem{}
	d["TrieStorage"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "chaindata RO")
	acctHex := flag.String("acct", "6c9d57be05dd69371c4dd2e871bce6e9f4124236825bb612ee18a45e5675be51", "addrHash (64 hex)")
	nib := flag.String("nib", "3", "first nibble of slot hash to target (single hex char)")
	flag.Parse()

	addrHash, _ := hex.DecodeString(*acctHex)
	if len(addrHash) != 32 {
		fmt.Fprintln(os.Stderr, "bad -acct")
		os.Exit(1)
	}
	if len(*nib) != 1 {
		fmt.Fprintln(os.Stderr, "bad -nib: need 1 hex char")
		os.Exit(1)
	}
	targetByte, err := hex.DecodeString("0" + *nib) // 0..f -> 0x00..0x0f
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad -nib hex:", err)
		os.Exit(1)
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	// Find first HashedStorage key under addrHash whose slotHash starts with target byte.
	rl := trie.NewRetainList(0)
	c, _ := tx.Cursor("HashedStorage")
	var found []byte
	for k, _, e := c.Seek(addrHash); k != nil; k, _, e = c.Next() {
		if e != nil {
			break
		}
		if len(k) != 64 {
			continue
		}
		if hex.EncodeToString(k[:32]) != *acctHex {
			break
		}
		// k[32] high nibble must equal targetByte[0] low nibble
		if (k[32] >> 4) == targetByte[0] {
			found = append([]byte(nil), k...)
			break
		}
	}
	c.Close()
	if found == nil {
		fmt.Println("no slot under nibble", *nib)
		os.Exit(1)
	}
	fmt.Printf("targeting slot key %x (1st nibble = %c)\n", found, "0123456789abcdef"[found[32]>>4])
	rl.AddKeyWithMarker(found, true)

	shc := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		return nil
	}

	loader := trie.NewFlatDBTrieLoader("trace-targeted", rl, nil, shc, false)
	t0 := time.Now()
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("stateRoot = %s  (%s)\n", root.Hex(), time.Since(t0).Truncate(time.Millisecond))
}
