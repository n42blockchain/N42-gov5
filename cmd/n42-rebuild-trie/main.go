// n42-rebuild-trie rebuilds TrieOfAccounts/TrieOfStorage for a hashed-canonical
// N42 chaindata from its already-populated HashedAccounts/HashedStorage tables.
//
// Why: a reth-migrated TrieOfStorage omits the empty-path (keylen-32)
// "account.root" records that erigon's incremental FlatDBTrieLoader requires
// (it re-processes whole cached-IH ranges around dirty paths and needs the
// neighbours' cached storage roots, which live only in those records). Without
// them, incremental state-root updates are wrong (see
// modules/state/commitment/trie_root_incremental_test.go). This one-time
// rebuild regenerates a complete N42-native TrieOf* so incremental updates are
// correct and O(dirty) per block.
//
// It does NOT re-hash: HashedAccounts/HashedStorage are the source of truth and
// are left untouched. Only TrieOfAccounts/TrieOfStorage are cleared and
// rebuilt. The computed root is checked against --expect before persisting.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/etl"
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
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "N42 chaindata dir")
	tmpdir := flag.String("tmpdir", `D:/N42-trie-tmp`, "ETL spill dir (same fast drive as datadir)")
	expect := flag.String("expect", "0x5c90881689e74b05c2b3500da4e937c091fc1bafc1d032f3924beceeb80bf319", "expected stateRoot hex")
	dirtyGB := flag.Uint64("dirty-gb", 64, "MDBX dirty space (GB) for the bulk Load tx")
	accBufGB := flag.Uint64("acc-buf-gb", 4, "ETL acct buffer GB")
	stoBufGB := flag.Uint64("sto-buf-gb", 16, "ETL storage buffer GB")
	flag.Parse()

	logger := log.New()
	if err := os.MkdirAll(*tmpdir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir tmpdir:", err)
		os.Exit(1)
	}
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).GrowthStep(4 * datasize.GB).
		DirtySpace(uint64(datasize.ByteSize(*dirtyGB) * datasize.GB)).
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	ctx := context.Background()
	t0 := time.Now()

	// VERIFY-BEFORE-CLEAR: Phase 2 rebuilds from leaves using a FULL
	// RetainList (minLength 1<<30 → Retain() returns true for every prefix →
	// cached TrieOf* hashes are never adopted; the loader descends to
	// HashedAccounts/HashedStorage leaves everywhere). Because it ignores the
	// cached nodes anyway, it does NOT need the tables cleared first — so we
	// can verify the recomputed root against --expect BEFORE touching the
	// on-disk TrieOf*. A mismatch (wrong --expect or corrupt leaves) then
	// exits with the existing trie intact instead of destroying the only
	// verified datadir. The clear is deferred to Phase 2.5, after the check.

	// Phase 2: CalcTrieRoot (read tx) → stream emitted trie nodes into ETL.
	accColl := etl.NewCollector("rebuild-trie-acc", *tmpdir,
		etl.NewSortableBuffer(datasize.ByteSize(*accBufGB)*datasize.GB), logger)
	defer accColl.Close()
	stoColl := etl.NewCollector("rebuild-trie-sto", *tmpdir,
		etl.NewSortableBuffer(datasize.ByteSize(*stoBufGB)*datasize.GB), logger)
	defer stoColl.Close()

	var accN, stoN uint64
	accCollector := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if len(keyHex) == 0 || hasState == 0 {
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		accN++
		return accColl.Collect(append([]byte(nil), keyHex...), append([]byte(nil), v...))
	}
	storCollector := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append(append(make([]byte, 0, len(accWithInc)+len(keyHex)), accWithInc...), keyHex...)
		if len(k) == 0 || hasState == 0 {
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		stoN++
		return stoColl.Collect(k, append([]byte(nil), v...))
	}

	fmt.Fprintln(os.Stderr, "phase2: CalcTrieRoot full descent (streaming nodes to ETL)")
	var root [32]byte
	{
		rtx, err := db.BeginRo(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "begin ro:", err)
			os.Exit(1)
		}
		// minLength 1<<30 → retain everything → ignore cached TrieOf*, descend leaves.
		loader := trie.NewFlatDBTrieLoader("rebuild-trie", trie.NewRetainList(1<<30), accCollector, storCollector, false)
		r, err := loader.CalcTrieRoot(rtx, nil)
		rtx.Rollback()
		if err != nil {
			fmt.Fprintln(os.Stderr, "CalcTrieRoot:", err)
			os.Exit(1)
		}
		root = r
	}
	fmt.Fprintf(os.Stderr, "phase2 done: root=%x accNodes=%d stoNodes=%d elapsed=%s\n",
		root[:], accN, stoN, time.Since(t0).Truncate(time.Second))

	if *expect == "" {
		fmt.Fprintf(os.Stderr, "REFUSING to clear+persist without --expect (would clear TrieOf* unverified). Re-run with --expect=0x<25,191,536 stateRoot>. Computed root was 0x%x\n", root[:])
		os.Exit(2)
	}
	if fmt.Sprintf("0x%x", root[:]) != *expect {
		fmt.Fprintf(os.Stderr, "ROOT MISMATCH: got 0x%x want %s — NOT persisting (TrieOf* left intact)\n", root[:], *expect)
		os.Exit(2)
	}

	// Phase 2.5: root verified against --expect — NOW it is safe to clear the
	// stale TrieOf*. Moved here from the top so a wrong --expect / bad leaves
	// can never leave the datadir with empty trie tables.
	fmt.Fprintln(os.Stderr, "phase2.5: root verified — clearing stale TrieAccount + TrieStorage")
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.ClearBucket("TrieAccount"); err != nil {
			return err
		}
		return tx.ClearBucket("TrieStorage")
	}); err != nil {
		fmt.Fprintln(os.Stderr, "clear:", err)
		os.Exit(1)
	}

	// Phase 3: bulk-load nodes into TrieOf* (own tx).
	fmt.Fprintln(os.Stderr, "phase3: loading TrieAccount + TrieStorage")
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := accColl.Load(tx, "TrieAccount", etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
			return fmt.Errorf("load TrieAccount: %w", err)
		}
		return stoColl.Load(tx, "TrieStorage", etl.IdentityLoadFunc, etl.TransformArgs{})
	}); err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "DONE root=0x%x accNodes=%d stoNodes=%d total=%s\n",
		root[:], accN, stoN, time.Since(t0).Truncate(time.Second))
}
