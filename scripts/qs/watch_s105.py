#!/usr/bin/env python3
"""Preserve a completed S105's evidence and write its result; never start a fleet."""
import json
import pathlib
import re
import shutil
import subprocess
import time

ROOT = pathlib.Path('/data/blockchain/wr-analysis/s105-20261013')
LOG = pathlib.Path('/data/blockchain/wr-logs/r35zzzce.log')
ROOT.mkdir(parents=True, exist_ok=True)
terminal = re.compile(r'^\d\d:\d\d:\d\d ROUND (DONE[^\n]*|[^\n]*ABORTED[^\n]*)', re.M)
deadline = time.monotonic() + 12 * 3600
while time.monotonic() < deadline:
    text = LOG.read_text() if LOG.exists() else ''
    match = terminal.search(text)
    if match:
        result = {'terminal': match[0], 'windows': {}, 'status': 'completed' if match[1] == 'DONE' else 'aborted'}
        for node in range(7):
            target = ROOT / f'node{node}'
            target.mkdir(exist_ok=True)
            for source in pathlib.Path(f'/data/blockchain/qs-node{node}/log').glob('n42*'):
                shutil.copy2(source, target / source.name)
        shutil.copy2(LOG, ROOT / LOG.name)
        leg = None
        for line in text.splitlines():
            found = re.match(r'^\d\d:\d\d:\d\d LEG (warmup|A1|B1|B2|A2):', line)
            if found:
                leg = found[1]
                result['windows'].setdefault(leg, {})
            window = re.match(r'^(win[12]): .*?execTPS=(\d+)', line)
            if leg and window:
                result['windows'][leg][window[1]] = int(window[2])
        result['standing_best_before_round'] = {'round': 'S103', 'executed_tps': 776967}
        result['performance_verdict'] = 'pending phase, CPU and safety analysis; no automatic best promotion'
        (ROOT / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        analyzer = pathlib.Path(__file__).with_name('analyze_s104.py')
        with (ROOT / 'phase-summary.txt').open('w') as output:
            subprocess.run(['python3', str(analyzer), '--log-root', str(ROOT), '--runner-log', str(LOG)], stdout=output, stderr=subprocess.STDOUT, check=True)
        print(json.dumps(result, indent=2), flush=True)
        break
    time.sleep(30)
else:
    (ROOT / 'result.json').write_text(json.dumps({'status': 'watcher_timeout', 'runner_log': str(LOG)}, indent=2) + '\n')
