import glob, json, sys, statistics as st
since = sys.argv[1]
props={}; views={}
for path in sorted(glob.glob('/data/blockchain/qs-node*/log/n42.log')):
    node=path.split('/')[3]
    for line in open(path, errors='replace'):
        if '"tMs"' not in line: continue
        try: d=json.loads(line)
        except: continue
        if d.get('time','')[:16] < since: continue
        m=d.get('msg')
        if m=='miner: propose phases' and d.get('txs',0)>=160000:
            props.setdefault(node,[]).append((d['tMs']-d.get('write',0)/1e6, d['tMs'], d['n']))
        elif m=='hotstuff: view changed' and d.get('isLeader'):
            views.setdefault(node,[]).append(d['tMs'])
q2s=[]; s2q=[]; per=[]
for node,ps in props.items():
    ps.sort(); vs=sorted(views.get(node,[]))
    for (seal,wend,n),(seal2,wend2,n2) in zip(ps,ps[1:]):
        if n2!=n+1 or seal2-seal>6000: continue
        per.append(seal2-seal)
        qc=[v for v in vs if seal < v <= seal2]
        if qc:
            q2s.append(qc[0]-seal); s2q.append(seal2-qc[0])
def p(name,xs):
    xs=sorted(xs)
    if xs: print(f'{name:34s} n={len(xs):4d} med={st.median(xs):6.0f} p10={xs[int(len(xs)*.1)]:6.0f} p90={xs[int(len(xs)*.9)]:6.0f} ms')
p('seal(v) -> seal(v+1) period', per)
p('seal(v) -> QC(v) [leader view change]', q2s)
p('QC(v) -> seal(v+1) [waiting for build]', s2q)
