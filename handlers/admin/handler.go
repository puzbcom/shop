package admin

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/services/kuaidi100"
	"mall/internal/services/mail"
	"mall/internal/services/settings"
	"mall/internal/services/translate"
	"mall/internal/social"
	"mall/internal/view"
)

type Handler struct {
	DB           *gorm.DB
	Engine       *view.Engine
	Settings     *settings.Store
	UploadDir    string
	GeminiAPIKey string
	OllamaURL    string
	OllamaModel  string
}

// re1688URL extracts the numeric offer/product ID from a 1688 product URL.
var re1688URL = regexp.MustCompile(`(?:offer|product)[/=](\d+)`)

// ali1688Client is a shared HTTP client used for 1688 API calls and image downloads.
var ali1688Client = &http.Client{Timeout: 30 * time.Second}

func currentUser(c *gin.Context) *models.User {
	return c.MustGet("user").(*models.User)
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}
	return string(hash), nil
}

// ── Dashboard ────────────────────────────────────────────────────────────────

func (h *Handler) Dashboard(c *gin.Context) {
	u := currentUser(c)

	var totalRevenue float64
	h.DB.Model(&models.Order{}).Select("COALESCE(SUM(grand_total),0)").
		Where("payment_status = ?", "paid").Scan(&totalRevenue)

	var orderCount int64
	h.DB.Model(&models.Order{}).Count(&orderCount)

	var productCount int64
	h.DB.Model(&models.Product{}).Count(&productCount)

	var customerCount int64
	h.DB.Model(&models.User{}).Where("user_type = ?", "customer").Count(&customerCount)

	var sellerCount int64
	h.DB.Model(&models.User{}).Where("user_type = ?", "seller").Count(&sellerCount)

	var recentOrders []models.Order
	h.DB.Order("created_at desc").Limit(10).
		Preload("Buyer").Find(&recentOrders)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/dashboard", gin.H{
		"User":          u,
		"TotalRevenue":  totalRevenue,
		"OrderCount":    orderCount,
		"ProductCount":  productCount,
		"CustomerCount": customerCount,
		"SellerCount":   sellerCount,
		"RecentOrders":  recentOrders,
	})
}

// ── Products ─────────────────────────────────────────────────────────────────

func (h *Handler) productList(c *gin.Context, filter string) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	search := c.Query("search")
	limit := 15
	offset := (page - 1) * limit

	var products []models.Product
	var total int64
	q := h.DB.Model(&models.Product{})
	switch filter {
	case "pending":
		q = q.Where("approved = 0")
	case "digital":
		q = q.Where("digital = 1")
	case "auction":
		q = q.Where("auction_product = 1")
	}
	if search != "" {
		q = q.Where("name LIKE ?", "%"+search+"%")
	}
	q.Count(&total)
	q.Order("created_at desc").Limit(limit).Offset(offset).
		Preload("Category").Find(&products)

	var seoList []models.SEOMeta
	productIDs := make([]uint, len(products))
	for i, p := range products {
		productIDs[i] = p.ID
	}
	if len(productIDs) > 0 {
		h.DB.Where("model_type = 'product' AND model_id IN ?", productIDs).Find(&seoList)
	}
	seoScores := make(map[uint]int, len(seoList))
	for _, s := range seoList {
		seoScores[s.ModelID] = s.SEOScore
	}

	h.Engine.Render(c, http.StatusOK, "admin", "admin/products", gin.H{
		"User":      u,
		"Products":  products,
		"Total":     total,
		"Page":      page,
		"Pages":     (int(total) + limit - 1) / limit,
		"Search":    search,
		"Filter":    filter,
		"SEOScores": seoScores,
	})
}

func (h *Handler) ProductList(c *gin.Context)           { h.productList(c, "") }
func (h *Handler) ProductListPending(c *gin.Context)    { h.productList(c, "pending") }
func (h *Handler) ProductListDigital(c *gin.Context)    { h.productList(c, "digital") }
func (h *Handler) ProductListClassified(c *gin.Context) { h.productList(c, "auction") }

func (h *Handler) ProductCreate(c *gin.Context) {
	u := currentUser(c)
	var cats []models.Category
	h.DB.Order("level asc, name asc").Find(&cats)
	var brands []models.Brand
	h.DB.Order("name asc").Find(&brands)
	var langs []models.Language
	h.DB.Where("status = 1 AND code != 'en'").Order("id asc").Find(&langs)
	exchangeRate, _ := strconv.ParseFloat(h.Settings.Get("exchange_rate", "1"), 64)
	priceMarkup, _ := strconv.ParseFloat(h.Settings.Get("price_markup", "0"), 64)
	if exchangeRate <= 0 {
		exchangeRate = 1
	}
	// Provide a default SEOMeta so the seo_panel partial always has a non-nil .SEO.
	defaultSEO := models.SEOMeta{RobotsIndex: 1, RobotsFollow: 1}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/product_form", gin.H{
		"User":          u,
		"Product":       nil,
		"Categories":    cats,
		"Brands":        brands,
		"Languages":     langs,
		"TransMap":      map[string]models.ProductTranslation{},
		"ChoiceOptions": []choiceOption{},
		"ExchangeRate":  exchangeRate,
		"PriceMarkup":   priceMarkup,
		"SEO":           &defaultSEO,
	})
}

// Import1688 handles GET /admin/products/import-1688?url=...
// It calls the 1688 Open API to fetch product details and returns JSON.
func (h *Handler) Import1688(c *gin.Context) {
	const appKey = "7471401"
	const appSecret = "DmX4tg4rjv5n"

	rawURL := strings.TrimSpace(c.Query("url"))
	if rawURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}

	m := re1688URL.FindStringSubmatch(rawURL)
	if m == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Not a valid 1688 product URL"})
		return
	}
	offerID := m[1]

	// Build signed 1688 API request
	apiMethod := "alibaba.product.get"
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	params := map[string]string{
		"app_key":   appKey,
		"method":    apiMethod,
		"timestamp": timestamp,
		"format":    "json",
		"v":         "2",
		"sign_method": "md5",
		"offer_id":  offerID,
	}

	// Sort keys
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build sign string: secret + k1v1k2v2... + secret
	var sb strings.Builder
	sb.WriteString(appSecret)
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(params[k])
	}
	sb.WriteString(appSecret)

	h16 := md5.Sum([]byte(sb.String()))
	sign := strings.ToUpper(fmt.Sprintf("%x", h16))
	params["sign"] = sign

	// Build query string
	qv := url.Values{}
	for k, v := range params {
		qv.Set(k, v)
	}

	apiURL := "https://gw.open.1688.com/openapi/param2/2/alibaba.product/get/" + appKey + "?" + qv.Encode()

	resp, err := ali1688Client.Get(apiURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to call 1688 API: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Parse 1688 API response
	var apiResp map[string]interface{}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Invalid API response", "raw": string(body)})
		return
	}

	// Check for API error
	if errCode, ok := apiResp["error_code"]; ok {
		errMsg, _ := apiResp["error_message"].(string)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("1688 API error %v: %s", errCode, errMsg), "raw": apiResp})
		return
	}

	// Extract product data from response
	result := extract1688Product(apiResp, offerID)

	// Build CPS-tracked source URL
	sourceURL := "https://detail.1688.com/offer/" + offerID + ".html?cps_track=tb7043003467_001"
	result.SourceURL = sourceURL

	// Download images and store them locally.
	// Images that fail to download are dropped entirely; keeping a CDN URL would
	// cause the JS layer to prepend "/uploads/" to it and produce a broken path.
	var goodImages []string
	for _, imgURL := range result.Images {
		if localPath, err := download1688Image(imgURL, h.UploadDir); err == nil {
			goodImages = append(goodImages, localPath)
		} else {
			fmt.Printf("[1688] Failed to download image %s: %v\n", imgURL, err)
		}
	}
	result.Images = goodImages

	if result.Thumbnail != "" {
		if localPath, err := download1688Image(result.Thumbnail, h.UploadDir); err == nil {
			result.Thumbnail = localPath
		} else {
			fmt.Printf("[1688] Failed to download thumbnail %s\n", result.Thumbnail)
			result.Thumbnail = ""
		}
	}

	c.JSON(http.StatusOK, result)
}

type product1688 struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Price       float64       `json:"price"`
	Stock       int           `json:"stock"`
	Thumbnail   string        `json:"thumbnail"`
	Images      []string      `json:"images"`
	Variants    []variant1688 `json:"variants"`
	SourceURL   string        `json:"source_url"` // 1688 URL with CPS tracking
}

type variant1688 struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	Stock int     `json:"stock"`
	SKU   string  `json:"sku"`
}

func extract1688Product(data map[string]interface{}, offerID string) product1688 {
	p := product1688{Variants: []variant1688{}}

	// Navigate: result.offerDetail or productInfo
	result, _ := data["result"].(map[string]interface{})
	if result == nil {
		result = data
	}
	offer, _ := result["offerDetail"].(map[string]interface{})
	if offer == nil {
		offer, _ = result["productInfo"].(map[string]interface{})
	}
	if offer == nil {
		offer = result
	}

	p.Name, _ = offer["subject"].(string)
	if p.Name == "" {
		p.Name, _ = offer["name"].(string)
	}

	// Price
	if priceRange, ok := offer["priceRange"].([]interface{}); ok && len(priceRange) > 0 {
		if pr, ok := priceRange[0].(map[string]interface{}); ok {
			pStr, _ := pr["price"].(string)
			p.Price, _ = strconv.ParseFloat(pStr, 64)
		}
	}
	if p.Price == 0 {
		if priceStr, ok := offer["price"].(string); ok {
			p.Price, _ = strconv.ParseFloat(priceStr, 64)
		}
	}

	// Stock
	if qty, ok := offer["amountOnSale"].(float64); ok {
		p.Stock = int(qty)
	}

	// Images
	if imgList, ok := offer["imageList"].([]interface{}); ok {
		for _, img := range imgList {
			if imgMap, ok := img.(map[string]interface{}); ok {
				if u, ok := imgMap["url"].(string); ok && u != "" {
					if !strings.HasPrefix(u, "http") {
						u = "https:" + u
					}
					p.Images = append(p.Images, u)
				}
			}
		}
	}
	if len(p.Images) > 0 {
		p.Thumbnail = p.Images[0]
		// Gallery starts at index 1 — index 0 is already the thumbnail.
		// Keeping them separate prevents the JS layer from needing to know
		// which index to skip when the thumbnail download fails.
		p.Images = p.Images[1:]
		if len(p.Images) > 7 {
			p.Images = p.Images[:7]
		}
	}

	// Description HTML
	if desc, ok := offer["description"].(string); ok {
		p.Description = desc
	}

	// SKU / variants
	if skuInfos, ok := offer["skuInfos"].([]interface{}); ok {
		for _, s := range skuInfos {
			sm, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			v := variant1688{}
			v.SKU, _ = sm["skuId"].(string)
			if pStr, ok := sm["price"].(string); ok {
				v.Price, _ = strconv.ParseFloat(pStr, 64)
			}
			if qty, ok := sm["quantity"].(float64); ok {
				v.Stock = int(qty)
			}
			// Build variant name from attributes
			if attrs, ok := sm["skuAttributes"].([]interface{}); ok {
				var parts []string
				for _, a := range attrs {
					if am, ok := a.(map[string]interface{}); ok {
						val, _ := am["attributeValue"].(string)
						if val != "" {
							parts = append(parts, val)
						}
					}
				}
				v.Name = strings.Join(parts, " / ")
			}
			p.Variants = append(p.Variants, v)
		}
	}

	return p
}

func download1688Image(imgURL, uploadDir string) (string, error) {
	h := md5.Sum([]byte(imgURL))
	base := fmt.Sprintf("%x", h)
	// Check cache for all possible extensions before making an HTTP request.
	for _, ext := range []string{".jpg", ".webp", ".png"} {
		if _, err := os.Stat(filepath.Join(uploadDir, "all", base+ext)); err == nil {
			return "uploads/all/" + base + ext, nil
		}
	}

	resp, err := ali1688Client.Get(imgURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("image server returned HTTP %d for %s", resp.StatusCode, imgURL)
	}

	ext := ".jpg"
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "webp") {
		ext = ".webp"
	} else if strings.Contains(ct, "png") {
		ext = ".png"
	}

	relPath := "uploads/all/" + base + ext
	fullPath := filepath.Join(uploadDir, "all", base+ext)
	const maxImageBytes = 10 << 20 // 10 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return "", err
	}
	if len(data) == maxImageBytes {
		return "", fmt.Errorf("image too large (>10 MB): %s", imgURL)
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return "", err
	}
	return relPath, nil
}

// choiceOption is the JSON shape stored in products.choice_options.
type choiceOption struct {
	AttributeName string   `json:"attribute_name"`
	Values        []string `json:"values"`
}

// variantRow is one row in products.variations JSON.
type variantRow struct {
	Type  string  `json:"type"`
	Price float64 `json:"price"`
	Qty   int     `json:"qty"`
	Sku   string  `json:"sku"`
	Image string  `json:"image"`
}

// addVariantImagesToGallery appends any per-variant images (var_image[]) not already in the
// gallery or used as the thumbnail to the comma-separated photos string, so the product page
// can display a variant's image (it switches among gallery thumbnails) when that variant is selected.
func addVariantImagesToGallery(c *gin.Context, photos string) string {
	seen := map[string]bool{}
	var ordered []string
	for _, p := range strings.Split(photos, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		ordered = append(ordered, p)
	}
	// The thumbnail is already shown as a gallery thumb on the product page, so don't duplicate it.
	if thumb := strings.TrimSpace(c.PostForm("thumbnail_img")); thumb != "" {
		seen[thumb] = true
	}
	for _, img := range c.PostFormArray("var_image[]") {
		img = strings.TrimSpace(img)
		if img == "" || seen[img] {
			continue
		}
		seen[img] = true
		ordered = append(ordered, img)
	}
	return strings.Join(ordered, ",")
}

// saveProductVariants parses variant form fields and writes product_stocks rows.
// Returns updated unit_price (lowest variant price), total stock, choice_options JSON,
// variations JSON, and variant_product flag.
func saveProductVariants(db *gorm.DB, productID uint, c *gin.Context) (choiceJSON string, variationsJSON string, variantProduct int, totalStock int, lowestPrice float64) {
	variantProduct = boolCheckbox(c.PostFormArray("variant_product"))
	if variantProduct != 1 {
		return "", "", 0, 0, 0
	}

	choiceNames := c.PostFormArray("choice_name[]")
	choiceValues := c.PostFormArray("choice_values[]")

	var choices []choiceOption
	for i, name := range choiceNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var vals []string
		if i < len(choiceValues) {
			for _, v := range strings.Split(choiceValues[i], ",") {
				v = strings.TrimSpace(v)
				if v != "" {
					vals = append(vals, v)
				}
			}
		}
		if len(vals) > 0 {
			choices = append(choices, choiceOption{AttributeName: name, Values: vals})
		}
	}

	varCombos := c.PostFormArray("var_combo[]")
	varPrices := c.PostFormArray("var_price[]")
	varQtys := c.PostFormArray("var_qty[]")
	varSkus := c.PostFormArray("var_sku[]")
	varImages := c.PostFormArray("var_image[]")

	// Build the new stock rows first (before touching the DB).
	var rows []variantRow
	lowestPrice = -1
	for i, combo := range varCombos {
		combo = strings.TrimSpace(combo)
		if combo == "" {
			continue
		}
		price := 0.0
		if i < len(varPrices) {
			price, _ = strconv.ParseFloat(varPrices[i], 64)
		}
		qty := 0
		if i < len(varQtys) {
			qty, _ = strconv.Atoi(varQtys[i])
		}
		sku := ""
		if i < len(varSkus) {
			sku = strings.TrimSpace(varSkus[i])
		}
		image := ""
		if i < len(varImages) {
			image = strings.TrimSpace(varImages[i])
		}
		totalStock += qty
		if lowestPrice < 0 || price < lowestPrice {
			lowestPrice = price
		}
		rows = append(rows, variantRow{Type: combo, Price: price, Qty: qty, Sku: sku, Image: image})
	}

	// Atomically replace old stock rows: delete then insert inside one transaction
	// so a mid-insert failure never leaves the product with partial stock data.
	// If the transaction cannot be started, abort variant saving entirely rather
	// than risk a corrupting partial non-transactional write.
	tx := db.Begin()
	if tx.Error != nil {
		log.Printf("warn: saveProductVariants: begin tx failed for product %d: %v", productID, tx.Error)
		return "", "", 0, 0, 0
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()
	if err := tx.Where("product_id = ?", productID).Delete(&models.ProductStock{}).Error; err != nil {
		log.Printf("warn: saveProductVariants: delete stocks failed for product %d: %v", productID, err)
		return "", "", 0, 0, 0
	}
	for _, row := range rows {
		skuCopy := row.Sku
		stock := models.ProductStock{
			ProductID: productID,
			Variant:   row.Type,
			Sku:       &skuCopy,
			Price:     row.Price,
			Qty:       row.Qty,
		}
		if row.Image != "" {
			imgCopy := row.Image
			stock.VariantImage = &imgCopy
		}
		if err := tx.Create(&stock).Error; err != nil {
			log.Printf("warn: saveProductVariants: insert stock failed for product %d: %v", productID, err)
			return "", "", 0, 0, 0
		}
	}
	if err := tx.Commit().Error; err != nil {
		log.Printf("warn: saveProductVariants: commit failed for product %d: %v", productID, err)
		return "", "", 0, 0, 0
	}
	committed = true
	if lowestPrice < 0 {
		lowestPrice = 0
	}

	if choices == nil {
		choices = []choiceOption{}
	}
	cb, _ := json.Marshal(choices)
	choiceJSON = string(cb)
	if rows == nil {
		rows = []variantRow{}
	}
	vb, _ := json.Marshal(rows)
	variationsJSON = string(vb)
	return
}

func (h *Handler) ProductStore(c *gin.Context) {
	u := currentUser(c)
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/products/create")
		return
	}
	price, _ := strconv.ParseFloat(c.PostForm("unit_price"), 64)
	quantity, _ := strconv.Atoi(c.PostForm("current_stock"))
	minQty, _ := strconv.Atoi(c.PostForm("min_qty"))
	if minQty < 1 {
		minQty = 1
	}
	catID, _ := strconv.ParseUint(c.PostForm("category_id"), 10, 64)
	brandID, _ := strconv.ParseUint(c.PostForm("brand_id"), 10, 64)
	slug := slugify(name)
	if slug == "" {
		// Name has no ASCII-compatible characters (e.g. Chinese from 1688 import
		// with failed translation). Generate a stable time-based slug.
		slug = fmt.Sprintf("product-%d", time.Now().UnixMilli())
	}
	description := c.PostForm("description")

	metaTitle := c.PostForm("meta_title")
	metaDesc := c.PostForm("meta_description")
	metaKw := c.PostForm("meta_keywords")
	product := models.Product{
		Name:            name,
		AddedBy:         "admin",
		UserID:          u.ID,
		CategoryID:      uint(catID),
		UnitPrice:       price,
		CurrentStock:    quantity,
		MinQty:          minQty,
		Slug:            slug,
		Description:     &description,
		Published:       boolCheckbox(c.PostFormArray("published")),
		Approved:        boolCheckbox(c.PostFormArray("approved")),
		Featured:        boolCheckbox(c.PostFormArray("featured")),
		Digital:         boolCheckbox(c.PostFormArray("digital")),
		AuctionProduct:  boolCheckbox(c.PostFormArray("auction_product")),
		MetaTitle:       &metaTitle,
		MetaDescription: &metaDesc,
		MetaKeywords:    &metaKw,
	}
	if brandID > 0 {
		b := uint(brandID)
		product.BrandID = &b
	}
	if thumb := c.PostForm("thumbnail_img"); thumb != "" {
		product.ThumbnailImg = &thumb
	}
	if alt := c.PostForm("thumbnail_img_alt"); alt != "" {
		product.ThumbnailImgAlt = &alt
	}
	if photos := addVariantImagesToGallery(c, strings.Join(c.PostFormArray("gallery_photos[]"), ",")); photos != "" {
		product.Photos = &photos
	}
	if origName, filePath := saveDigitalFile(c, h.UploadDir); filePath != "" {
		product.FileName = &origName
		product.FilePath = &filePath
	}
	if srcURL := strings.TrimSpace(c.PostForm("source_1688_url")); srcURL != "" {
		product.Source1688URL = &srcURL
	}
	if sp := c.PostForm("auction_start_price"); sp != "" {
		if v, err := strconv.ParseFloat(sp, 64); err == nil {
			product.AuctionStartPrice = &v
		}
	}
	if inc := c.PostForm("auction_min_bid_increment"); inc != "" {
		if v, err := strconv.ParseFloat(inc, 64); err == nil {
			product.AuctionMinBidIncrement = &v
		}
	}
	if ea := c.PostForm("auction_end_at"); ea != "" {
		if t, err := time.Parse("2006-01-02T15:04", ea); err == nil {
			product.AuctionEndAt = &t
		}
	}

	if err := h.DB.Create(&product).Error; err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to create product: " + err.Error()})
		return
	}

	// Save variants after product is created (need product.ID)
	choiceJSON, variationsJSON, variantProduct, totalStock, lowestPrice := saveProductVariants(h.DB, product.ID, c)
	if variantProduct == 1 {
		updates := map[string]interface{}{
			"variant_product": 1,
			"choice_options":  choiceJSON,
			"variations":      variationsJSON,
			"current_stock":   totalStock,
		}
		if lowestPrice > 0 {
			updates["unit_price"] = lowestPrice
		}
		h.DB.Model(&product).Updates(updates)
	}

	saveProductTranslations(h.DB, product.ID, c)
	saveSEOMeta(h.DB, "product", product.ID, c)
	c.Redirect(http.StatusFound, "/admin/products")
}

func (h *Handler) ProductEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var product models.Product
	if err := h.DB.Preload("Translations").Preload("Stocks").First(&product, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/products")
		return
	}
	transMap := make(map[string]models.ProductTranslation, len(product.Translations))
	for _, tr := range product.Translations {
		transMap[tr.Lang] = tr
	}
	var cats []models.Category
	h.DB.Order("level asc, name asc").Find(&cats)
	var brands []models.Brand
	h.DB.Order("name asc").Find(&brands)
	var langs []models.Language
	h.DB.Where("status = 1 AND code != 'en'").Order("id asc").Find(&langs)
	var seoMeta models.SEOMeta
	if h.DB.Where("model_type = ? AND model_id = ?", "product", product.ID).First(&seoMeta).Error != nil {
		seoMeta.RobotsIndex = 1
		seoMeta.RobotsFollow = 1
	}

	// Parse choice options for template
	var choiceOptions []choiceOption
	if product.ChoiceOptions != nil && *product.ChoiceOptions != "" {
		_ = json.Unmarshal([]byte(*product.ChoiceOptions), &choiceOptions)
	}

	exchangeRate, _ := strconv.ParseFloat(h.Settings.Get("exchange_rate", "1"), 64)
	priceMarkup, _ := strconv.ParseFloat(h.Settings.Get("price_markup", "0"), 64)
	if exchangeRate <= 0 {
		exchangeRate = 1
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/product_form", gin.H{
		"User":          u,
		"Product":       &product,
		"Categories":    cats,
		"Brands":        brands,
		"Languages":     langs,
		"TransMap":      transMap,
		"SEO":           &seoMeta,
		"ChoiceOptions": choiceOptions,
		"ExchangeRate":  exchangeRate,
		"PriceMarkup":   priceMarkup,
	})
}

func (h *Handler) ProductUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var product models.Product
	if err := h.DB.First(&product, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/products")
		return
	}

	price, _ := strconv.ParseFloat(c.PostForm("unit_price"), 64)
	quantity, _ := strconv.Atoi(c.PostForm("current_stock"))
	minQty, _ := strconv.Atoi(c.PostForm("min_qty"))
	if minQty < 1 {
		minQty = 1
	}
	catID, _ := strconv.ParseUint(c.PostForm("category_id"), 10, 64)
	brandID, _ := strconv.ParseUint(c.PostForm("brand_id"), 10, 64)
	name := c.PostForm("name")
	slug := slugify(name)
	if slug == "" {
		// Name has no ASCII-compatible characters; keep the existing slug rather
		// than overwriting it with an empty string.
		slug = product.Slug
		if slug == "" {
			slug = fmt.Sprintf("product-%d", time.Now().UnixMilli())
		}
	}

	metaTitle := c.PostForm("meta_title")
	metaDesc := c.PostForm("meta_description")
	metaKw := c.PostForm("meta_keywords")

	// Handle variants first
	choiceJSON, variationsJSON, variantProduct, totalStock, lowestPrice := saveProductVariants(h.DB, product.ID, c)

	updates := map[string]interface{}{
		"name":             name,
		"slug":             slug,
		"description":      c.PostForm("description"),
		"category_id":      uint(catID),
		"published":        boolCheckbox(c.PostFormArray("published")),
		"featured":         boolCheckbox(c.PostFormArray("featured")),
		"approved":         boolCheckbox(c.PostFormArray("approved")),
		"digital":          boolCheckbox(c.PostFormArray("digital")),
		"auction_product":  boolCheckbox(c.PostFormArray("auction_product")),
		"meta_title":       &metaTitle,
		"meta_description": &metaDesc,
		"meta_keywords":    &metaKw,
		"variant_product":  variantProduct,
		"min_qty":          minQty,
	}

	if variantProduct == 1 {
		updates["choice_options"] = choiceJSON
		updates["variations"] = variationsJSON
		updates["current_stock"] = totalStock
		if lowestPrice > 0 {
			updates["unit_price"] = lowestPrice
		}
	} else {
		updates["unit_price"] = price
		updates["current_stock"] = quantity
		updates["choice_options"] = ""
		updates["variations"] = ""
	}

	if brandID > 0 {
		b := uint(brandID)
		updates["brand_id"] = &b
	} else {
		updates["brand_id"] = nil
	}

	// Images come as plain path strings from hidden inputs set by the media picker
	if thumb := c.PostForm("thumbnail_img"); thumb != "" {
		updates["thumbnail_img"] = thumb
	} else {
		updates["thumbnail_img"] = nil
	}
	if alt := c.PostForm("thumbnail_img_alt"); alt != "" {
		updates["thumbnail_img_alt"] = alt
	} else {
		updates["thumbnail_img_alt"] = nil
	}
	photos := addVariantImagesToGallery(c, strings.Join(c.PostFormArray("gallery_photos[]"), ","))
	updates["photos"] = photos

	// Digital file
	if origName, filePath := saveDigitalFile(c, h.UploadDir); filePath != "" {
		updates["file_name"] = origName
		updates["file_path"] = filePath
	}
	src1688 := strings.TrimSpace(c.PostForm("source_1688_url"))
	updates["source_1688_url"] = func() interface{} {
		if src1688 != "" {
			return src1688
		}
		return nil
	}()
	// Auction fields
	if sp := c.PostForm("auction_start_price"); sp != "" {
		if v, err := strconv.ParseFloat(sp, 64); err == nil {
			updates["auction_start_price"] = v
		}
	} else {
		updates["auction_start_price"] = nil
	}
	if inc := c.PostForm("auction_min_bid_increment"); inc != "" {
		if v, err := strconv.ParseFloat(inc, 64); err == nil {
			updates["auction_min_bid_increment"] = v
		}
	} else {
		updates["auction_min_bid_increment"] = nil
	}
	if ea := c.PostForm("auction_end_at"); ea != "" {
		if t, err := time.Parse("2006-01-02T15:04", ea); err == nil {
			updates["auction_end_at"] = t
		}
	} else {
		updates["auction_end_at"] = nil
	}

	if err := h.DB.Model(&product).Updates(updates).Error; err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to update product: " + err.Error()})
		return
	}
	saveProductTranslations(h.DB, product.ID, c)
	saveSEOMeta(h.DB, "product", product.ID, c)
	c.Redirect(http.StatusFound, "/admin/products/"+strconv.Itoa(id)+"/edit")
}

// saveProductTranslations upserts product_translations rows from trans_name[lang] / trans_desc[lang] form fields.
func saveProductTranslations(db *gorm.DB, productID uint, c *gin.Context) {
	langs := c.PostFormArray("trans_langs[]")
	for _, lang := range langs {
		lang = strings.TrimSpace(lang)
		if lang == "" {
			continue
		}
		name := strings.TrimSpace(c.PostForm("trans_name[" + lang + "]"))
		desc := strings.TrimSpace(c.PostForm("trans_desc[" + lang + "]"))
		if name == "" && desc == "" {
			db.Where("product_id = ? AND lang = ?", productID, lang).Delete(&models.ProductTranslation{})
			continue
		}
		var tr models.ProductTranslation
		db.FirstOrCreate(&tr, models.ProductTranslation{ProductID: productID, Lang: lang})
		// Always assign both fields unconditionally so that clearing a field in the
		// form actually writes NULL to the DB instead of keeping the old value.
		if name != "" {
			tr.Name = &name
		} else {
			tr.Name = nil
		}
		if desc != "" {
			tr.Description = &desc
		} else {
			tr.Description = nil
		}
		// db.Save writes ALL columns (including nil pointers → NULL), which is
		// what we want. Using db.Select("*").Save would do the same but Save is
		// sufficient since GORM v2 Save always issues a full-column UPDATE.
		db.Save(&tr)
	}
}

// allowedImageExts is the set of permitted image file extensions for product/
// logo/slide/blog uploads. Rejecting other types prevents executable uploads.
var allowedImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true,
}

// allowedDigitalExts permits common digital-product delivery file types.
var allowedDigitalExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true,
	".pdf": true, ".zip": true, ".rar": true, ".mp4": true, ".mp3": true,
	".epub": true, ".mobi": true,
}

// isAllowedExt checks whether ext (lower-case, with dot) is in the given set.
func isAllowedExt(ext string, allowed map[string]bool) bool {
	return allowed[strings.ToLower(ext)]
}

// saveDigitalFile saves an uploaded digital product file and returns (origName, storedPath).
// Returns ("", "") if no file was uploaded.
func saveDigitalFile(c *gin.Context, uploadDir string) (string, string) {
	file, fh, err := c.Request.FormFile("digital_file")
	if err != nil {
		return "", ""
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !isAllowedExt(ext, allowedDigitalExts) {
		return "", ""
	}
	unique := strconv.FormatInt(time.Now().UnixNano(), 36) + ext
	subdir := filepath.Join(uploadDir, "digital")
	os.MkdirAll(subdir, 0755)
	dst := filepath.Join(subdir, unique)
	f, err := os.Create(dst)
	if err != nil {
		return "", ""
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		os.Remove(dst)
		return "", ""
	}
	f.Close()
	return fh.Filename, "uploads/digital/" + unique
}

// AITypeset reformats the product description as HTML and generates optimised
// SEO fields. Requires focus_keyword, text (description), and optionally
// product_name. Returns JSON with html, meta_title, meta_description, meta_keywords.
func (h *Handler) AITypeset(c *gin.Context) {
	// Prefer DB settings, fall back to env-var values from Handler struct
	ollamaURL := h.Settings.Get("ollama_url", h.OllamaURL)
	if ollamaURL == "" {
		ollamaURL = "http://222.186.58.41:11434/v1/chat/completions"
	}
	model := h.Settings.Get("ollama_model", h.OllamaModel)
	if model == "" {
		model = "deepseek-r1:8b"
	}
	// Normalize to an OpenAI-compatible chat-completions endpoint. The AI Settings
	// page often stores just the base URL (e.g. ".../compatible-mode/v1"); append
	// the path so providers like DashScope don't 404.
	if !strings.Contains(ollamaURL, "/chat/completions") {
		ollamaURL = strings.TrimRight(ollamaURL, "/") + "/chat/completions"
	}
	// Bearer token for hosted providers (DashScope/OpenAI/DeepSeek). Prefer the DB
	// setting, fall back to the OLLAMA_API_KEY env var. Local Ollama leaves it blank.
	apiKey := strings.TrimSpace(h.Settings.Get("ollama_api_key", os.Getenv("OLLAMA_API_KEY")))

	focusKw := strings.TrimSpace(c.PostForm("focus_keyword"))
	if focusKw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Focus keyword is required"})
		return
	}
	raw := strings.TrimSpace(c.PostForm("text"))
	if raw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is required"})
		return
	}
	productName := strings.TrimSpace(c.PostForm("product_name"))

	prompt := fmt.Sprintf(`You are an SEO expert and professional e-commerce copywriter.

Given the product description and focus keyword below, respond with ONLY a valid JSON object — no markdown fences, no explanations, no <think> blocks.

JSON schema (all fields required):
{
  "html": "<description reformatted as clean HTML, strict SEO rules applied>",
  "meta_title": "<SEO title, focus keyword first, EXACTLY 50-60 characters>",
  "meta_description": "<meta description, focus keyword in first sentence, EXACTLY 120-160 characters -- count every character including spaces before outputting>",
  "meta_keywords": "<focus_keyword, keyword2, keyword3, keyword4, keyword5>"
}

SEO rules - MUST follow exactly:
1. The focus keyword MUST appear in the very first <h3> heading (treat it as the H1/title).
2. The focus keyword MUST appear in the first <p> paragraph (first sentence preferred).
3. The focus keyword MUST appear naturally 3 to 5 times total in the full html - not more, not less.
4. meta_title MUST start with the focus keyword. Length MUST be 50-60 characters including spaces. If too short expand with a benefit phrase; if too long trim.
5. meta_description MUST contain the focus keyword in the first sentence. Length MUST be 120-160 characters including spaces. This is a hard requirement. Write a full compelling sentence(s). If under 120 add more detail. If over 160 trim.

HTML formatting rules:
- Use <h3> for the main title section and major sections, <h4> for sub-sections.
- Use <p> for paragraphs; split run-on text into clear short paragraphs.
- Use <ul><li>...</li></ul> for feature lists or bullet points.
- Use <strong> to emphasise key product benefits (including focus keyword on first use).
- Do NOT invent new information — only restructure and improve existing content.

Focus keyword: %s
Product name: %s
Description:
%s`, focusKw, productName, raw)

	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type ollamaReq struct {
		Model    string    `json:"model"`
		Messages []message `json:"messages"`
		Stream   bool      `json:"stream"`
	}

	reqBody, _ := json.Marshal(ollamaReq{
		Model:    model,
		Messages: []message{{Role: "user", Content: prompt}},
		Stream:   false,
	})

	client := &http.Client{Timeout: 180 * time.Second}
	httpReq, err := http.NewRequest(http.MethodPost, ollamaURL, strings.NewReader(string(reqBody)))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI request build failed: " + err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI request failed: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	var ollamaResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI response parse error"})
		return
	}
	if ollamaResp.Error.Message != "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": ollamaResp.Error.Message})
		return
	}
	if len(ollamaResp.Choices) == 0 {
		c.JSON(http.StatusBadGateway, gin.H{"error": "empty AI response"})
		return
	}

	content := ollamaResp.Choices[0].Message.Content

	// Strip <think>...</think> blocks (deepseek-r1 chain-of-thought)
	for {
		s := strings.Index(content, "<think>")
		e := strings.Index(content, "</think>")
		if s == -1 || e == -1 || e < s {
			break
		}
		content = content[:s] + content[e+len("</think>"):]
	}
	content = strings.TrimSpace(content)

	// Strip markdown fences
	if strings.HasPrefix(content, "```json") {
		content = content[7:]
	} else if strings.HasPrefix(content, "```") {
		content = content[3:]
	}
	content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	content = strings.TrimSpace(content)

	// Extract the JSON object (find first { ... last })
	first := strings.Index(content, "{")
	last := strings.LastIndex(content, "}")
	if first != -1 && last != -1 && last > first {
		content = content[first : last+1]
	}

	// Parse the AI's JSON output
	var aiOut struct {
		HTML            string `json:"html"`
		MetaTitle       string `json:"meta_title"`
		MetaDescription string `json:"meta_description"`
		MetaKeywords    string `json:"meta_keywords"`
	}
	if err := json.Unmarshal([]byte(content), &aiOut); err != nil {
		// JSON malformed — return raw content as html so the user still gets something
		c.JSON(http.StatusOK, gin.H{"html": content, "parse_error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"html":             aiOut.HTML,
		"meta_title":       aiOut.MetaTitle,
		"meta_description": aiOut.MetaDescription,
		"meta_keywords":    aiOut.MetaKeywords,
	})
}

// MediaFiles returns a paginated JSON list of uploads for the media picker.
func (h *Handler) MediaFiles(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	search := c.Query("search")
	limit := 40
	offset := (page - 1) * limit

	q := h.DB.Model(&models.Upload{}).Where("deleted_at IS NULL")
	if search != "" {
		q = q.Where("file_original_name LIKE ?", "%"+search+"%")
	}
	var total int64
	q.Count(&total)

	var uploads []models.Upload
	q.Order("created_at desc").Limit(limit).Offset(offset).Find(&uploads)

	type fileItem struct {
		ID   uint   `json:"id"`
		URL  string `json:"url"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	items := make([]fileItem, 0, len(uploads))
	for _, u := range uploads {
		if u.FileName == nil {
			continue
		}
		fn := *u.FileName
		url := "/" + strings.TrimPrefix(fn, "/")
		name := ""
		if u.FileOriginalName != nil {
			name = *u.FileOriginalName
		}
		items = append(items, fileItem{ID: u.ID, URL: url, Path: fn, Name: name})
	}
	c.JSON(http.StatusOK, gin.H{
		"files": items,
		"total": total,
		"page":  page,
		"pages": (int(total) + limit - 1) / limit,
	})
}

// MediaUpload uploads a file via AJAX, saves it to disk and the uploads table,
// and returns JSON {id, url, path, name}.
func (h *Handler) MediaUpload(c *gin.Context) {
	file, fh, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file"})
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if ext == "" {
		ext = ".jpg"
	}
	if !isAllowedExt(ext, allowedImageExts) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file type not allowed"})
		return
	}
	unique := strconv.FormatInt(time.Now().UnixNano(), 36) + ext
	subdir := filepath.Join(h.UploadDir, "all")
	os.MkdirAll(subdir, 0755)
	dst := filepath.Join(subdir, unique)
	f, err := os.Create(dst)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
		return
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		os.Remove(dst)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed: " + err.Error()})
		return
	}
	f.Close()

	fn := "uploads/all/" + unique
	origName := fh.Filename
	fsize := int(fh.Size)
	upload := models.Upload{
		FileOriginalName: &origName,
		FileName:         &fn,
		Extension:        &ext,
		FileSize:         &fsize,
	}
	h.DB.Create(&upload)

	c.JSON(http.StatusOK, gin.H{
		"id":   upload.ID,
		"url":  "/" + fn,
		"path": fn,
		"name": origName,
	})
}

// saveProductImage saves an uploaded product image to the uploads/all directory
// and returns the path string (e.g. "uploads/all/xxx.webp") or "" on no upload.
func saveProductImage(c *gin.Context, fieldName, uploadDir string) string {
	file, fh, err := c.Request.FormFile(fieldName)
	if err != nil {
		return ""
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if ext == "" {
		ext = ".jpg"
	}
	if !isAllowedExt(ext, allowedImageExts) {
		return ""
	}
	unique := strconv.FormatInt(time.Now().UnixNano(), 36) + ext
	subdir := filepath.Join(uploadDir, "all")
	os.MkdirAll(subdir, 0755)
	dst := filepath.Join(subdir, unique)
	f, err := os.Create(dst)
	if err != nil {
		return ""
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		os.Remove(dst)
		return ""
	}
	f.Close()
	return "uploads/all/" + unique
}

// saveProductImages saves multiple uploaded files (e.g. photos[]) and returns
// comma-joined paths, appending to any existing paths passed in existingPaths.
func saveProductImages(c *gin.Context, fieldName, uploadDir, existingPaths string) string {
	form, err := c.MultipartForm()
	if err != nil {
		return existingPaths
	}
	files := form.File[fieldName]
	var paths []string
	if existingPaths != "" {
		paths = strings.Split(existingPaths, ",")
	}
	subdir := filepath.Join(uploadDir, "all")
	os.MkdirAll(subdir, 0755)
	for _, fh := range files {
		file, err := fh.Open()
		if err != nil {
			continue
		}
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if ext == "" {
			ext = ".jpg"
		}
		if !isAllowedExt(ext, allowedImageExts) {
			file.Close()
			continue
		}
		unique := strconv.FormatInt(time.Now().UnixNano(), 36) + ext
		dst := filepath.Join(subdir, unique)
		f, err := os.Create(dst)
		if err != nil {
			file.Close()
			continue
		}
		if _, err := io.Copy(f, file); err != nil {
			f.Close()
			os.Remove(dst)
			file.Close()
			continue
		}
		f.Close()
		file.Close()
		paths = append(paths, "uploads/all/"+unique)
	}
	return strings.Join(paths, ",")
}

// boolCheckbox handles the hidden+checkbox pattern: hidden sends "0", checked checkbox appends "1".
func boolCheckbox(vals []string) int {
	for _, v := range vals {
		if v == "1" {
			return 1
		}
	}
	return 0
}

func (h *Handler) ProductTogglePublish(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var p models.Product
	if err := h.DB.First(&p, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/products")
		return
	}
	pub := 1
	if p.Published == 1 {
		pub = 0
	}
	h.DB.Model(&p).Update("published", pub)
	c.Redirect(http.StatusFound, "/admin/products")
}

func (h *Handler) ProductDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Product{}, id)
	c.Redirect(http.StatusFound, "/admin/products")
}

// ── Categories ────────────────────────────────────────────────────────────────

func (h *Handler) CategoryList(c *gin.Context) {
	u := currentUser(c)
	var cats []models.Category
	h.DB.Order("level asc, order_level asc, id asc").Find(&cats)

	catIDs := make([]uint, len(cats))
	for i, cat := range cats {
		catIDs[i] = cat.ID
	}
	var catSeoList []models.SEOMeta
	if len(catIDs) > 0 {
		h.DB.Where("model_type = 'category' AND model_id IN ?", catIDs).Find(&catSeoList)
	}
	catSeoScores := make(map[uint]int, len(catSeoList))
	catSeoMetas := make(map[uint]models.SEOMeta, len(catSeoList))
	for _, s := range catSeoList {
		catSeoScores[s.ModelID] = s.SEOScore
		catSeoMetas[s.ModelID] = s
	}

	h.Engine.Render(c, http.StatusOK, "admin", "admin/categories", gin.H{
		"User":       u,
		"Categories": cats,
		"SEOScores":  catSeoScores,
		"SEOMetas":   catSeoMetas,
	})
}

func (h *Handler) CategoryStore(c *gin.Context) {
	name := c.PostForm("name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/categories")
		return
	}
	slug := c.PostForm("slug")
	if slug == "" {
		slug = slugify(name)
	}
	parentID, _ := strconv.ParseUint(c.PostForm("parent_id"), 10, 64)
	level := 0
	if parentID > 0 {
		level = 1
	}
	orderLevel, _ := strconv.Atoi(c.PostForm("order_level"))
	commRate, _ := strconv.ParseFloat(c.PostForm("commision_rate"), 64)
	discount, _ := strconv.ParseFloat(c.PostForm("discount"), 64)
	featured := 0
	if c.PostForm("featured") == "1" {
		featured = 1
	}
	hotCat := "0"
	if c.PostForm("hot_category") == "1" {
		hotCat = "1"
	}
	top := 0
	if c.PostForm("top") == "1" {
		top = 1
	}
	digital := 0
	if c.PostForm("digital") == "1" {
		digital = 1
	}
	refundTime, _ := strconv.ParseUint(c.PostForm("refund_request_time"), 10, 64)
	rft := uint(refundTime)

	catMetaTitle := c.PostForm("meta_title")
	catMetaDesc := c.PostForm("meta_description")
	catMetaKw := c.PostForm("meta_keywords")

	icon := c.PostForm("icon")
	banner := c.PostForm("banner")
	cover := c.PostForm("cover_image")

	cat := models.Category{
		Name:            name,
		Slug:            &slug,
		Level:           level,
		OrderLevel:      orderLevel,
		CommisionRate:   commRate,
		Discount:        discount,
		Featured:        featured,
		HotCategory:     hotCat,
		Top:             top,
		Digital:         digital,
		MetaTitle:       &catMetaTitle,
		MetaDescription: &catMetaDesc,
		MetaKeywords:    &catMetaKw,
	}
	if parentID > 0 {
		pid := uint(parentID)
		cat.ParentID = &pid
	}
	if icon != "" {
		cat.Icon = &icon
	}
	if banner != "" {
		cat.Banner = &banner
	}
	if cover != "" {
		cat.CoverImage = &cover
	}
	if refundTime > 0 {
		cat.RefundRequestTime = &rft
	}
	h.DB.Create(&cat)
	saveSEOMeta(h.DB, "category", cat.ID, c)
	c.Redirect(http.StatusFound, "/admin/categories")
}

func (h *Handler) CategoryUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	name := c.PostForm("name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/categories")
		return
	}
	slug := c.PostForm("slug")
	if slug == "" {
		slug = slugify(name)
	}
	parentID, _ := strconv.ParseUint(c.PostForm("parent_id"), 10, 64)
	level := 0
	if parentID > 0 {
		level = 1
	}
	orderLevel, _ := strconv.Atoi(c.PostForm("order_level"))
	commRate, _ := strconv.ParseFloat(c.PostForm("commision_rate"), 64)
	discount, _ := strconv.ParseFloat(c.PostForm("discount"), 64)
	featured := 0
	if c.PostForm("featured") == "1" {
		featured = 1
	}
	hotCat := "0"
	if c.PostForm("hot_category") == "1" {
		hotCat = "1"
	}
	top := 0
	if c.PostForm("top") == "1" {
		top = 1
	}
	digital := 0
	if c.PostForm("digital") == "1" {
		digital = 1
	}
	refundTime, _ := strconv.ParseUint(c.PostForm("refund_request_time"), 10, 64)

	catMetaTitle := c.PostForm("meta_title")
	catMetaDesc := c.PostForm("meta_description")
	catMetaKw := c.PostForm("meta_keywords")

	updates := map[string]interface{}{
		"name":             name,
		"slug":             slug,
		"level":            level,
		"order_level":      orderLevel,
		"commision_rate":   commRate,
		"discount":         discount,
		"featured":         featured,
		"hot_category":     hotCat,
		"top":              top,
		"digital":          digital,
		"meta_title":       catMetaTitle,
		"meta_description": catMetaDesc,
		"meta_keywords":    catMetaKw,
	}
	if parentID > 0 {
		updates["parent_id"] = uint(parentID)
	} else {
		updates["parent_id"] = nil
	}
	if refundTime > 0 {
		updates["refund_request_time"] = uint(refundTime)
	}
	icon := c.PostForm("icon")
	banner := c.PostForm("banner")
	cover := c.PostForm("cover_image")
	if icon != "" {
		updates["icon"] = icon
	} else {
		updates["icon"] = nil
	}
	if banner != "" {
		updates["banner"] = banner
	} else {
		updates["banner"] = nil
	}
	if cover != "" {
		updates["cover_image"] = cover
	} else {
		updates["cover_image"] = nil
	}

	h.DB.Model(&models.Category{}).Where("id = ?", id).Updates(updates)
	saveSEOMeta(h.DB, "category", uint(id), c)
	c.Redirect(http.StatusFound, "/admin/categories")
}

func (h *Handler) CategoryDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		c.Redirect(http.StatusFound, "/admin/categories")
		return
	}
	h.DB.Delete(&models.Category{}, id)
	c.Redirect(http.StatusFound, "/admin/categories")
}

// ── Brands ────────────────────────────────────────────────────────────────────

func (h *Handler) BrandList(c *gin.Context) {
	u := currentUser(c)
	var brands []models.Brand
	h.DB.Order("name asc").Find(&brands)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/brands", gin.H{
		"User":   u,
		"Brands": brands,
	})
}

func (h *Handler) BrandStore(c *gin.Context) {
	name := c.PostForm("name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/brands")
		return
	}
	slug := slugify(name)
	h.DB.Create(&models.Brand{Name: name, Slug: &slug})
	c.Redirect(http.StatusFound, "/admin/brands")
}

func (h *Handler) BrandEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var brand models.Brand
	if err := h.DB.First(&brand, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/brands")
		return
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/brand_form", gin.H{
		"User":  u,
		"Brand": &brand,
	})
}

func (h *Handler) BrandUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	name := c.PostForm("name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/brands")
		return
	}
	slug := slugify(name)
	h.DB.Model(&models.Brand{}).Where("id = ?", id).Updates(map[string]interface{}{
		"name": name,
		"slug": slug,
	})
	c.Redirect(http.StatusFound, "/admin/brands")
}

func (h *Handler) BrandDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Brand{}, id)
	c.Redirect(http.StatusFound, "/admin/brands")
}

// ── Orders ────────────────────────────────────────────────────────────────────

func (h *Handler) orderList(c *gin.Context, addedBy string) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	status := c.Query("status")
	limit := 15
	offset := (page - 1) * limit

	var orders []models.Order
	var total int64
	q := h.DB.Model(&models.Order{})
	if addedBy == "inhouse" {
		q = q.Where("seller_id IS NULL OR seller_id = 0")
	} else if addedBy == "seller" {
		q = q.Where("seller_id > 0")
	}
	if status != "" {
		q = q.Where("delivery_status = ?", status)
	}
	q.Count(&total)
	q.Order("created_at desc").Limit(limit).Offset(offset).
		Preload("Buyer").Preload("Seller").Find(&orders)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/orders", gin.H{
		"User":    u,
		"Orders":  orders,
		"Total":   total,
		"Page":    page,
		"Pages":   (int(total) + limit - 1) / limit,
		"Status":  status,
		"AddedBy": addedBy,
	})
}

func (h *Handler) OrderList(c *gin.Context)       { h.orderList(c, "") }
func (h *Handler) OrderListInhouse(c *gin.Context) { h.orderList(c, "inhouse") }
func (h *Handler) OrderListSeller(c *gin.Context)  { h.orderList(c, "seller") }
func (h *Handler) OrderListPickup(c *gin.Context)  { h.orderList(c, "pickup") }

func (h *Handler) OrderDetail(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var order models.Order
	if err := h.DB.Preload("OrderDetails.Product").
		Preload("Buyer").
		Preload("Seller").
		First(&order, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/orders")
		return
	}

	// Load supplier orders keyed by order_detail_id for O(1) lookup in template.
	var sfOrders []models.SupplierOrder
	h.DB.Where("order_id = ?", id).Find(&sfOrders)
	sfMap := make(map[uint]*models.SupplierOrder, len(sfOrders))
	for i := range sfOrders {
		sfMap[sfOrders[i].OrderDetailID] = &sfOrders[i]
	}

	// Parse logistics JSON for any supplier orders that have it.
	// Build sfEvents: map[supplierOrderID][]TrackEvent for template access.
	sfEvents := make(map[uint][]kuaidi100.TrackEvent, len(sfOrders))
	for i := range sfOrders {
		sf := &sfOrders[i]
		if sf.LogisticsJSON != nil && *sf.LogisticsJSON != "" {
			var resp kuaidi100.Response
			if err := json.Unmarshal([]byte(*sf.LogisticsJSON), &resp); err != nil {
				log.Printf("warn: parse logistics JSON for supplier_order %d: %v", sf.ID, err)
			} else if len(resp.Data) > 0 {
				sfEvents[sf.ID] = resp.Data
			}
		}
	}

	h.Engine.Render(c, http.StatusOK, "admin", "admin/order_detail", gin.H{
		"User":     u,
		"Order":    order,
		"SFMap":    sfMap,    // map[order_detail_id]*SupplierOrder
		"SFEvents": sfEvents, // map[supplier_order_id][]TrackEvent
		"Carriers": kuaidi100.CommonCarriers,
	})
}

var allowedDeliveryStatuses = map[string]bool{
	"pending": true, "processing": true, "on the way": true, "delivered": true, "cancelled": true,
}
var allowedPaymentStatuses = map[string]bool{
	"unpaid": true, "paid": true,
}

func (h *Handler) OrderUpdateStatus(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	status := c.PostForm("delivery_status")
	payStatus := c.PostForm("payment_status")
	updates := map[string]interface{}{}
	if status != "" && allowedDeliveryStatuses[status] {
		updates["delivery_status"] = status
	}
	if payStatus != "" && allowedPaymentStatuses[payStatus] {
		updates["payment_status"] = payStatus
	}
	redirect := "/admin/orders/" + strconv.Itoa(id)
	if len(updates) == 0 {
		view.FlashSet(c, "error", "No valid status values provided.")
		c.Redirect(http.StatusFound, redirect)
		return
	}
	if err := h.DB.Model(&models.Order{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		log.Printf("error: OrderUpdateStatus id=%d: %v", id, err)
		view.FlashSet(c, "error", "Failed to update order status: "+err.Error())
		c.Redirect(http.StatusFound, redirect)
		return
	}
	// Keep order_details in sync with the parent order's delivery/payment status.
	if status != "" && allowedDeliveryStatuses[status] {
		h.DB.Model(&models.OrderDetail{}).Where("order_id = ?", id).
			Update("delivery_status", status)
	}
	if payStatus != "" && allowedPaymentStatuses[payStatus] {
		h.DB.Model(&models.OrderDetail{}).Where("order_id = ?", id).
			Update("payment_status", payStatus)
	}
	view.FlashSet(c, "success", "Order status updated successfully.")
	c.Redirect(http.StatusFound, redirect)
}

// ── Refunds ───────────────────────────────────────────────────────────────────

func (h *Handler) RefundList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	status := c.DefaultQuery("status", "")
	limit := 20
	offset := (page - 1) * limit

	var refunds []models.RefundRequest
	var total int64
	q := h.DB.Model(&models.RefundRequest{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	q.Count(&total)
	q.Order("created_at desc").Limit(limit).Offset(offset).
		Preload("Order").Preload("User").Find(&refunds)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/refunds", gin.H{
		"User":    currentUser(c),
		"Refunds": refunds,
		"Total":   total,
		"Page":    page,
		"Pages":   (int(total) + limit - 1) / limit,
		"Status":  status,
	})
}

func (h *Handler) RefundApprove(c *gin.Context) {
	id := c.Param("id")
	note := strings.TrimSpace(c.PostForm("note"))
	h.DB.Model(&models.RefundRequest{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": "approved", "admin_note": note})
	view.FlashSet(c, "success", "Refund approved.")
	c.Redirect(http.StatusFound, "/admin/refunds")
}

func (h *Handler) RefundDecline(c *gin.Context) {
	id := c.Param("id")
	note := strings.TrimSpace(c.PostForm("note"))
	h.DB.Model(&models.RefundRequest{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": "declined", "admin_note": note})
	view.FlashSet(c, "success", "Refund declined.")
	c.Redirect(http.StatusFound, "/admin/refunds")
}

// ── Sellers ───────────────────────────────────────────────────────────────────

func (h *Handler) SellerListPending(c *gin.Context) {
	c.Request.URL.RawQuery = "status=pending"
	h.SellerList(c)
}

func (h *Handler) SellerListVerified(c *gin.Context) {
	c.Request.URL.RawQuery = "status=verified"
	h.SellerList(c)
}

func (h *Handler) SellerList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	search := c.Query("search")
	status := c.Query("status")
	limit := 15
	offset := (page - 1) * limit

	buildQuery := func() *gorm.DB {
		q := h.DB.Model(&models.User{}).Where("users.user_type = ?", "seller")
		if search != "" {
			q = q.Where("users.name LIKE ? OR users.email LIKE ?", "%"+search+"%", "%"+search+"%")
		}
		switch status {
		case "banned":
			q = q.Where("users.banned = 1")
		case "verified":
			q = q.Joins("LEFT JOIN shops ON shops.user_id = users.id").
				Where("shops.verification_status = 1")
		case "pending":
			q = q.Joins("LEFT JOIN shops ON shops.user_id = users.id").
				Where("shops.verification_status = 0 OR shops.id IS NULL")
		}
		return q
	}

	var sellers []models.User
	var total int64
	buildQuery().Count(&total)
	buildQuery().
		Order("users.created_at desc").Limit(limit).Offset(offset).
		Preload("Shop").Find(&sellers)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/sellers", gin.H{
		"User":    u,
		"Sellers": sellers,
		"Total":   total,
		"Page":    page,
		"Pages":   (int(total) + limit - 1) / limit,
		"Search":  search,
		"Status":  status,
	})
}

func (h *Handler) SellerShow(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))

	var seller models.User
	if err := h.DB.Where("id = ? AND user_type = ?", id, "seller").
		Preload("Shop").First(&seller).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/sellers")
		return
	}

	var products []models.Product
	h.DB.Where("user_id = ?", id).Order("created_at desc").Limit(20).Find(&products)

	var productCount int64
	h.DB.Model(&models.Product{}).Where("user_id = ?", id).Count(&productCount)

	var orders []models.Order
	h.DB.Where("seller_id = ?", id).Order("created_at desc").Limit(10).
		Preload("Buyer").Find(&orders)

	var orderCount int64
	h.DB.Model(&models.Order{}).Where("seller_id = ?", id).Count(&orderCount)

	var totalEarning float64
	h.DB.Model(&models.CommissionHistory{}).
		Select("COALESCE(SUM(seller_earning),0)").
		Where("seller_id = ?", id).Scan(&totalEarning)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/seller_detail", gin.H{
		"User":         u,
		"Seller":       seller,
		"Products":     products,
		"ProductCount": productCount,
		"Orders":       orders,
		"OrderCount":   orderCount,
		"TotalEarning": totalEarning,
	})
}

func (h *Handler) SellerLoginAs(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))

	var seller models.User
	if err := h.DB.Where("id = ? AND user_type = ?", id, "seller").First(&seller).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/sellers")
		return
	}

	sess := sessions.Default(c)
	sess.Set("impersonator_id", u.ID)
	sess.Set("user_id", seller.ID)
	_ = sess.Save()
	c.Redirect(http.StatusFound, "/seller/dashboard")
}

func (h *Handler) StopImpersonating(c *gin.Context) {
	sess := sessions.Default(c)
	if adminID, ok := sess.Get("impersonator_id").(uint); ok && adminID > 0 {
		sess.Set("user_id", adminID)
		sess.Delete("impersonator_id")
		_ = sess.Save()
		c.Redirect(http.StatusFound, "/admin/sellers")
		return
	}
	c.Redirect(http.StatusFound, "/")
}

func (h *Handler) SellerDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var seller models.User
	if err := h.DB.Where("id = ? AND user_type = ?", id, "seller").First(&seller).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/sellers")
		return
	}

	if err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Delete(&models.Product{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&models.Shop{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.User{}, id).Error
	}); err != nil {
		c.Redirect(http.StatusFound, "/admin/sellers?error="+url.QueryEscape(err.Error()))
		return
	}
	c.Redirect(http.StatusFound, "/admin/sellers")
}

func (h *Handler) SellerVerify(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Model(&models.Shop{}).Where("user_id = ?", id).
		Update("verification_status", 1)
	c.Redirect(http.StatusFound, "/admin/sellers")
}

func (h *Handler) SellerBan(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u models.User
	if err := h.DB.Where("id = ? AND user_type = ?", id, "seller").First(&u).Error; err == nil {
		banned := 1
		if u.Banned == 1 {
			banned = 0
		}
		h.DB.Model(&u).Update("banned", banned)
	}
	c.Redirect(http.StatusFound, "/admin/sellers")
}

func (h *Handler) SellerListApplied(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	search := c.Query("search")
	limit := 15
	offset := (page - 1) * limit

	buildQuery := func() *gorm.DB {
		q := h.DB.Model(&models.User{}).
			Joins("LEFT JOIN shops ON shops.user_id = users.id").
			Where("users.user_type = ?", "seller").
			Where("shops.verification_status = 0 OR shops.id IS NULL")
		if search != "" {
			q = q.Where("users.name LIKE ? OR users.email LIKE ?", "%"+search+"%", "%"+search+"%")
		}
		return q
	}

	var sellers []models.User
	var total int64
	buildQuery().Count(&total)
	buildQuery().Order("users.created_at desc").Limit(limit).Offset(offset).Preload("Shop").Find(&sellers)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/seller_applied", gin.H{
		"User":    u,
		"Sellers": sellers,
		"Total":   total,
		"Page":    page,
		"Pages":   (int(total) + limit - 1) / limit,
		"Search":  search,
	})
}

func (h *Handler) SellerRatings(c *gin.Context) {
	u := currentUser(c)
	var sellers []models.User
	h.DB.Model(&models.User{}).
		Where("user_type = ?", "seller").
		Joins("LEFT JOIN shops ON shops.user_id = users.id").
		Order("COALESCE(shops.rating, 0) DESC").
		Preload("Shop").
		Find(&sellers)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/seller_ratings", gin.H{
		"User":    u,
		"Sellers": sellers,
	})
}

type PayoutRow struct {
	ID         uint
	SellerName string
	Email      string
	Amount     float64
	Message    string
	Status     int
	CreatedAt  time.Time
}

func (h *Handler) SellerPayoutList(c *gin.Context) {
	u := currentUser(c)
	statusFilter := c.Query("status")

	q := h.DB.Table("seller_withdraw_requests").
		Select("seller_withdraw_requests.id, users.name as seller_name, users.email, "+
			"COALESCE(seller_withdraw_requests.amount,0) as amount, "+
			"COALESCE(seller_withdraw_requests.message,'') as message, "+
			"COALESCE(seller_withdraw_requests.status,0) as status, "+
			"seller_withdraw_requests.created_at").
		Joins("LEFT JOIN users ON users.id = seller_withdraw_requests.user_id").
		Order("seller_withdraw_requests.created_at desc")

	switch statusFilter {
	case "pending":
		q = q.Where("COALESCE(seller_withdraw_requests.status,0) = 0")
	case "approved":
		q = q.Where("seller_withdraw_requests.status = 1")
	case "rejected":
		q = q.Where("seller_withdraw_requests.status = 2")
	}

	var rows []PayoutRow
	q.Scan(&rows)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/seller_payout_requests", gin.H{
		"User":         u,
		"Rows":         rows,
		"StatusFilter": statusFilter,
	})
}

func (h *Handler) SellerPayoutApprove(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	h.DB.Model(&models.SellerWithdrawRequest{ID: uint(id)}).Update("status", 1)
	view.FlashSet(c, "success", "Payout request approved.")
	c.Redirect(http.StatusFound, "/admin/sellers/payout-requests")
}

func (h *Handler) SellerPayoutReject(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	h.DB.Model(&models.SellerWithdrawRequest{ID: uint(id)}).Update("status", 2)
	view.FlashSet(c, "success", "Payout request rejected.")
	c.Redirect(http.StatusFound, "/admin/sellers/payout-requests")
}

// ── CMS Pages ─────────────────────────────────────────────────────────────────

// ── Menu ─────────────────────────────────────────────────────────────────────

func (h *Handler) MenuList(c *gin.Context) {
	u := currentUser(c)
	var items []models.MenuItem
	h.DB.Order("sort_order asc, id asc").Find(&items)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/menu", gin.H{
		"User":  u,
		"Items": items,
	})
}

// isSafeMenuURL rejects javascript: and data: URI schemes that could execute
// script in an <a href> context.
func isSafeMenuURL(rawURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(rawURL))
	return !strings.HasPrefix(lower, "javascript:") && !strings.HasPrefix(lower, "data:")
}

func (h *Handler) MenuStore(c *gin.Context) {
	label := strings.TrimSpace(c.PostForm("label"))
	rawURL := strings.TrimSpace(c.PostForm("url"))
	if label == "" || rawURL == "" {
		c.Redirect(http.StatusFound, "/admin/menu")
		return
	}
	if !isSafeMenuURL(rawURL) {
		c.Redirect(http.StatusFound, "/admin/menu?error=Invalid+URL+scheme")
		return
	}
	newTab := 0
	if c.PostForm("new_tab") == "1" {
		newTab = 1
	}
	var maxOrder int
	h.DB.Model(&models.MenuItem{}).Select("COALESCE(MAX(sort_order),0)").Scan(&maxOrder)
	h.DB.Create(&models.MenuItem{Label: label, URL: rawURL, NewTab: newTab, SortOrder: maxOrder + 10})
	c.Redirect(http.StatusFound, "/admin/menu")
}

func (h *Handler) MenuUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	label := strings.TrimSpace(c.PostForm("label"))
	rawURL := strings.TrimSpace(c.PostForm("url"))
	if !isSafeMenuURL(rawURL) {
		c.Redirect(http.StatusFound, "/admin/menu?error=Invalid+URL+scheme")
		return
	}
	newTab := 0
	if c.PostForm("new_tab") == "1" {
		newTab = 1
	}
	h.DB.Model(&models.MenuItem{}).Where("id = ?", id).Updates(map[string]interface{}{
		"label":   label,
		"url":     rawURL,
		"new_tab": newTab,
	})
	c.Redirect(http.StatusFound, "/admin/menu")
}

func (h *Handler) MenuDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.MenuItem{}, id)
	c.Redirect(http.StatusFound, "/admin/menu")
}

func (h *Handler) MenuReorder(c *gin.Context) {
	var body struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for i, id := range body.IDs {
		h.DB.Model(&models.MenuItem{}).Where("id = ?", id).Update("sort_order", (i+1)*10)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── Pages ─────────────────────────────────────────────────────────────────────

func (h *Handler) PageList(c *gin.Context) {
	u := currentUser(c)
	var pages []models.Page
	h.DB.Order("id asc").Find(&pages)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/pages", gin.H{
		"User":  u,
		"Pages": pages,
	})
}

func (h *Handler) PageEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var page models.Page
	if err := h.DB.First(&page, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/pages")
		return
	}
	_, success := c.GetQuery("success")
	// Editing language: blank/"en" edits the base Page row directly, anything
	// else loads/saves a PageTranslation overlay so the same Page can carry
	// multiple language versions of the title/content/SEO.
	lang := strings.TrimSpace(c.Query("lang"))
	if lang == "en" {
		lang = ""
	}
	editPage := page
	if lang != "" {
		var tr models.PageTranslation
		if err := h.DB.Where("page_id = ? AND lang = ?", id, lang).First(&tr).Error; err == nil {
			editPage.Title = tr.Title
			editPage.Content = tr.Content
			editPage.MetaTitle = tr.MetaTitle
			editPage.MetaDescription = tr.MetaDescription
			editPage.Keywords = tr.Keywords
		} else {
			// No translation yet — start blank so the editor doesn't accidentally
			// look "translated" when it's actually the English fallback.
			empty := ""
			editPage.Title = &empty
			editPage.Content = &empty
			editPage.MetaTitle = &empty
			editPage.MetaDescription = &empty
			editPage.Keywords = &empty
		}
	}
	data := gin.H{
		"User":      u,
		"Page":      editPage,
		"BasePage":  page, // original for reference (immutable slug etc.)
		"EditLang":  lang,
		"Languages": availablePageLangs(),
		"Success":   success,
	}
	// For contact page, load current settings so the form pre-fills
	if page.Type == "contact_us_page" {
		var settings []models.BusinessSetting
		h.DB.Find(&settings)
		smap := make(map[string]string, len(settings))
		for _, s := range settings {
			if s.Value != nil {
				smap[s.Type] = *s.Value
			}
		}
		data["ContactSettings"] = smap
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/page_edit", data)
}

// availablePageLangs returns the language codes the page editor exposes as
// translation targets. Mirrors internal/services/i18n locale files. "en" is
// implicit (the base row).
func availablePageLangs() []map[string]string {
	return []map[string]string{
		{"code": "en", "label": "English"},
		{"code": "cn", "label": "中文"},
		{"code": "es", "label": "Español"},
		{"code": "fr", "label": "Français"},
		{"code": "de", "label": "Deutsch"},
		{"code": "pt", "label": "Português"},
		{"code": "ru", "label": "Русский"},
		{"code": "ja", "label": "日本語"},
		{"code": "ko", "label": "한국어"},
		{"code": "ar", "label": "العربية"},
		{"code": "th", "label": "ไทย"},
	}
}

func (h *Handler) PageUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var page models.Page
	if err := h.DB.First(&page, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/pages")
		return
	}
	title := c.PostForm("title")
	slug := strings.TrimSpace(c.PostForm("slug"))
	content := c.PostForm("content")
	metaTitle := c.PostForm("meta_title")
	metaDesc := c.PostForm("meta_description")
	keywords := c.PostForm("keywords")

	// Which language is being edited? Blank or "en" = base row; anything else
	// upserts a PageTranslation row instead of touching the base page.
	lang := strings.TrimSpace(c.PostForm("edit_lang"))
	if lang == "en" {
		lang = ""
	}
	redirURL := "/admin/pages/" + strconv.Itoa(id) + "/edit?success=1"

	if lang != "" {
		var tr models.PageTranslation
		err := h.DB.Where("page_id = ? AND lang = ?", id, lang).First(&tr).Error
		if err != nil {
			tr = models.PageTranslation{PageID: uint(id), Lang: lang}
			tr.Title = &title
			tr.Content = &content
			tr.MetaTitle = &metaTitle
			tr.MetaDescription = &metaDesc
			tr.Keywords = &keywords
			h.DB.Create(&tr)
		} else {
			h.DB.Model(&tr).Updates(map[string]interface{}{
				"title":            &title,
				"content":          &content,
				"meta_title":       &metaTitle,
				"meta_description": &metaDesc,
				"keywords":         &keywords,
			})
		}
		c.Redirect(http.StatusFound, redirURL+"&lang="+lang)
		return
	}

	h.DB.Model(&page).Updates(map[string]interface{}{
		"title":            &title,
		"slug":             &slug,
		"content":          &content,
		"meta_title":       &metaTitle,
		"meta_description": &metaDesc,
		"keywords":         &keywords,
	})
	// Contact Us page: persist structured fields to business settings
	if page.Type == "contact_us_page" {
		contactKeys := []string{
			"contact_heading", "contact_subtitle",
			"support_email", "support_phone", "contact_hours", "address",
		}
		for _, key := range contactKeys {
			val := c.PostForm(key)
			var count int64
			h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Count(&count)
			if count == 0 {
				h.DB.Create(&models.BusinessSetting{Type: key, Value: &val})
			} else {
				h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Update("value", val)
			}
		}
		h.Settings.Invalidate()
	}
	c.Redirect(http.StatusFound, "/admin/pages/"+strconv.Itoa(id)+"/edit?success=1")
}

func (h *Handler) PageCreate(c *gin.Context) {
	u := currentUser(c)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/page_edit", gin.H{
		"User": u,
		"Page": models.Page{},
	})
}

func (h *Handler) PageStore(c *gin.Context) {
	title := c.PostForm("title")
	slug := strings.TrimSpace(c.PostForm("slug"))
	content := c.PostForm("content")
	metaTitle := c.PostForm("meta_title")
	metaDesc := c.PostForm("meta_description")
	keywords := c.PostForm("keywords")
	pageType := "custom"
	page := models.Page{
		Type:            pageType,
		Title:           &title,
		Slug:            &slug,
		Content:         &content,
		MetaTitle:       &metaTitle,
		MetaDescription: &metaDesc,
		Keywords:        &keywords,
	}
	h.DB.Create(&page)
	c.Redirect(http.StatusFound, "/admin/pages/"+strconv.Itoa(int(page.ID))+"/edit?success=1")
}

func (h *Handler) PageDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Page{}, id)
	c.Redirect(http.StatusFound, "/admin/pages")
}

// ── Languages ─────────────────────────────────────────────────────────────────

func (h *Handler) LanguageList(c *gin.Context) {
	u := currentUser(c)
	var langs []models.Language
	h.DB.Order("id asc").Find(&langs)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/languages", gin.H{
		"User":      u,
		"Languages": langs,
	})
}

func (h *Handler) LanguageStore(c *gin.Context) {
	rtl := 0
	if c.PostForm("rtl") == "1" {
		rtl = 1
	}
	lang := models.Language{
		Name:        c.PostForm("name"),
		Code:        strings.TrimSpace(c.PostForm("code")),
		AppLangCode: strings.TrimSpace(c.PostForm("app_lang_code")),
		RTL:         rtl,
		Status:      1,
	}
	if lang.AppLangCode == "" {
		lang.AppLangCode = lang.Code
	}
	h.DB.Create(&lang)
	c.Redirect(http.StatusFound, "/admin/languages")
}

func (h *Handler) LanguageToggle(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var lang models.Language
	if err := h.DB.First(&lang, id).Error; err == nil {
		status := 1
		if lang.Status == 1 {
			status = 0
		}
		h.DB.Model(&lang).Update("status", status)
	}
	c.Redirect(http.StatusFound, "/admin/languages")
}

func (h *Handler) LanguageDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Language{}, id)
	c.Redirect(http.StatusFound, "/admin/languages")
}

// ── Customers ─────────────────────────────────────────────────────────────────

func (h *Handler) CustomerList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	search := c.Query("search")
	limit := 15
	offset := (page - 1) * limit

	var customers []models.User
	var total int64
	q := h.DB.Model(&models.User{}).Where("user_type = ?", "customer")
	if search != "" {
		q = q.Where("name LIKE ? OR email LIKE ?", "%"+search+"%", "%"+search+"%")
	}
	q.Count(&total)
	q.Order("created_at desc").Limit(limit).Offset(offset).Find(&customers)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/customers", gin.H{
		"User":      u,
		"Customers": customers,
		"Total":     total,
		"Page":      page,
		"Pages":     (int(total) + limit - 1) / limit,
		"Search":    search,
	})
}

func (h *Handler) CustomerBan(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var cu models.User
	if err := h.DB.Where("id = ? AND user_type = ?", id, "customer").First(&cu).Error; err == nil {
		banned := 1
		if cu.Banned == 1 {
			banned = 0
		}
		h.DB.Model(&cu).Update("banned", banned)
	}
	c.Redirect(http.StatusFound, "/admin/customers")
}

// ── Flash Deals ───────────────────────────────────────────────────────────────

func (h *Handler) FlashDealList(c *gin.Context) {
	u := currentUser(c)
	var deals []models.FlashDeal
	h.DB.Order("created_at desc").Find(&deals)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/flash_deals", gin.H{
		"User":  u,
		"Deals": deals,
	})
}

// nilStr returns a pointer to s, or nil if s is empty.
func nilStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// parseDatetimeLocal converts a datetime-local string ("2006-01-02T15:04") to a unix timestamp pointer.
func parseDatetimeLocal(s string) *int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02T15:04", s, time.Local)
	if err != nil {
		return nil
	}
	ts := int(t.Unix())
	return &ts
}

func (h *Handler) FlashDealStore(c *gin.Context) {
	title := c.PostForm("title")
	if title == "" {
		c.Redirect(http.StatusFound, "/admin/flash-deals")
		return
	}
	bg := c.PostForm("background_color")
	tc := c.PostForm("text_color")
	deal := models.FlashDeal{
		Title:           &title,
		Status:          1,
		StartDate:       parseDatetimeLocal(c.PostForm("start_date")),
		EndDate:         parseDatetimeLocal(c.PostForm("end_date")),
		BackgroundColor: nilStr(bg),
		TextColor:       nilStr(tc),
	}
	h.DB.Create(&deal)
	c.Redirect(http.StatusFound, "/admin/flash-deals")
}

func (h *Handler) FlashDealUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if id == 0 {
		c.Redirect(http.StatusFound, "/admin/flash-deals")
		return
	}
	title := c.PostForm("title")
	status, _ := strconv.Atoi(c.PostForm("status"))
	bg := c.PostForm("background_color")
	tc := c.PostForm("text_color")

	updates := map[string]interface{}{
		"title":            title,
		"status":           status,
		"start_date":       parseDatetimeLocal(c.PostForm("start_date")),
		"end_date":         parseDatetimeLocal(c.PostForm("end_date")),
		"background_color": nilStr(bg),
		"text_color":       nilStr(tc),
	}
	h.DB.Model(&models.FlashDeal{}).Where("id = ?", id).Updates(updates)
	c.Redirect(http.StatusFound, "/admin/flash-deals")
}

func (h *Handler) FlashDealDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.FlashDeal{}, id)
	c.Redirect(http.StatusFound, "/admin/flash-deals")
}

// ── Coupons ───────────────────────────────────────────────────────────────────

func (h *Handler) CouponList(c *gin.Context) {
	u := currentUser(c)
	var coupons []models.Coupon
	h.DB.Order("created_at desc").Find(&coupons)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/coupons", gin.H{
		"User":    u,
		"Coupons": coupons,
	})
}

func (h *Handler) CouponStore(c *gin.Context) {
	u := currentUser(c)
	discount, _ := strconv.ParseFloat(c.PostForm("discount"), 64)
	coupon := models.Coupon{
		UserID:       u.ID,
		Code:         c.PostForm("code"),
		Type:         "cart_base",
		Details:      c.PostForm("details"),
		Discount:     discount,
		DiscountType: c.PostForm("discount_type"),
		Status:       1,
	}
	h.DB.Create(&coupon)
	c.Redirect(http.StatusFound, "/admin/coupons")
}

func (h *Handler) CouponDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Coupon{}, id)
	c.Redirect(http.StatusFound, "/admin/coupons")
}

func (h *Handler) CouponUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if id == 0 {
		c.Redirect(http.StatusFound, "/admin/coupons")
		return
	}
	discount, _ := strconv.ParseFloat(c.PostForm("discount"), 64)
	status, _ := strconv.Atoi(c.PostForm("status"))
	updates := map[string]interface{}{
		"code":          c.PostForm("code"),
		"discount_type": c.PostForm("discount_type"),
		"discount":      discount,
		"details":       c.PostForm("details"),
		"status":        status,
	}
	h.DB.Model(&models.Coupon{}).Where("id = ?", id).Updates(updates)
	c.Redirect(http.StatusFound, "/admin/coupons")
}

// ── Newsletter ────────────────────────────────────────────────────────────────

func (h *Handler) NewsletterList(c *gin.Context) {
	u := currentUser(c)
	var nl []models.Newsletter
	h.DB.Order("created_at desc").Limit(50).Find(&nl)
	flash, _ := c.Cookie("nl_flash")
	flashType, _ := c.Cookie("nl_flash_type")
	c.SetCookie("nl_flash", "", -1, "/", "", false, false)
	c.SetCookie("nl_flash_type", "", -1, "/", "", false, false)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/newsletter", gin.H{
		"User":        u,
		"Newsletters": nl,
		"Flash":       flash,
		"FlashType":   flashType,
	})
}

// newsletterRecipients builds the deduplicated list of recipient emails
// based on recipient_type and optional custom criteria.
func (h *Handler) newsletterRecipients(
	recipientType, regFrom, regTo, spendMin, spendMax, includeSubs string,
) []string {
	emails := make(map[string]struct{})

	switch recipientType {
	case "subscribers":
		var subs []models.Subscriber
		h.DB.Find(&subs)
		for _, s := range subs {
			if s.Email != "" {
				emails[s.Email] = struct{}{}
			}
		}

	case "all_users":
		var users []models.User
		h.DB.Where("user_type = ? AND email IS NOT NULL AND email != ''", "customer").Find(&users)
		for _, u := range users {
			if u.Email != nil && *u.Email != "" {
				emails[*u.Email] = struct{}{}
			}
		}

	case "custom":
		// Build user query
		q := h.DB.Where("user_type = ? AND email IS NOT NULL AND email != ''", "customer")
		if regFrom != "" {
			q = q.Where("created_at >= ?", regFrom)
		}
		if regTo != "" {
			q = q.Where("created_at <= ?", regTo+" 23:59:59")
		}
		if spendMin != "" || spendMax != "" {
			sixMonthsAgo := time.Now().AddDate(0, -6, 0)
			sub := h.DB.Model(&models.Order{}).
				Select("user_id, SUM(grand_total) as total_spend").
				Where("payment_status = ? AND created_at >= ?", "paid", sixMonthsAgo).
				Group("user_id")

			if spendMin != "" {
				if min, err := strconv.ParseFloat(spendMin, 64); err == nil {
					sub = sub.Having("total_spend >= ?", min)
				}
			}
			if spendMax != "" {
				if max, err := strconv.ParseFloat(spendMax, 64); err == nil {
					sub = sub.Having("total_spend <= ?", max)
				}
			}
			var buyerIDs []uint
			sub.Pluck("user_id", &buyerIDs)
			if len(buyerIDs) > 0 {
				q = q.Where("id IN ?", buyerIDs)
			} else {
				q = q.Where("1=0") // no matching buyers → no results
			}
		}
		var users []models.User
		q.Find(&users)
		for _, u := range users {
			if u.Email != nil && *u.Email != "" {
				emails[*u.Email] = struct{}{}
			}
		}
		// optionally add subscribers too
		if includeSubs == "1" {
			var subs []models.Subscriber
			h.DB.Find(&subs)
			for _, s := range subs {
				if s.Email != "" {
					emails[s.Email] = struct{}{}
				}
			}
		}
	}

	out := make([]string, 0, len(emails))
	for e := range emails {
		out = append(out, e)
	}
	return out
}

func (h *Handler) NewsletterPreviewCount(c *gin.Context) {
	var req struct {
		RecipientType      string `json:"recipient_type"`
		RegFrom            string `json:"reg_from"`
		RegTo              string `json:"reg_to"`
		SpendMin           string `json:"spend_min"`
		SpendMax           string `json:"spend_max"`
		IncludeSubscribers string `json:"include_subscribers"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	recipients := h.newsletterRecipients(
		req.RecipientType, req.RegFrom, req.RegTo,
		req.SpendMin, req.SpendMax, req.IncludeSubscribers,
	)
	c.JSON(http.StatusOK, gin.H{"count": len(recipients)})
}

func (h *Handler) NewsletterSend(c *gin.Context) {
	subject := strings.TrimSpace(c.PostForm("subject"))
	body := strings.TrimSpace(c.PostForm("body"))
	recipientType := c.PostForm("recipient_type")
	if recipientType == "" {
		recipientType = "subscribers"
	}

	if subject == "" || body == "" {
		c.SetCookie("nl_flash", "Subject and body are required.", 10, "/", "", false, false)
		c.SetCookie("nl_flash_type", "danger", 10, "/", "", false, false)
		c.Redirect(http.StatusFound, "/admin/newsletter")
		return
	}

	recipients := h.newsletterRecipients(
		recipientType,
		c.PostForm("reg_from"), c.PostForm("reg_to"),
		c.PostForm("spend_min"), c.PostForm("spend_max"),
		c.PostForm("include_subscribers"),
	)

	if len(recipients) == 0 {
		c.SetCookie("nl_flash", "No recipients matched your criteria.", 10, "/", "", false, false)
		c.SetCookie("nl_flash_type", "warning", 10, "/", "", false, false)
		c.Redirect(http.StatusFound, "/admin/newsletter")
		return
	}

	// Import mail service inline to avoid circular import
	sent, failCount := h.sendNewsletterEmails(subject, body, recipients)

	// Save campaign record
	nl := models.Newsletter{
		Subject:       subject,
		Body:          body,
		RecipientType: recipientType,
		SentCount:     sent,
		FailCount:     failCount,
		Status:        "sent",
	}
	h.DB.Create(&nl)

	msg := fmt.Sprintf("Newsletter sent to %d recipient(s).", sent)
	if failCount > 0 {
		msg += fmt.Sprintf(" %d failed.", failCount)
	}
	flashType := "success"
	if failCount > 0 && sent == 0 {
		flashType = "danger"
	} else if failCount > 0 {
		flashType = "warning"
	}
	c.SetCookie("nl_flash", msg, 10, "/", "", false, false)
	c.SetCookie("nl_flash_type", flashType, 10, "/", "", false, false)
	c.Redirect(http.StatusFound, "/admin/newsletter")
}

// sendNewsletterEmails sends emails via configured SMTP; returns (sent, failed).
func (h *Handler) sendNewsletterEmails(subject, htmlBody string, recipients []string) (int, int) {
	sent, failed := 0, 0
	for _, to := range recipients {
		if err := h.sendOneMail(to, subject, htmlBody); err != nil {
			failed++
		} else {
			sent++
		}
	}
	return sent, failed
}

// sendOneMail sends a single HTML email via the configured SMTP settings.
func (h *Handler) sendOneMail(to, subject, htmlBody string) error {
	return mail.Send(h.Settings, mail.Message{To: to, Subject: subject, HTML: htmlBody})
}

// ContactList displays all contact form messages with pagination
func (h *Handler) ContactList(c *gin.Context) {
	u := currentUser(c)
	page := 1
	if p, err := strconv.Atoi(c.DefaultQuery("page", "1")); err == nil && p > 0 {
		page = p
	}
	pageSize := 20
	offset := (page - 1) * pageSize

	var contacts []models.Contact
	var total int64
	h.DB.Order("created_at DESC").Limit(pageSize).Offset(offset).Find(&contacts)
	h.DB.Model(&models.Contact{}).Count(&total)

	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))

	h.Engine.Render(c, http.StatusOK, "admin", "admin/contacts_list", gin.H{
		"User":       u,
		"Contacts":   contacts,
		"Page":       page,
		"TotalPages": totalPages,
		"Total":      total,
	})
}

// ContactDetail displays a single contact message
func (h *Handler) ContactDetail(c *gin.Context) {
	u := currentUser(c)
	id := c.Param("id")

	var contact models.Contact
	if err := h.DB.First(&contact, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/contacts")
		return
	}

	// Find prev/next IDs for navigation
	var prevID, nextID uint
	var prev, next models.Contact
	if h.DB.Where("id < ?", contact.ID).Order("id desc").First(&prev).Error == nil {
		prevID = prev.ID
	}
	if h.DB.Where("id > ?", contact.ID).Order("id asc").First(&next).Error == nil {
		nextID = next.ID
	}

	h.Engine.Render(c, http.StatusOK, "admin", "admin/contacts_detail", gin.H{
		"User":    u,
		"Contact": contact,
		"PrevID":  prevID,
		"NextID":  nextID,
	})
}

// ContactReply saves an internal reply note on a contact message
func (h *Handler) ContactReply(c *gin.Context) {
	id := c.Param("id")
	reply := strings.TrimSpace(c.PostForm("reply"))

	if reply != "" {
		h.DB.Model(&models.Contact{}).Where("id = ?", id).Update("reply", reply)
	}
	c.Redirect(http.StatusSeeOther, "/admin/contacts/"+id)
}

// ContactDelete deletes a contact message
func (h *Handler) ContactDelete(c *gin.Context) {
	id := c.Param("id")
	h.DB.Delete(&models.Contact{}, id)
	c.Redirect(http.StatusSeeOther, "/admin/contacts")
}

func (h *Handler) AISettings(c *gin.Context) {
	u := currentUser(c)
	var settings []models.BusinessSetting
	h.DB.Find(&settings)
	smap := make(map[string]string, len(settings))
	for _, s := range settings {
		if s.Value != nil {
			smap[s.Type] = *s.Value
		}
	}
	// Surface env-var defaults so the form shows something useful on first visit
	if smap["ai_provider"] == "" {
		smap["ai_provider"] = "ollama"
	}
	if smap["ollama_url"] == "" && h.OllamaURL != "" {
		smap["ollama_url"] = h.OllamaURL
	}
	if smap["ollama_model"] == "" && h.OllamaModel != "" {
		smap["ollama_model"] = h.OllamaModel
	}
	if smap["gemini_api_key"] == "" && h.GeminiAPIKey != "" {
		smap["gemini_api_key"] = h.GeminiAPIKey
	}
	_, success := c.GetQuery("success")
	h.Engine.Render(c, http.StatusOK, "admin", "admin/ai_settings", gin.H{
		"User":     u,
		"Settings": smap,
		"Success":  success,
	})
}

func (h *Handler) AISettingsUpdate(c *gin.Context) {
	keys := []string{
		"ai_provider",
		"ollama_url", "ollama_model", "ollama_api_key",
		"gemini_api_key", "gemini_model",
	}
	for _, key := range keys {
		val := c.PostForm(key)
		var count int64
		h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Count(&count)
		if count == 0 {
			h.DB.Create(&models.BusinessSetting{Type: key, Value: &val})
		} else {
			h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Update("value", val)
		}
	}
	h.Settings.Invalidate()
	c.Redirect(http.StatusFound, "/admin/ai-settings?success=1")
}

func (h *Handler) SMTPSettings(c *gin.Context) {
	u := currentUser(c)
	var settings []models.BusinessSetting
	h.DB.Find(&settings)
	smap := make(map[string]string, len(settings))
	for _, s := range settings {
		if s.Value != nil {
			smap[s.Type] = *s.Value
		}
	}
	_, success := c.GetQuery("success")
	h.Engine.Render(c, http.StatusOK, "admin", "admin/smtp_settings", gin.H{
		"User":     u,
		"Settings": smap,
		"Success":  success,
	})
}

func (h *Handler) SMTPSettingsUpdate(c *gin.Context) {
	keys := []string{
		"smtp_host", "smtp_port", "smtp_encryption",
		"smtp_username", "smtp_password",
		"smtp_from_name", "smtp_from_email",
	}
	for _, key := range keys {
		val := c.PostForm(key)
		var count int64
		h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Count(&count)
		if count == 0 {
			h.DB.Create(&models.BusinessSetting{Type: key, Value: &val})
		} else {
			h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Update("value", val)
		}
	}
	h.Settings.Invalidate()
	c.Redirect(http.StatusFound, "/admin/smtp-settings?success=1")
}

func (h *Handler) SMTPTest(c *gin.Context) {
	u := currentUser(c)
	if u == nil || u.Email == nil {
		c.String(http.StatusBadRequest, "No email on current user account.")
		return
	}
	if err := h.sendOneMail(*u.Email, "SMTP Test", "<h2>SMTP is working!</h2><p>Your store can send emails.</p>"); err != nil {
		c.String(http.StatusInternalServerError, "Failed: "+err.Error())
		return
	}
	c.String(http.StatusOK, "Test email sent to "+*u.Email+" ✓")
}

// ── Reports ───────────────────────────────────────────────────────────────────

func (h *Handler) SalesReport(c *gin.Context) {
	u := currentUser(c)

	type MonthSales struct {
		Month string
		Total float64
	}
	var rows []MonthSales
	h.DB.Raw(`SELECT DATE_FORMAT(created_at,'%Y-%m') AS month, COALESCE(SUM(grand_total),0) AS total
		FROM orders WHERE payment_status='paid'
		GROUP BY month ORDER BY month DESC LIMIT 12`).Scan(&rows)

	var totalRevenue float64
	h.DB.Model(&models.Order{}).Select("COALESCE(SUM(grand_total),0)").
		Where("payment_status = ?", "paid").Scan(&totalRevenue)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/reports_sales", gin.H{
		"User":         u,
		"MonthlySales": rows,
		"TotalRevenue": totalRevenue,
	})
}

// ── SEO ───────────────────────────────────────────────────────────────────────

func saveSEOMeta(db *gorm.DB, modelType string, modelID uint, c *gin.Context) int {
	score, _ := strconv.Atoi(c.PostForm("seo_score"))
	robotsIndex := 0
	for _, v := range c.PostFormArray("seo_robots_index") {
		if v == "1" {
			robotsIndex = 1
			break
		}
	}
	robotsFollow := 0
	for _, v := range c.PostFormArray("seo_robots_follow") {
		if v == "1" {
			robotsFollow = 1
			break
		}
	}

	vals := map[string]interface{}{
		"model_type":    modelType,
		"model_id":      modelID,
		"focus_keyword": c.PostForm("seo_focus_keyword"),
		"og_title":      c.PostForm("seo_og_title"),
		"og_description": c.PostForm("seo_og_description"),
		"og_image":      c.PostForm("seo_og_image"),
		"canonical_url": c.PostForm("seo_canonical_url"),
		"schema_type":   c.PostForm("seo_schema_type"),
		"robots_index":  robotsIndex,
		"robots_follow": robotsFollow,
		"seo_score":     score,
	}

	var seo models.SEOMeta
	res := db.Where("model_type = ? AND model_id = ?", modelType, modelID).First(&seo)
	if res.Error != nil {
		// No existing record — insert
		seo = models.SEOMeta{
			ModelType:     modelType,
			ModelID:       modelID,
			FocusKeyword:  vals["focus_keyword"].(string),
			OGTitle:       vals["og_title"].(string),
			OGDescription: vals["og_description"].(string),
			OGImage:       vals["og_image"].(string),
			CanonicalURL:  vals["canonical_url"].(string),
			SchemaType:    vals["schema_type"].(string),
			RobotsIndex:   robotsIndex,
			RobotsFollow:  robotsFollow,
			SEOScore:      score,
		}
		db.Create(&seo)
	} else {
		// Existing record — update all fields explicitly
		db.Model(&seo).Updates(vals)
	}
	return score
}

// ── Business Settings ─────────────────────────────────────────────────────────

func (h *Handler) BusinessSettings(c *gin.Context) {
	u := currentUser(c)
	var settings []models.BusinessSetting
	h.DB.Find(&settings)
	smap := make(map[string]string, len(settings))
	for _, s := range settings {
		if s.Value != nil {
			smap[s.Type] = *s.Value
		}
	}
	_, success := c.GetQuery("success")
	h.Engine.Render(c, http.StatusOK, "admin", "admin/business_settings", gin.H{
		"User":     u,
		"Settings": smap,
		"Success":  success,
	})
}

func (h *Handler) BusinessSettingsUpdate(c *gin.Context) {
	keys := []string{
		"website_name", "website_title", "meta_description",
		"header_logo", "footer_copyright",
		"promo_bar_text", "promo_bar_link",
		"gift_banner_title", "gift_banner_subtitle", "gift_banner_cta_text", "gift_banner_cta_link",
		"hero_slide_1", "hero_slide_1_link",
		"hero_slide_2", "hero_slide_2_link",
		"hero_slide_3", "hero_slide_3_link",
		"affiliate_status", "affiliate_commission", "affiliate_min_payout", "affiliate_cookie_days",
		"default_currency", "currency_symbol", "currency_symbol_position",
		"no_of_decimal_points", "decimal_separator", "thousands_separator",
		"commission", "seller_registration",
		"exchange_rate_api_key", "exchange_rate", "price_markup",
		"shipping_method", "shipping_cost", "free_shipping_threshold",
	}
	for _, key := range keys {
		val := c.PostForm(key)
		var count int64
		h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Count(&count)
		if count == 0 {
			if err := h.DB.Create(&models.BusinessSetting{Type: key, Value: &val}).Error; err != nil {
				c.AbortWithStatusJSON(500, gin.H{"error": err.Error()})
				return
			}
		} else {
			if err := h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Update("value", val).Error; err != nil {
				c.AbortWithStatusJSON(500, gin.H{"error": err.Error()})
				return
			}
		}
	}
	h.Settings.Invalidate()
	c.Redirect(http.StatusFound, "/admin/business-settings?success=1")
}

func (h *Handler) PaymentSettings(c *gin.Context) {
	u := currentUser(c)
	var settings []models.BusinessSetting
	h.DB.Find(&settings)
	smap := make(map[string]string, len(settings))
	for _, s := range settings {
		if s.Value != nil {
			smap[s.Type] = *s.Value
		}
	}
	_, success := c.GetQuery("success")
	h.Engine.Render(c, http.StatusOK, "admin", "admin/payment_settings", gin.H{
		"User":     u,
		"Settings": smap,
		"Success":  success,
	})
}

func (h *Handler) PaymentSettingsUpdate(c *gin.Context) {
	keys := []string{
		"paypal_enabled", "paypal_mode", "paypal_client_id", "paypal_secret", "paypal_webhook_id",
		"stripe_enabled", "stripe_mode", "stripe_publishable_key", "stripe_secret_key", "stripe_webhook_secret",
		"alipay_enabled", "alipay_mode", "alipay_app_id", "alipay_private_key", "alipay_public_key", "alipay_notify_url",
	}
	for _, key := range keys {
		val := c.PostForm(key)
		var count int64
		h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Count(&count)
		if count == 0 {
			h.DB.Create(&models.BusinessSetting{Type: key, Value: &val})
		} else {
			h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Update("value", val)
		}
	}
	h.Settings.Invalidate()
	c.Redirect(http.StatusFound, "/admin/payment-settings?success=1")
}

func (h *Handler) SocialMediaSettings(c *gin.Context) {
	u := currentUser(c)
	keys := make([]string, 0, len(social.Platforms))
	for _, p := range social.Platforms {
		keys = append(keys, p.Key)
	}
	var settings []models.BusinessSetting
	h.DB.Where("type IN ?", keys).Find(&settings)
	smap := make(map[string]string, len(settings))
	for _, s := range settings {
		if s.Value != nil {
			smap[s.Type] = *s.Value
		}
	}
	_, success := c.GetQuery("success")
	h.Engine.Render(c, http.StatusOK, "admin", "admin/social_media", gin.H{
		"User":     u,
		"Settings": smap,
		"Success":  success,
	})
}

func (h *Handler) SocialMediaSettingsUpdate(c *gin.Context) {
	for _, p := range social.Platforms {
		val := strings.TrimSpace(c.PostForm(p.Key))
		var count int64
		h.DB.Model(&models.BusinessSetting{}).Where("type = ?", p.Key).Count(&count)
		if count == 0 {
			h.DB.Create(&models.BusinessSetting{Type: p.Key, Value: &val})
		} else {
			h.DB.Model(&models.BusinessSetting{}).Where("type = ?", p.Key).Update("value", val)
		}
	}
	h.Settings.Invalidate()
	c.Redirect(http.StatusFound, "/admin/social-media?success=1")
}

// LogoUpload uploads a header logo image and saves its path into the header_logo setting.
func (h *Handler) LogoUpload(c *gin.Context) {
	file, fh, err := c.Request.FormFile("image")
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !isAllowedExt(ext, allowedImageExts) {
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}
	unique := "logo_" + strconv.FormatInt(time.Now().UnixNano(), 36) + ext
	subdir := filepath.Join(h.UploadDir, "logos")
	os.MkdirAll(subdir, 0755)
	dst := filepath.Join(subdir, unique)
	f, err := os.Create(dst)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		os.Remove(dst)
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}
	f.Close()

	val := "/uploads/logos/" + unique
	var count int64
	h.DB.Model(&models.BusinessSetting{}).Where("type = ?", "header_logo").Count(&count)
	if count == 0 {
		h.DB.Create(&models.BusinessSetting{Type: "header_logo", Value: &val})
	} else {
		h.DB.Model(&models.BusinessSetting{}).Where("type = ?", "header_logo").Update("value", val)
	}
	h.Settings.Invalidate()
	c.Redirect(http.StatusFound, "/admin/business-settings?success=1")
}

// HeroSlideUpload uploads an image for a hero slide (slot 1‒3) and saves its
// path into the matching business setting.
func (h *Handler) HeroSlideUpload(c *gin.Context) {
	slot := c.Param("slot") // "1", "2", or "3"
	key := "hero_slide_" + slot
	if slot != "1" && slot != "2" && slot != "3" {
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}

	file, fh, err := c.Request.FormFile("image")
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !isAllowedExt(ext, allowedImageExts) {
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}
	unique := "slide" + slot + "_" + strconv.FormatInt(time.Now().UnixNano(), 36) + ext
	subdir := filepath.Join(h.UploadDir, "slides")
	os.MkdirAll(subdir, 0755)
	dst := filepath.Join(subdir, unique)
	f, err := os.Create(dst)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		os.Remove(dst)
		c.Redirect(http.StatusFound, "/admin/business-settings")
		return
	}
	f.Close()

	val := "/uploads/slides/" + unique
	var count int64
	h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Count(&count)
	if count == 0 {
		h.DB.Create(&models.BusinessSetting{Type: key, Value: &val})
	} else {
		h.DB.Model(&models.BusinessSetting{}).Where("type = ?", key).Update("value", val)
	}
	h.Settings.Invalidate()
	c.Redirect(http.StatusFound, "/admin/business-settings?success=1")
}

// ── Affiliate ─────────────────────────────────────────────────────────────────

func (h *Handler) AffiliateList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const limit = 20
	offset := (page - 1) * limit

	var users []models.User
	var total int64
	q := h.DB.Model(&models.User{}).Where("referral_code IS NOT NULL AND referral_code != ''")
	q.Count(&total)
	q.Order("id desc").Limit(limit).Offset(offset).Find(&users)

	// Earnings per user via affiliate_logs
	type stat struct {
		UserID   uint
		Earnings float64
		Refs     int64
	}
	var stats []stat
	h.DB.Model(&models.AffiliateLog{}).
		Select("user_id, SUM(amount) as earnings, COUNT(*) as refs").
		Group("user_id").Scan(&stats)
	statsMap := map[uint]stat{}
	for _, s := range stats {
		statsMap[s.UserID] = s
	}

	pages := int((total + int64(limit) - 1) / int64(limit))
	h.Engine.Render(c, http.StatusOK, "admin", "admin/affiliates", gin.H{
		"User":     u,
		"Users":    users,
		"StatsMap": statsMap,
		"Total":    total,
		"Page":     page,
		"Pages":    pages,
	})
}

func (h *Handler) AffiliateToggle(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var user models.User
	if err := h.DB.First(&user, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/affiliates")
		return
	}
	if user.ReferralCode != nil && *user.ReferralCode != "" {
		empty := ""
		h.DB.Model(&user).Update("referral_code", &empty)
	} else {
		code := "AFF" + strconv.Itoa(int(user.ID))
		h.DB.Model(&user).Update("referral_code", &code)
	}
	c.Redirect(http.StatusFound, "/admin/affiliates")
}

func (h *Handler) AffiliateWithdrawList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const limit = 20
	offset := (page - 1) * limit
	status := c.DefaultQuery("status", "pending")

	var items []models.AffiliateWithdraw
	var total int64
	h.DB.Model(&models.AffiliateWithdraw{}).Where("status = ?", status).Count(&total)
	h.DB.Where("status = ?", status).Preload("User").
		Order("id desc").Limit(limit).Offset(offset).Find(&items)

	pages := int((total + int64(limit) - 1) / int64(limit))
	h.Engine.Render(c, http.StatusOK, "admin", "admin/affiliate_withdraws", gin.H{
		"User":   u,
		"Items":  items,
		"Status": status,
		"Total":  total,
		"Page":   page,
		"Pages":  pages,
	})
}

func (h *Handler) AffiliateWithdrawApprove(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	// Only transition from "pending" to prevent re-approving already-rejected requests.
	h.DB.Model(&models.AffiliateWithdraw{}).
		Where("id = ? AND status = 'pending'", id).
		Update("status", "approved")
	c.Redirect(http.StatusFound, "/admin/affiliate-withdraws")
}

func (h *Handler) AffiliateWithdrawReject(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	// Only transition from "pending" to prevent re-rejecting already-approved requests.
	h.DB.Model(&models.AffiliateWithdraw{}).
		Where("id = ? AND status = 'pending'", id).
		Update("status", "rejected")
	c.Redirect(http.StatusFound, "/admin/affiliate-withdraws")
}

// ── Attributes ────────────────────────────────────────────────────────────────

func (h *Handler) AttributeList(c *gin.Context) {
	u := currentUser(c)
	var attrs []models.Attribute
	h.DB.Preload("Values").Order("id desc").Find(&attrs)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/attributes", gin.H{
		"User":       u,
		"Attributes": attrs,
	})
}

func (h *Handler) AttributeStore(c *gin.Context) {
	name := c.PostForm("name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/attributes")
		return
	}
	h.DB.Create(&models.Attribute{Name: &name})
	c.Redirect(http.StatusFound, "/admin/attributes")
}

func (h *Handler) AttributeEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var attr models.Attribute
	if err := h.DB.Preload("Values").First(&attr, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/attributes")
		return
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/attribute_form", gin.H{
		"User":      u,
		"Attribute": &attr,
		"Error":     c.Query("error"),
	})
}

func (h *Handler) AttributeUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	name := c.PostForm("name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/attributes")
		return
	}
	h.DB.Model(&models.Attribute{}).Where("id = ?", id).Update("name", name)
	c.Redirect(http.StatusFound, "/admin/attributes")
}

func (h *Handler) AttributeDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Where("attribute_id = ?", id).Delete(&models.AttributeValue{})
	h.DB.Delete(&models.Attribute{}, id)
	c.Redirect(http.StatusFound, "/admin/attributes")
}

func (h *Handler) AttributeValueStore(c *gin.Context) {
	attrID, _ := strconv.Atoi(c.Param("id"))
	value := c.PostForm("value")
	if value == "" {
		c.Redirect(http.StatusFound, "/admin/attributes")
		return
	}
	h.DB.Create(&models.AttributeValue{AttributeID: uint(attrID), Value: value})
	c.Redirect(http.StatusFound, fmt.Sprintf("/admin/attributes/%d/edit", attrID))
}

func (h *Handler) AttributeValueDelete(c *gin.Context) {
	attrID, _ := strconv.Atoi(c.Param("id"))
	valID, _ := strconv.Atoi(c.Param("val_id"))

	var usageCount int64
	h.DB.Model(&models.SizeChartDetail{}).Where("attribute_value_id = ?", valID).Count(&usageCount)
	if usageCount > 0 {
		c.Redirect(http.StatusFound, fmt.Sprintf("/admin/attributes/%d/edit?error=in_use", attrID))
		return
	}

	h.DB.Where("id = ? AND attribute_id = ?", valID, attrID).Delete(&models.AttributeValue{})
	c.Redirect(http.StatusFound, fmt.Sprintf("/admin/attributes/%d/edit", attrID))
}

// ── Colors ────────────────────────────────────────────────────────────────────

func (h *Handler) ColorList(c *gin.Context) {
	u := currentUser(c)
	var colors []models.Color
	h.DB.Order("id desc").Find(&colors)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/colors", gin.H{
		"User":   u,
		"Colors": colors,
	})
}

func (h *Handler) ColorStore(c *gin.Context) {
	name := c.PostForm("name")
	code := c.PostForm("code")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/colors")
		return
	}
	h.DB.Create(&models.Color{Name: &name, Code: &code})
	c.Redirect(http.StatusFound, "/admin/colors")
}

func (h *Handler) ColorEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var color models.Color
	if err := h.DB.First(&color, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/colors")
		return
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/color_form", gin.H{
		"User":  u,
		"Color": &color,
	})
}

func (h *Handler) ColorUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	name := c.PostForm("name")
	code := c.PostForm("code")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/colors")
		return
	}
	h.DB.Model(&models.Color{}).Where("id = ?", id).Updates(map[string]interface{}{
		"name": name,
		"code": code,
	})
	c.Redirect(http.StatusFound, "/admin/colors")
}

func (h *Handler) ColorDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Color{}, id)
	c.Redirect(http.StatusFound, "/admin/colors")
}

// ── Warranties ────────────────────────────────────────────────────────────────

func (h *Handler) WarrantyList(c *gin.Context) {
	u := currentUser(c)
	var warranties []models.Warranty
	h.DB.Order("id desc").Find(&warranties)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/warranties", gin.H{
		"User":      u,
		"Warranties": warranties,
	})
}

func (h *Handler) WarrantyStore(c *gin.Context) {
	text := c.PostForm("text")
	if text == "" {
		c.Redirect(http.StatusFound, "/admin/warranties")
		return
	}
	h.DB.Create(&models.Warranty{Text: text})
	c.Redirect(http.StatusFound, "/admin/warranties")
}

func (h *Handler) WarrantyEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var warranty models.Warranty
	if err := h.DB.First(&warranty, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/warranties")
		return
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/warranty_form", gin.H{
		"User":     u,
		"Warranty": &warranty,
	})
}

func (h *Handler) WarrantyUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	text := c.PostForm("text")
	if text == "" {
		c.Redirect(http.StatusFound, "/admin/warranties")
		return
	}
	h.DB.Model(&models.Warranty{}).Where("id = ?", id).Update("text", text)
	c.Redirect(http.StatusFound, "/admin/warranties")
}

func (h *Handler) WarrantyDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Warranty{}, id)
	c.Redirect(http.StatusFound, "/admin/warranties")
}

// ── Custom Labels ─────────────────────────────────────────────────────────────

func (h *Handler) CustomLabelList(c *gin.Context) {
	u := currentUser(c)
	var labels []models.CustomLabel
	h.DB.Order("id desc").Find(&labels)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/custom_labels", gin.H{
		"User":   u,
		"Labels": labels,
	})
}

func (h *Handler) CustomLabelStore(c *gin.Context) {
	text := c.PostForm("text")
	bg := c.PostForm("background_color")
	tc := c.PostForm("text_color")
	if text == "" {
		c.Redirect(http.StatusFound, "/admin/custom-labels")
		return
	}
	u := currentUser(c)
	h.DB.Create(&models.CustomLabel{UserID: u.ID, Text: text, BackgroundColor: bg, TextColor: tc})
	c.Redirect(http.StatusFound, "/admin/custom-labels")
}

func (h *Handler) CustomLabelEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var label models.CustomLabel
	if err := h.DB.First(&label, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/custom-labels")
		return
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/custom_label_form", gin.H{
		"User":  u,
		"Label": &label,
	})
}

func (h *Handler) CustomLabelUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	text := c.PostForm("text")
	bg := c.PostForm("background_color")
	tc := c.PostForm("text_color")
	if text == "" {
		c.Redirect(http.StatusFound, "/admin/custom-labels")
		return
	}
	h.DB.Model(&models.CustomLabel{}).Where("id = ?", id).Updates(map[string]interface{}{
		"text":             text,
		"background_color": bg,
		"text_color":       tc,
	})
	c.Redirect(http.StatusFound, "/admin/custom-labels")
}

func (h *Handler) CustomLabelDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.CustomLabel{}, id)
	c.Redirect(http.StatusFound, "/admin/custom-labels")
}

// ── Delivery Boys ─────────────────────────────────────────────────────────────

func (h *Handler) DeliveryBoyList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit := 15
	offset := (page - 1) * limit

	var boys []models.User
	var total int64
	h.DB.Model(&models.User{}).Where("user_type = ?", "delivery_boy").Count(&total)
	h.DB.Where("user_type = ?", "delivery_boy").
		Order("created_at desc").Limit(limit).Offset(offset).Find(&boys)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/delivery_boys", gin.H{
		"User":  u,
		"Boys":  boys,
		"Total": total,
		"Page":  page,
		"Pages": (int(total) + limit - 1) / limit,
	})
}

func (h *Handler) DeliveryBoyEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var boy models.User
	if err := h.DB.Where("id = ? AND user_type = ?", id, "delivery_boy").First(&boy).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/delivery-boys")
		return
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/delivery_boy_form", gin.H{
		"User": u,
		"Boy":  &boy,
	})
}

func (h *Handler) DeliveryBoyUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	name := c.PostForm("name")
	email := c.PostForm("email")
	phone := c.PostForm("phone")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/delivery-boys")
		return
	}
	// Scope the update to delivery_boy rows only to prevent IDOR on other user types.
	h.DB.Model(&models.User{}).Where("id = ? AND user_type = ?", id, "delivery_boy").Updates(map[string]interface{}{
		"name":  name,
		"email": email,
		"phone": phone,
	})
	c.Redirect(http.StatusFound, "/admin/delivery-boys")
}

func (h *Handler) DeliveryBoyBan(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var boy models.User
	if err := h.DB.Where("id = ? AND user_type = ?", id, "delivery_boy").First(&boy).Error; err == nil {
		banned := 1
		if boy.Banned == 1 {
			banned = 0
		}
		h.DB.Model(&boy).Update("banned", banned)
	}
	c.Redirect(http.StatusFound, "/admin/delivery-boys")
}

// ── Addons ────────────────────────────────────────────────────────────────────

// ── Additional Reports ────────────────────────────────────────────────────────

func (h *Handler) EarningReport(c *gin.Context) {
	u := currentUser(c)

	type SellerEarning struct {
		SellerID uint
		Name     string
		Total    float64
	}
	var rows []SellerEarning
	h.DB.Raw(`SELECT o.seller_id, u.name, COALESCE(SUM(o.grand_total),0) AS total
		FROM orders o JOIN users u ON u.id = o.seller_id
		WHERE o.payment_status = 'paid' AND o.seller_id IS NOT NULL
		GROUP BY o.seller_id, u.name ORDER BY total DESC LIMIT 20`).Scan(&rows)

	var totalPlatform float64
	h.DB.Model(&models.Order{}).Select("COALESCE(SUM(grand_total),0)").
		Where("payment_status = ?", "paid").Scan(&totalPlatform)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/reports_earning", gin.H{
		"User":          u,
		"SellerRows":    rows,
		"TotalPlatform": totalPlatform,
	})
}

func (h *Handler) WishlistReport(c *gin.Context) {
	u := currentUser(c)

	type WishProduct struct {
		ProductID uint
		Name      string
		Count     int64
	}
	var rows []WishProduct
	h.DB.Raw(`SELECT w.product_id, p.name, COUNT(*) AS count
		FROM wishlists w JOIN products p ON p.id = w.product_id
		GROUP BY w.product_id, p.name ORDER BY count DESC LIMIT 20`).Scan(&rows)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/reports_wishlist", gin.H{
		"User": u,
		"Rows": rows,
	})
}

func (h *Handler) SearchReport(c *gin.Context) {
	u := currentUser(c)

	type SearchRow struct {
		Keyword string
		Count   int64
	}
	var rows []SearchRow
	h.DB.Raw(`SELECT query AS keyword, SUM(count) AS count FROM searches
		GROUP BY query ORDER BY count DESC LIMIT 30`).Scan(&rows)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/reports_search", gin.H{
		"User": u,
		"Rows": rows,
	})
}

// ── Reviews ───────────────────────────────────────────────────────────────────

func (h *Handler) ReviewList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	status := c.Query("status")
	search := c.Query("search")
	limit := 15
	offset := (page - 1) * limit

	var reviews []models.Review
	var total int64
	q := h.DB.Model(&models.Review{})

	// Filter by status
	if status != "" {
		if status == "approved" {
			q = q.Where("status = ?", 1)
		} else if status == "pending" {
			q = q.Where("status = ?", 0)
		}
	}

	// Filter by product name or comment
	if search != "" {
		q = q.Joins("LEFT JOIN products ON products.id = reviews.product_id").
			Where("products.name LIKE ? OR reviews.comment LIKE ?", "%"+search+"%", "%"+search+"%")
	}

	q.Count(&total)
	q.Order("reviews.created_at desc").Limit(limit).Offset(offset).
		Preload("Product").Preload("User").Find(&reviews)

	// Build reviewer verification data
	type ReviewerInfo struct {
		Review          *models.Review
		IsVerifiedBuyer bool
		IsEmailVerified bool
		IsBanned        bool
		ReviewerType    string // "registered_user", "guest", "custom"
	}

	var reviewerInfos []ReviewerInfo
	for i := range reviews {
		info := ReviewerInfo{
			Review:       &reviews[i],
			ReviewerType: "custom",
		}

		if reviews[i].UserID != nil {
			info.ReviewerType = "registered_user"

			// Check if user is verified buyer (has purchased this product)
			if reviews[i].ProductID > 0 && reviews[i].UserID != nil {
				var orderCount int64
				h.DB.Model(&models.Order{}).
					Joins("LEFT JOIN order_details ON order_details.order_id = orders.id").
					Where("orders.user_id = ? AND order_details.product_id = ? AND orders.payment_status = ?",
						*reviews[i].UserID, reviews[i].ProductID, "paid").
					Count(&orderCount)
				info.IsVerifiedBuyer = orderCount > 0
			}

			// Check user verification status
			if reviews[i].User != nil {
				info.IsEmailVerified = reviews[i].User.EmailVerifiedAt != nil
				info.IsBanned = reviews[i].User.Banned == 1
			}
		}

		reviewerInfos = append(reviewerInfos, info)
	}

	// Get list of products for re-linking deleted products
	var products []models.Product
	h.DB.Select("id", "name").Order("name ASC").Find(&products)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/reviews", gin.H{
		"User":          u,
		"Reviews":       reviews,
		"ReviewerInfos": reviewerInfos,
		"Products":      products,
		"Total":         total,
		"Page":          page,
		"Pages":         (int(total) + limit - 1) / limit,
		"Status":        status,
		"Search":        search,
	})
}

func (h *Handler) ReviewApprove(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Model(&models.Review{}).Where("id = ?", id).Update("status", 1)
	c.Redirect(http.StatusFound, "/admin/reviews")
}

func (h *Handler) ReviewReject(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Model(&models.Review{}).Where("id = ?", id).Update("status", 0)
	c.Redirect(http.StatusFound, "/admin/reviews")
}

func (h *Handler) ReviewDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Review{}, id)
	c.Redirect(http.StatusFound, "/admin/reviews")
}

func (h *Handler) ReviewRelinkProduct(c *gin.Context) {
	reviewID, _ := strconv.Atoi(c.Param("id"))
	productID, _ := strconv.Atoi(c.PostForm("product_id"))

	if productID <= 0 {
		c.Redirect(http.StatusFound, "/admin/orphan-reviews?error=Please select a valid product")
		return
	}

	// Verify product exists
	var product models.Product
	if err := h.DB.Where("id = ?", productID).First(&product).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/orphan-reviews?error=Product not found")
		return
	}

	// Update review with new product ID
	if err := h.DB.Model(&models.Review{}).Where("id = ?", reviewID).
		Update("product_id", productID).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/orphan-reviews?error=Failed to relink review")
		return
	}

	c.Redirect(http.StatusFound, "/admin/orphan-reviews?success=Review relinked successfully")
}

// ── Translation AJAX ──────────────────────────────────────────────────────────

// AdminTranslate accepts JSON {source_lang, fields:{title,short_description,...}}
// and returns translated fields for each of the other 7 content languages.
func (h *Handler) AdminTranslate(c *gin.Context) {
	var req struct {
		SourceLang  string            `json:"source_lang"`
		Fields      map[string]string `json:"fields"`
		TargetLangs []string          `json:"target_langs"` // optional; if empty translates to all langs
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.SourceLang == "" {
		req.SourceLang = "en"
	}

	targetSet := make(map[string]bool, len(req.TargetLangs))
	for _, code := range req.TargetLangs {
		targetSet[code] = true
	}

	// Read Ollama settings: DB settings take priority; fall back to env/config values
	// loaded at startup (h.OllamaURL / h.OllamaModel) so the server works even before
	// the admin has visited the AI Settings page.
	ollamaURL := h.Settings.Get("ollama_url", "")
	if ollamaURL == "" {
		ollamaURL = h.OllamaURL
	}
	ollamaModel := h.Settings.Get("ollama_model", "")
	if ollamaModel == "" {
		ollamaModel = h.OllamaModel
	}
	// The translate service reads its bearer token from OLLAMA_API_KEY. Seed it
	// from the DB setting so hosted providers (DashScope/OpenAI) authenticate even
	// when the container has no such env var configured.
	if k := strings.TrimSpace(h.Settings.Get("ollama_api_key", "")); k != "" {
		_ = os.Setenv("OLLAMA_API_KEY", k)
	}

	fmt.Printf("[ADMIN_TRANSLATE] Starting translation request\n")
	fmt.Printf("[ADMIN_TRANSLATE] Ollama URL: '%s'\n", ollamaURL)
	fmt.Printf("[ADMIN_TRANSLATE] Ollama Model: '%s'\n", ollamaModel)
	fmt.Printf("[ADMIN_TRANSLATE] Source Language: %s\n", req.SourceLang)
	fmt.Printf("[ADMIN_TRANSLATE] Fields to translate: %v\n", req.TargetLangs)

	if ollamaURL == "" {
		fmt.Printf("[ADMIN_TRANSLATE] ERROR: Ollama URL is empty!\n")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Ollama URL not configured. Please go to Admin > Settings > AI Settings and configure the Ollama endpoint.",
		})
		return
	}
	if ollamaModel == "" {
		fmt.Printf("[ADMIN_TRANSLATE] ERROR: Ollama Model is empty!\n")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Ollama model not configured. Please go to Admin > Settings > AI Settings and configure the model name.",
		})
		return
	}

	fmt.Printf("[ADMIN_TRANSLATE] Settings validated, proceeding with translation (parallel)\n")

	// Collect target languages to process
	var langsToProcess []translate.LangInfo
	for _, lang := range translate.Langs {
		if lang.Code == req.SourceLang {
			continue
		}
		if len(targetSet) > 0 && !targetSet[lang.Code] {
			continue
		}
		langsToProcess = append(langsToProcess, lang)
	}
	fmt.Printf("[ADMIN_TRANSLATE] Will translate to %d languages in parallel\n", len(langsToProcess))

	// Prepare fields — no truncation; full text is sent for every field
	preparedFields := make(map[string]string, len(req.Fields))
	for field, text := range req.Fields {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		preparedFields[field] = text
	}

	// All fields for a language are translated in ONE combined call (see
	// translate.TranslateFields) so a short title borrows context from the
	// longer description, then the reply is split back per field. A semaphore
	// limits concurrency to 1: Ollama is single-threaded on GPU, so flooding it
	// causes timeouts for late-queued requests.
	results := make(map[string]map[string]string)
	var firstErr error
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 1) // only 1 concurrent Ollama request

	for _, lang := range langsToProcess {
		wg.Add(1)
		go func(l translate.LangInfo) {
			defer wg.Done()
			sem <- struct{}{}        // acquire slot
			defer func() { <-sem }() // release slot

			fieldsOut, err := translate.TranslateFields(preparedFields, req.SourceLang, l.Code, ollamaURL, ollamaModel)
			if err != nil {
				fmt.Printf("[ADMIN_TRANSLATE] [%s] ERROR: %v\n", l.Code, err)
			} else {
				fmt.Printf("[ADMIN_TRANSLATE] [%s] OK (%d fields)\n", l.Code, len(fieldsOut))
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if len(fieldsOut) > 0 {
				results[l.Code] = fieldsOut
			}
		}(lang)
	}
	wg.Wait()

	fmt.Printf("[ADMIN_TRANSLATE] Done. Results: %d languages\n", len(results))

	if firstErr != nil && len(results) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": firstErr.Error()})
		return
	}

	c.JSON(http.StatusOK, results)
}

// ── Blogs ─────────────────────────────────────────────────────────────────────

func (h *Handler) BlogList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	search := c.Query("search")
	status := c.Query("status") // "1"=published, "0"=draft, ""=all
	limit := 15
	offset := (page - 1) * limit

	q := h.DB.Model(&models.Blog{}).Where("deleted_at IS NULL")
	if search != "" {
		q = q.Where("title LIKE ? OR slug LIKE ?", "%"+search+"%", "%"+search+"%")
	}
	if status == "1" || status == "0" {
		q = q.Where("status = ?", status)
	}

	var total int64
	q.Count(&total)

	var blogs []models.Blog
	q.Order("id desc").Limit(limit).Offset(offset).Find(&blogs)

	var cats []models.BlogCategory
	h.DB.Where("deleted_at IS NULL").Order("category_name asc").Find(&cats)

	catMap := make(map[uint]string, len(cats))
	for _, cat := range cats {
		catMap[cat.ID] = cat.CategoryName
	}

	h.Engine.Render(c, http.StatusOK, "admin", "admin/blogs", gin.H{
		"User":    u,
		"Blogs":   blogs,
		"CatMap":  catMap,
		"Total":   total,
		"Page":    page,
		"Pages":   (int(total) + limit - 1) / limit,
		"Search":  search,
		"Status":  status,
	})
}

func (h *Handler) BlogCreate(c *gin.Context) {
	u := currentUser(c)
	var cats []models.BlogCategory
	h.DB.Where("deleted_at IS NULL").Order("category_name asc").Find(&cats)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/blog_form", gin.H{
		"User":       u,
		"Categories": cats,
		"Blog":       nil,
		"Langs":      translate.Langs,
		"TransMap":   map[string]models.BlogTranslation{},
	})
}

func (h *Handler) BlogStore(c *gin.Context) {
	sourceLang := c.PostForm("source_lang")
	if sourceLang == "" {
		sourceLang = "en"
	}
	title := c.PostForm("title_" + sourceLang)
	if title == "" {
		c.Redirect(http.StatusFound, "/admin/blogs/create")
		return
	}
	sl := c.PostForm("slug")
	if sl == "" {
		sl = slugify(title)
	}
	catID, _ := strconv.ParseUint(c.PostForm("category_id"), 10, 64)
	statusVal, _ := strconv.Atoi(c.PostForm("status"))
	news, _ := strconv.Atoi(c.PostForm("news"))
	event, _ := strconv.Atoi(c.PostForm("event"))
	goingOn, _ := strconv.Atoi(c.PostForm("going_on"))

	now := time.Now()
	blog := models.Blog{
		CategoryID:       uint(catID),
		Title:            title,
		Slug:             sl,
		ShortDescription: ptrStr(c.PostForm("short_description_" + sourceLang)),
		Description:      ptrStr(c.PostForm("description_" + sourceLang)),
		MetaTitle:        ptrStr(c.PostForm("meta_title_" + sourceLang)),
		MetaDescription:  ptrStr(c.PostForm("meta_description_" + sourceLang)),
		MetaKeywords:     ptrStr(c.PostForm("meta_keywords_" + sourceLang)),
		Status:           statusVal,
		News:             news,
		Event:            event,
		GoingOn:          goingOn,
		CreatedAt:        &now,
		UpdatedAt:        &now,
	}
	h.DB.Create(&blog)

	for _, lang := range translate.Langs {
		t := c.PostForm("title_" + lang.Code)
		if t == "" {
			continue
		}
		tr := models.BlogTranslation{
			BlogID:           blog.ID,
			Lang:             lang.Code,
			Title:            t,
			ShortDescription: ptrStr(c.PostForm("short_description_" + lang.Code)),
			Description:      ptrStr(c.PostForm("description_" + lang.Code)),
			MetaTitle:        ptrStr(c.PostForm("meta_title_" + lang.Code)),
			MetaDescription:  ptrStr(c.PostForm("meta_description_" + lang.Code)),
			MetaKeywords:     ptrStr(c.PostForm("meta_keywords_" + lang.Code)),
		}
		h.DB.Create(&tr)
	}
	c.Redirect(http.StatusFound, "/admin/blogs")
}

func (h *Handler) BlogEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var blog models.Blog
	if err := h.DB.Preload("Translations").Where("id = ? AND deleted_at IS NULL", id).First(&blog).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/blogs")
		return
	}
	transMap := make(map[string]models.BlogTranslation, len(blog.Translations))
	for _, tr := range blog.Translations {
		transMap[tr.Lang] = tr
	}
	var cats []models.BlogCategory
	h.DB.Where("deleted_at IS NULL").Order("category_name asc").Find(&cats)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/blog_form", gin.H{
		"User":       u,
		"Categories": cats,
		"Blog":       &blog,
		"Langs":      translate.Langs,
		"TransMap":   transMap,
	})
}

func (h *Handler) BlogUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var blog models.Blog
	if err := h.DB.Where("id = ? AND deleted_at IS NULL", id).First(&blog).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/blogs")
		return
	}

	sourceLang := c.PostForm("source_lang")
	if sourceLang == "" {
		sourceLang = "en"
	}
	title := c.PostForm("title_" + sourceLang)
	sl := c.PostForm("slug")
	if sl == "" {
		sl = slugify(title)
	}
	catID, _ := strconv.ParseUint(c.PostForm("category_id"), 10, 64)
	statusVal, _ := strconv.Atoi(c.PostForm("status"))
	news, _ := strconv.Atoi(c.PostForm("news"))
	event, _ := strconv.Atoi(c.PostForm("event"))
	goingOn, _ := strconv.Atoi(c.PostForm("going_on"))

	now := time.Now()
	updates := map[string]interface{}{
		"title":             title,
		"slug":              sl,
		"category_id":       uint(catID),
		"status":            statusVal,
		"news":              news,
		"event":             event,
		"going_on":          goingOn,
		"short_description": c.PostForm("short_description_" + sourceLang),
		"description":       c.PostForm("description_" + sourceLang),
		"meta_title":        c.PostForm("meta_title_" + sourceLang),
		"meta_description":  c.PostForm("meta_description_" + sourceLang),
		"meta_keywords":     c.PostForm("meta_keywords_" + sourceLang),
		"updated_at":        &now,
	}
	h.DB.Model(&blog).Updates(updates)

	// Replace all translations
	h.DB.Where("blog_id = ?", blog.ID).Delete(&models.BlogTranslation{})
	for _, lang := range translate.Langs {
		t := c.PostForm("title_" + lang.Code)
		if t == "" {
			continue
		}
		tr := models.BlogTranslation{
			BlogID:           blog.ID,
			Lang:             lang.Code,
			Title:            t,
			ShortDescription: ptrStr(c.PostForm("short_description_" + lang.Code)),
			Description:      ptrStr(c.PostForm("description_" + lang.Code)),
			MetaTitle:        ptrStr(c.PostForm("meta_title_" + lang.Code)),
			MetaDescription:  ptrStr(c.PostForm("meta_description_" + lang.Code)),
			MetaKeywords:     ptrStr(c.PostForm("meta_keywords_" + lang.Code)),
		}
		h.DB.Create(&tr)
	}
	c.Redirect(http.StatusFound, "/admin/blogs")
}

func (h *Handler) BlogDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	now := time.Now()
	h.DB.Model(&models.Blog{}).Where("id = ?", id).Update("deleted_at", &now)
	c.Redirect(http.StatusFound, "/admin/blogs")
}

// BlogUploadImage handles banner/meta-img uploads for a blog post.
// Field name determines which column to update: "banner" or "meta_img".
func (h *Handler) BlogUploadImage(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	field := c.Param("field") // "banner" or "meta-img"

	file, fh, err := c.Request.FormFile("image")
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/blogs/"+strconv.Itoa(id)+"/edit")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !isAllowedExt(ext, allowedImageExts) {
		c.Redirect(http.StatusFound, "/admin/blogs/"+strconv.Itoa(id)+"/edit")
		return
	}
	unique := strconv.FormatInt(time.Now().UnixNano(), 36) + ext
	dst := filepath.Join(h.UploadDir, unique)
	if f, err := os.Create(dst); err == nil {
		if _, cpErr := io.Copy(f, file); cpErr != nil {
			f.Close()
			os.Remove(dst)
			c.Redirect(http.StatusFound, "/admin/blogs/"+strconv.Itoa(id)+"/edit")
			return
		}
		f.Close()
	} else {
		c.Redirect(http.StatusFound, "/admin/blogs/"+strconv.Itoa(id)+"/edit")
		return
	}

	orig := fh.Filename
	size := int(fh.Size)
	extStr := strings.TrimPrefix(ext, ".")
	typeStr := "image"
	uploadPath := "uploads/" + unique
	upload := models.Upload{
		FileOriginalName: &orig,
		FileName:         &uploadPath,
		FileSize:         &size,
		Extension:        &extStr,
		Type:             &typeStr,
	}
	h.DB.Create(&upload)

	col := "banner"
	if field == "meta-img" {
		col = "meta_img"
	}
	h.DB.Model(&models.Blog{}).Where("id = ?", id).Update(col, upload.ID)
	c.Redirect(http.StatusFound, "/admin/blogs/"+strconv.Itoa(id)+"/edit")
}

// ── Blog Categories ───────────────────────────────────────────────────────────

func (h *Handler) BlogCategoryList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	search := c.Query("search")
	limit := 15
	offset := (page - 1) * limit

	q := h.DB.Model(&models.BlogCategory{}).Where("deleted_at IS NULL")
	if search != "" {
		q = q.Where("category_name LIKE ? OR slug LIKE ?", "%"+search+"%", "%"+search+"%")
	}

	var total int64
	q.Count(&total)
	var cats []models.BlogCategory
	q.Order("id desc").Limit(limit).Offset(offset).Find(&cats)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/blog_categories", gin.H{
		"User":       u,
		"Categories": cats,
		"Total":      total,
		"Page":       page,
		"Pages":      (int(total) + limit - 1) / limit,
		"Search":     search,
	})
}

func (h *Handler) BlogCategoryStore(c *gin.Context) {
	name := c.PostForm("category_name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/blog-categories")
		return
	}
	sl := c.PostForm("slug")
	if sl == "" {
		sl = slugify(name)
	}
	now := time.Now()
	h.DB.Create(&models.BlogCategory{
		CategoryName: name,
		Slug:         sl,
		CreatedAt:    &now,
		UpdatedAt:    &now,
	})
	c.Redirect(http.StatusFound, "/admin/blog-categories")
}

func (h *Handler) BlogCategoryDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	now := time.Now()
	h.DB.Model(&models.BlogCategory{}).Where("id = ?", id).Update("deleted_at", &now)
	c.Redirect(http.StatusFound, "/admin/blog-categories")
}

// ── helpers ───────────────────────────────────────────────────────────────────

func ptrStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ── Role Management ────────────────────────────────────────────────────────────

func (h *Handler) RoleList(c *gin.Context) {
	u := currentUser(c)
	var roles []models.Role
	h.DB.Order("id asc").Find(&roles)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/roles", gin.H{
		"User":  u,
		"Roles": roles,
	})
}

func (h *Handler) RoleCreate(c *gin.Context) {
	u := currentUser(c)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/role_form", gin.H{
		"User": u,
	})
}

func (h *Handler) RoleStore(c *gin.Context) {
	name := c.PostForm("name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/roles?error=Name is required")
		return
	}
	role := models.Role{
		Name:      name,
		GuardName: "web",
	}
	if err := h.DB.Create(&role).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/roles?error="+url.QueryEscape(err.Error()))
		return
	}
	c.Redirect(http.StatusFound, "/admin/roles?success=Role created")
}

func (h *Handler) RoleEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var role models.Role
	if err := h.DB.First(&role, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/roles")
		return
	}
	h.Engine.Render(c, http.StatusOK, "admin", "admin/role_form", gin.H{
		"User": u,
		"Role": role,
	})
}

func (h *Handler) RoleUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	name := c.PostForm("name")
	if name == "" {
		c.Redirect(http.StatusFound, "/admin/roles?error=Name is required")
		return
	}
	if err := h.DB.Model(&models.Role{}).Where("id = ?", id).Update("name", name).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/roles?error="+url.QueryEscape(err.Error()))
		return
	}
	c.Redirect(http.StatusFound, "/admin/roles?success=Role updated")
}

func (h *Handler) RoleDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Delete(&models.Role{}, id)
	c.Redirect(http.StatusFound, "/admin/roles?success=Role deleted")
}

// ── Role Permission Matrix ─────────────────────────────────────────────────────

type permGroup struct {
	Label       string
	Permissions []models.Permission
}

func (h *Handler) RolePermissions(c *gin.Context) {
	u := currentUser(c)

	var roles []models.Role
	h.DB.Where("name NOT IN ?", []string{"admin"}).Order("id asc").Find(&roles)

	var perms []models.Permission
	h.DB.Order("section asc, name asc").Find(&perms)

	var assigned []models.RoleHasPermission
	h.DB.Find(&assigned)
	assignedMap := make(map[string]bool, len(assigned))
	for _, a := range assigned {
		assignedMap[fmt.Sprintf("%d_%d", a.RoleID, a.PermissionID)] = true
	}

	// Group permissions by Section.
	groupMap := make(map[string][]models.Permission)
	groupOrder := []string{}
	for _, p := range perms {
		sec := "General"
		if p.Section != nil && *p.Section != "" {
			sec = *p.Section
		}
		if _, exists := groupMap[sec]; !exists {
			groupOrder = append(groupOrder, sec)
		}
		groupMap[sec] = append(groupMap[sec], p)
	}
	groups := make([]permGroup, 0, len(groupOrder))
	for _, sec := range groupOrder {
		groups = append(groups, permGroup{Label: sec, Permissions: groupMap[sec]})
	}

	h.Engine.Render(c, http.StatusOK, "admin", "admin/role_permissions", gin.H{
		"User":     u,
		"Roles":    roles,
		"Groups":   groups,
		"Assigned": assignedMap,
	})
}

func (h *Handler) RolePermissionsUpdate(c *gin.Context) {
	var roles []models.Role
	h.DB.Where("name NOT IN ?", []string{"admin"}).Find(&roles)

	var perms []models.Permission
	h.DB.Find(&perms)

	roleIDs := make([]uint, len(roles))
	for i, r := range roles {
		roleIDs[i] = r.ID
	}

	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		// Delete all existing grants for non-admin roles, then re-insert the checked ones.
		if len(roleIDs) > 0 {
			if err := tx.Where("role_id IN ?", roleIDs).Delete(&models.RoleHasPermission{}).Error; err != nil {
				return fmt.Errorf("delete permissions: %w", err)
			}
		}
		for _, role := range roles {
			for _, perm := range perms {
				key := fmt.Sprintf("grant_%d_%d", role.ID, perm.ID)
				if c.PostForm(key) == "1" {
					if err := tx.Exec("INSERT IGNORE INTO role_has_permissions (role_id, permission_id) VALUES (?, ?)", role.ID, perm.ID).Error; err != nil {
						return fmt.Errorf("insert permission role=%d perm=%d: %w", role.ID, perm.ID, err)
					}
				}
			}
		}
		return nil
	})

	if txErr != nil {
		c.Redirect(http.StatusFound, "/admin/role-permissions?error=Failed+to+update+permissions")
		return
	}
	c.Redirect(http.StatusFound, "/admin/role-permissions?success=Permissions+updated")
}

// ── User Management ────────────────────────────────────────────────────────────

func (h *Handler) UserList(c *gin.Context) {
	u := currentUser(c)
	search := c.Query("search")
	status := c.Query("status")

	query := h.DB.Where("user_type != ?", "super_admin").Order("id desc")
	if search != "" {
		query = query.Where("name LIKE ? OR email LIKE ?", "%"+search+"%", "%"+search+"%")
	}
	if status != "" {
		if status == "banned" {
			query = query.Where("banned = 1")
		} else if status == "active" {
			query = query.Where("banned = 0")
		}
	}

	var users []models.User
	query.Find(&users)

	var roles []models.Role
	h.DB.Find(&roles)

	// Load user roles from staff table
	var staffs []models.Staff
	h.DB.Find(&staffs)
	userRoleMap := make(map[uint]uint)
	for _, staff := range staffs {
		userRoleMap[staff.UserID] = staff.RoleID
	}

	h.Engine.Render(c, http.StatusOK, "admin", "admin/users", gin.H{
		"User":        u,
		"Users":       users,
		"Roles":       roles,
		"UserRoleMap": userRoleMap,
		"Search":      search,
		"Status":      status,
	})
}

func (h *Handler) UserAssignRole(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		c.Redirect(http.StatusFound, "/admin/users?error=Invalid+user")
		return
	}
	roleID, _ := strconv.Atoi(c.PostForm("role_id"))

	if roleID > 0 {
		// Look up the role so we can mirror its name into users.user_type below.
		// The /admin middleware checks users.user_type — without this mirror,
		// the staff row alone never grants admin access.
		var role models.Role
		if err := h.DB.First(&role, roleID).Error; err != nil {
			c.Redirect(http.StatusFound, "/admin/users?error=Invalid+role")
			return
		}

		// staff table holds the user→role mapping; upsert by user_id.
		var staff models.Staff
		result := h.DB.Where("user_id = ?", userID).First(&staff)
		if result.Error != nil {
			// No existing record — create one.
			staff = models.Staff{UserID: uint(userID), RoleID: uint(roleID)}
			if err := h.DB.Create(&staff).Error; err != nil {
				c.Redirect(http.StatusFound, "/admin/users?error=Failed+to+assign+role")
				return
			}
		} else {
			// Update existing record.
			if err := h.DB.Model(&staff).Update("role_id", uint(roleID)).Error; err != nil {
				c.Redirect(http.StatusFound, "/admin/users?error=Failed+to+assign+role")
				return
			}
		}

		// Mirror the role into users.user_type so the /admin middleware
		// recognises this user. Roles in the DB use Title-Case names ("Admin",
		// "Super Admin", "Operation"); normalise to the lowercase tokens the
		// middleware checks: admin / super_admin / operation / finance.
		// Anything outside that whitelist gets "staff" (no admin access) so we
		// never grant /admin to a custom role the middleware doesn't know.
		userType := strings.ToLower(strings.ReplaceAll(role.Name, " ", "_"))
		switch userType {
		case "admin", "super_admin", "operation", "finance":
			// allowed as-is
		default:
			userType = "staff"
		}
		if err := h.DB.Model(&models.User{}).
			Where("id = ? AND user_type != ?", userID, "super_admin").
			Update("user_type", userType).Error; err != nil {
			c.Redirect(http.StatusFound, "/admin/users?error=Failed+to+update+user+type")
			return
		}
	}
	c.Redirect(http.StatusFound, "/admin/users?success=User+role+updated")
}

func (h *Handler) UserCreate(c *gin.Context) {
	u := currentUser(c)
	var roles []models.Role
	h.DB.Find(&roles)
	h.Engine.Render(c, http.StatusOK, "admin", "admin/user_form", gin.H{
		"User":  u,
		"Roles": roles,
	})
}

func (h *Handler) UserStore(c *gin.Context) {
	name := c.PostForm("name")
	email := c.PostForm("email")
	password := c.PostForm("password")
	userType := c.PostForm("user_type")

	if name == "" || email == "" || password == "" {
		c.Redirect(http.StatusFound, "/admin/users/create?error=Name, email, and password are required")
		return
	}

	if userType == "" {
		userType = "customer"
	}
	// Allowlist user_type to prevent privilege escalation: a regular admin must
	// not be able to create "admin" or hidden "super_admin" accounts via this form.
	validUserTypes := map[string]bool{
		"customer":     true,
		"seller":       true,
		"delivery_boy": true,
		"operation":    true,
		"finance":      true,
	}
	if !validUserTypes[userType] {
		userType = "customer"
	}

	hashedPwd, err := hashPassword(password)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/users/create?error=Failed+to+hash+password")
		return
	}
	user := models.User{
		Name:     name,
		Email:    &email,
		Password: &hashedPwd,
		UserType: userType,
	}

	if err := h.DB.Create(&user).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/users/create?error="+url.QueryEscape(err.Error()))
		return
	}

	c.Redirect(http.StatusFound, "/admin/users?success=User created successfully")
}

func (h *Handler) UserEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))

	var user models.User
	if err := h.DB.First(&user, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/users")
		return
	}
	// Super admin accounts are hidden from and untouchable by regular admins.
	if user.UserType == "super_admin" {
		c.Redirect(http.StatusFound, "/admin/users")
		return
	}

	var roles []models.Role
	h.DB.Find(&roles)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/user_form", gin.H{
		"User":   u,
		"Edit":   user,
		"Roles":  roles,
	})
}

func (h *Handler) UserUpdate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	name := c.PostForm("name")
	email := c.PostForm("email")
	password := c.PostForm("password")
	userType := c.PostForm("user_type")

	if name == "" || email == "" {
		c.Redirect(http.StatusFound, "/admin/users/"+c.Param("id")+"/edit?error=Name and email are required")
		return
	}

	// Super admin accounts are hidden from and untouchable by regular admins —
	// reject any attempt to edit one even if the (guessable) ID is supplied directly.
	var target models.User
	if h.DB.First(&target, id).Error == nil && target.UserType == "super_admin" {
		c.Redirect(http.StatusFound, "/admin/users?error=Cannot+edit+super+admin+account")
		return
	}

	// Allowlist user_type to prevent privilege escalation to "admin".
	validUserTypes := map[string]bool{
		"customer":     true,
		"seller":       true,
		"delivery_boy": true,
		"operation":    true,
		"finance":      true,
	}
	if !validUserTypes[userType] {
		userType = "customer"
	}

	updates := map[string]interface{}{
		"name":      name,
		"email":     &email,
		"user_type": userType,
	}

	if password != "" {
		hashedPwd, err := hashPassword(password)
		if err != nil {
			c.Redirect(http.StatusFound, "/admin/users/"+c.Param("id")+"/edit?error=Failed+to+hash+password")
			return
		}
		updates["password"] = &hashedPwd
	}

	h.DB.Model(&models.User{}).Where("id = ?", id).Updates(updates)
	c.Redirect(http.StatusFound, "/admin/users/"+c.Param("id")+"/edit?success=User updated successfully")
}

func (h *Handler) UserDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	// Prevent self-deletion.
	if u := currentUser(c); u != nil && u.ID == uint(id) {
		c.Redirect(http.StatusFound, "/admin/users?error=Cannot+delete+your+own+account")
		return
	}
	// Prevent deletion of super_admin users.
	var target models.User
	if h.DB.First(&target, id).Error == nil && target.UserType == "super_admin" {
		c.Redirect(http.StatusFound, "/admin/users?error=Cannot+delete+super+admin+account")
		return
	}
	if err := h.DB.Delete(&models.User{}, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/users?error="+url.QueryEscape(err.Error()))
		return
	}
	c.Redirect(http.StatusFound, "/admin/users?success=User deleted successfully")
}

func slugify(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' {
			out = append(out, ch+32)
		} else if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			out = append(out, ch)
		} else if ch == ' ' || ch == '_' || ch == '-' {
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	return string(out)
}

// ── Product Import/Export ───────────────────────────────────────────────────

func (h *Handler) ProductImportForm(c *gin.Context) {
	h.Engine.Render(c, http.StatusOK, "admin", "admin/products_import", gin.H{
		"success": c.Query("success"),
		"error":   c.Query("error"),
	})
}

// xlsxRow holds parsed cell values for one spreadsheet row.
// Columns: A=name B=description C=category D=unit_price E=current_stock
//          F=sku G=variants H=thumbnail_img I=images J=product_url (export only, derived from slug)
//          K=source_1688_url L=min_qty
type xlsxRow struct {
	Name, Description, Category, SKU, Variants, Thumbnail, Images string
	Source1688URL                                                 string
	Price                                                         float64
	Stock, MinQty                                                 int
}

func parseXLSXUpload(data []byte) ([]xlsxRow, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}

	// Read shared strings
	sst, err := readXLSXSharedStrings(zr)
	if err != nil {
		return nil, err
	}

	// Read worksheet
	ws, err := readXLSXWorksheet(zr)
	if err != nil {
		return nil, err
	}

	strVal := func(cell xlsxCell) string {
		switch cell.T {
		case "s":
			idx, _ := strconv.Atoi(cell.V)
			if idx < len(sst) {
				return sst[idx]
			}
		case "inlineStr":
			return cell.IS.T
		}
		return cell.V
	}

	var rows []xlsxRow
	for _, row := range ws.SheetData.Rows {
		if row.R == 1 {
			continue // header
		}
		cm := make(map[string]xlsxCell)
		for _, c := range row.Cells {
			cm[c.R] = c
		}
		ref := func(col string) string {
			return strVal(cm[col+strconv.Itoa(row.R)])
		}
		price, _ := strconv.ParseFloat(ref("D"), 64)
		stock, _ := strconv.Atoi(ref("E"))
		minQty, _ := strconv.Atoi(ref("L"))
		if minQty < 1 {
			minQty = 1
		}
		r := xlsxRow{
			Name:          strings.TrimSpace(ref("A")),
			Description:   ref("B"),
			Category:      ref("C"),
			Price:         price,
			Stock:         stock,
			SKU:           strings.TrimSpace(ref("F")),
			Variants:      ref("G"),
			Thumbnail:     ref("H"),
			Images:        ref("I"),
			Source1688URL: strings.TrimSpace(ref("K")),
			MinQty:        minQty,
		}
		if r.Name != "" {
			rows = append(rows, r)
		}
	}
	return rows, nil
}

type xlsxSharedStringItem struct {
	InnerXML string `xml:",innerxml"`
}
type xlsxSST struct {
	Items []xlsxSharedStringItem `xml:"si"`
}
type xlsxCell struct {
	R  string `xml:"r,attr"`
	T  string `xml:"t,attr"`
	V  string `xml:"v"`
	IS struct {
		T string `xml:"t"`
	} `xml:"is"`
}
type xlsxWS struct {
	SheetData struct {
		Rows []struct {
			R     int        `xml:"r,attr"`
			Cells []xlsxCell `xml:"c"`
		} `xml:"row"`
	} `xml:"sheetData"`
}

func readXLSXSharedStrings(zr *zip.Reader) ([]string, error) {
	re := regexp.MustCompile(`<[^>]+>`)
	for _, f := range zr.File {
		if f.Name != "xl/sharedStrings.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open sharedStrings.xml: %w", err)
		}
		var s xlsxSST
		xml.NewDecoder(rc).Decode(&s)
		rc.Close()
		out := make([]string, len(s.Items))
		for i, item := range s.Items {
			t := re.ReplaceAllString(item.InnerXML, "")
			t = strings.ReplaceAll(t, "&amp;", "&")
			t = strings.ReplaceAll(t, "&lt;", "<")
			t = strings.ReplaceAll(t, "&gt;", ">")
			t = strings.ReplaceAll(t, "&quot;", `"`)
			t = strings.ReplaceAll(t, "&apos;", "'")
			out[i] = strings.TrimSpace(t)
		}
		return out, nil
	}
	return []string{}, nil // some xlsx files have no sharedStrings (all inline)
}

func readXLSXWorksheet(zr *zip.Reader) (*xlsxWS, error) {
	for _, f := range zr.File {
		if f.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open sheet1.xml: %w", err)
		}
		var ws xlsxWS
		xml.NewDecoder(rc).Decode(&ws)
		rc.Close()
		return &ws, nil
	}
	return nil, fmt.Errorf("sheet1.xml not found")
}

func (h *Handler) ProductImport(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/products/import?error=No file selected")
		return
	}

	src, err := file.Open()
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/products/import?error=Cannot open file")
		return
	}
	defer src.Close()

	data, err := io.ReadAll(src)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/products/import?error=Cannot read file")
		return
	}

	rows, err := parseXLSXUpload(data)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/products/import?error="+url.QueryEscape("Invalid xlsx: "+err.Error()))
		return
	}
	if len(rows) == 0 {
		c.Redirect(http.StatusFound, "/admin/products/import?error=File is empty")
		return
	}

	imported, skipped := 0, 0
	for _, row := range rows {
		if row.Name == "" {
			skipped++
			continue
		}

		var cat models.Category
		if row.Category != "" {
			h.DB.Where("name = ?", row.Category).FirstOrCreate(&cat, models.Category{Name: row.Category, Level: 1})
		}

		variants := parseVariants(row.Variants)
		isVariant := len(variants) > 0

		totalStock := row.Stock
		if totalStock == 0 && isVariant {
			for _, v := range variants {
				totalStock += v.Stock
			}
		}

		thumb := row.Thumbnail
		photos := row.Images
		src1688 := row.Source1688URL
		product := models.Product{
			Name:           row.Name,
			Description:    &row.Description,
			UnitPrice:      row.Price,
			CategoryID:     cat.ID,
			CurrentStock:   totalStock,
			Published:      1,
			Approved:       1,
			UserID:         currentUser(c).ID,
			AddedBy:        "admin",
			Attributes:     "[]",
			Slug:           slugify(row.Name),
			VariantProduct: func() int { if isVariant { return 1 }; return 0 }(),
			ThumbnailImg:   &thumb,
			Photos:         &photos,
			MinQty:         row.MinQty,
		}
		if src1688 != "" {
			product.Source1688URL = &src1688
		}

		if err := h.DB.Create(&product).Error; err != nil {
			skipped++
			continue
		}

		if isVariant {
			var variantNames []string
			for _, v := range variants {
				sku := v.SKU
				if sku == "" {
					sku = row.SKU // fall back to product-level SKU shared across variants
				}
				h.DB.Create(&models.ProductStock{ProductID: product.ID, Variant: v.Name, Price: v.Price, Qty: v.Stock, Sku: &sku})
				variantNames = append(variantNames, v.Name)
			}
			// Save choice_options so the frontend can render variant selector buttons
			choices := []choiceOption{{AttributeName: "Variant", Values: variantNames}}
			if cb, err := json.Marshal(choices); err == nil {
				choiceStr := string(cb)
				h.DB.Model(&product).Updates(map[string]interface{}{"choice_options": choiceStr})
			}
		} else if row.SKU != "" {
			sku := row.SKU
			h.DB.Create(&models.ProductStock{ProductID: product.ID, Variant: "default", Price: row.Price, Qty: row.Stock, Sku: &sku})
		}

		imported++
	}

	msg := url.QueryEscape(fmt.Sprintf("Imported %d products, skipped %d", imported, skipped))
	c.Redirect(http.StatusFound, "/admin/products/import?success="+msg)
}

func (h *Handler) ProductExport(c *gin.Context) {
	var products []models.Product
	h.DB.Preload("Category").Preload("Stocks").Find(&products)

	scheme := "https"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	baseURL := scheme + "://" + c.Request.Host

	header := []string{"name", "description", "category", "unit_price", "current_stock", "sku", "variants", "thumbnail_img", "images", "product_url", "source_1688_url", "min_qty"}
	var dataRows [][]string

	for _, p := range products {
		desc := ""
		if p.Description != nil {
			desc = *p.Description
		}
		catName := ""
		if p.Category != nil {
			catName = p.Category.Name
		}
		thumb := ""
		if p.ThumbnailImg != nil {
			thumb = *p.ThumbnailImg
		}
		photos := ""
		if p.Photos != nil {
			photos = *p.Photos
		}

		sku, variantsStr := "", ""
		// Column F: product-level SKU (shared across all variants for variant products).
		if len(p.Stocks) > 0 && p.Stocks[0].Sku != nil {
			sku = *p.Stocks[0].Sku
		}
		if p.VariantProduct == 1 && len(p.Stocks) > 0 {
			var parts []string
			for _, s := range p.Stocks {
				stockSKU := ""
				if s.Sku != nil {
					stockSKU = *s.Sku
				}
				parts = append(parts, fmt.Sprintf("%s|%.2f|%d|%s", s.Variant, s.Price, s.Qty, stockSKU))
			}
			variantsStr = strings.Join(parts, "; ")
		}

		productURL := ""
		if p.Slug != "" {
			productURL = baseURL + "/product/" + p.Slug
		}
		src1688 := ""
		if p.Source1688URL != nil {
			src1688 = *p.Source1688URL
		}
		minQty := p.MinQty
		if minQty < 1 {
			minQty = 1
		}
		dataRows = append(dataRows, []string{
			p.Name, desc, catName,
			fmt.Sprintf("%.2f", p.UnitPrice),
			fmt.Sprintf("%d", p.CurrentStock),
			sku, variantsStr, thumb, photos, productURL, src1688,
			fmt.Sprintf("%d", minQty),
		})
	}

	buf := buildXLSX(header, dataRows)
	c.Header("Content-Disposition", "attachment; filename=products_export.xlsx")
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf)
}

func buildXLSX(header []string, rows [][]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	allRows := append([][]string{header}, rows...)

	// Build shared strings table
	var sst []string
	index := make(map[string]int)
	addStr := func(s string) int {
		if i, ok := index[s]; ok {
			return i
		}
		i := len(sst)
		sst = append(sst, s)
		index[s] = i
		return i
	}
	for _, row := range allRows {
		for _, cell := range row {
			addStr(cell)
		}
	}

	writeFile := func(name, content string) {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}

	writeFile("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`+
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`+
		`<Default Extension="xml" ContentType="application/xml"/>`+
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`+
		`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`+
		`<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>`+
		`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`+
		`</Types>`)

	writeFile("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>`+
		`</Relationships>`)

	writeFile("xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`+
		`<sheets><sheet name="Products" sheetId="1" r:id="rId1"/></sheets></workbook>`)

	writeFile("xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>`+
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>`+
		`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`+
		`</Relationships>`)

	writeFile("xl/styles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`+
		`<fonts count="1"><font/></fonts><fills count="2"><fill/><fill/></fills>`+
		`<borders count="1"><border/></borders>`+
		`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>`+
		`<cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs>`+
		`</styleSheet>`)

	// Shared strings XML
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="%d" uniqueCount="%d">`, len(sst), len(sst)))
	for _, s := range sst {
		sb.WriteString(`<si><t xml:space="preserve">`)
		sb.WriteString(xlsxXMLEscape(s))
		sb.WriteString(`</t></si>`)
	}
	sb.WriteString(`</sst>`)
	writeFile("xl/sharedStrings.xml", sb.String())

	// Sheet XML
	cols := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L"}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for ri, row := range allRows {
		rowNum := ri + 1
		sheet.WriteString(fmt.Sprintf(`<row r="%d">`, rowNum))
		for ci, cell := range row {
			if ci >= len(cols) {
				break
			}
			sheet.WriteString(fmt.Sprintf(`<c r="%s%d" t="s"><v>%d</v></c>`, cols[ci], rowNum, addStr(cell)))
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	writeFile("xl/worksheets/sheet1.xml", sheet.String())

	zw.Close()
	return buf.Bytes()
}

func xlsxXMLEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// Helper functions for import/export

func parseCSVLine(line string) []string {
	var fields []string
	var current strings.Builder
	inQuotes := false

	for i := 0; i < len(line); i++ {
		ch := line[i]

		if ch == '"' {
			if inQuotes && i+1 < len(line) && line[i+1] == '"' {
				// Escaped quote
				current.WriteByte('"')
				i++
			} else {
				// Toggle quote state
				inQuotes = !inQuotes
			}
		} else if ch == ',' && !inQuotes {
			// Field separator
			fields = append(fields, strings.TrimSpace(current.String()))
			current.Reset()
		} else {
			current.WriteByte(ch)
		}
	}

	fields = append(fields, strings.TrimSpace(current.String()))
	return fields
}

func escapeCSV(s string) string {
	return strings.ReplaceAll(s, "\"", "\"\"")
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	val, _ := strconv.ParseFloat(s, 64)
	return val
}

func parseInt(s string) int {
	s = strings.TrimSpace(s)
	val, _ := strconv.Atoi(s)
	return val
}

type VariantEntry struct {
	Name  string
	Price float64
	Stock int
	SKU   string
}

func parseVariants(variantsStr string) []VariantEntry {
	if strings.TrimSpace(variantsStr) == "" {
		return []VariantEntry{}
	}

	var variants []VariantEntry
	// Format: "Size=S,Color=Red|price|stock|sku; Size=M,Color=Blue|price|stock|sku"
	entries := strings.Split(variantsStr, ";")

	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// Split variant name from price/stock/sku
		parts := strings.Split(entry, "|")
		if len(parts) < 2 {
			continue
		}

		variantName := strings.TrimSpace(parts[0])
		variantPrice := parseFloat(parts[1])
		variantStock := 0
		if len(parts) > 2 {
			variantStock = parseInt(parts[2])
		}
		variantSKU := ""
		if len(parts) > 3 {
			variantSKU = strings.TrimSpace(parts[3])
		}

		if variantPrice <= 0 {
			variantPrice = 0 // Allow 0 price, will use default
		}

		variants = append(variants, VariantEntry{
			Name:  variantName,
			Price: variantPrice,
			Stock: variantStock,
			SKU:   variantSKU,
		})
	}

	return variants
}

// ── Auction Bids ──────────────────────────────────────────────────────────────

// AuctionBids lists all bids for a product (GET /admin/products/:id/bids).
func (h *Handler) AuctionBids(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, "invalid id")
		return
	}
	var product models.Product
	if err := h.DB.First(&product, id).Error; err != nil {
		c.String(http.StatusNotFound, "product not found")
		return
	}
	var bids []models.AuctionBid
	h.DB.Where("product_id = ?", id).Preload("User").Order("amount desc").Find(&bids)

	// Check if a winner has been recorded already.
	var winner models.AuctionWinner
	hasWinner := h.DB.Where("product_id = ?", id).Preload("User").First(&winner).Error == nil

	h.Engine.Render(c, http.StatusOK, "admin", "admin/auction_bids", gin.H{
		"Product":   product,
		"Bids":      bids,
		"Winner":    winner,
		"HasWinner": hasWinner,
	})
}

// AuctionClose manually closes an auction and records the winner
// POST /admin/products/:id/auction/close
func (h *Handler) AuctionClose(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	redirect := fmt.Sprintf("/admin/products/%d/bids", id)

	var product models.Product
	if err := h.DB.First(&product, id).Error; err != nil {
		view.FlashSet(c, "error", "Product not found.")
		c.Redirect(http.StatusFound, redirect)
		return
	}
	if product.AuctionProduct != 1 {
		view.FlashSet(c, "error", "This product is not an auction.")
		c.Redirect(http.StatusFound, redirect)
		return
	}

	// Check if already closed.
	var existing models.AuctionWinner
	if h.DB.Where("product_id = ?", id).First(&existing).Error == nil {
		view.FlashSet(c, "error", "This auction has already been closed.")
		c.Redirect(http.StatusFound, redirect)
		return
	}

	// Find highest bid.
	var topBid models.AuctionBid
	if err := h.DB.Where("product_id = ?", id).Order("amount desc").First(&topBid).Error; err != nil {
		// No bids.
		h.DB.Create(&models.AuctionWinner{ProductID: uint(id), Status: "no_bids"})
		view.FlashSet(c, "success", "Auction closed — no bids were placed.")
		c.Redirect(http.StatusFound, redirect)
		return
	}

	winner := models.AuctionWinner{
		ProductID: uint(id),
		UserID:    topBid.UserID,
		BidID:     topBid.ID,
		Amount:    topBid.Amount,
		Status:    "pending",
	}
	if err := h.DB.Create(&winner).Error; err != nil {
		view.FlashSet(c, "error", "Failed to record winner: "+err.Error())
		c.Redirect(http.StatusFound, redirect)
		return
	}
	view.FlashSet(c, "success", fmt.Sprintf("Auction closed! Winner recorded with bid %s.", formatCurrency(topBid.Amount)))
	c.Redirect(http.StatusFound, redirect)
}

// formatCurrency is a simple helper to format amounts in the admin layer.
func formatCurrency(amount float64) string {
	return fmt.Sprintf("%.2f", amount)
}

// ── Support Tickets ──────────────────────────────────────────────────────────

// AdminTicketList lists all support tickets with optional status filter.
func (h *Handler) AdminTicketList(c *gin.Context) {
	u := currentUser(c)
	status := c.Query("status")

	var tickets []models.Ticket
	q := h.DB.Preload("User").Order("created_at desc")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	q.Find(&tickets)

	var counts struct {
		All      int64
		Pending  int64
		Open     int64
		Answered int64
		Closed   int64
	}
	h.DB.Model(&models.Ticket{}).Count(&counts.All)
	h.DB.Model(&models.Ticket{}).Where("status = ?", "pending").Count(&counts.Pending)
	h.DB.Model(&models.Ticket{}).Where("status = ?", "open").Count(&counts.Open)
	h.DB.Model(&models.Ticket{}).Where("status = ?", "answered").Count(&counts.Answered)
	h.DB.Model(&models.Ticket{}).Where("status = ?", "closed").Count(&counts.Closed)

	// Flash message
	flash, _ := c.Cookie("flash")
	flashType, _ := c.Cookie("flash_type")
	if flash != "" {
		c.SetCookie("flash", "", -1, "/", "", false, true)
		c.SetCookie("flash_type", "", -1, "/", "", false, true)
	}

	h.Engine.Render(c, http.StatusOK, "admin", "admin/tickets", gin.H{
		"User":      u,
		"Tickets":   tickets,
		"Status":    status,
		"Counts":    counts,
		"Flash":     flash,
		"FlashType": flashType,
	})
}

// AdminTicketDetail shows a single ticket thread.
func (h *Handler) AdminTicketDetail(c *gin.Context) {
	u := currentUser(c)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}

	var ticket models.Ticket
	if err := h.DB.Preload("User").Preload("Replies.User").First(&ticket, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}

	// Mark as viewed by admin
	h.DB.Model(&ticket).Update("viewed", 1)

	h.Engine.Render(c, http.StatusOK, "admin", "admin/ticket_detail", gin.H{
		"User":   u,
		"Ticket": ticket,
	})
}

// AdminTicketReply posts an admin reply on a ticket.
func (h *Handler) AdminTicketReply(c *gin.Context) {
	u := currentUser(c)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}

	var ticket models.Ticket
	if err := h.DB.First(&ticket, id).Error; err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}

	reply := strings.TrimSpace(c.PostForm("reply"))
	if reply == "" {
		c.Redirect(http.StatusFound, fmt.Sprintf("/admin/tickets/%d", id))
		return
	}

	h.DB.Create(&models.TicketReply{
		TicketID: uint(id),
		UserID:   u.ID,
		Reply:    reply,
	})
	h.DB.Model(&ticket).Updates(map[string]interface{}{
		"status":       "answered",
		"client_viewed": 0,
	})

	c.Redirect(http.StatusFound, fmt.Sprintf("/admin/tickets/%d", id))
}

// AdminTicketClose closes a ticket.
func (h *Handler) AdminTicketClose(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}
	h.DB.Model(&models.Ticket{}).Where("id = ?", id).Update("status", "closed")
	c.Redirect(http.StatusFound, fmt.Sprintf("/admin/tickets/%d", id))
}

// AdminTicketOpen re-opens a ticket.
func (h *Handler) AdminTicketOpen(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}
	h.DB.Model(&models.Ticket{}).Where("id = ?", id).Update("status", "open")
	c.Redirect(http.StatusFound, fmt.Sprintf("/admin/tickets/%d", id))
}

// AdminTicketDelete hard-deletes a ticket and all its replies.
func (h *Handler) AdminTicketDelete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}
	h.DB.Where("ticket_id = ?", id).Delete(&models.TicketReply{})
	h.DB.Delete(&models.Ticket{}, id)
	c.SetCookie("flash", "Ticket deleted successfully.", 5, "/", "", false, true)
	c.SetCookie("flash_type", "success", 5, "/", "", false, true)
	c.Redirect(http.StatusFound, "/admin/tickets")
}

// ── 1688 Supplier Order Fulfillment ──────────────────────────────────────────

// SupplierOrderSave creates or updates a SupplierOrder record for one order-
// detail line.  Called via AJAX from the order detail page.
// POST /admin/supplier-orders
func (h *Handler) SupplierOrderSave(c *gin.Context) {
	orderID, _ := strconv.ParseUint(c.PostForm("order_id"), 10, 64)
	orderDetailID, _ := strconv.ParseUint(c.PostForm("order_detail_id"), 10, 64)
	productID, _ := strconv.ParseUint(c.PostForm("product_id"), 10, 64)
	if orderID == 0 || orderDetailID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order_id and order_detail_id are required"})
		return
	}
	// Verify the order_detail_id belongs to order_id (security: prevent cross-order tampering).
	var od models.OrderDetail
	if err := h.DB.Where("id = ? AND order_id = ?", orderDetailID, orderID).First(&od).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order_detail not found for this order"})
		return
	}
	// Resolve product_id from the order detail when not explicitly supplied.
	if productID == 0 {
		productID = uint64(od.ProductID)
	}
	if productID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot determine product_id for this order detail"})
		return
	}

	var sf models.SupplierOrder
	// Try to find existing record; FirstOrCreate would race — use manual upsert.
	if err := h.DB.Where("order_detail_id = ?", orderDetailID).First(&sf).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	sf.OrderID = uint(orderID)
	sf.OrderDetailID = uint(orderDetailID)
	sf.ProductID = uint(productID)
	sf.AliOfferID = strings.TrimSpace(c.PostForm("ali_offer_id"))
	sf.AliOrderID = strings.TrimSpace(c.PostForm("ali_order_id"))
	qty, _ := strconv.Atoi(c.PostForm("qty"))
	if qty < 1 {
		qty = 1
	}
	sf.Qty = qty
	sf.UnitCostCNY, _ = strconv.ParseFloat(c.PostForm("unit_cost_cny"), 64)
	sf.SourceURL = strings.TrimSpace(c.PostForm("source_url"))
	status := strings.TrimSpace(c.PostForm("status"))
	validSFStatuses := map[string]bool{
		"pending": true, "ordered": true, "shipped": true,
		"delivered": true, "cancelled": true, "exception": true,
	}
	if !validSFStatuses[status] {
		if sf.ID == 0 {
			status = "pending"
		} else {
			status = sf.Status // keep existing value
		}
	}
	sf.Status = status
	sf.TrackingCompany = strings.TrimSpace(c.PostForm("tracking_company"))
	sf.TrackingNumber = strings.TrimSpace(c.PostForm("tracking_number"))
	// Allow notes to be cleared by sending an empty string.
	notes := strings.TrimSpace(c.PostForm("notes"))
	if notes != "" {
		sf.Notes = &notes
	} else {
		sf.Notes = nil
	}

	var dbErr error
	if sf.ID == 0 {
		dbErr = h.DB.Create(&sf).Error
	} else {
		// Use selective Updates (map form) so LogisticsJSON and SyncedAt
		// (written only by SupplierOrderSyncTracking) are never clobbered.
		notesVal := interface{}(nil)
		if sf.Notes != nil {
			notesVal = *sf.Notes
		}
		dbErr = h.DB.Model(&sf).Updates(map[string]interface{}{
			"order_id":         sf.OrderID,
			"order_detail_id":  sf.OrderDetailID,
			"product_id":       sf.ProductID,
			"ali_offer_id":     sf.AliOfferID,
			"ali_order_id":     sf.AliOrderID,
			"qty":              sf.Qty,
			"unit_cost_cny":    sf.UnitCostCNY,
			"source_url":       sf.SourceURL,
			"status":           sf.Status,
			"tracking_company": sf.TrackingCompany,
			"tracking_number":  sf.TrackingNumber,
			"notes":            notesVal,
		}).Error
	}
	if dbErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": dbErr.Error()})
		return
	}
	c.JSON(http.StatusOK, sf)
}

// SupplierOrderSyncTracking calls kuaidi100 to refresh logistics data for a
// supplier order and updates the DB record.
// POST /admin/supplier-orders/:sid/sync
func (h *Handler) SupplierOrderSyncTracking(c *gin.Context) {
	sid, _ := strconv.Atoi(c.Param("sid"))
	var sf models.SupplierOrder
	if err := h.DB.First(&sf, sid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "supplier order not found"})
		return
	}
	if sf.TrackingCompany == "" || sf.TrackingNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tracking company and number are required"})
		return
	}

	key := h.Settings.Get("kuaidi100_key", "jfQlTvuH8914")
	customer := h.Settings.Get("kuaidi100_customer", key)

	result, err := kuaidi100.Query(key, customer, sf.TrackingCompany, sf.TrackingNumber)
	if err != nil {
		// Use 422 (not 502): a failed lookup — e.g. a wrong tracking number — is a
		// business error, not a gateway failure. A 5xx gets intercepted by the
		// OpenResty/Cloudflare proxy, which swaps our JSON body for an HTML error
		// page and breaks the frontend's JSON parsing. A 4xx passes through intact.
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	raw, _ := json.Marshal(result)
	rawStr := string(raw)
	sf.LogisticsJSON = &rawStr
	now := time.Now()
	sf.SyncedAt = &now

	// Auto-advance SupplierOrder.Status based on kuaidi100 state.
	if newStatus := kuaidi100.StatusFromState(result.State); newStatus != "" {
		sf.Status = newStatus
	}

	h.DB.Save(&sf)

	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"state":       result.State,
		"state_label": kuaidi100.StateLabel(result.State),
		"events":      result.Data,
		"status":      sf.Status,
		"synced_at":   sf.SyncedAt,
	})
}

// SupplierOrderDelete removes a supplier order record.
// POST /admin/supplier-orders/:sid/delete
func (h *Handler) SupplierOrderDelete(c *gin.Context) {
	sid, _ := strconv.Atoi(c.Param("sid"))
	// Verify the record exists before deleting (prevents blind deletes of arbitrary IDs).
	var sf models.SupplierOrder
	if err := h.DB.First(&sf, sid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "supplier order not found"})
		return
	}
	if err := h.DB.Delete(&sf).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
