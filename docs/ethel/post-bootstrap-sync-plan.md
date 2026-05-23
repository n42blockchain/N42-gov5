# Post-Bootstrap Sync Plan — Catch-up + 12-sec Live Loop

**Date:** 2026-05-22
**Status:** Concrete plan; stage 1 + 3 already shipped, stage 2 has
gaps tracked here.

This doc answers: **after a client finishes the snapshot bootstrap
(Phase F) at height H₀, how does it reach the current chain tip
H_tip, and how does it stay current with 12-second cadence?**

## Three-stage pipeline

```
[snapshot]              [catch-up]           [live]
  fetch ──► H₀ ─────────────────────► H_near ──────────► H_tip ──► (continues)
   ✓                    gap (this doc)         ✓
```

| Stage | Purpose | Status |
|:-:|:--|:--|
| 1 | Snapshot bootstrap to H₀ | ✓ shipped (Phase F) |
| 2 | Catch-up H₀ → H_near (close enough for live) | partial — three paths, varying maturity |
| 3 | Live ingest H_near → H_tip every ~12 s | ✓ shipped (Engine API + libp2p gossip) |

H_near = "within 1 epoch (~6 min) of tip" is a reasonable threshold;
inside that window the live path takes over and queues any in-flight
blocks via `chain.AddFutureBlock()`.

## Stage 2 — Catch-up (the gap)

Three independent mechanisms, each appropriate in different
situations:

### 2A — Delta apply (preferred for clients within retention window)

```
n42-eth-snapshot delta apply --source <mirror> --datadir <data> --mode <m>
```

**When it applies:**
- Client's manifest_id matches one of the publisher's
  `based_on_manifest_id` entries in `releases.json`
- I.e., client is no older than the retention horizon
  (`n42-eth-publish prune --keep-deltas K` retains K weeks)

**Cost:** ~few GB per week (best case with H.3b segmented snapshot:
~2 GB/week; current state without H.3b: ~30 GB/week)

**Status:** ✓ shipped (Phase H.2)

**Gap:** automation — a wrapper that loops `delta apply` until the
client is current. Currently the user has to call it each cycle.

### 2B — libp2p block range fetch (N42 chain only)

```
internal/sync/initialsync/   round-robin peer fetcher + FSM
internal/sync/rpc_blocks_by_range.go  libp2p RPC for block ranges
```

**When it applies:**
- Client is connected to N42 libp2p peers
- The chain is the **N42 native chain** (not ETH mainnet — different
  transport)

**Cost:** ~30-50 MB/s sustained on warm peers; ~1 day for the full
25 M block backfill (mostly bound by EVM execution at the EL)

**Status:** ✓ shipped

**Gap:** no automatic trigger from snapshot completion. The user
runs `cmd/n42` and it figures out the gap on startup.

### 2C — Engine API replay (post-Merge ETH chains)

```
internal/ethel/engineapi/service.go    accepts engine_newPayload
internal/api/engine_api_v1.go          executes V1/V2/V3 payloads
```

**When it applies:**
- N42 is the EL behind a Consensus Layer (Caplin embedded in
  `cmd/eth-el -tags n42el`, or external Lighthouse/etc.)
- The CL is itself synced; it knows the block range to send

**Cost:** dominated by EVM execution; ~3-5 s per block on the
reference machine. 25 M blocks ≈ multi-week single-threaded; in
practice CL replays only the gap since the snapshot.

**Status:** ✓ shipped (Engine API consumer)

**Gap:** the CL needs to be aware that the EL just snapshot-jumped
to H₀ and shouldn't send payloads for blocks ≤ H₀. The Engine API
spec handles this via `engine_forkchoiceUpdated` + payload status
SYNCING/VALID/INVALID; CL backs off when EL says SYNCING.

## Stage 3 — Live (12-second loop)

Already shipped, in two flavours:

### 3A — Engine API (mainnet post-Merge)

```
CL                            N42 EL
──                            ──────
engine_newPayloadV3 ────────► ProcessBlock (validate + exec)
                              EngineStateAdapter.commit() → MDBX
                              emit NewChainEvent
engine_forkchoiceUpdatedV3 ─► set canonical head
                              prune side-forks
```

Latency budget per slot (already validated):

| Phase | Typical |
|:--|--:|
| receive + decode payload | < 10 ms |
| EVM execution (1000 tx) | 1 – 3 s |
| state commit | 1 – 2 s |
| ForkchoiceUpdated + finalisation | < 100 ms |
| **Total** | **3 – 5 s** (inside the 12 s budget) |

### 3B — libp2p gossip (N42 native chain)

```
internal/sync/subscriber_blocks.go    GossipSub blocks → chain.InsertChain()
internal/sync/decode_pubsub.go        decode pubsub message
```

N42-chain blocks arrive via GossipSub topic; the subscriber inserts
them straight into `chain.InsertChain()` (or queues into
`chain.AddFutureBlock()` if parent isn't yet present).

HotStuff/apos consensus is notified post-import via the chain event
feed.

## The full client lifecycle

```
$ n42-eth-snapshot fetch  --source <mirror> --datadir /var/lib/n42 --mode archive
... downloads ~849 GB over 1-2 h ...
$ n42-eth-snapshot verify --datadir /var/lib/n42
✓ result: OK at height 25,000,000

# Option A: catch up via deltas (if within retention window)
$ n42-eth-snapshot delta apply --source <mirror> --datadir /var/lib/n42 --mode archive
✓ updated to 25,049,995 (50K blocks across 7 days)
$ n42-eth-snapshot delta apply --source <mirror> --datadir /var/lib/n42 --mode archive
✓ updated to 25,099,820

# Switch to live sync — pick A or B
# A. Run as Engine API EL behind a CL:
$ cmd/eth-el -tags n42el \
    --datadir /var/lib/n42 \
    --auth-rpc-addr 127.0.0.1:8551 \
    --engine-api
# CL handles the rest; EL executes whatever payloads arrive

# B. Run as N42-native peer:
$ cmd/n42 --datadir /var/lib/n42
# libp2p discovers peers, gossip blocks arrive, InsertChain consumes
```

## Gaps to close (this is the engineering backlog)

| # | Gap | Effort | Outcome |
|:-:|:--|:-:|:--|
| 1 | `n42-eth-snapshot catch-up` command that loops delta apply until current | 2-3 days | one-command catch-up |
| 2 | Health-check `n42-eth-snapshot status` reports current height vs publisher latest | 1 day | "am I behind?" answer |
| 3 | Auto-trigger on `cmd/eth-el` startup: if behind, run catch-up before opening Engine API | 2-3 days | seamless restart-after-downtime |
| 4 | `cmd/eth-el --catch-up-mode auto` picks delta apply vs libp2p vs engine-replay based on gap size | 3-5 days | one-line ops |
| 5 | Tip-vs-publisher monitoring loop (background goroutine pulls `releases.json`, applies delta when newer) | 3-5 days | autopilot for live mirror clients |
| 6 | (eth/68 devp2p sync) | 1-2 weeks | mainline ETH peer interop — see `sync-protocol-comparison.md` |

Gaps 1-5 use the existing snapshot + delta + libp2p + Engine API
primitives — they're orchestration, not new protocol work.

Gap 6 is the only one that adds new wire protocol surface.

## Recommended landing order

1. **Gap 2 (`status`)** — tells you whether you need catch-up at all.
   1 day. Shipped, you can detect what other work is needed.

2. **Gap 1 (`catch-up`)** — loops delta apply. The natural next
   command after `fetch`. 2-3 days.

3. **Gap 5 (autopilot)** — turns the client into a follower that
   stays in sync indefinitely. Once 1-2 are in, this is just a
   timer + retry.

4. **Gap 3-4** — wires the catch-up into `cmd/eth-el` startup so
   operators don't have to think about it. 1-2 weeks once 1-2-5
   exist.

5. **Gap 6 (eth/68)** — only if mainline ETH interop becomes a hard
   requirement.

## Companion documents

- `docs/ethel/n42-eth-client-distribution.md` — modes + manifests
- `docs/ethel/n42-eth-delta-updates.md` — delta wire format
- `docs/ethel/n42-eth-distribution-test-plan.md` — IT-1..IT-7
- `docs/ethel/sync-protocol-comparison.md` — eth/68 vs Engine API vs libp2p
- `docs/ethel/devlog-eth-el-node.md` — `cmd/eth-el` architecture
