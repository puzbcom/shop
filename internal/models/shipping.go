package models

import "time"

type Carrier struct {
	ID           uint   `gorm:"primaryKey"`
	Name         string `gorm:"size:255"`
	Logo         *uint
	TransitTime  string `gorm:"size:255"`
	FreeShipping int    `gorm:"default:0"`
	Status       int    `gorm:"default:1"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Carrier) TableName() string { return "carriers" }

type CarrierRange struct {
	ID          uint `gorm:"primaryKey"`
	CarrierID   uint
	BillingType string  `gorm:"size:20"`
	Delimiter1  float64 `gorm:"type:decimal(25,2)"`
	Delimiter2  float64 `gorm:"type:decimal(25,2)"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (CarrierRange) TableName() string { return "carrier_ranges" }

type CarrierRangePrice struct {
	ID             uint `gorm:"primaryKey"`
	CarrierID      uint
	CarrierRangeID uint
	ZoneID         uint
	Price          float64 `gorm:"type:decimal(8,2)"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (CarrierRangePrice) TableName() string { return "carrier_range_prices" }

type ShippingSystem struct {
	ID              uint    `gorm:"primaryKey"`
	Name            string  `gorm:"size:255"`
	Active          int     `gorm:"default:0"`
	AddonIdentifier *string `gorm:"size:255"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (ShippingSystem) TableName() string { return "shipping_systems" }

type PickupPoint struct {
	ID                 uint `gorm:"primaryKey"`
	StaffID            uint
	Name               string `gorm:"size:255"`
	Address            string `gorm:"type:mediumtext"`
	Phone              string `gorm:"size:15"`
	PickUpStatus       *int
	CashOnPickupStatus *int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (PickupPoint) TableName() string { return "pickup_points" }

type PickupAddress struct {
	ID              uint `gorm:"primaryKey"`
	UserID          uint
	CourierType     string `gorm:"size:255"`
	AddressNickname string `gorm:"size:255"`
	IsPrimary       int    `gorm:"default:0"`
	Status          int    `gorm:"default:1"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (PickupAddress) TableName() string { return "pickup_addresses" }

type ShippingBoxSize struct {
	ID          uint   `gorm:"primaryKey"`
	CourierType string `gorm:"size:255"`
	UserID      uint
	Length      float64 `gorm:"type:float"`
	Breadth     float64 `gorm:"type:float"`
	Height      float64 `gorm:"type:float"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (ShippingBoxSize) TableName() string { return "shipping_box_sizes" }
