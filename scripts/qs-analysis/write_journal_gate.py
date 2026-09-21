#!/usr/bin/env python3
"""S21: tests hypothesis W (commander's ruling on S19/S20 6cq/6cr) -- does
push(v+1) wait on max(write(v) end, CommitQC(v) formed) + a constant, or is
the cycle set by something else entirely (the concurrent speculative build of
v+1, per proposal.go:132-142/2366-2392)?

Corrects the push-instant formula the whole campaign has used since 6cb:
push_instant = tMs - write/1e6 is only valid when lwWait=0 (true before S19).
With N42_LEADER_WRITE_AFTER_JOURNAL=1 (B2), push happens BEFORE lwWait and
write (worker.go:677 push, ~747-757 lwWait, 759-761 write), so the correct
formula is push_instant = tMs - (write + lwWait + notify)/1e6. Reusing the
uncorrected formula silently computes writeStart (B1) but writeStart+lwWait
(B2) -- a block-by-block bias that happens to roughly cancel in a same-leg
cycle subtraction (both endpoints share a similar lwWait) but corrupts any
CROSS-quantity comparison such as this task's own gate test.

Usage: write_journal_gate.py <kept-logs-dir> leg_b1_start leg_b1_end
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
vt_leader_lines = []
build_phases = {n: [] for n in NODES}   # (time, align, persistWait, reload, syscalls, fillTx, total)
commit_phases_spec = {n: [] for n in NODES}  # (time, speculative, commitNs)
spec_parked = {n: [] for n in NODES}    # (time, number, txs)
spec_hit = {n: [] for n in NODES}       # (time, number)

for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"hotstuff: view changed"' in line and '"isLeader":true' in line:
                d = json.loads(line); view_changed_leader[node].append((d['tMs'], d['view']))
            elif '"msg":"hotstuff view timing' in line and 'role=leader' in line:
                d = json.loads(line); vt_leader_lines.append((node, d['time'], d['msg']))
            elif '"msg":"miner: build phases"' in line:
                d = json.loads(line)
                build_phases[node].append((d['time'], d.get('align', 0) / 1e6, d.get('persistWait', 0) / 1e6,
                                            d.get('reload', 0) / 1e6, d.get('syscalls', 0) / 1e6,
                                            d.get('fillTx', 0) / 1e6, d.get('total', 0) / 1e6))
            elif '"msg":"miner: commit phases"' in line and '"speculative"' in line:
                d = json.loads(line)
                commit_phases_spec[node].append((d['time'], d.get('speculative'), d.get('commitNs', 0) / 1e6))
            elif '"msg":"miner: speculative build parked"' in line:
                d = json.loads(line); spec_parked[node].append((d['time'], d.get('number'), d.get('txs')))
            elif '"msg":"miner: speculative build hit"' in line:
                d = json.loads(line); spec_hit[node].append((d['time'], d.get('number')))

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
    tMs = d['tMs']
    write_ms = d['write'] / 1e6
    lw_ms = d.get('lwWait', 0) / 1e6
    notify_ms = d.get('notify', 0) / 1e6
    write_end = tMs - notify_ms                  # write finishes ~here (notify negligible when proposedEarly)
    write_start = write_end - write_ms
    push_instant_OLD = tMs - write_ms             # the formula every prior section used
    push_instant_NEW = write_start - lw_ms        # corrected: push happens BEFORE lwWait and write
    rec[n] = dict(node=d['node'], txs=d['txs'], time=d['time'], tMs=tMs,
                  write_start=write_start, write_end=write_end,
                  push_OLD=push_instant_OLD, push_NEW=push_instant_NEW,
                  lwWait=lw_ms, write=write_ms)

full_ns = sorted(n for n, d in propose_all.items() if d.get('txs', 0) >= FULL_TXS)
windows = {}
for leg_name, leg in (('B1', LEG_B1), ('B2', LEG_B2)):
    ns_leg = sorted(n for n in full_ns if in_leg(n, leg))
    w1c, w2c = WIN_COUNTS[leg_name]
    windows[leg_name + 'win1'] = ns_leg[:w1c]
    windows[leg_name + 'win2'] = ns_leg[w1c:w1c + w2c]
full_windows = sum((windows[w] for w in FULL_WIN_NAMES), [])
print(f'full windows {FULL_WIN_NAMES}: {len(full_windows)} blocks')


def leg_win_of_n(n):
    for w, ns in windows.items():
        if n in ns:
            return w
    return None


chained = []
for n in full_windows:
    if (n - 1) not in rec:
        continue
    r0, r1 = rec[n - 1], rec[n]
    if r0['node'] != r1['node']:
        continue
    cycle_new = r1['push_NEW'] - r0['push_NEW']
    if cycle_new <= 0 or cycle_new > 6000:
        continue
    qc = None
    for tMs, view in view_changed_leader.get(r1['node'], []):
        if tMs > r0['push_NEW']:
            qc = (tMs, view)
            break
    if qc:
        chained.append(dict(n=n, leader=r1['node'], qc=qc, cycle_new=cycle_new,
                             cycle_old=r1['push_OLD'] - r0['push_OLD']))
print(f'in-tenure (chained) full blocks usable: {len(chained)}')

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


vt_leader_by_n = {}
for node, t, msg in vt_leader_lines:
    n = n_of_view(view_of(msg), t)
    if n is not None:
        vt_leader_by_n[(node, n)] = msg


def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p10 = xs[int(len(xs) * .1)]
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):5d} median={med:8.1f} p10={p10:8.1f} p90={p90:8.1f}')
    return med


# ===== Sanity: does the OLD (uncorrected) push_instant formula matter for the cycle? =====
print()
print('=== sanity: OLD vs NEW cycle formula, win1 only, by leg ===')
for leg in ('B1', 'B2'):
    rows = [r for r in chained if leg_win_of_n(r['n']) == leg + 'win1']
    pstats(f'  {leg}win1 cycle (OLD formula, uncorrected)', [r['cycle_old'] for r in rows])
    pstats(f'  {leg}win1 cycle (NEW formula, lwWait-corrected)', [r['cycle_new'] for r in rows])

# ===== Core test: gate = max(write(v) end, CommitQC(v) formed) =====
#
# CommitQC(v) is derived from the SAME per-view instrumentation the rest of
# the campaign trusts (view_timing.go's own "propose" field: ViewStart(v+1)
# -> ProposalSent(v+1)) rather than the "hotstuff: view changed" isLeader
# search used for leg-offset calibration above. A direct cross-check found
# the isLeader-search proxy for CommitQC(v) disagrees with push(v+1) -
# propose(v+1) by 300-400ms specifically in B2 (self-consistency check:
# CommitQC(v)-push(v) should equal r1(v)+r2(v); via propose(v+1) it does,
# within noise, in BOTH legs; via the isLeader search it does only in B1) --
# so this section uses CommitQC(v) := push(v+1) - propose(v+1), which is
# self-consistent with r1+r2 in both legs and needs no extra join.
print()
print('=== JOB (item 2): push(v+1) - gate, and push(v+1) - CommitQC(v), win1 only, by leg ===')
gate_rows = collections.defaultdict(list)
for row in chained:
    n = row['n']
    win = leg_win_of_n(n)
    if win is None or not win.endswith('win1'):
        continue
    leg = win[:2]
    leader = row['leader']
    lm_vp1 = vt_leader_by_n.get((leader, n))
    if not lm_vp1:
        continue
    propose_vp1 = ms_field(lm_vp1, 'propose')
    if propose_vp1 is None:
        continue
    r_v = rec[n - 1]       # block v = n-1
    r_vp1 = rec[n]         # block v+1 = n
    push_vp1 = r_vp1['push_NEW']
    commitqc_v = push_vp1 - propose_vp1
    write_v_end = r_v['write_end']
    gate = max(write_v_end, commitqc_v)
    gate_rows[leg].append(dict(
        n=n, push_vp1=push_vp1, commitqc_v=commitqc_v, write_v_end=write_v_end, write_v_start=r_v['write_start'],
        propose_vp1=propose_vp1, gate=gate, gate_is_write=write_v_end >= commitqc_v,
        d_gate=push_vp1 - gate, d_qc=push_vp1 - commitqc_v, d_write=push_vp1 - write_v_end,
    ))

for leg in ('B1', 'B2'):
    rows = gate_rows[leg]
    print(f'-- {leg}win1 (n={len(rows)}) --')
    share_write_gates = 100 * sum(1 for r in rows if r['gate_is_write']) / len(rows) if rows else 0
    print(f'  share of views where write(v)_end >= CommitQC(v) (write is the later/gating one): {share_write_gates:.1f}%')
    pstats('  push(v+1) - gate=max(write_end,CommitQC)', [r['d_gate'] for r in rows])
    pstats('  push(v+1) - CommitQC(v)', [r['d_qc'] for r in rows])
    pstats('  push(v+1) - write(v)_end', [r['d_write'] for r in rows])

# ===== First leader-side step to start within 10ms of write(v) end, B2 =====
print()
print('=== which leader-side event is first to start within 10ms of write(v) end (B2 win1) ===')
# Available ms-precision candidates on the leader-side critical path per view:
# CommitQC(v) [qc tMs], and push(v+1) itself. commitWork begin / build phases /
# speculative build parked all lack tMs in this round's binary (checked: not
# present on those log lines) -- see Method/caveat below.
for leg in ('B1', 'B2'):
    rows = gate_rows[leg]
    near_qc = sum(1 for r in rows if abs(r['commitqc_v'] - r['write_v_end']) <= 10)
    print(f'  {leg}: CommitQC(v) within 10ms of write(v)_end: {near_qc}/{len(rows)} '
          f'({100*near_qc/len(rows):.1f}%)' if rows else f'  {leg}: n=0')

# ===== Build-phase / speculative-build duration characterization (2nd-resolution only) =====
print()
print('=== build-phase durations by speculative flag, joined by log ORDER (build+commit phases')
print('    are logged adjacently per commitWork call; no per-block number or tMs on either line')
print('    in this round\'s binary -- durations are exact, PLACEMENT is second-resolution only) ===')
spec_big = {'B1': [], 'B2': []}
spec_small = {'B1': [], 'B2': []}
fresh_all = {'B1': [], 'B2': []}
for node in NODES:
    b = build_phases[node]
    c = commit_phases_spec[node]
    m = min(len(b), len(c))
    for i in range(m):
        t, align, pw, reload_, sysc, filltx, total = b[i]
        _, spec, commitns = c[i]
        leg = leg_of_time(t)
        if leg is None:
            continue
        if spec:
            (spec_big if filltx > 400 else spec_small)[leg].append(total)
        else:
            fresh_all[leg].append(total)
for leg in ('B1', 'B2'):
    pstats(f'  {leg} speculative build TOTAL, fillTx>400ms subset (proxy for full blocks)', spec_big[leg])
    pstats(f'  {leg} speculative build TOTAL, fillTx<=400ms subset (proxy for empty/small blocks)', spec_small[leg])
    pstats(f'  {leg} fresh(non-speculative) build TOTAL (any size)', fresh_all[leg])

print()
print('=== speculative build outcome counts (all sizes, whole trimmed window) ===')
for node in NODES:
    print(f'  {node}: parked={len(spec_parked[node])} hit={len(spec_hit[node])}')
