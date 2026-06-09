package models

import "time"

type AffiliateLog struct {
	ID          uint      `gorm:"primaryKey"`
	UserID      uint      `gorm:"index"` // the affiliate (referrer)
	Type        string    `gorm:"size:20"` // "signup" | "purchase"
	Amount      float64   `gorm:"type:decimal(15,2);default:0"`
	ReferredUID *uint     // the user who was referred
	OrderID     *uint
	CreatedAt   time.Time
}

func (AffiliateLog) TableName() string { return "affiliate_logs" }

type AffiliateWithdraw struct {
	ID        uint       `gorm:"primaryKey"`
	UserID    uint       `gorm:"index"`
	Amount    float64    `gorm:"type:decimal(15,2)"`
	Status    string     `gorm:"size:20;default:'pending'"` // pending | approved | rejected
	Note      *string    `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time

	User *User `gorm:"foreignKey:UserID"`
}

func (AffiliateWithdraw) TableName() string { return "affiliate_withdraws" }
