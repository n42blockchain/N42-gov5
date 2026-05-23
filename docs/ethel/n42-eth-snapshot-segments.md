# Phase H.3 — Segment-Incremental Snapshot

**Status:**
- ✓ Phase H.3a — spec + selector glob support
- ✓ Phase H.3b-mini (2026-05-23) — `reth-snapshot-export --end-block H`
  emits monolithic files with spec-compatible segment naming
  (`accounts.0-H.{idx,ef,val.zst}` instead of `accounts.{idx,ef,val.zst}`)
- ⊘ Phase H.3b-full — real block-range per-segment writer (needs reth
  changeset reader; 1-2 weeks)

## Why

The original `accounts.0-25099999.{idx,ef,val.zst}` and
`storage.0-25099999.{idx,ef,val.zst}` snapshot files (~28 GB
combined) are rebuilt monolithically per release. Every delta
between two releases includes the **entire** snapshot tier, even
though most account/storage entries are unchanged between two
adjacent weekly releases.

For delta efficiency (Phase H), the snapshot tier needs the same
treatment as the history index (`accthist`/`storhist`/`txindex`,
`docs/ethel/history-build-v1-design.md`): split into **per-segment
files** so unchanged segments are reused across releases.

## Segment naming convention

```
snapshot/
├── accounts.0-999999.{idx,ef,val.zst,codedict}
├── accounts.1000000-1999999.{idx,ef,val.zst,codedict}
├── ...
├── accounts.25000000-25999999.{idx,ef,val.zst,codedict}
└── (same shape for storage.*)
```

- `<startBlock>-<endBlock>` inclusive range
- Segment size: **1,000,000 blocks** (`SnapshotSegmentSize` constant,
  aligned with cscompact's `HistSegmentSize`)
- Per-segment RecSplit MPHF + Elias-Fano + zstd values (same shape
  as the monolithic file, just bounded)
- A snapshot is "complete" at H if it has segments covering every
  range `[k*1M, (k+1)*1M-1]` for k in 0..⌊H/1M⌋, plus a partial
  trailing segment `[⌊H/1M⌋*1M, H]` if H is not a segment boundary

## Manifest implications

The existing selector pattern `snapshot/accounts.*.{idx,ef,val.zst}`
already matches segmented files. **No selector change required** —
this is why the H.3a delivery in this PR is just documentation +
glob verification, not new code.

Each segmented file is its own entry in the manifest:

```json
{
  "path": "snapshot/accounts.0-999999.idx",
  "section": "state-accounts",
  "size": 156250000,
  "blake2b256": "..."
},
{
  "path": "snapshot/accounts.1000000-1999999.idx",
  "section": "state-accounts",
  ...
}
```

## Delta math (with H.3 vs without)

Per archive release H₁ - H₀ = 1 week (~50K blocks):

| | Without H.3 | With H.3 |
|---|--:|--:|
| Number of snapshot files | 6 (3 per accounts/storage) | ~150 (26 × 3 per accounts/storage at 25M) |
| Files changed per release | 6 (all) | 6 (only the tail segment + 1 maybe new segment) |
| Snapshot bytes in delta | ~28 GB | **~1 GB** (one ~1M-block segment) |

Combined with the freezer rotated `.cdat`s already-incremental
nature, full weekly delta:

| | Without H.3 | With H.3 |
|---|--:|--:|
| Per-week delta | ~30 GB | **~2 GB** |
| As % of full archive (849 GB) | 3.5 % | **0.24 %** |

## Sub-tasks

| Sub | Outcome | Effort | Status |
|---|---|---|---|
| **H.3a docs + selector validation** | this doc + `TestSelector_HandlesSegmentedSnapshot` | <1 day | ✓ |
| **H.3b-mini segment naming** | `reth-snapshot-export --end-block H` emits `accounts.0-H.*` (still monolithic, but spec-compliant filename) | <1 day | ✓ (2026-05-23) |
| **H.3b-full writer migration** | per-segment `accounts.{k*1M}-{(k+1)*1M-1}.*` files; needs reth changeset reader to enumerate keys touched in each block range, dedupe to last value per segment, build per-segment RecSplit MPHF | 1-2 weeks | TODO |
| **H.3c per-segment verifier** | `cmd/n42-snapshot-verify-segment` validates one segment standalone (RecSplit + EF integrity + value hash count) | 2-3 days | TODO |
| **H.3d resplit tool** | `cmd/n42-snapshot-resplit` takes a monolithic `accounts.0-H.*` + reth `AccountChangeSets` and post-process splits into per-segment files (alternative to writer migration) | 1 week | TODO |

## H.3b-full implementation notes

Two paths to true per-segment files; pick one:

**Path A — writer-side segmentation (one DB scan)**
1. Open reth `AccountChangeSets` table (key = block + tx_idx, value = changes)
2. For each segment k in [0..⌈H/1M⌉]:
   a. Build set of keys touched in blocks [k*1M, (k+1)*1M-1]
   b. For each touched key, look up the value in `PlainAccountState`
   c. Build per-segment RecSplit MPHF + EF + zstd
3. Output: `accounts.{k*1M}-{(k+1)*1M-1}.{idx,ef,val.zst,codedict}`
4. Total scans: 1 over changesets + ⌈H/1M⌉ × M-keys reads from PlainState

**Path B — post-process splitter (two-pass)**
1. Existing `reth-snapshot-export --end-block H` produces monolithic
2. New `n42-snapshot-resplit`:
   a. Read reth `AccountHistory` (per-key sorted block list)
   b. For each key, find max(modBlocks) → maps to segment k
   c. Bucket all (key, value) pairs by segment
   d. Build per-segment RecSplit + EF + zstd
3. Faster on subsequent reruns (don't re-derive monolithic)

Path A is cleaner but couples export with segmentation. Path B is decoupled but needs `AccountHistory` reader (already present in cmd/reth-acct-history). Defer choice to operator scheduling decision.

## Reader compatibility

Readers that consume `snapshot/accounts.*.idx` via glob (the
manifest tooling, the snapshot loader in the RPC backend) work
unchanged — they iterate all matching files and dispatch by
block range encoded in the filename. The format-internal layout
of each `.idx`/`.ef`/`.val.zst` segment is identical to today's
monolithic layout; only the range covered is bounded.

## Companion documents

- `docs/ethel/n42-eth-client-distribution.md` — three-mode spec
- `docs/ethel/n42-eth-delta-updates.md` — Phase H design
- `docs/ethel/history-build-v1-design.md` — parallel pattern for
  history index segmenting
