// Package presenceproviders ports Passport's Steam and Last.fm presence
// scanners (Account/Presences/SteamPresenceService.cs and
// LastFmPresenceService.cs) together with their three Quartz stages.
//
// Passport wrote the resulting activities to Stargate over
// RemotePresenceService. Stargate now owns the activity rows, so the scanners
// live here and write through store.Start/End-equivalent helpers implemented in
// session.go. The helpers reproduce the semantics of Stargate's
// POST /api/activities/sessions/{start,end} handlers (get-or-create catalog,
// refresh the open session in place, renew the lease) because the store only
// exposes the row-level primitives.
package presenceproviders

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/broadcast"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/presence"
	"src.solsynth.dev/sosys/stargate/store"
)

// ModuleID is the reverse-DNS identifier of the module that owns this
// package.
const ModuleID = "app.solian.id.presenceproviders"

// Steam and Last.fm service identifiers, also used as the session manual ids
// exactly like the C# scanners (RemotePresenceService.StartSessionAsync
// receives serviceId as the manual id).
const (
	ServiceSteam  = "steam"
	ServiceLastFM = "lastfm"
)

// LeaseMinutes is the session lease both C# scanners request.
const LeaseMinutes = 10

// PresenceUpdateStage mirrors PresenceUpdateStage.cs.
type Stage string

const (
	StageActive Stage = "active"
	StageMaybe  Stage = "maybe"
	StageCold   Stage = "cold"
)

// ParseStage parses a stage name case-insensitively, mirroring
// Enum.TryParse(ignoreCase: true).
func ParseStage(value string) (Stage, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(StageActive):
		return StageActive, true
	case string(StageMaybe):
		return StageMaybe, true
	case string(StageCold):
		return StageCold, true
	default:
		return "", false
	}
}

// ErrAccountNotFound marks an account/connection lookup that found nothing so
// admin handlers can answer PASSPORT_ACCOUNT_NOT_FOUND.
var ErrAccountNotFound = errors.New("presenceproviders: account not found")

// Settings are the provider credentials. They come from
// [modules.settings."app.solian.id.presenceproviders"] and fall back to the
// Oidc TOML section (Oidc:Steam:ApiKey, Oidc:LastFm:ApiKey/ApiSecret) so an
// existing Stargate config keeps working.
type Settings struct {
	SteamAPIKey     string
	LastFMAPIKey    string
	LastFMAPISecret string
}

const (
	defaultSteamBaseURL  = "https://api.steampowered.com"
	defaultLastFMBaseURL = "https://ws.audioscrobbler.com/2.0/"
)

// Options wires a Service. Store is required; every other collaborator
// degrades to a no-op when nil.
type Options struct {
	Store        *store.Store
	DB           *gorm.DB
	Presence     *presence.Resolver
	PresencePort ports.Presence
	Broadcast    *broadcast.Broadcaster
	Log          *slog.Logger
	Settings     Settings

	// HTTPClient and the two base URLs are overridable for tests.
	HTTPClient    *http.Client
	SteamBaseURL  string
	LastFMBaseURL string
	Now           func() time.Time
}

// Service runs the provider scans and owns their scheduled stages.
type Service struct {
	store        *store.Store
	db           *gorm.DB
	resolver     *presence.Resolver
	presencePort ports.Presence
	broadcast    *broadcast.Broadcaster
	log          *slog.Logger
	settings     Settings

	httpClient    *http.Client
	steamBaseURL  string
	lastFMBaseURL string
	now           func() time.Time

	jobOnce   sync.Once
	jobCancel context.CancelFunc
	jobWG     sync.WaitGroup
	// One mutex per stage prevents overlapping runs of the same stage (C#
	// [DisallowConcurrentExecution]); different stages may overlap like the
	// separate Quartz jobs did.
	stageLocks [3]sync.Mutex
}

// New builds a Service.
func New(opts Options) *Service {
	logger := opts.Log
	if logger == nil {
		logger = slog.Default()
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	steamBase := strings.TrimRight(opts.SteamBaseURL, "/")
	if steamBase == "" {
		steamBase = defaultSteamBaseURL
	}
	lastFMBase := opts.LastFMBaseURL
	if lastFMBase == "" {
		lastFMBase = defaultLastFMBaseURL
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:         opts.Store,
		db:            opts.DB,
		resolver:      opts.Presence,
		presencePort:  opts.PresencePort,
		broadcast:     opts.Broadcast,
		log:           logger,
		settings:      opts.Settings,
		httpClient:    client,
		steamBaseURL:  steamBase,
		lastFMBaseURL: lastFMBase,
		now:           now,
	}
}

// LookupAccount resolves an admin route identifier through the same
// name/GUID/contact lookup the rest of the admin surface uses.
func (s *Service) LookupAccount(ctx context.Context, identifier string) (*model.Account, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("presenceproviders: store is not configured")
	}
	return s.store.AdminLookupAccount(ctx, identifier)
}

// ─────────────────────────── connections ───────────────────────────

// providerConnection is the subset of account_connections the scanners need.
// Reading the table directly is deliberate: the store exposes per-account
// lookups only, while a scan needs every connection of a provider in one
// query. Conditions mirror the C# ListConnectionsAsync gRPC call
// (soft-deleted rows excluded) plus the scanners' IsNullOrWhiteSpace filter.
type providerConnection struct {
	AccountID          uuid.UUID
	ProvidedIdentifier string
	AccessToken        *string
	RefreshToken       *string
}

func (s *Service) providerConnections(ctx context.Context, provider string, accountIDs []uuid.UUID) ([]providerConnection, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("presenceproviders: database is not configured")
	}
	if accountIDs != nil && len(accountIDs) == 0 {
		return nil, nil
	}
	query := s.db.WithContext(ctx).Table("account_connections").
		Select("account_id, provided_identifier, access_token, refresh_token").
		Where("deleted_at IS NULL").
		Where("LOWER(provider) = ?", strings.ToLower(strings.TrimSpace(provider))).
		Where("BTRIM(provided_identifier) <> ''")
	if accountIDs != nil {
		query = query.Where("account_id IN ?", accountIDs)
	}
	var rows []struct {
		AccountID          uuid.UUID
		ProvidedIdentifier string
		AccessToken        *string
		RefreshToken       *string
	}
	if err := query.Order("account_id, provided_identifier").Scan(&rows).Error; err != nil {
		return nil, err
	}
	connections := make([]providerConnection, 0, len(rows))
	for _, row := range rows {
		connections = append(connections, providerConnection{
			AccountID:          row.AccountID,
			ProvidedIdentifier: row.ProvidedIdentifier,
			AccessToken:        row.AccessToken,
			RefreshToken:       row.RefreshToken,
		})
	}
	return connections, nil
}

func (s *Service) steamConnections(ctx context.Context, accountIDs []uuid.UUID) ([]providerConnection, error) {
	return s.providerConnections(ctx, ServiceSteam, accountIDs)
}

func (s *Service) lastFMConnections(ctx context.Context, accountIDs []uuid.UUID) ([]providerConnection, error) {
	return s.providerConnections(ctx, ServiceLastFM, accountIDs)
}

// connectedAccountIDs is the Go equivalent of
// AccountEventService.GetPresenceConnectedUsersAsync: every account with a
// non-blank connection to one of providers, where a non-Steam provider also
// needs an access or refresh token (exactly the C# predicate).
func (s *Service) connectedAccountIDs(ctx context.Context, providers []string) ([]uuid.UUID, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("presenceproviders: database is not configured")
	}
	normalized := make([]string, 0, len(providers))
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		normalized = append(normalized, provider)
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	var ids []uuid.UUID
	err := s.db.WithContext(ctx).Table("account_connections").
		Distinct("account_id").
		Where("deleted_at IS NULL").
		Where("BTRIM(provided_identifier) <> ''").
		Where("LOWER(provider) IN ?", normalized).
		Where(`(LOWER(provider) = 'steam'
			OR COALESCE(access_token, '') <> ''
			OR COALESCE(refresh_token, '') <> '')`).
		Order("account_id").
		Pluck("account_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// onlineAccounts resolves IsOnline for a batch. The core resolver (device
// presence in Blade) is authoritative; the optional presence port serves a
// deployment where the resolver was not built; the stored status is the last
// fallback.
func (s *Service) onlineAccounts(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	online := make(map[uuid.UUID]bool, len(accountIDs))
	for _, id := range accountIDs {
		online[id] = false
	}
	if len(accountIDs) == 0 {
		return online, nil
	}
	if s.resolver != nil {
		devices, err := s.resolver.ResolveOnlineDevicesBatch(ctx, accountIDs)
		if err != nil {
			return nil, err
		}
		for id, list := range devices {
			online[id] = len(list) > 0
		}
		return online, nil
	}
	if s.presencePort != nil && s.presencePort.Available() {
		requested := make([]string, 0, len(accountIDs))
		for _, id := range accountIDs {
			requested = append(requested, id.String())
		}
		devices, err := s.presencePort.OnlineDevices(ctx, requested, presence.NamespaceSolian)
		if err != nil {
			return nil, err
		}
		for key, list := range devices {
			if id, err := uuid.Parse(key); err == nil {
				online[id] = len(list) > 0
			}
		}
		return online, nil
	}
	if s.store != nil {
		statuses, err := s.store.GetCurrentStatuses(ctx, accountIDs)
		if err != nil {
			return nil, err
		}
		for id, status := range statuses {
			online[id] = status != nil && status.IsOnline
		}
	}
	return online, nil
}

// accountsForStage applies the C# stage rule: Active = online, Maybe = nobody
// (the C# switch returns false), Cold = offline.
func (s *Service) accountsForStage(ctx context.Context, stage Stage, providers []string) ([]uuid.UUID, error) {
	if stage == StageMaybe {
		return nil, nil
	}
	ids, err := s.connectedAccountIDs(ctx, providers)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	online, err := s.onlineAccounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		isOnline := online[id]
		if (stage == StageActive && isOnline) || (stage == StageCold && !isOnline) {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// broadcastActivities publishes the friends-only update Stargate's activity
// handlers emit after every session write. Best-effort, like those handlers.
func (s *Service) broadcastActivities(ctx context.Context, accountID uuid.UUID) {
	if s == nil || s.broadcast == nil {
		return
	}
	if err := s.broadcast.ActivitiesChanged(ctx, accountID); err != nil && s.log != nil {
		s.log.Warn("broadcast presence activities", "account", accountID, "error", err)
	}
}

// ─────────────────────────── jobs ───────────────────────────

var stageIntervals = [...]struct {
	stage    Stage
	interval time.Duration
}{
	{StageActive, time.Minute},
	{StageMaybe, 5 * time.Minute},
	{StageCold, 10 * time.Minute},
}

// StartJobs starts the three periodic stages. It is idempotent.
func (s *Service) StartJobs(ctx context.Context) {
	if s == nil {
		return
	}
	s.jobOnce.Do(func() {
		jobCtx, cancel := context.WithCancel(ctx)
		s.jobCancel = cancel
		for _, entry := range stageIntervals {
			stage := entry.stage
			interval := entry.interval
			s.jobWG.Add(1)
			go func() {
				defer s.jobWG.Done()
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for {
					select {
					case <-jobCtx.Done():
						return
					case <-ticker.C:
						s.runStage(jobCtx, stage)
					}
				}
			}()
		}
	})
}

// Stop cancels the scheduled stages and waits for running ticks to return.
func (s *Service) Stop() {
	if s == nil {
		return
	}
	if s.jobCancel != nil {
		s.jobCancel()
	}
	s.jobWG.Wait()
}

// runStage is the PresenceUpdateJob Execute body for one stage, guarded so the
// same stage never overlaps itself.
func (s *Service) runStage(ctx context.Context, stage Stage) {
	index := stageIndex(stage)
	if index < 0 {
		return
	}
	if !s.stageLocks[index].TryLock() {
		s.log.Debug("presence stage still running, skipping tick", "stage", stage)
		return
	}
	defer s.stageLocks[index].Unlock()
	if err := s.RunStage(ctx, stage); err != nil {
		s.log.Error("presence stage failed", "stage", stage, "error", err)
	}
}

func stageIndex(stage Stage) int {
	switch stage {
	case StageActive:
		return 0
	case StageMaybe:
		return 1
	case StageCold:
		return 2
	default:
		return -1
	}
}

// RunStage scans both providers for the accounts selected by stage. Errors are
// per-provider and never stop the other provider (C# loops the services).
func (s *Service) RunStage(ctx context.Context, stage Stage) error {
	ids, err := s.accountsForStage(ctx, stage, []string{ServiceSteam, ServiceLastFM})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		s.log.Info("no accounts for presence stage", "stage", stage)
		return nil
	}
	var errs []error
	if result, err := s.ScanSteamUsers(ctx, ids); err != nil {
		errs = append(errs, err)
		s.log.Error("steam presence update failed", "stage", stage, "error", err)
	} else {
		s.log.Info("updated steam presences", "stage", stage, "accounts", len(ids), "items", len(result.Items))
	}
	if result, err := s.ScanLastFMUsers(ctx, ids); err != nil {
		errs = append(errs, err)
		s.log.Error("lastfm presence update failed", "stage", stage, "error", err)
	} else {
		s.log.Info("updated lastfm presences", "stage", stage, "accounts", len(ids), "items", len(result.Items))
	}
	return errors.Join(errs...)
}
