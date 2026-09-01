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
| 2 | `build --records-only --start 15220000 --end <tip+1>` | **~31-37 h**, ~325 GiB of node records + ~16 GiB sr |
| 3 | `cs-to-spill` + `finalize-leaves` for the 25.44M → tip tail | ~7 GiB |
| 4 | acceptance §4 (verify, proof-bench with `--out2`, stroot spot) | hours |
| 5 | fold the weekly `stroot-merge` back into the runbook §5 cadence | minutes/week |

D: has ~1,024 GiB free; the fresh continuation DB does not rewrite the old one,
so the old library keeps serving 0-15.22M throughout.

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

## 6. Weekly, once the catch-up lands

~525 leaves/block × ~100k blocks ≈ 52M leaves ⇒ **~35 min + ~8.7 GiB/week**,
placed after the runbook's Step 2b, followed by `stroot-merge` + `drop-table`.
Until then runbook §5 correctly reads "N/A — no DATC head advance".
