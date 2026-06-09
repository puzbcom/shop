// Package middleware provides Gin middleware matching Laravel's web + api middleware groups.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"mall/internal/models"
	"mall/internal/services/i18n"
	"mall/internal/services/settings"

	"gorm.io/gorm"
)

// Session returns the sessions middleware using a signed cookie store.
// Set secure=true in production so the session cookie is only sent over HTTPS.
func Session(secret []byte, secure bool) gin.HandlerFunc {
	store := cookie.NewStore(secret)
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return sessions.Sessions("mall_session", store)
}

// Locale reads the language from the session (set_language endpoint) or business_settings default.
// It stores the active lang code and Language model in the context.
func Locale(db *gorm.DB, st *settings.Store, i18nSvc *i18n.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		sess := sessions.Default(c)
		lang, _ := sess.Get("locale").(string)
		if lang == "" {
			lang = st.Get("default_language", "en")
		}
		var language models.Language
		if err := db.Where("code = ?", lang).First(&language).Error; err != nil {
			lang = "en"
			language = models.Language{Code: "en", Name: "English"}
		}
		c.Set("locale", lang)
		c.Set("language", language)
		i18nSvc.SetDefaultLang(st.Get("default_language", "en"))
		c.Next()
	}
}

// Recovery replaces gin.Recovery with structured logging.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic: %v\n%s", r, debug.Stack())
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

// Logger is a minimal request logger.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Printf("%s %s %d %s", c.Request.Method, c.Request.URL.Path,
			c.Writer.Status(), time.Since(start))
	}
}

// CSRF validates the _token form field on POST/PUT/PATCH/DELETE requests.
// SPA/API routes should skip this middleware.
func CSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method == http.MethodPost || method == http.MethodPut ||
			method == http.MethodPatch || method == http.MethodDelete {
			sess := sessions.Default(c)
			token, _ := sess.Get("_token").(string)
			// Accept token from form field or X-CSRF-Token header
			submitted := c.PostForm("_token")
			if submitted == "" {
				submitted = c.GetHeader("X-CSRF-Token")
			}
			if token == "" || token != submitted {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
		}
		c.Next()
	}
}

// GenerateCSRF sets a new CSRF token in the session if one doesn't exist.
func GenerateCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		sess := sessions.Default(c)
		if tok, _ := sess.Get("_token").(string); tok == "" {
			sess.Set("_token", generateToken())
			_ = sess.Save()
		}
		c.Next()
	}
}

func generateToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}

// Auth sets the current user in context from the session. Does not abort — use RequireAuth for that.
func Auth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		sess := sessions.Default(c)
		if uid, ok := sess.Get("user_id").(uint); ok && uid > 0 {
			var u models.User
			if err := db.First(&u, uid).Error; err == nil {
				c.Set("user", &u)
				c.Set("user_id", uid)
			}
		}
		c.Next()
	}
}

// RequireAuth aborts with 302 to /login if no authenticated user is in context.
// wantsJSON reports whether the request was made by client-side JS expecting a
// JSON response (fetch/XHR) rather than a full page navigation. Such requests
// must receive a JSON error status instead of a 302 redirect to an HTML page —
// otherwise fetch silently follows the redirect and r.json() fails with
// "Unexpected token '<'".
func wantsJSON(c *gin.Context) bool {
	if c.GetHeader("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	if c.GetHeader("X-CSRF-Token") != "" {
		return true
	}
	if strings.Contains(c.GetHeader("Accept"), "application/json") {
		return true
	}
	return false
}

func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, exists := c.Get("user"); !exists {
			if wantsJSON(c) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Your session has expired. Please log in again."})
				return
			}
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireUserType aborts with 403 if the authenticated user's type doesn't match any of the given types.
func RequireUserType(types ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(types))
	for _, t := range types {
		allowed[t] = true
	}
	return func(c *gin.Context) {
		u, exists := c.Get("user")
		if !exists {
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
			return
		}
		user := u.(*models.User)
		if !allowed[user.UserType] {
			if wantsJSON(c) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "You don't have permission to perform that action."})
				return
			}
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}

// Unbanned aborts if the user is banned.
// Special case: if an admin is impersonating a banned seller, restore the
// admin's session instead of locking them out.
func Unbanned() gin.HandlerFunc {
	return func(c *gin.Context) {
		if u, exists := c.Get("user"); exists {
			if user := u.(*models.User); user.Banned == 1 {
				sess := sessions.Default(c)
				// If there is an impersonator, restore the admin and redirect safely.
				if adminID, ok := sess.Get("impersonator_id").(uint); ok && adminID > 0 {
					sess.Set("user_id", adminID)
					sess.Delete("impersonator_id")
					_ = sess.Save()
					c.Redirect(http.StatusFound, "/admin/sellers?error=Impersonated+seller+is+banned")
					c.Abort()
					return
				}
				// Normal banned user: clear session and redirect to login.
				sess.Clear()
				_ = sess.Save()
				c.Redirect(http.StatusFound, "/login?banned=1")
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// TrackAffiliate stores the ?ref= query param in the session so it survives
// across page loads until the visitor registers or makes a purchase.
func TrackAffiliate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if ref := c.Query("ref"); ref != "" {
			sess := sessions.Default(c)
			sess.Set("affiliate_ref", ref)
			_ = sess.Save()
		}
		c.Next()
	}
}

// RequirePermission allows admins through unconditionally, and checks the named
// permission (via role matching user_type) for all other staff types.
// Unauthorized requests are redirected to /admin/dashboard with a flash error.
func RequirePermission(db *gorm.DB, perm string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u, exists := c.Get("user")
		if !exists {
			if wantsJSON(c) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Your session has expired. Please log in again."})
				return
			}
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
			return
		}
		user := u.(*models.User)
		if user.UserType == "admin" || user.UserType == "super_admin" {
			c.Next()
			return
		}
		var count int64
		db.Table("roles").
			Joins("JOIN role_has_permissions ON role_has_permissions.role_id = roles.id").
			Joins("JOIN permissions ON permissions.id = role_has_permissions.permission_id").
			Where("roles.name = ? AND permissions.name = ?", user.UserType, perm).
			Count(&count)
		if count > 0 {
			c.Next()
			return
		}
		if wantsJSON(c) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "You don't have permission to perform that action."})
			return
		}
		sess := sessions.Default(c)
		sess.Set("flash_error", "You don't have permission to access that page.")
		_ = sess.Save()
		c.Redirect(http.StatusFound, "/admin/dashboard")
		c.Abort()
	}
}

// GuestOnly redirects authenticated users away from guest-only pages.
func GuestOnly(redirectTo string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, exists := c.Get("user"); exists {
			c.Redirect(http.StatusFound, redirectTo)
			c.Abort()
			return
		}
		c.Next()
	}
}
