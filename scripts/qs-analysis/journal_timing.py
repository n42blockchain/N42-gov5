#!/usr/bin/env python3
"""S18: round 35zzzd (n42-r90, journal-timing stamps jpvMs/jcvMs/jcvAt on
top of the S17 send/receive edges). Tests prediction 86(a): does the
leader's own jcvMs (journalCommitVote's own MDBX-write duration) match
6cm's residual law (1/14/132/152/326 ms by size bucket) and, added to
6cm's six stamped segments, cover >=80% of Round2's median?

Also (Job 1b): for views where the LEADER's own jcvMs is small but r2kth
is still large, is the k-th (quorum-completing) FOLLOWER's own jcvMs
close to r2kth instead -- i.e. is the round waiting on whichever side
(leader or follower) is the slow one, not always the leader?

Job 1c (collision partner): for each node's own journal wait
(jcvAt -> jcvAt+jcvMs), does it end within 10ms of that SAME node's own
block-write end (blockimport/blockwrite "write" field), a
CommitToCanonical/persistState ("hotstuff: commit phases") end, or
neither?

Usage: journal_timing.py <kept-logs-dir> leg_b1_start leg_b1_end
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
vt_leader_lines = []
vt_follower_lines = []
commit_phases = {n: [] for n in NODES}   # node -> [(time, view, canon_end_approx, total_ms)]

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
                msg = d['msg']; t = d['time']
                if 'role=leader' in msg:
                    vt_leader_lines.append((node, t, msg))
                elif 'role=follower' in msg:
                    vt_follower_lines.append((node, t, msg))
            elif '"msg":"hotstuff: commit phases"' in line:
                d = json.loads(line)
                total = (d.get('canon', 0) + d.get('persist', 0)) / 1e6
                commit_phases[node].append((d['time'], d['view'], total))

for n in view_changed_leader:
    view_changed_leader[n].sort()
for n in commit_phases:
    commit_phases[n].sort()

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
    rec[n] = dict(node=d['node'], txs=d['txs'], time=d['time'], push_instant=push_instant, write=write / 1e6)

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

# vi<->node map, per leg (same method as 6cm)
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
    m = {}
    purity = []
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

# ================= JOB 1a: leader jcvMs/jpvMs =================
print()
print('=== JOB 1a: leader jcvMs / jpvMs, full in-tenure views (matched to block n-1) ===')
leader_rows = []
for row in chained:
    n = row['n']; leader = row['leader']
    lm = vt_leader_by_n.get((leader, n - 1))
    if not lm:
        continue
    jcvMs = ms_field(lm, 'jcvMs')
    jpvMs = ms_field(lm, 'jpvMs')
    jcvAt = field(lm, 'jcvAt', int)
    r2 = ms_field(lm, 'r2')
    r2kth = ms_field(lm, 'r2kth')
    if jcvMs is None or r2 is None:
        continue
    leader_rows.append(dict(n=n, leader=leader, jcvMs=jcvMs, jpvMs=jpvMs, jcvAt=jcvAt,
                             r2=r2, r2kth=r2kth, txs=propose_all[n - 1]['txs']))
print(f'usable leader rows: {len(leader_rows)} / {len(chained)}')
pstats('  jcvMs', [r['jcvMs'] for r in leader_rows])
pstats('  jpvMs', [r['jpvMs'] for r in leader_rows])
pstats('  r2', [r['r2'] for r in leader_rows])

print()
print('  joint table: jcvMs bucket -> share, mean r2kth, mean jcvMs, mean(r2kth-jcvMs)')
buckets3 = [('>=200', lambda x: x >= 200), ('20-200', lambda x: 20 <= x < 200), ('<20', lambda x: x < 20)]
for name, pred in buckets3:
    rows = [r for r in leader_rows if pred(r['jcvMs'])]
    if not rows:
        print(f'  {name}: n=0'); continue
    share = 100 * len(rows) / len(leader_rows)
    mean_r2kth = st.mean(x for x in (r['r2kth'] for r in rows) if x is not None)
    mean_jcv = st.mean(r['jcvMs'] for r in rows)
    gaps = [r['r2kth'] - r['jcvMs'] for r in rows if r['r2kth'] is not None]
    mean_gap = st.mean(gaps) if gaps else float('nan')
    print(f'  {name:8s} n={len(rows):4d} share={share:5.1f}%  mean_r2kth={mean_r2kth:7.1f}  '
          f'mean_jcvMs={mean_jcv:7.1f}  mean_gap={mean_gap:7.1f}')

print()
print('  jcvMs by block-size bucket (leader), next to 6cm residual law (1/14/132/152/326)')
buckets = collections.OrderedDict([
    ('0', lambda t: t == 0), ('1-20k', lambda t: 1 <= t < 20000),
    ('20-80k', lambda t: 20000 <= t < 80000), ('80-140k', lambda t: 80000 <= t < 140000),
    ('>140k', lambda t: t >= 140000)])
# use ALL leader lines (any size) joined directly to their OWN block, for the size law
all_leader_rows = []
for node, t, msg in vt_leader_lines:
    n = n_of_view(view_of(msg), t)
    if n is None or n not in propose_all:
        continue
    jcvMs = ms_field(msg, 'jcvMs')
    r2 = ms_field(msg, 'r2')
    if jcvMs is None:
        continue
    all_leader_rows.append(dict(n=n, jcvMs=jcvMs, r2=r2, txs=propose_all[n]['txs']))
for name, pred in buckets.items():
    xs = sorted(r['jcvMs'] for r in all_leader_rows if pred(r['txs']))
    r2s = sorted(r['r2'] for r in all_leader_rows if pred(r['txs']) and r['r2'] is not None)
    if xs:
        print(f'  {name:8s} jcvMs med={st.median(xs):6.1f} (n={len(xs)})   '
              f'r2 med={st.median(r2s):6.1f}' if r2s else f'  {name:8s} jcvMs med={st.median(xs):6.1f} (n={len(xs)})')

# ================= JOB 1b: follower jcvMs, k-th voter test =================
print()
print('=== JOB 1b: follower jcvMs/jpvMs, and the k-th-voter test ===')
all_follower_rows = []
for node, t, msg in vt_follower_lines:
    n = n_of_view(view_of(msg), t)
    if n is None:
        continue
    jcvMs = ms_field(msg, 'jcvMs')
    jpvMs = ms_field(msg, 'jpvMs')
    if jcvMs is None:
        continue
    all_follower_rows.append(dict(node=node, n=n, jcvMs=jcvMs, jpvMs=jpvMs))
pstats('  follower jcvMs (all views)', [r['jcvMs'] for r in all_follower_rows])
pstats('  follower jpvMs (all views)', [r['jpvMs'] for r in all_follower_rows])

# k-th voter's own jcvMs/jcvAt, matched to the leader rows above
kth_rows = []
for row in chained:
    n = row['n']; leader = row['leader']
    lm = vt_leader_by_n.get((leader, n - 1))
    if not lm:
        continue
    r2 = ms_field(lm, 'r2'); r2kth = ms_field(lm, 'r2kth')
    jcvMs_leader = ms_field(lm, 'jcvMs')
    jcvAt_leader = field(lm, 'jcvAt', int)
    cvKthVoter = field(lm, 'cvKthVoter', int)
    if cvKthVoter is None or r2 is None:
        continue
    follower_node = vi_to_node_for(propose_all[n - 1]['time'], cvKthVoter)
    if follower_node is None:
        continue
    fm = vt_follower_by_n.get((follower_node, n - 1))
    if not fm:
        continue
    jcvMs_f = ms_field(fm, 'jcvMs')
    jcvAt_f = field(fm, 'jcvAt', int)
    if jcvMs_f is None or jcvMs_leader is None:
        continue
    kth_rows.append(dict(n=n, r2=r2, r2kth=r2kth, jcv_leader=jcvMs_leader, jcv_kth=jcvMs_f,
                          leader_node=leader, kth_node=follower_node,
                          jcvAt_leader=jcvAt_leader, jcvAt_kth=jcvAt_f,
                          txs=propose_all[n - 1]['txs']))

print(f'  matched leader+k-th-follower rows: {len(kth_rows)}')
low_leader = [r for r in kth_rows if r['jcv_leader'] < 20 and r['r2kth'] is not None and r['r2kth'] >= 100]
print(f'  views with leader jcvMs<20ms AND r2kth>=100ms: {len(low_leader)}')
if low_leader:
    explained = sum(1 for r in low_leader if abs(r['r2kth'] - r['jcv_kth']) <= 40)
    print(f'    of those, kth-follower jcvMs within 40ms of r2kth: {explained}/{len(low_leader)} '
          f'({100*explained/len(low_leader):.1f}%)')
    pstats('    r2kth in this subset', [r['r2kth'] for r in low_leader])
    pstats('    kth-follower jcvMs in this subset', [r['jcv_kth'] for r in low_leader])

print()
print('  identity test: r2 ~ leader_jcvMs + kth_follower_jcvMs + ~24ms edges (6cm sum(L1..L2)=24)')
EDGES = 24
errs = []
within15 = 0
for r in kth_rows:
    pred = r['jcv_leader'] + r['jcv_kth'] + EDGES
    err = abs(pred - r['r2'])
    errs.append(err)
    if r['r2'] > 0 and err / r['r2'] <= 0.15:
        within15 += 1
pstats('  abs error |pred - r2|', errs)
print(f'  share within 15% of r2: {100*within15/len(kth_rows):.1f}% (n={len(kth_rows)})' if kth_rows else 'n/a')

# ================= JOB 1c: collision partner =================
print()
print('=== JOB 1c: collision partner for the journal wait (jcvAt -> jcvAt+jcvMs) ===')
print('  (commit_phases/"hotstuff: commit phases" carries no tMs -- only second-resolution')
print('   "time" -- so a canonical-commit/persist match can only be tested at 1s granularity,')
print('   reported separately from the ms-precise own-block-write test)')

# build a per-node list of (write_end_tMs) from propose_all (leader's own write) and
# blockimport (follower's own write), for a NEAREST-match search (not just n-1/n).
write_ends_by_node = collections.defaultdict(list)
for n, d in propose_all.items():
    write_ends_by_node[d['node']].append(d['tMs'])
for (node, n), d in blockimport.items():
    write_ends_by_node[node].append(d['tMs'])
for node in write_ends_by_node:
    write_ends_by_node[node].sort()

def nearest_write_end(node, jend):
    lst = write_ends_by_node.get(node, [])
    if not lst:
        return None
    import bisect
    i = bisect.bisect_left(lst, jend)
    best = None
    for cand in (lst[i - 1] if i > 0 else None, lst[i] if i < len(lst) else None):
        if cand is None:
            continue
        d = abs(cand - jend)
        if best is None or d < best:
            best = d
    return best

def check_collision(rows, label):
    own_write = 0; same_second_commit = 0; unknown = 0; total = 0
    for node, jcvAt, jcvMs, n in rows:
        if jcvAt is None or jcvMs < 5:
            continue
        total += 1
        jend = jcvAt + jcvMs
        d = nearest_write_end(node, jend)
        if d is not None and d <= 10:
            own_write += 1
            continue
        # same-second commit-phases check (coarse, 1s resolution only)
        import datetime
        EDT = datetime.timezone(datetime.timedelta(hours=-4))
        t_str = datetime.datetime.fromtimestamp(jend / 1000, EDT).strftime('%Y-%m-%d %H:%M:%S')
        hit_commit = any(t == t_str for t, view, tot in commit_phases.get(node, []))
        if hit_commit:
            same_second_commit += 1
        else:
            unknown += 1
    if total:
        print(f'  {label}: n={total}  own_write_end_within_10ms={own_write} ({100*own_write/total:.1f}%)  '
              f'commit_phases_same_second={same_second_commit} ({100*same_second_commit/total:.1f}%)  '
              f'unknown={unknown} ({100*unknown/total:.1f}%)')
    else:
        print(f'  {label}: n=0')

leader_rows_c = [(r['leader'], r['jcvAt'], r['jcvMs'], r['n']) for r in leader_rows]
check_collision(leader_rows_c, 'leader (own PrepareQC self-commit-vote journal)')
kth_rows_c = [(r['kth_node'], r['jcvAt_kth'], r['jcv_kth'], r['n']) for r in kth_rows]
check_collision(kth_rows_c, 'k-th follower (own commit-vote journal)')
