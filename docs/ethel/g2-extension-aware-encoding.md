# G2 Plain-Key Referencing — Extension Node Limitation

**Date:** 2026-05-21
**Status:** V2 encoding shipped, V2 dispatch DISABLED in Generator
  (extension-aware fix deferred)
**Predecessor:** [`g2-plain-key-referencing.md`](g2-plain-key-referencing.md)

## Discovery

The G2 LeafMarker encoding (`MarshalTrieNodeDenseV2`) replaces 33-byte
hashed-leaf slots with a 1-byte marker, then reconstructs `keccak(leaf_RLP)`
at proof time via `mpttrie.reconstructLeafHash`.

Synthetic micro-tests pass; the 500-account integration test
(`TestProofBytes_DenseV2FastPath_VsV1`) shows the reconstruction
**diverges from V1's stored hash** for 8 of 12 LeafMarker slots at
depth 1.

## Root cause

`lib/trie/gen_struct_step.go:226-267` emits **extension nodes** between a
branch's slot and a deeper leaf when the keccak space is sparse
(common in synthetic data, present-but-rarer in production):

```go
if buildExtensions {
    if remainderLen > 0 {
        ...
        if proving || retain(curr[:maxLen]) {
            e.extension(curr[remainderStart:remainderStart+remainderLen])
        } else {
            e.extensionHash(curr[remainderStart:remainderStart+remainderLen])
        }
    }
}
```

When this fires, the branch slot's stored hash is
`keccak(extension_RLP)` — `extension_RLP = [hp_encode(shared_prefix), leaf_RLP_or_hash]` —
NOT `keccak(leaf_RLP)`.

`reconstructLeafHash` only computes `keccak(leaf_RLP)`. Mismatch.

The MPT structure at such a slot is:

```
branch[c] ──> extension(shared_path) ──> leaf(remainder, value)
```

The branch's slot contains `keccak(extension_RLP)`. The extension's
"value" is the leaf's hash (or inline RLP for small leaves). Either
way, the slot's hash incorporates the extension's HP-encoded shared
prefix, which we don't reconstruct.

## Implications

V2 LeafMarker is **unsafe** for slots that hide an extension. The
build cannot distinguish "direct leaf" from "extension+leaf" by
inspecting only the slot data (both have `0xa0` prefix on the
hashStack — both push 33-byte hash entries).

## Fix paths

### Option A: skip compression when extension fires (recommended)

Track per-emission whether `extension()` or `extensionHash()` was the
last call before `branch()` pushes the slot. If so, the slot is an
extension; skip LeafMarker for that slot, store 33-byte hash.

Implementation: add a flag to `HashBuilder` set by extension /
extensionHash, cleared by branch. Read from `snapshotDenseSlots`
companion (per-slot extension bit).

Per-slot encoding overhead: +1 bit per slot. Storage cost: minimal.

Savings preserved: maybe 60-80% of V2's full theoretical (depends
on extension density). For uniform keccak, extensions are rare at
deep levels and common at shallow — overall ~70% savings achievable.

### Option B: encode extension's shared prefix in the marker

Two-byte marker: `[0x01, extension_len]`. For pure leaf (no
extension): `extension_len=0`. For ext+leaf: actual length.

Reconstruction: read shared_prefix from base by walking past the
prefix nibbles. Compute extension_RLP wrapping the leaf RLP.

Compression vs V1: 33B → 2-3B per leaf-pointing slot. ~94% savings.

Complexity: higher. Need to know where extension ends and leaf begins.

### Option C: ditch G2 V2, stay on V1

Accept 8.45 GB accounts dense (from real-data measurement). Total
chaindata after storage V1 bootstrap: ~58 GB. Still 70% smaller than
reth's combined AccountsTrie+HashedAccounts+StoragesTrie+HashedStorages
(196 GB). Sub-ms proofs work via fullProofBytesDense.

This is what's deployed today (commit 9241f8c9 + the V2 disable).

## Update 2026-05-21 evening — Option A partial implementation

Implemented per-slot extension origin tracking:

  lib/trie/hashbuilder.go
    + originStack []byte parallel to hashStack
    + leafHash / accountLeafHash / hash / code / emptyRoot → pushOrigin(originLeaf)
    + branchHash → popOrigins(N) + pushOrigin(originBranch)
    + extensionHash → overwrite top origin to originExtension
    + snapshotDenseSlots → also computes denseExtMask
    + LastExtMask() accessor

  MarshalTrieNodeDenseV2 → 3rd arg extMask uint16; LeafMarker only when
    state=1 AND tree=0 AND ext=0 AND hashed
  DenseBranchSink callback signature extended with extMask

Verified:
  - extensionHash fires (5 times in 500-account synthetic test)
  - originStack push/pop/overwrite happen in correct sequence at each
    opcode

NOT verified:
  - snapshotDenseSlots's captured extMask was always 0 for the failing
    cases at branch [0,0]. Either:
      (a) extension fires AFTER the relevant snapshot (lifetime/order
          mismatch between gen_struct_step's buildExtensions block and
          the snapshotDenseSlots call sites)
      (b) the extension'd entries are at stack positions OUTSIDE the
          top-N range that snapshot reads
      (c) snapshot captures a DIFFERENT branch's children than the one
          that will ultimately be the parent of the extension'd entry

Concretely: the V1 vs V2 cross-check showed digit 0 of branch [0]
stored a 33-byte hash that mismatches keccak(leafRLP) of the first
keccak-matching account under prefix [0,0]. AccountLeafByPrefix
returns 3 matches for that prefix — telling us there ARE 3 accounts
that share the prefix, which means the slot's child is actually a
deeper structure (extension+branch wrapping 3 leaves), not a direct
leaf.

But our origin tracking didn't catch the extension. Either the
extension was emitted for a DIFFERENT slot, or the lifetime/order is
off.

Next debug steps for whoever picks this up:
  1. instrument the OUTER loop iteration count in gen_struct_step,
     log per-iteration: maxLen, buildExtensions, what e.X opcode
     fires, and originStack content before/after
  2. specifically check: when the test failure prefix=[0,0,0] gets
     its parent's branchHash, what was the originStack just before?
  3. consider adding a second pushOrigin path inside the buildExtensions
     block to "remember" extensions that hit the just-snapshotted region

## Update 2026-05-21 late evening — root cause: extension ABSORPTION

Verified via instrumented traces:
  - extensionHash DOES fire (5 times in 500-acct test)
  - extensionHash DOES overwrite originStack[-1] to originExtension
  - subsequent snapshots for the BRANCH ABOVE these extension'd
    entries STILL show origin=branch (2), not extension (1)

Root cause: an intermediate branchHash absorbs the extension'd
child. When branch combines N children that include an extension'd
entry, popOrigins drops the extension origin and pushOrigin(branch)
appends a fresh branch entry. The new branch entry's origin says
"branch" — it has no memory that one of its children was
extension'd.

The PARENT of THIS new branch then sees origin=branch in its slot
(via topMostStack-N). The parent's V2 encoder treats this as "deeper
branch" (HasTree=1) and keeps 33 B verbatim — CORRECT.

But the V1 vs V2 cross-check shows 8 mismatches at branch [0]'s
HasTree=0 slots. So those slots have HasTree=0 (per the compact
form) AND originStack reports leaf (per my tracking). Both agree
"direct leaf". But the V1-stored hash and V2-reconstructed hash
differ.

Conclusion: my origin tracking IS faithful to what reth's compact
form claims. The problem is that reth's HasTree=0 + originLeaf
doesn't actually mean "direct leaf with no path compression" — it
can also mean "this subtree was fully absorbed into a single hash
that the slot stores", which is exactly what extension+leaf produces.
But after the absorption, NO marker survives on the stack to tell V2.

Fix paths (next session):

**Path 1**: Instead of per-slot origin tracking, capture the
HASHBUILDER's "did extension fire at this depth" as a separate
per-depth tracker. When parent's snapshot fires at depth D, check
if any extension at depth D+1+ contributed to the children slots.
Complex bookkeeping.

**Path 2**: Add a "compressed-prefix bit" to each hashStack entry
that survives branchHash absorption. When extensionHash fires, set
bit on the top entry. When branchHash combines children, OR all
their bits into the new branch entry's bit. Now ANY descendant
extension marks the slot. V2 encoder uses this to skip LeafMarker
for compressed-prefix slots.

**Path 2 implementation status (2026-05-21 late evening):**

Implemented in `lib/trie/hashbuilder.go::branchHash`:
- before pop, scan top-N originStack entries for any value==originExtension
- if found, hasExtChild=true
- after pop, push originExtension instead of originBranch

Verified via instrumentation:
- 5 extensionHash events fire in 500-acct test
- branchHash propagation OR works: 4 of the post-extension branchHash
  events produce new=originExtension (vs originBranch when none of
  children had ext)

Result on V1 vs V2 cross-check test:
- Oracle PASSES for all 5 sampled walks (✓)
- Sibling slot parity at branch [0] still shows 8 mismatching slots

Interpretation: Path 2 OR propagation works correctly within the
hashStack lifetime, but those 8 specific slots at branch [0] STILL
have origin=leaf at snapshot time (not extension). This means
either:
- The extension for these slots' content fires at a code path
  Path 2 doesn't cover (e.g. accountLeaf+extension instead of
  leafHash+extension)
- OR the test's mock SingleLeafLookup returns a different leaf
  than HashBuilder hashed for these slots (subtle distribution
  edge case)

Sampled oracle PASS means Path 2 is "good enough" for randomly-
selected walks but a deterministic regression for proofs hitting
the specific failing slots. V2 dispatch stays gated until parity
is byte-for-byte.

Path 2 is roughly 80% complete — remaining 20% requires identifying
the specific opcode/condition that fires extensions outside the
branchHash propagation chain.

## 2026-05-21 night — definitive diagnose of remaining 20%

`internal/mptproof/dense_v2_diagnose_test.go::TestDiagnose_V2_FailingSlots_HasExtension`
runs a direct probe: for each of the 8 failing slots at branch [0]
(digits 0, 2, 8, 9, 10, 11, 14, 15), count keccak matches under
that slot's nibble prefix.

Result (every failing slot has 2+ matches):

| Slot | Matches under prefix [0, X] |
|---|---|
| 0  | 3 (005a..., 00a0..., 00d6...) |
| 2  | 4 |
| 8  | 4 |
| 9  | 3 |
| 10 | 2 |
| 11 | 2 |
| 14 | 2 |
| 15 | 3 |

A HasTree=0 slot with 2+ matching leaves cannot be a single direct
leaf — its stored 33B is keccak(deeper-subtree-RLP), and V2's
LeafMarker fast path reconstructs keccak(leafRLP(first-match))
which differs.

### Why HasTree=0 can hide a real branch (not just an extension)

`lib/trie/gen_struct_step.go` registers the parent's tree bit via
two routes:

  - **line 287-288** (inside the `h()`-callback block, guarded by
    `h != nil` at line 278 AND `maxLen != 0`): unconditionally
    sets the bit when triggered.
  - **line 303-306** (inside the branch-close block): always sets
    the parent's hashMask bit, but sets the parent's treeMask bit
    ONLY when `hasTree[maxLen] != 0` (i.e. the current node has a
    nested branch of its own).

When a sub-branch's own children are all direct leaves
(`hasTree[maxLen] == 0`), the line 303-306 route does not register
a parent tree bit. The line 287-288 route may or may not fire
depending on whether `h()` is bound at that depth. The net result:
a parent slot can hold a 33B hash referencing a real branch while
its parent's treeMask claims "no deeper tree here."

Path 2's branchHash-OR pushes `originExtension` only when an
`extensionHash` was visible in the popped frames. It does NOT push
`originExtension` when the popped frames are all leaves that just
formed a real branch (branchHash without any wrapping extension),
which is exactly case (a) above. That is why 8/12 slots at branch
[0] still parity-fail.

### Fix sketch for the remaining 20%

Add a fourth `originStack` value (e.g. `originDeepSubtree=3`) and
push it from `branchHash` whenever popped children include 2+
leaves OR any non-leaf — i.e., whenever the resulting hash does not
correspond to a single direct leaf. V2 encoder then treats both
`originExtension` and `originDeepSubtree` as "not a leaf marker"
(store 33B verbatim). That collapses cases (a)/(b)/(c) into a
single "anything not a single direct leaf" predicate, which is the
real intent.

Defer implementation to a focused 1-2 day session — must re-run
the full V2 storage bootstrap + USDC end-to-end after.

## Current state (this commit)

**Path 3**: Give up V2 LeafMarker compression for this build path.
Accept V1's 8.45 GB accounts dense as the production size. Saves
the engineering. Storage V1 dense ~50 GB lands shortly via the
bootstrap-in-flight. Combined ~58 GB total, still 70% smaller than
reth combined.

Current state: shipping V1 (Path 3 by default). G2 V2 codec stays
in tree for future Path 2 work.

## Update 2026-05-21 late evening — 500-acct trace probe

Used `TestV2OriginTrace_TwoAccounts` (renamed via parameter, n=500)
to dump per-branch (state/tree/extMask) tuples. Findings:

| Branch | StateMask popcount | TreeMask popcount | ExtMask popcount |
|---|---|---|---|
| Root (key=) | 15 | 6 | **0** |
| key=00 | 13 | 1 | **0** |
| key=01 | 4 | 0 | 0 |
| ... | | | |
| Total extMask hits across 13 branches | | | **0** |

Statistical impossibility: 500 random accounts in 16 root buckets
should give ~31 accounts per bucket. Root has 9 buckets with
HasTree=0 (treeMask bit clear, slot is a "leaf-like" entry). Those
9 buckets can't actually be single leaves at random keccak
distribution — they must be extension+(deeper_branch). So extMask
should have ≥9 bits set at root.

But trace shows extMask=0. Origin tracking fundamentally missing
these extensions.

Two hypotheses to investigate next:

(H1) The buildExtensions block inside gen_struct_step's outer loop
fires extensionHash for a DIFFERENT subtree than the one whose
branch is currently being committed via snapshot+branchHash. The
extension'd entry on hashStack might be at a position OUTSIDE the
top-N slice that snapshotDenseSlots reads.

(H2) extensionHash is invoked but on the WRONG hashStack entry by
my interpretation. Maybe extension overwrites the entry BELOW the
top (the input child) rather than the top itself. Need to re-verify
which slot's hash extensionHash modifies.

To resolve, instrument extensionHash to print:
  - hashStack size before/after
  - which byte offset gets overwritten
  - the BEFORE and AFTER hash value at that position

Compare against expected: extensionHash should take the top
hashStackStride bytes (the just-pushed child), reset prefix to
0x80+32, and write the new hash there. originStack[-1] should
correspond.

Until this is debugged, the V2 dispatch must stay disabled — code
shipped Marshal/Unmarshal codec + reader + transcode tools, but
proof correctness requires extension-aware tracking that works.

## Current state (this commit)

- V2 codec (`MarshalTrieNodeDenseV2` / `UnmarshalTrieNodeDenseV2`)
  remains in `lib/trie/`. Synthetic round-trip tests pass.
- V2 transcoder (`cmd/n42-dense-measure --write`) remains, useful
  for ad-hoc compression measurement.
- Generator dispatch in `internal/mptproof/wire_full.go::FullAccountProofBytes`
  / `FullStorageProofBytes` **does NOT engage V2** even when
  `AccountsDenseV2Table` is populated. Comment in source explains.
- `TestProofBytes_DenseV2_ExtensionMismatch` skipped with reference
  back to this doc.

The 85.4% size estimate from `cmd/n42-dense-measure` measured a
HYPOTHETICAL V2 size assuming all LeafMarker conversions are valid.
In production with extensions accounted for, **actual V2 size will
be larger** — likely 25-40% smaller than V1 instead of 85%.

## Recommendation

If chaindata size matters: pursue **Option A** (extension-aware
marker). Estimated 1-2 weeks engineering.

If acceptable: stay on V1 (current production). The ~58 GB total
is competitive with major Ethereum clients and far below reth's
196 GB combined. Skip G2.
