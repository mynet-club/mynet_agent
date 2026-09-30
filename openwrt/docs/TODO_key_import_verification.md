# TODO: 私钥导入时本地验证与服务端比对

**状态**: 延迟实现（Lua 侧暂时跳过，待后续处理）  
**相关文件**: 
- `luasrc/controller/mynet.lua` — `api_node_save_key()`
- `luasrc/model/mynet/node.lua` — `save_private_key()`, `fetch_server_public_key()`
- `mynet_tui/internal/api/client.go` — `UploadNodePublicKey()`, `GetNodePublicKey()`
- `mynet_tui/cmd/private_key_importer.go` — 完整导入流程参考

---

## 背景

用户在 GNB Crypt Keys 页面通过"Upload File"或"Paste Hex"方式导入私钥时，理想的完整流程应该是：

1. 读取 128 hex char 私钥
2. 从私钥派生对应公钥（ed25519 本地运算）
3. 将本地派生公钥与服务端存储的公钥比对
4. 若一致 → 保存私钥到本地，同时从服务端拉取公钥写本地
5. 若不一致 → 提示用户"私钥与服务端公钥不匹配"，询问是否强制覆盖（上传新公钥）
6. 若服务端无公钥记录 → 直接上传（调用 `POST /nodes/{id}/keys/upload`）

## 当前实现（简化版）

**跳过了第2、3步**，直接：
1. 验证格式（128 hex chars）
2. 写私钥到 `{GNB_CONF_DIR}/{nid}/security/{nid}.private`（chmod 600）
3. 从服务端 `GET /nodes/{id}/keys` 拉取公钥
4. 写公钥到 `security/{nid}.public` 和 `ed25519/{nid}.public`

原因：Lua 没有原生 ed25519 公钥派生支持，需要调外部命令或 C 绑定，实现复杂度高。

---

## 完整流程参考（mynet_tui 实现）

### mynet_tui/cmd/private_key_importer.go 核心逻辑

```go
// 1. 读取私钥文件（128 hex chars）
privateKeyHex := strings.TrimSpace(string(keyData))
if len(privateKeyHex) != 128 { ... error ... }

// 2. 本地派生公钥
// GNB 私钥格式：前64个hex（32字节）是seed，后64个hex（32字节）是拼接的公钥
// 直接截取后64 hex = 公钥（ed25519标准：私钥后半部分即公钥）
publicKeyHex := privateKeyHex[64:] // 字符位置64-128

// 3. 读取服务端公钥
serverPubKey = client.GetNodePublicKey(nodeID)  // GET /nodes/{id}/keys

// 4. 比对
if serverPubKey != "" && serverPubKey != publicKeyHex {
    // 提示不匹配，询问是否强制覆盖
    // 用户确认后调用 UploadNodePublicKey(..., force=true)
}

// 5. 上传（如服务端无或用户确认覆盖）
client.UploadNodePublicKey(nodeID, publicKeyHex, force)

// 6. 写本地文件
os.WriteFile(privateKeyFile, []byte(privateKeyHex), 0600)
os.WriteFile(publicKeyFile,  []byte(publicKeyHex), 0644)  // 无换行
```

### 关键点

- **GNB ed25519 私钥结构**：128 hex chars = 64 bytes
  - bytes 0-31 (hex 0-63): seed（真正的私密部分）
  - bytes 32-63 (hex 64-127): 对应公钥（ed25519 标准）
  - 因此：`public_key_hex = private_key_hex[64:128]`（字符串截取，不需要加密运算）

- **公钥文件**：64 hex chars，NO newline，路径 `ed25519/{nid}.public`

- **服务端 API**：
  - 获取：`GET /nodes/{id}/keys`
  - 上传：`POST /nodes/{id}/keys/upload` body: `{ "custom_public_key": "...", "force_regenerate": true }`

---

## Lua 实现方案（待实现）

```lua
-- 从 128-hex 私钥中提取公钥（无需加密，直接字符串截取）
local function derive_public_key(priv_hex)
    if #priv_hex ~= 128 then return nil, "invalid private key length" end
    return priv_hex:sub(65, 128), nil  -- Lua 1-based index, chars 65-128
end
```

> **注意**：上述截取方式基于 GNB 工具 `gnb_crypto -c` 生成密钥的格式约定，
> 公钥就在私钥的后64个字符。需要在实机上用 `gnb_crypto` 验证这个假设。

### 完整实现步骤

1. 在 `node.lua` 中添加 `local function derive_public_key(priv_hex)` 按上述截取
2. 在 `api_node_save_key()` 中：
   ```lua
   local derived_pub = derive_public_key(key_hex)   -- 从导入私钥派生
   local server_pub  = fetch_server_public_key_hex(node_id)  -- 仅获取，不写文件
   if server_pub and server_pub ~= derived_pub then
       -- 返回 { success=false, mismatch=true, message="..." }
       -- 前端弹确认对话框
   else
       -- 一致或服务端无记录 → 上传
       upload_public_key(node_id, derived_pub)
       save_public_key(node_id, derived_pub)  -- 写本地
   end
   save_private_key(node_id, key_hex)
   ```
3. 前端 `mnNodeSavePrivKey()` 处理 `mismatch=true` 响应，弹出确认后附加 `force=1` 参数重提交

---

## 验证命令（在 OpenWrt 上）

```bash
# 生成密钥对查看格式
gnb_crypto -c /tmp/test_key

# 验证私钥后64字符是否等于公钥文件
PRIV=$(cat /etc/mynet/driver/gnb/conf/{nid}/security/{nid}.private)
echo "${PRIV:64:64}"  # 应等于 {nid}.public 内容
```
