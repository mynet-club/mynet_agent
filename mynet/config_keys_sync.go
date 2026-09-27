package mynet

import (
	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
	"github.com/mynet-club/mynet_agent-V2/shared/config"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

// Sync the existing public key without regenerating the device identity.
func configFaceKeysSync() int {
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
	_, pub, _, _, err := keys.LoadNodeKeys(utils.ResolveKeysDir(src, mode.Active.NodeID))
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	c := newBackClientFor(cred)
	path := "/api/v1/nodes/" + mode.Active.NodeID
	if _, _, err = c.doJSON("PUT", path+"/keys", map[string]string{"custom_public_key": pub, "key_type": gnbKeyType(string(mode.Active.Driver))}); err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	if _, _, err = c.doJSON("POST", path+"/bundle/publish", map[string]any{}); err != nil {
		return printJSON(map[string]any{"error": "公钥已上传，但发布配置失败：" + err.Error(), "uploaded": true})
	}
	return printJSON(map[string]any{"synced": true})
}
