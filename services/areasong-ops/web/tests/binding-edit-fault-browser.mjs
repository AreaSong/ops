import assert from 'node:assert/strict'
import { writeFile } from 'node:fs/promises'
const { chromium }=await import(process.env.PLAYWRIGHT_MODULE||'playwright')
const browser=await chromium.launch({headless:true,executablePath:process.env.PLAYWRIGHT_EXECUTABLE})
const p=await browser.newPage();p.setDefaultTimeout(5000)
const btn=name=>p.getByRole('button',{name,exact:true}), frame=()=>p.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))))
await p.route('**/*',r=>new URL(r.request().url()).origin!=='http://127.0.0.1:4173'?r.abort():new URL(r.request().url()).pathname==='/edit-harness'?r.fulfill({contentType:'text/html',body:'<div id="root"></div>'}):r.continue())
const passed=[]
try {
 await p.goto('http://127.0.0.1:4173/edit-harness')
 await p.evaluate(async()=>{
  const {default:RefreshRuntime}=await import('/@react-refresh');RefreshRuntime.injectIntoGlobalHook(window);window.$RefreshReg$=()=>{};window.$RefreshSig$=()=>t=>t;window.__vite_plugin_react_preamble_installed__=true
  const {default:React}=await import('/node_modules/.vite/deps/react.js'),{default:ReactDOM}=await import('/node_modules/.vite/deps/react-dom_client.js'),{AccessControl}=await import('/src/views/AccessControl.tsx')
  const root=ReactDOM.createRoot(document.getElementById('root')),actor='a'.repeat(64)
  const original={id:'ordinary',subject:'c'.repeat(64),tenantId:'default',roleId:'viewer',objectIds:['x, y','x, y'],expiresAt:'2030-01-01T00:00:00.123456789Z',createdBy:actor,createdAt:'2026-10-03T00:00:00Z'}
  const h=window.h={actor,tenant:'default',mode:'ready',writes:[],version:7,roles:[{id:'viewer',displayName:'观察者',permissions:['ops.read'],builtIn:true},{id:'platform-admin',displayName:'管理员',permissions:['*'],builtIn:true}],original}
  h.view=()=>({canManage:true,enforced:true,version:h.version,currentSubject:{subject:h.actor,tenantId:h.tenant},bindings:[original],roles:h.roles,tenants:[{id:'default',status:'active'}]})
  const value=b=>({id:b.id,subject:b.subject,tenantId:b.tenantId,roleId:b.roleId,permissions:h.roles.find(r=>r.id===b.roleId).permissions,objectIds:b.objectIds??[],expiresAt:b.expiresAt??null,jit:false,bindingApproval:'default'})
  h.render=()=>root.render(React.createElement(AccessControl,{access:h.view(),loading:false,available:true,error:'',busy:'',onRefresh(){},
   onVerifyActor:async()=>{const v=structuredClone(h.view());if(h.mode==='verify-delay')await new Promise(r=>h.release=r);if(h.mode==='missing')v.bindings=[];if(h.mode==='version')v.version++;if(h.mode==='denied')v.canManage=false;return v},
   onCreateChange:async body=>{h.writes.push(body);h.body=body;const c={id:'00000000-0000-4000-8000-000000000001',requestDigest:'sha256:'+'d'.repeat(64),state:h.mode==='rejected'?'rejected':'pending_approval',actorHash:h.mode==='wrong-creator'?'b'.repeat(64):actor,requiresDualApproval:true,approvalPolicy:'two_party_v1'};if(h.mode==='create-delay')await new Promise(r=>h.release=r);if(h.mode==='fail')throw Error('真实失败保留草稿');h.change=c;return c},
   onReadDetail:async()=>{const c=h.change,before=value(original),after=value(h.body.bindings[0]);return{...c,reviewerHash:actor,kind:'binding',operation:h.mode==='wrong-operation'?'create':'edit',availability:'ready',expectedVersion:7,currentVersion:7,binding:{before,after,changedFields:Object.keys(before).filter(k=>JSON.stringify(before[k])!==JSON.stringify(after[k]))}}},
  }));h.render();h.unmount=()=>root.render(null)
 })
 const open=async()=>{await btn('编辑绑定 ordinary').click();await p.getByRole('dialog').waitFor()}, change=async()=>p.getByLabel('绑定角色').selectOption('platform-admin')
 const close=async()=>{await btn('取消').click();if(await btn('放弃修改').count())await btn('放弃修改').click();await p.getByRole('dialog').waitFor({state:'hidden'})}
 await open();await btn('创建编辑提案').click();await p.getByText('没有实际变化，无需创建提案').waitFor();assert.equal(await p.evaluate(()=>h.writes.length),0);await close();passed.push('no-change/cancel')
 for(const mode of ['missing','version','denied']) {
  await p.evaluate(mode=>h.mode=mode,mode);await open();await change();await btn('创建编辑提案').click();await p.getByRole('alert').waitFor();assert.equal(await p.evaluate(()=>h.writes.length),0);await close()
 }passed.push('missing/version/denied')
 await p.evaluate(()=>h.mode='fail');await open();await change();await btn('创建编辑提案').click();await p.getByText('真实失败保留草稿').waitFor();assert.equal(await p.getByLabel('绑定角色').inputValue(),'platform-admin');const key=await p.evaluate(()=>h.writes.at(-1).idempotencyKey)
 await btn('创建编辑提案').click();await p.getByText('真实失败保留草稿').waitFor();assert.equal(await p.evaluate(()=>h.writes.at(-1).idempotencyKey),key)
 await close();await open();await change();await btn('创建编辑提案').click();await p.getByText('真实失败保留草稿').waitFor();assert.equal(await p.evaluate(()=>h.writes.at(-1).idempotencyKey),key)
 await p.evaluate(()=>h.mode='rejected');await btn('创建编辑提案').click();await p.getByText(/原编辑提案已撤销/).waitFor();await p.evaluate(()=>h.mode='ready');await btn('创建编辑提案').click();await p.getByRole('dialog').waitFor({state:'hidden'});assert.notEqual(await p.evaluate(()=>h.writes.at(-1).idempotencyKey),key);passed.push('failure/retry/reopen/rejected-rebuild')
 await p.evaluate(()=>h.mode='wrong-creator');await open();await change();await btn('创建编辑提案').click();await p.getByText(/创建身份与冻结身份不一致/).waitFor();await close();passed.push('wrong-creator-blocked')
 await p.evaluate(()=>h.mode='wrong-operation');await open();await change();await btn('创建编辑提案').click();await p.getByText(/服务端未确认本次固定身份的编辑差异/).waitFor();await close();passed.push('non-edit-blocked')
 for(const mode of ['verify-delay','create-delay'])for(const axis of ['actor','tenant','roundtrip','unmount']) {
  await p.evaluate(mode=>{h.mode=mode;h.actor='a'.repeat(64);h.tenant='default';h.release=null;h.render()},mode);await frame();await open();await change();const count=await p.evaluate(()=>h.writes.length)
  await btn('创建编辑提案').click();await p.waitForFunction(()=>typeof h.release==='function');await btn('提交中').dispatchEvent('click');assert.equal(await p.evaluate(()=>h.writes.length),count+(mode==='create-delay'?1:0))
  await p.evaluate(axis=>{if(axis==='unmount')h.unmount();else{if(axis==='tenant')h.tenant='other';else h.actor='b'.repeat(64);h.render()}},axis);await frame()
  if(axis==='roundtrip'){await p.evaluate(()=>{h.actor='a'.repeat(64);h.render()});await frame()}
  await p.evaluate(()=>h.release());await frame();assert.equal(await p.getByRole('dialog').count(),0);assert.equal(await p.getByText(/编辑提案 00000000/).count(),0)
 }passed.push('duplicate-click/actor/tenant/roundtrip/unmount/late-result')
 await writeFile(new URL('../../output/s3.3b/components.json',import.meta.url),JSON.stringify(passed,null,2));console.log(passed)
}finally{await browser.close()}
