import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const url = process.env.S33C_URL
assert.match(url, /^http:\/\/127\.0\.0\.1:\d+$/)
const output = new URL('../../output/s3.3c/browser/', import.meta.url); await mkdir(output, { recursive: true })
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
const stable = (v,id)=>({roles:v.roles,tenants:v.tenants,principals:v.principals,principalList:[...v.principalList].sort((a,b)=>a.subject.localeCompare(b.subject)),bindings:v.bindings.filter(b=>b.id!==id)})
async function cycle(creator,approver,id,self=false) {
 const before=await access(creator);await refresh(creator);await btn(creator,`撤销绑定 ${id}`).click();const dialog=creator.getByRole('dialog'), row=before.bindings.find(b=>b.id===id)
 assert.equal(await dialog.locator('dd').allTextContents().then(a=>a.includes(row.subject)),true)
 if(id==='sole-binding') {
  const count=posts.length;await creator.keyboard.press('Enter');assert.equal(posts.length,count)
  for(const [label,width,scheme] of [['desktop',1280,'light'],['narrow',375,'light'],['dark-preference',375,'dark']]) {
   await creator.setViewportSize({width,height:900});await creator.emulateMedia({colorScheme:scheme})
   assert.equal(await dialog.evaluate(e=>e.scrollWidth<=e.clientWidth),true)
   assert.equal(await dialog.locator('dd,p,code').evaluateAll(els=>els.every(e=>e.scrollWidth<=e.clientWidth)),true)
   assert.equal(await btn(dialog,'创建撤销提案').evaluate(e=>{const r=e.getBoundingClientRect();return r.top>=0&&r.bottom<=innerHeight}),true)
   await dialog.screenshot({path:new URL(`revoke-${label}.png`,output).pathname})
  }
  await btn(dialog,'创建撤销提案').focus();await creator.keyboard.press('Tab');assert.equal(await btn(dialog,'取消').evaluate(e=>e===document.activeElement),true)
  await creator.keyboard.press('Shift+Tab');assert.equal(await btn(dialog,'创建撤销提案').evaluate(e=>e===document.activeElement),true)
  await btn(dialog,'取消').click();await dialog.waitFor({state:'hidden'});assert.equal(posts.length,count)
  assert.equal(await btn(creator,`撤销绑定 ${id}`).evaluate(e=>e===document.activeElement),true)
  await btn(creator,`撤销绑定 ${id}`).click()
 }
 await btn(dialog,'创建撤销提案').click();await dialog.waitFor({state:'hidden'})
 let v=await access(creator);assert.deepEqual(v.bindings,before.bindings);assert.deepEqual(stable(v,id),stable(before,id))
 const change=v.pendingChanges.find(c=>c.state==='pending_approval');assert.ok(change)
 const request=posts.filter(p=>p.path==='/api/access/changes').at(-1).body
 assert.deepEqual(Object.keys(request).sort(),['expectedVersion','idempotencyKey','removeBindingIds','requiresDualApproval']);assert.deepEqual(request.removeBindingIds,[id]);assert.equal(request.expectedVersion,before.version)
 assert.equal(await btn(creator,'独立批准').count(),0)
 await refresh(approver);const card=approver.locator('article').filter({has:approver.getByRole('region',{name:`审阅提案 ${change.id}`})})
 await btn(card,'读取服务端详情').click();await card.getByText('撤销单条绑定 · 当前差异有效').waitFor();await card.getByText('绑定将不存在（撤销）').waitFor()
 const d=await approver.evaluate(async id=>(await fetch(`/api/access/changes/${id}/detail`)).json(),change.id);details.push(d)
 assert.equal(d.binding.before.id,id);assert.equal(d.binding.before.subject,row.subject);assert.deepEqual(d.binding.before.objectIds,row.objectIds??[]);assert.equal(d.binding.after,null)
 if(id==='sole-binding') {await approver.setViewportSize({width:375,height:900});await card.screenshot({path:new URL('revoke-review-narrow.png',output).pathname})}
 await card.locator('.runner-update-actions input').fill(await card.locator('.runner-update-actions code').innerText());await btn(card,'独立批准').click();await approver.getByText('独立批准已完成，需由创建人提交应用。').waitFor()
 v=await access(creator);assert.deepEqual(v.bindings,before.bindings,'approval must not revoke')
 await refresh(creator)
 if(id==='other-target') await creator.route(`**/api/access/changes/${change.id}/apply`,async route=>{await route.fetch();await route.abort('failed')},{times:1})
 await btn(creator,'创建人应用').click()
 if(self) await creator.getByText(/提案已应用，当前结果待核对/).waitFor()
 else await creator.getByText(`绑定 ${id} 撤销已核对完成，已按实际应用版本确认目标不存在。其他授权仍可能有效。`,{exact:true}).waitFor()
 v=await access(approver);assert.equal(v.version,before.version+1);assert.equal(v.bindings.some(b=>b.id===id),false);assert.deepEqual(stable(v,id),stable(before,id))
 await refresh(approver)
 const terminal=approver.locator('article').filter({has:approver.getByRole('region',{name:`审阅提案 ${change.id}`})})
 const writesBeforeRead=posts.length
 await btn(terminal,'读取服务端详情').click();await terminal.getByText('该绑定提案已应用；历史差异可能已失效，请只读核对生效策略。不要重新应用或重建提案。').waitFor()
 assert.equal(await terminal.getByRole('alert').count(),0);assert.equal(posts.length,writesBeforeRead)
 stages.push({id,createdUnchanged:true,approvedUnchanged:true,appliedVersion:v.version,readbackActor:self?'approver':'creator',selfRevoked:self,lostApplyRecovered:id==='other-target'})
 return change
}
try {
 const creator=await session('creator'),approver=await session('approver'),subject=await session('viewer'),other=await session('other')
 assert.equal(await btn(creator,'撤销绑定 unsupported-jit').isDisabled(),true)
 const seed=(await access(creator)).pendingChanges.find(c=>c.state==='applied'), path=`/api/access/changes/${seed.id}/detail`
 const authorization={soleBefore:await status(subject,path),independentBefore:await status(other,path)}
 assert.equal(authorization.soleBefore,200);assert.equal(authorization.independentBefore,200)
 await cycle(creator,approver,'sole-binding');authorization.soleAfter=await status(subject,path);assert.equal(authorization.soleAfter,403)
 await cycle(creator,approver,'other-target');authorization.independentAfter=await status(other,path);assert.equal(authorization.independentAfter,200)
 await cycle(creator,approver,'expired-target');assert.ok((await access(approver)).bindings.some(b=>b.id==='expired-keep'))
 await cycle(creator,approver,'self-binding',true);authorization.creatorAfter=await status(creator,path);assert.equal(authorization.creatorAfter,403)
 assert.deepEqual(errors,[])
 await writeFile(new URL('results.json',output),JSON.stringify({stages,authorization,posts,details,errors},null,2))
 console.log('S3.3c: four real dual Cookie revoke cycles; unchanged create/approve; exact absence; same Cookie sole 200→403/independent 200→200; expired/self revocation passed')
} finally {await Promise.all(contexts.map(c=>c.close()));await browser.close()}
