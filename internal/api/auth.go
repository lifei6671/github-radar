package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/securecookie"
	"golang.org/x/time/rate"

	"github.com/lifei6671/github-radar/internal/auth"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
)

type adminAuth struct {
	password func(string) bool
	codec    *securecookie.SecureCookie
	secure   bool
	attempts *rate.Limiter
}

func newAdminAuth(cfg config.Config) adminAuth {
	hash, err := auth.ParsePasswordHash(cfg.Auth.AdminPassword)
	checkPassword := func(password string) bool {
		return err == nil && hash.Verify(password)
	}
	// 将密码绑定到签名密钥，重启后轮换任一凭据都会使原会话失效。
	key := hmac.New(sha256.New, []byte(cfg.Auth.SessionSecret))
	key.Write([]byte(cfg.Auth.AdminPassword))
	codec := securecookie.New(key.Sum(nil), nil).
		SetSerializer(securecookie.JSONEncoder{}).
		MaxAge(consts.AdminSessionAgeSeconds)
	return adminAuth{
		password: checkPassword,
		codec:    codec,
		secure:   strings.HasPrefix(cfg.Server.SiteURL, "https://"),
		attempts: rate.NewLimiter(rate.Every(2*time.Second), 10),
	}
}

// adminRequest 允许反代后的已配置站点地址，其他请求按 Go 的同源规则检查。
func adminRequest(siteURL string) gin.HandlerFunc {
	guard := http.NewCrossOriginProtection()
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if origin := c.GetHeader("Origin"); origin == "" || origin != siteURL {
			if err := guard.Check(c.Request); err != nil {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": consts.CodeInvalidOrigin})
				return
			}
		}
		c.Next()
	}
}

func (a adminAuth) valid(c *gin.Context) bool {
	cookie, err := c.Request.Cookie(consts.AdminSessionCookie)
	if err != nil {
		return false
	}
	var signedIn bool
	return a.codec.Decode(consts.AdminSessionCookie, cookie.Value, &signedIn) == nil && signedIn
}

func (a adminAuth) require(c *gin.Context) {
	if !a.valid(c) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": consts.CodeUnauthorized})
		return
	}
	c.Next()
}

func (a adminAuth) setCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: consts.AdminSessionCookie, Value: value, Path: consts.APIPath, MaxAge: maxAge,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode,
	})
}

func (a adminAuth) register(group *gin.RouterGroup) {
	group.GET(consts.AuthSessionPath, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"authenticated": a.valid(c)})
	})
	group.POST(consts.AuthLoginPath, func(c *gin.Context) {
		if !a.attempts.Allow() {
			c.Header("Retry-After", "2")
			c.JSON(http.StatusTooManyRequests, gin.H{"code": consts.CodeLoginRateLimited})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		var input struct {
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidLogin})
			return
		}
		if !a.password(input.Password) {
			c.JSON(http.StatusUnauthorized, gin.H{"code": consts.CodeInvalidAdminPassword})
			return
		}
		value, err := a.codec.Encode(consts.AdminSessionCookie, true)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": consts.CodeInternalError})
			return
		}
		a.setCookie(c, value, consts.AdminSessionAgeSeconds)
		c.JSON(http.StatusOK, gin.H{"authenticated": true})
	})
	group.POST(consts.AuthLogoutPath, func(c *gin.Context) {
		a.setCookie(c, "", -1)
		c.JSON(http.StatusOK, gin.H{"authenticated": false})
	})
}
