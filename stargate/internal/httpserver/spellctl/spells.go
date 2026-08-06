// Package spellctl ports Passport's MagicSpellController: fetch, apply and
// resend magic spells (contact-verification links emailed at registration).
package spellctl

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps bundles the services spellctl uses.
type Deps struct {
	Store *store.Store
	Spell *spell.Service
	Log   *slog.Logger
}

// Register mounts the /api/spells routes (Passport MagicSpellController).
func Register(api *gin.RouterGroup, d Deps) {
	spells := api.Group("/spells")
	{
		spells.POST("/contact-verification/resend", middleware.RequireAuth(), resendContactVerification(d))
		spells.GET("/:word", getMagicSpell(d))
		spells.POST("/:word/apply", applyMagicSpell(d))
		spells.POST("/:word/resend", resendMagicSpell(d))
	}
}

// spellNotFound mirrors the C# PASSPORT_SPELL_NOT_FOUND 404 payload.
func spellNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PASSPORT_SPELL_NOT_FOUND", "Magic spell not found.", http.StatusNotFound))
}

// getMagicSpell mirrors MagicSpellController.GetMagicSpell: the spell is
// returned without its secret word, with the account hydrated when present.
func getMagicSpell(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		spell, err := d.Store.GetMagicSpellByWord(c.Request.Context(), c.Param("word"))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				spellNotFound(c)
				return
			}
			serverError(c, d, err)
			return
		}
		if spell.AccountId != "" {
			if accountID, err := uuid.Parse(spell.AccountId); err == nil {
				if account, err := d.Store.GetAccountByID(c.Request.Context(), accountID); err == nil {
					spell.Account = account
				}
			}
		}
		c.JSON(http.StatusOK, spell)
	}
}

// applyMagicSpell mirrors MagicSpellController.ApplyMagicSpell: applying a
// contact-verification spell verifies the contact and consumes the spell.
// Errors surface as a 400 PASSPORT_SPELL_APPLY_FAILED with the C# message.
func applyMagicSpell(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		spell, err := d.Store.GetMagicSpellByWord(c.Request.Context(), c.Param("word"))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				spellNotFound(c)
				return
			}
			serverError(c, d, err)
			return
		}
		var req magicSpellApplyRequest
		_ = c.ShouldBindJSON(&req)

		var applyErr error
		if spell.Type == model.MagicSpellTypeAuthPasswordReset && req.NewPassword != nil {
			applyErr = d.Spell.ApplyPasswordReset(c.Request.Context(), spell, *req.NewPassword)
		} else {
			applyErr = d.Spell.ApplyMagicSpell(c.Request.Context(), spell)
		}
		if applyErr != nil {
			c.JSON(http.StatusBadRequest, errs.New("PASSPORT_SPELL_APPLY_FAILED", applyErr.Error(), http.StatusBadRequest))
			return
		}
		c.Status(http.StatusOK)
	}
}

// magicSpellApplyRequest mirrors MagicSpellApplyRequest.
type magicSpellApplyRequest struct {
	NewPassword *string `json:"new_password"`
}

// resendMagicSpell mirrors MagicSpellController.ResendMagicSpell (public,
// resend by spell id, bypassVerify).
func resendMagicSpell(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("word"))
		if err != nil {
			spellNotFound(c)
			return
		}
		spell, err := d.Store.GetMagicSpellByID(c.Request.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				spellNotFound(c)
				return
			}
			serverError(c, d, err)
			return
		}
		if err := d.Spell.ResendMagicSpell(c.Request.Context(), spell, true); err != nil {
			serverError(c, d, err)
			return
		}
		c.Status(http.StatusOK)
	}
}

// resendContactVerification mirrors
// MagicSpellController.ResendActivationMagicSpell (authenticated, resend the
// current user's contact-verification spell).
func resendContactVerification(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := middleware.CurrentUser(c.Request.Context())
		if user == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		spell, err := d.Store.GetContactVerificationSpell(c.Request.Context(), user.Id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				c.JSON(http.StatusBadRequest, errs.New("PASSPORT_SPELL_NOT_FOUND",
					"Unable to find contact verification spell.", http.StatusBadRequest))
				return
			}
			serverError(c, d, err)
			return
		}
		if err := d.Spell.ResendMagicSpell(c.Request.Context(), spell, true); err != nil {
			serverError(c, d, err)
			return
		}
		c.Status(http.StatusOK)
	}
}

func serverError(c *gin.Context, d Deps, err error) {
	if d.Log != nil {
		d.Log.Error("spell request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
}
