package consts

// 管理端登录及签名 Cookie 共用这些协议常量。
const (
	APIPath                  = "/api/v1"
	AuthGroupPath            = "/auth"
	AuthLoginPath            = "/login"
	AuthSessionPath          = "/session"
	AuthLogoutPath           = "/logout"
	AdminSessionCookie       = "radar_admin_session"
	AdminSessionAgeSeconds   = 24 * 60 * 60
	AdminPasswordMinChars    = 12
	AdminPasswordMaxBytes    = 512
	AdminSessionSecretBytes  = 32
	CodeUnauthorized         = "unauthorized"
	CodeInvalidAdminPassword = "invalid_admin_password"
	CodeInvalidLogin         = "invalid_login"
	CodeLoginRateLimited     = "login_rate_limited"
	CodeInvalidOrigin        = "invalid_origin"
)
