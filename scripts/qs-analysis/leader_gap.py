#!/usr/bin/env python3
"""Where the leader's 400 ms between the QC and the next seal goes.

Round 35zzq took 39% off the vote path (seal -> QC 1146 -> 699 ms) and the
cycle did not move: QC -> next seal went 139 -> 406 ms. The miner's build
logs carry tMs from n42-r81 on, so each full block can be placed on one
timeline:

    QC(v)            leader's "hotstuff: view changed"
    trigger(v+1)     "miner: build triggered (leader view)"
    build end(v+1)   "miner: build phases"      (tMs is the END; start = tMs - total)
    park/hit         "miner: speculative build parked" / "... hit"
    seal(v+1)        "miner: propose phases"

Usage: leader_gap.py <since 'YYYY-MM-DD HH:MM'> [txs-min]
Reads /data/blockchain/qs-node*/log/n42.log.
"""
import glob, json, sys, statistics as st

since = sys.argv[1]
txmin = int(sys.argv[2]) if len(sys.argv) > 2 else 100000

# per node: the event streams we need, each (tMs, payload)
views, triggers, builds, seals, hits, parks = {}, {}, {}, {}, {}, {}


def add(d, node, item):
    d.setdefault(node, []).append(item)


for path in sorted(glob.glob('/data/blockchain/qs-node*/log/n42.log')):
    node = path.split('/')[3]
    with open(path, errors='replace') as f:
        for line in f:
            if '"tMs"' not in line:
                continue
            try:
                d = json.loads(line)
            except Exception:
                continue
            if d.get('time', '')[:16] < since:
                continue
            m, t = d.get('msg'), d.get('tMs')
            if t is None:
                continue
            if m == 'hotstuff: view changed':
                add(views, node, (t, d.get('view')))
            elif m == 'miner: build triggered (leader view)':
                add(triggers, node, (t, d.get('parent')))
            elif m == 'miner: build phases':
                add(builds, node, (t, d.get('total', 0) / 1e6))
            elif m == 'miner: propose phases' and d.get('txs', 0) >= txmin:
                add(seals, node, (t, d.get('n')))
            elif m == 'miner: speculative build hit':
                add(hits, node, (t, d.get('number')))
            elif m == 'miner: speculative build parked':
                add(parks, node, (t, d.get('number')))

def before(stream, t):
    """The last event in stream at or before t."""
    best = None
    for ts, payload in stream:
        if ts <= t and (best is None or ts > best[0]):
            best = (ts, payload)
    return best

rows = []
for node, ss in seals.items():
    for t_seal, n in sorted(ss):
        qc = before(views.get(node, []), t_seal)
        tr = before(triggers.get(node, []), t_seal)
        bd = before(builds.get(node, []), t_seal)
        if not qc or not tr:
            continue
        build_end = bd[0] if bd else None
        build_start = (bd[0] - bd[1]) if bd else None
        rows.append({
            'node': node, 'n': n,
            'qc_to_trigger': tr[0] - qc[0],
            'trigger_to_seal': t_seal - tr[0],
            'qc_to_seal': t_seal - qc[0],
            'build_after_qc': (build_start - qc[0]) if build_start is not None else None,
            'build_end_to_seal': (t_seal - build_end) if build_end is not None else None,
        })

if not rows:
    print('no full-block seals with tMs since', since)
    print('(n42-r81 and later stamp the build logs; older binaries do not)')
    sys.exit(0)

def med(key):
    xs = [r[key] for r in rows if r[key] is not None]
    return st.median(xs) if xs else float('nan')

print(f'{len(rows)} full-block seals since {since} (txs>={txmin}), medians in ms:')
print('  QC -> build triggered      %7.0f' % med('qc_to_trigger'))
print('  build started after the QC %7.0f   (negative = the build was already running)' % med('build_after_qc'))
print('  build ended -> seal        %7.0f' % med('build_end_to_seal'))
print('  QC -> seal                 %7.0f' % med('qc_to_seal'))
print('  speculative hits %d, parks %d' % (
    sum(len(v) for v in hits.values()), sum(len(v) for v in parks.values())))
