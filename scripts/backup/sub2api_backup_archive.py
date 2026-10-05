"""限定归档的 fd 锚定、私有发布；不修正来源权限，不清理失败材料。"""
from __future__ import annotations

import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import tarfile


class BackupError(ValueError):
    """仅稳定类别可离开私有执行边界。"""


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def encode(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode()


def absolute(path):
    p = Path(path)
    if not p.is_absolute() or str(p) != str(path) or '..' in p.parts or p == Path('/'):
        raise BackupError('unsafe_path')
    return p


@contextlib.contextmanager
def opened(path, *, directory=False, modes=None, uid=None, gid=None):
    """逐级 openat/no-follow；拒绝父链替换和可写非批准祖先。"""
    p = absolute(path)
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
    try:
        for index, part in enumerate(p.parts[1:]):
            last = index == len(p.parts) - 2
            flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
            if not last or directory:
                flags |= os.O_DIRECTORY
            new = os.open(part, flags, dir_fd=fd)
            os.close(fd)
            fd = new
            info = os.fstat(fd)
            if not last:
                sticky_root = info.st_uid == 0 and bool(info.st_mode & stat.S_ISVTX)
                if info.st_uid not in {0, os.geteuid()} or (info.st_mode & 0o022 and not sticky_root):
                    raise BackupError('unsafe_parent')
        check_info(info, directory=directory, modes=modes, uid=uid, gid=gid)
        yield fd, info
    finally:
        os.close(fd)


def check_info(info, *, directory=False, modes=None, uid=None, gid=None):
    valid_type = stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)
    if not valid_type or (not directory and info.st_nlink != 1):
        raise BackupError('unsafe_file_type')
    if info.st_uid != (os.geteuid() if uid is None else uid) or (gid is not None and info.st_gid != gid):
        raise BackupError('unsafe_owner')
    if info.st_mode & 0o022 or (modes is not None and stat.S_IMODE(info.st_mode) not in modes):
        raise BackupError('unsafe_mode')


def identity(st):
    return (st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns, st.st_ctime_ns, st.st_uid, st.st_gid, st.st_mode)


def read_regular(path, *, modes=(0o600,), uid=None, gid=None, limit=1024*1024):
    with opened(path, modes=modes, uid=uid, gid=gid) as (fd, before):
        if before.st_size <= 0 or before.st_size > limit:
            raise BackupError('invalid_size')
        with os.fdopen(os.dup(fd), 'rb') as stream:
            raw = stream.read(limit + 1)
        if len(raw) != before.st_size or identity(os.fstat(fd)) != identity(before):
            raise BackupError('source_changed')
        # 路径复读仅是补充；真实不可替换／mount 证明仍由可信后端提供。
        with opened(path, modes=modes, uid=uid, gid=gid) as (_, after):
            if identity(after) != identity(before):
                raise BackupError('source_replaced')
        return raw


def private_dir(path):
    p = absolute(path)
    with opened(p.parent, directory=True) as (fd, _):
        os.mkdir(p.name, mode=0o700, dir_fd=fd)
        os.fsync(fd)
    return p


def write_new(path, raw):
    p = absolute(path)
    with opened(p.parent, directory=True, modes=(0o700,)) as (parent, _):
        fd = os.open(p.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.fsync(parent)
    if read_regular(p, limit=max(len(raw), 1)) != raw:
        raise BackupError('publish_readback')


def publish(partial, destination):
    """目录被可信后端封闭；不覆盖目标，保留未成功提交的私有半成品。"""
    source, target = absolute(partial), absolute(destination)
    with opened(source) as (fd, _):
        os.fsync(fd)
    with opened(source.parent, directory=True, modes=(0o700,)) as (src, _):
        with opened(target.parent, directory=True, modes=(0o700,)) as (dst, _):
            # link 的 O_EXCL 语义避免覆盖，提交后仅移除自己的已提交临时名。
            os.link(source.name, target.name, src_dir_fd=src, dst_dir_fd=dst, follow_symlinks=False)
            os.fsync(dst)
            os.unlink(source.name, dir_fd=src)
            os.fsync(src)
    return file_record(target)


def file_record(path, *, uid=None, gid=None):
    with opened(path, modes=(0o600,), uid=uid, gid=gid) as (fd, before):
        h = hashlib.sha256()
        with os.fdopen(os.dup(fd), 'rb') as stream:
            for chunk in iter(lambda: stream.read(1024*1024), b''):
                h.update(chunk)
        if identity(os.fstat(fd)) != identity(before) or before.st_size <= 0:
            raise BackupError('artifact_changed')
        return {'sizeBytes': before.st_size, 'sha256': 'sha256:' + h.hexdigest()}


def tar_bytes(path, members):
    with open_exclusive(path) as output:
        with tarfile.open(fileobj=output, mode='w:gz') as bundle:
            for name, content in members.items():
                item = tarfile.TarInfo(name)
                item.mode, item.size = 0o600, len(content)
                bundle.addfile(item, io.BytesIO(content))


@contextlib.contextmanager
def open_exclusive(path):
    p = absolute(path)
    with opened(p.parent, directory=True, modes=(0o700,)) as (parent, _):
        fd = os.open(p.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
        with os.fdopen(fd, 'wb') as stream:
            yield stream
            stream.flush()
            os.fsync(stream.fileno())
        os.fsync(parent)


def archive_tree(source, destination, backend, ownership):
    with opened(source, directory=True, uid=ownership['uid'], gid=ownership['gid']) as (root, info):
        backend.check_member(str(source), identity(info))
        with open_exclusive(destination) as output:
            with tarfile.open(fileobj=output, mode='w:gz') as bundle:
                _walk(bundle, root, Path(source), 'data', info.st_dev, backend, ownership)


def _walk(bundle, fd, path, name, device, backend, ownership):
    before = os.fstat(fd)
    entry = tarfile.TarInfo(name)
    entry.type, entry.mode = tarfile.DIRTYPE, 0o700
    bundle.addfile(entry)
    for child in sorted(os.listdir(fd)):
        if name == 'data' and child == 'logs':
            continue  # 被排除子树不 stat/open/list。
        if child in {'.', '..'} or '/' in child or '\\' in child:
            raise BackupError('unsafe_member')
        _add_member(bundle, fd, path, child, name, device, backend, ownership)
    if identity(os.fstat(fd)) != identity(before):
        raise BackupError('directory_changed')


def _add_member(bundle, parent, path, child, name, device, backend, ownership):
    fd = os.open(child, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
    try:
        info = os.fstat(fd)
        directory = stat.S_ISDIR(info.st_mode)
        check_info(info, directory=directory, uid=ownership['uid'], gid=ownership['gid'])
        if info.st_dev != device:
            raise BackupError('nested_mount')
        backend.check_member(str(path / child), identity(info))
        if directory:
            _walk(bundle, fd, path / child, name + '/' + child, device, backend, ownership)
        else:
            entry = tarfile.TarInfo(name + '/' + child)
            entry.mode, entry.size = 0o600, info.st_size
            with os.fdopen(os.dup(fd), 'rb') as source:
                bundle.addfile(entry, source)
        if identity(os.fstat(fd)) != identity(info) or identity(os.stat(child, dir_fd=parent, follow_symlinks=False)) != identity(info):
            raise BackupError('member_changed')
    finally:
        os.close(fd)


def source_record(source):
    with opened(source['path'], modes=(0o600,), uid=source['uid'], gid=source['gid']) as (_, before):
        record = file_record(source['path'], uid=source['uid'], gid=source['gid'])
        with opened(source['path'], modes=(0o600,), uid=source['uid'], gid=source['gid']) as (_, after):
            if identity(before) != identity(after):
                raise BackupError('source_replaced')
        return {**record, 'device': before.st_dev, 'inode': before.st_ino,
                'mtimeNs': before.st_mtime_ns, 'ctimeNs': before.st_ctime_ns}


def private_parents(root, relative):
    current = Path(root)
    for part in Path(relative).parts:
        if part in {'.', '..'} or part.startswith('/'):
            raise BackupError('unsafe_member')
        current = current / part
        if not current.exists():
            private_dir(current)
        with opened(current, directory=True, modes=(0o700,)):
            pass
    return current


def copy_private(source, target):
    with opened(source, modes=(0o600,)) as (fd, before):
        with os.fdopen(os.dup(fd), 'rb') as source_stream, open_exclusive(target) as output:
            for chunk in iter(lambda: source_stream.read(1024*1024), b''):
                output.write(chunk)
        if identity(before) != identity(os.fstat(fd)):
            raise BackupError('copy_changed')
    return file_record(target)


def verify_private_tree(root):
    with opened(root, directory=True, modes=(0o700,)) as (fd, _):
        for name in os.listdir(fd):
            path = Path(root) / name
            st = os.stat(name, dir_fd=fd, follow_symlinks=False)
            if stat.S_ISDIR(st.st_mode):
                verify_private_tree(path)
            else:
                with opened(path, modes=(0o600,)):
                    pass


def validate_scoped_archives(artifacts, config_members):
    import gzip
    by_role = {a['role']: Path(a['path']) for a in artifacts}
    for role in ('postgres-sub2api', 'redis', 'volume-sub2api-data', 'configs'):
        path = by_role[role]
        with opened(path, modes=(0o600,)) as (fd, _):
            with os.fdopen(os.dup(fd), 'rb') as raw, gzip.GzipFile(fileobj=raw) as stream:
                if not stream.read(1):
                    raise BackupError('empty_archive')
                while stream.read(1024*1024):
                    pass
        if role == 'postgres-sub2api':
            continue
        with tarfile.open(path, 'r:gz') as bundle:
            names = set()
            size = 0
            for member in bundle:
                name = member.name
                if name in names or name.startswith('/') or '..' in Path(name).parts or '\\' in name or not (member.isfile() or member.isdir()):
                    raise BackupError('archive_member')
                if member.mode != (0o700 if member.isdir() else 0o600):
                    raise BackupError('archive_mode')
                names.add(name);size += member.size
                if len(names) > 200000 or size > 50*1024**3:
                    raise BackupError('archive_limit')
                if role == 'volume-sub2api-data' and (not (name == 'data' or name.startswith('data/')) or name == 'data/logs' or name.startswith('data/logs/')):
                    raise BackupError('archive_scope')
            expected = set(config_members) if role == 'configs' else {'metadata.txt', 'redis_data/dump.rdb', 'redis_data/users.acl'}
            if role != 'volume-sub2api-data' and names != expected:
                raise BackupError('archive_members')
            if role != 'volume-sub2api-data' and any(not bundle.getmember(name).isfile() or bundle.getmember(name).size <= 0 for name in names):
                raise BackupError('archive_member_type')
            if role == 'redis' and bundle.getmember('metadata.txt').size > 4096:
                raise BackupError('redis_metadata_size')
            if role == 'redis' and b'aclfile_included=yes' not in bundle.extractfile('metadata.txt').read().splitlines():
                raise BackupError('redis_acl_required')
