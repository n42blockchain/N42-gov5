# N42_* environment switches

Generated from the `common/envswitch` registry; do not edit by hand.
Regenerate with `go test -tags nosqlite,noboltdb,n42el ./internal/node/ -run TestEnvSwitchDoc -update`.

Scope: `perf` changes speed or memory only; `diag` adds logging or dumps; `config` selects a mode, path or size; `consensus` changes a rule every validator must apply alike; `bench-only` must not be set on a production node.

| Name | Scope | Kind | Default | Package | Description |
|------|-------|------|---------|---------|-------------|
| `N42_ACCOUNT_PREFETCH_WORKERS` | perf | raw | - | internal | Worker count of the account prefetch pass. |
| `N42_AHEAD_BASE_CLAIM_WAIT` | perf | not0 | on | internal | Default on: a claim waits for a prefill still reading; 0 stops the wait. |
| `N42_AHEAD_BASE_DEPTH` | perf | raw | - | internal | K: how many blocks behind the child's parent a base prefill may read the tree. |
| `N42_AHEAD_BASE_EARLY` | perf | defaulton | on | internal | Default on: a preparation starts its base prefill together with itself. |
| `N42_AHEAD_BASE_PREFETCH` | perf | defaulton | on | internal | Default on: prefill the ahead run's base tree reads. |
| `N42_AHEAD_COLLECT_CUT` | perf | is1 | off | modules/state | Account fold object slab: prepared objects of the fold share one slab (r68a). |
| `N42_AHEAD_PREP` | perf | defaulton | on | internal | Default on: the ahead run's preparation pipeline. |
| `N42_AHEAD_PRE_EARLY` | perf | optin | off | internal | Opt-in: a child launched after its parent's pre-hook starts its pre phase early. |
| `N42_ALLOC_LEAN` | perf | is1 | off | common/transaction | Lean transaction allocation: fewer per-transaction allocations on decode. |
| `N42_ANCHOR_DIR` | config | declared | - | internal/ethel/eldevp2p | Directory of consensus anchors an eth-el catch-up verifies against. |
| `N42_APPLY_BULK` | perf | not0 | on | internal | Default on: bulk account pass of applyMVSToIBS; 0 restores the per-entry loop. |
| `N42_ASYNC_MERKLE` | perf | not0 | on | internal/ethel/eldevp2p | Default on: async merkle pipeline of the eth-el downloader; 0 reverts to the synchronous stage. |
| `N42_ATTEST_AGGREGATOR` | config | declared | - | internal/ethel/eldevp2p | URL of the attestation aggregator an eth-el node posts to. |
| `N42_ATTEST_COMMITTEE` | bench-only | declared | - | internal/frame/attest | TEST ONLY: committee size k and attestations needed, for private benchmark fleets. |
| `N42_ATTEST_KEY` | config | declared | - | internal/ethel/eldevp2p | Attestation signing key of an eth-el node. |
| `N42_AUDIT_NIL_ACCT` | diag | is1 | off | modules/state | Cross-check every nil account read of the buffered reader against the base. |
| `N42_AVAIL_ADMIT_GAS` | perf | raw | - | internal | Availability-admission gas mode. |
| `N42_AVAIL_EARLY_PREP` | perf | defaulton | on | internal | Default on: the availability check starts the preparation early. |
| `N42_BLOCKHASH_DIR` | config | declared | - | internal/node | Cold blockHash to number index directory (blockhashindex segments). |
| `N42_BLOCK_CACHE_BLOCKS` | perf | int | 512 | internal | Count of fully decoded blocks the block cache keeps. |
| `N42_BLOCK_CACHE_PRIME` | perf | declared | - | internal | Opt-in: prime the block cache with a block already decoded when it becomes canonical. |
| `N42_BLOCK_DECODE_REUSE_POOL` | perf | declared | - | internal/sync | Block-push receive path reuses pool-resident transaction objects. |
| `N42_BLOCK_GOSSIP_FALLBACK` | config | declared | - | internal | Default on: SealedBlock's gossip fallback runs unconditionally; 0 gossips only with no direct peers. |
| `N42_BLOCK_SCRATCH_POOL` | perf | not0 | on | internal, internal/ingest, internal/parallel, modules/state/commitment | Default on: per-block scratch reused across blocks; 0 allocates fresh (r31a). |
| `N42_BLS_COMMITTEE` | config | declared | - | internal/api, internal/api/consensusrest | Committee size of the benchmark BLS voter pool. |
| `N42_BLS_POOL_SEED` | config | declared | - | internal/api, internal/api/consensusrest | Master seed of the benchmark BLS voter pool (hex). |
| `N42_BLS_POOL_SIZE` | config | declared | - | internal/api, internal/api/consensusrest | Size of the benchmark BLS voter pool. |
| `N42_BLS_RAMP_BLOCKS` | config | declared | - | internal/api, internal/api/consensusrest | Blocks over which the benchmark BLS pool ramps up. |
| `N42_BUILD_STALL_DIAG` | diag | is1 | off | internal, internal/miner | Diagnostic mutex-wait timers on the build path and its watchdog. |
| `N42_CHGSET_APPEND` | config | is1 | off | modules/state | appendDupRows puts rows with the cursor's APPEND flag (r58a). |
| `N42_CODETRACE` | diag | raw | - | modules/state | Log EVM code reads where the account carries a code hash but no code. |
| `N42_COLLECT_SHARD` | perf | raw | - | internal | Goroutines that split the bulk account pass's serial part. |
| `N42_COMMIT_DUP_FAST` | perf | is1 | off | internal | Commit-to-canonical keeps block reads off the MDBX writer (r76a). |
| `N42_COMMIT_INTERVAL` | config | declared | - | internal/ethel/eldevp2p | Commit interval of the eth-el downloader. |
| `N42_COMMIT_JOURNAL_EARLY` | perf | is1 | off | internal/consensus/hotstuff | Take the round-2 journal write (an fsync) off the commit round trip. |
| `N42_CONTENTION_DIAG` | diag | is1 | off | internal, internal/consensus/hotstuff, internal/miner, internal/sync | Contention stamps and lock-wait timing on the vote and seal paths (S14). |
| `N42_CROOT_DEBUG` | diag | declared | - | modules/state/commitment | Dump the concurrent root computation on mismatch or when set. |
| `N42_CS_FREEZER` | config | declared | - | internal/ethel/eldevp2p | Default on: per-block changesets append to freezer segment files instead of MDBX. |
| `N42_DDN_KEY_PASSWORD` | config | declared | - | internal/node | Password of the DDN receipt signer key. |
| `N42_DEFERRED_CHECK_CONCURRENT` | perf | declared | - | internal/sync | Block-push receive path dispatches InsertChain without waiting for the deferred check. |
| `N42_DEFERRED_DEPTH` | consensus | declared | - | common/block | Deferred execution depth k (default 1). |
| `N42_DEFERRED_DEPTH_FROM` | consensus | declared | - | common/block | First block number the deferred depth applies to. |
| `N42_DEFERRED_DEPTH_PREV` | consensus | declared | - | common/block | Deferred depth of the blocks below N42_DEFERRED_DEPTH_FROM. |
| `N42_DEFERRED_EXECUTION_TIME` | bench-only | declared | - | internal | Overrides the chain's deferredExecutionTime on a bench chain. |
| `N42_DROP_STALE_ADDR` | diag | declared | - | modules/state/commitment | Drop the stale root cache of one address (debug). |
| `N42_DUMP160` | diag | declared | - | modules/state/commitment | Dump the dirty account set of a root computation (debug). |
| `N42_DUMP_ADDR` | diag | declared | - | internal/api | Address list to dump at N42_DUMP_BLOCK. |
| `N42_DUMP_BLOCK` | diag | declared | - | internal/api | Block number the N42_DUMP_* switches act on. |
| `N42_DUMP_DIRTY` | diag | declared | - | internal/api | Dump the dirty set at N42_DUMP_BLOCK. |
| `N42_DUMP_DIRTY_STORAGE` | diag | declared | - | internal/api | Include storage in the N42_DUMP_DIRTY dump. |
| `N42_DUMP_SLOTS` | diag | declared | - | internal/api | Storage slot list to dump at N42_DUMP_BLOCK. |
| `N42_E1_EARLY` | perf | defaulton | on | internal | Default on: a light node publishes a fetched result before it stores the block. |
| `N42_E1_LIGHT_FRAMES_AHEAD` | perf | is1 | off | internal | A light node's import writes each block's frames ahead of the block. |
| `N42_E1_LIGHT_LAZY_RESOLVE` | perf | raw | - | internal | E=1 light node moves the frame decode off its vote path. |
| `N42_E1_PREFETCH` | perf | is1 | off | internal | A light node fetches the result of every queued block at once. |
| `N42_E1_SERVE` | bench-only | raw | - | internal | E=1 benchmark mode: address this executor serves results on. |
| `N42_E1_SOURCE` | bench-only | raw | - | internal | E=1 benchmark mode: URL of the executor a light node fetches results from. |
| `N42_ECRECOVER_IFMA` | perf | not0 | on | crypto | Default on: eight-way AVX-512 IFMA ecrecover kernel. |
| `N42_ETL_BUFFER_ACCT_GB` | perf | declared | - | internal/ethel | Account ETL sort buffer, in GB. |
| `N42_ETL_BUFFER_STO_GB` | perf | declared | - | internal/ethel | Storage ETL sort buffer, in GB. |
| `N42_ETL_TMPDIR` | config | declared | - | internal/ethel, internal/txlookup | Directory for ETL and RecSplit spill files. |
| `N42_EXECPROF` | diag | declared | - | internal/api | Profile execution of the eth-el engine adapter. |
| `N42_EXEC_LEAN` | perf | is1 | off | internal/parallel | Lean Block-STM execution (EXEC_LEAN). |
| `N42_FINALIZE_FAST` | perf | optin | off | modules/state | Fast dirty-object loop of the root walk's finalize. |
| `N42_FINALIZE_HOIST_DETACH` | perf | optin | off | modules/state | Hoist the root walk's pending credits and detach prefetch reads. |
| `N42_FINALIZE_TRACE` | diag | is1 | off | internal/consensus/hotstuff | Trace the finalize step of the HotStuff adapter. |
| `N42_FLOOR_LAG_HEADERS` | consensus | is1 | off | internal/consensus/hotstuff | Keep parent links for the committed-floor walk in a window of floor-lag headers. |
| `N42_FOLD_NOJOURNAL` | perf | optin | off | modules/state | The account fold skips the journal on a strict block. |
| `N42_FRAMES_WRITE_EARLY` | perf | optin | off | internal | Opt-in: write a block's frames early. |
| `N42_FRAME_ATTEST` | config | declared | - | internal/frame/attest | Enable frame attestations. |
| `N42_FRAME_ATTEST_COMMITTEE` | consensus | declared | - | internal/frame/attest | Attestation committee size (default 3; raised to f+1). |
| `N42_FRAME_ATTEST_FLUSH_MS` | perf | declared | - | internal/node | How long a committee member batches attestations before signing, in ms. |
| `N42_FRAME_ATTEST_PEERS` | config | declared | - | internal/frame/attest | Ingest addresses attestations are pushed to. |
| `N42_FRAME_BLOCKS` | consensus | declared | - | common/block, internal | Frame-referenced block production (leader side). |
| `N42_FRAME_BLOCK_MAX_FRAMES` | config | declared | - | internal/miner | Cap of frames in one block (default 1024). |
| `N42_FRAME_BLOCK_POOL_GAS` | consensus | declared | - | internal/miner | Gas a frame block gives the transaction pool. |
| `N42_FRAME_DB` | config | declared | - | internal/node | Frames go to their own MDBX store instead of the chain database. |
| `N42_FRAME_DB_MAPSIZE_GB` | perf | declared | - | internal/node | Map size of the frame store, in GB. |
| `N42_FRAME_DB_PRUNE_BATCHES` | bench-only | declared | - | modules/rawdb | BENCH ONLY: the frame store keeps only the last N batches. |
| `N42_FRAME_DB_SYNC` | bench-only | declared | - | internal/node | BENCH ONLY: safe-nosync drops the frame store's per-commit fsync. |
| `N42_FRAME_FILL_HELD` | perf | is1 | off | internal/miner | A build held on a gateway fills from the next frame's ready senders. |
| `N42_FRAME_GATEWAYS` | config | declared | - | internal/ingest | Gateway addresses whose frames this node admits. |
| `N42_FRAME_KEEP_BLOCKS` | config | declared | - | internal/frame/store | Overrides KeepBlocks of the frame store. |
| `N42_FRAME_LOG` | diag | declared | - | internal/node | Append frame data to a log beside the frame store. |
| `N42_FRAME_LOG_KEEP_SEGMENTS` | diag | declared | - | internal/node | Segments the frame log keeps. |
| `N42_FRAME_PICK_READY` | perf | not0 | on | internal/miner | Frame pick: wait for ready senders. |
| `N42_FRAME_PULL_PEERS` | config | declared | - | internal/ingest | Ingest addresses of the validators a node pulls missing frames from. |
| `N42_FRAME_PULL_TIMEOUT_MS` | config | declared | - | internal | Bound of the FRQ1 pull for missing frames, in ms. |
| `N42_FRAME_READY_GRACE_MS` | perf | declared | - | internal/miner | Grace before a frame is picked as ready, in ms. |
| `N42_FRAME_RECOVER_WORKERS` | perf | declared | - | internal/frame | Size of the admission sender-recovery pool (default 8). |
| `N42_FRAME_STORE_BYTES` | perf | declared | - | internal/frame/store | Byte budget of the frame store. |
| `N42_FULLROOT` | diag | declared | - | internal/api | Compute the full state root (debug). |
| `N42_GAS161` | diag | declared | - | internal/api | Gas diagnostics of the engine state adapter. |
| `N42_GOSSIP_VOTE_LANE` | perf | is1 | off | internal/consensus/hotstuff | Hand gossiped votes to their own goroutines. |
| `N42_HASHED_ACC_CACHE` | perf | declared | - | modules/state | Entries of the hashed account read cache. |
| `N42_HASHED_CODE_CACHE` | perf | declared | - | modules/state | Entries of the hashed code read cache. |
| `N42_HASHED_KECCAK_CACHE` | perf | declared | - | modules/state | Entries of the hashed keccak read cache. |
| `N42_HASHED_STO_CACHE` | perf | declared | - | modules/state | Entries of the hashed storage read cache. |
| `N42_HINT_RECOVERY_POOL` | perf | declared | - | common/transaction | Worker count of the hinted sender recovery pool (0 = off). |
| `N42_HISTORY_FOLD_TXN_MS` | config | millis | 30ms | internal | Budget of one history fold write transaction, in ms. |
| `N42_HISTORY_FOLD_YIELD_MS` | config | millis | 5ms | internal | Pause between two history fold write transactions, in ms. |
| `N42_HISTORY_INDEX_DEFERRED` | perf | declared | - | modules/state | Run history index maintenance off the block commit path. |
| `N42_HISTORY_INDEX_INTERVAL` | config | declared | - | internal/node | How often the deferred history backfiller folds the changesets. |
| `N42_HOTSTUFF_CATCHUP_GRACE_MS` | config | declared | - | internal/sync | Grace to withhold a lag-exactly-1 catch-up, in ms. |
| `N42_HOTSTUFF_LEADER_TENURE` | consensus | declared | - | internal/consensus/hotstuff | Consecutive views one validator leads before the rotation moves on. |
| `N42_INFLIGHT` | perf | int | 4 | internal/ethel/eldevp2p | Concurrent requests per peer of the eth-el downloader. |
| `N42_INGEST_WORKERS` | perf | declared | - | internal/api | Ingest worker count (0 = serial). |
| `N42_JOURNAL_TRACE` | diag | is1 | off | internal/consensus/hotstuff | Trace the vote journal. |
| `N42_KECCAK_ONEBLOCK` | perf | not0 | on | crypto | Default on: single-block Keccak-256 fast path. |
| `N42_LAG_EARLY_PUBLISH` | perf | optin | off | internal | Opt-in: an ahead run assembles its receipts beside the fold and publishes earlier. |
| `N42_LAG_EARLY_SNAPSHOT` | perf | optin | off | internal | The child starts at its parent's exec end. |
| `N42_LAG_GATE_APPLIED` | perf | defaulton | on | internal | Default on: the lag gate waits for the applied state. |
| `N42_LAG_HANDOFF_LEAN` | perf | is1 | off | internal | Lean hand-off of the lag pipeline (r68a). |
| `N42_LAG_PIPELINE` | perf | defaulton | on | internal | Default on: the lag pipeline. |
| `N42_LAG_RESULT_EARLY` | perf | optin | off | internal | Opt-in: publish the lag result early. |
| `N42_LAG_WRITE_ASYNC` | perf | defaulton | on | internal | Default on: the lag writer writes asynchronously. |
| `N42_LAG_WRITE_DEFER_BG` | perf | is1 | off | internal | Background writers yield the MDBX writer to the lag writer (r58a). |
| `N42_LAG_WRITE_DEFER_BG_IDLE_MS` | perf | millis | 1s | internal | Idle time after which background writers resume, in ms. |
| `N42_LAG_WRITE_DEFER_BG_MAX_BLOCKS` | perf | uint64 | 0 | internal | Blocks a background writer may be deferred (0 = unlimited). |
| `N42_LAG_WRITE_DEFER_BG_WAIT_MS` | perf | millis | 250ms | internal | Longest a background writer waits for the lag writer, in ms. |
| `N42_LAG_WRITE_EARLY_PREP` | perf | is1 | off | internal | A lag import prepares its write early (r67a). |
| `N42_LAG_WRITE_EARLY_PREP_DELAY` | perf | is1 | off | internal | Delay the early prepare (r68a). |
| `N42_LAG_WRITE_EARLY_PREP_SHARDS` | perf | raw | - | internal | Shards of the early prepare. |
| `N42_LAG_WRITE_PREPARE` | perf | defaulton | on | internal | Default on: the ordered writer prepares CommitBlock ahead. |
| `N42_LEADER_WRITE_AFTER_JOURNAL` | config | is1 | off | internal/consensus/hotstuff, internal/miner | The leader writes the block after the vote journal is durable. |
| `N42_LEADER_WRITE_AFTER_JOURNAL_TIMEOUT_MS` | config | declared | - | internal/miner | Timeout of the write-after-journal wait, in ms. |
| `N42_LEADER_WRITE_ASYNC` | perf | declared | - | internal/miner | The leader writes its sealed block asynchronously. |
| `N42_LEAFTRACE` | diag | declared | - | lib/trie | Dump every account leaf input of the hash builder. |
| `N42_LOG_COLOR` | diag | declared | - | log | Force ANSI colours in the log. |
| `N42_LOG_STDOUT` | diag | declared | - | log | Log to stdout even when it is not a terminal. |
| `N42_MAX_GOSSIP_MB` | config | declared | - | internal/p2p/encoder | Maximum gossip message size, in MiB. |
| `N42_MAX_PEERS` | config | declared | - | internal/devp2p | Peer limit of the devp2p server. |
| `N42_MDBX_COMMIT_TRACE` | diag | declared | - | lib/kv/mdbx | Log MDBX commit timings. |
| `N42_MDBX_DIRTY_GB` | perf | declared | - | internal/node | Dirty-page limit of the ChainDB, in GB. |
| `N42_MDBX_EARLY_WRITEBACK` | perf | declared | - | lib/kv/mdbx | MDBX early writeback of dirty pages. |
| `N42_MDBX_MAPSIZE_GB` | config | declared | - | internal/node | Map size of the ChainDB, in GB. |
| `N42_MDBX_SYNC` | config | declared | - | internal/node | MDBX durability mode. |
| `N42_MINER_ADOPT_APPENDS` | perf | is1 | off | internal | NewMinerRootComputer's own-block fast path. |
| `N42_MINER_FILL_GAS` | bench-only | declared | - | internal/miner | Gas the builder fills a block up to, instead of the header's gas limit. |
| `N42_MINER_PARALLEL_FILL` | perf | declared | - | internal/miner | Block-STM fill of the builder. |
| `N42_MINER_SENDER_ROTATE` | config | is1 | off | internal/miner/builder | Deterministic round-robin across senders whose head transactions tie. |
| `N42_MPT_VERIFY_INTERVAL` | perf | declared | - | internal/ethel/eldevp2p | Interval of the MPT verification of the eth-el downloader. |
| `N42_MV_PRESIZE` | perf | not0 | on | internal/parallel | Default on: presize the multi-version maps; 0 disables. |
| `N42_NOCOMMIT` | diag | declared | - | internal/api | Skip the commit of the engine state adapter (debug). |
| `N42_NONCETRACE` | diag | declared | - | internal | Trace every nonce increment in the given block. |
| `N42_NOOP_TRACER` | diag | is1 | off | internal/ethel | Run the no-op tracer on every transaction. |
| `N42_NOOP_TRACER_LEVEL` | diag | declared | - | internal/ethel | Level of the no-op tracer. |
| `N42_NO_HISTORY_INDEX` | config | declared | - | modules/state | Skip the history index. |
| `N42_NO_READCACHE` | diag | declared | - | internal/api | Bypass the cross-block hashed read cache. |
| `N42_ORPHAN_REINJECT` | config | optin | off | internal | Opt-in: reinject orphaned transactions after a reorg. |
| `N42_PARALLEL_TRACE` | diag | declared | - | internal/parallel | Trace the failed transactions of the wave executor. |
| `N42_PARALLEL_WORKERS` | perf | declared | - | internal | Block-STM worker count (default 32). |
| `N42_PEVM_BENCH` | bench-only | declared | - | internal/api | Shadow-run the Block-STM parallel EVM on this block's transactions and time it. |
| `N42_PEVM_LAZYCB` | bench-only | declared | - | internal/api | Lazy callback of the PEVM bench. |
| `N42_PEVM_TRACE` | diag | declared | - | modules/state | Per-failing-transaction diagnostics of the parallel executor. |
| `N42_PEVM_VTRACE_ADDRS` | diag | raw | - | modules/state | Address list for the multi-version view trace. |
| `N42_PEVM_WORKERS` | bench-only | declared | - | internal/api | Worker count of the PEVM bench. |
| `N42_PIPELINE` | perf | declared | - | internal/ethel/eldevp2p | Default on: download pipeline of the eth-el downloader; 0 reverts to serial. |
| `N42_PIPELINE_PROPOSE` | consensus | is1 | off | internal/consensus/hotstuff | A node holding the PrepareQC of its current view proposes the next block early. |
| `N42_POOL_PENDING_SNAPSHOT` | perf | declared | - | internal/txspool | Default on: Pending(false) reads the reorg-published snapshot. |
| `N42_PREHOOK_CUT` | perf | raw | - | modules/state | Shorter root preamble before the lag pipeline's pre-hook. |
| `N42_PROPOSAL_PUSH_GRACE_MS` | config | declared | - | internal/consensus/hotstuff | Grace before a proposal is pushed, in ms. |
| `N42_PROPOSE_BEFORE_WRITE` | perf | declared | - | internal/miner | With the early push on, the Proposal goes out before the block write. |
| `N42_PUSH_BEFORE_WRITE` | perf | declared | - | internal/miner | The leader direct-pushes a sealed block before WriteBlock. |
| `N42_QMDB_EVICT_LAG_BLOCKS` | config | declared | - | modules/state/commitment | Blocks of lag before QMDB evicts (0 = today's behaviour). |
| `N42_QMDB_INDEX_PRESIZE` | perf | declared | - | lib/qmdb | Default on: presize the QMDB index; false keeps grow-from-empty. |
| `N42_QMDB_PARALLEL_APPLY` | perf | declared | - | lib/qmdb | Worker count of ApplyOps' parallel pre-pass (0 = off). |
| `N42_QMDB_RELOAD_VERIFY` | diag | is1 | off | modules/state/commitment | Cross-check every fast QMDB reload against an independent rebuild. |
| `N42_QMDB_RETAIN_ENTRIES` | perf | raw | - | modules/state/commitment | Entries the QMDB root computer retains. |
| `N42_QMDB_SHARD_APPLY` | perf | declared | - | lib/qmdb | Worker count of ApplyOps' sharded path (0 = off). |
| `N42_QMDB_SINGLE_FOLD` | config | declared | - | internal | The leader's write path uses ComputeRootShared (single fold). |
| `N42_QMDB_TRUNC_INDEX` | perf | declared | - | lib/qmdb | New QMDB indexes store key prefixes. |
| `N42_RCPTDIFF` | diag | declared | - | internal/ethel/eldevp2p | Diff receipts against the reference. |
| `N42_RECEIPTS_RESULT_SPLIT` | perf | is1 | off | internal | Compute the receipts result (root, bloom, logs) beside the receipts. |
| `N42_RECEIPTS_RESULT_WORKERS` | perf | declared | - | internal | Parallel root/bloom worker count of the receipts result. |
| `N42_REORG_JOURNAL` | perf | declared | - | internal/ethel/eldevp2p | Recent committed blocks that keep an in-memory reverse diff. |
| `N42_REORG_LAG` | config | declared | - | internal/ethel/eldevp2p | Confirmation depth of live-follow in the eth-el downloader. |
| `N42_ROTOR_MAP_FIX` | config | not0 | on | internal/consensus/hotstuff | Default on: keep a validator's Rotor peer mapping after a direct send succeeds. |
| `N42_RW_HOLDER_DIAG` | diag | is1 | off | lib/kv/mdbx | Diagnose the holder of the MDBX read-write lock. |
| `N42_SENDER_CACHE_SLOTS` | perf | raw | - | common/transaction | Slots of the transaction sender cache. |
| `N42_SENDER_PREWARM_WORKERS` | perf | declared | - | internal | Worker count of the sender prewarm. |
| `N42_SENDER_RECOVER_WORKERS` | perf | declared | - | internal | Worker count of the sender recovery. |
| `N42_SENDER_TRACE` | diag | is1 | off | internal | Trace the source of recovered senders. |
| `N42_SEPARATE_VOTE_DB` | config | declared | - | internal/node | Keep votes in their own database. |
| `N42_SLOW_BLOCK_MS` | diag | declared | - | internal | Log blocks that take longer than this, in ms. |
| `N42_SNAP_ACC_CACHE` | perf | declared | - | internal/ethel/snapshotreader | Entries of the snapshot reader's account cache. |
| `N42_SNAP_MMAP` | perf | declared | - | internal/ethel, internal/ethel/snapshotreader | Default on: read snapshot files through mmap; 0 reads them into the heap. |
| `N42_SNAP_STO_CACHE` | perf | declared | - | internal/ethel/snapshotreader | Entries of the snapshot reader's storage cache. |
| `N42_STAGED` | config | declared | - | internal/ethel/eldevp2p | Staged catch-up mode of the eth-el downloader. |
| `N42_STATE_OBJECT_POOL` | perf | declared | - | modules/state | Recycle stateObject structs across blocks. |
| `N42_STATE_READ_QMDB` | config | declared | - | modules/state/commitment | QMDB state read mode. |
| `N42_STATE_WRITE_PREP_DEFER_LOADS` | perf | not0 | on | modules/state | Default on: defer the pending loads of the sharded prepared write. |
| `N42_STATE_WRITE_PREP_LOAD_PAR` | perf | raw | - | modules/state | Parallel pending loads for the sharded prepared write (r58a). |
| `N42_STATE_WRITE_PREP_SHARDS` | perf | raw | - | modules/state | Shards of the prepared write. |
| `N42_STATE_WRITE_QMDB_ONLY` | config | declared | - | modules/state/commitment | Account writes go to QMDB only. |
| `N42_STRESS_GASLIMIT` | bench-only | is1 | off | internal/consensus/misc, internal/miner | Lets the miner jump the block gas limit for a stress run. |
| `N42_SUBBATCH` | perf | declared | - | internal/ethel/eldevp2p | Sub-batch size of the staged eth-el catch-up. |
| `N42_SYSCALL_FINALIZE_ONCE` | perf | optin | off | modules/state | Prague block-end FinalizeTx work runs once. |
| `N42_TRACE_BLOCK` | diag | declared | - | internal/ethel | Block number the trace switches act on. |
| `N42_TRACE_OUT` | diag | raw | - | internal/ethel | Output file of the eth-el trace. |
| `N42_TRACE_PROD_PATH` | diag | is1 | off | internal/ethel | Trace on the production path. |
| `N42_TRACE_SKIP_OTHERS` | diag | is1 | off | internal/ethel | Skip transactions other than the traced one. |
| `N42_TRACE_STORCOLL` | diag | declared | - | modules/state/commitment | Trace the storage collector. |
| `N42_TRACE_STORCOLL_TOP` | diag | declared | - | lib/trie | Print the HashBuilder stack top when the storage collector finishes. |
| `N42_TRACE_TX` | diag | raw | - | internal/ethel | Transaction hash the trace switches act on. |
| `N42_TRANSFER_COMPONENT_WORKERS` | perf | raw | - | internal | Opt-in transfer component workers. |
| `N42_TRANSFER_DIRECT` | perf | not0 | on | internal | Default on: a Block-STM worker applies a fast-path transfer directly. |
| `N42_TRANSFER_FAST2` | perf | not0 | on | internal, internal/parallel | Default on: second transfer fast path. |
| `N42_TRANSFER_FAST_PATH` | perf | not0 | on | internal | Default on: transfer fast path. |
| `N42_TXERR` | diag | declared | - | internal/api | Log transaction errors of the engine state adapter. |
| `N42_TXINDEX_DIR` | config | declared | - | internal/node | Cold tx-hash to block index directory (txlookup segments). |
| `N42_TXINDEX_KEEP_BLOCKS` | perf | declared | - | internal/txindexer | Blocks the tx index keeps hot. |
| `N42_TXINDEX_SEAL_MIN_TX` | perf | declared | - | internal/txindexer | Minimum transactions before the tx index seals a segment. |
| `N42_TXINDEX_SEAL_WORKERS` | perf | declared | - | internal/txindexer | Worker count of the tx index sealing. |
| `N42_TXINDEX_TAIL` | config | declared | - | internal/txindexer | Tail tier of the tx index. |
| `N42_TXPOOL_ACCOUNT_QUEUE` | perf | declared | - | internal/txspool | Raises the per-account queue capacity of the pool. |
| `N42_TXPOOL_ACCOUNT_SLOTS` | perf | declared | - | internal/txspool | Raises the per-account slot capacity of the pool. |
| `N42_TXPOOL_GLOBAL_QUEUE` | perf | declared | - | internal/txspool | Raises the global queue capacity of the pool. |
| `N42_TXPOOL_GLOBAL_SLOTS` | perf | declared | - | internal/txspool | Raises the global slot capacity of the pool. |
| `N42_TXPOOL_NOLOCALS` | config | declared | - | internal/txspool | Treat RPC-submitted transactions as remote. |
| `N42_TXPOOL_READ_BUDGET` | perf | declared | - | internal/txspool | Concurrent txpool reads (default GOMAXPROCS clamped to [4, 64]). |
| `N42_TXROOT_BLAKE3_TIME` | bench-only | declared | - | internal | Overrides the chain's txRootBlake3Time on a bench chain. |
| `N42_TX_DECODE_POOL` | perf | not0 | on | internal/frame | Frame transactions decode into per-frame slabs. |
| `N42_TX_INGEST_HIGH_WATER` | config | declared | - | internal/api | Admission ceiling of the tx ingest (0 = no gate). |
| `N42_VOTE_DEFER_RETRY` | consensus | is1 | off | internal/consensus/hotstuff | Retry a vote deferred for availability instead of dropping it. |
| `N42_VOTE_JOURNAL_SEPARATE` | config | declared | - | internal/consensus/hotstuff | Journal votes in their own store. |
| `N42_VOTE_ON_AVAILABILITY` | consensus | declared | - | common/block | The availability vote. |
| `N42_WAVE_CPU_DIAG` | diag | is1 | off | internal/parallel | Measure each wave worker's thread CPU time. |
| `N42_WAVE_LPT` | perf | is1 | off | internal/parallel | Longest-processing-time sender-to-worker assignment. |
| `N42_WAVE_PARTITION_FAST` | perf | is1 | off | internal/parallel | Compute the LPT wave partition and steal chains faster (r65a). |
| `N42_WAVE_STEAL` | perf | is1 | off | internal/parallel | A worker that ran out of chains steals not-yet-started ones. |
| `N42_WRITE_PROBE` | diag | declared | - | lib/kv/mdbx | Write probe of the MDBX layer. |
