# Post-Storage-V1-Bootstrap Runbook

**Date:** 2026-05-21 late evening
**Purpose:** Once storage V1 bootstrap (pid 31432) completes, follow
these steps to ship V1 dense → chaindata → USDC validation.

## When bootstrap completes

The Monitor (task `b3qvpmrxx`) will emit `=== storage V1 bootstrap
exited ===` followed by the tail of the stdout log. Look for:

```
=== n42-mpt-build complete ===
  source table       PlainStorageState
  target bucket      D:\n42-mpt-dense\storage-mptcache/StoragesTrie
  leaves             1,569,829,384  (= reth's count)
  branches           ~147,000,000  (= reth's count)
  state root         0x???????
```

Compare `state root` against `D:\n42-snapshot/manifest.json` or any
known mainnet block 25,101,867 stateRoot. If they don't match, STOP
and investigate.

## Step 1 — Migrate dense into chaindata

```powershell
D:\n42-mpt\n42-mpt-migrate.exe `
    --src-accounts D:\n42-mpt-dense\accounts-mptcache `
    --src-storage  D:\n42-mpt-dense\storage-mptcache `
    --dst          D:\n42-chaindata `
    --src-mapsize-gb 256 `
    --dst-mapsize-gb 4096
```

Expected output:
- AccountsTrie copied (~28.9 M entries, ~8.9 GB compact)
- StoragesTrie copied (~147 M entries, ~30 GB)
- AccountsDense (V1) copied (~28.9 M entries, ~8.45 GB)
- StoragesDense (V1) copied (~147 M entries, ~50 GB est.)
- AccountsDenseV2 / StoragesDenseV2 SKIPPED (empty, --emit-dense was V1)

Total runtime: ~30-45 min (single MDBX writer, bottleneck is AppendDup).

After migrate, `D:\n42-chaindata` should be ~120-150 GB on disk.

## Step 2 — Verify state root persisted

```powershell
go test -count=1 -v -run TestGenerator_UnifiedEnv_BothTriesReachable ./internal/mptproof/
```

This opens the migrated env via Generator, walks a known address, and
prints the accounts/storage roots. Verify both match the bootstrap's
recorded roots.

## Step 3 — USDC account proof (sub-ms expected, dense V1 path)

```powershell
go test -count=1 -timeout 5m -v `
    -run TestFullProofBytes_Production_USDC_RethHashed `
    ./internal/mptproof/
```

Expected:
- `acct walk` ~ms
- `acct FullProofBytes` **<1 ms** (Generator auto-detects AccountsDense V1, dispatches to fullProofBytesDense)
- oracle PASS, 35-byte USDC account value
- proof: 9 nodes / 3779 bytes (matches morning measurement)

## Step 4 — USDC storage proof (target sub-second with dense V1)

Same test prints `stor slot 0 FullProofBytes` and `stor slot 1
FullProofBytes` timings. Expectations:

| Path | Cold | Warm |
|---|---|---|
| Compact + RethHashedLeafSource (today's baseline) | 14-35 s | similar |
| Dense V1 + RethHashedLeafSource (post-migrate) | **target 50-500 ms** | similar |

The dense path collapses inline-sibling rebuilds (the heavy-account
prefix overlap cost) — sub-second is realistic. If still >1 s,
profile `collectStorageLeavesWithPrefix` callers — they should
NEVER fire when dense reader returns 33 B branch-hash slots
directly.

## Step 5 — Archive log

```powershell
mkdir -Force docs\proof-archive
go test -count=1 -timeout 5m -v `
    -run TestFullProofBytes_Production_USDC_RethHashed `
    ./internal/mptproof/ > "docs\proof-archive\usdc_v1dense_$(Get-Date -Format yyyyMMdd_HHmmss).log" 2>&1
```

Commit the log for future regression comparison.

## Step 6 — Cleanup (optional)

If `D:\n42-mpt-dense` is no longer needed (data migrated):

```powershell
Remove-Item -Recurse -Force D:\n42-mpt-dense\accounts-mptcache
Remove-Item -Recurse -Force D:\n42-mpt-dense\storage-mptcache
Remove-Item -Recurse -Force D:\n42-mpt-dense\etl-tmp-storage
# Keep bootstrap-log for audit trail.
```

Reclaim ~70 GB.

## Troubleshooting

### Migrate fails with MDBX_BUSY on D:\n42-chaindata

Another process holds the env. Check:
```powershell
Get-Process | Where-Object { $_.WorkingSet64 -gt 1GB }
```

Likely culprit: a leftover `n42` node or `mpt-proof-debug`. Stop it
or wait for it to exit.

### State root mismatch

If migrate reports a state root different from what we expected, the
bootstrap may have used stale reth data. Re-bootstrap from scratch
against a known reth snapshot height.

### USDC storage proof STILL takes >5s

Generator's dispatch may not be picking up the dense reader. Check:
```go
generator.go::New() probes AccountsDenseTable / StoragesDenseTable
via mpttrie.DenseReader.Has() which requires the table to have at
least one entry AND the first key to look like a nibble path
(defensive check). If the migrate didn't populate the table, fix the
migrate.
```

Or check directly:
```powershell
D:\n42-mpt\n42-dense-measure.exe `
    --dir D:\n42-chaindata --table StoragesDense
```

Should report ~147 M rows.
