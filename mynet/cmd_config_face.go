package mynet

// config face —— localapi 经 Ops 调用的配置面命令（JSON on stdout）。
//
// 职责边界：mynetd/localapi 只做 HTTP 壳，配置读写/密钥/切换全在执行器。
// 这里包装 shared/config + mynet/keys，对 stdout 吐**一个** JSON 对象，
// localapi 解开后套 {success, data} —— 执行器不认 HTTP 形状。
//
//	mynet config status --json
//	mynet config tenants --json
//	mynet config keys --json
//	mynet config keys generate --vpn gnb|wireguard --json
//	mynet config keys set --private HEX --public HEX [--node-id N] [--algorithm A] [--json]
//	mynet config plugins --json

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
	"github.com/mynet-club/mynet_agent-V2/shared/config"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode json: %v\n", err)
		return 1
	}
	return 0
}

// configFaceStatus mode + active。
func configFaceStatus() int {
	store := &config.Store{}
	m, err := store.LoadMode()
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	if m == nil {
		return printJSON(map[string]any{"mode": "", "initialized": false})
	}
	return printJSON(map[string]any{
		"mode":        m.Mode,
		"active":      m.Active,
		"initialized": true,
	})
}

// configFaceTenants 租户列表（含 profile 摘要）。
// remoteTenant / fetchRemoteTenants —— 云端 `/api/v1/user/tenants/` 的租户表。
// 本地 profile 在迁移时常常只有目录键（`u_<user_id>` 合成键）而没有显示名；
// 名字与真实 tenant_id 一律以云端为准。
type remoteTenant struct {
	ID   string
	Name string
}

func fetchRemoteTenants(cred *Credential) []remoteTenant {
	if cred == nil {
		return nil
	}
	c := newBackClientFor(cred)
	raw, _, err := c.doJSON("GET", "/api/v1/user/tenants/", nil)
	if err != nil {
		return nil
	}
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || !env.Success {
		return nil
	}
	var list []any
	if err := json.Unmarshal(env.Data, &list); err != nil {
		var nested struct {
			Data []any `json:"data"`
		}
		if err2 := json.Unmarshal(env.Data, &nested); err2 != nil {
			return nil
		}
		list = nested.Data
	}
	out := make([]remoteTenant, 0, len(list))
	for _, it := range list {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		id := anyToStr(m["tenant_id"])
		if id == "" {
			id = anyToStr(m["id"])
		}
		if id == "" {
			continue
		}
		name := firstStr(m, "name", "tenant_name")
		out = append(out, remoteTenant{ID: id, Name: name})
	}
	return out
}

// pickDefaultTenant 用户裁定：默认租户是 ID 最大的那条。
// fetchRemoteNodeName 从云端节点清单取显示名（/api/v1/nodes 分页）。
func fetchRemoteNodeName(cred *Credential, nodeID string) string {
	if cred == nil || nodeID == "" {
		return ""
	}
	rows, err := allCloudNodes(newBackClientFor(cred))
	if err != nil {
		return ""
	}
	for _, it := range rows {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		id := anyToStr(m["node_id"])
		if id == "" {
			id = anyToStr(m["id"])
		}
		if id != nodeID {
			continue
		}
		if n := firstStr(m, "node_name", "name"); n != "" {
			return n
		}
	}
	return ""
}

func pickDefaultTenant(list []remoteTenant) (remoteTenant, bool) {
	var best remoteTenant
	found := false
	for _, t := range list {
		if !found || t.ID > best.ID {
			best = t
			found = true
		}
	}
	return best, found
}

// normalizeSnowflake 修正 JSON 数字被 fmt 成科学计数法的雪花 ID。
// 形如 3.844197700300659e+15 → 3844197700300659。
func normalizeSnowflake(s string) string {
	if s == "" || !strings.Contains(s, "e") && !strings.Contains(s, "E") {
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e18 {
		return strconv.FormatInt(int64(f), 10)
	}
	return s
}

// rememberTenantName 把云端显示名写回本地 profile，离线也能显示。
func rememberTenantName(store *config.Store, localKey, name, realID string) {
	if localKey == "" || name == "" {
		return
	}
	p, err := store.LoadOnlineProfile(localKey)
	if err != nil || p == nil {
		return
	}
	changed := false
	if p.TenantName != name {
		p.TenantName = name
		changed = true
	}
	if realID != "" && p.TenantID != realID && !strings.HasPrefix(p.TenantID, "u_") {
		// 只有目录键本身就是真实 id 时才改 TenantID 字段；
		// 目录键是 u_* 合成键时保持不动，避免路径断裂。
		_ = realID
	}
	if changed {
		_ = store.SaveOnlineProfile(p)
	}
}

func configFaceTenants() int {
	store := &config.Store{}
	list, err := store.ListTenants()
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	remote := fetchRemoteTenants(LoadCredential())
	remoteByID := map[string]remoteTenant{}
	var remoteDefault remoteTenant
	hasDefault := false
	for _, t := range remote {
		remoteByID[t.ID] = t
	}
	remoteDefault, hasDefault = pickDefaultTenant(remote)
	type tenantInfo struct {
		ID     string `json:"id"`
		Name   string `json:"name,omitempty"`
		Driver string `json:"driver,omitempty"`
		NodeID string `json:"node_id,omitempty"`
		RealID string `json:"real_tenant_id,omitempty"`
	}
	out := []tenantInfo{}
	for _, id := range list {
		ti := tenantInfo{ID: id}
		if p, err := store.LoadOnlineProfile(id); err == nil {
			ti.Name = p.TenantName
			ti.Driver = string(p.Driver)
			ti.NodeID = p.NodeID
		}
		// 名字为空时，用云端默认租户 / 同 id 租户补
		if ti.Name == "" {
			if rt, ok := remoteByID[id]; ok {
				ti.Name = rt.Name
				ti.RealID = rt.ID
			} else if hasDefault {
				ti.Name = remoteDefault.Name
				ti.RealID = remoteDefault.ID
			}
		}
		out = append(out, ti)
	}
	// 云端有、本地还没有 profile 的租户（含默认租户）也列出
	seen := map[string]bool{}
	for _, id := range list {
		seen[id] = true
	}
	for _, rt := range remote {
		if seen[rt.ID] {
			continue
		}
		out = append(out, tenantInfo{ID: rt.ID, Name: rt.Name, RealID: rt.ID})
	}
	return printJSON(out)
}

// configFaceKeys keys 子命令。
func configFaceKeys(args []string) int {
	sub := "show"
	if len(args) > 0 && !isFlag(args[0]) {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "show", "status":
		return configFaceKeysShow()
	case "generate":
		return configFaceKeysGenerate(args)
	case "set":
		return configFaceKeysSet(args)
	case "sync":
		return configFaceKeysSync()
	default:
		fmt.Fprintf(os.Stderr, "config keys: 未知子命令 %q\n", sub)
		return 2
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

func configFaceKeysDir() (src string, mode *domain.ModeFile, err error) {
	store := &config.Store{}
	src, mode, err = store.ActiveSource()
	if err != nil {
		return "", nil, err
	}
	nodeID := ""
	if mode != nil {
		nodeID = mode.Active.NodeID
	}
	// 密钥挂**节点**，不挂租户：{space}/nodes/{nodeID}/keys
	return utils.ResolveKeysDir(src, nodeID), mode, nil
}

// configFaceKeysDirWrite **写入**一律用节点级路径，不回退 legacy。
func configFaceKeysDirWrite() (src, dir string, mode *domain.ModeFile, err error) {
	store := &config.Store{}
	src, mode, err = store.ActiveSource()
	if err != nil {
		return "", "", nil, err
	}
	nodeID := ""
	if mode != nil {
		nodeID = mode.Active.NodeID
	}
	if nodeID == "" {
		return "", "", mode, fmt.Errorf("当前空间没有 node_id，无法定位密钥目录")
	}
	dir, err = utils.NodeKeysDir(src, nodeID)
	return src, dir, mode, err
}

// configFaceKeysShow 只回公钥指纹与是否存在，**绝不回 private**。
func configFaceKeysShow() int {
	keysDir, mode, err := configFaceKeysDir()
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	priv, pub, algo, nodeID, err := keys.LoadNodeKeys(keysDir)
	has := err == nil && priv != ""
	modeName, src := "", keysDir
	if mode != nil {
		modeName = string(mode.Mode)
	}
	if mode != nil && mode.Active.Source != "" {
		src = mode.Active.Source
	}
	return printJSON(map[string]any{
		"has_private":   has,
		"has_public":    pub != "",
		"public_hex":    pub,
		"public_prefix": prefixHex(pub),
		"algorithm":     algo,
		"node_id":       nodeID,
		"source":        src,
		"mode":          modeName,
		"keys_dir":      keysDir,
	})
}

func prefixHex(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8] + "…"
}

func configFaceKeysGenerate(args []string) int {
	vpn := ""
	nodeID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--vpn":
			if i+1 < len(args) {
				i++
				vpn = args[i]
			}
		case "--node-id":
			if i+1 < len(args) {
				i++
				nodeID = args[i]
			}
		}
	}
	_, keysDir, mode, err := configFaceKeysDirWrite()
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	if vpn == "" {
		vpn = "gnb"
		if mode != nil && string(mode.Active.Driver) == "wireguard" {
			vpn = "wireguard"
		}
	}
	if nodeID == "" && mode != nil {
		nodeID = mode.Active.NodeID
	}
	priv, pub, algo, err := keys.GenerateNodeKeys(vpn)
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	if err := keys.WriteNodeKeys(keysDir, priv, pub, algo, nodeID, ""); err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	return printJSON(map[string]any{
		"public_hex": pub,
		"algorithm":  algo,
		"node_id":    nodeID,
		"path":       keysDir,
		"note":       "private 仅本机；联网模式请把 public_hex 上传到 back",
	})
}

func configFaceKeysSet(args []string) int {
	priv, pub, nodeID, algo := "", "", "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--private":
			if i+1 < len(args) {
				i++
				priv = args[i]
			}
		case "--public":
			if i+1 < len(args) {
				i++
				pub = args[i]
			}
		case "--node-id":
			if i+1 < len(args) {
				i++
				nodeID = args[i]
			}
		case "--algorithm":
			if i+1 < len(args) {
				i++
				algo = args[i]
			}
		}
	}
	if priv == "" || pub == "" {
		return printJSON(map[string]any{"error": "需要 --private 与 --public"})
	}
	_, keysDir, mode, err := configFaceKeysDirWrite()
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	if nodeID == "" && mode != nil {
		nodeID = mode.Active.NodeID
	}
	if err := keys.WriteNodeKeys(keysDir, priv, pub, algo, nodeID, ""); err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	return printJSON(map[string]any{
		"public_hex": pub,
		"node_id":    nodeID,
		"path":       keysDir,
		"note":       "private 仅本机；联网模式请把 public_hex 上传到 back",
	})
}

// configFacePlugins 插件清单（配置面，不含进程）。
func configFacePlugins() int {
	store := &config.Store{}
	src, _, err := store.ActiveSource()
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	pdir := utils.PluginsConfDir(src)
	type item struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Installed bool   `json:"installed"`
	}
	out := []item{}
	for _, name := range []string{domain.PluginNameProxy} {
		it := item{Name: name}
		if pc, err := store.LoadPlugin(pdir, name); err == nil {
			it.Enabled = pc.Enabled
		}
		if d, err := utils.PluginPackageDir(name); err == nil {
			if _, err := os.Stat(filepath.Join(d, "manifest.json")); err == nil {
				it.Installed = true
			}
		}
		out = append(out, it)
	}
	return printJSON(out)
}

// configFaceUse config use 的 JSON 形态（切换 local/online）。
func configFaceUse(args []string) int {
	store := &config.Store{}
	if len(args) < 1 {
		return printJSON(map[string]any{"error": "usage: config use local|online --tenant T [--node N]"})
	}
	switch args[0] {
	case "local":
		m, err := store.UseLocal()
		if err != nil {
			return printJSON(map[string]any{"error": err.Error()})
		}
		return printJSON(m)
	case "online":
		tenant, node := "", ""
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--tenant":
				if i+1 < len(args) {
					i++
					tenant = args[i]
				}
			case "--node":
				if i+1 < len(args) {
					i++
					node = args[i]
				}
			}
		}
		if tenant == "" {
			return printJSON(map[string]any{"error": "online 需要 --tenant"})
		}
		if node == "" {
			if p, err := store.LoadOnlineProfile(tenant); err == nil {
				node = p.NodeID
			}
		}
		m, err := store.UseOnline(tenant, node)
		if err != nil {
			return printJSON(map[string]any{"error": err.Error()})
		}
		return printJSON(m)
	default:
		return printJSON(map[string]any{"error": "usage: config use local|online"})
	}
}

// configFaceIdentity 空间/用户/节点名 —— 供面板展示。
func configFaceIdentity() int {
	store := &config.Store{}
	m, _ := store.LoadMode()
	out := map[string]any{
		"logged_in": false,
	}
	cfg := utils.LoadAgentCfg()
	out["api_base"] = cfg.APIBase
	out["gateway"] = cfg.Gateway
	cred := LoadCredential()
	if cred != nil {
		out["logged_in"] = true
		out["user_email"] = cred.UserEmail
		out["user_id"] = normalizeSnowflake(cred.UserID)
		out["api_base"] = cred.APIBaseURL
	}
	if m != nil {
		out["mode"] = m.Mode
		out["tenant_id"] = m.Active.TenantID
		out["node_id"] = m.Active.NodeID
		out["driver"] = string(m.Active.Driver)
		if p, err := store.LoadOnlineProfile(m.Active.TenantID); err == nil {
			out["tenant_name"] = p.TenantName
			out["node_name"] = p.NodeName
			if p.APIBase != "" {
				out["api_base"] = p.APIBase
			}
		}
	}
	// 节点名为空时从云端补：profile 在 claim/迁移时常只写 node_id。
	if name, _ := out["node_id"].(string); name != "" {
		if cur, _ := out["node_name"].(string); cur == "" {
			if remote := fetchRemoteNodeName(LoadCredential(), name); remote != "" {
				out["node_name"] = remote
				// 回写 profile，下次不必再打云端
				if tid, _ := out["tenant_id"].(string); tid != "" {
					if p, err := store.LoadOnlineProfile(tid); err == nil && p != nil {
						p.NodeName = remote
						_ = store.SaveOnlineProfile(p)
					}
				}
			}
		}
	}
	// 联网：拉租户列表补名字（本地有缓存 profile 优先，云端补显示名）
	remote := fetchRemoteTenants(cred)
	remoteByID := map[string]remoteTenant{}
	for _, t := range remote {
		remoteByID[t.ID] = t
	}
	remoteDefault, hasDefault := pickDefaultTenant(remote)

	// 迁移残留：本地目录键可能是 `u_<user_id>`，而云端真实 tenant_id 是雪花 ID。
	// 显示名与 real_tenant_id 一律以云端为准；目录键保持不动（改目录会断路径）。
	if out["tenant_name"] == nil || out["tenant_name"] == "" {
		tid, _ := out["tenant_id"].(string)
		if rt, ok := remoteByID[tid]; ok && rt.Name != "" {
			out["tenant_name"] = rt.Name
			out["real_tenant_id"] = rt.ID
			rememberTenantName(store, tid, rt.Name, rt.ID)
		} else if hasDefault && remoteDefault.Name != "" {
			out["tenant_name"] = remoteDefault.Name
			out["real_tenant_id"] = remoteDefault.ID
			if tid != "" {
				rememberTenantName(store, tid, remoteDefault.Name, remoteDefault.ID)
			}
			// 只读展示：不写 credential.json。写凭证属登录/显式命令职责。
		}
	}

	if list, err := store.ListTenants(); err == nil && len(list) > 0 {
		type tn struct {
			ID     string `json:"id"`
			Name   string `json:"name,omitempty"`
			Node   string `json:"node_id,omitempty"`
			RealID string `json:"real_tenant_id,omitempty"`
		}
		var arr []tn
		for _, id := range list {
			t := tn{ID: id}
			if p, err := store.LoadOnlineProfile(id); err == nil {
				t.Name = p.TenantName
				t.Node = p.NodeID
			}
			if t.Name == "" {
				if rt, ok := remoteByID[id]; ok {
					t.Name = rt.Name
					t.RealID = rt.ID
				} else if hasDefault {
					t.Name = remoteDefault.Name
					t.RealID = remoteDefault.ID
				}
			}
			arr = append(arr, t)
		}
		// 云端额外租户（含默认租户）补进列表
		seen := map[string]bool{}
		for _, id := range list {
			seen[id] = true
		}
		for _, rt := range remote {
			if seen[rt.ID] {
				continue
			}
			arr = append(arr, tn{ID: rt.ID, Name: rt.Name, RealID: rt.ID})
		}
		out["tenants"] = arr
	} else if len(remote) > 0 {
		type tn struct {
			ID   string `json:"id"`
			Name string `json:"name,omitempty"`
		}
		arr := make([]tn, 0, len(remote))
		for _, rt := range remote {
			arr = append(arr, tn{ID: rt.ID, Name: rt.Name})
		}
		out["tenants"] = arr
	}
	return printJSON(out)
}

// configFaceRuntime 运行配置原文 + 计数。
func configFaceRuntime() int {
	store := &config.Store{}
	_, mode, err := store.ActiveSource()
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	nodeID := ""
	if mode != nil {
		nodeID = mode.Active.NodeID
	}
	gnbConf := filepath.Join(utils.GnbConfDir(), nodeID)
	if nodeID == "" {
		if ents, e := os.ReadDir(utils.GnbConfDir()); e == nil {
			for _, en := range ents {
				if en.IsDir() {
					gnbConf = filepath.Join(utils.GnbConfDir(), en.Name())
					nodeID = en.Name()
					break
				}
			}
		}
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(gnbConf, name))
		if err != nil {
			return ""
		}
		return string(b)
	}
	nodeConf := read("node.conf")
	routeConf := read("route.conf")
	addressConf := read("address.conf")

	// 计数：对端节点数、网段数
	peerN := 0
	netN := 0
	for _, ln := range strings.Split(routeConf, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts := strings.Split(ln, "|")
		if len(parts) < 2 {
			continue
		}
		ip := parts[1]
		if strings.HasSuffix(ip, ".0") || strings.Contains(ip, ".0.") && strings.HasSuffix(ln, "0") {
			netN++
		} else {
			// 主机行：末位非 0
			peerN++
		}
	}
	// address 行数 = 静态 endpoint 数
	addrN := 0
	for _, ln := range strings.Split(addressConf, "\n") {
		if strings.TrimSpace(ln) != "" {
			addrN++
		}
	}

	return printJSON(map[string]any{
		"node_id":        nodeID,
		"gnb_conf":       gnbConf,
		"node_conf":      nodeConf,
		"route_conf":     routeConf,
		"address_conf":   addressConf,
		"peer_count":     peerN,
		"network_count":  netN,
		"endpoint_count": addrN,
	})
}

// configFaceParity 对等配置检查：本地 node.conf vs 云端 options / 工作基线。
// 不改任何配置，只报告。导入/恢复/本地配置前应跑一遍。
func configFaceParity() int {
	local, path, err := LoadNodeConfKV()
	if err != nil {
		return printJSON(map[string]any{
			"error":  "读不到 node.conf: " + err.Error(),
			"items":  []ParityItem{},
			"ok":     false,
			"source": "local",
		})
	}
	// 尽量用云端 options 对照；失败则用 180 基线
	var remote map[string]any
	if items := fetchRemoteParityOptions(); items != nil {
		remote = items
	}
	par := CheckGNBParity(local, remote)
	ok := true
	var criticalMiss []string
	var warns []string
	for _, it := range par {
		if !it.Match {
			if it.Critical {
				ok = false
				criticalMiss = append(criticalMiss, it.Key)
			} else {
				warns = append(warns, it.Key)
			}
		}
	}
	return printJSON(map[string]any{
		"ok":            ok,
		"node_conf":     path,
		"items":         par,
		"critical_miss": criticalMiss,
		"warnings":      warns,
		"hint":          "Critical 项必须与对端一致，否则大包/TCP 可能静默丢弃",
	})
}

// fetchRemoteParityOptions 从云端 bundle.gnb.options 取对照值（尽力而为）。
func fetchRemoteParityOptions() map[string]any {
	cred := LoadCredential()
	if cred == nil {
		return nil
	}
	nodeID := activeNodeID()
	if nodeID == "" {
		return nil
	}
	if cred.APIBaseURL == "" {
		cred.APIBaseURL = utils.LoadAgentCfg().APIBase
	}
	c := newBackClientFor(cred)
	raw, code, err := c.doJSON("GET", "/api/v1/nodes/"+nodeID+"/bundle", nil)
	if err != nil {
		_ = code
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	payload := raw
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Data) > 0 {
		payload = env.Data
	}
	var bundle struct {
		Gnb struct {
			Options map[string]any `json:"options"`
		} `json:"gnb"`
	}
	if err := json.Unmarshal(payload, &bundle); err != nil {
		return nil
	}
	return bundle.Gnb.Options
}

// configFacePull 从服务端拉 bundle（需登录）→ 落 var/bundle.json → apply 投影。
func configFacePull() int {
	cred := LoadCredential()
	if cred == nil || cred.Token == "" {
		// 未登录：明确标记，UI 跳登录页
		return printJSON(map[string]any{
			"error":      "need_login",
			"need_login": true,
			"message":    "未登录，请先登录 mynet 再拉取配置",
		})
	}
	if cred.APIBaseURL == "" {
		cred.APIBaseURL = utils.LoadAgentCfg().APIBase
	}
	nodeID := ""
	st := &config.Store{}
	if _, m, err := st.ActiveSource(); err == nil && m != nil {
		nodeID = m.Active.NodeID
	}
	if nodeID == "" {
		return printJSON(map[string]any{"error": "无激活节点 node_id"})
	}
	c := newBackClientFor(cred)
	if rc := downloadBundle(c, nodeID); rc != 0 {
		return printJSON(map[string]any{
			"error":      "下载 bundle 失败（可能未登录或会话过期）",
			"need_login": true,
		})
	}
	// 同步一份到 conf/online/**/bundle.json
	store := &config.Store{}
	if src, _, err := store.ActiveSource(); err == nil {
		if b, err := os.ReadFile(filepath.Join(utils.VarDir(), "bundle.json")); err == nil {
			_ = os.WriteFile(filepath.Join(src, "bundle.json"), b, 0o600)
		}
	}
	// 投影
	if err := ApplyFromActive(false); err != nil {
		return printJSON(map[string]any{
			"pulled": true,
			"error":  "拉取成功但投影失败: " + err.Error(),
		})
	}
	// 拉取后跑对等检查（不阻断，只提醒）
	local, _, _ := LoadNodeConfKV()
	par := CheckGNBParity(local, nil)
	return printJSON(map[string]any{
		"pulled":   true,
		"node_id":  nodeID,
		"api_base": cred.APIBaseURL,
		"bundle":   filepath.Join(utils.VarDir(), "bundle.json"),
		"parity":   par,
		"message":  "已从服务端拉取并生成运行配置",
	})
}
