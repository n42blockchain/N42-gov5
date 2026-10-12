// n42-consensus-anchor reads a stopped node's public recovery evidence.
// It never writes chain data or exports validator private keys.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"os"
)

func qc(value hotstuff.QuorumCertificate) map[string]any {
	return map[string]any{"view": value.View, "blockHash": value.BlockHash.Hex(), "signature": "0x" + hex.EncodeToString(value.AggregateSignature), "signers": value.Signers}
}

func run(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("--db must be a chaindata directory")
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.NewMDBX(log.New()).Path(path).Label(kv.ChainDB).Accede().Readonly().Open(context.Background())
	if err != nil {
		return err
	}
	defer db.Close()
	return db.View(context.Background(), func(tx kv.Tx) error {
		separate, err := hotstuff.HasIndependentVoteStore(tx)
		if err != nil {
			return err
		}
		if separate {
			return fmt.Errorf("independent vote store enabled: export requires its merged recovery evidence")
		}
		state, err := hotstuff.LoadConsensusState(tx)
		if err != nil {
			return err
		}
		if state == nil {
			return fmt.Errorf("no persisted H2 consensus state")
		}
		raw, err := tx.GetOne(modules.HotStuffState, []byte("state"))
		if err != nil {
			return err
		}
		head := rawdb.ReadHeadBlockHash(tx)
		number := rawdb.ReadHeaderNumber(tx, head)
		if number == nil {
			return fmt.Errorf("canonical head unavailable")
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *number)
		if err != nil {
			return err
		}
		if canonical != head {
			return fmt.Errorf("head is not canonical")
		}
		header := rawdb.ReadHeader(tx, head, *number)
		if header == nil || header.Hash() != head {
			return fmt.Errorf("head header is unavailable or mismatched")
		}
		genesis, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		cfg, err := rawdb.ReadChainConfig(tx, genesis)
		if err != nil {
			return err
		}
		if cfg == nil || cfg.ChainID == nil {
			return fmt.Errorf("chain config unavailable")
		}
		appliedNumber, appliedHash, applied, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if applied && (appliedNumber != *number || types.Hash(appliedHash) != head) {
			return fmt.Errorf("QMDB applied head is not canonical head")
		}
		report := map[string]any{"schema": 1, "chainId": cfg.ChainID.Uint64(), "genesisHash": genesis.Hex(), "number": *number, "blockHash": head.Hex(), "stateRoot": header.Root.Hex(), "view": state.View, "lastVotedView": state.LastVotedView, "lastCommitVotedView": state.LastCommitVotedView, "lockedQc": qc(state.LockedQC), "commitQc": qc(state.LastCommittedQC), "stateHex": hex.EncodeToString(raw), "commitMatchesHead": state.LastCommittedQC.BlockHash == head}
		return json.NewEncoder(os.Stdout).Encode(report)
	})
}

func main() {
	db := flag.String("db", "", "stopped node chaindata directory (read-only)")
	flag.Parse()
	if *db == "" {
		fmt.Fprintln(os.Stderr, "--db is required")
		os.Exit(2)
	}
	if err := run(*db); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
