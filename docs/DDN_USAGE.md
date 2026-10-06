# Using the DDN

This is the companion usage guide to [`docs/DDN.md`](./DDN.md). Read that document first for the architecture and the implementation-status table.

**Scope reminder**: the N42 Distributed Decision Network (DecisionRequest/DecisionReceipt/ModelManifest, Scheduler, Aggregator, Quorum) is a specification from the whitepaper in `n42-26` and has no implementation in this repository (N42-gov5). This guide covers two things only:

1. What exists **today** in N42-gov5 that a DDN implementation could be built on (AI inference precompile, agent wallet, agent discovery, dataset governance, ZK training/attestation scaffolding, MCP tools).
2. What the whitepaper and `n42-26/docs/decision/` specify as **planned**, clearly marked as such, so you don't confuse a design doc for a shipped feature.

Every code reference below was checked against the actual file on 2026-10-05 in `/data/blockchain/gov5-work/wt-r27`.

## Audiences

- **App/agent developers**: want to call AI inference from a smart contract or MCP client today.
- **Decision providers** (whitepaper role: nodes that run a model and answer requests): there is no provider role to run in this repo yet — see [How a provider will run](#how-a-provider-will-run-planned).
- **Node operators**: want to know which config flags turn on what, and which ports are involved.

## Prerequisites

- A build of N42-gov5 (`make n42`) with CGO enabled (MDBX backend requirement).
- For the inference precompile, a chain config with `AIInferenceTime` set (test/private chains only — see the warning in §"Operational notes").
- For wallet/governance/training/attestation packages, nothing beyond importing the Go packages; they are no node-startup feature flags that block direct library use in tests or tooling today — but see `conf/ai_config.go` for the fields that gate node-level wiring.

## Configuration: what exists today

All AI subsystem configuration lives in `conf/ai_config.go`, under `AICfg`. Every subsystem defaults to **disabled**; nothing below is turned on by default.

| Field | Type | Default | Purpose |
|---|---|---|---|
| `AICfg.Wallet.Enabled` | bool | `false` | Master switch for the agent wallet service |
| `AICfg.Wallet.MaxSessionKeys` | int | `16` | Cap on session keys per account (hard max is `wallet.MaxSessionKeys` = 16) |
| `AICfg.Wallet.DefaultSpendLimit` | string | `"1000000000000000000"` (1 ETH, wei string) | Default per-session spend limit |
| `AICfg.Wallet.PaymasterEnabled` | bool | `false` | Enable gas sponsorship via `PaymasterService` |
| `AICfg.Coord.Enabled` | bool | `false` | Master switch for agent discovery/negotiation |
| `AICfg.Coord.MinAgentStake` | string | `"100000000000000000"` (0.1 ETH) | Minimum stake for `AgentRegistry` registration |
| `AICfg.Coord.NegotiationTimeoutSec` | int | `300` | Task negotiation timeout |
| `AICfg.Coord.MaxAgentsPerNode` | int | `100` | Cap on agents tracked per node |
| `AICfg.Governance.Enabled` | bool | `false` | Master switch for dataset governance |
| `AICfg.Governance.MaxDatasets` | int | `10000` | Cap on registered datasets |
| `AICfg.Governance.CommitteeQuorum` | int | `3` | Minimum votes for a valid tally |
| `AICfg.Governance.CommitteeThreshold` | float64 | `0.67` | Approval ratio required to pass |
| `AICfg.Training.Enabled` | bool | `false` | Master switch for ZK training verification |
| `AICfg.Training.MaxProofs` | int | `10000` | Cap on stored training proofs |
| `AICfg.Attestation.Enabled` | bool | `false` | Master switch for inference attestation |
| `AICfg.Attestation.MaxItems` | int | `100000` | Cap on stored attestations |
| `AICfg.Attestation.TTLSec` | int | `86400` | Attestation expiry (24h) |
| `AICfg.MEVOptimizer.Enabled` | bool | `false` | AI-assisted block building (unrelated to DDN, listed for completeness) |
| `AICfg.Inference.Enabled` | bool | `false` | Enables the real WASM execution backend for the `0x0301` precompile |
| `AICfg.Inference.FuncName` | string | `"infer"` | WASM export invoked per inference request |
| `AICfg.Inference.FuelLimit` | uint64 | `10_000_000` | Fuel bound per execution (see `internal/distributed/compute/wasm/wazero_runtime.go`) |

Messaging-layer config relevant to a future DDN (DID identity, E2E encryption used as a private delivery channel) lives in `conf/messaging_config.go` under `MessagingCfg` — notably `DIDEnabled` (default `false`) and `EncryptionEnabled` (default `false`). Neither field is DDN-specific; they are generic messaging-platform switches. None of `conf/ai_config.go` or `conf/messaging_config.go` has a field named anything like `DDN`, `Decision*`, or `ModelManifest` — because that layer does not exist here.

## How an agent obtains a decision today

There is no DDN `DecisionRequest`/`DecisionReceipt` round trip. What exists is a generic, model-agnostic inference request/response path through a precompile plus an off-chain result cache. This is the closest functional analog, and it is what you'd build a DDN `DecisionProvider` on top of if you were implementing one.

### 1. AI inference precompile (`0x0301`)

Defined in `internal/vm/contracts_ai_inference.go`. Address:

```go
var AIInferenceAddress = types.HexToAddress("0x0000000000000000000000000000000000000301")
```

Four selectors (first input byte):

| Selector | Byte | Signature | Gas |
|---|---|---|---|
| `requestInference` | `0x00` | `(modelHash [32]byte, inputCAS [32]byte, tier [1]byte) -> requestID [32]byte` | `10000 + 100 * len(payload-after-selector)` (`AIInferenceBaseGas` + `AIInferencePerByteGas`) |
| `getResult` | `0x01` | `(requestID [32]byte) -> (status uint8, outputCAS [32]byte)` | `2600` (`AIInferenceGetResultGas`) |
| `getModel` | `0x02` | `(modelHash [32]byte) -> (name, format, capabilities string)` (simplified length-prefixed ABI, not standard dynamic-offset ABI) | `2600` (`AIInferenceGetModelGas`) |
| `listModels` | `0x03` | `(capability string) -> modelHashes [][32]byte` | `5000` (`AIInferenceListModelsGas`) |

`requestInference` requires at least 65 bytes of payload (32 + 32 + 1). `listModels` caps the capability string at `maxCapabilityLen = 1024` bytes.

The precompile dispatches to a global `InferenceBackend` set via `vm.SetInferenceBackend(...)`. **Note the explicit warning in the file's header comment**: `getResult`/`getModel`/`listModels` read node-local state (model registry, CAS, execution progress) that is *not* synchronized by consensus. Do not set `ChainConfig.AIInferenceTime` on a multi-validator network until model registration and result settlement live in consensus state — validators would otherwise diverge and split.

Also note the `runRequestInference` caller-address placeholder: the precompile's `Run(input)` signature has no access to `msg.sender`, so `caller` is currently always the zero address (see the `NOTE` comment in `runRequestInference`). This matters if you plan to use the caller address for spend-limit or reputation attribution — it is not wired yet.

### 2. Wiring the backend at node startup

`internal/distributed/compute/inference/precompile_backend.go` implements `vm.InferenceBackend` by adapting an `InferenceService`:

```go
backend := inference.NewPrecompileBackend(svc, casLoad, casStore)
vm.SetInferenceBackend(backend)
```

`SubmitRequest` loads the input from CAS, calls `svc.SubmitRequest(modelHash, input, caller)` synchronously (fast — safe to call from inside `Run()` during block execution), then dispatches the actual model execution on a background goroutine bounded by `executionTimeout = 60s`, independent of the executor's own fuel-derived deadline. `getResult` polls the outcome later.

The executor itself is wazero-based (`internal/distributed/compute/inference/executor_wazero.go`), with a non-wazero stub (`executor_wasm_stub.go`) and scheduler glue (`executor_scheduler.go`). Model metadata lives in `model.go`.

### 3. Result caching

`internal/distributed/compute/inference/cache.go` defines `ResultCache`, an LRU+TTL cache of `CachedResult{RequestID, Status, OutputCAS, Timestamp}`, shared between the precompile path and the inference service so repeated/verified queries don't re-run the model.

### 4. MCP tools

`internal/mcp/` exposes a JSON-RPC surface (default port `8553`, `conf/mcp_config.go`) for AI agents that would rather call a tool than craft raw EVM calldata:

- `agent_tools.go` — `AgentProvider` interface: `FindAgents(capability, minReputation)`, `RequestTask(requesterDID, capability, inputCAS, maxBudget)`, `CheckTaskStatus(negotiationID)`, `GetReputation(did)`. Backed by `internal/ai/coord`.
- `agent_wallet_tools.go` — `AgentWalletProvider` interface: `CreateAccount(ownerKey, agentDID)`, `GetBalance(address)`, `AccountCount()`. Backed by `internal/ai/wallet`.
- `data_tools.go` — on-chain query tools (token transfers, address profiles, gas analytics, contract events) backed by the ExEx AI indexer (`internal/exex/extensions/`).

None of these three tool files expose a "submit decision request" or "get decision receipt" tool — that would be new work, not present today.

## How to register a model / dataset governance / training proof today

These three packages are real, usable Go APIs independent of any DDN wiring. They are good building blocks for a DDN Model Manifest's "has this model's training data been ethically approved" and "was this model actually trained the way it claims" questions — but nothing here produces a `ModelManifest` struct, because that struct doesn't exist in this repo.

### Dataset governance (`internal/ai/governance/`)

```go
registry := governance.NewDatasetRegistry(maxItems) // 0 = unbounded
id, err := registry.Register(owner, name, version, contentHash, metadataHash, size, recordCount, categories)
// id = Keccak256(owner || name || version)

committee := governance.NewCommittee(governance.CommitteeConfig{Quorum: 3, Threshold: 0.67}, registry)
committee.AddMember(reviewerAddr)
committee.CastVote(&governance.Vote{ /* datasetID, category, decision, signature over Keccak256(datasetID||decision||category) */ })
committee.FinalizeReview(datasetID) // -> DatasetApproved or DatasetRejected
approved := committee.IsApproved(datasetID)
```

Signatures are secp256k1; the voter address is recovered via `crypto.SigToPub` and checked against committee membership (see `internal/ai/governance/committee.go`, `verifyVoteSignature`).

### ZK training verification (`internal/ai/training/`)

```go
prover := training.NewTrainingProver(committee) // committee implements DatasetGovernance.IsApproved
recordID, err := prover.RegisterTraining(modelHash, datasetHashes, configHash, epochCount /* ... */)
proof, err := prover.ProveTraining(recordID, trace) // trace = *TrainingTrace, epoch checkpoints
ok, err := prover.VerifyTrainingProof(proof)
```

Public inputs are exactly 160 bytes: `modelHash(32) || initWeightsHash(32) || finalWeightsHash(32) || configHash(32) || datasetRootHash(32)` (`trainingPublicInputsSize` in `internal/ai/training/types.go`). The proof itself is a simulated hash-chain (`simulatedProofSize = 256` bytes in `prover.go`), not a production zk-SNARK/STARK — `TrainingVerifier` (in `verifier.go`) checks structure and public-input consistency, not a real circuit.

### Inference attestation (`internal/ai/attestation/`)

```go
svc := attestation.NewAttestationService(zkProvider, trainingCheck, ttl, maxItems)
// zkProvider implements ZKProofProvider.GetProof(requestID) ([]byte, bool, error)
// trainingCheck implements TrainingVerification.GetRecord(recordID) (modelHash types.Hash, verified bool, error) — may be nil

att, err := svc.CreateAttestation( /* requestID, modelHash, safety level, etc. */ )
signed, err := svc.SignAttestation(att.ID, operatorPrivKey)
ok, err := svc.VerifySignature(signed)
ok, err = svc.VerifyAttestation(att.ID) // combines signature + ZK proof + (if Critical) training verification
chain, err := svc.CreateChain([]types.Hash{attID1, attID2, ...}) // multi-hop, enforces output[i] == input[i+1]
```

`SafetyLevel` (`Standard` / `HighValue` / `Critical`, `types.go`) determines required rigor — `Critical` requires a verified training record via the `TrainingVerification` interface. This is the closest thing in this repo to a DDN `DecisionReceipt`'s evidence/verification fields, but it is a generic inference-attestation primitive, not DDN's receipt format, and it has no `provider_did`, `probability_distribution`, `confidence`, or `expiry`/`nonce` fields matching the whitepaper's `DecisionReceipt`.

### ZKML proof plumbing (`internal/zkprover/`, `internal/zkverifier/`)

```go
zp := zkprover.NewZKMLProver()
zp.CompileCircuit(modelHash, layerCount)
proof, err := zp.GenerateProof(modelHash, trace) // trace = *ExecutionTrace
```

Public inputs are 96 bytes: `modelHash(32) || inputHash(32) || outputHash(32)` (`publicInputsSize` in `internal/zkprover/zkml.go`). `internal/zkverifier/zkml_verifier.go`'s `ZKMLVerifier` checks the same 96-byte layout and consistency against a model registry. Same caveat as training proofs: structurally real, cryptographically simulated — not a production STARK/SNARK/SP1 circuit for ML inference yet. A `ZKMLVerifierAdapter` connects this to `coprocessor.TieredVerifier` via `SetZKMLVerifier()`.

## How a provider will run (Planned)

The whitepaper's `Decision Provider` role — a node that registers a Model Manifest, listens for `DecisionRequest`s matching its capabilities, and returns signed `DecisionReceipt`s — has no implementation here. There is no provider daemon, no listener process, no manifest registration RPC, and no provider-side reputation/stake contract matching the whitepaper's design. The closest adjacent concepts that do exist (and could be extended) are:

- `internal/ai/coord/registry.go`'s `AgentRegistry` — generic agent capability registration with min-stake (currently a package-level `var minStake = 1 ETH`, not the whitepaper's provider stake specifically).
- `internal/distributed/coprocessor/provider.go` and `marketplace.go` — a generic distributed-compute provider registry and reverse-auction marketplace (stake, capabilities, reputation tracking) built for the broader coprocessor system, not DDN decisions specifically.

Building a real Decision Provider would mean wiring a process that: registers via something like `AgentRegistry`, exposes an inference endpoint (perhaps reusing `InferenceService`/`PrecompileBackend`), and signs receipts — none of which exists as a single runnable binary today.

## Shadow-gateway mode

`n42-26/docs/decision/shadow-gateway.md` describes a **Rust/Python implementation in `n42-26`, not part of this Go repository.** Summary, attributed to that document: a read-only gateway (`scripts/decision_shadow.py`) that accepts one sanitized JSONL event per line, records a deterministic rule result alongside a shadow Jev classification, and cannot write node config, call N42 RPC, sign, submit, restart, or ban anything — critical deterministic alerts cannot be downgraded by model output, and API errors/malformed answers route to "deep analysis" rather than a silent healthy result. It has no counterpart in N42-gov5: there is no Go package here that ingests node/CI logs and runs a shadow AI classification pass.

## Verifying a receipt

The whitepaper's `DecisionReceipt` verification (signature + nonce/expiry + provider identity + optional ZK/TEE proof) is **Specification only** — there is no `DecisionReceipt` type to verify in this repo. The nearest usable analog, with real but different semantics, is `AttestationService.VerifyAttestation` (`internal/ai/attestation/service.go`): it checks an operator's secp256k1 signature over canonical attestation bytes (`canonicalBytes`), consults a pluggable `ZKProofProvider`, and for `SafetyCritical` attestations also checks `TrainingVerification`. Treat this as a structurally similar but functionally distinct primitive — it attests to one inference result's provenance chain, not to a DecisionRequest/DecisionReceipt pair with quorum aggregation.

## Operational notes

- **MCP server**: default port `8553` (`conf/mcp_config.go`, `DefaultMCPCfg().Port = 8553`). Exposes the agent/wallet/data tool surfaces listed above.
- **Message stream (SSE)**: default port `8554` (`conf/messaging_config.go`, `StreamServerPort: 8554`), part of the generic messaging platform, not DDN-specific — would be a plausible transport for pushing decision results to subscribers in a future DDN build.
- **DID identity**: `internal/distributed/messaging/identity/did.go` implements `did:n42:<address>`, generic to the messaging platform. A DDN `provider_did` field would plausibly reuse this method, but no code currently issues a DID scoped to "decision provider."
- **Keys**: wallet session keys (`internal/ai/wallet/account.go`), ethics-committee voter keys, training-proof signer keys, and attestation operator keys are all independent secp256k1 keys managed by their respective packages — there is no unified "DDN provider key" concept.
- **Do not enable `ChainConfig.AIInferenceTime` on a multi-validator network** until model registry/result state moves into consensus-synchronized storage (see the precompile file's header warning, §"AI inference precompile" above).

## Limitations and roadmap pointers

- The DDN protocol (request/receipt/manifest, scheduler, quorum, aggregation, settlement) does not exist in N42-gov5. Building it would be new, substantial work, not a wiring exercise over existing types.
- The pieces that do exist here (wallet, coord, governance, training, attestation, inference precompile) were built independently of the DDN whitepaper and are not currently cross-wired to each other as a pipeline — e.g., `AttestationService`'s `TrainingVerification` interface is optional and typically `nil` unless you construct and pass a concrete implementation yourself.
- ZK proofs in `zkprover`/`zkverifier` and `ai/training` are simulated (hash-chain / fixed-size byte blobs), not production SNARK/STARK/SP1 circuits — do not treat `VerifyTrainingProof`/`ZKMLVerifier` success as a cryptographic correctness guarantee today.
- For the actual, up-to-date DDN rollout status (on the `n42-26` side, not this repo), see `n42-26/docs/decision/roadmap-status-20260926.md` and the newer `n42-26/docs/decision/local-system1-roadmap.md`, which supersedes the whitepaper's phase ordering with a local-first (deterministic rules → local System-1 → Jev → System-2/human) sequence.

## FAQ

**Is DDN running on N42 today?**
No. Neither the protocol nor the N42-System1 model exists in N42-gov5. A separate, unrelated single-use-case prototype (public-proposal classification via Jev) exists as untracked files in `n42-26`, not merged there either.

**Can I call an AI model from a smart contract today?**
Yes, via the `0x0301` precompile, if `ChainConfig.AIInferenceTime` is set and an `InferenceBackend` is wired at node startup (test/private chains only — see the consensus-divergence warning above).

**Does the inference precompile know who called it?**
Not yet in a usable way — `runRequestInference` currently hardcodes the caller to the zero address pending plumbing to carry `msg.sender` into the precompile's `Run()` call.

**Is there a "decision provider" I can run to earn fees answering requests?**
No such role or binary exists yet. The closest generic building blocks are `internal/ai/coord`'s agent registry/negotiation and `internal/distributed/coprocessor`'s provider/marketplace packages, neither DDN-specific.

**Are the ZK proofs in `ai/training` and `zkprover` real?**
No — they are structurally validated simulated proofs (fixed-size byte blobs derived from a hash chain), not real zk-SNARK/STARK circuits. Treat verification success as "well-formed and internally consistent," not "cryptographically proven."

**What is Jev, and does N42-gov5 depend on it?**
Jev is TypeSafe AI's external cloud System-1 model, referenced only in the whitepaper and `n42-26` prototype as an optional benchmark/fallback provider. N42-gov5 has no dependency on it, no API client for it, and no package referencing it.

**Where do I configure AI subsystems in this repo?**
`conf/ai_config.go` (`AICfg`) for wallet/coord/governance/training/attestation/MEV/inference; `conf/messaging_config.go` (`MessagingCfg`) for DID/encryption/stream server if you need a transport layer. All default to disabled.

**If I wanted to prototype DDN here, where would I start?**
Define `DecisionRequest`/`DecisionReceipt`/`ModelManifest` as new Go types (there is no existing wire format to reuse), then wire them to the existing `InferenceService`/`PrecompileBackend` for execution, `AttestationService` for signed receipts, `governance.Committee`/`training.TrainingProver` for provenance, and `coord.AgentRegistry`/`coprocessor` provider registries for the provider/scheduler side. All of that is new integration work, not a flag you flip.
