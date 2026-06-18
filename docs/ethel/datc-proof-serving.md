# DATC Historical-Proof Serving — cost model & rate-limiting spec

Status: design (2026-06). Implementation spec for the future historical-proof
RPC. DATC is positioned as **"archive plus"**: geth/reth/erigon archive nodes do
NOT serve full-history EIP-1186 proofs by default; DATC does. Today only the
`n42-datc proof` CLI exists (no RPC) — so there is no live exposure yet. This
doc is the spec to follow when the RPC is built, AFTER the full build is
generated and accepted (lands together with the segment format,
[[datc-segment-format]]).

## 1. How one historical proof is computed (cost model)

`runProof → proofPath` (`cmd/n42-datc/proof.go:225`) has two phases:

**Phase A — node-record walk (top).** From the trie root, descend level by level
calling `branchSlotsAt` / `nodeHashAt`; each does an **as-of-N floor lookup** on
the temporal node records (`DatcAccNode` / `DatcStorNode`). Count ≈ trie depth
(account trie ~7–9 levels at 25M-block state; storage trie depth depends on the
contract). ~1–2 reads per level → ~10–20 reads.

**Phase B — leaf fold (bottom).** Where the node record stops being usable
(`!usable`), fold the subtree from the leaf history: `subtreeLeaves → asOfLeaves`
(`cmd/n42-datc/verify.go:757`) enumerates **every distinct key under the fold
path, as of N** (per key: `Seek(key|n+1)` then `Prev` → the floor version),
then builds an in-memory MPT subtree (`mptNodeRLP`) and keccaks every node.
Account leaves additionally call `nodeHashAt` for each account's storage root
(`verify.go:803`) — **one extra read per account leaf**.

Total ≈ `O(depth)` node reads + `O(foldKeys)` leaf reads + `O(foldKeys)` keccaks
+ `O(foldKeys)` transient memory.

## 2. Per-proof cost is extremely variable (the core fact)

| case | fold size | cost |
|------|-----------|------|
| EOA / small contract / recent N / shallow fold | a few leaves | ~20–40 reads + ~10 keccak → **sub-ms…tens of ms** (more if read-back hits disk) |
| big-contract storage proof (USDT/USDC-class, ≫10⁵ slots) / old N / fold shrinks | K distinct keys | **K×~2 reads + K keccak + O(K) memory** → **seconds…minutes + up to GBs transient** |

Two amplifiers:
- **Old N deepens the fold.** For historical N, fewer deep epochs are materialized
  in the node records, so `!usable` triggers higher up → a *larger* subtree is
  folded from leaves. **The older the queried block, the potentially heavier.**
- **Cost spread is 4–6 orders of magnitude** between a light and a heavy proof.
  ⇒ **QPS-only limiting is insufficient** — one heavy request equals tens of
  thousands of light ones.

## 3. Resource risk

- **Read-bound**, same substrate as the build. Proofs run on a read-only RoTx
  (MDBX MVCC snapshot) so they do **not block** the single writer, but they
  **compete for the same disk / page cache** — which the build already saturates
  (read-back is its bottleneck). Unbounded proof serving directly starves the
  build (and vice-versa).
- A handful of heavy proofs (big contract × old N) can saturate CPU, exhaust the
  page cache, and spike memory — a cheap DoS vector if unbounded.

## 4. Rate-limiting design (layered; the lever is concurrency + per-request budget, not QPS)

### 4.1 Concurrency cap — primary gate
A global semaphore: at most `maxConcurrent` proofs in flight (suggest 2–4;
**auto-drop to 1 while the build is active**). Independent of per-request cost,
this bounds total CPU/disk that proof serving can take. This is the main control
*because* per-request cost is unbounded-variable.

### 4.2 Per-request cost budget — mandatory runaway guard
Abort a single proof before it can hurt the box:
- `maxFoldLeaves`: if `asOfLeaves` would enumerate more than M distinct keys,
  **abort and return an error** (`proof too large — narrow the query: use a more
  recent block, or a contract/range with fewer live slots`) instead of OOM/stall.
  Enforce by counting in the `asOfLeaves` walk and bailing past the threshold.
- `maxFoldDepth` and a `perRequestTimeout` (e.g. 2–5 s): cut on either.
- This specifically tames the only tail (big-contract × old-N) that can take down
  the node.

### 4.3 Per-client QPS — secondary
Token bucket per client IP. Reuse an existing primitive:
`internal/cl/sentinel/handlers/rate_limiter.go` (token-bucket), or the
stateless-serve `TrustedProxies` + `jsonrpc.ClientIP` secure-by-default path
(commit 930eb708, [[project_stateless_serve_security]]) so a forged
`X-Forwarded-For` cannot bypass it.

### 4.4 Bounded queue + backpressure
Fixed-depth request queue; when full, return **429** rather than spawning
unbounded goroutines. Backpressure, not collapse.

### 4.5 Build coexistence
- Proofs use an independent read-only RoTx snapshot (MVCC) — never block the
  writer.
- While the build is active, drop `maxConcurrent` to 1 (or pause non-urgent
  proofs): the build is a finite batch job with an end; the proof RPC is a
  long-lived service. Letting the finite job finish first is the better global
  trade. Gate on the build's `progress`/running flag.

## 5. Config knobs (proposed)

```
proof.maxConcurrent      = 4      # auto → 1 while building
proof.maxFoldLeaves      = 50000  # exceed → reject ("proof too large")
proof.requestTimeoutSec  = 5
proof.perIPQPS           = 5
proof.queueDepth         = 64     # full → 429
proof.pauseWhileBuilding = true
```

## 6. Error semantics

| condition                         | response |
|-----------------------------------|----------|
| fold exceeds `maxFoldLeaves`/depth| 400 `proof too large` (deterministic; advise narrower query) |
| per-request timeout               | 504 / 503 `proof timeout` |
| queue full                        | 429 `busy, retry` |
| per-IP QPS exceeded               | 429 `rate limited` |
| block N beyond DATC head          | 400 `out of range` (recent blocks → serve from live node, §8) |

## 7. Wiring (when the RPC is built)

- A read-only handler holding the semaphore + token bucket + RoTx, calling the
  existing `querier.proofPath` (the CLI path) unchanged — only wrapped with the
  budget/limit envelope.
- Counters in the budget: thread a `leavesSeen` counter into `asOfLeaves` and
  return a typed `ErrProofTooLarge` past the threshold (small, localized change
  to the existing fold).
- Expose Prometheus: proof latency histogram, in-flight gauge, reject/timeout
  counters, fold-size histogram — to tune the knobs against real traffic.

## 8. Two-tier serving (recall)

DATC serves **historical** proofs (genesis → last update). **Recent/tip** proofs
should be served from the live node's current trie (`internal/mptproof`
`LatestProof`, on-demand) — far cheaper (hot state) and always fresh. The RPC
front routes `N ≥ checkpoint` to the live node and `N < checkpoint` to DATC. This
also keeps the heaviest DATC case (old N) off the hot path.

## 9. Interaction with the segment format

Under [[datc-segment-format]] each leaf/node read becomes: bloom probe → RecSplit
idx → zstd frame decode, scanning periods newest-first. The cost model is
unchanged in shape, but **old N scans more periods** — reinforcing §4.2 (per-request
budget) and the `key → latest-period` hint index (O(1) for tip, bounded for
historical). `maxFoldLeaves` also caps the number of period scans a single proof
can trigger.

## 10. Landing order

- **Now**: CLI only, no exposure — nothing to limit; the build runs undisturbed.
- **After full generation + acceptance**, when the proof RPC is built:
  §4.1 (concurrency cap), §4.2 (per-request budget), §4.5 (build coexistence) are
  **mandatory** (the cost tail can otherwise take down the node); §4.3/§4.4 are
  standard RPC hygiene. Ship them with the handler, not after.
