# n42-eth Distribution — Test Plan

**Scope:** Phases E (manifest), F (client CLI), H.1 (delta build),
H.2 (delta apply), H.3 (segmented snapshot), H.4 (publication).

**Date:** 2026-05-22
**Owner:** ETH-EL team

This document is the comprehensive verification matrix for the
five binaries that ship a complete distribution pipeline:

```
publisher                          client
─────────                          ──────
ethexec       — builds archive     n42-eth-snapshot  — fetches / verifies
n42-eth-manifest — writes manifest                     / applies deltas
n42-eth-delta-build — emits delta
n42-eth-publish — manages publication root
                                   ───────
                                   archive lives at /var/lib/n42
```

## Test pyramid

```
                          E2E
                         /    \
                Integration tests
               /         |          \
       Per-package    Per-package    Per-package
        unit tests     unit tests     unit tests
```

## Unit tests (per package)

### `cmd/n42-eth-manifest/manifest` — 7 tests

| Test | What it asserts |
|---|---|
| `TestSelectorFor_UnknownModeErrors` | unknown mode → error |
| `TestWalkFiles_MinimalSelectsOnlyMinimal` | minimal selector excludes bodies/witness/senders |
| `TestWalkFiles_FullExcludesSendersAndWitness` | full selector excludes senders + witness |
| `TestWithSenders_AddsSendersSection` | opt-in senders works + is idempotent |
| `TestWalkFiles_ArchiveSupersetOfFull` | archive ⊃ full strictly; witness is archive-only |
| `TestMissingSections_ReportsGaps` | gaps in datadir are reported per-section |
| `TestSelector_HandlesSegmentedSnapshot` (H.3a) | glob `accounts.*.idx` matches per-segment files |

### `cmd/n42-eth-snapshot/snapshot` — 7 tests

| Test | What it asserts |
|---|---|
| `TestDetectMode_Minimal` | bare-minimal datadir reports mode=minimal |
| `TestDetectMode_Archive` | full archive datadir reports mode=archive |
| `TestDetectMode_Partial` | incomplete datadir reports mode="" with gaps |
| `TestFetchAndVerifyRoundTrip` | end-to-end fetch via LocalSource, then Verify OK |
| `TestFetch_SkipsExisting` | second fetch reports 0 downloads, all already-ok |
| `TestVerify_DetectsCorruption` | byte flip → Verify fails (FAIL with mismatch) |
| `TestDowngrade_IdentifiesRedundant` | downgrade picks correct files for removal |

### `internal/mptproof` (RB-1..5a) — 12+ tests

Covered in companion docs; relevant here because the snapshot
storage section (accounts.* / storage.*) is consumed by these
readers.

## Integration tests (multi-binary E2E)

These tests script the actual binaries (or call their entry-point
libraries directly) to exercise the publisher→client flow.

### IT-1: publish full release + fetch + verify

```
1. Build two fake archives at heights 1000000 and 2000000 in
   t.TempDir().
2. Invoke n42-eth-manifest --mode minimal for each.
3. Invoke n42-eth-publish release for each.
4. From a fresh client datadir, n42-eth-snapshot fetch
   --source file:///<pub>/mainnet/2000000/minimal --mode minimal.
5. Client n42-eth-snapshot verify reports OK.
6. Client n42-eth-snapshot mode reports mode=minimal, height=2000000.
```

### IT-2: delta build + apply + verify

```
1. Build archives at H₀ and H₁ where H₁ = H₀ + (some modifications).
2. n42-eth-manifest for both.
3. n42-eth-delta-build --from-archive H₀ --to-archive H₁
   → delta tree with delta-manifest.
4. Publish baseline manifest at the source (so apply can install it).
5. Fresh client gets H₀ via fetch.
6. Client delta apply --source file:///<delta-tree> → SUCCESS.
7. Client verify reports OK with new manifest_id matching H₁.
8. Client mode reports the updated height.
```

### IT-3: delta refuse on wrong baseline

```
1. Build archives at H₀ and H₁.
2. Build delta H₀→H₁.
3. Client at H₂ (different baseline) tries delta apply.
4. Tool reports applicable=false with reason.
5. Client manifest is left untouched (atomicity check).
```

### IT-4: H.4 publish + prune retention

```
1. Publish 6 releases (heights 1M..6M) per mode.
2. Publish 12 deltas (1M→2M, 2M→3M, ..., 5M→6M) per mode.
3. n42-eth-publish prune --keep-releases 2 --keep-deltas 3.
4. Verify: only the latest 2 release dirs survive per mode,
   only the latest 3 delta dirs survive per mode.
5. releases.json reflects the surviving entries.
```

### IT-5: H.3 segmented snapshot reuses unchanged segments in delta

```
1. Build archives at H₀ and H₁ where the snapshot is split into
   1M-block segments. Most segments are identical between H₀ and H₁
   (same blake2b); only the tail segment changes.
2. n42-eth-delta-build between H₀ and H₁.
3. Delta carries only the changed snapshot segments + freezer tip
   files. Verify the delta size is < 1% of full archive.
```

### IT-6: corruption recovery via re-verify

```
1. Client has a complete archive.
2. Corrupt one .cdat file mid-stream.
3. n42-eth-snapshot verify reports FAIL on that file.
4. Client re-fetches just the corrupted file via fetch
   (file already exists at correct size → would skip — so verify
   first deletes it, then fetch downloads).
```

## E2E test execution

The integration tests live in `cmd/n42-eth-snapshot/snapshot/e2e_test.go`
(this PR) and `cmd/n42-eth-publish/e2e_test.go` (this PR). They
build mini archives in t.TempDir, run the library entry points,
and assert on outputs.

Run all together:

```bash
go test -count=1 -timeout 60s \
    ./cmd/n42-eth-manifest/... \
    ./cmd/n42-eth-snapshot/... \
    ./cmd/n42-eth-publish/...
```

## Test data fixtures

Per-test ephemeral via `t.TempDir()` + `touchTree` helper. No
persistent fixtures — every test seeds its own archive shape.
This keeps the tests deterministic, parallel-safe, and CI-runnable
without external state.

## Negative-path coverage

Each integration test has at least one paired negative test:

| Positive | Negative |
|---|---|
| IT-1 fetch OK | corrupt source file → blake2b mismatch on download |
| IT-2 delta apply OK | IT-3 wrong baseline → refused |
| IT-4 prune retains correct set | IT-4 dry-run leaves all files |
| IT-5 segmented delta is small | non-segmented archive: delta carries full snapshot |

## Acceptance criteria

| Phase | Pass criteria | Status |
|---|---|---|
| E | 7 unit tests + IT-1 manifest round-trip | ✓ |
| F | 7 unit tests + IT-1 fetch/verify/mode | ✓ |
| H.1 | smoke against fake archives + IT-2 delta round-trip | ✓ |
| H.2 | apply/refuse correctness + IT-3 | ✓ this PR |
| H.3a | selector glob matches segments + IT-5 small-delta | ✓ this PR |
| H.3b | snapshot writer emits per-segment files | TODO |
| H.4 | publish + prune correctness + IT-4 | ✓ this PR |

## What is NOT tested

| Out of scope this round | Why |
|---|---|
| Network failures during fetch (partial reads) | unit-level retry logic exists; full chaos testing needs a network harness |
| HTTP source against a real server | LocalSource exercises the same code path post-decode |
| Snapshot writer emitting segments (H.3b) | requires modifying `cmd/reth-snapshot-export`; tracked separately |
| Cross-platform path semantics (CIFS, NTFS junctions) | filepath.* is uniform across Go targets |
| S3-style storage backend | local file:// publisher proves the contract; S3 add-on uses same Source interface |
