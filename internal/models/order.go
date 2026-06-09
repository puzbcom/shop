package models

import "time"

type CombinedOrder struct {
	ID              uint `gorm:"primaryKey"`
	UserID          uint
	ShippingAddress *string `gorm:"type:text"`
	GrandTotal      float64 `gorm:"type:decimal(20,2);default:0"`
	CreatedAt       time.Time
	UpdatedAt       time.Time

	Orders []Order `gorm:"foreignKey:CombinedOrderID"`
}

func (CombinedOrder) TableName() string { return "combined_orders" }

type Order struct {
	ID                     uint `gorm:"primaryKey"`
	CombinedOrderID        *uint
	UserID                 *uint
	GuestID                *uint
	SellerID               *uint
	ShippingAddress        *string `gorm:"type:longtext"`
	BillingAddress         *string `gorm:"type:longtext"`
	AdditionalInfo         *string `gorm:"type:longtext"`
	ShippingType           string  `gorm:"size:50"`
	ShippingMethod         *string `gorm:"size:255"`
	OrderFrom              string  `gorm:"size:20;default:web"`
	PickupPointID          uint    `gorm:"default:0"`
	CarrierID              *uint
	DeliveryStatus         *string  `gorm:"size:20;default:pending"`
	PaymentType            *string  `gorm:"size:20"`
	PaymentStatus          *string  `gorm:"size:20;default:unpaid"`
	PaymentDetails         *string  `gorm:"type:longtext"`
	GrandTotal             *float64 `gorm:"type:decimal(20,2)"`
	CouponDiscount         float64  `gorm:"type:decimal(20,2);default:0"`
	Code                   *string  `gorm:"type:mediumtext"`
	TrackingCode           *string  `gorm:"size:255"`
	ShiprocketOrderID      *uint64
	ShiprocketShipmentID   *uint64
	ShiprocketStatus       *string `gorm:"size:50"`
	ShiprocketOrderStatus  *string `gorm:"size:255"`
	ShiprocketStatusCode   int64   `gorm:"default:0"`
	PickupAddressID        *uint64
	ShiprocketAwb          *string `gorm:"size:255"`
	ShiprocketCourierID    *uint64
	ShiprocketCourierName  *string `gorm:"size:255"`
	AwbAssignedAt          *time.Time
	ShiprocketLabelURL     *string `gorm:"column:shiprocket_label_url;type:text"`
	ShiprocketManifestURL  *string `gorm:"column:shiprocket_manifest_url;type:text"`
	PickupScheduledAt      *time.Time
	PickupToken            *string `gorm:"size:100"`
	SteadfastConsignmentID *string `gorm:"size:100"`
	SteadfastTrackingCode  *string `gorm:"size:100"`
	SteadfastStatus        *string `gorm:"size:100"`
	PathaoConsignmentID    *string `gorm:"size:100"`
	PathaoStatus           *string `gorm:"size:100"`
	RedxTrackingID         *string `gorm:"size:100"`
	RedxStatus             *string `gorm:"size:100"`
	RedxCharge             *string `gorm:"size:100"`
	PathaoDeliveryFee      *int    `gorm:"default:0"`
	Date                   int     `gorm:"column:date"`
	Viewed                 int     `gorm:"default:0"`
	DeliveryViewed         int     `gorm:"default:1"`
	PaymentStatusViewed    *int    `gorm:"default:1"`
	CommissionCalculated   int     `gorm:"default:0"`
	Notified               int     `gorm:"default:0"`
	DeliveredDate          *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time

	OrderDetails  []OrderDetail  `gorm:"foreignKey:OrderID"`
	Buyer         *User          `gorm:"foreignKey:UserID"`
	Seller        *User          `gorm:"foreignKey:SellerID"`
	CombinedOrder *CombinedOrder `gorm:"foreignKey:CombinedOrderID"`
}

func (Order) TableName() string { return "orders" }

type OrderDetail struct {
	ID                  uint `gorm:"primaryKey"`
	OrderID             uint
	SellerID            *uint
	ProductID           uint
	Variation           *string  `gorm:"type:longtext"`
	Price               *float64 `gorm:"type:decimal(20,2)"`
	CouponDiscount      *float64 `gorm:"type:decimal(20,2);default:0"`
	Tax                 float64  `gorm:"type:decimal(20,2);default:0"`
	GstRate             *float64 `gorm:"type:decimal(20,2)"`
	GstAmount           *float64 `gorm:"type:decimal(20,2)"`
	ShippingCost        float64  `gorm:"type:decimal(20,2);default:0"`
	Quantity            *int
	PaymentStatus       string  `gorm:"size:10;default:unpaid"`
	DeliveryStatus      *string `gorm:"size:20;default:pending"`
	RefundDays          int     `gorm:"default:0"`
	Reviewed            int     `gorm:"default:0"`
	ShippingType        *string `gorm:"size:255"`
	PickupPointID       *uint
	ProductReferralCode *string `gorm:"size:255"`
	EarnPoint           float64 `gorm:"type:decimal(25,2);default:0"`
	CreatedAt           time.Time
	UpdatedAt           time.Time

	Product *Product `gorm:"foreignKey:ProductID"`

	// VariantImage is the ordered variant's featured image path, resolved at render time (not persisted).
	VariantImage *string `gorm:"-"`
}

func (OrderDetail) TableName() string { return "order_details" }

type Coupon struct {
	ID           uint `gorm:"primaryKey"`
	UserID       uint
	Type         string  `gorm:"size:255"`
	Code         string  `gorm:"size:255"`
	Details      string  `gorm:"type:longtext"`
	Discount     float64 `gorm:"type:decimal(20,2)"`
	DiscountType string  `gorm:"size:100"`
	StartDate    *int
	EndDate      *int
	Status       int `gorm:"default:1"`
	CreatedAt    time.Time
	UpdatedAt    time.Time

	Usages []CouponUsage `gorm:"foreignKey:CouponID"`
}

func (Coupon) TableName() string { return "coupons" }

type CouponUsage struct {
	ID        uint `gorm:"primaryKey"`
	UserID    uint
	CouponID  uint
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (CouponUsage) TableName() string { return "coupon_usages" }

type RefundRequest struct {
	ID        uint    `gorm:"primaryKey"`
	OrderID   uint    `gorm:"not null;index"`
	UserID    uint    `gorm:"not null;index"`
	Reason    string  `gorm:"type:text"`
	Status    string  `gorm:"size:20;default:pending"` // pending | approved | declined
	AdminNote *string `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Order *Order `gorm:"foreignKey:OrderID"`
	User  *User  `gorm:"foreignKey:UserID"`
}

func (RefundRequest) TableName() string { return "refund_requests" }

type UserCoupon struct {
	UserID         uint    `gorm:"primaryKey"`
	CouponID       uint    `gorm:"primaryKey"`
	CouponCode     string  `gorm:"size:255"`
	MinBuy         float64 `gorm:"type:decimal(20,2)"`
	ValidationDays int
	Discount       float64 `gorm:"type:decimal(20,2)"`
	DiscountType   string  `gorm:"size:20"`
	ExpiryDate     int
}

func (UserCoupon) TableName() string { return "user_coupons" }

type Cart struct {
	ID                  uint `gorm:"primaryKey"`
	Status              int  `gorm:"default:1"`
	OwnerID             *uint
	UserID              *uint
	TempUserID          *string `gorm:"size:255"`
	AddressID           int     `gorm:"default:0"`
	BillingAddress      int     `gorm:"default:0"`
	ProductID           *uint
	Variation           *string  `gorm:"type:text"`
	Price               *float64 `gorm:"type:decimal(20,2);default:0"`
	Tax                 *float64 `gorm:"type:decimal(20,2);default:0"`
	ShippingCost        float64  `gorm:"type:decimal(20,2);default:0"`
	ShippingType        string   `gorm:"size:30"`
	PickupPoint         *uint
	CarrierID           *uint
	Discount            float64 `gorm:"type:decimal(10,2);default:0"`
	ProductReferralCode *string `gorm:"size:255"`
	CouponCode          *string `gorm:"size:255"`
	CouponApplied       int     `gorm:"default:0"`
	Quantity            int     `gorm:"default:0"`
	CreatedAt           time.Time
	UpdatedAt           time.Time

	User    *User    `gorm:"foreignKey:UserID"`
	Product *Product `gorm:"foreignKey:ProductID"`

	// VariantImage is the selected variant's featured image path, resolved at render time (not persisted).
	VariantImage *string `gorm:"-"`
}

func (Cart) TableName() string { return "carts" }

type Transaction struct {
	ID                uint `gorm:"primaryKey"`
	UserID            uint
	Gateway           *string `gorm:"size:255"`
	PaymentType       *string `gorm:"size:255"`
	AdditionalContent *string `gorm:"type:text"`
	MpesaRequest      *string `gorm:"size:255"`
	MpesaReceipt      *string `gorm:"size:255"`
	Status            int     `gorm:"default:0"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (Transaction) TableName() string { return "transactions" }

type CustomerProduct struct {
	ID               uint    `gorm:"primaryKey"`
	Name             *string `gorm:"size:255"`
	Published        int     `gorm:"default:0"`
	Status           int     `gorm:"default:0"`
	AddedBy          *string `gorm:"size:50"`
	UserID           *uint
	CategoryID       *uint
	SubcategoryID    *uint
	SubsubcategoryID *uint
	BrandID          *uint
	Photos           *string  `gorm:"size:255"`
	ThumbnailImg     *string  `gorm:"size:150"`
	Conditon         *string  `gorm:"size:50"`
	Location         *string  `gorm:"type:text"`
	VideoProvider    *string  `gorm:"size:100"`
	VideoLink        *string  `gorm:"size:200"`
	Unit             *string  `gorm:"size:200"`
	Tags             *string  `gorm:"size:255"`
	Description      *string  `gorm:"type:mediumtext"`
	UnitPrice        *float64 `gorm:"type:decimal(20,2);default:0"`
	MetaTitle        *string  `gorm:"size:200"`
	MetaDescription  *string  `gorm:"size:500"`
	MetaImg          *string  `gorm:"size:150"`
	Pdf              *string  `gorm:"size:200"`
	Slug             *string  `gorm:"size:200"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (CustomerProduct) TableName() string { return "customer_products" }

type ProductQuery struct {
	ID         uint `gorm:"primaryKey"`
	CustomerID uint
	SellerID   uint
	ProductID  uint
	Question   string  `gorm:"type:longtext"`
	Reply      *string `gorm:"type:longtext"`
	CreatedAt  *time.Time
	UpdatedAt  *time.Time
}

func (ProductQuery) TableName() string { return "product_queries" }
