#!/usr/bin/env python3
"""S12: reconstruct the per-block critical path for FULL blocks (txs>=150000)
across the seven-node fleet, from the kept round-35zzz B-leg logs.

n42-r86 (this round's binary) predates commits 89d15267/b97ca94e: "miner:
build triggered (leader view)", "miner: build phases", and the speculative
park/hit lines carry no "tMs" here (second-resolution "time" only), so the
leader's trigger/prefill/fill prefix cannot be placed at millisecond
resolution from those messages in this round. Two lines that DO carry tMs
give an equivalent, higher-resolution anchor instead:

  "miner: propose phases"   tMs = the instant AFTER WriteBlockWithState.
      task.createdAt = tMs - total/1e6   (total = time.Since(createdAt);
      createdAt is set when commit() hands the task to the sealer, i.e. the
      end of fillTransactions/commit() -- see worker.go:818,2349,2364).
      assemble = the whole commit() call (tCommitStart -> createdAt), so
      tCommitStart = createdAt - assemble/1e6 is the end of fill / start of
      assemble+finalize, in epoch ms, for EVERY full block (not just the
      ~5% that cross the prefill-phases 50ms cutoff).
      push_instant = tMs - write/1e6 (push happens before write under
      N42_PUSH_BEFORE_WRITE/N42_PROPOSE_BEFORE_WRITE; this is the existing
      convention from cycle.py/leg_compare.py's "seal" column, reused here
      under its real name).
  "blockimport phases"      tMs = end of a follower's import (after its own
      write); total = the import's own elapsed time, so its start is
      tMs - total/1e6.

Method notes (see docs/QS_BLOCK_TIME_BUDGET.md S12 section for the prose):
  - "full" block: txs >= 150000 on its "miner: propose phases" line.
  - leader(n) = the one kept file whose "miner: propose phases" carries n.
  - window carving: the three full windows (B1win1, B1win2, B2win1) are
    recovered from the kept logs alone, not from timestamps the round log
    never prints: B1's full blocks in chronological order, first 51 =
    win1, next 46 = win2 (counts read off r35zzz.log); B2's full blocks,
    first 50 = win1 (B2win2 -- 34.8% occupancy -- is excluded even if a
    stray block there clears 150000).
  - chained (in-tenure) vs handover: consecutive full n, n+1 with the same
    leader vs a different one.
  - QC(v) proxy (same convention as cycle.py/leader_gap.py): the leader's
    own next "hotstuff: view changed" (isLeader=true) strictly after
    push_instant(v).
  - quorum-forming vote: 7 validators, f=2, quorum = n-f = 5
    (internal/consensus/hotstuff/validator.go:67-75, confirmed by
    validator_quorum_test.go's {7,2,5} case). The leader self-votes at
    propose time (proposal.go:106-107), so the quorum needs 4 MORE votes
    from the 6 followers: the "5th of 7" is the 4th-fastest of 6 followers'
    votes. Follower "vote ready" is approximated by blockimport-phases end
    (import completion), since the two-phase-vote "casting held commit
    vote" line carries no block number and only ~4% as many lines as
    imports (most votes are not "held"); a best-effort nearest-following
    join to that message is reported separately as a spot check, not as
    the primary per-block series.

Usage: full_block_critical_path.py /data/blockchain/wr-logs/r35zzz-keep
"""
import sys, os, json, glob, statistics as st

ROOT = sys.argv[1] if len(sys.argv) > 1 else '/data/blockchain/wr-logs/r35zzz-keep'
FULL_TXS = 150000

LEG_B1 = ('2026-09-21 00:10:41', '2026-09-21 00:24:14')
LEG_B2 = ('2026-09-21 00:24:14', '2026-09-21 00:37:45')
WIN_COUNTS = {'B1': (51, 46), 'B2': (50, None)}  # (win1 count, win2 count or None)


def node_of(path):
    return os.path.basename(path).split('-')[0]


propose = {}       # n -> dict(node=, tMs=, txs=, fields..., time=)
blockimport = {}    # (node, n) -> dict(tMs=, total=, ...)
pblock = {}          # (node, n) -> dict(recoverMs, execMs, finalizeMs)
arrived = {}         # (node, n) -> tMs
view_changed = {}    # node -> list of (tMs, view, isLeader)
vote_cast = {}       # node -> list of (tMs, view, deferred)
view_timing_lines = []  # (node, time, text)

paths = sorted(glob.glob(os.path.join(ROOT, 'node*-B.log')))
if not paths:
    sys.exit('no node*-B.log files found under ' + ROOT)

propose_all = {}    # n -> dict, EVERY proposal regardless of size (predecessor lookups)

for path in paths:
    node = node_of(path)
    view_changed.setdefault(node, [])
    vote_cast.setdefault(node, [])
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line)
                d['node'] = node
                propose_all[d['n']] = d
                if d.get('txs', 0) >= FULL_TXS:
                    propose[d['n']] = d
            elif '"msg":"blockimport phases"' in line:
                d = json.loads(line)
                blockimport[(node, d['n'])] = d
            elif '"msg":"parallel block"' in line:
                d = json.loads(line)
                pblock[(node, d['n'])] = d
            elif '"msg":"block push: arrived"' in line:
                d = json.loads(line)
                arrived[(node, d['number'])] = d['tMs']
            elif '"msg":"hotstuff: view changed"' in line:
                d = json.loads(line)
                if d.get('isLeader'):
                    view_changed[node].append((d['tMs'], d['view'], True))
            elif '"msg":"two-phase vote: casting held commit vote"' in line:
                d = json.loads(line)
                vote_cast[node].append((d['tMs'], d['view'], d.get('deferred')))
            elif '"msg":"hotstuff view timing' in line:
                i = line.find('"time":"')
                t = line[i + 8:i + 27]
                view_timing_lines.append((node, t, line[line.find('"msg":"') + 7:].split('"')[0]))

for node in view_changed:
    view_changed[node].sort()
for node in vote_cast:
    vote_cast[node].sort()

full_ns = sorted(propose)
print(f'full blocks (txs>={FULL_TXS}): {len(full_ns)}  n range {full_ns[0]}..{full_ns[-1]}')

# ---- window carving (see module docstring) ----
def in_leg(n, leg):
    return leg[0] <= propose[n]['time'] <= leg[1]

windows = {}
for leg_name, leg in (('B1', LEG_B1), ('B2', LEG_B2)):
    ns_leg = sorted(n for n in full_ns if in_leg(n, leg))
    w1c, w2c = WIN_COUNTS[leg_name]
    windows[leg_name + 'win1'] = ns_leg[:w1c]
    if w2c is not None:
        windows[leg_name + 'win2'] = ns_leg[w1c:w1c + w2c]
    print(f'  {leg_name}: {len(ns_leg)} full blocks in leg time range; '
          f'win1={len(windows[leg_name+"win1"])}(want {w1c})',
          (f'win2={len(windows[leg_name+"win2"])}(want {w2c})' if w2c else '(win2 not used)'))

full_windows = windows['B1win1'] + windows['B1win2'] + windows['B2win1']
full_windows_set = set(full_windows)
print(f'three full windows combined: {len(full_windows)} blocks')

# ---- derived per-block leader fields (computed for EVERY proposal, any
# size, so a full block's predecessor need not itself be full) ----
rec = {}
for n, d in propose_all.items():
    tMs = d['tMs']
    total = d['total']
    write = d['write']
    push = d['push']
    notify = d['notify']
    bls = d['bls']
    assemble = d['assemble']
    finalize = d['finalize']
    seal2res = d['seal2res']
    created_at = tMs - total / 1e6
    t_commit_start = created_at - assemble / 1e6
    push_instant = tMs - write / 1e6
    residual = total - (bls + push + notify + write)  # gate-check + receipts-copy, ns
    rec[n] = dict(node=d['node'], txs=d['txs'], tMs=tMs, created_at=created_at,
                  t_commit_start=t_commit_start, push_instant=push_instant,
                  assemble=assemble, finalize=finalize, bls=bls, push=push,
                  notify=notify, write=write, total=total, seal2res=seal2res,
                  residual=residual)

# ---- chained vs handover pairs: for every FULL block v (n in full_windows),
# pair it with its immediate predecessor v-1, WHATEVER SIZE v-1 was (most
# predecessors are themselves full given ~97% occupancy, but requiring it
# would bias the handover/chained mix -- see module note below). ----
pred_size_not_full = 0
chained, handover = [], []
for n in full_windows:
    if (n - 1) not in rec:
        continue
    r0, r1 = rec[n - 1], rec[n]
    if r0['txs'] < FULL_TXS:
        pred_size_not_full += 1
    cycle = r1['push_instant'] - r0['push_instant']
    if cycle <= 0 or cycle > 6000:
        continue
    qc = None
    for tMs, view, is_leader in view_changed.get(r1['node'], []):
        if tMs > r0['push_instant']:
            qc = tMs
            break
    # trigger+prefill+fill: from QC(v) [view-start proxy for v+1] to when
    # commit()/assemble begins (end of fillTransactions). NOT anchored to
    # push_instant(v) directly -- ViewStart(v+1) does not begin until QC(v)
    # forms, so push_instant(v)->QC(v) (q2s) and QC(v)->t_commit_start(v+1)
    # (build_prefix) are the two components of the "lump", not one.
    build_prefix = (r1['t_commit_start'] - qc) if qc else None
    row = dict(n=n, leader=r1['node'], prev_leader=r0['node'], cycle=cycle,
               build_prefix=build_prefix, qc=qc,
               q2s=(qc - r0['push_instant']) if qc else None,
               s2q=(r1['push_instant'] - qc) if qc else None,
               rec1=r1, rec0=r0)
    (chained if r1['node'] == r0['node'] else handover).append(row)

print(f'predecessor-not-full (predecessor < {FULL_TXS} txs, still paired): {pred_size_not_full}')

def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):4d} median={med:8.1f} p90={p90:8.1f}')
    return med

print()
print('=== cycle time (push-instant to push-instant), ms ===')
pstats('all       ', [r['cycle'] for r in chained + handover])
pstats('in-tenure ', [r['cycle'] for r in chained])
pstats('hand-over ', [r['cycle'] for r in handover])
print(f'  in-tenure fraction: {len(chained)}/{len(chained)+len(handover)} = '
      f'{100*len(chained)/(len(chained)+len(handover)):.1f}%')

print()
print('=== leader-side build_prefix: t_commit_start(v) - QC(v-1 proxy), ms (trigger+prefill+fill, unresolved sub-split) ===')
pstats('in-tenure build_prefix', [r['build_prefix'] for r in chained])
pstats('hand-over build_prefix', [r['build_prefix'] for r in handover])

print()
print('=== push_instant(v) -> QC(v) [q2s] and QC(v) -> push_instant(v+1) [s2q], ms ===')
pstats('in-tenure q2s (push->QC)     ', [r['q2s'] for r in chained])
pstats('in-tenure s2q (QC->next push)', [r['s2q'] for r in chained])
pstats('hand-over q2s (push->QC)     ', [r['q2s'] for r in handover])
pstats('hand-over s2q (QC->next push)', [r['s2q'] for r in handover])

print()
print('=== leader propose-phases fields (ns raw), full windows ===')
for k in ('assemble', 'finalize', 'bls', 'push', 'notify', 'write', 'residual', 'total', 'seal2res'):
    pstats(f'  {k:10s}', [rec[n][k] / 1e6 for n in full_windows])

# sanity: total vs seal2res should track closely
diffs = [(rec[n]['total'] - rec[n]['seal2res']) / 1e6 for n in full_windows]
print(f'  total-seal2res gap: median {st.median(diffs):.2f} ms (createdAt->sealStart handoff)')

# ---- follower side: import completion per full block, all non-leader nodes ----
print()
print('=== follower import (blockimport phases), full windows, all non-leader nodes ===')
imp_rows = []
for n in full_windows:
    leader = rec[n]['node']
    for (node, nn), d in blockimport.items():
        pass
follower_imports = {}  # n -> {node: dict}
for n in full_windows:
    leader = rec[n]['node']
    followers = {}
    for node in view_changed:
        if node == leader:
            continue
        d = blockimport.get((node, n))
        if d:
            followers[node] = d
    follower_imports[n] = followers

all_import_totals = [d['total'] / 1e6 for n in full_windows for d in follower_imports[n].values()]
pstats('  import total', all_import_totals)

# quorum-forming follower = 4th-fastest of up-to-6 followers by import-end tMs
quorum_gaps = []          # push_instant(n) -> 4th-fastest follower import-end
slowest_gaps = []         # push_instant(n) -> slowest follower import-end
quorum_import_dur = []
for n in full_windows:
    followers = follower_imports[n]
    if len(followers) < 4:
        continue
    ends = sorted(d['tMs'] for d in followers.values())
    q_end = ends[3]  # 4th-fastest (0-indexed 3) of up to 6
    slow_end = ends[-1]
    quorum_gaps.append(q_end - rec[n]['push_instant'])
    slowest_gaps.append(slow_end - rec[n]['push_instant'])

print()
print('=== push_instant(v) -> 4th-fastest-follower import-done [quorum-forming], ms ===')
pstats('  quorum-follower gap', quorum_gaps)
pstats('  slowest-follower gap (straggler, not needed for quorum)', slowest_gaps)

# best-effort vote-cast join: nearest following "casting held commit vote" per node
vote_after_import = []
for n in full_windows:
    for node, d in follower_imports[n].items():
        vc = vote_cast.get(node, [])
        # binary-search-ish linear scan is fine at this scale per node list (~160 entries)
        best = None
        for tMs, view, deferred in vc:
            if tMs >= d['tMs']:
                best = tMs
                break
        if best is not None and best - d['tMs'] < 500:
            vote_after_import.append(best - d['tMs'])
print()
print('=== best-effort: import-done -> next "casting held commit vote" on same node (nearest-following, not block-keyed), ms ===')
pstats('  import->vote-cast (spot check)', vote_after_import)
print(f'  n held-vote lines available: { {node: len(v) for node, v in vote_cast.items()} }')

# ---- median in-tenure and median hand-over block: full waterfall dump ----
def pick_median_row(rows):
    rows2 = sorted(rows, key=lambda r: r['cycle'])
    return rows2[len(rows2) // 2]

med_chain = pick_median_row(chained) if chained else None
med_hand = pick_median_row(handover) if handover else None

def dump_waterfall(tag, row):
    if row is None:
        print(f'{tag}: no rows'); return
    n = row['n']
    r0, r1 = row['rec0'], row['rec1']
    T0 = r0['push_instant']
    print(f'\n=== waterfall: {tag} block n={n} leader={r1["node"]} prev_leader={r0["node"]} cycle={row["cycle"]:.0f} ms ===')
    print(f'  T0 = push_instant({n-1}) = {T0:.0f} (epoch ms)')
    if row['qc']:
        print(f'  push(v-1)->QC(v-1)/ViewStart(v) [q2s]: start=0.0 dur={row["q2s"]:.1f}')
    if row['build_prefix'] is not None:
        qoff = row['qc'] - T0
        print(f'  leader trigger+prefill+fill (unresolved sub-split): start={qoff:.1f} dur={row["build_prefix"]:.1f}')
    t = r1['t_commit_start'] - T0
    print(f'  leader commit()/assemble+finalize: start={t:.1f} dur={r1["assemble"]/1e6:.1f} (finalize done at +{r1["finalize"]/1e6:.1f})')
    t2 = r1['created_at'] - T0
    print(f'  leader bls sign: start={t2:.1f} dur={r1["bls"]/1e6:.1f}')
    t3 = t2 + r1['bls'] / 1e6
    print(f'  leader gate+copy residual (unsplit): start={t3:.1f} dur={r1["residual"]/1e6:.1f}')
    t4 = t3 + r1['residual'] / 1e6
    print(f'  leader push: start={t4:.1f} dur={r1["push"]/1e6:.1f}  (push_instant offset={row["cycle"]:.1f})')
    print(f'  leader write (parallel, off critical path candidate): start={row["cycle"]:.1f} dur={r1["write"]/1e6:.1f}')
    if row['qc']:
        print(f'  QC(v) proxy (leaders own next isLeader view-changed): offset={row["qc"]-T0:.1f}')
        print(f'    push(v-1)->QC(v) = {row["q2s"]:.1f} ; QC(v)->push(v) = {row["s2q"]:.1f}')
    followers = follower_imports.get(n, {})
    if followers:
        ends = sorted((d['tMs'], node) for node, d in followers.items() for _ in [0])
        ends = sorted((d['tMs'] - T0, node, d['total'] / 1e6) for node, d in followers.items())
        print('  follower imports (offset=import-end relative to T0, dur=own total):')
        for off, node, dur in ends:
            arr = arrived.get((node, n))
            arr_off = (arr - T0) if arr else None
            marker = ''
            print(f'    {node}: arrive_off={arr_off} import_end_off={off:.1f} import_dur={dur:.1f}{marker}')
        if len(ends) >= 4:
            print(f'  4th-fastest follower (quorum-forming): {ends[3]}')

dump_waterfall('median in-tenure', med_chain)
dump_waterfall('median hand-over', med_hand)
