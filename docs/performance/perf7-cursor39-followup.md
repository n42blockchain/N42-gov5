# perf7 cursor39 follow-up — 2026-09-05

The flagship performance goal is not achieved. These runs are diagnostic;
there is no accepted paired Go/Rust result.

## Recorded runs

Artifacts are under `/data/blockchain/gov5-perf7-20260904-2017`.

| Run | 30-second TPS windows | Median TPS |
| --- | --- | --- |
| `cursor39-warmup` | 48,898.89 / 43,465.47 / 43,465.63 | 43,465.63 |
| `worker31/cursor39-warmup` | 48,898.90 / 43,465.90 / 38,032.45 | 43,465.90 |

The worker31 run changed `N42_SENDER_RECOVER_WORKERS` to 31. A single
unbookended run establishes no causal improvement or regression. It does not
rule out sender verification as a bottleneck. CPU profiles were collected
**after** measurement windows and cannot establish measured-window CPU shares.

Both offline summaries report successful recipient-balance, sender-nonce,
signature, and changeset/history checks for each node's applied ancestry.
These are not independent EVM replay, fee/reward balance replay, or proof of
canonical finalization. Original cursor39 node2 reports height 154 with
`applied_is_canonical=false`; worker31 node4 reports height 153 with that flag
false. Worker31 node1 reports height 152 while other nodes report 153. Full
final-state agreement remains unproven. The measured window boundary hashes
and roots were checked across seven nodes.

## Artifact integrity and orchestration corrections

An initial worker31 attempt set `QS_ROOT` externally, but `nvme-env.sh`
overwrote it. That attempt reopened the original cursor39 databases, cleared
their pool journals and produced empty blocks before cancellation. Therefore
original cursor39 offline reports describe the state **before that restart**,
not the databases now on disk. Some run artifacts were overwritten by the
restart. Do not rerun the old manifest validator as if these were immutable
original-round artifacts. The original and worker31 reports have not been
rewritten to conceal this limitation.

`run-go-cursor39.sh` now recognizes `N42_QS_ROOT_BASE` after loading defaults.
The worker31 successful retry used:

```sh
mkdir -p /data/blockchain/gov5-perf7-20260904-2017/worker31/cursor39-warmup
python3 build/perf7/box-claim.py \
  --record /tmp/n42-perf7-worker31-claim4.jsonl -- \
  env N42_SENDER_RECOVER_WORKERS=31 \
  N42_QS_ROOT_BASE=/data/blockchain/gov5-perf7-20260904-2017/worker31 \
  bash build/perf7/run-go-cursor39.sh warmup
```

This command is a historical reproduction record; use a fresh directory for
future runs. Earlier retries and their exits are in claim1/2/3 records.
Worker31 offline audits and two microbenchmark attempts were incorrectly run
outside the claim wrapper. They must not be treated as isolated performance
evidence. Future builds, tests and offline audits use the wrapper as well.

## Next candidate: bounded pool snapshots

The worker31 phase summary records import medians of approximately 386 ms
sender checks, 695 ms execution, 358 ms root calculation, and 1,056 ms writing
(total 2,681 ms). The summary selects measured heights; it is not a full-hash
join for every timing event.

The candidate adds batches of at most 256 pool lookups under one read lock.
Hashing, signer checks and comparisons of recovered and declared senders remain
outside the lock. Wrong-hash hints and misses still use signature recovery.
The candidate must pass race/safety tests and a focused benchmark before a
fresh seven-node experiment. A microbenchmark improvement alone is insufficient
for acceptance.

## Batch40 / batch41 evidence

`batch40-warmup` completed its online observers, native commit trace join and
seven offline ancestry audits under `/tmp/n42-batch40-warmup-claim.jsonl`.
Its node2 applied head is height 154 and noncanonical; other applied heads
are height 153 and canonical. It remains a diagnostic warmup only.

The added `N42_SENDER_TRACE=1` output showed `hintFills=163000` for full imported
blocks. Thus wire senders were absent, and the expensive path was the serial
`applySenderHints`, not the wire-declared sender gate optimized first.
Batch41 extends bounded snapshots to that actual path. Sender filling remains
in order, and each pool object's signed hash is rechecked before its memo is
used. Batch misses and wrong-hash entries fall through to normal recovery.

Isolated microbenchmarks (32 Go processors, five measured iterations):

| Scope | Scalar | Batch | Limitation |
| --- | --- | --- | --- |
| 163,000 cached wire-declared sender checks | 17.14 ms | 1.88 ms | Hot hash/memo, no concurrent pool writer |
| 163,000 cold-decoded sender fills | 131.04 ms | 115.53 ms | Includes first hash; decoding outside timer, no concurrent pool writer |

The second benchmark adds approximately 11 KB per call. Neither row measures
seven-node throughput. Logs: `/tmp/n42-batch40-{race,bench}.log` and
`/tmp/n42-batch41-{race,bench}.log`; both test jobs completed under their own
BOX-CLAIM guards. Targeted race tests cover false declared senders, pool hash
mismatches, pool misses, batch boundaries and concurrent admission/removal.

Current full experiment command:

```sh
python3 build/perf7/box-claim.py \
  --record /tmp/n42-batch41-experiment-claim.jsonl -- \
  bash build/perf7/run-batch41-experiment.sh
```

This driver builds the candidate, runs fresh `batch41-warmup`, `batch41-a1`,
`batch41-b`, `batch41-a2` directories, then audits all four. A uses immutable
`n42-storage-cursor39`; B and warmup use `n42-batch41`. Source and binary hashes
are recorded in `build/perf7/batch41-{prepared,validation}.json`. The source
now includes the second change; batch40's preserved binary is the evidence for
its earlier snapshot. Status/results must be read from the actual experiment
artifacts; launching this driver does not establish improvement or acceptance.


### Batch41 completed measurements and audits

All four managed legs passed the online receipt, CPU-affinity, database-mode,
and memory/IO observers. A1, B, and A2 measured 24, 25, and 25 full blocks:

| Leg | 30-second TPS windows | Whole-round transactions |
| --- | --- | --- |
| A1 | 43,465.9 / 43,465.7 / 43,465.9 | 3,912,000 |
| B | 48,899.1 / 43,465.9 / 43,465.9 | 4,075,000 |
| A2 | 48,899 / 43,466 / 43,466 | 4,075,000 |

B equals the second baseline in whole-round transactions. Its +4.17% over A1
is matched by baseline drift, so this A-B-A does not establish a throughput
increase. The median window rate remains about 43,466 TPS, far below the
flagship target.

`batch41-phases.py` binds each follower timing to one unique node/height that
has a matching canonical blockwrite/native-commit hash. It rejects ambiguous
retries because the import timing log itself has no hash. Medians in ms:

| Phase | A1 (144 samples) | B (150 samples) | A2 (150 samples) |
| --- | --- | --- | --- |
| Sender stage | 392.386 | 165.398 | 369.114 |
| EVM loop | 707.098 | 721.112 | 696.924 |
| Root/finalize | 364.664 | 354.526 | 361.111 |
| Write | 1052.104 | 1163.786 | 1091.237 |
| Complete import | 2714.990 | 2616.620 | 2684.013 |

Sender-stage reduction appears against both baselines; this is a stage-time
finding, not a proven throughput gain. Stage medians do not sum to the median
of the total. Post-window node0 CPU samples identify pool admission signature
recovery as a candidate resource consumer; they cannot establish its share
during the measured windows.

All four rounds passed all seven offline ancestry audits, independently
recovering every stored transaction sender, checking two million recipient
balances per node, sender nonces, and the changeset/history bijection. The
comparison also verifies identical audit binary hashes between all four legs.
Warmup independently verified 6,111,800 signatures per node; B verified
6,087,200 per node. Both have identical canonical applied heads at height 153.

In both A1 and A2, node2 stopped at applied height 154, noncanonical at that
height, while the other six applied heads were canonical at height 153.
This is not proof of a conflicting finalized block: the applied marker can be
ahead of canonicalization. It does leave final same-head state agreement
unproven. The online measured boundary comparisons remain valid; the final
comparison retains `final_canonical_applied_agreement=false`.

The foreground guard exited zero and released both claims at
2026-09-05 07:14:18 UTC. At 07:14:30 UTC, both claim files were absent and the
guard's heavy-process scan was empty; the user was notified that the box was
free for their 20-slot A-B-A. `compare-batch41.py` then passed and wrote
`/data/blockchain/gov5-perf7-20260904-2017/batch41-comparison.json` with status
`validated-diagnostic-comparison` and `formal_go_rust_acceptance=false`.
There is still no demonstrated throughput increase or flagship acceptance.

### Validated follow-up: small address sorting

Two separately claimed jobs tested the address-sort hypothesis. The first,
`sort42`, replaced reflection with `slices.SortFunc` at every size. Although
small maps got faster and allocated less, 163,000-address sorting regressed
from 36.14 ms to 58.05 ms. That all-size variant was rejected.

The retained `sort42b` implementation uses the generic sorter only for sets of
at most three addresses; larger sets retain the former indexed sorter and
lexicographic order. Five-sample microbenchmark medians (GOMAXPROCS=1):

| Addresses | Previous ns/op | Selected ns/op | Previous bytes/allocs | Selected bytes/allocs |
| --- | ---: | ---: | --- | --- |
| 0 | 41.46 | 9.674 | 24 / 1 | 0 / 0 |
| 1 | 89.89 | 61.29 | 48 / 2 | 24 / 1 |
| 3 | 205.4 | 130.3 | 160 / 4 | 64 / 1 |
| 32 | 2534 | 2585 | 736 / 4 | 736 / 4 |
| 163000 | 38363632 | 36521744 | 3260512 / 4 | 3260512 / 4 |

These are sequential helper microbenchmarks, not fleet A-B-A evidence. The
large-set implementation is unchanged; its timing variation is not claimed
as a causal gain. Small-set allocation reductions are demonstrated. Neither
change is included in the preserved batch41 binary.

Both jobs passed `go test -short ./modules/state/... ./internal` and
`make -o version-build build`. Final source hashes and benchmark medians are
in `build/perf7/sort42b-{validation,comparison}.json`; logs are
`/tmp/n42-sort42b-{test,bench,build}.log`. The guard record
`/tmp/n42-sort42b-validation-claim.jsonl` records an isolated zero exit and
release at 2026-09-05 07:21:07 UTC. Both claims were subsequently absent and
no heavy processes remained. No seven-node throughput gain is established.

### Lazy43: allocate storage caches on first use

`stateObject` previously allocated a storage map header even for an ordinary
transfer account that never accesses storage. Fresh objects and recycled
objects with no retained map now keep it nil until `cacheCommittedState` or
`setState` first stores a slot. Existing small retained maps are still reused;
storage values, journaling, and epoch rules are unchanged.

The transfer-execution benchmark uses the existing `BenchmarkApplyTransferEVM/reuse`
fixture: 1,000 signed transfers through the normal EVM and transaction
finalization, GOMAXPROCS=1, five samples per variant. The preceding small-sort
change is present in both variants.

| Per 1,000 transfers | Before | Lazy43 |
| --- | ---: | ---: |
| Median time | 3.433276 ms | 3.401166 ms |
| Bytes allocated | 2,136,546 | 2,088,450 |
| Allocations | 17,076 | 16,074 |

The allocation reduction is demonstrated; the timing difference is small and
comes from sequential before/after microbenchmarks, not a seven-node bookended
comparison. No fleet throughput improvement is claimed.

`TestLazyStorageAcrossTransactions` covers the first write on a new contract
(which bypasses committed-storage caching), reads on an existing account,
snapshot rollback, and the next transaction's committed view. State, commitment,
snapshot, witness and internal tests passed. Targeted race tests cover that
scenario, pooling/reset equivalence, finalization read errors and destruction;
`make -o version-build build` passed as well. Evidence:
`build/perf7/lazy43-validation.json`, `/tmp/n42-lazy43-{test,race,before,after,build}.log`,
and `/tmp/n42-lazy43-{before,validation}-claim.jsonl`.

The original state-object source is preserved in
`build/perf7/state_object-lazy43-before.go.txt` with its hash recorded in
`lazy43-before-source.json`. A future paired binary build can use a Go build
overlay for this one file without replacing the dirty working tree. The final
guard exited zero and released both claims at 2026-09-05 07:28:06 UTC;
the subsequent heavy-process check was empty. Seven-node validation remains
pending for these allocation changes.

### Append44: native append batching probe, not adopted

An isolated prototype under `build/perf7/_append44` tested 163,000 variable-length
account changeset rows at one block height. MDBX's existing `PutMulti` requires
DupFixed and cannot represent these rows. The prototype uses the public native
transaction handle and the installed v0.41.0 header, with a bounded packed
buffer and repeated `MDBX_APPENDDUP` calls inside one C invocation. It is not
linked into the node and deliberately bypasses production write counters and
write-probe hooks, so it is an optimistic upper-bound probe.

Packing/copying is timed; input value construction, transaction setup, rollback
and durability/commit are not. Five sequential microbenchmark samples:

| Method | Median per 163,000 rows | Bytes allocated | Allocations |
| --- | ---: | ---: | ---: |
| Existing transaction AppendDup | 72.735 ms | 648 | 7 |
| Packed batches of 256 | 70.279 ms | 11,075,584 | 637 |
| Packed batches of 4,096 | 68.194 ms | 10,600,448 | 40 |

The best optimistic saving is only about 4.54 ms with roughly 10.6 MB extra
allocation per block. This does not justify integrating a new C write path.
The prototype's ordered variable-length round-trip and out-of-order rejection
tests passed. Its first attempt failed on MDBX_INCOMPATIBLE because the table
was opened without DupSort; that failure is preserved in
`/tmp/n42-append44-probe.log`. The corrected result is in
`/tmp/n42-append44-probe2.log` and `build/perf7/append44-probe-results.json`.
Both runs held separate claims; the corrected guard exited zero and released
at 2026-09-05 07:38:07 UTC. No heavy jobs or fresh claims remained afterward.

The unchanged general Block-STM path remains unsuitable for enabling: it shares
a non-concurrent MDBX-backed reader and re-executes broad suffixes on conflicts.
A larger execution experiment should first establish serial equivalence for
independent transfer components using immutable account snapshots and ordinary
EVM execution, with beneficiary credits merged only when no transaction can
observe that beneficiary. Unsupported transactions, shared state, protocol
system-account interactions and errors require conservative fallback. This is
a possible next design to evaluate, not an implemented or accepted fast path.

### Dependency45: measured transfer components

The read-only analyzer `build/perf7/_dependency45` verified all 25 canonical
blocks and 4,075,000 transactions in batch41 B against the original measurement.
It builds undirected sender/recipient components and preserves original
transaction order within each group. Tests cover late bridges, a recipient
subsequently sending funds, self-transfers, disconnected groups and empty input.
Signatures are not recovered again here; the preceding seven-node offline
audits verified the stored senders. This is a graph diagnostic, not EVM replay.

Every block has 28–31 endpoint components (median 29), with a largest component
of 6,000 transactions. Greedy balancing onto 32 lanes gives a maximum of 6,000
transactions per lane. All 163,000 recipients within each block are distinct;
the block typically drains long nonce sequences from about 29 senders, rather
than including all 6,000 configured senders. No endpoint equals coinbase, the
consensus SystemAddress, or a configured EIP-1559 fee collector. None of these
transaction-count results is an execution-time prediction.

Results: `batch41-b/dependency45.json`; provenance:
`build/perf7/dependency45-manifest.json`; test log:
`/tmp/n42-dependency45-test.log`; claim:
`/tmp/n42-dependency45-claim.jsonl`. The guarded job exited zero and released
at 2026-09-05 07:46:26 UTC. Both claims and heavy processes were absent afterward.

The next meaningful experiment is serial-vs-component EVM execution on an
immutable account snapshot from these actual blocks. Read historical values
explicitly at timestamp N for the pre-state of block N, checking cursor errors
and branch identity. Do not use HistoryStateReader with the live transaction
as its overlay: its current-state-first semantics would select the wrong base.
Run normal ApplyTransactionWithEVM inside each group, preserve original receipt
order/cumulative gas, and compare all endpoint balances/nonces/existence and
fee-account deltas with the serial result. Any eventual production path needs
separate guards for contract/precompile accesses, protocol system accounts,
fee/reward recipients, fork rules and error fallback, plus the original
block-start/block-end/finalization behavior. No parallel execution is enabled
by this analyzer.

### Lane46: actual-block serial/component EVM prototype

The standalone `build/perf7/_lane46` prototype reconstructs immutable account
values before each measured block using account history, checks applied-branch
ancestry, and executes each component with ordinary ApplyTransactionWithEVM.
It retains transaction order within groups, merges fee-account balance deltas,
and restores receipt order and cumulative gas. Unexpected code/storage reads
fail; endpoints overlapping fee/system/precompile/reward accounts and nonempty
account code are rejected. This is restricted to the paid EOA fixture, not a
general executor or an enabled node feature. Stored senders were verified by
the preceding offline audits, not independently recovered in this timing tool.

Each process does an excluded serial warmup followed by serial/grouped/serial.
The second run uses GOMAXPROCS=32 and `taskset -c 0-15,128-143`:

| Block | Components | Serial A1 | Grouped B | Serial A2 | Historical snapshot + graph |
| --- | ---: | ---: | ---: | ---: | ---: |
| 121 | 29 | 649.934 ms | 72.346 ms | 655.495 ms | 352.547 ms |
| 130 | 31 | 675.966 ms | 77.734 ms | 684.082 ms | 363.184 ms |
| 138 | 28 | 665.051 ms | 75.437 ms | 681.832 ms | 358.028 ms |

Every receipt and every touched account's balance, nonce, existence and code
hash matched between serial and grouped execution. All endpoint account views
also matched persisted post-block history (163,029 / 163,031 / 163,028 accounts),
and the recomputed receipt root matched the actual block header. The separate
race-instrumented block121 run passed the same checks with no race reports;
its timings are excluded from the table. Snapshot/graph construction is outside
the EVM timings and shown separately, not hidden as free preparation.

Important limits: the tool does not run protocol system calls, engine
finalization, state-root calculation, merge into a live IntraBlockState, or
persistence. Fee accounts are compared between executors, not against their
post-finalization chain balances. It uses a nil engine in ApplyTransaction;
for this paid HotStuff transfer fixture the nonce/transfer/paid-fee behavior
matches the exercised path, but this is not proof for other engines or free
transactions. No seven-node TPS or full-block acceptance claim follows.

Provenance and results are in `build/perf7/lane46b-validation.json`,
`/tmp/n42-lane46b-{121,130,138}.json`, corresponding `.err` files,
`/tmp/n42-lane46b-race.{json,err}`, and `/tmp/n42-lane46b-claim.jsonl`.
The guard released at 2026-09-05 07:59:37 UTC after an isolated zero exit;
claims and heavy processes were absent afterward. The initial unpinned result
and source remain in `/tmp/n42-lane46-result.json` and
`build/perf7/lane46-first-source.go.txt` and are not mixed into the table.

This justifies investigating integration, not enabling the existing broken
Block-STM switch. Integration must preserve block-start/end behavior, prove
account-snapshot consistency, and merge results into the real state's journal
and dirty set before root/commit. Adversarial boundary tests must cover fee
overflow and empty-account deletion, implicit-account observation, invalid
nonce/balance and execution errors, unsupported calls, and fallback without
partial real-state writes. The existing account prefetch cache covers only
recipients and hands out values destructively; it cannot simply be shared
between workers as an immutable reader.

## Merge47: concrete state and ordered commit writes

The offline prototype now merges grouped results into a fresh concrete
IntraBlockState using its ordinary setters and FinalizeTx. It preserves the
exact dirty address set, including touched accounts with unchanged values.
Unit tests cover those touches, empty-account deletion, and missing snapshot
input; race-instrumented unit tests and actual block121 replay passed.

All three fixtures matched serial receipt objects, header receipt roots,
historical endpoint accounts, dirty sets, and the complete ordered CommitBlock
writer stream (original/current account encodings, deletions and wipe calls).
This still excludes system calls, engine finalization, state roots and actual
persistence. The output field live_merge_ms denotes a fresh offline state,
not integration into a running node.

| Block | Serial A1 ms | Grouped B ms | Serial A2 ms | Merge ms | Snapshot + graph ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| 121 | 682.534 | 102.407 | 685.996 | 473.065 | 347.358 |
| 130 | 702.415 | 118.012 | 708.536 | 448.290 | 362.286 |
| 138 | 708.300 | 109.462 | 721.560 | 439.286 | 365.234 |

Merge timing includes input validation, sorting, object creation, ordinary
setters, FinalizeTx and output verification. It is diagnostic, not the cost
of a minimal production merge. It nevertheless shows that grouped EVM timing
alone substantially overstates the available gain. Historical snapshot/graph
cost is also separate and cannot be omitted from end-to-end claims.

Evidence: build/perf7/merge47-validation.json and
/tmp/n42-merge47-{121,130,138,race}.{json,err}; guard record
/tmp/n42-merge47-claim.jsonl. Same 32 logical CPU affinity as lane46b, excluded
serial warmup then A-B-A. Race timings are excluded. The isolated job exited
zero and released both claims at 2026-09-05 08:09:27 UTC. A subsequent check
found no effective claims or detected heavy processes. No production switch
was enabled and seven-node performance acceptance remains unproven.

## Snapshot48: current account view for restricted workers

Added modules/state/account_snapshot.go as an integration building block.
SnapshotAccounts runs on the owning state goroutine, captures current account
metadata after earlier block execution, materializes pending balance credits
through the normal journal-aware read, and deep-copies the result. The returned
AccountSnapshot has no database reference and gives each concurrent reader its
own copy. Captured absent/deleted accounts return nil; uncaptured addresses and
all code/storage requests return errors. No partial snapshot is returned after
a state-reader error. This is not a complete EVM state clone: transaction
metadata, selfdestruct flags, storage and code are not copied. The caller must
establish a transaction boundary and reject unsupported execution.

Corrected StateReader's blanket concurrency claim. The actual MDBX GetOne
implementation in lib/kv/mdbx/kv_mdbx_tx.go uses a mutable per-transaction cursor
cache; its existing opt-in race reproduction documents why it cannot be shared
as the base reader of the existing Block-STM executor.

The local pinned Rust reference's h2-execution/src/execution_path.rs explicitly
classifies canonical execution as LIVE_SEQUENTIAL and reserves LIVE_PEVM for
future qualification. Its historical parallel modes are not evidence of a live
seven-node parallel speedup. The Go work must likewise qualify the eventual
complete live execution path before claiming comparable performance.

No node execution switch uses this snapshot yet. The intended integration
boundary is after sender verification and ProcessExecutionBlockStart, before
the ordinary transaction loop. Snapshot acquisition, grouping, worker execution
and merge must all be included in import-phase timings. BAL, witness/tracing,
implicit fee/system-account observation and unsupported transaction shapes
need explicit exclusion or equivalent handling. Failed speculation must leave
the original state usable for serial execution; a failed underlying account
read is a sticky state error, not a successful fallback.

Validation passed: go test -race ./modules/state -run TestAccountSnapshot
-count=1; go test ./modules/state/...; make build. The initial test fixture
omitted Initialised=true for its persisted account, causing the constructor
to reset its balance; that test data was corrected and the full chain rerun.
Evidence: build/perf7/snapshot48-validation.json and
/tmp/n42-snapshot48b-{race,state,build}.log. The claim guard recorded an isolated
zero exit and released at 2026-09-05 08:22:53 UTC. No fleet TPS result is claimed.

## Transfer49: restricted execution from current state

Added internal/transfer_execution.go and transfer_components.go. The candidate
executor consumes SnapshotAccounts from the current state and uses ordinary
ApplyTransactionWithEVM with the actual engine context. It returns speculative
receipts and the full dirty account set, without installing worker writes into
the caller's state. Endpoint components preserve original transaction order;
receipts are restored to original indices and cumulative gas order.

Admission is limited to paid legacy/dynamic-fee EOA value transfers with empty
calldata/access lists and exactly 21,000 gas, at least two components, HotStuff,
no Parlia rules or BAL. It excludes active precompiles, SystemAddress, observed
fee destinations and accounts with code. The author is resolved through the
engine, rather than assuming header.Coinbase. Fee credits are reduced across
components; a conservative checked upper bound excludes wrapping balances
including the case where tip and base-fee collectors alias. Unsupported cases
and worker failures return no candidate so the caller can execute serially on
its unchanged account view; snapshot-reader errors remain fatal.

Tests compare complete receipts and dirty account metadata against serial EVM
execution, including a pre-execution endpoint credit, zero tips, separate or
aliased fee collectors, and an already funded fee account. They check original
account values remain unchanged and exercise invalid nonce/balance, code,
precompile, fee-endpoint, fee-overflow and block-gas fallback. Component tests
cover a late bridge joining earlier groups and reverse-direction transfers.

Validation passed: go test -race ./internal -run
'Test(SpeculateTransfers|TransferComponents)' -count=1; go test ./internal
./modules/state/...; make build. Evidence is in build/perf7/transfer49-validation.json
and /tmp/n42-transfer49-{race,tests,build}.log. The isolated claim guard exited
zero and released both markers at 2026-09-05 08:31:30 UTC.

This function is not yet wired to StateProcessor, does not merge into canonical
state, and does not prove full-block state roots, persistence or fleet TPS.
Before integration, the caller must enforce verified senders, transaction
boundaries, and absence of witness/tracing/BAL-dependent behavior. Full live
performance and the same-head agreement issue from batch41 remain open.

## Install50: merge into the caller state and compare commit streams

Added MergeEOAAccounts in modules/state/account_merge.go. It requires a clean
transaction boundary without active rollback revisions, state tracer, balance
observer or state snapshot recorder; unmaterialized credits also exclude merge.
All output membership, metadata and nonce checks precede writes. The entire
captured input is checked against the current account view to reject stale
speculation, including changed inputs outside a particular output. Only then
do ordinary setters and FinalizeTx install the dirty accounts. Errors after
installation are fatal, never permission to retry serially on modified state.

The candidate retains its input snapshot for this check. Worker block-gas
accounting is now explicitly checked against 21,000 per accepted transfer.
The integration tests install results into the actual caller's IntraBlockState
and compare its complete ordered CommitBlock writer stream to serial EVM
execution, including original/current account bytes, deletion and wipe calls.
They preserve a pre-execution endpoint credit and test all prior fee cases.
State tests reject stale inputs, pending transactions, live rollback revisions,
uncaptured outputs, root/code changes, empty live outputs and nonce regression;
they also preserve touched-but-unchanged accounts and empty-account deletion.

The first race, module-test and make-build chain completed in isolation
(/tmp/n42-install50-*.log, source hashes in build/perf7/install50-first-source.json).
Subsequent review explicitly excluded Aura's special system-account empty
preservation rule and added its rejection test. The amended race checks passed,
but the full revalidation was interrupted by a new Rust claim at 08:41:55 UTC;
the guard recorded conflict, stopped only its own process group and released
both markers at 08:41:56. This conflicted run is not an accepted complete chain.

Final revalidation is queued through /tmp/n42-install50c-claim.jsonl (foreground
exec session 30407); poll the actual handle before treating it as terminal or
starting another run. build/perf7/install50-validation.json records the pending
status. No StateProcessor switch is enabled yet, no full state root/persistence
proof follows from the ordered writer comparison, and fleet acceptance remains
open.

## Gate51: opt-in import integration (validation queued)

StateProcessor now has a default-off N42_TRANSFER_COMPONENT_WORKERS=2..32
option for blocks of at least 2,048 transactions. It attempts the restricted
executor only after sender verification and block-start operations; complete
snapshot/group/worker/merge time is included in the existing execution phase.
A successful merge supplies receipts and gas to the ordinary post-loop checks,
block-end calls and engine finalization. A diagnostic TransferComponents phase
field and installed log flag report whether the path was actually used. Failed
admission or stale merge uses the original loop; errors remain fatal.

The boundary is checked before speculative reads. Only the current state's
PlainStateReader or blockAccountPrefetchReader is admitted, and workers still
receive an independent AccountSnapshot. State hooks, writable snapshots, live
transactions/revisions and non-default VM configuration exclude the path. The
last transaction context is restored after merge for block-end processing.

Added tests for an actual 2,048-transaction StateProcessor execution with the
option off and on, comparing receipts and complete ordered commit writes and
asserting the alternate branch and engine finalization ran. The test engine
adds a reward; it is not the actual HotStuff root verifier. A forged declared
sender must fail before writes or finalization. Gate tests exclude unknown
readers, observers, pending transactions and non-default VM settings. These
new tests have NOT yet run as of this entry.

User reports Rust restarted an approximately 28-minute run after changing
system hugepage settings. Read-only capture in hugepage51-environment.json
records THP madvise, defrag defer, and zero reserved HugeTLB pages. Subsequent
fleet A-B-A must record the new environment and include a fresh excluded
warmup; older TPS cannot be attributed to code changes across this transition.

The unstarted install50c queue (session 30407) was explicitly cancelled by its
owner, exited 130 and was superseded by gate51 validation, not silently
restarted after a polling timeout. Current foreground session 38571 waits on
/tmp/n42-gate51-claim.jsonl. Rust's claims and actual fleet processes remain
active; the Codex guard has not claimed or started a heavy child.

Provenance and pending checks are in build/perf7/gate51-validation.json. The
option remains off for normal runs and has not been qualified for a fleet.
Full block-system-call, root, persistence and final same-head agreement proofs
and the actual seven-node Go/Rust throughput comparison remain outstanding.

Gate51 follow-up while Rust retains the machine: added
internal/transfer_qmdb_test.go before any queued heavy child started. Its test
name is included in the existing race-test filter. The test seeds identical
persisted QMDB layouts, executes serial/parallel 2,048-transaction imports with
real EIP-4788 prefix storage writes and a test finalizer reward, commits account
and storage changes plus their history indices, flushes QMDB in the same
transaction, then reloads a fresh computer from committed rows. It compares
receipts, roots and six persisted state/history tables, and explicitly checks
the beacon storage and history are nonempty. It has not run yet; its finalizer
still does not model actual HotStuff voting/rewards/fork choice or crash recovery.

Prepared gate52 experiment drivers, without building or launching a fleet.
They use one immutable n42-gate52 binary for excluded warmup/on, A1/off, B/on,
A2/off, otherwise retaining batch41's workload, affinity and durable full-history
settings. The build refuses to proceed until gate51-validation.json records
tests-build-passed and source hashes still match. Unique leg directories are
under /data/blockchain/gov5-perf7-20260905-gate52 and cannot be reused.

The extra transfer-mode observer reads only the worker variable from exactly
owned node environments; other environment entries are never emitted. The
post-round checker requires every measured canonical follower import to have
one unambiguous installed=true trace in B/warmup and none in A. The comparator
checks pre/post hugepage settings across every leg, alongside existing CPU,
DB-mode, receipt and full offline balance/history audits. Final same-head
agreement remains a reported requirement, not assumed from TPS.

Python drivers passed AST parsing and shell drivers passed bash -n only.
Once the integration checks pass, the intended command is:

```sh
python3 build/perf7/box-claim.py --record /tmp/n42-gate52-experiment-claim.jsonl -- bash build/perf7/run-gate52-experiment.sh
python3 build/perf7/compare-gate52.py
```

The comparison must run after the experiment claim records successful completion
and release. No gate52 binary or measurement is claimed by this preparation.

Gate51 validation completed successfully in isolation at 09:04:22 UTC, including
the newly added QMDB persistence test in the race filter, complete internal/state
module tests and make build. Both claims were released. Current source hashes
were checked against gate51-validation.json before updating its status to
tests-build-passed. This is actual root/reload/history test evidence for the
restricted fixture, still not real seven-node HotStuff acceptance.

Gate52 was then submitted to its own claim guard. Four batch41 leg directories
occupy about 18 GiB each; the volume had about 825 GiB free before scheduling,
so the planned four fresh legs fit without removing prior evidence. The new
fleet is pending the claim protocol; inspect /tmp/n42-gate52-experiment-claim.jsonl
and its actual foreground process before inferring completion.

Gate52 is now live under foreground exec session 38035; the guard started its
child at 09:07:25 UTC. Build and excluded warmup completed. All seven node
worker environments were verified as 32, the online observers and native commit
join passed, and the measured canonical follower imports passed the explicit
component-path installation check. The full candidate stage in warmup was
approximately 0.9–1.0 seconds per sampled dense block, including capture, planning
and merge. This is not a fleet improvement claim: integration overhead offsets
much of the standalone EVM gain. The driver proceeds to A1/off, B/on and A2/off
without changing the binary, then runs all offline audits. Poll session 38035
and the claim record; do not start another fleet while it remains alive.
