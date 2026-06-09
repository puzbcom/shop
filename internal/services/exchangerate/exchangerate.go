// Package exchangerate fetches the CNY exchange rate from exchangerate-api.com
// and persists it to the business_settings table every Monday at 08:00.
package exchangerate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"mall/internal/models"
	"mall/internal/services/settings"
)

// Fetcher holds dependencies for fetching and persisting the exchange rate.
type Fetcher struct {
	DB       *gorm.DB
	Settings *settings.Store
}

type apiResponse struct {
	Result string             `json:"result"`
	Rates  map[string]float64 `json:"conversion_rates"`
	Error  string             `json:"error-type"`
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// FetchAndSave fetches the current CNY → store-currency rate from exchangerate-api.com
// and writes it to the business_settings table, then invalidates the settings cache.
func (f *Fetcher) FetchAndSave() error {
	apiKey := f.Settings.Get("exchange_rate_api_key", "")
	if apiKey == "" {
		return fmt.Errorf("exchange_rate_api_key not configured in business settings")
	}

	// Target currency from business settings (e.g. "USD", "EUR")
	target := strings.ToUpper(strings.TrimSpace(f.Settings.Get("default_currency", "USD")))
	if target == "" {
		target = "USD"
	}

	url := fmt.Sprintf("https://v6.exchangerate-api.com/v6/%s/latest/CNY", apiKey)
	fmt.Printf("[EXCHANGE] Fetching CNY→%s from exchangerate-api.com\n", target)

	resp, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d from exchangerate-api.com", resp.StatusCode)
	}

	var data apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if data.Result != "success" {
		return fmt.Errorf("API error: %s", data.Error)
	}

	rate, ok := data.Rates[target]
	if !ok {
		return fmt.Errorf("currency %q not found in API response", target)
	}

	// Persist with 6 decimal places
	rateStr := strconv.FormatFloat(rate, 'f', 6, 64)
	if err := upsertSetting(f.DB, "exchange_rate", rateStr); err != nil {
		return fmt.Errorf("save exchange_rate: %w", err)
	}

	f.Settings.Invalidate()
	fmt.Printf("[EXCHANGE] Updated CNY→%s = %s\n", target, rateStr)
	return nil
}

// upsertSetting inserts or updates a business_setting row.
func upsertSetting(db *gorm.DB, key, value string) error {
	var count int64
	db.Model(&models.BusinessSetting{}).Where("type = ?", key).Count(&count)
	if count == 0 {
		// Copy value to a heap-allocated string so the pointer is safe for GORM to store.
		v := value
		return db.Create(&models.BusinessSetting{Type: key, Value: &v}).Error
	}
	return db.Model(&models.BusinessSetting{}).Where("type = ?", key).Update("value", value).Error
}

// nextMonday returns the next Monday at 08:00 local time.
// If today is Monday and it is before 08:00 it returns today at 08:00.
func nextMonday(from time.Time) time.Time {
	d := from
	// Advance to Monday
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, 1)
	}
	target := time.Date(d.Year(), d.Month(), d.Day(), 8, 0, 0, 0, from.Location())
	// If Monday 08:00 is already past, go to next week
	if !target.After(from) {
		target = target.AddDate(0, 0, 7)
	}
	return target
}

// RunWeeklyScheduler starts a background goroutine that:
//  1. Runs an initial fetch shortly after startup (30 s delay so DB is ready).
//  2. Then fires every Monday at 08:00 local time.
func RunWeeklyScheduler(f *Fetcher) {
	go func() {
		// Initial fetch after a brief startup delay
		time.Sleep(30 * time.Second)
		if err := f.FetchAndSave(); err != nil {
			fmt.Printf("[EXCHANGE] Initial fetch skipped/failed: %v\n", err)
		}

		// Weekly loop
		for {
			next := nextMonday(time.Now())
			fmt.Printf("[EXCHANGE] Next scheduled update: %s\n", next.Format(time.RFC1123))
			time.Sleep(time.Until(next))

			if err := f.FetchAndSave(); err != nil {
				fmt.Printf("[EXCHANGE] Weekly fetch failed: %v\n", err)
			}
		}
	}()
}
