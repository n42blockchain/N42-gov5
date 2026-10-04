# S79: CPU profile of the follower import path on full blocks — blocked on data reach

## Goal

CPU-profile the follower import path (exec wave, finalize, QMDB root) on the
>=100k-tx blocks from the 2026-10-03 125ms-pacing round (docs/QS_WORKERS_64.md,
docs/QS_PACING_125.md), using the offline single-node `qs-replay` tool, to name
the top functions inside the three phases node3's "parallel block" lines show
costing exec~275-294ms / finalize~113-117ms / root (not separately profiled
this round).

## Method attempted

1. Box-quiet check (three checks, ~20:18:17-20:18:56 EDT): load1 0.01-0.02,
   no `n42`/fleet processes running, no other `.box-claim-*` file. Claimed
   `/data/blockchain/gov5-work/.box-claim-s79`.
2. `df -h /data`: 285G free (>250G required). Reflink copies made:
   `cp -a --reflink=always /data/blockchain/qs-node0 /data/blockchain/qs-replay-node0`
   (2.2s, 35G) and the same for qs-node3 (36G) once node0 turned out empty-tailed.
3. Binary: `/data/blockchain/gov5-work/n42-r110`
   (sha256 `ecae54757bc175885afee96a8649124c1ae8513af796a602789e4f7fe77e0b34`),
   the standing lineage binary named in docs/QS_WORKERS_64.md / QS_QUEUE.md S76-S78.
4. `qs-replay --replay.scan` (cmd/n42/qsreplay_cmd.go) lists the last
   `replay.depth` blocks of the **stored head**; depth is capped at 256 (the
   QMDB undo window).

## Blocker found

Both qs-node0 and qs-node3's chaindata were stopped at fleet shutdown
20:09:16 EDT 2026-10-03, stored head **13674185**. `qs-replay --replay.scan
--replay.depth 256` against either reflinked copy shows **all 256 blocks in
the undo window have txs=0** — the chain idled (decaying baseFee, no flood
traffic) for thousands of blocks after the measured round ended.

Cross-checking node3's own "parallel block" log lines
(`/data/blockchain/qs-node3/log/n42-2026-10-03T19-30-13.515.log.gz` +
`n42-2026-10-03T20-07-32.628.log.gz` + `n42.log`) for `txs>=100000`: the full
163k-tx blocks referenced in the task (B1 19:31-19:43, B2 19:44-19:57) span
block numbers **13656001 to 13669276**. That is **4,909 to 18,184 blocks**
behind the current stored head (13674185) — far outside `qs-replay`'s 256-block
undo window. The tool, as written, can only rewind+replay the most recent 256
blocks of whatever chain tip the datadir holds; the fleet kept running (and
producing empty blocks) for ~4,900+ more blocks after the heavy round, so the
heavy blocks are no longer reachable by this tool against this datadir without
a deeper restore (snapshot/era import to an earlier height, which was out of
scope here and was not attempted to stay within the "no fleet round, offline
single-node measurement" instruction).

No CPU profile was taken: running the import path against the only blocks
`qs-replay` can reach (txs=0, trivial empty-block fast path) would not produce
the exec-wave / finalize data this task asked for, and would misrepresent the
uncontended full-block numbers if reported as such.

## What would unblock this

- A qs-node reflink taken **immediately** after a heavy round (before the
  fleet is left running on empty blocks for thousands more), or
- `qs-replay.depth` extended past 256 (requires widening the QMDB undo
  window retention, a code change — out of scope for a measurement-only
  task), or
- Restoring a datadir copy to a height inside [13656001, 13669276] from a
  snapshot/era archive if one exists, then running `qs-replay` from that
  restored tip.

## Board row

See docs/QS_QUEUE.md S79. Recommend re-queuing as S79-retry: snapshot/claim
the fleet's datadir **right after** the next heavy round finishes, before
the fleet is left idling, so the full-tx blocks stay inside the 256-block
undo window for `qs-replay`.

## Disk/cleanup

- `/data/blockchain/qs-replay-node0` (35G) and `/data/blockchain/qs-replay-node3`
  (36G) reflink copies kept per instructions (xfs reflink, near-zero marginal
  disk cost over the source until pages diverge).
- `/data/blockchain/gov5-work/.box-claim-s79` removed at end of run.
