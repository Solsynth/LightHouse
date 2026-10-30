// Package store e2ee/mls queries. These mirror the EF queries in
// DysonNetwork.Padlock E2EeService (snake_case table/column names from
// internal/migrate/0001_initial.sql). Soft-delete rows are excluded everywhere
// except where the C# uses IgnoreQueryFilters (AddMlsDeviceMembershipAsync).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
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

// --- Devices ---

// GetE2eeDevice loads a device by (account_id, device_id), excluding
// soft-deleted rows (the EF global query filter). Returns (nil, nil) when
// absent.
func (s *Store) GetE2eeDevice(ctx context.Context, accountID, deviceID string) (*E2eeDevice, error) {
	var entities []E2EEDeviceEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND device_id = ?", accountID, deviceID).
		Limit(1).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	device := e2eeDeviceFromEntity(&entities[0])
	return &device, nil
}

// ListActiveE2eeDevices lists the non-revoked devices of an account.
func (s *Store) ListActiveE2eeDevices(ctx context.Context, accountID string) ([]E2eeDevice, error) {
	var entities []E2EEDeviceEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND is_revoked = false", accountID).
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	var devices []E2eeDevice
	for i := range entities {
		devices = append(devices, e2eeDeviceFromEntity(&entities[i]))
	}
	return devices, nil
}

// ListActiveE2eeDeviceIDs returns the device ids of an account's non-revoked
// devices.
func (s *Store) ListActiveE2eeDeviceIDs(ctx context.Context, accountID string) ([]string, error) {
	var entities []E2EEDeviceEntity
	if err := s.DB.WithContext(ctx).
		Select("device_id").
		Where("account_id = ? AND is_revoked = false", accountID).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	var ids []string
	for i := range entities {
		ids = append(ids, entities[i].DeviceID)
	}
	return ids, nil
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
		accountUUID, err := uuidValue(accountID)
		if err != nil {
			return nil, err
		}
		entity := &E2EEDeviceEntity{
			ID:           uuid.New(),
			EntityBase:   EntityBase{CreatedAt: now, UpdatedAt: now},
			AccountID:    accountUUID,
			DeviceID:     deviceID,
			DeviceLabel:  deviceLabel,
			IsRevoked:    false,
			LastBundleAt: &now,
		}
		if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
			return nil, err
		}
		created := e2eeDeviceFromEntity(entity)
		return &created, nil
	}
	label := device.DeviceLabel
	if deviceLabel != nil && *deviceLabel != "" {
		label = deviceLabel
	}
	if err := s.DB.WithContext(ctx).Model(&E2EEDeviceEntity{}).
		Where("id = ?", device.Id).
		Updates(map[string]any{
			"device_label":   label,
			"is_revoked":     false,
			"revoked_at":     nil,
			"last_bundle_at": now,
			"updated_at":     now,
		}).Error; err != nil {
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
		accountUUID, err := uuidValue(accountID)
		if err != nil {
			return nil, err
		}
		// identity_key / signed_pre_key / signed_pre_key_signature are NOT NULL;
		// the C# model used non-nullable byte[] fields, so a placeholder row
		// stores empty arrays (the previous raw INSERT wrote NULL and always
		// tripped the constraint).
		entity := &E2EEKeyBundleEntity{
			ID:                    uuid.New(),
			EntityBase:            EntityBase{CreatedAt: now, UpdatedAt: now},
			AccountID:             accountUUID,
			DeviceID:              deviceID,
			Algorithm:             "",
			IdentityKey:           []byte{},
			SignedPreKey:          []byte{},
			SignedPreKeySignature: []byte{},
		}
		if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
			return nil, err
		}
		created := e2eeKeyBundleFromEntity(entity)
		bundle = &created
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
	if err := s.DB.WithContext(ctx).Model(&E2EEKeyBundleEntity{}).
		Where("id = ?", bundle.Id).
		Updates(map[string]any{
			"algorithm":                 bundle.Algorithm,
			"identity_key":              bytesOrEmpty(bundle.IdentityKey),
			"signed_pre_key_id":         bundle.SignedPreKeyId,
			"signed_pre_key":            bytesOrEmpty(bundle.SignedPreKey),
			"signed_pre_key_signature":  bytesOrEmpty(bundle.SignedPreKeySignature),
			"signed_pre_key_expires_at": bundle.SignedPreKeyExpiresAt,
			"meta":                      jsonMapPtr(bundle.Meta),
			"updated_at":                now,
		}).Error; err != nil {
		return nil, err
	}

	if len(req.OneTimePreKeys) > 0 {
		existing, err := s.listOneTimePreKeyIDs(ctx, bundle.Id)
		if err != nil {
			return nil, err
		}
		for _, key := range req.OneTimePreKeys {
			if existing[key.KeyId] {
				continue
			}
			bundleUUID, err := uuidValue(bundle.Id)
			if err != nil {
				return nil, err
			}
			accountUUID, err := uuidValue(accountID)
			if err != nil {
				return nil, err
			}
			preKey := &E2EEOneTimePreKeyEntity{
				ID:          uuid.New(),
				EntityBase:  EntityBase{CreatedAt: now, UpdatedAt: now},
				KeyBundleID: bundleUUID,
				AccountID:   accountUUID,
				DeviceID:    deviceID,
				KeyID:       key.KeyId,
				PublicKey:   key.PublicKey,
				IsClaimed:   false,
			}
			if err := s.DB.WithContext(ctx).Create(preKey).Error; err != nil {
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
	var entities []E2EEKeyBundleEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND device_id = ?", accountID, deviceID).
		Limit(1).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	bundle := e2eeKeyBundleFromEntity(&entities[0])
	return &bundle, nil
}

func (s *Store) getLatestE2eeKeyBundle(ctx context.Context, accountID string) (*E2eeKeyBundle, error) {
	var entities []E2EEKeyBundleEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("updated_at DESC").Limit(1).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	bundle := e2eeKeyBundleFromEntity(&entities[0])
	return &bundle, nil
}

func (s *Store) listE2eeKeyBundles(ctx context.Context, accountID string) ([]E2eeKeyBundle, error) {
	var entities []E2EEKeyBundleEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).Find(&entities).Error; err != nil {
		return nil, err
	}
	var bundles []E2eeKeyBundle
	for i := range entities {
		bundles = append(bundles, e2eeKeyBundleFromEntity(&entities[i]))
	}
	return bundles, nil
}

func (s *Store) listOneTimePreKeyIDs(ctx context.Context, keyBundleID string) (map[int]bool, error) {
	var entities []E2EEOneTimePreKeyEntity
	if err := s.DB.WithContext(ctx).
		Select("key_id").
		Where("key_bundle_id = ?", keyBundleID).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	ids := map[int]bool{}
	for i := range entities {
		ids[entities[i].KeyID] = true
	}
	return ids, nil
}

// claimOneTimePreKey claims the oldest unclaimed pre key of a bundle inside a
// SERIALIZABLE transaction (the C# serializes claiming reads so concurrent
// claims cannot hand out the same pre key twice).
func (s *Store) claimOneTimePreKey(ctx context.Context, keyBundleID, accountID, deviceID, requesterID string) (*E2eeOneTimePreKey, error) {
	var claimed *E2eeOneTimePreKey
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var entities []E2EEOneTimePreKeyEntity
		if err := tx.
			Where("key_bundle_id = ? AND account_id = ? AND device_id = ? AND is_claimed = false",
				keyBundleID, accountID, deviceID).
			Order("key_id").Limit(1).Find(&entities).Error; err != nil {
			return err
		}
		if len(entities) == 0 {
			return nil
		}
		preKey := e2eeOneTimePreKeyFromEntity(&entities[0])
		now := time.Now().UTC()
		claimedBy, err := uuidPtrOrNil(&requesterID)
		if err != nil {
			return err
		}
		if err := tx.Model(&E2EEOneTimePreKeyEntity{}).
			Where("id = ?", preKey.Id).
			Updates(map[string]any{
				"is_claimed":            true,
				"claimed_at":            now,
				"claimed_by_account_id": claimedBy,
				"updated_at":            now,
			}).Error; err != nil {
			return err
		}
		preKey.IsClaimed = true
		preKey.ClaimedAt = &now
		preKey.ClaimedByAccountId = &requesterID
		preKey.UpdatedAt = now
		claimed = &preKey
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// --- MLS key packages ---

// PurgeExpiredMlsKeyPackages deletes key packages older than the retention
// cutoff (mirrors PurgeExpiredMlsKeyPackagesAsync; 30 days).
func (s *Store) PurgeExpiredMlsKeyPackages(ctx context.Context, accountID string, cutoff time.Time) error {
	// Hard delete of every matching row, soft-deleted ones included (the raw
	// statement had no deleted_at filter).
	return s.DB.WithContext(ctx).Unscoped().
		Where("account_id = ? AND created_at < ?", accountID, cutoff).
		Delete(&MLSKeyPackageEntity{}).Error
}

// CountMlsKeyPackagesUploadedSince counts key packages uploaded in the window
// (used for the 10-per-account-per-24h upload limit).
func (s *Store) CountMlsKeyPackagesUploadedSince(ctx context.Context, accountID string, since time.Time) (int64, error) {
	var count int64
	// Unscoped: the raw statement counted soft-deleted rows too.
	err := s.DB.WithContext(ctx).Unscoped().Model(&MLSKeyPackageEntity{}).
		Where("account_id = ? AND created_at >= ?", accountID, since).
		Count(&count).Error
	return count, err
}

// InsertMlsKeyPackage stores a published key package.
func (s *Store) InsertMlsKeyPackage(ctx context.Context, kp *MlsKeyPackage) error {
	entity, err := mlsKeyPackageEntity(kp)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Create(entity).Error
}

// CountUnconsumedMlsKeyPackages counts the non-consumed key packages of a
// device (KP-depleted check).
func (s *Store) CountUnconsumedMlsKeyPackages(ctx context.Context, accountID, deviceID string) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&MLSKeyPackageEntity{}).
		Where("account_id = ? AND device_id = ? AND is_consumed = false", accountID, deviceID).
		Count(&count).Error
	return count, err
}

// ListMlsDeviceKeyPackages returns the oldest unconsumed key package per
// active device. When consume is true the read-and-claim loop runs inside a
// SERIALIZABLE transaction (mirrors ListMlsDeviceKeyPackagesAsync) so
// concurrent claims cannot return the same package twice; the consumed
// devices are returned for the KP-depleted notification.
func (s *Store) ListMlsDeviceKeyPackages(ctx context.Context, accountID string, requesterID *string, consume bool) ([]DeviceKeyPackage, []ConsumedDevice, error) {
	var responses []DeviceKeyPackage
	var consumed []ConsumedDevice
	run := func(db *gorm.DB) error {
		var deviceEntities []E2EEDeviceEntity
		if err := db.
			Where("account_id = ? AND is_revoked = false", accountID).
			Order("created_at").Find(&deviceEntities).Error; err != nil {
			return err
		}
		for i := range deviceEntities {
			device := e2eeDeviceFromEntity(&deviceEntities[i])
			var packageEntities []MLSKeyPackageEntity
			if err := db.
				Where("account_id = ? AND device_id = ? AND is_consumed = false", accountID, device.DeviceId).
				Order("created_at").Limit(1).Find(&packageEntities).Error; err != nil {
				return err
			}
			if len(packageEntities) == 0 {
				continue
			}
			pkg := mlsKeyPackageFromEntity(&packageEntities[0])
			if consume && !pkg.IsConsumed {
				now := time.Now().UTC()
				consumedBy, err := uuidPtrOrNil(requesterID)
				if err != nil {
					return err
				}
				if err := db.Model(&MLSKeyPackageEntity{}).
					Where("id = ?", pkg.Id).
					Updates(map[string]any{
						"is_consumed":            true,
						"consumed_at":            now,
						"consumed_by_account_id": consumedBy,
						"updated_at":             now,
					}).Error; err != nil {
					return err
				}
				consumed = append(consumed, ConsumedDevice{DeviceID: device.DeviceId, DeviceLabel: device.DeviceLabel})
			}
			responses = append(responses, DeviceKeyPackage{Device: device, Package: pkg})
		}
		return nil
	}
	if consume {
		if err := s.DB.WithContext(ctx).
			Transaction(run, &sql.TxOptions{Isolation: sql.LevelSerializable}); err != nil {
			return nil, nil, err
		}
		return responses, consumed, nil
	}
	if err := run(s.DB.WithContext(ctx)); err != nil {
		return nil, nil, err
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
	var statuses []MlsKeyPackageStatus
	err := s.DB.WithContext(ctx).
		Model(&E2EEDeviceEntity{}).
		Select("e2ee_devices.device_id, e2ee_devices.device_label, COUNT(mls_key_packages.id)::int AS available_count").
		Joins(`LEFT JOIN mls_key_packages
			ON mls_key_packages.account_id = e2ee_devices.account_id
			AND mls_key_packages.device_id = e2ee_devices.device_id
			AND mls_key_packages.is_consumed = false AND mls_key_packages.deleted_at IS NULL`).
		Where("e2ee_devices.account_id = ? AND e2ee_devices.is_revoked = false", accountID).
		Group("e2ee_devices.device_id, e2ee_devices.device_label").
		Having("COUNT(mls_key_packages.id) < 3").
		Scan(&statuses).Error
	if err != nil {
		return nil, err
	}
	return statuses, nil
}

// GetCapableDevices returns the oldest unconsumed key package per group member
// device (mirrors GetCapableDevicesAsync).
func (s *Store) GetCapableDevices(ctx context.Context, groupID string) ([]MlsKeyPackage, error) {
	var entities []MLSKeyPackageEntity
	// Table() keeps the raw FROM/JOIN shape: the correlated LATERAL subquery
	// cannot be expressed with a typed GORM association, and both membership
	// soft-delete filters are part of the statement.
	err := s.DB.WithContext(ctx).
		Table("mls_device_memberships AS m").
		Select("k.*").
		Joins(`JOIN LATERAL (
			SELECT k.* FROM mls_key_packages k
			WHERE k.account_id = m.account_id AND k.device_id = m.device_id
				AND k.is_consumed = false AND k.deleted_at IS NULL
			ORDER BY k.created_at LIMIT 1
		) k ON true`).
		Where("m.mls_group_id = ? AND m.deleted_at IS NULL", groupID).
		Scan(&entities).Error
	if err != nil {
		return nil, err
	}
	var packages []MlsKeyPackage
	for i := range entities {
		packages = append(packages, mlsKeyPackageFromEntity(&entities[i]))
	}
	return packages, nil
}

// --- MLS group states ---

// GetMlsGroupStateByGroupID loads the group state, returning (nil, nil) when
// absent (FirstOrDefaultAsync semantics).
func (s *Store) GetMlsGroupStateByGroupID(ctx context.Context, groupID string) (*MlsGroupState, error) {
	var entities []MLSGroupStateEntity
	if err := s.DB.WithContext(ctx).
		Where("mls_group_id = ?", groupID).
		Order("created_at").Limit(1).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	state := mlsGroupStateFromEntity(&entities[0])
	return &state, nil
}

// BootstrapMlsGroup creates the group state when absent inside a SERIALIZABLE
// transaction; replaying a bootstrap returns the existing state unchanged
// (mirrors BootstrapMlsGroupAsync).
func (s *Store) BootstrapMlsGroup(ctx context.Context, accountID, groupID string, epoch, stateVersion int64, meta map[string]any, now time.Time) (*MlsGroupState, error) {
	var state *MlsGroupState
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var entities []MLSGroupStateEntity
		if err := tx.
			Where("mls_group_id = ?", groupID).
			Order("created_at").Limit(1).Find(&entities).Error; err != nil {
			return err
		}
		if len(entities) > 0 {
			existing := mlsGroupStateFromEntity(&entities[0])
			state = &existing
			return nil
		}
		created, err := insertMlsGroupState(tx, groupID, epoch, stateVersion, []byte{}, []byte{}, meta, now)
		if err != nil {
			return err
		}
		state = created
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	return state, nil
}

// insertMlsGroupState writes a fresh mls_group_states row (group_info and
// ratchet_tree are NOT NULL).
func insertMlsGroupState(tx *gorm.DB, groupID string, epoch, stateVersion int64, groupInfo, ratchetTree []byte, meta map[string]any, now time.Time) (*MlsGroupState, error) {
	entity := &MLSGroupStateEntity{
		ID:           uuid.New(),
		EntityBase:   EntityBase{CreatedAt: now, UpdatedAt: now},
		MLSGroupID:   groupID,
		Epoch:        epoch,
		StateVersion: stateVersion,
		LastCommitAt: &now,
		GroupInfo:    groupInfo,
		RatchetTree:  ratchetTree,
		Meta:         jsonMapPtr(meta),
	}
	if err := tx.Create(entity).Error; err != nil {
		return nil, err
	}
	state := mlsGroupStateFromEntity(entity)
	return &state, nil
}

// UpdateMlsGroupState persists a mutated group state row.
func (s *Store) UpdateMlsGroupState(ctx context.Context, state *MlsGroupState) error {
	// The raw statement matched on id alone: soft-deleted rows are updated too.
	return s.DB.WithContext(ctx).Unscoped().Model(&MLSGroupStateEntity{}).
		Where("id = ?", state.Id).
		Updates(map[string]any{
			"epoch":          state.Epoch,
			"state_version":  state.StateVersion,
			"last_commit_at": state.LastCommitAt,
			"group_info":     state.GroupInfo,
			"ratchet_tree":   state.RatchetTree,
			"meta":           jsonMapPtr(state.Meta),
			"updated_at":     state.UpdatedAt,
		}).Error
}

// CreateMlsGroup inserts a fresh group state (mirrors CreateMlsGroupAsync,
// used by the group reset flow).
func (s *Store) CreateMlsGroup(ctx context.Context, groupID string, epoch, stateVersion int64, now time.Time) (*MlsGroupState, error) {
	return insertMlsGroupState(s.DB.WithContext(ctx), groupID, epoch, stateVersion, []byte{}, []byte{}, nil, now)
}

// UploadGroupInfo writes the GroupInfo/RatchetTree for the expected epoch
// inside a SERIALIZABLE transaction (mirrors UploadGroupInfoAsync).
func (s *Store) UploadGroupInfo(ctx context.Context, groupID string, groupInfo, ratchetTree []byte, expectedEpoch *int64, now time.Time) (*UploadGroupInfoResult, error) {
	var result *UploadGroupInfoResult
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var entities []MLSGroupStateEntity
		if err := tx.
			Where("mls_group_id = ?", groupID).
			Order("created_at").Limit(1).Find(&entities).Error; err != nil {
			return err
		}
		if len(entities) == 0 { // no state row
			if expectedEpoch != nil {
				result = &UploadGroupInfoResult{Success: false, GroupID: groupID, Epoch: -1}
				return nil
			}
			state, err := insertMlsGroupState(tx, groupID, 0, 0, groupInfo, ratchetTree, nil, now)
			if err != nil {
				return err
			}
			result = &UploadGroupInfoResult{Success: true, GroupID: state.MlsGroupId, Epoch: 0}
			return nil
		}
		state := mlsGroupStateFromEntity(&entities[0])
		if expectedEpoch != nil && state.Epoch != *expectedEpoch {
			result = &UploadGroupInfoResult{Success: false, GroupID: state.MlsGroupId, Epoch: state.Epoch}
			return nil
		}
		if err := tx.Model(&MLSGroupStateEntity{}).
			Where("id = ?", state.Id).
			Updates(map[string]any{
				"group_info":   groupInfo,
				"ratchet_tree": ratchetTree,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		result = &UploadGroupInfoResult{Success: true, GroupID: state.MlsGroupId, Epoch: state.Epoch}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// DeleteMlsGroup soft-deletes the group states and member device rows of a
// group (EF RemoveRange + the soft-delete save interceptor), returning the
// number of deleted states.
func (s *Store) DeleteMlsGroup(ctx context.Context, groupID string, now time.Time) (int64, error) {
	// The soft-delete save interceptor stamped updated_at as well, so the
	// deleted_at column is written explicitly.
	states := s.DB.WithContext(ctx).Model(&MLSGroupStateEntity{}).
		Where("mls_group_id = ? AND deleted_at IS NULL", groupID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now})
	if states.Error != nil {
		return 0, states.Error
	}
	if err := s.DB.WithContext(ctx).Model(&MLSDeviceMembershipEntity{}).
		Where("mls_group_id = ? AND deleted_at IS NULL", groupID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
		return 0, err
	}
	return states.RowsAffected, nil
}

// --- MLS device memberships ---

// ListMlsMembershipsByGroup lists the member devices of a group (excludes
// soft-deleted rows).
func (s *Store) ListMlsMembershipsByGroup(ctx context.Context, groupID string) ([]MlsDeviceMembership, error) {
	var entities []MLSDeviceMembershipEntity
	if err := s.DB.WithContext(ctx).
		Where("mls_group_id = ?", groupID).Find(&entities).Error; err != nil {
		return nil, err
	}
	var memberships []MlsDeviceMembership
	for i := range entities {
		memberships = append(memberships, mlsDeviceMembershipFromEntity(&entities[i]))
	}
	return memberships, nil
}

// ListMlsGroupMemberAccountIDs returns the distinct member account ids of a
// group (group reset notification).
func (s *Store) ListMlsGroupMemberAccountIDs(ctx context.Context, groupID string) ([]string, error) {
	var entities []MLSDeviceMembershipEntity
	if err := s.DB.WithContext(ctx).
		Select("account_id").
		Where("mls_group_id = ?", groupID).
		Distinct().Find(&entities).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []string
	for i := range entities {
		id := entities[i].AccountID.String()
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// IsMlsGroupMember reports whether (account, device) is a member of the group.
func (s *Store) IsMlsGroupMember(ctx context.Context, accountID, deviceID, groupID string) (bool, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&MLSDeviceMembershipEntity{}).
		Where("mls_group_id = ? AND account_id = ? AND device_id = ?", groupID, accountID, deviceID).
		Limit(1).Count(&count).Error
	return count > 0, err
}

// MarkMlsReshareRequired creates or updates the membership with
// last_reshare_required_at set (mirrors MarkMlsReshareRequiredAsync).
func (s *Store) MarkMlsReshareRequired(ctx context.Context, groupID, accountID, deviceID string, epoch int64, now time.Time) (*MlsDeviceMembership, error) {
	var entities []MLSDeviceMembershipEntity
	if err := s.DB.WithContext(ctx).
		Where("mls_group_id = ? AND account_id = ? AND device_id = ?", groupID, accountID, deviceID).
		Limit(1).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 { // create
		accountUUID, err := uuidValue(accountID)
		if err != nil {
			return nil, err
		}
		lastSeen := epoch
		entity := &MLSDeviceMembershipEntity{
			ID:                    uuid.New(),
			EntityBase:            EntityBase{CreatedAt: now, UpdatedAt: now},
			MLSGroupID:            groupID,
			AccountID:             accountUUID,
			DeviceID:              deviceID,
			JoinedEpoch:           epoch,
			LastSeenEpoch:         &lastSeen,
			LastReshareRequiredAt: &now,
		}
		if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
			return nil, err
		}
		membership := mlsDeviceMembershipFromEntity(entity)
		return &membership, nil
	}
	membership := mlsDeviceMembershipFromEntity(&entities[0])
	lastSeen := epoch
	if err := s.DB.WithContext(ctx).Model(&MLSDeviceMembershipEntity{}).
		Where("id = ?", membership.Id).
		Updates(map[string]any{
			"last_seen_epoch":          epoch,
			"last_reshare_required_at": now,
			"updated_at":               now,
		}).Error; err != nil {
		return nil, err
	}
	membership.LastSeenEpoch = &lastSeen
	membership.LastReshareRequiredAt = &now
	membership.UpdatedAt = now
	return &membership, nil
}

// UpsertMlsDeviceMembership creates or revives the membership row. Unlike the
// other membership queries this includes soft-deleted rows (the C# uses
// IgnoreQueryFilters) and clears the reshare markers (mirrors
// AddMlsDeviceMembershipAsync).
func (s *Store) UpsertMlsDeviceMembership(ctx context.Context, groupID, accountID, deviceID string, epoch int64, now time.Time) (*MlsDeviceMembership, error) {
	var entities []MLSDeviceMembershipEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Where("mls_group_id = ? AND account_id = ? AND device_id = ?", groupID, accountID, deviceID).
		Limit(1).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		accountUUID, err := uuidValue(accountID)
		if err != nil {
			return nil, err
		}
		lastSeen := epoch
		entity := &MLSDeviceMembershipEntity{
			ID:            uuid.New(),
			EntityBase:    EntityBase{CreatedAt: now, UpdatedAt: now},
			MLSGroupID:    groupID,
			AccountID:     accountUUID,
			DeviceID:      deviceID,
			JoinedEpoch:   epoch,
			LastSeenEpoch: &lastSeen,
		}
		if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
			return nil, err
		}
		membership := mlsDeviceMembershipFromEntity(entity)
		return &membership, nil
	}
	membership := mlsDeviceMembershipFromEntity(&entities[0])
	lastSeen := epoch
	// Unscoped: the raw statement matched soft-deleted rows and revived them.
	if err := s.DB.WithContext(ctx).Unscoped().Model(&MLSDeviceMembershipEntity{}).
		Where("id = ?", membership.Id).
		Updates(map[string]any{
			"deleted_at":                nil,
			"last_seen_epoch":           epoch,
			"last_reshare_required_at":  nil,
			"last_reshare_completed_at": nil,
			"updated_at":                now,
		}).Error; err != nil {
		return nil, err
	}
	membership.DeletedAt = nil
	membership.LastSeenEpoch = &lastSeen
	membership.LastReshareRequiredAt = nil
	membership.LastReshareCompletedAt = nil
	membership.UpdatedAt = now
	return &membership, nil
}

// ListDeviceReshareStatus lists the pending reshare memberships of a device
// (last_reshare_required_at set, last_reshare_completed_at null).
func (s *Store) ListDeviceReshareStatus(ctx context.Context, accountID, deviceID string) ([]MlsDeviceMembership, error) {
	var entities []MLSDeviceMembershipEntity
	if err := s.DB.WithContext(ctx).
		Where(`account_id = ? AND device_id = ?
			AND last_reshare_required_at IS NOT NULL AND last_reshare_completed_at IS NULL`,
			accountID, deviceID).Find(&entities).Error; err != nil {
		return nil, err
	}
	var memberships []MlsDeviceMembership
	for i := range entities {
		memberships = append(memberships, mlsDeviceMembershipFromEntity(&entities[i]))
	}
	return memberships, nil
}

// CompleteMlsReshare sets last_reshare_completed_at on the membership.
func (s *Store) CompleteMlsReshare(ctx context.Context, accountID, deviceID, groupID string, now time.Time) (bool, error) {
	res := s.DB.WithContext(ctx).Model(&MLSDeviceMembershipEntity{}).
		Where("account_id = ? AND device_id = ? AND mls_group_id = ?", accountID, deviceID, groupID).
		Updates(map[string]any{"last_reshare_completed_at": now, "updated_at": now})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// MarkAllDevicesReshareRequired flags every group member device for reshare
// (group reset), returning the number of memberships updated.
func (s *Store) MarkAllDevicesReshareRequired(ctx context.Context, groupID string, now time.Time) (int64, error) {
	res := s.DB.WithContext(ctx).Model(&MLSDeviceMembershipEntity{}).
		Where("mls_group_id = ?", groupID).
		Updates(map[string]any{
			"last_reshare_required_at":  now,
			"last_reshare_completed_at": nil,
			"updated_at":                now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// --- E2EE sessions / envelopes ---

// AccountExists reports whether the account row exists.
func (s *Store) AccountExists(ctx context.Context, accountID string) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&AccountEntity{}).
		Where("id = ?", accountID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// TouchE2eeSession bumps the session's last_message_at (fanout with a session
// id).
func (s *Store) TouchE2eeSession(ctx context.Context, sessionID string, now time.Time) error {
	return s.DB.WithContext(ctx).Model(&E2EESessionEntity{}).
		Where("id = ?", sessionID).
		Updates(map[string]any{"last_message_at": now, "updated_at": now}).Error
}

// InsertEnvelope stores an envelope deduplicating on client_message_id and
// assigning the next monotonic sequence per (recipient_account_id,
// recipient_device_id), mirroring CreateEnvelopeForTargetAsync. When a
// duplicate client_message_id exists the existing envelope is returned and
// nothing is inserted.
func (s *Store) InsertEnvelope(ctx context.Context, env *E2eeEnvelope) (*E2eeEnvelope, error) {
	if env.ClientMessageId != nil && *env.ClientMessageId != "" {
		var entities []E2EEEnvelopeEntity
		if err := s.DB.WithContext(ctx).
			Where(`sender_id = ? AND sender_device_id IS NOT DISTINCT FROM ?
				AND recipient_account_id = ? AND recipient_device_id IS NOT DISTINCT FROM ?
				AND client_message_id = ?`,
				env.SenderId, env.SenderDeviceId, env.RecipientAccountId, env.RecipientDeviceId, env.ClientMessageId).
			Limit(1).Find(&entities).Error; err != nil {
			return nil, err
		}
		if len(entities) > 0 {
			existing := e2eeEnvelopeFromEntity(&entities[0])
			return &existing, nil
		}
	}
	var seq int64
	// Unscoped: the raw statement did not filter soft-deleted rows.
	if err := s.DB.WithContext(ctx).Unscoped().Model(&E2EEEnvelopeEntity{}).
		Select("COALESCE(MAX(sequence),0)+1").
		Where("recipient_account_id = ? AND recipient_device_id IS NOT DISTINCT FROM ?",
			env.RecipientAccountId, env.RecipientDeviceId).
		Scan(&seq).Error; err != nil {
		return nil, err
	}
	env.Sequence = seq
	entity, err := e2eeEnvelopeEntity(env)
	if err != nil {
		return nil, err
	}
	// The INSERT pinned the envelope to Pending and stamped created_at for both
	// timestamps.
	entity.Sequence = seq
	entity.DeliveryStatus = 0
	entity.DeliveredAt = nil
	entity.AckedAt = nil
	entity.UpdatedAt = env.CreatedAt
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	return env, nil
}

// GetPendingEnvelopesByDevice returns the undelivered envelopes of a device
// (delivery_status != Acknowledged, unexpired) in sequence order, marking
// pending rows Delivered (mirrors GetPendingEnvelopesByDeviceAsync). Returns
// an empty slice when the device is not active.
func (s *Store) GetPendingEnvelopesByDevice(ctx context.Context, accountID, deviceID string, take int, now time.Time) ([]E2eeEnvelope, error) {
	var active int64
	if err := s.DB.WithContext(ctx).Model(&E2EEDeviceEntity{}).
		Where("account_id = ? AND device_id = ? AND is_revoked = false", accountID, deviceID).
		Limit(1).Count(&active).Error; err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, nil
	}

	var entities []E2EEEnvelopeEntity
	// Unscoped: the raw statement did not filter soft-deleted rows.
	if err := s.DB.WithContext(ctx).Unscoped().
		Where(`recipient_account_id = ? AND recipient_device_id = ?
			AND delivery_status <> 2 AND (expires_at IS NULL OR expires_at > ?)`,
			accountID, deviceID, now).
		Order("sequence").Limit(take).Find(&entities).Error; err != nil {
		return nil, err
	}
	var envelopes []E2eeEnvelope
	var pendingIDs []string
	for i := range entities {
		envelope := e2eeEnvelopeFromEntity(&entities[i])
		if envelope.DeliveryStatus == 0 { // Pending
			pendingIDs = append(pendingIDs, envelope.Id)
			envelope.DeliveryStatus = 1 // Delivered
			envelope.DeliveredAt = &now
		}
		envelopes = append(envelopes, envelope)
	}
	if len(pendingIDs) > 0 {
		// Unscoped: the raw statement matched on id alone.
		if err := s.DB.WithContext(ctx).Unscoped().Model(&E2EEEnvelopeEntity{}).
			Where("id IN ?", pendingIDs).
			Updates(map[string]any{"delivery_status": 1, "delivered_at": now, "updated_at": now}).Error; err != nil {
			return nil, err
		}
	}
	return envelopes, nil
}

// AckEnvelopeByDevice acknowledges a device envelope, returning nil when the
// device is inactive or the envelope is missing (both map to the C# null
// result).
func (s *Store) AckEnvelopeByDevice(ctx context.Context, accountID, deviceID, envelopeID string, now time.Time) (*E2eeEnvelope, error) {
	var active int64
	if err := s.DB.WithContext(ctx).Model(&E2EEDeviceEntity{}).
		Where("account_id = ? AND device_id = ? AND is_revoked = false", accountID, deviceID).
		Limit(1).Count(&active).Error; err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, nil
	}
	var entities []E2EEEnvelopeEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND recipient_account_id = ? AND recipient_device_id = ?", envelopeID, accountID, deviceID).
		Limit(1).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	envelope := e2eeEnvelopeFromEntity(&entities[0])
	if err := s.DB.WithContext(ctx).Model(&E2EEEnvelopeEntity{}).
		Where("id = ?", envelope.Id).
		Updates(map[string]any{"delivery_status": 2, "acked_at": now, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	envelope.DeliveryStatus = 2
	envelope.AckedAt = &now
	envelope.UpdatedAt = now
	return &envelope, nil
}

// MarkEnvelopeDelivered flips a pushed envelope to Delivered.
func (s *Store) MarkEnvelopeDelivered(ctx context.Context, envelopeID string, now time.Time) error {
	// Unscoped: the raw statement matched on id alone.
	return s.DB.WithContext(ctx).Unscoped().Model(&E2EEEnvelopeEntity{}).
		Where("id = ?", envelopeID).
		Updates(map[string]any{"delivery_status": 1, "delivered_at": now, "updated_at": now}).Error
}

// RevokeDevice marks the device revoked, purges its unacknowledged pending
// envelopes, and inserts a control envelope for every active sibling device
// inside a single transaction (mirrors RevokeDeviceAsync, whose implicit EF
// SaveChanges transaction wraps the same steps).
func (s *Store) RevokeDevice(ctx context.Context, accountID, deviceID string, now time.Time) (*RevokeDeviceResult, error) {
	result := &RevokeDeviceResult{}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var deviceEntities []E2EEDeviceEntity
		if err := tx.
			Where("account_id = ? AND device_id = ?", accountID, deviceID).
			Limit(1).Find(&deviceEntities).Error; err != nil {
			return err
		}
		if len(deviceEntities) == 0 {
			return nil
		}
		device := e2eeDeviceFromEntity(&deviceEntities[0])
		result.Found = true
		if device.IsRevoked {
			result.AlreadyRevoked = true
			return nil
		}
		if err := tx.Model(&E2EEDeviceEntity{}).
			Where("id = ?", device.Id).
			Updates(map[string]any{"is_revoked": true, "revoked_at": now, "updated_at": now}).Error; err != nil {
			return err
		}

		// Purge pending envelopes for the revoked device (hard delete).
		purged := tx.Unscoped().
			Where("recipient_account_id = ? AND recipient_device_id = ? AND delivery_status <> 2", accountID, deviceID).
			Delete(&E2EEEnvelopeEntity{})
		if purged.Error != nil {
			return purged.Error
		}
		result.PurgedCount = int(purged.RowsAffected)

		// Control envelopes for sibling devices.
		var siblingEntities []E2EEDeviceEntity
		if err := tx.
			Select("device_id").
			Where("account_id = ? AND is_revoked = false AND device_id <> ?", accountID, deviceID).
			Find(&siblingEntities).Error; err != nil {
			return err
		}

		for i := range siblingEntities {
			targetDeviceID := siblingEntities[i].DeviceID
			clientMessageID := fmt.Sprintf("mls-revoke-%s-%d-%s", deviceID, now.UnixMilli(), targetDeviceID)
			var seq int64
			// Unscoped: the raw statement did not filter soft-deleted rows.
			if err := tx.Unscoped().Model(&E2EEEnvelopeEntity{}).
				Select("COALESCE(MAX(sequence),0)+1").
				Where("recipient_account_id = ? AND recipient_device_id IS NOT DISTINCT FROM ?", accountID, targetDeviceID).
				Scan(&seq).Error; err != nil {
				return err
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
			entity, err := e2eeEnvelopeEntity(&control)
			if err != nil {
				return err
			}
			if err := tx.Create(entity).Error; err != nil {
				return err
			}
			result.ControlEnvelopes = append(result.ControlEnvelopes, control)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// --- entity -> model mappers ---

func e2eeDeviceFromEntity(entity *E2EEDeviceEntity) E2eeDevice {
	device := E2eeDevice{
		Id:           entity.ID.String(),
		AccountId:    entity.AccountID.String(),
		DeviceId:     entity.DeviceID,
		DeviceLabel:  entity.DeviceLabel,
		IsRevoked:    entity.IsRevoked,
		LastBundleAt: entity.LastBundleAt,
		RevokedAt:    entity.RevokedAt,
		CreatedAt:    entity.CreatedAt,
		UpdatedAt:    entity.UpdatedAt,
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		device.DeletedAt = &deleted
	}
	return device
}

func e2eeKeyBundleFromEntity(entity *E2EEKeyBundleEntity) E2eeKeyBundle {
	bundle := E2eeKeyBundle{
		Id:                    entity.ID.String(),
		AccountId:             entity.AccountID.String(),
		DeviceId:              entity.DeviceID,
		Algorithm:             entity.Algorithm,
		IdentityKey:           entity.IdentityKey,
		SignedPreKeyId:        entity.SignedPreKeyID,
		SignedPreKey:          entity.SignedPreKey,
		SignedPreKeySignature: entity.SignedPreKeySignature,
		SignedPreKeyExpiresAt: entity.SignedPreKeyExpiresAt,
		Meta:                  jsonMapBytes(entity.Meta),
		CreatedAt:             entity.CreatedAt,
		UpdatedAt:             entity.UpdatedAt,
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		bundle.DeletedAt = &deleted
	}
	return bundle
}

func e2eeOneTimePreKeyFromEntity(entity *E2EEOneTimePreKeyEntity) E2eeOneTimePreKey {
	preKey := E2eeOneTimePreKey{
		Id:                 entity.ID.String(),
		KeyBundleId:        entity.KeyBundleID.String(),
		AccountId:          entity.AccountID.String(),
		DeviceId:           entity.DeviceID,
		KeyId:              entity.KeyID,
		PublicKey:          entity.PublicKey,
		IsClaimed:          entity.IsClaimed,
		ClaimedAt:          entity.ClaimedAt,
		ClaimedByAccountId: uuidStr(entity.ClaimedByAccountID),
		CreatedAt:          entity.CreatedAt,
		UpdatedAt:          entity.UpdatedAt,
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		preKey.DeletedAt = &deleted
	}
	return preKey
}

func e2eeEnvelopeFromEntity(entity *E2EEEnvelopeEntity) E2eeEnvelope {
	envelope := E2eeEnvelope{
		Id:                  entity.ID.String(),
		SenderId:            entity.SenderID.String(),
		SenderDeviceId:      entity.SenderDeviceID,
		RecipientId:         entity.RecipientID.String(),
		RecipientAccountId:  entity.RecipientAccountID.String(),
		RecipientDeviceId:   entity.RecipientDeviceID,
		SessionId:           uuidStr(entity.SessionID),
		Type:                entity.Type,
		GroupId:             entity.GroupID,
		ClientMessageId:     entity.ClientMessageID,
		Sequence:            entity.Sequence,
		Ciphertext:          entity.Ciphertext,
		Header:              entity.Header,
		Signature:           entity.Signature,
		DeliveryStatus:      entity.DeliveryStatus,
		DeliveredAt:         entity.DeliveredAt,
		AckedAt:             entity.AckedAt,
		ExpiresAt:           entity.ExpiresAt,
		LegacyAccountScoped: entity.LegacyAccountScoped,
		Meta:                jsonMapBytes(entity.Meta),
		CreatedAt:           entity.CreatedAt,
		UpdatedAt:           entity.UpdatedAt,
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		envelope.DeletedAt = &deleted
	}
	return envelope
}

func mlsKeyPackageFromEntity(entity *MLSKeyPackageEntity) MlsKeyPackage {
	pkg := MlsKeyPackage{
		Id:                  entity.ID.String(),
		AccountId:           entity.AccountID.String(),
		DeviceId:            entity.DeviceID,
		DeviceLabel:         entity.DeviceLabel,
		KeyPackage:          entity.KeyPackage,
		Ciphersuite:         entity.Ciphersuite,
		IsConsumed:          entity.IsConsumed,
		ConsumedAt:          entity.ConsumedAt,
		ConsumedByAccountId: uuidStr(entity.ConsumedByAccountID),
		Meta:                jsonMapBytes(entity.Meta),
		CreatedAt:           entity.CreatedAt,
		UpdatedAt:           entity.UpdatedAt,
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		pkg.DeletedAt = &deleted
	}
	return pkg
}

func mlsGroupStateFromEntity(entity *MLSGroupStateEntity) MlsGroupState {
	state := MlsGroupState{
		Id:           entity.ID.String(),
		MlsGroupId:   entity.MLSGroupID,
		Epoch:        entity.Epoch,
		StateVersion: entity.StateVersion,
		LastCommitAt: entity.LastCommitAt,
		GroupInfo:    entity.GroupInfo,
		RatchetTree:  entity.RatchetTree,
		Meta:         jsonMapBytes(entity.Meta),
		CreatedAt:    entity.CreatedAt,
		UpdatedAt:    entity.UpdatedAt,
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		state.DeletedAt = &deleted
	}
	return state
}

func mlsDeviceMembershipFromEntity(entity *MLSDeviceMembershipEntity) MlsDeviceMembership {
	membership := MlsDeviceMembership{
		Id:                     entity.ID.String(),
		MlsGroupId:             entity.MLSGroupID,
		AccountId:              entity.AccountID.String(),
		DeviceId:               entity.DeviceID,
		JoinedEpoch:            entity.JoinedEpoch,
		LastSeenEpoch:          entity.LastSeenEpoch,
		LastReshareRequiredAt:  entity.LastReshareRequiredAt,
		LastReshareCompletedAt: entity.LastReshareCompletedAt,
		CreatedAt:              entity.CreatedAt,
		UpdatedAt:              entity.UpdatedAt,
	}
	if entity.DeletedAt.Valid {
		deleted := entity.DeletedAt.Time
		membership.DeletedAt = &deleted
	}
	return membership
}

// --- model -> entity builders (create paths) ---

// mlsKeyPackageEntity builds a publishable row. IsConsumed/consumed_* stay at
// their defaults and created_at stamps updated_at too, matching the previous
// INSERT (kp.UpdatedAt was not persisted).
func mlsKeyPackageEntity(kp *MlsKeyPackage) (*MLSKeyPackageEntity, error) {
	id, err := uuidValue(kp.Id)
	if err != nil {
		return nil, err
	}
	accountUUID, err := uuidValue(kp.AccountId)
	if err != nil {
		return nil, err
	}
	return &MLSKeyPackageEntity{
		ID:          id,
		EntityBase:  EntityBase{CreatedAt: kp.CreatedAt, UpdatedAt: kp.CreatedAt},
		AccountID:   accountUUID,
		DeviceID:    kp.DeviceId,
		DeviceLabel: kp.DeviceLabel,
		KeyPackage:  kp.KeyPackage,
		Ciphersuite: kp.Ciphersuite,
		IsConsumed:  false,
		Meta:        jsonMapPtr(kp.Meta),
	}, nil
}

// e2eeEnvelopeEntity builds an envelope row. Soft-delete/consumed defaults stay
// at their zero values; callers pin the persisted delivery state explicitly.
func e2eeEnvelopeEntity(env *E2eeEnvelope) (*E2EEEnvelopeEntity, error) {
	id, err := uuidValue(env.Id)
	if err != nil {
		return nil, err
	}
	senderID, err := uuidValue(env.SenderId)
	if err != nil {
		return nil, err
	}
	recipientID, err := uuidValue(env.RecipientId)
	if err != nil {
		return nil, err
	}
	recipientAccountID, err := uuidValue(env.RecipientAccountId)
	if err != nil {
		return nil, err
	}
	sessionID, err := uuidPtrOrNil(env.SessionId)
	if err != nil {
		return nil, err
	}
	return &E2EEEnvelopeEntity{
		ID:                  id,
		EntityBase:          EntityBase{CreatedAt: env.CreatedAt, UpdatedAt: env.UpdatedAt},
		SenderID:            senderID,
		SenderDeviceID:      env.SenderDeviceId,
		RecipientID:         recipientID,
		RecipientAccountID:  recipientAccountID,
		RecipientDeviceID:   env.RecipientDeviceId,
		SessionID:           sessionID,
		Type:                env.Type,
		GroupID:             env.GroupId,
		ClientMessageID:     env.ClientMessageId,
		Sequence:            env.Sequence,
		Ciphertext:          env.Ciphertext,
		Header:              env.Header,
		Signature:           env.Signature,
		DeliveryStatus:      env.DeliveryStatus,
		DeliveredAt:         env.DeliveredAt,
		AckedAt:             env.AckedAt,
		ExpiresAt:           env.ExpiresAt,
		LegacyAccountScoped: env.LegacyAccountScoped,
		Meta:                jsonMapPtr(env.Meta),
	}, nil
}

// --- json helpers ---

// jsonMapPtr converts a nullable meta map into a nullable jsonb value: nil maps
// stay SQL NULL and unmarshalable maps degrade to SQL NULL, mirroring the
// previous jsonBytes helper.
func jsonMapPtr(value map[string]any) *datatypes.JSON {
	if value == nil {
		return nil
	}
	encoded, err := encodeJSON(value)
	if err != nil {
		return nil
	}
	return &encoded
}

// jsonMapBytes decodes a nullable jsonb column into a meta map, mirroring the
// previous parseJSONMap helper (SQL NULL, JSON null and malformed JSON all
// yield nil).
func jsonMapBytes(raw *datatypes.JSON) map[string]any {
	if raw == nil || len(*raw) == 0 || string(*raw) == "null" {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(*raw, &value); err != nil {
		return nil
	}
	return value
}

// --- uuid helpers ---

func uuidValue(value string) (uuid.UUID, error) { return uuid.Parse(value) }

// uuidPtrOrNil parses an optional uuid string; nil or empty means SQL NULL.
func uuidPtrOrNil(value *string) (*uuid.UUID, error) {
	if value == nil {
		return nil, nil
	}
	if *value == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(*value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func uuidStr(value *uuid.UUID) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return &text
}

// bytesOrEmpty keeps the NOT NULL bytea columns of e2ee_key_bundles writable
// when a caller supplies no key material: the C# model used non-nullable
// byte[] fields, so an absent key was persisted as an empty array (the previous
// raw SQL wrote NULL and always tripped the constraint).
func bytesOrEmpty(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func strPtr(s string) *string { return &s }
