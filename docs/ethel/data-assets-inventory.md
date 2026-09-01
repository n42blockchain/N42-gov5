# eth-el Data Assets Inventory (as of 2026-08-30)

> What exists, where it lives, how far it covers, and who keeps it current.
> Written because the weekly runbook describes PROCESS and the mode spec
> describes PRODUCT, and neither answered "is this directory still alive?" —
> a question that came up five times in one session and had a different answer
> each time.
>
> Every height here was READ off the artefact (marker file, freezer items,
> segment footer, MDBX meta key), not copied from an earlier document. When a
> number is inferred rather than measured it says so.
>
> Companion docs: `weekly-update-runbook.md` (the process),
> `datc-pipeline.md` (the archive-plus tier end to end),
> `eth-el-datadir-layout.md` (on-disk layout), `n42-eth-client-distribution.md`
> (the published product).

## 1. Sources (not produced here)

| Path | Size | Head | State |
|---|---|---|---|
| `D:\geth` | 1,624 GB | ancient frozen **25,864,982** | stopped 2026-08-29 22:19; the week's target is frozen−1 |
| `D:\reth2k` | 3,866 GB | synced 2026-08-30 07:47 | stopped; source for codes / snapshot / hashed-state |

## 2. Derived freezers — geth-sourced, weekly

**`D:\n42-eth1`** (875 GB) — the header/body/receipt/index tier.

| Table | Covers to | Updated | Notes |
|---|---|---|---|
| `headerc` | 25,864,981 | ✅ weekly (Step 1) | partial tail auto-rewinds |
| `bodyc` | 25,864,981 | ✅ weekly (Step 1) | 356 cdat; `full` ships only the newest ~56 |
| `receipts` | items 25,864,982 | ✅ weekly (Step 1) | |
| `senders` | items 25,864,982 | ✅ weekly (Step 1) | **byte-identical duplicate of the N42-eth1177 copy** — 40.7 GB, see §7 |
| `txindex` | 24,000,000 → extending | ⚠️ first weekly run 2026-08-30 | was last built 2026-04-13; now Step 2b |
| `codes` | **25,252,184** | ❌ orphan since 2026-06-05 | see §7 — the live copy is in the publish root |
| `storhist` | 26 segments, built 2026-06-06 | ❌ not in any weekly step | its `accthist` half lives in `D:\n42-release`; see §5 |

**`D:\N42-eth1177`** (931 GB) — the replay tier (changesets + witness + state).

| Table / marker | Covers to | Updated |
|---|---|---|
| `senders` / `acctcs` / `storcs` / `witness` | items 25,864,982 | ✅ weekly (Step 2) |
| `ethel-last-block` | 25,864,981 | ✅ weekly (Step 2) |
| `PlainState` + `Code` (MDBX) | tip | ✅ side effect of Step 2 |

## 3. Weekly artefacts — reth-sourced

| Path | Size | Covers | Note |
|---|---|---|---|
| `D:\n42-snapshot-25765565` | 54 GB | 25,765,565 | 16 shards; regenerated weekly (~2 h) |
| `D:\N42-hashed-25765565` | 156 GB | 25,765,565 | vtrie root verified against expect |
| `n42-codes-<tip>` | ~6.2 GB | 25,765,565 | **the 25765565 directory was deleted into the Recycle Bin**; its content survives as hard links inside the publish root |

These are versioned per tip. The previous generation stays until the publish
swap; nothing prunes them automatically.

## 4. Publish roots

| Path | Size | Note |
|---|---|---|
| `D:\n42-publish-25765565` | 756 files / 1,134 GB **logical** | hard-link assembly — near-zero extra bytes. **Mutated by the next weekly run**; see the runbook §6b "publish root is NOT an immutable snapshot" |
| `D:\n42-release` | 16 GB | the 2026-06-06 release: holds `accthist` + `anchorc` + four manifests + `minimal.torrent` |

Published manifest contents as actually cut on 2026-08-16 (read from the JSON,
not from the spec):

| mode | files | size | sections present |
|---|---|---|---|
| archive | 476 | 830.2 GB | bodyc 360, witness 97, txindex 10, codes 5, headerc 4 — **anchors 0** |
| full | 173 | 165.5 GB | bodyc 57, snapshot 97, txindex 10, codes 5, headerc 4 — txindex is FULL history, not the one-year window |
| minimal | 97 | 24.6 GB | snapshot only — **headerc and codes were missing** (selector fixed 2026-08-30) |

## 5. Enhancement tiers — none of them weekly today

| Path | Size | Covers | Owner tool |
|---|---|---|---|
| `D:\n42-datc-bprime2-25m` | 1,160 GB | node records **15,220,000**; leaf layers **25,439,3xx** | `n42-datc` — **the cherry-datc build only** (this repo has 13 of its 35 files, and lacks `cs_to_spill` / `stroot_seg` / `drop_table`) |
| `D:\n42-idc-anchors-25m` | 4.8 GB | **25,098,768** (2026-06-16) | `blockproof-produce` |
| `storhist` (in n42-eth1) + `accthist` (in n42-release) | ~43 GB | built 2026-06-06 | `n42-hist-from-freezer` |

Three notes that cost real time to rediscover:

- **DATC is not one clock, and the directory name is a TARGET, not a state.**
  `csside`'s `SMeta["prog"]` = 25,439,371 and the leaf segments reach
  25,439,3xx, while `DatcMeta.progress` = 15,220,000. The cs→spill→finalize leg
  is DONE to 25.44M; only the node-record leg is behind. Proofs are served out
  of the node records, so **this library answers historical proofs for
  0-15,220,000 today** — the 1.1 TB is not yet a 0-25M archive-plus tier. The
  catch-up project builds exactly that missing layer.

  This gap is BY DESIGN, not a deletion. `cs-to-spill`'s own header says it
  extends the leaf history "from the forward changesets ALONE (no EVM, no trie,
  no 880 GB build MDBX) ... without resuming the OOM-prone build for anything
  but the node records (Pipeline B)". The cheap leg (changeset rows ARE leaf
  rows, just block-ordered) was run first to 25.44M; the expensive leg — EVM +
  trie + per-block gold root check, the one that OOM'd — was deliberately left
  for a dedicated run. Searched 2026-08-30: no second DATC output dir exists on
  D:/E:/C:, and nothing DATC-shaped is in the Recycle Bin.
- **anchors are published from the wrong path.** The archive selector matches
  `chain/freezer/anchorc.*`; the data sits in its own directory, so the archive
  manifest shipped zero anchor files.
- **history is split across two roots.** `internal/api/historical_state.go`
  opens `storhist` AND `accthist` from the same chain dir and fails if either
  is absent — as laid out today, no datadir can start the historical-state RPC.

## 6. Test / staging trees (E:)

| Path | Size | Keep? |
|---|---|---|
| `E:\ethel-{archive,full,min}-25765565` | 1,008 GB | current generation — the 2026-08-17 three-mode PASS |
| `E:\ethel-{archive,full,min}-25715848` | 888 GB | **superseded** (2026-08-09 generation) |
| `E:\qs-*` | ~460 GB | the self-developed chain's fleet — a DIFFERENT product, shares no data with this pipeline |

Test trees deliberately drop `snapshot/*.val.zst` (the node mmaps the raw
`.val`), which is why they are NOT a valid manifest source.

## 7. Reclaimable, with the verdict for each

Verified this session; nothing was deleted.

| Item | Size | Verdict |
|---|---|---|
| `E:\ethel-*-25715848` | 888 GB | ✅ superseded generation |
| DATC `leafseg` | 167 GB | ✅ reclaimable — `leafseg2` measured self-sufficient, next merge takes `--seg-old leafseg2` |
| `senders` duplicate | 40.7 GB | ✅ replace one copy with a hard link — 23 files match on name+size, sampled SHA256 identical |
| `codes` orphan in n42-eth1 | 6.1 GB | ⚠️ hard-link the current generation over it rather than delete: `witness-block-trace` auto-detects `<hb-dir>/codes.cidx` and a plain delete makes that path fail |
| DATC `leafspill-eq*` | 1.1 GB | ✅ equivalence-experiment residue |
| DATC `ckpt` | 1.7 GB | ⚠️ rebuildable, but the win is not worth the rebuild |
| **DATC `csside`** | **98 GB** | ⛔ **KEEP** — cs-to-spill resume state (`SAcct`/`SSlot` liveness overlay + progress). Deleting it forces a full changeset re-scan |

## 8. Task queue — execution order and disk gates

Rule for every entry: **check free space against the "needs" column first; if
it does not fit, SKIP the task and report, do not start and fill the disk.**
D: is the constrained volume (E: only carries test trees and the qs fleet).

### A. This week's run (2026-08-30)

| # | Task | Needs | Status |
|---|---|---|---|
| A1 | Step 1 — four light generators | in-place | ✅ 11m38s, freezers to 25,864,982 |
| A2 | Step 2 — witness/changeset replay | in-place | ✅ 1h06m35s, `ethel-last-block` 25,864,981, witness spot-check +0 |
| A3 | Step 2b — txindex archive | +2.1 GB | ✅ 1h12m43s, 26 segments, 3.71B tx, gate PASS |
| A4 | Step 3 — codes | +6.2 GB | running |
| A5 | snapshot export (16 shards) | +54 GB | queued |
| A6 | N42-hashed migration | +156 GB | queued — reth2k must be idle, serialize against A5 |
| A7 | txindex window tier (full = 1 year) | +5 GB | queued |

### B. Approved fixes

| # | Task | Needs | Status |
|---|---|---|---|
| B1 | `minimalSelector` ships headerc + codes | — | ✅ code + tests |
| B2 | publish root: copy each table's cidx + newest cdat, hard-link the rest | +10.5 GB per publish | at next publish |

### C. DATC catch-up to the weekly tip

Pipeline and commands: `datc-pipeline.md` §5. Runs only AFTER section A — it
is CPU-bound for 31-37 h and must not overlap the replay or the migration.

| # | Task | Needs | Gate before starting |
|---|---|---|---|
| C1 | `fork-state` → fresh continuation DB | live-state + trie copy, size to be MEASURED on the first run | free ≥ measured + 20% |
| C2 | `build --records-only --start 15220000` | ~325 GiB node records + ~16 GiB sr | free ≥ 400 GiB after C1 |
| C3 | `cs-to-spill` + `finalize-leaves` for the 25.44M → tip tail | ~7 GiB | trivial |
| C4 | acceptance (`verify`, `proof-bench --out2`, `stroot-merge --spot 2000`) | — | C2 finished clean |

C1's footprint is the one number nobody has measured. Measure it before
committing to C2: run C1, look at the resulting DB, then decide. The old
library stays read-only and keeps serving 0-15.22M throughout, so aborting
between C1 and C2 costs only the forked copy.

### D. Reclamation — EXECUTED 2026-08-30

| # | Item | Freed | Evidence |
|---|---|---|---|
| D1 | `E:\ethel-*-25715848` | **887.9 GB** (E:) | superseded by the 25765565 generation |
| D2 | DATC `leafseg` | **167.1 GB** (D:) | renamed first, then `verify --samples 6` reconstructed 6/6 roots byte-exact — including N=2,069,750 with `folds=177 leafReads=1,190,346`, i.e. the leaf segments and `sr.*.seg` really were exercised out of `leafseg2` |
| D4 | `codes.*` orphan in n42-eth1 | **5.65 GB** (D:) | deleted, NOT replaced — see below |
| D5 | DATC `leafspill-eq*` | 1.16 GB (D:) | equivalence-experiment residue |
| D3 | senders duplicate | — | **SKIPPED**, see below |

Result: D: 1,292 → **1,467 GB**, E: 733 → **1,621 GB**.

**D4 changed plan mid-flight.** The intent was to hard-link the current codes
generation over the June orphan. Testing first showed the two are different
FORMATS: the weekly export uses `--addr-index=false` (content-addressed:
16-byte `cidx` + `hidx`/`hoff`), so `CodesFreezerReader` opens it and reports
`0 entries` — it cannot answer address→code. The orphan was the addr-indexed
build. Deleting it is safe (misses fall through to the MDBX Code table; a
`witness-block-trace` run with `--datadir` returned `diff=+0` with the codes
freezer reporting 0 entries), but replacing it with the new generation would
have installed a silently empty index. **`witness-block-trace` needs
`--datadir`; do not expect the freezer to serve address lookups.**

**D3 skipped on purpose.** Removing the duplicate needs the replay to read
senders from elsewhere, and it does not: `internal/ethel/executor.go` opens
`TableSenders` on the `--datadir` output freezer itself, so de-duplicating
means either a code change (a `--senders-dir` for the main replay) or a weekly
step that rebuilds hard links for each new segment. 40.7 GB is not worth either
while D: has 1.4 TB free. Revisit if space ever gets tight.

### E. Approved 2026-08-30 — was backlog, now scheduled

| # | Task | Cost | Unblocks |
|---|---|---|---|
| E1 | anchors catch-up 25,098,768 → tip | 19-26 h (~1.5-2 h per 60k) | stateless mode publish; also fixes the archive manifest shipping **zero** anchor files (data must land at `chain/freezer/anchorc.*`, not its own dir) |
| E2 | history rebuild — `accthist` + `storhist` to tip | **✅ data done 2026-08-30, 23m27s** | historical-state RPC — still blocked on a READER fix (single-segment Lookup), see below |

**E2 has the same partial-tail defect as txindex, and a harder fix.**
`HistoryBuilder.BuildFromBlockKeys` resumes with `existingSegs *
HistSegmentSize` (segments are 1M blocks, same as txlookup). Both `accthist`
and `storhist` are 26 segments built 2026-06-06, when the tip was ~25.35M — so
segment 25 is partial, resume computes 26,000,000 > tip, and a re-run indexes
NOTHING. Unlike txindex, the history dat header is `magic + keyCount + flags`
with **no blockCount**, so a partial tail cannot be detected from the data.
**Fixed 2026-08-30** without a format change, using a POSITIONAL test instead:
if the existing segments claim to cover past `endBlock`, the last one cannot be
full — peel it with `TruncateLastSegment` and rebuild. Regression test
`TestHistoryPartialTailRewritten` was confirmed to FAIL with the peel disabled
(`Resuming history build from=1000000`, `Lookup(3399) = 1199`) and pass with it.

**Rebuilt 2026-08-30**: both indexes now cover to 25,864,981 (26 segments each,
40.6 GB total in `D:/n42-eth1/chain/freezer`). The peel fired exactly as
designed on real data — `claimedCoverage=26000000 endBlock=25864982 segment=25`
for both prefixes — then rebuilt segment 25 (accthist 41.4M keys / 2m45s,
storhist 170.1M keys / 8m33s). Whole run 23m27s. `accthist` was COPIED (not
hard-linked) from `D:/n42-release` first, so the two halves finally share one
chain dir; a hard link would have let the peel truncate the June release's cdat.

**But the RPC still cannot use them.** Two things surfaced during the rebuild:

1. `HistoryBuilder` builds RecSplit with `Enums:false, LessFalsePositives:true`,
   a combination where the fingerprint is never materialized — the builder now
   warns "lookups will phantom-hit". Harmless HERE only because
   `historical_state.go` verifies externally: it re-reads the changeset at the
   returned block and falls through when the key is absent.
2. **`HistoryReader.Lookup` only consults the segment containing `blockNum`**
   (`segNum := blockNum / segmentSize`). A slot last modified in segment 10 and
   queried at a height inside segment 25 returns `found=false`, and the caller
   falls back to PlainState — i.e. the CURRENT value, not the historical one.

So the index is now current, but historical-state RPC needs the reader to walk
backwards through earlier segments before it can be switched on. That is a
separate fix, not a data problem.
| E3 | ~~merge the DATC toolchain~~ | **✅ done 2026-08-30** — 22 files + `AccRootEmitter` hook (lib/trie, commitment) + `DecodeStorageChangesFunc`; carried forward this repo's `ReadCurrentFullBlockNumber` fix that cherry lacked. build/vet/test green | remaining: one read-only two-binary agreement check before C |

### Execution order (resource-driven, 2026-08-30)

CPU-bound long jobs must not overlap; code work can proceed alongside them.

| Order | Task | Why here |
|---|---|---|
| 1 | A5 → A6 → A7 | this week's deliverable; A5/A6 both read the 2.1 TB reth MDBX, serialize |
| 2 | ~~**E3** toolchain merge~~ ✅ | done 2026-08-30, alongside A5 |
| 3 | **E2** history — MEASURE first on a slice, then full | inputs already at tip from Steps 1-2; decide the single target chain dir before building |
| 4 | **D** reclamation (D2-D5, ~215 GB on D:) | cheap, and it is what makes C comfortable |
| 5 | **C** DATC catch-up | 31-37 h, the biggest single CPU block |
| 6 | **E1** anchors catch-up | 19-26 h, serialize after C |

Steps 5 and 6 together are ~2.5 days of continuous CPU. Run them detached, one
at a time, never beside the fleet or a weekly replay.
