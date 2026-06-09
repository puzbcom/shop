package frontend

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"mall/internal/models"
	"mall/internal/view"
)

type AuthHandler struct {
	DB     *gorm.DB
	Engine *view.Engine
}

func (h *AuthHandler) navCats(c *gin.Context) []models.Category {
	return navTranslatedCats(h.DB, c)
}

// GET /login
func (h *AuthHandler) LoginForm(c *gin.Context) {
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/login", gin.H{
		"Categories": h.navCats(c),
		"Error":      c.Query("error"),
		"Banned":     c.Query("banned"),
	})
}

// POST /login
func (h *AuthHandler) Login(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")

	if email == "" || password == "" {
		h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/login", gin.H{
			"Categories": h.navCats(c),
			"FormError":  "Email and password are required.",
			"Email":      email,
		})
		return
	}

	var user models.User
	if err := h.DB.Where("email = ?", email).First(&user).Error; err != nil {
		h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/login", gin.H{
			"Categories": h.navCats(c),
			"FormError":  "Invalid email or password.",
			"Email":      email,
		})
		return
	}

	if user.Password == nil {
		h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/login", gin.H{
			"Categories": h.navCats(c),
			"FormError":  "Invalid email or password.",
			"Email":      email,
		})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(*user.Password), []byte(password)); err != nil {
		h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/login", gin.H{
			"Categories": h.navCats(c),
			"FormError":  "Invalid email or password.",
			"Email":      email,
		})
		return
	}

	// Reject banned users before granting a session.
	if user.Banned == 1 {
		c.Redirect(http.StatusFound, "/login?banned=1")
		return
	}

	// Rotate the session to prevent session fixation: clear all previous
	// session data, then write the new authenticated user ID.
	sess := sessions.Default(c)
	sess.Clear()
	sess.Set("user_id", user.ID)
	_ = sess.Save()

	redirect := c.Query("next")
	// Only allow same-origin relative redirects (must start with "/" but not "//").
	if !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") {
		switch user.UserType {
		case "super_admin":
			redirect = "/super-admin/theme"
		case "admin", "operation", "finance":
			redirect = "/admin"
		case "seller":
			redirect = "/seller/dashboard"
		case "delivery_boy":
			redirect = "/delivery/dashboard"
		default:
			redirect = "/dashboard"
		}
	}
	c.Redirect(http.StatusFound, redirect)
}

// GET /register
func (h *AuthHandler) RegisterForm(c *gin.Context) {
	h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/register", gin.H{
		"Categories": h.navCats(c),
	})
}

// POST /register
func (h *AuthHandler) Register(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")
	confirm := c.PostForm("password_confirmation")

	renderErr := func(msg string) {
		h.Engine.Render(c, http.StatusOK, h.Engine.Theme(), "frontend/register", gin.H{
			"Categories": h.navCats(c),
			"FormError":  msg,
			"Name":       name,
			"Email":      email,
		})
	}

	if name == "" || email == "" || password == "" {
		renderErr("All fields are required.")
		return
	}
	if password != confirm {
		renderErr("Passwords do not match.")
		return
	}
	if len(password) < 8 {
		renderErr("Password must be at least 8 characters.")
		return
	}

	var existing models.User
	if err := h.DB.Where("email = ?", email).First(&existing).Error; err == nil {
		renderErr("An account with that email already exists.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		renderErr("Registration failed. Please try again.")
		return
	}

	hashStr := string(hash)
	user := models.User{
		Name:     name,
		Email:    &email,
		Password: &hashStr,
		UserType: "customer",
	}
	if err := h.DB.Create(&user).Error; err != nil {
		renderErr("Registration failed. Please try again.")
		return
	}

	sess := sessions.Default(c)

	// Credit affiliate if a referral code was stored in session — read before Clear().
	affiliateRef, _ := sess.Get("affiliate_ref").(string)

	// Rotate the session to prevent session fixation.
	sess.Clear()
	sess.Set("user_id", user.ID)
	_ = sess.Save()

	if affiliateRef != "" {
		var referrer models.User
		if h.DB.Where("referral_code = ?", affiliateRef).First(&referrer).Error == nil && referrer.ID != user.ID {
			h.DB.Create(&models.AffiliateLog{UserID: referrer.ID, Type: "signup", ReferredUID: &user.ID})
		}
	}

	c.Redirect(http.StatusFound, "/dashboard")
}

// GET /logout
func (h *AuthHandler) Logout(c *gin.Context) {
	sess := sessions.Default(c)
	sess.Clear()
	_ = sess.Save()
	c.Redirect(http.StatusFound, "/")
}

// guestCartID returns (or creates) a temp cart ID stored in the session.
func guestCartID(c *gin.Context) string {
	sess := sessions.Default(c)
	if id, ok := sess.Get("cart_temp_id").(string); ok && id != "" {
		return id
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	sess.Set("cart_temp_id", id)
	_ = sess.Save()
	return id
}
