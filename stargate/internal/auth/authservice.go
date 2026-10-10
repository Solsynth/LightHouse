package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/risk"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// SessionRevokedEvent is the auth.session.revoked payload (Phase 11 NATS).
type SessionRevokedEvent struct {
	SessionID string
	AccountID string
	ClientID  *string
	DeviceID  *string
	RevokedAt time.Time
}

// EventBus publishes domain events (NATS + WebSocket pushes).
type EventBus interface {
	PublishSessionRevoked(ctx context.Context, events []SessionRevokedEvent) error
	PublishWS(ctx context.Context, target string, event string, payload any) error
}

// ActionLogSink records account action logs.
type ActionLogSink interface {
	Create(ctx context.Context, accountID string, action model.ActionLogType, meta map[string]any, userAgent, ipAddress string, location *string, sessionID *string) error
}

// ErrInvalid signals a 400-class business-rule failure with a C#-matching
// message (the controller maps these to ApiError codes).
type ErrInvalid struct{ Message string }

func (e *ErrInvalid) Error() string { return e.Message }

// AuthService is the Go port of Padlock's AuthService.
type AuthService struct {
	store      *store.Store
	redis      *redis.Client
	cfg        *config.Config
	geo        *geo.Service
	jwt        *JWTService
	token      *TokenAuthService
	events     EventBus
	logs       ActionLogSink
	log        *slog.Logger
	httpClient *http.Client
}

// NewAuthService wires the auth service.
func NewAuthService(st *store.Store, rc *redis.Client, cfg *config.Config, geo *geo.Service, j *JWTService, token *TokenAuthService, events EventBus, logs ActionLogSink, log *slog.Logger) *AuthService {
	return &AuthService{
		store: st, redis: rc, cfg: cfg, geo: geo, jwt: j, token: token,
		events: events, logs: logs, log: log,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// --- Password / PIN factors ---

// HashPassword hashes with bcrypt cost 12 (same as BCrypt.Net-Next default
// used by SnAccountAuthFactor.HashSecret).
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyFactorPassword verifies a password/PIN factor secret or a TOTP
// timed-code factor, mirroring SnAccountAuthFactor.VerifyPassword.
func VerifyFactorPassword(f *model.AuthFactor, input string) (bool, error) {
	switch model.AuthFactorType(f.Type) {
	case model.AuthFactorTypePassword, model.AuthFactorTypePinCode:
		return bcrypt.CompareHashAndPassword([]byte(f.Secret), []byte(input)) == nil, nil
	case model.AuthFactorTypeTimedCode:
		return totp.Validate(input, f.Secret), nil
	default:
		return false, fmt.Errorf("unsupported verification type")
	}
}

// --- Captcha ---

// ValidateCaptcha verifies a captcha token with the configured provider,
// mirroring AuthService.ValidateCaptcha. SkipCaptcha short-circuits.
//
// Fail-closed: an unconfigured verifier is a misconfiguration, not a pass. It
// is only tolerated when the deployment opted out explicitly with
// [captcha] allow_disabled = true (config.CaptchaRequired); otherwise this
// returns an error and every captcha-gated flow refuses the request.
func (s *AuthService) ValidateCaptcha(ctx context.Context, token string) (bool, error) {
	if s.cfg == nil {
		return false, errors.New("the server misconfigured for the captcha")
	}
	if s.cfg.CaptchaRequired() {
		return false, errors.New("the server misconfigured for the captcha: captcha is not configured")
	}
	if !s.cfg.CaptchaEnabled() {
		return true, nil // explicitly opted out with [captcha] allow_disabled
	}
	if strings.TrimSpace(token) == "" {
		return false, nil
	}
	provider := strings.ToLower(s.cfg.Captcha.Provider)
	secret := s.cfg.Captcha.APISecret
	var verifyURL string
	switch provider {
	case "cloudflare":
		verifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	case "google":
		verifyURL = "https://www.google.com/recaptcha/siteverify"
	case "hcaptcha":
		verifyURL = "https://hcaptcha.com/siteverify"
	default:
		return false, errors.New("the server misconfigured for the captcha")
	}
	form := "secret=" + secret + "&response=" + token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, strings.NewReader(form))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var result struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.Success, nil
}

// --- Sessions ---

// RevokeSession revokes a session and all its descendants (BFS), bumping
// their epochs and publishing auth.session.revoked events. Account-wide token
// invalidation is reserved for RevokeAllSessionsForAccount.
func (s *AuthService) RevokeSession(ctx context.Context, sessionID uuid.UUID) (bool, error) {
	ids, err := s.collectSessionsToRevoke(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return false, nil
	}
	now := time.Now().UTC()
	revoked, err := s.store.RevokeSessions(ctx, ids, now)
	if err != nil {
		return false, err
	}
	if len(revoked) == 0 {
		return false, nil
	}
	if err := s.invalidateSessionCaches(ctx, revoked); err != nil {
		s.log.Warn("invalidate session caches", "error", err)
	}
	s.publishRevoked(ctx, revoked, now)
	return true, nil
}

// RevokeAllSessionsForAccount revokes every live session of an account.
func (s *AuthService) RevokeAllSessionsForAccount(ctx context.Context, accountID string) (int, error) {
	now := time.Now().UTC()
	revoked, err := s.store.RevokeAllSessions(ctx, accountID, now)
	if err != nil {
		return 0, err
	}
	if len(revoked) == 0 {
		return 0, nil
	}
	if err := s.invalidateSessionCaches(ctx, revoked); err != nil {
		s.log.Warn("invalidate session caches", "error", err)
	}
	s.publishRevoked(ctx, revoked, now)
	return len(revoked), nil
}

func (s *AuthService) collectSessionsToRevoke(ctx context.Context, root uuid.UUID) ([]uuid.UUID, error) {
	collected := map[uuid.UUID]struct{}{}
	frontier := []uuid.UUID{root}
	for len(frontier) > 0 {
		for _, id := range frontier {
			if _, exists := collected[id]; exists {
				continue
			}
			collected[id] = struct{}{}
		}
		children := []uuid.UUID{}
		if err := s.store.DB.WithContext(ctx).Unscoped().Model(&store.AuthSessionEntity{}).
			Select("id").
			Where("parent_session_id IN ?", frontier).
			Find(&children).Error; err != nil {
			return nil, err
		}
		frontier = children
	}
	ids := make([]uuid.UUID, 0, len(collected))
	for id := range collected {
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *AuthService) invalidateSessionCaches(ctx context.Context, sessions []store.RevokedSession) error {
	if s.redis == nil || !s.redis.Available() {
		return nil
	}
	for _, session := range sessions {
		key := "auth:session:" + session.SessionID
		_ = s.redis.Cache.Remove(ctx, key)
		_ = s.redis.Raw.Del(ctx, fmt.Sprintf(SessionTokensGroupFmt, session.SessionID)).Err()
		// Revocation drops the elevation grant: a revoked session must not
		// keep sudo for as long as the grant's TTL.
		s.ClearSudo(ctx, session.SessionID)
	}
	return nil
}

func (s *AuthService) publishRevoked(ctx context.Context, sessions []store.RevokedSession, at time.Time) {
	if s.events == nil {
		return
	}
	events := make([]SessionRevokedEvent, 0, len(sessions))
	for _, session := range sessions {
		events = append(events, SessionRevokedEvent{
			SessionID: session.SessionID,
			AccountID: session.AccountID,
			ClientID:  session.ClientID,
			DeviceID:  session.DeviceID,
			RevokedAt: at,
		})
	}
	if err := s.events.PublishSessionRevoked(ctx, events); err != nil {
		s.log.Warn("publish session revoked", "error", err)
	}
}

// --- Tokens ---

// CreateToken issues a single user token for a session.
func (s *AuthService) CreateToken(ctx context.Context, session *model.AuthSession) (string, error) {
	account, err := s.store.GetAccountByID(ctx, uuid.MustParse(session.AccountId))
	if err != nil {
		return "", errors.New("Session account not found.")
	}
	s.hydratePerk(ctx, account)
	expires := s.resolveAccessExpiry(session, time.Now().UTC())
	return s.jwt.CreateUserToken(session, account, expires)
}

// TokenPair is the access+refresh pair.
type TokenPair struct {
	AccessToken           string
	RefreshToken          string
	AccessTokenExpiresAt  time.Time
	RefreshTokenExpiresAt time.Time
}

// CreateTokenPair issues an access + refresh pair for a session.
func (s *AuthService) CreateTokenPair(ctx context.Context, session *model.AuthSession) (*TokenPair, error) {
	account, err := s.store.GetAccountByID(ctx, uuid.MustParse(session.AccountId))
	if err != nil {
		return nil, errors.New("Session account not found.")
	}
	s.hydratePerk(ctx, account)
	now := time.Now().UTC()
	accessExpires := s.resolveAccessExpiry(session, now)
	refreshExpires := now.Add(s.cfg.RefreshTokenLifetime())
	if session.ExpiredAt != nil {
		refreshExpires = session.ExpiredAt.Time()
	}
	access, err := s.jwt.CreateUserToken(session, account, accessExpires)
	if err != nil {
		return nil, err
	}
	refresh, err := s.jwt.CreateRefreshToken(session, refreshExpires)
	if err != nil {
		return nil, err
	}
	return &TokenPair{
		AccessToken: access, RefreshToken: refresh,
		AccessTokenExpiresAt: accessExpires, RefreshTokenExpiresAt: refreshExpires,
	}, nil
}

// CreateSessionAndIssueTokens completes a finished challenge into a session
// and token pair (mirrors the C# method including the action log).
func (s *AuthService) CreateSessionAndIssueTokens(ctx context.Context, challenge *model.AuthChallenge) (*TokenPair, error) {
	if challenge.Purpose == model.AuthChallengePurposeSudo {
		// Elevation challenges grant sudo to the bound session; they must
		// never be exchanged into a login session.
		return nil, &ErrInvalid{Message: "Elevation challenges do not mint sessions."}
	}
	if challenge.StepTotal <= 0 {
		return nil, &ErrInvalid{Message: "Challenge has no authentication factors configured."}
	}
	if challenge.StepRemain != 0 {
		return nil, &ErrInvalid{Message: "Challenge not yet completed."}
	}
	now := time.Now().UTC()
	if challenge.ExpiredAt != nil && challenge.ExpiredAt.Time().Before(now) {
		return nil, &ErrInvalid{Message: "Challenge has expired."}
	}

	// Reuse an existing session bound to this challenge.
	var existingSessionID uuid.UUID
	err := s.store.DB.WithContext(ctx).Model(&store.AuthSessionEntity{}).
		Select("id").
		Where("challenge_id = ? AND account_id = ?", challenge.Id, challenge.AccountId).
		Limit(1).Scan(&existingSessionID).Error
	if err == nil && existingSessionID != uuid.Nil {
		_ = s.store.DB.WithContext(ctx).Model(&store.AuthSessionEntity{}).
			Where("id = ?", existingSessionID).Update("last_granted_at", now).Error
		session, err := s.store.GetSessionWithAccount(ctx, existingSessionID)
		if err == nil {
			risk.ReleaseChallenge(ctx, s.redis, deref(challenge.IpAddress), challenge.Id)
			return s.CreateTokenPair(ctx, session)
		}
	}

	device, err := s.GetOrCreateDevice(ctx, challenge.AccountId, challenge.DeviceId, challenge.DeviceName, challenge.Platform)
	if err != nil {
		return nil, err
	}
	refreshLifetime := s.cfg.RefreshTokenLifetime()
	locationJSON, _ := json.Marshal(challenge.Location)
	session := &model.AuthSession{
		Type:            model.SessionTypeLogin,
		LastGrantedAt:   model.NewTime(now),
		ExpiredAt:       model.NewTime(now.Add(refreshLifetime)),
		AccountId:       challenge.AccountId,
		IpAddress:       challenge.IpAddress,
		UserAgent:       challenge.UserAgent,
		Location:        challenge.Location,
		Scopes:          scopesOrEmpty(challenge.Scopes),
		Audiences:       challenge.Audiences,
		ChallengeId:     &challenge.Id,
		ClientId:        &device.Id,
		ParentSessionId: challenge.ApprovedBySessionId,
		Epoch:           0,
	}
	var sessionID uuid.UUID
	err = s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entity := sessionEntityFrom(session)
		if err := tx.Create(&entity).Error; err != nil {
			return err
		}
		sessionID = entity.ID
		// Challenge is consumed.
		challenge.ExpiredAt = model.NewTime(now)
		return tx.Model(&store.ChallengeEntity{}).
			Where("id = ?", challenge.Id).
			Update("expired_at", challenge.ExpiredAt.Time()).Error
	})
	if err != nil {
		return nil, err
	}
	session.Id = sessionID.String()

	pair, err := s.CreateTokenPair(ctx, session)
	if err != nil {
		return nil, err
	}
	if s.logs != nil {
		locText := string(locationJSON)
		sid := sessionID.String()
		_ = s.logs.Create(ctx, challenge.AccountId, model.ActionLogNewLogin, map[string]any{
			"session_type": "Login",
			"challenge_id": challenge.Id,
		}, deref(challenge.UserAgent), deref(challenge.IpAddress), &locText, &sid)
	}
	risk.ClearFailures(ctx, s.redis, deref(challenge.IpAddress))
	// A completed challenge no longer counts against the per-IP new-challenge
	// quota.
	risk.ReleaseChallenge(ctx, s.redis, deref(challenge.IpAddress), challenge.Id)
	return pair, nil
}

// scopesOrEmpty materializes a scope list as an empty JSON array: the
// auth_sessions.scopes column is jsonb NOT NULL and a nil slice would encode as
// JSON null. Login sessions carry only the scopes the client asked for — the
// full-grant wildcard "*" is reserved for OAuth sessions, where
// PermissionScopeGate.HasFullScope honours it.
func scopesOrEmpty(scopes []string) []string {
	if scopes == nil {
		return []string{}
	}
	return scopes
}

// RefreshGraceAccepted reports whether a presented refresh-token epoch is the
// session's immediately previous rotation and still inside [window]. A
// non-positive window disables the grace (previous tokens are rejected
// immediately, the pre-grace behavior).
//
// The window exists because rotation revokes the presented token before the
// replacement is delivered: a dropped or timed-out refresh response (or a
// concurrent duplicate) would otherwise leave the caller holding a dead token
// and force a re-login even though the session is intact.
func RefreshGraceAccepted(session *model.AuthSession, presentedEpoch int, now time.Time, window time.Duration) bool {
	if window <= 0 || session == nil || session.RefreshedAt == nil {
		return false
	}
	if presentedEpoch != session.Epoch-1 {
		return false
	}
	return !session.RefreshedAt.Time().Add(window).Before(now)
}

// RefreshSessionAndIssueTokens rotates a refresh token (epoch bump) and
// returns a new pair plus the rotated session (the session powers the
// middleware auto-renew path without re-loading it). A refresh token from the
// immediately previous rotation is served idempotently while inside the
// configured grace window.
func (s *AuthService) RefreshSessionAndIssueTokens(ctx context.Context, refreshToken string) (*TokenPair, *model.AuthSession, error) {
	isValid, claims := s.jwt.ValidateJwt(refreshToken)
	if !isValid || claims == nil {
		return nil, nil, &ErrInvalid{Message: "Invalid refresh token."}
	}
	if TokenUseOf(claims) != TokenUseRefresh {
		return nil, nil, &ErrInvalid{Message: "Invalid refresh token."}
	}
	jti, ok := ParseUUIDClaim(claims, "jti")
	if !ok {
		return nil, nil, &ErrInvalid{Message: "Invalid refresh token."}
	}
	sessionID, ok := ParseUUIDClaim(claims, "sid")
	if !ok {
		sessionID = jti
	}
	accountID, ok := ParseUUIDClaim(claims, "sub")
	if !ok {
		return nil, nil, &ErrInvalid{Message: "Invalid refresh token."}
	}
	now := time.Now().UTC()
	session, err := s.store.GetSessionWithAccount(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, &ErrInvalid{Message: "Session was not found."}
		}
		return nil, nil, err
	}
	if session.AccountId != accountID.String() {
		return nil, nil, &ErrInvalid{Message: "Session was not found."}
	}
	if session.ExpiredAt != nil && !session.ExpiredAt.Time().After(now) {
		return nil, nil, &ErrInvalid{Message: "Session has been expired."}
	}

	tokenEpoch, hasEpoch := ClaimInt(claims, "epoch")
	if hasEpoch && tokenEpoch != session.Epoch {
		// A token exactly one rotation behind, inside the grace window, is a
		// duplicate of a rotation whose replacement never reached the caller
		// (dropped or timed-out response) or of a concurrent duplicate. Serve
		// it idempotently from the already-rotated session instead of rejecting
		// it — rejecting forces a re-login even though the session is alive.
		// Anything further behind is a replay and stays rejected.
		if RefreshGraceAccepted(session, tokenEpoch, now, s.cfg.RefreshGracePeriod()) {
			pair, err := s.CreateTokenPair(ctx, session)
			if err != nil {
				return nil, nil, err
			}
			return pair, session, nil
		}
		return nil, nil, &ErrInvalid{Message: "Refresh token has been revoked."}
	}

	newExpiry := now.Add(s.cfg.RefreshTokenLifetime())
	rotated, err := s.store.UpdateSessionRefresh(ctx, sessionID.String(), session.Epoch, now, newExpiry)
	if err != nil {
		return nil, nil, err
	}
	if !rotated {
		// Lost a race to a concurrent rotation of the same token (the epoch we
		// held no longer matches). Re-read and, when the winner's rotation is
		// inside the grace window, hand back a pair for its session rather than
		// failing the caller.
		reloaded, rerr := s.store.GetSessionWithAccount(ctx, sessionID)
		if rerr != nil {
			return nil, nil, rerr
		}
		if reloaded.ExpiredAt != nil && !reloaded.ExpiredAt.Time().After(now) {
			return nil, nil, &ErrInvalid{Message: "Session has been expired."}
		}
		if RefreshGraceAccepted(reloaded, session.Epoch, now, s.cfg.RefreshGracePeriod()) {
			pair, perr := s.CreateTokenPair(ctx, reloaded)
			if perr != nil {
				return nil, nil, perr
			}
			return pair, reloaded, nil
		}
		return nil, nil, &ErrInvalid{Message: "Refresh token has been revoked."}
	}
	session.LastGrantedAt = model.NewTime(now)
	session.ExpiredAt = model.NewTime(newExpiry)
	session.RefreshedAt = model.NewTime(now)
	session.Epoch++

	if s.redis != nil && s.redis.Available() {
		_ = s.redis.Cache.Remove(ctx, "auth:session:"+sessionID.String())
		_ = s.redis.Raw.Del(ctx, fmt.Sprintf(SessionTokensGroupFmt, sessionID.String())).Err()
	}

	pair, err := s.CreateTokenPair(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	return pair, session, nil
}

// TrackAuthenticatedActivityAsync throttles (1h) account-active action logs.
func (s *AuthService) TrackAuthenticatedActivity(ctx context.Context, session *model.AuthSession, ipAddress string) {
	if session == nil || s.logs == nil {
		return
	}
	activityKey := "auth:activity:" + session.AccountId
	if s.redis != nil && s.redis.Available() {
		found, err := s.redis.Cache.HasFlag(ctx, activityKey)
		if err == nil && found {
			return
		}
	}
	resolvedIP := ipAddress
	if resolvedIP == "" && session.IpAddress != nil {
		resolvedIP = *session.IpAddress
	}
	appID := ""
	if session.AppId != nil {
		appID = *session.AppId
	}
	_ = s.logs.Create(ctx, session.AccountId, model.ActionLogAccountActive, map[string]any{
		"session_id":   session.Id,
		"session_type": session.Type.String(),
		"app_id":       appID,
	}, deref(session.UserAgent), resolvedIP, nil, &session.Id)
	if s.redis != nil && s.redis.Available() {
		_ = s.redis.Cache.SetFlag(ctx, activityKey, time.Hour)
	}
}

// --- Sudo / elevation ---

// ErrSudoUnavailable reports that the elevation store (Redis) could not be
// consulted. Callers MUST fail closed: treat it as "not elevated" and answer
// 503, never as an implicit elevation.
var ErrSudoUnavailable = errors.New("sudo elevation store is unavailable")

// sudoKey is the Redis key holding a session's elevation grant. The value is
// an opaque flag; the TTL is the grant's remaining lifetime.
func sudoKey(sessionID string) string { return "accounts:" + sessionID + ":sudo" }

// IsSudoElevated reports whether the session currently holds an elevation
// grant. It fails closed: an unavailable cache is returned as an error so the
// caller can answer 503 instead of leaking the request through.
func (s *AuthService) IsSudoElevated(ctx context.Context, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	if s.redis == nil || !s.redis.Available() {
		return false, ErrSudoUnavailable
	}
	found, err := s.redis.Cache.HasFlag(ctx, sudoKey(sessionID))
	if err != nil {
		return false, err
	}
	return found, nil
}

// GrantSudo elevates the session for the configured sudo-mode lifetime and
// returns the instant the grant expires.
func (s *AuthService) GrantSudo(ctx context.Context, sessionID string) (time.Time, error) {
	if s.redis == nil || !s.redis.Available() {
		return time.Time{}, ErrSudoUnavailable
	}
	lifetime := 5 * time.Minute
	if s.cfg != nil {
		lifetime = s.cfg.Security.SudoModeLifetimeDuration()
	}
	until := time.Now().UTC().Add(lifetime)
	if err := s.redis.Cache.SetFlag(ctx, sudoKey(sessionID), lifetime); err != nil {
		return time.Time{}, err
	}
	return until, nil
}

// ClearSudo drops the elevation grants of the given sessions. Best-effort:
// revocation must not fail because the cache is unavailable, and a missing
// grant is not an error.
func (s *AuthService) ClearSudo(ctx context.Context, sessionIDs ...string) {
	if s.redis == nil || !s.redis.Available() {
		return
	}
	for _, id := range sessionIDs {
		if id == "" {
			continue
		}
		_ = s.redis.Cache.Remove(ctx, sudoKey(id))
	}
}

// sudoFactorTypeName maps a factor type to the lowercase name advertised in
// the AUTH_SUDO_REQUIRED detail and used to type the synthetic emailed
// fallback step.
func sudoFactorTypeName(t model.AuthFactorType) string {
	switch t {
	case model.AuthFactorTypePassword:
		return "password"
	case model.AuthFactorTypeEmailCode:
		return "email_code"
	case model.AuthFactorTypeInAppCode:
		return "in_app_code"
	case model.AuthFactorTypeTimedCode:
		return "timed_code"
	case model.AuthFactorTypePinCode:
		return "pin_code"
	case model.AuthFactorTypeRecoveryCode:
		return "recovery_code"
	case model.AuthFactorTypeNfcToken:
		return "nfc_token"
	case model.AuthFactorTypePasskey:
		return "passkey"
	case model.AuthFactorTypeQrLogin:
		return "qr_login"
	default:
		return strconv.Itoa(int(t))
	}
}

// SudoFactorHint returns the comma-joined lowercase factor type names the
// account may use to elevate. Real factors are the enabled, trustworthy
// pickable factors; "email_code" is appended when the account has no real
// factor combination reaching the elevation demand (2) and a verified email
// contact exists (the emailed fallback).
func (s *AuthService) SudoFactorHint(ctx context.Context, accountID string) (string, error) {
	factors, err := s.store.GetAuthFactors(ctx, uuid.MustParse(accountID))
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(factors))
	seen := make(map[model.AuthFactorType]bool, len(factors))
	completable := 0
	for _, f := range factors {
		if f.EnabledAt == nil || f.Trustworthy < 1 {
			continue
		}
		ft := model.AuthFactorType(f.Type)
		if ft == model.AuthFactorTypeRecoveryCode || ft == model.AuthFactorTypePinCode {
			continue
		}
		if !seen[ft] {
			seen[ft] = true
			names = append(names, sudoFactorTypeName(ft))
		}
		switch ft {
		case model.AuthFactorTypePasskey, model.AuthFactorTypeInAppCode, model.AuthFactorTypeQrLogin:
			// Not usable as a pickable factor of an elevation challenge.
		default:
			completable += f.Trustworthy
		}
	}
	if completable < 2 {
		if _, err := s.store.GetEmailContactForNotify(ctx, accountID, true); err == nil {
			names = append(names, "email_code")
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	return strings.Join(names, ","), nil
}

// RecordSudoElevated writes the accounts.sudo.elevate action log.
func (s *AuthService) RecordSudoElevated(ctx context.Context, challenge *model.AuthChallenge) {
	if s.logs == nil || challenge == nil {
		return
	}
	_ = s.logs.Create(ctx, challenge.AccountId, model.ActionLogAccountSudoElevate, map[string]any{
		"challenge_id": challenge.Id,
		"sudo_until":   challenge.SudoUntil,
	}, deref(challenge.UserAgent), deref(challenge.IpAddress), nil, challenge.SessionId)
}

// RecordSudoFailure writes the accounts.sudo.failure action log for a gated
// request that arrived without a live elevation grant.
func (s *AuthService) RecordSudoFailure(ctx context.Context, accountID, sessionID, ipAddress, userAgent string) {
	if s.logs == nil || accountID == "" {
		return
	}
	var sid *string
	if sessionID != "" {
		sid = &sessionID
	}
	_ = s.logs.Create(ctx, accountID, model.ActionLogAccountSudoFailure, map[string]any{}, userAgent, ipAddress, nil, sid)
}

// ValidatePinCode verifies the account's PIN factor.
func (s *AuthService) ValidatePinCode(ctx context.Context, accountID string, pinCode string) (bool, error) {
	factor, err := s.store.GetEnabledFactor(ctx, accountID, model.AuthFactorTypePinCode)
	if err != nil {
		return false, err
	}
	return VerifyFactorPassword(factor, pinCode)
}

// --- Recovery ---

// RecoverAccountWithRecoveryCodeAsync disables non-password factors, revokes
// all sessions, and issues a fresh session + tokens.
func (s *AuthService) RecoverAccountWithRecoveryCode(ctx context.Context, accountID string, recoveryCode, deviceID string, platform model.ClientPlatform, deviceName *string, ipAddress, userAgent string) (*TokenPair, error) {
	factor, err := s.store.GetEnabledFactor(ctx, accountID, model.AuthFactorTypeRecoveryCode)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &ErrInvalid{Message: "Recovery code factor not found."}
		}
		return nil, err
	}
	if factor.Secret != recoveryCode {
		return nil, &ErrInvalid{Message: "Invalid recovery code."}
	}

	now := time.Now().UTC()
	var disabledCount int64
	err = s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Disable all non-password, non-recovery factors + the recovery factor.
		res := tx.Model(&store.AuthFactorEntity{}).
			Where("account_id = ? AND enabled_at IS NOT NULL AND type NOT IN ?",
				accountID, []int{int(model.AuthFactorTypePassword), int(model.AuthFactorTypeRecoveryCode)}).
			Updates(map[string]any{"enabled_at": nil, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		disabledCount = res.RowsAffected
		return tx.Model(&store.AuthFactorEntity{}).
			Where("account_id = ? AND id = ?", accountID, factor.Id).
			Updates(map[string]any{"enabled_at": nil, "updated_at": now}).Error
	})
	if err != nil {
		return nil, err
	}

	revokedCount, err := s.RevokeAllSessionsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	_ = revokedCount

	device, err := s.GetOrCreateDevice(ctx, accountID, deviceID, deviceName, platform)
	if err != nil {
		return nil, err
	}
	location := s.geo.GetPointFromIp(ipAddress)
	locationJSON, _ := json.Marshal(location)
	session := &model.AuthSession{
		Type:          model.SessionTypeLogin,
		LastGrantedAt: model.NewTime(now),
		ExpiredAt:     model.NewTime(now.Add(s.cfg.RefreshTokenLifetime())),
		AccountId:     accountID,
		IpAddress:     &ipAddress,
		UserAgent:     &userAgent,
		Location:      location,
		ClientId:      &device.Id,
		// A recovered account is a fresh login: no app audiences and no
		// full-grant wildcard. Non-empty jsonb is required by the schema.
		Scopes:    []string{},
		Audiences: []string{},
	}
	entity := sessionEntityFrom(session)
	if err := s.store.DB.WithContext(ctx).Create(&entity).Error; err != nil {
		return nil, err
	}
	session.Id = entity.ID.String()
	if s.logs != nil {
		locText := string(locationJSON)
		sid := entity.ID.String()
		_ = s.logs.Create(ctx, accountID, model.ActionLogAccountRecovery, map[string]any{
			"factors_disabled": disabledCount,
			"sessions_revoked": revokedCount,
		}, userAgent, ipAddress, &locText, &sid)
	}
	return s.CreateTokenPair(ctx, session)
}

// --- Devices ---

// GetOrCreateDevice upserts a device (auth_clients) by (account_id, device_id),
// reviving soft-deleted rows and retrying on unique violations.
func (s *AuthService) GetOrCreateDevice(ctx context.Context, accountID, deviceID string, deviceName *string, platform model.ClientPlatform) (*model.AuthClient, error) {
	now := time.Now().UTC()
	revive := func(id uuid.UUID) {
		_ = s.store.DB.WithContext(ctx).Unscoped().Model(&store.AuthClientEntity{}).
			Where("id = ?", id).
			Updates(map[string]any{"deleted_at": nil, "updated_at": now}).Error
	}
	var entity store.AuthClientEntity
	err := s.store.DB.WithContext(ctx).
		Where("device_id = ? AND account_id = ?", deviceID, accountID).
		First(&entity).Error
	if err == nil {
		if entity.DeletedAt.Valid {
			revive(entity.ID)
		}
		return authClientModel(&entity), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	deviceNameValue := ""
	if deviceName != nil {
		deviceNameValue = *deviceName
	}
	entity = store.AuthClientEntity{
		ID:         uuid.New(),
		EntityBase: store.EntityBase{CreatedAt: now, UpdatedAt: now},
		AccountID:  uuid.MustParse(accountID),
		DeviceID:   deviceID,
		DeviceName: deviceNameValue,
		Platform:   int(platform),
	}
	err = s.store.DB.WithContext(ctx).Create(&entity).Error
	if err != nil {
		// Unique violation race: re-read including soft-deleted rows.
		var reRead store.AuthClientEntity
		if err2 := s.store.DB.WithContext(ctx).Unscoped().
			Where("device_id = ? AND account_id = ?", deviceID, accountID).
			First(&reRead).Error; err2 != nil {
			return nil, err
		}
		if reRead.DeletedAt.Valid {
			revive(reRead.ID)
		}
		entity = reRead
	}
	return authClientModel(&entity), nil
}

// authClientModel maps a persisted client entity to the domain model.
func authClientModel(entity *store.AuthClientEntity) *model.AuthClient {
	client := &model.AuthClient{
		Id:          entity.ID.String(),
		DeviceId:    entity.DeviceID,
		DeviceName:  entity.DeviceName,
		DeviceLabel: entity.DeviceLabel,
		AccountId:   entity.AccountID.String(),
		Platform:    model.ClientPlatform(entity.Platform),
		CreatedAt:   model.NewTime(entity.CreatedAt),
		UpdatedAt:   model.NewTime(entity.UpdatedAt),
	}
	if entity.DeletedAt.Valid {
		client.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return client
}

// CreateSessionFromParent creates a child session for login/session flows.
func (s *AuthService) CreateSessionFromParent(ctx context.Context, parentSession *model.AuthSession, deviceID string, deviceName *string, platform model.ClientPlatform, expiredAt *time.Time) (*model.AuthSession, error) {
	now := time.Now().UTC()
	parent, err := s.store.GetSessionWithAccount(ctx, uuid.MustParse(parentSession.Id))
	if err != nil {
		return nil, errors.New("Parent session not found.")
	}
	if parent.ExpiredAt != nil && !parent.ExpiredAt.Time().After(now) {
		return nil, errors.New("Parent session is expired.")
	}
	device, err := s.GetOrCreateDevice(ctx, parentSession.AccountId, deviceID, deviceName, platform)
	if err != nil {
		return nil, err
	}
	finalExpiry := parent.ExpiredAt
	if expiredAt != nil {
		finalExpiry = model.NewTime(*expiredAt)
	}
	if finalExpiry != nil && !finalExpiry.Time().After(now) {
		return nil, errors.New("Requested expiration time is already in the past.")
	}
	session := &model.AuthSession{
		Type:            parent.Type,
		IpAddress:       parent.IpAddress,
		UserAgent:       parent.UserAgent,
		Location:        parent.Location,
		AccountId:       parent.AccountId,
		LastGrantedAt:   model.NewTime(now),
		ExpiredAt:       finalExpiry,
		ParentSessionId: &parent.Id,
		ClientId:        &device.Id,
		Audiences:       parent.Audiences,
		Scopes:          parent.Scopes,
		AppId:           parent.AppId,
	}
	entity := sessionEntityFrom(session)
	if err := s.store.DB.WithContext(ctx).Create(&entity).Error; err != nil {
		return nil, err
	}
	session.Id = entity.ID.String()
	return session, nil
}

func (s *AuthService) resolveAccessExpiry(session *model.AuthSession, now time.Time) time.Time {
	target := now.Add(s.cfg.AccessTokenLifetime())
	if session.ExpiredAt != nil && session.ExpiredAt.Time().Before(target) {
		return session.ExpiredAt.Time()
	}
	return target
}

func (s *AuthService) hydratePerk(ctx context.Context, account *model.Account) {
	s.token.HydratePerk(ctx, account)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
