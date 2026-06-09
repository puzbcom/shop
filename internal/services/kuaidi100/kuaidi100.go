// Package kuaidi100 queries Chinese logistics tracking via the kuaidi100
// real-time polling API (https://poll.kuaidi100.com/poll/query.do).
//
// Sign formula: MD5(param_json + key + customer).ToUpperCase()
// where param_json = {"com":"<carrier_code>","num":"<tracking_number>"}
package kuaidi100

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const pollURL = "https://poll.kuaidi100.com/poll/query.do"

var httpClient = &http.Client{Timeout: 12 * time.Second}

// TrackEvent is a single logistics event returned by kuaidi100.
type TrackEvent struct {
	Context  string `json:"context"`
	Time     string `json:"time"`
	FTime    string `json:"ftime"`
	Status   string `json:"status"`
	AreaName string `json:"areaName"`
}

// Response is the kuaidi100 API response payload.
type Response struct {
	Message string       `json:"message"`
	Status  string       `json:"status"` // "200" = ok
	State   string       `json:"state"`  // see StateLabel
	Nu      string       `json:"nu"`     // tracking number echoed
	Com     string       `json:"com"`    // carrier code echoed
	Data    []TrackEvent `json:"data"`   // events, newest first
}

// Query fetches real-time tracking for a package.
//   key      – kuaidi100 API secret key  (e.g. "jfQlTvuH8914")
//   customer – kuaidi100 customer code   (defaults to key if empty)
//   company  – carrier code              (e.g. "shunfeng", "yuantong", "zhongtong")
//   number   – tracking number
func Query(key, customer, company, number string) (*Response, error) {
	if customer == "" {
		customer = key
	}
	if company == "" || number == "" {
		return nil, fmt.Errorf("kuaidi100: company and number are required")
	}

	paramObj := map[string]string{"com": company, "num": number}
	paramJSON, err := json.Marshal(paramObj)
	if err != nil {
		return nil, fmt.Errorf("kuaidi100: marshal param: %w", err)
	}
	paramStr := string(paramJSON)

	// Sign = MD5(param + key + customer).toUpperCase()
	h := md5.Sum([]byte(paramStr + key + customer))
	sign := strings.ToUpper(fmt.Sprintf("%x", h))

	form := url.Values{}
	form.Set("customer", customer)
	form.Set("sign", sign)
	form.Set("param", paramStr)

	resp, err := httpClient.Post(pollURL, "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("kuaidi100: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kuaidi100: HTTP %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("kuaidi100: decode: %w", err)
	}

	if result.Status != "200" {
		return nil, fmt.Errorf("kuaidi100: API error – status=%s message=%s",
			result.Status, result.Message)
	}

	return &result, nil
}

// StateLabel converts a kuaidi100 state code to a human-readable English label.
//
//	0 = In Transit
//	1 = Delivered
//	2 = Exception
//	3 = Failed Delivery
//	4 = Out for Delivery
//	5 = Expired / Overdue
func StateLabel(state string) string {
	labels := map[string]string{
		"0": "In Transit",
		"1": "Delivered",
		"2": "Exception",
		"3": "Failed Delivery",
		"4": "Out for Delivery",
		"5": "Expired",
	}
	if l, ok := labels[state]; ok {
		return l
	}
	return "Unknown"
}

// StatusFromState maps a kuaidi100 state code to a SupplierOrder.Status value.
func StatusFromState(state string) string {
	switch state {
	case "1":
		return "delivered"
	case "0", "4":
		return "shipped"
	case "2":
		return "exception"
	default:
		return ""
	}
}

// CommonCarriers is a curated list of popular Chinese carriers with their
// kuaidi100 code, for use in select dropdowns.
var CommonCarriers = []struct {
	Code string
	Name string
}{
	{"yunexpress", "云途物流 (YunExpress)"},
	{"shunfeng", "顺丰速运 (SF Express)"},
	{"yuantong", "圆通速递 (YTO Express)"},
	{"zhongtong", "中通快递 (ZTO Express)"},
	{"shentong", "申通快递 (STO Express)"},
	{"yunda", "韵达速递 (Yunda Express)"},
	{"jd", "京东物流 (JD Logistics)"},
	{"ems", "EMS"},
	{"debangkuaidi", "德邦快递 (Deppon)"},
	{"jitu", "极兔速递 (J&T Express)"},
	{"cainiao", "菜鸟物流 (Cainiao)"},
	{"other", "Other"},
}
