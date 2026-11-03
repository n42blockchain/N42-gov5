#!/usr/bin/env python3
"""Block shape: fit each per-block phase as fixed + per-1k-tx cost.

Usage: block_shape.py NODE_LOG[.gz] HH:MM:SS HH:MM:SS

Reads one node's JSON log over a time window (e.g. a measurement window of an
e1n26 leg on node0). The fit uses the blocks below the fill cap (163,000
transfers) because blocks at the cap all have the same size; the median at
the cap is printed next to the fitted value at 163k as a check. "fit@250k"
extrapolates the line, so read it as an estimate of what a larger fill
would cost per block, not as a measurement.
"""
import gzip,json,statistics as st,sys
f,lo,hi=sys.argv[1],sys.argv[2],sys.argv[3]
B={};P={};W={};A={};L={};S={}
op=gzip.open if f.endswith('.gz') else open
for l in op(f,'rt'):
    t=l.find('"time":"')
    if t<0: continue
    ts=l[t+19:t+27]
    if not (lo<=ts<=hi): continue
    try:o=json.loads(l)
    except: continue
    m=o.get('msg')
    if m=='blockimport phases': B[o['n']]=o
    elif m=='parallel block': P[o['n']]=o
    elif m=='blockwrite phases' and o.get('txs'): W[o['n']]=o
    elif m=='availability check phases': A[o['n']]=o
    elif m=='miner: lag build': L[o['n']]=o
    elif m=='miner: lag block sealed and proposed': S[o['n']]=o
def fit(xs,ys):
    mx,my=st.mean(xs),st.mean(ys)
    sxx=sum((x-mx)**2 for x in xs); syy=sum((y-my)**2 for y in ys)
    b=sum((x-mx)*(y-my) for x,y in zip(xs,ys))/sxx
    r=b*(sxx/syy)**.5 if syy else 0
    return my-b*mx,b,r
ns=[n for n in sorted(B) if n in P and P[n]['txs']<163000]
print('blocks',len(P),'below cap',len(ns),'at cap',sum(1 for n in P if P[n]['txs']==163000))
def rep(name,get):
    xs=[];ys=[];cy=[]
    for n in ns:
        try: y=get(n)
        except KeyError: continue
        xs.append(P[n]['txs']/1000); ys.append(y)
    for n in P:
        if P[n]['txs']==163000:
            try: cy.append(get(n))
            except KeyError: pass
    if len(xs)<20: print(name,'n/a'); return
    a,b,r=fit(xs,ys)
    capm=st.median(cy) if cy else float('nan')
    print(f"{name:12s} fixed {a:6.1f}  per-1k {b:6.3f}  r={r:4.2f}  fit@163k {a+b*163:6.1f}  median@cap {capm:6.1f}  fit@250k {a+b*250:6.1f}")
for k in ['aheadRun','aheadWait','exec','collect','fPreHook','fCompute','hdr','root','prep','setup','valid','recov','total']:
    rep(k,lambda n,k=k:B[n][k]/1e6)
for k in ['total','prep','commit','frames','qflush','chgset']:
    rep('bw '+k,lambda n,k=k:W[n][k]/1e6)
rep('avail',lambda n:A[n]['totalUs']/1e3)
ts=[(n,B[n]['tMs']) for n in sorted(B) if 'tMs' in B[n] and n in P]
d=[(P[n2]['txs'],t2-t1) for (n1,t1),(n2,t2) in zip(ts,ts[1:]) if n2==n1+1]
cap=[x for t,x in d if t==163000]; nc=[(t/1000,x) for t,x in d if t<163000]
a,b,r=fit([t for t,x in nc],[x for t,x in nc])
print(f'import tMs interval: median@cap {st.median(cap)} mean@cap {st.mean(cap):.1f}; below cap fixed {a:.1f} per1k {b:.3f} r={r:.2f}')
if L:
    lb=[o['total']/1e6 for o in L.values()]; print('lag build total median',st.median(lb),'txs median',st.median([o['txs'] for o in L.values()]))
