# S79/S79b: CPU profile of the follower import path on full blocks

## S79 blocker (kept for context)

The original S79 attempt (offline `qs-replay` against reflinked qs-node0/
qs-node3 datadirs) could not reach the heavy >=100k-tx blocks: the fleet kept
producing empty blocks for thousands of blocks after the 19:31-19:57 heavy
round ended, pushing the target blocks (13656001-13669276) 4,909-18,184 blocks
behind the stored head (13674185) -- far outside `qs-replay`'s 256-block undo
window. No profile was taken under S79.

S79b (board row, docs/QS_QUEUE.md) fixed this by taking a **live** CPU profile
from node3's pprof port during round 35zzzbe, keyed on the first
`txs>=100000` "parallel block" log line, instead of trying to replay after
the fact. That profile is what this report covers.

## Round 35zzzbe results

B1 win1/win2 140,388/136,826, B2 145,473/134,419 (B-mean 139.3k, inside the
136-146k same-config band), A 69.3k/70.9k. Config: n42-r110, interval-ms 250,
`N42_PARALLEL_WORKERS=32` on every leg, no A/B lever change this round.

Profiles in `/data/blockchain/gov5-work/scratch/s79/`:
- `node3-B1.pprof` (231 KB) -- 30s CPU profile captured at 21:59:32 EDT during
  B1's heavy window. **Valid**: 374.32s of samples over 30s wall (1247.70% —
  i.e. ~12.5 of the box's cores busy on node3 alone), dominated by real
  exec-wave work (ecrecover, sender recovery, parallel apply). Used for all
  analysis below.
- `node3-B1-heap.pprof` (133 KB) -- heap snapshot taken 22:00:08 EDT, ~36s
  after the CPU capture started. Valid, used for the heap section below.
- `node3-B2.pprof` (5 KB) / `node3-B2-heap.pprof` (15 KB) -- **empty/idle
  captures**. The CPU profile shows only 3290ms of samples at 10.97%
  utilization (one active core, intermittently), and its entire `-top -cum`
  is `NewNode -> QMDBRootComputer.LoadFrom -> Tree.loadTwigFrom` -- i.e. this
  capture landed during node3's QMDB tree load at startup/reconnect, not
  during live block processing. It is not usable for exec-wave analysis and
  is excluded below.

Binary: `/data/blockchain/gov5-work/n42-r110` (symbols embedded; used directly
with `go tool pprof`, no separate debug info needed).

## 1. Total CPU and category shares (node3-B1.pprof)

Total sampled: **374.32 CPU-seconds** over a 30s wall capture (~12.5 cores
busy on average on this one follower).

| Category | CPU-s | % of total |
|---|---|---|
| Hashing/signature (ecrecover via cgocall, keccakF1600, blst BLS) | ~199.8s | 53.4% |
| EVM core / state transition (TransitionDb, ApplyMessageWithFeeSink, preCheck) | ~23.1s | 6.2% |
| MVS/scheduler (internal/parallel: executeSingle/executeParallel, ReadAccount, Validate, readValid) | ~55.6s (within exec's 45.5s cum for executeSingle + ~10s Validate/readValid outside it) | ~14.9% |
| IBS/journal (modules/state: IntraBlockState.getStateObject, FinalizeTx, foldBalanceIncrease, journal.push) | ~16s | ~4.3% |
| Allocation/GC (mallocgc, gcBgMarkWorker, memclr, growslice-family) | ~18.2s (mallocgc) + ~13s (gcDrain/gcBgMarkWorker) + ~2s (memclr) = ~33s | ~8.8% |
| State reads (qmdb.mapIndex.Get, qmdb.Tree.GetVia, QMDBStateReader, mdbx cursor) | ~4-7s (mostly nested inside MVS.ReadAccount, counted above where overlapping) | ~1-2% incremental |
| Encoding (rlp decode, DecodeEthereumTransaction, compact codecs) | ~14.3s (DecodeEthereumTransaction cum) | ~3.8% |
| sync/locks (atomic.Int32.Add counters, not futex/semacquire -- none of those appear in top nodes) | ~7.5s (atomic counters only) | ~2.0% |
| networking/gossip (libp2p, pubsub, snappy) | negligible in this capture (<0.5s, only seen in the applyMVSToIBS focus as an unrelated concurrent stream handler) | <0.2% |
| other (recsplit, cgocall for mdbx puts/commits, misc runtime) | remainder | ~remainder |

Dominant single driver: **secp256k1 ecrecover via cgo** (`runtime.cgocall` ->
`_Cfunc_secp256k1_ext_ecdsa_recover`) at 189.13s cum, 50.6% of all samples --
this single cgo call is more than half the follower's CPU budget for the
30s window and swamps every other category, including the actual exec wave.

## 2. Top-level views

### `-top -cum` (top 40, abridged to the notable entries)

```
flat%   cum%    cum      function
0.13%  55.66%  208.34s  common/transaction.Sender
0.075% 53.99%  202.11s  common/transaction.RecoverSenderDeduped
0.008% 53.38%  199.82s  internal/ingest.(*Server).hintWorker
0.0053 53.16%  198.98s  common/transaction.RecoverOnPool
0.011% 53.08%  198.68s  internal/ingest.(*Server).hintWorker.func1
0.021% 52.60%  196.90s  common/transaction.londonSigner.Sender
0.027% 52.58%  196.82s  common/transaction.eip2930Signer.Sender
51.43% 51.72%  193.30s  runtime.cgocall
0.021% 51.46%  192.61s  common/transaction.recoverPlainRS
0.011% 50.65%  189.60s  crypto.Ecrecover
0.0027 50.64%  189.56s  erigontech/secp256k1.RecoverPubkey
12.17% 12.17%   45.54s  internal/parallel.(*Executor).executeParallel.func1
0.29%  12.14%   45.45s  internal/parallel.(*Executor).executeSingle
0.019% 10.18%   38.10s  internal/parallel.(*Executor).exec
0.032% 10.16%   38.03s  internal.(*StateProcessor).runParallel.func2
0.11%   8.59%   32.17s  internal.parallelApplyTx
      6.21%   23.26s  internal.ApplyMessageWithFeeSink
0.37%   6.17%   23.10s  internal.(*StateTransition).TransitionDb
2.89%   5.08%   19.01s  common/transaction.senderCacheGet
0.33%   4.87%   18.23s  runtime.mallocgc
0.096%  4.58%   17.15s  common/transaction.CachedSender
0.096%  4.24%   15.87s  modules/state.(*IntraBlockState).getStateObject
0.011%  3.83%   14.35s  common/transaction.DecodeEthereumTransaction
```

### `-top` flat (top 40, abridged)

```
flat     flat%  function
192.53s  51.43% runtime.cgocall
 10.82s   2.89% common/transaction.senderCacheGet
  8.36s   2.23% golang.org/x/crypto/sha3.keccakF1600
  7.49s   2.00% sync/atomic.(*Int32).Add
  6.07s   1.62% encoding/binary.littleEndian.Uint64
  5.79s   1.55% runtime.spanClass.sizeclass
  5.14s   1.37% memeqbody
  5.00s   1.34% runtime.memmove
  4.88s   1.30% internal/runtime/maps.ctrlGroup.matchH2
  4.20s   1.12% internal/runtime/syscall/linux.Syscall6
  3.49s   0.93% common/transaction.(*Transaction).Hash
  3.33s   0.89% lib/recsplit.(*RecSplit).recsplit
  2.97s   0.79% internal/parallel.(*MVS).ReadAccount
  2.22s   0.59% runtime.tryDeferToSpanScan
  2.09s   0.56% internal/parallel.(*MVS).ReadAccount.func1
  1.96s   0.52% runtime.memclrNoHeapPointers
  1.93s   0.52% internal.applySenderHints.func1
  1.40s   0.37% internal.(*StateTransition).TransitionDb
  1.32s   0.35% runtime.mapaccess2
  1.22s   0.33% runtime.mallocgc
  1.08s   0.29% internal/parallel.(*Executor).executeSingle
```

## 3. Focused views

- `-focus=ProcessParallel`: only 0.71% of total samples fall inside
  `ProcessParallel`'s call subtree in this capture; the window captured was
  dominated by sender-recovery hint workers (`internal/ingest.hintWorker`,
  concurrent with but outside `ProcessParallel` proper) rather than the
  parallel-exec call path itself. Within it: `runParallel` (0.87% cum),
  `MVS.ApplyAll` (0.085%), `FinalizeTx` (0.27% cum), `foldBalanceIncrease`
  (0.16% cum).
- `-focus=parallelApplyTx`: 5.34% of total. Inside: `TransitionDb` (6.17%
  cum relative to full total), `MVS.ReadAccount` (1.15%), `preCheck`
  (1.27%), `getStateObject` (4.05% cum), `qmdb.Tree.GetVia` (1.11% cum),
  `AsMessage` (0.69% cum). `internal/parallel_processor.go:659`.
- `-focus='Finalize|FinalizeTx|balanceInc|ProcessExecutionBlockEnd'`: 1.18%
  of total. Inside: `sortedAddresses[int]` (0.22% cum) -- a sort over dirty
  addresses on every finalize, `updateAccountWithWipe` (0.26% cum),
  `pendingIncreaseAddrs` (0.11% cum), `FinalizeTx` itself (1.31% cum),
  `foldBalanceIncrease` (0.072% cum). `modules/state/intra_block_state.go:67`
  (`pendingIncreaseAddrs`), `:1027` (`foldBalanceIncrease`), `:1269`
  (`FinalizeTx`).
- `-focus='ComputeRoot|qmdb'`: 2.01% of total. Inside: `qmdb.mapIndex.Get`
  (1.06% cum), `qmdb.Tree.GetVia` (1.38% cum), `QMDBStateReader.qmdbAccount`
  (1.81% cum), `LookupSource.Get` (1.45% cum), `blake3.Sum256/Sum512` hashing
  (~0.28% cum combined), `mdbx.Cursor.Get` (0.35% cum). Root computation
  itself is cheap relative to the account-read path that feeds it.
- `-focus=applyMVSToIBS`: 0.14% of total -- `internal/parallel_processor.go:739`.
  `MVS.ApplyAll` (0.1% cum), `journal.push` (0.016% cum). This is one of the
  cheapest phases sampled; it is not a meaningful cost center in this window.

### `-peek` on top 3 flat functions

- **`runtime.cgocall`** (51.43% flat): 97.84% of its time goes to
  `secp256k1._Cfunc_secp256k1_ext_ecdsa_recover` (the ecrecover C call); the
  remainder is mdbx cursor get/put/commit (~0.68%+0.59%+0.24%) and blst BLS
  pairing calls for vote/signature verification (~0.34%+0.16%+0.15%+...).
  Essentially all cgocall cost in this window is ecrecover.
- **`common/transaction.senderCacheGet`** (2.89% flat): called from
  `CachedSender` (64.91% of its cum) and `Sender` (35.09%); internally it
  calls `senderCacheSlots` (32.19%) and does a `memeqbody` comparison
  (10.63%) -- a linear/associative probe over cache slots per lookup.
  `common/transaction/sender_cache.go:75` (`senderCacheSlots`), `:132`
  (`senderCacheGet`).
- **`golang.org/x/crypto/sha3.keccakF1600`** (2.23% flat): 100% of its
  callers go through `sha3.(*state).permute`, which backs every
  `Transaction.Hash()` / RLP-hash call. Pure, unavoidable per-tx hashing
  cost outside of the cache.

## 4. Heap profile (node3-B1-heap.pprof)

### Top 15 inuse_space

```
741.71MB 17.26%  lib/qmdb.newMapIndexSized
496.04MB 11.54%  common/transaction.senderCachePut
366.56MB  8.53%  libp2p/go-buffer-pool.(*BufferPool).Get
353.51MB  8.23%  common/rlp.decodeUint256
283.03MB  6.59%  common/transaction.decodeEthereumTransaction (912.07MB cum)
260.37MB  6.06%  internal/txlookup.(*Tail).Add
241.03MB  5.61%  common/transaction.DecodeEthereumTransaction (1202.59MB cum)
217.52MB  5.06%  common/transaction.NewTxOwned
200.13MB  4.66%  internal/parallel.NewReadWriteSet
128.00MB  2.98%  common/transaction.init.0
 76.53MB  1.78%  internal.parallelApplyTx
 75.50MB  1.76%  common/transaction.(*Transaction).Hash
 63.55MB  1.48%  internal/sync.(*rawSSZBytes).UnmarshalSSZ
 63.00MB  1.47%  common/transaction.Sender (526.54MB cum)
 61.18MB  1.42%  lib/etl.(*sortableBuffer).Put
```

### Top 15 alloc_space (cumulative allocation volume over the run, not resident)

```
 3.27GB  5.82%  internal.parallelApplyTx (10.02GB cum)
 2.92GB  5.18%  common/transaction.decodeEthereumTransaction (7.53GB cum)
 2.76GB  4.92%  libp2p/go-buffer-pool.(*BufferPool).Get
 2.16GB  3.83%  modules/state.(*journal).push
 2.12GB  3.76%  common/rlp.decodeUint256
 1.45GB  2.58%  internal/p2p.MsgID
 1.40GB  2.49%  common/transaction.DecodeEthereumTransaction (9.22GB cum)
 1.33GB  2.37%  lib/etl.(*sortableBuffer).Put
 1.33GB  2.37%  google.golang.org/protobuf/internal/impl.consumeBytes
 1.33GB  2.37%  modules/state.(*IntraBlockState).setStateObject (1.76GB cum)
 1.25GB  2.22%  common/transaction.NewTxOwned
 1.17GB  2.08%  encoding/json.(*Decoder).refill
 1.14GB  2.03%  internal/parallel.(*MVS).Write
 1.07GB  1.89%  modules/state.(*IntraBlockState).Reset (1.28GB cum)
 1.00GB  1.79%  internal/parallel.(*ReadWriteSet).MarkBalanceInsensitive
```

`qmdb.newMapIndexSized` dominates resident heap (741MB) -- this is the live
MVS/QMDB index structure, expected to be large and long-lived, not a leak.
`senderCachePut` at 496MB resident is the sender LRU cache holding entries
for ~163k tx/block; this is the memory price of avoiding re-recovery (see
below) and is a reasonable trade given ecrecover's cost.

## 5. Ranked removable/parallelizable costs

### Exec wave (per-tx worker cost; fleet shows ~58us/tx: 32 workers x 294ms / 163k tx)

| Rank | Function (file:line) | CPU-% of total | Est. ms per 163k-tx block on critical path | Fix kind |
|---|---|---|---|---|
| 1 | `crypto.Ecrecover` / `secp256k1._Cfunc_secp256k1_ext_ecdsa_recover` via `common/transaction.recoverPlainRS` (`common/transaction/recover_deduped.go:42` call site; crypto in `crypto/signature_cgo.go:37`) | 50.6% cum | Dominant: at 50.6% of 294ms exec-wave budget, roughly **149ms/block** is ecrecover. This is the single largest cost in the whole exec wave by a wide margin. | **avoid work / cache**: `RecoverSenderDeduped` + `senderCacheGet`/`CachedSender` already dedupe and cache recovered senders across re-validation attempts (good), but every *first-seen* tx still pays one full secp256k1 recovery. The remaining lever is upstream: `internal/ingest.(*Server).hintWorker` (`internal/ingest/server.go:111`) is already recovering senders as a background hint pool before the exec wave touches them -- confirm hint-pool sizing/parallelism is saturating all idle cores ahead of the block, since recovery is the bottleneck and is trivially parallel (no cache can remove the first recovery, but it can be moved fully off the critical path and sharded wider). |
| 2 | `common/transaction.senderCacheGet`/`senderCacheSlots` (`common/transaction/sender_cache.go:75,132`) | 5.08% cum (2.89% flat) | ~15ms/block | **cache / batch**: `senderCacheSlots` does a linear/associative probe (`memeqbody` at 10.6% of this function's time) per lookup; worth checking slot-count vs. working-set size (163k tx/block) -- if collisions are frequent, widening associativity or switching to a direct hash-indexed slot would cut the probe cost without changing the cache's hit-rate benefit. |
| 3 | `golang.org/x/crypto/sha3.keccakF1600` via `Transaction.Hash()` (`common/transaction/transaction_signing.go` callers) | 2.52% cum (`Transaction.Hash` cum) | ~7ms/block | **avoid work / cache**: hash is recomputed per `Sender()`/`Hash()` call; verify `Transaction.Hash()` memoizes once per tx object rather than recomputing on every signer lookup -- if each of 163k tx triggers more than one keccak-over-full-tx-bytes call during the wave, caching the hash value on first compute removes duplicate work. |
| 4 | `internal/parallel.(*MVS).ReadAccount` / `.Validate` / `readValid` (`internal/parallel_processor.go` + `internal/parallel/`) | ~3.5-6% cum combined | ~10-18ms/block | **allocate less**: `ReadAccount.func1` and the MVS read path allocate per-access (newobject calls visible in `-focus=parallelApplyTx`); a pooled/reused read-result struct would cut `mallocgc`+GC pressure, which is already ~8.8% of total CPU. |
| 5 | `runtime.mallocgc` / `gcBgMarkWorker` (exec-wave allocation, notably `decodeEthereumTransaction`/`DecodeEthereumTransaction` at 1.2GB cum alloc, `NewTxOwned`) | ~8.8% combined (mallocgc+gcDrain) | ~26ms/block | **allocate less**: tx decode (`common/transaction/*.go`) is the single biggest alloc source outside parallelApplyTx itself (1.2GB cum in this 30s window); pooling decode buffers or decoding into a reused struct would shrink both mallocgc time and GC background-mark time. |

### Serial post-wave (fee credits, FinalizeTx pre-fold over ~23k recipients, applyMVSToIBS, root)

| Rank | Function (file:line) | CPU-% of total | Est. ms per 163k-tx block on critical path | Fix kind |
|---|---|---|---|---|
| 1 | `modules/state.sortedAddresses[int]` inside FinalizeTx/finalize path (`modules/state/intra_block_state.go` -- called from `pendingIncreaseAddrs` at `:67` and finalize helpers) | 0.22% cum | ~0.6-1ms/block, but runs on the *serial* critical path (finalize~113-117ms window per the task's own numbers) so its relative share there is larger than its global %; sorting addresses for deterministic fold order on every finalize over up to ~23k fee-credit recipients is pure overhead if the recipient set is already produced in a stable order upstream. | **avoid work / batch**: if the MVS write set can hand back addresses pre-sorted (or sorted once per block rather than per recipient group), the repeated `slices.partitionCmpFunc`/`insertionSortCmpFunc` calls seen under this focus go away. |
| 2 | `modules/state.(*IntraBlockState).foldBalanceIncrease` (`modules/state/intra_block_state.go:1027`) | 0.072-0.22% cum (varies by focus) | ~1-2ms/block over ~23k recipients, but this is exactly the fee-credit fold the task names, and it is single-threaded by construction (it runs after the parallel wave to resolve balance-increase conflicts) -- its absolute cost is modest here but it is the serialization point that caps post-wave parallelism. | **parallelize**: the fold is a map-reduce over independent recipient addresses; it could be sharded across workers (e.g. partition by address prefix) since each recipient's fold is independent, turning a serial O(23k) pass into a parallel one. |
| 3 | `internal.applyMVSToIBS` (`internal/parallel_processor.go:739`) + `internal/parallel.(*MVS).ApplyAll` | 0.14% cum | <1ms/block in this capture -- confirmed cheap, not a target. | **(no fix needed)**: this phase is already negligible; the task's premise that it might be a large serial cost is not supported by this profile. |
| 4 | `lib/qmdb.mapIndex.Get` / `Tree.GetVia` / `QMDBStateReader.qmdbAccount` (root computation input path, `lib/qmdb/*.go`, `modules/state/commitment/qmdb_*.go`) | 2.01% cum combined | ~6ms/block | **cache**: account reads feeding root computation go through `mapIndex.Get` -> `Tree.GetVia` -> blake3 hash; if the same accounts were already read during exec (likely, since fee payers/recipients overlap block-wide), a short-lived per-block read cache shared between exec and root-compute would avoid the second lookup. |

## Takeaways

- The single largest controllable cost in the whole follower import path is
  **ecrecover** (50.6% of all sampled CPU), not the parallel exec/finalize
  machinery the task hypothesized; `FinalizeTx`/`applyMVSToIBS`/root-compute
  are each under 2% of total CPU in this capture and are not meaningful
  targets on their own.
- The serial post-wave phases named in the task (fee fold, FinalizeTx
  pre-fold, applyMVSToIBS, root) are all cheap in aggregate CPU share here;
  their cost on the *wall-clock critical path* (finalize~113-117ms per the
  task's own per-block timing) comes from being serial, not from being CPU-
  heavy -- the actionable fix for them is parallelization of the fold, not
  CPU micro-optimization.
- B2's profile pair is unusable (idle/startup capture); any future retry
  should trigger the capture strictly on the `txs>=100000` log line as S79b
  did for B1, and verify total-sample utilization (`-raw` "Samples" line)
  before trusting the result.
