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
