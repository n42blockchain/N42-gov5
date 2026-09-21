#!/usr/bin/env python3
"""S25 Job 4: retroactive safety check -- per committed height, is there ever
more than one distinct hash among the fleet's "block committed!"/"hotstuff:
block committed" lines? These lines carry (hash, view) but NOT height; height
is joined in via a hash-prefix -> height table built from every OTHER line
in the same round that carries both a hash and a block number ("block push:
received", "🔨 Successfully sealed new block", "add future block").

Hash normalization: different call sites truncate hashes differently
("f47f65…13d8ac" vs "0xf47f6514cc") but both always start with the SAME six
hex characters of the real hash (after any "0x"), so the join key used
throughout is simply those first 6 hex chars -- collision risk is
negligible at this round's scale (a few thousand-to-tens-of-thousands of
committed heights against 16.7M possible 6-hex-char prefixes).

Usage: height_conflict_check.py <kept-logs-dir> [<kept-logs-dir> ...]
"""
import sys, os, json, glob, re, collections


def node_of(p):
    return os.path.basename(p).split('-')[0]


def hexprefix(s):
    s = s.strip()
    if s.startswith('0x'):
        s = s[2:]
    # truncated forms use unicode ellipsis; take everything before it
    s = s.split('…')[0].split('...')[0]
    s = re.sub(r'[^0-9a-fA-F]', '', s)
    return s[:6].lower() if len(s) >= 6 else None


def process_round(root):
    paths = sorted(glob.glob(os.path.join(root, 'node*-B.log')))
    if not paths:
        return None
    hash_to_height = {}   # 6-hex prefix -> height
    committed = []        # (node, time, view, hash_prefix)
    for path in paths:
        node = node_of(path)
        with open(path, errors='replace') as f:
            for line in f:
                if '"msg":"block push: received"' in line:
                    try:
                        d = json.loads(line)
                    except Exception:
                        continue
                    hp = hexprefix(str(d.get('hash', '')))
                    n = d.get('number')
                    if hp and n:
                        hash_to_height.setdefault(hp, n)
                elif '"msg":"🔨 Successfully sealed new block"' in line or 'Successfully sealed new block' in line:
                    try:
                        d = json.loads(line)
                    except Exception:
                        continue
                    hp = hexprefix(str(d.get('hash', '')))
                    n = d.get('number')
                    if hp and n:
                        hash_to_height.setdefault(hp, n)
                elif '"msg":"add future block"' in line:
                    try:
                        d = json.loads(line)
                    except Exception:
                        continue
                    hp = hexprefix(str(d.get('hash', '')))
                    n = d.get('number')
                    if hp and n:
                        hash_to_height.setdefault(hp, n)
                elif '"msg":"hotstuff: block committed"' in line or '"msg":"block committed!"' in line:
                    try:
                        d = json.loads(line)
                    except Exception:
                        continue
                    hp = hexprefix(str(d.get('hash', d.get('blockHash', ''))))
                    view = d.get('view')
                    t = d.get('time')
                    if hp:
                        committed.append((node, t, view, hp))

    # group committed events by resolved height
    by_height = collections.defaultdict(set)          # height -> set of hash prefixes
    by_height_detail = collections.defaultdict(list)  # height -> [(node, time, view, hp)]
    unresolved = 0
    for node, t, view, hp in committed:
        h = hash_to_height.get(hp)
        if h is None:
            unresolved += 1
            continue
        by_height[h].add(hp)
        by_height_detail[h].append((node, t, view, hp))

    conflicts = {h: hps for h, hps in by_height.items() if len(hps) > 1}
    return dict(
        round=os.path.basename(root).replace('-keep', ''),
        heights_checked=len(by_height),
        committed_events=len(committed),
        unresolved=unresolved,
        conflicts=conflicts,
        detail={h: by_height_detail[h] for h in conflicts},
    )


def main():
    total_heights = 0
    all_conflicts = []
    for root in sys.argv[1:]:
        res = process_round(root)
        if res is None:
            print(f'{root}: no node*-B.log files found, skipped')
            continue
        total_heights += res['heights_checked']
        print(f"{res['round']}: heights_checked={res['heights_checked']} "
              f"committed_events={res['committed_events']} unresolved={res['unresolved']} "
              f"conflicts={len(res['conflicts'])}")
        for h, hps in sorted(res['conflicts'].items()):
            print(f"  CONFLICT height={h} hashes={hps}")
            for node, t, view, hp in sorted(res['detail'][h]):
                print(f"    {node} {t} view={view} hash_prefix={hp}")
            all_conflicts.append((res['round'], h, hps))
    print()
    print(f'TOTAL heights checked across all rounds: {total_heights}')
    print(f'TOTAL conflicting heights found: {len(all_conflicts)}')
    for r, h, hps in all_conflicts:
        print(f'  {r}: height {h}, hashes {hps}')


if __name__ == '__main__':
    main()
