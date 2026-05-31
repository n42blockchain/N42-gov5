# IDC Stateless Production + Three-Mode Client Architecture

Status: design (2026-05-31). Builds on the validated stateless pipeline
(`internal/ethel/stateless`, `cmd/n42-stateless-anchor-produce`) — see
`stateless-verification.md` for the three trust layers and `mpt-proof-system.md`
for proof internals. This doc defines the **production/distribution** layer:
IDC producer nodes, an attack-hardened serving RPC, and the minimal / full /
archive client modes.

## 0. Roles at a glance

```
                 ┌───────────────────────── IDC producer cluster ─────────────────────────┐
   tip block ───▶│ exec node(s): per-block witness (12s) + every-1000 MPT anchor + attest │
                 │ serve RPC (rate-limited, authed, DDoS-guarded)                          │
                 └───────────────┬───────────────┬───────────────────────┬────────────────┘
                                 │               │                       │
                        headers+bodies     +witness (8KB/blk)     +witness +anchors
                                 ▼               ▼                       ▼
                              full            minimal                 archive
                       (genesis→tip,      (tip-1wk→tip,         (download all → execute
                        no state)          rolling, verify①②③)   genesis→tip → 12s live)
```

| | Producer (IDC) | minimal | full | archive |
|---|---|---|---|---|
| start | tip (live) | **tip** (latest) | **genesis** | download then **genesis** |
| stores | full state + trie + all artifacts | rolling **1 week** of {header,body,witness} + anchors | **genesis→tip** header+body | **full historical state** |
| produces | witness@12s, MPT@1000, attest | — | — | — |
| verifies | (authoritative) | ①header chain ②witness replay ③MPT@anchor | header chain | full re-execution |
| retention | full | auto-delete > 1 week | full header/body | full |

## 1. Producer (IDC cluster)

Multiple geographically-distinct IDC nodes, each a full eth-el exec node at the
tip. Per-block, in real time (≈12 s):

1. **Witness (layer ②), every block.** The exec path already records the
   per-block state-read witness (`internal/ethel/witness.go`
   `WitnessStateReader`, length-prefixed v1, ~7 KB compressed / ~37 KB raw at
   tip — see `cmd/n42-witness-size`). Emitted to the witness freezer +
   served live.
2. **MPT stateless anchor (layer ③), every K=1000 blocks.** Captured as a
   byproduct of the per-block changeset root computation
   (`commitment.ExtractMultiproof` over the K-block touched-key window via the
   `RebuildOptions.OnVerify` hook / staged-merkle hook), compact-encoded
   (`stateless.CompactProofFromNodes`, ~45 % of full RLP). Cadence rationale in
   §5; ~1 MB compact / 1000 blocks ≈ 1.27 KB/block amortized (≈18 % of the
   compressed witness).
3. **Multi-sig attestation.** Each producer signs `(blockNum, stateRoot,
   receiptRoot)` (`stateless.SignAttestation`); a client counts distinct
   producer signatures (`stateless.Attestation` aggregation) as defence-in-depth
   on top of self-verification. ≥M-of-N distinct IDC signers per anchor.

Producers are **redundant and stateless-to-each-other**: a client can fetch the
same artifacts from any producer and cross-check (content-addressed: witness
hashes, MPT proof anchors to the header root, code is keccak-addressed). A
malicious/faulty producer cannot forge data a client accepts — every artifact is
verified locally against the header chain.

## 2. Serving RPC (attack-hardened)

A read-only service (new package `internal/ethel/stateless/serve`, exposed over
the existing `modules/rpc` HTTP/WS transport) with methods:

- `stateless_getHeaders(from, count)` — contiguous headers (RLP), ≤ a cap.
- `stateless_getBlock(num)` — header + body.
- `stateless_getWitness(num)` — per-block witness (compressed).
- `stateless_getAnchor(num)` — MPT anchor proof (compact) at an anchor height +
  producer attestations.
- `stateless_getCode(hashes[])` — bytecodes by keccak (the `CodeRequest`/
  `CodeResponse` protocol in `stateless/bundle.go`; content-addressed, ≤ a cap).
- `stateless_head()` — current tip number + hash + finalized anchor.

**Attack protection** (layered):

1. **Per-IP token-bucket rate limiting** — requests/sec and bytes/sec caps,
   burst + sustained. Backed by `golang.org/x/time/rate` per remote addr, with
   an LRU of limiters. Reject with 429 + `Retry-After`.
2. **Request caps** — max `count`/range per call (e.g. ≤256 headers, ≤64 code
   hashes), max response bytes; pagination via cursors (reuse
   `messaging/store/query.go` cursor pattern).
3. **Connection limits** — max concurrent conns/IP, idle/read/write timeouts,
   max header+body size on the HTTP server (`modules/rpc` server config).
4. **Optional auth for write-ish/expensive calls** — API tokens / JWT
   (`cmd/clef`-style) for `getAnchor` bulk pulls; anonymous reads are
   rate-limited only.
5. **Anti-spam for P2P distribution** — if served over GossipSub instead of/
   alongside RPC, reuse RLN (`internal/distributed/messaging/rln`) to rate-limit
   per-identity without central state.
6. **DDoS posture** — front with a CDN/reverse-proxy for static artifacts
   (witness/anchor blobs are immutable + content-addressed → cacheable
   forever); the origin RPC only serves the live tip + cache misses. Health
   endpoint `/health`, metrics to the existing Prometheus stack
   (`internal/metrics`).

Immutability ⇒ cacheability: every artifact except the live head is fixed and
content-verifiable, so the bulk of traffic is CDN-absorbable; origin load is
bounded by tip rate (1 block / 12 s).

## 3. minimal node

Goal: trustless tip-following at 12 s with ~1 week of rolling data.

- **Bootstrap from a checkpoint header** (hard-coded / social / PoS-finalized),
  NOT genesis. `stateless.NewHeaderChain(anchor)`.
- **Live sync (12 s):** each new block — fetch header (extend
  `HeaderChain`, ① ), body + witness (replay → receiptRoot, ② via
  `ethel.VerifyBlockFull` with the per-block witness); at anchor heights also
  fetch the MPT anchor (③). `VerifyWindowCadence` enforces "③ every K".
- **Fast catch-up (join):** fetch a window of (header, witness, anchor) and
  fan-out verify in parallel (`stateless.VerifyBatch` / `VerifyWindowCadence`,
  workers = cores) — every block's targets are header-chain-anchored, so block N
  does not depend on N-1.
- **Retention — auto-delete > 1 week.** Keep a rolling window
  (≈50 400 blocks @ 12 s). Prune trusted-chain projection
  (`HeaderChain.Prune(keepFrom)`) + delete witness/body/anchor blobs below
  `keepFrom`. The anchor checkpoint advances forward as the window rolls (re-
  anchor to the latest finalized anchor so trust never depends on pruned data).
  Configurable `RetentionBlocks` (default 50 400).
- Stores: ~1 week × {header 600 B + body + witness 7 KB} + anchors (1 MB/1000).
  ≈ 50 400 × ~8 KB ≈ 400 MB + ~50 anchors × 1 MB ≈ 450 MB total.

## 4. full node

Goal: durable header+body archive from genesis (no state, no witness).

- Downloads + stores **header + body from genesis to tip**; verifies the header
  chain (parentHash) end to end; serves headers/bodies to minimal/archive peers
  (acts as a full-history block source so producers needn't serve cold blocks).
- No state, no trie, no witness → cheap (headers+bodies only). Optionally
  re-derives txindex/receipts.
- Follows tip at 12 s (header+body only).

## 5. archive node

Goal: full historical state, built by self-execution, then live.

1. **Download** header + body + witness (and code) from producers/full nodes,
   genesis → as far as available.
2. **Execute from genesis** using the witnesses (`replayWitnessBlock` forward,
   advancing a writable trie — the same forward-build the
   `n42-stateless-anchor-produce` producer demonstrates, but persisting full
   state + trie incrementally rather than O(state) full rebuilds). Verify each
   block's receiptRoot (②) and periodically the stateRoot (③) against the header
   chain.
3. **Catch up** to the current tip (download + execute the gap).
4. **Switch to 12 s live sync** once at tip — thereafter behaves like a producer
   minus the serving/attestation (or becomes a producer if it serves RPC).
- Stores: full PlainState + HashedState + TrieOf* + all headers/bodies. This is
  the heaviest mode (the existing eth-el archive datadir).

## 6. Wire artifacts (already built)

- **`StatelessBundle`** (`stateless/bundle.go`): per-block unit {Header, Body,
  Witness, NewCode, Proof}. Light bundle = no Proof (non-anchor); anchor bundle
  carries the MPT proof. Self-describing length-prefixed codec.
- **Code distribution** (`stateless/bundle.go`): `NewCode` ships
  newly-deployed code; `MissingCodeHashes`/`CodeRequest`/`VerifyCodeResponse`
  fetch + content-verify older code on demand.
- **Compact proof** (`stateless/proofwire.go`): faithful, ~45 % of full RLP;
  `CompactProofFromNodes` / `DecodeCompactToNodes`.
- **Verification** (`internal/ethel/minimal_verify.go`): `VerifyBlockFull`
  (①②③, nil-Proof ⇒ ①② only) + `VerifyWindowCadence` (enforces ③ every K).

## 7. Mapping to existing N42 components

| Need | Existing | New |
|---|---|---|
| witness gen (12s) | `internal/ethel` exec + `witness.go` | live-emit hook |
| MPT anchor (1000) | `commitment.ExtractMultiproof` + `RebuildOptions.OnVerify` / staged-merkle | wire into live exec (incremental verify, not O(state)) |
| verify ①②③ | `stateless` + `minimal_verify.go` | — |
| bundle / code dist | `stateless/bundle.go` | — |
| RPC transport | `modules/rpc`, `internal/api` | `stateless/serve` methods |
| rate limit / anti-spam | `messaging/rln`, std token-bucket | `serve` limiter middleware |
| P2P distribution (opt) | `internal/p2p` topics | stateless topics |
| retention/prune | `HeaderChain.Prune` | blob GC by block window |
| metrics | `internal/metrics` | serve/produce counters |

## 8. Phased plan

- **P-A — live producer**: hook per-block witness + every-1000 anchor capture
  into the live exec loop (incremental verify so it keeps up at 12 s; the
  `n42-stateless-anchor-produce` O(state) verify is fine for backfill, not
  live). Emit to freezers.
- **P-B — serve RPC**: `stateless/serve` with the methods + rate-limit/caps/
  timeouts middleware; metrics; `/health`. CDN-frontable static artifacts.
- **P-C — minimal client**: 12 s live sync + fast-catch-up `VerifyWindowCadence`
  + rolling 1-week prune + re-anchor.
- **P-D — full client**: header+body genesis archive + serve.
- **P-E — archive client**: download → forward-execute from genesis (persist
  full state) → catch up → switch to 12 s.
- **P-F — multi-IDC**: ≥M-of-N attestation aggregation, cross-producer
  cross-check, producer discovery.

### Open items
- **Incremental anchor verify** for the live producer (replace
  `FullStateRootVerify` O(state) with `MerkleStageIncremental` O(dirty)); the
  backfill producer (`n42-stateless-anchor-produce`) stays O(state).
- **Anchor span semantics**: current anchor = post-state multiproof over the
  K-block touched-key window (reverse-verifiable with the window's changesets).
  Confirm the minimal client's anchor verification path (reverse vs forward) and
  whether anchors carry their window's changesets or rely on the light bundles'.
- **Re-anchor protocol**: how the minimal client advances its trusted checkpoint
  as the 1-week window rolls (finalized-anchor handoff).
