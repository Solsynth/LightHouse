package model

// Client classes recorded with every analytics event. Unknown covers user
// agents the classifier could not place; empty user agents count as bots.
const (
	AnalyticsClientUnknown = 0
	AnalyticsClientHuman   = 1
	AnalyticsClientBot     = 2
)

// AnalyticsEventSummary aggregates one event name over a window.
type AnalyticsEventSummary struct {
	Event          string `json:"event"`
	Total          int64  `json:"total"`
	Humans         int64  `json:"humans"`
	Bots           int64  `json:"bots"`
	UniqueVisitors int64  `json:"unique_visitors"`
}

// AnalyticsDailyPoint aggregates one event name on one UTC day.
type AnalyticsDailyPoint struct {
	Date   string `json:"date"`
	Event  string `json:"event"`
	Total  int64  `json:"total"`
	Humans int64  `json:"humans"`
	Bots   int64  `json:"bots"`
}

// AnalyticsSummary is the per-resource analytics payload.
type AnalyticsSummary struct {
	Kind       string                  `json:"kind"`
	ResourceID string                  `json:"resource_id"`
	WindowDays int                     `json:"window_days"`
	Events     []AnalyticsEventSummary `json:"events"`
	Daily      []AnalyticsDailyPoint   `json:"daily"`
}
