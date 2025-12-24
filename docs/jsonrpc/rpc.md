# `rpc` Namespace

The `rpc` API provides methods to get information about the RPC server itself, such as the enabled namespaces.

## `rpc_modules`

Lists the enabled RPC namespaces and the versions of each.

| Client | Method invocation                         |
|--------|-------------------------------------------|
| RPC    | `{"method": "rpc_modules", "params": []}` |

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"rpc_modules","params":[]}
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "eth": "1.0",
        "net": "1.0",
        "web3": "1.0",
        "txpool": "1.0",
        "debug": "1.0",
        "trace": "1.0",
        "admin": "1.0",
        "rpc": "1.0",
        "n42": "1.0",
        "ots": "1.0"
    }
}
```

## Handling Responses During Syncing

When interacting with the RPC server while it is still syncing, some RPC requests may return an empty or null response, while others return the expected results. This behavior can be observed due to the asynchronous nature of the syncing process and the availability of required data. Notably, endpoints that rely on specific stages of the syncing process, such as the execution stage, might not be available until those stages are complete.

It's important to understand that during pipeline sync, some endpoints may not be accessible until the necessary data is fully synchronized. For instance, the `eth_getBlockReceipts` endpoint is only expected to return valid data after the execution stage, where receipts are generated, has completed. As a result, certain RPC requests may return empty or null responses until the respective stages are finished.

This behavior is intrinsic to how the syncing mechanism works and is not indicative of an issue or bug. If you encounter such responses while the node is still syncing, it's recommended to wait until the sync process is complete to ensure accurate and expected RPC responses.

## N42 Consensus (Clique PoA)

N42 uses the Clique Proof of Authority (PoA) consensus algorithm, which is particularly suitable for enterprise and consortium blockchains.

### Key Features

- **Validators**: Pre-selected and trusted entities authorized to seal blocks
- **Security**: Based on validator reputation and transparent actions
- **Efficiency**: Higher throughput compared to PoW, with predictable block times
- **Governance**: Validators can vote to add/remove other validators

### Consensus Parameters

| Parameter | Value | Description |
|-----------|-------|-------------|
| Block Time | 15 seconds | Target time between blocks |
| Epoch Length | 30000 | Blocks between checkpoint |
| Extra Data Vanity | 32 bytes | Custom validator data |
| Extra Data Seal | 65 bytes | Validator signature |

### Validator Responsibilities

1. **Block Production**: Validators take turns sealing blocks in a round-robin fashion
2. **In-turn vs Out-of-turn**: In-turn validators have priority; out-of-turn validators add delay
3. **Voting**: Validators can propose and vote on adding/removing validators
4. **Attestation**: With N42's beacon extension, validators also attest to block validity

### Use Cases

- Enterprise blockchain applications
- Consortium networks
- Supply chain management
- Test networks requiring mainnet-like behavior with higher efficiency
