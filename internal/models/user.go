package models

import (
	"time"
)

type User struct {
	ID                             uint `gorm:"primaryKey"`
	ReferredBy                     *uint
	Provider                       *string `gorm:"size:255"`
	ProviderID                     *string `gorm:"size:50"`
	RefreshToken                   *string `gorm:"type:text"`
	AccessToken                    *string `gorm:"type:longtext"`
	UserType                       string  `gorm:"size:20;default:customer"`
	Name                           string  `gorm:"size:191"`
	Email                          *string `gorm:"size:191;uniqueIndex"`
	EmailVerifiedAt                *time.Time
	VerificationCode               *string `gorm:"type:text"`
	VerificationStatus             int     `gorm:"default:1"`
	VerificationInfo               *string `gorm:"type:longtext"`
	NewEmailVerificiationCode      *string `gorm:"column:new_email_verificiation_code;type:text"`
	Password                       *string `gorm:"size:191"`
	RememberToken                  *string `gorm:"size:100"`
	DeviceToken                    *string `gorm:"size:255"`
	Avatar                         *string `gorm:"size:256"`
	AvatarOriginal                 *string `gorm:"size:256"`
	Address                        *string `gorm:"size:300"`
	Country                        *string `gorm:"size:30"`
	State                          *string `gorm:"size:30"`
	City                           *string `gorm:"size:30"`
	PostalCode                     *string `gorm:"size:20"`
	Phone                          *string `gorm:"size:20"`
	OtpActivationPurchaseCodWallet *int    `gorm:"default:0"`
	OtpAlertSeen                   *int    `gorm:"default:0"`
	Balance                        float64 `gorm:"type:decimal(20,2);default:0"`
	Banned                         int     `gorm:"default:0"`
	IsSuspicious                   *int    `gorm:"default:0"`
	ReferralCode                   *string `gorm:"size:255"`
	CustomerPackageID              *uint
	RemainingUploads               *int `gorm:"default:0"`
	CreatedAt                      *time.Time
	UpdatedAt                      *time.Time

	Products  []Product `gorm:"foreignKey:UserID"`
	Orders    []Order   `gorm:"foreignKey:UserID"`
	Carts     []Cart    `gorm:"foreignKey:UserID"`
	Reviews   []Review  `gorm:"foreignKey:UserID"`
	Addresses []Address `gorm:"foreignKey:UserID"`
	Wallets   []Wallet  `gorm:"foreignKey:UserID"`
	Shop      *Shop     `gorm:"foreignKey:UserID"`
	Seller    *Seller   `gorm:"foreignKey:UserID"`
	Staff     *Staff    `gorm:"foreignKey:UserID"`
}

func (User) TableName() string { return "users" }

type Seller struct {
	ID                   uint    `gorm:"primaryKey"`
	UserID               uint    `gorm:"uniqueIndex"`
	Rating               float64 `gorm:"type:decimal(3,2);default:0"`
	NumOfReviews         int     `gorm:"default:0"`
	NumOfSale            int     `gorm:"default:0"`
	VerificationStatus   int     `gorm:"default:0"`
	VerificationInfo     *string `gorm:"type:longtext"`
	CashOnDeliveryStatus int     `gorm:"default:0"`
	AdminToPay           float64 `gorm:"type:decimal(20,2);default:0"`
	BankName             *string `gorm:"size:255"`
	BankAccName          *string `gorm:"size:200"`
	BankAccNo            *string `gorm:"size:50"`
	BankRoutingNo        *int
	BankPaymentStatus    int `gorm:"default:0"`
	CreatedAt            time.Time
	UpdatedAt            time.Time

	User *User `gorm:"foreignKey:UserID"`
}

func (Seller) TableName() string { return "sellers" }

type Staff struct {
	ID        uint `gorm:"primaryKey"`
	UserID    uint
	RoleID    uint
	CreatedAt time.Time
	UpdatedAt time.Time

	User *User `gorm:"foreignKey:UserID"`
}

func (Staff) TableName() string { return "staff" }

type Address struct {
	ID         uint    `gorm:"primaryKey"`
	UserID     uint
	Name       *string `gorm:"size:255"`
	Email      *string `gorm:"size:255"`
	Address    *string `gorm:"size:255"`
	City       *string `gorm:"size:255"`
	State      *string `gorm:"size:255"`
	Country    *string `gorm:"size:255"`
	CountryID  *uint
	StateID    *uint
	CityID     *uint
	AreaID     *uint
	Longitude  *float64 `gorm:"type:decimal(17,15)"`
	Latitude   *float64 `gorm:"type:decimal(17,15)"`
	PostalCode *string  `gorm:"size:255"`
	Phone      *string  `gorm:"size:255"`
	SetDefault int      `gorm:"default:0"`
	SetBilling *string  `gorm:"size:20"`
	CreatedAt  time.Time
	UpdatedAt  time.Time

	User *User `gorm:"foreignKey:UserID"`
}

func (Address) TableName() string { return "addresses" }

type Wallet struct {
	ID             uint `gorm:"primaryKey"`
	UserID         uint
	AddedBy        *string `gorm:"size:100;default:customer"`
	Amount         float64 `gorm:"type:decimal(20,2)"`
	PaymentMethod  *string `gorm:"size:255"`
	PaymentDetails *string `gorm:"type:longtext"`
	CreatedAt      time.Time
	UpdatedAt      time.Time

	User *User `gorm:"foreignKey:UserID"`
}

func (Wallet) TableName() string { return "wallets" }

type PasswordReset struct {
	Email     string `gorm:"primaryKey;size:191"`
	Token     string `gorm:"size:191"`
	CreatedAt *time.Time
}

func (PasswordReset) TableName() string { return "password_resets" }

type RegistrationVerificationCode struct {
	ID         uint    `gorm:"primaryKey"`
	Email      *string `gorm:"size:191;uniqueIndex"`
	Phone      *string `gorm:"size:20;uniqueIndex"`
	Code       string  `gorm:"type:text"`
	IsVerified int     `gorm:"default:0"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (RegistrationVerificationCode) TableName() string { return "registration_verification_codes" }

type PersonalAccessToken struct {
	ID            uint64 `gorm:"primaryKey"`
	TokenableType string `gorm:"size:191"`
	TokenableID   uint64
	Name          string  `gorm:"size:191"`
	Token         string  `gorm:"size:64;uniqueIndex"`
	Abilities     *string `gorm:"type:text"`
	LastUsedAt    *time.Time
	ExpiresAt     *time.Time
	CreatedAt     *time.Time
	UpdatedAt     *time.Time
}

func (PersonalAccessToken) TableName() string { return "personal_access_tokens" }
