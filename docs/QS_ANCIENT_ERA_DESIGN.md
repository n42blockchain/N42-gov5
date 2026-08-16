# QS Native Chain — Ancient Era Offload Design

Status: **implemented and deployed** (2026-08-15). Code:
`modules/rawdb/ancientera` (container + store + manifest),
`modules/rawdb/ancient_fallback.go` (read tier), `cmd/n42-ancient-seal`
(offline seal + hot emit), `cmd/n42-ancient` (ls/verify/fetch/prune).
Scope: the n42 native chain (hotstuff + QMDB, `--chain private` / qs
fleet). The eth-el freezer (`modules/rawdb/freezer`, geth-compatible
cidx/cdat) is **not** changed by this design.

First production run (2026-08-15, qs fleet at head 14,210,892):
13 eras sealed in ~13 min (39 files + manifest, 17 GB — the sealed
ranges held ~38 GB in MDBX); hot MDBX emitted at 8.0 GB (from 49 GB,
6×); scrub + deep verify (832 blocks byte-compared against the source)
clean; fleet reseeded and producing with era reads live on all 7 nodes.

## 1. Motivation — measured data shape

Per-table stats from the qs fleet seed base (E:\qs-replay-v4, head 13,912,188,
46.1 GB net / 49.4 GB file):

| Class | Tables | Size | Share |
|---|---|---|---|
| Append-only, immutable (blockNum / sequence keyed) | BlockTransaction 20.6G, BlockWitness 5.2G, Header 4.2G, AccountChangeSet 3.1G, ConsensusEvidence 2.1G, Receipt 1.8G, BlockBody 0.8G, CanonicalHeader 0.7G, TransactionLog 0.4G, StorageChangeSet 0.1G | 39.0 GB | 85% |
| Immutable, hash-keyed | HeaderNumber | 1.3 GB | 3% |
| QMDB commitment (append-mostly by design) | qmdbTwigLeaves 2.1G, qmdbIndex 0.6G, qmdbEntries 0.4G | 3.1 GB | 7% |
| History inverted index | AccountHistory 0.9G, StorageHistory 10M | 0.9 GB | 2% |
| Truly random-update hot state | Account 0.4G, Storage 1.2M, Code 100K | 0.4 GB | ~1% |

~90% of the database is write-once data that MDBX gains nothing from holding.
Under txgen load essentially 100% of growth lands in the append tables
(last weekly increment: +708k blocks / 128.7M txs → +21 GB, ~18 GB of it
BlockTransaction alone).

Cautionary precedent: the eth-el freezer's geth-style 2 GB rotation has
accumulated **522 files** plus manual repair sidecars (`.bak-holefix`,
`.bak-gap`). This design explicitly optimizes for a small, self-describing,
individually verifiable file set instead.

## 2. Requirements

1. **Few files, low management burden.** File count grows by a handful per
   year; every file's identity, range, and content class are readable from
   its name and its own metadata.
2. **Self-describing versioned files.** Every sealed file carries a metadata
   block: format version, codec versions, chain identity, block range,
   content hashes. A file picked up in isolation (copied, torrented,
   restored from backup) can be fully identified and verified without any
   external context.
3. **Corruption and deletion tolerance.** Damage is detected (at open, at
   read, and by periodic scrub), never silently served. A damaged or deleted
   optional file degrades to "range pruned" semantics; the node keeps
   running.
4. **Consensus safety floor.** Headers + consensus evidence (HotStuff QC)
   are the mandatory class — the permanent record that the chain finalized
   what it finalized. They are always retained and verified; everything else
   (bodies, receipts, witnesses, changesets) is optional history that may be
   missing and is then treated as pruned.
5. **Recoverability.** Sealed files are byte-deterministic: the same chain
   range always seals to the same bytes. Recovery = copy from any peer (or
   regenerate via replay) and verify against the manifest hash.

Non-goals: QMDB tables stay in MDBX (live commitment structure, already
append-friendly internally). Hot state (Account/Storage/Code) stays. No
online migration of the existing 13.9M blocks — rollout rides the weekly
replay-reseed cycle (§10).

## 3. Era container — one file per (class, block range)

Inspired by Ethereum's era1 history-expiry format (immutable, self-contained,
verifiable archives), adapted to qs chain content.

- **Era span**: fixed power-of-two block range, default **2^20 = 1,048,576
  blocks**. Era `k` covers `[k·2^20, (k+1)·2^20)`. The span is recorded in
  the manifest and every file footer; all eras in one store share one span.
  Changing span = new store generation (offline regeneration), never mixed.
- **Naming**: `<class>-<k:08d>-<firstHash8>.era`, e.g.
  `chain-00000013-7686bdd7.era`. The 8-hex-byte first-canonical-hash suffix
  makes stale files from an abandoned fork generation visually distinct.
- **Classes** (three files per era, one file is the unit of pruning):

| Class | File | Content (per block) | Policy |
|---|---|---|---|
| A | `chain-*.era` | canonical hash, header (compact codec), ConsensusEvidence (QC + mobile BLS) | **mandatory** — never auto-pruned; verified at boot |
| B | `exec-*.era` | body with inlined transactions, senders, receipts, logs | optional — prunable, missing ⇒ pruned |
| C | `aux-*.era` | block witness, account changeset, storage changeset | optional — first to prune |

At measured density an exec era is roughly 0.5–1 GB compressed; at sustained
peak txgen density (~182 tx/block) up to ~5–9 GB — still a single file, no
rotation. Today's chain = 14 eras × 3 classes = **42 files + 1 manifest**;
steady-state growth ≈ 8–30 eras/year depending on block interval.

### 3.1 Internal layout (e2store-style TLV)

```
[header record]
  magic "N42E" | u16 format_version | u16 class | reserved
[data records]           — one per 64-block batch (freezer.BatchSize reuse)
  type=DATA | u32 len | zstd frame (concatenated per-block entries,
                                     length-prefixed, compact codecs)
[index record]
  type=INDEX | per-block (u32 frame_ordinal, u32 intra_offset)
             | per-frame (u64 file_offset, u64 xxh3 of compressed frame)
[meta record]            — the "version verification description" block
[footer]
  type=FOOTER | u64 meta_offset | u64 index_offset | blake3-256 of all
  preceding bytes | magic "N42e"
```

Readers seek the footer (fixed tail size), then the index — O(1) open,
O(1) block lookup, mmap-friendly. Per-frame decompression is bounded
(64 blocks), never whole-file heap decompression.

### 3.2 Meta record — versioned self-description (required)

CBOR/JSON map, additive-only fields; readers reject unknown **major**
format_version, ignore unknown keys:

| Field | Meaning |
|---|---|
| `format_version` | era container version (this spec = 1) |
| `class`, `era`, `span` | A/B/C, era number, blocks per era |
| `chain_id`, `genesis_hash` | chain identity — refuse cross-chain files |
| `block_range` | `[start, end)` actual (last era of a generation may be full-span only; partial eras are never sealed) |
| `first_hash`, `last_hash` | canonical hashes at range edges |
| `parent_hash` | parentHash of first block — chains era k to era k−1 |
| `accumulator` | blake3 over the era's ordered canonical hashes |
| `codecs` | map: header/tx/receipt/evidence/changeset codec versions, zstd level, dictionary id (pinned for determinism) |
| `payload_blake3` | hash of all data records (pre-index), the manifest cross-check value |
| `creator` | tool name + build version + creation time + source (live-seal / replay-v2 emit) |

`creator` is descriptive only and excluded from determinism checks; all
other fields and all data/index bytes are deterministic (§8).

## 4. Manifest — single source of truth per store

`ancient/MANIFEST.json`, atomic rewrite (temp + rename), and its blake3 is
stored in MDBX (`SyncStage`-adjacent key) after every rewrite — a stale,
truncated, or tampered manifest is detected at boot and rebuilt from the
era footers (footers are self-authenticating; the manifest is a cache +
prune ledger, not the root of trust).

Per era entry: class, era number, file name, size, `payload_blake3`,
`status: sealed | pruned`, pruned-at timestamp. Plus store-level: span,
chain id, genesis hash, generation id (first seal time + base head hash).

## 5. Retention policy

- **Freeze lag**: seal era k only when `finalized head ≥ (k+1)·span +
  threshold` (default 90,000 blocks, `AncientFreezeThreshold`). HotStuff
  commit is final ⇒ no reorg can ever reach a sealed era.
- **MDBX keeps**: all state + QMDB, CanonicalHeader, HeaderNumber, MaxTxNum,
  SyncStage/progress, and the unsealed recent window of everything else.
  (CanonicalHeader/HeaderNumber stay hot for navigation — 2 GB today,
  cheap; replacing old-range HeaderNumber with per-era RecSplit is a
  possible later optimization, explicitly deferred.)
- **Sealed ranges leave MDBX**: the weekly-rebuilt hot DB simply omits
  sealed ranges of Header, BlockBody, BlockTransaction, Receipt,
  TransactionLog, Senders, ConsensusEvidence, BlockWitness,
  AccountChangeSet, StorageChangeSet (no runtime deletes needed — §6).
  This supersedes the "BlockTx is small, keep hot" assumption in
  `CleanupFrozenData` (`freezer_integration.go:119`) — at 182 tx/block it
  is the largest table.
- **Prune** (operator command or policy, e.g. "aux beyond 4 eras, exec
  beyond 1 year"): delete the file, flip manifest status to `pruned`.
  One file = one prune unit. Class A is not pruneable by any command.
- AccountHistory/StorageHistory index entries pointing into pruned aux
  ranges become dead weight, tolerated (queries fail-soft, §7.3); they are
  rebuildable from aux eras and truncated opportunistically during weekly
  regeneration.

## 6. Write path — replay is the only writer

**Decision: nodes never seal eras.** The weekly `replay-v2 --emit-era` run
is the single producer of both outputs; nodes open the era directory
strictly read-only and only ever append fresh blocks to hot MDBX.

Per weekly cycle (incremental, O(one week's growth)):

1. Fold the fleet head into the base as today; old blocks are untouched,
   so **previously sealed eras are byte-stable across generations** —
   the emit step only creates the 0–1 newly completed era(s).
2. New era built as `<name>.era.tmp` in the era dir (same volume ⇒ atomic
   rename), streaming: data frames → index → meta → footer; fsync →
   rename → manifest rewrite (temp+rename+fsync) → manifest blake3 into
   the hot MDBX being built.
3. Emit the hot-only MDBX: state + QMDB + navigation + the unsealed
   recent window. Born compact — no delete churn, no free-page
   fragmentation, fresh B+trees every week.
4. Reseed the fleet: hot MDBX per node + era dir (copy or hardlink, §8).

Why replay-direct beats node-side live sealing here:

- **One writer ⇒ determinism by construction.** No need to prove
  live-seal ≡ emit byte-equality — the hardest correctness gate of the
  two-writer design disappears.
- **Zero new runtime load on the fleet.** Sealing an era reads gigabytes
  from MDBX and writes ~1 GB while consensus runs; the fleet is
  latency-sensitive and this box has a history of dying under stacked
  load. Replay runs while the fleet is stopped anyway.
- **No live MDBX delete churn.** Deleting a sealed era's rows from a
  running node's MDBX causes free-page churn and long commits; the weekly
  rebuilt hot DB gets the same result for free.
- **Era store is immutable at runtime.** Files can be ACL'd read-only;
  a node crash can never corrupt history; integrity checking has no
  moving target.
- **Matches the industry endgame** (EIP-4444/era1): history is produced
  once and distributed as verified artifacts; nodes consume, not curate.

Costs, accepted: hot MDBX grows between reseeds (up to ~+21 GB/week under
peak txgen; typical much less), truncated back to ~7 GB at each weekly
reseed — strictly better than today's unbounded 47 GB+. A skipped weekly
cycle degrades gracefully to the status quo (MDBX keeps growing, nothing
breaks).

**Deferred: node-side live seal.** Only needed if/when third-party nodes
must run autonomously without our weekly pipeline (public deployment).
The container format is writer-agnostic, so this adds later without
format changes; at that point the byte-equality determinism gate (§8)
becomes mandatory.

## 7. Integrity: detect, degrade, never lie

Three detection layers:

1. **Open-time (every boot, cheap)**: for each manifest `sealed` entry —
   file exists, size matches, footer magic + version ok, footer/meta
   `payload_blake3` matches manifest, chain id + genesis + era chaining
   (`parent_hash` ↔ previous era `last_hash`) consistent. No full read.
2. **Read-time (always)**: per-frame xxh3 verified on every decompression.
3. **Scrub (weekly gate / `n42 era verify`)**: full blake3 of every file vs
   manifest; class A additionally re-links the header hash chain and
   spot-verifies QC evidence against the validator set history.

Failure handling:

- **Class B/C corrupt**: log ERROR + metrics counter, quarantine in-memory
  (file left on disk for forensics), range served as **pruned**. Node keeps
  running. Operator repairs via `n42 era fetch` (§8).
- **Class B/C missing**: if manifest says `pruned` → normal. If manifest
  says `sealed` → WARN "unexpectedly missing, treating as pruned" +
  metrics; node keeps running. Tolerated by requirement #3.
- **Class A corrupt/missing**: CRITICAL log + metrics + refuse to serve or
  sync-serve that range; consensus itself continues (it only needs the
  MDBX recent window), but the node is flagged unhealthy until the era is
  restored. Class A is never auto-degraded to "pruned".

### 7.3 Missing-data semantics (EIP-4444-style)

- RPC: header queries always work (class A). Body/receipt/log/tx queries in
  a pruned/degraded range return a distinct "history pruned" error code,
  not empty results.
- P2P catch-up serving: the block-sync responder answers requests for
  unavailable ranges with an **explicit typed "range unavailable" response
  frame** — never an empty/short read. (Regression memory: a 2-byte error
  code once parsed as a length wedged a stranded node; the wire type must
  make error vs data unambiguous, and the requester must skip this peer
  for that range, not retry forever.)
- Bootstrap: new nodes get state snapshot + all class A eras (mandatory)
  + whatever B/C the operator ships. deploy-7node copies or links the
  era directory alongside chaindata.

## 8. Recovery — determinism as the mechanism

Era bytes are a pure function of chain content: pinned codec versions,
fixed 64-block frame boundaries, pinned zstd level + dictionary, no
timestamps in hashed regions (creator meta excluded from `payload_blake3`).
Therefore:

- All 7 fleet nodes hold byte-identical era files → recovery is
  `n42 era fetch --from <peer-or-path>` + blake3 verify against manifest.
- The weekly replay base (D:) regenerates any era from scratch; the
  regenerated file must match the fleet manifest hash — this doubles as a
  standing correctness audit of the whole pipeline.
- A unit test emits a synthetic range twice (fresh run + incremental
  resume) and asserts byte equality; the weekly gate re-hashes prior-
  generation eras and asserts they are unchanged. (If node-side live
  sealing lands later, live-seal ≡ emit byte equality becomes a gate.)

On a single-box fleet, per-node copies of identical eras may be NTFS
hardlinks to one physical copy (7× space saving); note the tradeoff — one
physical corruption then hits all nodes, so the replay base + off-box
backup remains the true recovery source. Default: real copies; hardlink is
an explicit operator choice.

## 9. Read path

Tiered, reusing the existing tiered-source pattern (`node.go` cs sources):
MDBX (recent + state) → era store. Era reads: mmap the file, binary-search
nothing (direct index), LRU cache of decompressed 64-block frames (bounded
memory; never whole-file heap decompression — the snapshotreader .zst OOM
lesson).

## 10. Rollout (rides the weekly cycle, no online migration)

- **Phase 0 (code, no fleet impact)**: era codec + read-only store +
  `replay-v2 --emit-era` + `n42 era verify|ls|fetch|prune` + read-path
  tier + tests (roundtrip, incremental determinism, corruption injection
  per layer, missing-file degradation, boot-check matrix, catch-up
  unavailable-frame). No node-side seal goroutine — nodes gain only the
  read tier and boot verification.
- **Phase 1 (next weekly reseed)**: replay-v2 emits era layout + hot MDBX;
  fleet reseeded with `AncientDB=true`. Hot MDBX drops from ~47 GB to
  ~7 GB/node. Rollback = previous generation, as every week.
- **Phase 2**: enable aux prune policy (e.g. keep 4 aux eras ≈ recent
  window for witness/mobile-verify + changesets for any unwind audit),
  observe one weekly cycle, then consider exec-era expiry windows.

## 11. Open questions

- Era span 2^20 vs 2^19: fewer files vs finer prune granularity — default
  2^20, revisit if sustained density makes exec eras exceed ~10 GB.
- ConsensusEvidence spot-verify depth at scrub time (full QC re-verify of
  14M blocks is expensive; sampling rate TBD).
- Whether HeaderNumber for sealed ranges moves to per-era RecSplit
  (deferred; beware the Enums insertion-order pitfall).
