package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/geo"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
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
func (s *AuthService) ValidateCaptcha(ctx context.Context, token string) (bool, error) {
	if s.cfg == nil || !s.cfg.CaptchaEnabled() {
		return true, nil
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

// RevokeSession revokes a session and all its descendants (BFS on
// parent_session_id), bumping epochs and account versions and publishing
// auth.session.revoked events.
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
	accounts := map[string]struct{}{}
	for _, r := range revoked {
		accounts[r.AccountID] = struct{}{}
	}
	for accountID := range accounts {
		if _, err := s.token.BumpAccountVersion(ctx, accountID); err != nil {
			s.log.Warn("bump account version", "error", err)
		}
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
	if _, err := s.token.BumpAccountVersion(ctx, accountID); err != nil {
		s.log.Warn("bump account version", "error", err)
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
		rows, err := s.store.Query(ctx,
			`SELECT id FROM auth_sessions WHERE parent_session_id = ANY($1)`, frontier)
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			frontier = append(frontier, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
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
	version, err := s.token.GetAccountVersion(ctx, account.Id)
	if err != nil {
		return "", err
	}
	expires := s.resolveAccessExpiry(session, time.Now().UTC())
	return s.jwt.CreateUserToken(session, account, version, expires)
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
	version, err := s.token.GetAccountVersion(ctx, account.Id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	accessExpires := s.resolveAccessExpiry(session, now)
	refreshExpires := now.Add(s.cfg.RefreshTokenLifetime())
	if session.ExpiredAt != nil {
		refreshExpires = session.ExpiredAt.Time()
	}
	access, err := s.jwt.CreateUserToken(session, account, version, accessExpires)
	if err != nil {
		return nil, err
	}
	refresh, err := s.jwt.CreateRefreshToken(session, version, refreshExpires)
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
	var existingSessionID *uuid.UUID
	err := s.store.QueryRow(ctx,
		`SELECT id FROM auth_sessions WHERE challenge_id = $1 AND account_id = $2 LIMIT 1`,
		challenge.Id, challenge.AccountId).Scan(&existingSessionID)
	if err == nil && existingSessionID != nil {
		_, _ = s.store.Exec(ctx,
			`UPDATE auth_sessions SET last_granted_at = $1 WHERE id = $2`, now, *existingSessionID)
		session, err := s.store.GetSessionWithAccount(ctx, *existingSessionID)
		if err == nil {
			return s.CreateTokenPair(ctx, session)
		}
	}

	tx, err := s.store.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

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
		Scopes:          scopesWithFullScope(challenge.Scopes),
		Audiences:       challenge.Audiences,
		ChallengeId:     &challenge.Id,
		ClientId:        &device.Id,
		ParentSessionId: challenge.ApprovedBySessionId,
		Epoch:           0,
	}
	var sessionID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO auth_sessions
		(id, type, last_granted_at, expired_at, account_id, ip_address, user_agent, location, scopes, audiences,
		 challenge_id, client_id, parent_session_id, epoch, created_at, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)
		RETURNING id`,
		int(session.Type), session.LastGrantedAt, session.ExpiredAt, session.AccountId,
		session.IpAddress, session.UserAgent, locationJSON,
		session.Scopes, session.Audiences, session.ChallengeId, session.ClientId,
		session.ParentSessionId, session.Epoch, now,
	).Scan(&sessionID)
	if err != nil {
		return nil, err
	}
	session.Id = sessionID.String()

	// Challenge is consumed.
	challenge.ExpiredAt = model.NewTime(now)
	_, err = tx.Exec(ctx, `UPDATE auth_challenges SET expired_at = $1 WHERE id = $2`, challenge.ExpiredAt, challenge.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

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
	return pair, nil
}

// fullScope is the wildcard scope that grants everything, mirroring
// PermissionScopeGate.HasFullScope in DysonNetwork.Shared.
const fullScope = "*"

// scopesWithFullScope appends the full-grant wildcard scope when missing.
// Normal login sessions always carry it so they bypass permission checks
// (HasFullScope), matching the C# semantics.
func scopesWithFullScope(scopes []string) []string {
	out := append([]string{}, scopes...)
	for _, scope := range out {
		if scope == fullScope {
			return out
		}
	}
	return append(out, fullScope)
}

// RefreshSessionAndIssueTokens rotates a refresh token (epoch bump) and
// returns a new pair plus the rotated session (the session powers the
// middleware auto-renew path without re-loading it).
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
	if tokenVer, ok := ClaimInt(claims, "ver"); ok {
		currentVer, err := s.token.GetAccountVersion(ctx, accountID.String())
		if err != nil {
			return nil, nil, err
		}
		if tokenVer < currentVer {
			return nil, nil, &ErrInvalid{Message: "Refresh token has been invalidated."}
		}
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
	if tokenEpoch, ok := ClaimInt(claims, "epoch"); ok && tokenEpoch != session.Epoch {
		return nil, nil, &ErrInvalid{Message: "Refresh token has been revoked."}
	}

	newExpiry := now.Add(s.cfg.RefreshTokenLifetime())
	_, err = s.store.Exec(ctx,
		`UPDATE auth_sessions SET last_granted_at = $1, expired_at = $2, epoch = epoch + 1 WHERE id = $3`,
		now, newExpiry, sessionID)
	if err != nil {
		return nil, nil, err
	}
	session.LastGrantedAt = model.NewTime(now)
	session.ExpiredAt = model.NewTime(newExpiry)
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

// --- Sudo / PIN ---

// ValidateSudoMode mirrors the sudo cache + PIN check.
func (s *AuthService) ValidateSudoMode(ctx context.Context, session *model.AuthSession, pinCode *string) (bool, error) {
	if session == nil {
		return false, nil
	}
	sudoKey := "accounts:" + session.Id + ":sudo"
	if s.redis != nil && s.redis.Available() {
		if found, _ := s.redis.Cache.HasFlag(ctx, sudoKey); found {
			return true, nil
		}
	}
	hasPin, err := s.hasEnabledFactor(ctx, session.AccountId, model.AuthFactorTypePinCode)
	if err != nil {
		return false, err
	}
	if !hasPin {
		return true, nil
	}
	if pinCode == nil || *pinCode == "" {
		return false, nil
	}
	valid, err := s.ValidatePinCode(ctx, session.AccountId, *pinCode)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	if valid && s.redis != nil && s.redis.Available() {
		_ = s.redis.Cache.SetFlag(ctx, sudoKey, 5*time.Minute)
	}
	return valid, nil
}

// ValidatePinCode verifies the account's PIN factor.
func (s *AuthService) ValidatePinCode(ctx context.Context, accountID string, pinCode string) (bool, error) {
	factor, err := s.store.GetEnabledFactor(ctx, accountID, model.AuthFactorTypePinCode)
	if err != nil {
		return false, err
	}
	return VerifyFactorPassword(factor, pinCode)
}

func (s *AuthService) hasEnabledFactor(ctx context.Context, accountID string, ftype model.AuthFactorType) (bool, error) {
	return s.store.HasEnabledFactor(ctx, accountID, ftype)
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

	tx, err := s.store.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Disable all non-password, non-recovery factors + the recovery factor.
	disabled, err := tx.Exec(ctx, `UPDATE account_auth_factors SET enabled_at = NULL
		WHERE account_id = $1 AND enabled_at IS NOT NULL
		AND type NOT IN ($2, $3)`,
		accountID, int(model.AuthFactorTypePassword), int(model.AuthFactorTypeRecoveryCode))
	if err != nil {
		return nil, err
	}
	disabledCount := disabled.RowsAffected()
	_, err = tx.Exec(ctx, `UPDATE account_auth_factors SET enabled_at = NULL
		WHERE account_id = $1 AND id = $2`, accountID, factor.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
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
	now := time.Now().UTC()
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
	}
	var sessionID uuid.UUID
	err = s.store.QueryRow(ctx, `INSERT INTO auth_sessions
		(id, type, created_at, last_granted_at, expired_at, account_id, ip_address, user_agent, location, client_id, epoch, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$2,$3,$4,$5,$6,$7,$8,0,$2) RETURNING id`,
		int(session.Type), now, session.ExpiredAt, session.AccountId, session.IpAddress,
		session.UserAgent, locationJSON, session.ClientId).Scan(&sessionID)
	if err != nil {
		return nil, err
	}
	session.Id = sessionID.String()
	if s.logs != nil {
		locText := string(locationJSON)
		sid := sessionID.String()
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
	var device model.AuthClient
	err := s.store.QueryRow(ctx, `SELECT id, device_id, device_name, device_label, account_id, platform, created_at, updated_at, deleted_at
		FROM auth_clients WHERE device_id = $1 AND account_id = $2`,
		deviceID, accountID).Scan(&device.Id, &device.DeviceId, &device.DeviceName, &device.DeviceLabel,
		&device.AccountId, &device.Platform, &device.CreatedAt, &device.UpdatedAt, &device.DeletedAt)
	if err == nil {
		if device.DeletedAt != nil {
			_, _ = s.store.Exec(ctx,
				`UPDATE auth_clients SET deleted_at = NULL, updated_at = $1 WHERE id = $2`, now, device.Id)
		}
		return &device, nil
	}
	device = model.AuthClient{
		Platform:  platform,
		DeviceId:  deviceID,
		AccountId: accountID,
	}
	if deviceName != nil {
		device.DeviceName = *deviceName
	}
	device.Id = uuid.NewString()
	err = s.store.QueryRow(ctx, `INSERT INTO auth_clients (id, platform, device_id, account_id, device_name, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6) RETURNING id`,
		device.Id, int(platform), deviceID, accountID, device.DeviceName, now).Scan(&device.Id)
	if err != nil {
		// Unique violation race: re-read including soft-deleted rows.
		row := s.store.QueryRow(ctx, `SELECT id, device_id, device_name, device_label, account_id, platform, created_at, updated_at, deleted_at
			FROM auth_clients WHERE device_id = $1 AND account_id = $2`,
			deviceID, accountID)
		err2 := row.Scan(&device.Id, &device.DeviceId, &device.DeviceName, &device.DeviceLabel,
			&device.AccountId, &device.Platform, &device.CreatedAt, &device.UpdatedAt, &device.DeletedAt)
		if err2 != nil {
			return nil, err
		}
		if device.DeletedAt != nil {
			_, _ = s.store.Exec(ctx,
				`UPDATE auth_clients SET deleted_at = NULL, updated_at = $1 WHERE id = $2`, now, device.Id)
		}
	}
	return &device, nil
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
	var sessionID uuid.UUID
	locJSON, _ := json.Marshal(session.Location)
	err = s.store.QueryRow(ctx, `INSERT INTO auth_sessions
		(id, type, created_at, last_granted_at, expired_at, account_id, ip_address, user_agent, location,
		 parent_session_id, client_id, audiences, scopes, app_id, epoch, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,0,$2) RETURNING id`,
		int(session.Type), now, session.ExpiredAt, session.AccountId, session.IpAddress, session.UserAgent,
		locJSON, session.ParentSessionId, session.ClientId, session.Audiences, session.Scopes, session.AppId).Scan(&sessionID)
	if err != nil {
		return nil, err
	}
	session.Id = sessionID.String()
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
