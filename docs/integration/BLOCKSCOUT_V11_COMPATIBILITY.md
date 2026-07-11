# Blockscout v11 Compatibility (n42 self-chain + eth-el)

**Blockscout target**: v11.x (backend v11.0.0 / v11.1.0, frontend v2.8.0) — supersedes the
earlier v9.3.2 baseline (`BLOCKSCOUT_V9.3.2_COMPATIBILITY.md`).
**Node RPC spec followed**: <https://docs.blockscout.com/setup/requirements/node-tracing-json-rpc-requirements>

N42 runs in two modes that share the same RPC handler code but wire it to different
storage backends, so Blockscout must be pointed at the right endpoint per mode:

| Mode | Chain | RPC host | Notes |
|------|-------|----------|-------|
| **n42** | self-chain (HotStuff / PoA / PoS) | `internal/node.Node` startRPC | full RPC, all namespaces |
| **eth-el** | Ethereum-compatible EL | `internal/ethel` public RPC service | per-mode surface gated by `rpccaps` |

---

## 1. Node JSON-RPC methods Blockscout v11 requires

From the Blockscout node-requirements doc, indexing needs:

- **eth_** (block/tx fetching): `eth_blockNumber`, `eth_call`, `eth_getBalance`, `eth_getCode`,
  `eth_getBlockByHash`, `eth_getBlockByNumber`, `eth_getTransactionByHash`,
  `eth_getTransactionByBlockHashAndIndex`, `eth_getTransactionByBlockNumberAndIndex`,
  `eth_getTransactionReceipt`, `eth_getUncleByBlockHashAndIndex`, `eth_getLogs`.
  (Plus `eth_getBlockReceipts` as the fast per-block receipt path.)
- **Internal transactions** — one of:
  - *Geth variant* (`ETHEREUM_JSONRPC_VARIANT=geth`): `debug_traceBlockByNumber` /
    `debug_traceTransaction` with the **callTracer** (Blockscout's default since v5.1.0).
  - *Erigon variant* (`ETHEREUM_JSONRPC_VARIANT=erigon`): `trace_replayBlockTransactions`,
    `trace_block`.
- **Pending transactions**: `txpool_content` (geth/erigon).
- **WebSocket** (recommended): `newHeads` subscription; otherwise Blockscout polls
  `eth_blockNumber`.

---

## 2. n42 mode — status: COMPLETE

All required methods are served by the shared handlers in `internal/api` +
`internal/tracers`, registered in `internal/node/node.go` `startRPC`.

### 2.1 What was added/fixed for v11

- **`trace_` namespace (Parity/Erigon) — NEW** (`internal/tracers/trace_api.go`,
  registered in `tracers.APIs`). Drives the existing `flatCallTracer`
  (`internal/tracers/native/call_flat.go`), which already emits the OpenEthereum flat-trace
  shape. Methods: `trace_block`, `trace_transaction`, `trace_get`,
  `trace_replayBlockTransactions`, `trace_replayTransaction`, `trace_call`, `trace_filter`.
  This unblocks Blockscout's **erigon variant**. (Only the `trace` type is fully populated in
  the replay envelope; `stateDiff` / `vmTrace` return `null` — Blockscout's internal-tx
  indexing requests only `trace`.)
- **`eth_getTransactionReceipt` `blobGasPrice` fix** (`internal/api/api_transaction.go`):
  was a hardcoded `1`; now computed from the block's `excessBlobGas` via
  `transaction.CalcBlobFee`, matching `eth_getBlockReceipts`.
- **`txpool_status` / `txpool_inspect` — NEW** (`internal/api/api_misc.go`): the two
  txpool summary methods that were previously missing.

### 2.2 Already present (verified)

`eth_getBlockReceipts`, `eth_feeHistory`, `eth_maxPriorityFeePerGas`, `eth_getProof`,
`eth_getLogs`, full-tx `eth_getBlockBy*`, `eth_syncing`, `eth_chainId`; the geth
**callTracer** path (`debug_traceTransaction`/`debug_traceBlockByNumber` with
`{"tracer":"callTracer"}`) via the `internal/tracers` service (which wins the `debug`
namespace merge); EIP-1559/4844 receipt & block fields (`effectiveGasPrice`, `blobGasUsed`,
`blobGasPrice`, `withdrawalsRoot`, `withdrawals`, `excessBlobGas`, `baseFeePerGas`, Cancun-gated).

### 2.3 Config (n42)

The RPC namespaces are gated by `--http.api` / `--ws.api` (there is **no** "empty = all"
default — the operator's flag decides). For Blockscout, enable the tracing namespaces:

```bash
n42 --http --http.addr 0.0.0.0 --http.port 8545 \
    --http.api eth,net,web3,txpool,debug,trace \
    --http.corsdomain "https://blockscout.example.com" \
    --ws --ws.addr 0.0.0.0 --ws.port 8546 --ws.api eth,net,web3
```

Blockscout compose env:
- Geth variant: `ETHEREUM_JSONRPC_VARIANT=geth`, `ETHEREUM_JSONRPC_TRACE_URL=http://n42:8545`.
- Erigon variant: `ETHEREUM_JSONRPC_VARIANT=erigon` (uses the new `trace_` namespace).

> Note: the old v9.3.2 doc listed `miner` / `personal` / `rpc` namespaces — those live only
> in the unused `internal/api/router.go` and are **not** registered by the production node
> path; do not rely on them.

---

## 3. eth-el mode — public RPC service

eth-el (execution-layer client, `internal/ethel`) previously exposed only the Engine API
plus a 3-method `eth` stub (`eth_syncing`/`eth_chainId`/`eth_blockNumber`) behind JWT for CL
upcheck — not a Blockscout-facing surface. v11 adds a dedicated **public (non-JWT) JSON-RPC
service** that reuses the same `internal/api` + `internal/tracers` handlers, gated per data
mode by `internal/ethel/rpccaps`.

### 3.1 Feasibility (why reuse works)

eth-el writes blocks through the **standard `rawdb`** schema
(`WriteHeader` / `WriteRawBody` / `WriteCanonicalHash` / `WriteHeadBlockHash` in
`engine_state_adapter.go`), so the `internal/api` block / header / body / transaction /
receipt / log readers work directly over the eth-el DB. State reads
(`eth_getBalance` / `eth_getCode` / `eth_getStorageAt` / `eth_call` / `eth_getProof`) use
eth-el's **hashed-canonical** state (no `PlainState`), so they go through eth-el's own state
reader rather than `state.NewPlainState`.

### 3.2 Per-mode surface (`rpccaps.Serviceable`)

The `rpccaps` matrix is the single source of truth for what each mode can serve; the public
service consults it and returns a clean *"method not available in mode X"* instead of a wrong
answer. Summary (Latest / Historical scope):

| Method class | M0 | M1 | Full | Archive |
|---|---|---|---|---|
| meta / header | yes | yes | yes | yes |
| body / tx-by-index | window | no | yes | yes |
| tx-by-hash | no | no | yes | yes |
| state / proof / EVM (latest) | window | yes | yes | yes |
| state / proof / EVM (historical) | no | no | no | yes |
| receipts / logs (latest) | window | slow | yes | yes |
| receipts / logs (historical) | no | no | no | yes |
| mempool (sendRawTx) | no | no | yes | yes |

**Archive** is the recommended Blockscout backend (full history). **Full** works for
recent-data indexing; historical queries return not-available. **M0/M1** serve only a rolling
window / latest and are not suitable as a general explorer backend.

### 3.3 Status — implemented (`internal/ethel/publicrpc/`)

- **`chain.go`** — read-only `common.IBlockChain` over the eth-el DB (block / header / body /
  tx / receipt / log reads via `rawdb`; write & lifecycle methods inert). `StateAt` builds the
  eth-el state reader so the trace backend can re-execute.
- **`service.go`** — constructs `api.NewAPI(chain, db, engine, …)` + `tracers.APIs`, registers
  `eth` / `web3` / `net` + `debug` / `trace`, installs the per-mode state-reader provider, and
  serves on a public (non-JWT) HTTP port. (The `txpool` + `n42` namespaces are skipped — a read
  node has no mempool; `rpccaps` gates mempool methods anyway.)
- **`gate.go`** — the `rpccaps` capability gate (HTTP middleware): rejects methods the data mode
  cannot serve with a clean `-32601 "not available in <mode> mode"`. Unit-tested (`gate_test.go`).

**Wiring**: `cmd/eth-el` registers the service via `RegisterFactory`; flags
`--publicrpc.enabled` / `--publicrpc.host` / `--publicrpc.port` (default 20015) /
`--publicrpc.mode` (`archive`|`full`|`m1`|`m0`). Built with `-tags …,n42el`.

**Blockscout compose** (eth-el):
```
ETHEREUM_JSONRPC_VARIANT=erigon         # or geth
ETHEREUM_JSONRPC_HTTP_URL=http://eth-el:20015
ETHEREUM_JSONRPC_TRACE_URL=http://eth-el:20015
```

### 3.4 Hashed-canonical state

When the node runs `--hashed-canonical` (reth-2.2 model, no PlainState), the state provider
switches to a hashed reader (reads `HashedAccounts`/`HashedStorage`, hashing the lookup key):

- **Latest / tip** — `state.HashedStateReader`. ✅ `eth_getBalance` / `getCode` /
  `getStorageAt` / `getTransactionCount` / `eth_call` / `eth_estimateGas` at `latest`, and
  `debug_`/`trace_` for the tip block.
- **Historical (Archive)** — `state.HashedHistoricalReader` (`modules/state/hashed_historical.go`).
  ✅ the same methods + `debug_`/`trace_` re-execution at **any** past block. It mirrors
  `PlainState`'s history path exactly — `FindByHistory` over the plain-keyed
  `AccountsHistory`/`StorageHistory` index + `AccountChangeSet`/`StorageChangeSet` — but falls
  back to the tip hashed state instead of the (empty) plain base for keys unchanged since the
  target block. Round-trip unit-tested (`hashed_historical_test.go`). Full mode (latest-only)
  returns a clean *"historical state not available in full mode"*.

Block/tx/receipt/log methods work at any height regardless of state mode (rawdb).

**Follow-ups**:
1. **Historical-scope gating.** The gate checks `Latest` scope; for `Full`, historical state/log
   queries slip through as `Latest` rather than being rejected. Refine with block-param scope
   detection.
2. **Live validation** against a real eth-el archive datadir (`E:\n42-archive-test`): confirm
   balances/logs/traces at latest and a historical height.

---

## 4. References

- Blockscout node requirements: <https://docs.blockscout.com/setup/requirements/node-tracing-json-rpc-requirements>
- Blockscout v11 release: <https://www.blog.blockscout.com/new-release-blockscout-v11-0-0/>
- eth-el RPC capability matrix: `internal/ethel/rpccaps/caps.go`, `docs/ethel/minimal-client-rpc-capability.md`
