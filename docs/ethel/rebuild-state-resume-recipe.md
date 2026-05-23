# RebuildState Resume Recipe — archive Scenario B

**Date:** 2026-05-23
**Context:** User wants archive-mode bootstrap test:
RebuildState consumes witness-replay output, produces PlainState
in MDBX. Auto-resume on restart is built-in.

## Source files (witness-replay output)

```
D:/witness-replay-out-v3/chain/freezer/   ~385 GB
  acctcs.cidx + acctcs.NNNN.cdat   per-block account changesets
  storcs.cidx + storcs.NNNN.cdat   per-block storage changesets
  wipes.cidx  + wipes.NNNN.cdat    SELFDESTRUCT pre-wipe sidecar
                                    (required when CS is from
                                    witness-replay — witness misses
                                    pre-wipe entries)
```

## Existing partial output

```
D:/N42-eth1177/mdbx.dat                   278 GB
  Account              386M  entries / 24 GB
  Storage              1.57B entries / 126 GB
  Code                 2.4M  entries / 16 GB
  DbInfo               1 entry           ethel_progress = 12,501,823

next rebuild-state run auto-resumes at block 12,501,824
to continue forward replay
```

## Source code

```
internal/ethel/rebuild_state.go:90
  func RebuildState(ctx, db, ancientDir, endBlock) — applies acctcs+storcs forward

internal/ethel/rebuild_state.go:101
  func RebuildStateWith(ctx, db, ancientDir, endBlock, opts) — with verify + persist-trie

cmd/ethexec/main.go:1529  runRebuildState — CLI driver
  --datadir <output MDBX>           where PlainState goes
  --leaves <CS source freezer dir>  default: <datadir>/chain/freezer
                                    set to D:/witness-replay-out-v3/chain/freezer
                                    to use witness-replay output
  --ancient <geth ancient>          for header / final state-root verify
  --start <N>                       omit → auto-resume from DbInfo/ethel_progress
                                    explicit 0 → CLEAR + rebuild from genesis
  --end <N>                         0 = all available; set for small smoke
  --verify <interval>               periodic state-root verify
  --wipes-sidecar <dir>             auto-detected if <leaves>/wipes.cidx present
```

## Auto-resume semantics

`cmd/ethexec/main.go:1589-1610` reads `DbInfo/ethel_progress`
from the target MDBX. If `--start` is NOT explicitly passed, the
next run begins at `progress + 1`. So:

```
1. rebuild-state ... --end 12550000   →  runs 12,501,824..12,550,000
2. (kill mid-way at block 12,520,000 — progress saved)
3. rebuild-state ... --end 12550000   →  auto-resumes 12,520,001..12,550,000
```

The progress marker is written under both `DbInfo/ethel_progress`
and the historical `Headers/ethel_progress` key (per the comment
at cmd/ethexec/main.go:2077) so older readers also see it.

## Recipe — resume D:/N42-eth1177 forward

```bash
# A. Small smoke (50K blocks more, ~15 min)
ethexec rebuild-state \
    --datadir D:/N42-eth1177 \
    --leaves  D:/witness-replay-out-v3/chain/freezer \
    --ancient D:/geth/geth/chaindata/ancient/chain \
    --end     12550000

# B. Full chain to tip-equivalent (~12 hours from 12.5M to 25.1M)
ethexec rebuild-state \
    --datadir D:/N42-eth1177 \
    --leaves  D:/witness-replay-out-v3/chain/freezer \
    --ancient D:/geth/geth/chaindata/ancient/chain \
    --end     25101867
```

## Interrupt-safe restart

```bash
# Start (sets progress as it goes)
ethexec rebuild-state --datadir D:/N42-eth1177 \
    --leaves D:/witness-replay-out-v3/chain/freezer \
    --ancient D:/geth/geth/chaindata/ancient/chain \
    --end 25101867

# Ctrl-C at any time. Progress is durable per commit interval
# (ethexec uses SafeNoSync but the ethel_progress + state commit
# atomically per write batch — last batch's worth may be lost,
# next run resumes from the LAST DURABLE checkpoint).

# Restart — auto-resume:
ethexec rebuild-state --datadir D:/N42-eth1177 \
    --leaves D:/witness-replay-out-v3/chain/freezer \
    --ancient D:/geth/geth/chaindata/ancient/chain \
    --end 25101867
# → "Auto-resuming from DbInfo/ethel_progress progress=12545000 startBlock=12545001"
```

## After PlainState reaches tip-eq → switch to cmd/eth-el

Once `ethel_progress` reaches ~25.1M:

```bash
# cmd/eth-el detects populated chaindata → skips bootstrap-rebuild
cmd/eth-el -tags n42el \
    --datadir D:/N42-eth1177 \
    --bootstrap.enabled=false \
    --caplin.enabled \
    --caplin.network mainnet \
    --caplin.checkpoint.url <URL> \
    --catch-up.mode auto
```

eth-el will:
1. Open the rebuilt PlainState (skip RebuildState)
2. Caplin checkpoint-syncs the beacon side
3. EL receives engine_newPayload for blocks 25,101,868..tip
4. Each payload runs EVM, mutates PlainState
5. At tip → 12s slot live loop

## Helper

`cmd/n42-read-ethel-progress/main.go` — read-only one-shot to
print the ethel_progress value of any MDBX:

```bash
n42-read-ethel-progress --dir D:/N42-eth1177
# ethel_progress: 12501823 (next rebuild-state run will resume at 12501824)
```

## Timing estimates

Past rebuild4.log on the same input + output:
- 0..5M:  ~6 min (state root verify at 5M = 23:29:48 − 23:24:33 start)
- 0..10M: ~28 min
- 0..15M: ~2 h
- 0..20M: ~6 h
- Storage hashing pass: +1h

So forward from 12.5M to 25.1M ≈ 8-12 hours (CPU + IO bound).

## Companion docs

- `docs/ethel/real-chain-three-mode-runbook.md` — 3-mode bootstrap design
- `docs/ethel/n42-eth-client-distribution.md` — minimal/full/archive spec
- `memory/project_eth_el_bootstrap_paths.md` — terminology + current state
