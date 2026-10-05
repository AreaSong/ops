"""Sub2API 限定请求、协调账本与回执的严格合同。CLI 不装配真实后端。"""
from __future__ import annotations

import fcntl
import json
import os
from pathlib import Path
import re
import sys
import time
import uuid

if __name__ == '__main__':
    sys.path.insert(0, str(Path(__file__).parent))

from sub2api_backup_archive import (BackupError, absolute, digest, encode, opened,
                                    read_regular, write_new)

JOBS = ('postgres', 'redis', 'volumes', 'config-capture')
ARTIFACTS = {
    'postgres': ('postgres-sub2api', 'postgres.sql.gz', 'pg-dumpall-sql-gzip-v1'),
    'redis': ('redis', 'redis.tar.gz', 'redis-rdb-acl-targz-v1'),
    'volumes': ('volume-sub2api-data', 'data.tar.gz', 'sub2api-data-targz-v1'),
    'config-capture': ('configs', 'configs.tar.gz', 'sub2api-configs-targz-v1'),
    'set': ('runtime-snapshot', 'runtime.json', 'sub2api-runtime-json-v2'),
}
BINDINGS = ('callId', 'taskId', 'planId', 'preparationId', 'leaseId', 'target',
            'operationDir', 'scopeDigest', 'implementationDigest', 'parentReceiptDigest', 'resourceDigest')
REQUEST_KEYS = {'protocolVersion', 'mode', 'service', 'job', 'resourceManifest', 'deadline', *BINDINGS}
HEX = re.compile(r'^[0-9a-f]{64}$')
UUID = re.compile(r'^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
SAFE = re.compile(r'^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$')
CONTROL = Path(__file__).resolve().with_name('backup-control.json')


def exact(value, keys):
    if not isinstance(value, dict) or set(value) != set(keys):
        raise BackupError('invalid_fields')
    return value


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise BackupError('duplicate_field')
            result[key] = value
        return result
    if not raw or len(raw) > 1024*1024:
        raise BackupError('invalid_size')
    try:
        return json.loads(raw, object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(BackupError('invalid_number')))
    except (UnicodeError, ValueError) as error:
        raise BackupError('invalid_json') from error


def load_json(path, *, modes=(0o600,)):
    return strict_json(read_regular(path, modes=modes))


def require_hex(value):
    if not isinstance(value, str) or not HEX.fullmatch(value):
        raise BackupError('invalid_digest')


def clean_environment(environment, control_path=CONTROL):
    # 运行选择不能来自环境；即使值看似无害也拒绝。
    allowed = {'PATH', 'LANG', 'LC_ALL', 'TZ', 'HOME', 'TMPDIR', 'SHLVL', '_', 'PWD'}
    if environment.get('PATH') != str(Path(control_path).parent.parent.parent / 'tools'):
        raise BackupError('environment_path')
    if set(environment) - allowed:
        raise BackupError('environment_override')
    if any(environment.get(key) != value for key, value in {'LANG': 'C', 'LC_ALL': 'C', 'TZ': 'UTC'}.items()):
        raise BackupError('environment_locale')


def load_control(path=CONTROL):
    value = load_json(path, modes=(0o600, 0o644))
    exact(value, {'schemaVersion', 'mode', 'coordinationRoot', 'instanceDigest'})
    if type(value['schemaVersion']) is not int or value['schemaVersion'] != 1:
        raise BackupError('control_version')
    if value['mode'] == 'shared-only':
        if value['coordinationRoot'] != '' or value['instanceDigest'] != '':
            raise BackupError('control_shared_fields')
    elif value['mode'] == 'coordinated':
        absolute(value['coordinationRoot'])
        require_hex(value['instanceDigest'])
    else:
        raise BackupError('control_mode')
    return value


def parse_request(arguments, environment, *, control_path=CONTROL, now=None):
    clean_environment(environment, control_path)
    if len(arguments) != 6 or arguments[::2] != ['--request', '--request-sha256', '--result']:
        raise BackupError('invalid_arguments')
    path, checksum, result = arguments[1::2]
    require_hex(checksum)
    raw = read_regular(absolute(path))
    if digest(raw) != checksum:
        raise BackupError('request_digest')
    request = strict_json(raw)
    validate_request(request, path, result, now=now)
    control = load_control(control_path)
    if control['mode'] != 'coordinated':
        raise BackupError('shared_only')
    return request, checksum, control


def validate_request(r, path, result, *, now=None):
    exact(r, REQUEST_KEYS)
    if type(r['protocolVersion']) is not int or r['protocolVersion'] != 1 or r['mode'] != 'service-exclusive-v1' or r['service'] != 'sub2api' or r['job'] != 'sub2api-set':
        raise BackupError('request_identity')
    for key in BINDINGS[:5]:
        if not isinstance(r[key], str) or not UUID.fullmatch(r[key]):
            raise BackupError('request_identity')
    for key in BINDINGS[7:]:
        require_hex(r[key])
    if not isinstance(r['target'], str) or not SAFE.fullmatch(r['target']):
        raise BackupError('request_target')
    operation = absolute(r['operationDir'])
    if operation.name != r['taskId'] or str(path) != str(operation / 'backup-request.json') or str(result) != str(operation / 'backup-result.json'):
        raise BackupError('request_path')
    with opened(operation, directory=True, modes=(0o700,)):
        pass
    if os.path.lexists(result):
        raise BackupError('result_exists')
    exact(r['resourceManifest'], {'path', 'sha256'})
    if r['resourceManifest']['path'] != str(operation / 'backup-resources.json') or r['resourceManifest']['sha256'] != r['resourceDigest']:
        raise BackupError('resource_reference')
    if type(r['deadline']) is not int or r['deadline'] <= (time.time() if now is None else now):
        raise BackupError('deadline')


def binding(r):
    return {key: r[key] for key in BINDINGS}


def load_resources(r):
    raw = read_regular(r['resourceManifest']['path'])
    if digest(raw) != r['resourceDigest']:
        raise BackupError('resource_digest')
    m = strict_json(raw)
    exact(m, {'schemaVersion', 'materialKind', 'target', 'identity', 'containers', 'postgres', 'redis', 'paths', 'roots', 'locks', 'resources', 'runtime'})
    if m['schemaVersion'] != 1 or type(m['schemaVersion']) is not int or m['materialKind'] not in {'synthetic', 'offline'} or m['target'] != r['target']:
        raise BackupError('resource_identity')
    exact(m['identity'], {'objectId', 'tenantId', 'serverId', 'daemonId', 'endpoint', 'project'})
    for key, value in m['identity'].items():
        if key == 'endpoint':
            if not isinstance(value, str) or not value.startswith('unix:///'):
                raise BackupError('daemon_endpoint')
            absolute(value[7:])
        elif not isinstance(value, str) or not SAFE.fullmatch(value):
            raise BackupError('resource_identity')
    exact(m['containers'], {'app', 'postgres', 'redis'})
    for value in m['containers'].values():
        exact(value, {'id', 'generation'})
        require_hex(value['id'])
        if not isinstance(value['generation'], str) or not SAFE.fullmatch(value['generation']):
            raise BackupError('container_generation')
    exact(m['postgres'], {'systemIdentifier', 'user', 'maintenanceDatabase', 'databases', 'roles', 'toolPath', 'version', 'catalogDigest'})
    exact(m['redis'], {'runId', 'version'})
    if any(not isinstance(value, str) or not SAFE.fullmatch(value) for value in m['redis'].values()):
        raise BackupError('redis_identity')
    for key in ('systemIdentifier', 'user', 'maintenanceDatabase', 'version'):
        if not isinstance(m['postgres'][key], str) or not SAFE.fullmatch(m['postgres'][key]):
            raise BackupError('postgres_identity')
    require_hex(m['postgres']['catalogDigest'])
    if m['postgres']['toolPath'] != '/usr/local/bin:/usr/bin:/bin':
        raise BackupError('container_tool_path')
    for key in ('databases', 'roles'):
        values = m['postgres'][key]
        if not isinstance(values, list) or not values or any(not isinstance(x, str) or not SAFE.fullmatch(x) for x in values) or len(set(values)) != len(values):
            raise BackupError('postgres_catalog')
    if m['postgres']['user'] not in m['postgres']['roles'] or m['postgres']['maintenanceDatabase'] not in m['postgres']['databases']:
        raise BackupError('postgres_catalog')
    exact(m['paths'], {'app', 'rdb', 'acl', 'controlled', 'runtime', 'env'})
    for value in m['paths'].values():
        exact(value, {'path', 'uid', 'gid'})
        absolute(value['path'])
        if type(value['uid']) is not int or type(value['gid']) is not int or min(value['uid'], value['gid']) < 0:
            raise BackupError('source_owner')
    app = Path(m['paths']['app']['path'])
    for key, source in m['paths'].items():
        if key != 'app' and (Path(source['path']).is_relative_to(app) or app.is_relative_to(source['path'])):
            raise BackupError('source_overlap')
    exact(m['roots'], {'backup', 'temporary', 'log', 'metrics', 'coordination'})
    for value in m['roots'].values():
        absolute(value)
    roots = list(m['roots'].values()) + [r['operationDir']]
    if any(app.is_relative_to(p) or Path(p).is_relative_to(app) for p in roots):
        raise BackupError('application_root_overlap')
    if len(set(roots)) != len(roots):
        raise BackupError('root_overlap')
    for index, left in enumerate(roots):
        if any(Path(left).is_relative_to(right) or Path(right).is_relative_to(left) for right in roots[index+1:]):
            raise BackupError('root_overlap')
    forbidden = ('/var/backups/ops', '/opt/areaforge', '/var/lib/areasong-ops', '/opt/account-vault')
    for value in roots:
        if any(Path(value).is_relative_to(x) for x in forbidden) and value != r['operationDir']:
            raise BackupError('shared_root')
    exact(m['locks'], JOBS[:3])
    for job, value in m['locks'].items():
        exact(value, {'path', 'device', 'inode'})
        if absolute(value['path']).name != 'ops-backup-' + job + '.lock' or any(type(value[k]) is not int or value[k] < 0 for k in ('device', 'inode')):
            raise BackupError('lock_identity')
    validate_resource_actions(m, r)
    return m


def validate_resource_actions(m, r):
    expected = {**{key: ('read', x['path']) for key, x in m['paths'].items()},
                **{key: ('write', x) for key, x in m['roots'].items()},
                **{key+'-lock': ('coordinate', x['path']) for key, x in m['locks'].items()},
                'operation': ('write', r['operationDir'])}
    exact(m['resources'], expected)
    for key, (action, path) in expected.items():
        value = exact(m['resources'][key], {'selector', 'objectId', 'tenantId', 'serverId', 'actions'})
        if value['selector'] != path or value['actions'] != [action] or value['serverId'] != m['identity']['serverId'] or value['tenantId'] != m['identity']['tenantId']:
            raise BackupError('resource_scope')
        if action == 'coordinate' or key == 'coordination':
            if not isinstance(value['objectId'], str) or not SAFE.fullmatch(value['objectId']) or value['objectId'] == m['identity']['objectId']:
                raise BackupError('coordination_object')
        elif value['objectId'] != m['identity']['objectId']:
            raise BackupError('resource_scope')


class LockSet:
    def __init__(self, locks):
        self.locks, self.handles = locks, []

    def __enter__(self):
        try:
            for job in JOBS[:3]:
                value = self.locks[job]
                handle = opened(value['path'], modes=(0o600,))
                fd, st = handle.__enter__()
                self.handles.append(handle)
                if (st.st_dev, st.st_ino) != (value['device'], value['inode']):
                    raise BackupError('lock_replaced')
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return self
        except BaseException:
            self.__exit__(*sys.exc_info())
            raise

    def __exit__(self, *args):
        for handle in reversed(self.handles):
            handle.__exit__(*args)
        self.handles.clear()


class Journal:
    def __init__(self, control, *, synthetic=False):
        self.root = absolute(control['coordinationRoot'])
        if not synthetic and any(self.root.is_relative_to(p) for p in ('/run', '/tmp', '/var/tmp', '/private/tmp', '/private/var/folders')):
            raise BackupError('persistent_root')
        if 'operations' in self.root.parts:
            raise BackupError('persistent_root')
        with opened(self.root, directory=True, modes=(0o700,)):
            pass
        marker = load_json(self.root / 'enabled.json')
        if marker != {'schemaVersion': 1, 'instanceDigest': control['instanceDigest']}:
            raise BackupError('coordination_marker')
        self.instance = control['instanceDigest']
        self.scan()

    def scan(self, *, active=None):
        lines = read_regular(self.root / 'ledger.jsonl').splitlines()
        if not lines:
            raise BackupError('coordination_ledger')
        head = strict_json(lines[0])
        if head != {'schemaVersion': 1, 'instanceDigest': self.instance}:
            raise BackupError('coordination_ledger')
        with opened(self.root, directory=True, modes=(0o700,)) as (fd, _):
            names = set(os.listdir(fd))
        registered = {}
        for line in lines[1:]:
            entry = exact(strict_json(line), {'callId', 'requestDigest'})
            if not UUID.fullmatch(entry['callId']) or entry['callId'] in registered:
                raise BackupError('coordination_ledger')
            require_hex(entry['requestDigest'])
            registered[entry['callId']] = entry['requestDigest']
        starts = {name[:-11] for name in names if name.endswith('.start.json')}
        if starts != set(registered):
            raise BackupError('coordination_missing_record')
        terminals = {name[:-14] for name in names if name.endswith('.terminal.json')}
        allowed = {'enabled.json', 'ledger.jsonl'} | {c + suffix for c in starts for suffix in ('.start.json', '.terminal.json')}
        if names - allowed or terminals - starts:
            raise BackupError('coordination_corrupt')
        for call in sorted(starts):
            if not UUID.fullmatch(call):
                raise BackupError('coordination_corrupt')
            start = load_json(self.root / (call + '.start.json'))
            exact(start, {'schemaVersion', 'callId', 'requestDigest', 'instanceDigest', 'state'})
            if start['schemaVersion'] != 1 or start['callId'] != call or start['state'] != 'running' or start['instanceDigest'] != self.instance:
                raise BackupError('coordination_corrupt')
            require_hex(start['requestDigest'])
            if start['requestDigest'] != registered[call]:
                raise BackupError('coordination_ledger')
            if call not in terminals:
                if call != active:
                    raise BackupError('persistent_block')
                continue
            terminal = load_json(self.root / (call + '.terminal.json'))
            exact(terminal, {'schemaVersion', 'callId', 'requestDigest', 'instanceDigest', 'state', 'proof'})
            if any(terminal[k] != start[k] for k in ('schemaVersion', 'callId', 'requestDigest', 'instanceDigest')) or terminal['state'] != 'completed':
                raise BackupError('coordination_corrupt')
            require_hex(terminal['proof'])

    def begin(self, call, checksum):
        self.scan()
        # 先持久登记，再写 start。中途丢失 start 也不能被误认成从未运行。
        with opened(self.root, directory=True, modes=(0o700,)) as (directory, _):
            fd = os.open('ledger.jsonl', os.O_WRONLY | os.O_APPEND | os.O_NOFOLLOW, dir_fd=directory)
            with os.fdopen(fd, 'ab') as stream:
                stream.write(encode({'callId': call, 'requestDigest': checksum}) + b'\n')
                stream.flush()
                os.fsync(stream.fileno())
        write_new(self.root / (call + '.start.json'), encode({'schemaVersion': 1, 'callId': call,
                  'requestDigest': checksum, 'instanceDigest': self.instance, 'state': 'running'}))

    def block(self, call, checksum):
        # 即使终态写入回执丢失，也保留额外阻断记录；scan 遇此记录必拒绝。
        write_new(self.root / (call + '.uncertain.json'), encode({'callId': call, 'requestDigest': checksum, 'state': 'uncertain'}))

    def verify_terminal(self, request, checksum, backend):
        # 只有直接注入的可信后端能证明终结；JSON、shell exit 或标记不能替代。
        proof = backend.terminal_proof(request)
        exact(proof, {'callId', 'requestDigest', 'instanceDigest', 'state', 'proof'})
        if proof['callId'] != request['callId'] or proof['requestDigest'] != checksum or proof['instanceDigest'] != self.instance or proof['state'] != 'completed':
            raise BackupError('terminal_unproven')
        require_hex(proof['proof'])
        self._verified_terminal = proof
        return proof

    def finish(self, request, checksum, proof):
        if proof is not getattr(self, '_verified_terminal', None):
            raise BackupError('terminal_unproven')
        self.scan(active=request['callId'])
        write_new(self.root / (request['callId'] + '.terminal.json'), encode({'schemaVersion': 1, **proof}))


def shared_gate(job, path, control, *, fd=9):
    if job not in JOBS[:3] + ('configs',) or absolute(path).name != 'ops-backup-' + job + '.lock':
        raise BackupError('internal_job')
    with opened(path, modes=(0o600,)) as (_, info):
        actual = os.fstat(fd)
        if (actual.st_dev, actual.st_ino) != (info.st_dev, info.st_ino):
            raise BackupError('internal_lock')
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with opened(path, modes=(0o600,)) as (probe, _):
            try:
                fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                pass
            else:
                raise BackupError('internal_lock_unheld')
    if control['mode'] == 'coordinated':
        Journal(control)  # 现存非终态必须阻断，重启不会清除它。
        # 旧 shell 没有可信 daemon/BGSAVE 终结后端；不创建可被误判完成的新调用。
        raise BackupError('shared_terminal_backend_required')


def main():
    args = sys.argv[1:]
    if args and args[0] == 'scoped':
        request, _, _ = parse_request(args[1:], dict(os.environ))
        load_resources(request)
        return
    if len(args) == 12 and args[0] == 'route':
        _, action, phase, operation, target, source, *options = args
        if (action, phase, source) != ('update', 'backup', ''):
            raise BackupError('scoped_route')
        request, _, _ = parse_request(options, dict(os.environ))
        if request['operationDir'] != operation or request['target'] != target:
            raise BackupError('scoped_route_binding')
        return
    if args == ['control']:
        print(digest(read_regular(CONTROL, modes=(0o600, 0o644))))
        load_control()
        return
    if len(args) == 5 and args[0] == 'shared':
        _, job, lock, checksum, descriptor = args
        if descriptor != '9' or checksum != digest(read_regular(CONTROL, modes=(0o600, 0o644))):
            raise BackupError('internal_control')
        shared_gate(job, lock, load_control())
        return
    raise BackupError('invalid_arguments')


if __name__ == '__main__':
    try:
        main()
    except (BackupError, OSError, ValueError, TypeError, KeyError):
        print('ERROR: backup_contract_rejected', file=sys.stderr)
        raise SystemExit(1)
