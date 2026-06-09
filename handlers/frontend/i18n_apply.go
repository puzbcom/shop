package frontend

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
)

// langOf returns the active locale code for the request ("" or "en" means base).
func langOf(c *gin.Context) string {
	l, _ := c.Get("locale")
	s, _ := l.(string)
	return s
}

// applyCategoryTranslation swaps a category's Name to the given language if a
// translation row is present (requires Translations preloaded).
func applyCategoryTranslation(cat *models.Category, lang string) {
	if lang == "" || lang == "en" {
		return
	}
	for _, tr := range cat.Translations {
		if tr.Lang == lang && tr.Name != "" {
			cat.Name = tr.Name
			return
		}
	}
}

func applyCategoryTranslations(cats []models.Category, lang string) {
	if lang == "" || lang == "en" {
		return
	}
	for i := range cats {
		applyCategoryTranslation(&cats[i], lang)
	}
}

// applyProductTranslations applies applyProductTranslation to a slice (requires
// Translations preloaded on each product).
func applyProductTranslations(ps []models.Product, lang string) {
	if lang == "" || lang == "en" {
		return
	}
	for i := range ps {
		applyProductTranslation(&ps[i], lang)
	}
}

// navTranslatedCats loads the top-level menu categories with their names
// translated for the current request locale.
func navTranslatedCats(db *gorm.DB, c *gin.Context) []models.Category {
	var cats []models.Category
	db.Preload("Translations").Where("level = 0").Order("order_level asc, id asc").Limit(8).Find(&cats)
	applyCategoryTranslations(cats, langOf(c))
	return cats
}
