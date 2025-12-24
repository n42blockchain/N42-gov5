# `miner` Namespace

The `miner` API provides methods for controlling the mining/sealing process.

> **Note**
> 
> In N42's Proof of Authority (PoA) consensus, these methods control the block sealing process rather than traditional PoW mining.

## `miner_setExtra`

Sets the extra data to be included in sealed blocks.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "miner_setExtra", "params": [extra]}` |

### Parameters

- `extra`: Extra data bytes (max 32 bytes)

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"miner_setExtra","params":["0x..."]}
{"jsonrpc":"2.0","id":1,"result":true}
```

## `miner_setGasPrice`

Sets the minimum gas price for transactions to be included.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "miner_setGasPrice", "params": [price]}` |

### Parameters

- `price`: Gas price in wei (hex)

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"miner_setGasPrice","params":["0x4a817c800"]}
{"jsonrpc":"2.0","id":1,"result":true}
```

## `miner_setGasLimit`

Sets the target gas limit for new blocks.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "miner_setGasLimit", "params": [limit]}` |

### Parameters

- `limit`: Gas limit (hex)

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"miner_setGasLimit","params":["0x1c9c380"]}
{"jsonrpc":"2.0","id":1,"result":true}
```

## `miner_start`

Starts the sealing process.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "miner_start", "params": []}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"miner_start","params":[]}
{"jsonrpc":"2.0","id":1,"result":null}
```

## `miner_stop`

Stops the sealing process.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "miner_stop", "params": []}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"miner_stop","params":[]}
{"jsonrpc":"2.0","id":1,"result":null}
```

