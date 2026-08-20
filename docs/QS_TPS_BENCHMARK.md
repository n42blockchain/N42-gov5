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

## The hidden fifth variable: baseFee carry-over between rounds

Found 2026-08-19, after two A/B rounds produced a difference that turned out
not to exist.

The flood submits at a fixed `-gasprice`. The chain's baseFee is **chain
state**, so it survives the round: a round that ran full 480M blocks leaves
the baseFee near or above the flood's cap, and the *next* round starts
squeezed. The signature is unmistakable once you look for it — blocks
alternate full/empty and occupancy pins at ~53%:

```
block 14481189  baseFee= 9.270 gwei  480.0M/480M (100%)  22,857 txs
block 14481190  baseFee=10.429 gwei    0.0M/480M (  0%)        0 txs   <- above the 10 gwei cap
block 14481191  baseFee= 9.126 gwei  480.0M/480M (100%)  22,857 txs
block 14481192  baseFee=10.266 gwei    0.0M/480M (  0%)        0 txs
```

A full block raises baseFee 12.5% (target is half of a 480M limit), which
lifts it over the cap; the resulting empty block drops it back under. So
`occupancy ~53%` and `full(>=95%) ~= blocks/2` is **not** a supply problem
and **not** a chain limit — it is the fee market oscillating across the
flood's own price cap.

**What this cost:** a round on `v5.7.954` right after an idle fleet reported
20,565 / 19,424 / 19,805 TPS at 100% occupancy; two rounds on `v5.7.952`
immediately after reported ~12,000 at 53%. That reads as a 40% regression in
the older binary. It is not: running `v5.7.954` again in the same late
position gave 12,186 / 11,805 / 11,807 at 52.5-53.4% — identical to
`v5.7.952`, with a marginally *better* block time (0.984 s vs 1.017 s).

**Rules:**

6. **Round order is a variable. Randomise it or neutralise it.** Only the
   first round after a long idle period sees a low baseFee. Either compare
   binaries in the same position, or let the chain idle until the baseFee
   decays, or raise `-gasprice` far enough that the cap never binds (and
   then re-check the faucet arithmetic, which scales with it).
7. **Report `full(>=95%)` alongside occupancy.** `blocks/2` is the fee
   oscillation; a spread of partial blocks is a genuine supply shortfall.
   They produce similar occupancy and mean completely different things.

8. **A round started while the baseFee sits above the flood's gasprice dies
   in its FUNDING phase**, not just in its measured windows. The senders never
   get funded, and then every transaction fails with
   `insufficient funds for gas * price + value` — 150,000 submitted, 8,850,000
   failed on 2026-08-20 — while the windows dutifully report an idle chain. It
   is rules 6/7 again, one phase earlier. Check the baseFee before starting,
   not just the faucet.

   Do NOT diagnose this as a drained faucet without checking twice. The first
   reading that afternoon said the faucet held 0 ETH; it actually held 939,800.
   `eth_getBalance` at `latest` against a node whose consensus is wedged can
   return 0 rather than an error, and that false zero sent a whole diagnosis
   down the wrong path.


## What the chain actually spends its CPU on (2026-08-19, under load)

30 s CPU profile on a node at the 480M tier, 22,857 tx blocks, shard-senders
(cold-follower path). Total samples 132.89 s in 30 s wall = **442.94%**, i.e.
~4.4 of the 5 threads `--pprof.maxcpu 5` allows. `runtime.cgocall` alone is
**70.8%** of all CPU; broken down by what it calls:

| Callee | % of all CPU | Reading |
|---|---|---|
| `secp256k1_ext_ecdsa_recover` | **30.7%** | 22,857 signature recoveries per block. Warming the pools (`-broadcast`) is the documented lever. |
| **`mdbx_txn_begin` (write)** | **20.4%** | Anomalous. Opening a write transaction costs 2.7x the work done inside `CommitToCanonical` and ~3x `cursor_put`. |
| `mdbxgo_cursor_put2` | 7.4% | The actual page writes. |
| `mdbx_txn_begin` (read) | 6.5% | One RoTx per `View`; reusable. |
| `mdbxgo_txn_commit_ex` | 4.2% | |
| `keccakF1600` | 2.0% | |
| blst (BLS aggregate/verify) | 0.1% | Consensus signatures are not a cost at this tier. |

The write-transaction-begin number is the finding. MDBX has a single writer
and `mdbx_txn_begin` processes the GC/freelist, so it is charged in
proportion to freelist pressure — and the same day's write-attribution run
measured 98% of the write volume as copy-on-write churn. **The write
amplification and the transaction-begin cost are the same problem seen from
two ends**, which also means every extra small write transaction (the
consensus-state writes at 2.3 per block, the head-pointer write at 1 per
block) pays this tax on top of its own 39x / 207x amplification.

Contention, same run: the RPC batch-submit path (`BatchRawTransaction`) and
the block-import path (`InsertChain`) each account for ~11% of total mutex
delay, apparently on the same lock.

**Caveat on the method:** the `--pprof.mutex` / `--pprof.block` flags
themselves are free — a round with them on measured 12,184 / 11,422 / 11,425
against 12,186 / 11,805 / 11,807 with them off. *Pulling* a profile is not
free: the two windows during which a 30 s CPU profile plus heap, mutex and
block dumps were fetched dropped from ~11,800 to 9,903 and 8,759 TPS. Fetch
profiles outside the measured windows, or discard those windows.

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

## 2026-08-19 results

Binary `v5.7.954` (round-3 audit fixes + txindex segment reopen + QMDB load
allocation fix), same rig, same 480M tier, pool 300k/100k, shard-senders.

| Round | Position | win1 | win2 | win3 | occupancy | cycle |
|---|---|---|---|---|---|---|
| `v954` | first after idle | **20,565** | **19,424** | **19,805** | 100% (54/54 full) | 1.11-1.18 s |
| `v952` | second | 12,186 | 12,187 | 11,807 | 54.2% | 1.017 s |
| `v952` | third | 11,807 | 11,427 | 11,805 | 52.5% | 1.017-1.053 s |
| `v954` | fourth | 12,186 | 11,805 | 11,807 | 52.5-53.4% | **0.984 s** |

Read the first row against the 2026-08-16 record (22,089 / 12,189 / 12,189,
occupancy 95.1% falling to 52%): the peak window is 6.9% lower, but all three
windows held full blocks where the record collapsed after window 1 — a
three-window mean of 19,931 against 15,489, **+28.7%**. Rows two to four are
the baseFee carry-over described above, not a binary difference; the like-for-
like comparison is row three against row four, where `v954` matches `v952` on
TPS and is slightly ahead on block time.

At 100% occupancy a block carries **22,857 transactions / 480M gas**, which is
the full tier.

### 2026-08-20: v5.7.955 (consensus state folded into the canonicalization tx)

| Round | Position | win1 | win2 | win3 | occupancy | cycle |
|---|---|---|---|---|---|---|
| `v955` | first after idle | **22,093** | 17,520 | 18,660 | 100% (all full) | **1.035 s** |
| `v954` | first after idle | 20,565 | 19,424 | 19,805 | 100% (all full) | 1.111 s |

Like-for-like — same position, same load, both at 100% occupancy carrying the
full 22,857 transactions per block — the block cycle drops from 1.111 s to
1.035 s, **6.8%**. Window 1 also matches the 2026-08-16 record of 22,089, but
at 100% occupancy where the record was at 95.1%, i.e. more work per block for
the same throughput.

The structural change is confirmed independently by the write probe on the
light production load, 283 write transactions over 71 blocks:

| Transaction class | v955 tx/block | v954 tx/block |
|---|---|---|
| main block commit | 1.00 | 1.00 |
| canonicalization **carrying consensus state** | 0.99 | 0.95 (state not included) |
| `HotStuffState` alone (the two vote barriers) | **1.99** | 2.32 |

1.99 is exactly the two `JournalVote` durability barriers HotStuff-2 needs
(prepare vote and commit vote), and 0.99 is the one canonicalization — the
separate `persistState` transaction is gone. Per-block byte counts are NOT
comparable across sessions: they move with freelist state, which differs
between runs.

**Still not verified:** whether `BeginRw`'s 20.4% CPU share actually dropped.
Two attempts failed for different reasons — the first round died in its
funding phase (rule 8), and in the second the 30 s profile landed on the
pool-admission phase rather than block execution: `secp256k1` was 73% of
cgocall while `cursor_put` was 0.08%, i.e. the node was verifying incoming
transactions, not writing blocks. A valid comparison has to be taken inside a
steady 100%-occupancy window on both binaries; profile placement matters as
much as round placement.

## Wedge recovery (2026-08-20)

A round that dies mid-flight can leave the fleet locked on a block nobody
has. Signature: every node at the same height and the same view, views
advancing only by 30 s timeouts, and the leader logging
`Sealed block lost to a competing candidate; dropping` at head+2.

`hotstuff-inspect --datadir <node>/chaindata` (note: **chaindata**, not the
datadir root) shows it plainly:

```
view=162846 timeouts=188 journal=193
lockedQC=162653/ec0a6b117f63   committedQC=162652/947870d1bd6f
lastVoted=162653/ec0a6b117f63  lastCommitVoted=162653/ec0a6b117f63
```

All seven nodes voted, commit-voted and locked block `ec0a6b117f63`, whose
body exists in no node's database — `eth_getBlockByNumber` returns null for
that height everywhere. The leader correctly extends the LockedQC block per
HotStuff-2's safety rule, so it builds head+2 forever and no follower can
validate a parent it does not have. The node's own startup check
("persisted consensus state agrees with the applied chain") passes, because
the persisted state IS consistent — it is the locked block that is missing.

This is the documented tradeoff of `consensus votes will not wait for block
persistence`, logged at startup: a stop between locking and persisting loses
the body.

Recovery, with the fleet stopped and every node showing the same committedQC:

```
hotstuff-reset --datadir E:\qs-node<i>\chaindata --apply --force                --backup <fresh-file>
```

It clears the round/vote journal (410 bytes), which removes the crash-time
no-equivocation guard, so it is only valid after a coordinated stop where the
applied chain has been verified identical across nodes. The tool enforces an
exclusive fsynced backup. Blocks resumed at 2 s immediately on restart.
