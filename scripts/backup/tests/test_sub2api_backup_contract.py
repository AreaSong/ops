import copy
import fcntl
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import uuid
from unittest.mock import patch
from scripts.backup.tests.test_sub2api_backup import Fixture, BACKUP, contract, BackupError, encode


class ContractTests(unittest.TestCase):
    def setUp(self):
        t=tempfile.TemporaryDirectory();self.addCleanup(t.cleanup);self.f=Fixture(t.name)

    def parse(self):
        return contract.parse_request(self.f.args,self.f.environment,control_path=self.f.control)

    def test_unknown_duplicate_missing_and_cross_identity(self):
        for mutation in (lambda v:v.update(extra=True),lambda v:v.pop('leaseId'),lambda v:v.update(service='areaforge'),lambda v:v.update(taskId=str(uuid.uuid4())),lambda v:v.update(deadline=0),lambda v:v.update(protocolVersion=True)):
            with self.subTest(mutation=mutation):
                value=copy.deepcopy(self.f.r);mutation(value)
                with self.assertRaises(BackupError):contract.validate_request(value,self.f.args[1],self.f.args[5])
        for raw in (b'{"x":1,"x":2}',b'{"x":NaN}',b'{',b'{}{}'):
            with self.assertRaises(BackupError):contract.strict_json(raw)

    def test_environment_and_arguments_cannot_switch_mode(self):
        for variable in ('OPS_BACKUP_JOB_WRAPPED','BACKUP_ROOT','REDIS_DATA_DIR','SUB2API_MODE','OPS_RESTORE_ENV_READER','DOCKER_HOST','DOCKER_CONTEXT','PGPASSWORD','PYTHONPATH','BASH_ENV','ENV','LD_PRELOAD'):
            with self.subTest(variable=variable):
                with self.assertRaisesRegex(BackupError,'environment_override'):
                    contract.parse_request(self.f.args,{**self.f.environment,variable:'injected'},control_path=self.f.control)
        for args in (self.f.args+['--request','x'], self.f.args[:-2],self.f.args[::-1]):
            with self.assertRaises(BackupError):contract.parse_request(args,self.f.environment,control_path=self.f.control)

    def test_missing_corrupt_or_shared_control_never_enables_set(self):
        for raw in (b'{}',b'{',encode({'schemaVersion':1,'mode':'shared-only','coordinationRoot':'','instanceDigest':''})):
            self.f.control.write_bytes(raw)
            with self.assertRaises(BackupError):self.parse()
        self.f.control.unlink()
        with self.assertRaises((BackupError,OSError)):self.parse()

    def test_result_exists_and_request_symlink_parent_rejected(self):
        result=self.f.operation/'backup-result.json';result.write_bytes(b'{}')
        with self.assertRaisesRegex(BackupError,'result_exists'):self.parse()
        result.unlink()
        request=self.f.operation/'backup-request.json';request.rename(request.with_suffix('.old'))
        request.symlink_to(request.with_suffix('.old'))
        with self.assertRaises((BackupError,OSError)):self.parse()

    def test_shared_physical_locks_conflict_before_any_backup(self):
        for job in contract.JOBS[:3]:
            with open(self.f.m['locks'][job]['path'],'rb') as held:
                fcntl.flock(held,fcntl.LOCK_EX|fcntl.LOCK_NB)
                with self.assertRaises(BlockingIOError):self.f.run()
            self.assertFalse(self.f.backend.commands)
        lock=self.f.m['locks']['postgres'];lock['inode']+=1;self.f.save()
        with self.assertRaisesRegex(BackupError,'lock_replaced'):self.f.run()

    def test_persistent_corrupt_missing_or_unknown_never_reinitialized(self):
        control=contract.load_control(self.f.control);journal=contract.Journal(control,synthetic=True)
        journal.begin(self.f.call,self.f.checksum)
        with self.assertRaisesRegex(BackupError,'persistent_block'):contract.Journal(control,synthetic=True)
        self.assertTrue((journal.root/(self.f.call+'.start.json')).exists())
        (journal.root/'enabled.json').unlink()
        with self.assertRaises((BackupError,OSError)):contract.Journal(control,synthetic=True)
        self.assertFalse((journal.root/'enabled.json').exists())

    def test_wrapper_and_three_adapters_reject_inherited_override_without_helpers(self):
        repo=BACKUP.parents[1]
        commands=[[str(BACKUP/'run-backup-job.sh'),'sub2api-set',*self.f.args]]
        for path in ('services/areasong-ops/adapters/compose-service.sh','services/areasong-ops/adapters/sub2api.sh','scripts/deploy/update-control/adapters/sub2api.sh'):
            commands.append([str(repo/path),'update','backup',str(self.f.operation),'v1.0','',*self.f.args])
        for cmd in commands:
            # PATH 即使指向拒绝脚本也不应被调用；纯 shell 环境检查先失败。
            env={**self.f.environment,'OPS_BACKUP_JOB_WRAPPED':'1'}
            result=subprocess.run(cmd,env=env,capture_output=True,timeout=5)
            self.assertNotEqual(result.returncode,0)
            self.assertIn(b'environment_override',result.stderr)
            self.assertFalse((self.f.operation/'backup-result.json').exists())

    def test_shared_wrapped_marker_alone_is_not_internal_protocol(self):
        for job in ('postgres','redis','volumes'):
            result=subprocess.run([str(BACKUP/('backup-'+job+'.sh'))],env={**self.f.environment,'PATH':'/usr/bin:/bin','OPS_BACKUP_JOB_WRAPPED':'1'},capture_output=True,timeout=5)
            self.assertNotEqual(result.returncode,0)
            self.assertIn(b'internal_contract_required',result.stderr)

    def test_real_three_layer_scoped_route_has_no_legacy_or_cli_backend_fallback(self):
        import shutil
        repo=BACKUP.parents[1]
        bundle=self.f.root/'bundle'
        names=['services/areasong-ops/adapters/compose-service.sh','services/areasong-ops/adapters/sub2api.sh','scripts/deploy/update-control/adapters/sub2api.sh']
        names += ['scripts/backup/'+n for n in ('run-backup-job.sh','sub2api_backup.py','sub2api_backup_contract.py','sub2api_backup_archive.py')]
        for name in names:
            target=bundle/name;target.parent.mkdir(parents=True,exist_ok=True,mode=0o700);shutil.copy2(repo/name,target)
        tools=bundle/'tools';tools.mkdir(mode=0o700)
        log=self.f.root/'route-log'
        python=tools/'python3'
        # macOS Python 会注入 Cocoa 编码变量；受控测试工具模拟 Linux 清洁启动，不放宽产品白名单。
        python.write_text('#!'+sys.executable+' -I\nimport os,sys,runpy\nfrom pathlib import Path\nargs=sys.argv[1:]\nwith Path('+repr(str(log))+').open("a") as out:out.write(" ".join(args)+"\\n")\nwhile args and args[0] in ("-I","-B"):args.pop(0)\nos.environ.pop("__CF_USER_TEXT_ENCODING",None)\nsys.argv=args\nrunpy.run_path(args[0],run_name="__main__")\n');python.chmod(0o700)
        result=subprocess.run([str(bundle/names[0]),'update','backup',str(self.f.operation),'v1.0','',*self.f.args],env=self.f.environment,capture_output=True,timeout=10)
        self.assertNotEqual(result.returncode,0);self.assertIn(b'sub2api_backup_rejected',result.stderr)
        entries=log.read_text().splitlines()
        self.assertEqual(len(entries),5)
        self.assertTrue(all('sub2api_backup_contract.py' in line for line in entries[:4]))
        self.assertIn('sub2api_backup.py',entries[4])
        self.assertFalse(self.f.destination.exists())

    def test_hostile_path_tools_never_execute_before_rejection(self):
        marker=self.f.root/'path-injected';tools=self.f.root/'malicious';tools.mkdir()
        for name in ('dirname','python3'):
            path=tools/name;path.write_text('#!/bin/sh\ntouch '+str(marker)+'\nexit 0\n');path.chmod(0o700)
        result=subprocess.run([str(BACKUP/'run-backup-job.sh'),'sub2api-set',*self.f.args],env={**self.f.environment,'PATH':str(tools)},capture_output=True,timeout=5)
        self.assertNotEqual(result.returncode,0);self.assertFalse(marker.exists())


if __name__=='__main__':unittest.main()
