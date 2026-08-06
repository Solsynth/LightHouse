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

// Spell storage for the magic-spell + affiliation-spell surface migrated from
// Passport's MagicSpellService / AffiliationSpellService.

const magicSpellColumns = `id, spell, type, expires_at, affected_at, meta, account_id, created_at, updated_at, deleted_at`

func scanMagicSpell(row pgx.Row) (*model.MagicSpell, error) {
	spell := &model.MagicSpell{}
	var meta []byte
	var accountID *string
	err := row.Scan(&spell.Id, &spell.Spell, &spell.Type, &spell.ExpiresAt, &spell.AffectedAt, &meta,
		&accountID, &spell.CreatedAt, &spell.UpdatedAt, &spell.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if accountID != nil {
		spell.AccountId = *accountID
	}
	spell.Meta = map[string]any{}
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &spell.Meta)
	}
	return spell, nil
}

// CreateMagicSpell inserts a spell (ids and timestamps must be set).
func (s *Store) CreateMagicSpell(ctx context.Context, spell *model.MagicSpell) error {
	meta, _ := json.Marshal(spell.Meta)
	if spell.Meta == nil {
		meta = []byte("{}")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO magic_spells
		(id, spell, type, expires_at, affected_at, meta, account_id, created_at, updated_at, deleted_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		spell.Id, spell.Spell, int(spell.Type), spell.ExpiresAt, spell.AffectedAt, meta,
		nullableUUID(spell.AccountId), spell.CreatedAt, spell.UpdatedAt, spell.DeletedAt)
	return err
}

// FindLiveMagicSpell returns a non-expired, non-deleted spell of the given
// type for an account (the C# preventRepeat lookup).
func (s *Store) FindLiveMagicSpell(ctx context.Context, accountID string, typ model.MagicSpellType) (*model.MagicSpell, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+magicSpellColumns+` FROM magic_spells
		WHERE account_id = $1 AND type = $2 AND deleted_at IS NULL
		  AND (expires_at IS NULL OR expires_at > $3)
		ORDER BY created_at DESC LIMIT 1`,
		accountID, int(typ), time.Now().UTC())
	return scanMagicSpell(row)
}

// GetMagicSpellByWord loads a spell by its secret word (not deleted).
func (s *Store) GetMagicSpellByWord(ctx context.Context, word string) (*model.MagicSpell, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+magicSpellColumns+` FROM magic_spells
		WHERE spell = $1 AND deleted_at IS NULL`, word)
	return scanMagicSpell(row)
}

// ListMagicSpellsByAccount returns an account's spells, newest first (the
// C# admin list: no deleted_at filter, OrderByDescending(CreatedAt)).
func (s *Store) ListMagicSpellsByAccount(ctx context.Context, accountID string) ([]*model.MagicSpell, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+magicSpellColumns+` FROM magic_spells
		WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var spells = []*model.MagicSpell{}
	for rows.Next() {
		spell, err := scanMagicSpell(rows)
		if err != nil {
			return nil, err
		}
		spells = append(spells, spell)
	}
	return spells, rows.Err()
}

// GetMagicSpellByID loads a spell by id (the C# resend lookup has no
// deleted_at filter).
func (s *Store) GetMagicSpellByID(ctx context.Context, id uuid.UUID) (*model.MagicSpell, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+magicSpellColumns+` FROM magic_spells WHERE id = $1`, id)
	return scanMagicSpell(row)
}

// GetContactVerificationSpell returns an account's contact-verification
// spell (the C# resend lookup: no deleted_at/expiry filters).
func (s *Store) GetContactVerificationSpell(ctx context.Context, accountID string) (*model.MagicSpell, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+magicSpellColumns+` FROM magic_spells
		WHERE type = $1 AND account_id = $2 ORDER BY created_at DESC LIMIT 1`,
		int(model.MagicSpellTypeContactVerification), accountID)
	return scanMagicSpell(row)
}

// DeleteMagicSpell hard-deletes a spell (C# db.Remove semantics).
func (s *Store) DeleteMagicSpell(ctx context.Context, id string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM magic_spells WHERE id = $1`, id)
	return err
}

// GetEmailContactForNotify returns an account's email contact for spell
// delivery: primary first, optionally verified only (NotifyMagicSpell's
// remote contact lookup with verifiedOnly: !bypassVerify).
func (s *Store) GetEmailContactForNotify(ctx context.Context, accountID string, verifiedOnly bool) (*model.Contact, error) {
	q := `SELECT id, type, verified_at, is_primary, is_public, content, account_id,
		created_at, updated_at, deleted_at
		FROM account_contacts
		WHERE account_id = $1 AND type = $2 AND deleted_at IS NULL`
	if verifiedOnly {
		q += ` AND verified_at IS NOT NULL`
	}
	q += ` ORDER BY is_primary DESC, created_at LIMIT 1`
	row := s.DB.QueryRow(ctx, q, accountID, int(model.ContactTypeEmail))
	contact := &model.Contact{}
	err := row.Scan(&contact.Id, &contact.Type, &contact.VerifiedAt, &contact.IsPrimary, &contact.IsPublic,
		&contact.Content, &contact.AccountId, &contact.CreatedAt, &contact.UpdatedAt, &contact.DeletedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return contact, nil
}

// MarkContactVerified sets verified_at on a contact, mirroring
// AccountService.MarkContactMethodVerified (only moves the stamp forward).
func (s *Store) MarkContactVerified(ctx context.Context, accountID, contactID string, verifiedAt time.Time) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE account_contacts
		SET verified_at = $1, updated_at = $1
		WHERE account_id = $2 AND id = $3 AND (verified_at IS NULL OR verified_at < $1)`,
		verifiedAt, accountID, contactID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ActivateAccountAndGrantVerified sets activated_at and upserts the
// `verified` group membership, mirroring the spell-path combination of
// TestService.TryActivateAfterContactVerification (skip when already
// activated) and Padlock's ActivateAccountAndGrantDefaultPermissions (set
// stamp + grant group + clear permission cache). It reports whether the
// account was newly activated (false when already activated or missing).
func (s *Store) ActivateAccountAndGrantVerified(ctx context.Context, accountID uuid.UUID, activatedAt time.Time) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE accounts
		SET activated_at = $1, updated_at = now()
		WHERE id = $2 AND activated_at IS NULL`,
		activatedAt, accountID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	var groupID uuid.UUID
	if err := s.DB.QueryRow(ctx, `SELECT id FROM permission_groups
		WHERE "key" = $1 AND deleted_at IS NULL`, "verified").Scan(&groupID); err != nil {
		return false, err
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO permission_group_members
		(group_id, actor, affected_at, expired_at, created_at, updated_at)
		VALUES ($1, $2, NULL, NULL, now(), now())
		ON CONFLICT (group_id, actor) DO UPDATE SET affected_at = NULL, expired_at = NULL, updated_at = now()`,
		groupID, accountID.String()); err != nil {
		return false, err
	}
	return true, nil
}

// ResetPasswordFactor replaces an account's password auth factor secret,
// mirroring the C# ResetPasswordFactorAsync on the account service.
func (s *Store) ResetPasswordFactor(ctx context.Context, accountID, passwordHash string) error {
	_, err := s.DB.Exec(ctx, `UPDATE account_auth_factors
		SET secret = $2, updated_at = now()
		WHERE account_id = $1 AND type = $3 AND deleted_at IS NULL`,
		accountID, passwordHash, int(model.AuthFactorTypePassword))
	return err
}

// ─────────────────────────── Affiliation spells ───────────────────────────

const affiliationSpellColumns = `id, spell, type, expires_at, affected_at, meta, account_id, created_at, updated_at, deleted_at`

func scanAffiliationSpell(row pgx.Row) (*model.AffiliationSpell, error) {
	spell := &model.AffiliationSpell{}
	var meta []byte
	var accountID *string
	err := row.Scan(&spell.Id, &spell.Spell, &spell.Type, &spell.ExpiresAt, &spell.AffectedAt, &meta,
		&accountID, &spell.CreatedAt, &spell.UpdatedAt, &spell.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if accountID != nil {
		spell.AccountId = *accountID
	}
	spell.Meta = map[string]any{}
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &spell.Meta)
	}
	return spell, nil
}

// GetAffiliationSpellByWord loads an affiliation spell by its word and type
// (the C# ConsumeRegistrationInvite lookup has no deleted_at filter).
func (s *Store) GetAffiliationSpellByWord(ctx context.Context, word string, typ model.AffiliationSpellType) (*model.AffiliationSpell, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+affiliationSpellColumns+` FROM affiliation_spells
		WHERE spell = $1 AND type = $2`, word, int(typ))
	return scanAffiliationSpell(row)
}

// CountAffiliationResults counts the recorded uses of an affiliation spell.
func (s *Store) CountAffiliationResults(ctx context.Context, spellID string) (int, error) {
	var count int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM affiliation_results WHERE spell_id = $1 AND deleted_at IS NULL`, spellID).Scan(&count)
	return count, err
}

// CreateAffiliationResult records one use of an affiliation spell.
func (s *Store) CreateAffiliationResult(ctx context.Context, spellID, resourceIdentifier string) error {
	now := time.Now().UTC()
	_, err := s.DB.Exec(ctx, `INSERT INTO affiliation_results
		(id, resource_identifier, spell_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$4)`,
		uuid.NewString(), resourceIdentifier, spellID, now)
	return err
}

// SetAffiliationSpellAffected stamps affected_at (max_usages == 1 case).
func (s *Store) SetAffiliationSpellAffected(ctx context.Context, spellID string, at time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE affiliation_spells SET affected_at = $2, updated_at = $2 WHERE id = $1`, spellID, at)
	return err
}

func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
