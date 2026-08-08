package auth

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// CreateSessionForOidc creates an OAuth/OIDC session, mirroring
// AuthService.CreateSessionForOidcAsync. customAppID != nil produces an OAuth
// session (authorizing a third-party app) with type=OAuth (1); nil produces
// an Oidc session (2). parentSessionID links sub-sessions (device flow).
func (s *AuthService) CreateSessionForOidc(ctx context.Context, db *pgxpool.Pool, accountID string, customAppID *string, parentSessionID *string, ipAddress, userAgent string) (*model.AuthSession, error) {
	now := time.Now().UTC()
	location := s.geo.GetPointFromIp(ipAddress)
	locationJSON, _ := json.Marshal(location)
	sessionType := model.SessionTypeOidc
	if customAppID != nil {
		sessionType = model.SessionTypeOAuth
	}

	var ipPtr *string
	if ipAddress != "" {
		ipPtr = &ipAddress
	}
	var uaPtr *string
	if userAgent != "" {
		uaPtr = &userAgent
	}
	var appIDUUID *uuid.UUID
	if customAppID != nil {
		if id, err := uuid.Parse(*customAppID); err == nil {
			appIDUUID = &id
		}
	}
	var parentUUID *uuid.UUID
	if parentSessionID != nil {
		if id, err := uuid.Parse(*parentSessionID); err == nil {
			parentUUID = &id
		}
	}

	session := &model.AuthSession{
		Type:            sessionType,
		AccountId:       accountID,
		IpAddress:       ipPtr,
		UserAgent:       uaPtr,
		Location:        location,
		AppId:           customAppID,
		ParentSessionId: parentSessionID,
		CreatedAt:       model.NewTime(now),
		LastGrantedAt:   model.NewTime(now),
		Audiences:       []string{},
		Scopes:          []string{},
	}
	var sessionID uuid.UUID
	err := db.QueryRow(ctx, `INSERT INTO auth_sessions
		(id, type, created_at, last_granted_at, account_id, ip_address, user_agent, location, app_id,
		 parent_session_id, audiences, scopes, epoch, updated_at)
		VALUES (gen_random_uuid(),$1,$2,$2,$3,$4,$5,$6,$7,$8,$9,$10,0,$2) RETURNING id`,
		int(sessionType), now, accountID, ipPtr, uaPtr, locationJSON, appIDUUID, parentUUID,
		session.Audiences, session.Scopes).Scan(&sessionID)
	if err != nil {
		return nil, err
	}
	session.Id = sessionID.String()

	if s.logs != nil {
		appIDText := ""
		if customAppID != nil {
			appIDText = *customAppID
		}
		locText := string(locationJSON)
		sid := sessionID.String()
		_ = s.logs.Create(ctx, accountID, model.ActionLogNewLogin, map[string]any{
			"session_type": sessionType.String(),
			"app_id":       appIDText,
		}, deref(uaPtr), deref(ipPtr), &locText, &sid)
	}
	return session, nil
}
