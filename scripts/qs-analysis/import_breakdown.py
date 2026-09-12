import glob, json, sys, statistics as st
since, until = sys.argv[1], sys.argv[2]
imp=[]; props={}
for path in sorted(glob.glob('/data/blockchain/qs-node*/log/n42.log')):
    node=path.split('/')[3]
    for line in open(path, errors='replace'):
        if '"prefix":"internal"' not in line and '"prefix":"miner"' not in line: continue
        try: d=json.loads(line)
        except: continue
        t=d.get('time','')[:16]
        if t < since or t > until: continue
        m=d.get('msg')
        if m=='blockimport phases' and d.get('txs',0)>=160000: imp.append(d)
        elif m=='miner: propose phases' and d.get('txs',0)>=160000:
            props.setdefault(node,[]).append((d['tMs']-d.get('write',0)/1e6, d['n']))
print('follower import (full blocks) n=',len(imp))
for k in ['hdr','body','prep','recov','align','exec','proc','root','valid','write','total']:
    xs=sorted(r.get(k,0)/1e6 for r in imp if k in r)
    if xs and st.median(xs)>0: print(f'  {k:8s} med={st.median(xs):6.0f} p90={xs[int(len(xs)*.9)]:6.0f} ms')
# handover vs chained: a block whose predecessor (n-1) was sealed by another node
sealer={}
for node,ps in props.items():
    for seal,n in ps: sealer[n]=(node,seal)
hand=[]; chain=[]
for n,(node,seal) in sealer.items():
    prev=sealer.get(n-1)
    if not prev or seal-prev[1]>8000 or seal-prev[1]<0: continue
    (chain if prev[0]==node else hand).append(seal-prev[1])
for name,xs in (('chained (same leader) seal->seal',chain),('handover (new leader) seal->seal',hand)):
    xs.sort()
    if xs: print(f'{name:36s} n={len(xs):4d} med={st.median(xs):6.0f} p90={xs[int(len(xs)*.9)]:6.0f} ms')
