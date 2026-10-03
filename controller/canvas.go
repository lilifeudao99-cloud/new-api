package controller

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	canvasSSOTicketTTL = 60 * time.Second
	canvasSSOTicketKey = "canvas:sso:ticket:"
)

func canvasPublicURL() string {
	value := strings.TrimSpace(os.Getenv("CANVAS_PUBLIC_URL"))
	if value == "" {
		value = "https://canvas.ailili.chat"
	}
	return strings.TrimRight(value, "/")
}

func canvasIntegrationSecret() string {
	return strings.TrimSpace(os.Getenv("CANVAS_INTEGRATION_SECRET"))
}

func requireCanvasIntegrationSecret(c *gin.Context) bool {
	configured := canvasIntegrationSecret()
	provided := strings.TrimSpace(c.GetHeader("X-Canvas-Integration-Secret"))
	if provided == "" {
		provided = strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	}
	if configured == "" || provided == "" || subtle.ConstantTimeCompare([]byte(configured), []byte(provided)) != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "CANVAS_INTEGRATION_UNAUTHORIZED"})
		return false
	}
	return true
}

func GetCanvasSSOLaunch(c *gin.Context) {
	if canvasIntegrationSecret() == "" {
		c.JSON(http.StatusNotImplemented, gin.H{"success": false, "code": "CANVAS_INTEGRATION_NOT_CONFIGURED"})
		return
	}
	userID := c.GetInt("id")
	if userID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_INVALID"})
		return
	}
	if !common.RedisEnabled || common.RDB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "CANVAS_SSO_STORAGE_UNAVAILABLE"})
		return
	}
	ticket, err := common.GenerateRandomCharsKey(32)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "code": "CANVAS_SSO_TICKET_FAILED"})
		return
	}
	if err := common.RedisSet(canvasTicketStorageKey(ticket), strconv.Itoa(userID), canvasSSOTicketTTL); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "CANVAS_SSO_STORAGE_UNAVAILABLE"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":      true,
		"code":         ticket,
		"expires_in":   int(canvasSSOTicketTTL / time.Second),
		"redirect_url": canvasCallbackURL(ticket),
	})
}

func canvasTicketStorageKey(ticket string) string {
	digest := sha256.Sum256([]byte(ticket))
	return canvasSSOTicketKey + hex.EncodeToString(digest[:])
}

func canvasCallbackURL(ticket string) string {
	base := canvasPublicURL() + "/auth/canvas/callback"
	parsed, err := url.Parse(base)
	if err != nil {
		return base + "?code=" + url.QueryEscape(ticket)
	}
	query := parsed.Query()
	query.Set("code", ticket)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func ExchangeCanvasSSOTicket(c *gin.Context) {
	if !requireCanvasIntegrationSecret(c) {
		return
	}
	var request struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": "CANVAS_SSO_TICKET_INVALID"})
		return
	}
	if !common.RedisEnabled || common.RDB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "CANVAS_SSO_STORAGE_UNAVAILABLE"})
		return
	}
	value, err := common.RedisGetDel(canvasTicketStorageKey(strings.TrimSpace(request.Code)))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "code": "CANVAS_SSO_TICKET_INVALID"})
		return
	}
	userID, err := strconv.Atoi(value)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "code": "CANVAS_SSO_TICKET_INVALID"})
		return
	}
	user, err := model.GetUserById(userID, false)
	if err != nil || user.Status != common.UserStatusEnabled {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_INVALID"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"user": gin.H{
			"id":           user.Id,
			"username":     user.Username,
			"display_name": user.DisplayName,
			"email":        user.Email,
		},
	})
}

// GetCanvasIntegrationProfile returns the ailili account identity and current
// balance for the server-to-server TapCanvas integration. The integration
// secret is required so the browser never receives a dashboard credential.
func GetCanvasIntegrationProfile(c *gin.Context) {
	if !requireCanvasIntegrationSecret(c) {
		return
	}
	var request struct {
		UserID int `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.UserID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": "CANVAS_PROFILE_REQUEST_INVALID"})
		return
	}
	user, err := model.GetUserById(request.UserID, false)
	if err != nil || user.Status != common.UserStatusEnabled {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_INVALID"})
		return
	}

	amount := float64(user.Quota) / common.QuotaPerUnit
	currency := "USD"
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount *= operation_setting.USDExchangeRate
		currency = "CNY"
	case operation_setting.QuotaDisplayTypeTokens:
		amount = float64(user.Quota)
		currency = "TOKENS"
	case operation_setting.QuotaDisplayTypeCustom:
		currency = "CUSTOM"
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"user": gin.H{
			"id":           user.Id,
			"username":     user.Username,
			"display_name": user.DisplayName,
			"email":        user.Email,
		},
		"balance": gin.H{
			"quota":           user.Quota,
			"used_quota":      user.UsedQuota,
			"available_quota": user.Quota,
			"amount":          amount,
			"currency":        currency,
			"display_type":    operation_setting.GetQuotaDisplayType(),
			"quota_per_unit":  common.QuotaPerUnit,
		},
	})
}

func EnsureCanvasAPIKey(c *gin.Context) {
	if !requireCanvasIntegrationSecret(c) {
		return
	}
	var request struct {
		UserID  int    `json:"user_id"`
		Purpose string `json:"purpose"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.UserID <= 0 || strings.TrimSpace(request.Purpose) != "canvas" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": "CANVAS_KEY_REQUEST_INVALID"})
		return
	}
	token, created, err := model.EnsureCanvasIntegrationToken(request.UserID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, model.ErrTokenInvalid) {
			c.JSON(http.StatusConflict, gin.H{"success": false, "code": "CANVAS_KEY_UNAVAILABLE"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"success": false, "code": "CANVAS_KEY_PROVISIONING_FAILED"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"created": created,
		"user_id": request.UserID,
		"purpose": "canvas",
		"api_key": token.Key,
	})
}
