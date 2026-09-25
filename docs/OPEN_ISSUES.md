# Open issues

Known defects and deferred work that no round or commit has resolved yet.
Each entry names the evidence, the blast radius, and what "done" is. Newest
first; move an entry to the commit that closes it, do not delete it here.

## Falcon-512 signature verification is forgeable (reported 2026-09-06, scheduled later)

`crypto/falcon/` is a simplified Falcon: `computeNTRUComplement`
(`crypto/falcon/internal.go`) is marked "a simplified version for
demonstration", and `falcon.Verify` (`crypto/falcon/falcon.go`) accepts
signatures the reference verifier would reject. A signature that passes
`Verify` can be produced without the private key.

Blast radius: the 0x14 precompile (`internal/vm/pq_contracts.go`,
`falconVerify`, gas 3500) and any account or transaction type whose
`SigAlgo` selects Falcon. The precompile is not in the standard fork maps
and activates only through `ChainConfig.PQPrecompilesTime`, so no live chain
is exposed today; `docs/DEVLOG.md` still lists Falcon-512 as production ready
and must not be believed until this is closed.

Done means: a verifier that matches the NIST Falcon-512 reference on the KAT
vectors (accepts every valid vector, rejects every mutated one), or the
precompile refuses Falcon until then. Scheduled after the qs throughput
work; do not enable `PQPrecompilesTime` on any chain before it lands.

## QMDB live-tree reload is O(history) (2026-09-06)

`ReloadForBuild` replays the entry log, so a node's start time grows with
the chain (rounds 32-35: 60-90 s at 13.8M blocks). Done means a checkpoint
the reload can start from.

## Intermittent startup stall after big-block legs (2026-09-06, rounds 32/34; 35g/35h)

A node logs "qmdb index loaded" and then nothing for 10+ minutes; the leg
loses that node past the 600 s readiness deadline and the next start of
the same store is fine. Rounds 35g (node3) and 35h (node2) added the
shape: the "index loaded" line reports a SHORT index (2,230,875 and
6,187,593 of 8,500,566 keys, puts == liveKeys), which the load prints
from its deferred summary on error as well as on success -- so the twig
scan most likely returned an error part way, `NewNode` returned it, and
the process exited (the pprof dump at +150 s is 0 bytes because the
server never came up). The stalled start's run.log was overwritten by the
next leg's start before it was read. debb97ef names the twig in the error
and logs it; the 35i runner copies run.log/run.err at +150 s and reports
whether the process is alive.

Round 35j named it: node2, `qmdb: load twig 64710 of 132328 (nextSlot
271007173, active 132327, indexed so far 6260263): qmdb: twig metadata
inconsistent (stored root does not match leafRoot+bits)`, logged at
01:32:51 and the process exited; the same store loaded all 132,328 twigs
at 01:42. A sealed twig in the middle of the store, read through one
read transaction, disagreed with its own metadata once and not the next
time -- a transient read, not the disk. 88b01d9e retries the startup load
three times on a fresh computer and read view. Still open: WHY a sealed
twig's stored root can disagree with its leaves on one read (a stale
leaf row from the evict/flush path? a value read past its transaction?),
and whether the same can happen on the live path.

Round 35l (node5, 03:35): all three attempts failed, at twigs 37904, 82102
and 85715 of 142788 -- a different sealed twig each time, so nothing on
disk is bad; the fourth start (the next leg, ten minutes later) loaded
all 8.6M keys. Ruled out since: the MDBX binding always opens with
NOTLS, so an unpinned read view is not it; the SIMD twig hasher
(`hashNodesRun`) has no shared mutable state; `recompute` is synchronous.
Still suspect: the leaf rehydration into the shared `scratch` buffer
(`LeavesInto` / `decodeSparseLeavesInto`) under a store whose leaf rows
were written by the evict/flush path -- the failures cluster at fleet
start, when seven nodes scan at once. Next: run the load under the race
detector against a copy of a failed store, or log the twig's leaf-row
length and the recomputed vs stored roots on the mismatch.

Round 35r (2026-09-07): the same mismatch on the LIVE path. The miner's
speculative reload (`ReloadForBuild`, the persistent per-leader
computer) failed four times across the fleet in one day -- node3 twice
within ten seconds of a mid-leg relaunch (pre-warm at 12:07:55, twig
169440; first build at 12:08:04, twig 20732), node5 and node6 once each
-- always a different twig, always transient. So the read is not only a
startup phenomenon, and "seven nodes scanning at once" is not required:
node3's two failures were one process reading alone. The consequence was
worse than a slow build: the build fell back to the default root, sealed
a stale block 19.7 s later, and its write panicked the miner worker for
the life of the process (fixed in 1f1140e3 -- a failed reload now
abandons the build, a stale seal is dropped before its write, and a
panic costs one block, not the worker). The mismatch itself stays open.

Round 35z2 (2026-09-08 05:15 EDT): node3's three startup attempts failed
on twigs 172644, 179292 and 185698 of 219511 (a different one each time,
all in the top quarter of the store), the process exited and the leg was
lost; the next leg's start loaded all 8,833,566 keys. Retries raised to
six, five seconds apart (n42-r35ac). Still open: the loader itself.

## Leader in-memory divergence after a branch switch (2026-09-06 round 32; cause found 2026-09-07 round 35m)

Round 32: node3 proposed a block with a root nobody could reproduce; a
restart healed it. Round 35m reproduced it with a leader tenure: a stale
seal, a branch switch to the lowest-hash sibling, then the leader's next
speculative build proposed with a wrong root (six rejections, zero
transactions). The builder's persistent speculative root computer
reloads trusting its index below a cursor for the store's layout, and an
unwind rewrites entries below that cursor. 359e1f89 voids the trust on
every unwind and failed-block revert. Round 35n B1 (node3, 13886258): the void fired ("branch
switch"), the next build rebuilt for 20 s, and the proposal was still
rejected -- so the fault is the unwind itself: a leader that had applied
its own losing sibling at 13886257, unwound to the parent, imported the
kept lowest-hash sibling and built on it has a state the six others do
not. The 20 s rebuild also times out the view (24 s views seen in the
decay), and with a tenure the same leader keeps re-proposing at the
height, so siblings recur. Then the probe: node3's persisted state at
the kept sibling 13886257 is byte-identical to node0's and node1's
(same head hash, same root, the tree reload matches) -- the unwind is
right on disk. The wrong root came from the speculative build's timing:
it waited for the parent to be PERSISTED (`WaitBlockPersisted`, header
present), and a sibling's header is stored on arrival, before its state
is applied; a build started in that window read the previous state, and
for an empty block the only difference is the winner's reward credit.
580d2f32 made the build wait for the applied marker
-- and round 35p showed that was wrong too: the wait runs BEFORE
`AlignAppliedBranch`, which is what unwinds a locally applied sibling
so the consensus parent can be imported, so a leader that had applied a
sibling failed every view of its tenure ("consensus parent not applied
in time", three timeouts a tenure). Reverted (persisted wait + align,
the original order). The void on branch switch was removed too
(99ea54ad): its 10-23 s rebuild timed out views under plain rotation.

What is left, then, for the bad root after a switch: the persisted
state is right and the speculative computer rebuilt from it was still
wrong, and the only accounts that differed in those zero-transaction
blocks are the ones the losing and winning siblings both credit (the
coinbase and the dev faucet). The builder reads accounts through the
LIVE tree (`QMDBLatestAccountSource` -> `Lookup`), so the suspect is the
live tree's in-memory state after RevertBlock + the winner's apply (the
index or the live bits for those two keys), not the disk. A reload of
the live tree from disk after a mutating unwind would test it, but that
reload is O(history) (60-90 s here). Until then: tenure 1 (rotation has
run whole rounds clean); with tenure the sibling race after a stale
seal recurs and the leader that switched builds the bad block.

Resolved 2026-09-08 (2685afb5): the miner's persistent speculative
computer now receives every undo record the live tree's branch switch
applies and peels them on its next build, lowering its trust cursor to the
unwound block's first slot, so the reload rescans exactly the rewritten
slots. Round 35z reproduced the failure in an empty-block decay (BAD BLOCK
13964698 after a sibling switch at 13964697, all six followers agreeing
against the leader); 35z2 is the first round on the fix. Leave this entry
until a full tenure round completes without a BAD BLOCK.

Not resolved. 35z2 B1 (05:46 EDT) reproduced it WITH the miner rewind in
place and with three distinct roots for the empty block 13966002: the
miner's speculative tree (55c1, built after the rewind + incremental
reload), the leader's live tree (d866: the leader had applied 019b,
unwound it to build its own candidate, then converged back to 019b and
re-applied it), and all six followers (95f0: applied the sibling 1c89,
unwound it, applied 019b). A lib/qmdb round trip of both shapes passes,
so the difference is above the tree (QMDBRootComputer.RevertBlock and
its staged-flush / reclaim state, the re-import path after "converging
on lowest-hash sibling", or the miner's incremental reload after a
rewind). What always precedes it is a sibling storm caused by the
startup: the miner pre-warm held minerRCMu for ~3.5 min after HotStuff
started, so 11 views timed out and their stale candidates surfaced at
one height (bcadd720 makes the pre-warm synchronous). Next: instrument
the live tree root after every apply/unwind on all nodes, and reproduce
the exact 35z2 sequence in a test at the BlockChain level.

35z5 (2026-09-08 13:32 EDT) narrows it to the STARTUP revert and clears the
tree layer. The fleet started on a chain whose last blocks came from the
sweep fleet, stopped without draining its head. node3 came up with its live
tree at slot 489598909 against the other six at 489543974, logged "startup:
reverting speculative (uncommitted) blocks to last committed head" plus a
depth-1 branch switch, pre-warmed its miner tree from the repaired store
(489543974, matching), became leader for the next view and sealed 13978490
with root 06f305d9; all six followers computed 7b25df27. No view timed out
first and no sibling storm preceded it -- the pre-warm fix held -- so the
trigger is the startup revert alone. lib/qmdb reproduces none of it:
revert_reapply_test.go now covers revert-then-reapply, sibling-revert-then-
winner, and the startup shape (load a store that HAS the block, revert with
the persisted undo record through a marshal round trip, then reload the
repaired store into a third tree and seal the next block on it) -- all three
agree with a tree that never saw the block. So the divergence is above the
tree: the node's revert path (unwindForReimport per block, PlainState
alignment, the applied marker) or what the leader executes on top of it.
Next: log the live tree root and applied marker on every node after startup
repair, and have the leader refuse to build until its applied head equals its
committed head.

**2026-09-09, round 35za: the startup revert is NOT the cause.** The
fingerprint that 60233433 added answered the question it was built for. All
seven nodes came up on `root 4dac01037782e8a1 nextSlot 520655309 applied
13994588 head 13994588` -- identical, no node holding an uncommitted block.
Two blocks later 13994590 came out with THREE roots: the proposer's miner
tree sealed `d37eafb0`, the proposer's own live tree computed `4c7768f4`
("live QMDB tree root does not reproduce sealed root"), and all six
followers computed `e2a77c89`. The six agree, so the proposer is wrong on
both of its trees, and it was wrong before it built: its live tree had
already diverged while importing 13994589, the block it had itself sealed
in the previous leg and re-imported after the restart.

So the shape is: a node that sealed a block, restarted before it was
committed, and then re-imported its own block, ends up with a live tree that
differs from the nodes that only ever imported it -- while every startup
fingerprint says the state was identical. Whatever differs is not the root,
the append cursor, the applied marker, or (checked in the test below) the
index.

`lib/qmdb/revert_reapply_test.go` holds the closest handle: revert-then-
reapply, sibling-revert-then-winner, and the startup shape (load a store that
HAS the block, revert with the persisted undo record, reload the repaired
store into a third tree). The first two pass. The third FAILS -- but not
reproducibly: in isolation the same revert-then-apply produces the fleet's
root, and the failure appears only with a miner-tree load and index audits
between the revert and the apply, with a different wrong root as those
statements change. It is skipped unless `N42_QMDB_REVERT_INVESTIGATION=1`.
The next step is to bisect the statements between the revert and the apply --
that instability is itself the signal, and it points at state shared or
carried across two Tree instances in one process, which is exactly what a
node has (the live tree and the miner's speculative tree).

## Plain `Account` table frozen at 13,750,514 on the qs fleet (2026-09-06, round 26)

`N42_STATE_WRITE_QMDB_ONLY=1` stopped plain Account writes; QMDBMeta records
`accountFrozenAt`. Every later start of those datadirs needs
`N42_STATE_READ_QMDB=1`, or the miner, txpool and RPC read stale accounts.
No repair tool rebuilds the table from the tree yet.

## Root divergence on the first block after a restart -- mechanism found (2026-09-10)

Round 35zg B1: the leg's restart reverted the incoming leader (node6) to the
committed head 14029922 while consensus named 14029923, applied before the
stop, as the parent. `commitWork`'s align is a no-op when the applied head is
below the parent, so the speculative build of 14029924 ran on a miner tree
reloaded at 14029922, the parent was re-imported a second later, and the
parked task was proposed because the speculative-hit check compares only the
parent hash. Three roots for an empty block: proposer c6901af1, proposer's
live tree 921cf405, followers c63d8299. Guarded from n42-r41 on (a build
whose parent is not the applied head is abandoned or imports the parent
first); the two-tree design that makes the mistake possible is track 3b of
QS_REPLAN_2026-09-09.md.

## Sequential fallback dropped delta credits -- FIXED (2026-09-20, round 35zzx)

Round 35zzx died on a BAD BLOCK at 13659302: the leader (node0) computed
root b552d4, all six followers computed bfcb7f, and the leader then sealed
13659303 carrying b552d4, so every follower rejected it ("deferred
execution: header 13659303 carries parent state root ..., this node
executed ...").

The finalize traces agreed on everything that was easy to compare: 23000
transactions, 20154 dirty accounts, identical coinbase and faucet balances,
and 20151 accounts written on both sides. The one difference was the second
counter, which `IntraBlockState.DirtySetSizes` returns under the name
`dirtySlots` but has been repurposed to count dirty accounts that are EMPTY
(nonce 0, balance 0 -- the EIP-161 deletes): 17037 on the leader against 1
on the followers. 17037 is not 2x8191; EIP-2935 writes exactly one slot per
block (`vm.StoreParentBlockHash`), which is the followers' 1.

So the leader ended the block with 17,036 accounts emptied. The money had
left the senders on both sides -- the faucet balance matched to the wei --
and on the leader it never arrived.

Cause: the leader's build was the only one in the round to fall back. Its
`parallel block` line reads `fallback=true, aborts=9064, executions=41263`;
the followers imported the same block with `fallback=false`. A recipient the
block only credits is recorded as a DELTA write (`RecordDeltaWrite`), whose
`Value` is nil and whose `Delta` carries the increment. `executeParallel`
replays those through `MVS.WriteDelta`; `runSequential` replayed every write
through `MVS.Write`, where a nil value means DELETED. `applyMVSToIBS` then
saw `value == nil && delta == nil`, put the address in `deletedAccounts` and
called `ibs.Selfdestruct` on it. Every credit-only recipient in the block was
selfdestructed.

Fixed in internal/parallel/executor.go: `runSequential` now mirrors the
parallel write-back. Regression: `TestSequentialPathKeepsDeltaWrites`
(internal/parallel/sequential_delta_test.go) covers both sequential entries
-- the `numTxs <= 2` shortcut inside `Run` and the wave-limit fallback -- and
fails on the old code with "recipient folded as value=[] delta=nil".

Note the blast radius: any block with 3+ transactions that exhausted
MaxWaves, and EVERY block of 1-2 transactions, lost its credit-only
recipients. Small blocks on the fleet are the empty ones (3 dirty accounts,
no transfers), which is why this surfaced as one bad block rather than a
constant drift. Evidence preserved in /data/blockchain/divergence-13659302/
(node0-leader, node1-follower).

Still open, separately: why that build fell back at all. 9064 aborts on
23000 transactions is the wave limit doing real work, and a fallback on the
leader's critical path also costs the block its parallelism.

## A quorum-committed block that no node stored -- fix CONFIRMED in a fleet round (2026-09-21, round 35zzzg -> 35zzzi, S23b/S26)

**STATUS (S26, 2026-09-21): fix prepared, not yet launched.** Root cause per
PART 1 of S26's own investigation (docs/QS_BLOCK_TIME_BUDGET.md 6cz):
under two-phase (deferred-execution) voting, `processProposal`'s Round 1
branch voted immediately, checking the extends-rule (`extendsJustify`)
only when the block happened to already be locally imported -- which is
essentially never true the instant a Proposal arrives, so the check was
skipped on the ordinary path, not just a rare race. Round 2
(`processPrepareQC`) had no extends check at all. Fix (no switch --
safety is not optional): both rounds now refuse a vote for a proposal
that does not extend its own JustifyQC block, once the block's real
parent is known (via the existing checked/imported bookkeeping; no wire
change). Leader side: `recordSealedOnParent` (the height-level
single-candidate guard) now runs at seal time, before push/propose,
instead of after the write completes, closing the window a second,
independently-sealed candidate on the same parent could slip through
while the first one's write was still in flight. Regression tests
`TestTwoPhasePrepareVoteRefusesNonExtendingProposal` and
`TestTwoPhaseCommitVoteRefusesNonExtendingProposal`
(internal/consensus/hotstuff/conflicting_commit_test.go) fail on
n42-r92's source and pass with the fix. Code commit `e49ce151`
("fix(hotstuff): refuse to vote for a block that does not extend the
committed head; drop stale seals before proposing"). Build: n42-r94 =
n42-r92's exact file set + this fix. Prepared round: 35zzzi
(run-r35zzzi.sh/chain-35zzzi.sh), with two new harness safety checks
(conflicting commits per height; a leg bench-run.sh itself refused to
measure), tested offline against 35zzzg (flags) and 35zzzf (clean).
Launch is the commander's call.

**STATUS (S31, 2026-09-21): refinement prepared, not yet launched.**
Round 35zzzi (6dg) confirmed the S26 fix holds in a full fleet round (0
conflicting heights across 3,355 checked) and measured its real cost
(Round1 60-72 -> 153-262ms; CommitQC(v) later than the leader's own
build end in 33% of win1 views, 78-83% of win2 views). S31 recovers the
cost without touching the rule: `extendsJustify` only ever reads a
block's parent hash, a header field never touched by
`CheckDeferredBlock`'s per-transaction check, so the two-phase Round 1
prepare vote now fires on a new `EventBlockHeaderKnown` (emitted by the
block-push path as soon as the header is peeked, before the full body
decode) instead of waiting for the deferred check. Round 2 is
unchanged; no header event falls back to the existing checked/imported
gate. Code commit `c124146e` ("perf(hotstuff): decide the prepare vote
on the block header, not the deferred check"). Full PART 0 proof (same
predicate as S26, timing only) and the binding argument (hashing the
header alone suffices) in docs/QS_BLOCK_TIME_BUDGET.md 6dh. Build:
n42-r95 = n42-r94's exact file set + this change. Prepared round:
35zzzk. Launch is the commander's call.

**SEVERITY (commander, 2026-09-21, from node5's raw log): this is a consensus SAFETY violation, not only
a liveness halt.** Both blocks were COMMITTED, one second apart, by honest nodes: `block committed!`
view 8784 hash `7a6d85...23259c` at 15:05:56 (votes 5/5 in both rounds) and `block committed!` view 8785
hash `f47f65...13d8ac` at 15:05:57 (votes 5/5 in both rounds) -- two different blocks at height 13661138
with the same parent (13661137, `5f35affe...`). The state did not fork only because no node could import
the second one; the chain halted instead. Two defects are needed for this and both are on the main line:
(1) the leader proposed, in view 8785, a block sealed on a parent that its own view-8784 block had
already superseded (a stale speculative build was pushed and proposed; the only check that caught it ran
at WRITE time, after the proposal had gathered its CommitQC -- `PROPOSE_BEFORE_WRITE`); (2) six followers
cast prepare AND commit votes for a block that does not extend the block they had just committed: the
deferred-vote path (`deferred check: block passes, vote may proceed before its import`) and the
extends-check that fails open (6ce: proposal.go, prepare 'gated on nothing beyond the Proposal message
itself') let a conflicting sibling through. A correct vote rule alone would have made (1) harmless.
Required before any further protocol-path change is adopted: a regression test that replays exactly this
(commit X at height h in view v; proposal Y at height h with X's parent in view v+1 -> no vote), and the
vote rule fixed to refuse it. Tracked as QS queue step S26.

Round 35zzzg, leg B2 (`N42_LEADER_WRITE_ASYNC=1`), 15:05:56-57 EDT, views
8784-8785. Node5 led a 4-view tenure (8782-8785) into which the last two
views both produced a block claiming **height 13661138**: view 8784
sealed and successfully wrote hash `7a6d85…23259c` (parent `13661137`);
a SECOND, separately-sealed candidate for the same height, hash
`f47f65…13d8ac` (`0xf47f6514cc`, parent `5f35af…3e51ab` = block
13661137's own hash -- i.e. also a direct child of 13661137, a genuine
sibling of `7a6d85…`), was independently pushed, proposed under view
8785, and **collected a full 5/5-vote CommitQC**. By the time node5's
own write of `f47f65…` ran, its local applied head had already advanced
to `7a6d85…` (13661138) via the OTHER candidate's earlier write, so the
authoritative write-path check (`checkQMDBLeaderSealParent`, unrelated
to and NOT bypassed by S23's relaxed pre-check) correctly rejected it:
`ErrStaleSeal`, `"sealed block is stale (applied head moved past its
parent)"`. Every one of the other six nodes received `f47f65…` via
direct push, cast a deferred (import-pending) vote for it
(`"deferred check: block passes, vote may proceed before its import",
number: 13661138`), then -- finding their own local head ALREADY at
`7a6d85…`/13661138 by the time `f47f65…` needed a home -- filed it as
`"add future block"` rather than importing it, since its parent slot
was already occupied by the sibling they had already applied. All seven
nodes then commit the QC (`"received Decide, committing block"`) but
can never execute it (`"hotstuff: committed block not executed
locally"`, `failures` climbing 1/2/3...), and `fetch-on-miss` retries
forever against peers that also only have the sibling. **A block with a
full, valid CommitQC exists in zero of the seven nodes' chains.**

Evidence: `/data/blockchain/wr-logs/r35zzzg-keep/node{0-6}-B.log`,
15:05:55-15:06:03 and again at restart 15:07:2x-15:19:3x (`"hotstuff:
persisted committed QC names a block this node does not have"`,
identical on all 7 nodes). Key lines (node5, chronological): `"hotstuff:
sealed block dropped — phase left WaitingForProposal" {block:
0x99e7eeba5f, view: 8785}` (a THIRD, later candidate for the same
tenure, correctly discarded by the engine's own phase check -- not
implicated further); `"🔨 Successfully sealed new block" {hash:
7a6d85…23259c, number: 13661138}`; `"hotstuff view timing: view=8785
role=leader ... votes=5/5"`; `"propose-before-write: the write failed
AFTER the Proposal left" {err: "sealed block 13661138 parent
5f35affe70bce8ac is no longer the applied head (13661138/
7a6d850bacab3b27)...", hash: 0xf47f6514cc}`. Follower lines (all six,
identical shape): `"block push: arrived"`, `"deferred check: block
passes, vote may proceed before its import"`, `"add future block"
{hash: f47f65…13d8ac, number: 13661138}`, `"received Decide, committing
block"`, `"hotstuff: committed block not executed locally"`.

Classification: **(B) -- reachable on r92 too, an open consensus-
liveness issue for the main line, not specific to S23's relaxed
pre-check.** The rejection that actually discarded the block ran
through `checkQMDBLeaderSealParent` under `bc.lock`, the ORIGINAL,
unrelaxed authoritative check -- S23's own bypass (`checkSealParentApplied`
accepting `asyncWriter.ExpectedParent()`) governs a different, earlier
gate and, even if it had taken the non-bypassed branch instead, the
same race (two views deciding two different blocks for the same
height) would have produced the same outcome. The actual bug is
upstream of the write path entirely: under fast, sub-second view churn
at a leg's teardown (views 8784/8785/8786 landing within ~7 seconds,
against this whole campaign's usual 650-900 ms full-block cycle),
`PUSH_BEFORE_WRITE`/`PROPOSE_BEFORE_WRITE` plus deferred (import-
pending) voting let TWO views' worth of proposals from the SAME
tenure-holding leader both collect quorum votes for the SAME height,
and nothing in the commit/import path reorgs a later-decided QC over an
already-applied sibling -- it just parks the QC'd block as unreachable
forever. Retiring `N42_LEADER_WRITE_ASYNC` (the commander's ruling on
S23) does NOT close this hazard.

Why it matters: a committed-but-unstored block halts the chain
permanently on restart -- the next leg (A2) inherited a parent it could
never fetch from anywhere and produced zero blocks for its entire
20-minute run, yet the harness's own run script printed `ROUND DONE`
over it (the analysis-side refusal, `"chain is not producing"` in
`bench-run.sh`, does not propagate to the runner's own terminal status
line). Undetected, this looks like an ordinary round completion in any
log grep that only checks for `ROUND DONE`.

What would detect it early: a harness check that every leg's decay
phase actually produces empty blocks (it already computes this --
`"decay done: N empty blocks produced"`, N=0 here -- the number already
exists in the log) and marks the ROUND `ABORTED` rather than `DONE`
when any leg reports 0. No fix to the consensus/import path is proposed
here.

**Retroactive fleet-wide check (S25 Job 4, 2026-09-21).** The harness's new
per-height, at-most-one-distinct-hash check was applied offline to every
kept round from today: `r35zzy`, `r35zzz`, `r35zzza`, `r35zzzb`, `r35zzzc`,
`r35zzzd`, `r35zzze`, `r35zzzf`, `r35zzzg`, `r35zzzh` --
`wt-r27/scripts/qs-analysis/height_conflict_check.py`, joining each
round's `"hotstuff: block committed"`/`"block committed!"` lines (hash +
view, no height) to a height via a hash-prefix table built from
`"block push: received"`/`"🔨 Successfully sealed new block"`/`"add future
block"` lines (all three carry both hash and height; the join key is the
first 6 hex characters of the hash, since different call sites truncate
hashes differently but always from the same prefix). **48,809 committed
heights checked fleet-wide across the 9 non-conflicting rounds plus
35zzzg; the ONLY conflict found anywhere is 35zzzg's own height
13661138** (hashes `7a6d85`/`f47f65`, views 8784/8785, exactly as
already documented above) -- zero conflicts in the other 9 rounds
(4,317-6,376 heights checked per round, 0 unresolved hash-to-height
joins in 9 of 10 rounds, 8 unresolved in 35zzzg from short-lived A2
retry noise, none affecting the conflict count). This is not proof the
defect is rare in general -- it is one incident in ten rounds' worth of
logs, at a leg-teardown timing this campaign has otherwise only produced
once -- but it is the full extent of what today's evidence shows: no
other kept round shows a second committed hash at any height.

**Confirmation round (S26, 2026-09-21, round 35zzzi, n42-r94 = n42-r92 +
`e49ce1512ff3cf17d5200d96b16fe191b4b8b09d`).** Full two-leg fleet round,
`GOMEMLIMIT=10GiB` throughout, docs/QS_BLOCK_TIME_BUDGET.md 6dg:
`height_conflict_check.py` re-run independently of the harness's own new
check finds **0 conflicting heights across 3,355 committed heights** --
matching the harness's own result. The fix's one new log line
(`"commit vote REFUSED: proposal does not extend its JustifyQC block"`,
`processPrepareQC`'s Round 2 guard) fired **0 times on any of the 7
nodes** -- the stale-sibling race did not recur this round. The fix's
OWN new leader-side guard (`"miner: suppressing divergent same-height
sibling"`, moved to run before push/propose per the fix) fired **exactly
once** (node6) -- the one genuine same-height-sibling collision this
round produced, caught before either candidate was ever proposed,
demonstrating the fix's own mechanism working as designed, not merely
absent because the race never recurred. The much larger, unrelated
`"sealed block is stale"`/`ErrStaleSeal` count (365-378 per node,
identical on every node) is the ordinary, pre-existing background rate
of a speculative build outracing real chain progress -- confirmed
harmless by the same 0-refusal count (none of these stale-seal events
was preceded by a proposal that had also collected votes). **The fix's
cost is real, not free**: Round1 rose from this lineage's own ~60-72 ms
baseline (n42-r92, round 35zzzf) to 153-262 ms, close to the ~180 ms the
fix's own design predicted (Round1 now waits on `CheckDeferredBlock`);
`CommitQC(v)` becomes the LATER event than the leader's own speculative
build (the vote round back on the critical path) in 78-83% of the
round's own second-window views, versus only 33% in the first window --
a real, measured throughput cost, concentrated in the leg's already-slow
second window, that should be tracked rather than assumed zero in future
comparisons against pre-S26 rounds. **Status: fix confirmed safe and
adopted; n42-r94 is the base binary from here forward regardless of the
measured cost, which is a tracked, not blocking, item.**

**2026-09-21, round 35zzzk (n42-r95, S31): 35 `import-gated vote REFUSED`
lines, all one incident (view 6557, all 7 nodes) -- an already-committed
block's own hash (view 6556) resurfaced one view later carrying a
self-referential JustifyQC (`justifyBlock == blockHash`); `extendsJustify`
correctly refused it, the view timed out and formed a TC (~5-6s, one view
only), 0 conflicting heights held. The refusal is correct, not a bug; the
upstream mechanism that let an already-committed hash resurface with a
self-referential justify one view later -- present in r95, not observed
in r94 -- is untraced and open (docs/QS_BLOCK_TIME_BUDGET.md 6dj).

**2026-09-23, S34: mechanism found, fix PREPARED, not yet launched.**
`internal/miner/worker.go`'s sibling-suppression path re-proposes the
FIRST block ever sealed on a parent without checking whether that
height has since been committed and written; in the incident, a
leftover seal-completion event re-injected node1's own ALREADY-
COMMITTED block (`a990bc..`) as a fresh candidate via `NotifyBlockSealed`
-> `onBlockReady`, which builds `Proposal.JustifyQC` from the leader's
own highest-known QC unconditionally -- the one guard that could have
caught the mismatch (`importedParents[blockHash]`-gated) fails open
because `importedParents` is populated only by `internal/sync`'s
import-notification call sites, never by a leader's own local write
path. The genuinely correct next block, already sealed and waiting,
was then ALSO silently dropped by the unrelated
"phase left WaitingForProposal" guard, because the stale proposal had
already advanced the view's phase before being refused by the voters.
Fix: `onBlockReady` now refuses UNCONDITIONALLY to propose a block
equal to its own JustifyQC.BlockHash, placed before any phase
mutation so a rejected stale proposal leaves the phase open for a
genuinely fresh seal to still succeed; `worker.go`'s sibling-suppression
path also checks the chain's own current head before re-proposing
(best-effort, not the safety net). Code commit `7288c8f0` ("fix(miner):
never re-propose a committed or already-proposed block; keep the fresh
seal"). Tests: `TestSealedBlockDroppedWhenItWouldJustifyItself`,
`TestFreshSealStillProposedAfterAStaleSelfJustifyAttempt` (the exact
incident end to end), `TestSealedBlockProposedWhenNotSelfJustify`
(`internal/consensus/hotstuff/proposal_self_justify_test.go`), all pass
including under `-race`; existing S26 regression tests unaffected. Full
mechanism/fix/timeline writeup: docs/QS_BLOCK_TIME_BUDGET.md 6dp;
docs/QS_HANDOVER_20260920.md "S34" section. Build: n42-r97 = n42-r96's
exact file set + this fix. Prepared round: 35zzzm (single configuration,
no A/B), queued behind 35zzzn. Launch is the commander's call.

**2026-09-23, round 35zzzm: fix CONFIRMED.** Guard fired 0 times (the exact incident did not recur this round); 0 `import-gated vote REFUSED`; 1 `sealed block dropped -- phase left WaitingForProposal` (vs 35zzzn's baseline 2), at 20:32:56, 6s into the round's own external memory-abort sequence and with 0 sibling-suppression lines preceding it -- classified as a shutdown artifact, not a sibling-suppression drop. 0/9,089 conflicting heights. Round ABORTED at 20:33:13 on an external memory spike (a foreign Rust/C++ coverage build's `cc1plus`, not the qs fleet -- see docs/QS_BLOCK_TIME_BUDGET.md 6dt), after both B1 and B2 had already completed their own win1/win2 measurement windows. **Status: n42-r97 adopted as the fleet base.**

## A follower re-decodes ~160k already-pool-resident transactions on every pushed block -- fix PREPARED, not yet launched (2026-09-22, S32)

**STATUS (S32, 2026-09-22): reuse fix prepared, not yet launched.**
6df/6di found a follower's block-push receive path
(`blockPushStreamHandler` -> `decodeChunkedBlock`) RLP-decodes every
transaction of a pushed block even though ~99.4% of them already sit
in the pool, decoded, with sender cached (the fleet's own measured
shape) -- 2.93 KB of the node's own 11.5 KB/transfer allocation.
`common/block/block_decode_reuse.go` (new) computes each transaction's
hash from its own raw RLP slice in the block body -- verified the
exact canonical hash preimage for all five transaction types
(docs/QS_BLOCK_TIME_BUDGET.md 6dk PART 0/1a) -- and asks the pool
before decoding, reusing the object on a hit for Legacy/AccessList/
DynamicFee only (Blob/SetCode always decode fresh, matching the
existing `hashFromEncoding()` cache-provenance boundary). Behind
`N42_BLOCK_DECODE_REUSE_POOL` (unset/"0" = today's decode exactly).
Offline proof (`BenchmarkDecodePushedBlock`, 160,000-tx block): B/op
falls 94.4% at the fleet's own 99.4% hit rate (130.8 -> 7.36 MB), ns/op
falls 47% (29.9 -> 15.8 ms) -- well past the task's own 40%-B/op /
no-regression bar. Code commit `f961f63e` ("perf(sync): reuse
pool-resident transactions when decoding a pushed block"). Full PART
0-4 writeup and prediction 95 in docs/QS_BLOCK_TIME_BUDGET.md 6dk;
mutability/aliasing findings in docs/QS_HANDOVER_20260920.md's own
"S32 PART 1" section. Build: n42-r96 = n42-r95's exact file set + this
change. Prepared round: 35zzzl (run-r35zzzl.sh/chain-35zzzl.sh), A/B
by leg on the new switch, GOMEMLIMIT fixed 10GiB every leg. Launch is
the commander's call.

**UPDATE (2026-09-22, S32 analysis): round 35zzzl ran; reuse measured
0% on every node in both switch-on legs (dec=full on every full
block, fleet-wide, no exceptions) -- prediction 95 is UNTESTED, not
falsified. Root cause is not the patch (every code path -- env parse,
`WithTxPool` gating, the PeekHeader wiring, the hash preimage, the
eligibility list -- re-checked correct against the live wiring): the
fleet's tx-gossip publish side has relayed zero messages fleet-wide
(the `"tx gossip: receiving"` canary never fires) since
`N42_TXPOOL_NOLOCALS=1` entered the baseline at round 35z3/35z4
(2026-09-08), because `NewLocalTxsEvent` -- the only event
`broadcastTxs` republishes -- is never sent for a `local=false`
(NOLOCALS-forced) submission; combined with `-shard-senders` routing
each sender to exactly one node's own RPC/pool, a follower's pool
structurally never holds another node's share of a block's
transactions. This has been true for every round run under this
baseline, not just this one. Full trace in
docs/QS_BLOCK_TIME_BUDGET.md 6dl. Safety stayed clean (0/4,495
conflicting heights, no BAD BLOCK, 0 refusals); n42-r96 is recommended
as the fleet base for S31's header-vote fix, with the reuse feature
left dormant (default off) pending either a propagation fix or a
re-scoped prediction.**

## Node shutdown closes the DB while an import is in flight -- logs a BAD BLOCK (2026-09-25, S46, round 35zzzx)

The campaign's first BAD BLOCK: block 13663008 arrived via push at the
same second the harness's own end-of-round SIGTERM began tearing the
node down; the import raced an already-closing MDBX handle
(`ProcessParallel: prefetch pending credits: ... db closed`),
"context canceled" fleet-wide for the same block on every node that
touched it. All seven nodes' own last successfully-committed block
(13663007) is identical; `height_conflict_check.py` found 0
conflicting heights. Not a validation defect and not related to the
round's own pool-overflow pacing question -- 13663008 was never
voted on or committed anywhere. Full trace: docs/QS_BLOCK_TIME_BUDGET.md
6e8. **The harness's end-of-round check should ignore BAD BLOCK lines
that occur after a leg's own SIGTERM, so a shutdown race does not
read as a live-round safety failure.**
