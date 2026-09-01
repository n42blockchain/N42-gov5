// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// leaf-audit — nail bug #2's last mile. For ONE account-trie node (nibble path,
// e.g. b50007) at height N, enumerate every leaf the fold would include, print
// full per-leaf provenance (floor block, nonce, balance, codeHash, RECONSTRUCTED
// storage root, EIP-158-empty flag), then fold several leaf subsets and compare
// each against the node's HPH record hash (read from the parent record — the
// build's ground truth that boundary proofs verify through):
//
//	fold(ALL)        — what foldAt computes today (known wrong for b50 subtrees)
//	fold(-empties)   — excluding EIP-158 empty accounts (the top hypothesis)
//	leave-one-out    — if neither matches, drop each single leaf until one matches
//
// A match identifies the exact spurious leaf and validates the fix in one shot.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

// auditLeaf is one account leaf under the audited node, with everything needed
// to explain (and re-fold) it.
type auditLeaf struct {
	hk         types.Hash // hashed account key
	floorBlk   uint64     // leaf-history version selected (floor ≤ N)
	acct       account.StateAccount
	sroot      types.Hash // reconstructed storage root at N (emptyTrieRoot if none)
	hasStorage bool
	empty158   bool // nonce==0 && balance==0 && empty code (EIP-161/158 empty)
	leaf       foldLeaf
}

func runLeafAudit(args []string) {
	fs := flag.NewFlagSet("leaf-audit", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (from build)")
	at := fs.Uint64("at", 0, "historical block height N")
	pathHex := fs.String("path", "", "account-trie node to audit, as hex nibbles (e.g. b50007)")
	maxLoo := fs.Int("max-loo", 128, "max leaves for the leave-one-out search")
	mapGB := fs.Int("map.gb", 1024, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" || *pathHex == "" || *at == 0 {
		die("--out, --path and --at required")
	}

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	schedV, _ := tx.GetOne(tDatcMeta, []byte("sched"))
	var sched epochSchedule
	for d := 0; d <= maxChgDepth && (d+1)*8 <= len(schedV); d++ {
		sched.e[d] = binary.BigEndian.Uint64(schedV[d*8:])
	}
	q := &querier{tx: tx, sched: sched, foldDepth: 4}
	cache := newFrameLRU()
	openSeg := func(tab int) *leafSegSet {
		s, ok, e := openLeafSegSet(*out, tab, cache)
		if e != nil || !ok {
			return nil
		}
		return s
	}
	q.segA, q.segS = openSeg(segTabLeafA), openSeg(segTabLeafS)
	q.segCA, q.segCS = openSeg(segTabChgA), openSeg(segTabChgS)

	p, err := parseNibbleHex(*pathHex)
	if err != nil {
		die("--path: %v", err)
	}
	n := *at
	fmt.Printf("leaf-audit node=%x at=%d sched.e=%v\n", p, n, sched.e)

	// 1. Ground truth: the node's hash per the parent's HPH record.
	base, baseOK := recordChildHash(q, p, n)
	if baseOK {
		fmt.Printf("record base hash (parent record child %x): %x\n", p[len(p)-1], base)
	} else {
		fmt.Printf("record base hash: UNAVAILABLE (parent record missing/mixed or child flagged changed)\n")
	}

	// 2. Enumerate leaves with provenance.
	audits, err := collectAuditLeaves(q, p, n)
	if err != nil {
		die("collect: %v", err)
	}
	nEmpty := 0
	for i := range audits {
		a := &audits[i]
		mark := ""
		if a.empty158 {
			mark = "  <<< EIP-158 EMPTY"
			nEmpty++
		}
		stor := "-"
		if a.hasStorage {
			stor = fmt.Sprintf("%x", a.sroot[:8])
		}
		fmt.Printf("  leaf hk=%x floorBlk=%d nonce=%d bal=%s code=%x stor=%s%s\n",
			a.hk, a.floorBlk, a.acct.Nonce, a.acct.Balance.String(), a.acct.CodeHash[:4], stor, mark)
	}
	fmt.Printf("%d leaves under %x at N=%d (%d EIP-158 empty)\n", len(audits), p, n, nEmpty)

	// 3. Cross-check my enumeration against the production fold path.
	refFold, refEx, err := q.foldAt(nil, p, n)
	if err != nil {
		die("foldAt: %v", err)
	}
	all := make([]foldLeaf, len(audits))
	for i := range audits {
		all[i] = audits[i].leaf
	}
	mine, mineEx, err := foldLeafList(p, all)
	if err != nil {
		die("fold(all): %v", err)
	}
	fmt.Printf("fold(ALL)      = %x exists=%v   (foldAt ref = %x exists=%v  agree=%v)\n",
		mine[:12], mineEx, refFold[:12], refEx, mine == refFold && mineEx == refEx)
	if mine != refFold || mineEx != refEx {
		fmt.Println("!!! audit enumeration diverges from asOfLeaves — fix the audit before trusting subset folds")
	}
	if baseOK && mine == base {
		fmt.Println("fold(ALL) == record base — no divergence at this node; audit a different path")
		return
	}

	// 4. Hypothesis folds.
	if nEmpty > 0 {
		var kept []foldLeaf
		for i := range audits {
			if !audits[i].empty158 {
				kept = append(kept, audits[i].leaf)
			}
		}
		h, ex, err := foldLeafList(p, kept)
		if err != nil {
			die("fold(-empties): %v", err)
		}
		verdict := ""
		if baseOK {
			verdict = fmt.Sprintf("  MATCH-BASE=%v", ex && h == base)
		}
		fmt.Printf("fold(-%d empt) = %x exists=%v%s\n", nEmpty, h[:12], ex, verdict)
		if baseOK && ex && h == base {
			fmt.Println(">>> CONFIRMED: excluding EIP-158 empty accounts reproduces the record hash")
			return
		}
	}

	// 5. Leave-one-out.
	if !baseOK {
		fmt.Println("(no record base to compare against — stopping after enumeration)")
		return
	}
	if len(audits) > *maxLoo {
		fmt.Printf("(%d leaves > --max-loo %d — skipping leave-one-out)\n", len(audits), *maxLoo)
		return
	}
	found := false
	for i := range audits {
		sub := make([]foldLeaf, 0, len(all)-1)
		sub = append(sub, all[:i]...)
		sub = append(sub, all[i+1:]...)
		h, ex, err := foldLeafList(p, sub)
		if err != nil {
			die("fold(-leaf %d): %v", i, err)
		}
		if ex && h == base {
			a := &audits[i]
			fmt.Printf(">>> LEAVE-ONE-OUT MATCH: dropping leaf hk=%x (floorBlk=%d nonce=%d bal=%s code=%x empty158=%v) reproduces the record hash\n",
				a.hk, a.floorBlk, a.acct.Nonce, a.acct.Balance.String(), a.acct.CodeHash[:4], a.empty158)
			found = true
		}
	}
	if !found {
		fmt.Println("no single-leaf exclusion matches the record — divergence is a missing leaf, a wrong leaf VALUE, or multi-leaf; compare leaf values against an independent source next")
		// Value-level aid: print each leaf's exact RLP fed to GenStructStep.
		for i := range audits {
			a := &audits[i]
			var buf bytes.Buffer
			if err := a.leaf.value.ToDoubleRLP(&buf, make([]byte, 16)); err == nil {
				fmt.Printf("  leaf hk=%x rlp=%x\n", a.hk, buf.Bytes())
			}
		}
		// Missing-leaf / wrong-value probes: full version history of EVERY key
		// under the path (dead floors included) + any storage history per key.
		if err := auditVersions(q, p, n); err != nil {
			fmt.Printf("auditVersions: %v\n", err)
		}
	}
}

// auditVersions prints, for every distinct account key under the nibble path,
// its complete leaf-history version list (block → value length; 0 = deletion)
// and whether ANY storage-leaf history exists for that key — exposing keys the
// live-floor walk skipped (a spurious deletion = the missing leaf) and EOAs
// with residual storage history (a wrong storage root = the wrong value).
func auditVersions(q *querier, p []byte, n uint64) error {
	bytePrefix := make([]byte, 0, len(p)/2)
	for i := 0; i+1 < len(p); i += 2 {
		bytePrefix = append(bytePrefix, p[i]<<4|p[i+1])
	}
	odd := len(p)%2 == 1

	c, err := q.leafCursor(false)
	if err != nil {
		return err
	}
	defer c.Close()
	sc, err := q.leafCursor(true)
	if err != nil {
		return err
	}
	defer sc.Close()

	fmt.Println("--- full version history under path ---")
	var curKey []byte
	var versions []string
	liveAtN := false
	flush := func() {
		if curKey == nil {
			return
		}
		mark := ""
		if !liveAtN {
			mark = "  <<< NOT LIVE AT N (dead/absent floor — missing-leaf candidate)"
		}
		// Any storage history under this account key?
		sd := make([]byte, 40)
		copy(sd, curKey)
		srows := 0
		for sk, _, se := sc.Seek(sd); sk != nil && se == nil && srows < 3; sk, _, se = sc.Next() {
			if len(sk) < 40 || !bytes.Equal(sk[:40], sd) {
				break
			}
			srows++
		}
		fmt.Printf("  key=%x versions=%v storHistRows=%d%s\n", curKey, versions, srows, mark)
	}
	k, v, err := c.Seek(bytePrefix)
	for k != nil {
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(k, bytePrefix) {
			break
		}
		if len(k) != 40 {
			k, v, err = c.Next()
			continue
		}
		hk := k[:32]
		if odd && hk[len(bytePrefix)]>>4 != p[len(p)-1] {
			k, v, err = seekNextKey(c, hk)
			continue
		}
		if !bytes.Equal(hk, curKey) {
			flush()
			curKey = append([]byte{}, hk...)
			versions = versions[:0]
			liveAtN = false
		}
		blk := binary.BigEndian.Uint64(k[32:])
		versions = append(versions, fmt.Sprintf("%d:%d", blk, len(v)))
		if blk <= n && len(v) > 0 {
			liveAtN = true
		} else if blk <= n && len(v) == 0 {
			liveAtN = false
		}
		k, v, err = c.Next()
	}
	flush()
	return nil
}

// parseNibbleHex turns "b50007" into []byte{0xb,5,0,0,0,7}.
func parseNibbleHex(s string) ([]byte, error) {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		var b byte
		if _, err := fmt.Sscanf(s[i:i+1], "%x", &b); err != nil {
			return nil, fmt.Errorf("bad nibble %q", s[i:i+1])
		}
		out[i] = b
	}
	return out, nil
}

// recordChildHash reads the audited node's hash from its PARENT's floor record
// (with the same step-back rule branchSlotsAt applies). ok=false when the
// parent record can't answer: missing, mixed masks, or the child is flagged
// changed in the window (then the record hash is not the value at N).
func recordChildHash(q *querier, p []byte, n uint64) (types.Hash, bool) {
	if len(p) == 0 || len(p)-1 > maxChgDepth {
		return types.Hash{}, false
	}
	parent, child := p[:len(p)-1], p[len(p)-1]
	d := len(parent)
	st, recEpoch, ok, err := q.floorRecord(nil, parent, n)
	if err != nil || !ok {
		return types.Hash{}, false
	}
	curEpoch := q.sched.epochOf(d, n)
	eLen := q.sched.e[d]
	if recEpoch == curEpoch && (n+1)%eLen != 0 {
		st2, recEpoch2, ok2, _ := q.floorRecordBefore(nil, parent, curEpoch)
		if !ok2 {
			return types.Hash{}, false
		}
		st, recEpoch = st2, recEpoch2
	}
	if recEpoch < curEpoch {
		changed, err := q.changedChildren(nil, parent, curEpoch, n)
		if err != nil || changed[child] {
			return types.Hash{}, false // record hash is pre-window, not the value at N
		}
	}
	if st.hasState&(1<<child) == 0 || st.hasHash&(1<<child) == 0 {
		return types.Hash{}, false
	}
	return types.Hash(st.hash[child]), true
}

// collectAuditLeaves walks distinct account keys under the nibble path and, for
// each floor(≤N) live leaf, decodes the account, reconstructs its storage root
// (same derivation asOfLeaves uses, fastEOA off), and flags EIP-158 emptiness.
func collectAuditLeaves(q *querier, p []byte, n uint64) ([]auditLeaf, error) {
	bytePrefix := make([]byte, 0, len(p)/2)
	for i := 0; i+1 < len(p); i += 2 {
		bytePrefix = append(bytePrefix, p[i]<<4|p[i+1])
	}
	odd := len(p)%2 == 1

	c, err := q.leafCursor(false)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	var out []auditLeaf
	var curKey []byte
	k, _, err := c.Seek(bytePrefix)
	for k != nil {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, bytePrefix) {
			break
		}
		if len(k) != 40 {
			k, _, err = c.Next()
			continue
		}
		hk := k[:32]
		if odd && hk[len(bytePrefix)]>>4 != p[len(p)-1] {
			k, _, err = seekNextKey(c, hk)
			continue
		}
		if bytes.Equal(hk, curKey) {
			k, _, err = c.Next()
			continue
		}
		curKey = append(curKey[:0], hk...)

		// Floor version ≤ n for this key: Seek(hk|n+1) then Prev (Last at EOF).
		seek := make([]byte, 40)
		copy(seek, hk)
		binary.BigEndian.PutUint64(seek[32:], n+1)
		fk, fv, ferr := c.Seek(seek)
		if ferr != nil {
			return nil, ferr
		}
		if fk == nil {
			fk, fv, ferr = c.Last()
		} else {
			fk, fv, ferr = c.Prev()
		}
		if ferr != nil {
			return nil, ferr
		}
		if fk != nil && len(fk) == 40 && bytes.Equal(fk[:32], hk) && len(fv) > 0 {
			var a auditLeaf
			copy(a.hk[:], hk)
			a.floorBlk = binary.BigEndian.Uint64(fk[32:])
			if err := a.acct.DecodeForStorage(fv); err != nil {
				return nil, fmt.Errorf("leaf %x decode: %w", hk, err)
			}
			sd := make([]byte, 40)
			copy(sd, hk)
			sroot, hasStorage, serr := q.nodeHashAt(sd, nil, n)
			if serr != nil {
				return nil, serr
			}
			a.hasStorage = hasStorage
			if hasStorage {
				a.sroot = sroot
				a.acct.Root = sroot
			} else {
				a.sroot = emptyTrieRoot
				a.acct.Root = emptyTrieRoot
			}
			a.empty158 = a.acct.Nonce == 0 && a.acct.Balance.IsZero() && a.acct.IsEmptyCodeHash()
			nibs := nibblesOf(hk)
			rem := append([]byte{}, nibs[len(p):]...)
			rem = append(rem, 0x10)
			buf := make([]byte, a.acct.EncodingLengthForHashing())
			a.acct.EncodeForHashing(buf)
			a.leaf = foldLeaf{remainder: rem, value: rlphacks.RlpEncodedBytes(buf)}
			out = append(out, a)
		}
		// Jump past this key's remaining versions.
		k, _, err = seekNextKey(c, hk)
	}
	return out, nil
}

// foldLeafList folds an explicit leaf list exactly the way foldAt does
// (GenStructStep + HashBuilder), so subset folds are comparable to production.
func foldLeafList(_ []byte, leaves []foldLeaf) (types.Hash, bool, error) {
	if len(leaves) == 0 {
		return types.Hash{}, false, nil
	}
	hb := trie.NewHashBuilder(false)
	var groups, hasTreeA, hasHashA []uint16
	var curr, succ []byte
	var currVal trie.GenStructStepLeafData
	retain := func(_ []byte) bool { return false }
	hc := func([]byte, uint16, uint16, uint16, []byte, []byte) error { return nil }
	var err error
	for i := range leaves {
		succ = append(succ[:0], leaves[i].remainder...)
		if len(curr) > 0 {
			groups, hasTreeA, hasHashA, err = trie.GenStructStep(retain, curr, succ, hb, hc, &currVal, groups, hasTreeA, hasHashA, false)
			if err != nil {
				return types.Hash{}, false, err
			}
		}
		curr = append(curr[:0], succ...)
		currVal.Value = leaves[i].value
	}
	if _, _, _, err := trie.GenStructStep(retain, curr, []byte{}, hb, hc, &currVal, groups, hasTreeA, hasHashA, false); err != nil {
		return types.Hash{}, false, err
	}
	root, err := hb.RootHash()
	if err != nil {
		return types.Hash{}, false, err
	}
	return root, true, nil
}
