// -qmdbfollow: live-fleet probe for the miner ReloadForBuild fallback rate.
//
// Full-loads the forest from a running validator's store (read snapshot),
// then repeatedly — as the LIVE chain advances — attempts the trusted-index
// fast reload against each new layout, exactly like the persistent miner
// computer does between leader turns. Reports, per round, whether the fast
// path held or the reconciliation baseline broke (the silent 5s full-rebuild
// fallback suspected behind the residual dropped-seal view timeouts).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func qmdbFollow(dir string) {
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Printf("open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	rc := commitment.NewQMDBRootComputer()
	var lastApplied uint64
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		an, _, _, _ := rawdb.ReadQMDBApplied(tx)
		lastApplied = an
		rc.SetCold(tx)
		t0 := time.Now()
		if lerr := rc.LoadFrom(tx); lerr != nil {
			return lerr
		}
		fmt.Printf("baseline: applied=%d full load %s fossils=%d\n",
			an, time.Since(t0), rc.Tree().LiveBits()-rc.Tree().LiveCount())
		return nil
	}); err != nil {
		fmt.Printf("baseline load: %v\n", err)
		os.Exit(1)
	}

	fast, slow := 0, 0
	for round := 1; round <= 10; round++ {
		// Wait for the live chain to move on (like waiting for our leader turn).
		for {
			var an uint64
			_ = db.View(context.Background(), func(tx kv.Tx) error {
				an, _, _, _ = rawdb.ReadQMDBApplied(tx)
				return nil
			})
			if an > lastApplied+4 {
				lastApplied = an
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err := db.View(context.Background(), func(tx kv.Tx) error {
			rc.SetCold(tx)
			prev := rc.Tree().NextSlot()
			t0 := time.Now()
			// Raw trusted reload first so the fallback is VISIBLE.
			terr := rc.Tree().LoadFromTrustedIndex(tx, prev, rc.Tree().LiveBits()-rc.Tree().LiveCount())
			if terr == nil {
				fast++
				fmt.Printf("round %d: applied=%d FAST %s\n", round, lastApplied, time.Since(t0))
				return nil
			}
			slow++
			fmt.Printf("round %d: applied=%d FALLBACK (%v) -> full rebuild ", round, lastApplied, terr)
			t1 := time.Now()
			lerr := rc.LoadFrom(tx)
			fmt.Printf("%s err=%v\n", time.Since(t1), lerr)
			return nil
		}); err != nil {
			fmt.Printf("round %d: %v\n", round, err)
		}
	}
	fmt.Printf("summary: fast=%d fallback=%d\n", fast, slow)
}
