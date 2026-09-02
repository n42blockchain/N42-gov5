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
2. **Purge the pool journal between rounds, after stopping.** Current builds
   persist pending transactions in MDBX `TxPoolJournal` during graceful stop;
   removing only the historical `txpool.journal` file (especially before
   stop) does nothing. `bench-run.sh` now stops all nodes first and runs the
   purpose-built `txpool-journal-reset -apply` against every stopped
   chaindata. A restored journal once made a round whose flood never launched
   report ~10k TPS purely from leftovers.
3. **Poll RPC readiness, never sleep.** A fixed sleep raced the 12 GB
   MDBX open; the flood died on "connection refused" and the round
   reported 0 tx.
   Sender nonce probes also fail the flood after their bounded retries; a
   partial sender set is a supply-harness failure, not a lower chain result.
4. **Check the faucet first.** Funding cost is
   approximately `senders x (pertx + 10) x 21000 x gasPrice` (plus the
   funding transfers' own gas). The faucet refills from
   `devBlockReward` (~86k ETH/day) — overnight is enough — but a broke
   faucet presents as mass transaction rejection, not as an error.
   Read the balance and mined nonce at `latest`; `pending` nonce only says how
   far the pool's contiguous nonce chain extends. A fresh replay from an
   external/mainnet source has no months of native-fleet rewards, unlike a
   weekly replay whose source is the stopped fleet's node 0.
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

9. **`bench-run.ps1 -DecaySec 90` neutralises the carry-over at the start of
   a round** — 90 empty blocks at 1 s pacing take the baseFee from wherever
   the last round left it back to the 1.0 gwei floor (0.875^90), and the
   script prints the resulting `gasPrice` so the round records its own
   starting condition. Added 2026-08-21, after rules 6/7 had been *known* for
   two days and were still costing rounds: a constraint written only in prose
   gets skipped, and the fix is to put it in the thing that runs.

10. **The baseFee also climbs DURING a round, and a long round outlives its
    own validity.** Decaying at the start is necessary but not sufficient: 60
    consecutive full blocks raise the baseFee 12.5% each, so it crosses the
    flood's cap partway through and the round falls into the same 53%
    oscillation. Measured 2026-08-21 with `-DecaySec 90`:

    | window | TPS | occupancy | full(>=95%) |
    |---|---|---|---|
    | win1 | **22,487** | 98.4% | 55/60 |
    | win2 | 18,665 | 89.1% | 49/55 |
    | win3 | 12,568 | 53.2% | 33/62 |

    Nothing changed between those windows except the fee market. **Compare
    same-numbered windows across binaries, and treat win1 as the measurement**
    — by win3 the rig is measuring the cap again. Chain-triggered profiles are
    unaffected: they arm on consecutive >=95% blocks, so they land in the
    valid region by construction.


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

That single-window comparison looked like a 6.8% gain. **It was noise.**
Repeating the same v955 round twice more, in the same first-after-idle
position and also at 100% occupancy, gave 20,142 @ 1.132 s and 19,743 @
1.154 s. The v954 number sits inside that spread, and a v954 round run later
the same night produced 20,569 @ 1.111 s — within 0.02% of its earlier
20,565. **Run-to-run spread on this rig exceeds the difference between the
two binaries**, so a single window cannot separate them. Report at least
three runs per binary before claiming a throughput difference.

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

**Verified, and the answer is no.** Two rounds run back to back with an
identical procedure — same pool, same offset spacing, both started from a
decayed baseFee, and the profile triggered by the CHAIN (four consecutive
blocks at >=95% occupancy) rather than by a log line:

| | v954, 28 blocks sampled | v955, 27 blocks sampled |
|---|---|---|
| total CPU | 281.0% | 273.5% |
| `secp256k1_ext_ecdsa_recover` | 34.18% | 33.44% |
| **`mdbx_txn_begin`** | **4.95%** | **4.95%** |
| `mdbxgo_txn_commit_ex` | 0.84% | 1.50% |

Per block, `mdbx_txn_begin` costs 149 ms on v954 and 150 ms on v955. Removing
one write transaction per block changed nothing measurable.

And note what else this says: **the 20.4% figure from 2026-08-19 does not
reproduce.** Both binaries sit near 5% today. That number was a property of
that particular run's database state — freelist pressure at the time — not a
standing cost of the node. A profile taken once is a description of one
moment on one database, and cost attribution drawn from it should be checked
against a second run before anything is built on it.

The change stays in regardless: folding the consensus write into the
canonicalization transaction makes canonical head and consensus state move
atomically, which closes a real crash window. It is a correctness change that
happens to be performance-neutral, and it should be described that way.

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


## 2026-08-21: the pool was re-encoding every transaction to protobuf

A profile sweep armed on the CHAIN (four consecutive >=95% blocks, then 30 s
CPU + heap delta + mutex + block + a 5 s execution trace) found a cost that
none of the earlier sweeps had separated out, because it hides inside a
function whose name suggests arithmetic.

`numSlots()` — the pool's "how big is this transaction" helper — called
`tx.Marshal()`, a full protobuf encode, uncached, on every call. The pool
asks for slots several times per transaction, and `validateTx` paid for a
second encode purely to length-check the result. On a node at the 480M tier
with 22,857-transaction blocks:

| | v5.7.955 run 1 | v5.7.955 run 2 | v5.7.956 |
|---|---|---|---|
| total CPU | 282.20% | 274.64% | (see note) |
| `numSlots` | 4.71% (3.99 s) | 4.22% | **0.012%** (0.01 s) |
| `Transaction.Marshal` | 6.54% (5.54 s) | 6.20% | **0.28%** (0.23 s) |
| `EncodedSize` (replacement) | 0.78% | 0.72% | 0.66% |

Near-zero, not literally gone — they drop out of the default `-top` listing,
which is what makes "absent" an easy thing to write and a wrong thing to
write. Checked with `-peek`, the residue is accounted for: `numSlots` keeps
0.01 s on the new `txWireSize -> EncodedSize` path, and the remaining proto
`Marshal` is entirely `miner.commit -> streamverify.BuildStreamPacket`, a
path this change never touched and a legitimate proto use (it is a
cross-boundary wire format). The pool's two call sites — `numSlots` at
3.51 s and `validateTx` at 1.67 s of the v955 total — are gone.

Allocation told the same story from the other side: over 27 sampled blocks,
`ConvertUint256IntToH256` was 13.98% of all bytes allocated and
`toProtoFields` another 14.54% — the protobuf path was roughly 30% of
everything the node allocated under load.

The fix is to size transactions by their RLP encoding (`EncodedSize`, which
memoises and which the batch ingest path already warms in parallel). That is
also the encoding the block builder budgets against and the broadcaster
publishes, and it matches geth, whose txpool sizes by `tx.Size()`.

**Two runs of the unchanged binary are in that table on purpose.** The
same-binary spread is the yardstick for whether a difference means anything:
0.49 points on `numSlots`, 1.27 points on `Ecrecover` (34.30% vs 35.57%), and
`mdbx_txn_begin` again moving several points between runs — the third
independent confirmation that its share tracks freelist state and cannot be
read from a single profile. A 4.2-4.7 point item going to zero is far outside
that spread; anything under ~1.5 points is not a result yet.

Note on the v956 column: its round inherited an elevated baseFee and ran at
53% occupancy, so its totals are not comparable with the v955 rounds. The
per-symbol facts that survive are the ones that do not depend on occupancy —
a symbol present in every profile of one binary and absent from the other.
That is why `-DecaySec` (rule 9) exists now.

### Also landed: the sender-seeding change, measured, and it is a null result

Seeding the pool's sender memo with the recovery the RPC decode already
performed was re-run properly on 2026-08-21 — `v5.7.956` with `-DecaySec 90`,
all three windows at 100% occupancy, profile armed on 31/31 full blocks — and
compared against the `v5.7.955` decayed-start round.

**The change works and does nothing measurable here.** Both halves matter.

*It works*: `internal/api.TestSeedRecoveredSenderAvoidsSecondRecovery` seeds a
deliberately wrong address and requires `Sender` to return it, which only a
cache lookup can do. It fails when the function is neutered to a no-op.

*It is not measurable on this rig*: the fraction of `Sender` calls that reach
an actual recovery, which is what the change is supposed to lower, sits inside
the same-binary spread.

| round | `recoverPlain`/`Sender` | RPC share |
|---|---|---|
| v955 (A) | 0.3008 | 0.115 |
| v955 (A') | 0.3166 | 0.130 |
| v955 (r2) | 0.3074 | 0.126 |
| **v956 (B'')** | **0.3105** | 0.131 |

Three rounds of the unchanged binary span 0.3008–0.3166; the changed binary
lands in the middle of that. Direction is right (−1.9% against A'), magnitude
is not separable from noise. Only ~13% of transactions reach this node over
RPC under `-shard-senders`, and the recovery it removes runs on a prewarm
goroutine — whose stack pprof does not attach to the RPC call that spawned it,
which is why `-focus` on the submit path shows nothing either.

The change stays: it is strictly less work for an identical result, and the
test pins the mechanism. It is credited with nothing.

**Two traps this comparison walked into first**, both worth recognising:

- *Per-executed-transaction normalisation is wrong for pool-side work.* The
  first pass divided ecrecover by executed transactions and reported +44% for
  the changed binary. Block time differed between the rounds (1.000 s vs
  1.111 s), so the same flood rate fed a slower chain: pool work per *executed*
  transaction rises for reasons that have nothing to do with the code. Use a
  denominator driven by the same thing as the numerator.
- *Removing one cost inflates every other percentage.* `ecrecover` read 35.57%
  → 39.74%, which looks like a regression until you notice `numSlots` vacated
  4.22 points of the same pie. Shares are not costs. Compare absolute
  CPU-seconds per unit of work, or ratios between symbols.

A control helps catch both: `ApplyTransaction`, which neither change touches,
came out 1.3140 vs 1.3705 µs per unit (+4.3%) — close enough to call the
rounds comparable, where the contaminated 53%-occupancy round had put the same
control 19% apart.

### Window results, and why win1 is not the whole story

| | A' (v955) | B'' (v956) |
|---|---|---|
| win1 | 22,487 @ 98.4% | 20,566 @ 100% |
| win2 | 18,665 @ 89.1% | 18,663 @ 100% |
| win3 | 12,568 @ 53.2% | 18,666 @ 100% |
| 3-window total txs | 3,223,953 | 3,474,264 |

Read win1 alone and v956 lost 8.5%. Read the totals and it won 7.8%. Neither
is a throughput result: v955's faster blocks (1.000 s vs 1.111 s) climbed the
baseFee faster and tipped it over the flood's cap by win3, while v956's slower
blocks stayed under it for all three windows. **The rig's own fee dynamics
couple block time to how long a round stays valid**, so a binary that produces
blocks slightly slower gets a longer valid window and a better total. Rule 10
says compare same-numbered windows; this round says even that is only sound
while both rounds are still inside their valid region.

### Fleet state

The A/B sequence ended in the documented wedge: all 7 nodes at
`lockedQC=36448/fa76e3fa521e`, views advancing (36461 -> 36466), timeouts
climbing, zero blocks. Before resetting anything, the production binary
(`v5.7.955`, i.e. without either change) was started on the same data and
wedged identically at the same lockedQC — which is what rules out the new
code as the cause, and what justifies `hotstuff-reset` rather than a
bisect. Journals backed up to `E:\hs-journal-backup-node*-20260821-*.bin`;
the fleet resumed at 14,526,691 and was producing within 3 s.


## 2026-08-31: the Linux rig, and two costs the 32-thread rig could not show

First full benchmark session on the Linux box (256 threads, 7 nodes at
`--pprof.maxcpu 37` each, `QS_UDP_BASE=31000`, seed `qs-era-linux`). Every
number below is `bench-run.sh` with `--decay-sec` and shard-senders, node 0's
instrumentation, chain 94 at the 480M tier.

### The rig was measuring its own pacing

`--block-interval-ms` is a cap, and on 32 threads it never bound. Here it did:

| pacing | win1 | win2 | win3 | occupancy | blockTime win3 |
|---|---|---|---|---|---|
| 1000 ms | 12,571 | 17,524 | 20,190 | 100% | 1.132 s |
| 1000 ms (repeat) | 11,428 | 20,190 | 22,095 | 100% | 1.034 s |
| 500 ms | 12,571 | 16,762 | 28,952 | 100% | 0.789 s |

All three windows at 100% occupancy and 22,857 transactions per block, so the
53% fee oscillation of the Windows rounds never appeared — and TPS *rises*
across windows here rather than falling, because the chain is still warming.
The 22,095 that reproduces the historical record is a **pacing** number on this
host. Report the pacing with the TPS, and sweep it before reading a result as a
chain limit.

**Rule 11: on a host that can outrun the cap, the cap is the measurement.**
The signature is blockTime pinned just above `--block-interval-ms` with
occupancy at 100%.

### The load generator became the limit at ~28k TPS

At 500 ms pacing and the standard 3000x3000 flood, the patched chain reached
0.594 s blocks and occupancy fell to 62.4% — blocks getting *faster* while
getting *emptier* is the harness running out of supply, not the chain. Raising
the flood (4000 senders x 5000, `--conc 96 --rpcbatch 200`, both now flags on
`bench-run.sh`) reached 32,000 TPS at 0.714 s, but destabilised the fleet:
4.000 s / 0.714 s / 2.069 s across three windows at 100% occupancy. The mutex
profile says why — `BatchRawTransaction` was 19.3% of all contention against
`InsertChain`'s 1.3%, i.e. the RPC ingest path holding the pool lock against
the block builder. That is the next thing to fix, and until it is, conc 96 is
not a setting to benchmark on.

### Finding 1: the sender-recovery fan-out was a constant 8

`recoverBlockSenders` sized its pool `min(NumCPU/4, 8)` at package init. On any
host over 32 threads that is just 8, it reads the machine rather than
`--pprof.maxcpu`, and it runs before GOMAXPROCS is applied. The importer's
`recov` phase sat at 137-149 ms — 22,857 recoveries x ~50 us over exactly 8
workers — while the node used 0.7 of its 37-thread budget.

Sizing it from GOMAXPROCS (minus a quarter), medians over full blocks:

| | base, 483 blocks | patched, 279 blocks |
|---|---|---|
| recov | 141.7 ms | **63.2 ms** |
| exec | 68.3 ms | 68.9 ms |
| write / body | 19.1 / 20.0 ms | 18.3 / 19.9 ms |
| import total | 260.2 ms | **182.1 ms** |

Every other phase unchanged, which is what makes it attributable. Same-window
TPS at 500 ms pacing went 12,571 / 16,762 to 19,809 / 28,103.

### Finding 2: a third of all allocation was in two round trips

Profiling by object count rather than bytes put two things on top that the
byte view had buried:

- `EffectiveGasTipCmp` allocated two uint256 per comparison, and it is the
  priced heap's comparator: **162 million allocations, 8.47% of every object
  the node allocated**, all from one call chain.
- `recoverPlain` took r and s as big.Int while every caller holds uint256 —
  two ToBig, two FromBig, two `big.Int.Bytes` per recovery, on the path where
  `secp256k1_ext_ecdsa_recover` is 94% of all cgo time.

| symbol (share of objects) | before | after |
|---|---|---|
| `EffectiveGasTip` | 8.47% | **0.00%** |
| `math/big.(*Int).Bytes` | 2.33% | **0.00%** |
| `uint256.(*Int).ToBig` | 5.84% | 4.45% |
| `DecodeEthereumTransaction` (control) | 2.85% | 3.46% |
| `eip2930Signer.Hash` (control) | 4.63% | 4.68% |

Total objects allocated fell 33.9%; GC was 20-25% of node CPU before it.
**Throughput did not separate** — 15,238 / 32,000 / 20,190 against
19,809 / 28,103 / 24,000, one window better and two worse, inside this rig's
same-binary spread. It is an allocation result and is claimed as one.

Two method notes, both of which cost a wrong reading first:

- **Rank allocation by objects, not bytes.** By bytes the top of the profile
  was MDBX cursors; by count it was the two above. GC cost tracks objects.
- **Normalise by share.** The rounds did different amounts of work, so absolute
  counts fell everywhere. The controls *rising* in share is the shape of
  something vacating the pie.

### Where the cycle time actually goes

At 0.714 s blocks the measured phases account for roughly half: import 182 ms
(recov 63 + exec 69 + body 20 + write 18), build 74 ms, propose 46 ms, commit
20 ms. The follower's import is inside the vote path — `two-phase vote: holding
commit vote until block imports` — so it is on the critical path once per
block, and the rest is two BLS vote rounds and the 2.44 MB block push. The next
lever on block time is the import path; the next lever on stability is the pool
lock.

### Rig changes made this session

- `bench-run.sh` gained `--conc` / `--rpcbatch`, defaulted to the historical
  values so recorded rounds stay comparable.
- The decay phase now **waits for the chain to produce and for the baseFee to
  reach the floor, and refuses the round otherwise**. A round started right
  after a heavy one saw 0 blocks in its 90 s decay, kept the previous round's
  3.29 gwei, and died in funding — rules 6 and 8, which rule 9 says to put in
  the script rather than the prose.
- Start rounds with `setsid`. A round launched with plain `nohup` from an agent
  shell died mid-flight when the shell's process group was cleaned up, leaving
  the fleet running and no measurement.

### Where the session left the rig

Three rounds of the final binary against the base, all at 500 ms pacing,
3000x4000 supply, conc 32, decayed start:

| round | win1 | win2 | win3 | win3 blockTime | win3 occupancy |
|---|---|---|---|---|---|
| base | 12,571 | 16,762 | 28,952 | 0.789 s | 100% |
| + recovery fan-out | 19,809 | 28,103 | 24,000 | 0.594 s | 62.4% |
| + allocation | 15,238 | 32,000 | 20,190 | 1.111 s | 98.1% |
| + allocation (confirm) | 14,476 | 29,714 | **32,381** | **0.496 s** | 70.2% |

The last row is the state of the rig: block time is back down ON the 500 ms cap
with occupancy at 70%, i.e. both limits that are left are the harness's — the
pacing flag and txflood's submission rate. Window-to-window spread within one
binary (14,476 to 32,381) is larger than any difference between the binaries,
which is why the two changes above are argued from phase timings and allocation
shares rather than from these numbers.

Next round should drop `--interval-ms` to 250 and raise the flood past 32k tx/s
before reading anything as a chain result. Fleet left healthy: all seven nodes
at the same committedQC after every round.

### Finding 3: the pool held its lock across two MDBX transaction opens per tx

The mutex profile at conc 96 put `BatchRawTransaction` at 19.3% of all
contention against `InsertChain`'s 1.3% — the RPC ingest path against the block
builder, on the same lock. The reason is not in any CPU profile, because the
work is cgo and allocation rather than Go CPU: `validateTx` called
`ReadState.GetNonce` and `GetBalance` per transaction, and each of those opens
its own read transaction. A 200-transaction batch opened 400 of them, all
inside `pool.mu`.

`StateCli.GetAccountsInfo` — one read transaction for a list of addresses —
already existed in the file and had never been called. Reading the batch's
distinct senders through it before taking the lock:

| | before | after |
|---|---|---|
| MDBX transaction + cursor allocation | 28.6 GB (17.2% of total) | 0, out of the profile |
| total allocated | 166.46 GB | 126.73 GB |
| **total mutex delay** | **4.61 hrs** | **3,322 s** |
| `BatchRawTransaction` contention | 3,348 s | 1,998 s |

Its *share* rises to 60% exactly because the total fell 80% — the same
shares-are-not-costs trap as the 2026-08-21 round, seen from the contention
side this time.

Throughput, same load: 12,190 / 25,143 / 28,571 against 11,428 / 19,809 /
31,238. Two windows better, one worse, inside the spread. Three rounds in a row
now where a large, mechanically-confirmed cost came out and TPS did not move
outside the noise — which is itself the finding: **at this pacing and supply
the fleet is not limited by any of these costs.** The remaining levers are the
block-interval cap, the load generator, and the import barrier inside the vote
path.

### Round 4, left open: recovery overlapped with execution

At 250 ms pacing the cap finally stops binding — window 3 reached 30,476 TPS at
**0.750 s** blocks, 100% occupancy — so this is the operating point where a
change to the import barrier can be read. The barrier is the follower's import
inside its vote path, and its two largest pieces, recovery (63 ms) and
execution (69 ms), were serial for no reason: they share only the memo.

The change (`recoverBlockSendersAsync`, commit "overlap sender recovery with
block execution") is in with unit evidence and a clean -race, and **no fleet
measurement**. Two things stopped the B round and both are worth recording:

- **The dev faucet is consumable at this supply.** Each 4000x5000 round funds
  4,209 ETH and the senders keep it. Four rounds took the faucet from 26,588 to
  1,049 ETH and the next round died in FUNDING. Refilling means idling the
  fleet for `devBlockReward` (~3.8 ETH/s of empty blocks) — budget it, or drop
  the supply, before planning a night of rounds.
- **The bench profile costs ~4.8 GB RSS per node, 33 GB for the fleet.** On a
  shared box that is the thing to check before starting, not after: the fleet
  plus another tenant's work filled swap.

**Rule 12: read the faucet balance against the round's funding cost, and the
box's free memory against 33 GB, before starting a round.** Both failures
present late — one in funding, one as a machine-wide stall.

**Closed 2026-09-01.** The B round ran on the exact A configuration once the
faucet was refilled (see below). Medians over full blocks on node 0, 200 and
198 blocks sampled:

| | A, before | B, overlapped | |
|---|---|---|---|
| `recov` | 68.3 ms | **24.4 ms** | −64%, and it now means only the part that did not fit under execution |
| `exec` | 71.7 ms | 87.2 ms | +22% — execution shares cores with the workers, and a transaction the executor reaches first recovers on its goroutine |
| **`proc` (the pair)** | **142.1 ms** | **112.8 ms** | **−20.7%** |
| `body` / `write` (controls) | 20.2 / 21.3 ms | 21.1 / 21.3 ms | flat |
| **import `total`** | **193.3 ms** | **165.5 ms** | **−14.4%** |

The predicted `max(63,69)` is not reached — 112.8 ms rather than ~70 — because
execution itself slows by 22% while sharing the machine with 28 recovery
workers. The pair still falls by a fifth and the barrier by a seventh, with the
controls flat, which is what makes it attributable.

**And this time throughput moved with it**, which none of the three previous
rounds managed:

| window | A (poolbatch) | B (overlapped) |
|---|---|---|
| win1 | 12,571 @ 1.818 s | **17,524 @ 1.304 s** |
| win2 | 11,428 @ 2.000 s | **30,095 @ 0.759 s** |
| win3 | 30,476 @ 0.750 s, 100% | 27,428 @ **0.571 s**, 68.6% |

Block time is lower in every window; 0.571 s is the fastest block this rig has
produced, and win3's TPS is lower only because occupancy fell to 68.6% — the
load generator again, at a block rate it cannot fill. Seven nodes agreed on
every committedQC through the round and no root mismatched.

Refilling the faucet for this round is worth recording as a procedure: the
production launch profile idles at 2 s per block (0.5 ETH/s), which needed 68
minutes for the 2 GB deficit; relaunching with `bench-7node.sh --interval-ms
250` idles at 4.0 blocks/s and did it in nine. **Refill with the bench profile,
not the production one.**

## 2026-09-01: the load generator was the ceiling, and behind it the chain tops out near 40k

Three rounds after the import-barrier work, all on the overlapped-recovery
binary at 250 ms pacing, 3000x4000 supply, conc 96 / rpcbatch 200.

### The generator was never pacing itself

`txflood -rate` divided nothing by `-rpcbatch`, and on the batched submit path
one permit releases a WHOLE batch — so `-rate 45000` with `-rpcbatch 200`
offered 9,000,000 transactions a second, i.e. no limit. Unpaced, the generator
hands over its entire pre-signed set and **exits**: 12M transactions in 80 s,
after which the pool (300k slots, about 13 blocks) drains for the rest of the
round. Every "occupancy collapsed while block time kept improving" reading in
the rounds above is that drain, not the chain.

**Rule 13: a generator that has EXITED is not a supply shortfall, it is a
finished job.** Read the tail of the flood's own log — `DONE submitted=... in
80s` next to a 4-minute round says the last three windows measured a draining
pool.

Fixed, the same round reads:

| window | TPS | occupancy | block time |
|---|---|---|---|
| win1 | 36,710 | 45.9% | 0.286 s |
| **win2** | **41,495** | **90.0%** | **0.496 s** |
| win3 | 34,771 | 70.8% | 0.465 s |
| win4 | 30,095 | 53.4% | 0.405 s |

### And then the chain saturates

Raising the offered rate from 40k to 70k (measured 79.9k) did NOT raise
throughput — the peak window fell from 41,495 to 36,190:

| offered | peak TPS | occupancy at peak |
|---|---|---|
| 40,000/s | **41,495** | 90.0% |
| ~79,900/s | 36,190 | 94.1% |

More offered load past ~40k just fills the pool and spends RPC and pool-lock
work on transactions that will not be built into a block any sooner. **On this
rig, at the 480M tier with 7 co-located nodes, the chain tops out near 40,000
TPS.**

### The session in one table

| configuration | peak TPS | block time |
|---|---|---|
| 1000 ms pacing (reproduces the historical record) | 22,095 | 1.034 s |
| 500 ms pacing, same binary | 28,952 | 0.789 s |
| + recovery fan-out, allocation, pool lock, recovery/execution overlap | 32,381 | 0.496 s |
| + 250 ms pacing and a generator that actually paces | **41,495** | 0.496 s |
| + offered rate raised to 80k | 36,190 (saturated) | 0.594 s |

Every one of those steps except the last was a limit of the RIG, not the chain:
the pacing cap, then the fan-out constant, then the generator's dump-and-exit.
The chain's own number only became visible once all three were out of the way.

### Also fixed in the harness

`--floods N` starts each generator only after the previous is past funding.
They all fund from the same dev faucet, so two funding at once race on that one
account's nonce: the second gets "replacement transaction underpriced", its
funding never confirms, and the round quietly runs on half its supply.

### Where the CPU goes at the 40k operating point

Profile taken at 0.448 s blocks / 39,619 TPS, node 0, 25 s, 509% CPU (5 of the
37 threads the node is allowed):

| | share of all CPU |
|---|---|
| `transaction.Sender` | **62.6%** |
| — via `txspool.prewarmSenders` | 33.8% |
| — via `internal.recoverSenderStride` (import) | 26.0% |

Secp256k1 recovery is now essentially the whole CPU cost of the node, split
between the pool recovering what arrives over RPC and the importer recovering
what arrives in a block. Nothing else reaches 8%.

A hypothesis that the pool was recovering senders for transactions it was about
to reject as ErrAlreadyKnown — prewarmSenders runs before the dedup — did not
survive measurement: prewarm was 43.07 s of CPU before the reordering and
42.31 s after. Under `-shard-senders` each sender's transactions go to exactly
one node and the generator submits each once, so there are few duplicates to
skip. The reordering is strictly less work and is kept, credited with nothing.

**And that A/B was not a valid comparison anyway**, which is worth recording as
its own lesson: the B round's third window collapsed to 7,238 TPS at 1.667 s
blocks while A's ran at 39,619, so the two profiles describe two different
machines. Window-to-window swing at a constant offered rate in a single
five-window round: 36,880 / 29,803 / 7,238 / 26,666 / 36,190. **Rule 14: check
that the two profiles were taken at comparable throughput before comparing
their symbol shares at all.**

### Not our bug: the consensus loop does not block on the pool

The n42-rs fleet sharing this host found a large one — its consensus event loop
inline-awaited an unbounded `eth_sendRawTransaction` batch for gossiped
transactions, so the loop could not poll its transport; one member went deaf
for 11.1 s and every view it subsequently led cost the fleet a timeout. Taking
the forwarding off the loop moved them 13,714 → 38,786 TPS.

Checked here, and this node's shape is different: `internal/sync/subscriber.go`
gives **every gossip topic its own message loop**, and each message is handled
in its own goroutine behind a 256-slot semaphore. Transaction gossip therefore
cannot make the block topic deaf — the two run different loops. The residual
risk is bounded and local: if all 256 transaction handlers block on the pool
lock at once, that topic's loop stalls at the semaphore, but consensus keeps
polling. Worth remembering the next time the pool lock gets slower.

### Measured: the sender cache hits under 10% at import, and hintFills is zero

`N42_SENDER_TRACE=1`, node 0, eight consecutive full blocks (22,857 transactions
each) at the 40k operating point:

| | per block |
|---|---|
| `hintFills` | **0** |
| `cacheHits` | 2,923 – 6,031 |
| `cacheMisses` | 35,488 – 298,008 |
| **hit rate** | **1.3% – 10.5%** |

**This refutes an inference recorded earlier in this file.** From the CPU split
alone — pool prewarm 33.8%, importer 26.0% — it was argued that an uncached
import at 39,619 TPS would cost roughly 7x the pool's share rather than 0.77x,
"consistent with the cache working". It is not working, and the arithmetic that
said it was rested on an assumption never checked: that node 0's pool holds
most of the chain's transactions. It does not.

The measured numbers explain themselves once that assumption is dropped.
`-shard-senders` routes each sender's transactions to exactly ONE node, so
node 0's pool only ever admits about 1/7 of what a block carries — and
2,923-6,031 hits against 22,857 transactions IS that 1/7. The cache is hitting
precisely the transactions this node's own pool recovered, and missing the
other six sevenths because they were never here to cache. Import therefore
re-derives essentially the whole block.

`hintFills = 0` says the same thing from the other side: `applySenderHints`
finds nothing, so the pool-hint path contributes nothing at all under this
load profile.

What this does and does not license:

- It does NOT say the cache is misdesigned. Under `-broadcast` — the documented
  warm-pool mode — every pool sees every transaction and the same cache should
  carry nearly the whole import. That is the configuration to measure next.
- It DOES say that under shard-senders the fleet pays secp256k1 twice for six
  sevenths of every block, on a path that is 62.6% of node CPU, and that no
  amount of cache sizing fixes it because the entry is never written.
- **Rule 15: a cache's hit rate is a measurement, never an inference from cost
  ratios.** Both times this file inferred one it was wrong.

## The `-broadcast` measurement did not happen: the configuration OOMs the box

The section above ends by naming `-broadcast` as "the configuration to measure
next". It was attempted on 2026-09-01 at 10:16 and it did not produce a number.

```
QS_BIN=.../n42-dedupfirst N42_SENDER_TRACE=1 \
./bench-run.sh --tag broadcast-trace --offset 9000000 --decay-sec 120 --windows 4 \
  --senders 3000 --pertx 4000 --conc 96 --rpcbatch 200 --rate 40000 \
  --interval-ms 250 --broadcast
```

Funding and decay passed. Five minutes into the flood, three of the seven nodes
(0, 1 and 4) disappeared: no panic, no stack, no line on stderr, the log simply
stops mid-sentence at 10:21:38. `txflood` then reported `connection refused` on
every port it still had open and exited with `submitted=10224650
failed=1775350`. No measurement window ever ran, so there is no TPS and no
sender trace for this configuration.

The cause is not subtle. The four survivors were sitting at **10.4 GB RSS
each**, against 4.8 GB for the same binary under `-shard-senders`. Seven of
those is ~73 GB on a 136 GB box that was also carrying another tenant's
seven-node fleet and 24 GB of shared memory; swap was full at 7/7 GB. The
silence in the logs is the OOM killer's signature.

The 2.2x memory is the point of `-broadcast` working as designed. Sharding
gives each pool ~1/7 of the transactions; broadcasting gives every pool all of
them, and the pool is sized in transactions (`--txpool.globalslots 300000`),
not in bytes. The mode that would make the sender cache hit is the same mode
that multiplies pool residency by seven.

Consequences, stated exactly:

- The warm-pool hypothesis is **still unmeasured**. This round did not refute
  it and did not confirm it. Nothing in the section above changes.
- The shard-senders finding stands on its own measurement and is unaffected.
- Measuring the hit rate does **not** require saturation. The trace prints
  `hintFills`/`cacheHits`/`cacheMisses` on every imported block at any rate, so
  the right retry is a small broadcast round — fewer senders, a lower `--rate`,
  and a pool cap that fits — not another attempt at the 40k operating point.
- **Rule 16: `-broadcast` is not a drop-in swap for `-shard-senders` at bench
  supply.** Budget ~7x pool residency, or shrink the supply and the pool cap to
  match. Rule 12's 33 GB free-memory check was calibrated on sharded runs and
  is not sufficient here.

## Where the memory actually went: a boolean that decoded 22,857 transactions

The OOM above raised the obvious question — what is 10.4 GB of resident node?
A peer benchmarking a different client offered the arithmetic that 10.4 GB over
a 300,000-slot pool is ~35 KB a transaction against a few hundred bytes for a
signed transfer, and asked whether that is a property of pooled transactions
here.

It is not, and the first correction is that **RSS is not a pool measurement**
for this node. MDBX is memory-mapped, so touched database pages are counted in
RSS while living in the page cache; `free` showed 24 GB shared on the box. The
number that answers the question is the Go live heap, and there is a profile of
it from the 40k round on node 0 (`wr-pprof/qs-40k-20260901/heap-node0.pprof`),
5,545 MB total:

| live heap | MB | share |
|---|---|---|
| `rawdb.CanonicalTransactions` (cum) | 2,474 | 44.6% |
| `mobileverify.(*PacketCache).put` | 796 | 14.4% |
| `qmdb.newMapIndexSized` | 783 | 14.1% |
| `txspool.(*txLookup).Add` | **54** | **1.0%** |

The transaction pool is 1% of the live heap. The pool was never the problem.

Following the 2.47 GB up its call graph:

```
rawdb.CanonicalTransactions   2474 MB
  <- rawdb.ReadCanonicalBodyWithTransactions
    <- rawdb.ReadBlock
      <- internal.(*BlockChain).GetBlock
        <- (*BlockChain).HasBlockAndState   2073 MB  (84%)
        <- (*BlockChain).GetBlockByHash      387 MB
```

`HasBlockAndState` returns a bool. It was implemented as:

```go
blk := bc.GetBlock(hash, number)   // reads header, reads body, decodes EVERY tx,
if blk == nil { return false }     // and caches the whole block in bc.blockCache
return bc.HasState(blk.Hash())
```

`ValidateBody` calls it twice on the import path — once for the incoming block,
once for its parent — so **every imported block dragged up to two foreign
22,857-transaction bodies through a full decode and left them live in the block
cache**, to learn two bits. That is 2.07 GB, 37% of the node's live heap, and
it is also where the 1,190 MB of `txCompactReader.u256` and 684 MB of
`unmarshalCompactStorage` in the flat profile come from.

`rawdb.HasBlock` already existed for exactly this, with a comment saying so:
it reads the 12-byte body storage record and falls back to the sealed ancient
range, decoding nothing. `HasHeader && rawdb.HasBlock` is precisely the
condition `rawdb.ReadBlock` checks before it decodes anything, and
`NewBlockFromStorage` builds the block with the hash it was looked up by, so
`HasState(blk.Hash())` was always `HasState(hash)`. The predicate is unchanged;
only the decode and the incidental cache fill are gone.

`TestHasBlockAndStateMatchesReadBlockPredicate` pins both halves — the
equivalence across {header+body canonical, header+body non-canonical, header
only, absent}, and that the call leaves `blockCache` empty. Against the old
implementation it fails with "populated blockCache with 2 entries".

**The heap saving is measured; the throughput effect is not.** The 2.07 GB is
read off an existing profile, but no round has been run with the fix, so
nothing here claims a TPS number. What it should do is reduce GC pressure and
make `--broadcast` fit — which is the round rule 16 says to retry small.

- **Rule 17: attribute memory from a heap profile, not from RSS.** An
  mmap-backed node's RSS includes page cache; dividing it by a pool cap
  produced a per-transaction cost off by two orders of magnitude and pointed at
  the wrong subsystem entirely.

## The warm-pool hypothesis is confirmed, the cache is not the mechanism, and the mechanism is worth 1%

The small broadcast round of rule 16 ran on 2026-09-01 at 11:04 with the
`HasBlockAndState` fix, a 60k/20k pool cap and `--rate 8000`, and it answered
the open question. So, it turns out, did the round that OOMed — the trace it
needed had already succeeded and was sitting in the log at 10:21 while this
file recorded the round as producing nothing.

**Read the trace before declaring a round a failure.** The round failed against
the goal I set it (a TPS window). The measurement I actually wanted did not
need one.

Node 0, `"sender source"` grouped by minute, three configurations in one log:

| window | configuration | hintFill% | cache hit% |
|---|---|---|---|
| 08:03–08:08 | `-shard-senders`, 40k saturated | 22.8 → **0.0** | 36.8 → **6.6** |
| 10:18–10:21 | `-broadcast`, 40k (the OOM round) | **100.0** | **0.00** |
| 11:07–11:11 | `-broadcast`, small | **100.0** | **0.00** |

Three results, all measured.

**1. The warm-pool hypothesis is confirmed.** Under `-broadcast` every pool
sees every transaction and `applySenderHints` fills **100%** of an imported
block's senders, in two independent rounds across 400+ blocks.

**2. The sender cache is not how.** Its hit rate under broadcast is not low, it
is **exactly zero**, every block. `applySenderHints` calls `Sender()` on the
POOL's transaction object, which returns from that object's own memo and never
reaches the cache; `recoverBlockSendersAsync` then finds `From() != nil` and
also never probes it. On the import path under broadcast the cache is never
consulted at all. Its misses are dominated by first-time pool admissions, which
cannot hit by construction.

So the earlier retraction did not go far enough. It is not that the sizing
advice was worth less than implied — **the cache is the wrong mechanism on both
paths.** Under shard-senders it does 6.6–36.8% decaying; under the mode a real
gossip network actually behaves like, it does nothing. Sizing it to 2^20 buys
nothing at import and still costs a keccak probe and a store per admission.

**3. And the mechanism that does work is worth 1%.** Paired per block within
the same second, same binary, 22,857-transaction blocks:

| | A shard 40k | B broadcast 40k |
|---|---|---|
| paired blocks | 275 | 169 |
| hint coverage | **7.3%** | **100.0%** |
| `recov` µs/tx | 1.059 | **1.102** |
| `body` µs/tx | 0.852 | 0.897 |
| `exec` µs/tx | 3.403 | 3.033 |
| **`total` ms** | **148.2** | **146.7** |

Going from 7.3% to 100% sender-hint coverage leaves `recov` unchanged — very
slightly worse — and moves the whole import by **1.0%**.

`recov` cannot show the saving, and that is a property of the instrumentation
rather than a surprise about behaviour: recovery overlaps execution, and
`phases.Exec = time.Since(tPhase) - joinWait`, so the CPU the recovery workers
burn alongside the executor is billed to `exec` by construction. End-to-end
`total` is the only honest reading.

A and B are different rounds — different supply, pool state and offered rate —
so the `exec` gap is not attributable to hints alone and no claim here rests on
it. The 1.0% total is the conservative reading and it is the one to keep.

This is the third time this file has recorded a large expected effect and
measured a small one. `state_processor.go` claimed the hint pass turned "a
260 ms parallel recovery into map lookups"; the comment now carries the table
instead.

- **Rule 18: a phase timer that overlaps another phase cannot price the work it
  overlaps.** Read overlapped optimisations off end-to-end total, never off the
  phase that was supposed to shrink.
- **Rule 19: hint coverage is a property of how transactions reach the pool,
  not of load.** Sharded routing decays it to zero over a round; broadcasting
  pins it at 100%. Any figure quoted from it must name the routing mode and
  where in the round it was taken.

### Still open

- `body` is 0.85–0.90 µs/tx in both segments and both are the OLD binary. The
  `HasBlockAndState` fix removes two full body decodes from inside
  `ValidateBody`, but **the prediction that `body` falls is untested**: the
  small round's blocks import below `slowBlockThreshold`, so their phase lines
  went to Debug and were never written. Testing it needs a saturated round at
  matched block size with the fixed binary.
- The 2.07 GB heap finding stands on its own profile and is unaffected by any
  of the above; it was never a CPU claim.

## The gap is not execution: per-block overhead is 5.5x a comparable client's

A parallel benchmarking effort on the same box (a Rust client, different
consensus and storage) reported a per-block decomposition on 2026-09-01, and it
lines up against this file's own numbers in a way neither headline does.

**Their figures are their report and are not verified here.** The workloads
differ, and 22,857 against 163,000 transactions per block is itself a confound
for anything amortised. What follows is recorded for the structural conclusion,
not the digits.

| | peer (163,000 tx) | this client (22,857 tx) |
|---|---|---|
| execution | 584.6 ms → **3.586 µs/tx** | **3.403 µs/tx** (3.033 broadcast) |
| everything else | 91.4 ms → **0.56 µs/tx** | 70.4 ms → **3.08 µs/tx** |
| total import | 676 ms → 4.15 µs/tx | 148.2 ms → 6.48 µs/tx |

Per-transaction execution is the same within ~15%, and this client is slightly
ahead of it. Both are serial EVM at ~3-3.6 µs a transfer. **The throughput gap
between the two clients is not execution speed — it is per-block overhead, and
theirs is 5.5x cheaper per transaction.** Their advantage comes from
amortising fixed per-block costs over a block seven times larger.

Measured on this side, `body` (0.85 µs/tx) and `recov` (1.06 µs/tx) together
are 1.9 µs/tx — more than the peer's entire non-execution cost per transaction.

This redirects the work. Most of this session went to execution and sender
recovery; recovery is now measured at 1.0% end to end (see the section above),
and the decomposition says the remaining room is in per-block overhead — the
costs a larger block would hide and a 22,857-transaction block exposes.

- **Rule 20: normalise per transaction before comparing clients, and split
  execution from everything else.** A TPS headline conflates execution speed
  with block size; the two clients here differ by 15% on the first and 5.5x on
  the second, and the headline shows neither.

`body` at 0.85 µs/tx is the first thing to price under rule 20, and it is
exactly the phase the `HasBlockAndState` fix should move — still untested,
still needing a saturated round at matched block size.

## R1: the `HasBlockAndState` prediction is refuted, for a reason that was in the code

The prediction recorded before this round was that `body` would fall materially
once `HasBlockAndState` stopped decoding block bodies. Run at matched block
size — saturated, shard-senders, 480M gas, 22,857-transaction blocks, the fixed
binary, 104 full blocks on node 0:

| phase | R1 (fixed) | A (old bin) |
|---|---|---|
| `hdr` µs/tx | 0.083 | 0.101 |
| **`body` µs/tx** | **0.891** | **0.852** |
| `recov` µs/tx | 1.406 | 1.059 |
| `exec` µs/tx | 3.368 | 3.403 |
| `valid` µs/tx | 0.172 | — |
| `write` µs/tx | 0.995 | — |

**`body` did not fall.** It is 0.891 against 0.852. The prediction is refuted —
but the measurement alone does not carry that, and it is worth being exact
about which leg does.

R1 ran under memory pressure A did not have: its win2 held 100% occupancy at
**0.870 s a block against A's 0.448 s**, and its whole import was 165.1 ms
against 148.2. Memory pressure inflates `body`, so a fix that helped could have
been masked by a round that was 1.9x slower overall — indeed `body` rose only
4.6% while everything around it rose far more, which if anything reads as
slightly better. **The measurement is confounded and cannot refute the
prediction on its own.**

What refutes it is the code, independently of any round — and it was already
read and quoted in this file before the prediction was made:

```go
func ReadBlock(tx kv.Getter, hash types.Hash, number uint64) *block.Block {
	header := ReadHeader(tx, hash, number)
	if header == nil { return nil }          // <-- returns here, body untouched
	body := ReadCanonicalBodyWithTransactions(tx, hash, number)
```

`ValidateBody`'s two calls are the incoming block and its parent. The incoming
block's header is not written until after validation, so `ReadHeader` returns
nil and `ReadBlock` returns before it reads a body at all. The parent was
cached by the import that just finished, so `GetBlock` returned from
`bc.blockCache` without touching the database. **On the steady-state forward
path neither call ever reached a decode.** The 2.07 GB is the cache *holding*
blocks decoded on the paths that do miss — restart, reorg, gap fill — which is
a real memory cost and was measured as one, but never a per-block CPU cost.

The caveat written down before the round said exactly this and it was correct:
"the 2.07 GB is RETAINED HEAP, not CPU time. Retention and CPU are different
claims." The error was not the caveat, it was predicting a CPU effect anyway
when two functions already quoted in this file ruled it out.

- **Rule 21: before predicting that removing work speeds something up, check
  that the work was on the hot path — not merely reachable from it.** A profile
  showing retained memory proves an allocation happened, not that it happens
  every block.
- **Rule 21a: when a round is confounded, say which leg of the argument the
  conclusion rests on.** "Refuted, measured" would have been an overclaim here;
  the round is unusable and the code is decisive. A confounded measurement that
  agrees with a sound argument is corroboration, never proof.

### What R1 does not license

The rest of the table is **not comparable to A** and no claim rests on it. This
round's nodes ran at **9.3 GB RSS each** against 4.8 GB recorded for the same
nominal configuration earlier, drove available memory from 74 GB to 6 GB, and
were stopped mid-round at win1 rather than risk a repeat of the morning's OOM
on a box shared with another tenant. `recov` at 1.406 against 1.059 and `total`
at 165.1 ms against 148.2 are measured under memory pressure that A did not
have. The 9.3 GB itself is unexplained and worth its own investigation.

R2 (the block-size arithmetic test for rule 20's 5.5x) did not run: the faucet
refused R1's first attempt at 2,246 ETH against 2,527 needed, and the reduced
round consumed what was left.

**Still unmeasured, and now the only open item from this session:** whether the
5.5x non-execution gap against the peer client is a defect or is block-size
arithmetic. That needs R1 and R2 at two gas ceilings on the same binary.
`bench-run.sh` gained `--gasceil` for it (block size here is set by the gas
ceiling, not `--interval-ms`: at 21,000 gas a transfer, 480M fills at 22,857
and the block closes before the interval expires).

## Why four separate optimisations all measured ~1%: the work was on idle cores

Four changes this session removed real, measured CPU from sender recovery and
none of them moved throughput by more than 1%:

| change | measured effect |
|---|---|
| fan-out sized from GOMAXPROCS, not a fixed 8 | within noise |
| batch dedup before `prewarmSenders` | prewarm 43.07 → 42.31 s (noise) |
| sender-hint coverage 7.3% → 100% | total 148.2 → 146.7 ms (**1.0%**) |
| `HasBlockAndState` stops decoding bodies | `body` 0.852 → 0.891 (none) |

A peer benchmarking a different client hit the same wall from the other side
and named it: **work taken off an idle core is not time saved.** Their fleet
uses 6% of its machine; eliminating a genuinely duplicated second recovery —
confirmed real, then genuinely removed with a cache sized 51x a block — moved
their import 676 → 661 ms and their TPS not at all.

This rig is in the same state and the numbers were already in this file. At the
40k operating point node 0 ran at **509% CPU — 5 of the 37 threads it is
allowed**, on a 256-core box shared by 7 nodes. `senderRecoveryFanout` returns
`GOMAXPROCS - GOMAXPROCS/4`, so it spreads recovery across ~28 workers on a
node that is busy on 5 cores. There was never a shortage of cores to take the
work off.

The distinction that actually predicts which change wins:

- **Removing work from the SERIAL path is a win.** Overlapping recovery with
  execution took the pair from a sum to a max and measured 14.4% — the only
  change this session that moved the number.
- **Removing CPU that is already off the serial path is not.** Once recovery
  runs concurrently with execution, cutting its cost can only help if recovery
  is the *longer* of the two overlapped legs. It is not: `exec` is 3.368 µs/tx
  and `joinWait` is a fraction of it. Every subsequent recovery optimisation
  was therefore capped at approximately zero before it was written.

Read against R1's phase table (7.22 µs/tx total), what remains on the serial
path is where the room is:

| serial stage | µs/tx | share |
|---|---|---|
| `exec` | 3.368 | **47%** |
| `write` | 0.995 | 14% |
| `body` | 0.891 | 12% |
| `recov` (hint pass + verify + join) | 1.406 | 19% |
| `valid` + `hdr` | 0.255 | 4% |

- **Rule 22: on an under-utilised node, only the serial path has a price.**
  Before optimising, ask whether the work is on the critical path or merely
  expensive. CPU-share profiles rank by cost and cannot answer that; they are
  what sent four changes at a 62.6% CPU line that was already parallel.

This does not retract the CPU profile — `transaction.Sender` really is 62.6% of
node CPU. It retracts the inference that a large CPU share implies a large
available speed-up, which is the same class of error as arguing time from a
memory profile (rule 21). Both are "measured the right thing, argued the wrong
one".

## The clean round: four open items closed, and a new peak

One round on 2026-09-01 at 20:28 settled everything left open, because it was
finally comparable to the baseline: **total 148.1 ms against A's 148.2**,
blockTime 0.462 s against 0.448. Command as recorded, plus a 10 s RSS sampler
running from before the fleet started.

| | win1 | win2 |
|---|---|---|
| TPS | 37,985 | **42,079** |
| occupancy | 80.4% | 85.0% |

42,079 is a new peak for this rig, above the 41,495 recorded earlier.

### 1. "4.8 GB per node" was the IDLE plateau

The figure quoted all day as "the bench profile cost" has, it turns out, no
recorded sampling time anywhere in this file. The curve settles it:

| phase | max RSS/node | available |
|---|---|---|
| startup spike | 6.70 GB | 75 GB |
| **idle decay plateau** | **4.73 – 4.78 GB** | 87 GB |
| flood begins | 7.24 GB | 66 GB |
| **saturated** | **11.15 GB** | 22 GB |

4.8 GB is where an idle node sits. The loaded number is **11.15 GB**, and the
"unexplained 9.3 GB, nearly double the same configuration" flagged in the R1
section **was never an anomaly** — it was a loaded sample compared against an
idle one. Rule 12's fleet budget is 78 GB, not 33.

This is the same error corrected in a peer's numbers earlier the same day (0.8
GB taken before a round, restated as 3.37 GB after it) and then repeated here
twice: once quoting 4.8 as a budget, once treating the gap as a finding.

- **Rule 23: a memory figure without a sampling phase is not a measurement.**
  Idle, startup and saturated differ by 2.4x on this node. Sample a curve.

### 2. The `body` prediction is refuted by a clean measurement

`body` is **0.848 µs/tx against A's 0.852** — unchanged, now at matched totals
rather than under the memory pressure that made the first attempt unusable.
Rule 21a's caution was right that the pressured round could not carry this; the
clean one can, and it agrees with the code argument.

### 3. Non-execution cost is per-transaction, not per-block

Binned by block size **within this one clean round**:

| med txs | non-exec µs/tx | `body` | `write` | `exec` |
|---|---|---|---|---|
| 10,400 | 3.356 | 0.813 | 0.917 | 2.939 |
| 12,424 | 3.478 | 0.803 | 0.857 | 2.950 |
| 22,857 | 3.346 | 0.848 | 0.749 | 3.135 |

**Flat** across 2.2x. The 14% rise seen in the pressured round was pressure; the
clean answer is that it does not move. A per-block fixed cost amortised over
2.2x more transactions would have fallen by up to 2.2x.

So the 5.5x non-execution gap against the peer client is a real per-transaction
cost and not block-size arithmetic. The limit stands: 2.2x of measured range
against a 7x separation is still an extrapolation.

### 4. Sender hints relocate work rather than remove it

| | A | clean |
|---|---|---|
| hint coverage | 4.5% | **27.0%** |
| cache hit | 8.3% | 27.9% |
| `recov` µs/tx | 1.059 | **1.408** |
| `exec` µs/tx | 3.403 | **3.135** |
| total ms | 148.2 | 148.1 |

Six times the hint coverage makes `recov` **worse** by 0.349 µs/tx and `exec`
better by 0.268, for a total that does not move. The mechanism fits: the hint
pass is a per-transaction map lookup into the pool on the SERIAL path, and what
it saves is recovery that was already overlapped with execution and billed to
`exec`. It moves work from a parallel context into a serial one and nets to
zero because the parallel copy was running on idle cores anyway (rule 22).

Stated as an observation, not a proven claim: A and the clean round differ in
binary as well, though neither change touches this path.

That is now the third independent measurement saying the hint/cache apparatus
does not buy what its comments claim — 1.0% end-to-end at 100% coverage, 0.00%
cache hits under broadcast, and now a composition shift that nets to zero.

## Cross-checked against a second client team: what this architecture already has

A second benchmarking session (Rust fleet, `feat/native-fleet7`) reported its
tenure-route results on 2026-09-02. Three of its findings were checked against
this codebase rather than assumed to transfer.

**1. "Every block executed twice on the critical path (builder, then the next
leader's import)" — the builder half does not apply here.** `worker.go:578`
seals with `WriteBlockWithState(blk, receipts, task.state, task.nopay)`: the
leader writes the block with the state `fillTx` already computed. There is no
second execution and no `InsertChain` of its own block. The `InsertExecutedBlock`
change that took their own-import from ~500 ms to 30-300 ms has no counterpart
to make here.

The *other* half does apply: the leader of h+1 must import h before it can build
on it, and that import is what `blockimport phases` measures. It is a
serialisation across nodes, not duplicated work within one.

**2. Build-ahead already exists and is 100% effective.** `PrepareSpeculativeBlock`
builds h+1 as soon as this node votes for h and expects to lead the next view.
Counted by block number over one round, not by log volume: **1,410 parked,
1,410 hit, intersection 1,410** — no speculation wasted, no hit without a park.
That covers part of what their `hotstuff.leaderTenure` buys. Leader tenure
itself is not implemented here; `LeaderForView` is round-robin.

**3. Their serial execution is 2.1-3.3 µs/tx and their BAL parallel executor
measured at PARITY on this workload.** This client is 3.135 µs/tx — the third
independent implementation in the same band, which is now a fairly strong
statement that per-transfer execution cost is a property of the work rather
than of any of these codebases.

The parity result is the one that changes plans here. This file's rule 22
identified `exec` (47% of the serial path) as the remaining target, and a check
of the code found a Block-STM implementation already present
(`internal/parallel/`, gated by `parallel_evm`, with the storage-wipe defect its
warning cited long since fixed). The natural next round was to enable it and
size the prize. **A second team measuring a different parallel executor at
parity on the same workload is a strong prior that this round returns nothing**,
and the mechanism is statable: at ~3 µs/tx of actual EVM work, per-access
version bookkeeping and an ordered serial merge are of the same order as the
work being parallelised.

That does not settle it — Block-STM and BAL are different designs, and a
refutation would be worth more than a confirmation. But it moves the round from
"size the prize" to "check whether a known-negative result reproduces", which is
a much smaller reason to spend a slot.

- **Rule 24: a per-transaction cost that three independent clients hit within
  50% of each other is a property of the workload, not of the code.** Look for
  the remaining room somewhere other than the thing everyone already agrees on.

## Qualifying rule 22: the cores are idle on average, not during the burst

Rule 22 said recovery optimisations measured ~1% because the work was on idle
cores — node 0 runs at 509% CPU of the 37 threads it is allowed on a 256-thread
box. The utilisation number is right; the *reason* attached to it was too
simple, and a second client team's CPU-pinning finding prompted the check.

This box (EPYC 9B45, Supermicro H14SSL-NT) is 128 physical cores / 256 threads,
SMT siblings paired as `(c, c+128)` — verified from
`topology/thread_siblings_list`, cpu0↔128 through cpu127↔255 — and a single
NUMA node, so there is no locality question. **This fleet does not pin at all**:
no `taskset`, `numactl` or cpuset anywhere in the deploy or bench scripts, only
`--pprof.maxcpu` setting GOMAXPROCS. So the sibling-collision failure that team
hit (contiguous logical ranges putting node i and node i+4 on the same physical
cores) cannot occur here.

What the check did find is that **the fleet is synchronised**, so an average is
the wrong statistic for a burst:

| | |
|---|---|
| GOMAXPROCS per node | 37 (`(nproc+6)/7`) |
| recovery fan-out per node | 28 (`GOMAXPROCS - GOMAXPROCS/4`) |
| blocks imported by all nodes in the same second | **215 / 225 (96%)** |
| peak recovery threads, all 7 importing at once | 196 on 128 physical cores |
| | **1.5x oversubscribed during the recovery phase** |

So the cores are not idle when the fan-out runs. They are idle on a 25-second
average and contended during the milliseconds the optimisation targets.

This does not change rule 22's conclusion, but it corrects its mechanism, which
matters because the mechanism is what gets applied to the next decision:

- **Recovery is not free because cores are available.** They are not, at the
  moment it runs.
- **It is free because it is overlapped with a LONGER leg.** `exec` is 3.135
  µs/tx against a `joinWait` that is a fraction of it, so recovery finishes
  inside execution's shadow whether it is contended or not. At 100% hint
  coverage the workers find memos and exit immediately — the oversubscription
  evaporates — and the total still did not move, which is the measurement that
  distinguishes the two explanations.

- **Rule 22a: "the machine is idle" is an average.** A synchronised fleet
  bursts; check whether the cores are free during the phase you are optimising,
  not over the profile window. And when an overlapped optimisation measures
  zero, the reason is the length of the other leg, not the supply of cores.

## The co-residency premise, measured: the scheduler is imperfect and it barely matters

Rule 22a's arithmetic assumed the scheduler fills physical cores before doubling
up on SMT siblings. `scripts/qs/coresidency.py` measured it during a saturated
round (22,857-tx blocks, 100% occupancy, 35,809 TPS, unpinned, 5,549 samples at
10 ms):

| runnable fleet threads | samples | avg cores shared by 2 DIFFERENT nodes | cores in use |
|---|---|---|---|
| <16 (idle) | 3,931 (71%) | 0.04 | 15 |
| 16–47 | 1,165 (21%) | 1.32 | 44 |
| 48–95 | 333 (6.0%) | 7.01 | 79 |
| 96–159 | 106 (1.9%) | 23.75 | 108 |
| **≥160 (burst)** | **14 (0.25%)** | **49.79** | **120** |

Peak 201 runnable threads, against the 196 the fan-out arithmetic predicted.

**The premise is refuted, and the refutation does not matter.** At the burst
~50 of 128 physical cores carry threads from two different nodes while **only
120 cores are in use** — the scheduler leaves 8 idle and doubles up anyway, so
it is not filling physical cores first. But the contended state is rare: ≥96
runnable threads in 2.2% of samples, ≥160 in 0.25%, and 71% of the time fewer
than 16 fleet threads are runnable at all.

That rarity is not luck, it is the duty cycle, and the phase numbers predict it:

	recovery / import   21.7 / 146.1 ms  = 14.9%
	import / wall       146.1 / 638 ms   = 22.9%
	=> burst share of wall              = 3.4%   (measured 2.2%)

So the upper bound on what pinning could buy here is about **0.5% of wall
clock** — 3% contended × ~50% of threads in cross-node pairs × ~35% lost to
sibling sharing. Against a peer fleet that measured **+26%** from the same fix,
because theirs shared physical cores 100% of the time by construction rather
than 3% of the time by scheduling.

The argument made before this measurement reached the same conclusion, but it
reached it through a claim that turned out to be false — that the scheduler
packs physical cores first. Right answer, wrong premise, which is the third
time this session; recording it because the premise would have been load-bearing
for a different fleet shape. A fleet with a longer recovery duty cycle, or one
sized so its burst is permanent rather than 3% of the time, gets a very
different number from the same scheduler.

Caveat on method: field 39 is the last CPU a thread *ran* on. A thread in state
R may be queued rather than running, so co-residency here is where threads last
ran, not a snapshot of simultaneous execution. It is the right instrument for
"does this scheduler double up", not for exact instantaneous placement.
