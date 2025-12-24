# `web3` Namespace

The `web3` API provides utility functions for the web3 client.

## `web3_clientVersion`

Get the web3 client version.


| Client | Method invocation                  |
|--------|------------------------------------|
| RPC    | `{"method": "web3_clientVersion"}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"web3_clientVersion","params":[]}
{"jsonrpc":"2.0","id":1,"result":"n42/v1.0.0/x86_64-unknown-linux-gnu"}
```

## `web3_sha3`

Get the Keccak-256 hash of the given data.

| Client | Method invocation                            |
|--------|----------------------------------------------|
| RPC    | `{"method": "web3_sha3", "params": [bytes]}` |

### Parameters

- `bytes`: The data to hash (hex-encoded with `0x` prefix)

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"web3_sha3","params":["0x68656c6c6f"]}
{"jsonrpc":"2.0","id":1,"result":"0x1c8aff950685c2ed4bc3174f3472287b56d9517b9c948127319a09a7a36deac8"}
```

### Using with Foundry Cast

```bash
# Hash a string
cast rpc web3_sha3 "0x$(echo -n 'hello' | xxd -p)"

# Result: 0x1c8aff950685c2ed4bc3174f3472287b56d9517b9c948127319a09a7a36deac8
```
