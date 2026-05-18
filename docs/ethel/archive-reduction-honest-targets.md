# N42 Archive Reduction — Honest Targets

**Date:** 2026-05-18
**Context:** Original target was 34 GB (vs current 945 GB ≈ 28×). After
real measurements and information-theoretic review, that target is
unreachable without changing archive semantics. This doc records what
is actually achievable and the trade-offs at each step.

## Baseline (D:/N42-eth1177, 25.1M blocks)

| Component | Size |
|-----------|------|
| MDBX PlainState | 298 GB |
| freezer storcs | 260 GB |
| freezer acctcs | 137 GB |
| freezer witness | 167 GB |
| freezer senders | 38 GB |
| Code (D:/N42-eth1) | 6 GB |
| **TOTAL** | **945 GB** |

## Where the original 34 GB target was wrong

1. **EF-only history was double-counted as free**: claimed "0 B per
   change" by ignoring that per-step snapshots still store the values.
   Real saving from EF vs varint deltas is ~10× on the block lists
   only (a few GB), not the values (where info-entropy lower bound
   applies).

2. **Entry counts underestimated**: estimated 300M storage entries;
   measured 1.57B. 5× more entries → 5× more value bytes regardless
   of encoding.

3. **NewValue redundancy not accounted**: storcs/acctcs store both
   OldValue and NewValue per change (~2× theoretical minimum) so
   unwind+forward both work without trie traversal. This is a
   deliberate semantic, not waste.

## Realistic ladder

| Strategy | storcs | acctcs | witness | senders | MDBX | code | Total | Cost |
|----------|--------|--------|---------|---------|------|------|-------|------|
| Current | 260 | 137 | 167 | 38 | 298 | 6 | **945** | — |
| Drop witness + senders post-verify | 260 | 137 | 0 | 0 | 298 | 6 | **701** | re-verify needs source |
| + snapshot replaces live MDBX | 260 | 137 | 0 | 0 | 25 | 6 | **428** | snapshot rebuild for full re-sync |
| + history index + 7-day warm CS | 50 | 30 | 0 | 0 | 25 | 6 | **111** | unwind window = 7 days |
| + drop NewValue (breaks fwd replay) | 25 | 15 | 0 | 0 | 25 | 6 | **71** | parallel-evm rewrite |

## Recommended target

**~110 GB (8.5× reduction)** — the "+ history index + 7-day warm CS"
row. This is the sweet spot:

- **Net win:** snapshot tier + RecSplit-indexed history covers all
  point-in-time queries.
- **Trade:** deep unwind beyond 7 days requires history → re-execution
  (not native freezer rollback). 7 days >> 100× PoS finality (32
  slots), so re-orgs deeper than that are practically impossible.

## Implementation status (2026-05-18)

- ✓ Snapshot writer (`cmd/reth-snapshot-export --n42`):
  - accounts: 3.92 GB / 386M / 37 min (measured)
  - storage: in progress (1.57B entries, ETA ~2h)
- ✓ History writer (`cmd/n42-history-build`):
  - v1 (per-key, plain): 11.92 B/entry account, 18.41 B/entry storage
  - v2 (addr-grouped): 17.67 B/entry storage at mid-era 1M smoke
  - **v1.5 (MPHF+fp)**: **9.69 B/entry storage** (-47% vs v2), 11.27 B/entry account at mid-era 1M smoke
  - All verified 100% (1000 random samples) via `cmd/n42-history-verify`

  MPHF+fp wins for storage because the 52B addr+slot key is 55% of
  storcs raw bytes. Replacing with 4B fingerprint + ~1.71 bit/key
  RecSplit MPHF collapses that to ~4.21 B/key. For accounts the key
  is 20B and blob is bigger, so MPHF gains are modest (~5%).

- TODO: CS warm-tier truncation (`freezer.TruncateTail` does not exist;
  needs design). Three options:
  - A. Add `TruncateTail` to freezer (general but invasive).
  - B. Atomic CS rewriter: build warm-only CS, swap. (Cleanest.)
  - C. Leave old CS, mark "history built", manually rm. (Simplest.)

## Realistic full-archive total (after MPHF+fp landed, snapshot measured)

| Component | Size | Notes |
|-----------|------|-------|
| snapshot accounts | 3.92 GB | measured: 386M entries / 37m11s |
| snapshot storage | **19.87 GB** | measured: 1.57B entries / 3h46m38s |
| code | 5.93 GB | existing (D:/N42-eth1/chain/freezer/codes.*) |
| account history MPHF+fp | ~30 GB | extrapolated from 1M mid-era (1.56 GB) |
| storage history MPHF+fp | ~41 GB | extrapolated from 1M mid-era (1.61 GB) |
| warm CS (7 days) | ~0.1 GB | pending impl |
| **TOTAL** | **~100 GB** | **vs 945 GB = 9.4× reduction** |

Storage snapshot landed slightly larger than estimated (19.87 vs ~15)
because 1.57B entries × 13 B/entry (incl. MPHF + EF + zstd) overhead
matches the mid-era 1M smoke ratio. Final number ~100 GB matches the
"recommended target ~110 GB" within 10%, well inside the original
honest-target band.

### Note on .val files

`reth-snapshot-export` keeps both `.val` (uncompressed) and `.val.zst`
after build. For deployment, delete `.val` and keep `.val.zst`. The
table above assumes `.val.zst` only.

## Final snapshot file inventory (D:/n42-snapshot)

```
accounts.codedict    71.4 MB    codeHash dict (2.34M unique × 32B)
accounts.idx         78.8 MB    RecSplit MPHF (1.71 bit/key)
accounts.ef         351.3 MB    Elias-Fano (ordinal → byte offset)
accounts.val.zst   3516.0 MB    zstd values (codeHash → 3B id)
                  ────────
                   4017.5 MB    accounts total

storage.idx         320.2 MB    RecSplit MPHF (1.71 bit/key)
storage.ef         1363.7 MB    Elias-Fano (ordinal → byte offset)
storage.val.zst   18665.0 MB    zstd values
                 ─────────
                  20349.0 MB    storage total
```

Combined snapshot: 23.79 GB (compressed, deployable).
With code: 29.72 GB.

## What 34 GB would require

Either:
- **Aggressive pruning**: drop all state historically touched by
  SELFDESTRUCT'd contracts, drop slots that never read post-set.
  Changes archive semantics; would need community spec.
- **Distributed shards**: each node holds 1/Nth of history,
  reconstitute on demand. Operational complexity offsets storage saved.

Neither is a 2-week project. The 110 GB target is reachable in
~1-2 weeks with the existing snapshot + history tools and a small
CS truncation tool.
