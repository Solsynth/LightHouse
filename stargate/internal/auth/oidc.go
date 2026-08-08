package auth

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// CreateSessionForOidc creates an OAuth/OIDC session, mirroring
// AuthService.CreateSessionForOidcAsync. customAppID != nil produces an OAuth
// session (authorizing a third-party app) with type=OAuth (1); nil produces
// an Oidc session (2). parentSessionID links sub-sessions (device flow).
func (s *AuthService) CreateSessionForOidc(ctx context.Context, database *gorm.DB, accountID string, customAppID *string, parentSessionID *string, ipAddress, userAgent string) (*model.AuthSession, error) {
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
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	audiences, _ := json.Marshal(session.Audiences)
	scopes, _ := json.Marshal(session.Scopes)
	var locationValue *datatypes.JSON
	if location != nil {
		encoded, _ := json.Marshal(location)
		value := datatypes.JSON(encoded)
		locationValue = &value
	}
	sessionID := uuid.New()
	if err := database.WithContext(ctx).Create(&store.AuthSessionEntity{
		ID: sessionID, AccountID: accountUUID, AppID: appIDUUID, ParentSessionID: parentUUID,
		Type: int(sessionType), Audiences: datatypes.JSON(audiences), Scopes: datatypes.JSON(scopes),
		IPAddress: ipPtr, UserAgent: uaPtr, Location: locationValue, Epoch: 0,
		LastGrantedAt: &now,
	}).Error; err != nil {
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
