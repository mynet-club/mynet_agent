package localapi

// LoginSession 向导登录会话（localapi 侧形状，组合根映射到 mynet.Credential）。
// 不 import mynet —— B2：凭证读写经注入。
type LoginSession struct {
	UserID            string
	UserEmail         string
	Token             string
	RefreshToken      string
	AuthExpiresAt     string
	RefreshExpiresAt  string
	TenantID          string
	MachineID         string
	DeviceFingerprint string
	CreatedAt         string
}
