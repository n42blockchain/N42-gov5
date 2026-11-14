// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Consensus engine resolution, signer plans, cohort auth and committee-pool wiring.

package node

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apoa"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/mobileverify"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type consensusRuntimePlan struct {
	bindAPOSBlockChain       bool
	startHotStuffService     bool
	registerHotStuffAdminAPI bool
}

type consensusSignerMode string

const (
	consensusSignerModeNone        consensusSignerMode = "none"
	consensusSignerModeWallet      consensusSignerMode = "wallet"
	consensusSignerModeHotStuffBLS consensusSignerMode = "hotstuff-bls"
)

type consensusSignerPlan struct {
	mode consensusSignerMode
}

func resolveHotStuffEngine(engine consensus.Engine) (*hotstuff.HotStuff, bool) {
	hs, ok := engine.(*hotstuff.HotStuff)
	return hs, ok
}

func resolveBridgeValidatorSet(engine consensus.Engine) *hotstuff.ValidatorSet {
	hs, ok := resolveHotStuffEngine(engine)
	if !ok || hs.Engine() == nil {
		return nil
	}
	return hs.Engine().CurrentValidatorSet()
}

// buildCohortAuth returns the reporter-authentication hooks for the cohort
// relay: a signer using this node's validator BLS key and a verifier that
// accepts an announcement only if its reporter is a current validator and the
// signature verifies against that validator's registered public key. On a
// non-HotStuff engine it returns nil,nil,nil — the relay runs unauthenticated,
// which is correct only for single-node / legacy chains that do not gossip
// cohort announcements. On a HotStuff engine (a real gossiping fleet) a missing
// etherbase or BLS key returns a non-nil error instead of silently disabling
// authentication: running a cohort participant with verify==nil is fail-OPEN
// and re-opens the reporter-spoofing/censorship vector Layer 1 closes, so the
// caller fails closed (fatal on a mining node). The validator set is read fresh
// on every verification so epoch rotations are honoured.
func (n *Node) buildCohortAuth() (mobileverify.CohortSigner, mobileverify.CohortVerifier, error) {
	if _, ok := resolveHotStuffEngine(n.engine); !ok {
		return nil, nil, nil
	}
	etherbase, err := n.Etherbase()
	if err != nil {
		return nil, nil, fmt.Errorf("cohort auth: etherbase unavailable: %w", err)
	}
	blsKey, err := hotstuff.LoadBLSKeyFromDir(n.keyDir, etherbase)
	if err != nil {
		return nil, nil, fmt.Errorf("cohort auth: BLS key unavailable for %s: %w", etherbase, err)
	}
	signer := func(msg []byte) []byte { return blsKey.Sign(msg).Marshal() }
	verifier := func(reporter types.Address, msg, sig []byte) bool {
		vs := resolveBridgeValidatorSet(n.engine)
		if vs == nil {
			return false
		}
		idx := vs.FindByAddress(reporter)
		if idx < 0 {
			return false // reporter is not a current validator
		}
		pk, perr := vs.GetPublicKey(hotstuff.ValidatorIndex(idx))
		if perr != nil {
			return false
		}
		return hotstuff.VerifyBLSSignature(sig, pk, msg)
	}
	return signer, verifier, nil
}

func authorizeWalletConsensusEngine(
	engine consensus.Engine,
	signer types.Address,
	signFn func(accounts.Account, string, []byte) ([]byte, error),
) bool {
	if poa, ok := engine.(*apoa.Apoa); ok {
		poa.Authorize(signer, signFn)
		return true
	}
	if pos, ok := engine.(*apos.APos); ok {
		pos.Authorize(signer, signFn)
		return true
	}
	return false
}

func resolveConsensusRuntimePlan(engine consensus.Engine) consensusRuntimePlan {
	var plan consensusRuntimePlan
	switch e := engine.(type) {
	case *apos.APos:
		plan.bindAPOSBlockChain = true
	case *hotstuff.HotStuff:
		plan.registerHotStuffAdminAPI = true
		plan.startHotStuffService = e.Engine() != nil
	}
	return plan
}

func resolveConsensusSignerPlan(engine consensus.Engine) consensusSignerPlan {
	switch engine.(type) {
	case *apoa.Apoa, *apos.APos:
		return consensusSignerPlan{mode: consensusSignerModeWallet}
	case *hotstuff.HotStuff:
		return consensusSignerPlan{mode: consensusSignerModeHotStuffBLS}
	default:
		return consensusSignerPlan{mode: consensusSignerModeNone}
	}
}

func resolveConsensusEngine(cfg *conf.Config, profile params.ProfileDescriptor, chainKv kv.RwDB) (consensus.Engine, error) {
	if cfg == nil || cfg.ChainCfg == nil {
		return nil, errors.New("missing chain config")
	}
	if !profile.SupportsConsensus(cfg.ChainCfg.Consensus) {
		return nil, fmt.Errorf("execution profile %q does not support consensus %q", profile.String(), cfg.ChainCfg.Consensus)
	}
	if cfg.ChainCfg.Consensus == params.EtHashConsensus && isPublicEthereumChain(cfg.NodeCfg.Chain) {
		return nil, publicEthereumPresetUnavailableError(cfg.NodeCfg.Chain)
	}

	if engine, ok := resolveSharedConsensusEngine(cfg.ChainCfg, chainKv); ok {
		return engine, nil
	}
	return resolveN42ConsensusEngine(cfg, chainKv)
}

func resolveSharedConsensusEngine(chainCfg *params.ChainConfig, chainKv kv.RwDB) (consensus.Engine, bool) {
	if chainCfg == nil {
		return nil, false
	}
	switch chainCfg.Consensus {
	case params.CliqueConsensus:
		return apoa.New(chainCfg.Clique, chainKv), true
	case params.Faker:
		return apos.NewFaker(), true
	default:
		return nil, false
	}
}

func resolveN42ConsensusEngine(cfg *conf.Config, chainKv kv.RwDB) (consensus.Engine, error) {
	if cfg == nil || cfg.ChainCfg == nil {
		return nil, errors.New("missing chain config")
	}
	switch cfg.ChainCfg.Consensus {
	case params.AposConsensu:
		apos.SetHardForkAllocDir(cfg.NodeCfg.DataDir)
		return apos.New(cfg.ChainCfg.Apos, chainKv, cfg.ChainCfg), nil
	case params.HotStuffConsensus:
		return hotstuff.New(cfg.ChainCfg.HotStuff, cfg.ChainCfg), nil
	default:
		return nil, fmt.Errorf("invalid engine name %s", cfg.ChainCfg.Consensus)
	}
}

func (n *Node) authorizeMiningEngine(etherbase types.Address) error {
	switch resolveConsensusSignerPlan(n.engine).mode {
	case consensusSignerModeWallet:
		wallet, findErr := n.accman.Find(accounts.Account{Address: etherbase})
		if wallet == nil || findErr != nil {
			log.Error("Etherbase account unavailable locally", "err", findErr)
			return fmt.Errorf("signer missing: %v", findErr)
		}
		if authorizeWalletConsensusEngine(n.engine, etherbase, wallet.SignData) {
			return nil
		}
	case consensusSignerModeHotStuffBLS:
		hs, ok := resolveHotStuffEngine(n.engine)
		if !ok {
			return errors.New("hotstuff engine unavailable")
		}
		blsKey, blsErr := hotstuff.LoadBLSKeyFromDir(n.keyDir, etherbase)
		if blsErr != nil {
			log.Error("HotStuff BLS key unavailable", "err", blsErr)
			return fmt.Errorf("hotstuff BLS key missing for %s: %v", etherbase, blsErr)
		}
		hs.Authorize(etherbase, blsKey)

		// Inject reward function (delegates to apos reward logic).
		hs.SetRewardFunc(func(chainCfg *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, chain consensus.N42ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
			return apos.DoReward(chainCfg, ibs, header, chain)
		})

		if err := hs.InitEngineFromConfig(); err != nil {
			return fmt.Errorf("hotstuff engine init failed: %w", err)
		}

		// Wire the live BLS committee-evidence pool (continues the resealed
		// chain's multi-sig for newly produced blocks). No-op unless enabled.
		if err := n.wireCommitteePool(hs); err != nil {
			return err
		}
	}
	return nil
}

// wireCommitteePool constructs the simulated BLS committee pool from config and
// injects it into the HotStuff engine (ParentBeaconRoot link + VerifyHeader) and
// the blockchain (per-block evidence write), so live block production keeps the
// BLS multi-sig that the replay reseal stamped onto historical blocks. The seed
// and sizes must match the resealer's for byte-identical continuation. No-op
// when the pool is not configured/enabled.
func (n *Node) wireCommitteePool(hs *hotstuff.HotStuff) error {
	hsCfg := n.config.ChainCfg.HotStuff
	if hsCfg == nil || hsCfg.CommitteePool == nil || !hsCfg.CommitteePool.Enabled {
		return nil
	}
	cp := hsCfg.CommitteePool
	seedBytes, err := hex.DecodeString(strings.TrimPrefix(cp.SeedHex, "0x"))
	if err != nil || len(seedBytes) != 32 {
		return fmt.Errorf("hotstuff committee pool: seedHex must be 32-byte hex (got %d bytes): %v", len(seedBytes), err)
	}
	var seed [32]byte
	copy(seed[:], seedBytes)
	pool, err := blspool.NewSimulatedPool(blspool.PoolConfig{
		Seed:          seed,
		PoolSize:      cp.PoolSize,
		CommitteeSize: cp.CommitteeSize,
		RampBlocks:    cp.RampBlocks,
		AllowHandover: cp.AllowHandover,
	})
	if err != nil {
		return fmt.Errorf("hotstuff committee pool: %w", err)
	}

	// Persist validator hand-overs to the chain DB, and load any recorded at a
	// previous run so the committee state survives restart.
	pool.SetPersistHook(func(slot int, pubkey []byte) error {
		return n.db.Update(context.Background(), func(tx kv.RwTx) error {
			return rawdb.WriteCommitteeRegistration(tx, slot, pubkey)
		})
	})
	if err := n.db.View(context.Background(), func(tx kv.Tx) error {
		regs, rerr := rawdb.ReadCommitteeRegistrations(tx)
		if rerr != nil {
			return rerr
		}
		return pool.LoadRegistrations(regs)
	}); err != nil {
		return fmt.Errorf("hotstuff committee pool: load registrations: %w", err)
	}

	realBC, ok := n.blockChain.(*internal.BlockChain)
	if !ok {
		return errors.New("hotstuff committee pool: concrete blockchain unavailable")
	}
	realBC.SetCommitteePool(pool)
	hs.SetCommitteeEvidence(pool, realBC)
	log.Info("HotStuff live BLS committee pool wired",
		"poolSize", cp.PoolSize, "committee", cp.CommitteeSize,
		"rampBlocks", cp.RampBlocks, "handover", cp.AllowHandover, "replaced", pool.ReplacedCount())
	return nil
}
