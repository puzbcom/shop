// Package view provides the html/template rendering engine for the mall application.
// It replicates Blade's @extends/@section/@yield pattern using Go named templates and
// registers a FuncMap of helpers matching the PHP frontend helpers (get_setting, translate,
// format_price, route, uploaded_asset, etc.).
package view

import (
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/services/i18n"
	"mall/internal/services/settings"
	"mall/internal/social"
)

// Engine holds parsed template sets and the shared FuncMap.
type Engine struct {
	db          *gorm.DB
	settings    *settings.Store
	i18n        *i18n.Service
	root        string // path to web/templates/
	isDev       bool
	activeTheme string // frozen at startup, never modified at runtime

	mu    sync.RWMutex
	cache map[string]*template.Template // cache key -> parsed template set
}

// New creates an Engine. root should be the absolute path to web/templates/.
// The active theme is read once from settings and frozen for the process lifetime.
func New(db *gorm.DB, st *settings.Store, i18nSvc *i18n.Service, root string, isDev bool) *Engine {
	theme := st.Get("homepage_select", "etsy")
	if theme == "" {
		theme = "etsy"
	}
	e := &Engine{
		db:          db,
		settings:    st,
		i18n:        i18nSvc,
		root:        root,
		isDev:       isDev,
		activeTheme: theme, // read-only after this point
		cache:       make(map[string]*template.Template),
	}
	if !isDev {
		e.preload() // pre-parses all layouts including the active theme
	}
	return e
}

// Theme returns the active frontend layout name, set once at startup.
// This is an O(1) field read — no I/O, no locking.
func (e *Engine) Theme() string { return e.activeTheme }

// funcMap builds the template function map. Called per-request so locale is current.
func (e *Engine) funcMap(c *gin.Context) template.FuncMap {
	locale, _ := c.Get("locale")
	lang, _ := locale.(string)
	if lang == "" {
		lang = "en"
	}
	language, _ := c.Get("language")
	langModel, _ := language.(models.Language)

	return template.FuncMap{
		// translate / __
		"translate": func(key string) string { return e.i18n.Translate(key, lang) },
		"__":        func(key string) string { return e.i18n.Translate(key, lang) },

		// get_setting
		"get_setting": func(key string, defs ...string) string {
			if len(defs) > 0 {
				return e.settings.Get(key, defs[0])
			}
			return e.settings.Get(key)
		},

		// uploaded_asset: returns URL for an upload ID or path
		"uploaded_asset": func(v interface{}) string {
			return uploadedAsset(e.db, v)
		},

		// static_asset: /static/... URL
		"static_asset": func(path string) string {
			return "/static/" + strings.TrimPrefix(path, "/")
		},

		// route: named route URL helper
		"route": func(name string, params ...interface{}) string {
			return resolveRoute(name, params...)
		},

		// format_price
		"format_price": func(amount interface{}) template.HTML {
			return template.HTML(formatPrice(e.settings, amount))
		},
		"single_price": func(amount interface{}) string {
			return singlePrice(e.settings, amount)
		},

		// Language helpers
		"current_lang": func() string { return lang },
		"is_rtl":       func() bool { return langModel.RTL == 1 },
		"language":     func() models.Language { return langModel },
		"get_languages": func() []models.Language {
			var langs []models.Language
			e.db.Where("status = 1").Order("id asc").Find(&langs)
			return langs
		},
		"nav_menu_items": func() []models.MenuItem {
			var items []models.MenuItem
			e.db.Order("sort_order asc, id asc").Find(&items)
			return items
		},

		// Social media: the single ordered list of platforms shown in admin
		// (form fields) and footer (rendered icons, hidden when blank).
		"list_socials": func() []social.Platform { return social.Platforms },

		// hex2rgba
		"hex2rgba": func(hex string, alpha float64) string {
			return hex2rgba(hex, alpha)
		},

		// CSRF helpers
		"csrf_token": func() string {
			sess := sessions.Default(c)
			tok, _ := sess.Get("_token").(string)
			return tok
		},
		"csrf_field": csrfField(c),

		// True when an admin is currently impersonating another user.
		"is_impersonating": func() bool {
			sess := sessions.Default(c)
			id, ok := sess.Get("impersonator_id").(uint)
			return ok && id > 0
		},

		// Basic helpers
		"safe_html": func(s string) template.HTML { return template.HTML(s) },
		"safe_url":  func(s string) template.URL { return template.URL(s) },
		"safe_js":   func(s string) template.JS { return template.JS(s) },
		"add":     func(a, b int) int { return a + b },
		"iterate": func(n int) []int {
			s := make([]int, n)
			for i := range s {
				s[i] = i
			}
			return s
		},
		"sub":       func(a, b int) int { return a - b },
		"addf":      func(a, b float64) float64 { return a + b },
		"subf":      func(a, b float64) float64 { return a - b },
		"mul":       func(a, b float64) float64 { return a * b },
		"div": func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"percent": func(part, total float64) float64 {
			if total == 0 {
				return 0
			}
			return part / total * 100
		},
		"date_format": func(t interface{}, layout string) string {
			switch v := t.(type) {
			case time.Time:
				return v.Format(layout)
			case *time.Time:
				if v == nil {
					return ""
				}
				return v.Format(layout)
			}
			return ""
		},
		"now":         func() time.Time { return time.Now() },
		"unix_to_time": func(v interface{}) time.Time {
			switch ts := v.(type) {
			case int:
				return time.Unix(int64(ts), 0)
			case *int:
				if ts == nil {
					return time.Time{}
				}
				return time.Unix(int64(*ts), 0)
			case int64:
				return time.Unix(ts, 0)
			}
			return time.Time{}
		},
		"seq": func(n int) []int {
			s := make([]int, n)
			for i := range s {
				s[i] = i + 1
			}
			return s
		},
		"hasPermission": func(permName string) bool {
			u, exists := c.Get("user")
			if !exists {
				return false
			}
			return userHasPermission(e.db, u.(*models.User), permName)
		},
		"current_user": func() *models.User {
			u, _ := c.Get("user")
			if u == nil {
				return nil
			}
			return u.(*models.User)
		},
		"is_authenticated": func() bool {
			_, exists := c.Get("user")
			return exists
		},
		// active_theme returns the live frontend theme (frozen at startup).
		"active_theme": func() string { return e.activeTheme },
		"cart_count": func() int64 {
			q := e.db.Model(&models.Cart{}).Where("status = 1")
			if u, ok := c.Get("user"); ok {
				uid := u.(*models.User).ID
				q = q.Where("user_id = ?", uid)
			} else {
				sess := sessions.Default(c)
				tempID, _ := sess.Get("cart_temp_id").(string)
				if tempID == "" {
					return 0
				}
				q = q.Where("temp_user_id = ?", tempID)
			}
			var n int64
			q.Count(&n)
			return n
		},
		"url_path":      func() string { return c.Request.URL.Path },
		"query":         func(key string) string { return c.Query(key) },
		"flash_success": func() string { return flashGet(c, "success") },
		"flash_error":   func() string { return flashGet(c, "error") },
		"flash_info":    func() string { return flashGet(c, "info") },
		"flash_warning": func() string { return flashGet(c, "warning") },
		"first_char": func(s interface{}) string {
			var str string
			switch v := s.(type) {
			case string:
				str = v
			case *string:
				if v != nil {
					str = *v
				}
			}
			for _, r := range str {
				return string(r)
			}
			return "?"
		},
		"deref_str": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
		"deref_float": func(f *float64) float64 {
			if f == nil {
				return 0
			}
			return *f
		},
		"deref_int": func(i *int) int {
			if i == nil {
				return 0
			}
			return *i
		},
		"str_default": func(s *string, def string) string {
			if s == nil || *s == "" {
				return def
			}
			return *s
		},
		"not_nil": func(v interface{}) bool {
			return v != nil
		},
		"join": func(s []string, sep string) string {
			return strings.Join(s, sep)
		},
		"split_comma": func(s string) []string {
			return strings.Split(s, ",")
		},
		"round_int": func(f float64) int { return int(f + 0.5) },
		"deref_uint": func(u *uint) uint {
			if u == nil {
				return 0
			}
			return *u
		},
		"deref_time": func(t *time.Time) time.Time {
			if t == nil {
				return time.Time{}
			}
			return *t
		},
		// sfStatusBadge maps a SupplierOrder status to a Bootstrap badge colour.
		"sfStatusBadge": func(status string) string {
			switch status {
			case "delivered":
				return "success"
			case "shipped":
				return "info"
			case "ordered":
				return "primary"
			case "cancelled":
				return "danger"
			case "exception":
				return "warning"
			default: // pending
				return "secondary"
			}
		},
		"itof":  func(i int) float64 { return float64(i) },
		"uitof": func(i uint) float64 { return float64(i) },
		"mulf":  func(a, b float64) float64 { return a * b },
		"int_to_float": func(i interface{}) float64 {
			switch v := i.(type) {
			case int:
				return float64(v)
			case int64:
				return float64(v)
			case uint:
				return float64(v)
			case uint64:
				return float64(v)
			case float64:
				return v
			case *int:
				if v != nil {
					return float64(*v)
				}
			case *float64:
				if v != nil {
					return *v
				}
			}
			return 0
		},
	}
}

// Render executes a named template set identified by layout + page.
func (e *Engine) Render(c *gin.Context, status int, layout, page string, data gin.H) {
	t, err := e.getTemplate(c, layout, page)
	if err != nil {
		log.Printf("view: template error layout=%s page=%s: %v", layout, page, err)
		c.String(http.StatusInternalServerError, "template error: %v", err)
		return
	}
	if data == nil {
		data = gin.H{}
	}
	if _, ok := data["Settings"]; !ok {
		data["Settings"] = e.settings
	}

	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	// Execute the layout (which {{template "content" .}} into itself)
	if err := t.ExecuteTemplate(c.Writer, "layouts/"+layout, data); err != nil {
		log.Printf("view: execute error layout=%s page=%s: %v", layout, page, err)
	}
}

// RenderPartial executes a specific named template (for HTMX partial responses).
func (e *Engine) RenderPartial(c *gin.Context, status int, layout, page, name string, data gin.H) {
	t, err := e.getTemplate(c, layout, page)
	if err != nil {
		c.String(http.StatusInternalServerError, "template error: %v", err)
		return
	}
	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, name, data); err != nil {
		log.Printf("view: execute error name=%s: %v", name, err)
	}
}

func (e *Engine) getTemplate(c *gin.Context, layout, page string) (*template.Template, error) {
	key := layout + "|" + page
	if !e.isDev {
		e.mu.RLock()
		cached, ok := e.cache[key]
		e.mu.RUnlock()
		if ok {
			// Clone so we don't mutate the shared cached template with per-request functions.
			t, err := cached.Clone()
			if err != nil {
				return nil, err
			}
			return t.Funcs(e.funcMap(c)), nil
		}
	}
	t, err := e.parse(c, layout, page)
	if err != nil {
		return nil, err
	}
	if !e.isDev {
		// Cache a fresh clone so the stored copy stays unexecuted and can be
		// cloned again on future requests. Returning t directly means it will
		// be executed by the caller; storing the same pointer would make
		// cached.Clone() fail with "cannot Clone after it has executed".
		if cacheEntry, cerr := t.Clone(); cerr == nil {
			e.mu.Lock()
			e.cache[key] = cacheEntry
			e.mu.Unlock()
		}
	}
	return t, nil
}

func (e *Engine) parse(c *gin.Context, layout, page string) (*template.Template, error) {
	fm := e.funcMap(c)
	t := template.New("").Funcs(fm)

	files, err := e.collectFiles(layout, page)
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		name := templateName(e.root, f)
		if _, err := t.New(name).Parse(string(content)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
	}
	return t, nil
}

// collectFiles returns the ordered list of template files for a layout+page combination.
func (e *Engine) collectFiles(layout, page string) ([]string, error) {
	var files []string

	layoutFile := filepath.Join(e.root, "layouts", layout+".html")
	if _, err := os.Stat(layoutFile); err != nil {
		return nil, fmt.Errorf("layout not found: %s", layoutFile)
	}
	files = append(files, layoutFile)

	// Shared partials directory
	partialsDir := filepath.Join(e.root, "partials")
	if infos, err := os.ReadDir(partialsDir); err == nil {
		for _, fi := range infos {
			if !fi.IsDir() && strings.HasSuffix(fi.Name(), ".html") {
				files = append(files, filepath.Join(partialsDir, fi.Name()))
			}
		}
	}

	// Page-section partials directory (e.g. web/templates/frontend/partials/)
	pageFile := filepath.Join(e.root, page+".html")
	if _, err := os.Stat(pageFile); err != nil {
		return nil, fmt.Errorf("page not found: %s", pageFile)
	}
	pageDir := filepath.Dir(pageFile)
	pagePartialsDir := filepath.Join(pageDir, "partials")
	if infos, err := os.ReadDir(pagePartialsDir); err == nil {
		for _, fi := range infos {
			if !fi.IsDir() && strings.HasSuffix(fi.Name(), ".html") {
				files = append(files, filepath.Join(pagePartialsDir, fi.Name()))
			}
		}
	}

	files = append(files, pageFile)
	return files, nil
}

// preload walks the templates dir to warm-cache at startup.
func (e *Engine) preload() {
	_ = filepath.WalkDir(e.root, func(_ string, _ fs.DirEntry, _ error) error { return nil })
}

func templateName(root, file string) string {
	rel, _ := filepath.Rel(root, file)
	name := strings.ReplaceAll(rel, `\`, "/")
	return strings.TrimSuffix(name, ".html")
}
