package frontend

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"mall/internal/models"
	"mall/internal/services/settings"
	"mall/internal/view"
)

type AccountHandler struct {
	DB        *gorm.DB
	Engine    *view.Engine
	Settings  *settings.Store
	UploadDir string
}

func (h *AccountHandler) navCats(c *gin.Context) []models.Category {
	return navTranslatedCats(h.DB, c)
}

// GET /dashboard
func (h *AccountHandler) Dashboard(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var orderCount int64
	h.DB.Model(&models.Order{}).Where("user_id = ?", user.ID).Count(&orderCount)

	var pendingCount int64
	h.DB.Model(&models.Order{}).Where("user_id = ? AND delivery_status = ?", user.ID, "pending").Count(&pendingCount)

	var wishCount int64
	h.DB.Model(&models.Wishlist{}).Where("user_id = ?", user.ID).Count(&wishCount)

	var recentOrders []models.CombinedOrder
	h.DB.Where("user_id = ?", user.ID).Order("created_at desc").Limit(5).
		Preload("Orders.OrderDetails.Product").Find(&recentOrders)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/dashboard", gin.H{
		"Categories":   h.navCats(c),
		"User":         user,
		"OrderCount":   orderCount,
		"PendingCount": pendingCount,
		"WishCount":    wishCount,
		"RecentOrders": recentOrders,
	})
}

// GET /purchase_history
func (h *AccountHandler) PurchaseHistory(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}

	var total int64
	h.DB.Model(&models.CombinedOrder{}).Where("user_id = ?", user.ID).Count(&total)
	totalPages := int((total + int64(perPage) - 1) / int64(perPage))
	offset := (page - 1) * perPage

	var orders []models.CombinedOrder
	h.DB.Where("user_id = ?", user.ID).Order("created_at desc").
		Offset(offset).Limit(perPage).
		Preload("Orders.OrderDetails.Product").Find(&orders)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/purchase_history", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Orders":     orders,
		"Page":       page,
		"TotalPages": totalPages,
		"Total":      total,
	})
}

// GET /purchase_history/:id
func (h *AccountHandler) PurchaseHistoryDetail(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.Redirect(http.StatusFound, "/purchase_history")
		return
	}
	var order models.CombinedOrder
	if err := h.DB.Where("id = ? AND user_id = ?", id, user.ID).
		Preload("Orders.OrderDetails.Product").
		First(&order).Error; err != nil {
		c.Redirect(http.StatusFound, "/purchase_history")
		return
	}
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/purchase_history_detail", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Order":      order,
	})
}

// POST /purchase_history/:id/cancel
func (h *AccountHandler) CancelOrder(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.Redirect(http.StatusFound, "/purchase_history")
		return
	}
	// Only cancel if the combined order belongs to this user and ALL sub-orders are still pending.
	var combined models.CombinedOrder
	if err := h.DB.Where("id = ? AND user_id = ?", id, user.ID).
		Preload("Orders.OrderDetails").First(&combined).Error; err != nil {
		view.FlashSet(c, "error", "Order not found.")
		c.Redirect(http.StatusFound, "/purchase_history")
		return
	}
	if len(combined.Orders) == 0 {
		view.FlashSet(c, "error", "Order has no items to cancel.")
		c.Redirect(http.StatusFound, "/purchase_history/"+strconv.FormatUint(id, 10))
		return
	}
	for _, o := range combined.Orders {
		ds := ""
		if o.DeliveryStatus != nil {
			ds = *o.DeliveryStatus
		}
		if ds != "pending" {
			view.FlashSet(c, "error", "Order cannot be cancelled once processing has started.")
			c.Redirect(http.StatusFound, "/purchase_history/"+strconv.FormatUint(id, 10))
			return
		}
	}
	// Cancel all sub-orders and restore stock.
	h.DB.Transaction(func(tx *gorm.DB) error {
		cancelled := "cancelled"
		tx.Model(&models.Order{}).Where("combined_order_id = ?", id).
			Updates(map[string]interface{}{"delivery_status": cancelled})
		tx.Model(&models.OrderDetail{}).
			Where("order_id IN (SELECT id FROM orders WHERE combined_order_id = ?)", id).
			Updates(map[string]interface{}{"delivery_status": cancelled})
		// Restore stock for each item.
		for _, o := range combined.Orders {
			for _, d := range o.OrderDetails {
				qty := 0
				if d.Quantity != nil {
					qty = *d.Quantity
				}
				if qty > 0 {
					tx.Model(&models.Product{}).Where("id = ?", d.ProductID).
						Update("current_stock", gorm.Expr("current_stock + ?", qty))
				}
			}
		}
		return nil
	})
	view.FlashSet(c, "success", "Order cancelled successfully.")
	c.Redirect(http.StatusFound, "/purchase_history/"+strconv.FormatUint(id, 10))
}

// POST /purchase_history/:id/refund
func (h *AccountHandler) RequestRefund(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.Redirect(http.StatusFound, "/purchase_history")
		return
	}
	reason := strings.TrimSpace(c.PostForm("reason"))
	if reason == "" {
		view.FlashSet(c, "error", "Please describe the reason for your refund request.")
		c.Redirect(http.StatusFound, "/purchase_history/"+strconv.FormatUint(id, 10))
		return
	}
	// Verify the combined order belongs to this user and load sub-orders.
	var combined models.CombinedOrder
	if err := h.DB.Where("id = ? AND user_id = ?", id, user.ID).
		Preload("Orders").First(&combined).Error; err != nil {
		view.FlashSet(c, "error", "Order not found.")
		c.Redirect(http.StatusFound, "/purchase_history")
		return
	}
	if len(combined.Orders) == 0 {
		view.FlashSet(c, "error", "Order has no items.")
		c.Redirect(http.StatusFound, "/purchase_history/"+strconv.FormatUint(id, 10))
		return
	}
	// Only allow refund requests on fully delivered orders.
	for _, o := range combined.Orders {
		ds := ""
		if o.DeliveryStatus != nil {
			ds = *o.DeliveryStatus
		}
		if ds != "delivered" {
			view.FlashSet(c, "error", "Refund requests can only be made after your order has been delivered.")
			c.Redirect(http.StatusFound, "/purchase_history/"+strconv.FormatUint(id, 10))
			return
		}
	}
	// Use the first sub-order for the refund record (one request per combined order).
	order := combined.Orders[0]
	// Check if a refund request already exists for this combined order.
	var existing models.RefundRequest
	if h.DB.Where("order_id = ? AND user_id = ?", order.ID, user.ID).First(&existing).Error == nil {
		view.FlashSet(c, "error", "A refund request for this order already exists.")
		c.Redirect(http.StatusFound, "/purchase_history/"+strconv.FormatUint(id, 10))
		return
	}
	h.DB.Create(&models.RefundRequest{
		OrderID: order.ID,
		UserID:  user.ID,
		Reason:  reason,
		Status:  "pending",
	})
	view.FlashSet(c, "success", "Refund request submitted. We'll review it within 2 business days.")
	c.Redirect(http.StatusFound, "/purchase_history/"+strconv.FormatUint(id, 10))
}

// GET /wishlists
func (h *AccountHandler) Wishlists(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var wishlist []models.Wishlist
	h.DB.Where("user_id = ?", user.ID).Preload("Product").Find(&wishlist)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/wishlists", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Wishlist":   wishlist,
	})
}

// POST /wishlists/toggle
func (h *AccountHandler) WishlistToggle(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	productIDStr := c.PostForm("product_id")
	productID, err := strconv.ParseUint(productIDStr, 10, 64)
	if err != nil || productID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product"})
		return
	}

	var w models.Wishlist
	err = h.DB.Where("user_id = ? AND product_id = ?", user.ID, productID).First(&w).Error
	if err == nil {
		h.DB.Delete(&w)
		c.JSON(http.StatusOK, gin.H{"added": false})
	} else {
		pid := uint(productID)
		h.DB.Create(&models.Wishlist{UserID: user.ID, ProductID: pid})
		c.JSON(http.StatusOK, gin.H{"added": true})
	}
}

// GET /wallet
func (h *AccountHandler) Wallet(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var transactions []models.Wallet
	h.DB.Where("user_id = ?", user.ID).Order("created_at desc").Find(&transactions)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/wallet", gin.H{
		"Categories":   h.navCats(c),
		"User":         user,
		"Transactions": transactions,
		"Balance":      user.Balance,
	})
}

// GET /addresses
func (h *AccountHandler) Addresses(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var addresses []models.Address
	h.DB.Where("user_id = ?", user.ID).Find(&addresses)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/addresses", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Addresses":  addresses,
		"Success":    c.Query("saved"),
	})
}

// addrFromForm builds an Address from POST form values (shared by add & update).
func addrFromForm(c *gin.Context) (name, email, addr, city, state, country, phone, postal string) {
	name    = strings.TrimSpace(c.PostForm("name"))
	email   = strings.TrimSpace(c.PostForm("email"))
	addr    = strings.TrimSpace(c.PostForm("address"))
	city    = strings.TrimSpace(c.PostForm("city"))
	state   = strings.TrimSpace(c.PostForm("state"))
	country = strings.TrimSpace(c.PostForm("country"))
	phone   = strings.TrimSpace(c.PostForm("phone"))
	postal  = strings.TrimSpace(c.PostForm("postal_code"))
	return
}

// POST /addresses
func (h *AccountHandler) AddAddress(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	name, email, addr, city, state, country, phone, postal := addrFromForm(c)
	a := models.Address{
		UserID:     user.ID,
		Name:       &name,
		Email:      &email,
		Address:    &addr,
		City:       &city,
		State:      &state,
		Country:    &country,
		Phone:      &phone,
		PostalCode: &postal,
	}
	h.DB.Create(&a)
	c.Redirect(http.StatusFound, "/addresses?saved=1")
}

// POST /addresses/:id/update
func (h *AccountHandler) UpdateAddress(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.Redirect(http.StatusFound, "/addresses")
		return
	}
	// Verify ownership before updating.
	var existing models.Address
	if h.DB.Where("id = ? AND user_id = ?", id, user.ID).First(&existing).Error != nil {
		view.FlashSet(c, "error", "Address not found.")
		c.Redirect(http.StatusFound, "/addresses")
		return
	}
	name, email, addr, city, state, country, phone, postal := addrFromForm(c)
	h.DB.Model(&existing).Updates(map[string]interface{}{
		"name":        name,
		"email":       email,
		"address":     addr,
		"city":        city,
		"state":       state,
		"country":     country,
		"phone":       phone,
		"postal_code": postal,
	})
	c.Redirect(http.StatusFound, "/addresses?saved=1")
}

// POST /addresses/delete/:id
func (h *AccountHandler) DeleteAddress(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		view.FlashSet(c, "error", "Invalid address.")
		c.Redirect(http.StatusFound, "/addresses")
		return
	}
	result := h.DB.Where("id = ? AND user_id = ?", id, user.ID).Delete(&models.Address{})
	if result.RowsAffected == 0 {
		view.FlashSet(c, "error", "Address not found.")
	}
	c.Redirect(http.StatusFound, "/addresses")
}

// POST /addresses/default/:id
func (h *AccountHandler) SetDefaultAddress(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		view.FlashSet(c, "error", "Invalid address.")
		c.Redirect(http.StatusFound, "/addresses")
		return
	}
	// Verify ownership before changing default.
	var addr models.Address
	if h.DB.Where("id = ? AND user_id = ?", id, user.ID).First(&addr).Error != nil {
		view.FlashSet(c, "error", "Address not found.")
		c.Redirect(http.StatusFound, "/addresses")
		return
	}
	// Both updates must be atomic to avoid a window where no default exists.
	h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Address{}).Where("user_id = ?", user.ID).Update("set_default", 0).Error; err != nil {
			return err
		}
		return tx.Model(&models.Address{}).Where("id = ? AND user_id = ?", id, user.ID).Update("set_default", 1).Error
	})
	c.Redirect(http.StatusFound, "/addresses")
}

// GET /profile
func (h *AccountHandler) Profile(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/profile", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Success":    c.Query("saved"),
		"Error":      c.Query("error"),
	})
}

// POST /profile
func (h *AccountHandler) UpdateProfile(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	name := strings.TrimSpace(c.PostForm("name"))
	phone := strings.TrimSpace(c.PostForm("phone"))

	updates := map[string]interface{}{}
	if name != "" {
		updates["name"] = name
	}
	if phone != "" {
		updates["phone"] = phone
	}

	// Password change
	newPass := c.PostForm("new_password")
	if newPass != "" {
		if len(newPass) < 8 {
			c.Redirect(http.StatusFound, "/profile?error=password_too_short")
			return
		}
		currentPass := c.PostForm("current_password")
		if user.Password != nil {
			if err := bcrypt.CompareHashAndPassword([]byte(*user.Password), []byte(currentPass)); err != nil {
				c.Redirect(http.StatusFound, "/profile?error=wrong_password")
				return
			}
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
		if err != nil {
			c.Redirect(http.StatusFound, "/profile?error=server_error")
			return
		}
		updates["password"] = string(hash)
	}

	if len(updates) > 0 {
		h.DB.Model(user).Updates(updates)
	}

	c.Redirect(http.StatusFound, "/profile?saved=1")
}

// ── Affiliate ──────────────────────────────────────────────────────────────────

func (h *AccountHandler) AffiliateDashboard(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var logs []models.AffiliateLog
	h.DB.Where("user_id = ?", user.ID).Order("created_at desc").Limit(20).Find(&logs)

	var totalEarnings float64
	h.DB.Model(&models.AffiliateLog{}).
		Where("user_id = ?", user.ID).
		Select("COALESCE(SUM(amount),0)").Scan(&totalEarnings)

	var pendingWithdraw float64
	h.DB.Model(&models.AffiliateWithdraw{}).
		Where("user_id = ? AND status = 'pending'", user.ID).
		Select("COALESCE(SUM(amount),0)").Scan(&pendingWithdraw)

	commission := h.Settings.Get("affiliate_commission", "5")
	minPayout := h.Settings.Get("affiliate_min_payout", "10")
	enabled := h.Settings.Get("affiliate_status", "1")

	var refCode string
	if user.ReferralCode != nil {
		refCode = *user.ReferralCode
	}

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/affiliate", gin.H{
		"Categories":     h.navCats(c),
		"User":           user,
		"RefCode":        refCode,
		"Logs":           logs,
		"TotalEarnings":  totalEarnings,
		"PendingWithdraw": pendingWithdraw,
		"Commission":     commission,
		"MinPayout":      minPayout,
		"Enabled":        enabled,
	})
}

func (h *AccountHandler) AffiliateJoin(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	if user.ReferralCode != nil && *user.ReferralCode != "" {
		c.Redirect(http.StatusFound, "/affiliate")
		return
	}
	// Use crypto/rand so codes are not guessable from the sequential user ID.
	b := make([]byte, 6)
	if _, err := cryptoRand.Read(b); err != nil {
		c.Redirect(http.StatusFound, "/affiliate?error=server")
		return
	}
	code := "REF" + hex.EncodeToString(b)
	h.DB.Model(user).Update("referral_code", &code)
	c.Redirect(http.StatusFound, "/affiliate")
}

func (h *AccountHandler) AffiliateWithdraw(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	amountStr := c.PostForm("amount")
	amount, _ := strconv.ParseFloat(amountStr, 64)
	minPayout, _ := strconv.ParseFloat(h.Settings.Get("affiliate_min_payout", "10"), 64)

	if amount < minPayout {
		c.Redirect(http.StatusFound, "/affiliate?error=min_payout")
		return
	}

	// Check balance and insert atomically to prevent double-withdrawal race.
	// FOR UPDATE locks the withdrawal rows so two concurrent transactions
	// cannot both read the same balance and both succeed.
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		var totalEarnings float64
		tx.Model(&models.AffiliateLog{}).Where("user_id = ?", user.ID).
			Select("COALESCE(SUM(amount),0)").Scan(&totalEarnings)

		var totalWithdrawn float64
		tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Model(&models.AffiliateWithdraw{}).
			Where("user_id = ? AND status IN ('pending','approved')", user.ID).
			Select("COALESCE(SUM(amount),0)").Scan(&totalWithdrawn)

		if amount > totalEarnings-totalWithdrawn {
			return fmt.Errorf("insufficient")
		}
		return tx.Create(&models.AffiliateWithdraw{UserID: user.ID, Amount: amount}).Error
	})
	if txErr != nil {
		if txErr.Error() == "insufficient" {
			c.Redirect(http.StatusFound, "/affiliate?error=insufficient")
		} else {
			c.Redirect(http.StatusFound, "/affiliate?error=server")
		}
		return
	}
	c.Redirect(http.StatusFound, "/affiliate?success=1")
}

// ── Support Tickets ──────────────────────────────────────────────────────────

// TicketList lists the current user's support tickets.
func (h *AccountHandler) TicketList(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var tickets []models.Ticket
	h.DB.Where("user_id = ?", user.ID).
		Order("updated_at desc").
		Preload("Replies").
		Find(&tickets)

	// Pre-compute status counts for the stats row
	stats := map[string]int{"all": len(tickets), "pending": 0, "open": 0, "answered": 0, "closed": 0}
	for _, t := range tickets {
		if _, ok := stats[t.Status]; ok {
			stats[t.Status]++
		}
	}

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/tickets", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Tickets":    tickets,
		"Stats":      stats,
		"ActiveNav":  "tickets",
	})
}

// TicketCreate shows the create-ticket form.
func (h *AccountHandler) TicketCreate(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/ticket_create", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"ActiveNav":  "tickets",
	})
}

// TicketStore processes new ticket submission.
func (h *AccountHandler) TicketStore(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	subject := strings.TrimSpace(c.PostForm("subject"))
	details := strings.TrimSpace(c.PostForm("details"))
	priority := c.PostForm("priority")
	ticketType := c.PostForm("type")

	if subject == "" || details == "" {
		h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/ticket_create", gin.H{
			"Categories": h.navCats(c),
			"User":       user,
			"ActiveNav":  "tickets",
			"Error":      "Subject and details are required.",
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
		UserID:   user.ID,
		Subject:  subject,
		Details:  &details,
		Priority: priority,
		Type:     ticketType,
		Status:   "pending",
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		h.Engine.Render(c, http.StatusInternalServerError, h.Engine.Theme(), "frontend/ticket_create", gin.H{
			"Categories": h.navCats(c),
			"User":       user,
			"ActiveNav":  "tickets",
			"Error":      "Failed to submit ticket. Please try again.",
		})
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/tickets/%d", ticket.ID))
}

// TicketDetail shows a single ticket thread for the owner.
func (h *AccountHandler) TicketDetail(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/tickets")
		return
	}

	var ticket models.Ticket
	if err := h.DB.Where("id = ? AND user_id = ?", id, user.ID).
		Preload("Replies.User").Preload("User").
		First(&ticket).Error; err != nil {
		c.Redirect(http.StatusFound, "/tickets")
		return
	}

	// Mark as seen by client
	h.DB.Model(&ticket).Update("client_viewed", 1)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/ticket_detail", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Ticket":     ticket,
		"ActiveNav":  "tickets",
	})
}

// TicketReply adds a customer reply to an existing ticket.
func (h *AccountHandler) TicketReply(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/tickets")
		return
	}

	var ticket models.Ticket
	if err := h.DB.Where("id = ? AND user_id = ?", id, user.ID).First(&ticket).Error; err != nil {
		c.Redirect(http.StatusFound, "/tickets")
		return
	}

	reply := strings.TrimSpace(c.PostForm("reply"))
	if reply == "" {
		c.Redirect(http.StatusFound, fmt.Sprintf("/tickets/%d", id))
		return
	}

	h.DB.Create(&models.TicketReply{
		TicketID: uint(id),
		UserID:   user.ID,
		Reply:    reply,
	})

	// Re-open if closed/answered; always mark as unseen by admin
	newStatus := ticket.Status
	if ticket.Status == "closed" || ticket.Status == "answered" {
		newStatus = "open"
	}
	h.DB.Model(&ticket).Updates(map[string]interface{}{
		"status":  newStatus,
		"viewed":  0,
		"client_viewed": 1,
	})

	c.Redirect(http.StatusFound, fmt.Sprintf("/tickets/%d", id))
}

// ── Auction Wins ─────────────────────────────────────────────────────────────

// GET /auction-wins
func (h *AccountHandler) AuctionWins(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var wins []models.AuctionWinner
	h.DB.Where("user_id = ? AND status IN ('pending','paid')", user.ID).
		Order("created_at desc").
		Preload("Product").
		Preload("Order.CombinedOrder").
		Find(&wins)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/auction_wins", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Wins":       wins,
	})
}

// GET /auction-wins/:id/checkout
func (h *AccountHandler) AuctionWinCheckout(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	wid, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || wid == 0 {
		c.Redirect(http.StatusFound, "/auction-wins")
		return
	}

	var win models.AuctionWinner
	if err := h.DB.Where("id = ? AND user_id = ? AND status = 'pending'", wid, user.ID).
		Preload("Product").First(&win).Error; err != nil {
		view.FlashSet(c, "error", "Auction win not found or already settled.")
		c.Redirect(http.StatusFound, "/auction-wins")
		return
	}

	var addresses []models.Address
	h.DB.Where("user_id = ?", user.ID).Find(&addresses)

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/auction_checkout", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Win":        win,
		"Addresses":  addresses,
	})
}

// POST /auction-wins/:id/checkout
func (h *AccountHandler) AuctionWinPlaceOrder(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	wid, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || wid == 0 {
		c.Redirect(http.StatusFound, "/auction-wins")
		return
	}

	var win models.AuctionWinner
	if err := h.DB.Where("id = ? AND user_id = ? AND status = 'pending'", wid, user.ID).
		Preload("Product").First(&win).Error; err != nil {
		view.FlashSet(c, "error", "Auction win not found or already settled.")
		c.Redirect(http.StatusFound, "/auction-wins")
		return
	}

	// Resolve shipping address.
	shippingAddr := ""
	if addrID := strings.TrimSpace(c.PostForm("address_id")); addrID != "" && addrID != "0" {
		var addr models.Address
		if h.DB.Where("id = ? AND user_id = ?", addrID, user.ID).First(&addr).Error == nil && addr.Address != nil {
			shippingAddr = *addr.Address
		}
	}
	if shippingAddr == "" {
		parts := []string{
			strings.TrimSpace(c.PostForm("address")),
			strings.TrimSpace(c.PostForm("city")),
			strings.TrimSpace(c.PostForm("postal_code")),
		}
		var nonEmpty []string
		for _, p := range parts {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		shippingAddr = strings.Join(nonEmpty, ", ")
	}

	paymentType := strings.TrimSpace(c.PostForm("payment_type"))
	if paymentType == "" {
		paymentType = "wallet"
	}

	amount := win.Amount
	now := int(time.Now().Unix())

	var combinedOrderID uint
	var orderID uint
	var insufficient bool
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		// 1. Create CombinedOrder first so sub-order can reference it immediately.
		combined := models.CombinedOrder{
			UserID:          user.ID,
			GrandTotal:      amount,
			ShippingAddress: &shippingAddr,
		}
		if err := tx.Create(&combined).Error; err != nil {
			return fmt.Errorf("create combined order: %w", err)
		}
		combinedOrderID = combined.ID

		// 2. Build the sub-order for the auctioned product.
		var sellerPtr *uint
		if win.Product != nil && win.Product.UserID > 0 {
			sid := win.Product.UserID
			sellerPtr = &sid
		}
		// Default: order is unpaid until payment is actually settled below.
		orderPaid := "unpaid"
		// 3. Settle payment.
		//    - "wallet": atomically lock user row, verify balance, debit it,
		//      and only then mark the order + auction win as paid. Without
		//      this check the user would receive the auction product for
		//      free since previously the win was flagged "paid" with no
		//      payment processing.
		//    - any other gateway: order stays unpaid + win stays pending;
		//      the user must complete payment via /pay/<combined> and the
		//      gateway webhook will flip the order to paid. We mark the
		//      AuctionWinner row "pending_payment" so the same win can't be
		//      re-checked-out from the /auction-wins page while a gateway
		//      transaction is in flight.
		winnerStatus := "pending_payment"
		if paymentType == "wallet" {
			var u models.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", user.ID).First(&u).Error; err != nil {
				return fmt.Errorf("lock user: %w", err)
			}
			if u.Balance < amount {
				insufficient = true
				return fmt.Errorf("insufficient wallet balance")
			}
			if err := tx.Model(&models.User{}).Where("id = ?", user.ID).
				Update("balance", gorm.Expr("balance - ?", amount)).Error; err != nil {
				return fmt.Errorf("debit wallet: %w", err)
			}
			orderPaid = "paid"
			winnerStatus = "paid"
		}

		order := models.Order{
			CombinedOrderID: &combined.ID,
			UserID:          &user.ID,
			SellerID:        sellerPtr,
			GrandTotal:      &amount,
			ShippingAddress: &shippingAddr,
			ShippingType:    "home_delivery",
			OrderFrom:       "auction",
			DeliveryStatus:  strPtr("pending"),
			PaymentStatus:   &orderPaid,
			PaymentType:     &paymentType,
			Date:            now,
		}
		if err := tx.Create(&order).Error; err != nil {
			return fmt.Errorf("create order: %w", err)
		}
		orderID = order.ID

		if win.Product != nil {
			qty := 1
			pid := win.ProductID
			detail := models.OrderDetail{
				OrderID:   order.ID,
				SellerID:  sellerPtr,
				ProductID: pid,
				Price:     &amount,
				Quantity:  &qty,
			}
			if err := tx.Create(&detail).Error; err != nil {
				return fmt.Errorf("create order detail: %w", err)
			}
			// OrderDetail payment_status mirrors the Order so webhook helpers
			// and admin views read a consistent state.
			tx.Model(&models.OrderDetail{}).Where("id = ?", detail.ID).
				Update("payment_status", orderPaid)

			// Decrement stock (physical auction goods have exactly 1 unit).
			tx.Model(&models.Product{}).Where("id = ?", pid).
				Update("current_stock", gorm.Expr("GREATEST(current_stock - 1, 0)"))
			tx.Model(&models.Product{}).Where("id = ?", pid).
				Update("num_of_sale", gorm.Expr("num_of_sale + 1"))
		}

		// 4. Update the auction win (paid for wallet, pending_payment otherwise).
		if err := tx.Model(&models.AuctionWinner{}).Where("id = ?", win.ID).
			Updates(map[string]interface{}{"status": winnerStatus, "order_id": orderID}).Error; err != nil {
			return fmt.Errorf("update auction winner: %w", err)
		}

		return nil
	})
	if insufficient {
		view.FlashSet(c, "error", "Insufficient wallet balance. Please top up your wallet or choose another payment method.")
		c.Redirect(http.StatusFound, fmt.Sprintf("/auction-wins/%d/checkout", wid))
		return
	}
	if txErr != nil {
		view.FlashSet(c, "error", "Order failed: "+txErr.Error())
		c.Redirect(http.StatusFound, fmt.Sprintf("/auction-wins/%d/checkout", wid))
		return
	}

	// If a non-wallet gateway was chosen, send the user to the payment page
	// for this combined order — payment_status flips to paid in the webhook.
	if paymentType != "wallet" {
		c.Redirect(http.StatusFound, fmt.Sprintf("/pay/%d", combinedOrderID))
		return
	}

	view.FlashSet(c, "success", "Order placed! Your auction purchase is confirmed.")
	// Redirect to purchase history so the user can track their order.
	c.Redirect(http.StatusFound, fmt.Sprintf("/purchase_history/%d", combinedOrderID))
}

// GET /download/:order_id/:detail_id
// Securely serves a digital product file after verifying order ownership and payment.
func (h *AccountHandler) DownloadDigital(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	orderID, err1 := strconv.ParseUint(c.Param("order_id"), 10, 64)
	detailID, err2 := strconv.ParseUint(c.Param("detail_id"), 10, 64)
	if err1 != nil || err2 != nil || orderID == 0 || detailID == 0 {
		c.String(http.StatusBadRequest, "invalid request")
		return
	}

	// Load order detail and verify the order belongs to this user.
	var detail models.OrderDetail
	err := h.DB.
		Joins("JOIN orders ON orders.id = order_details.order_id").
		Where("order_details.id = ? AND order_details.order_id = ? AND orders.user_id = ?", detailID, orderID, user.ID).
		Preload("Product").
		First(&detail).Error
	if err != nil {
		c.String(http.StatusNotFound, "order not found")
		return
	}

	// Verify payment is complete.
	if detail.PaymentStatus != "paid" {
		// Also accept if parent order is paid.
		var parentPaid string
		h.DB.Model(&models.Order{}).Select("COALESCE(payment_status,'unpaid')").
			Where("id = ?", detail.OrderID).Scan(&parentPaid)
		if parentPaid != "paid" {
			c.String(http.StatusForbidden, "payment required before downloading")
			return
		}
	}

	product := detail.Product
	if product == nil || product.Digital != 1 {
		c.String(http.StatusBadRequest, "this item is not a digital product")
		return
	}

	// Serve the local digital file.
	if product.FilePath != nil && *product.FilePath != "" {
		// Resolve absolute path: UploadDir + FilePath.
		uploadRoot := h.UploadDir
		if uploadRoot == "" {
			uploadRoot = "uploads"
		}
		abs, absErr := filepath.Abs(uploadRoot)
		if absErr != nil {
			c.String(http.StatusInternalServerError, "server configuration error")
			return
		}
		// saveDigitalFile historically stored FilePath as "uploads/digital/<file>"
		// — i.e. already prefixed with the upload dir name. Strip a single
		// leading "uploads/" so we don't end up with /…/uploads/uploads/…
		rel := filepath.FromSlash(*product.FilePath)
		trimPrefix := strings.TrimRight(filepath.Base(abs), string(os.PathSeparator)) + string(os.PathSeparator)
		if strings.HasPrefix(rel, trimPrefix) {
			rel = strings.TrimPrefix(rel, trimPrefix)
		}
		fullPath := filepath.Join(abs, rel)
		// Guard against path traversal.
		if !strings.HasPrefix(fullPath, abs+string(os.PathSeparator)) {
			c.String(http.StatusBadRequest, "invalid file path")
			return
		}
		filename := filepath.Base(fullPath)
		if product.FileName != nil && *product.FileName != "" {
			filename = *product.FileName
		}
		c.FileAttachment(fullPath, filename)
		return
	}

	c.String(http.StatusNotFound, "no download file available for this product")
}
