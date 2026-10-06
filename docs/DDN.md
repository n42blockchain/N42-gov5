# N42 Distributed Decision Network (DDN)

## Status at a glance

**Everything under "Specification" below describes the DDN protocol as defined in the N42 Distributed Decision Network Whitepaper v0.1 (`n42-26/docs/N42_Distributed_Decision_Network_Whitepaper_v0.1_EN.md`) and the design notes under `n42-26/docs/decision/`. That whitepaper and those design notes live in a different codebase (`n42-26`, a Rust/TypeScript workspace) and describe work that is, at most, prototype-stage there.**

**In this repository (N42-gov5, Go), integration phases A–E now implement an opt-in, off-chain shadow DDN:** canonical request/receipt/manifest types, native rules/Bayes/Transformer and optional HTTP providers, a bounded gateway, receipt signing/verification, registry-backed scheduling, and conservative quorum aggregation. See [usage](./DDN_USAGE.md), [canonical encoding](./DDN_CANONICAL.md), and [execution record](./DDN_INTEGRATION_PLAN.md). Native supervised model training is implemented; production settlement, private delivery and execution proofs remain future work. See [native execution](./DDN_NATIVE.md).

What this repository does have is a set of AI infrastructure building blocks — an agent wallet, agent discovery/negotiation, dataset governance, ZK training/inference attestation scaffolding, and an EVM precompile for inference requests — that are plausible foundations for a future DDN integration, and which this document maps explicitly in the [Integration map](#integration-map). Every claim about this repo below was checked against the listed file paths on 2026-10-05.

---

## 1. Purpose and scope

N42 DDN (whitepaper concept) targets the gap between:

- **Deterministic logic** (signatures, nonces, balances, state transitions, consensus) — must never depend on AI.
- **System-2 generative models** (Claude, GPT-class reasoning) — too slow/expensive for high-frequency, limited-answer-space decisions.

DDN proposes a middle **System-1 layer**: small, fast, typed classifiers (the `N42-System1` model family, built on a ModernBERT-style encoder) that answer bounded questions ("which queue?", "is this abnormal?", "escalate or not?") at millisecond latency, with calibrated confidence and the ability to abstain.

The core principle, verbatim from the whitepaper: **"Decision is not Consensus."** AI output is a probabilistic signal that can inform optimization, routing, or triage — never a correctness precondition.

This document (`docs/DDN.md`) describes the DDN *specification* and maps it against this repo's actual code. `docs/DDN_USAGE.md` covers how to use what exists today.

## 2. Architecture (Specification)

The whitepaper's end-to-end flow:

```text
DApp / Agent / N42 App
        |
        v
  DecisionRequest
        |
        v
N42 Decision Gateway -- deterministic pre-check
        |
        v
  Decision Scheduler
   /      |       \
  v       v        v
N42-System1  Jev   Other Providers
   \       |       /
    v      v      v
  Decision Aggregator
        |
  disagreement? --yes--> more nodes / heterogeneous models / System-2 / human review
        | no
        v
  DecisionReceipt
        |
  +-----+-----+
  v     v     v
Execute Audit Settlement
```

Key architectural points from the whitepaper:

- A model is just one implementation of a `DecisionProvider` — N42-System1, Jev (TypeSafe AI's cloud System-1 model), GLiClass, or any other classifier can plug in behind the same interface.
- The Scheduler picks providers by task capability, model compatibility, latency/reliability history, price, privacy mode, hardware, geography, reputation, and requested quorum size.
- The Aggregator combines multiple provider outputs into one `DecisionReceipt`, escalating to more providers, heterogeneous model families, System-2, or a human on disagreement — never silently picking a majority vote.
- Everything in this flow sits outside the consensus-critical path (see §6).

## 3. Data structures and protocols (Specification — whitepaper v0.1; Go v1 details in DDN_CANONICAL.md)

### DecisionRequest

```text
DecisionRequest {
    version, request_id
    task, schema_id
    input_hash, input_location?
    policy_hash, policy_parameters
    model_requirements?
    privacy_mode
    quorum, max_latency, max_cost
    deadline, nonce
    requester, signature
}
```

`task` values given as examples: `agent.route`, `node.anomaly`, `wallet.risk`, `content.classify`, `spam.detect`, `iot.event`. `input_hash` lets a receipt bind to a specific input without the raw input going on-chain.

### DecisionReceipt

```text
DecisionReceipt {
    version, request_id
    provider_did
    model, model_version, model_hash
    policy_hash, input_hash
    result, probability_distribution, confidence
    started_at, completed_at, latency
    expiry, nonce
    evidence_commitment?
    provider_signature
}
```

`model_hash` ties the receipt to a specific **Model Manifest** (architecture, weights hash, tokenizer hash, quantization, training-data manifest, calibration version, runtime version) rather than a free-text model name string.

### ModelManifest / Model Registration

```text
ModelManifest {
    model_id, family, version
    weights_hash, tokenizer_hash
    supported_tasks[], supported_schemas[]
    precision, context_limit
    calibration_manifest
    hardware_profile
    provider, timestamp, signature
}
```

Registration proves only "the network knows which model a node claims to run," not that the model is correct. TEE attestation and ZKML are listed as advanced-verification options still in prototype/feasibility testing on the whitepaper side.

Go v1 types exist in `internal/ddn/types`; their exact field names and hashing rules are documented in DDN_CANONICAL.md. Model registration remains future work.

## 4. N42-System1 model (Specification, ~1 page summary)

- **Baseline**: ModernBERT (encoder-only, ~149M params Base / ~395M Large, 8192-token context, RoPE + GeGLU + unpadding + Flash Attention + local/global alternating attention).
- **Input**: structured blockchain state serialized into normalized text blocks (`[CHAIN]`, `[TX]`, `[NODE]`, `[EXEC]`, `[POLICY]` sections) via "schema-aware serialization," so different nodes derive identical inputs from identical state.
- **Attention**: a blockchain-aware variant of ModernBERT's local/global alternation, grouping local attention by transaction/contract/peer/time-window/execution-batch, with periodic global attention over network/historical/policy/cross-component signals.
- **Output heads**: a shared encoder feeds multiple typed heads simultaneously — e.g., fault classification (NETWORK/EXEC/STORE), risk level (HIGH/MED/LOW), routing (RPC/TOOL/LLM), plus severity, abstention, need_more_data, need_system2 heads.
- **Calibration & abstention**: outputs are full probability distributions over typed labels, not single tokens, and the model can emit `ABSTAIN`. Tiered policy: `confidence < T1` → more providers, `confidence < T2` → System-2, high-risk task → mandatory human confirmation. Thresholds must be calibrated on N42's own data, not vendor marketing numbers.
- **Tiers**: Edge (mobile/IoT, quantized INT8/Q4), Node (PC/ordinary nodes), Validator (high-performance, long context, multi-task).

None of N42-System1's architecture, weights, training pipeline, or inference server exist in this Go repository. The closest functional analogs that do exist here are the inference precompile and WASM executor described in §7's integration map — they can host *any* model's forward pass, but no N42-System1 checkpoint or ModernBERT-derived encoder ships in this repo.

## 5. Trust and verification model

The whitepaper defines four verification layers, from weakest to strongest guarantee:

1. **Protocol verification** — are Request/Receipt fields, signatures, nonce, deadline, and hashes well-formed? (Deterministic, cheap.)
2. **Execution identity verification** — which provider claims to have run which Model Manifest? (A claim, not proof of correctness.)
3. **Multi-node result consistency** — do independent providers agree closely enough (Decision Quorum, §below)?
4. **Advanced computation proofs** — TEE remote attestation / ZKML. Even these only prove *"this model performed this computation on this input,"* never *"this decision is correct in the real world."*

**What is never verified, by design**: real-world correctness of an AI judgment. The whitepaper is explicit that three independent models agreeing is not proof of correctness — DDN must reject that assumption by construction (heterogeneous model-family quorum, not majority vote of correlated models).

**Why AI must stay off the consensus-critical path** (whitepaper §31, their strongest constraint): non-determinism, network latency, provider outage, model upgrades, cross-node output divergence, and adversarial inputs would all split consensus if AI output gated validity. N42 consensus must function with every model provider completely offline. AI may optimize (e.g., a Block-STM conflict-prediction hint, §32) but must never decide validity.

**In this repository**, this principle is already honored structurally: the AI inference precompile (`internal/vm/contracts_ai_inference.go`) carries an explicit code comment warning that its model registry, CAS, and execution state are node-local and NOT synchronized by consensus, and that `ChainConfig.AIInferenceTime` must not be set on a multi-validator network until that changes (see file header comment). This is the same "AI off the critical path" boundary the whitepaper describes, independently enforced in code that predates any DDN integration.

## 6. Privacy (Specification)

By default, only hashes go on-chain: `input_hash`, `policy_hash`, `DecisionReceipt`, settlement — never the raw prompt, private logs, personal information, or documents. Supported/validated input-delivery modes: direct local inference, end-to-end encrypted delivery to a designated provider, or storage in an access-controlled data layer. TEE confidential inference, ZK proofs, private retrieval, and encrypted model execution are listed as optional, still-prototype extensions.

This repo's E2E encryption stack (`internal/distributed/messaging/crypto/`) and DID identity (`internal/distributed/messaging/identity/`) are generic messaging-layer primitives, not DDN-specific, but are plausible building blocks for the "end-to-end encrypted delivery to a designated provider" mode (see integration map).

## 7. Economics and reputation (Specification)

**Economics**: a task may carry request/inference/aggregation/verification/data/settlement/challenge fees; a requester sets `max_cost` and the Scheduler fits the quorum/provider mix within it. v0.1 explicitly does **not** define reward ratios, inflation, or node yield — those are deferred to a later economic proposal.

**Reputation**: must not simply reward agreement with the majority (that converges to collective error). Inputs: availability, latency, protocol correctness, task completion, calibration error (Brier score, ECE), accuracy/precision/recall once ground truth is known, dispute history, model diversity. Reputation is kept **per task type** — a good spam classifier is not assumed to be a good financial-risk model.

**In this repo**, `internal/ai/coord/reputation.go` implements a general-purpose `ReputationSystem` (completion rate 40%, non-dispute rate 30%, response time 20%, stake 10%, with decay) for AI agents in the coordination/negotiation layer — conceptually adjacent but not task-partitioned the way the whitepaper specifies, and not wired to any DDN-specific calibration metric (Brier/ECE). See the integration map for exact status.

## 8. Integration map

| DDN component (whitepaper) | N42-gov5 package / feature | Status | File path(s) |
|---|---|---|---|
| DecisionRequest / DecisionReceipt / ModelManifest wire types | Canonical JSON + Keccak256 | **Implemented** | `internal/ddn/types/` |
| Decision Gateway, Scheduler, Aggregator, Quorum | Bounded shadow gateway, coprocessor scheduler, conservative fan-out | **Implemented**, disabled by default | `internal/ddn/{gateway,scheduler,aggregate,quorum}/` |
| N42-System1 model (ModernBERT-derived encoder, typed heads) | — | **Planned** (no code) | n/a |
| Decision Provider abstraction (model-agnostic) | Native rules/Bayes/Transformer, optional HTTP, registry adapter | **Implemented** | `internal/ddn/provider/` |
| Generic AI inference request/response path on-chain | AI inference precompile at `0x0301`: `requestInference`/`getResult`/`getModel`/`listModels` | **Implemented** (precompile dispatch + gas metering); backend wiring is node-local, not consensus-synchronized (see file header warning) | `internal/vm/contracts_ai_inference.go` |
| Inference result caching for repeated/verified queries | `ResultCache` (LRU + TTL, keyed by request hash) | **Implemented** | `internal/distributed/compute/inference/cache.go` |
| Inference execution backend | WASM-based executor (fuel-metered, wazero) + `InferenceBackend` wired to the precompile | **Implemented** | `internal/distributed/compute/inference/executor_wazero.go`, `executor_wasm_stub.go`, `precompile_backend.go`, `service.go`, `model.go` |
| Model registration / Model Manifest signing | — | **Planned** | n/a |
| Decision Provider identity (DID) | W3C DID v1.1, `did:n42:<address>` | **Implemented** (generic messaging-layer identity, not DDN-scoped) | `internal/distributed/messaging/identity/did.go`, `resolver.go` |
| Provider marketplace / staking / task negotiation | Agent discovery registry + task negotiation protocol | **Partial** — covers generic AI-agent task bidding (request/bid/accept/complete/dispute) with escrow and min-stake, not DDN decision tasks specifically | `internal/ai/coord/registry.go`, `negotiation.go` |
| Reputation (per-task-type, calibration-aware) | `ReputationSystem` (completion/dispute/response-time/stake weighted score + decay) | **Partial** — general agent reputation, not task-partitioned or calibration(Brier/ECE)-aware | `internal/ai/coord/reputation.go` |
| Agent wallet / spend limits for paying for decisions | `Account`, `SessionKey`, `SpendingPolicy` (Rate/Cap/Allowlist/Composite), `PaymasterService` | **Implemented** (generic AI-agent wallet, not DDN-specific settlement) | `internal/ai/wallet/account.go`, `policy.go`, `paymaster.go`, `service.go` |
| Model provenance / training-data governance (ethics committee) | `DatasetRegistry` + `Committee` (quorum/threshold secp256k1-signed voting) | **Implemented** (generic dataset governance, usable as the "approved training data" gate a Model Manifest would reference) | `internal/ai/governance/types.go`, `registry.go`, `committee.go` |
| ZK training verification (model authenticity) | `TrainingProver`/`TrainingVerifier`, 160-byte public inputs, `DatasetGovernance` interface | **Implemented** (structural + public-input validation; simulated proof, not a real ZK backend) | `internal/ai/training/types.go`, `prover.go`, `verifier.go` |
| ZK inference attestation (signed result + chain of custody) | `AttestationService`, `SignedAttestation`, `AttestationChain`, `SafetyLevel` | **Implemented** (structural scaffolding; depends on pluggable `ZKProofProvider`/`TrainingVerification` interfaces, not a production ZK backend) | `internal/ai/attestation/types.go`, `service.go` |
| ZKML proof generation/verification for a model forward pass | `ZKMLProver` (circuit from layer structure, 96-byte public inputs) / `ZKMLVerifier` | **Implemented** (structural validation + simulated proof; not a production ZK circuit) | `internal/zkprover/zkml.go`, `zkml_trace.go`; `internal/zkverifier/zkml_verifier.go` |
| Tiered verification (ZK default / Optimistic bond+challenge / TEE) | `TieredVerifier`, `OptimisticVerifier`, challenge manager, provider registry, marketplace, slashing | **Implemented** (generic distributed-compute verification tiers; TEE tier explicitly rejects everything until a real quote verifier is configured) | `internal/distributed/coprocessor/verification.go`, `challenge.go`, `provider.go`, `marketplace.go`, `slashing.go` |
| MCP tools for agent discovery / wallet / data queries | `AgentProvider`, `AgentWalletProvider` MCP tool interfaces | **Implemented** (generic MCP surface plus allowlisted `ddn.decide` / `ddn.getReceipt`) | `internal/mcp/agent_tools.go`, `agent_wallet_tools.go`, `data_tools.go` |
| Block-STM conflict-prediction hint from an async System-1 model | — | **Planned**; `internal/parallel/` implements Block-STM-style parallel execution itself (conflict detection, not AI hinting) | `internal/parallel/` (executor.go et al.) |
| PeerDAS peer-quality prediction hint | — | **Planned**; `internal/peerdas/` implements EIP-7594 custody/column sampling, no AI hinting layer | `internal/peerdas/` (custody.go, service.go, store.go) |
| On-chain-adjacent Jev integration (public-proposal classification) | — | **Not in this repo.** Exists only in `n42-26` as an untracked prototype (`DecisionHub.sol`, `ProposalRouter.sol`, `n42-decision-relay`) | n/a (see `n42-26/docs/decision/README.md`) |

## 9. Rollout phases (whitepaper §34, Phases 0–9) — cross-referenced against actual progress

**Important distinction**: the phase numbering below is the whitepaper's. A *separate*, more recent planning document in the same `n42-26` tree, `docs/decision/local-system1-roadmap.md` (dated 2026-09-26), supersedes the whitepaper's Jev-first phase ordering with a 9-phase **local-first** plan (deterministic rules → local System-1 → Jev on uncertain cases → System-2/human). **Both are `n42-26` (Rust/Python/TS) documents — neither phase list reflects work landed in this Go repository (N42-gov5).**

| Phase | Whitepaper description | Actual state (per `n42-26/docs/decision/roadmap-status-20260926.md`, a different codebase) |
|---|---|---|
| 0 | Dataset & Benchmark | Ongoing in `n42-26`; eight-class corpus/benchmark scripts exist there, no calibrated N42 corpus or real Jev comparison has run yet |
| 1 | Decision Gateway, shadow mode | `n42-26` has a read-only shadow prototype (`scripts/decision_shadow.py`); go/no-go gate (labelled real-event corpus, severe-recall comparison) not yet passed |
| 2 | N42-System1 v0 training | Not started per roadmap-status; local-first roadmap targets an independent local GLiClass provider first (Phase 2 of the *local-first* plan), not yet gated |
| 3 | N42-System1 v1 architecture | Not started |
| 4 | N42 App / Agent integration | Not started as a general router; a proposal-specific DApp/SDK exists only as untracked `n42-26` workspace files |
| 5 | Distributed Providers / protocol prototype | A single-provider, single-use-case prototype (`DecisionHub.sol`, `ProposalRouter.sol`, `n42-decision-relay`) exists in `n42-26`, untracked, not evidence that earlier gates passed |
| 6 | Decision Quorum / aggregation | Not started |
| 7 | Public DDN / controlled rollout | Not started |
| 8 | Edge network / device adaptation | Not started; N42-gov5 does have an unrelated, already-shipped mobile build path (`cmd/evmsdk/`, `make ios`/`make android`) that could host an edge model later, but no model ships today |
| 9 | Core optimization (PeerDAS/Block-STM hints) | Research-only per whitepaper; N42-gov5's `internal/parallel/` and `internal/peerdas/` are mature, production Block-STM and PeerDAS implementations, but carry no AI hinting layer |

The broader rollout above is distinct from the Go integration phases A–E. The Go shadow path now has packages and tests; this does not establish classifier calibration, production readiness, model training, or on-chain settlement.

## 10. Security threats (whitepaper §27)

| Threat | Defense direction (specification) |
|---|---|
| Sybil provider | stake / DID / reputation / cost |
| Multiple nodes running the same malicious model | model-family diversity requirement in quorum |
| Model version spoofing | Model Manifest hash binding |
| Replay | nonce + expiry + chain/domain separation |
| Prompt/state injection | schema separation + policy enforcement |
| Confidence manipulation | independent, per-provider calibration |
| Collusion | heterogeneous (cross-model-family) quorum |
| Data leakage | local inference / encryption |
| Provider withholding | deadline + rescheduling |
| Model poisoning | signed model registry + evaluation |
| Correlated AI failure | deterministic fallback / diversity |

The whitepaper's central warning: **"If three AIs agree, the answer must be correct" is a dangerous misconception DDN must reject from the start** — hence the emphasis on model-family (not just node) diversity in the quorum.

A separately relevant, already-documented repo constraint: this project's post-quantum Falcon verification path is currently forgeable and `PQPrecompilesTime` must not be enabled (see `docs/OPEN_ISSUES.md` and project memory `project-falcon-verify-forgeable.md`). That is unrelated to DDN but is the kind of "do not enable until X" gate the DDN threat table also calls for around model/provider trust.

## 11. Glossary

| Term | Meaning |
|---|---|
| DDN | Distributed Decision Network — the whitepaper's protocol/network for System-1 decisions |
| N42-System1 | The whitepaper's proposed model family (ModernBERT-derived) for fast typed decisions |
| Jev | TypeSafe AI's cloud "System One Model" — an external benchmark/optional provider, not a protocol dependency |
| DecisionRequest | Signed request for a typed decision, binding task/schema/input hash/policy/quorum |
| DecisionReceipt | Signed provider response binding result, probabilities, confidence, and model identity to a request |
| ModelManifest | Signed registration of a model's architecture/weights/tokenizer/calibration identity |
| Decision Quorum | Multiple independent (ideally cross-model-family) providers whose outputs are aggregated/compared |
| Heterogeneous Decision Quorum | A quorum explicitly requiring multiple distinct model families, not just multiple nodes |
| System-1 / System-2 | Fast typed classification vs. slow open-ended generation/reasoning |
| Decision Provider | Any entity (local model, cloud model, enterprise classifier) implementing the provider interface |
| GLiClass | An open-source zero-shot classification library used as a benchmark/candidate local provider in `n42-26` |

## 12. References

- Whitepaper: `/home/n42/src/n42/n42-26/docs/N42_Distributed_Decision_Network_Whitepaper_v0.1_EN.md`
- `/home/n42/src/n42/n42-26/docs/decision/README.md` (Jev testnet M1 prototype — DecisionHub/ProposalRouter/relay)
- `/home/n42/src/n42/n42-26/docs/decision/local-system1-roadmap.md` (supersedes the whitepaper's phase ordering with a local-first plan)
- `/home/n42/src/n42/n42-26/docs/decision/roadmap-status-20260926.md` (gate status snapshot)
- `/home/n42/src/n42/n42-26/docs/decision/shadow-gateway.md` (read-only shadow gateway spec)
- `/home/n42/src/n42/n42-26/docs/decision/system1-benchmark.md` (eight-class benchmark procedure)
- This repo (N42-gov5): `internal/vm/contracts_ai_inference.go`, `internal/distributed/compute/inference/cache.go`, `internal/ai/wallet/`, `internal/ai/coord/`, `internal/ai/governance/`, `internal/ai/training/`, `internal/ai/attestation/`, `internal/zkprover/zkml.go`, `internal/zkverifier/zkml_verifier.go`, `internal/distributed/coprocessor/`, `internal/distributed/messaging/identity/`, `internal/mcp/`, `conf/ai_config.go`, `conf/messaging_config.go`, `internal/peerdas/`, `internal/parallel/`
