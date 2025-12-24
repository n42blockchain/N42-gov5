# `ots` Namespace (Otterscan)

The `ots` (Otterscan) API provides methods compatible with the [Otterscan](https://github.com/otterscan/otterscan) block explorer.

## `ots_getApiLevel`

Returns the API level supported by this implementation.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_getApiLevel", "params": []}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"ots_getApiLevel","params":[]}
{"jsonrpc":"2.0","id":1,"result":8}
```

## `ots_getInternalOperations`

Returns internal operations (calls, creates, suicides) for a transaction.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_getInternalOperations", "params": [tx_hash]}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"ots_getInternalOperations","params":["0x..."]}
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": [
        {
            "type": "CALL",
            "from": "0x...",
            "to": "0x...",
            "value": "0x0"
        }
    ]
}
```

## `ots_hasCode`

Checks if an address contains code at a given block.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_hasCode", "params": [address, block]}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"ots_hasCode","params":["0x...", "latest"]}
{"jsonrpc":"2.0","id":1,"result":true}
```

## `ots_traceTransaction`

Returns a simplified trace for a transaction.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_traceTransaction", "params": [tx_hash]}` |

## `ots_getTransactionError`

Returns the error message for a failed transaction.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_getTransactionError", "params": [tx_hash]}` |

## `ots_getBlockDetails`

Returns detailed block information including issuance and fees.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_getBlockDetails", "params": [block]}` |

## `ots_getBlockDetailsByHash`

Returns detailed block information by hash.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_getBlockDetailsByHash", "params": [block_hash]}` |

## `ots_getBlockTransactions`

Returns paginated transactions for a block.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_getBlockTransactions", "params": [block, page, page_size]}` |

## `ots_searchTransactionsBefore`

Searches for transactions before a given block for an address.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_searchTransactionsBefore", "params": [address, block, page_size]}` |

## `ots_searchTransactionsAfter`

Searches for transactions after a given block for an address.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_searchTransactionsAfter", "params": [address, block, page_size]}` |

## `ots_getTransactionBySenderAndNonce`

Returns a transaction by sender address and nonce.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_getTransactionBySenderAndNonce", "params": [address, nonce]}` |

## `ots_getContractCreator`

Returns the creator of a contract.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "ots_getContractCreator", "params": [address]}` |

