package models

import "time"

type Category struct {
	ID                uint `gorm:"primaryKey"`
	ParentID          *uint
	Level             int     `gorm:"default:0"`
	Name              string  `gorm:"size:50"`
	OrderLevel        int     `gorm:"default:0"`
	CommisionRate     float64 `gorm:"type:decimal(8,2);default:0"`
	Discount          float64 `gorm:"type:decimal(20,2);default:0"`
	DiscountStartDate *int
	DiscountEndDate   *int
	Banner            *string `gorm:"size:100"`
	Icon              *string `gorm:"size:100"`
	CoverImage        *string `gorm:"size:100"`
	Featured          int     `gorm:"default:0"`
	HotCategory       string  `gorm:"type:enum('0','1');default:'0'"`
	Top               int     `gorm:"default:0"`
	Digital           int     `gorm:"default:0"`
	Slug              *string `gorm:"size:255"`
	RefundRequestTime *uint
	MetaTitle         *string `gorm:"size:255"`
	MetaDescription   *string `gorm:"type:text"`
	MetaKeywords      *string `gorm:"size:255"`
	CreatedAt         time.Time
	UpdatedAt         *time.Time

	Parent       *Category             `gorm:"foreignKey:ParentID"`
	Children     []Category            `gorm:"foreignKey:ParentID"`
	Translations []CategoryTranslation `gorm:"foreignKey:CategoryID"`
	Products     []Product             `gorm:"foreignKey:CategoryID"`
}

func (Category) TableName() string { return "categories" }

type CategoryTranslation struct {
	ID         uint `gorm:"primaryKey"`
	CategoryID uint
	Name       string `gorm:"size:50"`
	Lang       string `gorm:"size:100"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (CategoryTranslation) TableName() string { return "category_translations" }

type Brand struct {
	ID              uint    `gorm:"primaryKey"`
	Name            string  `gorm:"size:50"`
	Logo            *string `gorm:"size:100"`
	Top             int     `gorm:"default:0"`
	Slug            *string `gorm:"size:255"`
	MetaTitle       *string `gorm:"size:255"`
	MetaDescription *string `gorm:"type:text"`
	MetaKeywords    *string `gorm:"size:255"`
	CreatedAt       time.Time
	UpdatedAt       time.Time

	Translations []BrandTranslation `gorm:"foreignKey:BrandID"`
	Products     []Product          `gorm:"foreignKey:BrandID"`
}

func (Brand) TableName() string { return "brands" }

type BrandTranslation struct {
	ID        uint `gorm:"primaryKey"`
	BrandID   uint
	Name      string `gorm:"size:50"`
	Lang      string `gorm:"size:100"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (BrandTranslation) TableName() string { return "brand_translations" }

type Attribute struct {
	ID        uint    `gorm:"primaryKey"`
	Name      *string `gorm:"size:255"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Values       []AttributeValue       `gorm:"foreignKey:AttributeID"`
	Translations []AttributeTranslation `gorm:"foreignKey:AttributeID"`
}

func (Attribute) TableName() string { return "attributes" }

type AttributeTranslation struct {
	ID          uint `gorm:"primaryKey"`
	AttributeID uint
	Name        string `gorm:"size:50"`
	Lang        string `gorm:"size:100"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (AttributeTranslation) TableName() string { return "attribute_translations" }

type AttributeValue struct {
	ID          uint `gorm:"primaryKey"`
	AttributeID uint
	Value       string  `gorm:"size:255"`
	ColorCode   *string `gorm:"size:100"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (AttributeValue) TableName() string { return "attribute_values" }

type Color struct {
	ID        uint    `gorm:"primaryKey"`
	Name      *string `gorm:"size:30"`
	Code      *string `gorm:"size:10"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Color) TableName() string { return "colors" }

type Product struct {
	ID                            uint   `gorm:"primaryKey"`
	Name                          string `gorm:"size:200"`
	AddedBy                       string `gorm:"size:6;default:admin"`
	UserID                        uint
	CategoryID                    uint
	BrandID                       *uint
	Photos                        *string  `gorm:"size:2000"`
	ThumbnailImg                  *string  `gorm:"size:100"`
	ThumbnailImgAlt               *string  `gorm:"size:255"`
	ShortVideo                    *string  `gorm:"size:255"`
	ShortVideoThumbnail           *string  `gorm:"size:255"`
	VideoProvider                 *string  `gorm:"size:20"`
	VideoLink                     *string  `gorm:"type:longtext"`
	Tags                          *string  `gorm:"size:500"`
	Description                   *string  `gorm:"type:longtext"`
	UnitPrice                     float64  `gorm:"type:decimal(20,2)"`
	PurchasePrice                 *float64 `gorm:"type:decimal(20,2)"`
	VariantProduct                int      `gorm:"default:0"`
	Attributes                    string   `gorm:"size:1000"`
	ChoiceOptions                 *string  `gorm:"type:mediumtext"`
	Colors                        *string  `gorm:"type:mediumtext"`
	Variations                    *string  `gorm:"type:text"`
	CustomLabelID                 *string  `gorm:"size:1000"`
	TodaysDeal                    int      `gorm:"default:0"`
	Published                     int      `gorm:"default:1"`
	Draft                         int      `gorm:"default:0"`
	Pos                           int      `gorm:"default:0"`
	Approved                      int      `gorm:"default:1"`
	StockVisibilityState          string   `gorm:"size:10;default:quantity"`
	CashOnDelivery                int      `gorm:"default:0"`
	Featured                      int      `gorm:"default:0"`
	SellerFeatured                int      `gorm:"default:0"`
	CurrentStock                  int      `gorm:"default:0"`
	Unit                          *string  `gorm:"size:20"`
	Weight                        float64  `gorm:"type:decimal(8,2);default:0"`
	MinQty                        int      `gorm:"default:1"`
	LowStockQuantity              *int
	Discount                      float64 `gorm:"type:decimal(20,2);default:0"`
	DiscountType                  string  `gorm:"size:10;default:amount"`
	DiscountStartDate             *int
	DiscountEndDate               *int
	Tax                           *float64 `gorm:"type:decimal(20,2)"`
	TaxType                       *string  `gorm:"size:10"`
	HsnCode                       *string  `gorm:"size:20"`
	GstRate                       *float64 `gorm:"type:decimal(20,2);default:0"`
	ShippingType                  *string  `gorm:"size:20;default:flat_rate"`
	ShippingCost                  float64  `gorm:"type:decimal(20,2);default:0"`
	IsQuantityMultiplied          int      `gorm:"default:0"`
	EstShippingDays               *int
	NumOfSale                     int     `gorm:"default:0"`
	MetaTitle                     *string `gorm:"type:mediumtext"`
	MetaDescription               *string `gorm:"type:longtext"`
	MetaKeywords                  *string `gorm:"size:255"`
	MetaImg                       *string `gorm:"size:255"`
	Pdf                           *string `gorm:"size:255"`
	Slug                          string  `gorm:"type:mediumtext"`
	Rating                        float64 `gorm:"type:decimal(8,2);default:0"`
	Barcode                       *string `gorm:"size:255"`
	Digital                       int     `gorm:"default:0"`
	AuctionProduct                int        `gorm:"default:0"`
	AuctionStartPrice             *float64   `gorm:"type:decimal(20,2)"`
	AuctionMinBidIncrement        *float64   `gorm:"type:decimal(20,2)"`
	AuctionEndAt                  *time.Time
	FileName                      *string `gorm:"size:255"`
	FilePath                      *string `gorm:"size:255"`
	Source1688URL                 *string `gorm:"size:500"` // 1688 product URL with CPS tracking, set on import
	WholesaleProduct              int     `gorm:"default:0"`
	FrequentlyBoughtSelectionType *string `gorm:"size:19"`
	HasWarranty                   int     `gorm:"default:0"`
	WarrantyID                    *uint
	WarrantyNoteID                *uint
	CreatedAt                     time.Time
	UpdatedAt                     time.Time

	Category     *Category            `gorm:"foreignKey:CategoryID"`
	Brand        *Brand               `gorm:"foreignKey:BrandID"`
	User         *User                `gorm:"foreignKey:UserID"`
	Translations []ProductTranslation `gorm:"foreignKey:ProductID"`
	Stocks       []ProductStock       `gorm:"foreignKey:ProductID"`
	Taxes        []ProductTax         `gorm:"foreignKey:ProductID"`
	Reviews      []Review             `gorm:"foreignKey:ProductID"`
	Wishlists    []Wishlist           `gorm:"foreignKey:ProductID"`
	OrderDetails []OrderDetail        `gorm:"foreignKey:ProductID"`
}

func (Product) TableName() string { return "products" }

type ProductTranslation struct {
	ID          uint `gorm:"primaryKey"`
	ProductID   uint
	Name        *string `gorm:"size:200"`
	Unit        *string `gorm:"size:20"`
	Description *string `gorm:"type:longtext"`
	Lang        string  `gorm:"size:100"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (ProductTranslation) TableName() string { return "product_translations" }

type ProductCategory struct {
	ProductID  uint `gorm:"primaryKey"`
	CategoryID uint `gorm:"primaryKey"`
}

func (ProductCategory) TableName() string { return "product_categories" }

type ProductStock struct {
	ID        uint `gorm:"primaryKey"`
	ProductID uint
	Variant   string  `gorm:"size:255"`
	Sku       *string `gorm:"size:255"`
	Price     float64 `gorm:"type:decimal(20,2);default:0"`
	Qty       int     `gorm:"default:0"`
	Image     *uint
	// VariantImage is the featured image path shown when this variant is selected.
	VariantImage *string `gorm:"column:variant_image;size:2000"`
	CreatedAt    time.Time
	UpdatedAt time.Time
}

func (ProductStock) TableName() string { return "product_stocks" }

type ProductTax struct {
	ID        uint `gorm:"primaryKey"`
	ProductID uint
	TaxID     uint
	Tax       float64 `gorm:"type:decimal(20,2)"`
	TaxType   string  `gorm:"size:10"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (ProductTax) TableName() string { return "product_taxes" }

type Tax struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:255"`
	TaxStatus int    `gorm:"default:1"`
	CreatedAt *time.Time
	UpdatedAt time.Time
}

func (Tax) TableName() string { return "taxes" }

type Warranty struct {
	ID        uint   `gorm:"primaryKey"`
	Text      string `gorm:"size:100"`
	Logo      *uint
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Warranty) TableName() string { return "warranties" }

type WarrantyTranslation struct {
	ID         uint `gorm:"primaryKey"`
	WarrantyID uint
	Text       string `gorm:"size:50"`
	Lang       string `gorm:"size:100"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (WarrantyTranslation) TableName() string { return "warranty_translations" }

type CustomLabel struct {
	ID              uint `gorm:"primaryKey"`
	UserID          uint
	Text            string `gorm:"size:255"`
	BackgroundColor string `gorm:"size:255"`
	TextColor       string `gorm:"size:255"`
	SellerAccess    int    `gorm:"default:0"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (CustomLabel) TableName() string { return "custom_labels" }

type CustomLabelTranslation struct {
	ID            uint `gorm:"primaryKey"`
	CustomLabelID uint
	Text          string `gorm:"size:255"`
	Lang          string `gorm:"size:100"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (CustomLabelTranslation) TableName() string { return "custom_label_translations" }

type Review struct {
	ID                  uint   `gorm:"primaryKey"`
	Type                string `gorm:"size:10;default:real"`
	ProductID           uint
	UserID              *uint
	CustomReviewerName  *string `gorm:"size:100"`
	CustomReviewerImage *string `gorm:"size:100"`
	Rating              int     `gorm:"default:0"`
	Comment             string  `gorm:"type:mediumtext"`
	Photos              *string `gorm:"size:191"`
	Status              int     `gorm:"default:1"`
	Viewed              int     `gorm:"default:0"`
	CreatedAtIsCustom   int     `gorm:"default:0"`
	CreatedAt           time.Time
	UpdatedAt           time.Time

	Product *Product `gorm:"foreignKey:ProductID"`
	User    *User    `gorm:"foreignKey:UserID"`
}

func (Review) TableName() string { return "reviews" }

type FlashDeal struct {
	ID              uint    `gorm:"primaryKey"`
	Title           *string `gorm:"size:255"`
	StartDate       *int
	EndDate         *int
	Status          int     `gorm:"default:0"`
	Featured        int     `gorm:"default:0"`
	BackgroundColor *string `gorm:"size:255"`
	TextColor       *string `gorm:"size:255"`
	Banner          *string `gorm:"size:255"`
	Slug            *string `gorm:"size:255"`
	CreatedAt       *time.Time
	UpdatedAt       time.Time
}

func (FlashDeal) TableName() string { return "flash_deals" }

type FlashDealProduct struct {
	ID           uint `gorm:"primaryKey"`
	FlashDealID  uint
	ProductID    uint
	Discount     *float64 `gorm:"type:decimal(20,2);default:0"`
	DiscountType *string  `gorm:"size:20"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (FlashDealProduct) TableName() string { return "flash_deal_products" }

type SizeChart struct {
	ID                uint   `gorm:"primaryKey"`
	Name              string `gorm:"size:255"`
	CategoryID        uint
	FitType           *string `gorm:"size:191"`
	StretchType       *string `gorm:"size:191"`
	Photos            *string `gorm:"size:191"`
	Description       *string `gorm:"type:text"`
	MeasurementPoints string  `gorm:"size:255"`
	SizeOptions       string  `gorm:"size:255"`
	MeasurementOption *string `gorm:"size:191"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (SizeChart) TableName() string { return "size_charts" }

type SizeChartDetail struct {
	ID                 uint `gorm:"primaryKey"`
	SizeChartID        uint
	MeasurementPointID uint
	AttributeValueID   uint
	InchValue          *string `gorm:"size:191"`
	CenValue           *string `gorm:"size:191"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (SizeChartDetail) TableName() string { return "size_chart_details" }

type Wishlist struct {
	ID        uint `gorm:"primaryKey"`
	UserID    uint
	ProductID uint
	CreatedAt time.Time
	UpdatedAt time.Time

	Product *Product `gorm:"foreignKey:ProductID"`
}

func (Wishlist) TableName() string { return "wishlists" }

type Search struct {
	ID        uint   `gorm:"primaryKey"`
	Query     string `gorm:"size:1000"`
	Count     int    `gorm:"default:1"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Search) TableName() string { return "searches" }

type LastViewedProduct struct {
	ID        uint `gorm:"primaryKey"`
	UserID    uint
	ProductID uint
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (LastViewedProduct) TableName() string { return "last_viewed_products" }

type FrequentlyBoughtProduct struct {
	ProductID                 uint `gorm:"primaryKey"`
	FrequentlyBoughtProductID *uint
	CategoryID                *uint
}

func (FrequentlyBoughtProduct) TableName() string { return "frequently_bought_products" }
