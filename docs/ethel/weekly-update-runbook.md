# Weekly Data Update Runbook (eth-el derived data + DATC sr segments)

> Consolidated from `sync-runbook-2026-06-14.md` §5b/§5e and the weekly runs
> since. This is the OPERATIONAL doc: what to run every week, in order, with
> verification gates. All heights in the log section are point-in-time — the
> source of truth is each dataset's on-disk marker (§1).
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

## 4. Step 3 — codes coverage bump (~min)

The replay already wrote new Code rows into N42-eth1177 MDBX. Re-export the
content-addressed full-history codes freezer and bump the coverage marker:

```powershell
wk-ethexec.exe code-import2fz --db d:/N42-eth1177 --outdir d:/n42-codes-<tip> --coverage-block <tip>
```

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

## 6. Deferred / conditional items (not every week)

| Item | Trigger | Tool | Notes |
|---|---|---|---|
| snapshot regen (minimal/full) | needs >60 GB free RAM window | `reth-snapshot-export` | per MEMORY, deferred |
| anchors / bpp | when publishing stateless mode | `blockproof-produce` | ~1.5-2 h per 60k blocks |
| N42-hashed state | when eth-el follower redeploys | `n42-migrate-reth-hashed` (reth copy) or MerkleStageIncremental (changesets) | reth2k must be idle |
| manifests | at publish | `cmd/n42-eth-manifest` | blake3 per file |
| DATC library head extension | separate project (bprime2 frozen @15.22M) | cs→spill→recast pipeline + records build | sr merge (§5) rides on it |

## 7. Verification gates (every week)

- Four light items: log lines report resumed-from == prior marker and
  complete-to == geth tip; freezer-info re-probe shows Items() == tip+1.
- Replay: `ethel-last-block == tip`; witness spot-check
  (`witness-block-trace` on 2-3 sampled new blocks reproduces gasUsed).
- Full state-root GATE (`verify-root --workers 16`, ~1 h) — run after LARGE
  catch-ups or when the replay logged anomalies; not needed for a clean small week.
- DATC sr merge: built-in spot gate (§5.3).

## 8. Run log

### 2026-07-22 (this run — geth-sourced part; reth-sourced deferred)

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
