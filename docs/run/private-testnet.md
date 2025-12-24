# Run N42 in a Private Testnet

For those who need a private testnet to validate functionality or scale with N42.

## Prerequisites

- N42 binary installed
- Genesis file prepared
- Network connectivity between nodes

## Initializing the N42 Database

To create a blockchain node that uses a custom genesis block, first use `n42 init` to import and set the canonical genesis block for the new chain. This requires the path to `genesis.json` to be passed as an argument.

```bash
n42 init --datadir ./data genesis.json
```

## Setting Up Networking

With the node configured and initialized, the next step is to set up a peer-to-peer network. This requires a bootstrap node. The bootstrap node is a normal node that is designated to be the entry point that other nodes use to join the network. Any node can be chosen to be the bootstrap node.

To configure a bootstrap node, the IP address of the machine the bootstrap node will run on must be known. The bootstrap node needs to know its own IP address so that it can broadcast it to other nodes. On a local machine this can be found using tools such as `ifconfig` and on cloud instances such as Amazon EC2, the IP address of the virtual machine can be found in the management console.

**Firewall Requirements**: Allow UDP and TCP traffic on ports 30303.

### Starting the Bootstrap Node

```bash
n42 node --datadir ./node1 --p2p.addr 0.0.0.0:30303
```

This command will print an enode URL. Other nodes will use this URL to connect to the peer-to-peer network.

Example enode:
```
enode://abc123...@192.168.1.100:30303
```

## Running Member Nodes

Before running a member node, it must be initialized with the same genesis file as used for the bootstrap node.

With the bootnode operational and externally reachable, more N42 nodes can be started and connected via the bootstrap node using the `--bootnodes` flag.

### Example: Starting a Second Node

```bash
n42 node \
  --datadir ./node2 \
  --p2p.addr 0.0.0.0:30304 \
  --http.port 8546 \
  --bootnodes enode://abc123...@192.168.1.100:30303
```

## Configuring Validators

For a PoA network, you need to configure validators in your genesis file:

```json
{
  "config": {
    "chainId": 42,
    "clique": {
      "period": 15,
      "epoch": 30000
    }
  },
  "extraData": "0x0000...validator_addresses...signature",
  "alloc": {
    "0x...": { "balance": "1000000000000000000000" }
  }
}
```

## Running a Validator Node

```bash
n42 node \
  --datadir ./validator \
  --validator \
  --validator.key /path/to/key
```

## Network Topology

For a robust private network, consider:

1. **Multiple bootnodes**: Prevent single point of failure
2. **Geographic distribution**: Reduce latency
3. **Backup validators**: Ensure block production continues

## Monitoring

```bash
# Check peer count
curl -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' \
  http://localhost:8545

# Check sync status
curl -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"eth_syncing","params":[],"id":1}' \
  http://localhost:8545
```
