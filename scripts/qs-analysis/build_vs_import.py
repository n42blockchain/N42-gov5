#!/usr/bin/env python3
"""S24: what is inside the leader's buildBegin->specParked build (611-650 ms,
6cv) of a full in-tenure block, and what is the follower's blockimport
(774-803 ms, 6ca/6cv's import_breakdown numbers) of the SAME block, side by
side. Logs-only, single-threaded (35zzzg is running on the box).

Lines joined, all keyed by block number "n" (or "number" on seal path):
  miner: seal path       (leader, ms-precision: buildBeginTMs, specParkedTMs, ...)
  miner: commitWork begin (leader, no n, no tMs -- counted, not joined)
  miner: prefill phases   (leader, >50ms outlier only, has tMs+n: buildBegin's
                            own pre-fill breakdown: alignCall, lockWait,
                            insertParent, persistWait, roTxBegin,
                            specTreeReload, rootLockWait, headerPrepare,
                            blockStart, pendingSnapshot, trim, total)
  miner: parallel fill    (leader, no tMs, has n via caller context -- joined
                            by (node,time) proximity to the seal-path row
                            since it carries no "n" field itself; candidates,
                            included, failed, pick, run, pendingSnapshot,
                            trim, staleTrimmed)
  parallel block          (BOTH leader and follower -- the shared execution
                            engine; distinguished by whether the logging node
                            IS the block's own leader; waves, executions,
                            aborts, fallback, recoverMs, setupMs,
                            blockStartMs, executorMs, runMs, execMs,
                            validateMs, collectMs, applyMs, prefetchMs,
                            finalizeMs)
  blockimport phases      (follower only: hdr, body, prep, recov, align,
                            exec, proc, root, valid, write, total, tMs)
  miner: propose phases   (leader: task.assemble/task.finalize via the
                            "assemble"/"finalize" fields)

Usage: build_vs_import.py <kept-logs-dir> leg_b1_start leg_b1_end
       leg_b2_start leg_b2_end b1win1 b1win2 b2win1 b2win2 full_win_names
"""
import sys, os, json, glob, statistics as st, collections

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

propose_all = {}          # n -> dict (leader's own propose-phases line)
seal_path = {}             # (node, n) -> dict
prefill = {}                # (node, n) -> dict
parallel_fill = []          # (node, time, dict) -- no n, joined by proximity
parallel_block = collections.defaultdict(list)   # (node, n) -> [dict, ...] (usually 1)
blockimport = {}            # (node, n) -> dict

for path in paths:
    node = node_of(path)
    with open(path, errors='replace') as f:
        for line in f:
            if '"msg":"miner: propose phases"' in line:
                d = json.loads(line); d['node'] = node; propose_all[d['n']] = d
            elif '"msg":"miner: seal path"' in line:
                d = json.loads(line); seal_path[(node, d['number'])] = d
            elif '"msg":"miner: prefill phases"' in line:
                d = json.loads(line); prefill[(node, d['n'])] = d
            elif '"msg":"miner: parallel fill"' in line:
                d = json.loads(line); parallel_fill.append((node, d['time'], d))
            elif '"msg":"parallel block"' in line:
                d = json.loads(line); parallel_block[(node, d['n'])].append(d)
            elif '"msg":"blockimport phases"' in line:
                d = json.loads(line); blockimport[(node, d['n'])] = d


def in_leg(n, leg):
    return leg[0] <= propose_all[n]['time'] <= leg[1]


def leg_of_time(t):
    if LEG_B1[0] <= t <= LEG_B1[1]:
        return 'B1'
    if LEG_B2[0] <= t <= LEG_B2[1]:
        return 'B2'
    return None


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


def pstats(name, xs):
    xs = sorted(x for x in xs if x is not None)
    if not xs:
        print(f'{name}: n=0'); return None
    med = st.median(xs)
    p10 = xs[int(len(xs) * .1)]
    p90 = xs[min(len(xs) - 1, int(len(xs) * .9))]
    print(f'{name}: n={len(xs):5d} median={med:8.1f} p10={p10:8.1f} p90={p90:8.1f}')
    return med


# ============ Join per full in-tenure block: leader's own build + its own
# parallel-block execution, and every FOLLOWER's import + their own
# parallel-block execution of the SAME block. In-tenure = chained (n-1,n)
# same leader, as prior sections, but here every full-window block (chained
# or handover) can serve as "the block being built/imported" -- only the
# SPECULATIVE build's own timing needs the n-1 same-leader condition (since
# specParked only exists on a speculative call). Use ALL full-window blocks
# for the follower side, and the (n-1,n)-chained subset for the leader's
# buildBegin->specParked (the only case where specParkedTMs is nonzero).
leader_rows_by_win = collections.defaultdict(list)
for n in full_windows:
    if (n - 1) not in propose_all:
        continue
    node = propose_all[n]['node']
    if propose_all[n - 1]['node'] != node:
        continue
    sp = seal_path.get((node, n))
    if not sp or not sp.get('specParkedTMs') or not sp.get('buildBeginTMs'):
        continue
    win = leg_win_of_n(n)
    if win is None:
        continue
    leader_rows_by_win[win].append(dict(n=n, node=node, sp=sp,
                                         pf=prefill.get((node, n)),
                                         pb=parallel_block.get((node, n), [None])[0]))

follower_rows_by_win = collections.defaultdict(list)
for n in full_windows:
    win = leg_win_of_n(n)
    if win is None:
        continue
    leader_node = propose_all[n]['node']
    for node in NODES:
        if node == leader_node:
            continue
        bi = blockimport.get((node, n))
        if not bi:
            continue
        follower_rows_by_win[win].append(dict(n=n, node=node, bi=bi,
                                               pb=parallel_block.get((node, n), [None])[0]))

print()
for win in ('B1win1', 'B1win2', 'B2win1', 'B2win2'):
    print(f'population {win}: leader(build) n={len(leader_rows_by_win.get(win, []))}  '
          f'follower(import) n={len(follower_rows_by_win.get(win, []))}')

# ================= 1/2. LEADER build waterfall =================
print()
print('=== LEADER build waterfall (buildBegin -> specParked), by window ===')
for win in ('B1win1', 'B1win2', 'B2win1', 'B2win2'):
    rows = leader_rows_by_win.get(win, [])
    print(f'-- {win} (n={len(rows)}) --')
    pstats('  buildBegin->specParked (TOTAL, ms-precision)',
           [(r['sp']['specParkedTMs'] - r['sp']['buildBeginTMs']) for r in rows])
    # prefill's own 9 sub-steps (ns -> ms)
    pf_rows = [r['pf'] for r in rows if r['pf']]
    print(f'  prefill-phases rows matched: {len(pf_rows)}/{len(rows)}')
    for k in ('alignCall', 'lockWait', 'insertParent', 'persistWait', 'roTxBegin',
              'specTreeReload', 'rootLockWait', 'headerPrepare', 'blockStart',
              'pendingSnapshot', 'trim', 'total'):
        pstats(f'    prefill.{k}', [d.get(k, 0) / 1e6 for d in pf_rows])
    # parallel block's own fields (execution engine, shared leader/follower)
    pb_rows = [r['pb'] for r in rows if r['pb']]
    print(f'  parallel-block rows matched: {len(pb_rows)}/{len(rows)}')
    for k in ('recoverMs', 'setupMs', 'blockStartMs', 'executorMs', 'runMs',
              'execMs', 'validateMs', 'collectMs', 'applyMs', 'prefetchMs', 'finalizeMs'):
        pstats(f'    parblock.{k}', [d.get(k) for d in pb_rows])
    pstats('    parblock.waves', [d.get('waves') for d in pb_rows])
    pstats('    parblock.aborts', [d.get('aborts') for d in pb_rows])
    fallback_n = sum(1 for d in pb_rows if d.get('fallback'))
    print(f'    parblock.fallback: {fallback_n}/{len(pb_rows)}')
    # task.assemble / task.finalize from propose phases (commit()'s own wrapper)
    pp_rows = [propose_all[r['n']] for r in rows]
    pstats('  propose-phases.assemble (task.assemble, commit() wrapper)',
           [d.get('assemble', 0) / 1e6 for d in pp_rows])
    pstats('  propose-phases.finalize (task.finalize)',
           [d.get('finalize', 0) / 1e6 for d in pp_rows])
    pstats('  propose-phases.bls', [d.get('bls', 0) / 1e6 for d in pp_rows])
    pstats('  propose-phases.witness', [d.get('witness', 0) / 1e6 for d in pp_rows])
    # reconciliation: sum of finest available non-overlapping steps vs TOTAL
    named_sums = []
    for r in rows:
        pb_d = r['pb']
        if not pb_d:
            continue
        # recoverMs+setupMs+blockStartMs+executorMs+runMs+collectMs+applyMs+
        # prefetchMs+finalizeMs is the non-overlapping sum (execMs/validateMs
        # are sub-components OF runMs, per parallel_processor.go's own
        # WaveTimes() split -- NOT additional time; excluded here to avoid
        # double-counting).
        pb_total = sum(pb_d.get(k, 0) for k in ('recoverMs', 'setupMs', 'blockStartMs',
                                                 'executorMs', 'runMs', 'collectMs',
                                                 'applyMs', 'prefetchMs', 'finalizeMs'))
        pp = propose_all[r['n']]
        commit_wrapper = pp.get('assemble', 0) / 1e6  # commit()'s own wrapper, AFTER parallel block returns
        tot = r['sp']['specParkedTMs'] - r['sp']['buildBeginTMs']
        named_sums.append((pb_total, commit_wrapper, tot, tot - pb_total - commit_wrapper))
    if named_sums:
        pb_med = st.median(x[0] for x in named_sums)
        cw_med = st.median(x[1] for x in named_sums)
        tot_med = st.median(x[2] for x in named_sums)
        gap_med = st.median(x[3] for x in named_sums)
        print(f'  RECONCILE: parblock(sum, execution)={pb_med:.1f} + commit_wrapper(assemble)={cw_med:.1f} '
              f'= {pb_med+cw_med:.1f}  vs TOTAL(buildBegin->specParked)={tot_med:.1f}  '
              f'-> unaccounted(median of per-row gap)={gap_med:.1f} ({100*gap_med/tot_med:.1f}% of total)')

# ================= 3. FOLLOWER import waterfall =================
print()
print('=== FOLLOWER import waterfall (blockimport phases + parallel block), by window ===')
for win in ('B1win1', 'B1win2', 'B2win1', 'B2win2'):
    rows = follower_rows_by_win.get(win, [])
    print(f'-- {win} (n={len(rows)}) --')
    bi_rows = [r['bi'] for r in rows]
    for k in ('hdr', 'body', 'prep', 'recov', 'align', 'exec', 'proc', 'root', 'valid', 'write', 'total'):
        xs = [d.get(k, 0) / 1e6 for d in bi_rows]
        if any(x > 0 for x in xs):
            pstats(f'  blockimport.{k}', xs)
    pb_rows = [r['pb'] for r in rows if r['pb']]
    print(f'  parallel-block rows matched: {len(pb_rows)}/{len(rows)}')
    for k in ('recoverMs', 'setupMs', 'blockStartMs', 'executorMs', 'runMs',
              'execMs', 'validateMs', 'collectMs', 'applyMs', 'prefetchMs', 'finalizeMs'):
        pstats(f'    parblock.{k}', [d.get(k) for d in pb_rows])
    pstats('    parblock.waves', [d.get('waves') for d in pb_rows])
    pstats('    parblock.aborts', [d.get('aborts') for d in pb_rows])
    fallback_n = sum(1 for d in pb_rows if d.get('fallback'))
    print(f'    parblock.fallback: {fallback_n}/{len(pb_rows)}')
