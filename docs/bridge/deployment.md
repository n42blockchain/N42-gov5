# Cross-chain Bridge Deployment Matrix

This document is the operator-facing reference for what each `BridgeCfg`
field activates and what infrastructure each runtime mode requires. It
exists so that an operator can answer "what config do I need to anchor
N42 finality to Ethereum?" without reading `internal/node/node.go`'s
`startBridgeRuntime`.

The wiring itself lives at `internal/node/node.go:1807-1900`. The mock
publisher integration tests live at `internal/bridge/publisher_test.go`
and exercise the same path (chain → prover → submitter → state machine)
that production runs hit, just with scripted blocks instead of a real
chain.

## Runtime modes

Each row is gated by `cfg.BridgeCfg.Enabled`. If `Enabled = false`, the
bridge runtime never starts and `n.bridgePublisher`/`n.bridgeRouter`
remain nil.

| Mode | Required config | Behavior | When to use |
|------|----------------|----------|-------------|
| **Disabled** (default) | `Enabled: false` | No bridge runtime. `bridgePublisher`, `bridgeRouter` are nil. | Local dev nodes, validators that don't anchor. |
| **Local-only** (dry run) | `Enabled: true` only | Publisher polls chain, generates header chain proofs in-memory, **never submits**. `LastProvenBlock()` advances. | Smoke testing the proof generation pipeline without an ETH endpoint. Tested by `TestBridgePublisher_DryRunWhenSubmitterNil`. |
| **Local + ETH submission** | + `EthRPCEndpoint`, `VerifierAddress` | Publisher generates proofs (still without SP1 ZK) and submits them to the ETH `Verifier` contract via `ETHSubmitter`. | Devnet anchoring, integration tests against a real ETH RPC. |
| **Full ZK + ETH submission** | + `SP1Endpoint` or `SP1GuestBinary` | Publisher uses `ProveHeaderRangeWithSP1` to generate a real SP1 ZK proof (`HeaderChainProof.ProofData != nil`) before submitting. | Production. Slow proof generation but mathematically trustless settlement. |
| **Full + reverse bridge** | + `EthLightClientEnabled`, `EthBeaconEndpoint` | Adds the ETH→N42 path: a `BeaconFetcher` ingests beacon chain data into an `EthLightClient`, which the router uses to verify incoming withdrawals. | Production with bidirectional flows. |
| **Multi-chain (Hyperlane)** | + `HyperlaneEnabled`, `HyperlaneMailbox`, `HyperlaneN42Domain` | Adds Hyperlane Mailbox dispatcher to the router. Sends to non-ETH destinations route through Hyperlane instead of the ZK path. | When you need to bridge to chains other than ETH (Arbitrum, Optimism, Polygon, etc.). |

The five `Default*` route entries in `defaultRouteTable()`
(`zk_router.go:349-359`) put **Ethereum** on the ZK path and the rest of
the supported chains on Hyperlane. The router falls through to Hyperlane
for any unknown destination if Hyperlane is configured.

## Field-by-field

| Field | Default | Effect |
|-------|---------|--------|
| `Enabled` | `false` | Master switch. `false` ⇒ `startBridgeRuntime` is a no-op. |
| `PublisherBatchSize` | `100` (via `DefaultPublisherConfig`) | Number of blocks per proof batch. Larger ⇒ fewer ETH txs but slower finality. |
| `PublisherPollInterval` | `12s` | How often the publisher checks for new blocks. Pacing — set near block time. |
| `PublisherStartBlock` | `0` | Where to begin proving from on cold start. Use this to skip an already-proven historical range. |
| `EthRPCEndpoint` | `""` | If empty ⇒ submitter is nil ⇒ dry-run mode (proofs generated, never submitted). |
| `VerifierAddress` | `""` | Required when `EthRPCEndpoint != ""`. Address of the on-chain `Verifier` contract that accepts `HeaderChainProof`. |
| `BridgeAddress` | `""` | Currently unused by the publisher; reserved for future direct-call paths. |
| `SP1Endpoint` | `""` | Remote SP1 prover RPC. If both this and `SP1GuestBinary` are empty ⇒ no SP1 ⇒ proofs have `ProofData == nil`. |
| `SP1GuestBinary` | `""` | Path to a local SP1 RISC-V64 guest ELF. Alternative to `SP1Endpoint`. |
| `EthLightClientEnabled` | `false` | Enables reverse bridge (ETH→N42). Requires `EthBeaconEndpoint`. |
| `EthBeaconEndpoint` | `""` | Beacon chain HTTP endpoint for the light client. |
| `HyperlaneEnabled` | `false` | Enables Hyperlane dispatcher in the router. |
| `HyperlaneMailbox` | `""` | Hyperlane Mailbox contract on the LOCAL chain (the address the dispatcher calls). |
| `HyperlaneISMAddress` | `""` | Currently informational; ISM verification happens contract-side. |
| `HyperlaneN42Domain` | `0` | N42's Hyperlane domain ID. Embedded in dispatched messages. |

## Wiring trace

```
node.Start()
 └─ startBridgeRuntime() [node.go:1807]
    ├─ if !runtimePlan.startBridgeRuntime → return                          [bcfg.Enabled gate]
    ├─ bridgeCtx, bridgeCancel := context.WithCancel(n.ctx)
    ├─ sp1Client := zkprover.NewSP1ProverClient(...)                        [if SP1Endpoint OR SP1GuestBinary]
    ├─ headerProver := bridge.NewHeaderChainProver(sp1Client)
    ├─ submitter   := bridge.NewETHSubmitter(...)                            [if EthRPCEndpoint AND VerifierAddress]
    ├─ pub := bridge.NewBridgePublisher(chain, vs, prover, submitter, cfg)
    ├─ go pub.Run(bridgeCtx)                                                 [main loop, pollInterval ticks]
    ├─ hyperlane := bridge.NewHyperlaneMailboxBinding(...)                   [if HyperlaneEnabled]
    ├─ ethLC := bridge.NewEthLightClient(...) + go beaconFetcher.Run(...)    [if EthLightClientEnabled]
    ├─ stateProver := func(key) { ProveStateInclusion(jmtTree, root, key) }  [if blockChain.JMTCommitment != nil]
    ├─ n.bridgeRouter = bridge.NewZKRouter(pub, hyperlane, ethLC, stateProver, nil)
    └─ rpcAPIs += api.NewBridgeAPI(n.bridgeRouter).APIs()                    [exposes JSON-RPC bridge_*]

node.Stop()
 └─ "Bridge" stop step [node.go:2183]
    ├─ bridgeCancel()                                                        [drains all goroutines]
    ├─ bridgeRouter.Close()
    ├─ bridgeRouter = nil
    └─ bridgePublisher = nil
```

## Smoke test matrix

The publisher's full state machine is covered by mock-driven integration
tests in `internal/bridge/publisher_test.go`. Each test maps to a real
operational concern:

| Test | Concern | Equivalent devnet scenario |
|------|---------|----------------------------|
| `TestBridgePublisher_BatchAdvance` | Happy path: full batch ⇒ submit ⇒ advance | "Devnet has 100 blocks; bridge anchors 1 batch to ETH; LastProvenBlock = 100." |
| `TestBridgePublisher_NoOpUntilBatchFull` | Don't submit partial batches | "Devnet has 50 blocks but batchSize=100; nothing submitted, lastBlock=0." |
| `TestBridgePublisher_DryRunWhenSubmitterNil` | Local-only mode (no ETH endpoint) | "Operator forgot `EthRPCEndpoint`; publisher still proves and logs but doesn't burn gas." |
| `TestBridgePublisher_RunLoopMultipleBatches` | Full Run goroutine + ctx cancel + 3 batches | "Devnet runs for 30s, 3 anchor txs land on ETH, then SIGTERM cleanly drains the publisher." |
| `TestBridgePublisher_SubmitFailureRetries` | Submit error must NOT advance lastBlock | "ETH RPC goes down mid-anchor; bridge stalls (does not skip the failed range); recovers when RPC returns." |

The `SubmitFailureRetries` test is important: it confirms the publisher
follows the same "don't advance the persistent watermark on failure"
discipline as the EthEL freezer ordering in `output_batcher.go:Sync`.

## Why no live devnet smoke

A real cross-chain devnet smoke would require:
- A running N42 node with bridge enabled
- A running ETH devnet (Anvil/Hardhat) with the `Verifier` contract deployed
- A funded `etherbase` for ETH txs
- Either a real SP1 prover or a mock verifier accepting empty proofs

Setting that up reliably is out of scope for this stability pass. The
mock publisher tests cover the failure modes that would surface bugs
**inside** N42; the on-chain verifier is owned by a separate contracts
repo and tested there. When a live devnet is available, run:

```bash
# 1. Start ETH devnet with Verifier deployed
anvil --port 8545
forge script DeployVerifier --rpc-url http://localhost:8545 --broadcast

# 2. Start N42 node with bridge enabled
n42 --bridge-enable \
    --bridge-eth-rpc http://localhost:8545 \
    --bridge-verifier-address 0x... \
    --bridge-publisher-batch 10 \
    --bridge-publisher-poll 2s

# 3. Wait for first batch and check ETH side
n42-cli bridge status        # local view
cast call $VERIFIER 'latestStateRoot()(bytes32)' --rpc-url http://localhost:8545
```

Any future devnet smoke procedure should be added here.

## Cross-references

- `internal/bridge/publisher.go` — `BridgePublisher` implementation
- `internal/bridge/zk_router.go` — multi-chain router
- `internal/bridge/header_prover.go` — `ProveHeaderRange` / `ProveHeaderRangeWithSP1`
- `internal/bridge/eth_submitter.go` — `ETHSubmitter` (on-chain submission)
- `internal/bridge/eth_light_client.go` — reverse-bridge sync committee verification
- `internal/node/node.go:1807-1900` — `startBridgeRuntime`
- `internal/node/node.go:2183-2192` — bridge stop step
- `conf/bridge_config.go` — full `BridgeCfg` struct
