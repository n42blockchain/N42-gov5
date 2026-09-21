#!/usr/bin/env python3
"""S12b: test hypothesis H -- the held Round-2 commit vote (deferred
execution) is released by the END of the PARENT's import on the SAME
node, so the follower import chain (serial: import(v-1) before
import(v-2) is done is impossible) is the true pacemaker of the in-tenure
cycle, not a decoupled "vote/QC round-trip" as 6cb first read it.

Code path confirmed by reading, not assumed (internal/consensus/hotstuff/
proposal.go): onBlockImported(blockHash=hash of v-1) calls
castHeldCommitVoteIfAttested(pendingCommitQC.BlockHash) DIRECTLY and
synchronously (single ConsensusEngine goroutine, event-driven) --
proposal.go:517-547. So the moment a node's import of v-1 completes, if
block v was already "checked" (checkedBlocks[v]) and the pending
CommitQC is for v, the held vote fires right there. Under deferred
execution 87.4% of these lines carry "deferred":true (6cb, section 3).

Message/field map (same kept logs as 6ca/6cb, node{0-6}-B.log):
  "two-phase vote: casting held commit vote"   tMs, view, deferred, blockHash
      -- t_gate. No block number; converted via a view<->n offset
      calibrated from the QC-proxy join already used in 6cb (see below).
  "blockimport phases"                          tMs (END, after write --
      confirmed by reading blockchain.go:2472-2493: tMs is stamped after
      dWrite is computed), n, total (ns) -- t_parent_end for block n on
      that node, and the import-chain occupancy series.
  "miner: propose phases"                       tMs, n, total, write,
      assemble (as in 6cb) -- t_commit_start(v) for the hand-over check.
  "hotstuff: view changed" isLeader:true         tMs, view -- QC(v-1)
      proxy, reused from 6cb, and the view<->n calibration anchor.

Usage: parent_import_gate.py /data/blockchain/wr-logs/r35zzz-keep
"""
import sys, os, json, glob, statistics as st, collections

ROOT = sys.argv[1] if len(sys.argv) > 1 else '/data/blockchain/wr-logs/r35zzz-keep'
FULL_TXS = 150000
LEG_B1 = ('2026-09-21 00:10:41', '2026-09-21 00:24:14')
LEG_B2 = ('2026-09-21 00:24:14', '2026-09-21 00:37:45')
WIN_COUNTS = {'B1': (51, 46), 'B2': (50, None)}

def node_of(path):
    return os.path.basename(path).split('-')[0]

propose_all = {}       # n -> dict(node, tMs, txs, time, total, write, assemble, finalize)
blockimport = {}        # (node, n) -> dict(tMs, total, write, ...)
view_changed_leader = {}  # node -> [(tMs, view)]  isLeader==true only
vote_cast_raw = {}       # node -> [(tMs, view, deferred, blockHash)]
deferred_prep_raw = {}   # node -> [(tMs, view, blockHash)]  round-1 deferred-vote line

paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
if not paths:
    sys.exit('no node*-B.log files under ' + ROOT)
NODES = [node_of(p) for p in paths]

for path in paths:
    node = node_of(path)
    view_changed_leader.setdefault(node, [])
    vote_cast_raw.setdefault(node, [])
    deferred_prep_raw.setdefault(node, [])
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line)
                d['node'] = node
                propose_all[d['n']] = d
            elif '"msg":"blockimport phases"' in line:
                d = json.loads(line)
                blockimport[(node, d['n'])] = d
            elif '"msg":"hotstuff: view changed"' in line:
                d = json.loads(line)
                if d.get('isLeader'):
                    view_changed_leader[node].append((d['tMs'], d['view']))
            elif '"msg":"two-phase vote: casting held commit vote"' in line:
                d = json.loads(line)
                vote_cast_raw[node].append((d['tMs'], d['view'], d.get('deferred'), d.get('blockHash')))
            elif '"msg":"deferred vote: block checked and parent imported, voting"' in line:
                d = json.loads(line)
                if 'tMs' in d:
                    deferred_prep_raw[node].append((d['tMs'], d['view'], d.get('blockHash')))

for node in view_changed_leader:
    view_changed_leader[node].sort()
for node in vote_cast_raw:
    vote_cast_raw[node].sort()
for node in deferred_prep_raw:
    deferred_prep_raw[node].sort()

full_ns = sorted(n for n, d in propose_all.items() if d.get('txs', 0) >= FULL_TXS)
print(f'full blocks (txs>={FULL_TXS}): {len(full_ns)}')

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
print(f'three full windows combined: {len(full_windows)} blocks')

# ---- per-block leader fields (same formulas as 6cb) ----
rec = {}
for n, d in propose_all.items():
    tMs = d['tMs']; total = d['total']; write = d['write']; assemble = d['assemble']
    created_at = tMs - total / 1e6
    t_commit_start = created_at - assemble / 1e6
    push_instant = tMs - write / 1e6
    rec[n] = dict(node=d['node'], txs=d['txs'], time=d['time'], tMs=tMs,
                  t_commit_start=t_commit_start, push_instant=push_instant)

# ---- chained/handover rows + QC(v) proxy WITH its view number (for offset calibration) ----
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
    row = dict(n=n, leader=r1['node'], prev_leader=r0['node'], cycle=cycle, qc=qc, rec0=r0, rec1=r1)
    (chained if r1['node'] == r0['node'] else handover).append(row)

print(f'chained rows: {len(chained)}  handover rows: {len(handover)}')

# ---- calibrate view<->n offset per leg from the chained rows' QC proxy ----
offsets = {'B1': collections.Counter(), 'B2': collections.Counter()}
for row in chained:
    if row['qc'] is None:
        continue
    tMs, view = row['qc']
    leg = 'B1' if in_leg(row['n'], LEG_B1) else ('B2' if in_leg(row['n'], LEG_B2) else None)
    if leg is None:
        continue
    offsets[leg][view - row['n']] += 1

leg_offset = {}
for leg in ('B1', 'B2'):
    if not offsets[leg]:
        print(f'{leg}: no offset samples'); continue
    best, count = offsets[leg].most_common(1)[0]
    total = sum(offsets[leg].values())
    leg_offset[leg] = best
    print(f'{leg} view-n offset: {best} ({count}/{total} rows agree; others: {dict(offsets[leg])})')

def leg_of_time(t):
    if LEG_B1[0] <= t <= LEG_B1[1]:
        return 'B1'
    if LEG_B2[0] <= t <= LEG_B2[1]:
        return 'B2'
    return None

# ---- convert vote_cast_raw (view-keyed) into n-keyed, per node ----
vote_cast_by_n = {node: {} for node in NODES}
import datetime
EDT = datetime.timezone(datetime.timedelta(hours=-4))
for node, rows in vote_cast_raw.items():
    for tMs, view, deferred, blockHash in rows:
        # log "time" fields are America/New_York (EDT, UTC-4) this round;
        # tMs is a true Unix epoch ms -- convert with the same fixed offset
        # (verified against a real line: tMs 1789963801133 -> UTC 04:10:01
        # vs logged "2026-09-21 00:10:01").
        t_str = datetime.datetime.fromtimestamp(tMs / 1000, EDT).strftime('%Y-%m-%d %H:%M:%S')
        leg = leg_of_time(t_str)
        if leg is None or leg not in leg_offset:
            continue
        n = view - leg_offset[leg]
        vote_cast_by_n[node].setdefault(n, {})[bool(deferred)] = tMs

print(f'vote_cast_by_n coverage: {sum(len(v) for v in vote_cast_by_n.values())} (node,n) entries total')

def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p10 = xs[int(len(xs) * .1)]
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):4d} median={med:8.1f} p10={p10:8.1f} p90={p90:8.1f}')
    return med

# ===================== 1. d1 = t_gate - t_parent_end, in-tenure =====================
print()
print('=== 1. d1 = t_gate(v, deferred=True) - t_parent_end(v-1), in-tenure, all 6 followers of each full block ===')
d1_rows = []
missing_hold = 0
missing_parent = 0
total_slots = 0
for row in chained:
    v = row['n']; leader = row['leader']
    for F in NODES:
        if F == leader:
            continue
        total_slots += 1
        gate = vote_cast_by_n.get(F, {}).get(v, {})
        t_gate = gate.get(True)
        if t_gate is None:
            missing_hold += 1
            continue
        parent = blockimport.get((F, v - 1))
        if parent is None:
            missing_parent += 1
            continue
        t_parent_end = parent['tMs']
        d1_rows.append(dict(node=F, n=v, d1=t_gate - t_parent_end))

print(f'total (follower, full-block) slots: {total_slots}')
print(f'  no "casting held commit vote / deferred=true" line for this (node,n): {missing_hold} '
      f'({100*missing_hold/total_slots:.1f}%) -- likely cast via the immediate/non-held path (no wait)')
print(f'  held line present but no blockimport-phases(v-1) on that node: {missing_parent}')
print(f'  usable d1 rows: {len(d1_rows)}')
d1s = [r['d1'] for r in d1_rows]
pstats('  d1', d1s)
if d1s:
    n = len(d1s)
    band = lambda lo, hi: sum(1 for x in d1s if lo <= x <= hi) / n * 100
    print(f'  share 0<=d1<=20ms:  {band(0,20):.1f}%')
    print(f'  share d1>20ms:      {sum(1 for x in d1s if x>20)/n*100:.1f}%')
    print(f'  share d1<0ms:       {sum(1 for x in d1s if x<0)/n*100:.1f}%')
    print(f'  share -20<=d1<0ms:  {band(-20,-0.001):.1f}% (within one event-loop tick early)')

# ===================== 2. import chain saturation =====================
# IMPORTANT: computed per contiguous window (B1win1/B1win2/B2win1) --
# combining them would divide busy time by a "span" that includes the
# real, multi-minute gap between legs (funding ramp, decay warmup,
# drain), which crashes busy_fraction to a meaningless number.
print()
print('=== 2. follower import-chain saturation, per node, per contiguous window ===')
all_gaps = []
for win_name in ('B1win1', 'B1win2', 'B2win1'):
    win_ns = windows[win_name]
    print(f' -- {win_name} ({len(win_ns)} full blocks, {win_ns[0]}..{win_ns[-1]}) --')
    for F in NODES:
        entries = []
        for n in win_ns:
            d = blockimport.get((F, n))
            if d:
                end = d['tMs']; start = end - d['total'] / 1e6
                entries.append((n, start, end, d['total'] / 1e6))
        entries.sort()
        if len(entries) < 5:
            print(f'  {F}: too few entries ({len(entries)}), likely leader-heavy in this window')
            continue
        busy = sum(e[3] for e in entries)
        span = entries[-1][2] - entries[0][1]
        gaps = []
        for (n0, s0, e0, d0), (n1, s1, e1, d1_) in zip(entries, entries[1:]):
            if n1 == n0 + 1:
                gaps.append(s1 - e0)
        all_gaps.extend(gaps)
        print(f'  {F}: n={len(entries):3d} busy_fraction={100*busy/span:5.1f}%  '
              f'median_idle_gap={st.median(gaps) if gaps else float("nan"):7.1f} ms  '
              f'(n_consecutive_pairs={len(gaps)})')
print(f'pooled idle-gap median across all nodes/windows: {st.median(all_gaps):.1f} ms (n={len(all_gaps)})')

# ===================== 3. hand-over: build_prefix vs own parent-import end =====================
print()
print('=== 3. hand-over: new leader build_prefix vs its OWN import-end of the parent ===')
ho_rows = []
for row in handover:
    v = row['n']; new_leader = row['leader']
    r1 = row['rec1']
    parent = blockimport.get((new_leader, v - 1))
    if parent is None or row['qc'] is None:
        continue
    t_parent_end = parent['tMs']
    build_prefix = r1['t_commit_start'] - row['qc'][0]
    gap_to_own_import_end = r1['t_commit_start'] - t_parent_end
    ho_rows.append(dict(n=v, build_prefix=build_prefix, gap=gap_to_own_import_end))

print(f'hand-over rows with own-parent-import data: {len(ho_rows)} / {len(handover)}')
pstats('  build_prefix (QC-proxy to build start)', [r['build_prefix'] for r in ho_rows])
gaps = [r['gap'] for r in ho_rows]
pstats('  t_commit_start(v) - own blockimport-phases-end(v-1)', gaps)
if gaps:
    n = len(gaps)
    print(f'  share within 20ms of own parent-import end: {sum(1 for x in gaps if -20<=x<=20)/n*100:.1f}%')
    print(f'  share build starts AFTER own import end (gap>20ms, doing other work first): {sum(1 for x in gaps if x>20)/n*100:.1f}%')
    print(f'  share build starts BEFORE own import end (gap<-20ms, contradiction/other path): {sum(1 for x in gaps if x<-20)/n*100:.1f}%')

print()
print('=== summary numbers for the report ===')
print('in-tenure cycle median (6cb): 799.1 ms; follower import total median (6cb): 757.4 ms')
