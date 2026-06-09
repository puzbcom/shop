package frontend

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"mall/internal/models"
	"mall/internal/view"
)

type ProductsHandler struct {
	DB     *gorm.DB
	Engine *view.Engine
}

const perPage = 20

// inStockSQL is the WHERE clause that hides out-of-stock items from all listing
// pages. Digital and auction products are always shown since they don't have
// physical inventory; everything else requires current_stock > 0.
const inStockSQL = "(digital = 1 OR auction_product = 1 OR current_stock > 0)"

// List serves GET /products and GET /search
func (h *ProductsHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	sort := c.DefaultQuery("sort", "newest")
	search := c.Query("q")
	catID := c.Query("category")

	q := h.DB.Model(&models.Product{}).Where("published = 1 AND approved = 1").Where(inStockSQL)
	if search != "" {
		q = q.Where("name LIKE ?", "%"+search+"%")
	}
	if catID != "" {
		q = q.Where("category_id = ?", catID)
	}
	q = applySortProducts(q, sort)

	var total int64
	q.Count(&total)

	lang := langOf(c)
	var products []models.Product
	q.Preload("Translations").Offset((page - 1) * perPage).Limit(perPage).Find(&products)
	applyProductTranslations(products, lang)

	navCats := navTranslatedCats(h.DB, c)

	var sidebarCats []models.Category
	h.DB.Preload("Translations").Where("level = 0").Order("order_level asc, id asc").Find(&sidebarCats)
	applyCategoryTranslations(sidebarCats, lang)

	totalPages := int((total + perPage - 1) / perPage)

	title := "All Products"
	if search != "" {
		title = "Search: " + search
	}

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/products", gin.H{
		"Products":    products,
		"Categories":  navCats,
		"Sidebar":     sidebarCats,
		"Total":       total,
		"Page":        page,
		"TotalPages":  totalPages,
		"Sort":        sort,
		"Search":      search,
		"ActiveCatID": catID,
		"PageTitle":   title,
	})
}

// Category serves GET /category/:slug
func (h *ProductsHandler) Category(c *gin.Context) {
	slug := c.Param("slug")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	sort := c.DefaultQuery("sort", "newest")

	lang := langOf(c)
	var cat models.Category
	err := h.DB.Preload("Translations").Where("slug = ?", slug).First(&cat).Error
	if err != nil {
		// fall back to numeric ID
		if id, convErr := strconv.Atoi(slug); convErr == nil {
			err = h.DB.Preload("Translations").First(&cat, id).Error
		}
	}
	if err != nil {
		c.String(http.StatusNotFound, "Category not found")
		return
	}
	applyCategoryTranslation(&cat, lang)

	// collect this category + direct children IDs
	var childIDs []uint
	h.DB.Model(&models.Category{}).
		Where("parent_id = ?", cat.ID).
		Pluck("id", &childIDs)
	allCatIDs := append([]uint{cat.ID}, childIDs...)

	q := h.DB.Model(&models.Product{}).
		Where("published = 1 AND approved = 1").
		Where(inStockSQL).
		Where("category_id IN ?", allCatIDs)
	q = applySortProducts(q, sort)

	var total int64
	q.Count(&total)

	var products []models.Product
	q.Preload("Translations").Offset((page - 1) * perPage).Limit(perPage).Find(&products)
	applyProductTranslations(products, lang)

	navCats := navTranslatedCats(h.DB, c)

	var sidebarCats []models.Category
	h.DB.Preload("Translations").Where("level = 0").Order("order_level asc, id asc").Find(&sidebarCats)
	applyCategoryTranslations(sidebarCats, lang)

	totalPages := int((total + perPage - 1) / perPage)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/products", gin.H{
		"Products":    products,
		"Categories":  navCats,
		"Sidebar":     sidebarCats,
		"ActiveCat":   &cat,
		"ActiveCatID": strconv.Itoa(int(cat.ID)),
		"Total":       total,
		"Page":        page,
		"TotalPages":  totalPages,
		"Sort":        sort,
		"Search":      "",
		"PageTitle":   cat.Name,
	})
}

// Detail serves GET /product/:slug
func (h *ProductsHandler) Detail(c *gin.Context) {
	slug := c.Param("slug")
	locale, _ := c.Get("locale")
	lang, _ := locale.(string)

	var product models.Product
	err := h.DB.Where("slug = ? AND published = 1 AND approved = 1", slug).
		Preload("Category").
		Preload("Brand").
		Preload("Translations").
		Preload("Stocks").
		Preload("Reviews", func(db *gorm.DB) *gorm.DB {
			return db.Where("status = 1").Order("id desc").Limit(10)
		}).
		Preload("Reviews.User").
		First(&product).Error
	if err != nil {
		c.String(http.StatusNotFound, "Product not found")
		return
	}
	if lang != "" && lang != "en" {
		applyProductTranslation(&product, lang)
	}

	// Compute review statistics from loaded reviews.
	reviewCount := len(product.Reviews)
	reviewAvg := product.Rating
	starCounts := [6]int{} // index 1-5
	if reviewCount > 0 {
		sum := 0
		for _, r := range product.Reviews {
			sum += r.Rating
			if r.Rating >= 1 && r.Rating <= 5 {
				starCounts[r.Rating]++
			}
		}
		reviewAvg = float64(sum) / float64(reviewCount)
	}
	// Bar widths as percentages (5 down to 1).
	barPcts := [5]float64{}
	for i := 0; i < 5; i++ {
		star := 5 - i
		if reviewCount > 0 {
			barPcts[i] = float64(starCounts[star]) / float64(reviewCount) * 100
		}
	}

	var related []models.Product
	h.DB.Where("category_id = ? AND id != ? AND published = 1 AND approved = 1", product.CategoryID, product.ID).
		Where(inStockSQL).
		Order("num_of_sale desc").
		Limit(5).
		Find(&related)

	// Products from this seller (same user_id, excluding current product).
	var sellerProducts []models.Product
	h.DB.Where("user_id = ? AND id != ? AND published = 1 AND approved = 1", product.UserID, product.ID).
		Where(inStockSQL).
		Order("num_of_sale desc, id desc").
		Limit(12).
		Find(&sellerProducts)

	// Related searches: more products from the same category with thumbnails, for search cards.
	var relatedSearches []models.Product
	h.DB.Where("category_id = ? AND id != ? AND published = 1 AND approved = 1 AND thumbnail_img != ''", product.CategoryID, product.ID).
		Where(inStockSQL).
		Order("num_of_sale desc").
		Limit(6).
		Find(&relatedSearches)

	// Review highlights: top frequent meaningful words from review comments.
	reviewHighlights := extractHighlights(product.Reviews)

	// Buyers recommend: % of reviews with rating >= 4.
	buyersRecommend := 0
	if reviewCount > 0 {
		pos := 0
		for _, r := range product.Reviews {
			if r.Rating >= 4 {
				pos++
			}
		}
		buyersRecommend = pos * 100 / reviewCount
	}

	var navCats []models.Category
	h.DB.Where("level = 0").Order("order_level asc, id asc").Limit(8).Find(&navCats)

	var seoMeta models.SEOMeta
	h.DB.Where("model_type = ? AND model_id = ?", "product", product.ID).First(&seoMeta)

	// Load auction bids and pre-compute values needed by the template.
	var auctionBids []models.AuctionBid
	var topBid *models.AuctionBid
	auctionEnded := false
	var auctionEndUnix int64
	if product.AuctionProduct == 1 {
		h.DB.Where("product_id = ?", product.ID).
			Preload("User").
			Order("amount desc").
			Limit(10).
			Find(&auctionBids)
		if len(auctionBids) > 0 {
			topBid = &auctionBids[0]
		}
		if product.AuctionEndAt != nil {
			auctionEndUnix = product.AuctionEndAt.Unix()
			auctionEnded = time.Now().After(*product.AuctionEndAt)
		}
	}

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/product", gin.H{
		"Product":        product,
		"Related":        related,
		"Categories":     navCats,
		"SEO":            &seoMeta,
		"AuctionBids":    auctionBids,
		"TopBid":         topBid,
		"AuctionEnded":   auctionEnded,
		"AuctionEndUnix": auctionEndUnix,
		"ReviewAvg":        reviewAvg,
		"ReviewCount":      reviewCount,
		"BarPcts":          barPcts,
		"SellerProducts":   sellerProducts,
		"RelatedSearches":  relatedSearches,
		"ReviewHighlights": reviewHighlights,
		"BuyersRecommend":  buyersRecommend,
	})
}

// Bid handles POST /product/:slug/bid — places a new bid on an auction product.
func (h *ProductsHandler) Bid(c *gin.Context) {
	slug := c.Param("slug")

	u, exists := c.Get("user")
	if !exists {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	user := u.(*models.User)

	var product models.Product
	if err := h.DB.Where("slug = ? AND published = 1 AND approved = 1 AND auction_product = 1", slug).First(&product).Error; err != nil {
		c.String(http.StatusNotFound, "Auction not found")
		return
	}

	// Prevent sellers from bidding on their own auction products.
	if product.UserID == user.ID {
		c.Redirect(http.StatusFound, "/product/"+slug+"?bid_error=own_product")
		return
	}

	// Check auction has not ended. Reject bids submitted at or after the
	// official end time — !Before excludes the exact end instant.
	if product.AuctionEndAt != nil && !time.Now().Before(*product.AuctionEndAt) {
		c.Redirect(http.StatusFound, "/product/"+slug+"?bid_error=ended")
		return
	}

	amountStr := c.PostForm("amount")
	amount, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amount <= 0 {
		c.Redirect(http.StatusFound, "/product/"+slug+"?bid_error=invalid")
		return
	}

	floor := 0.0
	if product.AuctionStartPrice != nil {
		floor = *product.AuctionStartPrice
	}
	increment := 0.0
	if product.AuctionMinBidIncrement != nil {
		increment = *product.AuctionMinBidIncrement
	}

	// Atomic bid insert: lock the product row, re-read the current top bid
	// under the lock, validate the new bid against floor + increment, then
	// insert — prevents two concurrent bidders from racing past the same
	// stale minRequired and both succeeding at an identical amount.
	var minRequired float64
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		// Lock product row to serialise bidders on the same auction.
		var locked models.Product
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", product.ID).First(&locked).Error; err != nil {
			return err
		}
		// Re-check end time under the lock so a tick that crosses the
		// boundary while we waited can't sneak through.
		if locked.AuctionEndAt != nil && !time.Now().Before(*locked.AuctionEndAt) {
			return errAuctionEnded
		}
		var topBid models.AuctionBid
		tx.Where("product_id = ?", product.ID).Order("amount desc").First(&topBid)
		minRequired = floor
		if topBid.ID > 0 {
			minRequired = topBid.Amount + increment
		}
		if amount < minRequired {
			return errBidTooLow
		}
		return tx.Create(&models.AuctionBid{
			ProductID: product.ID,
			UserID:    user.ID,
			Amount:    amount,
		}).Error
	})
	if txErr == errAuctionEnded {
		c.Redirect(http.StatusFound, "/product/"+slug+"?bid_error=ended")
		return
	}
	if txErr == errBidTooLow {
		c.Redirect(http.StatusFound, fmt.Sprintf("/product/%s?bid_error=low&min=%.2f", slug, minRequired))
		return
	}
	if txErr != nil {
		c.Redirect(http.StatusFound, "/product/"+slug+"?bid_error=server")
		return
	}

	c.Redirect(http.StatusFound, "/product/"+slug+"?bid_ok=1")
}

var (
	errAuctionEnded = fmt.Errorf("auction ended")
	errBidTooLow    = fmt.Errorf("bid too low")
)

func applyProductTranslation(p *models.Product, lang string) {
	for _, tr := range p.Translations {
		if tr.Lang != lang {
			continue
		}
		if tr.Name != nil && *tr.Name != "" {
			p.Name = *tr.Name
		}
		if tr.Description != nil {
			p.Description = tr.Description
		}
		return
	}
}

func applySortProducts(q *gorm.DB, sort string) *gorm.DB {
	switch sort {
	case "popular":
		return q.Order("num_of_sale desc")
	case "price_asc":
		return q.Order("unit_price asc")
	case "price_desc":
		return q.Order("unit_price desc")
	case "rating":
		return q.Order("rating desc")
	default:
		return q.Order("id desc")
	}
}

// extractHighlights picks the top 8 frequent meaningful words from review comments.
func extractHighlights(reviews []models.Review) []string {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
		"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
		"with": true, "is": true, "it": true, "i": true, "this": true, "that": true,
		"was": true, "my": true, "so": true, "very": true, "are": true, "have": true,
		"be": true, "as": true, "from": true, "by": true, "its": true, "not": true,
	}
	freq := make(map[string]int)
	for _, r := range reviews {
		for _, w := range strings.Fields(strings.ToLower(r.Comment)) {
			w = strings.Trim(w, ".,!?;:\"'()")
			if len(w) >= 4 && !stopWords[w] {
				freq[w]++
			}
		}
	}
	type kv struct{ k string; v int }
	var sorted []kv
	for k, v := range freq {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
	var out []string
	for i, item := range sorted {
		if i >= 8 { break }
		// Title-case the word
		out = append(out, strings.Title(item.k))
	}
	return out
}
