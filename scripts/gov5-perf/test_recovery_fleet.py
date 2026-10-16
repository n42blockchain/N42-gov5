import contextlib
import copy
import hashlib
import json
from pathlib import Path
import signal
import subprocess
import unittest
from unittest.mock import patch

import fleet
import recovery_fleet
import test_strict_fleet


class RecoveryFleetTest(unittest.TestCase):
    def setUp(self):
        helper = test_strict_fleet.StrictRuntimeValidationTest()
        helper.setUp()
        self.addCleanup(helper.doCleanups)
        self.root, self.source, self.m, self.write = helper.root, helper.source, helper.m, helper.write
        self.identity = self.m['identity']
        self.manifest_hash = '0x' + self.m['supply']['manifest_sha256']
        self.epoch = [1] + [0] * 15
        addresses = ['0x' + f'{i+1:040x}' for i in range(4)]
        genesis = fleet.load(self.root/'genesis.json')
        genesis['alloc'] = {a: {'balance': '0x1000000000'} for a in addresses}
        self.write(self.root/'genesis.json', genesis)
        self.b = {'kind':'strict-owner-queue-canonical-observation-not-capacity-promotion','passed':False,'error':'unknown ACK','full_source_integrity_verified':True,'identity':self.identity,'manifest_sha256':self.manifest_hash,'runtime_manifest':str(self.root/'run.json'),'final':[self.status(1,4,'33',[2]+[0]*15) for _ in range(7)],'owner_final':self.owner(1,'33',[2]+[0]*15)}
        self.failed = self.root/'strict-attempts/failed/result.json'
        self.failed.parent.mkdir(parents=True, mode=0o700)
        self.write(self.failed,self.b)
        self.digest = hashlib.sha256(self.failed.read_bytes()).hexdigest()
        accounts = [{'address':a,'exists':True,'nonce':2,'balance':'1000000000','code_hash':recovery_fleet.EMPTY_CODE_HASH} for a in addresses]
        self.value = {'kind':'strict-owner-queue-recovery-no-dispatch-not-tps','passed':True,'identity':self.identity,'manifest_sha256':self.manifest_hash,'runtime_manifest':str(self.root/'run.json'),'baseline_result':str(self.failed),'baseline_sha256':'0x'+self.digest,'dispatched_transactions':0,'initial':[self.status(2,8,'44',self.epoch) for _ in range(7)],'final':[self.status(2,8,'44',self.epoch) for _ in range(7)],'owner_initial':self.owner(2,'44',self.epoch),'owner_final':self.owner(2,'44',self.epoch),'accounts':accounts,'canonical_nonce_sum':8,'account_vector_sha256':'0x'+hashlib.sha256(json.dumps(accounts,separators=(',',':')).encode()).hexdigest(),'readonly_snapshot_retries':0,'limitation':'unknown ACKs are not replay-authorized'}

    def owner(self,n,h,epoch):
        return {'Identity':self.identity,'Epoch':epoch,'ManifestHash':self.manifest_hash,'CanonicalHash':'0x'+h*32,'CanonicalNumber':n,'CanonicalGeneration':1,'Reconciling':False,'PendingTransactions':0,'RetainedEncodedBytes':0,'ReservedTransactions':0,'ReservedEncodedBytes':0,'CryptoVerified':0,'NewlyAdmitted':0}

    def status(self,n,count,h,epoch):
        return {'identity':self.identity,'number':n,'block_hash':'0x'+h*32,'committed_executed_transactions':count,'producer_running':False,'queued_transactions':0,'owner_queue':{'epoch':epoch,'manifest_hash':self.manifest_hash,'max_transactions':163000,'max_encoded_bytes':33554432}}

    def check(self,v):
        return recovery_fleet.evidence(v,self.m,self.b,self.failed,self.digest)

    def test_late_nonzero_commit_vector_is_independently_checked(self):
        self.assertEqual(self.check(self.value),8)
        for mutate in (lambda v:v.update(dispatched_transactions=1),lambda v:v.update(passed=False),lambda v:v.update(baseline_sha256='0x'+'99'*32),lambda v:v['owner_final'].update(NewlyAdmitted=1),lambda v:v['final'][0].update(producer_running=True),lambda v:v['accounts'][0].update(exists=False),lambda v:v['accounts'][0].update(nonce=3),lambda v:v['accounts'][0].update(balance='0'),lambda v:v['accounts'][0].update(code_hash='0x'+'00'*32),lambda v:v.update(canonical_nonce_sum=7),lambda v:v.update(account_vector_sha256='0x'+'99'*32)):
            value=copy.deepcopy(self.value);mutate(value)
            with self.assertRaises(ValueError):self.check(value)

    def test_failed_nonzero_same_runtime_baseline_required(self):
        self.assertEqual(recovery_fleet.baseline(self.failed,self.m),(self.b,self.digest))
        for key,value in (('passed',True),('full_source_integrity_verified',False),('manifest_sha256','0x'+'99'*32)):
            b=dict(self.b);b[key]=value;self.write(self.failed,b)
            with self.assertRaises(ValueError):recovery_fleet.baseline(self.failed,self.m)
        with self.assertRaises(ValueError):recovery_fleet.baseline(self.source/'result.json',self.m)

    def run_fixture(self,tail_failure=False,force_kill=False):
        processes=[];outer=self
        class Process:
            def __init__(self,args,**kwargs):
                self.args,self.returncode,self.signals=args,None,[];self.pid=90000000+len(processes);processes.append(self)
                if args[0]=='audit':
                    path=outer.root/'strict-attempts/recovery-q1';path.mkdir(mode=0o700);outer.write(path/'recovery.json',outer.value);self.returncode=0
            def poll(self):return self.returncode
            def send_signal(self,sig):
                self.signals.append(sig)
                if not (force_kill and self.args[0]=='owner'):self.returncode=0
            def wait(self,timeout=None):
                if self.returncode is None:raise subprocess.TimeoutExpired(self.args,timeout)
                return self.returncode
            def kill(self):self.signals.append(signal.SIGKILL);self.returncode=-9
        @contextlib.contextmanager
        def claimed():
            yield {'load1':0,'available_gib':100}
            if tail_failure:raise fleet.HardwareContention('tail ownership changed')
        exists=Path.exists
        def ready(p):return p.suffix=='.sock' or exists(p)
        with patch.object(fleet,'quiet_checks'),patch.object(fleet,'claim',claimed),patch.object(fleet,'check_owned_claim'),patch.object(recovery_fleet.shutil,'disk_usage',return_value=type('Disk',(),{'free':100<<30})()),patch.object(recovery_fleet.strict_fleet,'validate',return_value=self.m),patch.object(recovery_fleet.subprocess,'Popen',Process),patch.object(Path,'exists',ready):
            if force_kill:
                with self.assertRaisesRegex(ValueError,'cleanup did not exit cleanly'):recovery_fleet.run(self.m,self.failed,{'path':'node'},{'path':'owner'},{'path':'audit'},'recovery-q1')
            elif tail_failure:
                with self.assertRaises(fleet.HardwareContention):recovery_fleet.run(self.m,self.failed,{'path':'node'},{'path':'owner'},{'path':'audit'},'recovery-q1')
            else:recovery_fleet.run(self.m,self.failed,{'path':'node'},{'path':'owner'},{'path':'audit'},'recovery-q1')
        self.assertEqual(len(processes),9)
        self.assertTrue(all(p.returncode in (0,-9) for p in processes))
        self.assertEqual(sum(p.signals==[signal.SIGTERM] for p in processes),7 if force_kill else 8)
        self.assertFalse(any('init' in p.args or 'strict' in p.args for p in processes))
        for i in range(7):
            self.assertTrue(fleet.load(self.root/f'v{i}/node.json')['node']['miner'])
            self.assertFalse(fleet.load(self.root/f'attempts/recovery-q1/recovery-v{i}.json')['node']['miner'])
        return self.root/'attempts/recovery-q1'

    def test_run_never_initializes_dispatches_or_changes_configs(self):
        log=self.run_fixture();self.assertTrue((log/'recovery-fleet-result.json').exists())

    def test_claim_tail_failure_stops_owned_children_without_promoting(self):
        log=self.run_fixture(True);self.assertTrue((log/'recovery-fleet-observation.json').exists());self.assertFalse((log/'recovery-fleet-result.json').exists())

    def test_forced_cleanup_does_not_promote_success(self):
        log=self.run_fixture(force_kill=True)
        lifecycle=fleet.load(log/'process-lifecycle.json')
        self.assertFalse(lifecycle['cleanup_clean'])
        self.assertTrue(any(p['forced_kill'] for p in lifecycle['processes']))
        self.assertFalse((log/'recovery-fleet-result.json').exists())

    def test_shared_go_json_contract(self):
        value=fleet.load(Path(__file__).parent/'testdata/recovery-result.json')
        value.update(identity=self.identity,manifest_sha256=self.manifest_hash,runtime_manifest=str(self.root/'run.json'),baseline_result=str(self.failed),baseline_sha256='0x'+self.digest)
        for owner in (value['owner_initial'],value['owner_final']):
            owner.update(Identity=self.identity,ManifestHash=self.manifest_hash)
        for status in value['initial']+value['final']:
            status['identity']=self.identity
            status['owner_queue']['manifest_hash']=self.manifest_hash
        self.assertEqual(self.check(value),8)

    def test_existing_dangling_attempt_never_launches(self):
        (self.root/'attempts').mkdir(mode=0o700);(self.root/'attempts/recovery-q1').symlink_to(self.root/'missing')
        with patch.object(fleet,'quiet_checks') as quiet:
            with self.assertRaises(ValueError):recovery_fleet.run(self.m,self.failed,{},{},{},'recovery-q1')
            quiet.assert_not_called()
