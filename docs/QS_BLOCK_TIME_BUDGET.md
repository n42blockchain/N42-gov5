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
