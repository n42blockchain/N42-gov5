#!/usr/bin/env python3
"""Check real Ed25519 admission and receipts through a local RPC proxy."""
import argparse
import http.server
import json
import pathlib
import socket
import struct
import subprocess
import threading
import time
import urllib.request


def run(args):
    def rpc(body):
        request = urllib.request.Request(args.rpc, json.dumps(body).encode(),
                                         {'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=20) as response:
            return json.load(response)

    def call(method, params):
        result = rpc({'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params})
        if 'error' in result:
            raise RuntimeError(result['error'])
        return result['result']

    if int(call('eth_chainId', []), 16) != args.chain_id:
        raise RuntimeError('unexpected signing domain')
    lock = threading.Lock()
    hashes = []
    negatives = []
    ingest_negatives = []
    errors = []

    class Proxy(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            try:
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                requests = body if isinstance(body, list) else [body]
                for request in requests:
                    if request.get('method') != 'eth_sendRawTransaction':
                        continue
                    raw = request['params'][0]
                    if not raw.startswith('0x50'):
                        continue
                    with lock:
                        if not negatives:
                            # The final RLP field is the 64-byte Ed25519 signature.
                            # Flip its last scalar byte's low bit, retaining its shape.
                            bad = bytearray.fromhex(raw[2:])
                            bad[-1] ^= 1
                            answer = rpc(dict(request, params=['0x' + bad.hex()]))
                            error = answer.get('error')
                            if not error or not any(word in str(error).lower()
                                                    for word in ('signature', 'signer', 'recover')):
                                raise RuntimeError(f'bad signature was not explicitly rejected: {answer}')
                            if args.ingest:
                                host, port = args.ingest.rsplit(':', 1)
                                with socket.create_connection((host, int(port)), timeout=20) as connection:
                                    connection.sendall(struct.pack('<II', 1, len(bad)) + bad)
                                    reply = b''
                                    while len(reply) < 8:
                                        part = connection.recv(8 - len(reply))
                                        if not part:
                                            raise RuntimeError('ingest closed before its rejection reply')
                                        reply += part
                                accepted, pending = struct.unpack('<II', reply)
                                if accepted:
                                    raise RuntimeError('ingest accepted a tampered Ed25519 signature')
                                ingest_negatives.append({'accepted': accepted, 'pending': pending})
                            negatives.append(error)
                answer = rpc(body)
                responses = answer if isinstance(answer, list) else [answer]
                by_id = {response['id']: response for response in responses}
                with lock:
                    for request in requests:
                        if request.get('method') == 'eth_sendRawTransaction':
                            result = by_id[request['id']]
                            if 'error' in result:
                                errors.append(result['error'])
                            elif request['params'][0].startswith('0x50'):
                                hashes.append(result['result'])
                payload = json.dumps(answer).encode()
                self.send_response(200)
            except Exception as error:
                with lock:
                    errors.append(str(error))
                payload = json.dumps({'error': str(error)}).encode()
                self.send_response(502)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Proxy)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    args.output.mkdir(parents=True, exist_ok=True)
    try:
        command = [str(args.flood), '--rpc', f'http://127.0.0.1:{server.server_port}',
                   '--chain-id', str(args.chain_id), '--alg', 'ed25519',
                   '--senders', '8', '--pertx', '4', '--offset', str(args.offset),
                   '--conc', '2', '--rpcbatch', '4', '--gas', '300000', '--recipients', '8']
        with (args.output / 'transaction-smoke-flood.log').open('w') as output:
            subprocess.run(command, stdout=output, stderr=subprocess.STDOUT,
                           timeout=180, check=True)
        if errors or len(hashes) != 32 or len(negatives) != 1:
            raise RuntimeError(f'admission mismatch: {len(hashes)} valid, negatives={negatives}, errors={errors}')
        receipts = []
        deadline = time.monotonic() + 90
        for tx_hash in hashes:
            while time.monotonic() < deadline:
                receipt = call('eth_getTransactionReceipt', [tx_hash])
                if receipt:
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError(f'transaction not mined: {tx_hash}')
            if int(receipt['status'], 16) != 1:
                raise RuntimeError(f'transaction reverted: {receipt}')
            receipts.append(receipt)
        result = {'chain_id': args.chain_id, 'signature': 'Ed25519 / 0x50',
                  'successful_receipts': len(receipts), 'signature_rejection': negatives[0],
                  'transaction_hashes': hashes, 'blocks': sorted({r['blockHash'] for r in receipts}),
                  'ingest_tampered_signature': ingest_negatives,
                  'scope': 'RPC admission and execution; optional ingest rejection; restart evaluated separately'}
        (args.output / 'transaction-smoke.json').write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result, indent=2))
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--rpc', required=True)
    parser.add_argument('--flood', required=True, type=pathlib.Path)
    parser.add_argument('--output', required=True, type=pathlib.Path)
    parser.add_argument('--chain-id', type=int, default=1144)
    parser.add_argument('--ingest', help='also require binary-ingest rejection before the valid RPC control')
    parser.add_argument('--offset', type=int, default=1144000)
    run(parser.parse_args())
