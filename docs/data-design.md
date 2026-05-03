# N42 Data Design

This document is the authoritative inventory of every persistent data
class in an N42 datadir. It catalogs:

- Where each data class lives (MDBX or freezer)
- The on-disk encoding (cidx/cdat layout, batch + zstd compression, RLP/V2)
- Who writes it, who consumes it, and which capability requires it
- The relationship to standard Ethereum data (Reth/Erigon/Geth equivalents)

This complements `docs/ethel/freezer-tables.md` (which focuses narrowly on
the EthEL replay pipeline output) by extending coverage to consensus
data, MPT proofs, lookup indices, and mobile/BLS verifier paths.

---

## 1. Storage tier overview

An N42 datadir has TWO persistence tiers:

```
<datadir>/
  mdbx.dat                     # MDBX (B+tree, mmap, transactional)
  mdbx.lck
  chain/
    codes.cidx, codes.NNNN.cdat        # large append-only CAS-style table
    senders.cidx                       # SegmentStore senders (alternate format)
    freezer/
      headers.cidx, headers.NNNN.cdat  # per-block, batch-zstd
      bodies.{cidx,cdat}
      receipts.{cidx,cdat}
      senders.{cidx,cdat}
      witness.{cidx,cdat}
      acctcs.{cidx,cdat}
      storcs.{cidx,cdat}
      accthist.{cidx,cdat}             # RecSplit history shards
      storhist.{cidx,cdat}
      txindex.{cidx,cdat}
```

| Tier | Use cases | Random access | Append-only | Crash safety |
|------|-----------|---------------|-------------|--------------|
| MDBX | mutable state, indexes, consensus state | O(log n) | no | tx.Commit fsync |
| freezer (cidx/cdat) | append-only chain history, history bitmaps | O(1) by item index | yes | per-segment fsync + cidx fsync |

The split is the same trade-off as Erigon/Reth: MDBX for hot mutable
state with full ACID, freezer for cold append-only data where bulk
zstd compression beats per-row compression.

---

## 2. Freezer file format

Each freezer table is a pair: `{name}.{ext}idx` (the index) plus zero or
more `{name}.NNNN.{ext}dat` (the data segments). Default `ext = "c"`.

### 2.1 cidx (index file)

```
+-------------------+----------+----------+----------+--   --+----------+
| optional 16B hdr  | entry[0] | entry[1] | entry[2] |       | entry[N] |
+-------------------+----------+----------+----------+--   --+----------+
       16 B            6 B        6 B        6 B               6 B
```

- **Header** (16 bytes, optional — legacy tables omit it):
  - `[0..3]` magic `'CIDX'`
  - `[4]`    version (currently `1`)
  - `[5]`    flags (bit `0x01` = compressed, `0x02` = batch mode)
  - `[6]`    `batchSize` (`uint8`, typically `64`; `0` = no batch)
  - `[7]`    `entrySize` (currently `6`)
  - `[8..15]` reserved
- **Each entry** is exactly `6 bytes`:
  - `[0..1]` `fileNum` (`uint16` BE) — which `.NNNN.cdat` segment
  - `[2..5]` `offset`  (`uint32` BE) — byte offset within that segment

`Items()` is computed as `(cidx_size - header_size) / 6`.

### 2.2 cdat (data segments)

- Numbered `{name}.NNNN.cdat` starting at `0000`.
- Max segment size: 2 GiB (`maxFileSize`). When reached, writer rotates
  to the next number.
- For batch mode + zstd: each segment is a sequence of zstd blobs, one
  per batch (NOT per item).

### 2.3 Batch mode + zstd compression

When a table opts in (most write paths use batch mode):

1. The writer accumulates `batchSize` raw items (default **64**).
2. Concatenates them with a 4-byte length prefix per item:
   `[len:4 BE][item_0][len:4 BE][item_1]…[len:4 BE][item_{B-1}]`
3. zstd-compresses the whole concatenated buffer in one shot.
4. Appends the compressed blob to the current cdat at byte `offset`.
5. Writes `batchSize` cidx entries — **all sharing the same `(fileNum,
   offset)`**. So `cidx_count = item_count`, and consecutive cidx entries
   for the same batch are identical.

`Retrieve(item)` first reads the item's cidx entry to find
`(fileNum, offset)`, opens the cdat file, decompresses the batch, then
extracts the requested item by walking the length prefixes within the
batch.

This is why a healthy senders table covering 24,792,851 blocks has
~24 M cidx entries totaling `24M × 6 ≈ 144 MB`, **not** 4000.
A "small" cidx like `4,000` entries on a table that should have millions
indicates either a partial dataset (sender-recovery only ran for the
first 4000 blocks) or a different layout from the writer.

### 2.4 zstd dictionaries (optional)

Compressed tables MAY have per-segment `{name}.NNNN.zdict` files. If
present, the writer trains a zstd dictionary on the segment's data and
uses it to compress the batches in that segment. Reader auto-detects
and loads. Improves compression ratio on small repetitive batches.

---

## 3. Data classes

Each section: layout, format, writer, consumer.

### 3.1 账本数据 — Ledger (chain history)

The mainnet chain proper. In N42, ledger comes from `--ancient` (Geth
ancient) and is **mirrored** in compact form under `chain/` only when
the optional `header-compact`/`body-compact` subcommands run.

| Table | Tier | Key | Value | Codec | Source |
|-------|------|-----|-------|-------|--------|
| `Headers` | MDBX (`Header`) | block_num(8B BE) + hash(32B) | RLP header | RLP | rebuilt from freezer/ancient on startup |
| `headers` (compact) | freezer | item = block_num | header (compressed columns: parentHash delta, bloom, etc.) | adaptive dict + delta-varint + zstd | `cmd/ethexec header-compact` |
| `Bodies` | MDBX (`BlockBody`) | block_num(8B BE) + hash(32B) | RLP body | RLP | optional |
| `bodies` (compact) | freezer | item = block_num | body (cols: tx, uncle, withdrawal RLPs) | per-column zstd, top-100 To-addr dict | `cmd/ethexec body-compact` |
| `HeaderCanonical` | MDBX | block_num(8B BE) | hash(32B) | raw | execution stage |
| `HeaderTD` | MDBX | block_num + hash | total difficulty (RLP big.Int) | RLP | rebuilt from freezer |
| `HeadBlockKey` / `HeadHeaderKey` | MDBX | constant | hash(32B) | raw | sync stage marker |

Authoritative copy is the **Geth ancient at `--ancient`**, not the
N42 datadir. The compact mirrors are a portability optimization so a
node can replay without the upstream Geth ancient. Compact body reader
is currently disabled in the main run loop (see G1 in
`docs/ethel/freezer-tables.md`).

### 3.2 生成数据 — Generated (sender recovery, code import)

Data not present in the chain itself but derived once per
synchronization run.

| Table | Tier | Key | Value | Codec | Writer |
|-------|------|-----|-------|-------|--------|
| `senders` | freezer | item = block_num | concatenated 20B addresses (one per tx) | raw 20B/tx, batch zstd | `cmd/ethexec sender-recovery` |
| `Senders` (alt) | MDBX | block_num + hash | sender list (20B × tx_count) | raw bytes | legacy path |
| `senders` (segment) | freezer at `chain/senders.cidx` | block_num | same | raw 20B/tx + RecSplit-style segments | `sender-recovery --erigon-db` |
| `codes` | freezer at `chain/codes.cidx` | item = code index | contract bytecode | zstd batch | `cmd/ethexec code-import` |
| `Code` | MDBX | code_hash(32B) | bytecode | raw | EVM during execution |
| `ContractCode` | MDBX (`HashedCodeHash`) | addr_hash + incarnation | code_hash(32B) | raw | legacy Erigon-style mapping |

`senders` is **optional** — execution falls back to live `ecrecover` if
the freezer table is missing. Saves ~45 ms/block on dense blocks.

### 3.3 执行生成数据 — Execution-derived

Produced by `cmd/ethexec` running blocks.

| Table | Tier | Key | Value | Codec | Notes |
|-------|------|-----|-------|-------|-------|
| `receipts` | freezer | item = block_num | per-block receipt list | reth-style Compact (1B flags + tight fields) | `executor.writeOutputs` |
| `Receipts` | MDBX | block_num(8B BE) | canonical receipts | RLP | RPC `eth_getTransactionReceipt` consumer |
| `Log` | MDBX | block_num + tx_id | RLP logs | RLP | tx-level log lookup |
| `BlockWitness` | MDBX | block_num(8B BE) | serialized witness (state access set) | custom (`witness.go`) | mobile-SDK stateless verifier input |
| `witness` | freezer | item = block_num | same as `BlockWitness`, batch zstd | custom + zstd | per-block verify replay |

### 3.4 状态数据 — Plain state (current state, in MDBX)

| Table | Key | Value | Flags | Notes |
|-------|-----|-------|-------|-------|
| `Account` | address(20B) | V2-encoded `StateAccount` (nonce/balance/codeHash) | — | Erigon V2 codec; one row per active account |
| `Storage` (PlainStorageState) | address(20B) + slot(32B) | raw value (BE-minimal, ≤32B) | DupSort + AutoDupSortKeysConversion | Physical key=20B, dup value=32B slot+raw_value |
| `Code` | code_hash(32B) | raw bytecode | — | content-addressed |
| `SnapshotAccount` | address(20B) | proto-encoded `StateAccount` | — | flat-snapshot disk layer (Snap-sync) |
| `SnapshotStorage` | address + incarnation + slot | raw value | DupSort | flat-snapshot disk layer |

Plain state matches Reth's `PlainAccountState` and `PlainStorageState`
semantically. The DupSort + AutoDupSortKeysConversion on `Storage`
splits the logical 52-byte key into a 20-byte physical key plus a
32+N byte value, giving better page packing for dense storage.

### 3.5 附加数据 — Auxiliary (changesets + history bitmaps)

#### 3.5.1 Changesets

Per-block diffs of state. Used for unwind, history queries, snap-diff
verification.

| Table | Tier | Key | Value | Format | Notes |
|-------|------|-----|-------|--------|-------|
| `acctcs` | freezer | item = block_num | account changeset blob | unified codec: `[count:2LE]` + per-entry `[addr][oldLen][old][newLen][new]` (`changeset_codec.go`) | Both old and new values inline so forward replay AND backward unwind work without EVM |
| `storcs` | freezer | item = block_num | storage changeset blob | `[addrCount:2LE]` + per-addr `[addr][slotCount:2LE]` + per-slot `[slot:32][oldLen][old][newLen][new]` | 65535-slot chunking on big SELFDESTRUCTs |
| `AccountChangeSet` | MDBX | block_num(8B BE) | addr + V2 account encoding | DupSort | legacy Erigon-style |
| `StorageChangeSet` | MDBX | block_num + address + incarnation | plain_storage_key + value | DupSort | legacy Erigon-style |
| `leaves` (legacy) | freezer | item = block_num | per-block leaf changes | plain-key journal | being deprecated in favor of unified `acctcs/storcs` |

In Reth/Erigon, `AccountChangeSets` and `StorageChangeSets` are MDBX
DupSort tables. N42 keeps the MDBX versions for compatibility and
moves the per-block bulk into freezer for compression. The freezer
versions (`acctcs/storcs`) are the **authoritative** source for
forward replay and unwind in the EthEL pipeline.

#### 3.5.2 History bitmaps (RecSplit shards)

Inverted indices: "which blocks modified this account/slot?"

| Table | Tier | Key | Value | Codec |
|-------|------|-----|-------|-------|
| `AccountsHistory` | MDBX | address(20B) + shard_id_u64(8B) | roaring bitmap (block numbers in this shard) | roaring serialized |
| `StorageHistory` | MDBX | address + storage_key(32B) + shard_id_u64 | roaring bitmap | roaring serialized |
| `accthist` (compact) | freezer | item = shard | (addr, bitmap) RecSplit + delta-varint shard | zstd | `cmd/ethexec history-build` |
| `storhist` (compact) | freezer | item = shard | (addr, slot, bitmap) RecSplit | zstd | `cmd/ethexec storhist-build` |

Sharded by writing fresh shard when bitmap exceeds a size limit
(default 64 KB serialized). The biggest block-number in the bitmap is
used as the shard's `shard_id`. `eth_getStorageAt(historicalBlock)`
walks shards by ascending shard_id until it hits a shard whose biggest
value is `≥ targetBlock`.

### 3.6 约束证明 — MPT proof tables (in MDBX)

The hashed-state copy + intermediate trie nodes that let
`eth_getProof` answer Merkle proofs without re-execution, and let
state root verification use the standard Ethereum 32-byte hash.

| Table | Key | Value | Flags | Notes |
|-------|-----|-------|-------|-------|
| `HashedAccounts` | keccak256(addr)(32B) | account V2 encoding | — | `CalcTrieRoot` (Erigon 2.7 trie) input |
| `HashedStorage` | keccak256(addr)(32B) + incarnation(8B) + keccak256(slot)(32B) → value | DupSort + AutoDupSort (40+32) | DupSort | Physical key=addr_hash+incarnation, value=slot_hash+value |
| `TrieOfAccounts` | nibble prefix (variable) | `MarshalTrieNode(hasState, hasTree, hasHash, hashes)` | — | account trie intermediate nodes |
| `TrieOfStorage` | accountHash(32B) + incarnation(8B) + nibble prefix | trie node encoding | DupSort | per-account storage trie |
| `MPTBranch` | nibble prefix (variable) | `[afterMap:2B][cell encodings...]` | — | HexPatriciaHashed branch nodes (alternative trie impl) |
| `MPTRoot` | constant `"root"` | state root hash(32B) | — | crash recovery hint |

Two MPT impls coexist:
- **Erigon 2.7 `CalcTrieRoot`** — uses `HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage`.
- **HexPatriciaHashed (HPH)** — uses `MPTBranch` + a memory state machine.

The active root computer is selected at executor wire-up
(`internal/ethel/executor.go`). `cmd/ethexec compare-root` runs both
in parallel and verifies they agree.

### 3.7 检索数据 — Lookup indices

| Table | Tier | Key | Value | Notes |
|-------|------|-----|-------|-------|
| `HeaderNumber` | MDBX | header_hash(32B) | block_num(u64) | reverse map |
| `MaxTxNum` | MDBX | block_num(8B BE) | max_tx_num_in_block(u64) | tx-pos derivation |
| `TxLookup` | MDBX | tx_hash(32B) | tx/receipt lookup metadata (block_num + tx_idx) | RPC `eth_getTransactionByHash` |
| `txindex` (compact) | freezer | item = shard | (tx_hash, block_num, tx_idx) RecSplit shards | `cmd/ethexec txlookup-build` |
| `BlockTransactionLookup` | MDBX (alias) | tx_hash | tx lookup metadata | same as `TxLookup` |
| `LogTopicIndex` / `LogAddressIndex` | MDBX | topic-or-addr + 4B shard | bitmap(blockN) | log query optimizer |
| `CallTraceSet` | MDBX | block_num + addr | 2 bits (from / to) | call-graph index |

### 3.8 共识数据 — Consensus

#### 3.8.1 HotStuff-2 BFT (mobile-friendly variant)

| Table | Tier | Key | Value | Notes |
|-------|------|-----|-------|-------|
| `HotStuffState` | MDBX | constant `"state"` | `view(8) + consecutiveTimeouts(4) + lockedQC(var) + lastCommittedQC(var)` | crash recovery |
| `BlockVerify` | MDBX | block_num(8B BE) | per-block verification metadata | (legacy) |
| `ConsensusEvidence` | MDBX | block_num(8B BE) | QC + mobile BLS aggregate signature | moved out of `Header.Extra` to keep extraData ≤32 B (ETH standard compatibility) |

#### 3.8.2 Mobile BLS verification

`internal/evmsdk/` ships a Flutter SDK that runs an N42 light client on
mobile phones. The client uses **BLS12-381** aggregate signatures from
the validator committee. The on-disk form:

| Table | Tier | Key | Value | Notes |
|-------|------|-----|-------|-------|
| (in `ConsensusEvidence`) | MDBX | block_num(8B BE) | aggregate sig(96B) + bitmap of signing committee | per-block BLS aggregate, ~114 B fixed overhead |
| `Stake` | MDBX | validator address | stake info | committee selection input |

Each block carries a fixed-overhead 114 bytes of consensus evidence
(48B BLS pubkey + 48B BLS sig + bitmap of which committee members
signed). See `memory/project_bls_consensus_design.md` for the
threshold and committee-rotation rules.

### 3.9 数据可用性 (Data availability — EIP-4844 / PeerDAS)

Optional, only present when EIP-4844 blob txs are encountered.

| Table | Tier | Key | Value | Notes |
|-------|------|-----|-------|-------|
| `BlobSidecars` | MDBX | block_num(8B) + block_hash(32B) | proto-encoded list of `BlobSidecar` | EIP-4844 blob storage |
| `DataColumns` | MDBX | block_hash(32B) + col_index(8B) | encoded `DataColumn` | EIP-7594 PeerDAS columns |

### 3.10 同步进度 (Sync stage progress)

| Table | Tier | Key | Value | Notes |
|-------|------|-----|-------|-------|
| `SyncStage` | MDBX | stage name (`Headers` / `Bodies` / `Senders` / `Execution` / `ethel-last-block`) | block_num(8B BE) | resumable stage marker |
| `SnapSyncProgress` | MDBX | `pivot` / `account_cursor` / `state` | snap sync state | EthEL doesn't use snap sync |

`build/bin/show-progress.exe --datadir <path>` reads `ethel-last-block`
to report the highest fully-committed block.

### 3.11 Snapshot subsystem (Snap-sync diff layers)

| Table | Tier | Key | Value | Notes |
|-------|------|-----|-------|-------|
| `SnapshotAccount` | MDBX | address(20B) | proto-encoded `StateAccount` | flat snapshot disk layer |
| `SnapshotStorage` | MDBX | address + incarnation + slot (54B) | storage value | DupSort |
| `SnapshotMeta` | MDBX | `disk_root` / `gen_marker` / `gen_complete` | layered metadata | crash recovery |
| `SnapshotJournal` | MDBX | block_num(8B BE) | serialized diff layer | crash recovery |
| `SnapshotIndex` | MDBX | block_number(8B BE) | JSON `SnapshotMeta` (creation time, counts) | snapshot pruner uses |

### 3.12 Other CAS / state-tree backends (experimental)

N42 evaluated multiple state-tree backends; some are present but not
the primary state authority.

| Table | Backend | Status |
|-------|---------|--------|
| `JMTNode` / `JMTRoot` | Jellyfish Merkle Tree (Blake3) | implemented but not the active state root |
| `BMTNode` / `BMTRoot` | Binary Merkle Tree (Blake3) | implemented |
| `VerkleNode` / `VerkleRoot` | Verkle (BLS commitments) | scaffolding |
| `LtHashDigest` | running 2048-byte LtHash digest | enabled for proof-of-state |

Active state root currently uses Erigon 2.7 trie (`HashedAccounts/...`)
or HPH (`MPTBranch`) — see §3.6.

### 3.13 Mempool persistence

| Table | Tier | Key | Value | Notes |
|-------|------|-----|-------|-------|
| `TxPoolJournal` | MDBX | tx_hash(32B) | proto-encoded transaction | tx pool crash recovery |

---

## 4. Compression and segmentation specifics

### 4.1 Batch size selection

| Table | BatchSize | Rationale |
|-------|-----------|-----------|
| `senders` | 64 | small fixed-size payloads compress well grouped |
| `acctcs`, `storcs` | 64 | per-block changesets vary widely; 64 amortizes zstd dict |
| `bodies`, `receipts` | 64 | large blobs but zstd still benefits from inter-block context |
| `headers` | 64 | very small (~600 B raw); dictionary helps a lot |
| `accthist`, `storhist`, `txindex` | varies (RecSplit shard count) | each item is one shard; shards already aggregate many entries |

Default `freezer.BatchSize = 64`. Tables that opt OUT (rare) must call
`tbl.SetBatch(false)` before append.

### 4.2 Segment file size

`maxFileSize = 2 GiB` (`modules/rawdb/freezer/table.go`). Segments
rotate when adding the next batch would exceed this. The
post-compression batch sizes determine "blocks per segment":

| Table | Avg compressed batch (B) | ≈ blocks per 2 GiB segment |
|-------|--------------------------|----------------------------|
| `headers` | ~10 KB | ~12 M |
| `senders` | ~1 KB | ~120 M (typically file-size-limited only on busy chains) |
| `acctcs` | ~3 KB | ~40 M |
| `storcs` | ~5 KB | ~25 M |
| `bodies` | ~2 MB | ~64 K |
| `receipts` | ~500 KB | ~256 K |

These are rough; real values depend on chain density.

### 4.3 zstd dictionary training (optional)

`cmd/ethexec compact` and `cs-compact` retrain a per-segment dictionary
on the segment's first batch and use it for all subsequent batches in
the same segment. Improves compression by 10-30% on sparse tables.

---

## 5. Cross-references

- `docs/ethel/freezer-tables.md` — narrower freezer-only catalog with
  capability matrix
- `docs/DATA_PIPELINE_PLAN.md` — high-level pipeline architecture
- `memory/project_ethel_dev_log.md` — historical dev log of all freezer
  + codec work, includes performance measurements
- `memory/project_freezer_mdbx_atomicity.md` — durability ordering
  rules between MDBX and freezer commits
- `memory/feedback_freezer_batch_pitfalls.md` — gotchas when modifying
  freezer batch logic
- `memory/project_bls_consensus_design.md` — BLS aggregate sig +
  committee-rotation design
- `memory/reference_eth_client_storage.md` — comparison with
  Erigon/Reth/Geth storage architectures

---

## 6. Operator quick-reference

```bash
# Inspect everything in a datadir
build/bin/ethexec.exe db-stats --datadir <path> --hide-empty

# With sample-based decoded element estimates (slow; ~30s)
build/bin/ethexec.exe db-stats --datadir <path> --hide-empty --with-decoded

# Read the ethel sync progress
build/bin/show-progress.exe --datadir <path>

# Compute and verify state root at a specific block (no EVM)
build/bin/ethexec.exe verify-root --ancient <geth> --datadir <path> --block N

# Dump a single tx for debugging
build/bin/dump-tx.exe --ancient <geth> --block N --idx K
```
