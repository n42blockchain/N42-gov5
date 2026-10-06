#!/usr/bin/env python3
"""S15a: test hypothesis G -- the unconditional block-gossip fallback
(internal/blockchain.go:1758-1764) shares GossipSub's per-peer transport
with consensus messages and head-of-line-blocks PrepareQC/votes.

What this script CAN measure directly: r1kth/r2kth (leader-clock time
from round-start/PrepareQC-formed to the k-th vote's message arriving),
bucketed by the SAME view's own block size (txs) -- joined via the
view<->n offset calibration established in 6cb/6cc/6cd/6cg, but here
joined to block n directly (the view that PRODUCES n), not n-1, since
the question is "does a bigger block slow down ITS OWN round," not the
cycle-boundary framing 6cb/6cg used.

What it CANNOT measure: gossiped-copy-of-the-block arrival/validation
timing on the receiving side. internal/sync/subscriber_blocks.go's and
validate_blocks.go's own block-gossip log lines ("Subscriber received
new block", "Received block", "Block parent not yet available...") are
ALL log.Debug -- confirmed 0 occurrences in every one of the 7 kept
files at any level this round ran at. There is no gossip-block-arrival
timestamp in these logs to correlate against a vote's arrival; this
script reports that gap explicitly rather than approximating it.

Usage: gossip_headline.py <kept-logs-dir> [leg_b1_start leg_b1_end leg_b2_start leg_b2_end]
Positional overrides let this drive a different round (e.g. 35zzzb)
with its own leg boundaries -- same join/bucket logic, see
contention_attribution.py for the same convention.

  gossip_headline.py /data/blockchain/wr-logs/r35zzza-keep
  gossip_headline.py /data/blockchain/wr-logs/r35zzzb-keep \
      "2026-09-21 05:14:27" "2026-09-21 05:27:30" \
      "2026-09-21 05:27:30" "2026-09-21 05:40:47"
"""
import sys, os, json, glob, re, statistics as st, collections

ROOT = sys.argv[1] if len(sys.argv) > 1 else '/data/blockchain/wr-logs/r35zzza-keep'
if len(sys.argv) > 5:
    LEG_B1 = (sys.argv[2], sys.argv[3])
    LEG_B2 = (sys.argv[4], sys.argv[5])
else:
    LEG_B1 = ('2026-09-21 03:13:55', '2026-09-21 03:27:39')
    LEG_B2 = ('2026-09-21 03:27:39', '2026-09-21 03:41:01')
print(f'params: LEG_B1={LEG_B1} LEG_B2={LEG_B2}')

def node_of(p):
    return os.path.basename(p).split('-')[0]

paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
NODES = [node_of(p) for p in paths]

propose_all = {}
view_changed_leader = {n: [] for n in NODES}
vt_leader = []

LEADER_PAT = re.compile(
    r'view=(\d+) role=leader propose=(\d+)ms r1=(\d+)ms r2=(\d+)ms total=(\d+)ms'
    r'(?: votes=(\d+)/(\d+))?'
    r'(?: r1n=(\d+))?(?: r1lw=(\d+)ms)?(?: r1lwMax=(\d+)ms)?(?: r1wk=(\d+)ms)?'
    r'(?: r1kth=(\d+)ms)?(?: r1qk=(\d+)ms)?'
    r'(?: r2n=(\d+))?(?: r2lw=(\d+)ms)?(?: r2lwMax=(\d+)ms)?(?: r2wk=(\d+)ms)?'
    r'(?: r2kth=(\d+)ms)?(?: r2qk=(\d+)ms)?')

# --- 0. Confirm the gossip-side receive lines are absent (Debug-level gap) ---
debug_lines = ['Subscriber received new block', 'Block parent not yet available',
               'Received block with an invalid parent', 'Received block']
counts = collections.Counter()
for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"hotstuff: view changed"' in line and '"isLeader":true' in line:
                d = json.loads(line); view_changed_leader[node].append((d['tMs'], d['view']))
            elif '"msg":"hotstuff view timing' in line and 'role=leader' in line:
                m = LEADER_PAT.search(line)
                if m:
                    g = m.groups()
                    view = int(g[0])
                    newf = {k: (int(v) if v is not None else None) for k, v in zip(
                        ['r1n', 'r1lw', 'r1lwMax', 'r1wk', 'r1kth', 'r1qk',
                         'r2n', 'r2lw', 'r2lwMax', 'r2wk', 'r2kth', 'r2qk'], g[7:19])}
                    i = line.find('"time":"'); t = line[i + 8:i + 27]
                    vt_leader.append((node, t, view, newf))
            for dbg in debug_lines:
                if dbg in line:
                    counts[dbg] += 1
print('Gossip-block-receive Debug-level line counts across all kept files (expect 0, confirming the gap):')
for k in debug_lines:
    print(f'  {k!r}: {counts[k]}')

for n in view_changed_leader:
    view_changed_leader[n].sort()

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

# calibrate the view<->n offset per leg using full in-tenure chained rows (same method as 6cb-6cg)
chained_full = []
for n, d in propose_all.items():
    if d.get('txs', 0) < 150000:
        continue
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
        chained_full.append((n, qc))

offsets = {'B1': collections.Counter(), 'B2': collections.Counter()}
for n, (tMs, view) in chained_full:
    leg = 'B1' if in_leg(n, LEG_B1) else ('B2' if in_leg(n, LEG_B2) else None)
    if leg:
        offsets[leg][view - n] += 1
leg_offset = {leg: offsets[leg].most_common(1)[0][0] for leg in offsets if offsets[leg]}
print(f'\nleg offsets: {leg_offset} (agreement: { {l: offsets[l].most_common(2) for l in offsets} })')

vt_by_n = {}
for node, t, view, newf in vt_leader:
    leg = leg_of_time(t)
    if leg is None or leg not in leg_offset:
        continue
    n = view - leg_offset[leg]
    vt_by_n[(node, n)] = newf

# --- 3. Size discriminator: r1kth/r2kth bucketed by the SAME view's own block size ---
print('\n=== size discriminator: r1kth/r2kth of the view that PRODUCES block n, by n\'s own txs ===')
buckets = collections.OrderedDict([
    ('empty (0 tx)', lambda t: t == 0),
    ('small (1-999 tx)', lambda t: 1 <= t < 1000),
    ('mid (1,000-149,999 tx)', lambda t: 1000 <= t < 150000),
    ('full (>=150,000 tx)', lambda t: t >= 150000),
])
rows_by_bucket = {name: [] for name in buckets}
for (node, n), newf in vt_by_n.items():
    d = propose_all.get(n)
    if not d:
        continue
    txs = d['txs']
    for name, pred in buckets.items():
        if pred(txs):
            rows_by_bucket[name].append((newf.get('r1kth'), newf.get('r2kth')))
            break

def stat(xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        return 'n/a'
    return f'n={len(xs):5d} median={st.median(xs):6.0f} p90={xs[int(len(xs)*.9)]:6.0f}'

for name, rows in rows_by_bucket.items():
    r1s = [r[0] for r in rows]
    r2s = [r[1] for r in rows]
    print(f'  {name:26s} r1kth[{stat(r1s)}]  r2kth[{stat(r2s)}]')

# --- vote routing (direct/Rotor vs gossip fallback) ---
print('\n=== vote routing stats (direct Rotor vs mandatory gossip fallback), last sample per node ===')
for path in paths:
    node = node_of(path)
    last = None
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"hotstuff: vote routing stats"' in line:
                last = json.loads(line)
    if last:
        d, fb = last['direct'], last['fallback']
        print(f'  {node}: direct={d} fallback={fb} direct_share={100*d/(d+fb):.1f}%')
