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
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

func main() {
	datadir := flag.String("datadir", "D:/mainnet-bls", "converted chain datadir (contains chaindata/)")
	mapGB := flag.Int("map.gb", 4096, "MDBX map size (GB)")
	treeType := flag.String("tree", "jmt", "state commitment to verify: jmt (Blake3) or mpt (Keccak/HPH, ETH-compatible)")
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

	headPtr := rawdb.ReadCurrentBlockNumber(tx)
	if headPtr == nil {
		die("head block number unavailable")
	}
	head := *headPtr
	hash, _ := rawdb.ReadCanonicalHash(tx, head)
	header := rawdb.ReadHeader(tx, hash, head)
	if header == nil {
		die("head header %d unavailable", head)
	}
	headerRoot := header.Root

	fmt.Printf("=== n42-state-verify (tree=%s): %s ===\n", *treeType, *datadir)
	fmt.Printf("head block      : %d\n", head)
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
