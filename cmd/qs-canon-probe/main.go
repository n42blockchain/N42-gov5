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
	flag.Parse()
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
