// Command n42-qmdb-real loads a REAL converted chain's live PlainState (Account +
// Storage tables) into both the QMDB-twig store (lib/qmdb) and the JMT (lib/jmt),
// then reports the apples-to-apples persistent footprint, the parallel-vs-
// sequential twig-root speedup, and the compaction reclaim — on real key/value
// distributions rather than the synthetic workload of n42-qmdb-bench.
//
//	n42-qmdb-real --datadir D:/mainnet-bls --max-entries 2000000
//
// The footprint here is a SNAPSHOT of one live key set (no churn), so it answers
// "size vs JMT for the same live state". Compaction/churn reclaim stays in
// n42-qmdb-bench (it needs overwrite history, which a one-shot load lacks).
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
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

// countStore retains every JMT node so we can total the all-history node bytes
// (matches n42-qmdb-bench and docs/bench_state_report.md — no GC).
type countStore struct{ m map[jmt.Hash][]byte }

func newCountStore() *countStore { return &countStore{m: make(map[jmt.Hash][]byte)} }
func (c *countStore) Get(h jmt.Hash) ([]byte, error) {
	if d, ok := c.m[h]; ok {
		return d, nil
	}
	return nil, jmt.ErrNotFound
}
func (c *countStore) Put(h jmt.Hash, d []byte) error { c.m[h] = d; return nil }
func (c *countStore) Delete(jmt.Hash) error          { return nil }
func (c *countStore) Has(h jmt.Hash) (bool, error)   { _, ok := c.m[h]; return ok, nil }
func (c *countStore) bytes() int {
	n := 0
	for h, d := range c.m {
		n += len(h) + len(d)
	}
	return n
}

func mb(b int) float64 { return float64(b) / (1024 * 1024) }

func main() {
	datadir := flag.String("datadir", "D:/mainnet-bls", "converted chain datadir")
	mapGB := flag.Int("map.gb", 4096, "MDBX map GB")
	maxEntries := flag.Int("max-entries", 2000000, "cap total (account+storage) leaves loaded (0=unbounded)")
	withJMT := flag.Bool("jmt", true, "also build JMT for the footprint comparison (heavy RAM)")
	flag.Parse()

	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(filepath.Join(*datadir, "chaindata")).
		Label(kv.ChainDB).MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	q := qmdb.New()
	var jtree *jmt.Tree
	var jStore *countStore
	if *withJMT {
		jStore = newCountStore()
		jtree = jmt.New(jStore)
	}

	loaded := 0
	cap := *maxEntries
	t0 := time.Now()

	// Accounts.
	nAcc := 0
	ac, _ := tx.Cursor(modules.Account)
	for k, v, e := ac.First(); k != nil && e == nil; k, v, e = ac.Next() {
		if len(k) != 20 {
			continue
		}
		var a account.StateAccount
		if a.DecodeForStorage(v) != nil {
			continue
		}
		if a.CodeHash == (types.Hash{}) {
			a.CodeHash = emptyCodeHash
		}
		var addr types.Address
		copy(addr[:], k)
		kh := commitment.AccountKeyHash(addr)
		val := commitment.EncodeAccountValue(&a)
		q.Set(qmdb.Hash(kh), val)
		if *withJMT {
			_ = jtree.Put(jmt.Hash(kh), val)
		}
		nAcc++
		loaded++
		if cap > 0 && loaded >= cap {
			break
		}
	}
	ac.Close()

	// Storage.
	nSlot := 0
	if cap == 0 || loaded < cap {
		sc, _ := tx.Cursor(modules.Storage)
		for k, v, e := sc.First(); k != nil && e == nil; k, v, e = sc.Next() {
			if len(k) != 52 || len(v) == 0 {
				continue
			}
			var addr types.Address
			copy(addr[:], k[:20])
			var slot types.Hash
			copy(slot[:], k[20:52])
			val := new(uint256.Int).SetBytes(v)
			b := val.Bytes32()
			start := 0
			for start < 31 && b[start] == 0 {
				start++
			}
			kh := commitment.StorageKeyHash(addr, slot)
			q.Set(qmdb.Hash(kh), b[start:])
			if *withJMT {
				_ = jtree.Put(jmt.Hash(kh), b[start:])
			}
			nSlot++
			loaded++
			if cap > 0 && loaded >= cap {
				break
			}
		}
		sc.Close()
	}
	loadDur := time.Since(t0)

	// Roots.
	qmdb.ParallelRoot = true
	qRoot := q.Root()
	var jRoot jmt.Hash
	var jFlushBytes int
	if *withJMT {
		jtree.SetVersion(0)
		_, _ = jtree.BatchUpdate(nil)
		jRoot = jtree.Root()
		_ = jtree.Flush()
		jFlushBytes = jStore.bytes()
	}

	// Parallel-vs-sequential heavy recompute (all twigs dirty).
	qmdb.ParallelRoot = false
	q.ForceDirty()
	ts := time.Now()
	q.Root()
	seqDur := time.Since(ts)
	qmdb.ParallelRoot = true
	q.ForceDirty()
	tp := time.Now()
	q.Root()
	parDur := time.Since(tp)

	qEntryB, qTwigB := q.OnDiskBytes()
	st := q.Stats()

	fmt.Printf("=== n42-qmdb-real: %s (max-entries=%d) ===\n", *datadir, cap)
	fmt.Printf("loaded: accounts=%d storageSlots=%d total=%d  (%s)\n", nAcc, nSlot, loaded, loadDur.Round(time.Millisecond))
	fmt.Printf("QMDB worldRoot=%x  liveKeys=%d twigs=%d\n", qRoot[:8], q.LiveCount(), st.Twigs)
	fmt.Printf("\n-- persistent footprint (same live key set) --\n")
	fmt.Printf("  QMDB: entryLog=%.1f MB  twigMeta=%.1f MB  total=%.1f MB\n", mb(qEntryB), mb(qTwigB), mb(qEntryB+qTwigB))
	if *withJMT {
		fmt.Printf("  JMT:  nodes=%.1f MB  (count=%d)  root=%x\n", mb(jFlushBytes), len(jStore.m), jRoot[:8])
		if qEntryB+qTwigB > 0 {
			fmt.Printf("  ratio JMT/QMDB = %.2fx\n", float64(jFlushBytes)/float64(qEntryB+qTwigB))
		}
	}
	fmt.Printf("\n-- twig-root recompute (whole forest dirty) --\n")
	fmt.Printf("  sequential: %v   parallel: %v   speedup: %.1fx\n",
		seqDur.Round(time.Microsecond), parDur.Round(time.Microsecond),
		float64(seqDur)/float64(parDur))
}

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }
