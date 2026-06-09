package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Stripe handles POST /webhooks/stripe. Verifies the Stripe-Signature header
// against the configured stripe_webhook_secret, then marks the referenced
// order paid on payment_intent.succeeded / checkout.session.completed.
//
// For matching, the future checkout flow must set the local Order.ID on the
// PaymentIntent (metadata.order_id) or the Checkout Session (client_reference_id).
func (h *Handler) Stripe(c *gin.Context) {
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	secret := h.Settings.Get("stripe_webhook_secret", "")
	if secret == "" {
		log.Printf("stripe webhook: missing stripe_webhook_secret")
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	sigHeader := c.GetHeader("Stripe-Signature")
	if !verifyStripeSignature(sigHeader, rawBody, secret) {
		log.Printf("stripe webhook: signature verification FAILED")
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var event struct {
		ID         string          `json:"id"`
		Type       string          `json:"type"`
		DataObject json.RawMessage `json:"-"`
		Data       struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &event); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	switch event.Type {
	case "payment_intent.succeeded", "checkout.session.completed", "charge.succeeded":
		h.handleStripeSuccess(event.ID, event.Type, event.Data.Object)
	case "charge.refunded", "payment_intent.payment_failed":
		h.handleStripeNegative(event.ID, event.Type, event.Data.Object)
	default:
		// Acknowledge so Stripe stops retrying.
	}

	c.Status(http.StatusOK)
}

// verifyStripeSignature implements Stripe's signing scheme:
// header = "t=<ts>,v1=<hex-hmac-sha256(ts + '.' + body, secret)>"
// Tolerance: 5 minutes.
func verifyStripeSignature(header string, body []byte, secret string) bool {
	if header == "" {
		return false
	}
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	if ts == "" || len(sigs) == 0 {
		return false
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if abs64(time.Now().Unix()-tsInt) > 300 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(expected)) {
			return true
		}
	}
	return false
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (h *Handler) handleStripeSuccess(eventID, eventType string, obj json.RawMessage) {
	var p struct {
		ID                string            `json:"id"`
		Object            string            `json:"object"`
		ClientReferenceID string            `json:"client_reference_id"`
		Amount            int64             `json:"amount"`
		AmountTotal       int64             `json:"amount_total"`
		Currency          string            `json:"currency"`
		Status            string            `json:"status"`
		PaymentIntent     string            `json:"payment_intent"`
		Metadata          map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(obj, &p); err != nil {
		log.Printf("stripe webhook: bad object: %v", err)
		return
	}

	ref := p.ClientReferenceID
	if ref == "" && p.Metadata != nil {
		ref = p.Metadata["order_id"]
	}
	orders, err := h.resolveOrders(ref)
	if err != nil {
		log.Printf("stripe webhook: %v (event=%s)", err, eventType)
		return
	}
	amount := p.AmountTotal
	if amount == 0 {
		amount = p.Amount
	}
	details := map[string]interface{}{
		"gateway":        "stripe",
		"event_id":       eventID,
		"event_type":     eventType,
		"object_id":      p.ID,
		"object_type":    p.Object,
		"payment_intent": p.PaymentIntent,
		"amount_minor":   amount,
		"currency":       p.Currency,
		"status":         p.Status,
	}
	detailsJSON, _ := json.Marshal(details)
	if err := h.markOrdersPaid(orders, "stripe", string(detailsJSON)); err != nil {
		log.Printf("stripe webhook: mark paid: %v", err)
		return
	}
	log.Printf("stripe webhook: %d order(s) marked paid (%s %s)", len(orders), eventType, p.ID)
}

func (h *Handler) handleStripeNegative(eventID, eventType string, obj json.RawMessage) {
	var p struct {
		ID                string            `json:"id"`
		ClientReferenceID string            `json:"client_reference_id"`
		Metadata          map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(obj, &p); err != nil {
		return
	}
	ref := p.ClientReferenceID
	if ref == "" && p.Metadata != nil {
		ref = p.Metadata["order_id"]
	}
	orders, err := h.resolveOrders(ref)
	if err != nil {
		log.Printf("stripe webhook: %s: %v", eventType, err)
		return
	}
	newStatus := "unpaid"
	if eventType == "charge.refunded" {
		newStatus = "refunded"
	}
	h.markOrdersStatus(orders, newStatus)
	log.Printf("stripe webhook: %d order(s) marked %s (%s)", len(orders), newStatus, eventType)
}
