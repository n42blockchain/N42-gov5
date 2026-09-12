#!/usr/bin/env python3
"""Reconstruct per-view timelines from the fleet's JSON logs using the tMs
stamps (n42-r47+): for each proposal the leader sent, when each follower saw
the pushed block arrive, finished importing it, cast its held commit vote,
and when the leader's view changed (the QC). Usage:
  view-timeline.py <since 'YYYY-MM-DD HH:MM'> [txs-min]
Reads /data/blockchain/qs-node*/log/n42.log.
"""
import glob, json, sys, statistics

since = sys.argv[1]
txmin = int(sys.argv[2]) if len(sys.argv) > 2 else 100000

proposals = {}            # number -> (leader, tMs)
arrivals = {}             # number -> {node: tMs}
imports = {}              # number -> {node: (tMs_end, total_ms)}
views = []                # (node, view, tMs, isLeader)
votes = []                # (node, view, tMs)

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
            m = d.get('msg')
            if m == 'miner: propose phases' and d.get('txs', 0) >= txmin:
                proposals[d['n']] = (node, d['tMs'])
            elif m == 'block push: arrived' and d.get('txs', 0) >= txmin:
                arrivals.setdefault(d['number'], {})[node] = d['tMs']
            elif m == 'blockimport phases' and d.get('txs', 0) >= txmin:
                imports.setdefault(d['n'], {})[node] = (d['tMs'], d['total'] / 1e6)
            elif m == 'hotstuff: view changed':
                views.append((node, d['view'], d['tMs'], d.get('isLeader')))
            elif m == 'two-phase vote: casting held commit vote after import':
                votes.append((node, d['view'], d['tMs']))

# leader view changes by node, sorted by time, to find the QC after a proposal
leader_views = {}
for node, view, t, is_leader in views:
    leader_views.setdefault(node, []).append(t)
for node in leader_views:
    leader_views[node].sort()

rows = []
for n in sorted(proposals):
    leader, t0 = proposals[n]
    arr = arrivals.get(n, {})
    imp = imports.get(n, {})
    if not arr or not imp:
        continue
    arr_d = sorted(v - t0 for v in arr.values())
    imp_end = sorted(v[0] - t0 for v in imp.values())
    imp_dur = [v[1] for v in imp.values()]
    # the leader's next view change after the proposal = QC formed (approx)
    nxt = [t for t in leader_views.get(leader, []) if t > t0]
    qc = (nxt[0] - t0) if nxt else None
    rows.append((n, leader, arr_d, imp_end, imp_dur, qc))

if not rows:
    print('no complete views since', since)
    sys.exit(0)

def med(xs): return statistics.median(xs) if xs else float('nan')
print(f"{len(rows)} proposals with tMs since {since} (txs>={txmin}); ms after the proposal left the leader:")
print("  arrival on followers: median-of-medians %.0f, median p-last %.0f" % (
    med([med(r[2]) for r in rows]), med([r[2][-1] for r in rows])))
print("  import end on followers: median %.0f, last follower %.0f  (import duration median %.0f)" % (
    med([med(r[3]) for r in rows]), med([r[3][-1] for r in rows]), med([med(r[4]) for r in rows])))
qcs = [r[5] for r in rows if r[5] is not None]
print("  leader's next view change (QC): median %.0f" % med(qcs))
print("  => network+decode %.0f | import %.0f | import-end -> QC %.0f" % (
    med([med(r[2]) for r in rows]), med([med(r[4]) for r in rows]),
    med([r[5] - r[3][-1] for r in rows if r[5] is not None])))
print("sample (n, leader, arrivals, import ends, qc):")
for r in rows[-3:]:
    print(" ", r[0], r[1], [int(x) for x in r[2]], [int(x) for x in r[3]], r[5])
