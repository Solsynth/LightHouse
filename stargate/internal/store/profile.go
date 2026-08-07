package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Profile helpers for the Passport-moved profile surface. All queries target
// the account_profiles table (snake_case, timestamptz instants, jsonb blobs).

// GetProfileByAccount loads an account's 1:1 profile row.
func (s *Store) GetProfileByAccount(ctx context.Context, accountID uuid.UUID) (*model.Profile, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+profileColumns+` FROM account_profiles p
		WHERE p.account_id = $1 AND p.deleted_at IS NULL`, accountID)
	return scanProfile(row)
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
		if _, err := s.DB.Exec(ctx, `INSERT INTO account_profiles
			(id, account_id, created_at, updated_at, experience, social_credits)
			VALUES ($1, $2, $3, $3, 0, 100)
			ON CONFLICT (account_id) DO NOTHING`, uuid.NewString(), accountID, now); err != nil {
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
			if _, err := s.DB.Exec(ctx, `DELETE FROM account_profiles WHERE account_id = $1`, accountID); err != nil {
				return nil, err
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO account_profiles
				(id, account_id, created_at, updated_at, experience, social_credits)
				VALUES ($1, $2, $3, $3, 0, 100)`, uuid.NewString(), accountID, now); err != nil {
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

// HealBareProfile copies accounts.name into first_name for profile rows that
// carry no profile data at all (empty first/last name, no bio, no picture).
// Such rows are what migrated accounts that never edited their profile end up
// with — and what the old Passport created on demand — so reads otherwise
// emit data-less profiles for perfectly real accounts.
func (s *Store) HealBareProfile(ctx context.Context, accountID uuid.UUID) error {
	_, err := s.DB.Exec(ctx, `UPDATE account_profiles p SET first_name = a.name, updated_at = now()
		FROM accounts a
		WHERE p.account_id = a.id AND p.account_id = $1
		  AND a.deleted_at IS NULL AND p.deleted_at IS NULL
		  AND (p.first_name IS NULL OR btrim(p.first_name) = '')
		  AND (p.last_name IS NULL OR btrim(p.last_name) = '')
		  AND (p.bio IS NULL OR btrim(p.bio) = '')
		  AND p.picture IS NULL`, accountID)
	return err
}


// GetProfilesByAccountIDs loads the 1:1 profile rows for the given accounts
// (missing accounts are absent from the map).
func (s *Store) GetProfilesByAccountIDs(ctx context.Context, ids []uuid.UUID) (map[string]*model.Profile, error) {
	profiles := map[string]*model.Profile{}
	if len(ids) == 0 {
		return profiles, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT `+profileColumns+` FROM account_profiles p
		WHERE p.account_id = ANY($1) AND p.deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles[profile.AccountId] = profile
	}
	return profiles, rows.Err()
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
func (s *Store) ApplyProfileFieldPatch(ctx context.Context, accountID uuid.UUID, patch *ProfileFieldPatch) error {
	profile, err := s.GetOrCreateAccountProfile(ctx, accountID)
	if err != nil {
		return err
	}
	if patch.LastSeenAt != nil {
		profile.LastSeenAt = model.NewTime(*patch.LastSeenAt)
	}
	if patch.Experience != nil {
		profile.Experience = *patch.Experience
	}
	if patch.ExperienceDelta != nil && *patch.ExperienceDelta != 0 {
		profile.Experience += *patch.ExperienceDelta
	}
	if patch.SocialCredits != nil {
		profile.SocialCredits = *patch.SocialCredits
	}
	if patch.HasActiveBadge {
		profile.ActiveBadge = nil
		if patch.ActiveBadge != nil {
			value := patch.ActiveBadge
			profile.ActiveBadge = &value
		}
	}
	if patch.HasVerification {
		profile.Verification = patch.Verification
	}
	return s.SaveProfile(ctx, profile)
}

// SaveProfile writes the mutable profile columns (mirrors EF db.Update on
// SnAccountProfile; computed fields like level are derived and not stored).
func (s *Store) SaveProfile(ctx context.Context, p *model.Profile) error {
	links, err := json.Marshal(p.Links)
	if err != nil {
		return err
	}
	usernameColor, err := marshalJSONOrNull(p.UsernameColor)
	if err != nil {
		return err
	}
	verification, err := marshalJSONOrNull(p.Verification)
	if err != nil {
		return err
	}
	activeBadge, err := marshalJSONOrNull(p.ActiveBadge)
	if err != nil {
		return err
	}
	picture, err := marshalJSONOrNull(p.Picture)
	if err != nil {
		return err
	}
	background, err := marshalJSONOrNull(p.Background)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE account_profiles SET
		first_name = $1, middle_name = $2, last_name = $3, bio = $4, gender = $5,
		pronouns = $6, time_zone = $7, location = $8, links = $9, username_color = $10,
		birthday = $11, last_seen_at = $12, verification = $13, active_badge = $14,
		experience = $15, social_credits = $16, picture = $17, background = $18,
		updated_at = $19
		WHERE id = $20 AND deleted_at IS NULL`,
		p.FirstName, p.MiddleName, p.LastName, p.Bio, p.Gender,
		p.Pronouns, p.TimeZone, p.Location, links, usernameColor,
		p.Birthday, p.LastSeenAt, verification, activeBadge,
		p.Experience, p.SocialCredits, picture, background,
		time.Now().UTC(), p.Id)
	return err
}

// UpdateAccountBasicInfo applies the PATCH /api/accounts/me BasicInfo patch
// (only non-nil fields are written) and returns the refreshed account.
func (s *Store) UpdateAccountBasicInfo(ctx context.Context, accountID uuid.UUID, nick, language, region *string) (*model.Account, error) {
	now := time.Now().UTC()
	if nick != nil {
		if _, err := s.DB.Exec(ctx, `UPDATE accounts SET nick = $1, updated_at = $2 WHERE id = $3 AND deleted_at IS NULL`, *nick, now, accountID); err != nil {
			return nil, err
		}
	}
	if language != nil {
		if _, err := s.DB.Exec(ctx, `UPDATE accounts SET language = $1, updated_at = $2 WHERE id = $3 AND deleted_at IS NULL`, *language, now, accountID); err != nil {
			return nil, err
		}
	}
	if region != nil {
		if _, err := s.DB.Exec(ctx, `UPDATE accounts SET region = $1, updated_at = $2 WHERE id = $3 AND deleted_at IS NULL`, *region, now, accountID); err != nil {
			return nil, err
		}
	}
	return s.GetAccountByID(ctx, accountID)
}

func scanProfile(row pgx.Row) (*model.Profile, error) {
	profile := &model.Profile{}
	var (
		links, usernameColor, verification, activeBadge, picture, background []byte
		firstName, middleName, lastName, bio, gender, pronouns, timeZone     *string
		location                                                             *string
		birthday, lastSeenAt                                                 *model.Time
		experience                                                           int
		socialCredits                                                        float64
	)
	err := row.Scan(
		&profile.Id, &firstName, &middleName, &lastName, &bio, &gender, &pronouns, &timeZone, &location,
		&links, &usernameColor, &birthday, &lastSeenAt, &verification, &activeBadge, &experience, &socialCredits,
		&picture, &background, &profile.AccountId, &profile.CreatedAt, &profile.UpdatedAt, &profile.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	profile.FirstName = firstName
	profile.MiddleName = middleName
	profile.LastName = lastName
	profile.Bio = bio
	profile.Gender = gender
	profile.Pronouns = pronouns
	profile.TimeZone = timeZone
	profile.Location = location
	profile.Birthday = birthday
	profile.LastSeenAt = lastSeenAt
	profile.Experience = experience
	profile.SocialCredits = socialCredits
	profile.ComputeLeveling()
	_ = json.Unmarshal(links, &profile.Links)
	_ = json.Unmarshal(usernameColor, &profile.UsernameColor)
	_ = json.Unmarshal(verification, &profile.Verification)
	_ = json.Unmarshal(picture, &profile.Picture)
	_ = json.Unmarshal(background, &profile.Background)
	_ = json.Unmarshal(activeBadge, &profile.ActiveBadge)
	return profile, nil
}

// marshalJSONOrNull marshals a value to JSON, emitting SQL NULL for nil.
func marshalJSONOrNull(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func mustParseUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}
