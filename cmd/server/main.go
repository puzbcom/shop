package main

import (
	"encoding/gob"
	"log"
	"net/http"
	"path/filepath"
	"runtime"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mall/handlers/admin"
	"mall/handlers/delivery"
	"mall/handlers/frontend"
	"mall/handlers/payment"
	"mall/handlers/seller"
	"mall/handlers/webhook"
	"mall/internal/config"
	"mall/internal/database"
	"mall/internal/middleware"
	"mall/internal/models"
	"mall/internal/services/auction"
	"mall/internal/services/exchangerate"
	"mall/internal/services/i18n"
	"mall/internal/services/settings"
	"mall/internal/view"
)

func init() {
	// gorilla/securecookie uses gob to serialize session values stored as interface{}.
	// Basic types wrapped in interface{} must be registered explicitly.
	gob.Register(uint(0))
	gob.Register("")
}

func main() {
	cfg := config.Load()

	if !cfg.IsDev() {
		gin.SetMode(gin.ReleaseMode)
	}

	db, err := database.Open(cfg)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	log.Printf("connected to database %q", cfg.DBName)

	// Ensure new tables exist (idempotent).
	if err := db.AutoMigrate(&models.BlogTranslation{}, &models.ProductTranslation{}, &models.PageTranslation{}, &models.AffiliateLog{}, &models.AffiliateWithdraw{}, &models.SEOMeta{}, &models.MenuItem{}, &models.Newsletter{}, &models.SupplierOrder{}); err != nil {
		log.Printf("warn: auto-migrate: %v", err)
	}
	// addColumn adds a column only when missing. MySQL lacks ADD COLUMN IF NOT EXISTS
	// (it's MariaDB-only), so guard with information_schema. table/column/ddl are all
	// code-controlled constants — no user input — so the string-built SQL is safe.
	addColumn := func(table, column, ddl string) {
		var n int64
		db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = ? AND table_name = ? AND column_name = ?",
			db.Migrator().CurrentDatabase(), table, column).Scan(&n)
		if n > 0 {
			return
		}
		if err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + ddl).Error; err != nil {
			log.Printf("warn: add column %s.%s: %v", table, column, err)
		}
	}
	addColumn("products", "thumbnail_img_alt", "thumbnail_img_alt VARCHAR(255) NULL")
	addColumn("products", "source_1688_url", "source_1688_url VARCHAR(500) NULL")
	// Per-variant featured image path.
	addColumn("product_stocks", "variant_image", "variant_image VARCHAR(2000) NULL")
	// Ensure robots_index and robots_follow default to 1 in MySQL.
	db.Exec("ALTER TABLE seo_meta MODIFY COLUMN robots_index INT NOT NULL DEFAULT 1")
	db.Exec("ALTER TABLE seo_meta MODIFY COLUMN robots_follow INT NOT NULL DEFAULT 1")
	// Auction columns and table.
	addColumn("products", "auction_start_price", "auction_start_price DECIMAL(20,2) NULL")
	addColumn("products", "auction_min_bid_increment", "auction_min_bid_increment DECIMAL(20,2) NULL")
	addColumn("products", "auction_end_at", "auction_end_at DATETIME NULL")
	if err := db.AutoMigrate(&models.AuctionBid{}, &models.AuctionWinner{}); err != nil {
		log.Printf("warn: auction_bids migrate: %v", err)
	}

	// Address extra fields (name, email, city, state, country).
	if err := db.AutoMigrate(&models.Address{}); err != nil {
		log.Printf("warn: address migrate: %v", err)
	}
	// Refund requests table.
	if err := db.AutoMigrate(&models.RefundRequest{}); err != nil {
		log.Printf("warn: refund_requests migrate: %v", err)
	}
	// Support ticket columns — use AutoMigrate so it works on MySQL 5.7 and MariaDB too.
	if err := db.AutoMigrate(&models.Ticket{}, &models.TicketReply{}); err != nil {
		log.Printf("warn: ticket migrate: %v", err)
	}

	// Seed RBAC permissions and default role grants (idempotent).
	seedRBAC(db)

	// Core services
	st := settings.New(db)
	i18nSvc := i18n.New(st.Get("default_language", "en"))

	// Set default theme only on first run (no value in DB yet).
	// Once set, the theme is frozen at startup and only changeable via super admin + restart.
	if st.Get("homepage_select", "") == "" {
		if err := st.Set("homepage_select", "etsy"); err != nil {
			log.Printf("warn: set homepage_select: %v", err)
		}
	}

	// Seed default logo only if not yet configured
	if st.Get("header_logo", "") == "" {
		if err := st.Set("header_logo", "/static/img/cyshoplogo.webp"); err != nil {
			log.Printf("warn: set header_logo: %v", err)
		}
	}

	// Template engine
	templatesDir := projectRoot() + "/web/templates"
	eng := view.New(db, st, i18nSvc, templatesDir, cfg.IsDev())

	// Session secret from APP_KEY (padded to 32 bytes minimum)
	secret := []byte(cfg.AppKey)
	for len(secret) < 32 {
		secret = append(secret, 0)
	}

	r := gin.New()
	r.Use(
		middleware.Recovery(),
		middleware.Logger(),
		middleware.Session(secret, !cfg.IsDev()),
		middleware.GenerateCSRF(),
		middleware.Locale(db, st, i18nSvc),
		middleware.Auth(db),
		middleware.Unbanned(),
		middleware.TrackAffiliate(),
	)

	// Static assets and uploads
	r.Static("/static", projectRoot()+"/web/static")
	r.Static("/uploads", projectRoot()+"/uploads")

	// Sample import file
	r.GET("/sample_products_import.csv", func(c *gin.Context) {
		c.File(projectRoot() + "/sample_products_import.csv")
	})

	// Health check
	r.GET("/healthz", func(c *gin.Context) {
		sqlDB, _ := db.DB()
		if err := sqlDB.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "db": "down"})
			return
		}
		var one int
		db.Raw("SELECT 1").Scan(&one)
		var tables int64
		db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", cfg.DBName).Scan(&tables)
		c.JSON(http.StatusOK, gin.H{"status": "ok", "db": "up", "select1": one, "tables": tables})
	})

	// ── Payment gateway webhooks (public, no CSRF/auth) ──────────────────────
	webhookH := &webhook.Handler{DB: db, Settings: st}
	r.POST("/webhooks/paypal", webhookH.PayPal)
	r.POST("/webhooks/stripe", webhookH.Stripe)
	r.POST("/webhooks/alipay", webhookH.Alipay)

	// ── Frontend routes ──────────────────────────────────────────────────────
	homeH := &frontend.HomeHandler{DB: db, Engine: eng, Settings: st}
	r.GET("/", homeH.Index)

	etsyH := &frontend.EtsyHandler{DB: db, Engine: eng}
	r.GET("/etsy", etsyH.Index)

	productsH := &frontend.ProductsHandler{DB: db, Engine: eng}
	r.GET("/products", productsH.List)
	r.GET("/search", productsH.List)
	r.GET("/product/:slug", productsH.Detail)
	r.POST("/product/:slug/bid", middleware.RequireAuth(), middleware.CSRF(), productsH.Bid)
	r.GET("/category/:slug", productsH.Category)

	brandsH := &frontend.BrandsHandler{DB: db, Engine: eng}
	r.GET("/brands", brandsH.List)
	r.GET("/brand/:slug", brandsH.Detail)

	shopsH := &frontend.ShopsHandler{DB: db, Engine: eng}
	r.GET("/shops", shopsH.List)
	r.GET("/shop/:slug", shopsH.Detail)

	pagesH := &frontend.PagesHandler{DB: db, Engine: eng}
	r.GET("/categories", pagesH.Categories)
	r.GET("/flash-deals/:id", pagesH.FlashDeal)
	r.GET("/contact", pagesH.Contact)
	r.POST("/contact", pagesH.ContactSubmit)
	r.GET("/faq", pagesH.FAQ)
	r.GET("/page/:slug", pagesH.Page)
	r.GET("/blogs", pagesH.Blogs)
	r.GET("/blog/:slug", pagesH.BlogDetail)
	r.GET("/set-language", pagesH.SetLanguage)

	// ── Auth routes ──────────────────────────────────────────────────────────
	authH := &frontend.AuthHandler{DB: db, Engine: eng}
	r.GET("/login", middleware.GuestOnly("/dashboard"), authH.LoginForm)
	r.POST("/login", middleware.GuestOnly("/dashboard"), middleware.CSRF(), authH.Login)
	r.GET("/register", middleware.GuestOnly("/dashboard"), authH.RegisterForm)
	r.POST("/register", middleware.GuestOnly("/dashboard"), middleware.CSRF(), authH.Register)
	r.GET("/logout", authH.Logout)

	// ── Cart routes ───────────────────────────────────────────────────────────
	cartH := &frontend.CartHandler{DB: db, Engine: eng, Settings: st}
	r.GET("/cart", cartH.Show)
	r.POST("/cart/add", middleware.CSRF(), cartH.Add)
	r.POST("/cart/update", middleware.CSRF(), cartH.Update)
	r.POST("/cart/remove/:id", middleware.CSRF(), cartH.Remove)
	r.GET("/checkout", middleware.RequireAuth(), cartH.Checkout)
	r.POST("/checkout", middleware.RequireAuth(), middleware.CSRF(), cartH.PlaceOrder)
	r.POST("/coupon/apply", middleware.RequireAuth(), middleware.CSRF(), cartH.ApplyCoupon)
	r.POST("/coupon/remove", middleware.RequireAuth(), middleware.CSRF(), cartH.RemoveCoupon)
	r.GET("/order-confirmed", middleware.RequireAuth(), cartH.OrderConfirmed)

	// ── Payment routes (require auth; some return JSON) ──────────────────────
	payH := &payment.Handler{DB: db, Engine: eng, Settings: st}
	r.GET("/pay/:id", middleware.RequireAuth(), payH.Show)
	r.POST("/pay/:id/paypal/create", middleware.RequireAuth(), middleware.CSRF(), payH.PayPalCreate)
	r.POST("/pay/:id/paypal/capture", middleware.RequireAuth(), middleware.CSRF(), payH.PayPalCapture)
	r.GET("/pay/:id/stripe/start", middleware.RequireAuth(), payH.StripeStart)
	r.GET("/pay/:id/stripe/return", middleware.RequireAuth(), payH.StripeReturn)
	r.GET("/pay/:id/alipay/start", middleware.RequireAuth(), payH.AlipayStart)
	r.GET("/pay/:id/alipay/return", middleware.RequireAuth(), payH.AlipayReturn)

	// ── Account routes (require auth) ─────────────────────────────────────────
	accountH := &frontend.AccountHandler{DB: db, Engine: eng, Settings: st, UploadDir: cfg.UploadsDir}
	auth := r.Group("/", middleware.RequireAuth())
	{
		auth.GET("/dashboard", accountH.Dashboard)
		auth.GET("/purchase_history", accountH.PurchaseHistory)
		auth.GET("/purchase_history/:id", accountH.PurchaseHistoryDetail)
		auth.POST("/purchase_history/:id/cancel", middleware.CSRF(), accountH.CancelOrder)
		auth.POST("/purchase_history/:id/refund", middleware.CSRF(), accountH.RequestRefund)
		auth.GET("/download/:order_id/:detail_id", accountH.DownloadDigital)
		auth.GET("/auction-wins", accountH.AuctionWins)
		auth.GET("/auction-wins/:id/checkout", accountH.AuctionWinCheckout)
		auth.POST("/auction-wins/:id/checkout", middleware.CSRF(), accountH.AuctionWinPlaceOrder)
		auth.GET("/wishlists", accountH.Wishlists)
		auth.POST("/wishlists/toggle", middleware.CSRF(), accountH.WishlistToggle)
		auth.GET("/wallet", accountH.Wallet)
		auth.GET("/addresses", accountH.Addresses)
		auth.POST("/addresses", middleware.CSRF(), accountH.AddAddress)
		auth.POST("/addresses/:id/update", middleware.CSRF(), accountH.UpdateAddress)
		auth.POST("/addresses/delete/:id", middleware.CSRF(), accountH.DeleteAddress)
		auth.POST("/addresses/default/:id", middleware.CSRF(), accountH.SetDefaultAddress)
		auth.GET("/profile", accountH.Profile)
		auth.POST("/profile", middleware.CSRF(), accountH.UpdateProfile)
		auth.GET("/affiliate", accountH.AffiliateDashboard)
		auth.POST("/affiliate/join", middleware.CSRF(), accountH.AffiliateJoin)
		auth.POST("/affiliate/withdraw", middleware.CSRF(), accountH.AffiliateWithdraw)
		auth.GET("/tickets", accountH.TicketList)
		auth.GET("/tickets/create", accountH.TicketCreate)
		auth.POST("/tickets", middleware.CSRF(), accountH.TicketStore)
		auth.GET("/tickets/:id", accountH.TicketDetail)
		auth.POST("/tickets/:id/reply", middleware.CSRF(), accountH.TicketReply)
	}

	// ── Seller routes (require seller user type) ──────────────────────────────
	sellerH := &seller.Handler{DB: db, Engine: eng}
	sellerGroup := r.Group("/seller", middleware.RequireAuth(), middleware.RequireUserType("seller", "admin"))
	{
		sellerGroup.GET("/dashboard", sellerH.Dashboard)
		sellerGroup.GET("/products", sellerH.ProductList)
		sellerGroup.GET("/products/create", sellerH.ProductCreate)
		sellerGroup.POST("/products", middleware.CSRF(), sellerH.ProductStore)
		sellerGroup.GET("/products/:id/edit", sellerH.ProductEdit)
		sellerGroup.POST("/products/:id", middleware.CSRF(), sellerH.ProductUpdate)
		sellerGroup.POST("/products/:id/delete", middleware.CSRF(), sellerH.ProductDelete)
		sellerGroup.GET("/orders", sellerH.OrderList)
		sellerGroup.GET("/orders/:id", sellerH.OrderDetail)
		sellerGroup.POST("/orders/:id/status", middleware.CSRF(), sellerH.OrderUpdateStatus)
		sellerGroup.GET("/coupons", sellerH.CouponList)
		sellerGroup.GET("/coupons/create", sellerH.CouponCreate)
		sellerGroup.POST("/coupons", middleware.CSRF(), sellerH.CouponStore)
		sellerGroup.POST("/coupons/:id/delete", middleware.CSRF(), sellerH.CouponDelete)
		sellerGroup.GET("/reviews", sellerH.ReviewList)
		sellerGroup.GET("/withdraw-requests", sellerH.WithdrawList)
		sellerGroup.POST("/withdraw-requests", middleware.CSRF(), sellerH.WithdrawStore)
		sellerGroup.GET("/shop/settings", sellerH.ShopSettings)
		sellerGroup.POST("/shop/settings", middleware.CSRF(), sellerH.ShopSettingsUpdate)
		sellerGroup.GET("/profile", sellerH.Profile)
		sellerGroup.POST("/profile", middleware.CSRF(), sellerH.ProfileUpdate)
		sellerGroup.GET("/conversations", sellerH.ConversationList)
		sellerGroup.GET("/conversations/:id", sellerH.ConversationShow)
		sellerGroup.POST("/conversations/:id/send", middleware.CSRF(), sellerH.ConversationSend)
		sellerGroup.GET("/tickets", sellerH.SellerTicketList)
		sellerGroup.GET("/tickets/create", sellerH.SellerTicketCreate)
		sellerGroup.POST("/tickets", middleware.CSRF(), sellerH.SellerTicketStore)
		sellerGroup.GET("/tickets/:id", sellerH.SellerTicketDetail)
		sellerGroup.POST("/tickets/:id/reply", middleware.CSRF(), sellerH.SellerTicketReply)
	}

	// ── Admin routes (require admin user type) ────────────────────────────────
	adminH := &admin.Handler{DB: db, Engine: eng, Settings: st, UploadDir: cfg.UploadsDir, GeminiAPIKey: cfg.GeminiAPIKey, OllamaURL: cfg.OllamaURL, OllamaModel: cfg.OllamaModel}

	// Shared translation AJAX — accessible to any logged-in user (admin, seller, etc.)
	r.POST("/translate", middleware.RequireAuth(), middleware.CSRF(), adminH.AdminTranslate)

	// Accessible to any authenticated user so an impersonating admin (whose
	// session user type is now "seller") can return to their admin account.
	r.GET("/stop-impersonating", middleware.RequireAuth(), adminH.StopImpersonating)

	// All staff roles enter through this group; permission sub-groups restrict further.
	// no-store ensures browsers always fetch fresh HTML (prevents stale JS on admin pages)
	noCache := func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() }
	adminGroup := r.Group("/admin", middleware.RequireAuth(), middleware.RequireUserType("admin", "operation", "finance", "super_admin"), noCache)
	{
		adminGroup.GET("", adminH.Dashboard)
		adminGroup.GET("/dashboard", adminH.Dashboard)
		adminGroup.POST("/translate", middleware.CSRF(), adminH.AdminTranslate)
		adminGroup.GET("/media/files", adminH.MediaFiles)
		adminGroup.POST("/media/upload", middleware.CSRF(), adminH.MediaUpload)
		adminGroup.POST("/ai/typeset", middleware.CSRF(), adminH.AITypeset)

		// Products – view
		pv := adminGroup.Group("", middleware.RequirePermission(db, "products.view"))
		{
			pv.GET("/products", adminH.ProductList)
			pv.GET("/products/pending", adminH.ProductListPending)
			pv.GET("/products/digital", adminH.ProductListDigital)
			pv.GET("/products/classified", adminH.ProductListClassified)
			pv.GET("/products/export", adminH.ProductExport)
			pv.GET("/products/:id/bids", adminH.AuctionBids)
			pv.POST("/products/:id/auction/close", middleware.CSRF(), adminH.AuctionClose)
		}

		// Products – edit
		pe := adminGroup.Group("", middleware.RequirePermission(db, "products.edit"))
		{
			pe.GET("/products/import-1688", adminH.Import1688)
			pe.GET("/products/create", adminH.ProductCreate)
			pe.POST("/products/create", middleware.CSRF(), adminH.ProductStore)
			pe.GET("/products/:id/edit", adminH.ProductEdit)
			pe.POST("/products/:id/update", middleware.CSRF(), adminH.ProductUpdate)
			pe.POST("/products/:id/toggle-publish", middleware.CSRF(), adminH.ProductTogglePublish)
			pe.POST("/products/:id/delete", middleware.CSRF(), adminH.ProductDelete)
			pe.GET("/products/import", adminH.ProductImportForm)
			pe.POST("/products/import", middleware.CSRF(), adminH.ProductImport)
		}

		// Catalog
		cat := adminGroup.Group("", middleware.RequirePermission(db, "catalog.edit"))
		{
			cat.GET("/categories", adminH.CategoryList)
			cat.POST("/categories", middleware.CSRF(), adminH.CategoryStore)
			cat.POST("/categories/:id", middleware.CSRF(), adminH.CategoryUpdate)
			cat.POST("/categories/:id/delete", middleware.CSRF(), adminH.CategoryDelete)
			cat.GET("/brands", adminH.BrandList)
			cat.POST("/brands", middleware.CSRF(), adminH.BrandStore)
			cat.GET("/brands/:id/edit", adminH.BrandEdit)
			cat.POST("/brands/:id", middleware.CSRF(), adminH.BrandUpdate)
			cat.POST("/brands/:id/delete", middleware.CSRF(), adminH.BrandDelete)
			cat.GET("/attributes", adminH.AttributeList)
			cat.POST("/attributes", middleware.CSRF(), adminH.AttributeStore)
			cat.GET("/attributes/:id/edit", adminH.AttributeEdit)
			cat.POST("/attributes/:id", middleware.CSRF(), adminH.AttributeUpdate)
			cat.POST("/attributes/:id/delete", middleware.CSRF(), adminH.AttributeDelete)
			cat.POST("/attributes/:id/values", middleware.CSRF(), adminH.AttributeValueStore)
			cat.POST("/attributes/:id/values/:val_id/delete", middleware.CSRF(), adminH.AttributeValueDelete)
			cat.GET("/colors", adminH.ColorList)
			cat.POST("/colors", middleware.CSRF(), adminH.ColorStore)
			cat.GET("/colors/:id/edit", adminH.ColorEdit)
			cat.POST("/colors/:id", middleware.CSRF(), adminH.ColorUpdate)
			cat.POST("/colors/:id/delete", middleware.CSRF(), adminH.ColorDelete)
			cat.GET("/warranties", adminH.WarrantyList)
			cat.POST("/warranties", middleware.CSRF(), adminH.WarrantyStore)
			cat.GET("/warranties/:id/edit", adminH.WarrantyEdit)
			cat.POST("/warranties/:id", middleware.CSRF(), adminH.WarrantyUpdate)
			cat.POST("/warranties/:id/delete", middleware.CSRF(), adminH.WarrantyDelete)
			cat.GET("/custom-labels", adminH.CustomLabelList)
			cat.POST("/custom-labels", middleware.CSRF(), adminH.CustomLabelStore)
			cat.GET("/custom-labels/:id/edit", adminH.CustomLabelEdit)
			cat.POST("/custom-labels/:id", middleware.CSRF(), adminH.CustomLabelUpdate)
			cat.POST("/custom-labels/:id/delete", middleware.CSRF(), adminH.CustomLabelDelete)
		}

		// Orders – view
		ov := adminGroup.Group("", middleware.RequirePermission(db, "orders.view"))
		{
			ov.GET("/orders", adminH.OrderList)
			ov.GET("/orders/inhouse", adminH.OrderListInhouse)
			ov.GET("/orders/seller", adminH.OrderListSeller)
			ov.GET("/orders/pickup", adminH.OrderListPickup)
			ov.GET("/orders/:id", adminH.OrderDetail)
		}

		// Orders – edit status & supplier fulfillment
		oe := adminGroup.Group("", middleware.RequirePermission(db, "orders.edit"))
		{
			oe.POST("/orders/:id/status", middleware.CSRF(), adminH.OrderUpdateStatus)
			oe.POST("/supplier-orders", middleware.CSRF(), adminH.SupplierOrderSave)
			oe.POST("/supplier-orders/:sid/sync", middleware.CSRF(), adminH.SupplierOrderSyncTracking)
			oe.POST("/supplier-orders/:sid/delete", middleware.CSRF(), adminH.SupplierOrderDelete)
		}

		// Delivery boys
		del := adminGroup.Group("", middleware.RequirePermission(db, "delivery.edit"))
		{
			del.GET("/delivery-boys", adminH.DeliveryBoyList)
			del.GET("/delivery-boys/:id/edit", adminH.DeliveryBoyEdit)
			del.POST("/delivery-boys/:id", middleware.CSRF(), adminH.DeliveryBoyUpdate)
			del.POST("/delivery-boys/:id/ban", middleware.CSRF(), adminH.DeliveryBoyBan)
		}

		// Sellers
		sel := adminGroup.Group("", middleware.RequirePermission(db, "sellers.edit"))
		{
			sel.GET("/sellers", adminH.SellerList)
			sel.GET("/sellers/applied", adminH.SellerListApplied)
			sel.GET("/sellers/ratings", adminH.SellerRatings)
			sel.GET("/sellers/payout-requests", adminH.SellerPayoutList)
			sel.POST("/sellers/payout-requests/:id/approve", middleware.CSRF(), adminH.SellerPayoutApprove)
			sel.POST("/sellers/payout-requests/:id/reject", middleware.CSRF(), adminH.SellerPayoutReject)
			sel.GET("/sellers/:id", adminH.SellerShow)
			sel.POST("/sellers/:id/verify", middleware.CSRF(), adminH.SellerVerify)
			sel.POST("/sellers/:id/ban", middleware.CSRF(), adminH.SellerBan)
			sel.POST("/sellers/:id/login-as", middleware.CSRF(), adminH.SellerLoginAs)
			sel.POST("/sellers/:id/delete", middleware.CSRF(), adminH.SellerDelete)
		}

		// Customers
		cust := adminGroup.Group("", middleware.RequirePermission(db, "customers.view"))
		{
			cust.GET("/customers", adminH.CustomerList)
			cust.POST("/customers/:id/ban", middleware.CSRF(), adminH.CustomerBan)
		}

		// Reviews
		rev := adminGroup.Group("", middleware.RequirePermission(db, "reviews.edit"))
		{
			rev.GET("/reviews", adminH.ReviewList)
			rev.POST("/reviews/:id/approve", middleware.CSRF(), adminH.ReviewApprove)
			rev.POST("/reviews/:id/reject", middleware.CSRF(), adminH.ReviewReject)
			rev.POST("/reviews/:id/delete", middleware.CSRF(), adminH.ReviewDelete)
			rev.POST("/orphan-reviews/:id/relink", middleware.CSRF(), adminH.ReviewRelinkProduct)
		}

		// Refunds
		rfnd := adminGroup.Group("", middleware.RequirePermission(db, "orders.edit"))
		{
			rfnd.GET("/refunds", adminH.RefundList)
			rfnd.POST("/refunds/:id/approve", middleware.CSRF(), adminH.RefundApprove)
			rfnd.POST("/refunds/:id/decline", middleware.CSRF(), adminH.RefundDecline)
		}

		// Support tickets
		sup := adminGroup.Group("", middleware.RequirePermission(db, "support.edit"))
		{
			sup.GET("/tickets", adminH.AdminTicketList)
			sup.GET("/tickets/:id", adminH.AdminTicketDetail)
			sup.POST("/tickets/:id/reply", middleware.CSRF(), adminH.AdminTicketReply)
			sup.POST("/tickets/:id/close", middleware.CSRF(), adminH.AdminTicketClose)
			sup.POST("/tickets/:id/open", middleware.CSRF(), adminH.AdminTicketOpen)
			sup.POST("/tickets/:id/delete", middleware.CSRF(), adminH.AdminTicketDelete)
		}

		// Marketing
		mkt := adminGroup.Group("", middleware.RequirePermission(db, "marketing.edit"))
		{
			mkt.GET("/flash-deals", adminH.FlashDealList)
			mkt.POST("/flash-deals", middleware.CSRF(), adminH.FlashDealStore)
			mkt.POST("/flash-deals/:id/update", middleware.CSRF(), adminH.FlashDealUpdate)
			mkt.POST("/flash-deals/:id/delete", middleware.CSRF(), adminH.FlashDealDelete)
			mkt.GET("/coupons", adminH.CouponList)
			mkt.POST("/coupons", middleware.CSRF(), adminH.CouponStore)
			mkt.POST("/coupons/:id/update", middleware.CSRF(), adminH.CouponUpdate)
			mkt.POST("/coupons/:id/delete", middleware.CSRF(), adminH.CouponDelete)
			mkt.GET("/newsletter", adminH.NewsletterList)
			mkt.POST("/newsletter/send", middleware.CSRF(), adminH.NewsletterSend)
			mkt.POST("/newsletter/preview-count", middleware.CSRF(), adminH.NewsletterPreviewCount)
			mkt.GET("/contacts", adminH.ContactList)
			mkt.GET("/contacts/:id", adminH.ContactDetail)
			mkt.POST("/contacts/:id/reply", middleware.CSRF(), adminH.ContactReply)
			mkt.POST("/contacts/:id/delete", middleware.CSRF(), adminH.ContactDelete)
		}

		// Affiliates & payouts
		aff := adminGroup.Group("", middleware.RequirePermission(db, "affiliates.edit"))
		{
			aff.GET("/affiliates", adminH.AffiliateList)
			aff.POST("/affiliates/:id/toggle", middleware.CSRF(), adminH.AffiliateToggle)
			aff.GET("/affiliate-withdraws", adminH.AffiliateWithdrawList)
			aff.POST("/affiliate-withdraws/:id/approve", middleware.CSRF(), adminH.AffiliateWithdrawApprove)
			aff.POST("/affiliate-withdraws/:id/reject", middleware.CSRF(), adminH.AffiliateWithdrawReject)
		}

		// Reports – finance
		rf := adminGroup.Group("", middleware.RequirePermission(db, "reports.finance"))
		{
			rf.GET("/reports/sales", adminH.SalesReport)
			rf.GET("/reports/earning", adminH.EarningReport)
		}

		// Reports – operations
		ro := adminGroup.Group("", middleware.RequirePermission(db, "reports.ops"))
		{
			ro.GET("/reports/wishlist", adminH.WishlistReport)
			ro.GET("/reports/search", adminH.SearchReport)
		}

		// Content – blogs & pages
		con := adminGroup.Group("", middleware.RequirePermission(db, "content.edit"))
		{
			con.GET("/blogs", adminH.BlogList)
			con.GET("/blogs/create", adminH.BlogCreate)
			con.POST("/blogs", middleware.CSRF(), adminH.BlogStore)
			con.GET("/blogs/:id/edit", adminH.BlogEdit)
			con.POST("/blogs/:id", middleware.CSRF(), adminH.BlogUpdate)
			con.POST("/blogs/:id/delete", middleware.CSRF(), adminH.BlogDelete)
			con.POST("/blogs/:id/upload/:field", middleware.CSRF(), adminH.BlogUploadImage)
			con.GET("/blog-categories", adminH.BlogCategoryList)
			con.POST("/blog-categories", middleware.CSRF(), adminH.BlogCategoryStore)
			con.POST("/blog-categories/:id/delete", middleware.CSRF(), adminH.BlogCategoryDelete)
			con.GET("/menu", adminH.MenuList)
			con.POST("/menu", middleware.CSRF(), adminH.MenuStore)
			con.POST("/menu/:id/update", middleware.CSRF(), adminH.MenuUpdate)
			con.POST("/menu/:id/delete", middleware.CSRF(), adminH.MenuDelete)
			con.POST("/menu/reorder", middleware.CSRF(), adminH.MenuReorder)
			con.GET("/pages", adminH.PageList)
			con.GET("/pages/create", adminH.PageCreate)
			con.POST("/pages", middleware.CSRF(), adminH.PageStore)
			con.GET("/pages/:id/edit", adminH.PageEdit)
			con.POST("/pages/:id", middleware.CSRF(), adminH.PageUpdate)
			con.POST("/pages/:id/delete", middleware.CSRF(), adminH.PageDelete)
		}

		// Users & roles – admin only (users.edit never granted to operation/finance)
		usr := adminGroup.Group("", middleware.RequirePermission(db, "users.edit"))
		{
			usr.GET("/users", adminH.UserList)
			usr.GET("/users/create", adminH.UserCreate)
			usr.POST("/users", middleware.CSRF(), adminH.UserStore)
			usr.GET("/users/:id/edit", adminH.UserEdit)
			usr.POST("/users/:id", middleware.CSRF(), adminH.UserUpdate)
			usr.POST("/users/:id/assign-role", middleware.CSRF(), adminH.UserAssignRole)
			usr.POST("/users/:id/delete", middleware.CSRF(), adminH.UserDelete)
			usr.GET("/roles", adminH.RoleList)
			usr.GET("/roles/create", adminH.RoleCreate)
			usr.POST("/roles", middleware.CSRF(), adminH.RoleStore)
			usr.GET("/roles/:id/edit", adminH.RoleEdit)
			usr.POST("/roles/:id", middleware.CSRF(), adminH.RoleUpdate)
			usr.POST("/roles/:id/delete", middleware.CSRF(), adminH.RoleDelete)
			usr.GET("/role-permissions", adminH.RolePermissions)
			usr.POST("/role-permissions", middleware.CSRF(), adminH.RolePermissionsUpdate)
		}

		// Settings – admin only (settings.edit never granted to operation/finance)
		stt := adminGroup.Group("", middleware.RequirePermission(db, "settings.edit"))
		{
			stt.GET("/business-settings", adminH.BusinessSettings)
			stt.POST("/business-settings", middleware.CSRF(), adminH.BusinessSettingsUpdate)
			stt.POST("/business-settings/logo/upload", middleware.CSRF(), adminH.LogoUpload)
			stt.POST("/business-settings/slides/:slot/upload", middleware.CSRF(), adminH.HeroSlideUpload)
			stt.GET("/payment-settings", adminH.PaymentSettings)
			stt.POST("/payment-settings", middleware.CSRF(), adminH.PaymentSettingsUpdate)
			stt.GET("/social-media", adminH.SocialMediaSettings)
			stt.POST("/social-media", middleware.CSRF(), adminH.SocialMediaSettingsUpdate)
			stt.GET("/smtp-settings", adminH.SMTPSettings)
			stt.POST("/smtp-settings", middleware.CSRF(), adminH.SMTPSettingsUpdate)
			stt.GET("/smtp-test", adminH.SMTPTest)
			stt.GET("/ai-settings", adminH.AISettings)
			stt.POST("/ai-settings", middleware.CSRF(), adminH.AISettingsUpdate)
			stt.GET("/languages", adminH.LanguageList)
			stt.POST("/languages", middleware.CSRF(), adminH.LanguageStore)
			stt.POST("/languages/:id/toggle", middleware.CSRF(), adminH.LanguageToggle)
			stt.POST("/languages/:id/delete", middleware.CSRF(), adminH.LanguageDelete)
		}
	}

	// ── Super Admin routes (super_admin only — theme picker) ─────────────────
	superAdminGroup := r.Group("/super-admin",
		middleware.RequireAuth(),
		middleware.RequireUserType("super_admin"),
		noCache,
	)
	{
		superAdminGroup.GET("/theme",  adminH.ThemePicker)
		superAdminGroup.POST("/theme", middleware.CSRF(), adminH.ThemeUpdate)
	}

	// ── Delivery boy routes ───────────────────────────────────────────────────
	deliveryH := &delivery.Handler{DB: db, Engine: eng}
	deliveryGroup := r.Group("/delivery", middleware.RequireAuth(), middleware.RequireUserType("delivery_boy", "admin"))
	{
		deliveryGroup.GET("/dashboard", deliveryH.Dashboard)
		deliveryGroup.GET("/assigned-orders", deliveryH.AssignedOrders)
		deliveryGroup.GET("/pickup-requests", deliveryH.PickupRequests)
		deliveryGroup.GET("/on-the-way", deliveryH.OnTheWay)
		deliveryGroup.GET("/delivered", deliveryH.DeliveredOrders)
		deliveryGroup.GET("/cancelled", deliveryH.CancelledOrders)
		deliveryGroup.POST("/orders/:id/status", middleware.CSRF(), deliveryH.UpdateStatus)
		deliveryGroup.GET("/collections", deliveryH.Collections)
		deliveryGroup.GET("/earnings", deliveryH.Earnings)
		deliveryGroup.GET("/profile", deliveryH.Profile)
		deliveryGroup.POST("/profile", middleware.CSRF(), deliveryH.ProfileUpdate)
	}

	// Start auction closer — checks every minute for ended auctions and records winners.
	auction.RunScheduler(db)

	// Start weekly exchange-rate updater (CNY → store currency via exchangerate-api.com)
	exchangerate.RunWeeklyScheduler(&exchangerate.Fetcher{DB: db, Settings: st})

	addr := ":" + cfg.AppPort
	log.Printf("listening on http://localhost%s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// projectRoot returns the absolute path to the mall project root (two levels above cmd/server/).
func projectRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.ToSlash(filepath.Join(filepath.Dir(file), "..", ".."))
}

// seedRBAC ensures all permission records, operation/finance roles, and their
// default grants exist. Safe to call on every startup — existing data is never
// overwritten, so admin edits made via the UI persist across restarts.
func seedRBAC(db *gorm.DB) {
	type permDef struct{ name, section string }
	perms := []permDef{
		{"products.view", "Products"},
		{"products.edit", "Products"},
		{"catalog.edit", "Catalog"},
		{"orders.view", "Orders"},
		{"orders.edit", "Orders"},
		{"delivery.edit", "Delivery"},
		{"sellers.edit", "Sellers"},
		{"customers.view", "Customers"},
		{"reviews.edit", "Reviews"},
		{"marketing.edit", "Marketing"},
		{"affiliates.edit", "Finance"},
		{"reports.finance", "Finance"},
		{"reports.ops", "Reports"},
		{"content.edit", "Content"},
		{"users.edit", "Admin"},
		{"settings.edit", "Admin"},
		{"support.edit", "Support"},
	}

	for _, p := range perms {
		sec := p.section
		var perm models.Permission
		db.Where(models.Permission{Name: p.name, GuardName: "web"}).
			FirstOrCreate(&perm, models.Permission{Name: p.name, GuardName: "web", Section: &sec})
	}

	for _, roleName := range []string{"operation", "finance"} {
		var role models.Role
		db.Where(models.Role{Name: roleName, GuardName: "web"}).
			FirstOrCreate(&role, models.Role{Name: roleName, GuardName: "web"})
	}

	// Default grants — only applied when the role currently has no permissions
	// (i.e. first run). After that the admin controls them via /admin/role-permissions.
	type roleInit struct {
		name  string
		perms []string
	}
	defaults := []roleInit{
		{"operation", []string{
			"products.view", "products.edit", "catalog.edit",
			"orders.view", "orders.edit", "delivery.edit", "sellers.edit",
			"customers.view", "reviews.edit", "marketing.edit",
			"reports.ops", "content.edit", "support.edit",
		}},
		{"finance", []string{
			"orders.view", "customers.view",
			"affiliates.edit", "reports.finance",
		}},
	}

	for _, ri := range defaults {
		var role models.Role
		if err := db.Where("name = ?", ri.name).First(&role).Error; err != nil {
			continue
		}
		var count int64
		db.Model(&models.RoleHasPermission{}).Where("role_id = ?", role.ID).Count(&count)
		if count > 0 {
			continue
		}
		for _, permName := range ri.perms {
			var perm models.Permission
			if err := db.Where("name = ?", permName).First(&perm).Error; err != nil {
				continue
			}
			db.Exec("INSERT IGNORE INTO role_has_permissions (role_id, permission_id) VALUES (?, ?)", role.ID, perm.ID)
		}
	}
}
