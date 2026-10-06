# Repository-native DDN execution

The native path executes rules, classification, Transformer inference and
training in Go source. It has no Python process, external inference SDK,
cloud API, downloaded checkpoint or required HTTP sidecar. Existing DDN
admission, scheduling, quorum, signing and verification also remain Go code.
The optional HTTP backend is selected explicitly with `backend: http`.

## Node configuration

DDN remains disabled by default and shadow-only. Its default backend is now
`native-rules`. Under the existing `ai` configuration:

```yaml
ai:
  ddn:
    enabled: true
    gateway_enabled: true
    shadow_mode: true
    backend: native-rules
    provider_did: "did:n42:YOUR_LOWERCASE_PROVIDER_ADDRESS"
    quorum_size: 1
    max_provider_concurrency: 2
    max_concurrency: 2
    queue_size: 32
    max_items: 1024
    max_input_bytes: 65536
    max_latency_ms: 500
    receipt_ttl_sec: 300
```

Use a distributed-capable profile and replace the DID. The node needs no
sidecar URL or model hash to start native rules. Optional `model_hash` pins the
actual executable identity and causes configuration failure on mismatch.

`native-rules` supports `node.anomaly` / `health-v1`. Embedded consensus,
storage, network and execution error phrases produce explicit labels with
escalation and zero calibrated confidence. Conflicting categories and unmatched
input produce UNKNOWN/escalation. Absence of a recognized error never establishes
NORMAL. Matching uses token boundaries rather than substring fragments.

For learned classification, select `backend: native-bayes` or
`backend: native-transformer` and set `model_file` to a local artifact.
Relative paths resolve under the node data directory. Task, schema, version,
labels and model identity come from the validated artifact; configuration cannot
invent a model family. Health-task execution gives recognized rule alarms
priority, including conflicts, so model output cannot downgrade those alarms.

Use authenticated `n42_ddnInfo` (no arguments) or allowlisted MCP `ddn.info` to
read the actual provider identity, quorum and ordered labels. Submit and poll
with `n42_ddnSubmit` / `n42_ddnGetReceipt` or MCP `ddn.decide` / `ddn.getReceipt`
as described in [DDN_USAGE.md](DDN_USAGE.md). Ppm distributions use the advertised
label order. Learned outputs also contain one `kind: 1` choice answer with that
same order; abstention/escalation must be handled before consuming an answer.

For a native quorum, use the existing `sidecars` member array (the name is kept
for compatibility), with each member specifying `backend`, `model_file` when
needed, `provider_did`, optional pinned `model_hash` and integer `price`.
`quorum_size` must equal its length. Members can use different native backends;
repeated Bayes models count as one family, as do repeated Transformers.
The existing all-members agreement and diversity rules still apply. Different
algorithm families are not proof of independent errors or decision correctness.

Optional encrypted DDN keystores, receipt verification and registry eligibility
checks work identically for native providers. RPC/MCP registration uses existing
listeners. Decisions cannot mutate node configuration, consensus or settlement.

## Local training and execution

Build the repository-owned CLI:

```sh
make ddn-model
```

Training input is UTF-8 JSONL, one supervised example per line:

```jsonl
{"label":"NORMAL","text":"healthy synchronized peers connected"}
{"label":"STORAGE","text":"database corrupted disk full"}
```

Use a reviewed, representative labelled dataset; this two-line example explains
the file format and does not establish classifier quality. No datasets or model
weights are generated or fetched automatically at startup.

Train multinomial naive Bayes from scratch:

```sh
build/bin/ddn-model train -algorithm bayes -data events.jsonl -model health-nb.json \
  -task node.anomaly -schema health-v1 -confidence-ppm 800000 -coverage-ppm 500000
build/bin/ddn-model inspect -algorithm bayes -model health-nb.json
build/bin/ddn-model predict -algorithm bayes -model health-nb.json -input 'disk full'
```

The implementation learns integer document/token counts with Laplace smoothing,
Unicode word tokenization, log-space inference and normalized ppm distributions.
Insufficient vocabulary coverage returns UNKNOWN; low posterior confidence
returns ABSTAIN. Its hash is stable for identical training statistics. These
posterior estimates require independent calibration on the target workload.

Train the native Transformer from random initialization:

```sh
build/bin/ddn-model train -algorithm transformer -data events.jsonl -model health-tf.json \
  -task node.anomaly -schema health-v1 -hidden 32 -heads 4 -layers 2 \
  -feed-forward 64 -sequence 128 -epochs 20 -learning-rate 0.001 -seed 42
build/bin/ddn-model inspect -algorithm transformer -model health-tf.json
build/bin/ddn-model predict -algorithm transformer -model health-tf.json -input 'disk full'
```

This is a small bidirectional pre-norm Transformer encoder implemented here,
not a ModernBERT checkpoint or an implementation claiming ModernBERT compatibility.
It includes UTF-8 byte embeddings plus a CLS token, learned positions,
scaled dot-product multihead attention, residuals, LayerNorm, GELU feed-forward
blocks, final norm, mean pooling and a supervised classification head.
Byte tokenization requires no third-party vocabulary. Maximum sequence length
includes CLS; overlong input fails explicitly rather than being truncated.

Training performs full reverse-mode differentiation through embeddings, attention,
feed-forward blocks, normalization and the classifier. The optimizer is AdamW
with gradient clipping and cross-entropy loss. Initialization is seeded;
training order is fixed. The CLI reports initial/final training loss and step
count; training loss is not a substitute for held-out accuracy or calibration.
Training is an offline CLI/library operation, outside node request workers.

## Repository-owned provider on a separate machine

`cmd/ddn-provider` serves the same Go-native backends with the existing sidecar
protocol. It has no cloud/model SDK and accepts native backends only. Build and
start it on the independent provider machine:

```sh
make ddn-provider
# Set N42_DDN_PROVIDER_TOKEN securely in the process environment.
build/bin/ddn-provider -listen 10.0.0.20:8080 -chain-id 94 \
  -did did:n42:YOUR_PROVIDER_ADDRESS -backend native-transformer -model health-tf.json
```

Rules-only service uses `-backend native-rules` and needs no model file. The
listener defaults to loopback; binding a non-loopback address requires a token.
Tokens are read from the environment, not flags or model files. Requests need
`Authorization: Bearer <token>`. Protect remote transport with HTTPS termination
or use a trusted private network; bearer authentication itself does not encrypt
traffic. Header/body/time/concurrency limits and deadlines apply on the server.
Overload returns 429, invalid bindings 400, and timeout 504 without a fabricated
result. Authenticated `GET /info` returns actual identity and ordered labels.

On the node, explicitly select this repository-owned remote service:

```yaml
backend: http
sidecar_url: "http://10.0.0.20:8080/decide"
sidecar_token_env: N42_DDN_PROVIDER_TOKEN
provider_did: "did:n42:YOUR_PROVIDER_ADDRESS"
model_hash: "REPLACE_WITH_ACTUAL_SERVED_MODEL_HASH"
model_family: native-byte-transformer
tasks: [node.anomaly]
schemas: [health-v1]
```

Pin the served hash from CLI inspect or `/info`. Per-quorum member `token_env`
can select a different credential; otherwise the top-level environment name is
used. An explicitly configured missing token fails startup validation. The HTTP
client never redirects or puts credentials in its URL. The server rechecks chain,
request/input hashes, deadline, schema, input redaction and model binding, then
executes the local source implementation. Node/aggregator receipt signing remains
separate from the provider daemon; remote outputs are model identity claims rather
than independently signed execution proofs.

## Model integrity and resource bounds

Artifacts are strict, size-limited JSON. Bayes stores integer statistics;
Transformer stores finite bounded floating point weights with fixed parameter
names/order and exact tensor shapes. No arbitrary executable model code is loaded.
Model hashing uses compact Go JSON of validated artifact fields with default
HTML escaping. These artifacts are distinct from canonical signed DDN wire
objects, which contain only integer ppm outputs and no floating point fields.

For health tasks, the served identity is:

```
Keccak256("N42-native-health-guard-v1" || artifact_hash || rules_hash)
```

This binds learned weights and the embedded guard version/rule set. For other
tasks it is the artifact hash. Rules-only identity is `RulesHash()`. CLI inspect
reports both artifact hash and served model hash. Hash pinning and signed receipts
bind identity claims; they do not prove that an untrusted machine ran those weights.

Bayes bounds: 16 MiB artifact, 32 labels, 8192 words, 65536 input bytes.
Transformer bounds: 32 MiB artifact, 32 labels, 128 hidden dimensions, 8 heads,
4 layers, 512 feed-forward dimensions, 256 sequence tokens, 1000000 parameters.
Configuration combinations must satisfy shape/divisibility checks. The CLI
accepts at most 10000 examples and 64 MiB of training JSONL. Gateway admission,
worker/cache limits, fan-out permits and deadlines apply to native execution too.
Models own their weights/statistics; inference calls own their graph state.

## Validation and remaining scope

Tests check numerical finite-difference gradients for every parameter group,
training loss reduction, parameter updates, repeatable training, artifact reload,
held-out toy strings, concurrent inference, cancellation, invalid shapes/weights,
thresholds, rule conflicts and a signed node/gateway round trip without HTTP.
The repository-owned HTTP service also has authentication, request binding, backpressure and deadline tests. Full short tests and targeted race/vet checks pass. These are implementation checks, not evidence of production anomaly recall.

Private delivery, automatic model registration, durable settlement, TEE/ZKML
execution proof and production model quality remain separate work. General
classification can serve locally trained governance-related labels, while the
existing governance EIP-712 library signs typed answers; no automatic on-chain
proposal action is enabled.

## Go System1 compatibility and evaluation

`native-system1` is a repository-owned Go backend for task `node.anomaly`
and schema `system1-v1`. It exposes the ordered labels NORMAL, NETWORK,
CONSENSUS, EXECUTION, STORAGE, CONFIGURATION, PERFORMANCE, UNKNOWN. Its three
signed answers are a health choice (HEALTHY, DEGRADED, CRITICAL), an eight-class
category choice, and a Noul escalation value (0 or 1000000). Rule confidence is
zero: one-hot distributions describe deterministic rule outputs and are not
calibrated model probabilities. The category follows n42-26's benchmark rules;
its independent health severity follows the critical/degraded phrase rules.
For example, `data unavailable` produces category NORMAL, severity CRITICAL,
and escalation YES. Consumers must check severity and escalation as well as
category. This schema differs from the five-domain shadow schema in n42-26.

Select `-backend native-system1` in `cmd/ddn-provider`; set the node DDN
backend and allowlisted schema to `native-system1` and `system1-v1` respectively.
Existing `native-rules` remains conservative and uses `health-v1`.
The health guard is now version 2 and additionally recognizes `qc mismatch`,
`consensus halted`, and `data unavailable`. Its rule hash and the execution hashes
of guarded learned models change; update model hash pins when upgrading.

The native Go collector/runner/scorer uses n42-26-compatible corpus and
prediction JSONL fields. Collection redacts secrets before UTF-8-safe truncation,
does not invent truth labels, and writes a new mode-0600 file. An adjudicated
corpus requires truth, YES/NO need_escalation and incident_id on every event.

```sh
go run ./cmd/ddn-benchmark -mode collect -input node.log -source node -prefix node1 -output events.jsonl
# Adjudicate events independently and create corpus.jsonl.
go run ./cmd/ddn-benchmark -mode rules -input corpus.jsonl -output predictions.jsonl
go run ./cmd/ddn-benchmark -mode score -input corpus.jsonl -predictions predictions.jsonl -output metrics.json
```

The scorer rejects duplicate/missing/extra IDs and invalid/nonfinite latencies.
It reports accuracy, per-class recall/FPR/FNR (null for zero denominators),
unknown/escalation rates, escalation accuracy and interpolated p50/p95/p99.
It does not currently report CPU/RAM/cost or split incident groups. Keep incident
groups separate when constructing training and holdout sets.

Go governance answer validation now supports Choice (1), Score (2), and Noul (3),
including floor-quantized distributions and multi-level scores up to 9000000 ppm.
`types.ValidateGovernanceAnswers` validates frozen DecisionHub template bounds
and computes the review decision. Callers must authenticate and bind the template
through the policy hash; this helper does not submit or settle transactions.

Compatibility evidence: 12 cases generated by n42-26's actual rule implementation
matched Go classifications, escalation and quality metrics. Existing cross-language
EIP-712 vectors remain covered. These are regression checks, not a calibrated
real-world quality benchmark. Go still does not import GLiClass/ModernBERT weights
or tokenizers, and does not provide n42-26's full DecisionHub/ProposalRouter,
relay/watch, and TypeScript wallet workflow. Full capability parity is not claimed.
