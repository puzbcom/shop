package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// availableThemes is the fixed list of frontend themes.
// The Key must match a layout file at web/templates/layouts/{key}.html.
var availableThemes = []struct {
	Key   string
	Label string
	Color string // preview swatch color
}{
	{Key: "etsy",       Label: "Etsy (Default)", Color: "#f1641e"},
	{Key: "amazon",     Label: "Amazon",         Color: "#FF9900"},
	{Key: "ebay",       Label: "eBay",           Color: "#E53238"},
	{Key: "ozon",       Label: "Ozon",           Color: "#005BFF"},
	{Key: "aliexpress", Label: "AliExpress",     Color: "#FF4747"},
	{Key: "apple",      Label: "Apple.com",      Color: "#0071E3"},
	{Key: "mi",         Label: "Mi.com",         Color: "#FF6900"},
	{Key: "jd",         Label: "JD.com",         Color: "#C0392B"},
	{Key: "alibaba",    Label: "Alibaba.com",    Color: "#FF6A00"},
	{Key: "walmart",    Label: "Walmart",        Color: "#0071CE"},
	{Key: "vevor",      Label: "Vevor.com",      Color: "#F97316"},
}

// ThemePicker — GET /super-admin/theme
// Shows the theme selection grid. Reads the stored (pending) theme from settings;
// the live/active theme may differ until the next server restart.
func (h *Handler) ThemePicker(c *gin.Context) {
	u := currentUser(c)
	// Show the stored value (what will be active after next restart).
	stored := h.Settings.Get("homepage_select", "etsy")
	// Show what is currently live (frozen at this process's startup).
	live := h.Engine.Theme()

	h.Engine.Render(c, http.StatusOK, "admin", "admin/super_admin_theme", gin.H{
		"User":            u,
		"StoredTheme":     stored,
		"LiveTheme":       live,
		"PendingRestart":  stored != live,
		"AvailableThemes": availableThemes,
		"Success":         c.Query("success"),
		"Error":           c.Query("error"),
	})
}

// ThemeUpdate — POST /super-admin/theme
// Validates and saves the selected theme to the settings store.
// The change takes effect on next server restart (theme is read-only at runtime).
// When called via AJAX (X-Requested-With: XMLHttpRequest), returns JSON instead of redirecting.
func (h *Handler) ThemeUpdate(c *gin.Context) {
	selected := c.PostForm("theme")
	ajax := c.GetHeader("X-Requested-With") == "XMLHttpRequest"

	for _, t := range availableThemes {
		if t.Key == selected {
			if err := h.Settings.Set("homepage_select", selected); err != nil {
				if ajax {
					c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "save_failed"})
					return
				}
				c.Redirect(http.StatusFound, "/super-admin/theme?error=save_failed")
				return
			}
			if ajax {
				c.JSON(http.StatusOK, gin.H{"ok": true, "theme": selected})
				return
			}
			c.Redirect(http.StatusFound, "/super-admin/theme?success=1")
			return
		}
	}

	if ajax {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "invalid_theme"})
		return
	}
	c.Redirect(http.StatusFound, "/super-admin/theme?error=invalid_theme")
}
