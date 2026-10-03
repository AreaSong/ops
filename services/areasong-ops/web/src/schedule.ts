export function scheduleFromLocal(value: string): string {
  if (!value) throw new Error('请选择执行时间。')
  const date = new Date(value)
  const parts = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value)
  // 拒绝无效日期和夏令时跳跃中不存在的本地时间，避免 Date 静默归一化。
  if (!parts || !Number.isFinite(date.getTime()) ||
    [date.getFullYear(), date.getMonth() + 1, date.getDate(), date.getHours(), date.getMinutes()]
      .some((part, index) => part !== Number(parts[index + 1]))) {
    throw new Error('执行时间无效，请选择有效的本地日期与时间。')
  }
  return date.toISOString()
}

export function formatSchedule(value: string): string {
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return '执行时间无效'
  return `${new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit',
    minute: '2-digit', second: '2-digit', hour12: false, timeZoneName: 'longOffset',
  }).format(date)}（${Intl.DateTimeFormat().resolvedOptions().timeZone}）`
}

export function isScheduleDue(scheduleAt: string | undefined, now: number): boolean {
  return Boolean(scheduleAt && Number.isFinite(Date.parse(scheduleAt)) && now >= Date.parse(scheduleAt))
}
