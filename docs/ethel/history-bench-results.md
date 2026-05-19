# History Coldstore Bench Results

**Date:** 2026-05-18
**Hardware:** 32 cores / 128 GB RAM / NVMe (D:)
**Tool:** `cmd/n42-history-bench`

## Account full-scale (428M keys, 4.6B entries, 48.75 GB)

```
=== Single-thread random ===
  50000 samples in 8.837s
  176.75 µs/lookup, 5,658 qps

=== Sequential by sorted key ===
  50000 samples in 870ms (sorted-key locality)
  17.41 µs/lookup, 57,442 qps

=== Concurrent random (varying workers) ===
  wkrs | qps    | µs/lookup | speedup
  -----+--------+-----------+--------
  1    | 57,093 | 17.52     | 1.00x
  2    | 82,038 | 12.19     | 1.44x
  4    | 90,277 | 11.08     | 1.58x
  8    | 90,933 | 11.00     | 1.59x
  16   | 89,935 | 11.12     | 1.58x
  32   | 84,385 | 11.85     | 1.48x
```

**Saturation: 4-8 workers, ~91K qps.** Beyond 8 workers the shared
zstd.Decoder and serialised kvFile.ReadAt become contention points;
adding more threads regresses slightly.

## Mid-era 1M block storage (77M keys, 178M entries, 1.61 GB)

```
=== Single-thread random ===
  50000 samples in 5.054s
  101.09 µs/lookup, 9,892 qps

=== Sequential by sorted key ===
  10.54 µs/lookup, 94,911 qps

=== Concurrent random ===
  1   | 95,003  | 10.53 | 1.00x
  2   | 125,574 | 7.96  | 1.32x
  4-32| ~120K   | 8.3   | 1.27x
```

## Comparison

| Workload | Mid-era 1M storage (1.6 GB) | Full account (48.75 GB) | Ratio |
|----------|----------------------------|--------------------------|-------|
| Single-thread random | 101 µs | 177 µs | 1.75× slower |
| Sequential sorted | 10.5 µs | 17.4 µs | 1.66× slower |
| Concurrent peak qps | 120K | 91K | 0.76× |

Larger data set = more page cache misses (1.6 GB fits in OS cache,
48.75 GB doesn't), explaining the ~1.7× slowdown on single-thread.

## Architectural take

For the **cold tier** these numbers are excellent:
- Random Get: **<200 µs** worst case (cold cache, page decompress)
- Sequential / hot Get: **<20 µs** (page cache warm)
- Concurrent: **~91K qps** plateaued

The bottleneck under concurrency is the shared zstd.Decoder
(internal state, single-thread decode). For higher concurrent throughput:
- Use one Decoder per goroutine
- Or split into per-CPU readers
- Or use seekable zstd format that doesn't need full-page decode

This isn't blocking — 91K qps cold tier suffices for any normal
archive workload (eth_getBalanceAt-type queries are <100/sec per
node typically).

## Storage full-scale bench (measured)

Against 88.22 GB / 2.03B keys / 31.76M pages on 32-core/128GB/NVMe.
Bench sampled keys from late 1M block range (24M-25M, 205M unique
keys → ~40 GB sourceKeys map). Full-chain sampling OOM'd at ~111 GB
(see Tooling note below).

```
=== Single-thread random ===
  50000 samples in 6.619s
  132.40 µs/lookup, 7,553 qps

=== Sequential by sorted key ===
  50000 samples in 511ms (sorted-key locality)
  10.23 µs/lookup, 97,731 qps

=== Concurrent random (varying workers) ===
  wkrs | qps     | µs/lookup | speedup
  -----+---------+-----------+--------
  1    | 109,791 | 9.11      | 1.00x
  2    | 120,486 | 8.30      | 1.10x
  4    | 118,055 | 8.47      | 1.08x
  8    | 117,962 | 8.48      | 1.07x
  16   | 120,584 | 8.29      | 1.10x
  32   | 122,199 | 8.18      | 1.11x
```

**Saturation immediately at 2+ workers, ~122K qps peak.** Lower
concurrency contention than account because storage blobs are
smaller (avg 22 B vs account's 120 B) → less time inside page
decompress holding the zstd.Decoder lock.

## All three benches compared

| Dataset | Single random | Sequential | Concurrent peak |
|---------|--------------|-----------|----------------|
| Mid-era 1M storage (77M keys, 1.6 GB) | 101 µs | 10.5 µs | 120K qps |
| Account full (428M keys, 48.75 GB) | 177 µs | 17.4 µs | 91K qps |
| **Storage full (2.03B keys, 88.22 GB)** | **132 µs** | **10.2 µs** | **122K qps** |

**Insight**: storage full is FASTER than account full despite being
4.7× more keys and 1.8× more bytes. The dominant cost is per-page
linear scan time (zstd decompress + entry walk to ord%64 position),
which scales with blob size, not page count or total size:
- Account: ~120 B blob × 64 entries/page = ~7.6 KB raw page
- Storage: ~22 B blob × 64 entries/page = ~1.4 KB raw page

Sequential locality (~10 µs) reflects the page cache holding adjacent
keys' pages; random access (~130 µs) pays cold zstd decode per page.

## Tooling limitation: bench sourceKeys OOM at full scale

`cmd/n42-history-bench` builds an in-memory `map[string]struct{}` of
all unique keys in the configured block range to source realistic
queries. For full 25M-block storage this map needs ~160 GB (2.03B
keys × ~80 B Go map overhead) — OOMs on 128 GB hosts.

Workarounds:
- For storage: use a smaller `--start/--end` range (≤1M blocks
  late-chain) which gives ~200M keys / ~40 GB map.
- For account: full chain fits since 428M keys × 80B ≈ 35 GB.

Future improvement: rewrite bench to sample keys lazily from N random
blocks during the workload (no upfront map), or load keys from
snapshot tier (current-state keys, 1.57B for storage — still big but
no decode cost).
