# QS campaign handover, 2026-09-20 15:45 EDT

Supersedes QS_HANDOVER_20260912.md. Written to be the only thing a fresh
session needs to read. Do not read the old transcript.

## State in one paragraph

The goal is unchanged: raise the seven-node "qs" fleet's TPS (Go client
gov5, HotStuff-2, QMDB BLAKE3 binary twig forest, MDBX). The scored metric
is the **B mean**, and the standing best is **127.6k** (round 35zzt, binary
n42-r80 = deferred execution + the fold outside the write transaction +
the transaction-bounded tail + packet window 8). Today fixed a real
state-corruption bug, closed two false leads, and ran one round that
falsified its prediction. Nothing is in flight on the box right now: the
box is with **n42-rs** (claim taken 15:25 EDT), our fleet is down, and
round 35zzw is queued behind them, waiting.

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

## The queue

| id | step | binary | prediction | status |
|----|------|--------|-----------|--------|
| S2 | 35zzw: per-block base-read cache (`parallel.BaseCache`) | n42-r85 | 78 (6br) | **queued on the box, chain script waiting behind n42-rs** |
| S7 | sixteen generators with `-target-depth` halved (45000 -> 22500) | n42-r85 or later | 81 (6bv) | **specced and ruled: runs after S2** |
| S4 | the Prague delegation check reads every recipient (6bp) | not built | not written | candidate |
| S5 | the leader's write (~0.5 s) off the critical path | not built | not written | candidate |
| S6 | per-transaction allocation hotspots | not built | not written | candidate |

S1 (sixteen generators) and S3/S3b (the fallback and the reader
disagreement) are closed; see above.

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
parallelism -- one variable against 35zzt. Run it after S2, so the code
lever already on the box is read out first.

35zzw uses the **eight-generator baseline shape**, so it is directly
comparable to 35zzt's 127.6k and was not contaminated by S1's bad shape.
Let it run.

## Binaries

Built from a detached worktree at f7ec2836 with individual files checked
out from origin/main (see build-and-queue.sh; `git checkout origin/main --
<file>` picks up everything that has landed in those files).

    n42-r80 = deferred + fold + tail + packet window     <- the 127.6k baseline
    n42-r84 = r80 + the delta fix                        <- 35zzx attempt 2
    n42-r85 = r84 + the base-read cache                  <- 35zzw

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
2. Check the box: `ls -l /data/blockchain/.box-claim-*` and whether
   `chain-35zz*.sh` is still waiting. If 35zzw has finished, dispatch its
   analysis agent with a brief modelled on agent-tasks/S1-analyse-35zzx.md,
   scored against 127.6k and against prediction 78.
3. Collect the S7 report (prediction 81) and decide whether the supply round
   runs before or after the next code lever.
4. Arm the monitor before going quiet. Never poll.

## S7 runner prepared (2026-09-20, America/New_York time)

Scripts: `/data/blockchain/gov5-work/run-r35zzy.sh` and `/data/blockchain/gov5-work/chain-35zzy.sh`.

One-line diff: `export QS_FLOOD_EXTRA="-target-depth 22500 -depth-by-nonce -lazy-sign"` (35zzx's 45000 -> 22500; aggregate in-flight back to 360,000 as in 35zzt).

Not launched; waits for 35zzw to end and the box claim.
