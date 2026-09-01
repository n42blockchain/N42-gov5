// n42-datc verify — reconstructs the state root at sampled historical heights
// PURELY from the DATC records (epoch node bytes + change windows + leaf
// history) and compares each against the real header's stateRoot. This is the
// end-to-end gate for the temporal reconstruction: a single wrong child hash,
// window bound, or leaf floor breaks the root.
//
//	n42-datc verify --out D:/n42-datc-proto --headers D:/n42-eth1/chain/freezer \
//	  --samples 50
//
// Fold depths (where the account / storage trie stops using node records and
// folds from the leaf history) come from the build's DatcMeta (--acc-depth /
// --sto-depth at build time); --fold-depth only lowers the account one for
// diagnostics.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
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
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

var emptyTrieRoot = types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

// traceDatc gates verbose branch-resolution diagnostics (DATC_TRACE=1).
var traceDatc = os.Getenv("DATC_TRACE") != ""

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	out := fs.String("out", "", "DATC MDBX dir (from build)")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir")
	internalRoots := fs.Bool("internal-roots", false, "n42-mode oracle: expected roots from the build's DatcRoots table (no headerc)")
	samples := fs.Int("samples", 50, "sampled historical heights")
	foldDepth := fs.Int("fold-depth", 0, "diagnostic: fold the ACCOUNT trie from the leaf history at this depth instead of the build's record depth (0 = use DatcMeta; must not exceed it)")
	at := fs.Uint64("at", 0, "verify exactly this height (overrides sampling)")
	seed := fs.Int64("seed", 42, "sampling seed")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
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
	var hdrs *ethel.HeaderCompactReader
	if !*internalRoots {
		var err error
		hdrs, err = ethel.OpenHeaderCompact(*hdrDir)
		if err != nil {
			die("open headerc: %v", err)
		}
		defer hdrs.Close()
	}

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	q, head, err := loadQuerier(tx, *out, *foldDepth)
	if err != nil {
		die("%v", err)
	}
	{
		if os.Getenv("DATC_CHG_MDBX") != "" {
			// Diagnostic: force the change index through MDBX (ignore chg
			// segments) — used to locate change rows written by a different
			// (mixed) binary that routed them to MDBX rather than spill.
			q.segCA, q.segCS = nil, nil
		}
		if q.segA != nil || q.segS != nil {
			fmt.Printf("leaf history + change index: zstd segments (%s)\n", filepath.Join(*out, leafSegDir))
		}
	}
	rng := rand.New(rand.NewSource(*seed))

	fmt.Printf("DATC verify: head=%d samples=%d accFold=%d stoFold=%d\n", head, *samples, q.accFold, q.stoFold)
	var lat []time.Duration
	okCnt := 0
	for i := 0; i < *samples; i++ {
		n := rng.Uint64() % head
		if *at > 0 {
			n = *at
			*samples = 1
		}
		// Expected root: real header (mainnet mode) or the build's recorded
		// per-block MPT root (n42 mode — DatcRoots oracle). Blocks with no
		// recorded root (state-unchanged blocks) resolve to the previous one.
		var want types.Hash
		if *internalRoots {
			rv, found, err := floorRoot(tx, n)
			if err != nil || !found {
				die("DatcRoots floor for block %d: %v", n, err)
			}
			want = rv
		} else {
			hdr, err := hdrs.ReadHeader(n)
			if err != nil {
				die("header %d: %v", n, err)
			}
			want = hdr.Root
		}
		q.folds, q.recs, q.leafReads = 0, 0, 0
		t0 := time.Now()
		root, exists, err := q.nodeHashAt(nil, nil, n)
		el := time.Since(t0)
		if err != nil {
			die("rootAt(%d): %v", n, err)
		}
		if !exists {
			root = emptyTrieRoot
		}
		lat = append(lat, el)
		status := "OK "
		if root != want {
			status = "FAIL"
		} else {
			okCnt++
		}
		fmt.Printf("  [%s] N=%-8d root=%x.. want=%x..  %6s  (recs=%d folds=%d leafReads=%d)\n",
			status, n, root[:6], want[:6], el.Round(time.Millisecond), q.recs, q.folds, q.leafReads)
		if status == "FAIL" {
			die("root mismatch at block %d: datc=%x want=%x", n, root, want)
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	var sum time.Duration
	for _, l := range lat {
		sum += l
	}
	fmt.Printf("\n✅ %d/%d historical roots reconstructed byte-exact from DATC records.\n", okCnt, *samples)
	fmt.Printf("latency: avg=%s p50=%s p99=%s\n",
		(sum / time.Duration(len(lat))).Round(time.Millisecond),
		lat[len(lat)/2].Round(time.Millisecond),
		lat[len(lat)*99/100].Round(time.Millisecond))
}

// loadQuerier builds a querier from a build's DatcMeta (head, schedule,
// format, record depths, storage-root cadence) and opens the static segment
// sets when the build used --leaf-seg. foldOverride > 0 folds the account
// trie earlier than the record depth (diagnostics); it can never exceed it.
func loadQuerier(tx kv.Tx, out string, foldOverride int) (*querier, uint64, error) {
	metaV, err := tx.GetOne(tDatcMeta, []byte("head"))
	if err != nil || len(metaV) < 8 {
		return nil, 0, fmt.Errorf("DATC meta missing (run build first): %v", err)
	}
	head := binary.BigEndian.Uint64(metaV)
	if err := checkFormat(tx); err != nil {
		return nil, 0, err
	}
	schedV, _ := tx.GetOne(tDatcMeta, []byte("sched"))
	var sched epochSchedule
	for d := 0; d <= maxChgDepth && (d+1)*8 <= len(schedV); d++ {
		sched.e[d] = binary.BigEndian.Uint64(schedV[d*8:])
	}
	u64 := func(key string) (uint64, error) {
		v, err := tx.GetOne(tDatcMeta, []byte(key))
		if err != nil {
			return 0, err
		}
		if len(v) != 8 {
			return 0, fmt.Errorf("DatcMeta/%s missing", key)
		}
		return binary.BigEndian.Uint64(v), nil
	}
	accDepth, err := u64("accdepth")
	if err != nil {
		return nil, 0, err
	}
	stoDepth, err := u64("stodepth")
	if err != nil {
		return nil, 0, err
	}
	cad, err := u64("srcad")
	if err != nil {
		return nil, 0, err
	}
	q := &querier{tx: tx, sched: sched, accFold: int(accDepth), stoFold: int(stoDepth), stoRootPerBlock: cad == 1}
	if foldOverride > 0 {
		if foldOverride > q.accFold {
			return nil, 0, fmt.Errorf("--fold-depth %d exceeds the build's account record depth %d", foldOverride, q.accFold)
		}
		q.accFold = foldOverride
	}
	cache := newFrameLRU()
	open := func(tab int) (*leafSegSet, error) {
		s, ok, err := openLeafSegSet(out, tab, cache)
		if err != nil {
			return nil, fmt.Errorf("leafseg: %w", err)
		}
		if !ok {
			return nil, nil
		}
		return s, nil
	}
	for _, e := range []struct {
		tab int
		dst **leafSegSet
	}{{segTabLeafA, &q.segA}, {segTabLeafS, &q.segS}, {segTabChgA, &q.segCA}, {segTabChgS, &q.segCS}, {segTabStoRoot, &q.segSR}} {
		if *e.dst, err = open(e.tab); err != nil {
			return nil, 0, err
		}
	}
	return q, head, nil
}

// checkFormat refuses data written by a binary with another on-disk format.
func checkFormat(tx kv.Tx) error {
	fv, err := tx.GetOne(tDatcMeta, []byte("format"))
	if err != nil {
		return err
	}
	if len(fv) != 1 || fv[0] != datcFormat {
		return fmt.Errorf("DATC data format %v does not match this binary's format %d (rebuild required)", fv, datcFormat)
	}
	return nil
}

// floorRoot returns the last recorded MPT root ≤ n from DatcRoots (blocks
// that changed no state record no root; their root equals the previous one).
func floorRoot(tx kv.Tx, n uint64) (types.Hash, bool, error) {
	var h types.Hash
	c, err := tx.Cursor(tDatcRoots)
	if err != nil {
		return h, false, err
	}
	defer c.Close()
	var seek [8]byte
	binary.BigEndian.PutUint64(seek[:], n+1)
	k, v, err := c.Seek(seek[:])
	if err != nil {
		return h, false, err
	}
	if k == nil {
		k, v, err = c.Last()
	} else {
		k, v, err = c.Prev()
	}
	if err != nil || k == nil || len(v) != 32 {
		return h, false, err
	}
	copy(h[:], v)
	return h, true, nil
}

// querier reconstructs node hashes at historical heights from DATC data.
type querier struct {
	tx    kv.Tx
	sched epochSchedule
	// accFold / stoFold: depth at/below which the account / storage trie is
	// folded from the leaf history (= the build's record depth, or lower for
	// diagnostics). Records at depth >= fold do not exist.
	accFold, stoFold int
	// stoRootPerBlock: DatcStoRoot has a row for every block a contract's
	// storage changed (per-block build), so the floor row is exact without a
	// change-window check.
	stoRootPerBlock bool

	// seg*, when non-nil, serve the leaf history / change index / storage-root
	// history from static zstd segments (leafseg.go) instead of the MDBX tables.
	segA, segS, segCA, segCS, segSR *leafSegSet

	folds, recs, leafReads int
}

// leafCur is the cursor contract asOfLeaves needs; satisfied by both
// kv.Cursor (MDBX) and *segLeafCursor (segments).
type leafCur interface {
	Seek([]byte) ([]byte, []byte, error)
	Next() ([]byte, []byte, error)
	Prev() ([]byte, []byte, error)
	Last() ([]byte, []byte, error)
	Close()
}

// leafCursor opens the leaf-history cursor for one domain.
func (q *querier) leafCursor(storage bool) (leafCur, error) {
	if storage {
		if q.segS != nil {
			return q.segS.Cursor(), nil
		}
		return q.tx.Cursor(tDatcLeafS)
	}
	if q.segA != nil {
		return q.segA.Cursor(), nil
	}
	return q.tx.Cursor(tDatcLeafA)
}

// chgCursor opens the change-index cursor for one domain.
func (q *querier) chgCursor(storage bool) (leafCur, error) {
	if storage {
		if q.segCS != nil {
			return q.segCS.Cursor(), nil
		}
		return q.tx.Cursor(tDatcStoChg)
	}
	if q.segCA != nil {
		return q.segCA.Cursor(), nil
	}
	return q.tx.Cursor(tDatcAccChg)
}

// stoRootCursor opens the storage-root history cursor.
func (q *querier) stoRootCursor() (leafCur, error) {
	if q.segSR != nil {
		return q.segSR.Cursor(), nil
	}
	return q.tx.Cursor(tDatcStoRoot)
}

// storageRootAt answers "storage root of addrHash as of N" from the
// DatcStoRoot history: the floor row ≤ N is exact when rows are per block
// (E_0 == 1), or when no storage change of this contract falls inside N's
// own depth-0 epoch (window mode: rows are per window; a change in an
// earlier window would have produced a later row). decided=false means the
// caller must resolve via records/fold. exists=false with decided=true means
// the contract has no storage at N.
func (q *querier) storageRootAt(domain []byte, n uint64) (root types.Hash, exists, decided bool, err error) {
	c, err := q.stoRootCursor()
	if err != nil {
		return root, false, false, err
	}
	defer c.Close()
	seek := make([]byte, 0, 32+blkLen)
	seek = append(seek, domain...)
	seek = binary.BigEndian.AppendUint32(seek, uint32(n+1))
	k, v, err := c.Seek(seek)
	if err != nil {
		return root, false, false, err
	}
	if k == nil {
		k, v, err = c.Last()
	} else {
		k, v, err = c.Prev()
	}
	if err != nil {
		return root, false, false, err
	}
	haveFloor := k != nil && len(k) == 32+blkLen && bytes.Equal(k[:32], domain)
	if !q.stoRootPerBlock && q.sched.e[0] > 1 {
		// Rows are per epoch (window): a change inside N's own epoch is not
		// reflected by the floor row yet.
		changed, cerr := q.changedChildren(domain, nil, q.sched.epochOf(0, n), n)
		if cerr != nil {
			return root, false, false, cerr
		}
		if len(changed) > 0 {
			return root, false, false, nil
		}
	}
	if !haveFloor {
		return root, false, true, nil // never had storage before N
	}
	if len(v) == 0 {
		return root, false, true, nil
	}
	copy(root[:], v)
	return root, true, true, nil
}

// nodeHashAt returns the hash of the trie node at `path` (nibbles, relative to
// the domain) as of block N. domain nil = account trie; 40B addrHash+inc =
// that account's storage trie. exists=false → no subtree at this position.
//
// Record-driven path: the floor epoch record gives the children hashes at the
// epoch base; the change window tells which children changed since — those
// recurse. Falls back to a leaf-history fold (handles extensions, inline
// children, branch collapses natively) at/below foldDepth, when no record
// exists, or when the record shape is not a clean fully-hashed branch.
func (q *querier) nodeHashAt(domain, path []byte, n uint64) (types.Hash, bool, error) {
	if domain != nil && len(path) == 0 {
		// Storage root: O(1) from the storage-root history when it is exact
		// at N; otherwise fall through to the record/fold path.
		if h, exists, decided, err := q.storageRootAt(domain, n); err != nil {
			return types.Hash{}, false, err
		} else if decided {
			return h, exists, nil
		}
	}
	slots, nKids, usable, err := q.branchSlotsAt(domain, path, n)
	if err != nil {
		return types.Hash{}, false, err
	}
	if !usable {
		if domain == nil && len(path) == 0 {
			// The account-trie root has no TrieAccount row by convention —
			// synthesize it from its 16 depth-1 children.
			return q.synthesizeRoot(n)
		}
		// Below foldDepth / no clean record / collapsed branch — resolve from
		// leaves (handles extensions, inline children, collapses natively).
		return q.foldAt(domain, path, n)
	}
	if nKids == 0 {
		return types.Hash{}, false, nil
	}
	// keccak(RLP 17-list: 16 child slots + empty value)
	return branch17Hash(slots), true, nil
}

// branchSlotsAt assembles the 16 child hashes of the branch at `path` as of
// block N from the record path (floor record + change-window recursion).
// usable=false means the record path cannot answer this node (at/below
// foldDepth, no clean fully-hashed record, or the branch collapsed to <2
// children) — callers fall back to the leaf fold. Shared by nodeHashAt and
// the proof builder (proof.go), so both follow the exact same logic.
func (q *querier) branchSlotsAt(domain, path []byte, n uint64) (slots [16]*types.Hash, nKids int, usable bool, err error) {
	d := len(path)
	fold := q.accFold
	if domain != nil {
		fold = q.stoFold
	}
	if d >= fold {
		return slots, 0, false, nil
	}
	st, recEpoch, ok, err := q.floorRecord(domain, path, n)
	if err != nil {
		return slots, 0, false, err
	}
	if !ok {
		return slots, 0, false, nil
	}
	q.recs++
	if st.hasState != st.hasHash || st.hasState == 0 {
		// Mixed/inline children — the plain 17-RLP assembly would be wrong.
		return slots, 0, false, nil
	}

	// Children changed since the record: only N's own epoch can hold changes
	// (no record in a later epoch ⇒ untouched in it). If the record IS at N's
	// epoch, it is the end-of-epoch state: usable directly only when N is the
	// epoch's last block; otherwise step back and replay the window.
	curEpoch := q.sched.epochOf(d, n)
	eLen := q.sched.e[d]
	if recEpoch == curEpoch && (n+1)%eLen != 0 {
		st2, recEpoch2, ok2, err2 := q.floorRecordBefore(domain, path, curEpoch)
		if err2 != nil {
			return slots, 0, false, err2
		}
		if !ok2 {
			return slots, 0, false, nil
		}
		st, recEpoch = st2, recEpoch2
		if st.hasState != st.hasHash || st.hasState == 0 {
			return slots, 0, false, nil
		}
	}
	var changed map[byte]bool
	if recEpoch < curEpoch {
		changed, err = q.changedChildren(domain, path, curEpoch, n)
		if err != nil {
			return slots, 0, false, err
		}
	} // recEpoch == curEpoch with N at epoch end → no window
	if traceDatc && domain == nil && d <= 1 {
		// Also dump how many change rows exist for THIS epoch and the next few,
		// to see whether the changes for N's window were recorded under a
		// different epoch than curEpoch (the W-vs-e[d] suspicion).
		cc1, _ := q.changedChildren(domain, path, curEpoch, n)
		cc0, _ := q.changedChildren(domain, path, curEpoch-1, n)
		fmt.Fprintf(os.Stderr, "TRACE d=%d path=%x n=%d recEpoch=%d curEpoch=%d eLen=%d nChanged(cur)=%d nChanged(cur-1)=%d hasState=%04x\n",
			d, path, n, recEpoch, curEpoch, eLen, len(cc1), len(cc0), st.hasState)
	}

	// Assemble the branch: unchanged children from the record, changed ones
	// recursively at N.
	for nib := byte(0); nib < 16; nib++ {
		bit := uint16(1) << nib
		if changed[nib] {
			h, exists, err := q.nodeHashAt(domain, append(append([]byte{}, path...), nib), n)
			if err != nil {
				return slots, 0, false, err
			}
			if exists {
				hc := h
				slots[nib] = &hc
				nKids++
			}
			continue
		}
		if st.hasState&bit != 0 {
			hc := types.Hash(st.hash[nib])
			slots[nib] = &hc
			nKids++
		}
	}
	if nKids == 1 {
		// Branch collapsed at N — the node is a leaf/extension now; only the
		// fold knows its true shape.
		return slots, nKids, false, nil
	}
	return slots, nKids, true, nil
}

// synthesizeRoot assembles the account-trie root node from its 16 depth-1
// children (the root has no persisted row by erigon convention).
func (q *querier) synthesizeRoot(n uint64) (types.Hash, bool, error) {
	var slots [16]*types.Hash
	nKids := 0
	for nib := byte(0); nib < 16; nib++ {
		h, exists, err := q.nodeHashAt(nil, []byte{nib}, n)
		if err != nil {
			return types.Hash{}, false, err
		}
		if exists {
			hc := h
			slots[nib] = &hc
			nKids++
		}
	}
	if nKids == 0 {
		return types.Hash{}, false, nil
	}
	if nKids == 1 {
		return q.foldAt(nil, nil, n) // degenerate early-chain trie
	}
	return branch17Hash(slots), true, nil
}

// branch17Hash hashes a full branch node whose present children are all
// 32-byte hash references: keccak(RLP([c0..c15, ""])).
func branch17Hash(slots [16]*types.Hash) types.Hash {
	payload := 0
	for _, s := range slots {
		if s != nil {
			payload += 33
		} else {
			payload++
		}
	}
	payload++ // empty value slot
	var buf bytes.Buffer
	if payload < 56 {
		buf.WriteByte(byte(0xc0 + payload))
	} else {
		var be [8]byte
		binary.BigEndian.PutUint64(be[:], uint64(payload))
		i := 0
		for be[i] == 0 {
			i++
		}
		buf.WriteByte(byte(0xf7 + (8 - i)))
		buf.Write(be[i:])
	}
	for _, s := range slots {
		if s != nil {
			buf.WriteByte(0xa0)
			buf.Write(s[:])
		} else {
			buf.WriteByte(0x80)
		}
	}
	buf.WriteByte(0x80)
	h := sha3.NewLegacyKeccak256()
	h.Write(buf.Bytes())
	var out types.Hash
	h.Sum(out[:0])
	return out
}

// nodeState is a reconstructed node record: masks plus per-nibble child hashes
// (valid where the hasHash bit is set).
type nodeState struct {
	hasState, hasTree, hasHash uint16
	hash                       [16][32]byte
}

// decodeFullRecord parses a FULL record body (MarshalTrieNode bytes).
func decodeFullRecord(body []byte) (st nodeState, ok bool) {
	hasState, hasTree, hasHash, hashes, _ := trie.UnmarshalTrieNode(body)
	st.hasState, st.hasTree, st.hasHash = hasState, hasTree, hasHash
	hi := 0
	for nib := 0; nib < 16; nib++ {
		if hasHash&(1<<nib) == 0 {
			continue
		}
		if (hi+1)*32 > len(hashes) {
			return st, false
		}
		copy(st.hash[nib][:], hashes[hi*32:])
		hi++
	}
	return st, true
}

// applyDiff folds one DIFF record onto the previous state: masks are replaced;
// changed children take the diff's hashes; unchanged hashed children carry
// over (they must have existed before — else the chain is inconsistent).
func applyDiff(prev nodeState, rec []byte) (st nodeState, ok bool) {
	if len(rec) < 9 {
		return st, false
	}
	st.hasState = binary.BigEndian.Uint16(rec[1:])
	st.hasTree = binary.BigEndian.Uint16(rec[3:])
	st.hasHash = binary.BigEndian.Uint16(rec[5:])
	changed := binary.BigEndian.Uint16(rec[7:])
	pos := 9
	for nib := 0; nib < 16; nib++ {
		bit := uint16(1) << nib
		if st.hasHash&bit == 0 {
			continue
		}
		if changed&bit != 0 {
			if pos+32 > len(rec) {
				return st, false
			}
			copy(st.hash[nib][:], rec[pos:])
			pos += 32
			continue
		}
		if prev.hasHash&bit == 0 {
			return st, false // unchanged hashed child with no prior hash
		}
		st.hash[nib] = prev.hash[nib]
	}
	return st, pos == len(rec)
}

// floorRecord returns the reconstructed node state of the newest record with
// epoch ≤ epochOf(d, n). ok=false covers absent, tombstone, and undecodable
// chains alike — the caller falls back to the fold, which is always correct.
func (q *querier) floorRecord(domain, path []byte, n uint64) (nodeState, uint64, bool, error) {
	d := len(path)
	return q.floorRecordBefore(domain, path, q.sched.epochOf(d, n)+1)
}

// floorRecordBefore reconstructs the newest record with epoch < beforeEpoch by
// walking the DIFF chain back to its FULL anchor (bounded by the builder's
// fullEvery superblock rule) and folding forward.
func (q *querier) floorRecordBefore(domain, path []byte, beforeEpoch uint64) (nodeState, uint64, bool, error) {
	var zero nodeState
	table := tDatcAccNode
	full := path
	if domain != nil {
		table = tDatcStoNode
		full = append(append([]byte{}, domain...), path...)
	}
	prefix := make([]byte, 0, 1+len(full))
	prefix = append(prefix, byte(len(full)))
	prefix = append(prefix, full...)

	c, err := q.tx.Cursor(table)
	if err != nil {
		return zero, 0, false, err
	}
	defer c.Close()
	seek := append(append([]byte{}, prefix...), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(seek[len(prefix):], uint32(beforeEpoch))
	k, v, err := c.Seek(seek)
	if err != nil {
		return zero, 0, false, err
	}
	if k == nil {
		k, v, err = c.Last()
	} else {
		k, v, err = c.Prev()
	}
	if err != nil || k == nil {
		return zero, 0, false, err
	}
	if len(k) != len(prefix)+4 || !bytes.HasPrefix(k, prefix) {
		return zero, 0, false, nil
	}
	epoch := uint64(binary.BigEndian.Uint32(k[len(prefix):]))
	if len(v) == 0 {
		return zero, epoch, false, nil // tombstone
	}
	if v[0] == nodeRecMixed {
		return zero, epoch, false, nil // mixed children: only the fold knows the shape
	}

	// Collect the DIFF chain back to its FULL anchor.
	var diffs [][]byte
	for v[0] == nodeRecDiff {
		diffs = append(diffs, append([]byte{}, v...))
		k, v, err = c.Prev()
		if err != nil || k == nil || len(k) != len(prefix)+4 || !bytes.HasPrefix(k, prefix) || len(v) == 0 || v[0] == nodeRecMixed {
			return zero, 0, false, err // diff without an anchor (or tombstone/MIXED mid-chain)
		}
	}
	if v[0] != nodeRecFull {
		return zero, 0, false, nil
	}
	st, ok := decodeFullRecord(v[1:])
	if !ok {
		return zero, 0, false, nil
	}
	for i := len(diffs) - 1; i >= 0; i-- {
		st, ok = applyDiff(st, diffs[i])
		if !ok {
			return zero, 0, false, nil
		}
	}
	return st, epoch, true, nil
}

// changedChildren decodes the aggregated change rows of n's epoch: range scan
// over (d|domain|path|epoch) — rows are batch segments keyed by their first
// block — and collect events with block ≤ n.
func (q *querier) changedChildren(domain, path []byte, epoch, n uint64) (map[byte]bool, error) {
	d := len(path)
	prefix := make([]byte, 0, 1+len(domain)+d+4)
	prefix = append(prefix, byte(d))
	prefix = append(prefix, domain...)
	prefix = append(prefix, path...)
	prefix = binary.BigEndian.AppendUint32(prefix, uint32(epoch))

	c, err := q.chgCursor(domain != nil)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if traceDatc && domain == nil && d == 1 {
		wide := append([]byte{byte(d)}, path...) // [d][path], no epoch
		if dc, derr := q.chgCursor(false); derr == nil {
			cnt := 0
			for k, _, _ := dc.Seek(wide); k != nil && cnt < 6; k, _, _ = dc.Next() {
				if !bytes.HasPrefix(k, wide) {
					break
				}
				ep := uint64(0)
				if len(k) >= len(wide)+4 {
					ep = uint64(binary.BigEndian.Uint32(k[len(wide) : len(wide)+4]))
				}
				kt := k
				if len(kt) > 16 {
					kt = kt[:16]
				}
				fmt.Fprintf(os.Stderr, "  CHGSCAN path=%x wantEpoch=%d key=%x foundEpoch=%d\n", path, epoch, kt, ep)
				cnt++
			}
			if cnt == 0 {
				fmt.Fprintf(os.Stderr, "  CHGSCAN path=%x wantEpoch=%d: NO rows in bucket [d=1|path]\n", path, epoch)
			}
			dc.Close()
		}
	}
	out := make(map[byte]bool)
	for k, v, err := c.Seek(prefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, prefix) {
			break
		}
		if len(k) != len(prefix)+4 {
			continue
		}
		if uint64(binary.BigEndian.Uint32(k[len(prefix):])) > n {
			break // segment starts past n — later segments only later
		}
		blk := uint64(0)
		pos := 0
		for pos < len(v) {
			dlt, m := binary.Uvarint(v[pos:])
			if m <= 0 || pos+m >= len(v) {
				break
			}
			pos += m
			blk += dlt
			nib := v[pos]
			pos++
			if blk > n {
				break
			}
			out[nib] = true
		}
	}
	return out, nil
}

// foldAtTraced is foldAt with branch capture: every GenStructStep branch
// emission (path → MarshalTrieNode bytes) lands in `out` for diagnostics.
func (q *querier) foldAtTraced(domain, path []byte, n uint64, out map[string][]byte) (types.Hash, bool, error) {
	leaves, err := q.asOfLeaves(domain, path, n)
	if err != nil {
		return types.Hash{}, false, err
	}
	if len(leaves) == 0 {
		return types.Hash{}, false, nil
	}
	hb := trie.NewHashBuilder(false)
	var groups, hasTreeA, hasHashA []uint16
	var curr, succ []byte
	var currVal trie.GenStructStepLeafData
	retain := func(_ []byte) bool { return false }
	hc := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if hasState == 0 {
			return nil
		}
		buf := make([]byte, 6+len(hashes)+len(rootHash))
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		out[string(append([]byte{}, keyHex...))] = append([]byte{}, v...)
		return nil
	}
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

// foldAt rebuilds the subtree at `path` from the leaf history as of block N
// and returns its embedded hash (handles extensions/leaves/inline natively via
// GenStructStep — the same machinery production loaders use).
func (q *querier) foldAt(domain, path []byte, n uint64) (types.Hash, bool, error) {
	q.folds++
	leaves, err := q.asOfLeaves(domain, path, n)
	if err != nil {
		return types.Hash{}, false, err
	}
	if len(leaves) == 0 {
		return types.Hash{}, false, nil
	}
	hb := trie.NewHashBuilder(false)
	var groups, hasTreeA, hasHashA []uint16
	var curr, succ []byte
	var currVal trie.GenStructStepLeafData
	retain := func(_ []byte) bool { return false }
	hc := func([]byte, uint16, uint16, uint16, []byte, []byte) error { return nil }

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

type foldLeaf struct {
	remainder []byte // nibbles below `path`
	value     rlphacks.RlpSerializable
}

// asOfLeaves enumerates the leaves under `path` as of block N from the leaf
// history: per key, the floor entry ≤ N is its value (empty = absent).
func (q *querier) asOfLeaves(domain, path []byte, n uint64) ([]foldLeaf, error) {
	keyLen := 32
	if domain != nil {
		keyLen = stoDomainLen + 32 // storage leaf keys: addrHash(32) + slotHash(32)
	}
	// Byte-prefix from the nibble path (odd nibble → filter below); for
	// storage the path nibbles address the slotHash part after the domain.
	fullNibbles := path
	bytePrefix := make([]byte, 0, len(domain)+len(fullNibbles)/2)
	bytePrefix = append(bytePrefix, domain...)
	for i := 0; i+1 < len(fullNibbles); i += 2 {
		bytePrefix = append(bytePrefix, fullNibbles[i]<<4|fullNibbles[i+1])
	}
	oddNib := len(fullNibbles)%2 == 1
	var oddVal byte
	if oddNib {
		oddVal = fullNibbles[len(fullNibbles)-1]
	}

	c, err := q.leafCursor(domain != nil)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	var out []foldLeaf
	emit := func(hk, val []byte) error {
		if len(val) == 0 {
			return nil // deleted/absent at n
		}
		nibs := nibblesOf(hk[len(domain):]) // hashed key part
		rem := append([]byte{}, nibs[len(fullNibbles):]...)
		rem = append(rem, 0x10) // GenStructStep leaf terminator
		var v rlphacks.RlpSerializable
		if domain == nil {
			var acct account.StateAccount
			if err := acct.DecodeForStorage(val); err != nil {
				return fmt.Errorf("leaf account decode: %w", err)
			}
			// storage root at N for this account (emptyRoot for EOAs).
			sd := hk[:32]
			sroot, hasStorage, err := q.nodeHashAt(sd, nil, n)
			if err != nil {
				return err
			}
			if hasStorage {
				acct.Root = sroot
			} else {
				acct.Root = emptyTrieRoot
			}
			buf := make([]byte, acct.EncodingLengthForHashing())
			acct.EncodeForHashing(buf)
			v = rlphacks.RlpEncodedBytes(buf)
		} else {
			v = rlphacks.RlpSerializableBytes(append([]byte{}, val...))
		}
		out = append(out, foldLeaf{remainder: rem, value: v})
		return nil
	}

	// Adaptive distinct-key walk (the QMDB OldId idea adapted to the sorted
	// (key | block) layout): per key we need only the FLOOR entry ≤ n, so after
	// a few linear steps inside one key's version run we SEEK straight to the
	// floor — (key | n+1) then Prev — and then jump to the next distinct key
	// with another seek. Heavy contracts go from O(total history) reads to
	// O(live keys × log); keys with 1-2 versions stay on the cheap linear path.
	const linearBudget = 4
	var curKey, curVal []byte
	var haveFloor bool
	linearSteps := 0

	k, v, err := c.Seek(bytePrefix)
	for k != nil {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, bytePrefix) {
			break
		}
		if len(k) != keyLen+blkLen {
			k, v, err = c.Next()
			continue
		}
		hk := k[:keyLen]
		if oddNib && hk[len(bytePrefix)]>>4 != oddVal {
			// Wrong odd-nibble branch: skip this whole key.
			k, v, err = seekNextKey(c, hk)
			continue
		}
		if !bytes.Equal(hk, curKey) {
			if err := emit(curKey, ifFloor(haveFloor, curVal)); err != nil {
				return nil, err
			}
			curKey = append(curKey[:0], hk...)
			curVal, haveFloor, linearSteps = nil, false, 0
		}
		blk := uint64(binary.BigEndian.Uint32(k[keyLen:]))
		q.leafReads++
		switch {
		case blk > n:
			// Versions are ascending: everything further in this key is past n.
			k, v, err = seekNextKey(c, hk)
			continue
		case linearSteps < linearBudget:
			curVal = append(curVal[:0], v...)
			haveFloor = true
			linearSteps++
		default:
			// Long version run: jump straight to the floor entry ≤ n.
			seek := make([]byte, 0, keyLen+blkLen)
			seek = append(seek, hk...)
			seek = binary.BigEndian.AppendUint32(seek, uint32(n+1))
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
			if fk != nil && len(fk) == keyLen+blkLen && bytes.Equal(fk[:keyLen], hk) {
				curVal = append(curVal[:0], fv...)
				haveFloor = true
				q.leafReads++
			}
			k, v, err = seekNextKey(c, hk)
			continue
		}
		k, v, err = c.Next()
	}
	if err := emit(curKey, ifFloor(haveFloor, curVal)); err != nil {
		return nil, err
	}
	return out, nil
}

func ifFloor(have bool, v []byte) []byte {
	if !have {
		return nil
	}
	return v
}

// seekNextKey positions the cursor at the first entry of the next distinct
// hashed key after hk (hk + 1 in big-endian arithmetic).
func seekNextKey(c leafCur, hk []byte) ([]byte, []byte, error) {
	next := append([]byte{}, hk...)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			return c.Seek(next)
		}
	}
	return nil, nil, nil // key was all-0xFF: nothing after
}
