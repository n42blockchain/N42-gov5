package main

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

func TestRunAuthenticatesStoppedDatabaseBeforePublishing(t *testing.T) {
	previous := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	for _, defect := range []string{"healthy", "no-applied-marker", "chain-id", "entry-value", "omitted-plain-account"} {
		t.Run(defect, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "chaindata")
			db, err := mdbx.NewMDBX(log.New()).Path(dbPath).Label(kv.ChainDB).Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(t.Context(), func(tx kv.RwTx) error {
				addr := types.BytesToAddress([]byte{1})
				a := account.NewAccount()
				a.Balance.SetUint64(42)
				tree := qmdb.New()
				tree.Set(qmdb.Hash(commitment.AccountKeyHash(addr)), a.MarshalV2())
				tree.SetLeafStore(qmdb.LeafStoreFromGetter(tx))
				if _, _, err := tree.FlushTo(tx, 0); err != nil {
					return err
				}
				if defect != "omitted-plain-account" {
					if err := tx.Put(modules.Account, addr[:], a.MarshalV2()); err != nil {
						return err
					}
				}
				if defect == "entry-value" {
					var slot [8]byte
					encoded, err := tx.GetOne(qmdb.EntryTable, slot[:])
					if err != nil {
						return err
					}
					changed := append([]byte(nil), encoded...)
					changed[len(changed)-1] ^= 1
					if err := tx.Put(qmdb.EntryTable, slot[:], changed); err != nil {
						return err
					}
				}
				header := &block.Header{Number: uint256.NewInt(42), Difficulty: uint256.NewInt(1), Root: types.Hash(tree.Root())}
				head := header.Hash()
				rawdb.WriteHeader(tx, header)
				rawdb.WriteHeadBlockHash(tx, head)
				if err := rawdb.WriteCanonicalHash(tx, head, 42); err != nil {
					return err
				}
				genesis := types.HexToHash("0x01")
				if err := rawdb.WriteCanonicalHash(tx, genesis, 0); err != nil {
					return err
				}
				if err := rawdb.WriteChainConfig(tx, genesis, &params.ChainConfig{ChainID: big.NewInt(94)}); err != nil {
					return err
				}
				if defect != "no-applied-marker" {
					return rawdb.WriteQMDBApplied(tx, 42, [32]byte(head))
				}
				return nil
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "state.jsonl")
			headerOut, manifestOut := out+".header.rlp", out+".manifest.json"
			chainID := uint64(94)
			if defect == "chain-id" {
				chainID++
			}
			err = run(dbPath, out, headerOut, manifestOut, chainID, 0)
			healthy := defect == "healthy" || defect == "no-applied-marker"
			if (err == nil) != healthy {
				t.Fatalf("defect=%s err=%v", defect, err)
			}
			for _, path := range []string{out, headerOut, manifestOut} {
				_, statErr := os.Stat(path)
				if healthy && statErr != nil {
					t.Fatal(statErr)
				}
				if !healthy && !os.IsNotExist(statErr) {
					t.Fatalf("invalid snapshot published %s", path)
				}
			}
		})
	}
}
