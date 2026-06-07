# Caplin EL-follower runbook (#31 Phase 7-B / #32)

The lightweight Caplin EL-follower (option B) drives the N42 eth-el EL's Engine
API fork choice from an HTTP beacon **checkpoint-sync** endpoint — no beacon P2P
(sentinel/gossip) is run. The EL syncs execution payloads via its own eth/68
devp2p; Caplin pins finality and (once synced) follows the EL's live tip.

See `internal/cl/follower.go` for the driver and `docs/ethel/caplin-merge-plan.md`
for the A-vs-B decision and the full component-stack port.

## How it drives the EL

Per `internal/cl/follower.go`:
- **finalized / safe** ← the finalized beacon checkpoint's execution block hash,
  read from `RemoteCheckpointSync.GetLatestBeaconState` (full SSZ state → correct
  across every fork incl. Gloas). Refreshed every `finalityRefreshInterval`
  (15 min; finality moves ~1 epoch / 6.4 min).
- **head** ← the EL's own `CurrentHeader` once it has synced at/past finalized
  (≈12 s liveness via the EL's eth/68 devp2p); during catch-up, head = finalized
  so the EL syncs toward it.
- Drive cadence: `headDriveInterval` = 12 s; redundant updates are deduped.

## Enabling per tier

It is one launch flag — `--caplin.checkpoint.url <beacon endpoint>` (wired to
`BeaconCfg.CheckpointSyncURL`). Build eth-el with `-tags n42el`.

| Tier | Follower | Why |
|:--|:--|:--|
| **minimal** | **on** — `--caplin.checkpoint.url <url>` | snapshot-direct node wants trustless finality + live head without a separate CL |
| **full** | **on** — same flag | production EL; finality from checkpoint, head from its own devp2p |
| **archive** | optional | usually paired with a full external CL; the follower works the same if enabled |
| **mobile** | n/a | mobile uses its own SDK (`cmd/evmsdk`, witness+anchor), not this follower |

```bash
# minimal / full: enable the follower against a trusted beacon checkpoint endpoint
n42 eth-el --tags-built-with n42el \
    --caplin.enabled \
    --caplin.checkpoint.url https://beaconstate.ethstaker.cc \
    --caplin.network mainnet
```

A public beacon checkpoint-sync endpoint (e.g. ethstaker / your own CL) serves
`/eth/v2/debug/beacon/states/finalized` as SSZ; `RemoteCheckpointSync` uses the
network's default endpoint list when `--caplin.checkpoint.url` is empty.

## Validation (catch-up → 12 s live)

Live validation needs a reachable beacon checkpoint endpoint + the EL on eth/68
devp2p. Procedure:

1. **Start** eth-el (n42el) with `--caplin.enabled --caplin.checkpoint.url <url>`.
2. **Bootstrap log:** expect `Caplin follower: finalized checkpoint` with a
   beacon slot + execution number/hash within ~30 s (one full-state fetch).
3. **Catch-up:** while the EL is behind finalized, logs show
   `drove EL fork choice` with `head == finalized`; the EL's CurrentHeader number
   climbs toward the finalized number (watch eth_blockNumber / the EL sync logs).
4. **Live:** once the EL reaches finalized, `head` switches to the EL's own tip
   and advances ≈ every 12 s (`drove EL fork choice` with head != finalized,
   head number increasing). `eth_syncing` → false; `eth_blockNumber` tracks tip.
5. **Finality:** every ~15 min a new `finalized checkpoint` log appears with a
   higher execution number; `finalized`/`safe` in forkChoiceUpdated advance.

Unit coverage of the drive logic (no live endpoint needed):
`go test -tags "nosqlite,noboltdb,n42el" ./internal/cl/ -run TestDriveForkChoice`.

## Known limitations / next

- **Bandwidth:** the finalized refresh downloads a full (~150 MB) beacon state.
  TODO(caplin-light): swap in a light `/eth/v1/beacon/headers/finalized` poll +
  per-block execution-hash resolution (fork-version care needed for Gloas).
- **No independent fork choice:** option B trusts the checkpoint endpoint for
  finality and follows the EL's devp2p for head. It does not run attestation-based
  fork choice (that needs the beacon-gossip stack — option A, intentionally not
  ported). For adversarial-environment head selection, run a full external CL.
