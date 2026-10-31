package presenceartworks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
)

// HashPrefix is the content-address prefix stored on every row and reference.
const HashPrefix = "sha256:"

var (
	// ErrArtworkStreamNotFound reports a metadata row whose object vanished
	// from the bucket.
	ErrArtworkStreamNotFound = errors.New("presenceartworks: artwork stream not found")
	// ErrStorageUnavailable reports an unconfigured S3 section.
	ErrStorageUnavailable = errors.New("presenceartworks: storage is not configured")
)

// InvalidInputError is an upload/read validation failure. The C# controller
// mapped InvalidOperationException to 400; every other error stays a 500.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return e.Message }

func invalidf(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// mimeToExtension is the C# MimeToExtension table.
var mimeToExtension = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
	"image/avif":    ".avif",
}

// IsArtworkReference reports whether a profile field points at an artwork
// hash (mirrors PresenceArtworkService.IsArtworkReference).
func IsArtworkReference(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), HashPrefix)
}

// NormalizeHash accepts a bare SHA-256 hex digest or a `sha256:` reference and
// returns the canonical lower-case form.
func NormalizeHash(hash string) (string, error) {
	value := strings.TrimSpace(hash)
	if !strings.HasPrefix(strings.ToLower(value), HashPrefix) {
		value = HashPrefix + value
	}
	digest := strings.ToLower(value[len(HashPrefix):])
	if len(digest) != 64 {
		return "", invalidf("Invalid artwork hash.")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", invalidf("Invalid artwork hash.")
	}
	return HashPrefix + digest, nil
}

// Service owns the artwork table and the bucket.
type Service struct {
	DB       *gorm.DB
	Storage  *Storage
	Settings Settings
	Log      *slog.Logger
}

// NewService builds the service. An incomplete S3 section leaves Storage nil
// (the routes answer 503); an invalid endpoint is a configuration error.
func NewService(db *gorm.DB, settings Settings, log *slog.Logger) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	storage, err := NewStorage(settings)
	if err != nil {
		return nil, err
	}
	return &Service{DB: db, Storage: storage, Settings: settings, Log: log}, nil
}

// SaveArtwork validates and stores one upload. The second return value is
// false when the content already existed (the metadata row was touched).
func (s *Service) SaveArtwork(ctx context.Context, fileName, mimeType string, data []byte) (*PresenceArtworkEntity, bool, error) {
	if len(data) == 0 {
		return nil, false, invalidf("Artwork file cannot be empty.")
	}
	if int64(len(data)) > s.Settings.MaxFileSizeBytes {
		return nil, false, invalidf("Artwork file is too large. Maximum allowed is %d bytes.", s.Settings.MaxFileSizeBytes)
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" || !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return nil, false, invalidf("Only image files are supported for presence artwork.")
	}
	if s.Storage == nil {
		return nil, false, ErrStorageUnavailable
	}

	sum := sha256.Sum256(data)
	hash := HashPrefix + hex.EncodeToString(sum[:])
	now := time.Now().UTC()

	var existing PresenceArtworkEntity
	err := s.DB.WithContext(ctx).Where("hash = ?", hash).First(&existing).Error
	if err == nil {
		if err := s.DB.WithContext(ctx).Model(&existing).
			Update("last_referenced_at", now).Error; err != nil {
			return nil, false, err
		}
		existing.LastReferencedAt = now
		return &existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	extension := resolveExtension(mimeType, fileName)
	objectKey := s.objectKey(hash, extension)
	if err := s.Storage.Put(ctx, objectKey, data, mimeType); err != nil {
		return nil, false, err
	}

	artwork := &PresenceArtworkEntity{
		Hash:             hash,
		MimeType:         mimeType,
		Size:             int64(len(data)),
		StoragePath:      objectKey,
		LastReferencedAt: now,
	}
	if err := s.DB.WithContext(ctx).Create(artwork).Error; err != nil {
		// The object is unreachable without its row; clean it up like the C#
		// service did.
		if deleteErr := s.Storage.Delete(ctx, objectKey); deleteErr != nil {
			s.Log.Warn("presence artwork object cleanup failed", "key", objectKey, "error", deleteErr)
		}
		return nil, false, err
	}
	return artwork, true, nil
}

// GetArtwork loads one metadata row by hash. A missing row returns (nil, nil).
func (s *Service) GetArtwork(ctx context.Context, hash string) (*PresenceArtworkEntity, error) {
	normalized, err := NormalizeHash(hash)
	if err != nil {
		return nil, err
	}
	var artwork PresenceArtworkEntity
	if err := s.DB.WithContext(ctx).Where("hash = ?", normalized).First(&artwork).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &artwork, nil
}

// OpenArtwork streams the stored object. The HTTP layer serves it through
// http.ServeContent so Range requests work.
func (s *Service) OpenArtwork(ctx context.Context, artwork *PresenceArtworkEntity) (io.ReadSeekCloser, error) {
	if s.Storage == nil {
		return nil, ErrStorageUnavailable
	}
	return s.Storage.Open(ctx, artwork.StoragePath)
}

// CleanupExpiredArtworks removes up to 200 rows/objects whose
// last_referenced_at is older than the retention window. It is idempotent:
// a missing object is treated as already deleted and the row is dropped.
func (s *Service) CleanupExpiredArtworks(ctx context.Context, now time.Time) (int, error) {
	if s.Storage == nil {
		return 0, ErrStorageUnavailable
	}
	retention := s.Settings.RetentionDays
	if retention < 1 {
		retention = DefaultRetentionDays
	}
	threshold := now.UTC().AddDate(0, 0, -retention)

	var expired []PresenceArtworkEntity
	if err := s.DB.WithContext(ctx).
		Where("last_referenced_at < ?", threshold).
		Order("last_referenced_at ASC").
		Limit(200).
		Find(&expired).Error; err != nil {
		return 0, err
	}
	if len(expired) == 0 {
		return 0, nil
	}

	for i := range expired {
		if err := s.Storage.Delete(ctx, expired[i].StoragePath); err != nil {
			return 0, err
		}
	}
	hashes := make([]string, 0, len(expired))
	for i := range expired {
		hashes = append(hashes, expired[i].Hash)
	}
	// C# used RemoveRange: a hard delete, not a soft-delete timestamp.
	if err := s.DB.WithContext(ctx).Unscoped().
		Delete(&PresenceArtworkEntity{}, "hash IN ?", hashes).Error; err != nil {
		return 0, err
	}
	return len(expired), nil
}

// StartCleanupJob runs the retention sweep on a ticker (hourly by default,
// replacing Passport's Quartz cron "0 20 * * * ?") and returns a stop
// function. The first sweep runs after one interval.
func StartCleanupJob(ctx context.Context, service *Service, interval time.Duration, log *slog.Logger) func() {
	if service == nil || service.Storage == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = time.Hour
	}
	if log == nil {
		log = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				removed, err := service.CleanupExpiredArtworks(runCtx, time.Now().UTC())
				if err != nil {
					log.Warn("presence artwork cleanup failed", "error", err)
					continue
				}
				if removed > 0 {
					log.Info("presence artwork cleanup removed expired resources", "count", removed)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *Service) objectKey(hash, extension string) string {
	prefix := strings.Trim(s.Settings.KeyPrefix, "/")
	digest := hash[len(HashPrefix):]
	if prefix == "" {
		return digest[:2] + "/" + digest + extension
	}
	return prefix + "/" + digest[:2] + "/" + digest + extension
}

// resolveExtension maps the allow-listed MIME types to their canonical
// extension and falls back to the uploaded file's extension, then ".bin".
func resolveExtension(mimeType, fileName string) string {
	if extension, ok := mimeToExtension[strings.ToLower(strings.TrimSpace(mimeType))]; ok {
		return extension
	}
	fallback := filepath.Ext(fileName)
	if fallback == "" || len(fallback) > 10 {
		return ".bin"
	}
	return strings.ToLower(fallback)
}
