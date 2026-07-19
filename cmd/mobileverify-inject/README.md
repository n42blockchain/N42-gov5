# mobileverify-inject

Live-fleet end-to-end verification harness for the mobileverify **Layer 2B**
commit-reveal defense — the part unit and wire tests cannot cover, because it
drives real simulated devices against a running IDC fleet over the actual HTTP
+ P2P path.

## What it verifies

It registers `-honest` honest devices plus one **duplicate** device, waits for
the epoch to commit, then for each new block:

- submits each honest device's receipt to a **distinct** node, and
- submits the duplicate device's receipt to **two** nodes (node0 and node1) —
  the cross-node protocol violation that Layer 2B's reconciliation exists to
  catch.

After the height-gated cohort phase machine runs (commit → reveal → reconcile →
merge), it queries the finalized certificate and asserts the duplicate device
was **excluded** while every honest device is **present** — a genuine f+1 ban
driven by proof-of-possession reveals, on the wire.

## Node prerequisites

The cohort pipeline (relay + reveal gossip topic) is **auto-enabled by
`--profile n42`** — `applyN42NativeProductionDefaults` flips
`MobileVerifyCfg.Enabled` on for the n42 native profile, so no `--mobileverify`
flag is needed. What the harness *does* need is the phone-facing HTTP surface,
which is off by default:

```
--mobileverify.http 127.0.0.1:<port>
```

This exposes `/mobileverify/register`, `/mobileverify/receipt`,
`/mobileverify/cert/<hash>`, `/mobileverify/health`, etc.

### Fleet deploy note

`E:\deploy-7node.ps1` (the qs 7-node HotStuff launcher) exposes the mobileverify
HTTP surface on port `21012 + i` per node (RPC is `20012 + i`). The `--profile
n42` it already passes auto-enables the cohort pipeline; the script adds only
`--mobileverify.http` for the HTTP surface. (An earlier explicit `--mobileverify`
was redundant and has been removed.)

## Usage

```
mobileverify-inject \
  -mv  http://127.0.0.1:21012,http://127.0.0.1:21013,...,http://127.0.0.1:21018 \
  -rpc http://127.0.0.1:20012 \
  -honest 6 -rounds 4 -settle 12
```

| flag       | default                     | meaning                                             |
|------------|-----------------------------|-----------------------------------------------------|
| `-mv`      | `http://127.0.0.1:21012`    | comma-separated mobileverify HTTP base URLs, one/node |
| `-rpc`     | `http://127.0.0.1:20012`    | JSON-RPC URL for block info                          |
| `-honest`  | `6`                         | number of honest devices                            |
| `-rounds`  | `4`                         | blocks to inject over once devices are live         |
| `-settle`  | `10`                        | blocks to wait for the phase machine before querying |

Registrations are spread across nodes because each node is a separate process
with its own per-IP registration rate limiter (10/min). `register` is idempotent:
once the epoch has committed it returns the device's real assigned index (the
first, pending call returns a 0 placeholder), so the harness re-registers after
the devices go live to learn the true indices before analyzing the certificate.

## Expected output

```
committed indices refreshed: dup=15 honest=[18 19 16 20 17 14]
round 1/4: injected block N (…) — 8/8 submissions accepted
...
block N: PASS — dup device idx 15 EXCLUDED ✓, all 6 honest present ✓
==== summary: 4 blocks with dup excluded, 0 fail ====
L2B fleet verification: PASS
```

Exit code is non-zero if any injected block failed to exclude the cross-node
duplicate.
