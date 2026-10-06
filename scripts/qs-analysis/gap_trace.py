#!/usr/bin/env python3
"""S12c: split the two unexplained gaps 6cb/6cc left standing, using lines
that already exist in the kept logs (no new instrumentation).

Gap A (hand-over): the new leader's build starts a median 508 ms after its
own import of the outgoing leader's last block ends. Path traced by reading
(internal/sync/rpc_block_push.go, internal/consensus/hotstuff/service.go):

  InsertChain (blockimport phases logged inside, tMs = end incl. write)
    -> "block push: received" (tMs, rpc_block_push.go:66, right after
       InsertChain returns -- the tail of InsertChain not yet captured by
       "blockimport phases" own timestamp)
    -> blockApplied() check (no line; HasAppliedBlock, in-memory/marker read)
    -> NotifyBlockImported (service.go:591) -- if this hash matches a
       deferred production's parked parent, `go s.triggerBlockProduction`
       (service.go:1621, a NEW GOROUTINE -- scheduling delay, no line)
    -> triggerBlockProduction re-runs its 3 gates (service.go:355-397,
       "hotstuff: leader gate phases", no tMs -- durations only) and, on
       "trigger", calls blockProducer.TriggerBlockProduction
    -> miner: commitWork begin (worker.go:1096, no tMs, no block id)
    -> prefill (buildStallDiagEnabled only, logIfSlow >50ms,
       build_stall_watchdog.go:225-250 -- tMs when present)
    -> fillTransactions ("miner: parallel fill", no tMs, no block id)
    -> commit()/assemble (t_commit_start, derived as in 6cb/6cc)

Gap B (13.5% of held commit votes, ~134-149 ms after the parent's
import): same InsertChain -> "block push: received" tail, then
blockApplied() -> NotifyBlockImported -> retryDeferredChildren(parent
hash) -> deferredCheck(v) [-> "deferred check: block passes..." if this
is what flips checkedBlocks[v] true] -> onBlockImported also fires
castHeldCommitVoteIfAttested(pendingCommitQC.BlockHash) directly
(proposal.go:517-547) -- same goroutine, same event, no queue between
import and this call by inspection of the code.

Usage: gap_trace.py /data/blockchain/wr-logs/r35zzz-keep
"""
import sys, os, json, glob, statistics as st, datetime

ROOT = sys.argv[1] if len(sys.argv) > 1 else '/data/blockchain/wr-logs/r35zzz-keep'
FULL_TXS = 150000
LEG_B1 = ('2026-09-21 00:10:41', '2026-09-21 00:24:14')
LEG_B2 = ('2026-09-21 00:24:14', '2026-09-21 00:37:45')
WIN_COUNTS = {'B1': (51, 46), 'B2': (50, None)}
EDT = datetime.timezone(datetime.timedelta(hours=-4))

def node_of(path):
    return os.path.basename(path).split('-')[0]

propose_all = {}
blockimport = {}          # (node, n) -> dict
push_received = {}        # (node, number) -> tMs
deferred_pass = {}        # (node, number) -> [tMs,...] (may fire >1x)
prefill = {}               # (node, n) -> dict
vote_cast_raw = {}         # node -> [(tMs, view, deferred, blockHash)]

paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
NODES = [node_of(p) for p in paths]

for path in paths:
    node = node_of(path)
    vote_cast_raw.setdefault(node, [])
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"blockimport phases"' in line:
                d = json.loads(line); blockimport[(node, d['n'])] = d
            elif '"msg":"block push: received"' in line:
                d = json.loads(line); push_received[(node, d['number'])] = d['tMs']
            elif '"msg":"deferred check: block passes, vote may proceed before its import"' in line:
                d = json.loads(line)
                deferred_pass.setdefault((node, d['number']), []).append(d['tMs'])
            elif '"msg":"miner: prefill phases"' in line:
                d = json.loads(line); prefill[(node, d['n'])] = d
            elif '"msg":"two-phase vote: casting held commit vote"' in line:
                d = json.loads(line)
                vote_cast_raw[node].append((d['tMs'], d['view'], d.get('deferred'), d.get('blockHash')))
            elif '"msg":"hotstuff: view changed"' in line:
                d = json.loads(line)
                if d.get('isLeader'):
                    pass  # calibrated separately below via propose_all-only method

for node in vote_cast_raw:
    vote_cast_raw[node].sort()

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

# ---- QC(v) proxy, recomputed here (need view_changed_leader separately) ----
view_changed_leader = {node: [] for node in NODES}
for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"hotstuff: view changed"' in line and '"isLeader":true' in line:
                d = json.loads(line)
                view_changed_leader[node].append((d['tMs'], d['view']))
for node in view_changed_leader:
    view_changed_leader[node].sort()

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

def leg_of_time(t):
    if LEG_B1[0] <= t <= LEG_B1[1]:
        return 'B1'
    if LEG_B2[0] <= t <= LEG_B2[1]:
        return 'B2'
    return None

import collections
offsets = {'B1': collections.Counter(), 'B2': collections.Counter()}
for row in chained:
    if row['qc'] is None:
        continue
    tMs, view = row['qc']
    leg = 'B1' if in_leg(row['n'], LEG_B1) else ('B2' if in_leg(row['n'], LEG_B2) else None)
    if leg:
        offsets[leg][view - row['n']] += 1
leg_offset = {leg: offsets[leg].most_common(1)[0][0] for leg in offsets if offsets[leg]}

vote_cast_by_n = {node: {} for node in NODES}
for node, rows in vote_cast_raw.items():
    for tMs, view, deferred, blockHash in rows:
        t_str = datetime.datetime.fromtimestamp(tMs / 1000, EDT).strftime('%Y-%m-%d %H:%M:%S')
        leg = leg_of_time(t_str)
        if leg is None or leg not in leg_offset:
            continue
        n = view - leg_offset[leg]
        vote_cast_by_n[node].setdefault(n, {})[bool(deferred)] = tMs

def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p10 = xs[int(len(xs) * .1)]
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):4d} median={med:8.1f} p10={p10:8.1f} p90={p90:8.1f}')
    return med

print(f'chained={len(chained)} handover={len(handover)}')

# ===================== GAP A =====================
print()
print('=== GAP A: hand-over timeline, new leader L, block v, parent v-1 ===')
seg1 = []  # t_parent_end -> t_push_received (InsertChain tail)
seg_total = []  # t_parent_end -> t_commit_start(v) (the whole 508ms gap, cross-check vs 6cc)
prefill_hits = 0
seg_prefill_end_to_commit = []  # prefill(v) end -> t_commit_start(v) (= fillTransactions itself)
seg_pushrecv_to_prefillstart = []  # push_received(v-1) -> prefill(v) buildStart (if present)
for row in handover:
    v = row['n']; L = row['leader']
    parent = blockimport.get((L, v - 1))
    if not parent:
        continue
    t_parent_end = parent['tMs']
    t_pr = push_received.get((L, v - 1))
    if t_pr is not None:
        seg1.append(t_pr - t_parent_end)
    t_commit_start = row['rec1']['t_commit_start']
    seg_total.append(t_commit_start - t_parent_end)
    pf = prefill.get((L, v))
    if pf:
        prefill_hits += 1
        pf_end = pf['tMs']
        pf_start = pf_end - pf['total'] / 1e6
        seg_prefill_end_to_commit.append(t_commit_start - pf_end)
        if t_pr is not None:
            seg_pushrecv_to_prefillstart.append(pf_start - t_pr)

print(f'hand-over rows: {len(handover)}; with own-parent blockimport line: '
      f'{sum(1 for row in handover if blockimport.get((row["leader"], row["n"]-1)))}')
pstats('  t_parent_end -> block-push:received (InsertChain tail)', seg1)
pstats('  t_parent_end -> t_commit_start(v) (whole gap, cross-check vs 6cc 508ms)', seg_total)
print(f'  hand-over blocks with a matching "miner: prefill phases" line: {prefill_hits}/{len(handover)}')
prefill_totals = [prefill[(row['leader'], row['n'])]['total'] / 1e6
                   for row in handover if (row['leader'], row['n']) in prefill]
pstats('  prefill itself, direct (buildStart -> prefill end, own "total" field)', prefill_totals)
if seg_prefill_end_to_commit:
    pstats('  prefill(v) end -> t_commit_start(v) (= fillTransactions itself)', seg_prefill_end_to_commit)
if seg_pushrecv_to_prefillstart:
    pstats('  block-push:received(v-1) -> prefill(v) buildStart (gates+dispatch+queue, when both present)', seg_pushrecv_to_prefillstart)

# ===================== GAP B =====================
print()
print('=== GAP B: held commit-vote release relative to the chain-layer tail, in-tenure ===')
d1_full = []       # t_parent_end -> t_gate (same as 6cc)
d1_tail = []        # push_received(v-1) -> t_gate (removing the InsertChain-tail segment)
checked_before_import = 0
checked_after_import = 0
checked_gap = []
gate_minus_checked = []
for row in chained:
    v = row['n']; leader = row['leader']
    for F in NODES:
        if F == leader:
            continue
        gate = vote_cast_by_n.get(F, {}).get(v, {})
        t_gate = gate.get(True)
        if t_gate is None:
            continue
        parent = blockimport.get((F, v - 1))
        if parent is None:
            continue
        t_parent_end = parent['tMs']
        d1_full.append(t_gate - t_parent_end)
        t_pr = push_received.get((F, v - 1))
        if t_pr is not None:
            d1_tail.append(t_gate - t_pr)
        checks = deferred_pass.get((F, v))
        if checks:
            t_checked = min(checks)  # first time this node's check of v passed
            checked_gap.append(t_checked - t_parent_end)
            gate_minus_checked.append(t_gate - t_checked)
            if t_checked < t_parent_end:
                checked_before_import += 1
            else:
                checked_after_import += 1

pstats('  t_parent_end -> t_gate (full, matches 6cc)', d1_full)
pstats('  block-push:received(parent) -> t_gate (InsertChain tail removed)', d1_tail)
print(f'  rows with a "deferred check: block passes" line for v on the same node: {len(checked_gap)}')
print(f'  of those: check completed BEFORE parent import end: {checked_before_import}, '
      f'AFTER: {checked_after_import}')
pstats('  t_checked(v) - t_parent_end(v-1) (negative = v was already checked before its parent even imported)', checked_gap)
pstats('  t_gate - t_checked(v) (should be ~0 if release is synchronous with the check passing)', gate_minus_checked)
