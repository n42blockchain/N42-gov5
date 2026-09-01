# Weekly Data Update Runbook (eth-el derived data + DATC sr segments)

> Consolidated from `sync-runbook-2026-06-14.md` §5b/§5e and the weekly runs
> since. This is the OPERATIONAL doc: what to run every week, in order, with
> verification gates. All heights in the log section are point-in-time — the
> source of truth is each dataset's on-disk marker (§1).
>
> **What exists on disk, how far each dataset covers, and what is reclaimable:
> `data-assets-inventory.md`.** This doc is the process; that one is the state.
>
> House rules that apply to every step (see `data-pipeline-lessons.md`):
> long tasks run detached (`Start-Process`, never die with the session); never
> `kill -9` a writer (CTRL_BREAK for graceful stop); `N42_ETL_TMPDIR=D:/etl-tmp`
> for anything ETL-backed; capture raw output to a log file (no `| head` on RW
> ops); cold-cache/parallel-on-same-disk benchmark numbers are NOT trustworthy.

## 0. Cadence & scope

Weekly, after the live geth+CL have advanced the ancient freezer. Everything
auto-resumes from its marker to the new geth tip; a missed week just makes the
next delta bigger. Heavy CPU-bound steps run SERIALLY (two CPU-bound tasks on
one box: wall = sum, not max).

## 1. Step 0 — inventory (read-only, ~1 min)

```powershell
# source tip (geth ancient frozen; the week's target)
wk-block-stats.exe --ancient d:/geth/geth/chaindata/ancient/chain
# derived freezer heads
wk-freezer-info.exe --dir d:/n42-eth1/chain/freezer      # receipts/senders + headerc/bodyc (capacity!)
wk-freezer-info.exe --dir d:/N42-eth1177/chain/freezer   # senders/acctcs/storcs/witness
```

- Target height = `geth ancient frozen − 1` (valid indices).
- **headerc/bodyc cidx shows CAPACITY (segments × 8192), not actual coverage.**
  The generators now self-heal partial tails (probe last index → rewind), so
  just run them; don't trust the capacity number as a height.

## 2. Step 1 — the four light generators (serial chain, ~10 min)

Order matters only for senders-before-replay (senders are the replay's input
cache). All read geth ancient read-only and auto-resume; **omit `--start`**.

```powershell
$A='d:/geth/geth/chaindata/ancient/chain'
wk-ethexec.exe sender-recovery --ancient $A --datadir d:/n42-eth1     --workers 0
wk-ethexec.exe sender-recovery --ancient $A --datadir d:/N42-eth1177  --workers 0
wk-ethexec.exe header-compact  --ancient $A --datadir d:/n42-eth1
wk-ethexec.exe body-compact    --ancient $A --datadir d:/n42-eth1
wk-ethexec.exe receipt-copy    --ancient $A --datadir d:/n42-eth1     --workers 0
```

Typical rates: senders ~1400-1800 blk/s; headerc seconds; bodyc a few min;
receipts a few min. Partial columnar tail segments are detected and rewound
automatically (log line "final segment PARTIAL — rewinding").

## 3. Step 2 — witness/changeset EVM replay (HEAVY, detached, ~1 min per 1.5k blocks)

Extends acctcs + storcs + witness on N42-eth1177 (writes PlainState + Code as
a side effect). ~26-30 blk/s ⇒ a 60k-block week ≈ 40 min.

```powershell
Start-Process wk-ethexec.exe -ArgumentList '--ancient',$A,'--datadir','d:/N42-eth1177' `
  -RedirectStandardOutput D:\weekly-<date>-replay.log -RedirectStandardError D:\weekly-<date>-replay.err.log -NoNewWindow
```

Auto-resumes from `ethel-last-block`. Graceful stop = CTRL_BREAK; never hard-kill
(spill truncation). The freezer running 2-3 items ahead of the marker on resume
is normal (alignOnResume self-heals).

## 3b. Step 2b — txindex / TransactionLookup (weekly from 2026-08-30)

The tx-hash -> block index that serves `eth_getTransactionByHash`. Its source is
**geth ancient bodies**, so it does NOT wait for reth — it belongs with the
geth-sourced half of the week. It is CPU-bound (keccak + RecSplit), so it runs
AFTER the replay, never beside it.

Three published tiers (`internal/txlookup`, segments of 1,000,000 blocks):

| mode | txindex shipped | how it is built |
|---|---|---|
| **minimal** | none — the node indexes its own tail from the blocks it follows (`N42_TXINDEX_TAIL`) | not built, not published |
| **full** | last ~1 year | `--profile window --window-blocks 2600000` (writes `txindex.base`) |
| **archive** | full history from block 0 | `--profile archive` (base 0, no marker file) |

```powershell
$A='d:/geth/geth/chaindata/ancient/chain'
# archive tier — extends the published full-history index in place
wk-txindex-rebuild.exe --ancient $A --out d:/n42-eth1/chain/freezer --profile archive
# full tier — separate directory, one-year window
wk-txindex-rebuild.exe --ancient $A --out d:/n42-txindex-window-<tip> `
  --profile window --window-blocks 2600000
```

Both auto-resume; omit `--end` (defaults to the geth frozen tip). Set
`N42_ETL_TMPDIR=D:/etl-tmp` — RecSplit spills GBs per segment.

### Traps

1. **The final segment of every weekly run is PARTIAL, and resume arithmetic is
   in whole segments.** A partial tail counted as full resumes at the next 1M
   boundary and leaves those blocks unindexed forever — the same failure mode
   headerc/bodyc rewind against. `BuildRange` now peels partial tail segments
   and rewrites them (`partialTailSegments` + `SegmentStoreWriter.TruncateLastSegment`,
   covered by `TestPartialTailRewind` / `TestBuildRangeRewritesPartialTail`).
   The truncation shrinks the cdat as well, so the weekly tail rewrite does not
   leave orphaned frames inside a published artefact. **A pre-2026-08-30 binary
   does not have this** — rebuild from source (§6b rule 2) or the week silently
   indexes nothing.
2. **A window store keeps the base it was created with.** Once the one-year
   window slides past a 1M boundary the store covers more than a year and never
   shrinks (the builder logs a warning naming the extra blocks). Re-cut it by
   rebuilding the window tier from an empty directory — ~2.6 segments — not by
   deleting files from the live one.
3. **`full` and `archive` cannot share one publish root.** Both tiers land at
   the same in-datadir path (`chain/freezer/txindex.*`) with different content,
   and the manifest selector matches on that path. Hard-link a separate publish
   root per tier (same volume, instant) rather than trying to hold both.
4. **RecSplit tuning is not a free win.** `--enums` (default true) is documented
   as ~33.7 -> ~12 bit/key, but a previous measurement found Enums=true LARGER
   for this key distribution. Check the `bits/key` line the tool prints after
   the first segment before trusting either number. `--lfp=false` is only valid
   when the reader installs `Service.SetVerifier`.

### State as of 2026-08-30

The published index (`d:/n42-eth1/chain/freezer/txindex.*`, 24 full segments,
3,131,022,256 txs, ~13.2 GB) covers **[0, 24,000,000)** and was last built
**2026-04-13** — every publish since has shipped a four-month-stale index. The
first weekly run therefore has ~1.86M blocks of backlog (~2 segments) instead of
the ~0.1M steady state.

## 4. Step 3 — codes coverage bump (~min)

The replay already wrote new Code rows into N42-eth1177 MDBX. Re-export the
content-addressed full-history codes freezer and bump the coverage marker:

```powershell
wk-code-import2fz.exe --db d:/reth2k/db --outdir d:/n42-codes-<tip> `
  --coverage-block <tip> --addr-index=false
```

**Source is the reth `Bytecodes` table, not an N42 account join.** Codes are
content-addressed, so the join adds nothing and walks 405M accounts; the flag
that avoids it (`--addr-index=false`) is also silently ignored by a stale
binary, which is how one run burned an hour on the wrong path. reth2k must
have finished syncing first.

codes are FULL history — never window by recency (the hottest codes are the
oldest). Old codes dir stays until publish swap.

## 5. Step 4 — DatcStoRoot sr-segment weekly update (NEW, 2026-07-10)

Since 2026-07-10 the dense storage-root history lives in **static segments**
(`leafseg2/sr.*.seg`, 22.3 GB for 670M rows @15.22M) — the MDBX `DatcStoRoot`
table was dropped after a verified migration (`stroot-export`, spot 2000/2000,
bench 200/200). The reader (`storageRootAt`) serves from segments when present
and treats the MDBX table as the mutable tail.

**Weekly cadence (only in weeks where the DATC build head advanced):**

1. The DATC continuation build writes the week's new rows into the
   (auto-recreated) MDBX `DatcStoRoot` table — no config needed.
2. Merge the delta into the segments (streaming 2-way merge, bucket-level swap,
   kill-safe: old segments stay intact until the final swap phase):

   ```powershell
   $env:N42_DATC_LEAFSEG_DIR='leafseg2'
   n42-datc-ckpt2.exe stroot-merge --out D:\n42-datc-bprime2-25m --spot 2000
   ```

3. GATE: the built-in `--spot 2000` floor A/B must report 2000/2000 identical
   (it compares segments∪table vs table; any divergence is fatal).
4. Drop the merged-away table rows to keep the DB lean:

   ```powershell
   n42-datc-ckpt2.exe drop-table --out D:\n42-datc-bprime2-25m --table DatcStoRoot --yes
   ```

   (Pages go to the MDBX freelist; physical reclaim only via the next compact
   copy — not needed weekly.)
5. Optional deep gate after big weeks: `proof-bench --n 200 --ckpt-fold`
   (expect p50 ~130-160 ms warm, 200/200 verified).

Related tooling (same family, not weekly): `stroot-export` (one-shot full
migration of a frozen DB), `checkpoint-build` (early-block live-key ckpts —
build once for ≤4M only; larger sets are auto-gated and pay no rent),
`n42-chaindata-compact` (physical reclaim; 880→464 GB on 2026-07-10).

## 5b. DATC library head extension — why it is NOT weekly yet (2026-08-30)

> Full pipeline, data classes, acceptance gates and the resume procedure:
> **`datc-pipeline.md`**. This section is the sizing and the scheduling case.

DATC is the archive-plus tier: full-history EIP-1186 proofs at ANY height, not
just the tip. §5's `stroot-merge` is its weekly tail — and the reason that step
has read "N/A, no DATC head advance" for three weeks running is that the library
itself has not moved since 2026-07-10.

Its inputs are `--changesets D:/N42-eth1177` (Step 2's output) and
`--headers D:/n42-eth1` (Step 1's output), so it is a natural DOWNSTREAM stage
of this runbook and needs nothing from reth. The blocker is a ~31-37 h build,
not disk and not wiring — see the measured sizing below.

### Measured state (read from `DatcMeta`, not from prose)

| | value |
|---|---|
| `head` / `progress` | **15,220,000** |
| `leafprog` | **5,409,239,460** leaf changes (40.6% of the 13.33B full-chain workload) |
| `mdbx.dat` | 464 GiB (node records: DatcAccNode/DatcStorNode) |
| `leafseg2` | 428 GiB — `s` 159.7 + `cs` 139.7 + `a` 70.7 + `ca` 35.7 + `sr` 22.3 |
| total | **892 GiB**, untouched since 2026-07-10 |
| gap to this week's tip | **10,644,981 blocks / ~7.92B leaf changes** |

### Space and time — MEASURED 2026-08-30, not extrapolated

The first pass at this sized the catch-up by dividing every layer by
`leafprog` (5.41B — the NODE-record progress) and got 934-1306 GiB / 71-110 h.
That was wrong: the leaf layers run far ahead of the node layer. Measured
straight out of the segment footers (`key | block`, block = last 8 bytes
big-endian; every bucket's frame first-keys plus a full walk of each last
frame):

| layer | covers to | evidence |
|---|---|---|
| `a` account leaf history | **25,439,307** | 6 buckets sampled, keyLen 40 |
| `s` storage leaf history | **25,439,238** | 3 buckets sampled, keyLen 80 |
| `csside` `SMeta["prog"]` | **25,439,371** | cs-to-spill resume marker |
| `mdbx` node records (`DatcMeta.progress`) | **15,220,000** | the only layer still behind |

The cs→spill→finalize leg already ran to 25.44M and its output is on disk
(`leafspill2` is gone because the merge consumed it — what a clean finalize
looks like). What remains is the node-record leg, `sr`, and ~425k blocks of
leaf tail.

| remaining layer | basis | need |
|---|---|---|
| `mdbx` node records | **137 KiB/block measured** × 10.64M blocks | **~1,460 GiB** |
| `sr` storage roots | 1.54 KiB/block × 10.64M blocks | ~16 GiB |
| leaf tail 25.44M → 25.86M | ~525 leaves/block × 425k × 33 B | ~7 GiB |
| **total** | | **~1,524 GiB** |

**The 32.0 KiB/block figure in the row above was an estimate and it was 4.3x
low.** Measured 2026-09-01 on the real run: 368 GiB of records for the
15,220,000 → 17,900,000 stretch, i.e. 25.2% of the range, giving 137 KiB per
block. Linear extrapolation is if anything optimistic — the DeFi-dense back
half carries more leaf changes per block.

D: has 1,004 GiB free, so **this does not fit on this box**; it would wedge at
roughly 85% of the range. The build moves to n42dev, which has 3.9 TiB free
and 256 cores — see `datc-pipeline.md` §5c for what transfers and what does
not. Windows was the wrong host on a second count as well: MDBX under WriteMap
thrashes on this pattern, and the run degraded from 96 to 17 blk/s as private
bytes reached 102 GB against 125.6 GB of RAM.

Spill scratch is still a rounding error — it covers ~7 GiB of leaf tail, not
610 GiB of segments.

Corrected unit costs, for whoever sizes the NEXT extension: divide the leaf
layers by their real workload (~13.2B leaf changes to 25.44M, not 5.41B) and
`a`+`s` ≈ 18.7 B/leaf, `ca`+`cs` ≈ 14.2 B/leaf — about half what the wrong
denominator implied.

**Time**: 71-110 h assumed 7.92B leaf changes still had to be processed. They
do not. What is left is the node-record build over 10.64M blocks at the
July-measured 50-110 blk/s (93-95 with `--concurrent-root`) ⇒ **~31-37 h**.
Still a separate project rather than a weekly step, but a weekend rather than
most of a week.

**What is scratch vs artefact.** None of the artefact is scratch: all four
`leafseg2` tables are on the query path (`a`/`s` feed `leafCursor` for the
as-of leaf fold, `ca`/`cs` feed `chgCursor` so `nodeHashAt` knows which child
changed in which block), `sr` answers the per-block storage root, and the MDBX
node records are the proof's main path. The scratch is `leafspill/*.zspill`,
converted by `finalize-leaves` bucket by bucket (decompress, recompress to
`.seg`, delete the source). It is written at `SpeedDefault` and segments at
`SpeedBetterCompression`, so scratch runs ~10-20% larger than what it becomes,
and finalize deliberately RETAINS the spill if it skipped a corrupt frame —
both copies on disk at once. Small now; the rule still governs any full rebuild.

### The library dir holds pipeline STATE, not just the artefact (2026-08-30)

`d:/n42-datc-bprime2-25m` is 1,160 GiB, not the 892 GiB the artefact tables
account for. Before reclaiming anything from it, know what each subdir is —
two of them are live pipeline state and deleting either costs days:

| subdir | size | what it actually is |
|---|---|---|
| `mdbx.dat` + logs | 464 GiB | artefact: node records (proof main path) |
| `leafseg2` | 428 GiB | artefact: current leaf history + change index + `sr` |
| `leafseg` | 167 GiB | **input** to `finalize-leaves --seg-old leafseg --seg-out leafseg2` |
| `csside` | 98 GiB | **resume state** of `cs-to-spill`: liveness overlay (`SAcct`/`SSlot`) + `SMeta["prog"]` |
| `leafspill-eq*` | 1.1 GiB | equivalence-experiment residue (near-empty) |
| `ckpt` | 1.7 GiB | optional accelerator, rebuildable |

- **`csside` is not residue.** `cs-to-spill --side` defaults to `<out>/csside`
  and `--start 0` means "resume from side progress". Deleting it forces a
  re-scan of the whole changeset range and loses the pre-Cancun wipe-belt
  liveness overlay.
- **`leafseg` is the previous generation, but it is the seg-old INPUT** of the
  merge that produced `leafseg2`. It is reclaimable only once the next merge is
  confirmed to read `--seg-old leafseg2`; it is not "an unused old copy".

**And `SMeta["prog"]` reads 25,439,371 — while `DatcMeta.progress` reads
15,220,000.** The cs→spill leg of the catch-up already ran to 25.44M; only the
node-record leg is at 15.22M. If `leafseg2` really carries leaf history to
25.44M (its merge consumed a `leafspill2` that is no longer on disk, which is
what a successful finalize looks like), then most of the ~610 GiB of "new
segments" in the sizing above is ALREADY SPENT, and the remaining catch-up is
the node-record layer plus `sr` — a much smaller job than 934-1306 GiB.

**Measured 2026-08-30** (see the sizing section): `leafseg2` carries leaf
history to 25,439,3xx, so the merge that consumed `leafspill2` did complete and
`leafseg2` is self-sufficient — the next `finalize-leaves` takes
`--seg-old leafseg2`. That makes `leafseg` (167 GiB) reclaimable. `csside`
stays: it is the cs-to-spill resume marker, not residue.

### The DATC toolchain is now IN this repo (merged 2026-08-30)

It used to live only in `D:/cherry-datc`, which meant this repo could not build
`stroot-merge` / `drop-table` (§5's weekly commands) at all, and its
`leafSegDir` constant would have written a second segment generation into the
production library with no error.

Merged: 22 files brought over (`cs_to_spill`, `stroot_seg`, `fork_state`,
`drop_table`, `checkpoint_*`, `proof_bench`, `spill_heal`, `backfill`, …) plus
three lower layers the pipeline needs — `lib/trie/hashbuilder.go` and
`trie_root.go` (an OPTIONAL `AccRootEmitter` hook, nil by default, two guarded
call sites — this is what feeds DatcStoRoot), `commitment/trie_root_computer.go`
(`SetAccRootEmitter`), and `internal/ethel/changeset_codec.go`
(`DecodeStorageChangesFunc`).

The merge was NOT a copy: the divergence was two-way. This repo's
`--src n42` path had been fixed to read `rawdb.ReadCurrentFullBlockNumber`
(HeadBlockHash) while cherry still called `ReadCurrentBlockNumber`
(HeadHeaderHash) — and the accessor's own comment says state/proof consumers
must use the former, because leader-driven consensus advances the committed
head independently of the header head. That fix was carried forward.

Gates run: `go build ./...`, `go vet`, and `go test` on `lib/trie`,
`modules/state/commitment`, `internal/ethel/...` and `cmd/n42-datc` — all
green, i.e. the stateRoot path is unchanged with the hook unarmed.

**Two-binary agreement gate: PASSED 2026-08-30.** `verify --samples 6 --seed 7`
against the production library, merged build vs `n42-datc-ckpt2.exe` (the
2026-07-10 cherry build): all six heights returned identical root, `recs`,
`folds` and `leafReads` — including N=2,069,750, which really exercises the leaf
path (`folds=177 leafReads=1190346`) and the `sr.*.seg` storage-root reader.
Only wall time differed (cache warmth). The merged binary may be pointed at
`--out`.

### Time and memory

- **~31-37 h of continuous build** (node-record leg over 10.64M blocks; see the
  measured sizing above — the 71-110 h figure that stood here assumed the leaf
  layers still had to be rebuilt, and they do not). Still too long for a weekly
  window: a separate project, run in resumable chunks.
- Memory is the same class as the N42-hashed migration, which needed the fleet
  stopped: `--dirty.gb 16` of MDBX DirtySpace, `--stocache.m` (8M ≈ 1.2 GB,
  raised to 64M ≈ 10 GB to cut late-block read-back), `--gogc 400`, plus the
  mmap working set of a 464 GiB `mdbx.dat`. `--concurrent-root` adds 16 per-worker
  RoTx and a StateOverlay on top. Do not run it beside the fleet or beside the
  weekly replay.

### What weekly looks like AFTER the catch-up

Steady state is small and belongs right after Step 2b: ~525 leaves/block × 100k
blocks ≈ 52M leaves ≈ **35 min**, ~8.7 GiB/week, then §5's `stroot-merge` +
`drop-table` finally has a delta to fold. Until the catch-up lands, §5 stays
"N/A — no DATC head advance" and that line is correct, not an oversight.

## 6. Deferred / conditional items (not every week)

| Item | Trigger | Tool | Notes |
|---|---|---|---|
| snapshot regen (minimal/full) | needs >60 GB free RAM window | `reth-snapshot-export` | per MEMORY, deferred |
| anchors / bpp | when publishing stateless mode | `blockproof-produce` | ~1.5-2 h per 60k blocks |
| N42-hashed state | when eth-el follower redeploys | `n42-migrate-reth-hashed` (reth copy) or MerkleStageIncremental (changesets) | reth2k must be idle |
| manifests | at publish | `cmd/n42-eth-manifest` | blake3 per file; source = a hard-link publish root, NOT the E: test dirs (they drop `*.val.zst`) |
| DATC library head extension | separate project (node records @15.22M; leaf layers already @25.44M) — sizing in **§5b** | `n42-datc build` (resumable, cherry-datc binary) | ~1,524 GiB — moved to n42dev, see datc-pipeline.md §5c; sr merge (§5) rides on it |

## 6b. Time budget — take the short path, it is also the correct one

A full cycle is ~5 h wall clock and almost all of it is two jobs. Do not
spend the operator's time on anything else.

| Step | Cost | Notes |
|---|---|---|
| Step 0 inventory | 1 min | |
| Step 1 four generators | ~5 min | senders 40 s each, bodyc ~3 min, receipts ~1.5 min |
| Step 2 replay | ~26 min | ~31 blk/s |
| Step 2b txindex | ~17 min per 1M-block segment | tail segment is rebuilt every week; archive + window tiers are separate builds |
| Step 3 codes | ~11 min | **always `--db d:/reth2k/db` + `--addr-index=false`** |
| snapshot export | ~2 h | phase A scan is disk-bound; B ~9 min; C zstd ~30 min |
| N42-hashed migration | ~2 h | |
| three-mode assembly | ~2 min | same-volume moves are instant; see below |

Rules that keep it at 5 h instead of 8:

1. **codes come from the reth Bytecodes table, never from an account join.**
   `wk-code-import2fz --db d:/reth2k/db --outdir d:/n42-codes-<tip>
   --coverage-block <tip> --addr-index=false`. The account-join path walks
   405M accounts for an artefact that is content-addressed anyway.
2. **Rebuild every `wk-*.exe` from source BEFORE starting.** They are not
   auto-built. A stale binary silently ignores newer flags (that is exactly
   how the 405M-account join happened once), and you only find out an hour in.
3. **Do not benchmark, diff or cross-compare inside the weekly run.** Verify
   with the cheap gates in §7 (markers, freezer items, one witness spot-check)
   and move on. Anything else belongs in a separate session.
4. **Assemble the E: test dirs with same-volume moves, not copies.** Moving
   360 bodyc files between two E: directories is a metadata rename: 0.2 s
   versus ~20 min of copying.
5. **Serialize only what shares a device.** snapshot phase A/B and the
   migration both read the 2.1 TB reth MDBX, so those two must not overlap;
   phase C (local zstd) does not, and a second job may start there if the
   machine is otherwise idle.

### The publish root is NOT an immutable snapshot (2026-08-30)

The hard-link assembly is right about space — 756 files, 1131.6 GiB logical,
~zero extra bytes — but a hard link shares an INODE, and the weekly generators
APPEND to the last `.cdat` of each table and OVERWRITE each `.cidx` in place.
So the moment the next week runs, the published root changes underneath the
manifest that was cut from it.

Measured on `d:/n42-publish-25765565` right after this week's Step 1 + Step 2:

| manifest | files | size no longer matching |
|---|---|---|
| archive | 476 | **6** — `bodyc.0288.cdat`, `bodyc.cidx`, `headerc.0002.cdat`, `headerc.cidx`, `witness.0095.cdat` (741 MB → 2.0 GB), `witness.cidx` |
| full | 173 | **3** |
| minimal | 97 | 0 (snapshot only — the weekly run never touches it) |

Every changed file is an active tail: a table's `cidx`, or the last `.cdat`
still being appended to. Step 2b adds two more (`txindex.cidx` plus the last
`txindex.*.cdat`). The three manifestIDs recorded for 2026-08-16 therefore no
longer verify.

Fix, pick one at assembly time:

1. **Isolate the active tails** — COPY each table's `cidx` and its newest
   `.cdat`, hard-link everything else. Costs ~10.5 GiB (cidx ~465 MB total,
   5-6 tail segments up to 2 GB each) and makes the published tree genuinely
   immutable. Preferred.
2. **Do not keep the publish root** — assemble, cut the manifest, upload/seed,
   delete. Only viable when nothing needs to seed from it long-term.

Either way, re-verify the manifest against the tree immediately before
publishing; a size-only pass over the file list catches this in seconds.

### Mode scoping — full is a ONE-YEAR window, not everything

Getting this wrong wastes ~600 GB of copying and produces a mode that is not
the product. Authoritative spec (operator decision, 2026-07-02):

| Mode | Contents | Size |
|---|---|---|
| **min** | snapshot (16 shards, `.val` only) + headerc (all) + codes (all) | ~47 GB |
| **full** | min + **last ~1 year of bodyc** (the last ~56 segments) + retrimmed latest-only receipts + txindex | **~160 GB** |
| **archive** | migrated N42-hashed chaindata + headerc + **full-history bodyc** | ~790 GB |

`bodyc` file numbering is monotonic in block height, so the one-year window is
simply the last ~56 `bodyc.NNNN.cdat` segments (~118 GB at the 2026-08 tip);
the full `bodyc.cidx` is copied with them and the resulting gap is tolerated by
design (`ErrBodyTrimmed` + ColdResolver, no `start` field needed).

Known gap in the 2026-08-16 assembly: full carried the one-year bodies but not
yet the retrimmed receipts / txindex, which affect RPC completeness rather than
catch-up. txindex is closed as of 2026-08-30 — it is now Step 2b, with its own
per-tier window rule. The retrimmed receipts still wait on the retrim artefact.

## 7. Verification gates (every week)

- Four light items: log lines report resumed-from == prior marker and
  complete-to == geth tip; freezer-info re-probe shows Items() == tip+1.
- Replay: `ethel-last-block == tip`; witness spot-check
  (`witness-block-trace` on 2-3 sampled new blocks reproduces gasUsed).
- Full state-root GATE (`verify-root --workers 16`, ~1 h) — run after LARGE
  catch-ups or when the replay logged anomalies; not needed for a clean small week.
- txindex (§3b): the tool's own stats line reports segments and bits/key; the
  tail segment's blockCount must equal `tip - base - (segments-1)*1e6`, i.e. the
  run must have REWRITTEN the partial tail rather than skipped it (log line
  "final segment PARTIAL — rewinding"). Spot-check one tx hash from a block in
  the newly indexed range through `txlookup.Service`.
- DATC sr merge: built-in spot gate (§5.3).

## 8. Run log

### 2026-08-30 (geth + reth sourced; three long-standing gaps closed)

Source: geth ancient frozen **25,864,982** -> target **25,864,981** (prior week
25,765,565; d **99,416**). reth2k finished syncing mid-session at exactly
25,864,981 — zero gap, no unwind (read from `BlockBodyIndices`, not `r.bat`).
Both left STOPPED. All `wk-*.exe` rebuilt from source first; that mattered more
than usual, since main had merged the witness-replay optimisation branch and a
batch of VM/state/commitment changes since 08-16.

- Step 1: senders 1m10s / 1m07s (~1450 blk/s), headerc 1.7s, bodyc 5m37s
  (4.65 GB, 27.3% of raw RLP), receipts 3m42s. Both headerc and bodyc
  auto-rewound their partial tail (segment 3145). All exit 0.
- Step 2 replay: **1h06m35s @ 24.9 blk/s** — SLOWER than 08-16's 31.9 blk/s
  despite the optimisation merge; bufFlush stayed 2m20s-3m10s throughout, so
  async witness flush is still the bottleneck and this week's blocks are
  heavier (250-380 tx in the tail). `ethel-last-block = 25864981`.
  Do not judge the rate from the opening minutes — it starts near 44 blk/s.
- witness spot-check: 25,800,000 / 25,840,000 / 25,864,981 all **gas diff +0**.
  GATE PASS. (The codes-freezer it auto-detects is the June orphan; correct
  anyway because misses fall through to the MDBX Code table.)
- **Step 2b txindex — FIRST weekly run.** The published index had not moved
  since 2026-04-13, covering only [0, 24,000,000). archive tier extended to 26
  segments / **3,706,813,771 tx** / 15.24 GB in 1h12m43s; window tier (full's
  one-year cut) built fresh: 3 segments from base 23,000,000, 798,871,140 tx,
  4.38 GB, 1h33m15s. Gate: tail segment blockCount 864,982, total 25,864,982.
  - The tool's own stats line reported "941.43 GB / 2031.78 bits/key" for the
    archive tier because `reportStats` summed EVERY `.cdat` in `--out`, which is
    now a shared freezer dir. Fixed.
  - Measured bits/key: archive 35.3 (mixed old Enums=false + new Enums=true),
    window 43.88 (all Enums=true). **Enums=true is LARGER here**, contradicting
    the tool's own "~12 bit/key" comment — trust the measurement.
- Step 3 codes: **2,719,173** (+45,983), 6.24 GB, 15m09s, hidx 1.71 bits/key,
  `codes.coverage=25864981`.
- snapshot export (16 shards, `d:/n42-snapshot-25864981`): accounts
  **414,474,374**; storage **1,642,352,754**; `.idx` 1.71 bits/key, `.idx+.ef`
  7.87 bits/key, `.val` 29.64 GB -> `.val.zst` 22.81 GB (storage 75.4%
  retained — identical ratio to every prior run). 1h55m20s, 129 files, 55 GB.
- N42-hashed migration (`--dst D:/N42-hashed-25864981/chaindata`): acc
  414,474,373 (decodeFail 0) / sto 1,642,352,754 (shortVal 0) / tacc 30,942,707
  / tsto 144,255,228 (badSub 0) / code 2,719,173 (skipped 0, decodeFail 0) /
  **vtrie OK: root == expect 0x55ac2baa…3451** / `ethel-last-block=25864981`.
  2h06m01s, 160 GB. The acc/sto/code counts match the snapshot export and the
  codes export EXACTLY — three artefacts, two independent paths, same numbers.
- **history (accthist + storhist) rebuilt** — first time since 2026-06-06.
  `accthist` was COPIED from `D:/n42-release` into `d:/n42-eth1/chain/freezer`
  so both halves finally live in one chain dir (a hard link would have let the
  peel truncate the June release). Both peeled their partial segment 25
  (`claimedCoverage=26000000 endBlock=25864982`) and rebuilt: accthist 41.4M
  keys / 2m45s, storhist 170.1M keys / 8m33s, 23m27s total, 26 segments each,
  40.6 GB. **The RPC still needs a reader fix** — see the inventory: Lookup
  consults only the segment containing blockN.

Three defects fixed this run, all the same shape — segment stores resume in
WHOLE segments while a weekly run always ends mid-segment:

| Where | Symptom | Detection |
|---|---|---|
| `txlookup.BuildRange` | index frozen at 24M since April | segment header `blockCount` |
| `cscompact.BuildFromBlockKeys` | accthist/storhist frozen since June | positional (header has no blockCount) |
| `cscompact.BuildFromChangesets` | same defect, untriggered | positional |

`SegmentStoreWriter.TruncateLastSegment` (new) does the peel and shrinks the
cdat, so weekly tail rewrites leave no orphaned frames in a published artefact.
`HistoryAccumulator` has the same arithmetic but no non-test callers; annotated
rather than changed. Both regressions were verified to FAIL before the fix.

Also this session, off the critical path:

- `minimalSelector` now ships headerc + codes. The published minimal manifest
  was 97 files / 24.6 GB (snapshot only) against a 47 GB spec — the three-mode
  tests passed only because the test dirs were hand-assembled with both.
- **The DATC toolchain was merged from `D:/cherry-datc` into this repo** (§5b).
- **The publish root is not immutable** (§6b): 6 of archive's 476 files already
  differ from the 2026-08-16 manifest, because hard links share inodes and the
  weekly run appends to each table's last cdat and overwrites its cidx.

- **Three-mode test (E:, catch-up + live)** — serialized on :30403 / 20115,
  each started from H0 = 25,864,981 with `set-progress`:

  | mode | size | head reached | batches | live rounds | mismatch |
  |---|---|---|---|---|---|
  | minimal | 46.6 GB | 25,870,750 (+5,769) | 26 | see note | 0 |
  | full | 368.1 GB | 25,870,958 (+5,977) | 16 | **7 consecutive** | 0 |
  | archive | 174.8 GB | 25,871,143 (+6,162) | 17 | **9 consecutive** | 0 |

  full and archive both PASS the §5 acceptance bar (>= 4-5 consecutive live
  rounds each importing the next block, no state-root mismatch). Each mode
  exercised different fresh artefacts: minimal the snapshot + codes, full the
  one-year bodyc window + the WINDOW-tier txindex + the rebuilt accthist/
  storhist, archive the migrated hashed-canonical chaindata plus headerc for
  freezer-direct BLOCKHASH.

  **minimal note — a peer reporting a bogus tip stalls the follow loop.** After
  importing cleanly to 25,870,750 the run logged `remaining=3286114`, i.e. some
  peer advertised a head ~3.3M blocks above mainnet, and the node then made no
  further import attempt for 13 minutes (only peer add/drop lines). full and
  archive, on the same peer pool minutes later, got correct `remaining` values
  and followed the tip normally — so this is an occasional bad peer, not a
  systematic failure, but the follow loop clearly has no sanity bound on an
  advertised tip. Worth a guard (compare against the median peer head, or
  against wall-clock-implied height).

  Assembly notes: full's base was copied from the minimal datadir WHILE minimal
  was running — its chaindata was being written at the time. It worked, but
  copy from a stopped datadir. full here carries FULL receipts (181.6 GB) since
  the retrimmed artefact still does not exist; the published full tier does not
  ship receipts at all, so the test dir is larger than the product.

- NOT run: DATC sr merge (§5, no DATC head advance yet), anchors, publish. geth
  + reth2k left STOPPED; fleet was already down.

### 2026-08-16/17 (full cycle: geth + reth sourced + three-mode test)

Source: geth ancient frozen **25,765,566** -> target **25,765,565** (prior week
25,715,848; d **49,717**). reth2k synced to exactly the same height, so no
unwind was needed (verified by reading BlockBodyIndices, not `r.bat`). Both
left STOPPED. All `wk-*.exe` rebuilt from source first — `wk-ethexec`,
`wk-block-stats` and `wk-code-import2fz` were all July 10 binaries against
August sources.

- Step 1: senders 42 s / 37 s, headerc 0.8 s, bodyc 2m48s, receipts 1m34s
  (528 blk/s). All exit 0.
- Step 2 replay: ~26 min @ ~31 blk/s. `ethel-last-block = 25765565`. The
  progress log trails the real head — it stopped printing at 25,760,000 while
  the run had finished; check the marker, never the log tail.
- Step 3 codes (reth2k Bytecodes -> `d:/n42-codes-25765565`): **2,673,190**
  codes (+25,327), 6.2 GB @ 43.8%, 10m50s, `codes.coverage=25765565`,
  hidx 1.71 bits/key.
- snapshot export (reth2k PLAIN -> `d:/n42-snapshot-25765565`, 16 shards):
  accounts 410,774,174; storage 1,632,922,836; `.idx+.ef` 1.50 GB
  (7.87 bits/key); `.val` 25.17 GB -> `.val.zst` 18.99 GB (**75.4% retained,
  byte-identical ratio to previous runs**); 1h57m, heap < 460 MB.
- N42-hashed migration (`--dst D:/N42-hashed-25765565/chaindata`): acc
  410,774,174 (decodeFail 0) / sto 1,632,922,836 (shortVal 0) / tacc
  30,671,868 / tsto 143,485,906 (badSub 0) / code 2,673,190 (decodeFail 0) /
  **vtrie OK: root == expect 0xa36c3bf8...de67d93c** / `ethel-last-block=25765565`.
  2h04m. The code count matches the Step 3 export exactly — two independent
  paths agreeing.
- witness spot-check: blocks 25,720,000 / 25,745,000 / 25,765,565 all
  **gas diff +0**. GATE PASS.
- **Three-mode test (E:, catch-up + live)** — all three PASS, serialized on
  :30403 / 20115:
  - **min** (47 GB): H0 -> 25,767,377 (network tip) via snapshot-direct
    overlay, RebuildState skipped. 0 critical errors.
  - **full** (161 GB): -> 25,767,507 (tip). 0 critical errors.
  - **archive** (791 GB): -> 25,767,633 (tip). 0 critical errors on the
    second attempt — see the mapsize trap below.

Two mistakes worth not repeating:

1. **full was assembled with the full-history bodyc (672 GB).** full is a
   ONE-YEAR window (§6b): the last ~56 `bodyc.NNNN.cdat` segments, here
   `0303..0358` = 117.7 GB, giving a 161 GB datadir that still caught up to
   the tip. The full-history bodyc belongs to archive. The correction cost
   almost nothing because same-volume moves are renames (0.2 s for 360
   files) — copying would have cost ~20 min.
2. **archive died mid-catch-up on `MDBX_MAP_FULL`** because
   `--storage.mapsize.gb` was left at its 64 GB default against a 156 GB
   chaindata. The warning exists in `weekly-runbook-full.md` but sits ~35
   lines below the archive example command, which is exactly how it gets
   missed. Pass it in every mode; it is now in the example itself.

- **manifests published** (`d:/n42-publish-25765565`, a hard-link assembly of
  the four source dirs — same volume, instant, zero extra space, byte-identical
  to the artefacts):

  | mode | files | total | manifestID |
  |---|---|---|---|
  | minimal | 97 | 24.59 GB | `5586730f15ad7423…` |
  | full | 173 | 165.51 GB | `b56e785c2ab2b0c5…` |
  | archive | 476 | 830.24 GB | `b0964df3afe94446…` |

  Two traps here. The E: three-mode test dirs are NOT a valid manifest source:
  they deliberately drop `snapshot/*.val.zst` (the node mmaps the raw `.val`),
  which is precisely the payload the manifest publishes — a dry run showed 0
  `.val` matches. And the first `full` manifest came out at 476 files /
  677 GB because the one-year bodies rule lived only in assembly prose; the
  selector now enforces it (`Section.WindowSegments`, 74c9f773).
- NOT run: DATC sr merge (no DATC head advance), anchors/bpp, retrimmed
  receipts + txindex for full (RPC completeness, not catch-up).
- qs fleet (the n42 self-developed chain — a different product) left RUNNING
  throughout; it shares no data with this pipeline.

### 2026-08-09/10 (this run — full cycle: geth + reth sourced + three-mode test)

Source: geth ancient frozen **25,715,849** → target **25,715,848** (prior week
25,626,781; Δ **89,067**). geth STOPPED (last freeze 08-09 03:26). reth2k synced
by the operator to exactly 25,715,848 (`--debug.tip 0x9499348c…`, zero gap this
time), then stopped.

- senders (both dirs): → 25,715,848, 51 s each @ ~1725 blk/s. ✅
- headerc 1 s; bodyc 5m39s (4.27 GB, 61.5% below geth snappy); receipts 3m58s
  @ 373 blk/s. Two partial tail segs auto-rewound. ✅
- replay acctcs/storcs/witness: 25,626,782 → 25,715,848, 1h15m @ ~20-44 blk/s
  (async witness flush the bottleneck as usual). `ethel-last-block = 25715848`. ✅
- witness spot-check WORKS again (tool regression of 07-22 not reproduced):
  blocks 25,650,123 / 25,690,456 / 25,715,848 all gas diff **+0**. ✅ GATE PASS
- codes freezer (reth2k Bytecodes → `d:/n42-codes-25715848`): 2,647,863 codes
  (+48,608), 6.1 GB @ 43.7%, 11m49s, `codes.coverage=25715848`, hidx 1.71 b/key.
  **Trap hit**: the stale `wk-code-import2fz.exe` (Jul 10) predates
  `--addr-index=false` and silently ignored it (positional arg parsing), walking
  the 405M-account join; killed, rebuilt from source, re-ran clean. ✅
- snapshot export (reth2k PLAIN → `d:/n42-snapshot-25715848`, 16 shards):
  accounts 409,031,271 rows; storage 1,628,233,614 rows, idx 332 MB @ 1.71
  bits/key, val 25.1 GB (zst 18.9 GB). **Trap hit**: first storage pass died
  mid-scan when its bash wrapper was reclaimed by the session — the exe was a
  child of the shell. Long jobs must be `Start-Process`-detached (the replay
  survived the same reclaim precisely because it was). Accounts phase was
  already complete; storage re-ran detached, 1h21m. ✅
- N42-hashed migration (`--dst D:/N42-hashed-25715848/chaindata`, versioned dir,
  old kept): acc 409M / sto 1.63B / code 2,647,863 (decodeFail 0) /
  **vtrie OK: root == expect 0x5d765729…1cb810** / `ethel-last-block=25715848`.
  Fleet was already down — no OOM window needed. ✅ GATE PASS
- **Three-mode test (E:, catch-up + live)** — all three PASS, serialized on
  :30403 / 20115, each CTRL_BREAK-stopped clean:
  - **archive** (chaindata 156 GB + headerc): caught up +5,903 blocks in 31 min,
    then ~45 min live, per-block tExec 61-535 ms, `eth_blockNumber` advancing.
  - **minimal** (snapshot 97 files + headerc + codes + set-progress marker):
    caught up +6,175 in ~10 min (snapshot-direct overlay), ~39 min live clean.
  - **full** (minimal + bodyc 627 GB): caught up +6,435 in ~12 min, live clean.
  - Zero `state root mismatch` / `diverge` / `error` / `panic` in all three logs
    (after excluding routine network-ID/genesis peer-filter drops).
- NOT run: DATC sr merge (§5, no DATC head advance), anchors/bpp, manifests.
  geth + reth2k left STOPPED; fleet left STOPPED (was already down).
- Assembly notes for next time: `.val.zst` excluded from E: snapshot copies
  (mandatory); `set-progress`/`headcheck` use `ethel-last-block` while
  `read-progress` prints the legacy `DbInfo/ethel_progress` key — verify markers
  with headcheck, not read-progress.

### 2026-07-22 (geth-sourced part; reth-sourced deferred)

Source: geth 1.17.4 ancient frozen **Items=25,587,116** → target last block
**25,587,115** (prior week 25,503,167; Δ **83,948**). geth was STOPPED (last
freeze 04:44); freezer-heads is the authority (strips the geth cidx sentinel:
rawItems 25,587,117 → real 25,587,116).

Both geth-sourced AND reth-sourced steps ran this session (reth2k finished
syncing mid-session). reth2k head = **25,587,083** (its fixed `--debug.tip`
`0x00017439…8762`, byte-for-byte == geth's canonical block 25,587,083 hash —
cross-client verified; 32 blocks behind geth's 25,587,115 freeze, immaterial: the
follower advances forward from the base state / geth changesets bridge the gap).

- senders (both dirs): 25,503,168 → 25,587,116, ~70 s each @ ~1187 blk/s. ✅
- headerc: partial-tail seg 3113 rewound, → 25,587,115, 11 segs, 1 s. ✅
- bodyc: partial tail seg 3113 rewound, → 25,587,115, 354 files, 7m21s
  (4.32 GB, 28% of raw RLP). ✅
- receipts: → 25,587,116 (4m20s @ 322 blk/s). ✅
- replay acctcs/storcs/witness: 25,503,168 → 25,587,115 (~1h9m @ ~15-24 blk/s,
  async witness-buffer flush the bottleneck; blk/s dips to 15 on heavy-storage
  tail). All four N42-eth1177 tables Items()=25,587,116; PlainState+Code written
  as side effect. Clean exit, no anomalies. ✅ (the replay verifies gasUsed
  per-block inline as it executes, so a clean run to tip is itself the exec gate.)
- GATES: (a) freezer-heads all four tables = 25,587,116 ✅; (b) ethel-last-block
  = 25,587,115 ✅. (c) witness spot-check via `witness-block-trace` **could not
  run**: the tool is regressed — it fails with witness-stream misalignment
  ("nonce too high state:0") even on a prior-verified FROZEN block (25,490,000,
  which logged gas diff +0 on 2026-07-10 and whose witness is unchanged). Both
  the stale May-10 binary and a fresh rebuild fail identically → tool-side
  regression, NOT this week's data. `witness-replay` is a from-0 contiguous
  builder (alignOnResume rejects a mid-range fresh-output spot-check), so it's
  not a drop-in either. Flagged for separate investigation. Full authoritative
  `verify-root` (~1h) available if extra assurance wanted; not auto-run (clean
  replay, fleet box carrying the live qs consensus fleet).
- **N42-hashed (§6, reth-sourced)**: `n42-migrate-reth-hashed --reth d:/reth2k/db
  --dst D:/N42-hashed/chaindata --head-block 25587083 --expect-root 0x61099c71…5357`.
  Fleet gracefully stopped first (`n42-reconfig stop --data.dir E:\qs-nodeN` ×7,
  CTRL_BREAK) to free RAM — the migration WS peaked ~102 GB of reclaimable MDBX
  mmap (Commit stayed 40/157 GB, no OOM; with the fleet up this would have OOM'd,
  validating the stop). Phases (verbatim, ~1h20m): acc 404,233,408 / sto
  1,614,514,021 (badSub 0) / tacc 30,200,591 / tsto 142,070,390 (badSub 0) / code
  2,583,669 (decodeFail 0) / **vtrie OK: root == expect** / head. Fresh
  D:/N42-hashed/chaindata = 156 GB; ethel-last-block = 25,587,083. ✅ GATE PASS.
- codes freezer (§4, published n42-codes-<tip>): reth Bytecodes ARE now in the
  N42-hashed Code table (2,583,669). Separate content-addressed freezer export
  (for minimal/full clients) TBD — assess if the coverage marker needs bumping to
  25,587,083 this week.
- NOT run: DATC sr merge (§5, no DATC head advance), snapshot/anchors/manifests.
  geth left STOPPED; live qs fleet gracefully stopped for the migration (restart TBD).

### 2026-07-10 (this run)
- Source: geth ancient frozen **25,503,168** (prior week 25,439,371; Δ 63,797).
  geth+lighthouse were DOWN since the 00:37 OOM (see ckpt-v2 memory) — tip is
  last night's; restart of geth+CL for a fresher tip left to the operator.
- senders (both dirs): 25,439,371 → 25,503,167, 45 s each @ ~1400 blk/s. ✅
- headerc: resumed 25,436,160 (partial-tail rewind), → 25,503,167, 9 segs. ✅
- bodyc: partial tail seg 3105 detected + rewound, rebuilt → 25,503,167. ✅
- receipts: → 25,503,167 (2m47s, 381 blk/s). ✅
- replay acctcs/storcs/witness: 25,439,371 → 25,503,167 (~55 min @ ~22-30
  blk/s, async witness-buffer flush the bottleneck as usual); all four
  N42-eth1177 tables Items()=25,503,168. ✅
- witness spot-check: blocks 25,460,000 / 25,490,000 / 25,503,167 replay with
  gas diff +0 on every tx. ✅ GATE PASS
- codes: re-exported to d:/n42-codes-25503 — 2,540,345 codes, 5.9 GB,
  `codes.coverage=25,503,167` (17m16s). Old n42-codes-25439 kept until publish. ✅
- DatcStoRoot: segments freshly exported today (migration day) — merge N/A;
  weekly procedure documented in §5, `stroot-merge` tool landed (37ff9520)
  with the weekly-cadence equivalence test.
- NOT run this week: snapshot regen (RAM window), anchors/bpp, N42-hashed,
  manifests (no publish), DATC head extension. geth+lighthouse left STOPPED
  (down since the 07-10 00:37 OOM; restart is the operator's call).
- DatcStoRoot: segments freshly exported today (migration day) — merge N/A;
  weekly procedure documented in §5, `stroot-merge` tool landed (37ff9520).
