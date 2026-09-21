#!/usr/bin/env python3
"""S14: round 35zzza (n42-r87, N42_CONTENTION_DIAG=1). Re-measures the
in-tenure/hand-over cycle anatomy the same way as 6cb/6cd/6ce (reusing
that method: push_instant from "miner: propose phases", QC proxy from
the leader's own "hotstuff: view changed" isLeader:true events, view<->n
offset calibrated per leg), then pulls S14's new contention fields
directly off the SAME "hotstuff view timing" lines used for the QC-proxy
join, for the SAME blocks, so the attribution table is self-consistent
by construction (no separate-population matching needed, unlike 6ce's
Round1/Round2 reconciliation which had to join two independently
filtered samples).

New fields (glossary in docs/QS_BLOCK_TIME_BUDGET.md 6cf), all inline in
the "hotstuff view timing" message text, ms already:
  leader:   r1n r1lw r1lwMax r1wk r1kth r1qk   r2n r2lw r2lwMax r2wk r2kth r2qk
  follower: propLw propWk pqcLw pqcWk pqc2cv cvHeld cvGate

Usage: contention_attribution.py <kept-logs-dir> [leg_b1_start leg_b1_end
       leg_b2_start leg_b2_end b1win1 b1win2 b2win1 b2win2 full_win_names]
Positional overrides let the SAME script drive a different round (e.g.
35zzzb) with its own leg boundaries/window counts/full-window set, with
zero change to the join/computation logic below -- this is what "the
same scripts and bucket definitions" across rounds means in practice.
Defaults below are 35zzza's own parameters (6cg).

  contention_attribution.py /data/blockchain/wr-logs/r35zzza-keep
  contention_attribution.py /data/blockchain/wr-logs/r35zzzb-keep \
      "2026-09-21 05:14:27" "2026-09-21 05:27:30" \
      "2026-09-21 05:27:30" "2026-09-21 05:40:47" \
      53 56 53 58 B1win1,B2win1
"""
import sys, os, json, glob, re, statistics as st, collections

ROOT = sys.argv[1] if len(sys.argv) > 1 else '/data/blockchain/wr-logs/r35zzza-keep'
FULL_TXS = 150000
if len(sys.argv) > 9:
    LEG_B1 = (sys.argv[2], sys.argv[3])
    LEG_B2 = (sys.argv[4], sys.argv[5])
    WIN_COUNTS = {'B1': (int(sys.argv[6]), int(sys.argv[7])),
                  'B2': (int(sys.argv[8]), int(sys.argv[9]))}
    FULL_WIN_NAMES = tuple(sys.argv[10].split(',')) if len(sys.argv) > 10 else ('B1win1', 'B2win1', 'B2win2')
else:
    LEG_B1 = ('2026-09-21 03:13:55', '2026-09-21 03:27:39')
    LEG_B2 = ('2026-09-21 03:27:39', '2026-09-21 03:41:01')
    # 35zzza's full windows differ from 35zzz's: B1win2 (31.7% occupancy)
    # is NOT full; B1win1, B2win1, B2win2 all are (49.0/48.6/47.0%). Counts
    # from r35zzza.log's own win1/win2 lines.
    WIN_COUNTS = {'B1': (50, 67), 'B2': (50, 48)}
    FULL_WIN_NAMES = ('B1win1', 'B2win1', 'B2win2')
print(f'params: LEG_B1={LEG_B1} LEG_B2={LEG_B2} WIN_COUNTS={WIN_COUNTS} FULL_WIN_NAMES={FULL_WIN_NAMES}')

def node_of(path):
    return os.path.basename(path).split('-')[0]

paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
NODES = [node_of(p) for p in paths]

propose_all = {}
view_changed_leader = {n: [] for n in NODES}
vt_leader = []     # (node, time, view, r1total,r2total,total, dict-of-newfields)
vt_follower = []    # (node, time, view, recv,r1total,r2total,total, dict-of-newfields)
blockimport = {}
pblock = {}

LEADER_PAT = re.compile(
    r'view=(\d+) role=leader propose=(\d+)ms r1=(\d+)ms r2=(\d+)ms total=(\d+)ms'
    r'(?: votes=(\d+)/(\d+))?'
    r'(?: r1n=(\d+))?(?: r1lw=(\d+)ms)?(?: r1lwMax=(\d+)ms)?(?: r1wk=(\d+)ms)?'
    r'(?: r1kth=(\d+)ms)?(?: r1qk=(\d+)ms)?'
    r'(?: r2n=(\d+))?(?: r2lw=(\d+)ms)?(?: r2lwMax=(\d+)ms)?(?: r2wk=(\d+)ms)?'
    r'(?: r2kth=(\d+)ms)?(?: r2qk=(\d+)ms)?')
FOLLOWER_PAT = re.compile(
    r'view=(\d+) role=follower recv=(\d+)ms(?: exec=(\d+)ms)? r1=(\d+)ms r2=(\d+)ms total=(\d+)ms'
    r'(?: propLw=(\d+)ms)?(?: propWk=(\d+)ms)?(?: pqcLw=(\d+)ms)?(?: pqcWk=(\d+)ms)?'
    r'(?: pqc2cv=(\d+)ms)?(?: cvHeld=(true|false))?(?: cvGate=(\w+))?')

for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"blockimport phases"' in line:
                d = json.loads(line); blockimport[(node, d['n'])] = d
            elif '"msg":"parallel block"' in line:
                d = json.loads(line); pblock[(node, d['n'])] = d
            elif '"msg":"hotstuff: view changed"' in line and '"isLeader":true' in line:
                d = json.loads(line); view_changed_leader[node].append((d['tMs'], d['view']))
            elif '"msg":"hotstuff view timing' in line:
                i = line.find('"time":"'); t = line[i + 8:i + 27]
                if 'role=leader' in line:
                    m = LEADER_PAT.search(line)
                    if m:
                        g = m.groups()
                        view, propose_, r1t, r2t, total = (int(g[0]), int(g[1]), int(g[2]), int(g[3]), int(g[4]))
                        newf = {k: (int(v) if v is not None else None) for k, v in zip(
                            ['r1n', 'r1lw', 'r1lwMax', 'r1wk', 'r1kth', 'r1qk',
                             'r2n', 'r2lw', 'r2lwMax', 'r2wk', 'r2kth', 'r2qk'], g[7:19])}
                        vt_leader.append((node, t, view, propose_, r1t, r2t, total, newf))
                elif 'role=follower' in line:
                    m = FOLLOWER_PAT.search(line)
                    if m:
                        g = m.groups()
                        view, recv = int(g[0]), int(g[1])
                        r1t, r2t, total = int(g[3]), int(g[4]), int(g[5])
                        newf = dict(propLw=int(g[6]) if g[6] else None, propWk=int(g[7]) if g[7] else None,
                                    pqcLw=int(g[8]) if g[8] else None, pqcWk=int(g[9]) if g[9] else None,
                                    pqc2cv=int(g[10]) if g[10] else None,
                                    cvHeld=(g[11] == 'true') if g[11] else None, cvGate=g[12])
                        vt_follower.append((node, t, view, recv, r1t, r2t, total, newf))

for n in view_changed_leader:
    view_changed_leader[n].sort()

full_ns = sorted(n for n, d in propose_all.items() if d.get('txs', 0) >= FULL_TXS)

def in_leg(n, leg):
    return leg[0] <= propose_all[n]['time'] <= leg[1]

windows = {}
for leg_name, leg in (('B1', LEG_B1), ('B2', LEG_B2)):
    ns_leg = sorted(n for n in full_ns if in_leg(n, leg))
    w1c, w2c = WIN_COUNTS[leg_name]
    windows[leg_name + 'win1'] = ns_leg[:w1c]
    windows[leg_name + 'win2'] = ns_leg[w1c:w1c + w2c]
    print(f'{leg_name}: {len(ns_leg)} full-tx blocks in leg time range; '
          f'win1={len(windows[leg_name+"win1"])}(want {w1c}) win2={len(windows[leg_name+"win2"])}(want {w2c})')

full_windows = sum((windows[w] for w in FULL_WIN_NAMES), [])
print(f'full windows used: {FULL_WIN_NAMES}, combined {len(full_windows)} blocks')

rec = {}
for n, d in propose_all.items():
    tMs = d['tMs']; total = d['total']; write = d['write']
    push_instant = tMs - write / 1e6
    rec[n] = dict(node=d['node'], txs=d['txs'], time=d['time'], push_instant=push_instant)

chained, handover = [], []
for n in full_windows:
    if (n - 1) not in rec:
        continue
    r0, r1 = rec[n - 1], rec[n]
    cycle = r1['push_instant'] - r0['push_instant']
    if cycle <= 0 or cycle > 6000:
        continue
    qc = None
    for tMs, view in view_changed_leader.get(r1['node'], []):
        if tMs > r0['push_instant']:
            qc = (tMs, view)
            break
    row = dict(n=n, leader=r1['node'], prev_leader=r0['node'], cycle=cycle, qc=qc)
    (chained if r1['node'] == r0['node'] else handover).append(row)

print(f'chained(in-tenure)={len(chained)} handover={len(handover)}')

def leg_of_time(t):
    if LEG_B1[0] <= t <= LEG_B1[1]:
        return 'B1'
    if LEG_B2[0] <= t <= LEG_B2[1]:
        return 'B2'
    return None

offsets = {'B1': collections.Counter(), 'B2': collections.Counter()}
for row in chained:
    if row['qc'] is None:
        continue
    tMs, view = row['qc']
    leg = 'B1' if in_leg(row['n'], LEG_B1) else ('B2' if in_leg(row['n'], LEG_B2) else None)
    if leg:
        offsets[leg][view - row['n']] += 1
leg_offset = {leg: offsets[leg].most_common(1)[0][0] for leg in offsets if offsets[leg]}
print(f'leg offsets: {leg_offset} (agreement: '
      f'{ {leg: offsets[leg].most_common(3) for leg in offsets} })')

def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):4d} median={med:8.1f} p90={p90:8.1f}')
    return med, p90

print()
print('=== cycle time (push-instant to push-instant), ms ===')
pstats('all', [r['cycle'] for r in chained + handover])
pstats('in-tenure', [r['cycle'] for r in chained])
pstats('hand-over', [r['cycle'] for r in handover])
print(f'  in-tenure fraction: {len(chained)}/{len(chained)+len(handover)} = '
      f'{100*len(chained)/(len(chained)+len(handover)):.1f}%')

vt_leader_by_n = {}
for node, t, view, propose_, r1t, r2t, total, newf in vt_leader:
    leg = leg_of_time(t)
    if leg is None or leg not in leg_offset:
        continue
    n = view - leg_offset[leg]
    vt_leader_by_n[(node, n)] = (propose_, r1t, r2t, total, newf)

vt_follower_by_n = {}
for node, t, view, recv, r1t, r2t, total, newf in vt_follower:
    leg = leg_of_time(t)
    if leg is None or leg not in leg_offset:
        continue
    n = view - leg_offset[leg]
    vt_follower_by_n.setdefault((node, n), (recv, r1t, r2t, total, newf))

# ---- leader-side attribution, matched to block n-1's own view (the view
# whose CommitQC gates the cycle push(n-1)->push(n)) ----
print()
print('=== LEADER attribution, matched to the SAME view q2s uses (view of block n-1), in-tenure ===')
fields = ['propose', 'r1', 'r2', 'total', 'r1n', 'r1lw', 'r1lwMax', 'r1wk', 'r1kth', 'r1qk',
          'r2n', 'r2lw', 'r2lwMax', 'r2wk', 'r2kth', 'r2qk']
collected = {k: [] for k in fields}
matched = 0
for row in chained:
    got = vt_leader_by_n.get((row['leader'], row['n'] - 1))
    if not got:
        continue
    propose_, r1t, r2t, total, newf = got
    matched += 1
    collected['propose'].append(propose_); collected['r1'].append(r1t)
    collected['r2'].append(r2t); collected['total'].append(total)
    for k in newf:
        collected[k].append(newf[k])
print(f'matched leader rows: {matched} / {len(chained)}')
med = {}
for k in fields:
    r = pstats(f'  {k}', collected[k])
    med[k] = r[0] if r else None

print()
print('  additive check, Round1: r1kth + r1lw + r1qk vs r1 total')
if med.get('r1kth') is not None:
    s = (med.get('r1kth') or 0) + (med.get('r1lw') or 0) + (med.get('r1qk') or 0)
    print(f'    {med.get("r1kth"):.0f} + {med.get("r1lw"):.0f} + {med.get("r1qk"):.0f} = {s:.0f} vs r1={med["r1"]:.0f} ({100*s/med["r1"]:.1f}%)')
print('  additive check, Round2: r2kth + r2lw + r2qk vs r2 total')
if med.get('r2kth') is not None:
    s = (med.get('r2kth') or 0) + (med.get('r2lw') or 0) + (med.get('r2qk') or 0)
    print(f'    {med.get("r2kth"):.0f} + {med.get("r2lw"):.0f} + {med.get("r2qk"):.0f} = {s:.0f} vs r2={med["r2"]:.0f} ({100*s/med["r2"]:.1f}%)')

# ---- follower-side attribution, all 6 followers per in-tenure full block ----
print()
print('=== FOLLOWER attribution, all 6 followers per in-tenure full block, matched to block n-1 ===')
ffields = ['recv', 'r1', 'r2', 'total', 'propLw', 'propWk', 'pqcLw', 'pqcWk', 'pqc2cv']
fcollected = {k: [] for k in ffields}
held_count = 0; held_total = 0
gate_counts = collections.Counter()
fmatched = 0
for row in chained:
    leader = row['leader']
    for F in NODES:
        if F == leader:
            continue
        got = vt_follower_by_n.get((F, row['n'] - 1))
        if not got:
            continue
        recv, r1t, r2t, total, newf = got
        fmatched += 1
        fcollected['recv'].append(recv); fcollected['r1'].append(r1t)
        fcollected['r2'].append(r2t); fcollected['total'].append(total)
        for k in ('propLw', 'propWk', 'pqcLw', 'pqcWk', 'pqc2cv'):
            fcollected[k].append(newf.get(k))
        held_total += 1
        if newf.get('cvHeld'):
            held_count += 1
            if newf.get('cvGate'):
                gate_counts[newf['cvGate']] += 1
print(f'matched follower rows: {fmatched}')
fmed = {}
for k in ffields:
    r = pstats(f'  {k}', fcollected[k])
    fmed[k] = r[0] if r else None
print(f'  cvHeld share: {held_count}/{held_total} = {100*held_count/held_total:.1f}%')
print(f'  cvGate breakdown (of held): {dict(gate_counts)}')

print()
print('=== summary for write-up ===')
print(f'in-tenure cycle median vs 35zzz (799): see above')
