import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { test } from 'node:test'
import yaml from 'js-yaml'

const require = createRequire(import.meta.url)
const eslintRequire = createRequire(require.resolve('@eslint/eslintrc'))
const implementations = [['ESM', yaml], ['ESLint CommonJS', eslintRequire('js-yaml')]]

test('js-yaml 安装版本与已审核锁文件一致', () => {
  const locked = require('../package-lock.json').packages['node_modules/js-yaml']
  assert.equal(require('js-yaml/package.json').version, locked.version)
  assert.equal(eslintRequire('js-yaml/package.json').version, locked.version)
  assert.equal(locked.dev, true)
})

for (const [name, parser] of implementations) {
  test(`${name}：空 mapping 不能绕过合并预算（GHSA-2883-xcg3-v3hh）`, () => {
    const inputs = [
      'empty: &e {}\nmerged:\n  <<: [*e, *e, *e, *e]\n',
      'merged:\n  <<:\n    - {}\n    - {}\n    - {}\n    - {}\n',
    ]
    for (const input of inputs) {
      assert.throws(() => parser.load(input, { maxTotalMergeKeys: 2 }), /maxTotalMergeKeys/)
    }
    assert.throws(() => parser.load('merged: {<<: {}}', { maxTotalMergeKeys: 0 }), /maxTotalMergeKeys/)
  })

  test(`${name}：默认参数拒绝异常长度的合并序列`, () => {
    const input = `empty: &e {}\nmerged: {<<: [${Array(101).fill('*e').join(', ')}]}\n`
    assert.throws(() => parser.load(input), /abnormal merge sequence size/)
  })

  test(`${name}：普通 YAML、合法合并与多文档解析保持正常`, () => {
    const input = 'defaults: &d {enabled: true, retries: 2}\nservice: {<<: *d, name: ops, retries: 3}\n'
    assert.deepEqual(parser.load(input).service, { enabled: true, retries: 3, name: 'ops' })
    assert.deepEqual(parser.load('merged: {<<: {}}', { maxTotalMergeKeys: 1 }), { merged: {} })
    assert.deepEqual(parser.loadAll('enabled: true\n---\nname: ops\n'), [{ enabled: true }, { name: 'ops' }])
  })
}
