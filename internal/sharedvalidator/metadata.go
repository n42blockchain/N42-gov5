package sharedvalidator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// IsMetadataStore lets the normal full-execution startup refuse validator-only
// databases before constructing a BlockChain. Presence also fails closed for
// malformed marker contents; InitializeMetadata verifies the actual binding.
func IsMetadataStore(tx kv.Tx) (bool, error) {
	raw, err := tx.GetOne(modules.HotStuffState, metadataKey)
	return len(raw) > 0, err
}

// InitializeMetadata writes only genesis availability/config and the immutable
// validator-store binding. It never writes alloc accounts or execution state.
func InitializeMetadata(ctx context.Context, db kv.RwDB, cfg *params.ChainConfig, genesis *block.Block) error {
	if ctx == nil || db == nil || cfg == nil || cfg.Performance == nil || cfg.HotStuff == nil || len(cfg.HotStuff.Validators) != 7 || genesis == nil {
		return errors.New("metadata init requires explicit performance genesis and seven validators")
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	if err := cfg.ValidatePerformanceSelection("private", profile); err != nil {
		return err
	}
	if cfg.DeferredExecutionTime != nil || cfg.HotStuff.InteropV4 || (cfg.HotStuff.CommitteePool != nil && cfg.HotStuff.CommitteePool.Enabled) {
		return errors.New("shared validator deferred/domain adapter is not wired")
	}
	if genesis.Body() == nil || genesis.Number64() == nil || !genesis.Number64().IsZero() || genesis.Time() != params.PerformanceGenesisTime || !bytes.Equal(genesis.Header().(*block.Header).Extra, []byte(params.PerformanceGenesisMarker)) {
		return errors.New("shared validator requires actual dedicated performance genesis metadata")
	}
	identity := sharedexec.Identity{Version: 1, Namespace: cfg.Performance.NetworkNamespace, ChainID: cfg.ChainID.String(), GenesisHash: genesis.Hash()}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return db.Update(ctx, func(tx kv.RwTx) error {
		cursor, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer cursor.Close()
		account, _, err := cursor.First()
		if err != nil {
			return err
		}
		if account != nil {
			return errors.New("shared validator metadata store contains local account execution state")
		}
		existing, err := tx.GetOne(modules.HotStuffState, metadataKey)
		if err != nil {
			return err
		}
		binding, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		if len(existing) > 0 && !bytes.Equal(existing, binding) {
			return errors.New("shared validator metadata identity changed")
		}
		stored, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if stored != (types.Hash{}) {
			if stored != identity.GenesisHash {
				return errors.New("shared validator stored genesis mismatch")
			}
			storedCfg, err := rawdb.ReadChainConfig(tx, stored)
			if err != nil {
				return err
			}
			if storedCfg == nil {
				return errors.New("shared validator stored chain config missing")
			}
			storedRaw, err := json.Marshal(storedCfg)
			if err != nil {
				return err
			}
			if !bytes.Equal(storedRaw, encoded) {
				return errors.New("shared validator stored chain config changed")
			}
			if len(existing) == 0 {
				return errors.New("existing store is not explicitly bound as validator metadata")
			}
			head := rawdb.ReadHeadBlockHash(tx)
			number := rawdb.ReadHeaderNumber(tx, head)
			if number == nil || !rawdb.HasBlock(tx, head, *number) {
				return errors.New("shared validator canonical availability missing")
			}
			canonical, err := rawdb.ReadCanonicalHash(tx, *number)
			if err != nil {
				return err
			}
			if canonical != head {
				return errors.New("shared validator canonical metadata inconsistent")
			}
		} else {
			if err := rawdb.WriteBlock(tx, genesis); err != nil {
				return err
			}
			if err := rawdb.WriteCanonicalHash(tx, genesis.Hash(), 0); err != nil {
				return err
			}
			rawdb.WriteHeadBlockHash(tx, genesis.Hash())
			if err := rawdb.WriteHeadHeaderHash(tx, genesis.Hash()); err != nil {
				return err
			}
			if err := rawdb.WriteChainConfig(tx, genesis.Hash(), cfg); err != nil {
				return err
			}
		}
		return tx.Put(modules.HotStuffState, metadataKey, binding)
	})
}
