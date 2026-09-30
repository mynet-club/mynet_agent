// E-6：ubus 薄桥（可选）——mynet.* 对象
// 只透传到本机 mynetd localapi，不做业务
{{
	const fs = require("fs");

	function call_localapi(path) {
		// 简化：读 system.json 等本地态；HTTP 客户端由 rpcd shell 桥补充
		return { ok: true, note: "bridge stub", path: path };
	}

	return {
		"system": {
			status: {
				args: {},
				call: function(req) {
					return call_localapi("/api/v1/status");
				}
			},
			"system-json": {
				args: {},
				call: function(req) {
					try {
						const raw = fs.readfile("/etc/mynet/var/system.json");
						return { json: raw };
					} catch (e) {
						return { error: "system.json missing" };
					}
				}
			}
		}
	};
}}
