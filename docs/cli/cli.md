# CLI Reference

The N42 node is operated via the CLI by running the `n42` command. To stop it, press `ctrl-c`. You may need to wait a bit as N42 closes existing p2p connections or performs other cleanup tasks.

However, N42 has more commands than that:

```bash
n42 --help
```

See below for the full list of commands.

## Commands

```
$ n42 --help
NAME:
   n42 - N42 Blockchain Node

USAGE:
   n42 [global options] command [command options] [arguments...]

VERSION:
   1.0.0

COMMANDS:
   node     Start the N42 node
   db       Database commands
   p2p      P2P network commands
   debug    Debug utilities
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --chain value             The chain to sync (mainnet, testnet, devnet) (default: "mainnet")
   --datadir value           Data directory for the databases and keystore (default: "~/.n42")
   --help, -h                show help (default: false)
   --version, -v             print the version (default: false)

NODE OPTIONS:
   --http                    Enable the HTTP-RPC server (default: false)
   --http.addr value         HTTP-RPC server listening interface (default: "127.0.0.1")
   --http.port value         HTTP-RPC server listening port (default: 8545)
   --http.api value          APIs offered over the HTTP-RPC interface (default: "eth,net,web3")
   --http.corsdomain value   Comma separated list of domains from which to accept cross origin requests

   --ws                      Enable the WebSocket-RPC server (default: false)
   --ws.addr value           WebSocket-RPC server listening interface (default: "127.0.0.1")
   --ws.port value           WebSocket-RPC server listening port (default: 8546)
   --ws.api value            APIs offered over the WebSocket-RPC interface

   --authrpc.addr value      Listening address for authenticated Engine API
   --authrpc.port value      Listening port for authenticated Engine API (default: 8551)
   --authrpc.jwtsecret value Path to a JWT secret for authenticated RPC endpoints

P2P OPTIONS:
   --p2p.port value          P2P listening port (default: 30303)
   --p2p.max-peers value     Maximum number of peers (default: 50)
   --bootnodes value         Comma separated enode URLs for P2P discovery bootstrap

LOGGING OPTIONS:
   --log.level value         Log level (trace, debug, info, warn, error) (default: "info")
   --log.file value          Log file path
   --log.json                Output logs in JSON format (default: false)

METRICS OPTIONS:
   --metrics                 Enable metrics collection (default: false)
   --metrics.addr value      Metrics HTTP server listening interface (default: "127.0.0.1")
   --metrics.port value      Metrics HTTP server listening port (default: 6060)

DEBUG OPTIONS:
   --pprof                   Enable pprof HTTP server (default: false)
   --pprof.addr value        pprof HTTP server listening interface (default: "127.0.0.1")
   --pprof.port value        pprof HTTP server listening port (default: 6061)
```

## Examples

### Start a full node with HTTP RPC

```bash
n42 node --http --http.api eth,net,web3,txpool
```

### Start with WebSocket support

```bash
n42 node --http --ws --ws.api eth,net,web3
```

### Start with metrics enabled

```bash
n42 node --metrics --metrics.addr 0.0.0.0
```

### Start with custom data directory

```bash
n42 node --datadir /path/to/data
```

### Start on testnet

```bash
n42 node --chain testnet
```
