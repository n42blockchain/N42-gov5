# `consensusBeaconExt` Namespace

The `consensusBeaconExt` API provides N42-specific consensus extension methods for interacting with the beacon chain consensus layer.

> **Note**
> 
> This namespace contains sensitive methods for validators and should **not** be exposed publicly.

## `consensusBeaconExt_subscribeToVerificationRequest`

Subscribe to block verification requests. This is used by validators to receive blocks that need to be verified.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "consensusBeaconExt_subscribeToVerificationRequest", "params": [pubkey]}` |

### Parameters

- `pubkey`: The validator's BLS public key (hex-encoded)

### Returns

A subscription that emits `UnverifiedBlock` objects when new blocks need verification.

### Example

```js
// Subscribe to verification requests
// > {"jsonrpc":"2.0","id":1,"method":"consensusBeaconExt_subscribeToVerificationRequest","params":["0x..."]}
{"jsonrpc":"2.0","id":1,"result":"0x1234..."}  // subscription ID

// Received event
{
    "jsonrpc": "2.0",
    "method": "consensusBeaconExt_subscribeToVerificationRequest",
    "params": {
        "subscription": "0x1234...",
        "result": {
            "blockbody": { ... },
            "committee_index": 0,
            "db": { ... }
        }
    }
}
```

## `consensusBeaconExt_submitVerification`

Submit a block verification result with the validator's signature.

| Client | Method invocation |
|--------|-------------------|
| RPC    | `{"method": "consensusBeaconExt_submitVerification", "params": [pubkey, signature, attestation_data, block_hash]}` |

### Parameters

- `pubkey`: The validator's BLS public key (hex-encoded)
- `signature`: The BLS signature over the attestation data (hex-encoded)
- `attestation_data`: The attestation data object containing:
  - `slot`: The slot number
  - `committee_index`: The committee index
  - `receipts_root`: The computed receipts root
- `block_hash`: The recovered block hash (hex-encoded)

### Returns

Nothing on success, or an error if verification fails.

### Example

```js
// > {"jsonrpc":"2.0","id":1,"method":"consensusBeaconExt_submitVerification","params":["0xpubkey...","0xsignature...",{"slot":123,"committee_index":0,"receipts_root":"0x..."},"0xblockhash..."]}
{"jsonrpc":"2.0","id":1,"result":null}
```

## Validator Workflow

1. **Connect**: Establish a WebSocket connection to the N42 node
2. **Subscribe**: Call `consensusBeaconExt_subscribeToVerificationRequest` with your validator's public key
3. **Receive**: Wait for `UnverifiedBlock` events
4. **Verify**: Execute the block locally and compute the receipts root
5. **Sign**: Sign the attestation data using your BLS private key
6. **Submit**: Call `consensusBeaconExt_submitVerification` with the signature and data

### Example Client Code (Go)

```go
package main

import (
    "context"
    "log"
    
    "github.com/ethereum/go-ethereum/rpc"
)

func runValidator(wsURL string, privateKey []byte) error {
    client, err := rpc.DialWebsocket(context.Background(), wsURL, "")
    if err != nil {
        return err
    }
    defer client.Close()
    
    pubkey := derivePubKey(privateKey)
    
    // Subscribe to verification requests
    sub, err := client.Subscribe(
        context.Background(),
        "consensusBeaconExt",
        make(chan map[string]interface{}),
        "subscribeToVerificationRequest",
        pubkey,
    )
    if err != nil {
        return err
    }
    
    for {
        select {
        case msg := <-sub.Chan():
            block := msg.(map[string]interface{})
            
            // Verify the block
            receiptsRoot := verifyBlock(block)
            
            // Create attestation data
            attestationData := map[string]interface{}{
                "slot":            block["slot"],
                "committee_index": block["committee_index"],
                "receipts_root":   receiptsRoot,
            }
            
            // Sign the attestation
            sig := sign(privateKey, attestationData)
            
            // Submit verification
            var result interface{}
            err := client.Call(&result, "consensusBeaconExt_submitVerification",
                pubkey, sig, attestationData, block["hash"])
            if err != nil {
                log.Printf("Error submitting verification: %v", err)
            }
        case err := <-sub.Err():
            log.Printf("Subscription error: %v", err)
            return err
        }
    }
}
```

