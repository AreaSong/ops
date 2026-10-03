import type { AccessChangeDetail, BindingReviewValue } from '../types'

function BindingValue({ value, label }: { value: BindingReviewValue | null; label: string }) {
  return <div><h4>{label}</h4>{value ? <dl className="runner-update-detail">
    <div><dt>绑定 ID</dt><dd><code>{value.id}</code></dd></div>
    <div><dt>完整主体哈希</dt><dd><code>{value.subject}</code></dd></div>
    <div><dt>租户 ID</dt><dd>{value.tenantId}</dd></div>
    <div><dt>角色 ID</dt><dd>{value.roleId}</dd></div>
    <div><dt>基准版本完整权限（顺序与重复保留）</dt><dd>{value.permissions.map((p, i) => <code key={i}>{p}{p === '*' ? '（全部权限）' : ''} </code>)}</dd></div>
    <div><dt>对象原列表（顺序与重复保留）</dt><dd><code>{JSON.stringify(value.objectIds)}</code>{(!value.objectIds.length || value.objectIds.includes('*')) && <p>广泛匹配任意对象，可能包含角色允许的平台资源。</p>}</dd></div>
    <div><dt>绑定到期时间</dt><dd>{value.expiresAt ?? '不设绑定到期时间'}</dd></div>
  </dl> : <p>{label === '修改前' ? '此前不存在' : '绑定将不存在（撤销）'}</p>}</div>
}
export function BindingChangeReview({ detail }: { detail: AccessChangeDetail }) {
  if (!detail.binding) return null
  const labels: Record<string, string> = { id: '绑定 ID', subject: '主体', tenantId: '租户', roleId: '角色', permissions: '角色权限', objectIds: '对象范围', expiresAt: '期限', jit: 'JIT', bindingApproval: '绑定自身审批状态' }
  return <>
    <p role="status">{detail.operation === 'create' ? '新增绑定' : detail.operation === 'edit' ? '编辑既有绑定（完整替换）' : '撤销单条绑定'} · 当前差异有效</p>
    <p>实际变化：{detail.binding.changedFields.map(k => labels[k]).join('、') || '无授权字段变化'}。</p>
    <BindingValue value={detail.binding.before} label="修改前" />
    <BindingValue value={detail.binding.after} label="修改后" />
    <p>绑定自身状态：非 JIT；requiresDualApproval=false；approvalState、approvedByHash、secondApprovedByHash 为空；approvedAt、secondApprovedAt 为空。外层提案仍须独立批准，再由创建人应用。</p>
    <p>完整哈希是可关联的假名标识，不是匿名信息。期限清空表示不设到期时间；已过期绑定不保证当前提供权限。撤销单条绑定不保证主体完全失权；创建绑定也不保证主体能登录。</p>
  </>
}
