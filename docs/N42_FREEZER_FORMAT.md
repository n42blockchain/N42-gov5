# N42 Freezer File Format Specification (v1)

This document specifies the on-disk layout of the N42 freezer-style append-only
storage so external tools (Rust, Python, …) can read N42-produced files
without depending on the Go reference implementation.

The N42 freezer hosts two related but distinct file families:

1. **FreezerTable** — the main append-only block-indexed store with optional
   batch-zstd compression. Used for `headers`, `bodies`, `receipts`, `senders`,
   `acctcs`, `storcs`, `leaves`, `witness`, `hashes`, `diffs`. File suffix
   `.cidx` (index) + `.NNNN.cdat` (data) for compressed-variable tables, or
   `.ridx` + `.NNNN.rdat` for fixed-size tables.

2. **SegmentStore** — an alternative archive layout used for tables that store
   one large compressed segment per logical chunk (`senders` segment store,
   history shards `accthist` / `storhist`, transaction index `txindex`). Same
   directory, same `.cidx`/`.NNNN.cdat` extension, **different index entry
   size (12 bytes)** and a different `.cdat` framing.

Both formats coexist in the same directory; readers MUST distinguish them by
the index-entry size (6 vs 12 bytes) and the presence of the
`NCIX` magic header. Conventional table names map to one or the other:

| Table name in N42 directory | Format |
|---|---|
| `headers`, `bodies`, `receipts`, `acctcs`, `storcs`, `leaves`, `witness` | FreezerTable (cidx 6B) |
| `hashes`, `diffs` | FreezerTable fixed-size (ridx, item-size encoded in header) |
| `senders` (executor output) | FreezerTable (cidx 6B) |
| `accthist`, `storhist`, `txindex` | SegmentStore (cidx 12B) |

All multi-byte integers use **little-endian** byte order unless explicitly
called out as **big-endian**. The only big-endian integers in the format are
the legacy Geth-compatible `indexEntry` fields (file number + offset).

---

## 1. FreezerTable format

### 1.1 Directory layout

```
{table}/                # not actually in a sub-dir; flat directory
{table}.cidx
{table}.0000.cdat       # at most 2 GiB per .cdat file; auto-rotate
{table}.0001.cdat
...
```

Every table (`headers`, `bodies`, `receipts`, `senders`, `acctcs`, `storcs`,
`leaves`, `witness`) uses this exact pattern. A maximum-size constant
`maxFileSize = 2_000_000_000` triggers a new `.NNNN.cdat` file when the
current head file would exceed it.

### 1.2 `.cidx` file layout

```
+----------------+-------------+-------------+-----+-------------+
|   16-byte hdr  |  entry [0]  |  entry [1]  | ... |  entry [N]  |
| (NCIX or none) |    6 B      |    6 B      |     |    6 B      |
+----------------+-------------+-------------+-----+-------------+
```

#### 1.2.1 Optional 16-byte header

Files written by current N42 (`v1`) start with this 16-byte header.
Legacy Geth-format files (and tables whose first write was via Geth's freezer
code path) start at offset 0 with the first index entry directly. Detect by
reading the first 4 bytes:

| bytes 0..3       | format            |
|------------------|-------------------|
| `4E 43 49 58`    | N42 v1 (NCIX)     |
| anything else    | Geth-compatible   |

When the magic matches, the header is laid out as:

| Offset | Size | Field      | Notes |
|-------:|-----:|------------|-------|
| 0      | 4    | magic      | ASCII `"NCIX"` (`4E 43 49 58`) |
| 4      | 1    | version    | currently `1` |
| 5      | 1    | flags      | bitfield (see below) |
| 6      | 1    | batchSize  | `64` for batch mode, `0` for non-batch |
| 7      | 1    | entrySize  | always `6` for FreezerTable |
| 8..15  | 8    | reserved   | zero |

Flags:

| Bit    | Constant            | Meaning |
|-------:|---------------------|---------|
| `0x01` | `cidxFlagCompressed`| Each `.cdat` blob (or batch) is zstd-frame compressed |
| `0x02` | `cidxFlagBatchMode` | Index entries are grouped into `BatchSize`-item batches sharing one offset |

#### 1.2.2 Index entries

Each entry is **6 bytes, big-endian**:

| Offset | Size | Field   | Notes |
|-------:|-----:|---------|-------|
| 0      | 2    | fileNum | which `.NNNN.cdat` holds this item (BE) |
| 2      | 4    | offset  | byte offset within that `.cdat` (BE) |

The size of item `i` is **derived** from `entry[i+1].offset - entry[i].offset`
when both entries point to the same `.cdat` file. When `entry[i+1]` lives in a
different file, `entry[i]`'s blob extends to end-of-file in `entry[i].fileNum`.

Item count = `(filesize - headerSize) / 6`. `headerSize = 16` if magic present,
else `0`.

#### 1.2.3 Batch-mode indexing

When `cidxFlagBatchMode` is set (i.e. `flags & 0x02 != 0`), every `BatchSize`
consecutive cidx entries share **the same offset/fileNum**, and a single
`.cdat` blob holds all of them together. The blob is one of:

- **Compressed batch**: zstd frame with magic `28 B5 2F FD`. Decompress to get
  the raw batch bytes.
- **Raw batch**: bytes start with the first item's length prefix (no zstd
  magic). Used when zstd output ≥ raw size.

Within the raw (or decompressed) batch payload:

```
[len_0:4 LE][data_0: len_0 bytes]
[len_1:4 LE][data_1: len_1 bytes]
...
[len_{B-1}:4 LE][data_{B-1}]
```

where `B = BatchSize` (default `64`). To read item `i`:

1. Compute `batchIdx = i / B` and `inBatch = i % B`.
2. Look up `entry[i*B]`'s offset (the batch start).
3. Read the blob length from the next batch's offset (or end-of-file).
4. Decompress with zstd if it begins with the zstd frame magic.
5. Walk the length prefixes `inBatch` times to find the desired item.

The reference Go implementation caches the decoded batch keyed on
`(fileNum, offset)` so subsequent reads in the same batch are O(1).

### 1.3 `.NNNN.cdat` data files

A `.cdat` file is a flat byte stream — there is no per-file header. The cidx
fully describes its layout. Files rotate when appending the next blob would
push the current `.cdat` past `maxFileSize` (2 GiB).

Three blob shapes can occur:

- **Single uncompressed item** (cidx flag `cidxFlagCompressed=0`,
  `cidxFlagBatchMode=0`): the entire cidx-described byte range is the item's
  raw payload.
- **Single compressed item** (`cidxFlagCompressed=1`, `cidxFlagBatchMode=0`):
  the byte range is a complete zstd frame. Decompress to get the item's raw
  payload.
- **Batch blob** (`cidxFlagBatchMode=1`): see 1.2.3 above.

### 1.4 Fixed-size tables (`.ridx` + `.rdat`)

`hashes` and `diffs` use a parallel format with fixed-size items
(32 B for hashes, 32 B for difficulty). The `.ridx` file has the same 16-byte
optional header but every item is a fixed offset:

`.rdat` items are concatenated raw bytes (`item_size = entrySize`).

`hashes` table holds 32-byte canonical block hashes; `diffs` holds 32-byte
total-difficulty integers (big-endian).

These are read-mostly Geth-compatible tables. External tools that only need
post-execution data (changesets, leaves, witness) can ignore them.

---

## 2. SegmentStore format

Used for tables that group many entries into a small number of large
compressed segments (e.g. one segment per 8192 blocks for `senders` segment
store, or one segment per logical shard for `accthist`/`storhist`/`txindex`).

### 2.1 Directory layout

```
{prefix}.cidx                  # 12-byte segment-index entries
{prefix}.0000.cdat             # ≤ 2 GiB; auto-rotate
{prefix}.0001.cdat
...
```

### 2.2 `.cidx` file layout

No magic header — files start at offset 0 with segment entries.
Entry size: **12 bytes, little-endian**.

| Offset | Size | Field    | Notes |
|-------:|-----:|----------|-------|
| 0      | 2    | fileNum  | which `.NNNN.cdat` holds this segment |
| 2      | 2    | flags    | reserved, currently always 0 |
| 4      | 4    | datOff   | byte offset of the data payload within `.cdat` |
| 8      | 4    | riOff    | byte offset of the per-segment row index, OR 0 for batch frames |

Segment count = `filesize / 12`.

### 2.3 `.NNNN.cdat` layout

Two frame shapes coexist; distinguish by whether `riOff` in the cidx entry is
zero (batch frame) or non-zero (indexed frame).

#### 2.3.1 Batch frame (no per-row index)

```
[size: 4 LE][zstd-compressed bytes: size]
```

The compressed payload, after decompression, is a sequence of items in the
same `[len:4 LE][data]` layout as FreezerTable batch (§1.2.3). Used by
`senders` segment store: 8192 blocks per segment, each block's senders are
concatenated 20-byte addresses.

#### 2.3.2 Indexed frame (has per-row index)

```
[datSize: 4 LE][dat: datSize bytes][riSize: 4 LE][ri: riSize bytes]
```

`dat` is the compressed (zstd) data payload. `ri` is a row index (typically a
RecSplit MPHF + offset table) for O(1) random access into `dat` without
linear scan. Format of `ri` is a separate concern; see §3 below for tables
that use this.

---

## 3. Per-table semantics

### 3.1 `headers`, `bodies`, `receipts` (FreezerTable, batch mode optional)

Item `blockNum` → RLP-encoded geth-format `Header`, `Body`, or `Receipt[]`.
Compatible with go-ethereum freezer semantics; existing geth tooling reads
these.

### 3.2 `senders` (FreezerTable, batch mode)

Item `blockNum` → concatenation of recovered transaction senders for that
block: `addr_0 (20 B) || addr_1 (20 B) || …`. Length divides exactly by 20.
Empty bytes for an empty block.

### 3.3 `acctcs` (FreezerTable, batch mode)

Item `blockNum` → account changeset for that block. Wire format (per block):

```
[count: 4 LE]
{ for i in 0..count {
    [addr: 20 B]
    [oldValLen: 1 B]
    [oldVal: oldValLen bytes]   # empty if account did not exist pre-block
} }
```

`oldVal` is the v2-encoded `StateAccount` bytes (see
`common/account/state_account.go`). `oldValLen` is bounded ≤ 100. Empty oldVal
means the block created the account.

### 3.4 `storcs` (FreezerTable, batch mode)

Item `blockNum` → storage changeset for that block. Wire format:

```
[count: 4 LE]
{ for i in 0..count {
    [addr: 20 B]
    [slot: 32 B]
    [oldValLen: 1 B]
    [oldVal: oldValLen bytes]   # empty if slot was zero pre-block
} }
```

`oldVal` is **trimmed leading zeros** (uint256 minimal big-endian byte
representation), `oldValLen ≤ 32`.

### 3.5 `leaves` (FreezerTable, batch mode)

Item `blockNum` → grouped account+storage delta journal for that block. Wire
format (block N ≥ 1):

```
[acctCount: 4 LE]
{ for i in 0..acctCount {
    [addr: 20 B]
    [encLen: 2 LE]
    [enc: encLen bytes]    # v2 StateAccount; empty enc = deleted account
} }
[storAddrCount: 4 LE]
{ for j in 0..storAddrCount {
    [addr: 20 B]
    [slotCount: 2 LE]      # number of slot updates for this addr
    { for k in 0..slotCount {
        [slot: 32 B]
        [valLen: 1 B]
        [val: valLen bytes]    # trimmed zeros; empty = slot zeroed
    } }
} }
```

For block 0 (genesis), the format is a "self-contained" snapshot of the entire
PlainState rather than a delta — same wire layout but `acctCount` covers all
accounts and `storAddrCount` covers every address with non-zero storage.
See `internal/ethel/leaves_journal.go::EncodeGenesisJournal`.

### 3.6 `witness` (FreezerTable, batch mode)

Item `blockNum` → minimal state-access witness used by light clients. Format
TBD — currently consumer-private; opening up to external readers requires a
follow-up spec section.

### 3.7 `accthist` / `storhist` / `txindex` (SegmentStore, indexed frame)

Each segment covers a fixed shard size (1 M blocks per shard). The data
payload is a serialization of the per-key history bitmap; the row index is a
RecSplit MPHF over the keyset. RecSplit format follows the upstream
recsplit2 layout used by Erigon — see `lib/recsplit/`.

External tools that need historical lookup (`eth_getStorageAt(blockN)` style)
must open both the data and the row index, MPHF-lookup the key to get an
in-segment offset, then decode the bitmap. Spec for the bitmap encoding will
be added as §3.7.x in a future revision.

---

## 4. Compression details

### 4.1 zstd

- **Frame magic**: `28 B5 2F FD` (little-endian as it appears in the file).
- **Reference encoder**: `klauspost/compress/zstd` with
  `EncoderLevel=SpeedBestCompression` (level 22 by default for offline
  compaction; online runs use the encoder's default).
- **Dictionary**: not used in v1. A future version may add per-table
  dictionaries; readers that encounter a non-zero version byte in the cidx
  header (`buf[4] > 1`) MUST refuse to decode rather than silently interpret
  the stream.

### 4.2 Compression-skip rule (writer side)

The writer compares `len(zstd(raw)) < len(raw)` and writes the **raw** batch
when compression makes things bigger. Readers detect this by checking the
zstd frame magic at the start of the blob and falling back to raw parsing
when absent. This applies to FreezerTable batch blobs (§1.2.3) and
SegmentStore batch frames (§2.3.1).

---

## 5. Reader algorithms

### 5.1 FreezerTable random read (item `i`)

```
def read_item(table_dir, table, i):
    cidx = open(f"{table_dir}/{table}.cidx")
    magic = cidx.read(4)
    if magic == b'NCIX':
        version, flags, batch_sz, entry_sz = cidx.read(4)
        cidx.seek(16)                         # skip header
        idx_header_size = 16
    else:
        cidx.seek(0)
        flags = 0
        batch_sz = 0
        entry_sz = 6
        idx_header_size = 0

    is_batch = (flags & 0x02) != 0
    is_compr = (flags & 0x01) != 0

    if is_batch:
        # Reading item i requires the batch-start entry [i // B * B]
        # and the next batch-start entry to bound the blob.
        B = batch_sz
        batch_idx = i // B
        in_batch  = i %  B
        e0 = cidx_entry(idx_header_size + batch_idx       * B * entry_sz)
        e1 = cidx_entry(idx_header_size + (batch_idx + 1) * B * entry_sz, ok_if_eof=True)
        blob = read_cdat(table_dir, table, e0, e1)
        if blob[:4] == b'\x28\xB5\x2F\xFD':       # zstd frame magic
            payload = zstd_decompress(blob)
        else:
            payload = blob
        # Walk in_batch length-prefixed records:
        offset = 0
        for _ in range(in_batch):
            sz = read_u32_le(payload, offset)
            offset += 4 + sz
        sz = read_u32_le(payload, offset)
        return payload[offset+4 : offset+4+sz]
    else:
        e_i  = cidx_entry(idx_header_size + i       * entry_sz)
        e_i1 = cidx_entry(idx_header_size + (i + 1) * entry_sz, ok_if_eof=True)
        blob = read_cdat(table_dir, table, e_i, e_i1)
        if is_compr and blob[:4] == b'\x28\xB5\x2F\xFD':
            return zstd_decompress(blob)
        return blob
```

### 5.2 SegmentStore random read (segment `s`)

```
def read_segment(dir, prefix, s):
    cidx = open(f"{dir}/{prefix}.cidx")
    cidx.seek(s * 12)
    file_num, flags, dat_off, ri_off = unpack("<HHII", cidx.read(12))

    # Determine end of this segment's frame: peek next entry.
    cidx.seek((s+1) * 12)
    next_buf = cidx.read(12)
    if len(next_buf) == 12:
        next_file, _, next_dat, _ = unpack("<HHII", next_buf)
    else:
        next_file = file_num + 1   # last segment → reads to EOF

    cdat = open(f"{dir}/{prefix}.{file_num:04d}.cdat")
    if ri_off == 0:
        # Batch frame: [size:4 LE][zstd bytes]
        cdat.seek(dat_off)
        size = read_u32_le(cdat, 4)
        return zstd_decompress(cdat.read(size))
    else:
        # Indexed frame: [datSize][dat][riSize][ri]
        cdat.seek(dat_off)
        dat_size = read_u32_le(cdat, 4)
        dat = cdat.read(dat_size)
        cdat.seek(ri_off)
        ri_size = read_u32_le(cdat, 4)
        ri = cdat.read(ri_size)
        return zstd_decompress(dat), ri
```

---

## 6. Reference implementations

| Implementation | Language | Path |
|---|---|---|
| Reference reader+writer | Go | `modules/rawdb/freezer/` |
| SegmentStore | Go | `internal/cscompact/` |
| Diagnostic tool | Go | `cmd/cidx-inspect/`, `cmd/cdat-scan/` |
| Rust port (proposed) | Rust | TBD — see §7 |

A conformant reader can be built in any language given:

- `binary.{Big,Little}Endian` reading of fixed-size integers
- A zstd decoder (every modern language has one — `zstd` crate in Rust,
  `pyzstd` in Python, `klauspost/compress` in Go, etc.)
- File mmap or seek+read

No external dependencies beyond zstd are required for FreezerTable. The
indexed SegmentStore frames additionally need a RecSplit MPHF reader.

---

## 7. Rust port skeleton (informative)

```rust
// Cargo.toml deps:
//   zstd = "0.13"
//   memmap2 = "0.9"

use std::fs::File;
use std::io::Read;
use memmap2::Mmap;

const NCIX_MAGIC: [u8; 4] = *b"NCIX";
const CIDX_HDR_SIZE: usize = 16;
const ENTRY_SIZE: usize = 6;
const FLAG_COMPRESSED: u8 = 0x01;
const FLAG_BATCH:      u8 = 0x02;

pub struct FreezerTable {
    cidx: Mmap,
    cdats: Vec<Mmap>,         // index by fileNum
    flags: u8,
    batch_size: u8,
    idx_header_size: usize,
}

impl FreezerTable {
    pub fn open(dir: &str, name: &str) -> std::io::Result<Self> {
        let cidx_path = format!("{dir}/{name}.cidx");
        let cidx = unsafe { Mmap::map(&File::open(&cidx_path)?)? };
        let (flags, batch_size, idx_header_size) =
            if cidx.len() >= 16 && cidx[0..4] == NCIX_MAGIC {
                (cidx[5], cidx[6], CIDX_HDR_SIZE)
            } else {
                (0, 0, 0)
            };
        let mut cdats = Vec::new();
        for i in 0u16.. {
            let p = format!("{dir}/{name}.{:04}.cdat", i);
            match File::open(&p) {
                Ok(f) => cdats.push(unsafe { Mmap::map(&f)? }),
                Err(_) => break,
            }
        }
        Ok(Self { cidx, cdats, flags, batch_size, idx_header_size })
    }

    pub fn items(&self) -> u64 {
        ((self.cidx.len() - self.idx_header_size) / ENTRY_SIZE) as u64
    }

    fn read_entry(&self, idx_off: usize) -> Option<(u16, u32)> {
        if idx_off + ENTRY_SIZE > self.cidx.len() { return None; }
        let file_num = u16::from_be_bytes(self.cidx[idx_off..idx_off+2].try_into().unwrap());
        let off      = u32::from_be_bytes(self.cidx[idx_off+2..idx_off+6].try_into().unwrap());
        Some((file_num, off))
    }

    pub fn get(&self, item: u64) -> std::io::Result<Vec<u8>> {
        let is_batch = self.flags & FLAG_BATCH != 0;
        let is_compr = self.flags & FLAG_COMPRESSED != 0;
        if is_batch {
            let b = self.batch_size as u64;
            let batch_idx = item / b;
            let in_batch  = (item % b) as usize;
            let e0 = self.read_entry(self.idx_header_size + (batch_idx * b) as usize * ENTRY_SIZE)
                .ok_or(std::io::ErrorKind::InvalidData)?;
            let e1 = self.read_entry(self.idx_header_size + ((batch_idx + 1) * b) as usize * ENTRY_SIZE);
            let blob = self.slice_blob(e0, e1);
            let payload = if blob.starts_with(&[0x28, 0xB5, 0x2F, 0xFD]) {
                zstd::decode_all(blob)?
            } else { blob.to_vec() };
            // Walk length-prefixed records
            let mut off = 0;
            for _ in 0..in_batch {
                let sz = u32::from_le_bytes(payload[off..off+4].try_into().unwrap()) as usize;
                off += 4 + sz;
            }
            let sz = u32::from_le_bytes(payload[off..off+4].try_into().unwrap()) as usize;
            Ok(payload[off+4..off+4+sz].to_vec())
        } else {
            let e0 = self.read_entry(self.idx_header_size + (item as usize) * ENTRY_SIZE)
                .ok_or(std::io::ErrorKind::InvalidData)?;
            let e1 = self.read_entry(self.idx_header_size + ((item+1) as usize) * ENTRY_SIZE);
            let blob = self.slice_blob(e0, e1);
            if is_compr && blob.starts_with(&[0x28, 0xB5, 0x2F, 0xFD]) {
                Ok(zstd::decode_all(blob)?)
            } else { Ok(blob.to_vec()) }
        }
    }

    fn slice_blob(&self, e0: (u16, u32), e1: Option<(u16, u32)>) -> &[u8] {
        let cdat = &self.cdats[e0.0 as usize];
        let start = e0.1 as usize;
        let end = match e1 {
            Some((f, o)) if f == e0.0 => o as usize,
            _ => cdat.len(),
        };
        &cdat[start..end]
    }
}
```

This single file is enough to read every batch-mode FreezerTable in N42's
output (`acctcs`, `storcs`, `leaves`, `witness`, `senders`). RLP / changeset
decoding follows §3 and is left to the caller.

---

## 8. Versioning and compatibility

- Files written by this spec (v1) carry magic `NCIX` and version byte `1`.
- Geth-format files (no magic, 6-byte BE entries, no batch mode) are also
  readable and represent ungrouped variable-length items — version 0 conceptually.
- Any future version bump (≥2) MUST come with an updated spec section here
  and SHOULD remain backward-readable from v1. New flag bits SHOULD use bits
  `0x04` upward.
- SegmentStore files do not currently carry a version byte — a future version
  will likely add a 16-byte cidx header parallel to FreezerTable's `NCIX`.

---

## 9. Out-of-scope for v1

These are deliberately not specified in v1 and may be added later:

- **Witness encoding** (§3.6) — internal format may still change.
- **storhist / accthist / txindex bitmap encoding** (§3.7) — pending stable
  RecSplit/Roaring spec.
- **Dictionary-based zstd compression** — see §4.1 reserved version byte.
- **Cross-table consistency invariants** (e.g. block N appears in all output
  tables iff exec progress ≥ N) — covered by the executor's
  `alignOnResume` logic, not the file format itself.

---

## 10. Implementation notes for porters

- **Mmap is mandatory for performance**. Sequential `pread` of millions of
  cidx entries is slow; mmap lets the OS handle paging.
- **Cache the decoded batch**: when reading batch-mode tables, decoding the
  same batch repeatedly destroys throughput. The Go implementation uses an
  LRU keyed on `(fileNum, offset)`; recommend the same pattern.
- **Concurrent reads are safe**. The format is immutable once a file is
  closed by the writer; multiple readers can mmap concurrently.
- **The writer is single-threaded per table**. Coordination with the writer
  requires file-level locking (advisory) which the format itself does not
  prescribe — use OS-native conventions.

