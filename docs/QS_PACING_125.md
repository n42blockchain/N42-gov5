# Halving the pacing cap to 125ms: the wait shrank, the block time didn't

Round 35zzzbc (S77), n42-r110, `--block-interval-ms` 250 (B1) vs 125 (B2),
standing config otherwise. Logs: `/data/blockchain/wr-logs/r35zzzbc-keep/node*/`,
`r35zzzbc.log`. Launch check confirmed: `r35zzzbc.log:200-201` shows
`interval-ms=125` for the B2 leg and `=== r35-B2 ... interval=125ms ===`;
`bench-run.sh`'s own per-node `/proc/<pid>/cmdline` grep passed on all 7 nodes
(no abort).

Result: B1 139.3k TPS (140.4k/138.3k at 0.545s/0.667s), B2 141.2k TPS
(144.7k/137.7k at 0.526s/0.682s), **+1.4%** — inside the 3.6% noise floor.

## Rule 133 verdict

**(a) Pacing wait and QC-to-QC, B1 vs B2** (full leg, all 7 nodes, `miner:
pacing wait` / `hotstuff view timing` `us=` fields, consecutive-view diffs
only):

| metric | B1 (250ms) | B2 (125ms) | delta |
|---|---|---|---|
| pacing wait median | 179.0 ms (n=2159) | 54.3 ms (n=1348) | -124.7 ms |
| QC-to-QC median | 250.2 ms (n=2227 diffs) | 125.2 ms (n=3793 diffs) | -125.0 ms |

Both halved almost exactly with the knob — the pacing mechanism itself
behaves as designed in both legs.

**(b) Stability, full leg vs full leg (~13 min each, comparable duration):**

| signal | B1 | B2 |
|---|---|---|
| refusing block production on unexecuted committed parent | 0 | 3 |
| hotstuff "timeout" | 62 | 78 |
| sealed block is stale | 2281 | 4056 |
| catch-up: requesting range | 298 | 313 |
| conflicting commits | 0 | 0 |
| BAD BLOCK | 0 | 0 |

B2 is not flat vs B1: it introduces 3 occurrences of "refusing block
production on unexecuted committed parent" (zero in B1, the clean signal rule
133(d) names as the falsifier), stale-block rejections nearly double, and
timeouts/catch-ups tick up modestly. No conflicting commits or bad blocks in
either leg — safety held — but condition (b) ("stability signals at B1's
levels") fails.

**(c)** B2 beats B1 by only 1.4%, below the 3.6% noise floor — not a real
throughput gain.

**(d)** The 3 "unexecuted committed parent" refusals in B2 (vs 0 in B1) are
exactly the falsifying signal rule 133(d) anticipated: followers did stall
under the tighter 125ms grid often enough to trip the guard, even though it
never escalated to a conflicting commit or bad block in this run.

## Where the time went in B2 (view chain, win1, leader node6, consecutive
views 13378-13381)

Chain per view: `qcFormedUs(N)` → `commitWork begin(N+1)` → pacing wait (if
any) → `proposal broadcast(N+1)` → first vote → `qcFormedUs(N+1)` → commit
vote round → `commitQcUs(N+1)`.

| segment | view 13378 | view 13379 | view 13380 | view 13381 |
|---|---|---|---|---|
| commitQcUs(N-1) → commitWork begin(N), speculative=false | - | 27.1 ms | 0.5 ms | 0.5 ms |
| pacing wait (`miner: pacing wait`) | 0 ms (none logged) | 0 ms (none logged) | 0 ms (none logged) | 53.8 ms |
| → proposal broadcast | 14.2 ms | 1.9 ms | 1.0 ms | (included above) |
| broadcast → qcFormedUs (r1, vote round) | 62.3 ms | 63.0 ms | 59.7 ms | — |
| qcFormedUs → commitQcUs (r2, commit-vote round) | **377 ms** | **200 ms** | 9.9 ms | — |

Medians across the 7 views captured in this stretch: pacing wait ~0-54 ms
(often skipped entirely — the r2 stall already ate the view's budget), r1
(propose→QC) ~60 ms, **r2 (QC→commitQC) ~200 ms, with a 377 ms outlier** — now
the largest and most variable segment, dwarfing both the pacing wait and r1.

Block-level: this fleet runs ~1 block per view (no multi-view batching
observed — consecutive `blockimport phases` block numbers `n` increment by 1
per view). So the view chain above is also the block chain; there is no
separate "wait for previous block's execution" step distinct from r2 — r2
*is* that wait, by construction.

**New largest segment, with code:** `internal/consensus/hotstuff/proposal.go:286-296`
(`processPrepareQC`), the two-phase R2 gate:

```go
// Two-phase R2 gate: the CommitVote is the execution attestation — hold
// it until the block is imported locally, so a CommitQC still proves
// 2f+1 validators EXECUTED the block. onBlockImported re-enters this
// function with the held message once the import lands.
if e.twoPhaseVote && !e.importedBlocks[pqc.BlockHash] {
    held := *pqc
    e.pendingCommitQC = &held
    log.Info("two-phase vote: holding commit vote until block imports", ...)
    return nil
}
```

A follower's commit vote (and hence the leader's `commitQcUs`) is gated on
that follower having *finished importing/executing* the proposed block
locally — not just received and prepare-voted it. At 250ms/view this gate
usually resolved inside the pacing slack (follower exec is faster than 250ms
most of the time), so it was invisible. At 125ms/view, follower exec
increasingly runs past the shortened slot, so r2 — this exact gate —
balloons to 200-377ms and becomes the binding constraint, eating back nearly
all of the time the pacing cut freed.

## Why +1.4% and what's next

Halving `--block-interval-ms` removed ~125ms of leader-side sleep per view,
but that sleep was mostly slack the follower's execution time didn't need
anyway: once the artificial pacing floor dropped below real follower
import/exec latency, `proposal.go:290`'s two-phase commit-vote gate started
absorbing the freed time instead, so QC-to-QC fell exactly with the knob
while end-to-end block cadence barely moved. The next binding constraint is
follower block-import latency feeding the R2 commit-vote gate, not leader
pacing.

Proposed next lever (config-only, no code change): re-run this A/B with
follower-side block-import timing captured per view (the `blockimport
phases` `exec`/`write`/`root` fields already emit this — correlate them
against the R2 stall per view instead of guessing from `r2=NNNms`) to confirm
whether the stall tracks `exec+write+root` time directly. If so, the lever
with expected effect is whatever already-queued work shortens that path
(e.g. S74's `qmdb-evict-lag-blocks` or a future write-path change), not a
further pacing cut — a Sonnet-level follow-up round re-measuring R2 against
import-phase timestamps should land in well under an hour and would either
confirm or falsify this directly, expected to show R2 ≈ import latency ± 20ms
if correct.

## Decision

**Revert interval-ms to 250.** Condition (b) failed (3 new "unexecuted
committed parent" refusals, higher stale/timeout/catch-up counts) and
condition (c) failed (+1.4%, inside the 3.6% floor) — two of rule 133's four
gates did not pass, and (d)'s falsifier (rising refusal count) is exactly
what triggered. 125ms is too aggressive for this fleet at today's follower
import latency; keep 250ms as the standing config until the R2/import-latency
investigation above lands.
