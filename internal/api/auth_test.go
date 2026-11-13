package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	credentials "github.com/lifei6671/github-radar/internal/auth"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
)

func TestAdminPasswordSession(t *testing.T) {
	cfg := testAdminConfig(config.Config{})
	cfg.Server.SiteURL = "https://radar.example.com"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(nil, nil, logger, cfg, nil)
	const auth = consts.APIPath + consts.AuthGroupPath
	call := func(router http.Handler, method, path, origin, body string, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://internal:8080"+path, strings.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s 得到 %d %s", method, path, rec.Code, rec.Body.String())
		}
		return rec
	}
	status := func(router http.Handler, cookie *http.Cookie, authenticated string) {
		t.Helper()
		rec := call(router, http.MethodGet, auth+consts.AuthSessionPath, "", "", cookie, 200)
		if rec.Body.String() != `{"authenticated":`+authenticated+`}` {
			t.Fatalf("会话状态错误: %s", rec.Body.String())
		}
	}
	status(router, nil, "false")
	call(router, http.MethodPost, auth+consts.AuthLoginPath, "", `{"password":"wrong"}`, nil, 401)
	call(router, http.MethodPost, auth+consts.AuthLoginPath, "", `{"password":"`+cfg.Auth.AdminPassword+`"}`, nil, 401)
	body := `{"password":"` + testAdminPassword + `"}`
	call(router, http.MethodPost, auth+consts.AuthLoginPath, "https://untrusted.example.com", body, nil, 403)
	login := call(router, http.MethodPost, auth+consts.AuthLoginPath, cfg.Server.SiteURL, body, nil, 200)
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("登录应签发一个会话 Cookie")
	}
	cookie := cookies[0]
	if cookie.Name != consts.AdminSessionCookie || !cookie.HttpOnly || !cookie.Secure ||
		cookie.SameSite != http.SameSiteStrictMode || cookie.Path != consts.APIPath ||
		cookie.MaxAge != consts.AdminSessionAgeSeconds || strings.Contains(login.Body.String(), cfg.Auth.AdminPassword) {
		t.Fatal("会话 Cookie 属性或响应内容不符合约定")
	}
	status(router, cookie, "true")
	status(router, &http.Cookie{Name: cookie.Name, Value: cookie.Value + "x"}, "false")
	call(router, http.MethodPost, consts.APIPath+"/setup/initialize", "https://untrusted.example.com", "", cookie, 403)
	rotatedHash, err := credentials.HashPassword("new-password-for-admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, rotated := range []config.Auth{
		{AdminPassword: rotatedHash, SessionSecret: cfg.Auth.SessionSecret},
		{AdminPassword: cfg.Auth.AdminPassword, SessionSecret: "changed-signing-secret-at-least-32-bytes"},
	} {
		next := cfg
		next.Auth = rotated
		status(NewRouter(nil, nil, logger, next, nil), cookie, "false")
	}
	logout := call(router, http.MethodPost, auth+consts.AuthLogoutPath, cfg.Server.SiteURL, "", cookie, 200)
	removed := logout.Result().Cookies()
	if len(removed) != 1 || removed[0].Name != cookie.Name || removed[0].Path != cookie.Path || removed[0].MaxAge != -1 {
		t.Fatal("退出登录应清除原路径的会话 Cookie")
	}
	// 旧版令牌不能作为登录凭据继续访问管理 API。
	req := httptest.NewRequest(http.MethodGet, consts.APIPath+"/setup/status", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Auth.AdminPassword)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatal("管理 API 应拒绝旧 Bearer 请求")
	}
}
