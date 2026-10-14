#!/usr/bin/env python3
"""Deduplicate immutable recovery bodies of stopped V7/E1 benchmark cases.

Retains every pathname and byte; only byte-identical, same-owner regular files
inside a completed case may share an inode. Default is a read-only dry run.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import uuid


def same_bytes(left, right):
    with left.open('rb') as a, right.open('rb') as b:
        while True:
            chunk = a.read(1024 * 1024)
            if chunk != b.read(1024 * 1024):
                return False
            if not chunk:
                return True


def fingerprint(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def stopped(root):
    for file in root.glob('node*/*.pid'):
        pid = int(file.read_text().strip())
        process = Path(f'/proc/{pid}/stat')
        if process.exists() and process.read_text().split(') ')[1][0] != 'Z':
            raise RuntimeError(f'case has a live PID: {pid}')


def run(args):
    claim = Path('/data/blockchain/.box-claim-gov5/owner.json')
    if args.apply:
        if not claim.exists() or json.loads(claim.read_text()).get('worktree') != str(args.rs_repo.resolve()):
            raise RuntimeError('mutation requires this worktree hardware claim')
        foreign = [p for directory in ('/data/blockchain', '/data/blockchain/wr-logs')
                   for p in Path(directory).glob('.box-claim*') if p.name != '.box-claim-gov5']
        if foreign:
            raise RuntimeError(f'another hardware claim exists: {foreign}')
    report = dict(apply=args.apply, cases=[], paths_linked=0, duplicate_allocated_bytes=0)
    try:
        for case in args.case:
            result = json.loads((case/'result.json').read_text())
            manifest = json.loads((case/'run-manifest.json').read_text())
            if result['status'] not in ('passed', 'failed'):
                raise RuntimeError('case has not finished')
            env = manifest['environment']
            if env['F7_EL_MAP'] != '0,0,0,0,0,0,0' or env['N42_RS_REPO'] != str(args.rs_repo.resolve()):
                raise RuntimeError('only this worktree V7/E1 cases are eligible')
            root = Path(result['root']).resolve(strict=True)
            if root != Path(env['F7_ROOT']).resolve() or not root.is_relative_to('/data/blockchain/n42-performance-chain'):
                raise RuntimeError('unexpected case root')
            stopped(root)
            known = {}
            entry = dict(case=str(case), root=str(root), paths_linked=0, duplicate_allocated_bytes=0,
                         mismatches=0, files=[])
            report['cases'].append(entry)
            for node in range(7):
                directory = root/f'node{node}/consensus/execution-recovery'
                for path in sorted(directory.glob('*/*.rlp')):
                    info = path.lstat()
                    if not stat.S_ISREG(info.st_mode) or not re.fullmatch(r'[0-9a-f]{64}\.rlp', path.name):
                        raise RuntimeError('unexpected recovery body file')
                    if not 1024*1024 <= info.st_size <= 128*1024*1024:
                        continue
                    key = (path.parent.name, path.name, info.st_size, info.st_dev,
                           stat.S_IMODE(info.st_mode), info.st_uid, info.st_gid)
                    source = known.setdefault(key, path)
                    if source == path or source.stat().st_ino == info.st_ino or info.st_nlink != 1:
                        continue
                    if not same_bytes(source, path):
                        entry['mismatches'] += 1
                        continue
                    before = (info.st_ino, info.st_size, info.st_mtime_ns)
                    source_info = source.stat()
                    digest = fingerprint(source)
                    if args.apply:
                        stopped(root)
                        current = path.lstat()
                        if (current.st_ino, current.st_size, current.st_mtime_ns) != before:
                            raise RuntimeError('body changed while checking it')
                        temporary = path.with_name(path.name+'.dedup-'+uuid.uuid4().hex)
                        try:
                            os.link(source, temporary, follow_symlinks=False)
                            linked = temporary.stat()
                            if (linked.st_ino, linked.st_size, linked.st_mtime_ns) != (
                                    source_info.st_ino, source_info.st_size, source_info.st_mtime_ns):
                                raise RuntimeError('source changed while linking')
                            os.replace(temporary, path)
                            fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
                            try:
                                os.fsync(fd)
                            finally:
                                os.close(fd)
                            if fingerprint(path) != digest:
                                raise RuntimeError('linked body verification failed')
                        finally:
                            temporary.unlink(missing_ok=True)
                    allocated = info.st_blocks*512
                    entry['paths_linked'] += 1
                    entry['duplicate_allocated_bytes'] += allocated
                    entry['files'].append(dict(path=str(path), source=str(source), sha256=digest,
                                               bytes=info.st_size, old_inode=info.st_ino))
            report['paths_linked'] += entry['paths_linked']
            report['duplicate_allocated_bytes'] += entry['duplicate_allocated_bytes']
            print(json.dumps({k:v for k,v in entry.items() if k!='files'}), flush=True)
    finally:
        args.output.write_text(json.dumps(report, indent=2)+'\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--case', type=Path, action='append', required=True)
    parser.add_argument('--rs-repo', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--apply', action='store_true')
    run(parser.parse_args())
