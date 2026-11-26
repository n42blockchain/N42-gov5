# P1 v1.1 state sync at chain-94 scale: feasibility (gov5)

Date 2026-11-26 (America/New_York). Branch `feat/p1-statesync-scale` (from main ecaa51f7).
Scope after the 2026-11-26 direction change: measure, assess, do not implement the redesign.
Wire stays exactly as published (`qs-scratch/profile1/vectors/sync-v11/`); nothing below needs a wire change.

## 1. The chain-94 state that was measured

Source: the newest replay-v2 output on this host, the qs era seed `/data/blockchain/qs-era-linux`
(rebuilt 2026-10-15 from `qs-replay-hist-20261014`; `SEAL_DONE.json`, `wr-logs/seed-rebuild-20261014.log`,
memory note project-qs-seed-rebuild-20261016). Genesis `0xa2d2ff5d…be99`, chain id 94.
Measured on a reflink copy (`gov5-work/p1scale/seed`), never on the seed itself; no process had it open.

| | value | source |
|---|---|---|
| source head (old chain 94 datadir) | 14,066,100 | `qmdbBlockPos` rows; docs/QS_SEED_REBUILD.md |
| target head (replay, incl. 39,398 gap-fill empties) | 14,105,498, hash `0xa7f2c0a1…c269` | export |
| state root | `0xdc3e8af2…7473` = forest root, equals the target header root (exporter checks it) | n42-qmdb-export |
| accounts | 6,449,084 | Account table, export, reth dump |
| storage slots | 12,746 | Storage table |
| contracts (code rows) / code bytes | 19 / 70,931 B | Code table (n42-statesync-measure) |
| QMDB entries appended (next_slot) | 66,054,384 | forest |
| live / dead entries | 6,461,830 / 59,592,554 (9.8% live) | forest |
| twigs | 32,254 (596 hollow = all-dead, 31,658 hold at least one live slot) | measure |
| chaindata on disk | 66.6 GB mdbx.dat (+7.9 GB sealed era files, +4 MB txindex) | ls |

Old-chain header roots are not comparable (different trie); the replay re-executes every tx and checks
the QMDB root per batch (docs/QS_WEEKLY_REPLAY_SYNC.md).

Identical-state artifacts for all three clients (`/data/blockchain/gov5-work/p1scale/out/`, SHA256SUMS there):
- `chain94-v2.qmdb` portable tombstone v2 (N42QMDB\x03), 2,723,416,715 B, sha256 `4feddec0…411d83`
- `chain94-state.jsonl` reth init-state, 589,386,023 B, sha256 `004dc5aa…93bda2`; header RLP sha256 `e75cf716…07f565`

## 2. Served bytes and server build cost (today's materialising server)

`cmd/n42-statesync-measure` (this branch) builds the snapshot exactly as `v11.BuildSnapshot`
(server.go:70) and encodes every reply a whole-pivot client receives, framed as the server writes them.
Run on the copy, GOMAXPROCS 16, nice 10, cold page cache (`gov5-work/p1scale/logs/measure.log`):

| reply | bytes on the wire |
|---|---|
| qmdb_status/1 | 26 |
| qmdb_manifest/1 (8 pages x 4096 twigs) | 2,049,925 |
| qmdb_twigs/1 (504 requests x 64 twigs, max chunk 74,816) | 2,143,451,844 |
| code_by_hash/1 (19 codes) | ~71 KB |
| **total** | **~2.15 GB** |

Records: 6,461,830 LIVE_P, 0 LIVE, 58,371,946 DEAD (33 B each = ~1.93 GB, 90% of the payload).
Payload time: 2.9 min at 100 Mbit/s, 17 s at 1 Gbit/s.

Build of ONE served snapshot: 175.5 s wall (forest load 99 s cold, live-value verify 7 s, portable
encode 47 s, decode to twigs 14.5 s, preimages 7.5 s), **peak RSS 34.5 GB**, ~6.5 GB read, no disk written.
For comparison the streaming exporters on the same copy: n42-qmdb-export v2 2m29s / 6.9 GB RSS;
n42-reth-state-dump 2m53s / 7.0 GB RSS.

## 3. Full sync from genesis (replay-v2 throughput, the alternative)

Measured replay-v2 runs (offline: local source datadir, no p2p, no QC checks):

| run | blocks | txs | wall | rate | peak mem |
|---|---|---|---|---|---|
| Linux 2026-08-23 (`/data/blockchain/replay_v2_stats.json`) | 1..13,204,049 | 25.2 M | 31m51s | 6,910 blk/s, 13.2 k tx/s | not recorded |
| Windows full replay (QS_WEEKLY_REPLAY_SYNC.md) | 13,612,974 | 26.2 M | 1h33m47s | 2,419 blk/s | 28.76 GB private; base 28 GB |
| Windows increment 2026-08-10 (stress era) | 13,204,050..13,912,188 | 128.7 M | 52m46s | 224 blk/s, 40.7 k tx/s | — |

The 2026-10-14 run that produced the seed left no log on this host; its throughput is not known.
Estimate genesis -> 14.07 M on this box: ~32 min (empty/light era) + ~53 min (stress era) + the
154 k blocks after 13.91 M (not separately measured) = **~1.5-2 h of execution, ~29 GB peak**, 66.6 GB
chaindata at the end. Block data to fetch is of the order of the era files (7.9 GB for 0..13.63 M):
~11 min at 100 Mbit/s, overlappable with execution. Range sync inside the node (bodies_by_range +
import + QC checks) was NOT measured at this length; it is slower per block than the batch replay by
an unknown factor.

## 4. Pivot lifetime vs download time (current code)

`Server` keeps `cur` and `prev` (server.go:191-192); refresh when head >= cur.Pivot + 512 (:242),
build ~3 min, then cur -> prev. A pivot chosen just before a refresh stays servable for at least
512 blocks + one build: **~68 min at an 8 s slot, ~26 min at 3 s**. Download + verify of 2.15 GB:
~3-6 min at 100 Mbit/s, < 1-2 min at 1 Gbit/s. A transfer only outlives its pivot below
~4.2 Mbit/s (8 s slot) or ~11 Mbit/s (3 s slot): probability ~0 on any normal link. The real risk
is not expiry but **no resume**: join.go:92 holds everything in memory and any failure after the
last source drops restarts from zero.

## 5. What state sync needs at this scale, and cost

Blocking:
1. **Server memory.** 34.5 GB per materialised pivot, two retained = ~70 GB on a validator: not viable.
   Replace `Snapshot.Twigs` with a pinned view: per retained pivot keep only twig bits + roots
   (256 B + 64 B per twig = ~10 MB for 32 k twigs) and read records on demand from the
   append-only `qmdbEntries` rows (slot -> keyHash||value; a dead slot's leaf hash is recomputed
   from its row). Preimages: a keyHash -> preimage lookup (6.46 M entries, ~0.5-1 GB in memory, or a
   ~340 MB table). Pin = a refcounted pivot record, K = 3 (cur + 2 previous, ~2 h at 8 s), released
   when its last stream ends or after a lease. No long MDBX read txn needed (old txns would block
   page reuse for the whole pivot lifetime). Requires that dead entry rows are not reclaimed
   while a pivot that needs them is pinned (true on this seed: full history kept).
   Est. 4-6 engineer-days incl. tests.
2. **Client memory + resume.** FetchBundle keeps all 66 M records as Go structs (same order as the
   server, est. 25-35 GB) and then assembles a 2.7 GB portable file. Stream each verified twig to
   disk with a progress bitmap; on restart re-read the manifest and refetch only missing twigs; on a
   pivot switch keep every twig whose (leaf_root, bits_root) is unchanged in the new manifest (at
   chain-94 churn nearly all sealed twigs are unchanged between pivots 512 blocks apart).
   Est. 4-5 engineer-days incl. interrupted-download and pivot-rollover tests.
3. Not measured: bundle import of 6.45 M accounts on the client (snapshotjoin.Import) at this size.

Optional, big win: QMDB compaction (re-append live entries so old twigs become hollow) would cut
the payload from 2.15 GB to roughly 0.3 GB (live records only), since 90% of the bytes are dead
leaf hashes. That changes the forest/root history, so it is a chain-level decision, not a sync one.

## 6. Verdict

- Bandwidth and pivot lifetime are not the problem: 2.15 GB, minutes at 100 Mbit/s, inside a
  >= 26-68 min pivot window.
- The current implementation does not scale: 34.5 GB server RSS per pivot and an all-in-memory,
  non-resumable client. ~2 engineer-weeks to fix without wire changes (section 5).
- Full sync from genesis is workable for chain 94 today: ~1.5-2 h of execution (offline replay
  rate) plus ~10 GB of block data, ~29 GB peak memory; its in-node range-sync rate is still to be
  measured. Deferring p2p state sync is reasonable at this state size; it becomes worth it once the
  chain or its churn grows past what a ~2 h replay covers.
