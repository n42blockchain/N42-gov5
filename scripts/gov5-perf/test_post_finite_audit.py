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

import fleet
import queue_fixture
import post_finite_audit
import recovery_fleet
import resume_fleet
import test_recovery_fleet


class PostFiniteAuditTest(unittest.TestCase):
    def setUp(self):
        h = test_recovery_fleet.RecoveryFleetTest()
        h.setUp()
        self.addCleanup(h.doCleanups)
        self.h, self.root, self.m, self.write = h, h.root, h.m, h.write
        self.epoch = [3] + [0] * 15
        self.sha, self.plan_id = '11' * 32, '0x' + '77' * 32
        self.runtime_sha = hashlib.sha256((self.root/'run.json').read_bytes()).hexdigest()
        senders = [{'Index':i,'Address':'0x'+f'{i+1:040x}','Nonce':n} for i,n in enumerate([0,41,42,500])]
        frames = [{'ID':{'CorpusID':self.m['supply']['corpus_id'],'Shard':0,'Sequence':i},'SenderIndex':i,'PayloadHash':'0x'+f'{i+1:02x}'*32,'PayloadBytes':52000,'CanonicalSkipped':s['Nonce'],'NewlyAdmitted':500-s['Nonce']} for i,s in enumerate(senders)]
        self.plan = {'Version':2,'NonceEndExclusive':1000,'Identity':self.m['identity'],'ManifestSHA256':'0x'+self.m['supply']['manifest_sha256'],'CorpusID':self.m['supply']['corpus_id'],'GenesisFileSHA256':'0x'+self.m['genesis_sha256'],'Canonical':{'Number':1,'Hash':'0x'+'33'*32,'Root':'0x'+'55'*32},'CanonicalNonceSum':583,'Senders':senders,'Frames':frames}
        self.doc = {'kind':'gov5-bounded-resume-prefix-plan-v2','plan':self.plan,'plan_id':self.plan_id}
        first = h.owner(1,'33',self.epoch)
        final = dict(first,NewlyAdmitted=1417,CryptoVerified=2000,PendingTransactions=1417,RetainedEncodedBytes=156000)
        receipts=[];pending=retained=0
        for i,f in enumerate(frames):
            pending+=f['NewlyAdmitted'];retained+=f['PayloadBytes'] if f['NewlyAdmitted'] else 0
            status=dict(first,NewlyAdmitted=pending,PendingTransactions=pending,RetainedEncodedBytes=retained,CryptoVerified=(i+1)*500)
            receipts.append({'frame':f,'ack':{'PlanID':self.plan_id,'Status':status,'Duplicate':False,'NewlyAdmitted':f['NewlyAdmitted'],'CanonicalSkipped':f['CanonicalSkipped'],'PriorAdmission':f['NewlyAdmitted']}})
        self.candidate={'kind':'gov5-resume-four-frame-admission-candidate-v1','passed':True,'identity':self.m['identity'],'runtime_manifest':str(self.root/'run.json'),'runtime_sha256':'0x'+self.runtime_sha,'plan_sha256':'0x'+self.sha,'plan_id':self.plan_id,'owner_initial':first,'owner_final':final,'acknowledgments':receipts,'production_started':False,'commit_proof_included':False,'canonical_skipped':583,'newly_admitted':1417,'known_zero_retries':0}

    def test_first_snapshot_accepts_only_complete_exact_ack_delta(self):
        previous = [{'address':s['Address'],'nonce':s['Nonce'],'balance':'1000000000','code_hash':recovery_fleet.EMPTY_CODE_HASH,'exists':True} for s in self.plan['Senders']]
        accounts = copy.deepcopy(previous)
        increments = [200,100,100,0]
        actual = increments
        for account, extra in zip(accounts, actual):
            account['nonce'] += extra
            account['balance'] = str(int(account['balance'])-21001*extra)
        owner = self.h.owner(2,'44',[4]+[0]*15)
        roots = {'root':'0x'+'55'*32,'applied_root':'0x'+'55'*32}
        value = {'accounts':accounts,'owner_initial':owner,'owner_final':owner,'canonical_roots':{'roots':roots,'canonical_hash':owner['CanonicalHash'],'canonical_number':2,'account_batches':1,'final_read':True},'account_vector_sha256':'0x'+hashlib.sha256(json.dumps(accounts,separators=(',',':')).encode()).hexdigest()}
        historical = {'owner_final':{'Epoch':[9]+[0]*15},'accounts':previous}
        candidate = {'owner_initial':{'Epoch':self.epoch},'reference_final':{'number':2,'block_hash':owner['CanonicalHash'],'committed_executed_transactions':983},'sender_new_transactions':increments}
        with patch.object(recovery_fleet,'evidence',lambda *a:983):
            self.assertEqual(post_finite_audit.first_audit_evidence(value,self.doc,historical,self.m,{},self.h.failed,'00'*32,candidate),983)
            for mutate in [lambda x:x['accounts'][1].update(nonce=142),lambda x:x['accounts'][0].update(nonce=-1),lambda x:x['accounts'][2].update(balance='1000000000'),lambda x:x['canonical_roots']['roots'].update(applied_root='0x'+'66'*32),lambda x:x['canonical_roots'].update(account_batches=2),lambda x:x['canonical_roots'].update(final_read=False),lambda x:x['owner_final'].update(CanonicalNumber=1),lambda x:x['owner_initial'].update(Epoch=self.epoch)]:
                v=copy.deepcopy(value);mutate(v)
                with self.assertRaises(ValueError):post_finite_audit.first_audit_evidence(v,self.doc,historical,self.m,{},self.h.failed,'00'*32,candidate)
            second=copy.deepcopy(value);second['owner_initial']=dict(owner,Epoch=[5]+[0]*15);second['owner_final']=dict(second['owner_initial'])
            self.assertEqual(post_finite_audit.second_audit_evidence(second,value,self.doc,historical,self.m,{},self.h.failed,'00'*32,candidate),983)
            for mutate in [lambda x:x['owner_initial'].update(Epoch=owner['Epoch']),lambda x:x['owner_final'].update(CanonicalNumber=3),lambda x:x.update(account_vector_sha256='0x'+'99'*32)]:
                v=copy.deepcopy(second);mutate(v)
                with self.assertRaises(ValueError):post_finite_audit.second_audit_evidence(v,value,self.doc,historical,self.m,{},self.h.failed,'00'*32,candidate)
        for count in (982,984):
            with patch.object(recovery_fleet,'evidence',lambda *a:count):
                with self.assertRaises(ValueError):post_finite_audit.first_audit_evidence(value,self.doc,historical,self.m,{},self.h.failed,'00'*32,candidate)

    def test_failed_finite_input_projection_rejects_changed_drain_or_failure_contract(self):
        candidate={'owner_initial':{'Epoch':self.epoch},'sender_new_transactions':[200,100,100,0]}
        reference=self.h.status(2,983,'44',self.epoch);reference['producer_running']=True
        owner=self.h.owner(2,'44',self.epoch);owner['NewlyAdmitted']=400
        drain={'passed':True,'target_count':983,'samples':[{'reference':reference,'owner':owner}]}
        totals={'frames':2,'original':1000,'new':400,'skip':600,'bytes':105000}
        phase={'segments':[],'canonical_163000_blocks':[{'synthetic':True}]}
        accepted={'kind':'gov5-finite-phase-v2-stable-snapshot-failure-independent-sol-acceptance-v1','failure_verified':True,'functional_gate_passed':False,'capacity_5m_proven':False,'tps_measured':False,'candidate_sha256':None,'all_owned_processes_gone':True,'owned_claims_released':True,'all_sockets_absent':True,'physical_reopen_performed':False,'stable_before_stop_snapshot_obtained':False,'drain_passed':True,'journal_totals':totals,'canonical_floor':reference}
        changes=[None,lambda a,d:a.update(kind='old-window-failure'),lambda a,d:a.update(all_owned_processes_gone=False),lambda a,d:a.update(stable_before_stop_snapshot_obtained=True),lambda a,d:a.update(physical_reopen_performed=True),lambda a,d:a.update(candidate_sha256='00'*32),lambda a,d:d.update(passed=False),lambda a,d:d.update(target_count=982),lambda a,d:d['samples'][0]['reference'].update(committed_executed_transactions=982),lambda a,d:d['samples'][0]['owner'].update(PendingTransactions=1)]
        for i,change in enumerate(changes):
            a,d=copy.deepcopy(accepted),copy.deepcopy(drain)
            paths=[self.root/(f'input-{i}-{n}.json')for n in range(4)]
            self.write(paths[0],candidate)
            a['candidate_sha256']=hashlib.sha256(paths[0].read_bytes()).hexdigest()
            if change:change(a,d)
            for path,value in zip(paths[1:],[a,d,phase]):self.write(path,value)
            hashes=[hashlib.sha256(path.read_bytes()).hexdigest()for path in paths]
            with patch.object(post_finite_audit.finite_fleet,'preload_evidence',lambda *args:983),patch.object(post_finite_audit.finite_fleet,'finite_journal_evidence',lambda *args:totals),patch.object(post_finite_audit.finite_fleet,'phase_evidence',lambda *args:phase):
                args=(paths[0],hashes[0],paths[1],hashes[1],paths[2],hashes[2],paths[3],hashes[3],self.doc,{},self.m,self.sha)
                if change:
                    with self.assertRaises(ValueError):post_finite_audit.failed_finite_inputs(*args)
                else:
                    projection,got=post_finite_audit.failed_finite_inputs(*args)
                    self.assertEqual(got,totals);self.assertEqual(projection['reference_final'],reference)

    def run_fixture(self,fault=None):
        processes=[];outer=self
        class Process:
            def __init__(self,args,**kwargs):
                self.args,self.returncode,self.pid=args,None,90000000+len(processes);processes.append(self)
                if len(args)>1 and args[1]=='recover':
                    name=args[args.index('--attempt')+1]
                    assert '--require-canonical-roots' in args
                    p=outer.root/'strict-attempts'/name;p.mkdir(mode=0o700)
                    outer.write(p/'recovery.json',{'canonical_nonce_sum':2000,'account_vector_sha256':'0x'+'88'*32,'accounts':([{'changed':True}] if fault=='second-vector' and name.endswith('-reopen') else []), 'owner_final':{'Epoch':[len(processes)]+[0]*15}, 'canonical_roots':{'roots':{'root':'0x'+'55'*32,'applied_root':'0x'+'55'*32}}});self.returncode=0
            def poll(self):return self.returncode
            def wait(self,timeout=None):
                if self.returncode is None:raise subprocess.TimeoutExpired(self.args,timeout)
                return self.returncode
        def killpg(pid,sig):
            p=next(p for p in processes if p.pid==pid)
            if sig==signal.SIGKILL:p.returncode=-9
            elif fault=='forced' and p.args[0]=='owner':pass
            else:p.returncode=1 if fault=='nonzero' and p.args[0]=='owner' else 0
        private=self.root/'mock-claim';mirror=self.root/'mock-mirror'
        @contextlib.contextmanager
        def claimed():
            private.mkdir(mode=0o700);mirror.write_text('123\n');mirror.chmod(0o600)
            old=fleet._active_claim;fleet._active_claim={'path':private,'token':'mock-token','files':[(mirror,mirror.stat().st_dev,mirror.stat().st_ino)]}
            try:
                yield {'load1':0,'available_gib':100}
                if fault=='tail':raise fleet.HardwareContention('tail changed')
            finally:
                fleet._active_claim=old
                if fault!='claim-residue':mirror.unlink();private.rmdir()
        plan=self.root/'fixture-plan.json';proof=self.root/'fixture-proof.json';hist=self.root/'fixture-historical.json'
        admission=self.root/'acknowledged.json';self.write(admission,self.candidate)
        admission_sha=hashlib.sha256(admission.read_bytes()).hexdigest()
        for i in range(64):self.write(self.root/f'ack-shard-{i:02d}.jsonl',{})
        failure=self.root/'failure.json';supplement=self.root/'drain.json';phase=self.root/'phase.json'
        self.write(failure,{});self.write(supplement,{});self.write(phase,{'segments':[]})
        for path,value in [(plan,self.doc),(proof,{'synthetic':True}),(hist,{'synthetic':True})]:self.write(path,value)
        self.plan['SourceProofSHA256']='0x'+hashlib.sha256(proof.read_bytes()).hexdigest()
        status=self.h.owner(2,'44',self.epoch);status.update(NewlyAdmitted=1417,CryptoVerified=2000)
        ticks=[0.]
        def monotonic():ticks[0]+=.2;return ticks[0]
        exists=Path.exists
        def ready(p):return p.suffix=='.sock' or exists(p)
        lexists=os.path.lexists
        def leftover(p):
            if fault=='reopen-socket' and str(p)==self.m['owner_socket'] and len(processes)==18 and all(x.poll()is not None for x in processes):return True
            return lexists(p)
        def group(pid):
            p=next((p for p in processes if p.pid==pid),None)
            if not p:return []
            if fault=='group-residue' and p.args[0]=='owner' and p.poll()is not None:return [pid+100]
            return [pid]if p.poll()is None else []
        bins=[{'path':p,'sha256':'00'*32}for p in ['node','owner','replay']]
        with contextlib.ExitStack()as stack:
            mocks=[(fleet,'quiet_checks',lambda:None),(fleet,'claim',claimed),(fleet,'check_owned_claim',lambda *a,**k:None),(fleet,'binary',lambda p:{'path':str(p),'sha256':'00'*32}),(fleet,'status',lambda *a:self.h.status(2,2000,'44',self.epoch)),(post_finite_audit.bounded_fleet,'bounded_plan_inputs',lambda *a:(self.doc,{'owner_final':{'Epoch':[9]+[0]*15}})),(post_finite_audit,'failed_finite_inputs',lambda *a:({'reference_final':{'committed_executed_transactions':983}},dict(new=1417,skip=583))),(post_finite_audit,'second_audit_evidence',lambda *a:2000),(post_finite_audit,'first_audit_evidence',lambda *a:(_ for _ in ()).throw(ValueError('first canonical roots invalid')) if fault=='first-root' else 2000),(post_finite_audit,'_live_group',group),(resume_fleet.strict_fleet,'validate',lambda *a:self.m),(queue_fixture,'owner_status',lambda *a:status)]
            for target,name,value in mocks:stack.enter_context(patch.object(target,name,value))
            for target,value in [('subprocess.Popen',Process),('os.killpg',killpg),('os.sched_getaffinity',lambda p:{0,1}),('os.path.lexists',leftover),('shutil.disk_usage',lambda p:type('Disk',(),{'free':100<<30})()),('time.monotonic',monotonic),('time.sleep',lambda s:None)]:stack.enter_context(patch(target,value))
            stack.enter_context(patch.object(Path,'exists',ready))
            result=self.root/'attempts/resume-q1/post-finite-audit-result.json'
            if fault:
                with self.assertRaises((ValueError,fleet.HardwareContention)):post_finite_audit.run(self.m,*bins,'resume-q1',plan,self.sha,self.plan_id,hist,self.h.failed,admission,admission_sha,failure,'00'*32,supplement,'00'*32,phase,'00'*32)
                self.assertFalse(result.exists())
            else:
                post_finite_audit.run(self.m,*bins,'resume-q1',plan,self.sha,self.plan_id,hist,self.h.failed,admission,admission_sha,failure,'00'*32,supplement,'00'*32,phase,'00'*32)
                self.assertTrue(result.exists())
        self.assertTrue(all(p.poll()is not None for p in processes))
        return processes

    def test_first_roots_failure_stops_before_second_reopen(self):self.assertEqual(len(self.run_fixture('first-root')),9)
    def test_second_vector_change_not_promoted(self):self.run_fixture('second-vector')

    def test_two_clean_phases(self):
        processes=self.run_fixture()
        self.assertEqual(len(processes),18)
        for process in processes:
            self.assertNotIn('resume-four-frame',process.args)
            self.assertNotIn('--resume-ingress-plan',process.args)
        for p in self.root.glob('attempts/resume-q1/*-v*.json'):
            self.assertIs(json.loads(p.read_text())['node']['miner'],False)
    def test_claim_tail_failure_not_promoted(self):self.run_fixture('tail')
    def test_claim_cleanup_residue_not_promoted(self):self.run_fixture('claim-residue')
    def test_reopen_socket_residue_not_promoted(self):self.run_fixture('reopen-socket')
    def test_nonzero_exit_not_promoted(self):self.run_fixture('nonzero')
    def test_forced_exit_not_promoted(self):self.run_fixture('forced')
    def test_residual_group_not_promoted(self):self.run_fixture('group-residue')


if __name__=="__main__":unittest.main()
