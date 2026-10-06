# DATC — pipeline, data classes, acceptance, and how to resume the build

> DATC (Depth-Adaptive Temporal Checkpointing) is the **archive-plus** tier:
> EIP-1186 proofs at ANY historical height, not just the tip. Design:
> `eip1186-mpt-proof-storage-research.md` §6. This doc is the operational
> counterpart — what runs, what each artefact is for, what gates it, and what
> it takes to finish the current library.
>
> **The toolchain was merged into this repo on 2026-08-30** (35 files + the
> `AccRootEmitter` hook in `lib/trie` / `commitment`, plus
> `DecodeStorageChangesFunc`), so `go build ./cmd/n42-datc` now produces every
> command below. Before pointing the merged binary at the production `--out`,
> run one read-only command with both it and the `D:/cherry-datc` build and
> confirm identical output — see the runbook §5b gate.

## 1. Where the library stands (measured 2026-08-30)

| Layer | Covers to | Where |
|---|---|---|
| node records (`DatcAccNode` / `DatcStorNode`) | **15,220,000** | `D:/n42-datc-bprime2-25m/mdbx.dat`, 464 GiB |
| leaf history (`a`,`s`) + change index (`ca`,`cs`) | **25,439,3xx** | `leafseg2/`, 406 GiB |
| `sr` storage-root segments | 15,220,000 | `leafseg2/sr.*.seg`, 22.3 GiB |
| cs-to-spill progress (`SMeta["prog"]`) | 25,439,371 | `csside/`, 98 GiB |

**Proofs are served out of the node records, so the library answers historical
proofs for 0-15,220,000 today.** The leaf layers running ahead is by design
(§2), not a deletion — searched 2026-08-30: no second DATC DB exists anywhere
on D:/E:/C:, and nothing DATC-shaped is in the Recycle Bin.

## 2. Two pipelines, deliberately decoupled

The original single-pass `build` does everything: replay changesets, maintain
the trie, gold-check every block's root against the real header, write node
records AND leaf/change rows. At mainnet density that build is the OOM-prone,
multi-day part. So the work was split:

### Pipeline A — leaf history, without EVM or trie

`cs-to-spill`'s own words: extends the leaf history "from the forward
changesets ALONE (no EVM, no trie, no 880 GB build MDBX)". The acctcs/storcs
rows ARE the leaf rows, only block-ordered instead of key-ordered.

```powershell
# A1. changesets -> spill  (resume comes from the side DB's SMeta["prog"])
n42-datc cs-to-spill --out D:\n42-datc-bprime2-25m `
  --changesets D:\N42-eth1177\chain\freezer `
  --spill leafspill2 --side D:\n42-datc-bprime2-25m\csside --end <tip>

# A2. spill + previous segments -> new segment generation
n42-datc finalize-leaves --out D:\n42-datc-bprime2-25m `
  --spill leafspill2 --seg-old leafseg2 --seg-out leafseg3 --frame-kb 32
```

Two per-block rules need pre-state the changesets do not carry — ghost-storage
drops and SELFDESTRUCT wipes. Both are served by (old segments' boundary floor)
∪ (the `csside` liveness overlay), mirroring `blockApply` exactly. That is what
`SAcct`/`SSlot` are for, and why `csside` is state, not scratch.

### Pipeline B — node records only, in a FRESH database

Resuming the 880 GiB build DB thrashes (random B+tree writes across the whole
file under Windows WriteMap). So the continuation forks just the head state
into a compact new DB whose working set is the new range only:

```powershell
# B1. copy live-state tables (HashedAccounts, HashedStorage, TrieOfAccounts,
#     TrieOfStorage) + DatcMeta stamps into a fresh MDBX, sequentially
#     (cursor walk + Append/AppendDup). Resumable; the source stays read-only.
n42-datc fork-state --src D:\n42-datc-bprime2-25m --dst D:\n42-datc-cont-<tip>

# B2. node records + DatcStoRoot + gold checks ONLY — leaf/chg rows already
#     exist from Pipeline A, so --records-only skips re-emitting them.
n42-datc build --out D:\n42-datc-cont-<tip> --records-only `
  --changesets D:\N42-eth1177\chain\freezer `
  --headers D:\n42-eth1\chain\freezer `
  --start 15220000 --end <tip+1> `
  --concurrent-root --leaf-seg --dirty.gb 16 --stocache.m 64 --gogc 400
```

Queries then overlay the two: `--out` (old DB: segments, ckpt, records
< forkedAt) and `--out2` (fresh DB: records/StoRoot ≥ forkedAt, tried first).

## 3. Data classes — what to keep, what is scratch

| Class | Item | Keep? |
|---|---|---|
| **Input** | `acctcs`/`storcs` (N42-eth1177), `headerc` (n42-eth1) | produced by the weekly runbook Steps 1-2 |
| **Deliverable** | `DatcAccNode` / `DatcStorNode` in `mdbx.dat` | proof main path |
| **Deliverable** | `leafseg*/a.*`, `s.*` | leaf history — `leafCursor`, as-of fold |
| **Deliverable** | `leafseg*/ca.*`, `cs.*` | change index — `chgCursor`, `nodeHashAt` |
| **Deliverable** | `leafseg*/sr.*` (+ mutable `DatcStoRoot` tail) | per-block storage root, O(1) |
| **State** | `csside/` (`SAcct`,`SSlot`,`SMeta`) | **resume marker + liveness overlay — never delete** |
| **State** | previous generation `leafseg`/`leafseg2` | the `--seg-old` input of the NEXT merge; reclaimable one generation back |
| **Scratch** | `leafspill*/` `.zspill` | consumed by `finalize-leaves`; RETAINED deliberately if any frame was corrupt |
| **Optional** | `ckpt/` | accelerator for early-block live keys; rebuildable, auto-gated above 4M |

Scratch sizing note: spill is written at `SpeedDefault`, segments at
`SpeedBetterCompression`, so scratch is ~10-20% larger than what it becomes.

## 4. Acceptance — what gates each stage

| Gate | Command | Passes when |
|---|---|---|
| **Per-block/window gold check** | built into `build` (`--headers`) | every window's incremental root equals the real header stateRoot. A clean run to `--end` IS the correctness gate; a mismatch halts |
| Localize a gold-check failure | `build --bisect` (read-only, never commits) | reports the FIRST divergent block |
| Converter equivalence | `cs-to-spill` over a slice INSIDE the built range, then `cs-spill-compare --out <dir> --spill <scratch>` | converter rows match the built segments key-for-key (the discipline that caught the StorChg clobber bug) |
| Sampled height verify | `verify --out <dir> --headers <hdr> --samples N --fold-depth 4` | sampled historical heights reconstruct; use non-boundary heights |
| Proof latency + correctness | `proof-bench --out <old> --out2 <cont> --n 200 --fold-depth 4` | all verified; p50 ~130-160 ms warm |
| sr merge | `stroot-merge --out <dir> --spot 2000` | 2000/2000 identical (segments ∪ table vs table); any divergence is fatal |
| Missing changesets | `build --scan-gaps` | no block with an empty acctcs+storcs but a changed header stateRoot |

## 5. To finish the library — steps, inputs, cost

Inputs already on disk: `acctcs`/`storcs` to 25,864,982 and `headerc` to
25,864,981 (this week's Steps 1-2), leaf layers to 25.44M, `csside` to 25.44M.

| # | Step | Cost |
|---|---|---|
| 1 | `fork-state` → `D:\n42-datc-cont-<tip>` | sequential copy of head state + trie, a few hundred GiB |
| 2 | `build --records-only --start 15220000 --end <tip+1>` | **see §5c — the first-draft estimate was 4.5x low** |
| 3 | `cs-to-spill` + `finalize-leaves` for the 25.44M → tip tail | ~7 GiB |
| 4 | acceptance §4 (verify, proof-bench with `--out2`, stroot spot) | hours |
| 5 | fold the weekly `stroot-merge` back into the runbook §5 cadence | minutes/week |

The fresh continuation DB does not rewrite the old one, so the old library
keeps serving 0-15.22M throughout. It does NOT fit on D: — see §5c.

Memory: same class as the N42-hashed migration, which required stopping the
fleet — `--dirty.gb 16`, `--stocache.m 64` (≈10 GiB), `--gogc 400`, plus the
mmap working set. Do not run it beside the fleet or the weekly replay.

Interrupt safety: Ctrl+C once finishes the current batch (commit + spill frame
cut) and exits resumably. **Never `kill -9`** — that truncates the in-flight
spill frame; the 2026-06-13 incident lost multi-day work that way, which is why
`finalize-leaves` now keeps the spill whenever it had to skip a corrupt frame.

## 5b. What the first real Pipeline-B attempt found (2026-08-30)

Pipeline B had never actually been run — the node records stop at 15.22M
precisely because nobody had executed it. The first attempt therefore hit six
things in a row, every one of which would have wasted a 31-37 h run:

| # | Finding | Fix |
|---|---|---|
| 1 | `fork-state` opens src+dst with the plain chaindata table set, so the DATC prototype tables are unregistered. The four state tables copy fine and the run dies **22 minutes in** on `meta: table: DatcMeta, mdbx_cursor_open: input/output error` | register the DATC tables on both ends (`datcTableCfg`, 2026-08-30) |
| 2 | `build` derives the epoch schedule from `--alpha/--cbar`; it does **not** read `sched`/`stoSched` from DatcMeta, even though `fork-state` faithfully copies them | pass them explicitly (below) |
| 3 | `--window` (default TRUE) requires every `e[d] % W == 0` with `W = e[1] = 1024`. The production schedule has `e[3]=1` (B-prime per-block dense), so window mode is impossible for this library | `--window=false` |
| 4 | `--concurrent-root` help says "Window/incremental mode only", which reads as "useless without --window". It is not — the per-block path takes the same branch (`if !b.fwdMode && b.concurrentRoot`) | keep `--concurrent-root` with `--window=false` |
| 5 | **Resume backfill exhausts RAM on BOTH paths.** Default changeset replay: 66.6 GB at 34% of a 2,637,088-block replay, extrapolating to ~196 GB. `--backfill-segs`: **94.1 GB** with the storage table not yet started. The cost is not decoding — it is the marks: `backfill(segs) acct: 256,186,102 rows -> 1,667,405,767 marks`, and both paths produce the same set. The epoch spans 2.64M blocks because `e[4]=e[5]=4194304` | `--backfill-dirty=false` — the reader tolerates a missing record via the leaf-history fold, so the hole **degrades query latency, not correctness** (`backfill.go` header) |
| 6 | `fork-state` output size was unmeasured ("a few hundred GiB" in this doc's first draft) | **measured: 64 GB, 22m21s** |

### The command that follows from all six

```powershell
$env:N42_DATC_LEAFSEG_DIR='leafseg2'
n42-datc build --out D:\n42-datc-cont-25864981 --records-only `
  --changesets D:\N42-eth1177\chain\freezer --headers D:\n42-eth1\chain\freezer `
  --start 15220000 --end 25864982 `
  --sched     64,1024,16384,1,4194304,4194304 `
  --sto-sched 64,1024,16384,262144,4194304,4194304 `
  --window=false --concurrent-root --backfill-dirty=false `
  --leaf-seg --dirty.gb 16 --stocache.m 64 --gogc 400
```

The two schedules are READ FROM the library, not chosen: they are
`DatcMeta.sched` and `DatcMeta.stoSched` of `D:/n42-datc-bprime2-25m`. Verify
them again before any future continuation — a mismatch silently produces
records incompatible with the existing ones.

## 5c. Measured output size — the estimate was 4.5x low (2026-09-01)

§5's first draft guessed "~325 GiB of node records + ~16 GiB sr". The first
real run measured it instead:

| | |
|---|---|
| range | 15,220,000 → 25,864,982 (10,644,982 blocks) |
| reached | 17,900,000 = **25.2%** |
| `mdbx.dat` | **432 GiB**, of which 64 GiB is the fork-state baseline |
| records written | **368 GiB for 25.2% of the range** |
| linear extrapolation | **~1,460 GiB of records, ~1,524 GiB total** |

The file was checked before trusting that number: `fsutil sparse queryflag`
reports it is NOT sparse, and 1 MiB probes every 32 GiB come back 56-92%
non-zero, so the 432 GiB is real data rather than a preallocated hole.

Linear is if anything OPTIMISTIC — the DeFi-dense back half carries more leaf
changes per block than the front, which is exactly why the builder reports
progress against the leaf count rather than the block number.

**D: has ~1,004 GiB free, so this build cannot finish on this box.** It would
wedge at roughly 85% of the range with no way forward except deleting another
dataset. Windows was also the wrong host for a second reason: MDBX under
WriteMap thrashes on this access pattern (the `fork_state.go` header says so),
and the run degraded from 96 to 17 blk/s while private bytes climbed to 102 GB
against 125.6 GB of RAM.

### Therefore: the build moves to n42dev (2026-09-01)

| | this box | n42dev |
|---|---|---|
| cores | 32 | 256 |
| RAM | 125.6 GB (binding) | 136 GB |
| free space | 1,004 GiB (insufficient) | **3.9 TiB** |

MDBX files are portable across the two — verified by opening a
Windows-written `chaindata` on n42dev and reading `ethel-last-block` back —
so the in-progress 432 GiB DB moves as-is and resumes at 17,900,000 rather
than restarting from the fork point.

What has to be copied, and what does not:

| | size | why |
|---|---|---|
| `mdbx.dat` (continuation) | 432 GiB | resume state |
| `acctcs` | 146.8 GiB | replay input |
| `storcs` | 278.5 GiB | replay input |
| `headerc` | — | already on n42dev (weekly publish) |
| `leafseg2` | — | **not read**: `--leaf-seg` only builds a spill writer in `--out`, and `putLeaf` returns early under `--records-only`; `--backfill-segs` is unused because `--backfill-dirty=false` |

857 GiB at a measured 92 MB/s ≈ 2.6 h. Two transfer settings mattered enough
to measure: the default ssh cipher moved 20-50 MB/s where
`chacha20-poly1305@openssh.com` moved 71, and 8 parallel streams saturate the
link where one does not. The transfer is chunked at 16 GiB with a per-chunk
marker so it resumes rather than restarting.

## 5d. There are TWO DATC lines and they disagree about the on-disk format

Found 2026-09-01 while another session asked whether the archive-plus
toolchain should be merged onto `origin/main`. Both lines branch from
`b78aaa54` and neither supersedes the other:

| | `origin/main` ("format v2", 2a0cae17 + 33e0c66c) | this line (archive-plus, fd4d03f0) |
|---|---|---|
| new files | 4 — `bench.go`, `db.go`, two e2e tests | 23 — the operational pipeline |
| `fork-state`, `cs-to-spill`, `stroot-export`, `checkpoint-build` | **absent** | present |
| storage domain | **32 B** (addrHash; "the legacy 8-byte incarnation suffix is gone") | **40 B** (addrHash + 8 trailing bytes) |
| `DatcMeta/format` | stamped `= 2` | **no stamp at all** |
| node record kinds | adds `nodeRecMixed = 0x02` | FULL / DIFF only |
| leaf & root-history keys | 4-byte block suffix (`blkLen`) | — |

**A 32-byte domain and a 40-byte domain are incompatible key layouts.** Both
`n42-datc-bprime2-25m` (1.1 TB) and the continuation were built in the 40-byte
format, and the old library carries **no format stamp**, so a v2 binary finds
no version to check: it does not fail, it writes mismatched keys. That is the
same failure class this document already flags for schedules — *a mismatch
silently produces records incompatible with the existing ones*.

The other direction is just as blocking: v2 has no `fork-state` and no
`cs-to-spill`, so it cannot build a library or continue one at all.

**Therefore carrying one line onto the other is a FORMAT MIGRATION decision,
not a merge conflict to resolve.** Either v2 grows a read path for 40-byte
records, or the 1.1 TB library is rebuilt. Whoever resolves the six
conflicting files (`emit.go`, `leafseg.go`, `main.go`, `proof.go`,
`verify.go`, `trie_root_computer.go`) must not decide it by accident.

Before pointing ANY `n42-datc` binary at a production `--out`, check which
line it is from — `n42-datc fork-state` exists only in this one:

```
wk-datc fork-state          # v2 prints the top-level usage; this line asks for --src/--dst
```

Related: the toolchain sat UNCOMMITTED in the working tree until 2026-09-01,
so a checkout of `b78aaa54` has 13 of the 36 files and no `fork-state`. Check
what a box actually has before trusting a DATC run on it.

## 5e. Heavy regions look like a hang. They are not (2026-09-03)

The v2 genesis-range build (Windows, blocks [0, 17,900,000)) hit a stretch
around block **12,853,784** where throughput fell by an order of magnitude and
stayed there:

| | before | in the heavy region |
|---|---|---|
| leaf rate | 30-66K lf/s | **4-9K lf/s** (median 4K over 60 heartbeats) |
| block rate | 120-150 blk/s | **6 blk/s** |
| leaf % gained | — | **0.2 pp in 72 minutes** (38,033 blocks) |

Everything that would indicate a hang says the opposite:

  CPU        225.6 CPU-seconds per 10 wall seconds = **22.5 cores busy**
  writes     2 MB / 10 s
  log        written 4 seconds ago
  threads    40, process healthy, zero errors
  memory     commit 46-54%, heap flat at ~34 GB

So it is compute-bound on the blocks themselves, not stuck. Block 12.85M is
mid-2021 mainnet — the densest DeFi period, with the largest storage tries.
The upstream runbook already hints at this class of region ("含一次 DoS 区 4
小时慢段" for the 0→4.67M sparse range), so heavy stretches are expected;
this records a second one, far later in the chain.

**Do not kill a build that looks stalled here.** Check, in this order: CPU
seconds advancing, log mtime, error count, commit %. If CPU is pinned and the
log is fresh, it is working.

**What it does to the ETA.** A single instantaneous `lf/s` reading is
worthless in this build — it swings between 4K and 66K. Track the MEDIAN over
~60 heartbeats instead, and treat entering or leaving a heavy region as the
event worth reporting. At 4K lf/s the remaining 9.5B leaves would take ~660 h
(27 days); at the pre-region 30-60K it is under a week. Until the region ends
there is no way to tell which, and quoting either number alone is misleading.

## 5f. The heavy region was GC, not work — measured, and fixed 13.8x (2026-09-05)

§5e recorded a stretch where throughput collapsed and said it was compute-bound
on dense blocks. **That was half right and the wrong half mattered.** The CPU
was pinned, but not by the build.

A 30-second CPU profile off the build's own `--pprof.port 6072` settled it:

| | before | after |
|---|---|---|
| CPU samples in 30 s wall | **700.67 s (23.2 cores)** | **101.24 s (3.4 cores)** |
| `runtime.gcBgMarkWorker` | **89.66% cum** | out of the top 12 |
| top entry | `runtime.gcDrain` / `scanObject` | **`flushTrieRootConcurrent` 67.3%** |
| `runtime.spanClass.sizeclass` | **73.58% flat** | — |
| real trie work | 5.2% | 67.3% |
| median leaf rate | **4K lf/s** | **55K lf/s** |
| Go heap | 42.4 GB (over the 40 cap) | 13.8 GB |

23 of 24 cores were doing GC mark-scan. The build was not slow; it was starved.

### The knobs, and why SHRINKING the cache was right

| flag | was | now | why |
|---|---|---|---|
| `--gogc` | 150 | **400** | fewer GC cycles — the most direct lever |
| `--mem.gb` | 40 | **80** | the heap was ALREADY 42.4 GB, i.e. over its own soft cap, which is what made GC frantic. Commit was only 53%, so the headroom existed |
| `--stocache.m` | 32 | **16** | **the root fix** |

Halving the cache looks backwards — it lowers hit rate. It is right because
**GC scan cost scales with OBJECT COUNT, not with hit rate.** `lfCache` held
29.6M entries, so every mark cycle walked ~30M live objects; that is what
`spanClass.sizeclass` at 73.58% flat is showing. Fewer objects beat more hits
by a factor of ten here.

Two earlier calls in this document's history were wrong, and the same mistake
produced both: reasoning about the cache as a HIT-RATE device.

  "lfCache converges at 57% and will not fill"  — it reached 92.5% and began
  evicting (`rb` 33.8M > cache 29.6M).
  "widening lfCache cannot help, it has never evicted" — true premise, wrong
  frame. The question was never hits; it was objects. The correct move was to
  shrink it, which nothing about hit rate would ever suggest.

`spanClass.sizeclass` at 73.58% flat is what pointed at object count. That is a
measurement, not an inference, and it is the signal to look for.

### How to stop a build that has no console

The process is started via `Start-Process` with output redirection, so it gets
**no console** — `CTRL_BREAK` cannot reach it and `taskkill` without `/F`
reports "only forceful termination". What DOES work is that `taskkill /PID`
still sets `stopRequested`, and `main.go:1168` checks that flag **only after a
batch commit**. At a few blk/s with `--batch 8192` that is up to ~30 minutes.

Waiting is the mechanism, not a workaround. Three minutes of no reaction was
misread here as "cannot be stopped gracefully"; the build then stopped cleanly
on its own at the next boundary:

```
[datc] graceful stop at block 13934592 (committed; spill cut at frame boundary).
Resume: re-run the SAME command -- --start is auto-loaded from saved progress.
(Spill retained.)
```

No corrupt-frame warning, 2,053 spill files / 239.1 GB retained, and the resume
picked up at exactly 13,934,592 with identical epochs. **Do not escalate to
`/F`** — that truncates the in-flight spill frame; the `kill-tail` recovery in
`leafseg.go:344` exists for when someone already has.

Only the three memory/GC knobs moved. `--sched`, `--acc-root-epoch`, `--batch`,
`--map.gb`, `--dirty.gb`, `--decode-workers` and `--end` were verified byte-
identical before restarting, because a schedule mismatch silently produces
records incompatible with what is already built.

## 6. Weekly, once the catch-up lands

~525 leaves/block × ~100k blocks ≈ 52M leaves ⇒ **~35 min + ~8.7 GiB/week**,
placed after the runbook's Step 2b, followed by `stroot-merge` + `drop-table`.
Until then runbook §5 correctly reads "N/A — no DATC head advance".
