# Real-Chain Three-Mode Runbook

**Date:** 2026-05-23
**Goal:** Run cmd/eth-el on real ETH mainnet for each of the 3
distribution modes (minimal / full / archive), catch up from a
file-copy "downloaded" datadir to chain tip, then run 12-second
live sync.

This doc captures the actual end-to-end test (NOT the synthetic
mirror sim — that's a separate `docs/ethel/n42-eth-distribution-test-plan.md`
IT-7 already validated).

## Per-mode prerequisites

### Mode 1 — minimal (~86 GB datadir input)

```
Copy into <datadir>/:
  snapshot/accounts.{idx,ef,val.zst,codedict}    ← from D:/n42-snapshot
  snapshot/storage.{idx,ef,val.zst}              ← from D:/n42-snapshot
  chain/freezer/headerc.{cidx,NNNN.cdat}         ← from D:/n42-eth1
  chain/freezer/codes.{cidx,NNNN.cdat}           ← from D:/n42-eth1
                                                 = 39 GB minimal manifest
  (warm-tier MDBX is created fresh, empty)
                                                 + 0 GB initial
                                                 → 39 GB

Catch up from H₀=25,101,867 to tip H_now=~25,156,141:
  ~55K blocks. Headers + bodies pulled from libp2p / Engine API.
```

### Mode 2 — full (~720 GB datadir input)

```
Copy into <datadir>/:
  minimal files                                   = 39 GB
  chain/freezer/bodyc.{cidx,NNNN.cdat}            ← 567 GB from D:/n42-eth1
  chain/freezer/receipts.{cidx,NNNN.cdat}         ← ~63 GB from D:/n42-eth1
  chain/freezer/accthist.{cidx,NNNN.cdat}         ← 13 GB
  chain/freezer/storhist.{cidx,NNNN.cdat}         ← 28 GB
  chain/freezer/txindex.{cidx,NNNN.cdat}          ← 13 GB
                                                 → ~720 GB
```

### Mode 3 — archive (~849 GB datadir input; CS derived locally)

```
Copy into <datadir>/:
  full files                                      = 720 GB
  chain/freezer/witness.{cidx,NNNN.cdat}          ← 167 GB from D:/N42-eth1177
                                                 → 887 GB shipped
  (no acctcs/storcs in the manifest — those are LOCALLY DERIVED)
```

## Bootstrap pipeline per mode

### minimal / full → snapshot-direct (no rebuild)

The snapshot tier (`accounts.* + storage.*`) IS the historical
state at H₀. eth-el reads it directly via RecSplit + EF; a fresh
warm-tier MDBX overlays for blocks H₀+1..tip. **No PlainState
rebuild.** Bootstrap is essentially instant — only opening the
indexed files.

### archive → witness execution → CS → RebuildState → PlainState

Two-stage:

1. **witness-replay** (one-shot, multi-day on full chain):
   Replay each per-block witness through the EVM. Output is the
   `acctcs` + `storcs` changeset stream. Existing tool:
   `internal/ethel/witness_replay_pipeline.go` driven by
   `cmd/ethexec`.

2. **RebuildState** (forward replay of derived CS):
   `internal/ethel/rebuild_state.go:90` opens the freshly-produced
   acctcs/storcs and forward-applies every NEW value to
   PlainState. No EVM during this stage — it's a pure diff stream.

**Terminology correction:** the bootstrap package's "leaves
journal" comments are STALE. Actual code reads acctcs+storcs. The
"leaves" naming was retired well before this session; doc text
should be updated.

**Why archive doesn't ship CS pre-built:** witness is 167 GB,
CS is 397 GB (acctcs 137 + storcs 260). Shipping witness saves
230 GB on the wire at the cost of multi-day local CS derivation.
The publisher pipeline decided this is the right trade for
clients with disk pressure; clients with spare CPU + bandwidth can
opt into a pre-built-CS variant later.

So **D:/N42-eth1177 already has the CS derived** from a past
witness-replay run:
- `acctcs.cidx` + `acctcs.NNNN.cdat`  (~137 GB)
- `storcs.cidx` + `storcs.NNNN.cdat`  (~260 GB)
- `witness.cidx` + `witness.NNNN.cdat` (~167 GB)
- `senders.cidx` + `senders.NNNN.cdat` (~38 GB)

For a fresh archive bootstrap from the published manifest, ONLY
witness is in the package. CS is the local computation product.
For TESTING archive bootstrap today, we can short-circuit by
using D:/N42-eth1177's already-derived CS directly (skip
witness-replay) and just run RebuildState.

## ⚠ Blocker — snapshot-continue not wired in cmd/eth-el

`cmd/eth-el/main.go` has `bootstrap.enabled` (default true) which
ALWAYS triggers the leaves-journal → RebuildState path.

For minimal/full per `docs/ethel/n42-eth-client-distribution.md`,
the bootstrap path must be **snapshot-direct continue**:

  1. Open snapshot tier RecSplit + EF for accounts/storage
  2. Open fresh warm-tier MDBX for blocks H₀+1..
  3. State queries: warm tier → fallback snapshot

Implementation status: **NOT IMPLEMENTED** (task #94 in repo).
Estimate: 3-5 days of focused work to:

  - Add `--bootstrap.mode=snapshot|leaves|none` flag
  - New `internal/ethel/snapshotreader` package that reads
    PlainState from the RecSplit/EF snapshot files
  - Warm-tier overlay MDBX with proper read/write paths
  - PlainState interface to dispatch warm → snapshot
  - Integration into cmd/eth-el bootstrap service

## ⚠ Caplin mainnet setup

For all 3 modes the catch-up flow needs:

  - `cmd/eth-el -tags n42el` build with Caplin enabled
  - `--caplin.enabled` + `--caplin.network mainnet`
  - `--caplin.checkpoint.url <URL>` for fast beacon checkpoint sync
    (operator-supplied)
  - Open inbound libp2p ports 9000/tcp + 9000/udp for peers
  - Stable network connection to ETH beacon peers

The Caplin merge (Strategy A) is now complete (`commit 0ca2cae9`),
so the binary builds with -tags n42el. Per task #94, what's not
yet wired is the bootstrap-mode dispatch + the warm-tier reader.

## Phased plan (1-2 days, ONLY task #94 is a hard blocker)

| Phase | Work | Time |
|:--|:--|--:|
| 0 | task #94 — snapshot-continue path in cmd/eth-el (minimal/full bootstrap) | 3-5 days |
| 1 | copy snapshot+chaindata+freezer into a fresh datadir per mode | ~30 min IO (86 GB minimal) |
| 2 | build cmd/eth-el -tags n42el | 1 min |
| 3 | smoke-launch eth-el → opens snapshot, fresh warm tier, no work | <1 min |
| 4 | Caplin connects to mainnet beacon peers + checkpoint syncs | ~10 min |
| 5 | catch up 55K blocks (minimal) — Engine API receives + EVM executes | 3-12 hours |
| 6 | observe 12-sec live loop for 1+ hour | observation only |
| 7 | repeat for full (~5 hours more IO for 567 GB bodyc) | same compute as minimal |
| 8a | archive: witness-replay → produce CS (multi-day on full chain) | multi-day |
| 8b | archive: RebuildState reads derived CS → PlainState | hours |
| 8c | archive: catch-up + 12s live (same as minimal) | hours-days |

For TESTING archive bootstrap quickly today, skip 8a by reusing
the CS already derived in D:/N42-eth1177 from a past witness-replay
run. The published archive manifest ships only witness; CS is
local-derived in production.

## What we have NOW that's reusable

- `cmd/n42-eth-snapshot` for fetch/verify/catch-up/follow on the
  publisher mirror (synthetic IT-7 PASS already)
- `internal/ethel/snapshotprestart` with --catch-up.mode=auto
  strategy selection (G3 + G4 shipped)
- Caplin Strategy A merged → `-tags n42el` builds the embedded CL
- `internal/ethel/bootstrap` (leaves-rebuild path; needs the
  snapshot-mode sibling)

## What's the next concrete action

Realistically, the next session-sized chunk is **task #94**:

  1. Design `internal/ethel/snapshotreader` package
  2. Implement PlainState reads via RecSplit + EF (we already
     have the writer in `cmd/reth-snapshot-export`)
  3. Add warm-tier overlay
  4. Wire `--bootstrap.mode=snapshot` into cmd/eth-el
  5. Unit + e2e tests
  6. Smoke launch minimal mode against D:/n42-snapshot

Once that ships, the 3-mode REAL-CHAIN test becomes
straightforward IO + multi-hour wait.

## Open decisions for operator

| Decision | Why |
|:--|:--|
| Caplin checkpoint URL | Pick a trusted beacon snapshot host |
| Disk for fresh datadirs | 86 GB per minimal, 720 GB per full, 849 GB+ per archive |
| When to regenerate leaves journal for archive | Multi-day compute; requires geth ancient or witness-replay |
| Acceptance criteria | "12-sec live observed for N slots without divergence"? Specific metrics? |

## Companion docs

- `docs/ethel/n42-eth-client-distribution.md` — 3-mode design (snapshot-continue vs leaves-rebuild)
- `docs/ethel/post-bootstrap-sync-plan.md` — Stage 2 G1..G5 (orchestration done)
- `docs/ethel/state-storage-tiered-design.md` — snapshot+warm tier RFC (the design task #94 implements)
- `docs/ethel/devlog-eth-el-node.md` — current cmd/eth-el architecture
