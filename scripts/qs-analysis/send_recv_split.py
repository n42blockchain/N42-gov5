#!/usr/bin/env python3
"""S17: round 35zzzc (n42-r89, send/receive edge stamps on top of
N42_CONTENTION_DIAG=1). Splits Round2 (and Round1) into six segments
using the new sender/receiver stamps (glossary: docs/QS_BLOCK_TIME_BUDGET.md
6cl), joined by view<->n offset the same way as 6cb-6ck, PLUS a
validator-index<->node mapping derived from LeaderForView's own formula
(validator.go:174-179: leader = (view // tenure) % n_validators, tenure=4)
so cvKthVoter/pvKthVoter (validator indices) can be resolved to the
follower node whose own view-timing line carries the receive-side stamps.

Six Round2 segments (leader clock unless noted):
  L1 = pqcEmit2Deq + pqcDeq2Pub + pqcPubDur       (leader: PrepareQC emit -> publish end)
  D  = follower's own pqcRxAt - leader's own pqcPubAt   (downlink wire)
  F1 = follower's pqcRx2Arr + pqc2cv               (follower: rx -> commit vote emit)
  F2 = follower's cvEmit2Deq + cvDeq2Pub + cvPubDur (follower: commit vote emit -> publish end)
  U  = leader's cvKthRxAt - follower's own cvPubAt  (uplink wire)
  L2 = cvKthRx2Arr + r2qk                           (leader: rx -> handler -> QC formed)
The follower used for D/F1/F2/U is the one named by the LEADER's own
cvKthVoter for that view (the vote that actually completed the quorum).

Usage: send_recv_split.py <kept-logs-dir> leg_b1_start leg_b1_end
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

propose_all = {}
view_changed_leader = {n: [] for n in NODES}
blockimport = {}
vt_leader_lines = []     # (node, time, view, raw msg text)
vt_follower_lines = []    # (node, time, view, raw msg text)

for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"blockimport phases"' in line:
                d = json.loads(line); blockimport[(node, d['n'])] = d
            elif '"msg":"hotstuff: view changed"' in line and '"isLeader":true' in line:
                d = json.loads(line); view_changed_leader[node].append((d['tMs'], d['view']))
            elif '"msg":"hotstuff view timing' in line:
                d = json.loads(line)
                msg = d['msg']
                t = d['time']
                if 'role=leader' in msg:
                    vt_leader_lines.append((node, t, msg))
                elif 'role=follower' in msg:
                    vt_follower_lines.append((node, t, msg))

for n in view_changed_leader:
    view_changed_leader[n].sort()

def field(msg, name, cast=str):
    m = re.search(re.escape(name) + r'=("[^"]*"|-?\d+)', msg)
    if not m:
        return None
    v = m.group(1)
    if v.startswith('"'):
        v = v[1:-1]
    if cast is str:
        return v
    try:
        return cast(v)
    except ValueError:
        return None

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

rec = {}
for n, d in propose_all.items():
    tMs = d['tMs']; total = d['total']; write = d['write']
    push_instant = tMs - write / 1e6
    rec[n] = dict(node=d['node'], txs=d['txs'], time=d['time'], push_instant=push_instant)

full_ns = sorted(n for n, d in propose_all.items() if d.get('txs', 0) >= FULL_TXS)
windows = {}
for leg_name, leg in (('B1', LEG_B1), ('B2', LEG_B2)):
    ns_leg = sorted(n for n in full_ns if in_leg(n, leg))
    w1c, w2c = WIN_COUNTS[leg_name]
    windows[leg_name + 'win1'] = ns_leg[:w1c]
    windows[leg_name + 'win2'] = ns_leg[w1c:w1c + w2c]
full_windows = sum((windows[w] for w in FULL_WIN_NAMES), [])
print(f'full windows {FULL_WIN_NAMES}: {len(full_windows)} blocks')

chained = []
for n in full_windows:
    if (n - 1) not in rec:
        continue
    r0, r1 = rec[n - 1], rec[n]
    if r0['node'] != r1['node']:
        continue
    cycle = r1['push_instant'] - r0['push_instant']
    if cycle <= 0 or cycle > 6000:
        continue
    qc = None
    for tMs, view in view_changed_leader.get(r1['node'], []):
        if tMs > r0['push_instant']:
            qc = (tMs, view)
            break
    if qc:
        chained.append(dict(n=n, leader=r1['node'], qc=qc, cycle=cycle))
print(f'in-tenure full blocks usable: {len(chained)}')

offsets = {'B1': collections.Counter(), 'B2': collections.Counter()}
for row in chained:
    tMs, view = row['qc']
    leg = 'B1' if in_leg(row['n'], LEG_B1) else ('B2' if in_leg(row['n'], LEG_B2) else None)
    if leg:
        offsets[leg][view - row['n']] += 1
leg_offset = {leg: offsets[leg].most_common(1)[0][0] for leg in offsets if offsets[leg]}
print(f'leg offsets: {leg_offset}')

def n_of_view(view, t):
    leg = leg_of_time(t)
    if leg is None or leg not in leg_offset:
        return None
    return view - leg_offset[leg]

# ---- validator index <-> node, from LeaderForView's own formula ----
# IMPORTANT: nodes restart (SIGTERM+relaunch) between legs, so the view
# counter's phase relative to block numbers -- and hence the validator
# rotation's phase -- differs per leg (confirmed: leg_offset itself
# differs between B1/B2 by exactly 1, e.g. -13652355 vs -13652354).
# Use the ALREADY-CALIBRATED per-leg view<->n offset directly (no
# separate small-range search, which is what produced a misleadingly
# low 0.83 global purity by averaging two internally-consistent but
# mutually-shifted-by-one mappings).
vi_to_node_by_leg = {}
for leg_name in ('B1', 'B2'):
    if leg_name not in leg_offset:
        continue
    off = leg_offset[leg_name]
    groups = collections.defaultdict(collections.Counter)
    for n, d in propose_all.items():
        leg = leg_of_time(d['time'])
        if leg != leg_name:
            continue
        v = n + off
        vi = (v // TENURE) % NVAL
        groups[vi][d['node']] += 1
    purity = []
    m = {}
    for vi, c in groups.items():
        total = sum(c.values())
        top_node, top_count = c.most_common(1)[0]
        purity.append(top_count / total)
        m[vi] = top_node
    avgp = sum(purity) / len(purity) if purity else 0
    print(f'validator-index<->node map, {leg_name} (purity {avgp:.4f}): {m}')
    vi_to_node_by_leg[leg_name] = m

def vi_to_node_for(t, vi):
    leg = leg_of_time(t)
    m = vi_to_node_by_leg.get(leg)
    return m.get(vi) if m else None

def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):5d} median={med:8.2f} p90={p90:8.2f}')
    return med

# index leader/follower lines by (node, n)
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

print()
print('=== Round2 six-segment split, matched to block n-1 (the view whose Round2 gates the cycle) ===')
rows = []
neg_count = 0
for row in chained:
    n = row['n']; leader = row['leader']
    lm = vt_leader_by_n.get((leader, n - 1))
    if not lm:
        continue
    r2 = ms_field(lm, 'r2')
    r2qk = ms_field(lm, 'r2qk')
    cvKthRx2Arr = ms_field(lm, 'cvKthRx2Arr')
    cvKthRxAt = field(lm, 'cvKthRxAt', int)
    cvKthVoter = field(lm, 'cvKthVoter', int)
    cvKthVia = field(lm, 'cvKthVia')
    pqcEmit2Deq = ms_field(lm, 'pqcEmit2Deq')
    pqcDeq2Pub = ms_field(lm, 'pqcDeq2Pub')
    pqcPubDur = ms_field(lm, 'pqcPubDur')
    pqcPubAt = field(lm, 'pqcPubAt', int)
    follower_node = vi_to_node_for(propose_all[n - 1]['time'], cvKthVoter) if cvKthVoter is not None else None
    if follower_node is None:
        continue
    fm = vt_follower_by_n.get((follower_node, n - 1))
    if not fm:
        continue
    pqcRxAt = field(fm, 'pqcRxAt', int)
    pqcRx2Arr = ms_field(fm, 'pqcRx2Arr')
    pqc2cv = ms_field(fm, 'pqc2cv')
    cvEmit2Deq = ms_field(fm, 'cvEmit2Deq')
    cvDeq2Pub = ms_field(fm, 'cvDeq2Pub')
    cvPubDur = ms_field(fm, 'cvPubDur')
    cvPubAt = field(fm, 'cvPubAt', int)
    cvPath = field(fm, 'cvPath')
    pqcVia = field(fm, 'pqcVia')

    if None in (pqcEmit2Deq, pqcDeq2Pub, pqcPubDur, pqcPubAt, pqcRxAt,
                pqcRx2Arr, pqc2cv, cvEmit2Deq, cvDeq2Pub, cvPubDur, cvPubAt,
                cvKthRxAt, cvKthRx2Arr, r2qk, r2):
        continue

    L1 = pqcEmit2Deq + pqcDeq2Pub + pqcPubDur
    D = pqcRxAt - pqcPubAt
    F1 = pqcRx2Arr + pqc2cv
    F2 = cvEmit2Deq + cvDeq2Pub + cvPubDur
    U = cvKthRxAt - cvPubAt
    L2 = cvKthRx2Arr + r2qk
    total = L1 + D + F1 + F2 + U + L2
    if min(L1, D, F1, F2, U, L2) < -2:
        neg_count += 1
        continue
    rows.append(dict(n=n, L1=L1, D=D, F1=F1, F2=F2, U=U, L2=L2, total=total, r2=r2,
                      voter=cvKthVoter, follower=follower_node, cvPath=cvPath, pqcVia=pqcVia,
                      txs=propose_all[n - 1]['txs']))

print(f'usable rows: {len(rows)} / {len(chained)}; excluded for a segment < -2ms: {neg_count}')
for seg in ['L1', 'D', 'F1', 'F2', 'U', 'L2']:
    pstats(f'  {seg}', [r[seg] for r in rows])
med_r2 = pstats('  r2 (measured total)', [r['r2'] for r in rows])
med_sum = pstats('  sum(L1..L2)', [r['total'] for r in rows])
if med_r2 and med_sum:
    print(f'  closure: sum/r2 = {100*med_sum/med_r2:.1f}%')

print()
print('=== Round2 six-segment split, ALL views (any size), joined to the view\'s OWN block for the size law ===')
all_rows = []
for node, t, msg in vt_leader_lines:
    n = n_of_view(view_of(msg), t)
    if n is None or n not in propose_all:
        continue
    r2 = ms_field(msg, 'r2')
    r2qk = ms_field(msg, 'r2qk')
    cvKthRx2Arr = ms_field(msg, 'cvKthRx2Arr')
    cvKthRxAt = field(msg, 'cvKthRxAt', int)
    cvKthVoter = field(msg, 'cvKthVoter', int)
    pqcEmit2Deq = ms_field(msg, 'pqcEmit2Deq')
    pqcDeq2Pub = ms_field(msg, 'pqcDeq2Pub')
    pqcPubDur = ms_field(msg, 'pqcPubDur')
    pqcPubAt = field(msg, 'pqcPubAt', int)
    follower_node = vi_to_node_for(t, cvKthVoter) if cvKthVoter is not None else None
    if follower_node is None:
        continue
    fm = vt_follower_by_n.get((follower_node, n))
    if not fm:
        continue
    pqcRxAt = field(fm, 'pqcRxAt', int)
    pqcRx2Arr = ms_field(fm, 'pqcRx2Arr')
    pqc2cv = ms_field(fm, 'pqc2cv')
    cvEmit2Deq = ms_field(fm, 'cvEmit2Deq')
    cvDeq2Pub = ms_field(fm, 'cvDeq2Pub')
    cvPubDur = ms_field(fm, 'cvPubDur')
    cvPubAt = field(fm, 'cvPubAt', int)
    if None in (pqcEmit2Deq, pqcDeq2Pub, pqcPubDur, pqcPubAt, pqcRxAt,
                pqcRx2Arr, pqc2cv, cvEmit2Deq, cvDeq2Pub, cvPubDur, cvPubAt,
                cvKthRxAt, cvKthRx2Arr, r2qk, r2):
        continue
    L1 = pqcEmit2Deq + pqcDeq2Pub + pqcPubDur
    D = pqcRxAt - pqcPubAt
    F1 = pqcRx2Arr + pqc2cv
    F2 = cvEmit2Deq + cvDeq2Pub + cvPubDur
    U = cvKthRxAt - cvPubAt
    L2 = cvKthRx2Arr + r2qk
    if min(L1, D, F1, F2, U, L2) < -2:
        continue
    all_rows.append(dict(n=n, L1=L1, D=D, F1=F1, F2=F2, U=U, L2=L2, r2=r2,
                          txs=propose_all[n]['txs']))
print(f'usable rows (all sizes): {len(all_rows)}')

print()
print('=== Size law: r2 total and the six segments, by tx bucket, ALL views ===')
buckets = collections.OrderedDict([
    ('0', lambda t: t == 0), ('1-20k', lambda t: 1 <= t < 20000),
    ('20-80k', lambda t: 20000 <= t < 80000), ('80-140k', lambda t: 80000 <= t < 140000),
    ('>140k', lambda t: t >= 140000)])
seg_buckets = {name: {'r2': [], 'L1': [], 'D': [], 'F1': [], 'F2': [], 'U': [], 'L2': []} for name in buckets}
for r in all_rows:
    for name, pred in buckets.items():
        if pred(r['txs']):
            for seg in ('r2', 'L1', 'D', 'F1', 'F2', 'U', 'L2'):
                seg_buckets[name][seg].append(r[seg])
            break
for name in buckets:
    line = f'  {name:8s}'
    for seg in ('r2', 'L1', 'D', 'F1', 'F2', 'U', 'L2'):
        xs = sorted(seg_buckets[name][seg])
        line += f' {seg}={st.median(xs):.0f}(n={len(xs)})' if xs else f' {seg}=n/a'
    print(line)

print()
print('=== Round1 six-segment split (Proposal -> prepare votes -> PrepareQC) ===')
rows1 = []
neg1 = 0
for row in chained:
    n = row['n']; leader = row['leader']
    lm = vt_leader_by_n.get((leader, n - 1))
    if not lm:
        continue
    r1 = ms_field(lm, 'r1')
    r1qk = ms_field(lm, 'r1qk')
    pvKthRx2Arr = ms_field(lm, 'pvKthRx2Arr')
    pvKthRxAt = field(lm, 'pvKthRxAt', int)
    pvKthVoter = field(lm, 'pvKthVoter', int)
    prEmit2Deq = ms_field(lm, 'prEmit2Deq')
    prDeq2Pub = ms_field(lm, 'prDeq2Pub')
    prPubDur = ms_field(lm, 'prPubDur')
    follower_node = vi_to_node_for(propose_all[n - 1]['time'], pvKthVoter) if pvKthVoter is not None else None
    if follower_node is None:
        continue
    fm = vt_follower_by_n.get((follower_node, n - 1))
    if not fm:
        continue
    pvEmit2Deq = ms_field(fm, 'pvEmit2Deq')
    pvDeq2Pub = ms_field(fm, 'pvDeq2Pub')
    pvPubDur = ms_field(fm, 'pvPubDur')
    # No pr(oposal)RxAt / pv PubAt absolute fields exist for downlink/uplink
    # timing on Round1 in the glossary (only pqc/cv carry PubAt) -- so D/U
    # for Round1 are n/a; report what IS measurable: leader prEmit2Deq/
    # prDeq2Pub/prPubDur, follower's own pvEmit2Deq/pvDeq2Pub/pvPubDur, and
    # r1qk/pvKthRx2Arr, and let the reader see the gap explicitly.
    if None in (prEmit2Deq, prDeq2Pub, prPubDur, pvEmit2Deq, pvDeq2Pub, pvPubDur,
                pvKthRx2Arr, r1qk, r1):
        continue
    L1 = prEmit2Deq + prDeq2Pub + prPubDur
    F2 = pvEmit2Deq + pvDeq2Pub + pvPubDur
    L2 = pvKthRx2Arr + r1qk
    total = L1 + F2 + L2
    if min(L1, F2, L2) < -2:
        neg1 += 1
        continue
    rows1.append(dict(n=n, L1=L1, F2=F2, L2=L2, total=total, r1=r1))

print(f'usable rows: {len(rows1)} / {len(chained)}; excluded: {neg1}')
print('  NOTE: Round1 has no absolute PubAt/RxAt fields for the Proposal, so the')
print('  downlink/uplink wire legs (D, U-equivalent) are n/a -- only L1 (leader')
print('  propose->publish), F2 (follower prepare-vote emit->publish) and L2')
print('  (leader rx->handler->PrepareQC) are measurable; the remainder is the')
print('  unstamped proposal-transit + prepare-vote-transit wire time.')
for seg in ['L1', 'F2', 'L2']:
    pstats(f'  {seg}', [r[seg] for r in rows1])
med_r1 = pstats('  r1 (measured total)', [r['r1'] for r in rows1])
med_sum1 = pstats('  sum(L1,F2,L2)', [r['total'] for r in rows1])
if med_r1 and med_sum1:
    print(f'  named-segments share of r1: {100*med_sum1/med_r1:.1f}% (remainder = unstamped wire transit)')

print()
print('=== PATH breakdown ===')
path_counts = collections.Counter(r['cvPath'] for r in rows)
print(f'  cv path (k-th voter) counts: {dict(path_counts)}')
via_counts = collections.Counter(r['pqcVia'] for r in rows)
print(f'  pqc via (k-th voter side) counts: {dict(via_counts)}')
rotor_r2 = [r['total'] for r in rows if r['cvPath'] == 'rotor ok']
gossip_r2 = [r['total'] for r in rows if r['cvPath'] and 'gossip' in r['cvPath']]
pstats('  Round2 total, cvPath=rotor ok', rotor_r2)
pstats('  Round2 total, cvPath contains gossip', gossip_r2)

# Rotor success rate per message type, scanning ALL lines (not just full-window)
print()
print('=== Rotor success rate per message type, all views ===')
path_tally = {'pr': collections.Counter(), 'pv': collections.Counter(),
              'pqc': collections.Counter(), 'cv': collections.Counter()}
for node, t, msg in vt_leader_lines + vt_follower_lines:
    for pfx in ('pr', 'pqc'):
        v = field(msg, f'{pfx}Path')
        if v:
            path_tally[pfx][v] += 1
    for pfx in ('pv', 'cv'):
        v = field(msg, f'{pfx}Path')
        if v:
            path_tally[pfx][v] += 1
for k, c in path_tally.items():
    total = sum(c.values())
    ok = c.get('rotor ok', 0)
    print(f'  {k}Path: {dict(c)} -> rotor success {100*ok/total:.1f}%' if total else f'  {k}Path: n/a')

# Rotor error text
print()
print('=== Rotor failure error text (any "rotor failed" line) ===')
err_counts = collections.Counter()
for path in paths:
    with open(path, errors='replace') as f:
        for line in f:
            if 'rotor' in line and ('failed' in line or 'fail' in line):
                m = re.search(r'"err":"([^"]*)"', line)
                if m:
                    err_counts[m.group(1)] += 1
for err, c in err_counts.most_common(5):
    print(f'  {c:6d}  {err}')

