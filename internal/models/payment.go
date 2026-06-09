package models

import "time"

type PaymentMethod struct {
	ID              uint    `gorm:"primaryKey"`
	Name            string  `gorm:"size:255"`
	Active          int     `gorm:"default:0"`
	AddonIdentifier *string `gorm:"size:191"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (PaymentMethod) TableName() string { return "payment_methods" }

type Payment struct {
	ID             uint `gorm:"primaryKey"`
	SellerID       uint
	Amount         float64 `gorm:"type:decimal(20,2);default:0"`
	PaymentDetails *string `gorm:"type:longtext"`
	PaymentMethod  *string `gorm:"size:255"`
	TxnCode        *string `gorm:"size:100"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Payment) TableName() string { return "payments" }

type PaymentInformation struct {
	ID                 uint `gorm:"primaryKey"`
	UserID             uint
	PaymentType        string  `gorm:"size:255"`
	PaymentName        *string `gorm:"size:255"`
	PaymentInstruction *string `gorm:"size:255"`
	BankName           *string `gorm:"size:255"`
	AccountName        *string `gorm:"size:255"`
	AccountNumber      *string `gorm:"size:255"`
	RoutingNumber      *string `gorm:"size:255"`
	SetDefault         *string `gorm:"size:20"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (PaymentInformation) TableName() string { return "payment_informations" }

type CustomerPackage struct {
	ID            uint     `gorm:"primaryKey"`
	Name          *string  `gorm:"size:255"`
	Amount        *float64 `gorm:"type:decimal(20,2)"`
	ProductUpload *int
	Logo          *string `gorm:"size:150"`
	CreatedAt     *time.Time
	UpdatedAt     *time.Time
}

func (CustomerPackage) TableName() string { return "customer_packages" }

type CustomerPackagePayment struct {
	ID                uint `gorm:"primaryKey"`
	UserID            uint
	CustomerPackageID uint
	PaymentMethod     string  `gorm:"size:255"`
	Amount            float64 `gorm:"type:decimal(20,2)"`
	PaymentDetails    *string `gorm:"type:longtext"`
	Approval          int     `gorm:"default:1"`
	OfflinePayment    int     `gorm:"default:2"`
	Reciept           string  `gorm:"size:150"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (CustomerPackagePayment) TableName() string { return "customer_package_payments" }
