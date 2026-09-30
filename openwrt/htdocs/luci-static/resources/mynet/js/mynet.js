/**
 * mynet.js — MyNet LuCI 前端脚本
 * 处理 GNB 控制按钮、状态轮询、Toast 通知等交互。
 */

/* ─── 暗色模式：主检测在各页面 <head> 内联脚本（检测 Argon <style> 中 #1e1e1e） ─── */

/* ─── 基础 Fetch 封装 ─────────────────────────────────────── */

/** _t — i18n 翻译查找（window._mnI18n 由页面模板注入） */
function _t(key) {
    return (window._mnI18n && window._mnI18n[key]) || key;
}

/**
 * mnFetch — 向 LuCI AJAX 端点发送请求
 * @param {string}   url      - 完整 URL
 * @param {string}   method   - GET | POST
 * @param {object}   params   - 请求参数（POST: body；GET: 追加到 URL）
 * @param {function} onOk     - 成功回调 (data)
 * @param {function} onErr    - 失败回调 (errorMessage)
 */
function mnFetch(url, method, params, onOk, onErr) {
    var opts = { method: method, headers: {} };

    if (method === 'POST') {
        opts.headers['Content-Type'] = 'application/x-www-form-urlencoded';
        if (params) {
            opts.body = Object.keys(params)
                .map(function (k) {
                    return encodeURIComponent(k) + '=' + encodeURIComponent(params[k]);
                }).join('&');
        }
    } else if (params) {
        var qs = Object.keys(params)
            .map(function (k) {
                return encodeURIComponent(k) + '=' + encodeURIComponent(params[k]);
            }).join('&');
        url += (url.indexOf('?') >= 0 ? '&' : '?') + qs;
    }

    fetch(url, opts)
        .then(function (res) { return res.json(); })
        .then(function (data) {
            if (onOk) onOk(data);
        })
        .catch(function (err) {
            if (onErr) onErr(err.message || String(err));
        });
}

/* ─── 基础工具 ────────────────────────────────────────────── */

/** 简单 HTML 转义 */
function _esc(s) {
    if (!s) return '';
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

/* ─── Toast 通知 ─────────────────────────────────────────── */

var _toastTimer = null;

/**
 * mnShowToast — 显示底部弹窗通知
 * @param {string} msg   - 消息内容
 * @param {string} type  - '' (默认/success) 或 'error'
 * @param {object} opts  - { duration: ms, detail: string }
 */
function mnShowToast(msg, type, opts) {
    var el = document.getElementById('mn-toast');
    if (!el) return;
    opts = opts || {};

    var html = '<div class="mn-toast-msg">' + _esc(msg) + '</div>';
    if (opts.detail) {
        html += '<div class="mn-toast-detail">' + _esc(opts.detail).substring(0, 200) + '</div>';
    }
    el.innerHTML = html;

    if (type === 'error') {
        el.className = 'mn-toast mn-toast-error';
    } else if (type === 'success') {
        el.className = 'mn-toast mn-toast-success';
    } else {
        el.className = 'mn-toast';
    }

    if (_toastTimer) clearTimeout(_toastTimer);
    var duration = opts.duration || (type === 'error' ? 8000 : 3500);
    _toastTimer = setTimeout(function () {
        el.classList.add('mn-hidden');
    }, duration);
}

/* ─── GNB 控制 ───────────────────────────────────────────── */

/**
 * _mnApiInflight — 请求去重：同一 action 进行中则拒绝重复调用
 * _mnApiLastCall — 防抖：同一 action 两次调用间隔 < 1s 则忽略
 */
var _mnApiInflight = {};
var _mnApiLastCall = {};
var _MN_API_DEBOUNCE_MS = 1000;

/**
 * mnApi — 通用 API 操作（start / stop / restart / fw_setup 等）
 * 内置防抖 + 去重 + 按钮加载状态 + 错误引导 + 成功刷新
 * @param {string} action  - API action 名
 * @param {object} params  - 附加参数（可 null）
 * @param {HTMLElement} btn - 触发按钮（用于禁用/恢复 + 加载动画）
 */
function mnApi(action, params, btn) {
    var now = Date.now();
    /* 去重：同一 action 请求尚未返回 */
    if (_mnApiInflight[action]) {
        mnShowToast(_t('Request in progress, please wait'));
        return;
    }
    /* 防抖：距上次调用 < 1s */
    if (_mnApiLastCall[action] && now - _mnApiLastCall[action] < _MN_API_DEBOUNCE_MS) {
        return;
    }
    _mnApiInflight[action] = true;
    _mnApiLastCall[action] = now;

    /* 按钮加载状态 */
    var btnOrigText = '';
    if (btn) {
        btn.disabled = true;
        btnOrigText = btn.textContent;
        btn.setAttribute('data-mn-orig', btnOrigText);
        btn.innerHTML = '<span class="mn-btn-spinner"></span>' + btnOrigText;
        btn.classList.add('mn-btn-loading');
    }

    var url = _mnApiBase() + action;

    mnFetch(url, 'POST', params || {},
        function (data) {
            _mnApiInflight[action] = false;
            _mnBtnRestore(btn);
            if (data.success) {
                mnShowToast(data.message || 'OK', 'success');
                /* 成功后刷新状态，稍后刷新页面使信息同步 */
                mnRefreshStatus();
                setTimeout(function () { window.location.reload(); }, 2000);
            } else {
                mnShowToast(
                    data.message || _t('Operation failed'),
                    'error',
                    { detail: data.detail || '', duration: 10000 }
                );
            }
        },
        function (err) {
            _mnApiInflight[action] = false;
            _mnBtnRestore(btn);
            mnShowToast(_t('Network error: ') + err, 'error', { duration: 8000 });
        }
    );
}

/** _mnBtnRestore — 恢复按钮原始状态 */
function _mnBtnRestore(btn) {
    if (!btn) return;
    btn.disabled = false;
    btn.classList.remove('mn-btn-loading');
    var orig = btn.getAttribute('data-mn-orig');
    if (orig) btn.textContent = orig;
}

/* ─── 状态轮询 ───────────────────────────────────────────── */

/**
 * mnRefreshStatus — 拉取 GNB 服务状态并更新控制台指示点
 */
function mnRefreshStatus() {
    mnFetch(_mnApiBase() + 'status', 'GET', null,
        function (data) {
            if (!data.success) return;
            _updateStatusDots(data.vpn_status === 'running');
        },
        null  // 静默失败
    );
}

function _updateStatusDots(running) {
    // Dashboard + node 页状态点
    var nodeDot = document.getElementById('mn-vpn-dot');
    if (nodeDot) nodeDot.className = 'mn-dot ' + (running ? 'mn-dot-green' : 'mn-dot-red');

    var nodeLabel = document.getElementById('mn-vpn-label');
    if (nodeLabel) {
        nodeLabel.textContent = running ? _t('Running') : _t('Stopped');
        nodeLabel.className   = running ? 'mn-dep-ok' : 'mn-dep-fail';
    }

    var labels = document.querySelectorAll('.mn-status-label strong, .mn-running, .mn-stopped');
    labels.forEach(function (el) {
        if (running) {
            el.className = 'mn-running';
        } else {
            el.className = 'mn-stopped';
        }
    });
}

/* ─── GNB 自动安装 ───────────────────────────────────────── */

var _gnbInstallTimer = null;

/**
 * mnAutoInstallGnb — 页面加载时调用。
 * 若服务端渲染的 window.mnGnbCtlOk === false，自动触发后台安装（防重入由服务端控制）。
 * 参考 mynet_tui startup_helper.go autoInstallDrivers()
 */
function mnAutoInstallGnb() {
    if (window.mnGnbCtlOk) return;   // gnb_ctl 已存在，无需操作

    var panel = document.getElementById('mn-gnb-install-panel');
    if (!panel) return;

    panel.classList.remove('mn-hidden');
    _gnbSetMsg(_t('Triggering GNB auto-install…'), false);

    var baseUrl = _mnApiBase();

    // 调用 api_gnb_auto_install（防重入：服务端保证只启动一次）
    mnFetch(baseUrl + 'gnb_auto_install', 'POST', {},
        function (data) {
            if (data.status === 'already_ok') {
                _gnbSetMsg(_t('gnb_ctl installed') + ' ✓', false);
                _gnbMarkDepOk('gnb_ctl');
                setTimeout(function () { panel.classList.add('mn-hidden'); }, 3000);
                return;
            }
            if (data.status === 'error') {
                _gnbSetMsg(_t('Auto-install failed: ') + (data.message || ''), true);
                return;
            }
            // started 或 already_running → 开始轮询
            var archHint = data.arch ? (' (' + (data.os_name || '') + '/' + data.arch + ')') : '';
            _gnbSetMsg(_t('Installing GNB') + archHint + '…', false);
            _gnbPollStatus();
        },
        function (err) {
            _gnbSetMsg(_t('Request failed: ') + err, true);
        }
    );
}

/** _gnbPollStatus — 每 3 秒轮询 api_gnb_install_status，直到完成或失败 */
function _gnbPollStatus() {
    if (_gnbInstallTimer) return;   // 已在轮询
    _gnbInstallTimer = setInterval(function () {
        var baseUrl = _mnApiBase();
        mnFetch(baseUrl + 'gnb_install_status', 'GET', null,
            function (resp) {
                if (!resp.success || !resp.data) return;
                var d = resp.data;

                // 显示最新日志（最后 3 行）
                if (d.log_tail && d.log_tail !== '') {
                    var lines = d.log_tail.split('\n').filter(function (l) { return l.trim(); });
                    var last3 = lines.slice(-3).join('\n');
                    var logEl = document.getElementById('mn-gnb-install-log');
                    if (logEl) {
                        logEl.textContent = last3;
                        logEl.classList.remove('mn-hidden');
                    }
                }

                if (d.gnb_exists) {
                    // 安装成功
                    clearInterval(_gnbInstallTimer); _gnbInstallTimer = null;
                    _gnbSetMsg(_t('GNB install complete') + ' ✓', false);
                    _gnbMarkDepOk('gnb_ctl');
                    window.mnGnbCtlOk = true;
                    setTimeout(function () {
                        var panel = document.getElementById('mn-gnb-install-panel');
                        if (panel) panel.classList.add('mn-hidden');
                    }, 5000);
                    return;
                }

                if (d.done && d.failed) {
                    clearInterval(_gnbInstallTimer); _gnbInstallTimer = null;
                    _gnbSetMsg(_t('Install failed, check log'), true);
                    return;
                }

                if (!d.running && d.done) {
                    // 安装脚本退出但未检测到 gnb_exists（可能延迟）
                    clearInterval(_gnbInstallTimer); _gnbInstallTimer = null;
                    _gnbSetMsg(_t('Install script finished, refresh to check'), false);
                    return;
                }
            },
            null  // 静默失败
        );
    }, 3000);
}

/** _gnbSetMsg — 更新进度消息和图标状态；isError=true 时显示重试按钮 */
function _gnbSetMsg(msg, isError) {
    var msgEl    = document.getElementById('mn-gnb-install-msg');
    var iconEl   = document.getElementById('mn-gnb-install-icon');
    var retryBtn = document.getElementById('mn-gnb-retry-btn');
    if (msgEl)  msgEl.textContent = msg;
    if (iconEl) {
        if (isError) {
            iconEl.textContent = '✗';
            iconEl.className = 'mn-dep-fail';
        } else if (msg.indexOf('✓') >= 0) {
            iconEl.textContent = '✓';
            iconEl.className = 'mn-dep-ok';
        } else {
            iconEl.textContent = '⟳';
            iconEl.className = 'mn-gnb-spin';
        }
    }
    // 失败时显示重试按钮，进行中或成功时隐藏
    if (retryBtn) retryBtn.classList.toggle('mn-hidden', !isError);
}

/** mnRetryInstallGnb — 手动重试安装（强刷入口） */
function mnRetryInstallGnb() {
    if (_gnbInstallTimer) { clearInterval(_gnbInstallTimer); _gnbInstallTimer = null; }
    window.mnGnbCtlOk = false;
    var logEl = document.getElementById('mn-gnb-install-log');
    if (logEl) { logEl.textContent = ''; logEl.classList.add('mn-hidden'); }
    mnAutoInstallGnb();
}

/** _gnbMarkDepOk — 将依赖列表中 gnb_ctl 标记为绿色 ✓ */
function _gnbMarkDepOk(depName) {
    var items = document.querySelectorAll('[data-dep="' + depName + '"]');
    items.forEach(function (el) {
        el.className = 'mn-dep-item mn-dep-ok';
        el.textContent = depName + ' ✓';
    });
}

/** _mnApiBase — 构造 API 基础 URL */
function _mnApiBase() {
    return window.location.pathname
        .replace(/\/admin\/services\/mynet.*$/, '/admin/services/mynet/api/');
}

/** mnToggle — 切换元素可见性，并更新按钮图标和文字
 *  @param {string}      elId  要切换的元素 ID
 *  @param {HTMLElement} btn   触发按钮（可选，更新其内容）
 */
function mnToggle(elId, btn) {
    var el = document.getElementById(elId);
    if (!el) return;
    var hidden = el.classList.contains('mn-hidden');
    if (hidden) {
        el.classList.remove('mn-hidden');
        if (btn) btn.innerHTML = btn.dataset.labelCollapse || '&#9650; Hide';
    } else {
        el.classList.add('mn-hidden');
        if (btn) btn.innerHTML = btn.dataset.labelExpand  || '&#9660; View';
    }
}

// ─────────────────────────────────────────────────────────────
// Node 配置页 JS
// ─────────────────────────────────────────────────────────────

/** mnNodeRefreshConfig — 刷新单个配置文件
 *  @param {string} cfgType  "node" | "route" | "address"
 *  @param {HTMLElement} btn  按钮元素（显示加载状态）
 */
function mnNodeRefreshConfig(cfgType, btn) {
    var nodeId = window.mnCurrentNodeId;
    if (!nodeId) { return; }
    var orig = btn ? btn.textContent : '';
    if (btn) { btn.disabled = true; btn.textContent = '…'; }

    var statusEl = document.getElementById('mn-cfg-' + cfgType + '-status');
    if (statusEl) { statusEl.textContent = ''; statusEl.className = 'mn-config-status mn-muted mn-small'; }

    mnFetch(_mnApiBase() + 'node_config', 'POST',
        { node_id: nodeId, type: cfgType },
        function (data) {
            if (btn) { btn.disabled = false; btn.textContent = orig; }
            if (statusEl) {
                if (data.success) {
                    statusEl.textContent = '✓ ' + _t('updated');
                    statusEl.className = 'mn-config-status mn-dep-ok mn-small';
                } else {
                    statusEl.textContent = '✗ ' + (data.error || data.message || 'failed');
                    statusEl.className = 'mn-config-status mn-dep-fail mn-small';
                }
            }
            // 如果有配置内容返回则刷新 pre 块（当前实现是页面刷新）
            if (data.success) {
                setTimeout(function () { window.location.reload(); }, 800);
            }
        },
        function (err) {
            if (btn) { btn.disabled = false; btn.textContent = orig; }
            if (statusEl) {
                statusEl.textContent = '✗ ' + err;
                statusEl.className = 'mn-config-status mn-dep-fail mn-small';
            }
        }
    );
}

/** mnNodeRefreshAll — 一键刷新全部配置 */
function mnNodeRefreshAll() {
    var nodeId = window.mnCurrentNodeId;
    if (!nodeId) { return; }
    var gEl = document.getElementById('mn-node-global-status');
    if (gEl) { gEl.textContent = _t('Refreshing…'); gEl.className = 'mn-small mn-muted'; }

    mnFetch(_mnApiBase() + 'node_config', 'POST',
        { node_id: nodeId, type: 'all' },
        function (data) {
            if (gEl) {
                if (data.success) {
                    gEl.textContent = '✓ ' + _t('All configs refreshed') + ' (' + (data.files ? data.files.length : 0) + ' ' + _t('files') + ')';
                    gEl.className = 'mn-small mn-dep-ok';
                    setTimeout(function () { window.location.reload(); }, 800);
                } else {
                    var errs = data.errors && data.errors.length ? data.errors.join('; ') : (data.message || 'failed');
                    gEl.textContent = '✗ ' + errs;
                    gEl.className = 'mn-small mn-dep-fail';
                }
            }
        },
        function (err) {
            if (gEl) { gEl.textContent = '✗ ' + err; gEl.className = 'mn-small mn-dep-fail'; }
        }
    );
}

/** mnNodeSwitch — 切换节点 */
function mnNodeSwitch() {
    var sel = document.getElementById('mn-switch-node-id');
    if (!sel) return;
    var nodeId = sel.value;
    if (!nodeId) return;

    var selOpt  = sel.options[sel.selectedIndex];
    var label   = selOpt ? selOpt.text.trim() : nodeId;
    if (!window.confirm(_t('Switch to node:') + '\n' + label + '\n\n' + _t('This will save the new node configuration. Continue?'))) return;

    var resEl = document.getElementById('mn-switch-result');
    if (resEl) { resEl.textContent = _t('Switching…'); resEl.className = 'mn-small mn-muted'; resEl.style.display = 'block'; }

    mnFetch(_mnApiBase() + 'node_switch', 'POST',
        { node_id: nodeId },
        function (data) {
            if (data.success) {
                if (resEl) { resEl.textContent = '✓ ' + (data.message || 'switched'); resEl.className = 'mn-small mn-dep-ok'; }
                setTimeout(function () {
                    window.location.href = data.redirect || window.location.pathname;
                }, 600);
            } else {
                if (resEl) { resEl.textContent = '✗ ' + (data.message || 'switch failed'); resEl.className = 'mn-small mn-dep-fail'; }
            }
        },
        function (err) {
            if (resEl) { resEl.textContent = '✗ ' + err; resEl.className = 'mn-small mn-dep-fail'; }
        }
    );
}

/** mnKeyMode — 切换私钥操作模式（file / paste / gen），三选一互斥 */
function mnKeyMode(mode) {
    var modes = ['file', 'paste', 'gen'];
    modes.forEach(function(m) {
        var panel = document.getElementById('mn-keymode-' + m);
        var btn   = document.getElementById('mn-keymode-' + m + '-btn');
        if (panel) panel.classList.toggle('mn-hidden', m !== mode);
        if (btn) {
            btn.classList.toggle('mn-btn-active', m === mode);
            btn.classList.toggle('mn-btn-secondary', m !== mode);
        }
    });
}

/** mnNodeSavePrivKey — 保存私钥（从文件或输入框） */
function mnNodeSavePrivKey(mode) {
    var nodeId   = window.mnCurrentNodeId;
    // 兼容旧调用（无参数时）和新模式参数
    if (!mode) mode = 'file';
    var statusId = (mode === 'paste') ? 'mn-privkey-paste-status' : 'mn-privkey-status';
    var statusEl = document.getElementById(statusId);
    if (!nodeId) return;

    function _doSave(hexStr) {
        hexStr = hexStr.replace(/\s+/g, '');
        if (!hexStr) {
            if (statusEl) { statusEl.textContent = _t('Key is empty'); statusEl.className = 'mn-small mn-dep-fail'; }
            return;
        }
        if (hexStr.length !== 128 || !/^[0-9a-fA-F]{128}$/.test(hexStr)) {
            if (statusEl) { statusEl.textContent = '\u2717 ' + _t('Must be 128 hex chars') + ' (got ' + hexStr.length + ')'; statusEl.className = 'mn-small mn-dep-fail'; }
            return;
        }
        if (statusEl) { statusEl.textContent = _t('Saving…'); statusEl.className = 'mn-small mn-muted'; }

        mnFetch(_mnApiBase() + 'node_save_key', 'POST',
            { node_id: nodeId, key_hex: hexStr },
            function (data) {
                if (statusEl) {
                    if (data.success) {
                        statusEl.textContent = '✓ ' + _t('Saved');
                        statusEl.className = 'mn-small mn-dep-ok';
                        setTimeout(function () { window.location.reload(); }, 800);
                    } else {
                        statusEl.textContent = '✗ ' + (data.message || 'save failed');
                        statusEl.className = 'mn-small mn-dep-fail';
                    }
                }
            },
            function (err) {
                if (statusEl) { statusEl.textContent = '✗ ' + err; statusEl.className = 'mn-small mn-dep-fail'; }
            }
        );
    }

    if (mode === 'file') {
        var fileInput = document.getElementById('mn-privkey-file');
        if (fileInput && fileInput.files && fileInput.files[0]) {
            var reader = new FileReader();
            reader.onload = function (e) { _doSave(e.target.result || ''); };
            reader.readAsText(fileInput.files[0]);
            return;
        }
        if (statusEl) { statusEl.textContent = _t('No file selected'); statusEl.className = 'mn-small mn-dep-fail'; }
        return;
    }

    if (mode === 'paste') {
        var textInput = document.getElementById('mn-privkey-input');
        if (textInput && textInput.value.trim() !== '') {
            _doSave(textInput.value);
            return;
        }
        if (statusEl) { statusEl.textContent = _t('No key pasted'); statusEl.className = 'mn-small mn-dep-fail'; }
        return;
    }

    if (statusEl) { statusEl.textContent = _t('No key provided'); statusEl.className = 'mn-small mn-dep-fail'; }
}

/**
 * mnNodeGenKey — 在服务器上生成新 GNB 密钥对并上传公钥
 * 生成流程：调用 gnb_crypto 生成密钥对 → 私钥保存本地 → 公钥上传服务器
 * 操作前需用户确认（此操作会导致所有对端节点需要重新交换公钥）
 */
function mnNodeGenKey() {
    var nodeId   = window.mnCurrentNodeId;
    var statusEl = document.getElementById('mn-genkey-status');
    if (!nodeId || nodeId === '0' || nodeId === '') {
        if (statusEl) { statusEl.textContent = _t('No node selected'); statusEl.className = 'mn-small mn-dep-fail'; }
        return;
    }

    var confirmed = window.confirm(
        '⚠ ' + _t('Warning: Generating a new key pair will replace the current private key.') + '\n\n' +
        _t('All peer nodes will need to re-fetch public keys before reconnecting.') + '\n\n' +
        _t('Continue?')
    );
    if (!confirmed) return;

    if (statusEl) { statusEl.textContent = _t('Generating key pair…'); statusEl.className = 'mn-small mn-muted'; }

    mnFetch(_mnApiBase() + 'node_gen_key', 'POST',
        { node_id: nodeId },
        function (data) {
            if (data.success) {
                if (statusEl) {
                    statusEl.textContent = '✓ ' + _t('New key pair generated, public key uploaded. pub: ') +
                        (data.pub_hex ? data.pub_hex.substring(0, 16) + '…' : '');
                    statusEl.className = 'mn-small mn-dep-ok';
                }
                setTimeout(function () { window.location.reload(); }, 1200);
            } else {
                if (statusEl) {
                    statusEl.textContent = '✗ ' + (data.message || 'generation failed');
                    statusEl.className = 'mn-small mn-dep-fail';
                }
            }
        },
        function (err) {
            if (statusEl) { statusEl.textContent = '✗ ' + err; statusEl.className = 'mn-small mn-dep-fail'; }
        }
    );
}

/** mnInstallSystemDeps — 安装系统依赖（kmod-tun / bash / curl-tls） */
function mnInstallSystemDeps(btn) {
    var statusEl = document.getElementById('mn-sysdesp-status');
    if (btn) { btn.disabled = true; btn.textContent = '…'; }
    if (statusEl) { statusEl.textContent = _t('Installing…'); statusEl.className = 'mn-small mn-muted'; }

    mnFetch(_mnApiBase() + 'install_system_deps', 'POST', {},
        function (data) {
            if (btn) { btn.disabled = false; btn.textContent = '⚡ ' + _t('Auto-Install'); }
            if (data.success) {
                var errs = data.errors && data.errors.length;
                if (statusEl) {
                    statusEl.textContent = errs ? ('⚠ partial: ' + data.errors.join(', ')) : '✓ Done';
                    statusEl.className = 'mn-small ' + (errs ? 'mn-dep-fail' : 'mn-dep-ok');
                }
                setTimeout(function () { window.location.reload(); }, 1200);
            } else {
                if (statusEl) { statusEl.textContent = '✗ ' + (data.message || 'failed'); statusEl.className = 'mn-small mn-dep-fail'; }
            }
        },
        function (err) {
            if (btn) { btn.disabled = false; btn.textContent = '⚡ Auto-Install'; }
            if (statusEl) { statusEl.textContent = '✗ ' + err; statusEl.className = 'mn-small mn-dep-fail'; }
        }
    );
}

/* ─── Dashboard 统计轮询 ─────────────────────────────────── */

/**
 * mnRefreshDashboardStats — 拉取 dashboard_stats 并更新 UI 元素
 * 元素 id 命名约定: mn-stat-{key}
 */
function mnRefreshDashboardStats() {
    mnFetch(_mnApiBase() + 'dashboard_stats', 'POST', {},
        function (data) {
            if (!data.success) return;
            var m = data.metrics || {};
            var ids = {
                'mn-stat-cpu':    (m.cpu_percent || 0) + '%',
                'mn-stat-mem':    m.memory_total ? Math.round(m.memory_used / m.memory_total * 100) + '%' : '--',
                'mn-stat-vpn':    m.vpn_status || '--',
                'mn-stat-peers':  String(data.peers || 0),
                'mn-stat-rx':     _mnFmtBytes(m.rx_bytes || 0),
                'mn-stat-tx':     _mnFmtBytes(m.tx_bytes || 0),
                'mn-stat-uptime': _mnFmtUptime(m.uptime || 0),
                'mn-stat-svc':    (data.svc_state && data.svc_state.state) || '--',
            };
            for (var k in ids) {
                var el = document.getElementById(k);
                if (el) el.textContent = ids[k];
            }
        },
        function () { /* 静默失败 */ }
    );
}

function _mnFmtBytes(b) {
    if (b < 1024) return b + ' B';
    if (b < 1048576) return (b / 1024).toFixed(1) + ' KB';
    if (b < 1073741824) return (b / 1048576).toFixed(1) + ' MB';
    return (b / 1073741824).toFixed(2) + ' GB';
}

function _mnFmtUptime(sec) {
    if (!sec || sec <= 0) return '-';
    var d = Math.floor(sec / 86400);
    var h = Math.floor((sec % 86400) / 3600);
    var m = Math.floor((sec % 3600) / 60);
    if (d > 0) return d + 'd ' + h + 'h';
    if (h > 0) return h + 'h ' + m + 'm';
    return m + 'm';
}

/* ─── Pre-flight 校验展示 ────────────────────────────────── */

/**
 * mnRunPreflight — 执行 pre-flight 校验并在指定容器展示结果
 * @param {string|number} nodeId
 * @param {HTMLElement} container - 结果容器
 */
function mnRunPreflight(nodeId, container) {
    if (!container) return;
    container.innerHTML = '<span class="mn-small">' + _t('Checking…') + '</span>';
    mnFetch(_mnApiBase() + 'preflight', 'POST', { node_id: nodeId },
        function (data) {
            if (!data.success || !data.data) {
                container.innerHTML = '<span class="mn-dep-fail">' + _t('Check failed') + '</span>';
                return;
            }
            var checks = data.data.checks || [];
            var html = '<table class="table table-condensed"><thead><tr><th>Check</th><th>Status</th><th>Detail</th></tr></thead><tbody>';
            for (var i = 0; i < checks.length; i++) {
                var c = checks[i];
                var cls = c.ok ? 'mn-dep-ok' : 'mn-dep-fail';
                html += '<tr><td>' + c.name + '</td><td class="' + cls + '">' + (c.ok ? '✓' : '✗') + '</td><td class="mn-small">' + (c.detail || '') + '</td></tr>';
            }
            html += '</tbody></table>';
            container.innerHTML = html;
        },
        function (err) {
            container.innerHTML = '<span class="mn-dep-fail">Error: ' + err + '</span>';
        }
    );
}

/* ─── 日志尾部查看 ───────────────────────────────────────── */

function mnRefreshLogs(container, lines) {
    if (!container) return;
    mnFetch(_mnApiBase() + 'logs_tail', 'POST', { lines: lines || 100 },
        function (data) {
            if (data.success && data.log) {
                container.textContent = data.log;
                container.scrollTop = container.scrollHeight;
            }
        },
        function () { /* 静默 */ }
    );
}

/* ─── Tab 切换 ────────────────────────────────────────────── */

/**
 * mnSwitchTab — 通用 tab 切换
 * @param {string} tabName   - 例如 'keys' | 'peers'
 * @param {HTMLElement} btn  - 按钮
 */
function mnSwitchTab(tabName, btn) {
    // 找到按钮所在的 section 容器，限制 tab 切换范围
    var scope = btn ? btn.closest('.mn-section') : null;
    var panels = (scope || document).querySelectorAll('.mn-tab-panel');
    panels.forEach(function (p) { p.style.display = 'none'; });

    // 取消所有 tab 按钮激活
    var btns = (scope || document).querySelectorAll('.mn-tab-btn');
    btns.forEach(function (b) { b.classList.remove('mn-tab-active'); });

    // 显示目标 panel（用 style.display 覆盖 CSS .mn-tab-panel { display:none }）
    var target = document.getElementById('mn-tab-' + tabName);
    if (target) target.style.display = 'block';

    // 激活当前按钮
    if (btn) btn.classList.add('mn-tab-active');
}

/* ─── 诊断 ────────────────────────────────────────────────── */

/**
 * mnRunDiagnose — 运行系统诊断
 */
function mnRunDiagnose() {
    var btn = document.getElementById('mn-diag-btn');
    if (btn) btn.disabled = true;

    var resultDiv = document.getElementById('mn-diag-result');
    var logSection = document.getElementById('mn-diag-log-section');
    var tbody = document.querySelector('#mn-diag-table tbody');
    var logPre = document.getElementById('mn-diag-log');

    if (tbody) tbody.innerHTML = '<tr><td colspan="3">' + _t('Checking…') + '</td></tr>';
    if (resultDiv) resultDiv.classList.remove('mn-hidden');
    if (logSection) logSection.classList.add('mn-hidden');

    mnFetch(_mnApiBase() + 'diagnose', 'POST', {},
        function (data) {
            if (btn) btn.disabled = false;
            if (!data.success || !data.data) {
                if (tbody) tbody.innerHTML = '<tr><td colspan="3" class="mn-dep-fail">' +
                    (data.message || 'Diagnostics failed') + '</td></tr>';
                return;
            }
            var d = data.data;
            var checks = d.checks || [];
            var html = '';
            for (var i = 0; i < checks.length; i++) {
                var c = checks[i];
                var cls = c.ok ? 'mn-dep-ok' : 'mn-dep-fail';
                var icon = c.ok ? '✓' : '✗';
                html += '<tr><td>' + _esc(c.name) + '</td>'
                    + '<td class="' + cls + '">' + icon + '</td>'
                    + '<td class="mn-small">' + _esc(c.detail || '') + '</td></tr>';
            }
            if (tbody) tbody.innerHTML = html;

            // GNB 日志
            if (d.gnb_log && logPre) {
                logPre.textContent = d.gnb_log;
                if (logSection) logSection.classList.remove('mn-hidden');
            }
        },
        function (err) {
            if (btn) btn.disabled = false;
            if (tbody) tbody.innerHTML = '<tr><td colspan="3" class="mn-dep-fail">Error: ' +
                _esc(err) + '</td></tr>';
        }
    );
}

/* mnNodeRefreshAll: 使用上方 L363 的完整实现（POST node_config type=all） */
