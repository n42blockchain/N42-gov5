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

## 8. Method

`docs`-side reproduction: `analyze-legs.py` buckets `blockwrite`/`blockimport`
phase records into the legs delimited in a runner log, counting only blocks with
more than 20,000 transactions so each leg's baseFee-decay period drops out
without a hand-placed boundary. The view-timing table comes from the
`hotstuff view timing` lines, filtered to views adjacent to a full block --
without that filter the medians are dominated by the ~1,590 empty decay blocks
each leg produces and read 3-4x too fast.
