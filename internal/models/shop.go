package models

import "time"

type Shop struct {
	ID                      uint `gorm:"primaryKey"`
	UserID                  uint
	Name                    *string `gorm:"size:200"`
	Logo                    *string `gorm:"size:255"`
	Sliders                 *string `gorm:"type:longtext"`
	TopBanner               *string `gorm:"size:191"`
	BannerFullWidth1        *string `gorm:"size:191"`
	BannersHalfWidth        *string `gorm:"size:191"`
	BannerFullWidth2        *string `gorm:"size:191"`
	Phone                   *string `gorm:"size:255"`
	Address                 *string `gorm:"size:500"`
	Rating                  float64 `gorm:"type:decimal(3,2);default:0"`
	NumOfReviews            int     `gorm:"default:0"`
	NumOfSale               int     `gorm:"default:0"`
	SellerPackageID         *uint
	ProductUploadLimit      int        `gorm:"default:0"`
	PackageInvalidAt        *time.Time `gorm:"type:date"`
	RegistrationApproval    int        `gorm:"default:0"`
	VerificationStatus      int        `gorm:"default:0"`
	VerificationInfo        *string    `gorm:"type:longtext"`
	BusinessInfo            *string    `gorm:"type:longtext"`
	GstVerification         int        `gorm:"default:0"`
	CashOnDeliveryStatus    int        `gorm:"default:0"`
	AdminToPay              float64    `gorm:"type:decimal(20,2);default:0"`
	Facebook                *string    `gorm:"size:255"`
	Instagram               *string    `gorm:"size:255"`
	Google                  *string    `gorm:"size:255"`
	Twitter                 *string    `gorm:"size:255"`
	Youtube                 *string    `gorm:"size:255"`
	Slug                    *string    `gorm:"size:255"`
	MetaTitle               *string    `gorm:"size:255"`
	MetaDescription         *string    `gorm:"type:text"`
	PickUpPointID           *string    `gorm:"type:text"`
	ShippingCost            float64    `gorm:"type:decimal(20,2);default:0"`
	CommissionPercentage    float64    `gorm:"type:decimal(8,2);default:0"`
	DeliveryPickupLatitude  *float64   `gorm:"type:decimal(17,15)"`
	DeliveryPickupLongitude *float64   `gorm:"type:decimal(17,15)"`
	BankName                *string    `gorm:"size:255"`
	BankAccName             *string    `gorm:"size:200"`
	BankAccNo               *string    `gorm:"size:50"`
	BankRoutingNo           *int
	BankPaymentStatus       int     `gorm:"default:0"`
	TopBannerImage          *string `gorm:"type:longtext"`
	TopBannerLink           *string `gorm:"type:longtext"`
	SliderImages            *string `gorm:"type:longtext"`
	SliderLinks             *string `gorm:"type:longtext"`
	BannerFullWidth1Images  *string `gorm:"type:longtext"`
	BannerFullWidth1Links   *string `gorm:"type:longtext"`
	BannersHalfWidthImages  *string `gorm:"type:longtext"`
	BannersHalfWidthLinks   *string `gorm:"type:longtext"`
	BannerFullWidth2Images  *string `gorm:"type:longtext"`
	BannerFullWidth2Links   *string `gorm:"type:longtext"`
	CustomFollowers         *int    `gorm:"default:0"`
	CreatedAt               time.Time
	UpdatedAt               *time.Time

	User *User `gorm:"foreignKey:UserID"`
}

func (Shop) TableName() string { return "shops" }

type SellerWithdrawRequest struct {
	ID        uint `gorm:"primaryKey"`
	UserID    *uint
	Amount    *float64 `gorm:"type:decimal(20,2)"`
	Message   *string  `gorm:"type:longtext"`
	Status    *int
	Viewed    *int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (SellerWithdrawRequest) TableName() string { return "seller_withdraw_requests" }

type CommissionHistory struct {
	ID              uint `gorm:"primaryKey"`
	OrderID         uint
	OrderDetailID   uint
	SellerID        uint
	AdminCommission float64 `gorm:"type:decimal(25,2)"`
	SellerEarning   float64 `gorm:"type:decimal(25,2)"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (CommissionHistory) TableName() string { return "commission_histories" }

type FollowSeller struct {
	UserID uint `gorm:"primaryKey"`
	ShopID uint `gorm:"primaryKey"`
}

func (FollowSeller) TableName() string { return "follow_sellers" }

type Note struct {
	ID           uint `gorm:"primaryKey"`
	UserID       uint
	NoteType     string `gorm:"size:50"`
	Description  string `gorm:"type:longtext"`
	SellerAccess int    `gorm:"default:0"`
	CreatedAt    *time.Time
	UpdatedAt    *time.Time
}

func (Note) TableName() string { return "notes" }
