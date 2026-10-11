# From 150k to multi-million TPS: why the current design stops, and what replaces it

Date: 2026-10-09 (America/New_York). Status: P1a and P1b implemented (sections 5.1, 5.2); P2a (vote on availability, lag-2 execution) measured at S95: 206k execTPS at 163k-tx blocks, serial import 670 ms, lag 2, no retention; P2a2 (stage pipelining across blocks) implemented, S96 running on n42-r125 (P2a2 only); P3 (transfer fast path, sharded base cache) landed 2026-10-10 (6da19407, 664c6080, 38d37696, f9758584, 771142ae), see "P3 result" before section 6.
Inputs: the n42-rs analysis and the gov5 critical-path map (scratchpad
`n42rs-analysis.md`, `gov5-critical-path.md`, both derived from code, docs and
logs; nothing re-measured), plus QS rounds S77-S86.

## 1. The gap is mostly measurement, then design, and only last tuning

| Configuration | Measured | Rule set |
|---|---|---|
| n42-rs, "4M" | never measured; docs say "no pair reaches 3.5M or 4M" | model projection (depth-2 deferred) |
| n42-rs, E=1, 400k-tx blocks, 30 s window | 3.48M peak, 3.0-3.2M sustained | ONE execution layer answers all 7 votes; Ed25519 + gateway-attested frames; pure 21k-gas transfers; 12.5k senders; pre-signed 400M-tx file over binary TCP |
| n42-rs, 7 nodes each executing | ~650k (10.76), ~282k (September fleet) | like-for-like with gov5 |
| gov5, 7 nodes each executing (S83/S84) | 150k, occupancy 20-28% | JSON-RPC batches of 200, secp256k1 ecrecover on every node, full 26 MB bodies pushed |

Decomposition of the 21x:

1. **~5x is the rule set.** E=1 (execute once, seven light voters) versus
   seven independent executors is n42-rs's own 3.2M vs 650k. Not engineering.
2. **~2x is ingest and sender auth.** Ed25519 batch verify (13 us) plus
   attested frames (no per-tx check at all) versus ecrecover 28-72 us on all
   seven nodes (53% of node CPU in S79b, ~47 cores fleet-wide).
3. **~2x is where gov5 actually is today.** Blocks run 20-28% full; delivered
   supply (~134k/s from 8 JSON generators) equals the score. The engine's
   own ceiling with full blocks is an estimated 250-330k. n42-rs independent
   execution reaches 282k on the same box with 98% occupancy. Like-for-like,
   gov5 is within 2x of n42-rs and the 2x is the supply pipe, not execution.

What the S-rounds bought, for scale: S77 +1.4%, S78 -0.75%, S83 -31% runMs
with no TPS change, S84 flat. The noise floor is 3.6%. Every remaining
lever inside the current design (wave, root, write) is worth <=5% because
the block is not full and the follower path is replicated seven times.

## 2. Ruling

The current design cannot reach millions by tuning. Four things are
structural and each needs a format or protocol change, not a flag:

1. **Every validator re-executes and recomputes the root inside the commit
   path** (340-520 ms of a 0.5-0.65 s cycle). At 3M TPS, exec + recovery
   replicated 7x needs ~840 cores. The box has 256 threads.
2. **Ingest is JSON per tx with a single pool lock and ecrecover per node.**
   Cap ~65-70k/s per node; 1/7 of senders per shard; the leader can only
   propose what its own shard holds.
3. **Bodies ship whole** (18-26 MB per block, 156 MB egress per block on a
   real network = 1.25 s at 1 Gbit). Loopback hides this today.
4. **One lineage for state commit and persistence** (QMDB apply 56 ms
   serial, MDBX write 120-195 ms under the import lock).

Decision: stop the S-round series after S86. S86 only validates the
limiter-deadlock fix (53106179) and the interleaved load; it is not a TPS
step. No S87. Token budget moves to the design below.

## 3. Target and rules (so the number means something)

Target: **>= 3M TPS on this box under n42-rs's E=1 rules**, and
**>= 1M with seven independent executors** as the honest number. Report
both; never quote the E=1 number without the rule set. Workload: the same
pre-signed transfer file n42-rs uses (12.5k senders, 2M recipients), so the
two chains are compared on one input.

## 4. Architecture: order first, execute behind, persist last

```
 generators --binary frames (500 tx, gateway-signed)--> frame workers (per node)
      frame workers -- broadcast frame + hash, collect f+1 acks --> DA certificate
 leader proposes [frame hashes + DA certs]  --HotStuff-2 vote on availability--> QC
 ordered frames --> execution layer (one per node, or one shared in E=1 mode)
      exec N | root N-1 | persist N-2   (three-stage pipeline, lag 2)
 header N carries root(N-2); commit vote never waits for execution
```

### 4.1 Ingest: frames, not transactions (replaces ceiling 2 and 3)

- Wire: raw binary TCP frames of 500 txs, length-prefixed, signed once by the
  gateway (the load generator in the bench). A node admits a frame on the
  gateway signature alone; per-tx signature checks move to execution and
  are batched (secp256k1 batch recover on a worker pool, or Ed25519 for the
  0x50 transfer type as n42-rs does).
- Pool: a frame store keyed by frame hash. No per-tx insert, no single lock,
  no nonce sort at ingest. Sender/nonce ordering is enforced at execution
  (a tx with a stale nonce is skipped, not rejected at the door).
- Dissemination: each node's frame worker broadcasts frames it received to
  all validators and collects f+1 availability acks (Narwhal-style
  workers). A leader may propose any certified frame, not only its shard.
- Reuse: `internal/ingest/server.go` (hint feed is already binary) becomes
  the frame path; `txspool` stays only for the JSON-RPC user path.
- Gate P1: supply >= 1M tx/s into the leader, occupancy >= 95%, TPS >= 300k
  with today's executor. This alone should double the score.

### 4.2 Consensus: vote on availability, blocks by reference (ceiling 1, 3)

- Block body = ordered list of frame hashes + DA certificates (a few KB).
  Proposal carries only that; followers already hold the frames.
- Votes attest ordering and availability, not execution. The commit vote
  fires as soon as the body is checked (ms), so the view cycle is the vote
  round-trip (~60 ms) and nothing else.
- Deferred execution lag 2: header N carries root(N-2) and receipts root
  (N-2). A node whose execution falls behind still votes; it cannot
  propose until it has executed N-2. Equivocation on roots is detected at
  N+2 and slashed by the existing equivocation path.
- Reuse: the HotStuff engine (`internal/consensus/hotstuff`), the tenure
  logic, `deferred_execution.go` generalised from depth 1 to depth k.
- Gate P2: block cycle <= 100 ms at 200k txs per block; 7 nodes; execution
  running behind by <= 2 blocks; TPS >= 1M in E=1 mode.

### 4.3 Execution: one layer, transfer fast path, sender lanes (ceiling 1)

- One execution layer process per node (E=1 mode: one layer serving all
  seven validators, exactly as n42-rs benches). Validators become ~1-core
  processes.
- Transfer fast path: a tx type whose execution is balance/nonce arithmetic
  with no EVM interpreter (n42-rs `fast_transfer.rs`). Any non-transfer tx
  in a frame routes that frame to the Block-STM path; frames are typed at
  the gateway so the split is free.
- Lanes: group by sender across the whole block (interleaved supply makes
  this ~12.5k lanes), 32-64 workers, LPT (S83) kept. Build N+1 on N's
  in-memory output; never wait for persistence.
- Gate P3: exec >= 3M tx/s for pure transfers on 32 cores (10.4 us/tx today
  on the EVM path means ~100k/s per core on the fast path is the bar).

### 4.4 State: root behind, persistence last, sharded apply (ceiling 4)

- QMDB stays in memory as the live tree; apply is sharded by the top
  address nibble (16 subtrees applied in parallel, root folded once). Today
  apply is one serial loop (56 ms) and root 145 ms; n42-rs pays 29-38 ms
  for the whole root at 200k txs.
- Root for block N is computed while N+1 executes (lag 1) and shipped in
  header N+2 (lag 2). Persistence (MDBX) is batched every k blocks on a
  writer goroutine with no import lock; the account table is written in
  sorted runs to kill the 11.7x page amplification from round 19.
- History index and tx-lookup off by default in bench mode (same as rs).
- Gate P4: root(N) <= 40 ms at 200k txs; persist off the critical path;
  memory growth bounded over 3 minutes at 3M tx/s.

### 4.5 Resource model

- Judge every change by CPU-seconds per tx (fleet-wide), not wall TPS.
- E=1 for the headline, 7x independent executors for the honest number.
- Replicas on one box share 256 threads; GC was 20-54% of CPU. GOGC tracks
  live heap; the frame store is off-heap (mmap) so the pool does not scan.

## 5. Phases and stop rules

| Phase | Scope | Gate | Stop if |
|---|---|---|---|
| P1 frames | binary frame ingest, frame store, hash-reference bodies, DA acks | occupancy >= 95%, TPS >= 300k | < 250k with full blocks: executor is the bound, re-plan |
| P2 order-first | vote on DA, deferred lag 2, E=1 mode | >= 1M E=1 | cycle > 150 ms: consensus engine, not exec, is the bound |
| P3 fast exec | transfer fast path, whole-block sender lanes | >= 2.5M E=1 | fast path < 60k/s/core: revisit state access, not EVM |
| P4 state | sharded QMDB apply, root lag, batched persist | >= 3M E=1 sustained 3 min; >= 1M independent | memory growth unbounded: persistence design wrong |

Each phase is one Opus implementation agent for the protocol/format change
plus Sonnet agents for tests, harness and measurement; one round per gate,
no tuning rounds inside a phase. Expected cost: P1 and P2 are the large
ones (new wire format, new vote semantics); P3 and P4 reuse existing
parallel and QMDB code.

### 5.1 P1a status (frame ingest, 2026-10-09)

Done: transactions can enter a node as gateway-signed frames; consensus and
block bodies are unchanged.

- Wire (all little endian): `"FRM1"` magic u32 on the ingest listener, then
  header 36 B = version u8 (1) | tx type u8 (0 Ethereum RLP/2718, 1 native) |
  gateway address 20 B | seq u64 | tx count u16 (<= 500) | payload len u32
  (<= 1 MiB); payload = tx count x (u16 len | tx bytes); 65 B secp256k1
  signature over FrameHash = keccak256(header || payload). Reply 33 B:
  status u8 (0 accepted, 1 duplicate, 2 busy, 3 bad sig, 4 unknown gateway,
  5 malformed, 6 disabled) | FrameHash. Code: `internal/frame`.
- Admission: allowlist check + one ecrecover + one copy from the socket;
  the frame lands in `internal/frame/store` (FIFO ring keyed by FrameHash,
  byte budget, oldest evicted). Legacy batch messages share the listener.
- Env: `N42_FRAME_GATEWAYS` (comma-separated hex addresses; empty = frame
  ingest off), `N42_FRAME_STORE_BYTES` (default 4 GiB). Needs the ingest
  server on (`--ingest.addr`). (`N42_FRAME_BRIDGE_WORKERS` went with the
  bridge in P1b.)
- Generator: `txflood -frames -gateway-key <hex> -frame-peers host:port,...`
  (one frame in flight per `-conc` connection, busy = back off and resend).
- Temporary bridge (P1a only, deleted in P1b): a worker pool decoded each
  frame, recovered senders and called `AddLocals` once per frame so the
  existing miner could build from `txsPool.Pending`.
- Measured in-process (8 cores): door alone 36.8k frames/s = 18.4M tx/s
  (`BenchmarkFrameIngest`); verify 128 us per 500-tx frame on one core. The
  bridge is ecrecover-bound (~143k tx/s on the 5k-tx e2e test).
- P1b next: the miner reads `w.txsPool.Pending` in `fillTransactions`
  (`internal/miner/worker.go`) and bodies are assembled by
  `FinalizeAndAssemble` / `block.NewBlock` with `Body.Transactions`; the
  proposal carries the full block (`internal/consensus/hotstuff`). P1b puts
  ordered frame hashes in the body, has followers resolve them from the
  frame store, and moves sender recovery to execution.

### 5.2 P1b status (blocks by frame reference, 2026-10-09)

Done: blocks reference frames by hash; the leader fills from the frame
store, followers resolve from theirs, sender recovery is part of
execution, the P1a bridge is gone. Production is behind
`N42_FRAME_BLOCKS=1`; with it unset nothing produces a frame body and every
path is today's (ordinary bodies encode byte-identically).

Formats.
- Body: `Body.FrameHashes` (ordered FrameHash list). The header is
  unchanged; for a frame body **TxHash = keccak256(FrameHash_0 || ... ||
  FrameHash_n-1)** (`block.FrameTxRoot`). ReceiptHash, Root, Bloom, GasUsed
  keep their meaning (deferred execution included).
- Wire (push, fetch, gossip, sync are all block RLP): `blockRLP` gains a
  trailing optional `FrameHashes []Hash`; a frame block carries empty
  TxData plus the hashes (~33 B per frame). The HotStuff proposal was
  already hash-only (block hash + TxHash) and is unchanged; the sealed-block
  push of a frame block is header + hash list, the full body is never sent.
  The proto `types_pb.Body` is not changed: blocks no longer travel as
  proto (it is only a legacy decode fallback), so a frame block has no
  proto form.
- Storage: compact body codec version `0x02` = marker 0xFF, 0x02, uvarint
  frame count, 32 B per hash, then verifiers/rewards/ZK proof as in 0x01
  (0x01 bodies decode unchanged). In MDBX the BlockBody row stays the
  12-byte BodyForStorage with no transaction rows; the hash list sits under
  the body key + tag byte `0x46`; new table `Frames` (FrameHash -> encoded
  frame) is written with the block. `ReadBlock` (RPC block/tx lookups)
  decodes the transactions from `Frames`. Writing a frame block drops its
  frames from the frame store and remembers their hashes (resends answer
  duplicate).
- FRQ1 pull (ingest port): `"FRQ1"` u32 | count u16 (<= 4096) | hashes;
  reply per hash: status u8 (0 found, 7 not found) | FrameHash | (found)
  len u32 | frame. Served from the store, then `Frames`. Pulled frames are
  admitted like FRM1 (allowlist + signature + hash match).

Consensus rule change (frame blocks only): **skip rule.** A transaction
whose pre-execution check fails -- stale or future nonce, cannot pay
gas x price + value or the intrinsic gas, fee cap under the base fee,
non-EOA sender, unrecoverable signature -- is skipped: it stays in the
block with a receipt of status 0, gas used 0 and the previous cumulative
gas, writes no state, pays no fee, and is counted (`skipped` on the
"blockimport phases" and "parallel block" lines). Any other failure still
fails the block; ordinary blocks keep "any failure fails the block". Frame
entries that do not decode are dropped from the transaction list on every
node alike. Under deferred execution a frame block is includable by
construction; the check only resolves its frames and checks the gas sum.

Leader. `fillTransactions` takes whole frames, oldest first, skipping
frames held by unwritten ancestors (in-flight set), until the next frame's
admission gas estimate (sum of gas limits, RLP walk to the gas field)
does not fit or `N42_FRAME_BLOCK_MAX_FRAMES` (default 1024), after the
pool's local frames (below). Bundles are not read. Execution is `BuildParallelFrames` (Block-STM, same
code as the import) with a serial fallback that drops a frame on a failure
outside the skip rule; assembly stamps FrameTxRoot without a
per-transaction root.

Local frames (S87 fix, 2026-10-09). S87 found the leader never mined the
pool: JSON-RPC user transactions and the benchmark's funding sat there
while every block came out empty. Now the leader first takes the pool's
pending executable transactions (stale ones trimmed against the build's
parent state, then the pool fill's price-and-nonce order), packs them into
**local frames** of <= 500 transactions (and <= 1 MiB) signed by its own
frame key, puts them into its frame store and places them in the body
BEFORE the gateway frames, up to `N42_FRAME_BLOCK_POOL_GAS` (gas, default
a tenth of the block gas limit; what the pool leaves unused goes to the
gateway frames). An account whose next transaction does not fit, is
priced out by the base fee or has no Ethereum encoding is passed over
with its later transactions. Followers pull local frames over FRQ1 like
any frame. The pool learns about them as about any mined transaction: on
the head move its reset drops pending transactions below the sender's
state nonce (`demoteUnexecutables`), and a reorg diff reads frame bodies
decoded from `Frames`. The gateway pick skips the leader's own local
frames (leftovers of a discarded build are framed afresh from the pool);
an unused local frame leaves the store after 64 blocks.

Frame key. With the etherbase account unlocked in the keystore, the
leader signs with it, so the frame signer is the validator address. A
HotStuff validator has only its BLS key (the qs fleet keystores hold
nothing else) and no secp256k1 key for its address; it signs with a key
derived from its BLS secret (`frame.DeriveLocalKey`: keccak256 of a domain
tag, a counter and the secret), logged at start as "Local frame signer".
That address is not the validator address, so followers admit those
frames only when `N42_FRAME_GATEWAYS` lists it.

Admission (FRM1 push and FRQ1 pull alike): the signature must verify and
the signer must be in `N42_FRAME_GATEWAYS` OR in the current HotStuff
validator set (read per frame). With `N42_FRAME_BLOCKS=1` frame ingest
starts even with an empty gateway list (validators only).

Follower. `InsertChain` (before bc.lock) and the deferred check resolve
the hashes: frame store, then `Frames`, then FRQ1 to
`N42_FRAME_PULL_PEERS` (timeout `N42_FRAME_PULL_TIMEOUT_MS`, default 2000).
Unresolved -> not imported -> no vote (logged once per block). With
two-phase voting the header-known round-1 vote is still cast before
resolution; round 2 waits for import/check as before. Senders are
recovered by the existing batched step of the import (sender_recovery.go)
on the decoded transactions.

Env summary: `N42_FRAME_GATEWAYS` (frame signers admitted beside the
current validators), `N42_FRAME_STORE_BYTES`,
`N42_FRAME_BLOCKS=1` (leader production), `N42_FRAME_BLOCK_POOL_GAS`
(pool share of a frame block, default gas limit / 10), `N42_FRAME_BLOCK_MAX_FRAMES`,
`N42_FRAME_PULL_PEERS`, `N42_FRAME_PULL_TIMEOUT_MS`; txflood `-frames
-gateway-key <hex> -frame-peers a,b,... -frame-broadcast`.

Known limits. No frame dissemination or DA acks yet (P2): the generator
must broadcast every frame to every node (`-frame-broadcast`) or a leader
only proposes what it received, and followers pull the rest. Frames of an
abandoned block become fillable again only after 16 blocks. The era
freezer keeps no transactions for frame blocks (their `Frames` rows must
be kept). Native-codec frames carrying a declared sender go through the
sender gate, which rejects the block on a mismatch rather than skipping.

Measured in-process (8 cores): frame pick 16.6M frames/s from a
4,000-frame store; decode 4.5M tx/s, batch sender recovery 189k tx/s
(decode + recover 181k tx/s, 50k transfers). Recovery is now the
follower's dominant per-transaction cost before execution.

### P1 gate (S87, 35zzzbm)

Round 35zzzbm is the one gate round of phase P1 (section 5: one round per
gate, no tuning inside a phase). It runs the S86 legs unchanged (warm-up,
A1, B1, B2, A2; 2 windows of 60 s) with frame ingest and frame blocks on.

- Binaries: node `n42-r116`, generator `txflood-r45`.
- Fleet env (all 7 nodes): `N42_FRAME_GATEWAYS=<gateway address>` (one
  secp256k1 key for the round), `N42_FRAME_BLOCKS=1`,
  `N42_FRAME_PULL_PEERS=<7 ingest host:port>`, `N42_FRAME_STORE_BYTES=1073741824`
  (1 GiB per node, to stay inside GOMEMLIMIT); max frames per block (1024)
  and pull timeout (2000 ms) at their defaults. The ceiling of
  163k tx per block is 326 frames, well under 1024 x 500. Unchanged from S86:
  parallel EVM, `N42_MINER_PARALLEL_FILL=1`, `N42_WAVE_LPT=1`,
  `N42_MINER_SENDER_ROTATE=1`, `N42_BUILD_STALL_DIAG=1`.
- Generators: 8 x 1000 senders, pertx 12000, `-frames -gateway-key <hex>
  -frame-peers <7 ingest addrs> -frame-broadcast -interleave`, funding and
  nonces over `-rpc`. `-conc 4` per generator (32 sender connections, each
  with one connection per peer): a frame is 500 txs, so 400k tx/s is 800
  frames/s, and one connection round-trips a broadcast frame in a few
  milliseconds; 4 is well above need, the limit is lazy signing. Rate is 0
  (S86's 8 x 20000 cap would pin supply at 160k tx/s).
- Supply: pertx stays 12000 (8000 senders x 12000 = 96M txs per leg). With
  the 1 GiB store the prefill is about 9.7M txs, plus 135 s x 300k/s, about
  50M per leg, so 96M is enough.
- Faucet: the dev faucet earns `devBlockReward` = 1 ETH per block (chainspec),
  so its balance at funding equals the empty blocks produced in the baseFee
  decay: 1589 at 400 s in S86, about 3.97 blocks/s. Funding costs (pertx +
  10) x 21000 x gas price per sender, 8 x 252.25 = 2018 ETH per leg, more than
  1589, so S86's warm-up lost its eighth generator (204 ETH left, 252
  needed). Gas price cannot be lowered: the base fee floor is 1 gwei
  (`InitialBaseFee`; eth_gasPrice reads 1.000000007 gwei) and txflood has no
  value flag. S87 therefore runs the warm-up decay for 900 s (about 3570
  ETH, need 56%); later legs start at 3-5k ETH and keep earning 1 ETH per
  block. After seven generators the eighth still sees about 1.8k ETH against
  252 needed.
- Baseline: S84 B legs 148.9k/134.0k, 145.5k/137.2k at 20-28% occupancy.
- Gate: gas occupancy >= 95% AND B mean >= 300k tx/s.
- Stop rule (section 5): B mean < 250k with full blocks means the executor
  is the bound, re-plan; between 250k and 300k is a partial result and the
  gap is read from the follower exec versus root phases before P2.
- Reading the numbers: occupancy and TPS come from `measure-tps.sh`, which
  reads `eth_getBlockByNumber` hash lists (frame blocks reconstruct their
  transactions from the `Frames` table). Also read the `skipped` count on
  the "parallel block" lines, FRQ1 pull counts and unresolved-block logs.

**S87 result (35zzzbm, 2026-10-09 ET): INCONCLUSIVE, no measurement.** The
round ran on n42-r117 (lock-order fix 8cacba80 and pool read budget
12c0aa98) and ended at 21:43:21. Every flood died at funding ("funding was
not confirmed within 300s", latest nonce 0, pending nonce 1000..8000) in all
five legs, because with `N42_FRAME_BLOCKS=1` the leader fills blocks only
from the frame store (`fillFrames`, internal/miner/worker.go) and never
takes pool transactions, so the RPC funding transactions were never mined.
All blocks on all 7 nodes are empty (txs=0), produced at a steady 4 blocks
per second (240 per minute) through blocks 13,652,363 to 13,711,047 with no
wedge and no view-timeout run, so the r117 lock-order and pool-budget fixes
were not exercised by load. No frame was sent, so frame fill, resolve, pull,
missing and store-eviction paths are unexercised. The missing win lines are a
consequence, not a measure-tps.sh bug: the S25 capture logged "flood-
announcement signal not seen within 900s" and "neither signal fired;
skipping win1/win2 captures" in B1 and B2. Each leg took about 47 minutes
because decay, the funding barrier and the 900 s capture wait ran without a
flood. Occupancy 0%, TPS 0. The P1 gate and the stop rule are not decided.
Fix before the rerun: get funding mined under frame blocks (genesis
allocation for the 8000 senders, or an unframed warm-up block phase for
funding), then run the one P1 gate round again.

**S88 plan (35zzzbn).** Rerun of the S87 P1 gate on n42-r118, which is r117
plus local frames for pool transactions: the leader packs its own pool
transactions (the RPC funding transactions included) into locally built
frames, so funding is mined under `N42_FRAME_BLOCKS=1` and the gateway flood
can start. Configuration is otherwise unchanged from S87 (txflood-r45 with
`-frames -gateway-key -frame-peers -frame-broadcast -interleave`, pertx
12000, 1 GiB frame store, 900 s warm-up decay). The gate is the same:
gas occupancy >= 95% AND B mean >= 300k TPS; the flood flags are echoed into
the round log at each leg start so the run can be checked for `-frames` and
`-interleave`. Anything short of that, or another round with no flood, is
read as in S87: the gate is not decided.

**S88 result (35zzzbn, ROUND DONE 23:43:45 ET 2026-10-09): INVALID for the
gate.** Funding now works (local frames mined the RPC funding transactions),
the eight generators delivered 0.93-0.96M tx/s with busy=0, node3 accepted
192,000 frames (96M tx) per leg, and the leader built 163,000-tx frame
blocks. But 97-100% of the transactions in those blocks were skipped (B1
2,445,000 of 2,445,000; A1 10.13M of 10.29M), so occupancy was 0-5% and the
B mean was 1,358 TPS. Execution of a full block took 0.17-0.44 s; the
executor was never reached as a bound.

The defect is admission, not supply and not execution. The frame store is a
1 GiB ring: it held ~20,000 frames and evicted 166,000-170,000 per leg, and
since the P1a bridge was removed it never answers status 2 (Busy), so the
generators ran 6x ahead of consumption with no backpressure. Interleaved
senders put consecutive nonces of one sender in consecutive frames; once the
older frame is evicted unincluded, every later frame of that sender is a
future-nonce skip. Followers lost the same frames ("frames unavailable, not
importing (no vote)" 8/34/9/11 per leg), which explains the view changes
(~1 per commit), the 542 "committed block not executed locally" and the
1,648 "propose-before-write: write failed AFTER Proposal left" lines. The
skip reason is not logged, so the mechanism is inferred from the eviction
and skip counts; S89 adds the counter.

Gate: not met. Stop rule: not triggered (blocks were not full). P1 stays
undecided until one round runs with bounded admission.

**S89 plan (35zzzbo): bounded admission.** Rule: a frame is evicted only
after a block that references it is committed (plus a reorg margin of
`N42_FRAME_KEEP_BLOCKS`, default 64); when the store is at its byte cap with
nothing evictable, ingest answers status 2 (Busy) and stores nothing.
Frames arriving by FRQ1 pull for a proposal the node must vote on bypass the
cap (they are needed to make progress and are bounded by one block). The
leader fills blocks in store arrival order (oldest first), so the frames of
one gateway are consumed as a prefix and sender nonces stay contiguous.
txflood treats Busy as backpressure: it retries the same frame to that peer
with 5 ms doubling to 200 ms backoff until accepted, counts the retries,
and declares a frame sent only when every `-frame-peers` target accepted it;
supply then equals consumption and the generators run for the whole leg
instead of 100 s. Each imported frame block logs skipped-transaction counts
by reason (stale nonce, future nonce, cannot pay, intrinsic gas, fee cap,
non-EOA, bad signature) beside the existing `skipped` total. Faucet: A2 died
at funding (need 252 ETH, had 165-184), so the harness must size the warm-up
decay to the summed per-leg funding, not to one leg. Predicted: skipped
< 1% of frame-block txs, occupancy >= 95%, busy retries > 0 on every
generator, no "frames unavailable" line in B legs. Gate unchanged.

**S89 result (35zzzbo, ROUND ABORTED 01:56 ET 2026-10-10): INVALID for the
gate; bounded admission works, ordering does not.** The store held at
4 GiB / 80,891 frames, answered Busy, never evicted an un-included frame,
and consensus was clean (no "frames unavailable", no view timeout, all 7
nodes at the same committedQC). But 95-100% of frame-block transactions
were still skipped, every one of them futureNonce (staleNonce, cannotPay,
badSig: 0). Mechanism: a full store makes admission a lottery among the
Busy-retrying frames of eight generators with four frames in flight each.
txflood's `-conc` window slides past a frame that is still Busy, so a
generator's frames enter the store with gaps; arrival-order fill then packs
frames whose predecessor (same senders, nonce-1) is missing. The first
~18 frames of each block execute (gasUsed 122-184M of a 3,423M target),
the rest skip. The round aborted on the harness memory line (MemAvailable
19 GB): seven nodes at 12.5-13.9 GB RSS, the 4 GiB store inside each
GOMEMLIMIT heap, not a leak. measure-tps.sh prints 0.0% occupancy on
frame blocks, so the gate's occupancy term is not measurable yet.

**S90 plan (35zzzbp): in-order admission.** Ordering is the gateway's
responsibility and the node checks it. txflood sends frame k+1 to a peer
only after that peer accepted frame k (per-peer sequential stream; `-conc`
bounds concurrency across peers, not frames of one peer); a Busy frame is
retried in place. If the FRM1 header carries a per-gateway sequence, the
node admits only seq == last accepted + 1 per gateway and answers Busy
otherwise (duplicates still 1); if it does not, the wire format is left
alone and the generator rule is the whole fix. Store 2 GiB (40k frames,
~120 B blocks of buffer; saves 14 GB over the fleet). measure-tps.sh
computes occupancy from header gasUsed/gasLimit over RPC for every block.
Predicted: futureNonce skips < 1%, occupancy >= 95% on B legs, B mean the
first real P1 number. Gate unchanged.

**S90 result (35zzzbp, ROUND DONE 03:56 ET 2026-10-10): valid; P1 gate not
met; P1 closed, executor is the bound.** In-order delivery removed the
skips: every B block under a stable leader executed 163,000 of 163,000
transactions (gasUsed exactly 21,000 x 163,000), ingest outOfOrder 0, the
store flat at 2 GiB with generators paced by Busy to the chain's own
consumption (~150k tx/s). Executed TPS: B1 144.0k / 70.6k, B2 133.1k /
100.5k (win1 / win2), A2 65.6k. That is the S84 pre-frame baseline
(148.9k): removing the supply bound bought nothing by itself, exactly as
section 2 said it would not. Block anatomy (node3, 163k txs, p50):
follower import 1,082 ms = sender recovery 344 + execution 415 + root 100
+ write 152; leader build 833 ms (fillExec 762). Consensus waits for
execution on both sides, so the block cycle is ~1.1-1.9 s and throughput
is 163k / cycle.

Two defects to carry into P2. (1) The harness fill target is half the
ceiling (fillgas 3,423M of 6,846M), so measured occupancy was 50.0% and
the gate's 95% term was never reachable by configuration; the P2 round
sets the target to 95% of the ceiling. measure-tps.sh also counted every
block as unparsed (a bare `echo` after the RPC reply); occupancy itself was
correct. (2) In B1 a view timeout moved the lead and the new leader sealed
one height twice; from the next block on, every block on all seven nodes
was 100% future-nonce (gasUsed 0) for ~44 blocks: the orphaned block's
frames stayed pinned (released 0 all round) and the fill skipped past them,
so every later frame of those senders had a nonce gap. Pinned frames of a
block that lost must become fillable again at once.

**P2a plan (35zzzbq): execution off the consensus path, independent mode.**
Scope, one Opus agent: (a) sender recovery at frame admission, in
background workers, cached with the frame in the store and reused by build
and import (344 ms of the 1,082 ms import leaves the block path; frames
arrive seconds before inclusion); (b) vote on availability: a follower
votes as soon as the body's frame list is checked and every frame is held
locally (pulled if missing), without executing; (c) deferred execution lag
2: header N carries root(N-2) and receipts root(N-2), the existing depth-1
deferred path generalised to depth k, a node cannot propose until it has
executed N-2, execution runs behind in a pipeline (recover-at-ingest, exec,
root, write on different blocks); (d) orphan-pin release. E=1 mode (one
execution layer serving seven validators) is P2b, measured after P2a.
Gate P2a (7 nodes, each executing): block cycle <= 150 ms at 163k-tx
blocks, execution lag <= 2 blocks, B mean executed TPS >= 300k (the
pipeline bound is the slowest stage, exec 415 ms per 163k = ~390k). Stop
if the cycle stays > 150 ms with execution idle: the consensus engine, not
execution, is the bound.

**S91-S93 results (35zzzbq/br/bs, 2026-10-10 ET): P2a runs; throughput is
the serial import.** Two reseed defects first (the lag gate waited on a
zero HotStuff parent, 81931b48; a lag-built empty block carried the
zero-frame root, cc1bc626). S93 then produced blocks under P2a: header
votes precede import, commit votes wait for import, every lag build
reports N-2, no unavailable frame, no equivocation, no skip. Blocks of
309,500 transactions (95% of the ceiling) imported in 1,731 ms warm (exec
744 + root 170 + write 346 + residual sender recovery 383), so the leader
gate opened every ~1.7 s and the chain ran at ~179k TPS, then the seven
Go heaps grew past GOMEMLIMIT (Sys ~12.1 GB each, ~0.34 GB per block,
frame store only 1.2 GB of it), the page cache was evicted, MDBX faulted
and imports went to 5.9 s; the harness aborted at 17 GB free. Execution is
off the vote path, but the chain cannot run further than two blocks ahead
of it, so throughput is still one block's full import time. The next
levers are, in order: stage pipelining across blocks (execute N+1 while
N's root and write run; bound becomes exec alone, ~415k at 310k blocks),
recovery fully at admission (16+ workers), the transfer fast path (P3,
exec 744 ms -> ~100 ms), then root and write (P4). The heap growth needs
a profile before any of that is measured: S94 reruns P2a at 163k-tx
blocks with node3 heap profiles captured during the flood.

### S95 result and P2a2 plan

S95 (35zzzbu, n42-r124) gave a valid warm-up win1 at 163k-tx blocks: execTPS 206,467, blockTime 0.789 s, lag flat at 2, 0 skips, with node3's import at 669-710 ms (exec 358-395 + root 101-113 + write 138-163 ms); the available-block cache no longer shows in the heap and MemAvailable stayed above 25 GB. The P2a gate reads lag <= 2 PASS, cycle <= 150 ms FAIL (670 ms, the serial per-block import) and execTPS >= 300k FAIL (206k), so the P2a mechanism (votes before execution, lag 2, no retention) is closed and the bound is the serial import. win2 (84k) is invalid because the generators did not run dry: supply was 12M txs each, and they stalled at 5.9M on Busy when the 2 GiB frame store filled (generation ~300k/s vs import 206k/s), the stall being a reply wait with no deadline in sendOneFrame (cmd/txflood/frames.go) that parked each per-peer sender instead of retrying; the full store's raw + decoded bytes (2.1 + 3.6 GB) put the node heaps at the 10 GiB GOMEMLIMIT (largest heap 8.6 GB: ingest readFrame 2.1 GB, decoded transactions 3.6 GB, qmdb map index 0.75 GB), and S96 runs the store at 1 GiB, and the round was aborted at the end of A1 by a checker race on a shutdown BAD BLOCK (`db closed`), not an import failure. P2a2 overlaps the execution of block N+1 with the state root and write of block N inside the lag executor: exec N+1 reads the in-memory post-state of N before N is rooted and persisted, N's write lands before N+1's, and a failure of N discards N+1's speculative execution. The expected cycle is max(exec, root+write), about 400 ms at 163k-tx blocks, i.e. about 400k execTPS. In the same step the per-node decoded-transaction memory is cut (the 16-block block cache is about 2.3 GB at 163k txs and the frame store holds 2 GiB of raw bytes) so heaps sit well under GOMEMLIMIT; the harness fixes the checker race and the generator supply before S96.

P2a2 implemented (commits f5cc75d8, a07fc6a6, 7b717ac7, 09a7c782, 373eaa91, 5c9b3711): just before N's root (rewards applied) N's state is copied into a read-only snapshot; N+1's Block-STM runs on its own goroutine reading through that snapshot and never touches N's state; N+1's root, validation and write run afterwards on the executor goroutine so writes stay in parent order; the ahead run is used only if the parent's import wrote it and the applied head is exactly that parent, otherwise it is discarded and the block runs the serial path. Caveats: the fold stays with the execution half; ComputeRoot and the write's flush take the QMDB tree lock exclusively, so N+1's reads stall while they run; expected cycle max(exec+fold, root+write) ~ 440-500 ms at 163k. Block cache: default 512 untouched, the harness sets N42_BLOCK_CACHE_BLOCKS=4 (minimum 3; under P2a the deferred commit is retried right after the write that added the block to the cache, other readers fall back to MDBX).

### P3 result (2026-10-10)

Offline 163k-transfer exec-wave benchmark (Block-STM, 32 workers, 4 cores nice'd, MDBX state, relative numbers only).

Where the exec wave goes:

- EVM interpreter + intrinsic gas + StateTransition: ~12%.
- State reads: ~27% (the recipient GetCode check under Prague alone is 19%).
- IBS Reset + FinalizeTx: ~13%.
- MVS writes plus deletes of the previous block's stale write keys: ~14% (delete ~9%, useless at incarnation 0).
- AsMessage + tx.Hash: ~8%.
- Allocation: ~16% in-wave plus ~17% background GC.
- Signature recovery: 0% (memoised, ~33 ms per block outside the wave).

Off-CPU: 99% of worker lock-blocked time was the single RWMutex of the block's base-read cache (internal/parallel/base_cache.go), ~60% of worker wall time. This explains the S79b "workers 95% off-CPU" finding.

| Variant (median of 4 interleaved runs, per 163k block) | Exec wave ms | Process CPU ms |
|---|---|---|
| old path | 589 | 2143 |
| transfer fast path | 570 (-3%) | 2053 |
| fast path + base cache in 256 locked shards | 452 (-23%) | 1957 |

Worker blocked time fell 71% with the sharded cache (understated at 4 cores).

Fast path rules: non-nil recipient, empty data, no access list, gas limit 21000, recipient without code and not a precompile, EOA sender. It makes the same IntraBlockState calls in the same order as the EVM path (nonce/code-hash/balance checks, gas debit, nonce+1, Exist + Transfer, zero refund, tip min(tip, feeCap-baseFee) to the coinbase, base fee burned or sent to the Eip1559FeeCollector). Anything else falls back to the EVM path inside the same Block-STM incarnation. N42_TRANSFER_FAST_PATH=0 restores the old path.

Differential test: random 400-tx blocks under mainnet_qmdb (Prague), London, and London + fee collector, comparing inclusion, failures, gas, receipts root and bytes, coinbase/collector balances, and state root.

Limits: the benchmark uses MDBX state, not QMDB; the fleet chain config is assumed to be mainnet_qmdb with legacy 1 gwei txs.

Conclusion: the EVM is not the exec bound; per-tx overhead is.

S97 plan (P3b, in progress): code check from CodeHash without loading code, skip the stale-key delete at incarnation 0, memoise tx.Hash/AsMessage from frame decode, per-worker IBS / MV-view / receipt reuse to cut allocations. Target: exec wave <= 200 ms in the bench, then P4 (root + write <= 150 ms).

S96 result (2026-10-10): the round was valid on all five legs but showed no gain over S95: B1 201,033 / 182,017 and B2 179,300 / 171,150 execTPS against 206k, with the gate (>= 300k) failing. Imports remain strictly serial (0-8 ms gap), with median exec 379-388 + root ~102 + write 156-163 = ~710 ms per block; the ahead execution was used on only 15-23% of blocks, and where used the block took 505-530 ms against 731-737 without. Correction of the cause (S97 analysis): the queue insert already happened at availability; the ahead run was lost on the push path, not at the QC. (1) With N42_DEFERRED_CHECK_CONCURRENT=1 most blocks were imported by the push handler's InsertChain, which did not publish the snapshot, and lagStep's "already stored" path called lagForget and discarded the child's ahead run; (2) the leader's own sealed child had no resolved frames yet, so ParallelApplicable saw <= 4 txs and never launched (own to own 0 of 102). Fix 036d56cc: every import of a queued block publishes the snapshot, the pipeline is settled inside insertChain on every path, and applicability is decided after ResolveFrames; new counters aheadAtt/aheadUsed/aheadDisc/aheadSkip*. Cycle recurrence with the ahead run: W(N+1) = max(W(N), W(N-1)+L+exec) + root/write, and depth 3 removes the L term, leaving the floor max(exec, root+write). Side findings: lag exactly 2, heaps 8.4-8.7 GiB under the limit, txflood timeouts=0 with working backpressure, commit-to-canonical read ~0 with block cache 4, and B2 write 194-196 ms vs 156-163 in B1 unexplained. S97 plan: n42-r127 (P3 + P3b) plus the ahead-trigger fix (start N+1's speculative execution at availability, not at QC); expected cycle max(exec, root+write) with exec cut by P3/P3b.

P3b result (2026-10-10, commits 50035ee4, bca120e4, 1f924c3b, 2b356aca, 14f89125, 4322bea5): in the bench (163k transfers, 4 cores nice'd, relative numbers) the exec wave went from 452 to 333 ms (-26%; the old path is 589 ms), process CPU from 1957 to 1482 ms, and allocations from 36 to 14 per tx (-61%). What moved: a per-worker reusable Message (AsMessageInto), IntraBlockState Reset that keeps the small maps and the dirty buffer, MarkBalanceInsensitive without a map below 17 writes, and a per-worker gas pool / fee sink / by-value fees in the result slot. What did not: a code-hash based "has code" check (the fast path already read only the hash; the 19% GetCode share was the old EVM path) and skipping the stale-key delete at incarnation 0 (about 3% CPU; the 9% figure was scheduling noise). Remaining profile: account reads 43% (MVS.ReadAccount 20%, BaseCache 9%), read-lock contention on the shared MVS/BaseCache counters 13%, FinalizeTx 9%, tx.Hash + AsMessageInto 8.5%, MVS writes 6.5%, background GC 7.8% of process CPU. Next exec lever (P3c): MVS account read cost and the shared counters; remaining allocations are one receipt per tx, DecodeAccount/MarshalV2 per account, and one copy per delta write. Binary n42-r127, sha 97ef3f8cb06c210080a7f478d86efad722a8030eef31d17c10605a7db6c517f5.

P3c result (2026-10-10, commits c191d023, 60dad189, f8e00377, d7be54d9): in the bench (163k transfers, 4 cores nice'd, medians of 4 interleaved runs, noisy box, compare pairs only) the exec wave went from 338 to 251 ms cumulative; the old path is 589 ms, so -57% overall. c191d023 made lookups lock-free in the MVS and BaseCache (insert-only tables read with atomic loads) and folded per-worker counters after the wave: 349 -> 298 ms, blocked time on locks 21.8 -> 1.6 s per 8 blocks (the contention was the shard RWMutex reader counts plus the getOrCreateEntry / BaseCache put write locks, not stats). 60dad189 decodes once per account read (BaseCache keeps the encoding, deltas decode once into the caller's buffer, short version lists are scanned from the end, validation skips composing balance-insensitive reads): 298 -> 285 ms, allocs 14.1 -> 10.3 per tx. f8e00377 carves receipts from per-worker chunks, keeps MVS deltas by value, and lets the store adopt write values without copying: 374 -> 329 ms (under load 44), allocs 10.3 -> 7.3. d7be54d9 reuses one decode buffer per worker reader: 262 -> 258 ms, allocs 7.3 -> 5.3. Final profile share of the wave: account reads 28% (mostly cache misses on table entries and version lists), validation 12.5%, FinalizeTx 12.5%, tx.Hash + AsMessageInto 11%, MVS writes 8%, background GC 8% of process CPU. Not reached: the 200 ms target; the next step needs a lock-free version read, prefetching, or FinalizeTx work in modules/state. Tests green (one flaky TestQMDBLatestAccountSource_ConcurrentWithWriter passed 5/5 on rerun); -race on internal/parallel, the differential and lag pipeline tests. Binary n42-r129, sha b2931e10.... P4 (root + write <= 150 ms, then ~100 ms) is in progress; S98 will carry P3c + P4 on top of S97's r128.


### S97 result (2026-10-10)

The warm-up window reached 328,967 execTPS at a block time of 0.296 s, a new standing best (S95 was 206k), and B1 win1 reached 296,458 at 0.259 s; the round was aborted at 16:14 ET when MemAvailable fell to 18 GB. Occupancy was only 24-30%: the 8 generators supplied about 290k tx/s (signing-bound), so blocks held about 75k txs and the chain drained all of it in 0.26 s blocks. P2a2 is confirmed working: 68-73% of big blocks ran ahead, and the import of a 75k-tx block takes exec 72-80 + root 55-58 + write 94-95 = 240-264 ms (exec about 1 us/tx with P3/P3b). Memory is the second wall: heap grew from 1.3 to 8+ GiB in 60 s (about 28 MiB per block), led by txlookup.(*Tail).Add (2.04 GB, linear) and decoded transactions (3.5 GB), after which GC thrash pushed block time from 0.26 to 1.1 s. There was no BAD BLOCK, equivocation or unavailable event. The gate (>= 500k) was not reached because supply bound the run. Next walls: generator supply (txflood presign) and node memory (txlookup tail bound, decoded-tx release).

Arithmetic: at 75k txs the import is 240-264 ms, i.e. 3.3 us per tx all-in, so a 163k block would take about 530 ms serial and about 300 ms pipelined, giving about 540k tx/s. To reach 1M at 163k blocks the pipelined cycle must be 163 ms, i.e. root+write <= 160 ms (P4) and exec <= 160 ms (P3c gives about 1 us/tx already), with memory flat.

### P4 result (2026-10-10, commits e233022c, 58ee4b90, b1b4bdc0, f2242151, eb3c65ad, 607c21e3)

Bench TestQMDBImportRootWriteBench (internal/import_root_write_bench_test.go, N42_BENCH_IMPORT=1) imports 163,200 transfers as frame blocks through the lag executor with the ahead run on, READ_QMDB / WRITE_QMDB_ONLY, the HotStuff canonical rule and durable MDBX. It runs on 4 nice'd cores, medians of 3, so numbers are relative.

Baseline: root 94 ms = dirty-set collection 50, apply 16, op build/sort 15, snapshot publish 12, lock wait 0, fold 0. Write 209 ms = executed result (receipts root + bloom) 100, changesets 28, CommitBlock 24, QMDB flush 19, receipts 15, commit/fsync 13, block/frames 6, undo 1.5, history/tx lookup 0 (deferred).

Cuts:
- Receipts root + bloom on their own goroutine during the state root (write 204 -> 110).
- Radix-sorted address sets (collection 56 -> 40, CommitBlock 37 -> 17).
- QMDB ops built in parallel and radix-sorted (op build 14.5 -> 5.6, root 77 -> 68).
- Receipts row encoded before the write (15 -> 4.7).

Final: root 70 (collection 35, apply 16, publish 12, ops 6) + write 84 (changesets 21, CommitBlock 15, commit 12-20, QMDB flush 12, block 5, receipts 5) = 155 ms, from 297. Nothing is persisted asynchronously (the receipts goroutine is joined before commit). Tests are green, including the lag pipeline under -race. Binary n42-r130 (HEAD 5d5cbbfa) sha ff853a3d....

Remaining for <= 100 ms: run the MDBX write (changesets, CommitBlock, flush, commit, ~65 ms) on an ordered writer goroutine overlapped with the next block's root (needs a restart rule), and move collection + publish (~47 ms) out of IntraBlockState into an executor-built dirty set.

1M arithmetic: at 163k blocks the pipelined cycle is max(exec ~160, root+write 155) in the bench, so S98 tests whether the fleet gets near 163k/0.3 s = 540k, and the next step toward 1M is the ordered writer.

### Memory fixes after S97 (2026-10-10, commits 3b85e970, a0aba225, 606a98f1)

(a) The txlookup tail never drained. The tail holds every block's tx hashes until they are sealed into RecSplit segments. One builder seals a 1M-tx segment in about 4 s (~250k tx/s), below the 290k/s inflow, so with a 15 s tick about 4M hashes piled up, and the single hash->block map never shrank after deletes. Fix 3b85e970: a per-block bucket index (~37 B/tx, freed with the block; newest-first lookup keeps eth_getTransactionByHash correct), the sealer builds up to 3 segments concurrently (N42_TXINDEX_SEAL_WORKERS) appended in block order, tick 2 s, no hash-slice copy. Test: 2M txs at 10k/block through the real sealer, tail peak 190k (bound 230k), heap flat.

(b) Decoded transactions. The leader's miner kept every block it sealed: sealedByHash (16) and sealedOnParent (up to 512) held the block object that the import later fills with decoded txs, about 82 blocks over 150 s and growing toward 512. Ingest cache, frame store, txpool and available-block cache were ruled out. Fix a0aba225: sealedByHash stores a header+frame-list copy; sealedOnParent keeps the full block only while its height is undecided and swaps in the slim copy once the written head reaches it. Test internal/miner/sealed_heap_test.go (heap < 3 blocks over 60 sealed blocks; ~9 MB/block without the fix).

(c) Fix 606a98f1: fetch-on-miss imported every peer's copy of a block (108 goroutines holding copies behind the chain lock in the +180 s dump); now one import per hash at a time.

Expected steady-state heap per node at 290k tx/s is about 3-3.5 GiB (estimate; S97 was 8.5). Binary n42-r131 (HEAD 5e594590, includes P3c + P4) sha f4709990....

### P4b result (2026-10-10, commits 7b938407, dbf218da)

7b938407: the root takes the block's prefetched recipient credits as base+credit, computed in parallel, instead of building about 23k state objects; CommitBlock builds them later, in the write. The pipeline's post-state snapshot now comes from the root's own maps, so publishing it is essentially free. Root 65 -> ~37 ms (collection 33 -> 14, publish 10 -> 0); the write's `state` phase grows 14 -> ~30, so the work moved off the root rather than vanishing.

dbf218da: ordered writer, env N42_LAG_WRITE_ASYNC (default on). The critical path (root + hand-off) is 39-45 ms, down from ~105. Hand-off is 2.4 ms; its wait for the previous write is ~0.1 ms in the bench, but the bench paces blocks, so back-pressure is not loaded there. The writer's own write runs off the critical path at 86-107 ms wall; root+write summed is 123-143 ms. One write is in flight and the next hand-off waits for it. QMDB rows and the undo record are captured from the tree at hand-off; the flushed window is evicted only after the write is durable. "Imported" means durable: the hook, LagStats and the applied marker move only after commit, so the leader gate, votes and commits never see an undurable block; only HasBlock/GetBlock/GetHeader see the pending block. Only the lag import of the pending block's child at depth >= 2 runs during the write (it checks the tree root and skips the marker alignment); every other import, leader write, unwind or peel waits for the write first. A failed write reloads the tree from the store and queues the block again. A discarded ahead run of a block now finishes before that block executes again (a sender-cache data race caught by -race).

Tests: TestLagWriterRestartBeforeLastWrite (kill before block 4's write: header, marker, executed result, undo row and changesets all at 3, reloaded tree at 3's root; re-importing 4 gives the same in-memory root, 5-6 follow), TestLagWriterFailedWriteRequeues, TestLagPipelineAheadOrderedWriter (11/12 imports overlap the parent's write; results equal the serial chain). The txlookup tail and history backfill work only from committed or changeset data, so a head one block behind needs nothing new. internal, lib/qmdb, modules/state/..., rawdb and changeset are green; -race -run 'Lag|Commit' on internal ok. State roots in the bench match the old build block for block.

Not kept (step 3): writing the credits directly in CommitBlock saved ~1 ms. Writer CPU is ~68 ms/block; the biggest bucket is changesets at ~21 ms, almost all MDBX dupsort AppendDup (31k cgo puts) with no re-encoding to cut; the writer's wall time is inflated by competing with the ahead execution on 4 cores.

Binary n42-r132, HEAD dbf218da, sha256 1709457e05417ba19e962696db4637904d6c5c922ac9900632a2d42b6221f706. The cycle model is now max(execution ~160 ms at 163k on the fleet, write ~90, critical path ~40): execution is the limit, so the next step is P3d (exec). S98 (running on r131) does not carry P4b; S99 will carry r132 or its successor.


### S98 result (2026-10-10, 35zzzbx, n42-r131)

B2 win2 reached 274k execTPS at 163k-tx blocks (B1 187k and 103k, B2 223k and 274k; the A legs ran 87-90k, interval-bound). The per-block cycle is 280-330 ms median while the chain runs (exec ~146, root ~70, write ~120 ms at 163k; the ahead run covered 95-100% of blocks on the leader and 61-91% on followers). The gate of >= 500k is not met. The pipeline math is confirmed: a 330 ms cycle at 163k is about 495k tx/s of running time, so P4b's off-path write and P3d's exec cut are the right next steps for the cycle itself. The wall is elsewhere: 34-41% of wall time was lost to stalls, of two kinds. (1) View timeouts after a node's align phase takes seconds (node1 align 5.1 s on one block, 110 s over 21 blocks in B1). (2) A node 32 blocks behind taking the leader tenure and waiting on its own execution (24.6 s + 21.5 s stalls in B1 win2). Healthy view timing totals 251 ms (propose 181, r1 64, r2 11), so transport and votes are not the wall. Supply is not the limit: presign took 55 s per leg, timeouts 0, busy backpressure from the first seconds, about 4.7M txs queued.

Memory: the fixes held in the sense that the heap returns to 1.6-2.0 GiB after each leg and live is 3.6-3.9 GiB (top inuse buckets: ingest.readFrame 1.6 GB, qmdb.newMapIndexSized 755 MB, senderCachePut 632 MB, DecodeEthereumTransaction ~620 MB cum, NewReadWriteSet 205 MB). But the GOGC sawtooth up to the 10 GiB GOMEMLIMIT (NextGC 9.3 GiB) times 7 nodes puts the 7 nodes at 10-11 GiB Sys each, anon at 74-80 GiB, and evicts the MDBX page cache (halved; major faults 184-309k per 10 s against 1.6k on the A legs), with GC thrash (+49-57 per 30 s) on the lagging node. Sender recovery costs 400 ms/block for the first ~45 s after each per-leg restart (cold sender cache) and 42 ms afterwards. No BAD BLOCK, no restarts, all nodes imported identical block sets.

Conclusion: the new wall is the stall path, align seconds plus the lagged leader, fed by page-cache pressure. S99 = n42-r133 (P4b + P3d) with GOMEMLIMIT lowered so the fleet's anon stays ~50 GiB, plus whatever P5 (align / lagged-leader) lands.

P5a result (2026-10-10, commit 56659f4d): the S98 align stalls were a bug, not page-cache pressure. The align phase only opens a writer txn that returns at once on the forward path, so its time is the wait for the single MDBX writer lock. The holder was the idempotent duplicate-commit branch of commit-to-canonical, taken when a lagging node commits a block it has already imported (catch-up): it called WriteTxLookupEntries unconditionally, 163,000 Puts in one txn, held 5-27 s (write probe BlockTransactionLookup=163000; node1 had 52 such blocks, every one matching a probe), while the normal path skips the lookup under the txIndexer. The phase accounting hid it: dLookup was only set on the normal path, so the write landed in "body" (4,992 ms) with txlookup 0. Fix: the duplicate branch follows the indexer rule (no per-tx rows when the indexer is on) and times the write into txlookup when it does happen; nothing else in that branch rewrites per-tx data. Test TestCommitToCanonicalDuplicateTxLookupFollowsIndexer. The txindex seal (5.4-6.7 s on node1) does not take the MDBX writer lock: it is RecSplit building and file I/O off-lock, competing only for CPU and disk. Not changed: the lagged-leader park (LagReady gate, no timer of its own, ended only by the 6 s x 2^n view timeout); with align no longer stalling a node should not fall 32 blocks behind, so this is measured first in S99. Run-script change for S99: GOGC=200 and GOMEMLIMIT=10GiB let the live 3.9 GiB heap saw to the limit on every node (7 x 10-11 GiB Sys, anon 74-80 GiB of 136.6); S99 uses GOGC=100 with GOMEMLIMIT=7GiB.

P3d result (2026-10-10, commits 85869d10, bb91fcd0, 6554c4c8, f473acbe, be734ac4, 2e543cbb, 48e251f1, 2972fb13): in the bench (163k transfers, 4 nice'd cores, interleaved A/B pairs, box noisy up to load 70; the first four pairs 4-5 runs of 3 blocks, later pairs 7 runs of 6 blocks) the exec wave went 262.2 -> 146.8 ms (-44%, below the 160 ms target; the old EVM path was 589), process CPU 1194 -> 740 ms per block, allocs 5.25 -> 2.55 per tx (final pair dbf218da vs HEAD, 5 runs of 6 blocks). Per commit: 85869d10 fast-path transfers run directly on the worker's Block-STM reader/writer with no IntraBlockState, recording the same reads, writes and balance-insensitive marks, with a fallback to IBS (reset lazily) for anything else, N42_TRANSFER_DIRECT=0 turns it off, the differential test now compares EVM / old fast path / direct: 266.7 -> 207.0 ms, CPU 1202 -> 1002; bb91fcd0 two clock reads per tx instead of five: 209.7 -> 202.7; 6554c4c8 affinity keys computed in parallel (13 ms serial before): 204.0 -> 196.0; f473acbe each tx's message and hash built in that parallel pass in index order instead of in the wave: 207.7 -> 190.0, CPU 1007 -> 894; be734ac4 arena read/write sets from one slab of 512-byte cache-line-aligned slots (the old arena was 190 MB of live heap marked every GC cycle): 197.2 -> 190.3; 2e543cbb an account read keeps its store entry and the write and validation reuse it: 186.8 -> 171.0; 48e251f1 recorded values from one slab per worker: allocs 5.24 -> 2.55/tx, time within noise (168.0 -> 165.8), kept for the allocation rate; 2972fb13 store entries count their full writes so a recipient read that never uses the balance skips the version scan, delta sum and encoding, validation likewise: 163.7 -> 149.7, CPU 817 -> 767. Tried and reverted: an entry lookup cache before the direct path (no gain); hashes in the table slots plus account data inlined into the base cache (204 -> 218-221, worse); a per-worker cache of senders' own writes (191 -> 202); keeping prepared messages across blocks (190 -> 199, GC marks it every cycle); an unpadded slab (mixed, replaced by the padded one). Final profile share of the wave (615 ms CPU/block on 4 cores): recipient read 26.6% (15.7 points are base reads, 8.4 of them the bench's MDBX base reads; the fleet reads base through QMDB), MVS writes 17.4%, message+hash building (now before the wave, inside the measured time) 11%, fastTransferShape 10.3%, sender read 9.5%, receipt 4.9%, writer encoding 4.6%, validation 3.2%, background GC 8.8% of process CPU (receipts ~90 MB/block and version lists ~90 MB/block drive it). Tests: internal and internal/parallel pass; -race on internal/parallel and internal -run 'TestTransferFastPath|Parallel|Lag' (TestLagPipelineHeapBounded skipped under race: 10-min timeout on 4 cores, no race report before it); modules/state unchanged; TestQMDBImportRootWriteBench gives the same state root on all 12 blocks as the P4b build. Next levers, none above ~17%: atomics-only version store, transfer shape precomputed in the prepare pass, base reads checked on the fleet's QMDB path, presized version lists and receipts for GC. Binary n42-r133, HEAD 2972fb13 (includes P4b, P5a), sha256 6844541982b36077b3e3cfe44c35ba8062383afd16aa29eb26abb85423722516. Cycle model on the fleet after P3d (bench ratio applied to S98's 146 ms fleet exec): exec ~85-100 ms at 163k, write ~90 off the critical path, critical path ~40, so the pipelined cycle is bounded by exec and by the one-write-in-flight rule; 1M TPS needs ~163 ms, so the budget now exists if the S98 stalls are gone.

P6 result (2026-10-10, commits 9b6fb7d9, 2e0099ae, 18d5706a): the view timing spans (internal/consensus/hotstuff/view_timing.go:527-545, stamps at engine.go:361) are propose = CommitQC to ProposalSent (commit handling on the serial output loop, view change, leader gate, miner build+seal, NotifyBlockSealed/proposeBlock including journalPrepareVote, an MDBX write under e.mu), r1 = ProposalSent to PrepareQC (included a fixed time.Sleep(50ms) in broadcastBlockData before every Proposal publish, plus the followers' votes), r2 = PrepareQC to CommitQC. Measured on S99's leader node0 (n42-r133; S98's logs were overwritten): at 22.5k propose 172 ms of which 157 is the miner's pacing wait from --block-interval-ms 250 (fill 0.4 ms, seal+broadcast 1.8), r1 60 ms = the 50 ms sleep + ~4 ms follower; at 163k propose 336 ms of which 285 is the lag gate waiting for the leader's own execution of N-2 (82% of views), the build itself fill 1.8 / lag build 2.9 / build-to-broadcast 7 ms, p90 tail 24 ms with outliers 165-3372 ms when the vote journal waits behind chain writes (a 221 ms hold seen), CommitQC-to-view-change 18 ms avg (p90 53) because CommitToCanonical ran inline on the output loop; r1 97.5 ms because followers vote 20-31 ms median (p90 87-126) after the Proposal, their vote journal waiting for MDBX behind the lag writer's 90-107 ms block write. There is no per-tx work on the leader (the frame build hashes frames only, P1b), so the leader build is not the cost. Commits: 9b6fb7d9 removes the 50 ms pre-Proposal sleep under vote-on-availability (N42_PROPOSAL_PUSH_GRACE_MS overrides; a follower that gets the Proposal before the push waits 100 ms before fetch-on-miss): emit-to-publish 50.36 ms -> 5.6 us, expected r1 -50 ms at every block size. 2e0099ae journals votes in their own MDBX store <datadir>/hotstuff-journal (default on under vote-on-availability, N42_VOTE_JOURNAL_SEPARATE=0/1 forces; recovery merges the journal into the chain record; with no chain record, as after hotstuff-reset/qs-hsreset, the journal is discarded so the reset tools work unchanged): under a 90-of-150 ms writer load on NVMe the vote journal p50 29-30 / p90 79-82 ms (chain store, matches the fleet) -> p50 0.3-1.1 / p90 4-9 ms. 18d5706a: under the lag gate committed blocks are canonicalized on a FIFO commit loop of their own so the view change and build trigger no longer wait behind them (commits carrying a reconfiguration still wait): trigger latency 80.6 ms -> 16.6 us, order kept. Nothing reverted; block/header/proposal bytes unchanged (no build code touched). Expected after, to be confirmed on the fleet: 163k r1 ~10-45 ms instead of 97, the propose tail loses ~18 ms commit wait and the journal outliers, r2 shrinks likewise; propose remains bound by the lag gate = the import pipeline, so the 163k cycle is the leader's own exec+root of N-2. Tests: hotstuff (also -race), miner, ingest, internal, node green; -race not run on internal/node. Caveats: the follower availability check decodes 163k txs (36-80 ms) and gates r2 only, it could check per-frame Entry.Gas and decode off the vote path but that changes a vote rule; an older binary restarted on a datadir with a separate journal sees only the chain record and could lose that view's vote commitment; internal/node/node.go imports were already out of goimports order. Binary n42-r134, HEAD 18d5706a, sha256 2a2267414453d57e2871508e0fec8d75ca644ddffe3d60cf4e8267898a85a647. S100 candidate = r134.

S99 result (2026-10-10, 35zzzby, n42-r133): warm-up 274k/326k; B1 255,367 / 355,883 execTPS at 163k blocks (blockTime 0.638/0.458 s), the new standing best at 163k (S98 274k); B2 296,117 / 287,967 (0.550/0.566 s); A legs 89.6-90k, interval-bound. S98's align stalls are gone (align 0.19 ms median / 6.0 p90, aheadDisc 0, 6-8 view timeouts per 7 min instead of 13 per window) but the 163k cycle is 458-638 ms, not ~200. Leader B1 phase table (node0 med/p90 ms): exec 95/286, root 70/198, collect 59/217, recov 62/530, hdr 7.9/56, setup 6.7/55, write 115/345, aheadWait 191/565, total 486/953, aheadUsed 250/251, aheadDisc 0, align 0.19/6.0; node6: exec 80/207, root 67/130, collect 51/92, recov 53/541, write 129/270, total 384/820. blockwrite phases total 142/429 (block 40, commit 19, qflush 13). Block gaps node0 median 558, p90 1151, 70 gaps > 1 s, 56.5 s in gaps > 2 s over the leg (max 26.7 s), node6 110 s; B2 check on node0 (545 blocks >= 100k txs): gap median 496, 43 gaps > 1 s, 53 s in gaps > 2 s (max 10.9 s), 3 view timeouts, memstats Sys 8.6 / HeapInuse 6.9 GiB. Leader view timing med/p90 propose 182/559, r1 69/174, r2 14/109, total 328/720; "leader waits for its lagged execution" 63 times. Write probes with heldMs > 200: 190 lines on node0, AccountHistory+StorageHistory+DbInfo 1.0-2.5 s (17-31k rows), the block batch 0.6-1.2 s at p90. Memstats node0 Sys 11.9 / HeapInuse 9.8 GiB in B1, 16.6 GiB Sys in warm-up (GOMEMLIMIT=7GiB), node1 7.9/6.1, A1 5.6/4.9; major faults B1 median 5.5k per 10 s vs A1 885 (3x in sum). Verdict: the bench's gains (P3d exec, P4b root and off-path write) reached the fleet only for exec (147 ms on 4 cores -> 95 on the fleet); root/collect/recov/write still sit serially on the leader's critical path (no lag-writer lines: the ordered writer, ingest-time sender recovery and P4b root are not in effect on the fleet path), and the leader's view is gated by its own N-2 import (propose 182 of which the lag gate), so the cycle is the import total ~486 ms, not consensus. P7 addresses exactly those items; the leader's memory and the history-index writer holds are part of it. Gate >= 500k not met.

P7 result (2026-10-10, commits 457360dc, b9877903, a6ca2b2d, 240a2c7b, 8926c863, 7bf00bad; evidence copied from S99's node0/node6 logs 20:48-21:06 ET). (1) The ordered writer was bypassed, not off: with N42_DEFERRED_CHECK_CONCURRENT=1 the block-push handler called InsertChain (a non-lag import) before the availability check had queued the block, raced the lag executor for bc.lock, joined the in-flight write and wrote synchronously: 385 of 543 163k imports on node0 (424 on node6) wrote on the critical path (write 142 ms), only 158/119 were handed off (4 ms). The phases line counted the full write in that case. There were no lag-writer lines because the writer logs only failures. On the fleet the writer's own write is 120-145 ms median (p90 270-480). Fix 457360dc: a lag-owned block (vote on availability, a frame or empty block) takes the sequential order on the push path, and the gossip path leaves a queued block to the executor; the phases line gains handed/handWait. (2) Admission recovery ran FIFO at ~530 frames/s against ~615 consumed (recoverPending 8-14k) and re-recovered frames the imports had already done. Fix b9877903: a per-block sender job starts at the block's enqueue, sharing one cursor with the import, which joins it (N42_SENDER_PREWARM_WORKERS, default half the recovery fan-out), and admission recovery skips frames a block has resolved. Bench with 80% of frames recovered at admission (N42_BENCH_ADMIT_SENDERS_PCT): wall 7.82-7.94 -> 6.80-6.90 s per 10 blocks, same roots. (3) The fleet's collect 59 is the ahead run's post-wave work (bench 40-69 on 4 cores), not the root's dirty-set collection (bench 14). The bench takes the P4b path (creditsDetached 22857, materialised 1), and runAhead sets it identically on the fleet. a6ca2b2d logs rootLock/rootOps/rootApply/publish/credits so S100 shows where the fleet's root 70 goes. (4) The writer holds were the deferred history fold (one transaction of 18-22k rows, 80-97 MB dirty, 0.4-2.1 s); 30 of 34 block-write waits > 50 ms on node0 fell in a fold second. Fix 240a2c7b: the fold commits in transactions of <= N42_HISTORY_FOLD_TXN_MS=30 ms of puts, split between keys, with a 5 ms yield, and the marker goes with the last one (re-folding is idempotent). The block batch itself holds 133/359 ms (p50/p90), mostly its own puts (Frames 17 MB, Receipt 1.45 MB, undo 1 MB a block). (5) The heap profiles are node3's (port 6093): live 3.6-4.4 GB = frame store raw 0.93-1.1 GB, QMDB map index 0.78, sender cache 0.13+0.66, decoded txs 0.6-0.8, receipts 0.15. node0's excess is three transient spikes (+60-140M objects in 30 s) during bursts of future-queued 163k blocks (node0 136, node6 12). Fix 8926c863: a block the lag executor holds is not future-queued. (6) aheadWait = aheadRun - (N-1's root tail + N-1's write + N's pre-steps). On the fleet aheadRun was 487 ms, of which recov+setup+exec+collect was only 230 (bench preamble 7-14 ms, fleet 170-250). The preamble's fleet-only wait is the chain-wide frame-resolve mutex, held across 163k decodes and up to 2 s frame pulls. Fix 7bf00bad: the lock is striped per block, and an aheadPre field is added. Tests green in internal, internal/sync, internal/frame/..., modules/state/..., modules/rawdb/...; internal/ingest TestFrameIngestBusyAtCap is flaky before P7 too (4/10 at 0fa814c2). -race: internal/sync and internal/frame/... in full, modules/state on history/root tests, internal on Lag|Sender|Resolve|Backfill|Future|Frame|Availability|Pipeline (HeapBounded skipped). Binary n42-r135, HEAD 7bf00bad, sha256 69bf73e877b3ab44143ccf2e7e6ecd90d44bab4a91f422e9f2185c984007652d. Expected leader cycle at 163k: the write leaves (-115), recov and the preamble shrink (-50 to -200), and the cycle becomes aheadRun + root up to publish, about 250-290 ms (~560-650k TPS). 1M needs exec 98 and the ahead run's collect 60 cut further.

## 6. What this does not claim

- 4M is not a measured number anywhere, including n42-rs. The honest
  comparison bar is 3.2M E=1 / 650k independent.
- The E=1 number is a benchmark of the execution layer under a consensus
  wrapper; it is not seven nodes verifying. The 1M independent number is
  the one to publish as chain throughput.
- Loopback hides 156 MB/block of body egress; frames-by-reference fix that
  only if frames are disseminated once and referenced thereafter.
