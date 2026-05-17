# Tiered State Storage RFC — extreme compression + fast sync for N42

Status: **Draft / RFC**
Owner: ETH-EL team
Last updated: 2026-05-17

This RFC proposes replacing N42's monolithic PlainState MDBX (~278 GB at
25M blocks) with a 3-tier scheme adapted from Erigon E3, plus a daily
overlay layer unique to N42 that enables sub-10-minute boot of light
nodes. Goal: support archive, 30-day-prune, 24-hour-prune,
witness-only, and tip-executor profiles from one data archive.

Existing material:
- [n42-eth1-freezer-catalogue.md](n42-eth1-freezer-catalogue.md) — current chain/freezer layout
- [geth-fork-freezer-threshold.md](geth-fork-freezer-threshold.md) — patched geth feeding the input ancient

---

## Part 1 — How Erigon E3 stores world state

### 1.1 Six domains

`db/kv/tables.go:696-702` splits "state" into 6 independent domains:

| Domain | Content |
|--------|---------|
| `accounts` | nonce / balance / codeHash per address |
| `storage` | (addr ‖ slot) → value (52-byte composite key) |
| `code` | codeHash → bytecode |
| `commitment` | Merkle Patricia Trie nodes (state root computation) |
| `receipt` | per-block tiny receipt (without logs) |
| `rcache` | full receipts + logs (off by default) |

Each domain has independent: hot MDBX table (`AccountVals`, etc.) +
history `.v`/`.ef` files + per-step `.kv`/`.bt`/`.efi` files.

### 1.2 Step + frozen file structure (`db/config3/config3.go`)

```
DefaultStepSize         = 390,625 txnums   ≈ 200 (modern) to 1,500 (early) blocks
DefaultStepsInFrozenFile = 256
→ biggest immutable file covers 100M txnums ≈ 100K blocks
```

Each domain step produces:

```
accounts.0000-0001.kv     dictionary-compressed (K, V) array
accounts.0000-0001.bt     4 KB B-tree pages → file offset in .kv
accounts.0000-0001.efi    cuckoo-like existence filter (~1.5 B/key)
accounts.0000-0001.v      history values (per-step)
accounts.0000-0001.ef     Elias-Fano inverted index: key → [changeTxNums]
```

Read path:

```
hot MDBX  →  step-1.kv  →  step-8.kv  →  step-64.kv  →  step-256.kv
   ^                                                          ^
 newest                                                  oldest sealed

Per layer: .efi negative-cache filters >99% misses cheaply;
then .bt binary search → .kv page read.
```

Historical read ("X at block N"):

```
.ef[key] → binary search ≤ txN → latest changeTxNum → corresponding step's .v file
```

### 1.3 Per-domain compression policy (`db/state/statecfg/state_schema.go:193-330`)

| Domain | Keys | Vals | Notes |
|--------|------|------|-------|
| accounts | not compressed (small) | not compressed | hot-path optimization |
| storage | **compressed** (52 B composite, addrs heavily shared) | not compressed | |
| code | not compressed | **compressed** (4× on mainnet, 2.5× on bor) | bytecode patterns |
| commitment | **compressed** (trie prefix sharing) | page-64 zstd | `ReplaceKeysInValues=true` during merge |
| receipt | none | none | tiny |
| history | none | page-64 zstd | random-access penalty acceptable for cold reads |

### 1.4 Compression algorithm (`db/seg/`)

NOT zstd / snappy. Erigon's custom suffix-array dictionary:
- Sample-build a pattern dictionary (`MinPatternLen=20, MaxDictPatterns=64K`)
- Replace patterns scoring above `MinPatternScore` with dictionary IDs
- Optional page-level grouping (`ValuesOnCompressedPage=64`) + zstd per page

### 1.5 Snap-sync protocol (`db/snapshotsync/`, `db/downloader/`)

```
1. Manifest fetch
   - HTTP GET preverified.toml from GitHub OR Cloudflare R2
   - Lines: filename + size + sha1, signed by erigon team

2. BitTorrent download (anacrolix/torrent + WebSeed HTTP backup)
   - magnet:?xt=urn:btih:<infohash> per file

3. Verify each file's sha1 vs manifest

4. Locally rebuild .bt / .efi indexes from .kv  (~5-15 min CPU)

5. Commit ready: read state root from latest commitment step,
   align with head header.
```

ETH mainnet 25M blocks ≈ **600 GB** total domain + history, vs ~3 TB
for a reth/geth archive node (~80% smaller).

---

## Part 2 — Proposed N42 design

### 2.1 Three tiers

```
┌──────────────────────────────────────────────────────────────────┐
│ Tier 2: Tip MDBX (last 1024 blocks)         ~2 GB    mutable    │
│         FreezerThreshold = 1024 (already shipped)               │
├──────────────────────────────────────────────────────────────────┤
│ Tier 1: Daily overlay (last 1-N days)       ~200 MB/day  signed │
│         NEW format, committee-signed, HTTP distribution         │
├──────────────────────────────────────────────────────────────────┤
│ Tier 0: Monthly cold snapshots (immutable)  ~600 GB total       │
│         Borrowed from Erigon E3, step=1M blocks                 │
└──────────────────────────────────────────────────────────────────┘
```

### 2.2 File layout (`chain/freezer/`)

Extends today's catalogue (see n42-eth1-freezer-catalogue.md) with the
Erigon-style per-step domain values + a new `daily/` subtree:

```
chain/freezer/
  # Tier 0 — sealed cold (step = 1,000,000 blocks):
  accountvals.0000000-1000000.kv     dictionary-compressed (K, V)
  accountvals.0000000-1000000.bt     B-tree index
  accountvals.0000000-1000000.efi    existence filter
  storagevals.<step>.kv              52-B composite key, keys compressed
  storagevals.<step>.bt
  storagevals.<step>.efi
  codevals.<step>.kv                 replaces existing codes.cidx (vals compressed)
  codevals.<step>.bt
  commitmentvals.<step>.kv           N42 HPH trie nodes serialized
  commitmentvals.<step>.bt

  # Tier 0 — history (extends existing accthist/storhist):
  accounthist.<step>.v               page-64 zstd compressed values
  accounthist.<step>.ef              Elias-Fano inverted index
  storagehist.<step>.v
  storagehist.<step>.ef

  # Tier 0 — block data (existing, kept as input source):
  senders.cidx + .NNNN.cdat
  bodyc.cidx   + .NNNN.cdat
  headerc.cidx + .NNNN.cdat
  receipts.cidx + .NNNN.cdat         optional, from receipt-copy

  # Tier 1 — daily warm overlay (NEW):
  daily/
    20260515.accounts.delta          ~5 MB   touched keys + new vals
    20260515.storage.delta           ~50 MB
    20260515.code.delta              ~1 MB   sparse new contracts
    20260515.commitment.delta        ~10 MB  trie incremental
    20260515.witness.cdat            ~150 MB signed block witnesses
    20260515.manifest.toml           committee BLS-aggregate signature
```

Step size = **1,000,000 blocks** (aligns with existing `HistSegmentSize`
already used by accthist/storhist — see project_recsplit_history). One
step = one "month" of mainnet at current rate.

### 2.3 N42 vs Erigon — design deltas

| Decision | Erigon E3 | N42 RFC | Why |
|---------|-----------|---------|-----|
| Step size | 390 K txnums (~5-50 K blocks) | **1 M blocks** | Aligns with existing HistSegmentSize; cross-tool reuse |
| Commitment | Erigon-native trie | **N42 HPH** | Already verified bit-for-bit ETH-compatible on 11.9M blocks (project_mpt_complete) |
| Compression | Erigon suffix-array | **Port `db/seg` verbatim** | Best-in-class; pure Go; no need to reinvent |
| Tip MDBX | ~1000 blocks | **1024 blocks** | Matches patched-geth FreezerThreshold |
| Distribution | BitTorrent + HTTP CDN | **Same** + committee-signed manifest | Removes single-vendor trust root |
| Witness layer | Absent | **Reuse existing witness.cdat** | Lets light/24h nodes catch up without re-executing EVM |
| Daily overlay | Absent | **NEW per-day signed bundle** | Sub-10-minute boot for warm-range nodes |

### 2.4 Network sync flow

```powershell
# Phase 0 — Manifest (<1 s)
GET https://snapshots.n42.network/<chain>/latest.toml
verify committee BLS aggregate signature
→ list of cold-step files (magnet links) + daily-overlay URLs

# Phase 1 — Cold tier (30-90 min, network + disk bound)
torrent download <magnets>
+ HTTP webseed (CDN) fallback when peers are sparse
rebuild .bt / .efi indexes locally  (~5-15 min CPU)

# Phase 2 — Warm overlay (5-30 min)
for day in (cold_tip..yesterday):
  GET .../daily/{day}.{accounts,storage,code,commitment}.delta
  apply to temporary MDBX overlay

# Phase 3 — Tip (10-60 s)
GET last 1024 blocks of witness from peer
witness-replay → fills Tier 2 MDBX

→ node at head, ready to execute new blocks
```

### 2.5 Node profile matrix

| Profile | Retains | Size | Sync time | Use case |
|---------|---------|------|-----------|----------|
| **Archive** | Tier 0 + 1 + 2 (all) | ~600 GB | 1-4 h | Full RPC, `debug_trace*` |
| **30-day** | Last 30 daily overlays + Tier 2 | ~30 GB | 20-40 min | Wallet backend, recent-range RPC |
| **24-hour** | Last 1 daily overlay + Tier 2 | **~2.5 GB** | **5-10 min** | Mobile wallet, light client |
| **Witness-only** | 1 day of witness.cdat | 200 MB/day | seconds | Stateless validator |
| **Tip-executor** | Tier 2 only + live block witness | ~2 GB | ~1 min | Block builder / sequencer |

All five profiles read from the same archive — only retention differs.

### 2.6 24-hour node boot sequence

```
1. GET committee-signed cold snapshot manifest (~1 KB)         <1 s
2. From latest cold step, extract commitment .kv → state root
   + Merkle proof to anchor block                              <5 s
3. Verify anchor block hash on the canonical chain
4. Download yesterday + today daily overlay (~400 MB)        2-5 min
5. Download last 1024 blocks of witness (~150 MB)             ~30 s
6. witness-replay 1024 blocks → fill Tier 2 MDBX            ~10-60 s
7. Subscribe to gossip block stream, ready for next block
```

Critical: **no full-chain EVM replay anywhere**. The whole boot is
download + Merkle verify + 1024 blocks of witness-driven re-execution.

---

## Part 3 — Implementation plan

Rough order of operations, smallest piece first:

| Phase | Work | Estimate | Depends on |
|-------|------|----------|------------|
| **P1** | Port `db/seg` (suffix-array dictionary compression) into `lib/seg` | 2-3 d | — |
| **P2** | `accountvals` / `storagevals` writer: extend existing cscompact framework with `.kv` / `.bt` / `.efi` triplet, hook into executor commit boundary | 1 w | P1 |
| **P3** | `commitmentvals` + N42 HPH serializer to `.kv` | 3-4 d | P2 |
| **P4** | Daily overlay format + signer + HTTP serve | 1 w | P2 |
| **P5** | BitTorrent downloader (reuse anacrolix/torrent) + committee-signed manifest check | 1 w | P4 |
| **P6** | Tier-aware reader chain (Tier 2 → Tier 1 → Tier 0) inside `modules/state` IntraBlockState | 1-2 w | P2 |
| **P7** | `cmd/n42 --retention=<24h\|7d\|30d\|archive>` startup logic; manifest-driven download orchestrator | 1 w | P6 |
| **P8** | Witness layer wiring to daily overlay (witness.cdat already produced by executor) | 2-3 d | P4 |

Total: **6-8 weeks** to full functionality. **3-4 weeks** to a working
archive + 24h-node demo (P1 → P5).

---

## Key design decisions

1. **Reuse N42 HPH for commitment**, not Erigon's trie. Saves
   significant engineering and stays bit-for-bit ETH-compatible (proven
   on the 11.9M-block full replay in project_mpt_complete).

2. **Port Erigon's `db/seg` package wholesale**, not rebuild. It is
   pure Go, well-tested, and gives the 4× code / ~50% overall
   compression ratios that real Erigon snapshots demonstrate.

3. **Extend the existing cscompact + RecSplit framework**. The current
   accthist/storhist files are already a simplified form of `.v` + `.ef`.
   Adding `.bt` and `.efi` triplets is an incremental change, not a
   rewrite.

4. **Committee-signed manifest**, not single-vendor trust. Uses N42's
   existing BLS aggregate signature scheme (project_bls_consensus_design)
   so the snapshot distribution does not depend on a GitHub / Cloudflare
   root of trust the way Erigon's does.

5. **Witness layer as the catch-up shortcut**. Erigon does not have
   per-block witnesses; N42 already produces witness.cdat (24.998M+
   items in the live datadir). This lets light nodes skip full EVM
   replay during boot — the single largest sync-time saving.

6. **Daily overlay is a new tier**. Cold tier (BitTorrent, slow) does
   not move; warm tier (HTTP, fast) rolls one bundle per day,
   accelerating distribution of recent data without touching the cold
   archive.

7. **Tip uses FreezerThreshold = 1024** (already in the patched geth).
   Same boundary throughout the stack; no second number to tune.

---

## Open questions

- Daily overlay → cold tier promotion cadence: monthly seems natural
  (matches step size), but means a freshly-promoted month invalidates
  ~30 daily overlays. Need a clean fold-down protocol.
- Should `.delta` files share the same dictionary across days
  (smaller) or stand alone (independent download)? Erigon doesn't
  face this question because it has no daily overlay.
- Witness signing: per-block (small but many signatures) vs
  per-epoch-aggregated (larger but fewer verifies)?
- How to bound the size of `commitment.delta` when a day touches a
  cold-tier-spanning prefix?
