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
import bounded_fleet
import queue_fixture
import post_bounded_quiescent as q
import resume_fleet
import test_post_finite_audit

class QuiescentRecoveryTest(unittest.TestCase):
    def setUp(self):
        f=test_post_finite_audit.PostFiniteAuditTest();f.setUp();self.addCleanup(f.doCleanups)
        self.f,self.h,self.root,self.m=f,f.h,f.root,f.m
        self.epoch=[4]+[0]*15
        self.retained={"floor":{"number":2,"hash":"0x"+"33"*32,"count":1900},"target":{"number":3,"hash":"0x"+"44"*32,"parent":"0x"+"33"*32,"transactions":100},"expected_count":2000}
        self.owner=self.h.owner(3,'44',self.epoch)
        self.accounts=[{'address':s['Address'],'nonce':500,'balance':str(1000000000-21001*(500-s['Nonce'])),'code_hash':'0x'+'c5'*32,'exists':True}for s in f.plan['Senders']]
        self.roots={'root':'0x'+'55'*32,'applied_root':'0x'+'55'*32}
        self.snapshot={'owner':self.owner,'accounts':self.accounts,'canonical_nonce_sum':2000,'account_vector_sha256':'0x'+'88'*32,'canonical_roots':{'canonical_hash':self.owner['CanonicalHash'],'canonical_number':3,'roots':self.roots,'account_batches':1,'final_read':True},'validators':[]}
    def status(self):
        s=self.h.status(3,2000,'44',self.epoch);s.update(producer_running=True,recovery_only=True,drain_acknowledged=True,fresh_builds=0)
        return s
    def test_all_validator_ack_bindings_and_zero_fresh_builds_required(self):
        value=self.status()
        q.recovery_validator(value,self.m,self.epoch,self.f.plan['ManifestSHA256'],self.retained,converged=True)
        for change in [lambda s:s.update(recovery_only=False),lambda s:s.update(drain_acknowledged=False),lambda s:s.update(fresh_builds=1),lambda s:s.update(producer_running=False),lambda s:s.update(committed_executed_transactions=1999),lambda s:s.update(block_hash='0x'+'77'*32),lambda s:s['owner_queue'].update(epoch=[9]+[0]*15)]:
            s=copy.deepcopy(value);change(s)
            with self.assertRaises(ValueError):q.recovery_validator(s,self.m,self.epoch,self.f.plan['ManifestSHA256'],self.retained,converged=True)
    def test_post_stop_snapshot_rejects_late_import_root_or_vector_change(self):
        q.exact_snapshot(copy.deepcopy(self.snapshot),self.snapshot)
        for change in [lambda s:s['owner'].update(CanonicalNumber=4),lambda s:s['canonical_roots']['roots'].update(root='0x'+'66'*32),lambda s:s['accounts'][0].update(nonce=501),lambda s:s.update(account_vector_sha256='0x'+'66'*32)]:
            s=copy.deepcopy(self.snapshot);change(s)
            with self.assertRaises(ValueError):q.exact_snapshot(s,self.snapshot)
    def test_capture_rejects_moving_final_root_or_fresh_build(self):
        plan=self.f.plan;historical={'accounts':[{'address':s['Address'],'nonce':s['Nonce'],'balance':'1000000000','code_hash':'0x'+'c5'*32,'exists':True}for s in plan['Senders']]}
        admission={'sender_new_transactions':[500,459,458,0]}
        for fault in ['root','fresh','nonce']:
            calls=[0]
            def accounts(*args):
                calls[0]+=1;r=copy.deepcopy(self.roots)
                if fault=='root'and calls[0]>1:r['root']=r['applied_root']='0x'+'66'*32
                a=copy.deepcopy(self.accounts)
                if fault=='nonce':a[0]['nonce']+=1
                return {'accounts':a,'roots':r}
            status=self.status()
            if fault=='fresh':status['fresh_builds']=1
            with patch.object(fleet,'status',lambda *a:status),patch.object(queue_fixture,'owner_status',lambda *a:self.owner),patch.object(bounded_fleet,'canonical_account_request',accounts):
                with self.assertRaises(ValueError):q.capture_snapshot(self.m,plan,historical,admission,self.epoch,self.retained,lambda *a:None,True)

    def test_private_acknowledgement_rejects_wrong_binding_or_nonzero_fresh_builds(self):
        for fault in (None,'epoch','manifest','fresh','ack','http'):
            closed=[]
            outer=self
            class Client:
                def __init__(self,*args,**kwargs):pass
                def request(self,method,path,body,headers):
                    outer.assertEqual((method,path),('POST','/v1/recovery-only'))
                    self.value=json.loads(body)
                def getresponse(self):
                    value=dict(self.value,recovery_only=True,drain_acknowledged=True,fresh_builds=0)
                    if fault=='epoch':value['epoch']=[9]+[0]*15
                    if fault=='manifest':value['manifest_hash']='0x'+'99'*32
                    if fault=='fresh':value['fresh_builds']=1
                    if fault=='ack':value['drain_acknowledged']=False
                    return type('Response',(),{'status':503 if fault=='http'else 200,'read':lambda self,n:json.dumps(value).encode()})()
                def close(self):closed.append(True)
            with patch.object(fleet,'UnixHTTP',Client):
                if fault:
                    with self.assertRaises(ValueError):q.acknowledge_recovery(self.m,self.epoch,self.f.plan['ManifestSHA256'],lambda *a:None)
                    self.assertEqual(len(closed),1)
                else:
                    self.assertEqual(len(q.acknowledge_recovery(self.m,self.epoch,self.f.plan['ManifestSHA256'],lambda *a:None)),7)
                    self.assertEqual(len(closed),7)
    def test_complete_live_snapshot_then_exact_post_stop_snapshot(self):
        plan=self.f.plan
        historical={'accounts':[{'address':s['Address'],'nonce':s['Nonce'],'balance':'1000000000','code_hash':'0x'+'c5'*32,'exists':True}for s in plan['Senders']]}
        admission={'sender_new_transactions':[500,459,458,0]}
        calls=[]
        def accounts(m,owner,addresses):
            calls.append(addresses)
            return {'accounts':[a for a in self.accounts if a['address']in addresses],'roots':self.roots}
        with patch.object(fleet,'status',lambda *a:self.status()),patch.object(queue_fixture,'owner_status',lambda *a:self.owner),patch.object(bounded_fleet,'canonical_account_request',accounts):
            live=q.capture_snapshot(self.m,plan,historical,admission,self.epoch,self.retained,lambda *a:None,True)
            stopped=q.capture_snapshot(self.m,plan,historical,admission,self.epoch,self.retained,lambda *a:None,False)
            q.exact_snapshot(stopped,live)
            self.assertEqual(live['canonical_nonce_sum'],2000)
            self.assertEqual(len(live['validators']),7)
            self.assertEqual(len(calls),4)

    def test_retained_transition_accepts_floor_then_target_rejects_regression_or_third_head(self):
        target=self.status();floor=copy.deepcopy(target)
        floor.update(number=2,block_hash=self.retained['floor']['hash'],committed_executed_transactions=1900)
        args=(self.m,self.epoch,self.f.plan['ManifestSHA256'],self.retained)
        q.recovery_validator(floor,*args)
        q.recovery_validator(target,*args,previous=floor,converged=True)
        with self.assertRaises(ValueError):q.recovery_validator(floor,*args,previous=target)
        for edit in ({'number':4},{'committed_executed_transactions':1999},{'fresh_builds':True},{'fresh_builds':1}):
            v=copy.deepcopy(target);v.update(edit)
            with self.assertRaises(ValueError):q.recovery_validator(v,*args)

    def test_partial_known_ack_bounds_accept_actual_delta_and_reject_overshoot_or_balance(self):
        plan=self.f.plan;old=[{'address':s['Address'],'nonce':s['Nonce'],'balance':'1000000000','code_hash':'0x'+'c5'*32,'exists':True}for s in plan['Senders']]
        historical={'accounts':old};admission={'sender_new_transactions':[500,459,458,0]}
        accounts=copy.deepcopy(old);accounts[0].update(nonce=1,balance=str(1000000000-21001))
        count=sum(a['nonce']for a in accounts)
        self.assertTrue(q.bounded_account_evidence(accounts,historical,plan,admission,count).startswith('0x'))
        for edit in ({'nonce':501,'balance':str(1000000000-21001*501)},{'balance':'1'},{'exists':False}):
            v=copy.deepcopy(accounts);v[0].update(edit)
            with self.assertRaises(ValueError):q.bounded_account_evidence(v,historical,plan,admission,sum(a['nonce']for a in v))
        with self.assertRaises(ValueError):q.bounded_account_evidence(accounts[::-1],historical,plan,admission,count)

    def retained_fixture(self):
        directory=self.root/'ordinary';directory.mkdir(mode=0o700)
        logs=directory/'metadata-logs';logs.mkdir(mode=0o700)
        record={'Operation':'applied_child','Outcome':'ok','Identity':self.m['identity'],'Number':3,'Parent':self.retained['floor']['hash'],'Hash':self.retained['target']['hash'],'Transactions':100,'PhaseNs':{'durable_evidence':1}}
        owner=directory/'first-owner.log';owner.write_text(('prefix=sharedexec record='+json.dumps(json.dumps(record))+'\n')*7);owner.chmod(0o600)
        paths=[owner]
        for i in range(7):
            path=logs/('v'+str(i)+'.n42.log');rows=[{'msg':'hotstuff: recovering persisted state','lockedQCView':10,'committedQCView':10,'view':11,'lastVotedView':11},{'msg':'hotstuff: persisted consensus state agrees with the applied chain','height':2,'localHead':2,'committedQCBlock':'333333…333333'}]
            path.write_text('\n'.join(json.dumps(v)for v in rows)+'\n');path.chmod(0o600);paths.append(path)
        failure=dict(kind='gov5-post-bounded-readonly-recovery-failure-independent-sol-acceptance-v1',failure_verified=True,recovery_converged=False,full_canonical_account_snapshot_obtained=False,dispatched_transactions=0,second_reopen_performed=False,canonical_last_metadata={'number':2,'block_hash':self.retained['floor']['hash'],'committed_executed_transactions':1900},evidence_sha256={str(path):hashlib.sha256(path.read_bytes()).hexdigest()for path in paths})
        failed=directory/'failure.json';evidence=directory/'retained.json'
        def publish():
            self.f.write(failed,failure)
            value=dict(kind='gov5-post-bounded-retained-child-evidence-v1',ordinary_failure_path=str(failed),ordinary_failure_sha256=hashlib.sha256(failed.read_bytes()).hexdigest(),target=self.retained['target'])
            self.f.write(evidence,value)
            return evidence,hashlib.sha256(evidence.read_bytes()).hexdigest()
        return paths,failure,publish

    def test_retained_evidence_rederives_target_from_seven_actual_log_records(self):
        paths,failure,publish=self.retained_fixture();path,digest=publish()
        admission={'reference_final':dict(number=2,block_hash=self.retained['floor']['hash'],committed_executed_transactions=1900)}
        retained,pins=q.retained_inputs(path,digest,admission,self.m)
        self.assertEqual(retained,self.retained);self.assertEqual(len(pins),9)
        value=json.loads(path.read_text());value['target']['transactions']+=1;self.f.write(path,value)
        with self.assertRaisesRegex(ValueError,'target differs'):q.retained_inputs(path,hashlib.sha256(path.read_bytes()).hexdigest(),admission,self.m)

    def test_retained_evidence_rejects_changed_log_or_conflicting_lock(self):
        paths,failure,publish=self.retained_fixture();path,digest=publish()
        admission={'reference_final':dict(number=2,block_hash=self.retained['floor']['hash'],committed_executed_transactions=1900)}
        log=paths[1];original=log.read_text();log.write_text(original+' ')
        with self.assertRaisesRegex(ValueError,'evidence changed'):q.retained_inputs(path,digest,admission,self.m)
        rows=[json.loads(line)for line in original.splitlines()];rows[0]['lockedQCView']+=1
        log.write_text('\n'.join(json.dumps(row)for row in rows)+'\n');failure['evidence_sha256'][str(log)]=hashlib.sha256(log.read_bytes()).hexdigest();path,digest=publish()
        with self.assertRaisesRegex(ValueError,'lock/committed'):q.retained_inputs(path,digest,admission,self.m)

    def run_fixture(self,fault=None):
        processes=[];outer=self
        class Process:
            def __init__(self,args,**kwargs):
                self.args,self.returncode,self.pid=args,None,91000000+len(processes);processes.append(self)
                if len(args)>1 and args[1]=='recover':
                    attempt=args[args.index('--attempt')+1];d=outer.root/'strict-attempts'/attempt;d.mkdir(mode=0o700)
                    record=copy.deepcopy(outer.snapshot);record['owner_initial']=dict(outer.owner,Epoch=[len(processes)]+[0]*15);record['owner_final']=dict(record['owner_initial'])
                    if fault=='reopen-root':record['canonical_roots']['roots']['root']='0x'+'99'*32
                    outer.f.write(d/'recovery.json',record);self.returncode=0
            def poll(self):return self.returncode
            def wait(self,timeout=None):
                if self.returncode is None:raise subprocess.TimeoutExpired(self.args,timeout)
                return self.returncode
        def killpg(pid,sig):
            proc=next(p for p in processes if p.pid==pid)
            if sig==signal.SIGKILL:proc.returncode=-9
            elif fault=='forced'and proc.args[0]=='owner':pass
            else:proc.returncode=1 if fault=='nonzero'and proc.args[0]=='owner'else 0
        claim=self.root/'mock-claim';mirror=self.root/'mock-mirror'
        @contextlib.contextmanager
        def claimed():
            claim.mkdir(mode=0o700);mirror.write_text('123\n');mirror.chmod(0o600)
            old=fleet._active_claim;fleet._active_claim={'path':claim,'token':'mock-token','files':[(mirror,mirror.stat().st_dev,mirror.stat().st_ino)]}
            try:
                yield {'load1':0,'available_gib':100}
                if fault=='tail':raise fleet.HardwareContention('tail changed')
            finally:
                fleet._active_claim=old
                if fault!='claim-residue':mirror.unlink();claim.rmdir()
        paths=[self.root/n for n in ('plan.json','historical.json','candidate.json','failure.json','drain.json','phase.json')]
        for path in paths:self.f.write(path,{'segments':[]})
        for i in range(64):self.f.write(self.root/f'ack-shard-{i:02d}.jsonl',{})
        ticks=[0.]
        def mono():ticks[0]+=.2;return ticks[0]
        def group(pid):
            proc=next((p for p in processes if p.pid==pid),None)
            if proc is not None and proc.poll()is None:return [pid]
            if fault=='group'and proc is not None and proc.args[0]=='owner':return [pid+100]
            return []
        exists=Path.exists;lexists=os.path.lexists
        def leftover(path):
            if fault=='socket'and str(path)==self.m['owner_socket']and len(processes)==26 and all(p.poll()is not None for p in processes):return True
            return lexists(path)
        def capture(*args):
            if fault=='live':raise TimeoutError('no canonical/applied convergence')
            s=copy.deepcopy(self.snapshot)
            if fault=='late'and args[-1]is False:s['owner']['CanonicalNumber']+=1
            return s
        def ack(*args):
            if fault=='ack':raise ValueError('missing seventh ACK')
            return [{'validator':i,'ack':{'recovery_only':True,'drain_acknowledged':True,'fresh_builds':0}}for i in range(7)]
        bins=[{'path':p,'sha256':'00'*32}for p in ('node','owner','replay')]
        projected={'owner_initial':{'Epoch':self.f.epoch},'reference_final':{'number':2,'block_hash':'0x'+'33'*32,'committed_executed_transactions':1900},'sender_new_transactions':[500,459,458,0]}
        with contextlib.ExitStack()as stack:
            mocks=[(fleet,'quiet_checks',lambda:None),(fleet,'claim',claimed),(fleet,'check_owned_claim',lambda *a,**k:None),(fleet,'binary',lambda p:{'path':str(p),'sha256':'00'*32}),(q.bounded_fleet,'bounded_plan_inputs',lambda *a:(self.f.doc,{})),(q,'failed_window_inputs',lambda *a:(projected,dict(new=1417,skip=583))),(q,'retained_inputs',lambda *a:(self.retained,{})),(q,'first_audit_evidence',lambda *a:2000),(q,'second_audit_evidence',lambda *a:2000),(q,'acknowledge_recovery',ack),(q,'capture_snapshot',capture),(q,'_live_group',group),(resume_fleet.strict_fleet,'validate',lambda *a:self.m),(queue_fixture,'owner_status',lambda *a:self.owner)]
            for target,name,value in mocks:stack.enter_context(patch.object(target,name,value))
            for target,value in [('subprocess.Popen',Process),('os.killpg',killpg),('os.sched_getaffinity',lambda pid:{0,1}),('os.path.lexists',leftover),('shutil.disk_usage',lambda p:type('Disk',(),{'free':100<<30})()),('time.monotonic',mono),('time.sleep',lambda s:None)]:stack.enter_context(patch(target,value))
            stack.enter_context(patch.object(Path,'exists',lambda p:p.suffix=='.sock'or exists(p)))
            args=(self.m,*bins,'recover-q1',paths[0],self.f.sha,self.f.plan_id,paths[1],self.h.failed,paths[2],'00'*32,paths[3],'00'*32,paths[4],'00'*32,paths[5],'00'*32)
            output=self.root/'attempts/recover-q1/quiescent-recovery-result.json'
            if fault:
                with self.assertRaises((OSError,ValueError,fleet.HardwareContention)):q.run(*args)
                self.assertFalse(output.exists())
            else:q.run(*args);self.assertTrue(output.exists())
        self.assertTrue(all(p.poll()is not None for p in processes))
        return processes
    def test_recovery_then_two_reopens_clean_all26(self):
        processes=self.run_fixture();self.assertEqual(len(processes),26)
        for p in processes:self.assertNotIn('bounded-window',p.args);self.assertNotIn('--resume-ingress-plan',p.args)
        directory=self.root/'attempts/recover-q1'
        for path in directory.glob('*-v[0-6].json'):
            node=json.loads(path.read_text())['node']
            if path.name.startswith('recovery-'):self.assertIs(node['miner'],True);self.assertIs(node['performance_recovery_only'],True)
            else:self.assertIs(node['miner'],False);self.assertNotIn('performance_recovery_only',node)
    def test_missing_ack_never_enters_reopen(self):self.assertEqual(len(self.run_fixture('ack')),8)
    def test_nonconvergence_never_enters_reopen(self):self.assertEqual(len(self.run_fixture('live')),8)
    def test_late_import_after_validator_stop_prevents_reopen(self):self.assertEqual(len(self.run_fixture('late')),8)
    def test_reopen_root_change_not_promoted(self):self.run_fixture('reopen-root')
    def test_claim_tail_failure_not_promoted(self):self.run_fixture('tail')
    def test_claim_cleanup_residue_not_promoted(self):self.run_fixture('claim-residue')
    def test_nonzero_exit_not_promoted(self):self.run_fixture('nonzero')
    def test_forced_kill_not_promoted(self):self.run_fixture('forced')
    def test_residual_group_not_promoted(self):self.run_fixture('group')
    def test_socket_residue_not_promoted(self):self.run_fixture('socket')

if __name__=='__main__':unittest.main()
