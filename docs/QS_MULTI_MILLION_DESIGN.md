# From 150k to multi-million TPS: why the current design stops, and what replaces it

Date: 2026-10-09 (America/New_York). Status: P1a and P1b implemented (sections 5.1, 5.2); P1 gate round pending.
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
does not fit or `N42_FRAME_BLOCK_MAX_FRAMES` (default 1024). Bundles and
the pool are not read. Execution is `BuildParallelFrames` (Block-STM, same
code as the import) with a serial fallback that drops a frame on a failure
outside the skip rule; assembly stamps FrameTxRoot without a
per-transaction root.

Follower. `InsertChain` (before bc.lock) and the deferred check resolve
the hashes: frame store, then `Frames`, then FRQ1 to
`N42_FRAME_PULL_PEERS` (timeout `N42_FRAME_PULL_TIMEOUT_MS`, default 2000).
Unresolved -> not imported -> no vote (logged once per block). With
two-phase voting the header-known round-1 vote is still cast before
resolution; round 2 waits for import/check as before. Senders are
recovered by the existing batched step of the import (sender_recovery.go)
on the decoded transactions.

Env summary: `N42_FRAME_GATEWAYS`, `N42_FRAME_STORE_BYTES`,
`N42_FRAME_BLOCKS=1` (leader production), `N42_FRAME_BLOCK_MAX_FRAMES`,
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

## 6. What this does not claim

- 4M is not a measured number anywhere, including n42-rs. The honest
  comparison bar is 3.2M E=1 / 650k independent.
- The E=1 number is a benchmark of the execution layer under a consensus
  wrapper; it is not seven nodes verifying. The 1M independent number is
  the one to publish as chain throughput.
- Loopback hides 156 MB/block of body egress; frames-by-reference fix that
  only if frames are disseminated once and referenced thereafter.
