package mynet

// migrate-layout —— D4-E 一次性布局迁移：旧多树 → 节点唯一存放位。
//
//	mynet config migrate-layout [--dry-run]
//
// 旧布局（三处同代密钥 + 投影树）：
//
//	<home>/driver/gnb/conf/<id>/{node,route,address}.conf security/ ed25519/ …
//	<home>/driver/wireguard/<id>/
//	<home>/conf/{local,online/<tenant>}/nodes/<id>/keys/  node.{private,public} meta.json
//	（更旧的扁平 {space}/keys/ 也认，node_id 取 meta.json）
//
// 目标（唯一存放位）：`<home>/conf/<form>/<nodeId>/`（密钥 + 驱动 conf 同居）。
//
// 纪律：
//   - 密钥**同代直接搬**：gnb hex 规范化（去空白）后 sha256 核对；wg hex→base64
//     进 wg.conf 后回读比对（hex↔base64 只在搬运边界转换）。
//   - 旧树**不删**：整目录改名 `<src>.bak-<ts>` 保留，可人工回滚。
//   - 幂等：目标已有身份材料即跳过该节点。
//   - 冲突（两处密钥不一致 / 目标文件已存在且不同）→ 该节点失败并继续其余节点。
//
// 迁移后服务 `-c` 目标一致性由 install/服务单元生成面负责：这里尽力改写
// 已装的 mynet_gnb.service（无权限/不存在则明确提示重跑 mynet install）。
// **不跑任何实机迁移** —— 存量节点（181/182/184/188/.180）归用户窗口。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// migrateSource 一处旧落点。
type migrateSource struct {
	Dir   string // 旧目录
	Kind  string // gnb-tree | wg-tree | keys
	Form  string // 该落点蕴含的 form（keys 树才有；其余为空）
	Node  string // keys 扁平目录经 meta.json 解出的 node id
	Notes string
}

type migratePlan struct {
	Node    string
	Form    string
	Target  string
	Sources []migrateSource
	// KeyConflict 两处密钥不一致（不猜，失败）
	KeyConflict string
	// Done 已在目标位（幂等跳过）
	Done bool
}

func cmdConfigMigrateLayout(args []string) int {
	dry := false
	for _, a := range args {
		switch a {
		case "--dry-run":
			dry = true
		case "--help", "-h":
			fmt.Fprint(os.Stderr, usageMigrateLayout)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "migrate-layout 未知参数 %q\n%s", a, usageMigrateLayout)
			return 2
		}
	}
	// 布局迁移动的是**身份与密钥**树：home 必须显式，禁 exe/cwd 幻影路径。
	home, code := requireHome()
	if code != 0 {
		return code
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	plans := discoverMigratePlan(home)
	failed := map[string]string{}

	if len(plans) == 0 {
		fmt.Println("migrate-layout: 没有待迁移的旧布局（已是节点唯一存放位）")
		return 0
	}

	for _, p := range plans {
		fmt.Printf("节点 %s → conf/%s/%s\n", p.Node, p.Form, p.Node)
		for _, s := range p.Sources {
			fmt.Printf("  源 %-8s %s\n", s.Kind, s.Dir)
		}
		if p.Done {
			fmt.Println("  = 已在目标位（幂等跳过）")
			continue
		}
		if p.KeyConflict != "" {
			fmt.Printf("  ✗ 密钥冲突：%s（该节点不迁移，人工裁决后重跑）\n", p.KeyConflict)
			failed[p.Node] = p.KeyConflict
			continue
		}
		if dry {
			fmt.Printf("  dry-run: 将搬入 %s 并把旧树改名 .bak-%s\n", p.Target, ts)
			continue
		}
		if err := migrateOneNode(home, p, ts); err != nil {
			fmt.Printf("  ✗ %v\n", err)
			failed[p.Node] = err.Error()
			continue
		}
		fmt.Printf("  ✓ 已迁入 %s（旧树保留 .bak-%s）\n", p.Target, ts)
	}

	if !dry && len(failed) == 0 {
		// 服务 -c 目标一致性：install/服务单元生成面负责，这里尽力归位
		rewriteGnbUnitConfRel(home)
		fmt.Println("提示：服务单元的 gnb -c 已尽力归位；不放心可重跑 mynet install（生成面负责 -c 目标一致）")
	}
	note := ""
	if dry {
		note = "（dry-run 未动盘）"
	}
	fmt.Printf("migrate-layout: %d 个节点，%d 失败%s\n", len(plans), len(failed), note)
	if len(failed) > 0 {
		return 1
	}
	return 0
}

// discoverMigratePlan 扫旧落点，按 nodeID 归并出迁移计划。
func discoverMigratePlan(home string) []migratePlan {
	sources := scanMigrateSources(home)
	byNode := map[string][]migrateSource{}
	for _, s := range sources {
		node := s.Node
		if node == "" {
			node = filepath.Base(s.Dir)
		}
		if err := utils.ValidateDirName(node); err != nil {
			continue
		}
		byNode[node] = append(byNode[node], s)
	}

	activeForm, activeNode := utils.ActiveNodeInfo()
	nodes := make([]string, 0, len(byNode))
	for n := range byNode {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	var plans []migratePlan
	for _, node := range nodes {
		srcs := byNode[node]
		p := migratePlan{Node: node, Sources: srcs}

		// form 判定：keys 树投票 → 激活节点跟 mode.json → 冲突/无票兜底
		votes := map[string]bool{}
		for _, s := range srcs {
			if s.Form != "" {
				votes[s.Form] = true
			}
		}
		switch {
		case node == activeNode && activeForm != "":
			p.Form = activeForm
		case votes[utils.NodeFormLocal] && votes[utils.NodeFormOnline]:
			p.KeyConflict = "同时出现在 conf/local 与 conf/online 的 keys 树"
		case votes[utils.NodeFormOnline]:
			p.Form = utils.NodeFormOnline
		case votes[utils.NodeFormLocal]:
			p.Form = utils.NodeFormLocal
		default:
			// 只有 driver 树：跟当前 mode（单节点机即当前激活档）
			p.Form = activeForm
		}
		if p.Form == "" {
			p.Form = utils.NodeFormLocal
		}

		if tgt, err := utils.NodeRuntimePath(p.Form, node); err == nil {
			p.Target = tgt
			if utils.NodeDirHasIdentity(tgt) || fileExists(filepath.Join(tgt, "node.conf")) {
				p.Done = true
			}
		}

		// 密钥冲突预检：keys 树与 gnb security/ 的私钥必须同代
		if p.KeyConflict == "" && !p.Done {
			p.KeyConflict = detectKeyConflict(srcs, node)
		}
		plans = append(plans, p)
	}
	return plans
}

// scanMigrateSources 扫全部旧落点。
func scanMigrateSources(home string) []migrateSource {
	var out []migrateSource
	add := func(dir, kind, form, node string) {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			out = append(out, migrateSource{Dir: dir, Kind: kind, Form: form, Node: node})
		}
	}
	// 旧 gnb / wg 投影树（node id = 目录名；*.bak-<ts> 是迁移留档，跳过）
	if ents, err := os.ReadDir(filepath.Join(home, "driver", "gnb", "conf")); err == nil {
		for _, e := range ents {
			if e.IsDir() && !strings.Contains(e.Name(), ".bak-") {
				add(filepath.Join(home, "driver", "gnb", "conf", e.Name()), "gnb-tree", "", e.Name())
			}
		}
	}
	if ents, err := os.ReadDir(filepath.Join(home, "driver", "wireguard")); err == nil {
		for _, e := range ents {
			if e.IsDir() && !strings.Contains(e.Name(), ".bak-") {
				add(filepath.Join(home, "driver", "wireguard", e.Name()), "wg-tree", "", e.Name())
			}
		}
	}
	// 旧 keys 树：{space}/nodes/<id>/keys
	for _, space := range confSpaces(home) {
		form := utils.NodeFormOfSpace(space)
		if ents, err := os.ReadDir(filepath.Join(space, "nodes")); err == nil {
			for _, e := range ents {
				if e.IsDir() {
					add(filepath.Join(space, "nodes", e.Name(), "keys"), "keys", form, e.Name())
				}
			}
		}
		// 更旧的扁平 {space}/keys（node id 取 meta.json）
		flat := filepath.Join(space, "keys")
		if node := metaNodeID(flat); node != "" {
			add(flat, "keys", form, node)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// confSpaces conf/local 与 conf/online/*（旧 online 树带租户层）。
func confSpaces(home string) []string {
	out := []string{filepath.Join(home, "conf", "local")}
	ents, err := os.ReadDir(filepath.Join(home, "conf", "online"))
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, filepath.Join(home, "conf", "online", e.Name()))
		}
	}
	return out
}

func metaNodeID(dir string) string {
	m, err := keys.LoadNodeMeta(dir)
	if err != nil || m.NodeID == "" {
		return ""
	}
	return m.NodeID
}

// detectKeyConflict 同一节点多处私钥必须同代（字节级，规范化后比对）。
func detectKeyConflict(srcs []migrateSource, node string) string {
	privs := map[string]string{} // sha256(规范化 hex) → 出处
	for _, s := range srcs {
		h, ok := sourcePrivHash(s, node)
		if !ok {
			continue
		}
		if prev, hit := privs[h]; hit {
			_ = prev
			continue // 同代
		}
		if len(privs) > 0 {
			return fmt.Sprintf("%s 的私钥与其它落点不同代", s.Dir)
		}
		privs[h] = s.Dir
	}
	return ""
}

// sourcePrivHash 该落点私钥的规范化 sha256（统一转 hex 小写去空白后比对：
// gnb hex 直用；keys 树 hex 直用；wg-tree 从 wg.conf 的 base64 转 hex）。
func sourcePrivHash(s migrateSource, node string) (string, bool) {
	var priv string
	switch s.Kind {
	case "gnb-tree":
		p := filepath.Join(s.Dir, "security", node+".private")
		b, err := os.ReadFile(p)
		if err != nil {
			return "", false
		}
		priv = string(b)
	case "wg-tree":
		hexPriv, err := keys.ReadWGPrivateKeyHex(s.Dir)
		if err != nil {
			return "", false
		}
		priv = hexPriv
	case "keys":
		b, err := os.ReadFile(filepath.Join(s.Dir, "node.private"))
		if err != nil {
			return "", false
		}
		priv = string(b)
	default:
		return "", false
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(priv))))
	return hex.EncodeToString(sum[:]), true
}

// migrateOneNode 真迁移：先搬驱动树（整树复制，含 wg.conf 完整形态），
// 再搬 keys 树（只补/核对密钥，不与已复制内容互撞）→ 旧树改名 .bak-<ts>。
func migrateOneNode(home string, p migratePlan, ts string) error {
	if err := utils.SafeCreateDir(p.Target, 0o700); err != nil {
		return err
	}
	// ① 驱动树整树复制。顺序在 keys 之前：wg-tree 的 wg.conf 是完整形态
	// （[Interface]+[Peer]…），keys 路线只动 PrivateKey 行 —— 反过来会「内容不同」误撞。
	for _, s := range p.Sources {
		if s.Kind != "gnb-tree" && s.Kind != "wg-tree" {
			continue
		}
		if err := copyTreeChecked(s.Dir, p.Target); err != nil {
			return fmt.Errorf("搬 %s: %w", s.Dir, err)
		}
	}
	// ② keys 树：同代核对/补写密钥（gnb → security/，wg → wg.conf PrivateKey 行）
	for _, s := range p.Sources {
		if s.Kind != "keys" {
			continue
		}
		if err := migrateKeysSource(s, p, ts); err != nil {
			return fmt.Errorf("搬密钥 %s: %w", s.Dir, err)
		}
	}
	// ③ 旧树改名 .bak-<ts>（不删；keys 目录在 migrateKeysSource 里改名）
	for _, s := range p.Sources {
		if s.Kind == "keys" {
			continue
		}
		if err := renameBak(s.Dir, ts); err != nil {
			return fmt.Errorf("保留旧树 %s: %w", s.Dir, err)
		}
	}
	return nil
}

// migrateKeysSource 密钥同代搬移：
// gnb → security/<nodeID>.{private,public}（规范化后 sha256 核对）；
// wg  → wg.conf 的 PrivateKey（hex→base64，回读比对）。meta.json 随迁（以源为准）。
func migrateKeysSource(s migrateSource, p migratePlan, ts string) error {
	node := p.Node
	// meta.json 先读：node_id / back_pub_hex 等身份元数据是**源的为准**。
	// （WriteNodeKeys 会自写一份 meta.json，若后搬源 meta 会被它顶掉 —— 丢失
	//   back_pub_hex 等字段；故最后统一落源 meta。）
	srcMeta, srcMetaErr := os.ReadFile(filepath.Join(s.Dir, "meta.json"))
	dstMeta := filepath.Join(p.Target, "meta.json")
	if srcMetaErr == nil {
		if cur, err := os.ReadFile(dstMeta); err == nil && sha256Hex(cur) != sha256Hex(srcMeta) {
			return fmt.Errorf("%s 与目标 meta.json 不同代（不猜，人工裁决）", s.Dir)
		}
	}

	// 私钥
	b, err := os.ReadFile(filepath.Join(s.Dir, "node.private"))
	if err != nil {
		return err
	}
	privHex := strings.ToLower(strings.TrimSpace(string(b)))
	algo := ""
	backPub := ""
	if srcMetaErr == nil {
		var m keys.NodeKeyMeta
		if json.Unmarshal(srcMeta, &m) == nil {
			algo, backPub = m.Algorithm, m.BackPubHex
		}
	}
	isWG := algo == "x25519" || algo == "curve25519" || algo == "wireguard" || algo == "wg" ||
		(algo == "" && len(privHex) == 64)

	if isWG {
		// hex→base64 只在搬运边界转；回读比对走同一边界
		if _, err := keys.HexPrivToWGBase64(privHex); err != nil {
			return err
		}
		// 目标 wg.conf 已带不同的 key → 冲突（大小写归一后比）
		if cur, err := keys.ReadWGPrivateKeyHex(p.Target); err == nil && cur != "" &&
			strings.ToLower(cur) != privHex {
			return fmt.Errorf("%s 与目标 wg.conf 的私钥不同代", s.Dir)
		}
		if err := keys.WriteNodeKeys(p.Target, privHex, "", "x25519", node, backPub); err != nil {
			return err
		}
		// 回读比对（hex↔base64 只在搬运边界转）
		back, err := keys.ReadWGPrivateKeyHex(p.Target)
		if err != nil || strings.ToLower(back) != privHex {
			return fmt.Errorf("wg 私钥搬入后回读不一致（写 %s 回 %s）", privHex, back)
		}
	} else {
		dstPriv := filepath.Join(p.Target, "security", node+".private")
		if cur, err := os.ReadFile(dstPriv); err == nil {
			if strings.TrimSpace(string(cur)) != privHex {
				return fmt.Errorf("%s 与 %s 的私钥不同代", s.Dir, dstPriv)
			}
			// 已同代：不重复写
		} else {
			if err := utils.SafeCreateDir(filepath.Join(p.Target, "security"), 0o700); err != nil {
				return err
			}
			// 同代直接搬：规范化（去空白）后 sha256 核对
			if err := os.WriteFile(dstPriv, []byte(privHex), 0o600); err != nil {
				return err
			}
			got, err := os.ReadFile(dstPriv)
			if err != nil {
				return err
			}
			if sha256Hex([]byte(privHex)) != sha256Hex(got) {
				return fmt.Errorf("%s 搬入后 sha256 核对失败", s.Dir)
			}
		}
		// 公钥
		if pubB, err := os.ReadFile(filepath.Join(s.Dir, "node.public")); err == nil {
			dstPub := filepath.Join(p.Target, "security", node+".public")
			pub := strings.TrimSpace(string(pubB))
			if cur, err := os.ReadFile(dstPub); err == nil {
				if strings.TrimSpace(string(cur)) != pub {
					return fmt.Errorf("%s 与 %s 的公钥不同代", s.Dir, dstPub)
				}
			} else {
				if err := os.WriteFile(dstPub, []byte(pub), 0o644); err != nil {
					return err
				}
				got, err := os.ReadFile(dstPub)
				if err != nil || sha256Hex([]byte(pub)) != sha256Hex(got) {
					return fmt.Errorf("%s 公钥搬入后 sha256 核对失败", s.Dir)
				}
			}
		}
	}

	// meta.json 随迁：源有则以源为准（含 back_pub_hex / sign_algorithm），
	// 写后 sha256 核对；源没有就用 WriteNodeKeys 刚写的那份。
	if srcMetaErr == nil {
		if err := os.WriteFile(dstMeta, srcMeta, 0o600); err != nil {
			return err
		}
		got, err := os.ReadFile(dstMeta)
		if err != nil || sha256Hex(got) != sha256Hex(srcMeta) {
			return fmt.Errorf("meta.json 搬入后 sha256 核对失败")
		}
	}

	// 旧 keys 树改名 .bak-<ts>（扁平 {space}/keys 整目录随其唯一 node 走）
	return renameBak(s.Dir, ts)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// copyTreeChecked 递归搬文件：目标已存在且不同 → 报错（不覆盖）；相同 → 跳过。
// 每个文件写后 sha256 核对。
func copyTreeChecked(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// 生命周期锁是运行态，不迁
		if info.IsDir() {
			return utils.SafeCreateDir(filepath.Join(dst, rel), 0o700)
		}
		if info.Name() == ".gnb-lifecycle.lock" || info.Name() == "gnb.pid" {
			return nil
		}
		target := filepath.Join(dst, rel)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if cur, err := os.ReadFile(target); err == nil {
			if sha256Hex(cur) == sha256Hex(body) {
				return nil // 已同代
			}
			return fmt.Errorf("目标已存在且内容不同：%s", target)
		}
		perm := os.FileMode(0o600)
		if strings.HasSuffix(rel, ".conf") || strings.HasSuffix(rel, ".map") {
			perm = 0o644
		}
		if err := utils.SafeCreateDir(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, body, perm); err != nil {
			return err
		}
		got, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if sha256Hex(got) != sha256Hex(body) {
			return fmt.Errorf("搬入后 sha256 核对失败：%s", target)
		}
		return nil
	})
}

// renameBak 旧树整目录改名 <dir>.bak-<ts>（保留不删）。
func renameBak(dir, ts string) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	bak := dir + ".bak-" + ts
	if _, err := os.Stat(bak); err == nil {
		return nil // 已改过
	}
	return os.Rename(dir, bak)
}

// gnbSystemdUnitPaths mynet_gnb.service 的落位（生成面写 /etc/systemd/system，
// 发行包可能落 /lib/systemd/system）。包变量便于测试注入临时单元文件。
var gnbSystemdUnitPaths = []string{
	"/etc/systemd/system/mynet_gnb.service",
	"/lib/systemd/system/mynet_gnb.service",
}

// rewriteGnbUnitConfRel 尽力把已装 mynet_gnb.service 的 `-c` 目标归位到节点目录。
// 单元生成面（mynet install）是权威；这里是迁移/换身份后的兜底归位，失败只提示。
func rewriteGnbUnitConfRel(home string) {
	rewriteGnbUnitConfRelFiles(home, gnbSystemdUnitPaths)
}

// rewriteGnbUnitConfRelFiles 归位给定单元文件里 `-c …` 的目标（unix=运行投影 /var/run/mynet/<nodeId>）；
// 返回改写成功的文件数（0 = 已归位或没找到单元）。
//
// ⚠️ 必须用 ReplaceAllLiteralString：旧 want 里曾含 `${MYNET_HOME}`，而
// ReplaceAllString 的替换串里 `$` 是**组引用**语法 —— `${MYNET_HOME}` 会被
// 当命名组引用吃成空串，写出来的单元是 `-c /conf/<node>`（路径裸奔、
// 指向根下的 /conf）。2026-10-14 测试实锤（TestSetupRealignsGnbUnitToNodeConfDir）。
func rewriteGnbUnitConfRelFiles(home string, unitPaths []string) int {
	want := gnbConfPath(home)
	// 覆盖旧形态（${MYNET_HOME}/…）与新形态（绝对路径）
	re := regexp.MustCompile(`-c (?:\$\{MYNET_HOME\}/[^ \t\n]+|/var/run/mynet/[^ \t\n]+|/[^ \t\n]*mynet[^ \t\n]*)`)
	n := 0
	for _, unitPath := range unitPaths {
		body, err := os.ReadFile(unitPath)
		if err != nil {
			continue
		}
		updated := re.ReplaceAllLiteralString(string(body), "-c "+want)
		if updated == string(body) {
			continue
		}
		if werr := os.WriteFile(unitPath, []byte(updated), 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "⚠ 改写 %s 失败（重跑 mynet install 归位）: %v\n", unitPath, werr)
			continue
		}
		fmt.Println("已归位服务单元 -c 目标:", unitPath, "→", want)
		n++
	}
	return n
}

const usageMigrateLayout = `用法:
  mynet config migrate-layout [--dry-run]

把旧布局一次性迁到节点唯一存放位 conf/<form>/<nodeId>/：
  旧 driver/gnb/conf/<id>/*、driver/wireguard/<id>/*
  旧 conf/{local,online/<tenant>}/nodes/<id>/keys/*（及扁平 keys/）
密钥同代直接搬（sha256 / 回读核对），旧树改名 .bak-<ts> 保留不删。
幂等：已在目标位的节点跳过。冲突（密钥不同代）该节点失败、其余继续。
`
