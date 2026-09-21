#!/usr/bin/env python3
"""S13: test hypothesis D -- that delivering the ~26MB full block body from
the leader to the quorum-forming follower (plus that follower's
receive/decode) is the largest item on the in-tenure critical path.

Code reading first (internal/consensus/hotstuff/proposal.go,
internal/blockchain.go, internal/p2p/encoder/ssz.go -- see 6ce for full
citations): under two-phase voting (confirmed active this round via the
"two-phase vote: casting held commit vote" lines already used in
6cb/6cc/6cd), the Round-1 PREPARE vote (processProposal, proposal.go:226)
votes on "static validation alone" -- the leader's BLS signature and
JustifyQC -- and does NOT require the block body at all. Only the Round-2
COMMIT vote needs it (checkedBlocks/importedBlocks). So this script
measures block-BODY arrival ("block push: arrived") separately from the
protocol's own Delivery/ProposalReceived timing (which has no per-block
log line and is only visible in 6cb's aggregate "hotstuff view timing"
medians) -- they are different events on different messages.

Usage: delivery_budget.py /data/blockchain/wr-logs/r35zzz-keep
"""
import sys, os, json, glob, re, statistics as st, datetime, collections

ROOT = sys.argv[1] if len(sys.argv) > 1 else '/data/blockchain/wr-logs/r35zzz-keep'
FULL_TXS = 150000
LEG_B1 = ('2026-09-21 00:10:41', '2026-09-21 00:24:14')
LEG_B2 = ('2026-09-21 00:24:14', '2026-09-21 00:37:45')
WIN_COUNTS = {'B1': (51, 46), 'B2': (50, None)}

def node_of(path):
    return os.path.basename(path).split('-')[0]

propose_all = {}
blockimport = {}       # (node, n) -> dict
arrived = {}            # (node, number) -> tMs
pblock = {}              # (node, n) -> dict (parallel block: hintFills/hintHits/txs)
view_changed_leader = {}   # node -> [(tMs, view)] isLeader==true only
view_timing_leader = []     # (node, time, view, r1, r2, total, propose)

paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
NODES = [node_of(p) for p in paths]
for node in NODES:
    view_changed_leader[node] = []

VT_PAT = re.compile(r'view=(\d+) role=(\w+)(?: propose=(\d+)ms)?(?: recv=(\d+)ms)?'
                     r'(?: exec=(\d+)ms)? r1=(\d+)ms r2=(\d+)ms total=(\d+)ms')

for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"blockimport phases"' in line:
                d = json.loads(line); blockimport[(node, d['n'])] = d
            elif '"msg":"block push: arrived"' in line:
                d = json.loads(line); arrived[(node, d['number'])] = d['tMs']
            elif '"msg":"parallel block"' in line:
                d = json.loads(line); pblock[(node, d['n'])] = d
            elif '"msg":"hotstuff: view changed"' in line and '"isLeader":true' in line:
                d = json.loads(line); view_changed_leader[node].append((d['tMs'], d['view']))
            elif '"msg":"hotstuff view timing' in line and 'role=leader' in line:
                m = VT_PAT.search(line)
                if m:
                    view, role, propose_, recv, exec_, r1, r2, total = m.groups()
                    i = line.find('"time":"'); t = line[i + 8:i + 27]
                    view_timing_leader.append((node, t, int(view), int(r1), int(r2), int(total),
                                                int(propose_) if propose_ else None))
for node in NODES:
    view_changed_leader[node].sort()

full_ns = sorted(n for n, d in propose_all.items() if d.get('txs', 0) >= FULL_TXS)

def in_leg(n, leg):
    return leg[0] <= propose_all[n]['time'] <= leg[1]

windows = {}
for leg_name, leg in (('B1', LEG_B1), ('B2', LEG_B2)):
    ns_leg = sorted(n for n in full_ns if in_leg(n, leg))
    w1c, w2c = WIN_COUNTS[leg_name]
    windows[leg_name + 'win1'] = ns_leg[:w1c]
    if w2c is not None:
        windows[leg_name + 'win2'] = ns_leg[w1c:w1c + w2c]
full_windows = windows['B1win1'] + windows['B1win2'] + windows['B2win1']

rec = {}
for n, d in propose_all.items():
    tMs = d['tMs']; total = d['total']; write = d['write']; assemble = d['assemble']
    created_at = tMs - total / 1e6
    t_commit_start = created_at - assemble / 1e6
    push_instant = tMs - write / 1e6
    rec[n] = dict(node=d['node'], txs=d['txs'], time=d['time'], tMs=tMs,
                  t_commit_start=t_commit_start, push_instant=push_instant)

def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p10 = xs[int(len(xs) * .1)]
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):4d} median={med:8.1f} p10={p10:8.1f} p90={p90:8.1f}')
    return med

# ===================== 2. arrival offsets ranked, in-tenure full blocks =====================
# reconstruct chained/in-tenure the same way as prior sections (6cb/6cc/6cd),
# now also keeping the QC-proxy's own view number for the Round1/Round2 join below.
chained = []
for n in full_windows:
    if (n - 1) not in rec:
        continue
    r0, r1 = rec[n - 1], rec[n]
    if r0['node'] != r1['node']:
        continue  # in-tenure only
    cycle = r1['push_instant'] - r0['push_instant']
    if cycle <= 0 or cycle > 6000:
        continue
    qc = None
    for tMs, view in view_changed_leader.get(r1['node'], []):
        if tMs > r0['push_instant']:
            qc = (tMs, view)
            break
    chained.append((n, r1, r0, qc))

print(f'in-tenure full blocks usable: {len(chained)}')
print()
print('=== arrival offsets (block push: arrived - leader push_instant), ranked 1..6 of 6 followers ===')
rank_lists = [[] for _ in range(6)]
for n, r1, r0, qc in chained:
    leader = r1['node']
    offs = []
    for F in NODES:
        if F == leader:
            continue
        t = arrived.get((F, n))
        if t is not None:
            offs.append(t - r1['push_instant'])
    offs.sort()
    for i, v in enumerate(offs):
        if i < 6:
            rank_lists[i].append(v)

for i, xs in enumerate(rank_lists):
    label = f'rank {i+1}' + (' (quorum-forming, k=4th)' if i == 3 else '')
    pstats(f'  {label}', xs)

if rank_lists[0] and rank_lists[3]:
    spread = st.median(rank_lists[3]) - st.median(rank_lists[0])
    print(f'  spread median(rank4) - median(rank1) = {spread:.1f} ms')

# ===================== 4/5. follower-side breakdown + budget table =====================
print()
print('=== follower-side blockimport sub-fields, in-tenure full blocks (all 6 followers) ===')
hdr, body, recov, root = [], [], [], []
for n, r1, r0, qc in chained:
    leader = r1['node']
    for F in NODES:
        if F == leader:
            continue
        d = blockimport.get((F, n))
        if d:
            hdr.append(d.get('hdr', 0) / 1e6)
            body.append(d.get('body', 0) / 1e6)
            root.append(d.get('root', 0) / 1e6)
        pb = pblock.get((F, n))
        if pb:
            recov.append(pb.get('recoverMs', 0))
pstats('  hdr (wait on parallel VerifyHeaders/BLS seal result)', hdr)
pstats('  body (ValidateBody: recompute tx root over every tx)', body)
pstats('  root (state root #3, Finalize -> IntermediateRoot)', root)
pstats('  recov (parallel sender recovery, from "parallel block")', recov)

# ===================== 6. known-tx share (sender-hint cache fills) =====================
print()
print('=== 6. share of a block\'s txs whose sender was already cached (hintFills/txs), full blocks ===')
ratios = []
for n, r1, r0, qc in chained:
    leader = r1['node']
    for F in NODES:
        if F == leader:
            continue
        pb = pblock.get((F, n))
        if pb and pb.get('txs', 0) > 0:
            ratios.append(100.0 * pb.get('hintFills', 0) / pb['txs'])
pstats('  hintFills/txs %', ratios)

# ===================== 5. Round1/Round2 for the SAME 79 rows, budget table =====================
print()
print('=== 5. Round1/Round2 matched to the SAME rows q2s uses (view = (n-1) + leg offset) ===')
offsets = {'B1': collections.Counter(), 'B2': collections.Counter()}
for n, r1, r0, qc in chained:
    if qc is None:
        continue
    tMs, view = qc
    leg = 'B1' if in_leg(n, LEG_B1) else ('B2' if in_leg(n, LEG_B2) else None)
    if leg:
        offsets[leg][view - n] += 1
leg_offset = {leg: offsets[leg].most_common(1)[0][0] for leg in offsets if offsets[leg]}
print(f'  leg offsets: {leg_offset}')

def leg_of_time(t):
    if LEG_B1[0] <= t <= LEG_B1[1]:
        return 'B1'
    if LEG_B2[0] <= t <= LEG_B2[1]:
        return 'B2'
    return None

vt_by_n = {}
for node, t, view, r1v, r2v, totalv, proposev in view_timing_leader:
    leg = leg_of_time(t)
    if leg is None or leg not in leg_offset:
        continue
    vt_by_n[(node, view - leg_offset[leg])] = (r1v, r2v, totalv, proposev)

m_r1, m_r2, m_q2s = [], [], []
for n, r1, r0, qc in chained:
    if qc is None:
        continue
    got = vt_by_n.get((r1['node'], n - 1))  # view V(n-1)'s own Round1/Round2
    if got:
        r1v, r2v, totalv, proposev = got
        m_r1.append(r1v); m_r2.append(r2v)
        m_q2s.append(qc[0] - r0['push_instant'])

print(f'  matched rows: {len(m_r1)} / {len(chained)}')
med_r1 = pstats('  Round1 (matched)', m_r1)
med_r2 = pstats('  Round2 (matched)', m_r2)
med_q2s = pstats('  q2s, same rows', m_q2s)
if med_r1 is not None and med_r2 is not None and med_q2s:
    s = med_r1 + med_r2
    print(f'  Round1+Round2 = {s:.1f} ms vs q2s {med_q2s:.1f} ms => {100*s/med_q2s:.1f}%')
