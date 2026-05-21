# G1.d — Production dense MPT bootstrap runbook

**Status:** Ready to run. Estimated wall time on the dev box
(Ryzen 9 9950X / NVMe / 128 GB RAM): ~2.5 h.

## What this does

Populates `D:\n42-chaindata`'s new `AccountsDense` + `StoragesDense`
tables (declared in `internal/mpttrie/reader.go::OpenUnifiedDB`) with
the full per-child slot encoding for every MPT branch. After this
runs, `mptproof.Generator.New()` auto-detects the dense tables and
serves `eth_getProof` via the dense fast path
(`internal/mptproof/wire_full_dense.go`) instead of the legacy
compact + LeafSource rebuild path.

Expected outcome (per the design in
[`commitment-domain-plan.md`](commitment-domain-plan.md)):

| Operation | Compact path (today) | Dense path (after G1.d) |
|---|---|---|
| USDC account proof | <1 ms (already fast via RethHashedLeafSource) | <1 ms |
| USDC storage proof | 14-35 s (heavy-account overlap) | **target: ~5-20 ms** |
| Dense storage size | n/a | ~50-80 GB combined |

## Steps

### Step 1 — Account bootstrap (~25-30 min)

```powershell
D:\n42-mpt\n42-mpt-build.exe `
    --db D:\reth2k\db `
    --table PlainAccountState `
    --out D:\n42-mpt-dense `
    --tmp D:\n42-mpt-dense\etl-tmp `
    --etl-buf-mb 4096 `
    --out-mapsize-gb 256 `
    --emit-dense
```

Writes `D:\n42-mpt-dense\accounts-mptcache\mdbx.dat` containing
`AccountsTrie` + `AccountsDense` + `Meta` (state root + built_at).

### Step 2 — Storage bootstrap (~1.5 h)

```powershell
D:\n42-mpt\n42-mpt-build.exe `
    --db D:\reth2k\db `
    --table PlainStorageState `
    --out D:\n42-mpt-dense `
    --tmp D:\n42-mpt-dense\etl-tmp `
    --etl-buf-mb 4096 `
    --out-mapsize-gb 256 `
    --emit-dense
```

Writes `D:\n42-mpt-dense\storage-mptcache\mdbx.dat`.

Can run in parallel with Step 1 if disk I/O is not the bottleneck
(NVMe handles two scan-heavy passes without saturating).

### Step 3 — Migrate into chaindata (~20 min)

Extend `cmd/n42-mpt-migrate/main.go` to also copy `AccountsDense`
and `StoragesDense` (currently only knows about `AccountsTrie` +
`StoragesTrie`):

```go
migrations := []migrationPair{
    {srcDir: *srcAcct, srcTable: "AccountsTrie",    dstTable: "AccountsTrie",    metaPrefix: "accounts"},
    {srcDir: *srcStor, srcTable: "StoragesTrie",    dstTable: "StoragesTrie",    metaPrefix: "storage"},
    {srcDir: *srcAcct, srcTable: "AccountsDense",   dstTable: "AccountsDense",   metaPrefix: ""},  // no meta
    {srcDir: *srcStor, srcTable: "StoragesDense",   dstTable: "StoragesDense",   metaPrefix: ""},
}
```

Then:

```powershell
D:\n42-mpt\n42-mpt-migrate.exe `
    --src-accounts D:\n42-mpt-dense\accounts-mptcache `
    --src-storage  D:\n42-mpt-dense\storage-mptcache `
    --dst          D:\n42-chaindata
```

### Step 4 — Verify

The chaindata env now has 5 populated tables:
`AccountsTrie`, `StoragesTrie`, `AccountsDense`, `StoragesDense`,
`Meta`. Verify:

```powershell
# Run USDC integration test against production data
go test -count=1 -timeout 5m -v `
    -run TestFullProofBytes_Production_USDC_RethHashed `
    ./internal/mptproof/
```

Expected: account proof <1 ms (unchanged), storage proof now <100 ms
(was 14-35 s). The test will auto-detect dense via `Generator.New()`
and dispatch through `fullProofBytesDense`.

## Direct write alternative (skip Step 3)

If you don't want to run migrate again, write directly to
`D:\n42-chaindata`'s sibling envs (skipping the temp dirs). The
current `MDBXTarget` writes to the path passed in. Caveat: this
modifies the existing chaindata env — back it up first.

## Storage cost

| Table | Estimated size |
|---|---|
| AccountsDense | ~12 GB (compact 8.9 GB + ~50% for inline slot data) |
| StoragesDense | ~45 GB (compact 30 GB + ~50%) |
| **Total added to chaindata** | **~57 GB** |

Combined with existing 36 GB compact: **~93 GB total chaindata** —
down from yesterday's 246 GB (when we had the misguided 210 GB hashed
index). Net: G1 actually shrinks chaindata by ~150 GB while gaining
sub-second storage proofs.

Note: mdbx.dat is already 246 GB after yesterday's `ClearBucket`.
Dense writes reuse the freed pages — file size stays ~246 GB until
an offline `mdbx_copy --compact` reclaims the unused space.

## Risk register

1. **Hash mismatch on root**: If the dense bootstrap's computed root
   doesn't match the existing `Meta:accounts:state_root` (or block
   header's stateRoot at the bootstrap height), the build aborts.
   This catches structural encoding bugs.
2. **MDBX_BUSY during migrate**: ensure no other process holds the
   chaindata env. Migrate refuses on conflict.
3. **Disk space**: temp `D:\n42-mpt-dense\` peaks ~50 GB during
   ETL sort + AppendDup, then drops to ~57 GB final. With 2.2 TB
   free, no issue.
