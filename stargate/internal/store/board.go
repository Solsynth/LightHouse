package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Board helpers for the Passport-moved board surface (account_board_items).

const boardItemColumns = `id, "order", kind, widget_key, custom_app_id, custom_app_widget_key,
	is_enabled, payload, account_id, created_at, updated_at, deleted_at`

// ListBoardItems returns the account's board items ordered by position
// (mirrors AccountBoardService.GetBoardAsync).
func (s *Store) ListBoardItems(ctx context.Context, accountID uuid.UUID) ([]model.BoardItem, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+boardItemColumns+` FROM account_board_items
		WHERE account_id = $1 AND deleted_at IS NULL ORDER BY "order"`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.BoardItem
	for rows.Next() {
		item, err := scanBoardItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

// ReplaceBoardItems deletes all board rows for the account and inserts the
// given items in one transaction (mirrors ReplaceBoardAsync's ExecuteDelete +
// AddRange). Items are returned sorted by order.
func (s *Store) ReplaceBoardItems(ctx context.Context, accountID uuid.UUID, items []model.BoardItem) ([]model.BoardItem, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM account_board_items WHERE account_id = $1`, accountID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	for i := range items {
		items[i].AccountId = accountID.String()
		payload, err := json.Marshal(items[i].Payload)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_board_items
			(id, account_id, "order", kind, widget_key, custom_app_id, custom_app_widget_key,
			 is_enabled, payload, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
			items[i].Id, accountID, items[i].Order, items[i].Kind,
			items[i].WidgetKey, items[i].CustomAppId, items[i].CustomAppWidgetKey,
			items[i].IsEnabled, payload, now); err != nil {
			return nil, err
		}
		items[i].CreatedAt = model.NewTime(now)
		items[i].UpdatedAt = model.NewTime(now)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	return items, nil
}

func scanBoardItem(row pgx.Row) (*model.BoardItem, error) {
	item := &model.BoardItem{}
	var (
		widgetKey, customAppID, customAppWidgetKey, accountID *string
		payload                                               []byte
	)
	err := row.Scan(&item.Id, &item.Order, &item.Kind, &widgetKey, &customAppID, &customAppWidgetKey,
		&item.IsEnabled, &payload, &accountID, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	item.WidgetKey = widgetKey
	item.CustomAppId = customAppID
	item.CustomAppWidgetKey = customAppWidgetKey
	if accountID != nil {
		item.AccountId = *accountID
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &item.Payload)
	}
	return item, nil
}
