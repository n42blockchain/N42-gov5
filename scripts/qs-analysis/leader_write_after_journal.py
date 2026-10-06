#!/usr/bin/env python3
"""S19+S20: round 35zzze (n42-r91, prediction 87: does starting the leader's
own block write only AFTER its own commit-vote journal succeeds -- instead of
racing it for the single MDBX writer -- shrink Round2 by the amount 6cp
measured jcvMs contributes? A/B by leg: B1 = N42_LEADER_WRITE_AFTER_JOURNAL=0
(today's ordering), B2 = 1 (write waits on the journal, or a 150ms timeout,
or view-abandonment, whichever first). New fields on "miner: propose phases"
(leader-only, top-level JSON, nanoseconds): lwWait, lwWhy in
{"off","journal","abandoned","timeout","not-proposed-yet","unsupported"}.

Also computes the within-leg win1->win2 slowdown segment table used by S20
(the write-up itself reads the heap/CPU pprof captures and r35zzze-mem.log
separately -- this script only produces the phase table and the safety /
"where did the time go" checks that are log-derived).

Usage: leader_write_after_journal.py <kept-logs-dir> leg_b1_start leg_b1_end
       leg_b2_start leg_b2_end b1win1 b1win2 b2win1 b2win2 full_win_names
"""
import sys, os, json, glob, re, statistics as st, collections, bisect, datetime

ROOT = sys.argv[1]
LEG_B1 = (sys.argv[2], sys.argv[3])
LEG_B2 = (sys.argv[4], sys.argv[5])
WIN_COUNTS = {'B1': (int(sys.argv[6]), int(sys.argv[7])), 'B2': (int(sys.argv[8]), int(sys.argv[9]))}
FULL_WIN_NAMES = tuple(sys.argv[10].split(','))
FULL_TXS = 150000
TENURE = 4
NVAL = 7
EDT = datetime.timezone(datetime.timedelta(hours=-4))


def node_of(p):
    return os.path.basename(p).split('-')[0]


paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
NODES = [node_of(p) for p in paths]

propose_all = {}
view_changed_leader = {n: [] for n in NODES}
blockimport = {}
vt_leader_lines = []
vt_follower_lines = []
commit_phases = {n: [] for n in NODES}
prefill_lines = []       # (node, time, dict)
committed_not_executed = []   # (node, time)
refusing_production = []      # (node, time)
deferred_resumed = []         # (node, time)
build_triggered = []          # (node, tMs, time)
tc_events = set()
timeout_events = set()
fetch_lines = []

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
                commit_phases[node].append((d['time'], d['view'], total, d.get('canon', 0) / 1e6, d.get('persist', 0) / 1e6))
            elif '"msg":"miner: prefill phases"' in line:
                d = json.loads(line); prefill_lines.append((node, d['time'], d))
            elif '"msg":"hotstuff: committed block not executed locally"' in line:
                d = json.loads(line); committed_not_executed.append((node, d['time']))
            elif '"msg":"hotstuff: refusing block production on unexecuted committed parent"' in line:
                d = json.loads(line); refusing_production.append((node, d['time']))
            elif '"msg":"hotstuff: deferred production resumed' in line:
                d = json.loads(line); deferred_resumed.append((node, d['time']))
            elif '"msg":"miner: build triggered' in line:
                d = json.loads(line); build_triggered.append((node, d.get('tMs'), d['time']))
            elif '"msg":"TC formed locally' in line:
                d = json.loads(line); tc_events.add((d['time'], d.get('view')))
            elif 'view timed out' in line and line.strip().startswith('{'):
                d = json.loads(line); timeout_events.add((d.get('time'), d.get('view')))
            if 'fetch' in line.lower() or 'FetchBlockByHash' in line:
                fetch_lines.append((node, line))

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
    rec[n] = dict(node=d['node'], txs=d['txs'], time=d['time'], push_instant=push_instant,
                  write=write / 1e6, total=total / 1e6, assemble=d.get('assemble', 0) / 1e6,
                  finalize=d.get('finalize', 0) / 1e6, bls=d.get('bls', 0) / 1e6,
                  lwWait=d.get('lwWait', 0) / 1e6, lwWhy=d.get('lwWhy', 'off'), tMs=tMs)

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

chained = []
handover = []
for n in full_windows:
    if (n - 1) not in rec:
        continue
    r0, r1 = rec[n - 1], rec[n]
    cycle = r1['push_instant'] - r0['push_instant']
    if cycle <= 0 or cycle > 6000:
        continue
    if r0['node'] == r1['node']:
        qc = None
        for tMs, view in view_changed_leader.get(r1['node'], []):
            if tMs > r0['push_instant']:
                qc = (tMs, view)
                break
        if qc:
            chained.append(dict(n=n, leader=r1['node'], qc=qc, cycle=cycle))
    else:
        handover.append(dict(n=n, leader=r1['node'], prev_leader=r0['node'], cycle=cycle))
print(f'in-tenure (chained) full blocks usable: {len(chained)}; handover full blocks usable: {len(handover)}')

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


def leg_win_of_n(n):
    for w, ns in windows.items():
        if n in ns:
            return w
    return None


# ============ JOB 1a: per leg/window segment table ============
print()
print('=== JOB 1a: leader jcvMs/jpvMs, Round1/Round2, QC->push, cycle, blockwrite, lwWait/lwWhy ===')
rows_by_win = collections.defaultdict(list)
for row in chained:
    n = row['n']; leader = row['leader']
    win = leg_win_of_n(n)
    if win is None:
        continue
    lm = vt_leader_by_n.get((leader, n - 1))
    r1_d = rec[n]
    qc_tMs, qc_view = row['qc']
    entry = dict(n=n, leader=leader, cycle=row['cycle'], qc_to_push=r1_d['push_instant'] - qc_tMs,
                 write=r1_d['write'], lwWait=r1_d['lwWait'], lwWhy=r1_d['lwWhy'],
                 assemble=r1_d['assemble'], finalize=r1_d['finalize'], total=r1_d['total'],
                 write_start_offset=r1_d['total'] - r1_d['write'])
    if lm:
        entry['jcvMs'] = ms_field(lm, 'jcvMs')
        entry['jpvMs'] = ms_field(lm, 'jpvMs')
        entry['r1'] = ms_field(lm, 'r1')
        entry['r2'] = ms_field(lm, 'r2')
        entry['r1kth'] = ms_field(lm, 'r1kth')
        entry['r2kth'] = ms_field(lm, 'r2kth')
    rows_by_win[win].append(entry)

for win in ('B1win1', 'B1win2', 'B2win1', 'B2win2'):
    rs = rows_by_win.get(win, [])
    print(f'-- {win} (n={len(rs)}) --')
    pstats('  jcvMs', [r.get('jcvMs') for r in rs])
    pstats('  jpvMs', [r.get('jpvMs') for r in rs])
    pstats('  r1', [r.get('r1') for r in rs])
    pstats('  r2', [r.get('r2') for r in rs])
    pstats('  r1kth', [r.get('r1kth') for r in rs])
    pstats('  r2kth', [r.get('r2kth') for r in rs])
    pstats('  QC->push', [r['qc_to_push'] for r in rs])
    pstats('  in-tenure cycle', [r['cycle'] for r in rs])
    pstats('  leader blockwrite (write)', [r['write'] for r in rs])
    pstats('  write start offset from propose (total-write)', [r['write_start_offset'] for r in rs])
    pstats('  lwWait', [r['lwWait'] for r in rs])
    why_counts = collections.Counter(r['lwWhy'] for r in rs)
    tot = sum(why_counts.values()) or 1
    print(f'  lwWhy shares: ' + ', '.join(f'{k}={100*v/tot:.1f}%({v})' for k, v in why_counts.most_common()))

print()
print('-- combined B1 vs B2 (both windows pooled) --')
for leg in ('B1', 'B2'):
    rs = rows_by_win.get(leg + 'win1', []) + rows_by_win.get(leg + 'win2', [])
    print(f'-- {leg} (n={len(rs)}) --')
    pstats('  jcvMs', [r.get('jcvMs') for r in rs])
    pstats('  r2', [r.get('r2') for r in rs])
    pstats('  r2kth', [r.get('r2kth') for r in rs])
    pstats('  in-tenure cycle', [r['cycle'] for r in rs])
    why_counts = collections.Counter(r['lwWhy'] for r in rs)
    tot = sum(why_counts.values()) or 1
    print(f'  lwWhy shares: ' + ', '.join(f'{k}={100*v/tot:.1f}%({v})' for k, v in why_counts.most_common()))

print()
print('-- handover full blocks, by leg (cycle only) --')
handover_by_leg = collections.defaultdict(list)
for row in handover:
    leg = leg_of_time(row['leader'] and rec[row['n']]['time'])
    handover_by_leg[leg].append(row['cycle'])
for leg in ('B1', 'B2'):
    pstats(f'  {leg} handover cycle', handover_by_leg.get(leg, []))

# ---- timeout / abandoned explanation ----
print()
print('-- lwWhy=timeout/abandoned cases, explained against TC/timeout events --')
print(f'distinct TC events (time,view): {len(tc_events)}')
print(f'distinct view-timed-out events: {len(timeout_events)}')
timeout_views_by_leg = collections.defaultdict(set)
for t, v in sorted(tc_events) + sorted(timeout_events):
    leg = leg_of_time(t)
    if leg and v is not None:
        timeout_views_by_leg[leg].add(v)
for win in ('B2win1', 'B2win2'):
    rs = [r for r in rows_by_win.get(win, []) if r['lwWhy'] in ('timeout', 'abandoned')]
    for r in rs:
        n = r['n']
        leg = 'B1' if in_leg(n, LEG_B1) else 'B2'
        view = n + leg_offset.get(leg, 0)
        near_timeout = any(abs(view - v) <= 1 for v in timeout_views_by_leg.get(leg, set()))
        lm = vt_leader_by_n.get((r['leader'], n - 1))
        pqc_pub_at = field(lm, 'pqcPubAt', int) if lm else None
        print(f'  {win} n={n} view={view} lwWait={r["lwWait"]:.1f}ms lwWhy={r["lwWhy"]} '
              f'near_timeout_or_TC_event(+-1 view)={near_timeout}')

# ============ JOB 1b: "not merely moved" -- where did the saved time go ============
print()
print('=== JOB 1b: "not merely moved" -- prefill, build dispatch, CommitToCanonical, gates ===')
print(f'prefill phases lines (>50ms outlier only): {len(prefill_lines)}')
prefill_by_leg = collections.defaultdict(list)
for node, t, d in prefill_lines:
    leg = leg_of_time(t)
    if leg:
        prefill_by_leg[leg].append(d)
for leg in ('B1', 'B2'):
    ds = prefill_by_leg.get(leg, [])
    print(f'  {leg}: n={len(ds)}')
    if ds:
        for k in ('persistWait', 'specTreeReload', 'insertParent', 'lockWait', 'rootLockWait', 'total'):
            pstats(f'    {k}', [d.get(k, 0) / 1e6 for d in ds])

print(f'committed-not-executed-locally events: {len(committed_not_executed)}')
print(f'refusing-production-on-unexecuted-parent events: {len(refusing_production)}')
print(f'deferred-production-resumed events: {len(deferred_resumed)}')
for label, evs in (('committed-not-executed', committed_not_executed),
                   ('refusing-production', refusing_production),
                   ('deferred-resumed', deferred_resumed)):
    by_leg = collections.Counter(leg_of_time(t) for _, t in evs)
    print(f'  {label} by leg: {dict(by_leg)}')

fetch_by_leg = collections.Counter()
own_block_fetch = 0
leader_blocks_by_leg = collections.defaultdict(set)
for n, d in propose_all.items():
    leg = leg_of_time(d['time'])
    if leg:
        leader_blocks_by_leg[leg].add(d['node'])
for node, line in fetch_lines:
    try:
        d = json.loads(line)
    except Exception:
        continue
    t = d.get('time', '')
    leg = leg_of_time(t)
    if leg:
        fetch_by_leg[leg] += 1
print(f'fetch-mentioning lines by leg: {dict(fetch_by_leg)}')

# CommitToCanonical / commit-phases duration, by leg+window (approx: bucket by
# the view's mapped block n, then by window)
print()
print('-- commit phases (canon+persist), by window --')
commit_by_win = collections.defaultdict(list)
for node, cps in commit_phases.items():
    for t, view, tot, canon, persist in cps:
        leg = leg_of_time(t)
        if leg is None or leg not in leg_offset:
            continue
        n = view - leg_offset[leg]
        win = leg_win_of_n(n)
        if win:
            commit_by_win[win].append((canon, persist, tot))
for win in ('B1win1', 'B1win2', 'B2win1', 'B2win2'):
    rs = commit_by_win.get(win, [])
    pstats(f'  {win} canon', [r[0] for r in rs])
    pstats(f'  {win} persist', [r[1] for r in rs])
    pstats(f'  {win} canon+persist total', [r[2] for r in rs])

# ============ JOB 1c: followers ============
print()
print('=== JOB 1c: followers -- import total/write, jcvMs/jpvMs, by leg (expect no change) ===')
follower_import = collections.defaultdict(list)
for (node, n), d in blockimport.items():
    leg = leg_of_time(d.get('time', ''))
    if leg:
        follower_import[leg].append(d)
for leg in ('B1', 'B2'):
    ds = follower_import.get(leg, [])
    print(f'  {leg}: n={len(ds)}')
    if ds:
        for k in ('total', 'write', 'body', 'proc'):
            vals = [d.get(k) for d in ds if d.get(k) is not None]
            if vals:
                pstats(f'    {k}', [v / 1e6 for v in vals])

follower_jcv_by_leg = collections.defaultdict(list)
follower_jpv_by_leg = collections.defaultdict(list)
for node, t, msg in vt_follower_lines:
    leg = leg_of_time(t)
    if leg is None:
        continue
    jcvMs = ms_field(msg, 'jcvMs'); jpvMs = ms_field(msg, 'jpvMs')
    if jcvMs is not None:
        follower_jcv_by_leg[leg].append(jcvMs)
    if jpvMs is not None:
        follower_jpv_by_leg[leg].append(jpvMs)
for leg in ('B1', 'B2'):
    pstats(f'  {leg} follower jcvMs', follower_jcv_by_leg.get(leg, []))
    pstats(f'  {leg} follower jpvMs', follower_jpv_by_leg.get(leg, []))

# ============ JOB 1d: safety ============
print()
print('=== JOB 1d: safety ===')
print(f'TC events by leg: {collections.Counter(leg_of_time(t) for t, v in tc_events)}')
print(f'timeout events by leg: {collections.Counter(leg_of_time(t) for t, v in timeout_events)}')
all_events = sorted(set(tc_events) | set(timeout_events))
for t, v in all_events:
    leg = leg_of_time(t)
    if leg:
        print(f'  {t} view={v} leg={leg}')

# cross-check every leader block's write line exists (n in propose_all with
# consecutive coverage inside each leg's full windows)
missing = []
for win, ns in windows.items():
    for n in ns:
        if n not in propose_all:
            missing.append((win, n))
print(f'full-window blocks missing a propose-phases write line: {len(missing)} {missing[:10]}')

# ============ JOB 1e: derived full-block ceiling ============
print()
print('=== JOB 1e: derived full-block ceiling (txs / mixed measured cycle), win1 only ===')
for win in ('B1win1', 'B2win1'):
    rs = rows_by_win.get(win, [])
    if not rs:
        continue
    ns = windows[win]
    txs = [propose_all[n]['txs'] for n in ns if n in propose_all]
    cycles = [r['cycle'] for r in rs]
    if txs and cycles:
        mean_txs = st.mean(txs); mean_cycle = st.mean(cycles)
        print(f'  {win}: mean txs/block={mean_txs:.0f} mean cycle={mean_cycle:.1f}ms '
              f'-> derived ceiling={1000*mean_txs/mean_cycle:.0f} tx/s')
