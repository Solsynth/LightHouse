package analytics

import (
	"context"
	"strings"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// defaultSummaryDays is the window used when the caller does not pick one.
const defaultSummaryDays = 30

// maxSummaryDays bounds the scan a single summary request may perform.
const maxSummaryDays = 365

// summary aggregates the events of one resource. Unique visitors are counted
// per event name, preferring the account, then the visitor cookie, then the
// hashed address for clients without either.
func (h *handler) summary(ctx context.Context, kind, resourceID string, days int) (*model.AnalyticsSummary, error) {
	kind = strings.TrimSpace(kind)
	resourceID = strings.TrimSpace(resourceID)
	if h.db == nil {
		return nil, errInvalidEvent
	}
	if kind == "" || resourceID == "" {
		return nil, errInvalidEvent
	}
	if days <= 0 {
		days = defaultSummaryDays
	}
	if days > maxSummaryDays {
		days = maxSummaryDays
	}
	since := time.Now().UTC().AddDate(0, 0, -days)
	summary := &model.AnalyticsSummary{
		Kind:       kind,
		ResourceID: resourceID,
		WindowDays: days,
		Events:     []model.AnalyticsEventSummary{},
		Daily:      []model.AnalyticsDailyPoint{},
	}

	type eventRow struct {
		Event          string
		Total          int64
		Humans         int64
		Bots           int64
		UniqueVisitors int64
	}
	var rows []eventRow
	err := h.db.WithContext(ctx).Model(&store.AnalyticsEventEntity{}).
		Select(`event,
			COUNT(*) AS total,
			SUM(CASE WHEN client_class = ? THEN 1 ELSE 0 END) AS humans,
			SUM(CASE WHEN client_class = ? THEN 1 ELSE 0 END) AS bots,
			COUNT(DISTINCT COALESCE(CAST(account_id AS TEXT), NULLIF(visitor_id, ''), NULLIF(ip_hash, ''))) AS unique_visitors`,
			model.AnalyticsClientHuman, model.AnalyticsClientBot).
		Where("kind = ? AND resource_id = ? AND created_at >= ?", kind, resourceID, since).
		Group("event").
		Order("total DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		summary.Events = append(summary.Events, model.AnalyticsEventSummary{
			Event:          row.Event,
			Total:          row.Total,
			Humans:         row.Humans,
			Bots:           row.Bots,
			UniqueVisitors: row.UniqueVisitors,
		})
	}

	dayExpr := `to_char(date_trunc('day', created_at), 'YYYY-MM-DD')`
	if h.db.Dialector.Name() == "sqlite" {
		dayExpr = `strftime('%Y-%m-%d', created_at)`
	}
	type dailyRow struct {
		Day    string
		Event  string
		Total  int64
		Humans int64
		Bots   int64
	}
	var daily []dailyRow
	err = h.db.WithContext(ctx).Model(&store.AnalyticsEventEntity{}).
		Select(dayExpr+` AS day, event,
			COUNT(*) AS total,
			SUM(CASE WHEN client_class = ? THEN 1 ELSE 0 END) AS humans,
			SUM(CASE WHEN client_class = ? THEN 1 ELSE 0 END) AS bots`,
			model.AnalyticsClientHuman, model.AnalyticsClientBot).
		Where("kind = ? AND resource_id = ? AND created_at >= ?", kind, resourceID, since).
		Group("day, event").
		Order("day, event").
		Scan(&daily).Error
	if err != nil {
		return nil, err
	}
	for _, row := range daily {
		summary.Daily = append(summary.Daily, model.AnalyticsDailyPoint{
			Date:   row.Day,
			Event:  row.Event,
			Total:  row.Total,
			Humans: row.Humans,
			Bots:   row.Bots,
		})
	}
	return summary, nil
}
