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

## Step 4 — USDC storage proof (KNOWN LIMITATION post-2026-05-22)

Same test prints `stor slot 0 FullProofBytes` and `stor slot 1
FullProofBytes` timings. Updated expectations:

| Path | Result |
|---|---|
| Account proof (dense V1) | ✓ 9 nodes / 3779 bytes / **<1 ms** |
| Storage proof (dense V1) | ✗ ERROR: subtree cap exceeded (~200K-leaf hard limit) |

### Why storage proof fails

USDC's account-keccak shares a 7-nibble prefix with the deepest
persisted branch in the unified storage trie (`070b050805050b`).
At that depth, the subtree below contains USDC's *entire* storage
space (millions of slots from balanceOf / allowance mappings),
which our current fallback (`StorageByHashedPrefix` →
`expandSubtreeProofPath`) tries to load in full. The 200K leaf cap
in `reth_hashed.go` fires fast (under 400 ms) and the proof returns
an error rather than OOMing.

Also note: `mpttrie.Walk` sets `LeafDepth` only on
`Outcome=LandedOnLeaf`; for `NoBranchAtPath` (slot's hash points at
an under-persistence-threshold subtree) `LeafDepth` stays 0. The
dense path now derives the effective leaf depth from the deepest
hop's `PrefixDepth+1` to avoid prefix-scanning the entire 1.5B
storage table.

### Long-term fix options

1. **Per-contract storage tries** (Ethereum standard) — proofs
   would be scoped to one contract's slots, bounded by that
   contract's slot count.
2. **Dense-recursive descent** — walk dense reader entries below
   the deepest persisted branch using nibble paths, terminating
   when reaching an inline leaf or extension. Only viable if we
   persist sub-threshold branches in dense (currently we don't).
3. **Subtree streaming** — replace `expandSubtreeProofPath`'s
   load-all-leaves model with an iterator-driven trie reconstructor
   that builds the path one branch at a time.

Tracked as task #70.

## Step 5 — Archive log

```powershell
mkdir -Force docs\proof-archive
go test -count=1 -timeout 5m -v `
    -run TestFullProofBytes_Production_USDC_RethHashed `
    ./internal/mptproof/ > "docs\proof-archive\usdc_v1dense_$(Get-Date -Format yyyyMMdd_HHmmss).log" 2>&1
```

Commit the log for future regression comparison.

**Note** (post-2026-05-22): the archived log will contain two
`ERROR: ... 200000-leaf cap` lines for `stor slot 0`/`stor slot 1`.
That is the *expected* error described in Step 4 — the test still
PASSes overall (account proof validates, only storage cap-error is
recorded). A regression manifests as either (a) the test FAILing
with `unexpected storage-proof errors` (any error not containing
`200000-leaf cap`), or (b) the account proof timing/size moving off
9 nodes / 3779 bytes / <1 ms.

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
