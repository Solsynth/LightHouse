package auth

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// sessionEntityFrom converts a domain session into the persisted entity.
// Audiences/scopes are always materialized as JSON arrays: the schema
// declares them jsonb NOT NULL, and a nil datatypes.JSON would insert NULL.
func sessionEntityFrom(session *model.AuthSession) store.AuthSessionEntity {
	now := time.Now().UTC()
	var location *datatypes.JSON
	if session.Location != nil {
		if raw, err := json.Marshal(session.Location); err == nil {
			loc := datatypes.JSON(raw)
			location = &loc
		}
	}
	return store.AuthSessionEntity{
		ID:              uuid.New(),
		CreatedAt:       now,
		UpdatedAt:       now,
		AccountID:       uuid.MustParse(session.AccountId),
		AppID:           uuidPtr(session.AppId),
		Audiences:       datatypes.JSON(mustJSON(session.Audiences)),
		ChallengeID:     uuidPtr(session.ChallengeId),
		ClientID:        uuidPtr(session.ClientId),
		Epoch:           session.Epoch,
		ExpiredAt:       modelTimePtr(session.ExpiredAt),
		IPAddress:       session.IpAddress,
		LastGrantedAt:   modelTimePtr(session.LastGrantedAt),
		Location:        location,
		ParentSessionID: uuidPtr(session.ParentSessionId),
		Scopes:          datatypes.JSON(mustJSON(session.Scopes)),
		Type:            int(session.Type),
		UserAgent:       session.UserAgent,
	}
}

// mustJSON marshals a slice as JSON, falling back to an empty array.
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return raw
}

// modelTimePtr converts a domain *Time to a *time.Time (nil passthrough).
func modelTimePtr(t *model.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := time.Time(*t)
	return &v
}

// modelTimeFrom converts a *time.Time to a domain *Time (nil passthrough).
func modelTimeFrom(t *time.Time) *model.Time {
	if t == nil {
		return nil
	}
	return model.NewTime(*t)
}

// apiKeyModel maps a persisted API key entity to the domain model.
func apiKeyModel(entity *store.APIKeyEntity) *model.ApiKey {
	key := &model.ApiKey{
		Id:        entity.ID.String(),
		Label:     entity.Label,
		AccountId: entity.AccountID.String(),
		AppId:     uuidPtrToStr(entity.AppID),
		SessionId: entity.SessionID.String(),
		CreatedAt: model.NewTime(entity.CreatedAt),
		UpdatedAt: model.NewTime(entity.UpdatedAt),
	}
	if entity.DeletedAt.Valid {
		key.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return key
}

// authorizedAppModel maps a persisted authorized app to the domain model.
func authorizedAppModel(entity *store.AuthorizedAppEntity) *model.AuthorizedApp {
	app := &model.AuthorizedApp{
		Id:               entity.ID.String(),
		Type:             model.AuthorizedAppType(entity.Type),
		AccountId:        entity.AccountID.String(),
		AppId:            entity.AppID.String(),
		AppSlug:          entity.AppSlug,
		AppName:          entity.AppName,
		LastAuthorizedAt: model.NewTime(entity.LastAuthorizedAt),
		LastUsedAt:       modelTimeFrom(entity.LastUsedAt),
		CreatedAt:        model.NewTime(entity.CreatedAt),
		UpdatedAt:        model.NewTime(entity.UpdatedAt),
	}
	_ = json.Unmarshal(entity.Scopes, &app.Scopes)
	if entity.DeletedAt.Valid {
		app.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return app
}
