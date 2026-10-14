# Performance-chain P1: strict signatures, shared execution and restart

Status: **partial validation; restart liveness failed; hardware work paused at the user's request**. The other session needs approximately 30 minutes of hardware time. Do not restart heavy work merely because 30 minutes elapsed; wait for release/continuation.

## Scope and provenance

Independent experimental chain ID 1144, seven HotStuff BLS validator keys sharing one EL (V=7/E=1), Ed25519 0x50 transactions, strict verification, no gateway signature bypass, frame blocks disabled, deferred-execution depth 2. This is a functional check, not a TPS measurement or seven independent execution fault domains.

Rust source remained clean at `abe8891c6e76b77729c5e9522cf25503ad0c88e9`. `cargo build --locked --release -p n42 --bin n42 -p n42-h2-node --example h2_validator --example tx_flood -j 4` succeeded. h2_keygen was rebuilt separately. Binaries were copied into an isolated evidence directory and SHA256 values recorded in run-manifest.json. The fleet inherited CPU affinity 0-15 and RAYON_NUM_THREADS=8.

Evidence: `/data/blockchain/wr-analysis/performance-chain-p1-20261013/`. Chain data and vote logs are retained under `/data/blockchain/n42-performance-chain/e1/data/`; do not reset them during diagnosis. Runtime keys and JWT files must not be committed.

## Results

| Check | Result |
| --- | --- |
| Ed25519 transaction library | 21 passed, 4 timing benchmarks ignored |
| ImportOnce source-module tests | 8 passed, including seven requests/one owner, failed-owner takeover, transient-status retry and bounded retention |
| Real RPC admission | Tampered signature rejected with -32602, `invalid transaction signature`; the original valid control succeeds |
| Execution | All 32 Ed25519 transfers returned successful receipts |
| Validator agreement before restart | 256 views common to all seven validators; no conflicting view hashes |
| Shared execution before restart | 258 imports / 258 distinct hashes; 1538 requests served without import; 0 takeovers; 0 own-built blocks executed again |
| Graceful whole-fleet restart | Database and 32 old receipts restored, with original canonical block hashes; chain remained at height 258 and could not produce another block |
| Binary-ingest bad-signature check | Prepared but not reached: post-restart funding could not mine |
| Power-loss / persistence-failure recovery | Not tested |

The full n42 library test build was interrupted to avoid rebuilding unrelated test dependencies. The eight ImportOnce tests were instead compiled directly from the unchanged production module and its existing test file in an isolated Cargo harness; this does not claim the complete n42 test suite passed. Harness and output are retained as `/tmp/n42-performance-registry-tests/` and `import-once-isolated-tests.log`.

## Configuration fix

The first launch stopped before opening ports: historical fleet7 bench persistence threshold 8 violates the current Engine check `num-state-masking-blocks (30) + memory-block-buffer-target (6) < persistence-threshold`. The performance profiles now explicitly set `F7_PERSIST_THRESHOLD=64`. All three generated profiles were updated, and regeneration was verified. Source defaults for other fleets were not changed.

## Restart defect and next work

`bin/n42/src/main.rs` startup around lines 296-320 restores execution fields only for the head. At depth 2, the first child requires the execution result of head-1. `crates/n42/engine-types/src/hotstuff_consensus.rs::ancestor_executed_fields` therefore returns no fields and payload building reports that the ancestor execution result is not known. Repeated view timeouts follow; changing validator pacing cannot repair the missing result.

QMDB restore reconstructs only its persisted head tree (`QmdbForest::from_snapshot` / file-mode checkpoint restore). A deferred header carries an older ancestor result, so the missing frontier root cannot safely be taken from the head header or replaced by the head's root. Persist and restore the required execution frontier, bound it to canonical hashes and chain identity, and verify durability ordering against the database head. Missing, corrupt or mismatched recovery data must fail explicitly. Do not disable depth 2 or fabricate roots to make the smoke pass.

Resume by implementing that recovery fix in an isolated Rust worktree, with restart/frontier tests, then rerun the preserved-data restart and the strict ingest checks. Shared EL rolling restart is separately unsupported by fleet7.sh roll; this test deliberately used whole-fleet stop/up while retaining data. Only move to E4/E7 and controlled throughput comparisons after recovery succeeds.
