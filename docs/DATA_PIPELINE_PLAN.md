# N42 Data Pipeline Architecture Plan

## 1. Data Classification

All chain data divided into 3 categories by access pattern:

### Category A — Ledger (block-ordered, sequential access)

| Data | Source | Format | Segment | Compression |
|------|--------|--------|---------|-------------|
| headers | Geth ancient / P2P | columnar 8192-block | `headers.seg` | delta+dict+zstd |
| bodies | Geth ancient / P2P | columnar 8192-block | `bodies.seg` | columnar+zstd |
| senders | ecrecover(bodies) | raw 20B×N | `senders.seg` | batch-64+zstd |
| receipts | EVM exec / Geth ancient | Reth compact | `receipts.seg` | batch-64+zstd |

### Category B — State Derivation (block-ordered, used for state rebuild)

| Data | Source | Format | Segment | Compression |
|------|--------|--------|---------|-------------|
| leaves_journal | EVM exec | plain-key diffs | `leaves.seg` | batch-64+zstd |
| account_cs | EVM exec | columnar | `account_cs.seg` | dict+zstd |
| storage_cs | EVM exec | columnar | `storage_cs.seg` | dict+zstd |
| block_witness | EVM exec | access set | `witness.seg` | batch-64+zstd |

### Category C — Index (key-ordered, random access, locally built)

| Data | Source | Format | Segment | Compression |
|------|--------|--------|---------|-------------|
| txlookup | txHash→blockNum | RecSplit+EF | `txlookup.seg` | embedded |
| account_hist | addr→blocks[] | RecSplit+varint | `account_hist.seg` | zstd |
| storage_hist | addr+slot→blocks[] | RecSplit+varint | `storage_hist.seg` | zstd |

**Key insight:** Category A+B can be downloaded via BT. Category C must be built locally (RecSplit is CPU-bound, not worth distributing).

---

## 2. Unified Segment Interface

All data uses the same `SegmentStore` file layout:

```
{datadir}/segments/{type}/
├── segments.idx           # [12B per segment: fileNum(2)+flags(2)+datOff(4)+riOff(4)]
├── data.0000.dat          # frames ≤2GB, auto-rotate
├── data.0001.dat
└── ...
```

### Frame format (inside data.NNNN.dat)

**Category A/B (batch data):**
```
[4B size][batch-64 zstd compressed block]
```
No ri (RecSplit) needed — access by block number = segment_number × segment_size + offset.

**Category C (indexed data):**
```
[4B datSize][datBytes][4B riSize][riBytes]
```
RecSplit embedded for O(1) key→value lookup.

### Unified Go interface

```go
// pkg: internal/segment

type SegmentType string

const (
    SegHeaders     SegmentType = "headers"
    SegBodies      SegmentType = "bodies"
    SegSenders     SegmentType = "senders"
    SegReceipts    SegmentType = "receipts"
    SegLeaves      SegmentType = "leaves"
    SegAccountCS   SegmentType = "account_cs"
    SegStorageCS   SegmentType = "storage_cs"
    SegWitness     SegmentType = "witness"
    SegTxLookup    SegmentType = "txlookup"
    SegAccountHist SegmentType = "account_hist"
    SegStorageHist SegmentType = "storage_hist"
)

// SegmentStore is the unified read/write interface for all segment types.
type SegmentStore struct {
    dir      string
    segType  SegmentType
    writer   *SegmentStoreWriter
    reader   *SegmentStoreReader
}

func Open(dir string, segType SegmentType) (*SegmentStore, error)
func (s *SegmentStore) SegmentCount() uint64
func (s *SegmentStore) ReadData(segNum uint64) ([]byte, error)
func (s *SegmentStore) ReadIndex(segNum uint64) (*recsplit.Index, error)  // Category C only
func (s *SegmentStore) WriteSegment(data []byte, ri []byte) (uint64, error)
func (s *SegmentStore) Close()
```

---

## 3. Directory Structure

```
{datadir}/
├── chaindata/
│   └── mdbx.dat                 # L1 HOT: PlainState + recent 8192 blocks
│
├── segments/                    # L2/L3: all immutable segment data
│   ├── headers/                 # columnar 8192-block segments
│   │   ├── segments.idx
│   │   └── data.0000.dat
│   ├── bodies/
│   ├── senders/
│   ├── receipts/
│   ├── leaves/                  # N42 advantage: direct state rebuild
│   ├── account_cs/
│   ├── storage_cs/
│   ├── witness/
│   ├── txlookup/                # RecSplit indexed
│   ├── account_hist/            # RecSplit indexed
│   └── storage_hist/            # RecSplit indexed
│
├── torrents/                    # BT metadata
│   ├── headers.0000.torrent
│   ├── bodies.0000.torrent
│   └── ...
│
├── tmp/                         # build scratch space
└── network.json                 # P2P keys
```

### Segment file naming

All segment types share the same naming:
- `segments.idx` — master index (12B per entry)
- `data.NNNN.dat` — data files, rotate at 2GB, sequential numbering
- No loose `.ri` files — RecSplit embedded in data frames

### Segment sizes

| Type | Blocks/Segment | Rationale |
|------|---------------|-----------|
| headers, bodies, senders, receipts, leaves, account_cs, storage_cs, witness | **8192** | Aligned with EraE/torrent chunks, good BT piece size |
| txlookup, account_hist, storage_hist | **1,000,000** | RecSplit works best with large key sets |

---

## 4. Node Startup & Sync Flow

### Phase 1: Fast Sync (BT download)

```
START
  │
  ├─ Open MDBX (empty or checkpoint)
  │
  ├─ Discover peers via DHT
  │
  ├─ Download Category A segments via BT (highest priority):
  │    headers → bodies → senders
  │    (receipts can wait — not needed for state)
  │
  ├─ Download Category B segments via BT:
  │    leaves_journal (core — enables state rebuild)
  │    account_cs, storage_cs (for history queries)
  │    witness (optional, for stateless validation)
  │
  └─ Background: build Category C indices (local CPU):
       txlookup (RecSplit from bodies)
       account_hist (RecSplit from account_cs)
       storage_hist (RecSplit from storage_cs)
```

### Phase 2: State Rebuild (N42 advantage)

Traditional clients (Geth/Erigon/Reth) must re-execute ALL blocks to rebuild state.
N42 uses `leaves_journal` for **O(1) state replay per block**:

```
for block = 0..HEAD:
    journal = segments.ReadLeaves(block)
    for each (key, value) in journal:
        PlainState.Put(key, value)
    if block % commitInterval == 0:
        MDBX.Commit()

// No EVM execution needed!
// 11.9M blocks in ~10 minutes vs 68 minutes with full EVM
```

**State root verification:** every N blocks, compute JMT/BMT/MPT root from PlainState and compare with header.Root.

### Phase 3: Catch-up to Head

```
latestSegment = max downloaded segment
headBlock = P2P network head

if headBlock - latestSegment > 8192:
    # Still behind — continue BT download for remaining segments
    # Simultaneously execute recent blocks via EVM
    for block = latestSegment..headBlock:
        header, body = P2P.FetchBlock(block)
        Execute(header, body)  # full EVM
        Accumulate(leaves, cs, receipts, witness)
        if accumulated >= 8192:
            FlushSegment()     # write batch to segment store
else:
    # Near head — switch to live sync
    SwitchToLiveSync()
```

### Phase 4: Live Sync (consensus)

```
LIVE SYNC LOOP:
    block = consensus.NextBlock()
    Execute(block)  # full EVM

    # Write to MDBX (hot, <8192 blocks buffer)
    MDBX.Put(PlainState changes)
    MDBX.Put(leaves_journal[blockNum])

    # Accumulate for next segment flush
    histAccumulator.AddChanges(block)

    if MDBX.hotBlocks >= 8192:
        # Background thread: freeze oldest 8192 blocks
        FreezeSegment(oldestBlocks)
        # Background thread: create BT torrent for new segment
        CreateTorrent(newSegment)
        # Background thread: seed to network
        SeedTorrent(newSegment)
```

---

## 5. Background Processing Threads

### Thread 1: Segment Freezer (priority: HIGH)

Runs when MDBX hot buffer exceeds 8192 blocks.

```
Input:  MDBX hot tables (headers, bodies, receipts, etc.)
Output: segments/{type}/data.NNNN.dat

Steps:
1. Read 8192 blocks from MDBX
2. Encode each type:
   - headers: columnar delta+dict+zstd
   - bodies: columnar tx-field separation + zstd
   - senders/receipts/leaves/witness: batch-64 + zstd
   - account_cs/storage_cs: columnar dict + zstd
3. Append to SegmentStore
4. Delete frozen data from MDBX
```

### Thread 2: Index Builder (priority: MEDIUM)

Runs after new segments are frozen. CPU-bound, non-blocking.

```
Input:  segments/bodies/, segments/account_cs/, segments/storage_cs/
Output: segments/txlookup/, segments/account_hist/, segments/storage_hist/

Steps:
1. TxLookup: scan bodies segment, build RecSplit (txHash→blockNum)
   - Uses Elias-Fano for block boundaries (1000x compression)
2. AccountHist: scan account_cs segment, build RecSplit (addr→blocks[])
   - Delta-encoded block numbers, 96.9% single-write optimization
3. StorageHist: scan storage_cs segment, build RecSplit (addr+slot→blocks[])
```

### Thread 3: Torrent Seeder (priority: LOW)

Runs after segments are written.

```
Input:  segments/{type}/data.NNNN.dat
Output: torrents/{type}.NNNN.torrent

Steps:
1. Compute SHA-1 piece hashes (256KB pieces)
2. Create .torrent metadata
3. Map contentHash ↔ infohash in CAS bridge
4. Begin seeding via BitTorrent DHT
```

---

## 6. BT Download Strategy

### Torrent organization

Each segment type is one torrent. Torrent piece boundaries align with segment boundaries for efficient partial download.

```
Torrent: "n42-headers-0000"
  File: data.0000.dat (contains segments 0..N)
  Pieces: 256KB each
  Tracker: DHT + bootstrap nodes

Torrent: "n42-headers-0001"
  File: data.0001.dat (continues from N+1)
```

### Download priority order

```
Priority 1 (required for state):
  headers  → verify chain, extract state roots
  leaves   → rebuild PlainState without EVM

Priority 2 (required for full node):
  bodies   → tx content, uncle rewards
  senders  → pre-computed ecrecover results

Priority 3 (required for archive queries):
  receipts     → eth_getTransactionReceipt
  account_cs   → debug_traceTransaction revert
  storage_cs   → debug_traceTransaction revert
  witness      → stateless validation

Priority 4 (locally built, not downloaded):
  txlookup     → built from bodies
  account_hist → built from account_cs
  storage_hist → built from storage_cs
```

### Verification during download

```
1. Download headers segment
2. Verify header chain: parentHash[N+1] == hash(header[N])
3. Download leaves segment
4. Replay leaves → PlainState
5. Every 8192 blocks: verify stateRoot against header
6. If mismatch: discard segment, re-download from different peer
```

---

## 7. Data Flow Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                        GENESIS / BT SYNC                        │
│                                                                 │
│  BT Download ──→ segments/{type}/data.NNNN.dat                  │
│       │                                                         │
│       ├─ headers  ──→ verify chain                              │
│       ├─ leaves   ──→ replay PlainState (no EVM!)               │
│       ├─ bodies   ──→ [bg] build txlookup                      │
│       ├─ senders  ──→ ready                                     │
│       ├─ receipts ──→ ready                                     │
│       ├─ account_cs ──→ [bg] build account_hist                 │
│       └─ storage_cs ──→ [bg] build storage_hist                 │
│                                                                 │
│  State ready when: headers + leaves downloaded + replayed       │
│  Full node ready when: all Priority 1-3 downloaded              │
│  Archive ready when: all data + indices built                   │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                        LIVE SYNC                                │
│                                                                 │
│  P2P block ──→ EVM Execute ──→ MDBX hot buffer                 │
│       │              │                                          │
│       │              ├─ PlainState updates                      │
│       │              ├─ leaves_journal entry                    │
│       │              ├─ receipts                                │
│       │              ├─ account_cs / storage_cs                 │
│       │              └─ block_witness                           │
│       │                                                         │
│       └─ Every 8192 blocks:                                     │
│            [bg] Freeze → segments/{type}/                       │
│            [bg] Build RecSplit indices                          │
│            [bg] Create torrent + seed                           │
└─────────────────────────────────────────────────────────────────┘
```

---

## 8. Migration from Current Layout

### Current state (scattered)

```
{datadir}/
├── mdbx.dat
├── ancient/          # Geth freezer format (cidx/cdat)
│   ├── headers.cidx, headers.0000.cdat
│   ├── receipts.cidx, receipts.0000.cdat
│   └── ...
├── txlookup/         # old SegmentStore location
├── history/          # old SegmentStore location
│   ├── account_hist/
│   └── storage_hist/
├── headers.bin       # standalone compact file
└── bodies/           # standalone compact dir
```

### Migration steps

1. Create `segments/` directory tree
2. Move existing SegmentStore data:
   - `txlookup/` → `segments/txlookup/`
   - `history/account_hist/` → `segments/account_hist/`
   - `history/storage_hist/` → `segments/storage_hist/`
3. Convert Geth freezer tables → segment format:
   - `ancient/headers.*` → `segments/headers/` (columnar re-encode)
   - `ancient/receipts.*` → `segments/receipts/` (batch-64 re-encode)
4. Drop old `ancient/` directory
5. Update all code paths to use unified `segments/` root

### Backward compatibility

Reader auto-detects:
- 12B idx entries (new) vs 8B (legacy)
- Embedded ri vs separate `.ri` files
- segments.idx presence vs old naming convention

---

## 9. Size Estimates (ETH mainnet ~20M blocks)

| Segment Type | Raw Size | Compressed | Ratio | Notes |
|-------------|----------|------------|-------|-------|
| headers | ~55 GB | ~6 GB | 11% | columnar+delta+dict |
| bodies | ~400 GB | ~120 GB | 30% | columnar tx fields |
| senders | ~50 GB | ~38 GB | 76% | batch-64 zstd (20B×N) |
| receipts | ~200 GB | ~20 GB | 10% | Reth compact + batch-64 |
| leaves | ~100 GB | ~25 GB | 25% | batch-64 zstd |
| account_cs | ~50 GB | ~12 GB | 24% | columnar dict+zstd |
| storage_cs | ~500 GB | ~140 GB | 28% | columnar dict+zstd |
| witness | ~80 GB | ~20 GB | 25% | batch-64 zstd |
| txlookup | ~220 GB | ~4 MB | 0.002% | RecSplit+EF |
| account_hist | ~60 GB | ~8 GB | 13% | RecSplit+varint+zstd |
| storage_hist | ~330 GB | ~16 GB | 5% | RecSplit+varint+zstd |
| **Total** | **~2 TB** | **~405 GB** | **20%** | vs Erigon 4TB, Reth 3.3TB |

**N42 advantage:** 405 GB total vs Erigon 4 TB (10x smaller) primarily because:
- No trie nodes stored (JMT/BMT computed on-the-fly)
- Columnar compression > dictionary+Huffman for structured data
- RecSplit+EF eliminates 220 GB txlookup
- leaves_journal replaces full EVM re-execution

---

## 10. Implementation Priority

### P0 — Core (required for basic node)

1. Unify `SegmentStore` interface (move from cscompact to `internal/segment/`)
2. Segment freezer thread (MDBX → segments)
3. Leaves replay for state rebuild
4. Header/body segment reader integration in P2P sync

### P1 — Sync (required for network participation)

5. BT torrent creation from segments
6. BT download with header-chain verification
7. Leaves download → fast state rebuild
8. Catch-up EVM execution for gap blocks

### P2 — Archive (required for full RPC)

9. Receipt/changeset segment readers for RPC queries
10. Background index builder (txlookup, hist)
11. `eth_getTransactionReceipt`, `debug_traceTransaction` from segments

### P3 — Optimization

12. Compression tuning per data type
13. Adaptive segment sizing
14. Hot/warm cache management
15. Parallel segment building
