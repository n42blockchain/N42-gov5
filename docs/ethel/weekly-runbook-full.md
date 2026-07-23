# Weekly Runbook — Full Data Prep + Three-Mode Test (eth-el)

> **The single "do this every week" reference.** Consolidates the geth-sourced
> data prep (`weekly-update-runbook.md`), the reth-sourced steps (N42-hashed /
> codes / snapshot), and the minimal/full/archive test (`tester-guide.md`) into
> one repeatable flow so nothing is re-derived each week.
>
> Worked example values below are from the **2026-07-22** run (geth head
> 25,587,115 / reth head 25,587,083). Substitute the current heights from §1.
>
> House rules (every step): long tasks run **detached** (`Start-Process`, never
> die with the session); launch `.ps1` with `pwsh -File` **not** `powershell -File`
> (execution policy); in Bash-tool commands pass Windows `-Bin`/paths with
> **forward slashes** (bash eats backslashes → "file not found"); set
> `N42_ETL_TMPDIR=D:/etl-tmp` for ETL tools; **never hard-kill** a live writer
> (QMDB fleet / replay) — graceful stop only; capture raw output to a log file.

---

## 0. Data sources & drives

| Source | Path | Role |
|---|---|---|
| geth ancient | `d:/geth/geth/chaindata/ancient/chain` | canonical blocks/receipts; the geth-sourced target head |
| reth2k | `d:/reth2k/db` (+ `static_files`) | reth 2.2 full node synced to a fixed `--debug.tip`; the reth-sourced state base |
| geth-derived | `d:/n42-eth1` (receipts/senders/headerc/bodyc), `d:/N42-eth1177` (acctcs/storcs/witness/senders + PlainState+Code) | weekly incremental data |
| reth-derived | `d:/N42-hashed/chaindata` (hashed-canonical), `d:/n42-codes-<tip>` (codes freezer), `d:/n42-snapshot-<tip>` (state snapshot) | weekly base-state artifacts |

**D: = authoritative deliverables. E: = where the three-mode tests run** (copies;
never run eth-el against a D: deliverable — it advances/mutates state and, for an
ethexec-built datadir, truncates the freezer). See §6 gotcha table.

---

## 1. Inventory — find this week's two target heights (read-only, ~1 min)

```bash
# geth-sourced target = geth frozen head
build/bin/freezer-heads.exe d:/geth/geth/chaindata/ancient/chain
#   → "frozen=<Items>" (sentinel stripped). Target last block = Items − 1.
#     2026-07-22: Items 25,587,116 → geth target 25,587,115.

# reth-sourced target = reth2k head. Two cross-checks:
#  (a) highest headers static-file segment .off size / 24  (bytes-per-block)
ls d:/reth2k/static_files/ | grep -oE 'headers_[0-9]+_[0-9]+' | sort -t_ -k3 -n | tail -1
#  (b) confirm: geth block <reth-head> hash == reth --debug.tip in d:/reth2k/r.bat
build/bin/geth-hdr-probe.exe -ancient d:/geth/geth/chaindata/ancient/chain -blocks <reth-head>
#     2026-07-22: reth head 25,587,083, hash 0x00017439…8762 == r.bat --debug.tip ✅.
#     (reth is typically a few dozen blocks behind geth's latest freeze — immaterial.)
```

Record the block-25587083 **stateRoot** from geth-hdr-probe — it is the N42-hashed
`--expect-root` (2026-07-22: `0x61099c711d79c340e17e7d8b5656684ff44d3ede7b17c0ca4156dc6c67215357`).

---

## 2. geth-sourced data (auto-resume to geth head)

Full detail in `weekly-update-runbook.md` §1-4. Condensed:

```powershell
$A='d:/geth/geth/chaindata/ancient/chain'; $EX='C:\N42\N42-gov5\build\bin\ethexec.exe'
$env:N42_ETL_TMPDIR='D:/etl-tmp'
# Step 1 — four light generators (serial, ~10 min; omit --start, they auto-resume)
& $EX sender-recovery --ancient $A --datadir d:/n42-eth1    --workers 0
& $EX sender-recovery --ancient $A --datadir d:/N42-eth1177 --workers 0
& $EX header-compact  --ancient $A --datadir d:/n42-eth1
& $EX body-compact    --ancient $A --datadir d:/n42-eth1
& $EX receipt-copy    --ancient $A --datadir d:/n42-eth1    --workers 0
# Step 2 — witness/changeset EVM replay (HEAVY, detached, ~1 min / 1.5k blocks)
Start-Process $EX -ArgumentList '--ancient',$A,'--datadir','d:/N42-eth1177' `
  -RedirectStandardOutput D:\weekly-<date>-replay.log -RedirectStandardError D:\weekly-<date>-replay.err.log
```

**Gate**: `freezer-heads` shows n42-eth1 {receipts,senders} and N42-eth1177
{acctcs,storcs,witness,senders} all = geth Items; `ethel-last-block` = geth head.
The replay verifies gasUsed per-block inline, so a clean run to tip IS the exec gate.
(`witness-block-trace` spot-check is currently regressed — fails on known-good
frozen blocks too; a tool bug, not a data problem.)

---

## 3. reth-sourced data (from reth2k, → reth head)

**Prereq: reth2k finished syncing and its process is stopped** (so its MDBX is
free for exclusive read). Confirm `Get-Process reth,pevm` is empty.

### 3a. N42-hashed migration — the BIG one (stop the fleet first)

The fresh hashed-canonical state peaks ~100 GB of reclaimable MDBX mmap. With the
live qs fleet up (~75 GB) this OOMs — **gracefully stop the fleet first**:

```powershell
0..6 | % { C:\N42\N42-gov5\build\bin\n42-reconfig.exe stop --data.dir E:\qs-node$_ --timeout 90s }
```

Then migrate (verbatim trie import + verify; ~1h20m; detached):

```powershell
Start-Process C:\N42\N42-gov5\build\bin\n42-migrate-reth-hashed.exe -ArgumentList `
  '--reth','d:/reth2k/db','--dst','D:/N42-hashed/chaindata',
  '--head-block','<reth-head>','--expect-root','<stateRoot@reth-head>' `
  -RedirectStandardOutput D:\weekly-<date>-migrate.log -RedirectStandardError D:\weekly-<date>-migrate.err.log
```

**Gate**: log shows `PHASE vtrie OK: … root == expect`; `ethel-last-block` =
reth-head; `ethexec db-stats --datadir D:/N42-hashed/chaindata` tables non-empty.
Memory is safe as long as **Commit** stays well under the limit (mmap WS is
reclaimable — watch `\Memory\Committed Bytes`, not "free RAM"). Fresh dst ≈ 156 GB.

### 3b. codes freezer (published, content-addressed)

```powershell
Start-Process C:\N42\N42-gov5\build\bin\code-import2fz.exe -ArgumentList `
  '--db','d:/reth2k/db','--outdir','d:/n42-codes-<tip>' `
  -RedirectStandardOutput D:\weekly-<date>-codes.log -RedirectStandardError D:\weekly-<date>-codes.err.log
```

Produces `codes.cidx` + `codes.NNNN.cdat` (reads reth Bytecodes, joins
PlainAccountState). ~2.5 M codes. This is the minimal/full H0 bytecode source.

### 3c. state snapshot — the basis for minimal/full (from reth2k PlainState)

```powershell
Start-Process C:\N42\N42-gov5\build\bin\reth-snapshot-export.exe -ArgumentList `
  '-db','d:/reth2k/db','-out','d:/n42-snapshot-<reth-head>',
  '-end-block','<reth-head>','-table','both','-shards','16' `
  -RedirectStandardOutput D:\weekly-<date>-snapshot.log -RedirectStandardError D:\weekly-<date>-snapshot.err.log
```

Sharded RecSplit+EF+zstd `accounts.sNN.* / storage.sNN.*` (~1h; reads reth
PlainAccountState/PlainStorageState — reth's PLAIN tables, NOT the hashed
N42-hashed). This is the segment set snapshot-mode boots from.

### 3d. restart the fleet (after the big-mem step is done or memory allows)

```bash
pwsh -File E:/deploy-7node.ps1 -Bin C:/N42/N42-gov5/build/bin/n42-reconfig.exe
```

**Must override `-Bin` to `n42-reconfig.exe`** — the script default
(`n42-qs-32194f16.exe`) is the OLD binary and would downgrade the reconfig/
difficulty=0 fleet → consensus divergence. The script re-seeds a node dir only if
it's missing, so existing E:\qs-node0..6 just restart. Verify: 20012..20018
`eth_blockNumber` advancing, single block-hash per height, `difficulty=0x0`.

---

## 4. Build eth-el (once, or when the tree changed)

```bash
cd C:/N42/N42-gov5 && go build -tags "nosqlite,noboltdb" -o build/bin/eth-el.exe ./cmd/eth-el
```

Rebuild each week so eth-el's hashed-canonical reader matches the current
`n42-migrate-reth-hashed` format. Add `-tags n42el` ONLY if you need the embedded
Caplin CL; the self-contained test below uses `--eldevp2p.enabled` and doesn't.

---

## 5. Three-mode test (on **E:**, catch-up + live ~1 min)

All three run on E: copies. Ports: pick non-fleet ports (fleet uses http
20012-20018, p2p 62000-63000) — e.g. publicrpc `20115`, eldevp2p `:30403`.
eth-el requires MDBX at `<datadir>/chaindata/mdbx.dat`, freezer at
`<datadir>/chain/freezer`, snapshot segs at `<datadir>/snapshot`.

> **MANDATORY — seed the BLOCKHASH window (all three modes).** N42's `BLOCKHASH`
> opcode resolves via `getHeader` (canonical header lookup), **not** the EIP-2935
> contract. A hashed-canonical/migrated datadir has state but **no header chain
> below the head**, so `BLOCKHASH(n)` of any pre-migration block returns **zero** —
> a wrong `gasUsed`/state root that slips past the root check (root doesn't commit
> headers). Every mode's `<datadir>/chain/freezer` **must contain `headerc.*`
> covering at least `[head-256, head]`** (full-history headerc is fine). With it,
> the pre-loop `backfillBlockhashWindow(ctx,false)` fills the window from local
> headerc with **zero peers** → `backfilled=true` → the import gate never blocks.
> Without it the node depends on peers serving 256-block-old headers and the import
> gate defers catch-up (log: `deferring import — blockhash-window not yet
> backfilled have=X missing=Y`) until they do — which mainnet peers often won't.
> Proven by `n42-hashed-exec-check --fill-headers` (block 25587088 diverged −49582
> with the window empty; gas+root matched once seeded).

### ARCHIVE (ready as soon as §3a lands)

Copy the fresh hashed-canonical state to E:, then run it (hashed-canonical =
no PlainState; already has correct chaindata/ layout). **The migrated `chaindata`
has NO header chain — you MUST also seed `headerc.*` into `chain/freezer`** (see
the mandatory callout above), or `BLOCKHASH` returns zero and catch-up diverges:

```powershell
robocopy D:\N42-hashed\chaindata E:\ethel-archive-test\chaindata /E /MT:16   # ~156 GB
robocopy D:\n42-eth1 E:\ethel-archive-test\chain\freezer headerc.* /MT:16    # BLOCKHASH window seed (REQUIRED)
C:\N42\N42-gov5\build\bin\eth-el.exe --datadir E:/ethel-archive-test --hashed-canonical `
  --bootstrap.enabled=false --eldevp2p.enabled --eldevp2p.listen :30403 --engine.enabled=false `
  --catch-up.mode auto --publicrpc.enabled --publicrpc.port 20115
```

### MINIMAL (needs §3b codes + §3c snapshot)

Assemble `E:\ethel-min-test\snapshot\` ← `d:/n42-snapshot-<tip>/accounts.* storage.*`;
`E:\ethel-min-test\chain\freezer\` ← `d:/n42-eth1` `headerc.*` + `d:/n42-codes-<tip>` `codes.*`:

```powershell
C:\N42\N42-gov5\build\bin\eth-el.exe --datadir E:/ethel-min-test --snapshot.mode minimal `
  --bootstrap.mode snapshot --eldevp2p.enabled --eldevp2p.listen :30403 --engine.enabled=false `
  --publicrpc.enabled --publicrpc.port 20115
# (H0 code can come from reth directly instead of the freezer: --codes.reth-db d:/reth2k/db)
```

### FULL (minimal + the ledger freezers)

Same as minimal plus copy `bodyc.* receipts.* accthist.* storhist.* txindex.*`
into `E:\ethel-full-test\chain\freezer\`:

```powershell
C:\N42\N42-gov5\build\bin\eth-el.exe --datadir E:/ethel-full-test --snapshot.mode full `
  --bootstrap.mode snapshot --history.mode full --eldevp2p.enabled --eldevp2p.listen :30403 `
  --engine.enabled=false --publicrpc.enabled --publicrpc.port 20115
```

### Catch-up + live acceptance (per mode)

- **caught up**: log `eldevp2p: caught up head=… tip=…` (tip = max peer head;
  post-merge EL peers → real catch-up toward mainnet live head, needs internet).
- **live**: the 12 s follow loop (`probeForNewTip` then sleep 12 s). For the
  1-minute test, watch ≥ 4-5 consecutive live rounds each importing the next
  block **with no state-root mismatch** (the golden rule).
- **external check**: poll `eth_blockNumber` on `:20115`; read `ethel-last-block`
  with `build/bin/read-progress.exe`. Serialize the modes (one node at a time) to
  bound memory while the fleet is up.

---

## 6. Gotchas (the traps — read before repeating)

| Trap | Fix |
|---|---|
| Bash eats `\` in Windows paths (`-Bin C:\N42\…` → "file not found") | forward slashes: `-Bin C:/N42/…` |
| `powershell.exe -File x.ps1` → execution-policy error | use `pwsh -File` |
| deploy-7node.ps1 default `-Bin` is the OLD binary | always `-Bin …/n42-reconfig.exe` |
| eth-el on an ethexec datadir (mdbx.dat at top level) truncates the freezer | `mv mdbx.dat → chaindata/`, or use N42-hashed (already correct) / an E: copy |
| N42-hashed migration OOM with fleet up | gracefully stop fleet first; watch **Commit** not "free RAM" (mmap WS is reclaimable) |
| snapshot-export from N42-hashed | wrong — it needs reth PLAIN tables; export from `d:/reth2k/db` |
| minimal/full "not wired" (old runbook) | stale — snapshot-direct IS wired in `cmd/eth-el/main.go`; the blocker is only the snapshot segments existing for the week |
| `witness-block-trace` spot-check fails | tool regression (fails on known-good frozen blocks); rely on the replay's inline gasUsed gate |
| port clash with the fleet | fleet = http 20012-18 / p2p 62000-63000; test nodes pick others (20115 / :30403) |
| catch-up diverges at head+N with a gasUsed/root mismatch (e.g. −49582 @ 25587088) | `chain/freezer` is missing `headerc.*` → `BLOCKHASH` returns zero. Seed the window (headerc covering `[head-256, head]`) — mandatory for ALL modes, archive included |

---

## 7. Deferred / conditional (not every week)

DATC sr-segment merge (§5 of `weekly-update-runbook.md`, only when DATC head
advanced); anchors/bpp (stateless publish); manifests (at publish). geth +
lighthouse restart to freshen the ancient tip is the operator's call.
