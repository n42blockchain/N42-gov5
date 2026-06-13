# DATC: Spill Resilience, Graceful Shutdown, and OOM Fix

Date: 2026-06-13

## Context

`n42-datc build` reconstructs full-history EIP-1186 proof data (Depth-Adaptive
Temporal Checkpointing). In `--leaf-seg` mode the bulk tables (leaf history
`DatcLeafA/S` and change index `DatcAccChg/StorChg`) bypass MDBX: rows are
appended to per-bucket zstd **spill** files during the build, then `finalize`
sorts each bucket and writes static `.seg` segments. The MDBX node records
(`DatcAccNode/StorNode`) and the schedule/meta stay in MDBX.

The epoch schedule is `E_d = clamp(alpha*16^d / cbar, 1, 2^22)`. The 25M run
uses `--cbar 0.25` (so `E_0=64`, `E_1=W=1024`). In window mode the trie
materializes only at W-block boundaries and the root is gold-checked against
the real header there.

## The 2026-06-13 incident (root cause of the data loss)

1. A running `--leaf-seg` build was hard-killed (SIGKILL/TerminateProcess). The
   streaming zstd encoder writes each build run as one large, not-yet-closed
   frame, so the kill left a **truncated zstd frame** at the tail of every
   spill stream.
2. A resumed build opened the spill files `O_APPEND` and wrote a **second,
   cleanly-closed** zstd stream after the truncated frame.
3. `finalize` decoded each spill file as a whole stream. It hit the truncated
   first-run frame and, because the run is a single frame, **skipped the entire
   first stream** (the 0–13.37M data), recovering only the resumed stream's
   later epochs.
4. `finalize` then reported success and **`os.RemoveAll`'d the spill dir** —
   converting a recoverable kill-truncation into **permanent, multi-day data
   loss**.

Symptom: `verify` failed at mid-window blocks (`changedChildren` returned empty
because the change index for the lost range was gone), while window-boundary
blocks still passed (they reconstruct from MDBX node records, which were intact).
A `CHGSCAN` diagnostic confirmed the change-index segments contained only the
resumed range's epochs (>= 13056), proving the earlier range was absent from
both segments and MDBX.

The fold algorithm and the `cbar=0.25` schedule were never at fault — the loss
was caused entirely by the hard kill plus a finalize that silently dropped and
deleted the truncated data.

## Fixes (cmd/n42-datc)

1. **Per-batch spill frame cut** — `leafseg.go` `spillStream.cut()` /
   `leafSpillWriter.flushBatch()`; `main.go run()` calls `flushBatch()` after
   every batch commit. Each committed batch now ends in its own COMPLETE zstd
   frame, so a kill truncates only the in-flight batch's frame.

2. **Kill-resilient finalize** — `leafseg.go finalizeBucket()` decodes the
   spill frame-by-frame (split on the 4-byte zstd magic), accumulates
   consecutive good frames into a contiguous group, and resyncs at the next
   frame boundary when a frame fails to decode. Rows are parsed per group, so
   only the truncated frame's rows are dropped; the next group starts at a
   fresh, row-aligned frame.

3. **Finalize safety net** — `finalizeLeafSegments()` returns the per-bucket
   corrupt-frame count; if any frame was skipped it **RETAINS the spill dir
   (does not delete)** and prints a warning. Never silently deletes on
   incomplete recovery. (The prior code deleted the spill regardless.)

4. **Graceful shutdown** — `main.go` installs a SIGINT/SIGTERM handler that sets
   an atomic `stopRequested`. The build loop checks it after each batch
   (committed + spill cut at a frame boundary) and exits cleanly, skipping
   finalize so the run is resumable with `--start N`. Ctrl+C is now safe; the
   spill is retained for resume. Never kill -9.

5. **OOM fix (streaming SELFDESTRUCT wipe)** — `main.go accumulateBlock()`
   streams the pre-wipe slot tombstones straight from the HashedStorage cursor
   instead of materializing a full `live` map of every slot of a destructed
   contract. pprof showed that map as a ~25 GB heap spike (the dominant OOM
   risk); after the fix the in-flight heap is ~5 GB.

6. **Progress display** — `main.go` heartbeat prints a percentage:
   `[datc] 12.3%  block N / END  X blk/s  ETA ...`.

7. **Diagnostics (env-gated, default off)** — `verify.go`: `DATC_TRACE` (branch
   resolution trace), a change-index bucket scan, and `DATC_CHG_MDBX` (force the
   change index through MDBX). Used to locate the segment-vs-MDBX mismatch.

## Verification

- **Clean rebuild 0 -> 2,000,000** with `cbar=0.25 --leaf-seg`, no kill, then
  `verify --samples 50`: **50/50 historical roots reconstructed byte-exact**
  (avg 1.42s, p50 1.33s, p99 3.06s). This is the first end-to-end proof that the
  `cbar=0.25` mid-window fold is correct on clean, complete data.
- **Heap** (in-flight, ~3M): **5.0 GB**. The 25 GB wipe spike is gone
  (`run` flat dropped to 366 MB). Dense-region projection ~11–15 GB, well under
  the 100 GB soft limit. No OOM risk remaining.
- **CPU profile** (20s): `runtime.cgocall` (MDBX) 33%, trie
  keccak/cursor/HashBuilder ~25%, zstd compression only 2.7%. There is no safe,
  high-yield speedup without altering trie correctness; not attempted (a wrong
  change costs another multi-day rebuild). Throughput is erigon-class.

## Run guide

Full rebuild (`-> D:/n42-datc-eth25m-v2`), foreground, with live progress and
graceful Ctrl+C (single line):

```
cd C:\N42\N42-gov5
.\build\bin\n42-datc.exe build --src mainnet --changesets D:/N42-eth1177/chain/freezer --headers D:/n42-eth1/chain/freezer --out D:/n42-datc-eth25m-v2 --end 24998000 --cbar 0.25 --alpha 16 --leaf-seg --batch 16384 --map.gb 1024 --pprof.port 6061
```

- **Ctrl+C once** finishes the current batch, cuts the spill at a frame
  boundary, prints `graceful stop at block N ... Resume: --start N`, and exits.
  Never press it repeatedly and never kill -9.
- **Resume** by appending `--start <N>` to the same command.

Verify (single line):

```
.\build\bin\n42-datc.exe verify --out D:/n42-datc-eth25m-v2 --headers D:/n42-eth1/chain/freezer --samples 50 --fold-depth 4
```

Proof spot-check (WETH at a historical height):

```
.\build\bin\n42-datc.exe proof --out D:/n42-datc-eth25m-v2 --headers D:/n42-eth1/chain/freezer --addr 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2 --at 20000000 --fold-depth 4
```

## Status / follow-up

- B1 full rebuild `0 -> 24,998,000` is operator-run in the foreground with the
  fixed binary. Early sparse region runs at tens-of-thousands blk/s; the dense
  DeFi region (~13M–25M) runs at ~50–90 blk/s and dominates wall-clock
  (overall estimate ~1.5–2 days).
- On completion: `verify --samples 50` + `proof` spot-checks. Once green, the
  old corrupted `D:/n42-datc-eth25m` (segments cover only 13.37M–13.5M after the
  incident) can be removed to reclaim space.
- The earlier `D:/n42-datc-13m` (head 12,679,791) was built with an older
  schedule and fails verify; it is superseded by the v2 rebuild.
