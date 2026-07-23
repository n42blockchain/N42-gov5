// n42-hashed-exec-check deterministically re-executes a range of mainnet blocks
// on a hashed-canonical datadir using eth-el's OWN execution path
// (api.EngineStateAdapter.ExecutePayloadFromWire), reading blocks LOCALLY (headerc
// +bodyc or geth ancient) — no devp2p peers. Blocks run in ONE uncommitted batch
// tx, reproducing eth-el catch-up's cross-block shared read cache; the tx is
// ALWAYS rolled back, so the datadir is left unchanged.
//
// It forces N42_GAS161=1 so every block/tx logs its gas (GAS161 mismatch / GAS161
// tx), and honors N42_NO_READCACHE=1 (the diagnostic bypass) so you can A/B:
//
//	# cache ON (reproduce the bug): block 25587088 → GAS161 mismatch got<want
//	n42-hashed-exec-check --datadir D:/N42-hashed --from 25587084 --to 25587090
//	# cache OFF (confirm read-cache stale): 25587088 matches, all pass
//	N42_NO_READCACHE=1 n42-hashed-exec-check --datadir D:/N42-hashed --from 25587084 --to 25587090
//
// Diff the per-block "GAS161 tx" lines for 25587088 between the two runs → the tx
// whose txGas differs (+ its `to`) is the contract whose stale cached read caused
// the divergence.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"encoding/hex"
	"strings"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

func main() {
	datadir := flag.String("datadir", `D:/N42-hashed`, "hashed-canonical datadir (has chaindata/ + chain/freezer/)")
	blocksDir := flag.String("blocks", `d:/n42-eth1/chain/freezer`, "block source: headerc+bodyc dir, or a geth ancient dir")
	from := flag.Uint64("from", 25587084, "first block (inclusive)")
	to := flag.Uint64("to", 25587090, "last block (inclusive)")
	probe := flag.String("probe-addr", "", "if set, print this 0x address's account codeHash + Code-present from the batch tx AFTER each block (tracks corruption)")
	scanMissing := flag.Bool("scan-missing", false, "after the run, scan the batch tx for accounts with non-empty codeHash but MISSING Code entry (code writes dropped during 84-87 → the bug)")
	mapGB := flag.Int("mapsize-gb", 2048, "MDBX MapSize in GB (must exceed the 156GB file — eth-el's 64/256GB default MAP_FULLs)")
	flag.Parse()

	// Force per-block + per-tx gas diagnostics (GAS161 mismatch / GAS161 tx lines).
	_ = os.Setenv("N42_GAS161", "1")
	noCache := os.Getenv("N42_NO_READCACHE") == "1"

	// Register N42 chaindata table configs so the existing hashed tables open.
	modules.N42Init()
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

	logger := log2.New()
	chaindata := filepath.Join(*datadir, "chaindata")
	db, err := mdbx.NewMDBX(logger).Path(chaindata).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open mdbx:", err)
		os.Exit(1)
	}
	defer db.Close()

	fz, err := freezer.New(filepath.Join(*datadir, "chain", "freezer"), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open output freezer:", err)
		os.Exit(1)
	}

	chainCfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(chainCfg)
	adapter := api.NewEngineStateAdapter(db, fz, chainCfg, engine).WithHashedCanonical(true)

	// ONE uncommitted batch tx: reproduces eth-el catch-up's cross-block shared
	// read cache. NEVER committed → datadir data unchanged (read-modify-rollback).
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin rw:", err)
		os.Exit(1)
	}
	defer tx.Rollback()
	adapter.SetBatchTx(tx)
	defer adapter.SetBatchTx(nil)

	fmt.Printf("=== hashed-exec-check: blocks [%d,%d]  readCache=%s  (batch tx ROLLS BACK — datadir unchanged) ===\n",
		*from, *to, cacheState(noCache))

	// Freezer-direct BLOCKHASH — the clean ethexec/witness-block-trace model that
	// localCatchUp now uses: ancestor hashes come straight from the headerc
	// reader's stored h.Hash() (SetHash canonical), NOT MDBX getHeader's ParentHash
	// walk. No window seed, no field reconstruction. For a geth-ancient --blocks
	// source (full headers) there's no headerc reader; leave the default resolver
	// (that source isn't the migrated-datadir case this reproduces).
	if _, herr := os.Stat(filepath.Join(*blocksDir, "headerc.cidx")); herr == nil {
		hr, oerr := ethel.OpenHeaderCompact(*blocksDir)
		if oerr != nil {
			fmt.Fprintln(os.Stderr, "open headerc:", oerr)
			os.Exit(1)
		}
		defer hr.Close()
		adapter.SetHeaderHashReader(hr)
		fmt.Printf("=== freezer-direct BLOCKHASH: headerc reader installed (%s) ===\n", *blocksDir)
	}

	// EIP-2935 execution input: header.ParentHash (stored into state by
	// ProcessExecutionBlockStart). headerc strips it, so reconstruct from the
	// prior block's canonical hash — the ONLY field execution needs beyond the
	// freezer-direct BLOCKHASH. Mirrors localCatchUp.
	prevHash := types.Hash{}
	if p, _, perr := readHeaderBody(*blocksDir, *from-1); perr == nil && p != nil {
		prevHash = p.Hash()
	}

	for n := *from; n <= *to; n++ {
		hdr, gbody, rerr := readHeaderBody(*blocksDir, n)
		if rerr != nil {
			fmt.Printf("block=%d READ ERROR: %v\n", n, rerr)
			break
		}
		canonical := hdr.Hash()   // headerc stored canonical hash (SetHash)
		hdr.ParentHash = prevHash // EIP-2935 execution input (see above)
		ib := block.NewBlock(hdr, gbody.Transactions)
		blk, ok := ib.(*block.Block)
		if !ok {
			fmt.Printf("block=%d assemble error: unexpected type %T\n", n, ib)
			break
		}
		ws := make([]*api.Withdrawal, len(gbody.Withdrawals))
		for i, w := range gbody.Withdrawals {
			ws[i] = &api.Withdrawal{
				Index:          hexutil.Uint64(w.Index),
				ValidatorIndex: hexutil.Uint64(w.Validator),
				Address:        w.Address,
				Amount:         hexutil.Uint64(w.Amount),
			}
		}
		okRes, root, xerr := adapter.ExecutePayloadFromWire(blk, ws)
		if xerr != nil {
			fmt.Printf("block=%d EXEC ERROR: %v\n", n, xerr)
			break
		}
		// Mirror localCatchUp: write canonical[n] from the stored headerc hash so
		// the next block / peer-handoff parent-link check reads the right value.
		chainMark := ""
		if okRes {
			_ = rawdb.WriteCanonicalHash(tx, canonical, n)
			prevHash = canonical // next block's EIP-2935 ParentHash input
			chainMark = "  canon[n]=ok"
		}
		fmt.Printf("block=%d ok=%v wantGas=%d txs=%d root=%s%s%s\n",
			n, okRes, hdr.GasUsed, len(gbody.Transactions), root.Hex()[:12],
			hint(okRes), chainMark)
		if *probe != "" {
			probeAddr(tx, *probe, n)
		}
		if !okRes {
			// First divergence — stop; later blocks would build on un-persisted state.
			fmt.Printf(">>> FIRST DIVERGENCE at block %d (readCache=%s). GOT per-tx cum is in the 'GAS161 tx' log lines; canonical WANT per-tx cum below — first i where they differ is the culprit tx.\n",
				n, cacheState(noCache))
			dumpCanonReceipts(*blocksDir, n)
			if *scanMissing {
				// The divergent block (88) did NOT persist, so the batch tx now holds
				// state @(n-1)=@87 — exactly what block 88 read. Scan it.
				scanMissingCode(tx, n-1)
			}
			break
		}
	}
}

// checkHeaderWindow probes the SAME getHeader path opBlockhash uses (canonical
// hash → header) for the 256-block BLOCKHASH window below `from`, reporting how
// many are absent. Any absent header → BLOCKHASH(that number) returns zero in
// eth-el, a wrong read the state root can't catch.
func checkHeaderWindow(tx kv.RwTx, from uint64) {
	var start uint64
	if from > 260 {
		start = from - 260
	}
	var have, miss int
	var firstMiss, lastMiss uint64
	for n := start; n < from; n++ {
		ch, err := rawdb.ReadCanonicalHash(tx, n)
		if err != nil || ch == (types.Hash{}) {
			miss++
			if firstMiss == 0 {
				firstMiss = n
			}
			lastMiss = n
			continue
		}
		if h := rawdb.ReadHeader(tx, ch, n); h == nil {
			miss++
			if firstMiss == 0 {
				firstMiss = n
			}
			lastMiss = n
			continue
		}
		have++
	}
	fmt.Printf("=== BLOCKHASH window [%d,%d): getHeader HAVE=%d MISSING=%d", start, from, have, miss)
	if miss > 0 {
		fmt.Printf(" (first missing=%d last missing=%d → BLOCKHASH of these returns ZERO)", firstMiss, lastMiss)
	}
	fmt.Printf(" ===\n")
}

// verifyWindowParentChain checks that every seeded window header's STORED
// ParentHash field equals the prior block's canonical hash — exactly what
// BLOCKHASH resolves through (internalcore.GetHashFn walks header.ParentHash
// fields, never Hash()). A broken link here means BLOCKHASH of that height
// returns a wrong/zero value even though the canonical INDEX is correct — the
// real reason a seeded window still fails block 88.
func verifyWindowParentChain(tx kv.RwTx, from uint64) {
	var start uint64
	if from > 260 {
		start = from - 260
	}
	var checked, broken int
	var firstBroken uint64
	for n := start + 1; n < from; n++ {
		cn, _ := rawdb.ReadCanonicalHash(tx, n)
		if cn == (types.Hash{}) {
			continue
		}
		h := rawdb.ReadHeader(tx, cn, n)
		if h == nil {
			continue
		}
		cprev, _ := rawdb.ReadCanonicalHash(tx, n-1)
		checked++
		if h.ParentHash != cprev {
			broken++
			if firstBroken == 0 {
				firstBroken = n
			}
		}
	}
	fmt.Printf("=== BLOCKHASH ParentHash-chain [%d,%d): checked=%d broken=%d", start, from, checked, broken)
	if broken > 0 {
		fmt.Printf(" (first broken=%d → BLOCKHASH(%d) returns wrong/zero)", firstBroken, firstBroken-1)
	} else if checked > 0 {
		fmt.Printf(" (every stored ParentHash links → BLOCKHASH resolves correctly)")
	}
	fmt.Printf(" ===\n")
}

// fillHeaderWindow writes the 256 canonical headers below `from` (read from the
// --blocks source) into the batch tx — Header + CanonicalHash index — exactly
// what getHeader/opBlockhash needs. It's the A/B fix probe: if block 88 matches
// afterward, the missing window is proven to be the root cause. (This mutates
// only the rolled-back batch tx; the datadir is untouched.)
func fillHeaderWindow(tx kv.RwTx, dir string, from uint64) {
	var start uint64
	if from > 260 {
		start = from - 260
	}
	var wrote int
	// Reproduce fillWindowFromLocalHeaderc: reconstruct the ParentHash chain so
	// each seeded header carries a correct ParentHash FIELD (BLOCKHASH's GetHashFn
	// walks those fields; headerc strips them).
	var prevHash types.Hash
	if p, _, perr := readHeaderBody(dir, start-1); perr == nil && p != nil {
		prevHash = p.Hash()
	}
	for n := start; n < from; n++ {
		hdr, _, err := readHeaderBody(dir, n)
		if err != nil {
			fmt.Printf("  (fill-headers: read %d: %v)\n", n, err)
			prevHash = types.Hash{}
			continue
		}
		if hdr.ParentHash == (types.Hash{}) && prevHash != (types.Hash{}) {
			hdr.ParentHash = prevHash
		}
		if hdr.UncleHash == (types.Hash{}) {
			hdr.UncleHash = hash.EmptyUncleHash
		}
		canonical := hdr.Hash()
		rawdb.WriteHeader(tx, hdr)
		prevHash = canonical
		if err := rawdb.WriteCanonicalHash(tx, canonical, n); err != nil {
			fmt.Printf("  (fill-headers: write canonical %d: %v)\n", n, err)
			continue
		}
		wrote++
	}
	fmt.Printf("=== fill-headers: wrote %d canonical headers into [%d,%d) ===\n", wrote, start, from)
}

// scanMissingCode walks HashedAccount in the batch tx and reports every account
// with a non-empty codeHash whose Code table entry is ABSENT. The migrated @83
// state had 0 such accounts (verified by n42-check-code), so any hit here was
// written during 84-87 with its code DROPPED — the eth-el hashed-canonical bug.
// A 7702 delegation designator (0xef0100||delegate, 23 B) that hits this is the
// smoking gun: a later block CALLing the account resolves empty code and diverges.
func scanMissingCode(tx kv.RwTx, stateAt uint64) {
	fmt.Printf("  === scan-missing: HashedAccount with codeHash set but Code ABSENT (state @%d) ===\n", stateAt)
	c, err := tx.Cursor("HashedAccount")
	if err != nil {
		fmt.Printf("  (scan: cursor: %v)\n", err)
		return
	}
	defer c.Close()
	// Cache codeHash → present so we only probe Code once per distinct hash.
	present := make(map[string]bool, 1<<21)
	var scanned, withCode, missing int
	for k, v, e := c.First(); k != nil; k, v, e = c.Next() {
		if e != nil {
			fmt.Printf("  (scan: iterate: %v)\n", e)
			return
		}
		scanned++
		var acc account.StateAccount
		if acc.DecodeForStorageV2(v) != nil {
			continue
		}
		if account.IsEmptyCodeHash(acc.CodeHash) {
			continue
		}
		withCode++
		hk := string(acc.CodeHash[:])
		ok, seen := present[hk]
		if !seen {
			code, _ := tx.GetOne("Code", acc.CodeHash[:])
			ok = len(code) > 0
			present[hk] = ok
		}
		if !ok {
			missing++
			fmt.Printf("  MISSING-CODE addrHash=%s codeHash=%s nonce=%d balance=%s (code dropped in 84-87)\n",
				hex.EncodeToString(k), hex.EncodeToString(acc.CodeHash[:]), acc.Nonce, acc.Balance.String())
		}
	}
	fmt.Printf("  === scan done: %d accounts, %d with code, %d MISSING code ===\n", scanned, withCode, missing)
	if missing == 0 {
		fmt.Printf("  (0 missing → the bug is NOT a dropped Code write; the divergence is a wrong READ, not a wrong WRITE)\n")
	}
}

// dumpCanonReceipts prints canonical per-tx CUMULATIVE gas for block n from the
// geth ancient receipts freezer, so it can be diffed against the GOT per-tx cum
// in the GAS161 tx log lines (first differing i = the culprit tx).
func dumpCanonReceipts(dir string, n uint64) {
	f, err := freezer.NewReadOnly(dir)
	if err != nil {
		fmt.Printf("  (canon receipts: open freezer %s: %v)\n", dir, err)
		return
	}
	defer f.Close()
	raw, err := f.Ancient(freezer.TableReceipts, n)
	if err != nil {
		fmt.Printf("  (canon receipts: read block %d: %v)\n", n, err)
		return
	}
	rs, err := ethel.DecodeGethReceipts(raw)
	if err != nil {
		fmt.Printf("  (canon receipts: decode: %v)\n", err)
		return
	}
	fmt.Printf("  --- CANONICAL per-tx cumulative gas (block %d, %d receipts) ---\n", n, len(rs))
	var prev uint64
	for i, r := range rs {
		fmt.Printf("  CANON i=%d cumGas=%d txGas=%d\n", i, r.CumulativeGasUsed, r.CumulativeGasUsed-prev)
		prev = r.CumulativeGasUsed
	}
}

// probeAddr reads addr's account codeHash + whether its code is in the Code
// table, straight from the batch tx (so it reflects blocks executed so far).
func probeAddr(tx kv.RwTx, addr string, afterBlock uint64) {
	ab, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(addr, "0x"), "0X"))
	if err != nil || len(ab) != 20 {
		return
	}
	ah := crypto.Keccak256(ab)
	v, _ := tx.GetOne("HashedAccount", ah)
	if len(v) == 0 {
		fmt.Printf("  PROBE %s after block %d: NO account\n", addr, afterBlock)
		return
	}
	var acc account.StateAccount
	if e := acc.DecodeForStorageV2(v); e != nil {
		fmt.Printf("  PROBE %s after block %d: decode err %v\n", addr, afterBlock, e)
		return
	}
	empty := account.IsEmptyCodeHash(acc.CodeHash)
	codeLen := 0
	if !empty {
		code, _ := tx.GetOne("Code", acc.CodeHash[:])
		codeLen = len(code)
	}
	fmt.Printf("  PROBE %s after block %d: nonce=%d codeHash=%s emptyCode=%v codeLenInTable=%d\n",
		addr, afterBlock, acc.Nonce, hex.EncodeToString(acc.CodeHash[:8]), empty, codeLen)
}

func cacheState(off bool) string {
	if off {
		return "OFF"
	}
	return "ON"
}
func hint(ok bool) string {
	if ok {
		return "  (gas+root matched)"
	}
	return "  <-- MISMATCH"
}

// readHeaderBody reads a block's header + body locally. Prefers headerc/bodyc
// (columnar) when dir has headerc.cidx; otherwise falls back to a geth ancient
// freezer. Mirrors cmd/witness-block-trace's reader (which is package-local, so
// this is a copy, not an import).
func readHeaderBody(dir string, n uint64) (*block.Header, *ethel.GethBodyResult, error) {
	if _, err := os.Stat(filepath.Join(dir, "headerc.cidx")); err == nil {
		hr, err := ethel.OpenHeaderCompact(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("open headerc: %w", err)
		}
		defer hr.Close()
		br, err := ethel.OpenBodyCompact(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("open bodyc: %w", err)
		}
		defer br.Close()
		hdr, err := hr.ReadHeader(n)
		if err != nil {
			return nil, nil, fmt.Errorf("read headerc %d: %w", n, err)
		}
		db, err := br.ReadBody(n)
		if err != nil {
			return nil, nil, fmt.Errorf("read bodyc %d: %w", n, err)
		}
		var uncles []*block.Header
		if len(db.UncleRLP) > 0 {
			uncles = make([]*block.Header, len(db.UncleRLP))
			for i, raw := range db.UncleRLP {
				h, err := ethel.DecodeUncleHeader(raw)
				if err != nil {
					return nil, nil, fmt.Errorf("uncle %d: %w", i, err)
				}
				uncles[i] = h
			}
		}
		return hdr, &ethel.GethBodyResult{
			Transactions: db.Txs,
			Uncles:       uncles,
			Withdrawals:  db.Withdrawals,
		}, nil
	}
	// geth ancient fallback.
	f, err := freezer.NewReadOnly(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("open geth freezer: %w", err)
	}
	defer f.Close()
	hData, err := f.Ancient(freezer.TableHeaders, n)
	if err != nil {
		return nil, nil, fmt.Errorf("geth header %d: %w", n, err)
	}
	hdr, err := ethel.DecodeGethHeader(hData)
	if err != nil {
		return nil, nil, fmt.Errorf("decode geth header %d: %w", n, err)
	}
	bData, err := f.Ancient(freezer.TableBodies, n)
	if err != nil {
		return nil, nil, fmt.Errorf("geth body %d: %w", n, err)
	}
	body, err := ethel.DecodeGethBody(bData)
	if err != nil {
		return nil, nil, fmt.Errorf("decode geth body %d: %w", n, err)
	}
	return hdr, body, nil
}

