# G2 plain-key referencing — design

**Status:** Design, codec implementation in progress
**Predecessor:** [`commitment-domain-plan.md`](commitment-domain-plan.md) §G2

## Motivation

G1 dense form stores 33 B for every hashed child slot. For
**leaf-pointing** slots (HasTree bit clear), that 33 B is keccak of
a 1-leaf node whose value is in plain state and whose path is the
slot's position-derived suffix of keccak(addr) / keccak(addr)||keccak(slot).
Both pieces are recoverable from the base LeafSource — so we can
replace the 33 B hash with a tiny marker and recompute on read.

Estimate for our archive shape (after G1.d bootstrap measures real):

| | G1 dense | G2 dense | Saving |
|---|---|---|---|
| Accounts | ~12 GB | ~5-6 GB | ~6 GB |
| Storage  | ~64 GB | ~14 GB | ~50 GB |
| **Total** | **~76 GB** | **~20 GB** | **~56 GB** |

Storage benefits more because storage trie has more leaf-level branches.

## Encoding V2

Per-slot first byte determines the slot's form (same dispatch as V1
plus one new variant):

| First byte | Meaning | Total slot length |
|---|---|---|
| `0x01` | **HASHED-LEAF marker (G2 new)** | 1 byte |
| `0xa0` | hashed branch reference (33 B 0xa0‖keccak32) | 33 bytes |
| `0xc0..0xfe` | inline RLP (size = b0 − 0xc0 + 1) | 1..32 bytes |
| `0x80..0xb7` | (defensive; not expected) | as RLP short string |

The build-time decision: a slot is **HASHED-LEAF** iff
`stateMask[i]=1 AND treeMask[i]=0 AND original_slot[0]=0xa0`. Other
hashed slots (HasTree=1) remain 33 B.

Container layout (unchanged from V1):

```
+0   state_mask  uint16 BE
+2   tree_mask   uint16 BE
+4   for each set bit in state_mask, ascending:
       first byte (above) + payload
```

V1 vs V2 are NOT magic-byte distinguishable. Tables are explicit:

- `AccountsDense`, `StoragesDense` — V1 (G1.c bootstrap output)
- `AccountsDenseV2`, `StoragesDenseV2` — V2 (G2 bootstrap output, later)

Generator detects which is populated via `Has()` probes; never both.

## Read-time reconstruction

For each `0x01` HASHED-LEAF slot at nibble path `branchPath + i`:

1. Compute byte prefix `seekKey = nibblesToByteSeek(branchPath + i)`.
2. Cursor on the base's hashed table (`HashedAccounts` or `HashedStorages`)
   starting at `seekKey`.
3. Read first matching record — should be exactly one given HasTree=0
   (parser asserts uniqueness; mismatch = corruption).
4. Reconstruct leaf RLP:
   - Accounts: `leafRLP = encodeLeafRLP(suffix, account_compact)` where
     `suffix = nibblesOf(keccak(addr))[len(branchPath)+1:] + 0x10`
   - Storage: same with composite key `keccak(addr) || keccak(slot)`
5. `hash = keccak256(leafRLP)`
6. Output slot bytes = `0xa0 ‖ hash` (33 B for proof emission).

Cost per HASHED-LEAF reconstruction: 1 MDBX cursor.Seek + 1 keccak
(~50 µs cold, ~1 µs warm). For a typical USDC proof with ~7 hops ×
~12 leaf siblings = ~80 reconstructions = ~80 µs warm, ~4 ms cold.
Still dwarfed by walk overhead — net proof time still sub-ms warm.

## Implementation surface

```
lib/trie/trie_root.go
  MarshalTrieNodeDenseV2(stateMask, treeMask, slotData, buf) []byte
    — input slotData is hashStack frame (same as V1)
    — output: V2-encoded bytes
  UnmarshalTrieNodeDenseV2(buf) (stateMask, treeMask, slots [16][]byte, err)
    — slot bytes alias buf; 0x01 markers are 1-byte alias slices

internal/mpttrie/dense_reader.go
  DenseReader.GetV2(nibblePath, base HashedKeyScanner) (DenseBranchExpanded, ok, err)
    — like Get but expands 0x01 markers via base reconstruction
    — returns DenseBranchExpanded with all slots in 33B/inline form
    — base is required when any V2 marker is present

internal/mptbuild/builder.go
  Opts.DenseV2BranchSink — alternative to DenseBranchSink that uses V2 encoding
  (or: a flag on DenseBranchSink to choose format)

internal/mptproof/wire_full_dense.go
  fullProofBytesDense unchanged — works on (StateMask, TreeMask, Slots[16][]byte)
  DenseReader.GetV2 wires the marker expansion behind the scenes
```

Read-side change is mostly transparent to proof code — the reader
hides reconstruction.

## Open question: do we bootstrap V2 now or defer

Pros of bootstrapping V2 immediately after G1.d:
- 56 GB saved
- Cleanest final state

Pros of letting G1 V1 settle first:
- G1.d validates the architecture end-to-end before introducing
  per-slot reconstruction (an additional point of failure)
- V2 bootstrap requires Generator to know which table to read —
  another config

Recommendation: ship V1 to production via G1.d, measure size,
schedule V2 bootstrap as a follow-up if the size matters.

## Risk register

1. **Reconstruction correctness**: `keccak256(leafRLP(suffix, account))`
   MUST equal the original parent-stored hash. Validated by oracle
   verification during proof emission.

2. **HasTree=0 not always single-leaf**: in pathological tries with
   extension nodes between branch and leaf, the prefix could match
   multiple leaves. The parser asserts uniqueness — failure indicates
   the trie has structure incompatible with this compression (an
   extension we'd need to materialise). Mainnet/synthetic data
   doesn't have this case; documented for future awareness.

3. **Base availability at proof time**: if the base LeafSource is
   not the keccak-keyed RethHashedLeafSource (e.g. someone passes a
   pure plain-state LeafSource), prefix scans won't work — DenseV2
   reader returns error.
