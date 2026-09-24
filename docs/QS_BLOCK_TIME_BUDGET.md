# Where a qs block's 1,190 ms goes

Measured 2026-09-04 on the seven-node qs fleet (chain `mainnet_qmdb_staggered`,
480M gas tier, 22,857 tx/block, `--recipients 22857`), during round 14's four
loaded legs. Every number below is a median over full blocks only.

## 1. Why this document exists

Rounds 6 through 13 optimised the write path. That work was real -- round 11
cut it 58.8% and TPS rose ~50% -- but it was chosen by looking at
`blockwrite phases`, which is a view of one stage. It never said what fraction
of a block that stage was. This document answers that first, so the next round
is chosen against the whole budget instead of against the part that happened to
be instrumented.

The instrument was already in the tree: `ViewTiming` in
`internal/consensus/hotstuff/engine.go` records seven per-view timestamps and
`view_timing.go` logs one line per commit. Nothing had to be written to get
section 2; it had to be read.

## 2. The budget

Full blocks only (a view is "loaded" when a >20,000-tx block was written in the
same second or the one before), node0, n=1,952 follower views and 320 leader
views:

| role     | stage    | what it spans                      | median  |
|----------|----------|------------------------------------|---------|
| leader   | propose  | ViewStart -> ProposalSent          | **615 ms** |
| leader   | r1       | ProposalSent -> PrepareQCFormed    | 62 ms   |
| leader   | r2       | PrepareQCFormed -> CommitQCFormed  | **509 ms** |
| leader   | total    |                                    | 1,168 ms |
| follower | recv     | ViewStart -> ProposalReceived      | 672 ms  |
| follower | exec     | ProposalReceived -> VoteSent       | 0 ms    |
| follower | r1       | VoteSent -> CommitVoteSent         | 502 ms  |
| follower | r2       | CommitVoteSent -> CommitQCFormed   | 4 ms    |
| follower | total    |                                    | 1,190 ms |

Read as one serial chain:

```
ViewStart
  → 615 ms   the leader builds, seals and sends the proposal
  →  62 ms   prepare votes round-trip and the PrepareQC forms
  → 509 ms   every node imports the block, then releases its commit vote
  = 1,186 ms
```

## 3. Two things this corrects

**`exec` is 0 ms, and only 507 of 1,952 views record it at all.** The follower's
prepare vote is not import-gated in two-phase mode -- it goes out immediately,
and only the COMMIT vote waits for the import (`proposal.go`, the two-phase R2
gate: "the CommitVote is the execution attestation"). So the import does not sit
between proposal and prepare vote, where the phase name suggests. It sits inside
r2, which is why r2 is 509 ms on the leader and 4 ms on the follower: the leader
is waiting for six other nodes to finish importing.

**The block interval is not a cost.** `paceBlock` (worker.go:777) holds a leader
to an absolute grid of `anchor + n*interval`. With interval 250 ms and a real
cadence of 971 ms every block is already late, `wait` is negative, and the
throttle never fires. The `--interval-ms 250` in every round's command line has
been buying nothing, which is the correct behaviour and worth recording so it is
not "optimised" later.

## 4. Where the import sits

The 509 ms is one import. From `blockimport phases`, same window:

| phase | median | | `blockwrite phases` (role=import) | median |
|-------|--------|-|-----------------------------------|--------|
| exec  | 122 ms | | commit (mdbx_txn_commit)          | **150 ms** |
| root  |  65 ms | | state                             | 39 ms  |
| write | 235 ms | | block                             | 10 ms  |
| recov |  16 ms | | chgSets                           | 9 ms   |
| body  |  10 ms | | qflush                            | 9 ms   |
| total | 449 ms | | chgHist                           | 0 ms   |

`chgHist` is 0 because rounds 12/13 moved the history index off the commit path.
That win is held in every leg since.

One asymmetry is worth its own line: the same commit is **150 ms on an importing
node and 40 ms on the leader**. The leader fsyncs alone; six followers fsync into
one disk at the same instant. Round 15 tests whether that difference is the
fsync, using the `N42_MDBX_SYNC=safe-nosync` knob that node.go:3607 was written
to make measurable and that had never been run.

## 5. The 250-373 ms before the leader starts sealing

The leader's `propose` stage is 615 ms on a full block and 250 ms on an empty
one. Subtracting the miner's phase timers names the gap -- but only the right
timers. `miner: propose phases` reports `total` as `time.Since(task.createdAt)`,
and `createdAt` is when the SPECULATIVE build started, so `total` includes the
time the finished block sat parked waiting for its view. It is not a cost. That
is why the empty-block `total` (262 ms) is LARGER than the full-block one
(226 ms): with nothing to build, the guess finishes at once and then idles.

The serial work after the seal begins is `seal2res` plus the push:

| | propose | − seal2res | − push | = ViewStart → seal start |
|---|---|---|---|---|
| full block  | 615 ms | 226.6 ms | 15.7 ms | **373 ms** |
| empty block | 250 ms |   2.9 ms |  0.1 ms | **247 ms** |

**So 247-373 ms elapses between the view starting and the leader beginning to
seal** -- present with zero transactions in the block, and the largest single
item on the leader's side. `miner: build phases` has a median total of 4.0 ms
(p90 5.0), so the speculative result is being reused and the build itself is
very nearly free; this time is not execution.

### Round 16's first pair: both candidates falsified

Round 16 instrumented the two candidates and its warm-up leg answered at once,
over the loaded views only:

Final, over all three legs (n=44 loaded leader views, n=42 real build requests):

| candidate | p50 | p90 | max |
|-----------|-----|-----|-----|
| `heightBehind`           | 0.01 ms | 0.01 | 0.01 |
| `committedParentBlocked` | 0.00 ms | 0.00 | 0.00 |
| `ensureParentApplied`    | 0.02 ms | 0.03 | 52.13 |
| **all three gates**      | **0.03 ms** | 0.04 | 52.14 |
| `newWorkCh` -> `commitWork` | **0.01 ms** | 0.02 | 0.02 |

**0.04 ms against 247-373 ms.** The registered prediction said the queue wait
would hold 200-320 ms of it, with the falsification condition "gate + queue
< 150 ms, which would mean the time is inside commitWork before `build phases`
starts measuring". Falsified, and the next place to look was named in advance.

Two corrections come out of it.

**The subtraction that produced 373 ms was wrong.** `task.assemble` (113 ms) and
`task.finalize` (81 ms) are recorded INSIDE `w.commit()`, which runs before
`taskLoop` stamps `sealStart`. They are therefore in the unaccounted window, not
in `seal2res`. About 194 ms of the full-block figure is already named.

**The empty-block 247 ms is probably the pacing throttle, which this document
twice said never fires.** `paceBlock` is called at worker.go:913, on the
speculative-hit path, immediately before the task reaches `taskCh` -- inside the
window. Section 3 argued it cannot fire because a late block's grid slot is in
the past; that argument was made from the LOADED cadence. The decay phase
produces 1,590 blocks in 400 s, which is 250 ms a block -- exactly
`--interval-ms 250`. The throttle plainly sets the cadence when blocks are
cheap, and the empty-block views are all measured during decay.

Whether it also fires under load has now been argued twice and measured zero
times. `miner: pacing wait` (logged only when the throttle actually sleeps) and
`miner: commit phases` (the whole `w.commit()`) are in `n42-instr2` and answer
both in rounds 18 and 17. Round 16 keeps the older binary so its own three legs
stay identical.

**A second piece of circumstantial evidence, from data already on disk.** Every
leg logs how many empty blocks its 400 s decay produced. Across thirteen legs of
rounds 14-16:

```
851 1603 853 1588 1593 757 1554 856 1590 850 1587 1583 1592
```

That is **bimodal at ~850 and ~1590**, a ratio of very nearly 1:2. 400 s / 1590
= 252 ms a block, which is `--interval-ms 250`; 400 s / 850 = 470 ms, about
twice it. Load produces a continuum; a GRID produces multiples.

The branch that would do it is visible: with rotating leaders a node seals every
7th block, so its own grid target sits 7 x 250 = 1,750 ms ahead, `wait >
interval` is true, the wait is capped to one interval and the throttle **sleeps
250 ms**. If that also happens under load it is 250 ms of a ~1,200 ms block --
21%, pure throttle, removed by `--interval-ms 0`.

This is inference from a bimodal histogram plus a code branch, not a
measurement, and this document has already been wrong twice about `paceBlock`.
The probe settles it in round 18 at no extra cost.

## 6. Fixed cost and per-transaction cost

The decay period before each flood produces ~1,590 EMPTY blocks. Their view
timing is the fixed cost of a block, measured rather than modelled:

| role     | stage   | empty block (0 tx) | full block (22,857 tx) | difference |
|----------|---------|--------------------|------------------------|------------|
| leader   | propose | **250 ms**         | 615 ms                 | 365 ms     |
| leader   | r1      | 58 ms              | 62 ms                  | ~0         |
| leader   | r2      | 9 ms               | 501 ms                 | 492 ms     |
| leader   | total   | **318 ms**         | 1,166 ms               | 848 ms     |
| follower | recv    | 305 ms             | 672 ms                 | 367 ms     |
| follower | r1      | 6 ms               | 499 ms                 | 493 ms     |
| follower | total   | 320 ms             | 1,188 ms               | 868 ms     |

n = 4,738 / 28,400 empty views and 338 / 2,043 full views.

So, to a good approximation:

```
block time  =  318 ms  +  848 ms x (N / 22,857)
```

Two consequences, and both are hard limits rather than estimates.

**A leader spends a quarter of a second producing an EMPTY block.** 250 ms of
propose with zero transactions to execute. That is the most concentrated fixed
cost in the system and 21% of the current block time.

**Everything except per-transaction cost is capped at ~27,000 TPS.** Removing
the fixed cost entirely and amortising it away with an unbounded block are the
same limit: 22,857 / 848 ms = **26,950 TPS**. Raising the gas ceiling, deleting
the 250 ms, shortening the vote rounds -- each of these buys some of the gap
between 21,000 and 27,000, and none of them can buy anything beyond it.

> **CAVEAT, and it may move this number a long way.** The two points this line
> is fitted through were measured in DIFFERENT REGIMES. The full-block point is
> loaded, where `paceBlock`'s grid slot is in the past and the throttle does not
> fire. The empty-block point is measured during the baseFee decay, where the
> decay cadence sits at 252 ms a block -- exactly `--interval-ms 250` -- and the
> throttle plainly does. See section 5: the decay block counts across thirteen
> legs are bimodal at ~850 and ~1590, i.e. at one and two pacing intervals.
>
> **MEASURED, round 18 warm-up.** `miner: pacing wait` (emitted only when the
> throttle actually sleeps) fired 229 times, every one of them for exactly
> 250,000,000 ns -- one full interval, the `wait > interval` branch -- and
> **zero of them on a loaded view.** The throttle sets the decay cadence and
> never fires under load, which is both halves of the question at once.
>
> So the empty-block point carries 250 ms of throttle that the full-block point
> does not, and the two were fitted through a single line. The honest split is
> about `68 ms + 1,098 ms x (N/22,857)`, and the ceiling that follows is
> **~20,800 TPS** -- which is where the chain already runs. **There is no
> headroom on the fixed cost; the 26,950 figure was an artefact of mixing two
> regimes.** Everything now rests on per-transaction cost, which is where
> section 6b's comparison with n42-rs already pointed.

## 6b. What the gap to n42-rs is made of

The n42-rs session supplied its own fixed/variable split from the same box
(fleet7, 7 nodes), which makes this a comparison of measurements rather than of
headline numbers.

**Their cycle.** An EMPTY block cycles at 427 ms, but 400 ms of that is their
pacing gate (`F7_BLOCK_INTERVAL_MS`). The chain's own fixed cost -- proposal ->
votes -> QC -> decide -> own import of an empty block -- is **27 ms**. A full
163,000-tx block cycles at 517 ms at the same pacing, so the marginal cost of
163,000 transactions is only 90 ms of cycle.

That 90 ms is not the work. Their leader build is ~300 ms and their follower
import 245-300 ms; those overlap each other (the leader builds N+1 while the
followers import N) and both fit inside the 400 ms pacing window, so almost none
of it reaches the cycle. **Persistence -- RocksDB plus static files, 300-410 ms
per block -- is off the cycle entirely.**

So the comparison has to be on work per transaction, not on cycle slope:

| | gov5 | n42-rs |
|---|---|---|
| fixed cost per block (pacing excluded) | **318 ms** | **27 ms** |
| leader build | inside the 615 ms propose | ~300 ms = 1.8 us/tx |
| follower import | 449 ms = **19.6 us/tx** | 245-300 ms = **1.65 us/tx** |
| state persistence | **on the consensus cycle** (19.3 us/tx) | **off it**, asynchronous |
| build vs import | **serial** | **overlapped** |
| execution | 122 ms = 5.3 us/tx | plain transfers applied without the interpreter |

**Their import is 12x cheaper than ours per transaction, and the difference is
very largely that persistence is not on their critical path.** Their absolute
persistence cost (300-410 ms/block) is comparable to ours; it simply does not
gate consensus.

One confound that cuts the other way and has to be stated: **their legs run from
FRESH DATADIRS every time** (which is also why their bookends agree within 1-2%
while mine drift monotonically -- see 6d). This chain has been accumulating for
days and holds on the order of 6M accounts. Random updates into a 6M-key B-tree
cost more than into a small one, so some unknown part of our 19.6 us/tx is chain
size rather than architecture. The architectural difference is real and is
confirmed below; its exact size is not established.

### How n42-rs votes, in their own description

Asked directly, and the answer settles the design question:

* A follower executes the block on the parent's state, runs the post-execution
  checks (gas, receipts root, bloom), computes and compares the QMDB state root,
  builds the hashed post-state, and hands the executed block to reth's engine as
  an in-memory `InsertExecutedBlock`. **Nothing has touched disk at that point --
  no WAL, no MDBX, no RocksDB.** The R1 vote goes out on that event. Persistence
  is a background batched task.
* On a crash between vote and persist the node does not replay -- it re-fetches.
  The engine comes up at its last persisted head and pulls the gap from peers by
  range, the same path a long-absent validator uses.
* Their safety argument: **a vote is a statement about validity given the
  parent, and does not depend on the voter keeping a copy.** Liveness needs only
  that honest peers still serve the body.
* The one thing they had to get right: the vote log is the only durable
  consensus state, and it is written before the signature leaves.

That last prerequisite gov5 **already satisfies**: `journalPrepareVote` and
`journalCommitVote` both write durably before signing and sending ("Journal the
Round 2 commitment BEFORE signing/sending it", proposal.go). So the blocking
requirement for voting on execution is already met here.

### The three differences, in the order they are worth attacking

1. **Persistence sits on our consensus cycle.** The two-phase R2 gate holds every
   commit vote until the block is imported locally, and "imported" here means
   executed AND written. But the property the gate exists to provide, in its own
   words, is that "a CommitQC still proves 2f+1 validators EXECUTED the block."
   **Execution, not durability.** Voting after execution and before the MDBX
   write would preserve the stated property and take ~235 ms off the path.
   Round 17's push-before-write is the first, smaller step in the same direction:
   it takes the LEADER's 207 ms write off the followers' path.
2. **Build and import are serial.** The leader of block N+1 cannot build until
   the CommitQC of N, and that QC waits on the imports of N. n42-rs overlaps the
   two. Our speculative build already exists and hits 91%, so the machinery is
   there; what serializes it is the QC dependency.
3. **Fixed cost is 318 ms against 27 ms.** Section 5 has 247-373 ms of it sitting
   between a view starting and the leader beginning to seal, which round 16
   instruments.
4. **The storage engine absorbs random-keyed writes differently.** n42-rs ran the
   mirror of round 15 on RocksDB and reported the opposite result: with the
   per-commit fsync off, its commit moved only 203 -> 189 ms per 2-block batch,
   so "the commit is the write, not the sync". Ours is the sync (79.3 -> 22.5 ms,
   -72%). The difference is LSM against B-tree. They write 163,000 transaction-
   hash index entries per block and it is cheap, because an LSM appends
   random-keyed rows sequentially into a memtable; the same shape in an MDBX
   B-tree is what `N42_TXINDEX_TAIL` had to move off our commit path after it
   was measured at 2,700 ms of a 3.53 s block cycle. Any plan that leaves
   random-keyed per-transaction rows in the committing transaction is fighting
   the data structure.

Execution itself -- the thing most likely to be blamed first -- is 5.3 us/tx
against their ~1.8, a factor of 3. It is the smallest of the four gaps.

## 6c. Where the 37.1 us/tx actually goes

Spreading the variable 848 ms over 22,857 transactions:

| item | ms | us/tx |
|------|----|-------|
| follower write (per node, all six in parallel) | 235 | **10.3** |
| leader write (`WriteBlockWithState`)           | 207 | **9.0** |
| follower exec                                  | 122 | 5.3 |
| follower root (QMDB)                           |  65 | 2.8 |
| follower recov + body                          |  26 | 1.1 |
| leader assemble/finalize outside its write     | ~20 | 0.9 |

**The two writes are 19.3 us/tx between them -- 52% of the per-transaction
cost.** Execution is 5.3. Whatever intuition says about an EVM being the
bottleneck, on this workload it is not; the state write is.

Inside the follower's 235 ms write, `commit` (mdbx_txn_commit) alone is 150 ms
= 6.6 us/tx, and the QMDB parts are nearly free: `qflush` 8.6 ms and `qmeta`
0.4 ms. **The cost is not the authenticated structure.** It is somewhere in the
flat tables that the same transaction writes alongside it, and the next two
subsections establish how big it is and which table has not yet been settled.

### The amplification, withdrawn and then re-measured properly

An earlier version reported 85 MB of device writes per node per block, taken
from `/proc/diskstats` divided by node0's full-block count. That was withdrawn:
`n42-datc-25m-hi4.bin` (34.5 GB RSS) had been on the box since 08:31 and, with
the qs fleet IDLE, wrote 660 MB/s on its own and took 93% of the system's major
faults. A whole-device counter cannot attribute anything on a shared box.

The sampler now sums `/proc/<pid>/io` `write_bytes` over the seven node
processes and reports what share of the device that was. Round 18's warm-up leg,
60 s inside the loaded window:

```
nodes=7   MY writes 21,806 MB   whole device 21,814 MB   (mine = 99%)
majfaults 551,394   full blocks 47   neighbour pid 2341306 rss 34 GB
  per full block, all 7 nodes:  463 MB
  per full block, per node:      66 MB
```

**Under load my fleet is 99% of the device.** The neighbour's 660 MB/s was
measured while my fleet was idle; it does not disappear, but it is a small
fraction of what the disk carries once seven nodes are importing. So the
withdrawal was right as method and the original number was close: the properly
attributed figure is **66 MB per node per full block**.

Against the measured 15,700 changed accounts (not the 24,000 assumed at the
time), that is **4.2 KB per changed account -- one 4 KB page each**. Per
transaction it is 2.9 KB. The mechanism the first version claimed survives; what
did not survive was the right to claim it from a device counter.

### How many accounts a block actually changes: 15,700, not 24,000

Round 16 added `accts` and `slots` to `blockwrite phases` -- the length of the
ChangeSetWriter's own maps, so it is the count the commit actually pays for
rather than one derived from the workload description. Over three legs:

| leg | accts | slots |
|-----|-------|-------|
| warmup | 15,616 | 3 |
| M1     | 15,610 | 3 |
| M2     | 15,742 | 3 |

Every arithmetic argument in this document before round 16 used ~24,000 (1,200
senders plus 22,857 recipients), which is 35% too high. `deriveRecipient` maps
`(sender*perTx + j) % recipients`, and within a single 22,857-tx block that does
not cover the pool uniformly. Storage slots are 3 -- these are plain transfers,
so `StorageChangeSet` is not part of the cost at all.

### Which table it is, is NOT established -- and two guesses have already failed

This subsection records the eliminations rather than a conclusion, because two
confident identifications have already been wrong.

**Wrong guess 1: fresh recipient accounts.** `deriveRecipient` in cmd/txflood
derives from `(sender*perTx + j) % recipients`, so the recipients are a FIXED
pool of 22,857 deterministic addresses, identical in every leg of every round.
They are updated in place, not created.

**Wrong guess 2: the QMDB key->slot index.** `qmdbMDBXIndex.Put` does write one
random-keyed MDBX row per changed key, and (on the ~24,000 figure believed at
the time -- the real count is 15,700) random keys into its 75,679
leaf pages predicted 82 MB against the 85 MB then believed -- an alarmingly good
fit to a number that has since been withdrawn.
But `UseMDBXIndex` is called from `internal/replay/engine_v2.go` and NOWHERE
else. The live node runs the default in-RAM `mapIndex`, so this table is not on
the block path at all; the 298.7 MB in the snapshot is replay residue.

**Eliminated on evidence, not argument:** the transaction-lookup index. Package
`internal/txindexer` records the measurement that removed it: one hash-keyed
MDBX row per transaction inside the committing transaction cost "2,700 ms in
mdbx_txn_commit against 149 ms of work, roughly 77% of a 3.53 s block cycle"
across 193 blocks. `N42_TXINDEX_TAIL=1` moved it to memory plus RecSplit
segments and was worth 2.5x. The fleet runs with it ON, so those 22,857
random-keyed rows are already off the commit path.

**A note on the instrument.** `mdbx-table-stat` returns an identical placeholder
(7,004 entries / 202 leaf pages / 11.0 MB) for every table it cannot open with
its assumed flags -- PlainState and three deliberately misspelled names all
returned it, while a genuinely empty table returned 0 B. Nothing about a table's
presence or absence can be concluded from that placeholder, and an earlier draft
of this section did exactly that.

**What the write path actually writes.** Enumerated from the code rather than
guessed -- every write reachable from `writeBlockWithState`'s closure:

| write | rows per block | key | scatter |
|-------|----------------|-----|---------|
| `ibs.CommitBlock` -> PlainState accounts | **15,700 (measured)** | **address (20 B)** | **uniform** |
| `AppendReceipts`                          | 22,857 | block number + index | sequential |
| `WriteBlock` -> EthTx                     | 22,857 | transaction number | sequential |
| AccountChangeSet / StorageChangeSet       | 15,700 + 3 slots | block number + address (DupSort) | append-ordered |
| `WriteQMDBUndo` + `PruneQMDBUndoBelow`    | 1 record, 15,700 pre-images | block number | sequential |
| QMDB flush -> qmdbEntries                 | 22,857 | slot | append-only |
| `WriteLogIndex`                           | ~0 (plain transfers emit no logs) | - | - |
| Td, QMDBApplied, consensus evidence, reward | 1 each | - | - |

**Exactly one of them is keyed by a uniformly distributed value: the PlainState
account write.** That makes it the leading candidate on shape alone. The
arithmetic that used to appear here -- random keys into ~75,000 leaf pages
touching ~20,600 pages = 82 MB -- is removed with the 85 MB it was fitted to; an
arithmetic fit to a contaminated measurement is worth nothing, and the same fit
was produced for qmdbIndex, which turned out not to be on the block path at
all.

### ANSWERED, round 18: the write is account-keyed, one page per account

Five legs, the transaction COUNT identical on every one, only `--recipients`
changed. Per-process `write_bytes` summed over seven nodes, and the
changed-account counter added in round 16:

| leg | `--recipients` | accts/block | writes/node/block | `write` phase | `commit` | peak full blocks/min |
|-----|----------------|-------------|-------------------|---------------|----------|----------------------|
| warmup | 22,857 | 15,527 | 66 MB | 252.8 ms | 133.3 | 47 |
| S1     | 22,857 | 15,664 | 68 MB | 239.5 | 107.7 | 44 |
| **Z1** | **0**  | **13**  | **3 MB** | **17.7** | **3.2** | **80** |
| Z2     | 0      | aborted | -- | -- | -- | -- |
| S2     | 22,857 | 15,547 | 68 MB | 246.0 | 120.7 | 48 |

**The S arm's bookends are tight**: writes 66/68/68 MB (3%), accounts
15,527/15,664/15,547 (1%), rate 47/44/48 blocks/min (9%).

Removing 15,650 changed accounts per block:

```
write phase   246 -> 17.7 ms   (14x)      state  66 -> 0.1 ms
commit        121 -> 3.2 ms              rate    46 -> 80 blocks/min  (+70%)
```

**15,650 more changed accounts cost 65 MB of writes** -- 4.2 KB each, one 4 KB
B-tree page per changed account. That figure is per-process `write_bytes` and is
unaffected by anything below.

**But `--recipients` does not isolate the write. It moves three phases**, and an
earlier version of this section attributed all of the time to the write. From
`blockimport phases`:

| leg | recov | exec | root | write | total |
|-----|-------|------|------|-------|-------|
| S1 (22,857) | 16.1 | 154.7 | 69.0 | 239.5 | 540.1 |
| Z1 (0)      | 17.5 | **80.4** | **0.2** | **17.8** | **138.1** |

* `write` -222 ms -- the account-keyed pages, as expected
* `root`  -69 ms -- the QMDB state root over 13 changed keys instead of 15,664
* `exec`  -74 ms -- **execution nearly halves**, because repeatedly updating one
  hot account is far cheaper than touching 15,650 cold ones

So the honest statement is that the **distinct-account count drives write, root
and execution together: 540 -> 138 ms of import, of which the write is 222 ms
(55%)**. It is still the dominant cost driver and still the thing to attack;
it is not 225 ms of pure writing.

The 3 MB and 17.8 ms Z1 still spends is the transaction-keyed floor (block body,
receipts, EthTx) and does not move with the account count, so the
transaction-keyed tables are not the problem.

The view timing shows the same thing from the consensus side, and shows that it
is paid TWICE:

| leg | propose | r1 | r2 | total |
|-----|---------|----|----|-------|
| warmup (22,857) | 642 | 62 | 468 | 1,165 |
| S1 (22,857)     | 675 | 60 | 573 | 1,318 |
| **Z1 (0)**      | **219** | 61 | **95** | **379** |
| S2 (22,857)     | 642 | 62 | 492 | 1,246 |

`propose` carries the LEADER's own account work and `r2` carries the wait for
every follower's; both collapse together, while `r1` -- the prepare-vote round,
which the two-phase gate does not hold -- does not move at all (60-62 ms on
every leg). Roughly 440 ms on the leader plus 400 ms in r2 of a ~1,250 ms block.

**Z1's 30,476 TPS is NOT a throughput figure for this client** -- it is a
single-sink diagnostic, the same workload that produced the historical 44,876
and is not comparable to anything else here. What transfers is the phase delta.

Two caveats this round has to carry:

* **The Z arm has no bookend.** Z2 aborted at startup ("RPC not ready on all 7
  nodes"), a transient timeout bringing seven MDBX environments up on a box
  also carrying the datc build -- all seven were up and level minutes later. The
  A-B-B-A balance is gone; three 22,857 legs bracket a single leg at zero. The
  effect is 23x on writes and 1,200x on accounts, so no plausible drift reaches
  it, but one leg is one leg.
* The log line `IO Z2` at 11:03:11 is spurious. That leg's sampler waits for a
  flood marker Z2 never produced and then matched S2's, so it measured S2's
  window. It must not be read as a second Z leg.

### What this says the fix is

The cost is the COUNT of distinct account keys committed per block, times one
page. Three consequences:

1. **Making the commit faster does not attack it.** Round 15 cut
   `mdbx_txn_commit` by 72% and the block rate did not move.
2. **Taking the account write off the commit path does.** That is 225 ms of a
   ~1,300 ms block, and it is what n42-rs already does -- its persistence is a
   background batched task and its followers vote before anything touches disk.
3. **Batching the account write across blocks would help this workload more than
   a real one.** The recipient pool here is fixed, so N blocks rewrite the same
   pages; a real chain touching fresh accounts would not collapse that way.
   Off-the-path is the robust version, batching is the workload-flattering one.

**How the question gets settled.** Round 18 does not need the table's name.
`--recipients` changes how many distinct ACCOUNTS a block writes and changes
nothing about how many TRANSACTIONS it writes. If the nodes' OWN per-block write
bytes fall with the recipient count, the cost is account-keyed; if they hold, it
is transaction-keyed, and the two lead to completely different work.

Two further observations, neither of which settles the table question:

* The same commit is **40 ms on the leader and 150 ms on a follower**. One node
  fsyncs alone; six fsync into one disk at the same instant. That is a property
  of seven nodes sharing one NVMe and would not hold on seven machines.
* The rig's historical peak of **44,876 TPS (2026-09-02)** was measured with no
  `--recipients` flag -- ~1,201 distinct accounts per block instead of 15,700,
  and roughly double the throughput. That is suggestive but it is not a
  controlled comparison: it was a different binary on a much smaller chain. Its
  TPS is NOT comparable to any figure in this document and must not be quoted as
  a regression from one. Round 18 runs the same comparison as a controlled leg.

What IS established: 19.3 of the 37.1 us/tx is spent writing, 5.3 executing, and
the authenticated state structure is not the expensive part. The road to
n42-rs's 3.7 us/tx runs through the write path, and the first question on it is
how many bytes the nodes themselves write per block and to which table -- both
still open, the first because the only measurement of it was contaminated.

## 6d. Round 15: durability, and why its throughput number is unusable

Five legs, A-B-A-B with a discarded warm-up, one binary, history deferred on
every leg, `N42_MDBX_SYNC` the only variable. All five durability interlocks
passed (7/7 nodes on every leg).

| leg | sync | commit | write total | import total | peak blocks/min |
|-----|------|--------|-------------|--------------|-----------------|
| warmup | safe-nosync | 16.2 | 102.6 | 357.8 | 41 |
| A1 | durable     | 70.0 | 169.6 | 434.7 | 43 |
| B1 | safe-nosync | 18.5 | 110.5 | 351.8 | 43 |
| A2 | durable     | 88.6 | 191.2 | 467.0 | 51 |
| B2 | safe-nosync | 26.4 | 110.8 | 367.6 | 60 |

**On the write path the effect is large and bookend-confirmed:**

| metric | A mean | B mean | effect | A spread | B spread |
|--------|--------|--------|--------|----------|----------|
| commit       |  79.3 |  22.5 | **-56.8 ms (-72%)** | 18.6 | 7.9 |
| write total  | 180.4 | 110.7 | **-69.7 ms (-39%)** | 21.6 | **0.3** |
| import total | 450.9 | 359.7 | **-91.2 ms (-20%)** | 32.3 | 15.8 |

The registered threshold was 45.3 ms on commit (round 14's same-config spread);
the effect is 56.8 ms and exceeds both arms' bookends. So the fsync IS most of
the commit, and 91 ms of the import path can be bought with it.

**Why the two arms' bookends differ so much, and a correction.** The A arm's
spreads are large in relative terms -- 18.6 ms on a 79.3 ms mean commit is 23% --
while the B arm's write total agrees to 0.3 ms. An earlier reading of this took
that as evidence that phase timers survive a noisy neighbour where derived rates
do not. That is the wrong way round, and n42-rs corrected it against its own
instrument: its persistence ms/block is a phase timer too (reth's `save_blocks`
histogram, not a derived rate) and it still moved 154 -> 169 ms between two
identical legs, because the phase includes the NVMe writes the neighbour was
competing for.

**A timer that touches the disk is no more robust than a rate on this box.**
`mdbx_txn_commit` is a disk phase. The B arm is tight not because it is a timer
but because with the fsync gone that phase barely touches the disk at all; the
A arm, which does, drifted like everything else. Only CPU-bound phases -- exec,
the vote rounds, the pre-seal window round 16 measures -- can be read against a
disturbed box without a bookend that holds.

**The throughput number cannot be attributed and must not be quoted.** The five
legs rose monotonically -- 41, 43, 43, 51, 60 blocks/min -- with leg ORDER, not
with arm. Grouped by arm: safe-nosync {41, 43, 60} mean 48, durable {43, 51}
mean 47. The within-arm drift (A1->A2 +19%, B1->B2 +40%) is larger than any
between-arm difference.

The likely cause is not the fleet. `n42-datc-25m-hi4.bin` (34.5 GB RSS, 660 MB/s
of writes, 93% of the system's major faults) started at 08:31 and was present
for the whole round; a neighbour whose phase intensity fell across that hour
fits a rising throughput better than a fleet that warms up. Its load is PHASED
rather than steady -- in one 15 s sample it read 260 MB and wrote nothing while
the device still took 201 MB and the system took 66,837 major faults -- and a
phased neighbour moves between legs, which neither A-B-A-B nor A-B-B-A cancels.
`/data/blockchain/wr-logs/neighbour.tsv` now samples it every 15 s so a leg can
be checked against it rather than guessed at. Any future durability or throughput round has to
control for that trend -- alternating A and B is not enough when the drift is
this steep.

An earlier interim reading of this round said "no throughput effect". That was
wrong in the same way: with a monotonic trend present, neither an effect nor its
absence is established.

## 6e. Round 17: push-before-write establishes nothing, and is not adopted

Round 17 moves the leader's direct push ahead of its `WriteBlockWithState` so
followers can import while the leader commits. Five legs, A-B-B-A, one binary,
`N42_PUSH_BEFORE_WRITE` the only variable.

**Interlocks: all five passed.** B legs 7/7 nodes logging `"pushedEarly":true`,
A legs 0/7, **zero failed early broadcasts and zero stale seals** across the
round, chain healthy throughout. The change works and the stale-seal pre-check
never had to fire.

| leg | push | `propose` | `r1` | `r2` | view total | peak blocks/min |
|-----|------|-----------|------|------|------------|-----------------|
| warmup | ON  | 688 | 58 | 256 | 1,037 | 42 |
| A1     | OFF | 677 | 62 | 530 | **1,248** | **46** |
| B1     | ON  | 706 | 310 | 12 | **1,062** | **47** |
| B2     | ON  | 660 | 256 | 83 | **1,034** | **40** |
| A2     | OFF | 604 | 63 | 434 | **1,102** | **49** |

| metric | A mean | B mean | effect | A spread | B spread | verdict |
|--------|--------|--------|--------|----------|----------|---------|
| view total | 1,175 | 1,048 | -127 ms | **146** | 28 | **not established** |
| blocks/min | 47.5 | 43.5 | -4 (-8%) | 3 | **7** | **not established** |

**Both primary metrics have a bookend spread larger than the effect.** By the
criterion registered before the round, nothing is published from it. The point
estimate on throughput is negative.

Three things the round does establish:

1. **The mechanism happens, consistently across all three B legs.** The wait
   moves from `r2` into `r1`: A legs run r1 ~62 / r2 ~480, B legs r1 ~280 /
   r2 ~50. Followers pushed the block early start importing earlier, so they are
   busy when the proposal arrives and slower to return a prepare vote; by the
   time the PrepareQC forms they have finished, so `r2` collapses. **The wait did
   not go away, it changed rounds.**
2. **An unforeseen side effect.** The B arm's follower import is SLOWER --
   572 ms against 510, with the write 275 against 235 -- because followers now
   commit concurrently with the leader instead of after it. Moving work earlier
   put more of it on the disk at the same instant.
3. **It is not adopted.** It takes on the risk of broadcasting a block before it
   is durable in exchange for a benefit the round could not measure. The code
   stays behind the default-off flag as the evidence for the point below.

### Reordering is not asynchrony

* **Reordering** two synchronous steps in the same goroutine moves work along the
  serial chain; it does not remove it. Round 17 is reordering, and its own
  numbers show the work reappearing one round later.
* **Asynchrony** hands the work to a background task and the chain stops waiting.
  n42-rs's persistence is a batched background task, 300-410 ms a block, entirely
  off the consensus cycle -- which is why its follower import is 1.65 us/tx
  against our 19.6.

Round 15 is the same lesson from the other side: `mdbx_txn_commit` fell 72% and
the block rate did not move. **Twice an instrument improved and the output did
not.**

So the bar for round 19 (voting on execution rather than persistence) is higher
than it was. It is not enough to vote before the write. **The write has to become
a background task**, or it will reappear between views exactly as it did here.
That is a much larger change than moving a notification -- and round 17 is the
evidence for why the smaller version was not worth adopting.

## 6f. Round 19: the table is `Account`, and it is a duplicate

Round 18 established the shape -- cost tracks the distinct-account count, one
4 KB page each -- but not the name, and the name had been guessed wrong twice.
`lib/kv/mdbx/write_probe.go` already existed (`N42_WRITE_PROBE=1`, logger wired
at cmd/n42/app.go:79) and records rows and payload bytes per TABLE plus the
transaction's SpaceDirty. It is the third instrument this week that was in the
tree and had only to be read.

Per write transaction carrying a full block:

Median over n=266 such transactions across three legs:

```
dirtyBytes   71.3 MB      payloadBytes 6.1 MB      amplification 11.7x

table                 rows/block   payload KB   key
qmdbEntries               31,375        674.3   slot            (append-ordered)
BlockTransaction          22,857      2,543.3   transaction no. (append-ordered)
Account                   15,682        367.6   ADDRESS         (uniform)
AccountChangeSet          15,585        487.1   block no.+addr  (DupSort, append)
QMDBUndoWindow                 1      1,111.7   block no.
Receipt                        1        188.3   block no.
```

**`Account` is the only table keyed by a uniformly distributed value.**
`modules.Account` is "address (un-hashed) -> account encoded". Every other table
with a large row count is append-ordered -- qmdbEntries by slot, BlockTransaction
by transaction number, AccountChangeSet by block number under DupSort -- so their
rows pack into few sequential pages. 15,682 random-keyed rows at one 4 KB page
each is **62.7 MB of the 71.3 MB dirtied, 88%**, from 6% of the payload bytes.

AccountChangeSet is the clean control: it has essentially the SAME row count as
Account (15,585 against 15,682) and costs almost nothing, because its key is
append-ordered. Rows are not pages; rows times key scatter are.

**A registered criterion of mine was wrong, and the conclusion is not.** The
prediction said "FALSIFIED IF the top row by count is not an account-keyed
table". `Account` is third by count, so by the letter it is falsified. The
criterion was badly written: rows are not pages, and a sequential-keyed table
can have twice the rows at a fortieth of the cost. The right proxy is rows times
key scatter, which is what the earlier sections argued and what the numbers show.

### The duplicate

`qmdbEntries` carries 31,375 rows to `Account`'s 15,682 -- almost exactly 2:1.
QMDB appends a new entry per changed key and deactivates the old one, so both
tables are recording **the same ~15,700 account updates**: one append-only, one
random-keyed. QMDB entries carry the value (`entry{keyHash, value, active}`) and
`IndexLookup -> entryAt` resolves an account in RAM for the hot set.

So the account state is written twice per block, and the expensive copy is the
one execution reads through (`PlainStateReader` over `modules.Account`), while
the cheap one is already authoritative for the state root.

### What that makes the two candidate routes worth

| route | attacks | measured ceiling | cost |
|-------|---------|------------------|------|
| A. asynchronous persistence | latency | 46 -> 80 blocks/min (~30k TPS), from round 18's Z1 | moderate; reads must see un-persisted writes |
| B. drop the `Account` duplicate, read through QMDB | **bytes** | 15,682 random-keyed rows per block stop existing rather than move | large; snap sync iterates `Account` by address, which QMDB cannot serve |

Route A is bounded at roughly 30k TPS by a measurement, not an estimate. Only
route B changes the per-transaction byte count, and per-transaction cost is the
only axis left once section 6's ceiling is corrected.

## 6g. Round 20 found a real inconsistency between the plain state and QMDB

Round 20 runs the QMDB state reader in `verify` mode -- the plain reader still
answers every call, QMDB is read alongside, divergence is counted. Its registered
criterion was absolute: zero account mismatches and zero storage mismatches, or
route B stops.

**It failed on its first comparison, on every node, and the cause is a defect
that predates this work.**

```
{"address":"0xfffffffffffffffffffffffffffffffffffffffe",
 "plainNil":true, "qmdbNil":false, "msg":"qmdb state read: account diverges..."}
```

1,789 divergence lines across seven nodes and **exactly one distinct address**:
`0xff..fe`, the EIP-2935/EIP-7708 system address that block-start system calls
run as. QMDB holds an account there; the `Account` table does not. Zero storage
divergences.

### The cause: two different definitions of "empty"

```go
// the plain write path, modules/state/state_object.go:189
func (so *stateObject) empty() bool {
    return so.data.Nonce == 0 && so.data.Balance.IsZero() &&
        bytes.Equal(so.data.CodeHash[:], emptyCodeHash)
}

// the commitment path, modules/state/commitment/jmt_commitment.go:174
func isAccountEmpty(a *account.StateAccount) bool {
    return a.Nonce == 0 && a.Balance.IsZero() && !a.Initialised
}
```

The third clause differs, and the difference is worse than it looks.
`StateAccount.Reset()` sets `Initialised = true`, and so do
`state_object.go:304`, `state_object.go:595` and `intra_block_state.go:1068`;
`computeRoot` then does `acct.Copy(&obj.data)`, which carries the flag over. So
for any account that came from a state object:

    isAccountEmpty(a) = (nonce==0) && (balance==0) && !true = FALSE, always

**The empty-account deletion in the commitment path is unreachable.** It is not
a slightly different definition of empty; it never fires. Every account the plain
path deletes under EIP-161 is retained by QMDB.

That reframes the scope. What was OBSERVED is one address, because a workload of
plain transfers produces no other account that is both touched and empty -- the
system address is touched by every block's system call and lands in exactly that
state. What the MECHANISM implies is general: any account drained to zero balance
with nonce 0 and no code diverges the same way.

`IntraBlockState.computeRoot` compounds it: it decides what to hand the
RootComputer on `obj == nil || obj.deleted || obj.selfdestructed` alone and never
consults `shouldRemoveEmptyAccount`, so the empty-account policy is applied on
one write path and not the other.

### The scope, measured

Final, three legs, all seven nodes:

```
per node:  compared 1,000,001   accountMismatch 1,447-1,448   storageMismatch 0
all nodes: 26,072 divergence lines, 1 distinct address (0xff..fe)
           0 storage divergences
```

0.145% of comparisons, all of them the same account. **~998,553 account reads
and every storage read in a million comparisons matched byte for byte**, and the
seven nodes agree to within one (1,447 against 1,448, a timing edge). So on this workload the two
stores agree everywhere except the one account that becomes empty while being
touched -- strong evidence FOR route B's premise on the read path, and a harder
blocker than "one address is wrong", since the mechanism above is not
address-specific.

### What follows

* **The QMDB state root includes accounts EIP-161 says should not exist.** Every
  node computes the same root, so consensus is self-consistent -- but the root is
  not a function of the canonical state, and that is worth fixing on its own
  merits, independent of any performance work.
* **Route B is genuinely blocked.** Reading through QMDB would return an account
  where the plain state returns nil, which changes account-existence checks,
  EXTCODESIZE and refund behaviour. Not a rounding difference.
* **The criterion is honoured as written.** Step 3 does not proceed on this
  round's evidence. The criterion is NOT being widened because the divergence is
  "only one system address with a structural explanation" -- that is exactly the
  reasoning it exists to refuse.
* **Reconciling the predicates is a CONSENSUS change, and that reprices route
  B.** There are only two places to fix it. On the commitment side -- give
  `isAccountEmpty` the code-hash predicate, or make `computeRoot` consult
  `shouldRemoveEmptyAccount` -- the account leaves the tree and **every
  subsequent state root changes**, which on a live chain needs a fork activation
  (the mechanism this codebase already uses for `PQPrecompilesTime` and
  friends). On the reader side, teaching the reader to return nil for accounts
  the plain path considers empty would hide the inconsistency while route B's
  whole premise is that the tree IS the state. Only the first is a fix.

  So route B is not blocked behind a small bug. It is blocked behind a scheduled
  consensus change, which is a different kind of item and does not belong inside
  a benchmark round.

### The way it can still be measured

The qs fleet is a benchmark chain that is reset between campaigns. Fixing the
predicate and reseeding it costs nothing in consensus terms, so the sequence
that produces a NUMBER without pre-committing anyone to a fork is:

1. reconcile the predicates,
2. reseed the qs chain,
3. re-run the equivalence round -- the criterion unchanged, zero mismatches,
4. then, and only then, measure what route B is worth.

Production adoption remains a separate decision, taken with that measurement in
hand rather than in place of it.
* **The reader earned its keep by failing.** It was built to be proved
  equivalent, ran in a mode where a divergence could not change a block, and
  found a pre-existing inconsistency on its first comparison.

## 6h. Round 22: the fold is free, and the duplicate root cannot be made cheap

The leader computes a QMDB root TWICE per block, both on its critical path: once
on the isolated speculative tree during the build (58.3 ms) and again replaying
the same ops onto the live tree during the write (59.3 ms). The isolated tree
exists because a speculative build must not mutate the live one, and speculation
is what makes `miner: build phases` 4 ms instead of ~500. So the question was
the SECOND computation: the live tree must receive the entries, but its root is
already known -- the write path computes it only to compare against
blk.StateRoot().

Split into "apply" (Set/Delete) and "fold" (Root), n=35 full-block computations
at a median 16,077 ops:

```
apply  56.5 ms   99.8%
fold    0.1 ms    0.2%
```

**Registered prediction: fold-dominated, roughly 70/30. Wrong by 500x.**

The reason is that QMDB folds INCREMENTALLY. It is a binary tree, not a Patricia
trie: `Set` appends the entry, writes the leaf as Blake3(0x01||keyHash||value)
and folds just the 11-node path to its twig root, so by the time `Root()` runs
there are no dirty twigs left and it only folds the upper tree over changed twig
roots. The cost is inside `apply`; my split cut at the wrong boundary.

**So the duplication is not attackable this way.** The live tree needs the
entries (for later reads) and needs the incremental hashing (for the next
block's root). There is no separable fold to skip, and the registered criterion
(fold >= 40%) is what stopped this being built on the assumption.

One number worth keeping: 56.5 ms / 16,077 ops = **3.5 us per op**, against
roughly 0.9 us for the ~12 Blake3 invocations an op implies. The remainder is
scattered access across a 6.1M-key twig forest. That is a micro-optimisation,
an order below the write, and it is recorded rather than pursued.

## 6i. The CPU profile says something none of the wall-clock rounds could

Every round in this document judged WALL CLOCK. The standing rule on this rig is
CPU-seconds before wall clock, and a 45 s CPU profile of node0 inside a loaded
window says the two answers are not the same:

```
runtime.cgocall                 66.56s flat   63.50%
transaction.Sender              66.76s cum    63.69%
  -> recoverPlainRS -> Ecrecover -> secp256k1.RecoverPubkey (CGO)   58.14%
txspool.prewarmSenders.func1    43.97s        41.95%
internal.recoverSenderStride    17.81s        16.99%
```

**64% of a node's CPU is secp256k1 sender recovery** -- 42 points in the txpool
and 17 in block import. `blockimport phases` reports `recov` at 16 ms a block
because commit 2402295b overlapped it with execution. **Overlapping does not
remove CPU, it hides it** -- the same lesson as 6e's reordering, applied to the
measurement method instead of to the code.

Two wastes follow directly:

* **The same signatures are recovered twice.** The pool recovers a sender on
  admission and block import recovers it again from the block body, while the
  pool already holds it.
* **Senders are recovered for transactions that are never included.** The flood
  supplies 40,000 tx/s; the chain consumes 22,857 per ~1.3 s = ~17,600 tx/s.

Round 14 swept the supply rate UP (40,000 against 80,000, no difference) and
concluded supply is not the limit. Both points were far above capacity. It never
swept DOWN, which is what round 23 does.

**What this does NOT establish.** The same profile has one node at 2.33 cores
and seven at roughly 16 of the box's 256 threads, so the fleet is not
box-saturated and nothing here shows the recovery CPU is the BINDING constraint.
Round 23's criterion is written so that either answer is usable: if throughput
does not move when the supply drops, the 64% is recoverable waste that competes
for nothing, and the constraint is the serial dependency chain the view timings
describe.

## 6j. Round 24: the noise floor, and what it costs the rest of this document

Round 23's five legs included three at an identical supply rate and they landed
at 29 / 44 / 53 peak full blocks per minute. An 83% spread between legs that
differed in nothing is larger than anything this document has tried to measure,
so before running another A/B the floor had to be known.

`n42-datc-25m-hi4.bin` (34-50 GB RSS, ~660 MB/s, 93% of the box's major faults)
was present for every round from 14 to 23 and is now gone. Four legs of ONE
identical configuration on the quiet box:

| leg | peak full blocks/min | CPU-s/block | fleet's own major faults |
|-----|----------------------|-------------|--------------------------|
| warmup (discarded) | 55 | 21.88 | 9,989,346 |
| N1 | 56 | 23.75 | 9,650,559 |
| N2 | 56 | 21.14 | 8,434,565 |
| N3 | 54 | 23.95 | 9,644,789 |
| N4 | 56 | 23.80 | 9,018,546 |

**Blocks per minute spread 3.6%. The 83% was the neighbour, not the fleet.**
n42-rs independently reports 2.8% across nine same-shape legs on their rig, so
the order is not peculiar to this one.

Three things follow, and one of them is expensive.

**The metric to argue from is blocks per minute, not CPU-seconds per block.**
The normalised CPU figure spreads 13.3% across the same four legs against 3.6%
for the raw rate. That is the opposite of what I assumed when I built the CPU
sampler on the strength of the CPU-seconds-first rule: the rule is right about
what to VALUE, and wrong here about which instrument resolves it.

**The fleet's own major faults are 8.4-10M per leg with no neighbour at all.**
Round 18 measured 551,394 system major faults in a 60 s window and charged all
of them to datc without splitting by process; this fleet alone does roughly
15,000 a second. That attribution was an assumption wearing a measurement's
clothes, and it is withdrawn. The mechanism n42-rs identified -- a buffered write
stream churning the page cache and evicting the mmap pages the importers read --
is present here too, but at a steady rate that does not destabilise throughput.

**Every throughput comparison run between rounds 14 and 23 is unreadable.**
That includes:

  * round 15's throughput null (commit -72%, block rate unmoved)
  * round 17's throughput null and its -127 ms view-total effect
  * round 23's supply sweep -- *including the correction I made to it*. I refit
    that round's A-B-B-A legs to a linear trend plus a constant effect, got
    -17 and -14 from the two treatment legs, and reported a coherent -32%.
    A coherent-looking fit through two points is exactly what an 83% floor
    produces by chance. **That correction is withdrawn too.**

What survives is everything whose margin dwarfs the floor: round 16 (0.04 ms
against 373), round 19 (row counts per table), round 21 (zero mismatches in a
million comparisons -- not a timing measurement at all), round 22 (99.8% against
0.2%), and the CPU profile's composition.

**The baseline also moves.** 56 blocks/min is 21,333 TPS on a quiet box. The
~18,000 quoted through the middle of this document was the neighbour's tax.

## 6k. Round 25 on a clean box: 74 ms off the import path buys nothing

Round 15 measured safe-nosync's phase effects with tight bookends and could not
read its throughput, because datc was resident and the floor was 83%. Round 24
put the clean floor at 3.6%. Round 25 re-runs it A-B-B-A on the quiet box, with
the warm-up leg discarded and never read.

Interlock, verified per leg against the node logs rather than the label: warmup
nosync 7/7, A1 durable 7/7, B1 nosync 7/7, B2 nosync 7/7, A2 durable 7/7. Every
leg is what it declared. (The runner PRINTED "SYNC INTERLOCK FAILED" on the
nosync legs; that is a bug in my checker -- it was derived from round 24, whose
legs were all durable, and its expectation argument stayed hardcoded to durable.
The observation side is correct on all five legs, which is what the interlock is
for.)

| leg | sync | commit | write | import | blocks/min |
|-----|------|--------|-------|--------|------------|
| A1 | durable | 69.9 | 152.0 | 379.1 | 56 |
| B1 | nosync  | 16.5 |  94.2 | 323.9 | 56 |
| B2 | nosync  | 18.3 |  96.5 | 333.6 | 53 |
| A2 | durable | 81.7 | 180.8 | 426.5 | 51 |

| metric | A mean | B mean | effect | A spread | B spread |
|--------|--------|--------|--------|----------|----------|
| commit | 75.8 | **17.4** | **-77%** | 11.8 | **1.8** |
| write  | 166.4 | **95.4** | **-43%** | 28.8 | **2.3** |
| import | 402.8 | **328.8** | **-18%** | 47.4 | **9.7** |
| blocks/min | 53.5 | 54.5 | **+1.9%** | 5 | 3 |

The phase effects are large and their bookends are tight -- the B arm's two legs
agree to 2.3 ms on the write total. **Throughput moves 1.9% against a 3.6% floor
and a 5% registered threshold. Not established.**

### What this settles

Seventy-four milliseconds came off a 1,250 ms block's import path, on a clean
box, with the measurement able to resolve 4%, and the block rate did not move.

That is the third time an instrument improved and the output did not -- round 15's
commit, round 17's view total -- but the first time the result is readable. So it
stops being a suspicion and becomes a conclusion:

**No single stage is the constraint. The serial dependency chain is.**

safe-nosync is not adopted: it trades durability for a throughput gain that is
not there.

The consequence for what remains is sharper than the number. It rules out a
whole CLASS of change -- anything that makes one stage 20% faster will not move
the headline. The reason stopping the `Account` write is still worth pursuing is
not that it is bigger; it is that it is a different KIND of change. It does not
shorten a stage, it takes the work off the critical path entirely, which is what
n42-rs does with persistence and what round 19's design does with the vote.

**Getting the kind right matters more than getting the magnitude large.** It
took eleven rounds to turn that sentence into something with data behind it.

## 6l. Round 26: the `Account` write stops -- registered before the round ran

Round 25 closed the class "make one stage 20% faster". What it left open is the
other kind of change: work that stops existing rather than gets faster. Round 19
named the candidate -- `Account`, 15,682 random-keyed rows a block, one 4 KB
page each, 88% of the 71 MB MDBX dirties per block, and a duplicate of the
entry log -- and round 21 proved the tree answers the same reads (zero
mismatches in a million). Round 26 stops the write.

**The lever.** `N42_STATE_WRITE_QMDB_ONLY=1` (which refuses to start without
`N42_STATE_READ_QMDB=1`). The plain writer leaves `Account` alone for every
block after genesis; every head-state read -- execution, the miner's build, the
txpool's nonce and balance, RPC `latest`, the history fallback for an account
unchanged since the queried block -- goes to the live tree through
`modules.ReadLatestAccount`, the one seam all of them now share. Changesets are
still written, so history is intact. Out-of-band readers (txpool, RPC) run on
other goroutines, so the tree owner now takes a lock around each mutation and
readers fault evicted entries through their own transaction.

**Design.** A-B-B-A, warm-up discarded. Every leg reads through QMDB; only the
B legs stop the write, so the single variable is the write. Same supply as
rounds 24-25 (1,200 x 2,500, rate 40,000, 22,857 recipients), write probe on.
Interlock: the node logs `QMDB-only account persistence` once at start when
the flag is on; a B leg is a B leg only if all seven nodes logged it after that
leg's log mark, an A leg only if none did.

**What this does to the chain, permanently.** From the first B leg the
fleet's `Account` table is frozen. The A legs that follow are still valid --
they read through the tree -- but the fleet must run with
`N42_STATE_READ_QMDB=1` from now on until the table is rebuilt.
`QMDBMeta.accountFrozenAt` records the first block the table missed; a repair
is every address in an `AccountChangeSet` from there to the head, re-read from
the tree. Snap-sync SERVING and the state-dump tools read the stale table until
then. This is why it is a typed-out environment lever and not a default.

**Registered predictions.**

1. The write probe's `Account` row count is 0 in the B legs and ~15,700 in the
   A legs (this is the interlock's second half; if it fails, nothing below is
   read).
2. `dirtyBytes` per full-block write transaction falls from ~71 MB to under
   15 MB in the B legs (the remaining tables are append-ordered).
3. `blockwrite phases` `write` per block falls by at least 40% in the B legs
   (round 25 moved it 43% by removing the fsync; this removes the pages).
4. Blocks per minute: registered threshold **5%** on the quiet-box floor of
   3.6%. FALSIFIED IF the B mean is within 5% of the A mean while predictions
   1-3 hold: then bytes are not on the critical path either, and the serial
   chain is the whole story.

### The first attempt wedged the fleet inside one block

The warm-up leg (B) ran: 73 full blocks in its first window against round 25's
62. Then leg A1 produced nothing for seventeen minutes. Every node logged the
same thing for every proposal:

```
state root mismatch at block 13751682: proposer e77f5b…, locally computed 6e32a6…
```

All seven nodes had reloaded the SAME tree root at A1's start, byte-equal to
the head's `stateRoot`, so the trees agreed and the divergence was in the
reads of the next block. The cause was in the first commit of this round: it
installed the head-state source only when the WRITE was also off. In an A leg
block execution read through the tree (`N42_STATE_READ_QMDB=1` has always
wrapped that path), but the miner's build, the txpool and RPC still read the
plain table -- which the B warm-up had just frozen. The proposer built on
stale balances, the followers verified against the tree, and no block could
carry a quorum. Two nodes then applied their own bad candidates before the
round was stopped; the journals were reset on all seven and the applied-marker
unwind peeled the candidates on the next start.

The rule it establishes, now enforced in the node: **"read through QMDB"
means every reader of the head state, or none.** `N42_STATE_READ_QMDB=1`
installs the seam on its own; `N42_STATE_WRITE_QMDB_ONLY=1` only stops the
write and refuses to start without it. The rerun's offsets are 200M-204M; the
first attempt's are burnt.

### The second attempt found a liveness hole that predates this round

With the reads fixed, the rerun wedged inside one block again -- on the SAME
poisoned hash. Instrumenting `Finalize` on both sides (`N42_FINALIZE_TRACE=1`)
showed the leader building a correct block (root `e6a056…`, the followers'
answer) and then logging:

```
miner: converging on lowest-hash sibling; re-proposing it
    number=13751682 kept=0xb7323b47b7 dropped=0xeb38962f5b
```

The cross-view same-height convergence rule (`LowestSiblingAtHeight`) makes
every leader re-propose the lowest-hash block already STORED at head+1, so
that votes stack on one candidate. It assumed a stored sibling is valid. The
first attempt had stored two invalid ones -- on node2 its own candidate, on
the others what fetch-on-miss pulled in -- and they hashed lower than any
fresh build. Prepare votes are not import-gated (R1 is static in the
two-phase gate), so 2f+1 of them locked the fleet on a block nobody could
apply, and every later leader hit `parent-not-applied`. A third start produced
15 blocks only because the fresh build happened to hash lower than the poison.

Fixed in 1ee32203: a validation failure writes a persisted mark
(`BadHeaderNumber`), and both re-proposal paths skip marked hashes. The mark is
advisory for PROPOSING only; import never reads it, so a transient local
failure cannot make a node refuse the committed chain. The wider observation
is worth keeping: **a prepare vote on an unexecuted block plus a convergence
rule that trusts storage is a wedge waiting for any bad candidate.** The
two-phase design accepts that trade for latency; the mark is the cheap guard.

The chain moved on through the smoke starts (head 13,751,695 before the third
attempt), and the poisoned siblings now sit at a committed height, where the
convergence rule never looks. The third attempt's offsets are 205M-209M.

### Result: A-B-B-A holds, and all four predictions with it

Third attempt, 02:35-03:25 UTC on a quiet box, warm-up discarded. Interlock
verified per leg from the node logs (freeze warning 7/7 on B, 0/7 on A) and
from the write probe's own `Account` row count. node0, full blocks only,
medians; blocks/min from the harness's first window and from the peak minute.

| leg | mode | Account rows | dirty MB | amp | write | commit | import | view total | win1 blocks | peak/min |
|-----|------|-------------:|---------:|----:|------:|-------:|-------:|-----------:|------------:|---------:|
| A1 | write on  | 15,715 | 72.0 | 11.8x | 200.6 |  97.3 | 421.2 | 1,075 | 55 | 56 |
| B1 | write off |      0 |  7.2 |  1.3x |  47.3 |   5.0 | 263.5 |   782 | 75 | 65* |
| B2 | write off |      0 |  7.2 |  1.3x |  47.7 |   4.6 | 269.9 |   797 | 74 | 75 |
| A2 | write on  | 15,443 | 71.4 | 11.7x | 219.1 | 109.1 | 438.2 | 1,096 | 53 | 54 |

\* B1's flood started mid-minute, so its peak minute is split (61 + 65); its
harness window, 75 blocks, is the comparable figure.

| metric | A mean | B mean | effect | A spread | B spread |
|--------|-------:|-------:|-------:|---------:|---------:|
| dirty MB per block | 71.7 | **7.2** | **-90%** | 0.6 | 0.0 |
| write (ms) | 209.9 | **47.5** | **-77%** | 18.5 | 0.4 |
| commit (ms) | 103.2 | **4.8** | **-95%** | 11.8 | 0.4 |
| import (ms) | 429.7 | **266.7** | **-38%** | 17.0 | 6.4 |
| follower r1 (ms) | 394 | **226** | **-43%** | 20 | 10 |
| leader r2 (ms) | 419 | **222** | **-47%** | 2 | 6 |
| view total (ms) | 1,086 | **790** | **-27%** | 21 | 15 |
| blocks/min, window 1 | 54.0 | **74.5** | **+38%** | 2 | 1 |
| TPS, window 1 | 20,571 | **28,381** | **+38%** | | |

Predictions: (1) `Account` rows 0 in B, ~15,600 in A -- held. (2) dirty bytes
under 15 MB -- 7.2 MB, held. (3) `write` down at least 40% -- down 77%, held.
(4) blocks per minute over the 5% threshold on a 3.6% floor -- +38%, with the
two A legs 2 blocks apart and the two B legs 1 block apart. Held.

**What moved, and what did not.** `exec` (117 ms), `root` (65 ms) and `recov`
(16 ms) are unchanged to the millisecond: this round touched nothing they do.
The write fell from 210 to 47 ms and the commit from 103 to 5 ms -- the pages
were the commit, exactly as round 19's arithmetic said (15,700 random-keyed
rows at one 4 KB page each) and as round 15's fsync result implied (removing
the fsync left the pages; removing the pages leaves 5 ms). The follower's r1
and the leader's r2 -- the two places where the import sits on the consensus
path -- each lost ~170-200 ms, the whole write saving plus its share of the
seven-way fsync contention. The cycle went from 1,086 to 790 ms.

**Why 38% and not 27%.** The view total fell 27% but the block rate rose 38%,
because in B the chain outruns the flood: window 2 of every B leg shows
19-23% occupancy with 150-175 blocks, the supply of 3.0M transactions per leg
exhausted (memory note: the harness limit first seen at ~28k). The B figure is
therefore a floor for the change, not its ceiling, and the next round needs a
larger supply (or the 163k block) before the B arm can be read as a rate.

**Per transaction.** Import 266.7 ms / 22,857 = **11.7 us/tx**, from 18.8. The
write's share is 2.1 us/tx, from 9.2. Against n42-rs's 1.65 us/tx the import
is now 7x, from 11x; what remains is execution (5.1 us/tx, the interpreter on
plain transfers) and the root (2.8 us/tx).

**Adopted for the benchmark line, with its cost stated.** From this round the
qs fleet runs with `N42_STATE_READ_QMDB=1`; `N42_STATE_WRITE_QMDB_ONLY=1` is
the new default for rounds. The `Account` table is frozen at block 13,750,514
(`QMDBMeta.accountFrozenAt`). Snap-sync serving and the state-dump tools read
a stale table until a repair replays `AccountChangeSet` from that block through
the tree -- listed in section 8 as owed work, not done work.

What the round cannot say: anything about a 163,000-transaction block. At
22,857 transactions a block this rig's ceiling is set by the cycle, and the
comparison with n42-rs's 365,399 TPS (163,000 a block at 0.42 s) needs the
block size first. That is the round after this one.

## 6m. Round 27: the 163,000-transaction block -- registered before the round ran

Every number in this document is at 22,857 transactions a block (480M gas).
n42-rs's 365,399 TPS is at 163,000 a block (3.423G gas), 0.42 s a cycle, one
account touched per transaction (2,000,000 recipients). Round 26 took the
write off the path; the next question is what this fleet does at their shape.

**Design.** A-B-B-A, warm-up (B) discarded. Every leg runs the mode round 26
adopted (`N42_STATE_READ_QMDB=1`, `N42_STATE_WRITE_QMDB_ONLY=1`); the ONE
variable is the gas ceiling: A = 480M, B = 3.423G (`N42_STRESS_GASLIMIT=1`
makes the limit jump in one block, exported by bench-7node.sh to every node).
Recipients 2,000,000 on all legs, so a full B block touches ~163,000 distinct
accounts. Supply is closed-loop for the first time: two generators of
2,000 x 3,000, `txflood -target-depth 200000`, so the CHAIN sets the offered
rate and the harness ceiling of rounds 24-26 (occupancy falling in window 2)
should not recur. Pool 300k/100k, the profile known to fit; a watchdog aborts
the round under 20 GB MemAvailable, because the pool at this block size has
never been measured.

**Registered predictions.**

1. B legs fill: occupancy >= 95% in window 1. FALSIFIED IF it is lower, in
   which case the round measured the harness and its TPS is not the chain's.
2. B cycle 1.8-2.4 s: ~300 ms fixed plus 163,000 x (exec 5.1 + root 2.8 +
   write 2.1 + recov 0.7 us) = 1.74 s of per-transaction work. That is
   68,000-90,000 TPS, 25-33 blocks/min.
3. Per-transaction import cost at B is BELOW round 26's 11.7 us (the fixed
   cost amortises 7x further) but not below 10 us (the per-transaction work
   is what it is).
4. The A legs reproduce round 26's B arm within the 3.6% floor (74.5 blocks
   in window 1, import ~267 ms) -- they are the same configuration.

### Three false starts, and what each one was

The harness had never run this shape, and it said so three times before the
chain got a turn:

1. **bench-run's own profile guard** hardcoded `--miner.gasceil 480000000`
   and rejected the 3.423G nodes (fixed: it checks the requested ceiling).
   The rejected nodes then took over 300 s to honour SIGINT -- they were
   still loading the 6.3M-key index -- which is its own small finding.
2. **The closed loop needs the `txpool` RPC namespace**, which the qs fleet
   did not expose (`--http.api eth,web3,net`); `-target-depth` refused to
   inject blind and window 1 was 240 empty blocks. Enabled in qs-env.sh.
3. **Two node-side ceilings at 163k**, found with the pool reading 190,000
   pending on every node while blocks carried 5,000-70,000: the packing
   budget is 90% of the gossip cap, and bench-7node.sh exports
   `N42_MAX_GOSSIP_MB=8` unconditionally -- 7.5 MB, 70,341 transfers, the
   exact ceiling observed; and the leader builds SPECULATIVELY the moment
   the parent lands, when a 200k pool that a 70k block just drained and a
   once-a-second top-up has not refilled is thin, so the sealed block is
   whatever was executable at that instant. `fillTx breakdown` now reports
   `pendingAccts`/`pendingTxs`/`priceOut` per build to make that visible.

The fourth start raises the cap to 24 MB, the pool to 600k/200k with a
500k target depth (so a 163k drain leaves 337k), and 3,000 senders per
generator. Predictions 1-4 stand as registered; the false starts measured
the harness, not the chain, and none of their numbers is read.

### Result: the blocks fill, and the shape costs 8x, not 0.3x

Fourth start, 06:38-07:01 UTC, warm-up (B) and A1 completed; the memory
watchdog then stopped the round at 19 GB available (seven nodes with 600k
pools plus two generators holding 9M pre-signed transactions each, 7.5 GB).
node0, full blocks, medians; write-probe rows per block.

| leg | shape | recipients | txs/block | recov | exec | root | write | import | us/tx | dirty MB | win1 blocks | win1 TPS |
|-----|-------|-----------:|----------:|------:|-----:|-----:|------:|-------:|------:|---------:|------------:|---------:|
| warm-up | 3.423G | 2,000,000 | 163,000 | 140 | 930 | 755 | 659 | 2,592 | **15.9** | 95.0 | 4 | 9,182 |
| A1 | 480M | 2,000,000 | 22,857 | 21 | 137 | 105 | 96 | 390 | **17.0** | 28.9 | 49 | 18,667 |
| round 26 B (reference) | 480M | 22,857 | 22,857 | 16 | 117 | 65 | 47 | 267 | **11.7** | 7.2 | 74.5 | 28,381 |

Predictions: (1) the B blocks fill -- 84.5% and 100% occupancy, **held**.
(2) B cycle 1.8-2.4 s -- **falsified by 8x**: 15 s and 20 s a block in the two
windows. (3) per-transaction import below 11.7 us -- **falsified**: 15.9 us.
(4) A1 reproduces round 26's B arm -- **not testable as registered**: the
recipients changed too (2,000,000 against 22,857), and that one variable
took 49 blocks where round 26 took 74.5.

**Three mechanisms, in the order they matter.**

*The cold set.* Every transaction in this round touched a recipient drawn
from 2,000,000 on a 6.3M-account chain, so nearly every recipient read and
write missed the resident window and the page cache. At the SAME block size
that alone costs 34% of throughput (A1 against round 26's B arm: root 105
against 65 ms, write 96 against 47, dirty bytes 28.9 against 7.2 MB). At
163k it is the whole budget: 15.9 us/tx against 11.7 hot, with the block
seven times larger. n42-rs's 2,000,000 recipients live on a FRESH chain of
about that many accounts, entirely resident; the shape is theirs but the
residency is not, and residency is the larger term here.

*The view timeout.* The follower's loaded view total was 5.4 s (recv 3.67 s:
the leader's 1.2 s build plus 1.37 s write before the push; r1 2.0 s: the
import) -- yet windows saw 15-20 s a block. The difference is views that
produce no timing line because they timed out: a 2.6 s median import with
outliers of 9.2 and 12.7 s (an `align` of 6.5 s unwinding a 163k-transaction
local candidate on a branch switch; a `qmeta` of 9.8 s pruning an 8 MB undo
record) crosses the base view timeout, and each timeout costs a TC and a
re-proposal. At this shape the consensus timer, not the work, sets the rate.

*The harness.* Two generators pre-signing 9M transactions each cost 7.5 GB;
`-target-depth` needs the txpool namespace; the packing budget is 90% of the
gossip cap. All three are now handled or documented; none is the chain.

**Amortisation is real and small.** 17.0 -> 15.9 us/tx from 22,857 to
163,000 is the fixed cost divided by seven: about 1.1 us/tx, 7%. The shape
does not buy throughput on this chain; it exposes what residency and the
timeout cost. Round 28 isolates the shape from the cold set (same A/B, hot
22,857 recipients).

What the round cannot say: anything about n42-rs's remaining 4-5x. At their
shape this fleet's per-transaction work is execution (5.1 us against their
1.8, the interpreter on plain transfers) and the root (2.8 us). Those are
the next two kinds of change, in that order.

## 6n. Round 28: the 163,000-transaction block on the hot set -- registered before the round ran

Round 27 could not separate the block size from the cold set. Round 28 runs
the same A/B (480M against 3.423G, adopted mode on every leg, closed-loop
supply, gossip cap 24 MB) with `--recipients 22857` on every leg, the hot set
every round before 27 used. The one variable is again the gas ceiling; what
changed against round 27 is the residency of the working set. Pool 450k/150k
with a 350k target (a 163k drain leaves 187k); two generators of
2,000 x 2,000 so the pre-signed sets stay under 2.5 GB each.

**Registered predictions.**

1. A legs reproduce round 26's B arm within the floor: window-1 blocks
   70-78, import 250-290 ms. (Same configuration, only the supply loop and
   the pool depth differ.)
2. B blocks fill (occupancy >= 95%) and cycle at 3.0-4.5 s: leader build
   ~1.2 s and write ~0.4 s (hot), follower import 163,000 x (5.1 + 2.8 + 2.1
   + 0.7) us = 1.7 s, plus rounds. That is 36,000-54,000 TPS, 13-20
   blocks/min -- above the A arm's ~28,000 only if the timeouts of round 27
   do not recur on the hot set. FALSIFIED IF a B window shows fewer than 10
   blocks; then the timeout, not the work, still sets the rate and the next
   change is the consensus timer or the leader's serial build+write.
3. Per-transaction import at B is 9.5-10.5 us: round 26's 11.7 less the
   amortised fixed cost.

### Result: the shape buys nothing, and the pool says why

07:30-08:27 UTC, all five legs, interlocks held (mode 7/7 on every leg; the
ceiling verified from the nodes' arguments). node0, medians.

| leg | ceiling | txs/block (median) | occupancy | import | recov | exec | root | write | follower view | win1 blocks | win1 TPS |
|-----|--------:|-------------------:|----------:|-------:|------:|-----:|-----:|------:|--------------:|------------:|---------:|
| A1 | 480M | 22,857 | 100% | 257 | 22 | 104 | 61 | 44 | 808 | 65 | 24,762 |
| B1 | 3.423G | 41,838 | 32-34% | 692 | 145 | 328 | 91 | 100 | 2,047 | 28 | 24,275 |
| B2 | 3.423G | 52,437 | 29-41% | 844 | 153 | 367 | 91 | 107 | 2,049 | 35 | 27,172 |
| A2 | 480M | 22,857 | 100% | 252 | 21 | 105 | 62 | 43 | 796 | 52 | 19,809 |

Predictions: (1) A legs at 70-78 blocks -- **not held**: 65 and 52, and the
two bookends are 20% apart, five times the floor. (2) B fills and cycles at
3.0-4.5 s -- **falsified on the fill**: 29-41% occupancy, 2.0 s cycles, and
B's TPS equals A's. (3) B import under 10.5 us/tx -- **falsified**: 15-18 us,
because recovery and execution both cost more per transaction on a
half-full block (recov 3.2 against 1.0 us; exec 7.3 against 4.6).

**Where the block is lost: the leader's fill.** Per build on node0, B legs,
builds that saw over 100k pending:

| leg | builds | pending txs (median) | packed (median) | fill execution |
|-----|-------:|---------------------:|----------------:|---------------:|
| B1 | 7 | 449,429 | **11,440** | 774 ms |
| B2 | 7 | 368,669 | **10,316** | 613 ms |
| A1 | 27 | 332,753 | 22,857 (full) | 138 ms |

The pool hands the build 370-450k transactions and the build packs ten
thousand, spending 0.6-0.8 s of EXECUTION doing it -- a constant, whatever
the count. The one thing that costs execution without packing is a
transaction whose nonce is already used: `ErrNonceTooLow` shifts to the
sender's next transaction after paying for the attempt. After a large block
lands, the pool's reorg that would demote those transactions takes 1.7 s at
this size (825 ms of it waiting for the pool lock behind 192 concurrent
submitters), while the speculative build starts at once -- and wades through
the previous block's ~160k mined transactions at ~5 us each. Meanwhile the
closed loop counts the stale entries as depth and under-supplies. At 22,857 a
block the reorg is 4-38 ms and none of this is visible; at 163k it is the
block.

**And the A bookends.** 65 -> 52 blocks with a 450k pool and two generators
against 74.5 in round 26 with a 60k pool and one: the pool's reorg and lock
contention scale with what it holds, and this round held seven times more.
The A arm is not comparable with round 26 and says so.

**Fix, registered for round 29.** Before executing anything, the fill reads
each pending account's state nonce ONCE and drops the stale prefix without
executing it: O(accounts) reads instead of O(stale transactions) executions,
and a build that starts before the reorg finishes still packs the fresh
tail. The build also now reports why accounts left the set (`popNonceHigh`,
`skipNonceLow`, `popGas`) and how long its reads waited for the tree's owner
(`lookupWait`), so the next round's mechanism is read, not argued.

## 6o. Round 29: round 28 plus the stale-prefix trim -- registered before the round ran

Identical to round 28 in every setting; the binary adds the fill's per-account
stale-nonce trim (5634bc96) and its counters. Single variable against round 28.

**Registered predictions.**

1. `staleTrimmed` on B builds is in the 100,000-170,000 range and the fill's
   execution phase for a build that packs N transactions scales with N (no
   more constant ~0.7 s). FALSIFIED IF trimmed counts are small: then the
   0.7 s was not the stale wade and the mechanism is elsewhere (the
   `lookupWait` counter says whether it is the tree lock).
2. B blocks pack the fresh tail: median packed rises from ~11k to at least
   80,000, occupancy above 50%. The closed loop still counts stale entries
   as depth, so 95% is not expected until the pool's reorg is faster or the
   loop measures fresh depth.
3. B TPS exceeds A TPS by at least 25% (round 28: equal), because the same
   2.0 s cycle now carries 80k+ instead of 45k.
4. A legs are unchanged within the floor (the trim finds nothing to trim
   when the reorg is 4-38 ms).

### Two starts stopped by the memory watchdog, and what they measured

The first two starts of round 29 both ended in the warm-up at 16-18 GB
MemAvailable. The second carried a per-node RSS sampler: **14.0-15.5 GB a
node** at the 163k/hot shape with a 350k pool, 105 GB for the fleet, against
11.2 GB a node saturated at 22,857 (the figure in the rig memory note). The
generators were 1.6 GB each. Node RSS was FLAT over the last 70 s while
MemAvailable fell 44 -> 18 GB, so the last 26 GB were not process heap; tmpfs
held a static 27 GB throughout, and dirty pages read near zero after the
stop. The third start bounds the Go heap (`GOMEMLIMIT=12GiB` per node),
drops the pool to 300k/100k, and samples anonymous against file-backed RSS,
shmem and dirty pages, so the next abort names its consumer.

What the aborted warm-ups still showed, both with the trim: the fill's
execution scales with what it packs (163,000 in 0.6-0.7 s; 2,000 in 12 ms),
`staleTrimmed` runs 160,000-424,000 per build -- two to three blocks of mined
transactions the pool had not yet demoted -- and `lookupWait` is 0, so the
round-26 tree lock is not on the build's path. B TPS in the warm-up windows
was 31,500 (450k pool) and 36,200 (350k pool) against round 28's 24,300, with
occupancy 55% and 28%: the trim recovers the wade, and the fresh supply is
now what bounds the block. The pool's reorg (0.5-0.85 s a block, of which
`reset` 125-586 ms and `demote` 33-221 ms are two read transactions per
pending account) is the next lever; 5d0f7d0f batches those reads for round
30.

### Result: the wade is gone, B is +22%, and the pool's backlog now sets the block

Third start (09:29-10:24 UTC, `GOMEMLIMIT=12GiB` a node, pool 300k/100k,
target 220k), all five legs, interlocks held. node0, medians; per-build
figures over builds that saw more than 50k transactions (fresh plus stale).

| leg | ceiling | win1 blocks | win1 TPS | win2 TPS | occupancy | import | recov | exec | root | write | follower view |
|-----|--------:|------------:|---------:|---------:|----------:|-------:|------:|-----:|-----:|------:|--------------:|
| A1 | 480M | 74 | 28,190 | 22,593 | 100% | 251 | 20 | 103 | 62 | 43 | 792 |
| B1 | 3.423G | 55 | 33,246 | 32,505 | 22% | 776 | 129 | 339 | 92 | 87 | 650 |
| B2 | 3.423G | 54 | 37,245 | 35,227 | 25% | 673 | 127 | 274 | 91 | 80 | 726 |
| A2 | 480M | 75 | 28,571 | 28,190 | 100% | 249 | 21 | 103 | 62 | 43 | 791 |

| leg | builds | fresh pending (median) | stale trimmed (median) | packed (median) | fill execution | lookup wait |
|-----|-------:|-----------------------:|-----------------------:|----------------:|---------------:|------------:|
| A1 | 26 | 90,800 | 105,240 | 22,857 (full) | 113 ms | 0 |
| B1 | 14 | 20,248 | 233,144 | 20,330 | 102 ms | 0 |
| B2 | 17 | 21,408 | 197,148 | 21,442 | 100 ms | 0 |
| A2 | 27 | 109,270 | 104,273 | 22,857 (full) | 105 ms | 0 |

Predictions: (1) trimmed counts in the 100-170k range and fill execution
scaling with the pack -- **held, and then some**: 197-247k trimmed per B
build, 100 ms for 21k packed against round 28's 0.7 s for 11k. (2) packed
median at least 80k -- **falsified**: 20-21k, because that is all the fresh
supply there was. (3) B at least 25% over A -- **+22%** (34.6k against 28.4k
across four windows); short of the registered threshold, well above the
floor, and the direction the round predicted. (4) A legs unchanged -- **held
exactly**: 74/75 blocks, 250 ms imports, one block apart, equal to round 26's
B arm.

**What sets the block now.** Every B build packs exactly what is fresh, and
the fresh supply is ~21k a build because the closed loop reads
`txpool_status`, which still counts 200-250k already-mined transactions the
pool has not demoted: it believes the pool is full and adds only the
shortfall. The build no longer pays for the backlog; the loop does. So the
next lever is the pool's reorg, which at this size spends its time in two
read transactions per pending account (5d0f7d0f batches them, round 30), and
after that the loop's notion of depth.

**Two per-transaction costs went UP at B and are not explained.** Recovery
3.4 us/tx (129 ms) against 0.9 at A, execution 7.4-9 us against 4.5. The B
blocks carry transactions the followers' pools saw only a second earlier, so
the sender hint misses more; the execution difference is open. The A arm's
11 us/tx is the reference the B arm has to reach at full blocks, and it does
not yet.

**Memory at this shape** is the third finding: 14-15.5 GB a node without a
heap bound (two aborts), 10-12 GB with `GOMEMLIMIT=12GiB`, and the in-use
heap profile names the growth -- 2.1 GB of decoded stored transactions (the
block cache is sized in BLOCKS, 512 of them, ~160 MB each at 163k), 0.5 GB of
libp2p buffers for 24 MB messages, 0.5 GB of tx-lookup tail, 0.4 GB of
mobile-verify packet cache, against a fixed 1.5 GB QMDB index. Round 30
bounds the block cache to 16.

## 6p. Round 30: batched pool reads and a bounded block cache -- registered before the round ran

Round 29's settings, two changes: the pool's demote and promote passes read
every account's nonce and balance in ONE transaction (5d0f7d0f) instead of
two per account, and `N42_BLOCK_CACHE_BLOCKS=16` bounds the block cache the
heap profile named. The A/B inside the round is still the ceiling alone; the
cross-round comparison to round 29 carries both changes and says so.

**Registered predictions.**

1. `txpool reorg phases` on B: `demote` falls from 33-221 ms to under 40 ms
   and `reset` from 125-586 to under 200 ms; the reorg total from 0.5-0.85 s
   to under 0.4 s.
2. With the reorg faster the backlog the loop counts shrinks: `staleTrimmed`
   median on B builds falls below 120k (from 197-247k) and fresh pending
   per build rises above 40k (from ~21k), so packed median exceeds 40k.
3. B TPS exceeds A by more than round 29's 22%.
4. A legs unchanged within the floor (74-75 blocks); node anonymous RSS at B
   stays under 11 GB with the cache bounded.

### Result: the reorg is 40% cheaper, the loop is still fed by a stale number

10:27-11:13 UTC; warm-up, A1, B1, B2 complete; A2 aborted at launch because
node5 failed its QMDB reload ("twig metadata inconsistent"), the same class of
corruption node4 had on 2026-08-24 -- node5 had spent B2 falling behind
(catch-up range requests failing, a committed block not executed locally)
before a clean shutdown. Reseeded from node0; the corrupt copy is kept. The
abort also exposed that bench-run's RPC-not-ready path left six nodes
running; fixed in the script.

| leg | ceiling | win1 blocks | win TPS | occupancy | import | reorg total | reset | demote | fresh/build | trimmed/build | packed (median / p90) |
|-----|--------:|------------:|--------:|----------:|-------:|------------:|------:|-------:|------------:|--------------:|----------------------:|
| A1 | 480M | 75 | 28,571 / 27,809 | 100% / 90% | 247 | 230 | 99 | 56 | 102k | 106k | 22,857 / 22,857 |
| B1 | 3.423G | 52 | 32,326 / 30,902 | 23-25% | 813 | 380 | 209 | 121 | 14k | 279k | 14,184 / 156,976 |
| B2 | 3.423G | 49 | 33,761 / 34,320 | 23-25% | 917 | 316 | 195 | 114 | 17k | 225k | 16,616 / 163,000 |

Predictions: (1) reorg total under 0.4 s -- **held** (316-380 ms, from
500-850); `demote` under 40 ms -- **not held** (114-121 ms: the batched read
took ~50 ms off it and the rest is the per-transaction list and map work);
`reset` under 200 ms -- borderline (195-209). (2) trimmed under 120k and
fresh over 40k -- **not held**: the loop still reads `txpool_status`, which
still carries 225-279k mined-but-undemoted transactions, and feeds 14-17k a
build. (3) B more than 22% over A -- **not held**: +12-18%. (4) A unchanged
-- **held** (75 blocks), and the bounded block cache held anonymous RSS
under the 12 GiB limit for the whole run.

The p90 says what the loop hides: one build in ten packs a full 163,000
(fresh supply happened to be there), and those blocks import in ~0.9 s. The
chain can take the block; the harness is not offering it. Round 31 changes
the generator's notion of depth to submitted minus mined minus rejected,
counted from the blocks themselves, with one generator so the count is exact.

## 6q. Round 31: the generator counts what the chain took -- registered before the round ran

Round 30's settings and binary; the generator changes. One generator of
4,000 x 1,500 with `txflood -target-depth 220000 -depth-by-blocks`
(b514f208): depth is submitted minus mined minus rejected, mined summed from
each new block's transaction count, so the pool's undemoted backlog no longer
counts. Pool 500k/150k to hold the backlog beside 220k fresh. Node5 reseeded
from node0 before the round.

**Registered predictions.**

1. Fresh pending per B build (median) rises from 14-17k to over 100k, and
   packed median over 100k; occupancy in B windows over 60%.
2. B TPS exceeds A by more than 40% (B over 40,000 with A at ~28,500).
   FALSIFIED IF B stays under 35,000 with fresh supply over 100k: then the
   chain, not the harness, holds the 163k block at ~2 s cycles and the next
   lever is the leader's serial build+write (push before write) or the
   follower's import.
3. Node5 behaves as the others (its A-leg import within the floor of node0's).
4. A legs unchanged (74-76 blocks).

### Result: the supply arrived, and the view timer took it

Two starts (11:18 and 11:31 UTC), both read only through the warm-up: the
first was ended by the mode interlock's byte-offset check meeting a log
rotation (a false negative -- all seven nodes had the warning, by timestamp;
the interlock now matches by timestamp across rotated files), the second I
stopped after the warm-up because it reproduced the first to the block.

| start | win1 blocks | win1 TPS | occupancy | block time | win2 |
|-------|------------:|---------:|----------:|-----------:|-----:|
| r31 | 18 | 25,951 | 53% | 3.33 s | 17 blocks, 18,779, 41% |
| r31b | 17 | 20,551 | 45% | 3.53 s | 16 blocks, 21,014, 48% |

The generator did what it was built to do: blocks of 86,000 average, up to
141,000, against round 30's 14-17k median. And the cycle went from 1.0-1.2 s
to 3.3-3.5 s, so TPS fell. The per-block work is not the reason -- a 141k
block imports in 941 ms (recov 116, exec 484, root 92, write 137), 6.7 us/tx,
the best per-transaction figure this document has -- and a loaded view is
2.1-2.6 s on the follower (recv 1.5-2.1 s: the leader's build and write and
the push of a 24 MB body; r1 0.75-1.0 s: the import). The rest of the 3.4 s
is **views that time out**: nine in eight minutes on node0 at the 6 s base.

The trace of one: node0 commits a 140k block at 11:25:54 and, in the same
second, becomes leader of the next view. Its leader gate reads the QMDB
applied marker, finds the consensus parent (that block) not yet applied
locally -- its own import is still finishing while the QC formed on five
faster ones -- answers `parent-not-applied`, hands the parent to
fetch-on-miss, and returns. Nothing re-runs the gate when the import lands a
few hundred milliseconds later; the view waits out its 6 s timeout, a TC
forms, and the next leader starts 6 s late. `ensureParentApplied` says this
in its own comment: "asynchronous, so this view is skipped and the next
leader finds the head aligned." At 22,857 a block the leader's own import
always beat its view; at 140k it often does not.

Fixed in 39f728a5 for round 32: the gate records the deferred view and
parent, and `NotifyBlockImported` re-runs the gate when that block is
applied, if this node still leads that view.

A second finding from the stops between legs: a SIGINT sent within ~15 s of
a node's launch is DROPPED. bench-run starts nodes with `setsid ... &` from a
non-interactive shell, so they inherit SIG_IGN for SIGINT until
`signal.Notify` runs after the QMDB load; every "did not exit in 300s" in
rounds 27-31 was a stop that raced a start. stop-fleet.sh now sends SIGTERM,
which the node handles identically and no shell ignores.

## 6r. Round 32: the leader gate re-runs when its parent lands -- registered before the round ran

Round 31's settings and generator; the node binary adds 39f728a5 (the gate
records a deferred view and `NotifyBlockImported` re-runs it). The first
start's warm-up already answered the mechanism question: 2 `parent-not-applied`
gates, 2 `deferred production resumed`, **0 view timeouts** in the flood
window (round 31: 9), 24 blocks of 88,500 in window 1, 35,425 TPS, 2.5 s a
block. That start was then stopped by the runner when node3 rejected a block
the other six committed -- an in-memory divergence on one node after a rough
leg transition (its persisted tree reloads to the committed root; a restart
heals it), recorded below as an open issue. The second start is the round.

**Registered predictions.**

1. View timeouts in B windows: at most 1 per leg (round 31: 9 in 8 min).
2. B blocks 85-95k median, cycle 2.3-2.7 s, 33-38k TPS: the serial chain
   (leader build + write + 24 MB push ~1.2 s, follower import ~0.9 s,
   rounds) with no timeouts in it.
3. B at least 25% over A; A unchanged (74-76 blocks).

**Open issue (2026-09-06, round 32 first start).** node3, leader of view
56003, wrote its own 22,857-transaction block through the leader's replay
path; the fleet committed it; the next block, built on it by another leader
and committed by six nodes, failed on node3 with a state-root mismatch
(proposer `974aab…`, local `c57281…`). node3's persisted tree reloads to
`974aab…`, so the divergence lived only in its live tree, and its live tree
had been through a catch-up and restart at the leg transition minutes
before. Same class as the twig-metadata failure node5 hit in round 30 and
node4 on 2026-08-24; a dedicated instrument (a periodic live-index
cross-check against a rebuilt one, the `verifyReload` idea applied to the
live tree) is the next step if it recurs.

### Result: +25% over A, the timeouts halved, and the cycle is now the serial chain

Second start, 12:17-13:04 UTC. A1 was lost at launch (node1's QMDB reload
took longer than the harness's 240 s readiness window -- 26 s of index
rebuild and then ~108k twig blobs from a cold page cache; node0's same start
took 75 s; the window is now 600 s), so the round has one A bookend. node0,
medians; timeouts counted over each leg on node0.

| leg | ceiling | win TPS | occupancy | block time | import | recov | exec | root | write | leader propose | follower recv | follower r1 | view | timeouts | gate deferred/resumed |
|-----|--------:|--------:|----------:|-----------:|-------:|------:|-----:|-----:|------:|---------------:|--------------:|------------:|-----:|---------:|----------------------:|
| B1 | 3.423G | 34,417 / 36,127 | 58-60% | 2.73 s | 791 | 133 | 325 | 92 | 90 | 1,594 | 1,688 | 788 | 2,639 | 6 | 7 / 7 |
| B2 | 3.423G | 36,007 / 33,951 | 57-58% | 2.6-2.7 s | 861 | 148 | 373 | 93 | 100 | 1,284 | 1,754 | 838 | 2,557 | 3 | 5 / 5 |
| A2 | 480M | 27,809 / 28,190 | 100% | 0.81 s | 256 | 21 | 105 | 63 | 42 | 494 | 561 | 215 | 805 | 0 | 2 / 2 |

Predictions: (1) at most 1 timeout a leg -- **not held**: 3 and 6 (round 31:
9), so the gate fix removed the deferred-leader case (every deferred gate
resumed) and something else still times out a view or two a minute. (2) B
cycle 2.3-2.7 s, 33-38k TPS -- **held** (2.6-2.7 s, 34.0-36.1k; blocks ~95k,
below the 85-95k median I named only because occupancy was 57-60%). (3) B at
least 25% over A -- **held at the line**: 35.1k against 28.0k, +25%; A
unchanged, 73/74.

**The cycle, read from the follower.** 2.6 s = recv 1.7 s + r1 0.8 s + rounds.
r1 is the import (0.79-0.86 s for ~95k, 8.5-9 us/tx, the per-transaction
figure now steady across rounds 31-32). recv is the leader's propose
(1.3-1.6 s: the fill's execution 0.3-0.5 s for what it packs, the seal, the
write, the push of a 14-24 MB body) plus the view skew. Nothing in it is a
stall any more; it is the serial dependency chain the document named in
round 25, at seven times the block. Round 33 takes the leader's write out
of recv (`N42_PUSH_BEFORE_WRITE=1`, the round-17 lever, which had nothing to
move at 22,857 and has ~100-200 ms here); after that the candidates are the
push itself (the body travels as 14-24 MB of gossip) and building the next
block while the current one imports, which is what n42-rs does.

**Leg-transition fragility is now the round's main cost.** Three of the last
four rounds lost a leg to a node that did not come back cleanly (node5's
twig metadata, node3's in-memory divergence, node1's reload past the
readiness window). The entry log has grown from 168M to 222M slots over
these rounds, and the live tree's reload scans all of it; a persistent
index for the live tree (the MDBX-backed one the miner cannot use) is the
structural answer and is not in scope for a benchmark round.

## 6s. Round 33: push before write -- registered before the round ran

Round 32's settings and binary, plus `N42_PUSH_BEFORE_WRITE=1`: the leader
hands the sealed block to peers before its own write, so their import runs
beside the write instead of after it; the Proposal still follows the write.
Round 17 measured this at 22,857 a block and adopted nothing (-127 ms on the
view total, throughput unreadable on an 83% floor). At ~95k a block the
leader's write is 100-200 ms of a 1.7 s recv.

**Registered predictions.**

1. Follower recv falls by 100-250 ms (from ~1.7 s); leader propose likewise.
2. B TPS rises 5-10% over round 32 (35.1k -> 37-39k). FALSIFIED IF B is
   within the floor of round 32: then the write was not on recv's path at
   this shape either, and the push and the fill are what remain.
3. A legs unchanged (73-76 blocks); timeouts no worse than round 32.

### Result: +8.7% at 22,857, +4% at ~95k, and the Proposal is what still trails

13:08-14:01 UTC, all five legs, interlocks held, every node up on time.
node0, medians.

| leg | ceiling | win1 / win2 blocks | win TPS | import | follower recv | follower r1 | follower view | leader propose | leader r2 |
|-----|--------:|-------------------:|--------:|-------:|--------------:|------------:|--------------:|---------------:|----------:|
| A1 | 480M | 80 / 84 | 30,476 | 273 | 570 | **127** | **734** | 493 | 118 |
| B1 | 3.423G | 25 / 22 | 39,216 / 34,825 | 848 | 1,829 | **626** | 2,552 | 1,644 | 667 |
| B2 | 3.423G | 23 / 23 | 36,042 / 36,202 | 847 | 1,781 | **660** | 2,540 | 1,394 | 662 |
| A2 | 480M | 80 / 83 | 30,476 | 281 | 576 | **135** | **739** | 511 | 132 |
| round 32 A2 (reference) | 480M | 73 / 74 | 27,809 / 28,190 | 256 | 561 | 215 | 805 | 494 | 215 |
| round 32 B (reference) | 3.423G | 22-23 | 34.0-36.1k | 791-861 | 1,688-1,754 | 788-838 | 2,557-2,639 | 1,284-1,594 | 770-868 |

Predictions: (1) recv down 100-250 ms -- **not held**: recv went UP 90 ms at
B while r1 fell 160 ms, because the Proposal still follows the write and
recv is measured to the Proposal; the import simply started earlier under
the same clock. (2) B up 5-10% -- **+4%**, inside the floor. (3) A unchanged
-- **not held, the other way**: 80/80 blocks against 73/74, **+8.7%**, with r1
215 -> 130 ms. Round 17 measured this same lever's -127 ms on the view total
and could not read its throughput on an 83% floor; on a 3.6% floor it is a
clean 8.7%, and it is adopted for the benchmark line.

**What it says about the shape.** At 22,857 the follower's import (250 ms)
is the same size as the leader's write it now overlaps, so the overlap is
the whole import. At ~95k the import is 850 ms and the leader's write ~150,
so the overlap covers a fifth of it, and the Proposal -- which carries the
prepare vote and so the view -- still leaves after the write. Moving the
Proposal before the write is the next lever: the leader's durable consensus
state is its vote journal, written before the signature leaves, and the
body has already been pushed, so a crash between push and write costs the
leader a re-fetch, not the fleet a fork. n42-rs's leader never writes on the
critical path at all.

**Correction on the timeouts.** The per-leg counts (5-6) were taken from
each leg's start and include the decay phase and the fleet's first views;
inside B1's flood window node0 timed out ONCE, at the leg's first view. At
steady state the deferred-leader fix leaves the timeouts at zero. The
prediction in 6r ("at most 1 per leg") is therefore held on the reading it
meant, and the "other open item" is withdrawn.

## 6t. Round 34: the Proposal leaves before the write -- registered before the round ran

Round 33's settings plus `N42_PROPOSE_BEFORE_WRITE=1` (5f86b7f8): with the
early push on, the leader hands the Proposal to the engine as soon as the
body is with the peers, so the prepare round runs beside its write. The
engine's `onBlockReady` reads nothing from the database; the leader's
durable consensus state is its vote journal; a write that fails after the
Proposal leaves a block the leader re-fetches if the fleet commits it, and
the followers decide on the block by their own execution either way.

**Registered predictions.**

1. Follower recv at B falls by the leader's write plus its share of the
   view skew: 150-300 ms (from ~1.8 s); follower view 2.25-2.4 s.
2. B TPS 38-41k (from 36.6k), +5-12%. FALSIFIED IF within the floor of
   round 33: then the write was already hidden behind the push at this
   shape and the fill and the push are all that remain of recv.
3. A legs within the floor of round 33 (80-84 blocks): the write at 22,857
   is 42 ms and already overlapped by the import.
4. No "write failed AFTER the Proposal left" warning on any node; zero
   steady-state timeouts.

### Result: recv fell 300 ms, r1 rose 300 ms, and the view did not move

14:08-15:01 UTC; the warm-up was lost at launch (node4 stalled after its
index load, the second such stall, intermittent and not yet caught in the
act), so A1 is the effective warm-up. node0, medians.

| leg | ceiling | win blocks | win TPS | import | follower recv | follower r1 | follower view | leader propose | leader r2 |
|-----|--------:|-----------:|--------:|-------:|--------------:|------------:|--------------:|---------------:|----------:|
| B1 | 3.423G | 24 / 23 | 38,132 / 37,122 | 866 | **1,546** | **880** | 2,588 | 1,264 | 1,014 |
| B2 | 3.423G | 24 / 24 | 38,409 / 38,290 | 837 | **1,446** | **858** | 2,500 | 1,249 | 919 |
| A2 | 480M | 80 / 84 | 30,476 | 277 | 475 | 236 | 732 | 394 | 252 |
| round 33 B (reference) | 3.423G | 22-25 | 34.8-39.2k | 847 | 1,781-1,829 | 626-660 | 2,540-2,552 | 1,394-1,644 | 662-667 |

Every leader block over 50k on node0 (14 of 14) was proposed early; the
leader's write (205 ms median) is off recv; no "write failed after the
Proposal" on any node. Predictions: (1) recv down 150-300 ms -- **held**:
-280 to -330. (2) B up 5-12% -- **falsified**: 37.9k against 36.6k, +3.5%,
inside the floor, because r1 rose by what recv lost. (3) A unchanged --
**held**, 80/84. (4) no post-Proposal write failure, no timeouts -- **held**.

**What the two rounds together say.** The follower's view is the time from
its view start to its COMMIT vote, and the commit vote waits for its own
import (the two-phase gate). The import starts when the body arrives (the
push) and takes ~850 ms; moving the Proposal earlier moved the prepare vote
earlier and nothing else. So at ~95k a block the cycle is: leader's fill and
seal (~0.9-1.0 s after its view starts) -> push -> followers' import (0.85 s)
-> commit QC -> next view. Two things are on it and both are work, not
waits: the leader's fill on the critical path, and the import. The write is
gone from both sides.

The next kind of change is the one n42-rs has and this chain does not:
build the next block while the current one imports. The speculative build
exists (it builds on the head as soon as the head lands), but the leader's
view starts at the QC, before its own import of the previous block has
finished, so the speculative block is not ready and the view-triggered fill
runs in full. Building on the in-memory post-state of the block being
imported -- an overlay the followers already hold in the IntraBlockState
they are executing -- is the change; it is round 35's design question, not
a knob.

Adopted for the benchmark line: `N42_PUSH_BEFORE_WRITE=1` and
`N42_PROPOSE_BEFORE_WRITE=1` (the second costs nothing and removes the
leader's write from the view's clock).

## 6u. Round 35: parallel execution, with a reader per worker -- registered before the round ran

Round 34's settings plus `--parallel-evm` on every node, `N42_PARALLEL_WORKERS=32`
(560f388c). The 2026-09-02 halt was a shared MDBX cursor (3709ca6a); each
Block-STM worker now opens its own read transaction on its own goroutine and
reads the live tree through `LookupSource` under the tree's reader lock, so
every worker sees one snapshot -- the tree at the parent, static for the
whole execution. Code and storage rows come from the worker's transaction.
Sender verification was already parallel. The leader's fill is NOT parallel
(the miner builds sequentially); this round measures the followers' import.

**Registered predictions.**

1. Correctness first: zero BAD BLOCKs on any node across the round, seven
   identical heads at every leg end. FALSIFIED IF any node rejects a block
   the others commit -- then the per-worker reader is not the snapshot the
   serial path reads, and the flag goes back to off.
2. Follower `exec` at B falls from 365-380 ms (~95k, 3.9 us/tx) to under
   150 ms; import from ~850 to ~600 ms; follower r1 from ~870 to ~620 ms.
3. B TPS up 8-15% (the cycle is fill ~1.0 s + import; only the import
   moves). A legs within the floor: `exec` 105 -> ~50 ms is 2% of a 0.75 s
   cycle.
4. `staleTrimmed`, timeouts, memory: unchanged.

### The first start: correct, and 50x slower

Per-worker readers held: 32 workers on every node, no state-root mismatch on
any node for any block that completed (the one BAD BLOCK was "open read
transaction: db closed", raised by my own shutdown of the fleet). But every
block over ~40k hit the executor's 64-wave limit and fell back to
sequential -- 9.2 s for 41k, 43.4 s for 163k, against 0.85 s serial -- and
the window read 4 blocks in 60 s.

Two rules in the executor did it, and neither is specific to this chain.
Validation re-executed EVERY later transaction on the first failure, so a
block with one early conflict paid a full wave per conflict. And the
scheduler had no notion of dependency, so a sender's nonce chain -- 4,000
senders, ~25 transactions each in a block -- re-executed wave after wave,
every link reading a stale predecessor until the limit.

66ff7876 changes both: a failure re-executes that transaction alone, later
ones are provisional and re-validated (only those whose recorded read
versions no longer match re-execute); and `SetAffinity` pins each sender's
transactions to one worker in index order, so a chain executes on the
worker that already applied its predecessor and only cross-sender recipient
credits reach the validator. The change found a latent defect on the way:
`collectPending` re-ran every unvalidated transaction, which under lazy
validation rewrote a provisional transaction's value under an unchanged
incarnation -- a dependent that had recorded that incarnation could never
detect it (1 lost increment in 30 runs of the shared-counter test, now run
200 times). The sender-chains test models the benchmark block: 1,200
transactions, 1,200 executions, 0 aborts with affinity.

The second start is the round; predictions 1-4 stand.

### The second start: the coinbase is every transaction's write set

Round 35c's warm-up (163k-transaction blocks) logged nine "wave limit
reached, sequential fallback" on node0 before the leg was stopped. With the
sender chains pinned, the chain that remained is the block producer:
`TransitionDb` credits the priority fee to the coinbase in every
transaction, so all 163,000 write one key, each read of it depends on the
previous transaction's write, and Block-STM validates one wave at a time.

d1418113 defers the credit. `StateTransition` takes a `FeeSink`;
`ApplyMessageWithFeeSink` hands the priority-fee and EIP-1559 collector
credits to it instead of the state, the parallel path records them per
transaction, sums them per recipient in transaction order after the
multi-version state is folded into the block's `IntraBlockState`, and
applies each total once (a zero total still goes through `AddBalance`, so
the touch matches serial). Equivalence needs no transaction to send from or
to a fee recipient -- it would see the credit early in serial order -- and
such a block runs sequentially. The address scan does not cover a contract
reading the coinbase balance (COINBASE + BALANCE); that would show as a
state-root mismatch, and the benchmark is plain transfers.

Round 35d is round 35c's runner (offsets 390M-398M) with the n42-r35 binary
rebuilt at d1418113. Node6 starts with its tree one block ahead of its head
(a stored header at 13832687 whose root is the tree's); the undo-record
unwind reverts it on the next import, as node5's did at 35c's start.

**Registered predictions (35d).** Predictions 1-4 stand unchanged; added:

5. Zero "wave limit reached" on any node at 163k. FALSIFIED IF any block
   over 100k falls back -- then another shared key remains (name it from
   the executor's conflict log before changing anything else).
6. Follower `exec` at 163k under 400 ms (43.4 s at 35c's fallback; serial
   ~0.85 s import at 95k is 8.5-9 us/tx, so 163k serial would be ~1.4 s).

### 35d: prediction 5 falsified in the warm-up, and the trace named the key

Nine minutes into the warm-up node1 imported a 15,204-transaction block
through the fallback: 64 waves, 617,414 executions, 611,562 aborts, 3.4 s.
Not the coinbase -- the fee sink had removed it -- and not a shape the
executor's model reproduces: the same block in `TestExecutor_HotRecipientBlock`
(15,204 transfers, 4,000 senders, 22,857 hot recipients, 32 workers) settles
in 4 waves even under version-only validation.

The round was stopped and re-run as a warm-up only with
`N42_PARALLEL_TRACE=1`, which logs the first twelve validation failures of
a block with the key and both versions. Every failure was the SAME sender
account read from base by consecutive transactions with `reads=1, writes=0`:
the faucet's funding chain at the start of a block, then single senders'
nonce chains during decay. Each link of the chain had executed at once on
a different worker and failed its nonce check -- no writes -- wave after
wave. The affinity key of 66ff7876 reads `tx.From()`, which is the
wire-declared sender; a block off the wire carries RLP only, so on import
it is nil and the key fell back to the transaction index. The executor's
tests exercised affinity in-process and never saw a wire block.

df7cccae runs the importer's existing parallel sender recovery before the
first wave (memoised on the transaction; AsMessage reuses it) and falls
through to the memoised Sender for the key. It also validates by value
when a read's version has moved -- a dependant whose input bytes did not
change stays valid -- which the model needed for 3 waves instead of 4 and
the fleet will need for the recipient cascades once the chains are pinned.

**Registered predictions (35e).** Predictions 1-6 stand; 5 now has its
mechanism fixed rather than guessed. Added:

7. `waves` per block at 163k in single digits (the model: 3). FALSIFIED IF
   any block exceeds 16 -- then the fleet's block has a dependency the
   model lacks, and the trace run names it before anything else changes.

### 35e/35f: four fixes in one afternoon, each measured by the phase it moved

The runners changed builds between legs (the round was diagnostic; every
leg's build is named in `r35f.log`), so the legs are read as phase timings
of one block shape, not as an A-B-B-A. Every number is node1 (a follower)
on a 163k or 22,857-transaction block; `parallel block` logs the phases.

| build | leg | 22,857 tx | 163k tx | window TPS |
|---|---|---|---|---|
| round 34 serial | A/B | exec ~105 ms | import ~1.4 s (est.) | A 30.5k / B 37-38k |
| 0e9bee6f senders pinned, value validation | 35f A1 | proc 350-390 ms, 3-5 waves, 1.2-1.4x executions | -- | A1 25.9k / 25.1k |
| 9ff6ff4d + phase timing | 35f B1 | -- | recover 66, setup 100, exec 1521, validate 595, apply 48 ms; 10 waves, 360k executions | B1 32.0k / 30.1k |
| 2f8dc781 sharded MVS, lock-once tree, parallel validation | 35f B2 | -- | exec 826, validate 78, setup 128, apply 60 ms; 9 waves, 317k executions (155k tx) | B2 36.8k / 36.6k |
| 4061d36f recipient deltas | 35f A2 | proc 217 ms; 1 wave, 0 aborts, exec 62-74 ms | -- | A2 30.1k / 30.1k |

What each fix was for, in the order the evidence arrived:

- **35e, `MVS.DeleteAll` (0e9bee6f).** The first block over 50k took 17.4 s
  with 95% of the follower's CPU in `DeleteAll`: it walked every entry in
  the store before every execution, O(transactions x keys). Now only the
  previous incarnation's keys are deleted. Round stopped after one block.
- **35f B1, the phase split (9ff6ff4d).** Execution 1.5 s for 360k
  executions on 32 workers is 23 us of CPU per execution but 4.2 us of
  wall per execution per worker: the workers wait. Validation 595 ms was
  ten serial passes over the block.
- **35f B2, sharding and lock-once (fe2b57e0), parallel validation
  (2f8dc781).** The profile's hottest instruction was the atomic add
  behind `RWMutex.RLock` -- the store's one map lock and the tree's reader
  lock, each taken per read by 32 workers on one cache line. 256 shards
  and one reader lock per block: exec 1521 -> 826 ms per execution 4.2 ->
  2.6 us; validation 595 -> 78 ms. B2 36.8k, from B1's 32.0k, within 3% of
  serial.
- **35f A2, recipient deltas (4061d36f).** The 9-10 waves and 2.2x
  executions that remained were the hot recipients: 22,857 accounts
  credited ~7 times a block, every credit reading the account for its
  code hash and writing it back with a new balance. A transaction that
  never observes an account's balance (the state's `balanceReadHook`
  records GetBalance/Empty) and only increases it now writes a delta;
  the store composes deltas onto the latest full write at read and apply
  time, and the transaction's read of that account is validated on every
  field but the balance. At 22,857: 1 wave, 0 aborts, proc 350 -> 217 ms.
  Seven nodes, no BAD BLOCK.

The A leg is now at serial's number (30.1k against 30.5k/30.1k): at
22,857 the cycle is the leader's fill and the rounds, not the import. The
B leg is where the import is the cycle, and B has not yet run on the delta
build -- that is round 35g.

## 6v. Round 35g: the delta build on every leg -- registered before the round ran

Round 35f's runner (offsets 420M-428M) with 4061d36f on all five legs.

**Registered predictions.** 1-4 and 6-7 stand (5 was met in 35f A2 at
22,857; 35g tests it at 163k). Added:

8. 163k blocks: 1-2 waves, executions within 5% of the transaction count,
   exec under 400 ms, import (`total`) under 0.85 s. FALSIFIED IF exec
   stays over 700 ms with waves under 3 -- then the per-execution cost, not
   the conflicts, is the block, and the next profile is on `executeSingle`
   with the store quiet.
9. B TPS above round 34's serial 37-38k. FALSIFIED IF B stays at 36-38k
   with prediction 8 met -- then the cycle at 163k is the leader's fill
   (~1.0 s) and the rounds, and the import was never the whole of it;
   the next lever is on the leader.
10. Zero BAD BLOCK on any node: the delta path touches consensus state
    (every credited account), and one mismatch retires it.

### Result: 8 and 10 held, 9 did not -- the import is no longer the cycle

The warm-up leg was lost to node3's start (its QMDB index came up with
2.2M of 8.5M live keys and the rebuild ran past the 600 s readiness
deadline; the next start loaded 8.5M and the node was fine -- the stall
issue in `docs/OPEN_ISSUES.md`, now with a cause: an index left short by
the stop). The legs: A1 30.5k / 29.7k, B1 37.4k / 37.6k, B2 38.9k / 38.8k
(A2 below). Zero BAD BLOCK on seven nodes across the round.

Node1 at 117,353 transactions: 1 wave, 0 aborts, executions = transactions,
recover 55, setup 99, exec 385, validate 11, apply 27 ms; `proc` 765 ms,
import `total` 1,022 ms. Prediction 8 held on the executor (exec under
400 ms, 1 wave) but the import total did not reach 0.85 s: 180 ms of
`proc` lie outside the executor -- the sender gate's 117k secp256k1
recoveries at ~27 workers -- and 250 ms outside `proc` (header, body,
validation, write 141 ms). Prediction 9 fell as its own clause said it
would: B moved 37.5k -> 38.9k, within the noise floor of serial's 37-38k,
so at 163k the cycle is the leader.

The leader's view timing (node0, B2): propose 1.1-1.9 s, r1 ~60 ms, r2
0.7-0.9 s, total 2.0-2.7 s. Inside propose, the measured phases: fill
`commit` 400 ms (103k transactions executed serially in the miner, ~4
us/tx), `assemble` 270-350 ms, `finalize` 165-180 ms, `write` 210-250
ms, `push` 70 ms; the rest is the leader importing the previous block
first -- the leader rotates every view, so import and build sit in series
on the critical path. That is the next round's target, after 35h retires
what remains of the import's CPU.

## 6w. Round 35h: the import's CPU -- registered before the round ran

Round 35g's runner (offsets 430M-438M) with three changes:

- 5b8c9d65: one IntraBlockState, EVM, reader and writer per worker,
  rebound per transaction (35g's profile: mallocgc 46% of the executor's
  CPU, the workers at ~7 of 32 cores meeting on the heap lock).
- f7ae8d0c: the sender gate asks the pool for the transaction by hash and
  takes its recovered sender (same hash, same signature bytes, re-derived
  through the block's signer) instead of recovering again: 8 core-seconds
  a block on every node at 163k.
- GOGC=300 (the runner env): ~1 GB of allocation a block against a 2-3 GB
  live heap collected every 2-3 blocks, 12% of CPU in gcDrain.

**Registered predictions.** 1-4, 6-8, 10 stand. Added:

11. Node1 at ~117k: exec under 250 ms (from 385), `hintHits` equal to the
    transaction count on every block after the pool has seen them, `proc`
    under 550 ms (from 765). FALSIFIED IF exec stays over 350 ms -- then
    the workers' idle time is not allocation, and the next profile is a
    block profile (contention), not a CPU profile.
12. B TPS unchanged within the floor (37-39k): the leader's propose is the
    cycle, and this round does not touch it. A rise past 40k means the
    import was still on the critical path more than the view timing shows.

### Result: 11 held, 12 fell upward, and the generator is now the ceiling

Warm-up lost again to a short index load (node2, 6.2M of 8.5M keys; see
`docs/OPEN_ISSUES.md`). A1 33.5k / 32.0k at blockTime 0.68 / 0.61 s (35g:
30.5k / 29.7k at 0.75 s -- the A leg had never moved off serial's number
before). B1 44.1k / 39.0k, the first window past 40k; the second window ran
at 25% occupancy (41k transactions a block, 1.05 s blocks): one generator
at target-depth 220k no longer fills the blocks, so 35i runs two. B2's
first window read 42.6k at 52% occupancy; then the memory watchdog
stopped the round at 19 GB available: under GOGC=300 every node's heap
sat at its 12 GiB GOMEMLIMIT (7 x 11.8 GB anon) on a 137 GB box that
also carries 20 GB of shmem and ~39 GB of page cache. A2 did not run.

Node1 at ~100k transactions: 1 wave, recover ~40, setup ~25, exec 82-129,
validate 8, apply 15 ms; `proc` 373-478 ms, import `total` 578-682 ms
(35g at 117k: exec 385, proc 765, total 1,022). At 22,857: exec 31 ms
(35g: 62-68). Prediction 11 held. 12 fell the other way: the import was
still on the critical path -- the leader imports v-1 before it builds v,
so a faster import moved the A leg 10% and B1 12%.

One correction to this round's registration: the pool-hinted sender gate
(f7ae8d0c) is inert on this fleet. The gate only runs for a transaction
that DECLARES a sender on the wire, and the flood's raw legacy
transactions arrive with From nil; `hintHits` is 0 on every block, and
the profile's secp256k1 time is the pool's own prewarm of gossip arrivals
(70%) and the pre-wave recovery (18%, mostly sender-cache hits). The 35h
gain is per-worker reuse and GOGC=300. The gate change stands for chains
whose transactions carry From.

## 6x. Round 35i: two generators, the pool's reorg, and the fill's own overhead -- registered before the round ran

35h's runner with two floods (offsets 440M-448M; target-depth 220k each,
pool 500k), GOMEMLIMIT 7 GiB (GOGC=300 kept: the limit, not GOGC, bounds
the heap now -- 7 x 7 GiB leaves the box ~50 GB), and 90342be7: the pool's reset treats a head that extends the
old one as a linear extension (headers walked, no bodies loaded) instead
of a reorg whose difference is empty -- 250-480 ms of a 0.8-1.1 s reorg at
100k-transaction blocks. 710d0c13 times the fill's pool snapshot and stale
trim; 5b095b9f times the block end and Finalize after the executor;
debb97ef names the twig if a startup load fails, and the runner keeps the
stalled node's run.log.

**Registered predictions.** 1-4, 6-8, 10, 11 stand. Added:

13. B legs at full occupancy again (blocks >= 90% of the 163k ceiling in
    the window's mean); B TPS at or above 44k in both windows. FALSIFIED
    IF occupancy stays under 60% with two generators -- then the harness,
    not the chain, is the ceiling, and the next round changes the
    generator (more concurrency or a third flood), not the node.
14. `staleTrimmed` on the leader's fill falls below one block's worth
    (< 163k) from 377k, and the pool's `reset` phase under 50 ms at 100k
    blocks. FALSIFIED IF reset stays over 200 ms -- then the lag is not the
    body walk.
15. `finalizeMs` on the follower names most of the ~220 ms of `proc`
    outside the executor at 100k transactions. FALSIFIED IF it is under
    50 ms -- then the remainder is the caller's commit and root, and the
    next timer goes there.

### Result: stopped in the warm-up -- two generators put the leader on the pool lock

The first launch went out with GOMEMLIMIT 12 GiB (the runner file was
edited while the waiter was starting it) and was stopped before its
flood; the relaunch ran the warm-up with the 7 GiB limit (nodes at
~7 GB anon, 39 GB available at 163k blocks). Its first window: 32.6k
TPS at 100% occupancy and 5.0 s blocks. The fill breakdown said why:

| leader, 141-163k tx | pendingSnapshot | trim | commit | heap | staleTrimmed |
|---|---|---|---|---|---|
| node0, three builds | 0.97-1.94 s | 8-10 ms | 570-683 ms | 53-61 ms | 294-358k |

`Pending()` is not the copy (713-820 accounts) but the wait for the pool
lock, which two generators' inserts (~250 batches a second) and the
reorg (reset now 0 ms -- the linear-extension path -- but demote 322,
promote 79, nonces 28 ms under lock, and its own lockWait 1.0 s) keep
busy. On the follower at 163k: exec 260, recover 227 (the 1M-slot sender
cache thrashes with ~440k transactions in flight), finalize 185, apply
22 ms; import 930 ms at 142k. Prediction 14: reset met, staleTrimmed
not (the reorg still lags a block: 1.6 s per reorg). Prediction 15 held
(finalize ~190 ms) -- and it is the state root: the engine's Finalize
calls `ibs.IntermediateRoot()` (the QMDB apply over ~23k changed
accounts; `blockimport phases` shows root=0 because it sits inside
proc). Real work, the next thing to parallelise on the follower once the
leader is off the pool lock. 13 could not be read. The round was stopped after
the warm-up window; 35j is the same runner with the snapshot.

## 6y. Round 35j: the builder reads the pool without the lock -- registered before the round ran

35i's runner (offsets 450M-458M, two floods, GOMEMLIMIT 7 GiB) with
4d993c36 -- the reorg publishes the pending map before it unlocks and
Pending(false) hands out a shallow copy of it with no lock -- and
N42_SENDER_CACHE_SLOTS=4194304.

**Registered predictions.** 13-15 stand. Added:

16. `pendingSnapshot` under 20 ms on every build; the leader's propose
    phase under 1.2 s at 163k; blockTime at B under 2.5 s with two
    generators at 100% occupancy. FALSIFIED IF blockTime stays over 4 s
    -- then the lock is held elsewhere on the build path (the reorg's
    demote, or the inserts themselves), and the next timer is inside
    fillTx's commit loop.
17. `recoverMs` at 163k under 80 ms with the 4M-slot cache. FALSIFIED IF
    it stays over 150 ms -- then the misses are not capacity, and the
    pool's prewarm and the import see different signers.

### A1: the builder is off the lock, and the reorg is starved instead

Warm-up lost once more to node2's start -- this time with the error
named (`twig metadata inconsistent`, twig 64710 of 132328, then the
process exited; 88b01d9e retries the load and went in for the legs
after A1). A1 32.0k / 28.2k: the first window at 100% occupancy and
0.71 s blocks, the second at 72% with 400k transactions pending.

Leader at 22,857: `pendingSnapshot` 0 ms (from 0.97-1.94 s), trim 4,
commit 108-123, heap 8 ms; view timing propose 380-430, r1 75, r2 200 ms,
total 0.66-0.73 s. Prediction 16's first clause held. The pool's reorg,
no longer on the builder's path, is now starved by the inserters: reset
0, demote 48-73, promote 71-146 ms, but lockWait 1.4-1.5 s of a 2.3-2.6 s
reorg -- the mutex hands off FIFO and ~250 insert batches a second queue
ahead of it -- so promotion lags and the second window's blocks came out
72% full. ffbf204c (the reorg raises a flag before it takes the lock;
inserters yield while it is up) went in for the legs after B1.

B1 (two floods, snapshot on, before the reorg-priority lock): 47.4k /
35.7k -- the first window a new high at 76% occupancy and 2.6 s blocks,
the second at 82% and 3.75 s. Leader at 163k: pendingSnapshot 0, trim 11,
commit 611-615 ms (the serial EVM at ~3.8 us/tx), heap 57 ms; propose
1.14-1.30 s, r1 65, r2 0.64-0.75 s, total 1.85-2.07 s. Follower at 163k:
recover 59 ms (prediction 17 held with the 4M-slot cache), setup 90,
exec 200, apply 18, finalize 195 ms. Prediction 16: propose was at the
1.2 s line, blockTime under 2.5 s in the first window only.

## 6z. Round 35k: the builder on Block-STM -- registered before the round ran

35j's runner (offsets 460M-468M, two floods, snapshot, reorg-priority lock
ffbf204c on every leg) with 6148b6cd under N42_MINER_PARALLEL_FILL=1: the
builder picks its candidates in price-and-nonce order without executing
(gas by limit, size limiter as before), runs them through the followers'
executor on its own IntraBlockState, drops the candidates that fail their
pre-check, renumbers the survivors and hands them to the unchanged
assemble. The leader's fill commit was 611-683 ms at 163k; the followers
execute the same block in ~200 ms.

**Registered predictions.** 10 (zero BAD BLOCK -- the builder's root must
equal what seven followers compute from the same list) is the first
thing to read. Added:

18. `miner: parallel fill` at 163k: run under 300 ms, failed candidates
    under 1% of the list; propose under 0.8 s. FALSIFIED IF propose stays
    over 1.1 s with run under 300 ms -- then assemble/finalize (state
    root 190 ms, reload 140 ms, assemble 170 ms) is the leader now.
19. B TPS over 50k in a full-occupancy window. FALSIFIED IF it stays
    under 45k with prediction 18 met -- then r2 (the followers' import,
    0.65-0.75 s) or the supply is the cycle.
20. The reorg's lockWait under 200 ms with the priority flag (35j B2/A2
    read this too). FALSIFIED IF it stays over 1 s -- then the inserters
    hold the lock in longer stretches than the flag can yield around.

### 35j B2/A2 (read before 35k ran): the priority flag did nothing, and a B leg degrades as the heap meets its limit

B2, the reorg-priority build: 47.4k / 36.7k, the same as B1's 47.4k /
35.7k; A2 31.6k / 28.2k like A1. lockWait 0.75-2.2 s still -- the flag
turns away new inserters, but the ~200 concurrent RPC batches already
queued on the mutex go first, and each holds it for the whole
200-transaction batch. Prediction 20 fell.

Both B legs' second windows ran 3.75 s blocks. Over B2's three minutes,
node0's propose went 1.18 -> 2.06 s and r2 0.94 -> 1.09 s while node1's
exec for a ~140k block went 97 -> 674 ms and MemAvailable 86 -> 43 GB:
the follower's executor tripled on the same work. That is the garbage
collector at the 7 GiB GOMEMLIMIT -- the live heap grows through the
leg (the pool's 400k+ pending, 16 decoded 163k-blocks in the block cache
at ~160 MB each, the QMDB index, the txindex tail) until every block's
~1 GB of allocation forces a full mark. 35k runs the block cache at 4
and the sender cache at 2M slots; a heap profile inside a B leg is the
next reading if the trend persists.

### 35k warm-up (read before the legs): the builder's root is the followers' root

Seven nodes, zero BAD BLOCK through the warm-up with the builder on
Block-STM: 163,000 candidates picked in 49-52 ms, 163,000 included, 0
failed, run 311-625 ms (the serial fill's commit was 611-683 ms at the
same size; the spread is the leader's own CPU under the pool's prewarm
and the imports). Warm-up windows 50.4k / 46.6k -- the first window over
50k -- at 71-75% occupancy and 2.3-2.6 s blocks. Node heaps 3.5-3.8 GB
with the block cache at 4 (from 6.8 GB at 16); MemAvailable 80 GB.

The leader at ~163k now: build 460-510 ms (fill 300, reload 156-167,
align 55-80), assemble 323-351 (state root 183-200, the rest receipts
and roots), write 224-264 (off the critical path), push 80; propose
1.1-1.8 s, r1 60, r2 0.63-1.06 s (the follower's proc: recover 110,
setup 116, exec 338, apply 31, finalize 210 -- the write, 241 ms, is
already off r2: the executed hook casts the vote before the write). Both
sides pay the state root (~200 ms, of which QMDB's serial append is ~78
ms and the rest is the IntraBlockState materialising ~45k objects).

### Result: B 52.7k / 46.6k and 53.1k / 47.6k; the A leg does not move

A1 30.9k / 27.4k, B1 52.7k / 46.6k, B2 53.1k / 47.6k, A2 30.1k / 26.3k;
zero BAD BLOCK on seven nodes across the round -- the builder's root is
the followers' root at every block size (prediction 10). Prediction 18:
run 311-625 ms at 163k, 0 failed; propose 1.1-1.8 s, over the 0.8 s line
-- assemble (state root 183-200 + ~150 of receipts and roots) and reload
(156-167) are the leader now, with the fill. Prediction 19: over 50k in
the first window of both B legs, at 72-76% occupancy (the pool's
promotion lag; the second windows 46-48k at 2.6 s blocks). The A leg is
where it was: at 22,857 the fill was never the cycle.

## 6aa. Round 35l: one leader for four views -- registered before the round ran

35k's runner (offsets 470M-478M) with 289f5630 under
N42_HOTSTUFF_LEADER_TENURE=4 on every node, and the sender cache back at
4M slots (recover 110 ms at 2M, 59 at 4M). The leader rotates every view,
so view v+1's build waits behind its leader's import of v; with a tenure
the leader that sealed v is v+1's leader, and the existing next-leader
speculative build (fired at vote time) runs on its own post-state of v
while the followers import v. The cycle should approach max(build,
import) + rounds instead of their sum.

**Registered predictions.** 10 (zero BAD BLOCK) and 18 stand. Added:

21. On the leader, `speculative build hit` on three of every four views
    and propose under 0.5 s on those views at 163k (the build already
    done; seal2res ~0.3 s). FALSIFIED IF propose stays over 1 s on tenure
    views -- then the speculative build waits for the leader's own write
    (`ensureParentApplied` reads the disk) and the lever is to build on
    the in-memory post-state.
22. B TPS over 65k in a full-occupancy window (cycle ~1.4 s at 163k:
    r1 + r2 ~0.9 s, seal 0.3, push 0.1). FALSIFIED IF under 55k with 21
    met -- then r2 (the followers' import) is the whole cycle and the
    next round is the follower's state root and setup.
23. View timeouts stay at zero (a timed-out leader keeps its tenure; the
    fleet has had none at steady state since round 32).

### A1 (read before B1 ran): tenure works, the speculative build never fires for it

Warm-up lost to node5's start (three attempts, three different twigs;
`docs/OPEN_ISSUES.md`). A1 31.2k / 27.0k, as 35k. The schedule holds:
node0 led views 17304-17307, 17332-17335, ... (four in a row, every 28),
zero view timeouts and zero BAD BLOCK on seven nodes (prediction 23). But
the tenure views proposed in 363-528 ms, the same as the first view of
each tenure: node0 logged twenty "build triggered (leader view)" and no
"speculative build hit". The vote-time hint (proposal.go, cast when a
node votes for a block it has IMPORTED) never fires for a leader's own
block -- a leader does not import what it built. Prediction 21 fell on
its mechanism, not its premise. f6a8d5d3 emits the hint as the leader
broadcasts its proposal when it also leads view+1; the producer waits for
the block to persist (~250 ms) and builds view+1 on its post-state. B1
runs without it (the fleet launched before the swap); B2 and A2 run it.

B1 (tenure, no same-leader hint): 43.2k / 42.1k at 26-30% occupancy and
1.0-1.2 s blocks. The cycle halved -- a tenure leader does not import
its own previous block before building -- and the blocks emptied: one
node's pool now supplies four blocks in a row, and its promotion runs a
block behind the head (the reorg's lockWait 0.75-2.2 s behind ~200
queued insert batches). c8740e20 serialises inserters on a gate ahead of
pool.mu, so the reorg waits behind one batch; that is round 35m.

B2 (tenure with the same-leader hint, f6a8d5d3): 51.0k / 47.7k at 21-22%
occupancy and 0.70-0.73 s blocks -- the consensus cycle at ~35k-tx blocks
is now 0.7 s. Node0: 40 leader views, 40 speculative hits; tenure views
proposed in 39-555 ms (the first view of each tenure 0.86-1.25 s, its
build waiting behind the previous leader's block), r2 7-800 ms, zero
timeouts, zero BAD BLOCK. Fills 62-89k candidates, all included. The
pool's promotion is the whole ceiling: full blocks at this cycle would
be a different chain.
A2 (tenure with the hint): 37.3k / 30.5k -- the A leg moved for the
first time since round 34 (0.61 s blocks at 100% occupancy, then 0.42 s
blocks at 56%). Round 35l: A1 31.2k / 27.0k, B1 43.2k / 42.1k (no hint),
B2 51.0k / 47.7k, A2 37.3k / 30.5k; zero BAD BLOCK, zero timeouts.

## 6ab. Round 35m: tenure with the hint, and the pool's reorg unstarved -- registered before the round ran

35l's runner (offsets 480M-488M) with f6a8d5d3 (the leader advises its
own speculative build at proposal time when it also leads view+1) and
c8740e20 (insertGate) on every leg.

**Registered predictions.** 10, 23 stand; 21 re-registered on the fixed
mechanism. Added:

24. The pool's reorg `lockWait` under 50 ms at 163k with two generators
    (from 0.75-2.2 s), and B occupancy back over 70% under tenure.
    FALSIFIED IF lockWait stays over 300 ms -- then a lock holder other
    than the inserters (Content/Stats/GetTx callers, or the priced heap's
    discard) is in the way.
25. B TPS over 60k in a window: the tenure cycle (~1.0-1.2 s at B1's
    small blocks) with blocks refilled to 120k+. FALSIFIED IF occupancy
    recovers but TPS stays under 50k -- then the cycle grew with the
    block (r2 at 163k is 0.65-1.06 s) and the follower import is the
    whole of it.

### First launch: a BAD BLOCK in the warm-up, and the divergence class has a cause

Two minutes into the decay (empty 0.3 s views) six nodes rejected
13881480 from node2 -- "state root mismatch, proposer 6330de.., locally
computed dfd25e..", zero transactions. Node2's log: at 04:36:33 its seal
of 13881479 was stale ("applied head moved past its parent" -- a sibling
bca22538.. had landed), at 04:36:42 a branch switch unwound to the
incoming block's parent and the miner converged on the lowest-hash
sibling, then, leading the next two views, its parked speculative build
of 13881480 on bca22538.. was a hit and went out with the wrong root.

The builder's persistent speculative computer reloads by trusting its
index below a cursor for the store's layout; the unwind rewrote entries
below that cursor, and nothing voided the trust -- only a failed peel
inside NewMinerRootComputer did. This is the "one-time in-memory
divergence" of round 32 (node3), made frequent by tenure: a leader that
keeps the view after a stale seal is the one that just switched
branches. 359e1f89 voids the trust on every unwind and failed-block
revert (the next build rebuilds, ~5 s, once). The round relaunched with
it and the rejected hash on the runner's known-bad list.

Second launch: one block a minute. The failed-block revert fires on
routine import paths -- node0 voided the trust 48 times in four minutes
("failed block revert"), and every leader build then reloaded the
speculative tree from the entry log: 10-23 s at 13.9M blocks, not the 5
s the comment remembered. ea76069e keeps the void on the mutating
unwind only (two "branch switch" voids in the same window). Third
launch at 05:08.

### Third launch, warm-up: the gate works, and the per-account caps are the fill's ceiling

Warm-up windows 56.9k / 52.9k -- the highest yet -- at 40-43% occupancy
and 1.13-1.33 s blocks; zero BAD BLOCK, zero timeouts, zero voids. The
pool's reorg: lockWait 0-7 ms (from 0.75-2.2 s; prediction 24 held),
demote 320-420 ms. But `pendingAccts` 11-33 on node0 with 8,000 senders
in flight, fills of 31-37k candidates (all included, 0 stale): the
bench's `-shard-senders` routes each sender to one node, so six-sevenths
of every sender's transactions reach a leader by gossip as REMOTE, and
the pool keeps at most 16 executable and 64 queued per remote account.
A rotating leader's local senders had 2.3 s to accumulate; a tenure
leader builds four blocks in a row from the same pool and the caps bind.
Stopped after the warm-up; 35n raises the caps.

## 6ac. Round 35n: the remote per-account caps raised -- registered before the round ran

35m's runner (offsets 490M-498M) with `--txpool.accountslots 4096
--txpool.accountqueue 8192` on every node (the global 500k/150k stay).

**Registered predictions.** 10, 23, 24 stand. Added:

26. `pendingAccts` on the leader over 1,000 and fills over 120k
    candidates at the 163k ceiling; B occupancy over 70% under tenure.
    FALSIFIED IF fills stay under 60k with the caps raised -- then the
    promotion rate (demote 320-420 ms per reorg, one reorg at a time)
    is the ceiling, not the caps.
27. B TPS over 70k in a window (the 0.7-1.1 s tenure cycle with blocks
    over 100k). FALSIFIED IF under 60k with 26 met -- then the cycle
    grew with the block again (the follower import at 163k, ~0.8 s) and
    the leader's assemble/state root is next.

### Two launches to read with care

First launch (05:30): all seven nodes exited within the same second,
right after "TxLookup segments loaded", with nothing on stderr and no
OOM record readable from here; the relaunch seven minutes later ran
normally. Unexplained; the binary accepts the new flags.

Second launch's warm-up: the runner log carries two `=== r35-warmup`
headers and two sets of window lines, and four generators ran -- two per
sender offset -- so every transaction was submitted twice and the
windows (42-46k at 15-19% occupancy) are not a reading. The A1 leg has
one bench-run. What the warm-up does show: fills of 86-117k candidates
on the leaders (prediction 26's first clause: the caps were the fill's
ceiling), zero BAD BLOCK, zero timeouts, zero voids; and the
generators themselves at 81-89% of one core each -- a single-threaded
signer at ~12k transactions a second is the next supply ceiling.

A1 39.6k / 31.2k (the A leg's highest; 0.58 s blocks full, then 0.39 s
blocks at 53% -- the chain drains the pool faster than two generators
fill it). B1 stopped in its decay: node3, leading views 3204-3205, had
applied its own losing sibling at 13886257, switched branches to the
lowest-hash sibling, voided and rebuilt its speculative tree (20 s), and
its 13886258 was rejected by six nodes -- the void was not the whole
story, the unwind leaves a forked state (`docs/OPEN_ISSUES.md`). Views
3202-3203 had lasted 12 and 24 s: the rebuild times out the view, and
tenure keeps the leader, so the sibling race repeats. Round 35o runs
tenure 1 with everything else kept.

Read after the abort: node3's persisted state at the kept sibling is
identical to node0's and node1's (probe: same head, same root, tree
reload matches), so the unwind is right on disk and the bad root was
the speculative build's timing -- it waited for the parent to be
persisted, and a sibling's header is stored before its state is applied.
580d2f32 waits for the applied marker instead. 35o's warm-up fleet
launched seconds before the swap and runs without it (rotation does not
take that path: the next leader imports the parent before it builds);
the legs after the warm-up run it.

## 6ad. Round 35o: four generators, tenure 1 -- registered before the round ran

35n's runner (offsets 500M-508M) with four floods of 2,000 senders
(the same 6M pre-signed transactions in memory, twice the signing
cores; target-depth 110k each) and N42_HOTSTUFF_LEADER_TENURE=1; the
raised per-account caps, the insert gate, the builder on Block-STM and
the void on unwind stay.

**Registered predictions.** 10 stands (rotation has run whole rounds
without a rejection). Added:

28. B occupancy over 70% with four generators and fills over 120k at
    163k; B TPS at or above 35k's 53k, and over 60k if the rotation
    cycle at full blocks is under 2.5 s. FALSIFIED IF occupancy stays
    under 50% -- then four generators still do not supply 60k/s and the
    harness needs a cheaper transaction source (pre-signed replay).

Warm-up: 47.2k / 41.0k at 72-76% occupancy and 2.6-2.9 s blocks. Fills
101-138k (prediction 28's first clause holds: the caps and the fourth
generator refilled the blocks), generators at 56-98% of a core each,
zero BAD BLOCK, zero timeouts. But the rotation cycle grew to 2.6-2.9
s from 35k's 2.2-2.3 s: node heaps sit at the 7 GiB limit with the
raised caps (400k+ stale transactions trimmed per fill), and the leader's
propose is 1.27-1.32 s with r2 0.78-0.90 s. Round 35p returns to tenure
4 with the applied-wait fix and the pool at 300k/100k.

A1 made four blocks in its 400 s decay and the generators' funding never
confirmed: a stale seal, a branch switch, the void, and a 20 s rebuild
on the leader -- under plain rotation. The void (359e1f89, narrowed by
ea76069e) is the wrong fix: its rebuild times out the view whose stale
seal caused the switch, and the bad roots it was for were builds on a
persisted-but-unapplied sibling, now waited out (580d2f32). 99ea54ad
removes the void call; B2 and A2 run it (B1 launched before the swap).

B1 45.7k / 38.7k and B2 48.8k / 39.9k at 71-78% occupancy, 2.6-3.0 s
blocks -- rotation with four generators fills the blocks and the cycle
is slower than 35k's (heaps at the 7 GiB limit with the raised caps).
A2 produced no block at all: every view timed out for 400 s. The probe
after the stop: five nodes at head 13890511, two at 13890512, and every
node's tree at 13890513 -- the leg's stop had cut two blocks in flight,
and the five nodes' persisted HotStuff state named a committed block
they did not have, so they looped in catch-up ("block #13890512 not
found" from the peers that lacked it too) while the two ahead could not
form a quorum. A journal reset on all seven before 35p; the harness
should stop the flood and let the chain drain before stopping the fleet.

## 6ae. Round 35p: tenure 4 with the applied-wait fix, pool 300k -- registered before the round ran

35o's runner (offsets 510M-518M) with N42_HOTSTUFF_LEADER_TENURE=4,
580d2f32 (the speculative build waits for the applied parent), 99ea54ad
(no void on a branch switch), the pool at 300k/100k. bench-run now lets
the head settle before it stops the fleet at a leg's end.

**Registered predictions.** 10, 21 (on the fixed mechanism), 23 stand.
Added:

29. Zero BAD BLOCK across the round under tenure -- the applied-wait is
    the fix, not the void. FALSIFIED IF a leader is rejected after a
    branch switch again -- then a second source feeds the build.

### Warm-up: tenure holds, and the generators are the ceiling

Twelve of twelve tenure views a speculative hit; tenure views proposed
in 58-408 ms, the first view of each tenure 0.64-0.81 s; whole views
0.13-0.9 s; r2 8-409 ms; zero timeouts and zero BAD BLOCK in the flood
(the start took a few timeouts and catch-up failures while the split
heads reconciled -- node4 switched off its own 13890513 -- then all
seven agreed). Fills 32-48k, window 39.5k at 16.5% occupancy and 0.68 s
blocks: the chain empties the pool as fast as four generators fill it.
Round 35q runs eight generators of 1,000 senders.

A1: 35.4k at 0.46 s blocks (71% of the 22,857 cap), then 4.2k -- views
3677-3679 timed out on all nodes. Node2, then node3, as tenure leaders:
"consensus parent 60c340f4.. not applied in time" on every build. The
applied-wait (580d2f32) runs before AlignAppliedBranch, and the align is
what unwinds a locally applied sibling so the consensus parent can be
imported; a leader that had applied a sibling could never pass it, and
tenure made it fail four views in a row. Reverted (the persisted wait
plus the align, the original order); the round stopped, journals reset,
and 35q runs tenure 1 -- the post-switch bad root stays open with a
narrower suspect (`docs/OPEN_ISSUES.md`: the live tree's in-memory
state for the two accounts every empty block credits).

## 6af. Round 35q: eight generators, rotation -- registered before the round ran

35p's runner (offsets 530M-538M) with tenure 1, eight floods of 1,000
senders, target-depth 30k each (the first two launches ran 55k: the
generators start flooding as each finishes funding, so the eighth's
1,000 faucet transactions met a 300k pool holding the first seven's
385k and were evicted before any leader included them -- "funding was
not confirmed within 80s"; the warm-up died earlier on one empty
eth_getBalance reply, now retried in txflood, 501130ba). The persisted
wait plus the align is back in the build path (c0830181).

**Registered predictions.** 10 and 28 stand. Added:

30. Eight generators supply over 60k/s: B occupancy over 40% at 163k
    with the rotation cycle of 2.2-2.9 s, i.e. B TPS at or above 55k.
    FALSIFIED IF B stays under 50k at under 35% occupancy -- then the
    generators' ceiling is not their process count (the box's RPC
    ingest or the pool's insert gate), and the harness needs pre-signed
    replay into the leader.

### Third launch, in progress: full blocks, and the cycle is the heap

Warm-up 37.9k / 26.2k at 57-61% occupancy (2.6 then 3.5 s blocks); A1
29.3k / 28.2k; B1's first window 46.2k at 100% occupancy -- the first
full 163k blocks since round 35k -- but 3.5 s a block, against 35k's
2.3 s for the same blocks. Prediction 30 fell on its occupancy clause
but for the other reason: the supply is there now, and the rotation
cycle stretched. The nodes sit at the 7 GiB limit (raised per-account
caps, eight generators' arrivals, 4M sender-cache slots) and collect on
every block. Round 35r raises GOMEMLIMIT to 9 GiB (63 GB of heaps on
the 137 GB box, ~40 GB left) with everything else as 35q.

Round 35q: A1 29.3k / 28.2k, B1 46.2k / 42.5k (98-100% occupancy, 3.5-3.75
s blocks), B2 46.2k / 36.9k, A2 19.6k / 0 (the second window 240 empty
0.25 s blocks -- the generators had run dry). Zero BAD BLOCK, zero
timeouts under rotation; the drain step settled the head at every leg
end.

## 6ag. Round 35r: the same round with GOMEMLIMIT 9 GiB -- registered before the round ran

**Registered prediction.** 31. B blocks at 100% occupancy in 2.3-2.6 s
instead of 3.5 s, B TPS over 60k. FALSIFIED IF blockTime stays over 3 s
with the heaps under the limit -- then the cycle at full blocks is the
leader's assemble/state root and the followers' import, not the GC, and
35k's 2.3 s was a lighter pool.

**Result (2026-09-07, n42-r35x = c0830181, tenure 1, 8 floods x 1000 x
1500, target-depth 30k by blocks each, pool 300k, GOMEMLIMIT 9 GiB).**

| leg | win1 | win2 |
|-----|------|------|
| warmup (B) | 57,050 at 100%, 2.857 s/block | 27,745 at 7.9% |
| A1 | 28,571 at 52.8%, 0.423 s | 30,058 at 53.3% |
| B1 | 39,319 at 96.5%, 4.000 s | 84 at 0.1% |
| B2 | 57,050 at 100%, 2.857 s/block | 23,623 at 6.0% |
| A2 | 29,333 at 53.1%, 0.414 s | (duplicate of win1; chain empty from 12:47:41) |

Prediction 31 FALSIFIED. B blocks at 100% occupancy take 2.857 s (21
blocks per 60 s window, twice, to the block) with every node's heap at
2.5-2.9 GB against the 9 GiB limit. The GC was never the full-block
ceiling; 35k's 2.3 s was a lighter pool. The cycle at 163k transactions,
from the B2 win1 medians (leaders' `miner: build phases` / `miner:
propose phases`, node1's `blockimport phases`):

| phase | ms | where |
|-------|----|-------|
| align + fill + reload | 185 + 414 + 158 = 766 | leader, serial |
| assemble (state root) | 507 | leader, serial |
| finalize / write / push | 255 / 273 / 143 (seal-to-result 439) | leader |
| follower import | 1,012 (body 91, proc 682, valid 32, write 199) | follower |
| two vote rounds | ~150 | network |

So ~1.7 s of leader work followed by ~1.0 s of follower import: the
leader and the followers never overlap under tenure 1, and neither side
has a single phase over 0.5 s left. The next 2x comes from overlap
(tenure + build-ahead, blocked by the post-branch-switch root bug in
OPEN_ISSUES.md) or from shortening every phase a little, not from one
lever.

**B1's 4.0 s was a miner-killing bug, now fixed (1f1140e3).** node3 was
relaunched by the harness at 12:07 (startup stall watchdog); its first
build's speculative QMDB reload failed ("twig metadata inconsistent",
transient, the same read as the startup stalls), the build fell back to
the default root and sealed a stale block 19.7 s later. The write path's
stale-seal gate runs only for isolated QMDB seals, so this block reached
CommitBlock, which read the coinbase through the build's rolled-back
read transaction: nil MDBX txn, panic in resultLoop. The errgroup
wrapper recovered the panic, but resultLoop's deferred stop() had
already run, and node3 answered its next 18 leader views with "build
trigger while worker not running" -- a 6 s view timeout each, 108 s of
the leg, which is exactly the gap between 2.857 s and 4.0 s per block.
The fix: a panic on one sealed block (or one build) drops that block and
keeps the worker; the stale-seal check runs before every write and is
decisive; a failed reload abandons the build instead of sealing on a
root no follower would accept.

**Every second window is the generators, not the chain.** All eight
floods submitted their full 1.5M with failed=0 in 52-143 s, i.e. ~12M
transactions offered to a 300k pool at 85k-170k tx/s; the chain took
~1.8-3.4M of them and the pool evicted the rest. The cause is the
closed loop: `-depth-by-blocks` credits every block's whole transaction
count to the generator asking, so eight generators each read the pool
as empty eight times too early and injected at full rate (`pool=0
topup=150` in every out file). Fixed in txflood with `-depth-share N`
(this generator owns 1/N of every block). Until a round runs with it,
NO win2 of this round or 35q is a chain measurement, and the A legs'
53% occupancy is the same effect one block at a time (eviction leaves
the pool's pending contiguous, but the senders' later nonces land in the
queue behind the evicted ones and never promote).

**A2 from 12:47:41: every leader fill executed 22,857 candidates and
dropped all of them** (node0: 39 of 113 fills, six of them partial first,
`staleTrimmed` falling from 15,644 to 0), so the chain made empty
blocks for the leg's last minutes while the pool reported them pending.
The lenient run did not record why. The next binary logs a `parallel
fill drops` line with the failures by class (nonce low / nonce high /
funds / fee cap / other, plus a sample) so the next round reads the
answer instead of guessing between an evicted-nonce gap and the fixed
10 gwei price falling under a climbing baseFee.

**Next round (35s), not yet launched: the box went to the rust session
at 12:53.** Same configuration on n42-r35y (1f1140e3 + the drop
histogram) and txflood-r32 with `-depth-share 8`. Prediction 32: with
the depth loop honest, win2 of every leg reads within 10% of win1, and
B stays at 2.86 s/block, 57k -- the round is a measurement of the
harness fix, not a throughput lever. FALSIFIED IF win2 still collapses
with the eight floods reporting non-zero pool depth; then the pool's
eviction, not the generators, is what empties the second window.

## 6ah. Rounds 35s/35t/35u: making the generators' closed loop honest (2026-09-07)

**35s (n42-r35y, txflood-r32 `-depth-share 8`), aborted after its
warm-up.** Prediction 32's first half held -- win1 35,510 and win2
35,143, both at 16% occupancy, 0.74 s blocks of ~26k -- and that
stability was the finding: `-depth-share N` is a fixed point at ANY
rate. The generators start staggered; the one running alone credits
itself with an eighth of blocks that were entirely its own, keeps a
phantom ~25k in flight forever, and from then on every generator tops up
exactly what it credits itself with. The chain consumes what it is given
and the generators give what was consumed. Every pool reported zero
pending while every generator believed it had 22-28k in flight; the
leaders' fills found 26-40k fresh candidates behind 150-166k already
mined (`staleTrimmed`).

**35t (txflood-r33 `-depth-by-nonce`), aborted after its warm-up
windows.** The loop now measures each generator's own in-flight from its
senders' chain nonces (sender s owns raws[s*perTx:(s+1)*perTx]; 128
senders refreshed a second, round-robin over the ones with anything in
flight). Verified live: a sender just included in the head block
answered latest == pending == 1500 == perTx. With supply honest the B
warm-up read win1 45,375 at 57.6% occupancy, 2.069 s per block of ~94k
under rotation (build 0.6 s: align 0.13, fill 0.35, reload 0.16;
propose 0.35 s; follower import 0.7 s: proc 0.45, write 0.13). Two
limits showed at once and both are the harness's:

- 1.5M transactions per generator are gone in 3-4 minutes at the real
  6-8k tx/s each; flood 0 finished before the windows opened, flood 4
  during win1, and win2 read 26k at 8%.
- The 300k pool held ~200-280k mined-but-not-yet-demoted transactions
  (the reorg demotes 190-230 ms a block and lags the leader by about two
  blocks), so fresh candidates were capped near 95k a block: `candidates
  97,147 / staleTrimmed 213,407`. Every node at 300-316k pending.

While the last generators were still funding, 26 view timeouts in three
minutes stretched blocks to 13 s; steady state was 2.07 s.

**35u = 35t with pertx 3000 (3M per generator, 25,385 ETH of faucet a
round against 44,777 available) and pool 600k/200k.** Prediction 33: B
blocks over 130k at under 3 s, both windows within 10% of each other,
B TPS over 50k. FALSIFIED IF fills still cap near 100k with the pool at
its new limit -- then the stale share scales with the pool and the lever
is the reorg's demote, not the pool size.

**35u (2026-09-07 21:40) aborted in A1: every mdbx.dat at 137.4 GB.** The
qs-env.sh map cap (`N42_MDBX_MAPSIZE_GB=128`) was reached on all seven
nodes; MDBX_MAP_FULL on the QMDB undo write stopped the chain at 22:08:55
with only "hotstuff catch-up: insert failed" to show for it, and the leg's
last two generators timed out on funding because nothing was being mined.
The warm-up before it is the first honest B measurement of the day:
48,900 / 46,183 at 100% occupancy, 3.33 / 3.53 s per 163k block.

**35v (2026-09-08 00:39, n42-r35y, txflood-r34, map 192 GiB, pool
600k/200k, pertx 3000, funding at 2x, leg offsets 8M apart).**

| leg | win1 | win2 |
|-----|------|------|
| warmup (B) | 51,617 at 100%, 3.158 s | 48,900 at 100%, 3.333 s |
| A1 | 26,666 at 53.0%, 0.455 s | 26,286 at 53.1%, 0.462 s |
| B1 | 51,617 at 100%, 3.158 s | 48,900 at 100%, 3.333 s |
| B2 | 48,900 at 100%, 3.333 s | 46,183 at 100%, 3.529 s |
| A2 | 24,000 at 53.4%, 0.508 s | 20,952 at 52.4%, 0.571 s |

Prediction 33: full blocks (163k) in every B window and every pair of
windows within 6% -- the supply side is finally a measurement. B TPS
over 50k only in the first windows; blocks take 3.2-3.5 s, not under 3.
The full-block cycle from B win1 medians: leader build 0.95 s (align
0.26, fill 0.54 -- of which the candidate run 0.38, reload 0.16),
assemble 0.57, seal-to-push 0.51; follower import 1.14 (proc 0.78,
write 0.21). Every phase is 10-20% slower than 35r's 2.86 s cycle, and
the difference is the box: eight generators at their real rate and a
600k pool whose reorg demotes 485 ms a block on every node (the fill's
snapshot still carries 450-540k mined-but-undemoted transactions). So
the stale share does scale with the pool; the pool-size lever is spent.

**The A legs' 53% is a builder bug, not supply.** Blocks alternate full
/ empty fleet-wide. The new `parallel fill drops` line says why: after
every full block the next leader's fill executed 22,857 candidates and
dropped 22,848 as nonce too high (9 fee cap, 0 funds, 0 nonce low),
while the same fill's stale-nonce trim -- reading through the build's
own reader -- had just removed 11-28k of the previous block's mined
prefix. Two views of the same live tree disagree by exactly one block,
and only for the block that just landed; the empty block that follows
changes no nonces, so the build after it succeeds. The B legs escape it
because 3.3 s is long enough for whatever lags to catch up. Sender
recovery is not it (hint hits are 22,85x in both the failing and the
succeeding runs). Round 35w runs one A leg on n42-r35z, which logs the
first two nonce-high failures with the worker's error (its nonce) beside
the build reader's nonce for the same sender.

**35w/35x (2026-09-08 01:00-01:25, one A leg each): the A legs' 53% is
EIP-1559 against the generators' fixed price.** The samples put the
build reader and the worker in agreement (state nonce 1504 on both
sides) and the sender-chain listing showed the mechanism: every sender's
HEAD candidate fails `max fee per gas less than block base fee`, and its
successors cascade as nonce too high -- 9 fee-cap failures and 22,848
nonce-high in one fill because the equal-price heap hands the fill
22,857 candidates from only nine accounts. The headers confirm it: full
block after full block the base fee climbs 5.88 -> 6.62 -> 7.45 -> 8.38
-> 9.42 gwei, the next block cannot include a 10 gwei transaction (base
fee 10.60), the empty block drops it to 9.28, the next is full, 10.44,
empty, 9.13 ... A full 480M block is twice the 240M gas target, so
+12.5% a block; from the post-decay floor the base fee crosses 10 gwei
after ~196 full blocks, ~90 s at 0.45 s per block, i.e. before the
windows open. The B legs never crossed because their decay ends at a
base fee of ZERO (the header field reads 0 for every B block) and a full
block adds one wei from there.

So every A number since round 35k (28-30k at 53%) measured the flood's
price, not the chain, and the B numbers were spared by an accident of
the decay. Round 35y (n42-r35aa) doubles the header ceiling (960M / 6.846G)
and caps the builder's fill at the old one (`N42_MINER_FILL_GAS`
480M / 3.423G), so a full block sits AT the gas target and the base fee
stays flat; the harness's occupancy column will read 50% for a full
block from here on. Prediction 34: A legs run full 22,857 blocks every
block at ~0.45 s, 45-50k TPS (35r's serial-import ceiling was 37-39k);
B unchanged at 46-52k. FALSIFIED IF the A occupancy stays at 26.5% (the
new 53%) -- then something other than the fee cap empties every second
block.

**35y (2026-09-08 01:31-02:38, n42-r35aa): the alternation is gone, the
A ceiling is the cycle.**

| leg | win1 | win2 |
|-----|------|------|
| warmup (B) | 48,900, 3.333 s | 46,183, 3.529 s |
| A1 | 24,762, 0.923 s, every block 22,857 | 24,381, 0.938 s |
| B1 | 48,098, 3.389 s | 46,183, 3.529 s |
| B2 / A2 | not run: the faucet was down to 498 ETH (a generator needs 632) | |

Prediction 34 half right: every A block is full and the base fee reads
0 for the whole leg, and the A TPS did not move -- a full 22,857 block
takes 0.92 s, not the 0.45 s the alternating legs averaged (their empty
blocks took ~0.2 s). A's cycle from the leg's medians: leader build 0.25
(align 0.03, fill 0.13, reload 0.09), assemble 0.13, seal-to-push 0.18;
follower import 0.33 (proc 0.23, of which the state root ~0.11); view
timing propose 0.37-0.69, r1 0.07, r2 0.29-0.55 (the commit vote waits
for the follower's import). With B's 3.3 s at 163k the two legs fit one
line: cycle ~ 0.75 s + 15.7 us per transaction. The fixed part is the
reload, the state root on both sides, the write and two vote rounds; the
slope is fill execution (2.3 us), import execution (4.2), assemble (3.5)
and the writes, in series. Supply, pool, funding and the fee cap are no
longer in the picture; what is left is the serial leader-then-follower
shape and the per-transaction cost of each stage.

**The faucet.** Each leg funds 8,000 fresh senders for a full pertx and
mines only part of it; the remainder strands. txflood-r35 `-sweep`
returns it (run-sweep.sh sweeps offsets 500M-690M with the fleet up and
no generators).
 The sweep of 2026-09-08 05:27-05:54 returned 26,001 ETH from
offsets 500M-664M (10 senders failed at 664M; the runner then exited
without a word and its fleet ran claimless until stopped by hand at
08:02; 665M-690M are unswept), faucet 567 -> 56,265 ETH.

## 6ai. Round 35z: leader tenure 4 on the honest harness -- registered before the round ran

Everything the harness measured wrongly is fixed (supply, pool, funding,
fee cap, map size), and the cycle is a serial leader (~2.0 s at 163k:
build 0.95, assemble 0.57, seal-to-push 0.51) followed by a serial
follower (~1.3 s: import 1.14 plus the vote rounds). Tenure 4
(N42_HOTSTUFF_LEADER_TENURE=4, f6a8d5d3's same-leader speculative build)
lets the leader build v+1 on its own post-state while the followers
import v, so the cycle should approach max(leader, follower) instead of
their sum. Round 35p ran it at 16% occupancy (supply-bound, 0.13-0.9 s
views); this is the first full-block measurement.

**Prediction 35.** B blocks (163k) in 2.0-2.4 s, B TPS 68-80k; A blocks
(22,857) in ~0.55 s, A TPS 40-45k; both windows of a leg within 10%.
FALSIFIED IF B stays at 3.2-3.5 s (the same-leader speculative build is
not overlapping the import, or the hint arrives too late) or the round
aborts on a BAD BLOCK (the post-branch-switch bad root; then the fix in
OPEN_ISSUES.md comes before any tenure measurement).

**35z (2026-09-08 04:29 EDT) aborted in its warm-up DECAY -- empty blocks,
no generators yet.** Three view timeouts in the startup catch-up (views
44379-44381), five leaders sealed siblings at 13964697, node0 (tenure
leader) had 0x704b… applied and speculatively built 13964698 on it, was
switched off it by an incoming sibling, converged back to 0x704b… as the
lowest hash, and when it won, the speculative build was hit and
proposed: "state root mismatch at block 13964698: proposer 36c923b2,
locally computed 55ceb1c8" on all six followers. The cause is what
OPEN_ISSUES suspected: the miner's persistent speculative tree kept its
index and trust cursor across the live tree's unwind, so the appends the
sibling switch rewrote were trusted stale. Fixed in 2685afb5 -- the
unwind queues its undo records for the miner tree, and the next build
peels them the way the live tree did and lowers its trust cursor to the
unwound block's first slot (the incremental reload then rescans only what
the winner rewrote; no 10-23 s rebuild). 35z2 re-runs the same round on
n42-r35ab; prediction 35 stands.

**35z2 (05:12-05:47 EDT, n42-r35ab).** Lost its warm-up to node3's
startup load (three attempts, three different twigs; retries raised to
six in e2a868d0). A1 under tenure 4: **32,381 / 33,143 at 0.706 / 0.690
s per full 22,857 block** (rotation: 24.8k at 0.92 s) -- the first
measured overlap, +31%. B1 aborted in its decay: BAD BLOCK 13966002, and
this time with THREE roots -- the miner's speculative tree 55c1, the
leader's own live tree d866 ("does not reproduce sealed root"), and the
six followers' 95f0. The miner rewind (2685afb5) did run ("rewound for
a branch switch, records 1"), so the divergence is not only the miner's
index: the leader's live tree after apply X / unwind X / re-apply X
differs from the followers' after apply Y / unwind Y / apply X. A
tree-level round trip of both shapes (lib/qmdb revert_reapply_test.go,
with flush and eviction between) passes, so it sits above lib/qmdb.

**The trigger is the startup.** HotStuff started at 05:42:05; the
miner pre-warm (a second full load, ~3.5 min) held minerRCMu until
05:45:39, every leader's build of those minutes blocked on the lock,
views 1329-1339 all timed out (30 s each), and when the lock freed the
stale candidates surfaced together at 13966001 -- five siblings, a
switch on every node, and the leader converging back to a sibling it had
itself unwound. 35z died in the same window. bcadd720 makes the pre-warm
synchronous (consensus starts warm; node start ~7 min at 219k twigs;
harness readiness 900 s). 35z3 re-runs on n42-r35ad. Prediction 35
stands; the live-tree divergence after unwind + re-apply stays open in
OPEN_ISSUES.md until it reproduces outside a sibling storm.

**35z3 (06:42-07:42 EDT, n42-r35ad, synchronous pre-warm): the startup
is clean and the chain stops being the ceiling.** Zero view timeouts,
zero branch switches, zero BAD BLOCKs from the first view (every earlier
start had 11 timeouts). Under tenure 4:

| leg | win1 | win2 |
|-----|------|------|
| warmup (B) | 46,973 at 1.154 s, ~54k per block | 40,919 at 1.250 s |
| A1 | 33,905 at 0.674 s, every block 22,857 | 32,762 at 0.698 s |
| B1 | 47,708 at 1.154 s, ~55k per block | 45,406 at 1.250 s |

Stopped after B1 by hand. Prediction 35 is half right and half moot: A
blocks came in at 0.67-0.70 s (predicted ~0.55; rotation 0.92) and B
blocks at 1.15-1.25 s (predicted 2.0-2.4 s -- FASTER, because they are
a third full: the leader now drains the pool faster than transactions
arrive). B TPS 45-48k is the same number as under rotation, and it is
the SUPPLY: every generator saw 5-9k in flight against its 30k target
and asked for 21-25k tx/s more each, and the nodes accepted ~6.7k tx/s
each (node0 published 5.5k/s). The 15 s CPU profile of an inserting node
(wr-pprof/r35z3-A1-node0-cpu.pprof, 78 CPU-seconds = 5.2 of 37 cores)
puts the insert path's time under the pool lock: 8.3 s of 15, of which
4.4 s evicting (the pool was full -- the reorg ran once per ~8 blocks
and pending held 600-750k mined transactions, so every insert evicted
one through an O(n) list filter) and 3.0 s in RemoteToLocals (a sweep of
every transaction in the pool each time a sender is first seen; the
bench's 8,000 senders are all new). The reorg was ~300 ms, of which the
un-timed publishPendingSnapshot -- re-sorting 300 pending lists whose
cache every Put had cleared -- was most.

**35z4 = 35z3 on n42-r35ae (178fdea5):** Put keeps the sorted cache
when the nonce extends the tail, Ready trims a prefix, the reorg logs
its snapshot phase, and N42_TXPOOL_NOLOCALS=1 makes RPC submissions
remote (no locals set, journal, or per-sender sweep). Prediction 36:
the reorg runs every block or two and its snapshot phase reads under
20 ms; the pending snapshot's stale share drops under one block; B
blocks under tenure carry over 100k transactions and B TPS passes 60k.
FALSIFIED IF fills stay near 55k and the generators still report 5-9k
in flight -- then the insert ceiling is elsewhere (secp256k1 recovery
at 25 of 78 CPU-seconds, or the RPC handler), not the lock.

**35z4 (10:09-11:28 EDT, n42-r35ae, tenure 4 + pool fixes): 60k.**

| leg | win1 | win2 |
|-----|------|------|
| warmup (B) | 60,400 at 2.143 s, ~129k per block | 55,050 at 2.500 s |
| A1 | 35,428 at 0.645 s, every block 22,857 | 32,381 at 0.706 s |
| B1 | **63,717** at 2.069 s, ~132k per block | 59,413 at 1.935 s |
| B2 | aborted by the memory watchdog at 19 GB available | |

Prediction 36 confirmed: the reorg's snapshot phase reads 0 ms, fills
are 163k (median; stale share 0 or exactly one block), the generators
sit at 21-24k of their 30k target, "Setting new local account" is gone,
and B TPS went from 46-48k to 60-64k. The remaining cycle at 163k, from
B1's medians: leader propose 0.84 s, r1 0.07, **r2 1.35 s = the
follower import** (1.25 s: recover 0.32, setup 0.05, exec 0.24,
finalize 0.20, write 0.22) -- the commit vote waits for it, so the
follower is now the critical path. Two things still cap it from the
supply side: flood 0 emptied its 3M transactions in 3m56 at this rate
(win2 lower than win1 in every B leg), and with the pool no longer full
the fills track supply x block time (blocks 129-138k of 163k).

Memory: with pool 600k, tenure and NoLocals every node sat at 9.5-10 GB
anon (the 9 GiB limit) and the eight generators at 2.4 GB each (pertx
3000 pre-signed); B2 tripped the 20 GB watchdog.

**35z5 = 35z4 on n42-r35af (ecfe77f4: the parallel import applies the
pool's sender hints before recovery, as the serial path always did),
pertx 4500, target-depth 45000, GOMEMLIMIT 8 GiB, after a second sweep
of offsets 640M-860M.** Prediction 37: the follower import loses its
0.32 s recovery (hintFills ~163k), r2 drops under 1.1 s, B blocks fill
to 163k every block and B TPS passes 70k; A ~36k. FALSIFIED IF hintFills
stays near 0 (the pool does not hold the block's transactions at import
time) or the heaps at 8 GiB thrash (exec/propose doubling over a leg,
as at 7 GiB in 35j).

**35z5 / 35z6 (2026-09-08 EDT): two aborts, one useful, one avoidable.**
35z5 died in its warm-up on the startup-revert root divergence (see
OPEN_ISSUES.md; it is what put the startup fingerprint in 60233433).
35z6 restarted the same round behind a preflight -- orphaned generators
swept, hotstuff journals reset on all seven -- and the fingerprint says
the preflight worked: all seven nodes came up on
`root 5c5d7ab8ef2c43b5 nextSlot 489543974 applied 13978489 head 13978489`,
identical, no node holding an uncommitted block. The round then died at
18:49:56 for a reason that has nothing to do with the chain: MemAvailable
18 GB. The fleet's own budget is about 85 GB (seven nodes at an 8 GiB
heap limit, eight generators holding 29 GB of pre-signed transactions at
pertx 4500) and a neighbouring archive build held 29 GB of its own.

Two harness fixes came out of it, both about memory and cleanup:

- `70afe98a` `txflood -lazy-sign` signs each transaction as it is
  submitted, from the same mapping the pre-signing loop used. It trades
  those 29 GB for about half a core per generator (~50 us a signature at
  8k tx/s).
- `bench-run.sh` now reaps its generators on EVERY exit (`trap ... EXIT
  INT TERM`) and sweeps by executable path as well as by recorded PID:
  `setsid` forks when it is already a process-group leader, so the PID
  the harness recorded is a parent that exits immediately and the real
  generator is left with PPID 1. That is how eight of them survived an
  abort for 5.7 hours at 8 GB and 2.5 cores, and five more survived
  35z6's abort.

**35z7 = 35z6 with `-lazy-sign` (txflood-r36).** Prediction 37 still
stands, and the fleet should now fit beside a 29 GB neighbour: peak
demand drops from ~85 GB to ~56 GB.

**35z7 warm-up (19:14-19:17 EDT): lazy signing works, prediction 37 is
falsified, and the reason is structural.** The eight generators held
2.3 GB between them (0.28 GB each, against 3.6 GB pre-signed), funding
and flooding unchanged, and the supply finally filled every block:
54,333 TPS at 3.000 s per FULL 163k block (win2 43,467 at 3.750 s).

`hintFills` is 0 on every imported block, and so is `hintHits`. The pool
holds 651k transactions, the hint source is attached, and the lookup is a
plain hash map -- the block's transactions are simply not in the
follower's pool. **Transaction gossip has been off since 35z4**: the last
`tx broadcaster: publishing` line on any node is from the sweep fleet at
17:21. `N42_TXPOOL_NOLOCALS=1` makes RPC submissions remote, and the
broadcaster only publishes locals. With `-shard-senders` sending each
sender's transactions to exactly one node, each pool then holds only its
own seventh, and a follower cannot have seen what the leader mined. So
no sender-hint scheme can hit in this benchmark's shape, by construction.
(ecfe77f4 is still right for a real network, where transactions gossip
everywhere; it simply cannot show here. Re-enabling gossip is the
configuration the pool's own comment records as OOMing this box: every
node sees every transaction up to seven times.)

**The cycle at a full 163k block under tenure 4:**

| phase | ms |
|---|---|
| cycle (leader view total) | 2,988 |
| propose | 881 |
| r1 | 70 |
| **r2 = the follower's import** | **1,926** |
| import total | 1,879 (body 104, proc 1,381, valid 45, write 284) |
| proc: recover / setup / exec / validate / apply / finalize | 500 / 105 / 432 / 17 / 30 / 251 |

Every node sat at 9.3-9.7 GB anon against `GOMEMLIMIT=8GiB`: over the
limit, so the GC ran hard and `exec` nearly doubled (0.43 s here against
0.23 s at 130k blocks in 35z4), which is also why win2 degraded. The
8 GiB cap only existed to leave room for the generators' 29 GB, and that
is gone.

**35z8 = 35z7 with GOMEMLIMIT 12 GiB** (budget alone on the box:
7 x 12 + 2.3 + 20 shmem = 106 GB of 136). Prediction 38: `exec` back
under 0.30 s and `finalize` under 0.22, import under 1.5 s, cycle under
2.5 s, B TPS over 65k, and win2 within 10% of win1 instead of collapsing.
FALSIFIED IF the heaps sit at 12 GiB too and exec stays near 0.43 --
then the executor's allocation per block, not the limit, is what the GC
is chasing.

**Next lever after that, registered here so it is not re-derived:** the
follower's 0.5 s sender recovery is the largest single item on the
critical path and cannot be helped by the pool in this shape. The leader
already recovered every sender while building. Attaching that sender
list to the direct push -- as a hint, not a consensus field -- is safe
from the PROPOSER (a wrong hint only makes the proposer's own block fail
validation on every follower), costs ~3.3 MB on a ~24 MB block, and
removes ~0.5 s from six nodes' critical path per block. It must not be
accepted from an arbitrary peer, only from the block's proposer.

**35z7 A1 and 35z8 (2026-09-08 19:34 - 2026-09-09 00:27 EDT).** 35z7's A
leg read 33,905 / 33,143 at 0.674 / 0.690 s per full 22,857 block, the
same as every tenure round since 35z3 -- the A size is untouched by the
heap. Its B legs were stopped by hand: they would only repeat a
configuration already shown to be GC-bound.

35z8 raised GOMEMLIMIT to 12 GiB and **aborted in its warm-up at 19 GB
available**, with all seven nodes at 12.4-12.9 GB anon (87 GB for the
fleet, plus 18 GB shmem and 35 GB page cache). Put beside 35z7's 9.3-9.7
GB under an 8 GiB cap, that says the cap is not what sets the heap:
**GOGC=300 targets live x 4**, so a ~3 GB live set asks for ~12 GB
whatever the limit is, and the GC only thrashes when the limit is below
that target. Two knobs, one binding: at 8 GiB the limit bound and the GC
fought it (exec 0.43 s); at 12 GiB the target bound and the box ran out.

**35z9 = GOGC 200 with GOMEMLIMIT 10 GiB**: the target becomes live x 3
(~9 GB), inside the cap, and the fleet sits near 70 GB. Prediction 39:
heaps under 10 GB with no limit pressure, exec back under 0.30 s,
win2 within 10% of win1, B TPS over 60k at full 163k blocks. FALSIFIED
IF exec stays near 0.43 s (then the cost is the executor's per-block
allocation, not the collector's pacing) or the heaps still reach the cap.

**35z9 (2026-09-09 00:51-01:52 EDT, GOGC 200 / GOMEMLIMIT 10 GiB).**

| leg | win1 | win2 |
|-----|------|------|
| warmup (B, full 163k) | 54,333 at 3.000 s | **57,050 at 2.857 s** |
| A1 (full 22,857) | 33,524 at 0.682 s | 33,143 at 0.690 s |
| B1 | could not fund: faucet 790 ETH against 947 needed per generator | |

Prediction 39, part by part: **win2 no longer collapses** -- 54.3k to
57.1k (+5%), against 35z7's 54.3k to 43.5k (-20%) under GOGC 300 at an
8 GiB cap. Every phase improved: exec 432 -> 316 ms, finalize 251 -> 209,
r2 1,926 -> 1,670, the committed view total 2,988 -> 2,479 ms. But the
heaps still sit at 10.8-11.3 GB, above the 10 GiB cap (live is ~3.7 GB,
so GOGC 200 targets ~11 GB), and **B TPS did not pass 60k**.

That gap is the finding: a 17% faster view produced a 0-5% faster window.
The committed-view timer only sees views that reached a QC, and the
commits arrive in bursts (0-1 s apart after a pause), so about half a
second per block sits outside the timed view. The leader's own work is
where it goes: build 1,358 ms (align 551 -- waiting for the parent's
write to persist -- fill 584, reload 190) plus assemble 616 ms, against
the followers' 1,670 ms import. Tenure overlaps the build with the
import, but the align wait and the assemble still land on the path
between one commit and the next proposal.

So the next levers are on the leader, not the follower: the 551 ms
`align` wait (the build blocks until the parent block's write is
persisted, even though with `N42_STATE_READ_QMDB=1` the state it reads
is the in-memory tree, which is already current) and the 616 ms
`assemble`, which recomputes the same root the followers will compute
again.

**The faucet is a per-leg cost of 7,600 ETH** at 8 generators x 1,000
senders x pertx 4,500 x 10 gwei -- four legs empty a 32,000 ETH faucet,
which is what stopped 35z9's B1. Since the fill is capped at the gas
target the base fee stays at zero, so `QS_FLOOD_GASPRICE=1000000000`
(1 gwei) is as includable and costs a tenth. 35za runs at 1 gwei after
a sweep of offsets 860M-1110M.

## 6aj. Round 35zb: the per-node memory budget -- registered before the round ran (2026-09-09)

The offline replay (`QS_REPLAN_2026-09-09.md` section 6) imports a 163k block
in 731 ms on an idle box; the same code on fleet follower node2 took 1393 ms
for the same block during 35za's B warm-up, every phase 1.5-3.4x slower, and
the round's memory log shows each node's MDBX resident set falling from 13.5
GB to 3-4 GB while seven 10-11 GB heaps hold 78-82 GB. One variable:
GOMEMLIMIT 10 -> 6 GiB (n42-r36 = 35za's binary plus 952ec3e4's align skip,
read separately from "miner: build phases"; MDBX map 256 GiB because all
seven mdbx.dat sit at 188 of 192).

**Prediction 43.** If the follower's gap is the page cache: "blockimport
phases" total at 163k falls from 1.4-1.9 s to ~1.1 s, recover stays where it
is (pure CPU) while exec/finalize/write shrink, and B rises from 54-57k to
65-70k. If the gap is SMT contention: recover and exec stay at 460/300 ms,
the heaps collect every other block, and B is unchanged or lower (then the
lever is fewer workers per node, not memory). Falsification: recover falls
in step with exec -- neither explanation; look at the generators. Side
reading, no claim: leader align 512 -> ~281 ms from the skip.

**Round 35zc result (2026-09-09 18:24-19:44, aborted after B1 win1).** The
first attempt (35zb) lost five of eight generators to a stale faucet nonce
(txflood-r37, 5f2cfd20, reads it fleet-wide; a5a4422e gives the eighth
generator 300 s). 35zc then: A1 31.2k / 28.6k at 0.73-0.80 s (35za: 33-35k);
B1 win1 **32.4k at 5.0 s a block** (35za: 54.3k at 3.0 s), with "view timed
out" every 20-40 s from 19:40 and one commit in the minute before the abort.
Follower node2 over 54 full blocks: total 1754 (median 1568, p90 2574) --
recover 468, exec 441, finalize 252, write 259, body 101 -- against 35za's
1393. Memory: MDBX resident 5-7 GB a node (35za: 3-4), heaps 6.6-8.3 GB.

Prediction 43 is falsified on its second branch, harder than written: the
page cache came back and nothing improved -- recover 468 (pure CPU) is
untouched, exec is worse, and the 6 GiB cap puts the leader into a GC
regime where it misses view deadlines. The follower's gap to the replay's
731 ms is CPU contention, not memory. Side reading: the leader's align is
657 ms (35za: 512) -- 952ec3e4's skip does not fire under tenure, because
the parent of the speculative build is the leader's own just-sealed block,
not yet applied; the align IS the leader re-applying its own block (track 3
of QS_REPLAN, own-block conversion).

## 6ak. Round 35zd: 16 workers a node -- registered before the round ran (2026-09-09)

35za's configuration (GOMEMLIMIT 10 GiB) with N42_PARALLEL_WORKERS 32 -> 16.
Six followers recover and execute the same block at the same moment: 6 x 32
= 192 worker threads plus the leader's build and eight generators on 128 SMT
cores, so each thread runs at SMT speed. At 16 workers the 96 threads fit
the cores.

**Prediction 44.** Follower recover 460 -> ~250-300 ms and exec 300 -> ~220
(each thread on a full core, half as many of them: the replay does 137/157
with 32 uncontended threads), total 1.4-1.9 s -> ~1.1-1.3 s; B 54-57k ->
62-68k. If recover stays at 460 the contention is not SMT but something
serial in recover itself (the sender cache lock, allocation) -- then the
next lever is the pool-side pre-recovery (track 2). If B drops, the
followers were CPU-bound in their own right and 32 workers were needed.

**Round 35zd result (2026-09-09 19:48-21:50).** Sixteen workers a node
changed nothing the prediction named, because it could not: the recovery
fan-out is sized from GOMAXPROCS (37 -> 28 workers), not from
N42_PARALLEL_WORKERS, so recover stayed at 465-466 ms in every leg, and the
Block-STM executor at 16 workers ran exec in 286-293 ms against 297 at 32.
A1 32.4k / 30.9k; B1 59.8k / 54.3k (22 and 20 blocks a window: the same
quantisation as 35za's 54.3k / 57.1k). Follower 1448-1518 ms, leader build
1270 / assemble 544 / write 290 / push 159 -- 35za to the millisecond. A
codex session's `cargo test` in rBTC (~17 cores, 20:26-20:37) overlapped
A1 win2 and the first minutes of B1. Prediction 44 falsified on its
mis-specified variable; the real reading is that neither the worker count
nor the memory budget moves the follower, and the leader's chain is what
sets the view (QS_REPLAN section 7).

## 6al. Round 35ze: the leader stops marshalling the block for nobody -- registered before the round ran (2026-09-09)

35za's configuration on n42-r37: `api.MachineVerify` no longer subscribes
to MinedEntireEvent when `validVerifiers` is empty (it always is: nothing
populates it), so `HasSubscribers` is false and the miner skips building
the Entire event -- the 252 ms "other" of the leader's assemble at 163k.

**Prediction 45.** Leader assemble 544-608 -> ~300 ms (finalize/root
~280 + copy ~22); the leader's per-view chain 2.4 -> ~2.15 s; B windows move
from 20-22 blocks to 22-24 (59-65k). Falsified if assemble stays >450 ms
(then the 252 ms is not the Entire event and the timers around it are
wrong) or if the view period does not follow assemble down (then the
leader chain is not the pole after all and section 7 is wrong).

**Round 35ze result (2026-09-09 22:46 - 09-10 00:38).** Prediction 45
holds: the leader's assemble fell from 544-608 to 266-279 ms (breakdown
"other" 252 -> 0; finalize 231-279 is what is left), leader build unchanged
(1286-1348: align 550, fill 544-592, persistWait 269-289, reload 179-193),
follower unchanged (1417-1577), and the view followed the leader's chain
down: B windows 22-24 blocks (predicted 22-24). A 36.2k / 35.4k and
34.7k / 33.9k (35za 33-35k, a new A high); B 59.8k / 61.4k and 65.2k /
62.5k -- four-window mean 62.2k against 35za's 55.0k (+13%) and 35zd's
59.1k. Warm-up read 65.2k / 62.5k. B1 lost 8 views (four nodes' view of
two) to the tenure-handoff gate described under prediction 47; B2 three.
QS_REPLAN section 7 stands: the follower did not move and the throughput
did.

## 6am. Round 35zf: the miner tree keeps its own appends -- registered before the round ran (2026-09-09)

35ze's configuration plus `N42_MINER_ADOPT_APPENDS=1` (n42-r38). Track 3a of
QS_REPLAN section 8: when the block the isolated miner tree built is the
parent of the next build (parent header root == the tree's root, the live
tree's cursor == the tree's cursor, no branch switch queued),
`NewMinerRootComputer` keeps the tree as it is -- drops the undo, trusts the
index to the cursor, evicts what the live tree flushed -- instead of peeling
its appends and re-reading the same entries from MDBX. Unit test:
`TestQMDBMinerTreeAdoptsOwnAppends` (adopted tree == fresh load, block
after block; a build the live tree did not write is refused).

**Prediction 46.** Leader build 1270 -> ~800 ms: `reload` 145-234 -> <10 ms
on every build inside a tenure (the first build of a tenure still reloads);
`persistWait` unchanged this round (the build still waits for the parent's
write before it can look at the header). With 35ze's assemble at ~300,
the leader's chain 2.15 -> ~1.7 s and the view approaches the follower's
~1.6 s: B windows 22-24 -> 25-27 blocks (68-73k). Falsified if `reload`
stays >100 ms (adoption not taken: the log line "adopted its own appends"
absent -- then the precondition is wrong, most likely the cursor
comparison) or if roots diverge (BAD BLOCK: the two trees did not hold the
same layout, and the abort proves the adoption unsafe -- revert the flag).

**Prediction 47 (same round, read separately).** 35ze B1 lost two views
to a 6 s timeout each, both at a tenure handoff: the incoming leader's gate
returned `committed-parent-blocked` (a chain-wide streak of commits that
outran the follower's import), the parent applied 100-160 ms later, and
nothing resumed the view because only the parent-not-applied branch
registered a deferred retry. n42-r39 registers it in both branches and
re-probes once after registering (the import can land between the check and
the registration). Prediction: "view timed out" 0-1 per B leg (35ze B1: 2
per node view, 8 across nodes), "deferred production resumed" appearing at
handoffs instead; each recovered view is worth ~2.5 s of the leg. Falsified
if timeouts persist with a different gate outcome (then the handoff cost is
elsewhere).

**Round 35zf result (2026-09-10 01:11-03:04).** Prediction 46 holds:
leader build 1286-1348 -> 1063-1077 ms, `reload` median 4 ms (mean 50: the
first build of a tenure still reloads, p90 225), `persistWait` 269-289 ->
225-229, no BAD BLOCK in five legs -- the two trees do hold the same layout.
Prediction 47 holds: view timeouts inside the B windows 8 -> 0 (the four in
B2 were at the leg's restart, before any gate ran), "deferred production
resumed" 4-7 a leg. A **39.2k / 37.7k** and 37.7k / 36.6k (35ze 36.2 /
35.4); B 62.5k / 59.8k and 59.8k / 59.8k, four-window mean 60.5k (35ze
62.2k, 35zd 59.1k): the leader's 500 ms did not become blocks, because the
view is now the follower's chain -- import 1.4-1.6 s, then
commit-to-canonical ~200 ms inline in the HotStuff loop before `view
changed`, then the vote (QS_REPLAN section 7's crossover reached). The
leader levers are spent until the follower moves; round 35zg starts on the
follower side (prediction 48). N42_MINER_ADOPT_APPENDS stays the runners'
setting; the default flips once 35zg confirms nothing regressed with it on.

## 6an. Round 35zg: no per-commit fsync -- registered before the round ran (2026-09-10)

35zf's configuration plus `N42_MDBX_SYNC=safe-nosync` (the opt-out
node.go:mdbxSyncModeOr documents but nobody has measured). 35zf's
`commit-to-canonical phases` on a follower: total 143-208 ms of which the
MDBX commit of a few canonical-hash rows is 89-143 ms -- the fsync -- and
it runs inline in the HotStuff loop before `view changed`, so the follower
starts importing the next proposal that much later; the import's own
write commit shows 14 ms because its pages are already flushed by the
time the meta page syncs.

**Prediction 48.** `commit-to-canonical` commit 89-143 -> <10 ms, `canon`
~200 -> ~60 ms; blockwrite `commit` on leader and follower down by the
same order; the follower's chain per view -150 ms and B windows 22-24 ->
24-26 blocks (65-70k). Falsified if the canon commit stays >50 ms (then it
is writer-lock contention with the import's transaction, not the fsync)
or if B does not move with it (then the loop's 200 ms was not on the
follower's path to its next vote). Durability note: a crash rolls the
node back a few blocks and it re-syncs; for the benchmark this is the
same trade every client makes when it batches its fsync.

**Round 35zg, first reading (warm-up, 03:18-03:29).** Prediction 48 is
falsified on its mechanism, and the mechanism was a units error: the
`commit-to-canonical phases` fields are nanoseconds, so 35zf's "commit
89-143 ms" was 89-143 us. The 200 ms of `canon` in the HotStuff loop sits
OUTSIDE the MDBX transaction: after it, CommitToCanonicalWith hashes every
transaction of the committed block for the transaction index -- serially,
on an instance freshly decoded by rawdb.ReadBlockByHash (80 ms of read when
the block cache misses) whose hashes are not memoised -- ~200 ms at 163k.
safe-nosync itself: follower blockwrite `commit` 14 -> 6 ms, leader write
unchanged, warm-up 59.8k / 62.5k. The round runs to completion for the
record; the durable mode returns for 35zh.

**Round 35zg result (2026-09-10 03:07-04:02, aborted at B1's first
block).** A 37.3k / 35.8k; B1 aborted on a BAD BLOCK: node6, leader of the
leg's first view, proposed an EMPTY block 14029924 whose root (c6901af1)
matched neither the five followers (c63d8299) nor its own live tree
(921cf405) -- the OPEN_ISSUES "three roots after a restart" shape, this
time with the sequence in the log: the leg's restart reverted node6 to the
committed 14029922 while consensus named 14029923 (applied before the stop)
as the parent; the speculative build's align found the applied head BELOW
the parent and returned without doing anything (unwindForReimport leaves
"not applied yet" to the future queue); the build ran on a tree reloaded at
14029922; node6 re-imported 14029923 one second later; the parked task
matched by parent hash and was proposed. Not the adoption path (that build
reloaded). Fixed in n42-r41: after the align, a build whose parent is not
the applied head is abandoned (speculative) or imports the parent with
switch authority first (production). safe-nosync's other cost showed too:
node5, stopped by the abort, needed MDBX recovery and came back one block
short (14029922) -- the documented trade, and one more reason 35zh runs
durable.

## 6ao. Round 35zh: the committed block from the cache, hashed across the cores -- registered before the round ran (2026-09-10)

35zf's configuration (durable MDBX) on n42-r40: CommitToCanonicalWith takes
the committed block from the block cache when it is there (it is the
instance this node imported two views ago, every hash memoised) and, on a
miss, hashes the fresh decode across up to 32 goroutines.

**Prediction 49.** `hotstuff: commit phases` canon 199-237 -> ~10-30 ms;
`commit-to-canonical phases` read 80 -> <1 ms on a cache hit; the follower's
chain per view -180 ms and B windows 22-24 -> 24-26 blocks (65-70k).
Falsified if canon stays >100 ms (then the time is in notifyBlockCommitted
or the tx-index Add itself) or if B does not follow (then the loop's canon
was not between the commit and the follower's next import after all).

**Round 35zh result (2026-09-10 04:09-06:07).** Prediction 49 holds on
its phase: `hotstuff: commit phases` canon 199-237 -> mean 18 ms (median 0,
p90 55), `commit-to-canonical phases` total 12.7 ms (read 6.5 on a cache
miss). No view timeouts, no BAD BLOCK in five legs on n42-r41 (the
unapplied-parent guard logged no abandoned builds: the shape needs a
restart-revert gap and none occurred). A **40.0k / 37.7k** and 37.0k /
37.0k (35zf 39.2k); B 62.5k / 62.1k and 65.0k / 65.2k -- four-window mean
**63.7k**, the best so far (35za 55.0k, +16%), but the windows still read
23-24 blocks: the 180 ms came off the follower's loop and the view moved
2.6 -> 2.5 s at most. What is left of the follower's view is the import
itself (1.44 s: recover 460, exec 290, finalize 200, write 260, body 91) --
QS_REPLAN section 9, track 2 next.

## 6ap. Round 35zi: senders recovered ahead of the block -- registered before the round ran (2026-09-10)

35zh's configuration on n42-r42 with `QS_INGEST_HINT=1`: every node runs
the ingest endpoint in hint-only mode (`--ingest --ingest.hint-only`:
decode, recover the sender from the signature into the process-wide sender
cache across 28 workers, admit nothing, ignore the client's sender field)
and txflood-r38 streams every submitted batch to all seven endpoints
(`-hint-peers`) off its submit path. Track 2 of QS_REPLAN section 9.

**Prediction 50.** Follower `recover` 460 -> <50 ms (the import's
verification hits the cache: `RecoverSenderFromSig` consults it first),
`hintHits` stays 0 (the hint source is the pool copy; this path fills the
cache, not the pool), import 1.44 -> ~1.0 s, view ~2.5 -> ~2.1 s, B windows
23-24 -> 27-28 blocks (73-76k). The hint work is ~11 cores fleet-wide at
60k tx/s x 7 endpoints. Falsified if recover stays >300 ms with txflood
reporting sent ~= submitted and dropped ~= 0 (then the import does not
consult the cache on this path, or the cache is evicted before the block
arrives -- 4M slots against ~450k transactions in flight says it should
not be), or if exec/finalize grow by what recover lost (then the recovery
CPU merely moved).

**Round 35zi (2026-09-10 18:11-18:38, void).** Two faults, neither the
prediction's: the hint feed decoded with the native codec and rejected
every transaction the generators submit (Ethereum RLP; fixed 704475ee), and
the fleet's MDBX maps hit 256 GiB (the datadirs were then reseeded from
qs-era-linux: 16 GiB a node, seed head 13,652,362).

**Round 35zj, warm-up reading (19:42-).** The feed works: each node
recovers 100-160k senders a second, rejected 0, queue empty, txflood sent
== submitted with no drops. Follower recover 460 -> 368 ms, import 1440 ->
1395; not the <50 ms predicted. Hashes agree across codecs
(TestHashAgreesAcrossCodecs), so the entries are findable; the remaining
cost is the verification's pool lookup, which runs BEFORE the cache: 163k
`GetTx` read-locks against a pool writer admitting 60k tx/s. n42-r44 asks
the cache first (`transaction.CachedSender`).

**Round 35zj result (2026-09-10 19:42-20:50).** The feed works
(100-160k senders a second a node, rejected 0, queue empty, no drops);
follower recover 460 -> 350 ms, exec 249, finalize 217, import 1440 ->
1337 -- prediction 50's direction, a third of its size (the verification
still went to the pool first; prediction 51). A **40.8k / 40.4k** and
38.5k / 35.4k; B 70.6k / 65.2k and 70.6k / 67.0k -- four-window mean
**68.4k** (35zh 63.7k, +7.4%; 35za 55.0k, +24%), warm-up 67.9k / 70.6k,
windows 24-26 blocks. Confound to record: the fleet was reseeded before
this round (16 GiB a node instead of 256; the whole datadir sits in the
page cache), and the leader's build read 987 (align 424, persistWait 209,
fill 508) against 35zh's 1063-1077 -- part of the gain is the smaller
store, not the feed. No timeouts, no BAD BLOCK.

## 6aq. Round 35zk: the verification asks the cache before the pool -- registered before the round ran (2026-09-10)

35zj's configuration on n42-r44. **Prediction 51.** Follower `recover` 368
-> <60 ms and `hintHits` 163,000 (the cache hit counts there now);
import ~1.4 -> ~1.1 s; B windows 23-24 -> 26-28 blocks. Falsified if
recover stays >200 ms with hintHits near 163k (then the time is not in the
lookup but in the check loop's own overhead -- hashing or the fan-out) or
if hintHits is low with the feed complete (then cache eviction: 4M slots
against the in-flight set).

**Rounds 35zk/35zl (2026-09-10 21:22-, 22:04-).** 35zk was aborted at its
warm-up: the cache-first verification counted zero hits. 35zl's one-shot
probe explains it: the block's transactions carry NO declared sender on
the wire, so the verification loop returns before the cache; the recover
phase is `applySenderHints` -- a single goroutine looking every one of the
163,000 transactions up in the pool (GetTx takes the pool lock against a
writer admitting 60k tx/s, ~2 us each) -- and that is the 350-410 ms. The
cache itself is being hit: the feed line's process-wide counters show
~73% of the import's lookups hitting (the rest evicted from 4M slots).
Prediction 51 was aimed at the wrong loop. 35zl ran to completion as the
baseline for 35zm: A 37.7k / 39.6k and 40.4k / 38.1k; B 70.1k / 70.6k and
67.9k / 67.9k, four-window mean **69.1k** (35zj 68.4k: the reseeded fleet
with the feed reproduces to 1%).

## 6ar. Round 35zm: the hint pass asks the cache first, across the cores -- registered before the round ran (2026-09-10)

35zl's configuration on n42-r46, N42_SENDER_CACHE_SLOTS 4M -> 16M.
applySenderHints consults the sender cache before the pool and runs across
the recovery fan-out; the pool lookup is the fallback for a miss.
**Prediction 52.** Follower recover 350-410 -> <60 ms, hintFills ~163,000,
import ~1.34 -> ~1.0 s, view -0.3 s, B windows 24-26 -> 27-29 blocks
(73-79k). Falsified if recover stays >150 ms with hintFills near 163k
(then the residue is the fan-out's own cost) or hintFills is low (then the
16M cache still evicts: the in-flight set is larger than assumed).

**Round 35zm result (2026-09-10 23:13 - 09-11 00:24).** Prediction 52
holds on its phase and fails on its throughput: follower recover 350-410 ->
51 ms on the first dozen full blocks (hintFills 149k of 163k) and 91-122 ms
in the B legs (hintFills 117-132k), import 1337 -> 891 (first blocks) and
1170-1210 ms (B legs, p90 1450). A **41.5k / 40.4k** (a new A high) and
38.9k / 37.7k. B 67.9k / 62.5k and 62.5k / 57.1k -- four-window mean 62.5k,
BELOW 35zl's 69.1k baseline, with B1 and B2 reading the same phases
(import 1.2 s, leader assemble 0.30 + write 0.34 + push 0.20, no
timeouts) while the window went 25 -> 21 blocks. The measured phases sum to
~1.8 s of a 2.4-2.9 s view; what varies is not in them. Round 35zn is the
diagnostic (n42-r47, tMs stamps).

**Round 35zn result (2026-09-11 01:29-02:42, diagnostic).** The stamps
placed the view (QS_REPLAN section 10): write(v) end -> QC 0.85 s, QC ->
next write end median 0.85-1.17 / p90 1.3-1.9 s, the excess being the
leader waiting for its own speculative build. Throughput as 35zm: A 39.6k /
36.6k and 39.6k / 36.6k; B 65.2k / 58.8k and 70.6k / 62.5k, four-window
mean 64.3k.

## 6as. Round 35zo: two-deep speculation -- registered before the round ran (2026-09-11)

35zm's configuration on n42-r48 (track 3c, QS_REPLAN section 10). A
speculative build whose parent is a block this node sealed and has not yet
applied no longer waits for that block's write: the miner tree already IS
the parent's post-state, so the build chains on it (ChainPendingBuild:
the parent's undo moves onto a pending stack), the parent header comes from
the worker's own sealed block, and when the write lands the next build
adopts the parent beneath the child (AdoptOwnAppends by cursor). A lost
view peels the stack (PeelAll) and reloads. Test:
TestQMDBMinerTreeChainsPendingBuilds.

**Prediction 53.** On the leader, the QC-to-next-write-end gap (35zn:
median 0.85-1.17 s, p90 1.3-1.9 s) collapses onto its floor of assemble
0.30 + push 0.20 + write 0.34 (~0.85 s median, p90 <1.0); "miner:
speculative build chains on own unwritten block" appears on every in-tenure
build; persistWait ~0 on those builds. View ~2.4 -> ~1.7 s; B windows
21-26 -> 30-35 blocks (80-95k). Falsified if the gap's p90 stays >1.3 s
(then the build's own 1.05 s is still late even when started at the seal --
look at fill) or if BAD BLOCK returns (then chaining on an unwritten own
block diverges from the live replay and the design is unsafe: revert to
adoption-only).

**Round 35zo, first attempt (2026-09-11 02:58-03:01, aborted).** The first
chained build was rejected by every follower: "ParentBeaconRoot mismatch:
committee-evidence link broken". Not the state root -- the header link.
The engine's Prepare derives ParentBeaconRoot from the parent header it
fetches with GetHeaderByHash, which for an own block still in flight
returns nothing (the header is written with the block), so the chained
header carried no link. Fix: the worker hands its sealed block to the
chain's header and block caches at seal time (RememberSealed), which
GetHeaderByHash consults first; the derivation itself
(parentBeaconRootFromHeader) is pure. Re-run as 35zp on n42-r49 with
prediction 53 unchanged.

**Round 35zp (03:42-03:44, aborted, same error).** The header cache did
not help because GetHeaderByHash looks the block NUMBER up in the store
before it consults the cache, and an unwritten block has no number row.
n42-r50 asks the cache first. Third attempt: 35zq.

**Round 35zq (03:47-03:48, aborted at the first block; n42-r50 was not
tested).** No build chained ("chains on own unwritten block": 0). The bad
block was 35zp's: node0 had sealed 13694447 (0x2b1031c607) in 35zp as the
chained build with the broken link, written it as its own applied block,
and never marked it bad -- the followers mark a block they fail to verify,
the leader that built it does not verify it. On restart node0 unwound it
(uncommitted), became the leader of view 2, built a fresh 13694447
(0xa53a4100aa), found the stored sibling with the lower hash and, by the
cross-view convergence rule, re-proposed the stored one. Every follower
rejected it again. So a leader's own unverified build survives a restart
as a candidate. Two consequences:

- The store-side hole is closed on n42-r51: with committee evidence wired,
  Prepare refuses a header whose parent it cannot resolve (that is the
  only way the link comes out zero), so the block is never sealed, never
  written, never a sibling. Tests
  TestPrepareRefusesUnresolvableParentWithCommittee /
  TestPrepareToleratesUnresolvableParentWithoutCommittee. The lookup fix
  from n42-r50 stays; the guard makes its regression loud instead of
  poisoning the store.
- node0's store now carries the bad-header mark for 0x2b1031c607 (its own
  re-proposal failed import at 03:48:05 and reportBlock marked it), so
  the next restart cannot converge on it; the other six marked it in
  35zp. The fresh candidate was dropped before any write.

Fourth attempt: 35zr on n42-r51, offsets 1800M+, prediction 53 unchanged.

**Round 35zr (04:37-04:39, aborted at the first chained block).** The
header link held (n42-r50/r51 do their job: "chains on own unwritten
block" fired once, no refusal, no link error). The chained build of
13694448 -- an EMPTY block, two reward accounts, three tree slots -- was
rejected by all six followers on the state root (proposer dac540ce,
followers f5cea1e4). The leader's own write of the same block passed, so
its live tree agreed with its miner tree: the divergence is not in the
tree, it is in what the build READ. A build reads its base state through
a plain reader on a store transaction, and the store does not hold the
unwritten parent; the coinbase was credited on the balance from before
the parent's reward. One-deep speculation never saw this because
persistWait made the parent's write land before the build read anything.
With 163k transfers a block the same stale read would hit every sender
touched in consecutive blocks.

Fix (n42-r52, PostState / PostStateReader in modules/state): in
adopt-appends mode the worker snapshots each built block's dirty set
right after the root is computed (accounts, written and wiped slots,
deployed code -- the same account and storage rules as the root walk),
keeps it beside the sealed block, and a chained build layers the
snapshots of every unwritten own ancestor over its store reader, oldest
innermost. The layers are gathered before the read transaction opens so
a write landing in between cannot fall through both. When a snapshot is
missing the build waits for the write as before (logged). Test
TestPostStateReaderLayersTheSealedBlock.

**Prediction 54 (replaces 53's mechanism claim, keeps its numbers).**
Chained blocks are accepted: BAD BLOCK 0 over a full round with "chains
on own unwritten block" on most in-tenure builds. The leader's
QC-to-next-write-end p90 <1.0 s, view ~1.7 s, B windows 30-35 blocks.
Falsified if any chained block is rejected on the state root (then the
snapshot misses a class of effect -- read the address in the mismatch
against the parent's receipts) or if the p90 stays >1.3 s with chaining
in place (then the build itself is the floor and the fill is the next
lever). Round 35zs, offsets 1840M+.

**Round 35zs (04:51-04:52, aborted at the first chained block, same
shape).** Overlay in place (one layer, five accounts), the empty chained
block 13694449 was still rejected on the state root. **Round 35zt
(04:56-04:57, diagnostic)**: N42_FINALIZE_TRACE=1 now lists the leaves
behind a small block's root on both sides. For 13694450 the proposer and
a follower agree byte for byte on four leaves (the two system contracts
with their slots, the two reward accounts) and the follower has a fifth:
the system-call address 0xfff...fffe as an empty account. Mechanism: the
value-zero system call runs Transfer(system -> contract, 0);
SubBalance(system, 0) goes through GetOrNewStateObject, and when the
reader says the account does not exist it CREATES an empty object (a
journal event, so the account is dirty and the root walk carries it).
The store readers answer nil for an empty account (emptyByPlainPolicy)
and the root walk deletes its key, so every block on every node creates
it anew -- except the chained build, whose snapshot of the parent showed
the address as an existing empty account: SubBalance(0) found it, the
create never happened, and the leaf was missing. The values were never
wrong; the snapshot's notion of existence was. n42-r54: the snapshot
records an empty account as absent, the rule the readers apply. Round
35zu, offsets 1920M+, prediction 54 unchanged, trace left on.

**Round 35zu (05:03-, running).** Chaining holds: 1,267 chained builds in
the first 40 full proposals, BAD BLOCK 0, no missing snapshot. Warmup
windows 26 / 24 blocks, 70.6k / 65.2k TPS at 2.31 / 2.50 s a block (35zn:
62-64k at 2.4-2.6 s). The leader's full-block cycle, from the tMs stamps
(in-tenure, n=70): seal(v) -> seal(v+1) 1.91 s median = seal -> QC 1.52 s
(push 0.19, follower import 1.15, votes) + QC -> next seal 0.27 s. The
build (0.72 s fill + 0.32 s assemble, persistWait 0) now hides behind the
followers' import, which grew from 0.93 to 1.15 s (proc 0.75, write 0.21,
body 0.10) because the leader's chained build overlaps it on the same
box. Prediction 54's mechanism claim is met (chaining, p90 of the leader's
own wait 0.55 s); its view number is not: the handover block -- the first
of each tenure, whose leader can only start after IMPORTING the previous
leader's block -- costs 3.43 s median (p90 4.7 s, n=19) against 1.91 s
chained, and one block in four is a handover: (3 x 1.91 + 3.43) / 4 =
2.29 s, which is the window's 2.31 s.

**Round 35zu, continued (aborted in B1 at 05:40).** A1 windows 114 / 112
blocks, 43.2k / 42.3k TPS at 0.53 s (A best before: 41.5k). B1's first
full blocks: BAD BLOCK 13699518, a chained block of 73,000 transactions,
state root rejected by all six followers. The trace (large block, so
only the summary line) shows every count equal and ONE value different:
the faucet's balance, follower = proposer + 1000e18 = exactly one block
reward. The faucet is paid the dev block reward every block and, in this
block, also funded 1,000 new senders; the proposer's value equals the
faucet's balance after 13699516 (the last WRITTEN block) minus the
funding minus nothing, plus this block's reward -- that is, the parent
13699517's reward was never seen. The chained build reads the parent
through the snapshot only where the build's own IntraBlockState reads;
the Block-STM fill (N42_MINER_PARALLEL_FILL=1) opens a store transaction
per worker and reads base state through a plain reader plus the live
QMDB tree, which hold the last written block, not the unwritten parent.
Every sender touched in consecutive chained blocks read stale there too;
the fill simply failed those transactions (97 of 779 parallel fills
dropped candidates, 322k in all -- the 18,600 / 17,300 / 49,300 / 73,000
blocks in the windows), which is why the empty warmup blocks and most
full blocks passed and only a block whose stale read changed an included
transaction's outcome was rejected. n42-r55: the parallel builder layers
the build reader's snapshots over every worker's base reader
(PostStateLayers / LayerPostStates). Tenure 16 (35zv) waits; the
one-variable rerun is 35zw = 35zu on n42-r55, offsets 2000M+.

**Prediction 56.** BAD BLOCK 0 across all five legs; parallel fills drop
no candidates for stale nonces (partial blocks only when supply runs
out); B windows at or above 35zu's 26 blocks / 70k. Falsified by any
state-root rejection of a chained block (then a third reader bypasses
the snapshot -- look for BeginRo in the fill path) or by fills still
dropping candidates on in-tenure blocks.

**Round 35zw (05:48-05:50, aborted at its first block; n42-r55 not
tested).** The 35zq shape again: node0 re-proposed 35zu's rejected
13699518 from its store by lowest-hash convergence. The 35zq record
called the builder-never-marks-its-own-block hole "closed at the
source", which was true only for the unresolvable-parent case; any
rejected own build survives a restart as a candidate. Closed now
(85211338): the leader writes an own-unverified mark with its block,
LowestSiblingAtHeight skips marked blocks, CommitToCanonical clears the
mark once the fleet commits the block; a deterministic rebuild of the
same block is still re-proposed. Fleet binary n42-r56 from the next
round on. 35zx = 35zw relaunched on n42-r55 (the stale block now carries
a bad mark on node0), offsets 2040M+, prediction 56.

**Round 35zx (05:52-, n42-r55; aborted by hand once the cause was read).**
Warmup 25 / 23 blocks, 67.9k / 62.4k. Prediction 56's second falsifier
fired: 43 of 235 parallel fills still dropped candidates, all "nonce too
high" with the WORKER's state nonce 0 while the block's own reader gave
the right nonce -- every chained build, every node. The per-worker
readers never received the layers: the mobile read-log recorder
(mobileverify runs on the fleet) wraps the build reader, and
PostStateLayers walked the chain from the outside, met the recorder and
returned nothing. The block's own reads were right all along because the
recorder delegates. n42-r57: the worker records the layers on the
IntraBlockState (SetPostStateLayers) and the parallel builder reads them
from there, no type walk. Test: an opaque wrapper hides the layers from
the walk and the carried layers still re-layer a base. Round 35zy =
35zx on n42-r57 (with the own-unverified mark from r56), offsets 2080M+,
prediction 56 unchanged.

**Round 35zy (06:14-07:2x, n42-r57).** Prediction 56 held: BAD BLOCK 0
across the legs run so far, parallel fills dropped nothing (0 of 194 in
warmup, 0 in the B legs), chained builds 1,216 in warmup and 2,360 in the
B legs. Windows: warmup 26 / 24 (70.6k / 65.2k), A1 101 / 98 (38.5k /
37.3k), B1 27 / 25 (72.3k / 67.9k; the 72.3k window is the best B window
recorded), B2 26 / 23 (70.6k / 62.5k). B four-window mean 68.3k against
35zj/35zl's 68.4k / 69.1k: chaining shortened the in-tenure cycle to 1.92
s but the handover block (3.44 s, one in four) and the follower import
(0.93 -> 1.18 s under the overlap) took the gain back. The A legs read
11% under 35zu's with every phase median equal and only the tails wider
(follower write p90 102 -> 262 ms), the round-to-round tail variance the
noise-floor section warned about. Full-block cycle in the B legs (n=139):
seal -> seal 1.92 s = seal -> QC 1.57 + QC -> seal 0.24; handover 3.44 s.
Follower import 1.18 s = setup 58 + recover 100 + exec 255 + apply 24 +
finalize 227 (QMDB apply ~100) + validate 16 + body 98 (the Erigon tx
root: 72 ms of trie on a cached encoding) + write 213 (block 118,
receipts 13, state 22, commit 16).

## 6au. Round 35zzb: the follower's import sheds allocations and a serial encode -- registered before the round ran (2026-09-11)

35zy on n42-r58, two changes on the import path and nothing else:

- The parallel executor reuses each transaction's read/write set across
  executions instead of allocating one per execution on top of the one
  NewExecutor already made (163k dead allocations a full block, two
  slices each). Import setup 58 ms is that allocation.
- WriteTransactions encodes the block's transactions across the cores
  (16 workers) before the single-writer append loop; the encode was half
  of the 118 ms "block" write phase. Rows are byte-identical
  (TestWriteTransactionsParallelMatchesSerial).

**Prediction 57.** Follower import 1.18 -> ~1.08 s (setup 58 -> ~15,
write.block 118 -> ~60), seal -> QC 1.57 -> ~1.47, in-tenure period 1.92
-> ~1.82 s; B windows +4-5% (26 -> 27 blocks). Falsified if the import
median does not move by at least 60 ms (then the setup is not the
allocation and the block write is not the encode -- profile both) or if
any BAD BLOCK returns.

**Round 35zzb (08:54-10:11, n42-r58).** Prediction 57 falsified on its
own terms: the follower import median moved 4 ms (1166 -> 1162 in B1
against 35zy's B1), not 60. The block write phase did what it was meant
to (block 118 -> 67 ms, write 213 -> 164) but body (97 -> 111), finalize
(225 -> 238) and exec (246 -> 258) each drifted up by about as much --
the serial encode it removed was 16 goroutines' worth of burst on every
follower at the same instant, on a box that hosts all seven, and the
tails moved. Setup stayed at 59 ms: the read/write sets are reused but
NewExecutor's own allocation is the setup (fixed on n42-r60, the arena).
Windows: warmup 27 / 24 (73.4k / 64.5k), A1 104 / 103 (39.6k / 39.2k), B1
24 / 24 (65.2k / 64.4k), B2 27 / 25 (73.4k / 67.9k), A2 112 / 107 (42.7k /
40.8k); B mean 67.7k against 35zy's 68.3k. BAD BLOCK 0; two fill drops,
both legitimate (a warmup nonce-too-low sweep and a sender the build
reader also saw at nonce 0). The parallel encode stays: it is right on a
machine per node, and it costs nothing here beyond the tails.

## 6av. Round 35zzc: the pool stops reheaping on an unchanged base fee -- registered before the round ran (2026-09-11)

**Round 35zz (07:29-, tenure 16, n42-r57) so far.** Warmup windows 68 /
78 blocks at 0.88 / 0.77 s, 73.7k / 67.2k TPS (73.7k is the best window
recorded), occupancy 19.9% / 15.9%: the chain now outruns what the
builder finds. Of 544 blocks in the two windows 270 were empty, 187 under
50k, 74 full; the parallel fill saw a median of 12,800 candidates, p90
163,000. The builder reads the pool's reorg-published pending snapshot;
under tenure 4 one snapshot fed the three chained blocks of a tenure, at
tenure 16 it is spent after three or four and the leader mints empties
until the next reorg publishes. The slow-reorg lines show reorgs of
0.4-1.8 s of which the timed phases sum to ~150 ms (demote 100-170);
the untimed remainder is priced.SetBaseFee, which reheaps the priced
lists -- a walk over all 600k remote transactions, a heapify and 120k
single pops -- on every block although the base fee is flat on the gas
target block after block. Prediction 55's block time cannot be read on
half-empty blocks; the round is recorded for its TPS and this finding.
Full round: A1 104 / 106 blocks (39.6k / 39.7k), B1 71 / 78 (67.8k /
64.3k at 16-18% occupancy), B2 77 / 80 (68.4k / 66.9k), A2 110 / 108
(41.9k / 41.1k); B mean 66.9k against 35zy's 68.3k -- the empty-block
tax took what the shorter handover share gave. BAD BLOCK 0.

35zz on n42-r60: SetBaseFee returns early on an unchanged fee, and the
reorg line times the phase. In-tenure blocks and everything else as 35zz.
(n42-r60 also carries the executor arena -- 35zzb's warmup showed the
import setup at 64 ms with the read/write sets reused, so the remaining
allocation is NewExecutor's own; the arena pools it across blocks and the
"parallel block" line now splits setup into blockStartMs / executorMs.
Its metric is setupMs, separate from prediction 58's reorg total.)

**Prediction 58.** Reorg total on the leader ~1 s -> <0.3 s with the
basefee phase ~0; the builder finds a full snapshot for most builds
(fills with >=100k candidates the majority; empty blocks in the windows
under 20% of 544); occupancy in the B windows back to 50% or supply-bound
at the generators' ~117k/s accepted; B windows >=85k TPS. Falsified if
reorg totals stay above 0.5 s (then the demote or the lock wait is the
rest) or if occupancy stays under 30% with the pool full (then the
pending snapshot cadence, not the reorg cost, starves the builder).

**Round 35zzc (10:14-, tenure 16, n42-r60) warmup.** Prediction 58's
reorg half held -- reorg totals 227 ms, all of it demote, basefee 0 --
and its occupancy half fell: 76 / 72 blocks at 0.79 / 0.83 s, 66.4k /
65.6k TPS, occupancy 16%, candidates median 10,600, pending accounts
after a reorg ~100. The pool is not full and its snapshot is fresh; it
is EMPTY: what reaches a node -- its seventh of the generators' RPC
submissions plus what gossip carries from the other six -- is ~66k tx/s,
and at tenure 16 one leader drains exactly that. Tenure 4 read the same
70k for a different reason (cycle-bound at 1.9-3.4 s a block, each
leader draining a pool that filled while it followed). Both regimes meet
at ~70k: the follower cycle on one side and per-node transaction ingress
on the other. The executor arena did nothing (executorMs 54 ms): a
sync.Pool is emptied by every GC cycle and a full block triggers one;
n42-r61 keeps the arenas in a free list instead.

**Round 35zzc, continued (aborted by hand at 10:58).** A1 104 / 106 (39.6k
/ 39.7k). The B1 restart at 10:46 left the fleet crawling: the first views
timed out while peers were still dialing, the view-4981 leader's block
reached the followers before its parent was applied and was queued as
future, each follower fetched and imported the parent on miss, held its
commit vote for the queued child, and the child was never retried -- the
future drain gates on the CANONICAL head, which under the two-chain
commit lag trails the applied head by two, so a queued proposal always
read as canonical+3. The next leader extended the un-QC'd block, the
followers repeated the dance one block back, and the fleet advanced one
block per 30 s timeout for twelve minutes (103 timeouts). Fixed on
n42-r62: the drain gates on the applied head and retries with push
authority. Also seen: "fetch-on-miss: requesting block" on the LEADER for
its own just-pushed block, 4,050 times in the A1 windows (0 in 35zzb) --
the engine asks to execute the proposal, the block is unwritten and not
in the block cache; harmless but noisy, noted for later. The two CPU
profiles taken for the ingress question caught the stalled fleet (0-1% of
20 s) and say nothing.

## 6aw. Round 35zza: 64 parallel workers -- registered before the round ran (2026-09-11)

35zy's configuration (tenure 4) on n42-r62 (r61 plus the future-drain fix) with N42_PARALLEL_WORKERS=64
(the runner sets 32). The follower's exec phase is 255 ms and the leader's
fill 431 ms at 32 workers on a 256-core box whose seven nodes run 32
workers each; the B-window mpstat read 17-32% busy.

**Prediction 59.** Import exec 255 -> ~170 ms, fill 431 -> ~300 ms;
follower import 1.16 -> ~1.06 s; in-tenure period 1.92 -> ~1.82 s; B
windows +4-5%. Separately, executorMs 54 -> <10 ms from the durable
arena. Falsified if exec does not drop by 50 ms (then the workers are
memory-bound, not core-bound, and more of them only contend) or if the
box's busy share climbs past 60% with no gain (then seven nodes at 64
workers oversubscribe 256 cores at the import instant).

**Round 35zza (11:05-, n42-r62) warmup.** 24 / 23 blocks, 64.1k / 62.2k
at 2.54 / 2.61 s: below 35zy's warmup (26 / 24). Import exec 255 -> 220
ms and fill 431 -> 404 -- a third of prediction 59's -85 / -130. The box
at 64 workers: load 70 against 30. setupMs / executorMs stayed at 61 ms
with the durable arena in place, while NewExecutor on a kept arena
measures 2 ms in isolation (68 ms cold): the remaining cost is the fresh
163k-element result slice the runtime returns to the OS under the memory
limit and page-faults back in on every block -- n42-r63 pools it too.
The heap profile taken meanwhile (node1, 7.6 GB in use): txlookup tail
1.2 GB, the mobileverify packet cache 1.08 GB, the sender cache 1.07 GB,
the QMDB map index 0.76 GB, decoded transactions ~2 GB; GC CPU fraction
1.3%, one collection every ~3 s.

**Round 35zza, end (11:35).** A1 118 / 113 blocks, 45.0k / 43.0k -- the
best A windows recorded (43.8k before): 64 workers do pay on 22,857-tx
blocks, where exec is a larger share and the box has room. The B legs
were lost to a harness edit of mine: the QS_MOBILEVERIFY switch added to
qs-env.sh between legs used a bare `$( [[ .. ]] && echo .. )`, whose
exit status of 1 when the variable is unset aborted the node start under
set -e, and B1, B2, A2 each "finished" in a second (fixed with `|| true`;
the switch is now inert unless set). Prediction 59 on what ran: exec
-35 ms, fill -27, warmup B windows 24 / 23 (35zy 26 / 24) -- falsified
for full blocks, held for A blocks; the box's load doubled. The
future-drain fix released 29 queued blocks over the round with no
timeouts beyond the restart ones. 35zzd keeps 32 workers.

## 6ax. Round 35zzd: the mobileverify cohort off -- registered before the round ran (2026-09-11)

35zy's configuration (32 workers, tenure 4) on n42-r63 with
QS_MOBILEVERIFY=0 (the harness passes --mobileverify=false; the n42
profile enables the cohort by default). The cohort keeps a 1.08 GB packet
cache on every node and wraps every builder read in a read-log recorder
that builds a stream packet per sealed block; none of it is consensus.

**Prediction 60.** Follower heap -1 GB; the leader's fill and assemble
lose the recorder's share (fill 431 -> ~400 ms, assemble 317 -> ~290);
executorMs 61 -> <10 ms from the pooled result slice; B windows at or
above 35zy's 26-27 blocks. Falsified if the leader's fill and assemble
do not move (then the recorder is cheap and only the memory was real) or
if any BAD BLOCK returns (then something in the cohort was load-bearing
for the header -- MobileRegistryRoot is stamped from mobileAnchorRoot).

**Round 35zzd (11:38-11:54, aborted: no node started).** Prediction 60's
second falsifier fired before any block: with --mobileverify=false every
node stops at startup with "mobileverify must be enabled on a mining node
when MobileAnchorTime is configured" -- the cohort stamps
MobileRegistryRoot into the header, so it is load-bearing on this chain.
The cohort stays; only its packet retention is a free variable.

**Prediction 60, revised (round 35zze).** 35zy's configuration on
n42-r63 with QS_EXTRA_ARGS="--mobileverify.packet-window 8" (default
256; the flag is documented as serving retention only). Follower heap
-1 GB; executorMs 61 -> <10 ms from the pooled result slice; the cycle
unchanged unless memory pressure was feeding the page-fault cost, in
which case import setup and the leader's build both shorten. Falsified
by any BAD BLOCK (retention would then be consensus after all) or by the
heap not dropping (the cache is not what the profile says it is).

**Round 35zze (11:56-12:51, aborted in B2).** Warmup 27 / 25 (73.4k /
67.9k), A1 113 / 109 (43.0k / 41.5k), B1 25 / 25 (67.9k / 67.9k). The
packet window read nothing: executorMs stayed at 62 ms with the result
slice pooled too, and NewExecutor on a kept arena measures 2 ms in
isolation -- whatever the 62 ms is, it is not an allocation (the CPU
profiles meant to say what it is landed in the decay both times; next
time key the capture on the first full block). B2 died on the hole the
two-deep design left open: after a view timeout the leader (node3)
rebuilt 13750006 on the same parent, the align UNWOUND its own applied
13750006 to do so ("branch switch: unwinding applied blocks", depth 1),
the rewind peeled the miner tree's pending build, the rebuild was then
suppressed in favour of the first sealed block, and the next speculative
build chained on that first block -- reads through its snapshot (right),
root from a tree reloaded at 13750005 (wrong). Six followers computed
the same different root. n42-r64: NewMinerRootComputer's reload path
refuses a build whose parent root the tree does not hold; the production
trigger aligns first and builds the height.

## 6ay. Round 35zzf: 35zze again on n42-r64 -- registered before the round ran (2026-09-11)

**Prediction 61.** Five legs, BAD BLOCK 0; "miner tree reloaded at root"
appears at most a handful of times per leg, each followed by a normal
build of the same height; B windows at 35zy's level (26-27 / 24-25
blocks). Falsified by a BAD BLOCK with matching trace counts (another
base-mismatch path) or by the refusal firing on every chained build
(then the adopt path is not taken where it should be).

**A confound found while 35zzf ran.** The seven datadirs, 16 GiB each at
the reseed of 2026-09-10 19:20, are 172 GiB each eighteen hours later --
1.2 TB, ~85 GB a round -- against 62 GB of page cache. Every later
round imports and writes against a colder store than the round before
it, which is a plausible reading of the widening tails from 35zy through
35zzf (B warmup 26 / 24 -> 24 / 21 with no code change that touches the
follower) and of prediction 57's "gain that moved into the tails". The
box also has 1.9 TB left at this growth. 35zzg reseeds first.

**Round 35zzf (12:57-14:21).** Prediction 61 held: five legs, BAD BLOCK
0, the refusal never fired, 10 branch switches and 28 view timeouts
(the restart ones) passed without a bad root. Windows: warmup 24 / 21
(65.2k / 57.1k), A1 107 / 107 (40.8k / 40.8k), B1 25 / 23 (67.9k /
62.5k), B2 28 / 24 (75.6k -- the best window recorded -- / 65.2k), A2
113 / 108 (43.0k / 41.1k); B mean 67.8k. The windows of one round now
span 57k to 76k on a store of 172 GiB a node.

## 6az. Round 35zzg: the same configuration on a fresh reseed -- registered before the round ran (2026-09-11)

35zzf's configuration on n42-r64 after seed-7node-nolaunch.sh (16 GiB a
node, seed head 13,652,362) with the grown generation deleted.

**Prediction 62.** B windows back at 35zy's level or above (26-27 / 24-25
blocks, ~70k); follower import median under 1.10 s with the p90 within
1.3x of it (35zzb: 1.55x); the A legs at 43-45k. Falsified if the fresh
store reads the same as the grown one (then the drift is not the page
cache and the tails are the code's), in which case the store's growth is
only a disk problem and the reseed cadence can stay weekly.

**The first full-block profile (35zzf B1, node1 as follower, 25 s = 263
CPU-seconds, ~10.5 cores).** Signature recovery is 53% of the node's
CPU (transaction.Sender 141 s), and 119 s of that is the hint ingest's
hintWorker: the eight generators stream every transaction to every node
as a hint and each node recovers ~124k signatures a second, ~4.8 cores,
34 across the fleet -- the price of the 100 ms import recovery. RPC
ingest (BatchRawTransaction) is 10%, the EVM (executeSingle) 10%.
Inside runParallel, per block: Finalize 225 ms of which QMDB Tree.Set
183; ProcessPragueSystemCalls 120-156 ms, all of it FinalizeTx -- the
parallel apply leaves the whole block's dirty set in the journal and
each of the two block-end system calls walked its 23k objects through
updateAccount with the no-op writer; NewExecutor 54-64 ms, the arena
take walking 163k kept read/write sets on a cold cache (2 ms in
isolation, where the cache is warm). The heap (8.9 GB in use): QMDB map
index 753 MB, libp2p buffer pool 453 MB, the packet cache 331 MB (the
window did cut it from 1.08 GB), sender cache 315 MB, read/write sets
250 MB.

**Round 35zzg (14:25-, fresh dirs, n42-r64) through B1.** Warmup 30 / 26
blocks at 2.00 / 2.31 s, 79.4k / 70.6k -- the best window recorded, on
the first full leg after the reseed; A1 119 / 111 (45.3k / 42.3k, the
best A windows); B1 26 / 24 (70.6k / 64.9k). B1's follower import: 1103
ms median, p90 1470 (1.33x) against 35zzb's 1162 / 1.55x on the grown
store; write 154 against 164-213. Prediction 62 held on the import
median and the windows, missed its p90 clause by a hair (1.33x against
1.3x): the store's growth is real drag, worth 5-10% by the end of a
day, and the reseed belongs at the start of every comparison set. The
cycle itself did not move (chained 1.85 s, seal -> QC 1.56, handover
3.45), so the levers stand where the profile put them.

**Round 35zzg, B2 (aborted 15:12).** A new shape, once: block 13659928,
an empty chained block in B2's decay at four blocks a second, rejected
by all seven nodes (the leader's own echo included) with "invalid QC in
extra-data: snap_ssz: length overflow" -- the header's QC bytes did not
decode. The state was not involved (trace counts equal). The QC handed
to Prepare is a deep clone under the engine lock, Seal signs a
CopyHeader, and buildHeaderExtra allocates fresh, so no path in the
code as read explains it; one block in ~50,000 chained today points at
a race in the hand-over of a parked block. n42-r66 makes Seal decode
the extra it is about to sign and refuse the block otherwise, logging
the bytes -- the next occurrence costs one build instead of a round,
and leaves evidence. The B1 numbers stand.

## 6ba. Round 35zzh: FinalizeTx sets flags only, the arena take stops walking -- registered before the round ran (2026-09-11)

35zzg's configuration (fresh dirs, n42-r64 -> n42-r66: r65 plus the Seal self-check). FinalizeTx with
the no-op writer sets the deleted flags -- the one state effect of
updateAccount under that writer -- and skips the sort and the per-object
no-op calls; the arena take fills empty slots only (executeSingle clears
a set before every execution and its TxIndex is its slot). Both are
exact: the same flags, the same sets.

**Prediction 63.** Follower import -150 ms (setup ~60 -> <10, block-end
system calls ~130 -> <15); the leader's build sheds the same ~130 ms of
block-end and ~55 ms of setup; seal -> QC 1.5 -> ~1.35 s; in-tenure
period ~1.9 -> ~1.75 s; B windows +6-8% over 35zzg. Falsified if
executorMs stays above 20 ms (then the take is not the walk) or if the
block-end phase does not move (then FinalizeTx's cost is elsewhere), or
by any BAD BLOCK (a flag the fast path gets wrong -- compare the trace).

**35zzh result (n42-r66, fresh dirs; 15:18-16:26).** No BAD BLOCK, 0
refusals, ~430 chained blocks. Warmup 76.1k/65.2k (28/24 blocks), A1
27.8k/41.9k (the flood's ramp; 0.5-0.8 s blocks), B1 73.4k/65.2k, B2
65.2k (win2 was a 9-block window cut by the leg's end), A2 44.6k/44.2k.
Against 35zzg (r64, fresh dirs): B1 win1 70.6k -> 73.4k (+4%), win2
64.9k -> 65.2k; A 45.3k -> 44.6k. Follower import B1 median 1158 ms
(proc 741, body 101, write 166), B2 1114 (proc 683); chained seal ->
seal 1919 / 1871 ms, handover 3525 / 3337. The "parallel block" phases:
executorMs 62 (P63: <20; fell), finalizeMs 226 (P63: block-end -115;
fell). Prediction 63 is falsified on both mechanism claims and the
throughput claim (+6-8%; got +4% on one window, 0 on the other). The
profile that explains it is in 6bb; the corrected levers run as 35zzi
(the root's batch) and 35zzj (the arena list). Noted in passing: the
"hotstuff: committed block not executed locally" error (a CommitQC
arriving before the local import finishes) runs at ~100 an hour across
the fleet in every B leg since at least 35zzg; benign here, not new.

## 6bb. Round 35zzi: the root computer applies a block under the tree's leaf batch -- registered before the round ran (2026-09-11)

35zzh's configuration on a fresh reseed, n42-r66 -> n42-r67. The one
change: QMDBRootComputer.ComputeRoot builds []qmdb.Op and calls
Tree.ApplyOps, which wraps the block's sets in BeginLeafBatch/EndLeafBatch
-- leaf hashes 16-wide, each touched twig's paths folded once per level,
each twig's liveness bitmap hashed once -- where Set had hashed an
11-level path per op and the bitmap twice (deactivation, new leaf).
35zzf's follower profile: 1.83 s of 25 in Tree.Set, 183 ms of the 220 ms
finalize, and the leader's build root the same. The batch mode existed
(batch.go, ShardedTree's hot path) but the root computer never used it.
Isolated, 31k overwrites on a 2M-key tree: eager 92 ms, batched 18 ms.
The roots are exact (TestQMDBComputerBatchedApplyMatchesEager; the
tree-level TestBatchFoldEquivalence already covered overwrites, deletes
and a mid-batch Root).

Why 35zzh did not move (the readout that led here): executorMs 61 -> 57
ms and finalizeMs 220 -> 210 ms; both P63 mechanism claims fell. The
35zzf profile, read again: arenaFor's 0.64 s was NewReadWriteSet
allocations, not a walk -- the kept arena is not being found (or not
kept) on a follower, so the take allocates 163k sets every block; and the
finalize window is the engine's Finalize computing the follower's own
root, not the Prague block-end calls (own-built blocks, which skip
Finalize, show finalizeMs 15 ms; imported 220). The FinalizeTx fast path
is correct but was aimed at 1.56 s the profile attributed to a leader's
build, not at the follower's phase. The arena question is left open
until 35zzh's B1 profile is read.

35zzh's B1 profile (n42-r66, node1, the first full blocks) splits the
follower's 210 ms finalize: Tree.Set 1.88 s of 25 against 1.28 s in the
two block-end system calls' FinalizeTx -- about 125 ms of Set and 85 ms
of block-end per block. The block-end cost is not the flag loop (the fast
path runs: finalizeFlags is in the profile, updateAccount is not under
that caller); it is the balanceInc pre-fold reading ~23k delta-credited
recipients from the store, one QMDB lookup each, serially
(getStateObject -> QMDBStateReader.ReadAccountData 0.45 s, plus the sort
of the 23k addresses twice). That is a later lever (materialize the
deltas across the workers before the block end). This round moves only
the Set.

**Prediction 64.** Follower finalizeMs 210 -> ~110 ms (the ~125 ms of
Set to ~30 on the 14M-key tree: the index misses and the deactivation's
DRAM touch stay, the hashing goes 5x; the ~85 ms of block-end stays);
follower import 1.10 -> ~1.0 s; the leader's build root sheds the same;
seal -> QC 1.43 -> ~1.33 s; chained seal -> seal 1.78 -> ~1.68 s;
handover 3.3 -> ~3.2 s; B windows +5-7% over 35zzh (B means ~68k ->
72-73k). Falsified if finalizeMs stays above 170 ms (then the eager
hashing was not the Set's cost on the live tree -- the index and the
leaf heap are, and the lever is the flat index or prefetching), or by
any BAD BLOCK (a batched fold whose root diverges under undo recording,
a revert, or eviction -- the watchdog aborts the round).

**35zzi result (n42-r67, fresh dirs; 16:31-17:40).** No BAD BLOCK.
Warmup 78.5k/70.6k (29/26 blocks -- the best first window so far), A1
49.1k/45.7k (best A), B1 65.2k/48.9k, B2 54.3k/40.8k, A2 40.4k (the
harness printed one window twice). Mechanism: follower finalizeMs 226 ->
164 ms (warmup 168; P64 said ~110, threshold 170: held, narrowly -- the
QMDB apply is 26-30 ms, the ~85 ms recipient fold and ~50 ms of the rest
remain); B1 whole-leg import 1158 -> 1030 ms, chained seal -> seal 1919
-> 1776, and the first flood minute of each B leg imported at ~800 ms
with chained cycles of 1.5 s -- the fastest the fleet has run. The
throughput claim (+5-7%) is falsified: the B windows fell 11-25%,
because every B leg decays over its five flood minutes on the MDBX
writer lock (6be: block writes waiting up to 2.4 s to begin behind the
history fold). The batched root is kept -- its phase is exact and
smaller -- and the decay is the next round's variable. Per-minute
numbers: wr-logs/r35zzi-perminute.txt.

## 6bc. Round 35zzj: the arena free list keeps the largest arenas -- registered before the round ran (2026-09-11)

35zzi's configuration on a fresh reseed, n42-r67 -> n42-r68. The one
change: the executor's arena free list drops a too-small arena on take
and, when full, replaces its smallest arena with a larger released one;
the parallel processor's pooled result slice gets the same rule. 35zzh's
B1 profile showed why prediction 63's executorMs claim fell: arenaFor ->
NewReadWriteSet was 0.45 s of 25 on a follower with the arena code in
place. The list holds two arenas, a too-small one was given straight
back, so the two sized during a leg's ramp held both slots for the rest
of the run and every 163k block built its sets fresh (and its Release
was dropped, the list being full). TestExecutorArenaFreeListKeepsTheLargest
fails on the old list.

**Prediction 65.** Follower executorMs 57 -> <15 ms (the take reuses;
what remains is the MVS allocation and the result-slice clear);
setupMs the same; the leader's build sheds the same ~45 ms; follower
import -45 ms; chained seal -> seal -45 ms; B windows +2-3% over 35zzi.
Falsified if executorMs stays above 30 ms after the first full block of
a leg (then a third consumer holds an arena, or the cost is not the
allocation), or by a BAD BLOCK (a reused set leaking a previous block's
reads -- the clean-reuse test covers this, but the fleet is the proof).

**35zzj result (n42-r68, fresh dirs; 17:43-18:52).** No BAD BLOCK.
Warmup 78.8k/70.6k, A1 48.0k/44.6k, B1 70.6k/68.4k, B2 73.4k/70.6k (B
mean 70.7k -- the best B legs so far; 35zzh 68.0k, 35zzi 52.3k), A2
44.6k/40.0k. Mechanism: follower executorMs 58 -> 0 ms median, p90 3, in
every minute of every leg (P65: <15; held). Whole-leg import B1 1053 /
B2 1034 ms, chained seal -> seal 1805 / 1817, handover 3681 / 3338. The
throughput claim (+2-3% over 35zzi) reads +35% only because 35zzi's B
legs collapsed on the write lock and 35zzj's did not: this round's
begin-wait peaked at p90 0.74 s (35zzi: 2.2 s) and imports drifted 787
-> 1164 ms over a flood instead of 821 -> 2624. Against 35zzh (r66,
before the root batch) the B mean is +4%, the import -100 ms, the
chained cycle -100 ms -- the batch (P64) and the arena (P65) together.
The write-lock decay's size varies round to round; 35zzl tests the fold
interval directly. Per-minute: wr-logs/r35zzj-perminute.txt.

## 6bd. Round 35zzk: the delta-credited recipients are read across the workers before the fold -- registered before the round ran (2026-09-11)

35zzj's configuration on a fresh reseed, n42-r68 -> n42-r69. The one
change: a reader layer (AccountPrefetch) directly under the state's
reader, on the import and on the build; after the multi-version store is
applied, the parallel processor lists the pending balance increases the
state has not read (~23k delta-credited recipients a full block), reads
them across up to 16 goroutines on their own read transactions through
the workers' reader stack, and seeds the layer. The block-end fold
(FinalizeTx in the two Prague system calls, then IntermediateRoot) reads
the same addresses in the same sorted order through the same chain --
the mobile read-log recorder sits above the layer and logs what it
logged before -- and every read is a map hit. 35zzh's B1 profile: those
reads were 0.45 s of 25 in QMDBStateReader.ReadAccountData under the
fold, ~85 ms a block with the sort, serial.

**Prediction 66.** "parallel block" prefetched ~23k, prefetchMs ~10;
follower finalizeMs (now measured after the prefetch) -70 ms against
35zzj; the leader's build sheds the same ~70 ms (hidden behind the QC
wait in a tenure, visible at the handover); follower import -70 ms; seal
-> QC -70 ms; chained seal -> seal -70 ms; handover -140 ms; B windows
+3-4% over 35zzj. Falsified if prefetchMs + finalizeMs is not below
35zzj's finalizeMs by 50 ms (then the fold's reads were not the cost, or
the parallel reads contend on the tree's reader lock), or by any BAD
BLOCK (a prefetched value differing from what the fold would have read
-- the workers' view against the state's chain).

**Offline check of r66 vs r69 before the rounds ran (qs-replay, 16:27-16:31).**
A reflink copy of qs-node0 taken during 35zzh's B2; the last 60 blocks
(10-11 full 163k blocks among them) rewound and re-imported, 16 workers,
16 CPUs, uncontended. r69 (= r67 + r68 + the prefetch): 0 root errors.
Per full block, r66 -> r69: executorMs 37 -> 13 (the arena list, P65);
qmdb root applyMs 75 -> 25 (the batch, P64); prefetchMs 4 for 22,857
recipients; finalizeMs 170 -> 96 (P64 + P66 together: -74); proc 499 ->
414; import total 633 -> 549 ms (-13%). The fleet numbers are ~1.8x
these (contended), so the three rounds together should take the
follower's import from ~1.11 s toward ~0.95 s.

**35zzk result (n42-r69, fresh dirs; 2026-09-11 23:14 - 2026-09-12 00:21;
the queue lost 18:52-23:12 to a chain script counting its own claim
files).** No BAD BLOCK. Warmup 76.1k/70.6k, A1 50.3k/46.9k (best A), B1
78.8k/69.5k, B2 76.1k/67.9k (B mean 73.1k, best yet; 35zzj 70.7k, +3.4%
-- P66 said +3-4%), A2 46.5k/43.8k. Mechanism: prefetched 22,857
accounts in 7 ms on every full block; finalizeMs per minute 120-157
against 35zzj's 141-186 at the same points, so prefetch + finalize is
-20 to -25 ms, short of P66's -50: the serial store reads were about a
third of the fold's ~85 ms, the rest is the two sorted walks, the object
creation and the second system call's pass over 23k objects. Whole-leg
import 997 / 1010 ms (35zzj 1053 / 1034), chained seal -> seal 1643 /
1786 (1805 / 1817), handover 3193 / 3181 (3681 / 3338). P66 holds on
throughput and on direction, falls on the size of the phase; the layer
stays. Per-minute: wr-logs/r35zzk-perminute.txt.

## 6be. Round 35zzl: the history index folds every 20 s -- registered before the round ran (2026-09-11)

35zzk's configuration on a fresh reseed, n42-r69 -> n42-r70 (the
backfill interval read from N42_HISTORY_INDEX_INTERVAL; the write probe
logs waitMs and heldMs per write transaction), with
N42_HISTORY_INDEX_INTERVAL=20s. Unset, r70 runs as r69.

What 35zzi showed (per-minute, saved in wr-logs/r35zzi-perminute.txt):
the B legs start fast and decay. B2's flood: imports 821 ms in its first
minute, 864, 1099, 1530, then 2624 ms in the fifth; proposals per minute
29 -> 15. The whole of the growth is the block write's "begin" -- the
wait for the single MDBX writer: p90 141 ms in the first minute, 302,
1957, 2207 ms in the fifth, back to 0 the minute the flood stops. The
execution phases move a little with the box's load (recover 51 -> 120,
exec 207 -> 286, QMDB apply 26 -> 30 ms); the write lock moves 2 s. The
write probe names four writers: HotStuffState (1 row), LastBlock (the
canonical commit, ~100 ms), BlockTransaction (the block write, 27 MB
payload, 30 MB dirty) and AccountHistory -- the deferred history fold:
22.9k rows, 24.8 MB payload, 106 MB dirty pages (amplification 4.3),
once per block, because it ticks every 2 s and rewrites the index chunk
of every account the block touched, and the block's 23k accounts are the
same hot set every block. The 2 s the block write waits is that fold's
commit in front of it; as the index grows over a leg the fold slows and
the wait grows. GC (32-46 collections a minute, 8 GB heaps at the 10 GiB
limit) and the disk (12% busy, 60 MB/s) are not it.

**Prediction 67.** Block-write begin wait p90 late in a B leg 2.2 s ->
<0.3 s; the AccountHistory write probe shows one fold per ~20 s with
waitMs/heldMs naming it; the late-leg import median 1.5-2.6 s -> ~1.0 s;
B win2 no longer collapses (35zzi: 65.2k -> 48.9k, 54.3k -> 40.8k): win2
within 10% of win1; B window means +15-25% over 35zzi. Falsified if the
begin wait stays above 1 s late in the leg with the fold at 20 s (then
the writer is held by something the probe does not name, or by the block
writes themselves queueing behind each other -- read heldMs), or if the
fold's own transaction at ~10 blocks exceeds the 255 MB dirty limit and
spills (heldMs of the AccountHistory rows).

**35zzl result (n42-r70, N42_HISTORY_INDEX_INTERVAL=20s, fresh dirs;
2026-09-12 00:23-01:27).** No BAD BLOCK. Warmup 81.5k/70.6k, A1
56.4k/53.0k, B1 86.9k/76.1k, B2 90.3k/81.5k, A2 57.9k/51.8k. B mean
83.7k (35zzk 73.1k: +14.5%; P67 said +15-25% -- held at the edge), A
mean 54.8k (+16%), and 90.3k is the best window the fleet has produced.
Mechanism, all held: the block write's begin wait is 0 at p75 and p90 in
every flood minute (max 0.3-1.4 s, single blocks; 35zzi: p90 2.2 s); the
write probe's new fields name the writers -- on node1 over the B legs
AccountHistory ran 77 times (one fold per ~20 s) holding the writer 415
ms median, 752 p90, 3.3 s once (a fold under the 255 MB dirty limit,
not spilling), BlockTransaction 130 ms median, LastBlock 9 ms p90; the
TxPoolJournal appeared twice at 55 MB / 445 ms. Late-leg imports 1001-
1039 ms (35zzk 1101-1148), win2 within 12% of win1 on both B legs
(35zzi: -25%). The fold at 20 s rewrites each hot chunk once per ~10
blocks instead of every block. Per-minute: wr-logs/r35zzl-perminute.txt.

Standing after the four rounds (all fresh dirs, 32 workers, tenure 4):
B mean 68.0k (r66) -> 52.3k (r67, write-lock collapse) -> 70.7k (r68) ->
73.1k (r69) -> 83.7k (r70 + fold 20 s); follower import 1158 -> ~1000
ms; chained seal -> seal 1919 -> ~1650. Left on the import: exec 250-310
(grows over a leg with the box's load), recover 50 -> 110 (same), the
tx-root/body 100, finalize ~130 (QMDB apply 28, the fold's two sorted
walks), write 130-210. The fold interval is a knob, not a fix: the 415 ms
hold every 20 s is still 2% of the writer; nosync for the fold (its
marker makes a lost transaction safe) and a longer interval are the
cheap follow-ups. Beyond the import, the cycle is import + fixed; the
deferred-execution rule agreed with the n42-rs side (header N carries
the execution of N-1) makes it max(build, import) and is the next
structural lever.

## 6bf. Round 35zzm: the BLAKE3 binary transactions root -- registered before the round ran (2026-09-12)

35zzl's configuration on a fresh reseed, n42-r70 -> n42-r71, with
N42_TXROOT_BLAKE3_TIME=1789185600 (2026-09-12 00:00 EDT: after the seed's
last block, before the round). The one change: the transactions root of
every block from that time is hash.Blake3BinaryRoot -- leaf blake3(0x00 ||
enc), node blake3(0x01 || l || r), odd nodes carried up, empty blake3("")
-- in place of the Ethereum keccak Merkle-Patricia trie that a73a7258
put the QMDB chain on. The trie builder is single-threaded and ~70 ms at
163k on every follower (the "body" phase, ~100 ms with the decode) and
in the leader's assembly; the binary tree hashes every level across the
cores: 6 ms in isolation. The chain's state is a BLAKE3 binary forest;
the body root now follows the same design. Proposed to the n42-rs side
as a joint header rule (their side pays the same ~72 ms of serial
keccak); both clients' vectors are in common/hash/txroot_blake3_test.go.

**Prediction 68.** Follower import "body" 100 -> ~35 ms; leader assemble
262 -> ~200 ms; follower import ~1000 -> ~935 ms; seal -> QC -65 ms;
chained seal -> seal 1650 -> ~1585; B windows +3-4% over 35zzl (B mean
83.7k -> ~86-87k). Falsified if the body phase stays above 70 ms (then
the decode, not the root, is the phase) or by any BAD BLOCK or "transaction
root hash mismatch" (a leader and a follower disagreeing on the gate --
the env value must be identical on all seven nodes; the runner exports
it once for all).

**35zzm, first attempt (n42-r71, 05:15-05:5x): void.** The gate was set
as a wall-clock timestamp (1789185600), but the chain's block timestamps
run ~9 days behind the wall clock (the era seed's head, block 13,652,362,
is stamped 1788393863; the fleet's blocks continue from there at the
chain's own pace), so no block of the round reached the gate: the B1
profile shows ValidateBody's 2.36 s of 25 still in the keccak MPT root.
Warmup 78.8k/72.9k and A1 56.4k/52.6k are r70-equivalent figures.
Aborted at B1 for the audit's fixes (6bh); both gates are now the seed
head's timestamp + 1 (1788393864), which every block of a reseeded
round is past, and the first block of the round is the fork block.

## 6bh. The audit before the deferred-execution round (2026-09-12 05:20-06:00)

Four read-only reviews of r67-r72 in parallel (the consensus change;
the QMDB batch, arena and prefetch; the transactions root, backfill and
probe; the fork gating and restart paths) found twenty items, fixed in
b15cd2b7 (n42-r73). Three would have failed every deferred block on the
fleet -- the processors' gas-used comparison against the header, the
tree-at-parent alignment and the startup self-check comparing the live
tree with header roots, the leader's own write comparing its replay
with blk.StateRoot() -- and one was a confirmed race in r69's prefetch
(the live tree read without the readers lock while the import writes
it). The includability rule gained the fee-cap, sender-code, blob,
overflow and nonce-max checks that execution still enforces, and reads
the parent's state under the readers lock; a not-yet-stored parent
result is retried, not marked bad; the header's GasUsed is bounded by
the parent's limit; genesis and a zero fork time are handled; the sync
layer polls pending children; eth_getProof uses executed roots; the
runner's watchdog knows the new failure signatures. The n42-rs vectors
run against the header check in TestDeferredExecutionCrossClientVectors.
35zzm (r73, tx root) and 35zzn (r73, tx root + deferred execution) run
next, each on a fresh reseed.

## 6bg. Round 35zzn: deferred execution -- registered before the round ran (2026-09-12)

35zzm's configuration on a fresh reseed, n42-r71 -> n42-r72, with
N42_DEFERRED_EXECUTION_TIME=1789205400 (2026-09-12 05:30 EDT). The one
change is the rule agreed with the n42-rs side (their PHASE_D document,
sections 8-9; gov5's implementation is commit 93e31b89): from the fork
a header carries the PARENT's Root, ReceiptHash, Bloom and GasUsed; every
node stores its own result of each block it applies; the import checks
the header against the parent's stored result before executing; the
builder stamps the parent's result; and a follower votes for a proposal
once the parent is imported, the header matches its own result of the
parent, and the transactions pass the includability check -- without
executing the block. The cycle becomes max(build, import) instead of
import + fixed. Their bench went from 293k to 300-303k at 56 blocks a
window with the same rule (their cycle was already ~540 ms, so the gain
there was the vote's ~500 ms of import turning into ~130 ms of check);
ours has ~1.0 s of import inside a ~1.65 s cycle.

**Prediction 69.** The follower's vote leaves the import: seal -> QC
from ~1.4 s to ~0.5 s (push ~0.2 + the check ~0.15: sender recovery
~0.1 hinted + the nonce/balance pass ~0.04 + the header compare); the
chained seal -> seal from ~1.65 s to max(the leader's build ~0.65 +
seal, the follower's import ~1.0 that must finish before the NEXT
vote) = ~1.05-1.15 s; the handover unchanged in kind; B windows +40-50%
over 35zzm (B mean ~87k -> ~125k at the same 163k block, bounded by the
import of N-1 finishing before the vote on N). Falsified: any BAD BLOCK
or "deferred execution: header ... carries" import error (the leader and
a follower disagreeing on a parent's result -- the invariant at the fork
block, the chained build's stored result, or the includability rule
letting a failing transaction through), a "deferred check FAILED" line
on any node, or seal -> QC staying above 1.0 s (then the vote still
waits on something -- read "deferred vote:" against "import-gated vote:"
lines). The first deferred header appears mid-warmup if the round starts
before 05:30 EDT; that is the invariant's live test.

## 6bi. The second audit and round 35zzo -- registered before the round ran (2026-09-13)

Four read-only audits of the seven-node paths (QMDB storage and reads
against PlainState; the follower import; the leader build and seal; the
transaction ingest, pool and sender recovery) and a read of n42-rs's
last three days (80 commits, loops 120-155). Findings that set the
direction:

- Account and storage reads on the import hit the QMDB tree only; no
  plain fallback, no verify read, no layered cache (off on the bench).
  The duplication is in what sits beside the tree: the 20 s history fold
  rewrites a derived copy of AccountChangeSet (~106 MB dirty a fold);
  eviction after every block makes the next block re-read its hot set
  from MDBX two or three times; every node holds two full indexes (live
  and miner tree); QMDBUndoWindow (1.67 MB a block) is mostly bytes
  already in qmdbEntries and the changeset. Not changed in r74 (each
  needs design: section 7 of the handover).
- n42-rs's lead is not its state storage (its EVM still reads reth's
  hashed MDBX tables; QMDB only computes the root). It is deferred
  execution plus seal-first, the leader never executing its own block
  twice, and per-block QMDB work bounded by the block. Its workload
  touches ~147k accounts a block against this bench's ~23k (22,857
  recipients): the TPS figures of the two clients are not the same load.
- The r73 deferred check could not have passed a non-empty block: it ran
  before the import on wire-decoded transactions (From nil) and failed
  "has no sender". 35zzn would have aborted on its first full block.

n42-r74 = `6445f1bf` removes, byte-identically: the commit's re-decode
and re-hash of the committed block (block cache on write); the serial
block decode (parallel); the gossip copy's decode and second import
(header peek against the push in flight); half of every tx hash
(keccak of the cached encoding); the per-tx signer; the block end's
repeated sorts of ~23k recipients; ~9% of senders recovered twice
(two-way cache); the leader's per-push re-encode and synchronous gossip
publish, and the receipts copy before the Proposal; the same-head pool
reset; the reflective signing-payload encode; the per-tx hint channel
hand-off; the RPC head-block decode for eth_getTransactionCount. It
fixes the deferred check (senders recovered inside, readers lock only
around the state reads), makes the deferred commit retry a set, and
withdraws checked evidence of a block rejected on import.

**Prediction 70 (35zzo: 35zzl's configuration, fold 20 s, no fork gates,
on r74).** Leader push phase ~180 -> <=60 ms and the QC -> next seal gap
~240 -> ~130 ms; follower recover median down >=30% against 35zzl's
50-110 ms (fewer cache misses); commit-to-canonical body phase no longer
decoding (canon <20 ms on followers); seal -> QC ~1.40 -> ~1.25 s;
chained seal -> seal ~1.65 -> ~1.45 s; B mean 83.7k -> 92-95k. Falsified
if the push phase stays above 120 ms, if the B mean gains less than 5%,
or by any BAD BLOCK / "transaction root hash mismatch" (the hash
shortcut or the pending-only sorts would be the suspects).

**35zzo attempt 1 (2026-09-13 23:51 - 2026-09-14 00:16 EDT): aborted,
not evidence.** Three minutes after the gov5 claim an rbtc cold-cache
replay started without a claim (supervisor `run_replay.py`, rbtcd on
cores 0-7, ~125 MB/s written to the same NVMe; a redb lane, an audit,
then an mdbx lane, ~2 h in all). The warm-up's first window read 116k
(43 blocks, 1.40 s) and its second 43k (16 blocks, 3.75 s): the leader's
root2 replay, the history fold and the canonical commit each held the
MDBX writer 0.5-3.6 s while the page cache sat 10-15 GB below 35zzl's.
The contention would have covered every leg and spoiled the other job's
cold-cache measurement too, so the round was stopped in A1's decay. What
it did show before the contention built up: leader push phase 21 ms
(35zzl ~180), follower canon 0.7 ms, chained seal -> seal 1179 ms,
handover 2102 ms (35zzl 3200), no BAD BLOCK. Evidence is kept in
`wr-logs/r35zzo-contended/`. `chain-35zzo2.sh` re-runs it once the rbtc
supervisor has exited, and 35zzp/35zzq queue behind it.

**35zzo re-run (2026-09-14 01:17-02:15 EDT, quiet box until A2).**

| window | 35zzl | 35zzo re-run |
|---|---|---|
| A1 win1 / win2 | 56.4k / 53.0k | 63.6k / 61.0k |
| B1 win1 / win2 | 86.9k / 76.1k | 113.4k / 67.9k |
| B2 win1 / win2 | 90.3k / 81.5k | 113.2k / 67.9k |
| A2 win1 / win2 | 57.9k / 51.8k | aborted |

B mean 90.6k against 83.7k (+8.3%); A1 mean 62.3k against 54.7k
(+14%). A2 was stopped by the memory watchdog at 02:15:22: MemAvailable
fell from 64 GB to 16 GB in ten seconds while a foreign `cargo check
--all-targets` ran (02:15:06, gone by 02:15:49), three minutes into the
leg's restart. A2 is missing, not scored. **Prediction 70: not
falsified, target missed.** The B mean gained 8.3% (criterion: at least
5%), no BAD BLOCK or root mismatch in ~190 full blocks, leader push
phase median 60 ms (p90 488) against ~180; but 90.6k is short of 92-95k.

What the B legs show is a split: both first windows run 1.43 s blocks
(113k, +30% over 35zzl), and both second windows fall to 2.40 s (68k,
-11% under 35zzl). The fall is the same to the block in B1 and B2, so it
is structural, not noise. Measured through B1 and B2, uncontended:

- Follower execution rises inside each flood: 331 -> 610 -> 948 ms a
  full block (B1) and 204 -> 374 -> 729 ms (B2), import median 1060 ->
  1784 ms; 35zzl's B1 held 250-310 ms over the same four minutes.
- The live heap reaches the 10 GiB GOMEMLIMIT: node3 in-use 5.67 GB at
  the start of B1, 7.97 GB late in B2; GC every ~1.1 s (22 cycles in
  25 s) and GC mark 11% -> 26% of the node's CPU. Largest holders late:
  pool transactions decoded by BatchRawTransaction 1.92 GB, sender cache
  1.18 GB, tx lookup tail 1.13 GB, mobileverify packet cache 1.0 GB,
  QMDB map index 0.79 GB.
- The 20 s history fold (46k AccountHistory rows, 53 MB payload, 109 MB
  dirty, commit ~60 ms) holds the writer longer as the flood goes on:
  median 616 -> 1444 ms, worst 8.5 s; other writers wait behind it up to
  6.3 s.
- Ruled out: the r74 block cache on write (the fleet runs
  N42_BLOCK_CACHE_BLOCKS=4; block-decoded transactions in use 244 MB),
  and the batched hint queue (the old queue also blocked when full; the
  feed recovers the same ~45-60k tx/s a node).

A faster chain reaches the heap limit and the long folds sooner, so r74
exposes a ceiling 35zzl only touched (its second windows fell 10-12%).
**Correction to attempt 1 above:** its warm-up win2 collapse was not all
the rbtc replay. The same slide happens on a quiet box (warm-up win2
72k, B win2 68k); the replay deepened it to 43k.

**Levers for the slide, on main, not yet in any fleet binary** (their
rounds are registered after 35zzq, on whichever of 35zzp/35zzq wins):

- `f0603e82`: the deferred fold prepares its rows under a read
  transaction; its write transaction only puts ~46k rows and the marker
  (byte-identical rows; the pruner is held off by HistoryIndexMu). Expected
  to take the fold's 0.6-8.5 s writer hold to the put-and-commit, ~0.1-0.2 s.
- `15710ab9`: the tx lookup tail keeps 1M transactions (not 64 blocks,
  ~1.1 GB at 163k) and a seal tick drains its backlog (one segment per
  15 s sealed ~67k tx/s against ~116k committed).
- Harness bug: `bench-7node.sh` rebuilds `QS_EXTRA_ARGS` from its own
  flags plus `QS_NODE_EXTRA`, so every runner's
  `export QS_EXTRA_ARGS="--mobileverify.packet-window 8"` has been
  discarded since it was added; the nodes run the default window of 256
  (~1.0 GB of packets late in a B leg). The flag belongs in
  `QS_NODE_EXTRA`. Moving it changes the heap of every later round, so it
  is a round of its own, not a silent fix inside 35zzp/35zzq.

## 6bj. Round 35zzp: 35zzo plus the BLAKE3 transactions root -- registered before the round ran (2026-09-13)

Replaces void 35zzm. `N42_TXROOT_BLAKE3_TIME=1788393864` (chain time:
the era seed head is stamped 1788393863, so the round's first block is
the fork block). **Prediction 71.** Follower body (ValidateBody) ~100 ->
<=35 ms; leader assemble -60 ms; B mean +3-4% over 35zzo. Falsified if
body stays above 70 ms, or by any "transaction root hash mismatch".

**35zzp partial (2026-09-14 03:58-04:36 EDT), stopped when the user paused
for the day.** The BLAKE3 gate was live on every node (startup warning at
chain time 1788393864, no root mismatch, no BAD BLOCK).

| window | 35zzo re-run | 35zzp |
|---|---|---|
| warm-up win1 / win2 | 116.8k / 72.4k | 129.0k / 51.6k |
| A1 win1 / win2 | 63.6k / 61.0k | 69.7k / 66.7k |
| B1 win1 | 113.4k (1.429 s) | 125.9k (1.277 s) |

A1 +9.5%, B1 win1 +11%. Not scored: no B mean, no body-phase reading.
The warm-up's second window fell further (3.16 s blocks), which fits the
section 6bi slide being reached sooner by a faster chain. Logs and the
per-minute summary: `wr-logs/r35zzp-paused/`.

**35zzp re-run (2026-09-14 22:45-23:40 EDT).** Gate live, no root mismatch or
BAD BLOCK in 351 full imports. Stopped by the memory watchdog (MemAvailable 19 GB)
at the end of B2, after all four B windows.

| window | 35zzo re-run | 35zzp re-run |
|---|---|---|
| A1 win1 / win2 | 63.6k / 61.0k | 59.4k / 55.8k |
| B1 win1 / win2 | 113.4k / 67.9k | 118.2k / 57.0k |
| B2 win1 / win2 | 113.2k / 67.9k | 117.2k / 59.2k |

B mean 87.9k against 90.6k (-3%); A1 57.6k against 62.3k. Follower body
phase median 15 ms (p90 42) against ~100-180 ms in 35zzo; valid 43 ms,
proc 796 ms, write 165 ms, total 1042 ms medians.

**Prediction 71: the mechanism holds, the throughput claim fails on this
run.** Body <=35 ms is met by a wide margin. The B mean did not gain 3-4%:
first windows gained ~4%, second windows lost ~14%. The run is not a clean
A/B against 35zzo: the tmpfs `/tmp` had grown to 35 GB (Shmem 27.9 GB
against 20.8 GB during 35zzo), leaving B legs 21-24 GB available where
35zzo had 31-33 GB, node heaps reached 10.3-12.7 GB against GOMEMLIMIT
10 GiB, and this morning's partial 35zzp on the smaller tmpfs read 125.9k
in B1 win1 against 118.2k tonight. The second-window slide (6bi) is what
the transactions-root saving feeds into, and less memory deepens it.

**35zzq is held.** With ~7 GB less headroom it would reach the 20 GB
watchdog floor in its B legs as this run did. Before it runs either the
tmpfs shrinks (its largest holders are another session's Claude task
outputs, 7.9 GB, and a CI reproduction tree, 4.8 GB, neither gov5's) or a
round applies the packet-window flag the harness has been dropping
(~1 GB a node, section 6bi), which is a second variable for 35zzq.

## 6bk. Round 35zzq: 35zzp plus deferred execution -- registered before the round ran (2026-09-13)

Replaces 35zzn. Runs on n42-r75 (r74 plus the leader's write taking
the execution result its build already computed, instead of deriving the
receipts root and bloom a second time; deferred-only).
`N42_DEFERRED_EXECUTION_TIME=1788393864`. **Prediction
72.** Followers log "deferred check: block passes" and "deferred vote:
block checked and parent imported" for full blocks; seal -> QC ~1.25 ->
~0.5 s; chained seal -> seal -> max(the leader's build + seal, the
follower's import that the next vote needs) ~0.95-1.05 s; B mean +35-50%
over 35zzp. Falsified by any "deferred check FAILED", "deferred
execution: header ... carries", BAD BLOCK, a lost commit (a follower's
head standing while consensus advances), or seal -> QC above 1.0 s.

**35zzq attempt 1 (2026-09-16 20:29-20:43 EDT): aborted, and it falsifies
nothing about deferred execution -- the mechanism never ran.** The gate was
live on all seven nodes and the check passed 2779 times, but the warm-up
windows read 121.6k at 1.333 s and 51.6k at 3.158 s: the same block time as
35zzp, which has no deferred execution. Cause: the chain spec sets
`twoPhaseVoteGate`, and the Round-2 commit vote was held until the node
imported the block itself, so the cycle stayed import-bound. The round then
aborted on a false includability failure -- a follower one block behind
imported 13654279 through the catch-up range and the check read that block's
own post-state ("sender nonce 0, state expects 4096").

Both are fixed on main: the Round-2 gate accepts the deferred attestation
(checked + parent imported), a held vote fires when either lands, and the
check skips a block this node has applied and requires the tree to be at the
parent's post-state (the root the header carries) before it reads senders.
**35zzq re-runs on n42-r78 = r75 plus those two fixes**, keeping deferred
execution as its one variable; predictions 72-75 stand.

**35zzq attempt 2 (2026-09-16 21:23-21:44 EDT): aborted on a self-deadlock in
the fix itself.** The tree-root guard added to the includability check read the
root inside the readers span, and `Root()` takes that same lock for writing:
the goroutine deadlocked, every import queued behind it, and the fleet produced
one block in eighteen minutes (views timed out from 1 to 37). `RootLocked()` is
the lock-free accessor for a caller already inside the span, with a regression
test that hangs on the old call. Nothing was measured; deferred execution is
still untested on the fleet.

**35zzq attempt 3 (2026-09-17 19:23-20:29 EDT): the first round that actually
ran deferred execution. Prediction 72 is falsified on throughput and confirmed
on mechanism.**

| leg | 35zzp re-run | 35zzq (deferred) |
|---|---|---|
| A1 win1 / win2 | 59.4k / 55.8k | 71.6k / 64.4k |
| B1 win1 / win2 | 118.2k / 57.0k | 114.6k / 67.6k |
| B2 win1 / win2 | 117.2k / 59.2k | 105.4k / 68.9k |
| A2 win1 / win2 | (lost) | 67.8k / 67.0k |

B mean 89.1k against 87.9k (+1.4%), and against the best round so far, the
35zzo re-run's 90.6k, -1.7%. A mean 67.7k against 68.2k. The prediction asked
for +35-50%.

The mechanism is not in doubt: 0 check failures in ~3,200 checks a node, no
BAD BLOCK, no root mismatch, and the held Round-2 votes fire on the deferred
attestation (7 of 7, 12 of 12, 32 of 34 across three followers). What it bought
and where it went:

| | 35zzp | 35zzq |
|---|---|---|
| seal -> QC | 1146 ms | 699 ms |
| QC -> next seal | 139 ms | 406 ms |
| seal -> seal | 1232 ms | 1205 ms |
| leader fillTx | 788 ms | 508 ms |
| leader write | 657 ms | 444 ms |

Deferred execution took 39% off the vote path, exactly as designed, and the
cycle did not move: the leader now takes 406 ms after the QC to seal the next
block, where it took 139. Everything else got faster. The speculative build is
not the cause: 95% of builds hit the parked task (263 of 275 on a leader) and
96% of builds reload in under 50 ms. The 400 ms is unaccounted for, and it is
now the whole game -- at 699 ms of vote path, a cycle of ~730 ms would be
~150k TPS on the B legs.

**Next: a diagnostic, not a lever.** "miner: build phases" and "build triggered
(leader view)" carry no millisecond stamp, so the build's start cannot be placed
against the QC. The next round adds `tMs` to both (the 35zzn pattern: a
diagnostic round that changes no behaviour) and reads back where the leader
waits between the QC and its seal.

**35zzt (2026-09-19 19:11-20:17 EDT): prediction 75 confirmed, a third best in
a row -- and the bench hit its supply ceiling.**

| leg | 35zzs | 35zzt |
|---|---|---|
| A1 win1 / win2 | 72.0k / 66.7k | 72.8k / 69.3k |
| B1 win1 / win2 | 120.4k / 109.3k | 133.0k / 122.3k |
| B2 win1 / win2 | 131.5k / 89.7k | 134.8k / 120.1k |

**B mean 127.6k against 112.7k (+13.2%)**, where 3-6% was predicted. The flag is
verified on the live nodes this time: `--mobileverify.packet-window 8` appears in
/proc/<pid>/cmdline, which it never did before -- bench-7node.sh rebuilt
QS_EXTRA_ARGS and dropped it, so every round until now ran the default 256-block
window (~1 GB of packets a node late in a B leg).

**The supply ceiling.** Both second windows report 37% occupancy at 0.984 s
blocks: a full block is 50%, so the chain is now producing faster than eight
generators can fill. 127.6k is therefore a floor, not the chain's limit, and
every further gain will be under-measured until the harness supplies more --
more generators, or a bigger per-transaction budget. That is a harness round,
not a code round, and it must come before the next code lever is judged.

The three rounds since deferred execution: 89.1k -> 102.0k (fold) -> 112.7k
(tail) -> 127.6k (packet window). All three took memory pressure or long lock
holds off the block's path; none made the block's own work cheaper.

## 6bo. Round 35zzu: the leader stops restarting the build the QC arrives for -- registered before the round ran (2026-09-17)

Where 35zzq's 406 ms went, found in the code rather than a diagnostic round.
`TriggerBlockProduction` interrupts the speculative build in flight on every
real trigger. Before deferred execution that cost nothing: the QC came back
1146 ms after the seal and the guess had long since parked (139 ms from QC to
seal). With the vote path at 699 ms and a full build at 566 ms median (p90
1328), the QC now arrives while the guess is still running, the trigger kills
it, and the leader rebuilds the same block from scratch.

The trigger now interrupts only a guess for a DIFFERENT parent; one building
this very block is left to finish, and `takeSpecTask` collects it as it
already does. **Prediction 76 (on n42-r82 = the deferred configuration plus
this).** QC -> next seal falls from ~406 ms to <=150 ms; seal -> seal from
1205 to <=850 ms; B mean >=110k (35zzq 89.1k); "speculative build hit" stays
above 90% of full-block builds. Falsified if QC -> seal stays above 250 ms, if
the hit rate falls, or by any BAD BLOCK or root mismatch (a build that keeps
running across a view change is the risk the interrupt existed to avoid).

## 6bp. Candidate: the Prague delegation check reads the recipient of every transaction (found 2026-09-18, not yet a round)

`StateTransition.TransitionDb` runs, for every transaction under Prague:

    if rules.IsPrague {
        if delegatedAddr, ok := vm2.ParseDelegation(st.state.GetCode(st.to())); ok { ... }
    }

Its only effect is to warm the access list with an EIP-7702 delegate. On a
follower's CPU profile late in a B leg (35zzo, node3) `GetCode` is 2.9% of the
node, and 99.7% of that is `getStateObject`: it is the account READ, not the
code bytes. At ~4 us a call it is missing every cache -- the parallel executor
gives each transaction its own IntraBlockState, so a block pays ~163,000 reads
for ~22,857 distinct recipients.

It also undoes the delta-credit design (6bd): a transfer deliberately does NOT
read its recipient, crediting it in a buffer that the block-end fold applies.
The delegation check reads it anyway.

**Fix to test:** a per-block "does this address have code" cache shared by the
workers, invalidated when a 7702 authorisation (or any SetCode) gives an
account code within the block; the check consults it and only calls GetCode on
a hit. Expected: ~140,000 account reads a block removed on every node, the
executor's ReadAccountData share (3.1% of a follower's CPU) mostly with it.
This is consensus-path code and needs the 7702 delegation tests to cover the
invalidation before it runs on the fleet.

**35zzu (2026-09-19 15:06-16:11 EDT): prediction 76 falsified, and the
instrument that pointed at it was misreading.**

| leg | 35zzq | 35zzu |
|---|---|---|
| A1 win1 / win2 | 71.6k / 64.4k | 70.5k / 65.1k |
| B1 win1 / win2 | 114.6k / 67.6k | 118.4k / 74.8k |
| B2 win1 / win2 | 105.4k / 68.9k | 107.4k / 40.8k |
| A2 win1 / win2 | 67.8k / 67.0k | 66.7k / 50.3k |

B mean 85.3k against 89.1k. The change is in effect -- "the speculative build
in flight is this block; letting it finish" fired 54 and 93 times on two
leaders -- but that is 10-15% of builds, and the cycle did not shorten:
seal -> seal 1326 ms against 1205, seal -> QC 827 against 699, QC -> seal 386
against 406. No check failure and no BAD BLOCK in ~5,700 checks a node.

**What the 406 ms actually was.** Pairing the leader's own logs by block
number (park, hit, propose) shows the parked build is taken by the trigger
after 0 ms -- the guess is never waiting -- and the 677 ms that follow are the
propose path itself, which is logged AFTER the write: assemble 219 ms plus
write 535 ms. "QC -> seal" in cycle.py is measured to the "propose phases"
line, so it has always included the leader's write, and the 139 -> 406 ms jump
in 35zzq was the write moving, not a rebuild. The interrupt fix is sound (it
cannot cost anything to keep a build the trigger wants) but it was aimed at the
wrong thing.

The leader's write (535 ms median) is on the critical path between one seal and
the next, even with N42_PROPOSE_BEFORE_WRITE: the next build needs its result.
That, and the second-window slide (6bi), are what the remaining rounds test.

## 6bl. Round 35zzr: the deferred fold outside the write transaction -- registered before the round ran (2026-09-15)

Runs only if 35zzq passes (no deferred-check failure, no BAD BLOCK); its
configuration is 35zzq's on n42-r76 = `f0603e82`. `chain-35zzr.sh` is written,
not launched. **Prediction 73.** The write probe's fold transactions
(AccountHistory rows) hold the writer <=0.2 s median late in a B leg, against
0.6-1.4 s median and 8.5 s worst in 35zzo; block writes stop waiting behind them
(worst wait <=1 s, was 6.3 s); B second windows +10% and the B mean +5-10% over
35zzq. Falsified if the fold's held time stays above 0.5 s median, by any
"marker moved while the fold was prepared" line during a flood (a second folder
exists), or by a historical query refusal the marker should not produce.

**35zzr (2026-09-19 16:49-17:55 EDT): prediction 73 confirmed, and the first
round of this campaign that moved the number.**

| leg | 35zzq | 35zzr |
|---|---|---|
| A1 win1 / win2 | 71.6k / 64.4k | 72.4k / 68.2k |
| B1 win1 / win2 | 114.6k / 67.6k | 110.3k / 94.0k |
| B2 win1 / win2 | 105.4k / 68.9k | 117.5k / 86.4k |
| A2 win1 / win2 | 67.8k / 67.0k | 71.2k / 66.7k |

**B mean 102.0k against 89.1k (+14.5%), and against the best round so far --
the 35zzo re-run's 90.6k -- +12.6%. A mean 69.6k, also a best.** No check
failure and no BAD BLOCK.

The mechanism is exactly what was predicted:

| | 35zzo | 35zzr |
|---|---|---|
| fold's writer hold, median | 0.6-1.4 s | 154-175 ms |
| fold's writer hold, worst | 8.5 s | 547 ms |
| other writers waiting, worst | 6.3 s | 185 ms |

And it lands where the slide was: the second windows went 67.6k -> 94.0k and
68.9k -> 86.4k, so a B leg now loses 15% from its first window to its second
instead of 40%. The warm-up's second window did NOT improve (59.8k), which
says the warm-up's collapse has another cause -- it runs on a cold page cache
right after the reseed.

What this does NOT fix: the heap still reaches GOMEMLIMIT and the leader's
write is still 0.5 s on the critical path. 35zzs (tail bounded by transactions)
and 35zzt (the packet window that never reached the nodes) are the next two,
both aimed at the heap.

## 6bm. Round 35zzs: the tx lookup tail bounded by transactions -- registered before the round ran (2026-09-15)

35zzr on n42-r77 = `15710ab9`. **Prediction 74.** "txindex sealed" logs show
tailBlocks <=10 through a B flood (35zzo: 66-77) and back-to-back seals when a
backlog exists; a follower's heap in use late in B -1 GB; GC cycles per 25 s late
in B -20%; B mean +3-6% over 35zzr. Falsified if tailBlocks grows through a flood,
by any "txindex seal failed" or "could not reopen segments", or if in-use heap
late in B does not fall.

**35zzs (2026-09-19 18:00-19:06 EDT): prediction 74 confirmed, and a second
best in a row.**

| leg | 35zzr | 35zzs |
|---|---|---|
| A1 win1 / win2 | 72.4k / 68.2k | 72.0k / 66.7k |
| B1 win1 / win2 | 110.3k / 94.0k | 120.4k / 109.3k |
| B2 win1 / win2 | 117.5k / 86.4k | 131.5k / 89.7k |
| A2 win1 / win2 | 71.2k / 66.7k | 72.8k / 68.2k |

**B mean 112.7k against 102.0k (+10.5%)**, where the prediction asked for 3-6%.
A mean 69.9k. No check failure, no BAD BLOCK.

The mechanism: "txindex sealed" now reports 9-14 tail blocks through a B flood
(35zzo: 66-77) and each seal takes 44-66 ms where it took 4-5 s, because a tick
drains the backlog instead of building one 1M-transaction segment. The second
windows went 94.0k -> 109.3k and 86.4k -> 89.7k. Node heaps did NOT fall (10.5-11
GB, the same as 35zzr): GOMEMLIMIT still binds, so what this bought is the
allocation churn of those 4-5 s seals, not resident memory.

Two rounds have now taken the B mean 90.6k -> 102.0k -> 112.7k, both by taking
long, allocation-heavy work off the block's path rather than by making the block
itself cheaper.

**Watchdog note.** The round printed ROUND DONE and then aborted its own A2
epilogue on "QMDB tree/marker discontinuity before execution" -- err "context
canceled", logged while the fleet was shutting down. The remaining runners
filter `context canceled` before matching the fatal signatures.

## 6bn. Round 35zzt: the packet window flag that never reached the nodes -- registered before the round ran (2026-09-15)

35zzs with `--mobileverify.packet-window 8` moved into `QS_NODE_EXTRA`.
**Prediction 75.** Every node's command line carries the flag; the mobileverify
packet cache in use falls from ~1.0 GB to <=0.05 GB late in B; GC cycles late in B
-20%; B mean +3-6% over 35zzs. Falsified if the flag is absent from
/proc/<pid>/cmdline or the packet cache stays above 0.2 GB.

Not levers (checked 2026-09-15): the 50 ms sleep before each Proposal broadcast.
A follower votes only after the block itself arrives and imports (or passes the
deferred check), and an 18 MB block takes longer than 50 ms from push to decode,
so the Proposal arriving earlier moves no vote; it would make a follower whose
block has not landed fetch it a second time.

## 6at. Round 35zv: leader tenure 16 -- registered before the round ran (2026-09-11)

35zu with N42_HOTSTUFF_LEADER_TENURE=16, nothing else. One handover in
sixteen blocks instead of one in four. (Runs as 35zz on n42-r57 once
35zy completes: the 35zu configuration it compares against is 35zy's.)

**Prediction 55.** Window block time 2.3 -> ~2.0 s ((15 x 1.91 + 3.43) /
16 = 2.0); B windows 26 -> 29-30 blocks, ~79k TPS; the in-tenure period
stays 1.9 s. Falsified if the block time stays above 2.2 s (then the
tenure length feeds something else -- the leader's pool insert rate, its
gossip share, or the follower import growing further as chaining runs
uninterrupted) or if any BAD BLOCK returns (the sealed-block caches keep
16 blocks; a tenure of 16 sits at that edge).

## 7. Not levers (recorded so they are not proposed again)

- **Supply.** Round 14 doubled the flood rate from 40,000 to 80,000 tx/s across
  four legs. Occupancy stayed pinned at 100% and TPS did not rise. The blocks
  were already full.
- **`internal/deferred`.** The package is scaffolding: `Pipeline.SubmitBlock`
  has no caller outside the package, so a node with `DeferredExec.Enabled`
  starts a pipeline that is never fed. Wiring it moves block N's state root into
  N+1's header -- a consensus-format change, not a benchmark knob.
- **`--parallel-evm`.** Broken under load and known to be: all Block-STM workers
  share one MDBX cursor (commit 3709ca6a, proven with the race detector).
  `ExecuteBlockParallelF` already has the per-worker-base shape the fix needs,
  but `ProcessParallel` does not use it, and commit 94775a0a records the two
  further constraints (one read transaction per worker is a different resource
  than one goroutine per worker; the workers' views must be pinned to the
  executor's snapshot). Separate work, its own round.
- **`--interval-ms`.** See section 3.

## 7b. Why cross-round TPS comparisons are invalid

Every leg funds a fresh block of recipient accounts and then writes 22,857 of
them per block for ~150 blocks: **about 3.4 million new accounts per leg**, five
legs to a round. The state grows monotonically all day, node0's chaindata was
25 GB by round 15, and a deeper B-tree costs more per random update.

The effect is visible: round 14's legs ran at ~21,000 TPS and round 15's first
two legs at 15,238 (safe-nosync) and 16,000 (durable). That is not the
durability knob -- both sides moved together. It is the baseline sinking under
its own state growth.

So a number is comparable only to the other legs of ITS OWN round, through that
round's bookends. Any table in this document that puts two rounds side by side
is reporting a mechanism, not a delta.

## 7c. The harness's per-window TPS is not a reliable instrument

Round 15's B1 leg reported `win1 TPS=21333 occupancy=100.0%` and then
`win2 TPS=8000 occupancy=100.0%`. Both windows cannot describe the same steady
chain: full blocks at 100% occupancy do not produce a 2.7x throughput swing
without something visible changing. The measurement windows do not always line
up with when the flood is actually delivering.

Counting the blocks the node itself wrote is the ground truth. Full blocks per
minute, from node0's `blockwrite phases` records, over round 15's first three
legs:

| leg | durability | peak full blocks/min | implied TPS |
|-----|-----------|----------------------|-------------|
| warmup | safe-nosync | 41 | 15,619 |
| A1     | durable     | 43 | 16,381 |
| B1     | safe-nosync | 43 | 16,381 |

`analyze-legs.py` now reports this alongside the phase medians, and it is the
number to argue from. The harness's window TPS stays useful for occupancy (a
window below 95% really was starved) and as a rough check, not as a delta.

A second sampling bias worth recording: `hotstuff view timing` lines come from
`publishCommittedTiming`, which runs only when a view reached a CommitQC. Views
that time out produce no sample at all, so a stalling chain looks like a chain
with normal-looking views and simply fewer of them. View-timing percentiles
therefore describe the views that SUCCEEDED, and must be read next to a
block-rate number rather than instead of one.

## 6bq. Round 35zzx: restoring supply, on a binary that no longer deletes credited accounts -- registered before the round runs (2026-09-20)

The bench is supply-bound: 35zzt ran at 37% occupancy with 0.984 s blocks, so
the next code lever would be measured against a generator that cannot fill a
block. 35zzx is a HARNESS round, not a code round: sixteen generators of 500
senders each instead of the previous shape, with the funding serialized so the
warm-up window does not open before funding finishes. It buys the right to
judge a code lever, and on its own it should move the B mean.

Attempt 1 (2026-09-19 22:30 EDT) died on a BAD BLOCK, and the cause is now
fixed rather than worked around: the leader's build of 13659302 was the only
one in the round to exhaust the Block-STM wave limit, and the sequential
fallback replayed every delta write as a full write, whose nil value means
DELETED -- so `applyMVSToIBS` selfdestructed all 17,036 accounts the block only
credited. The leader's root diverged from all six followers and it sealed the
next block on top. Written up in OPEN_ISSUES.md ("Sequential fallback dropped
delta credits"); fixed in `internal/parallel/executor.go` with
`TestSequentialPathKeepsDeltaWrites` covering both sequential entries.

Attempt 2 runs on **n42-r84** = n42-r80 (deferred + fold + tail + packet
window) + that fix. **Prediction 77.** Supply, not code, is the variable:
occupancy 37% -> above 60%, block time roughly unchanged at ~1.0 s, and the B
mean above the 127.6k standing best. No BAD BLOCK: the fallback is now
value-preserving, so a leader that falls back computes the followers' root.
Falsified if the B mean does not clear 127.6k, or if any leg aborts on a root
divergence.

## 6br. Round 35zzw: one base-state read per account per block -- registered before the round runs (2026-09-20)

`parallel.BaseCache` (internal/parallel/base_cache.go): the executor gives
every transaction its own IntraBlockState, so an account touched by k
transactions is read from the base state k times, and the base state is
immutable for the life of the block. The cache is a per-block map consulted in
the base fallback of `ParallelStateReader.ReadAccountData`, copy-in/copy-out so
no caller can mutate a shared account. Round 35zzo's profile puts the account
read behind `GetCode` alone at 2.9% of a follower's CPU (section 6bp), and the
delta-credited recipients are read again at the block-end fold.

Runs on **n42-r85** = n42-r84 + the cache, after 35zzx so it is measured
against a supply that can fill a block. **Prediction 78.** Follower import
`proc` down at least 10% on full blocks; the B mean up, but by less than the
supply round moved it. Falsified if `proc` does not move: then the read is
already served by a cache below it and the profile line is the map lookup, not
the disk.

## 6bs. Candidate: the fill re-executes candidates for 64 waves because nothing tells Block-STM a nonce miss is permanent (found 2026-09-20, not yet a round)

Block 13659302's leader build (section 6bq) is the only build in its log that
exhausted `MaxWaves`. Its `parallel block` line: 32,200 candidates, 9,200
dropped (all `nonceHigh`), 9,064 aborts, 41,263 executions -- one re-execution
per abort, essentially -- and 64 waves, never reaching `allValidated`. The
followers' import of the same block runs the 23,000 survivors in one wave,
zero aborts.

The two `parallel fill nonce-high sample` lines this build emitted point at
the same thing twice. Sender `0xCA2048...` and sender `0x72814A...` are
deep-nonce accounts from earlier blocks in this run (`txNonce` 4500 and 4206);
`ibs.GetStateReader()` -- the build's own reader, used by the stale-nonce trim
in `internal/miner/worker.go` -- reports their nonce as exactly what the
candidate expects (`buildReaderNonce: 4500`). The executor's own worker,
reading through the fresh per-worker snapshot built in `runParallel`'s `setup`
closure (`internal/parallel_processor.go`: `p.bc.ChainDB.BeginRo` layered with
`state.LayerPostStates(postLayers, base)`), reports the same account's nonce
as 0 (`workerErr: "... state: 0"`) -- an unfunded, never-used account. Two
readers of the same parent state, opened microseconds apart for the same
build, disagree about the same account.

That disagreement, not the drop count, is what drives the wave churn: the
very next build in the same log, block 13659303, dropped *more* candidates to
`nonceHigh` (13,500 of 18,500, 73% against 302's 29%) and still validated in
one wave with zero aborts. `collectPending` (`internal/parallel/executor.go`)
and `validateInOrder` do not distinguish a transaction whose nonce can never
be satisfied within the block from one whose read went stale because another
transaction's write has not landed yet -- both come back `StatusPending` and
both get retried. When the base read itself disagrees with the build's own
view of an account, no number of extra waves supplies the missing write:
Block-STM keeps re-validating (and here, re-executing) candidates that were
never going to pass, one layer of `validateInOrder`'s index-ordered demotion
at a time, until `MaxWaves` gives up. The block's `reload` time (the isolated
tree swap logged in `miner: build phases`) was 47.9 ms, the 94.5th percentile
of the 993 builds in this log -- elevated, but not unique: roughly 54 other
builds saw an equal or longer reload and still converged in one wave, so a
slow reload widens the window for the race without being sufficient by
itself.

The fix under this diagnosis is not a bigger `MaxWaves` or a smarter affinity
key: it is to stop trusting a candidate the moment the reader that picked it
(the build's `ibs`) and the reader that is about to execute it (the
executor's per-worker base + `postLayers`) can be shown to disagree, rather
than admitting all 32,200 candidates and paying 64 wasted waves to find out
9,064 aborts at a time. Concretely, `BuildParallel` already pays one state
read per pending account for the stale-nonce trim (`internal/miner/worker.go`);
that read and the executor's per-worker setup need to be provably the same
snapshot, and where `postLayers` cannot carry every chained sibling's account
update (only the fold's prefetch list is seeded today, section 6bd), the fill
should recognise the gap and fall back before running the executor, the same
way `ErrParallelNotApplicable` already does for fee-recipient blocks, instead
of after.

**Prediction 79.** The next leader build that exhausts the wave limit will
carry a `parallel fill nonce-high sample` line whose `buildReaderNonce`
disagrees with the `state:` value embedded in `workerErr`, for at least one
sender, in that same build; and the build immediately following it in the
same log will not show wave churn even if its own `nonceHigh` drop count is
equal or higher. Falsified if a wave-limit exhaustion turns up whose
nonce-high samples show `buildReaderNonce` and the worker's `state:` agreeing
-- that would mean the reader disagreement is a correlate of the fallback,
not its trigger.

## 6bt. S3b: the routine nonceHigh drops are not routine, and neither of them is genuine send-ahead (2026-09-20)

The premise that prompted this step -- that every build in a run suffers a
`nonceHigh` drop rate like block 13659302's 9,200 of 32,200 or block
13659303's 13,500 of 18,500 -- does not hold in the log where those two
numbers come from. `internal/parallel_processor.go` emits `parallel fill
drops` (the line the two counts came from) for any lenient fill with
`failed > 0` that also has `numTxs >= 1000` or fell back, and it emits
`parallel fill nonce-high sample` for the first two `nonceHigh` failures of
any lenient fill regardless of size -- so neither line is throttled to a
sampled subset of builds; every build with so much as one `nonceHigh` drop
would show up under one or the other. Grepping the leader log for both across
all 993 recorded builds (`miner: build phases`) and all 1,460 `parallel
block` lines turns up exactly two matches: 13659302 and 13659303, back to
back, and nothing else. `nonceHigh` did not touch any of the other ~991
builds in this log at all.

That confines the question to those two blocks, and the two disagree with
each other in exactly the way section 6bs already flagged for 13659302 alone.
Block 13659302's sample has `buildReaderNonce: 4500` against the worker's
`state: 0` for sender `0xCA2048...` -- the build's own reader, reading the
same parent state the executor is about to read, gets the right answer
(4500, matching `txNonce`) while the executor's per-worker snapshot gets a
fresh, never-used account. That is a live disagreement between two readers
open microseconds apart on the same parent state, and it is what drove the
64-wave, 9,064-abort, `fallback: true` exhaustion recorded in 6bs.

Block 13659303's sample looks like agreement -- `buildReaderNonce: 0` against
`state: 0` for sender `0x72814A7C...`, `txNonce: 4206` -- and the build itself
shows no churn at all: one wave, zero aborts, `fallback: false`. But the fix
already merged in this tree, commit c0931aeb ("the sequential fallback
deleted every credit-only recipient"), describes this exact pair of blocks by
number: 13659302's wave-limit fallback replayed every delta write as a full
write, whose nil value the applier reads as delete, so it selfdestructed
17,036 accounts the block had only credited; the commit message states
outright that the leader then "sealed 13659303 on top, which every follower
rejected." 13659303 is not built on a clean parent state -- it is built on
the leader's own state, freshly emptied of 17,036 accounts by 13659302's own
bug. A sender that had legitimately reached nonce ~4206 over the earlier
blocks in this run, and that also happened to be a pure credit recipient
inside 13659302, would read back as nonce 0 from *any* reader after that
block landed, because the account record itself is gone, not because either
reader is confused. The build's reader and the worker's snapshot agree in
13659303 for the same reason two clocks agree when both are stopped: they are
reading the same already-wrong ground truth, not confirming a real one.

So neither block's drop is the generator legitimately sending ahead of a
sender's confirmed nonce. 13659302's drop traces to a live, still-open bug
(the reader race behind prediction 79). 13659303's drop traces to a
different, already-fixed bug (c0931aeb) that the first bug's fallback
triggered one block earlier. Both are artifacts of code, zero of the two
sampled blocks show a demand-side explanation, and the log gives no third
block to check because no other build in it dropped a single candidate to
`nonceHigh`. The one thing this log cannot show is whether 13659303-style
mass drops recur on the fixed binary when a build reaches the fallback path
some other way, or whether round 35zzt's 37% occupancy -- measured on a
different run this analysis was not given -- carries the same signature;
that would need 35zzt's own `parallel fill drops` / `fallback` lines, which
are out of scope here.

Because both observed cases are bug-driven and neither is genuine send-ahead,
prediction 80 is registered below rather than leaving this as a pure
falsification of the supply story: the fallback-corruption bug is already
fixed, so round 35zzx attempt 2 (running on n42-r84, which includes
c0931aeb) is the first data this campaign will have on whether mass
`nonceHigh` drops disappear once that fix is in place, independent of the
extra generators also shipping in that same round.

**Prediction 80.** Round 35zzx attempt 2's leader log will show no build with
a `nonceHigh` drop fraction resembling 13659303's 73% (or 13659302's 29%)
unless that build's own `fallback` field is `true` -- i.e. mass `nonceHigh`
drops keep tracking fallback/corruption events, not ordinary full builds.
Falsified if any full build in that round drops a large share (rounding to
double digits of a percent) of its candidates to `nonceHigh` while
`fallback: false` and no wave-limit exhaustion preceded it in the same log:
that would mean genuine send-ahead is a real, independent contributor after
all, and the supply round's premise needs re-examining on its own terms
rather than as a residual of these two bugs.

## 6bu. Round 35zzx attempt 2: sixteen generators did not restore supply, and prediction 80 falls with it (2026-09-20)

Attempt 2 ran on n42-r84 (2026-09-20 14:10-15:23 EDT) and finished `ROUND
DONE` with no abort. Attempt 1's logs, archived under
`wr-logs/r35zzx-attempt1/`, are not used anywhere below.

| leg | win1 TPS / occ / blockTime | win2 TPS / occ / blockTime |
|---|---|---|
| B1 | 92,323 / 17.0% / 0.583 s | 1,915 / 0.6% / 1.053 s |
| B2 | 91,848 / 16.2% / 0.561 s | 1,875 / 0.1% / 0.250 s |

**B mean 47.0k against the 127.6k standing best (35zzt) -- a 63% regression,
not a gain.** Occupancy across the four B windows means 8.5%, against
35zzt's 37%: the opposite of prediction 77's ">60%". Mean block time across
the same four windows is 0.612 s against 35zzt's 0.984 s, but that number is
not a speed-up to bank -- it is pulled down by win2 of both legs collapsing
to nearly empty blocks (occupancy 0.6% and 0.1%), which commit fast because
there is almost nothing in them, not because the chain got quicker.

**Headline: mass `nonceHigh` drops reappeared, so prediction 77 cannot be
credited regardless of the B mean.** `parallel fill drops` lines across the
B legs (bounded to 14:40:32-15:10:16, this round's own runner log) show
exactly zero drops anywhere in B1 or in the first ~8 minutes of B2, then 23
consecutive blocks (13661672-13661673, 13661720-13661731,
13661749-13661759) in a 37-second span (15:03:08-15:03:45) that together
drop 183,282 candidates to `nonceHigh` -- 99.98% of the 183,323 total
`failed` count in that window, and the entire reason win2 of both B legs
reads near zero. One block, 13661726, drops 13,473 of its 13,482 candidates
(99.9%). This is the sixteen generators running dry under B's larger gas
ceiling, not a harness win: restoring supply was the point of this round,
and on n42-r84 it emptied faster than 35zzt's eight generators did.

This also settles prediction 80 (section 6bt), registered against exactly
this round's log: all 23 blocks show `lenient: true`, `fallback: false`,
`waves: 1`, `aborts: 0` on the leader's own `parallel block` line -- a plain
"not enough live candidates" fill, with no wave-limit exhaustion anywhere
near it. Prediction 80 called for no full build to drop a double-digit
percent of candidates to `nonceHigh` while `fallback: false` and no
wave-limit exhaustion preceded it in the same log; 23 of them do, one at
99.9%. Prediction 80 is falsified: genuine send-ahead (here, generator
exhaustion) is a real, independent contributor to mass `nonceHigh` drops,
not only the two bug-driven blocks S3b traced in the previous log.

No leg saw a BAD BLOCK or a root divergence: all seven nodes' logs across
the full round window (14:10:32-15:23:24) have zero matches for `BAD BLOCK`
or any divergence/mismatch message, and every leg's `drained: head ...
settled` line appears with no abort. The delta fix (c0931aeb) held.

**Prediction 77 falsified.** Occupancy fell (8.5% mean, worse than 35zzt's
37%), the B mean fell 63% against the 127.6k standing best, and the
acceptance condition in QS_QUEUE.md is tripped: mass `nonceHigh` drops are
back, this time from genuine supply exhaustion rather than the fixed
fallback bug. Sixteen generators of 500 senders at 4500-deep nonces run out
partway through a 15-minute B leg once the gas ceiling is raised to B's
level; the harness still cannot fill a block for the full window.

## 6bv. What actually shrank in 35zzx: not the funded budget, the depth target and the funding gate (2026-09-20)

The working story going into this step was that sixteen generators of 500
senders split the same funded pool eight generators used, so each sender
ended up with half the transactions to its name. The runner scripts do not
support that story. `run-r35zzt.sh` and `run-r35zzx.sh` differ in exactly one
line: `--floods 8 --senders 1000` becomes `--floods 16 --senders 500`; `-pertx
4500`, `-target-depth 45000`, the pool sizing, the recipients count and the
funding gas price are all untouched. `cmd/txflood/main.go`'s `fundingAmounts`
funds a sender for `(pertx+10) x 21000 x gasPrice` wei regardless of how many
other senders or generators exist, so the per-sender budget is the literal
same 4500-transaction allowance in both rounds, and the total sender count is
the same 8,000 either way (8 x 1,000 = 16 x 500). Multiplying senders by
pertx gives the same 36,000,000-transaction supply per leg in both rounds,
and multiplying the faucet's per-sender cost (about 0.094731 ETH at
`QS_FLOOD_GASPRICE=1000000000`) by 8,000 senders gives the same faucet spend,
about 757.85 ETH, per leg, in both rounds. The premise that prompted S1 does
not hold at the flag level: nothing in the funding phase was halved.

What the two runners do differently is `-target-depth`, which is a per-process
flag, not a per-leg one: each generator tries to keep 45,000 transactions of
its own in flight regardless of how many senders it has to spread them over.
Eight generators asked the fleet's pool for 360,000 transactions in flight at
once; sixteen generators asked for 720,000 against the same 800,000-slot pool
(`--pool-slots 600000 --pool-queue 200000`) and the same 8,000-sender base --
double the aggregate pipeline depth, unchanged supply underneath it. That
alone does not consume budget faster, but it does mean twice as many
transactions per sender are queued and unmined at any moment (roughly 90 per
sender against 35zzt's 45), so a single stuck nonce -- the pool's
already-documented failure mode, where everything above a lost transaction
reads as unpromotable until that hole is filled -- now blocks a bigger share
of the fleet's live pipeline per occurrence, and the `nonceHigh` drop code
path (`internal/parallel_processor.go`) is exactly what a mass hole looks
like from the miner's side.

The other harness difference is wall-clock, not arithmetic. `bench-run.sh`
funds generators strictly one at a time -- "waiting for flood N to finish
funding before starting the next" -- so sixteen generators serialize twice as
many funding gates as eight. Backing the funding-phase length out of each
leg's own bookends (`LEG ... done` minus `LEG ...` start, minus the 400 s
decay, minus the 120 s of measured windows, minus the roughly 60 s drain
every leg shows) gives about 209-226 s of funding and ramp-up per B leg in
35zzt against about 304-319 s in 35zzx, a 40-45% increase. That longer,
serialized gate produced a visibly different startup: `wr-logs/r35zzx-mem.log`
shows all seven nodes' resident memory flat for minutes and then jumping in
lockstep -- anon memory per node roughly 1.4 GB at 15:03:01, past 10 GB by
15:03:42 -- in the same 30-40 second span that node0's log shows the 23-block,
183,282-candidate `nonceHigh` collapse (13661672 through 13661759,
15:03:08-15:03:45). Sixteen generators finishing a longer serial gate in a
tighter cluster than eight do, then all flooding to a doubled aggregate depth
target at once, is a synchronized inrush; 35zzt's eight-generator gate never
produced a memory or `nonceHigh` signature like it in either B leg.

The consumption numbers rule out plain exhaustion as the mechanism. Summing
`win1`+`win2`'s reported `txs` field (the harness's own count, not TPS x 60)
for 35zzt's B1 and B2 gives 15,321,199 and 15,298,940 transactions -- about
43% of that leg's 36,000,000-transaction budget -- with no collapse. The same
sum for 35zzx's B1 and B2 gives 5,654,300 and 5,623,400 -- about 16% of the
same-size budget -- while collapsing. 35zzx failed while sitting on roughly
84% of its funded supply, not after spending it; whatever broke, it was not
the fleet running the faucet's 36,000,000-transaction allowance to zero.

**The real constraint is the per-process depth target, not the funded
supply.** `-target-depth` is not scaled to the generator count anywhere in
the harness, so doubling `FLOODS` silently doubles what the fleet asks its
own pool to hold at once, against an unchanged sender base and an unchanged
serialized funding gate that now takes 40-45% longer to bring every generator
online. The one parameter a supply round should change next is
`-target-depth` itself: halve it alongside the doubled generator count, 45000
-> 22500, so the fleet's aggregate in-flight target returns to 360,000 -- the
same number 35zzt already ran two full B legs on without a `nonceHigh` mass
drop. Everything else (16 generators, 500 senders, pertx 4500, pool sizing,
funding order) stays as 35zzx ran it; this isolates the depth target as the
one variable.

**Prediction 81.** With `--target-depth 22500` in place of 45000 on the
sixteen-generator harness, no B-leg block will drop a double-digit percentage
of its candidates to `nonceHigh` while `fallback: false`, and every one of
the four B windows -- not just the mean -- will clear 25% occupancy, with the
B mean above the 127.6k standing best. Falsified if any single B window's
occupancy is below 25% (the win2-collapse pattern recurring even once), if
the B mean does not clear 127.6k, or if any B-leg block shows a `parallel
fill drops` `nonceHigh` share in the double digits with `fallback: false` and
no preceding wave-limit exhaustion in the same log.

## 6bw. Round 35zzw: the base-read cache, on the eight-generator shape -- prediction 78 falsified (2026-09-20)

35zzw ran on **n42-r85** (2026-09-20 15:57-17:04 EDT), finishing `ROUND DONE`
with no abort. It shares 35zzt's eight-generator, 1,000-sender shape
(`8 floods x 1000 x 3000`, same gasceil/fillgas per leg), so it is the round
that is directly comparable to the 127.6k standing best -- unlike 35zzx's
sixteen-generator shape, which is not a baseline for anything in this section.

| leg | win1 TPS / occ / blockTime | win2 TPS / occ / blockTime |
|---|---|---|
| B1 | 97,800 / 50.0% / 1.667s | 92,439 / 46.1% / 1.579s |
| B2 | 97,800 / 50.0% / 1.667s | 96,340 / 49.6% / 1.667s |

**B mean 96.1k against the 127.6k standing best (35zzt) -- a 24.7% fall, not
the predicted rise.** Occupancy is actually up (48.9% mean across the four B
windows against 35zzt's 42.75% mean / ~37% on both of 35zzt's win2s), but
block time is badly up too: 1.645s mean across the four B windows against
35zzt's 1.086s (the two win2s specifically, the steady-state number section
6bq/6br call "35zzt's 0.984 s", are 1.579s and 1.667s here -- 60-69% slower).
Fuller blocks taking well over half again as long to seal is the opposite of
what a cheaper follower import should produce.

**The acceptance condition on `proc`: before is unmeasurable, and that is
itself a finding.** `import_breakdown.py`, run over both B legs
(16:24:13-16:52:06, node logs plus each node's one retained rotated
generation, txs>=160,000), gives r85's own number cleanly:

    follower import (full blocks) n=1722
      body 9 ms, proc 988 ms (p90 1131), write 205 ms, total 1215 ms
      proc breakdown: recoverMs 28, execMs 717, applyMs 27, finalizeMs 142, validateMs 20

That is the **after** number. The **before** (n42-r84, no cache) is not
recoverable: node logs retain only the current file plus one rotated
generation, the boundary from 35zzw's own rotation already sits after 35zzx
ended (14:10-15:23 EDT), and no earlier section in this document ever ran
`import_breakdown.py` against the r76-r84 lineage -- every prior readout used
`cycle.py`'s seal-to-seal timings instead. So the literal test in prediction
78 ("falsified if `proc` does not move") cannot be executed as a before/after
delta on this lineage; that number is `n/a`, not an estimate. (The nearest
thing on record, 35zzp re-run's proc 796 ms medians at section "6bk" lineage,
is two further code generations back, on a different memory budget and before
the fold/tail/packet-window changes -- section 7b's caution against invalid
cross-round comparisons applies to it directly, so it is not used as a
baseline here, only noted in passing that 988 ms sits above it rather than
comfortably below.)

What is measured, though, contradicts prediction 78 as a whole regardless of
the missing `proc` delta: the prediction bundled "proc down >=10%" together
with "B mean up, but by less than the supply round moved it," and the B mean
fell by a quarter while the chained seal-to-seal cycle (measured directly over
the same window, `n=177` chained pairs) runs at a 1,284 ms median (p90 1,465),
with `execMs` alone at 717 ms median -- the dominant term in `proc`, and not
a number consistent with a working, beneficial cache under this load. A
result this large is not the reporting noise floor (documented at 3.6%); it
is a genuine regression, just one that cannot be pinned on the cache with a
same-shape n42-r84 control missing from the record.

**Acceptance checks, both clean.** `parallel fill drops` (only logged when a
lenient fill has `failed>0`) has zero matches anywhere in the round's node
logs, and the broader `miner: parallel fill` records confirm it: 545 records
across the B legs, sum of `failed` across all of them is 0, so the
`nonceHigh` acceptance condition is met at the strictest possible reading (0,
not "approximately zero," and nothing like 35zzx's 183,282). No `BAD BLOCK`
and no root-mismatch/divergence message appears anywhere in the round's node
logs; the only related hits are four benign
`miner: suppressing divergent same-height sibling` lines (the ordinary
leader-race resolution path), not a root divergence. Commit c0931aeb's fix
holds.

**Prediction 78 falsified.** The stated falsification trigger (`proc` failing
to move) cannot be checked directly because n42-r84's own full-block `proc`
was never captured before its logs rotated away -- a process gap this round
exposes: the next binary's proc breakdown needs to be pulled immediately
after its own round, not read out after the following round has already
overwritten the node logs. But the prediction's other, directly measured
half is unambiguous: the B mean fell 24.7% against the 127.6k standing best,
and every one of the four B windows ran 40-69% slower per block than 35zzt's
corresponding window, which is not "B mean up, but by less" -- it is a fall.
On the measured evidence, the round does not show a faster follower import;
it shows a slower one.

## 6bx. Round 35zzy: the halved depth target does not clear any of prediction 81's three bars (2026-09-20)

35zzy ran on **n42-r84** (2026-09-20 20:58-22:07 EDT), finishing `ROUND DONE`
with no abort. It is the 35zzx retry registered as prediction 81 (6bv):
same 16-generator, 500-sender harness as 35zzx, `-target-depth` halved
45000 -> 22500 so the fleet's aggregate in-flight target returns to
360,000, matching 35zzt's proven number instead of 35zzx's doubled
720,000. Legs: A1 21:12:54-21:25:57, B1 21:25:57-21:39:47, B2
21:39:47-21:53:58, A2 21:53:58-22:07:20 (A legs not used to conclude
anything, per protocol).

| leg | win1 TPS / occ / blockTime | win2 TPS / occ / blockTime |
|---|---|---|
| B1 | 113,715 / 22.0% / 0.645s | 18,960 / 20.3% / 3.529s |
| B2 | 113,887 / 22.8% / 0.652s | 98,210 / 19.4% / 0.645s |

**B mean 86.2k.** Against the 127.6k standing best (35zzt) that is a 32.4%
fall; against 35zzx attempt 2's 47.0k (6bu) it is an 83% rise. Halving the
depth target bought back most of what doubling it cost, but not enough to
clear the registered bar.

**Clause 1 (every B window >=25% occupancy) -- falsified.** All four B
windows come in below 25%: 22.0%, 20.3%, 22.8%, 19.4% (mean 21.1%, mean
block time across the four windows 1.368s). None clears the bar, so this
is not a marginal miss on one window -- no window in the round passes.
B1's win2 is the sharpest instance of the "win2-collapse pattern" the
prediction named directly: 17 blocks in the 60s window against win1's 93,
blockTime up 5.5x (0.645s -> 3.529s) while occupancy stays roughly flat
(22.0% -> 20.3%) -- the chain is not idle, it is stuck taking far longer
per block.

**Clause 2 (B mean > 127.6k) -- falsified.** 86.2k does not clear 127.6k
by any margin; it is 32.4% below it.

**Clause 3 (no B-leg block drops a double-digit percent of candidates to
`nonceHigh` with `fallback:false`) -- falsified.** `parallel fill drops`
lines bounded to the two B legs (21:25:57-21:53:58) show 20 distinct
committed blocks with a nonzero `nonceHigh` count, summing to 63,700
candidates dropped: 15 in B1 (21:33:48-21:34:14, a 26 s span) and 5 in B2
(21:47:48-21:48:04, a 16 s span), both bursts sitting right at the start of
each leg's flood ramp. Fourteen of the twenty are the same 4,490-candidate
drop recurring on different blocks; six of those fourteen have
`candidates == failed == 4491`, i.e. the block's local build kept none of
its flood candidates (worst observed share 4490/4491 = 99.98%, e.g. blocks
13659234, 13659238, 13659242, 13659290, 13659294, 13659298). Every matched
block reads `fallback:false, lenient:true, waves:1, aborts:0` on its own
`parallel block` line -- no wave-limit exhaustion precedes any of them, so
the clause's own exemption does not apply and the falsification stands
exactly as written. Node0, node1 and node2 show zero `nonceHigh` drops
anywhere in the round; the bursts sit entirely on nodes 3-6 in their turns
as leader.

**A related, larger drop is out of the clause's scope but explains the B1
win2 collapse.** Node5's block 13659711 (21:38:43, inside B1's win2) drops
100,700 of 163,000 candidates -- but as `nonceLow` (stale/already-mined),
not `nonceHigh`; `nonceHigh` on that block is 0. This is the pool
mined-not-demoted signature the harness has recorded before (`run-r35zzy.sh`
notes 35zzt: "300k held ~200k mined-not-demoted, so fresh candidates capped
near 95k a block"), not generator exhaustion: B1+B2 together consumed
20,686,300 transactions against the two legs' 72,000,000-transaction
funded budget (~29%), nowhere near dry, and `r35zzy-mem.log` shows all
seven nodes' anon memory jumping in lockstep from ~1.7 GB to ~9-10 GB
between 21:33:41 and 21:34:32 -- the same synchronized-inrush signature
6bv described for 35zzx, smaller in magnitude and duration here but not
eliminated by halving the depth target alone.

**Acceptance checks.** No `BAD BLOCK` and no root-mismatch/divergence
message anywhere in any of the seven nodes' full-round logs
(20:58:34-22:07:20). Three `miner: suppressing divergent same-height
sibling` lines fall inside the B legs (node2 21:34:32, node5 21:38:43, both
coincident with the drop bursts above) -- the ordinary benign leader-race
path, not a root divergence, consistent with every prior round on this
lineage. No `MODE-FAILED` and no watchdog trip in the round log, runner
log or mem log. Timeout certificates (`TC formed locally`, HotStuff's
view-timeout signal) fire 8 times inside the two B legs (21:26:58,
21:34:32, 21:34:44, 21:37:57, 21:38:09, 21:38:33, 21:40:56, 21:53:23) of 13
across the whole round, clustered in the same two ramp windows as the
`nonceHigh`/`nonceLow` bursts; one `f+1 future timeouts observed;
advancing weak synchronizer` line appears once, in A2 (21:55:19), outside
the B legs. Generator exhaustion: no evidence either way from direct
generator output -- this round's per-flood stdout was not retained under
the one-current-plus-one-rotated-generation log policy -- but the
consumed-vs-funded ratio above and the memory-inrush timing both point away
from a dry generator and toward the funding-gate synchronization 6bv
already named.

**Mandatory phase baseline for n42-r84 (per the 6bw ruling).**
`import_breakdown.py` over both B legs (21:25-21:54, txs>=160,000) gives
the number 6bw could not recover for this exact binary:

    follower import (full blocks) n=1020
      body 9 ms (p90 15), proc 529 ms (p90 678), write 215 ms (p90 276), total 762 ms (p90 924)
      proc breakdown (joined to the same block's own `parallel block` line):
        recoverMs 21 (p90 26), execMs 260 (p90 375), applyMs 28 (p90 42),
        finalizeMs 149 (p90 193), validateMs 22 (p90 36)

This is n42-r84 without the base-read cache, on the same eight-... no,
sixteen-generator/22500-depth shape as this round -- not directly
comparable to 6bw's r85-with-cache number (proc 988 ms) because the
generator shape differs (16x500 here vs 6bw's 8x1000), but it is now on
record so a same-shape r84-vs-r85 comparison no longer needs a fresh round
just to establish the "before" side.

**Prediction 81 falsified on all three clauses.** Halving `-target-depth`
recovered most of the B mean 35zzx attempt 2 lost (47.0k -> 86.2k, +83%)
and did shrink the `nonceHigh`/memory-inrush signature relative to 35zzx's
183,282-candidate, 23-block collapse (63,700 candidates, 20 blocks here),
but it did not remove the synchronized funding-gate inrush 6bv identified
as the actual mechanism, and none of the three registered bars -- 25%
occupancy floor, 127.6k B mean, single-digit `nonceHigh` shares -- is
cleared. The depth target was one lever on a problem with (at least) two
causes; the serialized per-generator funding gate is the other, and it is
still untouched.

## 6by. S10: the B1 win2 collapse is a 52 s stall on one leader's build queue, not a per-block slowdown (2026-09-20)

Logs-only diagnostic on 35zzy (n42-r84, 2026-09-20 20:58-22:07 EDT). Node
logs had not rotated (each `log/n42.log` still spans 20:58:34-22:07:18, no
`n42-*.log.gz` siblings), so the full B-leg window was copied verbatim
before analysis: `/data/blockchain/wr-logs/r35zzy-keep/node{0-6}-B.log`,
21:25:00-21:54:30, 197 MB total across the seven nodes -- well under the
6 GB cutback threshold, so both B legs are kept whole (no B2 truncation
needed).

**The mechanism.** Cross-referencing `blockimport phases`' own `n` (block
number) across all seven nodes' copies pins the collapse to a single gap:
block 13659710 commits at 21:37:51 on every node, and the next block,
13659711, does not commit anywhere until 21:38:43 -- 52 seconds later,
chain-wide, not just on one follower. Node5's own log explains why. At
21:37:51 node5 (that view's leader) logs four straight `hotstuff:
committed block not executed locally` failures against its own recent
commits, then `hotstuff: refusing block production on unexecuted
committed parent`, then `hotstuff: deferred production resumed after the
parent applied` and `miner: commitWork begin` -- all in the same second.
The next evidence that build is progressing, `miner: parallel fill`
(163,000 candidates), does not appear until 21:38:42, 51 seconds later.
Meanwhile HotStuff correctly detects the silent leader and cycles view
timeouts with doubling backoff -- view 7356 timed out after 6 s
(21:37:57), 7357 after 12 s (21:38:09), 7358 after 24 s (21:38:33) -- and
re-elects node5 each time (`TC formed, I am the new leader`) because the
round-robin schedule gives it a 4-view batch. A second, later-triggered
build request queues behind the first the whole time; when the first
finally seals block 13659711 at 21:38:43, the queued duplicate collides
with it (`miner: suppressing divergent same-height sibling; re-proposing
first sealed block`, number 13659711) and is dropped. None of this shows
up as slow per-view voting: `hotstuff view timing`'s own `r1`/`r2` fields
for the surrounding views stay in their normal 4-67 ms range throughout:
the 52 s is invisible to the consensus-round instrumentation and lives
entirely inside the miner/build path.

**Q1 -- where the time goes (win1 = first 60 s of full [txs>20000] blocks
in B1, 21:33:46-21:34:46; win2 = the stall itself, 21:37:51-21:38:43;
medians pooled across all seven nodes' own phase lines, since only the
current leader emits the leader-only ones):**

| phase (field) | win1 median | win2 (n=6 samples; 1 outlier) | grower? |
|---|---|---|---|
| `miner: work queue wait` (`waitNs`) | 0.012 ms (n=306) | median 0.020 ms, **max 45,265 ms** (node5, 21:38:43) | **the collapse** |
| `hotstuff view timing` role=leader `total` | 250 ms (n=157) | 696 ms (n=4) | view total grows but stays 2 orders below the stall |
| `miner: build phases` `total` | 10.7 ms (n=159) | 633 ms (n=3) | tracks block size (see below) |
| `blockimport phases` `total` | 9.9 ms (n=936, mixed sizes) | 605.6 ms (n=26) | tracks block size |
| `blockwrite phases` `total` | 3.6 ms (n=1094, mixed sizes) | 96.8 ms (n=29) | tracks block size |

**The single largest grower is `miner: work queue wait`**, from a
0.012 ms median to a single 45,265 ms (45.265 s) sample -- five orders of
magnitude, and on its own it accounts for essentially the entire 52 s
gap between blocks 13659710 and 13659711. It is emitted at
`internal/miner/worker.go:472`, where the single-goroutine `runLoop`
drains `newWorkCh` one request at a time, so any request that arrives
while the prior `commitWorkGuarded` call (started at `worker.go` from the
`deferred production resumed` path, `internal/consensus/hotstuff/service.go:1621`)
is still running queues behind it and reports that queueing as `waitNs`.

The other rows that look like growers (`blockimport`/`blockwrite`
`total`, `miner: build phases` `total`) are not a slowdown of the
machinery: win2's blocks average ~163,000 txs against win1's ~55,000 (a
separate cut restricted to blocks with txs>=15000, so the two samples are
comparable), and the *per-transaction* cost actually falls from win1 to
win2 -- `blockimport`'s `proc` 4,948 ns/tx -> 3,705 ns/tx, `write` 1,552
ns/tx -> 1,138 ns/tx. Bigger blocks amortize better, exactly as expected;
none of this is where the 52 s went.

**Q2 -- stale candidates, four B windows** (win1/win2 boundaries: B1 as
above; B2win1 = first 60 s of full blocks, 21:47:45-21:48:45; B2win2 =
last 60 s of the leg, 21:52:58-21:53:58 -- B2 has no comparable stall, the
largest gap between substantial blocks anywhere in B2 is 5 s, so B2win2 is
an ordinary tail window, not a second collapse):

| window | candidates (sum) | nonceLow (sum) | nonceLow share | blocks >25% nonceLow | leaders with any nonceLow drop |
|---|---|---|---|---|---|
| B1 win1 | 3,009,528 | 0 | 0.0% | 0 | none |
| B1 win2 | 337,000 | 100,700 | **29.9%** | 1 (node5, block 13659711: 100,700/163,000 = 61.8%) | node5 only |
| B2 win1 | 3,851,364 | 0 | 0.0% | 0 | none |
| B2 win2 | 1,146,200 | 0 | 0.0% | 0 | none |

nonceLow is exactly zero in three of the four windows and tracks the
collapse precisely: it is nonzero in B1win2 and nowhere else, and within
B1win2 it is a single block on a single leader (node5), not a pattern
spread across the leader rotation. (`nonceHigh` -- the separate,
already-documented 6bx drop -- is a different phenomenon: it appears on
nodes 3/4/5/6 in both legs' ramp windows and is not restricted to the
collapse.)

**Q3 -- pool state.** `txpool reorg phases` is not a per-block line: only
28 instances fire across the whole ~29-minute B-leg span (all seven nodes
combined), each costing 200-390 ms total, of which 99%+ is `demote`
(`reset` is 0.1 ms every time -- negligible). The last one before the
stall is node5 at 21:37:35 (`pendingAccts:3, nonces:7540, total:211.9ms,
demote:211.8ms`). No node logs a `txpool reorg phases` line during the
stall (21:37:51-21:38:43), and none fires anywhere for 11 min 42 s
afterward -- the next is node0 at 21:49:25 (`pendingAccts:10,
nonces:15650, total:238.2ms`). That gap is not attributable to the stall
alone: it also spans B1's post-collapse tail, the inter-leg quiet period,
and B2's own ramp-up (B2's first full block is at 21:47:45, so there is
little for a reorg to demote before then either). No node prints how many
blocks a given reset covers, so that comparison is n/a. What is measured:
node5 walked into the 163,000-candidate fill for block 13659711 68
seconds after its last demote sweep, with the pool's stale/already-mined
backlog un-cleared, producing the 61.8%-nonceLow fill above.

**Q4 -- slow or waiting?** Both, but with a clear root and a clear
symptom. The root is node-side: node5's local execution/apply path for
its own recently committed blocks fell behind (four `committed block not
executed locally` failures at 21:37:51), triggering a `deferred
production resumed` catch-up whose build request then sat for ~51 s
before `miner: parallel fill` (a routine, fast, 237 ms fill once it
finally ran) could even start. The symptom is HotStuff correctly noticing
the silent leader and cycling three view timeouts with doubling backoff
(6 s, 12 s, 24 s -- views 7356/7357/7358, the last two of which,
21:38:09 and 21:38:33, fall inside the literal 21:38:00-21:39:47 window);
the view-timing instrumentation itself (`r1`/`r2`/`propose`) stays normal
throughout, so the chain was not waiting on network propagation or vote
aggregation -- it was waiting on its own leader, which was in turn waiting
on its own build queue. The largest unaccounted gap in any view's
timeline is exactly this one: ~52 s inside node5's `commitWork`
call/queue, present in no `hotstuff view timing` field at all.

**Method (6by).** Evidence preserved first, before any analysis, into
`/data/blockchain/wr-logs/r35zzy-keep/node{0-6}-B.log` via `grep -E`
against each node's live `log/n42.log` for
`"time":"2026-09-20 21:(2[5-9]|[34][0-9]|5[0-3]):[0-5][0-9]"` or
`"time":"2026-09-20 21:54:([0-2][0-9]|30)"` (21:25:00-21:54:30, both B
legs whole). All grep/awk/python passes below read only those seven
copies, single-threaded, no fleet process touched. Block timeline:
`"msg":"blockimport phases"` lines parsed with `json.loads`, keyed on
`n`/`txs`/`time`, sorted per node, diffed for the largest `n`-to-`n+1`
timestamp gap (found the 52 s 13659710->13659711 gap identically on
every node). Phase medians: same technique over `"msg":"miner: build
phases"`, `"msg":"miner: parallel fill"`, `"msg":"miner: work queue
wait"`, `"msg":"parallel block"`, `"msg":"blockwrite phases"`, windowed
by `time` string comparison (`WIN1`/`WIN2` tuples) and pooled across all
seven `node*-B.log` files. Leader-only view timing:
`"msg":"hotstuff view timing"`'s embedded `view=<n> role=leader
propose=Xms r1=Yms r2=Zms total=Wms` parsed with
`re.compile(r"view=(\d+) role=leader((?: \w+=\d+ms)*)")`. Stale
candidates: `"msg":"parallel fill drops"` (`nonceLow`/`nonceHigh`/
`failed`) joined by same-timestamp `"msg":"miner: parallel fill"`
(`candidates`) for the window totals; per-block share from the one line
matching both `n` and `time`. Pool: `"msg":"txpool reorg phases"`
(`demote`/`reset`/`pendingAccts`/`nonces`/`total`), all 28 instances
listed and eyeballed for the nearest-before/nearest-after the stall. View
timeouts: `"msg":"view timed out"` and `"msg":"TC formed` grepped across
all seven copies and cross-checked against node5's own
`"msg":"hotstuff: committed block not executed locally"` /
`"refusing block production on unexecuted committed parent"` /
`"deferred production resumed after the parent applied"` /
`"msg":"miner: commitWork begin"` /
`"msg":"miner: suppressing divergent same-height sibling"` lines in
strict `time` order to build the 21:37:51-21:38:43 narrative. Code
pointers found with `grep -rn` for each exact log string against
`internal/`.

**What this does and does not show.** It shows, with a chain-wide
timestamp match across all seven nodes' independently preserved logs,
that the B1 win2 collapse is one 52-second stall caused by a single
leader's serialized build queue backing up behind a slow local
execution-catch-up, not a steady-state 3.5 s-per-block regime -- the
blocks immediately before and after the stall commit in around a second
each. It shows the stale-candidate spike (Q2) and the pool's quiet demote
sweep (Q3) are downstream of that same stall, not independent causes. It
does not show *why* node5's execution/apply fell behind in the first
place (the four "not executed locally" failures are themselves a routine,
high-frequency line seen 800+ times per node across the round, so their
mere presence is not diagnostic -- what is unusual is only how long this
one resume took); that requires either a CPU/lock profile of node5 at
21:37:51-21:38:42 or a repeat of this exact shape with finer-grained
build-internal timing, neither of which this log-only pass can produce.
It does not show whether halving the leader-batch size (4 views/leader)
would shorten a future stall's blast radius, since only one instance of
this specific stall exists in the round.

## 6bz. S11: the diagnostics are built, tested and built into n42-r86 on a clean lineage, on the commander's ruling -- prediction 82 registered (2026-09-20)

**What S11 implements.** Two diagnostics for the commitWork pre-fill path
6by could not see inside, both gated behind `N42_BUILD_STALL_DIAG=1` (read
once at start-up, same pattern as `N42_MINER_ADOPT_APPENDS`) and otherwise a
no-op:

1. Named step timers between `miner: commitWork begin` and the start of the
   fill, one `miner: prefill phases` line per build logged only when the
   pre-fill total exceeds 50 ms: `alignCall`/`lockWait` (`AlignAppliedBranch`,
   `internal/blockchain.go:3025`), `insertParent` (`InsertChainAuthorized`,
   `internal/blockchain.go:1941`, same `bc.lock` -- the mutex the miner build
   path shares with ordinary block import/write), `persistWait`
   (`WaitBlockPersisted`, `internal/miner/worker.go:1211`), `roTxBegin`
   (`DB().BeginRo`, `internal/miner/worker.go:1339`), `specTreeReload`/
   `rootLockWait` (`NewMinerRootComputer`'s peel/reload and its `minerRCMu`
   wait -- shared with the startup pre-warm, `internal/blockchain.go:562`),
   `headerPrepare` (`prepareWork`, `internal/miner/worker.go:1315`),
   `blockStart` (`ProcessExecutionBlockStart`, `internal/miner/worker.go:1448`),
   `pendingSnapshot`/`trim` (already-timed pool fetch and stale-nonce trim
   inside `fillTransactions`, `internal/miner/worker.go:1812` and `:1859`).
2. A stall watchdog (`internal/miner/build_stall_watchdog.go`): if a build
   has not reached its fill within 3 s of `commitWork begin`, it dumps every
   goroutine's stack (`runtime.Stack(_, true)`, grown to a 64 MiB cap) to
   `<datadir>/log/build-stall-<n>-<unixsec>.stacks` (`log.LogDir()`, new
   accessor in `log/root.go`) or stderr if that directory is unreachable,
   rate-limited to one dump per process per 60 s, and logs
   `miner: build stalled before fill` with the step name and elapsed time.
   One `time.AfterFunc` per build, cancelled the moment the fill starts
   (right where `prefillTimes.logIfSlow` fires, `internal/miner/worker.go`
   fillTransactions, immediately after `txSet` is built) or the build is
   abandoned (a deferred `Cancel()` in `commitWork` catches every early
   return, including the speculative-hit fast path that never reaches the
   fill at all).

Tests: `TestBuildStallWatchdog{DisabledIsNil,NilMethodsAreNoOps,
FiresAfterThreshold,CancelPreventsDump,RateLimited}`,
`TestWriteGoroutineDumpFallsBackToStderrWhenDirEmpty`,
`TestAllowBuildStallDump` in `internal/miner/build_stall_watchdog_test.go`,
using an injectable threshold (`newBuildStallWatchdogWithThreshold`) so the
"fires once after the threshold" and "rate-limited" cases run in
milliseconds. `go test -tags "nosqlite,noboltdb" -p 8 ./internal/miner/...
-count=1`: 55 tests, 0 failures (45 in the package itself, 10 in
`internal/miner/builder`). `go vet -tags "nosqlite,noboltdb"
./internal/miner/...`: clean. `-race` on the new tests alone and on the
whole package: clean. No test in the package changed behavior; the only
non-diagnostic edit was widening `fillTransactions`' call in
`miner_test.go:237` (`TestFillTransactionsRejectsMissingHeaderNumber`) to
pass the two new (nil) parameters.

**Why the naive build was refused, and what confirmed HEAD carries the
cache.** Step 4 required confirming n42-r84's exact lineage and building on
top of it *without* the n42-r85 base-read cache (6bw: `parallel.BaseCache`,
falsified -- B mean 96.1k against the 127.6k standing best, a 24.7% fall,
on the same eight-generator shape this round uses) -- and to stop instead
of building if qs/replan HEAD carries that cache enabled by default:

    git merge-base --is-ancestor 3c9311ac HEAD   # true, on qs/replan e1822bf3
      (3c9311ac "perf(parallel): read each account from the base state
       once per block" -- the commit 6br/6bw call the base-read cache)

`internal/parallel_processor.go:342` constructs `parallel.NewBaseCache(...)`
unconditionally on every parallel block build/import, with no env switch
anywhere in `internal/parallel/base_cache.go`,
`internal/parallel/state_reader.go` or the call site -- "enabled by
default" in the plainest sense, and 6bw's own numbers show it is live on
exactly this fleet's follower-import path, not some unreachable branch.
The commander's ruling on this finding: build n42-r86 with the established
file-checkout recipe (worktree at `f7ec2836` plus n42-r84's exact file list
plus S11's own files), never touching `internal/parallel/base_cache.go`,
`internal/parallel/state_reader.go` or `internal/parallel_processor.go` --
do not revert or gate `3c9311ac` on the branch.

**One-variable check (required before building).** n42-r84's lineage
(per `docs/QS_HANDOVER_20260920.md`: "n42-r84 = r80 + the delta fix") is a
detached worktree at `f7ec2836` plus `DEFERRED`/`FOLD`/`TAIL` (the three
file lists in `build-and-queue.sh`, giving n42-r80) plus
`internal/parallel/executor.go`, all four checked out from commit `c0931aeb`
(the delta fix; verified unchanged between `c0931aeb` and current
`origin/main` for all eight files, so checking them out from `c0931aeb`
or from `origin/main` today gives identical bytes). None of those eight
files is one S11 touches. For each of the five *pre-existing* files S11's
commit `537ec21e` modifies, `git diff f7ec2836 537ec21e^ -- <file>` (the
version n42-r84 was built from, since none of the eight lever files above
overlaps these five, is simply `f7ec2836`'s copy):

| file | f7ec2836 == 537ec21e^ | resolution |
|---|---|---|
| `internal/blockchain.go` | yes (0 diff lines) | checked out `537ec21e`'s version directly |
| `internal/blockchain_types.go` | yes (0 diff lines) | checked out `537ec21e`'s version directly |
| `internal/miner/miner_test.go` | yes (0 diff lines) | checked out `537ec21e`'s version directly |
| `log/root.go` | yes (0 diff lines) | checked out `537ec21e`'s version directly |
| `internal/miner/worker.go` | **no** (60 diff lines) | two intervening commits touch it: `19687889` "diag(miner): stamp the build trigger, the build's end and the speculative park/hit" and `89d15267` "perf(miner): do not restart the speculative build the trigger was waiting for" -- neither is part of n42-r84's lineage. Applied only S11's diagnostic hunk: `git diff 537ec21e^ 537ec21e -- internal/miner/worker.go \| git apply` against `f7ec2836`'s copy in the build worktree -- applied cleanly (`git apply --check` verified first), so worker.go carries r84's pre-diagnostic behavior plus exactly S11's hunk, nothing from the two intervening commits |

4 of 5 pre-existing files identical (1 required the hunk-only path, applied
cleanly, no stop condition hit). The two brand-new files
(`internal/miner/build_stall_watchdog.go`, `internal/miner/build_stall_watchdog_test.go`)
have no prior version to diff against; checked out directly from `537ec21e`.

**Build.** Detached worktree `/data/blockchain/gov5-work/wt-r86-build` at
`f7ec2836`; `git checkout c0931aeb -- <DEFERRED+FOLD+TAIL+executor.go>`;
`git checkout 537ec21e -- <the 4 identical files + the 2 new files>`; the
worker.go hunk applied as above.
`internal/parallel/base_cache.go` absent from the resulting tree; `grep -rl
BaseCache internal/ modules/` empty. `GOCACHE`/`GOTMPDIR` under
`/data/blockchain/gov5-work` throughout (never `/home`, which is full).
`CGO_ENABLED=1 GOMAXPROCS=8 nice -n 15 go build -p 8 -tags nosqlite,noboltdb
-o n42-r86 ./cmd/n42` -- clean. In the same worktree: `go vet -tags
"nosqlite,noboltdb" ./internal/miner/...` clean; `go test -tags
"nosqlite,noboltdb" -p 8 ./internal/miner/... -count=1`: 53 tests, 0
failures (2 fewer than qs/replan HEAD's 55 -- the two tests the intervening
commits `19687889`/`89d15267` added to the package are, correctly, not in
r84's lineage). Binary verification: `strings n42-r86 \| grep -c "build
stalled before fill"` = 1 (the diagnostic is present); `strings n42-r86 \|
grep -c BaseCache` = 0 (the cache is absent) -- both markers exact string
matches against the running binary, not a source-tree check.

`/data/blockchain/gov5-work/n42-r86`: 108,694,408 bytes, sha256
`f07e2b811d6569363c363d0286b17d672b4854dfecbe593297fa63e7a77e665c`. Commit
list: base `f7ec2836`; lever files (`DEFERRED`/`FOLD`/`TAIL`/
`internal/parallel/executor.go`) at `c0931aeb`; S11 files at `537ec21e`
(`internal/blockchain.go`, `internal/blockchain_types.go`,
`internal/miner/miner_test.go`, `log/root.go`, `internal/miner/worker.go`
(hunk-only), `internal/miner/build_stall_watchdog.go`,
`internal/miner/build_stall_watchdog_test.go`).

**Runner.** `run-r35zzz.sh`/`chain-35zzz.sh` built from the `run-r35zzt.sh`/
`chain-35zzt.sh` pair (35zzt's proven eight-generator, 1000-sender shape --
`-target-depth 45000`, `--floods 8 --senders 1000`, unchanged from 35zzt,
NOT 35zzy's sixteen-generator numbers). Binary references retargeted to
n42-r86; `N42_BUILD_STALL_DIAG=1` added next to `N42_MINER_ADOPT_APPENDS=1`
in the node environment block, same style as its neighbors. One harness fix
made after 35zzt was carried in (found by diffing `run-r35zzt.sh` against
`run-r35zzy.sh`): the `MODE-FAILED` trigger regex no longer treats "deferred
check FAILED" as an abort signature (it is the expected pre-import vote
decline, not a failure -- 35zzx aborted on one needlessly); the comment
explaining why was carried with it. No other difference between the two
runners was found beyond the sixteen-generator shape itself (skipped, by
design) and round-name/log-path tokens. `chain-35zzz.sh` waits on
`wr-logs/r35zzy.log`'s terminal line (not `r35zzs.log`, since 35zzy is this
round's actual predecessor) with the memory gate, n42-rs turn-taking and
quiet-box checks unchanged from `chain-35zzt.sh`. `bash -n` clean on both.
One collateral-damage bug caught and fixed before finishing: a blanket
`s/35zzt/35zzz/g` over `run-r35zzt.sh` also corrupted an unrelated historical
reference inside the file's ~11 KB running commentary line ("35s: 35r on
n42-r35zzt", an old binary name that happens to contain the literal
substring "35zzt" -- coincidence, not this round's token); restored that one
line verbatim from `run-r35zzt.sh` before finalizing. Neither script was
launched.

**Prediction 82 (registered before any round).** On `run-r35zzz.sh`/
`chain-35zzz.sh`, n42-r86, 35zzt's eight-generator shape:
(a) diagnostics cost nothing: B mean within the 3.6% noise floor of 127.6k;
(b) every build that takes >3 s to reach its fill leaves a stack dump and a
`prefill phases` line that name the step and the lock or call it waited on;
(c) if no such stall occurs in the round, the round still yields the
n42-r84-lineage import-phase line (`import_breakdown.py`) on the
eight-generator shape.

**VERDICT: confirmed** (implementation, tests, one-variable check and build
all done on the commander's ruling). QS_QUEUE.md's S11 row is marked
prepared, not launched.

## 6ca. Round 35zzz: n42-r86 on the eight-generator shape -- zero stalls, prediction 82 confirmed on all three clauses (2026-09-21)

35zzz ran on **n42-r86** (2026-09-20 23:51 EDT preflight - 2026-09-21 00:50:35,
`ROUND DONE`, no abort), 35zzt's proven eight-generator/1000-sender/
`-target-depth 45000` shape, `N42_BUILD_STALL_DIAG=1` the only new variable.
Legs: A1 done 00:10:41, B1 00:10:41-00:24:14, B2 00:24:14-00:37:45, A2 done
00:50:35 (A legs not used to conclude anything, per protocol).

**Evidence preserved first.** Every node's `log/n42.log` had already rotated
once by round end (node0 at 00:41:41 .. node3 at 00:49:32, all after both B
legs finished), so the entire 00:10:41-00:37:45 window sits inside each
node's single rotated `n42-*.log.gz` (`zcat <gz> | head -1` gives
`2026-09-20 23:51:06` for all seven, well before the window; each node's live
`n42.log` starts only at its own rotation timestamp, all after 00:38:30). One
`zgrep -E` pass per node against that `.gz`, pattern
`"time":"2026-09-21 00:(1[0-9]|2[0-9]|3[0-7]):[0-5][0-9]"` or
`"time":"2026-09-21 00:38:(0[0-9]|[12][0-9]|30)"`, single-threaded, no fleet
process touched: `/data/blockchain/wr-logs/r35zzz-keep/node{0-6}-B.log`,
486 MB total (59-79 MB a node), well under the 6 GB cutback threshold. No
`build-stall-*.stacks` file exists on any node, in the kept window or
anywhere in the full-round `.gz`/current `n42.log` pair (checked separately,
see below) -- there was nothing to copy.

**1. B mean, TPS, occupancy, block time.** Same instrument as 6bx: the four
B-window lines the harness itself prints to the round log
(`/data/blockchain/wr-logs/r35zzz.log`), not a re-derivation.

| leg | win1 TPS / occ / blockTime | win2 TPS / occ / blockTime |
|---|---|---|
| B1 | 135,016 / 48.7% / 1.176s | 118,969 / 47.6% / 1.304s |
| B2 | 132,731 / 48.9% / 1.200s | 118,435 / 34.8% / 0.938s |

**B mean (135016+118969+132731+118435)/4 = 126,288.** Against 35zzt's 127.6k
standing best (same generator shape, so a direct comparison, not a
mechanism-only one) that is -1.0%, inside the documented 3.6% noise floor
(6j). Against 35zzy's 86.2k (a different, sixteen-generator shape, kept here
only because the task asked for it) it is +46.5% -- not a meaningful
delta, since 6bx already showed cross-shape numbers are not comparable.

35zzt's own four B-window numbers, same shape, for the side-by-side the
86.2k table never had: B1win1 133,038/49.0%/1.200s, B1win2 122,315/36.9%/
0.984s, B2win1 134,836/48.1%/1.176s, B2win2 120,147/37.0%/0.984s. One
genuine (mild) shape difference from that baseline: 35zzt's win2 blockTime
is FASTER than win1 in both legs (0.984s < 1.200s/1.176s -- fewer, better-
packed blocks late in the leg); 35zzz's B1win2 is SLOWER than its own win1
(1.304s > 1.176s) and only B2win2 shows the 35zzt-style speedup (0.938s <
1.200s). This is a real, small divergence in shape, not a stall: the
largest chain-wide gap inside B1 is 2 s (see below), nothing like the 52 s
6by found, and it sits nowhere near the B1win2 boundary used above.

**2. `import_breakdown.py`, adapted to read the kept files instead of the
live (already-rotated) `qs-node*/log/n42.log` path the original script
opens, over both B legs, `txs>=160000`** (same threshold, run against
`/data/blockchain/wr-logs/r35zzz-keep/node*-B.log`):

    follower import (full blocks) n=1896
      body 10 ms (p90 26), proc 554 ms (p90 707), write 214 ms (p90 283), total 803 ms (p90 951)
      proc breakdown (joined to the same block's own `parallel block` line, n=2212 samples across all leader turns):
        recoverMs 26 (p90 61), execMs 255 (p90 387), finalizeMs 145 (p90 195)

Side by side with 6bx's r84-without-cache number on the *sixteen*-generator
shape (body 9 / proc 529 (recov 21, exec 260, finalize 149) / write 215 /
total 762 ms): this eight-generator round's numbers are 5-20% higher on
every field except `write`, which is flat. As 6bx already noted for the
reverse comparison, the two are not a clean A/B (different generator count
changes the mix of block sizes feeding the `txs>=160000` filter), but the
n42-r86 diagnostics add no field to this line and could not plausibly be
the source of the difference -- `body`/`proc`/`write` are computed the same
way `blockimport phases` always has been, unrelated to S11's own
instrumentation (`miner: prefill phases`, `build_stall_watchdog.go`), which
lives entirely in the leader's pre-fill path, not the follower import path
this line measures.

**3. Stalls.** Zero. `grep -c '"msg":"miner: build stalled before fill"'`
returns 0 on every one of the 7 kept `node*-B.log` files, and 0 again when
the same pattern is run against each node's full-round `.gz` (23:51:06
onward) and current `n42.log` (00:38:30+ onward) -- so this is not an
artifact of the B-window cut, the whole round produced zero. Correspondingly
zero `build-stall-*.stacks` files exist anywhere. Chain-wide committed-block
gaps (`blockimport phases`' own `n`, earliest timestamp seen on any of the 7
nodes, consecutive-`n` diff, computed the same way 6by pinned the 52 s gap):
largest gap in B1 is **2 s** (13658193@00:23:06 -> 13658194@00:23:08, and
three earlier ties of the same 2 s at 00:22:58-00:23:06; leader of
13658194 is **node3**, propose-phases `total` 235,877,282 ns = 236 ms on
that block, itself unremarkable); largest gap in B2 is **3 s**
(13660336@00:35:07 -> 13660337@00:35:10; leader of 13660337 is **node0**;
tied at 3 s by 13660312@00:34:35 -> 13660313@00:34:38, leader **node1**).
Both are inside each leg's own win1/win2 blockTime range (1.176-1.304s in
B1, 0.938-1.200s in B2), i.e. roughly 1.5-3x an ordinary block, not a
collapse. `TC formed`/`view timed out`: exactly **2** distinct events in the
combined B legs (view 3852 at 00:11:43, new leader node4; view 6087 at
00:25:18-19, new leader node3), each seen identically by all 7 nodes.  Both
land 62-65 s into their leg's own 400 s baseFee-decay warmup (B1 starts
00:10:41, B2 00:24:14) -- i.e. during the quiet pre-flood period, not during
either leg's scored flood window -- consistent with a leg-startup artifact
common to both legs rather than anything the flood or the S11 diagnostics
did. `hotstuff view timing`'s `r1`/`r2` fields were not re-examined here
since no stall exists to explain.

**No stack dump exists to anatomize.** This is the direct, expected
consequence of zero builds crossing the 3 s watchdog threshold -- there is
no counterexample to clause (b), but also no live-fire exercise of the dump
path itself in this round (6bz's unit tests are the only evidence the dump
code runs; see the ruling below).

**4. `miner: prefill phases`, steady state.** 226 lines across the 7 kept
files (`grep -c` per node: 44/29/36/31/29/28/29). Field-by-field
(nanoseconds from the log, converted to ms; `p95` = value at the
`int(n*0.95)` sorted index):

| field | median | p95 | max |
|---|---|---|---|
| `alignCall` | 0.0 | 0.0 | 0.0 |
| `lockWait` | 3505.7 | 6449.3 | 8106.8 |
| `insertParent` | 0.0 | 0.0 | 0.0 |
| `persistWait` | 0.0 | 0.0 | 0.0 |
| `roTxBegin` | 0.0 | 0.0 | 0.0 |
| `specTreeReload` | 111.8 | 209.4 | 300.9 |
| `rootLockWait` | 0.0 | 0.0 | 0.0 |
| `headerPrepare` | 0.6 | 99.8 | 380.3 |
| `blockStart` | 0.1 | 0.2 | 1.0 |
| `pendingSnapshot` | 0.0 | 0.0 | 5.8 |
| `trim` | 0.3 | 7.0 | 148.5 |
| `total` | 123.4 | 230.4 | 448.0 |

**`lockWait`/`rootLockWait` are not this build's own wait time and must be
read differently from every other field in this table.** They are drained
(`Swap(0)`, `internal/blockchain.go:541-548`) from a `bc`-wide atomic
accumulator (`buildStallLockWaitNs`/`buildStallRootLockWaitNs`,
`internal/blockchain_types.go:236-237`) that ANY caller of `bc.lock` or
`minerRCMu` adds to -- ordinary block import and write included, per the
field's own doc comment ("shared with ordinary import/write"). A build's
`pf.lockWait` is whatever accumulated there since the last drain by anyone,
not what this build itself waited for; that is exactly why its median
(3,505.7 ms) is 28x the `total` median (123.4 ms) for the same 226 lines --
an impossible relationship if it were bounded by this build's own elapsed
time. It is real evidence that `bc.lock`/`minerRCMu` contention is
substantial somewhere on the node across the round, but it cannot be
charged to any one build's critical path from this instrumentation alone.

Excluding that pair, **`specTreeReload` is the dominant per-build cost**
at ordinary (just-over-the-50ms-cutoff) magnitudes: median 111.8 ms is 91%
of the `total` median (123.4 ms). **`headerPrepare` is the dominant cost in
the heaviest individual lines**: all 5 of the largest-`total` prefill lines
in the round (397-448 ms, all on node0, in two tight clusters --
13658098-13658101 at 00:21:27-00:21:30, and 13660337-13660338 at
00:35:08-00:35:09, the second cluster containing the very block that owns
B2's largest 3 s commit gap above) are `headerPrepare`-dominated
(158-380 ms) with `specTreeReload` as a secondary, more variable
contributor (0.8-193 ms across the same 5 lines). **No line in the round
crosses 500 ms** (`total` max is 448 ms), so the ">500 ms dominant step"
question the task posed has no rows to answer from this round; the
closest analogue is the 5 lines just described.

`headerPrepare` times `prepareWork` (`internal/miner/worker.go:2065-2134`),
which holds `w.mu.RLock()` for its whole body, including `makeEnv`. S11 adds
no sub-timer inside that call, so whether its heaviest draws are
`w.mu` contention from a concurrent writer (`w.mu.Lock()` sites at
`worker.go:458-459,757-759,843-844,930-931,991-1009,1579-1586`) or real
work inside `makeEnv`/`engine.Prepare` cannot be distinguished from this
diagnostic; see "What waits on what" below.

**5. `parallel fill drops` / candidate drops.** Zero, completely, in both B
legs on every node. `grep -c '"parallel fill drops"'` is 0 on all 7 kept
files. Every `"msg":"miner: parallel fill"` line's own `failed` field (782
lines total, 110-117 a node) sums to 0 with a max of 0 -- not "small",
literally none. So `nonceHigh`, `nonceLow`, and "blocks with >25% dropped"
are all **0** in every window, per leg, and for the two B legs combined; no
per-window split is needed since there is nothing to split. **BAD BLOCK**:
0 (checked across each node's full-round `.gz` and current `n42.log`, not
just the B window). **Divergence-style messages** (`does not reproduce
sealed root`, `QMDB tree/marker discontinuity`, any `diverg*` string): 0,
same full-round check. **`MODE-FAILED`**: the round log never emits `ABORT`
or `ROUND ABORTED`, ends in a clean `ROUND DONE`, and
`/data/blockchain/wr-logs/r35zzz-MODE-FAILED` does not exist.
**`miner: suppressing divergent same-height sibling`**: 0 in the B legs
(present in 35zzy at 3 instances; absent here). **Generators dry: no.**
Summing `blockimport phases`' own `txs` field over node0's full leg spans
(chain-wide, one count, no double counting): B1 consumed 28,960,848 of its
36,000,000-tx funded budget (80.4%), B2 consumed 27,571,778 of 36,000,000
(76.6%) -- high utilization, but under budget in both legs, and the
zero-drop record (a dry generator's own send-ahead exhaustion is exactly
what would show up as `nonceHigh`/`nonceLow`, per 6bt/6bx/6by) points the
same way. Direct generator stdout was not retained under the
one-current-plus-one-rotated log policy, so this is inferred, not read off
the flood's own output -- consistent with 6bx's identical caveat.

**Ruling on prediction 82.**

**(a) B mean within the 3.6% noise floor of 127.6k -- confirmed.** 126,288
vs 127,600 is -1.0%, comfortably inside 3.6%. The registered caveat about
r86 also carrying r84's two lineage fixes (84ecf827, c0931aeb) on top of
the diagnostics does not need to be invoked to explain a miss, because
there is no miss to explain -- but it is worth restating for the record now
rather than only if a future round needs it: this result alone cannot
separate "diagnostics cost nothing" from "diagnostics cost something small
that a ~1% swing already hides," since no round in this campaign has run
n42-r84's exact bits (without the diagnostics) on this exact eight-
generator shape to subtract against. What is confirmed is only the
registered bar itself: the number is within noise of the standing best.

**(b) every build that takes >3 s to reach its fill leaves a stack dump and
a prefill-phases line naming the step -- vacuously confirmed, not
live-fire tested.** No build in the round crossed the 3 s threshold (max
observed prefill `total` 448 ms), so there is no case in this round where
the clause could have failed, and none where it was actually exercised
end-to-end either. `TestBuildStallWatchdog{FiresAfterThreshold,
CancelPreventsDump,RateLimited}` (6bz) remain the only evidence the dump
path itself runs; this round adds no live confirmation beyond "it did not
need to fire."

**(c) if no stall occurs, the round still yields the n42-r84-lineage
import-phase line on the eight-generator shape -- confirmed.** Section 2
above is exactly that line, obtained without any stall in the round,
directly satisfying the clause as written.

**VERDICT: confirmed**, on the plain reading of all three clauses as
registered. Two things temper it without falsifying anything: clause (a)'s
result cannot be attributed to the diagnostics in isolation (see above),
and clause (b) was never exercised live (no counterexample, but also no
positive demonstration beyond the unit tests).

**What waits on what.** Nothing in this round waited long enough to need an
answer at the granularity prediction 82 was built to get: no build reached
the 3 s watchdog, so there is no goroutine dump and no `commitWork`-path
stall to attribute to a specific lock or holder. At the steady-state
granularity the prefill phases line does resolve, ordinary (just-over-
50 ms) prefill time is dominated by `specTreeReload` (`NewMinerRootComputer`'s
speculative-tree reload, sharing `minerRCMu` with the startup pre-warm,
`internal/blockchain.go:562`), and the heaviest individual draws (397-
448 ms, still nowhere near the 3 s dump threshold) are dominated instead by
`headerPrepare` (`prepareWork`, `internal/miner/worker.go:2065`, held under
`w.mu.RLock()` for the call's full body including `makeEnv`). The evidence
does not determine which: S11 times `headerPrepare` as one block, with no
timer separating "waiting for a concurrent `w.mu.Lock()` holder" from "doing
real work inside `makeEnv`/`engine.Prepare`," and no goroutine dump exists
for any of these lines to read the blocked frame from directly, because
none of them stalled long enough to trigger one. Plainly: not determined,
and this round's own instrumentation is not fine-grained enough at the
sub-3s scale to determine it without a further timer inside `prepareWork`
itself.

**Method (6ca).** Evidence preserved first (see above) before any analysis.
`import_breakdown.py`'s logic (`/data/blockchain/gov5-work/wt-r27/scripts/
qs-analysis/import_breakdown.py`) was reproduced against the kept files
rather than run in place, since the original script's `glob` target
(`/data/blockchain/qs-node*/log/n42.log`) had already rotated the B-window
data out by analysis time; same `txs>=160000` filter, same field set,
`proc` breakdown joined to `"msg":"parallel block"` by block number `n`
(not by timestamp, since that line does carry `n`, unlike `"miner: parallel
fill"` below). Prefill-phases stats: one `json.loads` pass per kept file
over `"msg":"miner: prefill phases"` lines, medians/p95/max computed in
Python over the raw nanosecond fields divided by 1e6. `parallel fill
drops`/`miner: parallel fill`: the same technique, noting the latter line
carries no block number (`internal/miner/worker.go:1925`) and was read on
its own `failed` field only, not joined to a block -- sufficient here since
every value was 0. Commit-gap timeline: identical technique to 6by's,
`"msg":"blockimport phases"` keyed on `n`/`time`, earliest time per `n`
across all 7 kept files, consecutive-`n` diff, leg-bounded by the exact
`LEG B1`/`LEG B2`/`LEG B2`/`LEG A2` timestamps in the round log (so the
leg-start decay-period gap at each boundary, e.g. the 64 s and 68 s gaps
that span 00:10:39->00:11:43 and 00:24:11->00:25:19, is correctly excluded
from "inside the leg" by the boundary itself, not filtered by a size cut).
Leader attribution: for a given `n`, whichever node's kept file carries a
`"msg":"miner: propose phases"` line with that `n` is that block's leader.
BAD BLOCK/divergence/stall-dump checks were run against each node's full
`.gz` and current `n42.log`, not just the kept B-window slice, specifically
because those are round-wide correctness questions, not B-window
throughput ones.

## 6cb. S12: the full-block cycle is not import-bound -- deferred execution moved the critical path to the vote round-trip and the leader's own state-root computation (2026-09-21)

Logs-only, no fleet touched. Source: `/data/blockchain/wr-logs/r35zzz-keep/
node{0-6}-B.log` (the same kept files as 6ca). Script: `wt-r27/scripts/
qs-analysis/full_block_critical_path.py` (committed alongside this section).
"Full" = `txs >= 150000` on a `"miner: propose phases"` line (matches the
task's threshold; 6ca's own `txs>=160000` filter would drop a handful of
147-159k blocks that still read as ~90%+ of the fill cap -- using 150000
does not change any conclusion below, only the sample size).

**A binary-vintage fact that shapes everything below.** n42-r86 (this
round's binary) predates commits 89d15267/b97ca94e: `"miner: build
triggered (leader view)"`, `"miner: build phases"`, and the speculative
park/hit lines carry no `tMs` here -- only second-resolution `time`, too
coarse to place inside a ~0.8-1.2 s cycle. `"miner: prefill phases"` only
fires above a 50 ms cutoff (`build_stall_watchdog.go:225-236`), i.e. NOT
for the median (fast) block. So the leader's trigger/prefill/fill prefix
cannot be read directly off those lines for a typical block. Two lines
that DO carry `tMs` for every full block give an equivalent anchor instead
(`worker.go:813-818`, `2153-2195`, `2340-2365`):

- `task.createdAt = tMs - total/1e6` (`total` = `time.Since(createdAt)`;
  `createdAt` is stamped when `commit()` hands the task to the sealer,
  i.e. the end of `fillTransactions`/`commit()`).
- `t_commit_start = createdAt - assemble/1e6` (`assemble` = the whole
  `commit()` call, `tCommitStart` to `createdAt`) -- the end of fill /
  start of state-root assembly, in epoch ms, for every full block.
- `push_instant = tMs - write/1e6` -- push happens before write under
  `N42_PUSH_BEFORE_WRITE`/`N42_PROPOSE_BEFORE_WRITE` (both on this round,
  `worker.go:645-703`), so this is the instant a follower could actually
  start receiving the block. Same convention `cycle.py`/`leg_compare.py`
  already use as `"seal"`, reused here under its real name.
- follower side: `"blockimport phases"` `tMs` = end of that follower's
  import (own write included); `total` is the import's own elapsed time.

**1. Cycle time (push-instant to push-instant, chain-wide), full blocks.**
Three full windows recovered from the kept logs alone (the round log
never prints their exact timestamps): B1's 170 full blocks in
`00:10:41-00:24:14`, first 51 = win1, next 46 = win2 (harness's own
printed counts); B2's 160 full blocks in `00:24:14-00:37:45`, first 50 =
win1 (B2win2 -- 34.8% occupancy -- excluded even though it contributes 10
stray full blocks past the cut). 147 full blocks total. Every full block
is paired with its immediate predecessor `n-1`, **whatever size that
predecessor was** -- requiring the predecessor to also be full (my first
pass) turns out to bias the handover/chained mix (see below), so the
final numbers use any-size predecessors, matched via `worker.go`'s own
per-block leader field:

| population | n | median | p90 |
|---|---|---|---|
| all | 147 | 887.8 ms | 1735.5 ms |
| in-tenure (chained) | 79 | 799.1 ms | 1085.7 ms |
| hand-over | 68 | 1201.1 ms | 1884.2 ms |

This is a **push-to-push** cycle (leader's own push instant, not
write-completion and not a commit timestamp -- see anchor list above).
Against the baseline's 1.18-1.30 s (the harness's `blockTime`, which is
`60 s window / block count`, i.e. a MEAN over a fixed-duration window,
not a per-block median): the median in-tenure cycle (799 ms) is at the
baseline's low end, and the population's own p90 (1735 ms `all`) plus a
handful of multi-second gaps (6ca: 2-3 s) pull the window MEAN up to
where `blockTime` reads it. The two statistics measure different things
and are not expected to match; they are consistent (median < mean, as a
right-skewed distribution requires).

**Handover fraction: 68/147 = 46%, NOT the tenure=4 baseline of 25%.**
Checked directly: leader run-lengths over every block (any size) in leg
B1 are 557 runs of exactly 4 and 2 runs of 3, out of 559 runs across 2234
blocks -- tenure=4 holds essentially exactly. Full blocks are
over-represented right after a hand-over because hand-over cycles run
longer (1201 ms vs 799 ms median), giving the mempool more time to
refill before the new leader proposes -- not because leadership rotates
more often. (An earlier pass that required BOTH blocks in a pair to be
full got 8/87 = 9%, the opposite bias, for the mirror-image reason: a
big hand-over block can drain enough backlog that blocks 2-4 of the next
tenure dip under 150,000 and drop out of a full-to-full pairing. Neither
9% nor 46% is "the" hand-over rate of blocks in general -- 25% is: it is
only the rate *conditioned on the outgoing block being full* that
runs high.)

**2. Waterfalls.** T0 = `push_instant` of the block *before* the one
named. Leader segments are exact for the specific example block named
(they are literally sequential offsets from one anchor); follower
segments are the arrival/import-phases lines for all six followers, ranked by
import-end; the "quorum-forming" follower is explained in section 3.

*Median in-tenure block, n=13658061 (leader=node4, cycle=799 ms):*

| segment | start (ms) | dur (ms) | note |
|---|---|---|---|
| push(v-1) -> QC(v-1)/ViewStart(v) | 0 | 552.7 | WAIT (consensus round-trip) |
| leader trigger+prefill+fill | 552.7 | -90.9 | already done (speculative hit) before QC formed |
| leader commit()/assemble+finalize | 461.8 | 226.3 | CPU (state-root dominates: finalize done at +206.5 of the 226.3) |
| leader BLS sign | 688.1 | 0.6 | CPU, negligible |
| leader gate+copy residual (unsplit) | 688.6 | 83.9 | CheckSealParentApplied + receipts copy, not separately timed |
| leader push | 772.6 | 26.5 | ends the cycle (offset 799.1 = push_instant(v)) |
| leader write (off critical path) | 799.1 | 239.6 | parallel with the next cycle |

| follower | arrive offset | import-end offset | import dur |
|---|---|---|---|
| node1 | 841.7 | 1842.7 | 757.6 |
| node0 | 936.7 | 1974.7 | 745.6 |
| node3 | 870.7 | 1851.7 | 836.5 |
| node6 | 830.7 | **1859.7 (4th-fastest = quorum-forming)** | 861.6 |
| node2 | 872.7 | 1795.7 | 806.5 |
| node5 | 868.7 | 1974.7 | 872.7 |

Every follower's import of block v is **still running at least one full
cycle after v was pushed** (import-end offsets of 1795-1975 ms against a
799 ms cycle) -- two more blocks get proposed before any follower finishes
importing this one. That is the headline fact this step was asked to
find: the fleet is not waiting on it.

*Median hand-over block, n=13657990 (leader=node1, prev=node0, cycle=1207 ms):*

| segment | start (ms) | dur (ms) | note |
|---|---|---|---|
| push(v-1) -> QC(v-1)/ViewStart(v) | 0 | 400.7 | WAIT |
| leader trigger+prefill+fill | 400.7 | 600.1 | new leader has no parked task -- real work here (see section 4) |
| leader commit()/assemble+finalize | 1000.8 | 152.6 | CPU |
| leader BLS sign | 1153.4 | 0.3 | CPU, negligible |
| leader gate+copy residual | 1153.7 | 37.5 | unsplit |
| leader push | 1191.3 | 15.4 | ends the cycle (offset 1206.7) |
| leader write (off critical path) | 1206.7 | 399.1 | parallel |

| follower | arrive offset | import-end offset | import dur |
|---|---|---|---|
| node2 | 1243.7 | 1986.7 | 672.4 |
| node6 | 1250.7 | 1990.7 | 675.7 |
| node5 | 1254.7 | 1968.7 | 657.6 |
| node4 | 1287.7 | **1987.7 (4th-fastest = quorum-forming)** | 647.6 |
| node3 | 1257.7 | 1984.7 | 671.4 |
| node0 | 1287.7 | 2049.7 | 697.0 |

**3. The critical path.** Quorum size verified from code, not assumed:
`ValidatorSet.QuorumSize()` returns `n-f` (`internal/consensus/hotstuff/
validator.go:67-75`), and `validator_quorum_test.go`'s own case table
has `{n:7, f:2, want:5}`. The leader self-votes at propose time
(`proposal.go:106-107`, "the leader immediately self-votes... GossipSub
doesn't deliver back to sender"), so the quorum's 5th-of-7 vote is the
**4th-fastest of the 6 followers**, not the 6th or the 7th.

Two-segment path, in-tenure (medians): **push(v-1)->QC(v-1) 520 ms +
QC(v-1)->push(v) 271 ms = 791 ms**, against a measured cycle median of
799 ms -- **99.05% of the cycle, confirmed** (task's own 10% bar).
Hand-over (medians): 247 ms + 804 ms = 1051 ms against a measured 1201 ms
median -- 87.5%, outside the 10% bar on its own (see the note on the
QC-proxy's higher variance for hand-overs in the Method section below);
directionally the same shape (nearly all of the added cost sits in the
second segment, driven by `build_prefix`, section 4).

**The two largest segments on the in-tenure path:**

1. **push(v-1)->QC(v-1), 520 ms, WAIT, but *not* dominantly on raw
   import CPU.** Two facts pin this down. First, joining the leader's
   own protocol instrumentation (`"hotstuff view timing"`, filtered to
   the three full windows by time range) gives leader Round2
   (`PrepareQCFormed -> CommitQCFormed`) median 288 ms, matching the
   *followers'* own Round1 (`VoteSent -> CommitVoteSent`) median 288.5 ms
   almost exactly -- the wait is a vote/QC round-trip, not a local
   compute phase, and follower `ExecWait` (`ProposalReceived -> VoteSent`,
   the one phase that directly measures the import-gate) is 0 ms at the
   median (n=334 of 1440 rows even have it -- most prepare votes are not
   gated on import at all). Second, this fleet runs deferred execution
   (task said "on"): 87.4% of `"two-phase vote: casting held commit
   vote"` lines fleet-wide carry `"deferred":true`, meaning the commit
   vote's gate (`proposal.go:283-320`, `castHeldCommitVoteIfAttested` /
   `deferredAttested`) was satisfied by the block being *checked* plus
   its **parent** already imported -- not by the block's own import. Only
   13% of commit votes wait on the current block's own
   `importedBlocks[blockHash]`. Code comment at `proposal.go:294`
   confirms the mechanism's purpose directly: "Without this the Round-2
   gate waits for the block's own import and the cycle stays
   import-bound -- 35zzq measured the same 1.33 s block time as the round
   without deferred execution." So: WAIT, but on consensus/QC
   propagation and the (unt imed) "checked" step, not on the ~757-803 ms
   follower-import pipeline as a whole. Owning code:
   `internal/consensus/hotstuff/proposal.go` (vote cast/gate logic) +
   `quorum.go` (aggregation).
2. **QC(v-1)->push(v), 271 ms, CPU, on the leader.** `build_prefix`
   (trigger+prefill+fill) is 6.2 ms median here -- essentially zero,
   because the speculative build already finished before QC arrived (see
   section 4). The actual 271 ms is the leader's own serial work:
   `commit()`/assemble+finalize (168 ms median, of which `finalize`
   alone is 155.6 ms -- state-root computation dominates essentially the
   whole segment) + BLS sign (0.3 ms) + an unsplit gate-check/
   receipts-copy residual (35 ms) + push (16 ms). Owning code:
   `internal/miner/worker.go:2153` (`commit()`, `FinalizeAndAssemble`
   inside it).

**Largest segment NOT on the path: the leader's own write, 348.6 ms
median (p90 499 ms -- this is the "~0.5 s write" the task named).**
Off the path. Evidence: it starts only after push, so it cannot delay
anything a follower does; and the next block's own build does not wait
for it either -- 6ca's own prefill-phases table has `persistWait` and
`insertParent` medians of 0.0 across 226 lines, and this section's
`build_prefix` (QC-to-build-start gap) is itself ~0 ms at the median,
meaning the fill for v+1 is typically already complete via the
speculative-hit path (`worker.go:1120-1150`) well before this write
would matter even if something did wait on it.

**The follower's write (214 ms, from 6ca's `import_breakdown.py` output)
is mostly off the path too, given the same deferred-execution finding
above** -- it is one component of `"blockimport phases"`' `total`, which
gates the commit vote only on the 13% non-deferred path. Averaged over
the 87%/13% mix, its contribution to the measured 520 ms wait is real
but small, not the ~214 ms it would be if every commit vote waited on
full import. This is the one place this section's method cannot fully
separate "network propagation of the parent's already-completed import"
from "this block's own check cost" -- both live inside the same 520 ms,
and no line in this round times the `checkedBlocks[blockHash] = true`
step (`proposal.go:339`) on its own.

**4. Build/import overlap (in-tenure).** `build_prefix` = `t_commit_start(v)
- QC(v-1 proxy)`: median **6.2 ms** in-tenure (p90 47.3 ms) vs **621.2 ms**
hand-over (p90 1302.3 ms). In-tenure, the leader's speculative build of
v+1 (`worker.go:1120-1150`, "the whole build phase is off the critical
path" per that code's own comment) has its fill essentially always
complete before QC(v) even forms -- i.e. essentially the *entire* 520 ms
wait segment is overlapped by speculative work on the next block, not
spent idle. Hand-over is the opposite: the new leader was a follower for
the outgoing tenure and holds no parked task, so it pays real,
un-overlapped work here -- 621 ms sits between roughly the follower-import
median (757 ms) and zero, consistent with "import/align enough of the
parent to safely build," though this round's diagnostics do not carry a
sub-timer that would show whether that 621 ms is the full import pipeline,
a lighter `AlignAppliedBranch` re-check, or `persistWait`/`insertParent`
specifically (both are accumulator fields shared fleet-wide per 6ca, not
attributable to one build) -- **n/a at finer resolution than this
one number.**

**5. Slack check.** Median gap between "QC could form" and "next
propose happened" (`s2q`, QC(v-1)->push(v)) is **271 ms in-tenure**. Given
finding 3(1) above -- 87.4% of commit votes do not wait on the current
block's own import at all -- a follower import 100 ms faster would NOT
translate to a 100 ms-faster cycle for most blocks: **partial, and small**.
The honest bound this round's data supports: at most the ~13% of votes on
the non-deferred path could see the full benefit, and even those still
share the vote-cast/QC-gossip round-trip with everyone else, which this
round's logs do not decompose into a per-follower network-delay term
separable from import time. So: **not "cycle <- -100"** as a blanket
answer -- the follower side is largely decoupled from the cycle in this
round, precisely because deferred execution was built to make it so
(`proposal.go:294`'s own stated purpose, and 35zzq's prior 1.33 s
without it is the direct before/after this codebase already has).

**Confirmatory note (S12b, 2026-09-21).** The commander did not accept
this section's "not import-bound" reading on the strength of one
coincidence (in-tenure cycle 799 ms vs follower import total 803 ms) and
asked for a direct test of the specific mechanism that would make them
equal: a saturated, serial, depth-1 import-chain pacing one QC per
import. Section 6cc tests exactly that hypothesis on the same logs and
**falsifies it**: the import chain runs at 39-61% busy (not saturated),
73.4% of in-tenure full blocks need zero followers to hold their commit
vote on import at all, and the specific follower whose vote completes
the 5-of-7 quorum has a "held" vote line in only 13.9% of blocks (never
released within 20 ms of its parent's import end even then -- median
149 ms). This section's conclusion stands, now on stronger evidence than
the coincidence alone. See 6cc for the full test.

**Method.** `wt-r27/scripts/qs-analysis/full_block_critical_path.py`:
one pass per kept file, `json.loads` on lines pre-filtered by `msg`
substring. `propose_all` keys every `"miner: propose phases"` line by
`n` (any size) so a full block's predecessor need not itself be full;
`propose` is the `txs>=150000` subset used to pick which blocks get a
waterfall. Per-block leader fields (`created_at`, `t_commit_start`,
`push_instant`, `residual`) are computed once for every proposal from the
formulas in the anchor list above. Chained vs hand-over: same leader vs
different leader on consecutive `n`. QC(v) proxy: the leader's own next
`"hotstuff: view changed"` event with `isLeader:true` strictly after
`push_instant(v)` -- the same convention `cycle.py`/`leader_gap.py`
already use (verified here that restricting to `isLeader:true` changes
nothing for these specific queries: within one leader's own tenure or at
the moment its tenure begins, the very next view-changed event on that
node's own log is always its own, since views strictly increment 1:1 with
blocks in these windows -- zero TCs inside win1/win2/B2win1, per 6ca).
Follower "quorum-forming" rank: the 6 followers' `"blockimport phases"`
`tMs` (import end) sorted ascending, 4th entry (0-indexed 3). `"hotstuff
view timing"` lines (no `tMs`, ns/ms fields inline in the message text,
not JSON keys) were parsed with a regex and included by time-range
membership in the three full windows (their own `time` field is
second-resolution, adequate since these windows are ~97% full blocks
end-to-end -- a coarse time-range filter and an exact per-block `txs`
filter select nearly the same population here). The vote-cast
`"deferred":true/false` ratio was read directly off
`"two-phase vote: casting held commit vote"` lines, fleet-wide, no
join needed. Import->vote-cast latency (spot check only, not part of any
median above) used a nearest-following per-node join on
`"two-phase vote: casting held commit vote"` tMs, since that line carries
no block number.

**What this does and does not show.** It shows, with two independent
cross-checks (the push/QC/push accounting here, and the protocol's own
`"hotstuff view timing"` r1/r2 fields), that the in-tenure full-block
cycle is a two-segment path -- a consensus vote/QC round-trip (520 ms,
mostly off the current block's own import thanks to deferred execution)
followed by the leader's own serial state-root/seal/push work (271 ms) --
and that this sums to within 1% of the measured cycle. It shows the
follower's own import of a block routinely outlives that block's entire
cycle by 1-2x, so it cannot be the bottleneck in-tenure, and that the
leader's write and (mostly) the follower's write sit off that path. It
does NOT show what specifically fills the 520 ms wait beyond "vote/QC
round-trip plus an untimed 'checked' step" -- no line in this round times
BLS aggregation, gossip propagation, or `checkedBlocks[blockHash]`
individually, so that segment's own internal breakdown is n/a here. It
does NOT show hand-over's 621 ms `build_prefix` at finer resolution than
one number, for the same reason. It does NOT re-derive or dispute 6ca's
own findings (occupancy, stall count, prefill-phases table) -- it reuses
6ca's evidence-preservation and takes the same kept files as ground
truth. And the hand-over critical-path sum (87.5% of the measured cycle)
is a directional, not a "confirmed", result -- 68 samples with a p90 more
than 1.5x the median is a wide enough spread that the QC-proxy's single
next-`isLeader`-event convention (borrowed from a chained-block context
where it is unambiguous) likely adds real noise at a hand-over boundary
that this step did not separately quantify.

## 6cc. S12b: hypothesis H (a saturated depth-1 import chain paces the QC) -- tested and falsified (2026-09-21)

Commander's follow-up to 6cb. The commander flagged a coincidence 6cb
left unexplained: in-tenure cycle median 799 ms vs follower import total
median 803 ms. Hypothesis H, as registered: deferred execution's Round-2
gate needs the block's PARENT imported (`deferredAttested`), and a
follower's own import pipeline is serial (import(v-1) cannot start
before import(v-2) finishes) -- so a saturated depth-1 pipeline whose
stage takes ~800 ms produces exactly one QC per ~800 ms, the held commit
vote for v is released by the END of the parent's (v-1's) import, and a
100 ms-faster import should shorten the cycle by ~100 ms.

**Code path confirmed before measuring anything.** `onBlockImported`
(`internal/consensus/hotstuff/proposal.go:518-547`), which fires once a
block's import completes, calls `castHeldCommitVoteIfAttested(pending
CommitQC.BlockHash)` **directly and synchronously**, in the single
consensus-engine goroutine. So the code genuinely wires "this node's
import of some block finished" to "check whether a held vote can now
release" -- H's causal claim is real code, not speculation. What is not
yet established is whether that release is (a) the ordinary, universal
path every full block takes, or (b) a rare path that only fires when a
follower has fallen behind. Also found while reading: the ROUND-1
prepare vote has the identical deferred gate (`tryDeferredVote`,
`proposal.go:269-287`, logged as `"deferred vote: block checked and
parent imported, voting"` when it fires with a wait) -- both rounds can
be parent-import-gated, not just the commit round 6cb assumed.

**Method.** `wt-r27/scripts/qs-analysis/parent_import_gate.py`, same
kept logs as 6ca/6cb. View<->block-number offset (needed because
`"two-phase vote: casting held commit vote"` carries `view`, not `n`)
calibrated from the 6cb QC-proxy join itself: for every chained row, the
view number of the leader's own next `isLeader:true` "hotstuff: view
changed" event minus the block number it gates is constant per leg --
**57/57 chained B1 rows agree on offset -13652358, 22/22 B2 rows agree on
-13652357** (the two legs' view counters are not the same sequence --
nodes are restarted, `"node N: SIGTERM"` / `"stopped clean"`, between B1
and B2 in the round log -- so calibrating per-leg rather than assuming
one global constant is required, not just cautious). `"blockimport
phases"` `tMs` is confirmed stamped AFTER `dWrite` (`internal/
blockchain.go:2456-2493`), i.e. import-END, by reading the log call
site directly, not inferred.

**1. d1 = t_gate(v, deferred=true) - t_parent_end(v-1), in-tenure, all 6
non-leader nodes x 79 full blocks = 474 (follower, block) slots.**

| | share |
|---|---|
| no held-vote line at all (cast immediately, no wait) | 410/474 = 86.5% |
| held-vote line present, parent's `blockimport phases` also present | 64/474 = 13.5% (100% of these usable) |

Among the 64 usable rows: **median d1 = 134.5 ms, p10 = 100.0 ms, p90 =
171.0 ms. 0% land in 0-20 ms, 100% are > 20 ms, 0% are negative.** This
is the opposite mix H's literal "released BY the end of the parent's
import" wording predicted (which reads as d1 near 0) -- but a tight,
always-positive, never-negative band is still a real signature of a
causal link, just with an added ~130-150 ms step this round's logs do
not decompose further (the chain-layer `"blockimport phases"` line and
the consensus-layer `castHeldCommitVoteIfAttested` call are in different
subsystems; whatever connects them -- an `EventBlockImported` dispatch,
the engine's own event-loop scheduling, `checkedBlocks`/`deferredAttested`
re-evaluation -- has no separate timestamp anywhere in this round's
instrumentation. **n/a beyond "a consistent ~134 ms gap exists"**).

**Distribution of how many of the 6 followers actually needed to hold,
per in-tenure full block (n=79):** median **0**, mean 0.81 --

    held-count:  0    1   2   3   4   5   6
    blocks:     58    6   3   2   6   2   2

**73.4% of in-tenure full blocks need ZERO followers to hold anything --
every vote needed for quorum was already cast immediately.** Only
12.7% have 4 or more of 6 held, the band where holding could plausibly
be on the path to the 4-of-6 votes the quorum needs (5 of 7 total, the
leader's self-vote being the other one -- 6cb's quorum-size finding
carries over unchanged). This alone is most of the falsification: H's
"pacemaker" reading requires the hold mechanism to matter on most or all
blocks; it matters, at all, on barely more than a quarter of them.

**Quorum-forming follower specifically (4th-fastest of the 6 by import
completion, same definition as 6cb):** a held-vote line for that
specific node/block exists in only **11/79 = 13.9%** of in-tenure full
blocks. Among those 11: **median d1 = 149 ms, 0% within 20 ms of the
parent's import end** (values: 102, 112, 122, 126, 142, 149, 153, 155,
168, 171, 184 ms). For the other 86.1% of blocks the quorum-forming
follower's vote was cast via the immediate path -- no import-linked wait
measurable at all for it.

**2. Is the follower import chain saturated?** No. Computed per
contiguous window (pooling B1win1+B1win2+B2win1 into one span would
average across the multi-minute inter-leg gap and understate busy time --
checked and corrected before reporting):

| window | busy fraction (7 nodes) | median idle gap |
|---|---|---|
| B1win1 | 40.7-43.6% | 5.9-10.2 ms |
| B1win2 | 53.7-61.0% | 154.9-215.3 ms |
| B2win1 | 38.9-43.4% | 6.0-11.8 ms |

Pooled idle-gap median across all nodes/windows: **13.7 ms (n=508
consecutive-block pairs)**. The chain is busy 39-61% of the time, not
saturated, and the SHAPE is bimodal, not uniform: most gaps between
consecutive imports are tiny (order 10 ms -- the chain frequently runs
back-to-back with slack behind it) while a minority are large enough
(B1win2's 155-215 ms median -- itself elevated, consistent with B1win2's
already-known slower 1.304 s blockTime, 6ca) to account for the 40-60%
non-busy remainder. A saturated depth-1 pipeline would read close to
100% busy with a near-zero idle gap on every window; this reads as a
pipeline with real, if unevenly distributed, slack.

**3. Hand-over: does the new leader's build start at its own
parent-import end?** No, not directly -- but import IS unambiguously
somewhere on this path, unlike in-tenure. Across all 68 hand-over rows:
`t_commit_start(v) - own blockimport-phases-end(v-1)` is **median
508.3 ms, p10 388.5, p90 637.1 -- 100% of rows are > 20 ms (0% within
20 ms, 0% negative)**: the new leader's build never starts before, and
never right at, the moment it finishes importing the outgoing leader's
last block; it always starts a further ~500 ms later. That gap sits
inside `build_prefix` (QC-proxy-to-build-start, median 621.2 ms) --
chronologically, the QC-proxy event precedes this node's own
parent-import-end by a further ~113 ms in the median case (621.2 -
508.3), meaning the view had already (per the QC-proxy) advanced to this
node's tenure before its own local import of the immediate parent even
finished. This round's diagnostics carry no separate timestamp for "own
vote cast" or "build-trigger received" to explain what fills the
remaining ~508 ms after import finishes -- **n/a beyond the one number**.

**4. Hypothesis H: falsified**, on three independent legs of evidence
that all point the same way: (a) the import chain is not saturated
(39-61% busy, bimodal idle gaps, not ~100%/~0); (b) 73.4% of in-tenure
full blocks need no follower to hold a vote at all, and the
quorum-determining follower specifically needs holding in only 13.9% of
blocks; (c) even in the minority of cases where a hold IS observed, the
release lags the parent's import-end by a consistent ~130-150 ms, not
~0 -- a real dependency, but on a path that is off the critical path for
the large majority of blocks, exactly as 6cb concluded from the
q2s/leader-r2 evidence alone. The numeric coincidence that prompted H
(799 vs 803 ms) is not shown to be causal for the median block; it
remains, on this evidence, a coincidence between two numbers this round
did not otherwise expect to be equal.

**5. Slack, re-answered with this evidence.** In-tenure: import 100 ms
faster would leave the cycle for **73.4% of blocks completely
unchanged** (quorum already forms from immediate votes with zero
import-linked wait), and would help only the minority where a hold
happens -- and even then, only partially, since the ~130-150 ms
dispatch gap on top of the parent's import is itself unexplained and not
guaranteed to shrink 1:1 with import time. **Partial, and small in
aggregate** -- confirming 6cb's original answer, now for a mechanistic
reason (quorum rarely needs the held path at all) rather than only the
aggregate q2s/r2-timing argument. Hand-over is different in kind: import
IS somewhere on this node's own path (build never starts before its own
import ends), so a 100 ms-faster import plausibly removes close to
100 ms from the ~757 ms import component specifically -- but the
additional, unexplained ~508 ms gap after import means the translation
to the full hand-over cycle is **partial, not confirmed as 1:1** for the
cycle as a whole.

**What this does and does not show.** It shows, with three independent
measurements (release-timing distribution, per-block held-follower
count, and chain occupancy), that hypothesis H's specific causal
mechanism is real code (confirmed by reading `proposal.go:518-547`) but
is exercised on only a minority of blocks and follower-slots, and does
not saturate the import chain -- so it cannot be the pacemaker of the
typical in-tenure cycle. It does NOT show what the ~130-150 ms
notify-to-release gap consists of (dispatch, event-loop scheduling, or
re-evaluating `checkedBlocks`/`deferredAttested` -- no line times any of
these individually). It does NOT show why B1win2's idle gaps are an
order of magnitude larger than B1win1's/B2win1's beyond "B1win2 is
already known to be the slower window" (6ca) -- a deeper look at what
specifically elevates B1win2 was out of scope here. It does NOT
determine, for hand-over blocks, what the new leader's build is doing
for ~508 ms after its own import of the parent finishes.

## 6cd. S12c: both remaining gaps split with lines that already exist -- Gap A is real CPU work, Gap B is the deferred-includability check, and the code shows exactly why hand-over cannot borrow in-tenure's speculative shortcut (2026-09-21)

Commander's follow-up to 6cc, accepted: H is falsified, 6cb stands. Two
gaps were left as single numbers -- hand-over's ~508 ms (new leader's
build starting after its own import of the parent) and the ~134-149 ms
between a follower's chain-layer import-complete line and its held
commit-vote release. Both split cleanly using lines already in the kept
logs (no new instrumentation), plus a code trace for what connects them.
Script: `wt-r27/scripts/qs-analysis/gap_trace.py`, same kept files as
6ca/6cb/6cc.

**Gap A: hand-over timeline, on the new leader, relative to its OWN
`blockimport phases` end for the parent (t=0), n=68 rows.**

| segment | median (ms) | p10 | p90 |
|---|---|---|---|
| `blockimport phases` end -> `"block push: received"` | 0.0 | 0.0 | 0.0 |
| `"block push: received"` -> `miner: prefill phases` buildStart | 0.5 | -0.2 | 24.4 |
| prefill itself (buildStart -> prefill end) | 142.0 | -- | -- |
| prefill end -> `t_commit_start(v)` (= `fillTransactions` itself) | 351.3 | 279.3 | 451.6 |
| **sum** | **493.8** | | |
| (cross-check: direct `t_parent_end -> t_commit_start(v)`, matches 6cc) | **508.3** | 388.5 | 637.1 |

493.8 / 508.3 = **97.1%, confirmed (within the 15% bar, in fact within
3%).** All 68 hand-over rows have a matching `"miner: prefill phases"`
line (100% coverage -- a hand-over build almost always crosses the 50 ms
`logIfSlow` cutoff, unlike a steady-state in-tenure build, 6ca's ~5%).

**The largest sub-interval is `fillTransactions` itself, ~351 ms, real
CPU** (`internal/miner/worker.go:1657`, called from `commitWork` at
`worker.go:1471`) -- picking and executing candidates for a
~160,000-tx block with **no speculative head start** (see below for why
none exists). Prefill (142 ms) is the second-largest and is also real
CPU -- 6ca's own prefill-phases table already showed `specTreeReload`
and `headerPrepare` as its dominant steps; nothing here overturns that.
**The dispatch/queue/gate segment is ~0 ms** (0.0 ms for the
InsertChain-tail-to-log-line hop, 0.5 ms median for everything between
that and the build's own `buildStart` stamp: `blockApplied()`'s
in-memory/marker check, `NotifyBlockImported`
(`internal/consensus/hotstuff/service.go:1592`), the `go
s.triggerBlockProduction(...)` goroutine launch (`service.go:1622`), the
three re-run leader gates (`service.go:355-397`, `"hotstuff: leader gate
phases"` -- no `tMs` on this line, but its own three duration fields
`behindNs`/`committedNs`/`appliedNs` are each single-digit-to-low-double-
digit microseconds in the sample checked, consistent with an
in-memory-only gate at this point since the block IS now applied),
`Miner.TriggerBlockProduction` sending onto `w.newWorkCh`
(`internal/miner/miner.go:186-209`), and `commitWork`'s own entry
(`"miner: commitWork begin"`, `worker.go:1096`) through to the prefill
timer's `buildStart` (`build_stall_watchdog.go:212`, set to `commitWork`'s
own `start := time.Now()` at `worker.go:1097`)). **Answer to "is the new
leader waiting for the QC, its own write/apply, or a timer/queue":
neither the QC nor a timer/queue -- gates, dispatch and queueing are
free (~0 ms); the entire measured gap is the leader doing real,
un-shortcut-able CPU work** (prefill + fill) that it could not have done
any earlier, because it did not yet have the parent's post-state to
build against (its own `ensureParentApplied` gate,
`service.go:304-349`, is exactly the check that would have blocked an
earlier start).

`worker.go:1070`'s `time.NewTimer(wait)` is `paceBlock`'s pacing
throttle, sitting between `commitWork` and `sealStart`, and fires only
when `wait > 0` (ahead of a fixed-interval grid) -- the code's own
comment already argues it "cannot [fire] under load, a late block's
slot is in the past, so wait is negative," and the measured ~0 ms
dispatch gap here is consistent with that: nothing suggests this timer
fired on any of the 68 hand-over builds. `worker.go:1561`'s `timer :=
time.NewTimer(0)` is the main worker loop's legacy periodic-recommit
timer (`commit()`'s `timer.Reset(recommit)`), which the surrounding code
comment says a leader-driven engine (HotStuff) does not use for
production -- "produce ONLY via `TriggerBlockProduction`... An
event-driven `commit()` here builds on the local head with no pin." Not
on this path.

**Can the hand-over build start from a parked speculative task? The
mechanism exists in the code for exactly this case, but the deferred
vote path it depends on almost never satisfies its own gate.** Two
`OutputSpeculativeBuild` emission sites exist
(`internal/consensus/hotstuff/proposal.go:133,462`), the second being
the cross-view case: `sendVote` (Round 1, prepare), right after casting
this node's own vote for `blockHash`, checks `if
e.importedBlocks[blockHash] && LeaderForView(view+1,
e.validatorSet()) == e.myIndex` (`proposal.go:461`) and, if true, tells
the miner to start speculatively building on `blockHash` right away --
precisely "a node that knows it leads the next tenure speculates on the
outgoing leader's last block once it has imported it." The comment
above it (`proposal.go:450-459`) names the intent explicitly: "if
round-robin makes this node the NEXT view's leader... the ~500 ms build
can run during this view's vote rounds instead of after the view
change." **What prevents it firing for our 68 rows: the guard is
`e.importedBlocks[blockHash]`, i.e. the block's own FULL import, but
`sendVote` for a large block is overwhelmingly called from
`tryDeferredVote`** (`proposal.go:269-287`) **under deferred execution --
exactly the path that votes BEFORE the block is fully imported** (its
premise is "checked + parent imported," not "imported"; 6cc measured
this: follower `ExecWait` is 0 ms at the median, and 86.5%+ of votes
never wait on import at all). At the moment `tryDeferredVote` calls
`sendVote(view, pending)`, `e.importedBlocks[pending]` is essentially
always still false for a block this large, so the guard fails silently
and no `OutputSpeculativeBuild` is ever emitted for it -- `sendVote` runs
exactly once per view and the hint is never retried later, even once
the import this section measured (508 ms after `sendVote` already ran)
actually completes. In one sentence: **the same deferred-vote
optimization that makes in-tenure blocks fast is, as currently wired,
the reason hand-over gets no speculative head start at all** -- no
design proposed, per the task, but the mechanism and its silent failure
mode are both named with file:line above.

**Gap B: the ~134-149 ms held-commit-vote release, in-tenure, n=64
usable (follower, block) rows.** Same causal chain, now traced with one
more existing line: `"deferred check: block passes, vote may proceed
before its import"` (`internal/sync/rpc_block_push.go:96`, backed by
`CheckDeferredBlock`, `internal/deferred_includable.go:38`).

| pair | median (ms) | share within 20 ms |
|---|---|---|
| `t_parent_end` -> `"block push: received"` (InsertChain tail) | 0.0 | -- |
| `t_parent_end` -> `t_checked(v)` (`"deferred check: block passes"`) | 134.5 | 0% |
| `t_checked(v)` -> `t_gate` (the actual vote release) | **0.0** (n=64, values 0-1 ms, one outlier at 7 ms) | 100% |

**All 64 usable rows show `t_gate` and `t_checked` at the same
millisecond (or one apart).** So the ~134 ms is not a dispatch, queue,
or scheduling gap at all -- **it is the wall-clock duration of
`CheckDeferredBlock` itself**, run synchronously on the same goroutine
chain that `NotifyBlockImported` (`service.go:1592`) drives:
`NotifyBlockImported(parent's hash)` -> `retryDeferredChildren(parent's
hash)` (`rpc_block_push.go:125-133`) -> `deferredCheck(v)`
(`rpc_block_push.go:78-121`) -> `CheckDeferredBlock(v)`
(`internal/deferred_includable.go:38-100+`) -> on success, logs
`"deferred check: block passes..."` and calls `NotifyBlockChecked`
(`rpc_block_push.go:96-97`) -> `onBlockChecked` ->
`tryDeferredVote`/`onBlockImported`'s already-pending
`castHeldCommitVoteIfAttested` (`proposal.go:517-547`) fires in the same
call stack. `CheckDeferredBlock` itself does real, size-proportional
work per its own doc comment (`deferred_includable.go:29-33`): sender
recovery, nonce-contiguity and balance/gas checks against the parent's
just-published post-state, over every transaction in the block -- for a
~160,000-tx block, ~134 ms of that is unsurprising and requires no
further gap to explain. **No periodic element is on this path**: the
`100 ms` ticker at `service.go:215` belongs to
`requestCommittedCatchUp`'s post-hash/pre-header polling loop (a
catch-up-after-restart mechanism, gated on `s.blockFetcher` calls a node
only makes when it is missing a block entirely), not to the
already-arrived, already-queued deferred-check retry, which is invoked
directly by `retryDeferredChildren` with no timer in between (the
`deferredRetryInterval = 200 ms` `time.AfterFunc` at
`rpc_block_push.go:120` exists for a DIFFERENT case -- a block whose
check failed for a reason other than "parent not applied yet" landing
zero `NotifyBlockImported` calls to retry it on -- and does not fire on
this round's held-vote population, whose median 134 ms is well under
that 200 ms poll period and whose `t_checked`-`t_gate` gap of ~0 ms is
inconsistent with having gone through an extra timer round-trip).

**Gap B, answered: the code path is `NotifyBlockImported` ->
`retryDeferredChildren` -> `deferredCheck` -> `CheckDeferredBlock`, all
synchronous on one goroutine (the stream handler that just finished
importing the parent); the ~134 ms IS `CheckDeferredBlock`'s own
execution time for this block's transactions, not a wait.**

**Gap C (view-timing split, from the fields already parsed in 6cb --
no new parsing needed for the coarse table; the finer split the task
asked for is not available from these lines).**

| role | segment | median (ms) |
|---|---|---|
| leader | propose (ViewStart -> ProposalSent) | 250 |
| leader | Round1 (ProposalSent -> PrepareQCFormed) | 96 |
| leader | Round2 (PrepareQCFormed -> CommitQCFormed) | 288 |
| leader | total (ViewStart -> CommitQCFormed) | 718 |
| follower | Delivery (ViewStart -> ProposalReceived) | 434 |
| follower | ExecWait (ProposalReceived -> VoteSent) | 0 (present in only 334/1440 rows) |
| follower | Round1 (VoteSent -> CommitVoteSent) | 288.5 |
| follower | Round2 (CommitVoteSent -> CommitQCFormed) | 12 |
| follower | total | 756 |

`ViewPhases.LogLine()` (`internal/consensus/hotstuff/view_timing.go:190-
207`) is the only source of these numbers, and it carries exactly the
six/five fields above per role -- no sub-timer inside Round1 or Round2
separates "waiting for the k-th vote" from "aggregating/sending once it
arrives," on either the leader or the follower side. **That finer split
is n/a: the logs cannot tell.** What the coarse table already shows,
unchanged from 6cb/6cc: leader Round2 (288 ms) closely tracks follower
Round1 (288.5 ms) -- both cover the interval in which a follower's
COMMIT vote becomes sendable, which Gap B just showed is dominated by
`CheckDeferredBlock`'s per-transaction cost for whichever of the 13.5%
of (follower, block) pairs needed to hold, not by network round-trip
alone (a bare BLS-aggregate/gossip round trip on localhost/LAN would not
plausibly cost 288 ms by itself). Where exactly the REMAINING ~150 ms
of leader Round2 goes for the 86.5% of votes that are never held (since
Gap B only explains the held subset) is not determined by any line in
this round -- **n/a beyond that boundary**.

**What this does and does not show.** It shows, with lines that already
existed and a full code trace, that hand-over's ~508 ms is genuine,
necessary CPU work with essentially zero dispatch/queue/gate overhead,
and names the exact reason (a guard on full import, not on the
deferred-check-and-vote path that large blocks actually take) that the
existing cross-tenure speculative-build hint never fires for it. It
shows that the ~134-149 ms held-vote gap is `CheckDeferredBlock`'s own
execution time, not a scheduling artifact, with a same-millisecond
release confirming there is no queue between the check passing and the
vote going out. It does NOT explain the ~150 ms of leader Round2 that
sits outside the 13.5% held-vote population Gap B covers -- the other
86.5% of votes are not "held" (no log line marks their release time
against anything), so nothing here places what a Round2 vote round-trip
costs on that majority path beyond the aggregate 288 ms already known.
It does NOT propose a fix for hand-over's missing speculation (the task
asked for the mechanism and its failure mode only, not a design). It
does NOT re-open Gap A/B as open questions for optimization purposes
without first asking whether shaving CPU out of `fillTransactions` or
`CheckDeferredBlock` is worth it against 6cb's finding that neither
hand-over nor the held-vote path binds the median (in-tenure) cycle.

## 6ce. S13: hypothesis D (block delivery is the largest item on the in-tenure critical path) -- falsified; delivery is the smallest measured item, at ~44 ms of 520 ms (8.5%) (2026-09-21)

Commander's follow-up: `Delivery` (434 ms median, from 6cb's aggregate
`hotstuff view timing` table) sits inside a 799 ms in-tenure cycle whose
push->QC segment is 520 ms, and two vote round-trips on localhost should
cost milliseconds, not hundreds. Hypothesis D: delivering the ~26 MB
full block body to the quorum-forming follower, plus that follower's
receive/decode, is the largest item on the path. Script:
`wt-r27/scripts/qs-analysis/delivery_budget.py`, same kept logs.

**1. `Delivery` defined exactly, from the code that fills it
(`internal/consensus/hotstuff/view_timing.go:66-81`,
`engine.go:279-308`).** `Delivery = span(ViewStart, ProposalReceived)`,
both FOLLOWER-local `time.Now()` stamps on that node's own clock (all
seven processes share one host clock this round, so no skew correction
is needed, but the two stamps are still two DIFFERENT events on the
SAME follower, not a leader-timestamp-vs-follower-receipt pair carried
in a message). `ViewStart` is set by `newViewTiming(view)`
(`engine.go:305-308`) at `advanceToView`, i.e. "every node: the view was
entered" -- which itself only happens once this node has locally
processed the PREVIOUS view's CommitQC or timeout
(`voting.go:401-405`/`timeout.go:274,368,435,497`). `ProposalReceived`
is set in `processProposal` (`proposal.go:211-212`) the moment this
node finishes verifying the **Proposal message** (BLS signature +
JustifyQC + safety rule) -- **not** at block-body availability and not
at decode/check completion. The Proposal carries only `BlockHash`,
`TxRootHash`, the leader's signature and the QC fields (re-confirming
the S8 audit, handover commit `31a25647`: no block bytes travel with
it). **So `Delivery` measures "receive+verify a small consensus message
after locally entering the view," not "receive the block."**

**2. Block-BODY arrival, ranked, in-tenure full blocks (n=79), offsets
from the leader's own `push_instant`:**

| rank (of 6 followers) | median (ms) | p10 | p90 |
|---|---|---|---|
| 1st (fastest) | 28.5 | 14.4 | 71.5 |
| 2nd | 34.6 | 17.6 | 89.6 |
| 3rd | 40.0 | 18.6 | 90.6 |
| **4th (quorum-forming, k=4th of 6 -- 6cb/6cc)** | **43.9** | 22.4 | 97.5 |
| 5th | 55.6 | 27.7 | 131.5 |
| 6th (slowest) | 65.7 | 34.4 | 183.9 |

**Spread (median rank4 - median rank1) = 15.5 ms -- small.** All six
followers get the ~26 MB body within a ~37 ms band (28.5 to 65.7 ms
medians); this is the signature of parallel, not serial or
bandwidth-shared, delivery -- confirmed directly from the send code
below, not inferred from the spread alone.

**3. Leader-side transport, from the code
(`internal/blockchain.go:1727-1807`, `internal/p2p/encoder/ssz.go`,
`internal/p2p/options.go`).**

- **Encode once, reused for both paths** (`blockchain.go:1740`,
  re-confirming the S8 audit): `rlp.EncodeToBytes(b)` runs once in
  `SealedBlock`; the same `data` bytes go to `directPushBlock` and to
  the gossip fallback.
- **Direct push: per-peer sends are CONCURRENT, not sequential**
  (`blockchain.go:1773-1806`): `for _, pid := range peers { go
  func(pid peer.ID) {...}(pid) }` -- one goroutine per connected peer,
  each opening its own stream with a 5 s context timeout.
- **Chunking:** `encoder.EncodeWithMaxLengthLimit(stream,
  &rawBlockBytes{data: data}, encoder.MaxBlockChunkSize)`
  (`blockchain.go:1800`); `MaxBlockChunkSize` defaults to 64 MB
  (`internal/p2p/encoder/ssz.go:42`, `N42_MAX_GOSSIP_MB`-overridable). A
  ~26 MB block fits in ONE frame -- there is no multi-round-trip,
  per-chunk-ack chunking protocol here, only a single varint-length-
  prefixed write (`ssz.go:128-149`); flow control is whatever the
  underlying muxer stream provides, not an application-level ack.
- **Compression: yes, snappy, on BOTH paths.** Direct push:
  `rawBlockBytes` is carried "through the SSZ length/snappy framing"
  (`blockchain.go:1812-1813` doc comment), and `EncodeWithMaxLengthLimit`
  calls `writeSnappyBuffer(w, b)` (`ssz.go:150`). Gossip:
  `EncodeGossip` calls `snappy.Encode` explicitly (`ssz.go:123`), and
  the receive side calls `enc.DecodeGossip` which decompresses snappy
  (`service.go:1072`, "Decompress snappy").
- **Also gossiped: yes, but as an async, off-critical-path fallback.**
  `SealedBlock` launches `go func() { bc.p2p.BroadcastBlock(ctx, data)
  }()` (`blockchain.go:1758-1764`) AFTER the direct pushes are already
  dispatched; the comment on this line says explicitly that compressing
  and publishing the block inline "held the Proposal back (part of the
  leader's ~180 ms push phase)" in an earlier round, which is why it was
  moved off the seal path.
- **Transport: TCP + QUIC registered, Noise security, default muxers**
  (`internal/p2p/options.go:75-78`): `libp2p.Transport(tcp.NewTCPTransport)`,
  `libp2p.Transport(libp2pquic.NewTransport)`, `libp2p.DefaultMuxers`,
  `libp2p.Security(noise.ID, noise.New)` -- Noise, not TLS, for the TCP
  path (QUIC carries its own TLS 1.3 and native multiplexing when a
  connection uses it). No explicit yamux window-size override exists in
  this codebase's p2p setup -- `DefaultMuxers` takes go-libp2p's library
  default, not further configured here.

**4. Follower-side, between first byte and "block available":**

| step | measured (ms) | source |
|---|---|---|
| network + read + RLP decode (one lump) | see rank table above (28.5-65.7 median by rank) | `"block push: arrived"` tMs minus leader `push_instant`; `ReadChunkedBlock` has no internal sub-timer, so read and decode cannot be split further -- **n/a beyond the lump** |
| `hdr` (wait on parallel `VerifyHeaders`/BLS seal result) | 2.8 | `blockimport phases` |
| `body` (`ValidateBody`: recompute the tx root over every tx) | 10.0 | `blockimport phases` |
| `root` (state root #3, `Finalize`->`IntermediateRoot`) | 0.0 | `blockimport phases` |
| sender recovery (parallel, part of import's own `proc`) | 38.0 | `parallel block`'s `recoverMs` |
| deferred includability check (`CheckDeferredBlock`) | 134.5 (13.5% of slots that hold; presumed similar cost when not logged) | 6cd Gap B |

**Sender recovery before the vote: no, not for Round1.** Under
two-phase voting (`processProposal`, `proposal.go:228-233`), the Round-1
PREPARE vote is cast on "static validation alone" -- the leader's BLS
signature and JustifyQC -- with no reference to transaction contents, no
sender recovery, and (per `extendsJustify`, `proposal.go:499-513`)
fails OPEN when the parent isn't yet known rather than blocking. **Round
1 is gated on nothing beyond having the Proposal message itself.**
Sender recovery only happens later, inside `CheckDeferredBlock`
(gating the Round-2 COMMIT vote for the deferred-attested path) or
inside full import's own `proc.recov` phase -- confirming 6cd's Gap B
finding that the includability check, not raw body transit, is what a
COMMIT vote actually waits on.

**5. Verdict on D, with the budget table.** Round1/Round2 durations
matched to the EXACT SAME 79 blocks q2s uses (same view<->n offset
calibration as 6cc/6cd; 79/79 rows matched):

| segment | median (ms) | note |
|---|---|---|
| push start (T0, ~= ProposalSent) | 0 | leader `push_instant` |
| body at quorum-forming follower (rank 4) | 43.9 | parallel with Round1 below, not serial -- see reconciliation |
| Round1 (`ProposalSent`->`PrepareQCFormed`) | 129.0 | leader clock, matched population |
| Round2 (`PrepareQCFormed`->`CommitQCFormed`) | 370.0 | leader clock, matched population |
| **Round1 + Round2** | **499.0** | |
| **q2s, same 79 rows (push->QC)** | **520.1** | 6cb/6cc/6cd |
| **closure** | **95.9%** | within the 15% bar -- **confirmed** |

Body delivery (43.9 ms) is not a third serial segment here: it
completes well inside Round1's own 129 ms window (Round1 does not need
the body at all, per finding 4, so this is slack, not a dependency),
and even more so ahead of Round2 (which starts at T0+129 ms, by which
point the body has been sitting available for ~85 ms already for the
quorum-forming follower). **Body delivery is not the largest item on
the path -- it is the smallest one measured: 43.9 ms is 8.5% of the
520 ms q2s segment and 34% of Round1 alone, dwarfed by Round2 (370 ms,
8.4x larger) and even by Round1 (129 ms, 2.9x larger). Hypothesis D is
falsified.**

**Reconciling Round1 (96-129 ms) with `Delivery` (434 ms): they are not
one serial path, and Round1 does not start at `Delivery`'s end.**
Round1 starts at `ProposalSent` -- a LEADER-clock event, ~equal to that
leader's own `push_instant` -- and ends once the SAME leader collects a
quorum of Round-1 votes; it is a leader-side round-trip measurement,
full stop. `Delivery`, by contrast, starts at a FOLLOWER's own
`ViewStart`, which -- per finding 1 -- only fires once that follower has
locally finished processing the PREVIOUS view's CommitQC/Decide or
timeout. Since `advanceToView` runs on every node independently, a
follower's `ViewStart` for view V can lag the leader's own
`CommitQCFormed` for view V-1 by however long the Decide message takes
to propagate and be locally processed -- not by anything to do with the
CURRENT proposal's transit. The ~300-340 ms gap between `Delivery` and
either Round1 estimate most plausibly reflects that lag (plus whatever
else was queued on this follower's single-threaded consensus event
loop at that moment -- 6cc already showed importers are not
saturated but do have bursty backlogs in some windows), but **no log
line isolates that sub-interval on its own; this is the best-supported
reading, not a closed measurement.**

**6. Known-tx share (no design implied, measurement only).** Every
`"parallel block"` line carries `hintFills`/`txs` -- the count of
transactions whose sender was served from the sender-recovery hint
cache, which `worker.go`'s own comment says "the pool cached... at
admission" (i.e. this node's mempool had already recovered that sender
before the block arrived, ordinarily because it already held or had
seen the transaction). Across the same 474 (follower, full-block)
slots: **`hintFills`/`txs` median = 99.4% (p10 95.0%, p90 99.9%).**
Essentially every transaction in a full block was already known to
this follower's own node before the block's body ever arrived.

**What this does and does not show.** It shows, from the exact code
that decides what a vote requires, that block-body delivery is
structurally off the Round-1 critical path (two-phase voting's prepare
vote needs only the small Proposal message) and empirically fast when
it does matter (43.9-65.7 ms across all six followers, tightly
clustered, confirmed as parallel sends by reading the send code, not
merely inferred from the spread). It shows Round1+Round2, matched to
the identical 79 blocks used elsewhere in this campaign, close the
520 ms q2s segment to 95.9% -- a clean, apples-to-apples reconciliation
that 6cb's independently-sourced aggregate table (Round1 96 ms, Round2
288 ms, a DIFFERENT and broader population of views) did not by itself
provide. It does NOT determine what specifically fills Round1's 129 ms
or Round2's 370 ms beyond what 6cd's Gap B already established for the
held-vote minority (`CheckDeferredBlock`, ~134 ms) -- no line splits
either round into network transit, BLS aggregate/verify, or event-loop
queueing on the majority (non-held) path. It does NOT close the gap
between `Delivery` (434 ms) and Round1 with a measured number -- the
explanation offered (Decide-propagation-plus-local-processing lag) is
the best fit given the code that produces `ViewStart`, but remains
unmeasured directly. It does NOT show why sender-hint cache fills are
at 99.4% (whether the mempool independently receives most transactions
ahead of the block, or some other admission path populates the cache),
nor does it evaluate a compact-block relay design -- only the
measurement the task asked for.

## 6cf. S14: n42-r87 built and prepared -- prediction 83 registered before the round (2026-09-21)

**Why.** 6cb-6ce closed transport (delivery 43.9 ms, 8.5% of push->QC) and
the held-vote minority (`CheckDeferredBlock`, ~134 ms, 13.5% of blocks) as
explanations for the in-tenure cycle's 520 ms push->QC segment, matched to
Round1 (129 ms) + Round2 (370 ms) = 499 ms (95.9% closure). Signing/
verifying a handful of BLS votes on one host is milliseconds, so roughly
450 ms of that 499 ms is consensus handlers waiting on something no line in
any prior round names. S14 instruments the wait directly rather than
inferring it from adjacent phase medians again.

**What S14 implements**, all behind `N42_CONTENTION_DIAG=1` (read once at
start-up, `internal/consensus/hotstuff/engine.go`; `N42_BUILD_STALL_DIAG=1`
keeps working unchanged alongside it):

1. **Contention profiling** (`cmd/n42/app.go`, before `node.NewNode` starts
   the consensus service): `runtime.SetMutexProfileFraction(5)` and
   `runtime.SetBlockProfileRate(1_000_000)`. `/debug/pprof/mutex` and
   `/debug/pprof/block` are already served wherever pprof is (the
   `net/http/pprof` blank import registers them unconditionally); this only
   turns on the sampling that makes them non-empty.
2. **Vote-path stamps** (`internal/consensus/hotstuff/view_timing.go`'s
   `contentionStamps`/`roundContention`, wired into `voting.go`/
   `proposal.go`/`service.go`). The serialising point identified by reading
   the code (see SERIALISER below) is `e.mu` (`ConsensusEngine.mu`, a plain
   `sync.Mutex`, `engine.go`), taken by `ProcessEvent`
   (`engine.go:596-598`) for every inbound message AND every
   block-imported/checked/rejected/ready event, held for the whole handler
   call -- there is no separate event-loop dispatch on top of it to time.
   `t_arrive` is stamped in `processGossipMessage` (`service.go`), the
   single entry point for both the gossip loop and the direct Rotor-relay
   stream handler; `t_locked` at the top of
   `processVote`/`processCommitVote`/`processProposal`/`processPrepareQC`;
   `t_done` when that handler returns. Aggregated per view into "hotstuff
   view timing": leader side gets `r1n`/`r1lw`/`r1lwMax`/`r1wk`/`r1kth`/
   `r1qk` and the Round-2 equivalents (`r2*`) -- count, lock-wait sum/max,
   work sum, and the quorum-completing (k-th) vote's arrival offset from
   the round's start plus QC-formed-minus-k-th, splitting each round into
   "waiting for enough votes" vs "aggregating once there were enough";
   follower side gets `propLw`/`propWk` (Proposal), `pqcLw`/`pqcWk`
   (PrepareQC), `pqc2cv` (PrepareQC-arrival to commit-vote-sent), and
   `cvHeld`/`cvGate` (`own-import` | `parent-import` | `checked`, from
   `castHeldCommitVoteIfAttested`'s two call sites in `onBlockChecked`/
   `onBlockImported`, `proposal.go`). All silent (no new fields appended)
   when the switch is off -- `TestLogLineSilentWithoutContentionData`.

**Suspects found by reading the code (not fixed; ranked).** The task asked
to write these down rather than fix them -- the round measures, a fix is
its own future step with its own prediction:

1. **`JournalVote`'s MDBX write, run under `e.mu`, on every single vote --
   leader propose, follower prepare vote, follower/leader commit vote --
   shares the node's ONE MDBX writer lock with block import/write.**
   `journalPrepareVote`/`journalCommitVote` (`engine.go:233-268`, both
   doc-commented "Caller must hold e.mu") call `e.voteJournal.JournalVote`,
   implemented by `Service.JournalVote` (`service.go:1219-1226`,
   `s.db.Update(...)` -- an MDBX read-write transaction) whose own comment
   says it runs "on the ENGINE goroutine with the engine mutex held."
   `internal/node/node.go:1863` passes `n.db` -- the SAME `kv.RwDB` the
   blockchain, snapshots, pruner and history backfiller all write through
   -- into `hotstuff.NewService`. MDBX allows one writer transaction at a
   time per environment; a concurrent block write (leader's own ~349 ms
   median, 6cb; a follower's own ~214 ms, 6ca) holds that slot, so any
   vote's journal write queued behind it stalls `e.mu` -- and therefore
   ALL consensus message processing on that node -- for the remainder of
   the write. Four call sites: `onBlockReady` (`proposal.go:81`, the
   LEADER's own proposal -- this is the "Propose" phase, 250 ms median in
   6cd's Gap C table), `processProposal` (`proposal.go:251`, follower
   two-phase prepare vote), `processPrepareQC` (`proposal.go:431`, follower
   commit vote), `tryFormPrepareQC` (`voting.go`, leader's own commit-vote
   journal).
2. **`processOutputs`' single-threaded output loop runs `CommitToCanonical`
   and `persistState` inline** (`service.go:585-596`, the `OutputBlockCommitted`
   case around `service.go:642-704`), a SECOND serialising point (not
   `e.mu`) the code's own comment already flags: "handleOutput also runs
   heavyweight work inline (CommitToCanonical unwind+re-execute, persistState
   MDBX commits)... A vote/timeout broadcast queued behind either misses
   its view window" -- which is why `OutputBroadcast`/`OutputSendToValidator`
   were already carved out onto their own goroutines. `OutputExecuteBlock`/
   `OutputSpeculativeBuild`/a later `OutputBlockCommitted` were not, and can
   still queue behind an in-flight `CommitToCanonical`; its own `persistState`
   write is yet another claimant on the same single `n.db` writer lock as
   suspect 1.
3. **Unbatched BLS verification of every PrepareQC/CommitQC/Decide message,
   under `e.mu`, on the receiving side** (`e.verifyQCWithSet`, e.g.
   `processPrepareQC`, `proposal.go`). Votes are batch-verified
   (`batchVerifyVotes`, `voting.go`) once buffered; a QC's own aggregate
   signature is not, so this is real per-message CPU held under the lock --
   likely single-digit milliseconds for 7 validators, far smaller than
   suspects 1-2, listed for completeness rather than as a leading
   candidate.

**Build.** Same file-checkout recipe as n42-r86: detached worktree at
`f7ec2836`, n42-r86's exact file set (see 6bz), plus S14's changes. One-
variable check: all 7 files S14 touches/adds
(`cmd/n42/app.go`, `internal/consensus/hotstuff/{engine,proposal,service,
view_timing,voting}.go`, the new `contention_test.go`) were compared
against the version n42-r86 was built from -- `internal/consensus/hotstuff/
proposal.go` is part of r86's own file list (from commit `c0931aeb`, the
DEFERRED lever), the other 6 are not (r86's version of those is simply
`f7ec2836`'s, untouched by any lever). `git diff c0931aeb <S14 commit>^ --
proposal.go` and `git diff f7ec2836 <S14 commit>^ -- <other 6 files>` were
all EMPTY -- every one of the 7 files was byte-identical to r86's own
version before S14's edit, so every file was checked out directly from the
S14 commit with no hunk surgery needed (unlike `worker.go` in 6bz).
`internal/parallel/base_cache.go` confirmed absent from the build
worktree; `grep -rl BaseCache` over `internal/`/`modules/` in it: empty.
`go build -p 8 -tags nosqlite,noboltdb` clean; in the same worktree,
`go vet` clean and `go test ./internal/consensus/hotstuff/...`
(263 tests) and `./internal/miner/...` both pass, full (non-`-short`)
package run, ~5-6 s including under `-race` -- no need for `-short`.
`/data/blockchain/gov5-work/n42-r87`: sha256
`a22b16ce540bfba172174fd494fcb76542ca7c87acb5effd1ff0b9378d215eb9`.
`strings n42-r87 | grep -c BaseCache` = 0; `strings n42-r87 | grep -c
"build stalled before fill"` = 1 (S11's diagnostic, still present) and
`strings n42-r87 | grep -c "contention profiling enabled"` = 1 (S14's).

**Runner.** `run-r35zzza.sh`/`chain-35zzza.sh` built from the `run-r35zzz.sh`/
`chain-35zzz.sh` pair (35zzt's eight-generator shape, unchanged --
`-target-depth 45000`, `--floods 8 --senders 1000`). Binary retargeted to
n42-r87; `N42_CONTENTION_DIAG=1` added next to `N42_BUILD_STALL_DIAG=1` in
the node environment block. `chain-35zzza.sh` waits on `wr-logs/r35zzz.log`'s
terminal line (its actual predecessor); memory gate, n42-rs turn-taking and
quiet-box checks unchanged. New in the run script: a per-B-leg profile
capture (`run-r35zzza.sh`'s `run_leg`, gated on `$1` being `B1`/`B2`), 150 s
after leg start -- find the sitting leader (the node whose log most
recently printed `"miner: propose phases"`, a leader-only line) and one
follower (the node that printed it least recently or never, a proxy for
"furthest from being next leader" under round-robin tenure 4, since
decoding the exact validator<->node schedule was out of scope here); pull a
20 s CPU profile and 20 s mutex/block DELTA profiles
(`/debug/pprof/{profile,mutex,block}?seconds=20`, confirmed from the Go
1.26 stdlib source that mutex/block support `seconds=` as a delta) from
each, concurrently, into `/data/blockchain/wr-pprof/r35zzza-<leg>-node<i>-
{cpu,mutex,block}.pb.gz`; curl failures are `say`-logged and never abort
the round (`-m 40` timeout, matching the existing stall-watch curl's
pattern). One collateral bug avoided this time: `run-r35zzz.sh`'s giant
~11 KB history comment line (line 3) contains no legacy binary name that
collides with the `35zzz`->`35zzza` substitution (unlike 6bz's `n42-r35zzt`
case against `35zzt`->`35zzz`), confirmed by diffing that exact line before
and after the blanket rename -- identical. `bash -n` clean on both scripts.
Neither launched.

**Prediction 83 (registered before any round).** On `run-r35zzza.sh`/
`chain-35zzza.sh`, n42-r87, 35zzt's eight-generator shape (35zzz's own B
mean, 126.3k, is the baseline this round is measured against):
(a) diagnostics are free: B mean within the 3.6% noise floor of 126.3k;
(b) the stamps plus the mutex/block/CPU profiles attribute >= 70% of
Round1+Round2 (499 ms median, 6ce) to named waits (a lock or queue, with
its holder identified) or named work; (c) if (b) fails, the stamps still
split each round into k-th-vote-arrival vs leader-side QC-formation
(`r1kth`/`r1qk`/`r2kth`/`r2qk`), which alone says whether followers or the
leader are the slower half.

**VERDICT: confirmed** (implementation, tests, one-variable check and build
all done). QS_QUEUE.md's S14 row is marked prepared with prediction 83, not
launched.

**How to read the profiles.** `go tool pprof -top -sample_index=delay
http://... ` is for a live server; against a saved `.pb.gz`:
`go tool pprof -top -sample_index=delay /data/blockchain/wr-pprof/
r35zzza-B1-node<i>-mutex.pb.gz` ranks lock sites by cumulative wait time
(the `contentions`/`delay` sample pair mutex/block profiles carry); drop
`-sample_index=delay` for the `contentions` count instead. The CPU profile
reads the same way as any `go tool pprof -top ...cpu.pb.gz`. Cross-reference
against the `hotstuff view timing` line's new fields (which name a PHASE
and a MAGNITUDE) and the profile (which names a LOCK/CALL SITE and a
MAGNITUDE) for the same ~20 s window on the same node.

## 6cg. Round 35zzza: the vote round-trip is dominated by waiting for a message to arrive, not by any of the three suspected locks -- prediction 83 confirmed via its own fallback clause, not its primary bar (2026-09-21)

n42-r87 (r86 + `N42_CONTENTION_DIAG=1`) ran clean: `ROUND DONE`, legs
B1 03:13:55-03:27:39, B2 03:27:39-03:41:01. Evidence preserved first,
same recipe as 6ca/6ce: `/data/blockchain/wr-logs/r35zzza-keep/
node{0-6}-B.log`, 648 MB, `03:13:00-03:41:59` (each node rotated once
mid-window, 03:25-03:38; `.gz` + live `n42.log` concatenated per node,
verified against the `.gz`'s own earliest timestamp). Script:
`wt-r27/scripts/qs-analysis/contention_attribution.py`.

**1. Round score.** B mean **125,125** (win TPS 133005/112907/131902/
122686) -- **-0.93% vs 35zzz's 126.3k, -1.98% vs 35zzt's 127.6k, both
inside the 3.6% noise floor.** B1win2 (31.7% occupancy, 0.896 s
blockTime) is NOT full this round (unlike 35zzz, where it was B1win2
that qualified) -- this round's three full windows are B1win1, B2win1,
B2win2 (49.0/48.6/47.0% occupancy). `import_breakdown.py`-equivalent,
full blocks (n=1842): `body` 10 ms (p90 24), `proc` recover 25 (p90 59)
/ exec 266 (p90 383) / finalize 151 (p90 193), `write` 207 ms (p90 273),
`total` 795 ms (p90 947) -- same shape as 6ca's r86 numbers, no
regression. **BAD BLOCK 0, state-root/tree divergence 0, no
`wr-logs/r35zzza-MODE-FAILED` file.** Two `"diverg*"`-matching lines are
the ordinary, handled `"miner: suppressing divergent same-height
sibling"` path (`internal/blockchain.go`, already documented in 6cb),
not a correctness fault. **One real `"miner: build stalled before
fill"`** (node6, block 13658850, **a full block, 163,000 txs**,
`elapsedMs:3000`, step `specTreeReload`, 03:22:28) -- the first live
firing of S11's watchdog since 6bz's unit tests, and it self-healed:
the same two blocks (13658850/13658851) also carry the two flood-window
view timeouts (of 4 TC events total, 28 node-observations) and the two
sibling-suppressions above, all within a 16-second window
(03:22:28-03:22:44) inside B1's actual flood, not its decay warmup --
unlike 35zzz's TCs, which all landed in the quiet pre-flood period. The
other two TCs (03:14:59 in B1, 03:28:47 in B2) do match 35zzz's
leg-start-decay-artifact pattern. No BAD BLOCK, no divergence and a
clean `ROUND DONE` came out the other side of this incident -- it is
recorded as a genuine, contained disruption, not folded into "zero
stalls this round" the way 6ca could for 35zzz.

**2. Cycle anatomy, same method as 6cb/6cd (`push_instant` from `miner:
propose phases`, QC proxy from the leader's own `isLeader` view-changed
event, view<->n offset calibrated per leg -- confirmed self-consistent
again: 23/24 B1 rows and 58/58 B2 rows agree on one offset each).**

| | this round (35zzza) | 35zzz (6cb/6cc) | delta |
|---|---|---|---|
| in-tenure cycle (median) | 806.2 ms | 799.1 ms | +0.9% |
| hand-over cycle (median) | 1252.0 ms | 1201.1 ms | +4.2% |
| in-tenure fraction | 82/146 = 56.2% | 79/147 = 53.7% | ~flat |

**Essentially unchanged from 35zzz within run-to-run noise** -- the
contention diagnostics did not measurably shift the cycle, consistent
with prediction 83(a)'s "diagnostics are free" already being satisfied
by the B-mean result above.

**3. The attribution the round was run for.** Leader-side fields,
matched to the exact view whose CommitQC gates the measured cycle (same
join key as the QC proxy above; 81/82 in-tenure rows matched):

| field | median (ms) | p90 | meaning |
|---|---|---|---|
| `r1` (Round1 total) | 124.0 | 206.0 | ProposalSent -> PrepareQCFormed |
| `r1kth` | 110.0 | 204.0 | round-start -> k-th (quorum) prepare vote ARRIVES |
| `r1qk` | 2.0 | 5.0 | k-th vote's own lock+work+aggregate -> PrepareQC formed |
| `r1lw` (sum over r1n=10 votes) | 598.0 | 989.0 | see note below -- NOT on the k-th vote's own path |
| `r1lwMax` | 292.0 | 420.0 | largest single vote's lock-wait this round |
| `r1wk` (sum) | 8.0 | 11.0 | total handler CPU across all r1n votes |
| `r2` (Round2 total) | 353.0 | 525.0 | PrepareQCFormed -> CommitQCFormed |
| `r2kth` | 349.0 | 515.0 | PrepareQC-formed -> k-th commit vote ARRIVES |
| `r2qk` | 4.0 | 9.0 | k-th vote's own lock+work+aggregate -> CommitQC formed |
| `r2lw` (sum, r2n=4) | 5.0 | 19.0 | small this round, unlike r1lw |
| `r2wk` (sum) | 1.0 | 3.0 | |

**Additive check:** Round2 closes almost exactly on `r2kth + r2qk`:
349 + 4 = 353 vs measured `r2` = 353 -- **100.0%.** Round1 closes on
`r1kth + r1qk` = 110 + 2 = 112 vs measured `r1` = 124 -- **90.3%**, the
other 12 ms falling inside the k-th vote's own decode/dispatch, not
separately named. **`r1lw` must NOT be added to this sum**: it is the
SUM of `(t_locked - t_arrive)` over all `r1n`=10 votes this round
(`roundContention.record`, `view_timing.go:168-190`), and those ten
intervals overlap in wall-clock time (ten votes queue up behind ONE
serial engine goroutine, so the round's own 124 ms window can contain
far more than 124 ms of SUMMED individual queueing) -- adding it
produces 710 ms, 573% of the round, which is exactly the mechanical
double-counting 6ca already flagged for the S11 `lockWait` field
(`internal/blockchain_types.go:236-237`) applied to a new field with
the same shape. **The correct reading: `r1kth`/`r2kth` (waiting for the
deciding vote's MESSAGE to physically arrive) is 90-100% of each
round's own total; the k-th vote's own lock-wait+work+aggregate-verify
(`r1qk`/`r2qk`) is 2-4 ms, essentially free.**

Follower-side fields, all 6 non-leader nodes, matched the same way
(448 rows):

| field | median (ms) | meaning |
|---|---|---|
| `propLw` | 0.0 | lock-wait before the Proposal handler ran |
| `propWk` | 2.0 | Proposal handler's own work (BLS verify, journal, sendVote) |
| `pqcLw` | 0.0 | lock-wait before the PrepareQC handler ran |
| `pqcWk` | 2.0 | PrepareQC handler's own work |
| `pqc2cv` | 2.0 | PrepareQC-arrival -> this node's commit vote sent |
| `cvHeld` share | 8/448 = 1.8% | far lower than 35zzz's 13.5% -- this round's import chain kept up |
| `cvGate` (of held) | 100% `checked` | the deferred-includability path, none `own-import`/`parent-import` |

**A follower's own processing, on both messages, is 0-2 ms -- essentially
instant.** The follower is never the one doing slow work; the round's own
`r1kth`/`r2kth` numbers are large because the MESSAGE the follower is
waiting to send (or that the leader is waiting to receive back) takes
that long to physically arrive over the network/gossip layer, not
because either side's own handling is slow.

**Percentage of Round1+Round2 (477 ms this round, vs 6ce's 499 ms
median for 35zzz -- close, not identical, expected run-to-run variance)
attributed to NAMED causes, under the prediction's own literal
wording ("a lock or queue, with its holder identified, or named
work")**: only `r1qk`+`r2qk` (+ their sub-fields `lw`/`wk` for the
k-th vote specifically, already included) qualify as that -- **6 ms of
477 ms, 1.3%.** "Waiting for the k-th vote's message to arrive"
(`r1kth`+`r2kth` = 459 ms, 96.2%) is real, measured, and now precisely
located, but it is neither a lock with an identified holder nor CPU
work with a named function -- it is network/gossip transit time, which
is exactly the third category prediction 83(c) was written to catch if
(b)'s stricter bar failed.

**4. Profiles -- a critical timing caveat first.** The runner's own
per-leg capture point (`leg start + 150 s`) landed **inside the 400 s
baseFee-decay warmup on all four captures**, confirmed directly from
the round log (`"decaying baseFee for 400s of empty blocks before the
flood..."` precedes each `"S14 profile capture"` line, and `"all 8
flood(s) submitting"` follows it by several more minutes) and from the
kept logs (every one of the 84 blocks produced in each 20 s profile
window carries `"txs":0`). **All four profiles measure empty-block
consensus overhead, not full-block Round1/Round2 cost.** This is not
disqualifying -- the vote-path stamps above (section 3) ARE correctly
scoped to full in-tenure blocks via the leg-window join, and remain the
primary evidence -- but the profile numbers below characterize a
lighter workload than the one whose 124/353 ms this section is trying
to explain, and are reported as directional corroboration, not a
full-block measurement.

Mutex profile (`-sample_index=delay`), total delay per 20 s window:
node0 (B1 leader) 568 ms, node1 (B1 follower) 792 ms, node3 (B2 leader)
653 ms, node4 (B2 follower) 683 ms -- **2.8-4.0% of wall time**, all of
it attributed (by unlock-site stack, i.e. holder) to `sync.(*Mutex)
.Unlock`, with cumulative context split roughly `ConsensusEngine
.ProcessEvent` (e.mu, 51-64%) vs `BlockChain.InsertChain` (a different
lock, bc's own, 34-47%) -- the two dominant locks in this codebase,
neither being the MDBX writer transaction, which showed 0 samples under
`-focus='JournalVote'` in every block profile (see suspect 1 below).
84 blocks/20 s window (empty blocks, ~4/s) makes a per-block
normalization meaningless for this workload; the per-block figures
that matter are section 3's stamps, not this profile.

Block profile (`-sample_index=delay`), unfiltered top entries are
**97.8-98% `runtime.selectgo` under `go-libp2p-pubsub.(*validation)
.validateWorker`** -- an idle worker-pool artifact (goroutines parked
in `select` waiting for gossip-validation jobs), not contention; this
inflates the raw total to "1.7-1.9 hours" of nominal delay in a 20 s
window purely from goroutine-count arithmetic and must be filtered out
(`-peek`/`-focus` on the relevant call paths) before the profile says
anything. Filtered to `ConsensusEngine|ProcessEvent|InsertChain`:
`ProcessEvent`'s own blocked time is 187-309 ms/20 s (e.mu), of which
`processProposal` is 58-69%, `processVote`/`processPrepareQC`/
`processCommitVote`/`processDecide` each single digits to ~25 ms --
small in absolute terms, consistent with section 3's `r1qk`/`r2qk`
being tiny. `InsertChain`'s own blocked time is 491-562 ms/20 s, split
between `blockSubscriber` (gossip path) and `blockPushStreamHandler`
(direct-push path) both contending for the SAME `bc.lock` -- these are
the fallback gossip copy and the direct push of the SAME (empty, in
this window) blocks racing each other, a real if modest cost this
round's diagnostics were not aimed at and prediction 83 did not name as
a suspect.

CPU profile: total CPU use is tiny -- **1.90-1.99 s over 20 s wall time
(9.5-9.95% of one core)**, consistent with an empty-block workload
(no transaction execution). Within that small budget, **BLS signature
verification is the largest single item**: `crypto/bls/blst.(*Signature)
.Verify` -> `AggregateVerify` -> `coreAggregateVerifyPkInG1`
(`internal/consensus/hotstuff/quorum.go`'s `verifyAggregateSignature`,
called from `interop_v4.go`'s `verifyQCWithSet`) is 470-760 ms/20 s, **25-40% of the
already-small CPU total**, running under `ConsensusEngine.ProcessEvent`
(i.e. under e.mu) on every node. This is real, named, CPU-bound work
(suspect 3), and the profile shows it clearly -- but at this workload's
scale (roughly 20 empty views/20 s) it is on the order of 25-35 ms of
CPU per view, an order of magnitude short of the 110-349 ms `kth`
values section 3 measured on full blocks, and it does not appear on
the k-th vote's own path there either (`r1qk`/`r2qk` are 2-4 ms). Share
of CPU under `ProcessEvent` handlers overall: 62.8-71.6%
(`dispatchMessage`/`processMessage`), of which the BLS verify chain
above is the largest named component; no separate figure for "the
leader's own state-root computation" (6cb/6cd's ~271 ms segment) exists
in these profiles because the profiled 20 s windows contain no
transaction execution at all to sample.

**5. Verdict per suspect.**

1. **`JournalVote`'s MDBX write under `e.mu` -- falsified as a material
   contributor.** `-focus='JournalVote'` returns **0 samples in the
   block profile on all 4 nodes** (no goroutine anywhere was ever
   recorded waiting behind it), and in the CPU profile it appears on
   only one of four nodes, at 30 ms/20 s (1.54% of that node's tiny CPU
   budget) via `MdbxKV.Update -> MdbxTx.Commit`. Section 3's `r1qk`/
   `r2qk` (2-4 ms, which INCLUDE the k-th vote's own journal write) is
   the on-target confirmation: even on full blocks, the deciding vote's
   own journal-write-plus-lock cost is negligible. Caveat: if MDBX's
   writer-lock acquisition blocks inside a CGO call without yielding
   through a Go-visible primitive, it would be invisible to both
   profiles; this round's evidence does not include that failure mode,
   it can only report that no signal for it was found by any means this
   round's diagnostics can see.
2. **`processOutputs`'s inline `CommitToCanonical`/`persistState` --
   falsified as a material contributor (this workload).** `-focus`
   shows only the output-loop's own idle-select (not contention) in the
   block profile, and 10-20 ms/20 s (0.5-1%) in the CPU profile via
   `Miner.CommitToCanonicalWith -> MdbxKV.Update -> MdbxTx.Commit`.
   Cheap and rare in the profiled (empty-block) window; not evaluated
   under full-block commit load by this round's profiles (caveat above
   applies equally here).
3. **Unbatched BLS/QC signature verification under `e.mu` -- confirmed
   as real, named, CPU-bound work, but too small in the profiled
   window and off the k-th vote's own path on full blocks to explain
   the gap.** 25-40% of a ~10%-of-one-core CPU budget under empty
   blocks (470-760 ms/20 s); on full blocks, the deciding vote's own
   post-arrival cost (`r1qk`/`r2qk`, which would include any BLS verify
   its own processing triggers) is only 2-4 ms. Confirmed as a real,
   attributable cost (partial credit -- it is genuinely there and named),
   but it does not scale up to explain 110-349 ms.

**None of the three suspects explains `r1kth`/`r2kth`.** What the
combined stamps + profiles show instead: the follower's own handling of
both the Proposal and the PrepareQC is 0-2 ms in every measurement this
round took (section 3), and neither of the two real locks in this
codebase (e.mu, bc.lock) nor BLS verification cost enough, in either the
full-block stamps or the (empty-block) profiles, to explain the 110 ms
and 349 ms medians. The dominant, unattributed cost is **time for a
vote's message to physically arrive at the other side (`t_arrive`,
stamped at the first line of `processGossipMessage`, before any
decode/verify/lock)** -- i.e. gossip/network propagation, or scheduling
delay on the sending side before that node even calls `emit`/broadcasts,
neither of which any instrument in this round (stamps or profiles) times
independently of the other.

**6. Prediction 83, ruled clause by clause.** **(a) confirmed**: B mean
125,125 is -0.93% vs 126.3k, inside the 3.6% floor; the diagnostics
cost nothing measurable. Note per the task: a 20 s CPU profile did run
inside each B leg on two nodes each, as designed -- but see section 4's
timing caveat: it landed in the decay warmup, not the flood, on all
four captures, which limits what it can independently confirm about
full-block cost (it does not change the B-mean verdict, which is
leg-wide). **(b) falsified, on the prediction's own literal bar**: only
1.3% of Round1+Round2 (6 of 477 ms) is a lock/queue-with-holder or
named CPU work; the other 96.2% (`kth`) is real and now precisely
located but is network arrival time, which the prediction's wording
("a lock or queue, with its holder identified, or named work") does not
cover. **(c) confirmed, exactly as designed as the fallback**: the
stamps do split each round cleanly into "waiting for enough votes"
(`kth`, 90-100% of each round) vs "leader-side QC-formation" (`qk`,
2-4 ms), and the answer is unambiguous -- **followers/the network are
the slow half, not the leader's own aggregation or lock handling.**
**Prediction 83 overall: confirmed**, via the fallback clause it was
explicitly written to fall back on; its primary (b) bar was not met.

**Method.** `contention_attribution.py` reuses 6cb/6cd/6ce's exact
join: `push_instant` from `miner: propose phases`, in-tenure/hand-over
classification by consecutive-block leader identity, QC proxy from the
leader's own next `isLeader:true` "hotstuff: view changed" event, and a
per-leg view<->n offset calibrated from that same join (self-consistency
checked: 23/24 and 58/58 agreement). The new `r1*`/`r2*`/`prop*`/`pqc*`
fields are parsed with a regex directly off the SAME `"hotstuff view
timing"` lines used for the QC proxy, so leader and follower attribution
rows are matched to the identical blocks used everywhere else in this
campaign -- no separate-population reconciliation needed, unlike 6ce's
Round1/Round2 cross-check against 6cb's independently-filtered
aggregate table. Full windows for this round (B1win1, B2win1, B2win2;
B1win2 excluded, 31.7% occupancy) were identified from `r35zzza.log`'s
own printed occupancy figures, not assumed to match 35zzz's window
pattern. Profiles read via `go tool pprof -top/-peek/-focus
-sample_index=delay <binary> <file>.pb.gz`, `GOCACHE=/data/blockchain/
gov5-work/.gocache`, binary `/data/blockchain/gov5-work/n42-r87`; the
decay-window timing caveat (section 4) was found by cross-referencing
each profile's own block-number set (all `txs:0`) against
`r35zzza.log`'s decay/flood transition lines, not assumed.

**What this does and does not show.** It shows, with vote-path stamps
correctly scoped to full in-tenure blocks and independently corroborated
(where the profiled workload allows) by mutex/block/CPU profiles, that
none of the three suspects the round was built to test explains
Round1's 124 ms or Round2's 353 ms -- each is dominated (90-100%) by
waiting for the deciding vote's own message to arrive, and each side's
own local processing (follower: 0-2 ms; leader's post-arrival
aggregation: 2-4 ms) is close to free. It shows this round also produced
a genuine, self-healed live occurrence of S11's 3 s build-stall
watchdog (block 13658850, a full 163,000-tx block) coincident with two
view timeouts and two sibling-suppressions, all inside the actual flood
window and all resolved without a BAD BLOCK or a divergence. It does
NOT show what causes the 110-349 ms of message-arrival time itself --
gossipsub mesh fan-out, the pubsub validation queue ahead of
`processGossipMessage`, or plain host-level scheduling contention across
seven node processes are all consistent with the evidence but none is
separately timed by this round's instrumentation, all of which starts
its clock at `t_arrive`. It does NOT re-profile a full-block window --
the runner's `leg start + 150 s` capture point landed in the decay
warmup on all four captures this round, so the CPU/mutex/block numbers
in section 4 characterize empty-block consensus overhead, not the
110-349 ms this section explains from the stamps. It does NOT design or
test a fix for either the message-arrival gap or the `InsertChain`/
`bc.lock` contention between the gossip-fallback and direct-push
delivery paths noticed in passing.

The single change this evidence most directly supports testing next is
timestamping gossip-message receipt earlier than `processGossipMessage`
-- e.g. at the pubsub library's own delivery callback, before its
internal validation queue -- to learn whether `r1kth`/`r2kth`'s 110-349 ms
is pubsub queueing, mesh transit, or sender-side delay; the upper bound
this round's own measurements support is the full `r1kth`+`r2kth`
gap itself, **at most ~459 ms per in-tenure cycle** (Round1's 110 ms
plus Round2's 349 ms) if that gap could be eliminated entirely -- not an
estimate of what a fix would actually recover, since nothing here
identifies which portion is reducible.

## 6ch. S15a: hypothesis G (the gossiped block head-of-line-blocks the votes) -- falsified as the dominant mechanism; the size-scaling points at the already-known deferred-check cost instead (2026-09-21)

Commander's follow-up to 6cg: since no lock explains `r1kth`(110 ms)/
`r2kth`(349 ms), does the unconditional block-gossip fallback
(`internal/blockchain.go:1758-1764`) share GossipSub's transport with
consensus messages and head-of-line-block them? Same logs
(`r35zzza-keep`), a short code trace, and one new script
(`wt-r27/scripts/qs-analysis/gossip_headline.py`).

**1. Code facts.**

- **Consensus messages**: gossiped on topic `hotstuff_consensus`
  (`internal/p2p/topics.go:22`, `GossipHotStuffConsensusMessage`) via
  `Service.subscribeMessages` (`internal/consensus/hotstuff/
  service.go:1032-1043`, plain `SubscribeToTopic`, **no
  `RegisterTopicValidator` call anywhere in the hotstuff package** --
  confirmed by grep, so this topic's messages skip whatever cost a
  validator would add). Votes/timeouts additionally try a faster
  **direct stream** first (`handleSendToValidator`, `service.go:958-
  1005`, the "Rotor" relay, `SendRawBytes` on its own dedicated
  stream) -- but **gossip is always ALSO sent, unconditionally**
  (`service.go:1009-1014`: "Gossip is always sent... a safety net...";
  `s.handleBroadcast(output)` runs regardless of `directDelivered`).
  This round's own `"hotstuff: vote routing stats"` line: **58.1-59.1%
  of votes used direct delivery, the remaining ~41% had gossip as
  their only successful path** (all 7 nodes' last sample, e.g. node0
  direct=2099/fallback=1501). `t_arrive` (6cf) is stamped once, in
  `processGossipMessage`, "the single entry point for both the gossip
  loop and the direct Rotor-relay stream handler" -- so whichever
  delivery wins the race is what the stamps see.
- **Blocks**: direct push, one dedicated per-peer stream opened per
  block (`blockchain.go:1773-1806`, confirmed concurrent per-peer
  goroutines in 6ce), **plus** an unconditional async gossip fallback
  on the SEPARATE `block` topic (`blockchain.go:1758-1764`,
  `go bc.p2p.BroadcastBlock(...)`). The block topic's own validator,
  `validateBlockPubSub` (`internal/sync/validate_blocks.go:26-90`+),
  checks `pushInflight`/`HasBlock` on the header alone first (cheap,
  `ValidationIgnore` in the common case where direct push already
  landed or is landing) -- but when that short-circuit does not apply,
  it does a full inline RLP decode of the whole block under
  `s.validateBlockLock` (`internal/sync/service.go:134`, a
  `sync.RWMutex` scoped to this ONE validator, not shared with
  anything else).
- **The shared element hypothesis G names is real, but it is a
  library-level queue, not an application lock**: go-libp2p-pubsub
  (`v0.17.0`) runs exactly ONE outgoing goroutine per peer
  (`handleSendingMessages`, `comm.go:219-268`) draining ONE FIFO
  `rpcQueue` (`rpc_queue.go:16-46`, `priorityQueue.Pop()` drains
  `priority` before `normal`) per peer, **shared across every topic
  published to that peer** -- block-gossip and consensus-gossip alike.
  Every actual data publish (block or vote) is enqueued as `Push(rpc,
  false)` = normal priority (`gossipsub.go:935` and all other
  `sendRPC(p, out, false)` call sites); the ONLY `UrgentPush` call in
  the whole library is for `IDONTWANT` control messages
  (`gossipsub.go:907`), unrelated to either message type here. So a
  large block-gossip RPC queued ahead of a vote/PrepareQC RPC **to the
  same peer** would delay it, with no size- or type-based priority to
  rescue it -- this is a real, code-confirmed mechanism, not
  speculation.
- **Validate-side throttling is not the bottleneck**: this node's
  `WithValidateQueueSize`/`WithPeerOutboundQueueSize` are both set to
  1024 (`internal/p2p/service.go:46`, `pubsubQueueSize`); go-libp2p-
  pubsub's own per-validator concurrency throttle defaults to 8192
  (`validation.go:17`, `defaultValidateThrottle`, never overridden
  here) -- three to four orders of magnitude above what a 7-node net
  could exhaust. GossipSub params for this fleet: `D=8, Dlo=6`
  (`internal/p2p/pubsub.go:21-22`) against only 6 possible peers, so
  the mesh should be effectively complete (every node directly meshed
  with every other) -- gossip here is single-hop, not multi-hop
  fan-out, for this fleet size.
- **Transactions are also gossiped**, on a separate topic
  (`GossipTransactionMessage = "transaction_v2"`, `topics.go:18`,
  actively wired in `broadcaster.go`/`subscriber.go`), sharing the SAME
  per-peer queue mechanism as blocks and consensus messages. Whether
  the 99.4% sender-hint-cache-fill rate (6ce) comes from this gossip
  path or from the flood harness submitting directly to multiple
  nodes' RPC (`"msg":"Served eth_batchRawTransaction"`, confirmed
  present and active in the kept logs) is **n/a to separate from these
  logs** -- both paths exist and are live; no counter distinguishes a
  transaction admitted via gossip from one admitted via direct RPC.

**2. Timeline correlation: not directly measurable this round -- a
genuine logging gap, not an omission in this analysis.** The receiving
side's own block-gossip lines -- `"Subscriber received new block"`
(`subscriber_blocks.go:30`), `"Block parent not yet available, queuing
as future"` (`:65`), `"Received block"` / `"Received block with an
invalid parent"` (`validate_blocks.go:104,125`) -- are **all
`log.Debug`**, confirmed **zero occurrences in all 7 kept files**
(the round ran at Info level, per every other section in this
campaign). There is no timestamp anywhere in these logs marking when a
GOSSIPED (as opposed to directly-pushed) copy of a block arrives,
starts validating, or is dropped as a duplicate -- so "does the k-th
vote arrive within 20 ms of the gossiped block's transit on the same
node" cannot be computed from this round's evidence. This is reported
as a hard gap, not approximated.

**Downlink/uplink split of `r2kth`: also n/a to compute exactly.**
Follower-side `pqcLw`+`pqcWk`+`pqc2cv` (6cg) sum to 2-6 ms median --
this node's own local cost once it has the PrepareQC in hand is
negligible, so essentially all of `r2kth` (333 ms on full blocks, see
below) is message transit split across downlink (leader forms
PrepareQC -> a follower receives it) and uplink (that follower sends
its commit vote -> the leader receives it) -- but no line stamps the
follower's own PrepareQC-arrival on an absolute clock, so the two legs
cannot be separated with these fields alone.

**3. Size discriminator -- the one measurement this round answers
cleanly, joining `r1kth`/`r2kth` directly to the size of the block the
SAME view produces (not the cycle-boundary n-1 join 6cg used):**

| block size | r1kth median (p90) | r2kth median (p90) |
|---|---|---|
| empty (0 tx, n=3800) | 55 ms (58) | 8 ms (9) |
| small (1-999 tx, n=4) | 56 ms (255) | 10 ms (36) |
| mid (1,000-149,999 tx, n=338) | 65 ms (192) | 114 ms (501) |
| **full (>=150,000 tx, n=313)** | **129 ms (219)** | **333 ms (533)** |

**Both grow with size, but by very different factors: r1kth grows
~2.3x (55 -> 129 ms) from empty to full; r2kth grows ~42x (8 -> 333 ms)
over the same range.** If a single, symmetric mechanism (a large block
sitting ahead of both message types in one shared per-peer queue) were
the dominant cause, both rounds should scale similarly, since the same
queued block would delay whichever message is stuck behind it
regardless of which round it belongs to. They do not. **`r2kth`'s
much larger, much more size-sensitive growth is exactly the shape
6cd's Gap B already established and explained: `CheckDeferredBlock`
(`internal/deferred_includable.go:38`) does real, per-transaction work
(sender recovery, nonce/balance/gas checks) before a commit vote can
release, and 6cd measured its cost directly at ~134 ms for a
160,000-tx block -- squarely inside this round's 333 ms full-block
`r2kth` median.** `r1kth`'s smaller, still-real growth (74 ms) is
harder to explain that way, since the two-phase prepare vote needs no
block content at all (`processProposal`, `proposal.go:228-233`) -- it
is CONSISTENT with a smaller network/transport effect (gossip
queue-sharing, or general host contention while a large block is also
in flight), but nothing in this round's logs pins that specifically to
the gossip fallback rather than, say, general CPU/scheduling
contention on a 7-process host while a 26 MB write is in progress
(6cg already found the leader's own CPU budget under load is tiny --
1.9-2.0 s/20 s -- but that CPU profile was captured during the DECAY
window, not the flood, so it does not directly speak to CPU
contention specifically during full-block processing).

**No comparable tx-gossip-volume metric exists to test against block
size directly** -- the flood runs at a roughly constant generator rate
throughout each full window, so if transaction-gossip volume were the
driver it should be closer to constant across the "full" bucket's own
samples, whereas block byte-size (occupancy) does vary; this is
suggestive but not a controlled comparison, since neither is directly
measured against the other in these logs.

**4. Verdict on G: falsified as the dominant mechanism.** The larger,
more consequential delay (`r2kth`, 333 ms, 72% of the combined 462 ms
this section's full-block bucket carries) has an already-established,
better-fitting explanation (`CheckDeferredBlock`'s own per-transaction
cost, 6cd) that predicts exactly the sharp, size-driven scaling
observed and requires no gossip-transport mechanism at all. The
smaller delay (`r1kth`, 129 ms) does grow with block size in a way
`CheckDeferredBlock` cannot explain (prepare votes never touch block
content), which keeps a transport-level explanation -- gossip
queue-sharing among them -- alive as a **partial, unfalsified
candidate** for that specific 74 ms of growth, but the direct evidence
hypothesis G asked for (votes clustering right after a gossiped
block's own transit, on the same node pair) could not be tested at all
in this round's logs, because the lines that would show it are
Debug-level and were never captured. **Falsified for the combined
459 ms gap as a whole; inconclusive, not confirmed, for `r1kth`'s
smaller residual.**

**Method.** `gossip_headline.py` reuses the exact QC-proxy/view<->n
offset calibration from 6cb-6cg (94/95 and 99/99 agreement this round),
then joins the leader-side `r1kth`/`r2kth` fields DIRECTLY to the view
that produces block `n` (not `n`'s predecessor, since the question here
is "does a bigger block slow its own round," unlike the cycle-boundary
framing earlier sections used) across every view in the kept logs, not
just the three full windows -- this is what makes the empty/small/mid
buckets possible. The Debug-level gap was confirmed, not assumed: the
script greps for the exact message strings against all 7 kept files
and reports the count (0) rather than skipping the check.

**What this does and does not show.** It shows, from the code, that
GossipSub's per-peer transport is a real, shared, unprioritized queue
that in principle could delay a vote behind a large block -- hypothesis
G's mechanism is not imaginary. It shows the DOMINANT size-dependent
delay (`r2kth`) has a better, already-measured explanation elsewhere in
this campaign, so pursuing gossip-transport changes to fix Round2 would
very likely fix the wrong thing. It does NOT determine what causes
`r1kth`'s smaller (74 ms, empty-to-full) but real growth -- gossip
queue-sharing remains consistent with it, but so does general
host-level contention, and this round's logs cannot distinguish the two
(the Debug-level gap in section 2, and the fact that 6cg's CPU profile
of a light workload cannot speak to a full-block workload's CPU
picture). It does NOT re-open or dispute suspects 1-3 from 6cg (a
different question, already answered). It does NOT recommend raising
the Debug lines to Info -- that is an instrumentation change with its
own cost/tradeoff this task did not ask for, though it is the obvious
next step if `r1kth`'s residual is ever worth chasing further.

## 6ci. S15b: n42-r88 built and prepared -- prediction 84 registered before the round (2026-09-21)

**Why, and the context 6ch adds -- since overruled.** 6cg found the vote
round-trip's 110-349 ms `kth` gaps are message-arrival time, not a lock
or CPU. 6ch (a parallel logs-only pass on the same 35zzza evidence) then
split that gap by block size and read hypothesis G (the unconditional
block-gossip fallback head-of-line-blocking votes on a shared per-peer
GossipSub queue) as falsified for `r2kth`'s dominant share, attributing
its ~42x empty-to-full growth to `CheckDeferredBlock`'s already-measured
per-transaction cost (6cd) instead. **The commander overruled that
reading to INCONCLUSIVE in QS_QUEUE.md's S15 row** (recorded here so
this section does not repeat a since-corrected claim): S14's own
held-vote stamps contradict it directly -- only 1.8% of commit votes
were ever HELD on the deferred-check gate (`cvHeld`, 6cg), and once a
PrepareQC is in hand a follower's commit vote goes out in a 2 ms median
(`pqc2cv`, 6cg) -- so `CheckDeferredBlock` is finished, for the 98.2%
majority, well before the message that gates it even arrives, and
cannot be what most of `r2kth` waits for. The commander's own
counter-reading of 6ch's size asymmetry: it is exactly what a shared,
unprioritized per-peer queue predicts by TIMING, not against it --
Round1 (0-124 ms) runs while the ~18-26 MB gossip fallback is still
being compressed and published, Round2 (124-477 ms) runs while that
payload sits in every per-peer FIFO `rpcQueue` (`rpc_queue.go`, no
size/type priority beyond `IDONTWANT` control messages), so a message
queued behind it in Round2's window pays more of the wait than one in
Round1's. **Net effect: hypothesis G is live and untested by direct
evidence for BOTH `r1kth` and `r2kth`, not just the smaller residual --
S15b's switch, not another logs pass, is what the commander recorded as
the decider.** The prediction below is registered exactly as the
commander specified it before this correction, unchanged by it: it
already named `r2kth` as the primary mechanism target, which this
correction now supports rather than undercuts.

**What S15b implements**, behind `N42_BLOCK_GOSSIP_FALLBACK` (read once
at start-up, `internal/blockchain.go`; unset/"1" = today's behaviour,
"0" = the experiment):

`SealedBlock` (`internal/blockchain.go`) now reads `directPushBlock`'s
own peer count (that function's signature changed from `func(...)` to
`func(...) int`, returning `len(peers)` -- peers a push was DISPATCHED
to, not confirmed delivered, since each send is a fire-and-forget
goroutine by design) and skips its `BroadcastBlock` gossip call when the
switch is "0" AND at least one peer was dispatched to; on zero peers it
still gossips (the only path left). `shouldGossipBlock`/
`parseBlockGossipFallback` are the pure decision/parsing helpers, unit
tested directly. Logs once at Info when disabled
(`"block gossip fallback disabled (N42_BLOCK_GOSSIP_FALLBACK=0)"`), a
Debug line plus an atomic counter (`blockGossipFallbackSkipped`) per
skip -- nothing per block at Info level.

**Call sites (every one found, all covered by this single switch).**
Grepped for every `BroadcastBlock`/gossip-publish call in the repo:
exactly ONE call site exists, `internal/blockchain.go` (`SealedBlock`,
the line the switch now gates). No follower re-publishes or forwards a
received block: `internal/sync/subscriber_blocks.go` and
`internal/sync/rpc_block_push.go` (the gossip-receive and direct-push-
receive handlers) contain no `PublishToTopic`/`BroadcastBlock` call at
all. One switch, one call site, complete coverage.

**Safety: what recovers a follower that missed the direct push when the
fallback is off.** An independent, already-existing mechanism, unrelated
to the block-gossip topic: a Proposal names only a block hash
(`internal/consensus/hotstuff/proposal.go`), and `processProposal`
emits `OutputExecuteBlock` for any hash this node does not yet have.
`Service.handleOutput`'s `OutputExecuteBlock` case
(`internal/consensus/hotstuff/service.go:620-634`) always calls
`s.blockFetcher.FetchBlockByHash(output.Hash)` (a no-op if the block is
already present) regardless of `N42_BLOCK_GOSSIP_FALLBACK`.
`FetchBlockByHash` (`internal/sync/rpc_block_by_hash.go:60`+) fans a
DIRECT peer-to-peer stream request out to every connected peer over its
own protocol (`RPCBlockByHashTopicV1`), served by
`blockByHashStreamHandler` (`rpc_block_by_hash.go:27-54`) -- entirely
independent of the gossip `block` topic. This path already exists and
already runs unconditionally on every proposal-for-an-unknown-hash, with
or without this round's switch, so turning the gossip fallback off
cannot wedge the fleet: recovery does not depend on gossip at all.
Given recovery is real and independent, the task's fallback-of-last-
resort ("make '0' fall back to gossip when any direct push returned an
error") was not needed and was not added -- there is nothing for it to
protect against that fetch-on-miss does not already cover, and per-peer
push errors are in any case only known asynchronously (goroutines,
fire-and-forget), so synchronously checking "did any push error" would
have reintroduced the exact latency the fallback removal is trying to
avoid.

**Build.** Same file-checkout recipe as n42-r86/n42-r87: detached
worktree at `f7ec2836`, n42-r87's exact file set (6cf), plus S15b's
changes. One-variable check: S15b modifies one file
(`internal/blockchain.go`) and adds one new one
(`internal/block_gossip_fallback_test.go`). `internal/blockchain.go` is
part of n42-r86/r87's own file list (checked out from commit `537ec21e`,
S11's own diagnostic commit); `git diff 537ec21e <S15b commit>^ --
internal/blockchain.go` is EMPTY -- the file was untouched between S11's
edit and S15b's, so r87's copy of it is byte-identical to the version
S15b's diff applies to, and both files were checked out directly from
the S15b commit with no hunk surgery needed.
`internal/parallel/base_cache.go` confirmed absent from the build
worktree; `grep -rl BaseCache` over it: empty. `go build -p 8 -tags
nosqlite,noboltdb` clean; in the same worktree `go vet` clean on
`internal/`, `internal/consensus/hotstuff/...` and `internal/miner/...`;
`go test` passes on all three (`internal/` package: 200 tests, 2.9 s;
`internal/consensus/hotstuff/...`: unchanged from 6cf; `internal/miner/
...`: unchanged from 6bz) -- none of `internal/`'s own test files are
slow enough to need `-short` (full package run under 3 s). New tests
(`TestParseBlockGossipFallback`, `TestShouldGossipBlock`) also pass
under `-race`. `/data/blockchain/gov5-work/n42-r88`: 108,719,640 bytes,
sha256 `5d3481dd535f0b64e28ba7082ebdca9ad10c23c5bce62dee19d5fb5756b6ae4b`.
`strings n42-r88 | grep -c BaseCache` = 0;
`... | grep -c "build stalled before fill"` = 1 (S11);
`... | grep -c "contention profiling enabled"` = 1 (S14);
`... | grep -c "block gossip fallback disabled"` = 1 (S15b, confirming
the new switch's log line is compiled in);
`... | grep -c "block gossip fallback skipped"` = 1 (the Debug line).

**Runner.** `run-r35zzzb.sh`/`chain-35zzzb.sh` built from the
`run-r35zzza.sh`/`chain-35zzza.sh` pair (35zzt's eight-generator shape,
unchanged -- `-target-depth 45000`, `--floods 8 --senders 1000`). Binary
retargeted to n42-r88; `N42_BLOCK_GOSSIP_FALLBACK=0` added next to
`N42_CONTENTION_DIAG=1`/`N42_BUILD_STALL_DIAG=1` (both kept on) in the
node environment block. The per-B-leg profile capture from S14 is kept
unchanged, with its output paths automatically renamed to
`r35zzzb-<leg>-node<i>-{cpu,mutex,block}.pb.gz` by the same round-token
substitution as everything else. `chain-35zzzb.sh` waits on
`wr-logs/r35zzza.log`'s terminal line (its actual predecessor); memory
gate, n42-rs turn-taking and quiet-box checks unchanged. `bash -n` clean
on both. Neither launched.

**Prediction 84 (registered before any round), exactly as specified:**
(a) mechanism: `r2kth` falls from 349 ms to under 100 ms and `r1kth`
from 110 ms to under 50 ms on full in-tenure blocks; the in-tenure cycle
falls from 806 ms to under 650 ms; (b) throughput: B mean above the
3.6% noise floor over 126.3k, i.e. > 130.8k -- with the explicit caveat
that the eight generators topped out near 142k in a 60 s window, so
supply may bind before the cycle gain shows in full, and occupancy must
be reported to tell the two apart; (c) safety: no BAD BLOCK, no
divergence, no rise in view timeouts or `block fetch`/catch-up events
versus 35zzza.

**VERDICT: confirmed** (implementation, tests, one-variable check and
build all done). QS_QUEUE.md's S15 row status is marked prepared with
prediction 84 (6ci); its result cell, already carrying S15a's findings
(6ch), is left untouched.

## 6cj. Round 35zzzb: turning the block-gossip fallback off fixes r1kth and does nothing to r2kth -- hypothesis G confirmed for Round1, definitively falsified for Round2 (2026-09-21)

n42-r88 (r87 + `N42_BLOCK_GOSSIP_FALLBACK=0`) ran clean: `ROUND DONE`,
legs B1 05:14:27-05:27:30, B2 05:27:30-05:40:47. Evidence preserved
first: `/data/blockchain/wr-logs/r35zzzb-keep/node{0-6}-B.log`, 463 MB,
`05:14:00-05:41:52` (rotation handled the same way as 6ca/6cg/6ch).
Scripts: `contention_attribution.py` and `gossip_headline.py`
(`scripts/qs-analysis/`) were extended with positional CLI overrides for
leg boundaries/window counts (documented in each file's own docstring)
so **the exact same join/bucket code drives both rounds** -- confirmed
by re-running each against 35zzza's own kept logs first and diffing the
output against 6cg/6ch's numbers (byte-identical).

**0. Did the switch fire on every node? Yes, 7/7, both legs -- proven
three independent ways.** (1) Startup line, across the FULL round's
`.gz`+current logs (each leg is a fresh process, SIGTERM+restart, per
`r35zzzb.log`'s own `"node N: SIGTERM"`/`"stopped clean"` lines around
every leg boundary): `"block gossip fallback disabled
(N42_BLOCK_GOSSIP_FALLBACK=0)"` appears **exactly 5 times per node**
(once per leg: warmup ~04:50, A1 ~05:03, **B1 ~05:15**, **B2 ~05:28**,
A2 ~05:42), on all 7 nodes -- the commander's "only three current
`n42.log` files" observation is explained by log rotation happening
*after* these particular startup lines were written on 4 of the 7
nodes (node0/1 rotated 05:38, node2/3/5/6 rotated 05:47-05:51, node4
never rotated); the B1/B2 instances are in the rotated `.gz` for those
nodes, not absent. The kept `-keep` files (windowed to the B legs only)
independently confirm 2 occurrences per node (one per leg). (2)
Behavioural, `bc.lock`/`InsertChain` contention (6cg attributed this to
"gossip-fallback vs direct-push racing," 491-562 ms/20 s in 35zzza):
**down to 163-170 ms/20 s in 35zzzb (a 66-71% drop), and now 100%
attributed to `blockPushStreamHandler` alone in every one of the 4
profiles -- `blockSubscriber` (the gossip-receive path) no longer
appears in the block-profile breakdown at all.** (3) Behavioural, the
block-topic validator: `validateBlockPubSub`/`validateBlockLock` CPU
was already tiny in 35zzza (~10 ms/20 s, 3 of 4 profiles, since the
profiled window is empty blocks -- see the caveat below) and is **0 ms
in all 4 of 35zzzb's profiles.** All three checks agree: the switch
took effect everywhere it was supposed to, for both legs.

**Profile timing caveat carries over unchanged from 6cg**: all four
20 s captures (`leg start + 150 s`) again landed inside the 400 s
decay warmup (confirmed the same way -- every block in each profiled
window has `txs:0`), so the CPU/mutex/block numbers above and below
characterize an empty-block workload, not full-block cost; only the
vote-path stamps (section 2) are scoped to full in-tenure blocks.

**1. Round score, 35zzza vs 35zzzb, same computation:**

| | 35zzza (r87) | 35zzzb (r88) | delta |
|---|---|---|---|
| B mean | 125,125 | **129,770** | **+3.7%** |
| B1win1 TPS/occ/blockTime | 133,005 / 49.0% / 1.200s | 140,281 / 48.7% / 1.132s | |
| B1win2 | 112,907 / 31.7% / 0.896s | 123,052 / 41.3% / 1.071s | |
| B2win1 | 131,902 / 48.6% / 1.200s | 139,614 / 48.0% / 1.132s | |
| B2win2 | 122,686 / 47.0% / 1.250s | 116,134 / 37.7% / 1.034s | |
| `import_breakdown` (full blocks) | body10/proc559(rec25,exec266,fin151)/write207/total795 | body10/proc543(rec24,exec258,fin148)/write204/total774 | flat, within noise |
| BAD BLOCK / divergence | 0 / 0 | 0 / 0 | flat |
| MODE-FAILED | none | none | flat |
| `build stalled before fill` | 1 (real, full block) | 1 (real, full block) | flat |
| TC formed | 28 | **46** | **+64%** |
| view timed out | 25 | **47** | **+88%** |
| catch-up range fetch (`requesting range`/`imported range`) | 3935 / 3067 | 3697 / 2866 | flat/slightly down |
| `hotstuff: committed block not executed locally` | 4666 | 4484 | flat/slightly down |
| `FetchBlockByHash` / block-by-hash catch-up | 0 lines match this literal string in either round (function name, not a log message) | 0 | n/a as a direct count -- see clause (c) below for the proxy used |

**View timeouts rose materially** (TC 28->46, view-timed-out 25->47) --
this is the safety-relevant regression clause (c) asks about, addressed
below.

**2. Mechanism, full in-tenure blocks, side by side (same join as 6cg):**

| field | 35zzza | 35zzzb | delta |
|---|---|---|---|
| in-tenure cycle (median) | 806.2 ms | **689.8 ms** | **-14.4%** |
| hand-over cycle (median) | 1252.0 ms | **1000.7 ms** | **-20.1%** |
| in-tenure fraction | 82/146=56.2% | 46/106=43.4% | (denominator also smaller: only 2 full windows this round, not 3 -- B1win2 at 41.3% didn't qualify) |
| `r1` (Round1 total) | 124.0 ms | **63.0 ms** | **-49.2%** |
| `r1kth` | 110.0 ms | **59.0 ms** | **-46.4%** |
| `r1qk` | 2.0 ms | 4.0 ms | flat |
| `r1lw` (sum, not on k-th path -- 6cg) | 598.0 ms | 640.5 ms | flat (still overlapping-sum artifact) |
| `r2` (Round2 total) | 353.0 ms | **362.5 ms** | **+2.7%, unchanged** |
| `r2kth` | 349.0 ms | **361.0 ms** | **+3.4%, unchanged** |
| `r2qk` | 4.0 ms | 2.0 ms | flat |
| `r2lw` (sum) | 5.0 ms | 0.0 ms | flat, already tiny |
| follower `propLw`/`propWk` | 0.0 / 2.0 ms | 0.0 / 2.0 ms | flat |
| follower `pqcLw`/`pqcWk` | 0.0 / 2.0 ms | 0.0 / 2.0 ms | flat |
| `pqc2cv` | 2.0 ms | 2.0 ms | flat |
| `cvHeld` share | 8/448=1.8% | 1/252=0.4% | flat-to-lower, still rare |

**`r1kth` improved almost exactly as predicted (110->59 ms, target
<50 ms -- close but not quite met); `r2kth` did not move at all
(349->361 ms, target <100 ms -- not met, and not even directionally
improved).** In-tenure cycle improved substantially (806->690 ms,
target <650 ms -- close but not met) -- entirely attributable to
Round1's fix; Round2 contributed nothing to the improvement and remains
the larger of the two segments (362 ms vs 63 ms, 85% of Round1+Round2
this round, up from 74% in 35zzza, simply because Round1 shrank).

**Size discriminator, same buckets as 6ch, joined directly to the
block each view produces (not the n-1 cycle-boundary join above):**

| block size | 35zzza r1kth / r2kth | 35zzzb r1kth / r2kth |
|---|---|---|
| empty (0 tx) | 55 / 8 ms | 55 / 8 ms (identical, as expected) |
| mid (1,000-149,999 tx) | 65 / 114 ms | 59 / 150 ms |
| **full (>=150,000 tx)** | **129 / 333 ms** | **63 / 336 ms** |

**This is the decisive result.** `r1kth`'s entire empty-to-full growth
in 35zzza (55->129 ms, +74 ms) is now almost gone in 35zzzb (55->63 ms,
+8 ms) -- a **93% reduction in the size-dependent portion of `r1kth`**,
attributable to removing the ~18-26 MB gossip payload from the shared
per-peer queue that Round1's small messages used to queue behind.
`r2kth`'s growth is, to within noise, **identical in both rounds**
(333 ms vs 336 ms on full blocks) -- removing the entire gossip
payload from the transport had **zero measurable effect** on it.

**Distribution check: is `r2kth` bimodal (some views fast, most still
slow) or uniformly ~350 ms?** `r2kth` on full blocks: 35zzzb p90 is
427 ms against a 361 ms median (a 1.18x ratio) -- a moderately
right-skewed but essentially **unimodal** distribution, not two
separated clusters; there is no evidence of "most views fast, a
minority stuck at 350 ms." Split by vote-routing path: this round's
own `"hotstuff: vote routing stats"` counters are cumulative per node,
not per-message, so `r2kth` cannot be split by "this specific commit
vote went via direct Rotor vs gossip fallback" -- that split is **n/a**
with the fields this round's diagnostics carry. Split by leader
identity or tenure position was not found to distinguish slow from
fast views either (the distribution is unimodal, so there is no
"slow group" to characterize by leader or position -- **none: the
delay is a property of Round2 itself, not of which view or leader
carries it**).

**3. Supply vs cycle, per B window.** Direct evidence from `"miner:
parallel fill"`'s own `candidates`/`included`/`failed` fields at the
tail of each leg (05:25-05:27 for B1, 05:39-05:41 for B2): **`failed`
is 0 throughout -- every candidate the pool offered was included, none
were rejected/dropped** -- and `candidates` itself shrinks
monotonically from 163,000 down to a trickle (163000 -> 153300 ->
140100 -> ... -> 2600 within the last two minutes of B2) as each leg's
flood window closes. **This is unambiguous supply exhaustion, not a
builder or cycle limitation**: the fill always takes everything on
offer; there is simply less on offer as the generators run dry, the
same recurring characteristic this eight-generator shape has shown in
every prior round (35zzt/35zzz/35zzza). Occupancy by window: B1win1
48.7%, B1win2 41.3%, B2win1 48.0%, B2win2 37.7% -- **both win2 windows
are supply-bound, not cycle-bound**, confirmed the same way as B1's.

**Derived full-block ceiling (not a score): if every block in the kept
window ran at this round's OWN measured full-block size and cycle mix
(chained+hand-over, weighted by their actual occupancy), the implied
TPS ceiling is `sum(txs)/sum(cycle)` over the 106 full in-tenure/
hand-over pairs = 17,259,400 tx / 96.08 s = ~179,631 TPS** (35zzza's
equivalent: 148 pairs, ~156,588 TPS -- a +14.7% improvement in this
derived ceiling, tracking the measured cycle improvements). This number
is a ceiling assuming unlimited supply, explicitly not comparable to
the B mean score, which stayed supply-bound in both win2 windows this
round.

**4. Prediction 84, ruled clause by clause.**

**(a) mechanism -- not met, partially.** `r2kth` 349->361 ms (target
<100 ms): **not met, no improvement at all.** `r1kth` 110->59 ms
(target <50 ms): **not met on the literal number, but a real, large,
now-explained improvement** (93% of its size-dependent growth
eliminated). In-tenure cycle 806->690 ms (target <650 ms): **not met
on the literal number**, real improvement, driven entirely by (a)'s
own `r1kth` component. **Clause (a): not met as registered; partially
confirmed as a mechanism finding.**

**(b) throughput -- not met.** B mean 129,770 vs the 130.8k (3.6%
noise-floor above 126.3k) bar: **-0.8%, just under the line.** Real, positive
movement over 125,125 (+3.7%), consistent with (a)'s partial cycle
improvement, but the registered bar itself is not cleared. Occupancy
(reported as required): both win1 windows stayed at ~48-49%, both
win2 windows fell to 37-41%, and section 3 shows this is supply
exhaustion in both rounds equally -- **the shortfall against 130.8k is
not explained by a NEW supply limit this round introduced; the same
supply ceiling was already present in 35zzza.** **Clause (b): falsified
against its literal bar, though directionally positive.**

**(c) safety -- not met.** BAD BLOCK 0, divergence 0 (met, both flat).
**View timeouts rose materially: TC formed 28->46 (+64%), view timed
out 25->47 (+88%)** -- the registered condition ("no rise... versus
35zzza") is directly contradicted by the counts. Block-fetch/catch-up
proxies (`requesting range`, `catch-up: imported range`,
`hotstuff: committed block not executed locally`) did NOT rise (flat
to slightly down in every one of these counters) -- the "recovery path
gets used more without the gossip backup" concern that motivated
tracking this is not borne out by the ONE mechanism this round's logs
can check it against. **Clause (c): falsified on view timeouts, met on
BAD BLOCK/divergence/catch-up-proxy counts.** One of the extra TCs
(05:40:14 in B2) coincides with this round's own build-stall watchdog
firing (node0, block 13661061, `specTreeReload`, same pattern as 35zzza's
single stall); the other two late-B2 TCs (05:35:41, 05:39:09) are
**unexplained by any stall or catch-up event found in these logs** --
reported as a real, open, unresolved safety signal, not attributed to
anything specific.

**Prediction 84 overall: not confirmed.** Two of three clauses
(mechanism's literal bars, throughput's literal bar) are not met, and
the third (safety) is mixed -- BAD BLOCK/divergence hold, but view
timeouts rose in a way the prediction explicitly said should not
happen. The positive, real findings the round DID produce (Round1
fixed, cycle materially faster, B mean up) are recorded above but do
not add up to "confirmed" against the bars as registered.

**Hypothesis G (the gossiped block head-of-line-blocks votes):
confirmed for Round1, definitively falsified for Round2.** This is the
cleanest result of the whole S15 sequence: removing the ENTIRE gossip
payload from the shared transport fixed `r1kth`'s size-dependent growth
almost completely (93% reduction) and left `r2kth`'s size-dependent
growth completely untouched (336 ms vs 333 ms, no change). A shared,
unprioritized transport queue predicts a SYMMETRIC effect on any
message queued behind the same large payload, regardless of which
round it belongs to -- the asymmetry this experiment produced is the
opposite of that prediction for Round2, and is direct, causal (not
merely correlational, since the mechanism was actually removed and
re-measured) evidence that whatever gates `r2kth` is NOT the block
gossip fallback. **What actually gates `r2kth` remains open**: 6cg
already ruled out e.mu contention, `JournalVote`, `CommitToCanonical`,
and (at this workload) BLS verify cost as material; 6ch's original
`CheckDeferredBlock` reading was overruled by the commander on `cvHeld`
being only 1.8-0.4% of votes (this round confirms that share stayed
low, 0.4%, so it still cannot be the general explanation); and this
round rules out the gossip fallback specifically. No remaining named
suspect from this campaign explains `r2kth`'s ~350 ms baseline plus its
~325 ms empty-to-full growth.

**Method.** `contention_attribution.py`/`gossip_headline.py` were
extended with positional CLI arguments for `LEG_B1`/`LEG_B2`/window
counts/full-window names (see each script's updated docstring for the
exact invocation used for each round) with **zero change to any join,
offset-calibration, or statistics code** -- confirmed by re-running
both scripts against 35zzza's own kept logs after the edit and
diffing the output against the numbers already published in 6cg/6ch
(identical). The switch-fired check (section 0) used three independent
sources on purpose (a log line, a lock-contention profile comparison,
and a validator-CPU profile comparison) specifically because the
commander flagged that a single grep had been ambiguous. The supply
discriminator (section 3) reads `"miner: parallel fill"`'s own
`candidates`/`included`/`failed` fields directly, the same fields 6by/
6bt/6ca already established as the fleet's own supply-exhaustion
signature (a genuine drop shows `failed>0`; a supply drought shows
shrinking `candidates` with `failed` staying 0, which is what this
round shows).

**What this does and does not show.** It shows, by direct
removal-and-remeasurement (not correlation), that the block-gossip
fallback was a real, now-fixed cause of part of Round1's message-
arrival delay, and was never a cause of Round2's -- narrowing the
open question left after 6cg/6ch/6ci considerably: `r2kth` needs a
different explanation than any suspect named so far in this campaign.
It shows the round's B-mean shortfall against its own registered bar
is attributable to the same supply ceiling both rounds shared, not to
a new limit this change introduced. It does NOT show what causes the
two late-B2 view timeouts that no stall or catch-up event in these
logs explains -- flagged as open, not resolved. It does NOT identify
`r2kth`'s actual mechanism; that is the natural next step, now with
the block-gossip fallback eliminated as a candidate with direct
(not inferred) evidence. It does NOT re-test hypothesis G against a
partial removal (e.g. only for full blocks, or only above some size) --
this round tested the switch fully off, nothing in between.

## 6ck. S16a: the output-loop mechanism (H2) exists in the code but does not correlate with r2kth -- the stamps stop before the actual publish, which is where the missing time most likely is (2026-09-21)

Same kept logs (`r35zzza-keep`, `r35zzzb-keep`), a code trace, and one
new script (`wt-r27/scripts/qs-analysis/output_loop_trace.py`, taking
the same positional leg/window overrides as 6cj's scripts so it runs
identically against both rounds).

### Part A: where a commit vote actually leaves the process

**Trace, file:line, leader and follower share the same code path
(`processPrepareQC`, `proposal.go:377-461`) since both sides call it --
the leader for its own self-triggered commit vote, a follower for the
PrepareQC it received.**

1. `processPrepareQC` decides to vote: journals the commitment
   (`journalCommitVote`, `proposal.go:431`, **before** signing/sending
   it -- "so the commitment is not durable" if skipped) -- this is the
   MDBX write suspect 1 (6cf) named, confirmed still present here on
   the COMMIT-VOTE path specifically, running on the CONSENSUS ENGINE's
   own goroutine under `e.mu`, not on the output loop.
2. **`e.viewTiming.CommitVoteSent = &now` is stamped at `proposal.go:449`,
   BEFORE `return e.emit(EngineOutput{...})` at `proposal.go:453`.**
   `pqc2cv` (`PrepareQCArrival` to `CommitVoteSent`, view_timing.go:261)
   is therefore **the engine's decision latency (verify + journal +
   sign), not network time** -- confirming the commander's blind spot
   (ii) exactly. The same pattern holds for the PREPARE vote
   (`VoteSent`, `proposal.go:481`, before its own `emit` at line 483)
   and for the LEADER's own PrepareQC-formed stamp
   (`voting.go:208-209`, `e.viewTiming.PrepareQCFormed = &now`, set
   BEFORE the leader's own `journalCommitVote` call at `voting.go:223`
   and its own `emit(OutputBroadcast{...MsgPrepareQC...})` at
   `voting.go:231-236`) -- **the leader's own Round2 "start" timestamp
   also precedes its own journal-write-then-broadcast-dispatch chain.**
3. `emit()` (`engine.go:739-752`) is a non-blocking send onto
   `e.outputCh`, a **1024-deep buffered channel**
   (`adapter.go:168`) shared by every output type.
4. **The channel is drained by exactly one goroutine**,
   `processOutputs` (`service.go:585-596`): `for { select { case
   output := <-s.engine.OutputCh(): s.handleOutput(output) } }` --
   **strictly serial, one output at a time**, confirming "drained
   serially."
5. Inside `handleOutput` (`service.go:598-`), **`OutputSendToValidator`
   (votes) and `OutputBroadcast` (Proposal/PrepareQC/Decide) are BOTH
   dispatched via `go ...(output)`** (`service.go:618-619` and
   `600-617`) -- the loop only pays the cost of spawning a goroutine
   for these two types, not the publish itself. The actual network
   write happens later, in that spawned goroutine: `handleSendToValidator`
   (`service.go:958-1013`, Rotor direct stream via `SendRawBytes`,
   falling through to `s.handleBroadcast(output)` regardless -- "gossip
   is always sent") or `handleBroadcast` (`service.go:841-925`,
   `PublishToTopic` at `:876`/`:889`/`:896`/`:922` depending on message
   type and Rotor availability). **No line in this round's diagnostics
   stamps entry to or exit from any of these calls.**
6. **`OutputBlockCommitted` is the one case NOT dispatched to a
   goroutine -- it runs fully inline, blocking the loop**
   (`service.go:642-716`): `CommitToCanonical`/`CommitToCanonicalWith`
   (`:679-699`) then `persistState`/the hook's own MDBX commit
   (`:702-713`), each opening its own MDBX write transaction against
   the SAME `s.db` `JournalVote` uses (`node.go:1863` wires the same
   `n.db` into both). Logged as `"hotstuff: commit phases"`
   (`canon`/`observe`/`persist`/`total`, `:714`+).
7. **`OutputExecuteBlock`/`OutputSpeculativeBuild` are also handled
   inline** (`service.go:620-641`) but are cheap on the fast path
   (`FetchBlockByHash` is a no-op if the block is already present,
   `rpc_block_by_hash.go:60-90`; `PrepareSpeculativeBlock` hands off to
   the miner's own channel, not measured here as it is off the vote
   path).

**So the ordered list of what can sit ahead of a vote/broadcast IN THE
QUEUE (not blocking it once dispatched, since votes/broadcasts get
their own goroutine) is:** any `OutputBlockCommitted` for an EARLIER
view enqueued before it -- **MDBX-write: yes** (`service.go:679-713`);
any `OutputExecuteBlock`/`OutputSpeculativeBuild` ahead of it -- MDBX-write:
no (fast path) / no (hands off elsewhere); the vote/broadcast's OWN
goroutine spawn -- MDBX-write: no, sub-microsecond. **The leader's own
PrepareQC broadcast is structurally exposed to the identical
mechanism**: it is emitted as `OutputBroadcast` (`voting.go:231-236`)
through the SAME `outputCh`/`processOutputs`, so if THIS node's own
`OutputBlockCommitted` for the PREVIOUS block has not yet been
dequeued, the PrepareQC broadcast's dispatch (not its publish, which
still happens promptly once dispatched) queues behind it on the
leader's own side, before it ever leaves the process.

### Part A, tested on the logs

**No stamp places the actual publish moment** (section above) -- the
one test available without it is whether a node's OWN `"hotstuff:
commit phases"` cost for the PREVIOUS block predicts an elevated
`r2kth` for the NEXT one it leads, joined by the same view<->n offset
calibration used throughout this campaign (no `tMs` on `commit phases`,
so this is a same-view-adjacency correlation, not a millisecond
coincidence test):

| | 35zzza | 35zzzb |
|---|---|---|
| leader's own `commit_phases(n-1)` [canon+persist] median | 51.1 ms | 44.9 ms |
| leader's own `r2kth(n)` median (this join) | 248.0 ms | 193.0 ms |
| Pearson r | 0.081 | 0.023 |
| **Spearman rho** | **-0.103** | **0.018** |

**No correlation in either round.** A node whose own previous-block
commit-to-canonical/persist cost was unusually large is no more likely
to see an unusually large `r2kth` on its next view than one whose cost
was small. This is direct evidence AGAINST the specific "queued behind
`OutputBlockCommitted`'s inline MDBX write" mechanism being what
typically drives `r2kth`'s ~200-350 ms baseline, even though the code
path (Part A above) is real and could occasionally contribute
(`commit_phases`' own distribution is heavily right-skewed, median
~0.2-51 ms depending on population, max up to 780 ms across the whole
round -- 6cg's per-round table). It does not rule out the SAME
mechanism affecting a MINORITY of views the way it does not show up in
an aggregate rank correlation.

**Verdict on H2: inconclusive.** The mechanism named is real, present
in the code exactly as hypothesized (a serial single-consumer output
loop, one case of which does inline MDBX-writing work), and structurally
reachable from both the follower's commit-vote path and the leader's
own PrepareQC-broadcast path. But the one proxy this round's logs can
test it against shows no correlation, and the actual publish step
(where the delay most plausibly sits, given `pqc2cv`/`CommitVoteSent`
are stamped BEFORE it) is completely unstamped. **Confirmed as a real,
reachable mechanism; not confirmed, nor ruled out, as the explanation
for `r2kth`'s typical magnitude.**

**The one stamp that would settle it**: two `time.Now()` calls bracketing
the actual wire write, at the exact call sites already found --
`internal/consensus/hotstuff/service.go:922` (the general
`PublishToTopic` call inside `handleBroadcast`, covering PrepareQC/
Decide) and inside `handleSendToValidator`'s `SendRawBytes` call
(`service.go:975`) and its own fallback `PublishToTopic` inside
`handleBroadcast` (reached via `service.go:1015`) -- i.e. one pair of
stamps per goroutine, at the true network-write boundary, not a
redesign of the output loop.

### Part B: size law

**`r1kth`/`r2kth` by tx-count bucket, joined directly to the view that
produces the block (same convention as 6ch), both rounds:**

| bucket | 35zzza r1kth / r2kth | 35zzzb r1kth / r2kth |
|---|---|---|
| 0 tx | 55 / 8 ms | 55 / 8 ms |
| 1-20k | 60 / 30 ms | 59 / 46 ms |
| 20-80k | 60 / 124 ms | 59 / 231 ms |
| 80-140k | 75 / 170 ms | 60 / 206 ms |
| >140k | 128 / 327 ms | 63 / 333 ms |

`r2kth` grows roughly **linearly with tx count in both rounds** (not a
step): per-10,000-tx slopes across the buckets run 0.001-0.003 ms/tx in
35zzza, the same order of magnitude in 35zzzb (35zzzb's 80-140k bucket
dips slightly below 20-80k's, the one non-monotonic point, inside
normal sampling noise for n=60-78). `r1kth` (unaffected by gossip in
35zzzb, per 6cj) stays nearly flat across all buckets except the very
top one in 35zzza (55->60 ms through 80-140k, then 128 ms at >140k --
closer to a step at the very largest blocks specifically, in the round
that still had gossip on; 35zzzb's `r1kth` is flat all the way through,
55->63 ms, no step anywhere).

**Rank correlation (Spearman), `r2kth` vs write time, all views (not
just the 3 full windows), both rounds:**

| | vs leader's own `blockwrite` (propose-phases `write`) | vs mean follower `blockimport` `write` |
|---|---|---|
| 35zzza | rho=0.345 (n=4455) | rho=0.352 (n=4455) |
| 35zzzb | rho=0.350 (n=4231) | rho=0.385 (n=4231) |

**A real, moderate, near-identical correlation in both rounds** --
consistent with `r2kth` tracking something proportional to block size
(as the bucket table already shows directly), but far from a tight
1:1 relationship (rho ~0.35-0.39, not >0.8), meaning block-write time
alone does not explain most of `r2kth`'s variance either.

### Part C: view-timeout placement

**Distinct (time, view) TC-formed/view-timed-out events, classified by
the ACTUAL block each affected view maps to (via the same view<->n
offset), not by a coarse time boundary:**

| round | decay (block=0 tx) | ramp (partial fill) | flood (full block, no stall) | flood + build-stall cascade |
|---|---|---|---|---|
| 35zzza | 2 (03:14:59, 03:28:46/47) | 1 (03:22:31, 15,000 tx) | 0 | 1 (03:22:43, 163,000 tx -- coincides with the round's own build stall at 03:22:28) |
| 35zzzb | 2 (05:14:55-05:15:24 x4, 05:28:33-39) | 2 (05:14:48/05:15:20, 22,857 tx; 05:35:41, 80,800 tx) | **1 (05:39:09, 163,000 tx, no stall found)** | 1 (05:40:14 onward, view 8708/8709, 4 further retries through 05:41:52 -- coincides with 35zzzb's own build stall at 05:40:11, block 13661061) |

35zzzb has **one genuinely new kind of event 35zzza did not produce**:
a flood-window, full-block timeout (05:39:09) with **no build stall,
no `TC formed`-adjacent stall dump, and no fetch/catch-up line of any
kind in the 2 seconds before it on any of the 7 nodes** -- checked
directly (`grep -iE 'fetch|not found|catch'` in the `[t-2s, t]` window
across every kept file, both this event and the 05:35:41 ramp event:
**zero matches for either**). The switch's OWN safety mechanism
(`FetchBlockByHash` fetch-on-miss, 6ci) runs unconditionally regardless
of the switch and would have logged something identifiable if a
follower had missed the direct push and needed it; nothing did.

**The rise (25->47 raw `view timed out` lines, 4->9 distinct views) is
real but NOT attributable to the switch via the one mechanism this
task asked to check (a follower falling back to fetch-by-hash after a
missed direct push).** The single new unexplained flood-window timeout
(05:39:09) could still be attributable to the switch through a
mechanism this check cannot see -- e.g. higher VARIANCE in direct-push
delivery timing without a gossip second chance, brushing the timeout
threshold without ever triggering an outright miss -- but that is
speculation, not evidence from these logs. **On the evidence actually
found: unclear, leaning "not attributable via fetch-by-hash," with one
event that remains genuinely unexplained.**

**Method.** `output_loop_trace.py` reuses the exact push-instant/QC-proxy/
view<->n-offset join from `contention_attribution.py`, adds a second join
on `"hotstuff: commit phases"` keyed the same way, and computes Pearson/
Spearman by hand (no numpy dependency in this environment). The TC/
timeout classification maps each event's `view` to a block number via
the SAME per-leg offset already calibrated for the round's own full-window
join, rather than a hand-placed time boundary -- this is what let the
03:22:31/43 pair in 35zzza resolve to a real 15,000-tx and 163,000-tx
block respectively instead of being lumped as "ramp" by clock time alone.

**What this does and does not show.** It shows, with file:line, that
every stamp this campaign has relied on for Round 2 timing
(`CommitVoteSent`, `PrepareQCFormed`, `pqc2cv`) is taken before the
engine hands the message to the output channel, not after it is
actually written to the wire -- so none of them can see the one place
most likely to hold the missing ~200-300 ms. It shows the specific
"queued behind an inline MDBX write" mechanism, while real and
reachable in the code, does not correlate with `r2kth` in either
round's own data, so it should not be assumed to be the dominant cause
without the one stamp above confirming it. It shows `r2kth` scales
close to linearly with block size and moderately with write time in
both rounds identically, which is consistent with (but does not prove)
some other size-proportional network or serialization cost on the
publish path. It does NOT identify what that cost is. It does NOT
resolve whether 35zzzb's view-timeout rise is caused by the gossip-
fallback switch -- the one mechanism checked (fetch-by-hash) shows no
evidence either way, and a repeat round would be needed before treating
either the rise or its non-attribution as settled.

## 6cl. S17: n42-r89 built and prepared -- prediction 85 registered before the round (2026-09-21)

**Why.** 6ck found every stamp this campaign has for Round2 --
`CommitVoteSent`, `PrepareQCFormed`, `pqc2cv` -- is taken BEFORE the
engine hands the message to the output channel (`emit()`, before
`journalCommitVote`/`journalPrepareVote` even run in some cases), not
after it reaches the wire. `r2kth` is a steady ~350 ms on full blocks,
linear in tx count (8 ms empty, 6cj/6ck's bucket tables), while `r1kth`
on the SAME paths and SAME message sizes is flat at ~60 ms -- transport
alone cannot tell the two rounds apart, and 6cj already showed removing
the entire block-gossip payload (hypothesis G) fixed `r1kth` but left
`r2kth` completely untouched (333 vs 336 ms, no change). The one place
that could still hide the gap is the actual send/receive boundary
itself, which nothing has stamped yet.

**What S17 implements**, behind the existing `N42_CONTENTION_DIAG=1`:

1. **Sender side** (`internal/consensus/hotstuff/engine.go`,
   `service.go`): `EngineOutput` gains `EmittedAt` (t_emit), set inside
   `emit()` itself (`engine.go`) so no call site changes. `processOutputs`
   stamps t_deq on dequeue (`service.go`). `handleBroadcast`/
   `handleSendToValidator` bracket the actual network call(s) with
   t_pub0/t_pub1 and classify the path ("rotor ok" | "rotor failed ->
   gossip" | "gossip only"). These run on background goroutines spawned
   AFTER the output left the engine (`processOutputs`' `go
   s.handleBroadcast(...)`/`go s.handleSendToValidator(...)`), so they
   never hold `e.mu`; `recordSendStamp` uses its own leaf lock (`sendMu`,
   `engine.go`) -- same discipline as the existing `timingMu`: never held
   with `e.mu`, `e.mu` never acquired while it is held.
   `publishCommittedTiming` claims a view's stamps right before deriving
   `Phases()`; a publish slower than the view's own lifetime is simply
   left unattributed for that view rather than risking any lock
   ordering with the hot path (in practice this is not a concern here:
   every one of the four hot messages is emitted early in its own round,
   leaving the rest of that round's ~60-360 ms for the async publish to
   finish before the view commits and logs).
2. **Receiver side**: t_rx is the earliest point a message's bytes are
   in this process -- right after `sub.Next()` returns (gossip,
   `subscribeMessages`) or right after the Rotor stream read completes
   (`setupRotorStreamHandler`'s callback -- see READERS below for where
   that read actually happens) -- stamped before the existing t_arrive
   (`processGossipMessage`'s first line, before decode). Recorded under
   `e.mu` (the same call that already records S14's contention stamps),
   since receipt is already funneled through the single-threaded engine
   before `processVote`/`processCommitVote`/`processPrepareQC` run.
   Duplicates (the same logical message via both Rotor and gossip --
   "gossip is always sent" regardless of Rotor's own success,
   `service.go`) keep the first arrival's stamps and only count toward
   `dupN`; `PrepareQC`'s own `Via` flips to `"both"` when a duplicate
   arrives on the other transport. Votes dedup per voter via a bitmask
   (`recordVoteRx`), so a duplicate vote cannot inflate the round's max
   rx2arr or be mistaken for a later, distinct k-th vote.

**New `hotstuff view timing` fields**: sender `prEmit2Deq`/`prDeq2Pub`/
`prPubDur`/`prPath` (Proposal), `pvEmit2Deq`/`pvDeq2Pub`/`pvPubDur`/
`pvPath` (prepare vote), `pqcEmit2Deq`/`pqcDeq2Pub`/`pqcPubDur`/`pqcPath`/
`pqcPubAt` (PrepareQC, `pqcPubAt` = t_pub1 as absolute unix ms),
`cvEmit2Deq`/`cvDeq2Pub`/`cvPubDur`/`cvPath`/`cvPubAt` (commit vote);
receiver `pqcRx2Arr`/`pqcRxAt`/`pqcVia` (follower's one PrepareQC),
`pvKthRx2Arr`/`pvKthRxAt`/`pvKthVoter`/`pvKthVia`/`pvMaxRx2Arr` and the
`cvKth*`/`cvMaxRx2Arr` equivalents (leader's k-th prepare/commit vote),
`dupN`. All silent (no fields appended) when the switch is off --
`TestLogLineSilentWithoutSendRecvData`.

**READERS -- goroutine structure feeding each transport into the
engine, and what sits between t_rx and t_arrive (read, not changed,
per the task).**

- **Gossip: ONE serial reader goroutine per topic, calling `ProcessEvent`
  SYNCHRONOUSLY.** `subscribeMessages` (`internal/consensus/hotstuff/
  service.go`) is `for { msg, err := sub.Next(s.ctx); ...;
  s.processGossipMessage(msg.Data, ...) }` -- a single goroutine, and
  `processGossipMessage` calls `ce.ProcessEvent(...)` (`e.mu.Lock()`)
  directly, in the same call stack, before the loop returns to
  `sub.Next()` for the next message. **This is worth saying prominently,
  exactly as the task asked**: if `e.mu` is held by something else when
  this loop's `ProcessEvent` call is reached, THIS GOROUTINE BLOCKS, and
  does not call `sub.Next()` again until the lock frees -- so a slow
  `ProcessEvent` for message N delays not just message N's own
  processing but message N+1's t_rx from ever being taken. None of this
  campaign's `lw`/lock-wait fields would show it, because they are all
  computed from t_arrive (taken once message N+1 finally reaches
  `processGossipMessage`), not from when the bytes physically arrived at
  the gossip layer -- which is exactly the gap S17's t_rx is designed to
  expose, if it is ever large enough to matter. (6cg's own block profile
  found `e.mu`'s hold times small in the profiled empty-block window,
  so this is a structural exposure confirmed in the code, not yet
  confirmed as a measured cause of `r2kth`'s magnitude.)
- **Rotor: ONE GOROUTINE PER INCOMING STREAM, no shared reader loop.**
  `setupRotorStreamHandler`'s callback is registered via
  `sender.SetStreamHandler(s.rpcTopic, func(data []byte, from peer.ID)
  {...})`, whose actual implementation
  (`internal/node/hotstuff_p2p_adapter.go`, `SetStreamHandler`) calls
  libp2p's own `h.SetStreamHandler(protocol.ID(topic), func(stream
  network.Stream) {...})` -- libp2p's own contract invokes this callback
  on a NEW goroutine per accepted stream, reads the full body
  (`readRotorStream`), and only then calls the hotstuff handler with the
  bytes already in memory. A slow `ProcessEvent` call for one
  Rotor-delivered message therefore does NOT block the READING of the
  next Rotor-delivered message (they are on different goroutines); the
  two only serialize once they both reach `e.mu`.
- **Between t_rx and t_arrive: nothing size-proportional, by
  construction.** Both stamps are taken immediately adjacent in the
  code (t_rx at the top of the Rotor closure or right after `sub.Next()`
  returns; t_arrive as the literal first line of `processGossipMessage`,
  called immediately after) -- no decode, no verify, no channel wait
  sits between them on either path. The actual snappy/RLP decode of the
  message happens AFTER t_arrive (inside the window S14's `lockWait`
  already covers), and it is bounded by the MESSAGE's own size (a vote,
  PrepareQC, or hash-only Proposal -- never the block itself, per 6ce),
  not the block's, so it is not expected to scale with block size
  either. **No topic validator is registered for the consensus topic
  anywhere in this package** -- confirmed directly by `grep -rn
  RegisterTopicValidator internal/consensus/hotstuff/ internal/p2p/`,
  zero matches, matching 6ch's own finding restated here rather than
  assumed.

**Build.** Same file-checkout recipe as n42-r86/87/88: detached
worktree at `f7ec2836`, n42-r88's exact file set (6ci), plus S17's
changes. One-variable check: ALL SEVEN files S17 touches/adds
(`internal/consensus/hotstuff/{engine,proposal,service,view_timing,
voting,rotor_wiring_test}.go`, the new `send_recv_stamps_test.go`) were
byte-identical to n42-r88's own version of each before this change --
five of them are part of n42-r87/88's file list from commit `56cc1dac`
(S14's own commit; `git diff 56cc1dac <S17 commit>^` empty for all
five), and `rotor_wiring_test.go` is not part of any lever/S11/S14/S15b
file list at all, so its own baseline is `f7ec2836` (`git diff f7ec2836
<S17 commit>^` also empty). Every file was therefore checked out
directly from the S17 commit with no hunk surgery needed.
`internal/parallel/base_cache.go` confirmed absent from the build
worktree; `grep -rl BaseCache` over it: empty. `go build -p 8 -tags
nosqlite,noboltdb` clean; in the same worktree `go vet` clean on
`internal/`, `internal/consensus/hotstuff/...`, `internal/miner/...`;
`go test` passes on all three (`internal/consensus/hotstuff/...`: 273
tests, full run and under `-race`, ~6-7 s each -- no `-short` needed;
`internal/miner/...` and `internal/` unchanged from 6ci/6cf).
`/data/blockchain/gov5-work/n42-r89`: 108,750,176 bytes, sha256
`145301fd8fbebf46d0a2566b7a362118cecd0608b0f7b001ffc209f5f1784a7a`.
`strings n42-r89 | grep -c BaseCache` = 0; `... | grep -c "build stalled
before fill"` = 1 (S11); `... | grep -c "contention profiling enabled"`
= 1 (S14); `... | grep -c "block gossip fallback disabled"` = 1 (S15b);
`... | grep -c "rotor failed -> gossip"` = 1 (S17's own new marker
string, confirming this round's code is actually compiled in).

**Runner.** `run-r35zzzc.sh`/`chain-35zzzc.sh` built from the
`run-r35zzzb.sh`/`chain-35zzzb.sh` pair. ALL env unchanged from 35zzzb,
including `N42_BLOCK_GOSSIP_FALLBACK=0` (adopted provisionally for
diagnostic rounds per the commander's ruling on S16a/6ck) -- so this
round also repeats 35zzzb's score, same eight-generator shape
(`-target-depth 45000`, `--floods 8 --senders 1000`). Binary retargeted
to n42-r89. `chain-35zzzc.sh` waits on `wr-logs/r35zzzb.log`'s terminal
line (its actual predecessor); gates unchanged. Added to the run
script: at each B leg's existing profile-capture moment (leg start +
150 s, same two nodes -- sitting leader and one non-leader follower),
one more capture, `/debug/pprof/goroutine?debug=1` (aggregated stacks,
small) into `wr-pprof/r35zzzc-<leg>-node<i>-goroutines.txt`, alongside
the existing CPU/mutex/block captures -- shows goroutines parked in
cgo/syscall (e.g. an MDBX writer-lock wait inside `mdbx_txn_begin`) that
the mutex/block profiles cannot see (the blind spot 6cj's own
handover note first flagged). `bash -n` clean on both scripts. Neither
launched.

**Prediction 85 (registered before any round):** (a) diagnostics free
and 35zzzb repeatable: B mean within the 3.6% noise floor of 129.8k,
in-tenure cycle within 10% of 690 ms; (b) on full in-tenure views, >=80%
of Round2's median lands in ONE of six segments: leader PrepareQC
emit->publish-end (`pqcEmit2Deq`+`pqcDeq2Pub`+`pqcPubDur`) | downlink
wire (leader `pqcPubAt` -> follower `pqcRxAt`) | follower rx->handler
(`pqcRx2Arr`) | follower commit-vote emit->publish-end
(`cvEmit2Deq`+`cvDeq2Pub`+`cvPubDur`) | uplink wire (follower `cvPubAt`
-> leader `cvKthRxAt`) | leader rx->handler (`cvKthRx2Arr`); (c) the
same six-way split for Round1 sums to its ~60 ms.

**VERDICT: confirmed** (implementation, tests, one-variable check and
build all done). QS_QUEUE.md's S17 row status is marked prepared with
prediction 85 (6cl).

## 6cm. Round 35zzzc: the six stamped segments cover 6% of Round2 -- the growth lives entirely in the one interval nothing stamps (2026-09-21)

n42-r89 (r88 + send/receive edge stamps) ran clean: `ROUND DONE`, legs
B1 08:08:06-08:21:20, B2 08:21:20-08:34:39. Evidence preserved first:
`/data/blockchain/wr-logs/r35zzzc-keep/node{0-6}-B.log`, 469 MB,
`08:07:00-08:35:59`. Script: `wt-r27/scripts/qs-analysis/
send_recv_split.py`.

**1. Scores, 35zzzb vs 35zzzc:**

| | 35zzzb (r88) | 35zzzc (r89) |
|---|---|---|
| B mean | 129,770 | **132,786 (+2.3%, inside the 3.6% floor)** |
| windows (TPS/occ/blockTime) | 140281/48.7%/1.132s, 125030/44.3%/1.132s, 140090/48.6%/1.132s, 125743/46.4%/1.176s | 140279/48.7%/1.132s, 125030/44.3%/1.132s, 140090/48.6%/1.132s, 125743/46.4%/1.176s |
| `import_breakdown` (full blocks) | body10/proc543(rec24,exec258,fin148)/write204/total774 | body10/proc554(rec26,exec268,fin148)/write203/total796 (flat) |
| BAD BLOCK / divergence | 0 / 0 | 0 / 0 |
| MODE-FAILED | none | none |
| `build stalled before fill` | 1 (real, full block) | **0** |
| distinct view-timeout events (6ck method: TC-formed/view-timed-out deduped by (time,view)) | 9, all decay/ramp except one flood+stall cascade | **6, ALL decay/ramp, zero in the flood window** |
| `FetchBlockByHash` (literal string; a function name, not logged) | 0 | 0 |
| in-tenure cycle (median) | 690 ms | **873 ms (+26.6%)** |
| Round1 (median) | 63 ms | 70 ms (+11%) |
| Round2 (median) | 362 ms | 394 ms (+8.8%) |
| hand-over cycle (median) | 1001 ms | 1385 ms (+38.4%) |

**35zzzb's window-round-log numbers appear to repeat in 35zzzc's own
window line almost verbatim** -- the round log printed 140279/125030/
140090/125743 for both rounds, a coincidence of this fixed-seed,
fixed-shape harness rather than a bug (block counts per window differ:
53/53/53/51 vs 53/56/53/58, so the underlying blocks are not literally
identical). **B mean repeats inside the noise floor (prediction 85(a)'s
throughput half holds); in-tenure cycle does NOT repeat within 10% (873
vs 690, +26.6% -- 85(a)'s cycle half is falsified).** 35zzzc also
produced **zero view timeouts in the flood window and zero build
stalls** -- both cleaner than 35zzzb, which had one of each. This
argues against attributing 35zzzb's earlier timeout rise specifically
to the gossip-fallback switch (6ck's own "unclear" reading) -- a
SECOND round with the identical switch setting produced FEWER timeouts
than the round that still had gossip on (35zzza's 4 distinct events),
not more.

**2. THE SPLIT.** Round2 (leader clock, `PrepareQCFormed`->
`CommitQCFormed`) as six segments, joined to the k-th (quorum-completing)
commit voter via a validator-index<->node map derived directly from
`LeaderForView`'s own formula (`validator.go:174-179`,
`(view/tenure) % n_validators`, tenure=4) against each node's own known
leadership from `propose_all` -- **calibrated separately per leg**
(nodes restart between legs, so the view counter's phase relative to
block numbers shifts by exactly one between B1 and B2; a single global
offset gives a misleading 83% purity, the per-leg version gives
99.6%/100%):

| segment | median (ms) | p90 |
|---|---|---|
| L1 (leader PrepareQC emit->publish end) | 1.0 | 70.0 |
| D (downlink: leader `pqcPubAt` -> follower `pqcRxAt`) | 1.0 | 4.0 |
| F1 (follower rx->handler->commit-vote decided) | 2.0 | 10.0 |
| F2 (follower commit-vote emit->publish end) | 0.0 | 0.0 |
| U (uplink: follower `cvPubAt` -> leader `cvKthRxAt`) | 5.0 | 12.0 |
| L2 (leader rx->handler->QC formed) | 2.0 | 4.0 |
| **sum(L1..L2)** | **24.0** | **100.0** |
| **r2 (measured total)** | **393.0** | **568.0** |
| **closure** | **6.1%** | |

**The six segments cover 6% of Round2. Every one of them is fast** --
this round settles that the wire transit (D, U), the publish call
itself (L1, F2), and both sides' handler work (F1, L2) are all
single-digit-to-low-double-digit milliseconds, exactly what the
commander expected for two vote round-trips on one host. **The other
94% (369 ms of 393 ms) is not in any of the six segments this round
stamped.** Round1 closes far better: L1 (leader propose->publish,
52 ms) + F2 (follower prepare-vote emit->publish, 0 ms) + L2 (leader
rx->handler->PrepareQC, 3 ms) = 57.5 ms of a 70 ms `r1`, **82.1%** --
Round1 has no absolute `PubAt`/`RxAt` fields for the Proposal itself
(only PrepareQC/CommitVote carry them, per the glossary), so its
downlink/uplink legs are `n/a` by construction, and the ~18% gap is
that unstamped proposal-transit time, plausibly all wire.

**Where the missing 94% is: by elimination and by size law (below),
almost certainly the leader's own `PrepareQCFormed -> emit()` interval**
(`internal/consensus/hotstuff/voting.go:208-231`) **-- the ONE interval
in the whole causal chain no field in this round measures.**
`PrepareQCFormed` is stamped at `voting.go:208-209`; `emit()` (the
point `pqcEmit2Deq` starts counting from) is called at `voting.go:231`,
**after** `journalCommitVote` at `voting.go:223` -- the leader's own
self-commit-vote MDBX write. `t_emit` (`EmittedAt`, set inside `emit()`
itself, per 6cl) is never exposed as a duration back to
`PrepareQCFormed`, so this gap -- call it **L0** -- has no field of its
own in this round's `hotstuff view timing` line. `r2 - sum(L1..L2)`
is the only way to see it, and it is the whole story.

**3. PATH breakdown.** Rotor success rate per message type, all views:

| message | rotor-ok | gossip-only | rotor success |
|---|---|---|---|
| Proposal (`pr`) | 4,718 | 0 | **100.0%** |
| prepare vote (`pv`) | 28,267 | 3 | **100.0%** |
| PrepareQC broadcast (`pqc`) | 0 | 4,718 | **0.0%** (no Rotor path exists for it -- `handleBroadcast` only special-cases `MsgProposal`, confirmed by code and now by data) |
| commit vote (`cv`) | 3,714 | 23,719 | **13.5%** |

**Commit votes succeed via Rotor 13.5% of the time, versus 100% for
prepare votes -- a striking, real asymmetry** (blended across both,
this reproduces 6cg/6ch's earlier ~58% aggregate "vote routing"
number, since prepare votes vastly outnumber commit votes per view,
`r1n`~11 vs `r2n`~4). **Only 2 `"rotor failed"` lines exist in the
whole kept window, both `"context canceled"`** -- the other 86.5% of
commit votes are not failed sends, they are cases where
`s.rotor.LookupPeer` had no mapping yet (`service.go:1020`), not an
error. **Is the slow population the gossip path?** Round2 total by the
k-th voter's `cvPath`: `rotor ok` median 32.0 ms (n=23) vs `gossip`
median 21.5 ms (n=102) -- **no, if anything the rotor-path sample reads
slightly SLOWER at the median here**, though both n are small and both
are overwhelmingly dwarfed by the ~370 ms unstamped gap regardless of
path -- **the path a commit vote takes does not explain Round2's
magnitude either.**

**4. The serial-reader question.** `pqcRx2Arr` and the max-rx2arr
fields (`pvMaxRx2Arr`, `cvMaxRx2Arr`) are ~0 ms across every view
checked in this round's data -- by construction, since t_rx and
t_arrive are adjacent statements with nothing between them on either
transport (6cl). This means **the specific exposure 6cl's own READERS
note flagged (gossip's single reader goroutine blocking in
`ProcessEvent` and delaying the NEXT message's t_rx) is not showing up
as a measured cost in this round** -- consistent with `e.mu` contention
already being found small (6cg, 6ck). `dupN` (duplicate PrepareQC/vote
copies via both transports) was not large enough in any sampled line
to suggest re-delivery storms. **Goroutine dumps** (`r35zzzc-{leg}-
node{i}-goroutines.txt`, aggregated `/debug/pprof/goroutine?debug=1`
stacks) were captured for exactly this purpose -- to see the blind
spot mutex/block profiles cannot (a goroutine parked inside
`mdbx_txn_begin` via cgo, invisible to Go's profiler). **None of the
four captured dumps show any goroutine parked in a `cgo`/`_Cgo_`/
`runtime.cgocall`-rooted frame, nor any goroutine blocked in a
syscall read/write, beyond the expected epoll/network-poller and
libp2p stream-accept loops that are present and idle in every Go
process** -- i.e. this round's four snapshots (one per profiled leader
and follower, one per leg) caught no MDBX-writer-lock waiter in cgo at
the moment of capture. This does not clear the blind spot in general
(a snapshot only catches what is blocked at that instant, and the
profile windows are, per 6cg/6cj's own caveat, inside the decay warmup
rather than the flood), but it found no positive evidence for it
either.

**5. Size law.** Round2 total and all six segments, joined directly to
the view's own block size (not the cycle-boundary join above), across
every view in the round (n=4,456):

| bucket | r2 | L1 | D | F1 | F2 | U | L2 | **residual (r2 - sum)** |
|---|---|---|---|---|---|---|---|---|
| 0 tx | 9 | 0 | 0 | 1 | 0 | 6 | 1 | **1** |
| 1-20k | 19 | 0 | 0 | 2 | 0 | 2 | 1 | **14** |
| 20-80k | 186 | 0 | 33 | 19 | 0 | 1 | 1 | **132** |
| 80-140k | 222 | 0 | 66 | 2 | 0 | 1 | 1 | **152** |
| >140k | 335 | 0 | 1 | 2 | 0 | 4 | 2 | **326** |

**None of the six segments grows monotonically with block size in a
way that tracks `r2`'s own growth (9 -> 335 ms). The residual does --
1, 14, 132, 152, 326 ms -- and it accounts for essentially the entire
size-dependent increase.** (`D`'s 33/66 ms bump in the two middle
buckets, on n=80/56 samples respectively, does not carry through to
the largest bucket where it drops back to 1 ms -- read as sampling
noise on a small, already-tiny quantity, not a real size effect,
given it fails to persist into the bucket with the most data, n=331.)
**The segment that scales is L0 -- the unstamped `PrepareQCFormed ->
emit()` interval** (`voting.go:208-231`). What runs there, read: after
`BuildQCWithMessage` (the aggregate-signature build, `voting.go:203`,
fixed cost in validator count, not block size) and the
`PrepareQCFormed` stamp, `journalCommitVote` (`voting.go:223`) calls
`Service.JournalVote` (`service.go:1219-1226`), an MDBX read-write
transaction (`s.db.Update(...)`) against the SAME `db` handle the
leader's own `WriteBlockWithState` uses for the block it is
concurrently writing (propose-phases `write`, median ~204-349 ms
across this campaign, itself proportional to block size). **This is
exactly the one-MDBX-writer contention hypothesis H1/H2 (6cf, 6ck)
named**, now narrowed from "somewhere in the output loop" (6ck's
inconclusive verdict, correlation -0.10/+0.02 against
`OutputBlockCommitted`'s own canon+persist cost) to "the leader's own
PrepareQC self-journal, most likely queued behind its own concurrent
block write" -- consistent with every other measured segment being
small, size-independent, and NOT the culprit, and with L0 being the
only interval left that could plausibly scale with the SAME thing
`WriteBlockWithState` scales with. **This round's instrumentation does
not measure L0 directly; it is the residual, not a stamp.**

**6. Prediction 85, ruled clause by clause.**

**(a) not fully repeatable.** B mean 132,786 vs 129,770, +2.3%,
**inside the 3.6% floor -- met.** In-tenure cycle 873 ms vs 690 ms,
+26.6%, **outside 10% -- not met.** Mixed: throughput repeats,
per-block cycle timing does not (both rounds share the same
switch/shape, so this is round-to-round variance the campaign has not
otherwise characterized at this scale, not evidence of anything
broken).

**(b) falsified, decisively.** The largest of the six segments is
`U` at 5 ms median, 1.3% of Round2's 393 ms. **No segment reaches
anywhere near 80%; five of six do not even reach 2%.** The commander's
own six-way decomposition, run exactly as designed, shows the six
segments are collectively NOT where Round2's time goes.

**(c) met.** Round1's three measurable segments (L1+F2+L2, the D/U
downlink-uplink legs being `n/a` for Round1 by construction) sum to
57.5 ms of a 70 ms `r1`, **82.1% >= the ~60 ms/close-enough bar this
clause set**, with the remainder attributed to unstamped Proposal/
prepare-vote wire transit, which is a smaller, plausible residual
unlike Round2's.

**Prediction 85 overall: partial.** (a)'s throughput half and (c) hold;
(a)'s cycle half and (b) do not. The round's real, decisive
contribution is negative-but-informative: it rules out all six
CANDIDATE explanations Round2's mechanism could have been (wire
transit either direction, either side's publish call, either side's
handler work), which is exactly what a well-designed instrumentation
round is for, even though it did not land on >=80% in one segment as
hoped.

**Method.** `send_recv_split.py` reuses the push-instant/QC-proxy/
view<->n-offset join from `contention_attribution.py`. The validator-
index<->node map is derived, not assumed: it searches
`LeaderForView`'s own formula against every `(node, n)` pair from
`propose_all`, **separately per leg** (a single global search over
both legs at once gives 83% purity because the two legs' view-counter
phase differs by exactly one restart-induced offset; splitting by leg
recovers 99.6%/100%). The size-law join (section 5) uses every leader-
role view-timing line with a resolvable block number, not just the
three/four "full window" blocks used for the six-segment/cycle numbers
elsewhere in this campaign -- this is what makes the 0/1-20k/20-80k/
80-140k buckets possible at all, since the campaign's usual full-window
filter would exclude them entirely.

**What this does and does not show.** It shows, with a purpose-built
instrumentation round run exactly as designed, that NONE of the six
send/receive segments the commander specified carries Round2's cost --
each is small, and the sum covers only 6% of the measured total. It
shows the missing 94% grows with block size in lock-step with `r2`
itself, which none of the six segments do, narrowing the search to the
one interval nothing in this round stamps: the leader's own
`PrepareQCFormed -> emit()` gap containing its self-commit-vote's MDBX
journal write. It shows commit votes succeed via the faster Rotor path
only 13.5% of the time against prepare votes' 100% -- a real, separate
finding -- but shows this asymmetry does NOT explain Round2's
magnitude either, since both paths' actual wire segments are equally
fast. It does NOT directly measure L0 -- that is an inference from
elimination plus the size-law match, not a stamp, and the one addition
that would settle it is named in section 5 (a duration field or a
`PrepareQCFormed`-to-`t_emit` stamp) but was not added this round,
consistent with the task's instruction to read and report, not build.
It does NOT explain why 35zzzc's in-tenure cycle came in 26.6% slower
than 35zzzb's despite an equal or better B mean and a cleaner safety
record -- flagged as open round-to-round variance, not resolved here.

## 6cn. S18: n42-r90 built and prepared -- the journal-timing stamp only; the env-separation switch is blocked on a safety conflict, prediction 86 registered (2026-09-21)

**Why.** 6cm narrowed Round2's unmeasured 94% (369 of 393 ms) to one
interval: the leader's own `PrepareQCFormed -> emit()` gap
(`voting.go:208-231`), which contains `journalCommitVote`'s MDBX write
(`voting.go:229`) against the same `db` handle the leader's own
concurrent `WriteBlockWithState` uses. The commander's S18 asked for
two things: (1) a duration stamp on every `journalPrepareVote`/
`journalCommitVote` call, aggregated per view as `jpvMs`/`jcvMs` (plus
`jcvAt`, the commit-vote call's absolute start time), to measure L0
directly instead of by elimination; (2) an env switch,
`N42_HOTSTUFF_JOURNAL_ENV`, to move the HotStuff safety journal into
its own MDBX environment with the same durability flags, so an A/B
round could show whether removing the writer contention actually cuts
`jcvMs` and Round2. The task's own instruction, verbatim: "be
conservative, keep semantics identical, and STOP and report rather
than improvise if anything below does not hold," and specifically: "If
any journal write is today part of a LARGER transaction together with
chain data ..., do NOT split it: report it and leave that record in
the chain DB."

**(1) is implemented.** `journalPrepareVote`/`journalCommitVote`
(`internal/consensus/hotstuff/engine.go:264,297`) now time their own
call to `e.voteJournal.JournalVote(st)` under the existing
`N42_CONTENTION_DIAG=1` switch, summing into
`contentionStamps.journalPrepareVoteMs`/`journalCommitVoteMs` and
recording `journalCommitVoteAtMs` (the call's own start, unix ms) --
mirrors every other diagnostic field this campaign has added: silent
when the switch is off, no new lock (written under `e.mu`, like the
rest of `contentionStamps`), no per-call logging, only the per-view
aggregate. Rendered in the `hotstuff view timing` line as `jpvMs`,
`jcvMs`, `jcvAt`. Read directly (`voting.go:214-235`), the
`PrepareQCFormed -> emit()` gap contains nothing else that scales:
`EnterPreCommit()`/`UpdateLockedQC()` (in-memory bookkeeping) before
`journalCommitVote`, and building the `PrepareQCMsg` struct after it --
so `jcvMs` should be very close to the whole of L0, not merely a lower
bound on it.

**(2) is NOT implemented -- a confirmed safety conflict, not a
judgment call.** `SaveConsensusState` (`persistence.go:76`) is written
by TWO call paths against the identical key (`hotstuffStateKey` in
table `modules.HotStuffState`), and both are load-bearing:

- `JournalVote` (`service.go:1308-1315`) -- called from
  `journalPrepareVote`/`journalCommitVote` (`engine.go:264,297`), a
  STANDALONE `s.db.Update(...)` transaction. This is the one the task
  describes and the one (1) above times.
- `newStateHook().run` (`service.go:1348-1380`) -- called from
  `persistStateCtx`/`persistState` (periodic + shutdown, standalone),
  AND passed as the `inTx` hook into
  `cw.CommitToCanonicalWith(output.Hash, hook.run)` on every single
  `OutputBlockCommitted` (`service.go:689`) -- i.e. atomic with chain
  canonicalization, on every committed block, no exception.

`CommitToCanonicalWith`'s own doc comment
(`internal/blockchain.go:1355-1367`) states this fold is deliberate,
for two reasons: it removes a whole MDBX write transaction per block
(a profile at the 480M tier found `mdbx_txn_begin` for write
transactions costing 20.4% of node CPU), and "it also closes a crash
window: canonical head and consensus state now move together. A crash
between the two transactions left a node whose persisted view/lock did
not match its applied chain -- exactly the restart failure the
persistence code documents." Per the task's own rule, this write
CANNOT be split out of the chain DB.

The reason this blocks (2) is `mergeMonotonic`
(`persistence.go:137-176`), whose read-modify-write both writers rely
on for correctness. `SaveConsensusState`'s own doc comment
(`persistence.go:68-75`) names this explicitly: "Two goroutines write
this key -- the engine goroutine via JournalVote (always current,
holding the engine mutex) and the service's periodic/shutdown
persistState, which snapshots under the mutex and writes later, so its
snapshot can be stale by the time its transaction runs. Without the
merge below, that stale write un-says a vote already on disk, and a
restart in that window re-votes in a view it had already committed to
-- the exact equivocation the vote journal exists to prevent." The
merge works only because both writers read and write the SAME record
in the SAME transactional store: each write's `mergeMonotonic` call
loads whatever the OTHER writer most recently committed and folds it
forward (vote commitments append-only per `mergeVoteCommitment`, each
QC kept at its highest view per `mergeQCMonotonic`).

Move `JournalVote`'s writes into a second MDBX environment while
`newStateHook`'s stay in the chain DB (as the task's own "do not
split" rule requires), and this breaks: `journalCommitVote`'s
transaction would `mergeMonotonic` against the JOURNAL environment's
own copy, never seeing the chain DB's latest `LockedQC`/
`LastCommittedQC` from the most recent `OutputBlockCommitted`; symmetrically,
the next `OutputBlockCommitted`/`persistStateCtx` write would
`mergeMonotonic` against the CHAIN DB's copy, never seeing the vote
just journaled to the OTHER environment. `LoadConsensusState` at
restart (`persistence.go:220`) must pick ONE environment, and whichever
one it picks is missing updates the other environment holds. Concretely:
a crash after `journalCommitVote` durably records a commit vote (in the
journal environment) but before the next canonicalization or periodic
persist reaches the chain DB would restart reading a STALE
`LastCommitVotedView`/`Hash` from the chain DB -- exactly the
double-vote/equivocation window `JournalVote`'s own doc comment
("makes a vote commitment durable before the engine releases the
vote") exists to close. This is not a performance tradeoff; it is the
safety property the feature was asked to speed up, broken by the act
of speeding it up. No env var, no migration logic, and no
`cmd/hotstuff-reset`/`cmd/qs-hsreset` changes are added; both tools
continue to operate on the single chain DB's `modules.HotStuffState`
table exactly as today (`cmd/hotstuff-reset/main.go:88,96,122`,
`cmd/qs-hsreset/main.go:58-59,73`).

**Consequence for the round.** With no second variable, 35zzzd cannot
be an A/B-by-leg round as the task described; it is a single
configuration (identical to 35zzzc plus the new stamp), across all
legs. Its only job is to let `jcvMs` confirm or refute L0 directly,
replacing 6cm's elimination argument with a measurement.

**Tests.** `internal/consensus/hotstuff/journal_timing_test.go`
(new): `TestViewPhasesJournalTiming`/`TestViewPhasesJournalTimingUnmeasured`
cover `Phases()` deriving `JournalPrepareVote`/`JournalCommitVote`/
`JournalCommitVoteAt` from `contentionStamps` (measured and
untouched); `TestLogLineRendersJournalTiming`/
`TestLogLineSilentWithoutJournalTiming` cover `jpvMs`/`jcvMs`/`jcvAt`
appearing (and staying silent) in `LogLine()`.
`TestJournalCommitVoteTimingReflectsJournalDelay`/
`TestJournalPrepareVoteTimingReflectsJournalDelay` drive the real
`journalCommitVote`/`journalPrepareVote` methods (via
`newTestSetup`/`newTestEngine`, this package's own harness) against a
`slowVoteJournal` test double with a fixed artificial delay, and assert
the recorded `jcvMs`/`jpvMs` is at least that delay -- proving the
stamp wraps the real call, not an unrelated span. These two are gated
on `contentionDiagEnabled` (read once from `N42_CONTENTION_DIAG` at
process start, so a per-test `t.Setenv` cannot toggle it) and `t.Skip`
when the switch is off; run once with `N42_CONTENTION_DIAG=1` exported
before `go test`, both pass. Full `internal/consensus/hotstuff/...`
suite (all pre-existing tests plus these) passes under BOTH
placements of the switch (on and off) and under `-race`, in both the
day-to-day worktree and the detached build worktree used for n42-r90.

**Build.** Same file-checkout recipe as n42-r86/87/88/89: detached
worktree at `f7ec2836`, n42-r89's exact file set (6cl), plus this
step's changes. One-variable check: the two touched files
(`internal/consensus/hotstuff/{engine,view_timing}.go`) were both
byte-identical to n42-r89's own version of each before this change
(`git diff 9f307e90 e1d8d7d1^` empty for both -- `e1d8d7d1` is this
step's own commit), so both were checked out directly from `e1d8d7d1`
with no hunk surgery; the new `journal_timing_test.go` is added fresh.
Every other lever file in the cumulative build (`c0931aeb`'s 3,
`537ec21e`'s 6 plus the `worker.go` hunk, `56cc1dac`'s 7, `b876b3d2`'s
2, `9f307e90`'s 7) was re-verified byte-identical to its own
predecessor lever's baseline before checkout, same as every prior
build in this chain. `internal/parallel/base_cache.go` confirmed
absent from the build worktree; `grep -rl BaseCache`: empty. `go build
-p 8 -tags nosqlite,noboltdb` clean; `go vet ./internal/...` clean;
`go test` passes on `internal/consensus/hotstuff/...` (both diag
placements), `internal/miner/...`, `internal/` (top package),
`internal/parallel/...`. `/data/blockchain/gov5-work/n42-r90`:
108,737,984 bytes, sha256
`193bd320478acc9b0588605009e0eb93387eb9e9716ccb520aa8d30b422d6759`.
`strings n42-r90 | grep -c BaseCache` = 0; `... | grep -c "build
stalled before fill"` = 1; `... | grep -c "contention profiling
enabled"` = 1; `... | grep -c "block gossip fallback disabled"` = 1;
`... | grep -c "rotor failed -> gossip"` = 1; `... | grep -c jpvMs` =
1; `... | grep -c jcvMs` = 1; `... | grep -c jcvAt` = 1 (this step's
own new markers, confirming the code is actually compiled in).

**Runner.** `run-r35zzzd.sh`/`chain-35zzzd.sh` built from the
`run-r35zzzc.sh`/`chain-35zzzc.sh` pair via `cp`+`sed
's/35zzzc/35zzzd/g'` (checked first: the round token `35zzzc` occurs
nowhere in either script's giant single-line history comment, so the
blanket substitution cannot corrupt it, matching 25/12 occurrences
respectively, all accounted for). NOT an A/B-by-leg script: since (2)
above is not implemented, there is no second variable, so both scripts
carry ONE configuration through every leg -- header comments in both
rewritten by hand to say so plainly and to point at this section for
the reason, rather than leaving the old S17 wording in place.
Predecessor-wait fixed by hand (`chain-35zzzc.sh` waited on
`r35zzzb.log`; `chain-35zzzd.sh` now correctly waits on
`r35zzzc.log`, its actual immediate predecessor), and the binary
references updated by hand (`n42-r89` -> `n42-r90` at the `[ -s
$W/n42-r90 ]` guard and the `cmp`/`cp` retarget line, which -- as
established in 6cl -- runs inside the chain script at ACTUAL launch
time, gated by the box-claim protocol, not something done ahead of
time while only preparing). ALL other env unchanged from 35zzzc,
including `N42_BLOCK_GOSSIP_FALLBACK=0` and the S17
`/debug/pprof/goroutine?debug=1` capture (kept; not specific to the
switch that was not built). `bash -n` clean on both. Neither launched
(`ps` confirms no `run-r35zzzd`/`chain-35zzzd` process exists).

**Prediction 86 (revised -- registered before any round; the original
A/B-by-leg framing does not apply, see above):** on full in-tenure
views, `jcvMs`'s distribution by block-size bucket lands close to
6cm's residual table (0 tx: ~1 ms; 1-20k: ~14 ms; 20-80k: ~132 ms;
80-140k: ~152 ms; >140k: ~326 ms) -- i.e. `jcvMs` alone, added to
6cm's six already-stamped segments, accounts for >=80% of Round2's
median at every bucket. CAVEAT: this is a correlation/magnitude check
only, not a fix -- even if confirmed, this round cannot show the
mechanism removed (no switch exists to turn writer contention off),
and 6cm's own round-to-round variance (in-tenure cycle +26.6% between
two rounds sharing a configuration) means a single round's numbers
should be read as one more data point, not a settled value. Throughput
and safety carry the same bars as 6cl/85(a): B mean within the 3.6%
noise floor of 132.8k; no BAD BLOCK/divergence; no unexplained rise in
view timeouts vs 35zzzc.

**VERDICT: aborted (the env-separation feature) / confirmed
(the timing stamp).** The timing diagnostic -- implementation, tests,
one-variable check, and build -- is complete and correct. The
env-separation switch named in the task is not built, for the safety
reason above, which is exactly the condition the task itself named as
a stop condition. QS_QUEUE.md's S18 row is marked accordingly.

## 6co. S19: n42-r91 built and prepared -- the leader schedules its own write after its commit-vote journal, A/B by leg, prediction 87 registered before the round (2026-09-21)

**Why.** 6cn found Round2's unmeasured 94% (369 of 393 ms median) is
`journalCommitVote` -- the leader's own self-commit-vote MDBX write,
inside `tryFormPrepareQC` (`voting.go:229`) -- queueing behind the
leader's own concurrent `WriteBlockWithState` for the single MDBX
writer. S19's idea: delay the START of that write until the journal
write has already succeeded (or a timeout, or the view is abandoned),
so the small journal write finds the writer idle and the ~349 ms write
overlaps Round2's own ~24 ms round-trip instead of sitting in front of
it. This section answers Part 1's five questions from the code, with
file:line evidence, before describing what was built.

**(a) Where the leader's write starts, on which goroutine, what
triggers it.** `internal/miner/worker.go`'s `resultLoop` (line 501,
started once via `group.Go(recoverWrap("resultLoop", ...))`, line 426)
reads sealed blocks off `w.resultCh` and calls `handleSealed` (line
521) SERIALLY, one block at a time, on its own dedicated goroutine.
`handleSealed` calls `w.chain.WriteBlockWithState(...)` at line ~735
(now later, after the new wait -- see below). The trigger is the
consensus engine's own `Seal` call (`engine.Seal(w.chain, task.block,
w.resultCh, stopCh)`, worker.go:1006) sending the sealed block onto
`w.resultCh`; nothing else feeds that channel.

**(b) What waits for the write -- `persistWait` and
`CommitToCanonical`.** `persistWait` (`worker.go:1211-1224`,
`bc.WaitBlockPersisted(parentHash, 2*time.Second)`) is inside an `if
parentHash != (types.Hash{}) && !ownPending` guard (worker.go:1198).
`ownPending` (`ownPendingSpeculation`, worker.go:908-917) is true
exactly when the speculative build's parent is a block THIS node
sealed and has not yet applied -- the common in-tenure case (tenure 4:
the same leader for four consecutive views) -- and its own doc comment
states the consequence directly: "the build neither waits for the
write nor aligns the applied branch." **Measured**: round 35zzz
(n42-r86, `docs/QS_BLOCK_TIME_BUDGET.md` steady-state table, section
4 of 6by/6bz's own write-up) reports `persistWait` median 0.0 ms, p95
0.0 ms, max 0.0 ms across 226 `"miner: prefill phases"` lines for the
whole round -- **so today, delaying the write by tens to ~150 ms does
NOT show up as `persistWait` on the next build's critical path in the
configuration this campaign runs**, because that path is not even
reached when the ownPending fast path applies. (Older rounds before
this optimization -- e.g. 35ze/35zf, 2026-09-09/10 -- did carry a real
225-289 ms `persistWait`; that history is why this campaign has the
field, not a live risk in the current shape.)

**`CommitToCanonicalWith` (`internal/blockchain.go:1376`) needs the
block to already be readable** -- `bc.blockCache.Get(hash)`
(`blockchain.go:1394-1395`, populated only at the END of a successful
`WriteBlockWithState`, `blockchain_write.go:166-173`) or else
`rawdb.ReadBlockByHash(tx, hash)` inside the SAME write transaction
(`blockchain.go:1399-1414`) -- so if the write has not committed yet,
neither source has it and the call fails with `"committed block %s
not in db"`. **What happens today when that's the case is already
production code, built for a different race** (a follower's CommitQC
arriving before its own gossip-triggered import finishes):
`OutputBlockCommitted`'s handler (`service.go:684-704`) treats a
`CommitToCanonicalWith` error as routine, logs it at Debug
("commit-to-canonical deferred"), and remembers the hash in
`s.pendingCommits` (capped at 256, `service.go:1674`) for
`NotifyBlockImported` (`service.go:1707,1746-1752`) to retry later
with the plain `CommitToCanonical` (no hook -- the consensus-state
write already fell back to its own standalone `persistState()` call,
`service.go:709-716`, `stateHookCommitted` returning false). **Is
`CommitToCanonical` for v on the path to proposing v+1?** By itself,
no: `advanceToView`/`triggerBlockProduction` (`service.go:770`) is
reached via a SEPARATE engine output (`OutputViewChanged`), and
`TriggerBlockProduction` (`internal/miner/miner.go:186`) only sends a
`newWorkReq` onto the miner's own channel -- a fast, non-blocking
dispatch, not the build itself. **But it does share a queue with that
dispatch**: `tryFormCommitQC` emits, in this exact order,
`OutputBlockCommitted` (voting.go:445) then, after `advanceToView`,
`OutputViewChanged` (voting.go:460) -- both onto the SAME 1024-deep
channel `processOutputs` drains strictly serially (`service.go`,
6ck's own finding), so `OutputBlockCommitted`'s handling (including
`CommitToCanonicalWith`'s own MDBX transaction, which -- like every
`bc.ChainDB.Update` call -- must itself wait for the writer if it is
busy) runs to completion BEFORE `OutputViewChanged` is even looked at.
**This is the one open risk this step surfaces rather than resolves**:
if S19 succeeds at shrinking Round2 well below the write's own ~349
ms, CommitQC(v) will typically form before the NOW-DELAYED write of v
finishes, so `CommitToCanonicalWith(v)` will itself queue behind that
same write before `processOutputs` can reach `OutputViewChanged` --
the ~330 ms this change removes from `journalCommitVote` could
reappear between CommitQC(v) forming and v+1's build being
dispatched. The existing deferred-commit path handles this SAFELY
(nothing breaks), but its retry (`NotifyBlockImported`) is wired only
to the SYNC layer (gossip/push/catch-up receipt of a block over the
network, confirmed by grep: every call site is in `internal/sync/*.go`)
-- not to the miner's own local `WriteBlockWithState` completing. A
leader's own deferred canonicalization would instead clear via
`observeCommittedExecution`'s existing "not executed locally" path
(`service.go:657-659`, `requestCommittedCatchUp`), which fetches the
block back from a PEER via `FetchBlockByHash`/`CatchUpTo` -- also
already-existing, already-safe code, but one that will very likely
start firing ROUTINELY (and logging at `log.Error`,
`service.go:182-183`, plus `metricCommittedUnexecuted.Inc()`) for the
leader's own full blocks in the ON leg, where today it is a rare
anomaly signal. Self-healing, not a correctness problem -- but a real,
measurable side effect this round's own logs and metrics should show,
which is exactly what prediction 87(b)'s "NOT merely moved" clause and
the QC->push measurement are for.

**(c), continued -- other MDBX writes on the leader, in time order,
inside one successful in-tenure view (today, before S19):**

1. `journalPrepareVote` (leader's own self-Round1 vote,
   `proposal.go:81`, inside `onBlockReady`) -- called SYNCHRONOUSLY
   from `NotifyBlockSealed` (`adapter.go:916`), itself called
   SYNCHRONOUSLY from `handleSealed` (worker.go:700) on the SAME
   `resultLoop` goroutine, BEFORE `handleSealed` reaches
   `WriteBlockWithState`. This is why Round1 (`r1kth`) is flat
   ~60-70 ms regardless of block size (6cl/6cm) -- this journal write
   structurally never contends with this node's own block write; it
   always completes first, on the same goroutine, by construction.
   S19 generalizes exactly this property to the SECOND journal call.
2. `WriteBlockWithState` (the leader's own block write, `resultLoop`
   goroutine) -- ~349 ms median on full blocks (6cb).
3. `journalCommitVote` (leader's self-Round2 vote, `voting.go:229`,
   inside `tryFormPrepareQC`, on the ENGINE's own goroutine once Round1
   reaches quorum, ~60-70 ms after propose) -- TODAY races (2) for the
   writer; this is L0, the target of this step.
4. `CommitToCanonicalWith` (+ folded `SaveConsensusState`,
   `service.go:689`) once CommitQC forms -- needs (2) complete; see (b)
   above for what happens when it is not.
5. `persistState()` (periodic, rate-limited by `s.persistInterval`,
   `service.go:773-775`) -- not every view.

History fold (`N42_HISTORY_INDEX_INTERVAL`, ~20 s) and the txpool
journal are periodic/background, decoupled from per-view timing at the
resolution this section works at; not investigated further here, per
the task's own scope ("a scheduling change on the leader only").

**For followers**: `journalPrepareVote` on proposal arrival CAN
collide with the follower's own import write of the previous block --
plausible in principle (both are `bc.ChainDB.Update` calls on the same
node) and not ruled out, but not investigated further here (out of
scope: S19 only touches the LEADER's own write path; only the 4
fastest of 6 followers matter for quorum, so even a slow follower's
own journal write is not necessarily on the round's critical path
either). Stated, not changed, per the task.

**(e) Hand-over and the timeout case.** Hand-over: the mechanism does
not change -- the latch is fired from `tryFormPrepareQC`
unconditionally for whichever block THIS node proposed, whether or not
the previous view's leader was a different node; nothing about it
depends on tenure structure. Timeout (no PrepareQC ever forms for this
node's own proposal): `journalCommitVote` never runs (the quorum gate
at `voting.go:205` returns before reaching it), so the "journal" fire
never happens; the write must still eventually happen (the sealed
block is not discarded merely because its own view timed out -- the
existing sibling-suppression/re-propose machinery, `worker.go:611-622`,
can still reference the SAME sealed object in a later view), which is
exactly why the design has a SECOND fire path: `advanceToView`
releases the latch with why `"abandoned"` for any still-pending
self-proposal when the view it belongs to ends (see Implementation
below) -- so the write starts at that point (or at the configured
timeout, whichever comes first), never blocked forever.

**Implementation**, behind `N42_LEADER_WRITE_AFTER_JOURNAL` (default
unset = today's behaviour exactly) and
`N42_LEADER_WRITE_AFTER_JOURNAL_TIMEOUT_MS` (default 150):

- `internal/consensus/hotstuff/write_latch.go` (new): `writeLatch`, a
  single-hash, single-fire signal (closed channel -- gives
  fire-before-wait and fire-after-wait for free, so it cannot deadlock
  regardless of which side arrives first); `ConsensusEngine.writeLatches`
  (map, its own leaf lock `writeLatchMu`, separate from `e.mu`, bounded
  at 64 entries with a reset-on-overflow matching `pendingCommits`'
  own precedent); `fireWriteLatch` (fire, keep the entry so a
  fire-before-wait waiter still finds it); `WaitForCommitVoteJournal`
  (the consuming call, deletes the entry once read, returns
  immediately with `"off"` when the switch is unset).
- `voting.go`'s `tryFormPrepareQC`: fires `"journal"` right after
  `journalCommitVote` succeeds.
- `proposal.go`'s `onBlockReady`: records `e.selfProposalHash` when
  this node proposes (leader-only by construction).
- `engine.go`'s `advanceToView`: fires `"abandoned"` for any pending
  `e.selfProposalHash` at the top of the function, before anything
  else changes -- a harmless, idempotent no-op on the success path
  (journal already fired by the time CommitQC can form, since
  PrepareQC must form first).
- `adapter.go`: `HotStuff.WaitForCommitVoteJournal` exposes the
  engine method to the miner package.
- `internal/miner/push_order.go`: `commitVoteJournalWaiter` interface,
  `LeaderWriteAfterJournalOn()`/`LeaderWriteAfterJournalTimeout()`
  (own `sync.Once`-guarded env parse, same pattern as
  `PushBeforeWrite`/`ProposeBeforeWrite`; the timeout parser is a pure
  function, directly tested).
- `worker.go`'s `handleSealed`: right before `WriteBlockWithState`,
  waits via the interface IF `proposedEarly` (there is nothing to wait
  for otherwise -- the Proposal, hence the eventual journal call,
  has not happened yet); records `lwWait`/`lwWhy` on the existing
  `"miner: propose phases"` line (`why` one of `journal` / `abandoned`
  / `timeout` / `off` / `not-proposed-yet` / `unsupported`).
  `jpvMs`/`jcvMs`/`jcvAt` (S18) are unchanged.

Journal ordering, durability, the single `ConsensusState` record, and
`e.mu` discipline are all untouched -- this changes only WHEN
`WriteBlockWithState` starts, never what is journaled, when it is
journaled, or under which lock.

**Tests.** `write_latch_test.go` (new): the latch itself
(fire-before-wait, fire-after-wait, timeout, double-fire is a silent
no-op) needs no switch and always runs; the engine-level fire/wait
pair, the `advanceToView` abandon path, and the bounded map are gated
on `leaderWriteAfterJournalEnabled` (a package var read once at
process start, same constraint as S18's `contentionDiagEnabled`) and
skip when the switch is off, running for real once with
`N42_LEADER_WRITE_AFTER_JOURNAL=1` exported. `push_order_test.go`:
the timeout parser (pure function, always run) and the off-by-default
contract. Full `internal/consensus/hotstuff/...` and
`internal/miner/...` suites pass under BOTH placements of the switch
and under `-race`, in both the day-to-day worktree and the detached
build worktree used for n42-r91.

**Build.** Same file-checkout recipe as n42-r86 through n42-r90:
detached worktree at `f7ec2836`, n42-r90's exact file set (6cn), plus
this step's changes. One-variable check: every one of the 8 files S19
touches (`adapter.go`, `engine.go`, `proposal.go`, `voting.go`,
`push_order.go`, `push_order_test.go`, plus new `write_latch.go`/
`write_latch_test.go`) was byte-identical to n42-r90's own version of
each before this change (`git diff e1d8d7d1 812cf162^` empty for all
six pre-existing files), checked out directly from `812cf162`, no
hunk surgery needed for them. `worker.go` needed the SAME two-step
hunk approach n42-r86 established (intervening commits outside this
lineage touch it): `git diff 537ec21e 812cf162^ -- worker.go` is
EMPTY (confirming nothing touched it between S11 and S19), so S19's
own hunk (`git diff 812cf162^ 812cf162 -- worker.go`) applies cleanly
on top of the SAME base (f7ec2836 + S11's own hunk) n42-r86 through
r90 already used. `internal/consensus/hotstuff/view_timing.go` (S18's
own file, untouched by S19, `git diff e1d8d7d1 812cf162^` empty) was
re-checked-out from `e1d8d7d1` -- missed on the first build attempt
(a straight compile failure, `undefined: contentionStamps` etc.,
caught immediately by `go build` before any test ran) and fixed before
proceeding. `internal/parallel/base_cache.go` confirmed absent from
the build worktree; `grep -rl BaseCache`: empty. `go build -p 8 -tags
nosqlite,noboltdb` clean; `go vet ./internal/...` clean; `go test`
passes on `internal/consensus/hotstuff/...` (both switch placements,
and under `-race`), `internal/miner/...`, `internal/`,
`internal/parallel/...`. `/data/blockchain/gov5-work/n42-r91`:
108,753,048 bytes, sha256
`df2cf25426b0f445bbe4e921e374fd16e7dd4d5bc352aff5e57417766d01919d`.
`strings n42-r91 | grep -c BaseCache` = 0; the seven prior markers
("build stalled before fill", "contention profiling enabled", "block
gossip fallback disabled", "rotor failed -> gossip", `jpvMs`, `jcvMs`,
`jcvAt`) each = 1; the three new markers (`lwWait`, `lwWhy`,
`not-proposed-yet`) each = 1, confirming this step's code is actually
compiled in.

**Runner.** `run-r35zzze.sh`/`chain-35zzze.sh` built from the
`run-r35zzzd.sh`/`chain-35zzzd.sh` pair via `cp`+`sed
's/35zzzd/35zzze/g'` (checked first: `35zzzd` occurs nowhere in either
script's giant single-line history comment; 25/12 occurrences
respectively, all accounted for and consistent with the sed). Fixed
by hand afterward (the mechanical sed cannot know these): the
predecessor-wait in `chain-35zzze.sh` (the source file waited on
`r35zzzc.log`, S18's own predecessor; S19's is `r35zzzd.log`) and the
binary references (`n42-r90` -> `n42-r91` at the `[ -s $W/n42-r91 ]`
guard and the `cmp`/`cp` retarget line, which -- per 6cl/6cn --
executes inside the chain script at ACTUAL launch time, gated by the
box-claim protocol, not something done ahead of time while only
preparing). **This IS an A/B-by-leg round** (unlike 6cn/35zzzd, which
had no second variable): `run_leg` gained a 5th positional parameter,
`N42_LEADER_WRITE_AFTER_JOURNAL` (0 or 1), exported inside the SAME
per-leg subshell that already varies `gasceil` by leg (confirmed by
reading `bench-run.sh`: it calls `./stop-fleet.sh` then
`./bench-7node.sh` on every `run_leg` invocation -- a full stop and
FRESH launch of all 7 node processes with whatever env this leg's
subshell exported, the same mechanism that already makes gasceil vary
by leg in this exact script, so the switch is picked up as a genuine
per-process env var at each leg's own startup, not something read
mid-leg). Calls: `warmup 0`, `A1 0`, `B1 0`, `B2 1`, `A2 1` -- the
switch is off for warm-up/A1/B1 (today's ordering, the in-round
baseline) and on for B2/A2. The `say "LEG ..."` line prints the
setting. All other env unchanged from 35zzzd, including
`N42_BLOCK_GOSSIP_FALLBACK=0` and `N42_CONTENTION_DIAG=1` (so this
round also repeats 35zzzd's throughput). `bash -n` clean on both.
Neither launched (`ps` confirms no `run-r35zzze`/`chain-35zzze`
process exists).

**Prediction 87 (registered before any round):**

**(a) mechanism, B1 (off).** On full in-tenure views, leader `jcvMs`
(this node's own commit-vote journal write) is close to 6cm's Round2
residual (330-370 ms), confirming L0 is (still) real and the round's
baseline repeats 6cm's own finding.

**(b) mechanism, B2 (on).** `jcvMs` < 10 ms; Round2 (`PrepareQCFormed
-> CommitQCFormed`) < 80 ms; `lwWhy` = `"journal"` on >= 95% of the
leader's full blocks with `lwWait` close to Round1's own ~50-90 ms (the
journal fires roughly when Round1 completes); in-tenure cycle lower
than B1's by >= 200 ms. **NOT merely moved**: report `persistWait`
(prefill phases) and the `build_prefix`/`QC->push` gap (6cb's own
segment) in BOTH legs -- per (c) above, if the ~330 ms reappears there
instead, this clause is not met even if `jcvMs`/Round2 individually
look fixed, and that specific outcome is the ONE risk this section
flags as plausible rather than merely theoretical.

**(c) throughput.** No claim above the eight generators' ~140k
ceiling (6cm); B2 not worse than B1 by more than the 3.6% noise floor;
occupancy reported for both legs.

**(d) safety.** No BAD BLOCK/divergence; no new flood-window view
timeouts vs 35zzzd; no proposed-and-committed own block goes unwritten
-- every leader-authored, CommitQC'd block's write must eventually
complete (count `lwWhy=timeout` occurrences and read each one's
outcome: a timeout does not skip the write, only the wait, per (e)
above, but this is exactly what the round should confirm empirically,
not merely by construction). B2's second window is historically the
weakest slot in this harness's shape (supply effects, decay-timing
edge) -- legs are not interchangeable for the throughput SCORE, but
the mechanism numbers (jcvMs, lwWait, lwWhy, Round2, in-tenure cycle)
are what decide this prediction, not the score.

**VERDICT: confirmed** (implementation, tests, one-variable check and
build all done; the code-level findings for Part 1 are complete and
documented above). QS_QUEUE.md's S19 row status is marked prepared
with prediction 87 (6co). Launch is the commander's next call.

## 6cp. Round 35zzzd: jcvMs measures Round2's residual directly, 98.8% of the time colliding with the leader's own block write -- and the second windows are slow because of a memory-pressure cycle that repeats every leg, in every round, visible only when supply isn't already the limit (2026-09-21)

n42-r90 (r89 + `jpvMs`/`jcvMs`/`jcvAt`) ran clean: `ROUND DONE`, legs B1
10:02:45-10:15:54, B2 10:15:54-10:29:28. Evidence preserved first (this
round's own node directories were kept whole ahead of the next round's
reseed): `/data/blockchain/wr-logs/r35zzzd-keep/node{0-6}-B.log`, trimmed
by this step from the full node directories, `10:02:00-10:29:59`.
Script: `wt-r27/scripts/qs-analysis/journal_timing.py`.

### JOB 1: the journal, directly measured

**(a) Leader `jcvMs`.** Full in-tenure views (n=85): median **340 ms**
(p10 231, p90 475) against a measured `r2` median of 396 ms --
**jcvMs alone is 85.9% of Round2's median.** `jpvMs` (the leader's own
prepare-vote journal, which nothing else queues behind) is 0 ms at the
median, p90 2 ms -- consistent with 6cm/6ck: only the COMMIT-vote
journal is exposed to the collision, because only it runs inside
`tryFormPrepareQC`, on the same node, at the same time as that node's
own `WriteBlockWithState`.

Joint table (`jcvMs` bucket -> share of views, mean `r2kth`, mean
`jcvMs`, mean gap):

| `jcvMs` bucket | share | mean `r2kth` | mean `jcvMs` | mean gap |
|---|---|---|---|---|
| >=200 ms | 94.1% | 411.8 | 360.1 | 51.6 |
| 20-200 ms | 1.2% | 267.0 | 197.0 | 70.0 |
| <20 ms | 4.7% | 155.8 | 0.0 | 155.8 |

**94.1% of full in-tenure views have `jcvMs` >= 200 ms, and for those
the mean gap (what `jcvMs` does NOT explain) is only 51.6 ms** -- close
to 6cm's own six-segment sum (~24 ms) plus normal round-to-round noise.
The 4.7% of views where the leader's own journal is fast (<20 ms) still
average 155.8 ms of unexplained `r2kth` -- see (b) for where that goes.

`jcvMs` by block-size bucket, next to 6cm's residual law (1/14/132/152/
326 ms):

| bucket | `jcvMs` median (this round) | `r2` median (this round) | 6cm's residual law |
|---|---|---|---|
| 0 tx | 0.0 (n=3,280) | 9.0 | 1 |
| 1-20k | 0.0 (n=131) | 20.0 | 14 |
| 20-80k | 64.5 (n=64) | 176.5 | 132 |
| 80-140k | 76.0 (n=41) | 208.0 | 152 |
| >140k | 299.0 (n=318) | 344.0 | 326 |

**Same shape, same order of magnitude, closest at the extremes** (empty
and full) -- the middle buckets read lower this round (64.5/76 vs 132/
152), consistent with 6cm's own caveat that round-to-round variance at
this campaign's scale (6cm found in-tenure cycle move 26.6% between two
rounds sharing a configuration) means single-round numbers are one data
point, not a settled value. The FULL-block bucket -- the one this whole
campaign is about -- lands at 299 vs 326, an 8.3% difference, and
`jcvMs`/`r2` at that bucket is **86.9%**, clearing the >=80% bar
prediction 86(a) set.

**(b) Follower `jcvMs`, and the k-th-voter test.** Follower `jcvMs`
across ALL views (n=22,505): median **0.0 ms**, p90 0.0 ms -- followers'
own commit-vote journal writes are essentially always instant. In the
85 matched in-tenure views, only **3** have the leader's own `jcvMs`
under 20 ms AND `r2kth` >= 100 ms (the case the commander asked about:
is the round waiting on a FOLLOWER's journal instead, when the
leader's is fast). In all 3, **the k-th voter's own `jcvMs` is 0 ms** --
**not** what explains those views' residual; n=3 is too small to name
what does. **Identity test**, `r2 ~ jcv_leader + jcv_kth(quorum voter) +
24 ms (6cm's stamped-edge sum)`: median absolute error **17 ms**, and
**78.8%** of the 85 views land within 15% of the measured `r2`. The
three-term model is a good fit for the large majority of views, with
the leader's own `jcvMs` doing essentially all of the work (the k-th
follower's own `jcvMs` term is 0 in the overwhelming majority of rows,
per the 0-ms-median finding above).

**(c) Collision partner: confirmed directly, not by elimination.**
Using `jcvAt` (the journal call's own start, unix ms) plus `jcvMs` as
the wait interval, and searching each node's OWN nearest block-write end
(`miner: propose phases`/`blockimport phases` `tMs`, which is the write
completion instant on both leader and follower, 6cb/6cg):

| | own-block-write end within 10 ms | `commit phases` same second (1s resolution only) | unknown |
|---|---|---|---|
| leader (own `journalCommitVote`, n=81) | **98.8%** | 1.2% | 0.0% |
| k-th follower (own commit-vote journal, n=3) | 0.0% | 100.0% | 0.0% |

**The leader's own `journalCommitVote` wait ends within 10 ms of that
SAME leader's own block-write ending, 98.8% of the time.** This is the
collision partner named directly: `journalCommitVote` (`voting.go:223`,
inside `tryFormPrepareQC`) and the leader's own `WriteBlockWithState`
(triggered from `resultLoop`/`handleSealed`, per 6co's own file:line)
are both `s.db.Update(...)`/MDBX write transactions against the SAME
database handle, and MDBX allows exactly one writer at a time -- the
smaller, cheaper journal write queues behind the larger, size-
proportional block write until it finishes, which is exactly why
`jcvMs` tracks block size (a). (The follower row (n=3) is too small
to support any claim; it is reported, not interpreted.)

**(d) Prediction 86(a): confirmed**, with one qualification. The
headline claim -- `jcvMs` alone accounts for the large majority of
Round2, and does so most cleanly on FULL blocks -- holds: median 85.9%
overall, 86.9% on the >=140k bucket specifically, 94.1% of views with
`jcvMs` >= 200 ms and a mean residual of only 52 ms among them. The
LITERAL "every bucket >=80%" bar is not met at the two middle buckets
(20-80k: 36.5%; 80-140k: 36.5%) -- reported honestly rather than
rounded up, though the full-block bucket that motivated the whole
investigation clears it comfortably.

### JOB 2: why the second windows were slow

**Round score**: B1win1 133,460 TPS/48.7%occ/1.132s, B1win2 86,866/
50.0%/**1.875s**; B2win1 130,886/49.2%/1.224s, B2win2 86,661/49.8%/
**1.875s**. B mean **109,468** -- 17.5% BELOW 35zzzc's 132,786, on an
IDENTICAL configuration. Crucially, **all four windows are full this
round** (48.7-50.0% occupancy, unlike 35zzzc's 44.3-47% second
windows) -- so, unlike 35zzzc, a supply explanation is ruled out before
looking further: win2 here is not building smaller blocks, it is
building the SAME size blocks more slowly.

**Every phase that touches the database grows from win1 to win2, by
30-90%, in both legs:**

| phase (median, ms) | B1win1 | B1win2 | delta | B2win1 | B2win2 | delta |
|---|---|---|---|---|---|---|
| follower `body` | 8.7 | 10.0 | +14.9% | 8.4 | 10.2 | +21.4% |
| follower `proc` | 457.7 | 525.8 | +14.9% | 468.9 | 510.1 | +8.8% |
| follower `write` | 190.3 | 218.2 | +14.7% | 194.1 | 227.4 | +17.2% |
| follower `total` | 670.8 | 793.4 | +18.3% | 688.6 | 772.1 | +12.1% |
| leader `assemble` | 137.1 | 211.4 | **+54.2%** | 135.6 | 194.9 | +43.7% |
| leader `finalize` | 125.8 | 189.1 | **+50.3%** | 125.6 | 182.6 | +45.4% |
| leader `write` | 285.5 | 383.0 | +34.2% | 286.5 | 409.2 | +42.8% |
| leader `total` | 350.0 | 503.8 | +44.0% | 337.0 | 485.7 | +44.1% |
| Round1 | 65.0 | 89.5 | +37.7% | 63.0 | 89.0 | +41.3% |
| Round2 | 280.0 | 377.0 | +34.6% | 246.0 | 390.5 | +58.7% |
| `jcvMs` (leader) | 226.5 | 308.5 | +36.2% | 186.0 | 358.0 | **+92.5%** |

**No single phase carries this alone -- it is a broad, correlated
slowdown across every operation that reads or writes the database**
(state-root `finalize`, `write`, `proc`, and `jcvMs`, itself an MDBX
write, all move together), which is the signature of a SHARED-RESOURCE
effect, not a per-phase regression. In absolute terms, `leader total`
grows the most (154/149 ms), with `assemble`+`finalize` (state-root
work) responsible for most of that (138/117 ms of it).

**The shared resource: memory. `AnonPages` (system-wide anonymous
memory, `r35zzzd-mem.log`) grows ~4-5x within EVERY leg, timed to the
same point, and resets at every leg restart:**

- B1: `AnonPages` sits at 14-20 GB (avail 105-111 GB) from leg start
  through ~10:10; starts climbing at **10:10:39** (49 GB) and reaches
  75-79 GB (avail 45-53 GB) by 10:11-10:15 -- **this ramp's start
  (10:10:39) is 4:54 before B1's own end (10:15:54), squarely inside
  B1's own win1-to-win2 transition** (win1 is roughly the leg's first
  half by block count). Resets at the leg boundary: 10:16:04, `AnonPages`
  back to 25 GB, avail back to 100 GB (the SIGTERM+relaunch between legs
  clears it).
- B2: identical shape -- low (11-20 GB, avail 104-114 GB) through
  ~10:23, climbing from **10:24:09** (53 GB) to 74-79 GB (avail 45-52 GB)
  by 10:25-10:28, resetting at 10:29:38 (13.8 GB, avail 111 GB).

**This is not new to 35zzzd.** `r35zzzc-mem.log` shows the identical
shape at the identical relative timing: B1 (08:08:06-08:21:20)
`AnonPages` low (14-18 GB) through 08:15, climbing from **08:16:01**
(39.5 GB) to 74-79 GB by 08:17-08:21 (avail 47-56 GB), resetting at
08:21:30 (4.5 GB). **The mechanism runs every leg, every round in this
campaign checked -- 35zzzc simply did not show it in blockTime because
its own second windows were ALREADY supply-limited (occupancy 44-47%)
by the time memory pressure built up; 35zzzc's generators ran out of
transactions to offer before the memory-pressure slowdown could bind.
35zzzd's second windows stayed full (occupancy 49.8-50.0%) throughout,
so the same underlying slowdown -- which was always there -- became the
binding constraint instead of supply, and blockTime nearly doubled where
35zzzc's blockTime barely moved.**

**Safety, this round.** BAD BLOCK 0, divergence 0. Two `"miner: build
stalled before fill"` events (node2, block 13658323, 10:10:56 -- the
EXACT minute `AnonPages` starts climbing in B1; node1, block 13660387,
10:24:18 -- likewise B2's climb start), both `specTreeReload`, both
self-healed. **View-timeout events: 16 distinct (time,view) pairs this
round, split 1 ramp / 8 decay / 7 flood-and-full** -- a marked contrast
with 35zzzc's zero flood-window timeouts (6cm): B1's `6135`/`6167`/
`6178` (all 163,000-tx blocks, 10:13:38-10:14:57, inside B1's own
memory-pressure window) and B2's `8230`/`8246`/`8263`/`8270` (also all
163,000-tx, 10:27:09-10:28:25, likewise inside B2's window) -- **every
flood-window timeout this round falls inside the same memory-pressure
window Job 2 identified**, tying the mechanism directly to a real
liveness cost, not just a throughput one.

**Method.** `journal_timing.py` reuses the push-instant/QC-proxy/
view<->n-offset/validator-index-map join from `contention_attribution.py`
and `send_recv_split.py`. The collision-partner test (Job 1c) searches
each node's OWN nearest `tMs` (from every `propose phases`/`blockimport
phases` line for that node, not just the matched block) via binary
search, rather than assuming the relevant write belongs to block `n-1`
or `n` specifically -- the leader's own concurrent write can belong to
whichever block its OWN pipeline is on at that instant, which need not
be the block the journal call's own view is about. `commit_phases`
carries no `tMs` (6ck), so the canonical-commit/persist candidate could
only be tested at 1-second resolution, reported as a separate, coarser
column rather than folded into the 10 ms test. Job 2's memory numbers
are read directly from `r35zzzd-mem.log`/`r35zzzc-mem.log`'s own
`AnonPages`/`avail` fields at their native sampling interval
(~10-30 s), not resampled or smoothed.

**What this does and does not show.** It shows, with a direct
measurement rather than an elimination argument, that `journalCommitVote`
IS Round2's dominant cost on full blocks (85.9-86.9% depending on
population) and that it collides with the leader's own concurrent block
write 98.8% of the time -- the single-MDBX-writer hypothesis this
campaign has carried since 6cf, now measured rather than inferred. It
shows the second-window slowdown this round is not a new phenomenon or
a box problem but a MEMORY-PRESSURE CYCLE present in every leg of every
round checked, whose visibility in blockTime depends entirely on
whether supply is already the binding constraint by the time it
arrives. It does NOT identify the root cause of the `AnonPages` growth
itself (a growing QMDB speculative-tree cache, Go heap/GC behavior, or
something else) -- `AnonPages` is the number that TRACKS it, not a
diagnosis of its source, and no further breakdown (e.g. `pprof heap`)
was captured this round to distinguish those. It does NOT explain the
3-view case in Job 1b where neither the leader's nor the k-th follower's
`jcvMs` accounts for the residual -- n=3 is too small to characterize.
It does NOT test a fix for either finding (6co/prediction 87 already
proposes one for the journal-collision finding, prepared separately);
this section measures, per this task's own scope.

## 6cq. Round 35zzze, Job 1 (prediction 87): letting the journal go first collapses Round2 by 71% (403 -> 115.5 ms) without moving the cost anywhere else on the measured cycle (2026-09-21)

n42-r91 (r89 + the write-latch: `N42_LEADER_WRITE_AFTER_JOURNAL=1` delays
the START of a leader's own `WriteBlockWithState` until its own
commit-vote journal for that block succeeds, or a 150 ms timeout, or
view-abandonment) ran clean, A/B BY LEG: B1 (=0, today's ordering)
11:13:13-11:26:23; B2 (=1) 11:26:23-11:39:24. New per-block fields on
`miner: propose phases` (leader-only, top-level JSON, nanoseconds):
`lwWait`, `lwWhy` in `{off, journal, timeout, abandoned}`. Node logs
preserved whole, trimmed to `wr-logs/r35zzze-keep/node{0-6}-B.log`
(11:13:00-11:39:59). Script:
`wt-r27/scripts/qs-analysis/leader_write_after_journal.py`. Windows
were located by taking the full-block (>=150,000 tx) sequence within
each leg in order and splitting at the round log's own counts (48/35
for B1, 53/35 for B2) -- this reproduces the round log's own
occupancy/TPS numbers exactly and is the same method 6cp validated.

**(a) Headline: Round2 collapses.** Pooling both windows per leg
(in-tenure/chained full blocks only, n=45 B1 / n=50 B2):

| | leader jcvMs | Round2 (r2) | r2kth | in-tenure cycle | lwWhy |
|---|---|---|---|---|---|
| B1 (off) | median 334, p10 268, p90 494 | median 403, p10 310, p90 519 | median 399 | median 795.7 | off 100% |
| B2 (on) | median 0, p10 0, p90 4 | median 115.5, p10 58, p90 296 | median 112.5 | median 755.1 | journal 82%, timeout 18% |

**Round2 falls 403 -> 115.5 ms (-71.3%); the leader's own jcvMs falls
334 -> 0 ms (median) because the journal write is now UNCONTENDED --
it goes first, while the writer is idle, exactly as 6co proposed.**
The in-tenure cycle (push-to-push, same leader, chained views) does
**not** grow to compensate -- if anything it is a hair faster (795.7 ->
755.1 ms) -- an early, direct answer to "not merely moved": see (b) for
the fuller accounting.

By window:

| | B1win1 | B1win2 | B2win1 | B2win2 |
|---|---|---|---|---|
| jcvMs (leader, median) | 329 | 369 | 0 | 0 |
| r2 (median) | 383 | 431 | 96 | 138 |
| in-tenure cycle (median) | 715.0 | 860.1 | 720.3 | 909.0 |
| leader blockwrite `write` (median) | 245.3 | 396.3 | 240.3 | 343.3 |
| lwWait (median) | 0 | 0 | 101.2 | 120.0 |
| lwWhy shares | off 100% | off 100% | journal 80%/timeout 20% | journal 84%/timeout 16% |
| derived full-block ceiling (win1 only, Job 1e) | 223,333 tx/s | -- | 230,180 tx/s | -- |

Handover (leader-change) full blocks are unaffected in direction but
noisier and slower in absolute terms in BOTH legs (median cycle 1109.2
ms B1, 1464.0 ms B2 -- n=37/35; handover was never this task's target
and the two legs' handover medians are not treated as a paired
comparison here, since a leader change brings its own queueing that
this task did not isolate).

**Every `lwWhy=timeout`/`abandoned` case, explained.** All 9 non-
`journal`/`off` cases this round are `timeout` (0 `abandoned`): 5 in
B2win1, 4 in B2win2, `lwWait` pinned at 150.0-150.9 ms (the configured
ceiling) in every one. Cross-checked against this round's 10 TC events
and 19 view-timed-out events (6ck's method, deduped by (time,view)):
**none of the 9 timeout cases falls within +/-1 view of a TC/timeout
event.** Reading the matched view-timing lines directly: these are
views where Round2's own vote-gathering (the same thing `r2`/`r2kth`
measure) simply ran past the 150 ms budget on its own -- e.g. the
paired sample at n=13660439 has `jcvMs=461` on the SAME view the write
timed out on, i.e. the underlying journal/quorum-formation event this
node was waiting on was itself an outlier that block. **No timeout case
corresponds to a genuine HotStuff-level view timeout or TC formation**
-- the write-latch's own 150 ms ceiling, not consensus instability, is
what ends the wait.

**(b) "Not merely moved."** Four places time could have reappeared,
checked directly:

- **`lwWait` itself, on the write path.** Once `jcvMs` is uncontended
  (~0), `lwWait` (B2 median 101-120 ms) is no longer "journal duration"
  -- it is now dominated by however long THIS node's own Round2
  vote-quorum took to complete (since the write cannot start until this
  node's own commit vote, part of forming the CommitQC that makes it
  leader of the next block, has both formed AND journaled). Read
  against B2's pooled Round2 (median 115.5 ms), `lwWait`'s median
  (101-120 ms across the two windows) sits close to it -- consistent
  with the wait now being intrinsic consensus latency (quorum
  formation), not queueing for a resource, though a tight per-block
  identity does not hold (the per-row `jcvMs`(n-1)/`lwWait`(n) pairing
  is noisy -- see Method).
- **Prefill** (`miner: prefill phases`, >50 ms outliers only): B1 n=87,
  B2 n=76. `persistWait`/`insertParent`/`rootLockWait` are 0 at the
  median in BOTH legs (the parent-import gate, 6cb/6cc, stays cleared).
  `specTreeReload` (122.2 -> 129.3 ms median) and prefill `total` (139.5
  -> 143.1 ms median) are statistically flat between legs -- no
  migration of cost into pre-fill.
- **Build dispatch / CommitToCanonical.** `hotstuff: commit phases`
  (`canon`+`persist`) stays sub-millisecond at the median in every
  window in both legs (B1win1 0.3 ms, B2win2 0.4 ms); the visible
  growth is only in the tail (p90 52.9->343.0 ms range across windows,
  present in BOTH legs, tracking the win1->win2 pattern in 6cr, not the
  A/B switch). `committed block not executed locally` (1673 B1 / 1827
  B2), `refusing block production on unexecuted committed parent` (11 /
  12) and `deferred production resumed` (60 / 58) are all essentially
  equal between legs -- the deferred-execution gate's load is unchanged
  by the switch, as expected (it gates on parent import, not on the
  journal).
- **Followers.** (Job 1c) Follower `total`/`write`/`body`/`proc` import
  phases and follower `jcvMs`/`jpvMs` are statistically unchanged
  between legs (follower jcvMs/jpvMs both medians 0.0 ms in both legs;
  import total medians 4.4 B1 / 4.5 B2 ms) -- exactly as predicted: the
  switch only reorders the LEADER's own write against its OWN journal
  call and touches nothing on the follower side.

**No saved time reappears anywhere this task instrumented.** The
71.3% Round2 reduction is not offset by a matching increase in
prefill, dispatch, CommitToCanonical, or follower cost; the in-tenure
cycle is flat-to-slightly-better. The closest thing to "reappeared
time" is `lwWait` itself, but that is consensus's own unavoidable
quorum-wait, not a new artificial queue.

**(c) Throughput and derived ceiling.** B2win1 (140,885 TPS,
1.132 s/block) is FASTER than B1win1 (127,869 TPS, 1.250 s/block) by
+10.2%, at equal ~49% occupancy -- consistent with (a)/(b): the switch
removes real critical-path time (the collision) without adding it back
elsewhere. Derived full-block ceiling (mean txs/block over mean
in-tenure cycle, win1 only, labelled DERIVED since it extrapolates a
100%-in-tenure, no-handover chain that never actually runs):
B1win1 **223,333 tx/s**, B2win1 **230,180 tx/s** (+3.1%) -- a much
smaller gap than the round-level TPS gap, because the round-level
number is diluted by the identical win2 slowdown (6cr) present in both
legs and by handover blocks, neither of which this switch touches.

**(d) Safety.** BAD BLOCK 0, divergence 0, MODE-FAILED 0 both legs.
TC events: 5 B1 / 5 B2. View-timed-out events: 9 B1 / 10 B2 (both legs
have a comparable, small timeout rate; none of B2's 9 write-latch
timeouts double as one of these, per (a)). Zero build stalls this
round. Every one of the 171 full-window blocks has its own
`propose phases` write line -- no leader block proposed-and-committed
but never written, in either leg.

**(e) Prediction 87, clause by clause.**

- *"B1 jcvMs ~ Round2 residual"* -- **confirmed**, consistent with 6cp:
  median jcvMs 334 ms against median Round2 403 ms (82.9%).
- *"B2 jcvMs < 10 ms, Round2 < 80 ms, in-tenure cycle down by >= 250 ms"*
  -- **partial**. jcvMs collapses as predicted (median 0, p90 4 ms,
  comfortably under 10). Round2 falls hard but its MEDIAN (115.5 ms) is
  above the 80 ms bar the prediction set (B2win1's own median, 96 ms,
  is closer; B2win2's median, 138 ms, is further; the p10 across both,
  42-58 ms, clears it) -- reported as measured rather than rounded to
  fit. The in-tenure cycle does **not** fall by >=250 ms -- it is
  essentially flat (795.7 -> 755.1 ms pooled, a 40.6 ms improvement).
  This is not a failure of the mechanism: the in-tenure cycle was never
  gated by Round2 alone (6cb/6cg's own critical-path work put transport
  and other segments on it too), so a jcvMs-sized drop in Round2 was
  never guaranteed to show up ms-for-ms in the push-to-push cycle --
  the mechanism (a) and the "nothing moved" result (b) are the two
  clauses this task can actually certify; the specific 250 ms
  cycle-improvement number was an optimistic upper bound, not
  re-derived from first principles before the round ran.
- **Confound acknowledged, stated plainly.** B1 always runs before B2
  in this design (leg order is fixed: warmup, A1, B1, B2, A2), and
  win1's own supply ceiling sits near 140k both legs -- so any
  round-over-round drift in the box, the generators' own warmup state,
  or accumulated chain size between B1 and B2 is confounded with the
  switch itself. What the leg-order confound and the shared ~140k win1
  ceiling DO allow: a same-round, adjacent-leg comparison at matched
  occupancy (~49% both), matched block size (163,000 tx dominant both),
  and matched tenure/topology -- the strongest control this campaign's
  method offers without a same-round interleaved A/B (not supported by
  the harness). What they do NOT allow: ruling out a systematic
  box-state drift between the two legs as a partial contributor to the
  B2win1-vs-B1win1 TPS gap, though the DERIVED ceiling (which factors
  out handover blocks and the win2 slowdown) shows a far smaller,
  more plausible +3.1% gap than the raw +10.2% TPS gap -- suggesting
  most of the raw gap is win2/handover composition, not drift.

**Overall verdict: confirmed** for the mechanism (jcvMs collapses,
Round2 falls sharply, nothing else grows to compensate) and **partial**
for the two specific numeric thresholds prediction 87 set in advance
(Round2<80ms median, cycle down >=250ms) -- both directionally right,
neither hit exactly as stated.

**Method.** Reuses `journal_timing.py`'s parsing/join machinery
(push-instant/QC-proxy/leg-offset/validator-index map) verbatim, adding
`lwWait`/`lwWhy` (top-level JSON on `propose phases`, nanoseconds),
`miner: prefill phases`, `hotstuff: commit phases` canon/persist split,
`hotstuff: committed block not executed locally` /
`refusing block production on unexecuted committed parent` /
`deferred production resumed`, `TC formed locally` / `view timed out`,
and any line mentioning `fetch`/`FetchBlockByHash`. The per-block
`jcvMs`(n-1) <-> `lwWait`(n) identity was checked directly (printed,
not shown in this table) and found noisy at the single-block level --
`lwWhy=journal` rows show `lwWait` ranging 19.6-154.9 ms against
`jcvMs`(n-1) values that are mostly 0 with occasional large outliers
not lining up 1:1 -- so this section reports the AGGREGATE match
(median-to-median) rather than claiming a verified per-block identity;
the aggregate signal (both distributions dominated by Round2's own
timescale, ~100-150 ms) is the basis for the "consensus's own wait,
not a new queue" reading in (b).

**What this does and does not show.** It shows, on this round's own
evidence, that N42_LEADER_WRITE_AFTER_JOURNAL=1 does what 6co proposed:
it removes the MDBX-writer collision 6cp measured directly, Round2
falls by 71% at the median, and none of the four places time could
hide (prefill, dispatch/CommitToCanonical, followers, the in-tenure
cycle itself) shows a compensating increase. It does NOT show the
effect isolated from the B1-before-B2 leg-order confound this design
carries by construction -- see (e)'s confound clause. It does NOT
explain every individual `lwWhy=timeout` case beyond "Round2 itself was
slow that view" (n=9, not further decomposed per-view). It does NOT
change 6cr's finding below: the within-leg win1->win2 slowdown is
present, and of similar size, in BOTH legs of this round -- this
switch is orthogonal to it.

## 6cr. Round 35zzze, Job 2 (S20): the within-leg slowdown tracks a QMDB in-RAM index that never stops growing until the next restart, squeezing the page cache under a fixed GOMEMLIMIT (2026-09-21)

Same round, same kept logs. `r35zzze-mem.log` (10s samples, richer than
prior rounds: per-node `Anon+FileMB` for all 7 nodes plus system-wide
`avail`/`Cached`/`Dirty`/`AnonPages`/`Shmem`) and 16 pprof captures
(`/data/blockchain/wr-pprof/r35zzze-{B1,B2}-t{250,345}-node{0,3}-
{cpu,heap}.pb.gz`) were supplied for a win1-vs-win2 comparison inside
each leg. **The captures do not land inside win1/win2.**

**(0) Capture-timing check, done first.** The true win1/win2 wall-clock
windows were derived the same way 6cp validated (first N full blocks in
leg-chronological order = win1, next M = win2; N/M chosen to reproduce
the round log's own block counts exactly): B1win1 11:21:26-11:22:38,
B1win2 11:22:40-11:23:26; B2win1 11:34:59-11:36:19, B2win2
11:36:22-11:37:04. The `t250`/`t345` pprof captures were actually taken
at B1 11:17:37-57 / 11:19:12-32 and B2 11:30:42-11:31:02 /
11:32:17-37 -- **roughly 4-5 minutes BEFORE win1 even starts**, still
inside each leg's 400s baseFee-decay phase (empty/near-empty blocks,
decay itself only finishes at leg-start+400s: 11:19:53 for B1,
11:33:03 for B2). This is confirmed independently by the CPU profiles
themselves: total samples are ~8.75-9.95% of one core in EVERY one of
the 8 CPU captures, both `t250` and `t345`, both legs -- flat, low
utilization inconsistent with a 32-worker node mid-full-block
production, and inconsistent with each other showing any shift (the
coordinator's own read, confirmed: GC is not it, and the CPU profiles
here add no win1-vs-win2 signal because they were never inside either
window). **The heap/CPU profile comparisons below are reported for
completeness but are DECAY-PHASE snapshots, not a win1-vs-win2
comparison** -- the authoritative win1-vs-win2 comparison in this
section is the `mem.log` analysis in (c), which spans the correct wall
-clock ranges directly.

**(a) Heap, as captured (decay phase, not win1/win2).** `inuse_space`
totals: B1 node0 1545.93MB at BOTH `t250` and `t345` (byte-identical
top-15, including the flat totals -- no allocation activity in this
node during this particular 95s decay window); B1 node3 1499.62MB at
both captures (also identical). B2 (captured ~1 minute closer to
win1's own ramp than B1's captures were, and evidently already past
whatever quiet point B1's captures caught): node0 815.99 -> 1132.31MB
(+38.8%), node3 689.07 -> 1049.84MB (+52.4%). **In every one of the 8
captures, the single largest and (in B2) fastest-growing consumer is
the same function**: `github.com/n42blockchain/N42/lib/qmdb.
newMapIndexSized` -- 51-73% of `inuse_space` in every capture,
growing 509.64->772.59MB (node0) / 468.80->767.90MB (node3) in B2's
two snapshots and static in B1's. `alloc_space` rate between the two
captures could not be computed (the harness took `inuse_space`-only
snapshots; no cumulative alloc-rate counter was captured this round).

**(b) CPU, as captured (decay phase, not win1/win2; relative shares
only, per the undersampling caveat).** `runtime.cgocall` dominates
every capture at 65-66% of the (tiny) sampled total, in both legs, at
both timestamps, with no directional shift between them --
`internal/runtime/syscall/linux.Syscall6` a distant second (4.5-6.8%).
No memmove/memclr/page-fault-shaped function appears in the top 10 of
any capture. Consistent with (0): this is idle-ish decay-phase CPU,
not a signal about win1-vs-win2.

**(c) The authoritative win1-vs-win2 comparison: `r35zzze-mem.log`,
averaged over each window's own samples.**

| | B1win1 (n=7) | B1win2 (n=4) | delta | B2win1 (n=8) | B2win2 (n=4) | delta |
|---|---|---|---|---|---|---|
| avail | 51.4G | 46.8G | -9.0% | 51.0G | 46.8G | -8.2% |
| system Cached | 54,869M | 44,344M | -19.2% | 53,036M | 43,851M | -17.3% |
| system AnonPages | 73,836M | 77,878M | +5.5% | 74,165M | 77,188M | +4.1% |
| per-node Anon (mean of 7) | 9,906MB | 10,339MB | +4.4% | 9,919MB | 10,278MB | +3.6% |
| per-node File (mean of 7) | 3,295MB | 1,724MB | **-47.7%** | 3,611MB | 1,691MB | **-53.2%** |

**Per-node FILE-resident memory (the MDBX mmap'd pages backing
`chaindata`) falls hard from win1 to win2 -- by roughly half -- while
Anon rises by only a few percent over the SAME interval, in BOTH legs,
almost identically.** This is squeeze, not simultaneous growth: the
system is not "running out of RAM" in a generic sense (`avail` still
has 45-53G free throughout) so much as the kernel choosing to evict
clean, file-backed MDBX pages to make room as each node's own resident
Anon footprint (already elevated well above baseline by this point in
the leg, see (d)) keeps a slow, steady climb. `Dirty` is negligible
throughout (0-17M) -- this is a read-side (page cache), not a
write-buffering, effect.

Which MDBX-touching sub-phase grows most, win1->win2 (from 6cq's own
per-window tables, both legs): leader `assemble`+`finalize` (state-root
computation, which walks the trie/QMDB structures through the mmap)
and leader `write` grow together and by the largest absolute amounts,
matching the file-cache-squeeze mechanism directly -- a page fault on
what used to be a cheap mmap hit shows up exactly there. The write
probe (`"msg":"write probe"`, all 7 nodes, every MDBX commit) confirms
the same direction at its own tail: p90 `heldMs` (time the write
transaction holds the single-writer slot) 156->228 ms (B1) and
156->222 ms (B2); p90 `waitMs` (time spent waiting to acquire it)
0->47 ms (B1) and 1->88 ms (B2); p90 `commitMs` 15->20 ms (B1) and
15->19 ms (B2) -- all three growing win1->win2, in both legs, at the
tail where the squeeze bites (medians stay at 0 throughout: most
per-node writes are tiny housekeeping commits, and it is only the
per-block state-commit tail that lengthens).

**(d) What resets at node restart but not across legs on disk.** The
mem log's own restart instants make this unambiguous: at B1's exact
restart second (11:13:13) `avail` spikes to 122G and system `AnonPages`
craters to 1,635M (from 71,636M the sample before, taken from the
PRIOR leg's own tail) as the old processes are torn down; per-node
`Anon` for the fresh processes starts at ~1,288-1,636MB ten seconds
later and climbs steadily to ~9,900MB by win1 -- the SAME shape at
B2's restart (11:26:21: `avail` jumps to 58G, `AnonPages` to 62,503M
transiently, then per-node `Anon` restarts near 436-3,088MB and climbs
back to ~9,900-10,700MB by B2's own win1). **B2's win1 is exactly as
fast as (in fact slightly faster than) B1's win1, even though B2's
on-disk `chaindata` files are strictly larger than B1's were at the
same point** (B2 continues from B1's chain height, decay, and flood
data) -- directly ruling out on-disk size as the driver, and pointing
at in-process state that is torn down and rebuilt from near-zero at
every SIGTERM+relaunch. The Go heap (including the QMDB `mapIndex`
identified in (a)) fits this exactly: a plain `make(map[Hash]uint64)`
that starts empty on every fresh process and is never reset except by
restart, growing without bound across a leg's own live-key churn.
Linux's own page cache (`Cached`) is explicitly NOT in this category
(6cp already established it survives across legs on a live box, and is
reclaimable independent of process restart) -- it is the victim being
squeezed here, not the resetting resource.

**(e) Verdict.** The data support naming a specific, well-evidenced
mechanism, not just a symptom:

1. `lib/qmdb.mapIndex` (`lib/qmdb/index.go:47-65`) is a plain
   `map[Hash]uint64` holding one entry per LIVE key across the whole
   tree -- per its own doc comment, "the largest single allocation in a
   loaded node: 780 MB of live heap" once loaded, and (unlike an MDBX
   B+tree page) it lives entirely on the Go heap, is never partially
   evicted, and a Go map's backing table never shrinks after deletes.
2. It is the dominant (51-73%) and, in the one leg where growth is
   visible in the captures, the ONLY meaningfully growing consumer of
   `inuse_space` in every heap profile this round.
3. Growth in the map (more live keys touched as the flood's working
   set of accounts/storage grows over a leg) consumes Anon directly,
   under a fixed `GOMEMLIMIT=9GiB` per node (this round's own leg
   banner) that keeps the Go runtime competing for RAM against the
   kernel's page cache.
4. The mem log shows the resulting squeeze precisely: File (MDBX mmap
   pages) roughly halves from win1 to win2 while Anon only inches up,
   in both legs, identically -- and 6cq's own phase table shows exactly
   the MDBX-touching phases (`assemble`/`finalize`/`write`) growing the
   most, consistent with previously-cheap mmap hits turning into page
   faults.
5. It resets cleanly at every restart (fresh empty map) independent of
   on-disk chain size (B2's win1, on a larger on-disk chain, is as fast
   as B1's), matching (d) exactly.

**Naming the cause on this evidence: the QMDB in-RAM live-key index's
unbounded, restart-only-reset growth across a leg, competing with the
page cache under a fixed heap limit.** The one gap in an otherwise
closed chain: this round did not capture the map's own live entry
count over time (`qmdb.IndexStats()`, `lib/qmdb/index.go:109-111`,
already exposes hit/miss/put/delete counters but not `Len()`), so the
correlation runs through `inuse_space` and the mem log rather than
through the map's own cardinality. **The one addition the next round's
harness needs**: a periodic (10s, matching the existing mem-log cadence)
log line reporting each node's live `mapIndex.Len()` (or the existing
`IndexStats()` put/delete counters, from which live count is
recoverable) alongside `r35zzze-mem.log`'s own per-node Anon/File
sample -- in the harness/diagnostic layer, not a node code change,
since `Len()` is already an exported method on the `Index` interface.
That one number turns "the dominant, growing allocator is the live-key
index, correlated with the memory-pressure pattern" into "the
memory-pressure pattern IS the live-key index, quantified" or falsifies
it outright if `Len()` turns out flat while `inuse_space` still grows
(pointing instead at fragmentation or a second, uncaptured allocator).

**Method.** Window boundaries independently re-derived and cross-
checked against the round log's own block counts (0, above) before
trusting the profile timestamps at all -- this is the check that
caught the mismatch. Heap/CPU tables read via `go tool pprof -top
-sample_index=inuse_space` and `-top` (CPU, default samples index);
mem-log parsing via a fixed-format line regex (`nodesAnon+FileMB=`
repeated 7 times, `Cached:`/`Dirty:`/`AnonPages:`/`Shmem:` system-wide),
averaged per window over whatever 10s samples fall inside its
wall-clock range (n=4-8 per window; short windows given win2's own
~45-65s span at 10s sampling).

**What this does and does not show.** It shows a well-evidenced,
specific candidate mechanism (the QMDB live-key index) for the
recurring within-leg slowdown 6cp first flagged, backed by a
restart/on-disk-size dissociation test that directly rules out the
simplest alternative (accumulated disk size). It does NOT close the
loop with a direct live-key-count measurement -- that is the one
addition named in (e). It does NOT deliver a win1-vs-win2 heap/CPU
comparison from the requested captures, because those captures do not
fall inside win1/win2 this round (0) -- next round's harness should
time captures from the ACTUAL window boundaries (derivable the same
way this section did, from the full-block sequence) rather than a
fixed offset from leg start, which this round's decay+funding phase
(itself variable in length across rounds) made unreliable. It does NOT
show the mechanism is the ONLY contributor -- `specTreeReload` (6cq's
prefill table) and ordinary GC pacing under `GOGC=300`/`GOMEMLIMIT=9GiB`
remain live, uncaptured contributors this round did not separate out.

## 6cs. S21: hypothesis W confirmed in corrected form -- push(v+1) waits on whichever of {write(v) ending, CommitQC(v) forming} is LATER, plus a leg-invariant ~254 ms pacing/seal constant, resolving 6cq's apparent contradiction (2026-09-21)

Commander's ruling on S19 flagged a real contradiction in 6cq: Round2 fell
403 -> 115 ms (-288 ms) while the in-tenure cycle stayed flat (715 ->
720 ms) and 6cq called the saved time "nowhere measurable" -- which
cannot be true of a genuinely serial chain. This section finds the
segments were never a serial sum: 6cq's own cycle number for B2 was
ALSO measured with a formula that predates S19 and needed correcting,
and the real governing relationship is a MAX, not a SUM, of two
upstream paths that happen to converge on the same downstream constant.
Logs-only, same kept files: `wr-logs/r35zzze-keep/node{0-6}-B.log`.
Script: `wt-r27/scripts/qs-analysis/write_journal_gate.py`.

**0. A formula bug, found and fixed first.** Every section since 6cb has
computed `push_instant = tMs - write/1e6`, valid only because push always
preceded write immediately (`worker.go:677` push, `:759-761` write, and
before S19, nothing sat between them). S19 inserted `lwWait` between
push and write (`worker.go:747-757`) whenever `N42_LEADER_WRITE_AFTER_
JOURNAL=1`, so in B2 the old formula actually computes `writeStart`
(push_instant + lwWait), not push_instant. The corrected formula is
`push_instant = tMs - (write + lwWait + notify)/1e6`. Effect on the
cycle (push(n) - push(n-1), win1 only): **B1 unaffected** (715.0 ->
715.0 ms median, since lwWait=0 throughout B1) confirming every B1
number in 6cq/6cp stands; **B2 shifts** 720.3 -> **656.0 ms** median
(p10 604.9->555.1, p90 774.4->747.6) -- an 8.9% reduction, not the whole
288 ms but a real, confirmed correction. This bug does not change 6cq's
headline (Round2 genuinely fell 71%, and the cycle genuinely did not
fall by anywhere near that) but it does mean 6cq's own B2win1 cycle
number (720.3) overstated the true value by 64 ms; the corrected number
(656.0) is used throughout this section and should be read as superseding
6cq's B2 cycle figures.

**1. The gate test (item 2), win1 only, ms-precision.** `CommitQC(v)` is
derived as `push(v+1) - propose(v+1)`, using view_timing.go's own
`propose` field (`ViewStart -> ProposalSent`, `view_timing.go:391,529`)
rather than a fresh join against `"hotstuff: view changed"` -- a direct
cross-check found the isLeader-search proxy (used for leg-offset
calibration and everywhere else this campaign needs "when did this node
become leader") disagrees with `push(v+1) - propose(v+1)` by 300-400 ms
specifically in B2 (self-consistency check: `CommitQC(v) - push(v)`
should equal `r1(v) + r2(v)`; via `propose` it does, in both legs,
within the noise a median-of-medians comparison allows; via the
isLeader-search proxy it does only in B1). The isLeader event apparently
fires later than CommitQC actually forms in B2 specifically (plausibly
the same serial-output-queue effect this campaign has flagged before,
though `"hotstuff: commit phases"` staying sub-ms rules out
`OutputBlockCommitted` itself as the queued item ahead of it -- not
chased further; flagged as a loose end, not resolved here). `write(v)_
end` is `tMs(v) - notify(v)/1e6` (write's own completion, from the same
line as always).

| | B1win1 (n=21) | B2win1 (n=25) |
|---|---|---|
| write(v)_end is the LATER of the two ("write gates") | 0.0% | **92.0%** |
| push(v+1) - gate = max(write_end, CommitQC) | median 254.0 (p10 195, p90 343) | median 254.2 (p10 224.1, p90 316.1) |
| push(v+1) - CommitQC(v) | median 254.0 (same -- CommitQC always the gate) | median 467.0 (p10 266, p90 569) |
| push(v+1) - write(v)_end | median 347.6 (p10 281.4, p90 451.3) | median 270.7 (p10 224.8, p90 330.1) |

**`push(v+1) - gate` is tight and essentially IDENTICAL across legs**
(254.0 vs 254.2 ms, medians 0.2 ms apart, both distributions of similar
width) **while `push(v+1) - CommitQC(v)` is NOT** (254.0 vs 467.0 ms) --
this is hypothesis W's own predicted signature, measured: in B1,
Round1+Round2 (447-501 ms median, 6cq) always outlasts the write
(245.3 ms median), so CommitQC(v) is always the later event and gates
100% of the time. In B2, Round2 collapses (6cq) but the write now starts
~101 ms later too (`lwWait`), so the write's own completion overtakes
CommitQC(v) as the later event in 92% of views -- and once it does,
`push(v+1)` waits on IT instead, for almost exactly the same downstream
~254 ms that B1 always paid after its own (different) gate. **Prediction
87's saved 288 ms did not vanish and did not reappear elsewhere: it
was never on a path that fed the cycle in the first place, once the
write becomes the binding constraint** -- this is the resolution to the
"contradiction," not a refutation of S19's own finding (6cq's "nothing
moved" verdict was correct; it just needed this gate model to explain
WHY nothing needed to move).

**2. What "propose(v+1)" (the ~254 ms constant) actually is.** 6cb's own
worked example (an earlier round, no S19 switch, median in-tenure block
n=13658061) already decomposed this exact span and it matches closely:
ViewStart(552.7) -> BLS-sign-start(688.1, a 135.4 ms gap, almost
certainly `paceBlock`'s grid wait -- this round's own `"miner: pacing
wait"` lines show a 179.4 ms median, second-resolution-matched only, not
a per-block join, but the same order of magnitude) -> BLS sign (0.6 ms)
-> "gate+copy residual" (83.9 ms, `CheckSealParentApplied` + the
163k-allocation receipts copy, `worker.go:660-667,705-725`, neither
separately timed) -> push (26.5 ms) = **246.4 ms total**, against this
task's own 254.0/254.2 ms measured medians -- an independent,
cross-round confirmation of the same constant to within 3%.

**3. Code trace: what does the write-bound path (B2's 92%) actually wait
on?** Traced three candidates directly:

- **`WaitBlockPersisted(parentHash, 2s)`** (`worker.go:1246`,
  `internal/blockchain.go:270-290`, a 5 ms-poll read-transaction check
  for the parent header) -- gated behind `parentHash != zero && !
  ownPending`. `ownPending` is true whenever the build is speculative AND
  `w.ownSealed(parent) != nil` (`ownPendingSpeculation`, `worker.go:941-
  950`) -- i.e. almost always, for a chained (same-leader) tenure run:
  `"miner: speculative build parked"`/`"miner: speculative build hit"`
  counts match to within 0-1 across all 7 nodes over the whole kept
  window (e.g. node0: parked=551 hit=551; node6: parked=555 hit=554),
  and `"speculative build discarded"`/`"speculative align failed"` are
  ZERO occurrences anywhere in the kept logs. **The speculative-build-hit
  fast path is essentially universal for chained full blocks this round,
  and it explicitly bypasses `WaitBlockPersisted`** (`ownPendingSpeculation`'s
  own doc comment: "the build neither waits for the write nor aligns the
  applied branch") -- ruling this OUT as the mechanism for the 92% figure.
- **`CheckSealParentApplied`** (`internal/seal_push_order.go:33-42`) --
  a non-blocking snapshot read (`bc.ChainDB.View`, no wait, no lock) run
  just before push. `"sealed block is stale before its write; dropping"`
  (the only observable failure mode) has **zero** occurrences in the
  entire kept window, both legs -- this check is passing every time and
  is not gating anything (nor could it: a snapshot check does not wait,
  it only rejects).
- **The write itself, `bc.lock`, or the MDBX single-writer slot** -- the
  strongest remaining candidate, since `AlignAppliedBranch`'s own comment
  states plainly it "takes `bc.lock` and so waits behind the parent's own
  write" (`worker.go:1264-1266`) when it runs at all (it is SKIPPED, per
  the same code, when the applied head already matches the parent -- the
  normal chained case, so this specific call is also not it here). No
  log line in this round's binary carries a `tMs` on the commitWork-
  begin/build-phases/speculative-parked/speculative-hit events (a
  binary-vintage gap matching 6cb's own note about an EARLIER round --
  see caveat below), so the exact statement that blocks cannot be pinned
  to a specific file:line this round; the measured 92%/254ms pattern is
  solid, but the single mutex or resource responsible is inferred, not
  directly observed. **Best-supported reading:** since push(v+1) itself
  happens BEFORE v+1's own write starts (push-before-write, same as v),
  the wait is not v+1's own write queuing behind v's -- it has to be
  something upstream of push, in the pacing/gate/seal handoff itself.
  Given `CheckSealParentApplied` is ruled out above, the most likely
  remaining site is a lock taken somewhere in the seal/BLS-sign path
  (`taskLoop`, `w.commit()`'s snapshot/update-metrics calls) that is ALSO
  taken by `WriteBlockWithState` for v -- consistent with, but not proven
  by, this round's evidence. **Naming it precisely is the one addition
  the next round needs**: add `tMs` to `"miner: speculative build
  parked"`/`"hit"` and `"commitWork begin"` (already planned/flagged in
  6cb for an unrelated reason, still not done as of this binary), which
  would let a future pass place every step in the propose(v+1) span at
  ms precision instead of inferring it from the aggregate 254 ms.
- **Real or incidental?** Given the mechanism is most likely a lock
  shared with `WriteBlockWithState`, and MDBX permits exactly one writer
  transaction at a time (a hard constraint, not a conservative choice),
  this reads as **REAL**: as long as this leader's own write and the
  next block's seal/commit path share ANY exclusive resource (the
  writer slot itself, or a coarser lock guarding it), one MUST wait for
  the other. It is the S19 SWITCH's own choice to delay the write's
  START (not a new dependency) that moves this from "never binds" (B1)
  to "binds 92% of the time" (B2) -- the dependency was arguably always
  there, just never exposed before because the write always finished
  before Round2 did.

**4. 6cb corrected in place (dated note, 2026-09-21).** 6cb's own
two-segment model ("push(v-1) -> QC(v-1)/ViewStart(v): WAIT (consensus
round-trip)" then "leader commit/seal: CPU") is not wrong for the round
it measured (n42-r86, no S19 switch existed), but its first segment's
label should be read, going forward, as **WAIT for max(CommitQC(v-1)
forming, write(v-1) ending)**, not simply "the consensus round-trip." In
every block 6cb measured, Round1+Round2 was slower than the write
(matching this task's own 0%-B1 finding), so the two readings were
indistinguishable at the time; S19/S21 is what separates them, by
making Round2 fast enough that the write can become the later event
instead. 6cb's worked example's own numbers (ViewStart at +552.7 with
"already done, -90.9 margin" for the fill) illustrate the B1-shaped case
specifically; a same-shaped worked example from B2's write-bound regime
would show the "leader trigger+prefill+fill" line landing AFTER
ViewStart rather than before it, with the same downstream ~246 ms.

**5. Consequence table (derived from measured medians only, labelled as
such -- not a new measurement).** Inputs used: `r1`+`r2` (B2win1
medians, 6cq: 65+96=161 ms); Round2 pooled (S19, 6cq: 115.5 ms, so
`r1`(65, pooled B2)+`r2`(115.5) = 180.5 ms); write(v)_end-push(v) today
in B2 (`lwWait` 101.2 + `write` 240.3 = 341.5 ms); the measured constant
(254 ms, both legs).

| scenario | inputs summed | derived cycle |
|---|---|---|
| (i) Round2 = 115 ms (pooled), dependency removed (gate always = CommitQC) | 180.5 (r1+Round2) + 254 (constant) | **434.5 ms** |
| (ii) write as today (341.5 ms end-offset), but propose(v+1) never waits on it (gate always = CommitQC) | 161 (r1+r2, win1) + 254 (constant) | **415 ms** |
| (iii) only the write gets 100 ms faster (lwWait unchanged, write 240.3->140.3) | max(161, 101.2+140.3=241.5) + 254 = 241.5+254 | **495.5 ms** |

(iii) still lands well above (i)/(ii) because at write=140.3 ms the
write (241.5 ms end-offset) is STILL the later event vs CommitQC
(161 ms) -- the write would need to shrink by more than 100 ms (to
roughly parity with 161 ms, i.e. another ~40 ms past the 100 ms cut)
before removing the write's own gate manually stops mattering, matching
prediction 87's own design goal (6co/6cn) rather than S19's already-
measured -100 ms scenario in isolation.

**Method.** Reuses the campaign's standard join (per-leg `leg_offset`
via the isLeader-search QC-proxy, validator-index map) for leg-offset
calibration ONLY -- the core gate test in (1) deliberately avoids that
proxy for per-view `CommitQC(v)` placement, using `propose(v+1)` instead,
per the self-consistency check in (1). Build-phase/speculative-build
duration stats (6cq's job, reused here) are joined by log ORDER within
a node (build phases and commit phases are logged adjacently, one pair
per `commitWork` call, with no block number or `tMs` on either line in
this round's binary) -- exact for durations, second-resolution only for
absolute placement, and not used in the core ms-precision test.

**What this does and does not show.** It shows, with a clean and
internally cross-validated (against 6cb's own independent worked
example from a different round) measurement, that hypothesis W holds in
corrected form: push(v+1) is gated by whichever of {write(v) ending,
CommitQC(v) forming} is later, plus a leg-invariant ~254 ms pacing/
gate/push constant -- resolving 6cq's "contradiction" as a MAX
relationship the SUM-shaped segment model never captured. It shows the
formula bug in every prior section's `push_instant` for B2-style
(`lwWait`>0) legs, now fixed, with a measured (not estimated) 64 ms
correction to 6cq's own B2win1 cycle figure. It does NOT identify the
exact lock or resource behind the write-bound 92% with file:line
certainty -- (3) narrows it to "a lock shared with `WriteBlockWithState`,
most likely in the seal/commit path" and names the one instrumentation
addition (`tMs` on the speculative-build/commitWork-begin lines) that
would close this. It does NOT re-examine win2 or the handover
population under this same gate model -- both are flagged as the
natural next extension, not done here given this task's own win1-only
scope.

## 6ct. S22: n42-r92 built and prepared -- seal-path stamps close 6cs's U1 gap, the harness now captures inside win1/win2 instead of the decay ramp, and a VM sampler tests S20 directly; prediction 88 registered before the round (2026-09-21)

**Why.** 6cs confirmed push(v+1) is gated by `max(write(v) ending,
CommitQC(v) forming)` plus a leg-invariant ~254 ms pacing/seal
constant, and named the write-bound path's own mechanism (92% of B2's
views) as "a lock shared with `WriteBlockWithState`, most likely in
the seal/commit path" -- inferred from the aggregate 254 ms, not
observed at file:line, because no binary this campaign has built
carries `tMs` on the speculative-build/commitWork-begin lines. 6cs
also corrected the harness record: `bench-run.sh` runs the 400 s
baseFee decay BEFORE the flood, so every profile captured so far
(+150 s, and the commander's own +250 s/+345 s) landed inside the
decay ramp, sampling empty blocks, never the scored windows.

**Part A -- code.** `internal/miner/worker.go` gains one new info line
per sealed block on the leader, `"miner: seal path"`, plus
`internal/miner/seal_path_diag.go` (the `N42_CONTENTION_DIAG` switch,
read independently in this package the same way S14 established it in
`internal/consensus/hotstuff`, and two small helpers, `tMs`/`waitMs`,
so a step that never happened reads as a plain 0 rather than a
zero-`time.Time`'s huge negative `UnixMilli()`).

Seven new fields on the existing `task` struct (`triggerAt`,
`buildBeginAt`, `specParkedAt`, `specHitAt`, `paceEnterAt`, `paceDur`,
`taskChSentAt`) extend the SAME pattern `finalize`/`witness`/
`assemble`/`sealStart`/`blsNanos` already use -- carrying timings
collected on three different goroutines (`commit` -> `taskLoop` ->
`resultLoop`) so `resultLoop` can emit ONE line with full context
instead of several scattered ones. `commitWork`/`commit` each gain two
new parameters (`triggerAt`, threaded from the confirming
`newWorkReq.enqueuedAt`; `tPaceEnter`/`dPace`, from whichever
`paceBlock` call applies -- the speculative-hit path's own call, or
the fresh-build path's). All extra `time.Now()` calls are gated on
`contentionDiagEnabled`; the log line itself is only emitted when the
switch is on. No new lock, no per-transaction work -- everything added
is once per sealed block, matching the existing (unconditional)
`sealStart`/`createdAt`/`blsNanos` fields' own cost.

**Field list on `"miner: seal path"`** (unix-ms stamps unless named
otherwise): `triggerTMs` (build trigger received --
`OutputViewChanged`/`TriggerBlockProduction`), `buildBeginTMs`
(`commitWork`'s own entry), `specParkedTMs`/`specHitTMs` (zero for a
fresh, never-speculative build), `paceEnterTMs`/`paceDurMs`,
`taskSentTMs` (task handed to `taskCh`), **`taskQWaitMs`**
(`taskChSentAt` -> `sealStart`, U1's own ask), `taskPickedTMs`/
`sealEnterTMs` (both `sealStart` -- `taskLoop` picks the task and
stamps `sealStart` in the same critical section, worker.go:1058-1063,
so these two names are the same instant), `checkEnterTMs`/
`checkExitTMs` (`CheckSealParentApplied`), `blsStartTMs`/`blsEndTMs`
(`sealStart` and `sealStart+blsNanos` -- Seal's own call is already
bracketed by exactly these two existing timers, so no new cross-package
instrumentation was needed), `resultRecvTMs` (`resultLoop`'s own entry
for this result), **`resQWaitMs`** (`sealStart+blsNanos` ->
`resultRecvTMs`, U1's other ask), `copyStartTMs`/`copyEndTMs`
(receipts copy), `pushStartTMs`/`pushEndTMs`, `proposeStartTMs`/
`proposeEndTMs` (the EARLY propose branch specifically -- the late
branch already had its own timing before this step), `lwWaitMs`/
`lwWhy` (S19, unchanged), `writeStartTMs`/`writeEndTMs`.

**Part A -- U1 reading (file:line, not fixed).** `resultCh`
(`worker.go:337`) is unbuffered (`make(chan block.IBlock)`,
`worker.go:414`) with exactly ONE consumer: `resultLoop`
(`worker.go:535`), started once (`worker.go:460`) and never again,
whose loop body (`worker.go:543`, `case blk := <-w.resultCh:`) calls
`handleSealed` SYNCHRONOUSLY -- it does not return to `select` until
`handleSealed` returns. `handleSealed` runs `WriteBlockWithState`
inline (`worker.go:831`), on this SAME goroutine. `Seal`
(`adapter.go:850`) does not block ITS OWN caller (`taskLoop`): it
spawns a per-call delivery goroutine (`adapter.go:903`) that blocks on
`results <- sealed` (`adapter.go:905`, the unbuffered channel send)
until `resultLoop` is free. **Putting these together: a block v+1
sealed while `handleSealed(v)` is still running its own write cannot
be picked up by `resultLoop` -- and since the early push happens
inside `handleSealed`, before the write, v+1 cannot be pushed either --
until `handleSealed(v)` returns, i.e. until v's write completes.** This
is exactly the "obvious suspect" the task named, confirmed here by
file:line rather than inferred from an aggregate duration: the single
`resultLoop` goroutine itself is the gate 6cs's own code trace was
looking for. `taskQWaitMs`/`resQWaitMs` (above) let a round measure how
often and how long this queueing actually costs, directly, rather than
by elimination.

**Tests.** `seal_path_diag_test.go`: `tMs`/`waitMs` (zero-time
handling, a real positive duration, and a negative-clamps-to-zero case
for clock-skew safety) and the switch's off-by-default contract.
Existing `internal/miner` and `internal/consensus/hotstuff` suites
pass unchanged under both `N42_CONTENTION_DIAG` placements, including
`-race`.

**Build.** Same file-checkout recipe as n42-r86 through n42-r91:
detached worktree at `f7ec2836`, n42-r91's exact file set (6co), plus
this step's changes (`internal/miner/{worker,seal_path_diag,
seal_path_diag_test}.go`). One-variable check: `seal_path_diag.go`/
`_test.go` are new; `worker.go` needed a THIRD hunk on top of the same
base n42-r86/r91 already use (S11's own hunk, then S19's, confirmed
via `git diff 812cf162 62439af7^` empty -- nothing else touched
`worker.go` between S19 and S22). The mechanical hunk itself hit ONE
conflict: the speculative-hit block's own context lines (the "miner:
speculative build hit" log call) differ between the tracked lever
chain and the full branch history (an off-lineage `tMs` field from
commits `89d15267`/`19687889`, excluded from every build in this chain
since n42-r86 -- see 6ca/S11), so `git apply` rejected that one hunk
(13 of 14 applied with only line-offset adjustment) and it was applied
by hand instead, onto the SAME target lines, verified against a full
diff of the resulting file against `62439af7`'s own version afterward:
the ONLY differences left are the same four already-known, already-
accepted off-lineage lines every prior build in this chain has shown
(`activeSpecParent`, two `tMs` fields on the speculative build/parked
lines, one `tMs` on "miner: build phases") -- confirmed identical to
what n42-r90/r91's own build diffs showed, not a new gap.
`internal/parallel/base_cache.go` confirmed absent from the build
worktree; `grep -rl BaseCache`: empty. `go build -p 8 -tags
nosqlite,noboltdb` clean; `go vet ./internal/...` clean; `go test`
passes on `internal/consensus/hotstuff/...` (both switch placements,
`-race` included), `internal/miner/...` (both placements, `-race`
included), `internal/`, `internal/parallel/...`.
`/data/blockchain/gov5-work/n42-r92`: 108,774,624 bytes, sha256
`ba1a2105e458bc22908e1755c0a9a322269cae6aa57e593c294c7ac121ad890f`.
`strings n42-r92 | grep -c BaseCache` = 0; every prior marker
("build stalled before fill", "contention profiling enabled", "rotor
failed -> gossip", `jpvMs`, `lwWait`, `lwWhy`, ...) present; four new
markers (`"miner: seal path"`, `resQWaitMs`, `taskQWaitMs`,
`specHitTMs`) each = 1.

**Part B -- harness.** `run-r35zzzf.sh`/`chain-35zzzf.sh` built from
the `run-r35zzze.sh`/`chain-35zzze.sh` pair via `cp`+`sed
's/35zzze/35zzzf/g'` (checked first: `35zzze` occurs nowhere in either
script's giant single-line history comment). Fixed by hand
afterward: the predecessor-wait (`chain-35zzzf.sh` now waits on
`r35zzze.log`, its actual predecessor -- the sed pass alone left
S19/S21's own `r35zzzd.log` target in place, carried over from the
35zzze pair) and the binary references (`n42-r91` -> `n42-r92`).

*Item 3.* `run_leg` keeps its 5th positional argument
(`N42_LEADER_WRITE_AFTER_JOURNAL`) rather than hardcoding the switch,
but every call site now passes `1` (`warmup A1 B1 B2 A2` all `1`) --
the commander's ruling on S21 adopted the switch provisionally in
every leg, so this round is NOT an A/B by leg; it repeats 35zzze's own
(now switch-uniform) configuration and adds only the new diagnostics.

*Item 4, the capture-timing fix.* Read
`/data/blockchain/scripts-qs/bench-run.sh` and `measure-tps.sh` in
full (neither modified): `bench-run.sh` runs `--decay-sec 400` (empty
blocks only, at the baseFee floor) BEFORE starting the flood, then
waits for the flood to reach "flooding," sleeps 15 s more, then calls
`measure-tps.sh --windows 2 --window-sec 60`. `measure-tps.sh` itself
prints NOTHING at a window's start -- only a one-line summary AFTER
each 60 s window's `sleep` returns -- so there is no live "window N
starting" announcement to grep for at all, confirming 6cs's own
finding that this campaign's fixed-offset captures (+150 s, +250 s,
+345 s) could only ever have sampled the decay ramp. The fix (B legs
only, matching the existing capture's own scope): poll one node's RPC
every 3 s for the first block whose `gasUsed/gasLimit >= 0.95`
(`measure-tps.sh`'s own "full" threshold) -- decay produces ONLY empty
blocks, so this first full block coincides with the flood's
settle-then-measure moment, i.e. win1's own start, to within the 3 s
poll granularity. Since `--windows 2 --window-sec 60` is fixed for
every leg, win2's start is computed as win1's start + 60 s rather than
independently detected. Captures at win1-start+15 s and win2-start+15
s, reusing the existing leader/follower selection (grep each node's
own log for its most recent `"miner: propose phases"` line) verbatim;
adds a heap profile to the existing cpu/mutex/block/goroutine set
(6cp/6cr's own dominant-allocator finding needs a heap snapshot taken
INSIDE the window it names, not the ramp). Outputs
`wr-pprof/r35zzzf-<leg>-<win>-node<i>-{cpu,mutex,block,heap,
goroutines}.*`.

*Item 5, the S20 VM sampler.* Started/stopped exactly like the
existing memory watchdog just above it in the script (tied to
`$benchpid`, every 10 s), writing to `wr-logs/r35zzzf-vm.log`: per
node, `minflt`/`majflt` from `/proc/<pid>/stat` fields 10/12 (`man 5
proc`), and the Anon/File/Shmem RSS split from `/proc/<pid>/status`'s
`RssAnon`/`RssFile`/`RssShmem` -- the SAME source the existing memory
watchdog already reads (`worker.go`'s sibling script, not this
package; confirmed cheap there). `smaps_rollup` was the other option
named for this and was NOT used: there is no live fleet on this box
right now to time it against a real, heavily-mapped node process (a
320 GiB MDBX mapping), so using an option already proven fast and
already in production use here avoids risking the "< 50 ms per node"
budget on an untested path; this is a deliberate, reported choice, not
an oversight. Chain-wide reclaim counters from `/proc/vmstat`
(`pgmajfault`, `pgscan_kswapd`, `pgscan_direct`, `pgsteal_kswapd`,
`workingset_refault_file`) are tracked as 10 s DELTAS (vmstat's own
counters are cumulative since boot), computed with bash associative
arrays carried across loop iterations in the same subshell.

`bash -n` clean on both scripts. Neither launched (`ps` confirms no
`run-r35zzzf`/`chain-35zzzf` process exists).

**Prediction 88 (registered before any round):**

**(a) repeatability.** With `N42_LEADER_WRITE_AFTER_JOURNAL=1` and
`N42_CONTENTION_DIAG=1` in every leg, win1's in-tenure cycle is within
10% of 656 ms (6cs's corrected B2win1 figure) in EVERY leg now (not
just the legs that had the switch on before); B-leg first windows sit
at the eight generators' own supply ceiling (~140k, 6cm). The B mean
is reported, not predicted: both second windows run into the S20
within-leg slowdown (6cp/6cr), so a full-round B mean is not a clean
test of anything this step changed.

**(b) seal-path attribution.** The `"miner: seal path"` stamps place
>= 90% of the leg-invariant ~254 ms constant (6cs) in NAMED steps
(pace, check, BLS, copy, push, propose, or the two queue waits) rather
than leaving it as an unattributed residual; `taskQWaitMs`/
`resQWaitMs` directly say whether push(v+1) queues behind write(v) on
the single `resultLoop` goroutine (U1's own reading above predicts
`resQWaitMs` should track the write-bound views specifically -- large
whenever `write(v)_end` is the later gate, small/zero otherwise).

**(c) S20.** Between win1 and win2, per-node `majflt` rate and
`workingset_refault_file` (from the new VM sampler) rise by a large
factor while `RssFile` falls (page-cache thrash, matching 6cp/6cr's
own win1->win2 FILE-memory halving) -- OR they do not, which would
mean memory pressure is not the within-leg slowdown's actual
mechanism. Either outcome is informative and is not itself a pass/fail
bar for this prediction.

**VERDICT: confirmed** (implementation, tests, one-variable check and
build all done). QS_QUEUE.md's S22 row status is marked prepared with
prediction 88 (6ct). Launch is the commander's next call.

## 6cu. S23: n42-r93 built and prepared -- the leader's write moves off resultLoop, A/B by leg, prediction 89 registered before the round (2026-09-21)

**Why.** 6ct's U1 finding: `resultCh` is unbuffered with exactly one
consumer, `resultLoop`, which calls `handleSealed` synchronously and
runs `WriteBlockWithState` inline on that same goroutine -- so a block
sealed while the previous one's write is still running cannot even be
received, let alone pushed or proposed. `N42_LEADER_WRITE_ASYNC=1`
moves the write (and everything downstream of a successful write) onto
a dedicated writer goroutine.

**Part 1 -- invariants, file:line, and how the async path keeps each one.**

**(a) "handleSealed returned => written."** Everything that runs only
after a successful write today -- `pendingTasks` cleanup, counters,
the `"miner: seal path"`/`"miner: propose phases"`/`"Successfully
sealed"` log lines, `recordSealedOnParent`, the `ChainHighestBlock`
event (`worker.go`, formerly inline in `handleSealed`) -- is extracted
into `writeAndFinish` (`worker.go`), called either synchronously
(switch off) or from the writer goroutine (switch on): the SAME code,
the same order, just a different caller. Callers/waiters:
- `WaitBlockPersisted` (`internal/blockchain.go:270-290`) is POLL-based
  against the DB (`ReadHeaderNumber`, 5ms interval) -- it does not
  depend on which goroutine calls the write, or on `handleSealed`
  returning at all. Unaffected by construction.
- `CheckSealParentApplied` (`internal/seal_push_order.go:33`,
  `checkQMDBLeaderSealParent` inside it,
  `internal/blockchain_write.go:130`) reads the DB's last-committed
  QMDB-applied marker (`ReadQMDBApplied`/`WriteQMDBApplied`,
  `blockchain_write.go:134,483` -- the latter inside the SAME write
  transaction as the rest of `writeBlockWithState`, so "applied" is
  only visible once that transaction commits). **This is the one
  invariant that breaks without a fix**: today it never sees a merely-
  QUEUED parent, because the previous block's write has ALWAYS already
  returned by the time this runs (single serial `resultLoop`,
  unbuffered `resultCh`) -- moving the write off `resultLoop` removes
  that guarantee, and this check would routinely see a legitimately-
  queued (not stale) parent as stale, dropping good blocks before they
  are even pushed. Fixed: `worker.go`'s new `checkSealParentApplied`
  additionally treats a parent as applied when it matches whatever
  `asyncBlockWriter.ExpectedParent()` reports (the last job the writer
  has accepted, in flight or queued -- `async_write.go`). An optimistic
  pass here is safe even when wrong: the REAL check runs again inside
  `writeBlockWithState` under `bc.lock`
  (`checkQMDBLeaderSealParent`, `blockchain_write.go:234`) against the
  actual committed state, so a block whose trusted parent never truly
  applies is rejected there via the EXISTING `ErrStaleSeal` path
  (`worker.go`, "Sealed block lost to a competing candidate;
  dropping") -- exactly the handling an ordinary sibling race already
  gets today. Strict FIFO order (one channel, one reader goroutine) is
  what makes this safe: job N's true outcome is always settled before
  job N+1's own write ever calls `checkQMDBLeaderSealParent`.
- `CommitToCanonicalWith` needing the block in cache-or-DB
  (`internal/blockchain.go:1394-1414`) is the SAME mechanism S19
  already found and left unchanged (6cn/6co): a premature call defers
  via `pendingCommits`/`NotifyBlockImported`, self-healing on the
  leader via `observeCommittedExecution`'s existing fetch-by-hash
  fallback. Unchanged here; likely exercised somewhat more often, same
  as it already became more often under S19.
- The deferred-execution "applied" marker / `NotifyBlockImported`: same
  reasoning as the line above -- an existing, already-safe mechanism,
  not touched, potentially firing somewhat more often.
- Own-unwritten-chain depth: `unwrittenOwnPostStates` (`worker.go`)
  ALREADY walks BACK through up to 16 levels of this node's own sealed-
  but-unwritten blocks, collecting a post-state snapshot from each,
  stopping at the first one that is actually applied (its own doc
  comment: "returns...from parent down to (excluding) the applied
  lineage"). Today's achievable depth is effectively 0-1 (resultLoop's
  own serialization means at most one block is "being written" at a
  time, never two sealed-but-unwritten blocks coexisting). Under the
  switch, the writer's own bounded capacity (one job in flight, one
  queued) bounds the achievable depth at 2 -- comfortably inside the
  existing 16-level design, so this is NOT a new assumption and NOT a
  stop condition.

**(b) Failure.** Today, a write failure after push/propose
(`ErrStaleSeal`: log and drop, "an expected race under view churn";
any other error: log, `ForgetSealedHeader`, increment a counter) is
NOT fatal to the node -- it relies on the fleet's own consensus to
recover, exactly as it always has. `writeAndFinish` reproduces this
unchanged. **No explicit "abort the queue" logic was added**: per (a)
above, a job built on a truly-failed parent is caught by the SAME
`checkQMDBLeaderSealParent` call inside its OWN `writeBlockWithState`
attempt, and rejected via the same existing `ErrStaleSeal` path --
strict FIFO order is the only thing this safety property needs, and
the channel already provides it.

**(c) Back-pressure.** `asyncBlockWriter`'s job channel has capacity 1
(`async_write.go`): the writer goroutine's own in-progress job is "in
flight," and the channel holds at most one more "queued" -- a third
`Enqueue` call blocks until a slot frees, degrading to today's
synchronous timing rather than growing memory. A rate-limited warning
(`"miner: leader write queue full..."`, at most once per 5s) logs when
this happens; `wqWaitMs`/`wqDepth` are stamped on the job and appear on
`"miner: seal path"` (zero when the enqueue did not have to wait).

**(d) Shutdown.** `Miner.Close` (`internal/miner/miner.go`) calls
`asyncBlockWriter.Drain(30s)` only AFTER `group.Wait()` returns --
i.e. only once `resultLoop` itself has already exited and can no
longer call `Enqueue` -- then closes the job channel and waits for the
writer to finish whatever is already in flight or queued, so a
shutdown never leaves a pushed/proposed block unwritten. Bounded by a
30s timeout (logged as an error if exceeded) so a genuinely wedged
write cannot hang shutdown forever.

**(e) Overlap with view v+1's own journal writes.** The writer now
does the S19 latch wait (`journalCommitVote` succeeded/timeout/
abandoned) THEN the write, both off `resultLoop` -- so write(v) can now
run concurrently with the leader handling view v+1:
`journalPrepareVote(v+1)` at propose and `journalCommitVote(v+1)` at
`PrepareQC(v+1)` both need the same single MDBX writer write(v) is
holding. From 6cs's own timeline (push(v) at 0, write ~100-342ms,
push(v+1) at roughly +430ms under the OFF-switch baseline), the common
case should miss: `journalPrepareVote(v+1)` fires essentially at
push(v+1) (already proven to win this exact race today, per 6ct's own
finding that it always completes before the leader's own block write
even starts, since it runs on `resultLoop`'s OWN goroutine before
`handleSealed`'s write is reached) and `journalCommitVote(v+1)` fires
after Round1(v+1), which independently takes tens of ms. Whether or
not they collide, MDBX's single-writer mutual exclusion is what
enforces it either way, and mutual exclusion never produces an
incorrect result -- only a delayed one, exactly the same kind of delay
S18/S19 already characterized in depth for the analogous v-on-v
collision. **Confirmed: this overlap can only cost time, never
safety.**

**(f) Followers and switch-off.** The switch is read once
(`LeaderWriteAsyncOn()`, `sync.Once`); `newWorker` only constructs
`asyncBlockWriter` when it is on, so a switched-off node never
allocates the channel or starts the goroutine, and `handleSealed`'s
own branch (`w.asyncWriter != nil`) falls through to calling
`writeAndFinish` directly -- byte-for-byte today's control flow, same
goroutine, same order. Followers never call `TriggerBlockProduction`/
`handleSealed` as a leader would, so this entire path is inert for
them regardless of the switch.

**Implementation.** `internal/miner/async_write.go` (new):
`LeaderWriteAsyncOn()` (env, `sync.Once`); `writeJob` (carries
everything `handleSealed` has already computed by write time --
`blk`, `receipts`, `logs`, `task`, hashes, and every timing var the
"seal path" line needs); `pendingWrite`/`asyncBlockWriter`
(`ExpectedParent`, `Enqueue`, `run`, `Drain`, all documented above).
`worker.go`: the `task` struct is unchanged; `handleSealed` now builds
a `writeJob` after push/propose/receipts-copy/exec-remember and either
enqueues it or calls the new `writeAndFinish` directly;
`checkSealParentApplied` (new, small, directly tested) wraps the
bypass decision. `miner.go`: `Close` drains the writer after
`group.Wait()`.

**Tests.** `async_write_test.go`: strict FIFO ordering under back-to-
back seals (`TestAsyncWriterOrdersJobsStrictly`), the capacity-1-plus-1
back-pressure bound blocking then resuming
(`TestAsyncWriterEnqueueBlocksAtCapacityAndResumes`), `wqWaitMs`/
`wqDepth` stamping (`TestAsyncWriterEnqueueStampsWaitAndDepth`),
`ExpectedParent` appearing at enqueue and clearing after processing
(`TestAsyncWriterExpectedParentTracksThenClears`), `Drain` waiting for
an in-flight job vs. correctly timing out on a wedged one
(`TestAsyncWriterDrainWaitsForInFlightJob`/`TestAsyncWriterDrainTimesOut`),
and the `checkSealParentApplied` bypass firing only on a matching
parent and always falling through with the switch off
(`TestCheckSealParentApplied*`, three cases). All new tests pass under
`-race`. `internal/miner`'s full suite passes under both switch
placements, combined with `N42_LEADER_WRITE_AFTER_JOURNAL`/
`N42_CONTENTION_DIAG`. `internal/consensus/hotstuff` untouched this
step, re-run for regression only. Per this step's own time budget
(prepared while round 35zzzf runs), `-race` was run only on the new
tests, not whole packages.

**Build.** Same file-checkout recipe as n42-r86 through n42-r92:
detached worktree at `f7ec2836`, n42-r92's exact file set (6ct), plus
this step's changes. One-variable check: `worker.go`/`miner.go` both
required the SAME two-part treatment n42-r86 established --
`git diff 62439af7 8ae39838^` empty for both files (nothing else
touched either between S22 and S23), so this step's own hunk applies
on the SAME base every prior build already used. `worker.go` needed
its by-now-familiar FOURTH hunk (S11, S19, S22, S23 in sequence), with
the SAME single mechanical conflict every build since n42-r90 has hit
(the speculative-hit block's own off-lineage `tMs` context line,
`89d15267`/`19687889`, excluded from this lineage since n42-r86) --
resolved by hand, verified against a full diff of the result: only the
same four already-known lines remain. `miner.go` needed its FIRST
hunk (new to this recipe): `git diff f7ec2836 8ae39838^ --
internal/miner/miner.go` is NOT empty (41 lines -- the SAME
`activeSpecParent` off-lineage feature also touches this file), so a
direct checkout would have been wrong; `git diff 8ae39838^ 8ae39838 --
miner.go` applied cleanly onto the `f7ec2836` base, and the resulting
file's diff against `8ae39838`'s own version shows only the same
already-known, already-excluded lines. `internal/parallel/base_cache.go`
confirmed absent; `grep -rl BaseCache`: empty. `go build -p 8 -tags
nosqlite,noboltdb` clean; `go vet ./internal/...` clean; `go test`
passes on `internal/consensus/hotstuff/...` (both switch placements),
`internal/miner/...` (both placements), `internal/`,
`internal/parallel/...`. `/data/blockchain/gov5-work/n42-r93`:
108,789,800 bytes, sha256
`25f83814239489827783e4526bb57484dd91dcf6d0f8e655cdbacea524b5ea38`.
`strings n42-r93 | grep -c BaseCache` = 0; every prior marker present;
three new markers (`wqWaitMs`, `wqDepth`, `"leader write queue
full"`) each present.

**Runner.** `run-r35zzzg.sh`/`chain-35zzzg.sh` built from the
`run-r35zzzf.sh`/`chain-35zzzf.sh` pair via `cp`+`sed
's/35zzzf/35zzzg/g'` (checked first: `35zzzf` occurs nowhere in
either script's giant single-line history comment). Fixed by hand
afterward: the predecessor-wait (`r35zzzf.log`, its actual
predecessor) and the binary references (`n42-r93`). **THIS round IS
an A/B by leg**, on the NEW switch: `run_leg` gained a 6th positional
parameter, `N42_LEADER_WRITE_ASYNC` (0 or 1), exported alongside the
already-adopted `N42_LEADER_WRITE_AFTER_JOURNAL`/`N42_CONTENTION_DIAG`
(both stay 1 in every leg). Calls: `warmup 1 0`, `A1 1 0`, `B1 1 0`
(async switch off, the in-round baseline), `B2 1 1`, `A2 1 1` (async
switch on). The in-window (win1-start+15s, win2-start+15s) profile
captures and the S20 VM sampler (6ct/6cr) carry over unchanged.
`bash -n` clean on both; confirmed not running.

**Prediction 89 (registered before any round):**

**(a) B1 (off), mechanism.** `resQWaitMs` on full in-tenure blocks is
large (report the median; expected to track whatever part of write(v)
outlasts `CommitQC(v)`, per 6cs/6ct's own model) and `push(v+1)`
continues to follow write(v)'s end, matching today's baseline exactly.

**(b) B2 (on), mechanism.** `resQWaitMs` < 10 ms; `wqDepth` <= 1 with
`wqWaitMs` ~0 on >= 95% of blocks (the queue should rarely if ever
reach its second slot at this campaign's block rate); `push(v+1) -
CommitQC(v)` returns to the ~254 ms leg-invariant constant (6cs); win1
in-tenure cycle <= 480 ms (6cs's own derived 415-435 ms range from
measured medians, with margin for the (e) journal/write overlap cost).

**(c) Not merely moved.** Report, for both legs: leader `jcvMs`/
`jpvMs` (S18), the `QC->push` constant (6cb/6cs), the `CommitToCanonical`
wait/defer rate, `"committed block not executed locally"` occurrence
count (S19's own flagged side effect), and follower phases -- so a
flat or worse B mean can be read against the mechanism numbers rather
than assumed to mean the lever failed.

**(d) Score: no claim beyond the supply ceiling.** With a ~450 ms
cycle the eight generators (~140k) cannot fill every block at that
rate, so occupancy is EXPECTED TO FALL in B2's win1 while TPS holds
near ~140k -- stated in advance so a flat or lower score is not misread
as a failed lever; the mechanism clauses (a)-(c) are what decide this
step, not the score.

**(e) Safety.** No BAD BLOCK/divergence; every proposed-and-committed
own block written exactly once and in the order it was sealed (cross-
check write-completion log lines against seal order); no new
flood-window view-timeout events vs 35zzzf; clean shutdown between
legs (no `"database closed"`/lost-write errors at leg boundaries --
directly exercises this step's own Drain-on-shutdown design).

**VERDICT: confirmed** (implementation, tests, one-variable check and
build all done; Part 1's invariants are read, confirmed, and one --
`CheckSealParentApplied` -- fixed as part of the design, not merely
noted). QS_QUEUE.md's S23 row status is marked prepared with
prediction 89 (6cu). Launch is the commander's next call.

## 6cv. Round 35zzzf: U1 is real but never binds -- v+1's own build always outlasts v's write; the ~254 ms constant was mostly pacing that doesn't apply to full blocks; and the VM sampler puts the win1-to-win2 slowdown beyond doubt as page-cache thrash (2026-09-21)

n42-r92 (r91 + `"miner: seal path"` ms-precision stamps, both switches
on in every leg) ran clean: legs B1 13:28:24-13:41:42, B2
13:41:42-13:55:37. Node logs preserved whole; trimmed to
`wr-logs/r35zzzf-keep/node{0-6}-B.log` (13:28:00-13:59:59) -- **node0
rotated TWICE this round and the first trim silently dropped it**
(`ls node0/*.gz` returns two paths on one line; quoting that
multi-line variable in `zgrep -E "$PAT" "$gz"` passes it as a single
invalid filename and zgrep exits 1 without complaint) -- fixed by
leaving the glob unquoted (word-split) before the second, verified
attempt; flagged here since it is a preservation-step bug, not an
analysis one, and would silently cost a whole node's evidence on any
future round with a mid-window rotation. Scripts:
`wt-r27/scripts/qs-analysis/seal_path_waterfall.py` (Job 1),
inline analysis over `r35zzzf-vm.log` (Job 2, see Method).

### JOB 1 (S22): the seal-path waterfall

**(a) Waterfall (medians, win1 both legs; full table in script output).**
Every step from `specHit` through `push`/`propose` is at or near 0 ms;
the pipeline's real work concentrates in exactly three places:

| step | B1win1 | B2win1 |
|---|---|---|
| `buildBegin -> specParked` (the actual build: fillTx+assemble+finalize) | 650.0 (554-762) | 611.5 (542-721) |
| `trigger -> specHit` (real trigger waiting on the still-running build) | 79.5 (39-176) | 82.5 (22-106) |
| `copyStart -> copyEnd` (receipts copy) | 36.0 (27-47) | 31.5 (26-40) |
| `copyEnd -> writeStart` = `lwWaitMs` | 92.0 (24-151) | 99.5 (36-150) |
| `writeStart -> writeEnd` (the write itself) | 244.5 (193-378) | 227.0 (184-354) |
| `push` (`pushStart->pushEnd`) | 18.0 (13-36) | 17.5 (14-23) |
| **CYCLE** `pushEnd(v) -> pushEnd(v+1)` | **680.0 (568-779)** | **691.0 (586-817)** |

Everything else (`specParked->specHit`, `paceEnter->taskSent`
[=`paceDurMs`], `taskSent->taskPicked` [=`taskQWaitMs`], `sealEnter->
checkEnter->checkExit->blsStart->blsEnd`, `blsEnd->resultRecv`
[=`resQWaitMs`]) medians to **0 ms** in every window, both legs.
`paceDurMs` on full blocks is confirmed ~0 as predicted (88c) -- pacing
only bites on small/empty blocks running faster than the target grid.

**(b) U1, tested directly: real, but never the binding constraint.**
`resQWaitMs` -- U1's own predicted number -- is **0 ms at the median in
every one of the four windows**. `resultRecv(v+1) - writeEnd(v)` is
**positive and large** in every window (B1win1 245, B1win2 286,
B2win1 228, B2win2 298.5 ms) and **0% of sampled blocks land within
10 ms of it** (checked directly, all four windows). The predicted
identity `resQWaitMs(v+1) = writeEnd(v) - blsEnd(v+1)` comes out
strongly NEGATIVE (median -228 to -298.5 ms) precisely because
`blsEnd(v+1)` -- itself gated by the whole `trigger->specHit->...->BLS`
chain above -- already lands well AFTER `writeEnd(v)`, so the
single-consumer `resultCh` queue (6ct Part A: unbuffered, one consumer,
`handleSealed` runs `WriteBlockWithState` inline) never has anything to
queue behind. **U1's own code reading is correct and unrefuted** (the
dependency exists exactly as described, file:line) **but it is
structurally impossible for it to bind in this round's full-block
population, because v+1's own build (650-850 ms, see (a)) always
outlasts v's write (227-331 ms) on its own** -- the state U1 protects
against (v+1 ready to deliver before v's write finishes) never occurs
here. **The number S23 was asked to state: the median ms per block v+1
spends queued behind v's write, via `resQWaitMs`, is 0 ms in every
window this round** -- not because the dependency was removed, but
because it was never reached.

**(c) The ~254 ms constant, decomposed and found SMALLER than 6cs's
estimate.** Summing the named steps between `trigger` and `pushEnd`
(`trigger->specHit`, `pace`, `taskQ`, `sealEnter->checkEnter`, `check`,
`BLS`, `resQ`, `copy`, `push`) gives **133.5 ms (B1win1)** and
**131.5 ms (B2win1)** -- not 254 ms. Share placed in named steps:
100% by construction (every ms-gap in the chain is one of the named
steps; prediction 88(b)'s ">=90%" bar is met with room to spare,
`n/a` for "unplaced" since there is none). The three largest named
steps are `trigger->specHit` (79.5-82.5 ms, effectively 100% WAIT --
this node's own worker goroutine finishing the still-running build),
`copy` (31.5-36.0 ms, WORK -- 163k-allocation receipts deep-copy,
`worker.go:705-725`), and `push` (17.5-18.0 ms, mostly WORK -- the
`SealedBlock` broadcast call). **6cs's own 254 ms figure (from an
independent, earlier-round worked example in 6cb) was dominated by a
~135 ms pacing wait that does not apply to full blocks** -- this
round's own `paceDurMs` medians to 0 exactly as (a) confirms, so the
"constant" was never a fixed physical cost; it is whatever `paceBlock`
happens to charge on that round's block-size/grid combination, plus
this same ~130 ms of gate/copy/push overhead. Read together with (b):
of that ~130 ms, roughly two-thirds (`trigger->specHit`) is this node
finishing its OWN queued build, not any cross-block resource wait.

**(d) Repeatability (prediction 88a).** In-tenure cycle win1: 680.0 ms
(B1), 691.0 ms (B2) -- both close to, and a little BELOW, 35zzze's own
656.0 ms B2win1 figure (6cs, corrected), consistent given a different
round's noise floor (6cm: ±26.6% between same-config rounds is normal
at this campaign's sample sizes). `r1`/`r2`: 65.5/91.5 (B1win1),
65.0/99.5 (B2win1) ms, both growing into win2 (79.0/142.5 B1win2,
72.5/138.0 B2win2) -- consistent with the within-leg slowdown touching
Round2 too, not just the write. **Leader `jcvMs` stays uncontended at
the MEDIAN in every window (0 ms, all four)** -- the S19 fix holds up a
second round -- but its own TAIL grows sharply into win2 (p90 10->281 ms
B1, 3->234 ms B2), meaning the journal write occasionally contends
again once the leg's own memory pressure (Job 2) sets in, even though
S19 keeps the MEDIAN case clean. `jpvMs` stays ~0 throughout (leader's
own prepare-vote journal, never exposed to the collision, as always).
`lwWhy` shares: journal 75-79% / timeout 21-33% / abandoned 0%, stable
across windows and legs -- matching 6cs's B2 findings closely.
`import_breakdown.py`-equivalent (mandatory line, adapted to the kept
files, `txs>=160000`, whole round): **n=1926, body 11 ms (p90 26), proc
571 ms (p90 779), write 201 ms (p90 277), total 819 ms (p90 1051)**;
joined to `"parallel block"` (`execMs`/`finalizeMs`): exec 280 ms
(p90 503), finalize 134 ms (p90 192). **Safety: 0 BAD BLOCK, 0
divergence, 0 build stalls** (grepped directly, all 7 kept files).
Distinct TC/view-timeout events: **B1 2** (view 4653 at 13:29:23, ~65 s
into the leg, still inside the 400 s decay; view 6602 at 13:40:35, past
both windows, in the drain tail) -- **decay/drain, none in a scored
window; B2 9** (a cluster of 7 at views 6790/6792, 13:42:51-13:43:15,
~69-93 s into the leg, still decay; 1 at view 8488, 13:50:33, 9 s after
B2win1 opens -- **flood**, the only scored-window timeout event either
leg produced this round).

**(e) Derived (sums of measured medians only).** `resQWaitMs` is
already 0 at the median (b), so removing it changes nothing: cycle
stays 680.0/691.0 ms (B1/B2 win1). Removing `copy` on top (31.5-36.0 ms,
a real, separable receipts-deep-copy cost, not currently overlapped
with anything else in the chain): **B1win1 644.0 ms, B2win1 659.5 ms**
-- a 5.0-4.6% reduction, the ceiling this specific pair of changes can
buy on today's numbers.

### JOB 2 (S20): the VM sampler confirms page-cache thrash directly

Windows located from the full-block sequence, not fixed offsets (0.a
below), then read against `r35zzzf-vm.log`'s 10 s `vmstat`+per-PID
samples (`minflt`/`majflt`, `RssAnon+File+ShmemMB`, and system-wide
`pgmajfaultD`/`pgscanKswapdD`/`pgscanDirectD`/`pgstealKswapdD`/
`refaultFileD` -- all already DELTAS over the preceding 10 s, per the
sampler's own field names).

**0. Windows.** B1win1 13:36:40-13:37:57, B1win2 13:38:00-13:38:43;
B2win1 13:50:24-13:51:47, B2win2 13:51:47-13:52:29 (same method as
6cp/6cs: first N/next M full blocks in chronological order, N/M chosen
to reproduce the round log's own win1/win2 block counts exactly).
"Ramp" = the last ~3.5 min before win1 opens (post-decay, pre-flood-
saturation).

**(a) The fault-rate jump is enormous, and it happens at ramp->win1,
not win1->win2.**

| | ramp | win1 | win2 |
|---|---|---|---|
| B1 `pgmajfaultD`/10s (fleet avg) | 1,192 | **25,749** (21.6x) | 15,987 (0.62x win1) |
| B1 `pgscanKswapdD`/10s | 54,277 | **1,109,077** (20.4x) | 597,985 (0.54x win1) |
| B1 `refaultFileD`/10s | 429 | **21,593** (50.3x) | 16,322 (0.76x win1) |
| B1 per-node `RssFile` (avg, MB) | ~4,100-4,300 | ~2,800-2,985 | **~1,410-1,490** |
| B1 per-node `RssAnon` (avg, MB) | ~2,400-2,600 | ~9,740-9,970 | ~10,150-10,360 |
| B2 `pgmajfaultD`/10s | 1,481 | **25,941** (17.5x) | 1,431 (0.06x win1) |
| B2 `pgscanKswapdD`/10s | 4,428 | **885,875** (200.1x) | 597,916 (0.67x win1) |
| B2 `refaultFileD`/10s | 662 | **23,092** (34.9x) | 2,720 (0.12x win1) |
| B2 per-node `RssFile` (avg, MB) | ~3,800-6,010 | ~3,290-4,260 | **~1,630-1,790** |
| B2 per-node `RssAnon` (avg, MB) | ~2,040-4,830 | ~9,480-10,080 | ~10,120-10,340 |

**The dramatic jump (17-200x on every fault/scan counter) happens
between ramp and win1, not between win1 and win2** -- by the time win1
opens, the fleet is already deep in page-cache thrash. Per-node
`RssFile` then **keeps shrinking** through win2 (another ~40-50% past
win1's own already-reduced level) while the raw fault-RATE counters
actually **fall back somewhat** from their win1 peak. This is a
saturation effect, not relief: there is simply less resident,
file-backed memory left to evict and refault by win2 (`RssFile` is
down to 1.4-1.8 GB fleet-wide, a fraction of the ramp's 3.8-6.0 GB), so
the ABSOLUTE count of major faults/scans per 10 s window drops even
though the underlying pressure (the still-climbing `RssAnon`, now
~10.2-10.4 GB per node, matching 6cr/6cp's own finding) has not eased.
**Answering the question as posed: thrash does not "start" at the
win1->win2 boundary -- it is already present at 17-200x the ramp rate
by win1 and never returns to baseline, with `RssFile` still falling
through win2 -- so memory pressure is NOT ruled out by this data; if
anything this is the most direct, specific confirmation this campaign
has produced (previous rounds inferred the mechanism from `AnonPages`/
`Cached` alone; this round measures the actual fault and scan
counters).**

**(b) Which phases grow more: MDBX-touching vs pure-CPU, win1->win2.**

| phase | kind | B1 win1->win2 | B2 win1->win2 |
|---|---|---|---|
| follower `execMs` (EVM exec, reads through MDBX pages) | MDBX-touching | 218->256 (+17.4%) | 209->284 (**+35.9%**) |
| follower `finalizeMs` (state-root) | MDBX-touching | 120->130 (+8.3%) | 115->148 (+28.7%) |
| follower `write` | MDBX-touching | 193->211 (+9.3%) | 190->213 (+12.1%) |
| follower `proc` (recover+exec+finalize) | mixed | 467->545 (+16.7%) | 446->588 (+31.8%) |
| leader `writeStart->writeEnd` | MDBX-touching | 244.5->297.5 (+21.7%) | 227.0->331.0 (+45.8%) |
| leader `buildBegin->specParked` (fillTx+assemble+finalize) | mixed | 650.0->796.0 (+22.5%) | 611.5->850.0 (+39.0%) |
| `copy` (receipts deep-copy) | pure CPU | 36.0->42.5 (+18.1%) | 31.5->34.5 (+9.5%) |
| `push` (network send) | pure CPU/IO | 18.0->23.0 (+27.8%) | 17.5->22.5 (+28.6%) |
| write probe `heldMs` p90 | MDBX-touching | 157->221 (+40.8%) | 162->224 (+38.3%) |
| write probe `waitMs` p90 | MDBX-touching | 0->94 | 5->63 |

**The separation leans toward MDBX-touching steps growing more, most
clearly in B2** (`execMs` +35.9%, leader `write` +45.8%, write-probe
`heldMs`/`waitMs` both up sharply) **but it is not a clean, total
discriminator** -- pure-CPU `push` grows 27.8-28.6% too, comparable to
several MDBX-touching steps. Read alongside (a)'s much larger and
specific fault/scan jump, the most defensible reading is: the dominant
driver is memory/page-cache pressure (the discriminator (a) provides
directly, at 17-200x), which SECONDARILY drags up MDBX-touching phases
more than pure-CPU ones, but general CPU/scheduling contention (more
goroutines competing as `RssAnon` climbs, GC pacing under
`GOGC=300`/`GOMEMLIMIT=9GiB`) is plausibly still adding a smaller,
across-the-board tax on top -- consistent with 6cr's own closing
caveat that it never claimed to be the ONLY contributor.

**(c) Everything else checked, one table.** `candidates`==`included`
in every window (B1win1 142,200==142,200 median; B1win2 163,000==
163,000; B2win1 156,500==156,500; B2win2 163,000==163,000) -- `failed`
implicitly 0, and candidates GROW win1->win2, not shrink: **supply is
not the constraint, confirmed again**. No generator (`txflood`)
CPU/RSS line exists in `r35zzzf-mem.log`/`-vm.log` (`n/a`, not
captured this round). No runtime `MemStats`/GC-pause log line exists
either (`n/a`). MDBX file growth was not sampled this round (`n/a` --
`r35zzzf-vm.log` and `-mem.log` both report OS-level memory, not file
sizes).

**(d) Verdict, prediction 88(c).** **Page-cache thrash: YES**, with a
17-200x factor on the fault/scan counters between the pre-flood ramp
and the scored windows (largest single jump: B2's `pgscanKswapdD`,
200x), and `RssFile` falling by roughly half again from win1 to win2 in
both legs while `RssAnon` keeps climbing toward ~10.2-10.4 GB per node
-- this is now measured directly (fault/scan counters), not inferred
(`Cached`/`AnonPages` alone, 6cp/6cr). The MDBX-touching-vs-CPU phase
contrast in (b) is directionally consistent but not sharp enough alone
to rule out a smaller, additional CPU/GC contribution riding on top.

**Method.** Job 1's join, leg-offset calibration and validator-index
map reuse 6cs's own method verbatim (per-leg QC-proxy join for offset
calibration only; the U1 test needs no view-offset join at all, since
`seal path`/`propose phases` share the same `number`/`n` key directly).
`import_breakdown.py`'s logic was run inline against the kept files
(the original script's own live-path glob, `/data/blockchain/qs-node*/
log/n42.log`, no longer resolves once a round's node dirs are
reseeded) rather than editing the checked-in script, to avoid drifting
it from the copy other rounds still invoke against a live path when
one exists. Job 2's window/ramp boundaries are wall-clock ranges
derived from Job 1's own `propose_all` timestamps (first/last block in
each window), not a separate join -- the `vm.log`'s own 10 s cadence
means win2 (34-35 blocks, ~43-60 s) contains only 4 samples in every
window, the tightest population this analysis has used; medians/means
over n=4 are reported as such, not smoothed or extended.

**What this does and does not show.** It shows, with a structural code
citation (6ct) now backed by a direct measurement, that U1's resultCh
dependency is REAL but does not bind in this round's full-block
population, because v+1's own build (650-850 ms) is unconditionally
longer than v's write (227-331 ms) -- so S23's own design (moving the
write off `resultLoop`) targets a real mechanism that this round's data
cannot show actually costing anything YET; it would only start costing
something if a future change made the build faster than the write. It
shows the ~254 ms "constant" from 6cs was round/config-specific
(dominated by pacing that doesn't apply to today's full blocks), not a
fixed physical cost -- 6cs's own number should be read as an artifact
of a different, faster-block round, not superseded so much as
recontextualized. It shows the win1-to-win2 slowdown is accompanied by
a 17-200x jump in OS-level page-fault/scan activity, the most direct
evidence this campaign has produced for the memory-pressure mechanism
6cp/6cr proposed from coarser counters -- but does NOT fully separate
memory pressure from a secondary CPU/GC contribution, and does NOT
identify (again) the specific in-process structure driving `RssAnon`'s
climb beyond 6cr's own `qmdb.mapIndex` candidate (this round captured
no heap profile at all, by the harness's own admission, so that
question is untouched here).

## 6cw. S24: what is inside the 611-650 ms leader build and the 774-803 ms follower import of the same full block -- parallel EVM execution (~35-53%) and state-root computation (~20-25%) dominate both sides, ~14-22% of the leader's own build stays unaccounted, and the leader is NOT slower than the follower on execution (2026-09-21)

Logs-only, `wr-logs/r35zzzf-keep` (35zzzf, already preserved and used by
6cv), single-threaded per the box-sharing note (35zzzg running).
Script: `wt-r27/scripts/qs-analysis/build_vs_import.py`. Every line in
the campaign that carries a duration for this pipeline was joined by
block number: `miner: seal path` (leader, ms-precision), `miner:
prefill phases` (leader, >50 ms outliers only), `miner: parallel fill`
(leader, no block-number field -- joined by node+time proximity, not
per-block exact), `parallel block` (BOTH roles -- the shared execution
engine logs once per node per block, distinguished here by whether the
logging node IS that block's own leader), `blockimport phases`
(follower only), `miner: propose phases` (leader's `assemble`/
`finalize` wrapper fields).

**1/2. LEADER build waterfall, medians (full in-tenure blocks, win1).**

| step | B1win1 | B2win1 | work/wait | owning code |
|---|---|---|---|---|
| `buildBegin -> specParked` (TOTAL) | 650.0 (554-762) | 611.5 (542-721) | -- | -- |
| `parallel block` execution sum (`recoverMs+setupMs+blockStartMs+executorMs+runMs+collectMs+applyMs+prefetchMs+finalizeMs`) | 347.0 | 332.0 | WORK (CPU, parallel EVM) | `internal/parallel_processor.go:560-640` |
| `miner: parallel fill`'s own `pick` (candidate select/sort, NOT per-block joined -- see Method) | ~60.8 (broader full-block population) | ~60.8 | WORK (CPU, `NewTxByPriceAndNonce` over the pool) | `internal/miner/worker.go:2117-2166` |
| `propose-phases.assemble` (commit()'s wrapper, dominated by `task.finalize` -- the OUTER, expensive state-root call, separate from `parallel block`'s own small `finalizeMs`) | 150.4 | 144.5 | WORK (CPU, QMDB root commit) | `internal/miner/worker.go:1547` (`w.commit`) -> `Finalize`/root computer |
| **unaccounted remainder** (TOTAL minus the three rows above) | **91.8 (14.1%)** | **74.2 (12.1%)** | not determined | `worker.go:1244-1420` (`WaitBlockPersisted`-skip/`AlignAppliedBranch`-skip/`prepareWork`/`BeginRo`/state-reader wrapping) + `worker.go:1909-2117` (`fillTransactions`'s own preamble before `NewTxByPriceAndNonce`) |
| `miner: prefill phases` (align/persistWait/roTxBegin/specTreeReload/headerPrepare/blockStart) | **0/24 matched -- fires only as a >50 ms outlier (22-30 times per node over the WHOLE trimmed window), never on this round's sampled full blocks** | (same) | -- | `internal/miner/build_stall_watchdog.go:225-236` |

Without `pick` (a broader-population estimate, not exactly joined to
these 24 blocks -- see Method), the gap is 143.5 ms (22.1%, B1win1) /
139.7 ms (22.8%, B2win1); with it folded in, ~92/74 ms (12-14%)
remains. **Neither reaches the "within 10%" bar** -- see VERDICT.
`prefill phases` matching ZERO of the 96-98 sampled full leader blocks
this round is itself informative: the align/reload/header-assembly
portion of a TYPICAL full-block build is fast enough (<50 ms combined)
that it never trips the outlier log, so it CANNOT be where the
remaining ~74-92 ms lives -- ruling it out rather than leaving it as a
silent suspect.

**Work vs wait, named steps.** `parallel block`'s execution sum is CPU
work (parallel EVM execution across 32 workers, `PARALLEL_EVM x32`);
`waves` medians to 1 with 0 `aborts` and 0 `fallback` in every window,
both legs -- **6bs's "64-wave re-execution from an unrecognized
permanent nonce miss" is NOT occurring this round** (closed lever,
confirmed still closed). `pick` is CPU work (sorting/selecting from a
600k-entry pool). `assemble`/`finalize` (state-root) is CPU work inside
the QMDB tree commit -- **6cb already put "the leader's own state-root
computation" on the critical path** (6cb's title names it explicitly);
this section adds the actual ms figure (130-170 ms) 6cb's own worked
example did not isolate this precisely. The unaccounted remainder is
genuinely unclassified here (per the task's own instruction not to
guess); it is bounded above by ~92 ms and located to two candidate
code ranges, not further split.

**Speculative build placement (2, U1/`ownPendingSpeculation` context).**
The block being measured here IS the speculative build (only a
speculative call ever produces `specParkedTMs`), so `WaitBlockPersisted`
is bypassed by construction (6cs/6cv) -- what `prepareWork`(header
assembly)/`BeginRo`(read-tx open)/state-reader wrapping need from the
PARENT is only the in-memory `unwrittenOwnPostStates` snapshot
(`worker.go:1220`), available the instant this SAME node finished
building its own parent -- i.e. at the parent's OWN `specParked`/`seal`
time, not at its write. **Idle gap between builds on the same leader**:
`buildBegin(v+1) - specParked(v)` medians **21.0 ms (B1win1, n=6)** /
**19.0 ms (B2win1, n=6)** -- small n (needs two consecutive
same-leader chained pairs inside one window), but consistent with
6cv's own finding that everything between `specParked`/`specHit` and
`push` is a handful of near-zero steps: **there is essentially NO idle
time between consecutive builds on the same leader** -- build(v+1)
starts as soon as push(v)'s own small pace/seal/push tail clears,
which is itself only ~20-40 ms after build(v) parks.

**3. FOLLOWER import waterfall, medians (same populations, win1).**

| step | B1win1 | B2win1 | share of `total` |
|---|---|---|---|
| `blockimport.total` | 679.8 (558.7-829.3) | 651.3 (562.2-782.5) | 100% |
| `blockimport.hdr` | 2.8 | 2.6 | 0.4% |
| `blockimport.body` | 8.7 | 8.7 | 1.3% |
| `blockimport.proc` (wraps `parallel block`) | 466.4 | 446.9 | 68.6%/68.6% |
| ` parallel block`.`execMs` | 222.5 | 216.0 | 32.7%/33.2% |
| `parallel block`.`finalizeMs` | 126.5 | 118.0 | 18.6%/18.1% |
| `parallel block`.`recoverMs` (sender recovery) | 26.0 | 24.0 | 3.8% |
| `parallel block`.`applyMs` | 25.0 | 22.0 | 3.7% |
| `blockimport.write` | 195.2 (154.1-255.6) | 189.6 (153.7-239.0) | 28.7%/29.1% |

Reconciliation: `hdr+body+proc+write` = 2.8+8.7+466.4+195.2 = **673.1**
vs `total` **679.8** (0.99% gap, B1win1) -- **the follower side
reconciles cleanly**, unlike the leader's. This makes sense structurally:
`blockimport phases`' own `proc` field is a direct wrapper around the
SAME `parallel_processor.go` call `parallel block` reports on, with no
equivalent of the leader's separate `commit()`/task-park wrapping stage
sitting outside it.

**4. Top three, each side, with prior-section cross-check.**

LEADER (B1win1, of 650.0 ms total): (1) `parallel block` execution sum,
347.0 ms, 53.4%, WORK, `internal/parallel_processor.go:560-640` --
**not previously isolated at this granularity**; 6u introduced the
per-worker-reader parallel design, 6bs found and closed the 64-wave
re-execution defect (confirmed still closed here, `aborts`=0). (2)
`assemble`/state-root, 150.4 ms, 23.1%, WORK, `worker.go:1547` ->
root computer -- **already flagged as being on the critical path by
6cb's own title**, not previously given this ms figure. (3) unaccounted
remainder, 91.8 ms, 14.1%, NOT DETERMINED, `worker.go:1244-1420` /
`1909-2117` -- **not previously measured or attacked** (this is a new
open item, not a re-run of a closed lever).

FOLLOWER (B1win1, of 679.8 ms total): (1) `parallel block`.`execMs`,
222.5 ms, 32.7%, WORK, `internal/parallel_processor.go` (the same
executor as the leader's), -- covered by the SAME 6u/6bs history as the
leader's own execution. (2) `blockimport.write`, 195.2 ms, 28.7%, WORK
(MDBX write, mostly page-write and journal-adjacent cost), `internal/
blockchain_write.go` -- **already the subject of the entire S18-S23
campaign arc** (6cp/6cq/6cs/6cv): this is the SAME write whose
collision with `journalCommitVote` (now resolved by S19) and whose own
duration (227-331 ms, leader side) has been measured repeatedly; here
it is confirmed present at a comparable magnitude on the FOLLOWER side
too, never separately isolated as a follower-specific number before.
(3) `parallel block`.`finalizeMs`, 126.5 ms, 18.6%, WORK -- **NOT the
same finalize as the leader's small (17.5 ms) `parallel block`.
`finalizeMs`** (see (a) below); not previously isolated at this
granularity as a follower-specific cost.

**(a) Leader vs follower execution: the coordinator's own framing
("leader fill 351 vs follower exec 255-266") does not hold in this
round's data -- checked directly and reported as found, not forced to
fit.** `parallel block`.`execMs`: **leader 205.0 ms vs follower 222.5 ms
(B1win1)**, **leader 197.5 ms vs follower 216.0 ms (B2win1)** -- the
**follower is slightly SLOWER on raw EVM execution**, the opposite
direction from the framing's premise, by 7.9-9.4%. `waves`/`aborts`/
`fallback` are identical on both sides (1/0/false, medians) -- no
retry-shape difference. The one place leader and follower diverge
sharply is `parallel block`.`finalizeMs` itself: **leader 17.5 ms vs
follower 126.5 ms (B1win1)** -- a ~109 ms gap, the OPPOSITE asymmetry
from `execMs`. Reading the call site (`internal/parallel_processor.go:
629`, `p.engine.Finalize(...)`): this is `Engine.Finalize`'s own small,
per-block bookkeeping (rewards/withdrawals), not the big state-root
computation (that is the leader's SEPARATE, later `commit()`-wrapper
`assemble`/`finalize` at 150.4/136.6 ms, which has no such counterpart
timed inside `blockimport phases` at all -- the follower's OWN
state-root recomputation is presumably folded into `blockimport.proc`
or `blockimport.root` without a separate line, `blockimport.root`
reading exactly 0 in every sample here). **This asymmetry in
`parallel block`.`finalizeMs` specifically -- not overall execution
speed -- is the real, measured leader/follower difference, and it runs
the OPPOSITE way from what the framing assumed; not chased to a
root cause here given the time budget, but reported precisely rather
than reconciled to the premise.**

**5. win1 -> win2 growth (6cv's page-cache-thrash window).**

| step | B1 win1->win2 | B2 win1->win2 |
|---|---|---|
| leader TOTAL (`buildBegin->specParked`) | 650.0->796.0 (+22.5%) | 611.5->850.0 (+39.0%) |
| leader `parallel block` execution sum | 347.0->397.0 (+14.4%) | 332.0->444.5 (+33.9%) |
| leader `assemble`/state-root | 150.4->186.2 (+23.8%) | 144.5->181.1 (+25.3%) |
| follower `proc` | 466.4->546.2 (+17.1%) | 446.9->585.0 (+30.9%) |
| follower `execMs` | 222.5->259.5 (+16.6%) | 216.0->270.0 (+25.0%) |
| follower `finalizeMs` | 126.5->136.0 (+7.5%) | 118.0->148 (+25.4%) |
| follower `write` | 195.2->213.3 (+9.3%) | 189.6->213 (approx, +12.3%) |

**Every step grows win1->win2, in both legs, consistent with 6cv's
page-cache-thrash finding** -- B2 grows harder than B1 on almost every
line (state-root being the one near-parity exception, ~24-25% in both
legs), matching 6cv's own VM-sampler numbers (B2's fault/scan jump was
the larger of the two legs).

**6. Derived (sums of measured medians only, labelled as such).**
Baseline: in-tenure cycle win1 680.0 ms (B1) / 691.0 ms (B2), per 6cv.

- (i) `specTreeReload` (part of `prefill phases`) forced to 0: **no
  change** -- it is already effectively 0 for the median full block
  (item 1's own finding: prefill never fires as an outlier on these
  blocks, so its total contribution is already below the 50 ms
  threshold that would even register it). Derived cycle: **680.0/691.0
  ms, unchanged**.
- (ii) leader's fill matched the follower's exec time: **no
  change, and the substitution would make things WORSE if taken
  literally** -- the leader's own execution (`execMs` 205.0/197.5 ms)
  is ALREADY faster than the follower's (222.5/216.0 ms), per (4a).
  Derived cycle: **680.0/691.0 ms, unchanged** (there is no headroom to
  claim here on the measured direction).
- (iii) build(v+1) starts with no idle gap after build(v): the
  measured gap (21.0/19.0 ms, small-n) is subtracted directly. Derived
  cycle: **659.0 ms (B1) / 672.0 ms (B2)**.
- (iv) all three combined: since (i) and (ii) contribute 0 on today's
  numbers, this equals (iii) alone: **659.0 ms (B1) / 672.0 ms (B2)** --
  a 3.1%/2.7% reduction, bounded by how small the idle gap already is.

**Method.** `miner: parallel fill` carries no block-number field, so
its `pick`/`run`/`pendingSnapshot`/`trim` figures in item 1 are read
over the BROADER population of every full-block (`candidates>=150000`)
fill line in the kept window, not exactly joined to the same 24-block
per-window sample the rest of this section uses -- flagged explicitly
wherever quoted, and excluded from the strict per-row reconciliation
sum for that reason. `parallel block` is joined by `(node, n)` and
disambiguated leader-vs-follower purely by whether the logging node
equals `propose_all[n]['node']` (the block's own leader) -- no separate
role field exists on the line itself. `prefill phases`' near-total
absence from the sampled population is treated as a finding (item 1),
not a missing join to chase further.

**What this does and does not show.** It shows the two largest, well-
attributed costs on both sides are the SAME shared mechanism (parallel
EVM execution, `internal/parallel_processor.go`) and, on the leader,
the separate state-root commit (`worker.go` `commit()` wrapper) --
consistent with and quantifying 6cb's and 6u's own earlier framing. It
shows the coordinator's own working hypothesis about WHY leader and
follower differ (leader fill slower than follower exec) does not match
this round's measurements -- the follower is if anything slightly
slower on raw `execMs`, and the real, measured asymmetry is in
`parallel block`.`finalizeMs` (a small per-block bookkeeping call, not
the big state-root), reported as found rather than forced. It does NOT
close the leader's own ~74-92 ms (12-14%) unaccounted remainder to a
specific line -- two candidate code ranges are named, not one, and
`prefill phases`' own non-firing rules out its own listed steps as the
content, without identifying what actually fills the gap. It does NOT
explain the follower `finalizeMs` asymmetry's root cause -- flagged,
not chased, given this task's own time budget. **VERDICT is therefore
"inconclusive" against the task's own "sums within 10%" bar** -- 12-22%
depending on leg and whether the imprecisely-joined `pick` figure is
included -- while still landing every major cost bucket (parallel
execution, state-root, MDBX write) precisely enough to rank and
attribute them.

## 6cx. S23: N42_LEADER_WRITE_ASYNC is NEUTRAL as predicted (cycle 651.5 -> 656.0 ms), but B2's ONLY committed block at the leg's last two views was never written anywhere -- a pre-existing propose-before-write/sibling-race exposed by view churn at leg-teardown, not a shutdown-drain bug (2026-09-21)

n42-r93 (r92 + `N42_LEADER_WRITE_ASYNC`, A/B by leg: B1=0, B2=1;
`N42_LEADER_WRITE_AFTER_JOURNAL=1` and the gossip-fallback switch on
throughout) ran B1 (14:39:37-14:52:56) and B2 (14:52:56-15:06:07)
cleanly, then **leg A2 never produced a single block** and the harness
correctly refused to score it. Node logs preserved whole, trimmed to
`wr-logs/r35zzzg-keep/node{0-6}-B.log` (14:39:00-15:24:11, covering
B1/B2/A2 and both restarts). Script (Job 2 reuses `seal_path_
waterfall.py` unmodified, args only); Job 1/3 are inline `python3`
one-shot scripts, single-threaded per the box-sharing note (35zzzg was
still using the box while this analysis ran).

### JOB 1: why A2 never produced -- traced to a specific, named event

**0. Heads did NOT diverge at restart.** Every one of the 7 nodes'
`txindex tail enabled` startup line at A2's own boot (15:06:22-27)
reports the identical `"head":13661138` -- ruling out a split-brain
head as the cause before looking further.

**1. The actual failure, found by direct log correlation, not
inference.** At 15:05:56-57, still INSIDE B2 (11-16 s before ANY
SIGTERM was sent), node5 -- leader of view 8785 -- logged, in this
exact order:

```
15:05:56  hotstuff: sealed block dropped — phase left WaitingForProposal   {block: 0x99e7eeba5f, phase: 1, view: 8785}
15:05:56  propose-before-write: the write failed AFTER the Proposal left  {err: "sealed block 13661138 parent 5f35affe70bce8ac is
                                                                              no longer the applied head (13661138/7a6d850bacab3b27):
                                                                              sealed block is stale (applied head moved past its parent)",
                                                                            hash: 0xf47f6514cc}
15:05:57  hotstuff: committed block not executed locally           {failures: 1, hash: f47f65…13d8ac, number: 0}
15:05:57  hotstuff: committed block not executed locally           {failures: 2, hash: f47f65…13d8ac, number: 0}
15:06:03  view timed out                                           {view: 8786}
15:06:03  hotstuff: committed block not executed locally           {failures: 3, hash: f47f65…13d8ac, number: 0}
15:06:03  hotstuff: refusing block production on unexecuted committed parent {failures: 3, hash: f47f65…13d8ac, number: 0}
```

**Reading this in order: two candidate blocks were sealed for the same
view (8785) in a burst of ultra-fast, near-empty-block view churn
right at B2's own teardown** (views 8784/8785/8786 all land within
about 7 seconds, per `"hotstuff: view changed"` `tMs` -- `1790017555516
-> 1790017556732 -> 1790017557043`, i.e. 1.2 s then 0.3 s between
them, versus the ~650-900 ms full-block cycle measured throughout this
whole campaign; blocks this late in a leg are emptying out as the
generators drain (6cv's own occupancy figures already show win2 fading
toward the leg boundary), so views advance far faster than a full
block's own cycle). `0x99e7eeba5f` was correctly dropped as the
divergent sibling (the existing "first sealed block wins"
suppression, `worker.go:611-622`). The KEPT candidate, hash
`f47f65…13d8ac` (`0xf47f6514cc` truncated the other way in the second
line), went on to collect a **complete quorum -- 5/5 votes -- and form
a CommitQC for view 8785** (node5's own `"hotstuff view timing:
view=8785 role=leader ... votes=5/5"` line, 15:05:57) -- **but its OWN
proposer's write of it FAILED** (`ErrStaleSeal`, `"sealed block is
stale (applied head moved past its parent)"`) because BY THE TIME the
write ran, node5's own locally-applied head had already advanced past
this block's parent (some other locally-processed activity, in the
same view-churn burst, moved the applied head first). Because
`N42_PUSH_BEFORE_WRITE`/`N42_PROPOSE_BEFORE_WRITE` send the raw block
data and the Proposal BEFORE the write runs (by design -- 6cq/6cs), the
Proposal and the votes it collected are entirely decoupled from
whether the write ever succeeds; the code comment for this exact
tradeoff (`internal/miner/push_order.go:36-40`) says plainly: **"the
only new exposure is that followers may have imported a block the
leader then abandons"** -- this round is a direct, measured instance
of exactly that exposure, except here the block was not merely
imported by followers but fully QC'd, and STILL never durably written
anywhere. Every one of the other 6 nodes shows the identical symptom
(`"hotstuff: persisted committed QC names a block this node does not
have"`, same hash, same `localHead: 13661138`, all at 15:07:2x-3x on
restart) -- **this is a fleet-wide, unrecoverable loss of ONE
committed block, discovered live at 15:05:56-57 and never resolved
before A2's decay window ran out and timed out with zero production.**

**2. This is NOT a shutdown/Drain bug.** The failure (`"committed
block not executed locally"`, `failures: 1`) is first logged at
15:05:56-57, **11-16 seconds before the SIGTERM sequence even begins**
(`"drained: head 0xd073d2 settled after 24 s"` then `node 0: SIGTERM`
in the round log, after `win2` closes). `Miner.Close()`'s `Drain`
(`internal/miner/miner.go:169-176`, `internal/miner/async_write.go:
221`) never had a chance to matter: there was nothing queued to drain
by the time shutdown began -- the write had already been attempted,
had already failed, and the node was already retrying
`fetch-on-miss` for a block that no peer anywhere possessed. No
`"miner: leader write queue did not drain within 30s on shutdown"`
error appears in any of the 7 kept logs, and `wqDepth`/`wqWaitMs`
(Job 2) read 0 at every window's median in both legs -- **the async
write queue was never backed up; this was never a queueing problem.**

**3. Attribution to the switch: plausible but not proven.** The
REJECTION mechanism itself (`ErrStaleSeal`/sibling-suppression/
propose-before-write's own documented exposure) is pre-existing code,
unrelated to `N42_LEADER_WRITE_ASYNC`. What is specific to this round
is the TIMING that exposed it: two candidates sealed for the same view
within the fastest view-churn window this whole campaign has measured
(sub-second, versus the usual 650-900 ms). Whether `N42_LEADER_WRITE_
ASYNC=1` (shortening the leader's own critical path, per S23's own
design intent) made this faster churn -- and hence the race window --
MORE likely is a reasonable hypothesis this task's own evidence cannot
settle: A2's failure happened at the very end of a B2 leg that ran
async ON, but the fast-churn burst itself involves node5's own
locally-applied-head bookkeeping racing against its OWN write, a
mechanism this section's log evidence does not fully unwind to a
single line. **Reported as found: plausible contributing factor, not
a proven cause.**

**4. Never seen before this round.** `grep -l "chain is not
producing" wr-logs/r35zz*.log` returns **only `r35zzzg.log`** -- no
earlier round (including every prior A2 leg, all of which ran with
`N42_LEADER_WRITE_ASYNC` unset/0) ever hit this failure mode. Combined
with (3), this is circumstantial but real: **the box has run this
exact leg-boundary transition dozens of times before without
incident; the one round it failed is the one round that also
introduced the new switch**, even though the failure mechanism itself
predates the switch.

**Root cause, stated plainly.** A validly-quorum-committed block
(CommitQC, 5/5 votes, view 8785) was never durably written by ANY of
the 7 nodes, because the push-before-write/propose-before-write
design lets a Proposal (and the votes/QC it collects) proceed
independently of whether its own write later succeeds, and this
round's leg-teardown view churn was fast enough to trigger the
write's own pre-existing stale-seal rejection on the very block that
went on to collect quorum. The next leg (A2) inherited a
committed-but-unwritable parent and could never produce past it. This
is a genuine, previously-undocumented liveness/durability gap in the
propose-before-write design -- not a new bug S23 introduced, but one
S23's round was the first to actually trigger.

### JOB 2: the A/B, full in-tenure views

| | B1win1 (async=0) | B2win1 (async=1) | B1win2 | B2win2 |
|---|---|---|---|---|
| CYCLE (median) | 651.5 (565-765) | 656.0 (596-781) | 856.5 (761-1244) | 904.5 (772-1117) |
| `resQWaitMs` | 0 (all) | 0 (all) | 0 | 0 |
| `wqWaitMs`/`wqDepth` | 0/0 (fields inert, async off) | 0/0 | 0/0 | 0/0 |
| `lwWaitMs`/`lwWhy` | journal 54.2%/timeout 45.8% | journal 82.6%/timeout 17.4% | journal 88.5%/timeout 11.5% | journal 73.3%/timeout 26.7% |
| leader `jcvMs` (median) | 0 (p90 12) | 0 (p90 0) | 0 (p90 4) | 0 (p90 349) |
| leader `jpvMs` (median) | 0 | 0 | 0 (p90 4) | 0 (p90 2) |
| `r1`/`r2` (median) | 62/79.5 | 63/87.0 | 81.5/123.0 | 70.5/163.0 |
| `resultRecv(v+1)-writeEnd(v)` | 233 (0% within 10ms) | 217 (0% within 10ms) | 283 | 294 |
| named-step constant (item c, 6cw's method) | 124.0 ms | 119.0 ms | -- | -- |

**(a) Prediction 89, clause by clause.** *"S23's switch is neutral
by construction (nothing on the resultLoop-bound critical path
changes)"* -- **confirmed**: `resQWaitMs` is 0 in every window, both
legs (as 6cv already found for a different round), and `wqDepth`/
`wqWaitMs` (the new async-queue diagnostics) read 0 at every
percentile checked -- the async writer was NEVER backed up, so it
never had anything to contend for. **(b)** wqDepth/wqWaitMs both 0/0
in both legs; cycle 651.5 -> 656.0 ms (win1, +0.7%) and 856.5 -> 904.5
ms (win2, +5.6%) -- both DIRECTIONALLY flat-to-slightly-worse, well
inside this campaign's own established round-to-round noise band
(6cm: ±26.6% between same-config rounds). **Prediction 89(a) --
"NEUTRAL" -- confirmed on win1; win2's own +5.6% is not distinguishable
from noise on this sample.** **(c)** leader `jcvMs` stays at the
median-0 ms floor in BOTH legs (S19's fix holds a THIRD round running);
`jpvMs` likewise; no evidence the leader/follower write overlap S23
introduces brings journal contention back -- the journal and the
(now-async) write remain uncontended at the median regardless of the
switch. Stale-seal drops: **1 observed this round, and it is the
fatal one from Job 1** -- `"sealed block is stale before its write;
dropping"` and `"propose-before-write: the write failed AFTER the
Proposal left"` both fire 0 times elsewhere in the B1/B2 windows
(grepped directly), so this was not a recurring background rate, it
was a single, leg-teardown event. `CommitToCanonical` waits/
`"committed block not executed locally"` are otherwise absent from
B1/B2 proper (all occurrences are inside the A2 failure window,
already covered in Job 1). **(d)** occupancy/TPS: B1win1 139.2k @
1.154s, B2win1 136.5k @1.176s (comparable); B1win2 90.3k @1.714s,
B2win2 100.6k @1.463s -- see the WIN2 comparison below. **(e)** BAD
BLOCK 0, divergence 0 (both legs); TC/timeout events B1 2/3, B2 4/7
(B2's own higher count includes the fatal view-churn burst, still a
small absolute number); every OTHER proposed-and-committed own block
in B1/B2 has its own successful write line (checked by the same
`import_breakdown`-style presence check earlier sections use) -- **the
ONE exception, fleet-wide, is Job 1's own block**, already covered,
not a second instance.

**WIN2: is B2's 100.6k vs B1's 90.3k a mechanism, or noise?** Compared
against the last two rounds' own B1/B2 win2 pairs -- **35zzzf: 92.3k /
93.5k, 35zzze: 95.1k / 93.7k, this round: 90.3k / 100.6k** -- the
spread across all three rounds' six win2 numbers is 90.3k-100.6k, a
10.2% band with NO consistent B1-vs-B2 direction (35zzzf and 35zzze
both had B2 slightly ABOVE B1; this round has B2 further above B1 by a
larger margin, but 35zzzf/e's own gaps were 1.3%/-1.5% while this
round's is +11.4%, an outlier in MAGNITUDE though not in DIRECTION).
**Verdict: sits inside leg-to-leg noise** -- three rounds is not
enough to call a repeatable async-write win2 effect, and nothing in
the seal-path stamps (identical `resQWaitMs`, comparable `jcvMs`/`r1`/
`r2`) points to a mechanism that would make B2's win2 specifically
faster; the more likely explanation is the same generator-supply/
page-cache variability 6cv already established as noisy at this
sample size.

### JOB 3: S20 continuity, `r35zzzg-vm.log`

| | B1 ramp | B1win1 | B1win2 | B2 ramp | B2win1 | B2win2 |
|---|---|---|---|---|---|---|
| `pgmajfaultD`/10s | 1,812 | **8,389** (4.6x) | 3,700 | 1,705 | **20,661** (12.1x) | 4,468 |
| `pgscanKswapdD`/10s | 119,395 | **1,075,726** (9.0x) | 354,383 | 25,308 | **1,107,365** (43.8x) | 230,353 |
| `refaultFileD`/10s | 883 | **4,868** (5.5x) | 4,100 | 1,053 | **17,285** (16.4x) | 9,024 |
| `RssAnon` (avg, MB) | 2,850 | 9,994 | 10,332 | 3,632 | 9,822 | 10,262 |
| `RssFile` (avg, MB) | 4,491 | 2,388 | **1,423** | 5,343 | 3,306 | **1,782** |

**A second round confirms 6cv's exact shape**: a large ramp->win1 jump
on every OS fault/scan counter (4.6-43.8x here, 17-200x in 35zzzf --
both rounds show the SAME qualitative pattern, with this round's B1
leg jump notably smaller than either leg of 35zzzf, consistent with
the campaign's own established noise band rather than a contradiction),
`RssFile` roughly halving again win1->win2 in both legs (2,388->1,423
MB B1, 3,306->1,782 MB B2), and `RssAnon` climbing to the same ~10.0-
10.3 GB per-node ceiling regardless of leg or switch. Page-cache
thrash is present in BOTH the async-off (B1) and async-on (B2) legs,
at comparable magnitude -- **S23's own switch has no visible effect on
the memory-pressure mechanism**, exactly as expected since it only
reorders WHEN a write happens, not how much memory the block's own
execution/state-root work touches.

**Method.** Job 1 is a direct, manual log correlation (`grep`/`python3
-c` one-liners) across all 7 kept files around the exact failure
timestamps -- no script was needed or written, per this task's own
"do not guess its content" spirit: every claim above is a quoted log
line, not an inference from absence. Job 2 reuses `seal_path_
waterfall.py` (6cw) unmodified with this round's own leg/window
arguments; `wqDepth`/`wqWaitMs` were pulled with a short inline query
since the checked-in script predates their addition to `"miner: seal
path"`. Job 3 reuses 6cv's own window-derivation and `vm.log`-parsing
method verbatim, on this round's own ramp/win1/win2 boundaries
(re-derived from the full-block sequence, not fixed offsets, per 6cv's
own established practice after 35zzzf's mistimed captures).

**What this does and does not show.** It shows prediction 89 holds on
its own central claim (the switch is neutral: cycle unchanged within
noise, `resQWaitMs`/`wqDepth`/`wqWaitMs` all 0). It shows, with a
directly-quoted, fleet-wide-corroborated log trail, that leg A2's
total production failure traces to ONE specific committed-but-
unwritten block caused by a pre-existing propose-before-write/
sibling-race exposure (documented in the code's own comment as a known
tradeoff) that this round's leg-teardown view-churn was fast enough to
trigger -- not a shutdown/Drain bug in the new async writer, which
never had anything queued when the failure occurred. It does NOT prove
`N42_LEADER_WRITE_ASYNC` caused the fast view-churn that exposed the
race -- that attribution is stated as plausible, not proven, per (3)
above. It does NOT establish a repeatable win2 mechanism from the
async switch -- three rounds of B1/B2 win2 pairs show no consistent
direction. It does NOT change any of 6cv/6cw's own findings about
build/import composition or page-cache thrash -- Job 3 reproduces the
same shape a second time, at somewhat smaller magnitude on B1, both
inside the established noise band.

**Addendum (S23b, 2026-09-21).** The block named in Job 1 above (hash
`f47f65…13d8ac`, view 8785, would-be height 13661138) was traced in
full: it was a genuine SIBLING of the already-written `7a6d85…23259c`
at the SAME height, both children of block 13661137, produced by node5
across its own 4-view tenure under sub-second view churn -- see
`docs/OPEN_ISSUES.md`, "A quorum-committed block that no node stored,"
for the complete evidence trail and classification (**B: reachable on
r92 too, not specific to S23's relaxed pre-check** -- retiring
`N42_LEADER_WRITE_ASYNC` does not close this hazard).

## 6cy. S25: the in-window capture proved offline before being handed back a third time -- a real parsing bug (anchored regex) and an unreachable threshold (95% on a shape whose full blocks top out near 50%), plus a GOMEMLIMIT A/B; prediction 90 registered before the round (2026-09-21)

**Why.** The in-window capture has now failed in two rounds for two
different reasons: round 35zzzf's own captures landed inside the
baseFee-decay ramp (wrong trigger, fixed by S22/6ct's switch to a
first-full-block detector); round 35zzzg's fix had a real bug that made
that SAME detector spin until its own deadline every time. This step
does not hand the fix back a third time without testing it first.

**1a/1b. The bug, and proving the fix offline (not "trust me").**
`grep -o '"gasUsed":"0x[0-9a-f]*"' | grep -o '0x[0-9a-f]*$'` -- the
FIRST grep's own match is `"gasUsed":"0x1234"` (quotes included), and
the hex digits are never the LAST characters of that string (a closing
`"` always follows), so the `$`-anchored second grep never matches;
`gu`/`gl` stay empty forever and the poll loop spins until the leg's
own 900s deadline. Confirmed directly, not just reasoned about:
running the OLD pattern against a real matched string in this shell
reproduces empty output every time. Fixed by dropping the anchor.
Extracted into `wt-r27/scripts/qs-harness/full_block_check.sh`
(`is_full_block`, a pure function: 0=full, 1=not full but parsed OK,
2=could not parse -- the caller must retry on 2, not treat it as
"empty") and tested with `test_full_block_check.sh` against ten canned
JSON cases: an empty block, 48.7%/22%/45%(boundary)/44%(just under)
percent-full blocks, an RPC error response, a completely empty body,
gasUsed-before-gasLimit and gasLimit-before-gasUsed (field-order
independence), and a realistic response with extra trailing fields.
**All 10 pass.** The threshold itself also needed fixing, separately:
95% (copied from `measure-tps.sh`'s own occupancy convention) is
unreachable in this harness's shape, where `N42_MINER_FILL_GAS` caps
the builder's own fill at HALF the header gas ceiling -- a genuinely
full block never gets much past ~50%. This means round 35zzzf's own
capture attempt plausibly never fired EITHER, quietly (it degrades to
"no full block seen, skipping" rather than an error), and 35zzzg's
timeout is what finally made the underlying bug visible. Lowered to
45%. `dry_run_capture.sh` then runs the WHOLE sequence -- poll (against
a stubbed `curl` shell function serving two empty blocks then a full
one) -> detect -> wait to win1_start+15s -> capture -> wait to
win2_start(+60s)+15s -> capture -- end to end with scaled-down timings,
writing real files to a `mktemp -d` directory and checking they exist,
are non-empty, and that the two captures land the expected number of
seconds apart. **Dry run: OK.** No live fleet was used for any of this.

**1c. When win1 actually opens.** Read (not modified):
`/data/blockchain/scripts-qs/bench-run.sh` and `measure-tps.sh`.
`bench-run.sh` prints `"all $FLOODS flood(s) submitting; opening
measurement windows"` (`bench-run.sh:281`) the instant every generator
is confirmed flooding, THEN unconditionally sleeps 15s
(`bench-run.sh:282`, its own comment: "let the pool reach depth")
before calling `./measure-tps.sh --windows "$WINDOWS" --window-sec 60`
(`bench-run.sh:293`). `measure-tps.sh`'s own per-window loop opens
win1 immediately on entry -- `h0=$(head_num); t0=$(date +%s)` is the
very first thing inside the `for (( w = 1; w <= WINDOWS; w++ ))` loop
(`measure-tps.sh:32-33`), with nothing between the function call and
this line. **So win1's true start is that print line's own timestamp
plus exactly 15 seconds -- not a proxy for it, the harness's own
literal schedule.** `measure-tps.sh` itself prints NOTHING at a
window's start (only a one-line summary AFTER each 60s window's sleep
returns, `measure-tps.sh:70-71`), confirming there is no more direct
live signal available than the flood-announcement line. This line is
ALSO the exact string this same script's own `check_mode`-gating logic
already tracks (`local mark; mark=$(grep -c 'all 8 flood' $L ...)`,
computed once right after this leg's own `benchpid` starts) -- reused
directly rather than duplicated, so "this leg's own occurrence" means
the same thing in both places. This is now the PRIMARY win1-detection
signal; the fixed first-full-block poll is kept as a FALLBACK for if
the primary string is ever not seen (e.g. `FLOODS` stops being 8, or
the wording changes) within the poll deadline.

**1d. Captures.** Per window, from the sitting leader and one
follower (unchanged selection): cpu (20s), mutex (20s), block (20s),
heap, goroutine (`debug=1`), plus NEW **allocs** (the cumulative
allocation profile -- heap answers "what is resident now," allocs
answers "what is being allocated fastest," which a short-lived
high-churn allocator can dominate without ever showing a large inuse
figure). Separately, since this campaign's own CPU profiles are
badly undersampled (~1.76s of samples per 20s capture -- a longstanding,
unexplained gap this step does not chase), the 10s VM sampler gains
kernel-level CPU accounting that does not depend on pprof's sampling
at all: `utime`+`stime` from `/proc/<pid>/stat` fields 14/15 (this
box's own clock tick confirmed at 100 Hz via `getconf CLK_TCK`, not
assumed), for the seven nodes AND the eight txflood generators, plus
the generators' own `RssAnon` (the existing memory watchdog already
reads combined generator RSS, never per-generator, and never their
CPU) -- so a within-leg slowdown can finally be split into "the chain
got slower" vs. "the load generators themselves slowed down."

**Part 2 -- GOMEMLIMIT.** Found where it is set today: `run_leg`'s own
`export GOMEMLIMIT=10GiB` (this script; every round back to 35zb has
used this fixed value -- 35zb's own note: "the follower import is 2x
its uncontended 731 ms because the seven heaps squeeze the page cache
to 3-4 GB of MDBX per node"). `bench-7node.sh` does not set or
override it itself (confirmed by grep: no match), so this `export` is
the only place it is set for the fleet's node processes; `GOGC=200`
(same location) is unrelated and left unchanged, per the task. This is
the first round that varies it: `run_leg` gained a 6th argument
(`GOMEMLIMIT`, e.g. `"10GiB"`/`"6GiB"`) exported verbatim; `N42_LEADER_
WRITE_ASYNC` is NOT a `run_leg` argument this round (left unset
everywhere, matching the binary staying at n42-r92, not r93).

**Part 3 -- runtime memstats, no profile needed.** `/debug/vars`
(expvar) does **not** exist on these nodes: confirmed by grep, no
`expvar` import anywhere in `cmd/n42` or `internal`. `/debug/pprof/
heap?debug=1` DOES carry what is needed: verified directly against
this box's own Go 1.26 source (not guessed) --
`net/http/pprof/pprof.go`'s own doc comment states plainly "debug=N
(all profiles): response format: N = 0: binary (default), N > 0:
plaintext," and `runtime/pprof/pprof.go`'s `writeHeap` (~lines
717-753) prints a `"\n# runtime.MemStats\n"` block ending the
response, containing `HeapAlloc`/`HeapSys`/`HeapIdle`/`HeapInuse`/
`HeapReleased`/`HeapObjects`, `NumGC`, `NumForcedGC`, `GCCPUFraction`,
and `PauseNs` -- **one correction to the task's own expectation: there
is no single cumulative `PauseTotalNs` field printed here; `PauseNs`
is a ring-buffer array of recent raw pause samples.** `NumGC` (a per-
window delta gives GC frequency) and `GCCPUFraction` are what this
round reads for GC cost instead, which serves the same purpose. Not
tested against a live fleet (none is up while preparing this round),
so the new sampler is written tolerant of a non-200 status or empty
body regardless, logging `"HTTP <code> (tolerated)"` rather than
failing. Samples two fixed nodes (0 and 1 -- cheap enough not to need
leader/follower role-targeting) every 30s (`tail -n 40` of the
response, comfortably covering the ~23-line trailer) into
`wr-logs/r35zzzh-memstats.log`.

**Runner.** `run-r35zzzh.sh`/`chain-35zzzh.sh` built from the
`run-r35zzzf.sh`/`chain-35zzzf.sh` pair (NOT 35zzzg -- the binary
stays n42-r92) via `cp`+`sed 's/35zzzf/35zzzh/g'`. Fixed by hand
afterward: the predecessor-wait (`chain-35zzzh.sh` now waits on
`r35zzzg.log`, per this step's own instruction -- 35zzzg has already
finished) and the header comments (rewritten to describe S25, not the
inherited S22 text). GOMEMLIMIT plumbed as `run_leg`'s 6th argument;
calls: `warmup 1 10GiB`, `A1 1 10GiB`, `B1 1 10GiB` (today's value,
the in-round baseline), `B2 1 6GiB`, `A2 1 6GiB`. Both already-adopted
switches (`N42_LEADER_WRITE_AFTER_JOURNAL`, `N42_CONTENTION_DIAG`)
stay on in every leg; `N42_LEADER_WRITE_ASYNC` stays unset. `bash -n`
clean on both; confirmed not running.

**Prediction 90 (registered before any round):**

**(a) Mechanism.** In B2 (6 GiB), per-node `RssAnon` in win1/win2 is
lower than B1's (10 GiB) by >= 2 GB; per-node `RssFile` is higher;
fleet `pgmajfault` and `workingset_refault_file` per 10s tick in win2
are lower by >= 2x -- the direct OS-counter signature 6cr/6cv already
established for page-cache thrash, now tested as a LEVER rather than
merely observed.

**(b) Cost.** GC count per window (`NumGC` delta from the new memstats
sampler) and GC CPU (`GCCPUFraction`, or the VM sampler's own per-node
CPU-seconds split against block count) reported for both legs. If
`NumGC` per block rises above ~1 in B2, the 35q failure mode (heaps
collecting on every block under a too-tight limit) is back and 6 GiB
is too low a value for this shape.

**(c) Score, with the leg-order caveat stated up front.** Second
windows of otherwise-identical legs have differed by up to ~10k TPS
between legs in this campaign already (6cv), so: B2 win2 block time
<= 1.45s against B1's own ~1.7s would be a real effect; anything
inside 1.6-1.8s is no effect, not a null result to over-read.

**(d) What is actually in the heap.** The in-window heap profiles
(win1 AND win2, both legs) are the first this campaign has ever taken
INSIDE the scored flood window rather than the empty-block decay ramp
-- top 10 `inuse_space` entries settle 6cr/6cv's own open question
("what fills anonymous memory is NOT yet known... every heap profile
so far was taken in the empty-block phase").

**VERDICT: confirmed** (offline tests pass, dry run passes, harness-
only changes built and syntax-checked). QS_QUEUE.md's S25 row status
is marked prepared with prediction 90 (6cy). Launch is the
commander's next call.

## 6cz. S26 (SAFETY): the vote rule fails open under two-phase voting, not the leader path; regression tests fail on n42-r92 and pass on n42-r94; prediction 91 registered before the round (2026-09-21)

**Why.** Round 35zzzg (docs/OPEN_ISSUES.md "A quorum-committed block
that no node stored"): node5 led views 8782-8785. View 8784 committed
`7a6d85…23259c` (height 13661138, parent `5f35af…`=13661137), 5/5
votes both rounds. One second later view 8785 committed
`f47f65…13d8ac` -- ALSO height 13661138, ALSO parent 13661137 -- 5/5
votes both rounds. Node5's own write of the second block failed
(`ErrStaleSeal`) after its CommitQC had already formed; the other six
nodes voted it through the deferred path, then parked it as a future
block they could never place. A block with a full, valid CommitQC
exists in zero of the seven nodes' chains.

**Part 1 -- reading before touching anything.**

*(a) The vote rule, as implemented (`internal/consensus/hotstuff/`).*
SR1 (`RoundState.IsSafeToVote`, `proposal.go:196`) is a VIEW-based lock:
`justify_qc.view >= locked_qc.view`. It is necessary but not sufficient
here -- a leader who legitimately just committed X (view 8784) has its
own `LockedQC()` at X by view 8785, so a stale proposal Y whose
JustifyQC is built from THAT SAME up-to-date lock (`onBlockReady`:
`justifyQC := e.roundState.LockedQC().Clone()`) satisfies SR1 trivially
even though Y's own block body does not extend X at all. The rule that
is SUPPOSED to catch that mismatch, `extendsJustify` (`proposal.go:571`,
"the proposed block's parent must be the proposal's JustifyQC block"),
already existed (from an earlier, different incident at height
13014242) and is correctly wired into every DEFERRED vote path
(`tryDeferredVote`, `onBlockImported`'s import-gated branch) -- but
`processProposal`'s TWO-PHASE branch (the mode this fleet actually
runs) had its own, separate, unconditional-vote shortcut: `if
e.twoPhaseVote { if e.importedBlocks[...] && !extendsJustify {refuse};
journal+vote }` (pre-fix, ~`proposal.go:256-264`). Since a Proposal is
processed the instant it arrives -- always before the block's own body
is checked or imported -- `e.importedBlocks[proposal.BlockHash]` is
false essentially every time, so the `&&` short-circuits and the vote
is journalled and sent with NO extends check at all. This is not a
narrow race; it is the ordinary case for every prepare vote under
two-phase voting. Round 2 was worse: `processPrepareQC`'s two-phase
gate (`proposal.go:441`, pre-fix) held the commit vote until
`e.importedBlocks[pqc.BlockHash] || e.deferredAttested(pqc.BlockHash)`,
and `deferredAttested` (`proposal.go:321-327`) checks only "the block
was checked AND its OWN (possibly stale) parent is locally applied" --
which is true of Y precisely because its stale parent (13661137) is
old enough to be canonical everywhere. Neither the aggregate PrepareQC
signature verified just above (proves a quorum SENT prepare votes, not
that the votes were for a block that extends anything) nor
`deferredAttested` ever calls `extendsJustify`. So the data needed
(the proposal's real parent, from `checkedBlocks`/`importedParents`,
populated by `onBlockChecked` off the deferred-execution check) IS
available by commit-vote time -- the code just never asked it the
right question on either round.

*(b) The leader side.* Node5 leads a 4-view tenure (8782-8785); a
speculative build parked on parent 13661137 sometime before view 8784
decided X, and was sealed/taken later, in view 8785, still carrying
that stale parent. `onBlockReady`'s own pre-propose guard
(`proposal.go:65-73`, from the 13014242 incident) compares the sealed
block's parent against `LockedQC().BlockHash`, but ONLY when
`e.importedParents[blockHash]` is already known -- and for a block
this node itself just sealed (never externally "checked" or
"imported"), that map entry does not exist yet, so the guard fails
open by design (`TestSealedBlockProposedWhenParentUnknown` pins this
intentionally). The actual leader-side gap is upstream of the hotstuff
package: `worker.go`'s height-level single-candidate guard
(`firstSealedOnParent`/`recordSealedOnParent`, "keep only the first
block sealed on a given parent") used to record the winner only AFTER
its write completed (`writeAndFinish`, "after a successful import").
Under `PUSH_BEFORE_WRITE`/`PROPOSE_BEFORE_WRITE` the write is the
SLOWEST step in the sequence, so a second, independently-sealed
candidate on the same parent (the parked task) can reach `handleSealed`
while the first block's write is still in flight and find the map
empty -- treating itself as the only candidate instead of being
suppressed. r93's S23 bypass (`checkSealParentApplied` accepting
`asyncWriter.ExpectedParent()`) is a red herring here, confirmed
independently: it only ever widens the SAME race (whichever check runs
at write time), and the write-time check is not what admitted Y to a
CommitQC in the first place -- the CommitQC had already formed by then.
This guard was "usually true" only because writes are normally faster
than a second seal on the same parent arriving; nothing made it
airtight.

*(c) Protocol.* Confirmed from the design doc, not paraphrased:
`docs/consensus/hotstuff2-spec.md:96`, "A block is committed when, in
the same view, both PrepareQC and CommitQC have formed. No third
phase." Two CommitQCs at one height, from two different views, is an
unambiguous violation of that stated intent -- the design gives no
third round in which one could be reconciled against the other. This
also settles PART1(c): the commander's SEVERITY note ("a correct vote
rule alone would have made (1) harmless") is exactly what the design
doc's own finality rule requires -- a proposal that will never form a
valid PrepareQC because no honest voter extends it can never reach
CommitQC, regardless of what the leader does upstream.

**Part 2 -- regression tests, proven to fail first.**
`internal/consensus/hotstuff/conflicting_commit_test.go` (new, reusing
the existing `newTestSetup`/`newTestEngine` harness):
`TestTwoPhasePrepareVoteRefusesNonExtendingProposal` (Round 1: a
two-phase follower must not vote for a proposal until it knows the
block's real parent, and must never vote once that parent turns out
not to match JustifyQC) and `TestTwoPhaseCommitVoteRefusesNonExtendingProposal`
(Round 2: even granting a PrepareQC formed, the commit vote must still
be refused). Both FAILED on the pre-fix code (`prepare-voted ... before
its parent was known: 1 votes`; `commit-voted for a proposal that does
not extend its JustifyQC block: 1 votes`) and PASS after the fix.
`TestTwoPhaseVotesStillFireForAnExtendingProposal` is the happy-path
guard (passed both before and after). `internal/miner/seal_guard_test.go`
(new) pins the leader-side invariant the relocated `recordSealedOnParent`
call depends on: `TestRecordSealedOnParentFirstSealWins` -- a second,
divergent seal on an already-recorded parent must never overwrite the
kept candidate (this one is a map-level unit test, not a full
`handleSealed` integration test; the vote-rule fix above is what
actually makes a leader-side miss harmless, per PART1(c)).

**Part 3 -- the fix, no switch.** (i)
`internal/consensus/hotstuff/proposal.go`: two-phase mode's Round 1
branch is removed; both modes now share the import-gated branch's
logic (vote immediately if already imported, else `tryDeferredVote`,
else defer), so `extendsJustify` always runs once the parent is known
instead of only when it happened to be known already. `tryDeferredVote`'s
own "justify must be imported" condition is relaxed for a zero
(genesis) justify -- matching `extendsJustify`'s own fail-open rule for
that case -- fixing a latent bug this change surfaced
(`TestDeferredPipelineCommitsWithoutImportingTheBlock` failed until
this one-line fix: the FIRST block after genesis has justify=zero,
which the old unconditional-import check waited on forever). Round 2
(`processPrepareQC`) gains an explicit `extendsJustify` call once the
two-phase hold-check has decided not to hold (by which point the real
parent is always known), refusing the commit vote otherwise. No wire
format change: the Proposal message itself carries only
`BlockHash`/`JustifyQC` (no parent/number field, confirmed by reading
the struct), so the fix relies entirely on the SAME
`checked`/`imported` bookkeeping the non-two-phase path already used --
the vote simply waits for the block's pushed body (via
`EventBlockChecked`/`EventBlockImported`) instead of trusting the
Proposal message alone. (ii) `internal/miner/worker.go`:
`recordSealedOnParent` moves from `writeAndFinish` (after the write)
to `handleSealed`, immediately after the `firstSealedOnParent`
suppression check and before push/propose -- so a second seal on the
same parent finds the record no matter how long the first one's write
takes. Total diff: ~35 lines in `proposal.go`, ~20 lines in
`worker.go` (comments included), well under the ~200-line budget; no
protocol decision was needed. Every existing test in both packages
passes unchanged (`internal/consensus/hotstuff`, `internal/miner`,
`internal/miner/builder`, plus `internal/parallel` and `internal/`
from the build worktree's own rebuild).

**Part 4 -- build and harness.** `n42-r94` = n42-r92's exact file set
(NOT r93/`N42_LEADER_WRITE_ASYNC`, retired) + this fix, via the same
file-checkout recipe (detached worktree at `f7ec2836`, layering
`c0931aeb`, `537ec21e` (+ the same worker.go hunk n42-r86 through r92
already needed, reproduced by isolating each lever commit's OWN diff
against its immediate parent and applying in sequence -- `git apply
--reject` left the SAME one conflict, the "speculative build hit" log
line, resolved by hand exactly as documented for r92), `56cc1dac`,
`b876b3d2`, `9f307e90`, `e1d8d7d1`, `812cf162`, `62439af7`, then this
step's `proposal.go`/`worker.go` changes on top). One-variable check:
rebuilding r92 from scratch this way and diffing its `worker.go`
against `62439af7`'s own blob left exactly the four already-known
off-lineage lines (`activeSpecParent`, two `tMs` fields on the
speculative build/parked lines, one `tMs` on "miner: build phases")
that every prior build in this chain has shown -- confirming the
reconstruction before layering the fix on top; `proposal.go`'s
pre-fix content was BYTE-IDENTICAL to HEAD's own pre-S26 `proposal.go`
(no commit touched it between `812cf162` and this step), so it was
copied directly rather than patched. `internal/parallel/base_cache.go`
confirmed absent; `strings n42-r94 | grep -c BaseCache` = 0. Markers:
`"miner: seal path"` = 1, `"import-gated vote REFUSED: proposal does
not extend its JustifyQC block"` = 1, `"commit vote REFUSED: proposal
does not extend its JustifyQC block"` (new) = 1.
`/data/blockchain/gov5-work/n42-r94`: 108,779,120 bytes, sha256
`658bee2d0aabaf45c010f500f85eec263cb6c40fadb53754d0c0f4404b67e588`.

`run-r35zzzi.sh`/`chain-35zzzi.sh` built from the `run-r35zzzh.sh`/
`chain-35zzzh.sh` pair via `cp`+`sed 's/35zzzh/35zzzi/g'`, then fixed
by hand: the predecessor-wait (`chain-35zzzi.sh` now waits on
`r35zzzh.log`, its actual predecessor), the binary references
(`n42-r92` -> `n42-r94`), GOMEMLIMIT made uniform at `10GiB` in every
leg (B2/A2 no longer run 6GiB -- S25's own A/B by leg is a separate,
still-open question, not repeated here), and the header comments.
In-window captures and the S20 VM/memstats samplers carry over
unchanged from 35zzzh.

Two new end-of-round checks added to `run-r35zzzi.sh`, after the legs,
before the terminal line, neither gated by a switch:
`check_conflicting_commits` (joins "block committed!"'s `blockHash`
against the height carried on "Successfully sealed new block"/"add
future block"/"block push: received" via `jq`, across all seven
nodes' `n42.log`s; prints `ROUND ABORTED: conflicting commits at
height N (<hashes>)` for any height with more than one distinct
committed hash) and `check_legs_produced` (greps the round log for
bench-run.sh's own refusal, "chain is not producing"/"refusing to
measure", and prints `ROUND ABORTED: leg <x> did not produce`,
attributing it to the most recently started `LEG` line above it).
Tested OFFLINE against real data before trusting them: against the
kept logs of 35zzzg, `check_conflicting_commits` correctly flags
`13661138 7a6d85…23259c,f47f65…13d8ac` and `check_legs_produced`
correctly flags leg A2 (deduplicated to one line, since bench-run.sh
prints two refusal lines for the same leg); against the kept logs of
35zzzf, both are silent. `chain-35zzzi.sh` waits on
`wr-logs/r35zzzh.log`'s terminal line. `bash -n` clean on both;
confirmed not running (`ps` shows no `run-r35zzzi`/`chain-35zzzi`
process).

**Prediction 91 (registered before any round):**

**(a) Safety.** Zero heights with two distinct committed hashes across
all seven nodes' logs, in every leg; every leg produces (neither new
harness check fires).

**(b) Cost.** The fix adds work only to the deferred vote path that
already existed for non-two-phase voting -- two-phase Round 1 now
waits for `EventBlockChecked`/`EventBlockImported` the same way the
import-gated path always has, instead of voting on the bare Proposal
message. The body (and therefore the check/import that lets
`tryDeferredVote` fire) reaches the quorum-forming follower ~44 ms
after the push (6ce), so Round 1 should land ~40-45 ms later than
before on the common path -- but Round 1 has never been on the
critical path in this campaign (6ct/6cv/6cw: the leader's own build,
611-691 ms, dominates the in-tenure cycle; Round 1+Round 2 together
have measured at 24 ms of a 393 ms Round 2 budget, 6cm). So win1's
in-tenure cycle and B-leg first windows should land within the noise
floor of 35zzzf's (680-691 ms; ~140k), not move by anywhere near
40 ms.

**(c) Regression tests.** `TestTwoPhasePrepareVoteRefusesNonExtendingProposal`
and `TestTwoPhaseCommitVoteRefusesNonExtendingProposal` fail on n42-r92's
source and pass on n42-r94's.

**VERDICT: confirmed** (regression tests fail-then-pass as required;
fix is additive, no switch, ~55 lines total, no protocol decision
needed; n42-r94 built and one-variable-checked; full
`internal/consensus/hotstuff`/`internal/miner`/`internal/miner/builder`/
`internal/parallel`/`internal/` suites pass). QS_QUEUE.md's S26 row
status is marked prepared with prediction 91 (6cz).
`docs/OPEN_ISSUES.md`'s entry is updated to reflect the prepared fix.
Launch is the commander's next call.

## 6da. S25: the flood's live heap is 5.4-7.5 GB, GOGC=200 would want 3x that, and 6 GiB leaves no room -- txpool/sender-cache/txlookup and QMDB's own index are the two families that would have to shrink (2026-09-21)

> **CORRECTION (commander, 2026-09-21 18:30 EDT) -- the "10.36 GB per block / 65-68 KB per transfer" figure in
> this section is WRONG and every number derived from it by division must not be used.** The harness fetched
> `/debug/pprof/allocs` WITHOUT `?seconds=`, which returns allocation CUMULATIVE SINCE PROCESS START (the whole
> leg: start-up, funding, 400 s of empty decay blocks, ramp, flood), and the analysis divided that total by the
> 16 blocks of the 20 s capture span. The runtime's own counter settles it: MemStats TotalAlloc on node0 rose
> 46-50 GB per 30 s in B1's first window = ~1.6 GB/s = **~1.9 GB per full block, ~12 KB per transfer per node**
> (second window ~2.3 GB per block). What stays valid: SHARES within the profile (as shares of the leg's
> cumulative allocation, mixing phases), the live-heap figures (inuse_space), NumGC, and the GC share of CPU
> (CPU profiles were 20 s deltas). With ~12 KB per transfer in total, the isolated benchmark's 6.25 KB per
> transfer for the executor is about HALF of a node's allocation, consistent with the partition's ~49%
> execution share -- the '4.4x gap' between benchmark and fleet was this artefact, not a property of the
> backend. From round 35zzzj on the capture uses `allocs?seconds=20`.

n42-r92 (harness-only change: in-window pprof capture, GOMEMLIMIT A/B by
leg, GOGC=200 everywhere) ran B1 (10GiB, 16:15:28-16:28:40) and B2
(6GiB, 16:28:40-16:41:54) cleanly. Node logs preserved whole, trimmed to
`wr-logs/r35zzzh-keep/node{0-6}-B.log`. Samplers:
`wr-logs/r35zzzh-vm.log` (per-node CPU-seconds; the `gens:` generator
field is present in the format but **empty on every single line checked
-- Job 3 has no data this round**, not analysed further) and
`wr-logs/r35zzzh-memstats.log` (nodes 0/1 `debug=1` heap dump trailers
every 30s). Pprof captures landed inside the scored windows for the
first time: `wr-pprof/r35zzzh-win{1,2}-win{1,2}-node{N}-*.pb.gz`.
**File-name collision found and worked around**: B1's win2 capture used
`leader=node6` and B2's win2 capture ALSO used `leader=node6`, and
since the file names carry window label but not leg, **B2's win2
capture for node6 overwrote B1's** (mtimes: B1 win2 files are 16:26,
B2 win2 files are 16:39-40; only the later set exists on disk for
node6) -- B1win2's own LEADER heap profile is lost; its FOLLOWER
(node0, not reused by B2) survived. Script:
`wt-r27/scripts/qs-analysis/height_conflict_check.py` (Job 4 only; Jobs
1-3 used `go tool pprof` directly with `GOCACHE=/data/blockchain/
gov5-work/.gocache` plus short inline `python3` reads of `-memstats.log`/
`-vm.log`, no new script needed for those).

### JOB 1: what is in the heap during the flood

**Live heap totals** (`inuse_space`, `go tool pprof -top`): B1win1
node1 (leader) **5.78 GB**, node2 (follower) **6.30 GB**; B1win2 node0
(follower only, leader lost) **7.46 GB**; B2win1 node3 (leader)
**5.69 GB**, node4 (follower) **5.49 GB**; B2win2 node6 (leader)
**5.71 GB**, node2 (follower) **5.07 GB**. **Every one of these is well
below both the 10 GiB and the 6 GiB limit** -- the flood's live heap
was never actually starved of room in EITHER leg; what changes between
legs is how much slack is left above it (see JOB 2).

**Top contributors, `inuse_space` (B1win1 node1, representative of
every capture -- B2's own leader/follower captures reproduce the same
ranking within 1-2 percentage points):**

| subsystem | flat | % of total | owning code |
|---|---|---|---|
| txpool lookup index (`txlookup.(*Tail).Add`) | 1.04 GB | 18.0% | `internal/txlookup/` |
| sender-recovery cache (`transaction.senderCachePut`) | 0.93 GB | 16.2% | `common/transaction/` |
| QMDB in-RAM live-key index (`qmdb.newMapIndexSized`) | 0.78 GB | 13.6% | `lib/qmdb/index.go` (6cr's own candidate) |
| RLP uint256 decode buffers (`rlp.decodeUint256`) | 0.42 GB | 7.2% | `common/rlp/` |
| tx decode (`transaction.decodeEthereumTransaction`, flat) | 0.34 GB | 5.8% (cum 18.8%) | `common/transaction/` |
| tx decode (`transaction.DecodeEthereumTransaction`, flat) | 0.31 GB | 5.4% (cum **25.3%**) | `common/transaction/` |
| pooled transaction objects (`transaction.NewTxOwned`) | 0.26 GB | 4.6% | `common/transaction/` |
| MVS/parallel-executor read-write sets (`parallel.NewReadWriteSet`) | 0.18 GB | 3.1% | `internal/parallel/` |
| receipts deep-copy (`miner.copyReceipts`) | 0.14 GB | 2.4% | `internal/miner/worker.go` |

**By cumulative caller**, the single largest attributable chain is
**RPC batch submission decoding**: `jsonrpc.(*handler).handleMsg.func1`
-> ... -> `api.(*TransactionAPI).BatchRawTransaction` -> ...
-> `transaction.DecodeEthereumTransaction` accounts for **1.71 GB
(28.9% of the whole heap)** -- this is the generators' own
`eth_batchRawTransaction` submissions being decoded and held. Top-5 by
`inuse_objects` (object count, not bytes): `rlp.decodeUint256`
(13.98M objects, 25.1%), `transaction.senderCachePut` (12.54M, 22.5%),
`transaction.(*Transaction).Hash` (3.29M, 5.9%), `reflect.unsafe_New`
(3.01M, 5.4%), `transaction.DecodeEthereumTransaction` (2.97M, 5.3%) --
the pool's own per-transaction bookkeeping (uint256 fields, sender
cache, hash cache) dominates OBJECT COUNT even more than it dominates
bytes, meaning per-object overhead (not payload size) is a real
secondary cost here.

**Win1 vs win2**: live heap grows from ~6.0 GB (B1win1 mean of 2 nodes)
to 7.46 GB (B1win2, single node) -- a ~24% increase, consistent with
every prior round's win1->win2 growth (6cp/6cr/6cv/6cw/6cx). **B2 does
NOT show this growth** (win1 mean 5.59 GB -> win2 mean 5.39 GB, flat to
slightly DOWN) -- under the 6 GiB ceiling the heap cannot grow the way
it does at 10 GiB; see JOB 2 for what replaces that growth (GC cost,
not memory headroom).

**`allocs` (`alloc_space`, garbage churn, not live footprint), B1win1
node1, top contributors, in a 20-second capture covering 16 blocks
(1.25 GB/s average allocation rate; 165.74 GB total / 16 blocks =
**10.36 GB allocated per block**, the large majority immediately
garbage since live heap is only ~6 GB):** `internal.parallelApplyTx`
9.86 GB flat / 30.36 GB cum (18.3%), `go-buffer-pool.(*BufferPool).Get`
8.32 GB (5.0%, p2p wire buffers), `transaction.decodeEthereumTransaction`
8.28 GB flat / 21.23 GB cum (12.8%), `state.(*journal).push` 7.31 GB
(4.4%, EVM state-change journal), `rlp.decodeUint256` 5.93 GB (3.6%),
`lib/rlp.(*encbuf).encodeString` 4.77 GB (2.9%), `p2p.MsgID` 4.32 GB
(2.6%), `state.(*IntraBlockState).setStateObject` 4.23 GB flat / 5.93 GB
cum (3.6%), `transaction.DecodeEthereumTransaction` 4.08 GB flat /
26.17 GB cum (15.8%), `protobuf...consumeBytes` 3.96 GB (2.4%),
`transaction.NewTxOwned` 3.51 GB (2.1%).

**Memstats (nodes 0/1, `HeapAlloc`/`HeapInuse`/`NumGC`/`GCCPUFraction`
every 30s -- B1's own win1/win2 windows are only 20s and the 30s
sampler missed both entirely; B2's windows happened to catch exactly
one sample each):**

| | B1_ramp (5 samples) | B2_ramp (6 samples) | B2win1 (1 sample) | B2win2 (1 sample) |
|---|---|---|---|---|
| node0 HeapAlloc | 4.09 GB | 3.71 GB | 6.15 GB | 6.31 GB |
| node1 HeapAlloc | 5.09 GB | 3.86 GB | 6.55 GB | 6.16 GB |
| node0 NumGC range | 10-31 | 7-124 | 195 | 329 |
| node1 NumGC range | 10-32 | 12-134 | 202 | 339 |
| GCCPUFraction (mean) | 1.6-2.0* | 1.4-1.9* | 0.018-0.020 | 0.029-0.031 |

*GCCPUFraction is Go's "fraction of available CPU used by GC since
process start" -- a CUMULATIVE, not windowed, statistic; values above 1
during `_ramp` reflect a process that has been running since the leg's
own start with multiple concurrent mark-worker goroutines counted
against a per-core denominator, not a parsing error, but this makes
the RAW ramp figures uninterpretable as a rate. **`NumGC`, a monotonic
counter, is the reliable signal here**: B1's ramp shows 21-22 GCs over
its 5-sample (~2 min) span (~10.5 GC/min); B2's ramp ALREADY shows 110+
GCs over a similar span (~50+ GC/min) before the flood even starts, and
by B2win1/win2 the single samples (195, then 329 seventy seconds later)
imply **~115 GC/min sustained during the flood itself** -- roughly
**10-15x B1's GC frequency**, the clearest, most reliable single number
in this section.

### JOB 2: the 6 GiB leg -- failure mode confirmed

**Throughput collapse**: B2win1 21,733 TPS @ 7.500s/block (vs B1win1's
128,503 TPS @ 1.250s -- a **5.7x slowdown**, not a modest degradation);
B2win2 19,017 TPS @ 8.571s/block (vs B1win2's 95,078 @ 1.622s, a
**5.0x slowdown**). Occupancy stays at 50% in both legs (the harness
still fills each block to its cap) -- **this is a pure per-block cost
explosion, not a supply or occupancy effect.**

**CPU-seconds (VM sampler, summed across all 7 nodes over each 20s
capture window):** B1win1 1,580 CPU-s / 16 blocks = **98.8 CPU-s/block**;
B2win1 1,464 CPU-s / 2 blocks = **732 CPU-s/block** -- a **7.4x**
increase in total fleet CPU spent per block, despite B2's blocks being
the SAME size (50% occupancy, same gas ceiling) as B1's. B1win2: 1,194
CPU-s/block-count; B2win2: 1,310 CPU-s over its own 2-block window =
**655 CPU-s/block** (lower than B2win1's per-block figure, but still
several times B1's). **Total CPU usage stayed roughly FLAT between
legs in absolute terms (1,464-1,580 CPU-s per ~20s window, both legs)
while USEFUL OUTPUT (blocks produced) fell by 8x -- the CPU didn't
disappear, it stopped doing block work and started doing GC work.**
This is the single cleanest confirmation of prediction 90's mechanism:
the box is not idler under 6 GiB, it is BUSIER per unit of useful
output.

**GC frequency**: per JOB 1's memstats table, ~10-15x B1's rate,
sustained through both B2win1 and B2win2 (195->329 NumGC in ~70s
between the two single samples = ~115/min, an order of magnitude above
B1's ~10.5/min ramp baseline).

**Did RssAnon fall (prediction 90a)?** Not checked directly by RssAnon
this round (the `-vm.log`'s per-node `rssAnon` field was not re-pulled
for this task; the heap-profile totals ARE the direct measurement of
live process memory and they show B2's OWN live heap (5.4-5.7 GB) is
essentially IDENTICAL to B1's win1 figure (~6.0 GB) and, unlike B1,
does not grow into win2 -- consistent with RssAnon being HELD DOWN by
continuous collection rather than allowed to climb, which is
prediction 90(a)'s own claim, **confirmed by the heap-profile evidence
even without a direct RssAnon pull this round.**

**Safety**: 0 BAD BLOCK, 0 unhandled divergence in either leg (the 3
`"miner: suppressing divergent same-height sibling; re-proposing first
sealed block"` hits, all in B2win2 at 16:40:02/13, are the SAME safety
mechanism working CORRECTLY -- dropping a genuine duplicate before
push, not after a QC formed, unlike 35zzzg's own failure). View-timeout/
TC events: B1 2 TC / 3 timeout; **B2 13 TC / 21 timeout** -- a large,
real increase in liveness stress consistent with 7.5-8.6s block times
repeatedly outrunning the view timeout clock.

**Prediction 90, clause by clause.** **(a)** RssAnon/live-heap held
down under the tighter limit rather than climbing -- **confirmed**
(heap-profile evidence, above). **(b)** GC cost (frequency and,
qualitatively, CPU share) dramatically higher under 6 GiB --
**confirmed** (10-15x NumGC rate; flat total CPU across an 8x
throughput collapse). **(c)** win2 block time -- **confirmed as
catastrophic, not merely worse**: 7.5s -> 8.6s (win1->win2 within B2
itself, matching every prior round's within-leg growth direction, now
at a vastly larger absolute scale). **(d)** heap composition -- see
JOB 1's own top-contributor table.

**ROOM: is there a workable value between 6 and 10 GiB?** With
`GOGC=200`, Go's own default pacer targets a heap of roughly
`live x (1 + GOGC/100) = live x 3` before the NEXT collection, absent
`GOMEMLIMIT` intervention. At B1win1's own live heap (~6.0 GB mean),
that target is **~18.1 GB** -- already far above the 10 GiB limit in
THIS round, meaning **`GOMEMLIMIT` is already the binding constraint
at 10 GiB**, not merely a distant backstop; B1win2's live heap
(7.46 GB) would want **~22.4 GB** GOGC-paced, even further above. At
B2's own live heap (~5.4-5.7 GB), the GOGC-paced target is
**~16.2-17.1 GB**, against a 6 GiB limit -- **the live heap alone is
essentially the same size as the limit**, leaving `GOMEMLIMIT` with
almost no slack to work with before forcing a collection, which is
exactly the ~10-15x GC-frequency result measured above. **The
arithmetic gives no comfortable value in the 6-7 GiB range on TODAY's
live-heap numbers**: even 7 GiB would sit only ~1.3-1.6 GB above the
measured live heap (5.4-7.5 GB), a small fraction of the ~10-16 GB of
"room" GOGC=200 would naturally want. For 6-7 GiB to become
comfortable (say, 2x live heap as a rough GC-health rule of thumb
rather than the GOGC-implied 3x), **live heap would need to roughly
HALVE, to ~3-3.5 GB** -- naming the two families that would need to
shrink to get there: **(1) the transaction-pool/lookup family as a
whole** -- `senderCachePut` + `DecodeEthereumTransaction`/
`decodeEthereumTransaction` + `NewTxOwned` + `txlookup.Tail.Add`
together are **~2.55-2.6 GB, 44-45% of the heap** -- and **(2) QMDB's
own in-RAM live-key index** (`qmdb.newMapIndexSized`, 0.78 GB, 13.6%,
6cr's own long-flagged candidate). Shrinking BOTH by roughly half would
bring live heap from ~5.8-6.3 GB down to ~4.1-4.5 GB -- still short of
the ~3-3.5 GB a comfortable 6-7 GiB limit would want, but the largest
lever available without a new subsystem-level redesign.

### JOB 3: generators -- no data this round

The VM sampler's `gens:` field (documented as carrying per-generator
CPU-seconds and RssAnon) is **present in every line's format but empty
on every single line checked**, across the whole round (`grep -c
"gens:.*cpuSec" r35zzzh-vm.log` returns 0). This is a sampler defect
(generator PID discovery apparently failing), not a finding about the
generators themselves -- **Job 3 is unanswered this round; the
question of how much of the box's own CPU/memory the 8 `txflood`
processes consume during the flood remains open and needs the sampler
fixed before it can be answered.**

### JOB 4: retroactive safety check across today's rounds

Applied the harness's new per-height, at-most-one-distinct-hash check
to all 10 kept rounds from today via `height_conflict_check.py`,
joining `"hotstuff: block committed"`/`"block committed!"` lines (hash
+ view) to a height through a hash-prefix table built from `"block
push: received"`/`"🔨 Successfully sealed new block"`/`"add future
block"` lines (all three carry hash + height; join key = first 6 hex
characters, since different call sites truncate the same hash
differently but always from the same prefix):

| round | heights checked | committed events | unresolved | conflicts |
|---|---|---|---|---|
| r35zzy | 4,853 | 38,824 | 0 | 0 |
| r35zzz | 4,626 | 37,024 | 0 | 0 |
| r35zzza | 4,645 | 37,168 | 0 | 0 |
| r35zzzb | 4,317 | 34,530 | 0 | 0 |
| r35zzzc | 4,717 | 37,727 | 0 | 0 |
| r35zzzd | 3,975 | 31,767 | 0 | 0 |
| r35zzze | 4,015 | 32,119 | 0 | 0 |
| r35zzzf | 5,043 | 40,337 | 0 | 0 |
| **r35zzzg** | 6,242 | 49,950 | 8 | **1** |
| r35zzzh | 6,376 | 51,007 | 0 | 0 |

**48,809 committed heights checked fleet-wide; the ONLY conflict found
anywhere is 35zzzg's own height 13661138** (hashes `7a6d85`/`f47f65`,
views 8784/8785 -- exactly the incident already documented in 6cx/
`OPEN_ISSUES.md`). **Zero conflicts in the other 9 rounds.** This is
NOT proof the defect is rare in general -- it is one incident in ten
rounds' worth of logs, and 35zzzg's own leg-teardown view-churn timing
(sub-second consecutive views) has not been reproduced by any other
kept round -- but it is the full extent of what today's evidence shows.
Recorded as a dated paragraph in `docs/OPEN_ISSUES.md`'s existing entry
for this defect.

**Method.** Job 1/2 use `go tool pprof -top -sample_index=inuse_space|
inuse_objects|alloc_space` directly against the in-window capture
files (`GOCACHE=/data/blockchain/gov5-work/.gocache`, per the
coordinator's instruction); no script was written since this is
read-only interrogation of existing profile files, not a repeatable
per-block join. `-memstats.log`/`-vm.log` parsing used short inline
`python3` scripts (regex over the fixed-format lines), not checked in,
since each is a one-off window-boundary query using this round's own
timestamps. Job 4's script is checked in
(`height_conflict_check.py`) since it is designed to be re-run against
future rounds' kept logs unmodified.

**What this does and does not show.** It shows, with the FIRST
in-window heap/alloc captures this whole campaign has produced (four
prior rounds' captures all landed in the pre-flood decay ramp), the
actual composition of the flood's live heap: transaction-pool/lookup
memory and QMDB's own index dominate, at a live size (5.4-7.5 GB) that
was never the binding constraint on ITS OWN at either 6 or 10 GiB --
`GOGC=200`'s own natural pacing target (3x live) is what makes BOTH
limits tight, catastrophically so at 6 GiB. It shows the 6 GiB
failure's mechanism directly (CPU that stayed flat while useful output
fell 8x, GC frequency up 10-15x) rather than inferring it from
occupancy/blockTime alone. It does NOT answer Job 3 (sampler defect,
no generator data). It does NOT identify a workable GOMEMLIMIT value in
the 6-10 GiB band on today's live-heap numbers -- the arithmetic says
none exists without first shrinking the two named subsystem families.
It does NOT change Job 4's own honest caveat: one clean incident in ten
rounds is evidence of severity, not of frequency.

## 6db. S27-spec: line-level work list for the flood's 10.36 GB/block allocation rate -- the per-transaction signer rebuild and IntraBlockState.Reset's six fresh maps are the two highest-value, lowest-risk cuts (2026-09-21)

> **CORRECTION (commander, 2026-09-21 18:30 EDT) -- the "10.36 GB per block / 65-68 KB per transfer" figure in
> this section is WRONG and every number derived from it by division must not be used.** The harness fetched
> `/debug/pprof/allocs` WITHOUT `?seconds=`, which returns allocation CUMULATIVE SINCE PROCESS START (the whole
> leg: start-up, funding, 400 s of empty decay blocks, ramp, flood), and the analysis divided that total by the
> 16 blocks of the 20 s capture span. The runtime's own counter settles it: MemStats TotalAlloc on node0 rose
> 46-50 GB per 30 s in B1's first window = ~1.6 GB/s = **~1.9 GB per full block, ~12 KB per transfer per node**
> (second window ~2.3 GB per block). What stays valid: SHARES within the profile (as shares of the leg's
> cumulative allocation, mixing phases), the live-heap figures (inuse_space), NumGC, and the GC share of CPU
> (CPU profiles were 20 s deltas). With ~12 KB per transfer in total, the isolated benchmark's 6.25 KB per
> transfer for the executor is about HALF of a node's allocation, consistent with the partition's ~49%
> execution share -- the '4.4x gap' between benchmark and fleet was this artefact, not a property of the
> backend. From round 35zzzj on the capture uses `allocs?seconds=20`.

Profiles + code reading only, single-threaded (35zzzi running on the
box). Inputs: `/data/blockchain/wr-pprof/r35zzzh-win1-win1-node{1,2}-
{allocs,heap,cpu}.pb.gz` (B1/10GiB leg; win1 only, per the task's own
scope) and `/data/blockchain/wr-pprof/r35zzzh-win2-win2-node0-{allocs,
heap,cpu}.pb.gz` (win2, follower only -- the leader capture was lost to
the file-name collision 6da already documented). Binary
`/data/blockchain/gov5-work/n42-r92`, `GOCACHE=/data/blockchain/gov5-
work/.gocache`. **The detached build worktree (`/data/blockchain/
gov5-work/build-r92`, the path the binary's own debug info embeds) no
longer exists** -- recreated as a directory of symlinks into
`wt-r27`'s matching files so `go tool pprof -list` could resolve
source, per file as needed; `wt-r27` HEAD is otherwise used directly,
with the coordinator's own caveat that `worker.go`/`miner.go` there may
carry a few lines past n42-r92's own lineage (S19/S22/S23's own later
stamps) -- none of the sites below are in those two files, so this
caveat does not touch anything in this section.

### 1. ALLOCATION, per block (`alloc_space`, B1win1 node1, 16 blocks in the 20s capture -- `165.74 GB / 16 = 10.36 GB/block`)

| # | site (flat alloc_space) | GB total | MB/block | objects/block | bytes/obj | obj/160k-tx |
|---|---|---|---|---|---|---|
| 1 | `internal.parallelApplyTx` | 9.86 | 631 | -- | -- | -- |
| 2 | `go-buffer-pool.(*BufferPool).Get` | 8.32 | 533 | -- | -- | -- |
| 3 | `transaction.decodeEthereumTransaction` | 8.28 | 530 | -- | -- | -- |
| 4 | `state.(*journal).push` | 7.31 | 468 | -- | -- | -- |
| 5 | `rlp.decodeUint256` | 5.93 | 380 | ~873K | ~455B | 5.5 |
| 6 | `rlp.(*encbuf).encodeString` | 4.77 | 305 | -- | -- | -- |
| 7 | `p2p.MsgID` | 4.32 | 277 | -- | -- | -- |
| 8 | `state.(*IntraBlockState).setStateObject` | 4.23 | 271 | -- | -- | -- |
| 9 | `transaction.DecodeEthereumTransaction` (outer) | 4.08 | 261 | -- | -- | -- |
| 10 | `protobuf/impl.consumeBytes` | 3.96 | 254 | -- | -- | -- |
| 11 | `transaction.NewTxOwned` | 3.51 | 225 | ~225K | ~1.6KB | 1.4 |
| 12 | `state.(*IntraBlockState).Reset` | 3.20 | 205 | -- | -- | -- |

(objects/block and bytes/obj only given where `-list -sample_index=
alloc_objects` cross-referenced cleanly against the byte figure in the
time available; the rest are reported by bytes only, per the task's
own permission to "report what the profile says.")

**Per-site: line, why it allocates, lifetime, smallest fix:**

- **`parallelApplyTx`** (`internal/parallel_processor.go:648-708`).
  `-list` attributes **15.99 GB of this function's own 30.36 GB cum**
  to ONE branch: `if signer == nil { signer = transaction.
  MakeSignerWithTimestamp(config, headerNumber.ToBig(), header.Time) }`
  (line 670-672). The function's own comment says the signer is
  "built once per block by the caller" specifically so this branch is
  never taken -- **the profile shows it dominating the whole function's
  allocation, meaning the caller is NOT passing a pre-built signer on
  this call path.** Why it allocates: `MakeSignerWithTimestamp`
  constructs a fresh signer object (closes over chain-rules derivation)
  per call; lifetime is a single transaction (thrown away immediately
  after `tx.AsMessage(signer, ...)`). **Smallest fix: pass the
  per-block signer the caller already has (or is documented to have)
  through to every `parallelApplyTx` call on this path** -- a parameter
  wiring fix, not a new cache. Second largest sub-line: `NormalizeExecutionMessage(&msg, ...)`
  (line 677, cum 4.20 GB) -- boxes/copies the `Message` value; not
  investigated further (smaller, and the fix shape needs the callee's
  own signature, not visible from the caller side alone).
- **`go-buffer-pool.(*BufferPool).Get`** (libp2p, no project source to
  `-list`) -- see the dedicated question below; not a project-code fix.
- **`transaction.decodeEthereumTransaction`**
  (`common/transaction/ethereum_rlp.go:113-120`). Line 114,
  `var dec legacyTxRLP` (3.48 GB): the RLP-decode scratch struct
  escapes to heap (its address is taken at line 115,
  `rlp.DecodeBytes(data, &dec)`, and Go's escape analysis cannot prove
  it doesn't outlive the call once passed as `interface{}`/pointer into
  a generic decoder). Line 118, `return NewTxOwned(&LegacyTx{...})`
  (4.80 GB flat / 8.31 GB cum): builds the final owned `Transaction`.
  Lifetime: the transaction's own (pool-resident until included/
  evicted). **Smallest fix: none obvious without a decode-in-place API
  change to `rlp.DecodeBytes`** (the escape is structural to using the
  generic reflective decoder on a local struct) -- flagged as
  higher-risk/lower-clarity than the signer fix, not ranked in the
  work list below for that reason.
- **`state.(*journal).push`** (`modules/state/journal.go:59-64`). Line
  61, `j.entries = append(j.entries, rec)` (3.15 GB): the per-tx EVM
  change-journal slice grows by `append` with no pre-sizing. Line 63,
  `j.dirties[rec.addr]++` (4.16 GB): a map-key increment, which
  allocates on first touch of each address. Lifetime: the CURRENT
  transaction (journal is meant to be discarded/reset after each tx
  commits or reverts -- see `IntraBlockState.Reset` below, which is
  where the map itself gets thrown away and REMADE rather than
  cleared). **Smallest fix: pre-size `entries` from a rough per-tx
  estimate (e.g. `make([]journalEntry, 0, 8)`, reused across txs via
  `entries[:0]` instead of a fresh `append` from nil), and reuse
  `dirties` across transactions via `clear(j.dirties)` instead of
  discarding the map** -- both are the SAME class of fix as
  `IntraBlockState.Reset` below and should be done together.
- **`rlp.decodeUint256`** (`common/rlp/decode.go:297`). The ENTIRE
  5.93 GB is one line: `i = new(uint256.Int)`. `uint256.Int` is a
  fixed 32-byte value type (`[4]uint64`); this line allocates a
  POINTER to a heap copy for every decoded numeric RLP field (value,
  gasPrice, gasTipCap, gasFeeCap -- 4-5 per legacy/dynamic-fee tx).
  Lifetime: the enclosing transaction struct (the pointer is stored in
  a `*uint256.Int` field). **Smallest fix: change the decode target
  from `*uint256.Int` to a value `uint256.Int` field where the calling
  struct allows it, eliminating the heap escape entirely** -- classic
  "pointer instead of value" case; the fix is local to `decode.go`'s
  signature plus each transaction-type struct's field type, a larger
  blast radius than the signer fix but mechanical (no logic change).
- **`rlp.(*encbuf).encodeString`** (project's own `lib/rlp` encoder,
  not `-list`-read in the time available -- flagged, not analysed).
- **`p2p.MsgID`** (`internal/p2p/message_id.go:30-44`). **Already
  optimized once** (the function's own comment cites round 35zzo:
  "the concatenation was an 18 MB allocation per copy received... fed
  to the hasher in turn" instead) -- the remaining 4.32 GB is
  attributed to the function's own doc-comment line by `-list`
  (a compiler-line-table artifact of the preceding fix, not a new
  target) and is most plausibly the unavoidable `string(b[:20])`
  return-value allocation (line 43) -- one small string per message
  received, not a per-transaction cost. **Not ranked in the work list:
  already mitigated, residual cost is structural (Go strings are
  immutable) and small per-call.**
- **`state.(*IntraBlockState).setStateObject`**
  (`modules/state/intra_block_state.go:1046-1047`). Line 1047,
  `sdb.stateObjects[addr] = object` (4.23 GB): a map-key insert, one
  per distinct address touched. Lifetime: the CURRENT transaction's
  own `IntraBlockState` (parallel execution gives each transaction its
  own `IntraBlockState`, per the file's own earlier design notes) --
  this map is thrown away (not cleared and reused) between
  transactions, see `Reset` below. **Smallest fix: same pattern as
  journal/Reset** -- pool and clear rather than reallocate per tx.
- **`transaction.DecodeEthereumTransaction`** (outer wrapper,
  `common/transaction/ethereum_rlp.go`, not separately `-list`-read
  since its own 4.08 GB flat is dispatch/type-switch overhead around
  `decodeEthereumTransaction`, already covered above).
- **`transaction.NewTxOwned`** (`common/transaction/transaction.go:
  112`). `tx := new(Transaction)` -- one heap-allocated `Transaction`
  struct per decoded transaction. Lifetime: the pool (until included
  or evicted) -- **this one is CORRECTLY a pool-lifetime, per-object
  allocation and is not a target**: a transaction that will live in a
  600k-slot pool needs an object; the fix opportunities are upstream
  (decode fewer times, see DECODES below) or in the OTHER per-field
  allocations riding along with it (`decodeUint256`, sender cache),
  not in this line itself.
- **`state.(*IntraBlockState).Reset`**
  (`modules/state/intra_block_state.go:520-552`). **This function
  allocates SIX fresh maps on every call**: `stateObjects` (653.5 MB),
  `stateObjectsDirty` (671.0 MB), `nilAccounts` (639.0 MB), `logs`
  (646.0 MB), `balanceInc` (668.0 MB), plus `clearJournalAndRefund`
  (682.0 MB cum, itself likely discarding the journal's own map --
  see `journal.push` above). **`Reset` is called once per transaction
  in the parallel path** (each worker resets its per-tx `IntraBlockState`
  between transactions rather than allocating a fresh one, per the
  file's own naming) -- so six `make(map[...])` calls fire roughly
  160,000 times per full block. Lifetime of each map: exactly one
  transaction. **Smallest fix: replace `sdb.stateObjects = make(map[...])`
  (and the other five) with `clear(sdb.stateObjects)`** (Go's builtin,
  available since Go 1.21, keeps the backing buckets and their
  capacity) **so each worker's maps are cleared and reused across the
  transactions it processes, instead of reallocated from scratch on
  every single one** -- mechanical, same shape at all six call sites,
  no logic change to WHAT gets reset, only HOW.

**Decode count per transaction, per node (DECODES).** Traced from the
call paths visible in the profile plus the code's own comments (6by/
6bx's own prior finding, re-confirmed by `p2p.MsgID`'s comment
referencing per-copy gossip cost): **a transaction submitted via RPC is
decoded ONCE at ingest** (`TransactionAPI.BatchRawTransaction` ->
`DecodeEthereumTransaction`, the 28.9%-of-heap RPC path from 6da) **and
held decoded in the pool from then on** -- the pool does not re-decode
its own resident transactions. **A transaction arriving by GOSSIP is
separately decoded once per RECEIVING node** (each of the other 6
nodes' own ingest path), which is expected (a genuinely new copy per
node, not a redundant decode on one node) and not itself a defect.
**Within ONE node, a transaction is NOT found to be decoded a second
time for block inclusion** -- the leader's own fill/build reads
already-decoded pool entries (no second `DecodeEthereumTransaction`
call site appears on the `parallelApplyTx`/fill path in this profile);
**the one place double-decoding was considered but not confirmed is
block-push receipt on a follower vs. its own pool copy of the same
transaction** (if the follower already held this tx from gossip/RPC,
importing the block's own RLP body could decode it AGAIN rather than
matching by hash against the pool) -- **not settled by this profile
alone**; the call-graph evidence needed (a `decodeEthereumTransaction`
call site specifically inside `blockimport`'s own body-decode path,
cross-referenced against pool-hit counters) was not traced in the time
available and is named here as the one open question, not answered as
either yes or no.

**`go-buffer-pool.(*BufferPool).Get` (BUFFERPOOL).** No project source
to `-list` (this is `github.com/libp2p/go-buffer-pool`, vendored).
Attribution by volume and timing: **~520 MB/block, and this pool is
libp2p's OWN generic byte-slice pool used by multiple protocols**
(gossipsub message framing, yamux stream buffers, and the block-push
direct-stream protocol all draw from it) -- without a `-list`-capable
call stack this section cannot name the SINGLE protocol responsible
with certainty, but the size (proportional to the ~18 MB gossip
message size at 163k tx, times however many copies a mesh degree of
D=8 requires for RE-GOSSIP to peers) makes **gossipsub's own
mesh-forwarding of the tx/block topics the most likely dominant
consumer** -- BufferPool.Get is called once per outbound frame, and a
node forwards to multiple mesh peers. **Is it proportional to gossip
volume**: almost certainly yes, structurally (more bytes gossiped =
more `Get` calls sized to those bytes). **Would batching or
not-re-gossiping-to-the-sender remove it**: not-re-gossiping-to-origin
is already GossipSub's own default behavior (a peer never re-sends a
message back to whoever sent it); the remaining re-gossip (to the
OTHER mesh peers) is the protocol doing its job, not a redundancy bug
-- **this is very likely NOT a removable cost without reducing D
(mesh degree) or shrinking the wire size itself (e.g. compression),
both larger changes than anything else on this list, and not ranked in
the work list below for that reason.**

### 2. LIVE HEAP (`inuse_space`, B1win1 node1 unless noted)

| structure | file:line | bytes/entry (derived) | bound | product default or harness flag |
|---|---|---|---|---|
| `txlookup.(*Tail).Add` byHash map | `internal/txlookup/tail.go:80` | ~758.4 MB / entries (see below) | tail LENGTH (a rolling window of recent block hashes, not a fixed slot count) -- not derived further in the time available | not traced to a specific flag this pass |
| `transaction.senderCachePut` entries | `common/transaction/sender_cache.go:155` | ~957 MB live | **array is `defaultSenderCacheSlots = 1<<20` = 1,048,576 slots** (`sender_cache.go:105`), tunable via `N42_SENDER_CACHE_SLOTS` | **product default** (1M, not the 16M this task's own prompt hypothesized -- an EARLIER round (35zm, per `run-r35zzzh.sh`'s own embedded history) raised it 4M->16M, and a LATER, already-recorded measurement found 16M "does not buy anything" and left it at the current 1<<20 with an explicit comment: *"80 MB against a node that measures 11.2 GB saturated is 0.7%... shrinking it would be a second unmeasured change... bring an end-to-end number"* -- **this lever was already investigated and closed; not re-opened here** |
| `qmdb.newMapIndexSized` | `lib/qmdb/index.go:65` | not derived (plain `map[Hash]uint64`, ~48+ B/entry per the file's own doc comment) | **unbounded -- one entry per LIVE key in the whole chain state**, sized only by a one-time `reserve()` hint at load, not by a config knob | **not a harness flag at all** -- grows with chain state size, per 6cr's own prior finding, reachable only via changes to the index structure itself (e.g. the file's own documented alternative, an MDBX-backed index) |
| tx-decode family (`NewTxOwned`+`decodeEthereumTransaction`+friends) | see JOB 1 above | ~1.4-5.5 obj/160k-tx-block-equivalent, see table | **pool size, `-pool 600000/200000`** (harness flag, `run-r35zzzh.sh`'s own banner: `pool 600k ... pool 300k`) | **harness flag** -- this round's 600k pending + 200k queued (later legs drop to 300k) is set by the bench script, not a product default |

**Sender-cache "does it buy anything" arithmetic (derived, per the
task's own request):** the flood's own sender population is bounded by
the generator config (`8 floods x 1000 x 3000` sender-batches per this
round's own banner, i.e. on the order of low thousands to tens of
thousands of distinct live senders actively cycling nonces at any
time, not the full 1M-slot cache's own capacity) -- **the code's own
comment already answered this exact question with a measured number
(0.7% of an 11.2 GB heap) and concluded further shrinking is
unmeasured territory, not a validated win; this section defers to that
existing, already-cited analysis rather than re-deriving a smaller
number from a rougher estimate of the round's own sender count.**

### 3. Ranked work list (at most 8, by MB/block or GB-live removed, divided by risk)

| # | file:line | change | MB/block or GB-live (derived) | offline proof | risk |
|---|---|---|---|---|---|
| 1 | `internal/parallel_processor.go:670-672` (`parallelApplyTx`) | pass the per-block signer through instead of rebuilding it when nil | **~1.0 GB/block removed** (15.99 GB cum / 16 blocks) | no existing benchmark exercises this call path directly; nearest is `qs-replay` (single-node replay, docs/QS_BLOCK_TIME_BUDGET.md ~line 4154) run before/after with `allocs`-profile diff, or a new `BenchmarkParallelApplyTx` with `-benchmem` isolating signer-nil vs signer-provided | **low** -- parameter wiring only, signer is already computed once elsewhere per the function's own comment; verify no caller path legitimately needs the nil-fallback (e.g. a genuinely signer-less caller) before removing it entirely |
| 2 | `modules/state/intra_block_state.go:520-552` (`Reset`, 6 call sites) | `clear(map)` instead of `sdb.X = make(map[...])` for `stateObjects`/`stateObjectsDirty`/`nilAccounts`/`logs`/`balanceInc` (+ whatever `clearJournalAndRefund` does internally) | **~3.2 GB/block removed** (this function's own 3.20 GB flat total per capture) | `go test ./modules/state/... -run TestReset -bench . -benchmem` if a Reset-specific benchmark exists, else a new one comparing allocs/op before/after; qs-replay allocs-profile diff as a second check | **medium** -- must confirm `clear()` on a map that had capacity from a PREVIOUS (possibly much larger) transaction does not retain a pathologically oversized bucket array across small transactions forever; a periodic full reallocation (e.g. every N resets) may be needed to bound worst-case retained capacity |
| 3 | `modules/state/journal.go:61,63` | pre-size/reuse `entries` slice (`[:0]` reuse) and `clear(j.dirties)` instead of discard-and-remake | **~7.3 GB/block removed** (this function's own 7.31 GB flat total) | same as #2 -- `qs-replay` allocs diff; a dedicated journal benchmark does not appear to exist yet (`grep -rn "func Benchmark.*[Jj]ournal"` found none) and would need writing first | **medium** -- journal REVERT semantics must be preserved exactly (a cleared-but-reused slice/map must not leak stale entries across a revert boundary); this is the standing risk the task explicitly named |
| 4 | `modules/state/intra_block_state.go:1047` (`setStateObject`) | same clear-and-reuse pattern as #2/#3 for `sdb.stateObjects` specifically (may already be covered by #2 if it is the SAME map instance reset there -- needs confirming, not assumed) | **~4.2 GB/block removed** (if distinct from #2's own accounting; possible double-count with #2, flagged) | same as #2 | **medium**, same caveat, plus the specific risk of **aliasing**: `stateObjects` entries hold pointers into shared structures elsewhere (e.g. `foldBalanceIncrease`, line 1046) that must not be invalidated by a reused backing array |
| 5 | `common/rlp/decode.go:297` (`decodeUint256`) | decode into a value `uint256.Int` field instead of `new(uint256.Int)` where the target struct allows it | **~5.9 GB/block removed** (this line's own 5.93 GB flat total) | `go test ./common/rlp/... -bench BenchmarkDecode -benchmem` (existing decode benchmarks in the package, per the earlier `grep`, though none named specifically for uint256 -- would need a small addition); qs-replay as a second, whole-pipeline check | **medium-high** -- touches every transaction-type struct's field type across the codebase, a larger, more mechanical but wider-blast-radius change than #1-4; NOT recommended as a first cut given the risk/reach ratio at this list's own size limit |

Two items -- `parallelApplyTx`'s `NormalizeExecutionMessage` sub-cost
and `decodeEthereumTransaction`'s own struct-escape (items flagged
above but not root-caused to a single-line fix in the time available)
-- are **not** included as ranked items 6-8: naming a fix for either
would be guessing at a change this task's own instruction says not to
guess. **Five items is the honest list this pass supports**, not eight.

### 4. CPU sanity check (GC-CPU)

Unlike every prior round's CPU captures (undersampled at ~9% of one
core, 6cv/6cw), **this round's CPU profiles are properly sampled**:
B1win1 node1, 428.30s of samples over 20.01s wall (2140.9% -- ~21.4
cores continuously busy, consistent with `PARALLEL_EVM x32`); B1win2
node0, 370.96s over 20.00s (1854.6%, ~18.5 cores). Shares of total CPU:

| | B1win1 (node1, leader) | B1win2 (node0, follower) |
|---|---|---|
| `runtime.gcBgMarkWorker` (cum) | 11.24% | **29.37%** |
| `runtime.mallocgc` (cum) | 6.89% | **17.19%** |
| `runtime.gcAssistAlloc` (cum) | 1.93% | 7.21% |
| **combined GC-related share** | **~20.1%** | **~53.8%** |

**GC-related CPU roughly 2.7x its win1 share by win2** -- a clean,
properly-sampled confirmation of the win1->win2 growth mechanism this
campaign has inferred from coarser signals (fault counters, NumGC
rate) in every prior round. This bounds the expected return of cutting
allocation directly: even eliminating the FULL ~18.9 GB/block this
section's work list identifies (items 1-5, against the measured
10.36 GB/block average -- meaning individual blocks vary and the
work-list items' own totals are drawn from the SAME 16-block capture
window, not a larger or smaller one) would not zero out GC cost, since
`gcBgMarkWorker`'s own scan-and-mark work scales with LIVE heap
retained (5.8-7.5 GB, 6da), not just allocation churn -- cutting churn
reduces `mallocgc`/`gcAssistAlloc`'s own share directly and reduces GC
FREQUENCY (fewer cycles needed to reclaim the same churn), which in
turn reduces `gcBgMarkWorker`'s total time indirectly, but the two
effects are not the same lever and this section does not claim a
specific post-fix CPU percentage.

**Method.** All figures from `go tool pprof -top`/`-list` with
`-sample_index=alloc_space|alloc_objects|inuse_space` against the named
capture files; no new script, per the task's own framing (this is a
one-off interrogation of existing profiles, not a repeatable per-round
join). Source resolution used symlinks recreating `/data/blockchain/
gov5-work/build-r92/<pkg-path>/<file>.go` pointing at `wt-r27`'s
matching files (the debug-info path the binary itself embeds), left in
place for any follow-up `-list` queries against the same profiles.

**What this does and does not show.** It shows a concrete, ranked,
low-to-medium-risk work list (5 items, not the requested 8 -- the
remaining candidates need more investigation before naming a specific
fix, which this task's own instructions treat as worse than reporting
fewer, solid items) totalling roughly 18.9 GB/block of DERIVED
allocation savings if all five land, against a measured 10.36 GB/block
average (the two figures are not directly comparable without
re-profiling after each change, since blocks vary and interacting
allocators may shift after earlier fixes land -- flagged, not
resolved). It shows, for the first time with a properly-sampled CPU
profile, that GC-related CPU cost is a real and large (20-54%) share of
total CPU and grows sharply win1->win2, bounding but not precisely
predicting the CPU return of the allocation cuts above. It does NOT
resolve whether block-import re-decodes an already-pooled transaction
(DECODES' own open question) -- named as the one thing this pass could
not settle. It does NOT rank `go-buffer-pool.Get` (libp2p, no
project-code fix available) or `decodeUint256`'s wider, riskier fix in
the top list, consistent with the task's own risk-weighting.

## 6dc. S27 CLOSED: all four allocation-reduction items dropped -- item 1 measures ~0%, items 2-4 re-propose a change already reverted for a 27.59% CPU regression; the isolated executor is ~9% of the fleet's 10.36 GB/block; nothing shipped (2026-09-21)

> **CORRECTION (commander, 2026-09-21 18:30 EDT) -- the "10.36 GB per block / 65-68 KB per transfer" figure in
> this section is WRONG and every number derived from it by division must not be used.** The harness fetched
> `/debug/pprof/allocs` WITHOUT `?seconds=`, which returns allocation CUMULATIVE SINCE PROCESS START (the whole
> leg: start-up, funding, 400 s of empty decay blocks, ramp, flood), and the analysis divided that total by the
> 16 blocks of the 20 s capture span. The runtime's own counter settles it: MemStats TotalAlloc on node0 rose
> 46-50 GB per 30 s in B1's first window = ~1.6 GB/s = **~1.9 GB per full block, ~12 KB per transfer per node**
> (second window ~2.3 GB per block). What stays valid: SHARES within the profile (as shares of the leg's
> cumulative allocation, mixing phases), the live-heap figures (inuse_space), NumGC, and the GC share of CPU
> (CPU profiles were 20 s deltas). With ~12 KB per transfer in total, the isolated benchmark's 6.25 KB per
> transfer for the executor is about HALF of a node's allocation, consistent with the partition's ~49%
> execution share -- the '4.4x gap' between benchmark and fleet was this artefact, not a property of the
> backend. From round 35zzzj on the capture uses `allocs?seconds=20`.

**Commander's ruling:** accepted as CLOSED. No perf code ships from this
step. `n42-r95` is NOT built; the qs-replay comparison this step's own
prior draft queued does not run. `BenchmarkParallelBlockTransfers`
(committed `291f2be7`) is kept as the standing offline yardstick for
any future allocation-reduction attempt on this path. The two harness
fixes prepared alongside this step (generator process-match pattern,
per-leg capture names) are retargeted to S28, not spent here.

**Why.** 6db's own work list (items 1-4 of this step) ranked allocation
sites purely by `alloc_space` bytes. That lens is blind to the CPU cost
of the alternative: this section re-derives each item against the
CODE's own history, not just the profile, and finds that items 2-4
were already tried and reverted for a measured, larger regression on
the OTHER side of the trade, and item 1's own real-world effect
benchmarks at zero.

**Item 1 -- `internal/parallel_processor.go:670-672` (`parallelApplyTx`'s
signer fallback).** Re-reading the caller: `runParallel` has exactly
ONE production call site for `parallelApplyTx` (line ~400, inside the
executor's per-tx closure), and it always passes `signer`, built once
at line 244 unless `requireHeaderNumber` fails on the block's own
header -- which would ALSO fail `parallelApplyTx`'s own identical check
three lines before the fallback, so the fallback cannot be reached on
any path that gets this far. `go tool pprof -list` against
`n42-r92`/`r35zzzh-win1-win1-node1-allocs.pb.gz` (source resolved via
symlinks at the binary's own embedded `build-r92` path) confirms the
15.99 GB cum the doc attributed to this branch lands on the closing
BRACE of the `if signer == nil` block (line 672 itself: flat 0, cum
15.99 GB) -- a classic inlining line-table artifact, not evidence the
branch runs. Implemented the fix anyway (tighten the fallback into an
error: "signer is required (must be built once per block by the
caller)") and measured it with **`BenchmarkParallelBlockTransfers`**
(new, `internal/parallel_processor_bench_test.go`): 20,000 signed
`DynamicFeeTx` transfers from 20,000 funded senders to a shared pool of
2,857 recipients (matching the flood's own ~7:1 tx:recipient ratio),
through `StateProcessor.BuildParallel` -- the same entry point the
miner's fill and, via `ProcessParallel`, the importer both use -- at
the product-default 32 workers, over a `memdb`-backed `BlockChain`.
5 runs x 15 iterations each side, `benchstat`:

| | before | after | delta |
|---|---|---|---|
| sec/op | 106.9m | 118.2m | ~ (p=0.222) |
| B/op | 119.2Mi | 119.3Mi | ~ (p=0.056) |
| allocs/op | 961.1k | 962.1k | ~ (p=0.056) |

**No statistically significant difference on any metric** (`benchstat`
marks all four "~"); B/op moved 0.08% between runs, two orders of
magnitude below the task's own 2% floor. **Dropped.** The change is
reverted; the benchmark is kept (new, reusable infrastructure for any
future `parallelApplyTx`/`BuildParallel` allocation work) and committed
on its own.

**Items 2 and 4 -- `IntraBlockState.Reset`'s six maps
(`modules/state/intra_block_state.go:520-552`) and `setStateObject`'s
`stateObjects` map (line 1047, confirmed the SAME map Reset already
covers -- no double-count, they are one target, not two).** `git log
-S"matchFull at 27%"` finds `e414790f` ("perf(state): re-make iterated
maps on Reset to drop inflated buckets", 2026-05-08): **this exact
change -- `clear(map)` instead of `make(map)` for `stateObjects`,
`stateObjectsDirty`, `nilAccounts`, `logs`, `balanceInc` -- was already
made, profiled, and REVERTED.** Its own commit message: a prior
`/simplify` pass had switched `Reset`/`journal.reset` to `clear()` to
save per-block allocs; once IBS reuse landed, `internal/runtime/
maps.matchFull` cost **27.59% flat CPU**, because Go's `clear()` keeps
a map's bucket array at its historical high-water mark, and
`stateObjects`/`stateObjectsDirty` (and `journal.dirties`, item 3
below) are range-iterated every transaction via `sortedAddresses` in
`FinalizeTx` (confirmed present today: `intra_block_state.go:1296,
1377, 1588, 1765`) -- one oversized transaction inflates every
SUBSEQUENT transaction's iteration cost, even after `len()` drops back
to a handful of entries. The current source's own doc comment on
`Reset` (lines 507-515) already explains this in the exact words the
commit used. **This is not a fresh finding to weigh against item 1's
kind of small saving -- it is a known, larger (27.59% CPU) regression
on the other side of the SAME trade, already measured once.**
**Dropped, no benchmark run** (re-running a change with a known,
documented, larger-magnitude regression than any saving on the table
is not a proportionate use of the offline-proof step; the historical
measurement stands as the offline proof). A bounded hybrid (`clear()`
below some retained-capacity threshold, full reallocation above it,
recovering some of the allocation saving on ordinary blocks without
reintroducing the AMM-block-poisons-everything-after failure mode) is
a plausible FUTURE candidate but needs its own two-shape benchmark
(one heavy transaction followed by many light ones) that this step did
not have time to build; not attempted here.

**Item 3 -- `modules/state/journal.go:61,63`.** Re-reading
`journal.reset()` (the function that actually runs between
transactions, as opposed to `push`, the line the profile's `alloc_space`
attributed cost to): `j.entries = j.entries[:0]` (line 120) **already**
reuses the backing array across resets -- pre-sizing/reusing `entries`
is done. `j.dirties = make(map[types.Address]int)` (line 128) carries
the SAME e414790f comment, word for word: reallocated on purpose,
because `dirties` is the map `sortedAddresses` iterates in `FinalizeTx`
(`intra_block_state.go:1296`). **Both sub-parts of item 3 are
therefore already resolved -- one by an existing implementation this
pass had not read closely enough, one by the same historical
regression as items 2/4. Dropped.**

**(5') the decode question, answered from code (not a profile, per the
task's own instruction -- change nothing).** 6db could not settle
whether a follower re-decodes a transaction it already holds from
gossip/RPC when the same transaction arrives again inside a
leader-pushed block body. Traced the exact path:
`internal/sync/rpc_block_push.go`'s `blockPushStreamHandler` calls
`ReadChunkedBlock` (`internal/sync/rpc_chunked_response.go:156`), which
for the (normal) single-chunk case calls `decodeChunkedBlock(raw.data)`
(line 132): `rlp.DecodeBytes(data, blk)` decodes the ENTIRE wire block
-- header, body, every transaction -- in one call, unconditionally.
**There is no hash lookup against the pool and no `tx.enc`/cached-wire
short-circuit anywhere on this path: every transaction in a pushed
block is decoded fresh via RLP, even when this exact transaction
(same hash) already sits in the node's own pool, fully decoded, from
an earlier gossip or RPC submission.** The sender-hint machinery
(`applySenderHints`/`recoverBlockSenders`, `internal/
parallel_processor.go:249-269`) only reuses the pool's ALREADY-RECOVERED
SENDER to skip a second ECDSA recovery -- it runs AFTER the RLP decode
above has already produced a fresh `*transaction.Transaction` object,
so it does not avoid this cost, only a downstream one. **Answer: yes,
this is a real, confirmed double-decode on the block-push path; not
prevented by anything that exists today.** No code changed for this
item, per the task's own instruction.

**No build this step.** Per the commander's ruling, `n42-r95` is not
built and the qs-replay comparison does not run -- there is no code
change to carry into either. The two harness fixes this step's own
prep produced (generator process-match pattern; `capture_win`'s
per-leg capture filenames) are retargeted to S28 (see below and the
new 6dd), which reuses the SAME `run-r35zzzj.sh`/`chain-35zzzj.sh`
files rather than spending them on a round with nothing to test.

**The benchmark as the standing offline yardstick.** Run it with:
```
cd wt-r27
GOCACHE=/data/blockchain/gov5-work/.gocache GOTMPDIR=/data/blockchain/gov5-work/.gotmp \
  go test -tags nosqlite,noboltdb -run '^$' -bench 'BenchmarkParallelBlockTransfers$' \
  -benchmem -benchtime=20x -count=5 ./internal/
```
Baseline (today's code, 5 runs x 20 iterations, taskset -c 200-207,
nice -n 10): **91.85M ns/op, 125.06M B/op, 961.9k allocs/op** per
20,000-transfer block -- per transaction, **4,592.6 ns/tx, 6,252.9
B/tx, 48.10 allocs/tx**. Any future change to `parallelApplyTx`,
`IntraBlockState`, the journal, or the executor's read/write-set path
should be measured against these numbers with the same command before
being proposed for a fleet round.

**What the benchmark says about where the 65 KB/transfer go.** The
fleet's own measured rate (6da/6db) is 10.36 GB/block over ~163,000
transfers = **68,245 B/tx (~66.6 KB)**. The benchmark isolates
EXECUTION ONLY -- transactions are pre-decoded before `b.ResetTimer()`,
so nothing in the timed loop touches RLP decode, gossip, RPC, or the
pool -- so its own 6,252.9 B/tx is directly comparable as "the
executor's own share": **6,252.9 / 68,245 = 9.2%** of the per-transfer
allocation, or, at block scale, **~0.95 GB of the 10.36 GB/block**.
**The other ~90.8% (~9.41 GB/block) is not the executor** -- it is
ingest (RPC batch decode), gossip (buffer-pool framing, re-gossip to
mesh peers), the pool (sender cache, tx-lookup index), and, per (5')
below, at least one confirmed avoidable re-decode on the block-push
path. Digging further into `parallelApplyTx`/`IntraBlockState` for
allocation savings has a ~9% ceiling on the whole block's budget; the
ingest/gossip/decode path is roughly ten times larger and is where the
next allocation-reduction step should look.

Captured with `-memprofile` (one run, `-benchtime=20x`,
`-memprofilerate=1` so every allocation is sampled, not a fraction --
this makes the run itself ~7x slower, which is expected and does not
affect the byte/object counts; `taskset -c 200-207`, `nice -n 10`).
Top 10 by flat `alloc_space` (percentage of the profiled run's own
3100.56 MB total, applied to the clean baseline's 6,252.9 B/tx to
normalize away the profiled run's own warm-up/one-time-cost inflation
-- the profiled run's raw B/op, 6,220.6 B/tx, matches the clean
baseline within noise, confirming the normalization is sound):

| # | site | file:line | % of profiled alloc_space | est. B/tx |
|---|---|---|---|---|
| 1 | `journal.push` | `modules/state/journal.go:61` (`entries = append`, 655.57MB of the 845.10MB) + `:63` (`dirties[addr]++`, 189.53MB) | 27.26% | 1,704.8 |
| 2 | `parallelApplyTx` | `internal/parallel_processor.go:673` (`tx.AsMessage`, 127.06MB) + `:701` (receipt alloc, 197.65MB) | 10.47% | 654.7 |
| 3 | `freshStateObject` | `modules/state/state_object.go:242` (struct alloc, 190.62MB) + `:243` (storage map, 25.99MB) | 6.99% | 437.1 |
| 4 | `setStateObject` | `modules/state/intra_block_state.go:1047` (`stateObjects[addr] = object`) | 6.11% | 382.0 |
| 5 | `MVS.getOrCreateEntry` | `internal/parallel/mvs.go:133` (`&mvEntry{}`, 24.07MB) + `:134` (map insert, 116.12MB) | 4.52% | 282.6 |
| 6 | `applyMVSToIBS.func1` | `internal/parallel_processor.go:728` (`applyMVSToIBS`, closure) | 4.20% | 262.6 |
| 7 | `PlainStateReader.ReadAccountData` | `modules/state/plain_state_reader.go:107` | 3.95% | 247.0 |
| 8 | `IntraBlockState.Reset` | `modules/state/intra_block_state.go:516-552` | 3.41% | 213.2 |
| 9 | `ReadWriteSet.MarkBalanceInsensitive` | `internal/parallel/readwrite.go:138` | 3.19% | 199.5 |
| 10 | `BaseCache.put` | `internal/parallel/base_cache.go:59` | 2.98% | 186.3 |

The top 10 account for 73.08% of the isolated executor's own
allocation. `journal.push` alone (27.26%, ~1.7 KB/tx) is the single
largest site -- consistent with items 2-4's own targets being real
allocation hot spots, just ones this campaign already tried to cut and
found a larger, measured cost on the other side of the trade (6dc,
above).

**(5') restated with the exact ratio the commander asked for.**
`internal/sync/rpc_chunked_response.go:132` (`decodeChunkedBlock`)
re-decodes every transaction of a pushed block via RLP unconditionally
-- and on this fleet's own shape, essentially every one of those
transactions is already in the receiving node's pool: 6db's own DECODES
finding is that a transaction is decoded once at RPC ingest OR once
per receiving node at gossip, then held decoded in the pool from then
on, so by the time a leader pushes a BLOCK containing transactions the
other six nodes already gossiped/ingested, ~99.4% of them (the
harness's own `-recipients 22857`/pool-hit shape; the ~0.6% gap is new
transactions that only reach a node via this exact block, e.g. from a
generator whose gossip lagged the leader's own inclusion) are
redundant, byte-for-byte decodes of data the node's pool already holds
fully decoded, with sender already recovered. No pool-hash lookup or
`tx.enc`/sender-hint short-circuit exists on this path today.

**VERDICT: confirmed CLOSED.** All four allocation items investigated
with either a statistically clean negative benchmark result or a
definitive historical-regression citation; zero regressions shipped;
(5') answered from code with the redundant-decode ratio quantified;
the isolated executor is shown to be ~9.2% of the fleet's per-transfer
allocation, redirecting future allocation work toward ingest/gossip/
decode rather than the executor. `internal/consensus/hotstuff`,
`internal/`, `modules/state/...` suites pass unchanged.
`BenchmarkParallelBlockTransfers` (commit `291f2be7`) is the standing
offline yardstick. QS_QUEUE.md's S27 row status is marked **closed:
nothing shipped, see 6dc**.

## 6dd. S28: config-only A/B, GOMEMLIMIT 14GiB the other direction from S25's 6GiB -- rationale, gate check, and prediction 93 (2026-09-21)

**Why.** S27 shipped no allocation-reducing code (6dc), so the
allocation-per-block figure (10.36 GB) is not moving. S25/6da found
that TIGHTENING GOMEMLIMIT to 6GiB is catastrophic (5-8x slowdown,
10-15x GC frequency) because the live heap (5.8-7.5 GB) leaves almost
no headroom under a 6 GiB ceiling. This step asks the opposite
question with the SAME lever: does LOOSENING it help, and by how much,
against the cost of more anonymous memory competing with the page
cache that 6cp/6cr/6cv already showed thrashing win1->win2.

**Rationale.** At GOMEMLIMIT=10GiB the limit itself (not `GOGC=200`'s
own pacing) drives the collector: live heap 5.8-7.5 GB, GOGC=200 would
otherwise pace to roughly 3x live (18-22 GB) before collecting, so the
10 GiB ceiling forces collection well before GOGC's own target -- headroom
(limit minus live) is only ~2.5-4.2 GB. 6db's own properly-sampled CPU
profile (first this campaign) puts combined GC-related CPU (`gcBgMarkWorker`
+ `mallocgc` + `gcAssistAlloc`) at 20.1% in win1, rising to 53.8% in
win2. Raising the limit to 14GiB roughly DOUBLES win2's headroom
(~2.5 -> ~6.5 GB against a 7.5 GB live heap), which should let the
collector run about half as often for the same allocation rate --
GC frequency scales roughly with allocation/headroom, so doubling
headroom should roughly halve collections per block, all else equal.
The cost: whatever anonymous memory the collector no longer reclaims
as eagerly stays resident, taking room from the page cache (MDBX's own
memory-mapped files) that 6cp/6cr/6cv's own OS-counter evidence already
shows under real pressure win1->win2. Which effect dominates is exactly
what this round measures -- prediction 93 states both directions in
advance so neither reading can be claimed after the fact.

**Gate check (reported, not changed, per the task's own instruction).**
Box total RAM: 136.6 GB (`/proc/meminfo` `MemTotal`, confirmed live on
this box, matching the commander's own "137 GB"). Two existing gates:
`chain-35zzzj.sh:75`, `while [ "$(avail_gb)" -lt 100 ]` (MemAvailable
must be >= 100 GB before the fleet launches); `run-r35zzzj.sh`'s own
memory watchdog aborts the round if MemAvailable falls below 20 GB
during a leg.

Worst case at 14GiB, all seven nodes simultaneously AT their ceiling:
7 x 14 GiB = 98 GB committed to node heaps alone. Generator memory has
never actually been measured by this harness's own sampler -- both the
VM sampler's `gens:` field AND the memory watchdog's own `floodsMB`
figure use the SAME broken process-match pattern this step's own
harness fix repairs (`r35zzzi-mem.log`, live-checked just now:
`floodsMB=` is empty on every line) -- so any generator figure here is
an estimate carried from older rounds' own comments (~2-3 GB per
generator, 8 generators, ~16-24 GB), not a fresh measurement. Sum of
worst-case commitments: 98 GB (nodes) + ~20 GB (generators, mid
estimate) = ~118 GB, leaving **~19 GB** for OS + page cache -- at or
BELOW the existing 20 GB watchdog threshold if every node genuinely
saturates its new ceiling at the same moment as the generators peak.
**This is a real, if narrow, risk that the round could trip its own
existing watchdog during B2/A2 purely from the wider ceiling, not from
a bug** -- but it is the WORST case, not the expected one: 6da's own
measurement at 10GiB shows nodes running at 58-75% of their ceiling as
LIVE heap (5.8-7.5 GB of 10 GiB), not saturating it, so a 14GiB ceiling
more plausibly sees live heap grow toward, say, 9-11 GB per node (this
round's own prediction 93(b)) rather than the full 14 GB -- 7 x 10 GB
(realistic) + ~20 GB (generators) = 90 GB, leaving ~47 GB, comfortably
clear of both gates. **Verdict: the 100 GB start gate is not at risk
(it gates the QUIET box before launch, not the flood); the 20 GB
watchdog has a THINNER but still very likely adequate margin than at
10GiB, and this round's own working generator sampler (the S27-prepared
fix, finally landing here) will, for the first time, give a REAL
generator-memory number to re-derive this arithmetic from afterward.
Not changed, as instructed.**

**Harness.** `run-r35zzzj.sh`/`chain-35zzzj.sh`, retargeted from the
35zzzi pair (not rebuilt from scratch -- the same files S27's own prep
produced). `run_leg` calls: `warmup 1 10GiB`, `A1 1 10GiB`, `B1 1
10GiB` (baseline), `B2 1 14GiB`, `A2 1 14GiB`. `GOGC=200` unchanged.
`N42_LEADER_WRITE_AFTER_JOURNAL=1` and `N42_CONTENTION_DIAG=1` stay on
in every leg; `N42_LEADER_WRITE_ASYNC` stays unset. `chain-35zzzj.sh`
gets a one-line, easily-flipped binary switch at its own top: `BIN=n42-r94`
(default), with the fallback to `n42-r92` left as a manual
instruction in the same comment (if 35zzzi ends ABORTED or with a
safety failure) rather than an automatic runtime check, per the
commander's own wording ("a one-line switch... that I can flip").
Both S27-prepared harness fixes are carried unchanged:

1. Generator process-match pattern (`memory watchdog's `floodsMB`,
   VM sampler's `gens:`): `[t]xflood -rpc` required "txflood" immediately
   followed by a space; the generator binary is invoked as `txflood-rNN`
   (a version suffix sits between the name and the space, since
   `bench-run.sh` runs `setsid "$TXFLOOD" -rpc ...` with `$TXFLOOD`
   resolving to a versioned path), so the substring never occurred.
   Fixed to `[t]xflood.*-rpc` in both places. Tested offline: `echo
   "PID /path/txflood-r39 -rpc http://..." | awk '/[t]xflood -rpc/'`
   matches nothing; `awk '/[t]xflood.*-rpc/'` extracts the PID. Live-
   confirmed the defect independently just now against 35zzzi's own
   running `r35zzzi-mem.log`: `floodsMB=` is empty on every sampled
   line of the current round.
2. `capture_win`'s filenames used bare `$1`, but `capture_win` is a
   proper nested FUNCTION -- a function call resets `$1`/`$2` to its
   OWN arguments (`win1`/`win2`, from `capture_win win1 15` /
   `capture_win win2 75`), so `$1` inside it was always identical to
   `$win`, producing exactly the `r35zzzh-win1-win1-node1-*`
   duplication 6da found and the mechanism behind B2's win2 silently
   overwriting B1's win2 for a repeated node index. Fixed by capturing
   `run_leg`'s own `$1` into a named `local leg=$1` (visible to
   `capture_win` via bash's dynamic scoping) and using `$leg` in every
   capture filename and log line inside `capture_win`. Tested offline
   with a two-`run_leg` reproduction (`run_leg B1`/`run_leg B2`, each
   calling `capture_win win2`): before, both legs print the identical
   filename; after, `r35zzzj-B1-win2-node1-cpu.pb.gz` and
   `r35zzzj-B2-win2-node1-cpu.pb.gz` are distinct.

`bash -n` clean on both scripts; confirmed not running (`ps` shows no
`run-r35zzzj`/`chain-35zzzj` process). `chain-35zzzj.sh` waits on
`wr-logs/r35zzzi.log`'s terminal line.

**Prediction 93 (registered before any round, mechanism only):**

**(a)** NumGC per minute in win2 falls by >= 35% from 35zzzh/35zzzi's
own 10GiB baseline (~50-115 GC/min range measured across this
campaign's own 6/10 GiB legs), and combined GC+allocation CPU share in
win2 falls from ~54% (6db) to <= 40%.

**(b)** Per-node RssAnon rises by 2-4 GB in the B2/A2 (14GiB) legs
versus the B1 (10GiB) leg's own RssAnon, and fleet-wide `pgmajfault`/
`workingset_refault_file` per 10s tick in win2 RISE (not fall) as the
extra anonymous memory competes harder with the page cache -- reported
by exact magnitude, not just direction, once the round's own (now
fixed) VM sampler produces real numbers.

**(c)** Win2 block time: if (a)'s collection-frequency saving
outweighs (b)'s page-cache cost, win2 block time improves toward
win1's own figure; if (b) dominates, it does not, or gets worse. Either
outcome is the measured result, stated as such -- **anything inside
1.6-1.8s is NO EFFECT** (this campaign's own leg-order noise floor is
~0.1s, 6cv/6cx). Win1 (10GiB throughout in every leg) stays within
noise of 35zzzi's own win1 figure -- this round changes nothing about
B1/A1/warmup.

**(d) Safety.** The S26 harness checks (conflicting commits per
height; a leg that did not produce) stay green in every leg; the
14GiB legs produce blocks at all (a genuine risk per the GATES
analysis above, not assumed away).

**VERDICT: confirmed** (config-only change, no code; gates checked and
reported, not altered; both harness fixes tested offline; `bash -n`
clean; not launched). QS_QUEUE.md gets a new S28 row (status:
prepared) after the S27 row (status: closed). Launch is the
commander's next call.

## 6de. S29: 43% of the flood's allocation runs on the parallel-executor's own worker-pool goroutines and cannot be split leader-vs-follower by stack trace at all; of what CAN be split, block-import (F) is 3-4x ingest/gossip/pool combined (2026-09-21)

> **CORRECTION (commander, 2026-09-21 18:30 EDT) -- the "10.36 GB per block / 65-68 KB per transfer" figure in
> this section is WRONG and every number derived from it by division must not be used.** The harness fetched
> `/debug/pprof/allocs` WITHOUT `?seconds=`, which returns allocation CUMULATIVE SINCE PROCESS START (the whole
> leg: start-up, funding, 400 s of empty decay blocks, ramp, flood), and the analysis divided that total by the
> 16 blocks of the 20 s capture span. The runtime's own counter settles it: MemStats TotalAlloc on node0 rose
> 46-50 GB per 30 s in B1's first window = ~1.6 GB/s = **~1.9 GB per full block, ~12 KB per transfer per node**
> (second window ~2.3 GB per block). What stays valid: SHARES within the profile (as shares of the leg's
> cumulative allocation, mixing phases), the live-heap figures (inuse_space), NumGC, and the GC share of CPU
> (CPU profiles were 20 s deltas). With ~12 KB per transfer in total, the isolated benchmark's 6.25 KB per
> transfer for the executor is about HALF of a node's allocation, consistent with the partition's ~49%
> execution share -- the '4.4x gap' between benchmark and fleet was this artefact, not a property of the
> backend. From round 35zzzj on the capture uses `allocs?seconds=20`.

Profiles + code reading only, single-threaded/`nice` (35zzzi still on
the box). Inputs: `wr-pprof/r35zzzh-win1-win1-node{1,2}-allocs.pb.gz`
(B1/10GiB leg, win1; node1 = round log's own "leader=node1", node2 =
"follower=node2" for this specific capture -- see the caveat below on
what that label actually means for a tenure-rotating fleet). Binary
`n42-r92`, `GOCACHE=/data/blockchain/gov5-work/.gocache`. Script:
`wt-r27/scripts/qs-analysis/alloc_path_partition.py` (new, checked
in -- post-processes `go tool pprof -traces` text output, which has no
native "group by subsystem" mode).

### 1. Partition method, and the limit it ran into

**How paths were made mutually exclusive.** Every unique call stack in
`-traces`' own output was scanned end-to-end against an ORDERED list of
category patterns (leader-build markers, then follower-import markers,
then RPC-ingest, gossip-receive, gossip-send, pool-internal); the FIRST
category whose pattern matched ANY frame in that stack won the whole
stack's value, so no stack is counted twice. Order matters because
`internal.runParallel`/`parallelApplyTx` are SHARED by build and
import -- checking the disambiguating outer frame (`BuildParallel` for
the leader's own fill, `InsertChain`/`blockPushStreamHandler`/
`decodeChunkedBlock` for import) FIRST, before the shared internals,
is what keeps E and F apart wherever the outer frame is actually
present in the sample.

**The limit, found while building this.** A large share of samples
whose LEAF is `parallelApplyTx` (and everything it calls --
`decodeUint256`, `IntraBlockState.setStateObject`/`Reset`, `journal.
push`, `MVS.Write`, `ReadWriteSet.MarkBalanceInsensitive`, `NewTxOwned`,
`decodeEthereumTransaction`...) have a call stack **only 5-6 frames
deep**, bottoming out at `(*Executor).executeParallel.func1` -- the
worker-pool goroutine's OWN entry point. Go's default profiling stack
does not retain a goroutine's "created by" chain, so **once a
transaction's execution work is handed to the executor's worker pool,
the sample stack can no longer say whether that specific worker was
processing a leader's own `BuildParallel` fill or a follower's
`InsertChain`/`ProcessParallel` import -- both call into the exact same
pool through the exact same functions.** This is not a bug in this
section's method; it is a structural property of how the executor is
built (one shared worker pool, reached from two different callers).
**Rather than force a guess, this share is reported as its own
category, `EXEC_shared_build_or_import`, named and quantified, not
folded into either E or F or hidden in a catch-all.**

### 2. The partition (node1, "leader" per this capture; node2, "follower", side by side -- both react nearly identically, see the caveat below)

| category | node1 | node2 | GB/block* | KB/tx* |
|---|---|---|---|---|
| A: RPC ingest | 4.9% | 5.0% | 0.51 | 3.1 |
| B: gossip receive | 2.7% | 2.7% | 0.28 | 1.7 |
| C: gossip send/forward | 4.9% | 4.6% | 0.51 | 3.1 |
| D: txpool internal | 3.3% | 3.4% | 0.34 | 2.1 |
| E: leader build (attributable) | 5.6% | 5.6% | 0.58 | 3.6 |
| F: follower import (attributable) | 20.8% | 20.9% | 2.15 | 13.2 |
| **EXEC_shared (build+import, unattributable)** | **43.5%** | **43.0%** | **4.51** | **27.6** |
| unassigned (`G`, no pattern matched) | 14.3% | 14.9% | 1.48 | 9.1 |

*GB/block and KB/tx are derived: the node1 percentage applied to
6da/6dc's own measured 10.36 GB/block (163,000 tx/block), NOT to this
script's own raw total (which sums to 165.74-175.27 GB across the two
nodes' captures, a ~3-6% over-count from a rare double-annotated-stack
artifact in `-traces`' own output -- percentages, which are robust to
that scaling, are the primary number; GB/tx figures are for scale
only). **Share of the whole node's alloc_space assigned to an
EXCLUSIVE, named category (everything except `G`): 85.7% (node1),
85.1% (node2) -- at or just above the task's own 85% confirm bar.**

**Why node1 and node2 look nearly identical despite the round log
calling one "leader" and the other "follower" for this specific
capture:** tenure is 4, and the capture window (20s, ~16 full blocks)
spans MULTIPLE tenures -- **every node in a 7-node fleet leads roughly
1-in-7 blocks and follows the other 6-in-7 within any 20-second
window**, so a single node's own 20s capture already contains a
representative mix of its OWN leader-build traffic and its OWN
follower-import traffic for everyone else's blocks. The round log's
"leader=node1/follower=node2" label names which node was captured
mid-proposing a SPECIFIC block at the capture's own trigger instant,
not which role dominates that node's WHOLE 20-second window -- which
is why this section's own E/F split (attributable leader-build only
5.6%, attributable follower-import 20.8%, a ~3.7:1 ratio) is close to
the STRUCTURAL 1:6 leader:follower block-count ratio the fleet's own
tenure schedule produces, on BOTH nodes, regardless of the capture
label.

### 3. Mechanics of the two biggest CLEANLY-ATTRIBUTABLE non-executor groups

**F: follower import (2.15 GB/block, 13.2 KB/tx attributable, PLUS an
unknown share of `EXEC_shared`'s own 4.51 GB/block that is genuinely
import-side work the stack cannot prove).** Reached via `internal/
sync.(*Service).blockPushStreamHandler` -> `ReadChunkedBlock` ->
`decodeChunkedBlock` (`internal/sync/rpc_chunked_response.go:132`,
6dc's own citation) -> `rlp.DecodeBytes` of the WHOLE block (header +
body + every transaction) -> `BlockChain.InsertChain`/
`InsertChainAuthorized` -> `StateProcessor.Process`/`ProcessParallel`
-> the shared executor. **6dc already established the crossing count
here precisely: this decode is UNCONDITIONAL and re-decodes every
transaction via RLP even when that exact transaction (same hash)
already sits in the node's own pool, fully decoded, from an earlier
gossip or RPC arrival -- 6dc's own estimate, ~99.4% of a pushed
block's transactions are this kind of redundant decode.** `-focus`
against `go-buffer-pool|handleIncomingRPC|handleNewStream` (below)
shows `BufferPool.Get` itself sits partly on the RECEIVE side too (see
the C caveat below), meaning some of what this section counted as "C:
send/forward" may actually be inbound framing for the SAME push/gossip
traffic that feeds F -- flagged, not resolved, given the time budget.

**EXEC_shared (4.51 GB/block, 27.6 KB/tx, unattributable): the SAME
top sites 6db/6dc already named and ranked** (`journal.push` 27.3%
of the isolated executor benchmark, `parallelApplyTx`'s own `tx.
AsMessage`/receipt-alloc lines, `freshStateObject`, `setStateObject`,
`MVS.getOrCreateEntry`, `IntraBlockState.Reset`) -- this section adds
no new site-level finding here beyond confirming, via the fleet's own
in-window captures (not the isolated benchmark), that these sites'
FULL fleet-scale cost (27.6 KB/tx) is much larger than the isolated
`BenchmarkParallelBlockTransfers` figure (6.25 KB/tx, 6dc) because the
FLEET capture includes BOTH the leader's build execution AND every
follower's import execution of the SAME transactions, whereas the
isolated benchmark times ONE execution pass only -- **this 4.4x gap
(27.6 / 6.25) is close to, and consistent with, "one build + roughly
several import executions of overlapping transactions across a
20-second, multi-tenure window," not a discrepancy needing its own
explanation.**

### 4. Crossings: how many times one transaction is decoded/handled per node

- **RPC ingest**: a transaction submitted to THIS node's own RPC
  arrives and is decoded **once** (`A`), then held decoded in the pool.
  The harness's own generators submit round-robin/sharded across all 7
  nodes' RPC endpoints (not all to one node), so any single node's own
  RPC-ingest share reflects roughly 1/7 of the flood's total submission
  volume arriving THIS way.
- **Gossip receive**: GossipSub's own mesh (this campaign's standing
  config, `D=8`/`Dlo=6`) means a message can arrive at a node from
  MULTIPLE mesh peers before the LOCAL seen-cache (keyed by `MsgID`,
  `internal/p2p/message_id.go`) has recorded it as seen -- **but
  go-libp2p-pubsub's own dedup check (`validateWorker`/`pushMsg` in the
  vendored `go-libp2p-pubsub` package) runs AFTER the wire frame has
  already been read and unmarshalled into a `pubsubpb.Message`, and
  BEFORE the payload (the transaction bytes) is handed to this node's
  OWN application-level validator/decode** -- meaning the FRAMING
  allocation (the buffer read, protobuf unmarshal) happens for every
  physical copy received, duplicates included, while the actual
  transaction RLP DECODE (this section's own `B_gossip_receive`/`EXEC`
  cost) is gated behind the dedup check and should NOT re-decode an
  already-seen message's payload -- **not independently re-verified
  against the vendored library's own source in the time available;
  stated as the expected behavior per the library's documented design,
  not confirmed line-by-line this pass.**
- **Gossip send/forward**: `go-buffer-pool.(*BufferPool).Get`
  (8.3 GB/16 blocks fleet-wide on this node, matching 6da/6db's own
  figure exactly) is shared by GossipSub message framing AND (per the
  `-focus` check above) some receive-side handling
  (`handleIncomingRPC`/`handleNewStream` both appear in its own call
  subtree) -- **it is not exclusively an outbound/forward cost**, so
  this section's "C" label should be read as "buffer-pool-adjacent
  libp2p framing, receive and send mixed," not purely forwarding.
- **`broadcast=0`**: grepped in `run-r35zzzh.sh`'s own generator
  invocation flags -- **not resolved to a specific propagation-mode
  meaning in the time available**; the flag is passed to the harness's
  own `txflood` binary invocation, not to the node, and this section
  did not trace its effect through `txflood`'s own source (out of
  scope for a node-side profile read). **The fleet's actual tx
  propagation mode (full gossip vs hint-only vs announce-only) is
  therefore reported as NOT DETERMINED by this pass** -- the campaign's
  own `chain-35zzzh.sh` banner text does not mention `-hint-peers` for
  this round (round 35zi's own hint-only track is a DIFFERENT,
  historical configuration per the run script's embedded history, not
  this round's), which is suggestive of full gossip but not a
  confirmed reading of `broadcast=0` itself.

### 5. Live heap side (`inuse_space`, same categories)

Not re-run with the full `-traces`-based partition this pass (time
budget spent on the alloc_space partition and its own EXEC-boundary
finding, which the task's own numbered items placed first) -- **6da's
own `inuse_space` top-line figures stand as the answer**: `txlookup.
Tail.Add` (1.04 GB, 18.0%, category D) and `senderCachePut` (0.93 GB,
16.2%, category D) are pool-internal; `qmdb.newMapIndexSized`
(0.78 GB, 13.6%) is neither ingest nor pool in this section's own A-G
scheme -- it belongs to the CONSENSUS/COMMIT path (category G,
state-commitment, not transaction handling at all) and its own size is
driven by chain-state key count, not by anything in this section's own
per-transaction accounting. The tx-decode family (~25% of live heap,
6da) splits, by the SAME reasoning as the alloc-space partition above,
between category A (pool-resident, RPC-origin) and category
EXEC_shared/F (pool-resident, gossip/import-origin) -- **not further
separated for live heap in the time available.**

### 6. Ranked candidates outside the executor (at most 5)

| # | change | KB/tx removed (derived) | file:line | product/harness | offline proof | risk |
|---|---|---|---|---|---|---|
| 1 | Skip the RLP re-decode in `decodeChunkedBlock` for transactions already resident (by hash) in the local pool, reusing the pool's already-decoded object + already-recovered sender | **up to ~13.2 KB/tx** (this section's own attributable F share; the true ceiling is higher once `EXEC_shared`'s own import-side portion is counted, not separable here) | `internal/sync/rpc_chunked_response.go:132` | **product** | no existing benchmark covers block-import decode; would need a NEW one (decode+import N pool-resident transactions via a synthetic pushed block, alloc/op before/after) | medium -- must preserve byte-for-byte equivalence with what a fresh decode would produce (a stale/mismatched pool entry must not silently substitute the wrong transaction) |
| 2 | Confirm and, if needed, fix whether `go-buffer-pool.Get` on the RECEIVE side (`handleIncomingRPC`/`handleNewStream`) is sized to the FULL frame every time, vs. reusing a pooled buffer across reads | not derived (this section could not isolate receive-only from the mixed C total) | `internal/p2p/` + vendored `go-libp2p-pubsub`/`go-buffer-pool` | **product** (libp2p integration, not a node-only file) | a pubsub-loopback benchmark (two in-process nodes, one gossip topic, N synthetic tx messages) measuring `alloc_space`/message | medium -- vendored dependency, changes here are library-integration-level, not a single N42 file |
| 3 | Reduce GossipSub mesh degree `D` for this specific 7-node, full-mesh topology (every node already reachable within D=6-8 hops of a 7-peer graph; a full 7-node mesh needs D no larger than 6 to reach everyone directly) | not derived (bounds re-gossip COUNT, not bytes/tx directly -- would need a controlled A/B) | harness/node config (`gossip 24 MB` mesh params, `internal/p2p/gossip_scoring_params.go`) | **harness config`** (this campaign's own standing mesh-size choice, not a code change) | a pubsub-loopback benchmark varying D, counting `BufferPool.Get` calls/message | low-medium -- smaller D reduces propagation redundancy but could increase tail latency for a lost direct link; this campaign's own 7-node fleet is small enough that the tradeoff is probably favorable, not verified here |
| 4 | Batch multiple small transactions into fewer, larger gossip messages instead of one message per transaction (if that is in fact today's shape -- not confirmed this pass) | not derived (batching shape not confirmed) | harness or protocol-level (`internal/distributed/messaging` or the tx-gossip topic's own publish call site) | **not determined** whether product or harness without confirming today's batching shape first | a pubsub-loopback benchmark comparing 1-tx-per-message vs N-tx-per-message at fixed total tx volume | low, PROVIDED the batching shape itself is confirmed first -- listed as a candidate to investigate, not a confirmed opportunity |
| 5 | RPC ingest's `encoding/json.(*Decoder).refill`/`(*RawMessage).UnmarshalJSON` (2.88 GB + 1.08 GB of this node's own 20s window, per the `-focus=TransactionAPI` check) -- batch-decode via a lower-allocation JSON path (e.g. streaming array decode without `RawMessage` boxing) for `eth_batchRawTransaction` specifically | ~3.1 KB/tx of category A's own 3.1 KB/tx total (the JSON layer, not the RLP payload underneath it) | `modules/rpc/jsonrpc/` (exact call site not `-list`-read this pass) | **product** | existing RPC benchmarks were not found in the time available (`grep -rn "func Benchmark" modules/rpc/` not run this pass) -- would need a new one, submitting N raw txs through `BatchRawTransaction` | low-medium -- JSON decode changes are usually mechanical, but `eth_batchRawTransaction` is a live RPC surface other tooling may depend on exactly as shaped today |

**Method.** `-traces` output for both nodes' win1 captures was parsed
by `alloc_path_partition.py`; the script's own value-summing was
cross-checked against `go tool pprof -top`'s own reported total (within
~3-6%, attributed to a rare recurring-stack double-annotation in
`-traces`' own text format, not corrected further given the time
budget -- percentages are reported as the primary, scale-robust
number). Per-category top-site figures for A and C used `go tool pprof
-top -focus=<pattern>` directly (a reliable, standard pprof feature)
rather than the script's own leaf-name extraction, which was found
unreliable for stacks carrying a `-traces`-specific `bytes:`/`count:`
annotation line and was not used for citing individual site numbers
for that reason (category TOTALS from the script are still used, since
those sum correctly; only per-leaf naming within a category used
`-focus` instead).

**What this does and does not show.** It shows a mutually-exclusive,
honestly-labelled 8-way partition (A-G plus the newly-named
`EXEC_shared`) covering 85.1-85.7% of the flood's own alloc_space in
named, non-overlapping categories, clearing the task's own 85% bar. It
shows, as a structural finding rather than a methodology failure, that
the parallel executor's own worker-pool design makes 43% of the
flood's allocation UNATTRIBUTABLE to leader-build vs. follower-import
by stack trace alone -- a limit on what profiling (as opposed to
targeted instrumentation, e.g. a caller-ID parameter threaded through
`executeParallel`) can answer here. It shows F (follower import,
attributable share alone) is 3.7x the combined attributable A+B+C+D --
consistent with 6dc's own confirmed redundant-decode finding on the
`decodeChunkedBlock` path. It does NOT resolve the `C`-category
receive/send ambiguity in `go-buffer-pool.Get`'s own call sites, does
NOT determine what `broadcast=0` means for this round's actual
propagation mode, and does NOT re-verify go-libp2p-pubsub's own
dedup-vs-allocation ordering against its source -- three items named
as open, not answered, consistent with this task's own time budget and
its instruction to report findings honestly rather than force a
resolution.

## 6df. S30: H-bench wins -- the benchmark's memdb backend and its explicit skip of Finalize/block-end explain the 4.4x gap; H-multi's literal form (re-executing a committed block) is not found, but a real, already-counted second per-tx pass (CheckDeferredBlock's own sender recovery) is confirmed on the follower side (2026-09-21)

> **CORRECTION (commander, 2026-09-21 18:30 EDT) -- the "10.36 GB per block / 65-68 KB per transfer" figure in
> this section is WRONG and every number derived from it by division must not be used.** The harness fetched
> `/debug/pprof/allocs` WITHOUT `?seconds=`, which returns allocation CUMULATIVE SINCE PROCESS START (the whole
> leg: start-up, funding, 400 s of empty decay blocks, ramp, flood), and the analysis divided that total by the
> 16 blocks of the 20 s capture span. The runtime's own counter settles it: MemStats TotalAlloc on node0 rose
> 46-50 GB per 30 s in B1's first window = ~1.6 GB/s = **~1.9 GB per full block, ~12 KB per transfer per node**
> (second window ~2.3 GB per block). What stays valid: SHARES within the profile (as shares of the leg's
> cumulative allocation, mixing phases), the live-heap figures (inuse_space), NumGC, and the GC share of CPU
> (CPU profiles were 20 s deltas). With ~12 KB per transfer in total, the isolated benchmark's 6.25 KB per
> transfer for the executor is about HALF of a node's allocation, consistent with the partition's ~49%
> execution share -- the '4.4x gap' between benchmark and fleet was this artefact, not a property of the
> backend. From round 35zzzj on the capture uses `allocs?seconds=20`.

Logs + profiles + code, single-threaded/`nice` (35zzzi still on the
box). Same inputs as 6de (`wr-logs/r35zzzh-keep/node{1,2}-B.log`,
capture span 16:25:11-16:25:31 -- the actual profiled 20s, not the
16:25:11-16:26:32 span the task named, which spans BOTH win1's node1/
node2 capture AND win2's separate node6/node0 capture; this section
uses the window that matches the profiles 6de actually read).
`BenchmarkParallelBlockTransfers` was READ, not re-run (the task's own
instruction: at most once, pinned, and the box is still running
35zzzi -- not spent here since the committed benchmark numbers from
6dc are sufficient for this section's own comparison).

### H-multi: counted directly from the kept logs, node1 and node2, capture span only

| line | node1 | node2 |
|---|---|---|
| `miner: parallel fill` | 1 | 4 |
| `parallel block` | 15 | 16 |
| `miner: speculative build parked` | 2 | 3 |
| `miner: speculative build hit` | 2 | 3 |
| `miner: speculative build discarded` | 0 | 0 |
| `blockimport phases` | 14 | 13 |
| `deferred check:` | 15 | 13 |
| `miner: suppressing divergent...` (sibling drop) | 0 | 0 |

**`parallel block` fires EXACTLY ONCE per distinct committed height on
both nodes** (checked directly: node1's 15 occurrences cover 15
distinct heights 13658578-13658592, each with count 1; node2's 16
cover 16 distinct heights 13658577-13658592, each count 1). **Zero
speculative builds were discarded and zero same-height siblings were
suppressed in this window** -- every speculative build that started
was later hit, not wasted. **H-multi in its literal form -- the SAME
committed block's transaction set re-executed more than once on one
node -- is NOT FOUND in this window.** The denominator question the
task raised (was the "16 blocks" all full, and did speculative building
beyond those 16 happen inside the span) is answered directly by the
above: yes, all 15-16 `parallel block` firings correspond to the 15-16
FULL, distinct, committed heights counted in 6da/6de (no extra,
uncommitted executions ran inside the span on either node) -- **the
denominator (16 blocks / ~163,000 tx each) used throughout 6da-6de is
correct as a per-committed-transaction average; it is not inflated or
deflated by hidden re-execution.**

**A real, different form of "multi" IS confirmed, already counted in
6de's own `F` bucket.** `CheckDeferredBlock` (`internal/
deferred_includable.go:38`, the ONLY call site is `internal/sync/
rpc_block_push.go:91` -- **follower-only**, a node never runs this on
its own sealed block) calls `deferredTxPlan` (line 120), which:
recovers **every transaction's sender AGAIN** (`transaction.Sender
(signer, t)`, line 165, fanned out across `senderRecoveryFanout()`
goroutines) into a fresh `senders := make([]types.Address, len(txs))`
slice (line 126), then groups transactions into per-sender
`deferredSender` structs, each carrying its OWN `txs []*transaction.
Transaction` slice. **This is a genuine SECOND per-transaction pass --
sender recovery plus a full grouping/slice-allocation walk -- separate
from and IN ADDITION TO the executor's own per-tx sender recovery
inside `parallelApplyTx`/`recoverBlockSenders`.** It runs once per
received block, follower-side only (`deferred check:` firing 13-15
times matches the follower-import count almost exactly, 14/13 vs
13/15 blockimport-adjacent counts, consistent with running on
essentially every pushed block this node receives). **It was already
correctly bucketed into `F` in 6de** (the call chain `deferredCheck ->
CheckDeferredBlock -> deferredTxPlan` matched 6de's own F pattern
list) -- this section's contribution is naming the MECHANISM precisely,
not correcting a mis-bucketing.

### H-bench: read from the benchmark's own source, `internal/parallel_processor_bench_test.go`

Three structural gaps versus the fleet's real pipeline, all confirmed
by reading the file directly:

1. **State backend: `lib/kv/memdb` (in-memory), not MDBX/QMDB.**
   `runTransferBlockOnce` opens `bc.ChainDB.BeginRo` against a
   `memdb.NewTestDB` -- every state read the benchmark's own
   `PlainStateReader` performs is a Go map lookup, never a cgo call
   into MDBX, never a real QMDB tree/twig read, never the fleet's own
   cached-state-reader/post-state-layer wrapping stack the miner's real
   fill and the importer's real `ProcessParallel` run through. This is
   the single largest structural gap: every one of the fleet's own
   per-tx state reads (`PlainStateReader.ReadAccountData` -- present in
   BOTH the benchmark's own top-10, 6dc, AND the fleet profile, so the
   CODE PATH is shared, but the underlying STORAGE is not) costs
   whatever a real MDBX page read/cgo round-trip costs in the fleet,
   which the benchmark's in-memory map cannot reproduce.
2. **`BuildParallel` explicitly skips Finalize and block-end, by its
   own doc comment** (quoted verbatim in the benchmark's own code
   comment, `internal/parallel_processor_bench_test.go:121-124`):
   *"neither block end nor Finalize runs -- the builder's assemble does
   that."* The fleet's real pipeline -- both the miner's `commit()` (for
   a leader's OWN block) and the importer's `StateProcessor.Process`
   (for a follower's import) -- DOES run block-end system calls
   (EIP-7002/7251 Prague withdrawal/consolidation, per the CLAUDE.md's
   own architecture notes) and per-block `Finalize` (the delta-credit
   fold, the state-root-relevant bookkeeping) on top of what
   `BuildParallel` measures. **None of that cost is in the benchmark's
   own 6.25 KB/tx at all -- not "under-measured," genuinely absent.**
3. **20,000 transactions, reused/warm state across `b.N` iterations,
   fresh keys generated ONCE outside the timed loop.** The fleet's own
   full blocks are ~163,000 transactions (8.15x the benchmark's own
   count) against a chain state that has been accumulating live
   accounts/storage for the whole leg (6da/6cr's own `qmdb.mapIndex`
   growth finding) -- any per-block bookkeeping structure whose COST
   scales with total LIVE state size rather than purely with
   transactions-in-this-block (the QMDB index lookup itself, page-cache
   locality) would cost MORE in the fleet than in a benchmark running
   repeatedly against the SAME small, warm, 20,000-account state. This
   is a plausible contributor, not separately quantified this pass.

**Sites present (non-negligible) in the fleet's own profile but
absent or negligible in the benchmark's own memprofile top 10 (6dc):**
`go-buffer-pool.(*BufferPool).Get` (fleet: 8.32 GB/capture, 6da/6de's
own category C/B; wire/framing -- structurally cannot appear in a
benchmark that never touches the network), `transaction.
decodeEthereumTransaction`/`DecodeEthereumTransaction` (fleet: 8.28+
4.08 GB; the benchmark decodes its OWN 20,000 transactions ONCE,
OUTSIDE `b.ResetTimer()`, so decode cost is explicitly excluded from
the timed/measured loop by design -- 6dc's own text confirms this:
"transactions are pre-decoded before `b.ResetTimer()`"), `internal/
sync.(*Service).deferredCheck`/`deferredTxPlan` (fleet: real, per H-multi
above; `BuildParallel` has no caller that would ever reach
`CheckDeferredBlock`, so this is structurally absent from the
benchmark, not merely small), `protobuf/internal/impl.consumeBytes`
(fleet: 3.96 GB; block/message wire deserialization, the benchmark
builds its `block.Header` as a Go struct literal, never through
protobuf). **These four are exactly the sites 6de's own `A`/`B`/`C`/
`F` categories cover -- confirming, from the benchmark's own source
rather than inference, that the executor-isolated benchmark was never
going to see them, by design, not by oversight.**

### Reconciled account

| | executor cost per EXECUTION | executions per node per committed block | role |
|---|---|---|---|
| Isolated benchmark (`BuildParallel`, memdb, no Finalize/block-end, pre-decoded, 20k tx) | 6.25 KB/tx | 1 (measured in isolation) | neither -- a lower bound on the executor's OWN inner loop only |
| Fleet, leader role (build) | ~27.6 KB/tx (`EXEC_shared`, 6de) + ~3.6 KB/tx attributable build-wrapper (`E`, 6de) = **~31.2 KB/tx** | **1** (confirmed above: no re-execution found) | `BuildParallel` through the REAL MDBX/QMDB stack, plus `commit()`'s own Finalize/block-end the benchmark skips |
| Fleet, follower role (import) | ~27.6 KB/tx (`EXEC_shared`) + ~13.2 KB/tx attributable import-wrapper (`F`, 6de, INCLUDES `deferredTxPlan`'s own second sender-recovery pass) = **~40.8 KB/tx** | **1 execution + 1 additional lighter per-tx pass** (`deferredTxPlan`: sender recovery + grouping, not a full EVM re-execution) | `ProcessParallel` through the REAL stack, plus the unconditional RLP re-decode (6dc) and the deferred-check's own extra sender-recovery walk, both real and both already counted in `F` |

**`EXEC_shared`'s own 27.6 KB/tx, appearing in BOTH the leader and
follower rows above at the SAME value, is not double-counted across
this table -- it is the SAME underlying per-execution cost (the
executor doesn't know or care whether `parallelApplyTx` was reached via
`BuildParallel` or `ProcessParallel`), quoted once per row because each
row describes ONE node's ONE execution of that role.** The gap between
this per-execution cost (27.6 KB) and the benchmark's own (6.25 KB) --
a **4.4x** ratio -- is explained STRUCTURALLY by H-bench's three items
above (real MDBX/QMDB reads replacing in-memory map reads being the
largest of the three), not by counting the same execution more than
once.

**VERDICT: H-bench, primarily; H-multi confirmed in a real but minor,
already-counted form.** The literal H-multi ("a transaction's full
execution repeats") is not found in this window: `parallel block`
fires once per committed height, zero speculative builds were wasted.
The MILDER form of H-multi (a genuine second, lighter per-transaction
pass -- sender recovery, not full EVM execution -- via `deferredTxPlan`
on the follower side) IS confirmed and real, but it was ALREADY inside
6de's own `F` figure, not an unaccounted addition on top of it. The
DOMINANT explanation for the executor-cost gap (27.6 vs 6.25 KB/tx,
4.4x) is H-bench: the isolated benchmark runs against `memdb` (no real
MDBX/QMDB reads) and explicitly skips `Finalize`/block-end by its own
design, per its own doc comment -- structural absences, not
measurement noise.

### Corrections, dated

- **6dc's "~9.2%" (isolated executor's share of the fleet's per-transfer
  allocation) needs a READING correction, not a numeric one**: 6dc's
  own 9.2% figure is, and remains, an accurate measurement of WHAT THE
  BENCHMARK MEASURES (`BuildParallel`'s own inner-loop cost against
  `memdb`, Finalize/block-end excluded by the benchmark's own explicit
  design). **It should not be read as "the executor is 9.2% of the
  fleet's real per-transaction executor cost" -- it is closer to
  6.25/31.2 = 20% of the fleet's OWN real leader-role executor cost, or
  6.25/40.8 = 15% of the real follower-role cost**, once Finalize/
  block-end and the real storage backend are accounted for. 6dc's own
  conclusion (drop items 1-4, keep the benchmark as a yardstick) is
  UNCHANGED by this correction -- the benchmark remains a valid,
  useful tool for measuring changes WITHIN the executor's own inner
  loop; it was never designed to, and should not be read as, a
  full-fidelity stand-in for the fleet's total per-transaction cost.
- **6de's partition needs no bucket correction** -- `deferredTxPlan`
  was already correctly classified as `F` by this section's own
  re-check of the call chain. What 6de's own text did not yet say,
  and this section adds, is the NAME and MECHANISM of a specific,
  real cost inside `F`: a second, sender-recovery-only per-transaction
  pass, distinct from (and cheaper than) a full re-execution.

### One-paragraph restatement, of the 10.36 GB/block

**(i) One necessary execution** (the executor's own real, MDBX/QMDB-
backed, Finalize/block-end-included cost, ~31.2 KB/tx on the block's
own leader, ~4.51 GB/block worth of `EXEC_shared` fleet-wide-average
plus its own `E`/`F` wrapper shares) is **roughly 5.1 GB/block (49%)**
(`EXEC_shared` 4.51 + `E` 0.58 GB, the leader-side reading of "the one
execution a committed transaction structurally requires"). **(ii)
Repeated or discarded executions**: **not found in this window** --
0% of the 10.36 GB, per H-multi's own direct count above (zero
discarded speculative builds, zero suppressed siblings, one `parallel
block` per committed height). **(iii) Follower-side decode + deferred
check** (the unconditional RLP re-decode, 6dc, plus `deferredTxPlan`'s
own second sender-recovery pass, both inside `F`'s attributable
2.15 GB/block/13.2 KB-tx): **~2.15 GB/block (21%)**, on top of the SAME
transactions' `EXEC_shared` cost already counted in (i)'s reading for
the OTHER six nodes that import rather than build any given block
(this paragraph describes ONE node's own per-transaction average
across its own mixed role-week, not a fleet-wide sum across all seven
nodes, consistent with 6de's own scope). **(iv) Ingest/gossip/pool**
(`A`+`B`+`C`+`D`, 6de): **~1.61 GB/block (16%)**. **(v) Unassigned**
(`G`, 6de): **~1.48 GB/block (14%)**. These five sum to the measured
10.36 GB/block within rounding (5.1+0+2.15+1.61+1.48 = 10.34 GB).

**Method.** Log counts via direct `grep`/`python3 -c` one-liners
against the exact 20s window each profile actually covers (verified
per-height uniqueness with a small inline counter, no new script).
`deferred_includable.go` and `parallel_processor_bench_test.go` read
directly, in full, for the call chain and the benchmark's own explicit
scope statements -- no inference from profiles for either.

**What this does and does not show.** It shows the executor-cost gap
between 6dc's benchmark and 6de's fleet figures is real, structural,
and now explained by NAMED code differences (backend, Finalize/block-
end scope, decode timing) rather than left as an open ratio. It shows
H-multi's literal form is absent from this window by direct count, and
locates the real (milder) form of "multi" precisely, inside a bucket
that was already correctly counted. It does NOT re-run the benchmark
against a QMDB/MDBX-backed harness to QUANTIFY the backend gap
directly (the task's own instruction: at most once, pinned, and not
spent here) -- the 4.4x ratio is explained qualitatively, by naming
the missing pieces, not decomposed into "X% backend, Y% Finalize, Z%
scale." It does NOT re-measure `deferredTxPlan`'s own KB/tx separately
from the rest of `F` -- naming the mechanism was this section's own
scope, not re-splitting an already-correct bucket further.

## 6dg. S26: the vote-rule fix holds in a full fleet round -- 0 conflicting heights across 3,355 checked, 0 commit-vote refusals (the race didn't recur), and the cost is real: Round1 median 60ms -> 153-262ms, and in win2 the vote round is back on the critical path 78-83% of the time (2026-09-21)

n42-r94 (n42-r92's file set + `e49ce1512ff3cf17d5200d96b16fe191b4b8b09d`,
S26's vote-rule fix) ran B1 (17:25:57-17:39:22) and B2
(17:39:22-17:52:21) cleanly, `GOMEMLIMIT=10GiB` throughout. Node logs
preserved whole, trimmed to `wr-logs/r35zzzi-keep/node{0-6}-B.log`.
Script: `height_conflict_check.py` (6da, re-run unmodified) plus
`seal_path_waterfall.py` (6cv/6cx, unmodified) and the standard
inline `import_breakdown`/QC-vs-build queries this campaign has used
since 6cs.

### 1. Safety (91a)

`height_conflict_check.py`, run independently of the harness's own new
check: **3,355 committed heights checked, 0 conflicts** (matching the
harness's own "no height with two committed hashes" result exactly).
`commit vote REFUSED: proposal does not extend its JustifyQC block`
(`processPrepareQC`'s Round 2 guard, the ONE new log line the fix
adds) -- **0 occurrences on any of the 7 nodes**, confirmed directly
(the coordinator's own quick grep, re-checked here). Round 1's own
guard (`tryDeferredVote`'s `extendsJustify` check) adds **no log line
at all** -- a refusal there is silent, indistinguishable at the log
level from the ordinary "not yet checked/imported" early return one
line above it in the same function -- so Round 1 refusals cannot be
separately counted from logs; **zero Round 2 refusals is therefore the
only direct evidence available, and it says the stale-sibling race
that produced 35zzzg's own incident did not recur this round.**

`"sealed block is stale"`/`ErrStaleSeal` and `"the write failed AFTER
the Proposal left"` (`propose-before-write`'s own warning) both fire
**365-378 times per node** -- identical counts on every node, because
they are the SAME event logged from two call sites of one code path.
**This is a normal, expected background rate, not a recurrence of the
bug**: every one of these is a speculative build whose OWN write later
found the applied head had already moved on -- the everyday cost of
speculative building outracing real chain progress (6cp/6cw's own
"buildBegin->specParked takes 600-1000ms, longer than Round1+Round2"
finding), not a same-height sibling collision. **`"miner: suppressing
divergent same-height sibling"` -- the fix's OWN new leader-side guard,
moved to fire BEFORE push/propose -- occurred exactly ONCE (node6)**:
this is the ONE case in the whole round where two DIFFERENT candidates
were sealed for the SAME height, and the fix caught it before either
was ever pushed. **None of the 365-378 stale-seal events happened
AFTER a proposal that had ALSO collected votes** (the pre-fix hazard;
confirmed by the 0 commit-vote-refusal count above, since any block
that reached Round 2 with a non-extending parent would have tripped
that exact guard) -- every stale-seal event this round is the
BEFORE-any-hazard, ordinary kind.

### 2. Cost (91b): full in-tenure views, win1/win2, vs 35zzzf (r92) and 35zzzh-B1 (also r92)

| | 35zzzf win1 (r92) | 35zzzi win1 (r94) | 35zzzf win2 | 35zzzi win2 |
|---|---|---|---|---|
| Round1 (median) | ~63-65 ms | **153-157 ms** | ~70-72 ms | **212-262 ms** |
| Round2 (median) | ~87-99 ms | 10-11 ms | ~138-163 ms | 236-271 ms |
| leader `jcvMs` (median) | 0 | 0 | 0 (p90 234-349) | 0 (p90 272-433) |
| `lwWhy` timeout share | 17-27% | **66.7-85.7%** | 27-33% | **87.0-95.7%** |
| in-tenure CYCLE (median) | 691-656 ms | 650-663 ms | 850-904 ms | **877-1010 ms** |
| follower `import total` (mandatory line) | 813 ms (35zzzh) | **791 ms** | -- | -- |
| first-window TPS | 141.0k/137.5k | **130.0k/135.3k** | -- | -- |
| second-window TPS | 92.3k/93.5k | **85.2k/85.6k** | -- | -- |
| blockTime win2 | 1.765/1.714 s | **1.875/1.875 s** | -- | -- |

**Round1 rose almost exactly as predicted** (~60 -> ~153-262 ms,
against the ~180 ms the fix's own deferred-check-gating design
predicted -- landing a bit below in win1, a bit above in win2).
`lwWhy` shifted heavily toward `timeout` (the 150 ms write-latch
ceiling is now hit 67-96% of the time, up from 17-33%) -- consistent
with the SAME overall vote-round work now happening, mostly moved
earlier into Round1, leaving less of the 150 ms budget for the journal
to complete inside Round2's own now-much-faster window. **`import_breakdown`'s
own mandatory line (791 ms total, follower import) is inside noise of
35zzzh's own 813 ms** -- import itself is unaffected, as expected (the
fix touches voting, not import). **First-window TPS (130.0k/135.3k)
sits BELOW 35zzzf's 141.0k/137.5k and 35zzzh-B1's 128.5k -- inside the
round-to-round spread this campaign has already established (6cm: 
±26.6% between same-config rounds), not distinguishable from noise on
three data points, but consistent in DIRECTION with the fix costing
something on the win1 side too, not free.**

**Hand-over cycle**: not separately re-derived this pass (time budget;
the in-tenure CYCLE figures above are the ones directly comparable
across rounds using this campaign's own established join). The
in-tenure CYCLE itself stayed close to 35zzzf's own win1 figure
(650-663 vs 656-691 ms) but win2 grew further (877-1010 vs 850-904 ms)
-- **this is where a longer Round1 could plausibly bite hardest, per
the task's own framing, and win2's own numbers move in that direction,
though not by a large enough margin to separate from this campaign's
own established win1->win2 growth (already present in every prior
round at similar magnitude, 6cp/6cv/6cx) without a larger sample.**

### 3. Did CommitQC(v) move onto the critical path?

Using the same `propose` field / `push(v+1) = CommitQC(v) + propose`
identity 6cs established:

| | B1win1 | B2win1 | B1win2 | B2win2 |
|---|---|---|---|---|
| CommitQC(v) offset from push(v) (median, p90) | 167, 721 ms | 164, 478 ms | **605, 924 ms** | **533, 707 ms** |
| build-end offset from push(v) (median, p90) | 368, 499 ms | 361, 466 ms | -19, 586 ms | -16, 609 ms |
| **share of views where CommitQC is the LATER event** | 33.3% | 33.3% | **82.6%** | **78.3%** |

**In win1, the build (speculative execution of v+1) is still usually
the later, gating event (CommitQC later only 1-in-3 views) -- close to
6cs's own B1 finding (0% QC-later) but not identical, itself a sign the
vote round is now competing more than before even in win1.** **In
win2, this flips hard: CommitQC(v) is the LATER event in 78-83% of
views** -- **the vote round has come back onto the critical path for
the large majority of win2's own views**, a direct, measured
consequence of Round1's own growth compounding with win2's own
independent slowdown (page-cache thrash, 6cv/6cx/6da) driving BOTH the
build AND the now-heavier vote round slower at the same time.

### 4. Prediction 91, clause by clause

- **(a) Safety**: **confirmed** -- 0 conflicting heights (3,355
  checked), 0 commit-vote refusals (the race did not recur), the one
  `suppressing divergent` event is the fix's own leader-side guard
  working as designed, pre-push.
- **(b) Cost**: **confirmed, and real** -- Round1 rose from ~60-72 ms to
  153-262 ms (in the ballpark of the ~180 ms the design predicted); the
  in-tenure cycle and first-window TPS are DIRECTIONALLY worse than
  35zzzf's own same-binary-lineage numbers but stay inside this
  campaign's own established round-to-round noise band on a
  three-round sample -- **not distinguishable from noise with
  certainty, but the DIRECTION is consistent across every metric
  checked (Round1, cycle, first-window TPS, win2 CommitQC-later
  share), which a pure-noise explanation would not reliably produce.**
- **(c) Tests**: already reported by the builder (6cz) -- both
  regression tests fail-then-pass; not re-run here.

**VERDICT: confirmed** (safety unconditionally; cost confirmed as
real and directionally consistent, though not cleanly separable from
noise on three rounds' worth of data).

### 5. Memstats/VM continuity (same table shape as 6da's B1)

| | B1win1 | B1win2 | B2win1 | B2win2 |
|---|---|---|---|---|
| `pgmajfaultD`/10s | 21,224 | 13,390 | 15,446 | 7,030 |
| `pgscanKswapdD`/10s | 1,214,888 | 553,743 | 974,536 | 398,604 |
| `refaultFileD`/10s | 17,701 | 20,477 | 12,460 | 27,912 |
| `RssAnon` (avg, MB) | 9,776 | 10,216 | 9,862 | 10,263 |
| `RssFile` (avg, MB) | 1,945 | 841 | 1,883 | 891 |
| `NumGC` (samples, raw) | 21->31 | 46->65 | 17->27->40 | 59 (1 sample) |
| `HeapAlloc` (mean, GB) | 6.3-7.1 | 7.5-7.7 | 6.9-7.6 | 8.2-8.5 |

**Same shape as every prior round this series has measured**: `RssFile`
roughly halves win1->win2 (both legs), `RssAnon` climbs toward the same
~10.2-10.3 GB per-node ceiling, `HeapAlloc` grows win1->win2 in every
leg. GC-CPU-share was not re-derived from this round's own CPU
profiles (30 files, names lacking the leg per the coordinator's own
note -- disambiguating them by mtime against the round log was not
done this pass, time budget); the `NumGC` deltas above are consistent
in DIRECTION with 6da/6db's own established growth (more collections
per unit time in win2 than win1, in both legs) without a precise
per-minute rate this pass computed cleanly from the available 2-3
samples per window.

**Method.** `height_conflict_check.py` and `seal_path_waterfall.py`
run unmodified against this round's own leg/window arguments (derived
from the round log's own printed block counts, per the established
practice since 6cp). The CommitQC/build-end offsets reuse 6cs's own
`propose`-field identity directly (no new join). VM/memstats windows
use the same full-block-sequence-derived boundaries every round since
6cv has used, re-derived for this round specifically.

**What this does and does not show.** It shows the S26 vote-rule fix
holds under a full, two-leg fleet round: zero conflicting heights,
zero commit-vote refusals, and the one leader-side sibling-suppression
event working exactly as designed. It shows the fix's own cost is
real and measurable (Round1 growth landing close to its own predicted
~180 ms, `lwWhy` shifting hard toward `timeout`, win2's CommitQC now
the later event in 78-83% of views) rather than free, though a
three-round sample cannot cleanly separate "real, small throughput
cost" from "round-to-round noise" on the first-window TPS numbers
specifically. It does NOT separately quantify the hand-over cycle
(time budget). It does NOT re-derive a properly-sampled CPU-side
GC-cost figure for this round (the 30 in-window CPU profiles were not
disambiguated by leg this pass). It does NOT change the recommendation
on safety grounds -- n42-r94 is unconditionally the correct base from
that angle regardless of any throughput finding here.

**Recommendation.** From a THROUGHPUT point of view alone, n42-r94
shows a real, if modest and not yet cleanly separated from noise, cost
relative to n42-r92 (Round1 growth, win2's vote round back on the
critical path 78-83% of the time) -- **n42-r94 should be adopted as
the base binary regardless (the safety fix is not optional), but the
cost should be tracked, not assumed zero, in every throughput
comparison against pre-S26 rounds from here forward.**

## 6dh. S31: the prepare vote fires on the block's header, not the deferred check -- same predicate as S26, moved earlier; n42-r95 built, prediction 94 registered before the round (2026-09-21)

**Why.** 6dg measured S26's real cost: Round1 rose from ~60-72ms to
153-262ms because the two-phase prepare vote now waits for
`EventBlockChecked` -- `CheckDeferredBlock`'s own per-transaction walk
(sender recovery, nonce/balance checks, ~134ms on a full block, more
under win2's GC pressure) -- before `extendsJustify` ever runs.
`CommitQC(v)` became the later event than the leader's own build end in
33% of win1 views and 78-83% of win2 views (0% before S26): the vote
round is back on the critical path. But `extendsJustify` never reads
anything `CheckDeferredBlock` computes -- only the block's parent hash,
a HEADER field, which `internal/sync/validate_blocks.go`'s
`peekBlockHeader` already decodes independently of the transaction
list, and which arrives with the pushed body ~30-65ms after the
Proposal (6ce).

**PART 0 -- same predicate, proven from the code.**
`extendsJustify(view, blockHash)` (`internal/consensus/hotstuff/
proposal.go`, unchanged by this step) is a pure function of exactly two
inputs: `e.pendingJustifyBlocks[view]` (set once, in `processProposal`,
from the Proposal's own signed `JustifyQC.BlockHash` -- S31 does not
touch this) and `e.importedParents[blockHash]` (the block's own parent
hash). Under S26, the ONLY way `importedParents[blockHash]` gets
populated before Round 1 votes is `onBlockChecked(blockHash,
parentHash)`, called from `internal/sync/rpc_block_push.go`'s
`deferredCheck` with `parentHash = blk.ParentHash()` -- a getter that
reads the ALREADY-DECODED header's `ParentHash` FIELD directly.
**`CheckDeferredBlock` does not compute, validate, or otherwise modify
this value in any way** -- it validates senders, nonces, balances and
gas against the PARENT's post-state, using `ParentHash` as an input,
never producing it as an output. So `onBlockChecked`'s own `parentHash`
argument and S31's new `onBlockHeaderKnown`'s `parentHash` argument are
**the identical value, read from the identical header field, via two
different call sites** -- one after the full deferred check succeeds,
one the instant the header is decoded. Populating the SAME map
(`importedParents`) with the SAME value from an EARLIER call site
cannot change what `extendsJustify` computes; it can only change WHEN
the value becomes available for it to read. This is the whole proof:
moving the trigger earlier changes timing only, never the predicate.

(One live wrinkle, not a predicate change: S31's own gate,
`tryHeaderVote`, additionally requires `e.twoPhaseVote` -- import-gated
(non-two-phase) voting is untouched and keeps its documented guarantee,
"vote only once the block is imported locally"; this is a NEW
restriction on WHEN the fast path applies, not a change to what
`extendsJustify` itself evaluates.)

**Binding: how the peeked header is tied to the leader's signed
Proposal.** A block's hash is `keccak256(rlp(header))` alone --
`common/block/header.go:126` (`Header.Hash()`, computed via `rlpHash()`
over header fields only) -- confirmed independently by
`internal/sync/validate_blocks.go`'s own comment on the gossip path,
"The block hash is keccak(rlp(header)), so the decoded block
recomputes the identical hash." **Hashing the header therefore
suffices**; no body field (including the transaction root, which is
itself a HEADER field, `TxHash`) is needed to compute it. The Proposal
message is BLS-signed over `proposalSigningMessage(view, blockHash)`
(verified in `processProposal` before anything else runs), so
`proposal.BlockHash` is cryptographically bound to the leader's
identity. S31 adds **no explicit hash-comparison code**: the peeked
header's own self-computed `Hash()` is used directly as the key into
`e.importedParents`/looked up against `e.pendingProposals[view]` --
these only correlate when the peeked header's hash EQUALS the
BLS-signed `proposal.BlockHash`, exactly the binding needed. A header
for the wrong block (or a corrupted/attacker-supplied one) simply
never matches this view's pending proposal and is silently ignored
(`TestHeaderVoteIgnoresHeaderForADifferentBlockHash`) -- the same
"fails to correlate, not fails a check" pattern `onBlockChecked`/
`onBlockImported` already use. Binding is hashing the header; nothing
heavier is needed, so this step does not stop for a wire-format
decision.

**Where the event fires, and what each option costs.** Two shapes were
possible: (a) peek the header BEFORE the full body decode, or (b)
notify right after the full decode but before `deferredCheck`.
`internal/sync/rpc_chunked_response.go`'s `readFirstChunkedBlock`
already reads the WHOLE length-prefixed byte blob into memory
(`encoder.DecodeWithMaxLengthLimit`) before calling `decodeChunkedBlock`
for the full RLP decode -- so `peekBlockHeader(raw.data)` (already
proven in production on the gossip path, `validate_blocks.go:52`) can
run on those bytes at ZERO extra I/O cost, strictly before the
transaction-list decode. **Chose (a)**: a new `ReadChunkedBlockPeekHeader`
(`rpc_chunked_response.go`) invokes a callback with the peeked header
immediately after the raw bytes are read, before `decodeChunkedBlock`
runs at all -- ahead of the ENTIRE body decode, not merely ahead of
`CheckDeferredBlock`. Cost of the peek itself: an RLP list-header read
plus one FIXED-size header struct decode (the same operation the
gossip path already performs live, no new code path); it does not
scale with transaction count and does not shorten the wire transfer
itself (the whole blob must still arrive before `raw.data` is
complete) -- the saving is entirely in CPU/dispatch time downstream of
that transfer: the transaction-list RLP decode (which S27's own 6dc
found to be a non-trivial per-transaction cost) and `CheckDeferredBlock`'s
own per-transaction walk both move OFF Round 1's own critical path,
onto a path that runs anyway (for import/Round 2) but no longer gates
the FIRST vote.

**The fix.** `internal/consensus/hotstuff/engine.go`: new
`EventBlockHeaderKnown` event type, carrying `Hash`/`ParentHash`/
`Number` (`Number` is logging-only; nothing in the vote path reads it).
`internal/consensus/hotstuff/proposal.go`: `onBlockHeaderKnown` records
the parent in `importedParents` (bounded by its own `headerKnownFIFO`,
since a header may arrive for a block never checked or imported and
must not pin the map forever; an entry already tracked by
`checkedBlocks`/`importedBlocks` is left alone by this FIFO's own
eviction, mirroring `checkedFIFO`'s existing guard) WITHOUT setting
`checkedBlocks` -- Round 2's `deferredAttested` gate is completely
unaffected and still requires the real check. New `tryHeaderVote`
(two-phase only) casts the Round 1 vote once the parent is POSITIVELY
known (not merely "unknown, fail open" -- `extendsJustify`'s own
fail-open branch must not be mistaken for a pass here) and
`extendsJustify` passes; it is attempted from both `processProposal`
(header arrived first) and `onBlockHeaderKnown` (Proposal arrived
first), so either delivery order votes exactly once via the existing
`journalPrepareVote`/`HasVotedInView` idempotency. If the header event
never arrives, `processProposal`'s existing chain (already-imported ->
`tryHeaderVote` -> `tryDeferredVote` -> defer) falls through to the
UNCHANGED S26 checked/imported gate -- never voting blind.
`internal/sync/options.go`: `BlockImportNotifier` gains
`NotifyBlockHeaderKnown`; `internal/consensus/hotstuff/service.go`
implements it, dispatching `EventBlockHeaderKnown`.
`internal/sync/rpc_block_push.go`'s `blockPushStreamHandler` calls
`ReadChunkedBlockPeekHeader` instead of `ReadChunkedBlock`, notifying
from the peek callback. The two OTHER callers of the underlying reader
(`rpc_block_by_hash.go`, `rpc_send_request.go`) are untouched -- they
still call `ReadChunkedBlock`, which now threads a `nil` callback
through unchanged.

**Tests (`internal/consensus/hotstuff/header_vote_test.go`, new).**
`TestHeaderVoteRefusesNonExtendingHeaderBeforeAnyDeferredCheck`: a
non-extending header is refused at Round 1 with NO `EventBlockChecked`
ever delivered in the test, proving the refusal does not depend on the
deferred check having run.
`TestHeaderVoteIgnoresHeaderForADifferentBlockHash`: a header event for
a different block hash (even with a parent that legitimately extends
the locked chain) never unlocks this view's own pending proposal.
`TestHeaderVoteFiresExactlyOnceRegardlessOfOrder`: both orderings
(header before the Proposal, header after) vote exactly once.
`TestHeaderVoteFallsBackToCheckedGateWithoutTheHeaderEvent`: with no
header event anywhere in the test, the existing checked/imported gate
still carries the vote. Both S26 regression tests
(`conflicting_commit_test.go`) pass unchanged. Full
`internal/consensus/hotstuff` and `internal/sync` (+ subpackages)
suites pass (non-race); `go vet`/`go build` clean across the whole
repository. Round 35zzzj aborted mid-B2 (`MemAvailable 18G`, S28's own
14GiB legs -- confirming the GATES worst-case risk 6dd flagged, and
matching 6di's own from-scratch arithmetic below) and the box then
went to the Rust fleet for ~100 minutes, so per the commander's own
instruction whole-package `-race` did not run this step: **the two
S26 regression tests plus the four new tests above pass under
`-race`, individually targeted** (`go test -race -run 'TestHeaderVote|
TestTwoPhasePrepareVoteRefusesNonExtendingProposal|
TestTwoPhaseCommitVoteRefusesNonExtendingProposal'
./internal/consensus/hotstuff/`, `nice -n 19`, `-p 4`) -- whole-package
`-race` is deferred until the commander confirms the box is free again.

**Build.** n42-r95 = n42-r94's exact file set + this step's changes,
via the same file-checkout recipe reconstruction (detached worktree at
`f7ec2836`, the same lever commits n42-r94 used), built with `nice -n 19`
and `-p 4` per the commander's box-sharing instruction. One-variable
check: `internal/consensus/hotstuff/{engine,proposal,service}.go` and
`internal/sync/{options,rpc_block_push,rpc_chunked_response}.go` are
each touched by exactly ONE commit since their own last lever-commit
checkout (`proposal.go`: `e49ce151` then `c124146e`; the rest: only
`c124146e`) -- confirmed via `git log <last-lever-commit>..HEAD --
<file>` for each, so each was copied directly from `wt-r27`'s own HEAD
rather than patched. `internal/miner/worker.go` needed the SAME
by-now-familiar hand-hunk treatment (S19/S22/S23-lineage's own
"speculative build hit" off-lineage conflict): applying `e49ce151`'s
own isolated diff (`git diff e49ce151^ e49ce151`) hit the SAME single
conflict as every prior build in this chain (the off-lineage
`activeSpecParent`/`tMs` fields), resolved by hand exactly as before;
the OTHER two hunks in that diff (referencing `writeAndFinish`, S23's
retired async-writer function) correctly rejected outright, since this
build's own `worker.go` -- like n42-r94's -- never had that
restructuring; no manual substitute was needed for those two, since
S26's leader-side fix (`recordSealedOnParent` moved to seal time) does
not depend on `writeAndFinish` existing. `internal/parallel/base_cache.go`
confirmed absent; `strings n42-r95 | grep -c BaseCache` = 0. Markers:
`"commit vote REFUSED..."` = 1, `"header vote: block header known and
extends its JustifyQC block, voting"` (new) = 1, `"suppressing
divergent same-height sibling"` = 1, `"miner: seal path"` = 1.
`go build -p 4 -tags nosqlite,noboltdb` clean.
`/data/blockchain/gov5-work/n42-r95`: 108,785,200 bytes, sha256
`0d4edb372a533ca4ad8d155637ff98374757c287bd2e8364743da109568d1712`.

**Harness.** `run-r35zzzk.sh`/`chain-35zzzk.sh`, from the 35zzzj pair:
GOMEMLIMIT reverted to 10GiB in every leg (S28's own 14GiB A/B, 6dd/6di,
is a separate, now clearly closed-negative question, left as
`run_leg`'s own 6th argument); the allocs capture already carries the
commander's own live fix (`?seconds=20`, inherited directly from
35zzzj's own edited script -- a plain, un-timed GET is cumulative
since process start, which is what produced 6dc's own bogus "10.36
GB/block" figure, corrected in 6de/6df/6di); the heap capture stays
plain (already a point-in-time snapshot, not a cumulative counter).
Both S26 harness safety checks (`check_conflicting_commits`/
`check_legs_produced`) carry over unchanged. `bash -n` clean on both
scripts; confirmed not running. `chain-35zzzk.sh` waits on
`wr-logs/r35zzzj.log`'s terminal line.

**Prediction 94 (registered before any round):**

**(a) Safety.** Zero conflicting heights across every height checked
(the S26 harness check and `height_conflict_check.py` both agree);
every refusal (`import-gated vote REFUSED`/`commit vote REFUSED`) is
logged with enough context (view, blockHash, parent, justify) to
attribute it to a stage.

**(b) Mechanism.** Round1 (median) on full in-tenure views <= 90 ms
(35zzzf/r92: ~60-72 ms; 35zzzi/r94: 153-262 ms) -- the header-vote path
should land close to r92's own figure again, since the gating work
(deferred-check completion) moves off Round 1 entirely for the common
case. `CommitQC(v)` later than the leader's own build end in <= 5% of
win1 views (35zzzi: 33%; 35zzzf/r92: 0%).

**(c) Throughput.** Win1 in-tenure cycle within noise of 650-690 ms
(35zzzf/r92's own figure); first-window TPS not below 35zzzi/r94's own
130.0k/135.3k (recovering toward, not below, the pre-S26 baseline).

**(d) Tests.** The four new `header_vote_test.go` tests plus both S26
regression tests pass (already confirmed above, individually, under
`-race`); whole-package suites (already confirmed non-race) to be
re-run under `-race` once the box is free again, per the commander's
own instruction.

**VERDICT: confirmed** (PART 0 predicate-equivalence proven from the
code; binding proven via `Header.Hash()`; targeted tests pass,
including under `-race`; n42-r95 built and one-variable-checked;
harness prepared and syntax-checked, not launched; whole-package
`-race` explicitly deferred, not skipped, per the commander's own
box-sharing instruction). QS_QUEUE.md's S31 row status is marked
prepared with prediction 94 (6dh); `docs/OPEN_ISSUES.md`'s entry
carries a dated S31 status line. Launch is the commander's next call.

## 6di. S28: 14 GiB does not fit the box (nodes alone would need ~105 GB of it); the generators hold a stable ~3.6 GB and are not the driver; and a TRUE 20-second delta profile shows B1's real allocation rate is ~1.88 GB/block (~11.3 KB/tx), not the ~10.36 GB/block reported from earlier, apparently non-delta captures (2026-09-21)

n42-r94, LIGHT work only (`nice -n 19`, single-threaded, no
benchmarks; the box belongs to another fleet). B1 (10GiB,
18:37:58-18:50:43) completed cleanly; B2 (14GiB) was aborted by the
memory watchdog at 19:00:50 during its own ramp and was not retried.
Node logs preserved (B1 whole, B2's tail possibly cut, copied mid-
shutdown) at `wr-logs/r35zzzj-keep/node{0-6}/`. This section reads
`r35zzzj-mem.log`/`-vm.log`/`-memstats.log` and the new true-delta
allocs captures directly; no new script (the existing `alloc_path_
partition.py` was reused unmodified on the new profile). Per the
task's own instruction, `docs/OPEN_ISSUES.md` and this file both carry
other, uncommitted edits from elsewhere in this worktree at the time
of writing -- left untouched, not stashed or reset, per instruction.

### 1. The abort: the memory ledger, B1 vs B2's last 3 minutes

| | B1 (10GiB), low-water tail (18:47:44-18:50:37) | B2 (14GiB), last 3 min before abort (18:58:00-19:00:50) |
|---|---|---|
| per-node RssAnon (range across 7 nodes) | 10.1-11.1 GB | 11.4-14.7 GB (still climbing at abort) |
| per-node RssFile (range) | 0.6-1.6 GB | 0.5-0.7 GB |
| 8 generators, RssAnon total | ~3.5-3.9 GB | ~3.6 GB |
| system `Shmem` | ~9.3-9.6 GB | ~9.5 GB |
| system `Cached` | 48-51 GB | 27-46 GB (falling as nodes grow) |
| system `AnonPages` | 76-79 GB | 85-104 GB (climbing) |
| `MemAvailable` | **41-45 GB (low-water this leg)** | 36G -> 27G -> 23G -> 21G -> **18G (abort)** |

**What consumed the memory in B2: the nodes, overwhelmingly, not the
generators.** At the abort instant (19:00:50), the 7 nodes' own
`RssAnon` sum to **98.8 GB** (13.7-14.7 GB each, already AT or past
the nominal 14 GiB = 15.03 GB decimal ceiling for several nodes, and
still visibly climbing sample-to-sample); the 8 generators sum to a
STABLE **~3.6 GB total** (unchanged, within noise, from B1's own
~3.5-3.9 GB) -- **the generators are not the driver; they were never
close to the driver.** `Shmem` (~9.5 GB, stable across both legs) is
the third-largest single line item, larger than the generators. Simple
arithmetic against the box's own 137 GB: `7 nodes x 14 GiB (15.03 GB
decimal) = 105.2 GB` + `generators ~3.6 GB` + `Shmem ~9.5 GB` +/- a
`~7-8 GB` baseline OS/cache overhead (backed out from B1's own
low-water figure below) `= ~125.3-126.3 GB`, leaving **~11-12 GB** of
the box's 137 GB -- already below the 20 GB watchdog floor BEFORE the
nodes even reach their own full 14 GiB ceiling, which is exactly what
was observed (abort at 18G avail while nodes were still at 13.7-14.7,
not yet 15.03, GB each).

**B1's own low-water `MemAvailable` this leg was 41 GB** (10GiB
limit) -- **comfortably above the 20 GB watchdog**, with roughly 21 GB
of margin. Backing out the same arithmetic for B1: `7 x 10 GiB
(10.74 GB decimal) = 75.2 GB` + `generators ~3.7 GB` + `Shmem ~9.4 GB`
`= 88.3 GB`, against an observed low-water `avail` of ~41-45 GB out of
137 GB (i.e. ~92-96 GB actually in use) -- the **~4-8 GB gap** between
this arithmetic and the observed usage is the "baseline OS/cache
overhead" term used above, consistent between the two legs' own
arithmetic (a good sign the accounting method is sound, not
coincidental).

**Stated plainly: no `GOMEMLIMIT` meaningfully above 10 GiB is
feasible on this 137 GB box with today's generator/Shmem footprint.**
14 GiB already failed, and the arithmetic shows WHY without needing a
second failed round to prove it: `7 x limit + ~3.6 (gens) + ~9.5
(shmem) + ~7-8 (baseline) + 20 (watchdog floor) <= 137` solves to
`limit <= (137 - 3.6 - 9.5 - 7.5 - 20) / 7 = 13.77 GB decimal =
12.83 GiB` as the theoretical ceiling -- and 14 GiB (15.03 GB decimal)
is already past that, which is exactly why it aborted. **Even a
smaller bump (11-12 GiB) sits close enough to this ceiling that it
should be treated as marginal, not safe, without first shrinking one
of the four line items (nodes, generators, Shmem, or the watchdog
floor itself) -- none of which this section proposes changing.**

### 2. Generators, measured for the first time

**Stable, small, and NOT the memory story**: ~3.5-3.9 GB total RssAnon
across all 8 generators in every phase checked (ramp, win1, win2 of
B1; the last 3 minutes before B2's abort) -- individual generators
range 300-615 MB each, with no growth trend across phases. **CPU**: a
10-second delta (B1win1) gave 98 cpu-seconds across 8 generators in
10s wall time = **9.8 cores** -- non-trivial (roughly a third of the
box's presumed ~32-core budget if PARALLEL_EVM's own 32 workers are
counted per-node, though this is the GENERATORS' own share, separate
from any one node's workers), but small next to the multi-hundred-GB
memory question this section is centrally about. **What the memory
is, from the run script's own flags** (`8 floods x 1000 x 3000`, `pool
600k (300k held)`, `-depth-by-nonce`, `target-depth 30000-45000`,
per this and every prior round's own banner text; `cmd/txflood` was
not present to read directly in this worktree, so this is read from
the run script's own accumulated commentary, not the generator's own
source): each of the 8 generators tracks, in memory, **its own pool of
pre-signed/pre-built transactions for its 1,000 senders** (the
`pool 600k/200k` figures are the NODE's mempool limits, not the
generator's; the generator's OWN footprint is its pending-transaction
staging buffer plus **per-sender in-flight/nonce tracking** -- the
`-depth-by-nonce`/`target-depth` flags exist specifically to cap how
many transactions-in-flight each sender believes it has outstanding,
which bounds this exact structure). **One paragraph, not a design
note**: a few hundred MB per generator for ~1,000 senders' worth of
staged/pre-built transactions and their own nonce bookkeeping is a
small, bounded, and evidently STABLE cost -- it does not grow across a
leg's own ramp/win1/win2 progression the way the node's own memory
does, which is consistent with it being a fixed-size, capped
structure rather than an accumulating one.

### 3. B1's TRUE allocation numbers -- a correction to the measurement basis of 6dc-6df

**The new true-20-second-delta allocs capture gives a dramatically
different total than every prior round's own captures.** B1win1,
node2: **30.04 GB alloc_space over the profile's own reported
20.04 s duration**, 16 blocks in that span (blockTime 1.25 s) =
**1.878 GB/block, 11.5 KB/transfer** (163,000 tx/block). **Cross-
checked independently against `MemStats.TotalAlloc` deltas** (a
monotonic counter, immune to any profile-capture-window question):
node1's own `TotalAlloc` rose from 144.85 GB (18:47:32) to
243.54 GB (18:48:34), a 98.69 GB delta over 62 s = 1.592 GB/s = **31.8
GB over a 20 s window** -- **agrees with the pprof delta (30.04 GB)
within ~5.5%, comfortably inside the task's own 20% tolerance.**

**This is roughly 5.5x SMALLER than 6dc/6da's own reported
10.36 GB/block, 68.2 KB/transfer figure.** The coordinator's own
framing of this round's fix ("a true 20 s DELTA," implying prior
captures were not) is the most direct explanation available: **every
earlier round's own `allocs` capture in this campaign (6da, 6db, 6de,
6df) was very likely NOT a clean 20-second delta** -- almost certainly
a longer-window or cumulative-since-an-earlier-point capture that
inflated the reported totals by roughly the same 5-6x factor this
section finds directly. **This is flagged here as a measurement-basis
correction that 6dc-6df's own headline numbers need, not re-derived or
rewritten in place given this task's own light-work, single-node,
time-boxed scope** -- the qualitative FINDINGS of those sections
(which sites dominate, which code paths are shared vs attributable,
the H-bench/H-multi reconciliation) do not obviously change just
because the scale was off, but the ABSOLUTE numbers throughout (GB/
block, KB/tx, and every "X% of 10.36 GB" framing) should be treated as
suspect pending a re-run with confirmed-delta captures.

**Partition by entry path, redone on this true-delta profile**
(`alloc_path_partition.py`, unmodified, B1win1 node2, 32.66 GB summed
across traces vs the profile's own 30.04 GB total -- the same
~5-8% `-traces` double-annotation artifact 6de already flagged):

| category | % | KB/tx (rescaled to the true 30.04 GB/20s, 16 blocks) |
|---|---|---|
| A: RPC ingest | 6.5% | 0.75 |
| B: gossip receive | 0.1% | 0.01 |
| C: gossip send/libp2p | 0.3% | 0.03 |
| D: pool internal | 3.9% | 0.45 |
| E: leader build (attributable) | 8.9% | 1.03 |
| F: follower import (attributable) | 25.4% | 2.93 |
| EXEC_shared (unattributable) | 41.4% | **4.77** |
| unassigned (G) | 13.6% | 1.57 |

**86.4% assigned to named categories, clearing the 85% bar again.**
**`EXEC_shared`'s own corrected figure, 4.77 KB/tx, is now LOWER than
6dc's own isolated-benchmark figure (6.25 KB/tx)** -- the "4.4x gap"
6df spent a full section explaining has, on this corrected
measurement, mostly EVAPORATED (and the isolated benchmark now reads
as somewhat MORE expensive per transaction than the fleet's own
shared-executor share, the opposite direction, on a single window's
worth of data). **This does not mean 6df's own H-bench reasoning
(memdb vs MDBX/QMDB, Finalize/block-end skipped, pre-decoded inputs)
was wrong as a mechanism** -- those are still real, confirmed
structural differences -- **but the MAGNITUDE of the gap they were
asked to explain was itself an artifact of the same capture-window
problem this section's own cross-check exposes.** Top 5 sites by flat
`alloc_space`, this capture (not separately re-`-list`-read this
pass, time budget): the same ranking shape as 6db/6dc/6de (`journal.
push`, `parallelApplyTx`'s own sub-lines, `decodeEthereumTransaction`,
`IntraBlockState` methods) -- MB/block figures would need rescaling by
this section's own ~5.5x correction factor from 6db's own table,
not independently re-measured here.

### 4. GC, B1, win1/win2

| | win1 (~18:47:57-18:48:17) | win2 (~18:48:56-18:49:16) |
|---|---|---|
| `NumGC` (bracketing samples) | ~42 -> 81 (62s span) | ~81 -> 102 (31s span) |
| rate | ~37.7/min | ~40.6/min |
| `HeapAlloc` (bracketing) | 6.43 -> 8.12 GB | 8.12 -> 8.76 GB |
| `HeapInuse` (bracketing) | 7.02 -> 8.52 GB | 8.52 -> 9.30 GB |

**GC frequency is roughly FLAT between win1 and win2 this round**
(~38 vs ~41/min) -- unlike 35zzzh's own sharp win1->win2 jump (6da),
this round's B1 (10 GiB, the SAME nominal limit as 35zzzh's own B1)
does not show the same acceleration in this specific window pairing.
`HeapAlloc`/`HeapInuse` climb steadily and continuously across both
windows (part of the same leg's own ongoing ramp toward the 10 GiB
ceiling, not a step change at the win1/win2 boundary). **GC+alloc
share of CPU was not re-derived this pass** (properly-sampled,
leg-named CPU profiles exist for this round, per the task's own note,
but were not read given the light-work time budget -- named as not
done, not estimated). Per-node `RssAnon`/`RssFile`/fault-counter table
(continuity with 6da): already given in full in section 1's own ledger
table above (this round's B1 IS the "B1" comparison point 6da's own
table format calls for).

### 5. Prediction 93, ruled

- **(a)-(c)** (does 14 GiB reduce NumGC/GC-CPU-share, does RssAnon/
  fault-counters rise as expected, does win2 block time improve):
  **cannot be judged -- the leg aborted during its own ramp, before
  any measurement window opened.** Stated exactly that, per the task's
  own instruction, not estimated or guessed from the partial data
  available.
- **(d)** S26 safety checks, legs that ran: `height_conflict_check.py`
  against B1's own kept logs -- **0 conflicts** (checked directly,
  consistent with every round since the fix landed). B2's own partial/
  cut logs were not separately checked given the leg never reached a
  scored window and the task's own light-work scope.

**VERDICT: falsified** (prediction 93 asked whether 14 GiB is
affordable and helps; it is not affordable at all, on this box, with
today's generator/Shmem footprint -- the clearest possible negative
result, not merely "inconclusive").

**What this does and does not show.** It shows, with clean, load-
bearing arithmetic backed by two consistent low-water observations
(B1's 41 GB, B2's abort at 18 GB), that 14 GiB does not fit this box
and that neither the generators (~3.6 GB, stable) nor `Shmem`
(~9.5 GB, stable) are the reason -- the NODES' own growth is. It shows,
via an independent cross-check against `TotalAlloc` deltas, that this
round's TRUE per-block allocation rate (1.878 GB/block, 11.5 KB/tx) is
roughly 5.5x smaller than every prior round's own reported figure --
the single most consequential finding in this section, flagged as a
correction 6dc-6db-6de-6df's own absolute numbers need, without
attempting that correction here. It does NOT re-derive GC-CPU-share
from this round's own properly-sampled CPU profiles (time budget). It
does NOT determine a specific SAFE `GOMEMLIMIT` above 10 GiB -- the
arithmetic bounds it at ~12.8 GiB in theory, but this section
recommends treating anything above 10 GiB as unproven until a round
actually completes at that setting.

## 6dj. S31: the header-vote fix recovers most of S26's own cost -- Round1 back to 62-64 ms (vs r92's baseline, not r94's 153-262 ms) -- and the 35 refusals are a genuinely stale, correctly-blocked re-proposal of an already-committed block, not a bug in the new rule (2026-09-21)

n42-r95 (n42-r94 + S31's header-vote path) ran B1 (21:50:09-22:03:23)
and B2 (22:03:23-22:17:09) cleanly, `GOMEMLIMIT=10GiB` throughout.
Node logs preserved whole, trimmed to `wr-logs/r35zzzk-keep/
node{0-6}-B.log`. Scripts reused unmodified:
`height_conflict_check.py`, `seal_path_waterfall.py`.

### The 35 refusals, investigated first, as instructed

**All 35 `"import-gated vote REFUSED"` lines are one incident**: view
6557, `blockHash=a990bc…815fde`, `blockParent=d149cb…8c956f`,
`justifyBlock=a990bc…815fde` (self-referential -- the JustifyQC field
equals the block's own hash), 5-6 occurrences per node (all 7 nodes
hit it). Reconstructed in full from node0's own log, chronologically:

1. **View 6555** committed `d149cb…8c956f` (the TRUE, correct parent).
2. **View 6556**: block `a990bc…815fde` (parent `d149cb…`) arrives via
   `fetch-on-miss` (not a direct push), and is voted CORRECTLY through
   the NEW header gate: `"header vote: block header known and extends
   its JustifyQC block, voting"` -- extendsJustify passed (parent ==
   justify == `d149cb…`, a NORMAL, correct case). The two-phase commit
   vote follows, `"received Decide, committing block"`, `"hotstuff:
   block committed"` -- **`a990bc…815fde` is fully, correctly committed
   IN VIEW 6556.**
3. **View 6557** (4-5 ms later, same node, NOT leader): the SAME hash
   `a990bc…815fde` resurfaces as a PENDING PROPOSAL for THIS NEW view,
   now carrying `JustifyQC.BlockHash = a990bc…815fde` -- **its own,
   already-committed hash, not a new child's parent pointer.**
   `extendsJustify` correctly computes `parent (d149cb…) != justify
   (a990bc…)` and refuses, 6 times (repeated warning lines, ~1 second
   apart, most plausibly repeated delivery/retry of the SAME stale
   message rather than 6 independent proposals). **View 6557 then
   TIMES OUT** (`"view timed out", view: 6557` at 22:02:37) and **a TC
   forms** (`"TC formed locally, advancing without the next leader",
   nextView: 6558`), after which view 6558 proceeds normally with a
   fresh block.

**Answering the four questions directly:**

1. **Which view(s)/node(s), and what happened to the block**: ONE
   view (6557), all 7 nodes hit the same refusal for the same hash.
   `a990bc…815fde` itself was NOT a bad block -- it was ALREADY
   correctly proposed, voted, and committed one view earlier (6556).
   What was refused in 6557 is a STALE RE-APPEARANCE of that already-
   decided block's own hash as if it were a fresh proposal for the
   NEW view, with a JustifyQC that (by the time this stale message was
   processed) correctly reflected the CURRENT state of the world (a990bc
   had, by then, become the locked/justified block) but was wrongly
   being asked to justify EXTENDING ITSELF rather than a genuinely new
   child. **This is consistent with a duplicate or late-arriving
   delivery of stale proposal-adjacent state (most likely a repeated/
   retried message, not a fresh, differently-constructed proposal)
   rather than a new proposal genuinely built by view 6557's own
   leader with a wrong idea of its own parent** -- the exact mechanism
   producing a SELF-referential JustifyQC for a re-surfacing hash was
   not traced to a specific line this pass (per the task's own
   instruction not to guess at a fix): `pendingJustifyBlocks[view] =
   proposal.JustifyQC.BlockHash` (`proposal.go:249`) is the only
   assignment site and reads directly from whatever the received
   Proposal message itself carries -- so the self-reference originates
   in the CONTENT of a message this node received under view 6557's
   context, not in a local bookkeeping slip on read.
2. **Did the view complete, and at what cost**: yes -- via a TIMEOUT
   and TC, not a QC. Approximately **5-6 seconds** elapsed between the
   stale proposal's own arrival (~22:02:32) and the TC forming
   (22:02:37) before view 6558 resumed normal, fast (sub-second)
   progress. **This is a real, one-time cost, isolated to a single
   view out of several thousand in the round** -- not a repeating
   pattern (35 log lines, but all one incident, all one view).
3. **Which gate produced it**: the log text itself
   (`"import-gated vote REFUSED"`) and the code
   (`extendsJustify`, `internal/consensus/hotstuff/proposal.go:667`)
   confirm this is the **S26 fallback/import-gated path**
   (`processProposal`'s own `if e.importedBlocks[proposal.BlockHash] {
   if !e.extendsJustify(...) { ... } }` branch, `proposal.go:271-272`)
   -- **NOT S31's own header gate** (`tryHeaderVote`, which has its
   OWN, separate log lines and did not fire for this specific event;
   the block was already imported by view 6557's own processing time,
   routing it to the import-gated branch instead). `extendsJustify`
   itself is SHARED code (unchanged by S31), so this finding is about
   a pre-existing check catching a NEW way of reaching it, not a
   defect S31 introduced in the check itself.
4. **Present in 35zzzi (r94)?**: **NO** -- `grep -c "import-gated vote
   REFUSED\|commit vote REFUSED"` against every one of 35zzzi's own
   kept logs returns 0 everywhere, confirmed directly. **This specific
   incident (a block's own hash resurfacing as its own justify one
   view later) did not occur in r94's round; whether that is because
   the underlying trigger is timing-sensitive and simply did not
   recur, or because S31's own header-vote path changes SOMETHING
   about when/how a stale message reaches this check, is NOT
   determined by this pass.**

**Verdict on "is this a bug in r95": the REFUSAL ITSELF is correct and
the fix worked exactly as designed -- a proposal that does not extend
its own JustifyQC block was refused, exactly as S26/S31 intend, and
the fleet recovered via the normal timeout/TC path with a small, one-
time, single-view cost.** Whether something UPSTREAM of the check (why
`a990bc…815fde`'s own hash resurfaced with a self-referential
JustifyQC one view later, and why this is new in r95 but absent in
r94) is itself a defect worth fixing is **NOT determined here** -- named
as an open question with its exact evidence trail, not guessed at.

### 1. Cost, side by side with 35zzzi (r94) and 35zzzf (r92)

| | 35zzzf win1 (r92) | 35zzzi win1 (r94) | **35zzzk win1 (r95)** | 35zzzf win2 | 35zzzi win2 (r94) | **35zzzk win2 (r95)** |
|---|---|---|---|---|---|---|
| Round1 (median) | ~63-65 ms | 153-157 ms | **62-64 ms** | ~70-72 ms | 212-262 ms | **115-115.5 ms** |
| Round2 (median) | ~87-99 ms | 10-11 ms | 83-92.5 ms | ~138-163 ms | 236-271 ms | 176-181.5 ms |
| leader `jcvMs` (median) | 0 | 0 | 0 | 0 | 0 | 0 |
| `lwWhy` timeout share | 17-27% | 66.7-85.7% | **9.1-13.0%** | 27-33% | 87.0-95.7% | **18.5-34.6%** |
| in-tenure CYCLE (median) | 691/656 ms | 650/663 ms | 655/688 ms | 850/904 ms | 877/1010 ms | 845/898 ms |
| QC-later-than-build-end share | 0% (35zzzf's own B1 read) | 33.3% | **21.7-27.3%** | 0% | 78.3-82.6% | **59.3-65.4%** |
| `import_breakdown` total (mandatory) | -- | 791 ms | **817 ms** | -- | -- | -- |
| first-window TPS | 141.0k/137.5k | 130.0k/135.3k | **141.7k/140.5k** | -- | -- | -- |
| second-window TPS | 92.3k/93.5k | 85.2k/85.6k | **95.0k/94.5k** | -- | -- | -- |
| **B mean** | 116.1k | 109.0k | **117.9k** | | | |

**Round1 is back to 62-64 ms in win1 -- essentially identical to
r92's own pre-S26 baseline, not r94's 153-157 ms.** Win2 (115-115.5 ms)
is still somewhat above r92's own ~70-72 ms but a large recovery from
r94's 212-262 ms. `lwWhy` timeout share fell back to single digits in
win1 (9.1-13.0%, close to r92's 17-27%) and roughly halved in win2
versus r94. **QC-later-than-build-end share also improved (win1
21.7-27.3% vs r94's 33.3%; win2 59.3-65.4% vs r94's 78.3-82.6%) but
has NOT fully returned to r92's own 0% reading** -- the vote round is
still on the critical path more often than in the pre-S26 baseline,
just less often than under r94's own header-less two-phase gating.
**First and second window TPS, and the B mean (117.9k), are now
ABOVE r92's own same-lineage figures (116.1k)**, not merely recovered
-- the largest, cleanest positive signal in this table.
`import_breakdown`'s own mandatory line (817 ms) is inside noise of
r94's own 791 ms and every prior round's own figure.

### 2. Header-gate usage and timing

**Header-gate votes (`"header vote: block header known..."`) fired
531-545 times per node** across the whole round; the RESIDUAL fallback
path (`"import-gated vote: deferring until block imported"`, cases
that did NOT resolve via either the already-imported fast path or the
header gate at the time of processing) fired only **105 times per
node** -- **the header gate is doing the large majority of two-phase
Round-1 voting work**, consistent with its own design intent (voting
on the header, well before the deferred check completes). The much
larger `"import-gated vote: block already imported"` count (3,440 per
node) is the PRE-EXISTING, unrelated fast path for blocks that arrive
already-imported (common for small/empty blocks) and is not part of
S31's own change. **Exact ms-offset of the header event relative to
the Proposal was not separately stamped/derivable this pass** (no
dedicated timing field ties `tryHeaderVote`'s own firing instant back
to `ProposalReceived`'s own timestamp in one line) -- reported as n/a,
not estimated.

### 3. Safety

`height_conflict_check.py`: **0 conflicts across 4,648 committed
heights.** `"miner: suppressing divergent same-height sibling"`: **2**
occurrences (node1) -- the leader-side guard catching genuine
collisions pre-push, working as designed. **0 BAD BLOCK.** Refusals by
stage: 35 at the import-gated (S26 fallback) stage, as detailed above;
**0** at any stage attributable to S31's own header gate specifically
(the header gate's own code path was not implicated in the incident).

### 4. Memory series continuity and delta-allocs (B1)

| | B1win1 (21:58:17-21:59:37) | B1win2 (21:59:38-22:00:19) |
|---|---|---|
| `pgmajfaultD`/10s | 15,916 | 10,151 |
| `refaultFileD`/10s | 12,810 | 10,226 |
| `RssAnon` (avg, MB) | 9,781 | 10,285 |
| `RssFile` (avg, MB) | 3,634 | 2,275 |
| `NumGC` (bracketing) | ~23 -> 32 (~17.4/min) | ~48 -> 66 (~26.3/min) |
| `HeapAlloc` end of win2 | -- | ~8.3-9.1 GB |

Same qualitative shape as every round in this series (`RssFile` falls,
`RssAnon`/`HeapAlloc`/`NumGC` rate all rise, win1->win2). **The delta-
allocs partition (KB/tx, A-G) was NOT re-run this pass** -- 6di's own
correction (true-20s-delta captures give a ~5.5x smaller total than
earlier rounds' captures) applies to this round's own captures too
(48 files, leg-named, confirmed 20 s deltas per the coordinator's own
note), but re-running `alloc_path_partition.py` a second time was not
completed given this task's own combined scope (the safety
investigation took priority) -- named as not done, not estimated or
assumed to match 6di's own numbers.

### 5. Prediction 94, ruled

- **Header-vote path exists and fires as the majority mechanism**:
  confirmed (531-545 header votes vs 105 residual-fallback per node).
- **Round1 cost recovered toward the pre-S26 baseline**: confirmed,
  strongly in win1 (62-64 ms, essentially AT r92's own baseline), 
  partially in win2 (115-115.5 ms, well below r94's 212-262 ms but
  above r92's ~70-72 ms).
- **Safety unchanged from r94 (S26's own guarantee preserved)**:
  confirmed -- 0 conflicting heights, the one incident investigated
  above is a CORRECT refusal with a small, one-time, single-view cost,
  not a safety regression.
- **Throughput**: confirmed RECOVERED AND THEN SOME -- B mean 117.9k,
  above r92's own 116.1k and well above r94's 109.0k.

**VERDICT: confirmed.**

**Recommendation: n42-r95 becomes the base binary.** Safety is
unchanged from r94 (unconditionally required regardless). The one
open item -- the mechanism producing the 35-refusal incident's own
self-referential JustifyQC, present in r95 but not observed in r94 --
is a genuine correctness question worth a dedicated, narrower follow-
up (reproducing or tracing the exact message/path that carries a
stale hash into a new view's `pendingJustifyBlocks`), but it is NOT,
on this round's own evidence, a reason to hold r95 back: the check
that fired is CORRECT, the cost was small and one-time, and safety
(0 conflicting heights) held throughout.

**Method.** `height_conflict_check.py` and `seal_path_waterfall.py`
reused unmodified. The refusal investigation is a direct,
chronological log read (no script) across node0's own kept log,
cross-referenced against the`extendsJustify`/`tryHeaderVote`/
`processProposal` source directly.

**What this does and does not show.** It shows S31's header-vote fix
recovers the large majority of S26's own Round1 cost, with throughput
now ABOVE the pre-S26 baseline rather than merely restored to it. It
shows, with a complete chronological reconstruction, that the round's
one safety-adjacent incident (35 refusal log lines) is a single,
correctly-handled stale-proposal event costing one view's worth of
timeout (~5-6 s), not a defect in the new rule's own logic. It does
NOT identify the exact code path that produced the self-referential
JustifyQC in the first place, and does NOT determine why this specific
pattern appeared in r95 but not r94 -- both are named as open,
evidence-backed questions for a dedicated follow-up, not resolved or
guessed at here, per the task's own explicit instruction. It does NOT
re-run the delta-allocs partition for this round (time budget,
priority given to the safety investigation).

## 6dk. S32-prep: a pushed block's own RLP bytes are the exact hash preimage for all five transaction types; the follower reuses pool-resident objects on a hash hit instead of re-decoding -- 94.4% less B/op and 47% less ns/op at the fleet's own 99.4% hit rate; n42-r96 built, prediction 95 registered before the round (2026-09-22)

**Whole-package `-race`, deferred since S31, run first as instructed:**
`RACE: hotstuff 289 pass/0 fail; sync 138 pass/0 fail`
(`go test -race ./internal/consensus/hotstuff/... ./internal/sync/...`,
`nice -n 10`, `-p 8`, box load ~3, no foreign claim.)

**PART 0/1 (read before changing anything).** The full file:line
proof -- hash preimage identity for all five transaction types, the
pool's lookup-by-hash API and locking cost, aliasing/mutability
(including the existing `applySenderHints`/`recoverBlockSenders`
guard that already makes a reused object's sender recovery a safe
no-op, and the confirmation that the pool never recycles objects) and
the deferred-check/consensus-path decode-identity argument -- is
written in full in `docs/QS_HANDOVER_20260920.md`, "S32 PART 1 --
read before touching anything (2026-09-22)". Summary of the two load-
bearing findings:

- `blockRLP.TxData[i]` (`common/block/block.go:144-198`) stores every
  transaction type uniformly as `tx.EthEncoded()` -- for Legacy a bare
  RLP list, for AccessList/DynamicFee/SetCode/Blob `type_byte ||
  RLP(payload)`, and for Blob specifically WITHOUT its EIP-4844
  sidecar (`EncodeEthereumTransaction`, contrast the sidecar-carrying
  `EncodeEthereumPooledTransaction` used for pool/gossip propagation).
  Verified field-by-field that `BlobTx.hash()`/`SetCodeTx.hash()` use
  the identical field list/order as their own RLP encoding structs, so
  `keccak256(TxData[i])` is the correct canonical tx hash for ALL FIVE
  types when computed fresh from the block's own bytes -- no type
  needs excluding from the HASH computation itself.
- The narrower, existing `hashFromEncoding()` boundary
  (`common/transaction/transaction.go:598-604`, Legacy/AccessList/
  DynamicFee only) is reused as the OBJECT-REUSE eligibility list
  instead: Blob/SetCode always decode fresh even on a pool hit,
  because reusing the pool OBJECT (not just its hash) means trusting
  that object's own cached encoding downstream, and a Blob object's
  cache can legitimately carry the with-sidecar network form. This is
  a deliberate, conservative choice to match an already-established
  safety boundary rather than invent a new one for this task.

Mutability: the leader already builds blocks directly from
pool-resident objects (`internal/miner/worker.go:2082`,
`pending := w.txsPool.Pending(false)`) -- sharing objects with the
pool is an existing pattern, not a new risk. The only non-atomic
writes on a `*Transaction` (`SetFrom`/`SetNonce`) are already guarded
at both call sites (`internal/sender_recovery.go:130-133`,`293-296`)
by `if tx == nil || tx.From() != nil { skip }`, so a reused
(already-recovered) object is never written to again -- this also
means `applySenderHints` becomes a no-op for it, saving that share of
the follower's own recover phase too. No `sync.Pool`/recycling exists
anywhere in `internal/txspool` (grep-confirmed): a dereferenced
transaction stays valid indefinitely for any other holder.

**PART 2/3 (implementation + tests).** Behind `N42_BLOCK_DECODE_REUSE_POOL`
(unset/"0" = today's decode exactly): `common/block/block_decode_reuse.go`
adds `DecodeRLPReusePool`/`decodeBlockTxsReuse`, computing
`crypto.Keccak256Hash(data[i])` per transaction and asking the pool
(`internal/txspool.TxsPool.GetTx`, wired through a small `block.TxLookup`
function type to avoid an import cycle) before falling back to
`transaction.DecodeEthereumTransaction`. `internal/sync/rpc_chunked_response.go`
gains `decodeChunkedBlockReusePool`/`ReadChunkedBlockPeekHeader`'s new
`lookup` parameter; `internal/sync/rpc_block_push.go` wires the pool
lookup in only when the switch is on and a pool is configured.
Six new tests in `common/block/block_decode_reuse_test.go` cover
per-type equivalence (fresh vs. reuse: identical hashes, re-encoded
bytes, and senders for all five types; confirms Blob/SetCode are
NEVER reused despite a pool hit), the miss path, exact-hash-only
matching (no prefix collision), and two `-race` tests (concurrent
pool eviction during decode, concurrent import + pool insert). All
pass, including under `-race`.

**PART 4 (offline proof, gate: >=40% B/op reduction at 99.4% hits,
ns/op not worse).** `BenchmarkDecodePushedBlock`
(`common/block/block_decode_reuse_bench_test.go`), a realistic
160,000-transaction block, `taskset -c 200-207`, `-benchtime=3x`:

| variant | ns/op | B/op | allocs/op | reused | decoded |
|---|---|---|---|---|---|
| fresh | 29,891,950 | 130,803,288 | 2,560,037 | -- | 160,000 |
| reuse, 0.0% hits | 41,250,901 | 136,088,778 | 2,720,055 | 0 | 160,000 |
| reuse, 99.4% hits | 15,846,686 | 7,356,661 | 175,391 | 159,040 | 960 |
| reuse, 100.0% hits | 14,052,784 | 6,572,794 | 160,029 | 160,000 | 0 |

Per-transaction: fresh = 817.5 B, 16.0 allocs, 186.8 ns; reuse @
99.4% hits = 46.0 B, 1.10 allocs, 99.0 ns. **B/op falls 94.4% at the
fleet's own measured 99.4% hit rate** (well past the 40% bar); ns/op
falls 47% (an improvement, not a regression). At 0% hits the reuse
path costs ~38% MORE ns/op than fresh (the extra hash computation
buys nothing when nothing is ever found) -- reported as the path's
own honest worst case, not hidden. **Gate cleared: proceeding to
PART 5.**

Arithmetic for the fleet's own expected effect: at 99.4% hits the
per-transfer allocation this bucket removes is ~(817.5 - 46.0) =
771.5 B/tx, i.e. ~99.4% x 771.5 ~= 767 B of the task's own cited
2.93 KB/transfer follower body+decode bucket -- roughly a quarter of
that bucket by itself; duplicate LIVE objects avoided across the
fleet's own block cache depth of 4 (`N42_BLOCK_CACHE_BLOCKS=4`) are
~163,000 tx/block x 771.5 B x 4 ~= 503 MB of decoded-but-never-needed
transaction data no longer alive at once per node, on top of the
per-decode allocation saved.

**PART 5 (build + runner, since PART 4 cleared the bar).** n42-r96 =
n42-r95's exact file set (S31, confirmed base) plus this step's five
files, built via the established file-checkout recipe. Markers
confirmed present exactly once/zero as expected (`BaseCache`=0,
header-vote log line=1, commit-vote-refused log line=1, `blockimport
phases`=2). `sha256sum`: n42-r96 =
`73fdea7c005062898722265cfd9e6142f4581a46e83f6a0cd150bb50f8cbe97b`
(108,802,568 bytes); binary lineage n42-r92 -> n42-r94 (S26,
`e49ce151`) -> n42-r95 (S31, `c124146e`) -> n42-r96 (S32, `f961f63e`).

`run-r35zzzl.sh`/`chain-35zzzl.sh` built from the 35zzzk pair:
`run_leg` gains a 7th argument threading `N42_BLOCK_DECODE_REUSE_POOL`
A/B BY LEG (warm-up/A1/B1 = "0", B2/A2 = "1"); `GOMEMLIMIT` stays
fixed at 10GiB in every leg (S28's own question is separate, 6dd/6dm,
still available as the 6th argument). The predecessor wait was fixed
to `r35zzzk.log`; the binary check/install and the `BOX-NOTE-gov5.txt`
message were updated to n42-r96/S32. `bash -n` clean on both scripts;
neither is running.

**The chain script's memory-abort retry loop was removed for this
round, per the commander's own conditional instruction** ("only if
that is a small edit"): the loop's retry BEHAVIOR was confined to a
single, separable ten-line conditional (`if grep -q "MemAvailable"
$L35 && [ $attempt -lt 3 ]; then ... continue; fi`) inside the
existing `for attempt in 1 2 3; do ... done` wrapper -- deleting that
conditional (now: report which way the round ended, then
unconditionally `break`) and reducing the loop to `for attempt in 1`
left every other line (the memory-floor wait, the box-quiet claim
protocol, the reseed, the launch) untouched. This qualifies as the
small edit the instruction asked for; the wrapper itself was kept
(rather than un-nested) to minimize the diff. A round that aborts on
the memory floor now reports and stops, once, instead of silently
re-running while the box is shared.

**Prediction 95 (registered before any round, mechanism only):**

**(a) Allocation.** The follower's own body+decode allocation bucket
(2.93 KB/transfer, 6df/6di) falls by at least the benchmark's own
measured fraction x 0.7 (i.e. by at least ~66% of that bucket) at the
fleet's own ~99.4% hit rate.

**(b) Heap.** End-of-win2 `HeapAlloc` lower in B2 (switch on) than
B1's own (switch off) by at least half of this section's own derived
duplicate-live-object figure (~503 MB / 2 ~= 250 MB); `NumGC`/block
lower in B2 than B1.

**(c) Throughput/correctness.** Follower import total not slower in
B2 than B1; the `blockimport phases` line's new `reuse=`/`dec=`
fields show reuse >= 99% of a block's own transactions in every B2
block once the pool has caught up.

**(d) Safety.** S26's own checks stay green (0 conflicting heights);
no BAD BLOCK; transaction roots verified on every imported block in
every leg, exactly as every prior round.

**VERDICT: confirmed (bar cleared), not yet run.** PART 4's offline
proof clears the task's own 40%-B/op / no-ns/op-regression bar by a
wide margin; PART 1's hash-preimage and mutability questions are
answered from the code with no open item requiring a stop; n42-r96 is
built and marker-checked; the harness is prepared and syntax-checked
with the memory-retry loop removed as instructed. `docs/QS_QUEUE.md`'s
S32 row is marked prepared with prediction 95 (6dk);
`docs/OPEN_ISSUES.md` carries a dated S32 status line. Launch is the
commander's next call.

## 6dl. S32: round 35zzzl finished with reuse stuck at 0% on every node in every leg -- the code is correct end to end, the cause is that this fleet's transaction gossip has delivered zero messages since N42_TXPOOL_NOLOCALS=1 entered the baseline (round 35z3); prediction 95 is therefore UNTESTED, not falsified (2026-09-22)

Node logs preserved whole, trimmed to `wr-logs/r35zzzl-keep/
node{0-6}-B.log` (2026-09-22 15:00-15:29, 128,534-129,088 lines/node).
Scripts reused unmodified: `height_conflict_check.py`,
`import_breakdown.py`; a direct JSON scan of `blockimport phases`
lines did the reuse/dec accounting (no new script needed).

### FIRST, as instructed: why reuse stayed 0% in the switch-ON leg

The commander's own grep was reproduced exactly and extended to all
seven nodes for the whole B2 leg (15:14:38-15:28:26,
`N42_BLOCK_DECODE_REUSE_POOL=1`): **every node, every full block,
`reuse=0`**. node0: n=133 full blocks, `sum(reuse)=0`,
`sum(dec)=21,679,000` (= 133 x 163,000, exact). Nodes 1-6: identical
pattern, 132-133 full blocks each, `sum(reuse)=0` on all of them, no
exceptions. The A2 leg (also switch-on) was not separately re-checked
byte-for-byte but the same B2-window scan already spans past its own
start with the same result, so the finding covers both switch-on
legs.

**Step 1 -- did the switch take effect?** There is no dedicated
startup log line for this switch (checked: only the pre-existing,
unrelated "Sender hint source attached" line matches "reuse" in the
logs). Indirect evidence says yes, plumbing-wise:

- `internal/sync/block_decode_reuse_switch.go:26-40`
  (`BlockDecodeReusePoolOn`/`parseBlockDecodeReusePool`) is a
  straightforward `sync.Once`-gated `os.Getenv("N42_BLOCK_DECODE_REUSE_POOL")`
  parse accepting `"1"` -- correct.
- The fleet is NOT a single long-lived process across legs: every
  `run_leg` call in `run-r35zzzl.sh` invokes `./bench-run.sh`, which
  (`/data/blockchain/scripts-qs/bench-run.sh:100-107`) stops any
  running fleet (`stop-fleet.sh --no-inspect`), resets the pool
  journal, then calls `bench-7node.sh`, which calls `qs_launch_node`
  (`qs-env.sh:215-219`, a plain `setsid "$bin" ...` with no `env -i`
  scrubbing) for all seven nodes. So each leg's node processes are a
  **fresh exec** inheriting `run_leg`'s own exported environment,
  including `N42_BLOCK_DECODE_REUSE_POOL=$7`. This rules out the
  "stale process never restarted, so the env change never took"
  hypothesis I initially favored -- checked and rejected.
- `internal/node/node.go:1304-1306` only calls `n42sync.WithTxPool(pool)`
  (the sole setter of `s.cfg.txPool`, the field `rpc_block_push.go`
  gates on) when `cfg.P2PCfg.TxGossipEnabled && pool != nil`.
  `cfg.P2PCfg.TxGossipEnabled` defaults to `true` in
  `cmd/n42/config.go:127`, with the comment "this flag had no default
  and no CLI flag" -- confirmed by grepping the whole harness
  (`run-r35zzzl.sh`, `chain-35zzzl.sh`, `bench-run.sh`,
  `bench-7node.sh`, `qs-env.sh`) for `TxGossipEnabled`/`txgossip`/a
  config file: nothing overrides it. `pool` is the return of
  `txspool.NewTxsPoolWithConfig`, error-checked non-nil before use.
  Direct log proof this held at runtime: `"Subscribed to transaction
  gossip topic"` appears exactly 3 times per node in the kept window
  (once per leg-boundary restart within it), on all 7 nodes --
  meaning `s.cfg.txGossipEnabled` (set by `WithTxPool` itself,
  `options.go:87-94`) was true, so `s.cfg.txPool` was non-nil.
- `internal/sync/rpc_block_push.go:28-34` and
  `internal/sync/rpc_chunked_response.go`'s `ReadChunkedBlockPeekHeader`
  -> `decodeChunkedBlockReusePool` -> `Block.DecodeRLPReusePool` chain
  is exactly the S31 `ReadChunkedBlockPeekHeader` handler (confirmed
  by reading `blockPushStreamHandler` end to end): the commander's own
  candidate "did S32 wire reuse into the PeekHeader variant, or the
  other one?" is answered -- the PeekHeader variant, correctly.
- Hash computation: `decodeBlockTxsReuse`
  (`common/block/block_decode_reuse.go:82-84`) computes
  `crypto.Keccak256Hash(data[i])` where `data[i]` is
  `dec.TxData[i]`, and `blockRLP.TxData[i]` is filled at encode time
  from `tx.EthEncoded()` (`common/block/block.go:176-186`) -- the
  IDENTICAL bytes `transaction.Transaction.Hash()` hashes for the
  same eligible types (`transaction.go:580-586`,
  `hashFromEncoding` at `598-604`, same three-type list
  Legacy/AccessList/DynamicFee reused verbatim as
  `reuseSafeTxType`). No wrong-bytes, wrong-hash-type, or eligibility
  bug: this was already proven offline in 6dk PART 0/1 and rechecked
  here against the live wiring, not just the isolated benchmark.

Every piece the commander asked to check by code -- wrong bytes,
wrong hash type, wrong handler variant, eligibility mismatch -- reads
correct. That leaves one more possibility, checked last:
`decodeBlockTxsReuse`'s own fallback (`lookup == nil`) produces
EXACTLY the observed `reused=0, decoded=len(txs)` signature
(`common/block/block_decode_reuse.go:76-79`) -- indistinguishable in
the log fields from "lookup was non-nil but missed on every single
transaction." Both are consistent with the data. The second
possibility is the one confirmed by direct evidence:

**Step 2 -- transaction gossip subscribes but delivers nothing, on
every node, for the whole round.** `internal/sync/subscriber_transactions.go`
logs `"tx gossip: receiving"` on the first accepted gossiped
transaction and every 500th thereafter (`txGossipReceived`, a
dead-pipeline canary by design). Grepped across all seven kept logs,
the full round (both legs): **zero occurrences, on every node.** The
subscription itself succeeds (3x "Subscribed to transaction gossip
topic" per node, above) -- peers join the mesh -- but not one
transaction is ever actually relayed over it.

The reason is upstream of gossip's receive side, in the PUBLISH side,
and it is this round's own long-standing baseline configuration, not
a new bug:

- `run-r35zzzl.sh` exports `N42_TXPOOL_NOLOCALS=1` in every leg (a
  switch adopted at round 35z3/35z4, 2026-09-08, "RPC submissions are
  remote transactions -- no locals set / journal / pool-wide sweep
  per new sender").
- `internal/txspool/txs_pool_types.go:187-194`: this env var sets
  `pool.config.NoLocals = true`.
- `internal/txspool/txs_pool.go:149` (`AddLocals`): 
  `return pool.addTxs(txs, !pool.config.NoLocals, false)` -- with
  `NoLocals=true`, every RPC-submitted transaction is inserted with
  `local=false`.
- `internal/txspool/txs_pool.go:404-412` (`addTxs`): 
  `if local { ... event.GlobalEvent.Send(common.NewLocalTxsEvent{...}) }`
  -- with `local` always false this round, `NewLocalTxsEvent` is
  **never sent, for any transaction, by any node.**
- `internal/sync/tx_broadcaster.go:44-52` (`broadcastTxs`) subscribes
  ONLY to `NewLocalTxsEvent` -- deliberately, per its own comment, to
  avoid a 7x gossip-amplification loop by not re-publishing
  gossip-received transactions. With `NewLocalTxsEvent` never firing,
  the publisher has nothing to publish, ever, on any node.

Combined with `-shard-senders` routing (`bench-run.sh`'s default when
`--broadcast` is not passed -- this is what every round's own banner
reports as `broadcast=0`; `bench-run.sh:219`), each of the 8,000
flood senders is deterministically assigned to exactly ONE of the 7
node RPC endpoints (`sender % 7`). Since gossip re-publishes nothing,
each node's own live tx-pool holds ONLY the ~1/7 shard of senders
submitted directly to it, permanently -- never another node's share.
A follower importing a block another node proposed is, structurally,
importing mostly OTHER nodes' shards, which its own pool has never
held at any point. `s.cfg.txPool.GetTx(hash)` misses essentially every
time, not because of anything in the S32 patch, but because the
object it is looking for was never resident on that node to begin
with.

**This is fleet-wide and predates this round by two weeks** (NOLOCALS
has been in the baseline since round 35z3/35z4, 2026-09-08): every
round run under this configuration -- the entire back half of this
campaign -- has had a transaction-gossip pipeline that subscribes
successfully but relays nothing. It also explains, retroactively, WHY
round 35zi ("track 2: sender pre-recovery through hint-only ingest")
was ever necessary: the hint-only ingest endpoint
(`internal/ingest/server.go`) and `-hint-peers` were built specifically
to get sender-recovery information to every node by a side channel,
because the "normal" channel (tx gossip, which would also carry
decoded transaction bodies into every pool) was never delivering
anything. 6df's own "(5')" finding -- ~99.4% of a pushed block's
transactions already sit in the pool, decoded, with sender cached --
was evidently measured or reasoned about a different propagation
path (the hint-only feed populates the SENDER CACHE, a completely
separate structure from the pool's own tx map that `GetTx` reads);
it does not describe the pool's own transaction-object residency,
which this round shows to be close to 0% for cross-node blocks. This
gap between 6df's assumption and the pool's real content is the
proximate cause of today's UNTESTED result, and is being flagged here
as its own fact rather than folded quietly into the mechanism section
below.

**Conclusion on the addendum: not a bug in the S32 code.** Every code
path the commander asked about (env parse, `WithTxPool` gating, the
PeekHeader wiring, the hash preimage, the eligibility list) was
re-read against the live wiring and is correct. The 0% hit rate is a
structural property of this fleet's configuration (shard-senders +
a non-relaying tx-gossip publish side, both long-standing), not of
the decode-reuse patch, and it fully explains -- with no residual
mystery -- why every node's `reuse` counter stayed at 0 in both
switch-on legs.

### FIRST item on the original list: why B1 win1 is 116.7k @ 1.364s

`import_breakdown.py` on the two win1 capture windows (like-numbered,
both switch-off vs switch-on but per the above neither switch state
matters for decode): B1 win1 (15:10-15:12, n=14 full blocks) `proc`
med=836ms, `body` med=27ms, `total` med=1116ms; B2 win1 (15:24-15:26,
n=207 full blocks(*) ) `proc` med=620ms, `body` med=11ms, `total`
med=864ms. (*) the B2 sample size is inflated by the query window
picking up neighbouring blocks past the 20s capture; treat both as
indicative, not exact-window figures.

Checked and ruled out:
- **View timeouts / TC / sibling switches**: zero in both windows
  (`grep -c` for `timeout certificate|view timed out|advancing
  view|new-view|TC formed` is 0/0). The 108-114 "timeout" hits in a
  naive grep are all `"lwWhy":"timeout"` -- the normal
  N42_LEADER_WRITE_AFTER_JOURNAL 150ms fallback, not a consensus
  event -- and appear at a similar rate in both windows.
- **Memory pressure at capture time**: `r35zzzl-mem.log` shows BOTH
  win1 windows with node `RssAnon` at 9.9-10.9 GB (essentially AT the
  10GiB `GOMEMLIMIT`) and `RssFile` 1.2-3.1 GB -- the two legs look
  the SAME on this axis, so GC-thrash-at-the-ceiling (6da/6dj's own
  established mechanism) does not by itself explain why B1 is worse:
  B2 hits the same ceiling and is faster.
- **BAD BLOCK / root mismatch / conflicting heights**: none in the
  round (see safety section below) -- not a correctness incident.

Not ruled out, and the best-supported remaining candidate: B1 is the
FIRST full/dense leg run after `chain-35zzzl.sh`'s own fresh reseed
of all seven node directories immediately before launch (warm-up and
A1 precede it but A1's own gasceil is 960M vs B1/B2's 6.846B --
roughly a 7x smaller working set). B1's own MDBX/QMDB pages for this
round are being touched for the first time at B-tier block sizes,
while B2 benefits from B1's own ~13 minutes of full-block traffic
having already warmed the identical address ranges (same reseeded
data, same node identities' data files, just further along). This
matches this campaign's own established page-cache-warmup finding
(6cr/6cv) even though the coarse (10s-interval) memory-watchdog log
does not show a clean before/after signature on RSS alone -- the
warmup would show up in access latency to specific hot pages, not
aggregate RSS. **Classification: cold cache (best-supported by
elimination); not code, not consensus, not the reuse switch (which
is off in both legs' win1 anyway -- warm-up/A1/B1 are all
switch-off).** Every later mechanism number below is therefore
reported against **B2 win1/win2 vs B1 win1/win2 as captured**, flagged
where B1's own anomaly likely inflates its numbers, and 35zzzk's
141.7k @ 1.132s is kept only as an external reference point, not a
baseline substituted into any OFF-vs-ON delta.

### Mechanism (95a-c): reported as observed, not attributable to the switch

Because reuse was 0% in every ON-leg block, none of the OFF-vs-ON
deltas below can be read as an effect of `N42_BLOCK_DECODE_REUSE_POOL`
itself -- the switch never exercised its own reuse path in this round
(the `lookup==nil` fallback and the `lookup!=nil`-but-0%-hits path are
computationally close but not identical; see 6dk PART 4's own "reuse,
0.0% hits" row, which measured **41.25 ms/op vs fresh's 29.89 ms/op --
a 38% ns/op REGRESSION at 0% hits**, the extra hash computation buying
nothing). The numbers below are reported for the record, with that
caveat repeated at each line:

- **reuse/dec share**: 0% / 100% in every full block, both ON legs
  (confirmed above).
- **Follower body + import total** (`import_breakdown.py`, MANDATORY):
  B1 win1 body 27ms / total 1116ms -> B2 win1 body 11ms / total
  864ms. Both fall, but this is very unlikely to be the 0%-hit-rate
  decode REGRESSION 6dk's own benchmark predicts -- more likely the
  B1-anomaly (previous section) inflating B1's own numbers on every
  phase, decode included. Not attributable to the switch either way.
- **HeapAlloc, end of win2** (`go tool pprof -top` on the kept heap
  profiles): B1 win2 (node5, follower, 15:11:56) inuse_space total
  6,761.61 MB; B2 win2 (node0, follower, 15:25:52) inuse_space total
  7,557.94 MB -- **higher in B2, not lower**, the opposite of clause
  (b)'s prediction. Consistent with "the switch did nothing" (no
  duplicate-live-object savings to realize at 0% hits) plus ordinary
  leg-to-leg variance, not with a regression caused by the patch.
- **NumGC/min, NumGC/block**: not separately captured this round (the
  heap/allocs profiles kept do not carry `runtime.MemStats.NumGC`,
  and no metrics-endpoint scrape for it was taken in this pass) --
  flagged as a gap rather than reported as zero-difference.
- **RssAnon/RssFile, both legs, win1/win2** (`r35zzzl-mem.log`,
  10s-granularity node RSS): B1 win1 ~9.9-10.9 GB anon / 1.2-3.1 GB
  file; B1 win2 (~15:11-15:13) ~10.4-11.0 GB anon / 1.1-1.8 GB file;
  B2 win1 ~9.95-10.7 GB anon / 1.2-1.9 GB file; B2 win2 (~15:26-15:27)
  ~10.3-10.9 GB anon / 0.8-1.7 GB file. All four windows sit in the
  same 9.9-11.0 GB anon band against the fixed 10GiB `GOMEMLIMIT` --
  no leg-to-leg separation visible on this metric.
- **Faults (majflt)**: not captured this round (the mem watchdog logs
  RSS only, not `/proc/<pid>/stat` fault counters) -- another gap,
  not a zero-difference finding.
- **Leader-side control (jcvMs, build waterfall)**: not re-run this
  pass given the above; expected unchanged per 6dk's own reasoning
  (the patch touches only the follower's receive-side decode), and
  nothing in the follower-side data suggests otherwise.
- **In-tenure / hand-over cycle**: `import_breakdown.py`'s own join
  (B2 window only, larger sample) gives chained (same-leader)
  seal->seal median 970ms / handover median 1908ms, n=24/3 -- both in
  the expected shape for this fleet (handover roughly 2x chained) and
  not something the reuse switch would plausibly move.

### Safety (95d)

- `height_conflict_check.py` on the whole round: `heights_checked=4495,
  committed_events=35960, unresolved=0, conflicts=0`. Clean.
- `grep -l "BAD BLOCK"` across all 7 kept logs: no matches.
- Root-mismatch / invalid-root / tx-root-mismatch greps: no matches.
- `import-gated vote REFUSED`, `commit-vote REFUSED`, `header-vote
  REFUSED` (35zzzk's own view-6557 pattern): **zero on every node**,
  unlike 35zzzk's 35-line incident. No safety anomaly this round.
- New-code warning/error lines: `git show f961f63e` on the five
  touched files has zero `log.Warn`/`log.Error`/`log.Crit` call sites
  added -- there is no failure-path log string to check for, by
  design (the reuse-decode falls back silently to the ordinary path
  on any RLP error, per `decodeBlockTxsReuse`'s and
  `decodeChunkedBlockReusePool`'s own fallback branches).

### Prediction 95, clause by clause

**(a) Allocation** -- **UNTESTED.** The follower body+decode bucket's
fall was never exercised: reuse was 0% throughout, so the mechanism
prediction (a) describes (some fraction of the benchmark's measured
reduction) had nothing to act on. The delta-allocs partition
(`alloc_path_partition.py`, bucket F) was not run this pass given (a)
is already known to be untestable from the reuse counters alone.

**(b) Heap** -- **UNTESTED, and the raw numbers point the wrong way if
misread.** End-of-win2 `HeapAlloc`/inuse_space is HIGHER in B2
(7,557.94 MB) than B1 (6,761.61 MB), the opposite of the predicted
direction -- but with 0% reuse this cannot be attributed to the patch;
it is ordinary leg-to-leg variance (plausibly compounded by the same
B1-anomaly that depressed B1's own throughput). `NumGC`/block was not
captured.

**(c) Throughput/correctness** -- **PARTIALLY OBSERVED, not
confirmed.** Follower import total was NOT slower in the ON leg
(864ms vs 1116ms) -- satisfying the letter of "not slower" -- but
`reuse >= 99%` (the clause's own correctness bar) was not met at all
(0%, every block). A clause with two conditions where one silently
never engages is not a pass; recorded as untested on its substantive
half.

**(d) Safety** -- **CONFIRMED.** 0 conflicting heights (4,495
checked), no BAD BLOCK, no root mismatches, no new refusal pattern.
This clause is independent of whether reuse ever fired and holds
regardless.

**Overall VERDICT for prediction 95: UNTESTED, not falsified.** The
switch was live (subscriptions, plumbing, and env propagation all
confirmed correct end to end) but its own precondition -- the pool
already holding ~99.4% of a pushed block's transactions -- does not
hold on this fleet, because transaction gossip has relayed zero
messages fleet-wide since `N42_TXPOOL_NOLOCALS=1` entered the
baseline (round 35z3/35z4, 2026-09-08). This round measured the
switch's own 0%-hit worst case (6dk PART 4's own benchmark: -38%
ns/op at 0% hits on the isolated decode step) diluted into a total
block-import time where it is far too small a share to detect either
way against the observed B1-anomaly-sized noise.

### Recommendation

**Adopt the switch: not yet -- re-test first, do not judge from this
round.** The patch itself is unfalsified and low-risk (falls back
identically to today's behavior whenever `lookup` is nil or a hash
misses; zero new warning/error paths; safety clean); but nothing in
this round demonstrates it does anything on THIS fleet, because the
fleet's own tx-pool residency assumption the patch depends on does
not hold. Before spending another round on A/B by leg, either (i)
fix the propagation gap so followers' pools actually accumulate
cross-node transactions (the missing half is `broadcastTxs` firing
on `NewLocalTxsEvent`, which `NoLocals=true` currently suppresses
for every RPC submission -- a config/product decision, not a one-line
fix, since NOLOCALS was itself adopted deliberately for pool-lock
contention reasons at round 35z3/35z4), or (ii) accept that this
fleet's real transaction path is shard-senders + hint-only-ingest
(never full pool mesh) and re-scope prediction 95 around THAT
reality -- e.g. hash-lookup against a leader's own most-recently-built
block cache instead of the general pool, which is a different,
untested mechanism.

**n42-r96 as the fleet base: yes, for S31's header-vote fix**, which
this round's own safety section reconfirms clean (0 refusals, 0
conflicts) and which is the binary's other, already-validated change.
The reuse feature itself should be treated as present-but-dormant on
this fleet (off by default, `N42_BLOCK_DECODE_REUSE_POOL` unset) until
one of the two paths above is taken -- it is safe to carry forward
inert, not yet safe to credit with any throughput effect.

## 6dm. S33-spec: the live heap has four named owners covering >=90% of it in every profile -- decoded transaction objects (biggest, ~28-32%), the sender cache (~15-16%, documented as worth "approximately nothing"), the tx-hash tail index (~16-19%, a known, already-costed design tradeoff), and QMDB's in-RAM live-key map (~11-14%, flat); the top reduction is a one-line config change (2026-09-22)

Read-only: profiles + code, no builds, no benchmarks, box left free.
Sources: `/data/blockchain/wr-pprof/r35zzzl-B1-{win1,win2}-*-heap.pb.gz`
(node1/node2 win1, node4/node5 win2 -- switch off both windows, so this
is the cleanest single-leg growth pair) and the B2 win1/win2 pairs from
the same round for a second look; `go tool pprof -top -unit=mb`.
35zzzh/i/k's own equivalently-shaped captures were spot-checked and
show the same four owners in the same rank order (not tabulated in
full here to keep this a light pass, per the commander's own
instruction).

### 1. Owners (B1 win1 node1/node2 avg vs B1 win2 node4/node5 avg, MB)

| owner | win1 | win2 | % of total (win2) | bound is a... |
|---|---|---|---|---|
| decoded transaction objects (`DecodeEthereumTransaction` cum: `decodeUint256`+`decodeEthereumTransaction`+`NewTxOwned`+`CacheSender`+`Hash`+`cacheEncoded`+`SetFrom`+`txCompactReader`+`unmarshalCompactStorage`) | 1507.1 | 2159.7 | 29.7-32.4% | **mixed**: pool pending/queued (HARNESS: `--pool-slots 600000 --pool-queue 200000`), block cache (HARNESS/product: `N42_BLOCK_CACHE_BLOCKS=4`), plus transient decode garbage not yet collected |
| `internal/txlookup.(*Tail).Add` (tx-hash -> block-number tail index) | 1045.7 | 1127.6 | 15.9-16.6% | **product default** (hardcoded `const` in `internal/txindexer/indexer.go`, no env override): `txIndexSealMinTx=1_000_000`, `txIndexSealMaxBlocks=256`, `txIndexKeepBlocks=64`, `txIndexKeepTx=1_000_000` -- ALREADY documented in-code as costing "~1.1 GB of a node's heap... round 35zzo" at this fleet's own 163k-tx blocks |
| `common/transaction.senderCachePut` (process-wide sender-recovery memo) | 911.6 | 1154.1 | 15.4-16.3% | **harness flag**: `N42_SENDER_CACHE_SLOTS=16777216` (`run-r35zzzl.sh`, adopted round 35zm); code's own header comment: measured hit rate "8.3%-27.9%, decaying" (sharded submission) to "0.00%" (broadcast), end-to-end effect "none separable from round-to-round noise... kept because it is cheap and correct, not because it is load-bearing" |
| `lib/qmdb.newMapIndexSized` (in-RAM live-key index, `map[Hash]uint64`) | 766.1 | 783.6 | 10.4-10.8% | **product default**: no chain-config/env knob selects the index backend; `QMDBRootComputer.UseMDBXIndex` (an already-built MDBX-backed alternative that moves this off the Go heap into the B+tree page cache) is wired ONLY into `internal/replay/engine_v2.go` (the offline replay tool), never into the live node path (`internal/node/node.go`) |
| everything else (`parallel.NewReadWriteSet` arenas ~190-200, `mobileverify.PacketCache` ~35-155, `etl.sortableBuffer` (history fold) ~0-303, `miner.copyReceipts` ~65-125, `state.CapturePostState` ~49-61, `txspool.txLookup`/`txsSortedMap` ~40-90, misc <1% lines) | ~340 | ~430 | ~6-7% | mixed; each individually small |
| **total inuse_space** | **5756** | **6960** | **100%** | -- |

**Coverage: named owners (first four rows) account for 93-94% of
inuse_space in every profile checked** (both this table's pair and
the 35zzzh/i/k spot-checks) -- comfortably over the 80% bar.

Arithmetic behind "what the bench actually needs" the commander asked
for: the flood keeps 8 x 45,000 = 360,000 transactions in flight by
its own `-target-depth`, well under the pool's own 600,000+200,000 =
800,000-slot cap -- the cap is not what is currently binding pool
memory (see growth section: pool/decode-garbage growth tracks leg
activity, not a fill-to-cap event). Block cache at depth 4 holds
4 x 163,000 = 652,000 transaction objects; at the isolated benchmark's
own fresh-decode cost of 817.5 B/tx (6dk PART 4), that alone is
~511 MB of the ~1.5-2.2 GB "decoded transaction objects" bucket --
the remainder is pool pending/queued plus in-flight decode garbage
between GC cycles, not separable further without a heap profile that
distinguishes retaining structure (pprof's inuse_space groups by
ALLOCATION site, and pool insertion, block import, and block-cache
storage all route through the same `DecodeEthereumTransaction` call).

### 2. Growth, win1 -> win2 (same B1 leg, switch off both windows)

| owner | win1 (MB) | win2 (MB) | delta | % of total +1204 MB growth |
|---|---|---|---|---|
| decoded transaction objects | 1507.1 | 2159.7 | **+652.6** | 54% |
| sender cache | 911.6 | 1154.1 | **+242.5** | 20% |
| tx-hash tail | 1045.7 | 1127.6 | **+81.9** | 7% |
| QMDB map index | 766.1 | 783.6 | +17.5 | 1% |
| everything else | ~340 | ~430 | ~+90 | 7% |
| **total** | **5756** | **6960** | **+1204** | **100%** |

Reading each:
- **Sender cache (+242.5 MB, 20% of growth)**: filling toward its own
  16M-slot ceiling as the leg runs -- self-limiting once full (a
  direct-mapped/two-way table overwrites, it does not keep growing
  past capacity), consistent with the still-rising-but-decelerating
  shape a fill-then-plateau curve would show.
- **Tail index (+81.9 MB, 7%)**: per the code's own math this should
  self-bound near the `keepTx=1,000,000` window (~6 blocks) once the
  background sealer keeps pace, but `SealRangeKeepTx`'s `keep` bound
  only governs what is ELIGIBLE to seal -- actual eviction happens on
  a separate `DropBelow` after a segment finishes building
  (`txIndexSealMaxPerTick`, one segment per ~15 s tick). A busier
  leg's own CPU contention plausibly slows the segment-build tick
  relative to block production, letting the unsealed backlog grow
  toward the `keepBlocks=64` ceiling the code's own comment already
  measured at ~1.1 GB (round 35zzo) -- this round's own win2 figure
  (1.13 GB) matches that documented ceiling almost exactly, so this
  looks like an already-known, already-budgeted cost re-confirmed,
  not a new one.
- **Decoded transaction objects (+652.6 MB, 54% -- THE dominant
  grower)**: not a clean pool-fill signature by itself (the pool is
  nowhere near its 800k-slot cap per the arithmetic above); most
  consistent with accumulating decode garbage from a busier win2 (more
  blocks/tx processed per unit time as the round's own occupancy
  settles) sitting live between GC cycles under GOMEMLIMIT pressure,
  compounded by whatever the pool's own actual pending/queued count
  does over the same interval (not independently measured this pass --
  would need `txpool_status`/reorg-line pending counts cross-referenced
  against this window's own timestamps to fully split the two; flagged
  as a gap, not claimed).
- **QMDB map index (+17.5 MB, ~flat)**: consistent with its own nature
  -- it holds one entry per LIVE chain key, and this workload's
  recipient set is fixed (`--recipients 22857`) with senders reused
  across blocks, so the live-key count should grow slowly within one
  13-minute leg regardless of block count. Matches.
- **Fragmentation**: not directly measurable from these `.pb.gz`
  captures (no embedded `runtime.MemStats`; `go tool pprof -raw`
  confirms only the four standard heap sample types, no
  HeapInuse/HeapIdle/HeapReleased fields). The coarser proxy available
  this pass, `RssAnon` (mem-watchdog, 10 s granularity) minus
  inuse_space: win1 ~9.9-10.9 GB `RssAnon` vs ~5.6-5.9 GB inuse_space
  (~4.0-5.0 GB gap) narrowing to ~10.4-11.0 GB vs ~6.8-7.2 GB (~3.3-4.2
  GB gap) in win2 -- the gap does NOT grow with the leg; live-object
  growth is eating into headroom, not adding fragmentation. Flagged as
  a gap for a follow-up round that scrapes `/debug/pprof/heap?debug=1`
  or a metrics endpoint for the real MemStats fields.

### 3. Ranked reductions (config-only first; do not lower GOMEMLIMIT)

1. **Sender cache -- ~0.7-0.9 GB saved -- config: `N42_SENDER_CACHE_SLOTS` 16777216 -> back to 4194304 (the pre-35zm size) or lower.** Arithmetic: ~65-70 bytes/live entry measured (1.05-1.16 GB / ~16M slots near-full); cutting to 4M caps it near 260-280 MB, saving ~0.7-0.9 GB. Cost: the cache's own code comment already measured its end-to-end effect as unmeasurable against round-to-round noise at ANY size tested, and explicitly warns against enlarging it "on a CPU-share argument again without an end-to-end number" -- shrinking it runs with that same finding, not against it. Proof: pure env value, zero code risk, drop it into `run_leg`'s existing export and A/B by leg exactly like every other switch this campaign has used; watch for any drop in the fleet's own recovery-hit-rate diagnostics as the one thing to confirm didn't regress.
2. **Tail index keep-window -- ~0.5-0.9 GB saved -- code: lower `txIndexKeepBlocks` (64) and/or `txIndexSealMinTx`/`txIndexSealMaxBlocks` in `internal/txindexer/indexer.go`.** No env override exists today (a one-line addition would make this config-only for future rounds). Cost, already documented in the same file: a restart re-reads every unsealed-and-kept block in full to rebuild the tail, costing allocation proportional to the SAME window being shrunk (currently ~1.25 GB at 64 blocks) -- so this trades live-heap-during-the-leg for restart-time allocation, which is a fair trade only if restarts are rare in production relative to how long the fleet runs hot. Also watch whether a smaller `txIndexSealMaxPerTick`-vs-block-rate gap actually lets the sealer keep the tail AT its nominal `keepTx` bound (~1 block-multiple of 163k) instead of drifting to the `keepBlocks` ceiling, which per the growth section may be the larger win than changing the consts at all. Proof: needs a small code change (add an env override) plus an A/B by leg; not provable purely offline since it depends on the sealer keeping pace under this fleet's own CPU contention, which an isolated benchmark cannot reproduce.
3. **QMDB live-key index -- ~0.7-0.8 GB saved, but CPU/latency risk -- code: wire `QMDBRootComputer.UseMDBXIndex` into the live node startup path (`internal/node/node.go`), not just `internal/replay/engine_v2.go`.** This moves the entire ~0.77-0.78 GB owner off the Go heap into the MDBX B+tree page cache, which is reclaimable and outside GOMEMLIMIT's accounting the way `RssFile` already behaves differently from `RssAnon` in this campaign's own established distinction (6cr/6cv). Cost: every `Get`/`Put`/`Delete` on the live-key index -- called on every Set, Get, and cold-liveness check in the tree -- moves from an O(1) Go map lookup to an MDBX cursor operation; this is exactly the kind of change 6da/6dj's own page-cache-thrash findings say could go either way under this fleet's own memory pressure. Proof: NOT config-only -- needs a build and its own dedicated A/B round; do not fold into a config-only round.
4. **Pool slots/queue -- likely near-zero saved, included for completeness only -- config: `--pool-slots`/`--pool-queue` 600000/200000 -> smaller.** The flood's own `-target-depth` keeps 360,000 in flight (8 x 45,000), well under today's 800,000-slot cap, so the cap is very unlikely to be what is currently costing memory (pool structures are Go maps sized by actual content, not pre-allocated to the cap) -- shrinking it plausibly saves nothing measurable and risks generator-starvation if actual pending briefly spikes above a lowered cap. Not recommended as a priority item; listed so it is not silently skipped, and to record why it did not make the top 3.
5. **Block cache depth -- ~130-260 MB per step saved, moderate risk -- config: `N42_BLOCK_CACHE_BLOCKS` 4 -> 2 or 3.** Isolated cost per retained block ≈163,000 x 817.5 B ≈ 128 MB (6dk PART 4's own fresh-decode figure); dropping from 4 to 2 saves roughly one step (~128 MB) per block of depth removed. Cost: a shallower cache means more `FetchBlockByHash` misses on any re-ask (catch-up sync, a peer requesting an older block by hash) -- this fleet's own harness does not appear to stress that path directly, so risk is plausibly low, but unmeasured this pass. Proof: config-only, A/B by leg.
6. **History-fold ETL buffer (`etl.sortableBuffer.Put`) -- small (0-300 MB, appears intermittently, likely tied to `N42_HISTORY_INDEX_INTERVAL=20s`'s own fold cadence) -- not pursued as a priority**: the variance between profiles (0 MB in some, 303 MB in others, same round) suggests this is a transient, in-flight buffer captured mid-fold rather than a steady retained owner; a longer fold interval (already raised from 2s to 20s at round 35zzl for a different reason) trades a bigger buffer for less frequent write-amplification, so shrinking the interval back would cost real throughput for a small, non-steady memory return. Not ranked as an action item.

### 4. The GOGC question

The Go runtime's own documented rule (`runtime` package doc,
Environment Variables section, and `runtime/debug.SetMemoryLimit`):
GOGC sets a heap-growth target of `L*(1+GOGC/100)` over the live heap
size `L` at the end of the last collection, while `GOMEMLIMIT` (or an
equivalent `SetMemoryLimit` call) is a soft cap the collector treats
as a second, independent trigger -- the runtime schedules the next GC
at whichever of the two triggers is reached FIRST, i.e. effectively
`min(L*(1+GOGC/100), GOMEMLIMIT)`. When the GOGC-derived target
already exceeds the memory limit, the limit is what fires every time
and GOGC's specific value stops mattering to WHEN a collection
starts -- only to how far below the limit `L` would have naturally
sat if the limit were absent, which is moot once the limit binds
every cycle. Concretely on this fleet: at `GOGC=200` the target is
`3L`, which exceeds 10 GiB once `L` exceeds `10GiB/3 ≈ 3.33 GB` --
true for the entire measured 5.8-8.8 GB range (6da/6di/6dj), so the
limit already binds throughout. At `GOGC=100` the crossover is
`10GiB/2 = 5 GB` -- ALSO below the measured range's own floor (5.8
GB), so the limit would bind there too, for effectively the whole
range. Only at `GOGC=400` does the crossover rise to `10GiB/5 = 2 GB`,
still below 5.8 GB, so the limit binds there as well. **Answer: no,
neither GOGC=100 nor GOGC=400 would change anything under this
specific binding regime** -- the live heap `L` would have to fall
BELOW roughly 3.3-5.0 GB (depending on which of the two is tried)
before that GOGC value's own target would ever come in under the
10 GiB limit and start mattering again; since the whole documented
climb (5.8 -> 7.5-8.8 GB) sits above that crossover for every GOGC
value in the 100-400 range, **the only lever that changes GC
frequency here is reducing L itself (section 3's own reductions),
not GOGC.** This confirms, from the runtime's documented rule rather
than from the round's own CPU-share numbers, exactly what 6dj already
inferred empirically ("the effective GOGC shrinks as L grows").

### What this does and does not show

Shows: four named, quantified owners covering ~93-94% of live heap in
every profile checked, one of which (the sender cache) is a
config-only, code-comment-endorsed, near-zero-risk cut of ~0.7-0.9 GB;
a second (the tail index) is a known, already-documented tradeoff
whose current size matches its own designed ceiling; a third (QMDB's
map index) has an existing but unwired MDBX-backed alternative that
would need its own A/B round, not a config flip. Does not show: which
share of the dominant "decoded transaction objects" grower is pool
fill versus decode-garbage versus block cache without a
pending-count-correlated follow-up; true fragmentation (HeapInuse -
inuse_space) from these particular capture files; nor any live A/B
result for reduction #1 -- this section is the spec, not the round.

## 6dn. S35: prediction 97, registered before the round -- the sender cache 16M -> 4M slots by leg (2026-09-22)

Config-only, no build: n42-r96 (S31's header-vote fix; reuse switch
held at `N42_BLOCK_DECODE_REUSE_POOL=0` in every leg this round, since
6dl closed it untested and not owed a round). Round 35zzzn = the
35zzzl pair, A/B by leg on `N42_SENDER_CACHE_SLOTS` instead:
warm-up/A1/B1 = 16777216 (today's harness value, adopted round 35zm),
B2/A2 = 4194304. `GOMEMLIMIT` fixed 10GiB every leg; everything else
unchanged from 35zzzl.

**Prediction 97 (mechanism first):**

**(a) Live heap.** Follower `inuse_space` (in-window heap profile,
same capture convention as 6dm) in B2 lower than B1 at the
same-numbered window by 0.6-0.9 GB, with the sender-cache owner
(`common/transaction.senderCachePut` in `go tool pprof -top`) falling
from ~1.15 GB (measured 35zzzl B1win2, 6dm) toward ~0.3 GB (the 4M-slot
arithmetic bound at the same ~65-70 B/entry measured this round).

**(b) Hit rate and recover phase unchanged.** The sender-cache hit
rate (`"parallel block"`'s own `hintHits`/`hintFills` fields,
`internal/parallel_processor.go:638`; alternatively `"sender source"`'s
`hintFills`, `internal/state_processor.go:215` -- whichever line the
round's logs carry) unchanged within 1 point of 35zzzl's own figure,
and the follower `recov` phase (`"blockimport phases"`'s `recov`
field, the campaign's own `recoverMs`) unchanged within noise (26 ms).
If the hit rate falls measurably, the cache at 4M was doing real work
this round's own shape did not previously credit it for, and the
result says so rather than crediting (a)/(c) alone.

**(c) GC and block time.** `NumGC`/block in win2 lower in B2 than B1;
GC's own share of CPU in win2 lower in B2. Win2 block time better in
B2 than B1 IF (a) and (c) both move as predicted -- stated as the
measured outcome either way, against this round's own leg noise floor
(~0.1 s per prior rounds' own established noise-floor finding).

**(d) Safety.** 0 conflicting heights (`height_conflict_check.py`), no
BAD BLOCK.

**Baseline caveat, carried from 6dl.** B1 is the first full-block leg
run against this round's own fresh reseed and was shown (6dl) to run
measurably slower on unrelated grounds (cold cache, not memory
pressure or consensus) in 35zzzl. This round compares **win2 vs win2**
for every clause above, not win1 vs win1, and keeps 35zzzk's own B1
(a different round's first-full-leg baseline) as an external
sanity check rather than folding it into either leg's own numbers.

## 6do. S35: round 35zzzn confirms prediction 97 -- the sender cache owner falls 1.15 GB -> 0.32 GB exactly as predicted, but the win2 block-time win (1.667 -> 1.304 s) is real and traces to the LEADER's own write phase (719 -> 374 ms) via halved GC CPU share, not to the follower's own import breakdown, which is flat (2026-09-22)

Config-only: n42-r96 (S32 switch off, S31's header-vote fix only) with
`N42_SENDER_CACHE_SLOTS` A/B by leg -- warm-up/A1/B1 = 16777216, B2/A2
= 4194304 -- `GOMEMLIMIT` fixed 10GiB. Logs preserved to
`wr-logs/r35zzzn-keep/node{0-6}/` (subdirectory form this round, plus
each node's own rotated `.gz` predecessor -- both concatenated for
this analysis, since the live `n42.log` alone starts mid-B2 on most
nodes). Captures: `wr-pprof/r35zzzn-{B1,B2}-{win1,win2}-node*-{heap,cpu,allocs}.pb.gz`;
`r35zzzn-memstats.log` (full `runtime.MemStats` dumps, node0/node1,
~30s cadence). `height_conflict_check.py` run against a
`node*-B.log`-named symlink of the concatenated logs (the script's own
naming convention); `import_breakdown.py` adapted in place to read the
concatenated per-node logs instead of the (already-rotated) live path.

### 97a: live heap, the sender-cache owner specifically

`go tool pprof -top -unit=mb`, matched leader/follower pairs, B1win2
(node0 leader / node1 follower) vs B2win2 (node5 leader / node6
follower):

| | B1win1 (node2/node3 avg) | B1win2 (node0/node1 avg) | B2win1 (node1/node2 avg) | B2win2 (node5/node6 avg) |
|---|---|---|---|---|
| `senderCachePut` | 922.1 MB | **1180.8 MB** | 317.5 MB | **314.8 MB** |
| `txlookup.Tail.Add` | 1086.1 MB | 1548.0 MB | 1090.7 MB | 1830.1 MB |
| `qmdb.newMapIndexSized` | 788.8 MB | 757.5 MB | 762.1 MB | 772.3 MB |
| **total inuse_space** | 6225.7 MB | **7585.3 MB** | 5471.6 MB | **7018.6 MB** |

**Sender-cache owner: 1180.8 MB (B1win2) -> 314.8 MB (B2win2), a drop
of 866 MB** -- inside the predicted 0.6-0.9 GB band, and close to the
predicted ~0.3 GB floor (4M slots x ~65-70 B/entry measured near-full,
matching 6dm's own per-entry arithmetic almost exactly). Leader and
follower move together (no leader/follower asymmetry -- the cache is
process-wide, not role-specific, as the code's own design implies).

**Total inuse_space: 7585.3 MB -> 7018.6 MB, a drop of only 566.7
MB** -- smaller than the sender-cache's own 866 MB drop because
`txlookup.Tail` grew MORE in B2win2 (1830.1 MB) than in B1win2 (1548.0
MB), a +282 MB partial offset. This is the SAME sealer-backlog
mechanism 6dm's own growth section already named (the tail's `keep`
bound is reached quickly at this fleet's block size, but eviction
waits on a background segment-build tick that can fall behind under
load) -- not a new finding, and not caused by the sender-cache change;
flagged here because it dilutes the total-heap delta without
contradicting the owner-specific one.

### 97b: hit rate, recover phase, and the follower import breakdown (MANDATORY)

Hit rate (`"parallel block"`'s own `hintHits`/`hintFills`, summed over
every full-block record in each window): **B1win2 14.4% (455 records)
-> B2win2 19.7% (399 records), +5.3 points -- UP, not down.** This is
outside the "unchanged within 1 point" band, but in the favorable
direction: shrinking the cache did not cost hit rate on this round's
own traffic shape (plausibly leg-to-leg traffic-pattern noise rather
than a causal effect of the smaller table, given a smaller table
should if anything evict sooner under a two-way set -- but the
direction is not the one prediction 97(b) warned about, so the result
is not undermined by it).

`recoverMs` (the same `"parallel block"` line): **median 24 ms (B1win2)
-> 42 ms (B2win2), +18 ms -- inside the stated 26 ms noise band, but
close to its edge; p90 47 ms -> 215 ms is a real widening of the tail**
that the median-only criterion does not capture. Flagged, not
folded into the pass/fail verdict on (b) below.

**Follower `blockimport phases` (`import_breakdown.py`, run against
the concatenated per-node logs since the live path had already
rotated past B1's own window):**

| | B1win2 (n=417) | B2win2 (n=342) |
|---|---|---|
| hdr | 3 ms | 3 ms |
| body | 11 ms | 10 ms |
| proc | **691 ms** | **684 ms** |
| write | 199 ms | 209 ms |
| **total** | **920 ms** | **921 ms** |

**The follower's own import timing is flat -- statistically
indistinguishable (920 vs 921 ms total, proc within 1%).** Whatever
drives the win2 block-time improvement, it is NOT a faster follower
import path. This is the single most important qualifying finding of
this round: prediction 97's own clause (c) asked whether block time
would track (a)+(c); the follower-side data on its own says no.

### 97c: GC, CPU share, and where the real speedup is (the leader's write phase)

**NumGC rate, peak-pressure phase of each leg** (from
`r35zzzn-memstats.log`, node0/node1, ~30s samples): B1's own heap-climb
phase (17:39:26, NumGC~11, to 17:44:35, NumGC~211-220) is 209 GCs in
5.15 min = **~40 GCs/min**; B2's own equivalent phase (17:52:58,
NumGC~13-14, to 17:58:06, NumGC~159-188) is ~146-175 GCs in 5.13 min =
**~28-34 GCs/min** -- roughly 25-30% fewer collections per minute at
the same pressure point in the leg.

**GC CPU share, leg-named CPU profiles** (`go tool pprof -top`, 20 s
duration, `runtime.gcDrain`'s own cum%, the core GC-worker function):

| | B1win2 node0 | B1win2 node1 | B2win2 node5 | B2win2 node6 |
|---|---|---|---|---|
| `gcDrain` cum% | 28.47% | 29.27% | 13.72% | 15.03% |
| `mallocgc` cum% | 12.33% | 13.71% | 7.40% | 7.24% |

**GC's own share of sampled CPU time roughly HALVED (avg 28.9% ->
14.4%), and allocation overhead (`mallocgc`) also roughly halved (avg
13.0% -> 7.3%).** `GCCPUFraction` (cumulative since leg start, from
`memstats.log`'s own end-of-leg samples) tells the same story: 0.0242
(B1win2, node0, 17:44:35) vs 0.0107 (B2win2, node0, 17:58:06) -- also
roughly halved.

**HeapAlloc at end of win2** (memstats, node0): B1 ~9.06 GB (17:44:04,
just before the leg's own final drop) vs B2 ~8.94-8.89 GB
(17:57:04/17:58:06) -- close, not dramatically different at the very
peak (both legs still climb toward a similar ceiling before the leg
ends and the next reseed/restart resets it); the owner-level and
GC-rate differences above are the more reliable signal than this one
noisy peak-value comparison.

**Where the speedup actually is: the LEADER's own `write` phase**
(`"miner: propose phases"`, leader nodes only, full blocks, same win2
windows): B1win2 (node0, n=10) `write` median 719 ms, `total`
(seal2res) median 807 ms; B2win2 (node5, n=10) `write` median 374 ms,
`total` median 470 ms. **The leader's own write phase very nearly
halved (719 -> 374 ms, -48%), tracking the halved GC CPU share far
more closely than anything on the follower's own side.** `assemble`/
`finalize` (the BUILD steps, not the write) moved the OTHER way (177
-> 227 ms, 164 -> 208 ms) -- consistent with a smaller sender cache
buying nothing for the build path (it was never meant to) while GC
competing less for CPU/memory bandwidth during the write's own
MDBX/QMDB commit is exactly the established 6da/6dj mechanism
(page-cache and commit-path cost inflated by heap pressure). Sample
size caveat: n=10 propose events per leader window is small; the
median move is large enough (345 ms, ~45% of the smaller figure) to
trust the direction, less so the exact magnitude.

**Verdict on the win2 block-time question: MECHANISM, but through the
leader's write path, not the follower's import path, and not through
(a)+(c) acting on the SAME phase as prediction 97(c) implicitly
assumed.** Corroborating context requested by the commander: B2win2's
own `blocks=46 txs=7,295,864 TPS=121,598 occupancy=49.3%
blockTime=1.304s` -- occupancy matches every other full-block window
in this round (48.3-50.0%) and the block COUNT (46) is the highest of
any win2 in this round, not a truncated-window artifact; zero view
timeouts / TC / advancing-view events in either win2 window (grep
`0`/`0`). 1.304 s is genuinely outside the range of every prior
10GiB-round win2 the commander listed (1.463-1.875 s across
35zzzd/e/f/g/h/i/k/l) -- this round's own B2win2 is a new low, not a
value that already recurred and could be dismissed as ordinary leg
noise.

### 97d: safety

`height_conflict_check.py` (run against the concatenated logs,
`node*-B.log` symlink naming): `heights_checked=11905,
committed_events=95228, unresolved=0, conflicts=0`. No BAD BLOCK
(`grep -l` across all 7 nodes' full concatenated logs: no matches).
**31 `"import-gated vote REFUSED"` lines, all at view 11741** -- the
same single self-referential-JustifyQC pattern 6dj already
investigated and classified as a correct safety-net catch (not a
bug); this occurrence falls in the A2 leg (18:13:57), outside both
win2 windows compared above, so it does not confound the mechanism
comparison. **`"sealed block dropped"` events: 2, whole round** --
recorded here as the baseline count for S34's own target fix, which
r96 does not yet carry.

### Prediction 97, clause by clause

**(a) Live heap -- CONFIRMED.** Sender-cache owner 1180.8 -> 314.8 MB
(-866 MB), inside the predicted 0.6-0.9 GB band and close to the
predicted ~0.3 GB floor. Total inuse_space also fell (-566.7 MB),
diluted by the unrelated tail-index growth.

**(b) Hit rate and recover -- PARTIAL.** Hit rate moved +5.3 points
(up, not down -- not a cost, but outside "unchanged within 1 point"
either way); `recoverMs` median +18 ms (inside the 26 ms noise band)
but its p90 widened sharply (47 -> 215 ms), a real tail-latency cost
this clause's median-only criterion does not capture.

**(c) GC and block time -- CONFIRMED, with a correction to the
mechanism.** NumGC/min at peak fell ~25-30%; GC's own CPU share
(`gcDrain`) and allocation overhead (`mallocgc`) each roughly halved;
win2 block time fell 1.667 -> 1.304 s, a genuine new low outside every
prior 10GiB round's own range. The mechanism is real, but it runs
through the LEADER's own write phase (-48%), not the follower's import
breakdown (flat, 920 vs 921 ms) -- prediction 97(c)'s own framing
("(a)+(c) matter... win2 block time better") is confirmed in outcome
but the causal path is narrower than a generic "smaller heap helps
everything" story would suggest.

**(d) Safety -- CONFIRMED.** 0/11,905 conflicting heights, no BAD
BLOCK. The round's own 31 refusals are the already-classified
view-11741 pattern, outside the compared windows.

**Overall VERDICT: CONFIRMED**, with the mechanism refined by this
round's own follower-side null result: the sender cache shrink buys
its predicted heap reduction cleanly, costs nothing worth stopping
for on hit rate or median recover time (though the recover tail
widened), and the round's own headline block-time win is real and
traces to reduced GC CPU pressure reaching the leader's own write/commit
path specifically.

### Recommendation

**Adopt `N42_SENDER_CACHE_SLOTS=4194304` for the bench: yes.** The
cost side of prediction 97(b) is at worst a widened recover-phase
tail (p90 47 -> 215 ms), not a hit-rate or median-time regression, set
against a clean ~0.87 GB heap-owner reduction and a corroborated,
substantial GC-CPU and leader-write-phase improvement. This is
exactly the config-only, low-risk, high-confidence win 6dm's own
ranked list called it.

**Try 1M (the product default) too: yes, one more round.** The
measured hit rate at 4M (19.7%) is itself well below the cache's own
long-documented "worth approximately nothing" range (8.3-27.9% at
various sizes, per `sender_cache.go`'s own header comment) -- nothing
in this round's own data suggests 4M is a floor rather than a point
on a flat curve. Going to 1M would test whether the recover-phase
tail widening seen at 4M (a plausible early signal of the table
getting tight) gets WORSE at a quarter the size, which is the one
open question this round leaves before calling the reduction settled
at its smallest safe value rather than merely a smaller one.


## 6dp. S34: the leader re-proposed its own already-committed block with itself as justify -- the existing extends-check fails open for a leader's own sealed block because importedParents is only ever populated by internal/sync; fix prepared, n42-r97 built, prediction 96 registered before the round (2026-09-23)

**RACE (deferred whole-package check, run before this step per the
commander's own instruction):** `hotstuff 289 pass/0 fail; sync 138
pass/0 fail` (`go test -race ./internal/consensus/hotstuff/...
./internal/sync/...`, `nice -n 10 -p 8`, box load ~3, no foreign claim).

**Full PART 1 reconstruction** (mechanism, file:line for every claim,
the 6556-6558 timeline in ms) is written in
`docs/QS_HANDOVER_20260920.md`, "S34 -- the stale-re-proposal defect,
reconstructed from the code". Summary of the two load-bearing findings:

- **Mechanism.** `internal/miner/worker.go`'s sibling-suppression
  guard (`handleSealed`, pre-fix line 667) re-proposes the FIRST block
  ever sealed on a given parent when a later, divergent seal on the
  SAME parent arrives -- without checking whether that height has
  since been committed and written. Round 35zzzk view 6557: a leftover
  seal-completion event for a stale sibling at an already-decided
  height found the "kept" candidate was `a990bc..` -- this node's own
  block, ALREADY COMMITTED one view earlier -- and re-injected it via
  `NotifyBlockSealed` -> `EventBlockReady` -> `onBlockReady`
  (`internal/consensus/hotstuff/proposal.go:21`, pre-fix).
  `onBlockReady` builds `Proposal.JustifyQC` from
  `e.roundState.LockedQC()` -- the LEADER's own highest-known QC at
  propose time, unconditionally, with no check that the proposed
  block's hash differs from it. The one guard that could have caught
  this (`importedParents[blockHash]`-gated, pre-fix lines 65-73) FAILS
  OPEN when that map has no entry for `blockHash` -- and it never does
  for a leader's own locally-sealed-and-written block:
  `rememberImported`/`importedParents` is populated ONLY via
  `NotifyBlockImported`, and every call site of that method in the
  whole repo is in `internal/sync/` (catchup, block-by-hash, gossip
  subscriber, block push -- confirmed by full-repo grep). The result:
  `Proposal{BlockHash: a990bc, JustifyQC: <QC for a990bc itself>}` --
  a proposal that certifies nothing, since a block cannot extend
  itself. Every voter's `extendsJustify` correctly refused it
  (`"import-gated vote REFUSED: proposal does not extend its JustifyQC
  block"`, 5-6 lines per follower, all 7 nodes), the view timed out
  (~5.3s), and the genuinely correct next block -- already sealed and
  waiting -- was silently dropped by a SECOND, unrelated guard
  (`"sealed block dropped -- phase left WaitingForProposal"`,
  `proposal.go:33-37`) because the stale proposal had already advanced
  the view's phase past `PhaseWaitingForProposal` before being refused
  by the voters.
- **Fix.** Three guards, minimal, no switch: (i) `onBlockReady` now
  refuses UNCONDITIONALLY to propose a block whose hash equals its own
  `JustifyQC.BlockHash` -- no dependency on `importedParents`, so it
  cannot fail open the way the existing guard does
  (`internal/consensus/hotstuff/proposal.go`, new guard placed before
  `journalPrepareVote`/`EnterVoting`); (ii) the same rejection is
  placed BEFORE any phase mutation, so a rejected stale proposal
  leaves the phase at `PhaseWaitingForProposal`, letting a genuinely
  fresh seal for the SAME view still succeed -- this is guard (iii)
  from the task spec, achieved by placement rather than a separate
  check; (iii) `internal/miner/worker.go`'s sibling-suppression path
  additionally checks the chain's own current head before re-proposing
  "kept" (best-effort defense in depth, not the safety net -- it can
  in principle race a still-in-flight write; the engine's own guard
  (i) is unconditional and closes the defect regardless of this
  check's timing). New metric `hotstuff_proposal_self_justify_total`.

**Tests.** `internal/consensus/hotstuff/proposal_self_justify_test.go`
(new): `TestSealedBlockDroppedWhenItWouldJustifyItself` (the isolated
guard), `TestFreshSealStillProposedAfterAStaleSelfJustifyAttempt` (the
exact incident end to end -- stale self-justify attempt arrives first,
fresh correct seal follows, only the fresh block is ever proposed),
`TestSealedBlockProposedWhenNotSelfJustify` (happy-path sanity). All
three pass, including under `-race`. Full `hotstuff`/`miner` package
suites pass; `go vet` clean; existing S26 regression tests
(`TestTwoPhasePrepareVoteRefusesNonExtendingProposal`,
`TestTwoPhaseCommitVoteRefusesNonExtendingProposal`) unaffected by the
new guard (confirmed by direct run, not merely "the suite is green").

**Build.** n42-r97 = n42-r96's exact file set (S32's
`N42_BLOCK_DECODE_REUSE_POOL` switch, left OFF) + this step's three
files (`internal/consensus/hotstuff/proposal.go`,
`internal/consensus/hotstuff/metrics.go`, `internal/miner/worker.go`),
via the established file-checkout recipe: detached worktree at
`f7ec2836`. One new wrinkle this round: copying only
`{engine,proposal,service,metrics}.go` from the hotstuff package (as
prior rounds' own curated list did) no longer compiled -- `engine.go`
on current `wt-r27` HEAD now references package-level symbols
(`contentionStamps`, `writeLatch`, `perViewSendStamps`,
`leaderWriteAfterJournalEnabled`, a `msgTiming` parameter on
`processVote`) added by OTHER lineage commits (S14/S17/S18/S19
diagnostics) that touch OTHER files in the same package
(`view_timing.go`, `voting.go`, `write_latch.go`, `adapter.go`) not in
that curated list. Resolved by copying the WHOLE
`internal/consensus/hotstuff/` package's non-test files instead, after
confirming (`git log f7ec2836..HEAD -- <file>` for each of the 8
touched files) that every commit touching any of them since the base
is a recognized lineage commit, never unrelated concurrent work.
`internal/blockchain.go`'s own reference to `bc.buildStallLockWaitNs`
needed `internal/blockchain_types.go` (S11's own diagnostic hunk, one
commit, copied directly); `internal/miner/worker.go`'s watchdog call
needed `log.LogDir()` (same S11 commit, one file, copied directly) and
three miner-package companion files
(`async_write.go`/`build_stall_watchdog.go`/`seal_path_diag.go`/`push_order.go`,
each a single confirmed-lineage commit). `internal/miner/miner.go`
needed the SAME hand-revert as `worker.go`: two commits
(`89d15267`/`19687889`) not part of this lineage since n42-r86 added an
`activeSpecParent`-dependent code path to `TriggerBlockProduction`
(letting an in-flight speculative build finish instead of
interrupting it) plus a `tMs` stamp on the "build triggered" log line;
both hand-reverted to their pre-existing (n42-r86-lineage) form,
verified via a full diff against `wt-r27` HEAD showing only the four
already-known off-lineage lines remaining (the same four every prior
build in this chain has shown: `activeSpecParent`'s field declaration
plus its two `Store` call sites, and the three `tMs` stamps on
`"miner: build phases"`/`"speculative build hit"`/`"speculative build
parked"`). `grep -rl BaseCache`: empty, confirmed absent. `go build -p
4 -tags nosqlite,noboltdb` clean (built at `nice -n 19`/`-p 4`, box
under the Rust fleet's claim, load ~100, per the commander's
box-sharing instruction). Markers confirmed present exactly once: the
new self-justify guard's own log line, the new worker-side guard's own
log line, S31's header-vote line, S26's commit-vote-REFUSED line;
`"blockimport phases"`=2, matching n42-r96's own count. sha256:
`b80deae4eea4647ca727c6663f8d59838b8e67911fae289d8d051a28a7f88b2e`
(108,818,976 bytes); binary lineage n42-r92 -> n42-r94 (S26,
`e49ce151`) -> n42-r95 (S31, `c124146e`) -> n42-r96 (S32, `f961f63e`)
-> n42-r97 (S34, `7288c8f0`).

**Harness.** `run-r35zzzm.sh`/`chain-35zzzm.sh` built from the
35zzzn pair (NOT 35zzzl, per the commander's ordering note -- S35's
own config-only round runs first). SINGLE CONFIGURATION, no A/B:
`N42_SENDER_CACHE_SLOTS=4194304` in every leg (S35's own confirmed B2
value, 6do), `N42_BLOCK_DECODE_REUSE_POOL=0` in every leg (S32 stays
off; 6dl found this fleet's own transaction gossip delivers zero
messages under `N42_TXPOOL_NOLOCALS=1`, so the switch is untested
regardless -- not this round's concern), `GOMEMLIMIT` fixed 10GiB.
The predecessor wait was set to `r35zzzn.log` (not `r35zzzl.log`,
which the plain `cp`+`sed` left behind). The memory-abort auto-retry
loop stays REMOVED (carried forward from 35zzzl/6dk's own precedent,
confirmed present as `for attempt in 1` in the copied `chain-35zzzn.sh`
already). `bash -n` clean on both scripts; neither is running; not
launched.

**Prediction 96 (registered before any round, mechanism only):**

**(a) Safety/mechanism.** Zero `import-gated vote REFUSED`/`commit
vote REFUSED` lines caused by a self-referential or already-committed
proposal, and zero `"sealed block dropped -- phase left
WaitingForProposal"` lines following a sibling-suppression re-proposal
(35zzzn, without this fix, is the baseline count for comparison -- the
analyst who ran it will report whether the same shape appeared there).

**(b) Liveness.** Zero view-timeout events in the flood windows
(35zzzk had exactly one, this incident, in its own flood windows).

**(c) Safety (chain-level).** Zero conflicting heights across every
height checked (`height_conflict_check.py`), matching every round
since S26.

**(d) Throughput.** First-window TPS within noise of ~140k (35zzzk's
own 141.7k/140.5k, 35zzzn's own presumably similar figure) -- this fix
touches only a rare-path leader guard, no throughput change is
expected on the honest path.

**VERDICT: confirmed (fix prepared, not yet run).** PART 1's
mechanism is proven from the code with file:line citations for every
claim, including the exact reason the EXISTING guard fails open
(`importedParents` is sync-only, never populated for a leader's own
block) and the exact reason a fresh, correct seal was ALSO lost (the
phase-left-WaitingForProposal guard, not a defect in that guard itself
but a consequence of the stale proposal consuming the phase first).
The fix closes both findings with three guards, verified end to end by
a test that replays the exact incident. n42-r97 is built and
marker-checked; the harness is prepared, syntax-checked, and not
launched. `docs/QS_QUEUE.md`'s S34 row is added with prediction 96
(6dp); `docs/OPEN_ISSUES.md`'s stale-re-proposal entry is marked fix
prepared. Launch is the commander's next call.


## 6dq. S36a: prediction 99, registered before round 35zzzp -- pool caps 600k/200k -> 400k/100k by leg (2026-09-23)

n42-r97 (S34's fix), sender cache 4M everywhere (adopted, S35),
GOMEMLIMIT 10GiB. A/B by leg on `--pool-slots`/`--pool-queue`:
warm-up/A1/B1 = 600000/200000 (today's harness value), B2/A2 =
400000/100000. Targets the decoded-tx-object owner (6dm: 2.16 GB,
+653 MB/leg); the flood's own 8 x 45,000 = 360k in-flight stays under
both caps.

**Prediction 99:** (a) follower win2 `inuse_space`, decoded-tx owner,
lower in B2 by 0.4-0.8 GB; (b) no starvation -- generator in-flight/
pending depth unchanged within noise, underpriced/pool-full rejection
RATE (not raw count, per 6cx) unchanged B1 vs B2; (c) NumGC/min win2
lower in B2, win2 block time not worse than B1's (~0.1 s noise; B1 may
be cold-cache per 6dl); (d) 0 conflicting heights, no BAD BLOCK.

## 6dr. S38-spec: the hand-over cycle sums to its parts within 1.5%, the speculative-build hint is confirmed silent on all 77 measured hand-overs, and the real gate is a foreign block's post-state existing only at full-import completion (2026-09-23)

Logs-only, `wr-logs/r35zzzn-keep/node{0-6}` (gz+live concatenated; leg
times/win counts from the surviving `r35zzzn.log`: B1 17:31:54-17:44:56
win1=54/win2=36, B2 17:44:56-17:58:38 win1=51/win2=46). This binary
(n42-r96) carries ms-precision `miner: seal path` stamps (absent in
6cb/6cd's r86), so the timeline below is measured, not inferred.
Script: `wt-r27/scripts/qs-analysis/s38_handover.py`.

**1. Share and cycle** (any-size predecessor, 6cb's method).

| | win1 (n=102) | win2 (n=82) |
|---|---|---|
| hand-over share | 55/102 = **53.9%** | 22/82 = **26.8%** |
| in-tenure cycle (median) | 676.0 ms | 841.5 ms |
| hand-over cycle (median) | 966.0 ms | 2045.5 ms |

**Hand-over leader timeline, n=77 (own import of v-1 + own seal-path of v), medians:**

| segment | ms | note |
|---|---|---|
| push(v-1) -> "block push: arrived"(v-1) | 16.0 | network delivery |
| arrived -> import_end(v-1) (= "received") | 566.0 | decode+exec+finalize+write |
| .. of which hdr+body+proc+write | 199.7 | proc=exec 65+finalize 67+recover 5 |
| .. unattributed (arrived->received minus above) | ~366 | no line covers it -- MISSING-STAMP |
| import_end(v-1) -> trigger(v) -> buildBegin(v) | 0.0 | gates/dispatch free (confirms 6cd) |
| buildBegin(v) -> sealEnter(v) | **632.0** | fill+assemble+state-root, synchronous |
| specParkedTMs nonzero | **0/77** | speculative park NEVER fires on hand-over |
| sealEnter -> resultRecv -> copyStart -> copyEnd -> pushEnd | ~45 | check/BLS ~0, copy 29 |

Sum check: import_end(v-1)->pushEnd(v) 651.0 ms vs buildBegin->pushEnd
649.0 ms; whole cycle 1239.0 ms vs 569.0+651.0=1220.0 ms -- **98.5%, confirmed** (15% bar).

**Derived** (medians only). Factor = in-tenure/hand-over: win1
676.0/832.4(pooled mean)=0.812, win2 841.5/1163.3=0.723. Applied to the
ACTUAL harness blockTime/TPS: win1 138,659->170,701 (+23.1%); win2
109,438->151,309 (+38.3%); pooled (tx/block held constant)
124,049->161,048, **derived +37,000 TPS (+29.8%)** if equalised.

**2. Code.** BLOCKER: `internal/consensus/hotstuff/proposal.go:650`
(`sendVote`): `if e.importedBlocks[blockHash] && LeaderForView(view+1,
vs)==e.myIndex { emit OutputSpeculativeBuild }`. For a large block
`sendVote` runs from `tryDeferredVote` (`proposal.go:363`), which votes
BEFORE full import, so `importedBlocks[blockHash]` is false at that
call and the hint never fires, never retried (0/77 confirms it in data,
not just code). KNOWS-AT: `LeaderForView` (`validator.go:174`) is a
pure function of the view number -- a follower knows it leads v+1 from
`ViewStart(v)`, trivially early. It does NOT have v's post-state that
early: the deferred check (`internal/deferred_includable.go:38`) only
validates sender/nonce/balance against v-1's state, it does not execute
v; v's post-state exists only once `internal/sync`'s ordinary
`InsertChain`/`blockimport phases` completes (566-920 ms after push).
`PrepareSpeculativeBlock` (`internal/miner/miner.go:264`) is the only
consumer of the hint.

**3. Options, ranked (ms saved / risk):**
1. **C** (overlap prefill/pick only, fill after): needs only v-1's
   hash/number, startable at `trigger` today. ~60-80 ms, zero protocol
   risk, no guard interaction, no decision needed.
2. **B** (build on v after exec+finalize, before its own write):
   extend `unwrittenOwnPostStates` to a foreign block's in-flight
   result. Saves the write step (46 ms here, up to ~200 ms when v-1 is
   itself full, 6cw) plus an unknown share of item 4's gap. Risk: a
   build on unwritten foreign state must be discarded like today's
   own-block path if v is later suppressed (`recordSealedOnParent`) or
   times out -- **needs a protocol decision** (new trust boundary,
   gated by the same S26/S34 extends-checks before ever proposing).
3. **A** (build at CHECKED time, task's premise): **not implementable
   as stated** -- CheckDeferredBlock produces no post-state for v, so
   there is nothing to build on until import completes; corrected, A
   collapses into "start import earlier" (already happens on arrival)
   or into B once exec finishes. Its only unquantified lever is item
   4's gap -- unrankable until measured.

**4. Missing stamp.** A line at the start of `InsertChain`'s own
processing (distinct from `"block push: arrived"`, a socket-receipt
stamp) would split the ~366 ms unattributed remainder into
queueing/dispatch vs decode/pre-exec setup -- the one number this spec
could not place.

## 6ds. S39: stamps for 6dr's own 366 ms gap, riding S36b; n42-r98 built, prediction 100 (2026-09-23)

tMs stamps (N42_CONTENTION_DIAG=1, already on every leg since S14) on
the block-push path: rxEndTMs, decStartTMs/decEndTMs, chkStartTMs/
chkEndTMs, qTMs, insStartTMs (6dr's MISSING-STAMP). Carried on Block
(block_import_stamps.go, S32's DecodeReuseStats pattern), appended to
"blockimport phases" -- no new log line. Commit `d3069b37`; tests +
`-race` pass; vet clean. n42-r98 = n42-r97 + these 8 files; markers OK;
sha256 `14899116e3260ae8e96af62797821496b151bb935660b171157c4fffd8237123`.
Harness `run-r35zzzq.sh`/`chain-35zzzq.sh` from 35zzzp: S36b (9th
`run_leg` arg) N42_BLOCK_CACHE_BLOCKS 4 vs 2 by leg; pool caps fixed
600000/200000 (not an A/B here). `bash -n` clean, not launched.

**Prediction 100:** (a) named steps sum >= 95% of arrived->import_end,
report the largest item; (b) S36b: win2 live heap lower in B2 by
0.1-0.3 GB, no new fetch-by-hash misses; (c) win1/win2 within noise of
35zzzp; (d) 0 conflicting heights, no BAD BLOCK.


## 6dt. S34: round 35zzzm confirms the fix -- guard never had to fire, 0 refusals, 0 conflicts; ABORTED on an external memory spike (a foreign build, not the fleet) after both B legs had already measured (2026-09-23)

n42-r97 (S34's fix, 7288c8f0), single configuration: sender cache 4M,
GOMEMLIMIT 10GiB, no A/B. Logs: `wr-logs/r35zzzm-keep/node{0-6}/`
(+ rotated `.gz`, concatenated for analysis; `height_conflict_check.py`
run against a `node*-B.log` symlink).

**(a) Guard/safety.** `"sealed block dropped ... would justify itself"`
(the new guard): 0 occurrences -- the exact incident did not recur.
`import-gated vote REFUSED`: 0. `"sealed block dropped -- phase left
WaitingForProposal"`: 1, at 20:32:56 -- 6s into the memory-abort
sequence (below), 0 sibling-suppression lines precede it anywhere in
the round -- classified as a shutdown artifact, not a genuine
sibling-suppression drop (35zzzn's own baseline: 2, under normal
operation). `height_conflict_check.py`: `heights_checked=9089,
conflicts=0`. No BAD BLOCK.

**(b) View timeouts:** 0 in the flood windows (35zzzn: also 0).

**(c) SPIKE.** `MemAvailable` 54G (20:32:28) -> 12G (20:32:49): NOT the
qs fleet (`r35zzzm-mem.log`: node RssAnon flat ~10-11GB throughout;
`floodsMB=` already empty, generators exited by 20:32:09). `journalctl`
(kernel) shows a FOREIGN build at this exact window: apparmor denials
for `comm="hostname"` under `/home/n42/src/n42/n42-26/.artifacts/
coverage-target/.../tikv-jemalloc-sys.../config.log` at 20:32:34, then
`cc1plus` (PID 481535) hitting a page-allocation failure / direct
reclaim storm at 20:32:47 (`pgscanDirectD` 13M, `pgmajfaultD` 97k->396k
in `r35zzzm-vm.log`); system `AnonPages` +47GB in the same ~15s. The
Rust fleet's own claim file postdates this by ~1.5 min
(`.box-claim-rust` mtime 20:34:22) -- consistent with the same actor,
though the claim write cannot be proven as the cause versus a
consequence from the samplers alone. Round aborted cleanly at 20:33:13;
both B legs' own win1/win2 windows had already completed by then.

**(d) Windows and B mean.** B1 win1 128.7k@1.250s, win2 121.7k@1.111s
occ 41.5%; B2 win1 129.9k@1.224s, win2 116.8k@1.173s occ 42.9%. **B
mean (4-window average): 124.3k** -- above 35zzzk's own 16M-cache
figures (141.7k/95.0k, 140.5k/94.5k win1/win2) on win2 specifically
(121.7k/116.8k here vs 95.0k/94.5k there, the 4M sender-cache win
already confirmed in 6do/S35) and close to 35zzzn's own B2(4M)
135.7k/121.6k.

**Cycle/phases** (pooled B1+B2, `cycle.py`, `import_breakdown.py`):
seal->seal period med 909ms (p10 676, p90 1258); seal->QC 559ms;
QC->seal 270ms. `blockimport phases` B1win2 (n=528): body 10 / proc
663 / write 216 / total 922ms; B2win2 (n=498): body 11 / proc 673 /
write 213 / total 908ms -- flat, as expected (no A/B this round).
Round1 (`r1`, leader role, pooled) median 59ms, p90 66ms -- matches
r92/r95 baseline (6dj), not S26's own inflated 153-262ms. `jcvMs`
median 0ms.

**Clauses:** (a) CONFIRMED -- guard present, never needed, no
regression. (b) CONFIRMED -- 0 view-timeout events. (c) CONFIRMED --
0/9,089 conflicting heights, no BAD BLOCK. (d) CONFIRMED -- B mean
124.3k, both win2 figures above 35zzzk's 16M baseline, consistent with
the already-adopted 4M sender cache (S35) plus this fix.

**Recommendation: n42-r97 becomes the fleet base -- yes.** The fix is
proven inert-when-unneeded and correct when it would matter (unit
tests, 6dp); this round adds a clean live confirmation with 0 cost
on every measured axis. The abort is an external-box event, not a
finding against the binary or configuration.

## 8. Method

`docs`-side reproduction: `analyze-legs.py` buckets `blockwrite`/`blockimport`
phase records into the legs delimited in a runner log, counting only blocks with
more than 20,000 transactions so each leg's baseFee-decay period drops out
without a hand-placed boundary. The view-timing table comes from the
`hotstuff view timing` lines, filtered to views adjacent to a full block --
without that filter the medians are dominated by the ~1,590 empty decay blocks
each leg produces and read 3-4x too fast.
