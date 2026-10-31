// Package statusctl ports Passport's account-status and presence-activity HTTP
// surface into Stargate: /api/accounts/{me,:name}/statuses and the whole
// /api/activities tree (C# AccountEventController + PresenceActivityController
// + FriendsController.GetOverview's activity half).
//
// The controllers talk to narrow interfaces (DataStore, Presence) rather than
// the concrete store and resolver so the validation order and the
// device-presence visibility rule are testable without Postgres; the
// compile-time assertions below keep *store.Store and *presence.Resolver
// conforming.
package statusctl

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/grpcclient"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/presence"
	"src.solsynth.dev/sosys/stargate/store"
)

// DataStore is the persistence surface the status/activity controllers use.
type DataStore interface {
	GetCurrentStatus(ctx context.Context, accountID uuid.UUID) (*model.AccountStatus, error)
	GetCurrentStatusByApp(ctx context.Context, accountID uuid.UUID, app string) (*model.AccountStatus, error)
	GetCurrentStatuses(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID]*model.AccountStatus, error)
	CreateStatus(ctx context.Context, status *model.AccountStatus) error
	ClearStatus(ctx context.Context, accountID, statusID uuid.UUID) error
	UpdateStatus(ctx context.Context, status *model.AccountStatus) error

	GetActiveActivities(ctx context.Context, accountID uuid.UUID) ([]model.PresenceActivity, error)
	GetActiveActivitiesBatch(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID][]model.PresenceActivity, error)
	ListActivities(ctx context.Context, accountID uuid.UUID, filter store.ActivityFilter) ([]model.PresenceActivity, int, error)
	InsertActivity(ctx context.Context, activity *model.PresenceActivity) error
	UpdateActivity(ctx context.Context, activity *model.PresenceActivity) error
	GetActivityByID(ctx context.Context, accountID, activityID uuid.UUID) (*model.PresenceActivity, error)
	GetActivityByManualID(ctx context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error)
	GetActiveSessionByManualID(ctx context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error)
	DeleteActivity(ctx context.Context, accountID, activityID uuid.UUID) error
	EndActivitySession(ctx context.Context, accountID uuid.UUID, manualID string) (*model.PresenceActivity, error)

	GetCatalogItems(ctx context.Context, accountID uuid.UUID, tags []string, provider, query string, isActive *bool, offset, take int) ([]model.PresenceCatalogItem, error)
	GetCatalogItemByKey(ctx context.Context, accountID uuid.UUID, provider *string, catalogKey string) (*model.PresenceCatalogItem, error)
	GetCatalogStats(ctx context.Context, accountID uuid.UUID, from, to *time.Time, tags []string) (map[string]int64, error)
	GetTagStats(ctx context.Context, accountID uuid.UUID, from, to *time.Time) (map[string]int64, error)
	UpsertCatalogItem(ctx context.Context, item *model.PresenceCatalogItem) (*model.PresenceCatalogItem, error)
}

var _ DataStore = (*store.Store)(nil)

// Presence is the computed half of a status read (internal/presence).
type Presence interface {
	ResolveOnlineDevices(ctx context.Context, accountID uuid.UUID) (bool, []presence.OnlineDevice, error)
	ResolveOnlineDevicesBatch(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID][]presence.OnlineDevice, error)
	GetAccountIdleState(ctx context.Context, accountID uuid.UUID) (bool, *time.Time, error)
	ApplyPresence(ctx context.Context, status *model.AccountStatus, online bool, devices []presence.OnlineDevice, idle bool, idleSince *time.Time) error
	CanViewDevicePresence(ctx context.Context, ownerID uuid.UUID, viewerID *uuid.UUID, includeDevicePresence bool) (bool, error)
}

var _ Presence = (*presence.Resolver)(nil)

// Broadcaster pushes the post-write presence notifications Passport published
// from its service layer. It is nil when the presence module is not built.
type Broadcaster interface {
	StatusChanged(ctx context.Context, accountID uuid.UUID, deviceID string, previous *model.AccountStatus, at time.Time) error
	ActivitiesChanged(ctx context.Context, accountID uuid.UUID) error
}

// Deps is the dependency set the status/activity controllers need.
type Deps struct {
	Store     DataStore
	Accounts  AccountLookup
	Perm      middleware.PermissionChecker
	Presence  Presence
	Files     ports.Files
	Clients   *grpcclient.Clients
	Broadcast Broadcaster
	Log       *slog.Logger
}

// Register mounts the account-status routes and the activity tree.
func Register(api *gin.RouterGroup, d Deps) {
	registerStatuses(api, d)
	registerActivities(api, d)
}

// Loader reads statuses with presence applied; it is the Go port of
// AccountEventService.GetStatus/GetStatuses minus the (dropped) caches, and is
// shared with the DyProfileService status RPCs.
type Loader struct {
	Store    DataStore
	Presence Presence
}

// Status assembles one account's status. viewer is nil for an anonymous read.
func (l Loader) Status(ctx context.Context, ownerID uuid.UUID, viewer *uuid.UUID) (*model.AccountStatus, error) {
	online, devices, idle, idleSince, err := l.resolvePresence(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	status, err := l.Store.GetCurrentStatus(ctx, ownerID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if status == nil {
		status = defaultStatus(ownerID, online)
	}
	if err := l.applyPresence(ctx, status, online, devices, idle, idleSince); err != nil {
		return nil, err
	}
	visible, err := l.canView(ctx, ownerID, viewer)
	if err != nil {
		return nil, err
	}
	if !visible {
		status.OnlineDevices = nil
	}
	return status, nil
}

// Statuses assembles a batch of statuses, resolving device presence in one
// lookup and applying the per-account visibility rule.
func (l Loader) Statuses(ctx context.Context, ownerIDs []uuid.UUID, viewer *uuid.UUID) (map[uuid.UUID]*model.AccountStatus, error) {
	devicesByAccount := map[uuid.UUID][]presence.OnlineDevice{}
	if l.Presence != nil && len(ownerIDs) > 0 {
		resolved, err := l.Presence.ResolveOnlineDevicesBatch(ctx, ownerIDs)
		if err != nil {
			return nil, err
		}
		devicesByAccount = resolved
	}
	statuses, err := l.Store.GetCurrentStatuses(ctx, ownerIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*model.AccountStatus, len(ownerIDs))
	for _, id := range ownerIDs {
		devices := devicesByAccount[id]
		online := len(devices) > 0
		idle, idleSince := false, (*time.Time)(nil)
		if online {
			if idle, idleSince, err = l.idleState(ctx, id); err != nil {
				return nil, err
			}
		}
		status := statuses[id]
		if status == nil {
			status = defaultStatus(id, online)
		}
		if err := l.applyPresence(ctx, status, online, devices, idle, idleSince); err != nil {
			return nil, err
		}
		visible, err := l.canView(ctx, id, viewer)
		if err != nil {
			return nil, err
		}
		if !visible {
			status.OnlineDevices = nil
		}
		out[id] = status
	}
	return out, nil
}

// defaultStatus mirrors the synthesized SnAccountStatus GetStatus returns when
// the account has no active row: neutral, uncustomized, labelled by presence.
// The shared model helper also fills the non-nullable id/created_at/updated_at
// fields the Dart SDK requires.
func defaultStatus(accountID uuid.UUID, online bool) *model.AccountStatus {
	return model.DefaultAccountStatus(accountID.String(), online)
}

func (l Loader) resolvePresence(ctx context.Context, accountID uuid.UUID) (bool, []presence.OnlineDevice, bool, *time.Time, error) {
	if l.Presence == nil {
		return false, nil, false, nil, nil
	}
	online, devices, err := l.Presence.ResolveOnlineDevices(ctx, accountID)
	if err != nil {
		return false, nil, false, nil, err
	}
	if !online {
		return false, devices, false, nil, nil
	}
	idle, idleSince, err := l.idleState(ctx, accountID)
	if err != nil {
		return false, nil, false, nil, err
	}
	return online, devices, idle, idleSince, nil
}

func (l Loader) idleState(ctx context.Context, accountID uuid.UUID) (bool, *time.Time, error) {
	if l.Presence == nil {
		return false, nil, nil
	}
	return l.Presence.GetAccountIdleState(ctx, accountID)
}

func (l Loader) applyPresence(ctx context.Context, status *model.AccountStatus, online bool, devices []presence.OnlineDevice, idle bool, idleSince *time.Time) error {
	if l.Presence == nil {
		// Without a resolver an invisible status still must not read as
		// online, so fill the read-time projection locally.
		status.IsOnline = false
		status.OnlineDevices = []model.OnlineDevice{}
		status.IsIdle = false
		status.IdleSince = nil
		status.IsInvisible = status.Type == model.StatusTypeInvisible
		status.IsNotDisturb = status.Type == model.StatusTypeDoNotDisturb
		return nil
	}
	return l.Presence.ApplyPresence(ctx, status, online, devices, idle, idleSince)
}

func (l Loader) canView(ctx context.Context, ownerID uuid.UUID, viewer *uuid.UUID) (bool, error) {
	if l.Presence == nil {
		return false, nil
	}
	return l.Presence.CanViewDevicePresence(ctx, ownerID, viewer, false)
}

// ─────────────────────────── shared helpers ───────────────────────────

func internalError(c *gin.Context, err error) {
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

func requireCurrentUser(c *gin.Context) *model.Account {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return nil
	}
	return user
}

func currentAccountID(account *model.Account) (uuid.UUID, bool) {
	id, err := uuid.Parse(account.Id)
	return id, err == nil
}

// currentViewer returns the authenticated viewer id, nil for anonymous reads.
func currentViewer(c *gin.Context) *uuid.UUID {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		return nil
	}
	id, err := uuid.Parse(user.Id)
	if err != nil {
		return nil
	}
	return &id
}

// validationError mirrors ASP.NET's automatic model-state 400 shape.
func validationError(c *gin.Context, field, message string) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{field: {message}}))
}

// queryAny reads the first present of several query aliases. The C# endpoints
// bind camelCase names; Stargate's own JSON surface is snake_case, so both are
// accepted.
func queryAny(c *gin.Context, names ...string) (string, bool) {
	for _, name := range names {
		if values, ok := c.GetQueryArray(name); ok {
			if len(values) > 0 {
				return values[0], true
			}
			return "", true
		}
	}
	return "", false
}

func queryValues(c *gin.Context, names ...string) []string {
	for _, name := range names {
		if values, ok := c.GetQueryArray(name); ok {
			return values
		}
	}
	return nil
}

// parseOptionalInt mirrors the nullable int? binding: absent or blank is nil,
// an unparsable value is a model-state error.
func parseOptionalInt(c *gin.Context, names ...string) (*int, bool) {
	raw, ok := queryAny(c, names...)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, true
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		validationError(c, names[0], "The value '"+raw+"' is not valid.")
		return nil, false
	}
	return &value, true
}

// parseOptionalBool mirrors C#'s bool binding (bool.TryParse accepts
// True/False as well as true/false).
func parseOptionalBool(c *gin.Context, names ...string) (*bool, bool) {
	raw, ok := queryAny(c, names...)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, true
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		validationError(c, names[0], "The value '"+raw+"' is not valid.")
		return nil, false
	}
	return &value, true
}

func parseBool(c *gin.Context, names ...string) (bool, bool) {
	value, ok := parseOptionalBool(c, names...)
	if !ok {
		return false, false
	}
	return value != nil && *value, true
}

// parseOptionalTime mirrors DateTimeOffset? binding.
func parseOptionalTime(c *gin.Context, names ...string) (*time.Time, bool) {
	raw, ok := queryAny(c, names...)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, true
	}
	value, err := parseQueryTime(strings.TrimSpace(raw))
	if err != nil {
		validationError(c, names[0], "The value '"+raw+"' is not valid.")
		return nil, false
	}
	return &value, true
}

func parseQueryTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("invalid time")
}

// parseOffsetTake reads the offset/take pair with the C# action defaults,
// rejecting unparsable values as a model-state 400.
func parseOffsetTake(c *gin.Context, defaultTake int) (int, int, bool) {
	offset, ok := parseOptionalInt(c, "offset")
	if !ok {
		return 0, 0, false
	}
	take, ok := parseOptionalInt(c, "take")
	if !ok {
		return 0, 0, false
	}
	offsetValue, takeValue := 0, defaultTake
	if offset != nil {
		offsetValue = *offset
	}
	if take != nil {
		takeValue = *take
	}
	return offsetValue, takeValue, true
}

func normalizeNullableField(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeProvider(value *string) *string {
	normalized := normalizeNullableField(value)
	if normalized == nil {
		return nil
	}
	lowered := strings.ToLower(*normalized)
	return &lowered
}

func normalizeQueryableTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		trimmed := strings.TrimSpace(term)
		if trimmed == "" {
			continue
		}
		lowered := strings.ToLower(trimmed)
		if _, ok := seen[lowered]; ok {
			continue
		}
		seen[lowered] = struct{}{}
		out = append(out, lowered)
	}
	return out
}

func trimOrNil(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

// encodeMetaJSON mirrors InfraObjectCoder.ConvertObjectToByteString: the JSON
// encoding of the dictionary, with a nil dictionary encoding as "null".
func encodeMetaJSON(meta map[string]any) []byte {
	raw, err := json.Marshal(meta)
	if err != nil {
		return []byte("null")
	}
	return raw
}

func timeValue(t *model.Time) *time.Time {
	if t == nil {
		return nil
	}
	value := t.Time()
	return &value
}
