import type { PlanState, Risk, TaskState } from './types'

export const riskLabel: Record<Risk, string> = {
  read_only: '只读',
  low: '低风险',
  medium: '中风险',
  high: '高风险',
}

export const stateLabel: Record<TaskState, string> = {
  waiting_confirmation: '等待确认',
  queued: '排队中',
  running: '执行中',
  rolling_back: '回滚中',
  succeeded: '成功',
  failed: '失败',
  failed_recoverable: '可恢复失败',
  needs_attention: '需要人工处理',
  rolled_back: '已回滚',
  recovery_uncertain: '恢复待核对',
}

export const planStateLabel: Record<PlanState, string> = {
  pending_approval: '等待批准',
  approved: '等待执行',
  executing: '执行中',
  completed: '已完成',
  invalidated: '已失效',
}

export const phaseLabel: Record<string, string> = {
  queued: '排队',
  inspect: '检查',
  discover: '发现版本',
  preflight: '前置核验',
  backup: '新鲜备份',
  migration: '迁移门禁',
  apply: '执行变更',
  restart: '重启应用',
  health: '健康检查',
  smoke: '业务抽测',
  identity: '身份核验',
  drill: '隔离恢复',
  verify: '结果验证',
  rollback: '失败回滚',
  terminal: '完成',
}

export function formatTime(value?: string): string {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(new Date(value))
}

export function shortHash(value?: string): string {
  if (!value) return '—'
  return value.length > 18 ? `${value.slice(0, 10)}…${value.slice(-6)}` : value
}
