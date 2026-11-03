# Using the DDN

This is the companion usage guide to [`docs/DDN.md`](./DDN.md). Read that document first for the architecture and the implementation-status table.

The Go integration phases A–E are implemented as a disabled-by-default shadow service. The following sections also describe existing AI infrastructure and future whitepaper features; those are separate from the new off-chain DDN path.

## Native execution

The default backend is now repository-native `native-rules`. Native Bayes and Transformer training/inference are also implemented in Go. See [DDN_NATIVE.md](DDN_NATIVE.md) for source, local training, configuration and limits. Use `n42_ddnInfo` / MCP `ddn.info` to discover the active identity and ordered labels.

## Optional remote shadow DDN

Configure `AICfg.DDN` under the existing AI configuration. Enable both `enabled` and `gateway_enabled`, retain `shadow_mode: true`, and use a distributed-capable node profile. Example fields:

```yaml
ddn:
  backend: http
  enabled: true
  gateway_enabled: true
  shadow_mode: true
  sidecar_url: "http://sidecar.internal:8080/decide"
  provider_did: "did:n42:YOUR_LOWERCASE_PROVIDER_ADDRESS"
  model: "health-classifier"
  model_version: "v1"
  model_hash: "REPLACE_WITH_PINNED_0x_64_HEX_MODEL_HASH"
  model_family: "health-family-a"
  tasks: ["node.anomaly"]
  schemas: ["health-v1"]
  quorum_size: 1
  max_provider_concurrency: 2
  max_concurrency: 2
  queue_size: 32
  max_items: 1024
  max_input_bytes: 65536
  max_latency_ms: 500
  receipt_ttl_sec: 300
```

Replace identity/hash placeholders with the deployed provider's values. No model or remote sidecar is installed by enabling this configuration. The URL is the complete POST endpoint. Use HTTPS where transport confidentiality is required.

Submit through authenticated `n42_ddnSubmit` with parameters `[request, input]`; poll `n42_ddnGetReceipt` with `[request_id]`. These methods use the existing authenticated RPC or local IPC registration. They are not registered on public HTTP RPC. MCP equivalents are `ddn.decide` and `ddn.getReceipt`; add them to the existing server's `AllowedTools` list when an allowlist is configured.

Construct requests with `internal/ddn/types.DecisionRequest`: version 1, actual chain ID, unique requester/nonce, task/schema matching the provider, `privacy_mode: "public"`, configured quorum, positive latency budget, decimal-string `max_cost`, and a millisecond deadline in the next 24 hours. `input_hash` must be Keccak256 of the exact inline UTF-8 input. A zero request ID lets the gateway derive it. Input locations and private delivery are not fetched or supported. Secret-bearing inputs are rejected; redact before hashing and submission. Unsigned requests rely on RPC/MCP access control and do not authorize payments.

Records report `pending`, `complete`, or `failed`, with `shadow_mode` and `need_escalation`. Failed/time-out requests have no receipt and require escalation. Completed receipts expire at the earlier of their configured TTL and request deadline; consumers must verify expiry even if the cache still contains the record. Admission and nonce caches are bounded and process-local.

The sidecar receives JSON `{"request": <request>, "input": "sanitized text"}` and returns:

```json
{"request_id":"0x...","model_hash":"0x...","result":{"label":"NORMAL","probabilities_ppm":[],"confidence_ppm":0,"need_escalation":false,"answers":[]}}
```

Return the exact request ID and pinned model hash. Ppm values are integers; nonempty probability arrays sum to 1000000. Empty arrays and zero confidence honestly represent unavailable scores. Unknown fields, invalid results, oversized responses, redirects and late responses are rejected. Logs contain identifiers and receipt hashes rather than raw input or output labels.

For multiple providers, set `quorum_size` equal to the length of `sidecars`. Each entry has `url`, `provider_did`, `model`, `model_version`, `model_hash`, `model_family`, `tasks`, `schemas`, and integer `price`. The top-level provider DID identifies the aggregator. The group exposes its composite model identity through `quorum.Group.Identity`; requests pin that identity when model requirements are used. A shared `max_provider_concurrency` bound controls fan-out. The entire quorum must agree on typed outputs and distributions; confidence is the minimum and escalation is ORed. Splits, missing/invalid responses and multi-provider groups with fewer than two model families yield UNKNOWN/escalation. An evidence commitment binds the result set. Provider identity/model family are configured claims, not independently verified model execution.

Optional `signing_key_file` must point to an encrypted Web3 keystore file inside a dedicated `ddn-keystore/` directory with mode 0600. Supply its password in `N42_DDN_KEY_PASSWORD`; the provider/aggregator DID must match its lowercase signing address. No validator or wallet key is borrowed. Use `receipt.Verify` with the trusted address and originating request, then `verify.Verifier.Consume` for replay rejection. Settlement needs durable nonce storage. `require_registered_provider` optionally checks the existing coprocessor registry; `min_provider_reputation` sets its threshold. The reusable scheduler also enforces capability, task/schema, model, ETA and total cost.

Governance EIP-712 Quote/ResultAttestation compatibility is provided as a separate library format with Rust-derived test vectors. It does not activate DecisionHub settlement. Private encrypted transport, TEE/ZKML proof, automatic model registration and trained N42-System1 weights remain future work.

## Audiences

- **App/agent developers**: want to call AI inference from a smart contract or MCP client today.
- **Decision providers** (whitepaper role: nodes that run a model and answer requests): remote HTTP providers can serve the shadow path; full model registration remains planned — see [How a provider will run](#how-a-provider-will-run-planned).
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

Messaging-layer config relevant to a future DDN (DID identity, E2E encryption used as a private delivery channel) lives in `conf/messaging_config.go` under `MessagingCfg` — notably `DIDEnabled` (default `false`) and `EncryptionEnabled` (default `false`). Neither field is DDN-specific; they are generic messaging-platform switches. `AICfg.DDN` configures native or optional HTTP shadow execution; messaging encryption is not wired into DDN private delivery.

## How an agent obtains a decision today

The remote DDN round trip is documented above. The following generic inference precompile and result cache are separate existing AI infrastructure.

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

None of these three tool files expose a "submit decision request" or "get decision receipt" tool — the separate `internal/ddn/gateway/mcp_tools.go` provides those tools.

## How to register a model / dataset governance / training proof today

These three packages are real, usable Go APIs independent of any DDN wiring. They are good building blocks for a DDN Model Manifest's "has this model's training data been ethically approved" and "was this model actually trained the way it claims" questions — but these packages do not automatically populate or validate the new DDN `ModelManifest`.

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

The whitepaper's `Decision Provider` role — a node that registers a Model Manifest, listens for `DecisionRequest`s matching its capabilities, and returns signed `DecisionReceipt`s — is partially implemented by the remote provider interface and gateway. This repository does not ship a model-serving daemon or automatic manifest registration RPC. The closest adjacent concepts that do exist (and could be extended) are:

- `internal/ai/coord/registry.go`'s `AgentRegistry` — generic agent capability registration with min-stake (currently a package-level `var minStake = 1 ETH`, not the whitepaper's provider stake specifically).
- `internal/distributed/coprocessor/provider.go` and `marketplace.go` — a generic distributed-compute provider registry and reverse-auction marketplace (stake, capabilities, reputation tracking) built for the broader coprocessor system, not DDN decisions specifically.

Building a real Decision Provider would mean wiring a process that: registers via something like `AgentRegistry`, exposes an inference endpoint (perhaps reusing `InferenceService`/`PrecompileBackend`), and signs receipts — none of which exists as a single runnable binary today.

## Shadow-gateway mode

`n42-26/docs/decision/shadow-gateway.md` describes a **Rust/Python implementation in `n42-26`, not part of this Go repository.** Summary, attributed to that document: a read-only gateway (`scripts/decision_shadow.py`) that accepts one sanitized JSONL event per line, records a deterministic rule result alongside a shadow Jev classification, and cannot write node config, call N42 RPC, sign, submit, restart, or ban anything — critical deterministic alerts cannot be downgraded by model output, and API errors/malformed answers route to "deep analysis" rather than a silent healthy result. The Go gateway now accepts explicitly submitted sanitized input and calls the configured HTTP sidecar. Automatic node/CI log ingestion and deterministic-rule comparison are not enabled.

## Verifying a receipt

General DDN receipt verification is implemented in `internal/ddn/receipt` and replay consumption in `internal/ddn/verify`, as described above. ZK/TEE execution verification remains future work. The separate `AttestationService.VerifyAttestation` (`internal/ai/attestation/service.go`) handles inference provenance and its pluggable training/proof interfaces; it is not the DDN request/receipt verifier.

## Operational notes

- **MCP server**: default port `8553` (`conf/mcp_config.go`, `DefaultMCPCfg().Port = 8553`). Exposes the agent/wallet/data tool surfaces listed above.
- **Message stream (SSE)**: default port `8554` (`conf/messaging_config.go`, `StreamServerPort: 8554`), part of the generic messaging platform, not DDN-specific — would be a plausible transport for pushing decision results to subscribers in a future DDN build.
- **DID identity**: `internal/distributed/messaging/identity/did.go` implements `did:n42:<address>`, generic to the messaging platform. DDN uses this DID form for configured provider and aggregator identities.
- **Keys**: wallet session keys (`internal/ai/wallet/account.go`), ethics-committee voter keys, training-proof signer keys, and attestation operator keys are all independent secp256k1 keys managed by their respective packages — optional DDN receipt signing uses its own dedicated encrypted keystore.
- **Do not enable `ChainConfig.AIInferenceTime` on a multi-validator network** until model registry/result state moves into consensus-synchronized storage (see the precompile file's header warning, §"AI inference precompile" above).

## Limitations and roadmap pointers

- The DDN shadow request/receipt, scheduler and quorum path exists; production settlement and model execution proof remain future work.
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
