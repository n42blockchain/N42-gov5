# Test coverage campaign

Started 2026-10-02 (America/New_York). Branch `test/coverage-70`; per-agent branches `test/coverage-70-g<N>` merge into it.

## Baseline (2026-10-02 00:37 EDT)

`go test -short -cover -covermode=atomic ./...` with tags `nosqlite,noboltdb`, Go 1.26:

| metric | value |
|---|---|
| packages | 608 (579 instrumented) |
| statements | 211,489 |
| covered | 78,696 |
| **coverage** | **37.2%** |
| packages without any test file | 366 |

Target 70% = 148,042 covered statements, i.e. +69,346. Ranking by uncovered statements is in
`scratch/cov/rank.txt` (not committed); top of the list: internal/ethel 5,912, internal/api 3,886,
internal 3,043, internal/datc 2,924, internal/vm 2,595, cmd/n42 2,583, lib/state 2,544, internal/sync 2,266,
modules/state 2,195, generated gRPC stubs lib/gointerfaces/* 5,631 (0-1%), proto/*_pb 2,326.

Denominator notes: generated code (`lib/gointerfaces/*`, `proto/*_pb`, `*_gen.go`, `*.pb.go`) and vendored
post-quantum crypto internals (`crypto/dilithium/*/internal`, `crypto/kyber/*`, `crypto/csidh`) are ~14k statements
that no hand-written test should chase; `cmd/*` mains are ~7k. Both views (with and without them) are reported at
each checkpoint.

## Rules

- One commit per test file or small package; package tests pass and `go vet` is clean before each commit.
- English commit messages, no "claude" anywhere, no trailers.
- Test-only changes; untestable functions are listed, not patched.
- Tests run with `nice -n 19 -p 1 -short`, one package at a time: the box also runs the qs benchmark fleet.

## Wave 1 (dispatched 2026-10-02 01:05 EDT)

| group | packages |
|---|---|
| g1 | internal/api, modules/rpc/jsonrpc, internal/mcp |
| g2 | internal/vm, lib/trie, lib/rlp2, accounts/abi |
| g3 | modules/state, lib/state, modules/rawdb |
| g4 | internal/consensus/hotstuff, internal/txspool, lib/txpool, internal/cscompact |

## Wave 1 results

| group | package | before | after |
|---|---|---|---|
| g4 | internal/cscompact | 19.0 | 35.8 |
| g4 | internal/txspool | 44.3 | 57.7 |
| g4 | lib/txpool | 44.6 | 48.3 |
| g4 | internal/consensus/hotstuff | 62.0 | 64.3 |
| g2 | lib/rlp2 | 16.5 | 74.1 |
| g2 | accounts/abi | 30.8 | 59.6 |
| g2 | lib/trie | 46.7 | 56.8 |
| g2 | internal/vm | 50.0 | 50.5 |

Lesson: pure-helper tests exhaust quickly; the remaining mass (vm opcodes/precompiles, trie hashing, pool main loops,
hotstuff service) needs tests built on the packages' existing harnesses. Wave 2 assigns one or two packages per agent
with that instruction.

## Found while testing (not fixed; test-only campaign)

- `lib/rlp2/encodel.go` `EncodeString`: a 56-byte string takes the short-string branch (`> 56` instead of `>= 56`),
  emitting a non-canonical `0xB8` header; `String()` then rejects it.
- `lib/rlp2/encoder.go` `writeList`: long-list header (payload > 55 bytes) writes `0x00` as the length byte
  (`f8 00` for a 60-byte item instead of `f8 3f`); corrupt RLP. Reproduce:
  `NewEncoder(nil).List(func(i *Encoder) *Encoder { return i.Str(bytes.Repeat([]byte{1}, 60)) })`.
- `lib/rlp2/commitment.go` `EncodeByteArrayAsRlp`: for a single byte >= 0x80, `generateRlpPrefixLen(1)` returns 0 but
  one prefix byte is written, so the returned length undercounts by 1.
- `internal/cscompact/history_analysis.go` `ParseErigonBitmapValue`: a malformed 16-byte buffer makes
  `roaring64.Bitmap.UnmarshalBinary` panic (`makeslice: len out of range`) instead of falling through to the
  size-estimate fallback.

| g1 | internal/mcp | 6.5 | 75.7 |
| g1 | modules/rpc/jsonrpc | 5.8 | 51.8 |
| g1 | internal/api | 43.6 | 43.8 |
| g3 | modules/rawdb | 39.7 | 63.5 |
| g3 | modules/state | 55.3 | 57.2 |
| g3 | lib/state | 49.2 | 49.2 (aggregator needs a wired multi-domain setup) |

- `modules/rpc/jsonrpc/util.go` `UnmarshalText(h types.Hash, ...)` takes the hash BY VALUE, so
  `BlockNumberOrHash.UnmarshalJSON` returns nil error and a zero hash for every `blockHash` argument: any RPC
  call that selects a block by hash through this type silently resolves to the zero hash. Likely a real bug;
  the test documents the current behaviour (TestBlockNumberOrHashUnmarshalJSON).
| g6 | lib/commitment | 52.2 | 53.2 |
| g6 | modules/state/commitment | 51.2 | 53.1 |
| g6 | internal/mptproof | 32.5 | 38.1 |

- `lib/commitment/commitment.go` `Updates.TouchCode`: ORs `CodeUpdate` into Flags first, then tests `Flags == 0` to
  choose `DeleteUpdate` for empty code, so the delete branch is unreachable (TestUpdatesTouchCode pins current behaviour).
| g8 | internal/api | 43.8 | 48.9 |

- `internal/api/api_backend.go` `API.CurrentBlock()` and at least `EstimateGas` (pending default) and `BlobBaseFee`:
  a typed-nil `*block.Block` inside the `block.IBlock` interface passes the `== nil` check and the handler panics on
  `Header()`/`GasLimit()`. Any chain implementation that returns a nil concrete block crashes these RPC handlers.
| g5 | internal/vm | 50.5 | 55.6 (new bytecode execution harness exec_harness_test.go) |
| g7 | modules/rawdb | 63.5 | 77.1 |
| g7 | modules/state | 57.2 | 68.9 |
| g7 | internal/replay | 14.0 | 25.5 (the rest needs a real source+target datadir) |

- `modules/rawdb/accessors_chain_receipts.go` `ReadReceiptByTxHash`: scans `BaseTxId+i` for i in [0, TxAmount) although
  `WriteBody` reserves two extra slots and real transactions start at `BaseTxId+1`, so it returns the receipt one position
  off for every transaction after the first and misses the last one. Marked era-unaware / no production caller today;
  `TestReadReceiptByTxHash` pins current behaviour.
- Dead code found: `internal/replay` `BLSResealer.signMembers`, `modules/state` `Scheduler.beginExecution`.
| g9 | modules/state/commitment | 53.1 | 72.3 |
| g9 | lib/commitment | 53.2 | 66.7 |

- **MPT checkpoint does not round-trip** (`modules/state/commitment` `MPTRootComputer.SaveCheckpoint` ->
  `EncodeTrieState`/`RestoreTrieState`, backed by `lib/commitment/hex_patricia_hashed.go` `EncodeCurrentState`/`SetState`):
  encoding right after `ComputeRoot` and restoring on the same instance yields a different `RootHash()` (single-account
  trie: `b80146..` vs `69f2f8..`). The bulk-rebuild resume path cannot reproduce the root it checkpointed. Pinned weakly
  in `TestPersistentMPTRootComputerCheckpointRoundTrip`; needs the owner's investigation before any resume is trusted.
- `lib/commitment/commitment.go` `Updates.TouchPlainKeyNoDedup`: the `ModeUpdate` fallback passes a nil callback to
  `TouchPlainKey`, which calls it unconditionally for a new key -> nil-pointer panic on first use
  (`TestTouchPlainKeyNoDedupModeUpdateFallbackPanics`).
- `lib/commitment/hex_patricia_hashed.go` `resetForReuse`: pooled instances keep the CSV metrics prefix set by
  `EnableCsvMetrics`, so a later borrower can panic opening a stale path. Pool hygiene gap.
| g11 | crypto/sha3 | 0.0 | 99.4 |
| g11 | crypto/csidh | 0.0 | 74.0 |
| g11 | crypto/bls | 22.8 | 79.8 |
| g11 | crypto/bls12381 | 49.3 | 81.0 |
| g11 | accounts/abi/bind | 14.7 | 48.1 |
| g11 | conf | 55.6 | 77.9 |
| g11 | params | 64.6 | 87.7 |
| g11 | accounts | 37.3 | 97.3 |

- `crypto/bls12381` `Engine.AddPairInv` negates its G1 argument in place; reusing the same point variable across
  `AddPair`/`AddPairInv` silently corrupts the pairing input. Document or copy internally.
| g10 | internal/tracers | 11.7 | 72.8 |
| g10 | internal/tracers/native | 11.1 | 72.3 |
| g10 | internal/tracers/logger | 8.1 | 84.6 |
| g10 | internal/tracers/js | 45.1 | 65.4 |
| g10 | internal/consensus/apos | 13.6 | 33.9 (snapshot store table not registered in memdb.NewTestDB) |
| g10 | internal/consensus/apoa | 8.7 | 33.2 (same) |
| g10 | internal/miner | 16.1 | 17.1 (worker/miner loops need a live chain) |

- `internal/tracers/native/call_flat.go` `flatCallTracer.Stop()` forwards to the embedded callTracer but `GetResult`
  reads its own never-set `reason`, so `trace_block`/`trace_transaction` never surface an interruption
  (`TestFlatCallTracerStopDoesNotSurfaceReason`).
- Test-harness gap (non-test change, not made): the `poaSnapshot` table is not registered in `memdb.NewTestDB`, which
  blocks the apos/apoa `snapshot -> verifySeal` pipeline and most of their APIs (`modules/rawdb/accessors_test.go:534`
  skips for the same reason). Registering it would unlock ~1,500 statements.

## Checkpoint 1 (2026-10-02, after merging g1-g11, 10 groups, ~150 commits)

`go test -short -cover ./...`, 0 failing packages:

| view | covered / statements | coverage |
|---|---|---|
| whole module | 88,090 / 211,489 | **41.7%** (baseline 37.2%) |
| excluding generated stubs, vendored PQ crypto, cmd mains | 85,156 / 160,154 | **53.2%** |

Running: g12 (p2p subpackages, txlookup, mpttrie), g13 (mdbx, qmdb, etl, jmt, bmt, transaction, block),
g14 (avm, distributed, mev, deferred, bundler, metrics), g15 (core, sync).
| g13 | lib/etl | 59.1 | 80.7 |
| g13 | lib/bmt | 61.2 | 86.1 (lib/bmt/store 0 -> 89.3) |
| g13 | lib/jmt | 68.2 | 83.3 |
| g13 | common/transaction | 79.8 | 86.6 |
| g13 | common/block | 78.8 | 84.1 |

- **`lib/bmt/tree.go` `PutBatch`/`insertBatch` drops keys**: batching two or more entries into an empty tree leaves the
  second key unreadable (`Get` -> ErrNotFound) and yields a root different from sequential `Put`s, although BMT roots
  are meant to be insertion-order independent. Repro in `lib/bmt/tree_batch_test.go`. Check every production caller of
  `PutBatch` before trusting a BMT root built through it.
- `lib/etl` `NewCollectorFromFiles` leaves `fileDataProvider.wg` nil, so `Close()`/`Dispose()` on a restored collector
  panics inside `errgroup.Wait()` (`lib/etl/collector_more_test.go`).
| g14 | internal/metrics | 20.0 | 99.2 |
| g14 | internal/mev | 55.4 | 85.4 |
| g14 | internal/bundler | 65.0 | 93.4 |
| g14 | internal/deferred | 46.3 | 86.0 |
| g14 | internal/distributed/compute/inference | 68.8 | 93.0 |
| g14 | internal/distributed/messaging | 58.8 | 67.8 (peer handler needs a libp2p host pair) |
| g14 | internal/distributed/storage/torrent | 40.8 | 44.9 (real anacrolix client opens sockets) |

- **`internal/deferred/deep_pipeline.go` `DeepPipeline.Reset()` wedges the pipeline**: it cancels and waits but never
  clears `running`, so the following `Start` is a no-op and `ctx`/`cancel` keep pointing at the cancelled context;
  `SubmitBlock` then races a fresh channel against a closed `ctx.Done()`. A reorg-recovery Reset can permanently wedge
  the deep pipeline (`TestDeepPipeline_Reset`).
| g15 | internal | 35.0 | 35.3 |
| g15 | internal/sync | 14.7 | 22.0 |
| g15 | internal/sync/initialsync | 3.6 | 29.6 |
| g15 | internal/sync/snapsync | 39.2 | 44.0 |

- Harness gap (one-time investment that would unlock most of internal/sync, initialsync, snapsync): a lightweight
  in-repo fake for `network.Stream` (net.Pipe-backed) and a builder for `peers.Status` / `p2p.P2P` with real peer records.
- `internal/sync` `TestGraceCatchUpDeferredAndResolvedByNormalPath` (S63's own test) fails under `-race`; whether the race
  is in the test or in `rpc_catchup.go`'s grace path is being checked separately (S63 is adopted in the fleet).
| g17 | internal/avm/types | 7.9 | 95.4 |
| g17 | internal/avm/common | 48.2 | 97.2 |
| g17 | internal/avm/common/compiler | 0.0 | 52.6 (the rest shells out to solc/vyper) |
| g17 | internal/avm/abi | 79.5 | 89.8 |
| g17 | internal/avm/rlp | 88.4 | 94.4 |

- `internal/avm/rlp/decode.go` `IsInvalidRLPError(nil)` panics (no nil guard before `err.Error()`).
| g18 | internal | 35.3 | 47.5 (InsertChain/Start/ProcessParallel need a full engine + EVM harness) |
| g16 | lib/kv/mdbx | 63.7 | 83.8 |
| g16 | lib/qmdb | 79.7 | 88.9 |

- `lib/kv/mdbx/kv_mdbx_opts.go` `MdbxOpts.InMem(dir)` unconditionally resets `label` to `kv.InMem`, so
  `.Label(kv.ChainDB).InMem(dir)` silently loses the label (call-order footgun).
| g19 | lib/rlphacks | 0.0 | 84.0 |
| g19 | lib/common/dbg | 0.0 | 75.3 |
| g19 | lib/metrics | 0.0 | 84.1 |
| g19 | lib/kv/layered | 60.9 | 87.2 |
| g19 | lib/jmt/store | 26.0 | 86.5 |
| g19 | lib/diagnostics | 36.3 | 63.7 |

- `lib/rlphacks` `RlpEncodedBytes.DoubleRLPLen()` / `EncodeByteArrayAsRlp()` under-report the length by one byte for a
  single byte >= 0x80 (`generateRlpPrefixLen` ignores the first byte); the written bytes are right, the count is wrong.
- `lib/diagnostics` `SetFillDBInfo` appends a stage only when the list is nil, so every later stage with a new name is
  silently dropped.
| g12 | internal/p2p/netutil | 0.0 | 94.0 |
| g12 | internal/p2p/enr | 1.7 | 88.0 |
| g12 | internal/p2p/peers | 0.0 | 91.7 |
| g12 | internal/p2p/enode | 37.2 | 79.1 |
| g12 | internal/p2p/discover | 0.0 | 52.5 (v4 over an in-memory dgram pipe; discv5 untouched) |
| g12 | internal/mpttrie | 47.2 | 80.9 |
| g12 | internal/txlookup | 46.4 | 67.4 |

- `internal/p2p/enode` `filterIter.Next()` advances once and reports whether that single node passed, unlike upstream
  `Filter` which skips until a match; callers that expect upstream semantics will see non-matching nodes.
- `internal/p2p/enr` `IP.ENRKey()` picks "ip"/"ip6" from the value, so a zero `IP` used as a `Load` target asks for
  "ip6" and misses a stored "ip" entry; load through `IPv4`/`IPv6` instead.
| g20 | internal/ethel | 35.3 | 39.5 (first pass; coldseed 11.7 -> 85.8, eldevp2p 0 -> 100, publicrpc 18.3 -> 24.6) |
| g21 | internal/datc | 56.4 | 57.3 (helpers only; second pass g25 running) |
| g24 | internal/ethel | 39.5 | 40.3 (second pass weak; ethel now split per file, g26 = body codecs) |
| g23 | internal/consensus/hotstuff | 64.3 | 75.6 (service lifecycle needs a fake pubsub harness) |
| g25 | internal/datc | 57.3 | 60.1 (run* workers need an upstream freezer-input fixture; g28 builds it) |
| g22 | lib/kv/remotedbserver | 19.0 | 67.0 |
| g22 | lib/kv/remotedb | 5.8 | 78.5 |
| g22 | lib/direct | 0.0 | 49.3 (every hand-written file at 100%; the rest is mockgen output) |
| g22 | internal/node | 18.1 | 26.3 (the rest needs Start() or a full p2p.P2P fake) |

- `lib/kv/remotedbserver` `Range`/`IndexRange` pagination is dead: the server loop never checks the limit and
  `lib/kv/mdbx` `cursor2iter.HasNext()` turns false when its own limit counter hits zero, so `NextPageToken` is never
  produced through the public API (`TestRangePaginationNeverTriggersViaIntegration`).
- `lib/direct/eth_backend_client.go` `SubscribeLogs`: the server goroutine's deferred close of `chRecv` races a concurrent
  client `Send` on the same channel (reproducible under `-race`).
- `lib/direct` `SubscribeLogsStreamC`/`SubscribeLogsStreamS` do not override `Context()`, so calling it panics on the nil
  embedded gRPC stream.
| g26 | internal/ethel (body_compact/frames/wire slice) | 40.3 | 43.9 (every function in the three files >= 80%) |

- **`internal/ethel/body_compact.go` `BodyCompactStage.Run` corrupts a store whose only segment is partial on resume**:
  when `existingSegments` rewinds to 0 the `headFile`/`headSize` recompute block is skipped, the data file is reopened
  without `O_TRUNC` and appended at its real end, but the new index entry records offset 0, so readers see the stale
  first-run bytes. The >= 2-segment case is unaffected. Reproduced by the agent, not committed.
- `BodyCompactReader.Close()` does not join the frame-ahead goroutine (`startFrameAhead`), so a framed random read
  followed by Close races the background file reads against the handle teardown (`-race` confirmed).
- `startFrameAhead` calls `dataFile(0, seg)` and discards the result before the goroutine looks up the real file number;
  harmless with one data file, could trigger a spurious cold-resolver call under multi-file rotation.
| g27 | internal/ethel (hashstate/dict/codec/catch_up slice) | 40.3 | 44.1 (all three HPH bootstrap variants agree with the oracle root) |
| g30 | internal/ethel/modestate | 67.4 | 97.7 |
| g30 | internal/ethel/catchup | 66.0 | 95.7 |
| g30 | internal/ethel/bootstrap | 64.7 | 94.1 |
| g30 | internal/ethel/coldresolve | 58.4 | 87.2 |
| g30 | internal/ethel/stateless/serve | 55.0 | 83.7 |
| g30 | internal/ethel/engineapi | 51.1 | 82.3 |
| g30 | internal/ethel/publicrpc | 24.6 | 74.9 |
| g30 | internal/ethel/fetch | 56.6 | 60.2 (torrent and webrtc fetchers need live peers) |
| g29 | internal/ethel (executor/cs_freezer/codes readers/compare_mdbx slice) | 40.3 | 47.3 (synthetic geth-format freezer built in-process) |

- `internal/ethel/executor.go:229` `Executor.SetSenderFreezer(nil)` panics (calls `f.Table` on a nil freezer); every other
  `Set*` setter accepts nil as "disable".
| g31 | internal/sync | 22.0 | 27.6 (fake_stream_test.go + fake_p2p_test.go harness landed; handlers continue in g34) |
| g32 | internal/p2p | 12.9 | 55.5 (mocknet two-peer harness in testservice_test.go) |
| g28 | internal/datc | 60.1 | 71.1 (run_fixture_test.go synthesizes the headerc freezer and compacted acctcs/storcs inputs) |
| g36 | lib/kv/bitmapdb | 10.3 | 83.6 (rest of the group re-dispatched as g37) |

- `lib/kv/bitmapdb` `WalkChunkWithKeys64` with a tiny sizeLimit panics (`Maximum()` on an empty tail chunk after a cut,
  bitmapdb.go:274).
| g34 | internal/sync | 27.6 | 46.0 (catchUpTo/catchUpRange end to end over the pipe harness) |
| g33 | internal/consensus/apoa | 33.2 | 71.8 (snapshot table via modules.N42Init + swapped ChaindataTablesCfg in a test helper) |
| g33 | internal/consensus/apos | 33.9 | 60.4 |
| g33 | internal/miner | 17.1 | 22.4 (worker/miner loops are integration weight) |

- `internal/consensus/apos/consensus.go` `AccumulateRewards`: when `number == rewardEpoch` the backward walk underflows
  past block 0 (`SubUint64` wraps to MaxUint64) and fails with "block not found" instead of stopping.
- `apos.API.GetSigner` and `apoa.API.GetSigner`: nil-pointer panic on a nil `BlockNumberOrHash` with no current block
  (the missing-block error path calls `.String()` on the nil pointer).
| g39 | internal/p2p/discover/v5wire | 19.1 | 82.6 (full discv5 handshake over mclock.Simulated) |
| g35 | modules/changeset | 0.0 | 83.8 |
| g35 | lib/kv | 0.0 | 86.2 |
| g35 | common/utils | 37.5 | 81.9 |
| g35 | lib/chain | 15.3 | 93.1 |
| g35 | internal/consensus/misc | 36.3 | 78.5 |
| g35 | lib/common | 0.3 | 86.7 |

- `lib/common` `Bytes4/Bytes48/Bytes64/Bytes96.SetBytes` hard-code the 32-byte `length.Hash` (copy of `Hash.SetBytes`):
  Bytes4 panics on almost any input, the others panic for inputs between 32 bytes and their capacity. No production
  caller today (`TestG35BytesNSetBytesIsBroken` pins the panic).
| g38 | internal/sync/initialsync | 29.6 | 64.0 |
| g38 | internal/sync/snapsync | 44.0 | 57.2 |

- `internal/sync/initialsync/blocks_fetcher.go` `timeToWait()` multiplies `int64(timeTillEmpty) * blocksNeeded` without
  overflow protection; a huge batch request yields a near-zero or negative wait and defeats the rate limiter.
  `waitForBandwidth`'s `uint64(rem) >= count` wraps when `rem` is transiently negative (LeakyBucket.Count rounds up).
| g41 | internal/api/filters | 8.5 | 70.1 |
| g41 | lib/rlp | 67.2 | 95.9 |
| g41 | log | 37.4 | 76.8 |
| g41 | common/avmtypes | 8.0 | 95.6 |
| g41 | common/metrics | 16.7 | 78.7 |
| g41 | internal/metrics/prometheus | 15.5 | 81.3 |
| g41 | lib/types | 62.6 | 84.9 |
| g41 | lib/commitment/trie | 36.7 | 90.6 |
| g42 | internal/api | 48.9 | 57.0 (the rest needs an executed block with transactions; g44 builds that harness) |

- `internal/api/ens_api.go` `ethCall` is a permanent stub returning an error, so every ENS RPC (ResolveName,
  ResolveAddress, GetContentHash, GetTextRecord, GetOwner, GetResolver) always fails in production. Functional gap, not
  a test gap.
