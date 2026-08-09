import { AlertTriangle, Check, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { phaseLabel } from '../labels'
import type { Preview } from '../types'
import { StatusBadge } from './StatusBadge'

interface ConfirmationDialogProps {
  preview: Preview
  pending: boolean
  onCancel: () => void
  onConfirm: (value: string) => void
}
export function ConfirmationDialog({ preview, pending, onCancel, onConfirm }: ConfirmationDialogProps) {
  const [value, setValue] = useState('')
  useEffect(() => setValue(''), [preview.id])
  const phrase = preview.confirmationPhrase ?? ''
  const matches = !preview.requiresConfirmation || value === phrase

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.currentTarget === event.target && !pending) onCancel()
    }}>
      <section className="modal" role="dialog" aria-modal="true" aria-labelledby="confirm-title">
        <header className="modal-header">
          <div className="modal-title-group">
            <span className="warning-icon"><AlertTriangle size={20} aria-hidden="true" /></span>
            <div>
              <h2 id="confirm-title">确认生产操作</h2>
              <span>{preview.service} · {preview.action}</span>
            </div>
          </div>
          <StatusBadge kind="risk" value={preview.risk} />
          <button className="icon-button" type="button" onClick={onCancel} disabled={pending} title="关闭">
            <X size={18} aria-hidden="true" />
          </button>
        </header>
        <div className="modal-body">
          <dl className="governance-list">
            <div><dt>影响范围</dt><dd>{preview.scope}</dd></div>
            <div><dt>预期影响</dt><dd>{preview.impact}</dd></div>
            <div><dt>失败处理</dt><dd>{preview.rollback}</dd></div>
          </dl>
          <div className="step-line" aria-label="执行阶段">
            {preview.steps.map((step, index) => (
              <span key={step}>
                <b>{index + 1}</b>{phaseLabel[step] ?? step}
              </span>
            ))}
          </div>
          {preview.requiresConfirmation && (
            <label className="confirmation-field">
              <span>输入确认短语</span>
              <code>{phrase}</code>
              <input
                autoFocus
                type="text"
                value={value}
                onChange={(event) => setValue(event.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
            </label>
          )}
        </div>
        <footer className="modal-footer">
          <button type="button" className="button secondary" onClick={onCancel} disabled={pending}>取消</button>
          <button
            type="button"
            className="button danger"
            disabled={!matches || pending}
            onClick={() => onConfirm(value)}
          >
            <Check size={17} aria-hidden="true" />
            {pending ? '提交中' : '确认执行'}
          </button>
        </footer>
      </section>
    </div>
  )
}
