// Command n42-consensus-rest serves the Beacon-API-style consensus REST surface
// (internal/api/consensusrest) over a read-only N42 chain DB. Run it next to a
// chain datadir and point block explorers / beaconcha.in-style tooling at it.
//
//	n42-consensus-rest --datadir D:\mainnet-bls --addr :8555 --seed 0x<master-seed>
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/api/consensusrest"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	datadir := flag.String("datadir", "", "chain datadir (must contain chaindata/)")
	addr := flag.String("addr", ":8555", "HTTP listen address")
	seedHex := flag.String("seed", os.Getenv("N42_BLS_POOL_SEED"), "32-byte hex master seed (optional; enables pubkey routes)")
	poolSize := flag.Int("pool-size", 200000, "total mobile-voter pool size")
	committee := flag.Int("committee", 512, "per-block committee size")
	rampBlocks := flag.Uint64("ramp-blocks", 1000000, "blocks over which the pool ramps to full size")
	mapGB := flag.Int("map.gb", 4096, "MDBX map size (GB)")
	flag.Parse()

	if *datadir == "" {
		fmt.Fprintln(os.Stderr, "error: --datadir is required")
		os.Exit(1)
	}

	logger := log.New()
	// Register the standard N42 chaindata tables (incl. ConsensusEvidence) so a
	// bare Accede open can read them.
	kv.ChaindataTablesCfg = modules.N42TableCfg

	db, err := mdbxkv.NewMDBX(logger).
		Path(filepath.Join(*datadir, "chaindata")).
		Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		Accede().
		Readonly().
		Open(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open chaindata: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	cfg := consensusrest.Config{PoolSize: *poolSize, Committee: *committee, RampBlocks: *rampBlocks}
	if sh := strings.TrimPrefix(*seedHex, "0x"); sh != "" {
		b, derr := hex.DecodeString(sh)
		if derr != nil || len(b) != 32 {
			fmt.Fprintln(os.Stderr, "error: --seed must be 32-byte hex")
			os.Exit(1)
		}
		copy(cfg.Seed[:], b)
		cfg.HasSeed = true
	}

	srv := consensusrest.NewServer(db, cfg)
	logger.Info("consensus REST serving",
		"addr", *addr, "datadir", *datadir, "committee", *committee,
		"pool", *poolSize, "pubkeyRoutes", cfg.HasSeed)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}
