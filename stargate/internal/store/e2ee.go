// Package store e2ee/mls queries. These mirror the EF queries in
// DysonNetwork.Padlock E2EeService (snake_case table/column names from
// internal/migrate/0001_initial.sql). Soft-delete rows are excluded everywhere
// except where the C# uses IgnoreQueryFilters (AddMlsDeviceMembershipAsync).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LegacyDeviceID mirrors E2EeService.LegacyDeviceId ("legacy-account"), used
// for account-scoped (non-device) envelopes and revoke control envelopes.
const LegacyDeviceID = "legacy-account"

// --- Entities (row shapes, mirroring the C# SnE2ee*/SnMls* models) ---

// E2eeDevice mirrors SnE2eeDevice.
type E2eeDevice struct {
	Id           string
	AccountId    string
	DeviceId     string
	DeviceLabel  *string
	IsRevoked    bool
	LastBundleAt *time.Time
	RevokedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// E2eeKeyBundle mirrors SnE2eeKeyBundle.
type E2eeKeyBundle struct {
	Id                    string
	AccountId             string
	DeviceId              string
	Algorithm             string
	IdentityKey           []byte
	SignedPreKeyId        *int
	SignedPreKey          []byte
	SignedPreKeySignature []byte
	SignedPreKeyExpiresAt *time.Time
	Meta                  map[string]any
	CreatedAt             time.Time
	UpdatedAt             time.Time
	DeletedAt             *time.Time
}

// E2eeOneTimePreKey mirrors SnE2eeOneTimePreKey.
type E2eeOneTimePreKey struct {
	Id                 string
	KeyBundleId        string
	AccountId          string
	DeviceId           string
	KeyId              int
	PublicKey          []byte
	IsClaimed          bool
	ClaimedAt          *time.Time
	ClaimedByAccountId *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

// E2eeEnvelope mirrors SnE2eeEnvelope.
type E2eeEnvelope struct {
	Id                  string
	SenderId            string
	SenderDeviceId      *string
	RecipientId         string
	RecipientAccountId  string
	RecipientDeviceId   *string
	SessionId           *string
	Type                int
	GroupId             *string
	ClientMessageId     *string
	Sequence            int64
	Ciphertext          []byte
	Header              []byte
	Signature           []byte
	DeliveryStatus      int
	DeliveredAt         *time.Time
	AckedAt             *time.Time
	ExpiresAt           *time.Time
	LegacyAccountScoped bool
	Meta                map[string]any
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           *time.Time
}

// MlsKeyPackage mirrors SnMlsKeyPackage.
type MlsKeyPackage struct {
	Id                  string
	AccountId           string
	DeviceId            string
	DeviceLabel         *string
	KeyPackage          []byte
	Ciphersuite         string
	IsConsumed          bool
	ConsumedAt          *time.Time
	ConsumedByAccountId *string
	Meta                map[string]any
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           *time.Time
}

// MlsGroupState mirrors SnMlsGroupState.
type MlsGroupState struct {
	Id           string
	MlsGroupId   string
	Epoch        int64
	StateVersion int64
	LastCommitAt *time.Time
	GroupInfo    []byte
	RatchetTree  []byte
	Meta         map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// MlsDeviceMembership mirrors SnMlsDeviceMembership.
type MlsDeviceMembership struct {
	Id                     string
	MlsGroupId             string
	AccountId              string
	DeviceId               string
	JoinedEpoch            int64
	LastSeenEpoch          *int64
	LastReshareRequiredAt  *time.Time
	LastReshareCompletedAt *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
	DeletedAt              *time.Time
}

// DeviceKeyPackage pairs an active device with its oldest unconsumed key
// package (ListMlsDeviceKeyPackagesAsync).
type DeviceKeyPackage struct {
	Device  E2eeDevice
	Package MlsKeyPackage
}

// ConsumedDevice records a key package consumed by a consuming read, used for
// the KP-depleted notification.
type ConsumedDevice struct {
	DeviceID    string
	DeviceLabel *string
}

// UploadGroupInfoResult mirrors UploadGroupInfoResponse.
type UploadGroupInfoResult struct {
	Success bool
	GroupID string
	Epoch   int64
}

// RevokeDeviceResult mirrors the outcome of RevokeDeviceAsync.
type RevokeDeviceResult struct {
	Found            bool
	AlreadyRevoked   bool
	PurgedCount      int
	ControlEnvelopes []E2eeEnvelope
}

const (
	e2eeDeviceColumns = `id, account_id, device_id, device_label, is_revoked, last_bundle_at, revoked_at, created_at, updated_at, deleted_at`

	e2eeKeyBundleColumns = `id, account_id, device_id, algorithm, identity_key, signed_pre_key_id, signed_pre_key, signed_pre_key_signature, signed_pre_key_expires_at, meta, created_at, updated_at, deleted_at`

	e2eeOneTimePreKeyColumns = `id, key_bundle_id, account_id, device_id, key_id, public_key, is_claimed, claimed_at, claimed_by_account_id, created_at, updated_at, deleted_at`

	e2eeEnvelopeColumns = `id, sender_id, sender_device_id, recipient_id, recipient_account_id, recipient_device_id, session_id, type, group_id, client_message_id, sequence, ciphertext, header, signature, delivery_status, delivered_at, acked_at, expires_at, legacy_account_scoped, meta, created_at, updated_at, deleted_at`

	mlsKeyPackageColumns = `id, account_id, device_id, device_label, key_package, ciphersuite, is_consumed, consumed_at, consumed_by_account_id, meta, created_at, updated_at, deleted_at`

	mlsGroupStateColumns = `id, mls_group_id, epoch, state_version, last_commit_at, group_info, ratchet_tree, meta, created_at, updated_at, deleted_at`

	mlsDeviceMembershipColumns = `id, mls_group_id, account_id, device_id, joined_epoch, last_seen_epoch, last_reshare_required_at, last_reshare_completed_at, created_at, updated_at, deleted_at`
)

// queryer is satisfied by both *pgxpool.Pool and pgx.Tx so query helpers can
// run inside transactions.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// --- Devices ---

// GetE2eeDevice loads a device by (account_id, device_id), excluding
// soft-deleted rows (the EF global query filter). Returns (nil, nil) when
// absent.
func (s *Store) GetE2eeDevice(ctx context.Context, accountID, deviceID string) (*E2eeDevice, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+e2eeDeviceColumns+` FROM e2ee_devices
		WHERE account_id = $1 AND device_id = $2 AND deleted_at IS NULL`, accountID, deviceID)
	return scanE2eeDevice(row)
}

// ListActiveE2eeDevices lists the non-revoked devices of an account.
func (s *Store) ListActiveE2eeDevices(ctx context.Context, accountID string) ([]E2eeDevice, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+e2eeDeviceColumns+` FROM e2ee_devices
		WHERE account_id = $1 AND is_revoked = false AND deleted_at IS NULL ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []E2eeDevice
	for rows.Next() {
		d, err := scanE2eeDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, *d)
	}
	return devices, rows.Err()
}

// ListActiveE2eeDeviceIDs returns the device ids of an account's non-revoked
// devices.
func (s *Store) ListActiveE2eeDeviceIDs(ctx context.Context, accountID string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT device_id FROM e2ee_devices
		WHERE account_id = $1 AND is_revoked = false AND deleted_at IS NULL`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpsertE2eeDevice creates the device when absent, otherwise revives it and
// updates the label (mirrors the device blocks in UpsertDeviceBundleAsync /
// PublishMlsKeyPackageAsync).
func (s *Store) UpsertE2eeDevice(ctx context.Context, accountID, deviceID string, deviceLabel *string, now time.Time) (*E2eeDevice, error) {
	device, err := s.GetE2eeDevice(ctx, accountID, deviceID)
	if err != nil {
		return nil, err
	}
	if device == nil {
		device = &E2eeDevice{
			Id:          uuid.NewString(),
			AccountId:   accountID,
			DeviceId:    deviceID,
			DeviceLabel: deviceLabel,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		_, err := s.DB.Exec(ctx, `INSERT INTO e2ee_devices (id, account_id, device_id, device_label, is_revoked, last_bundle_at, revoked_at, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, $4, false, $5, NULL, $6, $6, NULL)`,
			device.Id, device.AccountId, device.DeviceId, device.DeviceLabel, now, now)
		if err != nil {
			return nil, err
		}
		device.IsRevoked = false
		device.LastBundleAt = &now
		return device, nil
	}
	label := device.DeviceLabel
	if deviceLabel != nil && *deviceLabel != "" {
		label = deviceLabel
	}
	_, err = s.DB.Exec(ctx, `UPDATE e2ee_devices
		SET device_label = $2, is_revoked = false, revoked_at = NULL, last_bundle_at = $3, updated_at = $3
		WHERE id = $1`, device.Id, label, now)
	if err != nil {
		return nil, err
	}
	device.DeviceLabel = label
	device.IsRevoked = false
	device.RevokedAt = nil
	device.LastBundleAt = &now
	device.UpdatedAt = now
	return device, nil
}

// --- E2EE key bundles + one-time pre keys ---
// Prekey claiming runs in a SERIALIZABLE transaction (mirrors
// GetPublicBundleAsync / GetPublicDeviceBundlesAsync). These helpers are not
// wired to any HTTP route (the C# exposes them only through the service
// interface), but the C# semantics are ported here for completeness.

// UpsertKeyBundleRequest mirrors UpsertE2EeKeyBundleRequest.
type UpsertKeyBundleRequest struct {
	Algorithm             string
	IdentityKey           []byte
	SignedPreKeyId        *int
	SignedPreKey          []byte
	SignedPreKeySignature []byte
	SignedPreKeyExpiresAt *time.Time
	OneTimePreKeys        []OneTimePreKeyInput
	Meta                  map[string]any
}

// OneTimePreKeyInput mirrors UpsertE2EeOneTimePreKey.
type OneTimePreKeyInput struct {
	KeyId     int
	PublicKey []byte
}

// UpsertE2eeKeyBundle upserts the device bundle plus the device row and
// appends new one-time pre keys (mirrors UpsertDeviceBundleAsync).
func (s *Store) UpsertE2eeKeyBundle(ctx context.Context, accountID, deviceID string, deviceLabel *string, req UpsertKeyBundleRequest, now time.Time) (*E2eeKeyBundle, error) {
	bundle, err := s.getE2eeKeyBundle(ctx, accountID, deviceID)
	if err != nil {
		return nil, err
	}
	if bundle == nil {
		bundle = &E2eeKeyBundle{Id: uuid.NewString(), AccountId: accountID, DeviceId: deviceID, CreatedAt: now, UpdatedAt: now}
		if _, err := s.DB.Exec(ctx, `INSERT INTO e2ee_key_bundles (id, account_id, device_id, algorithm, identity_key, signed_pre_key_id, signed_pre_key, signed_pre_key_signature, signed_pre_key_expires_at, meta, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, '', NULL, NULL, NULL, NULL, NULL, NULL, $4, $4, NULL)`,
			bundle.Id, bundle.AccountId, bundle.DeviceId, now); err != nil {
			return nil, err
		}
	}
	if _, err := s.UpsertE2eeDevice(ctx, accountID, deviceID, deviceLabel, now); err != nil {
		return nil, err
	}

	bundle.Algorithm = req.Algorithm
	bundle.IdentityKey = req.IdentityKey
	bundle.SignedPreKeyId = req.SignedPreKeyId
	bundle.SignedPreKey = req.SignedPreKey
	bundle.SignedPreKeySignature = req.SignedPreKeySignature
	bundle.SignedPreKeyExpiresAt = req.SignedPreKeyExpiresAt
	bundle.Meta = req.Meta
	bundle.UpdatedAt = now
	_, err = s.DB.Exec(ctx, `UPDATE e2ee_key_bundles
		SET algorithm = $2, identity_key = $3, signed_pre_key_id = $4, signed_pre_key = $5,
			signed_pre_key_signature = $6, signed_pre_key_expires_at = $7, meta = $8, updated_at = $9
		WHERE id = $1`,
		bundle.Id, bundle.Algorithm, bundle.IdentityKey, bundle.SignedPreKeyId, bundle.SignedPreKey,
		bundle.SignedPreKeySignature, bundle.SignedPreKeyExpiresAt, jsonBytes(bundle.Meta), now)
	if err != nil {
		return nil, err
	}

	if len(req.OneTimePreKeys) > 0 {
		existing, err := s.listOneTimePreKeyIDs(ctx, bundle.Id)
		if err != nil {
			return nil, err
		}
		for _, k := range req.OneTimePreKeys {
			if existing[k.KeyId] {
				continue
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO e2ee_one_time_pre_keys (id, key_bundle_id, account_id, device_id, key_id, public_key, is_claimed, claimed_at, claimed_by_account_id, created_at, updated_at, deleted_at)
				VALUES ($1, $2, $3, $4, $5, $6, false, NULL, NULL, $7, $7, NULL)`,
				uuid.NewString(), bundle.Id, accountID, deviceID, k.KeyId, k.PublicKey, now); err != nil {
				return nil, err
			}
		}
	}
	return bundle, nil
}

// E2eePublicBundle is a bundle plus the pre key claimed during a consuming
// read (nil when no pre key was available or consume was false).
type E2eePublicBundle struct {
	Bundle        E2eeKeyBundle
	ClaimedPreKey *E2eeOneTimePreKey
}

// GetPublicE2eeBundle returns the account's most recently updated bundle,
// optionally claiming its oldest unclaimed one-time pre key inside a
// SERIALIZABLE transaction (mirrors GetPublicBundleAsync).
func (s *Store) GetPublicE2eeBundle(ctx context.Context, accountID, requesterID string, consume bool) (*E2eePublicBundle, error) {
	bundle, err := s.getLatestE2eeKeyBundle(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if bundle == nil {
		return nil, nil
	}
	result := &E2eePublicBundle{Bundle: *bundle}
	if !consume {
		return result, nil
	}
	preKey, err := s.claimOneTimePreKey(ctx, bundle.Id, accountID, "", requesterID)
	if err != nil {
		return nil, err
	}
	result.ClaimedPreKey = preKey
	return result, nil
}

// E2eeDevicePublicBundle pairs an active device with its bundle and the pre
// key claimed during a consuming read.
type E2eeDevicePublicBundle struct {
	Device        E2eeDevice
	Bundle        E2eeKeyBundle
	ClaimedPreKey *E2eeOneTimePreKey
}

// GetPublicE2eeDeviceBundles returns one bundle per active device, claiming
// one one-time pre key per bundle inside a SERIALIZABLE transaction when
// consume is true (mirrors GetPublicDeviceBundlesAsync).
func (s *Store) GetPublicE2eeDeviceBundles(ctx context.Context, accountID, requesterID string, consume bool) ([]E2eeDevicePublicBundle, error) {
	devices, err := s.ListActiveE2eeDevices(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, nil
	}
	bundles, err := s.listE2eeKeyBundles(ctx, accountID)
	if err != nil {
		return nil, err
	}
	bundlesByDevice := make(map[string]E2eeKeyBundle, len(bundles))
	for _, b := range bundles {
		bundlesByDevice[b.DeviceId] = b
	}
	var responses []E2eeDevicePublicBundle
	for _, device := range devices {
		bundle, ok := bundlesByDevice[device.DeviceId]
		if !ok {
			continue
		}
		resp := E2eeDevicePublicBundle{Device: device, Bundle: bundle}
		if consume {
			preKey, err := s.claimOneTimePreKey(ctx, bundle.Id, accountID, device.DeviceId, requesterID)
			if err != nil {
				return nil, err
			}
			resp.ClaimedPreKey = preKey
		}
		responses = append(responses, resp)
	}
	return responses, nil
}

func (s *Store) getE2eeKeyBundle(ctx context.Context, accountID, deviceID string) (*E2eeKeyBundle, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+e2eeKeyBundleColumns+` FROM e2ee_key_bundles
		WHERE account_id = $1 AND device_id = $2 AND deleted_at IS NULL`, accountID, deviceID)
	return scanE2eeKeyBundle(row)
}

func (s *Store) getLatestE2eeKeyBundle(ctx context.Context, accountID string) (*E2eeKeyBundle, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+e2eeKeyBundleColumns+` FROM e2ee_key_bundles
		WHERE account_id = $1 AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT 1`, accountID)
	return scanE2eeKeyBundle(row)
}

func (s *Store) listE2eeKeyBundles(ctx context.Context, accountID string) ([]E2eeKeyBundle, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+e2eeKeyBundleColumns+` FROM e2ee_key_bundles
		WHERE account_id = $1 AND deleted_at IS NULL`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bundles []E2eeKeyBundle
	for rows.Next() {
		b, err := scanE2eeKeyBundle(rows)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, *b)
	}
	return bundles, rows.Err()
}

func (s *Store) listOneTimePreKeyIDs(ctx context.Context, keyBundleID string) (map[int]bool, error) {
	rows, err := s.DB.Query(ctx, `SELECT key_id FROM e2ee_one_time_pre_keys
		WHERE key_bundle_id = $1 AND deleted_at IS NULL`, keyBundleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// claimOneTimePreKey claims the oldest unclaimed pre key of a bundle inside a
// SERIALIZABLE transaction (the C# serializes claiming reads so concurrent
// claims cannot hand out the same pre key twice).
func (s *Store) claimOneTimePreKey(ctx context.Context, keyBundleID, accountID, deviceID, requesterID string) (*E2eeOneTimePreKey, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `SELECT `+e2eeOneTimePreKeyColumns+` FROM e2ee_one_time_pre_keys
		WHERE key_bundle_id = $1 AND account_id = $2 AND device_id = $3 AND is_claimed = false AND deleted_at IS NULL
		ORDER BY key_id LIMIT 1`, keyBundleID, accountID, deviceID)
	preKey, err := scanE2eeOneTimePreKey(row)
	if err != nil {
		return nil, err
	}
	if preKey == nil {
		return nil, tx.Commit(ctx)
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `UPDATE e2ee_one_time_pre_keys
		SET is_claimed = true, claimed_at = $2, claimed_by_account_id = $3, updated_at = $2
		WHERE id = $1`, preKey.Id, now, requesterID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	preKey.IsClaimed = true
	preKey.ClaimedAt = &now
	preKey.ClaimedByAccountId = &requesterID
	preKey.UpdatedAt = now
	return preKey, nil
}

// --- MLS key packages ---

// PurgeExpiredMlsKeyPackages deletes key packages older than the retention
// cutoff (mirrors PurgeExpiredMlsKeyPackagesAsync; 30 days).
func (s *Store) PurgeExpiredMlsKeyPackages(ctx context.Context, accountID string, cutoff time.Time) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM mls_key_packages WHERE account_id = $1 AND created_at < $2`, accountID, cutoff)
	return err
}

// CountMlsKeyPackagesUploadedSince counts key packages uploaded in the window
// (used for the 10-per-account-per-24h upload limit).
func (s *Store) CountMlsKeyPackagesUploadedSince(ctx context.Context, accountID string, since time.Time) (int64, error) {
	var count int64
	err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM mls_key_packages
		WHERE account_id = $1 AND created_at >= $2`, accountID, since).Scan(&count)
	return count, err
}

// InsertMlsKeyPackage stores a published key package.
func (s *Store) InsertMlsKeyPackage(ctx context.Context, kp *MlsKeyPackage) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO mls_key_packages (id, account_id, device_id, device_label, key_package, ciphersuite, is_consumed, consumed_at, consumed_by_account_id, meta, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, false, NULL, NULL, $7, $8, $8, NULL)`,
		kp.Id, kp.AccountId, kp.DeviceId, kp.DeviceLabel, kp.KeyPackage, kp.Ciphersuite, jsonBytes(kp.Meta), kp.CreatedAt)
	return err
}

// CountUnconsumedMlsKeyPackages counts the non-consumed key packages of a
// device (KP-depleted check).
func (s *Store) CountUnconsumedMlsKeyPackages(ctx context.Context, accountID, deviceID string) (int64, error) {
	var count int64
	err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM mls_key_packages
		WHERE account_id = $1 AND device_id = $2 AND is_consumed = false AND deleted_at IS NULL`,
		accountID, deviceID).Scan(&count)
	return count, err
}

// ListMlsDeviceKeyPackages returns the oldest unconsumed key package per
// active device. When consume is true the read-and-claim loop runs inside a
// SERIALIZABLE transaction (mirrors ListMlsDeviceKeyPackagesAsync) so
// concurrent claims cannot return the same package twice; the consumed
// devices are returned for the KP-depleted notification.
func (s *Store) ListMlsDeviceKeyPackages(ctx context.Context, accountID string, requesterID *string, consume bool) ([]DeviceKeyPackage, []ConsumedDevice, error) {
	q := queryer(s.DB)
	var tx pgx.Tx
	if consume {
		var err error
		tx, err = s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(ctx)
		q = tx
	}

	rows, err := q.Query(ctx, `SELECT `+e2eeDeviceColumns+` FROM e2ee_devices
		WHERE account_id = $1 AND is_revoked = false AND deleted_at IS NULL ORDER BY created_at`, accountID)
	if err != nil {
		return nil, nil, err
	}
	var devices []E2eeDevice
	for rows.Next() {
		d, err := scanE2eeDevice(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		devices = append(devices, *d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var responses []DeviceKeyPackage
	var consumed []ConsumedDevice
	var dirty bool
	for _, device := range devices {
		row := q.QueryRow(ctx, `SELECT `+mlsKeyPackageColumns+` FROM mls_key_packages
			WHERE account_id = $1 AND device_id = $2 AND is_consumed = false AND deleted_at IS NULL
			ORDER BY created_at LIMIT 1`, accountID, device.DeviceId)
		packageRow, err := scanMlsKeyPackage(row)
		if err != nil {
			return nil, nil, err
		}
		if packageRow == nil {
			continue
		}
		if consume && !packageRow.IsConsumed {
			now := time.Now().UTC()
			if _, err := q.Exec(ctx, `UPDATE mls_key_packages
				SET is_consumed = true, consumed_at = $2, consumed_by_account_id = $3, updated_at = $2
				WHERE id = $1`, packageRow.Id, now, requesterID); err != nil {
				return nil, nil, err
			}
			dirty = true
			consumed = append(consumed, ConsumedDevice{DeviceID: device.DeviceId, DeviceLabel: device.DeviceLabel})
		}
		responses = append(responses, DeviceKeyPackage{Device: device, Package: *packageRow})
	}
	if dirty && tx != nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
	}
	return responses, consumed, nil
}

// MlsKeyPackageStatus is one device's available-count row for the KP status
// endpoint.
type MlsKeyPackageStatus struct {
	DeviceID       string
	DeviceLabel    *string
	AvailableCount int
}

// MlsKeyPackageStatusPerDevice returns the devices with fewer than 3
// non-consumed key packages (mirrors GetMlsKeyPackageStatusAsync).
func (s *Store) MlsKeyPackageStatusPerDevice(ctx context.Context, accountID string) ([]MlsKeyPackageStatus, error) {
	rows, err := s.DB.Query(ctx, `SELECT d.device_id, d.device_label, COUNT(k.id)::int
		FROM e2ee_devices d
		LEFT JOIN mls_key_packages k ON k.account_id = d.account_id AND k.device_id = d.device_id
			AND k.is_consumed = false AND k.deleted_at IS NULL
		WHERE d.account_id = $1 AND d.is_revoked = false AND d.deleted_at IS NULL
		GROUP BY d.device_id, d.device_label
		HAVING COUNT(k.id) < 3`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var statuses []MlsKeyPackageStatus
	for rows.Next() {
		var st MlsKeyPackageStatus
		if err := rows.Scan(&st.DeviceID, &st.DeviceLabel, &st.AvailableCount); err != nil {
			return nil, err
		}
		statuses = append(statuses, st)
	}
	return statuses, rows.Err()
}

// GetCapableDevices returns the oldest unconsumed key package per group member
// device (mirrors GetCapableDevicesAsync).
func (s *Store) GetCapableDevices(ctx context.Context, groupID string) ([]MlsKeyPackage, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+mlsKeyPackageColumns+` FROM mls_device_memberships m
		JOIN LATERAL (
			SELECT k.* FROM mls_key_packages k
			WHERE k.account_id = m.account_id AND k.device_id = m.device_id
				AND k.is_consumed = false AND k.deleted_at IS NULL
			ORDER BY k.created_at LIMIT 1
		) k ON true
		WHERE m.mls_group_id = $1 AND m.deleted_at IS NULL`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var packages []MlsKeyPackage
	for rows.Next() {
		p, err := scanMlsKeyPackage(rows)
		if err != nil {
			return nil, err
		}
		packages = append(packages, *p)
	}
	return packages, rows.Err()
}

// --- MLS group states ---

// GetMlsGroupStateByGroupID loads the group state, returning (nil, nil) when
// absent (FirstOrDefaultAsync semantics).
func (s *Store) GetMlsGroupStateByGroupID(ctx context.Context, groupID string) (*MlsGroupState, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+mlsGroupStateColumns+` FROM mls_group_states
		WHERE mls_group_id = $1 AND deleted_at IS NULL ORDER BY created_at LIMIT 1`, groupID)
	return scanMlsGroupState(row)
}

// BootstrapMlsGroup creates the group state when absent inside a SERIALIZABLE
// transaction; replaying a bootstrap returns the existing state unchanged
// (mirrors BootstrapMlsGroupAsync).
func (s *Store) BootstrapMlsGroup(ctx context.Context, accountID, groupID string, epoch, stateVersion int64, meta map[string]any, now time.Time) (*MlsGroupState, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `SELECT `+mlsGroupStateColumns+` FROM mls_group_states
		WHERE mls_group_id = $1 AND deleted_at IS NULL ORDER BY created_at LIMIT 1`, groupID)
	existing, err := scanMlsGroupState(row)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, tx.Commit(ctx)
	}

	state := &MlsGroupState{
		Id:           uuid.NewString(),
		MlsGroupId:   groupID,
		Epoch:        epoch,
		StateVersion: stateVersion,
		LastCommitAt: &now,
		GroupInfo:    []byte{},
		RatchetTree:  []byte{},
		Meta:         meta,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mls_group_states (id, mls_group_id, epoch, state_version, last_commit_at, group_info, ratchet_tree, meta, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, NULL)`,
		state.Id, state.MlsGroupId, state.Epoch, state.StateVersion, state.LastCommitAt,
		state.GroupInfo, state.RatchetTree, jsonBytes(state.Meta), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return state, nil
}

// UpdateMlsGroupState persists a mutated group state row.
func (s *Store) UpdateMlsGroupState(ctx context.Context, state *MlsGroupState) error {
	_, err := s.DB.Exec(ctx, `UPDATE mls_group_states
		SET epoch = $2, state_version = $3, last_commit_at = $4, group_info = $5, ratchet_tree = $6, meta = $7, updated_at = $8
		WHERE id = $1`,
		state.Id, state.Epoch, state.StateVersion, state.LastCommitAt, state.GroupInfo,
		state.RatchetTree, jsonBytes(state.Meta), state.UpdatedAt)
	return err
}

// CreateMlsGroup inserts a fresh group state (mirrors CreateMlsGroupAsync,
// used by the group reset flow).
func (s *Store) CreateMlsGroup(ctx context.Context, groupID string, epoch, stateVersion int64, now time.Time) (*MlsGroupState, error) {
	state := &MlsGroupState{
		Id:           uuid.NewString(),
		MlsGroupId:   groupID,
		Epoch:        epoch,
		StateVersion: stateVersion,
		LastCommitAt: &now,
		GroupInfo:    []byte{},
		RatchetTree:  []byte{},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO mls_group_states (id, mls_group_id, epoch, state_version, last_commit_at, group_info, ratchet_tree, meta, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, $8, $8, NULL)`,
		state.Id, state.MlsGroupId, state.Epoch, state.StateVersion, state.LastCommitAt,
		state.GroupInfo, state.RatchetTree, now); err != nil {
		return nil, err
	}
	return state, nil
}

// UploadGroupInfo writes the GroupInfo/RatchetTree for the expected epoch
// inside a SERIALIZABLE transaction (mirrors UploadGroupInfoAsync).
func (s *Store) UploadGroupInfo(ctx context.Context, groupID string, groupInfo, ratchetTree []byte, expectedEpoch *int64, now time.Time) (*UploadGroupInfoResult, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `SELECT `+mlsGroupStateColumns+` FROM mls_group_states
		WHERE mls_group_id = $1 AND deleted_at IS NULL ORDER BY created_at LIMIT 1`, groupID)
	state, err := scanMlsGroupState(row)
	if err != nil {
		return nil, err
	}
	if state == nil { // no state row
		if expectedEpoch != nil {
			return &UploadGroupInfoResult{Success: false, GroupID: groupID, Epoch: -1}, tx.Commit(ctx)
		}
		state = &MlsGroupState{
			Id:           uuid.NewString(),
			MlsGroupId:   groupID,
			Epoch:        0,
			StateVersion: 0,
			GroupInfo:    groupInfo,
			RatchetTree:  ratchetTree,
			LastCommitAt: &now,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mls_group_states (id, mls_group_id, epoch, state_version, last_commit_at, group_info, ratchet_tree, meta, created_at, updated_at, deleted_at)
			VALUES ($1, $2, 0, 0, $3, $4, $5, NULL, $6, $6, NULL)`,
			state.Id, state.MlsGroupId, now, groupInfo, ratchetTree, now); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &UploadGroupInfoResult{Success: true, GroupID: state.MlsGroupId, Epoch: 0}, nil
	}

	if expectedEpoch != nil && state.Epoch != *expectedEpoch {
		return &UploadGroupInfoResult{Success: false, GroupID: state.MlsGroupId, Epoch: state.Epoch}, tx.Commit(ctx)
	}
	state.GroupInfo = groupInfo
	state.RatchetTree = ratchetTree
	state.UpdatedAt = now
	if _, err := tx.Exec(ctx, `UPDATE mls_group_states SET group_info = $2, ratchet_tree = $3, updated_at = $4 WHERE id = $1`,
		state.Id, groupInfo, ratchetTree, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &UploadGroupInfoResult{Success: true, GroupID: state.MlsGroupId, Epoch: state.Epoch}, nil
}

// DeleteMlsGroup soft-deletes the group states and member device rows of a
// group (EF RemoveRange + the soft-delete save interceptor), returning the
// number of deleted states.
func (s *Store) DeleteMlsGroup(ctx context.Context, groupID string, now time.Time) (int64, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE mls_group_states SET deleted_at = $2, updated_at = $2
		WHERE mls_group_id = $1 AND deleted_at IS NULL`, groupID, now)
	if err != nil {
		return 0, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE mls_device_memberships SET deleted_at = $2, updated_at = $2
		WHERE mls_group_id = $1 AND deleted_at IS NULL`, groupID, now); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// --- MLS device memberships ---

// ListMlsMembershipsByGroup lists the member devices of a group (excludes
// soft-deleted rows).
func (s *Store) ListMlsMembershipsByGroup(ctx context.Context, groupID string) ([]MlsDeviceMembership, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+mlsDeviceMembershipColumns+` FROM mls_device_memberships
		WHERE mls_group_id = $1 AND deleted_at IS NULL`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memberships []MlsDeviceMembership
	for rows.Next() {
		m, err := scanMlsDeviceMembership(rows)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, *m)
	}
	return memberships, rows.Err()
}

// ListMlsGroupMemberAccountIDs returns the distinct member account ids of a
// group (group reset notification).
func (s *Store) ListMlsGroupMemberAccountIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT account_id FROM mls_device_memberships
		WHERE mls_group_id = $1 AND deleted_at IS NULL`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// IsMlsGroupMember reports whether (account, device) is a member of the group.
func (s *Store) IsMlsGroupMember(ctx context.Context, accountID, deviceID, groupID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM mls_device_memberships
		WHERE mls_group_id = $1 AND account_id = $2 AND device_id = $3 AND deleted_at IS NULL)`,
		groupID, accountID, deviceID).Scan(&exists)
	return exists, err
}

// MarkMlsReshareRequired creates or updates the membership with
// last_reshare_required_at set (mirrors MarkMlsReshareRequiredAsync).
func (s *Store) MarkMlsReshareRequired(ctx context.Context, groupID, accountID, deviceID string, epoch int64, now time.Time) (*MlsDeviceMembership, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+mlsDeviceMembershipColumns+` FROM mls_device_memberships
		WHERE mls_group_id = $1 AND account_id = $2 AND device_id = $3 AND deleted_at IS NULL
		LIMIT 1`, groupID, accountID, deviceID)
	membership, err := scanMlsDeviceMembership(row)
	if err != nil {
		return nil, err
	}
	if membership == nil { // create
		lastSeen := epoch
		membership = &MlsDeviceMembership{
			Id:          uuid.NewString(),
			MlsGroupId:  groupID,
			AccountId:   accountID,
			DeviceId:    deviceID,
			JoinedEpoch: epoch,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO mls_device_memberships (id, mls_group_id, account_id, device_id, joined_epoch, last_seen_epoch, last_reshare_required_at, last_reshare_completed_at, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, $4, $5, $5, $6, NULL, $7, $7, NULL)`,
			membership.Id, membership.MlsGroupId, membership.AccountId, membership.DeviceId,
			membership.JoinedEpoch, now, now); err != nil {
			return nil, err
		}
		membership.LastSeenEpoch = &lastSeen
		membership.LastReshareRequiredAt = &now
		return membership, nil
	}
	lastSeen := epoch
	if _, err := s.DB.Exec(ctx, `UPDATE mls_device_memberships
		SET last_seen_epoch = $2, last_reshare_required_at = $3, updated_at = $3
		WHERE id = $1`, membership.Id, epoch, now); err != nil {
		return nil, err
	}
	membership.LastSeenEpoch = &lastSeen
	membership.LastReshareRequiredAt = &now
	membership.UpdatedAt = now
	return membership, nil
}

// UpsertMlsDeviceMembership creates or revives the membership row. Unlike the
// other membership queries this includes soft-deleted rows (the C# uses
// IgnoreQueryFilters) and clears the reshare markers (mirrors
// AddMlsDeviceMembershipAsync).
func (s *Store) UpsertMlsDeviceMembership(ctx context.Context, groupID, accountID, deviceID string, epoch int64, now time.Time) (*MlsDeviceMembership, error) {
	row := s.DB.QueryRow(ctx, `SELECT `+mlsDeviceMembershipColumns+` FROM mls_device_memberships
		WHERE mls_group_id = $1 AND account_id = $2 AND device_id = $3 LIMIT 1`, groupID, accountID, deviceID)
	membership, err := scanMlsDeviceMembership(row)
	if err != nil {
		return nil, err
	}
	if membership == nil {
		lastSeen := epoch
		membership = &MlsDeviceMembership{
			Id:            uuid.NewString(),
			MlsGroupId:    groupID,
			AccountId:     accountID,
			DeviceId:      deviceID,
			JoinedEpoch:   epoch,
			LastSeenEpoch: &lastSeen,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		_, err := s.DB.Exec(ctx, `INSERT INTO mls_device_memberships (id, mls_group_id, account_id, device_id, joined_epoch, last_seen_epoch, last_reshare_required_at, last_reshare_completed_at, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, $4, $5, $5, NULL, NULL, $6, $6, NULL)`,
			membership.Id, membership.MlsGroupId, membership.AccountId, membership.DeviceId,
			membership.JoinedEpoch, now)
		if err != nil {
			return nil, err
		}
		return membership, nil
	}
	lastSeen := epoch
	if _, err := s.DB.Exec(ctx, `UPDATE mls_device_memberships
		SET deleted_at = NULL, last_seen_epoch = $2, last_reshare_required_at = NULL, last_reshare_completed_at = NULL, updated_at = $3
		WHERE id = $1`, membership.Id, epoch, now); err != nil {
		return nil, err
	}
	membership.DeletedAt = nil
	membership.LastSeenEpoch = &lastSeen
	membership.LastReshareRequiredAt = nil
	membership.LastReshareCompletedAt = nil
	membership.UpdatedAt = now
	return membership, nil
}

// ListDeviceReshareStatus lists the pending reshare memberships of a device
// (last_reshare_required_at set, last_reshare_completed_at null).
func (s *Store) ListDeviceReshareStatus(ctx context.Context, accountID, deviceID string) ([]MlsDeviceMembership, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+mlsDeviceMembershipColumns+` FROM mls_device_memberships
		WHERE account_id = $1 AND device_id = $2
			AND last_reshare_required_at IS NOT NULL AND last_reshare_completed_at IS NULL
			AND deleted_at IS NULL`, accountID, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memberships []MlsDeviceMembership
	for rows.Next() {
		m, err := scanMlsDeviceMembership(rows)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, *m)
	}
	return memberships, rows.Err()
}

// CompleteMlsReshare sets last_reshare_completed_at on the membership.
func (s *Store) CompleteMlsReshare(ctx context.Context, accountID, deviceID, groupID string, now time.Time) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE mls_device_memberships
		SET last_reshare_completed_at = $2, updated_at = $2
		WHERE account_id = $1 AND device_id = $2 AND mls_group_id = $3 AND deleted_at IS NULL`,
		accountID, deviceID, groupID, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MarkAllDevicesReshareRequired flags every group member device for reshare
// (group reset), returning the number of memberships updated.
func (s *Store) MarkAllDevicesReshareRequired(ctx context.Context, groupID string, now time.Time) (int64, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE mls_device_memberships
		SET last_reshare_required_at = $2, last_reshare_completed_at = NULL, updated_at = $2
		WHERE mls_group_id = $1 AND deleted_at IS NULL`, groupID, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// --- E2EE sessions / envelopes ---

// AccountExists reports whether the account row exists.
func (s *Store) AccountExists(ctx context.Context, accountID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $1 AND deleted_at IS NULL)`, accountID).Scan(&exists)
	return exists, err
}

// TouchE2eeSession bumps the session's last_message_at (fanout with a session
// id).
func (s *Store) TouchE2eeSession(ctx context.Context, sessionID string, now time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE e2ee_sessions SET last_message_at = $2, updated_at = $2
		WHERE id = $1 AND deleted_at IS NULL`, sessionID, now)
	return err
}

// InsertEnvelope stores an envelope deduplicating on client_message_id and
// assigning the next monotonic sequence per (recipient_account_id,
// recipient_device_id), mirroring CreateEnvelopeForTargetAsync. When a
// duplicate client_message_id exists the existing envelope is returned and
// nothing is inserted.
func (s *Store) InsertEnvelope(ctx context.Context, env *E2eeEnvelope) (*E2eeEnvelope, error) {
	if env.ClientMessageId != nil && *env.ClientMessageId != "" {
		row := s.DB.QueryRow(ctx, `SELECT `+e2eeEnvelopeColumns+` FROM e2ee_envelopes
			WHERE sender_id = $1 AND sender_device_id IS NOT DISTINCT FROM $2
				AND recipient_account_id = $3 AND recipient_device_id IS NOT DISTINCT FROM $4
				AND client_message_id = $5 AND deleted_at IS NULL
			LIMIT 1`,
			env.SenderId, env.SenderDeviceId, env.RecipientAccountId, env.RecipientDeviceId, env.ClientMessageId)
		existing, err := scanE2eeEnvelope(row)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}
	var seq int64
	if err := s.DB.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM e2ee_envelopes
		WHERE recipient_account_id = $1 AND recipient_device_id IS NOT DISTINCT FROM $2`,
		env.RecipientAccountId, env.RecipientDeviceId).Scan(&seq); err != nil {
		return nil, err
	}
	env.Sequence = seq
	_, err := s.DB.Exec(ctx, `INSERT INTO e2ee_envelopes (id, sender_id, sender_device_id, recipient_id, recipient_account_id, recipient_device_id, session_id, type, group_id, client_message_id, sequence, ciphertext, header, signature, delivery_status, delivered_at, acked_at, expires_at, legacy_account_scoped, meta, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 0, NULL, NULL, $15, $16, $17, $18, $18, NULL)`,
		env.Id, env.SenderId, env.SenderDeviceId, env.RecipientId, env.RecipientAccountId, env.RecipientDeviceId,
		env.SessionId, env.Type, env.GroupId, env.ClientMessageId, env.Sequence, env.Ciphertext, env.Header,
		env.Signature, env.ExpiresAt, env.LegacyAccountScoped, jsonBytes(env.Meta), env.CreatedAt)
	if err != nil {
		return nil, err
	}
	return env, nil
}

// GetPendingEnvelopesByDevice returns the undelivered envelopes of a device
// (delivery_status != Acknowledged, unexpired) in sequence order, marking
// pending rows Delivered (mirrors GetPendingEnvelopesByDeviceAsync). Returns
// an empty slice when the device is not active.
func (s *Store) GetPendingEnvelopesByDevice(ctx context.Context, accountID, deviceID string, take int, now time.Time) ([]E2eeEnvelope, error) {
	var active bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM e2ee_devices WHERE account_id = $1 AND device_id = $2 AND is_revoked = false AND deleted_at IS NULL)`,
		accountID, deviceID).Scan(&active); err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}

	rows, err := s.DB.Query(ctx, `SELECT `+e2eeEnvelopeColumns+` FROM e2ee_envelopes
		WHERE recipient_account_id = $1 AND recipient_device_id = $2
			AND delivery_status <> 2
			AND (expires_at IS NULL OR expires_at > $3)
		ORDER BY sequence LIMIT $4`, accountID, deviceID, now, take)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var envelopes []E2eeEnvelope
	var pendingIDs []string
	for rows.Next() {
		e, err := scanE2eeEnvelope(rows)
		if err != nil {
			return nil, err
		}
		if e.DeliveryStatus == 0 { // Pending
			pendingIDs = append(pendingIDs, e.Id)
			e.DeliveryStatus = 1 // Delivered
			e.DeliveredAt = &now
		}
		envelopes = append(envelopes, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pendingIDs) > 0 {
		if _, err := s.DB.Exec(ctx, `UPDATE e2ee_envelopes SET delivery_status = 1, delivered_at = $2, updated_at = $2
			WHERE id = ANY($1)`, pendingIDs, now); err != nil {
			return nil, err
		}
	}
	return envelopes, nil
}

// AckEnvelopeByDevice acknowledges a device envelope, returning nil when the
// device is inactive or the envelope is missing (both map to the C# null
// result).
func (s *Store) AckEnvelopeByDevice(ctx context.Context, accountID, deviceID, envelopeID string, now time.Time) (*E2eeEnvelope, error) {
	var active bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM e2ee_devices WHERE account_id = $1 AND device_id = $2 AND is_revoked = false AND deleted_at IS NULL)`,
		accountID, deviceID).Scan(&active); err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	row := s.DB.QueryRow(ctx, `SELECT `+e2eeEnvelopeColumns+` FROM e2ee_envelopes
		WHERE id = $1 AND recipient_account_id = $2 AND recipient_device_id = $3 AND deleted_at IS NULL`,
		envelopeID, accountID, deviceID)
	env, err := scanE2eeEnvelope(row)
	if err != nil {
		return nil, err
	}
	if env == nil {
		return nil, nil
	}
	if _, err := s.DB.Exec(ctx, `UPDATE e2ee_envelopes SET delivery_status = 2, acked_at = $2, updated_at = $2 WHERE id = $1`,
		env.Id, now); err != nil {
		return nil, err
	}
	env.DeliveryStatus = 2
	env.AckedAt = &now
	env.UpdatedAt = now
	return env, nil
}

// MarkEnvelopeDelivered flips a pushed envelope to Delivered.
func (s *Store) MarkEnvelopeDelivered(ctx context.Context, envelopeID string, now time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE e2ee_envelopes SET delivery_status = 1, delivered_at = $2, updated_at = $2
		WHERE id = $1`, envelopeID, now)
	return err
}

// RevokeDevice marks the device revoked, purges its unacknowledged pending
// envelopes, and inserts a control envelope for every active sibling device
// inside a single transaction (mirrors RevokeDeviceAsync, whose implicit EF
// SaveChanges transaction wraps the same steps).
func (s *Store) RevokeDevice(ctx context.Context, accountID, deviceID string, now time.Time) (*RevokeDeviceResult, error) {
	result := &RevokeDeviceResult{}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `SELECT `+e2eeDeviceColumns+` FROM e2ee_devices
		WHERE account_id = $1 AND device_id = $2 AND deleted_at IS NULL`, accountID, deviceID)
	device, err := scanE2eeDevice(row)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return result, tx.Commit(ctx)
	}
	result.Found = true
	if device.IsRevoked {
		result.AlreadyRevoked = true
		return result, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE e2ee_devices SET is_revoked = true, revoked_at = $2, updated_at = $2 WHERE id = $1`,
		device.Id, now); err != nil {
		return nil, err
	}

	// Purge pending envelopes for the revoked device.
	rows, err := tx.Query(ctx, `DELETE FROM e2ee_envelopes
		WHERE recipient_account_id = $1 AND recipient_device_id = $2 AND delivery_status <> 2 RETURNING id`,
		accountID, deviceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		result.PurgedCount++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Control envelopes for sibling devices.
	siblingRows, err := tx.Query(ctx, `SELECT device_id FROM e2ee_devices
		WHERE account_id = $1 AND is_revoked = false AND device_id <> $2 AND deleted_at IS NULL`, accountID, deviceID)
	if err != nil {
		return nil, err
	}
	var siblings []string
	for siblingRows.Next() {
		var id string
		if err := siblingRows.Scan(&id); err != nil {
			siblingRows.Close()
			return nil, err
		}
		siblings = append(siblings, id)
	}
	siblingRows.Close()
	if err := siblingRows.Err(); err != nil {
		return nil, err
	}

	for _, targetDeviceID := range siblings {
		clientMessageID := fmt.Sprintf("mls-revoke-%s-%d-%s", deviceID, now.UnixMilli(), targetDeviceID)
		var seq int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM e2ee_envelopes
			WHERE recipient_account_id = $1 AND recipient_device_id IS NOT DISTINCT FROM $2`,
			accountID, targetDeviceID).Scan(&seq); err != nil {
			return nil, err
		}
		control := E2eeEnvelope{
			Id:                 uuid.NewString(),
			SenderId:           accountID,
			SenderDeviceId:     strPtr(LegacyDeviceID),
			RecipientId:        accountID,
			RecipientAccountId: accountID,
			RecipientDeviceId:  &targetDeviceID,
			Type:               3, // Control
			ClientMessageId:    &clientMessageID,
			Sequence:           seq,
			Ciphertext:         []byte{1},
			Meta:               map[string]any{"event": "mls_device_revoked", "revoked_device_id": deviceID},
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if _, err := tx.Exec(ctx, `INSERT INTO e2ee_envelopes (id, sender_id, sender_device_id, recipient_id, recipient_account_id, recipient_device_id, session_id, type, group_id, client_message_id, sequence, ciphertext, header, signature, delivery_status, delivered_at, acked_at, expires_at, legacy_account_scoped, meta, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, $4, $5, $6, NULL, $7, NULL, $8, $9, $10, NULL, NULL, 0, NULL, NULL, NULL, false, $11, $12, $12, NULL)`,
			control.Id, control.SenderId, control.SenderDeviceId, control.RecipientId, control.RecipientAccountId,
			control.RecipientDeviceId, control.Type, control.ClientMessageId, control.Sequence, control.Ciphertext,
			jsonBytes(control.Meta), now); err != nil {
			return nil, err
		}
		result.ControlEnvelopes = append(result.ControlEnvelopes, control)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// --- scan helpers ---

func scanE2eeDevice(row pgx.Row) (*E2eeDevice, error) {
	var d E2eeDevice
	err := row.Scan(&d.Id, &d.AccountId, &d.DeviceId, &d.DeviceLabel, &d.IsRevoked,
		&d.LastBundleAt, &d.RevokedAt, &d.CreatedAt, &d.UpdatedAt, &d.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func scanE2eeKeyBundle(row pgx.Row) (*E2eeKeyBundle, error) {
	var b E2eeKeyBundle
	var meta []byte
	err := row.Scan(&b.Id, &b.AccountId, &b.DeviceId, &b.Algorithm, &b.IdentityKey, &b.SignedPreKeyId,
		&b.SignedPreKey, &b.SignedPreKeySignature, &b.SignedPreKeyExpiresAt, &meta,
		&b.CreatedAt, &b.UpdatedAt, &b.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Meta = parseJSONMap(meta)
	return &b, nil
}

func scanE2eeOneTimePreKey(row pgx.Row) (*E2eeOneTimePreKey, error) {
	var k E2eeOneTimePreKey
	err := row.Scan(&k.Id, &k.KeyBundleId, &k.AccountId, &k.DeviceId, &k.KeyId, &k.PublicKey,
		&k.IsClaimed, &k.ClaimedAt, &k.ClaimedByAccountId, &k.CreatedAt, &k.UpdatedAt, &k.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func scanE2eeEnvelope(row pgx.Row) (*E2eeEnvelope, error) {
	var e E2eeEnvelope
	var meta []byte
	err := row.Scan(&e.Id, &e.SenderId, &e.SenderDeviceId, &e.RecipientId, &e.RecipientAccountId,
		&e.RecipientDeviceId, &e.SessionId, &e.Type, &e.GroupId, &e.ClientMessageId, &e.Sequence,
		&e.Ciphertext, &e.Header, &e.Signature, &e.DeliveryStatus, &e.DeliveredAt, &e.AckedAt,
		&e.ExpiresAt, &e.LegacyAccountScoped, &meta, &e.CreatedAt, &e.UpdatedAt, &e.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Meta = parseJSONMap(meta)
	return &e, nil
}

func scanMlsKeyPackage(row pgx.Row) (*MlsKeyPackage, error) {
	var k MlsKeyPackage
	var meta []byte
	err := row.Scan(&k.Id, &k.AccountId, &k.DeviceId, &k.DeviceLabel, &k.KeyPackage, &k.Ciphersuite,
		&k.IsConsumed, &k.ConsumedAt, &k.ConsumedByAccountId, &meta, &k.CreatedAt, &k.UpdatedAt, &k.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k.Meta = parseJSONMap(meta)
	return &k, nil
}

func scanMlsGroupState(row pgx.Row) (*MlsGroupState, error) {
	var st MlsGroupState
	var meta []byte
	err := row.Scan(&st.Id, &st.MlsGroupId, &st.Epoch, &st.StateVersion, &st.LastCommitAt,
		&st.GroupInfo, &st.RatchetTree, &meta, &st.CreatedAt, &st.UpdatedAt, &st.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.Meta = parseJSONMap(meta)
	return &st, nil
}

func scanMlsDeviceMembership(row pgx.Row) (*MlsDeviceMembership, error) {
	var m MlsDeviceMembership
	err := row.Scan(&m.Id, &m.MlsGroupId, &m.AccountId, &m.DeviceId, &m.JoinedEpoch, &m.LastSeenEpoch,
		&m.LastReshareRequiredAt, &m.LastReshareCompletedAt, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func parseJSONMap(b []byte) map[string]any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

func jsonBytes(m map[string]any) []byte {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

func strPtr(s string) *string { return &s }
