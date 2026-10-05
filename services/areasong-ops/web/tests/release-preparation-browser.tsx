import { createRoot } from 'react-dom/client'
import { useState } from 'react'
import { ConfirmationDialog } from '../src/components/ConfirmationDialog'
import type { ReleasePlan } from '../src/types'
import '../src/styles.css'

function Fixture() {
  const [state, setState] = useState<'pending_approval' | 'approved' | 'observing'>('pending_approval')
  const [legacy, setLegacy] = useState(false)
  const [visible, setVisible] = useState(true)
  const plan: ReleasePlan = {
    id: 'synthetic-plan', actorHash: 'a'.repeat(64), service: '合成应用', action: 'update',
    tenantId: '目标租户', serverId: 'synthetic-local', target: 'v1.1.0', risk: 'high', state,
    digest: 'd'.repeat(64), requiresConfirmation: true, confirmationPhrase: '批准合成计划',
    requiresDualApproval: true, approvalPolicy: 'two_party_v1',
    createdAt: '2026-10-04T00:00:00Z', updatedAt: '2026-10-04T00:00:00Z',
    approvedByHash: state === 'pending_approval' ? undefined : 'b'.repeat(64),
    observationEndsAt: state === 'observing' ? '2026-10-04T00:00:00Z' : undefined,
    approvalSummary: {
      schemaVersion: legacy ? 1 : 2, service: '合成应用', action: 'update', risk: 'high',
      approvalPolicy: 'two_party_v1', target: 'v1.1.0', impact: '仅合成验证', rollback: '不调用适配器',
      scope: '创建者及两个目标租户', steps: ['preflight', 'backup', 'apply'], expectedBefore: {},
      ...(legacy ? {} : { lifecycle: {
        version: 1 as const, source: 'manual_single' as const, executionMode: 'local' as const,
        preparationId: 'synthetic-inspection', creatorTenantId: '创建者租户',
        targets: [{ tenantId: '创建者租户', expectedGeneration: '1' }, { tenantId: '目标租户', expectedGeneration: '9223372036854775807' }],
        targetObjects: [{ objectId: 'service:synthetic-application-with-a-long-stable-identifier', tenantId: '目标租户', serverId: 'synthetic-local' }],
        scopeDigest: 'e'.repeat(64),
      } }),
    },
  }
  return <>
    <button onClick={() => { setLegacy(false); setState('pending_approval'); setVisible(true) }}>审阅新计划</button>
    <button onClick={() => { setLegacy(true); setState('observing'); setVisible(true) }}>旧观察计划</button>
    <button onClick={() => { setLegacy(false); setState('approved'); setVisible(true) }}>已批准计划</button>
    {visible && <ConfirmationDialog plan={plan} pending={false} currentActorHash={state === 'pending_approval' ? 'b'.repeat(64) : 'a'.repeat(64)}
      onCancel={() => setVisible(false)} onConfirm={() => { document.body.dataset.approvals = '1' }} onClosePlan={() => { document.body.dataset.closed = '1' }} />}
  </>
}
createRoot(document.getElementById('root')!).render(<Fixture />)
