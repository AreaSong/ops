import { CheckCircle2, KeyRound, LoaderCircle, RotateCcw, ShieldAlert } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { formatTime, shortHash } from '../labels'
import type { CredentialProfile, CredentialRotation } from '../types'

interface CredentialsProps {
  profile: CredentialProfile | null
  loading: boolean
  onRefresh: () => void
  onRotate: (secret: string, expiresAt: string, confirmation: string) => Promise<void>
  onClose: (rotation: CredentialRotation, confirmation: string) => Promise<void>
}

const rotationStateLabel: Record<CredentialRotation['state'], string> = {
  running: '执行中',
  failed: '验证失败',
  rolled_back: '已自动回滚',
  needs_attention: '需要人工核对',
  switched_pending_revocation: '等待撤销旧凭据',
  revocation_verified: '正在完成收口',
  completed: '已完成',
}

export function Credentials({ profile, loading, onRefresh, onRotate, onClose }: CredentialsProps) {
  const [secret, setSecret] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [closeConfirmation, setCloseConfirmation] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [closing, setClosing] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!profile) return
    setSubmitting(true)
    const oneTimeSecret = secret
    setSecret('')
    try {
      await onRotate(oneTimeSecret, expiresAt, confirmation)
      setConfirmation('')
    } finally {
      setSubmitting(false)
    }
  }

  async function closeRotation() {
    if (!profile?.lastRotation) return
    setClosing(true)
    try {
      await onClose(profile.lastRotation, closeConfirmation)
      setCloseConfirmation('')
    } finally {
      setClosing(false)
    }
  }

  const activeRotation = profile?.lastRotation?.state === 'switched_pending_revocation' ||
    profile?.lastRotation?.state === 'revocation_verified'
  const rotationBlocksNew = profile?.lastRotation != null &&
    ['running', 'switched_pending_revocation', 'revocation_verified', 'needs_attention'].includes(profile.lastRotation.state)

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">类型化敏感变更</span><h1>凭据轮换</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新凭据状态">
          {loading ? <LoaderCircle className="spin" size={17} /> : <RotateCcw size={17} />}
        </button>
      </header>

      {!profile && !loading && <div className="empty-state">凭据状态当前不可用</div>}
      {profile && (
        <>
          <section className="credential-summary">
            <div className="credential-heading">
              <span className="credential-icon"><KeyRound size={20} /></span>
              <div><h2>{profile.displayName}</h2><p>{profile.target}</p></div>
              <span className={profile.configured ? 'credential-health healthy' : 'credential-health warning'}>
                {profile.configured ? '已配置' : '未配置'}
              </span>
            </div>
            <dl className="credential-facts">
              <div><dt>固定仓库</dt><dd><code>{profile.repository}</code></dd></div>
              <div><dt>当前短指纹</dt><dd><code>{shortHash(profile.fingerprint)}</code></dd></div>
              <div><dt>当前到期日</dt><dd>{profile.expiresAt || '未配置'}</dd></div>
              <div><dt>风险等级</dt><dd>高风险</dd></div>
            </dl>
          </section>

          {profile.lastRotation && (
            <section className="page-section">
              <div className="section-heading"><h2>最近一次轮换</h2><span>{formatTime(profile.lastRotation.createdAt)}</span></div>
              <div className={`rotation-status rotation-${profile.lastRotation.state}`}>
                {profile.lastRotation.state === 'completed' ? <CheckCircle2 size={19} /> : <ShieldAlert size={19} />}
                <div>
                  <strong>{rotationStateLabel[profile.lastRotation.state]}</strong>
                  <p>{profile.lastRotation.outcome || profile.lastRotation.validationResult || '等待结果'}</p>
                  <small>新凭据 {profile.lastRotation.fingerprint} · 到期 {profile.lastRotation.expiresAt}</small>
                </div>
              </div>
              {activeRotation && (
                <div className="credential-closure">
                  <div>
                    <strong>{profile.lastRotation.state === 'revocation_verified' ? '继续完成轮换收口' : '在 GitHub 撤销旧 Token 后收口'}</strong>
                    <p>{profile.lastRotation.state === 'revocation_verified'
                      ? '旧 Token 的撤销证据已持久化；可安全重试清理 root-only 回滚副本。'
                      : '控制面会在线验证旧 Token 已失效，然后删除 root-only 回滚副本。'}</p>
                  </div>
                  <label className="credential-field">
                    <span>确认短语</span>
                    <code>我已撤销旧 GitHub Token</code>
                    <input value={closeConfirmation} onChange={(event) => setCloseConfirmation(event.target.value)}
                      autoComplete="off" spellCheck={false} />
                  </label>
                  <button className="button danger" type="button" onClick={() => void closeRotation()}
                    disabled={closing || closeConfirmation !== '我已撤销旧 GitHub Token'}>
                    {closing ? '验证中' : '验证撤销并收口'}
                  </button>
                </div>
              )}
            </section>
          )}

          <section className="page-section">
            <div className="section-heading"><h2>轮换新凭据</h2><span>固定目标 · 固定仓库 · Issues 读写</span></div>
            <form className="credential-form" onSubmit={(event) => void submit(event)} autoComplete="off">
              <input className="credential-username" type="text" name="username"
                value="github-alertmanager-token" autoComplete="username" readOnly tabIndex={-1} />
              <label className="credential-field">
                <span>新 fine-grained Token</span>
                <input type="password" name="github-alertmanager-token" value={secret}
                  onChange={(event) => setSecret(event.target.value)} autoComplete="new-password"
                  minLength={32} maxLength={512} spellCheck={false} required />
              </label>
              <label className="credential-field">
                <span>到期日</span>
                <input type="date" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} required />
              </label>
              <label className="credential-field credential-confirmation">
                <span>确认短语</span>
                <code>{profile.confirmationPhrase}</code>
                <input value={confirmation} onChange={(event) => setConfirmation(event.target.value)}
                  autoComplete="off" spellCheck={false} required />
              </label>
              <div className="credential-boundary">
                Token 只经本次 HTTPS 请求、Unix Socket 和 Runner 内存传递；不会写入浏览器存储、SQLite、日志、任务事件或普通备份。
              </div>
              <button className="button danger" type="submit"
                disabled={submitting || rotationBlocksNew || confirmation !== profile.confirmationPhrase || !secret || !expiresAt}>
                {submitting ? <><LoaderCircle className="spin" size={15} />验证并切换</> : '验证并轮换'}
              </button>
            </form>
          </section>
        </>
      )}
    </div>
  )
}
