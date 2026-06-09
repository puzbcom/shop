// Package auction provides background processing for ended auctions.
package auction

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"mall/internal/models"
)

// CloseEnded finds all auction products whose end time has passed and no winner
// has been recorded yet, then creates an AuctionWinner row for the highest bidder.
// If there are no bids, a sentinel no_bids record is written so the product is
// not re-processed on the next tick.
// Returns the number of auctions settled and any error.
func CloseEnded(db *gorm.DB) (int, error) {
	// Find products whose auction has ended but have no winner yet.
	var products []models.Product
	db.Where("auction_product = 1 AND auction_end_at IS NOT NULL AND auction_end_at <= ?", time.Now()).
		Where("id NOT IN (SELECT product_id FROM auction_winners)").
		Find(&products)

	settled := 0
	for _, p := range products {
		pid := p.ID

		// Double-check inside a transaction to handle concurrent scheduler instances.
		txErr := db.Transaction(func(tx *gorm.DB) error {
			// Re-check under lock: if a winner already exists, skip.
			var count int64
			tx.Model(&models.AuctionWinner{}).Where("product_id = ?", pid).Count(&count)
			if count > 0 {
				return nil // already handled by another goroutine/restart
			}

			// Find the highest bid.
			var topBid models.AuctionBid
			if err := tx.Where("product_id = ?", pid).Order("amount desc").First(&topBid).Error; err != nil {
				// No bids — write sentinel so we don't re-check this product.
				return tx.Create(&models.AuctionWinner{
					ProductID: pid,
					UserID:    0,
					Status:    "no_bids",
				}).Error
			}

			return tx.Create(&models.AuctionWinner{
				ProductID: pid,
				UserID:    topBid.UserID,
				BidID:     topBid.ID,
				Amount:    topBid.Amount,
				Status:    "pending",
			}).Error
		})

		if txErr != nil {
			fmt.Printf("[AUCTION] Product %d: error closing auction: %v\n", pid, txErr)
			continue
		}

		// Re-load to print result.
		var winner models.AuctionWinner
		db.Where("product_id = ?", pid).First(&winner)
		if winner.Status == "no_bids" {
			fmt.Printf("[AUCTION] Product %d closed — no bids\n", pid)
		} else {
			fmt.Printf("[AUCTION] Product %d closed — winner user %d, bid %.2f\n",
				pid, winner.UserID, winner.Amount)
		}
		settled++
	}
	return settled, nil
}

// RunScheduler starts a background goroutine that calls CloseEnded every minute.
// It runs an initial check 10 seconds after startup, then once per minute.
func RunScheduler(db *gorm.DB) {
	go func() {
		// Short initial delay to let DB connections settle.
		time.Sleep(10 * time.Second)

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		// Run immediately on startup, then on every tick.
		for {
			if n, err := CloseEnded(db); err != nil {
				fmt.Printf("[AUCTION] Scheduler error: %v\n", err)
			} else if n > 0 {
				fmt.Printf("[AUCTION] Closed %d auction(s)\n", n)
			}
			<-ticker.C // wait for next minute before checking again
		}
	}()
}
