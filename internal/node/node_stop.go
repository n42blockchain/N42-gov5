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

// stopServices phases: each returns its group of shutdown steps, in the
// order (*Node).stopServices runs them.

package node

import (
	"context"

	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// stopRPCClosers returns the RPC, gRPC KV and web3 gateway shutdown steps.
func (n *Node) stopRPCClosers() []namedCloser {
	return []namedCloser{
		// 1. RPC services (depends on everything, stop first)
		{"RPC services", func() error { n.stopRPC(); return nil }},
		// 2a. gRPC KV server
		{"gRPC KV server", func() error {
			if n.grpcServer != nil {
				n.grpcServer.GracefulStop()
			}
			return nil
		}},
		// 2b. web3:// gateway
		{"web3 gateway", func() error {
			if n.web3Gateway != nil {
				n.web3Gateway.Stop()
			}
			return nil
		}},
	}
}

// stopAIClosers returns the AI and mobile attestation shutdown steps.
func (n *Node) stopAIClosers() []namedCloser {
	return []namedCloser{
		// 2c. AI services
		{"AI wallet", func() error {
			n.walletService = nil
			return nil
		}},
		{"AI safety + coord", func() error {
			n.attestationService = nil
			n.trainingProver = nil
			n.dataGovernance = nil
			n.agentRegistry = nil
			return nil
		}},
		{"AI inference", func() error {
			if n.inferenceExecutor != nil {
				if err := n.inferenceExecutor.Close(context.Background()); err != nil {
					log.Warn("AI inference executor close failed", "err", err)
				}
				n.inferenceExecutor = nil
			}
			return nil
		}},
		{"Mobile attestation", func() error {
			if n.mobileHTTPServer != nil {
				n.mobileHTTPServer.Stop()
				n.mobileHTTPServer = nil
			}
			if n.mobileRegService != nil {
				n.mobileRegService.Stop()
				n.mobileRegService = nil
			}
			if n.mobileCommitter != nil {
				n.mobileCommitter.Stop() // final commit drains pending
				n.mobileCommitter = nil
			}
			if n.mobileCohortRelay != nil {
				n.mobileCohortRelay.Stop()
				n.mobileCohortRelay = nil
			}
			if n.mobileCohort != nil {
				n.mobileCohort.Stop()
				n.mobileCohort = nil
			}
			if n.mobilePacketService != nil {
				n.mobilePacketService.Stop()
				n.mobilePacketService = nil
			}
			if n.mobileTorrentClient != nil {
				n.mobileTorrentClient.Stop()
				n.mobileTorrentClient = nil
			}
			return nil
		}},
	}
}

// stopDistributedClosers returns the bridge, distributed services, OtterSync
// and deferred execution shutdown steps.
func (n *Node) stopDistributedClosers() []namedCloser {
	return []namedCloser{
		// 2d. Cross-chain bridge
		{"Bridge", func() error {
			if n.bridgeCancel != nil {
				n.bridgeCancel()
			}
			if n.bridgeRouter != nil {
				n.bridgeRouter.Close()
				n.bridgeRouter = nil
			}
			n.bridgePublisher = nil
			return nil
		}},
		// 2e. Distributed infrastructure
		{"Distributed services", func() error {
			if n.ddnGateway != nil {
				n.ddnGateway.Stop()
			}
			if n.ddnSigner != nil {
				n.ddnSigner.Close()
			}
			// Before the database closes: the sealer may be mid-write.
			if n.txIndexer != nil {
				n.txIndexer.Stop()
			}
			// NOTE: the era store is closed in its own late phase (after
			// Sync/P2P) — RPC and range-serving goroutines read it until
			// those services stop.
			if n.notifyService != nil {
				n.notifyService.Stop()
			}
			if n.messagingService != nil {
				n.messagingService.Stop()
			}
			n.storageResolver = nil
			if n.storageBridge != nil {
				n.storageBridge.Stop()
			}
			if n.coprocessorService != nil {
				n.coprocessorService.Stop()
			}
			return nil
		}},
		// 2e. OtterSync (stateless — just nil the reference)
		{"OtterSync", func() error {
			n.torrentsyncService = nil
			return nil
		}},
		// 2c. Deferred execution pipeline
		{"Deferred execution", func() error {
			if n.deferredPipeline != nil {
				n.deferredPipeline.Stop()
			}
			return nil
		}},
	}
}

// stopAuxiliaryClosers returns the shutdown steps of the auxiliary services:
// snapshot manager, pruner, history, ingest, tx generator, bundler, PeerDAS,
// MCP and the ZK prover.
func (n *Node) stopAuxiliaryClosers() []namedCloser {
	return []namedCloser{
		// 2. Snapshot manager
		{"Snapshot manager", func() error {
			if n.snapshotMgr != nil {
				n.snapshotMgr.Stop()
			}
			return nil
		}},
		// 3. Pruner
		{"Pruner", func() error {
			if n.pruner != nil {
				n.pruner.Stop()
			}
			return nil
		}},
		// 3a. History backfiller — stopped before the DB closes; it holds no
		// state of its own, and a partial batch is simply rebuilt from the
		// marker, so an abrupt stop costs one batch of work and nothing else.
		{"History backfiller", func() error {
			if n.historyBackfiller != nil {
				n.historyBackfiller.Stop()
			}
			return nil
		}},
		// 3b. History expirer
		{"History expirer", func() error {
			if n.historyExpirer != nil {
				n.historyExpirer.Stop()
			}
			return nil
		}},
		// 3c. Ingest server
		{"Ingest server", func() error {
			if n.ingestServer != nil {
				n.ingestServer.Stop()
			}
			return nil
		}},
		// 3d. Transaction generator
		{"Transaction generator", func() error {
			if n.txGenerator != nil {
				n.txGenerator.Stop()
			}
			return nil
		}},
		// 3d. Bundler service
		{"Bundler service", func() error {
			if n.bundlerService != nil {
				n.bundlerService.Stop()
			}
			return nil
		}},
		// 3e. PeerDAS service
		{"PeerDAS service", func() error {
			if n.peerdasService != nil {
				return n.peerdasService.Stop()
			}
			return nil
		}},
		// 3f. MCP server
		{"MCP server", func() error {
			if n.mcpServer != nil {
				return n.mcpServer.Stop()
			}
			return nil
		}},
		{"ZK prover", func() error {
			if n.zkProverService != nil {
				n.zkProverService.Stop()
			}
			return nil
		}},
	}
}

// stopChainClosers returns the miner, sync, txpool, deposit contract,
// consensus and ExEx shutdown steps.
func (n *Node) stopChainClosers() []namedCloser {
	return []namedCloser{
		// 4. Miner
		{"Miner", func() error { n.miner.Close(); return nil }},
		// 4b. Backfill service (stop before sync services)
		{"Backfill sync", func() error {
			if n.backfillService != nil {
				n.backfillService.Stop()
			}
			return nil
		}},
		// 5. Checkpoint sync + Snap sync + Initial sync
		// Note: checkpointSync relies on node context cancellation (done above).
		{"Checkpoint sync", func() error {
			// Cancellation owns checkpoint shutdown; keep the pointer immutable
			// while the sync-startup goroutine observes it.
			return nil
		}},
		{"Snap sync", func() error { return n.snapSync.Stop() }},
		{"Initial sync", func() error { return n.is.Stop() }},
		// 6. Sync service (depends on P2P + blockchain, must stop before blockchain closes)
		{"Sync service", func() error { return n.sync.Stop() }},
		// 7. Transaction pool
		{"Transaction pool", func() error { return n.txspool.Stop() }},
		// 8. Deposit contract
		{"Deposit contract", func() error {
			if n.depositContract != nil {
				return n.depositContract.Stop()
			}
			return nil
		}},
		// 8b. HotStuff consensus service
		{"HotStuff service", func() error {
			if n.hotstuffService != nil {
				n.hotstuffService.Stop()
			}
			if n.hotstuffJournalDB != nil {
				n.hotstuffJournalDB.Close()
			}
			return nil
		}},
		// 9. Consensus engine
		{"Consensus engine", func() error { return n.engine.Close() }},
		// 10. ExEx manager (stop extensions before blockchain closes)
		{"ExEx manager", func() error {
			if n.exexManager != nil {
				n.exexManager.Stop()
			}
			return nil
		}},
	}
}

// stopStateClosers returns the snapshot journal, Verkle flush, stateless,
// blockchain and tracing shutdown steps.
func (n *Node) stopStateClosers() []namedCloser {
	return []namedCloser{
		// 10b. Snapshot journal (persist diff layers before blockchain closes)
		{"Snapshot journal", func() error {
			if realBC, ok := n.blockChain.(*internal.BlockChain); ok {
				if tree := realBC.SnapshotTree(); tree != nil {
					if err := tree.SaveJournal(); err != nil {
						log.Warn("Failed to save snapshot journal", "err", err)
					}
				}
			}
			return nil
		}},
		// 10c. Verkle tree flush (persist nodes before blockchain closes)
		{"Verkle flush", func() error {
			realBC, ok := n.blockChain.(*internal.BlockChain)
			if !ok {
				return nil
			}
			vc := realBC.VerkleCommitment()
			if vc == nil {
				return nil
			}
			nodes, bytes, err := vc.Flush()
			if err != nil {
				log.Warn("Failed to flush Verkle tree", "err", err)
			} else if nodes > 0 {
				log.Info("Verkle tree flushed on shutdown", "nodes", nodes, "bytes", bytes)
			}
			return nil
		}},
		// 10d. Release references to stateless helpers (no goroutines to stop)
		{"ZK verifier + stateless", func() error {
			n.zkVerifier = nil
			n.statelessValidator = nil
			return nil
		}},
		// 11. Blockchain (flush and close DB, after all consumers stopped)
		{"Blockchain", func() error { return n.blockChain.Close() }},
		// 11. OpenTelemetry tracing (flush remaining spans before network goes down)
		{"Tracing", func() error {
			if n.tracingShutdown != nil {
				return n.tracingShutdown(context.Background())
			}
			return nil
		}},
	}
}

// stopNetworkClosers returns the devp2p, P2P and era store shutdown steps.
func (n *Node) stopNetworkClosers() []namedCloser {
	return []namedCloser{
		// 12. P2P networking (transport layer, last to go)
		{"Ethereum devp2p", func() error {
			if n.devp2pServer != nil {
				n.devp2pServer.Stop()
				n.devp2pServer = nil
			}
			return nil
		}},
		{"P2P network", func() error { return n.p2p.Stop() }},
		// Era store LAST among readers' dependencies: sync/P2P/RPC serve
		// ancient reads until stopped; closing earlier would turn their
		// in-flight reads into spurious errors.
		{"Era store", func() error {
			if n.eraStore != nil {
				rawdb.SetAncientStore(nil)
				n.eraStore.Close()
				n.eraStore = nil
			}
			return nil
		}},
	}
}
