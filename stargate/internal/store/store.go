// Package store centralizes persistence queries and maps database entities to
// the API/domain model types.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

const accountColumns = `id, name, nick, language, region, activated_at, is_superuser, automated_id, created_at, updated_at, deleted_at`
const profileColumns = `p.id, p.first_name, p.middle_name, p.last_name, p.bio, p.gender, p.pronouns, p.time_zone, p.location, p.links, p.username_color, p.birthday, p.last_seen_at, p.verification, p.active_badge, p.experience, p.social_credits, p.picture, p.background, p.account_id, p.created_at, p.updated_at, p.deleted_at`

func accountColsPrefixed(alias string) string {
	return alias + `.id, ` + alias + `.name, ` + alias + `.nick, ` + alias + `.language, ` + alias + `.region, ` + alias + `.activated_at, ` + alias + `.is_superuser, ` + alias + `.automated_id, ` + alias + `.created_at, ` + alias + `.updated_at, ` + alias + `.deleted_at`
}

func profileColsPrefixed(alias string) string {
	return alias + `.id, ` + alias + `.first_name, ` + alias + `.middle_name, ` + alias + `.last_name, ` + alias + `.bio, ` + alias + `.gender, ` + alias + `.pronouns, ` + alias + `.time_zone, ` + alias + `.location, ` + alias + `.links, ` + alias + `.username_color, ` + alias + `.birthday, ` + alias + `.last_seen_at, ` + alias + `.verification, ` + alias + `.active_badge, ` + alias + `.experience, ` + alias + `.social_credits, ` + alias + `.picture, ` + alias + `.background, ` + alias + `.account_id, ` + alias + `.created_at, ` + alias + `.updated_at, ` + alias + `.deleted_at`
}

func scanAccount(row rowScanner) (*model.Account, error) {
	account := &model.Account{}
	var automatedID *uuid.UUID
	if err := row.Scan(&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region,
		&account.ActivatedAt, &account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	account.AutomatedId = uuidPtrStr(automatedID)
	return account, nil
}

func scanAccountWithProfile(row rowScanner) (*model.Account, error) {
	account := &model.Account{}
	profile := &model.Profile{}
	var automatedID *uuid.UUID
	var profileID, profileAccountID *string
	var firstName, middleName, lastName, bio, gender, pronouns, timeZone, location *string
	var birthday, lastSeenAt *model.Time
	var experience *int
	var socialCredits *float64
	var links, usernameColor, verification, activeBadge, picture, background []byte
	var profileCreated, profileUpdated, profileDeleted *model.Time
	if err := row.Scan(&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region,
		&account.ActivatedAt, &account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt,
		&profileID, &firstName, &middleName, &lastName, &bio, &gender, &pronouns, &timeZone, &location,
		&links, &usernameColor, &birthday, &lastSeenAt, &verification, &activeBadge, &experience, &socialCredits,
		&picture, &background, &profileAccountID, &profileCreated, &profileUpdated, &profileDeleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	account.AutomatedId = uuidPtrStr(automatedID)
	if profileID != nil {
		profile.Id, profile.AccountId = *profileID, *profileID
		if profileAccountID != nil {
			profile.AccountId = *profileAccountID
		}
		profile.FirstName, profile.MiddleName, profile.LastName = firstName, middleName, lastName
		profile.Bio, profile.Gender, profile.Pronouns, profile.TimeZone, profile.Location = bio, gender, pronouns, timeZone, location
		profile.Birthday, profile.LastSeenAt = birthday, lastSeenAt
		if experience != nil {
			profile.Experience = *experience
		}
		if socialCredits != nil {
			profile.SocialCredits = *socialCredits
		}
		profile.CreatedAt, profile.UpdatedAt, profile.DeletedAt = profileCreated, profileUpdated, profileDeleted
		_ = json.Unmarshal(links, &profile.Links)
		_ = json.Unmarshal(usernameColor, &profile.UsernameColor)
		_ = json.Unmarshal(verification, &profile.Verification)
		_ = decodeActiveBadge(profile, activeBadge)
		_ = json.Unmarshal(picture, &profile.Picture)
		_ = json.Unmarshal(background, &profile.Background)
		profile.ComputeLeveling()
		account.Profile = profile
	}
	return account, nil
}

var ErrNotFound = errors.New("not found")

// Store wraps the shared GORM handle. Callers intentionally share this handle
// so service-level transactions can span store and runtime consumer writes.
type Store struct {
	DB *gorm.DB
}

func New(database any) *Store {
	if handle, ok := database.(*gorm.DB); ok {
		return &Store{DB: handle}
	}
	value := reflect.ValueOf(database)
	configMethod := value.MethodByName("Config")
	if configMethod.IsValid() {
		results := configMethod.Call(nil)
		if len(results) == 1 {
			config := results[0]
			connConfig := config.Elem().FieldByName("ConnConfig")
			if connConfig.IsValid() {
				connString := connConfig.MethodByName("ConnString")
				if connString.IsValid() {
					dsn := connString.Call(nil)[0].String()
					handle, err := gorm.Open(postgres.Open(dsn), &gorm.Config{SkipDefaultTransaction: true})
					if err == nil {
						return &Store{DB: handle}
					}
				}
			}
		}
	}
	panic("store.New requires *gorm.DB")
}

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

func mapNotFound(err error) error {
	if isNotFound(err) {
		return ErrNotFound
	}
	return err
}

func timePtr(value *time.Time) *model.Time {
	if value == nil {
		return nil
	}
	return model.NewTime(*value)
}

func timeValue(value *model.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := time.Time(*value)
	return &result
}

func deletedTime(value gorm.DeletedAt) *model.Time {
	if !value.Valid {
		return nil
	}
	return model.NewTime(value.Time)
}

func uuidPtrStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	value := id.String()
	return &value
}

func ParseUUID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid uuid %q: %w", value, err)
	}
	return id, nil
}

func accountFromEntity(entity *AccountEntity) *model.Account {
	if entity == nil {
		return nil
	}
	return &model.Account{
		Id: entity.ID.String(), Name: entity.Name, Nick: entity.Nick,
		Language: entity.Language, Region: entity.Region,
		ActivatedAt: timePtr(entity.ActivatedAt), IsSuperuser: entity.IsSuperuser,
		AutomatedId: uuidPtrStr(entity.AutomatedID), CreatedAt: timePtr(&entity.CreatedAt),
		UpdatedAt: timePtr(&entity.UpdatedAt), DeletedAt: deletedTime(entity.DeletedAt),
	}
}

func profileFromEntity(entity *ProfileEntity) *model.Profile {
	if entity == nil {
		return nil
	}
	profile := &model.Profile{
		Id: entity.ID.String(), FirstName: entity.FirstName, MiddleName: entity.MiddleName,
		LastName: entity.LastName, Bio: entity.Bio, Gender: entity.Gender,
		Pronouns: entity.Pronouns, TimeZone: entity.TimeZone, Location: entity.Location,
		Birthday: timePtr(entity.Birthday), LastSeenAt: timePtr(entity.LastSeenAt),
		Experience: entity.Experience, SocialCredits: entity.SocialCredits,
		AccountId: entity.AccountID.String(), CreatedAt: timePtr(&entity.CreatedAt),
		UpdatedAt: timePtr(&entity.UpdatedAt), DeletedAt: deletedTime(entity.DeletedAt),
	}
	_ = decodeJSON(entity.Links, &profile.Links)
	_ = decodeJSON(entity.UsernameColor, &profile.UsernameColor)
	_ = decodeJSON(entity.Verification, &profile.Verification)
	if entity.ActiveBadge != nil {
		_ = decodeActiveBadge(profile, []byte(*entity.ActiveBadge))
	}
	_ = decodeJSON(entity.Picture, &profile.Picture)
	_ = decodeJSON(entity.Background, &profile.Background)
	profile.ComputeLeveling()
	return profile
}

func sessionFromEntity(entity *AuthSessionEntity) *model.AuthSession {
	if entity == nil {
		return nil
	}
	session := &model.AuthSession{
		Id: entity.ID.String(), Type: model.SessionType(entity.Type),
		LastGrantedAt: timePtr(entity.LastGrantedAt), ExpiredAt: timePtr(entity.ExpiredAt),
		AccountId: entity.AccountID.String(), IpAddress: entity.IPAddress,
		UserAgent: entity.UserAgent, CreatedAt: timePtr(&entity.CreatedAt),
		UpdatedAt: timePtr(&entity.UpdatedAt), DeletedAt: deletedTime(entity.DeletedAt),
		ClientId: uuidPtrStr(entity.ClientID), ParentSessionId: uuidPtrStr(entity.ParentSessionID),
		ChallengeId: uuidPtrStr(entity.ChallengeID), AppId: uuidPtrStr(entity.AppID),
		Epoch: entity.Epoch,
	}
	_ = decodeJSONValue(entity.Audiences, &session.Audiences)
	_ = decodeJSONValue(entity.Scopes, &session.Scopes)
	_ = decodeJSON(entity.Location, &session.Location)
	return session
}

func authClientFromEntity(entity *AuthClientEntity) model.AuthClient {
	return model.AuthClient{Id: entity.ID.String(), DeviceId: entity.DeviceID,
		DeviceName: entity.DeviceName, DeviceLabel: entity.DeviceLabel,
		AccountId: entity.AccountID.String(), Platform: model.ClientPlatform(entity.Platform),
		CreatedAt: timePtr(&entity.CreatedAt), UpdatedAt: timePtr(&entity.UpdatedAt),
		DeletedAt: deletedTime(entity.DeletedAt)}
}

func factorFromEntity(entity *AuthFactorEntity) model.AuthFactor {
	factor := model.AuthFactor{Id: entity.ID.String(), Type: model.AuthFactorType(entity.Type),
		Trustworthy: entity.Trustworthy, AccountId: entity.AccountID.String(),
		EnabledAt: timePtr(entity.EnabledAt), ExpiredAt: timePtr(entity.ExpiredAt),
		CreatedAt: timePtr(&entity.CreatedAt), UpdatedAt: timePtr(&entity.UpdatedAt),
		DeletedAt: deletedTime(entity.DeletedAt)}
	if entity.Secret != nil {
		factor.Secret = *entity.Secret
	}
	_ = decodeJSON(entity.Config, &factor.Config)
	return factor
}

func (s *Store) TouchLastActive(ctx context.Context, accountID, sessionID string, seenAt time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if accountID != "" {
			if err := tx.Model(&ProfileEntity{}).Where("account_id = ?", accountID).Updates(map[string]any{
				"last_seen_at": seenAt, "updated_at": seenAt,
			}).Error; err != nil {
				return err
			}
		}
		if sessionID != "" {
			if err := tx.Model(&AuthSessionEntity{}).Where("id = ?", sessionID).Update("last_granted_at", seenAt).Error; err != nil {
				return err
			}
			if err := tx.Model(&AuthSessionEntity{}).Where("id = ? AND expired_at IS NOT NULL", sessionID).
				Update("expired_at", gorm.Expr("?::timestamptz + interval '7 days'", seenAt)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetAccountByID(ctx context.Context, id uuid.UUID) (*model.Account, error) {
	var entity AccountEntity
	if err := s.DB.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return accountFromEntity(&entity), nil
}

func (s *Store) GetAccountByName(ctx context.Context, name string) (*model.Account, error) {
	var entity AccountEntity
	if err := s.DB.WithContext(ctx).Where("name = ?", name).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return accountFromEntity(&entity), nil
}

func (s *Store) GetAccountWithProfile(ctx context.Context, id uuid.UUID) (*model.Account, error) {
	account, err := s.GetAccountByID(ctx, id)
	if err != nil {
		return nil, err
	}
	var profile ProfileEntity
	result := s.DB.WithContext(ctx).Where("account_id = ?", id).First(&profile)
	if result.Error == nil {
		account.Profile = profileFromEntity(&profile)
	} else if !isNotFound(result.Error) {
		return nil, result.Error
	}
	return account, nil
}

func (s *Store) GetAccountWithProfileByName(ctx context.Context, name string) (*model.Account, error) {
	account, err := s.GetAccountByName(ctx, name)
	if err != nil {
		return nil, err
	}
	var profile ProfileEntity
	result := s.DB.WithContext(ctx).Where("account_id = ?", account.Id).First(&profile)
	if result.Error == nil {
		account.Profile = profileFromEntity(&profile)
	} else if !isNotFound(result.Error) {
		return nil, result.Error
	}
	return account, nil
}

func (s *Store) GetSessionWithAccount(ctx context.Context, id uuid.UUID) (*model.AuthSession, error) {
	var entity AuthSessionEntity
	if err := s.DB.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	session := sessionFromEntity(&entity)
	account, err := s.GetAccountByID(ctx, entity.AccountID)
	if err != nil {
		return nil, err
	}
	session.Account = account
	return session, nil
}

func (s *Store) GetAuthFactors(ctx context.Context, accountID uuid.UUID) ([]model.AuthFactor, error) {
	var entities []AuthFactorEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	factors := make([]model.AuthFactor, 0, len(entities))
	for i := range entities {
		factors = append(factors, factorFromEntity(&entities[i]))
	}
	return factors, nil
}
