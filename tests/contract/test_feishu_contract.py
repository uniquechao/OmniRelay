"""脱敏接口样例契约检查；不替代真实 HTTP、数据库和平台验收。"""
import copy
import json
import re
import unittest
from datetime import datetime
from pathlib import Path

FIXTURES = Path(__file__).resolve().parents[1] / 'fixtures' / 'feishu'
CONNECTIONS = {'disabled', 'connecting', 'connected', 'reconnecting', 'unavailable'}
STATES = {'queued', 'running', 'succeeded', 'failed'}
ERRORS = {'credentials_missing', 'credentials_rejected', 'bot_unavailable', 'provider_temporarily_unavailable'}


def require(condition):
    if not condition:
        raise ValueError('契约校验失败')


def fields(obj, required, optional=()):
    require(type(obj) is dict)
    require(set(required) <= obj.keys() <= set(required) | set(optional))


def integer(value, minimum=0):
    require(type(value) is int and value >= minimum)


def timestamp(value, nullable=False):
    if nullable and value is None:
        return
    require(type(value) is str and bool(re.fullmatch(r'\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z', value)))
    datetime.fromisoformat(value.replace('Z', '+00:00'))


def text(value):
    require(type(value) is str and bool(value))


def config(item):
    fields(item, ['name', 'status', 'headers', 'body'])
    require(item['status'] == 200)
    b = item['body']
    fields(b, ['app_id', 'tenant_key', 'credentials_configured', 'bot_name', 'oauth_callback_url', 'enabled', 'version', 'connection'])
    for key in ['app_id', 'tenant_key']:
        text(b[key])
    require(type(b['bot_name']) is str)
    require(b['oauth_callback_url'] == 'https://relay.example.test/auth/feishu/callback')
    require(type(b['enabled']) is bool and type(b['credentials_configured']) is bool)
    integer(b['version'])
    fields(item['headers'], ['ETag'])
    require(item['headers']['ETag'] == f'"bot-{b["version"]}"')
    c = b['connection']
    fields(c, ['state', 'last_heartbeat_at', 'last_message_at'])
    require(c['state'] in CONNECTIONS)
    require(b['enabled'] == (c['state'] != 'disabled'))
    timestamp(c['last_heartbeat_at'], True)
    timestamp(c['last_message_at'], True)
    if c['state'] == 'connected':
        timestamp(c['last_heartbeat_at'])
    if b['version'] == 0:
        require(not b['enabled'] and c['state'] == 'disabled')


def lifecycle(b):
    require(b['state'] in STATES)
    for k in ['created_at', 'started_at', 'finished_at']:
        timestamp(b[k], k != 'created_at')
    require((b['started_at'] is None) == (b['state'] == 'queued'))
    require((b['finished_at'] is not None) == (b['state'] in {'succeeded', 'failed'}))
    if b['started_at']:
        require(b['created_at'] <= b['started_at'])
    if b['finished_at']:
        require(b['started_at'] <= b['finished_at'])


def check(b):
    fields(b, ['id', 'state', 'credentials_generation', 'created_at', 'started_at', 'finished_at', 'results'])
    text(b['id'])
    require(type(b['credentials_generation']) is str and bool(re.fullmatch('[1-9][0-9]*', b['credentials_generation'])))
    lifecycle(b)
    require(type(b['results']) is list)
    names = []
    for result in b['results']:
        fields(result, ['name', 'state', 'checked_at'], ['error'])
        require(result['name'] in {'tenant_token', 'bot_info'})
        require(result['state'] in {'passed', 'failed', 'skipped'})
        timestamp(result['checked_at'])
        if result['state'] == 'failed':
            require(result.get('error') in ERRORS)
        else:
            require('error' not in result)
        names.append(result['name'])
    require(len(set(names)) == len(names))
    if b['state'] in {'succeeded', 'failed'}:
        require(names == ['tenant_token', 'bot_info'])
        require((b['state'] == 'succeeded') == all(r['state'] == 'passed' for r in b['results']))
        if b['state'] == 'failed':
            require(any(r['state'] == 'failed' for r in b['results']))
    if b['state'] == 'queued':
        require(not names)


def scope(b):
    if b is None:
        return
    fields(b, ['department_roots', 'explicit_people', 'entire_org'])
    integer(b['department_roots'])
    integer(b['explicit_people'])
    require(type(b['entire_org']) is bool)


RUN_FIELDS = ['id', 'trigger', 'state', 'created_at', 'started_at', 'finished_at', 'department_count', 'people_count', 'missing_people_count', 'outside_scope_count', 'new_people_count', 'changed_people_count', 'scope']
STATUS_FIELDS = ['snapshot_version', 'scope', 'visible', 'last_success_at', 'next_run_at', 'interval_seconds']


def run(b, submission=False):
    fields(b, RUN_FIELDS + (['coalesced'] if submission else []), ['failure_reason'])
    integer(b['id'], 1)
    require(b['trigger'] in {'initial', 'scheduled', 'manual'})
    lifecycle(b)
    scope(b['scope'])
    for k in RUN_FIELDS:
        if k.endswith('_count'):
            integer(b[k])
    if b['state'] == 'failed':
        text(b.get('failure_reason'))
    else:
        require('failure_reason' not in b)
    if submission:
        require(type(b['coalesced']) is bool and b['state'] in {'queued', 'running'})


def department_path(value):
    require(type(value) is list and bool(value))
    for name in value:
        text(name)


def directory(b, kind):
    extras = ['items', 'total'] + (['current', 'latest_attempt'] if kind == 'runs' else [])
    fields(b, STATUS_FIELDS + extras)
    require(type(b['visible']) is bool)
    if b['visible']:
        integer(b['snapshot_version'], 1)
        require(b['scope'] is not None)
    else:
        require(b['snapshot_version'] is None and b['scope'] is None)
        if kind != 'runs':
            require(b['items'] == [] and b['total'] == 0)
    scope(b['scope'])
    timestamp(b['last_success_at'], True)
    timestamp(b['next_run_at'], True)
    integer(b['interval_seconds'])
    integer(b['total'])
    require(type(b['items']) is list and b['total'] >= len(b['items']))
    seen = set()
    for item in b['items']:
        if kind == 'runs':
            run(item)
            key = item['id']
        elif kind == 'departments':
            fields(item, ['id', 'parent_id', 'name', 'path', 'has_children'])
            for k in ['id', 'name']:
                text(item[k])
            department_path(item['path'])
            require(type(item['parent_id']) is str and type(item['has_children']) is bool)
            key = item['id']
        else:
            fields(item, ['open_id', 'name', 'avatar_url', 'directory_status', 'account_id', 'account_status', 'departments', 'platform_roles'])
            text(item['open_id'])
            text(item['name'])
            require(type(item['avatar_url']) is str and (not item['avatar_url'] or item['avatar_url'].startswith('https://')))
            require(item['directory_status'] in {'unknown', 'active', 'inactive', 'resigned'})
            require(item['account_status'] in {'active', 'inactive', 'unprovisioned'})
            require((item['account_id'] is None) == (item['account_status'] == 'unprovisioned'))
            if item['account_id'] is not None:
                text(item['account_id'])
            require(type(item['departments']) is list and type(item['platform_roles']) is list)
            for d in item['departments']:
                fields(d, ['id', 'path'])
                text(d['id'])
                department_path(d['path'])
            for role in item['platform_roles']:
                text(role)
            key = item['open_id']
        require(key not in seen)
        seen.add(key)
    if kind == 'runs':
        for k in ['current', 'latest_attempt']:
            if b[k] is not None:
                run(b[k])
        if b['current'] is not None:
            require(b['current']['state'] in {'queued', 'running'})


def expected_http(r):
    """目标契约的独立决策表；真实服务适配器应重放同一输入。"""
    if r['session'] != 'valid':
        return 401, 'unauthorized'
    if r['role'] == 'member' or (r['method'] == 'PATCH' and r['role'] != 'admin'):
        return 403, 'forbidden'
    if r['method'] in {'PATCH', 'POST'}:
        if r['origin'] != 'same' or r['csrf'] != 'valid':
            return 403, 'forbidden'
    if r['method'] == 'PATCH':
        if type(r['body']) is not dict or set(r['body']) != {'enabled'} or type(r['body']['enabled']) is not bool:
            return 400, 'invalid_request'
        if r['if_match'] is None:
            return 428, 'config_version_required'
        if r['if_match'] != '"bot-0"':
            return 412, 'config_version_conflict'
    if r['method'] == 'POST':
        if r['path'] == '/api/v1/system/directory/sync-runs':
            return 202, None
        if r['body'] != {}:
            return 400, 'invalid_request'
        if type(r['idempotency_key']) is not str or not re.fullmatch('[A-Za-z0-9._:-]{1,128}', r['idempotency_key']):
            return 400, 'invalid_idempotency_key'
        return 202, None
    if r['path'].endswith(('check-missing', 'check-other-source')):
        return 404, 'check_not_found'
    if 'snapshot_version=16' in r['path']:
        return 409, 'directory_snapshot_changed'
    return 200, None


def load(name):
    return json.loads((FIXTURES / (name + '.json')).read_text())


class FeishuContract(unittest.TestCase):
    def test_bot_configs(self):
        items = load('bot-config')
        self.assertEqual({x['body']['connection']['state'] for x in items}, CONNECTIONS)
        for item in items:
            with self.subTest(name=item['name']):
                config(item)
        self.assertIsNone(items[2]['body']['connection']['last_message_at'])

    def test_checks(self):
        items = load('bot-checks')
        self.assertEqual({x['body']['state'] for x in items}, STATES)
        self.assertEqual({r['error'] for x in items for r in x['body']['results'] if 'error' in r}, ERRORS)
        for item in items:
            with self.subTest(name=item['name']):
                check(item['body'])
        submissions = load('check-submissions')
        for item in submissions:
            fields(item, ['name', 'status', 'body'])
            self.assertEqual(item['status'], 202)
            fields(item['body'], ['id', 'state', 'reused'])
            text(item['body']['id'])
            self.assertIn(item['body']['state'], STATES)
            self.assertIs(type(item['body']['reused']), bool)
        self.assertEqual(len({x['body']['id'] for x in submissions}), 1)
        self.assertEqual([x['body']['reused'] for x in submissions], [False, True, True, True])

    def test_oauth(self):
        items = load('oauth')
        self.assertEqual({x['name'] for x in items}, {'cancelled', 'denied', 'unavailable', 'legacy', 'invalid-state', 'expired-state', 'replayed-state', 'wrong-browser'})
        for item in items:
            fields(item, ['name', 'transaction_valid', 'status', 'location', 'session_created'])
            self.assertIs(item['transaction_valid'], None if item['name'] == 'legacy' else item['status'] == 303)
            self.assertIs(item['session_created'], False)
            if item['transaction_valid'] is False:
                self.assertEqual((item['status'], item['location']), (400, None))
            else:
                self.assertEqual(item['status'], 303)
                self.assertEqual(item['location'], '/#/login?error=' + ('feishu' if item['name'] == 'legacy' else item['name']))

    def test_directory(self):
        people = [p for x in load('directory') for p in x['people']['items']]
        self.assertEqual({p['directory_status'] for p in people}, {'unknown', 'active', 'inactive', 'resigned'})
        self.assertEqual({p['account_status'] for p in people}, {'unprovisioned', 'active', 'inactive'})
        self.assertEqual({x['body']['state'] for x in load('directory-submissions')}, STATES)
        self.assertEqual({x['body']['coalesced'] for x in load('directory-submissions') if x['status'] == 202}, {False, True})
        for item in load('directory'):
            fields(item, ['name', 'departments', 'people', 'runs'])
            versions = set()
            for kind in ['departments', 'people', 'runs']:
                with self.subTest(name=item['name'], kind=kind):
                    directory(item[kind], kind)
                    versions.add(item[kind]['snapshot_version'])
            self.assertEqual(len(versions), 1)
        for item in load('directory-submissions'):
            fields(item, ['name', 'status', 'body'])
            self.assertIn(item['status'], {200, 202})
            run(item['body'], item['status'] == 202)

    def test_http_matrix(self):
        rows = load('http-matrix')
        self.assertEqual({x['expected']['status'] for x in rows}, {200, 202, 400, 401, 403, 404, 409, 412, 428})
        self.assertEqual(len({x['name'] for x in rows}), len(rows))
        for row in rows:
            with self.subTest(name=row['name']):
                fields(row, ['name', 'request', 'expected'])
                fields(row['request'], ['method', 'path', 'role', 'session', 'origin', 'csrf', 'if_match', 'idempotency_key', 'body'])
                fields(row['expected'], ['status'], ['error'])
                self.assertIn(row['request']['method'], {'GET', 'POST', 'PATCH'})
                self.assertIn(row['request']['role'], {'admin', 'operator', 'member'})
                self.assertIn(row['request']['session'], {'valid', 'expired', 'absent'})
                self.assertIn(row['request']['origin'], {'same', 'cross', 'absent'})
                self.assertIn(row['request']['csrf'], {'valid', 'invalid', 'absent'})
                self.assertEqual(expected_http(row['request']), (row['expected']['status'], row['expected'].get('error')))

    def test_idempotency_sequences(self):
        cases = load('idempotency-sequences')
        self.assertEqual(len(cases), 12)
        for case in cases:
            with self.subTest(name=case['name']):
                fields(case, ['name', 'source', 'generation', 'key', 'now_seconds', 'bindings', 'active_tasks', 'expected'])
                fields(case['source'], ['tenant_key', 'app_id'])
                integer(case['now_seconds'])
                candidates = []
                for task in case['bindings'] + case['active_tasks']:
                    fields(task, ['id', 'generation', 'finished_seconds', 'tenant_key', 'app_id'], ['key'])
                    text(task['id'])
                    require(bool(re.fullmatch('[1-9][0-9]*', task['generation'])))
                    if task['finished_seconds'] is not None:
                        integer(task['finished_seconds'])
                def same_source(task):
                    return all(task[k] == case['source'][k] for k in ['tenant_key', 'app_id'])
                candidates = [b for b in case['bindings'] if same_source(b) and b['key'] == case['key']
                              and (b['finished_seconds'] is None or case['now_seconds'] < b['finished_seconds'] + 86400)]
                if not candidates:
                    candidates = [a for a in case['active_tasks'] if same_source(a)
                                  and a['generation'] == case['generation'] and a['finished_seconds'] is None]
                actual = dict(id=candidates[0]['id'], reused=True) if candidates else dict(id='new-task', reused=False)
                self.assertEqual(actual, case['expected'])

    def test_reject_invalid_contracts(self):
        # 反例验证校验器确实拒绝类型、枚举、版本、时间和敏感字段错误。
        base = load('bot-config')[0]
        for key, value in [('version', True), ('version', -1), ('enabled', 'false'), ('app_secret', '禁止字段')]:
            b = copy.deepcopy(base)
            b['body'][key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                config(b)
        for key, value in [('state', 'online'), ('last_heartbeat_at', '2026-11-17T20:00:00+08:00'), ('last_message_at', '2026-99-17T20:00:00Z')]:
            b = copy.deepcopy(base)
            b['body']['connection'][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                config(b)
        b = copy.deepcopy(base)
        b['headers']['ETag'] = '"bot-9"'
        with self.assertRaises(ValueError):
            config(b)
        for key, value in [('state', 'done'), ('credentials_generation', '0'), ('finished_at', None), ('access_token', '禁止字段')]:
            b = copy.deepcopy(load('bot-checks')[3]['body'])
            b[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                check(b)
        b = copy.deepcopy(load('bot-checks')[3]['body'])
        b['results'][0]['error'] = '原始平台错误'
        with self.assertRaises(ValueError):
            check(b)
        b = copy.deepcopy(load('directory')[3]['people'])
        b['items'] = load('directory')[1]['people']['items']
        with self.assertRaises(ValueError):
            directory(b, 'people')

        for invalid in ['研发/运维', [], ['研发', 5]]:
            b = copy.deepcopy(load('directory')[1]['people'])
            b['items'][0]['departments'][0]['path'] = invalid
            with self.subTest(path=invalid), self.assertRaises(ValueError):
                directory(b, 'people')


if __name__ == '__main__':
    unittest.main()
