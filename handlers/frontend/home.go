package frontend

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/services/settings"
	"mall/internal/view"
)

type HomeHandler struct {
	DB       *gorm.DB
	Engine   *view.Engine
	Settings *settings.Store
}

func (h *HomeHandler) Index(c *gin.Context) {
	// Theme is frozen at startup in Engine.activeTheme; EtsyHandler uses h.Engine.Theme().
	(&EtsyHandler{DB: h.DB, Engine: h.Engine, Settings: h.Settings}).Index(c)
}
