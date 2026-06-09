package frontend

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/view"
)

type BrandsHandler struct {
	DB     *gorm.DB
	Engine *view.Engine
}

func (h *BrandsHandler) List(c *gin.Context) {
	var brands []models.Brand
	h.DB.Order("name asc").Find(&brands)

	var navCats []models.Category
	h.DB.Where("level = 0").Order("order_level asc, id asc").Limit(8).Find(&navCats)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/brands", gin.H{
		"Brands":     brands,
		"Categories": navCats,
	})
}

func (h *BrandsHandler) Detail(c *gin.Context) {
	slug := c.Param("slug")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	sort := c.DefaultQuery("sort", "newest")

	var brand models.Brand
	err := h.DB.Where("slug = ?", slug).First(&brand).Error
	if err != nil {
		if id, convErr := strconv.Atoi(slug); convErr == nil {
			err = h.DB.First(&brand, id).Error
		}
	}
	if err != nil {
		c.String(http.StatusNotFound, "Brand not found")
		return
	}

	q := h.DB.Model(&models.Product{}).
		Where("brand_id = ? AND published = 1 AND approved = 1", brand.ID).
		Where(inStockSQL)
	q = applySortProducts(q, sort)

	var total int64
	q.Count(&total)

	var products []models.Product
	q.Offset((page - 1) * perPage).Limit(perPage).Find(&products)

	var navCats []models.Category
	h.DB.Where("level = 0").Order("order_level asc, id asc").Limit(8).Find(&navCats)

	totalPages := int((total + perPage - 1) / perPage)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/brand", gin.H{
		"Brand":      brand,
		"Products":   products,
		"Categories": navCats,
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
		"Sort":       sort,
	})
}
