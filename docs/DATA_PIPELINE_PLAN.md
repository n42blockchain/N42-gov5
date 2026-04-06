# N42 Data Pipeline Architecture Plan

## 1. Data Classification

All chain data divided into 3 categories by access pattern:

### Category A — Ledger (block-ordered, sequential access)

| Data | File Prefix | Source | Compression |
|------|-------------|--------|-------------|
| block headers | `headers` | P2P / Geth ancient | columnar 8192 + delta+dict+zstd |
| block bodies | `bodies` | P2P / Geth ancient | columnar 8192 + zstd |
| tx senders | `senders` | ecrecover(bodies) | batch-64 + zstd |
| tx receipts | `receipts` | EVM exec / Geth ancient | Reth compact + batch-64 + zstd |

### Category B — State Derivation (block-ordered, used for state rebuild)

| Data | File Prefix | Source | Compression |
|------|-------------|--------|-------------|
| state leaf diffs | `leaves` | EVM exec | batch-64 + zstd |
| account changesets | `acctcs` | EVM exec | columnar dict + zstd |
| storage changesets | `storcs` | EVM exec | columnar dict + zstd |
| block witnesses | `witness` | EVM exec | batch-64 + zstd |

### Category C — Index (key-ordered, random access, locally built)

| Data | File Prefix | Source | Compression |
|------|-------------|--------|-------------|
| tx hash index | `txindex` | RecSplit(bodies) | RecSplit + Elias-Fano |
| account history | `accthist` | RecSplit(acctcs) | RecSplit + varint + zstd |
| storage history | `storhist` | RecSplit(storcs) | RecSplit + varint + zstd |

**Key insight:** Category A+B can be downloaded via BT. Category C must be built locally (RecSplit is CPU-bound, not worth distributing).

---

## 2. File Layout

All chain archive data lives in a single `chain/` directory. File prefix = data type.

```
{datadir}/chain/
├── headers.cidx              # master index [12B/entry]
├── headers.0000.cdat         # data frames ≤2GB, auto-rotate
├── headers.0001.cdat
├── bodies.cidx
├── bodies.0000.cdat
├── senders.cidx
├── senders.0000.cdat
├── receipts.cidx
├── receipts.0000.cdat
├── leaves.cidx
├── leaves.0000.cdat
├── acctcs.cidx
├── acctcs.0000.cdat
├── storcs.cidx
├── storcs.0000.cdat
├── witness.cidx
├── witness.0000.cdat
├── txindex.cidx
├── txindex.0000.cdat
├── accthist.cidx
├── accthist.0000.cdat
├── storhist.cidx
└── storhist.0000.cdat
```

### Naming conventions

- Extension: `.cidx` (chain index), `.cdat` (chain data) — avoids OS file association conflicts
- Format: `{prefix}.cidx` for master index, `{prefix}.NNNN.cdat` for data (NNNN = 0000-padded file number)
- Rotation: new `.cdat` file when current exceeds 2GB
- No subdirectories per type — flat structure, file prefix is the namespace

### cidx entry format (12 bytes)

```
[2B fileNum LE][2B flags LE][4B datOffset LE][4B riOffset LE]
```

- Category A/B: riOffset = 0 (no RecSplit, access by block number)
- Category C: riOffset points to embedded RecSplit data within the cdat frame

### cdat frame format

**Category A/B (batch data):**
```
[4B size LE][compressed batch bytes]
```

**Category C (indexed data):**
```
[4B datSize LE][datBytes][4B riSize LE][riBytes]
```

### Segment sizes

| Category | Blocks/Segment | Rationale |
|----------|---------------|-----------|
| A/B (batch) | **8192** | EraE/torrent chunk aligned, ~200KB-2MB compressed |
| C (index) | **1,000,000** | RecSplit needs large key sets for efficiency |

### Full directory tree

```
{datadir}/
├── chaindata/mdbx.dat       # L1 HOT: PlainState + recent blocks
├── chain/                    # L2/L3: all immutable chain archive
│   ├── headers.cidx
│   ├── headers.0000.cdat
│   ├── bodies.cidx
│   ├── bodies.0000.cdat
│   ├── ...                   # all 11 types in flat layout
│   └── storhist.0000.cdat
├── torrents/                 # BT metadata per cdat file
└── tmp/                      # build scratch space
```

---

## 3. Unified Go Interface

```go
// pkg: internal/chain

type DataType string

const (
    Headers  DataType = "headers"
    Bodies   DataType = "bodies"
    Senders  DataType = "senders"
    Receipts DataType = "receipts"
    Leaves   DataType = "leaves"
    AcctCS   DataType = "acctcs"
    StorCS   DataType = "storcs"
    Witness  DataType = "witness"
    TxIndex  DataType = "txindex"
    AcctHist DataType = "accthist"
    StorHist DataType = "storhist"
)

// Store reads and writes chain archive segments for a single data type.
type Store struct { ... }

func OpenStore(chainDir string, dt DataType) (*Store, error)
func (s *Store) SegmentCount() uint64
func (s *Store) ReadData(segNum uint64) ([]byte, error)
func (s *Store) ReadIndex(segNum uint64) (*recsplit.Index, error)  // Category C
func (s *Store) WriteSegment(data []byte, ri []byte) (uint64, error)
func (s *Store) Close()
```

---

## 4. Node Startup & Sync Flow

### Phase 1: BT Download

```
START
  │
  ├─ Open MDBX (empty or from checkpoint)
  ├─ Discover BT peers via DHT
  │
  ├─ Priority 1 — download headers + leaves:
  │    headers.*.cdat  → verify header chain (parentHash linkage)
  │    leaves.*.cdat   → replay PlainState (no EVM!)
  │
  ├─ Priority 2 — download remaining ledger:
  │    bodies, senders, receipts
  │
  ├─ Priority 3 — download archive data:
  │    acctcs, storcs, witness
  │
  └─ Background CPU — build local indices:
       txindex   (from bodies)
       accthist  (from acctcs)
       storhist  (from storcs)
```

### Phase 2: State Rebuild (N42 advantage)

Traditional clients must re-execute ALL blocks via EVM to get PlainState.
N42 replays `leaves` directly — **pure Put(), no EVM**:

```
H_bt = highest block covered by BT data

for block = 0..H_bt:
    journal = chain.ReadLeaves(block)
    for each (key, value) in journal:
        PlainState.Put(key, value)
    if block % 10000 == 0:
        MDBX.Commit()

// 11.9M blocks: ~10 min (leaves replay) vs ~68 min (full EVM)
```

State root verification: every N blocks, compute root from PlainState, compare with header.Root.

### Phase 3: Catch-up to Head (EVM execution)

BT data has a boundary (H_bt). Gap between H_bt and network head must be filled by EVM execution:

```
H_bt   = last block with BT data (e.g. 20,000,000)
H_head = current network head     (e.g. 20,050,000)

for block = H_bt + 1 .. H_head:
    header, body = P2P.FetchBlock(block)
    Execute(header, body)               # full EVM
    Accumulate(leaves, receipts, cs, witness)
    if accumulated >= 8192:
        FlushSegment()                  # write to chain/*.cdat
```

### Phase 4: Live Sync (consensus)

Node is at head. New blocks arrive from consensus:

```
LIVE SYNC LOOP:
    block = consensus.NextBlock()       # PoA/PoS/HotStuff
    Execute(block)                      # full EVM

    # Write to MDBX hot buffer
    MDBX.Put(PlainState changes)
    MDBX.Put(hot leaves / receipts / cs / witness)

    # Accumulate for segment flush
    histAccumulator.AddChanges(block)

    if hotBlocks >= 8192:
        # Background: freeze oldest 8192 blocks → chain/*.cdat
        FreezeSegment()
        # Background: build RecSplit indices
        BuildIndices()
        # Background: create torrent + seed to network
        SeedTorrent()
```

---

## 5. Background Processing Threads

### Thread 1: Segment Freezer (HIGH priority)

Triggers when MDBX hot buffer exceeds 8192 blocks.

```
Input:  MDBX hot tables
Output: chain/{prefix}.cidx + chain/{prefix}.NNNN.cdat

Encoding per type:
  headers    → columnar delta+dict+zstd (8192 blocks)
  bodies     → columnar tx-field separation + zstd (8192 blocks)
  senders    → batch-64 + zstd
  receipts   → Reth compact + batch-64 + zstd
  leaves     → batch-64 + zstd
  acctcs     → columnar dict + zstd
  storcs     → columnar dict + zstd
  witness    → batch-64 + zstd

After freeze: delete frozen blocks from MDBX
```

### Thread 2: Index Builder (MEDIUM priority)

Triggers after new segments are frozen. CPU-bound.

```
Input:  chain/bodies.*.cdat, chain/acctcs.*.cdat, chain/storcs.*.cdat
Output: chain/txindex.cidx+cdat, chain/accthist.cidx+cdat, chain/storhist.cidx+cdat

txindex:  RecSplit(txHash→blockNum) + Elias-Fano block boundaries
accthist: RecSplit(addr→blocks[]) + delta-varint + zstd
storhist: RecSplit(addr+slot→blocks[]) + delta-varint + zstd
```

### Thread 3: Torrent Seeder (LOW priority)

Triggers after new cdat files are written.

```
Input:  chain/{prefix}.NNNN.cdat
Output: torrents/{prefix}.NNNN.torrent

Steps: SHA-1 piece hashing → .torrent metadata → DHT seeding
```

---

## 6. BT Download Strategy

### Torrent per cdat file

```
Torrent: "n42-headers-0000"
  File:    chain/headers.0000.cdat
  Pieces:  256KB each
  Tracker: DHT + bootstrap nodes
```

### Download priority

```
P1 (state rebuild):  headers + leaves
P2 (full node):      bodies + senders + receipts
P3 (archive):        acctcs + storcs + witness
P4 (locally built):  txindex + accthist + storhist  ← NOT downloaded
```

### Verification

```
1. Download headers.*.cdat
2. Verify: parentHash[N+1] == hash(header[N])
3. Download leaves.*.cdat
4. Replay leaves → PlainState
5. Every 8192 blocks: stateRoot == header.Root?
6. Mismatch → discard + re-download from different peer
```

---

## 7. Data Flow Diagram

```
┌──────────────────────────────────────────────────────────────┐
│                     BT SYNC (cold start)                     │
│                                                              │
│  BT peers ──→ chain/*.cdat                                   │
│     │                                                        │
│     ├─ headers  → verify chain                               │
│     ├─ leaves   → replay PlainState (no EVM)  → state at H_bt│
│     ├─ bodies   → [bg] build txindex                         │
│     ├─ senders  → ready                                      │
│     ├─ receipts → ready                                      │
│     ├─ acctcs   → [bg] build accthist                        │
│     └─ storcs   → [bg] build storhist                        │
│                                                              │
│  State ready: headers + leaves replayed to H_bt              │
└──────────────────────────────────────────────────────────────┘
                         │
                         ▼
┌──────────────────────────────────────────────────────────────┐
│                   EVM CATCH-UP (H_bt → H_head)               │
│                                                              │
│  P2P blocks ──→ EVM Execute ──→ all outputs                  │
│     block H_bt+1 .. H_head      (leaves, receipts, cs, etc.) │
│                                                              │
│  Accumulated outputs → chain/*.cdat (every 8192 blocks)      │
└──────────────────────────────────────────────────────────────┘
                         │
                         ▼
┌──────────────────────────────────────────────────────────────┐
│                   LIVE CONSENSUS SYNC                         │
│                                                              │
│  Consensus block ──→ EVM Execute ──→ MDBX hot buffer         │
│                                                              │
│  Every 8192 blocks:                                          │
│    [bg] Freeze → chain/*.cdat                                │
│    [bg] Build indices (txindex, accthist, storhist)           │
│    [bg] Create torrent + seed                                │
└──────────────────────────────────────────────────────────────┘
```

---

## 8. Migration from Current Layout

### Current (scattered)

```
{datadir}/
├── mdbx.dat
├── ancient/          # Geth freezer (headers.cidx, headers.0000.cdat, ...)
├── txlookup/         # SegmentStore (segments.idx, data.0000.dat)
├── history/
│   ├── account_hist/ # SegmentStore
│   └── storage_hist/ # SegmentStore
├── headers.bin       # standalone compact
└── bodies/           # standalone compact (bodies.idx, bodies.0000.dat)
```

### Target (unified)

```
{datadir}/
├── chaindata/mdbx.dat
└── chain/
    ├── headers.cidx, headers.0000.cdat
    ├── bodies.cidx, bodies.0000.cdat
    ├── ...
    └── storhist.cidx, storhist.0000.cdat
```

### Migration

1. Rename `segments.idx` → `{prefix}.cidx`, `data.NNNN.dat` → `{prefix}.NNNN.cdat`
2. Move all into `chain/`
3. Convert Geth freezer tables → chain format (columnar re-encode)
4. Reader auto-detects legacy vs new format

---

## 9. Size Estimates (ETH mainnet ~20M blocks)

| Prefix | Raw | Compressed | Ratio | Notes |
|--------|-----|-----------|-------|-------|
| headers | ~55 GB | ~6 GB | 11% | columnar+delta+dict |
| bodies | ~400 GB | ~120 GB | 30% | columnar tx fields |
| senders | ~50 GB | ~38 GB | 76% | batch-64 (20B×N) |
| receipts | ~200 GB | ~20 GB | 10% | Reth compact |
| leaves | ~100 GB | ~25 GB | 25% | batch-64 |
| acctcs | ~50 GB | ~12 GB | 24% | columnar dict |
| storcs | ~500 GB | ~140 GB | 28% | columnar dict |
| witness | ~80 GB | ~20 GB | 25% | batch-64 |
| txindex | ~220 GB | ~4 MB | 0.002% | RecSplit+EF |
| accthist | ~60 GB | ~8 GB | 13% | RecSplit+varint |
| storhist | ~330 GB | ~16 GB | 5% | RecSplit+varint |
| **Total** | **~2 TB** | **~405 GB** | **20%** | |

Comparison: Erigon ~4 TB, Reth ~3.3 TB, Geth ~2.5 TB, **N42 ~405 GB**.

---

## 10. Implementation Priority

### P0 — Core

1. Unified `Store` interface in `internal/chain/`
2. Flat `chain/` directory with `{prefix}.cidx` + `{prefix}.NNNN.cdat`
3. Segment freezer thread (MDBX → chain/)
4. Leaves replay for state rebuild

### P1 — Sync

5. BT torrent creation from cdat files
6. BT download with header-chain verification
7. Leaves download → fast state rebuild → EVM catch-up to head
8. Consensus sync switch

### P2 — Archive

9. Receipt/changeset readers for RPC queries
10. Background index builder (txindex, accthist, storhist)
11. `eth_getTransactionReceipt`, `debug_traceTransaction`

### P3 — Optimization

12. Compression tuning per type
13. Hot/warm cache management
14. Parallel segment building
