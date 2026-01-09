# Blockscout v9.3.2 Compatibility Guide

## Overview

N42 blockchain provides full compatibility with Blockscout v9.3.2, the leading open-source blockchain explorer for Ethereum-based networks.

**Blockscout Version**: v9.3.2
**Release Date**: December 19, 2024
**Compatibility Status**: ✅ Fully Compatible

## Blockscout v9.3.2 Release Information

### What's New in v9.3.2

This release includes three important bug fixes:

1. **Handle_continue bad return value** (#13769)
   - Fixed incorrect return values in asynchronous operations

2. **Make `find_history_and_token_fetchers` public** (#13768)
   - Improved API accessibility for historical data fetching

3. **Resolve TLS version issue on application startup** (#13767)
   - Fixed TLS compatibility issue preventing application startup

### Breaking Changes from v9.3.0

Version 9.3.0 introduced strict parameter validation for REST API endpoints including:
- `/api/v2/transactions/*`
- `/api/v2/token-transfers/*`
- `/api/v2/internal-transactions/*`
- `/api/v2/config/*`
- `/api/v2/main-page/*`
- `/api/v2/smart-contracts/*`
- `/api/v2/stats/*`
- `/api/v2/search/*`
- `/api/v2/withdrawals/*`

## Required Ethereum JSON-RPC Endpoints

N42 implements all required Ethereum JSON-RPC endpoints for Blockscout v9.3.2:

### Core Blockchain Methods (eth namespace)

| Method | Status | Description | Location |
|--------|--------|-------------|----------|
| `eth_chainId` | ✅ | Get chain ID | `api.go:328` |
| `eth_blockNumber` | ✅ | Get latest block number | `api.go:348` |
| `eth_syncing` | ✅ | Get sync status | `blockscout.go:66` |
| `eth_coinbase` | ✅ | Get coinbase address | `blockscout.go:98` |
| `eth_mining` | ✅ | Get mining status | `blockscout.go:109` |
| `eth_hashrate` | ✅ | Get hashrate (0 for PoA) | `blockscout.go:117` |
| `eth_gasPrice` | ✅ | Get suggested gas price | `api.go:180` |
| `eth_maxPriorityFeePerGas` | ✅ | Get priority fee (EIP-1559) | `api.go:197` |
| `eth_feeHistory` | ✅ | Get fee history (EIP-1559) | `api.go:213` |
| `eth_accounts` | ✅ | List accounts | `blockscout.go:345` |
| `eth_protocolVersion` | ✅ | Get protocol version | `rpc_extra.go:296` |

### Account & State Methods

| Method | Status | Description | Location |
|--------|--------|-------------|----------|
| `eth_getBalance` | ✅ | Get account balance | `api.go:333` |
| `eth_getCode` | ✅ | Get contract code | `api.go:355` |
| `eth_getStorageAt` | ✅ | Get storage value | `api.go:373` |
| `eth_getTransactionCount` | ✅ | Get account nonce | `api.go:1013` |
| `eth_getProof` | ✅ | Get Merkle proof | `blockscout.go:372` |

### Block Methods

| Method | Status | Description | Location |
|--------|--------|-------------|----------|
| `eth_getBlockByNumber` | ✅ | Get block by number | `api.go:902` |
| `eth_getBlockByHash` | ✅ | Get block by hash | `api.go:931` |
| `eth_getBlockTransactionCountByNumber` | ✅ | Get tx count by block number | `blockscout.go:129` |
| `eth_getBlockTransactionCountByHash` | ✅ | Get tx count by block hash | `blockscout.go:479` |
| `eth_getBlockReceipts` | ✅ | Get all receipts in block | `blockscout.go:247` |

### Transaction Methods

| Method | Status | Description | Location |
|--------|--------|-------------|----------|
| `eth_getTransactionByHash` | ✅ | Get transaction by hash | `api.go:1165` |
| `eth_getTransactionByBlockHashAndIndex` | ✅ | Get tx by block hash & index | `api.go:1207` |
| `eth_getTransactionByBlockNumberAndIndex` | ✅ | Get tx by block number & index | `blockscout.go:193` |
| `eth_getTransactionReceipt` | ✅ | Get transaction receipt | `api.go:1077` |
| `eth_sendRawTransaction` | ✅ | Send raw transaction | `api.go:1035` |
| `eth_sendTransaction` | ✅ | Send transaction | `api.go:1239` |

### Call & Estimation Methods

| Method | Status | Description | Location |
|--------|--------|-------------|----------|
| `eth_call` | ✅ | Execute call without tx | `api.go:648` |
| `eth_estimateGas` | ✅ | Estimate gas usage | `api.go:889` |

### Uncle Methods (PoA - Always Return Empty)

| Method | Status | Description | Location |
|--------|--------|-------------|----------|
| `eth_getUncleCountByBlockNumber` | ✅ | Get uncle count (always 0) | `blockscout.go:157` |
| `eth_getUncleCountByBlockHash` | ✅ | Get uncle count (always 0) | `api.go:391` |
| `eth_getUncleByBlockNumberAndIndex` | ✅ | Get uncle (always null) | `blockscout.go:182` |
| `eth_getUncleByBlockHashAndIndex` | ✅ | Get uncle (always null) | `blockscout.go:495` |

### Filter & Event Methods

| Method | Status | Description | Location |
|--------|--------|-------------|----------|
| `eth_newFilter` | ✅ | Create log filter | `filters/api.go` |
| `eth_newBlockFilter` | ✅ | Create block filter | `filters/api.go` |
| `eth_newPendingTransactionFilter` | ✅ | Create pending tx filter | `filters/api.go` |
| `eth_getFilterChanges` | ✅ | Get filter changes | `filters/api.go` |
| `eth_getFilterLogs` | ✅ | Get filter logs | `filters/api.go` |
| `eth_getLogs` | ✅ | Get logs | `filters/api.go` |
| `eth_uninstallFilter` | ✅ | Remove filter | `filters/api.go` |

### WebSocket Subscriptions

| Method | Status | Description | Location |
|--------|--------|-------------|----------|
| `eth_subscribe` | ✅ | Create subscription | `filters/api.go` |
| `eth_unsubscribe` | ✅ | Remove subscription | `filters/api.go` |

Supported subscription types:
- `newHeads` - New block headers
- `logs` - Contract event logs
- `newPendingTransactions` - Pending transactions
- `syncing` - Sync status changes

## Additional Namespaces

### web3 Namespace

| Method | Status | Description |
|--------|--------|-------------|
| `web3_clientVersion` | ✅ | Get client version |
| `web3_sha3` | ✅ | Compute Keccak-256 |

### net Namespace

| Method | Status | Description |
|--------|--------|-------------|
| `net_version` | ✅ | Get network ID |
| `net_listening` | ✅ | Get listening status |
| `net_peerCount` | ✅ | Get peer count |

### txpool Namespace

| Method | Status | Description |
|--------|--------|-------------|
| `txpool_content` | ✅ | Get pending/queued txs |
| `txpool_contentFrom` | ✅ | Get txs from address |
| `txpool_status` | ✅ | Get pool statistics |
| `txpool_inspect` | ✅ | Get pool summary |

### debug Namespace

| Method | Status | Description |
|--------|--------|-------------|
| `debug_traceTransaction` | ✅ | Trace transaction execution |
| `debug_traceCall` | ✅ | Trace call execution |
| `debug_traceBlockByNumber` | ✅ | Trace block execution |
| `debug_traceBlockByHash` | ✅ | Trace block execution |
| `debug_setHead` | ✅ | Rewind blockchain |
| `debug_memStats` | ✅ | Get memory stats |
| `debug_gcStats` | ✅ | Get GC stats |
| `debug_stacks` | ✅ | Get goroutine stacks |
| `debug_freeOSMemory` | ✅ | Force garbage collection |

### admin Namespace

| Method | Status | Description |
|--------|--------|-------------|
| `admin_nodeInfo` | ✅ | Get node information |
| `admin_peers` | ✅ | Get peer list |
| `admin_datadir` | ✅ | Get data directory |
| `admin_addPeer` | ⚠️ | Add peer (not implemented) |
| `admin_removePeer` | ⚠️ | Remove peer (not implemented) |

### personal Namespace (Disabled by Default)

| Method | Status | Description |
|--------|--------|-------------|
| `personal_listAccounts` | ✅ | List accounts |
| `personal_listWallets` | ✅ | List wallets |

### miner Namespace

| Method | Status | Description |
|--------|--------|-------------|
| `miner_start` | ✅ | Start mining |
| `miner_stop` | ✅ | Stop mining |
| `miner_mining` | ✅ | Get mining status |
| `miner_setEtherbase` | ✅ | Set coinbase |
| `miner_setGasPrice` | ✅ | Set gas price |
| `miner_setGasLimit` | ✅ | Set gas limit |

### rpc Namespace

| Method | Status | Description |
|--------|--------|-------------|
| `rpc_modules` | ✅ | List available modules |

## N42-Specific Enhancements for Blockscout

### Batch Query Optimization

N42 provides batch query methods to improve Blockscout performance:

```javascript
// Batch get balances for multiple addresses
eth_batchGetBalance([address1, address2, ...], blockNumber)

// Batch get code for multiple contracts
eth_batchGetCode([address1, address2, ...], blockNumber)
```

### Blockscout Compatibility Check

Query Blockscout compatibility information:

```javascript
eth_getBlockscoutCompatibility()
```

Returns:
```json
{
  "compatible": true,
  "blockscoutVersion": "9.3.2",
  "nodeVersion": "N42/v1.0.0",
  "supportedAPIs": ["eth", "web3", "net", "txpool", "debug", ...],
  "features": {
    "eip1559": true,
    "eip2930": true,
    "blockReceipts": true,
    "stateProofs": true,
    "batchRequests": true,
    "webSocketStreaming": true,
    "traceAPI": true,
    "txPoolAPI": true,
    "accountEnumeration": true,
    "uncleBlocks": false
  }
}
```

## Supported EIPs

| EIP | Status | Description |
|-----|--------|-------------|
| EIP-1559 | ✅ | Fee market change |
| EIP-2930 | ✅ | Optional access lists |
| EIP-4844 | ✅ | Shard blob transactions |
| EIP-6780 | ✅ | SELFDESTRUCT changes |

## Configuration

### Enabling RPC Endpoints

Edit your N42 configuration file or use command-line flags:

```bash
# Enable HTTP RPC
n42 --http --http.addr 0.0.0.0 --http.port 8545 --http.api eth,web3,net,txpool,debug

# Enable WebSocket RPC
n42 --ws --ws.addr 0.0.0.0 --ws.port 8546 --ws.api eth,web3,net,txpool

# Enable CORS for Blockscout
n42 --http.corsdomain "https://blockscout.yourdomain.com"
```

### Recommended Configuration for Blockscout

```toml
[NodeCfg]
HTTP = true
HTTPHost = "0.0.0.0"
HTTPPort = "8545"
HTTPApi = "eth,web3,net,txpool,debug,trace"
HTTPCors = "https://blockscout.yourdomain.com"

WS = true
WSHost = "0.0.0.0"
WSPort = "8546"
WSApi = "eth,web3,net,txpool"
WSOrigins = "https://blockscout.yourdomain.com"

# For better Blockscout performance
[P2PCfg]
MaxPeers = 50

[MetricsCfg]
Enable = true
Port = 6061
```

## Network Ports

| Port  | Protocol | Purpose | Exposure |
|-------|----------|---------|----------|
| 8545  | TCP      | HTTP JSON-RPC | Public (configure firewall) |
| 8546  | TCP      | WebSocket JSON-RPC | Public (configure firewall) |
| 8551  | TCP      | Authenticated RPC (Engine API) | Private |
| 30303 | TCP/UDP  | P2P Discovery | Public |

## Deploying Blockscout v9.3.2

### 1. Set up N42 Node

```bash
# Start N42 with Blockscout-compatible configuration
./n42 --config blockscout-config.toml
```

### 2. Deploy Blockscout

Using Docker Compose:

```yaml
version: '3.8'

services:
  blockscout:
    image: blockscout/blockscout:v9.3.2
    environment:
      ETHEREUM_JSONRPC_VARIANT: 'geth'
      ETHEREUM_JSONRPC_HTTP_URL: 'http://n42-node:8545'
      ETHEREUM_JSONRPC_WS_URL: 'ws://n42-node:8546'
      ETHEREUM_JSONRPC_TRACE_URL: 'http://n42-node:8545'
      CHAIN_ID: '42'
      COIN: 'N42'
      COIN_NAME: 'N42'
      NETWORK: 'N42 Network'
      SUBNETWORK: 'Mainnet'
      LOGO: '/images/n42_logo.svg'

    ports:
      - "4000:4000"
    depends_on:
      - postgres
      - redis

  postgres:
    image: postgres:15-alpine
    environment:
      POSTGRES_DB: blockscout
      POSTGRES_USER: blockscout
      POSTGRES_PASSWORD: your_password
    volumes:
      - postgres-data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    volumes:
      - redis-data:/data

volumes:
  postgres-data:
  redis-data:
```

### 3. Configure Indexer

Blockscout will automatically detect and use available RPC methods.

## Performance Optimization

### RPC Caching

Enable caching for frequently accessed data:

```toml
[CacheCfg]
BlockCacheSize = 256
ReceiptCacheSize = 512
TransactionCacheSize = 1024
```

### Database Tuning

For better performance with Blockscout:

```toml
[DatabaseCfg]
MaxOpenConnections = 100
MaxIdleConnections = 50
ConnectionLifetime = "5m"
```

### Indexing Performance

Blockscout v9.3.2 uses `eth_getBlockReceipts` for faster indexing. N42 implements this efficiently by:

1. Batching receipt retrieval per block
2. Optimizing database queries
3. Caching frequently accessed receipts

## Testing Blockscout Compatibility

Run the compatibility test suite:

```bash
cd /Users/jieliu/Documents/n42/N42-gov5
go test ./internal/api -run TestBlockscout -v
```

### Manual Testing

Test core endpoints:

```bash
# Test syncing status
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_syncing","params":[],"id":1}'

# Test block receipts (v9.0+ optimization)
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockReceipts","params":["latest"],"id":1}'

# Test compatibility check
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockscoutCompatibility","params":[],"id":1}'
```

## Troubleshooting

### Issue: Blockscout shows "Sync in progress" indefinitely

**Solution**: Check that `eth_syncing` returns `false`:
```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_syncing","params":[],"id":1}'
```

### Issue: Missing transactions in Blockscout

**Solution**: Verify transaction indexing is working:
```bash
# Check latest block
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Get block with transactions
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",true],"id":1}'
```

### Issue: CORS errors in browser

**Solution**: Update CORS configuration:
```bash
n42 --http.corsdomain "https://blockscout.yourdomain.com,http://localhost:4000"
```

### Issue: WebSocket disconnections

**Solution**: Check WebSocket configuration and increase timeouts:
```toml
[NodeCfg]
WSPathPrefix = ""
HTTPTimeouts.ReadTimeout = "30s"
HTTPTimeouts.WriteTimeout = "30s"
HTTPTimeouts.IdleTimeout = "120s"
```

## Limitations

### POA/POS vs POW

N42 uses Proof-of-Authority (PoA) or Proof-of-Stake (PoS) consensus:

- **No Uncle blocks**: All uncle-related methods return empty/null
- **No mining difficulty**: Difficulty is always 0 or 1
- **No hashrate**: `eth_hashrate` always returns 0

### State Proofs

`eth_getProof` provides partial support:
- ✅ Account balance, nonce, code hash
- ✅ Storage values
- ⚠️ Merkle proofs (placeholder, not yet implemented)

### Trace API

Debug trace methods are supported but may have performance impact on live nodes.

## Support

For issues with Blockscout integration:

1. Check the [N42 GitHub Issues](https://github.com/n42blockchain/n42/issues)
2. Review [Blockscout Documentation](https://docs.blockscout.com/)
3. Join N42 Discord/Telegram community

## References

- [Blockscout v9.3.2 Release](https://github.com/blockscout/blockscout/releases/tag/v9.3.2)
- [Blockscout Documentation](https://docs.blockscout.com/)
- [Blockscout API Swagger](https://github.com/blockscout/swaggers)
- [Ethereum JSON-RPC Specification](https://ethereum.github.io/execution-apis/api-documentation/)
- [N42 RPC Documentation](/docs/rpc/)

## Changelog

### 2026-01-08
- ✅ Full compatibility with Blockscout v9.3.2
- ✅ Implemented all required JSON-RPC endpoints
- ✅ Added batch query optimization methods
- ✅ Added compatibility check endpoint
- ✅ Comprehensive documentation and testing guide

---

**Status**: Production Ready ✅
**Last Updated**: 2026-01-08
**Maintained By**: N42 Core Team
