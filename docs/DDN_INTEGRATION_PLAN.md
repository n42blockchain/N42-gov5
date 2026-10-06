# DDN Integration Plan (Go node)

Companion to `docs/DDN.md` (specification vs. this repo, status as of 2026-10-05)
and `docs/DDN_USAGE.md`. This document plans *how* to build DDN in Go, grounded
in what `n42-26` (the Rust/TS workspace) actually has, not only the whitepaper.

## 1. What n42-26 actually has

Checked 2026-10-05 against `/home/n42/src/n42/n42-26` (read-only). Two
unrelated things both match "decision": the HotStuff-2 `Decide` commit message
(`crates/n42-consensus/src/protocol/decision.rs` — consensus-finality vote
counting, nothing to do with DDN) and the AI decision work below.

| Piece | Path | What it is | Maturity | External deps |
|---|---|---|---|---|
| Whitepaper | `docs/N42_Distributed_Decision_Network_Whitepaper_v0.1_EN.md` | Full DDN spec: `DecisionRequest`/`DecisionReceipt`/`ModelManifest`, Scheduler/Aggregator, System-1 model family | Docs only | — |
| Roadmap | `docs/decision/local-system1-roadmap.md` (2026-09-26, supersedes the Jev-first ordering) | 9-phase plan; **DDN itself is Phase 9, P3, explicitly not started**: "Signed, nonce-bound, expiring provider receipts, replay checks, aggregation policy and settlement audit" | Docs only, not built | — |
| Shadow gateway (node/CI health) | `scripts/decision_shadow.py`, `scripts/decision_gateway.py`, `scripts/decision_benchmark.py`, `scripts/decision_candidates.py`, `scripts/decision_corpus.py`, `scripts/collect_decision_events.py`, `scripts/run_decision_provider.py`, `scripts/decision_providers.py` | Offline, read-only: converts log snapshots to JSONL events, routes through `rules -> local GLiClass -> Jev (cloud fallback) -> escalation`, benchmarks each provider against an adjudicated 8-class corpus (`NORMAL/NETWORK/CONSENSUS/EXECUTION/STORAGE/CONFIGURATION/PERFORMANCE/UNKNOWN` + `need_escalation`). Cannot write config, call RPC, sign, or restart anything. | Working prototype with unit tests (`scripts/test_decision_*.py`); CPU, pure Python, no CGO | Optional `TYPESAFE_API_KEY` for the Jev cloud call; local path needs only a pinned GLiClass model dir (HF `transformers`-class zero-shot classifier) |
| On-chain proposal relay (separate, older prototype) | `contracts/decision/DecisionHub.sol`, `contracts/decision/examples/ProposalRouter.sol`, `contracts/decision/test/DecisionHub.t.sol`, `bin/n42-decision-relay/{src/main.rs,src/lib.rs}` (1121 LOC), `sdk/decision-ts/`, `examples/decision-dapp/` | EVM contracts + a Rust relay binary (`quote`/`evaluate`/`submit`/`watch`) that classifies **public governance proposals** via the Jev cloud model and posts a signed, EIP-712 result on-chain. `docs/decision/local-system1-roadmap.md` explicitly calls this "a separate, untracked workspace prototype," not the production path. | Working for the governance use case; Foundry + cargo tests exist; **no account rate-limiting, no API cost accounting, no private inputs, single-process `watch` with no multi-instance leasing** (per `docs/decision/README.md`, "运行边界") | Foundry/forge, alloy (Rust EVM SDK), reqwest, Jev/TypeSafe cloud API, Node/viem for the example dApp |
| System-1 benchmark doc | `docs/decision/system1-benchmark.md` | Procedure for building the frozen corpus and scoring rules/GLiClass/Jev identically | Docs + supporting scripts above | — |
| Local-first roadmap status | `docs/decision/roadmap-status-20260926.md`, `docs/decision/dependency-upgrade-20261005.md` | Status notes / dependency bump log for the relay crate | Docs only | — |

**Reusable from n42-26 regardless of language**: the EIP-712 `Quote`/`ResultAttestation` domain
(`name: "N42Decision", version: "1"`) and digest fields from `bin/n42-decision-relay/src/lib.rs`:

- `QuoteFields { chain_id, hub, requester, refund_to, consumer, template_id, input_hash, deadline, signer_version, fee, quote_expiry }`
- `ResultFields { chain_id, hub, request_id, answer_hash, evidence_hash, model_hash, signer_version }`
- `answer_hash = keccak256(abi_encode(Answer[]))` where `Answer { kind: u8, selected, valuePpm, confidencePpm, probabilitiesPpm }`
- `DecisionTemplate { model: String, questions: [Choice{options}|Score{levels}|Noul] }`

This is a **different, narrower** wire format than the whitepaper's
`DecisionRequest`/`DecisionReceipt` — it is proposal-routing-specific (quote →
fee escrow → EIP-712 result), not a general multi-provider quorum protocol.
It is the only wire format n42-26 has actually shipped and tested, so it is
the natural interop target for anything that touches governance-proposal
routing; it is **not** a substitute for the general DDN receipt schema, which
remains whitepaper-only on both sides.

**Not present anywhere in n42-26**: a Scheduler, an Aggregator/quorum engine,
a `ModelManifest`/model-registration mechanism, TEE/ZKML-backed model
attestation, or any multi-provider general-purpose `DecisionRequest` flow.
ModernBERT/ONNX/candle/tch are absent — the only "local model" path is
GLiClass via Python/HF `transformers`, CPU-only, invoked from a script, not a
long-running service.

## 2. Existing Go pieces this plan binds to

| Go piece | Path | Interface | Role for DDN |
|---|---|---|---|
| `InferenceBackend` | `internal/vm/contracts_ai_inference.go:64` | `SubmitRequest(modelHash, inputCAS, tier, caller) -> requestID`; `GetResult(requestID) -> (status, outputCAS)` | Precompile `0x0301` selector surface; a DDN gateway can implement this to expose decisions on-chain without a new precompile |
| `ResultCache` | `internal/distributed/compute/inference/cache.go:41` | LRU+TTL `Put/Get/Delete/Prune`, keyed by `types.Hash` request ID | Cache for decision results pending receipt signing |
| `Verifier` (`TieredVerifier`) | `internal/distributed/coprocessor/verification.go:32` | `Verify(task, proofData, publicOutputs) (bool, error)`; ZK default, Optimistic (bond+challenge), TEE | Natural home for a future `DecisionProvider` verification tier, and for provider slashing (`slashing.go`) |
| Provider registry/marketplace | `internal/distributed/coprocessor/provider.go`, `marketplace.go` | stake, capability, reverse-auction bid/select | Reusable almost as-is for DDN provider discovery/selection — avoids rebuilding a scheduler from scratch |
| Attestation service | `internal/ai/attestation/service.go` | `SignedAttestation`, chain-of-custody, TTL+`Prune()` | Reusable for `DecisionReceipt` signing/verification pattern (secp256k1 via `crypto.SigToPub`, same as governance/training) |
| MCP tool registration | `internal/mcp/agent_tools.go`, `data_tools.go` | `s.registerAgentTools(provider)` pattern | Where `ddn.decide` / `ddn.getReceipt` tools get exposed to AI agents |
| DID resolver | `internal/distributed/messaging/identity/resolver.go` | LRU-cached resolve | `provider_did` field binding in a receipt |
| ExEx AI indexer | `internal/exex/extensions/ai_indexer.go` | token transfers, events, address profiles, gas | Data feed for `node.anomaly`/`wallet.risk` tasks (matches n42-26's health-classification use case, not governance) |
| AICfg | `conf/ai_config.go` | no `DDN` block today (`grep` confirms zero hits) | New `AICfg.DDN` config struct, same shape as `Wallet`/`Coord`/`MEV` sub-configs |

## 3. Target architecture

### 3.1 Package tree (new)

```
internal/ddn/
  types/       DecisionRequest, DecisionReceipt, ModelManifest, QuantizedAnswer — Go structs,
               JSON + RLP/compact codecs, hashing (keccak256 over canonical encodings)
  gateway/     deterministic pre-check, request validation, routing entry point
               (binds to vm.InferenceBackend optionally; MCP tools; JSON-RPC namespace n42_ddn*)
  scheduler/   provider selection (capability/latency/price/privacy/quorum) —
               thin wrapper over coprocessor's existing provider registry/marketplace
  provider/    DecisionProvider interface + adapters: local (rules/WASM), sidecar (HTTP/gRPC to
               a Python/GLiClass or Jev-style service), stub (for shadow mode / tests)
  quorum/      multi-provider fan-out + disagreement policy (escalate vs accept)
  aggregate/   combine provider outputs into one DecisionReceipt candidate
  receipt/     DecisionReceipt construction, signing via ai/attestation-style secp256k1,
               chain-of-custody linking to the originating DecisionRequest
  verify/      replay/nonce/expiry checks, receipt verification for on-chain or RPC consumers
```

### 3.2 Model runtime options (Phase B decision)

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| ONNX via a Go runtime (e.g. `onnxruntime_go` CGO binding) | Real local inference, matches "local-first" roadmap intent | New CGO dependency on a fleet that is already CPU-saturated (box claim / box-sharing rules in memory apply); no ONNX export of GLiClass exists in n42-26 today; adds a build-tag surface | Not for Phase 1 |
| WASM-hosted model via the existing wazero executor (`internal/distributed/compute/wasm/`, `inference/executor_wasm.go`) | Zero new CGO deps, reuses fuel-metered sandbox + model cache already built for `internal/distributed/compute/inference`, fits the "outside consensus, bounded resources" requirement | No classifier exists in WASM form yet (GLiClass is a PyTorch/HF model, not compiled to WASM); would require a real export/quantization effort before Phase 1 can show results | Phase 2+ target, not Phase 1 |
| External sidecar over HTTP/gRPC (process n42-26's own Python scripts, or any `DecisionProvider`-shaped service) | **Reuses n42-26's working code directly** (`decision_gateway.py`, `decision_providers.py`, GLiClass path) with zero Go-side model work; trivially shadow-mode (just don't route its output anywhere load-bearing); matches how this repo already treats inference as "a service behind `InferenceBackend`" | Extra process to deploy/monitor; network hop latency; sidecar crash must fail open, not block anything | **Recommended for Phase 1** |

Recommendation: start with the sidecar. It lets Phase B stand up a real,
end-to-end DDN gateway in Go calling the *actual* n42-26 Python provider
stack (rules → GLiClass → optional Jev) over a narrow HTTP contract, with
zero new CGO/WASM investment, and defers the "compile a classifier to WASM"
or "add ONNX CGO" questions until there is a Go-side consumer that needs them.

### 3.3 Binding map

| DDN concept | Binds to |
|---|---|
| `DecisionRequest` ingress | `internal/mcp` tool (`ddn.decide`) + new JSON-RPC namespace (e.g. `n42_ddnSubmit`/`n42_ddnGetReceipt` in `internal/api/`), optional precompile selector reusing `InferenceBackend`-shaped access at `0x0301` only if a task is explicitly an inference task; DDN itself does not need a new precompile for Phase 1-3 |
| `DecisionReceipt` signing | `internal/ai/attestation` pattern (secp256k1 sign/verify, TTL+Prune) — new `ddn/receipt` package mirrors it rather than importing it directly, to keep `ai/*` decoupled per the existing interface-decoupling convention |
| Provider discovery/selection | `internal/distributed/coprocessor/provider.go` + `marketplace.go` as-is; `ddn/scheduler` is a thin task-shape adapter, not a rewrite |
| Slashing for bad providers | `internal/distributed/coprocessor/slashing.go` (Verify-or-Slash) |
| Quorum/aggregation | New (`ddn/quorum`, `ddn/aggregate`) — nothing in n42-26 or this repo implements multi-provider disagreement policy today |
| Privacy mode | `internal/distributed/messaging` E2E crypto (X25519/XChaCha20-Poly1305) for payload privacy, `messaging/identity` DID for `provider_did` |
| Data feed (`node.anomaly`, `wallet.risk`) | `internal/exex/extensions/ai_indexer.go` for on-chain data; host metrics/log tailing for node/CI tasks (mirrors n42-26's `collect_decision_events.py` input shape) |
| Config | `conf/ai_config.go`: new `AICfg.DDN { Enabled, GatewayEnabled, SidecarURL, QuorumSize, MaxLatencyMs, ShadowMode bool, ... }`, same style as `Wallet`/`Coord`/`MEV` |

### 3.4 Wire-level compatibility with n42-26

Two separate compatibility surfaces, do not conflate them:

1. **General DDN `DecisionRequest`/`DecisionReceipt`** — whitepaper-only on
   both sides. No canonical hashing/signing scheme exists yet in either repo.
   Go types in `ddn/types` should therefore define their *own* canonical
   encoding (recommend: deterministic JSON field order + `keccak256` over
   UTF-8 bytes, matching the hashing primitive already used everywhere else
   in this repo — `common/crypto`, `Keccak256` for DIDs/messaging IDs) and
   publish it as the proposed interop format, since nothing in n42-26
   constrains this choice. **Flag**: this is the actual ambiguity — until a
   human picks a canonical encoding, a Go gateway and a future Rust DDN
   gateway cannot interoperate; Phase A's deliverable is exactly this choice
   plus test vectors.
2. **Proposal-routing EIP-712 format** (if the governance-proposal use case
   is in scope) — already fixed by n42-26's shipped code: domain
   `{name: "N42Decision", version: "1", chainId, verifyingContract: hub}`,
   `Quote`/`ResultAttestation` struct fields exactly as listed in §1. A Go
   signer/verifier for this format is a direct, low-ambiguity port (same
   field names, same EIP-712 typed-data hashing any Go EIP-712 lib or a hand
   rolled `common/crypto`-based implementation can reproduce) and should move
   to `ddn/receipt` as an optional compatibility shim, not the default
   general-purpose receipt format.

## 4. Shadow mode in Go

- Run the gateway as its own goroutine group with its own bounded worker
  pool (not the miner/consensus/txpool pool); cap it via a dedicated
  `GOMAXPROCS`-independent semaphore (`AICfg.DDN.MaxConcurrency`), since the
  box is CPU-saturated by the qs fleet — this must not steal cycles from
  block production or the exec wave (see `[Share the box's CPU under stress
  tests]` in memory). Treat it like `internal/mcp`'s existing server: opt-in,
  disabled by default, own port, own goroutines, started/stopped in
  `internal/node/node.go`'s "Distributed services" phase alongside
  `messagingService`.
- Data path: `ddn/gateway` subscribes to the ExEx AI indexer / host log
  tailer as a **read-only consumer** — it must never block block import or
  state commit; drop events under backpressure rather than apply backpressure
  upstream (same posture as n42-26's "cannot write node configuration... does
  not alter an existing alert").
- Kill switch: `AICfg.DDN.Enabled=false` (default) fully disables; even when
  enabled, `AICfg.DDN.ShadowMode=true` (default while shadow) means the
  gateway computes and logs/exports receipts but nothing consumes them for
  real routing/execution decisions.
- Metrics (follow the 250+ Prometheus metrics convention in
  `internal/metrics/`): request count, per-provider latency histogram,
  escalation rate, UNKNOWN rate, sidecar error rate, quorum disagreement
  rate. Logs: structured, one line per receipt with request_id/provider/
  model_hash/result/latency, no raw input text (mirror n42-26's redaction
  discipline in `decision_shadow.py`).

## 5. Phased delivery

| Phase | Scope | Files (new) | Interfaces | Tests | Acceptance | Size | Difficulty |
|---|---|---|---|---|---|---|---|
| A | Types + canonical hashing + test vectors | `internal/ddn/types/{request,receipt,manifest,hash}.go` | `DecisionRequest`, `DecisionReceipt`, `ModelManifest` structs + `CanonicalHash()` | Unit tests incl. fixed test vectors checked into `internal/ddn/types/testdata/` | Hash is stable across runs; vectors documented for a future Rust-side port | ~600 LOC | Easy — pure data types |
| B | Gateway + MCP/JSON-RPC in shadow mode, sidecar stub | `internal/ddn/gateway/{gateway,rpc,mcp_tools}.go`, `internal/ddn/provider/{interface,sidecar,stub}.go`, `conf/ai_config.go` DDN block, `internal/node/node.go` wiring | `DecisionProvider` interface, `ddn.decide` MCP tool, `n42_ddnSubmit`/`n42_ddnGetReceipt` RPC methods | Unit tests with `stub` provider; integration test spins up an HTTP stub server standing in for the sidecar | Shadow-mode gateway runs end-to-end against a stub with zero impact on existing test suite timings | ~1200 LOC | Medium — new RPC namespace + node wiring is the fiddly part |
| C | Receipts + attestation | `internal/ddn/receipt/{sign,verify,eip712_compat}.go` | Mirrors `ai/attestation.SignedAttestation` pattern; optional EIP-712 shim for proposal-routing compat | Unit tests for sign/verify round-trip, EIP-712 digest matches a vector computed by hand from n42-26's struct fields | Receipts verifiable without the signer present; EIP-712 shim produces byte-identical digests to the documented n42-26 field layout | ~500 LOC | Medium — EIP-712 digest correctness needs care |
| D | Providers via coprocessor | `internal/ddn/provider/coprocessor_adapter.go`, `scheduler/select.go` | Adapts `coprocessor.ProviderRegistry`/`Marketplace` to `DecisionProvider` selection | Unit tests with fake registry entries | Scheduler picks a provider by capability+reputation+price without duplicating coprocessor logic | ~400 LOC | Medium — adapter correctness against an existing, tested subsystem |
| E | Quorum/aggregation | `internal/ddn/quorum/{fanout,policy}.go`, `internal/ddn/aggregate/combine.go` | Multi-provider fan-out, disagreement policy (escalate on split, never silent majority-vote per whitepaper) | Unit tests covering agree/disagree/timeout/partial-failure matrices | Disagreement always escalates or surfaces `need_escalation=YES`, never silently resolved | ~700 LOC | **Hard — this is genuinely new logic, no reference implementation in either repo, and the whitepaper's "never silently majority-vote" rule needs careful test coverage of edge cases (ties, partial timeouts, single-provider quorum)** |

E2E-against-n42-26: only meaningful once a real sidecar exists (Phase B+).
`scripts/decision_gateway.py`/`decision_providers.py` can run standalone and
be hit over HTTP from a Go integration test — recommend adding that as a
Phase B stretch test, not a blocker, since it requires Python deps
(GLiClass model dir) not guaranteed available in CI.

## 6. Risks and open questions

1. **Model availability**: GLiClass needs a pinned local model directory;
   nothing in this repo or n42-26 vendors it. Phase 1 cannot demo a *real*
   local classifier without someone fetching and pinning that model — the
   sidecar can run rules-only in the meantime.
2. **Determinism across providers**: the whitepaper assumes providers can
   disagree and that's fine, but a `DecisionReceipt` that feeds any
   on-chain or precompile-adjacent path must not become a disguised
   consensus input. Any future wiring to `InferenceBackend`/precompile
   `0x0301` must re-affirm the "Decision is not Consensus" boundary already
   documented for the inference precompile.
3. **Gas/precompile exposure**: do **not** add a new precompile for DDN in
   Phases A-E; routing through MCP/RPC keeps it fully off the EVM gas/state
   path, matching this repo's existing AI-infra posture (wallet, coord,
   governance, training, attestation are all off-chain-adjacent, not
   precompiles, except inference itself).
4. **Security — prompt/feature injection**: sidecar inputs must be
   size-capped and redacted before leaving the node (n42-26's
   `decision_shadow.py` redaction regexes for API keys / hex secrets are a
   good starting point to port).
5. **Security — DoS**: shadow-mode gateway must drop under backpressure, not
   queue unboundedly; cap concurrent sidecar calls.
6. **Security — key handling**: receipt-signing keys are a new class of key
   material (separate from consensus/validator keys and wallet session
   keys); needs its own keystore scoping before Phase C ships signing for
   real.
7. **Canonical hash/encoding choice (§3.4, item 1)** is unresolved — this is
   the top blocker for any claim of n42-26 interop and should be decided by
   a human before Phase A locks its test vectors.

### Decisions a human must make before Phase B

1. Is the governance-proposal (EIP-712, on-chain `DecisionHub`/`ProposalRouter`)
   use case in scope for this Go integration at all, or is DDN here scoped
   only to the node/CI health-classification use case (the one with an
   actual working n42-26 prototype and a maintained roadmap)? They have
   different wire formats and different consumers.
2. Canonical encoding/hashing for the general `DecisionRequest`/
   `DecisionReceipt` (JSON+keccak256 as proposed above, or something else) —
   needed before Phase A test vectors are meaningful to anyone outside this
   repo.
3. Where does the model/sidecar actually run — colocated with the node
   process (shares the CPU-saturated box) or on a separate machine reachable
   over the network? This changes the shadow-mode CPU-isolation design in §4.

## Execution decisions and progress (2026-10-05)

The user confirmed JSON + Keccak256 canonical encoding, governance EIP-712
compatibility in scope, and a remote HTTP sidecar. Canonical v1 details and fixed
vectors are documented in `docs/DDN_CANONICAL.md`.

- Branch integration: main and qs/block-time-budget merged at `e2edebe0`,
  preserving applied-state evidence, durable voting and the newer deferred
  execution checks. Both remote branches advanced without rewriting history.
- Phase A: Go wire types, integer ppm answers, request validation, canonical
  hashing and fixed vectors implemented. `go test ./internal/ddn/types` passes.
- Merge validation: `make build` and targeted tests for txflood, HotStuff,
  ingest, QMDB, state/commitment, state, miner, API and internal passed.
  `make lint` cannot run: this environment has no golangci-lint binary.
