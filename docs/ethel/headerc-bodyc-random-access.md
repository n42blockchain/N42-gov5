# headerc / bodyc — why consumers fall back to geth ancient, and how to fix it without losing compression

> Investigated 2026-08-31 after observing that both the Linux gov5 box and
> n42-rs read `witness-geth/` (raw geth ancient tables) instead of the
> `headerc`/`bodyc` columnar freezers that the weekly pipeline produces.

## 1. The format is excellent at what it was designed for

`bodyc` at the 2026-08-30 tip: **4.65 GB compact vs 7.55 GB geth-snappy vs
17.05 GB raw RLP** — 27.3% of raw, and **38% smaller than geth's own snappy
encoding**. That number is the whole reason the format exists and it is not in
question here.

It gets there by *columnar separation inside a segment*: 8192 blocks are split
field-by-field (all nonces together, all gas prices together, …), then the
whole segment is zstd-compressed as one unit.

## 2. The cost: a segment is the unit of decompression

`BodyCompactReader.decodeSegment` / `HeaderCompactReader.loadSegment` both do:

```go
compressed := make([]byte, compSize)      // the WHOLE segment
raw, err := dec.DecodeAll(compressed, nil)
blocks, err := decodeBodySegment(raw)     // decode all 8192 blocks
```

There is no sub-segment index. Reading **one** block costs decompressing and
decoding **8192**. Both readers then cache exactly one segment (`cachedSeg`),
so any access pattern that alternates between segments re-pays the cost every
time.

Measured on this box (2026-08-31, `headerc-bodyc-probe`, warm page cache):

| access pattern | wall time |
|---|---|
| random single block, cold process | **2.78 - 3.97 s** |
| 1000 sequential blocks inside one segment | 2.76 s total = 2.76 ms/block |

The two rows are the same number. Reading a thousand blocks costs what reading
one costs, because the cost *is* the single decompression. Amortised over a
full segment that is ~0.34 ms/block — excellent. Paid per random block it is
**~8192x** worse than it looks.

For comparison, geth's ancient store compresses each item independently
(snappy per entry), so a random block read is a single small decompress —
microseconds. That is a 3-4 order-of-magnitude gap on random access, and it is
exactly why a consumer that reads out of order picks the raw tables.

This also explains the witness-replay finding recorded on 2026-08-24: with 128
cores the run sat at 99.4% idle because a single reader was decoding whole
`bodyc` segments (1.0-1.6 s each) as an Amdahl serial section.

## 3. Two separate problems, do not conflate them

1. **Random access cost** — structural, fixable without touching the ratio
   (§4).
2. **Cross-language cost** — the columnar layout plus the flag-driven optional
   columns (`bfPostMerge`, `bfWithdrawals`, `bfHasBlob`, `bfHasSetCode`,
   `bfHasAccessList`, `bfAuthVFull`) is a non-trivial decoder to port. Fixing
   (1) does NOT fix (2).

   **Where Rust actually stands (searched 2026-08-31).** More is done than the
   fallback suggests:

   | exists | where | does what |
   |---|---|---|
   | snapshot selector | `pevm/src/snapshot/{selector,manifest,fetch,verify,catchup,follow}.rs` | a port of this repo's Go selector — parses the manifest and FETCHES `headerc.*` / `bodyc.*` per tier |
   | witness recording | `reth-witness` (reth fork, `feat(witness): record positional state-read witnesses while executing`) | Rust EVM emits its own execution witnesses |

   What is missing is only the **decoder**: `bodyc` / `body_compact` /
   `headerc` appear nowhere in `reth-witness/crates`, `N42-26` or `n42-rs`.
   Rust can already download these files by manifest; it cannot open them. The
   cross-language contract is built at the selection layer and broken at the
   decode layer — which raises the value of §4, since the Rust side is already
   at the door.

## 4. Proposed fix for (1): frame the segment, keep the ratio

Keep 8192-block segments and the existing `.cidx` (so nothing above changes),
but inside a segment write **independently decompressible frames**:

```
segment := [flags u16][frameCount u16][frameIndex][frame 0][frame 1]...
frameIndex[i] := (compOffset u32, rawLen u32, firstBlock u16)
frame        := zstd(columnar-encoded F blocks)
```

Reading block B becomes: `seg = B/8192`, `frame = (B%8192)/F`, decompress one
frame. Cost drops by a factor of `8192/F`.

**Ratio protection.** Columnar compression wins by giving zstd long runs of
like-typed values, so shorter columns compress worse. Two mitigations, both
already proven inside this tree:

- **Pick F near the zstd window rather than far below it.** DATC's leaf
  segments measured this directly: 32 KiB frames were *4.5x slower at p50*
  than 256 KiB frames because per-frame decoder churn dominated, despite
  decompressing 4x less. Start at F = 256 blocks (~100-200 KiB/frame) and
  measure, do not assume smaller is better.
- **Train a zstd dictionary** over sampled segments and compress every frame
  against it. This is the standard recovery for small-block compression and
  should return most of the long-column advantage; the dictionary ships once
  per generation alongside the `.cidx`.

**Compatibility.** `frameCount == 0` means "legacy whole-segment layout", so
existing segments keep decoding on the old path and no regeneration is forced.
A regenerated `bodyc` costs about the same as this week's Step 1 body-compact
run (~6 min per 100k blocks, ~6 h for full history).

## 5. What (2) needs, separately

If n42-rs is meant to consume these formats at all, it needs one of:

- a written wire spec for the columnar layout and every flag bit, pinned by
  cross-language fixtures like `testdata/h2_v4_*.json` does for consensus; or
- a small C ABI over the Go reader; or
- an explicit decision that Rust consumers read geth ancient and these
  freezers stay a Go-side storage optimisation.

The third is a legitimate answer — but it should be a decision on record, not
the current situation where both consumers quietly fall back and the pipeline
keeps producing an artefact they do not read.

## 6. Measured 2026-08-31 — the trade-off is far better than assumed

Experiment: `internal/ethel/frame_experiment_test.go`, real segment 3051
(blocks 24,993,792..25,001,983, a dense recent region), re-encoded columnar at
each candidate frame size with the existing `encodeBodySegment`.

**Compression cost**

| F (blocks/frame) | frames | bytes | vs whole-segment |
|---|---|---|---|
| 8192 | 1 | 448,695,105 | baseline |
| 2048 | 4 | 450,537,263 | +0.41% |
| 1024 | 8 | 451,978,201 | +0.73% |
| 512 | 16 | 453,877,104 | +1.15% |
| **256** | 32 | 456,442,813 | **+1.73%** |
| 128 | 64 | 460,155,500 | +2.55% |
| 64 | 128 | 465,335,788 | +3.71% |

**Random-read latency** (decode + parse one frame, shared decoder + DecodeAll,
20 iterations)

| F | payload | best | avg | speedup |
|---|---|---|---|---|
| 8192 | 448.7 MB | 1.283 s | 1.635 s | 1x |
| 1024 | 57.5 MB | 169.7 ms | 185.7 ms | 8.8x |
| **256** | 13.6 MB | 39.5 ms | **45.7 ms** | **35.8x** |
| 64 | 3.78 MB | 9.79 ms | 10.85 ms | 150x |

**Latency falls essentially linearly with frame size** — the per-frame decoder
churn that made DATC's 32 KiB frames 4.5x SLOWER does not appear here, because
this reuses one decoder through `DecodeAll` instead of allocating per frame.
That earlier finding is what dictated the shape of the measurement.

### Recommendation: F = 256

+1.73% size for a 35.8x cut in random-read latency clears both acceptance
bars, and **no trained dictionary is needed** — the columnar layout already
groups like-typed fields, so zstd captures most of the redundancy inside a
100-200 KiB frame and the long-column advantage is far smaller than assumed.

At the real 2026-08-30 scale: **bodyc 4.65 GB -> 4.73 GB (+80 MB), random
block read 2.7 s -> ~46 ms.**

F = 64 is available if random access dominates (10.85 ms, +3.71% / +170 MB).
Matching geth's microsecond random reads would need F = 1, which surrenders the
ratio back to snappy levels — framing moves along a trade-off curve, it does
not erase the gap.

Caveat: measured while the DATC C2 build was running, so absolute times carry
some CPU contention. The relative comparison is unaffected (same machine, same
load, back-to-back).

## 6b. Every constant F touches

Two different things in this tree are called a "frame" and they are measured
in different units. Mixing them up is how the "32 KiB frames are 4.5x slower"
result gets misread as "F=32 blocks is slow".

**bodyc / headerc framing — F counts BLOCKS**

| constant | value | where | meaning |
|---|---|---|---|
| `bodyFrameSize` | **256** | `internal/ethel/body_frames.go` | blocks per bodyc frame |
| `headerFrameSize` | **256** (`= bodyFrameSize`) | `internal/ethel/header_frames.go` | blocks per headerc frame |
| `bodyFrameCacheSize` | **32** | `internal/ethel/body_frames.go` | frames retained; 32 x 256 = 8192 = one whole segment, so the worst case matches the old single-segment cache rather than exceeding it |
| `headerFrameCacheSize` | **32** (`= bodyFrameCacheSize`) | `internal/ethel/header_frames.go` | same, for headers |
| `frameIndexEntrySize` | **12** bytes | `internal/ethel/body_frames.go` | compOffset u32 + compLen u32 + blockStart u16 + blockCount u16 |
| `HeaderSegmentSize` | **8192** | `internal/ethel/header_compact.go` | blocks per SEGMENT — unchanged by framing, and shared by headerc and bodyc |
| `zstdSkippableMagic` | `0x184D2A50` | `internal/ethel/body_frames.go` | discriminates a framed payload from a plain zstd frame (`0xFD2FB528`) |
| frameCount `== 0` | — | payload header | legacy whole-segment layout; existing files keep decoding unchanged |

Candidate values measured before settling on 256: **8192** (whole segment,
the baseline), **2048**, **1024**, **512**, **256**, **128**, **64** — §6 has
the size and latency for each. **F = 1** is the theoretical end of the curve
(geth's per-entry snappy) and is not offered: it surrenders the ratio.

**DATC leaf/stroot segments — "frame" counts KiB of uncompressed payload**

| flag / constant | default | where |
|---|---|---|
| `leafFrameRaw` | **256 KiB** | `cmd/n42-datc/leafseg.go`, the `finalize-leaves --frame-kb` default |
| `--frame-kb` (finalize-leaves) | **256** KiB | `cmd/n42-datc/main.go` |
| `--frame-kb` (stroot-export) | **32** KiB | `cmd/n42-datc/stroot_seg.go` |
| `segFrameRawTarget` | set from `--frame-kb` | `cmd/n42-datc/main.go`, `stroot_seg.go` |

The DATC finding that 32 KiB frames were 4.5x slower at p50 than 256 KiB is
about THIS table, not the block-count one. It still shaped the bodyc
measurement — it is why §6 reuses a single decoder through `DecodeAll` rather
than allocating one per frame, and with that removed the per-frame churn does
not reappear.

## 7. Remaining work

### Original verification plan (superseded by §6)

1. Take one dense segment (e.g. the 25.0M region) and re-encode it at
   F ∈ {8192, 1024, 256, 64}, with and without a trained dictionary.
2. Report, per F: compressed size (vs today's 4.65 GB baseline scaled), random
   single-block latency, and full-segment sequential throughput.
3. Accept only a configuration that holds the ratio within ~2% of today while
   cutting random-block latency by at least 10x.
4. Re-run the 2026-08-24 witness-replay profile to confirm the Amdahl section
   actually disappears — the ratio and the latency are both means to that end.
