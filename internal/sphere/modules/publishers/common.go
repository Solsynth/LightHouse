package publishers

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/go/pkg/models"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

var publisherNamePattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9\-_\.]*[a-zA-Z0-9])?$`)

func (i *instance) notFound(c *gin.Context) {
	httpx.NotFound(c, "Not found.")
}

func (i *instance) badRequest(c *gin.Context, code, message string) {
	httpx.Fail(c, errs.BadRequest(code, message))
}

func (i *instance) forbidden(c *gin.Context, message string) {
	httpx.Fail(c, errs.Forbidden(message))
}

func (i *instance) conflict(c *gin.Context, code, message string) {
	httpx.Fail(c, errs.New(code, message, 409))
}

func (i *instance) internal(c *gin.Context) {
	httpx.Internal(c)
}

func (i *instance) unavailable(c *gin.Context) {
	httpx.Fail(c, errs.New("SERVICE_UNAVAILABLE", "The dependent service is unavailable.", 503))
}

// accountID returns the authenticated account id, aborting with 401 when it is
// missing. The engine's optional-auth middleware means handlers still guard.
func (i *instance) accountID(c *gin.Context) (uuid.UUID, bool) {
	user := authn.CurrentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	id, err := uuid.Parse(user.GetId())
	if err != nil {
		httpx.Unauthorized(c)
		return uuid.Nil, false
	}
	return id, true
}

func (i *instance) publisherOr404(c *gin.Context, name string) (*store.PublisherEntity, bool) {
	publisher, err := i.publisherByName(c.Request.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.notFound(c)
			return nil, false
		}
		i.internal(c)
		return nil, false
	}
	return publisher, true
}

// validatePublisherPayload implements the PublisherRequest DataAnnotations.
func (i *instance) validatePublisherPayload(c *gin.Context, request publisherRequest, requireNameNick bool) bool {
	fieldErrors := map[string][]string{}
	if requireNameNick && (request.Name == nil || strings.TrimSpace(*request.Name) == "") {
		i.badRequest(c, "PUBLISHER_NAME_NICK_REQUIRED", "Name and Nick are required.")
		return false
	}
	if requireNameNick && (request.Nick == nil || strings.TrimSpace(*request.Nick) == "") {
		i.badRequest(c, "PUBLISHER_NAME_NICK_REQUIRED", "Name and Nick are required.")
		return false
	}
	if request.Name != nil {
		if utf8.RuneCountInString(*request.Name) > 256 {
			fieldErrors["name"] = append(fieldErrors["name"], "The field Name must be a string or array type with a maximum length of '256'.")
		} else if *request.Name != "" && !publisherNamePattern.MatchString(*request.Name) {
			fieldErrors["name"] = append(fieldErrors["name"], "Name must be URL-safe (alphanumeric, hyphens, underscores, or periods) and cannot start/end with special characters.")
		}
	}
	if request.Nick != nil && utf8.RuneCountInString(*request.Nick) > 256 {
		fieldErrors["nick"] = append(fieldErrors["nick"], "The field Nick must be a string or array type with a maximum length of '256'.")
	}
	if request.Bio != nil && utf8.RuneCountInString(*request.Bio) > 4096 {
		fieldErrors["bio"] = append(fieldErrors["bio"], "The field Bio must be a string or array type with a maximum length of '4096'.")
	}
	if len(fieldErrors) > 0 {
		httpx.Fail(c, errs.Validation(fieldErrors))
		return false
	}
	return true
}

// fileReference resolves a Drive file id into the jsonb reference object.
func (i *instance) fileReference(c *gin.Context, id *string) (datatypes.JSON, bool) {
	if id == nil || *id == "" {
		return nil, true
	}
	if i.files == nil {
		i.unavailable(c)
		return nil, false
	}
	reference, err := i.files.GetFile(c.Request.Context(), *id)
	if err != nil || reference == nil {
		i.internal(c)
		return nil, false
	}
	return store.ToJSON(*reference), true
}

func (i *instance) accountProfileBio(account *gen.DyAccount) *string {
	if account == nil || account.GetProfile() == nil {
		return nil
	}
	bio := account.GetProfile().GetBio().GetValue()
	if bio == "" {
		return nil
	}
	return &bio
}

func (i *instance) accountProfilePicture(account *gen.DyAccount) datatypes.JSON {
	if account == nil {
		return nil
	}
	if file := account.GetProfile().GetPicture(); file != nil {
		return store.ToJSON(protoFile(file))
	}
	if file := account.GetPicture(); file != nil {
		return store.ToJSON(protoFile(file))
	}
	return nil
}

func (i *instance) accountProfileBackground(account *gen.DyAccount) datatypes.JSON {
	if account == nil {
		return nil
	}
	if file := account.GetProfile().GetBackground(); file != nil {
		return store.ToJSON(protoFile(file))
	}
	if file := account.GetBackground(); file != nil {
		return store.ToJSON(protoFile(file))
	}
	return nil
}

func (i *instance) addMember(ctx context.Context, publisherID, accountID uuid.UUID, role int, joined *time.Time) error {
	entity := store.PublisherMemberEntity{
		PublisherID: publisherID,
		AccountID:   accountID,
		Role:        role,
		JoinedAt:    joined,
	}
	return i.store.DB.WithContext(ctx).Create(&entity).Error
}

func (i *instance) setMemberRole(ctx context.Context, publisherID, accountID uuid.UUID, role int) error {
	return i.store.DB.WithContext(ctx).
		Model(&store.PublisherMemberEntity{}).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		Update("role", role).Error
}

func (i *instance) memberRow(ctx context.Context, publisherID, accountID uuid.UUID, joinedOnly bool) (*store.PublisherMemberRow, error) {
	query := i.store.DB.WithContext(ctx).Model(&store.PublisherMemberRow{}).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		Where("deleted_at IS NULL")
	if joinedOnly {
		query = query.Where("joined_at IS NOT NULL")
	}
	var row store.PublisherMemberRow
	if err := query.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (i *instance) deleteMember(ctx context.Context, publisherID, accountID uuid.UUID) error {
	return i.store.DB.WithContext(ctx).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		Delete(&store.PublisherMemberEntity{}).Error
}

// publisherModelFor maps and hydrates one publisher.
func (i *instance) publisherModelFor(ctx context.Context, entity *store.PublisherEntity, withAccount bool) model.Publisher {
	mapped := store.PublisherToModel(entity)
	publishers := []model.Publisher{mapped}
	entities := []*store.PublisherEntity{entity}
	i.hydrateRealm(ctx, publishers, entities)
	if withAccount && entity.AccountID != nil {
		if account := i.getAccount(ctx, entity.AccountID.String()); account != nil {
			publishers[0].Account = i.accountRef(account)
		}
	}
	return publishers[0]
}

func validationError(field, message string) *errs.ApiError {
	return errs.Validation(map[string][]string{field: {message}})
}

func protoFile(file *gen.DyCloudFile) *models.SnCloudFileReferenceObject {
	if file == nil {
		return nil
	}
	reference := models.FromProtoValue(file)
	return &reference
}

func (i *instance) memberPublisher(ctx context.Context, publisher *store.PublisherEntity) *model.Publisher {
	mapped := store.PublisherToModel(publisher)
	publishers := []model.Publisher{mapped}
	entities := []*store.PublisherEntity{publisher}
	i.hydrateRealm(ctx, publishers, entities)
	return &publishers[0]
}

func normalizeTagList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.ToLower(strings.TrimSpace(value))
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
