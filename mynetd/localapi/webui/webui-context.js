/* 管理模式切换器：网络配置模式与运行设备焦点分开处理。 */
(function () {
  const style = document.createElement("style");
  style.textContent = ".context-modal{max-width:660px}.context-tabs{display:flex;gap:6px;border-bottom:1px solid var(--line);margin:10px 0 16px}.context-tabs button{border:0;background:transparent;color:var(--mut);border-radius:8px 8px 0 0}.context-tabs button.on{color:var(--brand);background:var(--soft)}.context-current{padding:12px 14px;background:var(--soft);border:1px solid var(--line);border-radius:12px;color:var(--mut);font-size:12px;margin-bottom:8px}.context-item{width:100%;display:flex;align-items:center;justify-content:space-between;text-align:left;margin:6px 0;padding:13px 14px}.context-item small{display:block;color:var(--mut);font-size:11px;margin-top:2px}.context-item.selected{border-color:var(--brand);color:var(--brand)}.context-empty{padding:28px 12px;color:var(--mut);text-align:center}.choice-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.context-choice{text-align:left;padding:18px;min-height:112px;background:var(--soft)}.context-choice b{font-size:15px}.context-choice small{display:block;color:var(--mut);font-size:12px;margin-top:7px;line-height:1.5}.term-tip{display:inline-grid;place-items:center;width:18px;height:18px;margin-left:5px;border:1px solid var(--brand);border-radius:50%;background:transparent;color:var(--brand);font:11px var(--mono);cursor:pointer;vertical-align:1px}.term-popover{position:fixed;z-index:120;width:min(310px,calc(100vw - 36px));padding:12px 14px;background:var(--card);border:1px solid var(--brand);border-radius:12px;box-shadow:0 14px 34px #15231d33;color:var(--ink);font-size:12px;line-height:1.55}.term-popover b{display:block;color:var(--brand);margin-bottom:3px}.term-popover button{float:right;border:0;background:transparent;color:var(--mut);padding:0;font-size:16px}@media(max-width:520px){.choice-grid{grid-template-columns:1fr}}body{background:var(--bg);color:var(--ink)}";
  document.head.appendChild(style);
  const light = document.createElement('style');
  light.textContent = ':root{--bg:#f5f8f7;--side:#ffffff;--card:#ffffff;--card2:#f7faf9;--soft:#eef6f2;--line:#d8e5df;--ink:#173129;--mut:#61766d;--brand:#16805d;--warn:#b77908;--bad:#c94b4b}body{background:linear-gradient(135deg,#f7fbf9 0%,#eef6f2 100%)}.shell{display:block}.side{display:none}.top{width:min(940px,calc(100% - 68px));margin:0 auto;padding-left:0;padding-right:0;background:#ffffffdd;border-bottom-color:var(--line)}.content{max-width:940px;padding-top:26px}.head{margin-bottom:18px}.hero,.card,.stat{background:#fff !important;color:var(--ink);box-shadow:0 8px 24px #315b4a0d}.hero{align-items:flex-start;padding:24px}.hero h2{font-size:23px}.hero-summary{display:grid;gap:5px;margin-top:10px;color:var(--mut)}.hero-summary b{color:var(--ink)}.stats{display:none}.grid2{display:block}.overview-tabs{margin-top:14px}.overview-tabbar{display:flex;gap:4px;padding:5px;background:var(--soft);border-radius:12px;margin-bottom:10px}.overview-tabbar button{flex:1;border:0;background:transparent;color:var(--mut);padding:10px;border-radius:9px}.overview-tabbar button.on{background:#fff;color:var(--brand);box-shadow:0 2px 8px #315b4a14}.overview-panel{display:none}.overview-panel.on{display:block}.overview-panel .card{margin:0}.nav button:hover,.nav button.on,.subnav button.on{background:#e4f3ed;color:#126b4e}.primary{box-shadow:0 4px 12px #16805d2b}.caution{background:#fff8e8;color:#9a6400;border-color:#e8c982}.danger-action{background:#fff1ef;color:#b83d38;border-color:#e8aaa6}.modalbg{background:#17312955;backdrop-filter:blur(5px)}.modal{background:#ffffff;border-color:#cfe1d8;box-shadow:0 20px 60px #1731292a}input,select{background:#fff;color:var(--ink);border:1px solid var(--line);border-radius:10px;padding:10px}.workflow{margin:16px 0;padding:15px;background:var(--soft);border-radius:12px}.workflow b{display:block}.workflow small{display:block;color:var(--mut);margin-top:4px}.apply-steps{display:grid;gap:8px;margin:14px 0}.apply-step{padding:10px 12px;border-radius:10px;background:var(--soft)}.apply-step.done{color:var(--brand)}.apply-step.failed{color:var(--bad)}pre{background:#f4f8f6}.toast{background:#fff;border-color:#cfe1d8;box-shadow:0 8px 20px #17312918}.check small,.sub,.fact span{color:var(--mut)}@media(max-width:850px){.top{width:calc(100% - 36px)}.content{padding-top:20px}}';
  document.head.appendChild(light);
  const tips = {
    "节点 ID": "节点是网络中的独立身份。节点 ID 用来区分不同的网络身份。",
    "网络驱动": "网络驱动负责建立加密连接。MyNet 支持 GNB 和 WireGuard。",
    "虚拟地址": "设备在这张网络中的内部地址，与设备所在地点的实际地址无关。",
    "对端": "网络中的另一个节点。当前设备会通过直连或中继与对端通信。",
    "隧道接口": "系统中承载加密网络流量的虚拟网络接口。",
    "路由": "决定某段流量应通过哪个设备或隧道发送。",
    "密钥": "用于识别设备并建立加密连接的一对密钥。私钥只保存在设备上。",
    "模式": "决定网络配置由谁负责：MyNet 云端模式，或自主管理模式。"
  };
  function tipFor(label, text) {
    if (!label || label.querySelector?.('.term-tip')) return;
    const b = document.createElement('button'); b.className = 'term-tip'; b.type = 'button'; b.textContent = '?'; b.setAttribute('aria-label', label.textContent.trim() + '说明');
    b.onclick = (e) => { e.stopPropagation(); document.querySelectorAll('.term-popover').forEach(x => x.remove()); const p = document.createElement('div'); p.className = 'term-popover'; p.innerHTML = '<button aria-label="关闭">×</button><b>' + esc(label.textContent.trim()) + '</b>' + esc(text); document.body.appendChild(p); const r = b.getBoundingClientRect(); p.style.left = Math.max(18, Math.min(r.left, innerWidth - p.offsetWidth - 18)) + 'px'; p.style.top = Math.min(r.bottom + 8, innerHeight - p.offsetHeight - 18) + 'px'; p.querySelector('button').onclick = () => p.remove(); };
    label.appendChild(b);
  }
  document.addEventListener('click', (e) => { if (!e.target.closest('.term-tip,.term-popover')) document.querySelectorAll('.term-popover').forEach(x => x.remove()); });
  function relabel() {
    const exact = (selector, text) => { const e = document.querySelector(selector); if (e) e.textContent = text; };
    exact('.label', '我的网络'); exact('[data-main="overview"]', '◉　我的设备'); if (!document.querySelector('#crumb')?.dataset.homeReady) exact('#crumb', 'MyNet  >  概览'); exact('.eyebrow', 'MyNet device');
    const crumb = document.querySelector('#crumb'); if (crumb && !crumb.dataset.homeReady) { crumb.dataset.homeReady = '1'; crumb.textContent = 'MyNet  >  概览'; crumb.style.cursor = 'pointer'; crumb.onclick = () => go('overview'); }
    const heroP = document.querySelector('#view-overview .head p'); if (heroP) heroP.textContent = '查看这台设备在网络中的状态、连接和运行身份。';
    const statusDetail = document.querySelector('#status-detail'); if (statusDetail && statusDetail.textContent.includes('本机实时探测')) statusDetail.textContent = 'MyNet 服务正在实时检查连接状态。';
    exact('#view-settings .eyebrow', 'Device settings'); exact('#view-settings h1', '设备配置');
    exact('#view-connections h1', '路由'); exact('#view-connections .eyebrow', 'Routes');
    exact('#view-connections .head p', '查看这台设备可到达的网络、路由和对端设备。');
    exact('#view-console h1', '网络空间'); exact('#view-console .eyebrow', 'Network space');
    exact('#view-console .head p', '在当前设备上打开 MyNet 网络空间，可在新窗口继续使用。');
    exact('#view-settings .head p', '管理这台设备的身份、密钥、运行配置和转发出口。');
    ['#space-name','#fact-space'].forEach(sel => { const e = document.querySelector(sel); if (e && /^MyNet\s*·\s*u_/.test(e.textContent.trim())) e.textContent = 'MyNet 云端网络'; });
    exact('#btn-new-ws', '添加设备配置'); exact('#btn-key-restore', '导入密钥');
    const grid = document.querySelector('#view-overview .grid2');
    if (grid && !grid.dataset.tabsReady) {
      const cards = Array.from(grid.children); if (cards.length >= 2) {
        grid.className = 'overview-tabs'; grid.dataset.tabsReady = '1';
        const bar = document.createElement('div'); bar.className = 'overview-tabbar';
        const panels = document.createElement('div');
        const names = [['节点状态','当前设备的网络身份与连接状态'],['系统状态','MyNet 服务、驱动、路由和防火墙状态']];
        let details = document.querySelector('#view-overview > details');
        cards.forEach((card, i) => { const panel = document.createElement('div'); panel.className = 'overview-panel' + (i === 0 ? ' on' : ''); if (i === 0) { const h = card.querySelector('h3'); if (h) h.textContent = '节点状态'; const facts = card.querySelector('.facts'); if (facts && !facts.querySelector('[data-extra]')) { facts.insertAdjacentHTML('beforeend','<div class="fact" data-extra><span>隧道接口</span><b class="mono" id="tab-tun">—</b></div><div class="fact" data-extra><span>网络流量</span><b id="tab-traffic">—</b></div>'); } } else { const h = card.querySelector('h3'); if (h) h.textContent = '系统状态'; } panel.appendChild(card); panels.appendChild(panel); const b = document.createElement('button'); b.textContent = names[i][0]; b.title = names[i][1]; b.className = i === 0 ? 'on' : ''; b.onclick = () => { bar.querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b)); panels.querySelectorAll('.overview-panel').forEach(x => x.classList.toggle('on', x === panel)); }; bar.appendChild(b); });
        grid.append(bar, panels);
      }
    }
    const overviewHead = document.querySelector('#view-overview > .head'); if (overviewHead) overviewHead.style.display = 'none';
    const tabbar = document.querySelector('#view-overview .overview-tabbar');
    if (tabbar && !tabbar.dataset.iaReady) {
      tabbar.dataset.iaReady = '1';
      const current = Array.from(tabbar.querySelectorAll('button')); if (current[0]) current[0].textContent = '节点状态'; if (current[1]) current[1].textContent = '系统状态';
      const settingsTab = document.createElement('button'); settingsTab.textContent = '网络设置'; settingsTab.title = '管理模式、身份与密钥、运行配置和转发出口'; settingsTab.onclick = () => go('settings'); tabbar.appendChild(settingsTab);
      const add = (text, view) => { const b = document.createElement('button'); b.textContent = text; b.onclick = () => go(view); tabbar.appendChild(b); };
      add('路由', 'connections'); add('诊断与日志', 'logs'); add('网络空间', 'console');
      let logs = document.getElementById('view-logs'); if (!logs) { logs = document.createElement('section'); logs.className = 'view'; logs.id = 'view-logs'; logs.innerHTML = '<div class="head"><div><div class="eyebrow">Diagnostics</div><h1>诊断与日志</h1><p>检查运行状态，并查看诊断和操作记录。</p></div><div class="actions"><button data-go="overview">返回概览</button></div></div><div class="card" id="diagnostic-home"><h3>运行诊断</h3><p class="note">驱动、运行配置、网络接口、路由和防火墙状态会在这里集中显示。</p></div><div class="card" style="margin-top:14px"><h3>操作日志</h3><p class="note">具体命令输出会在对应操作完成后显示。</p><pre id="log-status">等待操作记录</pre></div>'; document.querySelector('.content').appendChild(logs); }
    }
    exact('#daemon', document.querySelector('#daemon-dot')?.classList.contains('ok') ? 'MyNet 服务已连接' : 'MyNet 服务不可用');
    document.querySelectorAll('#view-overview .actions button, #view-overview .hero .actions button').forEach(e => {
      if (e.textContent.includes('节点设置')) e.textContent = '设备设置';
      if (e.textContent.includes('启动节点')) e.textContent = '启动网络';
      if (e.textContent.includes('配置节点')) e.textContent = '配置网络';
    });
    document.querySelectorAll('#view-overview .fact span').forEach(e => { if (e.textContent.trim() === '运行空间') e.firstChild.textContent = '当前网络'; });
    const runtimeNote = document.querySelector('#pane-runtime .note'); if (runtimeNote) runtimeNote.textContent = 'MyNet 云端会自动同步网络配置；我来管理模式由你维护网络配置。';
    const pluginNote = document.querySelector('#pane-plugins .note'); if (pluginNote) pluginNote.textContent = '把当前设备作为流量入口，并选择可用的出口设备。';
    const keyNote = document.querySelector('#pane-keys .note'); if (keyNote) keyNote.textContent = '密钥用于识别设备并建立加密连接，只保存在这台设备上。更换密钥会使现有连接暂时中断。';
    document.querySelectorAll('#view-settings button').forEach(e => { if (e.textContent.trim() === '从服务端更新') e.textContent = '从 MyNet 云端更新'; });
    document.querySelectorAll('#view-overview .fact span, #view-overview .stat small, #view-connections th').forEach(e => { const t = e.textContent.trim(); if (tips[t]) tipFor(e, tips[t]); });
    const mode = document.querySelector('#mode'); if (mode && mode.textContent === '在线配置') mode.textContent = 'MyNet 云端模式'; if (mode && mode.textContent === '本地配置') mode.textContent = '自主管理模式';
    const local = document.querySelector('#wiz-local'); if (local) { const b = local.querySelector('b'), s = local.querySelector('small'); if (b) b.textContent = '自主管理模式'; if (s) s.textContent = '自己维护网络配置和启动方式'; }
    const online = document.querySelector('#wiz-online'); if (online) { const b = online.querySelector('b'), s = online.querySelector('small'); if (b) b.textContent = 'MyNet 云端'; if (s) s.textContent = '加入组织，选择网络设备'; }
  }
  relabel();
  let relabelQueued = false;
  new MutationObserver(() => { if (relabelQueued) return; relabelQueued = true; setTimeout(() => { relabelQueued = false; relabel(); }, 0); }).observe(document.body, {subtree:true, childList:true, characterData:true});
  const $ = (id) => document.getElementById(id);
  const esc = (v) => String(v == null ? "—" : v).replace(/[&<>"']/g, (c) => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]));
  const api = async (path, method, body) => {
    try {
      const r = await fetch(path, {method: method || "GET", headers: {"Content-Type": "application/json", "Origin": location.origin}, body: body ? JSON.stringify(body) : undefined});
      return await r.json();
    } catch (e) { return {success:false, message:e.message}; }
  };
  const toast = (title, message, kind) => {
    if (typeof window.toast === "function") return window.toast(title, message || "", kind || "");
    const root = $("toasts"); if (!root) return;
    const e = document.createElement("div"); e.className = "toast " + (kind || ""); e.textContent = title + (message ? "：" + message : ""); root.appendChild(e); setTimeout(() => e.remove(), 3500);
  };
  const confirm = (title, message, ok, fn) => {
    const back = document.createElement("div"); back.className = "modalbg";
    back.innerHTML = `<div class="modal"><h3>${esc(title)}</h3><p class="note">${esc(message)}</p><div class="actions"><button data-cancel>取消</button><button class="danger" data-ok>${esc(ok || "继续")}</button></div></div>`;
    document.body.appendChild(back); back.querySelector("[data-cancel]").onclick = () => back.remove();
    back.querySelector("[data-ok]").onclick = async () => { back.remove(); await fn(); };
  };
  function addContextButton() {
    const space = document.querySelector(".space"); if (!space || $("context-open")) return;
    const b = document.createElement("button"); b.id = "context-open"; b.className = "quiet"; b.textContent = "切换运行节点"; b.onclick = open;
    space.insertBefore(b, space.firstChild);
  }
  function open() {
    let root = $("context-modal");
    if (!root) { root = document.createElement("div"); root.id = "context-modal"; root.className = "modalbg"; document.body.appendChild(root); }
    root.innerHTML = `<div class="modal context-modal"><div class="cardhead"><div><div class="eyebrow">Management mode</div><h3 style="margin:4px 0 0">选择管理模式</h3></div><button class="quiet" data-close>关闭</button></div><p class="note">模式决定网络配置由谁负责。两种模式只能选择一种，与设备所在位置无关。</p><div class="context-tabs"><button class="on" data-tab="source">网络与模式</button><button data-tab="focus">运行设备</button></div><div data-panel="source"><div class="choice-grid"><button class="context-choice" data-source="local"><b>自主管理模式</b><small>你自己维护网络配置，适合实验室、家庭或特殊网络。</small></button><button class="context-choice" data-source="online"><b>MyNet 云端模式</b><small>加入组织并选择网络，配置自动同步，适合日常使用。</small></button></div><div id="context-sources" class="context-list">读取中…</div></div><div data-panel="focus" class="hidden"><div id="context-sessions">读取运行设备…</div></div></div>`;
    root.querySelector("[data-close]").onclick = () => root.remove();
    root.querySelectorAll("[data-tab]").forEach((b) => b.onclick = () => { root.querySelectorAll("[data-tab]").forEach(x => x.classList.toggle("on", x === b)); root.querySelectorAll("[data-panel]").forEach(x => x.classList.toggle("hidden", x.dataset.panel !== b.dataset.tab)); if (b.dataset.tab === "focus") loadSessions(); });
    root.querySelectorAll("[data-source]").forEach((b) => b.onclick = () => chooseSource(b.dataset.source));
    loadSources();
  }
  async function loadSources() {
    const box = $("context-sources"); if (!box) return;
    const [mode, cloud] = await Promise.all([api("/api/v1/config/mode"), api("/api/v1/config/cloud")]);
    const active = mode.data && mode.data.active || {};
    if (!cloud.success && (cloud.need_login || String(cloud.message).includes('need_login'))) {
      box.innerHTML = '<div class="context-empty"><b>登录后查看云端网络</b><p>登录信息只交给本机 MyNet 服务保存。</p><button class="primary" data-login>登录 MyNet</button></div>';
      box.querySelector('[data-login]').onclick = () => { $("context-modal")?.remove(); openLogin(); };
      return;
    }
    const list = cloud.data && Array.isArray(cloud.data.nodes) ? cloud.data.nodes : [];
    box.innerHTML = `<div class="context-current">当前：${esc(mode.data && mode.data.mode === "online" ? "MyNet 云端模式" : "自主管理模式")}</div>` + list.map(n => { const tenant = n.tenant_id || n.tenant?.id || ''; const name = n.node_name || n.name || '未命名设备'; const org = n.tenant_name || n.tenant?.name || '云端网络'; return `<button class="context-item" data-tenant="${esc(tenant)}" data-node="${esc(n.node_id || n.id)}"><span><b>${esc(name)}</b><small>${esc(org)} · ${esc(Array.isArray(n.vpn_type) ? n.vpn_type.join('/') : n.vpn_type || '网络驱动未标注')}</small></span><span>使用 →</span></button>`; }).join("") + (list.length ? '' : '<div class="context-empty">账号下暂时没有可用设备。</div>');
    box.querySelectorAll("[data-tenant]").forEach((b) => b.onclick = () => switchOnline(b.dataset.tenant, b.dataset.node));
  }
  async function chooseSource(source) {
    if (source === "local") {
      confirm("切换到自主管理模式？", "网络配置将改由你维护，并重新应用自建网络设置。当前连接可能暂时中断。", "切换模式", async () => {
        const r = await api("/api/v1/config/mode", "POST", {mode:"local"});
        toast(r.success ? "已切换到自主管理模式" : "切换失败", r.message || "", r.success ? "ok" : "err");
        if (r.success) { $("context-modal")?.remove(); window.refresh?.(); }
      });
    }
  }
  async function switchOnline(tenant, node) {
    confirm("切换联网节点？", `将停止当前节点，改用 ${tenant} / ${node || "默认节点"} 的配置并重新应用。`, "切换并应用", async () => {
      const r = await api("/api/v1/config/active", "POST", {tenant, node});
      toast(r.success ? "已切换联网节点" : "切换失败", r.message || "", r.success ? "ok" : "err");
      if (r.success) { $("context-modal")?.remove(); window.refresh?.(); }
    });
  }
  async function loadSessions() {
    const box = $("context-sessions"); if (!box) return;
    const r = await api("/api/v1/nodes"); const d = r.data || {}; const nodes = Array.isArray(d.nodes) ? d.nodes : [];
    box.innerHTML = nodes.length ? nodes.map(n => `<button class="context-item ${n.focused ? "selected" : ""}" data-node="${esc(n.node_id)}"><span><b>${esc(n.node_id)}</b><small>${esc(n.driver || "驱动未识别")} · ${n.active ? "运行中" : "已停止"}${n.focused ? " · 当前焦点" : ""}</small></span><span>${n.focused ? "当前" : "切换 →"}</span></button>`).join("") : `<div class="context-empty">暂无并行运行节点。启动一个已配置节点后，它会出现在这里。</div>`;
    box.querySelectorAll("[data-node]").forEach((b) => b.onclick = () => focusNode(b.dataset.node));
  }
  async function focusNode(node) {
    confirm("切换运行设备？", `页面将聚焦设备 ${node}。其它并行运行的设备不会被停止。`, "切换设备", async () => {
      const r = await api("/api/v1/nodes/switch", "POST", {node_id:node});
      toast(r.success ? "运行焦点已切换" : "切换失败", r.message || "", r.success ? "ok" : "err");
      if (r.success) { $("context-modal")?.remove(); window.refresh?.(); }
    });
  }
  addContextButton();
  document.querySelectorAll('[data-main="console"]').forEach((b) => b.addEventListener('click', () => { const f = document.getElementById('console-frame'); if (f && !f.getAttribute('src')) f.src = '/gui/'; }));
  async function hydrateIdentity() {
    const r = await api('/api/v1/config/identity'); const d = r.data || {};
    const label = d.mode === 'online' ? (d.tenant_name || d.node_name || 'MyNet 云端网络') : '自主管理网络';
    window.__mynetDriver = d.driver === 'wireguard' ? 'wireguard' : 'gnb';
    const name = document.getElementById('space-name'); if (name && r.success) name.textContent = label;
    const fs = document.getElementById('fact-space'); if (fs && r.success) fs.textContent = label;
    const driver = document.getElementById('driver'); if (driver && r.success && d.driver) driver.textContent = d.driver;
  }
  hydrateIdentity();
  window.action = function (id) { const driver = window.__mynetDriver || 'gnb'; const svc = ['start','stop','restart'].includes(id) ? ['service', id, driver] : null; return ({install:['安装驱动',['install']],configure:['配置网络',null],start:['启动网络',svc],restart:['重新启动',svc],stop:['停止网络',svc],'fix-routes':['修复路由',['apply','--auto']],retry:['重试启动',['service','start',driver]],uninstall:['卸载驱动',['uninstall']]}[id] || ['刷新',null]); };
  const oldRefresh = window.refresh;
  if (typeof oldRefresh === 'function') window.refresh = async function () { const out = await oldRefresh.apply(this, arguments); await hydrateIdentity(); return out; };

  function openLogin() {
    $('wizard')?.classList.remove('hidden');
    document.querySelectorAll('.wiz').forEach(x => x.classList.toggle('on', x.dataset.wiz === '2'));
    if ($('wiz-api') && !$('wiz-api').value) $('wiz-api').value = 'https://api.mynet.club';
  }
  function humanizeOverview() {
    const hero = $('hero'); if (!hero) return;
    const ok = hero.classList.contains('ok'), warn = hero.classList.contains('warn');
    const title = ok ? '网络连接正常' : warn ? '网络连接需要处理' : '网络尚未连接';
    const backendDetail = $('status-detail').textContent;
    const detail = ok ? '这台设备可以访问当前网络中的其它设备。' : warn && backendDetail.includes('这些目标') ? backendDetail : 'MyNet 会保留诊断信息，帮助你恢复连接。';
    if ($('status-title').textContent !== title) $('status-title').textContent = title;
    if ($('status-detail').textContent !== detail) $('status-detail').textContent = detail;
    let summary = hero.querySelector('.hero-summary');
    if (!summary) { summary = document.createElement('div'); summary.className = 'hero-summary'; $('status-detail').after(summary); }
    const summaryHTML = `<span>网络地址　<b>${esc($('overlay')?.textContent)}</b></span><span>连接情况　<b>${esc($('peers')?.textContent)} 台可用</b></span><span>已运行　　<b>${esc($('uptime')?.textContent)}</b></span>`;
    if (summary.innerHTML !== summaryHTML) summary.innerHTML = summaryHTML;
    if ($('tab-tun')) $('tab-tun').textContent = $('tun')?.textContent || '—';
    if ($('tab-traffic')) $('tab-traffic').textContent = $('traffic')?.textContent || '—';
    const p = $('primary'), q = $('secondary');
    if (p && /重启/.test(p.textContent)) { p.textContent = '重新连接'; if (p.className !== 'caution') p.className = 'caution'; }
    if (q && /停止/.test(q.textContent) && q.className !== 'danger-action') q.className = 'danger-action';
  }
  const overviewWatch = new MutationObserver(() => humanizeOverview());
  if ($('hero')) overviewWatch.observe($('hero'), {subtree:true, childList:true, characterData:true});
  humanizeOverview();
  const originalGo = window.go;
  if (typeof originalGo === 'function') window.go = function (view) { const labels = {overview:'概览', settings:'设备配置', connections:'路由', logs:'诊断与日志', console:'网络空间'}; const out = originalGo(view); const crumb = $('crumb'); if (crumb) crumb.textContent = 'MyNet  >  ' + (labels[view] || '概览'); return out; };

  const originalAct = window.act;
  if (typeof originalAct === 'function') window.act = function(id, button) {
    if (id === 'restart') return confirm('重新连接网络？', '连接会短暂中断，正在使用这台设备的会话可能需要重新连接。', '重新连接', () => originalAct(id, button));
    if (id === 'stop') return confirm('停止网络连接？', '停止后，这台设备将无法访问当前 MyNet 网络，直到你再次启动。', '停止连接', () => originalAct(id, button));
    return originalAct(id, button);
  };

  const keysPane = $('pane-keys');
  if (keysPane) { const box = document.createElement('div'); box.className = 'workflow'; box.innerHTML = '<b>设备身份是一组配对密钥</b><small>生成新密钥会同时替换本机身份；在云端模式下，随后只上传公钥并重新发布配置。私钥不会离开这台设备。</small>'; keysPane.querySelector('.note')?.after(box); }
  const keyGen = document.getElementById('btn-key-gen');
  if (keyGen) { keyGen.textContent = '生成并同步新密钥'; keyGen.onclick = () => confirm('生成并同步新密钥？', '这会替换当前设备身份并短暂中断连接。云端模式会上传新公钥并重新发布配置。', '生成并同步', async () => { const made = await api('/api/v1/config/keys', 'POST', {generate:true}); if (!made.success) return toast('生成失败', made.message || '', 'err'); const synced = await api('/api/v1/command', 'POST', {argv:['config','keys','sync','--json']}); toast(synced.success ? '新密钥已生成并同步' : '密钥已生成，但同步失败', synced.message || synced.stderr || '', synced.success ? 'ok' : 'err'); window.refresh?.(); }); }
  const keyUpload = document.getElementById('btn-key-upload');
  if (keyUpload) { keyUpload.textContent = '仅同步现有公钥'; keyUpload.onclick = () => confirm('同步现有公钥？', '不会生成新密钥，只上传当前公钥并重新发布云端配置。', '同步公钥', async () => { const r = await api('/api/v1/command', 'POST', {argv:['config','keys','sync','--json']}); toast(r.success ? '公钥已同步' : '同步失败', r.message || r.stderr || '', r.success ? 'ok' : 'err'); }); }
  const plugForm = $('plug-form');
  if (plugForm && !$('plug-peer')) { $('plug-id')?.remove(); const select = document.createElement('select'); select.id = 'plug-peer'; select.innerHTML = '<option value="">选择一个对端设备</option>'; const label = document.createElement('label'); label.textContent = '出口设备'; const field = document.createElement('div'); field.className = 'field'; field.append(label, select); plugForm.insertBefore(field, plugForm.firstChild); }
  const plugAdd = $('btn-plug-add'); if (plugAdd) plugAdd.textContent = '选择出口设备';
  const plugToggle = $('btn-plug-toggle'); if (plugToggle) plugToggle.textContent = '读取代理状态';
  async function refreshProxyUI() {
    const [pr, ns] = await Promise.all([api('/api/v1/plugins'), api('/api/v1/node-status')]); const item = Array.isArray(pr.data) ? pr.data.find(x => x.name === 'proxy') : null; const enabled = !!item?.enabled;
    const support = item?.installed === true;
    const plugStatus = $('plug-status'); if (plugStatus) plugStatus.innerHTML = `<span class="badge ${support ? 'ok' : 'warn'}">${support ? '支持代理' : '未安装代理支持'}</span> <span class="badge ${enabled ? 'ok' : ''}">${enabled ? '已启用' : '未启用'}</span><small class="note" style="display:block;margin-top:8px">支持代理表示这台设备具备能力；只有启用并选择出口设备后，才会实际转发流量。</small>`;
    if (plugToggle) { plugToggle.textContent = enabled ? '停用代理' : '启用代理'; plugToggle.className = enabled ? 'danger-action' : 'primary'; plugToggle.disabled = !support; }
    const select = $('plug-peer'); const peers = ns.data?.peers || []; if (select && !select.dataset.ready) { select.innerHTML = '<option value="">选择一个对端设备</option>' + peers.filter(x => !x.is_self).map(x => `<option value="${esc(x.node_id)}">${esc(x.node_name || x.node_id)} · ${esc(x.overlay || '')}</option>`).join(''); select.dataset.ready = '1'; }
    if (plugToggle) plugToggle.onclick = () => confirm(enabled ? '停用代理？' : '启用代理？', enabled ? '代理将停止作为其它设备的流量出口。' : '代理会作为流量入口，已选择的对端设备才会参与转发。', enabled ? '停用代理' : '启用代理', async () => { const r = await api('/api/v1/command','POST',{argv:['plugin',enabled?'disable':'enable','proxy']}); toast(r.success?'代理状态已更新':'操作失败',r.message||r.stderr||'',r.success?'ok':'err'); settings(); });
  }
  refreshProxyUI();
  const plugSave = $('btn-plug-save'); if (plugSave) plugSave.onclick = e => busy(e.currentTarget, async () => { const id = $('plug-peer')?.value; if (!id) return toast('请选择出口设备', '出口必须来自当前对端设备列表。', 'warn'); const r = await api('/api/v1/command','POST',{argv:['plugin','exits','add',id,'--weight',$('plug-weight').value||'10','--order',$('plug-order').value||'1']}); toast(r.success?'出口设备已加入':'添加失败',r.message||r.stderr||'',r.success?'ok':'err'); settings(); });
  const pull = document.getElementById('btn-rt-pull');
  if (pull) pull.onclick = () => confirm('从 MyNet 云端更新配置？', '将下载当前网络的最新设备、路由和连接信息。下载完成后还需要“应用配置”。', '更新配置', async () => { const state = await api('/api/v1/wizard/state'); if (!state.data?.logged_in) return openLogin(); const r = await api('/api/v1/command', 'POST', {argv:['config','pull','--json']}); let out = {}; try { out = JSON.parse(r.stdout || '{}'); } catch (_) {} if (out.need_login) return openLogin(); toast(r.success ? '云端配置已更新' : '更新失败', r.message || r.stderr || out.message || '', r.success ? 'ok' : 'err'); window.settings?.(); });

  const apply = $('btn-rt-apply');
  if (apply) { apply.textContent = '应用配置'; apply.onclick = () => confirm('应用新的网络配置？', '将生成运行配置、重新连接网络，并更新系统路由和防火墙。连接会短暂中断。', '开始应用', async () => { const back = document.createElement('div'); back.className = 'modalbg'; back.innerHTML = '<div class="modal"><h3>正在应用配置</h3><p class="note">请保持页面打开。</p><div class="apply-steps"><div class="apply-step">○ 生成运行配置</div><div class="apply-step">○ 重新连接网络</div><div class="apply-step">○ 应用系统路由和防火墙</div></div></div>'; document.body.appendChild(back); const r = await api('/api/v1/config/apply', 'POST', {start:true}); const steps = Array.isArray(r.steps) ? r.steps : []; back.querySelector('.apply-steps').innerHTML = steps.map(s => `<div class="apply-step ${esc(s.state)}">${s.state === 'done' ? '✓' : s.state === 'failed' ? '×' : '○'} ${esc(s.name)}</div>`).join('') || `<div class="apply-step ${r.success?'done':'failed'}">${r.success?'✓ 配置已应用':'× '+esc(r.message||'应用失败')}</div>`; back.querySelector('.note').textContent = r.success ? '配置已经生效。' : (r.message || '应用失败'); const close = document.createElement('button'); close.textContent = '关闭'; close.className = r.success ? 'primary' : 'danger-action'; close.onclick = () => { back.remove(); window.refresh?.(); }; back.querySelector('.modal').appendChild(close); }); }
  const backup = document.createElement('button'); backup.id = 'btn-key-backup'; backup.textContent = '从备份恢复'; backup.className = 'quiet';
  const keyRestore = document.getElementById('btn-key-restore'); if (keyRestore && keyRestore.parentElement) { keyRestore.parentElement.appendChild(backup); backup.onclick = () => confirm('从备份恢复？', '将读取保存的网络密钥与配置，可能覆盖当前设置。', '恢复备份', async () => { const r = await api('/api/v1/command', 'POST', {argv:['config','import']}); toast(r.success ? '备份已恢复' : '恢复失败', r.message || r.stderr || '', r.success ? 'ok' : 'err'); window.refresh?.(); }); }
  const wizGen = document.getElementById('wiz-key-gen'), wizMake = document.getElementById('wiz-rt-make');
  if (wizGen) wizGen.disabled = true; if (wizMake) wizMake.disabled = true;
  const wizLogin = document.getElementById('wiz-login'); if (wizLogin) wizLogin.onclick = e => busy(e.currentTarget, async () => { const r = await api('/api/v1/wizard/login', 'POST', {api_base_url:$('wiz-api').value.trim(), email:$('wiz-email').value.trim(), password:$('wiz-pass').value}); $('wiz-pass').value = ''; toast(r.success ? '登录成功' : '登录失败', r.message || '', r.success ? 'ok' : 'err'); if (r.success) { $('wizard').classList.add('hidden'); if (wizGen) wizGen.disabled = false; if (wizMake) wizMake.disabled = false; open(); } });
})();
