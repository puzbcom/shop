package models

import "time"

type BusinessSetting struct {
	ID        uint    `gorm:"primaryKey"`
	Type      string  `gorm:"size:255"`
	Value     *string `gorm:"type:longtext"`
	Lang      *string `gorm:"size:30"`
	CreatedAt time.Time
	UpdatedAt *time.Time
}

func (BusinessSetting) TableName() string { return "business_settings" }

type Language struct {
	ID          uint   `gorm:"primaryKey"`
	Name        string `gorm:"size:100"`
	Code        string `gorm:"size:100"`
	AppLangCode string `gorm:"size:255;default:en"`
	RTL         int    `gorm:"default:0"`
	Status      int    `gorm:"default:1"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Language) TableName() string { return "languages" }

type Upload struct {
	ID               uint    `gorm:"primaryKey"`
	FileOriginalName *string `gorm:"size:255"`
	FileName         *string `gorm:"size:255"`
	UserID           *uint
	FileSize         *int
	Extension        *string `gorm:"size:10"`
	Type             *string `gorm:"size:15"`
	ExternalLink     *string `gorm:"size:500"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

func (Upload) TableName() string { return "uploads" }

type TopBanner struct {
	ID        uint    `gorm:"primaryKey"`
	Text      string  `gorm:"type:longtext"`
	Link      *string `gorm:"size:100"`
	Status    int     `gorm:"default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (TopBanner) TableName() string { return "top_banners" }

type Page struct {
	ID              uint    `gorm:"primaryKey"`
	Type            string  `gorm:"size:50"`
	Title           *string `gorm:"size:255"`
	Slug            *string `gorm:"size:255"`
	Content         *string `gorm:"type:longtext"`
	MetaTitle       *string `gorm:"type:text"`
	MetaDescription *string `gorm:"size:1000"`
	Keywords        *string `gorm:"size:1000"`
	MetaImage       *string `gorm:"size:255"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Page) TableName() string { return "pages" }

// PageTranslation holds per-language overrides of Page.Title and Page.Content
// (and the SEO meta fields). The frontend looks up by (page_id, lang) when
// rendering /page/:slug under a non-English locale and falls back to the base
// Page row if no translation exists.
type PageTranslation struct {
	ID              uint   `gorm:"primaryKey"`
	PageID          uint   `gorm:"index:idx_page_lang,unique;column:page_id"`
	Lang            string `gorm:"size:10;index:idx_page_lang,unique"`
	Title           *string `gorm:"size:255"`
	Content         *string `gorm:"type:longtext"`
	MetaTitle       *string `gorm:"type:text"`
	MetaDescription *string `gorm:"size:1000"`
	Keywords        *string `gorm:"size:1000"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (PageTranslation) TableName() string { return "page_translations" }

type MenuItem struct {
	ID        uint   `gorm:"primaryKey"`
	Label     string `gorm:"size:255"`
	URL       string `gorm:"size:500"`
	SortOrder int    `gorm:"default:0"`
	NewTab    int    `gorm:"default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (MenuItem) TableName() string { return "menu_items" }

type Blog struct {
	ID               uint `gorm:"primaryKey"`
	CategoryID       uint
	Title            string  `gorm:"size:255"`
	Slug             string  `gorm:"size:255"`
	ShortDescription *string `gorm:"type:text"`
	Description      *string `gorm:"type:longtext"`
	Banner           *uint
	MetaTitle        *string `gorm:"size:255"`
	MetaImg          *uint
	MetaDescription  *string `gorm:"type:text"`
	MetaKeywords     *string `gorm:"type:text"`
	Status           int     `gorm:"default:1"`
	News             int     `gorm:"default:0"`
	Event            int     `gorm:"default:0"`
	GoingOn          int     `gorm:"default:0"`
	CreatedAt        *time.Time
	UpdatedAt        *time.Time
	DeletedAt        *time.Time

	Translations []BlogTranslation `gorm:"foreignKey:BlogID"`
}

func (Blog) TableName() string { return "blogs" }

// BlogTranslation stores per-language content for a blog post.
// Supported lang codes: en, cn, es, fr, ar, de, jp, ru
type BlogTranslation struct {
	ID               uint      `gorm:"primaryKey"`
	BlogID           uint      `gorm:"uniqueIndex:idx_blog_lang"`
	Lang             string    `gorm:"size:10;uniqueIndex:idx_blog_lang"`
	Title            string    `gorm:"size:255"`
	ShortDescription *string   `gorm:"type:text"`
	Description      *string   `gorm:"type:longtext"`
	MetaTitle        *string   `gorm:"size:255"`
	MetaDescription  *string   `gorm:"type:text"`
	MetaKeywords     *string   `gorm:"size:255"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (BlogTranslation) TableName() string { return "blog_translations" }

type BlogCategory struct {
	ID           uint   `gorm:"primaryKey"`
	CategoryName string `gorm:"size:255"`
	Slug         string `gorm:"size:255"`
	CreatedAt    *time.Time
	UpdatedAt    *time.Time
	DeletedAt    *time.Time
}

func (BlogCategory) TableName() string { return "blog_categories" }

type DynamicPopup struct {
	ID                 uint    `gorm:"primaryKey"`
	Status             int     `gorm:"default:0"`
	Title              string  `gorm:"size:191"`
	Summary            string  `gorm:"type:text"`
	Banner             *string `gorm:"size:191"`
	BtnLink            string  `gorm:"size:191"`
	BtnText            *string `gorm:"size:191"`
	BtnTextColor       *string `gorm:"size:191"`
	BtnBackgroundColor *string `gorm:"size:191"`
	ShowSubscribeForm  *string `gorm:"size:191"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (DynamicPopup) TableName() string { return "dynamic_popups" }

type CustomAlert struct {
	ID              uint    `gorm:"primaryKey"`
	Status          int     `gorm:"default:0"`
	Type            string  `gorm:"size:191"`
	Banner          *string `gorm:"size:191"`
	Link            string  `gorm:"size:191"`
	Description     string  `gorm:"type:text"`
	TextColor       *string `gorm:"size:191"`
	BackgroundColor *string `gorm:"size:191"`
	AutoHide        int     `gorm:"default:0"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (CustomAlert) TableName() string { return "custom_alerts" }

type Subscriber struct {
	ID        uint   `gorm:"primaryKey"`
	Email     string `gorm:"size:50;uniqueIndex"`
	CreatedAt *time.Time
	UpdatedAt time.Time
}

func (Subscriber) TableName() string { return "subscribers" }

// Newsletter stores sent newsletter campaigns.
type Newsletter struct {
	ID           uint      `gorm:"primaryKey"`
	Subject      string    `gorm:"size:500"`
	Body         string    `gorm:"type:longtext"`
	RecipientType string   `gorm:"size:50"`  // subscribers / all_users / custom
	SentCount    int       `gorm:"default:0"`
	FailCount    int       `gorm:"default:0"`
	Status       string    `gorm:"size:20;default:'sent'"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Newsletter) TableName() string { return "newsletters" }

type Contact struct {
	ID        uint    `gorm:"primaryKey"`
	Name      string  `gorm:"size:255"`
	Email     string  `gorm:"size:191"`
	Phone     *string `gorm:"size:20"`
	Content   string  `gorm:"type:text"`
	Image     *string `gorm:"size:191"`
	Reply     *string `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Contact) TableName() string { return "contacts" }

type EmailTemplate struct {
	ID                    uint    `gorm:"primaryKey"`
	Receiver              string  `gorm:"size:20"`
	Identifier            string  `gorm:"size:100"`
	EmailType             string  `gorm:"size:255"`
	Subject               string  `gorm:"size:255"`
	DefaultText           *string `gorm:"type:text"`
	Status                int     `gorm:"default:1"`
	IsStatusChangeable    int     `gorm:"default:1"`
	IsDafaultTextEditable int     `gorm:"default:1"`
	Addon                 *string `gorm:"size:50"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (EmailTemplate) TableName() string { return "email_templates" }

type NotificationType struct {
	ID          uint    `gorm:"primaryKey"`
	UserType    string  `gorm:"size:20;default:customer"`
	Type        string  `gorm:"size:100"`
	Name        string  `gorm:"size:100"`
	Image       *string `gorm:"size:100"`
	DefaultText string  `gorm:"size:255"`
	Status      int     `gorm:"default:1"`
	Addon       *string `gorm:"size:50"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (NotificationType) TableName() string { return "notification_types" }

type FirebaseNotification struct {
	ID         uint    `gorm:"primaryKey"`
	Title      *string `gorm:"size:255"`
	Text       *string `gorm:"type:text"`
	ItemType   string  `gorm:"size:255"`
	ItemTypeID uint
	ReceiverID uint
	IsRead     int `gorm:"default:0"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (FirebaseNotification) TableName() string { return "firebase_notifications" }

type Conversation struct {
	ID             uint `gorm:"primaryKey"`
	SenderID       uint
	ReceiverID     uint
	Title          *string `gorm:"size:1000"`
	SenderViewed   int     `gorm:"default:1"`
	ReceiverViewed int     `gorm:"default:0"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Conversation) TableName() string { return "conversations" }

type Message struct {
	ID             uint `gorm:"primaryKey"`
	ConversationID uint
	UserID         uint
	Message        *string `gorm:"type:text"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Message) TableName() string { return "messages" }

type Ticket struct {
	ID           uint  `gorm:"primaryKey"`
	Code         int64 `gorm:"column:code"`
	UserID       uint
	Subject      string  `gorm:"size:255"`
	Details      *string `gorm:"type:longtext"`
	Files        *string `gorm:"type:longtext"`
	Status       string  `gorm:"size:10;default:pending"`
	Priority     string  `gorm:"size:10;default:medium"`
	Type         string  `gorm:"size:20;default:general"`
	Viewed       int     `gorm:"default:0"`
	ClientViewed int     `gorm:"default:0"`
	CreatedAt    time.Time
	UpdatedAt    time.Time

	User    *User         `gorm:"foreignKey:UserID"`
	Replies []TicketReply `gorm:"foreignKey:TicketID"`
}

func (Ticket) TableName() string { return "tickets" }

type TicketReply struct {
	ID        uint `gorm:"primaryKey"`
	TicketID  uint
	UserID    uint
	Reply     string  `gorm:"type:longtext"`
	Files     *string `gorm:"type:longtext"`
	CreatedAt time.Time
	UpdatedAt time.Time

	User *User `gorm:"foreignKey:UserID"`
}

func (TicketReply) TableName() string { return "ticket_replies" }

type Element struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:255"`
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

func (Element) TableName() string { return "elements" }

type ElementType struct {
	ID        uint `gorm:"primaryKey"`
	ElementID uint
	Name      string `gorm:"size:100"`
	IsDefault int    `gorm:"default:0"`
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

func (ElementType) TableName() string { return "element_types" }
