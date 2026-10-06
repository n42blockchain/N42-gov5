import glob, json, sys, statistics as st
since = sys.argv[1]
build=[]; prop=[]; seal=[]; period={}
for path in sorted(glob.glob('/data/blockchain/qs-node*/log/n42.log')):
    node=path.split('/')[3]
    for line in open(path, errors='replace'):
        if '"prefix":"miner"' not in line: continue
        try: d=json.loads(line)
        except: continue
        if d.get('time','')[:16] < since: continue
        m=d.get('msg')
        if m=='miner: build phases' and d.get('fillTx',0)>200e6: build.append(d)
        elif m=='miner: propose phases' and d.get('txs',0)>=160000:
            prop.append(d); period.setdefault(node,[]).append(d['tMs'])
        elif m=='🔨 Successfully sealed new block' and d.get('txs',0)>=160000: seal.append(d)
def show(name, rows, keys, div=1e6):
    print(name, 'n=',len(rows))
    for k in keys:
        xs=sorted(r.get(k,0)/div for r in rows if k in r)
        if xs: print(f'  {k:12s} med={st.median(xs):7.0f} p90={xs[int(len(xs)*.9)]:7.0f} ms')
show('build phases (full)', build, ['align','persistWait','reload','syscalls','fillTx','total'])
show('propose phases (full)', prop, ['assemble','finalize','bls','push','write','seal2res','total'])
show('sealed elapsed (full)', seal, ['elapsed'])
per=[]
for node,ts in period.items():
    ts.sort()
    per += [b-a for a,b in zip(ts,ts[1:]) if b-a<6000]
per.sort()
if per: print('leader write-end period (same leader, consecutive full blocks): n',len(per),'med',st.median(per),'p90',per[int(len(per)*.9)],'ms')
