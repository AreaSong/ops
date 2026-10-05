#!/usr/bin/env python3
"""限定 set 监督器。真实执行/观察后端未装配，命令行保持拒绝。"""
from __future__ import annotations

import gzip
import os
from pathlib import Path
import sys
import time

if __name__ == '__main__':
    sys.path.insert(0, str(Path(__file__).parent))

from sub2api_backup_archive import (BackupError, archive_tree, digest, encode, file_record,
    identity, source_record, open_exclusive, opened, private_dir, publish, read_regular, tar_bytes, write_new)
from sub2api_backup_contract import (ARTIFACTS, BINDINGS, CONTROL, JOBS, Journal, LockSet,
    binding, exact, load_resources, parse_request, require_hex, strict_json)

GUARANTEES = ('immutableInputs', 'closedDependencies', 'exclusiveCluster', 'defaultDatabasesEmpty',
    'rolesDependenciesMapped', 'noExternalTablespaces', 'noForeignServersOrSubscriptions',
    'templatesUnmodified', 'ddlExcluded', 'bootstrapCompatible', 'restoreVersionEvidence',
    'exclusiveRedisInstance', 'allBGSaveInitiatorsControlled', 'redisTopologyMapped',
    'stableMounts', 'configurationStable', 'acceptableConsistency')


def prove(backend, r, m, checksum):
    if backend is None:
        raise BackupError('trusted_backend_required')
    proof = backend.prove(r, m)
    exact(proof, {'requestDigest', 'resourceDigest', 'guarantees', 'proof'})
    if proof['requestDigest'] != checksum or proof['resourceDigest'] != r['resourceDigest'] or proof['guarantees'] != list(GUARANTEES):
        raise BackupError('scope_unproven')
    require_hex(proof['proof'])
    # dumpall 将创建非内置角色，现有恢复器以 postgres 初始化；不能忽略冲突。
    if 'postgres' in m['postgres']['roles']:
        raise BackupError('bootstrap_role_conflict')
    catalog = backend.postgres_catalog(r)
    exact(catalog, {'databases', 'roles', 'catalogDigest', 'systemIdentifier', 'container', 'version'})
    expected = {key: m['postgres'][key] for key in ('databases', 'roles', 'catalogDigest', 'systemIdentifier', 'version')}
    expected['container'] = m['containers']['postgres']
    if catalog != expected:
        raise BackupError('postgres_catalog_changed')
    return proof['proof']


def docker_exec(m, role, *args):
    return ['docker', '--host', m['identity']['endpoint'], 'exec', m['containers'][role]['id'], *args]


def collect_postgres(backend, r, m, destination):
    pg = m['postgres']
    command = docker_exec(m, 'postgres', 'env', '-i', 'PATH=' + pg['toolPath'],
                          'pg_dumpall', '-U', pg['user'], '-l', pg['maintenanceDatabase'], '--no-password')
    with open_exclusive(destination) as output:
        with gzip.GzipFile(filename='', fileobj=output, mode='wb', mtime=0) as compressed:
            completion = backend.dump_postgres(r, command, compressed)
    exact(completion, {'callId', 'container', 'systemIdentifier', 'execId', 'exitCode', 'state', 'proof'})
    require_hex(completion['execId'])
    require_hex(completion['proof'])
    if completion['callId'] != r['callId'] or completion['container'] != m['containers']['postgres'] or completion['systemIdentifier'] != pg['systemIdentifier'] or type(completion['exitCode']) is not int or completion['exitCode'] != 0 or completion['state'] != 'completed':
        raise BackupError('postgres_exec_unproven')
    with gzip.open(destination, 'rb') as stream:
        if not stream.read(1):
            raise BackupError('postgres_empty')
        while stream.read(1024*1024):
            pass
    backend.assert_postgres_unchanged(r)


def source_bytes(m, key, *, limit=8*1024*1024):
    source = m['paths'][key]
    modes = (0o600, 0o644) if key in ('controlled', 'runtime') else (0o600,)
    return read_regular(source['path'], modes=modes, uid=source['uid'], gid=source['gid'], limit=limit)


def collect_redis(backend, r, m, destination):
    # INFO 和接受/终结的归因由可信后端保存；不从 LASTSAVE 秒数推导完成。
    commands = [docker_exec(m, 'redis', 'redis-cli', 'INFO', section) for section in ('server', 'persistence')]
    baseline = backend.redis_baseline(r, commands)
    exact(baseline, {'runId', 'container', 'inProgress', 'rdbIdentity'})
    if baseline['runId'] != m['redis']['runId'] or baseline['container'] != m['containers']['redis'] or baseline['inProgress'] is not False:
        raise BackupError('redis_not_idle')
    before = source_record(m['paths']['rdb'])
    if baseline['rdbIdentity'] != before:
        raise BackupError('redis_baseline_changed')
    proof = backend.redis_save(r, docker_exec(m, 'redis', 'redis-cli', 'BGSAVE'))
    exact(proof, {'callId', 'runId', 'container', 'execId', 'state', 'accepted', 'observedRunning', 'copySafe', 'rdbIdentity', 'proof'})
    require_hex(proof['proof'])
    require_hex(proof['execId'])
    if proof['callId'] != r['callId'] or proof['runId'] != baseline['runId'] or proof['container'] != baseline['container'] or proof['state'] != 'succeeded' or any(proof[k] is not True for k in ('accepted', 'observedRunning', 'copySafe')):
        raise BackupError('redis_termination_unproven')
    current = source_record(m['paths']['rdb'])
    if current == before or proof['rdbIdentity'] != current:
        raise BackupError('redis_snapshot_unattributed')
    rdb = source_bytes(m, 'rdb', limit=1024*1024*1024)
    acl = source_bytes(m, 'acl')
    tar_bytes(destination, {'metadata.txt': b'format=redis-rdb-snapshot\naclfile_included=yes\n',
                           'redis_data/dump.rdb': rdb, 'redis_data/users.acl': acl})
    if current != source_record(m['paths']['rdb']) or source_bytes(m, 'acl') != acl:
        raise BackupError('redis_copy_changed')
    backend.assert_redis_copy_stable(r, proof)


def artifact(job, path):
    role, name, fmt = ARTIFACTS[job]
    if path.name != name:
        raise BackupError('artifact_path')
    return {'role': role, 'path': name, 'format': fmt, **file_record(path)}


def subreceipt(r, checksum, job, item, proof):
    return {'protocolVersion': 1, 'service': 'sub2api', 'job': job, **binding(r),
            'requestDigest': checksum, 'state': 'completed', 'artifact': item, 'proof': proof}


def validate_subreceipt(raw, r, checksum, job, item):
    v = strict_json(raw)
    exact(v, {'protocolVersion', 'service', 'job', *BINDINGS, 'requestDigest', 'state', 'artifact', 'proof'})
    require_hex(v['proof'])
    if v != subreceipt(r, checksum, job, item, v['proof']):
        raise BackupError('subreceipt_mismatch')
    return digest(raw)


def collect(job, backend, r, m, destination):
    if job == 'postgres':
        collect_postgres(backend, r, m, destination)
    elif job == 'redis':
        collect_redis(backend, r, m, destination)
    elif job == 'volumes':
        source = m['paths']['app']
        archive_tree(source['path'], destination, backend, source)
    else:
        from restore_point_metadata import capture_scoped_configs
        capture_scoped_configs(m, destination)


def prepare_directory(r, m):
    # 根由另批初始化；本单元只创建本次 task/call 私有目录，不复用旧调用。
    root = Path(m['roots']['backup'])
    with opened(root, directory=True, modes=(0o700,)):
        pass
    task = root / r['taskId']
    if task.exists():
        with opened(task, directory=True, modes=(0o700,)):
            pass
    else:
        private_dir(task)
    destination = private_dir(task / r['callId'])
    private_dir(destination / '.incomplete')
    return destination


def set_receipt(r, checksum, state, items, children, proof, started, ended, category):
    return {'protocolVersion': 1, 'service': 'sub2api', 'job': 'sub2api-set', **binding(r),
            'requestDigest': checksum, 'state': state, 'startedAt': started, 'endedAt': ended,
            'artifacts': items, 'subreceipts': children, 'proof': proof,
            'cleanup': 'retained', 'category': category}


def validate_result(value, r, checksum, destination):
    exact(value, {'protocolVersion', 'service', 'job', *BINDINGS, 'requestDigest', 'state',
                  'startedAt', 'endedAt', 'artifacts', 'subreceipts', 'proof', 'cleanup', 'category'})
    require_hex(value['proof'])
    if value != set_receipt(r, checksum, 'completed', value['artifacts'], value['subreceipts'], value['proof'], value['startedAt'], value['endedAt'], 'none'):
        raise BackupError('set_receipt_mismatch')
    if type(value['startedAt']) is not int or type(value['endedAt']) is not int or not 0 < value['startedAt'] <= value['endedAt'] <= r['deadline']:
        raise BackupError('set_time')
    expected = [artifact(job, destination / ARTIFACTS[job][1]) for job in (*JOBS, 'set')]
    if value['artifacts'] != expected or len(value['subreceipts']) != 4:
        raise BackupError('set_artifacts')
    for job, item, child in zip(JOBS, expected, value['subreceipts']):
        path = destination / (job + '.receipt.json')
        checksum_child = validate_subreceipt(read_regular(path), r, checksum, job, item)
        if child != {'job': job, 'path': path.name, 'sha256': 'sha256:' + checksum_child}:
            raise BackupError('set_subreceipts')
    from restore_point_metadata import validate_scoped_runtime
    validate_scoped_runtime(strict_json(read_regular(destination / 'runtime.json')), expected,
                            expected_binding={**binding(r), 'requestDigest': checksum})


def publish_metrics(r, m, state, started, children):
    root = Path(m['roots']['metrics'])
    lines = []
    for job in (*JOBS, 'set'):
        success = state == 'completed' if job == 'set' else any(x['job'] == job and x['path'] == job + '.receipt.json' for x in children)
        labels = '{service="sub2api",job="' + job + '"}'
        for name, value in [('last_attempt_timestamp_seconds', started), ('last_duration_seconds', max(0, int(time.time())-started)),
                            ('last_result', int(success)), ('uncertain', int(state == 'uncertain'))]:
            lines.append('service_backup_job_' + name + labels + ' ' + str(value))
    if state == 'completed':
        lines.append('service_backup_set_last_success_timestamp_seconds{service="sub2api"} ' + str(int(time.time())))
    else:
        existing = root / 'sub2api-backup-set.prom'
        if existing.exists():
            for line in read_regular(existing).decode().splitlines():
                if line.startswith('service_backup_set_last_success_timestamp_seconds{service="sub2api"} ') and line.rsplit(' ', 1)[-1].isdigit():
                    lines.append(line)
    temporary = root / ('.sub2api-backup-set.' + r['callId'])
    write_new(temporary, ('\n'.join(lines)+'\n').encode())
    with opened(root, directory=True, modes=(0o700,)) as (fd, _):
        os.replace(temporary.name, 'sub2api-backup-set.prom', src_dir_fd=fd, dst_dir_fd=fd)
        os.fsync(fd)


def run(arguments, environment, *, backend=None, control_path=CONTROL):
    r, checksum, control = parse_request(arguments, environment, control_path=control_path)
    m = load_resources(r)
    if control['coordinationRoot'] != m['roots']['coordination'] or control['instanceDigest'] != digest(encode(m['identity'])):
        raise BackupError('coordination_mapping')
    if environment.get('HOME') != str(Path(m['roots']['temporary']) / 'home') or environment.get('TMPDIR') != str(Path(m['roots']['temporary']) / 'tmp'):
        raise BackupError('environment_directories')
    # 任何敏感读取之前，私有后端必须确认本次绑定和隔离；无CLI合成开关。
    proof = prove(backend, r, m, checksum)
    with LockSet(m['locks']):
        journal = Journal(control, synthetic=m['materialKind'] == 'synthetic')
        destination = prepare_directory(r, m)
        return execute_set(backend, r, m, checksum, proof, journal, destination)


def execute_set(backend, r, m, checksum, proof, journal, destination):
    items, children = [], []
    started = int(time.time())
    state, category = 'uncertain', 'set_incomplete'
    result_path = Path(r['operationDir']) / 'backup-result.json'
    journal.begin(r['callId'], checksum)
    try:
        for job in JOBS:
            if time.time() >= r['deadline']:
                raise BackupError('deadline')
            partial = destination / '.incomplete' / ARTIFACTS[job][1]
            collect(job, backend, r, m, partial)
            final = destination / partial.name
            publish(partial, final)
            item = artifact(job, final)
            receipt_path = destination / (job + '.receipt.json')
            raw = encode(subreceipt(r, checksum, job, item, proof))
            write_new(receipt_path, raw)
            child_digest = validate_subreceipt(read_regular(receipt_path), r, checksum, job, item)
            items.append(item)
            children.append({'job': job, 'path': receipt_path.name, 'sha256': 'sha256:' + child_digest})
        from restore_point_metadata import finalize_scoped_runtime
        runtime = finalize_scoped_runtime(r, m, checksum, destination, items, children, proof)
        write_new(destination / '.incomplete/runtime.json', encode(runtime))
        publish(destination / '.incomplete/runtime.json', destination / 'runtime.json')
        items.append(artifact('set', destination / 'runtime.json'))
        result = set_receipt(r, checksum, 'completed', items, children, proof, started, int(time.time()), 'none')
        validate_result(result, r, checksum, destination)
        # 先发布并复读；任何异常必须留下持久阻断，不能凭脚本返回码释放。
        terminal = journal.verify_terminal(r, checksum, backend)
        if time.time() >= r['deadline']:
            raise BackupError('late_terminal_proof')
        result['endedAt'] = int(time.time())
        write_new(result_path, encode(result))
        validate_result(strict_json(read_regular(result_path)), r, checksum, destination)
        if time.time() >= r['deadline']:
            raise BackupError('late_set_commit')
        journal.finish(r, checksum, terminal)
        publish_metrics(r, m, 'completed', started, children)
        state, category = 'completed', 'none'
    except Exception as error:
        category = 'set_contract_failed' if isinstance(error, BackupError) else 'backend_failure'
        journal.block(r['callId'], checksum)
        write_new(destination / '.incomplete/diagnostic.log', str(error).encode('utf-8', errors='replace') or b'failure')
        if job in JOBS and not (destination / (job + '.receipt.json')).exists():
            failed = {**subreceipt(r, checksum, job, None, proof), 'state': 'uncertain'}
            failed_path = destination / '.incomplete' / (job + '.receipt.json')
            write_new(failed_path, encode(failed))
            children.append({'job': job, 'path': str(failed_path.relative_to(destination)), 'sha256': 'sha256:' + digest(read_regular(failed_path))})
        if not result_path.exists():
            write_new(result_path, encode(set_receipt(r, checksum, state, items, children, proof, started, int(time.time()), category)))
        raise BackupError(category) from None
    finally:
        if state != 'completed':
            publish_metrics(r, m, state, started, children)
    return result


def main():
    run(sys.argv[1:], dict(os.environ))


if __name__ == '__main__':
    try:
        main()
    except (BackupError, OSError, ValueError, TypeError, KeyError):
        print('ERROR: sub2api_backup_rejected', file=sys.stderr)
        raise SystemExit(1)
