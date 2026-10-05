"""仅临时合成数据；后端不启动 daemon/SQL/网络，禁止树有 stat/open/list 哨兵。"""
import copy
import contextlib
import gzip
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from unittest.mock import patch
import uuid

BACKUP = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BACKUP))
import sub2api_backup as supervisor
import sub2api_backup_contract as contract
from sub2api_backup_archive import BackupError, digest, encode, file_record, source_record, read_regular
import restore_point_metadata as metadata

SECRET = b'SYNTHETIC-ONLY-secret-value'


class Fixture:
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.task, self.call, self.plan, self.preparation, self.lease = [str(uuid.uuid4()) for _ in range(5)]
        self.operation = self.root / self.task
        self.source = self.root / 'source'
        roots = {key: str(self.root / key) for key in ('backup', 'temporary', 'log', 'metrics', 'coordination')}
        for path in [self.operation, self.source, *map(Path, roots.values())]:
            path.mkdir(mode=0o700)
        (self.source/'app').mkdir(mode=0o700)
        (self.source/'app/state').write_bytes(b'fixture app state')
        (self.source/'app/state').chmod(0o600)
        (self.source/'app/logs').mkdir(mode=0o700)
        (self.source/'app/logs/forbidden').write_bytes(b'must not read')
        self.forbidden = [self.root/name for name in ('account-vault', 'areaforge', 'jadeai', 'ops')]
        self.forbidden.append(self.source/'app/logs')
        for path in self.forbidden[:-1]:
            path.mkdir(mode=0o700)
        paths = {'app': {'path': str(self.source/'app'), 'uid': os.geteuid(), 'gid': (self.source/'app').stat().st_gid}}
        for key in ('controlled', 'runtime', 'env', 'rdb', 'acl'):
            p = self.source/key
            p.write_bytes(b'compose fixture' if key in ('controlled','runtime') else (b'old-rdb' if key=='rdb' else SECRET))
            p.chmod(0o644 if key in ('controlled','runtime') else 0o600)
            paths[key] = {'path': str(p), 'uid': os.geteuid(), 'gid': p.stat().st_gid}
        locks = {}
        for job in contract.JOBS[:3]:
            p=self.root/('ops-backup-'+job+'.lock');p.write_bytes(b'');p.chmod(0o600)
            st=p.stat();locks[job]={'path':str(p),'device':st.st_dev,'inode':st.st_ino}
        identity = {'objectId':'app-object','tenantId':'tenant','serverId':'server', 'daemonId':'daemon', 'endpoint':'unix://'+str(self.root/'no-daemon.sock'), 'project':'sub2api'}
        containers = {role:{'id':str(i)*64,'generation':'generation-1'} for i,role in enumerate(('app','postgres','redis'),1)}
        runtime = {'containers':{role:{'name':'sub2api-'+role,'configured_image':'fixture/'+role+':1', 'image_id':'sha256:'+str(i)*64,'container_id':containers[role]['id'],'version':'1.0','revision':'a'*40} for i,role in enumerate(containers,1)},'database':{'user':'sub2api','database':'sub2api','migrations':23}}
        self.m = {'schemaVersion':1,'materialKind':'synthetic','target':'v1.0','identity':identity,'containers':containers,
            'postgres':{'systemIdentifier':'123','user':'sub2api','maintenanceDatabase':'postgres','databases':['postgres','template0','template1','sub2api'],'roles':['sub2api'],'toolPath':'/usr/local/bin:/usr/bin:/bin','version':'18.1','catalogDigest':'a'*64},
            'redis':{'runId':'redis-run-1','version':'8.0'},'paths':paths,'roots':roots,'locks':locks,'resources':{},'runtime':runtime}
        expected={**{k:('read',v['path']) for k,v in paths.items()},**{k:('write',v) for k,v in roots.items()},**{k+'-lock':('coordinate',v['path']) for k,v in locks.items()},'operation':('write',str(self.operation))}
        for k,(action,selector) in expected.items():
            self.m['resources'][k]={'selector':selector,'objectId':'coordinator' if action=='coordinate' or k=='coordination' else identity['objectId'],'tenantId':'tenant','serverId':'server','actions':[action]}
        self.r={'protocolVersion':1,'mode':'service-exclusive-v1','service':'sub2api','job':'sub2api-set','callId':self.call,'taskId':self.task,'planId':self.plan,'preparationId':self.preparation,'leaseId':self.lease,'target':'v1.0','operationDir':str(self.operation),'scopeDigest':'b'*64,'implementationDigest':'c'*64,'parentReceiptDigest':'d'*64,'resourceDigest':'','resourceManifest':{},'deadline':int(time.time())+300}
        self.control=self.root/'bundle/scripts/backup/backup-control.json'
        self.control.parent.mkdir(parents=True,mode=0o700)
        instance=digest(encode(identity))
        self.control.write_bytes(encode({'schemaVersion':1,'mode':'coordinated','coordinationRoot':roots['coordination'],'instanceDigest':instance}));self.control.chmod(0o600)
        for name in ('enabled.json','ledger.jsonl'):
            p=Path(roots['coordination'])/name;p.write_bytes(encode({'schemaVersion':1,'instanceDigest':instance})+b'\n');p.chmod(0o600)
        self.environment={'PATH':str(self.root/'bundle/tools'),'HOME':str(Path(roots['temporary'])/'home'),'TMPDIR':str(Path(roots['temporary'])/'tmp'),'LANG':'C','LC_ALL':'C','TZ':'UTC'}
        self.save()
        self.backend = Backend(self)

    def save(self):
        raw=encode(self.m);p=self.operation/'backup-resources.json';p.write_bytes(raw);p.chmod(0o600)
        self.r['resourceDigest']=digest(raw);self.r['resourceManifest']={'path':str(p),'sha256':digest(raw)}
        raw=encode(self.r);p=self.operation/'backup-request.json';p.write_bytes(raw);p.chmod(0o600)
        self.checksum=digest(raw)
        self.args=['--request',str(p),'--request-sha256',self.checksum,'--result',str(self.operation/'backup-result.json')]

    def run(self):
        return supervisor.run(self.args,self.environment,backend=self.backend,control_path=self.control)

    @property
    def destination(self):
        return Path(self.m['roots']['backup'])/self.task/self.call


class Backend:
    def __init__(self, fixture):
        self.f=fixture;self.commands=[];self.members=[];self.failure='';self.redis_state='succeeded';self.fast=False;self.catalog_extra='';self.bad_proof=False

    def prove(self,r,m):
        return {'requestDigest':self.f.checksum,'resourceDigest':r['resourceDigest'],'guarantees':list(supervisor.GUARANTEES),'proof':'e'*64}

    def postgres_catalog(self,r):
        p=self.f.m['postgres'];result={k:copy.deepcopy(p[k]) for k in ('databases','roles','catalogDigest','systemIdentifier','version')};result['container']=self.f.m['containers']['postgres']
        if self.catalog_extra:result[self.catalog_extra].append('other-app')
        return result

    def command(self,args):
        m=self.f.m
        assert args[:3]==['docker','--host',m['identity']['endpoint']]
        assert args[3]=='exec' and args[4] in [v['id'] for v in m['containers'].values()]
        assert 'ps' not in args and 'sh' not in args and '-a' not in args
        self.commands.append(args)

    def dump_postgres(self,r,command,output):
        self.command(command)
        output.write(b'-- synthetic pg_dumpall\nCREATE ROLE sub2api PASSWORD \''+SECRET+b"';\n")
        if self.failure=='postgres':raise RuntimeError(SECRET.decode())
        return {'callId':r['callId'],'container':self.f.m['containers']['postgres'],'systemIdentifier':self.f.m['postgres']['systemIdentifier'],'execId':'a'*64,'exitCode':9 if self.failure=='postgres_exit' else 0,'state':'completed','proof':'b'*64}

    def assert_postgres_unchanged(self,r):
        if self.failure=='postgres_changed':raise RuntimeError('changed')

    def redis_baseline(self,r,commands):
        for cmd in commands:self.command(cmd)
        return {'runId':self.f.m['redis']['runId'],'container':self.f.m['containers']['redis'],'inProgress':self.failure=='redis_running','rdbIdentity':source_record(self.f.m['paths']['rdb'])}

    def redis_save(self,r,command):
        self.command(command)
        if self.failure=='redis_disconnect':raise ConnectionError(SECRET.decode())
        p=Path(self.f.m['paths']['rdb']['path']);p.write_bytes(b'REDIS0012-synthetic-new-snapshot')
        return {'callId':r['callId'],'runId':self.f.m['redis']['runId'] if self.failure!='redis_restart' else 'new-run', 'container':self.f.m['containers']['redis'],'execId':'f'*64,'state':self.redis_state,'accepted':True,'observedRunning':not self.fast,'copySafe':self.failure!='redis_writer','rdbIdentity':source_record(self.f.m['paths']['rdb']),'proof':'e'*64}

    def assert_redis_copy_stable(self,r,proof):
        if self.failure=='redis_copy':raise RuntimeError('changed')

    def check_member(self,path,observed):
        self.members.append(path)
        if self.failure=='mount' and path.endswith('/state'):raise BackupError('nested_mount')

    def terminal_proof(self,r):
        return {'callId':r['callId'] if not self.bad_proof else str(uuid.uuid4()),'requestDigest':self.f.checksum,'instanceDigest':digest(encode(self.f.m['identity'])),'state':'completed','proof':'f'*64}


class AccessSentinel:
    def __init__(self, test, forbidden):
        self.test, self.forbidden, self.fds, self.access = test, forbidden, {}, []
        self.stack = contextlib.ExitStack()

    def check(self, kind, value, kwargs):
        source = 'absolute'
        if isinstance(value, int):
            self.test.assertIn(value, self.fds, 'untracked directory fd')
            path, source = self.fds[value], 'directory-fd'
        else:
            path = Path(os.fsdecode(value))
            if not path.is_absolute():
                parent = kwargs.get('dir_fd')
                self.test.assertIn(parent, self.fds, 'untracked relative fd')
                path, source = self.fds[parent] / path, 'relative-fd'
        self.test.assertFalse(any(path.is_relative_to(x) for x in self.forbidden), (kind, str(path)))
        self.access.append((kind, str(path), source))
        return path

    def wrap(self, original, kind):
        def checked(path, *args, **kwargs):
            resolved = self.check(kind, path, kwargs)
            value = original(path, *args, **kwargs)
            if kind == 'open':
                self.fds[value] = resolved
            return value
        return checked

    def __enter__(self):
        for name in ('open', 'stat', 'lstat', 'listdir'):
            self.stack.enter_context(patch('os.'+name, self.wrap(getattr(os,name), name)))
        return self

    def __exit__(self, *args):
        return self.stack.__exit__(*args)


def save_test_evidence(name, value):
    directory = os.environ.get('B2A_TEST_EVIDENCE_DIR')
    if directory:
        root = Path(directory)
        root.mkdir(parents=True, mode=0o700, exist_ok=True)
        target = root / (name + '.json')
        target.write_bytes(encode(value));target.chmod(0o600)


class ScopedBackupTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.f=Fixture(self.tmp.name)
        self.addCleanup(self.preserve)

    def preserve(self):
        directory = os.environ.get('B2A_TEST_EVIDENCE_DIR')
        if directory:
            import zipfile
            target = Path(directory) / (self._testMethodName + '.zip')
            with zipfile.ZipFile(target, 'w', zipfile.ZIP_DEFLATED) as archive:
                for path in sorted(self.f.root.rglob('*')):
                    if path.is_file() and not path.is_symlink():
                        archive.write(path, str(path.relative_to(self.f.root)))
            target.chmod(0o600)

    def test_complete_set_fixed_commands_private_sources_and_no_forbidden_access(self):
        f=self.f
        sentinel = AccessSentinel(self, f.forbidden)
        with sentinel:
            result=f.run()
        self.assertTrue(sentinel.access)
        self.assertTrue(any(event[2] == 'relative-fd' for event in sentinel.access))
        self.assertTrue(any(event[2] == 'directory-fd' for event in sentinel.access))
        save_test_evidence('access-sentinel', {'events': sentinel.access, 'commands': f.backend.commands})
        self.assertEqual(result['state'],'completed')
        self.assertEqual(len(result['artifacts']),5);self.assertEqual(len(result['subreceipts']),4)
        self.assertEqual(f.backend.commands[0],supervisor.docker_exec(f.m,'postgres','env','-i','PATH=/usr/local/bin:/usr/bin:/bin','pg_dumpall','-U','sub2api','-l','postgres','--no-password'))
        with gzip.open(f.destination/'postgres.sql.gz','rb') as stream:self.assertIn(SECRET,stream.read())
        with tarfile.open(f.destination/'configs.tar.gz') as archive:
            self.assertEqual(set(archive.getnames()),set(metadata.CONFIG_MEMBERS['sub2api'].values()))
            self.assertEqual(archive.extractfile(metadata.CONFIG_MEMBERS['sub2api']['env']).read(),SECRET)
        with tarfile.open(f.destination/'redis.tar.gz') as archive:self.assertEqual(archive.extractfile('redis_data/users.acl').read(),SECRET)
        for p in f.destination.rglob('*'):
            self.assertEqual(p.stat().st_mode&0o777,0o700 if p.is_dir() else 0o600)
        self.assertEqual(Path(f.m['paths']['controlled']['path']).stat().st_mode&0o777,0o644)
        for p in [f.destination/'runtime.json',f.operation/'backup-result.json',Path(f.m['roots']['metrics'])/'sub2api-backup-set.prom']:
            self.assertNotIn(SECRET,p.read_bytes())
        self.assertFalse(list(Path(f.m['roots']['metrics']).glob('backup-job-*')))
        contract.Journal(contract.load_control(f.control),synthetic=True)

    def test_pg_extra_database_or_role_rejected_before_dump(self):
        for field in ('databases','roles'):
            with self.subTest(field=field):
                self.f.backend.catalog_extra=field
                with self.assertRaises(BackupError):self.f.run()
                self.assertFalse(self.f.backend.commands);self.assertFalse(self.f.destination.exists())

    def test_pg_bootstrap_conflict_rejected(self):
        self.f.m['postgres']['roles'].append('postgres');self.f.save()
        with self.assertRaisesRegex(BackupError,'bootstrap_role_conflict'):self.f.run()
        self.assertFalse(self.f.backend.commands)

    def test_missing_backend_never_reads_sources(self):
        with patch.object(supervisor,'source_bytes',side_effect=AssertionError('source touched')):
            with self.assertRaisesRegex(BackupError,'trusted_backend_required'):
                supervisor.run(self.f.args,self.f.environment,control_path=self.f.control)

    def test_failure_retains_material_and_restart_block(self):
        self.f.backend.failure='postgres'
        with self.assertRaises(BackupError):self.f.run()
        self.assertTrue((self.f.destination/'.incomplete/postgres.sql.gz').exists())
        result=json.loads((self.f.operation/'backup-result.json').read_bytes())
        self.assertEqual(result['state'],'uncertain');self.assertNotIn(SECRET,encode(result))
        with self.assertRaises(BackupError):contract.Journal(contract.load_control(self.f.control),synthetic=True)

    def test_redis_unattributed_completion_and_unknown_keep_block(self):
        self.f.backend.fast=True
        with self.assertRaises(BackupError):self.f.run()
        self.assertTrue((self.f.destination/'postgres.sql.gz').exists())
        self.assertFalse((self.f.destination/'runtime.json').exists())
        with self.assertRaises(BackupError):contract.Journal(contract.load_control(self.f.control),synthetic=True)

    def test_terminal_proof_wrong_call_never_completes(self):
        self.f.backend.bad_proof=True
        with self.assertRaises(BackupError):self.f.run()
        self.assertEqual(json.loads((self.f.operation/'backup-result.json').read_bytes())['state'],'uncertain')

    def test_old_or_cross_call_subreceipt_rejected(self):
        result=self.f.run();path=self.f.destination/'postgres.receipt.json';value=json.loads(path.read_bytes())
        value['callId']=str(uuid.uuid4());path.write_bytes(encode(value))
        with self.assertRaises(BackupError):supervisor.validate_result(result,self.f.r,self.f.checksum,self.f.destination)

    def test_runtime_v2_rejects_cycle_extra_fields_and_old_v1(self):
        result=self.f.run();runtime=json.loads((self.f.destination/'runtime.json').read_bytes())
        for mutate in (lambda v:v.update(schemaVersion=1),lambda v:v['backup'].update(finalReceiptDigest='a'*64),lambda v:v['containers']['app'].update(Env=[SECRET.decode()])):
            value=copy.deepcopy(runtime);mutate(value)
            with self.assertRaises((BackupError,TypeError)):metadata.validate_scoped_runtime(value,result['artifacts'])

    def test_restore_requires_independent_target_verifier(self):
        from argparse import Namespace
        with self.assertRaisesRegex(ValueError,'v2_restore_target_unproven'):
            metadata.require_scoped_restore_target(Namespace(),{})

    def test_result_publish_failure_keeps_durable_block(self):
        original=supervisor.write_new
        failed=False
        def write(path, raw):
            nonlocal failed
            if path == self.f.operation/'backup-result.json' and not failed:
                failed=True
                raise OSError('synthetic write failure')
            return original(path,raw)
        with patch.object(supervisor,'write_new',write):
            with self.assertRaises(BackupError):self.f.run()
        with self.assertRaises(BackupError):contract.Journal(contract.load_control(self.f.control),synthetic=True)

    def test_backend_alphanumeric_secret_stays_private(self):
        self.f.backend.dump_postgres=lambda *args: (_ for _ in ()).throw(BackupError('SyntheticSecretValue'))
        with self.assertRaises(BackupError):self.f.run()
        self.assertNotIn(b'SyntheticSecretValue',(self.f.operation/'backup-result.json').read_bytes())
        self.assertIn(b'SyntheticSecretValue',(self.f.destination/'.incomplete/diagnostic.log').read_bytes())

    def test_application_cannot_archive_operation_or_backup_tree(self):
        for path in (self.f.operation,Path(self.f.m['roots']['backup']),Path(self.f.m['roots']['coordination'])):
            self.f.m['paths']['app']['path']=str(path)
            self.f.m['resources']['app']['selector']=str(path);self.f.save()
            with self.assertRaises(BackupError):self.f.run()
            self.assertFalse(self.f.backend.commands)

    def test_sentinel_blocks_relative_open_and_fd_list(self):
        sentinel=AccessSentinel(self,self.f.forbidden)
        fd=os.open(self.f.root,os.O_RDONLY|os.O_DIRECTORY)
        forbidden_fd=os.open(self.f.forbidden[0],os.O_RDONLY|os.O_DIRECTORY)
        sentinel.fds={fd:self.f.root,forbidden_fd:self.f.forbidden[0]}
        try:
            with sentinel:
                with self.assertRaises(AssertionError):os.open('account-vault',os.O_RDONLY,dir_fd=fd)
                with self.assertRaises(AssertionError):os.stat('account-vault',dir_fd=fd)
                with self.assertRaises(AssertionError):os.listdir(forbidden_fd)
        finally:
            os.close(fd);os.close(forbidden_fd)

    def test_missing_registered_start_is_corruption_not_empty_ledger(self):
        control=contract.load_control(self.f.control);journal=contract.Journal(control,synthetic=True)
        journal.begin(self.f.call,self.f.checksum)
        (journal.root/(self.f.call+'.start.json')).unlink()
        with self.assertRaisesRegex(BackupError,'coordination_missing_record'):contract.Journal(control,synthetic=True)

    def test_v2_outer_v1_and_synthetic_target_staging_private_modes(self):
        from argparse import Namespace
        from scripts.backup.tests.recovery_fixture import make_contract
        f=self.f;receipt=f.run()
        artifacts=[{k:(str(f.destination/item['path']) if k=='path' else item[k]) for k in ('role','path','sizeBytes','sha256')} for item in receipt['artifacts']]
        app=f.m['runtime']['containers']['app']
        before={'currentVersion':app['version'],'currentImage':app['configured_image'],'currentImageId':app['image_id'],'runtimeIdentityHash':'sha256:'+'b'*64}
        outer=make_contract({'artifacts':artifacts,'before':before},backup_task_id=f.task)
        restore=f.root/outer['taskId'];restore.mkdir(mode=0o700)
        path=restore/'recovery-point.json';path.write_bytes(encode(outer));path.chmod(0o600)
        args=Namespace(contract=path,service='sub2api',target=outer['recoveryPointId'],backup_root=Path(f.m['roots']['backup']),expected_mode='isolated',operation_dir=restore)
        with self.assertRaisesRegex(ValueError,'v2_restore_target_unproven'):metadata.validated_point(args)
        proof={'taskId':outer['taskId'],'planId':outer['planId'],'sourceDigest':f.r['resourceDigest'],
            'mapping':{'daemonId':'restore-daemon','endpoint':'unix://'+str(f.root/'restore.sock'),'project':'restore-fixture','containers':{key:{'id':str(i)*64,'generation':'restore-generation'} for i,key in enumerate(('app','postgres','redis'),4)},'mounts':{key:str(f.root/('restore-'+key)) for key in ('app','postgres','redis')}},'configuration':'matched','bootstrap':'compatible','proof':'a'*64}
        args._target_verifier=lambda *ignored:proof
        mask=os.umask(0o022)
        try:staged=metadata.stage(args)
        finally:os.umask(mask)
        saved=restore/'selected-point.json';saved.write_bytes(encode(staged));saved.chmod(0o600)
        metadata.verify_staged(args)
        for root in ('selected-configs','selected-recovery-point'):
            for item in (restore/root).rglob('*'):
                self.assertEqual(item.stat().st_mode&0o777,0o700 if item.is_dir() else 0o600)
        for key in ('controlled','runtime'):
            config=restore/'selected-configs'/metadata.CONFIG_MEMBERS['sub2api'][key]
            original=config.read_bytes();config.write_bytes(b'changed compose')
            with self.assertRaisesRegex(ValueError,'staged_configuration_changed'):metadata.verify_staged(args)
            config.write_bytes(original)
        Path(staged['stagedArtifacts']['redis']).chmod(0o644)
        with self.assertRaises(BackupError):metadata.verify_staged(args)
        self.assertEqual(outer['schemaVersion'],1)
        self.assertEqual(staged['runtime']['schemaVersion'],2)

    def test_redis_failure_states_never_retry_or_drop_persistent_block(self):
        for failure in ('redis_running','redis_disconnect','redis_restart','redis_writer','redis_copy','unknown','failed'):
            with self.subTest(failure=failure):
                root=self.f.root/failure;root.mkdir(mode=0o700);fixture=Fixture(root)
                if failure in ('unknown','failed'):fixture.backend.redis_state=failure
                else:fixture.backend.failure=failure
                with self.assertRaises(BackupError):fixture.run()
                self.assertLessEqual(sum(cmd[-1]=='BGSAVE' for cmd in fixture.backend.commands),1)
                with self.assertRaises(BackupError):contract.Journal(contract.load_control(fixture.control),synthetic=True)
                self.assertFalse((fixture.destination/'runtime.json').exists())

    def test_postgres_nonzero_exit_cannot_publish_valid_gzip_as_success(self):
        self.f.backend.failure='postgres_exit'
        with self.assertRaises(BackupError):self.f.run()
        self.assertTrue((self.f.destination/'.incomplete/postgres.sql.gz').exists())
        self.assertTrue((self.f.destination/'.incomplete/postgres.receipt.json').exists())
        self.assertFalse((self.f.destination/'postgres.sql.gz').exists())

    def test_late_terminal_proof_keeps_uncertain_after_deadline(self):
        current=[time.time()]
        original=self.f.backend.terminal_proof
        def delayed(request):
            result=original(request);current[0]=request['deadline']+1;return result
        self.f.backend.terminal_proof=delayed
        with patch.object(supervisor.time,'time',side_effect=lambda:current[0]):
            with self.assertRaises(BackupError):self.f.run()
        with self.assertRaises(BackupError):contract.Journal(contract.load_control(self.f.control),synthetic=True)
        result=json.loads((self.f.operation/'backup-result.json').read_bytes())
        self.assertEqual(result['state'],'uncertain')


if __name__=='__main__':unittest.main()
