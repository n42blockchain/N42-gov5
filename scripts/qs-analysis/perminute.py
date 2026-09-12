#!/usr/bin/env python3
"""Per-minute follower import / leader proposal summary for a round's B legs.
Usage: perminute.py <since 'YYYY-MM-DD HH:MM'> [out-file]  (reads the live n42.log of every node)"""
import json,glob,sys,collections,statistics
since=sys.argv[1]; out=open(sys.argv[2],'w') if len(sys.argv)>2 else sys.stdout
imp=collections.OrderedDict(); pb=collections.OrderedDict(); prop=collections.OrderedDict(); rp=collections.OrderedDict()
for p in glob.glob('/data/blockchain/qs-node[0-6]/log/n42.log'):
    for line in open(p,errors='replace'):
        if '"time":"' not in line: continue
        i=line.find('"time":"'); t=line[i+8:i+24]
        if t<since: continue
        m=t[11:16]
        if '"msg":"blockimport phases"' in line:
            d=json.loads(line)
            if d.get('txs',0)>=160000: imp.setdefault(m,[]).append(d)
        elif '"msg":"parallel block"' in line:
            d=json.loads(line)
            if d.get('txs',0)>=160000 and not d['lenient']: pb.setdefault(m,[]).append(d)
        elif '"msg":"miner: propose phases"' in line:
            d=json.loads(line); prop.setdefault(m,[]).append(d)
        elif '"msg":"qmdb root phases"' in line:
            d=json.loads(line)
            if d['ops']>=20000: rp.setdefault(m,[]).append(d['applyNs']/1e6)
def med(xs): return statistics.median(xs) if xs else float('nan')
def p90(xs): return sorted(xs)[int((len(xs)-1)*.9)] if xs else float('nan')
print("minute | proposals(full) mean_txs | import n med p90 proc write_p90 | recover executor exec finalize | qmdb_apply", file=out)
for m in sorted(set(imp)|set(prop)):
    pr=prop.get(m,[]); v=imp.get(m,[]); b=pb.get(m,[])
    full=sum(1 for d in pr if d['txs']>=160000)
    line="%s | %3d(%3d) %6.0f | %3d %5.0f %5.0f %5.0f %5.0f | %4.0f %4.0f %4.0f %4.0f | %4.0f"%(m,len(pr),full,med([d['txs'] for d in pr]) if pr else 0,len(v),med([d['total']/1e6 for d in v]),p90([d['total']/1e6 for d in v]),med([d['proc']/1e6 for d in v]),p90([d['write']/1e6 for d in v]),med([d['recoverMs'] for d in b]),med([d['executorMs'] for d in b]),med([d['execMs'] for d in b]),med([d['finalizeMs'] for d in b]),med(rp.get(m,[])))
    print(line, file=out)
