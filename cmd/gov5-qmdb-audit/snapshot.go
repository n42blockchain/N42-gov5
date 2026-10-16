package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type auditedGenesis struct {
	Genesis conf.Genesis
	Anchor  chainAnchor
	SHA256  string
	Tx      kv.Tx
	db      kv.RwDB
	dir     string
}

func (g *auditedGenesis) close() {
	if g.Tx != nil {
		g.Tx.Rollback()
	}
	if g.db != nil {
		g.db.Close()
	}
	if g.dir != "" {
		os.RemoveAll(g.dir)
	}
}

func prepareAuditGenesis(ctx context.Context, genesisFile, expectedGenesis string) (*auditedGenesis, error) {
	gf, err := os.OpenFile(genesisFile, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := gf.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		gf.Close()
		return nil, errors.New("bounded regular genesis required")
	}
	raw, err := io.ReadAll(io.LimitReader(gf, (16<<20)+1))
	gf.Close()
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != expectedGenesis {
		return nil, errors.New("genesis bytes differ")
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		return nil, err
	}
	if genesis.Config == nil || genesis.Config.StateScheme != "qmdb" || len(genesis.Alloc) == 0 || len(genesis.Alloc) > 12500 || genesis.StateRoot != (types.Hash{}) {
		return nil, errors.New("bounded computed QMDB performance genesis required")
	}
	cfg := &conf.Config{NodeCfg: conf.NodeConfig{Profile: "n42", Chain: "private", PerformanceGenesis: genesisFile}, ChainCfg: genesis.Config}
	if err := node.ValidatePerformanceGenesis(cfg, &genesis); err != nil {
		return nil, err
	}
	modules.N42Init()
	scratchDir, err := os.MkdirTemp("", "gov5-genesis-audit-")
	if err != nil {
		return nil, err
	}
	g := &auditedGenesis{Genesis: genesis, SHA256: expectedGenesis, dir: scratchDir}
	complete := false
	defer func() {
		if !complete {
			g.close()
		}
	}()
	scratch, err := mdbx.NewMDBX(log.New()).Path(scratchDir).Label(kv.ChainDB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).MapSize(256 * datasize.MB).GrowthStep(4 * datasize.MB).Open(ctx)
	if err != nil {
		return nil, err
	}
	g.db = scratch
	var genesisAnchor chainAnchor
	if err := scratch.Update(ctx, func(tx kv.RwTx) error {
		b, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).WriteGenesisState(tx)
		if err == nil {
			genesisAnchor = chainAnchor{0, b.Hash(), b.StateRoot()}
		}
		return err
	}); err != nil {
		return nil, err
	}
	gtx, err := scratch.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	g.Tx = gtx
	// Independently reject a genesis whose plain storage was not represented
	// by its commitment, rather than silently inheriting a genesis reader gap.
	if _, err := auditForest(gtx, gtx, genesisAnchor.Root); err != nil {
		return nil, fmt.Errorf("scratch genesis: %w", err)
	}
	g.Anchor = genesisAnchor
	complete = true
	return g, nil
}

func auditBoundSnapshot(tx kv.Tx, g *auditedGenesis) (auditReport, error) {
	report := auditReport{Kind: "gov5-qmdb-missing-genesis-leaves-readonly-audit-v2", GenesisSHA256: g.SHA256, Genesis: g.Anchor}
	genesis, genesisAnchor, gtx := &g.Genesis, g.Anchor, g.Tx
	err := func() error {
		stored, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if stored != genesisAnchor.Hash {
			return errors.New("copy genesis hash differs")
		}
		storedCfg, err := rawdb.ReadChainConfig(tx, stored)
		if err != nil {
			return err
		}
		a, err := json.Marshal(storedCfg)
		if err != nil {
			return err
		}
		b, err := json.Marshal(genesis.Config)
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			return errors.New("stored chain configuration differs")
		}
		n, h, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("missing applied marker")
		}
		applied, err := checkedHeader(tx, types.Hash(h), n)
		if err != nil {
			return err
		}
		ch := rawdb.ReadHeadBlockHash(tx)
		cn := rawdb.ReadHeaderNumber(tx, ch)
		if cn == nil || *cn > n || n-*cn > 256 {
			return errors.New("canonical/applied ancestry outside bounded audit")
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *cn)
		if err != nil {
			return err
		}
		if canonical != ch {
			return errors.New("head is not canonical")
		}
		header, err := checkedHeader(tx, ch, *cn)
		if err != nil {
			return err
		}
		ancestor := applied
		for height := n; height > *cn; height-- {
			ancestor, err = checkedHeader(tx, ancestor.ParentHash, height-1)
			if err != nil {
				return err
			}
		}
		if ancestor.Hash() != ch {
			return errors.New("applied is not a canonical extension")
		}
		report.Canonical = chainAnchor{*cn, ch, header.Root}
		report.Applied = chainAnchor{n, types.Hash(h), applied.Root}
		if !genesis.Config.IsSpuriousDragon(0) || genesis.Config.HotStuff == nil {
			return errors.New("empty-beneficiary policy requires EIP-161 from genesis")
		}
		validators := make(map[types.Address]bool)
		for _, validator := range genesis.Config.HotStuff.Validators {
			validators[types.HexToAddress(validator.Address)] = true
		}
		beneficiaries, err := usedBeneficiaries(tx, applied, genesisAnchor.Hash, validators)
		if err != nil {
			return err
		}
		report.Forest, err = auditForestWithBeneficiaries(tx, gtx, applied.Root, beneficiaries)
		return err
	}()
	return report, err
}
