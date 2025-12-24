# `n42` Namespace

The `n42` API provides N42-specific methods for node information and diagnostics.

## `n42_getClientVersion`

Returns the client version information.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "n42_getClientVersion", "params": []}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"n42_getClientVersion","params":[]}
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "code": "N42",
        "name": "n42",
        "version": "1.0.0",
        "commit": "abc123...",
        "build_timestamp": "2024-01-01T00:00:00Z",
        "capabilities": [
            "engine_v1",
            "engine_v2",
            "engine_v3"
        ]
    }
}
```

## `n42_getBalanceChangesInBlock`

Returns the balance changes for all accounts in the specified block.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "n42_getBalanceChangesInBlock", "params": [block]}` |

### Parameters

- `block`: Block number, hash, or tag (`latest`, `earliest`, `pending`)

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"n42_getBalanceChangesInBlock","params":["0x100"]}
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "0x1234...": {
            "from": "0x0",
            "to": "0x1000"
        },
        "0x5678...": {
            "from": "0x2000",
            "to": "0x1500"
        }
    }
}
```
