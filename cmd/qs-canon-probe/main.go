// Command qs-canon-probe prints, for one or more chaindata dirs, the canonical
// hash + header presence at a given height range — used to diagnose canonical
// divergence across stopped HotStuff validator node DBs (same-height competing
// blocks after view changes).
//
//	qs-canon-probe -from 13013137 -to 13013141 E:/qs-node0/chaindata E:/qs-node1/chaindata ...
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func main() {
	from := flag.Uint64("from", 0, "first height")
	to := flag.Uint64("to", 0, "last height (inclusive)")
	walk := flag.Bool("walk", false, "walk parent chain down from HeadBlockHash; print derived vs canonical hash over [from,to]")
	qmdbRoot := flag.Bool("qmdbroot", false, "LoadFrom the QMDB forest twice and print both roots vs the applied marker's header root (fidelity + determinism probe)")
	qmdbDiff := flag.Bool("qmdbdiff", false, "diff the QMDB twig tables of exactly two chaindata dirs (same applied history must be byte-identical; a differing twig localizes an unwind repair bug)")
	qmdbOps := flag.Bool("qmdbops", false, "load the forest twice from one store and apply an identical synthetic op sequence to both instances; roots must match (miner-isolated vs live instance equivalence probe)")
	revertDepth := flag.Uint64("qmdbrevert", 0, "N>0: load the forest at the applied marker and ApplyUndo N blocks newest-to-oldest, comparing the tree root against the canonical header root after every step — the first mismatch pinpoints an unfaithful revert (in-memory only, store untouched)")
	audit := flag.Bool("stateaudit", false, "cross-check every PlainState row against the reloaded QMDB tree (the network-verified commitment); splits a deterministic wrong-root wedge into corrupt-flat-input vs execution/index fault")
	csAddr := flag.String("csgrep", "", "hex address: list every changeset row recording a pre-value for it (did this key's writes go through the changeset writer?)")
	flag.Parse()
	if *csAddr != "" {
		modules.N42Init()
		kv.ChaindataTablesCfg = modules.N42TableCfg
		csGrep(flag.Arg(0), *csAddr)
		return
	}
	if *audit {
		modules.N42Init()
		kv.ChaindataTablesCfg = modules.N42TableCfg
		stateAudit(flag.Arg(0))
		return
	}
	if *revertDepth > 0 {
		modules.N42Init()
		kv.ChaindataTablesCfg = modules.N42TableCfg
		revertLadder(flag.Arg(0), *revertDepth)
		return
	}
	if *qmdbOps {
		modules.N42Init()
		kv.ChaindataTablesCfg = modules.N42TableCfg
		opsQMDB(flag.Arg(0))
		return
	}
	if *qmdbDiff {
		if flag.NArg() != 2 {
			fmt.Fprintln(os.Stderr, "-qmdbdiff needs exactly two chaindata dirs")
			os.Exit(1)
		}
		modules.N42Init()
		kv.ChaindataTablesCfg = modules.N42TableCfg
		diffQMDB(flag.Arg(0), flag.Arg(1))
		return
	}
	if flag.NArg() == 0 || *to < *from {
		fmt.Fprintln(os.Stderr, "usage: qs-canon-probe -from N -to M <chaindata>...")
		os.Exit(1)
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	for _, dir := range flag.Args() {
		db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
			MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
		if err != nil {
			fmt.Printf("%s: open: %v\n", dir, err)
			continue
		}
		tx, err := db.BeginRo(context.Background())
		if err != nil {
			fmt.Printf("%s: begin: %v\n", dir, err)
			db.Close()
			continue
		}
		head := rawdb.ReadCurrentBlockNumber(tx)
		headStr := "?"
		if head != nil {
			headStr = fmt.Sprintf("%d", *head)
		}
		hbh := rawdb.ReadHeadBlockHash(tx)
		hbn := rawdb.ReadHeaderNumber(tx, hbh)
		hbnStr := "?"
		if hbn != nil {
			hbnStr = fmt.Sprintf("%d", *hbn)
		}
		an, ah, aok, _ := rawdb.ReadQMDBApplied(tx)
		appliedStr := "unset"
		if aok {
			appliedStr = fmt.Sprintf("%d/%x", an, ah[:8])
		}
		fmt.Printf("== %s  head=%s headBlockHash=%s/%x qmdbApplied=%s\n", dir, headStr, hbnStr, hbh[:8], appliedStr)
		for n := *from; n <= *to; n++ {
			ch, _ := rawdb.ReadCanonicalHash(tx, n)
			hdr := rawdb.ReadHeader(tx, ch, n)
			ceStr := "-"
			if ce, cerr := rawdb.ReadConsensusEvidence(tx, n); cerr == nil && ce != nil {
				br := ce.BeaconRoot()
				ceStr = fmt.Sprintf("%x", br[:6])
			}
			if hdr != nil {
				fmt.Printf("  %d canon=%x root=%x ce=%s\n", n, ch[:8], hdr.Root[:6], ceStr)
			} else {
				fmt.Printf("  %d canon=%x (no header) ce=%s\n", n, ch[:8], ceStr)
			}
		}
		// -walk: derive the TRUE chain by walking parent hashes down from
		// HeadBlockHash to -from, then print derived vs canonical-row hash for
		// the [from,to] window — shows exactly what the startup linkage repair
		// should rewrite and where it would stop.
		if *walk {
			hh := rawdb.ReadHeadBlockHash(tx)
			hn := rawdb.ReadHeaderNumber(tx, hh)
			if hn == nil {
				fmt.Printf("  walk: HeadBlockHash %x has no HeaderNumber row\n", hh[:8])
			} else {
				num, cur := *hn, hh
				derived := map[uint64][8]byte{}
				stop := ""
				for num >= *from {
					if num <= *to {
						var h8 [8]byte
						copy(h8[:], cur[:8])
						derived[num] = h8
					}
					hdr := rawdb.ReadHeader(tx, cur, num)
					if hdr == nil {
						stop = fmt.Sprintf("HEADER MISSING at %d %x", num, cur[:8])
						break
					}
					if len(hdr.Extra) < 4 || string(hdr.Extra[:4]) != "N42H" {
						stop = fmt.Sprintf("replay boundary (no N42H) at %d", num)
						break
					}
					cur = hdr.ParentHash
					num--
				}
				fmt.Printf("  walk: head=%d %x stop=%q\n", *hn, hh[:8], stop)
				for n := *from; n <= *to; n++ {
					ch, _ := rawdb.ReadCanonicalHash(tx, n)
					d, ok := derived[n]
					dStr := "unreached"
					if ok {
						dStr = fmt.Sprintf("%x", d[:])
					}
					match := "MISMATCH"
					if ok && fmt.Sprintf("%x", d[:]) == fmt.Sprintf("%x", ch[:8]) {
						match = "ok"
					}
					fmt.Printf("  %d derived=%s canon=%x %s\n", n, dStr, ch[:8], match)
				}
			}
		}
		// -qmdbroot: rebuild the forest twice from this store and compare the
		// roots against the applied marker's header root — separates a
		// deterministic-but-wrong reload from a nondeterministic one.
		if *qmdbRoot {
			an, ah, aok, _ := rawdb.ReadQMDBApplied(tx)
			if !aok {
				// Replay-produced stores carry no marker; fall back to the
				// stored head so pure-replay stores can still be probed.
				ah = rawdb.ReadHeadBlockHash(tx)
				if n := rawdb.ReadHeaderNumber(tx, ah); n != nil {
					an, aok = *n, true
				}
			}
			if !aok {
				fmt.Println("  qmdbroot: no applied marker and no head")
			} else {
				hdr := rawdb.ReadHeader(tx, ah, an)
				want := "?"
				if hdr != nil {
					want = fmt.Sprintf("%x", hdr.Root[:8])
				}
				for pass := 1; pass <= 2; pass++ {
					rc := commitment.NewQMDBRootComputer()
					rc.SetCold(tx)
					if err := rc.LoadFrom(tx); err != nil {
						fmt.Printf("  qmdbroot pass%d: LoadFrom error: %v\n", pass, err)
						continue
					}
					got := rc.Root()
					rc.SetCold(nil)
					fmt.Printf("  qmdbroot pass%d: tree=%x markerHeader=%s applied=%d next=%d\n",
						pass, got[:8], want, an, rc.Tree().NextSlot())
				}
			}
		}
		tx.Rollback()
		db.Close()
	}
}

// revertLadder reproduces the live "unwound node diverges from the cluster"
// failure offline: rebuild the forest at the applied marker, then ApplyUndo
// one block at a time (newest→oldest, exactly the branch-switch loop), and
// after every step compare the in-memory tree root against the canonical
// header root at that height. Every header on the ladder was accepted under
// import-side root verification, so the header root is the ground truth for
// "a tree that never saw the reverted blocks". The first mismatching step is
// the smallest failing revert; its undo record is then dumped for shape
// analysis (kills, twig spread, boundary position).
func revertLadder(dir string, depth uint64) {
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Printf("open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Printf("begin: %v\n", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	an, ah, aok, _ := rawdb.ReadQMDBApplied(tx)
	if !aok {
		fmt.Println("no applied marker")
		return
	}
	hdr := rawdb.ReadHeader(tx, ah, an)
	if hdr == nil {
		fmt.Println("applied header missing")
		return
	}
	rc := commitment.NewQMDBRootComputer()
	rc.SetCold(tx)
	if err := rc.LoadFrom(tx); err != nil {
		fmt.Printf("LoadFrom: %v\n", err)
		return
	}
	got := rc.Root()
	fmt.Printf("marker=%d reload=%x headerRoot=%x match=%v\n", an, got[:8], hdr.Root[:8], types.Hash(got) == hdr.Root)
	if types.Hash(got) != hdr.Root {
		fmt.Println("baseline already diverged (this store's flushed state is not the marker state); pick another node")
		return
	}
	cur, curHash := an, ah
	for step := uint64(0); step < depth && cur > 0; step++ {
		undos, uerr := rawdb.ReadQMDBUndos(tx, cur-1, cur)
		if uerr != nil || len(undos) != 1 {
			fmt.Printf("step %d: no undo row for %d (err=%v) — ladder ends\n", step, cur, uerr)
			return
		}
		undo := undos[0]
		h := rawdb.ReadHeader(tx, curHash, cur)
		if h == nil {
			fmt.Printf("step %d: header %d missing\n", step, cur)
			return
		}
		if aerr := rc.Tree().ApplyUndo(undo); aerr != nil {
			fmt.Printf("step %d: ApplyUndo(%d) failed: %v\n", step, cur, aerr)
			return
		}
		parent := rawdb.ReadHeader(tx, h.ParentHash, cur-1)
		if parent == nil {
			fmt.Printf("step %d: parent header %d missing\n", step, cur-1)
			return
		}
		r := rc.Root()
		match := types.Hash(r) == parent.Root
		fmt.Printf("revert %d: tree=%x parentHeader=%x kills=%d prevNext=%d %s\n",
			cur, r[:8], parent.Root[:8], len(undo.Entries), undo.PrevNextSlot,
			map[bool]string{true: "ok", false: "MISMATCH"}[match])
		if !match {
			fmt.Printf("  smallest failing revert found at block %d — undo shape: entries=%d prevNextSlot=%d\n",
				cur, len(undo.Entries), undo.PrevNextSlot)
			seen := map[uint64]int{}
			for i := range undo.Entries {
				seen[undo.Entries[i].Slot/qmdb.TwigSize]++
			}
			fmt.Printf("  revived-slot twig spread: %d twigs\n", len(seen))
			return
		}
		cur, curHash = cur-1, h.ParentHash
	}
	fmt.Printf("ladder clean for %d steps\n", depth)
}

// opsQMDB loads the forest twice from one store — instance A wired like the
// LIVE computer (SetCold before LoadFrom) and instance B like the miner's
// ISOLATED computer (LoadFrom then SetCold) — applies the identical op
// sequence to both (overwrite live keys, delete live keys, insert new keys),
// and compares roots after every phase. Divergence reproduces the live
// "sealed root not reproduced by the live tree" failure in isolation.
func opsQMDB(dir string) {
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Printf("open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Printf("begin: %v\n", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	a := commitment.NewQMDBRootComputer() // live-style wiring
	a.SetCold(tx)
	if err := a.LoadFrom(tx); err != nil {
		fmt.Printf("A LoadFrom: %v\n", err)
		return
	}
	b := commitment.NewQMDBRootComputer() // miner-style wiring
	if err := b.LoadFrom(tx); err != nil {
		fmt.Printf("B LoadFrom: %v\n", err)
		return
	}
	b.SetCold(tx)
	fmt.Printf("baseline: A=%x B=%x next A=%d B=%d\n", a.Root().Bytes()[:8], b.Root().Bytes()[:8], a.Tree().NextSlot(), b.Tree().NextSlot())

	// Harvest real live keyHashes from the tail of the entry log.
	var keys []qmdb.Hash
	next := a.Tree().NextSlot()
	lo := uint64(0)
	if next > 5000 {
		lo = next - 5000
	}
	for s := lo; s < next && len(keys) < 600; s++ {
		v, e := tx.GetOne(qmdb.EntryTable, be8p(s))
		if e != nil || len(v) < 32 {
			continue
		}
		var kh qmdb.Hash
		copy(kh[:], v[:32])
		if _, ok := a.Tree().Get(kh); ok {
			keys = append(keys, kh)
		}
	}
	fmt.Printf("harvested %d live keys\n", len(keys))

	apply := func(name string, f func(t *qmdb.Tree)) {
		f(a.Tree())
		f(b.Tree())
		ra, rb := a.Root(), b.Root()
		match := "ok"
		if ra != rb {
			match = "MISMATCH"
		}
		fmt.Printf("phase %-10s A=%x B=%x nextA=%d nextB=%d %s\n", name, ra[:8], rb[:8], a.Tree().NextSlot(), b.Tree().NextSlot(), match)
	}
	third := len(keys) / 3
	apply("overwrite", func(t *qmdb.Tree) {
		for i := 0; i < third; i++ {
			t.Set(keys[i], []byte{0xde, 0xad, byte(i), byte(i >> 8)})
		}
	})
	apply("delete", func(t *qmdb.Tree) {
		for i := third; i < 2*third; i++ {
			t.Delete(keys[i])
		}
	})
	apply("insert", func(t *qmdb.Tree) {
		for i := 0; i < 500; i++ {
			var kh qmdb.Hash
			kh[0], kh[1], kh[2] = 0xAB, byte(i), byte(i>>8)
			t.Set(kh, []byte{0xbe, 0xef, byte(i)})
		}
	})
}

func be8p(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

// diffQMDB compares the persisted QMDB positional layout of two stores. The
// layout is a deterministic function of the applied block sequence, so two
// nodes whose applied marker is the same block must match byte-for-byte; any
// differing twig/entry row localizes where an unwind repair diverged.
func diffQMDB(dirA, dirB string) {
	open := func(dir string) (kv.Tx, func()) {
		db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
			MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
		if err != nil {
			fmt.Printf("%s: open: %v\n", dir, err)
			os.Exit(1)
		}
		tx, err := db.BeginRo(context.Background())
		if err != nil {
			fmt.Printf("%s: begin: %v\n", dir, err)
			os.Exit(1)
		}
		return tx, func() { tx.Rollback(); db.Close() }
	}
	txA, closeA := open(dirA)
	defer closeA()
	txB, closeB := open(dirB)
	defer closeB()

	get := func(tx kv.Tx, table string, key []byte) []byte {
		v, _ := tx.GetOne(table, key)
		return v
	}
	be8 := func(v uint64) []byte {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], v)
		return b[:]
	}
	nsA := get(txA, qmdb.MetaTable, []byte("nextSlot"))
	nsB := get(txB, qmdb.MetaTable, []byte("nextSlot"))
	if len(nsA) < 8 || len(nsB) < 8 {
		fmt.Printf("nextSlot missing: A=%x B=%x\n", nsA, nsB)
		return
	}
	nextA := binary.BigEndian.Uint64(nsA)
	nextB := binary.BigEndian.Uint64(nsB)
	rootA := get(txA, qmdb.MetaTable, []byte("root"))
	rootB := get(txB, qmdb.MetaTable, []byte("root"))
	fmt.Printf("A %s nextSlot=%d metaRoot=%x\n", dirA, nextA, rootA[:8])
	fmt.Printf("B %s nextSlot=%d metaRoot=%x\n", dirB, nextB, rootB[:8])
	numTwigs := int((nextA + qmdb.TwigSize - 1) / qmdb.TwigSize)
	if nb := int((nextB + qmdb.TwigSize - 1) / qmdb.TwigSize); nb > numTwigs {
		numTwigs = nb
	}
	mismTwig, mismBlob := 0, 0
	const maxPrint = 12
	for id := 0; id < numTwigs; id++ {
		k := be8(uint64(id))
		ma, mb := get(txA, qmdb.TwigTable, k), get(txB, qmdb.TwigTable, k)
		if !bytes.Equal(ma, mb) {
			mismTwig++
			if mismTwig <= maxPrint {
				bitsDiff := 0
				if len(ma) >= 64+qmdb.TwigSize/8 && len(mb) >= 64+qmdb.TwigSize/8 {
					for i := 64; i < 64+qmdb.TwigSize/8; i++ {
						for x := ma[i] ^ mb[i]; x != 0; x &= x - 1 {
							bitsDiff++
						}
					}
				}
				desc := fmt.Sprintf("lenA=%d lenB=%d", len(ma), len(mb))
				if len(ma) >= 64 && len(mb) >= 64 {
					desc = fmt.Sprintf("rootA=%x rootB=%x leafRootEq=%v bitsDiff=%d",
						ma[:8], mb[:8], bytes.Equal(ma[32:64], mb[32:64]), bitsDiff)
				}
				fmt.Printf("  twig %d (slots %d..%d): %s\n", id, uint64(id)*qmdb.TwigSize, (uint64(id)+1)*qmdb.TwigSize-1, desc)
			}
		}
		la, lb := get(txA, qmdb.LeavesTable, k), get(txB, qmdb.LeavesTable, k)
		if !bytes.Equal(la, lb) {
			mismBlob++
			if mismBlob <= maxPrint {
				fmt.Printf("  leafblob %d differs: lenA=%d lenB=%d\n", id, len(la), len(lb))
			}
		}
	}
	fmt.Printf("twigs=%d twigMetaMismatch=%d leafBlobMismatch=%d\n", numTwigs, mismTwig, mismBlob)
	// Entry rows inside mismatching twigs would refine further, but the twig
	// meta (bits+roots) is what LoadFrom rebuilds the world root from.
}
