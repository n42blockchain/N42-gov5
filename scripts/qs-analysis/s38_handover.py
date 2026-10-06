#!/usr/bin/env python3
"""S38-spec: hand-over vs in-tenure cycle share/timing on round 35zzzn
(n42-r96, ms-precision "miner: seal path" stamps). See
docs/QS_BLOCK_TIME_BUDGET.md 6dr for the prose.

Usage: s38_handover.py <dir with node*-B.log> (gz+live concatenated,
node names must sort as node0..node6)
"""
import sys, os, json, glob, statistics as st

ROOT = sys.argv[1]
FULL_TXS = 150000
LEG_B1 = ('2026-09-22 17:31:54', '2026-09-22 17:44:56')
LEG_B2 = ('2026-09-22 17:44:56', '2026-09-22 17:58:38')
WIN_COUNTS = {'B1': (54, 36), 'B2': (51, 46)}

def node_of(p): return os.path.basename(p).split('-')[0]

propose_all = {}   # n -> dict (ALL sizes)
seal_path = {}      # n -> dict (ALL sizes, keyed 'number')
blockimport = {}    # (node,n) -> dict
pblock = {}         # (node,n) -> dict
push_recv = {}       # (node,number) -> tMs
push_arrived = {}    # (node,number) -> tMs

paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"miner: seal path"' in line:
                d = json.loads(line); d['node'] = node; seal_path[d['number']] = d
            elif '"msg":"blockimport phases"' in line:
                d = json.loads(line); blockimport[(node, d['n'])] = d
            elif '"msg":"parallel block"' in line:
                d = json.loads(line); pblock[(node, d['n'])] = d
            elif '"msg":"block push: received"' in line:
                d = json.loads(line); push_recv[(node, d['number'])] = d['tMs']
            elif '"msg":"block push: arrived"' in line:
                d = json.loads(line); push_arrived[(node, d['number'])] = d['tMs']

def in_leg(n, leg):
    d = propose_all.get(n)
    return d is not None and leg[0] <= d['time'] <= leg[1]

full_ns = sorted(n for n, d in propose_all.items() if d.get('txs', 0) >= FULL_TXS)
windows = {}
for leg_name, leg in (('B1', LEG_B1), ('B2', LEG_B2)):
    ns_leg = sorted(n for n in full_ns if in_leg(n, leg))
    w1c, w2c = WIN_COUNTS[leg_name]
    windows[leg_name + 'win1'] = ns_leg[:w1c]
    windows[leg_name + 'win2'] = ns_leg[w1c:w1c + w2c]
    print(f'{leg_name}: total full in leg={len(ns_leg)} win1={len(windows[leg_name+"win1"])} win2={len(windows[leg_name+"win2"])}', file=sys.stderr)

def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p10 = xs[int(len(xs)*.1)]
    p90 = xs[min(len(xs)-1, int(len(xs)*.9))]
    print(f'{name}: n={len(xs):4d} median={med:9.1f} p10={p10:9.1f} p90={p90:9.1f}')
    return med

for winname in ('win1', 'win2'):
    ns = windows['B1'+winname] + windows['B2'+winname]
    chained, handover = [], []
    for n in ns:
        if n not in seal_path or (n-1) not in seal_path:
            continue
        leader = propose_all[n]['node']
        prev_leader = propose_all.get(n-1, {}).get('node')
        cycle = seal_path[n]['pushEndTMs'] - seal_path[n-1]['pushEndTMs']
        if cycle <= 0 or cycle > 6000:
            continue
        row = dict(n=n, leader=leader, prev_leader=prev_leader, cycle=cycle)
        if prev_leader == leader:
            chained.append(row)
        else:
            handover.append(row)
    total = len(chained) + len(handover)
    print(f'\n=== {winname}: n={total} chained={len(chained)} handover={len(handover)} '
          f'handover_share={100*len(handover)/total:.1f}% ===')
    pstats(f'  {winname} in-tenure cycle', [r['cycle'] for r in chained])
    pstats(f'  {winname} hand-over cycle', [r['cycle'] for r in handover])

# ===== Hand-over leader timeline, pooled across both legs' win1+win2 =====
print('\n=== HAND-OVER LEADER TIMELINE (pooled win1+win2, both legs) ===')
all_handover = []
for winname in ('win1', 'win2'):
    ns = windows['B1'+winname] + windows['B2'+winname]
    for n in ns:
        if n not in seal_path or (n-1) not in seal_path:
            continue
        leader = propose_all[n]['node']
        prev_leader = propose_all.get(n-1, {}).get('node')
        if prev_leader is not None and prev_leader != leader:
            all_handover.append(n)

print(f'hand-over blocks with own seal_path+predecessor seal_path: {len(all_handover)}')

# for each hand-over block v, leader L: L's own import of v-1 (it was a follower)
rows = []
for v in all_handover:
    L = propose_all[v]['node']
    prev_push_end = seal_path[v-1]['pushEndTMs']
    bi = blockimport.get((L, v-1))
    if not bi:
        continue
    t_import_end = bi['tMs']
    t_import_start = t_import_end - bi['total']/1e6
    pr = push_recv.get((L, v-1))
    pa = push_arrived.get((L, v-1))
    pb = pblock.get((L, v-1))
    sp = seal_path[v]
    row = dict(v=v, L=L,
               prev_push_end=prev_push_end,
               arrive=pr,
               arrived=pa,
               import_start=t_import_start,
               hdr=bi['hdr']/1e6, body=bi['body']/1e6, proc=bi['proc']/1e6,
               write=bi['write']/1e6, align=bi.get('align',0)/1e6,
               import_total=bi['total']/1e6,
               import_end=t_import_end,
               exec_ms=(pb or {}).get('execMs'), finalize_ms=(pb or {}).get('finalizeMs'),
               recover_ms=(pb or {}).get('recoverMs'),
               trigger=sp.get('triggerTMs'), buildBegin=sp.get('buildBeginTMs'),
               specParked=sp.get('specParkedTMs'), specHit=sp.get('specHitTMs'),
               sealEnter=sp.get('sealEnterTMs'), checkEnter=sp.get('checkEnterTMs'),
               checkExit=sp.get('checkExitTMs'), blsStart=sp.get('blsStartTMs'),
               blsEnd=sp.get('blsEndTMs'), resultRecv=sp.get('resultRecvTMs'),
               copyStart=sp.get('copyStartTMs'), copyEnd=sp.get('copyEndTMs'),
               writeStart=sp.get('writeStartTMs'), writeEnd=sp.get('writeEndTMs'),
               pushEnd=sp.get('pushEndTMs'))
    rows.append(row)

print(f'rows with own blockimport for the parent: {len(rows)}')

def ok(v):
    return v is not None and v != 0

def seg(name, a, b):
    xs = [r[b]-r[a] for r in rows if ok(r.get(a)) and ok(r.get(b))]
    pstats(f'  {name}', xs)

seg('prev_push_end(v-1) -> arrived(v-1) [pure network delivery]', 'prev_push_end', 'arrived')
seg('arrived(v-1) -> arrive/received(v-1)==import_end [decode+exec+finalize+write, whole import]', 'arrived', 'arrive')
seg('prev_push_end(v-1) -> arrive(v-1) [delivery+whole import, cross-check]', 'prev_push_end', 'arrive')
pstats('  import breakdown: hdr', [r['hdr'] for r in rows])
pstats('  import breakdown: body(decode)', [r['body'] for r in rows])
pstats('  import breakdown: proc(exec+finalize)', [r['proc'] for r in rows])
pstats('  import breakdown: proc.execMs', [r['exec_ms'] for r in rows])
pstats('  import breakdown: proc.finalizeMs', [r['finalize_ms'] for r in rows])
pstats('  import breakdown: proc.recoverMs', [r['recover_ms'] for r in rows])
pstats('  import breakdown: write(v-1, follower own persist)', [r['write'] for r in rows])
seg('import_end(v-1) -> trigger(v)', 'import_end', 'trigger')
seg('trigger(v) -> buildBegin(v)', 'trigger', 'buildBegin')
n_specParked = sum(1 for r in rows if ok(r.get('specParked')))
print(f'  hand-over rows with a real (nonzero) specParkedTMs: {n_specParked}/{len(rows)}')
seg('buildBegin(v) -> sealEnter(v) [WHOLE build: fill+assemble+finalize state-root, no park/hit]', 'buildBegin', 'sealEnter')
seg('sealEnter(v) -> checkEnter(v)', 'sealEnter', 'checkEnter')
seg('checkEnter(v) -> checkExit(v) [CheckSealParentApplied]', 'checkEnter', 'checkExit')
seg('checkExit(v) -> blsStart(v)', 'checkExit', 'blsStart')
seg('blsStart(v) -> blsEnd(v) [BLS sign]', 'blsStart', 'blsEnd')
seg('blsEnd(v) -> resultRecv(v)', 'blsEnd', 'resultRecv')
seg('resultRecv(v) -> copyStart(v)', 'resultRecv', 'copyStart')
seg('copyStart(v) -> copyEnd(v) [receipts copy]', 'copyStart', 'copyEnd')
seg('copyEnd(v) -> pushEnd(v)', 'copyEnd', 'pushEnd')
seg('buildBegin(v) -> pushEnd(v) [whole leader-side build+seal+push]', 'buildBegin', 'pushEnd')
seg('WHOLE hand-over cycle: prev_push_end -> pushEnd(v)', 'prev_push_end', 'pushEnd')
seg('cross-check sum: import_end(v-1) -> pushEnd(v)', 'import_end', 'pushEnd')
