# fillTx growth within a B leg: correlation, breakdown, and levers

Round r35zzzax, leg B1. Source: `/data/blockchain/wr-logs/r35zzzax-keep/node{1,2}/n42.log`
(+ the gzipped rotation for node2's pre-20:38 history) and
`/data/blockchain/wr-pprof/r35zzzax-B1-win2-node1-cpu.pb.gz`.

## 1. Correlation

The pool does not log `pending=/queued=` depth lines in this harness; the closest
proxies are `txpool reorg phases` (`pendingAccts`, `queueAccts`, `nonces`) and the
`miner: parallel fill` line's own `candidates`/`included` (the actual per-build tx
count). node1 leads in 8-block tenure bursts (`gap>3s` separates them); five
consecutive bursts inside B1 (20:10:25, 20:11:00, 20:11:35, 20:12:12, 20:12:57):

| burst start | candidates (full blocks) | pick (ms) | run (ms) | pendingAccts (nearest) |
|---|---|---|---|---|
| 20:10:25 | 163000 | 74-96 | 342-416 | ~24 |
| 20:11:00 | 163000 | 79-107 | 395-603 | 37 |
| 20:11:35 | 163000 | 82-143 | 346-494 | 4 (post-reorg dip) |
| 20:12:12 | 163000 | 85-143 | 345-643 | 50 |
| 20:12:57 | 163000 | 76-260 | 385-635 | 44 |

**The included tx count per full block is flat at 163000 the whole leg** (capped by
`fillgas`/21000 gas-per-transfer) — it is *not* the "+11%" variable from the
anatomy doc's round, so this round can't reproduce that exact ratio, but it gives
a cleaner test: tx count is constant and fillTx still roughly doubles (416 ms →
895 ms total pick+run) from the first burst to the fourth/fifth. `pendingAccts`
also trends up across bursts (24→37→50) but resets hard after each reorg (37→4),
while pick/run keep climbing leg-wide — so **pendingAccts/queueAccts fits worse
than "elapsed time into the leg / cumulative pool churn"**; neither pick nor run
tracks included-tx-count (flat) or pendingAccts (sawtooth) as cleanly as a
leg-elapsed trend. `pendingSnapshot` itself (the `Pending()` call) is negligible
throughout (0.01–0.05 ms) — it is already served from `pool.pendingSnap` (the
Round 27/28 cached-snapshot path), so **pool-map construction is not the
bottleneck**; whatever depth effect exists is downstream of it.

## 2. Breakdown (profile: r35zzzax-B1-win2-node1-cpu.pb.gz, leader=node1 only for
the first ~5s of this 20s window per the runner log)

`go tool pprof -focus='fillTransactions|BuildParallel|ApplyTransaction|NewTxByPriceAndNonce|Pending'`
only matched 0.29% of the window's 311 total CPU-seconds — node1 spends the
other ~80% of this window as a follower (import/vote), confirming windows are
not pure-leader time. Within the matched slice, the dominant leaves are QMDB
cold-path state reads, not pool bookkeeping:

- `lib/qmdb.getterCold.ColdEntry` / `mapIndex.Get` — 0.37s cum (largest)
- `lib/kv/mdbx.(*MdbxTx).GetOne` — 0.36s cum
- `runtime.mapaccess2` / map hashing (`ctrlGroup.matchH2`, `aeshashbody`) — 0.25s
- `StateProcessor.runParallel` / `prefetchPendingCredits` — 0.12s / 0.05s
- `txsSortedMap.Len`, `Transaction.From`/`GasTipCap` — negligible (<0.02s each)

So inside `fillTransactions`/`BuildParallel`, **per-tx state lookups (QMDB cold
entries + MDBX GetOne) dominate**, not `NewTxByPriceAndNonce`'s heap ops and not
`Pending()`. The from-source read of `internal/miner/worker.go`:
- `Pending()` (txs_pool.go:184): cheap snapshot path confirmed by logs.
- `NewTxByPriceAndNonce` (builder/ordering.go:47-67): `heap.Init` is
  O(accounts), `Shift`/`Pop` are O(log accounts) each — with accounts only in
  the tens, this cannot explain a 2x swing.
- The growing cost sits in `commitTx`/`BuildParallel`'s `ApplyTransaction` path
  (worker.go:1763, 2286 parallel-fill call) — each committed tx does a QMDB
  cold lookup for account/storage state, and that lookup cost rises with the
  **total state footprint touched so far in the run** (more distinct hot
  accounts accumulate as the flood continues), which correlates with — but is
  not identical to — pool depth. Pool depth and per-tx state-read cost both
  grow with "how long the flood has been running", which is why pendingAccts
  and fillTx move together without pendingAccts being the direct cause.

## 3. Lever design (no code changes)

| Lever | Expected fillTx at 600k pending | Risk | Complexity |
|---|---|---|---|
| **A. Per-build gas/time budget cutoff that stops the walk once included-gas or a wall-clock cap is hit** (already partially present via `env.gasPool`/sizeLimiter, but no time cutoff) | Bounds `pick`'s worst case; does not touch `run` (EVM cost is per included tx, already capped by gasceil) — expect little change since tx count is already flat | None — pure leader-side early-exit, still nonce/price ordered | Sonnet |
| **B. Bounded candidate set: top-K per sender by effective tip with nonce continuity, maintained incrementally by the pool instead of rebuilt per build** | Removes heap re-`Init` cost (small today) but does not touch the QMDB cold-read cost that actually dominates — low expected win (~10-20 ms) | Fairness: must preserve nonce order per sender; low risk since it's leader policy only | Opus (pool-side incremental structure + invalidation on reorg) |
| **C. Warm/prefetch the state reads `BuildParallel` needs before `run`, or cache QMDB cold entries across builds within a tenure** (the actual hot path per the profile) | This is the lever that matches the profile: cuts `run`'s 340-640 ms by however much of it is cold-lookup latency (profile shows ColdEntry+GetOne ~0.73s of the 0.89s matched — most of the measured cost) | None to consensus; purely a leader-side cache, correctness unaffected if invalidated on writes | Opus (cache coherency with concurrent writers/evictions in `lib/qmdb`) |

**Recommendation: C**, since the profile — not the depth theory — shows QMDB
cold-path reads as the dominant site inside `fillTransactions`/`BuildParallel`,
with A as a cheap, low-risk companion (Sonnet-level, do first) and B deferred
(real win is smaller than C's based on this profile).

## Vote/QC gap (logging only, for a later round)

Existing lines to pair: `"header vote: block header known and extends its
JustifyQC block, voting"` (hotstuff/validator.go) and `"hotstuff: block
committed"` (persistence.go). Neither currently carries a correlation ID or a
monotonic nanosecond field pairable to the proposer's own emit timestamp, so
closing the ~290 ms residual needs: (1) the proposer's block-send timestamp
(when `commitWork`'s result is handed to broadcast, not when it finishes
sealing) and (2) the follower's own `vote` emit timestamp for the *same block
hash*, both at microsecond/nanosecond resolution rather than the current
1-second JSON `"time"` granularity — the existing lines round to the second,
which is too coarse to measure a 290 ms gap at all.
