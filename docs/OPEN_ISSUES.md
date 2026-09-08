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

## Plain `Account` table frozen at 13,750,514 on the qs fleet (2026-09-06, round 26)

`N42_STATE_WRITE_QMDB_ONLY=1` stopped plain Account writes; QMDBMeta records
`accountFrozenAt`. Every later start of those datadirs needs
`N42_STATE_READ_QMDB=1`, or the miner, txpool and RPC read stale accounts.
No repair tool rebuilds the table from the tree yet.
