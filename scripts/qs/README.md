# qs fleet ops scripts (Linux)

Port of the Windows PowerShell fleet scripts. Same chain, same ports, same
levers; the Windows-only pieces (`sendbreak.exe` CTRL_BREAK, `Start-Process`,
`robocopy`) are replaced by `kill -INT`, `setsid`, and `cp -a`.

**No key material lives in this directory.** Both secrets are supplied from
outside:

| Secret | How | Note |
|---|---|---|
| 7 validator BLS keys | `QS_VALIDATORS` → a `qs-validators.md` you copy out of band | never commit, never place inside a node datadir |
| dev faucet key | `N42_DEV_FAUCET_KEY` env var | without it the fleet runs but produces empty blocks |

## Layout

`qs-env.sh` is the single source of truth for paths, ports, environment levers
and the launch argument set. Everything else sources it. That is deliberate: the
Windows deploy script declared the environment only inside itself, and a rolling
restart driven by a different script silently dropped `N42_TXINDEX_TAIL` — which
put every tx-lookup row back inside the consensus commit and made
`eth_getTransactionByHash` return null across the indexed range. A lever declared
outside the thing that launches the process is a lever that gets dropped.

For the same reason the launch arguments live in `qs_build_args` rather than
being copied into each script; the Windows roll script carried a second copy
annotated "mirrors deploy BuildArgs exactly".

## Before the first run

```bash
# 1. Reserve the UDP ports. LINUX-ONLY HAZARD: 33000-33006 falls inside the
#    default ephemeral range (net.ipv4.ip_local_port_range = 32768 60999), so
#    any outbound connection from any process can steal a node's listen port.
#    The Windows fleet lost a node on two consecutive starts to the same class
#    of bug. TCP 32000-32006 is below the range and needs nothing.
sudo sysctl -w net.ipv4.ip_local_reserved_ports=33000-33006

# 2. File descriptors: 7 MDBX instances plus libp2p.
ulimit -n 65536

# 3. Point the scripts at your layout (defaults are under /data/blockchain).
export QS_ROOT=/data/blockchain
export QS_VALIDATORS=$HOME/qs-validators.md
export N42_DEV_FAUCET_KEY=<dev faucet key>
```

## Weekly cycle

The weekly source is the stopped fleet's `qs-node0`, exactly like the Windows
runbook's `E:\qs-node0`; it is not the external/mainnet catch-up database.
That distinction preserves the native fleet's dev rewards, faucet nonce and
synthetic-load history across the replay fold. An external-mainnet replay is a
one-time bootstrap of a new independent fleet and starts without that live
economic state.

```bash
export QS_SOURCE=/data/blockchain/qs-node0
```

| Step | Command |
|---|---|
| 1. stop + record | `./stop-fleet.sh` |
| 2. fold the week in | `n42 replay-v2 --source $QS_SOURCE --target $QS_BASE --chain mainnet_qmdb_staggered --tree qmdb` |
| 2b. seal + hot | `n42-ancient-seal --source $QS_BASE --out $QS_SEED --seal` then `--emit-hot` |
| 2c. tx index | `./build-seed-txindex.sh` |
| 3. re-seed | `mv $QS_NODE_ROOT{0..6}` aside, then `./deploy-7node.sh` |
| 4. accept | heights advancing and identical across `20012..20018` |

For the first bootstrap only, run the normal fleet through Step 4 before the
first throughput round. The standard 3000 x 3000 benchmark needs about 1,897
ETH in the dev faucet; an established fleet has accumulated that from the
1 ETH/block `devBlockReward`, while a newly replayed external-mainnet seed has
not. Subsequent weekly folds from `qs-node0` retain it.

`--source` / `--target` take the datadir **root**; the tool appends
`/chaindata` itself. Passing the chaindata path fails with an Accede-mode error.

`--data` for `deploy-7node.sh` is the **era layout** dir, not the raw replay
base.

## Isolation

The mesh binds and advertises `127.0.0.1` with discovery off. Do not change that
to a LAN address while another machine runs this same chain with the same BLS
keys: the two fleets would be seen as one validator set equivocating, and this
chain ships its own equivocation detector and slashing.

## Stopping

`SIGINT` and `SIGTERM` both reach the graceful path (`cmd/n42/app.go`).
**Never `SIGKILL`** — it truncates the MDBX spill and poisons the QMDB undo
layer.

A fleet-wide stop can leave `lockedQC` ahead of `committedQC`. That is expected
and now self-healing: the next leader re-applies the locked parent before
building (`Service.ensureParentApplied`). If a fleet still will not produce:

```bash
hotstuff-reset -datadir <node>/chaindata -apply -force -backup <file>
```

## Benchmark

```bash
./bench-run.sh --offset 1600000 --tag r1 --decay-sec 90
```

One round launches the fleet at the 480M gas tier, floods it, samples three
60 s windows, then stops everything. Rules the rig enforces, each one paid for:

This is the Ubuntu spelling of the Windows sequence
`bench-run.ps1 -> bench-7node.ps1 -> txflood -> measure-tps.ps1 -> stop`.
Funding's complete nonce chain is submitted to node 0 and propagated by the
existing P2P mesh; submitting the same chain to all seven RPCs duplicates the
gossip traffic and fills the logs with `already known`.

- **`--offset` must be fresh every round.** Reusing a drained sender set makes
  the pool demote in a spiral that looks exactly like a node failure.
- **`--decay-sec 90` unless the chain is already idle.** A round following a
  full-block round inherits its elevated baseFee, the fixed 10 gwei flood can no
  longer fill every block, and occupancy collapses to ~53% — a number that
  cannot be compared with a decayed-start round.
- **Compare like-numbered windows.** baseFee climbs *within* a round too
  (12.5% per full block), so win3 of a full-block round measures the flood's
  price cap rather than the chain. win1 is the comparable one.
- **Occupancy below ~95% means the supply pipe was the limiter**, not the
  chain, and the TPS figure is not a chain result.
- **`--profiling` only for rounds whose purpose IS the profile.** Mutex/block
  sampling sits inside the critical path being measured.

`--maxcpu` defaults to `(nproc + 6) / 7` — about 37 per node on 256 threads,
matching how the Windows rig deliberately ran 7x5=35 on 32 threads. Override
with `QS_MAXCPU` if that turns out wrong for this hardware.

Two things the Windows rig had that are worth knowing about: `N42_MAX_GOSSIP_MB=8`
(the 1 MiB default caps a block at ~8.5k transfers = 37% of the 480M tier no
matter how deep the pool is, so without it the rig measures the wire cap) and
`N42_STRESS_GASLIMIT=1` (lets gasceil reach the target in one block). Both are
set by `bench-7node.sh`.
