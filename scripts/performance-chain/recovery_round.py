#!/usr/bin/env python3
"""Run a retained-data SIGKILL recovery check on an isolated performance profile.

No data reset, no host-wide process matching, and no secret-bearing command lines
are collected. A failed phase stops the fleet and leaves all evidence in place.
"""
import argparse
import base64
import hashlib
import hmac
import json
import os
import pathlib
import signal
import subprocess
import time
import urllib.request


def run(args):
    manifest = json.loads(args.profile.read_text())
    env = os.environ.copy()
    for key in ('N42_FRAME_GATEWAYS', 'N42_FRAME_ATTEST_MIN', 'N42_INGEST_SHARD',
                'F7_FLOOD_REPLAY', 'F7_FLOOD_GATEWAY_KEY', 'F7_FLOOD_CLAIM',
                'N42_VOTE_BEFORE_IMPORT', 'N42_TAKE_COMPACT', 'N42_COMPACT_BODY'):
        env.pop(key, None)
    env.update(manifest['environment'])
    env.update(F7_BIN=str(args.bundle.resolve()), N42_RS_REPO=str(args.rs_repo.resolve()),
               RAYON_NUM_THREADS='8')
    root = pathlib.Path(env['F7_ROOT'])
    args.output.mkdir(parents=True, exist_ok=False)
    rpc_url = f"http://127.0.0.1:{env['F7_HTTP_BASE']}"
    auth_url = f"http://127.0.0.1:{env['F7_AUTH_BASE']}"
    fleet = args.rs_repo / 'scripts/fleet7.sh'
    scripts = pathlib.Path(__file__).resolve().parent

    def write(name, result):
        (args.output / name).write_text(json.dumps(result, indent=2) + '\n')

    def rpc(method, params=(), authenticated=False, layer=0):
        headers = {'Content-Type': 'application/json'}
        if authenticated:
            def b64(value):
                return base64.urlsafe_b64encode(value).rstrip(b'=')
            message = b64(b'{"alg":"HS256","typ":"JWT"}') + b'.' + b64(
                json.dumps({'iat': int(time.time())}).encode())
            secret = bytes.fromhex((root / 'jwt.hex').read_text().strip().removeprefix('0x'))
            token = message + b'.' + b64(hmac.new(secret, message, hashlib.sha256).digest())
            headers['Authorization'] = 'Bearer ' + token.decode()
        url = f"http://127.0.0.1:{int(env['F7_AUTH_BASE'] if authenticated else env['F7_HTTP_BASE']) + layer}"
        request = urllib.request.Request(url,
            json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': list(params)}).encode(), headers)
        with urllib.request.urlopen(request, timeout=10) as response:
            answer = json.load(response)
        if 'error' in answer:
            raise RuntimeError(answer['error'])
        return answer['result']

    expected_samples = []
    if args.expected_receipts:
        expected_samples = json.loads(args.expected_receipts.read_text())['receipt_samples']

    def check_expected_receipts():
        for layer in range(manifest['execution_layers']):
            for sample in expected_samples:
                receipt = rpc('eth_getTransactionReceipt', [sample['transaction_hash']], layer=layer)
                if not receipt or receipt['blockHash'] != sample['block_hash'] or int(receipt['status'], 16) != 1:
                    raise RuntimeError(f'EL{layer} changed a retained benchmark receipt')

    def head():
        return int(rpc('eth_blockNumber'), 16)

    def wait_above(height, seconds=150):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                value = head()
                if value > height:
                    return value
            except (OSError, RuntimeError):
                pass
            time.sleep(0.5)
        raise RuntimeError(f'head did not advance beyond {height}')

    def command(command, name, timeout=240):
        with (args.output / name).open('w') as output:
            subprocess.run(command, env=env, stdout=output, stderr=subprocess.STDOUT,
                           timeout=timeout, check=True)

    def saved_replay_targets(evidence_name):
        targets = set()
        evidence = args.output / evidence_name
        evidence.mkdir()
        for validator, layer in enumerate(manifest['validator_to_execution_layer']):
            checkpoint = root / f'node{validator}/consensus/consensus-checkpoint.json'
            if not checkpoint.exists():
                continue
            frozen = checkpoint.read_bytes()
            saved = json.loads(frozen)
            destination = evidence / f'node{validator}'
            destination.mkdir()
            (destination / checkpoint.name).write_bytes(frozen)
            vote_log = checkpoint.with_name('vote-log.bin')
            if vote_log.exists():
                (destination / vote_log.name).write_bytes(vote_log.read_bytes())
            for field in ('last_committed_qc', 'locked_qc'):
                qc = saved[field]
                if qc['view']:
                    targets.add((layer, qc['block_hash']))
        return sorted(targets)

    def wait_for_saved_replay(targets, name):
        deadline = time.monotonic() + args.startup_timeout
        known = {}
        while time.monotonic() < deadline:
            try:
                for layer, block_hash in targets:
                    key = (layer, block_hash)
                    if key not in known:
                        block = rpc('eth_getBlockByHash', [block_hash, False], layer=layer)
                        if block:
                            if block['hash'] != block_hash:
                                raise RuntimeError('saved replay block hash mismatch')
                            known[key] = int(block['number'], 16)
                if len(known) == len(targets):
                    floors = {layer: max(number for (owner, _), number in known.items()
                                        if owner == layer) for layer, _ in targets}
                    if all(int(rpc('eth_blockNumber', layer=layer), 16) >= number
                           for layer, number in floors.items()):
                        write(name, [{'execution_layer': layer, 'hash': block_hash,
                                      'number': known[(layer, block_hash)]}
                                     for layer, block_hash in targets])
                        return
            except OSError:
                pass
            time.sleep(0.5)
        raise RuntimeError('saved certified replay did not finish before smoke traffic')

    def check_other_claims():
        other = [p for folder in ('/data/blockchain', '/data/blockchain/wr-logs')
                 for p in pathlib.Path(folder).glob('.box-claim*')
                 if p.name != '.box-claim-gov5']
        if other:
            raise RuntimeError('another hardware claim is present')
        if not pathlib.Path('/data/blockchain/.box-claim-gov5').exists():
            raise RuntimeError('performance session hardware claim is absent')

    def check_startup_order(name):
        checks = []
        for layer in range(manifest['execution_layers']):
            validator = manifest['validator_to_execution_layer'].index(layer)
            lines = (root / f'node{validator}' / 'el.log').read_text().splitlines()
            ready = next((i for i, line in enumerate(lines) if 'QMDB state ready' in line), None)
            rpc_open = next((i for i, line in enumerate(lines) if 'RPC auth server started' in line), None)
            if ready is None or rpc_open is None or ready >= rpc_open:
                raise RuntimeError(f'EL{layer} exposed Engine RPC before QMDB recovery was ready')
            checks.append({'execution_layer': layer, 'state_ready_line': ready + 1,
                           'auth_rpc_line': rpc_open + 1})
        write(name, checks)
        summary['startup_order_checks'] = summary.get('startup_order_checks', 0) + len(checks)

    def stop_owned_processes():
        mapping = manifest['validator_to_execution_layer']
        files = [root / f'node{mapping.index(i)}/el.pid' for i in range(manifest['execution_layers'])]
        files += [root / f'node{i}/v.pid' for i in range(7)]
        processes = []
        for path in files:
            pid = int(path.read_text())
            executable = pathlib.Path(f'/proc/{pid}/exe').resolve(strict=True)
            if not executable.is_relative_to(args.bundle.resolve()):
                raise RuntimeError('owned fleet PID points at an unexpected executable')
            processes.append(pid)
        for pid in processes:
            os.kill(pid, signal.SIGKILL)

    summary = {'topology': manifest['topology'], 'validators': 7,
               'execution_layers': manifest['execution_layers'], 'test': 'whole-fleet SIGKILL; retained data',
               'power_cut_test': False, 'strict_ed25519': True, 'status': 'running'}
    write('result.json', summary)
    started = False
    try:
        check_other_claims()
        # Refuse to take over a live profile belonging to another launcher.
        for path in root.glob('node*/*.pid'):
            try:
                os.kill(int(path.read_text()), 0)
            except ProcessLookupError:
                continue
            raise RuntimeError('profile already has a live process')
        replay_targets = saved_replay_targets('before-first-start-consensus')
        started = True
        command(['taskset', '-c', '0-15', 'bash', str(fleet), 'up'], 'first-up.log')
        check_startup_order('first-startup-order.json')
        wait_for_saved_replay(replay_targets, 'first-replay-boundary.json')
        required_height = max([120, args.minimum_start_height] +
                              [sample['block_number'] for sample in expected_samples])
        wait_above(required_height, args.startup_timeout)
        check_expected_receipts()
        if int(rpc('eth_chainId'), 16) != manifest['chain_id']:
            raise RuntimeError('wrong chain ID')
        common = ['--rpc', rpc_url, '--flood', str(args.bundle / 'examples/tx_flood'),
                  '--chain-id', str(manifest['chain_id'])]
        command(['python3', str(scripts / 'transaction_smoke.py'), *common,
                 '--ingest', f"127.0.0.1:{env['F7_INGEST_BASE']}", '--offset', str(args.offset),
                 '--output', str(args.output / 'before-rpc')], 'before-rpc.log')
        command(['python3', str(scripts / 'ingest_smoke.py'), *common,
                 '--ingest', f"127.0.0.1:{env['F7_INGEST_BASE']}", '--offset', str(args.offset + 100),
                 '--output', str(args.output / 'before-ingest')], 'before-ingest.log')
        hashes = []
        for phase, file in (('before-rpc', 'transaction-smoke.json'), ('before-ingest', 'ingest-smoke.json')):
            hashes.extend(json.loads((args.output / phase / file).read_text())['transaction_hashes'])
        receipts = {h: rpc('eth_getTransactionReceipt', [h]) for h in hashes}
        baseline = rpc('eth_getBlockByNumber', ['latest', False])
        before = int(baseline['number'], 16)
        persisted = rpc('n42Engine_persistedBlock', authenticated=True)
        if not isinstance(persisted, int) or persisted >= before:
            raise RuntimeError('test did not capture an unpersisted execution tail')
        write('before-forced-stop.json', {'block': baseline, 'persisted': persisted, 'receipts': receipts})
        deadline = time.monotonic() + 90
        for layer in range(manifest['execution_layers']):
            while time.monotonic() < deadline:
                block = rpc('eth_getBlockByNumber', [hex(before), False], layer=layer)
                if block:
                    if block['hash'] != baseline['hash']:
                        raise RuntimeError(f'EL{layer} canonical hash differs')
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError(f'EL{layer} did not catch up before forced termination')
        check_other_claims()
        stop_owned_processes()
        replay_targets = saved_replay_targets('after-kill-consensus')
        command(['taskset', '-c', '0-15', 'bash', str(fleet), 'up'], 'second-up.log')
        check_startup_order('second-startup-order.json')
        wait_for_saved_replay(replay_targets, 'second-replay-boundary.json')
        after = wait_above(before)
        check_expected_receipts()
        for tx_hash, receipt in receipts.items():
            now = rpc('eth_getTransactionReceipt', [tx_hash])
            if not now or now['blockHash'] != receipt['blockHash'] or int(now['status'], 16) != 1:
                raise RuntimeError('previous successful receipt changed after recovery')
        block = rpc('eth_getBlockByNumber', [hex(before), False])
        if block['hash'] != baseline['hash']:
            raise RuntimeError('canonical head before crash was not retained')
        for layer in range(manifest['execution_layers']):
            deadline = time.monotonic() + 90
            while time.monotonic() < deadline:
                block = rpc('eth_getBlockByNumber', [hex(before), False], layer=layer)
                if block:
                    if block['hash'] != baseline['hash']:
                        raise RuntimeError(f'EL{layer} lost the canonical crash head')
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError(f'EL{layer} did not recover the crash head')
        for layer in range(1, manifest['execution_layers']):
            for tx_hash, receipt in receipts.items():
                now = rpc('eth_getTransactionReceipt', [tx_hash], layer=layer)
                if not now or now['blockHash'] != receipt['blockHash'] or int(now['status'], 16) != 1:
                    raise RuntimeError(f'EL{layer} did not preserve a successful receipt')
        command(['python3', str(scripts / 'transaction_smoke.py'), *common,
                 '--ingest', f"127.0.0.1:{env['F7_INGEST_BASE']}", '--offset', str(args.offset + 200),
                 '--output', str(args.output / 'after-rpc')], 'after-rpc.log')
        wait_above(after + 20)
        new_hashes = json.loads((args.output / 'after-rpc' / 'transaction-smoke.json').read_text())['transaction_hashes']
        new_receipts = {tx_hash: rpc('eth_getTransactionReceipt', [tx_hash]) for tx_hash in new_hashes}
        target_height = max(int(receipt['blockNumber'], 16) for receipt in new_receipts.values())
        target_block = rpc('eth_getBlockByNumber', [hex(target_height), False])
        for layer in range(1, manifest['execution_layers']):
            deadline = time.monotonic() + 90
            while time.monotonic() < deadline:
                block = rpc('eth_getBlockByNumber', [hex(target_height), False], layer=layer)
                if block:
                    if block['hash'] != target_block['hash']:
                        raise RuntimeError(f'EL{layer} differs at new receipt height')
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError(f'EL{layer} did not catch up to new transactions')
            for tx_hash, receipt in new_receipts.items():
                now = rpc('eth_getTransactionReceipt', [tx_hash], layer=layer)
                if not now or now['blockHash'] != receipt['blockHash'] or int(now['status'], 16) != 1:
                    raise RuntimeError(f'EL{layer} has no matching new successful receipt')
        if manifest['execution_layers'] == 1:
            command(['python3', str(scripts / 'check_shared_execution.py'), '--root', str(root),
                     '--output', str(args.output / 'shared-execution.json')], 'shared-execution.log')
        summary.update(status='passed', head_before=before, persisted_before=persisted,
                       head_after=after, preserved_successful_receipts=len(receipts),
                       new_successful_receipts=96, canonical_hash_preserved=True,
                       old_receipt_checks_all_layers=len(receipts) * manifest['execution_layers'],
                       new_receipt_checks_all_layers=len(new_hashes) * manifest['execution_layers'],
                       retained_benchmark_receipt_checks=2 * len(expected_samples) * manifest['execution_layers'])
    except BaseException as error:
        summary.update(status='failed', error=str(error))
        raise
    finally:
        if started:
            try:
                command(['bash', str(fleet), 'down'], 'final-down.log')
            except Exception as error:
                summary.update(cleanup_error=str(error), status='failed')
            for node in root.glob('node*'):
                target = args.output / node.name
                target.mkdir(exist_ok=True)
                for log in node.glob('*.log*'):
                    import shutil
                    shutil.copyfile(log, target / log.name)
        write('result.json', summary)
        print(json.dumps(summary))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profile', type=pathlib.Path, required=True, help='generated manifest.json')
    parser.add_argument('--bundle', type=pathlib.Path, required=True)
    parser.add_argument('--rs-repo', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--offset', type=int, required=True)
    parser.add_argument('--expected-receipts', type=pathlib.Path, help='earlier benchmark result to verify before/after crash')
    parser.add_argument('--startup-timeout', type=int, default=150,
                        help='seconds allowed to reach retained receipt heights during initial replay')
    parser.add_argument('--minimum-start-height', type=int, default=120,
                        help='wait beyond this replay boundary before sending test transactions')
    run(parser.parse_args())
