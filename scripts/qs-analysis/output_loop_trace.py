#!/usr/bin/env python3
"""S16a: test hypothesis H2 -- Round2 (r2kth) is a commit vote or PrepareQC
broadcast queued in the serial output loop (processOutputs,
internal/consensus/hotstuff/service.go:585-596) behind inline work that
opens an MDBX write transaction (OutputBlockCommitted's CommitToCanonical
+ persistState, service.go:642-713).

Code trace summary (see 6ck for the full citations):
  - OutputSendToValidator (votes) and OutputBroadcast (Proposal/PrepareQC/
    CommitVote-relay/Decide) are each dispatched via `go func(){...}()`
    INSIDE handleOutput -- i.e. processOutputs' single-consumer loop only
    pays the cost of spawning the goroutine, not the publish itself.
  - OutputBlockCommitted is the ONE case handled fully INLINE on that same
    loop: CommitToCanonical + persistState, each opening its own MDBX
    write transaction (service.go:679-713, "hotstuff: commit phases").
  - Both e.viewTiming.CommitVoteSent (proposal.go:449) and pqc2cv's other
    endpoint are stamped BEFORE `e.emit(...)` -- i.e. before the output
    even reaches the channel, let alone gets dispatched or published. The
    stamps in this round's diagnostics do not reach past "the engine
    decided to emit"; the actual network write inside handleBroadcast/
    handleSendToValidator (PublishToTopic, service.go:922 and others) is
    unstamped by anything in this round's data.

What this script computes: the ONLY test available without that missing
stamp -- per-node "hotstuff: commit phases" (canon+persist, i.e. the
inline, potentially-slow work sharing the SAME output-loop queue as the
vote/broadcast dispatch) joined by the SAME view<->n offset calibration
used throughout this campaign, correlated against r2kth for the
following view. This is a same-VIEW-adjacency correlation, not a
same-millisecond timestamp coincidence (commit phases carries no tMs),
and is reported as such.

Usage: output_loop_trace.py <kept-logs-dir> leg_b1_start leg_b1_end
       leg_b2_start leg_b2_end b1win1 b1win2 b2win1 b2win2 full_win_names
"""
import sys, os, json, glob, re, statistics as st, collections

ROOT = sys.argv[1]
LEG_B1 = (sys.argv[2], sys.argv[3])
LEG_B2 = (sys.argv[4], sys.argv[5])
WIN_COUNTS = {'B1': (int(sys.argv[6]), int(sys.argv[7])), 'B2': (int(sys.argv[8]), int(sys.argv[9]))}
FULL_WIN_NAMES = tuple(sys.argv[10].split(','))
FULL_TXS = 150000

def node_of(p):
    return os.path.basename(p).split('-')[0]

paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
NODES = [node_of(p) for p in paths]

propose_all = {}
view_changed_leader = {n: [] for n in NODES}
vt_leader = []          # (node, time, view, r1kth, r2kth, r2, r1)
commit_phases = {n: [] for n in NODES}   # node -> [(time, view, canon+persist total ns)]
blockimport = {}         # (node, n) -> dict (for follower write ms)

LEADER_PAT = re.compile(
    r'view=(\d+) role=leader propose=(\d+)ms r1=(\d+)ms r2=(\d+)ms total=(\d+)ms'
    r'(?: votes=(\d+)/(\d+))?'
    r'(?: r1n=(\d+))?(?: r1lw=(\d+)ms)?(?: r1lwMax=(\d+)ms)?(?: r1wk=(\d+)ms)?'
    r'(?: r1kth=(\d+)ms)?(?: r1qk=(\d+)ms)?'
    r'(?: r2n=(\d+))?(?: r2lw=(\d+)ms)?(?: r2lwMax=(\d+)ms)?(?: r2wk=(\d+)ms)?'
    r'(?: r2kth=(\d+)ms)?(?: r2qk=(\d+)ms)?')

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
            elif '"msg":"hotstuff view timing' in line and 'role=leader' in line:
                m = LEADER_PAT.search(line)
                if m:
                    g = m.groups()
                    view = int(g[0])
                    r1, r2 = int(g[2]), int(g[3])
                    r1kth = int(g[11]) if g[11] else None
                    r2kth = int(g[17]) if g[17] else None
                    i = line.find('"time":"'); t = line[i + 8:i + 27]
                    vt_leader.append((node, t, view, r1kth, r2kth, r2, r1))
            elif '"msg":"hotstuff: commit phases"' in line:
                d = json.loads(line)
                total = d.get('canon', 0) + d.get('persist', 0)
                commit_phases[node].append((d['time'], d['view'], total))

for n in view_changed_leader:
    view_changed_leader[n].sort()
for n in commit_phases:
    commit_phases[n].sort()

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
        chained.append(dict(n=n, leader=r1['node'], qc=qc))
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

vt_by_n = {}
for node, t, view, r1kth, r2kth, r2, r1 in vt_leader:
    n = n_of_view(view, t)
    if n is not None:
        vt_by_n[(node, n)] = dict(r1kth=r1kth, r2kth=r2kth, r2=r2, r1=r1)

commit_by_n = {}
for node, rows in commit_phases.items():
    for t, view, total in rows:
        n = n_of_view(view, t)
        if n is not None:
            commit_by_n[(node, n)] = total / 1e6  # ms

def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):5d} median={med:8.2f} p90={p90:8.2f}')
    return med

# ===== PART A: leader's own commit-phases(n-1) vs leader's own r2kth(n) =====
print()
print("=== PART A: leader's own commit-phases(n-1) [canon+persist ms] vs leader's own r2kth(n) ===")
pairs = []
for row in chained:
    n = row['n']; leader = row['leader']
    cp = commit_by_n.get((leader, n - 1))
    vt = vt_by_n.get((leader, n))
    if cp is not None and vt is not None and vt['r2kth'] is not None:
        pairs.append((cp, vt['r2kth']))
print(f'matched pairs: {len(pairs)}')
pstats('  commit_phases(n-1) [leader own]', [p[0] for p in pairs])
pstats('  r2kth(n) [leader own round]', [p[1] for p in pairs])
if len(pairs) > 3:
    xs = [p[0] for p in pairs]; ys = [p[1] for p in pairs]
    rx = st.pstdev(xs); ry = st.pstdev(ys)
    if rx > 0 and ry > 0:
        mx = st.mean(xs); my = st.mean(ys)
        cov = sum((a - mx) * (b - my) for a, b in zip(xs, ys)) / len(xs)
        pearson = cov / (rx * ry)
        print(f'  Pearson r (commit_phases(n-1) vs r2kth(n)): {pearson:.3f}')
    # spearman
    def rank(vals):
        order = sorted(range(len(vals)), key=lambda i: vals[i])
        ranks = [0] * len(vals)
        for r, i in enumerate(order):
            ranks[i] = r
        return ranks
    rxk = rank(xs); ryk = rank(ys)
    n = len(xs)
    d2 = sum((a - b) ** 2 for a, b in zip(rxk, ryk))
    spearman = 1 - 6 * d2 / (n * (n * n - 1)) if n > 1 else float('nan')
    print(f'  Spearman rho: {spearman:.3f}')
    # share where commit_phases(n-1) > 5ms (an outlier vs its own ~0.2ms median) coincides with r2kth in top tercile
    outliers = [i for i, x in enumerate(xs) if x > 5]
    print(f'  commit_phases(n-1) > 5ms outliers: {len(outliers)}/{len(xs)}')
    if outliers:
        thresh = sorted(ys)[int(len(ys) * 0.67)]
        hit = sum(1 for i in outliers if ys[i] >= thresh)
        print(f'  of those, r2kth(n) in the top third ({thresh:.0f}ms+): {hit}/{len(outliers)}')

# ===== PART B: size law =====
print()
print('=== PART B: r1kth/r2kth by tx bucket, and rank-correlation with write ms ===')
buckets = collections.OrderedDict([
    ('0', lambda t: t == 0),
    ('1-20k', lambda t: 1 <= t < 20000),
    ('20-80k', lambda t: 20000 <= t < 80000),
    ('80-140k', lambda t: 80000 <= t < 140000),
    ('>140k', lambda t: t >= 140000),
])
rows_by_bucket = {name: [] for name in buckets}
write_pairs_r1 = []
write_pairs_r2 = []
followerwrite_pairs_r2 = []
for (node, n), vt in vt_by_n.items():
    d = propose_all.get(n)
    if not d:
        continue
    txs = d['txs']
    for name, pred in buckets.items():
        if pred(txs):
            rows_by_bucket[name].append((vt['r1kth'], vt['r2kth']))
            break
    if vt['r2kth'] is not None:
        write_pairs_r2.append((txs, rec[n]['write'], vt['r2kth']))
    if vt['r1kth'] is not None:
        write_pairs_r1.append((txs, rec[n]['write'], vt['r1kth']))
    # follower write: average blockimport 'write' ms across the 6 non-leader nodes for THIS block
    fw = [blockimport[(F, n)]['write'] / 1e6 for F in NODES if F != node and (F, n) in blockimport]
    if fw and vt['r2kth'] is not None:
        followerwrite_pairs_r2.append((sum(fw) / len(fw), vt['r2kth']))

for name, rows in rows_by_bucket.items():
    r1s = [r[0] for r in rows]; r2s = [r[1] for r in rows]
    def stat(xs):
        xs = sorted(x for x in xs if x is not None)
        return f'n={len(xs)} med={st.median(xs):.0f}' if xs else 'n/a'
    print(f'  {name:8s} r1kth[{stat(r1s)}]  r2kth[{stat(r2s)}]')

def spearman(xs, ys):
    def rank(vals):
        order = sorted(range(len(vals)), key=lambda i: vals[i])
        ranks = [0] * len(vals)
        for r, i in enumerate(order):
            ranks[i] = r
        return ranks
    n = len(xs)
    if n < 3:
        return float('nan')
    rxk = rank(xs); ryk = rank(ys)
    d2 = sum((a - b) ** 2 for a, b in zip(rxk, ryk))
    return 1 - 6 * d2 / (n * (n * n - 1))

print(f'  rank-corr r2kth vs leader own blockwrite(write) ms: rho={spearman([p[1] for p in write_pairs_r2],[p[2] for p in write_pairs_r2]):.3f} n={len(write_pairs_r2)}')
print(f'  rank-corr r2kth vs mean-follower-write ms: rho={spearman([p[0] for p in followerwrite_pairs_r2],[p[1] for p in followerwrite_pairs_r2]):.3f} n={len(followerwrite_pairs_r2)}')

# ===== PART C: view timeout placement =====
print()
print('=== PART C: TC/timeout placement ===')
tc_events = set()
timeout_events = set()
fetch_lines = []
for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"TC formed locally' in line:
                d = json.loads(line)
                tc_events.add((d['time'], d.get('view')))
            elif 'view timed out' in line:
                d = json.loads(line) if line.strip().startswith('{') else None
                if d:
                    timeout_events.add((d.get('time'), d.get('view')))
            if 'fetch' in line.lower() or 'FetchBlockByHash' in line:
                fetch_lines.append(line)

print(f'distinct TC events (time,view): {len(tc_events)}')
for t, v in sorted(tc_events):
    leg = leg_of_time(t)
    print(f'  {t} view={v} leg={leg}')
print(f'distinct view-timed-out events: {len(timeout_events)}')
for t, v in sorted(timeout_events):
    leg = leg_of_time(t)
    print(f'  {t} view={v} leg={leg}')
print(f'lines mentioning "fetch" (any case) in the kept window: {len(fetch_lines)}')
