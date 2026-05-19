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

## Full file inventory

### Snapshot tier (D:/n42-snapshot)

| File | Size | Contents | Read API |
|------|------|----------|----------|
| accounts.codedict | 71.4 MB | 2.34M unique codeHash dict (sorted 32B hashes; id=position) | sequential mmap → `dict[id3B]` |
| accounts.idx | 78.8 MB | RecSplit MPHF (1.71 bit/key) — addr → ordinal | `recsplit.IndexReader.Lookup(addr)` |
| accounts.ef | 351.3 MB | Elias-Fano ordinal → byte offset in .val | `eliasfano32.Get(ord)` |
| accounts.val.zst | 3,516 MB | zstd values; each entry = `[fp:4B][len:1B][V2-encoded-acct]`, codeHash replaced by 3B dict id | seek → 1B len → read len B → resolve codeHash via codedict |
| **accounts subtotal** | **4,018 MB ≈ 3.92 GB** | | |
| storage.idx | 320.2 MB | RecSplit MPHF for (addr,slot) 52B key | `Lookup(addr‖slot)` |
| storage.ef | 1,364 MB | EF offsets (7.29 bit/key) | `Get(ord)` |
| storage.val.zst | 18,665 MB | values, `[fp:4B][len:1B][1-32B val]` | same pattern as accounts |
| **storage subtotal** | **20,349 MB ≈ 19.87 GB** | | |
| **Snapshot total** | **23.79 GB** | | |

### History tier (D:/n42-history-full)

| File | Size | Contents | Read API |
|------|------|----------|----------|
| account.mphf | 87.3 MB | RecSplit MPHF for 428M ever-touched addrs (1.71 bit/key) | `Lookup(addr)` → ord |
| account.idx | 51.0 MB | sparse page-offset table, 8B per page × 6.69M pages | `idx[ord/64]` → page byte offset |
| account.kv | 48.62 GB | zstd-compressed pages (64 entries/page); page-decoded entry = `[fp:4B][varint blobLen][packed-history-blob]` | seek page → zstd decode → walk to (ord%64) → verify fp → return blob |
| **account history subtotal** | **48.75 GB** (11.32 B/entry, 122 B/key) | | |
| storage.mphf | 414.43 MB | MPHF for 2.03B ever-touched (addr,slot) (1.71 bit/key) | `Lookup(addr‖slot)` |
| storage.idx | 242.29 MB | page offsets, 8B per page × 31.76M pages | |
| storage.kv | 87.58 GB | zstd pages same format | |
| **storage history subtotal** | **88.22 GB** ✓ measured (11.23 B/entry, 46.6 B/key, build wall 7h25m) | | |
| **History total** | **~137 GB** (measured) | | |

### Code tier (D:/N42-eth1/chain/freezer)

| File | Size | Contents | Read API |
|------|------|----------|----------|
| codes.cidx | 55.9 MB | sorted entries `[20B addr][2B fileNum][4B offset]` × 9.77M | binary search by addr |
| codes.0000-0003.cdat | 5,935 MB | per-entry `[4B len][zstd(bytecode)]`, 2 GB file rotation | seek to offset → zstd decode |
| **Code total** | **5.93 GB** | | |

### Block tier (D:/N42-eth1177/chain/freezer/ + geth ancient)

| File | Size | Notes |
|------|------|-------|
| headers.{cidx,cdat} | ~5 GB | Compact RLP headers |
| bodies.{cidx,cdat} | ~100 GB | Tx + uncles (or `bodyc` if columnar) |
| receipts.{cidx,cdat} | ~30 GB | Optional (clients can re-execute) |
| senders.{cidx,cdat} | ~38 GB | Optional (clients can ecrecover) |
| **Blocks total** | **~150 GB** raw / ~80 GB without optional | |

### Grand total (all measured 2026-05-18)

| Tier | Compressed deployable |
|------|----------------------|
| Snapshot | 23.79 GB |
| Code | 5.93 GB |
| History (account + storage) | **136.97 GB** ✓ |
| Blocks (full chain data) | ~150 GB |
| **Full archive** | **~317 GB** |
| **State-only archive (no blocks)** | **166.69 GB** ✓ |
| **Fast (snapshot + code + recent delta)** | **~30 GB** |

vs original 945 GB MDBX+freezer:
- Full archive: **3× smaller** (clients usually have blocks anyway via eth/68)
- State-only: **5.7× smaller** (945 → 167 GB measured)
- Fast mode: **31× smaller**

## Access benchmark (measured)

See `history-bench-results.md` for full bench. Headlines:

| Tier | Workload | µs/lookup | qps |
|------|----------|-----------|-----|
| Account history (428M keys, 48.75 GB) | Single-thread random | 177 | 5.7K |
| Account history | Sequential sorted | 17 | 57K |
| Account history | 4-8 worker concurrent | 11 | 91K |
| Mid-era storage (77M keys, 1.6 GB) | Single-thread random | 101 | 9.9K |
| Mid-era storage | Concurrent peak | 8.3 | 120K |

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

## Client/server distribution

See `client-server-sync.md` for the full sync flow, including:
- bootstrap (full archive / fast mode)
- eth/68 catch-up and live sync
- weekly delta publication cadence
- blake2b manifest trust model
- multi-mirror hash consensus

## Growth projection

Per-year increment (extrapolated from 25M-block density):

| Tier | New per year |
|------|-------------|
| Snapshot | ~3 GB |
| History | ~16 GB |
| Code | ~1 GB |
| Blocks | ~150 GB (raw) / ~50 GB compact |
| **Total compressed (full archive)** | **~20 GB/year** |
| **Total compressed (state-only)** | **~20 GB/year** |
| **Fast mode (snapshot+delta)** | **~3-4 GB/year** |

| Time | Full archive (with blocks) | State-only | Fast |
|------|----------------------------|------------|------|
| Now (25M) | 310 GB | 160 GB | 30 GB |
| +6 mo | 320 GB | 168 GB | 32 GB |
| +1 yr | 330 GB | 176 GB | 34 GB |
| +2 yr | 360 GB | 192 GB | 38 GB |
| +3 yr | 390 GB | 210 GB | 42 GB |
| +5 yr | 450 GB | 240 GB | 50 GB |

## Compression-scheme upgrade roadmap

Step files are append-only; old data is **never re-compressed** when a
new scheme lands. Each step file self-describes via `Version` header.
Readers dispatch per-step. Old clients only download new-version
files; existing files stay valid.

| Phase | Time | Trigger | New scheme |
|-------|------|---------|-----------|
| **v1.5 (current)** | now | — | MPHF+fp ✓ done |
| **v2 (step framework)** | +1-2 mo | first monthly merge | weekly/monthly/yearly step tiers + reader-side merge query |
| **v3 (value dict)** | +12-18 mo | total ≥ 200 GB | top-N common-value dict per domain (-15-25%) |
| **v4 (EF-only history)** | +24-36 mo | total ≥ 250 GB | Erigon-E3 style; needs per-step snapshot reuse (-30-40%) |
| **v5 (domain-aware codec)** | +36-60 mo | total ≥ 300 GB | per-contract-template compression (-40-60%) |
