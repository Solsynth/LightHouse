package realmctl

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/model"
)

// datatypesJSON is the jsonb column type the realm entities store artwork in.
type datatypesJSON = datatypes.JSON

// Request bodies. Field names and lengths mirror the C# request classes so a
// client sending the same JSON keeps working.

type realmRequest struct {
	Slug         *string `json:"slug"`
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	PictureId    *string `json:"picture_id"`
	BackgroundId *string `json:"background_id"`
	IsCommunity  *bool   `json:"is_community"`
	IsPublic     *bool   `json:"is_public"`
}

type memberRequest struct {
	RelatedUserId uuid.UUID `json:"related_user_id"`
	Role          int       `json:"role"`
}

type memberProfileRequest struct {
	Nick *string `json:"nick"`
	Bio  *string `json:"bio"`
}

type labelRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Color       *string `json:"color"`
	Icon        *string `json:"icon"`
}

type labelAssignmentRequest struct {
	LabelId *uuid.UUID `json:"label_id"`
}

type boostRequest struct {
	Shares   int     `json:"shares"`
	Currency *string `json:"currency"`
}

type rolePermissionRequest struct {
	RoleLevel        int  `json:"role_level"`
	CanChat          bool `json:"can_chat"`
	CanPost          bool `json:"can_post"`
	CanComment       bool `json:"can_comment"`
	CanUploadMedia   bool `json:"can_upload_media"`
	CanModeratePosts bool `json:"can_moderate_posts"`
	CanModerateChat  bool `json:"can_moderate_chat"`
	CanManageMembers bool `json:"can_manage_members"`
	CanManageRealm   bool `json:"can_manage_realm"`
}

type userPermissionRequest struct {
	AccountId        uuid.UUID `json:"account_id"`
	CanChat          *bool     `json:"can_chat"`
	CanPost          *bool     `json:"can_post"`
	CanComment       *bool     `json:"can_comment"`
	CanUploadMedia   *bool     `json:"can_upload_media"`
	CanModeratePosts *bool     `json:"can_moderate_posts"`
	CanModerateChat  *bool     `json:"can_moderate_chat"`
	CanManageMembers *bool     `json:"can_manage_members"`
	CanManageRealm   *bool     `json:"can_manage_realm"`
}

// bindJSON decodes the request body, writing the ASP.NET validation shape on
// failure. An empty body binds the zero value, which is what a missing
// [FromBody] payload does for a struct parameter.
func bindJSON(c *gin.Context, target any) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, target); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return false
	}
	return true
}

func validation(c *gin.Context, field string) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{field: {"The field is invalid."}}))
}

func parseInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func itoa(value int) string { return strconv.Itoa(value) }

func isBlank(value *string) bool {
	return value == nil || strings.TrimSpace(*value) == ""
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func queryBool(c *gin.Context, name string) bool {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return false
	}
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}

func exceeded(value string, limit int) bool {
	return len([]rune(value)) > limit
}

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

func nowUTC() time.Time { return time.Now().UTC() }

// idOfAccount parses an account's uuid id (always valid in practice).
func idOfAccount(account *model.Account) uuid.UUID {
	if account == nil {
		return uuid.Nil
	}
	id, _ := uuid.Parse(account.Id)
	return id
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func trimSpace(value string) string { return strings.TrimSpace(value) }

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

// callerWithProfile reloads the authenticated account with its profile, the way
// the C# controllers called AccountService.GetAccount before reading
// account.Profile.Level or account.PerkLevel. A missing row falls back to the
// session account so the request still answers.
func (d Deps) callerWithProfile(c *gin.Context, account *model.Account) *model.Account {
	if d.Profiles == nil {
		return account
	}
	id, ok := accountIDOf(account)
	if !ok {
		return account
	}
	loaded, err := d.Profiles.GetAccountWithProfile(c.Request.Context(), id)
	if err != nil || loaded == nil {
		if d.Log != nil && err != nil {
			d.Log.Warn("realm caller reload failed", "account", account.Id, "error", err)
		}
		return account
	}
	if loaded.PerkLevel == 0 && account.PerkLevel > 0 {
		loaded.PerkLevel = account.PerkLevel
	}
	return loaded
}
