# D:\n42-eth1\chain\freezer\ catalogue

D:\n42-eth1\ is the ETH EL output/cache root produced by `cmd/ethexec` against the geth ancient at D:\geth\geth\chaindata\ancient\chain. **Geth ancient is read-only by hard rule** — no non-geth process may mutate it (see commit `8db3e879` for the safety guard added after a wipe incident).

The freezer subdir mixes FOUR distinct on-disk formats. Same `.cidx`/`.cdat` suffix, totally different layouts — do not assume freezer-table semantics from the extension.

## Output path: ALWAYS chain/freezer/

All ethexec build/extend tools must write into `<datadir>/chain/freezer/` — sender-recovery, receipt-copy, body-compact, header-compact, cs-compact, history-build, txlookup-build, sender-recovery --erigon-db. Earlier 5 of these had a bug writing to `<datadir>/chain/` (top-level, not the freezer subdir); fixed in commit `e507832d` 2026-05-15. Re-running on a clean datadir before that fix would silently start fresh tables in the wrong location and miss existing data. The original move-everything-into-chain/freezer/ commit was `cd0b0d87`.

## Format A — standard freezer table (`modules/rawdb/freezer`)

Per-entry, individually-addressable items. `.cidx` is 6 B per item (`fileNum:2B BE + offset:4B BE`), optional 16-B `NCIX` header. `.NNNN.cdat` rotates at 2 GB. Items appended sequentially; batch mode (multiple items share a cidx offset) is auto-detected from two consecutive entries having the same offset. Per-entry zstd flag in header.

| Table | Items | Files | Size | Encoding | Source | Used by |
|-------|-------|-------|------|----------|--------|---------|
| `senders.cidx` + `senders.NNNN.cdat` | 25,101,867 (2026-05-15) | 21+ (0000..) | ~37.7 GB | batch=64 zstd, one entry per block = `20B × txCount` packed sender addresses | `ethexec sender-recovery --ancient <geth>` runs ecrecover over geth bodies | `ethexec` executor skips ecrecover in hot path |
| `receipts.cidx` + `receipts.NNNN.cdat` | growing → 25,101,867 target | 34+ | ~63 GB at 16M items | batch=64 zstd, EncodeReceiptsCompact (decodes geth snappy-RLP, re-encodes compact) | `ethexec receipt-copy --ancient <geth>` worker pool | `ethexec` executor, witness-replay |

Senders cidx is **header-less** (legacy/geth-style — first 16 bytes are first-block index entries, not NCIX magic). `tbl.Items() = idxSize / 6`. Resume is automatic: re-running picks up at `tbl.Items()` and appends. Don't pass `--start` unless you intend to truncate; auto-resume is the safe default. SenderStage `Sync()`s every 100 K blocks; caps post-crash rollback to one checkpoint window.

Receipts compresses to ~30 % of geth raw (geth 13.2 KB/receipt-block at ~331 GB / 25M; N42 ~3.94 KB/receipt-block). Receipt-copy decode+re-encode is CPU-bound on modern post-merge blocks; throughput drops from ~9K blk/s on early blocks to ~1.6K blk/s in the 16M-25M zone (rollup calldata, dense logs).

## Format B — codes (address-indexed, custom)

NOT a freezer table. Defined in `internal/ethel/codes_freezer_reader.go` + `cmd/code-import2fz`. `.cidx` = 16-B header + N × 26-B entries `(addr:20B + fileNum:2B + offset:4B)`, **sorted by address ascending**, binary searched. Bytecode is per-entry zstd. fileNum is uint16; writer rotates `.cdat` at 2 GB (≤10 files for 24M-block mainnet).

| Table | Entries | Files | Size | Source | Used by |
|-------|---------|-------|------|--------|---------|
| `codes.cidx` + `codes.NNNN.cdat` | ~2.25M contracts (58.6 MB / 26 B − 16 B hdr) | 4 (0000..0003) | ~6.2 GB | `code-import2fz` from reth `Bytecodes` table (or equivalent MDBX) | `ethexec` executor, `witness-replay` when MDBX `Code` is incomplete |

⚠ Earlier `code-import` runs may have stored `keccak(value) ≠ key` entries (fixed in commit `9187100c`). `codes.cidx` here was rebuilt cleanly after that fix, but verify with `check-account-code` before trusting.

## Format C — N42 columnar (header/body)

Defined in `internal/ethel/header_compact.go` and `body_compact.go`. `.cidx` is 8 B **per segment** (`fileNum:2B LE + reserved:2B + offset:4B LE`). Each segment covers **8,192 blocks** and stores fields columnarly (split per field), then zstd-compresses each column. `.NNNN.cdat` rotates at 2 GB.

| Table | Segments | Files | Size | Source | Used by |
|-------|----------|-------|------|--------|---------|
| `headerc.cidx` + `headerc.NNNN.cdat` | 3,065 (24,520 / 8) | 3 | ~4.6 GB | `ethexec header-compact` from geth headers | replaces geth `headers.cdat` for replay — picker in `witness_replay_source.go` prefers headerc when present |
| `bodyc.cidx` + `bodyc.NNNN.cdat` | 3,065 (24,520 / 8) | 336 (0000..0335) | 567.49 GB | `ethexec body-compact` from geth bodies | replaces geth `bodies.cdat`; tx fields stored columnarly (nonce/value/gasPrice trimmed-u256, addr column, etc.) |

Compression vs geth (measured 2026-05-15 on the 99,884-block 25.0M-25.1M tail extension):

- **bodyc**: vs_raw 27.5 %, vs_snappy 65.0 %. 99,884 blocks → 13 segments → 4.93 GB compact (geth_snappy 7.59 GB, raw_rlp 17.94 GB). 7 min 19 s single-threaded.
- **headerc**: ratio 36.5 %, saved 63.5 %. 99,884 blocks → 13 segments → 0.02 GB compact (geth 0.06 GB). 1 s.

Both build tools have **built-in geth-sentinel handling**: when the last cidx entry returns zero-length data, they log "trailing decode error — capping range" + "stopped at last good block due to trailing corruption" and exit 0. Same observable behaviour as the explicit sentinel-skip in sender-stage but emerges from existing trailing-corruption recovery code rather than a dedicated check.

## Format D — cscompact SegmentStore + RecSplit (history)

Defined in `internal/cscompact/segment_store.go` + `history_*.go`. `.cidx` is 12 B per segment (`fileNum:2B LE + flags:2B LE + datOff:4B LE + riOff:4B LE`). Each segment covers `HistSegmentSize = 1,000,000` blocks. Within a segment, RecSplit perfect-hash maps a key to a position; the data is a delta-varint block-number list per key.

| Table | Segments | Files | Size | Key | Source | Used by |
|-------|----------|-------|------|-----|--------|---------|
| `accthist.cidx` + `accthist.NNNN.cdat` | 25 (300 / 12) | 8 (0000..0006, partial 0007) | ~13.4 GB | 20-B address | `cscompact.NewAccountHistoryAccumulator` during executor run, OR `ethexec history-build --erigon-db <reth>` | RPC `eth_getBalance/code/nonce` at historical block (`internal/api/historical_state.go`) |
| `storhist.cidx` + `storhist.NNNN.cdat` | 25 (300 / 12) | 18 (0000..0017) | ~28 GB | 52-B addr+slot | `cscompact.NewStorageHistoryAccumulator`, OR same history-build CLI | RPC `eth_getStorageAt` historical |
| `txindex.cidx` + `txindex.NNNN.cdat` | 24 (288 / 12) | 9 (0000..0008) | ~13 GB | 32-B txHash → (blockNum, txIndex) | `internal/txlookup/reth_builder.go` via `ethexec txlookup-build --erigon-db <reth>` (or `--ancient` for N42 ancient) | RPC `eth_getTransactionByHash` |

Both history-build and txlookup-build need a **reth/erigon MDBX as input**, not the geth ancient — they read the structured changeset / lookup tables, not the raw blocks. `accthist`/`storhist` are 1 segment ahead of txindex (txindex 24 vs accthist/storhist 25); rerun txlookup-build to fill the gap.

## Geth source (D:\geth\geth\chaindata\ancient\chain — READ-ONLY)

After 2026-05-15 work: 25,101,867 real blocks (cidx reports 25,101,868 incl. sentinel). 1141.97 GB total.

- `headers.cidx` + 9+ files, 12.3 GB
- `bodies.cidx` + 424+ files, 785 GB
- `receipts.cidx` + 180+ files, 331 GB
- `hashes.ridx` + 3 files, 0.88 GB
- No difficulty table (post-Merge geth doesn't keep it separately).

All geth `.cdat` entries are individually snappy-compressed RLP; freezer table treats them as opaque blobs and the ethel decoder calls `snappy.Decode` + `rlp.DecodeBytes` (see `internal/ethel/rlp_decode.go`).

The 25M → 25.1M extension on 2026-05-15 came from forking geth at C:\n42\geth, lowering the chain-freezer cutoff (new `params.FreezerThreshold = 1024` decoupled from `FullImmutabilityThreshold` so pathdb StateHistory / reorg / downloader stay at 90000), then using a new `geth db migrate-to-ancient --to <BLOCK>` offline subcommand to force-migrate the ~89 K hot-zone blocks already in pebble down into ancient without needing to start the full node. ~2080 blk/s, 43 s for 90 K blocks. See [geth-fork-freezer-threshold.md](geth-fork-freezer-threshold.md).

## Geth trailing sentinel quirk

Geth's freezer cidx contains ONE extra trailing entry past the last real block — a "next-write" sentinel marking the offset where the next item would be appended. N42's `modules/rawdb/freezer/openFreezer` computes `items = idxSize / 6` and does NOT subtract this sentinel, so `inputFreezer.Frozen()` for a geth ancient returns `real_items + 1`. Reading the sentinel index returns zero-length data because `endOffset == startOffset`.

Any reader iterating `[0, inputFreezer.Frozen())` against a geth ancient MUST tolerate the last iteration returning empty data:

```go
if len(data) == 0 && blockNum+1 == endBlock { break }
```

`SenderStage` has an explicit check (commit `c0ccae7d`). `ReceiptStage` and `rebuild-state` need an audit. `BodyCompactStage` and `HeaderCompactStage` already tolerate it via their existing trailing-corruption recovery (no explicit check, but they log "trailing decode error — capping range" / "stopped at last good block due to trailing corruption" and exit 0).

## Operational notes

**Resume order on a fresh ancient extension** (e.g. after `geth db migrate-to-ancient`):

```powershell
# All five extend the matching table to (geth_ancient_real_blocks). Idempotent / resume-safe.
.\build\bin\ethexec.exe sender-recovery --ancient D:\geth\geth\chaindata\ancient\chain --datadir D:\n42-eth1 --workers 16
.\build\bin\ethexec.exe receipt-copy   --ancient D:\geth\geth\chaindata\ancient\chain --datadir D:\n42-eth1 --workers 16
.\build\bin\ethexec.exe body-compact   --ancient D:\geth\geth\chaindata\ancient\chain --datadir D:\n42-eth1
.\build\bin\ethexec.exe header-compact --ancient D:\geth\geth\chaindata\ancient\chain --datadir D:\n42-eth1
# accthist/storhist/txindex need reth/erigon MDBX, not the geth ancient.
```

Sender-recovery and receipt-copy gate on `tbl.Items()` of their target table; bodyc/headerc gate on `bodyc.cidx` / `headerc.cidx` segment count × 8192. All five tolerate the geth trailing sentinel and exit cleanly when the last entry reads empty.

Approximate throughput for the 99 K-block 25.0M → 25.1M extension at 16 workers (where applicable) on ~128 GB / 32-core / NVMe:

| Tool | Throughput | Time for 100 K blocks |
|------|-----------|------------------------|
| sender-recovery | ~1000 blk/s | ~1.5 min |
| body-compact (single thread) | ~225 blk/s | ~7 min |
| header-compact | ~100 K blk/s | ~1 s |
| receipt-copy | drops 9K → 1.5K blk/s on 25M-zone receipts | hard to extrapolate, dominates the queue (~hours for full 21M) |
