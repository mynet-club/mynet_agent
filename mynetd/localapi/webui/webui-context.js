/* MyNet WebUI 旁路模块：术语 tip + 管理模式/运行设备切换。
 * 布局与页面结构由 index.html 原生维护，此处不再改写 DOM 结构。 */
(function () {
  const $ = (id) => document.getElementById(id);
  const esc = (v) => String(v == null ? '—' : v).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));

  async function api(path, method, body) {
    try {
      const r = await fetch(path, {
        method: method || 'GET',
        headers: { 'Content-Type': 'application/json', 'Origin': location.origin },
        body: body ? JSON.stringify(body) : undefined
      });
      return await r.json();
    } catch (e) {
      return { success: false, message: e.message };
    }
  }

  function toast(title, message, kind) {
    if (typeof window.toast === 'function') return window.toast(title, message || '', kind || '');
    const root = $('toasts');
    if (!root) return;
    const e = document.createElement('div');
    e.className = 'toast ' + (kind || '');
    e.textContent = title + (message ? '：' + message : '');
    root.appendChild(e);
    setTimeout(() => e.remove(), 3500);
  }

  function confirmDlg(title, message, okLabel, fn) {
    if (typeof window.confirmDlg === 'function') return window.confirmDlg(title, message, okLabel, fn);
    const back = document.createElement('div');
    back.className = 'modalbg';
    back.innerHTML =
      '<div class="modal"><h3>' + esc(title) + '</h3>' +
      '<p class="note">' + esc(message) + '</p>' +
      '<div class="actions"><button class="btn" data-cancel>取消</button>' +
      '<button class="btn danger" data-ok>' + esc(okLabel || '继续') + '</button></div></div>';
    document.body.appendChild(back);
    back.querySelector('[data-cancel]').onclick = () => back.remove();
    back.querySelector('[data-ok]').onclick = async () => { back.remove(); await fn(); };
  }

  /* ── 术语 tip ── */
  const tips = {
    '节点 ID': '节点是网络中的独立身份。节点 ID 用来区分不同的网络身份。',
    '网络驱动': '网络驱动负责建立加密连接。MyNet 支持 GNB 和 WireGuard。',
    '网络地址': '设备在这张网络中的内部地址，与设备所在地点的实际地址无关。',
    '在线设备': '当前网络中已建立路由、可互相访问的设备数量。',
    '隧道接口': '系统中承载加密网络流量的虚拟网络接口。',
    '网络接口': '系统中承载加密网络流量的虚拟网络接口。',
    '路由': '决定某段流量应通过哪个设备或隧道发送。',
    '密钥': '用于识别设备并建立加密连接的一对密钥。私钥只保存在设备上。',
    '管理模式': '决定网络配置由谁负责：MyNet 云端模式，或自主管理模式。'
  };

  function tipFor(label, text) {
    if (!label || label.querySelector?.('.term-tip')) return;
    const b = document.createElement('button');
    b.className = 'term-tip';
    b.type = 'button';
    b.textContent = '?';
    b.setAttribute('aria-label', label.textContent.trim() + '说明');
    b.onclick = (e) => {
      e.stopPropagation();
      document.querySelectorAll('.term-popover').forEach((x) => x.remove());
      const p = document.createElement('div');
      p.className = 'term-popover';
      p.innerHTML =
        '<button aria-label="关闭">×</button><b>' + esc(label.textContent.trim()) + '</b>' + esc(text);
      document.body.appendChild(p);
      const r = b.getBoundingClientRect();
      p.style.left = Math.max(18, Math.min(r.left, innerWidth - p.offsetWidth - 18)) + 'px';
      p.style.top = Math.min(r.bottom + 8, innerHeight - p.offsetHeight - 18) + 'px';
      p.querySelector('button').onclick = () => p.remove();
    };
    label.appendChild(b);
  }

  document.addEventListener('click', (e) => {
    if (!e.target.closest('.term-tip,.term-popover')) {
      document.querySelectorAll('.term-popover').forEach((x) => x.remove());
    }
  });

  function attachTips() {
    document.querySelectorAll('.facts .f small, .kv-table .k').forEach((el) => {
      const t = el.textContent.trim();
      if (tips[t]) tipFor(el, tips[t]);
    });
  }

  /* ── 样式：tip / 切换模态 ── */
  const style = document.createElement('style');
  style.textContent = [
    '.term-tip{display:inline-grid;place-items:center;width:16px;height:16px;margin-left:5px;',
    'border:1px solid var(--brand,#16805d);border-radius:50%;background:transparent;color:var(--brand,#16805d);',
    'font:10px/1 var(--mono,monospace);cursor:pointer;vertical-align:1px;padding:0}',
    '.term-popover{position:fixed;z-index:120;width:min(310px,calc(100vw - 36px));padding:12px 14px;',
    'background:var(--card,#fff);border:1px solid var(--brand,#16805d);border-radius:12px;',
    'box-shadow:0 14px 34px #15231d33;color:var(--ink,#152a23);font-size:12px;line-height:1.55}',
    '.term-popover b{display:block;color:var(--brand,#16805d);margin-bottom:3px}',
    '.term-popover button{float:right;border:0;background:transparent;color:var(--mut,#61766d);padding:0;font-size:16px;cursor:pointer}',
    '.context-modal{max-width:660px}',
    '.context-tabs{display:flex;gap:6px;border-bottom:1px solid var(--line,#d7e4de);margin:10px 0 16px}',
    '.context-tabs button{border:0;background:transparent;color:var(--mut,#61766d);border-radius:8px 8px 0 0;padding:8px 12px;cursor:pointer}',
    '.context-tabs button.on{color:var(--brand,#16805d);background:var(--soft,#eef5f2);font-weight:650}',
    '.context-current{padding:12px 14px;background:var(--soft,#eef5f2);border:1px solid var(--line,#d7e4de);',
    'border-radius:12px;color:var(--mut,#61766d);font-size:12px;margin-bottom:8px}',
    '.context-item{width:100%;display:flex;align-items:center;justify-content:space-between;text-align:left;',
    'margin:6px 0;padding:13px 14px;border:1px solid var(--line,#d7e4de);background:var(--card,#fff);',
    'border-radius:12px;cursor:pointer;color:var(--ink,#152a23);font:inherit}',
    '.context-item small{display:block;color:var(--mut,#61766d);font-size:11px;margin-top:2px}',
    '.context-item.selected{border-color:var(--brand,#16805d);color:var(--brand,#16805d)}',
    '.context-item:hover{background:var(--soft,#eef5f2)}',
    '.context-empty{padding:28px 12px;color:var(--mut,#61766d);text-align:center}',
    '.choice-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}',
    '.context-choice{text-align:left;padding:18px;min-height:112px;background:var(--soft,#eef5f2);',
    'border:1px solid var(--line,#d7e4de);border-radius:12px;cursor:pointer;color:var(--ink,#152a23);font:inherit}',
    '.context-choice b{font-size:15px}',
    '.context-choice small{display:block;color:var(--mut,#61766d);font-size:12px;margin-top:7px;line-height:1.5}',
    '.context-choice:hover{border-color:var(--brand,#16805d)}',
    '@media(max-width:520px){.choice-grid{grid-template-columns:1fr}}'
  ].join('');
  document.head.appendChild(style);

  /* ── 管理模式 / 运行设备切换 ── */
  function openContextModal() {
    let root = $('context-modal');
    if (!root) {
      root = document.createElement('div');
      root.id = 'context-modal';
      root.className = 'modalbg';
      document.body.appendChild(root);
    }
    root.innerHTML =
      '<div class="modal context-modal">' +
      '<div class="cardhead"><div><h3 style="margin:0">选择管理模式</h3></div>' +
      '<button class="btn ghost" data-close>关闭</button></div>' +
      '<p class="note">模式决定网络配置由谁负责。两种模式只能选择一种，与设备所在位置无关。</p>' +
      '<div class="context-tabs">' +
      '<button class="on" data-tab="source">网络与模式</button>' +
      '<button data-tab="focus">运行设备</button></div>' +
      '<div data-panel="source">' +
      '<div class="choice-grid">' +
      '<button class="context-choice" data-source="local"><b>自主管理模式</b>' +
      '<small>你自己维护网络配置，适合实验室、家庭或特殊网络。</small></button>' +
      '<button class="context-choice" data-source="online"><b>MyNet 云端模式</b>' +
      '<small>加入组织并选择网络，配置自动同步，适合日常使用。</small></button>' +
      '</div><div id="context-sources" class="context-list">读取中…</div></div>' +
      '<div data-panel="focus" class="hidden"><div id="context-sessions">读取运行设备…</div></div></div>';

    root.querySelector('[data-close]').onclick = () => root.remove();
    root.querySelectorAll('[data-tab]').forEach((b) => {
      b.onclick = () => {
        root.querySelectorAll('[data-tab]').forEach((x) => x.classList.toggle('on', x === b));
        root.querySelectorAll('[data-panel]').forEach((x) => {
          x.classList.toggle('hidden', x.dataset.panel !== b.dataset.tab);
        });
        if (b.dataset.tab === 'focus') loadSessions();
      };
    });
    root.querySelectorAll('[data-source]').forEach((b) => {
      b.onclick = () => chooseSource(b.dataset.source);
    });
    loadSources();
  }
  window.openContextModal = openContextModal;

  async function loadSources() {
    const box = $('context-sources');
    if (!box) return;
    const [mode, cloud] = await Promise.all([
      api('/api/v1/config/mode'),
      api('/api/v1/config/cloud')
    ]);
    if (!cloud.success && (cloud.need_login || String(cloud.message || '').includes('need_login'))) {
      box.innerHTML =
        '<div class="context-empty"><b>登录后查看云端网络</b>' +
        '<p>登录信息只交给本机 MyNet 服务保存。</p>' +
        '<button class="btn primary" data-login>登录 MyNet</button></div>';
      box.querySelector('[data-login]').onclick = () => {
        $('context-modal')?.remove();
        if (typeof window.openLoginModal === 'function') window.openLoginModal();
        else if (typeof window.openWizard === 'function') window.openWizard();
      };
      return;
    }
    if (!cloud.success) {
      box.innerHTML = '<div class="context-empty"><b>设备列表暂不可用</b><p>' + esc(cloud.message || '请稍后重试') + '</p><button class="btn" data-retry>重新加载</button></div>';
      box.querySelector('[data-retry]').onclick = loadSources;
      return;
    }
    const list = cloud.data && Array.isArray(cloud.data.nodes) ? cloud.data.nodes : [];
    const active = (mode.data && mode.data.active) || {};
    box.innerHTML =
      '<div class="context-current">当前：' +
      esc(mode.data && mode.data.mode === 'online' ? 'MyNet 云端模式' : '自主管理模式') + '</div>' +
      list.map((n) => {
        const tenant = n.tenant_id || (n.tenant && n.tenant.id) || '';
        const name = n.node_name || n.name || '未命名设备';
        const org = n.tenant_name || (n.tenant && n.tenant.name) || '云端网络';
        return (
          '<button class="context-item" data-tenant="' + esc(tenant) + '" data-node="' + esc(n.node_id || n.id) + '">' +
          '<span><b>' + esc(name) + '</b><small>' + esc(org) + ' · ' +
          esc(Array.isArray(n.vpn_type) ? n.vpn_type.join('/') : n.vpn_type || '网络驱动未标注') +
          '</small></span><span>使用 →</span></button>'
        );
      }).join('') +
      (list.length ? '' : '<div class="context-empty">账号下暂时没有可用设备。</div>');
    box.querySelectorAll('[data-tenant]').forEach((b) => {
      b.onclick = () => switchOnline(b.dataset.tenant, b.dataset.node, list.find(n => String(n.node_id || n.id) === b.dataset.node));
    });
    void active;
  }

  async function chooseSource(source) {
    if (source === 'online') {
      if (!window.session?.logged_in) { $('context-modal')?.remove(); window.openLoginModal?.(); return; }
      await loadSources();
      const box = $('context-sources');
      if (box) { box.scrollIntoView({block:'nearest'}); box.querySelector('button')?.focus(); }
      return;
    }
    if (source === 'local') {
      confirmDlg(
        '切换到自主管理模式？',
        '网络配置将改由你维护，并重新应用自建网络设置。当前连接可能暂时中断。',
        '切换模式',
        async () => {
          const r = await api('/api/v1/config/mode', 'POST', { mode: 'local' });
          toast(r.success ? '已切换到自主管理模式' : '切换失败', r.message || '', r.success ? 'ok' : 'err');
          if (r.success) {
            $('context-modal')?.remove();
            window.refresh?.();
          }
        }
      );
    }
  }

  async function switchOnline(tenant, node, selectedNode) {
    confirmDlg(
      '接入此设备？',
      '选择 ' + (selectedNode?.node_name || node || '设备') + ' 作为本机身份，并自动准备配置与启动连接。已有密钥会保留。',
      '接入本机',
      async () => {
        if (typeof window.useGateNode !== 'function') { toast('面板暂不可用', '请刷新后重试', 'err'); return; }
        const selected = await window.useGateNode(tenant, node, document.createElement('button'), selectedNode);
        if (selected) {
          $('context-modal')?.remove();
          goPage('config');
          await window.connectLocalDevice();
        }
      }
    );
  }

  async function loadSessions() {
    const box = $('context-sessions');
    if (!box) return;
    const r = await api('/api/v1/nodes');
    const d = r.data || {};
    const nodes = Array.isArray(d.nodes) ? d.nodes : [];
    box.innerHTML = nodes.length
      ? nodes.map((n) =>
          '<button class="context-item ' + (n.focused ? 'selected' : '') + '" data-node="' + esc(n.node_id) + '">' +
          '<span><b>' + esc(n.node_id) + '</b><small>' + esc(n.driver || '驱动未识别') + ' · ' +
          (n.active ? '运行中' : '已停止') + (n.focused ? ' · 当前焦点' : '') +
          '</small></span><span>' + (n.focused ? '当前' : '切换 →') + '</span></button>'
        ).join('')
      : '<div class="context-empty">暂无并行运行节点。启动一个已配置节点后，它会出现在这里。</div>';
    box.querySelectorAll('[data-node]').forEach((b) => {
      b.onclick = () => focusNode(b.dataset.node);
    });
  }

  async function focusNode(node) {
    confirmDlg(
      '切换运行设备？',
      '页面将聚焦设备 ' + node + '。其它并行运行的设备不会被停止。',
      '切换设备',
      async () => {
        const r = await api('/api/v1/nodes/switch', 'POST', { node_id: node });
        toast(r.success ? '运行焦点已切换' : '切换失败', r.message || '', r.success ? 'ok' : 'err');
        if (r.success) {
          $('context-modal')?.remove();
          window.refresh?.();
        }
      }
    );
  }

  attachTips();
  new MutationObserver(() => attachTips()).observe(document.body, {
    subtree: true,
    childList: true
  });
})();
