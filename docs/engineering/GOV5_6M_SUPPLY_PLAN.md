# Gov5 supply above 5M transactions/s

Date: 2026-10-15. User correction: the load supply must exceed the chain's
5M/s target. Astra reviewed the Rust implementation; Sol implements in an
isolated Go worktree. Do not edit Rust, the original seven-node chain, or ETH
EL+CL. The completed 8-transaction and same-data restart evidence remains valid.

## Corrected acceptance target

The previous 32,768-transaction / 20-second diagnostic has a mathematical
ceiling of 1,638.4 TPS. It cannot qualify a generator for this target. Its
waiting task was stopped before provisioning or running; artifacts are retained.

Qualify sustained **6M unique transaction wires/s**, giving 20% headroom over
5M. A 20-second measured interval needs at least 120M distinct transactions,
in addition to warm-up and remaining supply. A rate argument alone proves
nothing. Record offered, written, receiver-confirmed, verified, admitted and
canonical-executed counts separately; never sum duplicated seven-node traffic.

## What n42-rs actually implements

Read-only reference root: `/home/n42/src/n42/n42-rs`.

| Reference | Mechanism to reuse |
| --- | --- |
| `crates/n42/h2-node/examples/tx_flood.rs`, `Args`, `pregen`, `pregen_worker` | Offline signing into worker-specific files, outside the measured interval; nonce streams are partitioned by sender. |
| Same file, `open_replay_set`, `PregenHeader::mismatches` | Bind algorithm, chain ID, sender offset/count, nonce capacity, worker partition, gas/fee, recipients, frame size and gateway parameters before replay. |
| Same file, `Ingest`, `replay_over_ingest` | Persistent binary connections, multiple frames in flight, at most one outstanding frame per sender, ordered replies and bounded windows. |
| Same file, `frame_suffix` | A partial ACK advances only its accepted prefix. Remaining transaction signatures are not rewritten; a changed gateway frame attestation is signed again if needed. |
| `scripts/fleet7-bench.sh` | `F7_FLOOD_REPLAY`, `F7_FLOOD_PROCS`, `F7_FLOOD_WINDOW`, `F7_FLOOD_RATE`; separate generator CPU allocation from chain processes. |
| `docs/BREAKTHROUGH_DESIGN.md` sections 10.71 and 10.72 | Live signing became a supply ceiling; the workload moved to pre-generated signed transactions. Configured offer and actual delivery differ under queue backpressure. |

The earlier documented replay set contains **192M transactions**, not 192M
frames. X11c's actual loop351 runner uses **400M pre-signed transactions**,
12,500 senders and 32,000 transactions per sender; see
`scripts/fleet7-runs/run-loop351.sh`. Do not confuse the earlier set with the
record run's corpus.
A useful initial pipeline is 64 workers x 32 outstanding frames x 500 tx/frame
= 1.024M outstanding transactions. At 6M/s, that covers about 171ms of response
latency, subject to sender/window limits and actual resource measurements. This
is sizing arithmetic, not a measured Go or Rust 6M qualification.

Rust's X11c record is 4.24M / 4.06M / 4.18M executed TPS (section 10.121).
Those chain figures must not be presented as a Go result or proof that an
arbitrary configured flood rate was actually delivered.

## Trust and recovery differences

`crates/n42/tx-ingest/src/lib.rs` documents an explicit gateway quorum path:
enough valid configured gateway attestations admit a frame without individual
transaction-signature verification at that admission stage. This is a trust
boundary, not merely a faster serializer. Claimed senders alone do not grant
trust; deferred verification is also different from omitting verification.
Do not enable either policy implicitly in an ordinary Go/ETH configuration.

Rust's timeout/reconnect replay relies on its node's sender/nonce duplicate
handling. Gov5's current pending-hash set is not equivalent: an old committed
nonce must not be blindly reinserted. The new transport must bind session,
corpus, shard, sequence/frame ID and payload hash. Retransmission can count only
newly accepted items. Until durable session watermarks and restart reconciliation
exist, unknown ACK/disconnection stops the session; it does not authorize replay.

Rust signed data for another chain cannot be copied into chain 4205001 or have
its nonce rewritten. Generate new signed data with the complete selected Go
identity. Reusing a corpus in a later chain run requires the correct starting
nonces, not a repeat of already committed transactions.

## Implementation slices

1. **Supply qualification.** Add a versioned, identity-bound corpus format and
   streaming binary replay tool with a bounded receiver. Pre-generate at least
   the measured 120M plus warm-up and headroom. Candidate sizes are 160M for a
   4-second warm-up plus 20-second interval and margin, or the Rust-shaped
   earlier Rust-shaped 6,000 senders x 32,000 nonces = 192M. Freeze parameters
   before the run. These sizes qualify one 20-second interval, not a long
   capacity round: a 90-second interval at 6M/s alone needs 540M unique
   transactions plus warm-up and margin, or independently validated rolling
   segments with non-overlapping nonce ranges.
   Store per-shard hashes, non-overlapping sender/nonce ranges and complete
   genesis/algorithm/rule provenance. The existing Go `TXFC0001` header is not
   sufficient as identity or integrity evidence; its reader must not allocate
   from an unbounded supplied frame length.
2. **Binary transfer acceptance.** Require receiver-confirmed unique counts
   of at least 120M over an actual 20-second interval at >=6M/s, with monotonic
   time, bounded bytes/transaction credits, no corpus recycling, and observable
   ACK timing. Use prevalidated immutable corpus data. Label this result as
   supply/transport qualification: a fast sink is not a blockchain and does not
   prove transaction admission, signature authorization, execution or finality.
3. **Real E1 ingress.** Feed one shared executor-owned queue used by all seven
   leaders, instead of multiplying unique supply into seven RPC submissions.
   Add an explicit `BuildFromQueue` path: prepared header -> parent/view-bound
   queue lease -> execution -> complete BlockRLP -> existing validator seal,
   WAL, import and 5/7 CommitQC. Initial queue sizing may be 2M transactions /
   512MiB encoded bytes, both enforced; process RSS and object overhead are
   additional. Lease consumption follows canonical commit; failed candidates
   release leases. Do not introduce FrameBody commitments in this slice.
4. **Capacity measurement.** Qualify actual ingress separately, then measure
   canonical-executed unique transactions with supplied data remaining. A
   slower chain may reduce accepted feed through backpressure: a full bounded
   queue differs from generator starvation. Record that distinction, execution
   lag, queue occupancy, byte rates, CPU affinity and persistence progress.

Keep default interfaces unchanged and feature gates explicit. The current
metadata constructor does not start the ordinary txspool/frame ingest, and
shared execution currently rejects frame bodies. Existing Go frame code is a
reuse opportunity, not evidence that the E1 runtime already supports it.

## Validation and hardware

Before a large corpus: test format/identity/hash rejection, truncated and
oversized lengths, sender/nonce uniqueness, partial ACKs, duplicate frame IDs,
unknown ACKs, credit accounting, cancellation and bounded allocations. Validate
ordinary Ethereum/default paths remain unchanged. Start with small fixtures,
then qualify the real corpus under the existing BOX protocol.

Use separate bounded hardware turns for generation, replay qualification and
chain measurement; release ownership between turns. Never compile or generate
the large corpus alongside the other session's measurements. Preserve failed
data/logs and report measured limits. **No 6M supply result exists yet.**
