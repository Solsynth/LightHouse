package rewind

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
)

// SchemaVersion is the document layout version Passport emitted.
const SchemaVersion = 1

// ErrPointNotFound mirrors the C# InvalidOperationException message for a
// missing (account, year) point.
var ErrPointNotFound = errors.New("No rewind point was found.")

// AccountLookup hydrates the account + profile included in the response.
type AccountLookup interface {
	GetAccountWithProfile(ctx context.Context, id uuid.UUID) (*model.Account, error)
}

// Point is the SnRewindPoint wire shape.
type Point struct {
	ID            string         `json:"id"`
	Year          int            `json:"year"`
	SchemaVersion int            `json:"schema_version"`
	SharableCode  *string        `json:"sharable_code"`
	Data          map[string]any `json:"data"`
	AccountID     string         `json:"account_id"`
	Account       *model.Account `json:"account"`
	CreatedAt     *model.Time    `json:"created_at"`
	UpdatedAt     *model.Time    `json:"updated_at"`
	DeletedAt     *model.Time    `json:"deleted_at"`
}

// Service owns the rewind table and the optional downstream clients.
type Service struct {
	DB       *gorm.DB
	Accounts AccountLookup
	Sources  *Sources
	Log      *slog.Logger

	missingCheckInTableOnce sync.Once
}

// NewService builds the service.
func NewService(db *gorm.DB, accounts AccountLookup, sources *Sources, log *slog.Logger) *Service {
	return &Service{DB: db, Accounts: accounts, Sources: sources, Log: log}
}

// GetPublic loads a point by its sharable code (nil when the code is unknown).
func (s *Service) GetPublic(ctx context.Context, code string) (*Point, error) {
	var entity RewindPointEntity
	err := s.DB.WithContext(ctx).
		Where("sharable_code = ?", code).
		Order("created_at ASC").
		First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	account := s.lookupAccount(ctx, entity.AccountID)
	return pointFromEntity(&entity, account), nil
}

// GetOrCreate returns the current year's point, creating it on first access.
func (s *Service) GetOrCreate(ctx context.Context, accountID uuid.UUID) (*Point, error) {
	year := time.Now().UTC().Year()
	entity, err := s.findByAccountYear(ctx, accountID, year)
	if err != nil {
		return nil, err
	}
	if entity != nil {
		return pointFromEntity(entity, s.lookupAccount(ctx, accountID)), nil
	}
	account, err := s.requireAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}

	data, err := s.buildData(ctx, account, accountID, year)
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	entity = &RewindPointEntity{
		ID:            uuid.New(),
		Year:          year,
		SchemaVersion: SchemaVersion,
		Data:          datatypes.JSON(encoded),
		AccountID:     accountID,
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	return pointFromEntity(entity, account), nil
}

// SetPublic assigns a fresh random 16-character sharable code.
func (s *Service) SetPublic(ctx context.Context, accountID uuid.UUID, year int) (*Point, error) {
	entity, err := s.findByAccountYear(ctx, accountID, year)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		return nil, ErrPointNotFound
	}
	for range 5 {
		code, err := randomCode(16)
		if err != nil {
			return nil, err
		}
		err = s.DB.WithContext(ctx).Model(entity).Update("sharable_code", code).Error
		if err == nil {
			entity.SharableCode = &code
			s.reload(ctx, entity)
			return pointFromEntity(entity, s.lookupAccount(ctx, accountID)), nil
		}
		if !isUniqueViolation(err) {
			return nil, err
		}
	}
	return nil, errors.New("rewind: could not allocate a unique sharable code")
}

// SetPrivate clears the sharable code (the public URL stops resolving).
func (s *Service) SetPrivate(ctx context.Context, accountID uuid.UUID, year int) (*Point, error) {
	entity, err := s.findByAccountYear(ctx, accountID, year)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		return nil, ErrPointNotFound
	}
	if err := s.DB.WithContext(ctx).Model(entity).Update("sharable_code", nil).Error; err != nil {
		return nil, err
	}
	entity.SharableCode = nil
	s.reload(ctx, entity)
	return pointFromEntity(entity, s.lookupAccount(ctx, accountID)), nil
}

// buildData assembles the per-service document: the locally computed
// passport stats plus one map per configured downstream rewind service.
func (s *Service) buildData(ctx context.Context, account *model.Account, accountID uuid.UUID, year int) (map[string]map[string]any, error) {
	data := map[string]map[string]any{}
	if s.Sources != nil {
		downstream, err := s.Sources.Events(ctx, accountID.String(), year)
		if err != nil {
			return nil, err
		}
		for service, payload := range downstream {
			if service == "passport" {
				continue
			}
			if payload == nil {
				payload = map[string]any{}
			}
			data[service] = payload
		}
	}
	passport, err := s.passportStats(ctx, account, accountID, year)
	if err != nil {
		return nil, err
	}
	data["passport"] = passport
	return data, nil
}

func (s *Service) findByAccountYear(ctx context.Context, accountID uuid.UUID, year int) (*RewindPointEntity, error) {
	var entity RewindPointEntity
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND year = ?", accountID, year).
		Order("created_at ASC").
		First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entity, nil
}

// reload refreshes the timestamps GORM set in the database (the toggles need
// updated_at for the response; a reload also keeps a concurrent write honest).
func (s *Service) reload(ctx context.Context, entity *RewindPointEntity) {
	if err := s.DB.WithContext(ctx).First(entity, "id = ?", entity.ID).Error; err != nil {
		s.log().Warn("rewind point reload failed", "id", entity.ID.String(), "error", err)
	}
}

func (s *Service) requireAccount(ctx context.Context, accountID uuid.UUID) (*model.Account, error) {
	if s.Accounts == nil {
		return nil, nil
	}
	return s.Accounts.GetAccountWithProfile(ctx, accountID)
}

// lookupAccount hydrates best-effort: a public point stays readable even when
// the account row is gone or the profile lookup fails.
func (s *Service) lookupAccount(ctx context.Context, accountID uuid.UUID) *model.Account {
	if s.Accounts == nil {
		return nil
	}
	account, err := s.Accounts.GetAccountWithProfile(ctx, accountID)
	if err != nil {
		s.log().Warn("rewind account hydration failed", "account_id", accountID.String(), "error", err)
		return nil
	}
	return account
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func pointFromEntity(entity *RewindPointEntity, account *model.Account) *Point {
	data := map[string]any{}
	if len(entity.Data) > 0 {
		if err := json.Unmarshal(entity.Data, &data); err != nil || data == nil {
			data = map[string]any{}
		}
	}
	return &Point{
		ID:            entity.ID.String(),
		Year:          entity.Year,
		SchemaVersion: entity.SchemaVersion,
		SharableCode:  entity.SharableCode,
		Data:          data,
		AccountID:     entity.AccountID.String(),
		Account:       account,
		CreatedAt:     timeOrNil(entity.CreatedAt),
		UpdatedAt:     timeOrNil(entity.UpdatedAt),
		DeletedAt:     deletedTime(entity.DeletedAt),
	}
}

func timeOrNil(value time.Time) *model.Time {
	if value.IsZero() {
		return nil
	}
	return model.NewTime(value)
}

func deletedTime(value gorm.DeletedAt) *model.Time {
	if !value.Valid {
		return nil
	}
	return model.NewTime(value.Time)
}

// randomCode produces an unbiased alphanumeric code (same alphabet as the C#
// implementation, without its modulo bias).
func randomCode(length int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	const limit = 256 - (256 % len(alphabet))
	result := make([]byte, length)
	cursor := 0
	var buffer [32]byte
	for cursor < length {
		if _, err := rand.Read(buffer[:]); err != nil {
			return "", err
		}
		for _, value := range buffer {
			if int(value) >= limit {
				continue
			}
			result[cursor] = alphabet[int(value)%len(alphabet)]
			cursor++
			if cursor == length {
				break
			}
		}
	}
	return string(result), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
