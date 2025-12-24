# Running N42 on Mainnet or Testnets

## Running the N42 Node

Firstly, ensure that you have installed N42 by following the [installation instructions](../installation/installation.md).

Now, to start the archive node, run:

```bash
n42 node
```

At this point, our N42 node has begun discovery and has started syncing with the network.

## Network Options

### Mainnet

To connect to N42 mainnet (default):

```bash
n42 node --chain mainnet
```

### Testnet

To connect to N42 testnet:

```bash
n42 node --chain testnet
```

### Devnet

To run a local development network:

```bash
n42 node --dev
```

## Node Modes

### Full Node

A full node stores the current state and recent blocks:

```bash
n42 node
```

### Archive Node

An archive node stores the entire history:

```bash
n42 node --archive
```

## Verify the chain is growing

You can easily verify this by inspecting the logs and seeing that headers are arriving. Now sit back and wait for the stages to run! 

In the meantime, consider:
- Setting up [observability](./observability.md) to monitor your node's health
- Testing the [JSON-RPC API](../jsonrpc/intro.md)

## Example Commands

```bash
# Start with HTTP RPC enabled
n42 node --http --http.api eth,net,web3

# Start with WebSocket enabled
n42 node --ws --ws.api eth,net,web3

# Start with metrics enabled
n42 node --metrics --metrics.addr 0.0.0.0

# Start with custom data directory
n42 node --datadir /path/to/data
```
