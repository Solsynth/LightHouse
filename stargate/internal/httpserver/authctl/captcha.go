package authctl

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/go/pkg/errs"
)

// CaptchaController port (POST /api/auth/captcha/verify + GET
// /api/auth/captcha). The raw-token POST /api/auth/captcha lives in auth.go
// (AuthController.ValidateCaptcha).

type captchaVerifyRequest struct {
	Token string `json:"token"`
}

func (h *handler) captchaVerify(c *gin.Context) {
	ctx := c.Request.Context()
	var req captchaVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_CAPTCHA_TOKEN_REQUIRED", "Token is required."))
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_CAPTCHA_TOKEN_REQUIRED", "Token is required."))
		return
	}
	valid, err := h.d.Auth.ValidateCaptcha(ctx, req.Token)
	if err != nil {
		h.logError("validate captcha", err)
	}
	if !valid {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_CAPTCHA_INVALID", "Invalid captcha."))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *handler) captchaGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"provider": h.d.Cfg.Captcha.Provider,
		"apiKey":   h.d.Cfg.Captcha.APIKey,
	})
}
