package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// termZone 是产品概念 Zone 的中文界面用词（2026-10-04 裁定叫「网络区」）。
//
// linux/openwrt 形态没有 WebUI，CLI 输出就是界面，所以这里也要跟着换词。
// 全仓换词清单共四处，彼此不共享，改词要一起改：
//   - mynet_gui/src/config/terms.ts       （平台前端 zh-CN）
//   - mynet_api/config/mynet.php          （API 返回给前端的中文消息）
//   - mynetd/localapi/webui/terms.js      （节点 WebUI 的 JS 文案）
//   - 本常量                               （节点 CLI 的中文输出）
//
// 命令名 `mynet tenants`、JSON 字段 tenant_id、代码标识符都不动 —— 界面词 ≠ 代码名。
const termZone = "网络区"

// cmdTenants / cmdNode —— 首次运行向导的第 2、3 步（账号态操作）。
//
//	mynet tenants
//	mynet node list
//	mynet node claim <node-id> [--vpn-type gnb|wireguard] [--endpoint host:port] [--back-pub HEX]
//
// ⚠️ 与「TUI 向导」的区别：**交互在本机 web 页面与 OpenWrt 的 LuCI 页面上**，
// 这里只提供 UI 与脚本可调用的**非交互原语**（用户 2026-09-25 裁定）。
// 所以参数一律走 flag、结果走 stdout，不做问答式引导；跑在脚本里不打断。
//
// claim 做的是「选节点 → 生成本机密钥 → 上传公钥 → 下载配置」一条龙，
// 对应向导第三步。它的关键约束：
//
//	· 私钥**只在本机生成**，绝不向后端索取（后端侧 P0-4 收口，
//	  bundle 里 private_key 恒为空）
//	· 上传的只有公钥（后端的 validateCustomGnbKey 允许只给 public）
//	· 下载配置走**用户凭证**的 GET /nodes/{id}/bundle
//	  （NodeConfigController::getBundle，api.php:108 注释明说
//	   「供 ctl 安装包等用户凭证场景」）—— 这一步解鸡生蛋：
//	  节点签名通道（/node/bundle）需要先有 mynet.conf，而 mynet.conf
//	  正是 claim 要写的。所以**向导阶段用用户 token，运行期用节点签名**。
func cmdTenants(args []string) int {
	cred := requireCred()
	c := newBackClientFor(cred)

	// ⚠️ 这个端点的 data 是**数组**（[{tenant_id,name},…]），
	// 不能走 getEnveloped(…, &map) —— 那会 "cannot unmarshal array into map"。
	// 实测踩过：别的端点 data 是对象，这里不是。
	raw, code, err := c.doJSON("GET", "/api/v1/user/tenants/", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "取%s失败（HTTP %d）: %v\n", termZone, code, err)
		return 1
	}
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || !env.Success {
		fmt.Fprintf(os.Stderr, "取%s失败: 响应异常\n", termZone)
		return 1
	}
	var list []any
	// data 可能是裸数组，也可能包在 {data:[…]} 里 —— 两种都吃得下
	if err := json.Unmarshal(env.Data, &list); err != nil {
		var nested struct {
			Data []any `json:"data"`
		}
		if err2 := json.Unmarshal(env.Data, &nested); err2 != nil {
			fmt.Fprintf(os.Stderr, "解析%s列表失败: %v\n", termZone, err)
			return 1
		}
		list = nested.Data
	}
	if len(list) == 0 {
		fmt.Printf("(没有可用的%s)\n", termZone)
		return 0
	}

	for _, it := range list {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		fmt.Printf("%s\t%s\n", anyToStr(m["tenant_id"]), firstStr(m, "name", "tenant_name"))
	}

	return 0
}

func cmdNode(args []string) int {
	// C3：node claim 写密钥/上传公钥，必须显式 MYNET_HOME
	if _, code := requireHome(); code != 0 {
		return code
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet node <list|claim>")
		fmt.Fprintln(os.Stderr, "  list")
		fmt.Fprintln(os.Stderr, "  claim <node-id> [--vpn-type gnb|wireguard] [--endpoint host:port] [--back-pub HEX]")
		return 2
	}
	switch args[0] {
	case "list":
		return nodeList()
	case "claim":
		return nodeClaim(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知 node 子命令: %s\n", args[0])
		return 2
	}
}

// nodeList 列出当前用户的节点。
// 与 `mynet nodes` 同一件事，这里放两个入口是为了让向导的语义顺序
// （tenants → node list → node claim）在命令面上也成立。
func nodeList() int {
	cred := requireCred()
	c := newBackClientFor(cred)

	var raw map[string]any
	if err := c.getEnveloped("/api/v1/nodes", &raw); err != nil {
		fmt.Fprintln(os.Stderr, "取节点失败:", err)
		return 1
	}

	// 实测形状：data.nodes.data[]（分页信封里再套一层）
	var rows []any
	if n, ok := raw["nodes"].(map[string]any); ok {
		rows, _ = n["data"].([]any)
	}
	if rows == nil {
		rows, _ = raw["data"].([]any)
	}
	if len(rows) == 0 {
		fmt.Println("(没有节点 —— 先到后端上创建)")
		return 0
	}

	fmt.Printf("%-20s %-22s %-10s %-16s %s\n", "NODE_ID", "NAME", "VPN", "OVERLAY_IP", "STATUS")
	for _, it := range rows {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		fmt.Printf("%-20s %-22s %-10s %-16s %s\n",
			anyToStr(m["node_id"]),
			truncate(fmt.Sprintf("%v", m["node_name"]), 22),
			truncate(fmt.Sprintf("%v", joinIfSlice(m["vpn_type"])), 10),
			long2ip(m["node_ip"]),
			firstStr(m, "status", "node_status"),
		)
	}

	return 0
}

func nodeClaim(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet node claim <node-id> [--vpn-type gnb|wireguard] [--endpoint host:port] [--back-pub HEX]")
		return 2
	}
	nodeID := args[0]
	// 防御：`--help` 这类标志位不能当 node-id。
	// 实测踩过：`node claim --help` 把 "--help" 写进 mode.json/profile，
	// 状态变「未配置节点」，还建出 nodes/--help/ 空树。宁可报错也不静默写脏配置。
	if strings.HasPrefix(nodeID, "-") {
		fmt.Fprintf(os.Stderr, "✗ node-id 不能以 - 开头（得到 %q）\n", nodeID)
		fmt.Fprintln(os.Stderr, "  用法: mynet node claim <node-id> [--vpn-type gnb|wireguard] …")
		fmt.Fprintln(os.Stderr, "  帮助: mynet node   （不带子命令）")
		return 2
	}
	if nodeID == "" || strings.ContainsAny(nodeID, "/\\ \t\n") {
		fmt.Fprintf(os.Stderr, "✗ node-id 含非法字符: %q\n", nodeID)
		return 2
	}
	vpnType := ""
	endpoint := ""
	backPub := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--vpn-type":
			if i+1 >= len(args) {
				return claimUsage("--vpn-type 需要 gnb|wireguard")
			}
			i++
			vpnType = args[i]
		case "--endpoint":
			if i+1 >= len(args) {
				return claimUsage("--endpoint 需要 host:port")
			}
			i++
			endpoint = args[i]
		case "--back-pub":
			if i+1 >= len(args) {
				return claimUsage("--back-pub 需要 hex")
			}
			i++
			backPub = args[i]
		default:
			return claimUsage("未知参数 " + args[i])
		}
	}

	cred := requireCred()
	c := newBackClientFor(cred)
	rows, err := allCloudNodes(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取设备类型失败:", err)
		return 1
	}
	actualVPN, err := cloudNodeVPN(rows, nodeID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if vpnType != "" && vpnType != actualVPN {
		return claimUsage("VPN 类型与云端设备不一致")
	}
	vpnType = actualVPN

	// 1) 后端的节点面公钥（写 mynet.conf 的 BACK_PUB_HEX）。
	//    公开端点，向导/装机都靠它；取不到时才允许 --back-pub 显式给。
	if backPub == "" {
		pub, err := fetchBackFacePub()
		if err != nil {
			fmt.Fprintf(os.Stderr, "取后端节点面公钥失败: %v\n", err)
			fmt.Fprintln(os.Stderr, "  用 --back-pub <hex> 显式指定（运维可从服务器上的 back_face_pub 读）")
			return 1
		}
		backPub = pub
		fmt.Println("后端节点面公钥:", backPub)
	}

	// 2) 本机生成密钥 + 写 conf/online/{tenant}/keys（配置树 v2）
	//    私钥只在本机产生，绝不向后端索取
	if rc := cmdSetupV2([]string{
		"--online",
		"--tenant", tenantOr(cred),
		"--vpn-type", vpnType,
		"--node-id", nodeID,
		"--back-pub", backPub,
	}); rc != 0 {
		return rc
	}

	// setup only selects the profile; prepare identity without rotating existing keys.
	// D4-E：密钥就存节点唯一存放位 conf/<form>/<nodeId>/
	dir, err := utils.NodeRuntimeDir(utils.NodeFormOnline, nodeID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureNodeKeys(dir, vpnType, nodeID, backPub); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// 3) 上传**公钥**（私钥不上传 ——后端的 validateCustomGnbKey 允许只给 public）
	pub, err := readNodePublicKey(cred, nodeID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读 node.public 失败:", err)
		return 1
	}
	keyType := gnbKeyType(vpnType)
	upBody := map[string]string{
		"custom_public_key": pub,
		"key_type":          keyType,
	}
	if _, code, err := c.doJSON("PUT", "/api/v1/nodes/"+nodeID+"/keys", upBody); err != nil {
		fmt.Fprintf(os.Stderr, "上传公钥失败（HTTP %d）: %v\n", code, err)
		return 1
	}
	fmt.Printf("公钥已上传到后端（key_type=%s）\n", keyType)

	// ⚠️ 必须强制重新发布 bundle，再往下下载。
	// GET /nodes/{id}/bundle 返回的是 config_revisions 里的**旧修订**
	// （只在没有修订时才现算）。刚换了公钥，旧修订里还是上一次的公钥 ——
	// 于是 refresh-config 的密钥对校验会正确地拒掉：
	//   「本机 NODE_PRIV_HEX 与 bundle 里的 public_key 不是一对」
	// 实测第一次跑 claim 就撞上了。根因在后端：节点密钥变更时该让 bundle
	// 失效（记 CPI）；这里在客户端强制 publish 一次，先把链路走通。
	if _, code, err := c.doJSON("POST", "/api/v1/nodes/"+nodeID+"/bundle/publish", map[string]any{}); err != nil {
		fmt.Fprintf(os.Stderr, "重新发布 bundle 失败（HTTP %d）: %v\n", code, err)
		return 1
	}
	fmt.Println("bundle 已重新发布（纳入新公钥）")

	// 4) 设 endpoint（没有 index 时的最小可用发现方式，见
	//    BundleEnvelopePublisher 的 static_nodes 注释）
	if endpoint != "" {
		if _, code, err := c.doJSON("PUT", "/api/v1/nodes/"+nodeID, map[string]string{
			"server_host": endpoint,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "设置 endpoint 失败（HTTP %d）: %v\n", code, err)
			return 1
		}
		fmt.Println("endpoint 已设置:", endpoint)
	}

	// 5) 下载配置 —— **用户凭证**走 GET /nodes/{id}/bundle。
	//    这一步解鸡生蛋：节点签名通道需要先有 mynet.conf，而它正是上一步写的。
	//    拿到的 envelope 落到 var/bundle.json，与 mynetd 拉的是同一个文件。
	if rc := downloadBundle(c, nodeID); rc != 0 {
		return rc
	}

	// 6) 渲染驱动原生配置 —— **claim 必须做到这一步**。
	//
	// ⚠️ 只下载 bundle 是**没做完**：节点目录（conf/<form>/<nodeId>/）里要有
	// node.conf / route.conf / address.conf / security/ / ed25519/ 才轮得到 gnb 起进程。
	// 早期版本 claim 到 downloadBundle 就收尾，向导随后 `service start gnb`
	// 直接失败在「找不到 node.conf」—— 而 claim 还打印「完成」，把人引向下一步。
	// 那正是「向导没把活干完」的形态：每一步都绿，最后一步才发现前面少干了事。
	if rc := cmdRefresh([]string{"refresh-config", "--auto"}); rc != 0 {
		fmt.Fprintln(os.Stderr, "渲染驱动配置失败（claim 的最后一步）")
		return rc
	}

	fmt.Println("\nclaim 完成：密钥已生成、公钥已上传、配置已渲染。")
	fmt.Printf("启动隧道: sudo %s service start %s\n", os.Args[0], vpnType)
	fmt.Printf("装成服务: sudo %s install --gateways <wss://…>\n", os.Args[0])
	return 0
}

// downloadBundle 用用户 token 拉 bundle 并落盘 var/bundle.json。
func downloadBundle(c *backClient, nodeID string) int { return downloadBundleOutput(c, nodeID, true) }
func downloadBundleQuiet(c *backClient, nodeID string) int {
	return downloadBundleOutput(c, nodeID, false)
}
func downloadBundleOutput(c *backClient, nodeID string, verbose bool) int {
	raw, code, err := c.doJSON("GET", "/api/v1/nodes/"+nodeID+"/bundle", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "下载配置失败（HTTP %d）: %v\n", code, err)
		return 1
	}

	var env struct {
		Data json.RawMessage `json:"data"`
	}
	envelope := raw
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Data) > 0 {
		envelope = env.Data
	}

	// 与 mynetd 的 bundleStore 同一落点：var/bundle.json
	path := varDir() + "/bundle.json"
	if err := os.MkdirAll(varDir(), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(path, envelope, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写 bundle.json 失败:", err)
		return 1
	}
	if verbose {
		fmt.Printf("配置已下载: %s（%d 字节）\n", path, len(envelope))
	}

	return 0
}

func claimUsage(msg string) int {
	fmt.Fprintln(os.Stderr, "用法: mynet node claim <node-id> [--vpn-type gnb|wireguard] [--endpoint host:port] [--back-pub HEX]")
	if msg != "" {
		fmt.Fprintln(os.Stderr, "     ", msg)
	}
	return 2
}

// requireCred 没登录就明确退出 —— 不静默降级、不猜。
func requireCred() *Credential {
	cred := LoadCredential()
	if cred == nil {
		fmt.Fprintln(os.Stderr, "未登录。先跑:")
		fmt.Fprintln(os.Stderr, "  mynet auth login --email you@example.com --password-stdin")
		os.Exit(1)
	}

	return cred
}

// tenantOr 从凭证推 tenant 目录名。
func tenantOr(cred *Credential) string {
	if cred == nil {
		return "default"
	}
	if cred.TenantID != "" {
		return cred.TenantID
	}
	if cred.UserID != "" {
		return "u_" + cred.UserID
	}
	return "default"
}

// readNodePublicKey 读节点身份公钥（D4-E：节点唯一存放位 conf/<form>/<nodeId>/）。
// claim 上传的是这把 public；旧 keys/ 树由 LoadNodeKeys 只读兼容。
func readNodePublicKey(cred *Credential, nodeID string) (string, error) {
	keysDir, err := utils.NodeRuntimeDir(utils.NodeFormOnline, nodeID)
	if err != nil {
		return "", err
	}
	_, pub, _, _, err := keys.LoadNodeKeys(keysDir)
	if err != nil {
		return "", err
	}
	if pub == "" {
		return "", fmt.Errorf("node public key 为空")
	}
	return pub, nil
}

// joinIfSlice vpn_type 在后端侧是 string[]（A-7），打印时合起来。
func joinIfSlice(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			parts = append(parts, fmt.Sprintf("%v", x))
		}

		return strings.Join(parts, ",")
	default:
		return fmt.Sprintf("%v", t)
	}
}

// firstStr 取 map 里第一个非空的字段值（后端的同一概念在不同端点叫法不一：
// name/tenant_name、status/node_status）。注意 **Go 没有 PHP 的 ??** ——
// 我第一版就写成了 `m["name"] ?? m["tenant_name"]`，编译不过。
func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s := strings.TrimSpace(fmt.Sprintf("%v", v)); s != "" && s != "<nil>" {
				return s
			}
		}
	}

	return ""
}

// long2ip 后端的 node_ip 是 ip2long 的整数，人要看点分。
func long2ip(v any) string {
	n := int64(0)
	switch t := v.(type) {
	case float64:
		n = int64(t)
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return s // 已是点分就不动
		}
	default:
		return ""
	}
	if n == 0 {
		return "-"
	}

	return fmt.Sprintf("%d.%d.%d.%d", (n>>24)&0xff, (n>>16)&0xff, (n>>8)&0xff, n&0xff)
}

func cloudNodeVPN(rows []any, nodeID string) (string, error) {
	for _, row := range rows {
		n, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(n["node_id"]) != nodeID {
			continue
		}
		raw := n["vpn_type"]
		if types, ok := raw.([]any); ok {
			if len(types) != 1 {
				return "", fmt.Errorf("设备需要在专家模式选择 VPN 类型")
			}
			raw = types[0]
		}
		vpn, _ := raw.(string)
		if vpn == "gnb" || vpn == "wireguard" {
			return vpn, nil
		}
		return "", fmt.Errorf("不支持的设备 VPN 类型")
	}
	return "", fmt.Errorf("账号下找不到设备 %s", nodeID)
}
