// n42-trace-eip2935 forces a read-only FlatDBTrieLoader descent into a
// specific account's storage subtree (default: EIP-2935 history contract),
// so the env-gated trace in lib/trie/trie_root.go's genStructStorage
// (N42_TRACE_STORCOLL_TOP=1) fires and dumps HashBuilder.topHash() at the
// exact moment storCollector is invoked. Pair with N42_TRACE_STORCOLL=1
// for the outer storCollector emission record. Use to verify whether the
// rootHash GenStructStepEx hands to the callback matches the freshly
// rebuilt storage subtree root (vs whatever stale value is cached in
// TrieOfStorage at keyHex='').
//
// Read-only: opens the chaindata Accede+Readonly, never writes.
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
	d["HashedStorage"] = kv.TableCfgItem{
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                64,
		DupToLen:                  32,
	}
	d["TrieAccount"] = kv.TableCfgItem{}
	d["TrieStorage"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "N42 chaindata dir (read-only)")
	acctHex := flag.String("acct", "6c9d57be05dd69371c4dd2e871bce6e9f4124236825bb612ee18a45e5675be51", "addrHash to force descent (64 hex)")
	nSlots := flag.Int("n-slots", 8, "first N HashedStorage slots under acct to retain (forces descent)")
	flag.Parse()

	addrHash, err := hex.DecodeString(*acctHex)
	if err != nil || len(addrHash) != 32 {
		fmt.Fprintln(os.Stderr, "bad -acct: need 64 hex chars")
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

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	// Build RetainList: first N HashedStorage slots under addrHash, marker=true.
	// The marker forces a HashedStorage leaf scan around each slot, which in
	// turn pushes the loader into genStructStorage for that subtree — the
	// only path that triggers our STORCOLLTOP trace.
	rl := trie.NewRetainList(0)
	c, err := tx.Cursor("HashedStorage")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor HashedStorage:", err)
		os.Exit(1)
	}
	nFound := 0
	for k, _, e := c.Seek(addrHash); k != nil && nFound < *nSlots; k, _, e = c.Next() {
		if e != nil {
			break
		}
		if len(k) != 64 {
			continue
		}
		// stop when we walk past this account's prefix
		if hex.EncodeToString(k[:32]) != *acctHex {
			break
		}
		rl.AddKeyWithMarker(append([]byte(nil), k...), true)
		nFound++
	}
	c.Close()
	fmt.Printf("retained %d slot keys under acct %s\n", nFound, *acctHex)
	if nFound == 0 {
		fmt.Println("no slots found under that prefix — descent will not happen, trace will not fire")
		os.Exit(1)
	}

	// Non-nil shc so the wrapper closure in trie_root.go does not short-circuit
	// before reaching the STORCOLLTOP trace. No-op body — we don't need the
	// actual collected hashes for this probe; the trace fires regardless.
	shc := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		return nil
	}

	loader := trie.NewFlatDBTrieLoader("trace-eip2935", rl, nil, shc, false)
	t0 := time.Now()
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CalcTrieRoot:", err)
		os.Exit(1)
	}
	fmt.Printf("stateRoot = %s  (elapsed %s)\n", root.Hex(), time.Since(t0).Truncate(time.Millisecond))
}
