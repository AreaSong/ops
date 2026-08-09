import type { Risk, TaskState } from '../types'
import { riskLabel, stateLabel } from '../labels'

type BadgeProps =
  | { kind: 'risk'; value: Risk }
  | { kind: 'state'; value: TaskState }
  | { kind: 'health'; value: 'healthy' | 'warning' | 'error' | 'unknown'; label: string }

export function StatusBadge(props: BadgeProps) {
  if (props.kind === 'risk') {
    return <span className={`badge badge-risk-${props.value}`}>{riskLabel[props.value]}</span>
  }
  if (props.kind === 'state') {
    return <span className={`badge badge-state-${props.value}`}>{stateLabel[props.value]}</span>
  }
  return <span className={`badge badge-health-${props.value}`}>{props.label}</span>
}
