#!/usr/bin/env python3
"""S22: round 35zzzf (n42-r92, r91 + "miner: seal path" ms-precision stamps
covering trigger->pace->taskQ->seal->check->BLS->resultQ->copy->push->
propose->write, all on ONE line keyed by block "number"). Both S19
(N42_LEADER_WRITE_AFTER_JOURNAL=1) and the S15b gossip-fallback switch are
on in every leg this round -- no A/B; this is a repeatability + resolution
pass on 6cs's own hypothesis (gate = max(write(v)_end, CommitQC(v)) + a
~254 ms constant), testing U1 (the builder's own code reading: resultCh is
unbuffered with ONE consumer, so v+1's sealed result cannot be received
until v's write returns, since handleSealed runs WriteBlockWithState
inline on that same consumer).

Usage: seal_path_waterfall.py <kept-logs-dir> leg_b1_start leg_b1_end
       leg_b2_start leg_b2_end b1win1 b1win2 b2win1 b2win2 full_win_names
"""
import sys, os, json, glob, re, statistics as st, collections

ROOT = sys.argv[1]
LEG_B1 = (sys.argv[2], sys.argv[3])
LEG_B2 = (sys.argv[4], sys.argv[5])
WIN_COUNTS = {'B1': (int(sys.argv[6]), int(sys.argv[7])), 'B2': (int(sys.argv[8]), int(sys.argv[9]))}
FULL_WIN_NAMES = tuple(sys.argv[10].split(','))
FULL_TXS = 150000
TENURE = 4
NVAL = 7


def node_of(p):
    return os.path.basename(p).split('-')[0]


paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
NODES = [node_of(p) for p in paths]

seal = {}                     # (node, number) -> dict of stamps
propose_all = {}               # n -> propose-phases dict (txs, node, time, tMs)
view_changed_leader = {n: [] for n in NODES}
vt_leader_lines = []
vt_follower_lines = []

for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: seal path"' in line:
                d = json.loads(line)
                d['node'] = node
                seal[(node, d['number'])] = d
            elif '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"hotstuff: view changed"' in line and '"isLeader":true' in line:
                d = json.loads(line); view_changed_leader[node].append((d['tMs'], d['view']))
            elif '"msg":"hotstuff view timing' in line:
                d = json.loads(line)
                msg = d['msg']; t = d['time']
                if 'role=leader' in msg:
                    vt_leader_lines.append((node, t, msg))
                elif 'role=follower' in msg:
                    vt_follower_lines.append((node, t, msg))

for n in view_changed_leader:
    view_changed_leader[n].sort()


def ms_field(msg, name):
    m = re.search(re.escape(name) + r'=(-?\d+)ms', msg)
    return int(m.group(1)) if m else None


def view_of(msg):
    m = re.search(r'view=(\d+)', msg)
    return int(m.group(1)) if m else None


def in_leg(n, leg):
    return leg[0] <= propose_all[n]['time'] <= leg[1]


def leg_of_time(t):
    if LEG_B1[0] <= t <= LEG_B1[1]:
        return 'B1'
    if LEG_B2[0] <= t <= LEG_B2[1]:
        return 'B2'
    return None


full_ns = sorted(n for n, d in propose_all.items() if d.get('txs', 0) >= FULL_TXS)
windows = {}
for leg_name, leg in (('B1', LEG_B1), ('B2', LEG_B2)):
    ns_leg = sorted(n for n in full_ns if in_leg(n, leg))
    w1c, w2c = WIN_COUNTS[leg_name]
    windows[leg_name + 'win1'] = ns_leg[:w1c]
    windows[leg_name + 'win2'] = ns_leg[w1c:w1c + w2c]
    print(f'{leg_name}: {len(ns_leg)} full blocks total; win1={len(windows[leg_name+"win1"])} win2={len(windows[leg_name+"win2"])}')
full_windows = sum((windows[w] for w in FULL_WIN_NAMES), [])
print(f'full windows {FULL_WIN_NAMES}: {len(full_windows)} blocks')


def leg_win_of_n(n):
    for w, ns in windows.items():
        if n in ns:
            return w
    return None


chained = []
for n in full_windows:
    if (n - 1) not in propose_all:
        continue
    node_v = propose_all[n - 1]['node']
    node_vp1 = propose_all[n]['node']
    if node_v != node_vp1:
        continue
    sv = seal.get((node_v, n - 1))
    svp1 = seal.get((node_vp1, n))
    if not sv or not svp1:
        continue
    win = leg_win_of_n(n)
    chained.append(dict(n=n, node=node_v, win=win, v=sv, vp1=svp1))
print(f'in-tenure (chained) full blocks with seal-path on both v,v+1: {len(chained)}')

# leg offsets (for r1/r2/jcvMs join via hotstuff view timing, same method as prior sections)
offsets = {'B1': collections.Counter(), 'B2': collections.Counter()}
for row in chained:
    n = row['n']
    node = row['node']
    for tMs, view in view_changed_leader.get(node, []):
        if tMs > row['v']['pushStartTMs']:
            leg = leg_of_time(propose_all[n]['time'])
            if leg:
                offsets[leg][view - n] += 1
            break
leg_offset = {leg: offsets[leg].most_common(1)[0][0] for leg in offsets if offsets[leg]}
print(f'leg offsets: {leg_offset}')


def n_of_view(view, t):
    leg = leg_of_time(t)
    if leg is None or leg not in leg_offset:
        return None
    return view - leg_offset[leg]


vt_leader_by_n = {}
for node, t, msg in vt_leader_lines:
    n = n_of_view(view_of(msg), t)
    if n is not None:
        vt_leader_by_n[(node, n)] = msg

vt_follower_by_n = {}
for node, t, msg in vt_follower_lines:
    n = n_of_view(view_of(msg), t)
    if n is not None:
        vt_follower_by_n.setdefault((node, n), msg)


def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p10 = xs[int(len(xs) * .1)]
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):5d} median={med:8.1f} p10={p10:8.1f} p90={p90:8.1f}')
    return med


# ================= JOB 1a: full waterfall, per window =================
GAP_STEPS = [
    ('trigger->buildBegin', 'triggerTMs', 'buildBeginTMs'),
    ('buildBegin->specParked', 'buildBeginTMs', 'specParkedTMs'),
    ('specParked->specHit', 'specParkedTMs', 'specHitTMs'),
    ('specHit->paceEnter', 'specHitTMs', 'paceEnterTMs'),
    ('paceEnter->taskSent(paceDurMs)', 'paceEnterTMs', 'taskSentTMs'),
    ('taskSent->taskPicked(taskQWaitMs)', 'taskSentTMs', 'taskPickedTMs'),
    ('taskPicked->sealEnter', 'taskPickedTMs', 'sealEnterTMs'),
    ('sealEnter->checkEnter', 'sealEnterTMs', 'checkEnterTMs'),
    ('checkEnter->checkExit', 'checkEnterTMs', 'checkExitTMs'),
    ('checkExit->blsStart', 'checkExitTMs', 'blsStartTMs'),
    ('blsStart->blsEnd', 'blsStartTMs', 'blsEndTMs'),
    ('blsEnd->resultRecv(resQWaitMs)', 'blsEndTMs', 'resultRecvTMs'),
    ('resultRecv->pushStart', 'resultRecvTMs', 'pushStartTMs'),
    ('pushStart->pushEnd', 'pushStartTMs', 'pushEndTMs'),
    ('pushEnd->proposeStart', 'pushEndTMs', 'proposeStartTMs'),
    ('proposeStart->proposeEnd', 'proposeStartTMs', 'proposeEndTMs'),
    ('proposeEnd->copyStart', 'proposeEndTMs', 'copyStartTMs'),
    ('copyStart->copyEnd', 'copyStartTMs', 'copyEndTMs'),
    ('copyEnd->writeStart(lwWaitMs)', 'copyEndTMs', 'writeStartTMs'),
    ('writeStart->writeEnd', 'writeStartTMs', 'writeEndTMs'),
]

print()
print('=== JOB 1a: waterfall, v+1 only (offsets from v+1s own trigger), by window ===')
for win in ('B1win1', 'B1win2', 'B2win1', 'B2win2'):
    rows = [r for r in chained if r['win'] == win]
    print(f'-- {win} (n={len(rows)}) --')
    for label, a, b in GAP_STEPS:
        xs = []
        for r in rows:
            va, vb = r['vp1'].get(a), r['vp1'].get(b)
            if va is not None and vb is not None and va > 0 and vb > 0:
                xs.append(vb - va)
        pstats(f'  {label}', xs)
    # cross-block: cycle push(v)->push(v+1) approximated as pushStart(v+1)-pushStart(v)? use pushEndTMs consistently
    cyc = [r['vp1']['pushEndTMs'] - r['v']['pushEndTMs'] for r in rows
           if r['vp1'].get('pushEndTMs') and r['v'].get('pushEndTMs')]
    pstats('  CYCLE pushEnd(v)->pushEnd(v+1)', cyc)

# ================= JOB 1b: U1 test =================
print()
print('=== JOB 1b: test U1 -- resultRecvTMs(v+1) vs writeEndTMs(v) ===')
for win in ('B1win1', 'B1win2', 'B2win1', 'B2win2'):
    rows = [r for r in chained if r['win'] == win]
    diffs = []
    within10 = 0
    resq_pred = []
    resq_actual = []
    for r in rows:
        we_v = r['v'].get('writeEndTMs')
        rr_vp1 = r['vp1'].get('resultRecvTMs')
        bls_vp1 = r['vp1'].get('blsEndTMs')
        resq_vp1 = r['vp1'].get('resQWaitMs')
        if we_v and rr_vp1:
            d = rr_vp1 - we_v
            diffs.append(d)
            if abs(d) <= 10:
                within10 += 1
        if we_v and bls_vp1 and resq_vp1 is not None:
            resq_pred.append(we_v - bls_vp1)
            resq_actual.append(resq_vp1)
    print(f'-- {win} (n={len(rows)}) --')
    pstats('  resultRecv(v+1) - writeEnd(v)', diffs)
    if diffs:
        print(f'  share within 10ms: {100*within10/len(diffs):.1f}%')
    pstats('  resQWaitMs(v+1) actual', resq_actual)
    pstats('  predicted resQWaitMs = writeEnd(v) - blsEnd(v+1)', resq_pred)
    # taskQWaitMs too
    pstats('  taskQWaitMs(v+1)', [r['vp1'].get('taskQWaitMs') for r in rows])
    pstats('  paceDurMs(v+1) [full blocks, expect ~0]', [r['vp1'].get('paceDurMs') for r in rows])

# ================= JOB 1c: the ~254ms constant, decomposed =================
print()
print('=== JOB 1c: decompose push(v+1)-gate constant into named steps ===')
CONST_STEPS = [
    ('trigger->specHit', 'triggerTMs', 'specHitTMs'),
    ('pace (paceDurMs)', 'paceEnterTMs', 'taskSentTMs'),
    ('taskQ (taskQWaitMs)', 'taskSentTMs', 'taskPickedTMs'),
    ('sealEnter->checkEnter', 'sealEnterTMs', 'checkEnterTMs'),
    ('check (checkEnter->checkExit)', 'checkEnterTMs', 'checkExitTMs'),
    ('BLS (blsStart->blsEnd)', 'blsStartTMs', 'blsEndTMs'),
    ('resQ (blsEnd->resultRecv)', 'blsEndTMs', 'resultRecvTMs'),
    ('copy (copyStart->copyEnd)', 'copyStartTMs', 'copyEndTMs'),
    ('push (pushStart->pushEnd)', 'pushStartTMs', 'pushEndTMs'),
]
for win in ('B1win1', 'B2win1'):
    rows = [r for r in chained if r['win'] == win]
    print(f'-- {win} (n={len(rows)}) --')
    total_named = 0
    step_medians = []
    for label, a, b in CONST_STEPS:
        xs = [r['vp1'][b] - r['vp1'][a] for r in rows
              if r['vp1'].get(a) and r['vp1'].get(b) and r['vp1'][a] > 0 and r['vp1'][b] > 0]
        med = pstats(f'  {label}', xs)
        if med is not None:
            step_medians.append((label, med))
    named_sum = sum(m for _, m in step_medians)
    print(f'  sum of named-step medians: {named_sum:.1f} ms')

# ================= JOB 1d: repeatability =================
print()
print('=== JOB 1d: repeatability (prediction 88a) ===')
for win in ('B1win1', 'B1win2', 'B2win1', 'B2win2'):
    rows = [r for r in chained if r['win'] == win]
    print(f'-- {win} (n={len(rows)}) --')
    leader_rows = []
    for r in rows:
        n = r['n']; node = r['node']
        lm = vt_leader_by_n.get((node, n - 1))
        if lm:
            leader_rows.append(dict(
                r1=ms_field(lm, 'r1'), r2=ms_field(lm, 'r2'),
                jcvMs=ms_field(lm, 'jcvMs'), jpvMs=ms_field(lm, 'jpvMs')))
    pstats('  r1', [x['r1'] for x in leader_rows])
    pstats('  r2', [x['r2'] for x in leader_rows])
    pstats('  jcvMs', [x['jcvMs'] for x in leader_rows])
    pstats('  jpvMs', [x['jpvMs'] for x in leader_rows])
    why_counts = collections.Counter(r['vp1'].get('lwWhy') for r in rows)
    tot = sum(why_counts.values()) or 1
    print(f'  lwWhy shares: ' + ', '.join(f'{k}={100*v/tot:.1f}%({v})' for k, v in why_counts.most_common()))
