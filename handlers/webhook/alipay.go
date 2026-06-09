package webhook

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// Alipay handles POST /webhooks/alipay. Alipay sends form-encoded async
// notifications; we verify the RSA2 signature against the configured Alipay
// public key, then mark the referenced order paid on TRADE_SUCCESS/FINISHED.
//
// Per Alipay protocol, the response body MUST be the literal string "success"
// or "fail" — Alipay ignores HTTP status codes and retries until it sees
// "success".
func (h *Handler) Alipay(c *gin.Context) {
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.String(http.StatusOK, "fail")
		return
	}
	form, err := url.ParseQuery(string(rawBody))
	if err != nil {
		c.String(http.StatusOK, "fail")
		return
	}

	pubKey := h.Settings.Get("alipay_public_key", "")
	appID := h.Settings.Get("alipay_app_id", "")
	if pubKey == "" {
		log.Printf("alipay webhook: missing alipay_public_key")
		c.String(http.StatusOK, "fail")
		return
	}

	// app_id guard: ensure the notification belongs to our merchant app.
	if appID != "" && form.Get("app_id") != appID {
		log.Printf("alipay webhook: app_id mismatch (got %q want %q)", form.Get("app_id"), appID)
		c.String(http.StatusOK, "fail")
		return
	}

	if err := verifyAlipaySignature(form, pubKey); err != nil {
		log.Printf("alipay webhook: signature verification FAILED: %v", err)
		c.String(http.StatusOK, "fail")
		return
	}

	switch form.Get("trade_status") {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		h.handleAlipaySuccess(form)
	case "TRADE_CLOSED":
		h.handleAlipayClosed(form)
	default:
		// other intermediate states — acknowledge without action
	}

	c.String(http.StatusOK, "success")
}

// verifyAlipaySignature implements Alipay's RSA2 verification:
//   1. Take all params except "sign" and "sign_type".
//   2. Sort keys ascending; join as "k=v&k=v" (raw, not URL-encoded).
//   3. RSA-SHA256 verify against base64-decoded "sign" using Alipay's public key.
func verifyAlipaySignature(form url.Values, pubKeyB64 string) error {
	sig := form.Get("sign")
	if sig == "" {
		return errors.New("missing sign")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("bad sign b64: %w", err)
	}

	keys := make([]string, 0, len(form))
	for k := range form {
		if k == "sign" || k == "sign_type" {
			continue
		}
		// Alipay drops empty values from the signed string.
		if form.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+form.Get(k))
	}
	signedStr := strings.Join(parts, "&")

	pub, err := parseRSAPublicKey(pubKeyB64)
	if err != nil {
		return fmt.Errorf("parse public key: %w", err)
	}
	sum := sha256.Sum256([]byte(signedStr))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sigBytes)
}

// parseRSAPublicKey accepts a base64 DER-encoded RSA public key (no PEM
// header/footer), tries PKIX first then PKCS#1.
func parseRSAPublicKey(b64 string) (*rsa.PublicKey, error) {
	b64 = strings.Join(strings.Fields(b64), "") // strip whitespace
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	if pub, err := x509.ParsePKIXPublicKey(der); err == nil {
		if k, ok := pub.(*rsa.PublicKey); ok {
			return k, nil
		}
		return nil, errors.New("not an RSA public key")
	}
	if k, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("could not parse as PKIX or PKCS#1")
}

func (h *Handler) handleAlipaySuccess(form url.Values) {
	outTradeNo := form.Get("out_trade_no")
	orders, err := h.resolveOrders(outTradeNo)
	if err != nil {
		log.Printf("alipay webhook: %v", err)
		return
	}
	details := map[string]interface{}{
		"gateway":        "alipay",
		"trade_no":       form.Get("trade_no"),
		"out_trade_no":   outTradeNo,
		"trade_status":   form.Get("trade_status"),
		"total_amount":   form.Get("total_amount"),
		"receipt_amount": form.Get("receipt_amount"),
		"buyer_logon_id": form.Get("buyer_logon_id"),
		"gmt_payment":    form.Get("gmt_payment"),
		"notify_time":    form.Get("notify_time"),
	}
	detailsJSON, _ := json.Marshal(details)
	if err := h.markOrdersPaid(orders, "alipay", string(detailsJSON)); err != nil {
		log.Printf("alipay webhook: mark paid: %v", err)
		return
	}
	log.Printf("alipay webhook: %d order(s) marked paid (trade_no=%s)", len(orders), form.Get("trade_no"))
}

func (h *Handler) handleAlipayClosed(form url.Values) {
	orders, err := h.resolveOrders(form.Get("out_trade_no"))
	if err != nil {
		return
	}
	h.markOrdersStatus(orders, "unpaid")
	log.Printf("alipay webhook: %d order(s) trade closed", len(orders))
}
