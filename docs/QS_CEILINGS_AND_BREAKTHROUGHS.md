# Ceilings and breakthroughs, layer by layer (commander, 2026-09-26)

Scope: the seven-node qs fleet on this box (137 GB, ~208 cores, seven nodes at GOMEMLIMIT 10 GiB, one MDBX
writer per node, 163,000-transfer blocks). Every number below is measured in QS_BLOCK_TIME_BUDGET.md (section
ids in brackets). Same box, n42-rs: 0.577 s per full block, 98% occupancy, 280k tx/s (6f0).

Scored today: B 134.7k (35zzzad). Engine ceiling today: 185-194k tx/s at tenure 8 (6e2/6f2). Why the two
differ: blocks are half full (supply ~135-140k/s delivered) and full-block builds take 1.2-2.0 s under load
(6f5). Why the engine ceiling is where it is: the leader must EXECUTE block N and commit its state root before it
can propose N+1 (the header of N+1 carries the execution of N: the deferred-execution rule of 6bg/6bh). The
cycle is therefore never shorter than pick + exec + root + push on one node: 61 + 340 + 147 + 18 ~= 570-650 ms.

## L1 Supply and ingest (generators -> RPC -> pool)

Ceiling now: ~16.8k tx/s per generator connection = 200 tx / (10 ms serial decode + recovery + 3 ms pool add)
(6f2, S53); eight connections ~135-140k/s; offering more slows the nodes (S51, S54, three rounds).
Breakthrough: (a) decode and recover a batch on a worker pool (S53, n42-r102, being re-measured in 35zzzai);
(b) stop recovering at ingest at all: the hint-only path already ships the sender with the transaction; accept
the claimed sender into the pool and verify the signature once, in parallel, inside the deferred check or the
executor (which recovers anyway) -- moves ~50 us/tx off the RPC thread entirely; (c) a binary batch endpoint
(RLP list, no JSON) for the generators. Expected: per connection 16.8k -> 60k+; the eight generators would
then deliver up to their own signing rate (~33k/s each, 6e4). Risk: (b) changes what "in the pool" promises;
a wrong hint must be caught before the vote (the deferred check already recovers senders: 6dx).

## L2 Mempool and memory (the second-window collapse)

Ceiling now: live heap 5.8-7.5 GB against 10 GiB; GC 20% of CPU in win1 -> 54% in win2 (6da/6db); the
per-block cost of a full block rises from ~0.65 s to 1.2-2.0 s as the heap fills (6f5); the box cannot give
more memory (6di). Owners (6dm): decoded transaction objects 2.16 GB (600k pool x ~3.6 KB), tail index 1.13,
QMDB map index 0.78, sender cache 0.31 (was 1.15).
Breakthrough: (a) keep the pool as raw RLP bytes + a 40-byte index entry per transaction and decode at fill
time on the 32 fill workers (or reuse the block's own decode on followers): -1.5 GB live, and the follower's
"decode 42 ms" becomes the pool's; (b) tail index behind a knob (keepBlocks 64 -> 8): -0.9 GB; (c) QMDB index on
MDBX (exists, unwired): -0.7 GB; (d) with steady pacing (rate 16000-20000) the pool no longer needs 600k slots:
300k (-1 GB) -- 35zzzp failed only because the generators were bursty then. Expected: live heap ~3 GB, GC back
to ~20% in win2, full-block builds ~0.65 s throughout -> win2 = win1, +10-15k on the B mean alone.

## L3 Consensus: pipeline depth (the structural ceiling)

Ceiling now: depth-1 deferral. Leader of N+1 needs root(N) => exec(N)+root(N) sit inside every cycle. Tenure 8
removed the hand-over tax (16.5% -> 0%, 6f2); votes are off the path (Round1 60, Round2 115 ms, 6dj/6dt).
Breakthrough: depth-2 deferral -- the header of N+1 carries the execution of N-1, not N. Then the leader
proposes N+1 as soon as N is ordered (pick ~60 + push ~20 + PrepareQC ~60 = ~150-250 ms) while exec(N) runs
behind; execution becomes a pipeline stage that must merely keep up: exec + finalize + write per node ~490-540
ms (6cw/6dx) => ~300-330k tx/s, i.e. the n42-rs shape (their 0.577 s cycle IS their execution time). The
includability check (nonce, balance, gas) runs against the state of N-1 plus the pending deltas of N -- the
deferred check already models same-block credits (6dx). Followers vote on the header (S31) and import behind.
Expected: cycle 650 -> ~300 ms; throughput bounded by execution ~300k. Risk: protocol change shared with
n42-rs (the joint header rule); a two-block-deep reorg window for includability; must keep S26/S34's guards.
This is the one change that can double the score.

## L4 Leader build (pick, exec, root)

Ceiling now: 611-650 ms = parallel exec 332-347 (32 workers, 2.1 us/tx) + assemble/root 145-150 + pick 61 +
~80 unplaced (6cw); 1.2-2.0 s under GC pressure (6f5). Breakthrough: under L3 this leaves the cycle; on its own,
(a) the root computation can start per completed wave instead of after the fill (streaming QMDB append), (b)
pick from a pre-sorted per-account queue (pool keeps the ready list) instead of snapshot+sort. Expected: -100
to -150 ms without L3; irrelevant with L3.

## L5 State commitment (QMDB)

Ceiling now: root 147-150 ms on the leader, finalize 126 ms on followers (6cw); n42-rs 39-95 ms (6f0); the
isolated speculative tree (bc.minerRC) only admits the node's own appends, so no node can build on a foreign
unwritten parent (6du). Breakthrough: incremental root over the fill's waves; a minerRC that can adopt a foreign
candidate by replaying its mutation log (undo bookkeeping) -- needed for hand-over speculation only if L3 is
not done. Expected: -50 to -100 ms per block.

## L6 Persistence (one MDBX writer)

Ceiling now: block write 195-215 ms serial inside InsertChain on followers (6dx), 349 ms on the leader, journal
contention solved by scheduling (S19). Breakthrough: batched, off-path persistence -- write block N while
importing N+1, commit every k blocks (n42-rs ~120 ms amortised); the earlier async-write attempt (S23) failed on
a sibling-race guard, since fixed by S26/S34 (the authoritative check is at write time). Expected: follower
import 790 -> ~600 ms; with L3 this raises the execution-stage ceiling from ~300k toward ~360k.

## L7 Follower import (decode, check, exec, finalize, write)

Ceiling now: decode 42 + check 228 (now concurrent, S42) + InsertChain 792 (6dx). Breakthrough: (a) compact
blocks -- if every node held every transaction (ingest-all, as n42-rs: each generator to every node, or gossip)
the push would carry 32-byte hashes (5 MB instead of 26 MB) and followers would skip decode and recovery
(S32's reuse becomes real: today pools hold 1/7, 6dl); (b) execute-before-commit: start exec(N) on arrival,
before the vote, since exec is the pipeline stage under L3. Expected: -40 to -70 ms per block, and one live
copy of each transaction instead of two.

## L8 Network

Not a ceiling: the body reaches the quorum follower in 44 ms (6ce); the gossip fallback is off (S15b). Compact
blocks (L7a) would cut bytes 5x and remove the last decode.

## L0 The score and the harness

The B mean averages two 60-s windows per leg; win2 has always carried the memory collapse (L2). The engine
ceiling (txs per full block / full-block cycle) is now reported beside the score so node progress is visible
even when supply or memory caps the score.

## Order

1. S53r (35zzzai): does parallel ingest lift delivered supply past 140k? (running)
2. L2a raw-bytes pool + L2d pool 300k: the second window (config + one code change; low risk).
3. L3 depth-2 deferral: the doubling; needs a joint header rule with n42-rs -- spec first, prototype behind a
   fork-time switch like N42_DEFERRED_EXECUTION_TIME, safety tests (S26/S34 replays) before any round.
4. L6 batched persistence (after L3 makes execution the stage that matters).
5. L7a ingest-all + compact blocks (halves the follower's remaining per-block work; changes the supply path).
