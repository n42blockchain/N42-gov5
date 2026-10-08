# AI Infrastructure

Moved out of CLAUDE.md on 2026-10-08. Agent wallet, discovery, inference precompile, ZKML, AI block building, and the three AI safety subsystems.

## Tests

```bash
# AI infrastructure tests (150 tests across 10 packages, all race-safe)
go test ./internal/ai/wallet/ ./internal/ai/coord/ -v    # Agent wallet + discovery (34 tests)
go test ./internal/vm/ -run "TestAIInference" -v        # AI inference precompile (6 tests)
go test ./internal/distributed/compute/inference/ -v    # Inference cache + executor (26 tests)
go test ./internal/exex/extensions/ -v                  # AI data indexer (6 tests)
go test ./internal/zkprover/ -run "TestZKML|TestExecution" -v  # ZKML prover + trace (9 tests)
go test ./internal/zkverifier/ -run "TestZKML" -v       # ZKML verifier (5 tests)
go test ./internal/mev/ -run "TestAI|TestGas" -v        # AI block optimizer (13 tests)
go test ./internal/ai/governance/ -v                      # Data governance (19 tests)
go test ./internal/ai/training/ -v                        # ZK training verification (17 tests)
go test ./internal/ai/attestation/ -v                     # Inference attestation (15 tests)
go test -race ./internal/ai/wallet/ ./internal/ai/coord/ ./internal/vm/ ./internal/distributed/compute/inference/ ./internal/exex/extensions/ ./internal/zkprover/ ./internal/zkverifier/ ./internal/mev/ ./internal/ai/... -count=1  # Race check
```

### AI Agent Infrastructure

`internal/ai/wallet/` and `internal/ai/coord/` provide a complete AI Agent platform with 3 subsystems:

**Agent Wallet** (`ai/wallet/account.go`, `policy.go`, `paymaster.go`, `service.go`):
- `Account`: deterministic address derivation from owner key + DID via Keccak256.
- `SessionKey`: time-limited keys with contract allowlists, method selectors, spend limits, gas caps. Max 16 per account.
- `SpendingPolicy` interface with `RatePolicy` (sliding window), `CapPolicy` (per-tx + daily), `AllowlistPolicy`, `CompositePolicy` (AND/OR).
- `PaymasterService`: deposit pool per owner, gas sponsorship with tagged paymaster data.
- Bundler integration: `AgentSessionValidator` interface in `bundler/validator.go` for session key validation in UserOps.

**Agent Discovery** (`ai/coord/registry.go`, `negotiation.go`, `reputation.go`):
- `AgentRegistry`: register agents with capabilities + stake, discover by capability (sorted by reputation).
- `TaskNegotiation`: full lifecycle — request → bid → accept → complete/dispute. Escrow on acceptance.
- `ReputationSystem`: weighted score (completion 40%, disputes 30%, response time 20%, stake 10%), decay factor.
- Messaging integration: `AgentDiscoveryTopic` and `AgentNegotiateTopic` in messaging service.

**AI Inference Precompile** (`vm/contracts_ai_inference.go`):
- Precompiled contract at `0x0301`, activated via `ChainConfig.AIInferenceTime`.
- Selectors: `0x00` requestInference, `0x01` getResult, `0x02` getModel, `0x03` listModels.
- Gas: base 10000 + 100/byte for requests; 2600 for queries; 5000 for listing.
- `InferenceBackend` interface decouples precompile from service implementation.

**ZKML Verification** (`zkprover/zkml.go`, `zkprover/zkml_trace.go`, `zkverifier/zkml_verifier.go`):
- `ZKMLProver`: generates circuits from model structure, produces proofs with 96-byte public inputs (modelHash + inputHash + outputHash).
- `ExecutionTrace`: captures per-layer intermediate values for ZK witness generation.
- `ZKMLVerifier`: validates proof structure and public input consistency.
- Coprocessor integration: `ZKMLVerifierAdapter` connects to `TieredVerifier` via `SetZKMLVerifier()`.

**AI Block Building** (`mev/ai_optimizer.go`, `mev/gas_predictor.go`):
- `AIBlockOptimizer`: scores transactions by effective tip × gas efficiency, stable-sorts preserving nonce order.
- `FairnessGuard`: detects sandwich attack patterns when fairness mode enabled.
- `DetectMEV`: identifies arbitrage (3+ same-contract txs) and liquidation (high-value) patterns.
- `GasPredictor`: EWMA-based prediction (alpha=0.3, window=32 blocks) for base fee and gas usage.
- Miner integration: `AIOptimizer` interface in `miner.go`, injected via `SetAIOptimizer()`.

**Configuration** (`conf/ai_config.go` — `AICfg`):

| Field | Default | Description |
|-------|---------|-------------|
| `AICfg.Wallet.Enabled` | `false` | Master switch for agent wallet service |
| `AICfg.Wallet.MaxSessionKeys` | `16` | Max session keys per account |
| `AICfg.Wallet.PaymasterEnabled` | `false` | Enable gas sponsorship |
| `AICfg.Coord.RegistryEnabled` | `false` | Enable agent discovery |
| `AICfg.Coord.MinAgentStake` | `0.1 ETH` | Minimum stake for registration |
| `AICfg.Coord.NegotiationTimeoutSec` | `300` | Task negotiation timeout |
| `AICfg.MEV.Enabled` | `false` | Enable AI block optimization |
| `AICfg.MEV.FairnessMode` | `true` | Enable sandwich detection |
| `AICfg.MEV.FallbackOnError` | `true` | Fall back to standard ordering |

### AI Safety Infrastructure

`internal/ai/governance/`, `internal/ai/training/`, and `internal/ai/attestation/` provide three modular, decoupled AI safety subsystems:

**Feature 1 — Training Data Governance** (`ai/governance/`):
- `DatasetRegistry`: registers training datasets with content hash (via CAS), owner, metadata. Links datasets to trained models for provenance.
- `Committee`: human ethics review committee with quorum/threshold voting. Members cast votes signed with secp256k1 (signature over `Keccak256(datasetID || decision || category)`). Voter address recovered via `crypto.SigToPub`.
- Categories: Fairness, Privacy, ContentSafety, Transparency. Dataset must pass all required categories.
- Lifecycle: `DatasetPending` → `DatasetUnderReview` (on first vote) → `DatasetApproved`/`DatasetRejected` (on finalize).

**Feature 2 — ZK Training Verification** (`ai/training/`):
- `TrainingProver`: generates ZK proofs that a model was trained from approved datasets with specific config/weights.
- `DatasetGovernance` interface: decouples from governance package. Checks `IsApproved(datasetID)` before allowing training registration.
- `TrainingTrace`: epoch-level checkpoints (weights before/after, loss, gradients) for ZK witness generation.
- `TrainingProof`: 160-byte public inputs = `modelHash(32) || initWeightsHash(32) || finalWeightsHash(32) || configHash(32) || datasetRootHash(32)`.
- `TrainingVerifier`: structural validation + public input consistency (mirrors `ZKMLVerifier` pattern).

**Feature 3 — ZK Inference Attestation** (`ai/attestation/`):
- `AttestationService`: creates signed attestations binding inference results to their full provenance chain (model, training, data governance).
- `SignedAttestation`: operator signs canonical attestation bytes with secp256k1. Signature verified via `crypto.SigToPub`.
- `AttestationChain`: validates multi-hop pipelines (e.g., perception → planning → control for autonomous driving). Enforces `output[i] == input[i+1]`.
- `SafetyLevel`: Standard (text/images), HighValue (financial/medical), Critical (autonomous driving, robotics). Critical level requires verified training record.
- TTL-based expiry with `Prune()` for memory management.

**Interface Decoupling:**
```
governance.Committee implements training.DatasetGovernance
training.TrainingProver → used by attestation.TrainingVerification
zkprover.ZKMLProver → used by attestation.ZKProofProvider
```

**Configuration** (`conf/ai_config.go` — `AICfg`):

| Field | Default | Description |
|-------|---------|-------------|
| `AICfg.Governance.Enabled` | `false` | Enable data governance |
| `AICfg.Governance.MaxDatasets` | `10000` | Max registered datasets |
| `AICfg.Governance.CommitteeQuorum` | `3` | Min votes for valid decision |
| `AICfg.Governance.CommitteeThreshold` | `0.67` | Approval ratio for pass |
| `AICfg.Training.Enabled` | `false` | Enable training ZK verification |
| `AICfg.Attestation.Enabled` | `false` | Enable inference attestation |
| `AICfg.Attestation.TTLSec` | `86400` | Attestation expiry (24h) |

