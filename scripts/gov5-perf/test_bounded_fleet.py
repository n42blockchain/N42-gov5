import contextlib
import copy
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import unittest
from unittest.mock import patch

import bounded_fleet as bounded
import fleet
import queue_fixture
import recovery_fleet
import test_recovery_fleet


class BoundedFleetTest(unittest.TestCase):
    def setUp(self):
        h = test_recovery_fleet.RecoveryFleetTest(); h.setUp(); self.addCleanup(h.doCleanups)
        self.h, self.root, self.m, self.write = h, h.root, h.m, h.write
        self.epoch, self.plan_id, self.sha = [3]+[0]*15, '0x'+'77'*32, '11'*32
        self.runtime_sha = hashlib.sha256((self.root/'run.json').read_bytes()).hexdigest()
        self.plan = {'Identity':self.m['identity'],'ManifestSHA256':'0x'+self.m['supply']['manifest_sha256'],'CorpusID':self.m['supply']['corpus_id'],'Canonical':{'Number':1,'Hash':'0x'+'33'*32,'Root':'0x'+'55'*32},'CanonicalNonceSum':100,'Senders':[{'Index':i,'Address':'0x'+f'{i+1:040x}','Nonce':0} for i in range(4)],'MaxSourceFrames':8,'MaxSourceTransactions':4000,'MaxNewTransactions':3900,'SourceEncodedBytes':424672,'NonceEndExclusive':1000,'RecoverySHA256':'0x'+'22'*32}
        self.doc, self.owner = {'plan':self.plan,'plan_id':self.plan_id}, h.owner(1,'33',self.epoch)

    def canonical(self,n,b,c):
        return {'identity':self.m['identity'],'number':n,'block_hash':'0x'+b*32,'committed_executed_transactions':c,'producer_running':True,'owner_queue':{'epoch':self.epoch,'manifest_hash':self.plan['ManifestSHA256'],'max_transactions':163000,'max_encoded_bytes':32<<20}}

    def candidate(self):
        a={'request_start_monotonic_ns':4000000000,'response_end_monotonic_ns':5000000000,'single_validator_canonical':self.canonical(2,'44',100)}
        b={'request_start_monotonic_ns':25000000000,'response_end_monotonic_ns':26000000000,'single_validator_canonical':self.canonical(3,'55',320)}
        return {'kind':'gov5-bounded-resume-20s-window-and-drain-candidate-not-capacity-v2','passed':True,'identity':self.m['identity'],'plan_id':self.plan_id,'plan_sha256':'0x'+self.sha,'runtime_sha256':'0x'+self.runtime_sha,'runtime_manifest':str(self.root/'run.json'),'offered_tps':6500000,'target_warmup_seconds':4,'actual_warmup_seconds':4,'full_source_integrity_verified':True,'physical_reopen_included':False,'capacity_5m_proven':False,'corpus_available_through_window':True,'window_start':a,'window_end':b,'samples':[],'canonical_window_delta':220,'actual_window_seconds':22.,'canonical_committed_executed_tps':10.,'stream_final':{'first_observed_shard_exhaustion_seconds':0.,'acknowledged_original_frames':1,'acknowledged_unique_admitted':500,'busy_retries':0,'snapshot_retries':0},'owner_initial':self.owner,'owner_final':dict(self.owner,NewlyAdmitted=500,CryptoVerified=500),'drained_canonical_count':600,'reference_final':self.canonical(4,'66',600),'sender_new_transactions':[500,0,0,0],'source_files_before':[],'source_files_after':[],'initial':[self.canonical(1,'33',100) for _ in range(7)]}

    def test_window_clock_and_drain_boundaries(self):
        v=self.candidate()
        self.assertEqual(bounded.window_evidence(v,self.doc,self.m,self.sha,self.runtime_sha,6500000),600)
        for change in [lambda x:x['window_end'].update(request_start_monotonic_ns=24999999999),lambda x:x.update(canonical_window_delta=500),lambda x:x.update(canonical_committed_executed_tps=500/22),lambda x:x['stream_final'].update(first_observed_shard_exhaustion_seconds=26),lambda x:x['window_end']['single_validator_canonical'].update(committed_executed_transactions=99),lambda x:x.update(corpus_available_through_window=False),lambda x:x.update(runtime_sha256='0x'+'99'*32),lambda x:x['owner_final'].update(PendingTransactions=1),lambda x:x.update(initial=[]),lambda x:x['window_start']['single_validator_canonical'].update(identity={}),lambda x:x['window_end']['single_validator_canonical']['owner_queue'].update(max_transactions=200000),lambda x:x['reference_final'].update(producer_running=False),lambda x:x.update(actual_warmup_seconds=5)]:
            bad=copy.deepcopy(v);change(bad)
            with self.assertRaises(ValueError):bounded.window_evidence(bad,self.doc,self.m,self.sha,self.runtime_sha,6500000)
        v['initial']=[self.canonical(i+2,'44',100) for i in range(7)]
        self.assertEqual(bounded.window_evidence(v,self.doc,self.m,self.sha,self.runtime_sha,6500000),600)

    def test_source_ready_empty_baseline_and_journal_ownership(self):
        v={'kind':'gov5-bounded-source-ready-before-production-v2','plan_id':self.plan_id,'epoch':self.epoch,'owner':self.owner,'full_source_integrity_verified':True,'dispatched_transactions':0}
        self.assertEqual(bounded.source_ready_evidence(v,self.doc,self.m),self.epoch)
        for change in [lambda x:x['owner'].update(NewlyAdmitted=1),lambda x:x['owner'].update(CryptoVerified=500),lambda x:x['owner'].update(CanonicalHash='0x'+'99'*32),lambda x:x.update(dispatched_transactions=1)]:
            bad=copy.deepcopy(v);change(bad)
            with self.assertRaises(ValueError):bounded.source_ready_evidence(bad,self.doc,self.m)
        path=self.root/'empty-journal';path.write_bytes(b'');path.chmod(0o600)
        self.assertEqual(bounded.read_journal(path),b'')
        link=self.root/'alias';link.symlink_to(path)
        with self.assertRaises(ValueError):bounded.read_journal(link)
        link.unlink();os.link(path,link)
        with self.assertRaises(ValueError):bounded.read_journal(path)

    def test_ready_marker_is_published_only_after_complete_json(self):
        path=self.root/'handshake.json'
        seen=[]
        actual=os.open
        def opening(name,flags,*args,**kwargs):
            if str(name).endswith('.complete'):
                self.assertEqual(__import__('json').loads(path.read_text()),{'finished':True})
                seen.append('complete')
            return actual(name,flags,*args,**kwargs)
        with patch('os.open',opening):bounded.publish_handshake(path,{'finished':True})
        bounded.ready_complete(Path(str(path)+'.complete'))
        self.assertEqual(seen,['complete'])
        failed=self.root/'failed-handshake.json'
        with patch('os.fsync',side_effect=OSError('injected JSON durability failure')):
            with self.assertRaises(OSError):bounded.publish_handshake(failed,{'finished':True})
        self.assertFalse(Path(str(failed)+'.complete').exists())

    def run_fixture(self,fault=None):
        outer=self;processes=[];candidate=self.candidate()
        ready_value={'kind':'gov5-bounded-source-ready-before-production-v2','plan_id':self.plan_id,'epoch':self.epoch,'owner':self.owner,'full_source_integrity_verified':True,'dispatched_transactions':0}
        class Process:
            def __init__(self,args,**kwargs):
                self.args,self.pid,self.returncode,self.env=args,90000000+len(processes),None,kwargs['env'];processes.append(self)
                if len(args)>1 and args[1]=='bounded-window':
                    self.attempt=outer.root/'strict-attempts'/args[args.index('--attempt')+1];self.attempt.mkdir(mode=0o700);outer.write(self.attempt/'source-ready.json',ready_value);(self.attempt/'source-ready.json.complete').write_bytes(b'');(self.attempt/'source-ready.json.complete').chmod(0o600)
                elif len(args)>1 and args[1]=='recover':
                    path=outer.root/'strict-attempts'/args[args.index('--attempt')+1];path.mkdir(mode=0o700);outer.write(path/'recovery.json',{'account_vector_sha256':'0x'+'88'*32});self.returncode=0
            def poll(self):
                if hasattr(self,'attempt') and (self.attempt/'production-ready.json.complete').exists() and self.returncode is None:
                    outer.write(self.attempt/'result-candidate.json',candidate);self.returncode=1 if fault=='command' else 0
                return self.returncode
            def wait(self,timeout=None):
                if self.poll() is None:raise subprocess.TimeoutExpired(self.args,timeout)
                return self.returncode
        def killpg(pid,sig):
            p=next(p for p in processes if p.pid==pid)
            if sig==signal.SIGKILL:p.returncode=-9
            elif fault=='forced' and p.args[0]=='owner':pass
            else:p.returncode=1 if fault=='nonzero' and p.args[0]=='owner' else 0
        private,mirror=self.root/'mock-claim',self.root/'mock-mirror'
        @contextlib.contextmanager
        def claimed():
            private.mkdir(mode=0o700);mirror.write_text('own');mirror.chmod(0o600)
            old=fleet._active_claim;fleet._active_claim={'path':private,'token':'mock','files':[(mirror,mirror.stat().st_dev,mirror.stat().st_ino)]}
            try:
                yield {'available_gib':100,'load1':0}
                if fault=='tail':raise fleet.HardwareContention('tail contention')
            finally:
                fleet._active_claim=old
                if fault!='claim-residue':mirror.unlink();private.rmdir()
        plan,proof,historical=(self.root/n for n in ('plan.json','proof.json','historical.json'))
        for path in (plan,proof,historical):self.write(path,{'synthetic':True})
        self.plan['SourceProofSHA256']='0x'+hashlib.sha256(proof.read_bytes()).hexdigest()
        old_exists=Path.exists;old_lexists=os.path.lexists
        def exists(path):return path.suffix=='.sock' or old_exists(path)
        def lexists(path):return True if fault=='socket' and len(processes)==18 and str(path).endswith('validator.sock') else old_lexists(path)
        ticks=[0.]
        def clock():ticks[0]+=.2;return ticks[0]
        stable={'owner':self.owner,'accounts':[],'account_vector_sha256':'0x'+'88'*32,'canonical_roots':{'roots':{'root':'0x'+'55'*32,'applied_root':'0x'+'55'*32}},'canonical_nonce_sum':600}
        with contextlib.ExitStack() as stack:
            mocks=[('bounded_fleet.bounded_plan_inputs',lambda *a:(self.doc,{'owner_final':{'Epoch':[9]+[0]*15}})),('recovery_fleet.baseline',lambda *a:({},'00'*32)),('fleet.quiet_checks',lambda *a,**k:None),('fleet.claim',claimed),('fleet.check_owned_claim',lambda *a,**k:None),('fleet.binary',lambda path:{'path':str(path),'sha256':'mock'}),('strict_fleet.validate',lambda *a:self.m),('subprocess.Popen',Process),('os.killpg',killpg),('os.sched_getaffinity',lambda pid:{0,1}),('time.monotonic',clock),('time.sleep',lambda _:None),('bounded_fleet._live_group',lambda pid:[pid] if next(p for p in processes if p.pid==pid).poll() is None else []),('shutil.disk_usage',lambda *a:type('Disk',(),{'free':1<<40})()),('pathlib.Path.exists',exists),('os.path.lexists',lexists),('fleet.status',lambda *a:{'identity':self.m['identity'],'producer_running':True,'owner_queue':{'epoch':self.epoch,'manifest_hash':self.plan['ManifestSHA256']}}),('bounded_fleet.window_evidence',lambda *a:600),('bounded_fleet.journal_evidence',lambda *a:None),('bounded_fleet.quiesce_producers',lambda *a:(_ for _ in ()).throw(ValueError('missing ACK')) if fault=='ack' else [{'validator':i,'ack':{'fresh_builds':i+1}}for i in range(7)]),('bounded_fleet.quiescent_owner_snapshot',lambda *a:(_ for _ in ()).throw(TimeoutError('not quiescent')) if fault=='quiescence' else copy.deepcopy(stable)),('bounded_fleet.stable_owner_snapshot',lambda *a:dict(stable,owner=dict(self.owner,CanonicalNumber=2)) if fault=='late' else stable),('bounded_fleet.bounded_reopened_evidence',lambda *a:600)]
            for target,value in mocks:stack.enter_context(patch(target,value))
            bins=[{'path':name,'sha256':'mock'} for name in ('node','owner','replay')]
            if fault:
                with self.assertRaises((ValueError,TimeoutError)):bounded.run(self.m,*bins,'bounded-q1',plan,self.sha,self.plan_id,proof,historical,self.h.failed)
            else:
                result=bounded.run(self.m,*bins,'bounded-q1',plan,self.sha,self.plan_id,proof,historical,self.h.failed)
                self.assertEqual(result['canonical_nonce_sum'],600);self.assertTrue(result['claim_cleanup']['all_owned_claims_absent'])
        self.assertTrue(all(p.poll() is not None for p in processes))
        self.assertEqual((self.root/'attempts'/'bounded-q1'/'bounded-fleet-result.json').exists(),not bool(fault))
        if not fault:self.assertEqual(len(processes),18);self.assertEqual([p.env['GOMAXPROCS'] for p in processes],['36','4']+['2']*16)

    def test_missing_barrier_ack_never_reopens(self):self.run_fixture('ack')
    def test_nonconvergence_never_reopens(self):self.run_fixture('quiescence')
    def test_late_import_after_stop_never_reopens(self):self.run_fixture('late')

    def test_production_ack_allows_different_positive_fresh_build_counters(self):
        outer=self
        for fault in (None,'epoch','fresh','ack','http'):
            created=[];closed=[]
            class Client:
                def __init__(self,*args,**kwargs):self.index=len(created);created.append(self)
                def request(self,method,path,body,headers):
                    outer.assertEqual((method,path),('POST','/v1/recovery-only'));self.request_value=json.loads(body)
                def getresponse(self):
                    value=dict(self.request_value,recovery_only=True,drain_acknowledged=True,fresh_builds=self.index+1)
                    if fault=='epoch':value['epoch']=[8]+[0]*15
                    if fault=='fresh':value['fresh_builds']=-1
                    if fault=='ack':value['drain_acknowledged']=False
                    return type('Response',(),{'status':503 if fault=='http' else 200,'read':lambda self,n:json.dumps(value).encode()})()
                def close(self):closed.append(self.index)
            with patch.object(fleet,'UnixHTTP',Client):
                if fault:
                    with self.assertRaises(ValueError):bounded.quiesce_producers(self.m,self.plan,self.candidate(),lambda *a:None)
                    self.assertEqual(closed,[0])
                else:
                    values=bounded.quiesce_producers(self.m,self.plan,self.candidate(),lambda *a:None)
                    self.assertEqual([a['ack']['fresh_builds']for a in values],list(range(1,8)))
                    self.assertEqual(closed,list(range(7)))

    def test_live_quiescence_waits_for_lagging_peers_and_preserves_window(self):
        candidate=self.candidate();frozen=copy.deepcopy(candidate)
        for sender in self.plan['Senders']:sender['Nonce']=25
        previous=[{'address':s['Address'],'nonce':25,'balance':'1000000000','code_hash':recovery_fleet.EMPTY_CODE_HASH,'exists':True}for s in self.plan['Senders']]
        accounts=copy.deepcopy(previous);accounts[0]['nonce']+=500;accounts[0]['balance']=str(1000000000-21001*500)
        owner=self.h.owner(4,'66',self.epoch);owner.update(NewlyAdmitted=500,CryptoVerified=500)
        roots={'root':'0x'+'77'*32,'applied_root':'0x'+'77'*32}
        acks=[{'validator':i,'ack':{'fresh_builds':i+1}}for i in range(7)]
        for fault in (None,'fresh','regress','reference-regress','initial-regress','root','deadline'):
            current=copy.deepcopy(candidate)
            if fault=='initial-regress':current['initial'][1]=self.canonical(3,'55',320)
            calls=[0];ticks=[0.]
            def now():ticks[0]+=.01;return ticks[0]
            def status(path,identity):
                index=calls[0]%7;round_=calls[0]//7;calls[0]+=1
                value=self.canonical(2,'44',320)if round_==0 and (index!=0 or fault=='reference-regress') else self.canonical(4,'66',600)
                value.update(recovery_only=True,drain_acknowledged=True,fresh_builds=index+1)
                if fault=='fresh':value['fresh_builds']+=1
                if fault=='regress'and round_>0:value=self.canonical(1,'33',100);value.update(recovery_only=True,drain_acknowledged=True,fresh_builds=index+1)
                return value
            read_calls=[0]
            def read(m,o,addresses):
                read_calls[0]+=1;r=dict(roots)
                if fault=='root'and read_calls[0]>1:r['root']=r['applied_root']='0x'+'88'*32
                if fault=='deadline':ticks[0]+=50
                return {'accounts':[a for a in accounts if a['address']in addresses],'roots':r}
            with patch.object(fleet,'status',status),patch.object(queue_fixture,'owner_status',lambda *a:owner),patch.object(bounded,'canonical_account_request',read),patch('time.monotonic',now),patch('time.sleep',lambda s:None):
                if fault:
                    with self.assertRaises((ValueError,TimeoutError)):bounded.quiescent_owner_snapshot(self.m,self.plan,{'accounts':previous},current,acks,lambda *a:None)
                else:
                    value=bounded.quiescent_owner_snapshot(self.m,self.plan,{'accounts':previous},current,acks,lambda *a:None)
                    self.assertEqual(value['canonical_nonce_sum'],600);self.assertEqual(len(value['validators']),7);self.assertGreaterEqual(calls[0],21)
            self.assertEqual(candidate,frozen)

    def test_clean_staged_run(self):self.run_fixture()
    def test_claim_tail_failure(self):self.run_fixture('tail')
    def test_claim_cleanup_residue(self):self.run_fixture('claim-residue')
    def test_nonzero_cleanup(self):self.run_fixture('nonzero')
    def test_forced_cleanup(self):self.run_fixture('forced')
    def test_window_failure(self):self.run_fixture('command')
    def test_socket_residue(self):self.run_fixture('socket')


if __name__=='__main__':unittest.main()
