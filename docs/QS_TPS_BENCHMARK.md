# QS throughput benchmark — rig, protocol, and the four settings that decide the number

Reproducible TPS measurement for the qs native chain (hotstuff + QMDB,
7 co-located nodes). Written after the 2026-08-16 run reproduced the
2026-08-05 record on a build that was 9 commits newer.

Scripts (on `E:\`, derived from `deploy-7node.ps1` so they carry the real
validator keys and peer IDs):

| Script | Role |
|---|---|
| `bench-7node.ps1` | launches the 7 nodes with the benchmark configuration |
| `bench-run.ps1` | one full round: journal purge → launch → flood → measure → stop |
| `measure-tps.ps1` | samples N windows; reports TPS **and occupancy** |

```powershell
& E:\bench-run.ps1 -Offset 1800000 -Tag baseline          # 3 x 60 s windows
& E:\bench-run.ps1 -Bin <other.exe> -Offset 1900000 -Tag ab   # A/B a second binary
```

## The four settings that decide the number

Every one of these was found by measurement, and three of them were
missing from a fresh rig. Get any of them wrong and the benchmark
measures the harness instead of the chain.

### 1. `N42_TXINDEX_TAIL=1` — worth **2.5x** on its own

Without it every transaction writes a random-key `TxLookup` row into
MDBX. At 22,857 transactions per block that scatters thousands of dirty
pages, and `mdbx_txn_commit` becomes the single dominant cost of the
whole block cycle — it lands in the leader's `propose` phase, so the
block cycle stretches while build, import and voting all look healthy.

Measured, same binary, same load, full 480M blocks:

| | commit-to-canonical | block cycle | TPS |
|---|---|---|---|
| tail tier **off** | 1,855–2,012 ms (commit 1,725–1,880) | 2.6–2.9 s | ~8,000 |
| tail tier **on** | 0.5–10.4 ms (txlookup 0) | 0.98 s | **22,089** |

Caveat for production: the tail tier keeps new lookups in `<datadir>/txindex`
(RecSplit segments), which the weekly reseed does **not** copy, and on
first enable it starts at `head+1` without back-scanning. Tx-hash lookups
for the tail period therefore do not survive a reseed. Irrelevant at
benchmark loads, a deliberate choice for production.

### 2. `N42_MAX_GOSSIP_MB` — the block-size cap masquerading as a gas cap

The packing budget is `90% x MaxGossipSize - 32 KB`. The 1 MiB default
caps a block at ~8,500 transfers (~107 B each) — **37% of the 480M gas
tier, regardless of pool depth, CPU or supply**. The give-away is an
occupancy that is *constant* across windows (37.2% every time) while
everything else varies. A full 480M block needs ~2.44 MB of transaction
payload, so set at least 4; the rig uses 8.

### 3. Pool size — moderate, not maximal

`--txpool.globalslots` / `--txpool.globalqueue`. The default 5120+1024
cannot hold even one full block: the builder then fills a block by
draining and waiting, which shows up as 100% occupancy with a **long**
block time and *low* CPU. Raising it to 300k/100k recovered ~22% on its
own. Bigger is not better — the pool's own add/remove/lookup work grows
with depth, so pick roughly 2–4 blocks' worth of transactions
(60k–300k at this tier) rather than the largest value that fits.

### 4. Supply, and the sender cache it warms

`recov` (sender recovery) is the largest import component whenever the
followers' pools do **not** already hold the block's transactions: each
one then costs a full secp256k1 recovery (~50 µs, 4-way parallel at
`--pprof.maxcpu 5`) — 280 ms per full block. Instrumentation
(`N42_SENDER_TRACE=1`, logs `hintFills` / `cacheHits` / `cacheMisses`
per block) showed only ~1,200–3,300 of 22,857 senders coming from pool
hints under `-shard-senders`, i.e. exactly the 1/7 the node received
directly — transaction gossip does not keep the pools in sync at this
rate. Warming the pools (`-broadcast`) takes `recov` from ~280 ms to
~50 ms. Neither setting is "wrong": shard-senders measures the
cold-follower path, broadcast measures the warm one. Say which you used.

## Protocol

- **480M gas tier**: `--miner.gasceil 480000000` + `N42_STRESS_GASLIMIT=1`.
- **Pacing**: `--block-interval-ms 1000` (a *cap*; the achieved cycle is
  what you measure). `hotstuff.period` in the chainspec is seconds and is
  bypassed by `fastPropose`.
- **`--pprof.maxcpu 5`**: 7 nodes x 5 = 35 threads on 32. Raising it
  oversubscribes; the critical path is largely serial, so a 33% total CPU
  reading is normal and is **not** evidence of spare throughput.
- **Load**: `txflood -senders 3000 -pertx 3000 -gasprice 10000000000
  -rpcbatch 100 -conc 32`. The 10 gwei protocol keeps funding at ~1,896
  ETH for 3000 senders and sustains full occupancy for three windows
  (at 100 gwei the baseFee climb squeezes the window to ~2 minutes).
- **Windows**: 3 x 60 s. Report every window — a single peak window is
  not a result.

## Rules that cost a round each when broken

1. **`-sender-offset` must be fresh every round.** Reusing a drained or
   nonce-used sender set produces a demote spiral (pool collapses, TPS
   goes to zero) that looks exactly like a node failure.
2. **Purge the pool journal between rounds.** A restored journal replays
   the previous round's unmined transactions: one round whose flood never
   even launched still reported ~10k TPS, purely from leftovers.
3. **Poll RPC readiness, never sleep.** A fixed sleep raced the 12 GB
   MDBX open; the flood died on "connection refused" and the round
   reported 0 tx.
4. **Check the faucet first.** Funding cost is
   `senders x (pertx + 10) x 21000 x gasPrice`. The faucet refills from
   `devBlockReward` (~86k ETH/day) — overnight is enough — but a broke
   faucet presents as mass transaction rejection, not as an error.
5. **Read occupancy, not just TPS.** Fast empty blocks and slow full
   blocks both produce mid-range TPS for completely different reasons.

## 2026-08-16 results

Binary `v5.7.952` (all round-1/round-2 audit fixes), 7 nodes, 480M tier,
1000 ms pacing, pool 300k/100k, shard-senders:

| Configuration | win1 | win2 | win3 | occupancy | cycle |
|---|---|---|---|---|---|
| gossip 1 MiB default | 7,648 | 7,086 | 6,804 | 37.2% (capped) | 1.11 s |
| gossip 8 MB, pool 6k | 8,378 | 7,617 | — | 100% | 2.7–3.0 s |
| + pool 300k | 10,664 | 8,759 | 7,999 | 100% | 2.1–2.6 s |
| **+ `N42_TXINDEX_TAIL=1`** | **22,089** | 12,189 | 12,189 | 95.1% → 52% | **0.98 s** |

Reference: 22,473 TPS @ 1.02 s (2026-08-05, v5.7.943). win2/win3 fall to
~52% occupancy because the flood's supply pipe drains, which is the same
~15.4k tx/s submission ceiling documented in the earlier rounds — not a
chain limit.

**A/B result**: an identical round on `v5.7.950` (the build *before* the
import-time sender-verification gate) produced 8,759 / 6,856 / 6,856 TPS
against the gate build's 8,759 / 6,855 / 6,475 — the gate costs nothing
measurable at the system level. It does not even engage on this path:
`txflood` submits Ethereum RLP, which carries no `From`, so both builds
recover every sender anyway.
