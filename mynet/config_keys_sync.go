package mynet

import (
	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// Sync the existing public key without regenerating the device identity.
//
// 顺带回填 back 节点面公钥（meta.json 的 back_pub_hex）：早期 keys set/generate
// 写死空串，mynetd 节点面因此永远装配不了 —— sync 是在线的，正好补。
func configFaceKeysSync() int { return configFaceKeysSyncConnect(false) }
func configFaceKeysSyncConnect(connect bool) int {
	cred := LoadCredential()
	if cred == nil || cred.Token == "" {
		return printJSON(map[string]any{"error": "need_login", "need_login": true})
	}
	src, mode, err := (&config.Store{}).ActiveSource()
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	if mode == nil || mode.Active.NodeID == "" {
		return printJSON(map[string]any{"error": "请先选择网络身份"})
	}
	if connect && mode.Mode != domain.ModeOnline {
		return printJSON(map[string]any{"error": "请先选择云端设备"})
	}
	keysDir := utils.ResolveKeysDir(src, mode.Active.NodeID)
	_, pub, _, _, err := keys.LoadNodeKeys(keysDir)
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}

	out := map[string]any{"synced": true}
	if meta, mErr := keys.LoadNodeMeta(keysDir); mErr == nil && meta.BackPubHex == "" && mode.Mode == domain.ModeOnline {
		if bp, fErr := fetchBackFacePub(); fErr != nil {
			out["back_pub_warning"] = "回填 back 节点面公钥失败: " + fErr.Error()
		} else if uErr := keys.UpdateNodeMetaBackPub(keysDir, mode.Active.NodeID, bp); uErr != nil {
			out["back_pub_warning"] = "回填 back 节点面公钥写盘失败: " + uErr.Error()
		} else {
			out["back_pub_backfilled"] = true
		}
	}

	if connect {
		if warning, ok := out["back_pub_warning"]; ok {
			return printJSON(map[string]any{"error": warning})
		}
	}
	c := newBackClientFor(cred)
	path := "/api/v1/nodes/" + mode.Active.NodeID
	if _, _, err = c.doJSON("PUT", path+"/keys", map[string]string{"custom_public_key": pub, "key_type": gnbKeyType(string(mode.Active.Driver))}); err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	if connect {
		if _, _, err = c.doJSON("PATCH", path+"/status", map[string]int{"status": 1}); err != nil {
			return printJSON(map[string]any{"error": "启用设备失败：" + err.Error(), "uploaded": true})
		}
	}
	if _, _, err = c.doJSON("POST", path+"/bundle/publish", map[string]any{}); err != nil {
		return printJSON(map[string]any{"error": "公钥已上传，但发布配置失败：" + err.Error(), "uploaded": true})
	}
	return printJSON(out)
}
