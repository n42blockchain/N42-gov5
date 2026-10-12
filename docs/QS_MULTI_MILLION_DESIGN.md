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

S100 result (2026-10-11, 35zzzbz, n42-r135; run 01:27-03:13 ET; t = seconds after win1 start = barrier release + 15 s, release B1 02:34:05, B2 02:51:13): warm-up 380,333 / 187,450 (blockTime 0.429/0.870 s); B1 380,333 / 108,667 (0.429/1.500 s, 40 blocks); B2 383,050 / 133,117 (0.426/1.224 s, 49 blocks); A legs 90.0k / 89.6-90.0k, interval-bound. Win1 383,050 at 163k blocks is the new standing best (S99 356k, which was its win2; S99 win1 255k); win2 is 109-187k in all three 163k legs, 2-3x below win1, with 163k-tx blocks at 50.0% occupancy throughout (supply never binds). Window arithmetic: measure-tps reads win1's 140 blocks through node0's RPC after the 60 s sleep, so win2 starts ~+89 s (B1) / ~+80 s (B2): node0's own head advances exactly 49 blocks over +80..+140 in B2 and 40 over ~+89..+143 in B1. Capture labels in the run log (S22/S95 "+20/+30/+60/+90/+150 s", "win1_start+15s") are anchored to a "flood signal" seen at 02:33:00/02:50:05, 65 s and 68 s before the real release, so the heap profiles are at +20/30/60 = -60/-50/-20 s (funding, identical profiles), +90 = +10 s, +150 = +70 s, goroutine debug=2 at +180/+240 = +101/+160 s. PHASE TABLES (163k blocks, ms median/p90; W1 = t 0-60, T = 60-89 B1 / 60-80 B2, W2 = 89-149 B1 / 80-140 B2). node0 B1 W1 (142 imports, handed 142, ahead 134, aheadUsed 134, aheadDisc 0, skipBusy 0, skipNotAvail +8): handWait 0.6/0.8, aheadPre 13.0/19.0, aheadWait 300/435, aheadRun 360/482, rootLock 0.0/0.0, rootOps 6.1/14.0, rootApply 24.2/39.0, publish 0.0/0.1, collect 57/109, exec 113/195, root 74/122, recov 121/217, hdr 13/27, setup 5.9/24, write 3.5/8.4, total 410/538. node0 B1 T (59): aheadWait 322/582, aheadRun 388/676, collect 65/117, exec 153/326, root 71/130, recov 97/225, total 460/704. node0 B1 W2 (49, handed 47): handWait 0.4/0.8, aheadPre 10.5/61, aheadWait 323/888, aheadRun 714/1149, rootOps 6.7/27, rootApply 26/139, collect 89/543, exec 188/756, root 97/532, recov 56/416, hdr 16/420, setup 16/90, write 7.7/50, total 677/1264. node6 B1 W1 (141): aheadPre 12.4/17.2, aheadWait 294/453, aheadRun 347/495, rootOps 5.6/13.3, rootApply 23.5/38.5, collect 58/104, exec 108/194, root 70/124, recov 131/209, hdr 11/26, setup 5.9/21, write 3.5/6.8, total 394/556; W2 (44): aheadWait 263/481, aheadRun 334/547, collect 65/131, exec 110/249, root 77/152, recov 70/175, hdr 11/30, write 4.9/12, total 384/637. node0 B2 W1 (141, handed 141, ahead 131): aheadPre 12.8/18.3, aheadWait 289/420, aheadRun 341/485, rootOps 6.3/13.0, rootApply 24.9/37.3, collect 58.5/99, exec 111/196, root 74.5/115, recov 131/192, hdr 13/26.5, setup 6.2/23, write 3.5/7.6, total 400/555; W2 (49): aheadPre 12.3/25, aheadWait 369/820, aheadRun 624/1116, rootOps 8.0/64.5, rootApply 31.7/148, collect 116/463, exec 272/577, root 126/507, recov 67/341, hdr 11/136, setup 14/234, write 11.3/42, total 595/1265. node6 B2 W1 (141): aheadWait 295/448, aheadRun 353/506, collect 60/112, exec 108/173, root 74/120, recov 142/216, write 3.5/10.5, total 399/558; W2 (92 imports): aheadWait 190/360, aheadRun 230/424, exec 79/165, collect 59/93, root 84/155, recov 44.5/168, hdr 4/22, write 3.8/8.1, total 320/512. WHAT P7 DELIVERED: (a) the write is off the critical path: handed on 142/142 and 141/141 163k imports in W1 on node0 (141/141 on node6) with write 3.5/8 ms and handWait 0.5/0.9 ms (S99: 115 ms, 158 of 543 handed); (b) rootLock 0.0 ms, publish 0.0/0.1 ms, the history fold bounded (below); (c) the block time went 0.46-0.64 -> 0.43 s and the leader import total 486 -> 410 ms. WHAT IT DID NOT: recov is 121-142 ms in W1 (S99 62), not near 0, so the sender job is not hiding the recovery (it falls to 45-70 ms in W2 only because ingest stalls and admission catches up); aheadWait is 289-300 (S99 191) because the ahead run itself is 340-360 ms (aheadPre 13 + recov 121 + setup 6 + exec 113 + collect 57 + ~50 other), so the cycle is the ahead run, not the predicted 250-290 ms; exec 111-113 (S99 95), collect 57-59 (S99 59) unchanged. The root's 74 ms is rootOps 6.1 + rootApply 24.2 + rootLock 0 + publish 0 = 31 ms inside ComputeRoot plus ~40 ms (39.5/73 p90) of Finalize outside it; creditsDetached/creditsMaterialised never appear on any of the 995 163k lines, so the P4b detached-credit path is not engaged on the fleet and the 40 ms is unattributed by these fields. Prediction 560-650k not met; gate >= 500k not met. THE WIN2 COLLAPSE: it is one node's import path surfacing as leader view timeouts. (1) Every view that timed out is a node0 tenure view (view // 8 mod 7 == 0): B1 14392-14394, 14448, 14504-14505, 14560-14563; B2 17304-17305, 17360-17363, 17416-17417, 17472-17474, 17528-17530 (17527 timed out on node0 alone); the first at 02:36:09 (t=109, B1) and 02:53:10 (t=102, B2); W2 holds 3 (B1) and 3 (B2) of them as seen by the followers, 12 s each. Followers' gaps: B1 W2 node6 median 0.42 s but a 32.1 s and a 9.9 s hole (sum of gaps > 2 s 42.0 s, 44 blocks); B2 W2 node6 median 0.33 s, one 18.5 s hole, 92 blocks (~250k TPS by the followers' own count). Node0's gaps: B1 W2 median 0.91 s, p90 2.50, 23 gaps > 1 s, 23.6 s in gaps > 2 s, max 8.7 s; B2 W2 median 0.74, p90 1.51, 14 > 1 s, 9.2 s > 2 s, max 3.9; W1 on every node: median 0.40-0.41, p90 0.55-0.57, 0 gaps > 1 s. (2) "leader waits for its lagged execution": node0 25/8/4 (B1 W1/T/W2), 22/8/9 (B2): in W2 node0's gate is waiting on an N-2 import that is 20-44 blocks behind its peers (head lag vs node6 at t=100: 20 blocks B1, 18 B2; peak 27 B1 t=110, 44 B2 t=140), so it cannot build; the wait ends in the 12 s timeout. (3) The measured win2 is node0's own head (measure-tps polls port 20012 = node0), which lags the followers: B2 node0 49 blocks vs followers 92 in the same 60 s. (4) FIRST SYMPTOM, time order on node0 (B1): t=60 (02:35:20) win1 ends and measure-tps starts fetching the 140 window blocks from node0 (an eth_getBlockByNumber/RPCMarshalBlock goroutine is running in the debug=2 dumps at t=101 and t=160; 40 MB of jsonrpc handler allocations in the +150 heap profile at t=70; the A legs, whose blocks are tiny, never collapse); t=70 (02:35:30) node0-only rise of exec 105 -> 188 ms and aheadRun 330 -> 479 ms (node6/node3: 118/101, 342/346) and node0 falls 3 imports behind node6 at t=75 (02:35:35; 10 at t=95, 21 at t=100) while block arrivals, votes and Decides still match node6 (differences 0-4 until t=95); t=76 (02:35:36) "frame blocks: orphan frames fillable again" (326 frames), node0 only, 22 times in W2 (B2: first t=86, 21 times); t=89 heap; t=94-98 "block push: received" (node0 only, 14 B1 / 19 B2 in W2) and fetch-on-miss 29 / 27 in W2 (node6 2-4); t=97-107 write probes held 772 / 2064 / 2776 ms with commitMs 14-24 (a process stall, not a commit); t=97 node0's own tenure (views 14392+) starts, no proposal, timeout at t=109 (TC formed on node0 02:36:09), then 14393 (t=121), 14394 (t=133), proposals resume t=145. B2 is the same sequence 10 s earlier in the cycle: exec 142 -> 195 at t=70-80, orphan frames t=86, push-received t=94, views 17304/17305 time out at t=102/114, 17360 at t=139. The other six nodes stay at aheadRun 334-369 ms until their stall. (5) Causality is inferred, not proven: no RPC latency is logged, so the poll's cost on node0 is read from the timing, the goroutine dump and the A-leg control; the clean test is to poll a node that is not the first of the rotation, or a header-only count, in the same round. WRITE PROBES (heldMs > 200), node0 B1 W1/T/W2: 17/10/33 (> 500: 0/0/5, > 1000: 0/0/2, max 307/420/2776), B2 35/11/23 (> 500 0/0/7, > 1000 0/0/2, max 370/406/1519); node1-6 B1 W2 3-8 (max 240-411), B2 W2 5-11 (max 241-329). The W1 holds are the block batch (Frames 17.3 MB + qmdbEntries 13-48k rows, payload 21.4-21.9 MB, heldMs 207-440, commitMs 15-350); the history fold is bounded now (AccountHistory 66-706 rows, <= 0.12 MB per transaction vs 17-31k rows / 80-97 MB in S99) and the W2 holds of 483-510 ms on it have commitMs 0-1, i.e. the writer thread was not running. MEMORY (memstats exist for node0 and node1 only, 30 s samples; HeapInuse / Sys GiB-ish decimal GB, GCs per 30 s): node0 B1 5.87/7.28 (t=-3), 6.08/8.11 (27), 6.01/8.28 (58), 6.53/8.29 (89), 8.56/10.66 (121), 6.67/10.79 (151), GCs +8, +24, +29, +36, +41, +43, +56; node1 B1 6.45, 6.10, 6.11, 6.15, 4.84, 6.57 / Sys <= 8.41, GCs +25, +31, +29, +17, +9, +28; node0 B2 5.69 (12), 6.54 (43), 6.27 (73), 7.58 (104), 10.85 (135, Sys 12.3), 10.10 (167), 6.25 (198, Sys 13.5), GCs +18, +27, +31, +42, +52, +36, +37; node1 B2 5.5-6.5, Sys <= 8.75, GCs +18 ... +2. GOMEMLIMIT is 7 GiB (7.5 GB): node0 goes over it only during its own stall. GC pauses from the PauseNs ring: node0 W2 83 (B1) / 103 (B2) GCs, 21 / 25 pauses > 10 ms, max 70 / 137 ms (after W2: 149 / 542 ms), W1 52 / 50 GCs, max 16 ms; node1 W2 26 / 55 GCs, max 7.5 / 6.1 ms. Heap profiles (node0, inuse_space): +20/30/60 (before release, identical) 1.08 GB = QMDB map index 0.80, sender-cache slab 0.13, nothing else; +90 (t=10) 3.74 GB = readFrame 1.07 (frame store, 20.2k frames pinned), map index 0.78, senderCachePut 0.22, decode 0.52 cum; +150 (t=70) 4.66 GB = readFrame 1.11, map index 0.80, senderCachePut 0.77, decode 0.70 cum, CommitToCanonicalWith 0.17. Growth +90 -> +150: +0.92 GB, of which senderCachePut +0.52 (10.0M live objects, a bounded cache filling to its 16.8M slots), decode +0.16, parallel scratch +0.06; readFrame, map index flat. So the leader heap is flat per block (not the S97 pattern); node0's excursions to 8.6 / 10.9 GB are transient and follow its own lag (bursts of orphan-frame fills and pulled blocks), they do not precede it. Box: Cached falls 92 -> 52 GB across the release (avail 63 -> 45 GB, no swap), pgscanDirect 2.1M in the release second (B2 0.8M), then major faults 250-320k per 10 s (B1 02:34:21-02:36:05, B2 02:51:35-02:53:07) with no change at the W1/W2 boundary and 33-100k during the stall; not the trigger. INGEST / STORE: no "store full" line anywhere; the frame store sits at ~20.2k frames / 1.07 GB on every node in W1 (evicted 7.5k per 10 s), recoverPending 12-14k, pending <= 652; busy replies W1/T/W2 node0 2180/970/2777 (B1), 2774/1103/2743 (B2) vs node1-6 1.1-2.0k/285-985/21-162 and 1.3-2.5k/323-696/0-3, i.e. in W2 only node0 still refuses frames; accepted frames per node W2 16.3k (B2, equal on all) / 18.3k (B1) vs 46-54k in W1 (the chain-driven supply, below). Range requests ("remote returned error code 2: invalid block number") 40-80 per window on every node in W1 and W2, 385 over the leg: baseline, not a W2 change; fetch-on-miss and "committed block not executed locally" on node0 4 / 4 / 29 and 10 / 12 / 17 (B1), 2 / 3 / 27 and 16 / 6 / 46 (B2) vs 2-4 / 6-11 on node6. There are no pool-size lines in n42.log in this build. GENERATORS (40 logs, 8 per leg): timeouts=0 and failed=0 on every generator in every leg, stderr empty, presign 55.6-56.4 s per generator on warm-up/B1/B2 (37.1-37.5 s on A), funded in 2-12 s; busy retries 3.83-3.87k per generator in B1 (30.5k total), 4.47-4.51k in B2, rising steadily (~500 per 10 s of sends) with no step; send rate 380-400k/s in aggregate through the first 80 s after release in both legs, 316-351k at 90-100 s, then 33-200k/s from 110 s (B1: 33k/line at 120 s, 50k at 160 s; B2: 15k at 150 s), i.e. the generators follow the chain's hole and recover with it; each flood sent 9.4-10.6M of its 12M presigned txs, so the corpus never ran out and blocks stayed full at 163k. Supply is not the mechanism. SAFETY: 0 "bad block" lines on any node over the whole run (01:27-03:13), 0 panics, 0 fatal, one "runLoop error: context canceled" on node0 at the 02:58:49 shutdown, 35 "stopped clean" lines (7 nodes x 5 legs), "Opening database" 5 per node (one per leg, no unplanned restart); the only error-level line is "hotstuff: committed block not executed locally" (node0 533 plus the one runLoop line, node1-6 115-149, all requests for a missing block, none a mismatch); the 7 nodes hold the same 20,777 blocks (13,652,363-13,673,139) with the same state root for every block (0 numbers with differing roots), no node left behind, final drained heads settle after 60 s per leg. VERDICT: P7 delivered what it targeted on the import path (write handed off on 100% of 163k imports at 3.5 ms, lock and publish at 0, block time 0.43 s, win1 383k = new best) but not the recov/aheadWait items (recov 121-142, ahead run 340-360 ms), so the cycle is ~0.41 s and the 560-650k prediction is not met; the win2 collapse is not P7, supply, store/transport or OS memory but a single node's import (node0, the RPC-polled node) falling behind from t=+70 s and then stalling its own leader tenure with 12 s view timeouts, first visible as node0-only exec/aheadRun growth at t=70 (02:35:30 B1) and orphan-frame fills at t=76; the fleet has no import headroom (ahead run p90 480-505 ms against a 430 ms block time), so one node losing ~30% turns into whole-chain holes. Next: (1) take the harness's poll off the rotation (poll a node's RPC with a count-only method, or fetch the window by header number and tx count, or stop win2's fetch before win2) and rerun S100's binary, to see whether win2 returns to ~380k; (2) the ahead run (recov 121 -> the sender job, exec 113, collect 57) is the remaining critical path, and the 40 ms of root outside ComputeRoot needs its own fields.

S100 CPU profile and the recovery wall (2026-10-11): profiles /data/blockchain/wr-pprof/r35zzzbz-{B1,B2}-{win1,win2}-node{0,6}-cpu.pb.gz, 20 s each. The harness's "win1" profile is taken before the flood reaches the chain (85 blocks, 0 txs: 1.8 CPU-s per 20 s, 0.09 threads, all blst vote verification and libp2p), so only the "win2" profiles carry load: 48 blocks, 7,824,000 txs in 20 s (~391k TPS). Harness note: the profile timing must follow the barrier release, not the flood signal. LOAD: under load every node burns 652-664 CPU-s per 20 s = 32.6-33.2 busy threads, leader = follower; 7 nodes = ~231 of the box's 256 threads (EPYC 9B45, 128 cores / 256 threads) before the 8 generators, so the fleet is at saturation at ~391k TPS. That is the import headroom S100 lacked and why one extra load (the RPC poll) tipped node0 over. WHERE THE CPU GOES (node0 B1, follower within 1 point): secp256k1 sender recovery 64.3% (427 s: crypto.Ecrecover 420 s, split between ingest frame.RecoverSenders 257 s and the block's internal.senderJob 176 s), GC 9.8%, write (mdbx, etl, recsplit, txlookup, sort) 7.6%, Block-STM 6.8%, RLP / tx decode / Tx.Hash / From 6.1%, root (qmdb/commitment) 2.9%, libp2p/pubsub 0.8%, scheduler 0.1%; top flat runtime.cgocall 63.5%; mutex profiles are empty (not enabled). COST PER SIGNATURE: 427 s / 7.824M = 54.6 us CPU, while a single libsecp256k1 recover is 25-35 us, so each tx is recovered about twice per node (admission and the P7 sender job) and/or suffers hyperthread contention; overall per-tx CPU per node is 85 us. ARITHMETIC FOR 1M TPS with 7 nodes on this box: the budget is 256 threads / 7M sig-s per second = 36 us of CPU per tx per node for everything; recovery alone at 54.6 us needs 382 threads, deduplicated at ~27 us it needs 191, leaving nothing for execution, write, GC and the generators. With every node recovering every signature, 1M TPS is impossible on one box regardless of pipeline work. The scored target therefore needs two kinds of change: (1) P8a, one recovery per tx per node, worth ~22 us/tx; (2) P8b, frame-level sender attestation: each frame is deterministically assigned to a committee of f+1 = 3 of the 7 validators, who recover its senders and sign (frame digest, senders digest); other nodes accept a frame's senders when f+1 matching attestations are present (f+1 agreeing results of a deterministic function guarantee at least one honest verifier, so the senders are correct under the f=2 model), verify 3 signatures per frame instead of 163k per block, and fall back to local recovery when attestations are missing at execution time; expected recovery cost per node falls to 3/7, ~12 us/tx average. After (1)+(2) the per-tx budget is ~48 us against the 36 us needed; the remaining ~12 us must come from GC (9.8%: presized version lists, receipts), decode (6.1%: decode once at admission, keep the message) and the write (7.6%). HARNESS FOR S101: the measure-tps RPC poll moves off the leader, and CPU profiles are taken 30 s after the barrier release.

P8 result (2026-10-11, commits 3b5dd7c6 P8a, 86ee6655 + ea3ec8fe P8b; binaries n42-r136 = P8a, n42-r137 = P8a + P8b). P8a FINDING: the fleet was not recovering each transaction twice. The S100 B1 win2 allocation profiles cover the same 20 s as the CPU profiles (02:34:31-51) and count Ecrecover's one allocation per call: 8.06M calls on node0 and 7.62M on node6 for 7.824M block transactions, 1.03 and 0.97 per transaction (sampling noise both ways); admission 4.63M, the sender job 3.42M, the import's join 0.24M. 420 CPU-s / 8.06M = 52 us per recovery against 25.8 us measured on the same box at load ~20 (BenchmarkEcrecover): the doubling is SMT and all-core contention at 231 of 256 busy threads, not a second recovery. The P7 skip did leak at the edges: admission checked NeedsSenders once before a 15-30 ms frame recovery, so a block resolving the frame meanwhile recovered it again; the leader's build never claimed its frames; the serial import recovered per transaction alongside execution. A test reproduces 1.07-1.24 recoveries per transaction. P8a CHANGE: each stored frame carries a recovery state (none / running with a wait channel / done); store.BeginRecover gives exactly one caller ownership, the others the result or a channel to wait on; admission owns or skips; a block's sender job, the import (joining the job or running one ad hoc), the leader's frame build (spans from its picks) and the serial import of a frame block (now a barrier before execution) all go frame by frame through the claim, with spans from the frame headers (FrameSpansOf; a body with dropped entries falls back to per-transaction recovery). TestFrameSenderRecoveredOncePerNode: 1.000 recoveries per transaction in all four cases (admission first, racing, a one-worker backlog, Block-STM). Batching recoveries per cgo call is not worth it: the cgo transition is 65 ns of a 25.8 us recovery (0.25%), and libsecp256k1 has no batch recovery. Recovery workers: per node admission 16 (N42_FRAME_RECOVER_WORKERS) + 14 per queued block job (half of GOMAXPROCS x 3/4) + the import's 28 are goroutines under GOMAXPROCS = --pprof.maxcpu 37, so 7 x 37 = 259 threads on a 256-thread box: QS_MAXCPU=36 (252) removes the oversubscription; the per-recovery inflation itself only falls as the box's busy threads fall, which is what P8b does. Expected per-node recovery CPU with P8a alone: 1.00 x txs x 25.8-52 us = 10-20 cores at 391k TPS, 26-52 at 1M.

P8b DESIGN (as implemented, internal/frame/attest). Assignment: the current epoch's validator set (HotStuff CurrentValidatorSet) sorted by address ascending; frame h's committee is indexes (h mod n + k) mod n for k < size, h read as a 256-bit big-endian integer, size = N42_FRAME_ATTEST_COMMITTEE (default 3) raised to f+1. Digest: keccak256 of the concatenated 20-byte senders in the frame's DecodeTxs order (zero address = signature did not recover). Message (deviation: batched): a member collects its attestations for N42_FRAME_ATTEST_FLUSH_MS (default 20 ms, at most 256 frames) and sends one batch, body = attester 20 | count u16 | per frame (frameHash 32 | digest 32 | n u16 | n x 20-byte senders) | sigLen u16 | BLS signature; the signature (the member's HotStuff BLS key) is over keccak256("n42-frame-attest-v1" || attester || count u32 BE || (frameHash || digest) per frame), the senders bound by the digest. Batching replaces three signature checks per frame by one per batch per member: a 32-frame batch costs 1.9 ms to decode, verify and hash (BenchmarkBatchVerify), ~0.12 us per transaction per member's batch, and a list is hashed once per (frame, digest). Transport: FAT1 ("FAT1" LE magic | body length u32 | body, no reply) on the ingest listener, one persistent connection per peer (N42_FRAME_ATTEST_PEERS, default N42_FRAME_PULL_PEERS minus this node), queue of 1024 messages, a slow or dead peer loses messages and recovers those frames itself. Every member sends the full list (3 x 10 KB per 500-tx frame per receiver, loopback). Acceptance: a batch from a non-validator or with a bad signature is dropped; an entry from a non-member, or whose list does not hash to its digest, is dropped, logged (rate-limited) and counted; f+1 attestations of one digest from distinct members plus a list hashing to it accept the frame: the senders go into the frame store (SetSenders under the admission signer), so admission, the block's job and the leader's fill take them like admission-recovered senders. Under f = 2 f+1 = 3 = the whole committee, so a single silent or byzantine member sends its frames to the fallback, never to a wrong list. Fallback: a non-member skips the frame at admission; a block's sender job defers it to its last pass and then recovers it locally if it is still not accepted (counted as fallback in the "frame attestations" log line, every 10 s, with batches, entries, accepted, published, badSig, notMember, badDigest). A block never executes with senders that are neither recovered locally nor attested by f+1; block hashes and state roots are untouched (attestation is off-chain), and leader rotation does not interact (per frame, not per block). TestFrameAttestationReplacesRecovery: a validator of seven recovers exactly its committee's frames (2,600-3,000 of 5,600 transactions over 56 frames, 3/7 expected) and none of the attested ones; with one member withholding every third frame, exactly the withheld frames fall back (fallback = withheld); executed roots equal a run without attestation. Expected per-node recovery CPU with P8a + P8b: 3/7 of the transactions, 4.3-8.7 cores at 391k TPS and 11-22 cores at 1M (25.8-52 us per recovery), plus ~0.4 us per transaction of batch verification. Env for the next round: N42_FRAME_ATTEST=1 (P8b; leave unset for a P8a-only round on n42-r136), N42_FRAME_ATTEST_COMMITTEE=3, N42_FRAME_PULL_PEERS as today, QS_MAXCPU=36.

S101 result (2026-10-12, 35zzzca, n42-r137 = P8a + P8b; run 23:37 ET 2026-10-11 to 01:18; barrier releases warm-up 00:13:14, B1 00:42:04, B2 00:58:06; win1 opens at release + ~15-20 s): warm-up (ON) win1 516,167 execTPS (190 blocks, 0.316 s) / win2 0 txs (240 empty blocks, 0.25 s); A1 90.0k / 90.0k; B1 (ON) win1 546,050 (201 blocks, 0.299 s, 50.0% occupancy) / win2 0 txs (240 empty blocks); B2 (OFF) win1 388,483 (143 blocks, 0.420 s) / win2 149,417 (55 blocks, 1.091 s); A2 90.0k / 90.0k. First windows above the 500k gate: B1 win1 546,050 and warm-up win1 516,167, 163k-tx blocks, attestation ON; OFF repeats S100 (388k vs S100 383k). THE WIN2 ZERO IS NOT A STOP: the chain mined the whole 96,000,000-tx corpus (8 x 12M, 192,000 frames of 500 txs) in 589 consecutive 163k blocks from 00:42:04 (block 13666314) to 00:45:01 (13666902, 156,000 txs, the last frame), 177 s = 542k tx/s on average, 521-554k per 10 s from 00:42:10 to 00:44:30, 489k in the supply tail; warm-up identical (96,000,000 txs, 589 blocks, 00:13:14-00:16:11). Then the leader's lag builds read txs 0, frames 0, localFrames 0 (e.g. 13666903 txs=0 frames=0 storeFrames=5854, 13666913 txs=0 frames=0 storeFrames=1942 draining to 0): supply was empty, not frames excluded (every build before had frames=326, localFrames=0, e.g. 13666900 txs=163000 frames=326 storeFrames=6832 fill=1.2 ms). Generators (8 logs): all DONE frames=24000 txs=12000000 failed=0 timeouts=0 in 2m43.6-2m46.0 (72.3-73.3k tx/s accepted each), busy 5,553-5,657 retries each, stderr 0 bytes, aggregate send 536k/s mean over t+5..150 s (pushed back by busy replies from t+5 s: it matches the chain), corpus 12M each fully sent by 00:44:50. Why win2 saw none: measure-tps.sh is sequential (sleep 60, then fetch every block of the window with eth_getBlockByNumber, 163k hashes each, from node6, then open the next window); win1's 201-block fetch took >= 100 s (win2 contains no tx, so its start is >= 00:45:01; its end printed after the 00:46:04 goroutine capture), longer than the 177-s corpus. Win2 is a harness artifact (the same in warm-up), the repo's measure-tps.py samples all boundaries before enumerating blocks for exactly this reason. Windows in the run log that carry a tps are valid; the profile windows (win1 = release + 31-51 s, win2 = +90-110 s) were taken under load (ON 544k then 520-540k/s; OFF 391-424k then 340k/s). ATTESTATION COUNTS (frame attestations lines, committee 3, f+1 = 3, per node over the leg): 192,000 frames per node = 109.4-110.1k accepted from attestations (57.3%) + 81.9-82.6k recovered locally (42.7% = the 3/7 committee share) + 30-46 fallback (0.02%, a non-member recovered before the batch arrived); published 81.95-82.62k per node = 575,978 in total = 3 x 192,000 (every frame signed by exactly its 3 members), entries received 493.4-494.1k per node = 576,000 - own; batches 42.15-42.21k per node; badSig 0, notMember 0, badDigest 0, badBatch 0 on all 7 nodes; no "senders do not match the signed digest" line anywhere. Per 326-frame block: ~187 frames attested, ~139 recovered by a committee member, 0.07 fallback. In OFF (B2) there are no attestation lines and every node recovers (ingest recovered 140-141k of 168k admitted frames per node; node6 123.8k). B1 WIN1 vs B2 WIN1 (163k imports, ms median/p90; node0 / node6; B1 202 / 201 imports, B2 145 / 144; t = release + 16..76 s ON, + 15..75 s OFF): exec 85.3/125.7, 81.3/121.0 vs 104.2/197.9, 103.4/172.1; recov 48.1/64.8, 42.6/61.3 vs 130.0/217.8, 123.3/213.4; collect 62.2/85.8, 63.0/86.1 vs 52.8/86.8, 55.4/99.1; aheadPre 12.6/18.3, 13.4/18.9 vs 11.7/16.5, 12.5/16.1; aheadRun 232.9/293.4, 225.5/287.9 vs 328.7/475.3, 320.2/511.3; aheadWait 190.7/246.9, 180.3/243.4 vs 278.9/432.7, 270.2/451.0; root 75.6/104.2, 77.3/108.4 vs 62.3/108.6, 64.6/109.6; handed 100% of imports in both legs, handWait 0.7/1.0 vs 0.5/1.2; setup 5.5/16.8 vs 5.6/16.8; write 4.1/7.2 vs 3.5/15.0; hdr 9.3/21.7 vs 11.1/26.3; total 284.7/354.8, 284.6/349.2 vs 402.2/542.8, 388.0/562.1. Cycle: import-end gap median 286 / 288 ms (p90 365 / 382, max 527 / 679) vs 407 / 392 (p90 568 / 566, max 788 / 806); view duration median 283 / 281 ms vs 398 / 396 (p90 373 / 369 vs 566 / 569); block time 0.299 vs 0.420 s: the 0.42 -> 0.30 s fall is confirmed. Views (leader / follower, node0): propose 182/348 vs 308/606, r1 42/58 vs 48/125, r2 38/58 vs 40/78, leader total 269/421 vs 403/684; follower recv 234/309 vs 338/495, total 283/366 vs 397/554. WHAT MOVED: recov 130 -> 48 ms (-82, attestation took 57% of the frames' recovery out of the import), the ahead run 329 -> 233 (-96) and aheadWait 279 -> 191 (-88) with it, exec 104 -> 85 (-19: less contention), total 402 -> 285 (-117); what did not: aheadPre 12-13, setup 5.5, handWait 0.7, hdr 9-11, write 3.5-4.1 (off the critical path), collect +9 and root +13 ms (62 -> 76, more blocks per second share the same cores, not a regression of the root work: parallel-block applyMs 35 vs 30). The ahead run is still the cycle (233 of 285 ms), now recov 48 + exec 85 + collect 62 + pre 13 + setup 6. CPU PROFILES (20 s each, binary n42-r137; CPU-s per 20 s / busy threads per node; fleet threads = 7x): B1 win1 549 / 27.4 (node0), 549 / 27.4 (node6) = 192 fleet threads; B1 win2 503 / 25.2, 539 / 27.0; B2 win1 648 / 32.4, 642 / 32.1 = 226 fleet threads; B2 win2 641 / 32.1, 650 / 32.5. Per block: ON 8.2 CPU-s (50 us per tx), OFF 13.6 CPU-s (83 us per tx; S100 85). secp256k1 recovery (_Cfunc_secp256k1_ext_ecdsa_recover, cum): B1 win1 234 s (42.6%) / 244 s (44.4%) vs B2 win1 404 s (62.4%) / 406 s (63.2%); per block 3.5 vs 8.5 CPU-s (ratio 0.41 = 3/7), per recovered signature ~50 us in both legs (ON 234 s / (67 blocks x 163k x 3/7), OFF 404 s / (47.6 x 163k)), so attestation removed the count and not the unit cost; frame.RecoverSenders cum 257 s (46.9%) vs 278 s (43.0%): with ON all of the recovery sits in the ingest-side committee recovery (the block-side join is the 48 ms recov), with OFF 278 s is ingest-side and ~126 s more is the block's sender job. Attestation cost: attest.Pool.HandleWire 7 CPU-s (1.3%) per node per 20 s (batch decode, BLS verify, accept), frame.Frame.Verify 5 s (0.9%), consensus VerifyBLSSignature 5 s (0.9%); OFF has no HandleWire. Other shares ON win1 / OFF win1 (node0): GC mark worker 29 s (5.2%) / 36 s (5.5%); mallocgc 30 (5.6%) / 28 (4.3%); Block-STM executeParallel 50 s (9.1%) / 40 s (6.1%); applySenderHints 37 s (6.8%) / 21 s (3.2%, more hint fills per second when the sender job is short); tx decode (DecodeEthereumTransaction) 37 s (6.7%) / 30 s (4.6%); block write (runLagWrite + mdbx Update) 5 + 11 s (3.0%) / 3 + 6 s (1.4%); keccak 15 s (2.7%) / 14 s (2.2%); background txindex segment build (txlookup.BuildSegmentFile, recsplit) 57 s (10.4%) / 43 s (6.6%), background txindex segment sealing, present in both legs. Under ON recovery is still 43-44% of the node and the box is at 192 of 256 threads before the generators. SAFETY: 0 "bad block" / panic / fatal lines on any node over the whole run (zcat of every rotated log plus the live log), "Opening database" 5 per node (one per leg, no unplanned restart), 35 clean stops; B1: 847 views committed in 00:42:00-00:46:00, every one present on all 7 nodes with the same block hash (0 differing; deferred execution carries the parent's roots in the header, so equal hashes are equal state roots), zero timed-out views (no TC formed after 00:32:48 startup), a single 1.36-s view gap on all nodes at 00:44:45 (14496 -> 14497); badDigest 0 and notMember 0 on every node, i.e. no node saw an attested sender list that differed from its digest, and f+1 = 3 matching digests were needed for every accepted frame; B2: 453 views, 0 differing hashes (node6 missing 5 at the edge of its stall). Range-request warnings ("invalid block number", "Could not retrieve blocks") and "committed block not executed locally" are baseline noise in both legs (B1 49-97, B2 33-57 per node, node6 209 in B2). Box: avail >= 60 GB and AnonPages <= 59 GB through B1 (7 x GOMEMLIMIT 7 GiB + generators 203 MB each), Cached fell 90 -> 50 GB at the release as in S100, no OOM. B2 WIN2 COLLAPSE (OFF): it recurs, and it moved with the RPC poll from node0 to node6. node0 did not fall behind: its 163k import total p50 is 402 ms (00:58:21-00:59:25), 431 (59:25-59:50), 405 (59:50-00:20), 296 (01:00:20-02:00), 260 (01:02-03:40), the same as nodes 1-5 (283-299, 247-260); node6 alone goes 388 -> 475 -> 613 -> 722 -> 831 ms (n = 77 and 88 imports in the last two spans), keeps 85 "orphan frames fillable again" and 165 fetch-on-miss lines against node0's 3 and 5, and has 3x the busy replies of node0 at the leg total (14.5k vs 3.9k). First symptom, time order: win1 ends ~00:59:21 and measure-tps starts fetching its 143 blocks from node6; 00:59:30-40 node6 import p50 441 -> 566 ms while node0 stays 391-442; 00:59:40-00:59:59 node6 heap/alloc surge (win2 profile: mallocgc 111 s = 17.0%, GC assist 44 s = 6.7%, GC mark 68 s = 10.4%, vs node0 44 s / 15 s / 61 s); 00:59:59 view 17632, node6 is leader and logs "leader gate phases outcome=lag-wait" + "leader waits for its lagged execution" with its block 13669963 import at total 4,391 ms (exec 1,587 ms); 01:00:04 TC formed (6 s), view 17633 node6 leader again, TC at 01:00:16 (12 s), proposal at 01:00:17 (propose 1,496 ms, total 1,791 ms), then every return of the rotation to node6 times out on all 7 nodes: views 17688-17692 (01:00:34-01:01:46: 6, 12, 24, 30 s), 17744-17746, 17800-17803, 17856-17859 (backoff 4-30 s, last TC 01:04:27), 13 TCs in all, 0 in B1. The measured win2 (55 blocks, 1.091 s) is the fleet waiting on node6's tenures. So the S100 hypothesis (the polled node is the one that falls behind and stalls its tenure) is confirmed in direction: the collapse followed the poll from node0 to node6. But B1 contradicts "the poll alone is enough": node6 served the same 201-block fetch for >= 100 s under ON (00:43:20-00:45:10) with import p50 285 / 304 / 281 ms in the three spans, zero timeouts and 7 orphan-fill lines in the whole leg, because ON leaves the fleet at 27 busy threads and a 0.30-s cycle; OFF runs 32 threads (226 of 256) and a 0.42-s cycle, where the poll tips the polled node. Conclusion: poll cost is real and fatal only without headroom; the clean fix is still a count-only poll. GATE: >= 500k is met in win1 only (546,050 and 516,167 at 163k blocks, ON); win2 never measured it (empty by harness); the chain's own timeline shows 542k sustained over 177 s, so the claim is supported but unmeasured by the harness for a second window. NEXT: (1) harness: sample the win1/win2 boundaries (eth_blockNumber + timestamps) before fetching any block, or fetch count-only (eth_getBlockTransactionCountByNumber + gas from the header), from a node that is not first in the rotation; keep 12M per generator (96M lasts 177 s, enough for 15 + 2 x 60 s only if no fetch intervenes) or raise pertx to 18M for margin; (2) rerun ON-only legs (B1, B1b) so win2 is measured under load; (3) then the ahead run is the cycle: exec 85 + collect 62 + recov 48 + pre 13 ms; with recovery at 43% of the node, the next CPU is Block-STM 9%, decode 6.7%, hint fills 6.8%, GC 5.2% and the txindex tail 10.4% (turn it off for a bench leg).

P9 result (2026-10-12, commits f8786256, 1c3cb2a9, bffa6a30, 188f369b, 08eb91a7, 0cb1284d; binary n42-r138, HEAD 0cb1284d, sha256 826bb06c1088ba663aad11e268253a78bc7ec350ae1ffe32bb8657cc6ef554ea). Goal: a node's CPU per transaction from ~50 us to <= 36 us. BASE (S101 B1 win1 node0, attestation ON, n42-r137): 549 CPU-s per 20 s over 67 x 163k = 10.9M txs = 50.3 us/tx, 1% of the node = 0.50 us/tx; allocation 46.9 GB per 20 s. Fleet estimates below are the S101 share times the bench ratio; nothing here is fleet-measured yet. (1) TXINDEX SEALING 57.05 s (10.4%, 5.2 us/tx): no rebuilds (33 "txindex sealed" lines in 00:42-00:46, every segment 1,141,000 keys = 7 x 163k, ~6 s wall each, ~9.5 segments per 20 s) and no extra hashing (the tail holds the tx hashes; murmur 0.2 s); the cost is RecSplit itself: the leaf bijection search 34.1 s (60%), the etl collectors' sort.Stable through an interface + flush 18.3 s (32%), Put 2.0, merge 2.0. Changes: RecSplitArgs.InMemory (keys in slices, counting sort by bucket, each bucket sorted alone in the etl order; index byte-identical to the etl path, TestInMemoryMatchesEtl with forced spills), used by the sealer; leaf size 8 -> 5 for sealer segments (the leaf size is in each index header, old leaf-8 segments read as before). BenchmarkBuildSegmentFile (1.141M keys, GOMAXPROCS=4): 2,844 ns/key (395 MB allocated per build) -> in-memory 2,200 (71 MB) -> leaf 5 672 ns/key, -76%; index 35.84 -> 36.06 bits/key (+0.6%; leaf 6 776 / 35.99, leaf 4 617 / 36.27). Expected 10.4% -> ~2.5%, -4.0 us/tx. (2) applySenderHints 37.3 s (6.8%, 3.4 us/tx): per transaction a Tx.Hash (11.9 s), a sender-cache probe (8.5 s) and, on the miss, the pool's read lock (GetTx 14.7 s, RWMutex reader-count contention across 28 workers); it filled 0 senders in 587 of 589 imports (2 nonzero at the leg start), because a frame block's senders come per frame from the store or the attestations, which it never consulted (it checked From(), not the memo). Change: skipped when the block has frame spans (parallel and serial import; recoverBlockSendersFor applies the frame senders in bulk), and it skips any tx whose memo already holds a sender under the block's signer (Transaction.HasSenderMemo). BenchmarkApplySenderHintsMiss: 252 ns/tx wall at 3 workers for the all-miss pass, now not run. Expected -25..-37 s (the hash moves to later consumers: receipts, txlookup), -2.6 us/tx. (3) TX DECODE 36.7 s (6.7%, 3.4 us/tx): 1.43 decodes per block tx per node (block resolution DecodeFramesEach 27.7 s for every tx, committee recovery RecoverSenders 9.9 s for the 3/7 recovered locally), 72% inside the reflection RLP decoder, 15 allocations per tx; keeping admission's decoded txs for block resolution would hold ~1.5M decoded txs (~0.9 GB) live, so the decode itself was made cheap instead: hand-rolled canonical decoding of legacy and empty-access-list EIP-1559 txs (body, integers and recipient in one allocation), everything else (non-canonical sizes/integers, overflow, access list, odd recipient, trailing bytes) falls back to the reflection decoder; TestFastDecodeMatchesReflection (84 encodings + every 1-byte mutation and truncation) and FuzzFastDecode (6.57M inputs, 90 s) require reflect.DeepEqual wherever the fast path accepts. BenchmarkDecodeLegacyTransfer (the fleet's txflood corpus is legacy): 898 -> 265 ns, 1,128 -> 552 B, 15 -> 4 allocs. Expected -22..-26 s, -2.1 us/tx. (4) RECOVERY UNIT COST (no change): BenchmarkEcrecoverParallel under taskset on idle cores 100-107 and their siblings 228-235 (sibling of cpu N is N+128): 1 thread 26.4 us; 8 threads on 8 cores 3.63 us/op = 29.0 us of thread time per recovery; 8 threads on 4 cores (2 per core) 5.60 us/op = 44.8 us; 16 threads on 8 cores 2.80 us/op = 44.8 us. So the fleet's ~50 us per recovery is SMT (44.8) plus all-core load, and SMT is a net gain per core: 8 cores do 275k recoveries/s with 1 thread each and 357k/s with 2 (+30%). Fewer, longer-running recovery workers per node would lower the thread-seconds booked per recovery but not the core time, and would give up the 30%, so the defaults stay. Consequence for the model: per-tx thread-us is not a constant -- it rises toward the SMT figure as the box fills, so 36 us measured at 192 of 256 threads would be ~40+ at 256; the 1M target needs margin below 36. (5) BLOCK-STM 9.1% / GC 5.2%: the allocation profile's top items were receipts 4.64 GB (already chunked; 425 B a receipt, the bloom), etl Put 4.06 GB (gone with (1)), decode 10.9 GB (about half gone with (3)), MV version-list growth 5.0 GB (writeOwned 2.78 + writeDelta 2.26: ~4,000 senders x ~40 versions per block grown 1 -> 64), SetSenderMemo 1.5 GB (block resolution and the block's sender job both boxed the same memo). Change: ApplySenders skips a tx whose memo is already under the same signer (re-apply allocates 0, tested). Not changed: version-list presizing needs the per-key writer count from the affinity partition (estimated -0.6 us/tx; next). Allocation cut ~10 GB of 46.9 (-21%) -> GC mark ~-6 s, ~-0.5 us/tx, plus ~0.2 us/tx of memo CPU. TOTAL ESTIMATE: -4.0 -2.6 -2.1 -0.7 = -9.4 us/tx, 50.3 -> ~41 us/tx per node (fleet ~156 of 256 threads at 542k, ~1M TPS needs ~36 at full load); the remaining ~5 us: MV presizing 0.6, history backfill fold 10.4 s (1.9%, 1 us), receipt/Tx.Type cache misses on first touch (fillReceipt 3.5 s), Keccak 17.7 s (3.2%: tx hash, frame hash, attestation digest). SAFETY: no consensus input changed -- decode is DeepEqual-gated, the hint skip changes only where a memo comes from (the sender is the same signature's), txindex is off-chain. TESTS: go test ./internal/ (109 s), internal/frame/..., internal/parallel, internal/txlookup, modules/state, lib/recsplit/..., common/transaction green; internal/ingest TestFramePullBypassesCap / TestFrameIngestBusyAtCap and internal/txindexer TestTailStaysBoundedUnderSteadyCommits are flaky at the base too (store-cap tests depend on random frame sizes, fail 2 of 3 at 68324f77 under -race; the tail test's unverified multi-segment lookup hits the 8-bit existence filter's 1/256 false positive: 7/30 runs at leaf 8, 3/30 at leaf 5); -race green on internal (384 s), internal/frame/..., internal/txlookup, internal/txindexer, internal/parallel, common/transaction, lib/recsplit (TestOpenIndexFromBytes fails checkptr under -race at the base, pre-existing). NEXT ROUND (S102 on n42-r138): no new env; profile ON legs at release + 30 s and read: txindex share (expect ~2.5%), applySenderHints absent, DecodeEthereumTransaction (expect ~2-2.5%), GC mark, CPU-s per 20 s / txs = us/tx.

S102 result (2026-10-12, 35zzzcb, n42-r138 = P9; run 02:40-04:01 ET, attestation ON every leg, boundaries-first measure-tps.sh, corpus 8 x 15M = 120M = 736 blocks of 163k per flood; barrier releases warm-up 02:51:31, B1 03:23:00, B2 03:40:35): warm-up win1/win2 627,550 / 627,550 (231 blocks each, 0.260 s); A1 90.0k / 90.0k; B1 646,567 / 646,567 (238 / 238, 0.252 s); B2 654,717 / 649,283 (241 / 239, 0.249 / 0.251 s); A2 90.0k / 90.0k; every B block 163,000 txs, occupancy 50.0%, 0 full blocks (3.42 Tgas fillgas). GATE: >= 500k met in both windows of every B leg and of warm-up; standing best 654,717 (B2 win1) at 163k blocks (S101: 546,050 in one window). WINDOWS ARE DISTINCT, THE EQUAL COUNTS ARE A STEADY CADENCE: measure-tps.sh now takes boundaries first and prints only counts, so the heads are not in the run log; node0's import log reproduces them (win1 = release + 15 s .. +75 s, win2 = +75 .. +135 s, two consecutive 60-s spans) at 229-241 blocks per span (B1 240/237 at +13 s, 239/238 at +15 s; B2 240/239; warm-up 229-230 / 232), flood blocks 13667503-13668238 in B1, and 231/231, 238/238 are 4 blocks/s +-1. Blocks per 10 s on node0 through each flood: 37-42 (B1: 28 in the release bucket, 41, 40, 38, 42, 39, 40, 40, 40, 39, 40, 39, 39, 41, 38, 39, 37, 39, 37), i.e. 240 blocks a minute for 188 s; the corpus ran out at 02:54:44 (warm-up), 03:26:08 (B1), 03:43:44 (B2), ~53 s after win2 closed (release + 135 s), so both windows are fully inside the flood (S101's win2 was empty). CYCLE IS INTERVAL-BOUND AT THE MEDIAN (163k imports, ms median / p90; node0 B1, B2; node6 B1, B2; n = 736 each): total 237.5/309.1, 222.0/325.2; 238.9/311.6, 224.0/320.2; aheadRun 161.7/202.5, 136.2/179.6; 167.1/210.0, 136.8/184.0; aheadWait 121.8/157.3, 103.0/140.1; 125.8/162.9, 104.6/144.6; exec 67.9/90.7, 65.6/82.8; 67.8/91.1, 67.4/87.7; recov 2.3/3.7, 2.3/3.9; 2.4/4.5, 2.2/4.1; collect 55.9/74.7, 45.3/65.1; 58.8/78.3, 46.3/63.7; aheadPre 8.6/13.6, 7.6/13.7; 9.0/15.9, 7.5/12.9; root 95.9/127.7, 82.2/120.7; 93.6/129.3, 84.2/118.5; setup 14.8/27.2, 7.7/19.6; write 4.8/8.0, 4.2/8.1; handed 736 of 736 on all four, handWait 0.5-0.7/1.0-1.1; ahead used on 597/458 (node0) and 617/467 (node6) of 736 imports. Recovery fell 48 -> 2.3 ms (the frame senders are ready from the attestation/ingest path before the block imports), exec 85 -> 68, collect 62 -> 56/45. Import total median 222-239 ms against the 250 ms interval = 11-28 ms of headroom, but the p90 total is 309-325 ms, above it. The leader waits for the grid: miner: pacing wait median 109.0 / 103.3 ms (p90 170.6 / 177.1, max 224.8 / 250.0; node0 n = 62 / 87 tenures), node6 80.6 / 130.7 (p90 180 / 206); lag build total (leader) 34.1 / 98.6 ms, gate phases 0.4-0.5 ms. View interval (view changed tMs deltas) median 250 / 249 ms on both nodes, p90 296 / 280, p99 477 / 483, max 675 / 1,164; 69 and 60 of ~745 views > 300 ms (9% / 8%); import-end gaps (node0, node6) median 0.240 / 0.242 s (B1), 0.227 / 0.233 s (B2), p90 0.348 / 0.341, 0.357 / 0.354, p99 0.47-0.52, max 0.65-0.69 (B1), 1.6 / 2.0 (B2); gaps > 0.3 s: 127 / 117 of 735 (17% / 16%) in B1, 218 / 192 (30% / 26%) in B2; > 0.4 s: 29 / 27, 26 / 20. Commit phases total median 17.9-36.2 ms, p90 134-140 (canon 14.6-29 median, p90 107-115; persist 0.5 / p90 9-33). So the 0.249-0.252 s block time is the interval plus jitter, and 654.7k = 163,000 / 0.249 is the interval's ceiling (652k), not the chain's. A tail of overrun views (p90 import 310-325 ms) is what still separates 647k from 652k. CPU PROFILES (20 s each, B1 win1 03:23:31 / win2 03:24:30, B2 win1 03:41:05 / win2 03:42:05; node0 and node6): CPU-s per 20 s B1 win1 454 / 459, win2 439 / 432; B2 win1 423 / 422, win2 422 / 424 = 21.1-22.9 busy threads per node, ~153 fleet threads of 256 (B1 ~157, B2 ~148). Txs in the span: 79 blocks = 12.88M (B1, both nodes), 79-80 blocks = 12.88-13.04M (B2): us per tx per node B1 35.3 / 35.6 / 34.1 / 33.6 (mean 34.7), B2 32.4 / 32.8 / 32.4 / 32.5 (mean 32.5); S101 was 50.3, P9's estimate was ~41: -35% (B2) against -19%. Shares (cum, of the node's CPU, B1 win1 .. B2 win2): secp256k1 recovery (frame.RecoverSenders -> _Cfunc_secp256k1_ext_ecdsa_recover) 56.4-59.8% (B1) / 58.1-58.3% (B2), ~42 us per recovery over the 3/7 committee share (233 s / (80 x 163k x 3/7) at B2 win1), the SMT-loaded unit cost, so recovery is now 19 us of the 32.5; txlookup.BuildSegmentFile 2.8-3.0% (B1) / 2.9-3.1% (B2) against the ~2.5% expected (from 10.4%); applySenderHints absent from the profiles (from 6.8%); tx decode (frame.DecodeTxs / DecodeEthereumTransaction) 3.8-4.3% against 2-2.5% expected (6.7% before; fastDecodeLegacy is 2.9% of it; sha3.keccakF1600 3.6-4.0% flat); Block-STM (executeParallel.func2) 12.0-12.8%, runParallelMode 1.5-1.9%; GC mark worker 3.4-4.4% (5.2% before); block write (writeBlockWithStateOpt) 1.2-1.3%; QMDB root compute (computeRootViaComputer) 0.6-0.7%; attest/BLS (blst, attest.Pool) 1.4-1.6%. CEILING: TPS ~= 256 / (7 x us/tx) = 256 / (7 x 32.5e-6) = 1.125M at B2's 32.5 us (1.054M at B1's 34.7); SMT stretches the per-tx figure as the box fills (P9 note: 36 measured at 192 of 256 threads would be ~40+ at 256), so ~0.9M is the safer ceiling at 40 us. At 650k the fleet used 153 of 256 threads, so CPU is not what stops the chain at 250 ms. MEMORY AND STALLS: memstats sampler (30 s), peak Sys / HeapInuse GiB node0, node1: warm-up 7.27 / 6.40 and 7.28 / 6.22; A1 5.99 / 5.19 and 5.87 / 5.28; B1 7.21 / 6.13 and 7.29 / 6.14; B2 7.28 / 5.41 and 7.23 / 6.43; A2 5.98 / 4.74 and 5.97 / 4.16, all at GOMEMLIMIT 7 GiB (Sys 7.2-7.3, no OOM). Major faults per 10 s in the flood spans (vmstat, 19 samples each): B1 median 538k, p90 584k, max 686k; B2 median 508k, p90 578k, max 617k (about 9M per span: the QMDB/MDBX mapped pages being read back; present in every B leg). View timeouts: 0 in all flood spans on all 7 nodes; the only TCs are at node startup of each leg (02:17:11, 02:59:11, 03:13:17, 03:30:55, 03:48:39). Write probes > 200 ms in the flood spans: 14-23 per node per leg (node0 20 in B1, max 420 ms; 19 in B2, max 1,310 ms; B2 maxima 1.0-1.6 s across nodes), none visible in the block cycle. Fetch-on-miss 0-3 per node per leg (one request at each release), orphan lines 0, 'committed block not executed locally' 48-74 per node per leg, 'Could not retrieve blocks' 239-333 and catch-up range lines 276-438 per node per leg (baseline noise as in S101). Generators (8 x 3 legs, all .out files): failed=0, timeouts=0 everywhere, DONE frames=30000 txs=15000000 in 2m54-2m58 (B1; 84.4-86.1k tx/s accepted each), 2m57-2m58 (B2; 84.0-84.7k), 2m58-3m03 (warm-up; 81.9-84.3k), busy replies 88,961 (B1), 93,750 (B2), 89,140 (warm-up) in total, stderr empty. SAFETY: 0 BAD BLOCK (no 'db closed' / 'context canceled' either), 0 panic on all 7 nodes over the whole round (zcat of the rotated log plus the live log), 'Opening database' 5 per node = one per leg, no unplanned restart; B1 747 and B2 742 committed views each present on all 7 nodes with the same block hash (0 differing; deferred execution carries the parent's roots in the header, so equal hashes are equal state roots); frame attestations: badSig 0, notMember 0, badDigest 0, badBatch 0 on every line of every node (129-130 stats lines per node; fallback 13-25 per node). VERDICT: (1) the 500k gate is met in both windows of every B leg (647-655k, 163k blocks, 0.249-0.252 s); (2) the 250 ms interval is the pacer (view median 250 ms, leader waits ~105 ms in its tenure) with 13-28 ms of median import headroom and a p90 import of 309-325 ms that already overruns, so the next bound is the import ahead run, not the interval; (3) 32.5 us per tx per node (B2; 34.7 B1; S101 50.3) = 422 CPU-s per 20 s, ~153 of 256 threads, ceiling ~1.12M TPS on the 256 threads (~0.9M with the SMT stretch to ~40 us); (4) next = S103 (35zzzcc): block interval 125 ms in warm-up/B1/B2, so the import (total median ~222-238 ms today, ahead run 136-162) sets the cycle; expected 0.22-0.25 s blocks and 650-740k, with the ahead run (exec 66 + collect 45-56 + pre 8 + setup 8-15 + root 82-96) as the target of P10; recovery (58% of the CPU) is where a lower per-tx figure would still come from.

P10 result (2026-10-12, commits d8231db2 bench, 8083efac item 1, 87379a1f item 2, 9f3d9522 item 3, 5861219a item 4, da6dc1a5 item 5, bc452b34 fix; binary n42-r139, built at HEAD 869986c1, sha256 c0136bcc7eb8ffa677ab4661148643cc6bbc9bd5b6b71a8bbbb02fa7dfacd19c). Goal: the fleet's per-block ahead run at 163k from ~233 ms (S101; S102 162/136) to <= 150 ms. Bench: TestQMDBImportRootWriteBench (163.2k transfers, 4 nice'd cores, 12 blocks, medians of blocks 3..12) now reports the ahead run by phase (a.pre, a.setup, a.collect and its parts, a.gap, a.run, a.wait) and N42_BENCH_PRAGUE=1 runs the fleet's fork set (two Prague system calls at every block end, history call at block start); every A/B gave the same 12 state roots. FINAL A/B (base d8231db2 vs 5861219a, Prague, the two clean interleaved pairs at load 18-20; pairs 3-5 ran under the S102/S103 fleet at load 96-159 and moved the same way): recov 5.4 -> 0.0, aheadPre 5.6 -> 0.1, collect 39.3 -> 20.2, exec 174 -> 143, aheadRun 252 -> 203, root 65 -> 54 (outside ComputeRoot 42 -> 26), wall per block 400 -> 348 (-13%), and the ordered writer's write 78 -> 104 (see 5). (1) RECOV: at block time the import still walked every frame (store claim, ApplySenders memo check, a Sender pass per tx) and ran the declared-sender gate over 163k txs, even when block resolution had attached every sender. Now resolution records a block whose senders all came from the store under one signer (and whether any tx declares a From), a block's sender job records completion, and the import skips recovery and, with no declared From, the gate; a job is not started for a completed block. The lag build takes only frames whose senders are cached, holding back a gateway's later frames behind a frame that is not ready (arrival order per gateway), with N42_FRAME_READY_GRACE_MS=1000 after which a frame is taken anyway (N42_FRAME_PICK_READY=0 restores the old pick; the lag build line logs heldGateways). S101's 48 ms was mostly the hint pass P9 removed (S102 2.3 ms); bench 5.4 -> 0.0. (2) COLLECT was receipts assembly 8.2 (serial loop, cache misses), the MVS fold 25 (serial walk of the final versions ~11 + IBS ops), the pending-credit prefetch 4.4 and the fee totals 3.6. Now receipts/logs/cumulative gas/fee totals in two parallel passes over chunks when nothing was dropped (2.6 ms, TestCollectParallelResultsMatchesSerial), final versions read over disjoint shard ranges in parallel, the fold and the block end read accounts from the run's own base-read cache (AccountPrefetch.SetKnown: same parent state; the prefetch is skipped, detached credits take their base there), IBS maps/journal/credit records reserved once: 40 -> 18 ms; the serial IBS fold (~15 ms) remains. (3) AHEADPRE was the fee-recipient scan touchesAny over 163k txs (6.6 ms on the fleet) run twice per ahead run; now once and split across goroutines; the result slots are cleared once, off the run: pre 5.3 -> 0.1, setup -1.3, unphased remainder 6.6 -> 3.1. (4) EXEC: tx hashes are computed in the background when the block is queued (the wave's preparation hashed 163k txs); a sender's version list is presized from per-worker affinity-key counts (exec microbench 393k -> 354k allocations a block; N42_MV_PRESIZE=0 disables); the affinity keys/messages are prepared before the block-start system calls (Executor.PrepareAffinity). FINDING: an ahead run starts at its parent's snapshot, just before the parent's ComputeRoot, which holds the QMDB tree exclusively; the run's first tree read (with Prague the EIP-2935 call at block start) waits for it: ~20 ms of the bench's setup and the fleet's setup 17 ms (S102) are that wait, not CPU. Prepared before it, the preparation overlaps the wait: 4-way A/B ahead run 224 -> 206, exec 176 -> 147, wall 359 -> 341. Moving the ops build/sort out of the tree lock did not measure (dropped). (5) ROOT outside ComputeRoot on the fleet: the two Prague system calls finalize with a no-op writer, and that FinalizeTx loaded every pending credit first: ~23k accounts materialised serially (~30 ms; 2.0 of 549 CPU-s in S101's profile), so P4b's detached credits never applied on the fleet (no creditsDetached on its phases line). A no-op FinalizeTx on a detaching state now leaves them pending and dirty in the journal (TestDetachedCreditsSurviveSystemCallFinalize: root dirty set, post-state and CommitBlock rows equal the materialising path, EIP-161 zero credit to an absent account included): bench root 67 -> 49, outside ComputeRoot 43 -> 25 (16 without Prague; the rest is the system calls' two finalizations over ~31k journal dirties and the dirty-set build). N42_FINALIZE_TRACE=1 costs another ~4 ms (DirtySetSizes, 0.25 s/20 s in S101). COST: the credits now load in the ordered writer's CommitBlock instead: bench w.state 14 -> 38 ms, write 78 -> ~100 (off the critical path); a parallel CommitBlock materialisation cut w.state 38 -> 27 but slowed the 4-core wall (dropped). (6) FIX bc452b34: a discarded ahead run that had not started waited for itself (or two for each other) in awaitDrained; deterministic under -race in two beforeSnapshot tests after item 4; runs now wait only for older discarded runs. EXPECTED ON THE FLEET (S102 node0 medians as base: pre 9.6, recov 2.3, setup 17, exec 68, collect 56, aheadRun 168, root 92): pre ~3, recov ~0, collect ~25, exec ~58, setup ~12, aheadRun ~110-135 ms; root ~60 (ComputeRoot ops+apply ~33 unchanged), ~56 with N42_FINALIZE_TRACE unset (root <= 40 is not reached). Cycle from W(N+1) = max(W(N), W(N-1)+aheadRun) + root-to-publish: aheadRun + ~25 = ~135-160 ms, i.e. 1.0-1.2M TPS at 163k; the one-write-in-flight rule now competes: the writer's own write was 120-145 ms (P7) and gains ~25-35 ms of credit loading, so handWait may become the floor at ~150-175 ms. TESTS: internal, internal/frame/..., internal/parallel, internal/miner, modules/state/... green; internal/ingest TestFrameIngestBusyAtCap / TestFramePullBypassesCap flaky at f214e5cc too (4 of 6 runs); -race green on internal (Lag|Sender|Frame|Parallel|Collect|Prague|Transfer|Prefetch|Pipeline|Availability, 273 s), internal/parallel, internal/frame/..., internal/miner, modules/state (Detach|Prefetch|Root|Credit|Finalize|Reserve|Commit). NEXT ROUND (n42-r139): unset N42_FINALIZE_TRACE; defaults otherwise. Read on the phases line: creditsDetached ~22.8k (proves 5), aheadPre <= 3, recov ~0, setup, collect, exec, aheadRun, root, handWait and the writer's write (the floor candidate); the lag build's heldGateways (should be ~0 in steady state).

P11 result (2026-10-12, commits a50e958c bench, 56ad8049 item 1, 150d62f0 frame store, a1b3723a Log rows, af4b2ba9 changeset cursor, 53288059 lock hold; binary n42-r140, built at HEAD 53288059, sha256 5952bde85b65cca16245ecb073810d1d178b4629645ca31d1dca2231ad02d66d). Goal: the ordered writer's per-block write for a 163k-transfer block <= 100 ms on the fleet, so that the one-write-in-flight rule is not the cycle floor. BEFORE: fleet S102 (r138, all 7 nodes, 163k blocks, mean / median ms): total 134.3 / 128.5 (p90 169) = begin 8.3 / 0 (p90 26.6: the history fold's <= 30 ms transactions holding the writer lock), receipts 14.5 / 10.1, block 38.2 / 37.5 (frames; 26.7 -> 39.0 -> 41.1 median from warm-up to B2 as the Frames table grew), state 15.2 / 14.4, changesets 18.0 / 16.8, QMDB flush 9.6 / 7.5, qmeta 2.8 / 1.4, commit 26.9 / 19.8; write probe 26.8 MB dirty, 22.8 MB payload of which Frames 17.3 MB (326 rows), Receipt 1.45 MB, undo 1.67 MB, 22k AccountChangeSet rows, 22k qmdbEntries puts + 22k deletes; fleet CPU profile (B2 win1 node0): the write 64 ms CPU per block of its 129 ms wall, WriteFrame 9 ms CPU of the 37 ms block phase. P10 moved ~23k pending-credit loads into CommitBlock (bench state 14 -> 38), so the fleet's write was expected at 150-175 ms. Bench (TestQMDBImportRootWriteBench, Prague, 4 nice'd cores, medians of blocks 3..12) at the base: paced 97-104 ms (state 35-43, changesets 21-23, commit 14-21, receipts 4-5, block 4-6, QMDB flush 3.4). BENCH MODE (a50e958c): the paced bench never loads back-pressure (hand-off wait 0.1 ms); N42_BENCH_WRITE_BACK_TO_BACK=1 holds each write until something joins it (lagWriteJoinForTest), so the next write follows at once: the regime where the writer is the floor. Base there: write 77-82 ms (state 28-30, changesets 20, commit 12-13, QMDB flush 10, receipts 4, block 4-6), hand-off wait 75-80 = the write: two consecutive 163k blocks pipeline at the write's wall. ITEMS (each an interleaved A/B, 3 pairs unless noted, same 12 state roots in every run): (1) 56ad8049: CommitBlock and the changeset sort/encode run before the transaction, on a goroutine the hand-off starts before it waits for the previous write. CommitBlock talks to its writer only through StateWriter calls, so it runs against a StateWriteLog; with the history index deferred or off the log becomes the plain rows and the encoded, sorted changeset rows (a PlainStateWriter over a row buffer) and the transaction only puts them. CreateContract reads the store: it is admitted on the prepared rows when the address has no Storage row earlier in the block (checked when preparing) or in the store (checked in the transaction before any row is applied), else the log is replayed through the real writer (old calls, old order). Every Prague block needs this: the system calls' sender is touched, empty and removed (EIP-161). N42_LAG_WRITE_PREPARE=0 restores the old path. Back-to-back: write 77.0/78.2/82.2 -> 41.7/41.3/42.6 ms (state 28-30 -> 0, changesets 20.0 -> 13.0, prep 43-50 off the transaction, fully hidden); paced the write is unchanged (it waits ~45 ms for a prep started at the hand-off). (3+4) 150d62f0, frame store: with N42_FRAME_DB=1 the node opens <datadir>/frames (MDBX, the chain database's map size and durability) and frames go there in their own durable transaction, started at the hand-off and joined before the block's commit (imported = durable: a durable block always has its frames); WriteBlock paths store them there inside their transaction; readers look in the frame store first, then in the Frames table (old frames stay readable). The frames were not durable anywhere else (the ingest frame store is in memory), so a reference-only write was not possible. Back-to-back: write 42.1/40.3/45.6 -> 29.8/29.2/29.7 ms; block 5.2-5.6 -> 0.4-0.5, commit 11.8-12.5 -> 2.8-3.0 (the dirty set without 17 MB of frames), frame store write 12-13 off the transaction, never waited for; paced write 86.6 -> 64.5. (3b) a1b3723a: the Log rows and the log-index input are prepared off the write (AppendReceiptsEncoded and WriteLogIndex each walked 163k receipts in the transaction; fleet profile ~9 ms of the receipts phase): back-to-back write 29.3/28.6/30.6 -> 25.9/26.0/26.5, receipts 3.9-4.6 -> 0.1-0.2. (2) af4b2ba9: the prepared changeset rows go through one DupSort cursor per table: changesets 12.0 -> 11.4 in every pair, write -1 ms. The remaining 11.4 ms is 31k MDBX APPENDDUP puts: AccountChangeSet values are variable-length, so MDBX_MULTIPLE (DUPFIXED only) does not apply, and a per-block blob would change the format every consumer reads (unwind, history fold, DATC, historical RPC) -- not attempted. (5) 53288059: with everything before the state phase now ~1 ms, the write waits for its preparations before opening the transaction, so the MDBX writer lock (the history fold and the commit path's canonical writes take it too) is not held across the wait: paced, the transaction held 55-66 ms -> 19.1-19.8; back-to-back unchanged (24.9/25.0 -> 25.2/25.3). NOT KEPT: QMDB flush replay through cursors (appended entry runs, key-ordered deletes with in-place delete of the next key): equal rows, no gain (entries 3.2 -> 3.9, the reclaim blocks 10.0 -> 10.0; the 30.8k deletes cost MDBX page work, not the search); dropped. The bench's QMDB flush alternates 3.3 / 10 ms by block (every other flush reclaims the previous block's 30.8k dead entry rows); the fleet does both every block (7.5 median). FINAL (base a50e958c vs 53288059, N42_BENCH_FRAME_DB=1, 3 interleaved pairs, load 3.2-4.3, same 12 state roots in all 12 runs): back-to-back write 79.2/78.6/80.4 -> 25.2/25.1/24.8 ms = transaction 24.6-24.9 (changesets 11.4, QMDB flush 9.5-9.7, commit 2.8, qmeta ~1, block 0.4, receipts 0.1, state 0.0), hand-off wait 77.6-80.0 -> 24.6-25.0 (two consecutive 163k blocks pipeline at the write's wall, now 25 ms), prep 42-49 and frame store 12-16 on their own goroutines; paced write 103.5/97.6/96.6 -> 75.3/63.0/55.1 total, of which prep wait 36-44 and the transaction 19.4-26.3; bench wall per 12 blocks 3.96-4.08 -> 4.06-4.10 s (exec-bound on 4 cores; the prep's ~45 ms of CPU competes with the ahead run there, a.run +5-10). FLEET PROJECTION (S102 means, bench deltas applied): begin 8.3, receipts ~1, block ~1, state ~0, changesets ~10, QMDB flush 9.6, qmeta 2.8, commit ~6-10 (bench 12.8 -> 2.8 with the frames out; the fleet's fsync is heavier) = ~40-45 ms of writer time per block, against ~160 expected after P10; by the S102 fleet/bench ratio (1.3-1.6 x) 33-40 ms. The two parallel tracks a block's write joins: prep (bench 42-49 on 4 cores; fleet est. 55-75) and the frame store write (bench 12-16; fleet est. 35-50, as it inherits the random-insert cost and its own 17 MB fsync), both started at hand-off N and needed only by write N; they overlap the import's own 110-135 ms cycle, so neither is a floor. Cycle W(N+1) = max(W(N), W(N-1) + aheadRun) + root-to-publish: the one-write-in-flight floor is now ~40-45 ms << aheadRun, so the write leaves the cycle and P11 leaves it at the ahead run (+~25): 135-160 ms, 1.0-1.2M TPS at 163k. READING THE FLEET LINES: when the import is the floor the hand-off finds the previous write done, so the next write waits for its own prep: blockwrite total = prepWait + the transaction (fleet est. 80-120 ms) and is NOT the floor; the floor quantities are the import line's handWait (should be ~0) and the transaction (total - prepWait). New fields on 'blockwrite phases': prep, prepWait, frames, framesWait (should be 0); the write probe should show ~10 MB dirty and no Frames rows in chaindata. Tests: TestLagWriterPreparedWriteSameRows (every chaindata table, block by block, against CommitBlock in the write; history deferred (prepared rows; a SELFDESTRUCT wiping a stored slot replays) and inline (replay); transfers, fresh recipients, an EIP-161 touch, a creation, slot rewrites with LOG1; plus a pipelined import), TestLagWriterFrameStoreSameRows (frame blocks through the availability check: chaindata equal except Frames, which stays empty; the frame store equals the reference's Frames rows; frames, hash lists and frame-body transactions read back), TestFrameStoreSyncWriteAndFallback; TestLagWriterRestartBeforeLastWrite, TestLagWriterFailedWriteRequeues and TestLagPipelineAheadOrderedWriter pass. Green: internal, modules/state/..., modules/rawdb/..., modules/changeset, lib/qmdb/..., lib/kv/mdbx; -race green on internal (Lag|Prepared|Frame|Pipeline|Restart, 320 s), modules/rawdb, modules/changeset, lib/kv/mdbx, modules/state (Commit|ChangeSet|Selfdestruct|Prepared|Detach|Credit|Finalize|History). NEXT ROUND (n42-r140): N42_FRAME_DB=1 on every node (new; without it the frames stay in the chaindata write, ~38 ms on the fleet); N42_LAG_WRITE_PREPARE defaults on; otherwise P10's env (N42_FINALIZE_TRACE unset). Optional, not measured: N42_HISTORY_FOLD_TXN_MS=15 for the begin tail (p90 26.6). Frames now grow <datadir>/frames by ~17 MB a 163k block (S102: ~37 GB per node per round); the chaindata no longer does.

## 6. What this does not claim

- 4M is not a measured number anywhere, including n42-rs. The honest
  comparison bar is 3.2M E=1 / 650k independent.
- The E=1 number is a benchmark of the execution layer under a consensus
  wrapper; it is not seven nodes verifying. The 1M independent number is
  the one to publish as chain throughput.
- Loopback hides 156 MB/block of body egress; frames-by-reference fix that
  only if frames are disseminated once and referenced thereafter.
