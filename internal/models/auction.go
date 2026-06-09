package models

import "time"

// AuctionBid records a single bid placed by a user on an auction product.
type AuctionBid struct {
	ID        uint      `gorm:"primaryKey"`
	ProductID uint      `gorm:"index"`
	UserID    uint      `gorm:"index"`
	Amount    float64   `gorm:"type:decimal(20,2)"`
	CreatedAt time.Time

	User    *User    `gorm:"foreignKey:UserID"`
	Product *Product `gorm:"foreignKey:ProductID"`
}

func (AuctionBid) TableName() string { return "auction_bids" }

// AuctionWinner records the outcome of a closed auction.
// Status values: pending (won, not yet paid) | paid | expired (winner didn't pay in time).
type AuctionWinner struct {
	ID        uint    `gorm:"primaryKey"`
	ProductID uint    `gorm:"uniqueIndex"` // one winner per product
	UserID    uint    `gorm:"index"`
	BidID     uint
	Amount    float64 `gorm:"type:decimal(20,2)"`
	Status    string  `gorm:"size:20;default:pending"`
	OrderID   *uint
	CreatedAt time.Time
	UpdatedAt time.Time

	Product *Product `gorm:"foreignKey:ProductID"`
	User    *User    `gorm:"foreignKey:UserID"`
	Order   *Order   `gorm:"foreignKey:OrderID"`
}

func (AuctionWinner) TableName() string { return "auction_winners" }
