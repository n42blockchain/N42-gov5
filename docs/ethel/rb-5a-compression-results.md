# RB-5a — StoragesTrie 33B Packed Subkey

**Date:** 2026-05-22
**Status:** ✅ COMPLETE — codec + transcoder + V2 reader + e2e
test all landed and validated against full D:\reth2k state.

## Motivation

Reth's StoragesTrie persists subkeys as 65-byte fixed
`StoredNibblesSubKey` (v1 format): 64 bytes of zero-padded nibble
payload + 1 byte length. Empirical scan against D:\reth2k\db (138.6
million rows) showed an average nibble depth of **3.82 nibbles**,
meaning ~60 bytes per row are pure padding.

Reth's own v2 schema (`PackedStoredNibblesSubKey`) packs to 33 bytes
(32 packed nibbles + 1 length). Memcmp ordering on the packed buffer
matches nibble-lexicographic ordering, so MDBX DupSort can use it as
subkey directly — no custom comparator needed.

## Projection (from full 138.6M-row scan)

| Scheme | Total value bytes | avg/row | vs reth v1 |
|---|---:|---:|---:|
| reth v1 (65B subkey) | 23.72 GB | 183.7 B | baseline |
| **reth v2 (33B packed) ← RB-5a** | **19.59 GB** | **151.7 B** | **-4.13 GB (-17.4%)** |
| RB-5d variable-length | 15.74 GB | 121.9 B | -7.98 GB (-33.6%) |
| Schema change (subkey→key) | 15.33 GB | 118.7 B | -8.39 GB (-35.4%) |

Numbers above measure value-section bytes only; MDBX page/branch
overhead adds ~30% in the .dat file.

## Implementation

```
internal/mptproof/
  reth_packed_subkey.go        PackNibblesV2 / UnpackNibblesV2 +
                               tests (round-trip, ordering, malformed)
  reth_trie_reader_v2.go       RethTrieReaderV2 — drop-in alternative
                               to RethTrieReader for the V2 table
  reth_v2_e2e_test.go          USDC StorageBranchAt v1 vs v2 byte-
                               identical regression guard

cmd/
  n42-storage-trie-measure/    scan reth, project per-scheme sizes
  n42-storage-trie-transcode/  read reth v1 → write v2 to a new env
```

## Empirical results — full D:\reth2k state

| Measurement | Value |
|---|---|
| Source rows | 138,608,434 |
| Transcode wall time | **8m27s** (273 K rows/s sustained) |
| Source key+value bytes (logical) | 27.85 GB |
| Dest key+value bytes (logical) | **23.72 GB** |
| Logical saving | **4.13 GB (-14.8%)** |
| Dest mdbx.dat file size | **26 GB** |
| Reth's StoragesTrie file size | 31.4 GB |
| **Net on-disk compression** | **-5.4 GB (-17.2%)** |
| USDC v1/v2 byte-identical regression | **PASS** ✓ |

The on-disk saving (-17.2%) slightly exceeds the logical saving
(-14.8%) because MDBX page overhead scales with data volume —
less data also means fewer pages and less per-page framing.

Test paths validated:
- `[]` (empty): absent in both (consistent)
- `[0], [1], [0xa], [0xb]`: dense depth-1 branches, 16-child each
- `[0,0,0,0,2]`: absent in both (USDC slot 000020e9's sibling)
- `[0,0,0,0,a]`: 4-child branch (state=0x43cd), masks + hashes match

All USDC storage branch reads via V2 produce structurally
IDENTICAL `mpttrie.BranchNode` values to V1.

## Next steps (future RB-5 sub-tasks)

| Sub | Saving | Effort | Status |
|---|---:|---|---|
| RB-5b dict-zstd on mask header | ~0.5 GB | 1-2 days | not started |
| RB-5c AccountsTrie key 2-nib pack | ~0.1 GB | 0.5 day | not started |
| RB-5d variable-length subkey (custom DupSort cmp) | ~4 GB extra | 3-5 days | not started |
| RB-5e schema change (subkey→key, drop DupSort) | ~4.5 GB extra | 1-2 weeks | future |

Stretch combined achievable in Tier 3 ladder: ~15-17 GB on the
StoragesTrie section (-50% vs reth v1 baseline).

## Compatibility / migration

The v1 reader (`RethTrieReader.StorageBranchAt`) and v2 reader
(`RethTrieReaderV2.StorageBranchAt`) present an IDENTICAL public
contract — same return shape (mpttrie.BranchNode, bool, error). The
existing RB-1..4 proof builder (`BuildRethStorageProof`,
`enumerateUSDCStorageSubLeaves`, walker) work against either
reader via a small interface shim or direct constructor swap.

The transcode tool produces a separate env (`D:\n42-storage-trie-v2`
by default) so the source reth env stays read-only per project
policy.
