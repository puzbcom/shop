package frontend

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/services/settings"
	"mall/internal/view"
)

type EtsyHandler struct {
	DB       *gorm.DB
	Engine   *view.Engine
	Settings *settings.Store
}

func (h *EtsyHandler) Index(c *gin.Context) {
	// Fetch hero slides from business settings
	type Slider struct {
		Photo string
		Text1 string
		Text2 string
		Text3 string
	}
	var sliders []Slider
	if h.Settings != nil {
		for i := 1; i <= 3; i++ {
			slot := strconv.Itoa(i)
			imgKey := "hero_slide_" + slot
			linkKey := "hero_slide_" + slot + "_link"
			imgVal := h.Settings.Get(imgKey, "")
			linkVal := h.Settings.Get(linkKey, "")
			if imgVal != "" {
				sliders = append(sliders, Slider{
					Photo: imgVal,
					Text1: "",
					Text2: "",
					Text3: linkVal,
				})
			}
		}
	}

	lang := langOf(c)

	var categories []models.Category
	h.DB.Preload("Translations").Where("level = 0").Order("order_level asc, id asc").Limit(8).Find(&categories)

	var featuredProducts []models.Product
	h.DB.Preload("Translations").Where("published = 1 AND approved = 1 AND featured = 1").Where(inStockSQL).
		Order("num_of_sale desc").Limit(10).Find(&featuredProducts)

	if len(featuredProducts) == 0 {
		h.DB.Preload("Translations").Where("published = 1 AND approved = 1").Where(inStockSQL).
			Order("num_of_sale desc").Limit(10).Find(&featuredProducts)
	}

	var newArrivals []models.Product
	h.DB.Preload("Translations").Where("published = 1 AND approved = 1").Where(inStockSQL).
		Order("id desc").Limit(5).Find(&newArrivals)

	var bestSellers []models.Product
	h.DB.Preload("Translations").Where("published = 1 AND approved = 1").Where(inStockSQL).
		Order("num_of_sale desc").Limit(5).Find(&bestSellers)

	applyCategoryTranslations(categories, lang)
	applyProductTranslations(featuredProducts, lang)
	applyProductTranslations(newArrivals, lang)
	applyProductTranslations(bestSellers, lang)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/etsy", gin.H{
		"Sliders":          sliders,
		"Categories":      categories,
		"FeaturedProducts": featuredProducts,
		"NewArrivals":     newArrivals,
		"BestSellers":     bestSellers,
	})
}
