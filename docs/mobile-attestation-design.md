# Mobile Attestation — Unified Design

Status: **design approved, implementation in progress** (see roadmap §9).

Mobile devices strengthen the network by independently re-verifying
committed blocks and returning BLS-signed attestations that the IDC
layer aggregates into compact, publicly checkable certificates. Mobiles
are **not** in the consensus path: they never vote, never affect
quorum/finality, and can never delay a block.

This document unifies the two mobile execution stacks that already
exist — the production mining SDK's V2 verification path
(`cmd/evmsdk`, "mint v2") and the standalone-app path (the APOS
`MinedBlock`/`AggSign` flow in `internal/api`) — onto **one data
plane**, and specifies the missing server side: registry, distribution,
collection, aggregation.

---

## 1. Roles and hard boundaries

| Role | Participates in consensus | Data it produces |
|---|---|---|
| IDC validator | yes — HotStuff-2 votes, QC aggregation, block production | blocks, QCs, verification data (StreamPacket) |
| Mobile verifier | **no** | `VerificationReceipt` (BLS attestation over its own re-execution) |

Hard boundaries (design invariants, not tunables):

1. Mobile attestations never enter `hotstuff.Service` /
   `ConsensusEngine.ProcessEvent`. `MobileAttestationCert` (§6) is
   shaped like a QC but is a **bypass evidence object**, not a QC.
2. Mobile registration requires no stake. Sybil resistance is
   lightweight (§8.2); economic staking would turn the mobile fleet
   into a de-facto second consensus layer.
3. A mobile must be able to verify everything it receives against a
   committed header. Distribution intermediaries (CDN edges, torrent
   peers, other IDC nodes) are untrusted (§8.3).

Uses of the resulting data: divergence alarms (any valid receipt whose
`ComputedReceiptsRoot` disagrees with the committed header is a
first-class incident signal), public transparency dashboards, and a
future reputation/reward layer. Never BFT safety.

## 2. Existing assets and the unification decision

Two mobile verification stacks exist today:

**(a) mint v2 — `cmd/evmsdk` (production mining SDK, used by the
Flutter app through `com.mobileSdk.Api`).** Downlink: `StreamPacket`
(V2 wire format, `docs/mobile/protocol-spec.md`) — header + raw txs +
ordered read log + deduplicated bytecodes; the phone re-executes the
block against the read log and recomputes the receipts root. Uplink:
`VerificationReceipt` (`cmd/evmsdk/verification_receipt.go`) — BLS
signature over the canonical 72-byte message
`block_hash(32) ‖ block_number(8 LE) ‖ computed_receipts_root(32)`,
deliberately timestamp-free so every phone attesting to the same block
signs **identical bytes**, enabling `FastAggregateVerify` (2 pairings
regardless of signer count). Byte-compatible with the Rust reference
(`n42-mobile/src/receipt.rs`). Missing: the server side — the producer
service is not wired, and no collector/aggregator exists.

**(b) Standalone app — APOS `MinedBlock`/`AggSign`
(`internal/api/api.go`, `agg_sign.go`).** WebSocket subscription pushes
the node's own sealed block + state snapshot to deposit-gated
verifiers; they return `AggSign{Number, StateRoot, Sign, Address}`
which feeds **directly into APOS seal quorum** — i.e. this path is
consensus-coupled, deposit-gated, and only covers the node's own
blocks. It predates HotStuff and does not generalize.

**Unification: one data plane, (a)'s formats, (b) retired to legacy.**

- Downlink format: `StreamPacket` (already spec'd byte-exactly,
  cross-language, production-proven decoder on phones).
- Uplink format: `VerificationReceipt` (already aggregation-ready by
  construction; already implemented in Go and Rust).
- The APOS `AggSign` path remains untouched for the legacy APOS chains
  that use it, but the mobile-attestation system does not build on it:
  it violates boundary #1 (consensus-coupled) and #2 (deposit-gated).
- Both existing clients (SDK embedding, standalone app) converge on the
  same facade: decode StreamPacket → re-execute → sign
  VerificationReceipt → submit. One codebase, one wire spec, two
  packagings.

What §2 buys us: the two hardest client-side pieces (deterministic
re-execution and the attestation format) are **already built and
production-hardened**. Everything new in this design is server-side.

## 3. Component 1 — Mobile registry (accumulator-anchored)

### 3.0 Three identity tiers (why this is not the staking registry)

**Scope: this whole system is a feature of the N42 native chain (HotStuff
consensus), NOT eth-el.** The eth-el `mobile_minimal` stateless client is
a separate, complementary capability (see §11 non-goals); nothing here
runs on the eth-el execution paths. The consensus-path anchor (§3.5) is
wired only into the n42 native miner and importer and is dormant on
eth-el by fork gate.

The chain has three distinct identity spaces; do not conflate them:

- **T1 — staked consensus validators.** ETH-style stake (deposit
  contract), HotStuff validator set, committee sampling for voting.
  Economic security; inside consensus.
- **T2 — the CommitteePool bridge** (`params/config.go`
  `HotStuffCommitteePoolConfig`): the 200K-voter / 512-committee pool
  carried over from the replay reseal, committed on-chain via each
  block's ConsensusEvidence + ParentBeaconRoot link, with an
  `AllowHandover` path where real staked keys replace simulated pool
  slots. A transitional consensus-evidence layer — still inside
  consensus.
- **T3 — mobile verifiers** (this document). Zero-stake, outside
  consensus, produce bypass attestations.

The mobile registry is T3. It **reuses the on-chain *anchoring pattern*
of T2** (a seed/root committed on-chain, advanced over time) but **not
its trust model** (no stake, no consensus weight). The old
`cmd/n42-blspool` 200K simulated pool is a T2 artifact for backfilling
legacy chain evidence; it is **not** the seed of the T3 registry, which
is built fresh from real device registrations.

### 3.1 The registry is an accumulator, not a map

Registrations settle into an **append-only committed set anchored by a
BMT root** (`lib/bmt` — the Blake3 binary Merkle tree validated across
11.7M replay blocks). Anchoring on the accumulator is what makes the
signer index **globally canonical** — a device's `MobileIndex` is its
append position in the committed set, identical on every node — instead
of a per-node guess. (An earlier sketch tried per-node integer indices
propagated by gossip with first-writer-wins conflict resolution; it
could not guarantee two nodes agree on a device's index, so a cert built
on one node could fail to verify on another. The accumulator removes
that failure class.)

Two layers, mempool-and-settled shaped:

- **pending** — PoP-verified registrations gossiped between IDC nodes
  but not yet committed; they cannot attest yet.
- **committed** — the append-only ordered set under the BMT root. A
  device gets a stable index and may attest. `CommitEpoch` folds the
  pending set in, **sorted by BMT key hash** so every node that commits
  the same batch derives the identical index assignment and the
  identical root. Append-only: an index never moves across epochs.

### 3.2 Registration and PoP

- **First-launch auto-registration.** The device generates a BLS
  keypair locally; on install/first run the client submits
  `(pubkey 48B, PoP)` to any IDC node. The registration lands in
  *pending* and becomes effective (attestable) at the next epoch commit
  — the endpoint reports `committed: false` until then.
- **Proof of possession is mandatory.** Without a PoP per registered
  key, an attacker registers a rogue key (the inverse of other keys'
  product) and forges aggregate signatures. This is a correctness
  requirement of BLS aggregation, not hardening. PoP = BLS signature
  over the registrant's own pubkey under a dedicated domain tag
  (`n42/mobileverify/pop/v1`), structurally disjoint from the 72-byte
  receipt message so a PoP can never be replayed as an attestation or
  vice versa.
- **Zero-cost, no stake** (boundary #2). Lightweight Sybil resistance
  per §8.2.

### 3.3 On-chain anchoring and the consistency model (honest)

The committed accumulator **root** is the value an epoch batch anchors
on-chain (roadmap phase 6b). The batch transaction lists the batch's
members, so a node that missed a gossiped registration still adopts the
exact committed set from the chain — making the root canonical across
the fleet.

Consistency model, stated plainly:

- Within one node the accumulator is fully deterministic.
- Cross-node **canonical** agreement on the root requires the epoch
  batch to be settled on-chain. Until a batch is anchored, roots are
  per-node advisory and converge as gossip does (a node that has the
  same committed membership computes the same root and verifies the same
  certs). The on-chain batch write is the anchoring seam — built up to
  `Registry.Root()` + `EpochCommitter` today; the block-production write
  itself is phase 6b, kept out of the consensus path (a batch is a
  regular transaction / system write, never a header field followers
  reject on, so mobile registration can never fork liveness).

### 3.4 Key lifecycle

Implemented (phase 6b):

- **Revoke** removes a device from active membership: it leaves the BMT
  (its membership proof and any new attestation are refused) and the root
  advances, but its index slot and pubkey are RETAINED so historical
  certs referencing that index still verify.
- **Rotate** = revoke the old key, admit the new key to pending — the
  device continues under a fresh key at the next commit.
- **PruneInactive(cutoff)** revokes every active device whose last
  attestation predates the cutoff, so the committed set does not grow
  unbounded. Liveness is refreshed by `MarkActive`, called when a receipt
  is admitted.

### 3.5 Anchor persistence vs on-chain commitment (honest split)

Two distinct things, only the first of which is built:

- **Persistent anchor log (phase 6b, done).** Each epoch commit writes
  `(epoch → root, headBlock, timeMs)` to a dedicated rawdb table
  (`MobileRegistryAnchors`), queryable via `mobileverify_getAnchors`. This
  is a durable, per-node, cross-checkable record: every node's anchor log
  should converge, and a divergence between nodes' logs is itself a
  signal. It is **not** in the state root.
- **Header-committed anchor (phase 6c, n42 native chain only, DORMANT).**
  Bind the committed accumulator root into the block hash — the SAME
  mechanism the 200K/512 CommitteePool uses, so activation needs no state
  contract, no genesis alloc, and **no replay**:
  - An optional header field `Header.MobileRegistryRoot`
    (`rlp:"optional,nil"`, trailer flag 0x40) the leader stamps from its
    committed registry when `IsMobileAnchor` is active; nil ⇒ omitted ⇒
    pre-fork/eth-el header hashes byte-identical. It participates in the
    consensus block hash via RLP — tamper-evident and light-client
    readable straight from the header, exactly like the committee's
    `ParentBeaconRoot = Blake3(parent CE)` link.
  - **No state-trie write.** Stamping the header does NOT touch the state
    root: there is no system contract, no `SetState`, nothing to
    genesis-alloc or self-deploy. This is why activation is just a
    chainspec flag, like turning on the committee — no chain replay.
    (An earlier draft mirrored EIP-4788 with a ring-buffer system
    contract; that was over-engineering — the committee proves a header
    commitment plus the rawdb side-table is sufficient, and it was the
    state-trie contract that would have needed genesis-alloc/replay. It
    was removed.)
  - **Persistence** of the full root history is the phase-6b rawdb anchor
    log (`MobileRegistryAnchors`), the analogue of the committee's
    `ConsensusEvidence` MDBX table.
  - **No-fork guarantee**: the header field is carried, not consensus-
    validated for correctness (like `ParentBeaconRoot`). Build stamps it,
    import carries it in the block hash — no recomputation, no divergence.
    Correctness is checked off-chain against gossiped membership.
  - **n42 native ONLY, not eth-el.** Stamped only in the n42 miner
    (`worker.commitWork`), gated on `IsMobileAnchor`; eth-el chainspecs
    leave `MobileAnchorTime` nil so the field is never set there.
  - **Dormant.** `MobileAnchorTime` is nil everywhere; byte-inert until a
    test chain sets it. Activation = set the flag on the chainspec, no
    replay — identical to enabling the CommitteePool.

## 4. Component 2 — Leader produces verification data with the block

- **In the sealing pipeline, not after.** The leader produces the
  block's `StreamPacket` during block production (read-log capture is
  an execution observer, same pattern as `internal/bal_capture.go` —
  observing, never mutating). Producing it after
  `OutputBlockCommitted` would make mobile data lag the chain head,
  defeating "timely".
- **Pushed to ALL IDC nodes, not kept at the leader.** The packet
  rides a dedicated gossip topic to every IDC node, each of which
  caches it independently. Any IDC node can then serve any recent
  block's packet — no single-serving-point, leader rotation and leader
  failure do not interrupt mobile service.
- Each IDC node keeps a rolling window (most recent N blocks) of
  packets; N is coupled to the mobile-side "how far back may I verify"
  policy. Packets are content-addressed (`block_hash`) so cache fills
  are idempotent and CDN-friendly.

## 5. Component 3 — Distribution (three segments)

**5a. IDC ↔ IDC**: the gossip fan-out of §4. Existing gossip
infrastructure; the packet is one message per block.

**5b. IDC → mobile (the fan-out that must scale).** Direct-serving
caps out around 10⁷ registered devices per node (bandwidth-bound
capacity model, §10); the 10⁸+ target requires a distribution layer:

- **Target form: torrent swarm.** A block's StreamPacket is identical
  for every consumer — the ideal BT object. Reuse the
  `internal/distributed/storage/torrent` bridge (real, live-tested on
  eth-el cold segments); phones join as light leechers pulling from
  nearby IDC/edge peers, never bound to one origin.
- **Transitional form (ship first): HTTPS + CDN.** Every IDC node
  exposes `GET /mobileverify/packet/{blockHash}`; a CDN fronts the
  fleet. Origins only absorb cache-miss fills. This gets the
  end-to-end loop running before the torrent client lands in the app.
- Either form, the phone's trust model is identical: verify everything
  against the committed header (§8.3). Distribution is untrusted
  plumbing.

**5c. Mobile → IDC (attestation return).** After verifying, the phone
signs a `VerificationReceipt` and POSTs it to **any** IDC node's
collection endpoint (`/mobileverify/receipt`). Per-block collection
window (30–60 s past commit, covering verification latency + network);
within the window, receipts are deduplicated by `MobileIndex` (latest
wins), then the window closes and aggregation runs (§6).

## 6. Aggregation — `MobileAttestationCert`

Per `(block, computed_receipts_root)` bucket, at window close:

```
MobileAttestationCert {
    BlockHash        [32]byte
    BlockNumber      uint64
    ReceiptsRoot     [32]byte   // the root this cohort attested to
    AggregateSig     [96]byte   // BLS aggregate of the cohort's signatures
    SignerMask       []byte     // sparse encoding of MobileIndex set, §6.1
    WindowClosedAt   uint64     // unix ms; not signed, metadata only
}
```

- Aggregation reuses the same `crypto/bls` math as QC aggregation.
  Verification is `FastAggregateVerify(cohortPubkeys, signingMessage)`
  — one aggregate-pubkey addition chain plus 2 pairings, independent
  of cohort size, because all cohort members signed identical bytes
  (that is exactly why `VerificationReceipt` excludes the timestamp).
- Receipts whose `ComputedReceiptsRoot` differs from the committed
  header's are **not discarded**: they aggregate into their own
  cert bucket. A non-empty minority bucket is the divergence-alarm
  signal this whole system exists to produce.
- Certs are bypass objects: stored, optionally gossiped between IDC
  nodes for redundancy, queryable over RPC — never consensus input.

### 6.1 SignerMask — sparse, not dense

A dense bitmap over the registry (the `QuorumCertificate.Signers`
shape) is wrong here: at 10⁷–10⁸ registered devices it is megabytes
per block while actual per-block participation is a small fraction
(duty-cycle ~1/144 × online fraction). Default encoding:

```
uvarint count, then count × uvarint deltas of the SORTED MobileIndex
sequence (first delta = first index; subsequent = gap - 1 … i.e.
strictly ascending, duplicate-free by construction)
```

Compact for sparse cohorts, trivially streamable, and
duplicate-rejection falls out of the strictly-ascending rule. If
participation density ever makes delta-varint lose to a compressed
bitmap (Roaring), the mask gets a 1-byte format tag and both formats
coexist — an implementation detail behind the codec, not a design
change.

## 7. End-to-end flow

```
leader seals block
  → StreamPacket produced in the sealing pipeline          (§4)
  → gossiped to all IDC nodes, each caches a rolling window (§4)
  → phones fetch via CDN/torrent                            (§5b)
  → phone decodes, re-executes against the read log,
    recomputes the receipts root                            (evmsdk, exists)
  → phone signs VerificationReceipt, POSTs to any IDC node  (§5c)
  → collection window: dedup by MobileIndex, bucket by root (§5c)
  → window closes: aggregate per bucket → MobileAttestationCert (§6)
  → cert stored / gossiped / queryable; divergence buckets alarm (§6)
  → NOTHING feeds back into HotStuff voting                 (§1)
```

## 8. Security checklist

1. **Rogue-key**: PoP verified at registration (§3). Non-negotiable.
2. **Sybil**: no stake by design; rate-limited registration plus
   platform device attestation (Play Integrity / DeviceCheck) where
   available, plus per-source registration quotas. The goal is to make
   fake-fleet inflation expensive, not impossible — the data's uses
   (§1) tolerate a noisy tail, and the mask makes cohort composition
   auditable after the fact.
3. **Untrusted distribution**: everything the phone consumes is
   verified against the committed header (StreamPacket is
   content-addressed by block hash; re-execution checks the receipts
   root). A malicious edge can withhold, never falsify.
4. **Collection DoS**: per-IP/per-index rate limits; one receipt per
   `MobileIndex` per block (latest wins); signature verification is
   the gate before a receipt touches any state.
   Batch-verify or sample-then-aggregate-verify under load: honest
   cohorts pass the single aggregate check; on failure, bisect to
   eject the invalid receipts (standard aggregate-verification
   fallback).
5. **Aggregation cost**: measured on this codebase — 512-signer
   aggregate + FastAggregateVerify ≈ 8.9 ms on one core; aggregation
   is point-addition-linear and embarrassingly shardable. The real
   scale cost is mask codec churn, which §6.1 bounds.
6. **Consensus isolation**: certs never enter `ProcessEvent`; the
   mobile surface lives in its own RPC namespace (`mobileverify_*`),
   so "is this consensus input?" is answerable from the call site.

## 9. Implementation roadmap

| Phase | Deliverable | Status |
|---|---|---|
| 1 | `internal/mobileverify`: registry (PoP), receipt intake, window collector, aggregator, `MobileAttestationCert` + sparse mask codec — pure library + tests | **in progress** |
| 2 | Leader-side StreamPacket production in the sealing pipeline + IDC gossip topic + rolling-window cache | |
| 3 | HTTP endpoints (`/mobileverify/register`, `/packet/{hash}`, `/receipt`) + `mobileverify_*` RPC for cert queries | |
| 4 | Client facade convergence: SDK + standalone app on the unified pipeline (registration call, receipt submission) | |
| 5 | CDN deployment recipe; torrent-swarm distribution (reuse storage/torrent) | |
| 6 | Accumulator registry (BMT root), gossip replication, epoch committer, divergence alarms | **done** |
| 6b | Key lifecycle (revoke / rotate / inactivity prune); persistent anchor log + `mobileverify_getAnchors` | **done** |
| 6c | Header-committed anchor: `Header.MobileRegistryRoot` stamped by the leader (n42 native only, committee-style header commitment, fork-gated dormant, NO state contract / NO replay) | **done (worktree, dormant)** |
| 6d | Set `MobileAnchorTime` on a test chainspec, validate cross-node determinism, then live rollout — no genesis alloc, no replay (just the flag, like enabling CommitteePool) | |

Each phase lands independently testable; nothing activates outside the
`mobileverify` namespace until phase 3 wires endpoints, and consensus
code is never touched in any phase.

## 10. Capacity model (engineering estimate)

Assumptions: ~2 s blocks; StreamPacket tens-of-KB typical (read log +
txs; bytecodes usually deduplicated away); a phone session verifies
continuously for ~10 min/day (duty cycle 1/144).

- **Phone side**: sustained downlink well under 1 Mbps; re-execution
  per block far under the block interval on current SoCs. Not the
  bottleneck.
- **IDC direct serving (no CDN)**: egress-bound. 10 Gbps ≈ 3×10⁴
  concurrent ≈ 4×10⁶ registered per node at 1/144 duty cycle; 25 Gbps
  ≈ 10⁷. Ceiling regardless of tuning: ~10⁷ per node.
- **With CDN/torrent distribution** (§5b): origin egress stops being
  the bound; the binding costs move to receipt intake (signature
  checks, shardable across cores and nodes) and mask codec. The
  10⁸+ ("hundreds of millions of devices") target is reachable only
  in this configuration — the distribution layer is a requirement,
  not an optimization.

## 11. Non-goals

- Mobile participation in BFT quorum, under any framing.
- Slashing/staking economics for mobiles.
- Replacing the APOS `AggSign` path on legacy chains (it stays as-is;
  it is simply not the foundation for this system).
- MPT-witness (state-proof) verification on phones: the unified data
  plane verifies **execution** (receipts root). The stateless-witness
  path (`mobile_minimal`, eth-el) remains a separate, complementary
  capability; folding it into this pipeline is future work.
