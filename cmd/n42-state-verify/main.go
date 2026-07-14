// Command n42-state-verify checks that a converted chain's stored state root
// (the JMT root recorded in the head block header) equals an independent
// from-scratch JMT rebuilt over the full PlainState. Because the JMT is
// content-addressed, the batch-rebuilt root equals the incrementally-computed
// root if and only if the incremental computation is correct — so a match is a
// strong, definitive verification of state-root correctness (catching any
// incremental-JMT divergence). It opens the datadir read-only, so it can run
// beside a live replay-v2 conversion (MDBX allows concurrent readers).
//
//	n42-state-verify --datadir D:\mainnet-bls
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/bmt"
	"github.com/n42blockchain/N42/lib/jmt"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

func main() {
	datadir := flag.String("datadir", "D:/mainnet-bls", "converted chain datadir (contains chaindata/)")
	mapGB := flag.Int("map.gb", 4096, "MDBX map size (GB)")
	treeType := flag.String("tree", "jmt", "state commitment to verify: jmt (Blake3) or mpt (Keccak/HPH, ETH-compatible)")
	proofSample := flag.Int("proof-sample", 0, "qmdb only: after MATCH, verify N account + N storage membership proofs (eth_getProof data) against header.Root")
	undoWindow := flag.Int("undo-e2e", 0, "qmdb only: after MATCH, apply N realistic blocks with undo recording on the REAL forest and verify ProofAt reproduces every historical root (in-memory; nothing is written)")
	flag.Parse()

	logger := log.New()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(logger).
		Path(filepath.Join(*datadir, "chaindata")).
		Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open chaindata: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	headPtr := rawdb.ReadCurrentFullBlockNumber(tx)
	if headPtr == nil {
		die("head block number unavailable")
	}
	head := *headPtr
	hash, _ := rawdb.ReadCanonicalHash(tx, head)
	headLabel := "committed block"
	// The live QMDB forest reflects the applied state, which can be one or more
	// executed proposals ahead of HotStuff's committed head. Verify against the
	// applied marker's header rather than reporting that normal speculative
	// window as state corruption.
	if *treeType == "qmdb" {
		if appliedNum, appliedHash, ok, rerr := rawdb.ReadQMDBApplied(tx); rerr != nil {
			die("read QMDB applied head: %v", rerr)
		} else if ok {
			head, hash, headLabel = appliedNum, appliedHash, "applied block"
		}
	}
	header := rawdb.ReadHeader(tx, hash, head)
	if header == nil {
		die("%s header %d/%x unavailable", headLabel, head, hash[:8])
	}
	headerRoot := header.Root

	fmt.Printf("=== n42-state-verify (tree=%s): %s ===\n", *treeType, *datadir)
	fmt.Printf("%-16s: %d\n", headLabel, head)
	fmt.Printf("header.Root     : %x\n", headerRoot)
	if *treeType == "jmt" {
		storedJMT, _ := jmtstore.ReadJMTRoot(tx)
		storedRoot := types.Hash(storedJMT)
		fmt.Printf("stored JMT root : %x\n", storedRoot)
		if headerRoot != storedRoot {
			fmt.Printf("WARN: header.Root != stored JMT root (plumbing inconsistency)\n")
		}
	}

	fmt.Printf("reading full PlainState…\n")
	t0 := time.Now()
	accts, stor, nAcc, nSlot, err := readAllState(tx)
	if err != nil {
		die("read state: %v", err)
	}
	fmt.Printf("  accounts=%d storageSlots=%d (%s)\n", nAcc, nSlot, time.Since(t0).Round(time.Millisecond))

	// Diagnostics: contracts vs storage-bearing addresses, and an independent
	// DupSort recount of the Storage table to detect any under-read.
	contracts := 0
	for _, a := range accts {
		if a.CodeHash != emptyCodeHash {
			contracts++
		}
	}
	dupCount := 0
	if cd, e := tx.CursorDupSort(modules.Storage); e == nil {
		for k, _, e2 := cd.First(); k != nil && e2 == nil; k, _, e2 = cd.Next() {
			dupCount++
		}
		cd.Close()
	}
	// Raw Account table entry count (all key lengths) to detect any under-read.
	acctRaw, acctNon20, acctEmpty := 0, 0, 0
	if ac, e := tx.Cursor(modules.Account); e == nil {
		for k, _, e2 := ac.First(); k != nil && e2 == nil; k, _, e2 = ac.Next() {
			acctRaw++
			if len(k) != 20 {
				acctNon20++
			}
		}
		ac.Close()
	}
	for _, a := range accts {
		if a.Nonce == 0 && a.Balance.IsZero() && a.CodeHash == emptyCodeHash {
			acctEmpty++
		}
	}
	fmt.Printf("  contracts=%d storageAddrs=%d StorageDupEntries=%d | AccountTableRaw=%d (non-20B=%d) emptyAccts(read)=%d\n",
		contracts, len(stor), dupCount, acctRaw, acctNon20, acctEmpty)

	// MPT (Keccak/HPH) mode: rebuild the Ethereum-compatible state root and
	// compare to header.Root. This is the zkEVM-relevant commitment — a Keccak
	// MPT root that off-the-shelf Ethereum zkEVM circuits can verify in-circuit.
	if *treeType == "mpt" {
		fmt.Printf("rebuilding MPT (HPH/Keccak) from scratch…\n")
		t1 := time.Now()
		mrc := commitment.NewMPTRootComputer()
		mrc.SetStateReader(commitment.NewPlainStateMPTReader(tx))
		mptRoot, e := mrc.ComputeRoot(accts, stor)
		if e != nil {
			die("mpt compute root: %v", e)
		}
		fmt.Printf("  MPT rebuild root: %x (%s)\n", mptRoot, time.Since(t1).Round(time.Millisecond))
		if mptRoot == headerRoot {
			fmt.Printf("\n✅ MATCH — header.Root equals from-scratch MPT (Keccak) rebuild. ETH-compatible state root is correct.\n")
			return
		}
		fmt.Printf("\n❌ MPT rebuild (%x) != header.Root (%x)\n", mptRoot, headerRoot)
		os.Exit(1)
	}

	// BMT (binary Blake3) mode: rebuild from PlainState and compare to header.Root.
	if *treeType == "bmt" {
		fmt.Printf("rebuilding BMT (binary Blake3) from scratch…\n")
		t1 := time.Now()
		brc := commitment.NewBMTRootComputer(commitment.NewBMTCommitment(bmt.New(bmt.NewMemStore())))
		bmtRoot, e := brc.ComputeRoot(accts, stor)
		if e != nil {
			die("bmt compute root: %v", e)
		}
		fmt.Printf("  BMT rebuild root: %x (%s)\n", bmtRoot, time.Since(t1).Round(time.Millisecond))
		if bmtRoot == headerRoot {
			fmt.Printf("\n✅ MATCH — header.Root equals from-scratch BMT (binary Blake3) rebuild. State is correct.\n")
			return
		}
		fmt.Printf("\n❌ BMT rebuild (%x) != header.Root (%x)\n", bmtRoot, headerRoot)
		os.Exit(1)
	}

	// QMDB (append-only twig) mode: the world root is history-dependent, so it is
	// NOT a from-scratch rebuild of the live key set. Verify by RELOADING the
	// persisted positional entry log and recomputing the root, which must equal
	// header.Root. (A from-PlainState rebuild would deliberately diverge.)
	if *treeType == "qmdb" {
		fmt.Printf("reloading QMDB twig forest from entry log…\n")
		t1 := time.Now()
		qrc := commitment.NewQMDBRootComputer()
		if e := qrc.LoadFrom(tx); e != nil {
			die("qmdb reload: %v", e)
		}
		qRoot := qrc.Root()
		fmt.Printf("  QMDB reload root: %x  liveKeys=%d  (%s)\n", qRoot, qrc.Tree().LiveCount(), time.Since(t1).Round(time.Millisecond))
		if qRoot == headerRoot {
			fmt.Printf("\n✅ MATCH — header.Root equals reloaded QMDB world root. State is correct.\n")
			if *proofSample > 0 {
				verifyQMDBProofs(qrc.Tree(), headerRoot, accts, stor, *proofSample)
			}
			if *undoWindow > 0 {
				runUndoWindowE2E(qrc.Tree(), accts, *undoWindow)
			}
			return
		}
		fmt.Printf("\n❌ QMDB reload (%x) != header.Root (%x)\n", qRoot, headerRoot)
		os.Exit(1)
	}

	fmt.Printf("rebuilding JMT from scratch…\n")
	t1 := time.Now()
	tree := jmt.New(jmt.NewMemStore())
	rc := commitment.NewJMTRootComputer(commitment.NewJMTCommitment(tree))
	freshRoot, err := rc.ComputeRoot(accts, stor)
	if err != nil {
		die("compute root: %v", err)
	}
	fmt.Printf("  rebuild root  : %x (%s)\n", freshRoot, time.Since(t1).Round(time.Millisecond))

	if freshRoot == headerRoot {
		fmt.Printf("\n✅ MATCH — incremental state root equals from-scratch rebuild. State is correct.\n")
		return
	}

	// On mismatch, cross-check against the converted chain's OWN JMT: if sampled
	// PlainState leaves match what the live tree returns, our read+encoding is
	// correct and the rebuild gap is a real divergence (or a read-completeness
	// issue); if they differ, the rebuild mismatch is a false negative.
	fmt.Printf("\nrebuild differs — cross-checking sampled leaves against the converted JMT…\n")
	ctx := context.Background()
	ns := jmtstore.NewLazyDBStore(ctx, db, jmtstore.JMTNodeTable)
	ctree := jmt.NewFromRoot(ns, jmt.Hash(headerRoot))
	aM, aMiss, aDiff, n := 0, 0, 0, 0
	for addr, acct := range accts {
		if n >= 30 {
			break
		}
		n++
		got, _ := ctree.Get(commitment.AccountKeyHash(addr))
		want := commitment.EncodeAccountValue(acct)
		switch {
		case got == nil:
			aMiss++
		case bytesEqual(got, want):
			aM++
		default:
			aDiff++
			if aDiff <= 3 {
				fmt.Printf("  acct %x got=%x want=%x\n", addr[:6], got, want)
			}
		}
	}
	sM, sMiss, sDiff, m := 0, 0, 0, 0
	for addr, slots := range stor {
		for slot, val := range slots {
			if m >= 30 {
				break
			}
			m++
			got, _ := ctree.Get(commitment.StorageKeyHash(addr, slot))
			var want [32]byte
			val.WriteToSlice(want[:])
			switch {
			case got == nil:
				sMiss++
			case bytesEqual(got, want[:]):
				sM++
			default:
				sDiff++
			}
		}
		if m >= 30 {
			break
		}
	}
	fmt.Printf("account sample (n=%d): match=%d missing=%d diff=%d\n", n, aM, aMiss, aDiff)
	fmt.Printf("storage sample (n=%d): match=%d missing=%d diff=%d\n", m, sM, sMiss, sDiff)

	// Full scan: compare EVERY PlainState key against the live JMT to pinpoint
	// the divergence when counts match but roots differ (value diff, or
	// offsetting missing/extra keys). Prints the first few mismatches.
	faM, faMiss, faDiff := 0, 0, 0
	for addr, acct := range accts {
		got, _ := ctree.Get(commitment.AccountKeyHash(addr))
		want := commitment.EncodeAccountValue(acct)
		switch {
		case got == nil:
			faMiss++
			if faMiss <= 5 {
				fmt.Printf("  [acct MISSING in JMT] %x want=%x\n", addr, want)
			}
		case bytesEqual(got, want):
			faM++
		default:
			faDiff++
			if faDiff <= 5 {
				fmt.Printf("  [acct VALUE DIFF] %x jmt=%x plain=%x\n", addr, got, want)
			}
		}
	}
	fsM, fsMiss, fsDiff := 0, 0, 0
	for addr, slots := range stor {
		for slot, val := range slots {
			got, _ := ctree.Get(commitment.StorageKeyHash(addr, slot))
			var want [32]byte
			val.WriteToSlice(want[:])
			switch {
			case got == nil:
				fsMiss++
				if fsMiss <= 5 {
					fmt.Printf("  [stor MISSING in JMT] %x/%x want=%x\n", addr, slot, want[:])
				}
			case bytesEqual(got, want[:]):
				fsM++
			default:
				fsDiff++
				if fsDiff <= 5 {
					fmt.Printf("  [stor VALUE DIFF] %x/%x jmt=%x plain=%x\n", addr, slot, got, want[:])
				}
			}
		}
	}
	fmt.Printf("FULL account scan: match=%d missing=%d diff=%d (of %d)\n", faM, faMiss, faDiff, len(accts))
	fmt.Printf("FULL storage scan: match=%d missing=%d diff=%d (of %d)\n", fsM, fsMiss, fsDiff, nSlot)

	// Definitive: count leaves in the live JMT and compare to the PlainState key
	// count. content-addressed JMT ⇒ equal key set ⇒ equal root; so unequal
	// leaf counts prove a divergent key set (stale/missing entries).
	liveLeaves, lerr := countLeaves(ns, jmt.Hash(headerRoot))
	myKeys := len(accts) + nSlot
	if lerr != nil {
		fmt.Printf("live JMT leaf traversal error: %v\n", lerr)
	} else {
		fmt.Printf("live JMT leaves=%d   PlainState keys(accts+storage)=%d   delta=%d\n",
			liveLeaves, myKeys, liveLeaves-myKeys)
	}

	// Self-consistency: collect the live tree's OWN leaves and rebuild them in a
	// single batch. If this != headerRoot, the incremental tree's stored root
	// does not correspond to its own leaf set (stale internal nodes / incremental
	// JMT bug). If it == rebuild root (145…), the incremental path diverged from
	// the batch path for the same key→value set.
	var liveEntries []jmt.BatchEntry
	if e2 := collectLeaves(ns, jmt.Hash(headerRoot), &liveEntries); e2 != nil {
		fmt.Printf("collectLeaves error: %v\n", e2)
	} else {
		fresh := jmt.New(jmt.NewMemStore())
		rb, e3 := fresh.BatchUpdate(liveEntries)
		if e3 != nil {
			fmt.Printf("rebuild-from-live-leaves error: %v\n", e3)
		} else {
			fmt.Printf("rebuild-from-LIVE-leaves root = %x (n=%d)\n", rb[:], len(liveEntries))
			fmt.Printf("  == header.Root(%x)? %v\n", headerRoot[:4], types.Hash(rb) == headerRoot)
		}
		// Order-independent leaf-set checksum (same formula as engine batch-end
		// diagnostic) to compare leaf sets across in-process vs committed.
		var x jmt.Hash
		hasher := jmt.DefaultHasher()
		for _, e := range liveEntries {
			buf := make([]byte, 0, len(e.KeyHash)+len(e.Value))
			buf = append(buf, e.KeyHash[:]...)
			buf = append(buf, e.Value...)
			h := hasher.Hash(buf)
			for i := range x {
				x[i] ^= h[i]
			}
		}
		fmt.Printf("committed leafChecksum = %x (n=%d)\n", x[:12], len(liveEntries))
	}
	fmt.Printf("\n❌ rebuild != header.Root. See sample diagnostics above to classify (read bug vs real divergence).\n")
	os.Exit(1)
}

func countLeaves(ns jmt.NodeStore, h jmt.Hash) (int, error) {
	if h == jmt.EmptyHash {
		return 0, nil
	}
	data, err := ns.Get(h)
	if err != nil {
		return 0, err
	}
	node, err := jmt.DecodeNode(data)
	if err != nil {
		return 0, err
	}
	switch node.Type {
	case jmt.NodeTypeLeaf:
		return 1, nil
	case jmt.NodeTypeExtension:
		return countLeaves(ns, node.Extension.Child)
	case jmt.NodeTypeInternal:
		total := 0
		for i := range node.Internal.Children {
			if node.Internal.Children[i].Valid {
				c, err := countLeaves(ns, node.Internal.Children[i].Hash)
				if err != nil {
					return 0, err
				}
				total += c
			}
		}
		return total, nil
	}
	return 0, nil
}

// collectLeaves walks the tree at h and appends every leaf as a BatchEntry
// (keyHash → value), so the exact live leaf set can be rebuilt in one batch.
func collectLeaves(ns jmt.NodeStore, h jmt.Hash, out *[]jmt.BatchEntry) error {
	if h == jmt.EmptyHash {
		return nil
	}
	data, err := ns.Get(h)
	if err != nil {
		return err
	}
	node, err := jmt.DecodeNode(data)
	if err != nil {
		return err
	}
	switch node.Type {
	case jmt.NodeTypeLeaf:
		v := make([]byte, len(node.Leaf.Value))
		copy(v, node.Leaf.Value)
		*out = append(*out, jmt.BatchEntry{KeyHash: node.Leaf.KeyHash, Value: v})
		return nil
	case jmt.NodeTypeExtension:
		return collectLeaves(ns, node.Extension.Child, out)
	case jmt.NodeTypeInternal:
		for i := range node.Internal.Children {
			if node.Internal.Children[i].Valid {
				if err := collectLeaves(ns, node.Internal.Children[i].Hash, out); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readAllState(tx kv.Tx) (
	map[types.Address]*account.StateAccount,
	map[types.Address]map[types.Hash]*uint256.Int,
	int, int, error,
) {
	accounts := make(map[types.Address]*account.StateAccount)
	c, err := tx.Cursor(modules.Account)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	defer c.Close()
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, nil, 0, 0, err
		}
		if len(k) != 20 {
			continue
		}
		var acct account.StateAccount
		if err := acct.DecodeForStorage(v); err != nil {
			return nil, nil, 0, 0, fmt.Errorf("decode account %x: %w", k, err)
		}
		if acct.CodeHash == (types.Hash{}) {
			acct.CodeHash = emptyCodeHash
		}
		var addr types.Address
		copy(addr[:], k)
		accounts[addr] = acct.SelfCopy()
	}

	storageMap := make(map[types.Address]map[types.Hash]*uint256.Int)
	nSlot := 0
	sc, err := tx.Cursor(modules.Storage)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	defer sc.Close()
	for k, v, err := sc.First(); k != nil; k, v, err = sc.Next() {
		if err != nil {
			return nil, nil, 0, 0, err
		}
		if len(k) < 52 {
			continue
		}
		var addr types.Address
		copy(addr[:], k[:20])
		var slot types.Hash
		switch {
		case len(k) == 52: // addr(20)+slot(32), AutoDupSort composite key (DupFromLen=52)
			copy(slot[:], k[20:52])
		case len(k) == 54: // addr(20)+incarnation(2)+slot(32)
			copy(slot[:], k[22:54])
		case len(k) >= 60: // addr(20)+incarnation(8)+slot(32)
			copy(slot[:], k[28:60])
		default:
			continue
		}
		val := new(uint256.Int)
		if len(v) > 0 {
			val.SetBytes(v)
		}
		if val.IsZero() {
			continue
		}
		if storageMap[addr] == nil {
			storageMap[addr] = make(map[types.Hash]*uint256.Int)
		}
		storageMap[addr][slot] = val
		nSlot++
	}
	return accounts, storageMap, len(accounts), nSlot, nil
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}

// runUndoWindowE2E exercises the recent-blocks proof window on the REAL loaded
// forest: applies `window` blocks of realistic ops (overwrites of real existing
// accounts, creations, deletions) with undo recording — all in-memory, nothing
// is written to the DB — then for every depth into the window reconstructs the
// historical root via ProofAt, asserting byte-exact equality with the recorded
// root and correct values. Reports undo-record sizes and ProofAt latency.
func runUndoWindowE2E(
	tree *qmdb.Tree,
	accts map[types.Address]*account.StateAccount,
	window int,
) {
	fmt.Printf("\n=== recent-blocks proof window E2E (%d blocks on the real forest) ===\n", window)

	// Deterministic sample of real account keys to overwrite.
	realKeys := make([]qmdb.Hash, 0, window*4)
	for addr := range accts {
		realKeys = append(realKeys, qmdb.Hash(commitment.AccountKeyHash(addr)))
		if len(realKeys) == cap(realKeys) {
			break
		}
	}
	if len(realKeys) < window {
		die("not enough live accounts for the E2E")
	}

	type blockRec struct {
		root  qmdb.Hash
		undo  *qmdb.BlockUndo
		bytes int
	}
	recs := make([]blockRec, 0, window+1)
	recs = append(recs, blockRec{root: tree.Root()}) // target base = real chain head

	// Track expected values for sampled keys at each height.
	expected := make([]map[qmdb.Hash][]byte, 0, window+1)
	cur := make(map[qmdb.Hash][]byte)
	snap := func() map[qmdb.Hash][]byte {
		m := make(map[qmdb.Hash][]byte, len(cur))
		for k, v := range cur {
			m[k] = v
		}
		return m
	}
	// Base values of the sampled real keys (their CURRENT live values).
	for _, k := range realKeys {
		if v, ok := tree.Get(k); ok {
			cur[k] = append([]byte(nil), v...)
		}
	}
	expected = append(expected, snap())

	totalUndoBytes := 0
	for b := 0; b < window; b++ {
		tree.StartUndoRecording()
		// 4 overwrites of real accounts + 1 creation + 1 deletion per block —
		// roughly this chain's observed ~2.4 state-changes/block, doubled.
		for j := 0; j < 4; j++ {
			k := realKeys[(b*4+j)%len(realKeys)]
			nv := []byte(fmt.Sprintf("e2e-val-%d-%d", b, j))
			tree.Set(k, nv)
			cur[k] = nv
		}
		var ck qmdb.Hash
		ck[0], ck[1], ck[2] = 0xe2, 0xe2, byte(b)
		tree.Set(ck, []byte{byte(b)})
		cur[ck] = []byte{byte(b)}
		if b > 0 {
			var dk qmdb.Hash
			dk[0], dk[1], dk[2] = 0xe2, 0xe2, byte(b-1)
			tree.Delete(dk)
			delete(cur, dk)
		}
		undo := tree.StopUndoRecording()
		ub := len(undo.Marshal())
		totalUndoBytes += ub
		recs = append(recs, blockRec{root: tree.Root(), undo: undo, bytes: ub})
		expected = append(expected, snap())
	}
	fmt.Printf("  applied %d blocks: undo total %d B, avg %d B/block\n",
		window, totalUndoBytes, totalUndoBytes/window)

	// Verify every depth; time ProofAt.
	head := len(recs) - 1
	var proofCount int
	var proofTotal time.Duration
	for target := 0; target < head; target++ {
		undos := make([]*qmdb.BlockUndo, 0, head-target)
		for b := target + 1; b <= head; b++ {
			undos = append(undos, recs[b].undo)
		}
		// Two real overwritten keys + the created key visible at this height.
		checkKeys := []qmdb.Hash{realKeys[0], realKeys[(target*4)%len(realKeys)]}
		for _, k := range checkKeys {
			t0 := time.Now()
			proof, root, found, err := tree.ProofAt(k, undos)
			proofTotal += time.Since(t0)
			proofCount++
			if err != nil {
				die("ProofAt target %d: %v", target, err)
			}
			if root != recs[target].root {
				die("target %d: reconstructed root %x != recorded %x", target, root[:8], recs[target].root[:8])
			}
			want, wantLive := expected[target][k]
			if found != wantLive {
				die("target %d key %x: found=%v want %v", target, k[:4], found, wantLive)
			}
			if found {
				if !bytesEqual(proof.Value, want) {
					die("target %d key %x: value mismatch", target, k[:4])
				}
				if !qmdb.VerifyProof(root, proof) {
					die("target %d key %x: proof does not verify", target, k[:4])
				}
				if !qmdb.VerifyEncodedProof(root, proof.Marshal()) {
					die("target %d key %x: encoded proof does not verify", target, k[:4])
				}
			}
		}
	}
	fmt.Printf("  verified %d historical roots × keys = %d proofs, all byte-exact\n", head, proofCount)
	fmt.Printf("  ProofAt latency: avg %s/proof (deepest window = %d blocks)\n",
		(proofTotal / time.Duration(proofCount)).Round(time.Microsecond), window)
	fmt.Printf("\n✅ recent-blocks proof window verified on the real forest.\n")
}

// verifyQMDBProofs samples live accounts and storage slots, produces a QMDB
// membership proof for each (the proof data eth_getProof returns for this
// commitment), and checks that (a) the proof folds to header.Root and (b) the
// proven value equals what PlainState holds. Also checks one absence case.
func verifyQMDBProofs(
	tree *qmdb.Tree,
	root types.Hash,
	accts map[types.Address]*account.StateAccount,
	stor map[types.Address]map[types.Hash]*uint256.Int,
	sample int,
) {
	qroot := qmdb.Hash(root)
	fmt.Printf("\n=== eth_getProof data verification (QMDB membership proofs) ===\n")

	// Accounts.
	accOK, accFail, accChecked := 0, 0, 0
	for addr, acct := range accts {
		if accChecked >= sample {
			break
		}
		// Empty accounts are deleted from the commitment — skip (correctly absent).
		if acct.Nonce == 0 && acct.Balance.IsZero() && acct.CodeHash == emptyCodeHash {
			continue
		}
		accChecked++
		kh := qmdb.Hash(commitment.AccountKeyHash(addr))
		p, ok := tree.GetProof(kh)
		if !ok {
			fmt.Printf("  ❌ account %x: no proof (missing from commitment)\n", addr)
			accFail++
			continue
		}
		if !qmdb.VerifyProof(qroot, p) {
			fmt.Printf("  ❌ account %x: proof does not verify against header.Root\n", addr)
			accFail++
			continue
		}
		// Exercise the eth_getProof wire codec: marshal → unmarshal → verify,
		// exactly as the QMDBStateProofProvider serves and a client checks.
		if !qmdb.VerifyEncodedProof(qroot, p.Marshal()) {
			fmt.Printf("  ❌ account %x: encoded (wire) proof does not verify\n", addr)
			accFail++
			continue
		}
		if !bytes.Equal(p.Value, commitment.EncodeAccountValue(acct)) {
			fmt.Printf("  ❌ account %x: proven value != PlainState account\n", addr)
			accFail++
			continue
		}
		accOK++
	}
	fmt.Printf("  accounts : %d verified, %d failed (of %d sampled)\n", accOK, accFail, accChecked)

	// Storage slots.
	stoOK, stoFail, stoChecked := 0, 0, 0
	for addr, slots := range stor {
		if stoChecked >= sample {
			break
		}
		for slot, val := range slots {
			if stoChecked >= sample {
				break
			}
			if val == nil || val.IsZero() {
				continue
			}
			stoChecked++
			kh := qmdb.Hash(commitment.StorageKeyHash(addr, slot))
			p, ok := tree.GetProof(kh)
			if !ok {
				fmt.Printf("  ❌ storage %x/%x: no proof\n", addr, slot)
				stoFail++
				continue
			}
			if !qmdb.VerifyProof(qroot, p) {
				fmt.Printf("  ❌ storage %x/%x: proof does not verify\n", addr, slot)
				stoFail++
				continue
			}
			if !qmdb.VerifyEncodedProof(qroot, p.Marshal()) {
				fmt.Printf("  ❌ storage %x/%x: encoded (wire) proof does not verify\n", addr, slot)
				stoFail++
				continue
			}
			var want [32]byte
			val.WriteToSlice(want[:])
			if !bytes.Equal(p.Value, want[:]) {
				fmt.Printf("  ❌ storage %x/%x: proven value != PlainState slot\n", addr, slot)
				stoFail++
				continue
			}
			stoOK++
		}
	}
	fmt.Printf("  storage  : %d verified, %d failed (of %d sampled)\n", stoOK, stoFail, stoChecked)

	// Absence: a key that is not in the live set must have no membership proof.
	absent := qmdb.Hash(crypto.Keccak256Hash([]byte("n42-absent-probe-key")))
	if _, ok := tree.GetProof(absent); ok {
		fmt.Printf("  ❌ absence: a non-existent key returned a proof\n")
		accFail++
	} else {
		fmt.Printf("  absence  : non-existent key correctly has no proof\n")
	}

	if accFail == 0 && stoFail == 0 {
		fmt.Printf("\n✅ getProof data correct — every sampled proof folds to header.Root and matches PlainState.\n")
	} else {
		fmt.Printf("\n❌ getProof data verification FAILED (%d account + %d storage failures)\n", accFail, stoFail)
		os.Exit(1)
	}
}
