package main

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestReportPreservesChainIDAndRejectsInconsistentEvidence(t *testing.T) {
	previous := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	for _, defect := range []string{"healthy", "header-number", "applied-head", "separate-store", "corrupt-state"} {
		t.Run(defect, func(t *testing.T) {
			db := memdb.NewTestDB(t)
			tx, err := db.BeginRw(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			header := &block.Header{Number: uint256.NewInt(42), Difficulty: uint256.NewInt(1)}
			head := header.Hash()
			rawdb.WriteHeader(tx, header)
			rawdb.WriteHeadBlockHash(tx, head)
			if err := rawdb.WriteCanonicalHash(tx, head, 42); err != nil {
				t.Fatal(err)
			}
			genesis := types.HexToHash("0x01")
			if err := rawdb.WriteCanonicalHash(tx, genesis, 0); err != nil {
				t.Fatal(err)
			}
			chainID := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 80), big.NewInt(94))
			if err := rawdb.WriteChainConfig(tx, genesis, &params.ChainConfig{ChainID: chainID}); err != nil {
				t.Fatal(err)
			}
			if err := hotstuff.SaveConsensusState(tx, &hotstuff.ConsensusState{View: 7}); err != nil {
				t.Fatal(err)
			}
			switch defect {
			case "header-number":
				// Preserve the header hash, but store its RLP under a false height.
				raw, err := tx.GetOne(modules.Headers, rawdb.HeaderKey(42, head))
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Put(modules.Headers, rawdb.HeaderKey(43, head), raw); err != nil {
					t.Fatal(err)
				}
				if err := rawdb.WriteHeaderNumber(tx, head, 43); err != nil {
					t.Fatal(err)
				}
				if err := rawdb.WriteCanonicalHash(tx, head, 43); err != nil {
					t.Fatal(err)
				}
			case "applied-head":
				if err := rawdb.WriteQMDBApplied(tx, 41, [32]byte(head)); err != nil {
					t.Fatal(err)
				}
			case "separate-store":
				if err := tx.Put(modules.HotStuffState, []byte("separate_vote_store_v1"), []byte("binding")); err != nil {
					t.Fatal(err)
				}
			case "corrupt-state":
				if err := tx.Put(modules.HotStuffState, []byte("state"), []byte{1}); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err = writeReport(tx, &out)
			if defect != "healthy" {
				if err == nil || out.Len() != 0 {
					t.Fatalf("inconsistent evidence exported: err=%v output=%s", err, out.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var report map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if string(report["chainId"]) != chainID.String() {
				t.Fatalf("chain ID truncated: %s", report["chainId"])
			}
		})
	}
}
