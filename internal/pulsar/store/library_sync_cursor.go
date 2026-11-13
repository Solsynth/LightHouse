package store

import (
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// NextLibrarySyncRevs reserves n consecutive revisions for one account inside tx and
// returns the first one. The cursor row is created on first use. Callers assign
// base+i to the i-th applied change, so one pull page is always a contiguous
// revision range.
func NextLibrarySyncRevs(tx *gorm.DB, accountID uuid.UUID, n int) (int64, error) {
	if n < 0 {
		n = 0
	}
	cursor := model.LibrarySyncCursor{}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("account_id = ?", accountID).
		Take(&cursor).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		cursor = model.LibrarySyncCursor{AccountID: accountID, Rev: 0}
		if err := tx.Create(&cursor).Error; err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	}
	base := cursor.Rev + 1
	cursor.Rev += int64(n)
	if err := tx.Model(&model.LibrarySyncCursor{}).
		Where("account_id = ?", accountID).
		Update("rev", cursor.Rev).Error; err != nil {
		return 0, err
	}
	return base, nil
}

// CurrentLibrarySyncRev reads the account's cursor without advancing it.
func CurrentLibrarySyncRev(tx *gorm.DB, accountID uuid.UUID) (int64, error) {
	cursor := model.LibrarySyncCursor{}
	err := tx.Where("account_id = ?", accountID).Take(&cursor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return cursor.Rev, nil
}
