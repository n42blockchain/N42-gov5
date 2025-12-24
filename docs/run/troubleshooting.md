# Troubleshooting

As N42 is still in development, while running the node you can experience some problems related to different parts of the system: pipeline sync, blockchain tree, p2p, database, etc.

This page tries to answer how to deal with the most popular issues.

## Database

### Database write error

If you encounter an irrecoverable database-related errors, in most of the cases it's related to the RAM/NVMe/SSD you use. For example:
```console
Error: A stage encountered an irrecoverable error.

Caused by:
   0: An internal database error occurred: Database write error code: -30796
   1: Database write error code: -30796
```

or

```console
Error: A stage encountered an irrecoverable error.

Caused by:
   0: An internal database error occurred: Database read error code: -30797
   1: Database read error code: -30797
```

1. Check your memory health: use [memtest86+](https://www.memtest.org/) or [memtester](https://linux.die.net/man/8/memtester). If your memory is faulty, it's better to resync the node on different hardware.
2. Check database integrity:

```bash
git clone https://github.com/n42blockchain/N42
cd N42
go build -o mdbx-check ./tools/mdbx-check
./mdbx-check $(n42 db path)/mdbx.dat | tee mdbx_chk.log
```
If the check has detected any errors, please open an issue and post the output from the mdbx_chk.log file.

## Network Issues

### Unable to connect to peers

If your node cannot connect to peers:

1. Check that ports 30303 (TCP and UDP) are open in your firewall
2. Verify your internet connection is stable
3. Try adding bootnodes manually using `--bootnodes`

### Sync stuck

If sync appears to be stuck:

1. Check `n42 db stats` to see database growth
2. Monitor logs for any error messages
3. Try restarting the node
4. If persistent, consider re-syncing from a snapshot

## Performance Issues

### High CPU usage

1. Check if you're running in debug mode (use release builds)
2. Reduce the number of RPC connections
3. Consider disabling unused RPC namespaces

### High memory usage

1. Check your cache settings
2. Consider running with lower memory limits
3. Ensure you're using a recent version with memory optimizations

## Getting Help

If you're still experiencing issues:

1. Search existing [GitHub Issues](https://github.com/n42blockchain/N42/issues)
2. Join our [Telegram](https://t.me/N42_official) community
3. Open a new issue with detailed logs and system information
