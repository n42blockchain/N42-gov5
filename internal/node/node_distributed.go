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
// Distributed runtime, coprocessor provider and bridge runtime start helpers.

package node

import (
	"context"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/bridge"
	dwasm "github.com/n42blockchain/N42/internal/distributed/compute/wasm"
	dcoprocessor "github.com/n42blockchain/N42/internal/distributed/coprocessor"
	dmessaging "github.com/n42blockchain/N42/internal/distributed/messaging"
	dnotify "github.com/n42blockchain/N42/internal/distributed/notify"
	dstorage "github.com/n42blockchain/N42/internal/distributed/storage"
	dstorageEd2k "github.com/n42blockchain/N42/internal/distributed/storage/ed2k"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

func (n *Node) startDistributedRuntime() {
	if !n.runtimePlan.startDistributed {
		return
	}
	if n.config.CoprocessorCfg.Enabled {
		svc, err := dcoprocessor.NewService(&n.config.CoprocessorCfg)
		if err != nil {
			log.Error("Failed to create coprocessor service", "err", err)
		} else {
			n.coprocessorService = svc
			svc.Start()
			log.Info("ZK coprocessor service enabled",
				"tier", n.config.CoprocessorCfg.DefaultVerificationTier,
				"marketplace", n.config.CoprocessorCfg.EnableMarketplace,
			)
			n.startCoprocessorProvider(svc)
		}
	}
	// Log compute engine availability (engines are started on-demand by coprocessor).
	cc := &n.config.ComputeCfg
	if cc.WASMEnabled || cc.BatchEnabled || cc.InferenceEnabled {
		log.Info("Distributed compute engines configured",
			"wasm", cc.WASMEnabled,
			"batch", cc.BatchEnabled,
			"inference", cc.InferenceEnabled,
		)
	}
	if n.config.MessagingCfg.Enabled {
		n.messagingService = dmessaging.NewService(&n.config.MessagingCfg)
		n.messagingService.Start()

		// Wire P2P relay if enabled and P2P layer is available.
		if n.config.MessagingCfg.P2PRelayEnabled && n.p2p != nil {
			relay := dmessaging.NewRelay(&n.config.MessagingCfg, n.messagingService, n.p2p, nil)
			if err := relay.Start(); err != nil {
				log.Error("Failed to start messaging relay", "err", err)
			} else {
				n.messagingService.SetRelay(relay)
			}
		}

		log.Info("Messaging service enabled",
			"relay", n.config.MessagingCfg.P2PRelayEnabled,
			"encryption", n.config.MessagingCfg.EncryptionEnabled,
			"rln", n.config.MessagingCfg.RLNEnabled,
			"persistence", n.config.MessagingCfg.PersistenceEnabled,
			"groups", n.config.MessagingCfg.GroupsEnabled,
			"stream", n.config.MessagingCfg.StreamServerEnabled,
			"did", n.config.MessagingCfg.DIDEnabled,
		)
	}
	if n.config.StorageCfg.Enabled {
		n.storageBridge = dstorage.NewBridge(&n.config.StorageCfg)
		n.storageBridge.Start()

		// Build UniversalResolver with all available backends.
		contentBridge := dstorage.NewContentBridge(n.storageBridge.IPFSClient(), false)
		ed2kBridge := dstorageEd2k.NewBridge()
		// CAS callback reads from MDBX ContentStore table; nil if not available.
		var casGet func([32]byte) ([]byte, error)
		if n.db != nil {
			casGet = func(hash [32]byte) ([]byte, error) {
				rtx, err := n.db.BeginRo(context.Background())
				if err != nil {
					return nil, err
				}
				defer rtx.Rollback()
				return rtx.GetOne(modules.ContentStore, hash[:])
			}
		}
		n.storageResolver = dstorage.NewUniversalResolver(
			n.storageBridge,
			nil, // torrent bridge (requires running client, optional)
			ed2kBridge,
			contentBridge,
			casGet,
		)
		log.Info("Storage bridge + universal resolver enabled")
	}
	if n.config.NotifyCfg.Enabled {
		n.notifyService = dnotify.NewService(&n.config.NotifyCfg)
		n.notifyService.Start()
		log.Info("Push notification service enabled")
	}
}

// startCoprocessorProvider opts this node in as a resident compute provider
// (CoprocessorCfg.ProviderEnabled): register the configured identity with
// stake and WASM capability, then serve claimable tasks with the built-in
// wazero engine. The serve loop lives under the coprocessor service's
// lifecycle, so Stop() tears it down and closes the engine.
func (n *Node) startCoprocessorProvider(svc *dcoprocessor.Service) {
	cfg := &n.config.CoprocessorCfg
	if !cfg.ProviderEnabled {
		return
	}
	if !types.IsHexAddress(cfg.ProviderAddress) {
		log.Error("Coprocessor provider disabled: invalid provider_address", "addr", cfg.ProviderAddress)
		return
	}
	addr := types.HexToAddress(cfg.ProviderAddress)
	if err := svc.RegisterProvider(addr, cfg.ProviderStakeWei, []dcoprocessor.Capability{dcoprocessor.CapWASM}); err != nil {
		log.Error("Coprocessor provider disabled: registration failed", "err", err)
		return
	}
	engine := dwasm.NewEngine(dwasm.NewWazeroRuntime(context.Background()), 64, cfg.TaskTimeoutSec, 1.0)
	prov := dcoprocessor.NewReferenceProvider(svc, addr, engine, cfg.ProviderEntryPoint)
	svc.AttachProvider(prov, time.Duration(cfg.ProviderPollSec)*time.Second, cfg.ProviderGasLimit, func() {
		_ = engine.Close(context.Background())
	})
}

func (n *Node) startBridgeRuntime() {
	if !n.runtimePlan.startBridgeRuntime {
		return
	}
	bridgeCtx, bridgeCancel := context.WithCancel(n.ctx)
	n.bridgeCancel = bridgeCancel
	bcfg := &n.config.BridgeCfg

	var sp1Client *zkprover.SP1ProverClient
	if bcfg.SP1Endpoint != "" || bcfg.SP1GuestBinary != "" {
		sp1Client = zkprover.NewSP1ProverClient(bcfg.SP1Endpoint, bcfg.SP1GuestBinary, "")
	}
	headerProver := bridge.NewHeaderChainProver(sp1Client)

	var submitter bridge.ProofSubmitter
	if bcfg.EthRPCEndpoint != "" && bcfg.VerifierAddress != "" {
		var err error
		submitter, err = bridge.NewETHSubmitter(
			bcfg.EthRPCEndpoint,
			types.HexToAddress(bcfg.VerifierAddress),
			n.etherbase)
		if err != nil {
			log.Warn("Bridge ETH submitter creation failed", "err", err)
		}
	}

	pubCfg := &bridge.PublisherConfig{
		BatchSize:    bcfg.PublisherBatchSize,
		PollInterval: bcfg.PublisherPollInterval,
		StartBlock:   bcfg.PublisherStartBlock,
	}
	n.bridgePublisher = bridge.NewBridgePublisher(n.blockChain, resolveBridgeValidatorSet(n.engine), headerProver, submitter, pubCfg)
	go func() {
		if err := n.bridgePublisher.Run(bridgeCtx); err != nil && err != context.Canceled {
			log.Warn("Bridge publisher stopped", "err", err)
		}
	}()

	var hyperlane bridge.HyperlaneDispatcher
	if bcfg.HyperlaneEnabled && bcfg.HyperlaneMailbox != "" {
		var err error
		hyperlane, err = bridge.NewHyperlaneMailboxBinding(
			fmt.Sprintf("http://127.0.0.1:%s", n.config.NodeCfg.HTTPPort),
			types.HexToAddress(bcfg.HyperlaneMailbox),
			bcfg.HyperlaneN42Domain,
			n.etherbase)
		if err != nil {
			log.Warn("Hyperlane dispatcher creation failed", "err", err)
		}
	}

	var ethLC *bridge.EthLightClient
	if bcfg.EthLightClientEnabled && bcfg.EthBeaconEndpoint != "" {
		blsVerifier := &bridge.BLSVerifierImpl{}
		lcCfg := &bridge.EthLightClientConfig{}
		var err error
		ethLC, err = bridge.NewEthLightClient(lcCfg, blsVerifier)
		if err != nil {
			log.Warn("ETH light client creation failed (need bootstrap data)", "err", err)
		} else {
			fetcher := bridge.NewBeaconFetcher(bcfg.EthBeaconEndpoint, ethLC, 12*time.Second)
			go func() {
				if err := fetcher.Run(bridgeCtx); err != nil && err != context.Canceled {
					log.Warn("Beacon fetcher stopped", "err", err)
				}
			}()
		}
	}

	var stateProver bridge.StateProverFunc
	if realBC, ok := n.blockChain.(*internal.BlockChain); ok {
		if jmtCommit := realBC.JMTCommitment(); jmtCommit != nil {
			tree := jmtCommit.Tree()
			stateProver = func(key []byte) (*bridge.StateInclusionProof, error) {
				current := n.blockChain.CurrentBlock()
				if current == nil {
					return nil, fmt.Errorf("no current block")
				}
				stateRoot := types.Hash(current.StateRoot())
				return bridge.ProveStateInclusion(tree, stateRoot, key)
			}
		}
	}

	n.bridgeRouter = bridge.NewZKRouter(
		n.bridgePublisher, hyperlane, ethLC, stateProver, nil)

	bridgeAPI := api.NewBridgeAPI(n.bridgeRouter)
	n.rpcAPIs = append(n.rpcAPIs, bridgeAPI.APIs()...)

	log.Info("Cross-chain bridge enabled",
		"ethRPC", bcfg.EthRPCEndpoint,
		"hyperlane", bcfg.HyperlaneEnabled)
}
