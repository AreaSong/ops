import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
const { outputText } = ts.transpileModule(readFileSync(new URL('../src/schedule.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
})
const { scheduleFromLocal, formatSchedule, isScheduleDue } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

test('明确时区转换、非法日期及夏令时跳跃校验', () => {
  const previous = process.env.TZ
  try {
    process.env.TZ = 'Asia/Shanghai'
    assert.equal(scheduleFromLocal('2030-01-02T09:30'), '2030-01-02T01:30:00.000Z')
    const formatted = formatSchedule('2030-01-02T01:30:00Z')
    for (const part of ['2030', '09:30', 'GMT+08:00', 'Asia/Shanghai']) assert.ok(formatted.includes(part))
    for (const invalid of ['', 'garbage', '2030-02-30T10:00', '2030-01-01T10:00Z']) assert.throws(() => scheduleFromLocal(invalid))
    assert.equal(scheduleFromLocal('2000-01-01T10:00'), '2000-01-01T02:00:00.000Z') // 已到期仍可重试原请求，执行门禁在服务端。
    process.env.TZ = 'America/New_York'
    assert.throws(() => scheduleFromLocal('2030-03-10T02:30'), /无效/)
    assert.equal(scheduleFromLocal('2030-01-02T09:30'), '2030-01-02T14:30:00.000Z')
  } finally { if (previous === undefined) delete process.env.TZ; else process.env.TZ = previous }
})

test('到期边界使用可控时间；无效时间不能放行', () => {
  const at = '2030-01-02T01:30:00Z', due = Date.parse(at)
  assert.equal(isScheduleDue(at, due - 1), false)
  assert.equal(isScheduleDue(at, due), true)
  assert.equal(isScheduleDue(at, due + 1), true)
  assert.equal(isScheduleDue(undefined, due), false)
  assert.equal(isScheduleDue('bad', due), false)
})
