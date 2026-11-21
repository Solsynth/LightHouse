// Historical backfill: plays pushed before the play-to-scrobble projection
// existed live only in library_plays. This walks every account that has any and
// projects the plays onto the scrobble history with the same thresholds, ids
// and catalog links the live push path uses.
package librarysync

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/config"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// BackfillPlayScrobbles projects each account's historical plays onto the
// scrobble history once. The per-account marker row commits in the same
// transaction as the projected scrobbles, so a crash retries the whole account
// and a completed account is never revisited (user deletions stay deleted).
// It returns how many scrobbles it created; no events are published, this is
// history repair rather than live activity.
func BackfillPlayScrobbles(ctx context.Context, db *gorm.DB, cfg config.Scrobbler) (int64, error) {
	var accounts []uuid.UUID
	if err := db.WithContext(ctx).Model(&model.LibrarySyncPlay{}).
		Distinct("account_id").Pluck("account_id", &accounts).Error; err != nil {
		return 0, err
	}

	var created int64
	for _, accountID := range accounts {
		marked := model.LibrarySyncScrobbleBackfill{}
		err := db.WithContext(ctx).Where("account_id = ?", accountID).Take(&marked).Error
		if err == nil {
			continue
		}
		if !isNotFound(err) {
			return created, err
		}

		var batch int64
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var plays []model.LibrarySyncPlay
			if err := tx.Where("account_id = ?", accountID).Order("started_at").Find(&plays).Error; err != nil {
				return err
			}
			for _, play := range plays {
				scrobble, err := projectPlayScrobble(tx, accountID, cfg, play.TrackFingerprint, play.StartedAt, play.ListenedMs, play.Completed)
				if err != nil {
					return err
				}
				if scrobble != nil {
					batch++
				}
			}
			return tx.Create(&model.LibrarySyncScrobbleBackfill{
				AccountID:   accountID,
				CompletedAt: time.Now().UTC(),
			}).Error
		})
		if err != nil {
			return created, err
		}
		created += batch
	}
	return created, nil
}
