# HotStuff dynamic-reconfiguration chaos suite

Stability/liveness tests for HotStuff-2 validator reconfiguration under failures:
validator add/remove, node crashes, quorum loss, flapping, and sustained churn.
Everything runs on the **isolated `qs_epoch_test` chain (chainId 95)** on localhost —
never against a live/production chain (stopping a majority of nodes halts consensus).

Windows + PowerShell 7 (`pwsh`). The fleet is 11 nodes: validators 0–6 (genesis) and
observers 7–10 (join via reconfiguration).

## What it found

The sustained-churn probe (`chaos-hard.ps1`) surfaced a real liveness bug (fixed in
`0f98b96b`): reconfiguration lived only in memory, so a node that crashed and
restarted after a validator add/remove reverted to the **genesis** set, could no
longer verify the reconfigured set's QCs (`signers bitmap length mismatch`), and the
whole chain stalled even with every node back up. The active validator set is now
persisted and restored on restart.

## Layout

| file | purpose |
|------|---------|
| `setup.ps1`        | build the test binary + tools, generate the deterministic validator pool and a JWT secret (one-time, idempotent) |
| `deploy-fleet.ps1` | launch/restart a range of nodes (`-First N -Last M`) |
| `chaos.ps1`        | sequential failure scenarios; aborts on the first non-recovery |
| `chaos-hard.ps1`   | aggressive sustained-churn probe, then a recovery check |
| `lib.ps1`          | shared helpers (JWT/RPC, node pid/kill/launch/height, validator pool) — dot-sourced |

## Run

```powershell
# 1. one-time setup (builds build/bin/n42-epoch-test.exe, writes validators.md + jwt.hex)
pwsh test/hotstuff-chaos/setup.ps1

# 2. launch the fleet (validators first, then observers)
pwsh test/hotstuff-chaos/deploy-fleet.ps1 -First 0 -Last 6
pwsh test/hotstuff-chaos/deploy-fleet.ps1 -First 7 -Last 10
# wait until node 0 is producing and observers are synced (eth_blockNumber on :20112..:20122)

# 3. run the scenarios (crash / quorum-loss / majority-crash / flap / reconfig-under-crash)
pwsh test/hotstuff-chaos/chaos.ps1          # -> chaos-log.txt

# 4. (optional) add a validator, then run the sustained-churn regression for the durability fix
#    e.g. broadcast admin_proposeAddValidator(node7) to authrpc 8651..8661, then:
pwsh test/hotstuff-chaos/chaos-hard.ps1     # -> chaos-hard-log.txt  (expect RECOVERED)
```

A PASS is: `chaos.ps1` reaches `CHAOS DONE: all scenarios recovered`, and
`chaos-hard.ps1` logs `RECOVERED` (never `NOT-RECOVERED`). On failure the fleet is
left frozen so you can inspect `E:\qs-test-nodeN\run.log` (per-node consensus logs).

## Config

Override via environment before running (defaults in `lib.ps1`):

| var | default | meaning |
|-----|---------|---------|
| `CHAOS_DATAROOT` | `E:\qs-test-node` | per-node datadir prefix (`…0`,`…1`,…) |
| `CHAOS_BIN`      | `build/bin/n42-epoch-test.exe` | test binary |
| `CHAOS_JWT`      | `test/hotstuff-chaos/jwt.hex`  | authrpc JWT secret |
| `CHAOS_POOL`     | `test/hotstuff-chaos/validators.md` | validator keys (generated) |

Ports per node `i`: http `20112+i`, authrpc `8651+i`, p2p tcp `64000+i`.

## Notes / gotchas

- `validators.md` holds BLS **private keys**. It is deterministic (seed
  `0x4242…42`) and `.gitignore`'d — never commit it; regenerate with `setup.ps1`.
- Chaos uses **hard kill** (`Stop-Process -Force`) to simulate crashes / network loss.
  The helper is `Stop-ChaosNode`, not `Kill`: `kill` is a `Stop-Process` alias and
  aliases outrank functions in PowerShell command resolution.
- True network partition isn't simulated (Windows firewall doesn't filter loopback);
  crash / quorum-loss / flap cover the disconnect + recovery paths.
