import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import assert from 'node:assert/strict'
const html = readFileSync(new URL('../../mynetd/localapi/webui/index.html', import.meta.url), 'utf8')
const code = html.slice(html.indexOf('function renderConfigFlow('), html.indexOf('// flowPullApply：'))
for (const failure of [null, 'connect']) {
  const elements = new Map()
  const calls = [], messages = []
  const $ = id => {
    if (!elements.has(id)) elements.set(id, { innerHTML: '', textContent: '', setAttribute() {}, classList: { toggle() {}, add() {}, remove() {} } })
    return elements.get(id)
  }
  const sandbox = { $, session: { logged_in: true, node_id: '42' }, modeInfo:{mode:'online'}, status: { state: 'IDLE' }, keySectionOpen: false, connectBusy: false, connectResult: null,
    window: { __mynetDriver: 'wireguard' }, flowStep: () => '', esc: s => s,
    busy: (_, f) => f(), toast: (...args) => messages.push(args), logAction: () => {}, goPage: () => {}, refresh: async () => {}, loadSettings: async () => {},
    api: async (path, method, body) => { calls.push({ path, body }); return { success: !path.endsWith(failure || '!'), message: 'retryable failure' } }
  }
  vm.runInNewContext(code, sandbox)
  sandbox.renderConfigFlow(false, false)
  await $('flow-connect').onclick({ currentTarget: {} })
  assert.equal(calls[0].path, '/api/v1/config/connect')
  assert.equal(calls.length, 1)
  if (failure) assert.equal(messages.at(-1)[2], 'err')
  else assert.equal(messages.at(-1)[2], 'ok')
  assert(!calls.some(c => c.body?.private_hex || c.body?.generate), 'normal flow must preserve identity in executor')
}
console.log('Device flow: success and failure feedback passed')
const bridge = html.slice(html.indexOf('let deviceBridgeBusy = false;'), html.indexOf('/* ── 闸门过后的流程台阶'))
let handler, bridgeCalls = 0
const frameWindow = {}
vm.runInNewContext(bridge, {
  window: { addEventListener: (_, fn) => { handler = fn } },
  location: { origin: 'http://127.0.0.1:8096' },
  $: () => ({ contentWindow: frameWindow }),
  api: async () => { bridgeCalls++; return { success: false, message: 'offline' } }, toast: () => {}
})
await handler({ origin: 'https://foreign.example', source: frameWindow, data: { type: 'mynet:connect-device', nodeId: '42' } })
await handler({ origin: 'http://127.0.0.1:8096', source: {}, data: { type: 'mynet:connect-device', nodeId: '42' } })
await handler({ origin: 'http://127.0.0.1:8096', source: frameWindow, data: { type: 'mynet:connect-device', nodeId: '../admin' } })
assert.equal(bridgeCalls, 0, 'bridge must reject foreign frames and invalid identifiers')
await handler({ origin: 'http://127.0.0.1:8096', source: frameWindow, data: { type: 'mynet:connect-device', nodeId: '42' } })
assert.equal(bridgeCalls, 1)
console.log('Embedded connect bridge: origin, source and identifier checks passed')
const selection = html.slice(html.indexOf('async function useGateNode('), html.indexOf('// Accept explicit connect actions'))
for (const failSelection of [false, true]) {
 const elements = new Map(), requests = [], frame = {}
 const $ = id => {
  if (!elements.has(id)) elements.set(id, { contentWindow: frame, innerHTML: '', setAttribute() {}, classList: { toggle() {}, add() {}, remove() {} } })
  return elements.get(id)
 }
 let receive
 const sandbox = { $, session: { logged_in: true, tenant_id: 't1', node_id: '' }, cloudNodesCache: null, modeInfo:{mode:'online'}, status: { state: 'IDLE' }, keySectionOpen: false, connectBusy: false, connectResult: null,
  location: { origin: 'http://127.0.0.1:8096' }, document: { createElement: () => ({}) },
  window: { __mynetDriver: 'wireguard', addEventListener: (_, fn) => { receive = fn } },
  busy: (_, fn) => fn(), logAction: () => {}, toast: () => {}, goPage: () => {}, flowStep: () => '', esc: s => s,
  loadSession: async () => { sandbox.session.node_id = '42' },
  loadSettings: async () => sandbox.renderConfigFlow(false, false), refresh: async () => {},
  api: async (path, method, body) => {
   requests.push({ path, body })
   if (path.endsWith('/cloud')) return { success: true, data: { nodes: [{ node_id: 42, tenant_id: 't1', vpn_type: ['wireguard'] }] } }
   return { success: !(failSelection && path.endsWith('/active')), message: 'selection failed' }
  }
 }
 vm.runInNewContext(selection + bridge + code, sandbox)
 await receive({ origin: sandbox.location.origin, source: frame, data: { type: 'mynet:connect-device', nodeId: '42' } })
 assert.equal(requests[1].body.vpn_type, 'wireguard')
 assert.equal(requests.length, failSelection ? 2 : 3)
 if (!failSelection) assert.equal(requests.at(-1).path, '/api/v1/config/connect')
}
console.log('Created device -> select WireGuard -> connect: success and selection failure passed')
const localElements=new Map()
const localSandbox={
 $:id=>{if(!localElements.has(id))localElements.set(id,{innerHTML:'',classList:{toggle(){},add(){},remove(){}},setAttribute(){}});return localElements.get(id)},
 modeInfo:{initialized:true,mode:'local'},session:{logged_in:false,node_id:'42'},status:{state:'STOPPED'},window:{},keySectionOpen:false,
 connectBusy:false,connectResult:null,flowStep:(_,title)=>title,busy:(_,fn)=>fn(),api:async()=>{throw new Error('local render called cloud')}
}
vm.runInNewContext(code,localSandbox);localSandbox.renderConfigFlow(false,false)
assert.equal(localElements.get('flow-body').innerHTML,'自主管理网络')
console.log('Self-managed mode remains accessible without cloud login')

const contextSource=readFileSync(new URL('../../mynetd/localapi/webui/webui-context.js',import.meta.url),'utf8')
const contextSwitch=contextSource.slice(contextSource.indexOf('  async function switchOnline('),contextSource.indexOf('  async function loadSessions('))
for(const selected of [false,true]){
 let result,connected=0,passedNode
 const sandbox={confirmDlg:(_,__,___,fn)=>{result=fn()},document:{createElement:()=>({})},$:()=>({remove(){}}),goPage(){},toast(){},
 window:{useGateNode:async(_,__,___,node)=>{passedNode=node;return selected},connectLocalDevice:async()=>{connected++}}}
 vm.runInNewContext(contextSwitch,sandbox)
 const node={node_id:42,vpn_type:'wireguard'}
 await sandbox.switchOnline('t1','42',node);await result
 assert.equal(passedNode,node)
 assert.equal(connected,selected?1:0)
}
console.log('Cloud context selection shares connect flow and stops after selection failure')
