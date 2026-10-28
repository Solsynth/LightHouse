package fediverse

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/config"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// server is the module's shared state. Every helper file hangs off it.
type server struct {
	core     *core.Services
	db       *gorm.DB
	store    *store.Store
	log      *slog.Logger
	settings map[string]any

	domain   string
	baseURL  string
	fileBase string

	keys       *keyService
	discovery  *discoveryService
	delivery   *deliveryService
	moderation *moderationService

	http            *http.Client
	authorizedFetch gin.HandlerFunc
}

func newServer(c *core.Services, settings map[string]any, log *slog.Logger) *server {
	if log == nil {
		log = slog.Default()
	}
	cfg := c.Config
	domain := strings.TrimSpace(cfg.ActivityPub.Domain)
	if domain == "" {
		domain = "localhost"
	}
	baseURL := "https://" + domain
	fileBase := strings.TrimRight(cfg.ActivityPub.FileBaseURL, "/")
	if fileBase == "" {
		fileBase = baseURL + "/files"
	}

	s := &server{
		core:     c,
		db:       c.DB,
		store:    store.New(c.DB),
		log:      log,
		settings: settings,
		domain:   domain,
		baseURL:  baseURL,
		fileBase: fileBase,
	}
	s.http = newSafeClient()
	s.keys = newKeyService(c.DB, log, s.serverKeyPath(), s.serverKeyID())
	s.discovery = &discoveryService{s: s}
	s.delivery = newDeliveryService(s)
	s.moderation = newModerationService(c.DB, log)
	s.authorizedFetch = s.authorizedFetchMiddleware()
	return s
}

func (s *server) serverKeyPath() string {
	if path := strings.TrimSpace(s.core.Config.ActivityPub.ServerKeyPath); path != "" {
		return path
	}
	return "./Keys/server-key.json"
}

func (s *server) serverKeyID() string {
	return s.baseURL + "/activitypub/actor#main-key"
}

// bootstrap generates the server signing key on first boot and seeds the
// moderation rules configured under [fediverseModeration].
func (s *server) bootstrap(ctx context.Context) error {
	if _, err := s.keys.getServerKey(); err != nil {
		return fmt.Errorf("server signing key: %w", err)
	}
	if err := s.seedModerationRules(ctx, s.core.Config.FediverseModeration.Rules); err != nil {
		return fmt.Errorf("seed moderation rules: %w", err)
	}
	return nil
}

// ---- small helpers --------------------------------------------------------

func (s *server) actorURL(username string) string {
	return s.baseURL + "/activitypub/actors/" + username
}

func (s *server) objectURL(id uuid.UUID) string {
	return s.baseURL + "/activitypub/objects/" + id.String()
}

func (s *server) settingBool(key string) bool {
	if s.settings == nil {
		return false
	}
	value, ok := s.settings[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true")
	default:
		return false
	}
}

func (s *server) requireAdmin(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		httpx.Unauthorized(c)
		c.Abort()
		return
	}
	if !user.GetIsSuperuser() {
		httpx.Forbidden(c, "You do not have the permission to perform this action.")
		c.Abort()
		return
	}
	c.Next()
}

// authorizedFetchMiddleware mirrors AuthorizedFetchMiddleware: when enabled it
// rejects unsigned fetches of actor documents and posts, while collections and
// local traffic stay public.
func (s *server) authorizedFetchMiddleware() gin.HandlerFunc {
	enabled := s.settingBool("authorizedFetch")
	return func(c *gin.Context) {
		if !enabled {
			c.Next()
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		if !strings.HasPrefix(strings.ToLower(path), "/activitypub/actors/") &&
			!strings.HasPrefix(strings.ToLower(path), "/posts/") {
			c.Next()
			return
		}
		if c.GetHeader("Signature") != "" || localHost(c.Request.Host, s.domain) {
			c.Next()
			return
		}
		if strings.Contains(path, "/followers") || strings.Contains(path, "/following") ||
			strings.Contains(path, "/outbox") || strings.Contains(path, "/featured") {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "Unauthorized",
			"message": "This server requires HTTP signatures for fetching remote resources (Authorized Fetch)",
		})
	}
}

func localHost(host, domain string) bool {
	host = strings.Split(host, ":")[0]
	return strings.EqualFold(host, domain) || strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(domain))
}

// ---- wire mappers ---------------------------------------------------------

func instanceToModel(entity *store.FediverseInstanceEntity) model.FediverseInstance {
	return model.FediverseInstance{
		ID:                     entity.ID.String(),
		Domain:                 entity.Domain,
		Name:                   entity.Name,
		Description:            entity.Description,
		Software:               entity.Software,
		Version:                entity.Version,
		IconURL:                entity.IconURL,
		ThumbnailURL:           entity.ThumbnailURL,
		ContactEmail:           entity.ContactEmail,
		ContactAccountUsername: entity.ContactAccountUsername,
		ActiveUsers:            entity.ActiveUsers,
		Metadata:               store.JSONValue(entity.Metadata),
		IsBlocked:              entity.IsBlocked,
		IsSilenced:             entity.IsSilenced,
		BlockReason:            entity.BlockReason,
		LastFetchedAt:          store.TimePtr(entity.LastFetchedAt),
		LastActivityAt:         store.TimePtr(entity.LastActivityAt),
		MetadataFetchedAt:      store.TimePtr(entity.MetadataFetchedAt),
		CreatedAt:              store.TimePtr(&entity.CreatedAt),
		UpdatedAt:              store.TimePtr(&entity.UpdatedAt),
		DeletedAt:              store.DeletedPtr(entity.DeletedAt),
	}
}

func ruleToModel(entity *store.FediverseModerationRuleEntity) model.FediverseModerationRule {
	return model.FediverseModerationRule{
		ID:              entity.ID.String(),
		Name:            entity.Name,
		Description:     entity.Description,
		Type:            entity.Type,
		Action:          entity.Action,
		Domain:          entity.Domain,
		KeywordPattern:  entity.KeywordPattern,
		IsRegex:         entity.IsRegex,
		IsEnabled:       entity.IsEnabled,
		ReportThreshold: entity.ReportThreshold,
		ExpiresAt:       store.TimePtr(entity.ExpiresAt),
		Priority:        entity.Priority,
		IsSystemRule:    entity.IsSystemRule,
		CreatedAt:       store.TimePtr(&entity.CreatedAt),
		UpdatedAt:       store.TimePtr(&entity.UpdatedAt),
		DeletedAt:       store.DeletedPtr(entity.DeletedAt),
	}
}

// activityPubTime renders a Go time the way NodaTime's Instant serializer
// does: RFC3339 with second precision.
func activityPubTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ---- JSON helpers ---------------------------------------------------------

func getString(dict map[string]any, key string) string { return anyToString(dict[key]) }

func anyToString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case map[string]any, []any:
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(raw)
	default:
		return fmt.Sprint(typed)
	}
}

func getObject(dict map[string]any, key string) map[string]any {
	value, ok := dict[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case json.RawMessage:
		var decoded map[string]any
		if json.Unmarshal(typed, &decoded) == nil {
			return decoded
		}
	}
	return nil
}

func getObjects(dict map[string]any, key string) []any {
	value, ok := dict[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []any:
		return typed
	case map[string]any:
		return []any{typed}
	}
	return nil
}

func parseInstant(value any) *time.Time {
	text := anyToString(value)
	if text == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.999999999Z07:00", time.RFC1123} {
		if parsed, err := time.Parse(layout, text); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

// ---- context helpers ------------------------------------------------------

func currentUser(c *gin.Context) accountUser {
	value, ok := c.Get("current_user")
	if !ok {
		return nil
	}
	user, _ := value.(accountUser)
	return user
}

// accountUser is the subset of *gen.DyAccount the fediverse handlers need.
type accountUser interface {
	GetId() string
	GetName() string
	GetNick() string
	GetIsSuperuser() bool
}

// randomToken returns a random hex string used in generated activity ids.
func randomToken() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return uuid.NewString()
	}
	return fmt.Sprintf("%x", buf)
}

// errNotFound is the package's sentinel for missing rows.
var errNotFound = gorm.ErrRecordNotFound

// unsafeURL guards the fields that must be absolute https URIs (actor URIs).
func unsafeURL(raw string) bool {
	return strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "http://")
}

// hostOf extracts a host without panicking on malformed input.
func hostOf(raw string) string {
	if parsed, err := urlParse(raw); err == nil {
		return parsed.Host
	}
	return ""
}

// urlParse indirection keeps net/url out of the helper imports where unused.
func urlParse(raw string) (*parsedURL, error) {
	return parseURL(raw)
}

type parsedURL = urlValue

// netIP is used by the redirect guard.
func netIP(host string) []net.IP {
	addresses, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	return addresses
}

// ensure module import stays referenced if helpers change.
var _ = module.PortRequirement{}

// compile-time guard that the wire model stays reachable from this file.
var _ = model.FediverseInstance{}

// configDocuments keeps the config type referenced for documentation.
var _ = config.FediverseRuleSeed{}
