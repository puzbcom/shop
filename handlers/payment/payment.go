// Package payment drives the buyer-facing payment flow for external gateways
// (PayPal, Stripe, Alipay) after an order is placed. The cart's PlaceOrder
// redirects here when payment_type is one of those gateways.
package payment

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/services/settings"
	"mall/internal/view"
)

type Handler struct {
	DB       *gorm.DB
	Engine   *view.Engine
	Settings *settings.Store
}

var httpClient = &http.Client{Timeout: 25 * time.Second}

// loadCombined loads a CombinedOrder scoped to the current user and ensures it
// belongs to them. Returns nil + writes a redirect on failure.
func (h *Handler) loadCombined(c *gin.Context) *models.CombinedOrder {
	user := c.MustGet("user").(*models.User)
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		c.Redirect(http.StatusFound, "/dashboard")
		return nil
	}
	var co models.CombinedOrder
	if err := h.DB.Preload("Orders").Where("id = ? AND user_id = ?", id, user.ID).First(&co).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard")
		return nil
	}
	return &co
}

func (h *Handler) anyOrderPaid(co *models.CombinedOrder) bool {
	for _, o := range co.Orders {
		if o.PaymentStatus != nil && *o.PaymentStatus == "paid" {
			return true
		}
	}
	return false
}

// Show renders /pay/:id, choosing a template based on the order's payment_type.
func (h *Handler) Show(c *gin.Context) {
	co := h.loadCombined(c)
	if co == nil {
		return
	}
	if h.anyOrderPaid(co) {
		c.Redirect(http.StatusFound, "/purchase_history")
		return
	}
	gateway := ""
	if len(co.Orders) > 0 && co.Orders[0].PaymentType != nil {
		gateway = *co.Orders[0].PaymentType
	}

	user := c.MustGet("user").(*models.User)
	data := gin.H{
		"User":       user,
		"Order":      co,
		"Gateway":    gateway,
		"GrandTotal": co.GrandTotal,
	}
	switch gateway {
	case "paypal":
		data["ClientID"] = h.Settings.Get("paypal_client_id", "")
		data["Currency"] = strings.ToUpper(h.Settings.Get("default_currency", "USD"))
	case "stripe":
		data["PublishableKey"] = h.Settings.Get("stripe_publishable_key", "")
	case "alipay":
		// nothing template-side; user clicks a button to redirect.
	default:
		c.Redirect(http.StatusFound, "/order-confirmed")
		return
	}
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/pay", data)
}

// =====================================================================
// PayPal
// =====================================================================

func (h *Handler) paypalBase() string {
	if strings.ToLower(h.Settings.Get("paypal_mode", "sandbox")) == "live" {
		return "https://api-m.paypal.com"
	}
	return "https://api-m.sandbox.paypal.com"
}

func (h *Handler) paypalToken() (string, error) {
	clientID := h.Settings.Get("paypal_client_id", "")
	secret := h.Settings.Get("paypal_secret", "")
	if clientID == "" || secret == "" {
		return "", errors.New("paypal credentials not configured")
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, _ := http.NewRequest(http.MethodPost, h.paypalBase()+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	req.SetBasicAuth(clientID, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("paypal oauth: %d %s", resp.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	return out.AccessToken, nil
}

// POST /pay/:id/paypal/create — invoked by the PayPal Buttons JS to create the
// PayPal order. Returns { id } which the JS then hands back to PayPal.
func (h *Handler) PayPalCreate(c *gin.Context) {
	co := h.loadCombined(c)
	if co == nil {
		return
	}
	if h.anyOrderPaid(co) {
		c.JSON(http.StatusConflict, gin.H{"error": "already paid"})
		return
	}
	token, err := h.paypalToken()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	currency := strings.ToUpper(h.Settings.Get("default_currency", "USD"))
	combinedRef := fmt.Sprintf("%d", co.ID)
	payload := map[string]interface{}{
		"intent": "CAPTURE",
		"purchase_units": []map[string]interface{}{{
			"reference_id": combinedRef,
			"custom_id":    combinedRef,
			"invoice_id":   combinedRef,
			"amount": map[string]interface{}{
				"currency_code": currency,
				"value":         fmt.Sprintf("%.2f", co.GrandTotal),
			},
		}},
	}
	buf, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, h.paypalBase()+"/v2/checkout/orders", bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("paypal create: %d %s", resp.StatusCode, body)})
		return
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": out.ID})
}

// POST /pay/:id/paypal/capture?paypal_order_id=... — capture an approved order.
func (h *Handler) PayPalCapture(c *gin.Context) {
	co := h.loadCombined(c)
	if co == nil {
		return
	}
	if h.anyOrderPaid(co) {
		c.JSON(http.StatusOK, gin.H{"status": "already_paid"})
		return
	}
	paypalOrderID := strings.TrimSpace(c.Query("paypal_order_id"))
	if paypalOrderID == "" {
		paypalOrderID = strings.TrimSpace(c.PostForm("paypal_order_id"))
	}
	if paypalOrderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing paypal_order_id"})
		return
	}
	token, err := h.paypalToken()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	req, _ := http.NewRequest(http.MethodPost, h.paypalBase()+"/v2/checkout/orders/"+url.PathEscape(paypalOrderID)+"/capture", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("paypal capture: %d %s", resp.StatusCode, body)})
		return
	}
	// Optional sanity check: ensure status COMPLETED.
	var captureResp struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &captureResp)
	if captureResp.Status != "" && captureResp.Status != "COMPLETED" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "paypal capture status: " + captureResp.Status})
		return
	}
	details, _ := json.Marshal(map[string]interface{}{
		"gateway":         "paypal",
		"paypal_order_id": paypalOrderID,
		"capture_raw":     json.RawMessage(body),
		"captured_at":     time.Now().UTC().Format(time.RFC3339),
	})
	if err := h.markPaid(co, "paypal", string(details)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// =====================================================================
// Stripe
// =====================================================================

// GET /pay/:id/stripe/start — create a Stripe Checkout Session and 303 to it.
func (h *Handler) StripeStart(c *gin.Context) {
	co := h.loadCombined(c)
	if co == nil {
		return
	}
	if h.anyOrderPaid(co) {
		c.Redirect(http.StatusFound, "/purchase_history")
		return
	}
	secret := h.Settings.Get("stripe_secret_key", "")
	if secret == "" {
		c.String(http.StatusServiceUnavailable, "Stripe not configured")
		return
	}
	currency := strings.ToLower(h.Settings.Get("default_currency", "USD"))
	combinedRef := fmt.Sprintf("%d", co.ID)
	amountMinor := int64(co.GrandTotal * 100)
	scheme := "https"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	origin := scheme + "://" + c.Request.Host
	successURL := origin + "/pay/" + combinedRef + "/stripe/return?session_id={CHECKOUT_SESSION_ID}"
	cancelURL := origin + "/pay/" + combinedRef

	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	form.Set("client_reference_id", combinedRef)
	form.Set("payment_intent_data[metadata][order_id]", combinedRef)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", currency)
	form.Set("line_items[0][price_data][product_data][name]", "Order #"+combinedRef)
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(amountMinor, 10))

	req, _ := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, "Stripe error: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		c.String(http.StatusBadGateway, "Stripe error: %d %s", resp.StatusCode, body)
		return
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.URL == "" {
		c.String(http.StatusBadGateway, "Stripe: malformed response")
		return
	}
	c.Redirect(http.StatusSeeOther, out.URL)
}

// GET /pay/:id/stripe/return — landing after Stripe Checkout. Marks paid
// immediately by retrieving the session; webhook is the authoritative backup.
func (h *Handler) StripeReturn(c *gin.Context) {
	co := h.loadCombined(c)
	if co == nil {
		return
	}
	if h.anyOrderPaid(co) {
		c.Redirect(http.StatusFound, "/order-confirmed")
		return
	}
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if sessionID == "" {
		c.Redirect(http.StatusFound, "/pay/"+fmt.Sprintf("%d", co.ID))
		return
	}
	secret := h.Settings.Get("stripe_secret_key", "")
	if secret == "" {
		c.String(http.StatusServiceUnavailable, "Stripe not configured")
		return
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.stripe.com/v1/checkout/sessions/"+url.PathEscape(sessionID), nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := httpClient.Do(req)
	if err != nil {
		c.Redirect(http.StatusFound, "/pay/"+fmt.Sprintf("%d", co.ID))
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		c.Redirect(http.StatusFound, "/pay/"+fmt.Sprintf("%d", co.ID))
		return
	}
	var sess struct {
		ID                string `json:"id"`
		PaymentStatus     string `json:"payment_status"`
		ClientReferenceID string `json:"client_reference_id"`
		AmountTotal       int64  `json:"amount_total"`
		Currency          string `json:"currency"`
	}
	if err := json.Unmarshal(body, &sess); err != nil {
		c.Redirect(http.StatusFound, "/pay/"+fmt.Sprintf("%d", co.ID))
		return
	}
	if sess.PaymentStatus != "paid" || sess.ClientReferenceID != fmt.Sprintf("%d", co.ID) {
		c.Redirect(http.StatusFound, "/pay/"+fmt.Sprintf("%d", co.ID))
		return
	}
	details, _ := json.Marshal(map[string]interface{}{
		"gateway":      "stripe",
		"session_id":   sess.ID,
		"amount_minor": sess.AmountTotal,
		"currency":     sess.Currency,
		"verified_at":  time.Now().UTC().Format(time.RFC3339),
	})
	_ = h.markPaid(co, "stripe", string(details))
	c.Redirect(http.StatusFound, "/order-confirmed")
}

// =====================================================================
// Alipay
// =====================================================================

const (
	alipayGatewayLive    = "https://openapi.alipay.com/gateway.do"
	alipayGatewaySandbox = "https://openapi-sandbox.dl.alipaydev.com/gateway.do"
)

func (h *Handler) alipayGateway() string {
	if strings.ToLower(h.Settings.Get("alipay_mode", "sandbox")) == "live" {
		return alipayGatewayLive
	}
	return alipayGatewaySandbox
}

// GET /pay/:id/alipay/start — redirect to the Alipay-hosted PC payment page.
func (h *Handler) AlipayStart(c *gin.Context) {
	co := h.loadCombined(c)
	if co == nil {
		return
	}
	if h.anyOrderPaid(co) {
		c.Redirect(http.StatusFound, "/order-confirmed")
		return
	}
	appID := h.Settings.Get("alipay_app_id", "")
	privKeyB64 := h.Settings.Get("alipay_private_key", "")
	if appID == "" || privKeyB64 == "" {
		c.String(http.StatusServiceUnavailable, "Alipay not configured")
		return
	}
	scheme := "https"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	origin := scheme + "://" + c.Request.Host
	combinedRef := fmt.Sprintf("%d", co.ID)
	notifyURL := h.Settings.Get("alipay_notify_url", origin+"/webhooks/alipay")
	returnURL := origin + "/pay/" + combinedRef + "/alipay/return"

	biz, _ := json.Marshal(map[string]interface{}{
		"out_trade_no": combinedRef,
		"total_amount": fmt.Sprintf("%.2f", co.GrandTotal),
		"subject":      "Order #" + combinedRef,
		"product_code": "FAST_INSTANT_TRADE_PAY",
	})

	params := map[string]string{
		"app_id":      appID,
		"method":      "alipay.trade.page.pay",
		"format":      "JSON",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"notify_url":  notifyURL,
		"return_url":  returnURL,
		"biz_content": string(biz),
	}
	sig, err := alipaySign(params, privKeyB64)
	if err != nil {
		c.String(http.StatusInternalServerError, "Alipay sign error: %v", err)
		return
	}
	params["sign"] = sig

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	c.Redirect(http.StatusFound, h.alipayGateway()+"?"+q.Encode())
}

// GET /pay/:id/alipay/return — landing after Alipay payment. The real payment
// confirmation comes from the async notify webhook; here we just verify the
// return-URL signature, then redirect to order-confirmed.
func (h *Handler) AlipayReturn(c *gin.Context) {
	co := h.loadCombined(c)
	if co == nil {
		return
	}
	// Verify the return-URL signature (Alipay signs query params too).
	pubKeyB64 := h.Settings.Get("alipay_public_key", "")
	if pubKeyB64 != "" {
		form := url.Values{}
		for k, v := range c.Request.URL.Query() {
			if len(v) > 0 {
				form.Set(k, v[0])
			}
		}
		// alipayVerifyForm reuses the same scheme as the webhook receiver.
		if err := alipayVerifyForm(form, pubKeyB64); err != nil {
			c.String(http.StatusUnauthorized, "Alipay return: bad signature")
			return
		}
	}
	c.Redirect(http.StatusFound, "/order-confirmed")
}

// =====================================================================
// Shared helpers
// =====================================================================

// markPaid sets all orders under a combined order to paid.
func (h *Handler) markPaid(co *models.CombinedOrder, gateway, details string) error {
	return h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Order{}).Where("combined_order_id = ?", co.ID).Updates(map[string]interface{}{
			"payment_status":  "paid",
			"payment_type":    gateway,
			"payment_details": details,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.OrderDetail{}).
			Where("order_id IN (?)", tx.Model(&models.Order{}).Select("id").Where("combined_order_id = ?", co.ID)).
			Update("payment_status", "paid").Error; err != nil {
			return err
		}
		// Promote any linked auction winner from "pending_payment" to "paid".
		return tx.Model(&models.AuctionWinner{}).
			Where("order_id IN (?) AND status = ?",
				tx.Model(&models.Order{}).Select("id").Where("combined_order_id = ?", co.ID),
				"pending_payment").
			Update("status", "paid").Error
	})
}

// alipaySign builds the canonical "k=v&k=v" string (excluding sign / sign_type,
// dropping empty values, sorted ascending) and signs it with RSA2 (SHA256).
func alipaySign(params map[string]string, privKeyB64 string) (string, error) {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	signedStr := strings.Join(parts, "&")

	priv, err := parseRSAPrivateKey(privKeyB64)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(signedStr))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

func alipayVerifyForm(form url.Values, pubKeyB64 string) error {
	sig := form.Get("sign")
	if sig == "" {
		return errors.New("missing sign")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(form))
	for k := range form {
		if k == "sign" || k == "sign_type" {
			continue
		}
		if form.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+form.Get(k))
	}
	signed := strings.Join(parts, "&")

	pub, err := parseRSAPublicKey(pubKeyB64)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(signed))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sigBytes)
}

func parseRSAPrivateKey(b64 string) (*rsa.PrivateKey, error) {
	b64 = strings.Join(strings.Fields(b64), "")
	// Allow PEM input too.
	if strings.Contains(b64, "BEGIN") {
		block, _ := pem.Decode([]byte(b64))
		if block == nil {
			return nil, errors.New("bad pem")
		}
		if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return k, nil
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("not RSA")
		}
		return rk, nil
	}
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not RSA")
	}
	return rk, nil
}

func parseRSAPublicKey(b64 string) (*rsa.PublicKey, error) {
	b64 = strings.Join(strings.Fields(b64), "")
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	if pub, err := x509.ParsePKIXPublicKey(der); err == nil {
		if k, ok := pub.(*rsa.PublicKey); ok {
			return k, nil
		}
	}
	if k, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("could not parse public key")
}

// Compile-time guard against accidental hex import removal in stripped trees.
var _ = hex.EncodeToString
