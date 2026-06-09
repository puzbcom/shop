package delivery

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/view"
)

type Handler struct {
	DB     *gorm.DB
	Engine *view.Engine
}

func currentUser(c *gin.Context) *models.User {
	return c.MustGet("user").(*models.User)
}

// orderQuery scopes orders assigned to this delivery boy (carrier_id = user.ID).
func (h *Handler) orderQuery(u *models.User) *gorm.DB {
	return h.DB.Model(&models.Order{}).Where("carrier_id = ?", u.ID)
}

// ── Dashboard ────────────────────────────────────────────────────────────────

func (h *Handler) Dashboard(c *gin.Context) {
	u := currentUser(c)

	var assigned, onTheWay, delivered, cancelled int64
	h.orderQuery(u).Where("delivery_status NOT IN (?)", []string{"delivered", "cancelled"}).Count(&assigned)
	h.orderQuery(u).Where("delivery_status = ?", "on the way").Count(&onTheWay)
	h.orderQuery(u).Where("delivery_status = ?", "delivered").Count(&delivered)
	h.orderQuery(u).Where("delivery_status = ?", "cancelled").Count(&cancelled)

	var recentOrders []models.Order
	h.orderQuery(u).Order("created_at desc").Limit(10).
		Preload("Buyer").
		Preload("OrderDetails.Product").
		Find(&recentOrders)

	h.Engine.Render(c, http.StatusOK, "delivery", "delivery/dashboard", gin.H{
		"User":         u,
		"Assigned":     assigned,
		"OnTheWay":     onTheWay,
		"Delivered":    delivered,
		"Cancelled":    cancelled,
		"RecentOrders": recentOrders,
	})
}

// ── Order list views ──────────────────────────────────────────────────────────

func (h *Handler) orderListPage(c *gin.Context, title, statusFilter string) {
	u := currentUser(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit := 15
	offset := (page - 1) * limit

	var orders []models.Order
	var total int64
	q := h.orderQuery(u)
	switch statusFilter {
	case "assigned":
		q = q.Where("delivery_status NOT IN (?)", []string{"delivered", "cancelled"})
	case "pickup":
		q = q.Where("delivery_status IN (?)", []string{"pending", "processing"})
	default:
		q = q.Where("delivery_status = ?", statusFilter)
	}
	q.Count(&total)
	q.Order("created_at desc").Limit(limit).Offset(offset).
		Preload("Buyer").
		Preload("OrderDetails.Product").
		Find(&orders)

	h.Engine.Render(c, http.StatusOK, "delivery", "delivery/order_list", gin.H{
		"User":         u,
		"PageTitle":    title,
		"Orders":       orders,
		"Total":        total,
		"Page":         page,
		"Pages":        (int(total) + limit - 1) / limit,
		"StatusFilter": statusFilter,
	})
}

func (h *Handler) AssignedOrders(c *gin.Context) {
	h.orderListPage(c, "Assigned Orders", "assigned")
}

func (h *Handler) PickupRequests(c *gin.Context) {
	h.orderListPage(c, "Pickup Requests", "pickup")
}

func (h *Handler) OnTheWay(c *gin.Context) {
	h.orderListPage(c, "On The Way", "on the way")
}

func (h *Handler) DeliveredOrders(c *gin.Context) {
	h.orderListPage(c, "Delivered", "delivered")
}

func (h *Handler) CancelledOrders(c *gin.Context) {
	h.orderListPage(c, "Cancelled", "cancelled")
}

// ── Update order delivery status ──────────────────────────────────────────────

var allowedDeliveryStatuses = map[string]bool{
	"pending":    true,
	"processing": true,
	"on the way": true,
	"delivered":  true,
	"cancelled":  true,
}

func (h *Handler) UpdateStatus(c *gin.Context) {
	u := currentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	status := c.PostForm("delivery_status")
	back := c.Request.Referer()
	if back == "" {
		back = "/delivery/orders"
	}
	if status == "" || !allowedDeliveryStatuses[status] {
		c.Redirect(http.StatusFound, back)
		return
	}
	h.orderQuery(u).Where("id = ?", id).
		Update("delivery_status", status)
	c.Redirect(http.StatusFound, back)
}

// ── Collections (COD amounts for delivered orders) ────────────────────────────

func (h *Handler) Collections(c *gin.Context) {
	u := currentUser(c)

	var orders []models.Order
	h.orderQuery(u).
		Where("delivery_status = ? AND payment_type = ?", "delivered", "cash_on_delivery").
		Order("created_at desc").
		Preload("Buyer").
		Find(&orders)

	var totalCollected float64
	for _, o := range orders {
		if o.GrandTotal != nil {
			totalCollected += *o.GrandTotal
		}
	}

	h.Engine.Render(c, http.StatusOK, "delivery", "delivery/collections", gin.H{
		"User":           u,
		"Orders":         orders,
		"TotalCollected": totalCollected,
	})
}

// ── Earnings ──────────────────────────────────────────────────────────────────

func (h *Handler) Earnings(c *gin.Context) {
	u := currentUser(c)

	var totalEarnings float64
	h.DB.Model(&models.Wallet{}).
		Select("COALESCE(SUM(amount), 0)").
		Where("user_id = ? AND added_by = ?", u.ID, "delivery_boy").
		Scan(&totalEarnings)

	var transactions []models.Wallet
	h.DB.Where("user_id = ? AND added_by = ?", u.ID, "delivery_boy").
		Order("created_at desc").
		Find(&transactions)

	h.Engine.Render(c, http.StatusOK, "delivery", "delivery/earnings", gin.H{
		"User":          u,
		"TotalEarnings": totalEarnings,
		"Transactions":  transactions,
	})
}

// ── Profile ───────────────────────────────────────────────────────────────────

func (h *Handler) Profile(c *gin.Context) {
	u := currentUser(c)
	success := c.Query("success") == "1"
	errMsg := c.Query("error")
	h.Engine.Render(c, http.StatusOK, "delivery", "delivery/profile", gin.H{
		"User":    u,
		"Success": success,
		"Error":   errMsg,
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
			c.Redirect(http.StatusFound, "/delivery/profile?error=wrong_password")
			return
		}
		if len(newPwd) < 8 {
			c.Redirect(http.StatusFound, "/delivery/profile?error=password_too_short")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
		if err != nil {
			c.Redirect(http.StatusFound, "/delivery/profile?error=server_error")
			return
		}
		updates["password"] = string(hash)
	}
	if len(updates) > 0 {
		h.DB.Model(u).Updates(updates)
	}
	c.Redirect(http.StatusFound, "/delivery/profile?success=1")
}
