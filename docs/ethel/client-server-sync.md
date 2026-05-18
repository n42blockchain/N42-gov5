# N42-eth Client/Server Sync Flow

**Date:** 2026-05-18
**Companions:**
- `archive-reduction-honest-targets.md` — file inventory, sizes, growth projections
- `n42-eth1-freezer-catalogue.md` — block-data freezer format
- `state-storage-tiered-design.md` — tiered storage RFC

This doc covers the **sync flow** (how clients bootstrap, catch up,
stay live, update). File layout and sizes live in the companion docs.

---

## Two client modes

| Mode | Tier downloads | Disk | Query capability |
|------|---------------|------|------------------|
| **Full archive** | snapshot + history + code + blocks | ~270 GB | `eth_getXAt(addr, anyBlock)` |
| **Fast** | snapshot + code + recent delta (skip history) | ~30 GB | `eth_getXAt(addr, "latest")` only |

Both modes use the same eth/68 DevP2P for live sync; difference is
which historical data tier they retain.

---

## Trust anchor: blake2b manifest

Implementation: `internal/bundle/manifest.go` (commit `9a8773a7`).

```json
{
  "version": 1,
  "algorithm": "blake2b-256",
  "segment_size": 67108864,     // 64 MiB BT-style chunks
  "chain_id": 1,
  "block_range": {"start": 0, "end": 25101867},
  "files": [
    {"path": "snapshot/...", "size": ..., "whole_hash": "...",
     "segments": ["...", "..."]}
  ]
}
```

**Multi-server consensus**: client downloads the same file from N ≥ 2
mirrors; any hash disagreement aborts. No single server is trusted.
Manifest itself must come from a trusted out-of-band source
(signed git tag, project website, on-chain `SegmentRoot` commitment).

Per-segment hashes (files ≥ 1 GiB) let a client retry just one
corrupt 64 MiB chunk instead of re-downloading multi-GB files.

---

## Mode 1: Full archive

```
┌─────────────────────────────────────────────────────────────┐
│ Phase 1 — Bootstrap (one-time, ~270 GB)                     │
├─────────────────────────────────────────────────────────────┤
│ 1. Fetch & verify manifest.json (signed/on-chain anchor)    │
│ 2. Parallel HTTP/BT download from M ≥ 2 mirrors:            │
│    snapshot/, history/, code/, blocks/                      │
│ 3. blake2b-256 per file (+ per-segment retry on mismatch)   │
│ 4. Cross-mirror hash agreement check                        │
└─────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────┐
│ Phase 2 — Catch-up (H₀ → live, minutes-hours)               │
├─────────────────────────────────────────────────────────────┤
│ 1. eth/68 peers → discover live head H_live                 │
│ 2. For range (H₀, H_live]:                                  │
│    - GetBlockHeaders → verify parent hashes + sigs          │
│    - GetBlockBodies, optionally GetReceipts                 │
│    - Re-execute via local EVM                               │
│    - Pipeline writes leaves / acctcs / storcs / witness     │
│ 3. Fast path: if server published delta covering the range, │
│    skip re-execution, import the delta directly             │
└─────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────┐
│ Phase 3 — Live (steady state)                               │
├─────────────────────────────────────────────────────────────┤
│ - NewBlock / NewBlockHashes drive import                    │
│ - EVM execute, write MDBX + freezers                        │
│ - Weekly Friday: download delta + new history step (~2 GB)  │
│   verify blake2b, merge in                                  │
└─────────────────────────────────────────────────────────────┘
```

---

## Mode 2: Fast (snapshot-only)

Bootstrap identical to mode 1 but **skips `history/`** (saves ~90 GB).
Catch-up + live identical.

Retention policy: keep changesets in a rolling window (default 7
days). After that, `acctcs.cdat` / `storcs.cdat` rotation drops old
segments. No upstream history download — historical-block queries
fail with "out of retention window".

Use case: wallet backend, MEV searcher, RPC for `latest` only.

---

## eth/68 messages used

Standard Ethereum DevP2P. See `internal/p2p/`.

| Message | Direction | Use |
|---------|-----------|-----|
| `Status` | both | Negotiate chainID, head, TD |
| `NewBlockHashes` | server→client | Head announcement |
| `NewBlock` | server→client | Full block push |
| `GetBlockHeaders` | client→server | Range header fetch |
| `GetBlockBodies` | client→server | Bodies for headers |
| `GetReceipts` | client→server | Receipts (skip if re-executing) |
| `NewPooledTransactionHashes` | both | Pending tx |
| `GetPooledTransactions` | client→server | Pull pending tx |

Sustained throughput on 32-core/NVMe: **~50K tx/sec re-execution**,
~5× faster than the chain produces blocks. Week-old bootstrap catches
up in ~10 min.

---

## Server-side weekly publish (Friday 00:00 UTC)

```
1. Server's own pipeline up-to-date to H_week
2. Build delta-H_lastweek+1-H_week.tar.zst:
   ├─ Block data (headers/bodies/receipts) for the week
   ├─ Pre-computed acctcs/storcs (saves clients re-execution)
   └─ Code (new contracts only)
3. Build incremental history step:
   ├─ account.[H_lastweek+1-H_week].{mphf,idx,kv}  (~1 GB)
   └─ storage.[H_lastweek+1-H_week].{mphf,idx,kv}  (~1.5 GB)
4. Append entries to manifest.json
5. Re-sign manifest, push to mirrors + BT swarm
```

Client polls (or webhook):
- ~5 min/week to download + verify
- Step-files are append-only, never re-build → old downloads remain valid

Periodically (every 4-6 months): server merges 4-6 weekly steps into
a single "month" step file. Same blake2b verify, identical reader
code path.

---

## Failure modes & defenses

| Threat | Defense |
|--------|---------|
| Single malicious mirror | Multi-mirror hash consensus |
| Stale/outdated manifest | `--max-manifest-age` flag rejects stale |
| 64 MiB chunk corrupt mid-DL | Per-segment retry |
| eth/68 peer feeds bad block | Header sig + on-chain `SegmentRoot` |
| Server stops publishing | Federated mirror + BT swarm |
| Disk bit-rot post-install | Periodic `n42 verify` cron |
| Client missed a week's delta | eth/68 catch-up handles up to months natively |

---

## Implementation status

| Component | Status | Location |
|-----------|--------|----------|
| Snapshot writer | ✓ done | `cmd/reth-snapshot-export` |
| History writer (MPHF+fp) | ✓ done | `cmd/n42-history-build --mphf` |
| History verify | ✓ done | `cmd/n42-history-verify` |
| Code freezer reader | ✓ done | `internal/ethel/codes_freezer_reader.go` |
| Block freezer | ✓ done | `modules/rawdb/freezer/` |
| eth/68 DevP2P | ✓ done | `internal/p2p/` |
| blake2b manifest | ✓ done | `internal/bundle/` |
| **Delta tar.zst builder** | TODO | `cmd/n42-delta-build` |
| **Client bootstrap orchestrator** | partial | `cmd/n42` has live; needs phase 1 |
| **Multi-mirror hash consensus** | TODO | extend `internal/bundle/verify.go` |
| **BT swarm support** | TODO | reuse `internal/distributed/storage/torrent/` |
| **Weekly server cron pipeline** | TODO | runbook + systemd unit |

---

## Future CLI

```bash
# Phase 1: bootstrap full or fast mode
n42 bootstrap --mode=fast \
    --manifest=https://n42.org/manifest.json \
    --mirrors=https://m1.n42.org,https://m2.n42.org,bt:magnet:...

# Phase 2/3: sync + live (default behavior)
n42 sync --datadir=/data

# Server: publish weekly delta
n42-server publish-delta --range=25101867-25151867

# Verify local archive against manifest
n42 verify --manifest=/data/manifest.json
```
