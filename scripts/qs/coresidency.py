#!/usr/bin/env python3
"""Measure whether the scheduler puts threads from DIFFERENT fleet nodes on the
same physical core.

usage: coresidency.py [match-substring] [seconds]
   e.g. coresidency.py 'bin/n42 --chain' 60

Why it exists: the fleet is synchronised -- 215 of 225 blocks are imported by
all seven nodes within the same second -- so seven nodes each fanning out
GOMAXPROCS-GOMAXPROCS/4 recovery workers is ~196 runnable threads on 128
physical cores during that phase, whatever the 25-second average says. The
arithmetic in docs/QS_TPS_BENCHMARK.md assumes the scheduler fills physical
cores before doubling up on SMT siblings. That has never been checked, and
checking the premise is cheaper than A/B-ing the fix (rule 22a).

Method: /proc/<pid>/task/*/stat field 39 is the last CPU a thread ran on. Only
threads in state R are counted -- a sleeping thread's field is stale. This box
pairs siblings as (c, c+128), so physical core = cpu % 128.
"""
import glob, os, sys, time, collections

PHYS = 128
def node_pids(pat):
    out = {}
    for d in glob.glob('/proc/[0-9]*'):
        try:
            cmd = open(d + '/cmdline', 'rb').read().replace(b'\0', b' ').decode('utf8', 'replace')
        except OSError:
            continue
        if pat in cmd and 'grep' not in cmd:
            # label by datadir suffix so nodes are distinguishable
            tag = None
            for tok in cmd.split():
                for marker in ('qs-node', '/node', 'node'):
                    if marker in tok and any(ch.isdigit() for ch in tok):
                        tag = tok[-24:]
                        break
                if tag:
                    break
            if tag is None:
                tag = 'pid' + d.rsplit('/', 1)[1]
            out[int(d.rsplit('/', 1)[1])] = tag
    return out

def sample(pids):
    """physical core -> set of node tags with a RUNNABLE thread on it"""
    core = collections.defaultdict(set)
    live = 0
    for pid, tag in pids.items():
        for t in glob.glob('/proc/%d/task/*/stat' % pid):
            try:
                s = open(t).read()
            except OSError:
                continue
            r = s[s.rfind(')') + 2:].split()
            if r[0] != 'R':            # field 3 = state
                continue
            cpu = int(r[36])           # field 39 overall = index 36 after comm
            core[cpu % PHYS].add(tag)
            live += 1
    return core, live

def main():
    pat = sys.argv[1] if len(sys.argv) > 1 else 'bin/n42-hasblock --chain'
    secs = int(sys.argv[2]) if len(sys.argv) > 2 else 60
    pids = node_pids(pat)
    if not pids:
        print('no processes match %r' % pat); return
    print('watching %d processes' % len(pids))
    for pid in sorted(pids):
        try:
            aff = [l.split()[1] for l in open('/proc/%d/status' % pid)
                   if l.startswith('Cpus_allowed_list')][0]
        except (OSError, IndexError):
            aff = '?'
        print('  pid %-8d affinity %s' % (pid, aff))
    print('NOTE: an affinity narrower than the whole machine means placement is '
          'PINNED, and this tool then measures the pinning, not the scheduler.')
    shared_samples = tot_samples = 0
    hist = collections.Counter()
    peak = 0
    end = time.time() + secs
    while time.time() < end:
        core, live = sample(pids)
        if live == 0:
            time.sleep(0.02); continue
        multi = sum(1 for c, tags in core.items() if len(tags) > 1)
        used  = len(core)
        hist[multi] += 1
        peak = max(peak, live)
        tot_samples += 1
        if multi: shared_samples += 1
        time.sleep(0.05)
    print('\nsamples with >=1 runnable thread: %d' % tot_samples)
    print('samples where two DIFFERENT nodes shared a physical core: %d (%.1f%%)'
          % (shared_samples, 100 * shared_samples / max(tot_samples, 1)))
    print('peak simultaneously-runnable fleet threads: %d (of %d physical cores)' % (peak, PHYS))
    print('distribution of cross-node shared cores per sample:')
    for k in sorted(hist)[:12]:
        print('  %2d shared cores: %5d samples (%.1f%%)' % (k, hist[k], 100 * hist[k] / tot_samples))

main()
