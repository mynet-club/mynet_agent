package driver

// 平台生命周期归属：**平台服务管理器在，就交给它**。
//
// 这条规则原本只活在 Lua 执行器里（OpenWrt 上 `mynet service …` 经它委托
// `/etc/init.d/mynet`），Go 驱动没有对应实现 —— 于是**直接调 `mynet service`**（shell、
// 或任何不经执行器的入口）在 OpenWrt 上会绕过 procd 自己 spawn gnb / exec wg-quick：
//   - wg：OpenWrt 没有 wg-quick ⇒ 必失败；
//   - gnb：spawn 出的进程 procd 不认 ⇒ 与 procd 那份争 tun（双 gnb 的温床）。
//
// 与 gnb 的 `systemdManages()` 是同一取向：能托管的平台就托管，不自建一套。

import (
	"fmt"
	"strings"
)

// platformServiceOwnerFn 可注入（测试）。默认见各平台文件。
var platformServiceOwnerFn = platformServiceOwner

// delegatePlatformService 尝试把生命周期动作交给平台服务单元。
// action = start|stop|restart（与 init 脚本/服务管理器的动作名一致）。
// 返回 handled=true 表示已由平台处理（err 为其结果），调用方不要再自建。
func delegatePlatformService(execFn func(string, ...string) ([]byte, error), action string) (bool, error) {
	owner := platformServiceOwnerFn()
	if owner == "" {
		return false, nil
	}
	out, err := execFn(owner, action)
	if err != nil {
		return true, fmt.Errorf("%s %s: %w (%s)", owner, action, err, strings.TrimSpace(string(out)))
	}

	return true, nil
}
