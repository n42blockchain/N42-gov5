# eth-el node — architecture

## What this is

`eth-el` is N42's **Ethereum mainnet execution-layer node**. It runs side-by-side with a consensus-layer client (Caplin embedded, or external Lighthouse/Prysm) and exposes the standard Engine API to it.

It is **distinct from** `cmd/n42` (the native HotStuff chain) and **distinct from** `cmd/ethexec` (a stage-validation test program that replays mainnet blocks to generate leaves journals + witness for BT distribution). `cmd/ethexec` is a developer tool; `cmd/eth-el` is a deployable node.

## What makes it different from geth/erigon/reth

The bootstrap path. Instead of downloading every block from genesis (`full sync`), or downloading a state snapshot at a recent finalized height (`snap sync`), eth-el uses **leaves-based bootstrap**:

1. CL (or a config flag) tells us the chain tip / desired starting point.
2. BitTorrent downloads pre-computed `leaves journal` segments (built by `cmd/ethexec` on a beefy machine, distributed via the OtterSync swarm).
3. `RebuildState` materialises a full PlainState at some recent block N, directly from the leaves — no replay required.
4. Headers and bodies for the small range `[N+1, tip]` are downloaded (BT or P2P), executed by the existing `Executor`, and the node is now in sync.
5. From this point on the node is "live": Engine API takes over and CL drives `NewPayload` / `ForkchoiceUpdated` for new blocks.

This compresses initial sync from days/weeks to hours: leaves are O(active accounts), not O(history).

## Services

`Node` orchestrates the following services. Each implements a tiny `Service` interface (`Start(ctx) error`, `Stop() error`) and is registered in start-order; shutdown happens in reverse.

| # | Service              | Owner of                                        | Lifecycle      |
|---|----------------------|-------------------------------------------------|----------------|
| 1 | `chaindb`            | MDBX env (chaindata)                            | persistent     |
| 2 | `outFreezer`         | leaves / witness / receipts freezer             | persistent     |
| 3 | `torrentClient`      | anacrolix/torrent client + DHT                  | persistent     |
| 4 | `torrentSync`        | OtterSync importer/exporter (BT ↔ chaindata)    | persistent     |
| 5 | `bootstrap`          | one-shot: leaves DL → RebuildState              | one-shot       |
| 6 | `catchUp`            | one-shot: BT segment replay → tip               | one-shot       |
| 7 | `engineAPI`          | http+ipc Engine API server on auth-RPC port     | persistent     |
| 8 | `caplin` (optional)  | embedded CL — only with `-tags n42el`           | persistent     |
| 9 | `live`               | sentinel goroutine; blocks until ctx.Done       | persistent     |

One-shot services (`bootstrap`, `catchUp`) implement `Start` as the actual work and `Stop` as a no-op. They run synchronously during Node startup, before any persistent service that depends on them. If they fail, Node startup fails.

Persistent services (`chaindb`, `outFreezer`, `torrentClient`, `torrentSync`, `engineAPI`, `caplin`, `live`) are kicked off after the one-shots succeed. They each run their own goroutines and respect `ctx.Done()` for graceful shutdown.

## Lifecycle state machine

```
                  +-----------+
                  |   INIT    |  (cli flags parsed, conf assembled)
                  +-----+-----+
                        |
                        v
                +---------------+
                |  OPEN_STORAGE  |  chaindb, outFreezer
                +-------+-------+
                        |
                        v
               +-----------------+
               |  OPEN_DOWNLOAD  |  torrentClient, torrentSync
               +--------+--------+
                        |
                        v
                  +------------+
                  | BOOTSTRAP  |  download leaves → RebuildState
                  +-----+------+   (skip if chaindata already populated)
                        |
                        v
                  +-----------+
                  |  CATCH_UP |  download segments → executor.Run
                  +-----+-----+   (loops until head ≥ desired)
                        |
                        v
                  +-----------+
                  | START_API |  engineAPI server bound + listening
                  +-----+-----+
                        |
                        v
                  +-----------+
                  | START_CL  |  if --caplin.enabled (n42el tag)
                  +-----+-----+
                        |
                        v
                  +-----------+
                  |   LIVE    |  block on ctx.Done()
                  +-----+-----+
                        |
                        v  ctx cancelled (SIGINT/SIGTERM)
                  +-----------+
                  |  SHUTDOWN |  reverse-order Stop on every persistent service
                  +-----------+
```

## Caplin integration

Caplin (the embedded CL, `internal/cl/`) is an **optional** service gated behind the `n42el` build tag. The same eth-el binary built with or without `-tags n42el` is byte-compatible at the surface level: only the presence of internal services differs.

When enabled (`--caplin.enabled` AND `-tags n42el`):
- The Node creates a `cl.Service` after the Engine API server is listening.
- The `cl.Service` receives an `*eladapter.Adapter` whose `Backend` is the eth-el Node's `chaindb` reader (existing `internal/cl/eladapter` from Phase 5–6 work).
- Caplin runs in a separate goroutine with its own dedicated MDBX (`<datadir>/caplin/`).
- On shutdown, Caplin is stopped before the chaindata MDBX so the Backend reads do not hit a closed db.

When disabled (default):
- A no-op stub satisfies the same interface so `cmd/eth-el` builds without `-tags n42el`.
- An external CL (Lighthouse, Prysm) is expected to talk to eth-el over the HTTP Engine API on the configured auth-RPC port.

## Reference architectures

The split mirrors:

- **erigon's `cmd/erigon`**: stage-loop bootstrap + Engine API ([cmd/erigon/main.go](https://github.com/erigontech/erigon))
- **reth's `cmd/reth`**: pipeline + tree + RPC + AuthServer ([crates/node](https://github.com/paradigmxyz/reth))
- **geth's `cmd/geth`**: full node service collection inside `node.Node`

Where eth-el deviates is in the bootstrap layer — the leaves-journal path replaces the snap-sync state download. Everything from "in sync" onwards is identical to a standard EL.

## Files

```
cmd/eth-el/
  main.go            cli, signal handling, calls Node
  beacon_wire.go     //go:build n42el  — wires Caplin to the Node
  beacon_wire_stub.go //go:build !n42el — no-op shim
  beacon_backend.go  //go:build n42el  — eladapter.Backend impl
  beacon_backend_test.go

internal/ethel/
  node.go            Node struct, registration, Start/Stop
  service.go         Service interface + helpers
  + existing executor / catch_up / rebuild_state / etc.

conf/
  ethel_config.go    EthELCfg root config
  beacon_config.go   already exists (Phase 0)
```
