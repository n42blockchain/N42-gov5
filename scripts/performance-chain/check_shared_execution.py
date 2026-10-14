#!/usr/bin/env python3
"""Require seven matching commit streams and runtime ImportOnce evidence."""
import argparse
import json
import pathlib
import re


def check(root):
    commits = []
    for node in range(7):
        views = {}
        for view, block in re.findall(r'COMMIT view=(\d+) block=(0x[0-9a-fA-F]{64})',
                                      (root / f'node{node}' / 'v.log').read_text()):
            if view in views and views[view] != block:
                raise ValueError(f'node{node} committed conflicting blocks at view {view}')
            views[view] = block
        if not views:
            raise ValueError(f'node{node} has no commits')
        commits.append(views)
    shared = set.intersection(*(set(views) for views in commits))
    if len(shared) < 5:
        raise ValueError(f'only {len(shared)} views are common to all seven validators')
    union = set.union(*(set(views) for views in commits))
    for view in union:
        if len({views[view] for views in commits if view in views}) != 1:
            raise ValueError(f'validators disagree at view {view}')
    text = re.sub(r'\x1b\[[0-9;]*m', '', (root / 'node0' / 'el.log').read_text())
    counters = {}
    for field in ('once_imports', 'once_blocks', 'once_takeovers',
                  'once_served_total', 'own_from_build', 'own_executed_again'):
        values = [int(value) for value in re.findall(rf'\b{field}=(\d+)', text)]
        if not values:
            raise ValueError(f'no runtime counter for {field}')
        counters[field] = values[-1]
    if counters['once_served_total'] == 0:
        raise ValueError('no request was served through ImportOnce')
    if counters['once_imports'] != counters['once_blocks'] or counters['once_takeovers']:
        raise ValueError(f'normal-run imports were not one per hash: {counters}')
    if counters['own_executed_again']:
        raise ValueError('a locally built block was executed again')
    return {'validators': 7, 'execution_layers': 1, 'common_commit_views': len(shared),
            'conflicting_views': 0, 'counters': counters,
            'scope': 'current log segment; process-local counters reset on restart'}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    args = parser.parse_args()
    result = check(args.root)
    args.output.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))
