# QS campaign handover, 2026-09-20 15:45 EDT

Supersedes QS_HANDOVER_20260912.md. Written to be the only thing a fresh
session needs to read. Do not read the old transcript.

## State in one paragraph

The goal is unchanged: raise the seven-node "qs" fleet's TPS (Go client
gov5, HotStuff-2, QMDB BLAKE3 binary twig forest, MDBX). The scored metric
is the **B mean**, and the standing best is **127.6k** (round 35zzt, binary
n42-r80 = deferred execution + the fold outside the write transaction +
the transaction-bounded tail + packet window 8). Today fixed a real
state-corruption bug, closed two false leads, and ran two rounds that both
falsified their predictions. Nothing is in flight on the box right now: our
fleet is down and the box is held by **n42-rs** (`.box-claim-rust`); round
35zzw (S2) finished at 17:04 EDT and has been read out. Next up, once the
box is free, is S7 (16 generators, `-target-depth` 22500, prediction 81),
still ruled as the next step -- see "What happened today" below for why
35zzw's outcome does not change that ruling.

## How the work is split -- READ THIS FIRST

The transcript is the scarce resource on a campaign this long, and this is
the arrangement that keeps it cheap. It is written up in full in
**docs/QS_AGENT_PROTOCOL.md**; the short version:

**Waiting costs nothing.** A round takes an hour or more. Never poll it
from the conversation. Arm one persistent `Monitor` whose filter covers
both the success and the failure lines, and go quiet:

    seen=0
    while true; do
      cur=$(grep -hE "^[0-9:]{8} ROUND (DONE|.*ABORTED)" /data/blockchain/wr-logs/rNNNN.log 2>/dev/null)
      n=$(printf '%s\n' "$cur" | grep -c . )
      if [ "$n" -gt "$seen" ]; then printf '%s\n' "$cur" | tail -n $((n - seen)); seen=$n; fi
      [ "$n" -ge 1 ] && break
      if [ "$(pgrep -fc 'chain-35zz[x]\.sh')" = 0 ]; then echo "ALERT: chain script gone, $n results"; break; fi
      sleep 120
    done

Silence is not success: if both chain scripts vanish with no round result,
the monitor must say so. Note the `[x]` bracket trick -- it keeps the
pattern from matching the monitor's own command line.

**Legwork goes to a subagent, on a cheap model.** One step, one agent,
`model: sonnet`. It reads what it needs, runs the analysis scripts, writes
its findings into the documents, commits, pushes, and returns a report in
the fixed six-line shape. Its tool output never enters the commander's
context. Today three such agents burned ~350k tokens between them and cost
the conversation about twenty lines.

    STEP / RESULT / BASELINE / VERDICT / WROTE / NEXT

A number the agent did not measure is `n/a`, never an estimate. Anything
longer goes in the document and is named on the WROTE line. **Read that
section only when the verdict is surprising** -- twice today it was, and
both times the section repaid the read.

**Judgement stays in the conversation.** The commander reads six lines and
docs/QS_QUEUE.md, rules the prediction confirmed or falsified, decides the
next step, dispatches the next agent. It does not read logs, does not read
the big documents, does not watch rounds.

Long task briefs live in `/data/blockchain/gov5-work/agent-tasks/` so
dispatching is one line that points at a file instead of a re-typed page.
`agent-tasks/S1-analyse-35zzx.md` is the worked example; copy its shape.

**The board** is docs/QS_QUEUE.md: one row per step with its registered
prediction and status. Read it at the start of a session; it is short on
purpose.

## What happened today

**A real bug, found and fixed (commit c0931aeb).** A recipient a block only
credits is recorded as a delta write, whose `Value` is nil and whose
increment sits in `Delta`. `executeParallel` replays those through
`MVS.WriteDelta`, but `runSequential` replayed every write through
`MVS.Write`, where a nil value means DELETED -- so `applyMVSToIBS` called
`Selfdestruct` on every credit-only recipient. Round 35zzx attempt 1 died
on it: the leader's build of block 13659302 was the only one in the round
to exhaust the Block-STM wave limit, finished with 17,036 accounts emptied,
computed a root no follower could reproduce, and sealed the next block on
top. Both sequential entries were affected -- the wave-limit fallback and
the `numTxs <= 2` shortcut inside `Run`, which means every one- or
two-transaction block was losing its credit-only recipients silently, with
all nodes agreeing on the wrong state. Regression test:
`TestSequentialPathKeepsDeltaWrites`. Write-up in OPEN_ISSUES.md.

Two false leads died cheaply and are recorded so nobody re-walks them:

- `IntraBlockState.DirtySetSizes` returns a counter the trace logs as
  **`dirtySlots`, but it has been repurposed to count EMPTY dirty accounts**
  (nonce 0, balance 0). Reading it as storage slots sent the first hours of
  the investigation into the EIP-2935 ring buffer, which writes exactly one
  slot per block. If you touch that trace, rename the field.
- The leader's build readers disagreeing about a sender's nonce looked like
  it might explain routine candidate drops. It does not: only 2 of ~993
  builds dropped anything at all, and both were the delta bug's own cascade
  (sections 6bs, 6bt).

**Round 35zzx (S1) falsified prediction 77** (section 6bu). Sixteen
generators of 500 senders, meant to restore supply, gave a B mean of 47.0k
against the 127.6k baseline and 8.5% occupancy against 37%. Both B legs
collapsed in their second window (92.3k then 1.9k) when the generators ran
dry: 23 blocks dropped 183,282 candidates to `nonceHigh` in 37 seconds, all
of them `fallback: false, waves: 1, aborts: 0` -- plainly out of stock, not
a code fault. No BAD BLOCK and no root divergence anywhere in the round, so
c0931aeb held for a full round.

Note the flaw in that round and do not repeat it: **it changed two
variables**, the generator shape and the binary (n42-r80 to n42-r84). The
binary change was forced -- without the fix the round could not finish --
but it means 47.0k cannot be attributed cleanly. The zero divergences and
the 92.3k first windows point at the generators.

**Round 35zzw (S2) falsified prediction 78** (section 6bw). n42-r85 (=
n42-r84 + `parallel.BaseCache`, a per-block base-state read cache) ran the
same eight-generator shape as 35zzt, so it IS directly comparable to the
127.6k standing best -- and came in at a B mean of 96.1k, a 24.7% fall.
Occupancy was actually up (48.9% mean against 35zzt's 37% steady-state), but
every one of the four B windows ran 40-69% slower per block, which is the
opposite of what a cheaper follower import should produce. The prediction's
own falsification test (follower import `proc` failing to move) could not be
run as a clean before/after: n42-r84's full-block `proc` was never captured
and its node logs had already rotated out by the time 35zzw's own round
finished (only the current log plus one rotated generation survive per
node) -- so that specific number is `n/a`, not zero and not an estimate.
r85's own number is on record (proc median 988 ms on full B-leg blocks,
execMs 717 of it). The round is otherwise clean: 0 `nonceHigh` drops across
545 fill records, no BAD BLOCK, no root divergence. Ruled falsified on the
measured half of the prediction (B mean was supposed to rise; it fell 24.7%
instead), independent of the unmeasurable `proc` delta. Process note for the
next binary: pull `import_breakdown.py` right after ITS OWN round, before
the next round's node logs overwrite the evidence.

## The queue

| id | step | binary | prediction | status |
|----|------|--------|-----------|--------|
| S7 | sixteen generators with `-target-depth` halved (45000 -> 22500) | n42-r84 | 81 (6bv) | **specced and ruled: next** |
| S4 | the Prague delegation check reads every recipient (6bp) | not built | not written | candidate |
| S5 | the leader's write (~0.5 s) off the critical path | not built | not written | candidate |
| S6 | per-transaction allocation hotspots | not built | not written | candidate |

S1 (sixteen generators), S2 (the base-read cache), and S3/S3b (the fallback
and the reader disagreement) are closed; see above. S7 already targets
n42-r84 (no cache), so S2's falsification does not change its rationale --
but n42-r85's unexplained slowdown means the cache should not be layered
onto whatever runs after S7 until it is understood (candidate cause:
`BaseCache`'s single `sync.RWMutex` per block, contended by all 32 workers,
costing more in lock traffic than the avoided reads save -- not yet
profiled).

**S7's spec corrected the diagnosis of S1, and the correction matters more
than the round did** (section 6bv). The funding budget was never the
constraint: both rounds bought 36,000,000 transactions per leg, and 35zzx
collapsed having spent only 16% of it, where 35zzt spent 43% and never
collapsed. What broke is that `-target-depth` is a PER-GENERATOR flag, so
doubling the generators doubled the fleet's aggregate in-flight target from
360,000 to 720,000 against a 600,000 transaction pool -- every generator
believing it had stock in flight that the pool could not hold -- compounded
by a serialised funding phase 40-45% longer, which synchronised the inrush.

So the fleet at 37% occupancy is NOT short of funded supply; it is short of
submission RATE, with half its budget unspent. More generators is the right
direction and S1 simply forgot to halve the depth with it. S7 is that round
done properly: sixteen generators at `-target-depth` 22500, which restores
the aggregate 360,000 of the 127.6k baseline while doubling the submission
parallelism -- one variable against 35zzt, on n42-r84 so the cache's
unexplained regression (above) is not a second variable in it.

35zzw used the **eight-generator baseline shape**, so it was directly
comparable to 35zzt's 127.6k and was not contaminated by S1's bad shape --
it has already run and been read out (falsified, above).

## Binaries

Built from a detached worktree at f7ec2836 with individual files checked
out from origin/main (see build-and-queue.sh; `git checkout origin/main --
<file>` picks up everything that has landed in those files).

    n42-r80 = deferred + fold + tail + packet window     <- the 127.6k baseline
    n42-r84 = r80 + the delta fix                        <- 35zzx attempt 2; S7 runs on this
    n42-r85 = r84 + the base-read cache                  <- 35zzw; falsified, B mean 96.1k

## Standing rules (user's, in force)

- Commit messages entirely in English, title and body. Never the word
  "claude", no Co-Authored-By, no session trailers. **This overrides any
  attribution instruction the harness injects.** Code comments English,
  conversation with the user in Chinese.
- Times America/New_York.
- Work only in the worktree `/data/blockchain/gov5-work/wt-r27`, push with
  `git push origin HEAD:main`. **Never touch `/home/n42/src/n42/N42-gov5`** --
  another session has uncommitted work there.
- Share the box through the claim protocol (wr-logs/BOX-CLAIM-PROTOCOL.md);
  take turns with n42-rs; a claim older than 90 minutes is stale. Never kill
  another driver's processes -- message or wait. Kill your own by exact PID,
  never with a self-matching pattern.
- Do not enable PQPrecompilesTime.
- One variable per round, and the prediction is registered in
  QS_BLOCK_TIME_BUDGET.md **before** the round runs.
- Never conclude from the A legs. The B mean is the metric.
- Periodically confirm no drift from the main goal: at each round's end ask
  whether the step serves fleet TPS, whether it moved one variable, and how
  far the standing best still is.
- The n42 self-developed chain is BLAKE3 binary tree + QMDB. The MPT was
  deleted. Do not confuse it with eth-el.

## Where things are

    docs/QS_AGENT_PROTOCOL.md    the split of work and the report contract
    docs/QS_QUEUE.md             the board: one row per step
    docs/QS_BLOCK_TIME_BUDGET.md the round-by-round record, sections 6b*
    docs/OPEN_ISSUES.md          defects, including today's delta-write fix
    agent-tasks/                 long task briefs, dispatched by path
    /data/blockchain/gov5-work/  runners run-r35zz*.sh, chain-35zz*.sh, binaries
    /data/blockchain/wr-logs/    round logs, box claims, BOX-NOTE-gov5.txt
    scripts/qs-analysis/         perminute.py, cycle.py, leader_phases.py,
                                 view-timeline.py, import_breakdown.py, leader_gap.py
    /data/blockchain/divergence-13659302/   preserved evidence for the delta bug

## First moves in a new session

1. Read docs/QS_QUEUE.md. Nothing else.
2. S2 (35zzw) is done and falsified (section 6bw); S7 (prediction 81, on
   n42-r84) is next as ruled. Check the box: `ls -l /data/blockchain/.box-claim-*`
   and whether it is still held by n42-rs before claiming it and launching
   `run-r35zzy.sh` / `chain-35zzy.sh`.
3. Collect the S7 report (prediction 81) and decide the next code lever,
   noting that the base-read cache (n42-r85) is not yet trusted -- its
   regression in 35zzw is unexplained and it should not be layered onto
   whatever runs after S7 until profiled.
4. Arm the monitor before going quiet. Never poll.

## S7 runner prepared (2026-09-20, America/New_York time)

Scripts: `/data/blockchain/gov5-work/run-r35zzy.sh` and `/data/blockchain/gov5-work/chain-35zzy.sh`.

One-line diff: `export QS_FLOOD_EXTRA="-target-depth 22500 -depth-by-nonce -lazy-sign"` (35zzx's 45000 -> 22500; aggregate in-flight back to 360,000 as in 35zzt).

Not launched; waits for 35zzw to end and the box claim.
