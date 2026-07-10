// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// proof — emit an EIP-1186 proof (account + storage slots) at ANY historical
// height from DATC data.
//
// Construction mirrors the verifier's two-level machinery:
//   - depths above foldDepth: branch nodes assembled from the record path
//     (branchSlotsAt — the exact logic nodeHashAt uses), RLP'd as 17-lists of
//     hash refs. Dense mainnet account-trie tops are always such branches;
//     any other shape falls through to the subtree path below.
//   - at/below foldDepth (and whole storage tries): the subtree's leaves as
//     of N (asOfLeaves) build an in-memory MPT — leaf/extension/branch RLP
//     with <32-byte child inlining — and the path nodes are collected.
//
// Discipline: the assembled proof is then INDEPENDENTLY verified — a pure
// hash-chain walk from the expected root (the real header.Root, or DatcRoots
// in n42 mode) down to the claimed value. Only a proof that survives this
// walk is emitted.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/modules"
)

func keccak(b []byte) types.Hash {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	var out types.Hash
	h.Sum(out[:0])
	return out
}

// ---------------------------------------------------------------------------
// minimal RLP helpers (string + list items)

func rlpStr(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return []byte{b[0]}
	}
	if len(b) < 56 {
		return append([]byte{0x80 + byte(len(b))}, b...)
	}
	var lenb []byte
	for v := len(b); v > 0; v >>= 8 {
		lenb = append([]byte{byte(v)}, lenb...)
	}
	return append(append([]byte{0xb7 + byte(len(lenb))}, lenb...), b...)
}

func rlpList(items ...[]byte) []byte {
	var payload []byte
	for _, it := range items {
		payload = append(payload, it...)
	}
	if len(payload) < 56 {
		return append([]byte{0xc0 + byte(len(payload))}, payload...)
	}
	var lenb []byte
	for v := len(payload); v > 0; v >>= 8 {
		lenb = append([]byte{byte(v)}, lenb...)
	}
	return append(append([]byte{0xf7 + byte(len(lenb))}, lenb...), payload...)
}

// hexPrefix encodes nibbles with the leaf/extension flag (yellow paper HP).
func hexPrefix(nibbles []byte, leaf bool) []byte {
	flag := byte(0)
	if leaf {
		flag = 2
	}
	out := make([]byte, 1+len(nibbles)/2)
	if len(nibbles)%2 == 1 {
		out[0] = (flag+1)<<4 | nibbles[0]
		nibbles = nibbles[1:]
	} else {
		out[0] = flag << 4
	}
	for i := 0; i+1 < len(nibbles); i += 2 {
		out[1+i/2] = nibbles[i]<<4 | nibbles[i+1]
	}
	return out
}

// ---------------------------------------------------------------------------
// in-memory MPT subtree built from as-of-N leaves

// mleaf is one subtree leaf: nibble path relative to the subtree root and the
// COMPLETE RLP item of its value (account RLP verbatim / RLP-string of slot).
type mleaf struct {
	nib  []byte
	item []byte
}

// mptNodeRLP builds the RLP of the subtree over leaves[lo:hi) (sorted by nib,
// all sharing nib[:depth]) and appends every node lying on the path to
// `target` (nil = collect nothing) to *pathOut, top-down. Returns the node's
// RLP (the caller decides ref-vs-inline).
func mptNodeRLP(leaves []mleaf, depth int, target []byte, pathOut *[][]byte) []byte {
	onPath := target != nil
	emit := func(rlp []byte) []byte {
		if onPath {
			*pathOut = append(*pathOut, rlp)
		}
		return rlp
	}
	if len(leaves) == 1 {
		return emit(rlpList(rlpStr(hexPrefix(leaves[0].nib[depth:], true)), leaves[0].item))
	}
	// Longest common prefix from depth.
	lcp := depth
	for {
		if lcp >= len(leaves[0].nib) {
			break
		}
		c := leaves[0].nib[lcp]
		same := true
		for i := 1; i < len(leaves); i++ {
			if lcp >= len(leaves[i].nib) || leaves[i].nib[lcp] != c {
				same = false
				break
			}
		}
		if !same {
			break
		}
		lcp++
	}
	if lcp > depth {
		// Extension covering nib[depth:lcp].
		ext := leaves[0].nib[depth:lcp]
		// The child is on the target path iff the target matches the extension.
		var childTarget []byte
		if onPath && len(target) >= lcp && bytes.Equal(target[depth:lcp], ext) {
			childTarget = target
		}
		child := mptNodeRLP(leaves, lcp, childTarget, pathOut)
		ref := child
		if len(child) >= 32 {
			h := keccak(child)
			ref = rlpStr(h[:])
		}
		return emit(rlpList(rlpStr(hexPrefix(ext, false)), ref))
	}
	// Branch at depth.
	var items [17][]byte
	for i := range items {
		items[i] = []byte{0x80}
	}
	lo := 0
	for lo < len(leaves) {
		c := leaves[lo].nib[depth]
		hi := lo
		for hi < len(leaves) && leaves[hi].nib[depth] == c {
			hi++
		}
		var childTarget []byte
		if onPath && len(target) > depth && target[depth] == c {
			childTarget = target
		}
		child := mptNodeRLP(leaves[lo:hi], depth+1, childTarget, pathOut)
		if len(child) >= 32 {
			h := keccak(child)
			items[c] = rlpStr(h[:])
		} else {
			items[c] = child // inline node embeds verbatim
		}
		lo = hi
	}
	return emit(rlpList(items[:]...))
}

// subtreeLeaves loads the as-of-N leaves under (domain, path) and converts
// them to mleaf form (nibbles relative to the SUBTREE root, full RLP items).
func (q *querier) subtreeLeaves(domain, path []byte, n uint64) ([]mleaf, error) {
	raw, err := q.asOfLeavesEntry(domain, path, n)
	if err != nil {
		return nil, err
	}
	out := make([]mleaf, 0, len(raw))
	for _, lf := range raw {
		nib := lf.remainder[:len(lf.remainder)-1] // strip the 0x10 terminator
		// The leaf node's value entry is always a STRING item wrapping the
		// value's RLP (rlphacks' "double RLP"): the account RLP gets one
		// string wrap; raw storage bytes get RLP'd then wrapped.
		var item []byte
		switch v := lf.value.(type) {
		case rlphacks.RlpEncodedBytes:
			item = rlpStr([]byte(v)) // account RLP → string-wrapped
		case rlphacks.RlpSerializableBytes:
			item = rlpStr(rlpStr([]byte(v))) // raw slot bytes → RLP → string-wrapped
		default:
			return nil, fmt.Errorf("unexpected leaf value type %T", lf.value)
		}
		out = append(out, mleaf{nib: nib, item: item})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// proof assembly

// proofPath returns the EIP-1186 node list for `fullNib` (the key's nibbles
// relative to the domain) as of block N: record-path branches on top, then
// the built-from-leaves subtree below.
func (q *querier) proofPath(domain, fullNib []byte, n uint64) ([][]byte, error) {
	var nodes [][]byte
	path := []byte{}
	dbg := os.Getenv("N42_DATC_PROOF_DEBUG") != ""
	if dbg {
		fmt.Fprintf(os.Stderr, "[pp] sched.e=%v foldDepth=%d\n", q.sched.e, q.foldDepth)
	}
	for {
		tb := time.Now()
		slots, nKids, usable, err := q.branchSlotsAt(domain, path, n)
		authEmpty := q.missAuthEmpty // capture before the root-synth block below recurses and clobbers it
		if dbg {
			fmt.Fprintf(os.Stderr, "[pp] path=%x branchSlotsAt=%v usable=%v nKids=%d\n", path, time.Since(tb), usable, nKids)
		}
		if err != nil {
			return nil, err
		}
		if !usable && len(path) == 0 {
			// The root node (account trie, domain==nil, OR a storage trie, domain
			// set) has no trustworthy empty-path record: synthesize the root
			// branch from its 16 depth-1 children (mirrors synthesizeRoot). A
			// storage root with <2 branch children is degenerate (leaf/extension)
			// — fall through to the subtree fold below, which builds it natively.
			tr := time.Now()
			nKids = 0
			for nib := byte(0); nib < 16; nib++ {
				tn := time.Now()
				r0 := q.recs
				h, exists, err := q.nodeHashAt(domain, []byte{nib}, n)
				if err != nil {
					return nil, err
				}
				if dbg {
					fmt.Fprintf(os.Stderr, "[pp]   root child nib=%x nodeHashAt=%v recs=%d exists=%v\n", nib, time.Since(tn), q.recs-r0, exists)
				}
				if exists {
					hc := h
					slots[nib] = &hc
					nKids++
				}
			}
			if dbg {
				fmt.Fprintf(os.Stderr, "[pp] root synth 16x nodeHashAt=%v nKids=%d\n", time.Since(tr), nKids)
			}
			if nKids >= 2 {
				usable = true
			}
		}
		if !usable {
			if authEmpty {
				return nodes, nil // authoritatively empty subtree at N: absence proven by parent, no fold
			}
			tl := time.Now()
			leaves, err := q.subtreeLeaves(domain, path, n)
			if dbg {
				fmt.Fprintf(os.Stderr, "[pp] path=%x subtreeLeaves=%v nLeaves=%d\n", path, time.Since(tl), len(leaves))
			}
			if err != nil {
				return nil, err
			}
			if len(leaves) == 0 {
				return nodes, nil // empty subtree: absence proven by the parent
			}
			// Leaves carry nibbles RELATIVE to the subtree root; the target is
			// the key's remaining nibbles below `path`.
			sub := mptNodeRLP(leaves, 0, fullNib[len(path):], &nodes)
			// Cross-check: the independently built subtree must reproduce the
			// fold's hash (the value committed by the parent branch).
			if want, exists, ferr := q.foldAt(domain, path, n); ferr == nil && exists {
				got := keccak(sub)
				if got != want {
					return nil, fmt.Errorf("subtree builder mismatch at path %x: built %x fold %x (%d leaves)",
						path, got[:8], want[:8], len(leaves))
				}
			}
			return nodes, nil
		}
		if nKids == 0 {
			return nodes, nil // no subtree here at N (exclusion)
		}
		// Branch RLP: 17-list, children as 32-byte hash refs.
		var items [17][]byte
		for i := range items {
			items[i] = []byte{0x80}
		}
		for nib := 0; nib < 16; nib++ {
			if slots[nib] != nil {
				items[nib] = rlpStr(slots[nib][:])
			}
		}
		nodes = append(nodes, rlpList(items[:]...))
		c := fullNib[len(path)]
		if slots[c] == nil {
			return nodes, nil // child absent: exclusion proven by this branch
		}
		path = append(path, c)
	}
}

// leafFloor returns the leaf-history floor value for one exact key at N
// (nil, false when the key has no live value at N).
func (q *querier) leafFloor(storage bool, key []byte, n uint64) ([]byte, bool, error) {
	c, err := q.leafCursor(storage)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()
	seek := make([]byte, 0, len(key)+8)
	seek = append(seek, key...)
	seek = binary.BigEndian.AppendUint64(seek, n+1)
	k, v, err := c.Seek(seek)
	if err != nil {
		return nil, false, err
	}
	if k == nil {
		k, v, err = c.Last()
	} else {
		k, v, err = c.Prev()
	}
	if err != nil {
		return nil, false, err
	}
	if k == nil || len(k) != len(key)+8 || !bytes.Equal(k[:len(key)], key) {
		return nil, false, nil
	}
	if len(v) == 0 {
		return nil, false, nil // deleted at floor
	}
	return append([]byte{}, v...), true, nil
}

// ---------------------------------------------------------------------------
// independent hash-chain verifier (the emission gate)

// walkProof descends from root through the node set by the key nibbles and
// returns the value item found (nil for a proven absence). Errors mean the
// proof does NOT verify.
func walkProof(root types.Hash, nodes [][]byte, keyNib []byte) ([]byte, error) {
	byHash := make(map[types.Hash][]byte, len(nodes))
	for _, nd := range nodes {
		byHash[keccak(nd)] = nd
	}
	cur, ok := byHash[root]
	if !ok {
		if len(nodes) == 0 {
			return nil, nil // empty trie / absence at the very top
		}
		return nil, fmt.Errorf("root node %x not in proof", root[:8])
	}
	depth := 0
	for {
		items, err := rlpSplitList(cur)
		if err != nil {
			return nil, err
		}
		switch len(items) {
		case 17:
			if depth == len(keyNib) {
				return items[16], nil
			}
			ref := items[keyNib[depth]]
			if len(ref) == 0 {
				return nil, nil // absent child: exclusion
			}
			depth++
			if next, isHash := refResolve(ref, byHash); next != nil {
				cur = next
				continue
			} else if isHash {
				return nil, fmt.Errorf("missing node at depth %d", depth)
			}
			return nil, fmt.Errorf("malformed child ref at depth %d", depth)
		case 2:
			pathNib, leaf := decodeHexPrefix(items[0])
			if leaf {
				if bytes.Equal(keyNib[depth:], pathNib) {
					return items[1], nil
				}
				return nil, nil // different leaf occupies the slot: exclusion
			}
			if len(keyNib)-depth < len(pathNib) || !bytes.Equal(keyNib[depth:depth+len(pathNib)], pathNib) {
				return nil, nil // extension diverges: exclusion
			}
			depth += len(pathNib)
			if next, isHash := refResolve(items[1], byHash); next != nil {
				cur = next
				continue
			} else if isHash {
				return nil, fmt.Errorf("missing node below extension at depth %d", depth)
			}
			return nil, fmt.Errorf("malformed extension child at depth %d", depth)
		default:
			return nil, fmt.Errorf("node with %d items", len(items))
		}
	}
}

// refResolve maps a child reference (32-byte hash string or inline node) to
// the child node bytes. (nil, true) = hash not present in the set.
func refResolve(ref []byte, byHash map[types.Hash][]byte) ([]byte, bool) {
	if len(ref) == 32 {
		var h types.Hash
		copy(h[:], ref)
		nd, ok := byHash[h]
		if !ok {
			return nil, true
		}
		return nd, true
	}
	if len(ref) > 0 {
		return ref, false // inline node (its own RLP, re-listed)
	}
	return nil, false
}

// rlpSplitList splits one RLP list into its items. Branch children that are
// inline nodes come back as their full RLP; string items come back as their
// PAYLOAD bytes.
func rlpSplitList(b []byte) ([][]byte, error) {
	payload, _, err := rlpOpen(b, true)
	if err != nil {
		return nil, err
	}
	var items [][]byte
	for len(payload) > 0 {
		if payload[0] >= 0xc0 {
			// nested list (inline node): keep verbatim
			_, total, err := rlpOpen(payload, true)
			if err != nil {
				return nil, err
			}
			items = append(items, payload[:total])
			payload = payload[total:]
			continue
		}
		content, total, err := rlpOpen(payload, false)
		if err != nil {
			return nil, err
		}
		items = append(items, content)
		payload = payload[total:]
	}
	return items, nil
}

// rlpOpen returns (payload, totalLen) of the first item, asserting list-ness.
func rlpOpen(b []byte, wantList bool) ([]byte, int, error) {
	if len(b) == 0 {
		return nil, 0, fmt.Errorf("empty rlp")
	}
	p := b[0]
	switch {
	case p < 0x80:
		if wantList {
			return nil, 0, fmt.Errorf("not a list")
		}
		return b[:1], 1, nil
	case p < 0xb8:
		l := int(p - 0x80)
		if wantList {
			return nil, 0, fmt.Errorf("not a list")
		}
		return b[1 : 1+l], 1 + l, nil
	case p < 0xc0:
		ll := int(p - 0xb7)
		l := 0
		for i := 0; i < ll; i++ {
			l = l<<8 | int(b[1+i])
		}
		if wantList {
			return nil, 0, fmt.Errorf("not a list")
		}
		return b[1+ll : 1+ll+l], 1 + ll + l, nil
	case p < 0xf8:
		l := int(p - 0xc0)
		if !wantList {
			return nil, 0, fmt.Errorf("unexpected list")
		}
		return b[1 : 1+l], 1 + l, nil
	default:
		ll := int(p - 0xf7)
		l := 0
		for i := 0; i < ll; i++ {
			l = l<<8 | int(b[1+i])
		}
		if !wantList {
			return nil, 0, fmt.Errorf("unexpected list")
		}
		return b[1+ll : 1+ll+l], 1 + ll + l, nil
	}
}

func decodeHexPrefix(b []byte) (nibbles []byte, leaf bool) {
	if len(b) == 0 {
		return nil, false
	}
	leaf = b[0]&0x20 != 0
	if b[0]&0x10 != 0 {
		nibbles = append(nibbles, b[0]&0x0f)
	}
	for _, c := range b[1:] {
		nibbles = append(nibbles, c>>4, c&0x0f)
	}
	return nibbles, leaf
}

// ---------------------------------------------------------------------------
// CLI

func nibblesOfBytes(b []byte) []byte {
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, c>>4, c&0x0f)
	}
	return out
}

func runProof(args []string) {
	fs := flag.NewFlagSet("proof", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (from build)")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir (root oracle)")
	internalRoots := fs.Bool("internal-roots", false, "n42-mode oracle: DatcRoots instead of headerc")
	addrHex := fs.String("addr", "", "account address (0x…)")
	slotsHex := fs.String("slots", "", "comma-separated storage slot keys (0x…)")
	at := fs.Uint64("at", 0, "historical block height")
	foldDepth := fs.Int("fold-depth", 4, "account-trie fold depth (must match data density)")
	fastEOA := fs.Bool("fast-eoa", false, "skip storage-root probes for empty-code accounts (no dense storage-root layer; mainnet-safe: EIP-161 code-less accounts hold no storage)")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	wantRootHex := fs.String("want-root", "", "expected state root hex; bypasses the (slow, random-access) headerc oracle — for timing / offline verification")
	timeSteps := fs.Bool("time", false, "print per-step wall time to stderr")
	cpuProfile := fs.String("cpuprofile", "", "write a CPU profile to this path")
	ckptFold := fs.Bool("ckpt-fold", true, "route early-block subtree folds through the live-key checkpoints (ckpt/ dir) when present — kills the minutes-long early-block future-scan folds. No-op when the DB has no (v2) checkpoints.")
	ckptMaxBlock := fs.Int64("ckpt-max-block", 0, "checkpoint routing gate: 0 = auto (default; live-key-count gate — large sets are excluded because the record path is faster there), >0 = hard cutoff (checkpoints ≤ this block), -1 = use all present")
	_ = fs.Parse(args)
	if *cpuProfile != "" {
		pf, perr := os.Create(*cpuProfile)
		if perr != nil {
			die("cpuprofile: %v", perr)
		}
		if serr := pprof.StartCPUProfile(pf); serr != nil {
			die("start cpuprofile: %v", serr)
		}
		defer pprof.StopCPUProfile()
	}
	if *out == "" || *addrHex == "" {
		die("--out and --addr required")
	}
	addr := types.HexToAddress(*addrHex)
	var slots []types.Hash
	if *slotsHex != "" {
		for _, s := range strings.Split(*slotsHex, ",") {
			slots = append(slots, types.HexToHash(strings.TrimSpace(s)))
		}
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

	metaV, err := tx.GetOne(tDatcMeta, []byte("head"))
	if err != nil || len(metaV) < 8 {
		die("DATC meta missing: %v", err)
	}
	head := binary.BigEndian.Uint64(metaV)
	if *at >= head {
		die("--at %d out of range (head %d)", *at, head)
	}
	schedV, _ := tx.GetOne(tDatcMeta, []byte("sched"))
	var sched epochSchedule
	for d := 0; d <= maxChgDepth && (d+1)*8 <= len(schedV); d++ {
		sched.e[d] = binary.BigEndian.Uint64(schedV[d*8:])
	}
	// Storage-trie schedule: independent since --sto-sched; absent key
	// (pre-split DBs) means the storage side used the account schedule.
	stoSched := sched
	if ssV, _ := tx.GetOne(tDatcMeta, []byte("stoSched")); len(ssV) >= (maxChgDepth+1)*8 {
		for d := 0; d <= maxChgDepth; d++ {
			stoSched.e[d] = binary.BigEndian.Uint64(ssV[d*8:])
		}
	}

	q := &querier{tx: tx, sched: sched, stoSched: stoSched, foldDepth: *foldDepth, fastEOA: *fastEOA}
	{
		cache := newFrameLRU()
		open := func(tab int) *leafSegSet {
			s, ok, err := openLeafSegSet(*out, tab, cache)
			if err != nil || !ok {
				return nil
			}
			return s
		}
		q.segA, q.segS = open(segTabLeafA), open(segTabLeafS)
		q.segCA, q.segCS = open(segTabChgA), open(segTabChgS)
	}
	if *ckptFold {
		st := openCkptStore(*out, *ckptMaxBlock)
		if st.available(segTabLeafA) || st.available(segTabLeafS) {
			q.ckpt, q.ckptFold = st, true
		} else {
			st.Close()
		}
	}

	tstep := time.Now()
	step := func(name string) {
		if *timeSteps {
			fmt.Fprintf(os.Stderr, "[t] %-16s %v\n", name, time.Since(tstep))
			tstep = time.Now()
		}
	}
	step("open+segments")

	// Expected root (the trust anchor).
	var wantRoot types.Hash
	if *wantRootHex != "" {
		wantRoot = types.HexToHash(*wantRootHex)
	} else if *internalRoots {
		var rk [8]byte
		binary.BigEndian.PutUint64(rk[:], *at)
		rv, err := tx.GetOne(tDatcRoots, rk[:])
		if err != nil || len(rv) != 32 {
			die("DatcRoots missing for %d: %v", *at, err)
		}
		copy(wantRoot[:], rv)
	} else {
		hdrs, err := ethel.OpenHeaderCompact(*hdrDir)
		if err != nil {
			die("open headerc: %v", err)
		}
		defer hdrs.Close()
		hdr, err := hdrs.ReadHeader(*at)
		if err != nil {
			die("read header %d: %v", *at, err)
		}
		wantRoot = hdr.Root
	}
	step("oracle root")

	ah := keccak(addr[:])
	accNib := nibblesOfBytes(ah[:])

	// Account proof nodes + account value.
	accNodes, err := q.proofPath(nil, accNib, *at)
	if err != nil {
		die("account proof: %v", err)
	}
	step("acc proofPath")
	if foldStats != nil {
		fmt.Fprintf(os.Stderr, "[foldStats] %v (folds=%d leafReads=%d)\n", foldStats, q.folds, q.leafReads)
	}
	res := &account.AccProofResult{Address: addr, Balance: "0x0", Nonce: 0}
	res.AccountProof = accNodes
	res.CodeHash = keccak(nil)
	res.StorageHash = emptyTrieRoot

	domain := make([]byte, 40)
	copy(domain, ah[:])
	accRaw, accLive, err := q.leafFloor(false, ah[:], *at)
	if err != nil {
		die("account value: %v", err)
	}
	step("acc leafFloor")
	var acct account.StateAccount
	if accLive {
		if err := acct.DecodeForStorage(accRaw); err != nil {
			die("account decode: %v", err)
		}
		res.Balance = "0x" + acct.Balance.Hex()
		res.Nonce = acct.Nonce
		res.CodeHash = acct.CodeHash
		sroot, hasStorage, found := q.storageRootAt(ah[:], *at)
		if !found {
			var err error
			sroot, hasStorage, err = q.nodeHashAt(domain, nil, *at)
			if err != nil {
				die("storage root: %v", err)
			}
		}
		if hasStorage {
			res.StorageHash = sroot
		}
	}

	// Stop the CPU profile here — the reconstruction (proofPath + fold) is the
	// part we profile; the oracle walk below may die() and skip the deferred stop.
	if *cpuProfile != "" {
		pprof.StopCPUProfile()
	}

	if *timeSteps && len(accNodes) > 0 {
		rh := keccak(accNodes[0])
		fmt.Fprintf(os.Stderr, "[root] reconstructed=%x  wantRoot=%x\n", rh[:], wantRoot[:])
	}

	// Independent verification: hash-chain walk from the EXPECTED root.
	gotVal, err := walkProof(wantRoot, accNodes, accNib)
	if err != nil {
		die("PROOF DOES NOT VERIFY against root %x: %v", wantRoot[:8], err)
	}
	if accLive {
		var want bytes.Buffer
		ebuf := make([]byte, acct.EncodingLengthForHashing())
		acct.Root = res.StorageHash
		acct.EncodeForHashing(ebuf)
		want.Write(ebuf)
		if !bytes.Equal(gotVal, want.Bytes()) {
			die("account leaf mismatch: proof walk yields %x, leaf history says %x", gotVal, want.Bytes())
		}
	} else if gotVal != nil {
		die("expected absence, proof walk found a value")
	}

	// Storage proofs.
	for _, slot := range slots {
		sh := keccak(slot[:])
		sp := account.StorProofResult{Key: slot, Value: "0x0"}
		if res.StorageHash != emptyTrieRoot {
			sNib := nibblesOfBytes(sh[:])
			sNodes, err := q.proofPath(domain, sNib, *at)
			if err != nil {
				die("storage proof %x: %v", slot[:8], err)
			}
			sp.Proof = sNodes
			composite := make([]byte, 72)
			copy(composite, domain)
			copy(composite[40:], sh[:])
			sval, slive, err := q.leafFloor(true, composite, *at)
			if err != nil {
				die("slot value: %v", err)
			}
			got, err := walkProof(res.StorageHash, sNodes, sNib)
			if err != nil {
				die("STORAGE PROOF DOES NOT VERIFY for %x: %v", slot[:8], err)
			}
			if slive {
				sp.Value = fmt.Sprintf("0x%x", sval)
				// The walk returns the leaf string's payload = RLP(slotBytes).
				if !bytes.Equal(got, rlpStr(sval)) {
					die("slot leaf mismatch for %x", slot[:8])
				}
			} else if got != nil {
				die("expected slot absence for %x, proof walk found a value", slot[:8])
			}
		}
		res.StorageProof = append(res.StorageProof, sp)
	}

	// Emit as JSON with hex-encoded node arrays.
	type js struct {
		Address      string     `json:"address"`
		Balance      string     `json:"balance"`
		Nonce        uint64     `json:"nonce"`
		CodeHash     string     `json:"codeHash"`
		StorageHash  string     `json:"storageHash"`
		AccountProof []string   `json:"accountProof"`
		StorageProof []sjs      `json:"storageProof,omitempty"`
		Block        uint64     `json:"blockNumber"`
		StateRoot    types.Hash `json:"stateRoot"`
	}
	o := js{
		Address: addr.Hex(), Balance: res.Balance, Nonce: res.Nonce,
		CodeHash: res.CodeHash.Hex(), StorageHash: res.StorageHash.Hex(),
		Block: *at, StateRoot: wantRoot,
	}
	for _, nd := range res.AccountProof {
		o.AccountProof = append(o.AccountProof, fmt.Sprintf("0x%x", nd))
	}
	for _, sp := range res.StorageProof {
		e := sjs{Key: sp.Key.Hex(), Value: sp.Value}
		for _, nd := range sp.Proof {
			e.Proof = append(e.Proof, fmt.Sprintf("0x%x", nd))
		}
		o.StorageProof = append(o.StorageProof, e)
	}
	enc, _ := json.MarshalIndent(o, "", "  ")
	fmt.Println(string(enc))
	fmt.Printf("\n✅ proof VERIFIED against %s root %x at block %d (%d account nodes, %d storage proofs)\n",
		map[bool]string{true: "DatcRoots", false: "header"}[*internalRoots], wantRoot[:8], *at, len(res.AccountProof), len(res.StorageProof))
	_ = filepath.Join
}

type sjs struct {
	Key   string   `json:"key"`
	Value string   `json:"value"`
	Proof []string `json:"proof"`
}

// rlpOpenPayload returns the payload of one RLP string item.
func rlpOpenPayload(b []byte) []byte {
	p, _, err := rlpOpen(b, false)
	if err != nil {
		return nil
	}
	return p
}