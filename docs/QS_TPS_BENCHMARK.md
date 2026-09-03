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

## The transactions root: measured on the fleet, and the benchmark that understated it

The encoding cache (`Transaction.EthEncoded`) ran on the fleet on 2026-09-02,
same configuration as the `coresid` baseline with only the binary changed —
saturated, shard-senders, 60k pool, 22,857-transaction blocks. 73 full blocks:

| phase µs/tx | encmemo | coresid | Δ |
|---|---|---|---|
| `hdr` | 0.084 | 0.101 | −0.017 |
| **`body`** | **0.458** | **0.855** | **−0.397** |
| `recov` | 0.859 | 1.303 | −0.444 |
| `exec` | 3.380 | 3.309 | +0.071 |
| `write` | 0.777 | 0.823 | −0.046 |
| **total** | **136.6 ms** | **154.3 ms** | **−17.7 ms** |

**`body` fell 0.397 µs/tx — 9.1 ms a block — and the prediction recorded before
the round said 0.28.** For once the measurement beat the estimate, and the
reason is a defect in the benchmark that produced the estimate.

### The benchmark's transactions were unsigned

`benchTxs` built transfers with V, R and S unset. A transfer encodes to **36
bytes that way against ~110 signed**, because R and S are 32 bytes each. The
transactions root's cost is dominated by encoding its leaves, so the benchmark
was measuring about a third of the real work and understating every
encoding-side saving in proportion. It is fixed, and the comment says why.

- **Rule 25: a synthetic transaction without a signature is not a transaction.**
  It is two thirds of one, and the third it is missing is the expensive third.
  The bias runs toward concluding an encoding optimisation is not worth doing.

### What this round does NOT license

`recov` fell 0.444 µs/tx and **that is unattributed**. `N42_SENDER_TRACE=1` was
not set for this round, so there is no hint-coverage figure for it, and this
file already records that coverage alone moves `recov` by ±0.35 (4.5% → 27%
coverage cost +0.349). Reading the code, the encoding cache cannot touch that
path: `verifyBlockSenders` returns early when `From()` is nil, `applySenderHints`
does map lookups, and the recovery workers run secp256k1 — none of them encode.
So it is most likely round-to-round variation.

Which means the **−17.7 ms total is not the change's effect**. The defensible
figure is the −9.1 ms in `body`, about 6% of the import. Quoting the total would
be folding an unexplained phase into a result, which is the error this file has
already recorded three times in other forms.

The round's win2 also collapsed to 1.5% occupancy on exhausted supply; only
win1 and the full blocks in it were used.

### Where the whole line ended up

| approach | wall | CPU | verdict |
|---|---|---|---|
| sequential encode (before) | 2.01 s | 2.33 s | baseline |
| parallel encode, 4 workers | 1.44 s | 3.58 s | **rejected** — buys wall with CPU |
| parallel encode, chunked | 1.61 s | 2.93 s | rejected, same reason |
| **cache the wire encoding** | **1.27 s** | **1.37 s** | shipped |

The parallel versions were measured, committed, and reverted. The one that
survived removes the work instead of redistributing it: a transaction that
arrived over the wire was decoded from its canonical encoding and the decoder
was dropping those exact bytes, so the root re-derived them for every leaf of
every imported block.

### What is left in the transactions root, and one candidate that failed

With the encoding cached, a profile of what remains says the work is now real:

| | share of the root's cost |
|---|---|
| `sha3.keccakF1600` | **62.0%** |
| `sha3.(*state).Write` | 19.7% |
| `runtime.memmove` | 11.3% |

Keccak at 62% is the MPT's own arithmetic and there is no waste left to remove
from it — only splitting the trie could help, and that is the peer's technique
with the inlining hazard still unaddressed.

The 19.7% in `Write` looked like a free win. `HashBuilder.completeLeafHash`
issues one `Write` per piece of a leaf's header **and one per compact-key byte**:
a transactions key is 2-3 bytes, but a state trie's hashed key is 33, so a leaf
could cost 36 calls into the sponge for a 40-byte header. Assembling the header
in a scratch buffer and issuing a single `Write` is byte-identical by
construction, and all four trie/commitment suites passed with it.

**It measured as nothing, and it was reverted.** Five repeats of each:

	with the change    median 11,945,008 ns   range 11.39 - 12.50 M
	without            median 11,229,275 ns   range 10.29 - 12.78 M

Median 6.4% *worse* with ranges overlapping almost entirely — noise. An earlier
single pair had shown −4.3% and was about to be committed on that basis.

The mechanism was real and the arithmetic behind it was wrong: Go's
`sha3.(*state).Write` copies small inputs into the sponge buffer and only runs
`keccakF1600` when 136 bytes have accumulated. Thirty-six one-byte writes are
therefore thirty-six small copies, not thirty-six permutations, and a 40-byte
header costs the same number of permutations however it is split.

- **Rule 26: a plausible mechanism plus one A/B pair is not a result.** Repeat
  the pair before committing; this file has now recorded the same error at the
  level of memory, of CPU share, and of a stale warning, and this is the level
  of ordinary benchmark noise.

### Where the round leaves the import

| phase | µs/tx | share |
|---|---|---|
| `exec` | 3.380 | **56.5%** |
| `recov` | 0.859 | 14.4% |
| `write` | 0.777 | 13.0% |
| `body` | 0.458 | 7.7% |
| `valid` + `hdr` | 0.247 | 4.1% |

`exec` is now more than half, and three independent clients measure per-transfer
execution within 50% of each other (rule 24) with a BAL parallel executor at
parity — so it is not obviously reachable. `write` has never been broken down:
`blockwrite phases` logs at Info only above `slowBlockThreshold`, and a
follower's 17.8 ms write stays under it, so a round with `N42_SLOW_BLOCK_MS`
lowered is what that needs.

## `write` decomposed: 72% of it is one call

`write` is 13% of the import and had never been split, because `blockwrite
phases` logs at Info only above `slowBlockThreshold` (50 ms) and a follower's
write is ~18 ms. `N42_SLOW_BLOCK_MS=1` lowers only the log LEVEL — the fields
are computed either way — so the round stays comparable. `--profiling` was
deliberately not used: it samples inside the critical path being measured.

63 full blocks, 22,857 transactions each:

| part of `write` | ms | share |
|---|---|---|
| **`block`** (`rawdb.WriteBlock`) | **13.34** | **72.3%** |
| `commit` | 2.09 | 11.3% |
| `receipts` | 0.83 | 4.5% |
| `chgset` | 0.63 | 3.4% |
| `ce`, `qflush`, `state`, `post`, `qmeta`, `begin`, `snap`, `root2` | <0.5 each | 6.0% |
| **total** | **18.45** | |

**13.34 ms in one call — 0.58 µs a transaction, 9.4% of a 142.4 ms import.**
That is the largest single identified item outside execution, and unlike `exec`
there is no cross-client evidence that it is irreducible.

Where it goes, by reading: `WriteBlock` → `WriteBody` → `WriteTransactions`,
which per transaction does a `MarshalCompactStorage`, a `types.CopyBytes`, and
an MDBX `Append`. The copy looks redundant — `Append` reaches mdbx-go's `Put`,
which memcpys into the page during the call — but `kv.RwTx` has several
implementations (memdb, layered, remotedb) and any one of them retaining the
slice would make removing it a corruption bug. By magnitude the copy is ~1 ms
of the 13.34 anyway; the bulk is the compact marshal plus the B-tree insert.

**Further attribution needs a profile of the node under load, not more code
reading**, and that is where this stops rather than guessing.

### Two other results from the same round

**Hint coverage was 2.8% here against 27.0% in the previous round** — same
binary, same configuration, same generator settings. A 10x swing in a quantity
this file had started treating as a property of the routing mode. It also
retroactively vindicates refusing to attribute the previous round's `recov`
drop to the encoding cache: had that been claimed, this round would have
contradicted it.

- **Rule 27: hint coverage is not reproducible round to round.** Any figure
  derived from it needs the coverage logged in the same round, and a phase that
  moves with it cannot be attributed to anything else.

**Conditions, logged rather than assumed.** Major faults ran 25-38k/s through
the round with the neighbouring 33 GB job at 28-31 GB resident; a peer measured
26k/s and this file measured 129k/s earlier the same day, so the contamination
level swings ~5x and is not a constant anyone can assume. TLB shootdowns were
6-18/s against an 18/s idle baseline — no TLB effect on this box, which a peer
independently confirmed at 11/s under a full flood.

And win2 produced **zero** transactions across 240 blocks: supply exhaustion,
not a chain limit. Only win1 and the full blocks inside it were used.

## A-B-A: the compact codec fix is worth 1.79 ms a block, and the total cannot see it

Every single-pair comparison in this file has been confounded by something that
drifted between the two rounds — memory pressure, hint coverage swinging 10x,
block interval 2.3x, and a neighbouring 25M-block build whose residency went
26 → 47 → 74 → 25 → 51 GB inside two hours. So this one was run as **A-B-A**:
old binary, new binary, old binary, back to back in one sitting, with
conditions sampled every 5 s throughout.

| leg | binary | `block` ms | `write` ms | `total` ms | TPS | datc peak |
|---|---|---|---|---|---|---|
| A | encoding cache only | 13.30 | 19.86 | 141.5 | 40,000 | 74.0 GB |
| **B** | **+ uint256 fix** | **11.37** | **17.57** | 141.3 | 44,876 | 53.8 GB |
| A2 | encoding cache only | 13.03 | 18.02 | 143.1 | 35,428 | 50.8 GB |

**The two bookends agree to 0.27 ms on `block`**, so the baseline is stable for
that phase and the difference is attributable: **B is 1.79 ms below the mean of
the two A legs.**

**The prediction was 3.2 ms and the measurement is 1.79** — 56% of it. The
falsifiable clause written before the round named the reason: "the bench runs
unloaded, so the marshal's share of 13.34 ms may be smaller under fleet load
where MDBX contention dominates." Direction right, magnitude overestimated 1.8x,
for the anticipated reason.

### What the round refuses to support

**`total` did not move.** 141.5 / 141.3 / 143.1 — the B leg is 1.0 ms below the
A mean, against 1.6 ms of spread between the two A legs themselves. A 1.79 ms
saving in a 142 ms import is 1.3%, and this rig cannot resolve that in the
total. The phase measurement is the result; the total is not.

**TPS is not the result either**, and it is the trap this design was built to
catch. B measured 44,876 against A's 40,000 — a 12% gain that would have been
published from an A/B pair. A2, the same binary as A, came back at 35,428. The
three TPS figures track the neighbouring job's residency (74 / 53.8 / 50.8 GB)
more closely than they track the binary.

**Other phases drifted where `block` did not.** A2's `body` was 0.665 µs/tx
against 0.460 and 0.469 in the other two legs, and its `exec` was 3.213 against
3.530 and 3.484. So "the bookends agree" holds for `block` specifically and
must not be generalised to the whole table.

- **Rule 28: bookend an A/B with a repeat of A.** Three legs cost one extra
  round and convert "the number moved" into "the number moved more than the
  baseline drifted" — which on this box, today, was the difference between a
  12% claim and a 1.3% one.

## The parallel-EVM A-B-A did not measure a speed. It found a consensus bug

`exec` is 56% of a follower's import — the largest remaining item — and
`internal/parallel` has implemented Block-STM for a long time behind a config
key the bench harness could not reach. A `--parallel-evm` flag made it
reachable, and the first round ever run with it halted the chain.

One binary; the flag was the only variable. Bookend quantity declared before
the round: `exec` µs/tx, the two sequential legs to agree within 10%.

| leg | execution | bad blocks | full blocks | `exec` µs/tx | TPS |
|---|---|---|---|---|---|
| A | sequential | **0** | 78 | 3.380 | 36,288 |
| **B** | **Block-STM** | **12** | **0** | — chain halted | **0** |
| A2 | sequential | **0** | 99 | 3.397 | 36,721 |

The bookends agree to **0.5%**, so the middle leg's failure is attributable to
the flag and not to the box, the neighbour or the harness.

Under Block-STM, six of seven nodes rejected the same two blocks and the
measurement window produced zero blocks. **The failure is not a state-root
mismatch and not a storage-wipe case:**

```
could not apply tx 0 from block 13618674:
insufficient funds for gas * price + value:
address 0x8AD44d589C5f3fF71E8112a71cd5cA1cFEbca6Db have 0 want 210000000000001
```

**That first diagnosis was wrong and is corrected below.** It was published to
two peer sessions and committed before the failing transactions were tabulated.

### The mechanism, after actually counting

| failing block | tx index | occurrences |
|---|---|---|
| 13618674 | **0** | 9 |
| 13618675 | **0** | 2 |
| 13618675 | 2500 | 1 |

**Eleven of twelve rejections are on tx 0** — the block's first transaction.
Nothing earlier in the same block can have credited that sender, so the balance
must come from the parent state and "cross-transaction write visibility" cannot
be the cause. `MVS.Read` confirms it from the other side: at `txIndex 0` the
version search returns `found=false` unconditionally, so tx 0 always falls
through to the base reader.

The cause is one level lower than `internal/parallel`. `ProcessParallel` hands
**every Block-STM worker the same state reader**, with a comment that records
the requirement rather than checking it:

```go
// NOTE: stateReader (base reader) must be safe for concurrent reads.
pReader := parallel.NewParallelStateReader(stateReader, executor.MVS(), rw, txIndex)
```

It is not safe. That reader is a `PlainStateReader` over an `MdbxTx`, and
`MdbxTx.GetOne` takes a per-bucket cached cursor out of an **unsynchronised
map** and calls `SeekExact` on it — so every worker shares one MDBX cursor,
which is not thread-safe even when the map access is benign.

`TestGetOneIsNotConcurrencySafe` in `lib/kv/mdbx` proves it with the race
detector rather than by argument: twelve `WARNING: DATA RACE` reports on
`statelessCursor` reached from `GetOne`, the exact path the workers take. It is
opt-in (`N42_PROVE_MDBX_RACE=1`) because it demonstrates a real race and would
otherwise fail `make race` for a defect it documents rather than introduces.

So the fix is not in `internal/parallel`: a parallel executor needs a reader
per worker, or its reads serialised.

- **Rule 31: tabulate the failures before naming the mechanism.** "Insufficient
  funds" read as an intra-block ordering bug at a glance, and one `grep -c` on
  the transaction index refuted it. The wrong version was already in a commit
  message and in two peers' notes by then.

### This overturns an earlier entry in this file

An earlier section records verifying that the SELFDESTRUCT/CREATE2 storage-wipe
defect the runtime warning named was fixed — `ParallelStateWriter.CreateContract`
records the marker, `ReadAccountStorage` shadows stale slots, the merge applies
the wipes, and `wipe_shadow_test.go` passes five ordering cases — and softening
the warning to "not consensus-audited end to end".

**That verification was correct and the conclusion drawn beside it was too
generous.** The distance between "the named defect is closed" and "the path is
usable" was larger than the section allowed for. The warning now states the
measured failure instead of the absence of an audit, and the flag's help text
says the same.

- **Rule 29: a stop condition declared before the round is what turns a failed
  round into a finding.** The prediction file said "any state-root mismatch,
  bad-block report or import error ends the experiment regardless of the
  timing". Without that line, `win1: blocks=0 TPS=0` reads as a harness fault
  and gets rerun. With it, the monitor raised it as the primary signal within
  a minute.
- **Rule 30: A-B-A certifies a failure as well as a difference.** The design was
  adopted to separate signal from drift; two clean sequential legs bracketing a
  halt also prove the halt was not the environment.

And the flag was worth adding for this alone: **a path that cannot be exercised
cannot be found to be broken.** This one had been unreachable from the rig for
as long as it has existed, and it survived ten minutes of 22,857-transaction
blocks.

### The same defect is in a feature that is not marked experimental

Chasing the parallel-EVM halt to `MdbxTx.GetOne`'s shared cursor raised an
obvious follow-up: is any other path concurrent over one transaction? The
codebase mostly gets this right — `ConcurrentMPTRootComputer`,
`engine_state_adapter` and `witness_replay_worker` all open a **per-worker
RoTx**. One does not.

`StatePrefetcher.Prefetch` starts `runtime.NumCPU()/2` I/O workers — **128 on
the bench box** — and hands every one of them the *same* `state.StateReader`.
The caller then executes the block with that same reader on the main goroutine:

```go
prefetcher.Prefetch(concreteBlock, reader)   // N goroutines read through `reader`
...
bc.process.Process(concreteBlock, ibs, reader, writer, blockHashFunc)
```

So the prefetch workers race each other *and* the executor on one MDBX cursor —
the identical mechanism that halted the chain under `--parallel-evm`, in a
feature gated by an ordinary config key (`prefetch`) rather than by an
experimental flag.

It has never been observed in a round for one reason: **`Prefetch` defaults to
false and no bench round has set it.** That is luck, not safety.

Both `internal/prefetcher.go` and the switch site in `internal/node/node.go` now
say so, and enabling it logs a warning. The fix is not a comment — each worker
needs its own transaction — and it is not a one-line change either: at
`NumCPU/2` the worker count is chosen for cores, and "one RoTx per worker" needs
it chosen for transactions instead.

- **Rule 32: when a defect is found on one path, grep for the same shape on the
  others before moving on.** The parallel executor advertised its requirement in
  a comment (`must be safe for concurrent reads`) and the prefetcher did not
  advertise it at all, which made the unmarked one the more dangerous of the
  two.

### What the fix has to look like, and why it is not a one-liner

`lib/kv/mdbx` opens no `NOTLS`, and `BeginRo` calls `runtime.LockOSThread()`.
A read transaction is therefore **bound to an OS thread**, which settles the
shape of any fix: a concurrent reader cannot share one transaction and cannot
hand one between goroutines. Each worker must open its own `BeginRo` on its own
goroutine — exactly what `ConcurrentMPTRootComputer`, `engine_state_adapter` and
`witness_replay_worker` already do.

Two things make it more than a mechanical change:

- **Worker count.** `StatePrefetcher` picks `runtime.NumCPU()/2` — 128 on this
  box. One read transaction per worker is a very different resource than one
  goroutine per worker, so the count has to be chosen for transactions.
- **Snapshot consistency.** An MDBX read transaction sees the last committed
  state at `BeginRo`. Workers opening at slightly different moments could
  straddle a commit and disagree. `evmRecord` already runs under a read
  transaction, so the executor's own view is a fixed snapshot; the workers' views
  must be pinned to the same one rather than opened opportunistically.

Neither is hard, but both are consensus-critical, and this file has spent a day
demonstrating what happens to a claim made without measuring. The defect is
recorded, proven and warned about; the fix is a separate piece of work with its
own round.

## Correction: this rig's flood is a SINGLE-SINK workload, and the cross-client comparisons assumed otherwise

`cmd/txflood` has no recipient option. Every flood transaction is signed to one
hard-coded address:

```go
dead := types.HexToAddress("0x000000000000000000000000000000000000dEaD")
...
raws[s*(*perTx)+j] = signOne(keys[s], addrs[s], dead, base+uint64(j), ...)
```

So a full 22,857-transaction block on this rig writes roughly **1,201 distinct
accounts** — 1,200 senders, each touched ~19 times, plus the one sink. A peer
client's 163,000-transaction block spread over 2,000,000 recipients writes
~163,000. **About 136x more state per block, for a workload this file has been
comparing per-transaction costs against.**

### What this invalidates

The section above headed "the gap is not execution" compared this client's
per-transaction costs with a peer's and concluded that execution matches within
15% while per-block overhead is 5.5x cheaper on their side. **Those numbers are
not measured on the same workload**, and the direction of the error is against
this rig's favour: a block that writes 1,201 accounts does far less trie and
storage work than one writing 163,000, so this client's `exec`, `write` and
`block` figures are all flattered relative to theirs. Rule 20 (normalise per
transaction, split execution from the rest) still holds as a method; the
comparison it was derived from does not.

### And it explains the neighbour sensitivity

A separate measurement had this client's `exec` rising 4.9% while the datc job
ran and a peer's rising 2.5x. Two explanations were offered — "you are faster so
a stall is a larger fraction of your cost" (mine) and "your hot state is
anonymous Go heap, ours is file-backed mmap" (theirs). **Neither needed to be
true.** A working set of ~1,201 accounts stays resident under any eviction
pressure; one of 2,000,000 cannot. The immunity is the workload, not the client.

### How the premise went unchecked

A peer stated hours earlier that "yours spreads transfers over up to 2,000,000
hash-derived recipients; mine sent every transfer to one address", and changed
their generator to match what they believed this one did. **It was accepted here
without reading `txflood`.** The recipient is nine lines from the signing call.

- **Rule 33: read your own generator before comparing workloads.** Every
  cross-client per-transaction figure in this file predates that reading, and a
  peer re-tooled their benchmark on the strength of a description of this rig
  that was wrong.

What a comparable rig needs is a recipient option in `txflood` — hash-derived,
count configurable — and every cross-client number in this file re-measured
with it. Until then the per-transaction costs recorded here describe a
single-sink workload and should be quoted as such.

## The spread round: at a real write set, every priority in this file changes

The first round with `-recipients 22857` — each block writing ~22,857 distinct
accounts instead of ~1,201, recipients reused across blocks so the fixture grows
by 22,857 accounts rather than three million. Same binary, same configuration,
same pool, same rate as the single-sink baseline; the workload is the only
change.

| | single sink (pe-A2) | **spread (22,857)** | |
|---|---|---|---|
| TPS | 36,721 | **12,571 / 14,476** | −2.7x |
| blockTime | 0.622 s | **1.579 / 1.818 s** | |
| occupancy | 100% | 100% | |

| phase µs/tx | sink | spread | ratio |
|---|---|---|---|
| `hdr` | 0.088 | 0.088 | 1.00x |
| `body` | 0.469 | 0.471 | 1.00x |
| `recov` | 0.835 | 0.724 | 0.87x |
| `exec` | 3.397 | 5.670 | 1.67x |
| `valid` | 0.168 | 0.171 | 1.02x |
| **`write`** | **0.717** | **23.101** | **32.2x** |
| **total** | **143.0 ms** | **769.1 ms** | **5.38x** |

### The prediction was wrong, and its falsifiable clause said why

Recorded before the round: *"`block` / `write` RISE SUBSTANTIALLY. `block` is
`rawdb.WriteBlock`; ~19x the distinct account writes per block is the whole
change."* And: *"If `block` does not rise, my model of where its 13 ms goes is
wrong: the cost would not be per-account-written."*

| within `write` | sink | spread | ratio |
|---|---|---|---|
| **`block`** (`rawdb.WriteBlock`) | 13.03 ms | **10.83 ms** | **0.83x** |
| **`commit`** | 2.22 ms | **283.50 ms** | **128x** |
| **`chgset`** | 0.29 ms | **128.04 ms** | **442x** |
| `receipts` | 0.72 ms | 0.89 ms | 1.24x |

`block` did not rise because `rawdb.WriteBlock` writes the *block* — header and
transactions — and a block's serialised size does not depend on how many
accounts its transactions touch. What rises is what is proportional to accounts
changed: the MDBX `commit` and the changeset write.

### What this does to the day's work

**Everything optimised today lives in phases that are 1-2% of a realistic
import.** `body` is 0.471 µs/tx of 33.648 — **1.4%** — and the transactions-root
encoding cache and the compact-codec `uint256` fix both target it. They are
still real reductions in CPU and allocations, and the A-B-A that measured the
second one at 1.79 ms stands. But they were aimed by a phase table taken on a
workload that did almost no state work, and on the workload that does, the
target was never there.

The honest ordering at a real write set is `write` 69%, `exec` 17%, everything
else 14% — against `exec` 56%, `write` 13% on the single sink. **`commit` alone
is 37% of the import**, and it had been 1.5%.

- **Rule 34: a phase table is a property of the workload, not of the client.**
  Every priority in this file above this section was derived from a table whose
  dominant term was an artifact of paying one address.

### Not answered by this round

The second prediction — that per-transaction cost would start rising with block
size once the write set was real, as a peer measures on theirs — **cannot be
tested here**: the chain ran at 100% occupancy throughout, so all 67 full blocks
were 22,857 transactions and there is no size range to bin. It needs a round
paced below saturation.

### Qualification: the spread round ran under page-cache starvation

The section above reports the spread workload's phase table as the cost of a
real write set. Its conditions log, read afterwards against a peer's finding,
says something narrower.

| spread round, nodes up, n=118 samples | |
|---|---|
| machine major faults/s | median **16,067**, peak **203,010** |
| available memory | median 60 GB, **minimum 4 GB** |
| neighbouring datc job | 55-67 GB |

A peer traced their own import doubling to exactly this: at full blocks their
engine thread took 26 MB/s of disk reads with `rchar = 0` — mmap page faults on
MDBX state pages — with the machine at 55,444 major faults/s against 837/s on
partial blocks, and `folio_wait_bit_common` 31% of the thread's wchan samples.
**My round peaked at 203,010/s and ran out to 4 GB of free memory.**

Every phase in that table is **wall time**. `exec`, `commit`, `chgset` and
`state` cannot distinguish executing from waiting on an evicted page, and under
those conditions an unknown share of each is the latter.

What survives and what does not:

- **The workload comparison's direction survives.** A spread write set is more
  expensive than a single sink, and dramatically so. Nothing about that needs
  page-cache pressure to be true.
- **The magnitudes do not.** "`write` is 69% of the import", "`commit` is 37%",
  "`exec` rose 1.67x" describe a node whose state pages were being evicted
  faster than it could fault them back. On a box with the page cache to hold its
  working set, those numbers are unknown.
- **And the causation runs the wrong way to call it a simple confound.** A
  spread workload *causes* the memory pressure — more distinct accounts is a
  larger resident working set — so the pressure is partly intrinsic to the
  workload and partly the neighbour's. The two cannot be separated from this
  round.

- **Rule 35: a wall-clock phase table taken under page-cache pressure measures
  the pressure.** The instruments that tell them apart are per-thread D-state
  share, the thread's `wchan`, and `/proc/<pid>/io` `read_bytes` against
  `rchar` — none of which appear in a Go pprof profile, because the thread is
  not running when it happens.

The honest next step is to repeat the spread round when the box is quiet and the
fleet's files fit in the page cache, and compare. Until then the single-sink
table is a measurement of a workload nobody runs and the spread table is a
measurement of a machine under pressure.

### The retention genus: a cache sized in blocks, not in bytes

A peer's heap profile found 4.24 GB of their execution layer — 59.5% of its live
heap at the end of a flood — in decoded transactions of every block imported
since the flood started, retained by a subscriber rather than by the tree.
Grepping for the same shape here (rule 32) finds two:

| | sized by | at 22,857-transaction blocks |
|---|---|---|
| `blockCacheLimit = 512` | **block count** | 1.2 GB/node at 110 B a transaction, **3.3 GB** at 300 |
| `latestBlockCh` buffer 50 | **block count** | 0.12 – 0.3 GB/node |

`blockCache` is an LRU of 512 **fully decoded** blocks. A decoded transaction is
several times its wire encoding — `uint256` fields, not bytes — so the cache's
footprint is a fixed count multiplied by a quantity that grows with block size.
On a chain of small blocks 512 is nothing; at bench block sizes it is gigabytes,
and nothing in the code says so.

This is consistent with the `CanonicalTransactions` figure already in this file:
**2,474 MB live** in a morning heap profile, 84% of it reached through
`HasBlockAndState → GetBlock`. That path was fixed and no longer decodes, but
**the fix removed a filler, not the cache** — legitimate `GetBlock` callers
still populate all 512 slots.

**Not measured**: how full the cache runs now that its largest filler is gone.
The next round's heap profile answers it directly, and until then this is a
structural finding with a magnitude estimate, not a measurement.

- **Rule 36: a cache sized in items retains bytes.** Where the items are blocks,
  transactions or accounts, the ceiling moves with the workload and the constant
  in the code stops describing it. Both of today's retention findings — mine and
  the peer's — are this, and the peer's `PacketCache` analogue on my side (256
  blocks, 796 MB measured) is a third.

### The 5.6 GB is not reclaimable, and the round that would have measured it never ran

`--mobileverify=false` was added to the bench fleet on the strength of a peer's
RocksDB finding and a 796 MB heap line. **Every node then refused to start:**

```
fatal: Error starting protocol stack: mobileverify must be enabled on a
       mining node when MobileAnchorTime is configured
```

This chainspec sets `mobileAnchorTime`, which makes `MobileRegistryRoot`
mandatory in HotStuff headers, so a mining node without the pipeline would
propose headers every follower rejects. `node.go` fails fast on exactly that and
is right to.

So the finding survives and the fix does not: **PacketCache's 796 MB a node,
5.6 GB across seven, is real and is not reclaimable by turning the feature
off on this chain.** The lever is the retention — `MobileVerifyCfg.PacketWindow`,
256 blocks, no CLI flag today — and choosing a smaller window needs a round to
say which one, which is a different piece of work from the one attempted.

**The process failure is mine and is the more useful half.** I changed a
fleet-wide launch argument, deployed it, announced the round to two sessions and
started it — without once starting a single node with the new argument. The
round aborted cleanly (`RPC not ready on all 7 nodes`), the guard did its job,
and the cost was one slot and about half an hour. A smoke test of one node would
have cost forty seconds; the revert was verified with exactly that, seven of
seven up.

- **Rule 37: start one node before you start seven.** A launch-argument change
  is not a code change with a test suite behind it, and the harness's own abort
  message reports it as an environment fault rather than as a bad flag.

The conditions during the attempt are worth recording anyway, because they were
not the cause and would have been blamed: the neighbouring `datc` job went
41.2 → 84.8 GB during the round, and available memory never fell below 48 GB.
The failure was in the first two seconds of node startup, before any of that
mattered.

### `--mobileverify.packet-window` smoke-tested; the round it enables is waiting for a regime

The flag committed as "NOT YET SMOKE-TESTED ON A NODE" now has been, the way
rule 37 requires and the way the change it replaced should have been:
`--mobileverify.packet-window 4` reaches the process, `node.go` logs
`window = 4` at startup, and all seven RPCs answer. The commit message's
disclaimer was true when written and is superseded here rather than by rewriting
history.

Two things the smoke test also caught, neither of them the flag:

- The nodes read **DOWN** at 20 s and **OK** at 60 s. A fleet is not up when the
  processes exist; the first check was simply too early, and a round that
  branched on it would have aborted a healthy fleet. The harness's own
  `RPC not ready` abort has the same shape.
- The two `window = 256` lines in the log were from **16:29 and 01:11**, earlier
  runs appended to the same file. Reading them as this run's output would have
  said the flag did not work. Timestamps, not `tail`.

**The round is not being run yet.** `datc` is at 82.9 GB with 47 GB available
and the page cache already squeezed; a peer's three legs under 52-79 GB of it
were unreadable and they said so rather than quoting them. A conditions-and-
retention round taken on a box already starved by the neighbour cannot separate
my fleet's consumption from the neighbour's, which is the whole question. It
waits for the regime a peer's launcher also waits for: `datc` under 45 GB and
80 GB available.

## Round 5 — the memprobe round on a quiet box

The round the previous section was waiting for. It fired automatically at
04:22:33 on the first clean-box conditions of the day: `datc` at 0 GB, 115 GB
available, major faults 0-2/s against the 16,067-203,010/s of the confounded
spread round. Two windows, both at 100% occupancy: 14,095 TPS / 1.622 s and
14,857 TPS / 1.538 s.

**38. The qualification I put on the spread table does not apply to my client.**
After a peer found their own import waiting on evicted state pages, I qualified
my spread table as describing "a node under page-cache starvation" and said its
magnitudes could not travel until re-measured on a quiet box. That was the right
methodology and the wrong conclusion. On the quiet box the table reproduces
within 4% everywhere:

| phase | quiet box | datc at 55-67 GB |
|-------|-----------|------------------|
| exec  | 5.474 | 5.670 |
| write | 23.165 | 23.101 |
| total | 756.6 ms | 769.1 ms |
| commit | 304.00 ms | 283.50 ms |
| chgset | 134.75 ms | 128.04 ms |

The neighbour cost my client nothing measurable. `write` at 69% of the import
path, and `commit` at ~40%, stand unqualified. A qualification is a claim too,
and it needs a measurement before it travels just as much as the number it
qualifies does.

**39. My import probably does not wait on pages — but the D-state half of that
claim was never measured, and is withdrawn.** WITHDRAWN AND CORRECTED: I
reported "zero threads in D state across 78 samples" and even put a p-value on
it. The sampler measured nothing. It parsed `/proc/<tid>/stat` by stripping
through `<comm>) ` and then taking field 3 of the remainder — the ppid — so the
`case` matching D/R/S never matched and all three counters were structurally
zero. The tell was in the output I read and quoted: D=0, R=0, S=0 with
total=73. Three states summing to zero over 73 live threads is not a finding,
it is a broken instrument, and I should have checked D+R+S against `total`
before believing a number I liked. The sampler now refuses to write a file of
zeroes (verified by re-introducing the bug and watching it exit 3) and takes a
`DSTATE_PID` override so the parse can be proven on any live process first.

What survives is the evidence that did not come from that sampler:
`read_bytes` of 0-12 KB/s, read straight from `/proc/<pid>/io`, and IPC 2.51
from `perf`. Both point the same way — no meaningful disk reads, CPU-bound
rather than stalled — and the phase-table invariance in 38 is independent of
all of it. So the conclusion stands on other legs; it is the D-state
measurement specifically that does not exist, and no argument may rest on it.

**40. `blockCache` holds nothing — the gigabyte estimate is retracted.** I
committed that the `blockCacheLimit = 512` hazard "may already be empty, in
which case the constant is a hazard that is not firing and the gigabyte estimate
must not travel as a number", and that a heap profile would settle it. It does:
no LRU or block-cache entry appears anywhere in the top 40 of a 1,960 MB heap.
The constant is still a structural hazard — nothing bounds it by bytes — but it
is not currently costing anything, and the estimate is withdrawn.

**41. The largest consumer is QMDB's in-memory index, not the caches I had been
chasing.** `qmdb.newMapIndexSized` holds 761 MB, 38.8% of the heap, more than
the next six entries combined. `PacketCache` measures 111 MB here — but this
profile was taken ~76 full blocks into a 256-block window that had just been
filled with 663 empty decay blocks, so the window was roughly 30% full of real
packets. It neither confirms nor refutes the 796 MB steady-state figure, and
must not be quoted against it. Measuring PacketCache honestly needs a profile
taken after 256 consecutive full blocks.

## Round 6 — the commit A-B-A, which reports no number

Pre-registered before the round: `commit` in the blockwrite line is
`dUpdate - dBegin - dInClosure`, i.e. mdbx_txn_commit itself, measured at
304 ms/block. The round split it into page-write volume versus fsync latency
with N42_MDBX_SYNC=safe-nosync, bookended durable / safe-nosync / durable,
fresh sender offset per leg, write_bytes sampled from /proc/<pid>/io
throughout so the volume half did not depend on the A/B.

**42. The bookend failed and the round publishes nothing.** A1 272.2 ms,
A2 176.7 ms, both durable, 42.6% apart against a stop condition of 15%. The
pre-registered rule says a round whose bookends disagree is noise and reports
nothing, and that rule was written after a single A/B pair showed -4.3% and
five repeats showed +6.4%. It costs more to honour when the treatment leg looks
dramatic than when it does not, which is the only circumstance in which it is
worth anything.

**43. The first leg after a box handover is not a valid baseline.** The
bookends did not disagree randomly: A1 wrote 252.6 MB per block against 148.2
and 154.1 for the two legs after it, 67% more, and its commit and its
within-leg drift (-44%, against -17% in A2) both follow that. A1 started
minutes after a neighbouring fleet released the box, so it ran against a page
cache full of someone else's data and paid to fault its own working set back
in. B and A2 ran warm. Every A-B-A this harness runs after a handover has this
shape, and the fix is a warm-up leg that is thrown away, not a longer decay --
the decay produces empty blocks and warms nothing that a full block touches.

**44. What the round saw, labelled as an observation and not as a result.**
On fast blocks (<=3 s spacing) commit was 320.1 ms in A1, 34.5 ms in B, 179.7
ms in A2. The treatment direction is unambiguous and its magnitude is several
times the baseline spread, so fsync -- not page-write volume -- is where
commit's time goes. That is enough to say the volume model behind P2 was wrong
and to say why: it conflated pushing 252 MB into the page cache (memcpy speed,
tens of ms) with getting those bytes onto the device (fdatasync, hundreds).
It is NOT enough to publish a percentage, and no percentage from this round
should be quoted. The re-run that would earn one: discard a warm-up leg, then
A-B-A-B on warm state.

P1 survives either way: write volume was 252.6 MB/block cold and ~150 MB warm,
both inside the pre-registered 60-400 MB band, and it was measured directly
rather than inferred from the A/B. The prediction file also claimed P1 and P3
could not both be true. That clause was wrong and is withdrawn: the volume is
real AND the time is the fsync's, because the volume is precisely what makes
the fsync slow.

And whatever a re-run shows, safe-nosync weakens durability -- a crash or power
loss rolls chaindata back several blocks, which the node must then re-sync.
Nothing here is a throughput recommendation.

## The metrics that are collected and served nowhere

Found while trying to test H-spill directly, and worth its own note because it
will waste the next person's round the same way it nearly wasted mine.

Three metrics packages coexist:

  common/metrics    -- what `--metrics` actually serves. SetupMetrics calls
                       its Setup(), which handles /debug/metrics/prometheus
                       against ITS DefaultRegistry. 1142 lines on a live node.
  internal/metrics  -- RegisterSystemMetrics(), Go runtime and system gauges.
  lib/metrics       -- everything the storage layer counts: db_pgops (newly,
                       cow, clone, split, merge, spill, unspill, wops), kvcache,
                       txpool, layered, disk, mem. Registered into ITS OWN
                       defaultSet, which reaches an HTTP handler only through
                       lib/metrics.Setup() -- and that function HAS NO CALLERS
                       anywhere in the tree.

So db_pgops{phase="spill"} is updated on every single MDBX commit and is
readable by nothing. `--metrics` will not show it; neither will the pprof port.
Wiring it is a one-line change (register lib/metrics' defaultSet, or call its
Setup) and is not made here, because the fleet was mid-round and a rebuild would
have cost the bookend.

Two consequences worth separating. The instrumentation one: any round that
plans to read a storage-layer metric must check the endpoint FIRST, on a leg it
is willing to throw away -- a warm-up leg caught this after ten minutes instead
of after fifty. The performance one: MdbxTx.Commit calls CollectMetrics on
every commit, which makes two cgo calls -- env.Info() and tx.Info(true), the
latter scanning the reader lock table -- to maintain gauges with no reader. That
lands inside the measured `commit` window by construction. It is a floor under
every leg equally rather than a confound, but nobody has measured how large a
floor, and it is being paid for nothing.

## Round 7 — the re-run: one bookend held, one did not, and H-spill survived

Design earned by round 6: a warm-up leg run and DISCARDED, then A-B-A-B on
warm state. Five legs, fresh sender offset each, write_bytes sampled
throughout.

  leg      sync           n   chgset   commit   wtotal   MB/blk
  warmup   durable       64    221.8    214.1    608.9    148.0   (discarded)
  A1       durable       49    295.0    174.7    760.0    139.9
  B1       safe-nosync   78    231.4     55.7    453.7    140.9
  A2       durable       67    163.3    159.0    432.2    141.4
  B2       safe-nosync   90    189.5     45.4    346.9    139.5

**45. The warm-up leg worked, and the round still publishes no number.** The A
bookend came in at 9.4% against round 6's 42.6%, so discarding a leg did fix
the post-handover cold start. The B bookend failed at 20.5%. R1 required both,
so by the pre-registration this round publishes no figure for the fsync split
and I stop pursuing that split with this harness. Two failed bookends is the
rig saying something, not luck twice.

Worth recording why B failed, as a lesson for the NEXT pre-registration and
not as a reason to reinterpret this one: B1 55.7 and B2 45.4 differ by 10.3 ms
in absolute terms and 20.5% in relative terms. The same 10.3 ms sits inside the
A legs' agreement without trouble. A relative-spread criterion is harsh on small
magnitudes and should have been stated as "15% or 20 ms, whichever is larger".

**46. H-spill survives a pre-registered test with a falsifier.** The original
method died -- the spill gauge is exposed nowhere (see the metrics note above)
-- and the replacement was recorded before any A/B leg ran: compare write_bytes
on chgset-spike blocks against normal blocks. Blocks whose chgset spikes run
1021.4 ms against 191.2 ms, 5.3x the time, while writing 151.5 MB against
142.4 MB, 1.06x the bytes. The rival hypothesis (those blocks simply write
more) predicted >=1.5x and is rejected. So the cost RELOCATES inside the
transaction rather than the block doing more work, which is what MDBX spilling
dirty pages mid-transaction looks like.

This result does not lean on the failed bookend: it compares block populations
WITHIN legs, not legs against each other.

**47. The fsync changes the wait, not the bytes -- and this corrects round 6.**
MB/block across the four counted legs: 139.9, 140.9, 141.4, 139.5. Durable and
safe-nosync are indistinguishable. In round 6 that number fell from 252.6 to
148.2 and I explained it as the kernel coalescing dirty pages when no fsync
forces them out. That explanation was wrong: 252.6 was the cold leg, and warm
against warm the byte volume is flat. The correction matters because the flat
version is the cleaner evidence for the reconciliation in round 6 -- the volume
is real, the time is the fsync's, and the volume is exactly what makes the
fsync slow.

**48. I left the ghost claim I had warned a peer about.** The runner's exit
trap removes /data/blockchain/.box-claim-gov5. A refresher I added mid-round --
to stop a 50-minute round reading as stale under the peer's 30-minute rule --
also mirrored the claim to wr-logs/, and nothing removed the mirror. I mirrored
the creation and not the deletion, an hour after describing that exact failure
to the peer as "worse than the staleness it was written to fix, and a silent
one". It lived about 45 seconds and was caught by the handover check, which is
the argument for having a handover check that looks rather than assumes.

## Round 8 — all four predictions failed, which was the useful outcome

Registered before the round: commit, wtotal and chgset together, bookend
criterion max(15% of the mean, 20 ms), warm-up leg discarded, then A-B-A-B.

  leg      sync           chgset   commit   wtotal   MB/blk
  warmup   durable         223.0    231.8    539.9    140.3   (discarded)
  A1       durable         225.4    248.4    581.4    148.5
  B1       safe-nosync     219.9     92.1    482.7    145.5
  A2       durable         246.6    227.3    567.1    148.7
  B2       safe-nosync     234.2     65.0    453.8    143.2

  bookends:   commit  A  8.9% HOLD   B 34.5% FAIL
              wtotal  A  2.5% HOLD   B  6.2% HOLD
              chgset  A  9.0% HOLD   B  6.3% HOLD

**49. wtotal is the most stable quantity on this rig, not the least, and I had
it exactly backwards.** P2 predicted wtotal's bookends would fail at >40% and
P3 named chgset as the source, both on the strength of round 7 where wtotal's A
legs sat 55% apart. wtotal came in at 2.5% and 6.2%, chgset at 9.0% and 6.3%.
The round-7 spread was one leg -- A1 at wtotal 760 against A2's 432 -- and I
generalised an outlier into a property of the metric, then built a whole round's
design around defending against it. The defensive co-registration was still the
right call: it is what let the round say something instead of failing shut.

**50. commit has failed a bookend three times; I stop measuring the fsync split
on this rig.** Rounds 6, 7 and 8. This round's failure is the B side at 34.5%
(27.1 ms on a 78 ms mean), while every other quantity in the same legs held.
That pattern -- commit noisy while chgset and wtotal are steady -- is what
H-spill predicts: cost relocating between phases leaves each phase varying and
the total still. In the B legs commit falls 27.1 ms while chgset rises 14.3 and
the total falls 28.9.

**51. P4 was gated on P1 and stays unevaluated.** wtotal's bookends both held
and would have carried an effect size (durable ~574 ms against safe-nosync ~468
ms), and it is not reported, because the gate was registered before the data
and honouring a gate only when the ungated path is unappealing is not
honouring it. The next round makes wtotal primary and registers its own effect
band.

**52. The node rotates its log mid-round.** n42.log rotated at 07:47:59, inside
the last leg, and the first pass of the analyser found n=0 for the four legs
before it and blocks only for B2. Nothing was lost -- it had moved to
n42-*.log.gz -- but an analyser that reads only n42.log will silently report a
round as empty, or worse, report only its tail. Read the rotated archives too.

Also corrected: my earlier claim that this harness is an order of magnitude
noisier than a peer's (their 1-3% same-binary spread against my "9-55%"). The
9-55% was commit's. On wtotal and chgset this rig runs 2.5-9%, which is a much
closer comparison. The noisy thing was the metric, not the harness.

## Correction to round 8: "wtotal is the stable one" was a claim about medians

A peer read one of their legs in 20-block buckets instead of as a single median
and found a within-leg slope the median had hidden. Applying the same read to
round 8's four counted legs, wtotal per 20-block bucket:

  A1 durable   568 647 575         +1.2%  (non-monotonic)
  B1 nosync    500 477 312        -37.6%
  A2 durable   585 556 544         -7.0%
  B2 nosync    444 542 393 508    +14.4%  (oscillating)

Round 8's finding 49 said wtotal is the most stable quantity on this rig at
2.5% and 6.2%. That is true of the LEG MEDIANS and false of the blocks under
them. B1 falls 37.6% across its own leg while B2 oscillates, and their medians
land 6.2% apart. Two legs of different internal shape agreeing on a median is
not two stable legs agreeing, and I reported the first as though it were the
second.

The arithmetic that settles it: the between-leg difference is 14.3 ms on the A
side and 28.9 ms on the B side, while the median within-leg bucket RANGE is
60.0 ms and 168.5 ms. The legs differ by far less than the blocks inside them
swing, so the agreement carries no information about the treatment. It is not
evidence that wtotal is stable; it is a coincidence of medians.

What survives from round 8 unchanged: commit failed its bookend a third time,
so it stays retired; P4 stayed unevaluated and no number was published, which
is now doubly right; and the log-rotation instrument note. What does not
survive is the recommendation that later rounds simply make wtotal primary and
read leg medians. Round 9 registers Q5 before it has data: buckets are reported
for every leg, and a bookend holds only if the between-leg difference is also
smaller than the median within-leg bucket range. Under Q5 round 8's wtotal
bookends would FAIL, which is the point of adding it -- a criterion added after
looking at old data is only safe if it tightens the bar.

### Correction to the correction: Q5 was backwards, and its check was rigged

The criterion I registered in the previous section -- "a bookend holds only if
the between-leg difference is also smaller than the median within-leg bucket
range" -- is wrong in direction, and the check that "confirmed" round 8 would
fail it contained a hardcoded `and False` that printed FAIL for any input.

Run properly, round 8 PASSES that stated criterion on both sides (A: 14.3 <
60.0; B: 28.9 < 168.5). And it would pass nearly anything: a bookend wants two
same-treatment legs to agree, so "difference smaller than the noise" is the
PASS condition, and a median over ~75 blocks is far more precise than the
block-level range. I published a rule that cannot fail and called it a
tightening, then told a peer it was strict while they were preparing to adopt
it.

What the buckets really showed stands, and it is about DRIFT rather than
spread. A leg that drifts has no level to compare; a leg that is merely noisy
has one, and has it more precisely than its range suggests. Round 8's
first-to-last bucket change: A1 +1.2%, A2 -7.0%, B2 +14.4%, B1 -37.6%
(500 477 312). B1 has no stable level and its median is an artefact of where
the leg stopped -- which is the real reason round 8's B-side agreement carries
no information, and it is not the reason I gave.

Replacement registered for round 9 before it has data. Q5: report each leg's
buckets and its first-to-last change; a leg moving more than 20% end to end is
treated as having no level and its median is not compared. Q6: the treatment
difference must exceed the bookend spread of the same quantity, or the round
has not separated the treatment from the rig.

## Round 9 — the question is unanswerable on this rig, and that is the answer

Registered before the round (Q1-Q6), wtotal primary, warm-up leg discarded,
A-B-A-B, drift criterion added from a peer's bucket method.

  leg      sync          chgset  commit  wtotal  MB/blk   wtotal buckets -> drift
  warmup   durable        232.3   248.4   630.9   137.9   664 660 551  -17.0% (discarded)
  A1       durable        268.6   162.1   592.4   140.3   551 631 563   +2.1%
  B1       safe-nosync    296.4    86.1   544.3   140.2   540 549 549   +1.6%
  A2       durable        286.5   257.3   705.6   147.5   671 727 738   +9.8%
  B2       safe-nosync    314.7    75.2   549.9   143.9   520 584 536   +3.1%

  Q5 drift   : all four counted legs have a level (1.6-9.8%). The warm-up leg
               drifted -17.0% and was discarded before any of this, which is
               what a discarded leg is for.
  bookends   : wtotal  A 17.4% FAIL   B  1.0% HOLD
               chgset  A  6.5% HOLD   B  6.0% HOLD
               commit  A 45.4% FAIL   B 13.6% HOLD
  Q1 FAILS, Q3 HOLDS, Q4 HOLDS (1.3%), Q6 FAILS, Q2 not evaluated.

**53. Q6 is what settles it: the effect is smaller than the noise between two
identical legs.** The durable-to-nosync difference on wtotal is 101.9 ms. The
spread between the two DURABLE legs, same binary, same flags, same workload, is
113.2 ms. A treatment that moves the number less than re-running the same
configuration moves it has not been separated from the rig, and no amount of
further legs on this harness changes that -- it is a statement about the
harness, not about the sample size.

So the pre-registered stop fires: after rounds 6, 7, 8 and 9 I stop measuring
the fsync split here, and I do not go looking for a fifth metric. Q6 is the
criterion that did the work, and it only existed because a peer's bucket read
had already embarrassed one of my conclusions; without it this round would have
reported a clean-looking 17% cut off the B legs' 1.0% bookend and buried the
fact that the A legs disagreed by more than the effect.

**54. What four rounds did establish, none of it the thing I was chasing.**
- Q4 held for the third time: MB/block is 143.9 against 142.0, 1.3% apart. The
  fsync changes the wait and not the bytes, measured on durable/nosync pairs in
  rounds 7, 8 and 9.
- chgset is the stable term (6.5% and 6.0% here, on drift-validated legs) and
  commit is the unstable one (45.4% on the A side). wtotal inherits commit's
  instability, which is why it failed here having looked stable in round 8.
- H-spill stands from round 7: 5.3x the time at 1.06x the bytes, cost
  relocating inside the transaction rather than blocks doing more work. That
  mechanism is also the reason commit cannot be pinned down -- the quantity
  moves between phases run to run.

The honest summary of the whole line: `commit` at 304 ms was a real observation
about one workload on one afternoon, its time is the fsync rather than the page
writes (direction, from round 6, never quantified), and this harness cannot
measure how much because its run-to-run variation on the write path exceeds the
effect. A rig with per-leg state resets -- a peer's design, 1-3% same-binary
spread against my 17-45% -- could answer it. Mine cannot.
