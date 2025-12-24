# `flashbots` Namespace

The `flashbots` API provides methods for MEV (Maximal Extractable Value) bundle submission and validation, compatible with the Flashbots relay specification.

## `flashbots_sendBundle`

Submits a bundle of transactions to be included in a specific block.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "flashbots_sendBundle", "params": [bundle]}` |

### Parameters

- `bundle`: Object containing:
  - `txs`: Array of signed transactions (hex-encoded)
  - `blockNumber`: Target block number (hex)
  - `minTimestamp` (optional): Minimum timestamp for inclusion
  - `maxTimestamp` (optional): Maximum timestamp for inclusion
  - `revertingTxHashes` (optional): Transactions allowed to revert

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"flashbots_sendBundle","params":[{"txs":["0x...","0x..."],"blockNumber":"0x100"}]}
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "bundleHash": "0x..."
    }
}
```

## `flashbots_callBundle`

Simulates a bundle to estimate its profitability.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "flashbots_callBundle", "params": [bundle]}` |

### Parameters

Same as `flashbots_sendBundle` with additional:
- `stateBlockNumber`: Block number to use as state reference

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"flashbots_callBundle","params":[{"txs":["0x..."],"blockNumber":"0x100","stateBlockNumber":"latest"}]}
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "bundleHash": "0x...",
        "coinbaseDiff": "0x...",
        "gasFees": "0x...",
        "results": [
            {
                "txHash": "0x...",
                "gasUsed": "0x5208",
                "value": "0x..."
            }
        ],
        "stateBlockNumber": "0xff",
        "totalGasUsed": "0xa410"
    }
}
```

## `flashbots_getBundleStats`

Returns statistics about a previously submitted bundle.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "flashbots_getBundleStats", "params": [bundleHash, blockNumber]}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"flashbots_getBundleStats","params":["0x...", "0x100"]}
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "isSimulated": true,
        "isSentToMiners": true,
        "isHighPriority": false,
        "simulatedAt": "2024-01-01T00:00:00Z",
        "submittedAt": "2024-01-01T00:00:01Z"
    }
}
```

## `flashbots_getUserStats`

Returns statistics for a Flashbots searcher.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "flashbots_getUserStats", "params": [blockNumber]}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"flashbots_getUserStats","params":["latest"]}
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "isHighPriority": true,
        "allTimeMinerPayments": "0x...",
        "allTimeGasSimulated": "0x...",
        "last7dMinerPayments": "0x...",
        "last7dGasSimulated": "0x...",
        "last1dMinerPayments": "0x...",
        "last1dGasSimulated": "0x..."
    }
}
```

## `flashbots_cancelBundle`

Cancels a previously submitted bundle.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "flashbots_cancelBundle", "params": [bundleHash]}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"flashbots_cancelBundle","params":["0x..."]}
{"jsonrpc":"2.0","id":1,"result":true}
```

