package frontend

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/services/settings"
	"mall/internal/view"
)

type CartHandler struct {
	DB       *gorm.DB
	Engine   *view.Engine
	Settings *settings.Store
}

func (h *CartHandler) navCats(c *gin.Context) []models.Category {
	return navTranslatedCats(h.DB, c)
}

// userOrTempID returns the authenticated user's ID or nil, and the temp cart string ID.
func userOrTempID(c *gin.Context) (*uint, string) {
	if u, ok := c.Get("user"); ok {
		user := u.(*models.User)
		return &user.ID, ""
	}
	return nil, guestCartID(c)
}

// cartQuery returns a base GORM scope that filters cart rows for the current visitor.
func (h *CartHandler) cartQuery(c *gin.Context) *gorm.DB {
	uid, tempID := userOrTempID(c)
	q := h.DB.Model(&models.Cart{}).Where("status = 1")
	if uid != nil {
		return q.Where("user_id = ?", *uid)
	}
	return q.Where("temp_user_id = ?", tempID)
}

// fillVariantImages sets each line's VariantImage from its matching product_stocks row,
// so cart/checkout can display the selected variant's featured image.
func (h *CartHandler) fillVariantImages(lines []models.Cart) {
	for i := range lines {
		l := &lines[i]
		if l.ProductID == nil || l.Variation == nil || *l.Variation == "" {
			continue
		}
		var stock models.ProductStock
		if err := h.DB.Select("variant_image").
			Where("product_id = ? AND variant = ?", *l.ProductID, *l.Variation).
			First(&stock).Error; err == nil && stock.VariantImage != nil && *stock.VariantImage != "" {
			l.VariantImage = stock.VariantImage
		}
	}
}

// GET /cart
func (h *CartHandler) Show(c *gin.Context) {
	var lines []models.Cart
	h.cartQuery(c).Preload("Product").Find(&lines)
	h.fillVariantImages(lines)

	var subtotal float64
	for _, l := range lines {
		if l.Price != nil {
			subtotal += *l.Price * float64(l.Quantity)
		}
	}

	shippingMethod, shippingCost, freeThreshold := h.shippingFor(subtotal)
	if allDigital(lines) {
		shippingCost = 0
		freeThreshold = 0
	}
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/cart", gin.H{
		"Categories":            h.navCats(c),
		"Lines":                 lines,
		"Subtotal":              subtotal,
		"ShippingMethod":        shippingMethod,
		"ShippingCost":          shippingCost,
		"FreeShippingThreshold": freeThreshold,
		"FreeShippingRemaining": freeShippingRemaining(subtotal, freeThreshold),
		"Total":                 subtotal + shippingCost,
	})
}

// freeShippingRemaining returns how much more the buyer needs to spend to
// reach the free-shipping threshold; 0 once met or if no threshold is set.
func freeShippingRemaining(subtotal, threshold float64) float64 {
	if threshold <= 0 || subtotal >= threshold {
		return 0
	}
	return threshold - subtotal
}

// shippingFor returns the configured method label, the EFFECTIVE shipping cost
// for the given subtotal (zero if the free-shipping threshold is met), and the
// raw threshold. A threshold of 0 means free-shipping-over is disabled.
func (h *CartHandler) shippingFor(subtotal float64) (method string, cost float64, threshold float64) {
	if h.Settings == nil {
		return
	}
	method = h.Settings.Get("shipping_method", "")
	cost, _ = strconv.ParseFloat(strings.TrimSpace(h.Settings.Get("shipping_cost", "0")), 64)
	threshold, _ = strconv.ParseFloat(strings.TrimSpace(h.Settings.Get("free_shipping_threshold", "0")), 64)
	if cost < 0 {
		cost = 0
	}
	if threshold < 0 {
		threshold = 0
	}
	if threshold > 0 && subtotal >= threshold {
		cost = 0
	}
	return
}

// allDigital returns true when every cart line points at a digital product.
// Used to suppress shipping cost on digital-only orders, since they ship nothing.
func allDigital(lines []models.Cart) bool {
	if len(lines) == 0 {
		return false
	}
	for _, l := range lines {
		if l.Product == nil || l.Product.Digital != 1 {
			return false
		}
	}
	return true
}

// POST /cart/add
func (h *CartHandler) Add(c *gin.Context) {
	productIDStr := c.PostForm("product_id")
	qtyStr := c.PostForm("quantity")
	variant := strings.TrimSpace(c.PostForm("variant"))
	productID, err := strconv.ParseUint(productIDStr, 10, 64)
	if err != nil || productID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product"})
		return
	}

	qty, _ := strconv.Atoi(qtyStr)
	if qty <= 0 {
		qty = 1
	}
	if qty > 99999 {
		qty = 99999
	}

	var product models.Product
	if err := h.DB.First(&product, productID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}

	// Auction products must be won via bidding — they cannot be added to the cart.
	if product.AuctionProduct == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Auction products cannot be added to cart. Please place a bid instead."})
		return
	}

	// Digital products have no physical stock — cap at qty=1 and skip stock check.
	if product.Digital == 1 {
		qty = 1
	} else {
		// Enforce the product's minimum order quantity.
		if product.MinQty > 1 && qty < product.MinQty {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Minimum order quantity is %d", product.MinQty)})
			return
		}
		if product.CurrentStock < qty {
			// Reject add-to-cart when stock is already exhausted.
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient stock"})
			return
		}
	}

	// Use variant price if applicable
	price := product.UnitPrice
	if variant != "" {
		var stock models.ProductStock
		if err := h.DB.Where("product_id = ? AND variant = ?", productID, variant).First(&stock).Error; err == nil {
			price = stock.Price
		}
	}

	uid, tempID := userOrTempID(c)

	// Check if same product+variant line already exists
	var existing models.Cart
	q := h.DB.Where("product_id = ? AND status = 1", productID)
	if variant != "" {
		q = q.Where("variation = ?", variant)
	} else {
		q = q.Where("(variation IS NULL OR variation = '')")
	}
	if uid != nil {
		q = q.Where("user_id = ?", *uid)
	} else {
		q = q.Where("temp_user_id = ?", tempID)
	}

	if err := q.First(&existing).Error; err == nil {
		// For digital products, qty is always 1; for physical, guard against oversell.
		newQty := existing.Quantity + qty
		if product.Digital != 1 && newQty > product.CurrentStock {
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient stock"})
			return
		}
		if product.Digital == 1 {
			newQty = 1 // cap digital at one copy
		}
		h.DB.Model(&existing).Update("quantity", newQty)
	} else {
		pid := uint(productID)
		ownerID := product.UserID
		line := models.Cart{
			Status:    1,
			OwnerID:   &ownerID,
			ProductID: &pid,
			Price:     &price,
			Quantity:  qty,
		}
		if variant != "" {
			line.Variation = &variant
		}
		if uid != nil {
			line.UserID = uid
		} else {
			line.TempUserID = &tempID
		}
		h.DB.Create(&line)
	}

	// Redirect back or return JSON for AJAX
	if c.GetHeader("Accept") == "application/json" || c.GetHeader("X-Requested-With") == "XMLHttpRequest" {
		var count int64
		qc := h.DB.Model(&models.Cart{}).Where("status = 1")
		if uid != nil {
			qc = qc.Where("user_id = ?", *uid)
		} else {
			qc = qc.Where("temp_user_id = ?", tempID)
		}
		qc.Count(&count)
		c.JSON(http.StatusOK, gin.H{"success": true, "cart_count": count})
		return
	}
	c.Redirect(http.StatusFound, "/cart")
}

// POST /cart/update
func (h *CartHandler) Update(c *gin.Context) {
	idStr := c.PostForm("id")
	qtyStr := c.PostForm("quantity")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/cart")
		return
	}
	qty, _ := strconv.Atoi(qtyStr)
	if qty > 99999 {
		qty = 99999
	}

	uid, tempID := userOrTempID(c)
	q := h.DB.Model(&models.Cart{}).Where("id = ? AND status = 1", id)
	if uid != nil {
		q = q.Where("user_id = ?", *uid)
	} else {
		q = q.Where("temp_user_id = ?", tempID)
	}

	if qty <= 0 {
		q.Delete(&models.Cart{})
	} else {
		// Enforce the product's minimum order quantity on the cart line.
		var line models.Cart
		lq := h.DB.Where("id = ? AND status = 1", id)
		if uid != nil {
			lq = lq.Where("user_id = ?", *uid)
		} else {
			lq = lq.Where("temp_user_id = ?", tempID)
		}
		if err := lq.First(&line).Error; err == nil && line.ProductID != nil {
			var product models.Product
			if err := h.DB.First(&product, *line.ProductID).Error; err == nil {
				if product.Digital != 1 && product.MinQty > 1 && qty < product.MinQty {
					qty = product.MinQty
				}
			}
		}
		q.Update("quantity", qty)
	}
	c.Redirect(http.StatusFound, "/cart")
}

// POST /cart/remove/:id
func (h *CartHandler) Remove(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/cart")
		return
	}
	uid, tempID := userOrTempID(c)
	q := h.DB.Where("id = ? AND status = 1", id)
	if uid != nil {
		q = q.Where("user_id = ?", *uid)
	} else {
		q = q.Where("temp_user_id = ?", tempID)
	}
	q.Delete(&models.Cart{})
	c.Redirect(http.StatusFound, "/cart")
}

// POST /coupon/apply  (requires auth)
func (h *CartHandler) ApplyCoupon(c *gin.Context) {
	user := c.MustGet("user").(*models.User)
	code := strings.TrimSpace(c.PostForm("coupon_code"))
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Please enter a coupon code."})
		return
	}

	var coupon models.Coupon
	if err := h.DB.Where("LOWER(code) = LOWER(?) AND status = 1", code).First(&coupon).Error; err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Invalid or expired coupon code."})
		return
	}

	now := int(time.Now().Unix())
	if coupon.StartDate != nil && *coupon.StartDate > now {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "This coupon is not yet active."})
		return
	}
	if coupon.EndDate != nil && *coupon.EndDate < now {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "This coupon has expired."})
		return
	}

	// Calculate subtotal from cart.
	var lines []models.Cart
	h.DB.Where("user_id = ? AND status = 1", user.ID).Preload("Product").Find(&lines)
	var subtotal float64
	for _, l := range lines {
		if l.Price != nil {
			subtotal += *l.Price * float64(l.Quantity)
		}
	}
	if subtotal == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Your cart is empty."})
		return
	}

	var discountAmt float64
	if coupon.DiscountType == "percent" {
		discountAmt = subtotal * coupon.Discount / 100
	} else {
		discountAmt = coupon.Discount
	}
	if discountAmt > subtotal {
		discountAmt = subtotal
	}

	// Persist applied coupon in session.
	sess := sessions.Default(c)
	sess.Set("coupon_code", coupon.Code)
	sess.Set("coupon_id", int(coupon.ID))
	sess.Set("coupon_discount", discountAmt)
	_ = sess.Save()

	_, shippingCost, _ := h.shippingFor(subtotal - discountAmt)
	if allDigital(lines) {
		shippingCost = 0
	}
	c.JSON(http.StatusOK, gin.H{
		"code":          coupon.Code,
		"discount":      discountAmt,
		"final_total":   subtotal - discountAmt + shippingCost,
		"subtotal":      subtotal,
		"shipping_cost": shippingCost,
	})
}

// POST /coupon/remove  (requires auth)
func (h *CartHandler) RemoveCoupon(c *gin.Context) {
	sess := sessions.Default(c)
	sess.Delete("coupon_code")
	sess.Delete("coupon_id")
	sess.Delete("coupon_discount")
	_ = sess.Save()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /checkout  (requires auth)
func (h *CartHandler) Checkout(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var lines []models.Cart
	h.DB.Where("user_id = ? AND status = 1", user.ID).Preload("Product").Find(&lines)
	if len(lines) == 0 {
		c.Redirect(http.StatusFound, "/cart")
		return
	}
	h.fillVariantImages(lines)

	var addresses []models.Address
	h.DB.Where("user_id = ?", user.ID).Find(&addresses)

	var subtotal float64
	for _, l := range lines {
		if l.Price != nil {
			subtotal += *l.Price * float64(l.Quantity)
		}
	}

	// Restore any applied coupon from session.
	sess := sessions.Default(c)
	appliedCode, _ := sess.Get("coupon_code").(string)
	appliedDiscount, _ := sess.Get("coupon_discount").(float64)

	gateways := gin.H{
		"paypal": h.Settings != nil && h.Settings.Get("paypal_enabled") == "1",
		"stripe": h.Settings != nil && h.Settings.Get("stripe_enabled") == "1",
		"alipay": h.Settings != nil && h.Settings.Get("alipay_enabled") == "1",
	}
	subtotalAfterCoupon := subtotal - appliedDiscount
	shippingMethod, shippingCost, freeThreshold := h.shippingFor(subtotalAfterCoupon)
	if allDigital(lines) {
		shippingCost = 0
		freeThreshold = 0
	}
	finalTotal := subtotalAfterCoupon + shippingCost
	if finalTotal < 0 {
		finalTotal = 0
	}
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/checkout", gin.H{
		"Categories":     h.navCats(c),
		"Lines":          lines,
		"Addresses":      addresses,
		"User":           user,
		"Subtotal":       subtotal,
		"CouponCode":     appliedCode,
		"CouponDiscount": appliedDiscount,
		"Gateways":              gateways,
		"ShippingMethod":        shippingMethod,
		"ShippingCost":          shippingCost,
		"FreeShippingThreshold": freeThreshold,
		"FreeShippingRemaining": freeShippingRemaining(subtotalAfterCoupon, freeThreshold),
		"FinalTotal":            finalTotal,
	})
}

// POST /checkout  (requires auth)
func (h *CartHandler) PlaceOrder(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	var lines []models.Cart
	h.DB.Where("user_id = ? AND status = 1", user.ID).Preload("Product").Find(&lines)
	if len(lines) == 0 {
		c.Redirect(http.StatusFound, "/cart")
		return
	}

	// Resolve shipping address: prefer saved address, fall back to manual entry.
	shippingAddr := ""
	if addrID := strings.TrimSpace(c.PostForm("address_id")); addrID != "" && addrID != "0" {
		var addr models.Address
		if h.DB.Where("id = ? AND user_id = ?", addrID, user.ID).First(&addr).Error == nil && addr.Address != nil {
			shippingAddr = *addr.Address
		}
	}
	if shippingAddr == "" {
		// Manual address fields from the form
		parts := []string{
			strings.TrimSpace(c.PostForm("address")),
			strings.TrimSpace(c.PostForm("city")),
			strings.TrimSpace(c.PostForm("state")),
			strings.TrimSpace(c.PostForm("postal_code")),
			strings.TrimSpace(c.PostForm("country")),
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

	// Validate and resolve coupon from session (re-checked server-side).
	sess := sessions.Default(c)
	var appliedCoupon *models.Coupon
	var couponDiscount float64
	if cid, ok := sess.Get("coupon_id").(int); ok && cid > 0 {
		var cp models.Coupon
		if h.DB.Where("id = ? AND status = 1", cid).First(&cp).Error == nil {
			nowTs := int(time.Now().Unix())
			valid := (cp.StartDate == nil || *cp.StartDate <= nowTs) &&
				(cp.EndDate == nil || *cp.EndDate >= nowTs)
			if valid {
				appliedCoupon = &cp
			}
		}
	}

	// Calculate grand total and group lines by seller before starting the transaction.
	var subtotalForCoupon float64
	for _, l := range lines {
		if l.Price != nil {
			subtotalForCoupon += *l.Price * float64(l.Quantity)
		}
	}
	if appliedCoupon != nil {
		if appliedCoupon.DiscountType == "percent" {
			couponDiscount = subtotalForCoupon * appliedCoupon.Discount / 100
		} else {
			couponDiscount = appliedCoupon.Discount
		}
		if couponDiscount > subtotalForCoupon {
			couponDiscount = subtotalForCoupon
		}
	}
	_, shippingCost, _ := h.shippingFor(subtotalForCoupon - couponDiscount)
	if allDigital(lines) {
		shippingCost = 0
	}
	grand := subtotalForCoupon - couponDiscount + shippingCost

	sellerLines := make(map[uint][]models.Cart)
	for _, l := range lines {
		sid := uint(0)
		if l.OwnerID != nil {
			sid = *l.OwnerID
		}
		sellerLines[sid] = append(sellerLines[sid], l)
	}

	var combinedID uint
	now := int(time.Now().Unix())

	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		// 1. Create the combined order.
		combined := models.CombinedOrder{
			UserID:          user.ID,
			GrandTotal:      grand,
			ShippingAddress: &shippingAddr,
		}
		if err := tx.Create(&combined).Error; err != nil {
			return fmt.Errorf("create combined order: %w", err)
		}
		combinedID = combined.ID

		// 1b. Record coupon usage.
		if appliedCoupon != nil {
			tx.Create(&models.CouponUsage{UserID: user.ID, CouponID: appliedCoupon.ID})
		}

		// 2. For each seller group, create one Order + its OrderDetails.
		// Distribute coupon discount proportionally across seller sub-orders.
		// The flat shipping cost is attached to the first seller's order so the
		// sum of per-seller Order.GrandTotal equals CombinedOrder.GrandTotal.
		firstSellerOrder := true
		for sellerID, sLines := range sellerLines {
			var sellerSubtotal float64
			for _, l := range sLines {
				if l.Price != nil {
					sellerSubtotal += *l.Price * float64(l.Quantity)
				}
			}
			sellerDiscount := 0.0
			if subtotalForCoupon > 0 {
				sellerDiscount = couponDiscount * sellerSubtotal / subtotalForCoupon
			}
			sellerTotal := sellerSubtotal - sellerDiscount
			if firstSellerOrder {
				sellerTotal += shippingCost
				firstSellerOrder = false
			}
			sid := sellerID
			var sellerPtr *uint
			if sid > 0 {
				sellerPtr = &sid
			}
			order := models.Order{
				CombinedOrderID: &combined.ID,
				UserID:          &user.ID,
				SellerID:        sellerPtr,
				GrandTotal:      &sellerTotal,
				CouponDiscount:  sellerDiscount,
				ShippingAddress: &shippingAddr,
				ShippingType:    "home_delivery",
				OrderFrom:       "web",
				DeliveryStatus:  strPtr("pending"),
				PaymentStatus:   strPtr("unpaid"),
				PaymentType:     &paymentType,
				Date:            now,
			}
			if err := tx.Create(&order).Error; err != nil {
				return fmt.Errorf("create order for seller %d: %w", sid, err)
			}

			for _, l := range sLines {
				if l.ProductID == nil {
					// Product was deleted after being added to cart; abort the whole order.
					return fmt.Errorf("product in cart no longer exists")
				}
				qty := l.Quantity

				isDigital := l.Product != nil && l.Product.Digital == 1
				if !isDigital {
					// Atomically decrement products.current_stock (race-free).
					result := tx.Model(&models.Product{}).
						Where("id = ? AND current_stock >= ?", *l.ProductID, qty).
						Update("current_stock", gorm.Expr("current_stock - ?", qty))
					if result.Error != nil {
						return fmt.Errorf("stock update for product %d: %w", *l.ProductID, result.Error)
					}
					if result.RowsAffected == 0 {
						name := ""
						if l.Product != nil {
							name = l.Product.Name
						}
						return fmt.Errorf("insufficient stock for product: %s", name)
					}

					// Also decrement variant-level stock in product_stocks if applicable.
					if l.Variation != nil && *l.Variation != "" {
						tx.Model(&models.ProductStock{}).
							Where("product_id = ? AND variant = ? AND qty >= ?", *l.ProductID, *l.Variation, qty).
							Update("qty", gorm.Expr("qty - ?", qty))
						// Non-fatal: if variant row not found, product-level stock still decremented.
					}
				}

				// Increment num_of_sale on the product (digital or physical).
				tx.Model(&models.Product{}).
					Where("id = ?", *l.ProductID).
					Update("num_of_sale", gorm.Expr("num_of_sale + ?", qty))

				detail := models.OrderDetail{
					OrderID:   order.ID,
					SellerID:  &sid,
					ProductID: *l.ProductID,
					Variation: l.Variation,
					Price:     l.Price,
					Quantity:  &qty,
				}
				if err := tx.Create(&detail).Error; err != nil {
					return fmt.Errorf("create order detail for product %d: %w", *l.ProductID, err)
				}
			}
		}

		// 3. Clear the cart only after all orders and details are committed.
		if err := tx.Where("user_id = ? AND status = 1", user.ID).Delete(&models.Cart{}).Error; err != nil {
			return fmt.Errorf("clear cart: %w", err)
		}

		return nil
	})

	if txErr != nil {
		errSess := sessions.Default(c)
		errSess.Set("checkout_error", txErr.Error())
		// If the failure was coupon-related, clear the stale coupon so the user
		// isn't stuck in a retry loop with an already-invalid code.
		errMsg := txErr.Error()
		if strings.Contains(errMsg, "coupon") || strings.Contains(errMsg, "stock") {
			errSess.Delete("coupon_code")
			errSess.Delete("coupon_id")
			errSess.Delete("coupon_discount")
		}
		_ = errSess.Save()
		c.Redirect(http.StatusFound, "/checkout?error=1")
		return
	}

	// Store last order ID in session; clear applied coupon.
	sess.Set("last_order_id", fmt.Sprintf("%d", combinedID))
	sess.Delete("coupon_code")
	sess.Delete("coupon_id")
	sess.Delete("coupon_discount")
	_ = sess.Save()

	switch paymentType {
	case "paypal", "stripe", "alipay":
		c.Redirect(http.StatusFound, fmt.Sprintf("/pay/%d", combinedID))
	default:
		c.Redirect(http.StatusFound, "/order-confirmed")
	}
}

// GET /order-confirmed  (requires auth — moved to auth group in main.go)
func (h *CartHandler) OrderConfirmed(c *gin.Context) {
	user := c.MustGet("user").(*models.User)

	sess := sessions.Default(c)
	orderIDStr, _ := sess.Get("last_order_id").(string)
	sess.Delete("last_order_id")
	_ = sess.Save()

	var combined *models.CombinedOrder
	if orderIDStr != "" {
		var co models.CombinedOrder
		// Scope by user_id to prevent order-confirmation peeking.
		if err := h.DB.Where("id = ? AND user_id = ?", orderIDStr, user.ID).
			Preload("Orders.OrderDetails.Product").First(&co).Error; err == nil {
			combined = &co
			for oi := range combined.Orders {
				dets := combined.Orders[oi].OrderDetails
				for di := range dets {
					d := &dets[di]
					if d.Variation == nil || *d.Variation == "" {
						continue
					}
					var stock models.ProductStock
					if err := h.DB.Select("variant_image").
						Where("product_id = ? AND variant = ?", d.ProductID, *d.Variation).
						First(&stock).Error; err == nil && stock.VariantImage != nil && *stock.VariantImage != "" {
						d.VariantImage = stock.VariantImage
					}
				}
			}
		}
	}

	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/order_confirmed", gin.H{
		"Categories": h.navCats(c),
		"User":       user,
		"Order":      combined,
	})
}

func strPtr(s string) *string { return &s }
