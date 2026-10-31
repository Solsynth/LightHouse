package passkit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/store"
)

// ErrAccountNotFound reports a pass request for a missing account.
var ErrAccountNotFound = errors.New("account was not found")

const maxProfilePictureBytes = 8 << 20

// Deps is the service dependency set.
type Deps struct {
	Store    *store.Store
	Settings Settings
	SiteURL  string
	Perks    ports.Perks
	Files    ports.Files
	Log      *slog.Logger
}

// Service owns Apple Wallet pass records and .pkpass generation.
type Service struct {
	store    *store.Store
	settings Settings
	siteURL  string
	perks    ports.Perks
	files    ports.Files
	log      *slog.Logger
	client   *http.Client
}

// NewService builds the PassKit domain service.
func NewService(deps Deps) *Service {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	siteURL := deps.SiteURL
	if deps.Settings.SiteURL != nil && strings.TrimSpace(*deps.Settings.SiteURL) != "" {
		siteURL = *deps.Settings.SiteURL
	}
	return &Service{
		store:    deps.Store,
		settings: deps.Settings,
		siteURL:  strings.TrimRight(strings.TrimSpace(siteURL), "/"),
		perks:    deps.Perks,
		files:    deps.Files,
		log:      log,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Available reports whether the service can serve reads.
func (s *Service) Available() bool { return s != nil && s.store != nil && s.store.DB != nil }

// GetOrCreatePass returns the account's pass record for the configured pass
// type, creating it with a fresh authentication token when absent.
func (s *Service) GetOrCreatePass(ctx context.Context, accountID uuid.UUID) (*PassEntity, error) {
	var pass PassEntity
	err := s.store.DB.WithContext(ctx).
		Where("account_id = ? AND pass_type_identifier = ?", accountID, s.settings.PassTypeIdentifier).
		First(&pass).Error
	if err == nil {
		return &pass, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	token, err := newAuthenticationToken()
	if err != nil {
		return nil, err
	}
	created := &PassEntity{
		ID:                  uuid.New(),
		AccountID:           accountID,
		PassTypeIdentifier:  s.settings.PassTypeIdentifier,
		SerialNumber:        accountID.String(),
		AuthenticationToken: token,
		LastUpdatedTag:      "",
	}
	result := s.store.DB.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// A concurrent request won the insert; read its row.
		if err := s.store.DB.WithContext(ctx).
			Where("account_id = ? AND pass_type_identifier = ?", accountID, s.settings.PassTypeIdentifier).
			First(&pass).Error; err != nil {
			return nil, err
		}
		return &pass, nil
	}
	return created, nil
}

// GetPassBySerial loads a pass by its type + serial pair.
func (s *Service) GetPassBySerial(ctx context.Context, passTypeIdentifier, serialNumber string) (*PassEntity, error) {
	var pass PassEntity
	err := s.store.DB.WithContext(ctx).
		Where("pass_type_identifier = ? AND serial_number = ?", passTypeIdentifier, serialNumber).
		First(&pass).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pass, nil
}

// IsAuthorized checks the `ApplePass <token>` authorization header.
func (s *Service) IsAuthorized(pass *PassEntity, authorizationHeader string) bool {
	if pass == nil {
		return false
	}
	const scheme = "ApplePass "
	header := strings.TrimSpace(authorizationHeader)
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return false
	}
	token := strings.TrimSpace(header[len(scheme):])
	if len(token) != len(pass.AuthenticationToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(pass.AuthenticationToken)) == 1
}

// RegisterDevice registers or refreshes a device push registration.
func (s *Service) RegisterDevice(ctx context.Context, pass *PassEntity, deviceLibraryIdentifier, pushToken string) error {
	var registration RegistrationEntity
	err := s.store.DB.WithContext(ctx).
		Where("pass_id = ? AND device_library_identifier = ?", pass.ID, deviceLibraryIdentifier).
		First(&registration).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		registration = RegistrationEntity{
			ID:                      uuid.New(),
			PassID:                  pass.ID,
			DeviceLibraryIdentifier: deviceLibraryIdentifier,
			PushToken:               pushToken,
		}
		return s.store.DB.WithContext(ctx).Create(&registration).Error
	case err != nil:
		return err
	}
	if registration.PushToken == pushToken {
		return nil
	}
	return s.store.DB.WithContext(ctx).Model(&RegistrationEntity{}).
		Where("id = ?", registration.ID).
		Update("push_token", pushToken).Error
}

// UnregisterDevice hard-deletes a device registration, mirroring Passport's
// DbSet.Remove.
func (s *Service) UnregisterDevice(ctx context.Context, pass *PassEntity, deviceLibraryIdentifier string) (bool, error) {
	result := s.store.DB.WithContext(ctx).Unscoped().
		Where("pass_id = ? AND device_library_identifier = ?", pass.ID, deviceLibraryIdentifier).
		Delete(&RegistrationEntity{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// GetUpdatedSerialNumbers lists the pass serials a device has registered that
// changed after passesUpdatedSince.
func (s *Service) GetUpdatedSerialNumbers(ctx context.Context, deviceLibraryIdentifier, passTypeIdentifier, passesUpdatedSince string) ([]string, string, error) {
	query := s.store.DB.WithContext(ctx).Model(&PassEntity{}).
		Joins("JOIN apple_pass_registrations ON apple_pass_registrations.pass_id = apple_passes.id AND apple_pass_registrations.deleted_at IS NULL").
		Where("apple_pass_registrations.device_library_identifier = ?", deviceLibraryIdentifier).
		Where("apple_passes.pass_type_identifier = ?", passTypeIdentifier)
	if strings.TrimSpace(passesUpdatedSince) != "" {
		query = query.Where("apple_passes.last_updated_tag > ?", passesUpdatedSince)
	}
	var passes []PassEntity
	if err := query.Order("apple_passes.last_updated_tag").Find(&passes).Error; err != nil {
		return nil, "", err
	}
	serials := make([]string, 0, len(passes))
	for i := range passes {
		serials = append(serials, passes[i].SerialNumber)
	}
	lastUpdated := passesUpdatedSince
	if len(passes) > 0 {
		lastUpdated = passes[len(passes)-1].LastUpdatedTag
	}
	if lastUpdated == "" {
		lastUpdated = passesUpdatedSince
	}
	return serials, lastUpdated, nil
}

// BuildLastUpdatedTag hashes the account state clients can see on the pass,
// mirroring ApplePassService.BuildLastUpdatedTag (SHA-256 over NodaTime
// 100-nanosecond ticks and identity fields).
func (s *Service) BuildLastUpdatedTag(account *model.Account) string {
	profileUpdated := "0"
	if account.Profile != nil {
		profileUpdated = unixTicks(account.Profile.UpdatedAt)
	}
	identifier := ""
	level := "0"
	if account.PerkSubscription != nil {
		identifier = account.PerkSubscription.Identifier
		level = strconv.Itoa(account.PerkSubscription.PerkLevel)
	}
	raw := strings.Join([]string{
		unixTicks(account.UpdatedAt),
		profileUpdated,
		identifier,
		level,
		account.Name,
		account.Nick,
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// GenerateMemberPass builds a signed .pkpass for the account and refreshes
// the stored last-updated tag.
func (s *Service) GenerateMemberPass(ctx context.Context, accountID uuid.UUID) ([]byte, *PassEntity, error) {
	if missing := s.settings.MissingRequired(); len(missing) > 0 {
		return nil, nil, &ConfigError{Missing: missing}
	}

	account, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, ErrAccountNotFound
		}
		return nil, nil, err
	}
	if account.Profile == nil {
		profile, err := s.store.GetOrCreateAccountProfile(ctx, accountID)
		if err != nil {
			return nil, nil, err
		}
		account.Profile = profile
	}
	s.hydratePerk(ctx, account)

	pass, err := s.GetOrCreatePass(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if tag := s.BuildLastUpdatedTag(account); pass.LastUpdatedTag != tag {
		if err := s.store.DB.WithContext(ctx).Model(&PassEntity{}).
			Where("id = ?", pass.ID).
			Update("last_updated_tag", tag).Error; err != nil {
			return nil, nil, err
		}
		pass.LastUpdatedTag = tag
	}

	images, err := s.collectImages(ctx, account)
	if err != nil {
		return nil, nil, err
	}

	document := buildPassDocument(s.settings, pass, account, s.profileURL(account))
	passJSON, err := json.Marshal(document)
	if err != nil {
		return nil, nil, err
	}
	files := make(map[string][]byte, len(images)+1)
	for name, data := range images {
		files[name] = data
	}
	files["pass.json"] = passJSON

	material, err := loadSigningMaterial(s.settings.SigningCertificatePath, s.settings.SigningCertificatePassword, s.settings.AppleWwdrCertificatePath)
	if err != nil {
		return nil, nil, &ConfigError{Detail: err.Error()}
	}
	archive, err := buildPassArchive(files, material)
	if err != nil {
		return nil, nil, err
	}
	return archive, pass, nil
}

func (s *Service) hydratePerk(ctx context.Context, account *model.Account) {
	if s.perks == nil || !s.perks.Available() {
		return
	}
	subscription, err := s.perks.Subscription(ctx, account.Id)
	if err != nil {
		s.log.Warn("passkit: hydrate perk subscription", "account", account.Id, "error", err)
		account.PerkSubscription = nil
		account.PerkLevel = 0
		return
	}
	if subscription == nil {
		account.PerkSubscription = nil
		account.PerkLevel = 0
		return
	}
	account.PerkSubscription = subscription
	account.PerkLevel = subscription.PerkLevel
}

func (s *Service) profileURL(account *model.Account) string {
	return s.siteURL + "/accounts/" + account.Name
}

// collectImages mirrors ApplePassService.AddImagesAsync: the profile picture
// wins for icon/thumbnail when available, configured asset paths are the
// fallback, and the three icon sizes are mandatory.
func (s *Service) collectImages(ctx context.Context, account *model.Account) (map[string][]byte, error) {
	picture := s.fetchProfilePicture(ctx, account)
	images := make(map[string][]byte, 9)

	required := []struct {
		name string
		path *string
	}{
		{"icon.png", s.settings.IconPath},
		{"icon@2x.png", s.settings.Icon2XPath},
		{"icon@3x.png", s.settings.Icon3XPath},
	}
	for _, asset := range required {
		data, err := assetBytes(picture, asset.path, asset.name)
		if err != nil {
			return nil, err
		}
		images[asset.name] = data
	}

	optional := []struct {
		name     string
		path     *string
		fallback bool
	}{
		{"logo.png", s.settings.LogoPath, false},
		{"logo@2x.png", s.settings.Logo2XPath, false},
		{"logo@3x.png", s.settings.Logo3XPath, false},
		{"thumbnail.png", s.settings.ThumbnailPath, true},
		{"thumbnail@2x.png", s.settings.Thumbnail2XPath, true},
		{"thumbnail@3x.png", s.settings.Thumbnail3XPath, true},
	}
	for _, asset := range optional {
		var data []byte
		if asset.fallback && len(picture) > 0 {
			data = picture
		} else if asset.path != nil && strings.TrimSpace(*asset.path) != "" {
			loaded, err := readImageAsset(*asset.path)
			if err != nil {
				return nil, err
			}
			data = loaded
		}
		if len(data) > 0 {
			images[asset.name] = data
		}
	}
	return images, nil
}

func assetBytes(preferred []byte, path *string, name string) ([]byte, error) {
	if len(preferred) > 0 {
		return preferred, nil
	}
	if path == nil || strings.TrimSpace(*path) == "" {
		return nil, &ConfigError{Missing: []string{fmt.Sprintf("an asset for %s", name)}}
	}
	return readImageAsset(*path)
}

func readImageAsset(path string) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, &ConfigError{Detail: fmt.Sprintf("resolve Apple Wallet asset %q: %v", path, err)}
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, &ConfigError{Detail: fmt.Sprintf("read Apple Wallet asset %q: %v", absolute, err)}
	}
	return data, nil
}

// fetchProfilePicture resolves the profile picture URL (local file reference
// first, then the files port) and downloads it. Failures degrade to nil so a
// configured asset path can take over.
func (s *Service) fetchProfilePicture(ctx context.Context, account *model.Account) []byte {
	if account.Profile == nil || account.Profile.Picture == nil {
		return nil
	}
	picture := account.Profile.Picture
	imageURL := strings.TrimSpace(picture.Url)
	if imageURL == "" && picture.Id != "" && s.files != nil && s.files.Available() {
		reference, err := s.files.Resolve(ctx, picture.Id)
		if err != nil {
			s.log.Warn("passkit: resolve profile picture", "account", account.Id, "error", err)
			return nil
		}
		if reference != nil {
			imageURL = strings.TrimSpace(reference.Url)
		}
	}
	if imageURL == "" {
		return nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		s.log.Warn("passkit: build profile picture request", "account", account.Id, "error", err)
		return nil
	}
	response, err := s.client.Do(request)
	if err != nil {
		s.log.Warn("passkit: fetch profile picture", "account", account.Id, "error", err)
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.log.Warn("passkit: fetch profile picture", "account", account.Id, "status", response.StatusCode)
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxProfilePictureBytes+1))
	if err != nil {
		s.log.Warn("passkit: read profile picture", "account", account.Id, "error", err)
		return nil
	}
	if len(data) > maxProfilePictureBytes {
		s.log.Warn("passkit: profile picture exceeds size limit", "account", account.Id)
		return nil
	}
	return data
}

func newAuthenticationToken() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate pass authentication token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func unixTicks(value *model.Time) string {
	if value == nil {
		return "0"
	}
	instant := value.Time()
	return strconv.FormatInt(instant.Unix()*10_000_000+int64(instant.Nanosecond())/100, 10)
}
