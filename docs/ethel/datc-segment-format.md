# DATC Segment Format — time-axis segments + key hint index

Status: design (2026-06). Supersedes the key-bucketed `leafseg/` presentation
for the *final delivered* DATC artifact. The build (`n42-datc build`) is
unchanged; this is a packaging/serving format that a `pack` step produces from
the build's records (or that the spill writer targets directly — see Migration).

## 1. Goals & the one decision that drives everything

Requirements:
- **~2 GB freezer-like segments** (operator-familiar, mmap/stream-friendly).
- **A weekly update modifies only the *last* file** — historical files are frozen.
- **BitTorrent-friendly**: sealed segments are immutable & content-addressed, so
  a peer downloads each once and only the small active tail changes.
- **Point query stays cheap**: `proof-as-of(addr, slot, block)`.
- **Trustless distribution**: a downloaded segment is verifiable without trusting
  the seeder (on-chain `segment_root`).

The decisive constraint: a week of new blocks touches keys whose hashes are
**uniform over the whole key space**. Therefore ANY scheme partitioned by *key*
(today's 256 `leafseg` buckets) has *every* partition touched every week → every
file rewritten → every infohash churns → BT dead. The fix is not the segment
count or size (256 × ~2 GB is fine) — it is the **partition axis**:

> **Partition by TIME (block range), not by key.** A week's writes land in ONE
> segment (the active tail); all earlier segments are frozen.

Key locality (needed for queries) is recovered *inside* each segment (records
sorted by key) plus a small mutable hint index. Same ~256-segment shape, just a
different axis.

## 2. Data types carried

Per the build (`cmd/n42-datc`), six record streams + meta:

| stream    | record (logical)                         | query                         |
|-----------|------------------------------------------|-------------------------------|
| `leafA`   | (hashedAddr, block) → account V2 bytes   | account value as-of block     |
| `leafS`   | (hashedAddr‖inc‖hashedSlot, block) → val | storage value as-of block (BIG)|
| `nodeAcc` | (path, epoch) → MarshalTrieNode bytes    | account-trie node as-of block |
| `nodeSto` | (addr‖inc‖path, epoch) → node bytes      | storage-trie node as-of block |
| `chgAcc`  | (depth, path, epoch) → changed-block list| which child changed when      |
| `chgSto`  | (depth, addr‖path, epoch) → block list   | (window selection for proofs) |
| `meta`    | head / sched / segment manifest          | —                             |

`leafS` dominates total size; it drives the ~2 GB seal trigger.

## 3. On-disk layout

A **period** = a contiguous block range `[b0, b1)`. One directory per period
holds all six streams plus their indexes, sealed together under one
`segment_root`.

```
datc/
  MANIFEST.json                 # ordered period list (see §7)
  hint.mdbx/                    # mutable key→latest-period accelerator (§6)
  p000000-000000/               # period 0  (b0=0)
  ...
  p015728640-015925000/         # a sealed historical period  [15728640,15925000)
    leafA.dat  leafA.idx
    leafS.dat  leafS.idx        # leafS.dat ≈ 2 GB → this is what sealed the period
    nodeAcc.dat nodeAcc.idx
    nodeSto.dat nodeSto.idx
    chgAcc.dat  chgAcc.idx
    chgSto.dat  chgSto.idx
    SEAL                        # segment_root + per-file hashes + [b0,b1) (§8)
  p015925000-OPEN/              # the ACTIVE TAIL — the ONLY mutable period
    leafS.dat                   # block-appended, not yet key-sorted
    ...                         # no .idx / no SEAL until sealed
```

Periods are **size-driven** (freezer-style): the active tail seals when its
`leafS.dat` reaches the target (~2 GB) *or* at a calendar boundary (month),
whichever first. `b1` is recorded at the moment of sealing. ~256 periods ≈ the
whole chain at this size; finer/coarser is a single constant.

### 3.1 `.dat` (sealed)

zstd frames (~256 KiB raw each, independently decodable for partial fetch). Each
frame is a run of records; a record is `uvarint(keyLen) ‖ key ‖ uvarint(valLen)
‖ val`. The **full key embeds the block/epoch suffix**, so records are
totally ordered by `(logicalKey, block)` and a key's versions are contiguous.
Records within a sealed segment are **sorted by full key** (key-major *within the
period*) for locality and proof streaming.

### 3.2 `.idx` (sealed) — RecSplit + bloom

Reuse the existing N42 cold-store index (`cmd/coldstore-*`, the monthly-RecSplit
history tooling):
- **RecSplit / MPHF**: `hashedKey → byte offset of the key's first record in
  .dat`. No stored keys, no fingerprint table — collisions/identity are settled
  by the on-chain `segment_root` commitment (see [[project_recsplit_no_fingerprint]]).
- **Bloom filter** in the idx header: `key ∈ this period?` in O(1), ~1 byte/key,
  ~1% FP — lets a query skip a period without touching `.dat`.
- A key present in the period may have several versions; they are contiguous from
  the RecSplit offset, scanned to the floor `block ≤ N`.

### 3.3 Active tail (`p..-OPEN`)

Pure **block-append**, no sort, no RecSplit, no bloom: each block's records are
appended to `<stream>.dat` as the build produces them (it already emits in block
order). This is what makes a weekly update a tail append. Queries against the
unsealed tail use a tiny in-RAM/MDBX side index (the hint index doubles as this);
the tail is small by construction (≤ one period).

## 4. Weekly update — touches only the tail

```
1. n42-datc build (resume, higher --end)   # processes only new blocks
2. pack new records → APPEND to p..-OPEN/<stream>.dat   # tail grows, O(week)
3. update hint.mdbx: touched keys → OPEN period         # small, mutable
4. if tail leafS.dat ≥ 2 GB (or month boundary):
      SEAL: sort tail by key → write .idx (RecSplit+bloom) → compute segment_root
            → rename p..-OPEN → p<b0>-<b1> (immutable) → start fresh p<b1>-OPEN
```

Frozen periods are **never reopened**. Steps 2–3 are O(week); step 4 (seal) is a
once-per-period O(period) job, not O(total history). Contrast with the current
key-bucket format whose every update is O(total) and re-touches all 256 files.

## 5. Query — `proof-as-of(addr, slot, N)`

For each needed key (the leaf + each trie-node path key):

```
candidates = periods with b0 ≤ N, NEWEST first   (binary search MANIFEST)
for p in candidates:                              # incl. the OPEN tail if b0≤N
    if not p.bloom.maybe(key): continue           # O(1) skip
    off = p.idx.lookup(key); if miss: continue
    rec = floor version with block ≤ N at/after off
    if rec found: return rec                       # newest period wins
```

The **first** period (newest, `b0 ≤ N`) that holds the key with a version `≤ N`
is the answer — later periods only hold later blocks. Hot/recent keys resolve in
the newest period immediately; cold keys walk back, each step an O(1) bloom test
(≈100–256 max). A proof needs leaf + ~depth nodes → that many newest-first scans.

## 6. Hint index — restore O(1) for tip/recent queries

`hint.mdbx`: `hashedKey → latest period id` (the most recent period that touched
the key). Mutable, tiny, rebuilt incrementally each week (step 3); **not part of
the BT artifact** (it is the hot tier).

- **Tip query** (`N = head`): `hint[key]` → that period directly, skip the scan → O(1).
- **Historical query** (`N < head`): hint gives an upper bound; start the §5 scan
  at `min(hint[key], period_of(N))`. Still bounded, usually 1 hop.
- Optional stronger variant for heavy historical workloads: `key → sorted period
  list` (erigon-style inverted index) → O(log periods) for any `N`. Bigger, still
  mutable/local. Start with the single-latest hint; upgrade only if profiling
  shows cold-key scans dominate.

Losing `hint.mdbx` is non-fatal: it is a pure accelerator, rebuildable from the
sealed `.idx` blooms.

## 7. MANIFEST.json

```json
{
  "version": 1,
  "head": 25311094,
  "sched": "...",
  "periods": [
    {"id":"p000000-000000","b0":0,"b1":196000,"sealed":true,
     "segment_root":"0x…","files":{"leafS":{"bytes":2106432001,"hash":"0x…"}, …}},
    …,
    {"id":"p015925000-OPEN","b0":15925000,"b1":null,"sealed":false}
  ]
}
```

The sealed entries are append-only; only the trailing `OPEN` entry mutates until
it seals (then a new `OPEN` is appended). `segment_root` is the Merkle root over
the six per-file content hashes (see §8) — the single value to anchor on-chain.

## 8. Trust & verification (BT-safe)

`SEAL` per sealed period:
- `fileHash[stream] = keccak256(<stream>.dat ‖ <stream>.idx)` (or blake3; match
  the cold-store tool).
- `segment_root = Merkle(fileHash[leafA..chgSto] ‖ b0 ‖ b1)`.
- Anchor `segment_root` per period on-chain (the existing `segment_root`
  commitment path) → a downloaded period is verifiable against chain state with
  zero trust in the seeder. A proof served from a period also carries the
  period's `segment_root` so a light client checks provenance.

## 9. BitTorrent propagation

- Each **sealed period directory** is an immutable content-addressed unit → one
  stable infohash. Seed once; peers dedup and 1-of-N seed (reuse the validated
  cold-segment torrent path, [[project_body_compression_eip4444]]).
- A weekly update changes **only the OPEN tail** → re-seed just that small dir;
  on seal it becomes another immutable seed and a fresh tiny tail starts.
- `MANIFEST.json` (+ on-chain `segment_root`s) is the lightweight thing peers
  fetch to discover & verify the period set.

## 10. Trade-offs (honest)

| axis            | key-bucket (current) | time-segment (this)              |
|-----------------|----------------------|----------------------------------|
| weekly update   | O(total), all files  | **O(week), tail file only**      |
| BT              | dead (all churn)     | **sealed = immutable, seed once**|
| point query     | O(1) bucket seek     | O(periods) bloom; **O(1) via hint** for tip; O(log) with inverted variant |
| seal/compaction | n/a (always rewrite) | once per period, O(period)       |

The only thing given up vs key-buckets is worst-case cold-historical point query
(bounded by #periods, cut by bloom + hint). For an occasionally-served historical
proof DB that wants cheap weekly updates AND BT, that is the right trade.

## 11. Reuse / migration

- **Reuse**: the RecSplit+bloom `.idx`, `segment_root` commitment, and torrent
  cold-segment seeding already exist (monthly-RecSplit history tooling,
  `cmd/coldstore-*`, [[project_recsplit_history]], [[project_state_tiering_design]]).
  This design = apply that to ALL six DATC streams (today only leaf VALUES are
  segmented; `nodeAcc/nodeSto/chgAcc/chgSto` must be segmented the same way).
- **Migration from `leafseg/` (key buckets)**: a one-time `pack` pass reads the
  current key-bucketed segments + MDBX node/chg tables and re-emits them as
  time-periods (it already has every record sorted; it re-partitions by block
  range and builds per-period RecSplit+bloom). After that, the build's spill
  writer targets the active tail directly (append in block order), and seal runs
  at the per-batch frame-cut boundary when the tail crosses the size target.
- **No change to the build's correctness path**: gold-check, leaf/node/chg record
  bytes, and the verifier fold are all unchanged — only where the bytes land.

## 12. Open items

- Pick seal trigger: size-only (~2 GB) vs size-or-month. Month aligns
  `segment_root` cadence with the existing tiering; size keeps files uniform.
  Recommended: **size-primary, month as a secondary cap** (so a quiet month does
  not leave a tiny dangling period and a dense month splits at ~2 GB).
- Decide hint granularity: single-latest (start here) vs inverted period-list
  (upgrade if cold-key historical queries dominate).
- Finalize hash function (keccak vs blake3) to match the cold-store tool so the
  `.idx`/`segment_root` code is shared verbatim.
