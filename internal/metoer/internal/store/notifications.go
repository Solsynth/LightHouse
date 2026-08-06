package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/go/pkg/models"
)

// appFilterClause mirrors PushService.ApplyNotificationAppFilter /
// ApplySubscriptionAppFilter: with the resolved app id equal to the default
// app, rows with a NULL or empty app id also match. Returns "" when no
// filter applies. The value placeholder is $argNo.
func appFilterClause(column string, resolvedAppID, defaultAppID *string, argNo int) (string, []any) {
	if resolvedAppID == nil || *resolvedAppID == "" {
		return "", nil
	}
	if defaultAppID != nil && *resolvedAppID == *defaultAppID {
		return fmt.Sprintf("(%s = $%d OR %s IS NULL OR %s = '')", column, argNo, column, column), []any{*resolvedAppID}
	}
	return fmt.Sprintf("%s = $%d", column, argNo), []any{*resolvedAppID}
}

// CountUnreadNotifications counts unread (viewed_at IS NULL), non-deleted
// notifications for an account, with the app filter applied.
func (s *Store) CountUnreadNotifications(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string) (int, error) {
	clause, args := appFilterClause("app_id", resolvedAppID, defaultAppID, 2)
	query := `SELECT count(*) FROM notifications WHERE account_id = $1 AND viewed_at IS NULL AND deleted_at IS NULL`
	if clause != "" {
		query += " AND " + clause
	}
	var count int
	if err := s.pool.QueryRow(ctx, query, append([]any{accountID}, args...)...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ListNotifications returns one page (created_at DESC) plus the total count
// for the same filter (mirrors the C# baseQuery.CountAsync + page fetch).
func (s *Store) ListNotifications(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string, offset, take int) ([]*model.Notification, int, error) {
	clause, appArgs := appFilterClause("app_id", resolvedAppID, defaultAppID, 2)
	baseWhere := `WHERE account_id = $1 AND deleted_at IS NULL`
	if clause != "" {
		baseWhere += " AND " + clause
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications `+baseWhere, append([]any{accountID}, appArgs...)...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append([]any{accountID}, appArgs...)
	pageArgs = append(pageArgs, offset, take)
	query := `SELECT ` + notificationColumns + ` FROM notifications ` + baseWhere +
		` ORDER BY created_at DESC OFFSET $3 LIMIT $4`
	if clause != "" {
		query = `SELECT ` + notificationColumns + ` FROM notifications ` + baseWhere +
			` ORDER BY created_at DESC OFFSET $` + fmt.Sprint(len(appArgs)+2) + ` LIMIT $` + fmt.Sprint(len(appArgs)+3)
	}
	rows, err := s.pool.Query(ctx, query, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []*model.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, n)
	}
	return items, total, rows.Err()
}

// ListNotificationsForSop loads the page used by ListSopNotifications
// (created_at DESC, capped at offset+take+replayCount) plus the total and
// duplicate counts under the app filter. Returns (dbNotifications,
// totalCount, duplicateCount).
func (s *Store) ListNotificationsForSop(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string, offset, take, replayCount int, replayIDs []uuid.UUID) ([]*model.Notification, int, int, error) {
	clause, appArgs := appFilterClause("app_id", resolvedAppID, defaultAppID, 2)
	baseWhere := `WHERE account_id = $1 AND deleted_at IS NULL`
	if clause != "" {
		baseWhere += " AND " + clause
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications `+baseWhere, append([]any{accountID}, appArgs...)...).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	dupCount := 0
	if len(replayIDs) > 0 {
		dupWhere := `WHERE account_id = $1 AND deleted_at IS NULL AND id = ANY($2)`
		if clause != "" {
			dupClause := fmt.Sprintf("(%s = $3 OR %s IS NULL OR %s = '')", "app_id", "app_id", "app_id")
			if !(defaultAppID != nil && len(appArgs) > 0 && resolvedAppID != nil && *resolvedAppID == *defaultAppID) {
				dupClause = "app_id = $3"
			}
			dupWhere += " AND " + dupClause
		}
		dupArgs := []any{accountID, replayIDs}
		if clause != "" {
			dupArgs = append(dupArgs, appArgs[0])
		}
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications `+dupWhere, dupArgs...).Scan(&dupCount); err != nil {
			return nil, 0, 0, err
		}
	}
	fetch := offset + take + replayCount
	pageArgs := append([]any{accountID}, appArgs...)
	pageArgs = append(pageArgs, fetch)
	limitNo := 3
	if clause != "" {
		limitNo = len(appArgs) + 2
	}
	query := `SELECT ` + notificationColumns + ` FROM notifications ` + baseWhere +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(limitNo)
	rows, err := s.pool.Query(ctx, query, pageArgs...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var items []*model.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, 0, 0, err
		}
		items = append(items, n)
	}
	return items, total, dupCount, rows.Err()
}

// MarkNotificationsViewed sets viewed_at for the given ids (only rows still
// unviewed and non-deleted, mirroring ExecuteUpdate under the query filter).
func (s *Store) MarkNotificationsViewed(ctx context.Context, ids []uuid.UUID, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE notifications SET viewed_at = $1, updated_at = $1 WHERE id = ANY($2) AND viewed_at IS NULL AND deleted_at IS NULL`,
		models.Time(now.UTC()), ids)
	return err
}

// MarkAllNotificationsViewed marks every unread notification of the account
// under the app filter.
func (s *Store) MarkAllNotificationsViewed(ctx context.Context, accountID uuid.UUID, resolvedAppID, defaultAppID *string, now time.Time) error {
	clause, appArgs := appFilterClause("app_id", resolvedAppID, defaultAppID, 3)
	query := `UPDATE notifications SET viewed_at = $1, updated_at = $1 WHERE account_id = $2 AND viewed_at IS NULL AND deleted_at IS NULL`
	if clause != "" {
		query += " AND " + clause
	}
	_, err := s.pool.Exec(ctx, query, append([]any{models.Time(now.UTC()), accountID}, appArgs...)...)
	return err
}

// BatchInsertNotifications inserts the given notifications in one batch.
func (s *Store) BatchInsertNotifications(ctx context.Context, notifications []*model.Notification) error {
	if len(notifications) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, n := range notifications {
		meta, err := json.Marshal(n.Meta)
		if err != nil {
			return err
		}
		batch.Queue(`INSERT INTO notifications (id, account_id, app_id, content, created_at, deleted_at, meta, priority, push_type, subtitle, title, topic, updated_at, viewed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			n.Id, n.AccountId, n.AppId, n.Content, n.CreatedAt, n.DeletedAt, meta, n.Priority, n.PushType, n.Subtitle, n.Title, n.Topic, n.UpdatedAt, n.ViewedAt)
	}
	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range notifications {
		if _, err := results.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// SaveNotification inserts a single notification (SaveNotification overload).
func (s *Store) SaveNotification(ctx context.Context, n *model.Notification) error {
	return s.BatchInsertNotifications(ctx, []*model.Notification{n})
}

// DeleteExcessNotifications runs the NotificationRetentionCleanupJob SQL:
// keeps max 100 notifications per (account, app) partition, newest first.
func (s *Store) DeleteExcessNotifications(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM notifications
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY account_id, COALESCE(app_id, '') ORDER BY created_at DESC
				) AS rn
				FROM notifications
			) AS subq
			WHERE subq.rn > 100
		)`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RecycleSoftDeleted hard-deletes rows soft-deleted before threshold, one
// statement per table with a deleted_at column (AppDatabaseRecyclingJob).
func (s *Store) RecycleSoftDeleted(ctx context.Context, threshold time.Time) error {
	tables := []string{
		"notifications",
		"push_subscriptions",
		"notification_preferences",
		"email_sending_plans",
		"email_sending_plan_recipients",
		"email_sending_plan_advances",
		"email_delivery_records",
		"notification_delivery_records",
		"notification_send_records",
	}
	for _, table := range tables {
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM `+table+` WHERE deleted_at IS NOT NULL AND deleted_at < $1`, threshold.UTC()); err != nil {
			return fmt.Errorf("recycle %s: %w", table, err)
		}
	}
	return nil
}
