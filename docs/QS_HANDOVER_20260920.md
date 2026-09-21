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

## Two rulings from 35zzw that bind the next session

**Do not carry n42-r85's base-read cache into any later binary.** Round
35zzw (section 6bw) came in at a B mean of 96.1k against the 127.6k
standing best, a 24.7% fall, and the shape of the fall is specific:
occupancy ROSE (48.9% against 42.75%) while block time rose much harder
(1.645 s against 1.086 s), with `execMs` 717 ms of a 988 ms follower
`proc`. Fuller blocks sealing half again as slowly is the opposite of a
cheaper import. Later levers branch from **n42-r84**, not r85.

The commander's hypothesis for the next session to test, cheaply, before
anything else is tried with this cache: `parallel.BaseCache` guards a
single map with one `sync.RWMutex`, and it is consulted on the base
fallback of every account read. With `PARALLEL_EVM x32` and ~23,000
transactions a block, that is a global lock inserted into the hottest read
path in the executor, and a lock convoy there would produce exactly this
signature -- more work admitted per block, each block taking longer. A CPU
profile of a follower during a B leg settles it; if it is the mutex, the
fix is sharding the map by address prefix the way `MVS` already shards,
not abandoning the idea. Do not re-run the round before profiling it.

**A harness defect worth fixing first: the round's own before/after is not
recoverable.** Prediction 78's literal criterion was the follower's import
`proc` time, and it could not be executed, because node logs retain only
the current file plus one rotated generation and no earlier round in this
lineage ever ran `import_breakdown.py` -- every prior readout used
`cycle.py`'s seal-to-seal timings instead. The agent correctly reported
`n/a` rather than inventing a baseline, but a campaign that registers
phase-level predictions must capture phase-level numbers every round.
**From now on, every round's analysis agent records the
`import_breakdown.py` line (body / proc / write / total, plus the proc
breakdown) into its section, whether or not the round's prediction asks
for it.** Then the next round always has its baseline. This is also why
prediction 78 was written badly: it bundled a mechanism claim (`proc`
down) with a throughput claim, so half of it could not be ruled on. Keep
those separate in future predictions.

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

## Duplicate serialization audit (2026-09-20)

Read-only static audit, done off-box while n42-rs held the fleet (grep/read
only, no build/bench/test run). Trigger: n42-rs found their vote path
decodes the block, re-encodes 163k transactions into a NEW_PAYLOAD frame,
pushes 26 MB over a local socket, and the execution layer parses it again
-- 107 ms, 29% of the block cycle. Question: does gov5, a single process
with no engine-API socket, have the same class of waste on its in-process
equivalents (proposal decode, push/gossip re-encode, tx-root re-derivation,
sender-hash re-encoding, size/logging encodes, deep copies) for a ~163k-tx,
~26 MB block?

Headline: **falsified for the specific n42-rs mechanism.** The HotStuff
`Proposal` wire message never carries the block or its transactions at all
-- `encodeProposal`/`decodeProposal` (`internal/consensus/hotstuff/codec.go:175-224`)
put only `BlockHash` and `TxRootHash` (32+32 bytes) on the vote-path
message; the 26 MB body travels on a separate channel (direct P2P push +
gossip fallback) that the vote never touches. So there is no analogue of
"decode block -> re-encode 163k txs into the consensus message -> push ->
decode again" on the vote path itself, by construction. Most of the
individual duplications that *would* recreate the same class of cost on the
push/import path were already found and removed in `6445f1bf` (n42-r74,
documented in `docs/QS_BLOCK_TIME_BUDGET.md` around line 4367) and confirmed
still in place by this reread of the current source. One genuine
still-open item was found (#1 below): every node still fully re-serializes
all 163k transactions a second time, in a different byte layout, on the
storage write.

| # | Path | file:line chain | What happens N times per block per node | Status | Existing measurement | Smallest fix |
|---|------|------------------|------------------------------------------|--------|----------------------|---------------|
| 1 | write (both) | `modules/rawdb/accessors_chain.go:374-385` `encodeTxForStorage` (compact `tx_compact.go:202-234` or `EncodeEthereumTransaction` `common/transaction/ethereum_rlp.go:229-...`), called from `WriteTransactions`/`encodeTxsParallel` at `accessors_chain.go:394-463` | Every tx is serialized a 2nd time (wire decode -> struct fields -> per-tx storage record), even though the exact wire RLP bytes are already cached on the tx (`tx.enc`, see row 5). Storage needs a different byte layout (keyed compact record, not a block-level RLP list), so this is not the identical-bytes-for-no-reason case n42-rs found, but it is still a full second pass over 163k txs on every node's write. | confirmed | yes -- round 35zy ("block" phase 118 ms of a 213 ms write) and round 35zzb (parallel encode, ~half via `parallelTxEncodeMin`/`encodeTxsParallel`, doc lines ~3706-3739) | Let `MarshalCompactStorage` reuse `tx.EthEncoded()` for the legacy-shaped fields it already stores verbatim (nonce/gas/to/data/sign), instead of re-copying them field-by-field from `tx.inner`; would not remove the pass but could cut per-tx allocation |
| 2 | follower gossip vs. push race | `internal/sync/rpc_block_push.go:25-51` (`ReadChunkedBlock` decode at line 28, `pushInflight.Store` only at line 49) vs. `internal/sync/validate_blocks.go:47-61` (`peekBlockHeader` + `pushInflight.Load` check) | If gossip's header-peek runs in the window between the push handler starting its full decode (line 28) and marking the hash busy (line 49), the gossip path does not see "busy" yet and may fall through to its own full RLP decode of the same 163k-tx block (`validate_blocks.go:74-77`) before the push's `HasBlock`/`InsertChain` result is visible. | suspected (race window, not traced to a live occurrence) | n/a | Store the hash in `pushInflight` from the decoded header before/while reading the body, not after, or peek the header on the push side too and register it before the full chunked read completes |
| 3 | leader push (all peers) + gossip fallback | `internal/blockchain.go:1700-1733` `SealedBlock` (`rlp.EncodeToBytes` once, comment explicitly states the reuse) -> `directPushBlock` (`internal/blockchain.go:1738-1774`, per-peer loop reuses the same `data` slice, only re-wraps it in the SSZ chunk framing via `rawBlockBytes`) | N/A -- single encode, shared bytes, across every peer and the gossip goroutine | not a problem (already fixed) | doc line ~3706 area / commit `6445f1bf` list ("the leader's per-push re-encode... removes") | none needed |
| 4 | follower commit-to-canonical | `internal/blockchain.go:1358-1382` `CommitToCanonicalWith` reads `bc.blockCache` first; populated at `internal/blockchain_write.go:159-173` inside `writeBlockWithState`'s deferred hook | N/A -- the imported/sealed `*block.Block` instance (every tx hash memoised) is reused instead of `rawdb.ReadBlockByHash` decoding 163k txs from MDBX again | not a problem (already fixed) | round 35zg / 35zzm cited inline in the comment (80 ms decode + ~200 ms re-hash avoided) | none needed |
| 5 | decode -> tx hash / tx root | `common/transaction/ethereum_rlp.go:100-107` (`DecodeEthereumTransaction` caches the exact wire bytes into `tx.enc`) feeding `common/transaction/transaction.go:439-451` (`EthEncoded`) and `:575-593` (`Hash()`, keccak of the cached encoding) and `common/block/block.go:157-181` (`Block.EncodeRLP` calls `tx.EthEncoded()` per tx, no re-encode) | N/A -- one encode (the original wire bytes) serves the tx hash, the tx root leaf, and any re-encode (RLP push) of the block | not a problem (already fixed) | doc: "half of every tx hash (keccak of the cached encoding)" in the `6445f1bf` list; isolation bench "70 -> 6 ms" for the Blake3 switch | none needed |
| 6 | sender recovery: deferred check + import | `internal/deferred_includable.go:120-247` (`deferredTxPlan`, calls `transaction.Sender` per tx) runs before `internal/sync/rpc_block_push.go:52` / `subscriber_blocks.go:58` `deferredCheck(blk)`, then the normal import's own sender recovery (`internal/sender_recovery.go`) walks the same `blk.Transactions()` slice | Per-object memo (`common/transaction/transaction_signing.go:218-247`, `tx.from` field) plus a process-wide two-way cache keyed by tx hash mean the import's pass is a lookup, not a second secp256k1 recovery, for txs the deferred check already touched (same tx pointers, same slice) | not a problem for the common case; ~9% still misses under cache pressure per doc | doc: "~9% of senders recovered twice (two-way cache)" fix already landed in `6445f1bf`; no new measurement here | n/a (already the documented residual, not newly found) |

Not a problem -- checked and cleared:

- Consensus vote message (`Proposal`) never carries the block or tx list (`internal/consensus/hotstuff/codec.go:175-224`) -- the entire n42-rs mechanism (decode -> re-encode into the vote frame -> push -> decode again) has no analogue here by construction.
- Header hash is cached on the struct (`common/block/header.go:126-133`, `atomic.Value`) and the cache is set once per decoded instance; no evidence of repeated `rlpHash()` calls across validate/import/write for the same instance.
- Tx root (`TxRootAt`) is computed exactly once per node per block: once by the leader at block assembly (`internal/miner/worker.go:2302`, `NewBlockFromReceipt` -> `common/block/block.go:222`) and once by each follower in `ValidateBody` (`internal/block_validator.go:98`, called from exactly one site, `internal/blockchain_insert.go:104`). `writeBlockWithState`/`state_processor.go` do not recompute it (grepped, no hits).
- Block-level RLP decode of a pushed/gossiped block is parallelized (`common/block/block.go:184-198` `DecodeRLP` -> `decodeBlockTxs`/`:387-427`, threshold `parallelTxDecodeMin`), and each per-tx decode caches its own wire bytes (`DecodeEthereumTransaction`), so the transactions-root leaf hashes and any later RLP re-encode reuse those bytes rather than re-deriving them.
- The one full per-tx re-marshal outside storage/write (`internal/miner/worker.go:2179-2192`, the `MinedEntireEvent` RPC snapshot) is gated behind `event.GlobalEvent.HasSubscribers(...)` and does not run when nothing is subscribed -- already the "ask before building" fix described in its own comment.
- Receipts are deep-copied once, after the push and the Proposal leave, not before (`internal/miner/worker.go:705-725`), matching the `6445f1bf` list item "the receipts copy before the Proposal".
- No `Size()`/`len(Marshal())` measure-only calls were found in the hot files searched (`internal/blockchain.go`, `internal/blockchain_write.go`, `internal/state_processor.go`, `internal/miner/worker.go`, `internal/consensus/hotstuff/*.go`); the one `Size()`-adjacent path (`common/transaction/transaction.go` `EncodedSize`/`EthEncoded`) is itself cache-backed.

Verdict: no same-class duplicate of the n42-rs mechanism exists on the vote
path (it is falsified there), because the Proposal is hash-only by design.
One confirmed, already-measured full second pass over all 163k txs remains
on the write path for storage-layout reasons (#1), and one narrow race
window between the push and gossip decode paths is suspected but not
confirmed live (#2). Neither is new: #1 is a known, parallelized, tracked
cost; #2 is a previously-undocumented edge case worth a follow-up read of
the push/gossip interleave under a real fleet trace, not a code change on
current evidence.

## S11 prepared -- n42-r86 built on a clean lineage, round ready, not launched (2026-09-20)

Implements the two diagnostics 6by asked for (commit `537ec21e`,
`feat(miner): pre-fill step timers and a build-stall goroutine dump
(diagnostic)`): named step timers between `commitWork begin` and the start
of the fill (`miner: prefill phases`, logged above 50 ms) and a 3 s stall
watchdog that dumps every goroutine's stack to
`<datadir>/log/build-stall-<n>-<unixsec>.stacks`. Both behind
`N42_BUILD_STALL_DIAG=1`, off by default, no behavior change to block
production. See `docs/QS_BLOCK_TIME_BUDGET.md` section 6bz for the full
writeup, field-by-field file:line list, the one-variable check and test
results.

**Confirming n42-r84's lineage surfaced a live contamination risk, and the
commander ruled how to route around it rather than touch the branch.**
qs/replan's current HEAD carries commit `3c9311ac` (the n42-r85 base-read
cache, `parallel.BaseCache`) unconditionally -- `internal/parallel_processor.go:342`
constructs it on every parallel build/import with no env gate anywhere in
the cache's own files. 6bw falsified this cache on the same eight-generator
shape this round uses (B mean 96.1k vs the 127.6k standing best, a 24.7%
fall), and it was never reverted or gated on the branch. Ruling: build
n42-r86 with the established file-checkout recipe (worktree at `f7ec2836` +
n42-r84's exact file list + S11's own files) so the cache's files are
simply never in the build, rather than revert or gate `3c9311ac` on
qs/replan.

**n42-r86: built.** `/data/blockchain/gov5-work/n42-r86`, 108,694,408 bytes,
sha256 `f07e2b811d6569363c363d0286b17d672b4854dfecbe593297fa63e7a77e665c`.
Commit list: base `f7ec2836`; `DEFERRED`/`FOLD`/`TAIL`/
`internal/parallel/executor.go` at `c0931aeb`; S11's seven files at
`537ec21e` -- four byte-identical to r84's own version of the file
(`internal/blockchain.go`, `internal/blockchain_types.go`,
`internal/miner/miner_test.go`, `log/root.go`, checked out whole), one
requiring a hunk-only apply (`internal/miner/worker.go`: two intervening
commits `19687889`/`89d15267` are not part of r84's lineage, so only S11's
own diagnostic diff was applied onto r84's copy of the file, verified
clean with `git apply --check` first), and two brand new
(`internal/miner/build_stall_watchdog.go`,
`internal/miner/build_stall_watchdog_test.go`). Verified in the binary
itself: `strings n42-r86 | grep -c "build stalled before fill"` = 1,
`strings n42-r86 | grep -c BaseCache` = 0. In the build worktree: `go vet`
clean, `go test ./internal/miner/...` 53/53 (2 fewer than qs/replan HEAD's
55 -- the two tests `19687889`/`89d15267` added are correctly absent from
r84's lineage).

**Runner: `run-r35zzz.sh`/`chain-35zzz.sh`, built from the 35zzt pair, not
launched.** 35zzt's eight-generator shape (`-target-depth 45000`, `--floods
8 --senders 1000`, unchanged), binary retargeted to n42-r86,
`N42_BUILD_STALL_DIAG=1` added next to `N42_MINER_ADOPT_APPENDS=1`. One
harness fix carried in from after 35zzt (diffed against `run-r35zzy.sh`):
the `MODE-FAILED` regex no longer treats "deferred check FAILED" as an
abort signature (expected pre-import vote decline, not a failure). No other
difference beyond the sixteen-generator shape (skipped by design) and
round-name tokens. `chain-35zzz.sh` waits on `wr-logs/r35zzy.log`'s
terminal line; memory gate, n42-rs turn-taking and quiet-box checks
unchanged. `bash -n` clean on both.

**To read the dump when the round produces one:** the file is a plain
`runtime.Stack(_, true)` text dump (goroutine ID, state, full call stack per
goroutine); grep it for the step name the accompanying
`miner: build stalled before fill` log line names (e.g.
`step="alignAppliedBranch"`) to find the stuck goroutine, then read up its
stack for the exact blocking call (channel receive, mutex `Lock`, mmap
fault, etc.). The `miner: prefill phases` lines in the surrounding log
window separately show which named step's cumulative time actually grew
that build, without needing the dump at all in the common case.

Prediction 82 is registered in 6bz. Launch is the commander's next call.

## S14 prepared -- n42-r87 built, contention diagnostics on, round ready, not launched (2026-09-21)

Why: 6cb-6ce closed transport and the held-vote minority as explanations
for the in-tenure cycle's 520 ms push->QC segment (Round1 129 ms + Round2
370 ms = 499 ms, 95.9% of it), leaving ~450 ms of consensus handlers
waiting on something no line names. See `docs/QS_BLOCK_TIME_BUDGET.md`
section 6cf for the full writeup, the ranked suspects found by reading the
code, and prediction 83.

**n42-r87: built.** `/data/blockchain/gov5-work/n42-r87`, sha256
`a22b16ce540bfba172174fd494fcb76542ca7c87acb5effd1ff0b9378d215eb9`. Same
file-checkout recipe as n42-r86 (worktree at `f7ec2836` + n42-r86's file
set + S14's commit `56cc1dac`): all 7 files S14 touches/adds were
byte-identical to r86's own version before this change (one of them,
`internal/consensus/hotstuff/proposal.go`, is part of r86's own DEFERRED
lever from `c0931aeb`; the other 6 are simply `f7ec2836`'s untouched
copies), so every one was checked out directly from `56cc1dac` with no
hunk surgery needed. `internal/parallel/base_cache.go` confirmed absent;
`grep -rl BaseCache` over the worktree: empty. In the build worktree:
`go vet` clean, `go test ./internal/consensus/hotstuff/...` (263 tests)
and `./internal/miner/...` both pass, full package run (not `-short`,
~5-6 s including under `-race`). `strings n42-r87 | grep -c BaseCache` = 0;
`... | grep -c "build stalled before fill"` = 1; `... | grep -c
"contention profiling enabled"` = 1.

**What S14 adds, gated behind `N42_CONTENTION_DIAG=1`** (see 6cf for the
full field list and file:line detail): runtime mutex/block profiling
(`SetMutexProfileFraction(5)`/`SetBlockProfileRate(1_000_000)`, before the
consensus service starts) and per-message vote-path timing stamps
(arrival -> `e.mu` acquired -> handler done) aggregated into the existing
`hotstuff view timing` line as new short fields: leader `r1n`/`r1lw`/
`r1lwMax`/`r1wk`/`r1kth`/`r1qk` and the `r2*` equivalents; follower
`propLw`/`propWk`/`pqcLw`/`pqcWk`/`pqc2cv`/`cvHeld`/`cvGate`. Silent when
the switch is off.

**Suspects found by reading the code, ranked (not fixed -- 6cf has the
full detail):**
1. `JournalVote` (`service.go:1219-1226`, an MDBX write transaction) runs
   under `e.mu` on every single vote and shares the node's one MDBX writer
   lock with block import/write (`n.db`, `node.go:1863`) -- a concurrent
   block write can stall it, and since it runs under `e.mu`, stalls ALL
   consensus message processing on that node for the wait.
2. `processOutputs`' serial output loop runs `CommitToCanonical`/
   `persistState` inline (`service.go:585-704`), a second serialising
   point (already partially mitigated for broadcasts) that also contends
   for the same MDBX writer lock as suspect 1.
3. Unbatched BLS verification of incoming QC messages under `e.mu`
   (`verifyQCWithSet`) -- real CPU held under the lock, but likely small
   (single digits of ms for 7 validators) next to 1-2.

**Runner: `run-r35zzza.sh`/`chain-35zzza.sh`, built from the 35zzz pair,
not launched.** Same eight-generator shape as always (`-target-depth
45000`, `--floods 8 --senders 1000`); binary retargeted to n42-r87;
`N42_CONTENTION_DIAG=1` added next to `N42_BUILD_STALL_DIAG=1`.
`chain-35zzza.sh` waits on `wr-logs/r35zzz.log`'s terminal line (its
actual predecessor); gates unchanged. New: a per-B-leg profile capture in
`run-r35zzza.sh` (150 s after leg start, sitting leader + one non-leader
follower, 20 s CPU + mutex + block delta profiles into
`/data/blockchain/wr-pprof/r35zzza-<leg>-node<i>-{cpu,mutex,block}.pb.gz`,
curl failures logged and never fatal). `bash -n` clean on both.

**How to read a saved profile:** `go tool pprof -top -sample_index=delay
/data/blockchain/wr-pprof/r35zzza-B1-node<i>-mutex.pb.gz` ranks lock sites
by cumulative wait time (drop `-sample_index=delay` for a contention
count instead); the block profile reads the same way; the CPU profile
reads like any `go tool pprof -top ...cpu.pb.gz`. Cross-reference against
the `hotstuff view timing` line's new fields for the same ~20 s window on
the same node -- the log line names a PHASE and a MAGNITUDE, the profile
names a LOCK/CALL SITE and a MAGNITUDE.

Prediction 83 is registered in 6cf. Launch is the commander's next call.

## S15b prepared -- n42-r88 built, block-gossip-fallback experiment ready, not launched (2026-09-21)

Why: 6cg found the vote round-trip's 110-349 ms `kth` gaps are message-
arrival time, not a lock. 6ch read hypothesis G (the unconditional
block-gossip fallback head-of-line-blocking votes) as falsified for
`r2kth`'s dominant share, crediting `CheckDeferredBlock` instead -- but
**the commander overruled that to INCONCLUSIVE in QS_QUEUE.md's S15
row**: only 1.8% of commit votes were ever held on the deferred-check
gate (`cvHeld`, 6cg) and a follower's commit vote fires 2 ms after a
PrepareQC arrives (`pqc2cv`, 6cg), so the check is done long before the
gating message shows up for the 98.2% majority and cannot be what most
of `r2kth` waits for; 6ch's own size asymmetry is what a shared,
unprioritized per-peer gossip queue predicts by timing (Round1 runs
while the fallback is still being published, Round2 while it sits in
every per-peer queue). Hypothesis G is therefore live and untested by
direct evidence for BOTH `r1kth` and `r2kth` -- S15b's switch is what
decides it, per the commander's own note. See
`docs/QS_BLOCK_TIME_BUDGET.md` section 6ci for the full writeup and
prediction 84.

**n42-r88: built.** `/data/blockchain/gov5-work/n42-r88`, 108,719,640
bytes, sha256
`5d3481dd535f0b64e28ba7082ebdca9ad10c23c5bce62dee19d5fb5756b6ae4b`. Same
file-checkout recipe as n42-r86/r87, extended with S15b's commit
(`b876b3d2`): one file modified (`internal/blockchain.go`, confirmed
byte-identical to r87's own version before this change -- untouched
since S11's `537ec21e`) and one new (`internal/block_gossip_fallback_test.go`),
both checked out directly, no hunk surgery needed. `internal/parallel/
base_cache.go` confirmed absent; `grep -rl BaseCache`: empty. In the
build worktree: `go vet` clean on `internal/`, `internal/consensus/
hotstuff/...`, `internal/miner/...`; `go test` passes on all three
(`internal/`: 200 tests, ~3 s -- fast enough that no `-short` was
needed). `strings n42-r88 | grep -c BaseCache` = 0; `... | grep -c
"build stalled before fill"` = 1; `... | grep -c "contention profiling
enabled"` = 1; `... | grep -c "block gossip fallback disabled"` = 1;
`... | grep -c "block gossip fallback skipped"` = 1.

**What S15b adds, behind `N42_BLOCK_GOSSIP_FALLBACK`** (unset/"1" =
today's behaviour; "0" = the experiment): `SealedBlock`
(`internal/blockchain.go`) skips its post-direct-push `BroadcastBlock`
gossip call once the direct push was dispatched to at least one
connected peer (`directPushBlock` now returns that count); on zero
peers it still gossips. Exactly one gossip-publish call site exists in
the whole repo and this switch covers it; no follower re-publishes or
forwards a received block (`internal/sync/subscriber_blocks.go`/
`rpc_block_push.go` call no publish at all).

**Safety.** A follower that misses the direct push recovers through an
already-existing, gossip-INDEPENDENT path: a Proposal names only a
block hash, `OutputExecuteBlock` always triggers
`FetchBlockByHash` (`internal/consensus/hotstuff/service.go:620-634` ->
`internal/sync/rpc_block_by_hash.go:60`+), a direct peer-to-peer stream
request unrelated to the gossip `block` topic. This runs unconditionally
regardless of the switch, so turning the fallback off cannot wedge the
fleet -- no additional "fall back to gossip on push error" safety net
was needed or added.

**Runner: `run-r35zzzb.sh`/`chain-35zzzb.sh`, built from the 35zzza
pair, not launched.** Same eight-generator shape; binary retargeted to
n42-r88; `N42_BLOCK_GOSSIP_FALLBACK=0` added, `N42_CONTENTION_DIAG=1`/
`N42_BUILD_STALL_DIAG=1` kept on; S14's per-B-leg profile capture kept,
outputs renamed to `r35zzzb-*`. `chain-35zzzb.sh` waits on
`wr-logs/r35zzza.log`'s terminal line; gates unchanged. `bash -n` clean
on both.

Prediction 84 is registered in 6ci, exactly as specified: (a) mechanism
(`r2kth` 349->under 100 ms, `r1kth` 110->under 50 ms, in-tenure cycle
806->under 650 ms), (b) throughput (B mean > 130.8k, watch for supply
binding near 142k), (c) safety (no BAD BLOCK/divergence/rise in view
timeouts or block-fetch events vs 35zzza). Per the commander's overrule
above, `r2kth` is now the clause with the STRONGER supporting case going
in (the deferred-check alternative is ruled out for the 98.2% majority),
not the harder one -- this round is a real test of the primary
mechanism, not just a residual. Launch is the commander's next call.

## S17 prepared -- n42-r89 built, send/receive edge stamps ready, not launched (2026-09-21)

Why: 6ck found every existing Round2 stamp (`CommitVoteSent`,
`PrepareQCFormed`, `pqc2cv`) is taken BEFORE the message reaches the
output channel, not after it hits the wire -- so nothing has ever
measured emit->publish on the sender or wire->handler-entry on the
receiver, which is where `r2kth`'s steady ~350 ms (linear in tx count,
unlike Round1's flat ~60 ms on the same paths and sizes) most likely
sits. See `docs/QS_BLOCK_TIME_BUDGET.md` section 6cl for the full
writeup, the READERS finding, and prediction 85.

**n42-r89: built.** `/data/blockchain/gov5-work/n42-r89`, 108,750,176
bytes, sha256
`145301fd8fbebf46d0a2566b7a362118cecd0608b0f7b001ffc209f5f1784a7a`. Same
file-checkout recipe as n42-r86/87/88 (commit `9f307e90`): all 7 files
S17 touches/adds were byte-identical to n42-r88's own version before
this change (5 via `56cc1dac`, S14's commit; `rotor_wiring_test.go` via
`f7ec2836`, since it is not part of any lever/S11/S14/S15b file list),
so every file was checked out directly, no hunk surgery needed.
`internal/parallel/base_cache.go` confirmed absent; `grep -rl
BaseCache`: empty. In the build worktree: `go vet` clean on `internal/`,
`internal/consensus/hotstuff/...`, `internal/miner/...`; `go test`
passes on all three (`internal/consensus/hotstuff/...`: 273 tests, full
run and under `-race`, ~6-7 s -- no `-short` needed). `strings n42-r89 |
grep -c BaseCache` = 0; `... | grep -c "build stalled before fill"` = 1;
`... | grep -c "contention profiling enabled"` = 1; `... | grep -c
"block gossip fallback disabled"` = 1; `... | grep -c "rotor failed ->
gossip"` = 1 (S17's own new marker).

**Field glossary** (all behind `N42_CONTENTION_DIAG=1`, silent
otherwise -- `TestLogLineSilentWithoutSendRecvData`):

Sender (this node's own emit->publish timing for a message it sent):
- `prEmit2Deq`/`prDeq2Pub`/`prPubDur`/`prPath` -- Proposal (leader)
- `pvEmit2Deq`/`pvDeq2Pub`/`pvPubDur`/`pvPath` -- prepare vote (follower)
- `pqcEmit2Deq`/`pqcDeq2Pub`/`pqcPubDur`/`pqcPath`/`pqcPubAt` -- PrepareQC (leader)
- `cvEmit2Deq`/`cvDeq2Pub`/`cvPubDur`/`cvPath`/`cvPubAt` -- commit vote (follower)
- `Emit2Deq` = t_emit (engine `emit()`) -> t_deq (`processOutputs` dequeues it)
- `Deq2Pub` = t_deq -> t_pub0 (queued behind this goroutine's own dispatch)
- `PubDur` = t_pub0 -> t_pub1 (the actual network call(s))
- `PubAt` = t_pub1, absolute unix ms (PrepareQC/commit vote only, for cross-node joins on the shared host clock)
- `Path` = `"rotor ok"` | `"rotor failed -> gossip"` | `"gossip only"`

Receiver (arrival timing on this node):
- `pqcRx2Arr`/`pqcRxAt`/`pqcVia` -- follower's one PrepareQC message this view (`Via` can be `"both"` if a duplicate arrived on the other transport)
- `pvKthRx2Arr`/`pvKthRxAt`/`pvKthVoter`/`pvKthVia`/`pvMaxRx2Arr` -- leader's quorum-completing (k-th) prepare vote, plus the max rx2arr over the round's votes
- `cvKthRx2Arr`/`cvKthRxAt`/`cvKthVoter`/`cvKthVia`/`cvMaxRx2Arr` -- same, commit vote
- `Rx2Arr` = t_arrive (existing S14 stamp) - t_rx (new: earliest point the bytes were in this process)
- `dupN` = count of messages seen a second time via the OTHER transport this view (kept: the first arrival's stamps; "gossip is always sent" regardless of Rotor's own success)

**READERS (read, not changed):** the gossip path
(`subscribeMessages`) is ONE serial goroutine that calls `ProcessEvent`
synchronously -- a slow `ProcessEvent` (e.g. `e.mu` held elsewhere)
blocks this loop from calling `sub.Next()` again, delaying the NEXT
gossip message's own t_rx, which no existing lock-wait field would show
(they all key off t_arrive, taken only once the delayed message finally
gets read). The Rotor path is the opposite: libp2p's `SetStreamHandler`
gives every incoming stream its own goroutine
(`internal/node/hotstuff_p2p_adapter.go`), so a slow `ProcessEvent`
there does not block reading the next Rotor message. Nothing
size-proportional sits between t_rx and t_arrive on either path by
construction (the two stamps are code-adjacent), and no topic validator
is registered for the consensus topic (confirmed by grep, matching
6ch).

**Runner: `run-r35zzzc.sh`/`chain-35zzzc.sh`, built from the 35zzzb
pair, not launched.** ALL env unchanged from 35zzzb, including
`N42_BLOCK_GOSSIP_FALLBACK=0`; binary retargeted to n42-r89.
`chain-35zzzc.sh` waits on `wr-logs/r35zzzb.log`'s terminal line. Added:
a `/debug/pprof/goroutine?debug=1` capture (aggregated stacks) alongside
the existing per-B-leg CPU/mutex/block captures, into
`wr-pprof/r35zzzc-<leg>-node<i>-goroutines.txt` -- shows cgo/syscall
waits (e.g. an MDBX writer-lock wait) invisible to the mutex/block
profiles. `bash -n` clean on both.

Prediction 85 is registered in 6cl. Launch is the commander's next call.

## S18 prepared -- n42-r90 built, journal-write timing stamp ready; the env-separation switch is NOT built (safety conflict), not launched (2026-09-21)

Why: 6cm narrowed Round2's unmeasured 94% to the leader's own
`PrepareQCFormed -> emit()` gap (`voting.go:208-231`), which contains
`journalCommitVote`'s MDBX write against the same `db` handle the
leader's own concurrent block write uses. S18 asked for two things: a
`jpvMs`/`jcvMs`/`jcvAt` timing stamp on the journal calls, and an env
switch to move the HotStuff safety journal into its own MDBX
environment so an A/B round could test whether removing the writer
contention helps. See `docs/QS_BLOCK_TIME_BUDGET.md` section 6cn for
the full writeup and prediction 86.

**Only the timing stamp is built.** The env-separation switch is not,
because `SaveConsensusState` is written by two paths sharing one
monotonic, equivocation-preventing record -- `JournalVote`
(`service.go:1308`, standalone, the one the task describes) and
`newStateHook().run` (`service.go:1348`), which is ALSO the `inTx` hook
folded atomically into `CommitToCanonicalWith` on every committed
block (`service.go:689`) by design (`blockchain.go:1355-1367`'s own
doc comment: closes a crash window between canonical head and
consensus state). The task's own rule says not to split a journal
write that shares a transaction with chain data. Splitting only the
first path into a second environment while the second stays in the
chain DB gives `mergeMonotonic` (`persistence.go:137`) two
independently-advancing copies of the same record with no
reconciliation -- `LoadConsensusState` at restart would read a stale
vote commitment from whichever environment it picks, reopening the
exact double-vote window the journal exists to close
(`persistence.go:68-75`'s own doc comment names this failure mode).
This is a confirmed correctness conflict, not a judgment call --
exactly the condition the task named as a stop-and-report case. No env
var, no migration, no `hotstuff-reset`/`qs-hsreset` changes.

**n42-r90: built.** `/data/blockchain/gov5-work/n42-r90`, 108,737,984
bytes, sha256
`193bd320478acc9b0588605009e0eb93387eb9e9716ccb520aa8d30b422d6759`.
Same file-checkout recipe as n42-r86/87/88/89 (commit `e1d8d7d1`): both
touched files (`engine.go`, `view_timing.go`) were byte-identical to
n42-r89's own version before this change (`git diff 9f307e90
e1d8d7d1^` empty for both), checked out directly, no hunk surgery;
`journal_timing_test.go` is new. `internal/parallel/base_cache.go`
confirmed absent; `grep -rl BaseCache`: empty. `go vet` clean on
`internal/...`; `go test` passes on `internal/consensus/hotstuff/...`
(both `N42_CONTENTION_DIAG` placements, and under `-race`),
`internal/miner/...`, `internal/`, `internal/parallel/...`. `strings
n42-r90 | grep -c BaseCache` = 0; the four prior markers ("build
stalled before fill", "contention profiling enabled", "block gossip
fallback disabled", "rotor failed -> gossip") each = 1; the three new
markers (`jpvMs`, `jcvMs`, `jcvAt`) each = 1.

**Field glossary addition** (behind the existing
`N42_CONTENTION_DIAG=1`, silent otherwise):
- `jpvMs` -- this node's own `journalPrepareVote` MDBX-write duration this view (summed, in the rare case it fires more than once)
- `jcvMs` -- same, `journalCommitVote`. On the leader this is the self-commit-vote journal write inside `tryFormPrepareQC` (`voting.go:229`) -- the one call in L0.
- `jcvAt` -- absolute start time (unix ms) of the `journalCommitVote` call, for cross-node/cross-round joins

**Runner: `run-r35zzzd.sh`/`chain-35zzzd.sh`, built from the 35zzzc
pair, not launched.** NOT an A/B script -- with no second variable,
every leg runs ONE configuration, identical to 35zzzc plus the new
stamp. Header comments in both rewritten by hand to say this plainly.
`chain-35zzzd.sh`'s predecessor wait fixed by hand to `r35zzzc.log`
(the `sed` pass alone would have left it waiting on `r35zzzb.log`,
35zzzc's predecessor, not 35zzzd's); binary references updated by hand
to `n42-r90`. ALL other env unchanged from 35zzzc, including
`N42_BLOCK_GOSSIP_FALLBACK=0` and the S17 goroutine-dump capture.
`bash -n` clean on both; confirmed not running.

Prediction 86 (revised, see 6cn for the exact bar and caveat) is
registered. Launch is the commander's next call.

## S19 prepared -- n42-r91 built, leader-write-after-journal switch ready, A/B by leg, not launched (2026-09-21)

Why: 6cn/6cm found Round2's unmeasured 94% is `journalCommitVote` (the
leader's own self-commit-vote MDBX write) queueing behind the leader's
own concurrent `WriteBlockWithState` for the single MDBX writer. S19:
delay the START of that write until the journal write has already
succeeded (or a timeout, or the view is abandoned) instead. See
`docs/QS_BLOCK_TIME_BUDGET.md` section 6co for the full writeup,
Part 1's five code-level answers, and prediction 87.

**Part 1 answers, in one line each (6co has the full evidence):**
- (a) write starts in `handleSealed` (worker.go), on the miner's own
  `resultLoop` goroutine, triggered by the engine's `Seal` call
  handing a sealed block to `w.resultCh`.
- (b) `persistWait` does NOT see this delay in the current
  configuration (ownPendingSpeculation bypasses it for a leader's own
  speculative next build; measured 0.0 ms median/p95/max across 226
  lines in round 35zzz). `CommitToCanonicalWith` needs the write
  already complete (or cached); NOT directly on the path to proposing
  v+1 (`TriggerBlockProduction` is a fast, non-blocking channel send),
  BUT it runs on the SAME serial `processOutputs` loop, strictly
  before `OutputViewChanged` (which dispatches v+1's build) -- so if
  Round2 shrinks a lot, `CommitToCanonicalWith(v)` may itself queue
  behind the still-running delayed write before `processOutputs` can
  reach `OutputViewChanged`, potentially reappearing as a NEW delay
  between CommitQC(v) and dispatching v+1. Handled safely today (a
  deferred-commit retry path already exists) but that retry
  (`NotifyBlockImported`) is wired to the SYNC layer only, not to the
  miner's own local write completing -- a leader's own deferred
  commit would instead clear via the existing "not executed locally"
  fetch-by-hash fallback, which will likely start firing routinely
  (log.Error + a metric) for the leader's own full blocks in the ON
  leg. Self-healing, not unsafe, but a real, measurable, and
  previously-rare-turned-routine side effect this round should show.
- (c)/(d) other MDBX writes on the leader in order:
  `journalPrepareVote` (always wins the race against the block write --
  same goroutine, strictly before it), `WriteBlockWithState`,
  `journalCommitVote` (today's collision, the target), then
  `CommitToCanonicalWith`+folded state save, then periodic
  `persistState()`. Followers: `journalPrepareVote` on proposal
  arrival CAN collide with the follower's own import write of the
  previous block; stated, not investigated further (out of scope).
- (e) hand-over: the mechanism is unconditional, nothing changes.
  Timeout (no PrepareQC ever forms): the write still eventually
  starts, via the SECOND fire path (`advanceToView` releases the latch
  with why "abandoned" for any pending self-proposal when its view
  ends), never blocked forever.

**n42-r91: built.** `/data/blockchain/gov5-work/n42-r91`, 108,753,048
bytes, sha256
`df2cf25426b0f445bbe4e921e374fd16e7dd4d5bc352aff5e57417766d01919d`.
Same file-checkout recipe as n42-r86 through r90 (commit `812cf162`):
all 6 pre-existing files S19 touches were byte-identical to n42-r90's
own version before this change, checked out directly; `worker.go`
needed the same two-hunk approach n42-r86 established (S11's hunk,
then S19's own, both apply cleanly onto the same f7ec2836 base); one
file (`view_timing.go`, S18's own, untouched by S19) was missed on the
first build attempt -- a straight `undefined: contentionStamps` compile
failure, caught immediately by `go build` and fixed by checking it out
from `e1d8d7d1` before proceeding. `internal/parallel/base_cache.go`
confirmed absent; `grep -rl BaseCache`: empty. `go vet` clean on
`internal/...`; `go test` passes on `internal/consensus/hotstuff/...`
(both switch placements, and under `-race`), `internal/miner/...`,
`internal/`, `internal/parallel/...`. `strings n42-r91 | grep -c
BaseCache` = 0; the seven prior markers each = 1; the three new
markers (`lwWait`, `lwWhy`, `not-proposed-yet`) each = 1.

**Field glossary addition** (miner's own `"miner: propose phases"`
line, not `hotstuff view timing` -- these values are only known on the
miner side): `lwWait` (ms the write start was delayed), `lwWhy`
(`journal` / `abandoned` / `timeout` / `off` / `not-proposed-yet` /
`unsupported`).

**Runner: `run-r35zzze.sh`/`chain-35zzze.sh`, built from the 35zzzd
pair, not launched. THIS IS an A/B-by-leg round** (unlike 35zzzd):
`run_leg` gained a 5th parameter, `N42_LEADER_WRITE_AFTER_JOURNAL`,
exported inside the same per-leg subshell that already varies
`gasceil` by leg -- confirmed safe by reading `bench-run.sh`: every
`run_leg` call fully stops and freshly relaunches all 7 node processes
(`stop-fleet.sh` then `bench-7node.sh`), so each leg's env, including
the new switch, is picked up as a genuine per-process startup value.
Calls: `warmup 0`, `A1 0`, `B1 0` (switch off, the in-round baseline),
`B2 1`, `A2 1` (switch on). Predecessor-wait fixed by hand to
`r35zzzd.log` (S19's actual predecessor; the sed pass alone would have
left S18's own `r35zzzc.log` target in place); binary references
updated by hand to `n42-r91`. All other env unchanged from 35zzzd,
including `N42_BLOCK_GOSSIP_FALLBACK=0`/`N42_CONTENTION_DIAG=1`.
`bash -n` clean on both; confirmed not running.

Prediction 87 (see 6co for the exact bars and the "NOT merely moved"
caveat) is registered. Launch is the commander's next call.
