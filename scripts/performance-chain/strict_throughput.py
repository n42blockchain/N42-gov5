#!/usr/bin/env python3
"""Fresh V7/E1 strict-signature throughput leg, with post-window receipt checks."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import urllib.request


def run(args):
    manifest = json.loads(args.profile.read_text())
    if manifest['execution_layers'] != 1 or manifest['validators'] != 7:
        raise RuntimeError('this comparison requires V7/E1')
    claim = Path('/data/blockchain/.box-claim-gov5/owner.json')
    if not claim.exists() or json.loads(claim.read_text()).get('worktree') != str(args.rs_repo.resolve()):
        raise RuntimeError('requires this worktree to own the hardware claim')
    foreign = [p for directory in ('/data/blockchain', '/data/blockchain/wr-logs')
               for p in Path(directory).glob('.box-claim*') if p.name != '.box-claim-gov5']
    if foreign:
        raise RuntimeError(f'other hardware claims exist: {foreign}')
    if args.root.exists():
        raise RuntimeError('refusing to reuse an existing throughput data directory')
    args.output.mkdir(parents=True, exist_ok=False)
    env = {key: os.environ[key] for key in ('PATH', 'HOME', 'LANG', 'TMPDIR') if key in os.environ}
    env.update(manifest['environment'])
    env.update(F7_ROOT=str(args.root.resolve()), F7_BIN=str(args.bundle.resolve()),
               N42_RS_REPO=str(args.rs_repo.resolve()), N42_INGEST_VERIFY='all',
               N42_FRAME_BLOCKS='0', F7_PIN='1', F7_FLEET_CPUS='144', F7_EL_CPUS='128',
               F7_VAL_CPUS='16', RAYON_NUM_THREADS='64', F7_DROP_CACHE='0', F7_HUGEPREP='0',
               F7_MEASURE_FROM_LOG='1', F7_MEASURE_FULL_TXS='10000', F7_FLOOD_WINDOW='64',
               F7_TX_GAS='21000')
    for key in ('N42_FRAME_GATEWAYS', 'N42_FRAME_ATTEST_MIN', 'N42_INGEST_SHARD',
                'F7_FLOOD_REPLAY', 'F7_FLOOD_GATEWAY_KEY', 'F7_FLOOD_CLAIM',
                'N42_VOTE_BEFORE_IMPORT', 'N42_TAKE_COMPACT', 'N42_COMPACT_BODY',
                'N42_FAST_TRANSFER', 'N42_PARALLEL_BUILD', 'N42_BUILDER_PULLER',
                'N42_OUTPUT_SHARDS', 'N42_BUILD_AHEAD', 'N42_RECOVERY_PHASES',
                'N42_BODY_ONCE', 'N42_TX_QUEUE', 'N42_TX_INGEST_DIRECT',
                'N42_TX_QUEUE_RUN', 'N42_TX_ROOT_THREADS'):
        env.pop(key, None)
    if args.mode in ('parallel', 'output-shards', 'ahead', 'journal-timing', 'body-once', 'sender-queue', 'direct-queue', 'direct-runs', 'direct-raw', 'root-pool'):
        env.update(N42_FAST_TRANSFER='1', N42_PARALLEL_BUILD='1', N42_BUILDER_PULLER='4096')
    if args.mode == 'output-shards':
        env.update(N42_OUTPUT_SHARDS='16')
    if args.mode == 'ahead':
        env.update(N42_BUILD_AHEAD='1')
    if args.mode == 'journal-timing':
        env.update(N42_RECOVERY_PHASES='1')
    if args.mode in ('body-once', 'direct-raw', 'root-pool'):
        env.update(N42_BODY_ONCE='1')
    if args.mode in ('sender-queue', 'direct-queue', 'direct-runs', 'direct-raw', 'root-pool'):
        env.update(N42_TX_QUEUE='1')
    if args.mode in ('direct-queue', 'direct-runs', 'direct-raw', 'root-pool'):
        env.update(N42_TX_INGEST_DIRECT='1')
    if args.mode in ('direct-runs', 'direct-raw', 'root-pool'):
        env.update(N42_TX_QUEUE_RUN='64')
    if args.mode == 'root-pool':
        env.update(N42_TX_ROOT_THREADS='16')
    report = dict(status='running', mode=args.mode, strict_ed25519=True, gateway_bypass=False,
                  root=str(args.root), offset=args.offset, receipt_samples=[], windows=[])
    report['binaries'] = {rel: hashlib.sha256((args.bundle / rel).read_bytes()).hexdigest()
                          for rel in ('n42', 'examples/h2_validator', 'examples/tx_flood')}
    bundle_manifest = json.loads((args.bundle.parent/'run-manifest.json').read_text())
    for rel, actual in report['binaries'].items():
        if bundle_manifest['binaries'].get(rel) != actual:
            raise RuntimeError(f'binary hash differs from bundle provenance: {rel}')
    genesis_hash = hashlib.sha256(Path(env['F7_GENESIS']).read_bytes()).hexdigest()
    if genesis_hash != manifest['genesis_sha256']:
        raise RuntimeError('genesis differs from prepared profile')
    report.update(source_commit=bundle_manifest['source_commit'], genesis_sha256=genesis_hash,
                  transaction_signature=manifest['transaction_signature'], durable_pre_vote_bodies=True)
    (args.output / 'run-manifest.json').write_text(json.dumps(dict(report, environment=env), indent=2)+'\n')
    benchlog = args.output / 'bench.log'
    scripts = args.rs_repo / 'scripts'
    def rpc(method, params):
        request = urllib.request.Request(f"http://127.0.0.1:{env['F7_HTTP_BASE']}",
            json.dumps(dict(jsonrpc='2.0', id=1, method=method, params=params)).encode(),
            {'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=30) as response:
            answer = json.load(response)
        if 'error' in answer:
            raise RuntimeError(str(answer['error']))
        return answer['result']
    try:
        with benchlog.open('w') as stream:
            subprocess.run(['bash', str(scripts/'fleet7-bench.sh'), '--tag', args.mode,
                '--senders', '6000', '--pertx', '30000', '--offset', str(args.offset),
                '--conc', '64', '--rpcbatch', '200', '--recipients', '2000000',
                '--windows', '3', '--window-sec', '15', '--decay-sec', '20',
                '--gasceil', '4200000000', '--gasprice', '1000000000000000000000000'],
                env=env, stdout=stream, stderr=subprocess.STDOUT, check=True, timeout=900)
        text = benchlog.read_text()
        if args.mode == 'root-pool':
            el_text = re.sub(r'\x1b\[[0-9;]*m', '', (args.root/'node0/el.log').read_text())
            if not any('dedicated transaction root worker pool enabled' in line and 'threads=16' in line
                       for line in el_text.splitlines()):
                raise RuntimeError('requested transaction root pool was not observed at runtime')
            report['root_pool_threads'] = 16
        match = re.search(r'first full block at epoch ([\d.]+)', text)
        if not match:
            raise RuntimeError('missing measurement start')
        start = float(match[1])
        sys.dont_write_bytecode = True
        spec = importlib.util.spec_from_file_location('measure', scripts/'fleet7-measure.py')
        measure = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(measure)
        rows = measure.log_blocks(str(args.root/'node0/el.log'))
        for index in range(3):
            window = [r for r in rows if start+15*index <= r[0] < start+15*(index+1)]
            if not window or sum(r[2] for r in window) == 0:
                raise RuntimeError('empty throughput window')
            report['windows'].append(dict(window=index+1, txs=sum(r[2] for r in window),
                tps=sum(r[2] for r in window)/15, blocks=len(window), cycle_s=15/len(window),
                occupancy_pct=sum(r[3] for r in window)/len(window)))
            # Low occupancy is a performance result, not a harness exception.
            # Sample a nonempty measured block even when none reaches 10k txs.
            chosen = max(window, key=lambda row: row[2])
            block = rpc('eth_getBlockByNumber', [hex(chosen[1]), False])
            if len(block['transactions']) != chosen[2]:
                raise RuntimeError('canonical transaction count disagrees with log')
            txs = block['transactions']
            for index in sorted({sample*(len(txs)-1)//15 for sample in range(16)}):
                tx_hash = txs[index]
                receipt = rpc('eth_getTransactionReceipt', [tx_hash])
                if not receipt or int(receipt['status'], 16) != 1 or receipt['blockHash'] != block['hash']:
                    raise RuntimeError('measured transaction receipt failed')
                report['receipt_samples'].append(dict(transaction_hash=tx_hash, block_hash=block['hash'],
                    block_number=chosen[1], status=1))
        if args.mode in ('direct-queue', 'direct-runs', 'direct-raw', 'root-pool'):
            with (args.output/'signature-smoke.log').open('w') as stream:
                subprocess.run(['python3', str(Path(__file__).parent/'transaction_smoke.py'),
                    '--rpc', f"http://127.0.0.1:{env['F7_HTTP_BASE']}",
                    '--ingest', f"127.0.0.1:{env['F7_INGEST_BASE']}",
                    '--flood', str(args.bundle/'examples/tx_flood'),
                    '--chain-id', str(manifest['chain_id']), '--offset', str(args.offset+7000),
                    '--output', str(args.output/'signature-smoke')],
                    stdout=stream, stderr=subprocess.STDOUT, check=True, timeout=240)
            report['signature_smoke'] = 'passed: RPC and binary ingest rejection; new execution receipts'
        with (args.output/'consensus-check.log').open('w') as stream:
            subprocess.run(['python3', str(Path(__file__).parent/'check_shared_execution.py'),
                '--root', str(args.root), '--output', str(args.output/'consensus.json')],
                stdout=stream, stderr=subprocess.STDOUT, check=True, timeout=60)
        report['status'] = 'passed'
    except BaseException as error:
        report.update(status='failed', error=str(error))
        raise
    finally:
        text = benchlog.read_text() if benchlog.exists() else ''
        for pid in re.findall(r'flood\s+: pid (\d+)', text):
            try:
                if Path(f'/proc/{pid}/exe').resolve(strict=True) == (args.bundle/'examples/tx_flood').resolve():
                    os.kill(int(pid), signal.SIGTERM)
            except ProcessLookupError:
                pass
            except FileNotFoundError:
                pass
        try:
            with (args.output/'down.log').open('w') as stream:
                subprocess.run(['bash', str(scripts/'fleet7.sh'), 'down'], env=env,
                               stdout=stream, stderr=subprocess.STDOUT, timeout=60, check=True)
        except Exception as error:
            report.update(status='failed', cleanup_error=str(error))
            raise
        finally:
            (args.output/'result.json').write_text(json.dumps(report, indent=2)+'\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('profile', 'bundle', 'rs-repo', 'root', 'output'):
        parser.add_argument('--'+name, type=Path, required=True)
    parser.add_argument('--mode', choices=('serial', 'parallel', 'output-shards', 'ahead', 'journal-timing', 'body-once', 'sender-queue', 'direct-queue', 'direct-runs', 'direct-raw', 'root-pool'), required=True)
    parser.add_argument('--offset', type=int, required=True)
    run(parser.parse_args())
