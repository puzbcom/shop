// Package webhook receives server-to-server notifications from external
// payment gateways (PayPal, etc.) and updates order state.
package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/services/settings"
)

type Handler struct {
	DB       *gorm.DB
	Settings *settings.Store
}

var paypalHTTP = &http.Client{Timeout: 20 * time.Second}

func (h *Handler) paypalAPIBase() string {
	if strings.ToLower(h.Settings.Get("paypal_mode", "sandbox")) == "live" {
		return "https://api-m.paypal.com"
	}
	return "https://api-m.sandbox.paypal.com"
}

// PayPal handles POST /webhooks/paypal. PayPal posts event notifications here;
// we verify the signature against the configured webhook ID, then mark the
// referenced order paid on PAYMENT.CAPTURE.COMPLETED.
//
// For matching to work, the client-side checkout flow that creates the PayPal
// order MUST set purchase_units[].custom_id to our Order.ID (as a string).
func (h *Handler) PayPal(c *gin.Context) {
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	clientID := h.Settings.Get("paypal_client_id", "")
	secret := h.Settings.Get("paypal_secret", "")
	webhookID := h.Settings.Get("paypal_webhook_id", "")
	if clientID == "" || secret == "" || webhookID == "" {
		log.Printf("paypal webhook: missing config (client_id/secret/webhook_id)")
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	token, err := h.paypalAccessToken(clientID, secret)
	if err != nil {
		log.Printf("paypal webhook: oauth: %v", err)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}

	ok, err := h.verifyPayPalSignature(token, webhookID, c.Request.Header, rawBody)
	if err != nil {
		log.Printf("paypal webhook: verify: %v", err)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	if !ok {
		log.Printf("paypal webhook: signature verification FAILED")
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var event struct {
		ID         string          `json:"id"`
		EventType  string          `json:"event_type"`
		ResourceJS json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(rawBody, &event); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	switch event.EventType {
	case "PAYMENT.CAPTURE.COMPLETED":
		h.handleCaptureCompleted(event.ID, event.ResourceJS)
	case "PAYMENT.CAPTURE.DENIED", "PAYMENT.CAPTURE.REFUNDED", "PAYMENT.CAPTURE.REVERSED":
		h.handleCaptureNegative(event.EventType, event.ID, event.ResourceJS)
	default:
		// Acknowledge unhandled events so PayPal stops retrying.
	}

	c.Status(http.StatusOK)
}

// paypalAccessToken obtains a short-lived OAuth token via client_credentials.
func (h *Handler) paypalAccessToken(clientID, secret string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, _ := http.NewRequest(http.MethodPost, h.paypalAPIBase()+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	req.SetBasicAuth(clientID, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := paypalHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("oauth status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("empty access_token")
	}
	return out.AccessToken, nil
}

// verifyPayPalSignature posts to PayPal's verify-webhook-signature endpoint
// using the transmission headers from the incoming request and the raw body
// as the webhook_event.
func (h *Handler) verifyPayPalSignature(token, webhookID string, headers http.Header, rawBody []byte) (bool, error) {
	var eventObj json.RawMessage = rawBody
	payload := map[string]interface{}{
		"auth_algo":         headers.Get("Paypal-Auth-Algo"),
		"cert_url":          headers.Get("Paypal-Cert-Url"),
		"transmission_id":   headers.Get("Paypal-Transmission-Id"),
		"transmission_sig":  headers.Get("Paypal-Transmission-Sig"),
		"transmission_time": headers.Get("Paypal-Transmission-Time"),
		"webhook_id":        webhookID,
		"webhook_event":     eventObj,
	}
	for k, v := range payload {
		if s, ok := v.(string); ok && s == "" {
			return false, fmt.Errorf("missing header for %s", k)
		}
	}
	buf, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, h.paypalAPIBase()+"/v1/notifications/verify-webhook-signature", bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := paypalHTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("verify status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, err
	}
	return out.VerificationStatus == "SUCCESS", nil
}

// PayPal capture resource (only the fields we need).
type paypalCapture struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	CustomID   string `json:"custom_id"`
	InvoiceID  string `json:"invoice_id"`
	Amount     struct {
		Value        string `json:"value"`
		CurrencyCode string `json:"currency_code"`
	} `json:"amount"`
	CreateTime string `json:"create_time"`
	UpdateTime string `json:"update_time"`
}

func (h *Handler) handleCaptureCompleted(eventID string, resourceJS json.RawMessage) {
	var res paypalCapture
	if err := json.Unmarshal(resourceJS, &res); err != nil {
		log.Printf("paypal webhook: bad resource: %v", err)
		return
	}
	orders, err := h.resolveOrders(res.CustomID, res.InvoiceID)
	if err != nil {
		log.Printf("paypal webhook: %v", err)
		return
	}
	details := map[string]interface{}{
		"gateway":     "paypal",
		"event_id":    eventID,
		"capture_id":  res.ID,
		"status":      res.Status,
		"amount":      res.Amount.Value,
		"currency":    res.Amount.CurrencyCode,
		"custom_id":   res.CustomID,
		"invoice_id":  res.InvoiceID,
		"update_time": res.UpdateTime,
	}
	detailsJSON, _ := json.Marshal(details)
	if err := h.markOrdersPaid(orders, "paypal", string(detailsJSON)); err != nil {
		log.Printf("paypal webhook: mark paid: %v", err)
		return
	}
	log.Printf("paypal webhook: %d order(s) marked paid (capture %s)", len(orders), res.ID)
}

func (h *Handler) handleCaptureNegative(eventType, eventID string, resourceJS json.RawMessage) {
	var res paypalCapture
	if err := json.Unmarshal(resourceJS, &res); err != nil {
		return
	}
	orders, err := h.resolveOrders(res.CustomID, res.InvoiceID)
	if err != nil {
		log.Printf("paypal webhook: %s: %v", eventType, err)
		return
	}
	newStatus := "unpaid"
	if eventType == "PAYMENT.CAPTURE.REFUNDED" || eventType == "PAYMENT.CAPTURE.REVERSED" {
		newStatus = "refunded"
	}
	h.markOrdersStatus(orders, newStatus)
	log.Printf("paypal webhook: %d order(s) marked %s (%s)", len(orders), newStatus, eventType)
}

