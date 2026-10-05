import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from scripts.backup.tests.test_sub2api_backup import Fixture, BackupError, read_regular
import sub2api_backup_archive as archive


class ArchiveTests(unittest.TestCase):
    def setUp(self):
        t=tempfile.TemporaryDirectory();self.addCleanup(t.cleanup);self.f=Fixture(t.name)

    def test_env_acl_required_private_and_no_source_permission_repair(self):
        for key in ('env','acl'):
            p=Path(self.f.m['paths'][key]['path']);p.chmod(0o644)
            with self.assertRaises(BackupError):read_regular(p)
            self.assertEqual(p.stat().st_mode&0o777,0o644)
            p.chmod(0o600)
        with self.assertRaises(BackupError):read_regular(self.f.source/'env',uid=os.geteuid()+1)
        with self.assertRaises(BackupError):read_regular(self.f.source/'env',gid=99999)

    def test_symlink_hardlink_and_fifo_rejected(self):
        for kind in ('symlink','hardlink','fifo'):
            with self.subTest(kind=kind):
                target=self.f.source/'app'/kind
                if kind=='symlink':target.symlink_to(self.f.source/'env')
                elif kind=='hardlink':os.link(self.f.source/'env',target)
                else:os.mkfifo(target,0o600)
                out=self.f.root/(kind+'.tar.gz')
                with self.assertRaises((BackupError,OSError)):
                    archive.archive_tree(self.f.source/'app',out,self.f.backend,self.f.m['paths']['app'])
                self.assertTrue(out.exists());target.unlink()

    def test_same_device_mount_proof_rejection_keeps_partial(self):
        self.f.backend.failure='mount'
        with self.assertRaises(BackupError):self.f.run()
        self.assertTrue((self.f.destination/'.incomplete/data.tar.gz').exists())
        self.assertFalse((self.f.destination/'runtime.json').exists())

    def test_path_replacement_after_open_rejected(self):
        path=self.f.source/'env';original=archive.identity;calls=0
        def changed(st):
            nonlocal calls
            calls+=1
            if calls==1:
                path.rename(path.with_name('old-env'));path.write_bytes(b'new');path.chmod(0o600)
            return original(st)
        with patch.object(archive,'identity',changed):
            with self.assertRaises(BackupError):read_regular(path)

    def test_publish_does_not_overwrite_and_preserves_failed_material(self):
        source=self.f.root/'partial';target=self.f.root/'target'
        archive.write_new(source,b'new');archive.write_new(target,b'old')
        with self.assertRaises(FileExistsError):archive.publish(source,target)
        self.assertEqual(source.read_bytes(),b'new');self.assertEqual(target.read_bytes(),b'old')

    def test_source_record_uses_approved_redis_uid_and_generation(self):
        from types import SimpleNamespace
        source=dict(self.f.m['paths']['rdb']);source['uid']=os.geteuid()+100
        target=Path(source['path']).stat().st_ino
        original=os.fstat
        def observed(fd):
            st=original(fd)
            if st.st_ino != target:return st
            value=SimpleNamespace(**{name:getattr(st,name) for name in dir(st) if name.startswith('st_')})
            value.st_uid=source['uid']
            return value
        with patch('os.fstat',observed):
            result=archive.source_record(source)
        self.assertEqual(result['inode'],target)
        self.assertIn('mtimeNs',result);self.assertIn('ctimeNs',result)

    def test_redis_directory_or_empty_member_cannot_pass_format_contract(self):
        import tarfile
        import io
        from restore_point_metadata import CONFIG_MEMBERS
        result=self.f.run()
        records=[{**item,'path':str(self.f.destination/item['path'])} for item in result['artifacts']]
        for kind in ('directory','empty'):
            path=self.f.destination/'redis.tar.gz'
            with tarfile.open(path,'w:gz') as bundle:
                for name,payload in {'metadata.txt':b'aclfile_included=yes\n','redis_data/dump.rdb':b'REDIS','redis_data/users.acl':b'user fixture'}.items():
                    info=tarfile.TarInfo(name);info.mode=0o600;info.size=len(payload)
                    if name=='redis_data/users.acl':
                        info.size=0
                        if kind=='directory':info.type=tarfile.DIRTYPE;info.mode=0o700
                    bundle.addfile(info,io.BytesIO(payload) if info.size else None)
            with self.assertRaises(BackupError):archive.validate_scoped_archives(records,CONFIG_MEMBERS['sub2api'].values())


if __name__=='__main__':unittest.main()
