package view

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/services/settings"
)

// --- named route registry ---

var (
	routeMu    sync.RWMutex
	routeTable = make(map[string]string) // name -> pattern with :param replaced later
)

// RegisterRoute adds a named route. Call from router setup.
func RegisterRoute(name, pattern string) {
	routeMu.Lock()
	routeTable[name] = pattern
	routeMu.Unlock()
}

// resolveRoute builds a URL from a named route and optional positional params.
// Params are substituted left-to-right into :segment placeholders.
func resolveRoute(name string, params ...interface{}) string {
	routeMu.RLock()
	pattern, ok := routeTable[name]
	routeMu.RUnlock()
	if !ok {
		return "/" + name
	}
	parts := strings.Split(pattern, "/")
	pi := 0
	for i, p := range parts {
		if strings.HasPrefix(p, ":") && pi < len(params) {
			parts[i] = fmt.Sprintf("%v", params[pi])
			pi++
		}
	}
	return strings.Join(parts, "/")
}

// --- uploaded_asset ---

var uploadCache sync.Map // id (uint) -> string URL

func uploadedAsset(db *gorm.DB, v interface{}) string {
	if v == nil {
		return "/static/assets/img/placeholder.jpg"
	}
	switch val := v.(type) {
	case string:
		if val == "" {
			return "/static/assets/img/placeholder.jpg"
		}
		// Could be a numeric string (upload ID) or a path
		if id, err := strconv.ParseUint(val, 10, 64); err == nil {
			return resolveUploadID(db, uint(id))
		}
		// If path already contains uploads/, don't double-prefix
		if strings.HasPrefix(val, "uploads/") || strings.Contains(val, "/uploads/") {
			return "/" + strings.TrimPrefix(val, "/")
		}
		return "/uploads/" + strings.TrimPrefix(val, "/")
	case *string:
		if val == nil || *val == "" {
			return "/static/assets/img/placeholder.jpg"
		}
		return uploadedAsset(db, *val)
	case *uint:
		if val == nil {
			return "/static/assets/img/placeholder.jpg"
		}
		return resolveUploadID(db, *val)
	case uint:
		return resolveUploadID(db, val)
	case int:
		return resolveUploadID(db, uint(val))
	case int64:
		return resolveUploadID(db, uint(val))
	case float64:
		return resolveUploadID(db, uint(val))
	}
	return "/static/assets/img/placeholder.jpg"
}

func resolveUploadID(db *gorm.DB, id uint) string {
	if id == 0 {
		return "/static/assets/img/placeholder.jpg"
	}
	if v, ok := uploadCache.Load(id); ok {
		return v.(string)
	}
	var upload models.Upload
	if err := db.First(&upload, id).Error; err != nil {
		return "/static/assets/img/placeholder.jpg"
	}
	if upload.FileName == nil {
		return "/static/assets/img/placeholder.jpg"
	}
	fn := *upload.FileName
	// FileName is stored as "uploads/all/xxx.webp", we need "/uploads/all/xxx.webp"
	url := "/" + strings.TrimPrefix(fn, "/")
	uploadCache.Store(id, url)
	return url
}

// --- price formatting ---

func formatPrice(st *settings.Store, amount interface{}) string {
	val := toFloat64(amount)
	symbol := st.Get("currency_symbol", "$")
	pos := st.Get("currency_symbol_format", "symbol_left")
	decimals := 2
	if d := st.Get("no_of_decimals", "2"); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			decimals = n
		}
	}
	sep := st.Get("decimal_separator", ".")
	thou := st.Get("thousands_separator", ",")
	formatted := formatNumber(val, decimals, sep, thou)
	if pos == "symbol_right" {
		return formatted + symbol
	}
	return symbol + formatted
}

func singlePrice(st *settings.Store, amount interface{}) string {
	return formatPrice(st, amount)
}

func formatNumber(val float64, decimals int, decSep, thousSep string) string {
	base := fmt.Sprintf("%.*f", decimals, val)
	parts := strings.SplitN(base, ".", 2)
	intPart := parts[0]
	decPart := ""
	if len(parts) == 2 {
		decPart = parts[1]
	}

	// Add thousands separator
	n := len(intPart)
	if n > 3 {
		var sb strings.Builder
		mod := n % 3
		if mod != 0 {
			sb.WriteString(intPart[:mod])
			intPart = intPart[mod:]
		}
		for i := 0; i < len(intPart); i += 3 {
			if sb.Len() > 0 {
				sb.WriteString(thousSep)
			}
			sb.WriteString(intPart[i : i+3])
		}
		intPart = sb.String()
	}

	if decimals > 0 {
		return intPart + decSep + decPart
	}
	return intPart
}

func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case *float64:
		if val != nil {
			return *val
		}
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case *int:
		if val != nil {
			return float64(*val)
		}
	case int64:
		return float64(val)
	case *int64:
		if val != nil {
			return float64(*val)
		}
	case uint:
		return float64(val)
	case *uint:
		if val != nil {
			return float64(*val)
		}
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	}
	return 0
}

// --- RBAC ---

func userHasPermission(db *gorm.DB, u *models.User, permName string) bool {
	if u == nil {
		return false
	}
	// Admins and super admins have unrestricted access.
	if u.UserType == "admin" || u.UserType == "super_admin" {
		return true
	}
	// Primary check: user_type maps to a role by name (operation, finance, …).
	var count int64
	db.Table("roles").
		Joins("JOIN role_has_permissions ON role_has_permissions.role_id = roles.id").
		Joins("JOIN permissions ON permissions.id = role_has_permissions.permission_id").
		Where("roles.name = ? AND permissions.name = ?", u.UserType, permName).
		Count(&count)
	if count > 0 {
		return true
	}
	// Fallback: direct user permission.
	db.Table("model_has_permissions").
		Joins("JOIN permissions ON permissions.id = model_has_permissions.permission_id").
		Where("model_has_permissions.model_type = ? AND model_has_permissions.model_id = ? AND permissions.name = ?",
			`App\Models\User`, u.ID, permName).
		Count(&count)
	if count > 0 {
		return true
	}
	// Fallback: user assigned to role via model_has_roles.
	db.Table("model_has_roles").
		Joins("JOIN role_has_permissions ON role_has_permissions.role_id = model_has_roles.role_id").
		Joins("JOIN permissions ON permissions.id = role_has_permissions.permission_id").
		Where("model_has_roles.model_type = ? AND model_has_roles.model_id = ? AND permissions.name = ?",
			`App\Models\User`, u.ID, permName).
		Count(&count)
	return count > 0
}

// --- hex2rgba ---

func hex2rgba(hex string, alpha float64) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) != 6 {
		return fmt.Sprintf("rgba(0,0,0,%.2f)", alpha)
	}
	r, _ := strconv.ParseInt(hex[0:2], 16, 32)
	g, _ := strconv.ParseInt(hex[2:4], 16, 32)
	b, _ := strconv.ParseInt(hex[4:6], 16, 32)
	return fmt.Sprintf("rgba(%d,%d,%d,%.2f)", r, g, b, alpha)
}

// --- flash messages ---

func flashGet(c *gin.Context, key string) string {
	sess := sessions.Default(c)
	val, _ := sess.Get("flash_" + key).(string)
	if val != "" {
		sess.Delete("flash_" + key)
		_ = sess.Save()
	}
	return val
}

// FlashSet stores a flash message in the session. Call from handlers before redirect.
func FlashSet(c *gin.Context, key, msg string) {
	sess := sessions.Default(c)
	sess.Set("flash_"+key, msg)
	_ = sess.Save()
}

// --- CSRF field ---

func csrfField(c *gin.Context) func() template.HTML {
	return func() template.HTML {
		sess := sessions.Default(c)
		tok, _ := sess.Get("_token").(string)
		return template.HTML(fmt.Sprintf(`<input type="hidden" name="_token" value="%s">`, tok))
	}
}
