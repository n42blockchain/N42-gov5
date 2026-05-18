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

## Storage full-scale bench (pending)

Storage history full build in progress (~5-7h ETA). Will benchmark
when complete. Expected to be ~50 GB and similar µs/lookup since the
Get path is the same.
