# Run a Node

Congratulations, now that you have installed N42, it's time to run it!

In this chapter we'll go through a few different topics you'll encounter when running N42, including:

1. [Running on mainnet or official testnets](./mainnet.md)
2. [Logs and Observability](./observability.md)
3. [Transaction types](./transactions.md)
4. [Ports](./ports.md)
5. [Troubleshooting](./troubleshooting.md)

## Quick Start

Start N42 with default settings:

```bash
n42 node
```

Start with HTTP RPC enabled:

```bash
n42 node --http
```

Start with all standard APIs:

```bash
n42 node --http --http.api eth,net,web3,txpool,debug,trace
```

## Common Options

| Option | Description |
|--------|-------------|
| `--chain` | Specify the network (mainnet, testnet, devnet) |
| `--datadir` | Custom data directory |
| `--http` | Enable HTTP RPC server |
| `--ws` | Enable WebSocket RPC server |
| `--metrics` | Enable Prometheus metrics |
| `--log.level` | Set log verbosity (trace, debug, info, warn, error) |

## Next Steps

- Configure [JSON-RPC endpoints](../jsonrpc/intro.md)
- Set up [monitoring and observability](./observability.md)
- Learn about [running validators](./private-testnet.md)
