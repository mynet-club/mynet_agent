import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import assert from 'node:assert/strict'
const html=readFileSync(new URL('../../mynetd/localapi/webui/index.html',import.meta.url),'utf8')
const apiCode=html.slice(html.indexOf('async function api('),html.indexOf('/* U6'))
for(const scenario of ['success','http','network','timeout','invalid-json']){
 let cleared=0
 const sandbox={AbortController,setTimeout:()=>42,clearTimeout:()=>cleared++,fetch:async()=>{
  if(scenario==='network')throw new TypeError('offline')
  if(scenario==='timeout'){const e=new Error();e.name='AbortError';throw e}
  return {ok:scenario!=='http',status:502,json:async()=>{if(scenario==='invalid-json')throw new SyntaxError('html response');return {success:true,message:'backend failure'}}}
 }}
 vm.runInNewContext(apiCode,sandbox)
 const result=await sandbox.api('/test')
 assert.equal(cleared,1,'request deadline must be released on every exit')
 assert.equal(result.success,scenario==='success')
 if(scenario==='timeout')assert.equal(result.code,'request_timeout')
 if(scenario==='invalid-json')assert.equal(result.code,'unexpected_response')
}
const busyCode=html.slice(html.indexOf('async function busy('),html.indexOf('function confirmDlg'))
const sandbox={};vm.runInNewContext(busyCode,sandbox)
const button={disabled:false,dataset:{},innerHTML:'接入',setAttribute(){},removeAttribute(){}}
let release,started=0
const pending=sandbox.busy(button,()=>{started++;return new Promise(resolve=>release=resolve)})
await sandbox.busy(button,()=>{started++})
assert.equal(started,1,'duplicate click started two operations')
release();await pending
assert.equal(button.disabled,false);assert.equal(button.innerHTML,'接入')
await assert.rejects(sandbox.busy(button,()=>{throw new Error('sync failure')}))
assert.equal(button.disabled,false,'sync failure left button stuck')
console.log('WebUI tools: HTTP failures, deadlines, duplicate clicks and sync failure passed')
// A failed status refresh must not claim that a running device is stopped.
const loadSessionCode=html.slice(html.indexOf('async function loadSession('),html.indexOf('let statusReadOk'))
const sessionSandbox={session:{logged_in:true,node_id:'42'},api:async()=>({success:false}),window:{}}
vm.runInNewContext(loadSessionCode,sessionSandbox)
await sessionSandbox.loadSession()
assert.equal(sessionSandbox.session.node_id,'42')
assert.equal(sessionSandbox.session.logged_in,true)
console.log('Last known device identity survives a temporary API outage')
