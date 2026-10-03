import { usePlanDialog } from '../usePlanDialog'
import { useState } from 'react'
import { formatSchedule, scheduleFromLocal } from '../schedule'

interface Props {
  service: string
  action: string
  target: string
  pending: boolean
  error: string
  onCancel: () => void
  onCreate: (scheduleAt?: string) => void
}

export function PlanScheduleDialog({ service, action, target, pending, error, onCancel, onCreate }: Props) {
  const dialogRef = usePlanDialog<HTMLFormElement>(pending, onCancel)
  const [scheduled, setScheduled] = useState(false)
  const [localTime, setLocalTime] = useState('')
  const [validation, setValidation] = useState('')
  let preview = ''
  try { if (scheduled && localTime) preview = formatSchedule(scheduleFromLocal(localTime)) } catch { /* 提交时显示校验错误。 */ }

  return (
    <div className="modal-backdrop">
      <form ref={dialogRef} tabIndex={-1} className="modal plan-dialog" role="dialog" aria-modal="true" aria-labelledby="schedule-title" onSubmit={(event) => {
        event.preventDefault()
        if (pending) return
        try {
          const scheduleAt = scheduled ? scheduleFromLocal(localTime) : undefined
          setValidation('')
          onCreate(scheduleAt)
        } catch (reason) { setValidation((reason as Error).message) }
      }}>
        <header className="modal-header"><h2 id="schedule-title">创建发布计划</h2></header>
        <div className="modal-body">
          <p>{service} · {action} · {target || '当前版本'}</p>
          <label className="confirmation-field">
            <span>执行方式</span>
            <select value={scheduled ? 'scheduled' : 'immediate'} disabled={pending} onChange={(event) => {
              setScheduled(event.target.value === 'scheduled')
              setLocalTime('')
              setValidation('')
            }}>
              <option value="immediate">立即执行</option>
              <option value="scheduled">指定时间</option>
            </select>
          </label>
          {scheduled && <label className="confirmation-field">
            <span>计划执行时间</span>
            <input type="datetime-local" value={localTime} disabled={pending} aria-describedby="schedule-help" aria-invalid={Boolean(validation)} onChange={(event) => { setLocalTime(event.target.value); setValidation('') }} />
          </label>}
          <p id="schedule-help">时区：{Intl.DateTimeFormat().resolvedOptions().timeZone}（浏览器本地时间）。{scheduled ? '这是最早允许执行时间；已到期仍需完成审批并由有权用户手动执行。' : '创建后仍需完成审批并手动执行。'}此时间不代表生产维护窗口授权。</p>
          {preview && <p>执行时间：{preview}</p>}
          {(validation || error) && <p className="inline-error" role="alert">{validation || error}</p>}
        </div>
        <footer className="modal-footer">
          <button type="button" className="button secondary" disabled={pending} onClick={onCancel}>取消</button>
          <button type="submit" className="button primary" disabled={pending}>{pending ? '创建中' : '创建计划'}</button>
        </footer>
      </form>
    </div>
  )
}
