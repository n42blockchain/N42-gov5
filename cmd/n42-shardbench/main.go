// n42-shardbench — standalone, READ-ONLY benchmark of the parallel nibble-shard
// state-root computation (FlatDBTrieLoader.CalcTrieRootShard + CombineNibbleSubtries)
// against the serial CalcTrieRoot, on a real committed trie. It opens an MDBX dir
// read-only, samples a dirty set (simulating a per-window RetainList), and times
// serial vs 16 concurrent shards (each on its OWN RoTx). It asserts the roots are
// byte-identical and reports the speedup. It writes NOTHING — safe to point at any
// idle trie DB; it never touches a running build's --out.
//
// This is the P4 decision-gate: does the proven-correct parallel shard actually
// run faster on mainnet-scale data, before investing in the in-build overlay
// refactor (per-worker RoTx + shared leaf/trie overlays)?
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "shardbench: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	dbPath := flag.String("db", `D:/N42-hashed`, "MDBX dir with HashedAccounts/TrieOfAccounts (opened READ-ONLY)")
	changes := flag.Int("changes", 50000, "sampled keys to retain (simulated per-window dirty set); 0 = full rebuild (retain all)")
	mapGB := flag.Int("map.gb", 2048, "MDBX map size GB")
	flag.Parse()

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()

	db, err := mdbxkv.NewMDBX(logger).Readonly().Path(*dbPath).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for n, it := range kv.ChaindataTablesCfg {
				d[n] = it
			}
			return d
		}).Open(ctx)
	if err != nil {
		die("open %s read-only: %v", *dbPath, err)
	}
	defer db.Close()

	// Sample a dirty set spread across the 16 top nibbles (each shard then has
	// real recompute work). changes==0 => full rebuild via retain-all.
	tx, err := db.BeginRo(ctx)
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()
	var sampled [][]byte
	if *changes > 0 {
		c, err := tx.Cursor(kv.HashedAccounts)
		if err != nil {
			die("cursor: %v", err)
		}
		perNib := *changes / 16
		if perNib < 1 {
			perNib = 1
		}
		const stride = 37
		for nib := 0; nib < 16; nib++ {
			k, _, err := c.Seek([]byte{byte(nib << 4)})
			if err != nil {
				die("seek nib %x: %v", nib, err)
			}
			cnt, step := 0, 0
			for k != nil && cnt < perNib {
				if k[0]>>4 != byte(nib) {
					break
				}
				if step%stride == 0 {
					sampled = append(sampled, append([]byte{}, k...))
					cnt++
				}
				step++
				k, _, err = c.Next()
				if err != nil {
					die("next: %v", err)
				}
			}
		}
		c.Close()
		fmt.Printf("sampled %d keys across 16 nibbles (perNib≈%d)\n", len(sampled), perNib)
	} else {
		fmt.Printf("full-rebuild mode (retain all)\n")
	}

	// Group the sampled keys by top nibble so each shard worker builds a
	// RetainList of ONLY its own nibble's keys — what a real per-worker build
	// does. (Giving every worker the full 50k list would charge 16× retain-build
	// to the parallel side and understate the speedup.)
	var byNib [16][][]byte
	for _, k := range sampled {
		byNib[k[0]>>4] = append(byNib[k[0]>>4], k)
	}
	mkRetainFrom := func(keys [][]byte) *trie.RetainList {
		if *changes == 0 {
			return trie.NewRetainList(65) // minLength>64 → every prefix retained → full rebuild
		}
		rl := trie.NewRetainList(0)
		for _, k := range keys {
			rl.AddKeyWithMarker(k, true)
		}
		return rl
	}

	serial := func(t kv.Tx) (types.Hash, time.Duration) {
		t0 := time.Now()
		l := trie.NewFlatDBTrieLoader("ser", mkRetainFrom(sampled), nil, nil, false)
		r, err := l.CalcTrieRoot(t, nil)
		if err != nil {
			die("serial CalcTrieRoot: %v", err)
		}
		return r, time.Since(t0)
	}

	parallel := func() (types.Hash, time.Duration) {
		t0 := time.Now()
		var subs [16][]byte
		var errs [16]error
		var wg sync.WaitGroup
		for nib := 0; nib < 16; nib++ {
			wg.Add(1)
			go func(nib int) {
				defer wg.Done()
				stx, e := db.BeginRo(ctx)
				if e != nil {
					errs[nib] = e
					return
				}
				defer stx.Rollback()
				l := trie.NewFlatDBTrieLoader("sh", mkRetainFrom(byNib[nib]), nil, nil, false)
				h, e := l.CalcTrieRootShard(stx, byte(nib), nil)
				if e != nil {
					errs[nib] = e
					return
				}
				if h != trie.EmptyRoot {
					hb := make([]byte, 32)
					copy(hb, h[:])
					subs[nib] = hb
				}
			}(nib)
		}
		wg.Wait()
		for nib, e := range errs {
			if e != nil {
				die("shard %x: %v", nib, e)
			}
		}
		root, err := trie.CombineNibbleSubtries(subs)
		if err != nil {
			die("combine: %v", err)
		}
		return root, time.Since(t0)
	}

	// Warm-up (page cache) so both timings are warm and comparable.
	fmt.Printf("warming up …\n")
	rWarm, _ := serial(tx)
	_, _ = parallel()

	rSer, dSer := serial(tx)
	rPar, dPar := parallel()

	fmt.Printf("\nserial   : %v  root=%s\n", dSer.Round(time.Millisecond), rSer.Hex())
	fmt.Printf("parallel : %v  root=%s  (16 shards, 16 RoTx)\n", dPar.Round(time.Millisecond), rPar.Hex())
	if rSer != rPar || rSer != rWarm {
		die("ROOT MISMATCH serial=%s parallel=%s warm=%s", rSer.Hex(), rPar.Hex(), rWarm.Hex())
	}
	sp := float64(dSer) / float64(dPar)
	fmt.Printf("MATCH ✓   speedup = %.2fx  (serial %v → parallel %v)\n",
		sp, dSer.Round(time.Millisecond), dPar.Round(time.Millisecond))
}
