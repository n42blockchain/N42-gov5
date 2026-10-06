import glob, json, gzip, sys, statistics as st
since, until, txmin = sys.argv[1], sys.argv[2], int(sys.argv[3])
props={}; views={}; imp=[]; fills=[]; pblock=[]
paths=sorted(glob.glob('/data/blockchain/qs-node*/log/n42.log'))+sorted(glob.glob('/data/blockchain/qs-node*/log/n42-2026-09-11T0[4-9]*.log.gz'))
for path in paths:
    node=path.split('/')[3]
    op=gzip.open if path.endswith('.gz') else open
    with op(path,'rt',errors='replace') as f:
        for line in f:
            if '"time":"' not in line: continue
            i=line.find('"time":"'); t=line[i+8:i+24]
            if t<since or t>until: continue
            try: d=json.loads(line)
            except: continue
            m=d.get('msg')
            if m=='miner: propose phases' and d.get('txs',0)>=txmin:
                props.setdefault(node,[]).append((d['tMs']-d.get('write',0)/1e6, d['tMs'], d['n'], d))
            elif m=='hotstuff: view changed' and d.get('isLeader'): views.setdefault(node,[]).append(d['tMs'])
            elif m=='blockimport phases' and d.get('txs',0)>=txmin: imp.append(d)
            elif m=='miner: parallel fill' and d.get('candidates',0)>=txmin: fills.append(d)
            elif m=='parallel block' and d.get('txs',0)>=txmin and not d.get('lenient'): pblock.append(d)
def p(name,xs,div=1.0):
    xs=sorted(x/div for x in xs)
    if xs: print(f'  {name:30s} n={len(xs):4d} med={st.median(xs):6.0f} p90={xs[int(len(xs)*.9)]:6.0f}')
per=[];q2s=[];s2q=[];hand=[]
sealer={}
for node,ps in props.items():
    ps.sort(); vs=sorted(views.get(node,[]))
    for (seal,wend,n,d) in ps: sealer[n]=(node,seal)
    for (seal,wend,n,d),(seal2,wend2,n2,d2) in zip(ps,ps[1:]):
        if n2!=n+1 or seal2-seal>6000: continue
        per.append(seal2-seal)
        qc=[v for v in vs if seal < v <= seal2]
        if qc: q2s.append(qc[0]-seal); s2q.append(seal2-qc[0])
for n,(node,seal) in sealer.items():
    prev=sealer.get(n-1)
    if prev and prev[0]!=node and 0<seal-prev[1]<8000: hand.append(seal-prev[1])
print(f'{since}..{until} txs>={txmin}')
p('chained seal->seal',per); p('  seal->QC',q2s); p('  QC->next seal',s2q); p('handover seal->seal',hand)
p('propose write ms',[d['write'] for _,_,_,d in sum(props.values(),[])],1e6)
p('propose assemble ms',[d['assemble'] for _,_,_,d in sum(props.values(),[])],1e6)
p('fill run ms',[d['run'] for d in fills],1e6)
p('fill pick ms',[d['pick'] for d in fills],1e6)
for k in ['setupMs','recoverMs','execMs','applyMs','finalizeMs','validateMs']:
    p('import '+k,[d[k] for d in pblock if k in d])
for k in ['body','proc','write','total']:
    p('import '+k,[d[k] for d in imp if k in d],1e6)
