// Package sharedvalidator keeps availability, consensus WAL and authenticated
// remote execution evidence without constructing a local execution BlockChain.
package sharedvalidator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

var metadataKey = []byte("performance-validator-metadata-v1")
var executionPrefix = []byte("performance-remote-executed/")

type Replica struct {
	ctx      context.Context
	db       kv.RwDB
	cfg      *params.ChainConfig
	genesis  *block.Block
	identity sharedexec.Identity
	engine   *hotstuff.HotStuff
	client   *sharedexec.Client
	bridge   *sharedexec.CertificateBridge
	mu       sync.Mutex
	service  *hotstuff.Service
}

// New initializes/binds a metadata-only store. The caller owns database,
// engine and client lifecycle. It never loads execution state or signing keys.
func New(ctx context.Context, db kv.RwDB, cfg *params.ChainConfig, genesis *block.Block, engine *hotstuff.HotStuff, client *sharedexec.Client) (*Replica, error) {
	if ctx == nil || db == nil || cfg == nil || cfg.Performance == nil || cfg.HotStuff == nil || len(cfg.HotStuff.Validators) != 7 || genesis == nil || engine == nil || engine.Engine() == nil || client == nil {
		return nil, errors.New("shared validator requires explicit performance genesis, seven validators and initialized consensus/client")
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	if err := cfg.ValidatePerformanceSelection("private", profile); err != nil {
		return nil, err
	}
	if cfg.DeferredExecutionTime != nil || cfg.HotStuff.InteropV4 || (cfg.HotStuff.CommitteePool != nil && cfg.HotStuff.CommitteePool.Enabled) {
		return nil, errors.New("shared validator deferred/domain adapter is not wired")
	}
	if genesis.Body() == nil || genesis.Number64() == nil || !genesis.Number64().IsZero() || genesis.Time() != params.PerformanceGenesisTime || !bytes.Equal(genesis.Header().(*block.Header).Extra, []byte(params.PerformanceGenesisMarker)) {
		return nil, errors.New("shared validator requires actual dedicated performance genesis metadata")
	}
	identity := sharedexec.Identity{Version: 1, Namespace: cfg.Performance.NetworkNamespace, ChainID: cfg.ChainID.String(), GenesisHash: genesis.Hash()}
	if identity != client.Identity() {
		return nil, errors.New("shared validator execution identity mismatch")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var bound params.ChainConfig
	if err := json.Unmarshal(encoded, &bound); err != nil {
		return nil, err
	}
	r := &Replica{ctx: ctx, db: db, cfg: &bound, genesis: genesis, identity: identity, engine: engine, client: client}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
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
	}); err != nil {
		return nil, err
	}
	r.bridge, err = sharedexec.NewCertificateBridge(client, db)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func evidenceKey(hash types.Hash) []byte {
	key := append([]byte(nil), executionPrefix...)
	return append(key, hash[:]...)
}

// AttachService installs the actual import notification and public certificate
// sink. Availability and execution gates remain backed by this metadata store.
func (r *Replica) AttachService(service *hotstuff.Service, fetcher hotstuff.BlockFetcher) error {
	if service == nil || fetcher == nil {
		return errors.New("shared validator consensus service required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.service != nil {
		return errors.New("shared validator service already attached")
	}
	if err := service.SetCommitCertificateSink(r); err != nil {
		return err
	}
	service.SetBlockFetcher(executionGateFetcher{BlockFetcher: fetcher, replica: r})
	r.service = service
	return nil
}

type executionGateFetcher struct {
	hotstuff.BlockFetcher
	replica *Replica
}

func (f executionGateFetcher) BlockApplied(hash types.Hash, number uint64) bool {
	return f.replica.BlockApplied(hash, number)
}

// Import persists availability first. A remote accepted/missing-parent response
// is not execution evidence and never notifies import-gated voting.
func (r *Replica) Import(ctx context.Context, raw []byte) (sharedexec.ImportResult, error) {
	if len(raw) == 0 || len(raw) > sharedexec.MaxBlockBytes {
		return sharedexec.ImportResult{}, errors.New("shared validator block size invalid")
	}
	var b block.Block
	if err := b.Unmarshal(raw); err != nil {
		return sharedexec.ImportResult{}, err
	}
	header, ok := b.Header().(*block.Header)
	body, bodyOK := b.Body().(*block.Body)
	if !ok || !bodyOK || header == nil || header.Number == nil || !header.Number.IsUint64() || body.IsFrameBody() || len(b.Transactions()) > sharedexec.MaxTransactions || b.TxHash() != block.BodyTxRoot(body, header.Time) {
		return sharedexec.ImportResult{}, errors.New("shared validator availability body mismatch")
	}
	r.mu.Lock()
	result, err := r.importLocked(ctx, &b, raw)
	service := r.service
	r.mu.Unlock()
	// Service may replay pending commits through r.CommitCertificate. Never
	// invoke it while holding r.mu (certificateMu -> r.mu is the sink order).
	if err == nil && result.Durable && service != nil {
		service.NotifyBlockImported(b.Hash(), b.TxHash())
	}
	return result, err
}

func (r *Replica) importLocked(ctx context.Context, b *block.Block, raw []byte) (sharedexec.ImportResult, error) {
	header := b.Header().(*block.Header)
	if err := r.engine.VerifyPerformanceSealedHeader(r, header); err != nil {
		return sharedexec.ImportResult{}, err
	}
	if err := r.db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteBlock(tx, b) }); err != nil {
		return sharedexec.ImportResult{}, err
	}
	result, err := r.client.Import(ctx, sharedexec.ImportRequest{Identity: r.identity, BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !result.Durable {
		return result, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return sharedexec.ImportResult{}, err
	}
	if err := r.db.Update(ctx, func(tx kv.RwTx) error { return tx.Put(modules.HotStuffState, evidenceKey(b.Hash()), encoded) }); err != nil {
		return sharedexec.ImportResult{}, err
	}
	return result, nil
}

func (r *Replica) BlockApplied(hash types.Hash, number uint64) bool {
	if number == 0 {
		return hash == r.identity.GenesisHash
	}
	valid := false
	_ = r.db.View(r.ctx, func(tx kv.Tx) error {
		raw, err := tx.GetOne(modules.HotStuffState, evidenceKey(hash))
		if err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > 16384 {
			return nil
		}
		var evidence sharedexec.ImportResult
		if err := json.Unmarshal(raw, &evidence); err != nil {
			return err
		}
		header := rawdb.ReadHeader(tx, hash, number)
		valid = header != nil && rawdb.HasBlock(tx, hash, number) && evidence.Identity == r.identity && evidence.BlockHash == hash && evidence.Number == number && evidence.ParentHash == header.ParentHash && evidence.Durable && evidence.Checked && evidence.Executed && evidence.State == "durable" && evidence.Result.Root == header.Root && evidence.Result.ReceiptHash == header.ReceiptHash && evidence.Result.Bloom == header.Bloom && evidence.Result.GasUsed == header.GasUsed
		return nil
	})
	return valid
}

// CommitCertificate advances local metadata only after full local availability,
// authenticated remote execution evidence and the public remote canonical ACK.
// A lost local transaction leaves the Service outbox pending for idempotent retry.
func (r *Replica) CommitCertificate(ctx context.Context, qc *hotstuff.QuorumCertificate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if qc == nil {
		return errors.New("shared validator commit certificate missing")
	}
	ih, err := r.GetHeaderByHash(qc.BlockHash)
	if err != nil {
		return err
	}
	header, ok := ih.(*block.Header)
	if !ok || header == nil {
		return errors.New("shared validator committed availability missing")
	}
	if err := r.engine.VerifyPerformanceCommitCertificate(r, header, qc); err != nil {
		return err
	}
	if !r.BlockApplied(qc.BlockHash, header.Number.Uint64()) {
		return errors.New("shared validator remote execution evidence missing")
	}
	if err := r.bridge.CommitCertificate(ctx, qc); err != nil {
		return err
	}
	return r.db.Update(ctx, func(tx kv.RwTx) error {
		head := rawdb.ReadHeadBlockHash(tx)
		height := rawdb.ReadHeaderNumber(tx, head)
		if height == nil {
			return errors.New("shared validator canonical head missing")
		}
		if header.Number.Uint64() <= *height {
			canonical, err := rawdb.ReadCanonicalHash(tx, header.Number.Uint64())
			if err != nil {
				return err
			}
			if canonical != qc.BlockHash {
				return errors.New("shared validator finalized metadata conflict")
			}
			return nil
		}
		if header.Number.Uint64() != *height+1 || header.ParentHash != head {
			return errors.New("shared validator canonical parent must commit first")
		}
		if err := rawdb.WriteCanonicalHash(tx, qc.BlockHash, header.Number.Uint64()); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, qc.BlockHash)
		if err := rawdb.WriteHeadHeaderHash(tx, qc.BlockHash); err != nil {
			return err
		}
		return rawdb.WriteHotStuffCommittedHead(tx, qc.BlockHash)
	})
}
