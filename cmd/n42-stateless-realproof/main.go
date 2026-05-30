// n42-stateless-realproof generates a REAL per-account EIP-1186 proof from a
// live hashed-canonical N42 chaindata (e.g. D:/N42-hashed) using the production
// ProofRetainer + FlatDBTrieLoader — the same machinery that computes eth-el's
// header.Root — and feeds it through the P8 stateless consumer
// (stateless.VerifyAccountInclusion) to prove a minimal client can verify a real
// mainnet account against the trusted head state root.
//
// This is Step 2 of P8 ③b ("接真实主网数据"): it closes the loop from real
// on-disk state to the P8 partialTrie. The account-trie root the proof anchors
// to is the production CalcTrieRoot output; the INDEPENDENT anchor is the
// canonical head header.Root read straight from the datadir (if it carries block
// headers), so the check is not circular. --expect overrides the anchor when the
// datadir has no headers (pure migration state).
//
// D:/N42-hashed is mainnet-aligned, so the address is supplied in plaintext
// (--addr, default USDC). Storage-slot proofs need plaintext slots (only from a
// plain-keyed reth datadir) and are out of scope here — Step 2b.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func nopAccHC(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
	return nil
}
func nopStorHC(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
	return nil
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "hashed-canonical N42 chaindata dir")
	addrHex := flag.String("addr", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", "plaintext account address (default USDC)")
	expect := flag.String("expect", "", "override anchor stateRoot hex (used only when datadir has no headers)")
	outPath := flag.String("out", "stateless-realproof.txt", "write the result summary here (trustworthy vs polluted stdout)")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginRo(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin ro:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	addr := types.HexToAddress(*addrHex)
	addrHash, err := types.HashData(addr[:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash addr:", err)
		os.Exit(1)
	}

	// 0. Independent anchor: canonical head header.Root straight from the datadir
	//    (bypasses CalcTrieRoot). Empty if this datadir carries no block headers.
	var headerRoot []byte
	var headNum uint64
	haveHeader := false
	if hh := rawdb.ReadHeadBlockHash(tx); hh != (types.Hash{}) {
		if n := rawdb.ReadHeaderNumber(tx, hh); n != nil {
			headNum = *n
			if hdr := rawdb.ReadHeader(tx, hh, headNum); hdr != nil {
				headerRoot = hdr.Root[:]
				haveHeader = true
			}
		}
	}
	if !haveHeader {
		// Migration datadir: the LastBlock head pointer is unset, but headers
		// exist keyed by number. Find the canonical head via the highest
		// CanonicalHeader key (block_num_u64 BE).
		if c, cerr := tx.Cursor(kv.HeaderCanonical); cerr == nil {
			k, v, lerr := c.Last()
			c.Close()
			if lerr == nil && len(k) == 8 && len(v) == 32 {
				headNum = binary.BigEndian.Uint64(k)
				var hh types.Hash
				copy(hh[:], v)
				if hdr := rawdb.ReadHeader(tx, hh, headNum); hdr != nil {
					headerRoot = hdr.Root[:]
					haveHeader = true
				}
			}
		}
	}

	// 1. Read + decode the real account from the leaf table.
	enc, err := tx.GetOne(kv.HashedAccounts, addrHash[:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "get account:", err)
		os.Exit(1)
	}
	if len(enc) == 0 {
		fmt.Fprintf(os.Stderr, "account %s (hash %x) not present in state\n", addr.Hex(), addrHash[:8])
		os.Exit(2)
	}
	var acc account.StateAccount
	if err := acc.DecodeForStorage(enc); err != nil {
		fmt.Fprintln(os.Stderr, "decode account:", err)
		os.Exit(1)
	}

	// 2. Generate the EIP-1186 account proof via the production machinery.
	t0 := time.Now()
	rl := trie.NewRetainList(0)
	pr, err := trie.NewProofRetainer(addr, &acc, nil, rl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "NewProofRetainer:", err)
		os.Exit(1)
	}
	loader := trie.NewFlatDBTrieLoader("realproof", rl, nopAccHC, nopStorHC, false)
	loader.SetProofRetainer(pr)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CalcTrieRoot:", err)
		os.Exit(1)
	}
	elapsed := time.Since(t0).Truncate(time.Millisecond)
	res, err := pr.ProofResult()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ProofResult:", err)
		os.Exit(1)
	}

	// 3. Feed the real proof through the P8 stateless consumer, anchored to the
	//    computed account-trie root.
	va, verr := stateless.VerifyAccountInclusion(root[:], res)

	// 4. Independent anchor check: computed root == header.Root (or --expect).
	rootHex := fmt.Sprintf("0x%x", root[:])
	anchorSrc := "header.Root"
	anchorHex := fmt.Sprintf("0x%x", headerRoot)
	if !haveHeader {
		anchorSrc = "--expect"
		anchorHex = *expect
	}
	anchorOK := anchorHex == "" || rootHex == anchorHex

	// Assemble a trustworthy summary (stdout in this env can be polluted).
	var b []byte
	add := func(format string, args ...interface{}) { b = append(b, []byte(fmt.Sprintf(format, args...))...) }
	add("=== n42-stateless-realproof ===\n")
	add("dir            : %s\n", *dir)
	add("address        : %s\n", addr.Hex())
	add("addrHash       : %x\n", addrHash[:])
	if haveHeader {
		add("head block     : %d\n", headNum)
	} else {
		add("head block     : (no headers in datadir)\n")
	}
	add("computed root  : %s\n", rootHex)
	add("anchor (%s): %s\n", anchorSrc, anchorHex)
	add("anchor match   : %v\n", anchorOK)
	add("proof elapsed  : %s\n", elapsed)
	add("acct proof len : %d nodes\n", len(res.AccountProof))
	add("leaf nonce     : %d\n", acc.Nonce)
	add("leaf balance   : %s\n", acc.Balance.ToBig().String())
	add("leaf codeHash  : %x\n", acc.CodeHash[:])
	if verr != nil {
		add("VERIFY         : FAIL: %v\n", verr)
	} else {
		add("VERIFY         : PASS (exists=%v nonce=%d bal=%s storageRoot=%x)\n",
			va.Exists, va.Nonce, va.Balance.ToBig().String(), va.StorageRoot)
	}
	pass := verr == nil && anchorOK && va != nil && va.Exists
	add("RESULT         : %s\n", map[bool]string{true: "PASS", false: "FAIL"}[pass])

	if werr := os.WriteFile(*outPath, b, 0o644); werr != nil {
		fmt.Fprintln(os.Stderr, "write out:", werr)
	}
	fmt.Fprint(os.Stderr, string(b))

	if !pass {
		os.Exit(2)
	}
}
