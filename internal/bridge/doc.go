// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

/*
Package bridge implements the N42 ZK-native cross-chain bridge.

# Architecture

The bridge is organized into four functional clusters within a single package
(flat layout avoids Go import cycles while maintaining clear separation):

	N42→ETH (ZK proof path):
	  publisher.go        BridgePublisher — polls chain, batches blocks, submits proofs
	  header_prover.go    HeaderChainProver — SP1 ZK proof generation + local verification
	  state_prover.go     StateInclusionProof — JMT Merkle proofs for state inclusion
	  eth_submitter.go    ETHSubmitter — sends proofs to N42Verifier.sol on Ethereum

	ETH→N42 (reverse bridge):
	  eth_light_client.go EthLightClient — tracks ETH finality via 512-member sync committee
	  eth_beacon_fetcher.go BeaconFetcher — polls ETH beacon API for finality updates
	  bls_verifier.go     BLSVerifierImpl — BLS12-381 aggregate signature verification

	Multi-chain (Hyperlane):
	  hyperlane_dispatcher.go HyperlaneMailboxBinding — dispatches messages to 150+ chains
	  hyperlane_receiver.go   HyperlaneReceiver — monitors inbound Hyperlane messages
	  hyperlane_abi.go        Hyperlane IMailbox ABI constants

	Core routing:
	  router.go           Router interface + BridgeStatus enum
	  zk_router.go        ZKRouter — dual-path routing (ZK for ETH, Hyperlane for others)
	  metrics.go          Prometheus metrics (shared across all clusters)

# Trust Chain

	HotStuff-2 BLS consensus → SP1 ZK header proof → JMT state proof → ETH on-chain verification

# Decoupling

BridgePublisher depends only on interfaces (ProofSubmitter, IBlockChain) and is
independent of ZKRouter, HyperlaneDispatcher, and node.Node. The ZKRouter
orchestrates all paths but each cluster is independently testable.

# Solidity Contracts (contracts/bridge/)

	N42Verifier.sol   SP1 proof verification + verified state root storage
	N42Bridge.sol     Deposit/withdraw with ZK proof + time-locked large withdrawals
	ZKISM.sol         Hyperlane ISM replaced with ZK verification
*/
package bridge
