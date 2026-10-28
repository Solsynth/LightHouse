package fediverse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// serverKeyData is the JSON document stored at ActivityPub:ServerKeyPath. The
// keys mirror ServerSigningKeyData and its snake_case serializer.
type serverKeyData struct {
	KeyID         string `json:"key_id"`
	PublicKeyPEM  string `json:"public_key_pem"`
	PrivateKeyPEM string `json:"private_key_pem"`
	Algorithm     string `json:"algorithm"`
	CreatedAt     string `json:"created_at"`
}

type keyService struct {
	db      *gorm.DB
	log     *slog.Logger
	path    string
	keyID   string
	mu      sync.Mutex
	actorMu sync.Mutex
	cached  *serverKeyData
}

func newKeyService(db *gorm.DB, log *slog.Logger, path, keyID string) *keyService {
	return &keyService{db: db, log: log, path: path, keyID: keyID}
}

// getServerKey loads the deployment key, generating it on first boot.
func (k *keyService) getServerKey() (*serverKeyData, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cached != nil {
		return k.cached, nil
	}
	if data, err := os.ReadFile(k.path); err == nil {
		var key serverKeyData
		if err := json.Unmarshal(data, &key); err == nil && key.PrivateKeyPEM != "" && key.PublicKeyPEM != "" {
			if key.KeyID == "" {
				key.KeyID = k.keyID
			}
			k.cached = &key
			return k.cached, nil
		}
		k.log.Warn("server signing key file is unreadable; generating a new key", "path", k.path)
	}

	privatePEM, publicPEM, err := generateKeyPair()
	if err != nil {
		return nil, err
	}
	key := &serverKeyData{
		KeyID:         k.keyID,
		PublicKeyPEM:  publicPEM,
		PrivateKeyPEM: privatePEM,
		Algorithm:     "rsa-sha256",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil && filepath.Dir(k.path) != "." {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(k.path, raw, 0o600); err != nil {
		return nil, fmt.Errorf("write server signing key: %w", err)
	}
	k.cached = key
	k.log.Info("generated server signing key", "path", k.path, "key_id", key.KeyID)
	return key, nil
}

func (k *keyService) getKeyForActor(ctx context.Context, actorID uuid.UUID) (*store.FediverseKeyEntity, error) {
	var key store.FediverseKeyEntity
	if err := k.db.WithContext(ctx).Where("publisher_id = ?", actorID).First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

func (k *keyService) getKeyByKeyID(ctx context.Context, keyID string) (*store.FediverseKeyEntity, error) {
	var key store.FediverseKeyEntity
	if err := k.db.WithContext(ctx).Where("key_id = ?", keyID).First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

// getOrCreateActorKey mirrors ActivityPubKeyService.GetOrCreateKeyForActor:
// a row without a private key is repaired in place.
func (k *keyService) getOrCreateActorKey(ctx context.Context, actor *store.PublisherEntity) (*store.FediverseKeyEntity, error) {
	if actor == nil || actor.URI == nil || *actor.URI == "" {
		return nil, errors.New("actor has no ActivityPub URI")
	}
	k.actorMu.Lock()
	defer k.actorMu.Unlock()

	keyID := *actor.URI + "#main-key"
	var existing store.FediverseKeyEntity
	err := k.db.WithContext(ctx).Where("publisher_id = ?", actor.ID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil && existing.PrivateKeyPEM != nil && *existing.PrivateKeyPEM != "" {
		return &existing, nil
	}

	privatePEM, publicPEM, genErr := generateKeyPair()
	if genErr != nil {
		return nil, genErr
	}
	now := time.Now().UTC()
	if err == nil {
		existing.KeyPEM = publicPEM
		existing.PrivateKeyPEM = &privatePEM
		existing.Algorithm = "rsa-sha256"
		existing.RotatedAt = &now
		existing.DeletedAt = gorm.DeletedAt{}
		if saveErr := k.db.WithContext(ctx).Unscoped().Save(&existing).Error; saveErr != nil {
			return nil, saveErr
		}
		return &existing, nil
	}

	created := &store.FediverseKeyEntity{
		ID:            uuid.New(),
		KeyID:         keyID,
		KeyPEM:        publicPEM,
		PrivateKeyPEM: &privatePEM,
		Algorithm:     "rsa-sha256",
		PublisherID:   &actor.ID,
	}
	if createErr := k.db.WithContext(ctx).Create(created).Error; createErr != nil {
		// A concurrent creator may have won; fall back to its row.
		if row, lookupErr := k.getKeyByKeyID(ctx, keyID); lookupErr == nil {
			return row, nil
		}
		return nil, createErr
	}
	k.log.Info("generated actor signing key", "actor_uri", *actor.URI)
	return created, nil
}

// rotateKey replaces an actor's key pair.
func (k *keyService) rotateKey(ctx context.Context, actorID uuid.UUID) error {
	var actor store.PublisherEntity
	if err := k.db.WithContext(ctx).Where("id = ?", actorID).First(&actor).Error; err != nil {
		return err
	}
	k.actorMu.Lock()
	defer k.actorMu.Unlock()

	privatePEM, publicPEM, err := generateKeyPair()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var key store.FediverseKeyEntity
	if err := k.db.WithContext(ctx).Where("publisher_id = ?", actorID).First(&key).Error; err == nil {
		key.KeyPEM = publicPEM
		key.PrivateKeyPEM = &privatePEM
		key.RotatedAt = &now
		key.Algorithm = "rsa-sha256"
		return k.db.WithContext(ctx).Unscoped().Save(&key).Error
	}
	if actor.URI == nil {
		return errors.New("actor has no ActivityPub URI")
	}
	created := &store.FediverseKeyEntity{
		ID:            uuid.New(),
		KeyID:         *actor.URI + "#main-key",
		KeyPEM:        publicPEM,
		PrivateKeyPEM: &privatePEM,
		Algorithm:     "rsa-sha256",
		PublisherID:   &actorID,
	}
	return k.db.WithContext(ctx).Create(created).Error
}

// resolvePublicKey finds the key that signed an inbound request. It checks the
// cached fediverse_keys row, the actor mirror row and finally the keyId URL.
func (k *keyService) resolvePublicKey(ctx context.Context, keyID, actorURI string) (string, error) {
	if row, err := k.getKeyByKeyID(ctx, keyID); err == nil && row.KeyPEM != "" {
		return row.KeyPEM, nil
	}

	var actor store.PublisherEntity
	actorErr := k.db.WithContext(ctx).Unscoped().Where("uri = ?", actorURI).First(&actor).Error
	if actorErr == nil {
		if actor.PublicKey != nil && *actor.PublicKey != "" {
			return *actor.PublicKey, nil
		}
	}

	// The key document is served at the keyId URL (actor or key endpoint).
	publicPEM, err := k.fetchPublicKeyDocument(ctx, keyID)
	if err != nil {
		return "", err
	}
	var publisherID *uuid.UUID
	if actorErr == nil {
		publisherID = &actor.ID
		actor.PublicKey = &publicPEM
		_ = k.db.WithContext(ctx).Unscoped().Model(&actor).Update("public_key", publicPEM).Error
	}
	k.storeRemoteKey(ctx, keyID, publicPEM, publisherID)
	return publicPEM, nil
}

func (k *keyService) fetchPublicKeyDocument(ctx context.Context, keyID string) (string, error) {
	if err := validateOutboundURL(keyID); err != nil {
		return "", err
	}
	req, err := newActivityRequest(ctx, keyID)
	if err != nil {
		return "", err
	}
	client := newSafeClient()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("key document %s returned %s", keyID, resp.Status)
	}
	var document map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&document); err != nil {
		return "", err
	}
	if pem := getString(document, "publicKeyPem"); pem != "" {
		return pem, nil
	}
	if key := getObject(document, "publicKey"); key != nil {
		if pem := getString(key, "publicKeyPem"); pem != "" {
			return pem, nil
		}
	}
	return "", errors.New("key document has no publicKeyPem")
}

// storeRemoteKey caches a remote actor's signing key.
func (k *keyService) storeRemoteKey(ctx context.Context, keyID, publicPEM string, publisherID *uuid.UUID) {
	var existing store.FediverseKeyEntity
	err := k.db.WithContext(ctx).Where("key_id = ?", keyID).First(&existing).Error
	if err == nil {
		existing.KeyPEM = publicPEM
		if publisherID != nil {
			existing.PublisherID = publisherID
		}
		_ = k.db.WithContext(ctx).Unscoped().Save(&existing).Error
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return
	}
	row := &store.FediverseKeyEntity{
		ID:          uuid.New(),
		KeyID:       keyID,
		KeyPEM:      publicPEM,
		Algorithm:   "rsa-sha256",
		PublisherID: publisherID,
	}
	_ = k.db.WithContext(ctx).Create(row).Error
}

// publicKeyForActor returns the actor's public key, creating its pair when the
// actor has never been served before.
func (k *keyService) publicKeyForActor(ctx context.Context, actor *store.PublisherEntity) (string, error) {
	if actor == nil {
		return "", errors.New("actor not found")
	}
	if key, err := k.getKeyForActor(ctx, actor.ID); err == nil && key.KeyPEM != "" {
		return key.KeyPEM, nil
	}
	if actor.PublicKey != nil && *actor.PublicKey != "" {
		return *actor.PublicKey, nil
	}
	key, err := k.getOrCreateActorKey(ctx, actor)
	if err != nil {
		return "", err
	}
	return key.KeyPEM, nil
}

// ---- helpers --------------------------------------------------------------

func newActivityRequest(ctx context.Context, target string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/activity+json, application/ld+json")
	req.Header.Set("User-Agent", "DysonNetwork/1.0 (Lighthouse)")
	return req, nil
}

var errKeyUnavailable = errors.New("fediverse: no signing key available")
