package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/internal/replay"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func main() {
	dir := os.Args[1]
	var head uint64
	fmt.Sscanf(os.Args[2], "%d", &head)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(512 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		panic(err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		panic(err)
	}
	defer tx.Rollback()

	present, missing, undec, withMobile := 0, 0, 0, 0
	var firstMissing uint64 = ^uint64(0)
	var minSigners, maxSigners uint16 = ^uint16(0), 0
	var minMob, maxMob uint16 = ^uint16(0), 0
	for n := uint64(1); n <= head; n++ {
		ce, err := rawdb.ReadConsensusEvidence(tx, n)
		if err != nil {
			undec++
			if undec <= 3 {
				fmt.Printf("  UNDECODABLE at %d: %v\n", n, err)
			}
			continue
		}
		if ce == nil {
			missing++
			if firstMissing == ^uint64(0) {
				firstMissing = n
			}
			continue
		}
		present++
		if ce.SignerCount < minSigners {
			minSigners = ce.SignerCount
		}
		if ce.SignerCount > maxSigners {
			maxSigners = ce.SignerCount
		}
		if ce.HasMobile {
			withMobile++
			if ce.MobParticipantCount < minMob {
				minMob = ce.MobParticipantCount
			}
			if ce.MobParticipantCount > maxMob {
				maxMob = ce.MobParticipantCount
			}
		}
	}
	fmt.Printf("presence [1..%d]: present=%d missing=%d undecodable=%d firstMissing=%v\n", head, present, missing, undec, firstMissing)
	fmt.Printf("committee signers min=%d max=%d | mobile-layer blocks=%d participants min=%d max=%d\n", minSigners, maxSigners, withMobile, minMob, maxMob)

	seedHex := "03c75de6b57f3563919956d11700f1d0c932e3c157506b23ed2c40d3ca47bb2f"
	var seed [32]byte
	sb, _ := hex.DecodeString(seedHex)
	copy(seed[:], sb)
	r, err := replay.NewBLSResealer(replay.BLSResealConfig{Seed: seed, PoolSize: 200000, CommitteeSize: 512, RampBlocks: 1000000})
	if err != nil {
		fmt.Printf("resealer: %v\n", err)
		return
	}
	okN, failN := 0, 0
	for n := uint64(1); n <= head; n += 1000000 {
		ce, err := rawdb.ReadConsensusEvidence(tx, n)
		if err != nil || ce == nil {
			continue
		}
		ok, verr := r.VerifyCE(ce)
		if ok {
			okN++
		} else {
			failN++
			fmt.Printf("  VERIFY FAIL at %d: %v\n", n, verr)
		}
	}
	fmt.Printf("BLS committee QC verify (sampled every 1M): ok=%d fail=%d\n", okN, failN)
}
