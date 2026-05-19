# N42-eth Client/Server Sync Flow

**Date:** 2026-05-18
**Companions:**
- `archive-reduction-honest-targets.md` — file inventory, sizes, growth projections
- `n42-eth1-freezer-catalogue.md` — block-data freezer format
- `state-storage-tiered-design.md` — tiered storage RFC

This doc covers the **sync flow** (how clients bootstrap, catch up,
stay live, update). Detailed file-format specs and per-file sizes live
in the companion docs.

---

## Server-side data layout (overview)

A canonical N42-eth archive server hosts:

```
archive/
├── snapshot/                          ← world state at sealed height H₀
│   ├── accounts.[0-H₀].codedict       ← codeHash dictionary
│   ├── accounts.[0-H₀].idx            ← RecSplit MPHF, 1.71 bit/key
│   ├── accounts.[0-H₀].ef             ← Elias-Fano (ord → byte offset)
│   ├── accounts.[0-H₀].val.zst        ← zstd values (codeHash → 3B id)
│   ├── storage.[0-H₀].idx
│   ├── storage.[0-H₀].ef
│   └── storage.[0-H₀].val.zst
│                                      ← total ~24 GB @ H₀=25M
├── history/                           ← per-key timeline blobs
│   ├── account.[0-H₀].mphf            ← MPHF for ever-touched addrs
│   ├── account.[0-H₀].idx             ← page offset table
│   ├── account.[0-H₀].kv              ← zstd pages, packed (block,value) blobs
│   ├── storage.[0-H₀].mphf
│   ├── storage.[0-H₀].idx
│   └── storage.[0-H₀].kv
│                                      ← total ~130 GB @ H₀=25M
├── code/                              ← bytecode addressed by addr
│   ├── codes.cidx                     ← sorted (addr,fileNum,offset)
│   ├── codes.0000.cdat                ← zstd bytecodes
│   ├── codes.0001.cdat
│   └── ...                            ← ~6 GB
├── blocks/                            ← header + bodies + receipts (+ senders)
│   ├── headers.cidx + headers.NNNN.cdat
│   ├── bodies.cidx + bodies.NNNN.cdat
│   ├── receipts.cidx + receipts.NNNN.cdat
│   └── senders.cidx + senders.NNNN.cdat   (optional; recompute via ecrecover)
│                                      ← ~150 GB raw / ~80 GB without optional
├── delta/                             ← weekly increments since H₀
│   ├── delta-H₀+1-H₀+wₐ.tar.zst       ← week-a's blocks + state diffs
│   ├── delta-H₀+wₐ+1-H₀+wᵦ.tar.zst
│   └── ...
└── manifest.json                      ← blake2b hashes of every file above
```

See `archive-reduction-honest-targets.md` for the per-file inventory
with read APIs.

---

## Two client modes

| Mode | Tier downloads | Disk | Query capability |
|------|---------------|------|------------------|
| **Full archive** | snapshot + history + code + blocks | ~310 GB | `eth_getXAt(addr, anyBlock)` |
| **State-only archive** | snapshot + history + code (no blocks) | ~160 GB | historical state queries; block data from peers |
| **Fast** | snapshot + code + recent delta (skip history) | ~30 GB | `eth_getXAt(addr, "latest")` only |

All modes use the same eth/68 DevP2P for live sync; difference is
which historical tier they retain.

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

Use case: explorer, RPC provider serving arbitrary `block` parameter
queries (`eth_getStorageAt(addr, slot, block)`, `eth_getBalance(addr,
block)`, etc.).

```
┌──────────────────────────────────────────────────────────────┐
│ Phase 1: One-time bootstrap (~310 GB, hours)                 │
├──────────────────────────────────────────────────────────────┤
│ 1. Fetch manifest.json from trusted source                   │
│    └─ Verify against signed git tag or on-chain SegmentRoot  │
│ 2. Parallel download from M ≥ 2 servers (HTTP/BT):           │
│    ├─ snapshot/*  (24 GB)                                    │
│    ├─ history/*   (~130 GB)                                  │
│    ├─ code/*      (6 GB)                                     │
│    └─ blocks/*    (~150 GB)                                  │
│ 3. Per file: blake2b-256 vs manifest                         │
│    └─ Per-segment retry on mismatch (64 MiB chunks)          │
│ 4. Multi-server consensus check:                             │
│    └─ Same file from N servers → same hash, else abort       │
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────┐
│ Phase 2: Catch up to head (block H₀ → live)                  │
├──────────────────────────────────────────────────────────────┤
│ 1. Compute local head = H₀ (manifest block_range.end)        │
│ 2. Open eth/68 DevP2P connections to N42 peers               │
│ 3. NewBlockHashes / NewBlock subscriptions                   │
│ 4. For each gap [H₀+1 .. live_head]:                         │
│    ├─ GetBlockHeaders, GetBlockBodies, GetReceipts           │
│    ├─ Verify chain validity (parent hash, ECDSA sigs)        │
│    └─ Re-execute via EVM → produce acctcs/storcs locally     │
│ 5. Optional: download delta-H₀+1-H₀+W.tar.zst                │
│    └─ Skip re-execution for blocks covered by delta          │
│       (delta IS the same acctcs+storcs that re-exec produces)│
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────┐
│ Phase 3: Live sync (steady state, ongoing)                   │
├──────────────────────────────────────────────────────────────┤
│ - eth/68 NewBlock notifications drive block import           │
│ - Each new block: EVM re-execute → write to MDBX             │
│ - acctcs/storcs auto-build for unwind window (~7 days)       │
│ - Every Friday 00:00 UTC: server publishes delta-*.tar.zst   │
│   ├─ Client downloads ~400 MB                                │
│   ├─ Verify blake2b vs manifest_update                       │
│   └─ Merge into history tier (incremental .kv file)          │
└──────────────────────────────────────────────────────────────┘
```

---

## Mode 2: Fast (snapshot-only, prune history)

Use case: RPC provider that only serves CURRENT state
(`eth_getBalance(addr, "latest")`, `eth_call`); no historical-block
queries.

```
┌──────────────────────────────────────────────────────────────┐
│ Phase 1: Snapshot bootstrap (~30 GB, < 1 hour)               │
├──────────────────────────────────────────────────────────────┤
│ 1. Fetch manifest.json                                       │
│ 2. Download only the snapshot tier:                          │
│    ├─ snapshot/accounts.*.val.zst (3.43 GB) + idx + ef       │
│    ├─ snapshot/storage.*.val.zst (18.23 GB) + idx + ef       │
│    └─ code/* (6 GB)                                          │
│ 3. blake2b verify (per file + per 64 MiB segment)            │
│ 4. Skip history/ entirely (~130 GB saved)                    │
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────┐
│ Phase 2: Catch up to head via eth/68                         │
├──────────────────────────────────────────────────────────────┤
│ Identical to full-archive Phase 2.                           │
│ Snapshot's accounts.idx and storage.idx serve `getState(K)`  │
│ in O(1) — execution can begin immediately at H₀+1.           │
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────┐
│ Phase 3: Live + retention policy                             │
├──────────────────────────────────────────────────────────────┤
│ - Live blocks executed as in full-archive mode               │
│ - acctcs/storcs writes go to ring buffer:                    │
│   ├─ Keep most recent N days (configurable, default 7)       │
│   └─ Older changesets discarded after their week's snapshot  │
│     is built into history (NOT downloaded — built locally)   │
│ - Optional client-side history: build incrementally from N₀  │
│   onward to your retention window only                       │
│ - No upstream history download                               │
└──────────────────────────────────────────────────────────────┘
```

**Trade-off**: cannot serve `eth_getBalanceAt(addr, ANY_BLOCK)` for
blocks < H₀ + retention_window. Cheapest mode; ideal for a wallet
backend, MEV searcher, or anything needing only the "live" view.

---

## eth/68 messages used (catch-up + live)

N42 uses the standard Ethereum DevP2P `eth/68` sub-protocol. See
`internal/p2p/` for the wire implementation.

| Message | Direction | Use |
|---------|-----------|-----|
| `Status` | peer ↔ peer | Negotiate chain ID, head, total difficulty on handshake |
| `NewBlockHashes` | server → client | Lightweight head announcement (hash only) |
| `NewBlock` | server → client | Full block push at finality |
| `GetBlockHeaders` | client → server | Range header fetch (used during catch-up) |
| `GetBlockBodies` | client → server | Tx + uncles for previously-fetched headers |
| `GetReceipts` | client → server | Receipt fetch (skip if client re-executes locally) |
| `NewPooledTransactionHashes` | both | Pending tx announcement |
| `GetPooledTransactions` | client → server | Pull pending tx by hash |

**Client catch-up algorithm:**
1. After bootstrap, local head = H₀.
2. eth/68 peer reports head = H_live. Gap = H_live − H₀.
3. Fetch headers in batches of 1024 from H₀+1 upward.
4. Verify header chain (parent hash, signature / PoS attestation).
5. Fetch bodies + (optionally) receipts.
6. Execute via local EVM (or skip if a downloaded delta covers the range).
7. Maintain N42 pipeline: leaves / acctcs / storcs / witness as configured.

**Sustained throughput on modern hardware (32-core, NVMe):**
- **~50K tx/sec re-execution** locally
- ~5× faster than the chain produces blocks
- Week-old bootstrap catches up in **~10 min execution time**
- Disk I/O dominates above ~100K tx/sec; further parallel-EVM (Phase 5+) can hit ~200K tx/sec on dense ranges

---

## Server-side weekly publish (Friday 00:00 UTC)

```
1. Server's own pipeline catches up to H_week
2. Build delta-H_lastweek+1-H_week.tar.zst:
   ├─ Block data (headers/bodies/receipts) for the week
   │  ~7,200 blocks/day × 7 days ≈ 50K blocks
   ├─ Pre-computed acctcs/storcs (saves clients re-execution)
   ├─ Code (new contracts only — typically few hundred)
   └─ Total: ~400 MB after zstd
3. Build incremental history step files:
   ├─ account.[H_lastweek+1-H_week].{mphf,idx,kv}  (~1 GB)
   └─ storage.[H_lastweek+1-H_week].{mphf,idx,kv}  (~1.5 GB)
   Append-only: existing history files are NEVER rebuilt
4. Append entries to manifest.json
   blake2b-256 every new file; manifest.version++
5. Re-sign manifest, push to mirrors + BT swarm
6. Optional: tag git commit `archive-YYYY-MM-DD` with manifest hash
   for out-of-band trust anchor
```

**Client side**: poll (or webhook):
- Detect new manifest version → fetch only entries with new paths
- Download + verify delta (~400 MB) + history step (~2.5 GB)
- ~5 min/week wall on residential 100 Mbps
- Step-files are append-only → old downloads remain valid forever

**Step file merge cadence** (server side, transparent to clients):
- Every 4-6 months: server merges 4-6 weekly steps into a single
  "month" step file (same MPHF+fp format)
- Reduces total file count from ~24 per year to ~12-15
- Old weekly step files DELETED from server after monthly merge
  published (kept ~30-day grace period for laggard clients)
- Every 12 months: months merged into yearly step files
- Reader is multi-step-aware — no client code change needed

**On-chain anchor (optional)**:
- Each manifest commitment can be hashed and posted on-chain as
  `SegmentRoot[Hₖ] = blake2b(manifest_kᵗʰ_version)`
- Clients verify off-chain manifest matches on-chain SegmentRoot
  before trusting it
- Trustless chain-of-custody without a centralized PKI

---

## Recovery from missing/bad delta

If a client misses a delta or one fails blake2b verification:

1. **Native fallback**: eth/68 catch-up handles up to ~weeks of gap
   without any delta — just slower (re-execute each block locally).
2. **Bulk re-sync from later anchor**: if the gap exceeds ~1 month,
   the faster recovery is to fetch a newer `manifest.json` and
   incrementally apply from a recent rollup point (skips re-execution
   for the rolled-up range).
3. **Server retention**: servers MUST retain every published delta
   file indefinitely (~400 MB × 52 weeks = ~20 GB/year — cheap).
   Manifest history keeps every published version (~few MB total).
4. **BT swarm fallback**: even if the original publisher goes down,
   any client that downloaded a delta becomes a seeder. Lost-server
   recovery via BT is the failure-of-failsafe.

## Failure modes & defenses

| Threat | Defense |
|--------|---------|
| Single malicious mirror serves wrong data | Multi-mirror hash consensus; reject minority hashes |
| Old client downloads outdated manifest | Manifest carries `generated_at`; client rejects if older than `--max-manifest-age` |
| Server stops publishing | Federated mirror network; BT swarm survives single-node loss |
| eth/68 peer feeds invalid block | Header signature + state-root commitment via on-chain SegmentRoot |
| 64 MiB chunk corrupt mid-download | Per-segment hash → re-fetch only that 64 MiB |
| Disk bit-rot post-install | Periodic `n42 verify` cron checks blake2b on stored files |
| Client missed a week's delta | eth/68 catch-up handles up to months natively |
| Manifest signing key compromise | Time-locked: only signatures within `--max-manifest-age` accepted; out-of-band trust anchor (on-chain SegmentRoot or signed git tag) bootstraps any new key |

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
