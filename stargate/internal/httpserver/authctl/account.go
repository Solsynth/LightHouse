package authctl

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/spell"
)

// ---------------------------------------------------------------------------
// AccountController (registration + validate)
// ---------------------------------------------------------------------------

var (
	nameRegex      = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	emailNoPlusRe  = regexp.MustCompile(`^[^+]+@[^@]+\.[^@]+$`)
	emailAddrRegex = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// accountCreateRequest mirrors AccountCreateRequest.
type accountCreateRequest struct {
	Name             string  `json:"name"`
	Nick             string  `json:"nick"`
	Email            string  `json:"email"`
	Password         string  `json:"password"`
	Language         string  `json:"language"`
	CaptchaToken     string  `json:"captcha_token"`
	AffiliationSpell *string `json:"affiliation_spell"`
}

func (h *handler) createAccount(c *gin.Context) {
	ctx := c.Request.Context()
	var req accountCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}

	// DataAnnotations-equivalent validation (name regex / email '+' rule /
	// password length) mirrors AccountController.AccountCreateRequest.
	fieldErrors := map[string][]string{}
	if len(req.Name) < 2 || len(req.Name) > 256 || !nameRegex.MatchString(req.Name) {
		fieldErrors["name"] = []string{"Name can only contain letters, numbers, underscores, and hyphens."}
	}
	if len(req.Nick) == 0 || len(req.Nick) > 256 {
		fieldErrors["nick"] = []string{"The Nick field is required."}
	}
	if len(req.Email) == 0 || len(req.Email) > 1024 || !emailAddrRegex.MatchString(req.Email) {
		fieldErrors["email"] = []string{"The Email field is not a valid e-mail address."}
	} else if strings.Contains(req.Email, "+") || !emailNoPlusRe.MatchString(req.Email) {
		fieldErrors["email"] = []string{"Email address cannot contain '+' symbol."}
	}
	if len(req.Password) < 4 || len(req.Password) > 128 {
		fieldErrors["password"] = []string{"The field Password must be a string or array type with a minimum length of 4."}
	}
	if len(req.Language) > 32 {
		fieldErrors["language"] = []string{"The field Language must be a string or array type with a maximum length of 32."}
	}
	if h.d.Cfg != nil && h.d.Cfg.CaptchaEnabled() && len(req.CaptchaToken) == 0 {
		fieldErrors["captcha_token"] = []string{"The CaptchaToken field is required."}
	}
	if len(fieldErrors) > 0 {
		c.JSON(http.StatusBadRequest, validationError(fieldErrors))
		return
	}

	valid, err := h.d.Auth.ValidateCaptcha(ctx, req.CaptchaToken)
	if err != nil {
		h.logError("validate captcha", err)
	}
	if !valid {
		c.JSON(http.StatusBadRequest, validationError(map[string][]string{
			"captcha_token": {"Invalid captcha token."},
		}))
		return
	}

	ip := middleware.ClientIP(c.Request)
	region := h.d.Geo.GetCountryCodeFromIp(ip)
	if region == "" {
		region = "us"
	}
	language := req.Language
	if language == "" {
		language = "en-us"
	}

	taken, err := h.d.Store.CheckAccountNameTaken(ctx, req.Name)
	if err != nil {
		h.logError("check account name", err)
	}
	if taken {
		c.JSON(http.StatusBadRequest, badRequestDetail("Failed to create account.", "Account name has already been taken."))
		return
	}
	emailUsed, err := h.d.Store.CheckEmailUsed(ctx, req.Email)
	if err != nil {
		h.logError("check email", err)
	}
	if emailUsed {
		c.JSON(http.StatusBadRequest, badRequestDetail("Failed to create account.", "Email has already been used."))
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusBadRequest, badRequestDetail("Failed to create account.", err.Error()))
		return
	}

	now := time.Now().UTC()
	account := &model.Account{
		Id:        uuid.NewString(),
		Name:      req.Name,
		Nick:      req.Nick,
		Language:  language,
		Region:    region,
		CreatedAt: model.NewTime(now),
		UpdatedAt: model.NewTime(now),
	}
	if err := h.d.Store.CreateAccountWithRegistration(ctx, account, req.Email, passwordHash); err != nil {
		h.logError("create account", err)
		c.JSON(http.StatusBadRequest, badRequestDetail("Failed to create account.", err.Error()))
		return
	}
	affiliationSpell := ""
	if req.AffiliationSpell != nil {
		affiliationSpell = *req.AffiliationSpell
	}
	h.afterRegistration(ctx, account, req.Email, affiliationSpell)
	// The C# returns the in-memory entity whose Contacts navigation holds the
	// freshly created email contact.
	account.Contacts = []model.Contact{{
		Id:        uuid.NewString(),
		Type:      int(model.ContactTypeEmail),
		IsPrimary: true,
		Content:   req.Email,
		AccountId: account.Id,
		CreatedAt: model.NewTime(now),
		UpdatedAt: model.NewTime(now),
	}}
	c.JSON(http.StatusOK, account)
}

// afterRegistration ports the Passport AccountCreatedEvent consumer into the
// registration request: it consumes the affiliation (registration-invite)
// spell, creates + emails the contact-verification magic spell, and sends the
// welcome email. The C# runs these on the event bus asynchronously, so every
// failure is logged here without failing the registration response.
func (h *handler) afterRegistration(ctx context.Context, account *model.Account, email, affiliationSpell string) {
	contact, err := h.d.Store.GetEmailContact(ctx, account.Id)
	if err != nil || contact == nil {
		h.logError("load registration contact for spell", err)
		return
	}

	if affiliationSpell != "" {
		if consumed, skipsTests, err := h.d.Spells.ConsumeRegistrationInvite(ctx, affiliationSpell, account.Id); err != nil {
			h.logError("consume affiliation spell", err)
		} else if consumed && skipsTests {
			// The C# calls TestService.TryActivateAccount for invites that
			// skip entry tests; that check still requires a verified contact,
			// which a fresh registration cannot have, so it no-ops here.
			// Activation happens when the contact-verification spell is
			// applied (entry-test logic stays in Passport).
			if h.d.Log != nil {
				h.d.Log.Info("affiliation invite consumed; activation deferred to contact verification", "account_id", account.Id)
			}
		}
	}

	spell, err := h.d.Spells.CreateMagicSpell(ctx, account.Id, model.MagicSpellTypeContactVerification, map[string]any{
		"contact_method": email,
		"contact_id":     contact.Id,
	}, spell.CreateOptions{PreventRepeat: true})
	if err != nil {
		h.logError("create contact verification spell", err)
		return
	}
	if err := h.d.Spells.NotifyMagicSpell(ctx, spell, true); err != nil {
		h.logError("notify contact verification spell", err)
	}
	if err := h.d.Spells.SendWelcomeEmail(ctx, account, email); err != nil {
		h.logError("send welcome email", err)
	}
}

// accountCreateValidateRequest mirrors AccountCreateValidateRequest.
type accountCreateValidateRequest struct {
	Name  *string `json:"name"`
	Email *string `json:"email"`
}

func (h *handler) validateCreateAccount(c *gin.Context) {
	ctx := c.Request.Context()
	var req accountCreateValidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	if req.Name != nil {
		taken, err := h.d.Store.CheckAccountNameTaken(ctx, *req.Name)
		if err != nil {
			h.logError("check account name", err)
		}
		if taken {
			c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_ACCOUNT_NAME_TAKEN", "Account name has already been taken."))
			return
		}
	}
	if req.Email != nil {
		used, err := h.d.Store.CheckEmailUsed(ctx, *req.Email)
		if err != nil {
			h.logError("check email", err)
		}
		if used {
			c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_ACCOUNT_EMAIL_TAKEN", "Email has already been used."))
			return
		}
	}
	c.JSON(http.StatusOK, "Everything seems good.")
}

// ---------------------------------------------------------------------------
// AccountCurrentController (only pin-status; GET/PATCH /me are profilectl's)
// ---------------------------------------------------------------------------

func (h *handler) getPinStatus(c *gin.Context) {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.New("UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	hasPin, err := h.d.Store.HasEnabledFactor(c.Request.Context(), user.Id, model.AuthFactorTypePinCode)
	if err != nil {
		h.logError("check pin factor", err)
	}
	c.JSON(http.StatusOK, gin.H{
		"has_pin":             hasPin,
		"validation_required": hasPin,
	})
}

// badRequestDetail builds the C#-shaped 400 error with a detail message.
func badRequestDetail(message, detail string) *errs.ApiError {
	return &errs.ApiError{
		Code:    "BAD_REQUEST",
		Message: message,
		Detail:  &detail,
		Status:  http.StatusBadRequest,
	}
}
