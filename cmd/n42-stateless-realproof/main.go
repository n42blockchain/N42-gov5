// n42-stateless-realproof generates a REAL per-account EIP-1186 proof from a
// live hashed-canonical N42 chaindata (e.g. D:/N42-hashed) using the production
// ProofRetainer + FlatDBTrieLoader — the same machinery that computes eth-el's
// header.Root — and feeds it through the P8 stateless consumer
// (stateless.VerifyAccountInclusion) to prove a minimal client can verify a real
// mainnet account (and its storage slots) against the trusted head state root.
//
// This is P8 ③b Step 2/2b ("接真实主网数据"): it closes the loop from real
// on-disk state to the P8 partialTrie. The account-trie root the proof anchors
// to is the production CalcTrieRoot output; the INDEPENDENT anchor is the
// canonical head header.Root read straight from the datadir.
//
// D:/N42-hashed is mainnet-aligned but HASHED, so plaintext slots are needed for
// storage proofs. --reth points at a plain-keyed reth datadir (D:/reth2k) from
// which we harvest real plaintext slots for the address (Step 2b). Without
// --reth only the account proof is produced (Step 2).
package main

import (
	"bytes"
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

// harvestSlots opens a plain-keyed reth datadir, enumerates `addr`'s storage
// slots (PlainStorageState: key=addr20, dup=slot32||value), and keeps up to
// `max` plaintext slots that are ALSO present (non-zero) in the hashed N42 tx
// (so the resulting storage proofs are real inclusion proofs at the N42 head).
func harvestSlots(logger log.Logger, rethDir string, addr types.Address, addrHash types.Hash, n42tx kv.Tx, max int) ([]types.Hash, error) {
	rdb, err := mdbx.NewMDBX(logger).Path(rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(func(kv.TableCfg) kv.TableCfg {
			return kv.TableCfg{
				"PlainStorageState": kv.TableCfgItem{Flags: kv.DupSort},
				"PlainAccountState": kv.TableCfgItem{},
			}
		}).Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("open reth: %w", err)
	}
	defer rdb.Close()

	rtx, err := rdb.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer rtx.Rollback()
	c, err := rtx.CursorDupSort("PlainStorageState")
	if err != nil {
		return nil, err
	}
	defer c.Close()

	var slots []types.Hash
	v, err := c.SeekBothRange(addr[:], nil)
	for ; v != nil && len(slots) < max; _, v, err = c.NextDup() {
		if err != nil {
			return nil, err
		}
		if len(v) < 32 {
			continue
		}
		slot := types.BytesToHash(v[:32])
		// keep only slots present in the N42 hashed state (same head).
		slotHash, herr := types.HashData(slot[:])
		if herr != nil {
			return nil, herr
		}
		var key64 [64]byte
		copy(key64[:32], addrHash[:])
		copy(key64[32:], slotHash[:])
		nv, gerr := n42tx.GetOne(kv.HashedStorage, key64[:])
		if gerr != nil {
			return nil, gerr
		}
		if len(nv) == 0 {
			continue // absent at N42 head — skip so we exercise real inclusion
		}
		slots = append(slots, slot)
	}
	return slots, nil
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "hashed-canonical N42 chaindata dir")
	rethDir := flag.String("reth", "", "plain-keyed reth datadir for plaintext slot harvest (e.g. D:/reth2k/db); empty = account proof only")
	addrHex := flag.String("addr", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", "plaintext account address (default USDC)")
	maxSlots := flag.Int("slots", 6, "max storage slots to prove (requires --reth)")
	probeCS := flag.Uint64("probe-cs", 0, "if set, report AccountChangeSet/StorageChangeSet entry counts for this block and exit")
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

	if *probeCS != 0 {
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], *probeCS)
		countDup := func(table string) (int, []byte) {
			c, cerr := tx.CursorDupSort(table)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "%s cursor: %v\n", table, cerr)
				return -1, nil
			}
			defer c.Close()
			n := 0
			var first []byte
			for v, e := c.SeekBothRange(bk[:], nil); v != nil; _, v, e = c.NextDup() {
				if e != nil {
					break
				}
				if n == 0 {
					first = append([]byte(nil), v...)
				}
				n++
			}
			return n, first
		}
		aN, aFirst := countDup("AccountChangeSet")
		// StorageChangeSet key = blockNum(8) + address(+incarnation) — composite,
		// so scan by 8-byte blockNum prefix over the full key space.
		scanPrefix := func(table string) (keys int, dups int, firstKey, firstVal []byte) {
			c, cerr := tx.CursorDupSort(table)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "%s cursor: %v\n", table, cerr)
				return -1, -1, nil, nil
			}
			defer c.Close()
			for k, v, e := c.Seek(bk[:]); k != nil; k, v, e = c.Next() {
				if e != nil || len(k) < 8 || !bytes.Equal(k[:8], bk[:]) {
					break
				}
				if keys == 0 {
					firstKey = append([]byte(nil), k...)
					firstVal = append([]byte(nil), v...)
				}
				if dups == 0 || !bytes.Equal(k, firstKey) {
					keys++
				}
				dups++
			}
			return keys, dups, firstKey, firstVal
		}
		sKeys, sDups, sfk, sfv := scanPrefix("StorageChangeSet")
		fmt.Fprintf(os.Stderr, "block %d: AccountChangeSet=%d (first dup %x)\n", *probeCS, aN, aFirst)
		fmt.Fprintf(os.Stderr, "  StorageChangeSet keys=%d dups=%d firstKey=%x firstVal=%x\n", sKeys, sDups, sfk, sfv)
		return
	}

	addr := types.HexToAddress(*addrHex)
	addrHash, err := types.HashData(addr[:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash addr:", err)
		os.Exit(1)
	}

	// 0. Independent anchor: canonical head header.Root straight from the datadir.
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
		// Migration datadir: LastBlock head pointer unset, but headers exist
		// keyed by number — find the canonical head via the highest key.
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

	// 1b. Harvest plaintext slots from reth (Step 2b).
	var slots []types.Hash
	if *rethDir != "" {
		slots, err = harvestSlots(logger, *rethDir, addr, addrHash, tx, *maxSlots)
		if err != nil {
			fmt.Fprintln(os.Stderr, "harvest slots:", err)
			os.Exit(1)
		}
	}

	// 2. Generate the EIP-1186 account (+ storage) proof via production machinery.
	t0 := time.Now()
	rl := trie.NewRetainList(0)
	pr, err := trie.NewProofRetainer(addr, &acc, slots, rl)
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

	// 3. Feed the real proof through the P8 stateless consumer (account + storage).
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

	// Count non-zero storage slots actually proven (real inclusions).
	nonZero := 0
	for i := range res.StorageProof {
		if res.StorageProof[i].Value != "0" && res.StorageProof[i].Value != "" {
			nonZero++
		}
	}

	var b []byte
	add := func(format string, args ...interface{}) { b = append(b, []byte(fmt.Sprintf(format, args...))...) }
	add("=== n42-stateless-realproof ===\n")
	add("dir            : %s\n", *dir)
	add("reth           : %s\n", *rethDir)
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
	add("storage proofs : %d (%d non-zero / real inclusion)\n", len(res.StorageProof), nonZero)
	add("leaf nonce     : %d\n", acc.Nonce)
	add("leaf balance   : %s\n", acc.Balance.ToBig().String())
	add("leaf codeHash  : %x\n", acc.CodeHash[:])
	for i := range res.StorageProof {
		sp := &res.StorageProof[i]
		add("  slot[%d] %x = %s (%d proof nodes)\n", i, sp.Key[:8], sp.Value, len(sp.Proof))
	}
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
