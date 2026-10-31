package accountevents

import (
	"src.solsynth.dev/sosys/stargate/model"
)

// Wire types mirror DysonNetwork.Shared.Models.AccountEvent.cs. JSON is
// snake_case with nulls omitted (the Stargate convention), enums are integers
// and timestamps are model.Time (RFC3339 strings).

// CheckInLevel mirrors CheckInResultLevel.
type CheckInLevel int

const (
	CheckInWorst   CheckInLevel = 0
	CheckInWorse   CheckInLevel = 1
	CheckInNormal  CheckInLevel = 2
	CheckInBetter  CheckInLevel = 3
	CheckInBest    CheckInLevel = 4
	CheckInSpecial CheckInLevel = 5
)

func (l CheckInLevel) String() string {
	switch l {
	case CheckInWorst:
		return "Worst"
	case CheckInWorse:
		return "Worse"
	case CheckInNormal:
		return "Normal"
	case CheckInBetter:
		return "Better"
	case CheckInBest:
		return "Best"
	case CheckInSpecial:
		return "Special"
	default:
		return "Normal"
	}
}

// FortuneTip mirrors CheckInFortuneTip.
type FortuneTip struct {
	IsPositive bool   `json:"is_positive"`
	Title      string `json:"title"`
	Content    string `json:"content"`
}

// FortuneReport mirrors CheckInFortuneReport.
type FortuneReport struct {
	Version        int    `json:"version"`
	Poem           string `json:"poem"`
	Summary        string `json:"summary"`
	SummaryDetail  string `json:"summary_detail"`
	Wish           string `json:"wish"`
	Love           string `json:"love"`
	Study          string `json:"study"`
	Career         string `json:"career"`
	Health         string `json:"health"`
	LostItem       string `json:"lost_item"`
	LuckyColor     string `json:"lucky_color"`
	LuckyDirection string `json:"lucky_direction"`
	LuckyTime      string `json:"lucky_time"`
	LuckyItem      string `json:"lucky_item"`
	LuckyAction    string `json:"lucky_action"`
	AvoidAction    string `json:"avoid_action"`
	Ritual         string `json:"ritual"`
}

// CheckInResult mirrors SnCheckInResult.
type CheckInResult struct {
	Id               string         `json:"id"`
	Level            CheckInLevel   `json:"level"`
	RewardPoints     *float64       `json:"reward_points,omitempty"`
	RewardExperience *int           `json:"reward_experience,omitempty"`
	Tips             []FortuneTip   `json:"tips"`
	FortuneReport    *FortuneReport `json:"fortune_report,omitempty"`
	AccountId        string         `json:"account_id"`
	BackdatedFrom    *model.Time    `json:"backdated_from,omitempty"`
	CreatedAt        *model.Time    `json:"created_at,omitempty"`
	UpdatedAt        *model.Time    `json:"updated_at,omitempty"`
	DeletedAt        *model.Time    `json:"deleted_at,omitempty"`
}

// EventVisibility mirrors EventVisibility.
type EventVisibility int

const (
	VisibilityPrivate EventVisibility = 0
	VisibilityFriends EventVisibility = 100
	VisibilityPublic  EventVisibility = 200
)

// RecurrenceFrequency mirrors RecurrenceFrequency.
type RecurrenceFrequency int

const (
	RecurrenceNone    RecurrenceFrequency = 0
	RecurrenceDaily   RecurrenceFrequency = 1
	RecurrenceWeekly  RecurrenceFrequency = 2
	RecurrenceMonthly RecurrenceFrequency = 3
	RecurrenceYearly  RecurrenceFrequency = 4
)

// RecurrencePattern mirrors RecurrencePattern. DaysOfWeek values are ISO
// weekdays (Monday=1 .. Sunday=7), the NodaTime IsoDayOfWeek numbering.
type RecurrencePattern struct {
	Frequency   RecurrenceFrequency `json:"frequency"`
	Interval    int                 `json:"interval"`
	EndDate     *model.Time         `json:"end_date,omitempty"`
	Occurrences *int                `json:"occurrences,omitempty"`
	DaysOfWeek  []int               `json:"days_of_week,omitempty"`
	DayOfMonth  *int                `json:"day_of_month,omitempty"`
	MonthOfYear *int                `json:"month_of_year,omitempty"`
}

// UserCalendarEvent mirrors SnUserCalendarEvent (and the UserCalendarEventDto
// the calendar aggregation returns; Account is hydrated for both).
type UserCalendarEvent struct {
	Id          string                            `json:"id"`
	Title       string                            `json:"title"`
	Description *string                           `json:"description,omitempty"`
	Location    *string                           `json:"location,omitempty"`
	StartTime   model.Time                        `json:"start_time"`
	EndTime     model.Time                        `json:"end_time"`
	IsAllDay    bool                              `json:"is_all_day"`
	Visibility  EventVisibility                   `json:"visibility"`
	Recurrence  *RecurrencePattern                `json:"recurrence,omitempty"`
	Tags        []string                          `json:"tags"`
	Meta        map[string]any                    `json:"meta,omitempty"`
	Icon        *model.SnCloudFileReferenceObject `json:"icon,omitempty"`
	Background  *model.SnCloudFileReferenceObject `json:"background,omitempty"`
	AccountId   string                            `json:"account_id"`
	Account     *model.Account                    `json:"account,omitempty"`
	CreatedAt   *model.Time                       `json:"created_at,omitempty"`
	UpdatedAt   *model.Time                       `json:"updated_at,omitempty"`
	DeletedAt   *model.Time                       `json:"deleted_at,omitempty"`
}

// NotableHolidayType mirrors NotableHolidayType.
type NotableHolidayType int

const (
	HolidayPublic      NotableHolidayType = 0
	HolidayBank        NotableHolidayType = 1
	HolidaySchool      NotableHolidayType = 2
	HolidayAuthorities NotableHolidayType = 3
	HolidayOptional    NotableHolidayType = 4
	HolidayObservance  NotableHolidayType = 5
)

// NotableDayTag mirrors NotableDayTag.
type NotableDayTag int

const (
	NotableHoliday     NotableDayTag = 0
	NotableEvent       NotableDayTag = 1
	NotableAnniversary NotableDayTag = 2
	NotableMemorial    NotableDayTag = 3
	NotableFestival    NotableDayTag = 4
)

// notableDayTagNames are the Enum.TryParse<NotableDayTag>(..., ignoreCase: true)
// names the C# endpoints accept.
var notableDayTagNames = map[string]NotableDayTag{
	"holiday":     NotableHoliday,
	"event":       NotableEvent,
	"anniversary": NotableAnniversary,
	"memorial":    NotableMemorial,
	"festival":    NotableFestival,
}

// ParseNotableDayTag parses a tag name case-insensitively.
func ParseNotableDayTag(value string) (NotableDayTag, bool) {
	tag, ok := notableDayTagNames[normalizeTagName(value)]
	return tag, ok
}

// NotableDay is the generated (occurrence-resolved) notable day shape.
type NotableDay struct {
	Date           model.Time           `json:"date"`
	LocalName      *string              `json:"local_name,omitempty"`
	GlobalName     *string              `json:"global_name,omitempty"`
	LocalizableKey *string              `json:"localizable_key,omitempty"`
	CountryCode    *string              `json:"country_code,omitempty"`
	Description    *string              `json:"description,omitempty"`
	Meta           map[string]any       `json:"meta,omitempty"`
	OccurrenceKey  *string              `json:"occurrence_key,omitempty"`
	Holidays       []NotableHolidayType `json:"holidays"`
	Tags           []NotableDayTag      `json:"tags"`
}

// StoredNotableDay is the raw `notable_days` row served by /api/notable-days
// (SnNotableDay).
type StoredNotableDay struct {
	Id                string          `json:"id"`
	Name              string          `json:"name"`
	Description       *string         `json:"description,omitempty"`
	LocalName         *string         `json:"local_name,omitempty"`
	LocalizableKey    *string         `json:"localizable_key,omitempty"`
	StartDate         model.Time      `json:"start_date"`
	EndDate           model.Time      `json:"end_date"`
	IsAllDay          bool            `json:"is_all_day"`
	Region            string          `json:"region"`
	Tags              []NotableDayTag `json:"tags"`
	Meta              map[string]any  `json:"meta,omitempty"`
	IsRecurring       bool            `json:"is_recurring"`
	RecurrencePattern *string         `json:"recurrence_pattern,omitempty"`
	IsPeriod          bool            `json:"is_period"`
	HolidayDays       []string        `json:"holiday_days,omitempty"`
	DisplayOrder      *int            `json:"display_order,omitempty"`
	CreatedAt         *model.Time     `json:"created_at,omitempty"`
	UpdatedAt         *model.Time     `json:"updated_at,omitempty"`
	DeletedAt         *model.Time     `json:"deleted_at,omitempty"`
}

// NotableDayRequest is the create/update body (C# NotableDayRequest).
type NotableDayRequest struct {
	Name              string          `json:"name"`
	Description       *string         `json:"description"`
	LocalName         *string         `json:"local_name"`
	LocalizableKey    *string         `json:"localizable_key"`
	StartDate         model.Time      `json:"start_date"`
	EndDate           model.Time      `json:"end_date"`
	IsAllDay          *bool           `json:"is_all_day"`
	Region            string          `json:"region"`
	Tags              []NotableDayTag `json:"tags"`
	Meta              map[string]any  `json:"meta"`
	IsRecurring       bool            `json:"is_recurring"`
	RecurrencePattern *string         `json:"recurrence_pattern"`
	IsPeriod          bool            `json:"is_period"`
	HolidayDays       []string        `json:"holiday_days"`
	DisplayOrder      *int            `json:"display_order"`
}

// CalendarEventType mirrors CalendarEventType.
type CalendarEventType int

const (
	CalendarUserEvent CalendarEventType = 0
	CalendarCheckIn   CalendarEventType = 1
	CalendarStatus    CalendarEventType = 2
	CalendarNotable   CalendarEventType = 3
)

// CreateCalendarEventRequest mirrors CreateCalendarEventRequest.
type CreateCalendarEventRequest struct {
	Title        string             `json:"title"`
	Description  *string            `json:"description"`
	Location     *string            `json:"location"`
	StartTime    model.Time         `json:"start_time"`
	EndTime      model.Time         `json:"end_time"`
	IsAllDay     bool               `json:"is_all_day"`
	Visibility   EventVisibility    `json:"visibility"`
	Recurrence   *RecurrencePattern `json:"recurrence"`
	Tags         []string           `json:"tags"`
	Meta         map[string]any     `json:"meta"`
	IconId       *string            `json:"icon_id"`
	BackgroundId *string            `json:"background_id"`
}

// UpdateCalendarEventRequest mirrors UpdateCalendarEventRequest; nil fields
// are left unchanged.
type UpdateCalendarEventRequest struct {
	Title        *string            `json:"title"`
	Description  *string            `json:"description"`
	Location     *string            `json:"location"`
	StartTime    *model.Time        `json:"start_time"`
	EndTime      *model.Time        `json:"end_time"`
	IsAllDay     *bool              `json:"is_all_day"`
	Visibility   *EventVisibility   `json:"visibility"`
	Recurrence   *RecurrencePattern `json:"recurrence"`
	Tags         *[]string          `json:"tags"`
	Meta         map[string]any     `json:"meta"`
	IconId       *string            `json:"icon_id"`
	BackgroundId *string            `json:"background_id"`
}

// DailyEventResponse mirrors DailyEventResponse.
type DailyEventResponse struct {
	Date          model.Time            `json:"date"`
	CheckInResult *CheckInResult        `json:"check_in_result,omitempty"`
	Statuses      []model.AccountStatus `json:"statuses"`
	UserEvents    []UserCalendarEvent   `json:"user_events"`
	NotableDays   []NotableDay          `json:"notable_days"`
}

// MergedCalendarEvent mirrors MergedCalendarEvent.
type MergedCalendarEvent struct {
	Id          *string           `json:"id,omitempty"`
	Type        CalendarEventType `json:"type"`
	Title       string            `json:"title"`
	Description *string           `json:"description,omitempty"`
	Location    *string           `json:"location,omitempty"`
	StartTime   model.Time        `json:"start_time"`
	EndTime     model.Time        `json:"end_time"`
	IsAllDay    bool              `json:"is_all_day"`
	Meta        map[string]any    `json:"meta,omitempty"`
}

// MergedDailyEventResponse mirrors MergedDailyEventResponse.
type MergedDailyEventResponse struct {
	Date          model.Time            `json:"date"`
	CheckInResult *CheckInResult        `json:"check_in_result,omitempty"`
	Statuses      []model.AccountStatus `json:"statuses"`
	UserEvents    []UserCalendarEvent   `json:"user_events"`
	NotableDays   []NotableDay          `json:"notable_days"`
	MergedEvents  []MergedCalendarEvent `json:"merged_events"`
}

// EventCountdownItem mirrors EventCountdownItem.
type EventCountdownItem struct {
	EventId        *string                           `json:"event_id,omitempty"`
	EventType      CalendarEventType                 `json:"event_type"`
	Title          string                            `json:"title"`
	Description    *string                           `json:"description,omitempty"`
	Location       *string                           `json:"location,omitempty"`
	StartTime      model.Time                        `json:"start_time"`
	EndTime        model.Time                        `json:"end_time"`
	IsAllDay       bool                              `json:"is_all_day"`
	DaysRemaining  int                               `json:"days_remaining"`
	HoursRemaining int                               `json:"hours_remaining"`
	IsOngoing      bool                              `json:"is_ongoing"`
	Meta           map[string]any                    `json:"meta,omitempty"`
	AccountId      *string                           `json:"account_id,omitempty"`
	Icon           *model.SnCloudFileReferenceObject `json:"icon,omitempty"`
	Background     *model.SnCloudFileReferenceObject `json:"background,omitempty"`
}

// CalendarSearchResultItem mirrors CalendarSearchResultItem.
type CalendarSearchResultItem struct {
	Type       CalendarEventType  `json:"type"`
	StartTime  model.Time         `json:"start_time"`
	EndTime    model.Time         `json:"end_time"`
	UserEvent  *UserCalendarEvent `json:"user_event,omitempty"`
	NotableDay *NotableDay        `json:"notable_day,omitempty"`
}

// CalendarEventSubscription mirrors SnCalendarEventSubscription.
type CalendarEventSubscription struct {
	Id              string      `json:"id"`
	SubscriberId    string      `json:"subscriber_id"`
	TargetAccountId string      `json:"target_account_id"`
	Notify          bool        `json:"notify"`
	CreatedAt       *model.Time `json:"created_at,omitempty"`
	UpdatedAt       *model.Time `json:"updated_at,omitempty"`
	DeletedAt       *model.Time `json:"deleted_at,omitempty"`
}

// TimelineEventType mirrors TimelineEventType.
type TimelineEventType int

const (
	TimelineStatusChange TimelineEventType = 0
	TimelineActivity     TimelineEventType = 1
)

// AccountTimelineItem mirrors AccountTimelineItem.
type AccountTimelineItem struct {
	Id        string                  `json:"id"`
	CreatedAt model.Time              `json:"created_at"`
	EventType TimelineEventType       `json:"event_type"`
	Status    *model.AccountStatus    `json:"status,omitempty"`
	Activity  *model.PresenceActivity `json:"activity,omitempty"`
}
