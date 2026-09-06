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

## 8. Method

`docs`-side reproduction: `analyze-legs.py` buckets `blockwrite`/`blockimport`
phase records into the legs delimited in a runner log, counting only blocks with
more than 20,000 transactions so each leg's baseFee-decay period drops out
without a hand-placed boundary. The view-timing table comes from the
`hotstuff view timing` lines, filtered to views adjacent to a full block --
without that filter the medians are dominated by the ~1,590 empty decay blocks
each leg produces and read 3-4x too fast.
