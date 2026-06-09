package frontend

import (
	"net/http"
	"strconv"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/view"
)

type PagesHandler struct {
	DB     *gorm.DB
	Engine *view.Engine
}

func (h *PagesHandler) navCats(c *gin.Context) []models.Category {
	return navTranslatedCats(h.DB, c)
}

// Categories — /categories
func (h *PagesHandler) Categories(c *gin.Context) {
	var topLevel []models.Category
	h.DB.Preload("Translations").Where("level = 0").Order("order_level asc, id asc").Find(&topLevel)
	applyCategoryTranslations(topLevel, langOf(c))

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/categories", gin.H{
		"TopCategories": topLevel,
		"Categories":    h.navCats(c),
	})
}

// Brands listing — /brands
// (handled by BrandsHandler; this is kept for PagesHandler parity)

// FlashDeal — /flash-deals/:id
func (h *PagesHandler) FlashDeal(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.String(http.StatusNotFound, "Flash deal not found")
		return
	}

	var deal models.FlashDeal
	if err := h.DB.Where("status = 1").First(&deal, id).Error; err != nil {
		// try any deal if id doesn't match
		if err2 := h.DB.Where("status = 1").Order("id desc").First(&deal).Error; err2 != nil {
			c.String(http.StatusNotFound, "No active flash deals")
			return
		}
	}

	var fdps []models.FlashDealProduct
	h.DB.Where("flash_deal_id = ?", deal.ID).Find(&fdps)

	productIDs := make([]uint, 0, len(fdps))
	for _, fp := range fdps {
		productIDs = append(productIDs, fp.ProductID)
	}

	var products []models.Product
	if len(productIDs) > 0 {
		h.DB.Where("id IN ? AND published = 1 AND approved = 1", productIDs).Where(inStockSQL).Find(&products)
	}

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/flash_deal", gin.H{
		"Deal":       deal,
		"Products":   products,
		"Categories": h.navCats(c),
	})
}

// Contact — GET /contact
func (h *PagesHandler) Contact(c *gin.Context) {
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/contact", gin.H{
		"Categories": h.navCats(c),
		"Success":    c.Query("sent") == "1",
	})
}

// ContactSubmit — POST /contact
func (h *PagesHandler) ContactSubmit(c *gin.Context) {
	name := c.PostForm("name")
	email := c.PostForm("email")
	message := c.PostForm("message")

	// Validate required fields
	if name == "" || email == "" || message == "" {
		h.Engine.Render(c, http.StatusBadRequest, h.Engine.Theme(), "frontend/contact", gin.H{
			"Categories": h.navCats(c),
			"Error":      "Please fill in all fields",
			"FormName":   name,
			"FormEmail":  email,
			"FormMsg":    message,
		})
		return
	}

	contact := models.Contact{
		Name:    name,
		Email:   email,
		Content: message,
	}

	// Save to database and check for errors
	if err := h.DB.Create(&contact).Error; err != nil {
		h.Engine.Render(c, http.StatusInternalServerError, h.Engine.Theme(), "frontend/contact", gin.H{
			"Categories": h.navCats(c),
			"Error":      "Failed to save message. Please try again.",
			"FormName":   name,
			"FormEmail":  email,
			"FormMsg":    message,
		})
		return
	}

	c.Redirect(http.StatusSeeOther, "/contact?sent=1")
}

// FAQ — /faq
func (h *PagesHandler) FAQ(c *gin.Context) {
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/faq", gin.H{
		"Categories": h.navCats(c),
	})
}

// CMS page — /page/:slug
func (h *PagesHandler) Page(c *gin.Context) {
	slug := c.Param("slug")

	var page models.Page
	if err := h.DB.Where("slug = ?", slug).First(&page).Error; err != nil {
		c.String(http.StatusNotFound, "Page not found")
		return
	}

	// Apply per-locale translation overlay if one exists for this page.
	if locale, ok := c.Get("locale"); ok {
		if lang, _ := locale.(string); lang != "" && lang != "en" {
			var tr models.PageTranslation
			if err := h.DB.Where("page_id = ? AND lang = ?", page.ID, lang).First(&tr).Error; err == nil {
				if tr.Title != nil && *tr.Title != "" {
					page.Title = tr.Title
				}
				if tr.Content != nil && *tr.Content != "" {
					page.Content = tr.Content
				}
				if tr.MetaTitle != nil && *tr.MetaTitle != "" {
					page.MetaTitle = tr.MetaTitle
				}
				if tr.MetaDescription != nil && *tr.MetaDescription != "" {
					page.MetaDescription = tr.MetaDescription
				}
				if tr.Keywords != nil && *tr.Keywords != "" {
					page.Keywords = tr.Keywords
				}
			}
		}
	}

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/cms_page", gin.H{
		"CMSPage":    page,
		"Categories": h.navCats(c),
	})
}

// Blogs listing — /blogs
func (h *PagesHandler) Blogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	locale, _ := c.Get("locale")
	lang, _ := locale.(string)

	var total int64
	h.DB.Model(&models.Blog{}).Where("status = 1 AND deleted_at IS NULL").Count(&total)

	var blogs []models.Blog
	h.DB.Where("status = 1 AND deleted_at IS NULL").Order("id desc").
		Preload("Translations").
		Offset((page - 1) * 12).Limit(12).Find(&blogs)

	if lang != "" && lang != "en" {
		for i := range blogs {
			applyBlogTranslation(&blogs[i], lang)
		}
	}

	totalPages := int((total + 11) / 12)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/blogs", gin.H{
		"Blogs":      blogs,
		"Categories": h.navCats(c),
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
	})
}

// Blog detail — /blog/:slug
func (h *PagesHandler) BlogDetail(c *gin.Context) {
	slug := c.Param("slug")
	locale, _ := c.Get("locale")
	lang, _ := locale.(string)

	var blog models.Blog
	if err := h.DB.Preload("Translations").Where("slug = ? AND status = 1 AND deleted_at IS NULL", slug).First(&blog).Error; err != nil {
		c.String(http.StatusNotFound, "Blog post not found")
		return
	}
	if lang != "" && lang != "en" {
		applyBlogTranslation(&blog, lang)
	}

	var recent []models.Blog
	h.DB.Where("id != ? AND status = 1 AND deleted_at IS NULL", blog.ID).Order("id desc").Limit(4).Find(&recent)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/blog", gin.H{
		"Blog":       blog,
		"Recent":     recent,
		"Categories": h.navCats(c),
	})
}

// SetLanguage — GET /set-language?lang=xx
// Saves the chosen language code into the session and redirects back.
func (h *PagesHandler) SetLanguage(c *gin.Context) {
	code := c.Query("lang")
	if code != "" {
		var lang models.Language
		if err := h.DB.Where("code = ? AND status = 1", code).First(&lang).Error; err == nil {
			sess := sessions.Default(c)
			sess.Set("locale", code)
			_ = sess.Save()
		}
	}
	ref := c.GetHeader("Referer")
	if ref == "" {
		ref = "/"
	}
	c.Redirect(http.StatusSeeOther, ref)
}

func applyBlogTranslation(blog *models.Blog, lang string) {
	for _, tr := range blog.Translations {
		if tr.Lang != lang {
			continue
		}
		if tr.Title != "" {
			blog.Title = tr.Title
		}
		if tr.ShortDescription != nil {
			blog.ShortDescription = tr.ShortDescription
		}
		if tr.Description != nil {
			blog.Description = tr.Description
		}
		if tr.MetaTitle != nil {
			blog.MetaTitle = tr.MetaTitle
		}
		if tr.MetaDescription != nil {
			blog.MetaDescription = tr.MetaDescription
		}
		if tr.MetaKeywords != nil {
			blog.MetaKeywords = tr.MetaKeywords
		}
		return
	}
}
