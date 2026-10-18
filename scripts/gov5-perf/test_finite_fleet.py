import contextlib
import copy
import hashlib
import json
import os
from pathlib import Path
import signal
import struct
import subprocess
import unittest
from unittest.mock import patch

import bounded_fleet
import finite_fleet as finite
import fleet
import test_bounded_fleet


class FiniteFleetTest(unittest.TestCase):
    def setUp(self):
        h = test_bounded_fleet.BoundedFleetTest(); h.setUp(); self.addCleanup(h.doCleanups)
        self.h, self.root, self.m, self.write = h, h.root, h.m, h.write

    def candidate(self):
        h = self.h
        p = dict(h.plan, CanonicalNonceSum=0, NonceEndExclusive=10000, MaxSourceFrames=120000, MaxSourceTransactions=60000000, MaxSourceEncodedBytes=6370080000, SourceEncodedBytes=6367069694, MaxNewTransactions=60000000, Senders=[{'Index':i,'Address':'0x'+f'{i+1:040x}','Nonce':0} for i in range(6000)])
        doc = {'plan_id': h.plan_id, 'plan':p}
        frames, senders, shards = [], [0]*6000, [0]*64
        digest = hashlib.sha256(b'gov5/finite-phase-schedule/v1\0'+bytes.fromhex(h.plan_id[2:])+struct.pack('>QQ',200000,400))
        for i in range(400):
            shard, seq = i%64, i//64
            f={'ID':{'CorpusID':p['CorpusID'],'Shard':shard,'Sequence':seq},'SenderIndex':i,'Address':p['Senders'][i]['Address'],'FirstNonce':0,'Transactions':500,'PayloadHash':'0x'+f'{i+1:064x}','PayloadBytes':51000,'CanonicalSkipped':0,'NewlyAdmitted':500}
            digest.update(bytes.fromhex(p['CorpusID'][2:])+struct.pack('>HQI',shard,seq,i)+bytes.fromhex(f['Address'][2:])+struct.pack('>QH',0,500)+bytes.fromhex(f['PayloadHash'][2:])+struct.pack('>IHH',51000,0,500))
            frames.append(f);senders[i]=500;shards[shard]+=1
        s={'plan_id':h.plan_id,'schedule_sha256':'0x'+digest.hexdigest(),'target_new_transactions':200000,'newly_admitted':200000,'canonical_skipped':0,'original_transactions':200000,'encoded_bytes':400*51084,'retained_encoded_bytes':400*51000,'frames_per_shard':shards,'sender_new_transactions':senders,'frames':frames}
        initial=copy.deepcopy(h.owner)
        final=dict(initial,NewlyAdmitted=200000,PendingTransactions=200000,RetainedEncodedBytes=s['retained_encoded_bytes'],CryptoVerified=200000)
        v={'kind':'gov5-bounded-finite-preload-candidate-no-tps-v1','passed':True,'identity':self.m['identity'],'plan_id':h.plan_id,'plan_sha256':'0x'+h.sha,'runtime_sha256':'0x'+h.runtime_sha,'runtime_manifest':str(self.root/'run.json'),'schedule':s,'owner_initial':initial,'owner_final':final,'stream_final':{'acknowledged_unique_admitted':200000,'acknowledged_canonical_skipped':0,'acknowledged_original_frames':400,'source_read_transactions':200000,'source_read_encoded_bytes':s['encoded_bytes'],'busy_retries':0,'snapshot_retries':0},'sender_lifetime_budget':{'charged_original_frames':400,'charged_original_transactions':200000,'charged_original_encoded_bytes':s['encoded_bytes'],'charged_new_transactions':200000},'sender_new_transactions':senders,'consumed_prefixes':[{'next_sequence':n,'admitted_transactions':n*500} for n in shards],'source_files_before':[],'source_files_after':[],'full_source_integrity_verified':True,'production_started':False,'commit_proof_included':False,'tps_measured':False,'capacity_5m_proven':False}
        return v,doc

    def test_preload_recomputes_schedule_derived_values_and_finite_boundary(self):
        v,doc=self.candidate();h=self.h
        self.assertEqual(finite.preload_evidence(v,doc,self.m,h.sha,h.runtime_sha),200000)
        for mutate in [lambda x:x['schedule'].update(newly_admitted=200001),lambda x:x['schedule']['frames_per_shard'].__setitem__(0,0),lambda x:x['schedule']['sender_new_transactions'].__setitem__(0,0),lambda x:x['schedule']['frames'][1]['ID'].update(Sequence=1),lambda x:x['schedule']['frames'][0].update(PayloadHash='0x'+'99'*32),lambda x:x.update(production_started=True),lambda x:x['owner_final'].update(CanonicalNumber=2),lambda x:x['owner_final'].update(PendingTransactions=199999),lambda x:x.update(tps_measured=True),lambda x:x['stream_final'].update(source_read_transactions=200500)]:
            bad=copy.deepcopy(v);mutate(bad)
            with self.assertRaises(ValueError):finite.preload_evidence(bad,doc,self.m,h.sha,h.runtime_sha)

    def test_journals_bind_exact_schedule_payload_and_lifetime(self):
        v,doc=self.candidate();p=doc['plan']
        directory=self.root/'journals';directory.mkdir(mode=0o700)
        lines=[[] for _ in range(64)]
        for i,f in enumerate(v['schedule']['frames']):
            n=i+1
            life={'MaxSourceFrames':p['MaxSourceFrames'],'MaxSourceTransactions':p['MaxSourceTransactions'],'MaxSourceEncodedBytes':p['SourceEncodedBytes'],'MaxNewTransactions':p['MaxNewTransactions'],'SourceFrames':n,'SourceTransactions':500*n,'SourceEncodedBytes':51084*n,'NewTransactions':500*n,'ReservedFrames':0,'ReservedTransactions':0,'ReservedEncodedBytes':0,'ReservedNew':0}
            ack={'PlanID':doc['plan_id'],'Status':dict(v['owner_initial'],NewlyAdmitted=500*n,PendingTransactions=500*n,RetainedEncodedBytes=51000*n),'Duplicate':False,'NewlyAdmitted':500,'PriorAdmission':500,'CanonicalSkipped':0,'Lifetime':life}
            lines[f['ID']['Shard']].append({'frame':f,'ack':ack})
        for shard,values in enumerate(lines):
            path=directory/f'ack-shard-{shard:02d}.jsonl';path.write_text(''.join(json.dumps(x)+'\n' for x in values));path.chmod(0o600)
        self.assertEqual(finite.finite_journal_evidence(directory,v,doc)['new'],200000)
        for mutate in [lambda x:x[-1]['frame'].update(PayloadHash='0x'+'99'*32),lambda x:x[0]['ack']['Status'].update(CanonicalNumber=2),lambda x:x[0]['ack']['Lifetime'].update(SourceFrames=2,SourceTransactions=1000),lambda x:x[0]['ack']['Status'].update(PendingTransactions=501),lambda x:x[0]['ack']['Lifetime'].update(SourceEncodedBytes=51085)]:
            changed=copy.deepcopy(lines[0]);mutate(changed)
            (directory/'ack-shard-00.jsonl').write_text(''.join(json.dumps(x)+'\n' for x in changed))
            with self.assertRaises(ValueError):finite.finite_journal_evidence(directory,v,doc)

    def phase_records(self):
        parent,block='0x'+'11'*32,'0x'+'22'*32
        def record(operation,validator,path='',tx=0,durable=False):
            return {'SchemaVersion':1,'Operation':operation,'Path':path,'Outcome':'ok','LastPhase':'done','StartedUnixMicros':1,'TotalNs':10,'AccountedNs':8,'UnaccountedNs':2,'PhaseNs':{'work':8},'Validator':self.m['validators'][validator]['address'],'View':2,'Number':3,'Parent':parent,'Hash':block,'Durable':durable,'Transactions':tx,'RawBytes':100}
        return [record('produce',1,'newBuild',163000,True),record('import',0,'',163000,True),record('commit_certificate',0,'advanceCanonical')]

    def segments(self,records):
        directory=self.root/'segments';directory.mkdir(mode=0o700,exist_ok=True)
        segments=[]
        for i in range(7):
            path=directory/f'v{i}.jsonl'
            selected=[r for r in records if r['Validator']==self.m['validators'][i]['address']]
            raw=b''.join((json.dumps({'msg':'shared validator operation phases','operation':r['Operation'],'record':r})+'\n').encode() for r in selected)
            path.write_bytes(raw);path.chmod(0o600);segments.append({'validator_index':i,'path':str(path),'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)})
        return segments

    def test_phase_shape_requires_same_hash_durable_import_and_canonical_advance(self):
        records=self.phase_records()
        result=finite.phase_evidence(self.segments(records),self.m)
        self.assertEqual(result['canonical_163000_blocks'][0]['transactions'],163000)
        self.assertFalse(result['tps_measured'])
        early=dict(records[0],Outcome='canceled',Hash='0x'+'00'*32,Parent='0x'+'00'*32,Transactions=0,Durable=False)
        retained=finite.phase_evidence(self.segments(records+[early]),self.m)
        self.assertEqual(len(retained['records']),4)
        malformed=dict(early,Hash='not-a-hash')
        with self.assertRaises(ValueError):finite.phase_evidence(self.segments(records+[malformed]),self.m)
        with self.assertRaises(ValueError):finite.phase_evidence(self.segments(records),self.m,3)
        for change in [lambda x:x[0].update(Durable=False),lambda x:x[1].update(Hash='0x'+'99'*32),lambda x:x[2].update(Path='alreadyCanonical'),lambda x:x[2].update(Outcome='error'),lambda x:x[1].update(TotalNs=11),lambda x:x[1].update(Transactions=162999),lambda x:x[1].update(Validator='0x'+'99'*20)]:
            bad=copy.deepcopy(records);change(bad)
            with self.assertRaises(ValueError):finite.phase_evidence(self.segments(bad),self.m)

    def test_phase_capture_rejects_rotation_or_prefix_change(self):
        for validator in self.m['validators']:
            directory=Path(validator['data_dir'])/'log';directory.mkdir(mode=0o700)
            path=directory/'n42.log';path.write_text('{"historical":true}\n');path.chmod(0o600)
        baseline=finite.phase_log_baselines(self.m)
        for validator in self.m['validators']:
            with (Path(validator['data_dir'])/'log/n42.log').open('a') as out:out.write('{"new":true}\n')
        output=self.root/'capture';output.mkdir(mode=0o700)
        segments=finite.capture_phase_segments(self.m,baseline,output)
        self.assertEqual(Path(segments[0]['path']).read_text(),'{"new":true}\n')
        path=Path(self.m['validators'][0]['data_dir'])/'log/n42.log'
        path.write_text('{"replaced":true}\n')
        with self.assertRaises(ValueError):finite.capture_phase_segments(self.m,baseline,self.root/'capture-bad')

    def test_drain_timeout_records_actual_samples_and_never_shortens_to_success(self):
        h=self.h;p=h.plan
        candidate={'owner_initial':h.owner,'schedule':{'newly_admitted':500}}
        status=h.canonical(2,'44',600);owner=dict(h.owner,NewlyAdmitted=500)
        ticks=[0.]
        def clock():ticks[0]+=1;return ticks[0]
        records={}
        with patch('fleet.status',return_value=status),patch('queue_fixture.owner_status',return_value=owner),patch('time.monotonic',side_effect=clock),patch('time.sleep'):
            success=finite.finite_drain(self.m,p,candidate,600,lambda:None,lambda *a:None,100,status,lambda n,v:records.update({n:v}))
            self.assertTrue(success['passed'])
        ticks[0]=0;owner['PendingTransactions']=1;records.clear()
        with patch('fleet.status',return_value=status),patch('queue_fixture.owner_status',return_value=owner),patch('time.monotonic',side_effect=clock),patch('time.sleep'):
            with self.assertRaises(TimeoutError):finite.finite_drain(self.m,p,candidate,600,lambda:None,lambda *a:None,100,status,lambda n,v:records.update({n:v}))
        self.assertFalse(records['finite-drain.json']['passed']);self.assertTrue(records['finite-drain.json']['samples'])

    def run_fixture(self,fault=None):
        h=self.h;processes=[];candidate=h.candidate()
        candidate['schedule']={'schedule_sha256':'0x'+'55'*32,'newly_admitted':500}
        class Process:
            def __init__(process,args,**kwargs):
                process.args,process.pid,process.returncode,process.env=args,90000000+len(processes),None,kwargs['env'];processes.append(process)
                if len(args)>1 and args[1]=='bounded-phase-preload':
                    if len(processes)!=2:raise AssertionError('validator started before preloader')
                    path=h.root/'strict-attempts'/args[args.index('--attempt')+1];path.mkdir(mode=0o700);h.write(path/'result-candidate.json',candidate);process.returncode=1 if fault=='command' else 0
                elif len(args)>1 and args[1]=='recover':
                    path=h.root/'strict-attempts'/args[args.index('--attempt')+1];path.mkdir(mode=0o700);h.write(path/'recovery.json',{'account_vector_sha256':'0x'+'88'*32});process.returncode=0
            def poll(process):return process.returncode
            def wait(process,timeout=None):
                if process.returncode is None:raise subprocess.TimeoutExpired(process.args,timeout)
                return process.returncode
        def killpg(pid,sig):
            process=next(p for p in processes if p.pid==pid)
            process.returncode=-9 if sig==signal.SIGKILL else 1 if fault=='nonzero' and process.args[0]=='owner' else 0
        private,mirror=h.root/'mock-claim',h.root/'mock-mirror'
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
        plan,proof,historical=(h.root/n for n in ('plan.json','proof.json','historical.json'))
        for path in (plan,proof,historical):h.write(path,{'synthetic':True})
        h.plan['SourceProofSHA256']='0x'+hashlib.sha256(proof.read_bytes()).hexdigest()
        old_exists,old_lexists=Path.exists,os.path.lexists
        def exists(path):return path.suffix=='.sock' or old_exists(path)
        def lexists(path):return (fault=='socket' and len(processes)==18 and str(path).endswith('validator.sock')) or old_lexists(path)
        ticks=[0.]
        def clock():ticks[0]+=.1;return ticks[0]
        stable={'owner':h.owner,'canonical_roots':{'roots':{'root':'0x'+'55'*32,'applied_root':'0x'+'55'*32}},'canonical_nonce_sum':600}
        status=h.canonical(2,'44',600)
        def drained(*args):
            if fault=='drain':raise TimeoutError('actual bounded drain timeout')
            return {'passed':True,'reference':status,'owner':h.owner,'canonical_count':600}
        with contextlib.ExitStack() as stack:
            mocks=[('finite_fleet.bounded_plan_inputs',lambda *a:(h.doc,{'owner_final':{'Epoch':[9]+[0]*15}})),('recovery_fleet.baseline',lambda *a:({},'00'*32)),('fleet.quiet_checks',lambda *a,**k:None),('fleet.claim',claimed),('fleet.check_owned_claim',lambda *a,**k:None),('fleet.binary',lambda path:{'path':str(path),'sha256':'mock'}),('strict_fleet.validate',lambda *a:h.m),('subprocess.Popen',Process),('os.killpg',killpg),('os.sched_getaffinity',lambda pid:{0,1}),('time.monotonic',clock),('time.sleep',lambda _:None),('finite_fleet._live_group',lambda pid:[pid] if next(p for p in processes if p.pid==pid).poll() is None else []),('shutil.disk_usage',lambda *a:type('Disk',(),{'free':1<<40})()),('pathlib.Path.exists',exists),('os.path.lexists',lexists),('fleet.status',lambda *a:status),('queue_fixture.owner_status',lambda *a:candidate['owner_final']),('finite_fleet.preload_evidence',lambda *a:600),('finite_fleet.finite_journal_evidence',lambda *a:{'new':500,'skip':0}),('finite_fleet.finite_schedule_evidence',lambda *a:None),('finite_fleet.finite_drain',drained),('finite_fleet.phase_log_baselines',lambda *a:[]),('finite_fleet.capture_phase_segments',lambda *a:[]),('finite_fleet.phase_evidence',lambda *a:{'canonical_163000_blocks':[{}]}),('finite_fleet.stable_owner_snapshot',lambda *a:stable),('finite_fleet.bounded_reopened_evidence',lambda *a:600)]
            for target,value in mocks:stack.enter_context(patch(target,value))
            bins=[{'path':name,'sha256':'mock'} for name in ('node','owner','replay')]
            if fault:
                with self.assertRaises((ValueError,TimeoutError)):finite.run(h.m,*bins,'finite-q1',plan,h.sha,h.plan_id,proof,historical,h.h.failed)
            else:
                result=finite.run(h.m,*bins,'finite-q1',plan,h.sha,h.plan_id,proof,historical,h.h.failed)
                self.assertEqual(result['canonical_nonce_sum'],600);self.assertFalse(result['tps_measured']);self.assertTrue(result['claim_cleanup']['all_owned_claims_absent'])
        self.assertTrue(all(p.poll() is not None for p in processes))
        self.assertEqual((h.root/'attempts/finite-q1/finite-fleet-result.json').exists(),not bool(fault))
        if not fault:self.assertEqual(len(processes),18);self.assertEqual([p.env['GOMAXPROCS'] for p in processes],['36','4']+['2']*16)
        if fault=='command':self.assertEqual(len(processes),2)

    def test_clean_preload_before_producers_and_physical_reopen(self):self.run_fixture()
    def test_failed_preload_never_starts_producers_or_replays(self):self.run_fixture('command')
    def test_failed_drain_keeps_phase_evidence_and_no_reopen(self):self.run_fixture('drain')
    def test_claim_tail_has_no_promotion(self):self.run_fixture('tail')
    def test_claim_residue_has_no_promotion(self):self.run_fixture('claim-residue')
    def test_nonzero_runtime_has_no_promotion(self):self.run_fixture('nonzero')
    def test_reopen_socket_residue_has_no_promotion(self):self.run_fixture('socket')


if __name__=='__main__':unittest.main()
