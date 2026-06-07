# Caplin independent fork choice — runbook (#34, B+)

The **independent fork choice** driver (`internal/cl/independent_forkchoice.go`)
runs caplin's real LMD-GHOST + Casper FFG over beacon blocks gathered from a
**diverse beacon peer set**, and drives the N42 eth-el EL to the
attestation-weighted head. It replaces the lightweight follower
(`follower.go`), which blindly pinned the EL to the EL's own eth/68 devp2p tip
and is therefore **not safe in an adversarial environment** (an attacker who
controls your EL peers can feed a valid-but-non-canonical chain).

See `docs/ethel/caplin-merge-plan.md` (2026-06-07 #34 section) for the full
component stack and the bottom-up port history.

## What makes it adversarial-safe

| | follower (`follower.go`) | independent fork choice |
|:--|:--|:--|
| **head source** | the EL's own devp2p tip (`CurrentHeader`) — **unvalidated** | `forkChoice.GetHead()` — LMD-GHOST + FFG over attestations |
| **block source** | n/a (EL syncs itself) | many beacon peers via gossip + req/resp |
| **validation gate** | none (trusts the EL tip) | `OnBlock(fullValidation=true)` — state transition + sig checks |
| **finality** | single checkpoint endpoint | checkpoint anchor + FFG over received blocks |

The guarantee is structural: `resolveHeadExec` (the head→EL decision) takes only
the fork-choice store, **never** the engine/EL tip — so a malicious EL peer
cannot move the head. Pinned by `independent_forkchoice_test.go`
(`TestResolveHeadExec_*`).

## Enabling

Independent fork choice runs when **both** a checkpoint endpoint **and** a
sentinel discovery port are set (otherwise the lightweight follower runs):

```bash
n42 eth-el --tags-built-with n42el \
    --caplin.enabled \
    --caplin.checkpoint.url https://beaconstate.ethstaker.cc \
    --caplin.network mainnet \
    --caplin.sentinel.discovery.port 4000
```

- `--caplin.checkpoint.url` → `BeaconCfg.CheckpointSyncURL` (weak-subjectivity anchor).
- `--caplin.sentinel.discovery.port` → `BeaconCfg.SentinelDiscoveryPort` (>0 enables beacon P2P).
  **Defaults to 9000** (`cmd/eth-el/main.go`), so independent fork choice is the
  default whenever caplin is enabled with a checkpoint URL.

Set `--caplin.sentinel.discovery.port 0` to fall back to the lightweight
follower (EL-tip following, not adversarial-safe).

Build eth-el with `-tags n42el`. The driver opens a libp2p host + discv5 on the
discovery port and joins the beacon gossip + req/resp protocols.

> Note: the current wiring binds discovery UDP and libp2p TCP to the same port
> value (`Port == TCPPort == SentinelDiscoveryPort`, `IpAddr = 0.0.0.0`). Splitting
> them + NAT/bootnode config is tracked as production hardening in the merge plan.

## Validation: catch-up → live

1. **Bootstrap.** Expect within ~30 s:
   `Caplin independent fork choice: anchor loaded` with a `beaconSlot` and
   `anchorRoot`, then `... sentinel started`.
2. **Peers.** `Caplin P2P ... peers_count` climbs as discv5 finds beacon peers
   and the handshake (Status fork-digest match) admits them.
3. **Block ingestion.** Two paths feed `OnBlock`:
   - `blockSyncLoop` — req/resp `beacon_blocks_by_range` from many peers (catch-up
     + the reliable backstop), every ~4 s.
   - `gossipBlockLoop` — `beacon_block` gossip (sub-slot latency for the live tip);
     expect `... subscribed to beacon_block gossip`.
4. **Head drive.** As fork choice accumulates blocks + attestations, expect
   `Caplin independent fork choice: drove EL to attestation-weighted head` with a
   `headExec` that advances ≈ every slot once live. `eth_blockNumber` on the EL
   tracks this head.
5. **Finality.** `finalizedExec` advances ~1 epoch (6.4 min) behind the head.

## Validation: adversarial head selection (the point of B+)

Goal: confirm the EL follows the **attestation-weighted** head, not whatever its
own devp2p peers feed it.

- **Deterministic (CI):**
  `go test -tags "nosqlite,noboltdb,n42el" ./internal/cl/ -run TestResolveHeadExec`
  proves the head→EL decision is sourced solely from the fork choice (the fake
  resolver exposes no EL tip), so an adversarial EL tip is structurally ignored.
  `forkchoice` package tests prove `GetHead` returns the higher-attestation-weight
  branch when two chains compete, and `OnBlock(fullValidation)` rejects invalid
  blocks.
- **Live (manual):** point the EL's eth/68 devp2p at a peer serving a
  valid-but-non-canonical (lower-weight) chain while the beacon peer set serves
  the canonical chain. The `drove EL to attestation-weighted head` log must track
  the canonical (beacon-attested) head; the EL must NOT advance onto the
  adversarial chain. (Needs a controlled two-chain testnet; not a public-mainnet
  procedure.)

## Known limitations / next

- **Fork-digest transitions:** `gossipBlockLoop` subscribes to the *current* fork
  digest only; across a fork it stops receiving gossip until restart. `blockSyncLoop`
  (req/resp) is the backstop and keeps working. Auto-resubscribe on fork change is
  a follow-up.
- **Attestation subnets / aggregation / sync committee** gossip services are
  intentionally not ported (block-only B+). Block-embedded attestations drive fork
  choice; live single-slot tip reorg resistance is slightly weaker than a full
  beacon node but finality + near-tip safety hold.
- **Production hardening:** split discovery/TCP ports, NAT (`extip:`/stun),
  bootnode/static-peer config surface, configurable `MaxPeerCount`.
