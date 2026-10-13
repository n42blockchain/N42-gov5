#!/usr/bin/env python3
"""Prepare an isolated, strict-signature performance-chain profile; start nothing."""
import argparse
import hashlib
import json
import pathlib
import shlex
import subprocess

TOPOLOGIES = {
    'e1': [0, 0, 0, 0, 0, 0, 0],
    'e4': [0, 0, 1, 1, 2, 2, 3],
    'e7': [0, 1, 2, 3, 4, 5, 6],
}

def write_once(path, content):
    if path.exists() and path.read_text() != content:
        raise ValueError(f'refusing to replace a different profile: {path}')
    path.write_text(content)

def prepare(args):
    repo = args.rs_repo.resolve()
    template = repo / 'crates/chainspec/res/genesis/n42_fleet7_bench_d2.json'
    genesis = json.loads(template.read_text())
    config = genesis['config']
    if len(config['hotstuff']['validators']) != 7:
        raise ValueError('expected the seven-validator template')
    if args.chain_id <= 0 or args.chain_id == config['chainId']:
        raise ValueError('performance chain ID must be positive and distinct from the template')
    config.update(chainId=args.chain_id, altSigTx=True, frameBlocks=False,
                  deferredExecutionTime=0, deferredExecutionDepth=2, stateScheme='qmdb')
    config['hotstuff']['minProposeDelayMs'] = 125
    extra = bytes.fromhex(genesis['extraData'][2:])
    if len(extra) < 32:
        raise ValueError('template has no genesis vanity field')
    # Changing both the chain ID and header vanity separates the signing domain
    # and genesis/fork identity; config changes alone do not change a header hash.
    vanity = (b'\xd2N42-PERFORMANCE-ED25519-V1').ljust(32, b'\0')
    genesis['extraData'] = '0x' + (vanity + extra[32:]).hex()
    genesis_text = json.dumps(genesis, indent=2) + '\n'
    root = args.output.resolve() / args.topology
    root.mkdir(parents=True, exist_ok=True)
    genesis_path = root / 'genesis.json'
    mapping = TOPOLOGIES[args.topology]
    base = {'e1': 24100, 'e4': 25100, 'e7': 26100}[args.topology]
    env = {
        'N42_RS_REPO': str(repo), 'F7_ROOT': str(root / 'data'),
        'F7_GENESIS': str(genesis_path), 'F7_BIN': str(repo / 'target/release'),
        'F7_VALIDATORS': '7', 'F7_NODES': '7',
        'F7_EL_MAP': ','.join(map(str, mapping)), 'F7_PROFILE': 'bench',
        'F7_PIN': '0', 'F7_MOBILE': '0', 'F7_INGEST': '1',
        'F7_FLOOD_ALG': 'ed25519', 'N42_ED25519_BATCH': '256',
        'N42_INGEST_VERIFY': 'all', 'N42_FRAME_BLOCKS': '0',
        'N42_IMPORT_ONCE': '1', 'N42_BUILD_ON_SEAL': '1',
        'N42_BUILD_CHAIN': '1', 'N42_FOLLOWER_PARALLEL': '1',
        'F7_BLOCK_INTERVAL_MS': '125', 'F7_BENCH_GASCEIL': '4200000000',
        'F7_BENCH_POOL_SLOTS': '600000',
        'F7_AUTH_BASE': str(base), 'F7_HTTP_BASE': str(base + 100),
        'F7_INGEST_BASE': str(base + 200), 'F7_PAYLOAD_BASE': str(base + 300),
        'F7_DEVP2P_BASE': str(base + 400), 'F7_P2P_BASE': str(base + 500),
        'F7_MOBILE_BASE': str(base + 600), 'F7_METRICS_BASE': str(base + 700),
    }
    manifest = {
        'track': 'performance-chain', 'stage': 'configuration prototype; not a fleet result',
        'chain_id': args.chain_id, 'topology': args.topology, 'validators': 7,
        'execution_layers': max(mapping) + 1, 'validator_to_execution_layer': mapping,
        'transaction_signature': 'Ed25519 / 0x50 / strict batch verification',
        'consensus_signature': 'existing HotStuff BLS',
        'gateway_signature_bypass': False, 'frame_blocks': False,
        'shared_execution_fault_domain': max(mapping) + 1,
        'genesis_sha256': hashlib.sha256(genesis_text.encode()).hexdigest(),
        'rs_source_commit': subprocess.check_output(['git', '-C', str(repo), 'rev-parse', 'HEAD'], text=True).strip(),
        'rs_binary_provenance': 'existing binaries; rebuild and verify before measurement',
        'environment': env,
        'reuse_status': 'interfaces and vectors to be verified; full gov5 module integration pending',
    }
    cleanup = 'unset N42_FRAME_GATEWAYS N42_FRAME_ATTEST_MIN N42_INGEST_SHARD F7_FLOOD_REPLAY F7_FLOOD_GATEWAY_KEY F7_FLOOD_CLAIM\n'
    env_text = '# Generated performance-chain profile: starts no process.\n' + cleanup
    env_text += ''.join(f'export {name}={shlex.quote(value)}\n' for name, value in env.items())
    write_once(genesis_path, genesis_text)
    write_once(root / 'profile.env', env_text)
    write_once(root / 'manifest.json', json.dumps(manifest, indent=2) + '\n')
    print(root / 'manifest.json')

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--rs-repo', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, default=pathlib.Path('/data/blockchain/n42-performance-chain'))
    parser.add_argument('--topology', choices=TOPOLOGIES, default='e1')
    parser.add_argument('--chain-id', type=int, default=1144, help='local experimental ID, not a published network ID')
    prepare(parser.parse_args())
