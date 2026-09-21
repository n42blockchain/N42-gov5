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

## A quorum-committed block that no node stored -- fix prepared (2026-09-21, round 35zzzg, S23b/S26)

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
