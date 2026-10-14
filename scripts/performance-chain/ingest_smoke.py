#!/usr/bin/env python3
"""Submit ordinary strict Ed25519 ingest frames and require successful receipts."""
import argparse
import json
import pathlib
import subprocess
import time
import urllib.request


def run(args):
    def rpc(method, params):
        body = json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params}).encode()
        request = urllib.request.Request(args.rpc, body, {'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=10) as response:
            result = json.load(response)
        if 'error' in result:
            raise RuntimeError(result['error'])
        return result['result']

    if int(rpc('eth_chainId', []), 16) != args.chain_id:
        raise RuntimeError('unexpected chain ID')
    args.output.mkdir(parents=True, exist_ok=True)
    first = int(rpc('eth_blockNumber', []), 16)
    command = [str(args.flood), '--rpc', args.rpc, '--ingest', args.ingest,
               '--chain-id', str(args.chain_id), '--alg', 'ed25519', '--senders', '8',
               '--pertx', '4', '--offset', str(args.offset), '--conc', '2',
               '--rpcbatch', '4', '--window', '1', '--gas', '300000', '--recipients', '8']
    with (args.output / 'ingest-smoke-flood.log').open('w') as output:
        subprocess.run(command, stdout=output, stderr=subprocess.STDOUT, timeout=180, check=True)
    transactions = []
    cursor = first + 1
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline and len(transactions) < 32:
        latest = int(rpc('eth_blockNumber', []), 16)
        while cursor <= latest:
            block = rpc('eth_getBlockByNumber', [hex(cursor), True])
            if not block:
                raise RuntimeError(f'missing block {cursor}')
            transactions.extend(tx for tx in block['transactions'] if int(tx['type'], 16) == 0x50)
            cursor += 1
        if len(transactions) < 32:
            time.sleep(0.5)
    # Run only on an isolated functional-test chain with no other producer.
    if len(transactions) != 32:
        raise RuntimeError(f'expected 32 fresh Ed25519 transactions, saw {len(transactions)}')
    hashes = []
    blocks = set()
    for transaction in transactions:
        receipt = rpc('eth_getTransactionReceipt', [transaction['hash']])
        if not receipt or int(receipt['status'], 16) != 1:
            raise RuntimeError(f'transaction did not execute successfully: {receipt}')
        hashes.append(transaction['hash'])
        blocks.add(receipt['blockHash'])
    result = {'chain_id': args.chain_id, 'transport': 'ordinary binary ingest; no gateway or sender claims',
              'successful_receipts': len(hashes), 'starting_head': first,
              'ending_head': cursor - 1, 'transaction_hashes': hashes, 'blocks': sorted(blocks)}
    (args.output / 'ingest-smoke.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--rpc', required=True)
    parser.add_argument('--ingest', required=True)
    parser.add_argument('--flood', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--chain-id', type=int, default=1144)
    parser.add_argument('--offset', type=int, default=1144300)
    run(parser.parse_args())
