import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
const cache = new Map()
function moduleURL(file) {
 if(cache.has(file))return cache.get(file)
 let s=readFileSync(new URL(`../src/${file}.ts`,import.meta.url),'utf8').replace(/from '\.\/(bindingChange|accessChangeApply|accessChangeReview|roleChange)'/g,(_,name)=>`from '${moduleURL(name)}'`)
 const url=`data:text/javascript;base64,${Buffer.from(ts.transpileModule(s,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ES2022}}).outputText).toString('base64')}`;cache.set(file,url);return url
}
const m=await import(moduleURL('bindingRevoke')), {applyReviewedAccessChange:apply}=await import(moduleURL('accessChangeApply'))
const actor='a'.repeat(64), other='b'.repeat(64)
const original={id:'ordinary',subject:'c'.repeat(64),tenantId:'default',roleId:'viewer',objectIds:['x, y','duplicate','duplicate'],expiresAt:'2000-01-01T08:00:00.123456789+08:00',createdBy:actor,createdAt:'2026-10-03T00:00:00Z'}
const access={canManage:true,currentSubject:{subject:actor,tenantId:'default'},version:7,bindings:[original],roles:[{id:'viewer',permissions:['ops.read']}],tenants:[{id:'default',status:'active'}]}
const change={id:'00000000-0000-4000-8000-000000000001',requestDigest:'sha256:'+'d'.repeat(64),state:'approved',actorHash:actor,approvedByHash:other,requiresDualApproval:true,approvalPolicy:'two_party_v1'}
const before={id:original.id,subject:original.subject,tenantId:'default',roleId:'viewer',permissions:['ops.read'],objectIds:original.objectIds,expiresAt:'2000-01-01T00:00:00.123456789Z',jit:false,bindingApproval:'default'}
const detail={...change,reviewerHash:actor,kind:'binding',operation:'revoke',availability:'ready',expectedVersion:7,currentVersion:7,binding:{before,after:null,changedFields:Object.keys(before)}}
const view={...access,pendingChanges:[change]}, applied={...change,state:'applied',appliedPolicyVersion:8}, after={...view,version:8,bindings:[],pendingChanges:[applied]}
test('撤销只发精确目标/版本/审批/UUID，不附授权字段；普通已过期记录允许',()=>{
 const body=m.bindingRevokeRequest(original,access),map=new Map(), storage={getItem:k=>map.get(k),setItem:(k,v)=>map.set(k,v),removeItem:k=>map.delete(k)}
 const key=m.bindingRevokeKey(actor,'default',body,storage)
 assert.deepEqual({...body,idempotencyKey:key},{removeBindingIds:['ordinary'],expectedVersion:7,requiresDualApproval:true,idempotencyKey:key});assert.match(key,/^[a-f0-9-]{36}$/)
 assert.equal(key,m.bindingRevokeKey(actor,'default',body,storage));for(const [a,t,b] of [[other,'default',body],[actor,'other',body],[actor,'default',{...body,expectedVersion:8}],[actor,'default',{...body,removeBindingIds:['other']} ]])assert.notEqual(key,m.bindingRevokeKey(a,t,b,storage))
 m.forgetBindingRevokeKey(actor,'default',body,storage);assert.notEqual(key,m.bindingRevokeKey(actor,'default',body,storage))
})
test('六审批字段、JIT、bootstrap、未知字段、异常来源、ID别名和无效租户阻断',()=>{
 for(const patch of [{jit:true},{requiresDualApproval:true},{approvalState:'approved'},{approvedByHash:actor},{secondApprovedByHash:actor},{approvedAt:'2000-01-01T00:00:00Z'},{secondApprovedAt:'2000-01-01T00:00:00Z'},{createdBy:'bootstrap'},{createdBy:''},{future:false},{id:'ORDINARY'},{tenantId:'*'},{expiresAt:''},{updatedAt:'2026-01-01T00:00:00Z'}])assert.ok(m.bindingRevokeRestriction({...original,...patch},access),JSON.stringify(patch))
 for(const patch of [{bindings:[]},{bindings:[original,{...original,id:'ORDINARY'}]},{tenants:[{id:'default',status:'disabled'}]},{roles:[{id:'viewer',permissions:['unknown']}]}])assert.throws(()=>m.bindingRevokeRequest(original,{...access,...patch}))
})
test('冻结身份/租户/版本/原目标/角色，原目标消失不算撤销成功',()=>{
 m.verifyBindingRevoke(original,access,access)
 for(const patch of [{version:8},{canManage:false},{currentSubject:{subject:other,tenantId:'default'}},{currentSubject:{subject:actor,tenantId:'other'}},{bindings:[]},{bindings:[{...original,objectIds:['other']}]},{bindings:[{...original,expiresAt:'2000-01-01T00:00:00.123456788Z'}]},{roles:[{id:'viewer',permissions:['*']}]}])assert.throws(()=>m.verifyBindingRevoke(original,access,{...access,...patch}))
})
test('可信撤销精确匹配before、纳秒、对象顺序重复和权限；拒绝伪投影',()=>{
 assert.equal(m.bindingRevokeDetailMatches(detail,change,access,original),true)
 for(const patch of [{kind:'other'},{operation:'edit'},{operation:'create'},{availability:'stale'},{requestDigest:'sha256:'+'e'.repeat(64)},{state:'pending_approval'},{binding:{...detail.binding,after:before}},{binding:{...detail.binding,before:null}},{binding:{...detail.binding,changedFields:['id']}},{binding:{...detail.binding,before:{...before,bindingApproval:'unknown'}}},{binding:{...detail.binding,before:{...before,expiresAt:'2000-01-01T00:00:00.123456788Z'}}},{binding:{...detail.binding,before:{...before,objectIds:['duplicate','x, y','duplicate']}}},{binding:{...detail.binding,before:{...before,permissions:['*']}}}])assert.equal(m.bindingRevokeDetailMatches({...detail,...patch},change,access,original),false,JSON.stringify(patch))
 for(const patch of [{actorHash:other},{id:'bad'},{requestDigest:''},{state:'unknown'}])assert.equal(m.validBindingRevokeResponse({...change,...patch},actor),false)
})
function apiFor(last,write=async()=>applied) {let reads=0,writes=0,details=0;return{api:{access:async()=>{if(++reads<=2)return view;if(last instanceof Error)throw last;return last},accessChangeDetail:async()=>{details++;return detail},applyAccessChange:async()=>{writes++;return write()}},counts:()=>({reads,writes,details})}}
test('撤销公共应用返回操作类型；角色删除/绑定编辑不混淆；applied终态不重发',async()=>{
 const f=apiFor(after);assert.deepEqual(await apply(f.api,view,change),{bindingID:'ordinary',operation:'revoke'});assert.equal(f.counts().writes,1)
 const terminal=apiFor(after);terminal.api.access=async()=>after;assert.equal(await apply(terminal.api,view,change),undefined);assert.equal(terminal.counts().writes,0);assert.equal(terminal.counts().details,0)
})
test('明确applied后403/管理资格丢失/版本漂移保留已应用事实，目标仍在不能确认',async()=>{
 for(const last of [new Error('403'),{...after,canManage:false,pendingChanges:[]},{...after,version:9},{...after,bindings:[original]},{...after,pendingChanges:[]},{...after,pendingChanges:[{...applied,requestDigest:'sha256:'+'e'.repeat(64)}]}]){const f=apiFor(last);await assert.rejects(()=>apply(f.api,view,change),/已应用.*待核对/);assert.equal(f.counts().writes,1)}
})
test('响应丢失只读恢复；读回403和无状态均结果未知，不重发写入',async()=>{
 const lost=async()=>{throw Error('响应丢失')}, f=apiFor(after,lost);assert.deepEqual(await apply(f.api,view,change),{bindingID:'ordinary',operation:'revoke'});assert.equal(f.counts().writes,1)
 for(const last of [new Error('403'),{...after,canManage:false,pendingChanges:[]}]){const f=apiFor(last,lost);await assert.rejects(()=>apply(f.api,view,change),/结果未知/);assert.equal(f.counts().writes,1)}
})
test('公共apply前身份代次变更阻断写入，错误撤销详情不能绕过',async()=>{
 const f=apiFor(after);let active=true;f.api.accessChangeDetail=async()=>{active=false;return detail};await assert.rejects(()=>apply(f.api,view,change,()=>active),/会话已切换/);assert.equal(f.counts().writes,0)
 const g=apiFor(after);g.api.accessChangeDetail=async()=>({...detail,binding:{...detail.binding,after:before}});await assert.rejects(()=>apply(g.api,view,change));assert.equal(g.counts().writes,0)
})

test('角色删除丢失响应后也必须核对冻结身份和租户',async()=>{
 const f=apiFor({...after,roles:[],currentSubject:{subject:other,tenantId:'other'}},async()=>{throw Error('response lost')})
 f.api.accessChangeDetail=async()=>({...change,reviewerHash:actor,kind:'role_deletion',operation:'delete',availability:'ready',expectedVersion:7,currentVersion:7,roleDeletion:{before:{id:'custom',displayName:'Custom',permissions:['ops.read']},references:{bindings:'none',directPrincipals:'none'}}})
 await assert.rejects(()=>apply(f.api,view,change),/已应用.*待核对/);assert.equal(f.counts().writes,1)
})
