package webhook

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"mall/internal/models"
)

// resolveOrders interprets a merchant reference string (set by us when
// creating the gateway transaction) as a CombinedOrder.ID and returns all
// child Orders belonging to it. As a fallback (legacy data, or single-Order
// references), it tries Order.ID directly.
func (h *Handler) resolveOrders(refs ...string) ([]models.Order, error) {
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		ref = strings.TrimPrefix(ref, "c") // tolerate "c123" combined-id prefix
		if ref == "" {
			continue
		}
		id, err := strconv.ParseUint(ref, 10, 64)
		if err != nil || id == 0 {
			continue
		}
		var orders []models.Order
		if err := h.DB.Where("combined_order_id = ?", id).Find(&orders).Error; err == nil && len(orders) > 0 {
			return orders, nil
		}
		var single models.Order
		if err := h.DB.First(&single, id).Error; err == nil {
			return []models.Order{single}, nil
		}
	}
	return nil, fmt.Errorf("no order matched refs=%v", refs)
}

// markOrdersPaid sets payment_status=paid, payment_type, and payment_details
// for every order in the slice and their order_details.
func (h *Handler) markOrdersPaid(orders []models.Order, paymentType, details string) error {
	if len(orders) == 0 {
		return nil
	}
	ids := make([]uint, len(orders))
	for i, o := range orders {
		ids[i] = o.ID
	}
	return h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Order{}).Where("id IN ?", ids).Updates(map[string]interface{}{
			"payment_status":  "paid",
			"payment_type":    paymentType,
			"payment_details": details,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.OrderDetail{}).Where("order_id IN ?", ids).Update("payment_status", "paid").Error; err != nil {
			return err
		}
		// If any of these orders are linked to an AuctionWinner row (auction
		// checkout via a non-wallet gateway), flip the winner status from
		// "pending_payment" to "paid" so the win is no longer treated as
		// awaiting payment.
		return tx.Model(&models.AuctionWinner{}).
			Where("order_id IN ? AND status = ?", ids, "pending_payment").
			Update("status", "paid").Error
	})
}

// markOrdersStatus updates payment_status only (used for refunds/closures).
func (h *Handler) markOrdersStatus(orders []models.Order, status string) {
	if len(orders) == 0 {
		return
	}
	ids := make([]uint, len(orders))
	for i, o := range orders {
		ids[i] = o.ID
	}
	if err := h.DB.Model(&models.Order{}).Where("id IN ?", ids).Update("payment_status", status).Error; err != nil {
		log.Printf("webhook: update orders %v -> %s: %v", ids, status, err)
	}
	h.DB.Model(&models.OrderDetail{}).Where("order_id IN ?", ids).Update("payment_status", status)
}
