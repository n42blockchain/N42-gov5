# QS write-phase budget: what the history switch disables, and what fills the ~190-227 ms write phase

Resolves the apparent contradiction between docs/QS_DISK_WORKING_SET.md's
117.6 ms/block WriteHistory claim (35zzzau), S70's null A/B result on
N42_NO_HISTORY_INDEX (docs/QS_QUEUE.md row S70, round 35zzzaw), and S72's
builder's-doubt that the QMDB apply inside ComputeRoot is only ~56 ms, not
the 214 ms the write phase shows (docs/QS_QUEUE.md row S72,
docs/QS_BLOCK_TIME_BUDGET.md 6h).

## (a) What N42_NO_HISTORY_INDEX actually turns off

`modules/state/history_index_mode.go:56-61` (`HistoryIndexDisabled`) gates a
single call site: `modules/state/plain_state_writer.go:252-264`
(`PlainStateWriter.WriteHistory`):

```go
func (w *PlainStateWriter) WriteHistory() error {
	if HistoryIndexDisabled() || HistoryIndexDeferred() {
		return nil
	}
	if w.csw != nil {
		return w.csw.WriteHistory()
	}
	return nil
}
```

`WriteChangeSets` (`plain_state_writer.go:230-236`) has no such gate and
always runs. So the switch disables ONLY the AccountHistory/StorageHistory
inverted-index write (the roaring-bitmap RMW); it never touches the
changesets themselves, which `internal/blockchain_write.go:334`
(`stateWriter.WriteChangeSets()`) writes unconditionally right before
`internal/blockchain_write.go:339` calls `stateWriter.WriteHistory()`. The
only other caller, `internal/api/api.go:386`, uses the same flag purely to
refuse historical RPC queries when the index is off — a read-path gate, not
a write-path one.

**Measured on node1 of r35zzzaw, B1 leg (index ON, unset), 17:45-17:51,
183 high-tx blocks (txs >= 50,000, median 163,000):** `chgHist` (the
WriteHistory call) has median 0.001 ms and p90 0.001 ms — effectively free,
even with the index genuinely enabled. This is consistent with S70's ruling
("the history index costs nothing measurable at 11 GiB" — n42-r106's
standing GOMEMLIMIT/cache config keeps the working set resident, so the
roaring-bitmap RMW never pays the random-read-from-disk cost that
35zzzau's number was measured under). **The switch does exactly what it
says (drops the inverted-index write only); it just has nothing to save at
this fleet's cache size, which is why S70 found no A/B signal.**

## (b) What is actually in the follower's write phase

`internal/blockchain_write.go`'s write-phase timers span from
`tUpdateStart := time.Now()` (just before `bc.ChainDB.Update(...)`, line
~232) to `dUpdate := time.Since(tUpdateStart)` (line ~596, right after
`Update` returns) plus a small `dPost` for QMDB commit bookkeeping
(`CommitFlushed`/`EvictFlushed`/`TakeUndo`, lines ~597-604). `dCommit` is
derived (`dUpdate - dBegin - dInClosure`) because MDBX's fsync is not
separately instrumented. All of this becomes the single `"write"` field of
the parent `"blockimport phases"` log line.

Order of operations inside the closure, with node1/B1 high-tx-block
(txs>=50,000, n=183) **median / p90**, from
`/data/blockchain/wr-logs/r35zzzaw-keep/node1/n42-2026-10-02T18-05-16.254.log.gz`,
`"blockwrite phases"` and `"qmdb root phases"` lines:

| order | field | op | median ms | p90 ms |
|---|---|---|---|---|
| 1 | `receipts` | `rawdb.AppendReceipts` + `rawdb.WriteLogIndex` | 12.8 | 18.8 |
| 2 | `block` | `rawdb.WriteBlock` (header/body/td) | 50.0 | 78.7 |
| 3 | `ce` | consensus evidence write | 0.45 | 0.66 |
| 4 | `state` (`dState`) | `ibs.CommitBlock` — plain-state account/storage writes | 17.5 | 25.4 |
| 5a | `chgTrunc` | `changeset.Truncate` (re-import safety) | 0.015 | 0.024 |
| 5b | `chgSets` | `stateWriter.WriteChangeSets()` | 16.4 | 23.5 |
| 5c | `chgHist` | `stateWriter.WriteHistory()` (the N42_NO_HISTORY_INDEX switch) | 0.001 | 0.001 |
| 6 | `root2` | leader-only isolated QMDB replay (0 on follower/import role) | 0.0 | 0.0* |
| 7 | `qflush` | QMDB positional-entry-log + twig-meta flush to the MDBX tx | 13.2 | 20.1 |
| 8 | `qmeta` | QMDB evict/undo bookkeeping tied to the flush | 33.2 | 49.1 |
| 9 | `snap` | snapshot-tree diff update (disabled here, 0) | 0.0 | 0.0 |
| 10 | `commit` (derived) | MDBX `Update`'s commit/fsync | 17.5 | 35.1 |
| 11 | `post` | `CommitFlushed`/`EvictFlushed`/`TakeUndo` | 0.34 | 0.61 |
| — | **sum of 1-11 (median path)** | | **161.3** | **251.9 (p90 col sum; tails don't add linearly)** |
| — | **measured `total` (blockwrite)** | | **174.4** | 433.5 (full tail, dominated by GC/tx-size outliers) |
| — | **measured `write` (blockimport, parent field)** | | **171.5** | **227.2** |

\* `root2` p90 is 51.6 ms over all 183 rows because a handful of these blocks
ran role=leader (isolated-seal replay); on pure-follower rows it is 0.
`lib/qmdb` apply itself (`"qmdb root phases"`, `applyNs`, same window) is
median 24.9 ms / p90 34.8 ms — this is the number S72 cites as "~56 ms at a
median 16k ops" in a different (lower-tx) sample; at this window's
163k-tx/1008-op-batch blocks it is smaller, and it is NOT the same thing as
`qflush`+`qmeta` (33.2+13.2=46.4 ms median), which are the separate
flush/evict/undo bookkeeping that happens after apply, inside the write
transaction, every block regardless of role.

The median-column sum (161.3 ms) is within 8% of the measured median write
(171.5 ms); the gap is BeginRw wait and other small uncounted slivers. **The
historical 190-214 ms figures in the queue docs are this same sum pushed up
by the WriteBlock (`block`) and QMDB flush/evict (`qflush`+`qmeta`) tails,
not by WriteHistory, which is unconditionally ~0.**

## (c) Largest two items, and critical-path status

1. **QMDB flush + evict/undo bookkeeping (`qflush`+`qmeta`), median 46.4 ms,
   p90 69.2 ms** — persisting the positional entry log and twig metadata for
   the block just applied, and consuming the undo record. This is pure
   persistence/crash-recovery plumbing for the NEXT block's revert path; it
   does not feed the vote.
2. **`rawdb.WriteBlock` (`block`), median 50.0 ms, p90 78.7 ms** — header,
   body, and total-difficulty rows. Also pure persistence: the proposal and
   vote reference the block/header the executor already built in memory.

**Neither is on the follower's critical path for the deferred-execution
vote.** `docs/QS_BLOCK_TIME_BUDGET.md:2305-2308` states it directly for the
follower's r2 phase: "the follower's proc: recover 110, setup 116, exec 338,
apply 31, finalize 210 -- the write, 241 ms, is already off r2: the executed
hook casts the vote before the write." The same section gives the leader's
own write (224-264 ms) as "off the critical path" for its proposal send.
`docs/QS_BLOCK_TIME_BUDGET.md:4545-4551` separately confirms the Round-2
gate accepts the deferred attestation (computed from `state`/root, not from
the persistence writes) so the held commit vote fires off that attestation,
not off `WriteBlock`/`WriteChangeSets`/`WriteHistory`/QMDB-flush/commit. Of
the ordered list above, only step 4 (`state`, `ibs.CommitBlock`, which
produces the root the deferred check validates) has any bearing on the
vote; steps 1-3 and 5-11, including both of the two largest items, are
persistence-only.

## Sources

- `modules/state/history_index_mode.go:1-96`
- `modules/state/plain_state_writer.go:230-266`
- `internal/blockchain_write.go:206-230,250-340,596-648`
- `internal/api/api.go:386`
- `docs/QS_QUEUE.md` rows S70, S72
- `docs/QS_BLOCK_TIME_BUDGET.md:2295-2312,1799,4532-4595`
- `/data/blockchain/wr-logs/r35zzzaw-keep/node1/n42-2026-10-02T18-05-16.254.log.gz`,
  `"blockwrite phases"` / `"qmdb root phases"` / `"blockimport phases"` lines,
  17:45-17:51 (B1), filtered to `txs >= 50000` (n=183/156)
