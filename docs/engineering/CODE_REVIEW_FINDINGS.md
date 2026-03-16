# Code Review Findings

## 2026-03-15

### Resolved

1. `internal/tracing/tracing.go`

Issue:

- `Init` merged `sdkresource.Default()` with a resource created from `semconv/v1.37.0`.
- The OpenTelemetry SDK in `go.mod` is `v1.42.0`, and the default resource now carries a newer schema URL.
- The merge failed with `conflicting Schema URL`, which broke tracing initialization and caused `go test ./...` to fail in `internal/tracing`.

Fix:

- Replaced `sdkresource.NewWithAttributes(semconv.SchemaURL, ...)` with `sdkresource.NewSchemaless(...)`.
- This keeps service-identifying attributes while avoiding schema-version coupling to the imported semconv package path.

Verification:

- `go test -count=1 ./internal/tracing`

2. `internal/sync/*` and `internal/sync/initialsync/*`

Issue:

- Several runtime sync paths still called `CurrentBlock().Number64()` directly after the repository had started introducing nil-safe block-number helpers.
- If `CurrentBlock()` existed but `Number64()` was `nil`, code in `metrics`, `rpc_status`, `subscriber`, `fetcher`, and multiple `initialsync` peer-wait / queue paths could panic on `IsZero()`, `Cmp()`, or by passing a nil number downstream.

Fix:

- Added package-level `currentBlockNumber(...)` helpers in `internal/sync` and `internal/sync/initialsync` that return a safe zero value when the current block number is unavailable.
- Switched direct `CurrentBlock().Number64()` call sites in the affected sync paths to the helper.
- Hardened `updateMetrics()` with a nil-safe header-number guard.
- Added regression tests covering nil current-block-number handling in both `sync` and `initialsync`.

Verification:

- `go test -count=1 ./internal/sync/...`

3. `modules/rawdb/accessors_indexes.go`

Issue:

- `WriteTxLookupEntries` computed `block.Number64().Bytes()` without validating that the block number existed.
- A block object with a nil number triggered a nil-pointer panic during tx lookup index generation.

Fix:

- Reused the package's `requireBlockNumber(...)` helper before writing tx lookup entries.
- When the block number is unavailable, the function now logs and returns instead of crashing.
- Added regression tests for both the nil-number case and the normal write path.

Verification:

- `go test -count=1 ./modules/rawdb`

4. `internal/txspool/txs_pool.go`

Issue:

- `reset()` assumed `newBlock` was always non-nil and that every block involved in a reorg walk had a non-nil `Number64()`.
- In practice, interface-based callers and test doubles can provide a nil current head or ancestor block number, which made the txpool reset path panic before it could recover.

Fix:

- Added nil-safe block-number helpers for the txpool package.
- Hardened `reset()` to:
  - resolve `newBlock` from the chain only when available,
  - bail out cleanly if no current head exists,
  - skip reorg-diff walking when head or ancestor block numbers are unavailable instead of dereferencing nil.
- Added regression tests for nil `newBlock`, nil head numbers, and nil ancestor numbers.

Verification:

- `go test -count=1 ./internal/txspool`

5. `internal/snapshot/compress.go`

Issue:

- The recent refactor ignored the return error from `zstd.NewWriter` / `zstd.NewReader`.
- If construction ever failed, the singleton encoder/decoder would remain nil and later calls would crash with a nil dereference far away from the initialization site.

Fix:

- Restored fail-fast initialization.
- `getEncoder()` and `getDecoder()` now panic immediately with a clear message if construction fails or unexpectedly returns nil.

Verification:

- `go test -count=1 ./internal/snapshot`

6. `modules/state/witness/encoding_binary.go`

Issue:

- `DecodeBinaryWitness` trusted the untrusted `numAccountProofs`, `numStorageProofs`, and `numCodes` counters and allocated slices before proving the remaining input could possibly contain that many entries.
- A tiny malicious payload could therefore trigger disproportionately large allocations during decode.
- Separately, `EncodeBinaryWitness` still panicked if `w.Codes` contained a nil entry.

Fix:

- Added count-vs-remaining-bytes bounds checks before allocating proof/code slices.
- Added explicit validation for nil code entries during encode and return an error instead of panicking.
- Added regression tests for oversized account/storage/code counts and nil code entries.

Verification:

- `go test -count=1 ./modules/state/witness`

7. `modules/state/entire.go`

Issue:

- `Entire.Clone()` unconditionally dereferenced `Header` and `Snap`.
- Partially populated `Entire` values could therefore panic during clone instead of preserving nil fields.

Fix:

- Made `Clone()` nil-safe for both `Header` and `Snap`.
- Added regression coverage for cloning an `Entire` with nil optional fields.

Verification:

- `go test -count=1 ./modules/state`

8. `lib/jmt/proof.go`

Issue:

- `VerifyProof` only checked that the proof's node hashes chained together and that the recomputed root matched.
- It did not verify that the traversed internal-node nibble choices or extension-node path actually matched `proof.Key`.
- As a result, a truncated inclusion proof could be mis-accepted as an exclusion proof, and an exclusion path for one key could be replayed against a different key and still verify.

Fix:

- Added key-path validation while walking the proof:
  - internal nodes must use the nibble implied by `proof.Key`,
  - extension nodes must match the queried key path,
  - proofs may terminate at an internal/extension node only when they prove a real exclusion at that point,
  - proofs that stop early while a referenced child still exists are now rejected.
- Added regression tests for truncated proofs and for reusing one key's exclusion path as another key's proof.

Verification:

- `go test -count=1 ./lib/jmt`

9. `modules/rpc/jsonrpc/json.go`

Issue:

- `parseMessage` already rejected malformed batch items, but it still accepted extra non-whitespace bytes after the closing `]` of a batch request.
- That meant payloads like `[{...}]{"jsonrpc":"2.0"}` could be treated as a valid batch instead of a parse error.

Fix:

- Added explicit decoder error checks for the opening token, each batch element decode, and the closing token.
- Added an EOF check after the closing `]` so trailing garbage now fails parsing.
- Added a regression test covering trailing-data rejection.

Verification:

- `go test -count=1 ./modules/rpc/jsonrpc`

10. `internal/download/download.go`

Issue:

- `Downloader.ConnHandler` performed direct oneof payload type assertions based only on `SyncType`.
- A malformed or malicious sync message with a mismatched `SyncType` / payload combination could therefore panic the downloader on untrusted network input.
- The same path also assumed proto block/header numbers were always present when logging or storing received headers/bodies.
- Separately, `Downloader.Start()` dereferenced `d.network` without validating it, which made misconfigured startup paths panic instead of returning a clear error.

Fix:

- Added checked payload decoding for each `SyncType` branch and return `ErrInvalidSyncTaskPayload` instead of panicking on mismatched payloads.
- Added proto header/block number helpers and used them in the downloader's header/body processing path so malformed network payloads fail closed instead of nil-dereferencing.
- Added an early `ErrInvalidNetwork` guard in `Start()`.
- Added regression tests for mismatched payloads, malformed proto block bodies, and nil-network startup.

Verification:

- `go test -count=1 ./internal/download`
- `go test -count=1 ./internal/tracers ./internal/tracers/js ./internal/download`

11. `internal/mcp/tools.go`

Issue:

- `toolGetTransaction` scanned recent blocks with `for i := head; i > start; i--`.
- That loop excluded the lower bound of the intended scan window.
- As a result, transactions in block `0` were never found when `head <= 128`, and transactions exactly at `head - 128` were skipped when `head > 128`.

Fix:

- Changed the scan loop to include the window's lower bound while still avoiding uint64 underflow.
- Added regression tests covering both the genesis-boundary case and the general lower-bound case.

Verification:

- `go test -count=1 ./internal/mcp`

12. `internal/metrics/prometheus/prometheus.go`

Issue:

- `Handler()` called `prometheus.DefaultRegisterer.MustRegister(defaultSet)` every time a new HTTP handler was constructed.
- Creating the handler more than once against the same default Prometheus registry therefore panicked with a duplicate collector registration error.

Fix:

- Guarded `defaultSet` registration with `sync.Once` so repeated handler construction is idempotent.
- Added a regression test that constructs the handler twice against a fresh default registry and serves a request successfully.

Verification:

- `go test -count=1 ./internal/metrics/prometheus`

13. `cmd/evmsdk/common_verify.go`

Issue:

- `vertify()` only checked `bean.Entire.Header != nil`, then immediately dereferenced `bean.Entire.Header.Number` and `bean.Entire.Snap`.
- Malformed external JSON input with a missing header number or missing snapshot therefore panicked before verification could return a structured error.

Fix:

- Added explicit validation for `Header.Number` and `Entire.Snap` before any logging or state-hash computation.
- Reused the resolved block number instead of repeatedly dereferencing the nested field.
- Added regression tests for missing header number and missing snapshot input.

Verification:

- `go test -count=1 ./cmd/evmsdk`

## 2026-03-16

### Resolved

14. `internal/zkprover/service.go`

Issue:

- `Stop()` canceled the service context permanently, but `Start()` reused the same `s.ctx` on the next lifecycle.
- After a restart, both `SubmitBlock()` and background status polling therefore called the prover client with `context.Canceled`.
- In practice that silently degraded the service into local-only job tracking after a restart: remote prover submissions failed immediately, and the poller exited as soon as it started.

Fix:

- Added serialized lifecycle state for `Start()` / `Stop()` to avoid overlapping service transitions.
- Recreated the service context on restart when the previous one had already been canceled.
- Made repeated `Start()` / `Stop()` calls idempotent instead of stacking pollers on a dead context.
- Added a regression test that restarts the service and verifies the prover client receives a live context on the next submission.

Verification:

- `go test -count=1 ./internal/zkprover`
- `go test -count=1 ./internal/zkprover/... ./internal/zkverifier ./cmd/evmsdk`

15. `scripts/test_blockscout.sh`

Issue:

- The script runs with `set -e` but used `((PASS_COUNT++))` and `((FAIL_COUNT++))` to update counters.
- In bash, `((expr))` exits with status `1` when the expression evaluates to `0`.
- That meant the very first successful or failed test case could terminate the entire script immediately, so the compatibility sweep often stopped after a single RPC call.

Fix:

- Replaced post-increment arithmetic commands with assignment-style arithmetic expansion so counter updates remain zero-safe under `set -e`.

Verification:

- `bash -n scripts/test_blockscout.sh`
- `bash scripts/test_blockscout.sh http://127.0.0.1:38545` against a local mock RPC server

16. `contracts/deposit/contract.go`

Issue:

- `NewDeposit()` ignored subscription errors from `event.GlobalEvent.Subscribe(...)` and still returned a `Deposit` instance even when one or both subscriptions were unavailable.
- This is reachable after the relevant global event scopes have already been closed, because `SubscriptionScope.Track(...)` then returns `nil`.
- `Start()` and `eventLoop()` subsequently assumed both subscriptions were non-nil, which caused a panic when the listener tried to call `Err()` / `Unsubscribe()` on a nil subscription.

Fix:

- Preserved and logged subscription errors in `NewDeposit()`.
- If either subscription is unavailable, the constructor now unsubscribes any partial success and leaves the listener disabled instead of starting with a half-initialized state.
- `Start()` now degrades to a no-op with a warning when event subscriptions are unavailable.
- Added a regression test that closes the relevant global event scopes first, then creates and starts a `Deposit` listener to verify it no longer panics or blocks shutdown.

Verification:

- `go test -count=1 ./contracts/deposit`
- `go test -count=1 ./contracts/... ./internal/zkverifier ./cmd/evmsdk`

17. `common/crypto/bls/blst/public_key.go`

Issue:

- `(*PublicKey).MarshalText()` delegated straight to `p.Marshal()`, and `Marshal()` dereferenced `p.p` without checking whether the receiver had been initialized.
- A zero-value `PublicKey` therefore panicked during text or JSON serialization, because `encoding/json` uses `encoding.TextMarshaler` automatically.

Fix:

- Made `Marshal()` nil-safe and return `nil` for an uninitialized key instead of panicking.
- Made `MarshalText()` return an explicit `uninitialized public key` error when the receiver has no decoded key material.
- Added regression tests for zero-value `Marshal()`, zero-value `MarshalText()`, and zero-value `json.Marshal()`.

Verification:

- `go test -count=1 ./common/crypto/bls/blst`
- `go test -count=1 ./common/crypto/bls/... ./common/crypto/bls12381 ./contracts/...`

18. `common/crypto/stark/stark.go`

Issue:

- `AggregateSignatures()` enforces `1 <= signer count <= 1024`, but `ParseProof()`, `Verifier.Verify()`, and `Verifier.VerifyWithMessage()` did not validate `SignerCount` at all.
- That allowed hand-crafted proofs claiming `0` signers, or more than `MaxSignatures`, to bypass the structural invariant and enter verification as if they were valid aggregated proofs.
- In particular, a zero-signer proof with self-consistent zero roots and aggregate hash could be accepted.

Fix:

- Added shared signer-count validation and applied it in `ParseProof()`, `Verify()`, and `VerifyWithMessage()`.
- Proofs with `SignerCount < MinSignatures` or `SignerCount > MaxSignatures` now fail closed with `ErrInvalidProof`.
- Added regression tests for zero-signer verification and for parsing proofs with zero or oversized signer counts.

Verification:

- `go test -count=1 ./common/crypto/stark`
- `go test -count=1 ./common/crypto/...`

19. `cmd/evmsdk/common.go`

Issue:

- `(*EvmEngine).Start()` created a fresh `ctx/cancelFunc` before checking whether the engine was already running.
- A duplicate `Start()` call therefore returned `"evme is running"` but still replaced the live engine context under the running background verifier.
- Separately, when startup failed before the background task was established, `Start()` left the newly created context and cancel function attached to a stopped engine.

Fix:

- Moved the running-state guard ahead of context creation.
- On startup failure, `Start()` now cancels and clears the just-created context instead of leaving stale lifecycle state behind.
- Added regression tests covering both duplicate-start context preservation and startup-failure cleanup.

Verification:

- `go test -count=1 ./cmd/evmsdk`

20. `cmd/evmsdk/common_verify.go`

Issue:

- The background verification worker sent signed results to `ichan` using a plain blocking send.
- If the websocket write goroutine had already exited, or the engine context was canceled while no receiver was available, the worker could block forever on `ichan <- resp`.
- That stranded the goroutine and prevented its deferred engine-state cleanup from running.

Fix:

- Wrapped result delivery in a context-aware helper that aborts the send when the engine is stopping.
- Added regression tests for both successful delivery and cancellation-driven unblock behavior.

Verification:

- `go test -count=1 ./cmd/evmsdk`

21. `cmd/evmsdk/ws.go`

Issue:

- `WebSocketService.Chans()` started a write goroutine using `for msg := range chI`, but nothing ever closed `chI`.
- Its read goroutine also sent text frames into an unbuffered `chO` with a plain blocking send.
- When the engine stopped, the verification worker could exit while the websocket writer remained blocked forever waiting for the next message, or while the reader remained blocked trying to deliver a message that nobody would read anymore.
- Both cases leaked goroutines and websocket connections across stop/restart cycles.
- The same outbound request path also serialized `JSONRPCRequest` with `"error": null`, which is not part of a valid JSON-RPC request object.

Fix:

- Added explicit websocket service shutdown with `Close()` and a `done` signal so writer goroutines can exit immediately on engine stop.
- Wired `verificationTaskBg()` to close the websocket service on startup failure and on background-task exit.
- Refactored both the writer loop and read-message delivery path to honor shutdown instead of waiting forever on application channels.
- Marked the request `error` field as `omitempty` so outbound JSON-RPC requests no longer contain `error:null`.
- Added regression tests covering safe close, shutdown-driven writer/read exit, and correct wrapped request output.

Verification:

- `go test -count=1 ./cmd/evmsdk`
- `go test -count=1 ./cmd/evmsdk ./common/crypto/... ./contracts/... ./internal/zkprover/... ./internal/zkverifier`

22. `common/crypto/bls/blst/signature.go`

Issue:

- `VerifyMultipleSignatures()` batch-decompressed compressed signatures with `BatchUncompress()`, but never converted decompression failure into an error.
- When any supplied signature bytes were malformed, `BatchUncompress()` returned `nil` and the code fell through to `MultipleAggregateVerify(...)`, which then returned a plain verification failure.
- The external API therefore reported malformed input as `verify=false, err=nil`, making bad wire data indistinguishable from a legitimate cryptographic verification miss.

Fix:

- Added an explicit post-decompression check in `VerifyMultipleSignatures()`.
- Malformed compressed signatures now return a decode error instead of silently degrading to `false,nil`.
- Added a regression test covering malformed compressed signature input.

Verification:

- `go test -count=1 ./common/crypto/bls -run 'TestVerifyMultipleSignaturesRejectsMalformedSignature|TestMultipleSignatureVerification|TestMultipleSignatureFromBytes' -v`
- `go test -count=1 ./common/crypto/bls/blst`
- `go test -count=1 ./common/crypto/...`

23. `common/crypto/bls/blst/signature.go`

Issue:

- `VerifyMultipleSignatures()` still treated zero-value or uninitialized `common.PublicKey` entries as an ordinary verification miss.
- In that case the API returned `verify=false, err=nil`, which made malformed verifier inputs look the same as a genuine cryptographic mismatch.
- The single-signature verification helpers also relied on implicit nil handling in the underlying blst binding instead of checking their `common.PublicKey` inputs explicitly.

Fix:

- Added a shared public-key unwrap helper for `blst` verification entrypoints.
- `Verify()`, `AggregateVerify()`, and `FastAggregateVerify()` now fail closed immediately when given an uninitialized public key.
- `VerifyMultipleSignatures()` now returns `errUninitializedPublicKey` for invalid public-key entries instead of silently degrading to `false,nil`.
- Added regression coverage for single-signature, fast-aggregate, and batch verification with uninitialized public keys.

Verification:

- `go test -count=1 ./common/crypto/bls -run 'TestSignatureVerifyReturnsFalseOnUninitializedPublicKey|TestFastAggregateVerifyReturnsFalseOnUninitializedPublicKey|TestVerifyMultipleSignaturesRejectsUninitializedPublicKey|TestVerifyMultipleSignaturesRejectsMalformedSignature' -v`
- `go test -count=1 ./common/crypto/bls/blst ./common/crypto/bls`
- `go test -count=1 ./common/crypto/...`

24. `common/crypto/bls/blst/public_key.go`

Issue:

- The earlier zero-value hardening covered `Marshal()` / `MarshalText()`, but the rest of the `PublicKey` helper surface still dereferenced `p.p` directly.
- Zero-value or typed-nil public keys could therefore still panic in `Copy()`, `IsInfinite()`, `Equals()`, `Aggregate()`, and `AggregateMultiplePubkeys()`.
- That left multiple helper paths unsafe for caller-provided or partially initialized public-key values.

Fix:

- Hardened the remaining `PublicKey` helpers to fail closed on zero-value or uninitialized keys.
- `Copy()` now returns another zero-value key instead of panicking.
- `IsInfinite()` / `Equals()` now return `false` on invalid inputs, and aggregation helpers now return `nil` instead of dereferencing nil internal pointers.
- Added regression coverage for zero-value `Copy()`, `IsInfinite()`, `Equals()`, `Aggregate()`, and mixed-input `AggregateMultiplePubkeys()`.

Verification:

- `go test -count=1 ./common/crypto/bls/blst -run 'TestPublicKeyCopyZeroValue|TestPublicKeyIsInfiniteZeroValue|TestPublicKeyEqualsZeroValue|TestPublicKeyAggregateZeroValue|TestAggregateMultiplePubkeysRejectsUninitializedKey' -v`
- `go test -count=1 ./common/crypto/bls/blst ./common/crypto/bls`
- `go test -count=1 ./common/crypto/...`

25. `common/crypto/bls/signature_batch.go`

Issue:

- `SignatureBatch.Copy()` blindly called `Copy()` on every public key entry, and `RemoveDuplicates()` blindly called `Equals()` on each candidate pair.
- `AggregateBatch()` also assumed `AggregateMultiplePubkeys()` would always succeed.
- A batch containing nil or invalid public-key entries could therefore panic in copy, deduplication, or message aggregation helpers instead of failing predictably.

Fix:

- `Copy()` now preserves nil public-key entries instead of dereferencing them.
- `RemoveDuplicates()` now skips duplicate elimination unless both public keys are present and comparable.
- `AggregateBatch()` now returns `invalid public key in signature batch` when grouped aggregation encounters invalid public-key input.
- Added regression coverage for nil-public-key copy, deduplication, and aggregate-batch paths.

Verification:

- `go test -count=1 ./common/crypto/bls -run 'TestSignatureBatchCopyHandlesNilPublicKey|TestSignatureBatchRemoveDuplicatesHandlesNilPublicKey|TestSignatureBatchAggregateBatchRejectsInvalidPublicKey|TestVerifyMultipleSignaturesRejectsUninitializedPublicKey' -v`
- `go test -count=1 ./common/crypto/bls/blst ./common/crypto/bls`
- `go test -count=1 ./common/crypto/...`

26. `internal/metrics/prometheus/prometheus_test.go`

Issue:

- The regression test for repeated handler construction reset `registerDefaultSetOnce` through a helper that returned `sync.Once` by value.
- `go vet ./...` correctly flagged that helper as copying a lock-containing value (`sync.Once` embeds `sync.noCopy` semantics).
- That left the repository in a state where the code compiled and tests passed, but the required vet gate failed during the final regression sweep.

Fix:

- Removed the helper that returned `sync.Once` by value.
- The test now resets `registerDefaultSetOnce` directly with a zero-value literal in place.
- This preserves the test's intent while eliminating the lock-value copy that tripped `go vet`.

Verification:

- `go test -count=1 ./internal/metrics/prometheus`
- `go vet ./...`

### Regression Baseline

- Targeted verification completed successfully for:
  - `./internal/tracing`
  - `./internal/sync/...`
  - `./modules/rawdb`
  - `./internal/txspool`
  - `./internal/snapshot`
  - `./modules/state`
  - `./modules/state/witness`
  - `./lib/jmt`
  - `./modules/rpc/jsonrpc`
  - `./internal/download`
  - `./internal/tracers`
  - `./internal/tracers/js`
  - `./internal/mcp`
  - `./internal/metrics/prometheus`
  - `./internal/zkprover`
  - `./internal/zkverifier`
  - `./contracts/deposit`
  - `./contracts/pqregistry`
  - `./common/crypto/bls`
  - `./common/crypto/bls/blst`
  - `./common/crypto/bls12381`
  - `./common/crypto/stark`
  - `./common/crypto/...`
  - `./cmd/evmsdk`
- Broad verification completed successfully for:
  - `go vet ./...`
  - `go test ./...` across the repository package set
  - `go test -count=1 ./tests` in `210.529s`
  - `make build`
- Script verification completed successfully for:
  - `scripts/test_blockscout.sh` syntax
  - `scripts/test_blockscout.sh` full mock-RPC sweep
- Latest explicit exit status observed: `rc=0`
