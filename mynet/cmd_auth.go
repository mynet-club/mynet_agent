package mynet

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// cmdAuth — 账号态（登录/状态/登出）。
//
//	mynet auth login --email E [--password-stdin | --password P]
//	mynet auth status
//	mynet auth logout
//
// ⚠️ 这是「首次运行向导：登录 → 选节点 → 下载配置」的**第一步**，
// 但它刻意是**非交互**的：交互在本机 web 页面与 OpenWrt 的 LuCI 页面上做，
// 命令行只提供 UI 与脚本可调用的原语（用户 2026-09-25 裁定：
// 「交互实现在本机的 web 页面…OpenWrt 实现落地 luci 页面，千万不敢搞到 tui 里。
//
//	然后 mynet 命令行配合落地」）。所以这里**不做**问答式向导 ——
//
// 密码从 --password-stdin 读，跑在脚本里不打断；TUI 那套是另一个东西，不碰。
//
// 凭证存 $MYNET_HOME/conf/credential.json（0600），与 OpenWrt 侧
// LuCI 包的 /etc/mynet/conf/credential.json **同一路径同一字段**，
// 两端读写同一个文件，可以互相接管。
func cmdAuth(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet auth <login|status|logout> [参数]")
		fmt.Fprintln(os.Stderr, "  login  --email E [--password-stdin | --password P] [--tenant ID]")
		fmt.Fprintln(os.Stderr, "  status")
		fmt.Fprintln(os.Stderr, "  logout")
		return 2
	}
	switch args[0] {
	case "login":
		return authLogin(args[1:])
	case "status":
		return authStatus()
	case "logout":
		return authLogout()
	default:
		fmt.Fprintf(os.Stderr, "未知 auth 子命令: %s\n", args[0])
		return 2
	}
}

func authLogin(args []string) int {
	// 铁律：终端**只经 gateway**（本机 mynetd 中继），不直连 api_base。
	email, password, tenant := "", "", ""
	fromStdin := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--api": // 已取消（2026-10-12）：后端地址不入参，指因报错
			// 2026-10-12 全面取消：后端地址不下发、不入参、不落配置。
			return authUsage("--api 已取消：终端统一经 gateway 中继 —— 请去掉该参数")
		case "--email":
			if i+1 >= len(args) {
				return authUsage("--email 需要值")
			}
			i++
			email = args[i]
		case "--password":
			if i+1 >= len(args) {
				return authUsage("--password 需要值")
			}
			i++
			password = args[i]
		case "--password-stdin":
			fromStdin = true
		case "--tenant":
			if i+1 >= len(args) {
				return authUsage("--tenant 需要值")
			}
			i++
			tenant = args[i]
		default:
			return authUsage("未知参数 " + args[i])
		}
	}
	if email == "" {
		return authUsage("需要 --email")
	}
	if password == "" && fromStdin {
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			password = strings.TrimRight(sc.Text(), "\r\n")
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintln(os.Stderr, "读 stdin 失败:", err)
			return 1
		}
	}
	if password == "" {
		// 不做交互式 ReadPassword：那正是我们刻意避免的「CLI 里的向导」。
		// 脚本用 --password-stdin，人手敲时也能用；真要交互去 web 页面。
		return authUsage("需要密码：--password-stdin（推荐，脚本可用）或 --password P")
	}

	c := newBackClient("")
	raw, code, err := c.doJSON("POST", "/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": password,
		// Token Binding：登录时声明设备指纹，后端与 token 绑定（integration-plan §14.4）
		"device_fingerprint": DeviceFingerprint(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "登录失败（HTTP %d）: %v\n", code, err)
		return 1
	}

	// 响应形状：{success, data:{user:{user_id,name,email}, tenant_id, auth_token}}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			User struct {
				UserID interface{} `json:"user_id"`
				Name   string      `json:"name"`
				Email  string      `json:"email"`
			} `json:"user"`
			TenantID  interface{} `json:"tenant_id"`
			AuthToken string      `json:"auth_token"`
			// 后端侧建成真会话后才有这三项（mynet_api f60cf1c）
			RefreshToken       string `json:"refresh_token"`
			AuthTokenExpiresIn int    `json:"auth_token_expires_in"`
			RefreshExpiresIn   int    `json:"refresh_token_expires_in"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintf(os.Stderr, "解析登录响应失败: %v\n", err)
		return 1
	}
	if !env.Success || env.Data.AuthToken == "" {
		fmt.Fprintf(os.Stderr, "登录未成功: %s\n", env.Message)
		return 1
	}

	cred := &Credential{
		UserID:    anyToStr(env.Data.User.UserID),
		UserEmail: env.Data.User.Email,
		Token:     env.Data.AuthToken,
		// ⚠️ 这三个字段第一版**漏了**，而「几分钟内跑完一条命令」看不出来
		//（30 分钟 access token 够用）。真到「上周 login、今天 nodes」时，
		// 自动续期会拿空 refresh_token 去续 → 死路。验证产物里读出来
		// `refresh_token = <0 字符>` 才发现。
		RefreshToken:      env.Data.RefreshToken,
		AuthExpiresAt:     expiryFrom(env.Data.AuthTokenExpiresIn),
		RefreshExpiresAt:  expiryFrom(env.Data.RefreshExpiresIn),
		MachineID:         machineID(),
		DeviceFingerprint: DeviceFingerprint(),
		TenantID:          anyToStr(env.Data.TenantID),
		CreatedAt:         nowISO(),
	}
	// （写这段时又把 PHP 的 `$cred` 与 `''` 带进了 Go —— 本仓今天第二次。
	//  可见「跨语言写代码」时语法残留是系统性的，靠编译器兜比靠仔细强。）
	if cred.RefreshToken == "" {
		fmt.Fprintln(os.Stderr, "⚠️ 登录响应里没有 refresh_token —— 会话无法续期，过期后只能重登")
	}
	if tenant != "" {
		cred.TenantID = tenant
	}
	// 上游缺陷：UserAuthController 登录响应的 tenant_id 取
	// `$request->attributes->get('tenant_id')`（BuildsUserTokenResponse），
	// 而登录时尚未解析用户租户 → 恒为空。这里登录成功后向
	// `/api/v1/user/tenants/` 回填默认区，避免凭证落空 tenant_id。
	if cred.TenantID == "" {
		if tid := fetchDefaultTenantID(newBackClient(cred.Token)); tid != "" {
			cred.TenantID = tid
		}
	}
	if err := SaveCredential(cred); err != nil {
		fmt.Fprintf(os.Stderr, "保存凭证失败: %v\n", err)
		return 1
	}

	fmt.Printf("已登录 %s（user_id=%s tenant_id=%s）\n", cred.UserEmail, cred.UserID, cred.TenantID)
	fmt.Printf("凭证: %s (0600)\n", credentialPath())

	// 顺手取一次后端公钥并告诉用户 —— 向导下一步要往 mynet.conf 里写它。
	// 取不到不致命（可由运维手工给），但要明说。
	pub, err := fetchBackFacePub()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ 取后端节点面公钥失败: %v\n", err)
		fmt.Fprintf(os.Stderr, "   后续 mynet node claim 需要它；或用 --back-pub 显式给\n")
	} else {
		fmt.Printf("后端节点面公钥: %s\n", pub)
	}

	return 0
}

func authStatus() int {
	c := LoadCredential()
	if c == nil {
		if e := CredentialErr(); e != "" {
			// 权限不足/损坏 ≠ 未登录：说清真实原因，避免误导人重登覆盖好 token
			fmt.Println("✗", e)
			fmt.Println("  （不是未登录 —— 凭证在，只是当前用户读不了）")
			return 1
		}
		fmt.Println("未登录（无 credential.json 或 token 为空）")
		fmt.Println("  登录: mynet auth login --email you@example.com --password-stdin")
		return 1
	}
	fmt.Printf("已登录 %s (user_id=%s tenant_id=%s)\n", c.UserEmail, c.UserID, c.TenantID)
	fmt.Printf("  token    %s…（%d 字符）\n", truncate(c.Token, 12), len(c.Token))
	fmt.Printf("  文件     %s\n", credentialPath())

	// 顺手探一次后端公钥，让「向导前置是否就绪」一眼可见
	if pub, err := fetchBackFacePub(); err != nil {
		fmt.Printf("  后端公钥  ⚠️ 取不到: %v\n", err)
	} else {
		fmt.Printf("  后端公钥  %s\n", pub)
	}

	return 0
}

func authLogout() int {
	cred := LoadCredential()
	// ⚠️ 必须先吊销**服务端**会话。只删本地文件的话，那条会话还活着、
	// refresh token 还能继续换出新凭证 —— 那不叫登出，只是忘了。
	if cred != nil && cred.RefreshToken != "" {
		c := newBackClient(cred.Token)
		if _, code, err := c.doJSON("POST", "/api/v1/auth/revoke-session", map[string]string{
			"refresh_token": cred.RefreshToken,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ 服务端会话吊销失败（HTTP %d）: %v\n", code, err)
			fmt.Fprintln(os.Stderr, "  本地凭证仍会删除；如需彻底请稍后重试")
		} else {
			fmt.Println("服务端会话已吊销")
		}
	}
	if err := RemoveCredential(); err != nil {
		fmt.Fprintln(os.Stderr, "删除凭证失败:", err)
		return 1
	}
	fmt.Println("已登出（节点身份 mynet.conf 不受影响 —— 那是节点的，不是账号的）")
	return 0
}

// expiryFrom 把「多少秒后过期」换成 RFC3339 时刻；0/负值返回空（表示不过期或未知）。
func expiryFrom(seconds int) string {
	if seconds <= 0 {
		return ""
	}

	return time.Now().Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)
}

func authUsage(msg string) int {
	fmt.Fprintln(os.Stderr, "用法: mynet auth login --email E [--password-stdin | --password P] [--tenant ID]")
	if msg != "" {
		fmt.Fprintln(os.Stderr, "     ", msg)
	}
	return 2
}

// fetchBackFacePub 取后端的**节点面公钥**（公开端点，见 mynet_api 的
// GET /api/v1/system/back-face-key）。没有它就没法往 mynet.conf 写 BACK_PUB_HEX。
func fetchBackFacePub() (string, error) {
	c := newBackClient("")
	var data struct {
		PublicKeyHex string `json:"public_key_hex"`
		Algorithm    string `json:"algorithm"`
	}
	if err := c.getEnveloped("/api/v1/system/back-face-key", &data); err != nil {
		return "", err
	}
	if data.PublicKeyHex == "" {
		return "", fmt.Errorf("响应里没有 public_key_hex")
	}

	return data.PublicKeyHex, nil
}

// fetchDefaultTenantID 登录后回填默认网络区。
// 列表端点 data 是数组 [{tenant_id,name},…]；取首个可用。
// （上游登录响应 tenant_id 为空时的补偿，见 authLogin 注释。）
func fetchDefaultTenantID(c *backClient) string {
	if c == nil {
		return ""
	}
	raw, code, err := c.doJSON("GET", "/api/v1/user/tenants/", nil)
	if err != nil || code != 200 {
		return ""
	}
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || !env.Success {
		return ""
	}
	var list []any
	if json.Unmarshal(env.Data, &list) != nil {
		var nested struct {
			Data []any `json:"data"`
		}
		if json.Unmarshal(env.Data, &nested) != nil {
			return ""
		}
		list = nested.Data
	}
	for _, it := range list {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if id := anyToStr(m["tenant_id"]); id != "" {
			return id
		}
	}
	return ""
}
