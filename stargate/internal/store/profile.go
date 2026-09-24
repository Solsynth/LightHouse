package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Profile helpers for the Passport-moved profile surface. All queries target
// the account_profiles table (snake_case, timestamptz instants, jsonb blobs).

// GetProfileByAccount loads an account's 1:1 profile row.
func (s *Store) GetProfileByAccount(ctx context.Context, accountID uuid.UUID) (*model.Profile, error) {
	var entity ProfileEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return profileFromEntity(&entity), nil
}

// profileFromEntity maps an account_profiles row to the API model. jsonb
// columns are nullable (*datatypes.JSON): nil is SQL NULL, the literal bytes
// "null" are a JSON null — the two stay distinct all the way through
// decodeActiveBadge (the SDK reads `active_badge == null` as "no badge").
func profileFromEntity(entity *ProfileEntity) *model.Profile {
	profile := &model.Profile{
		Id:            entity.ID.String(),
		FirstName:     entity.FirstName,
		MiddleName:    entity.MiddleName,
		LastName:      entity.LastName,
		Bio:           entity.Bio,
		Gender:        entity.Gender,
		Pronouns:      entity.Pronouns,
		TimeZone:      entity.TimeZone,
		Location:      entity.Location,
		Birthday:      timePtr(entity.Birthday),
		LastSeenAt:    timePtr(entity.LastSeenAt),
		Experience:    entity.Experience,
		SocialCredits: entity.SocialCredits,
		AccountId:     entity.AccountID.String(),
		CreatedAt:     timePtr(&entity.CreatedAt),
		UpdatedAt:     timePtr(&entity.UpdatedAt),
		DeletedAt:     deletedTime(entity.DeletedAt),
	}
	profile.ComputeLeveling()
	_ = decodeJSON(entity.Links, &profile.Links)
	_ = decodeJSON(entity.UsernameColor, &profile.UsernameColor)
	_ = decodeJSON(entity.Verification, &profile.Verification)
	_ = decodeJSON(entity.Picture, &profile.Picture)
	_ = decodeJSON(entity.Background, &profile.Background)
	var activeBadge []byte
	if entity.ActiveBadge != nil {
		activeBadge = *entity.ActiveBadge
	}
	_ = decodeActiveBadge(profile, activeBadge)
	return profile
}

// isBareProfile reports whether a profile carries no user-authored data at all
// (no first/last name, no bio, no picture). Such rows are what migrated
// accounts that never edited their profile end up with — and what the old
// Passport created on demand. The Solian client refuses to cache/serve a
// profile it deems bare (it treats the empty shell as a server-side
// fallback), so every read path that surfaces a profile to a client must
// ensure bare rows are healed before serialization.
func isBareProfile(p *model.Profile) bool {
	if p == nil {
		return true
	}
	nonBlank := func(s *string) bool { return s != nil && strings.TrimSpace(*s) != "" }
	return !nonBlank(p.FirstName) &&
		!nonBlank(p.LastName) &&
		!nonBlank(p.Bio) &&
		p.Picture == nil
}

// loadProfileForAccount loads an account's profile, healing bare (data-less)
// shells so no read path surfaces an empty profile to a client (the Solian
// client refuses to cache/serve a profile with no name/bio/picture). It
// returns nil when the account has no profile row — callers create on demand
// via GetOrCreateAccountProfile — preserving the existing read contract.
func (s *Store) loadProfileForAccount(ctx context.Context, accountID uuid.UUID) (*model.Profile, error) {
	profile, err := s.GetProfileByAccount(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if isBareProfile(profile) {
		return s.GetOrCreateAccountProfile(ctx, accountID)
	}
	return profile, nil
}

// GetOrCreateAccountProfile loads the account's profile, creating an empty
// row when missing. The returned profile always carries the board list
// (mirrors AccountService.GetOrCreateAccountProfileAsync + board hydration).
// A concurrent-create race is resolved by re-reading after the insert.
// Bare rows (no name/bio/picture — the common case for migrated accounts that
// never edited their profile) are backfilled with the account's name so
// clients never see a data-less profile for an existing account.
func (s *Store) GetOrCreateAccountProfile(ctx context.Context, accountID uuid.UUID) (*model.Profile, error) {
	_, err := s.GetProfileByAccount(ctx, accountID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// Profile row missing entirely (or tombstoned): create (or revive) it.
	if errors.Is(err, ErrNotFound) {
		now := time.Now().UTC()
		// A fresh entity per attempt: Create mutates the model it is handed.
		newProfile := func() *ProfileEntity {
			return &ProfileEntity{
				ID:            uuid.New(),
				EntityBase:    EntityBase{CreatedAt: now, UpdatedAt: now},
				AccountID:     accountID,
				Experience:    0,
				SocialCredits: 100,
			}
		}
		if err := s.DB.WithContext(ctx).
			Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_id"}}, DoNothing: true}).
			Create(newProfile()).Error; err != nil {
			return nil, err
		}

		_, err = s.GetProfileByAccount(ctx, accountID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if errors.Is(err, ErrNotFound) {
			// A soft-deleted profile row still occupies the (unfiltered) unique
			// index, so the insert above was a no-op. Hard-delete the tombstone
			// and retry so the account gets a live profile (mirrors the C#
			// filtered unique index).
			if err := s.DB.WithContext(ctx).Unscoped().
				Where("account_id = ?", accountID).Delete(&ProfileEntity{}).Error; err != nil {
				return nil, err
			}
			if err := s.DB.WithContext(ctx).Create(newProfile()).Error; err != nil {
				return nil, err
			}
		}
	}

	// Backfill bare rows with the account's name (the only derivable field).
	// Condition mirrors the client-side bare check; a no-op once healed.
	if err := s.HealBareProfile(ctx, accountID); err != nil {
		return nil, err
	}
	return s.GetProfileByAccount(ctx, accountID)
}

// HydrateAccountProfile attaches the account profile used by authenticated
// session contexts. It creates a profile for legacy accounts that predate the
// account_profiles migration, matching the account gRPC read behavior.
func (s *Store) HydrateAccountProfile(ctx context.Context, account *model.Account) error {
	if account == nil {
		return errors.New("account is required")
	}
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		return err
	}
	profile, err := s.GetOrCreateAccountProfile(ctx, accountID)
	if err != nil {
		return err
	}
	account.Profile = profile
	return nil
}

// HealBareProfile copies accounts.name into first_name for profile rows that
// carry no profile data at all (empty first/last name, no bio, no picture).
// Such rows are what migrated accounts that never edited their profile end up
// with — and what the old Passport created on demand — so reads otherwise
// emit data-less profiles for perfectly real accounts.
func (s *Store) HealBareProfile(ctx context.Context, accountID uuid.UUID) error {
	return s.DB.WithContext(ctx).Model(&ProfileEntity{}).
		Where(`account_id = ?
			AND (first_name IS NULL OR btrim(first_name) = '')
			AND (last_name IS NULL OR btrim(last_name) = '')
			AND (bio IS NULL OR btrim(bio) = '')
			AND picture IS NULL
			AND EXISTS (SELECT 1 FROM accounts a WHERE a.id = account_profiles.account_id AND a.deleted_at IS NULL)`, accountID).
		Updates(map[string]any{
			"first_name": gorm.Expr("(SELECT a.name FROM accounts a WHERE a.id = account_profiles.account_id AND a.deleted_at IS NULL)"),
			"updated_at": gorm.Expr("now()"),
		}).Error
}

// GetProfilesByAccountIDs loads the 1:1 profile rows for the given accounts
// (missing accounts are absent from the map). Bare rows are healed
// (backfilled with the account name) so no profile shell is surfaced.
func (s *Store) GetProfilesByAccountIDs(ctx context.Context, ids []uuid.UUID) (map[string]*model.Profile, error) {
	profiles := map[string]*model.Profile{}
	if len(ids) == 0 {
		return profiles, nil
	}
	var entities []ProfileEntity
	if err := s.DB.WithContext(ctx).Where("account_id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		profile := profileFromEntity(&entities[i])
		if isBareProfile(profile) {
			healed, err := s.GetOrCreateAccountProfile(ctx, entities[i].AccountID)
			if err != nil {
				return nil, err
			}
			profile = healed
		}
		profiles[profile.AccountId] = profile
	}
	return profiles, nil
}

// ProfileFieldPatch carries the fields Passport features publish on
// accounts.profile_updated (the denormalized account_profiles fields that
// moved to Stargate with the table).
type ProfileFieldPatch struct {
	LastSeenAt      *time.Time
	Experience      *int
	ExperienceDelta *int
	SocialCredits   *float64
	ActiveBadge     any
	HasActiveBadge  bool
	Verification    *model.SnVerificationMark
	HasVerification bool
}

// ApplyProfileFieldPatch applies a Passport-published profile field patch to
// the account's profile row (create-on-missing), mirroring the feature
// writers that used to hit Passport's own account_profiles table.
//
// The write is column-targeted: only fields present in the patch are SET, so
// a routine XP/last-seen patch can never clobber active_badge/verification
// that another writer (admin verification, badge sync) committed
// concurrently. The previous load-modify-SaveProfile form wrote every profile
// column from its read snapshot, so two overlapping patches — across the
// fleet's replicas, or a patch racing a user/admin profile save — silently
// NULLed the badge/verification the later snapshot had not seen.
// A literal JSON null in ActiveBadge/Verification still clears the column
// (the Passport "null to clear" contract).
func (s *Store) ApplyProfileFieldPatch(ctx context.Context, accountID uuid.UUID, patch *ProfileFieldPatch) error {
	if _, err := s.GetOrCreateAccountProfile(ctx, accountID); err != nil {
		return err
	}
	updates := map[string]any{"updated_at": gorm.Expr("now()")}
	if patch.LastSeenAt != nil {
		updates["last_seen_at"] = *patch.LastSeenAt
	}
	// Preserve the legacy combine semantics when an event carries both an
	// absolute experience and a delta: absolute first, then the delta on top.
	switch {
	case patch.Experience != nil && patch.ExperienceDelta != nil && *patch.ExperienceDelta != 0:
		updates["experience"] = *patch.Experience + *patch.ExperienceDelta
	case patch.Experience != nil:
		updates["experience"] = *patch.Experience
	case patch.ExperienceDelta != nil && *patch.ExperienceDelta != 0:
		updates["experience"] = gorm.Expr("experience + ?", *patch.ExperienceDelta)
	}
	if patch.SocialCredits != nil {
		updates["social_credits"] = *patch.SocialCredits
	}
	if patch.HasActiveBadge {
		v, err := marshalJSONOrNull(patch.ActiveBadge)
		if err != nil {
			return err
		}
		updates["active_badge"] = v
	}
	if patch.HasVerification {
		v, err := marshalJSONOrNull(patch.Verification)
		if err != nil {
			return err
		}
		updates["verification"] = v
	}
	return s.DB.WithContext(ctx).Model(&ProfileEntity{}).
		Where("account_id = ?", accountID).Updates(updates).Error
}

// SaveProfile writes the mutable profile columns (mirrors EF db.Update on
// SnAccountProfile; computed fields like level are derived and not stored).
//
// Pointer columns (names, bio, gender, username_color, birthday, last_seen,
// verification, active_badge, picture, background) are written only when set.
// Callers load the row and merge the fields they change, so an unconditional
// full-row write would clobber a column another writer committed between the
// read and the save — the race that NULLed admin-set verification and active
// badges. Scalars (links, experience, social_credits) always round-trip.
// Explicit clears go through ApplyProfileFieldPatch ("null to clear").
func (s *Store) SaveProfile(ctx context.Context, p *model.Profile) error {
	links, err := json.Marshal(p.Links)
	if err != nil {
		return err
	}
	updates := map[string]any{
		"updated_at": gorm.Expr("now()"),
		"links":      datatypes.JSON(links),
	}
	if p.FirstName != nil {
		updates["first_name"] = *p.FirstName
	}
	if p.MiddleName != nil {
		updates["middle_name"] = *p.MiddleName
	}
	if p.LastName != nil {
		updates["last_name"] = *p.LastName
	}
	if p.Bio != nil {
		updates["bio"] = *p.Bio
	}
	if p.Gender != nil {
		updates["gender"] = *p.Gender
	}
	if p.Pronouns != nil {
		updates["pronouns"] = *p.Pronouns
	}
	if p.TimeZone != nil {
		updates["time_zone"] = *p.TimeZone
	}
	if p.Location != nil {
		updates["location"] = *p.Location
	}
	if p.UsernameColor != nil {
		v, err := marshalJSONOrNull(p.UsernameColor)
		if err != nil {
			return err
		}
		updates["username_color"] = v
	}
	if p.Birthday != nil {
		updates["birthday"] = p.Birthday
	}
	if p.LastSeenAt != nil {
		updates["last_seen_at"] = p.LastSeenAt
	}
	if p.Verification != nil {
		v, err := marshalJSONOrNull(p.Verification)
		if err != nil {
			return err
		}
		updates["verification"] = v
	}
	if p.ActiveBadge != nil {
		v, err := marshalJSONOrNull(p.ActiveBadge)
		if err != nil {
			return err
		}
		updates["active_badge"] = v
	}
	updates["experience"] = p.Experience
	updates["social_credits"] = p.SocialCredits
	if p.Picture != nil {
		v, err := marshalJSONOrNull(p.Picture)
		if err != nil {
			return err
		}
		updates["picture"] = v
	}
	if p.Background != nil {
		v, err := marshalJSONOrNull(p.Background)
		if err != nil {
			return err
		}
		updates["background"] = v
	}
	return s.DB.WithContext(ctx).Model(&ProfileEntity{}).
		Where("id = ?", p.Id).Updates(updates).Error
}

// UpdateAccountBasicInfo applies the PATCH /api/accounts/me BasicInfo patch
// (only non-nil fields are written) and returns the refreshed account.
func (s *Store) UpdateAccountBasicInfo(ctx context.Context, accountID uuid.UUID, nick, language, region *string) (*model.Account, error) {
	now := time.Now().UTC()
	updates := map[string]any{}
	if nick != nil {
		updates["nick"] = *nick
	}
	if language != nil {
		updates["language"] = *language
	}
	if region != nil {
		updates["region"] = *region
	}
	if len(updates) > 0 {
		updates["updated_at"] = now
		if err := s.DB.WithContext(ctx).Model(&AccountEntity{}).
			Where("id = ?", accountID).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	return s.GetAccountByID(ctx, accountID)
}

// decodeActiveBadge canonicalizes the stored active-badge jsonb into the
// snake_case SnAccountBadgeRef wire shape the Island SDK strict-casts
// (account.g.dart): id/type/meta/account_id/created_at/updated_at must all be
// present, so the raw column cannot leak. The column holds either legacy C#
// EF rows (PascalCase partial refs like {"Id","Type","Label"}) or NATS-synced
// refs (snake_case, from Passport's accounts.profile_updated); both normalize
// to the same shape the C# Passport served on /accounts/me. Missing ref
// timestamps default to the profile row's own created_at/updated_at (the C#
// used ModelBase defaults; the client only parses, never displays them).
func decodeActiveBadge(profile *model.Profile, raw []byte) error {
	profile.ActiveBadge = nil
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		return err
	}
	// Accept snake_case (NATS) and C# PascalCase (legacy EF) keys.
	refString := func(key, legacy string) *string {
		if v, ok := stored[key]; ok {
			if s, isString := v.(string); isString {
				return &s
			}
		}
		if v, ok := stored[legacy]; ok {
			if s, isString := v.(string); isString {
				return &s
			}
		}
		return nil
	}
	// refValue dereferences a stored string, emitting nil (JSON null) when
	// the key is absent so the wire keeps the C# "null" shape.
	refValue := func(key, legacy string) any {
		if s := refString(key, legacy); s != nil {
			return *s
		}
		return nil
	}
	refMeta := func() map[string]any {
		if v, ok := stored["meta"]; ok {
			if m, isMap := v.(map[string]any); isMap {
				return m
			}
		}
		if v, ok := stored["Meta"]; ok {
			if m, isMap := v.(map[string]any); isMap {
				return m
			}
		}
		return map[string]any{}
	}
	// refTime prefers the profile row's own timestamps (the C# served its
	// ModelBase defaults; the client only parses, never displays them),
	// falling back to the stored ref values, then an epoch default.
	refTime := func(t *model.Time, key, legacy string) any {
		if t != nil {
			return time.Time(*t).UTC().Format(time.RFC3339)
		}
		if v := refString(key, legacy); v != nil {
			return *v
		}
		return time.Time{}.UTC().Format(time.RFC3339)
	}
	ref := map[string]any{
		"id":           refValue("id", "Id"),
		"type":         refValue("type", "Type"),
		"label":        refValue("label", "Label"),
		"caption":      refValue("caption", "Caption"),
		"meta":         refMeta(),
		"activated_at": refValue("activated_at", "ActivatedAt"),
		"expired_at":   refValue("expired_at", "ExpiredAt"),
		"account_id":   profile.AccountId,
		"created_at":   refTime(profile.CreatedAt, "created_at", "CreatedAt"),
		"updated_at":   refTime(profile.UpdatedAt, "updated_at", "UpdatedAt"),
		"deleted_at":   nil,
	}
	if ref["account_id"] == "" {
		if v := refString("account_id", "AccountId"); v != nil {
			ref["account_id"] = *v
		}
	}
	value := any(ref)
	profile.ActiveBadge = &value
	return nil
}

// marshalJSONOrNull marshals a value to JSON, emitting SQL NULL for nil.
// The nil check must also catch typed nil pointers (e.g. *model.Time,
// *any, *SnVerificationMark): an interface holding one is not == nil, and
// json.Marshal would emit the JSON literal `null` instead of SQL NULL,
// silently converting every cleared column into jsonb 'null' on save.
func marshalJSONOrNull(v any) (any, error) {
	if isNilValue(v) {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(b), nil
}

// isNilValue reports whether v is nil, including typed nil pointers.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return rv.IsNil()
	}
	return false
}
