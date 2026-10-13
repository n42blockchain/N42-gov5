import pathlib,json,gzip,statistics,collections,re,argparse
parser=argparse.ArgumentParser(description="Summarize S104 phase rows from preserved node0/node6 JSON logs")
parser.add_argument("--log-root",default="/data/blockchain/wr-analysis/s104-resume-20261013")
parser.add_argument("--runner-log",default="/data/blockchain/wr-logs/r35zzzcd.log")
args=parser.parse_args()
out=pathlib.Path(args.log_root)
log=pathlib.Path(args.runner_log).read_text();starts={}
for line in log.splitlines():
 m=re.match(r'(\d\d:\d\d:\d\d) S101 capture (warmup|B1|B2): barrier release seen',line)
 if m:starts[m[2]]=m[1]
print('release times',starts)
def secs(v):a=list(map(int,v.split(':')));return a[0]*3600+a[1]*60+a[2]
results={}
for node in [0,6]:
 phases=collections.defaultdict(list)
 for p in sorted((out/f'node{node}').glob('*')):
  opener=gzip.open if p.suffix=='.gz' else open
  with opener(p,'rt') as f:
   for line in f:
    try:r=json.loads(line)
    except:continue
    t=r.get('time','');t=secs(t[-8:]) if len(t)>=8 else -1
    leg=next((leg for leg,start in starts.items() if secs(start)+15<=t<secs(start)+135),None)
    if leg is None:continue
    win='win1' if t<secs(starts[leg])+75 else 'win2'
    if r.get('msg') in ['blockimport phases','blockwrite phases'] and r.get('txs')==163000:phases[(leg,win,r['msg'])].append(r)
    elif r.get('msg') in ['leader gate phases','deferred production resumed']:phases[(leg,win,r['msg'])].append(r)
 for (leg,win,kind),rows in phases.items():
  nums={k:[r[k] for r in rows if type(r.get(k)) in [int,float]] for k in rows[0] if k not in ['n','tMs','txs','gas','rootOps']}
  fields=['total','aheadPre','recov','aheadRun','aheadWait','setup','setupAff','setupSys','exec','collect','root','rootApply','handWait','prep','prepWait','frames','framesWait','begin','block','state','chgset','qflush','commit']
  obj={'count':len(rows),'milliseconds':{k:round(statistics.median(nums[k])/1e6,2) for k in fields if nums.get(k)}}
  if kind=='blockimport phases':
   obj['ahead_used']=sum(bool(r.get('ahead')) for r in rows);obj['ahead_counters']={k:[rows[0].get(k),rows[-1].get(k)] for k in ['aheadAtt','aheadUsed','aheadDisc','aheadLate','aheadSkipParent','aheadSkipBusy','aheadSkipNotAvail','aheadTakeLaunch']}
  results[f'node{node}/{leg}/{win}/{kind}']=obj
print(json.dumps(results,indent=2));(out/'phase-summary.json').write_text(json.dumps(results,indent=2)+'\n')
