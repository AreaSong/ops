import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const url = process.env.S33B_URL
assert.match(url, /^http:\/\/127\.0\.0\.1:\d+$/)
const output = new URL('../../output/s3.3b/browser/', import.meta.url); await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const contexts = [], errors = [], posts = [], stages = [], details = []
const btn = (p, name) => p.getByRole('button', { name, exact: true })
async function session(actor) {
 const context = await browser.newContext({ viewport: { width:1280,height:900 } });contexts.push(context)
 await context.addCookies([{name:'review_actor',value:actor,url}]); await context.route('**/*',r => new URL(r.request().url()).origin === url ? r.continue() : r.abort())
 const p = await context.newPage(); p.setDefaultTimeout(12000)
 p.on('pageerror',e=>errors.push(e.message));p.on('request',r=>{if(r.method()==='POST')posts.push({actor,path:new URL(r.url()).pathname,body:r.postDataJSON()})})
 await p.goto(url);await btn(p,'访问控制').click();await p.getByTitle('刷新访问策略').waitFor();return p
}
async function access(p) {return p.evaluate(async()=> (await fetch('/api/access')).json())}
async function status(p,path) {return p.evaluate(async path=>(await fetch(path)).status,path)}
async function refresh(p) { await p.getByTitle('刷新访问策略').click();await p.waitForFunction(()=>!document.querySelector('button[title="刷新访问策略"]').disabled) }
async function post(p,path,body) {
 const csrf=(await p.context().cookies()).find(c=>c.name==='areasong_ops_csrf').value
 return p.evaluate(async({path,body,csrf})=>{const r=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json','X-AreaSong-Ops-CSRF':csrf},body:JSON.stringify(body)});const data=await r.json();if(!r.ok)throw Error(JSON.stringify(data));return data},{path,body,csrf})
}
const target = v=>v.bindings.find(b=>b.id==='existing-binding')
const stable = v=>({roles:v.roles,tenants:v.tenants,principals:v.principals,principalList:[...v.principalList].sort((a,b)=>a.subject.localeCompare(b.subject)),bindings:v.bindings.filter(b=>b.id!=='existing-binding')})
async function edit(p) {await refresh(p);await btn(p,'编辑绑定 existing-binding').click();return p.getByRole('dialog')}
async function approve(p,id) {
 await refresh(p);const card=p.locator('article').filter({has:p.getByRole('region',{name:`审阅提案 ${id}`})})
 await btn(card,'读取服务端详情').click();await card.getByText('编辑既有绑定（完整替换） · 当前差异有效').waitFor()
 await card.locator('.runner-update-actions input').fill(await card.locator('.runner-update-actions code').innerText());await btn(card,'独立批准').click()
 await p.getByText('独立批准已完成，需由创建人提交应用。').waitFor()
}
async function cycle(creator,approver,label,mutate,check) {
 const before=await access(creator), dialog=await edit(creator);await mutate(dialog)
 if(label==='role') {
  for(const [name,width,scheme] of [['desktop',1280,'light'],['narrow',375,'light'],['dark-preference',375,'dark']]) {
   await creator.setViewportSize({width,height:900});await creator.emulateMedia({colorScheme:scheme})
   assert.equal(await dialog.evaluate(el=>el.scrollWidth<=el.clientWidth),true)
   assert.equal(await dialog.locator('dd,p,textarea').evaluateAll(els=>els.every(e=>e.scrollWidth<=e.clientWidth)),true)
   await dialog.screenshot({path:new URL(`edit-${name}.png`,output).pathname})
  }
  await btn(dialog,'创建编辑提案').focus();await creator.keyboard.press('Tab');assert.equal(await dialog.getByLabel('绑定角色').evaluate(e=>e===document.activeElement),true)
  await creator.keyboard.press('Shift+Tab');assert.equal(await btn(dialog,'创建编辑提案').evaluate(e=>e===document.activeElement),true)
  await dialog.getByLabel('完整对象数组（JSON）').focus();await creator.keyboard.press('Tab');assert.equal(await btn(dialog,'显式清空对象范围').evaluate(e=>e===document.activeElement),true)
  await dialog.getByLabel('期限操作').scrollIntoViewIfNeeded();await dialog.screenshot({path:new URL('edit-narrow-expiry.png',output).pathname})
  assert.equal(await dialog.evaluate(e=>e.contains(document.activeElement)),true)
 }
 await btn(dialog,'创建编辑提案').click();await dialog.waitFor({state:'hidden'})
 let view=await access(creator);assert.deepEqual(target(view),target(before),'creation must preserve binding');assert.deepEqual(stable(view),stable(before))
 const change=view.pendingChanges.find(c=>c.state==='pending_approval');assert.ok(change)
 const d=await creator.evaluate(async id=>(await fetch(`/api/access/changes/${id}/detail`)).json(),change.id);details.push(d)
 assert.equal(await btn(creator,'独立批准').count(),0,'no self approval')
 await approve(approver,change.id);view=await access(creator);assert.deepEqual(target(view),target(before),'approval must preserve binding')
 await refresh(creator);await btn(creator,'创建人应用').click();await creator.getByText('绑定 existing-binding 编辑已生效，已按实际应用版本读回确认授权字段。',{exact:true}).waitFor()
 view=await access(creator);assert.equal(view.version,before.version+1);assert.deepEqual(stable(view),stable(before));check(target(view),target(before))
 assert.equal(d.kind,'binding');assert.equal(d.operation,'edit');assert.ok(d.binding.before&&d.binding.after)
 stages.push({label,before:target(before),after:target(view),changedFields:d.binding.changedFields,createdUnchanged:true,approvedUnchanged:true,version:view.version})
 return view
}
try {
 const creator=await session('creator'),approver=await session('approver'),subject=await session('viewer')
 assert.equal(await btn(creator,'编辑绑定 unsupported-jit').isDisabled(),true);await creator.getByText('JIT 或绑定自身六审批字段非默认，不支持编辑').waitFor()
 let dialog=await edit(creator),count=posts.length;await btn(dialog,'创建编辑提案').click();await dialog.getByText('没有实际变化，无需创建提案').waitFor();assert.equal(posts.length,count)
 await btn(dialog,'取消').click();await dialog.waitFor({state:'hidden'});assert.equal(posts.length,count);assert.equal(await btn(creator,'编辑绑定 existing-binding').evaluate(e=>e===document.activeElement),true)
 const seed=(await access(creator)).pendingChanges.find(c=>c.state==='applied'), protectedPath=`/api/access/changes/${seed.id}/detail`
 assert.equal(await status(subject,protectedPath),200);assert.equal(await status(subject,'/api/access'),200)
 await cycle(creator,approver,'role',async d=>d.getByLabel('绑定角色').selectOption('viewer'),(after,before)=>{assert.equal(after.roleId,'viewer');assert.deepEqual(after.objectIds,before.objectIds);assert.equal(after.expiresAt,'2030-01-01T00:00:00.123456789Z')})
 const authorization={detail:await status(subject,protectedPath),access:await status(subject,'/api/access')};assert.deepEqual(authorization,{detail:403,access:200})
 await cycle(creator,approver,'narrow',async d=>d.getByLabel('完整对象数组（JSON）').fill('["access"]'),(after,before)=>{assert.deepEqual(after.objectIds,['access']);assert.equal(after.expiresAt,before.expiresAt)})
 assert.equal(await status(subject,'/api/access'),200)
 await refresh(subject);assert.equal(await subject.getByRole('button',{name:/编辑绑定/}).count(),0,'permission denied entry')
 await cycle(creator,approver,'broaden',async d=>{await btn(d,'显式清空对象范围').click();await d.getByText('广泛匹配任意对象，可能包含角色允许的平台资源。').waitFor()},(after,before)=>{assert.deepEqual(after.objectIds??[],[]);assert.equal(after.expiresAt,before.expiresAt)})
 assert.equal(await status(subject,'/api/access'),200)
 await cycle(creator,approver,'clear',async d=>{await d.getByLabel('期限操作').selectOption('clear');await d.getByText('请求期限：不设绑定到期时间',{exact:true}).waitFor()},after=>assert.equal(after.expiresAt,undefined))
 await cycle(creator,approver,'set',async d=>{await d.getByLabel('期限操作').selectOption('set');await d.getByLabel('新期限（RFC3339，含时区偏移）').fill('2031-02-28T08:00:00.987654321+08:00')},after=>assert.equal(after.expiresAt,'2031-02-28T00:00:00.987654321Z'))
 // 冻结版本后用第二条已授权合成变更制造真实漂移；草稿不得偷偷换版本。
 dialog=await edit(creator);await dialog.getByLabel('绑定角色').selectOption('platform-admin')
 const v=await access(creator),b=target(v)
 const drift=await post(creator,'/api/access/changes',{bindings:[{id:b.id,subject:b.subject,tenantId:b.tenantId,roleId:b.roleId,objectIds:['access'],expiresAt:b.expiresAt}],expectedVersion:v.version,requiresDualApproval:true,idempotencyKey:crypto.randomUUID()})
 await post(approver,`/api/access/changes/${drift.id}/approve`,{digest:drift.requestDigest,confirmation:drift.confirmationPhrase});await post(creator,`/api/access/changes/${drift.id}/apply`,{})
 count=posts.length;await btn(dialog,'创建编辑提案').click();await dialog.getByText('策略版本已变化，请关闭并刷新后重新核对').waitFor();assert.equal(posts.length,count)
 await btn(dialog,'取消').click();await btn(dialog,'放弃修改').click();await dialog.waitFor({state:'hidden'})
 assert.deepEqual(errors,[])
 await writeFile(new URL('results.json',output),JSON.stringify({stages,authorization,posts,details,errors,cancelNoWrite:true,versionDriftNoWrite:true,unsupportedDisabled:true},null,2))
 console.log('S3.3b: five real dual Cookie edit cycles; unchanged create/approve; exact readback; same identity detail 200→403/access 200; cancellation, unsupported and version drift passed')
} finally {await Promise.all(contexts.map(c=>c.close()));await browser.close()}
