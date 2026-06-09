package seller

import (
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/services/translate"
	"mall/internal/view"
)

type Handler struct {
	DB     *gorm.DB
	Engine *view.Engine
}

func currentUser(c *gin.Context) *models.User {
	return c.MustGet("user").(*models.User)
}

// ── Dashboard ────────────────────────────────────────────────────────────────

func (h *Handler) Dashboard(c *gin.Context) {
	u := currentUser(c)

	var totalSales float64
	h.DB.Model(&models.Order{}).
		Select("COALESCE(SUM(grand_total),0)").
		Where("seller_id = ? AND payment_status = ?", u.ID, "paid").
		Scan(&totalSales)

	var orderCount int64
	h.DB.Model(&models.Order{}).Where("seller_id = ?", u.ID).Count(&orderCount)

	var pendingCount int64
	h.DB.Model(&models.Order{}).
		Where("seller_id = ? AND delivery_status = ?", u.ID, "pending").
		Count(&pendingCount)

	var productCount int64
	h.DB.Model(&models.Product{}).Where("user_id = ?", u.ID).Count(&productCount)

	var recentOrders []models.Order
	h.DB.Where("seller_id = ?", u.ID).
		Order("created_at desc").Limit(10).
		Preload("OrderDetails.Product").
		Preload("Buyer").
		Find(&recentOrders)

	h.Engine.Render(c, http.StatusOK, "seller", "seller/dashboard", gin.H{
		"User":         u,
		"TotalSales":   totalSales,
		"OrderCount":   orderCount,
		"PendingCount": pendingCount,
		"ProductCount": productCount,
		"RecentOrders": recentOrders,
	})
}

// ── Products ─────────────────────────────────────────────────────────────────

func (h *Handler) ProductList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit := 15
	offset := (page - 1) * limit

	var products []models.Product
	var total int64
	q := h.DB.Model(&models.Product{}).Where("user_id = ?", u.ID)
	q.Count(&total)
	q.Order("created_at desc").Limit(limit).Offset(offset).
		Preload("Category").Find(&products)

	h.Engine.Render(c, http.StatusOK, "seller", "seller/products", gin.H{
		"User":     u,
		"Products": products,
		"Total":    total,
		"Page":     page,
		"Pages":    (int(total) + limit - 1) / limit,
	})
}

func (h *Handler) ProductCreate(c *gin.Context) {
	u := currentUser(c)
	var cats []models.Category
	h.DB.Where("level = 0").Order("name asc").Find(&cats)
	var brands []models.Brand
	h.DB.Order("name asc").Find(&brands)

	h.Engine.Render(c, http.StatusOK, "seller", "seller/product_form", gin.H{
		"User":       u,
		"Categories": cats,
		"Brands":     brands,
		"Product":    nil,
		"Langs":      translate.Langs,
		"TransMap":   map[string]models.ProductTranslation{},
	})
}

func (h *Handler) ProductStore(c *gin.Context) {
	u := currentUser(c)
	sourceLang := c.PostForm("source_lang")
	if sourceLang == "" {
		sourceLang = "en"
	}
	name := c.PostForm("name_" + sourceLang)
	if name == "" {
		c.Redirect(http.StatusFound, "/seller/products/create")
		return
	}

	catID, _ := strconv.ParseUint(c.PostForm("category_id"), 10, 64)
	if catID == 0 {
		c.Redirect(http.StatusFound, "/seller/products/create?error=Category+is+required")
		return
	}
	brandID, _ := strconv.ParseUint(c.PostForm("brand_id"), 10, 64)
	price, _ := strconv.ParseFloat(c.PostForm("unit_price"), 64)
	if price <= 0 {
		c.Redirect(http.StatusFound, "/seller/products/create?error=Price+must+be+greater+than+zero")
		return
	}
	quantity, _ := strconv.Atoi(c.PostForm("current_stock"))
	if quantity < 0 {
		quantity = 0
	}

	slug := slugify(name)
	cat := uint(catID)
	var brandPtr *uint
	if brandID > 0 {
		b := uint(brandID)
		brandPtr = &b
	}

	product := models.Product{
		Name:         name,
		AddedBy:      "seller",
		UserID:       u.ID,
		CategoryID:   cat,
		BrandID:      brandPtr,
		UnitPrice:    price,
		CurrentStock: quantity,
		Slug:         slug,
		Description:  ptrStr(c.PostForm("description_" + sourceLang)),
	}
	h.DB.Create(&product)

	for _, lang := range translate.Langs {
		n := c.PostForm("name_" + lang.Code)
		if n == "" {
			continue
		}
		tr := models.ProductTranslation{
			ProductID:   product.ID,
			Lang:        lang.Code,
			Name:        ptrStr(n),
			Description: ptrStr(c.PostForm("description_" + lang.Code)),
		}
		h.DB.Create(&tr)
	}
	c.Redirect(http.StatusFound, "/seller/products")
}

func (h *Handler) ProductEdit(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var product models.Product
	if err := h.DB.Preload("Translations").Where("id = ? AND user_id = ?", id, u.ID).First(&product).Error; err != nil {
		c.Redirect(http.StatusFound, "/seller/products")
		return
	}
	transMap := make(map[string]models.ProductTranslation, len(product.Translations))
	for _, tr := range product.Translations {
		transMap[tr.Lang] = tr
	}
	var cats []models.Category
	h.DB.Where("level = 0").Order("name asc").Find(&cats)
	var brands []models.Brand
	h.DB.Order("name asc").Find(&brands)

	h.Engine.Render(c, http.StatusOK, "seller", "seller/product_form", gin.H{
		"User":       u,
		"Categories": cats,
		"Brands":     brands,
		"Product":    &product,
		"Langs":      translate.Langs,
		"TransMap":   transMap,
	})
}

func (h *Handler) ProductUpdate(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var product models.Product
	if err := h.DB.Where("id = ? AND user_id = ?", id, u.ID).First(&product).Error; err != nil {
		c.Redirect(http.StatusFound, "/seller/products")
		return
	}

	sourceLang := c.PostForm("source_lang")
	if sourceLang == "" {
		sourceLang = "en"
	}
	price, _ := strconv.ParseFloat(c.PostForm("unit_price"), 64)
	quantity, _ := strconv.Atoi(c.PostForm("current_stock"))
	catID, _ := strconv.ParseUint(c.PostForm("category_id"), 10, 64)
	brandID, _ := strconv.ParseUint(c.PostForm("brand_id"), 10, 64)
	cat := uint(catID)

	updates := map[string]interface{}{
		"name":          c.PostForm("name_" + sourceLang),
		"unit_price":    price,
		"current_stock": quantity,
		"category_id":   cat,
		"description":   c.PostForm("description_" + sourceLang),
	}
	if brandID > 0 {
		b := uint(brandID)
		updates["brand_id"] = &b
	} else {
		updates["brand_id"] = nil
	}
	h.DB.Model(&product).Updates(updates)

	// Replace translations
	h.DB.Where("product_id = ?", product.ID).Delete(&models.ProductTranslation{})
	for _, lang := range translate.Langs {
		n := c.PostForm("name_" + lang.Code)
		if n == "" {
			continue
		}
		tr := models.ProductTranslation{
			ProductID:   product.ID,
			Lang:        lang.Code,
			Name:        ptrStr(n),
			Description: ptrStr(c.PostForm("description_" + lang.Code)),
		}
		h.DB.Create(&tr)
	}
	c.Redirect(http.StatusFound, "/seller/products")
}

func (h *Handler) ProductDelete(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Where("id = ? AND user_id = ?", id, u.ID).Delete(&models.Product{})
	c.Redirect(http.StatusFound, "/seller/products")
}

// ── Orders ───────────────────────────────────────────────────────────────────

func (h *Handler) OrderList(c *gin.Context) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	status := c.DefaultQuery("status", "")
	limit := 15
	offset := (page - 1) * limit

	var orders []models.Order
	var total int64
	q := h.DB.Model(&models.Order{}).Where("seller_id = ?", u.ID)
	if status != "" {
		q = q.Where("delivery_status = ?", status)
	}
	q.Count(&total)
	q.Order("created_at desc").Limit(limit).Offset(offset).
		Preload("OrderDetails.Product").
		Preload("Buyer").
		Find(&orders)

	h.Engine.Render(c, http.StatusOK, "seller", "seller/orders", gin.H{
		"User":   u,
		"Orders": orders,
		"Total":  total,
		"Page":   page,
		"Pages":  (int(total) + limit - 1) / limit,
		"Status": status,
	})
}

func (h *Handler) OrderDetail(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var order models.Order
	if err := h.DB.Where("id = ? AND seller_id = ?", id, u.ID).
		Preload("OrderDetails.Product").
		Preload("Buyer").
		First(&order).Error; err != nil {
		c.Redirect(http.StatusFound, "/seller/orders")
		return
	}
	h.Engine.Render(c, http.StatusOK, "seller", "seller/order_detail", gin.H{
		"User":  u,
		"Order": order,
	})
}

var allowedDeliveryStatuses = map[string]bool{
	"pending": true, "processing": true, "on the way": true, "delivered": true, "cancelled": true,
}

func (h *Handler) OrderUpdateStatus(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	status := c.PostForm("delivery_status")
	redirect := "/seller/orders/" + strconv.Itoa(id)
	if !allowedDeliveryStatuses[status] {
		view.FlashSet(c, "error", "Invalid delivery status.")
		c.Redirect(http.StatusFound, redirect)
		return
	}
	if err := h.DB.Model(&models.Order{}).
		Where("id = ? AND seller_id = ?", id, u.ID).
		Update("delivery_status", status).Error; err != nil {
		view.FlashSet(c, "error", "Failed to update order status.")
		c.Redirect(http.StatusFound, redirect)
		return
	}
	// Keep order_details in sync with the parent order status.
	h.DB.Model(&models.OrderDetail{}).
		Where("order_id = ? AND seller_id = ?", id, u.ID).
		Update("delivery_status", status)
	view.FlashSet(c, "success", "Order status updated.")
	c.Redirect(http.StatusFound, redirect)
}

// ── Coupons ──────────────────────────────────────────────────────────────────

func (h *Handler) CouponList(c *gin.Context) {
	u := currentUser(c)
	var coupons []models.Coupon
	h.DB.Where("user_id = ?", u.ID).Order("created_at desc").Find(&coupons)
	h.Engine.Render(c, http.StatusOK, "seller", "seller/coupons", gin.H{
		"User":    u,
		"Coupons": coupons,
	})
}

func (h *Handler) CouponCreate(c *gin.Context) {
	u := currentUser(c)
	h.Engine.Render(c, http.StatusOK, "seller", "seller/coupon_form", gin.H{
		"User":   u,
		"Coupon": nil,
	})
}

func (h *Handler) CouponStore(c *gin.Context) {
	u := currentUser(c)
	code := strings.TrimSpace(c.PostForm("code"))
	if code == "" {
		c.Redirect(http.StatusFound, "/seller/coupons?error=Coupon+code+is+required")
		return
	}
	discount, _ := strconv.ParseFloat(c.PostForm("discount"), 64)
	if discount <= 0 {
		c.Redirect(http.StatusFound, "/seller/coupons?error=Discount+must+be+greater+than+zero")
		return
	}
	discountType := c.PostForm("discount_type")
	validDiscountTypes := map[string]bool{"percent": true, "amount": true}
	if !validDiscountTypes[discountType] {
		c.Redirect(http.StatusFound, "/seller/coupons?error=Invalid+discount+type")
		return
	}
	if discountType == "percent" && discount > 100 {
		c.Redirect(http.StatusFound, "/seller/coupons?error=Percent+discount+cannot+exceed+100")
		return
	}
	coupon := models.Coupon{
		UserID:       u.ID,
		Code:         code,
		Type:         "seller_base",
		Details:      c.PostForm("details"),
		Discount:     discount,
		DiscountType: discountType,
		Status:       1,
	}
	h.DB.Create(&coupon)
	c.Redirect(http.StatusFound, "/seller/coupons")
}

func (h *Handler) CouponDelete(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	h.DB.Where("id = ? AND user_id = ?", id, u.ID).Delete(&models.Coupon{})
	c.Redirect(http.StatusFound, "/seller/coupons")
}

// ── Reviews ──────────────────────────────────────────────────────────────────

func (h *Handler) ReviewList(c *gin.Context) {
	u := currentUser(c)
	var reviews []models.Review
	h.DB.Joins("JOIN products ON products.id = reviews.product_id").
		Where("products.user_id = ?", u.ID).
		Order("reviews.created_at desc").
		Preload("Product").
		Preload("User").
		Find(&reviews)
	h.Engine.Render(c, http.StatusOK, "seller", "seller/reviews", gin.H{
		"User":    u,
		"Reviews": reviews,
	})
}

// ── Withdraw Requests ─────────────────────────────────────────────────────────

func (h *Handler) WithdrawList(c *gin.Context) {
	u := currentUser(c)
	var requests []models.SellerWithdrawRequest
	h.DB.Where("user_id = ?", u.ID).Order("created_at desc").Find(&requests)

	var shop models.Shop
	h.DB.Where("user_id = ?", u.ID).First(&shop)

	h.Engine.Render(c, http.StatusOK, "seller", "seller/withdraw", gin.H{
		"User":     u,
		"Requests": requests,
		"Shop":     shop,
	})
}

func (h *Handler) WithdrawStore(c *gin.Context) {
	u := currentUser(c)
	amount, _ := strconv.ParseFloat(c.PostForm("amount"), 64)
	if amount <= 0 {
		c.Redirect(http.StatusFound, "/seller/withdraw-requests?error=Amount+must+be+greater+than+zero")
		return
	}
	// Verify the seller has sufficient balance to cover the withdrawal.
	var shop models.Shop
	if err := h.DB.Where("user_id = ?", u.ID).First(&shop).Error; err != nil {
		c.Redirect(http.StatusFound, "/seller/withdraw-requests?error=Seller+profile+not+found")
		return
	}
	if amount > shop.AdminToPay {
		c.Redirect(http.StatusFound, "/seller/withdraw-requests?error=Insufficient+balance")
		return
	}
	msg := c.PostForm("message")
	status := 0
	req := models.SellerWithdrawRequest{
		UserID:  &u.ID,
		Amount:  &amount,
		Message: &msg,
		Status:  &status,
	}
	h.DB.Create(&req)
	c.Redirect(http.StatusFound, "/seller/withdraw-requests")
}

// ── Shop Settings ─────────────────────────────────────────────────────────────

func (h *Handler) ShopSettings(c *gin.Context) {
	u := currentUser(c)
	var shop models.Shop
	h.DB.Where("user_id = ?", u.ID).First(&shop)
	h.Engine.Render(c, http.StatusOK, "seller", "seller/shop_settings", gin.H{
		"User": u,
		"Shop": shop,
	})
}

func (h *Handler) ShopSettingsUpdate(c *gin.Context) {
	u := currentUser(c)
	var shop models.Shop
	if err := h.DB.Where("user_id = ?", u.ID).First(&shop).Error; err != nil {
		shop.UserID = u.ID
	}

	name := c.PostForm("name")
	phone := c.PostForm("phone")
	address := c.PostForm("address")
	facebook := c.PostForm("facebook")
	instagram := c.PostForm("instagram")
	twitter := c.PostForm("twitter")
	youtube := c.PostForm("youtube")

	updates := map[string]interface{}{
		"name":      name,
		"phone":     phone,
		"address":   address,
		"facebook":  facebook,
		"instagram": instagram,
		"twitter":   twitter,
		"youtube":   youtube,
	}
	if shop.ID == 0 {
		shop.Name = &name
		shop.Phone = &phone
		shop.Address = &address
		shop.Facebook = &facebook
		shop.Instagram = &instagram
		shop.Twitter = &twitter
		shop.Youtube = &youtube
		h.DB.Create(&shop)
	} else {
		h.DB.Model(&shop).Updates(updates)
	}
	c.Redirect(http.StatusFound, "/seller/shop/settings?success=1")
}

// ── Profile ──────────────────────────────────────────────────────────────────

func (h *Handler) Profile(c *gin.Context) {
	u := currentUser(c)
	success := c.Query("success") == "1"
	h.Engine.Render(c, http.StatusOK, "seller", "seller/profile", gin.H{
		"User":    u,
		"Success": success,
	})
}

func (h *Handler) ProfileUpdate(c *gin.Context) {
	u := currentUser(c)
	name := c.PostForm("name")
	phone := c.PostForm("phone")

	updates := map[string]interface{}{}
	if name != "" {
		updates["name"] = name
	}
	if phone != "" {
		updates["phone"] = phone
	}

	curPwd := c.PostForm("current_password")
	newPwd := c.PostForm("new_password")
	if curPwd != "" && newPwd != "" {
		if u.Password == nil || bcrypt.CompareHashAndPassword([]byte(*u.Password), []byte(curPwd)) != nil {
			h.Engine.Render(c, http.StatusOK, "seller", "seller/profile", gin.H{
				"User":  u,
				"Error": "wrong_password",
			})
			return
		}
		if len(newPwd) < 8 {
			h.Engine.Render(c, http.StatusOK, "seller", "seller/profile", gin.H{
				"User":  u,
				"Error": "password_too_short",
			})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
		if err != nil {
			h.Engine.Render(c, http.StatusOK, "seller", "seller/profile", gin.H{
				"User":  u,
				"Error": "server_error",
			})
			return
		}
		updates["password"] = string(hash)
	}

	if len(updates) > 0 {
		h.DB.Model(u).Updates(updates)
	}
	c.Redirect(http.StatusFound, "/seller/profile?success=1")
}

// ── Conversations ─────────────────────────────────────────────────────────────

type convItem struct {
	Conv   models.Conversation
	Other  models.User
	Unread bool
}

func (h *Handler) ConversationList(c *gin.Context) {
	u := currentUser(c)
	var convs []models.Conversation
	h.DB.Where("sender_id = ? OR receiver_id = ?", u.ID, u.ID).
		Order("updated_at desc").Find(&convs)

	// Collect the "other" participant IDs
	otherIDs := make([]uint, 0, len(convs))
	for _, cv := range convs {
		if cv.SenderID == u.ID {
			otherIDs = append(otherIDs, cv.ReceiverID)
		} else {
			otherIDs = append(otherIDs, cv.SenderID)
		}
	}
	var users []models.User
	if len(otherIDs) > 0 {
		h.DB.Where("id IN ?", otherIDs).Find(&users)
	}
	userMap := make(map[uint]models.User, len(users))
	for _, ou := range users {
		userMap[ou.ID] = ou
	}

	items := make([]convItem, len(convs))
	for i, cv := range convs {
		otherID := cv.ReceiverID
		if cv.ReceiverID == u.ID {
			otherID = cv.SenderID
		}
		unread := (cv.ReceiverID == u.ID && cv.ReceiverViewed == 0) ||
			(cv.SenderID == u.ID && cv.SenderViewed == 0)
		items[i] = convItem{Conv: cv, Other: userMap[otherID], Unread: unread}
	}

	h.Engine.Render(c, http.StatusOK, "seller", "seller/conversations", gin.H{
		"User":  u,
		"Items": items,
	})
}

func (h *Handler) ConversationShow(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))

	var conv models.Conversation
	if err := h.DB.Where("id = ? AND (sender_id = ? OR receiver_id = ?)", id, u.ID, u.ID).
		First(&conv).Error; err != nil {
		c.Redirect(http.StatusFound, "/seller/conversations")
		return
	}

	// Mark as viewed
	if conv.ReceiverID == u.ID {
		h.DB.Model(&conv).Update("receiver_viewed", 1)
	} else {
		h.DB.Model(&conv).Update("sender_viewed", 1)
	}

	var messages []models.Message
	h.DB.Where("conversation_id = ?", id).Order("created_at asc").Find(&messages)

	otherID := conv.ReceiverID
	if conv.ReceiverID == u.ID {
		otherID = conv.SenderID
	}
	var other models.User
	h.DB.First(&other, otherID)

	h.Engine.Render(c, http.StatusOK, "seller", "seller/conversation_show", gin.H{
		"User":         u,
		"Conversation": conv,
		"Messages":     messages,
		"Other":        other,
	})
}

func (h *Handler) ConversationSend(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	text := c.PostForm("message")
	if text == "" {
		c.Redirect(http.StatusFound, "/seller/conversations/"+strconv.Itoa(id))
		return
	}

	var conv models.Conversation
	if err := h.DB.Where("id = ? AND (sender_id = ? OR receiver_id = ?)", id, u.ID, u.ID).
		First(&conv).Error; err != nil {
		c.Redirect(http.StatusFound, "/seller/conversations")
		return
	}

	h.DB.Create(&models.Message{ConversationID: uint(id), UserID: u.ID, Message: &text})

	// Reset viewed flag for the other party
	if conv.SenderID == u.ID {
		h.DB.Model(&conv).Update("receiver_viewed", 0)
	} else {
		h.DB.Model(&conv).Update("sender_viewed", 0)
	}

	c.Redirect(http.StatusFound, "/seller/conversations/"+strconv.Itoa(id))
}

// ── helpers ──────────────────────────────────────────────────────────────────

func ptrStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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

// ── Support Tickets ──────────────────────────────────────────────────────────

// SellerTicketList lists the seller's support tickets.
func (h *Handler) SellerTicketList(c *gin.Context) {
	u := currentUser(c)

	var tickets []models.Ticket
	h.DB.Where("user_id = ?", u.ID).
		Order("updated_at desc").
		Preload("Replies").
		Find(&tickets)

	stats := map[string]int{"all": len(tickets), "pending": 0, "open": 0, "answered": 0, "closed": 0}
	for _, t := range tickets {
		if _, ok := stats[t.Status]; ok {
			stats[t.Status]++
		}
	}

	h.Engine.Render(c, http.StatusOK, "seller", "seller/tickets", gin.H{
		"User":    u,
		"Tickets": tickets,
		"Stats":   stats,
	})
}

// SellerTicketCreate shows the create-ticket form.
func (h *Handler) SellerTicketCreate(c *gin.Context) {
	u := currentUser(c)
	h.Engine.Render(c, http.StatusOK, "seller", "seller/ticket_create", gin.H{
		"User": u,
	})
}

// SellerTicketStore creates a new support ticket for the seller.
func (h *Handler) SellerTicketStore(c *gin.Context) {
	u := currentUser(c)

	subject := strings.TrimSpace(c.PostForm("subject"))
	details := strings.TrimSpace(c.PostForm("details"))
	priority := c.PostForm("priority")
	ticketType := c.PostForm("type")

	if subject == "" || details == "" {
		h.Engine.Render(c, http.StatusOK, "seller", "seller/ticket_create", gin.H{
			"User":  u,
			"Error": "Subject and details are required.",
		})
		return
	}

	if priority == "" {
		priority = "medium"
	}
	if ticketType == "" {
		ticketType = "general"
	}

	code := int64(100000 + rand.Intn(900000))
	ticket := models.Ticket{
		Code:     code,
		UserID:   u.ID,
		Subject:  subject,
		Details:  &details,
		Priority: priority,
		Type:     ticketType,
		Status:   "pending",
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		h.Engine.Render(c, http.StatusInternalServerError, "seller", "seller/ticket_create", gin.H{
			"User":  u,
			"Error": "Failed to submit ticket. Please try again.",
		})
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/seller/tickets/%d", ticket.ID))
}

// SellerTicketDetail shows a single ticket thread for the seller.
func (h *Handler) SellerTicketDetail(c *gin.Context) {
	u := currentUser(c)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/seller/tickets")
		return
	}

	var ticket models.Ticket
	if err := h.DB.Where("id = ? AND user_id = ?", id, u.ID).
		Preload("Replies.User").Preload("User").
		First(&ticket).Error; err != nil {
		c.Redirect(http.StatusFound, "/seller/tickets")
		return
	}

	h.DB.Model(&ticket).Update("client_viewed", 1)

	h.Engine.Render(c, http.StatusOK, "seller", "seller/ticket_detail", gin.H{
		"User":   u,
		"Ticket": ticket,
	})
}

// SellerTicketReply adds a seller reply to an existing ticket.
func (h *Handler) SellerTicketReply(c *gin.Context) {
	u := currentUser(c)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/seller/tickets")
		return
	}

	var ticket models.Ticket
	if err := h.DB.Where("id = ? AND user_id = ?", id, u.ID).First(&ticket).Error; err != nil {
		c.Redirect(http.StatusFound, "/seller/tickets")
		return
	}

	reply := strings.TrimSpace(c.PostForm("reply"))
	if reply == "" {
		c.Redirect(http.StatusFound, fmt.Sprintf("/seller/tickets/%d", id))
		return
	}

	h.DB.Create(&models.TicketReply{
		TicketID: uint(id),
		UserID:   u.ID,
		Reply:    reply,
	})

	newStatus := ticket.Status
	if ticket.Status == "closed" || ticket.Status == "answered" {
		newStatus = "open"
	}
	h.DB.Model(&ticket).Updates(map[string]interface{}{
		"status":        newStatus,
		"viewed":        0,
		"client_viewed": 1,
	})

	c.Redirect(http.StatusFound, fmt.Sprintf("/seller/tickets/%d", id))
}
