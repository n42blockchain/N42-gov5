# n42 QMDB-native prefetch — evaluation (2026-07-15)

Whether the n42 self-developed chain should adopt an EIP-7928-style access-list
prefetch, and if so, how to measure the benefit before committing.

## Placement

EIP-7928 BAL is an ETH/Glamsterdam consensus feature and lives in the **eth-el**
path (`internal/ethel/bal`): header hash binding (`BALTime` fork,
`Header.BlockAccessListHash`), out-of-band full-BAL service, generation +
verification. On **n42** it stays dormant — no chainspec sets `balTime`, so the
header field is nil and omitted from RLP; n42 headers and consensus are unchanged.

n42 does **not** need the consensus layer: it is not ETH-interop-constrained, and
its state root already commits to the execution result (a strictly stronger
cross-node commitment than a BAL hash). The only part that could interest n42 is
the **prefetch** — warming state reads ahead of / alongside execution.

## The prefetch mechanism is already built and EIP-7928-decoupled

n42 can prefetch without any header field or fork:

- `bal.BuildBALFromViews(views, base)` harvests the touched accounts/slots +
  post-values from the block-STM `MVStateView`s (an execution by-product), and
- `ethel.PrewarmFromBAL(reader, bal)` warms those accounts/slots through a
  `BALPrewarmReader` (any reader — a QMDB reader qualifies).

No consensus surface, no fork. This reuses the same code as the eth-el BAL.

## Cost measured (mechanism overhead)

`BenchmarkPrewarmFromBAL` (busy block: 200 accounts × 8 slots = ~1800 warm reads,
no-op reader to isolate dispatch): **~122 µs/block, 4800 allocs**. That is the
controllable dispatch cost; the actual read latency is on top and depends on the
backing store.

## Why the benefit is likely marginal for n42 — measure first

Prefetch does **not reduce total read work** — it issues the same reads, only
earlier, to *overlap* their latency with other work. Its benefit is therefore:

    benefit ≈ (QMDB cold-read latency − cache-hit latency)
              × overlappable-reads
              × (fraction of read latency actually hidden)

Two properties of n42 shrink this:

1. **QMDB is append-only and read-fast.** Cold-read latency (twig forest, mmap)
   is low, so the `cold − cached` gap prefetch can hide is small.
2. **Block-STM already overlaps reads.** n42 executes transactions in parallel
   (`modules/state` MVHashMap / parallel executor), so per-tx reads already run
   concurrently. A separate prefetch pass mostly re-issues reads the parallel
   executor is already overlapping — little additional latency to hide.

So the expected marginal benefit on top of block-STM is small, and it competes
with block-STM for the same cores/IO. Do **not** adopt it blindly.

## Measurement plan (definitive, on a real workload)

A memdb micro-benchmark understates QMDB (mem reads are ~free). Measure on the
real path:

1. Add a prefetch hook on the n42 QMDB import path (behind a flag/env, default
   off): before `StateProcessor.Process`, if a harvested access set is available,
   run `PrewarmFromBAL` on the QMDB reader on a worker goroutine.
2. Replay a tx-dense range (e.g. via `replay-v2` or a fleet node) twice — hook off
   vs on — and compare **block execution time** (Mgas/s) and QMDB read counts /
   cache-hit rate.
3. Adopt only if the tx-dense delta is clearly positive and does not regress the
   block-STM path (core contention).

## Recommendation

- Keep EIP-7928 BAL (consensus + out-of-band service) **eth-el only**.
- On **n42**, keep BAL dormant. If prefetch is pursued, do it as the
  QMDB-native, non-consensus path above (harvest from MVHashMap → `PrewarmFromBAL`
  on the QMDB reader), gated behind a flag, and **measure on a replay run first** —
  the block-STM overlap likely leaves little to gain.
