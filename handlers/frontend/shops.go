package frontend

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/view"
)

type ShopsHandler struct {
	DB     *gorm.DB
	Engine *view.Engine
}

func (h *ShopsHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}

	var total int64
	h.DB.Model(&models.Shop{}).Count(&total)

	var shops []models.Shop
	h.DB.Order("num_of_sale desc").
		Offset((page - 1) * perPage).Limit(perPage).
		Find(&shops)

	var navCats []models.Category
	h.DB.Where("level = 0").Order("order_level asc, id asc").Limit(8).Find(&navCats)

	totalPages := int((total + perPage - 1) / perPage)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/shops", gin.H{
		"Shops":      shops,
		"Categories": navCats,
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
	})
}

func (h *ShopsHandler) Detail(c *gin.Context) {
	slug := c.Param("slug")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	sort := c.DefaultQuery("sort", "newest")

	var shop models.Shop
	err := h.DB.Where("slug = ?", slug).First(&shop).Error
	if err != nil {
		if id, convErr := strconv.Atoi(slug); convErr == nil {
			err = h.DB.First(&shop, id).Error
		}
	}
	if err != nil {
		c.String(http.StatusNotFound, "Shop not found")
		return
	}

	q := h.DB.Model(&models.Product{}).
		Where("user_id = ? AND published = 1 AND approved = 1", shop.UserID).
		Where(inStockSQL)
	q = applySortProducts(q, sort)

	var total int64
	q.Count(&total)

	var products []models.Product
	q.Offset((page - 1) * perPage).Limit(perPage).Find(&products)

	var navCats []models.Category
	h.DB.Where("level = 0").Order("order_level asc, id asc").Limit(8).Find(&navCats)

	totalPages := int((total + perPage - 1) / perPage)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/shop", gin.H{
		"Shop":       shop,
		"Products":   products,
		"Categories": navCats,
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
		"Sort":       sort,
	})
}
