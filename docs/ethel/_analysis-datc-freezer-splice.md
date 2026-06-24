# Analysis cache — DATC changeset freezer + splice + gap-derivation

> **Purpose**: cached engineering analysis so a future session can SKIP re-reading
> the source. Before trusting this doc, run the staleness check below; if a file's
> last-commit hash still matches, the analysis for it is current — reuse it instead
> of re-analyzing. If it changed, re-read only that file and update its section here.
> This is a project-local cache (NOT global memory).

## Staleness check (validate before reuse)

```
git log -1 --format=%h -- <file>      # compare to "analyzed@" below
```

| file | analyzed@ | lines | section |
|------|-----------|-------|---------|
| modules/rawdb/freezer/table.go            | 74e02e4c | 1246 | §1 freezer format |
| cmd/splice-leaves/main.go                 | 256f30d5 | 333  | §2 splice template |
| cmd/n42-datc/changeset_fallback.go        | a16d0506 | 510  | §3 gap derivation |
| cmd/n42-datc/emit.go                      | 0fc8feaf | 439  | §4 storage records |
| cmd/n42-datc/verify.go / proof.go         | 0fc8feaf | 936/741 | §4 |

- Analyzed by: **Claude Opus 4.8 (1M)** — 2026-06-24, repo HEAD 0fc8feaf, branch concurrent-datc-root.
- If a newer/different model re-validates, append its name; analysis logic is model-independent but flag disagreements.

---

## §1 FreezerTable format (modules/rawdb/freezer/table.go)

Append-only table = `{name}.cidx` (index) + `{name}.NNNN.cdat` (data, **2 GB max** = `maxFileSize 2_000_000_000`, rotated). `ext="c"`.

**cidx**: 16-byte header (`encodeCidxHeader`, magic `"NCIX"`, ver, **flags**, batchSize, entrySize) then `item×6`-byte entries. `idxHeaderSize`=16 for N42 files (0 for legacy geth). Each entry = `indexEntry{fileNum uint16 BE, offset uint32 BE}` (6 bytes) = byte offset where the item's data starts in its `.cdat`.

**flags** (`cidxFlag*`): `0x01 compressed` (zstd), `0x02 batchMode`, `0x08 addrIndex` (special, code freezer only). **acctcs & storcs both = 0x03 (compressed + batch), batchSize=64.**

**Batch mode (the key fact for splicing)** — `AppendBatchBlob(startItem, count, blob)`:
- writes ONE zstd blob covering `count` (≤64) items, and writes `count` cidx entries **all pointing to the same (fileNum, offset)** = the batch's start.
- batch payload (before zstd) = length-prefixed items: **`[len:4 LE][data]` repeated** (see `retrieveBatch` parser).
- batches are **atomic** (never split across files); rotation happens between batches when `headSize+len(blob) > maxFileSize`.
- `Retrieve(item)` in batch mode → `retrieveBatch`: finds the batch by scanning cidx for the contiguous run sharing one offset, reads `[offset, nextDistinctOffset)`, zstd-decodes, splits length-prefixed → returns the item's decompressed bytes.

**Open**: `NewFreezerTableReadOnly(dir,name,"c")` / `NewFreezerTableCompressed(dir,name,"c")`. On open it reads the cidx, sets `items = entries`, `headFile = last entry's fileNum`, `headSize = head .cdat size`. **A table that resumes from an existing cidx + head .cdat continues appending at `items`** — this is what makes the seeded-splice (below) work. Older .cdat files are opened lazily only on Retrieve, so a **partial dir (only segments NN..last + full cidx) opens fine and Retrieves items ≥ first-present-segment's start**.

## §2 Splice template (cmd/splice-leaves/main.go)

`rawSource` reads raw compressed batch blobs directly from cidx+cdat (bypasses decompression): `indexEntry(item)`, `readBatchBlob(b)` → raw bytes of batch b = `[entry(b*64).offset, endOffset)` in its file (endOffset = next batch's offset if same file, else file size; atomic batches). `copyBatches(src,dst,startBatch,endBatch)` → `dst.AppendBatchBlob(b*64, 64, rawBlob)` verbatim (no recompress). It builds a FULL table from batch 0 (numbers from 0000) — **the redesign below starts mid-stream instead.**

## §2.1 Redesigned directory-splice (start at gap segment, preserve NN numbering)

Goal: output dir holds only segments **NN..last** (NN = segment of the gap) + a full cidx, drop-in over the source's NN..last. Lightweight: only the gap batch is re-encoded; all later batches are raw-copied.

Because **gapLo=25101824 is batch-aligned (=392216×64) and gapHi=25101866 < 392216×64+64, the whole 43-block gap is inside ONE batch (392216).** Algorithm per table:
1. `NN = src.indexEntry(gapLo).fileNum`; `gapBatchOffset = src.indexEntry(gapLo).offset`.
2. **Seed** the dst dir: copy `src/<t>.cidx[0 : 16 + gapLo*6]` → `dst/<t>.cidx` (header + first gapLo entries, all unchanged); copy `src/<t>.00NN.cdat[0 : gapBatchOffset]` → `dst/<t>.00NN.cdat` (batches before the gap within NN). **No files < NN.**
3. Open `dst = NewFreezerTableCompressed(dst, t, "c")` → resumes at item=gapLo, headFile=NN, headSize=gapBatchOffset.
4. **Re-encode batch 392216**: 64 items 25101824..25101887. gap items (25101824..866) = derived blob (§3); non-gap (25101867..887) = `srcTable.Retrieve(i)`. Pack `[len:4LE][data]×64`, zstd-encode, `dst.AppendBatchBlob(gapLo, 64, blob)`.
5. **Raw-copy batches 392217..last** via `copyBatches` (verbatim, natural rotation → NN+1, NN+2…). Final batch may be partial (`maxItems=25311094` not ÷64): count = maxItems - lastBatchStart.
6. Verify (open dst): Retrieve the 43 gap items (non-empty, decode OK, match derived), gapLo-1 / gapHi+1 boundaries, last item; `dst.Items()==maxItems`; assert no `<t>.{0000..00(NN-1)}.cdat` present.

Caveat: re-encoding the gap batch with a fresh zstd encoder yields a different-but-valid frame for batch 392216 (content identical on decode). Later batches are byte-identical (raw copy).

## §3 Gap derivation (cmd/n42-datc/changeset_fallback.go — `loadChangesetFallback`)

Gap = primary acctcs/storcs blob with **2-byte LE count header == 0** (0-length OR count=0 stub — use `primaryCount`, NOT len). For [start,end), a block is a gap iff both primaries count==0 AND the secondary erigon MDBX (`D:/N42-hashed`, `modules.AccountChangeSet`/`StorageChangeSet`) has an entry.

Derivation per gap key: keep block-origin OLD value (from secondary changeset) and derive post-block NEW value = **OLD value at the key's NEXT change** (forward sweep from gapLo, `changeset.DecodeAccounts/DecodeStorage`, key bn==8-byte BE block), or if never changes again → secondary's current `HashedAccounts[keccak(addr)]` / `HashedStorage` (AutoDupSort DupToLen=32, SeekBothRange on 32-byte addrHash, value=slotHash32‖val — but here GetOne(64-byte keccak‖keccak) works via AutoDupSort key-fold). Result: `fbBlock{csA,csS (old), newA,newS (new)}`.

Encode to wire format: `ethel.EncodeAccountChanges(csA, getNewA)` / `ethel.EncodeStorageChanges(csS, getNewS)` → exact acctcs/storcs blob. This 43-block gap = **7000 account keys + 14536 storage keys**, derived from D:/N42-hashed in ~12 min.

Gap constant: **blocks [25101824, 25101866]** (43 blocks). Source freezer D:/N42-eth1177/chain/freezer; secondary D:/N42-hashed/chaindata (erigon, covers 25096156–25208641, aligned to mainnet ≤25,191,536).

## §4 incarnation / storage-records (emit.go, verify.go, proof.go @ 0fc8feaf)

System dropped incarnation. **External tables** (HashedStorage, TrieOfStorage) physical key = 32-byte addrHash; reads MUST use 32 bytes (HashedStorage=AutoDupSort folds long keys; TrieOfStorage={Flags:DupSort} plain, no fold → 40-byte read misses). DATC's own 40-byte "domain" (addrHash32‖8 zero) is a leaf-composite-only convention (72-byte DatcLeafS key); harmless internally, fatal when used to read TrieOfStorage. The **root node** (account or storage trie) is never read from a stored empty-path record — synthesized from depth-1 children (storage tries' keylen-32 entry is stale under reth/incremental; mirrors `StorageTrieCursor.SeekToAccount`). Both fixed @ 0fc8feaf. See [[project_datc_concurrent_root_serial_fallback]].
