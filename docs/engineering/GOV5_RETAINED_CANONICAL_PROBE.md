# Retained canonical startup probe

The performance validator now probes its retained local canonical checkpoint,
not `(genesis, 1)`. The old request exceeds the owner's existing 256-block
ancestry window when the applied tip reaches height 257. The window and owner
ancestry/header/body/execution checks are unchanged.

Startup reads the canonical progress and actual local header in one metadata
transaction. It rejects missing progress/header, malformed encoding, zero hash,
non-uint64/mismatched height, overflow, and a header whose freshly computed hash
differs from the canonical checkpoint. Direct local header lookup prevents an
ancient-store fallback from hiding missing metadata. A startup-only decoder
converts malformed native nested protobuf fields into an error; ordinary header
decoding and normal canonical-counter reads are unchanged.

The subsequent `AppliedChild` request uses the full configured chain identity,
local canonical hash, local number plus one, and the existing five-second
probe deadline. An owner behind this checkpoint, another fork, or a checkpoint
outside the retained window still fails closed. There is no genesis fallback,
metadata repair, execution bypass, or new historical catch-up implementation.
The default full node and ETH paths do not use this performance constructor.

## Actual validation

On 2026-10-17 the fixed v4 source passed under a BOX claim with
`GOMAXPROCS=2`, `GOFLAGS=-p=1`, `nice -n 19`, and `ionice -c 3`:

```text
go test -p 1 ./internal/node ./internal/sharedvalidator -run '^Test(PerformanceValidatorRetainedCanonicalProbeAfterPhysicalReopen|CanonicalProbeHeaderRejectsMalformedLocalEncoding)$' -count=1 -timeout=120s
go test -race -p 1 ./internal/node ./internal/sharedvalidator ./internal/sharedexec -run '^Test(PerformanceValidator|OwnerQueueStartup|AppliedChild|CanonicalProgress|CanonicalProbeHeader)' -count=1 -timeout=180s
go vet -p 1 ./internal/node ./internal/sharedvalidator
```

The real MDBX fixture executes and publicly certifies 257 blocks, physically
closes both owner and metadata databases, reopens them, demonstrates the old
probe's failure, and successfully constructs the lightweight validator using
the retained checkpoint. It separately damages only the local header row
(missing, invalid encoding, incomplete nested fields, changed root, changed
number), requires constructor rejection, restores the exact original bytes, and
requires successful construction again. Owner build/import counters remain
zero during these startup checks. Owner-behind, fork, out-of-window, overflow,
zero-hash, and missing-progress cases also reject. Direct malformed-decoder
unit cases must return a nil header and error rather than panic.

Targeted package timings were node 8.876s and sharedvalidator 0.011s; race timings
were node 13.858s, sharedvalidator 1.042s, and sharedexec 1.223s. Vet exited zero
with no output. The controller completed at 03:19:30 UTC, after its normal
claim tail check. Sol independently checked all three exit codes, fixed source
and controller/runner hashes, absent owned process groups, and release of all
five claim paths.

Evidence directory:
`/data/blockchain/gov5-work/validation/20261017-gov5-retained-probe-v4`.

- `status.json`: SHA256 `54fbd3a5c932e60079dbc2ce0d2da7126c03920b9eedb2d36ba9eb15f9572331`.
- `independent-acceptance-v2.json`: SHA256 `1c3f9f0d43ff83bd6d1f85dfbbabc95c355a061df4133ccfc4a881050f99bb21`.
- Fixed controller SHA256: `4d5205cdbcf71687310168f38ac6bfda2a1edb56c9fea5fe2debd43d21ccd47f`.

The earlier v2 fixture failed because its socket parent equalled the dedicated
root instead of being strictly inside it; its failed log remains preserved.
The v3 waiting controller was explicitly stopped before any stage when the
additional local-header validation requirement was identified. Neither was
reported as a passing probe validation.

This verifies physical database reopen and the actual node constructor in
small temporary fixtures. It does not claim a new seven-process run, target
transaction liveness, a full replay proof, full Make/build validation, or TPS.
The repaired `s192r1` data, source corpus, backups, genesis, and nonce vector were
not opened or changed in this validation turn. The next bounded resume phase
still requires authenticated source-to-sender mapping, explicit plan/session
binding, transport and real execution fixtures, rebuilt binaries, and its own
hardware turn before dispatch.
