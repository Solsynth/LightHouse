// Package store centralizes the SQL queries shared across the service
// packages. Table/column names follow the EF snake_case schema produced by
// internal/migrate/0001_initial.sql.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TouchLastActive applies a last-active signal — from Stargate's own traffic
// (the auth middleware toucher) or the fleet's accounts.last_active events —
// mirroring Padlock's LastActiveFlushHandler: profile last_seen_at (the
// presented value), the session's last_granted_at and the 7-day keep-alive
// for expiring sessions.
func (s *Store) TouchLastActive(ctx context.Context, accountID, sessionID string, seenAt time.Time) error {
	if accountID != "" {
		if _, err := s.DB.Exec(ctx,
			`UPDATE account_profiles SET last_seen_at = $1, updated_at = $1
			 WHERE account_id = $2 AND deleted_at IS NULL`, seenAt, accountID); err != nil {
			return err
		}
	}
	if sessionID != "" {
		if _, err := s.DB.Exec(ctx,
			`UPDATE auth_sessions SET last_granted_at = $1
			 WHERE id = $2 AND deleted_at IS NULL`, seenAt, sessionID); err != nil {
			return err
		}
		if _, err := s.DB.Exec(ctx,
			`UPDATE auth_sessions SET expired_at = $1::timestamptz + INTERVAL '7 days'
			 WHERE id = $2 AND expired_at IS NOT NULL AND deleted_at IS NULL`, seenAt, sessionID); err != nil {
			return err
		}
	}
	return nil
}

var ErrNotFound = errors.New("not found")

// Store wraps the database pool with typed queries.
type Store struct {
	DB *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store { return &Store{DB: db} }

const accountColumns = `id, name, nick, language, region, activated_at, is_superuser, automated_id, created_at, updated_at, deleted_at`

const profileColumns = `p.id, p.first_name, p.middle_name, p.last_name, p.bio, p.gender, p.pronouns, p.time_zone, p.location, p.links, p.username_color, p.birthday, p.last_seen_at, p.verification, p.active_badge, p.experience, p.social_credits, p.picture, p.background, p.account_id, p.created_at, p.updated_at, p.deleted_at`

// accountColsPrefixed returns the account column list with the given alias.
func accountColsPrefixed(alias string) string {
	return alias + `.id, ` + alias + `.name, ` + alias + `.nick, ` + alias + `.language, ` + alias + `.region, ` +
		alias + `.activated_at, ` + alias + `.is_superuser, ` + alias + `.automated_id, ` + alias + `.created_at, ` +
		alias + `.updated_at, ` + alias + `.deleted_at`
}

// profileColsPrefixed returns the profile column list with the given alias.
func profileColsPrefixed(alias string) string {
	return alias + `.id, ` + alias + `.first_name, ` + alias + `.middle_name, ` + alias + `.last_name, ` +
		alias + `.bio, ` + alias + `.gender, ` + alias + `.pronouns, ` + alias + `.time_zone, ` + alias + `.location, ` +
		alias + `.links, ` + alias + `.username_color, ` + alias + `.birthday, ` + alias + `.last_seen_at, ` +
		alias + `.verification, ` + alias + `.active_badge, ` + alias + `.experience, ` + alias + `.social_credits, ` +
		alias + `.picture, ` + alias + `.background, ` + alias + `.account_id, ` + alias + `.created_at, ` +
		alias + `.updated_at, ` + alias + `.deleted_at`
}

// GetAccountByID loads an account (without profile/contacts).
func (s *Store) GetAccountByID(ctx context.Context, id uuid.UUID) (*model.Account, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = $1 AND deleted_at IS NULL`, id)
	return scanAccount(row)
}

// GetAccountByName loads an account by name.
func (s *Store) GetAccountByName(ctx context.Context, name string) (*model.Account, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE name = $1 AND deleted_at IS NULL`, name)
	return scanAccount(row)
}

// GetAccountWithProfile loads an account with its 1:1 profile (LEFT JOIN).
func (s *Store) GetAccountWithProfile(ctx context.Context, id uuid.UUID) (*model.Account, error) {
	q := `SELECT ` + accountColsPrefixed("a") + `, ` + profileColsPrefixed("p") + ` FROM accounts a
		LEFT JOIN account_profiles p ON p.account_id = a.id AND p.deleted_at IS NULL
		WHERE a.id = $1 AND a.deleted_at IS NULL`
	row := s.DB.QueryRow(ctx, q, id)
	return scanAccountWithProfile(row)
}

// GetAccountWithProfileByName loads an account with profile by name.
func (s *Store) GetAccountWithProfileByName(ctx context.Context, name string) (*model.Account, error) {
	q := `SELECT ` + accountColsPrefixed("a") + `, ` + profileColsPrefixed("p") + ` FROM accounts a
		LEFT JOIN account_profiles p ON p.account_id = a.id AND p.deleted_at IS NULL
		WHERE a.name = $1 AND a.deleted_at IS NULL`
	row := s.DB.QueryRow(ctx, q, name)
	return scanAccountWithProfile(row)
}

// GetSessionWithAccount loads a session with its account (and client id),
// mirroring EF's Include(e => e.Client).Include(e => e.Account).
func (s *Store) GetSessionWithAccount(ctx context.Context, id uuid.UUID) (*model.AuthSession, error) {
	q := `SELECT s.id, s.type, s.last_granted_at, s.expired_at, s.audiences, s.scopes, s.ip_address,
		s.user_agent, s.location, s.account_id, s.client_id, s.parent_session_id, s.challenge_id, s.app_id, s.epoch,
		s.created_at, s.updated_at, s.deleted_at,
		` + accountColsPrefixed("a") + `
		FROM auth_sessions s
		JOIN accounts a ON a.id = s.account_id
		WHERE s.id = $1`
	row := s.DB.QueryRow(ctx, q, id)
	session := &model.AuthSession{}
	var (
		audiences, scopes                             []string
		location                                      []byte
		clientID, parentSessionID, challengeID, appID *uuid.UUID
		epoch                                         int
	)
	account := &model.Account{}
	var automatedID *uuid.UUID
	err := row.Scan(
		&session.Id, &session.Type, &session.LastGrantedAt, &session.ExpiredAt, &audiences, &scopes,
		&session.IpAddress, &session.UserAgent, &location, &session.AccountId,
		&clientID, &parentSessionID, &challengeID, &appID, &epoch,
		&session.CreatedAt, &session.UpdatedAt, &session.DeletedAt,
		&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region, &account.ActivatedAt,
		&account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	session.Audiences = audiences
	session.Scopes = scopes
	if len(location) > 0 && string(location) != "null" {
		var gp model.GeoPoint
		if err := json.Unmarshal(location, &gp); err == nil {
			session.Location = &gp
		}
	}
	session.ClientId = uuidPtrStr(clientID)
	session.ParentSessionId = uuidPtrStr(parentSessionID)
	session.ChallengeId = uuidPtrStr(challengeID)
	session.AppId = uuidPtrStr(appID)
	session.Epoch = epoch
	if account.Id != "" {
		account.AutomatedId = uuidPtrStr(automatedID)
		session.Account = account
	}
	return session, nil
}

// GetAuthFactors lists the enabled factors for an account.
func (s *Store) GetAuthFactors(ctx context.Context, accountID uuid.UUID) ([]model.AuthFactor, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, type, secret, config, trustworthy, enabled_at, expired_at, account_id, created_at, updated_at, deleted_at
		FROM account_auth_factors WHERE account_id = $1 AND deleted_at IS NULL ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var factors []model.AuthFactor
	for rows.Next() {
		var f model.AuthFactor
		var config []byte
		var secret *string
		if err := rows.Scan(&f.Id, &f.Type, &secret, &config, &f.Trustworthy, &f.EnabledAt, &f.ExpiredAt,
			&f.AccountId, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt); err != nil {
			return nil, err
		}
		if secret != nil {
			f.Secret = *secret
		}
		if len(config) > 0 {
			_ = json.Unmarshal(config, &f.Config)
		}
		factors = append(factors, f)
	}
	return factors, rows.Err()
}

func scanAccount(row pgx.Row) (*model.Account, error) {
	account := &model.Account{}
	var automatedID *uuid.UUID
	err := row.Scan(&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region,
		&account.ActivatedAt, &account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	account.AutomatedId = uuidPtrStr(automatedID)
	return account, nil
}

func scanAccountWithProfile(row pgx.Row) (*model.Account, error) {
	account := &model.Account{}
	profile := &model.Profile{}
	var automatedID *uuid.UUID
	var (
		links, usernameColor, verification, activeBadge, picture, background       []byte
		profileID, profileAccountID                                                *string
		firstName, middleName, lastName, bio, gender, pronouns, timeZone, location *string
		birthday, lastSeenAt                                                       *model.Time
		experience                                                                 *int
		socialCredits                                                              *float64
		profileCreated, profileUpdated, profileDeleted                             *model.Time
	)
	err := row.Scan(
		&account.Id, &account.Name, &account.Nick, &account.Language, &account.Region,
		&account.ActivatedAt, &account.IsSuperuser, &automatedID, &account.CreatedAt, &account.UpdatedAt, &account.DeletedAt,
		&profileID, &firstName, &middleName, &lastName, &bio, &gender, &pronouns, &timeZone, &location,
		&links, &usernameColor, &birthday, &lastSeenAt, &verification, &activeBadge, &experience, &socialCredits,
		&picture, &background, &profileAccountID, &profileCreated, &profileUpdated, &profileDeleted,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	account.AutomatedId = uuidPtrStr(automatedID)
	if profileID != nil {
		profile.Id = *profileID
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
		if experience != nil {
			profile.Experience = *experience
		}
		if socialCredits != nil {
			profile.SocialCredits = *socialCredits
		}
		profile.ComputeLeveling()
		if profileAccountID != nil {
			profile.AccountId = *profileAccountID
		}
		profile.CreatedAt = profileCreated
		profile.UpdatedAt = profileUpdated
		profile.DeletedAt = profileDeleted
		_ = json.Unmarshal(links, &profile.Links)
		_ = json.Unmarshal(usernameColor, &profile.UsernameColor)
		_ = json.Unmarshal(verification, &profile.Verification)
		_ = json.Unmarshal(picture, &profile.Picture)
		_ = json.Unmarshal(background, &profile.Background)
		_ = decodeActiveBadge(profile, activeBadge)
		account.Profile = profile
	}
	return account, nil
}

func uuidPtrStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	v := id.String()
	return &v
}

// ParseUUID parses a string into a UUID.
func ParseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid uuid %q: %w", s, err)
	}
	return id, nil
}
