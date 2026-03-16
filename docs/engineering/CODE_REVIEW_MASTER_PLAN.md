# Code Review Master Plan

## Goal

Establish a repeatable, repository-wide code review workflow for `N42-gov5`, generate a complete file inventory ordered by functional module, review the codebase in risk-based phases, fix confirmed issues, and verify each change with targeted tests.

## Scope

- Included: tracked files and current untracked project files.
- Excluded: runtime chain data and local build artifacts under `devtest/`, `mainnet/`, `n42data/`, `build/`, `bin/`, `coverage`, `.codex-cache/`, `.claude/`.
- Working rule: the repository currently has unrelated user changes in the worktree; this review must not revert or overwrite them.

## Baseline Snapshot

- Snapshot date: `2026-03-16`
- Raw workspace files after pruning `.git`, local caches, runtime data, and build outputs: `11390`
- Formal review inventory source: `git ls-files -co --exclude-standard`
- Formal review inventory after exclusions: `2543` files
- Go source files: `1887`
- Go test files: `429`
- Note: `tests/` has two very different counts depending on the lens:
  - filesystem count via `find tests -type f`: `8844`
  - formal review inventory via `git ls-files -co --exclude-standard | rg '^tests/'`: `27`

Top-level file distribution in the formal review inventory:

- `lib`: 826
- `internal`: 788
- `common`: 411
- `modules`: 142
- `accounts`: 62
- `docs`: 55
- `api`: 51
- `cmd`: 38
- `conf`: 32
- `tests`: 27

## Deliverables

1. `docs/engineering/CODE_REVIEW_FILE_INVENTORY.md`
2. `docs/engineering/CODE_REVIEW_MASTER_PLAN.md`
3. Code fixes for confirmed issues found during review
4. Focused tests for each fix
5. Final review summary with resolved issues, remaining risks, and verification status

## Functional Module Order

The review will follow execution risk first, then storage integrity, then networking and peripheral systems:

1. Inventory and execution baseline
2. Core execution path
3. State, database, and persistence path
4. Network, node, and RPC path
5. ZK, crypto, contracts, and tooling path
6. Cross-module regression verification

Primary package order inside those phases:

1. `cmd/`, `conf/`, `api/`
2. `internal/blockchain*`, `internal/block_validator.go`, `internal/forkchoice.go`, `internal/evm.go`, `internal/state_processor.go`
3. `internal/api/`, `internal/consensus/`, `internal/miner/`, `internal/sync/`, `internal/download/`
4. `modules/rawdb/`, `modules/state/`, `modules/ethdb/`, `internal/snapshot/`, `internal/txspool/`, `lib/jmt/`
5. `internal/p2p/`, `internal/network/`, `internal/node/`, `modules/rpc/`, `internal/tracers/`, `internal/metrics/`
6. `internal/zkprover/`, `internal/zkverifier/`, `common/crypto/`, `contracts/`, `cmd/evmsdk/`, `tools/`, `scripts/`

## Review Checklist

Each reviewed package should be checked for:

- compile errors and broken package boundaries
- nil dereferences, unchecked type assertions, and unsafe casts
- integer overflow, signed/unsigned conversion, and block-number boundary bugs
- concurrency hazards, lock ordering, goroutine leaks, and event subscription cleanup
- consensus and state transition invariants
- serialization, SSZ/RLP/JSON compatibility, and wire-format stability
- DB read/write symmetry, snapshot consistency, and rollback safety
- RPC/API error semantics and backward compatibility
- misconfigured defaults, path handling, and operator-facing failure modes
- missing or weak tests around new behavior

## Execution Phases

### Phase 0: Inventory and Baseline

- Generate the inventory document with `scripts/generate_code_review_inventory.sh`.
- Record the module/file counts and review order.
- Capture the current dirty worktree so later fixes can be attributed cleanly.

### Phase 1: Core Execution Path

Review and fix issues in:

- `internal/blockchain*`
- `internal/block_validator*`
- `internal/forkchoice*`
- `internal/evm*`
- `internal/state_processor.go`
- `internal/api/`
- `internal/consensus/`
- `internal/miner/`
- `internal/sync/`

Verification:

- targeted `go test` for touched packages
- expand to dependent packages if interface changes occur

### Phase 2: State and Persistence Path

Review and fix issues in:

- `modules/rawdb/`
- `modules/state/`
- `modules/ethdb/`
- `internal/snapshot/`
- `internal/txspool/`
- `lib/jmt/`

Verification:

- targeted `go test` on package clusters
- add regression tests for persistence edge cases

### Phase 3: Network and Node Services

Review and fix issues in:

- `internal/p2p/`
- `internal/network/`
- `internal/node/`
- `modules/rpc/jsonrpc/`
- `internal/tracers/`
- `internal/download/`
- `internal/mcp/`
- `internal/metrics/`

Verification:

- targeted `go test` for protocol, RPC, and subscription behavior

### Phase 4: ZK, Crypto, Contracts, and Tooling

Review and fix issues in:

- `internal/zkprover/`
- `internal/zkverifier/`
- `common/crypto/`
- `contracts/`
- `cmd/evmsdk/`
- `tools/`
- `scripts/`

Verification:

- package tests where available
- build validation for executable entrypoints if required by the change

### Phase 5: Regression Sweep and Closure

- Re-run focused package tests for all touched areas.
- Run a broader build/test pass if the worktree state allows it.
- Summarize fixed issues, deferred risks, and commands executed.

## Operating Rules

- Use `apply_patch` for manual edits.
- Keep changes scoped to confirmed defects or review infrastructure.
- Prefer targeted tests after each fix instead of waiting for a large final batch.
- If a package already has unrelated user edits, integrate with them carefully and do not revert them.
- Record blockers explicitly when a full-package test cannot be run because of unrelated worktree state.

## Current Status

- Phase 4 deep review across `internal/zkprover`, `internal/zkverifier`, `cmd/evmsdk`, `scripts/`, and `common/crypto/` is complete.
- Latest phase 4 fixes landed:
  - `internal/zkprover/service.go`: restart now recreates a live prover context and serializes `Start()` / `Stop()`.
  - `scripts/test_blockscout.sh`: counter updates no longer trigger premature exit under `set -e`.
  - `contracts/deposit/contract.go`: missing event subscriptions now disable the listener cleanly instead of panicking on startup/shutdown.
  - `common/crypto/bls/blst/public_key.go`: zero-value public keys no longer panic during text/JSON serialization.
  - `common/crypto/bls/blst/signature.go`: malformed compressed signatures in batch verification now return decode errors instead of silently degrading to `verify=false, err=nil`.
  - `common/crypto/bls/blst/signature.go`: uninitialized public keys now fail closed across verification helpers, and batch verification returns an explicit error instead of `false,nil`.
  - `common/crypto/bls/blst/public_key.go`: zero-value public-key helpers now fail closed instead of panicking in `Copy()`, `IsInfinite()`, `Equals()`, `Aggregate()`, and `AggregateMultiplePubkeys()`.
  - `common/crypto/bls/signature_batch.go`: nil public keys in batch helpers now stay non-fatal, and aggregate batching returns a clear error instead of panicking.
  - `common/crypto/stark/stark.go`: parsed and verified proofs now reject impossible signer counts.
  - `cmd/evmsdk/common.go`: failed or duplicate `Start()` calls no longer replace live engine contexts.
  - `cmd/evmsdk/common_verify.go`: verification result delivery now aborts on context cancellation instead of blocking forever.
  - `cmd/evmsdk/ws.go`: websocket reader/writer goroutines now honor shutdown, and outbound JSON-RPC requests no longer include `error:null`.
  - `internal/metrics/prometheus/prometheus_test.go`: regression tests no longer trip `go vet` by copying `sync.Once` values.
- [x] Baseline inventory counts collected
- [x] Review order defined
- [x] Inventory document generated
- [x] Phase 1 core execution review completed
- [x] Phase 2 state/persistence review completed
- [x] Phase 3 network/node review completed
- [x] Phase 4 ZK/crypto/tooling review completed
- [x] Regression sweep completed
