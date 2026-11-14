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
// AI runtime and inference start helpers.

package node

import (
	"time"

	"github.com/n42blockchain/N42/internal/ai/attestation"
	"github.com/n42blockchain/N42/internal/ai/coord"
	"github.com/n42blockchain/N42/internal/ai/governance"
	"github.com/n42blockchain/N42/internal/ai/training"
	"github.com/n42blockchain/N42/internal/ai/wallet"
	"github.com/n42blockchain/N42/internal/distributed/compute/inference"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/log"
)

func (n *Node) startAIRuntime() {
	if !n.runtimePlan.startAIRuntime {
		return
	}
	if n.config.AICfg.Wallet.Enabled {
		n.walletService = wallet.NewService(n.config.AICfg.Wallet.MaxSessionKeys, n.config.AICfg.Wallet.PaymasterEnabled)
		log.Info("AI wallet service enabled",
			"maxSessionKeys", n.config.AICfg.Wallet.MaxSessionKeys,
			"paymaster", n.config.AICfg.Wallet.PaymasterEnabled,
		)
	}
	if n.config.AICfg.Governance.Enabled {
		reg := governance.NewDatasetRegistry(n.config.AICfg.Governance.MaxDatasets)
		n.dataGovernance = governance.NewCommittee(governance.CommitteeConfig{
			Quorum:    n.config.AICfg.Governance.CommitteeQuorum,
			Threshold: n.config.AICfg.Governance.CommitteeThreshold,
		}, reg)
		log.Info("AI data governance enabled",
			"quorum", n.config.AICfg.Governance.CommitteeQuorum,
			"threshold", n.config.AICfg.Governance.CommitteeThreshold,
		)
	}
	if n.config.AICfg.Training.Enabled {
		var gov training.DatasetGovernance
		if n.dataGovernance != nil {
			gov = n.dataGovernance
		}
		n.trainingProver = training.NewTrainingProver(gov)
		log.Info("ZK training verification enabled")
	}
	if n.config.AICfg.Attestation.Enabled {
		ttl := time.Duration(n.config.AICfg.Attestation.TTLSec) * time.Second
		n.attestationService = attestation.NewAttestationService(
			nil, nil, ttl, n.config.AICfg.Attestation.MaxItems,
		)
		log.Info("ZK inference attestation enabled",
			"ttl", n.config.AICfg.Attestation.TTLSec,
		)
	}
	if n.config.AICfg.Coord.Enabled {
		maxAgents := n.config.AICfg.Coord.MaxAgentsPerNode
		if maxAgents <= 0 {
			maxAgents = 10000
		}
		n.agentRegistry = coord.NewAgentRegistry(maxAgents)
		log.Info("AI agent coordination enabled",
			"maxAgents", maxAgents,
		)
	}
	if n.config.AICfg.Inference.Enabled {
		n.startAIInference()
	}
}

// startAIInference wires the AI inference precompile (0x0301) to a real WASM
// execution backend: ModelRegistry (metadata) + a keccak256 in-memory CAS
// (input/output blobs) + WazeroExecutor (genuine wazero-backed execution,
// see internal/distributed/compute/wasm/wazero_runtime.go) + InferenceService
// (opML request lifecycle) + PrecompileBackend (the vm.InferenceBackend
// adapter). Before this wiring, vm.SetInferenceBackend was called only from
// a unit test — every requestInference call in a real chain hit
// errAIInferenceNoBackend regardless of chain config activation.
//
// The model registry starts empty: there is no admin RPC yet to register a
// model's WASM bytecode at runtime (a follow-up), so until one is registered,
// requestInference correctly fails with "model not registered" instead of
// "no backend" — an accurate, not a misleading, error.
func (n *Node) startAIInference() {
	models := inference.NewModelRegistry()
	cas := inference.NewMemCAS()
	executor := inference.NewWazeroExecutor(n.ctx, inference.WazeroExecutorConfig{
		FuncName:  n.config.AICfg.Inference.FuncName,
		FuelLimit: n.config.AICfg.Inference.FuelLimit,
	})
	svc := inference.NewInferenceService(models)
	svc.SetExecutor(executor)
	backend := inference.NewPrecompileBackend(svc, cas.Load, cas.Store)

	vm2.SetInferenceBackend(backend)
	n.inferenceExecutor = executor
	log.Info("AI inference precompile backend enabled",
		"address", vm2.AIInferenceAddress.Hex(),
		"funcName", n.config.AICfg.Inference.FuncName,
		"fuelLimit", n.config.AICfg.Inference.FuelLimit,
	)
}
