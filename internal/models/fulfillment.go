// Package models — fulfillment models for supplier (1688 dropship) orders
// and kuaidi100 logistics tracking.
package models

import "time"

// SupplierOrder tracks a 1688 dropship purchase made on behalf of a customer
// order item.  One row per order_detail that is sourced from 1688.
//
// Lifecycle:
//   pending   → admin clicks "Order on 1688" and enters the 1688 trade ID
//   ordered   → ali_order_id has been set
//   shipped   → 1688 has dispatched; tracking_number is known
//   delivered → kuaidi100 state=="1" or manually confirmed
//   cancelled → order cancelled
type SupplierOrder struct {
	ID              uint       `gorm:"primaryKey"`
	OrderID         uint       `gorm:"index;not null"`
	OrderDetailID   uint       `gorm:"index;not null"`
	ProductID       uint       `gorm:"index;not null"`
	AliOfferID      string     `gorm:"size:64"`             // 1688 offer/product numeric ID
	AliOrderID      string     `gorm:"size:64;index"`       // 1688 trade order ID (entered manually)
	Qty             int        `gorm:"not null;default:1"`
	UnitCostCNY     float64    `gorm:"type:decimal(20,4)"` // CNY unit price at order time
	SourceURL       string     `gorm:"size:500"`            // 1688 URL with CPS tracking
	Status          string     `gorm:"size:32;default:'pending'"` // pending/ordered/shipped/delivered/cancelled
	TrackingCompany string     `gorm:"size:64"`  // kuaidi100 carrier code e.g. "shunfeng"
	TrackingNumber  string     `gorm:"size:128"` // logistics tracking number
	LogisticsJSON   *string    `gorm:"type:longtext"` // raw kuaidi100 API response
	Notes           *string    `gorm:"type:text"`
	SyncedAt        *time.Time // last successful kuaidi100 sync time
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// Relations
	Order       *Order       `gorm:"foreignKey:OrderID"`
	OrderDetail *OrderDetail `gorm:"foreignKey:OrderDetailID"`
	Product     *Product     `gorm:"foreignKey:ProductID"`
}

func (SupplierOrder) TableName() string { return "supplier_orders" }
