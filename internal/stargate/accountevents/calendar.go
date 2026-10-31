package accountevents

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

var (
	// ErrCalendarEventNotFound marks a missing calendar event.
	ErrCalendarEventNotFound = errors.New("calendar event not found")
	// ErrNotEventOwner marks a write on someone else's event.
	ErrNotEventOwner = errors.New("calendar event does not belong to user")
	// ErrSubscriptionNotFound marks a missing subscription.
	ErrSubscriptionNotFound = errors.New("subscription not found")
)

// ValidationError carries a request-field message (C# ArgumentException).
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func validationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

// CalendarOptions is the calendar aggregation request.
type CalendarOptions struct {
	Month              int
	Year               int
	ViewerID           *uuid.UUID
	RegionCode         string
	IncludeNotableDays bool
	Layer              *int
	ReplaceInvisible   bool
}

// ─────────────────────────── calendar aggregation ───────────────────────────

// Calendar mirrors GetEventCalendar: one entry per UTC day of the month with
// the day's check-in, statuses and visible calendar events.
func (s *Service) Calendar(ctx context.Context, account *model.Account, opts CalendarOptions) ([]DailyEventResponse, error) {
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		return nil, err
	}
	year, month := opts.Year, opts.Month
	if year == 0 {
		year = s.now().Year()
	}
	startOfMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	endOfMonth := startOfMonth.AddDate(0, 1, 0)

	var viewerID *uuid.UUID
	if opts.ViewerID != nil {
		viewerID = opts.ViewerID
	}
	isOwner := viewerID != nil && *viewerID == accountID

	statuses, err := s.deps.Accounts.ListStatusesBetween(ctx, accountID, startOfMonth, endOfMonth)
	if err != nil {
		return nil, err
	}
	if opts.ReplaceInvisible {
		for i := range statuses {
			if statuses[i].Type == model.StatusTypeInvisible {
				statuses[i].Type = model.StatusTypeDefault
			}
		}
	}

	checkIns, err := s.deps.Store.ListCheckInsBetween(ctx, accountID, startOfMonth, endOfMonth)
	if err != nil {
		return nil, err
	}
	for i := range checkIns {
		s.PrepareCheckInResult(account, &checkIns[i], fortuneReportVersion)
	}

	filter := CalendarEventFilter{StartTime: &startOfMonth, EndTime: &endOfMonth}
	allEvents := make([]UserCalendarEvent, 0)
	own, err := s.deps.Store.ListAccountEvents(ctx, accountID, viewerID, filter)
	if err != nil {
		return nil, err
	}
	if !isOwner {
		if viewerID == nil {
			// Anonymous: the store already restricted the list to Public.
			allEvents = append(allEvents, own...)
		} else {
			friends, err := s.friendSet(ctx, accountID)
			if err != nil {
				return nil, err
			}
			for _, event := range own {
				if event.Visibility == VisibilityPublic || (event.Visibility == VisibilityFriends && friends[*viewerID]) {
					allEvents = append(allEvents, event)
				}
			}
		}
	} else {
		allEvents = append(allEvents, own...)
		friendIDs, err := s.friendIDs(ctx, accountID)
		if err != nil {
			return nil, err
		}
		friendEvents, err := s.deps.Store.ListEventsForAccounts(ctx, friendIDs,
			[]EventVisibility{VisibilityPublic, VisibilityFriends}, filter)
		if err != nil {
			return nil, err
		}
		allEvents = append(allEvents, friendEvents...)

		subscribedIDs, err := s.deps.Store.ListSubscriptions(ctx, accountID)
		if err != nil {
			return nil, err
		}
		subscribedEvents, err := s.deps.Store.ListEventsForAccounts(ctx, subscribedIDs,
			[]EventVisibility{VisibilityPublic}, filter)
		if err != nil {
			return nil, err
		}
		allEvents = append(allEvents, subscribedEvents...)
	}

	expanded := ExpandRecurringEvents(dedupeEvents(allEvents), startOfMonth, endOfMonth)
	s.hydrateAccounts(ctx, expanded)

	statusesByDate := make(map[string][]model.AccountStatus)
	for _, status := range statuses {
		if status.CreatedAt == nil {
			continue
		}
		key := status.CreatedAt.Time().UTC().Format("2006-01-02")
		statusesByDate[key] = append(statusesByDate[key], status)
	}
	checkInByDate := make(map[string]CheckInResult)
	for _, result := range checkIns {
		if result.CreatedAt == nil {
			continue
		}
		key := result.CreatedAt.Time().UTC().Format("2006-01-02")
		checkInByDate[key] = result
	}
	eventsByDate := make(map[string][]UserCalendarEvent)
	for _, event := range expanded {
		key := event.StartTime.Time().UTC().Format("2006-01-02")
		eventsByDate[key] = append(eventsByDate[key], event)
	}

	notableByDate := make(map[string][]NotableDay)
	if opts.IncludeNotableDays && strings.TrimSpace(opts.RegionCode) != "" && s.deps.Notable != nil {
		days, err := s.deps.Notable.GetNotableDays(ctx, year, opts.RegionCode, nil, opts.Layer)
		if err != nil {
			return nil, err
		}
		for _, day := range days {
			instant := day.Date.Time().UTC()
			if instant.Month() != time.Month(month) || instant.Year() != year {
				continue
			}
			key := instant.Format("2006-01-02")
			notableByDate[key] = append(notableByDate[key], day)
		}
	}

	daysInMonth := daysInMonth(year, time.Month(month))
	calendar := make([]DailyEventResponse, 0, daysInMonth)
	for day := 1; day <= daysInMonth; day++ {
		date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		key := date.Format("2006-01-02")
		entry := DailyEventResponse{
			Date:        modelTime(date),
			Statuses:    statusesByDate[key],
			UserEvents:  eventsByDate[key],
			NotableDays: notableByDate[key],
		}
		if entry.Statuses == nil {
			entry.Statuses = []model.AccountStatus{}
		}
		if entry.UserEvents == nil {
			entry.UserEvents = []UserCalendarEvent{}
		}
		if entry.NotableDays == nil {
			entry.NotableDays = []NotableDay{}
		}
		if result, ok := checkInByDate[key]; ok {
			copied := result
			entry.CheckInResult = &copied
		}
		calendar = append(calendar, entry)
	}
	return calendar, nil
}

// MergedCalendar mirrors GetMergedEventCalendar: the daily calendar plus a
// flat merged_events list carrying every source with its type.
func (s *Service) MergedCalendar(ctx context.Context, account *model.Account, opts CalendarOptions) (*MergedDailyEventResponse, error) {
	year, month := opts.Year, opts.Month
	if year == 0 {
		year = s.now().Year()
	}
	baseOpts := opts
	baseOpts.IncludeNotableDays = false
	calendar, err := s.Calendar(ctx, account, baseOpts)
	if err != nil {
		return nil, err
	}

	notableDays := make([]NotableDay, 0)
	if strings.TrimSpace(opts.RegionCode) != "" && s.deps.Notable != nil {
		days, err := s.deps.Notable.GetNotableDays(ctx, year, opts.RegionCode, nil, opts.Layer)
		if err != nil {
			return nil, err
		}
		for _, day := range days {
			instant := day.Date.Time().UTC()
			if instant.Month() == time.Month(month) && instant.Year() == year {
				notableDays = append(notableDays, day)
			}
		}
	}
	notableByDate := make(map[string][]NotableDay)
	for _, day := range notableDays {
		key := day.Date.Time().UTC().Format("2006-01-02")
		notableByDate[key] = append(notableByDate[key], day)
	}

	response := &MergedDailyEventResponse{
		Date:         modelTime(time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)),
		NotableDays:  notableDays,
		MergedEvents: []MergedCalendarEvent{},
	}
	if len(calendar) > 0 {
		response.Date = calendar[0].Date
		response.CheckInResult = calendar[0].CheckInResult
	}
	for _, day := range calendar {
		response.Statuses = append(response.Statuses, day.Statuses...)
		response.UserEvents = append(response.UserEvents, day.UserEvents...)
	}
	if response.Statuses == nil {
		response.Statuses = []model.AccountStatus{}
	}
	if response.UserEvents == nil {
		response.UserEvents = []UserCalendarEvent{}
	}

	merged := make([]MergedCalendarEvent, 0)
	for _, day := range calendar {
		key := day.Date.Time().UTC().Format("2006-01-02")

		if day.CheckInResult != nil {
			result := day.CheckInResult
			levelName := result.Level.String()
			rewardPoints := float64(0)
			if result.RewardPoints != nil {
				rewardPoints = *result.RewardPoints
			}
			rewardExperience := 0
			if result.RewardExperience != nil {
				rewardExperience = *result.RewardExperience
			}
			id := result.Id
			merged = append(merged, MergedCalendarEvent{
				Id:          &id,
				Type:        CalendarCheckIn,
				Title:       "Check-in: " + levelName,
				Description: new("Daily check-in result: " + levelName),
				StartTime:   *result.CreatedAt,
				EndTime:     *result.CreatedAt,
				IsAllDay:    true,
				Meta: map[string]any{
					"level":             levelName,
					"reward_points":     rewardPoints,
					"reward_experience": rewardExperience,
					"tips":              result.Tips,
					"fortune_report":    result.FortuneReport,
				},
			})
		}

		for _, status := range day.Statuses {
			if status.CreatedAt == nil {
				continue
			}
			id := status.Id
			title := "Status Update"
			if status.Label != nil && *status.Label != "" {
				title = *status.Label
			}
			end := status.CreatedAt.Time().Add(24 * time.Hour)
			if status.ClearedAt != nil {
				end = status.ClearedAt.Time()
			}
			merged = append(merged, MergedCalendarEvent{
				Id:          &id,
				Type:        CalendarStatus,
				Title:       title,
				Description: new(fmt.Sprintf("Status: %s - %s", attitudeName(status.Attitude), statusTypeName(status.Type))),
				StartTime:   *status.CreatedAt,
				EndTime:     modelTime(end),
				IsAllDay:    false,
				Meta: map[string]any{
					"attitude": attitudeName(status.Attitude),
					"type":     statusTypeName(status.Type),
					"symbol":   stringValue(status.Symbol),
				},
			})
		}

		for _, event := range day.UserEvents {
			id := event.Id
			description := ""
			if event.Description != nil {
				description = *event.Description
			}
			meta := event.Meta
			if meta == nil {
				meta = map[string]any{}
			}
			merged = append(merged, MergedCalendarEvent{
				Id:          &id,
				Type:        CalendarUserEvent,
				Title:       event.Title,
				Description: &description,
				Location:    event.Location,
				StartTime:   event.StartTime,
				EndTime:     event.EndTime,
				IsAllDay:    event.IsAllDay,
				Meta:        meta,
			})
		}

		for _, notable := range notableByDate[key] {
			title := "Holiday"
			if notable.GlobalName != nil {
				title = *notable.GlobalName
			} else if notable.LocalName != nil {
				title = *notable.LocalName
			}
			holidays := make([]string, 0, len(notable.Holidays))
			for _, holiday := range notable.Holidays {
				holidays = append(holidays, holidayTypeName(holiday))
			}
			merged = append(merged, MergedCalendarEvent{
				Type:        CalendarNotable,
				Title:       title,
				Description: new(stringValue(notable.LocalName)),
				StartTime:   notable.Date,
				EndTime:     modelTime(notable.Date.Time().Add(24 * time.Hour)),
				IsAllDay:    true,
				Meta: map[string]any{
					"local_name":    stringValue(notable.LocalName),
					"country_code":  stringValue(notable.CountryCode),
					"holiday_types": holidays,
				},
			})
		}
	}

	sortMergedEvents(merged)
	response.MergedEvents = merged
	return response, nil
}

// ─────────────────────────── event listing ───────────────────────────

// CalendarTags returns the account's event tags.
func (s *Service) CalendarTags(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	return s.deps.Store.CalendarTags(ctx, accountID)
}

// AccessibleEvents mirrors GetAccessibleCalendarEventsAsync.
func (s *Service) AccessibleEvents(ctx context.Context, viewerID uuid.UUID, accountID *uuid.UUID, startTime, endTime *time.Time, query string, tags []string, offset, take int) ([]UserCalendarEvent, int, error) {
	friendIDs, err := s.friendIDs(ctx, viewerID)
	if err != nil {
		return nil, 0, err
	}
	subscribedIDs, err := s.deps.Store.ListSubscriptions(ctx, viewerID)
	if err != nil {
		return nil, 0, err
	}
	if accountID != nil && *accountID != viewerID &&
		!containsUUID(friendIDs, *accountID) && !containsUUID(subscribedIDs, *accountID) {
		return []UserCalendarEvent{}, 0, nil
	}

	events, err := s.deps.Store.ListAccessibleEvents(ctx, viewerID, friendIDs, subscribedIDs, CalendarEventFilter{
		AccountID: accountID,
		Query:     query,
		Tags:      normalizeCalendarTags(tags),
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return nil, 0, err
	}
	expanded := ExpandCalendarEventsForRange(events, startTime, endTime)
	sortEvents(expanded)

	total := len(expanded)
	page := paginateEvents(expanded, offset, take)
	s.hydrateAccounts(ctx, page)
	return page, total, nil
}

// AccountEvents mirrors GetUserCalendarEventsAsync for the public account
// calendar: anonymous viewers see Public events, friends also Friends events.
func (s *Service) AccountEvents(ctx context.Context, accountID uuid.UUID, viewerID *uuid.UUID, startTime, endTime *time.Time, query string, tags []string, offset, take int) ([]UserCalendarEvent, int, error) {
	events, err := s.deps.Store.ListAccountEvents(ctx, accountID, viewerID, CalendarEventFilter{
		Query:     query,
		Tags:      normalizeCalendarTags(tags),
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return nil, 0, err
	}
	if viewerID != nil && *viewerID != accountID {
		friends, err := s.friendSet(ctx, accountID)
		if err != nil {
			return nil, 0, err
		}
		filtered := events[:0]
		for _, event := range events {
			if event.Visibility == VisibilityPublic || (event.Visibility == VisibilityFriends && friends[*viewerID]) {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}

	expanded := ExpandCalendarEventsForRange(events, startTime, endTime)
	sortEvents(expanded)
	total := len(expanded)
	page := paginateEvents(expanded, offset, take)
	s.hydrateAccounts(ctx, page)
	return page, total, nil
}

// ─────────────────────────── event CRUD ───────────────────────────

// CreateEvent mirrors CreateCalendarEventAsync.
func (s *Service) CreateEvent(ctx context.Context, accountID uuid.UUID, request CreateCalendarEventRequest) (*UserCalendarEvent, error) {
	if strings.TrimSpace(request.Title) == "" {
		return nil, validationError("request", "Title is required")
	}
	start := request.StartTime.Time()
	end := request.EndTime.Time()
	if !end.After(start) {
		return nil, validationError("request", "End time must be after start time")
	}

	event := &UserCalendarEvent{
		Title:       strings.TrimSpace(request.Title),
		Description: normalizeNullableString(request.Description),
		Location:    normalizeNullableString(request.Location),
		StartTime:   modelTime(start),
		EndTime:     modelTime(end),
		IsAllDay:    request.IsAllDay,
		Visibility:  request.Visibility,
		Recurrence:  request.Recurrence,
		Tags:        normalizeCalendarTags(request.Tags),
		Meta:        request.Meta,
		AccountId:   accountID.String(),
	}
	s.resolveEventFiles(ctx, event, request.IconId, request.BackgroundId)
	if err := s.deps.Store.CreateCalendarEvent(ctx, event); err != nil {
		return nil, err
	}
	s.hydrateAccounts(ctx, []UserCalendarEvent{*event})
	return event, nil
}

// UpdateEvent mirrors UpdateCalendarEventAsync.
func (s *Service) UpdateEvent(ctx context.Context, accountID, eventID uuid.UUID, request UpdateCalendarEventRequest) (*UserCalendarEvent, error) {
	event, err := s.deps.Store.GetCalendarEvent(ctx, eventID)
	if err != nil {
		return nil, ErrCalendarEventNotFound
	}
	ownerID, err := uuid.Parse(event.AccountId)
	if err != nil || ownerID != accountID {
		return nil, ErrNotEventOwner
	}

	if request.Title != nil {
		event.Title = strings.TrimSpace(*request.Title)
	}
	if request.Description != nil {
		event.Description = normalizeNullableString(request.Description)
	}
	if request.Location != nil {
		event.Location = normalizeNullableString(request.Location)
	}
	if request.StartTime != nil {
		event.StartTime = *request.StartTime
	}
	if request.EndTime != nil {
		event.EndTime = *request.EndTime
	}
	if request.IsAllDay != nil {
		event.IsAllDay = *request.IsAllDay
	}
	if request.Visibility != nil {
		event.Visibility = *request.Visibility
	}
	if request.Recurrence != nil {
		event.Recurrence = request.Recurrence
	}
	if request.Tags != nil {
		event.Tags = normalizeCalendarTags(*request.Tags)
	}
	if request.Meta != nil {
		event.Meta = request.Meta
	}
	if !event.EndTime.Time().After(event.StartTime.Time()) {
		return nil, validationError("request", "End time must be after start time")
	}
	s.resolveEventFiles(ctx, event, request.IconId, request.BackgroundId)

	if err := s.deps.Store.UpdateCalendarEvent(ctx, event); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrCalendarEventNotFound
		}
		return nil, err
	}
	event.UpdatedAt = modelTimePtrValue(s.now())
	s.hydrateAccounts(ctx, []UserCalendarEvent{*event})
	return event, nil
}

// DeleteEvent mirrors DeleteCalendarEventAsync.
func (s *Service) DeleteEvent(ctx context.Context, accountID, eventID uuid.UUID) error {
	event, err := s.deps.Store.GetCalendarEvent(ctx, eventID)
	if err != nil {
		return ErrCalendarEventNotFound
	}
	ownerID, err := uuid.Parse(event.AccountId)
	if err != nil || ownerID != accountID {
		return ErrNotEventOwner
	}
	if err := s.deps.Store.DeleteCalendarEvent(ctx, eventID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrCalendarEventNotFound
		}
		return err
	}
	return nil
}

// Event mirrors GetCalendarEventAsync with the visibility rule applied. An
// anonymous viewer only sees Public events.
func (s *Service) Event(ctx context.Context, eventID uuid.UUID, viewerID *uuid.UUID) (*UserCalendarEvent, error) {
	event, err := s.deps.Store.GetCalendarEvent(ctx, eventID)
	if err != nil {
		return nil, ErrCalendarEventNotFound
	}
	ownerID, err := uuid.Parse(event.AccountId)
	if err != nil {
		return nil, ErrCalendarEventNotFound
	}
	switch {
	case viewerID != nil && *viewerID == ownerID:
		// owner sees everything
	case event.Visibility == VisibilityPublic:
		// public
	case event.Visibility == VisibilityFriends && viewerID != nil:
		friends, err := s.friendSet(ctx, ownerID)
		if err != nil {
			return nil, err
		}
		if !friends[*viewerID] {
			return nil, ErrCalendarEventNotFound
		}
	default:
		return nil, ErrCalendarEventNotFound
	}
	s.hydrateAccounts(ctx, []UserCalendarEvent{*event})
	return event, nil
}

func (s *Service) resolveEventFiles(ctx context.Context, event *UserCalendarEvent, iconID, backgroundID *string) {
	resolve := func(fileID *string) *model.SnCloudFileReferenceObject {
		if fileID == nil || s.deps.Files == nil || !s.deps.Files.Available() {
			return nil
		}
		file, err := s.deps.Files.Resolve(ctx, *fileID)
		if err != nil {
			s.log().Warn("resolve calendar file", "file", *fileID, "error", err)
			return nil
		}
		return file
	}
	if icon := resolve(iconID); icon != nil {
		event.Icon = icon
	}
	if background := resolve(backgroundID); background != nil {
		event.Background = background
	}
}

// ─────────────────────────── subscriptions ───────────────────────────

// Subscribe mirrors SubscribeToCalendarEventsAsync: subscribing twice returns
// the existing row.
func (s *Service) Subscribe(ctx context.Context, subscriberID, targetID uuid.UUID) (*CalendarEventSubscription, error) {
	if subscriberID == targetID {
		return nil, validationError("request", "Cannot subscribe to your own events")
	}
	existing, err := s.deps.Store.GetSubscription(ctx, subscriberID, targetID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return s.deps.Store.CreateSubscription(ctx, subscriberID, targetID)
}

// Unsubscribe mirrors UnsubscribeFromCalendarEventsAsync.
func (s *Service) Unsubscribe(ctx context.Context, subscriberID, targetID uuid.UUID) error {
	if err := s.deps.Store.DeleteSubscription(ctx, subscriberID, targetID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrSubscriptionNotFound
		}
		return err
	}
	return nil
}

// Subscriptions returns the target account ids a subscriber follows.
func (s *Service) Subscriptions(ctx context.Context, subscriberID uuid.UUID) ([]uuid.UUID, error) {
	return s.deps.Store.ListSubscriptions(ctx, subscriberID)
}

// Subscribers returns the subscriber ids of a target account.
func (s *Service) Subscribers(ctx context.Context, targetID uuid.UUID) ([]uuid.UUID, error) {
	return s.deps.Store.ListSubscribers(ctx, targetID)
}

// ─────────────────────────── countdown ───────────────────────────

// Countdown mirrors GetCountdownEventsAsync.
func (s *Service) Countdown(ctx context.Context, account *model.Account, viewerID *uuid.UUID, regionCode string, includeNotableDays bool, tag *NotableDayTag, take, offset int, layer *int) ([]EventCountdownItem, int, error) {
	accountID, err := uuid.Parse(account.Id)
	if err != nil {
		return nil, 0, err
	}
	now := s.now()
	isOwner := viewerID != nil && *viewerID == accountID

	var events []UserCalendarEvent
	if !isOwner && viewerID != nil {
		visible, err := s.deps.Store.ListAccountEvents(ctx, accountID, viewerID, CalendarEventFilter{})
		if err != nil {
			return nil, 0, err
		}
		friends, err := s.friendSet(ctx, accountID)
		if err != nil {
			return nil, 0, err
		}
		for _, event := range visible {
			if event.Visibility == VisibilityPublic || (event.Visibility == VisibilityFriends && friends[*viewerID]) {
				events = append(events, event)
			}
		}
	} else if !isOwner {
		visible, err := s.deps.Store.ListAccountEvents(ctx, accountID, nil, CalendarEventFilter{})
		if err != nil {
			return nil, 0, err
		}
		events = append(events, visible...)
	} else {
		own, err := s.deps.Store.ListAccountEvents(ctx, accountID, viewerID, CalendarEventFilter{})
		if err != nil {
			return nil, 0, err
		}
		events = append(events, own...)

		friendIDs, err := s.friendIDs(ctx, accountID)
		if err != nil {
			return nil, 0, err
		}
		friendEvents, err := s.deps.Store.ListEventsForAccounts(ctx, friendIDs,
			[]EventVisibility{VisibilityPublic, VisibilityFriends}, CalendarEventFilter{})
		if err != nil {
			return nil, 0, err
		}
		events = append(events, friendEvents...)

		subscribedIDs, err := s.deps.Store.ListSubscriptions(ctx, accountID)
		if err != nil {
			return nil, 0, err
		}
		subscribedEvents, err := s.deps.Store.ListEventsForAccounts(ctx, subscribedIDs,
			[]EventVisibility{VisibilityPublic}, CalendarEventFilter{})
		if err != nil {
			return nil, 0, err
		}
		events = append(events, subscribedEvents...)
	}

	expanded := expandRecurringEventsForCountdown(dedupeEvents(events), now, true)
	items := make([]EventCountdownItem, 0, len(expanded))
	for _, event := range expanded {
		items = append(items, countdownFromEvent(event, now))
	}

	if includeNotableDays && strings.TrimSpace(regionCode) != "" && s.deps.Notable != nil {
		currentYear := now.Year()
		for _, year := range []int{currentYear - 1, currentYear, currentYear + 1, currentYear + 2} {
			days, err := s.deps.Notable.GetNotableDays(ctx, year, regionCode, tag, layer)
			if err != nil {
				return nil, 0, err
			}
			for _, day := range days {
				items = append(items, countdownFromNotableDay(day, now))
			}
		}
	}

	sortCountdown(items, now)
	total := len(items)
	return paginateCountdown(items, offset, take), total, nil
}

func countdownFromEvent(event UserCalendarEvent, now time.Time) EventCountdownItem {
	start := event.StartTime.Time()
	end := event.EndTime.Time()
	timeUntil := start.Sub(now)
	daysRemaining := int(timeUntil.Hours() / 24)
	hoursRemaining := int(timeUntil.Hours()) % 24
	if daysRemaining < 0 {
		daysRemaining = 0
	}
	if hoursRemaining < 0 {
		hoursRemaining = 0
	}
	id := event.Id
	accountID := event.AccountId
	return EventCountdownItem{
		EventId:        &id,
		EventType:      CalendarUserEvent,
		Title:          event.Title,
		Description:    event.Description,
		Location:       event.Location,
		StartTime:      event.StartTime,
		EndTime:        event.EndTime,
		IsAllDay:       event.IsAllDay,
		DaysRemaining:  daysRemaining,
		HoursRemaining: hoursRemaining,
		IsOngoing:      !start.After(now) && end.After(now),
		Meta:           event.Meta,
		AccountId:      &accountID,
		Icon:           event.Icon,
		Background:     event.Background,
	}
}

func countdownFromNotableDay(day NotableDay, now time.Time) EventCountdownItem {
	start := day.Date.Time()
	timeUntil := start.Sub(now)
	daysRemaining := int(timeUntil.Hours() / 24)
	hoursRemaining := int(timeUntil.Hours()) % 24
	if daysRemaining < 0 {
		daysRemaining = 0
	}
	if hoursRemaining < 0 {
		hoursRemaining = 0
	}
	title := "Holiday"
	if day.GlobalName != nil {
		title = *day.GlobalName
	} else if day.LocalName != nil {
		title = *day.LocalName
	}
	holidays := make([]string, 0, len(day.Holidays))
	for _, holiday := range day.Holidays {
		holidays = append(holidays, holidayTypeName(holiday))
	}
	ongoing := start.UTC().Format("2006-01-02") == now.Format("2006-01-02")
	return EventCountdownItem{
		EventType:      CalendarNotable,
		Title:          title,
		Description:    day.LocalName,
		StartTime:      day.Date,
		EndTime:        modelTime(start.Add(24 * time.Hour)),
		IsAllDay:       true,
		DaysRemaining:  daysRemaining,
		HoursRemaining: hoursRemaining,
		IsOngoing:      ongoing,
		Meta: map[string]any{
			"local_name":    stringValue(day.LocalName),
			"country_code":  stringValue(day.CountryCode),
			"holiday_types": holidays,
		},
	}
}

// ─────────────────────────── search ───────────────────────────

// Search mirrors SearchCalendarAsync: accessible user events merged with
// notable days, ordered by start time.
func (s *Service) Search(ctx context.Context, viewerID uuid.UUID, regionCode string, query string, accountID *uuid.UUID, startTime, endTime *time.Time, tags []string, notableTag *NotableDayTag, offset, take int) ([]CalendarSearchResultItem, int, error) {
	events, _, err := s.AccessibleEvents(ctx, viewerID, accountID, startTime, endTime, query, tags, 0, math.MaxInt32)
	if err != nil {
		return nil, 0, err
	}
	notableDays := make([]NotableDay, 0)
	if s.deps.Notable != nil {
		notableDays, _, err = s.deps.Notable.SearchNotableDays(ctx, regionCode, startTime, endTime, query, notableTag, 0, math.MaxInt32)
		if err != nil {
			return nil, 0, err
		}
	}

	items := make([]CalendarSearchResultItem, 0, len(events)+len(notableDays))
	for i := range events {
		event := events[i]
		items = append(items, CalendarSearchResultItem{
			Type:      CalendarUserEvent,
			StartTime: event.StartTime,
			EndTime:   event.EndTime,
			UserEvent: &event,
		})
	}
	for i := range notableDays {
		day := notableDays[i]
		items = append(items, CalendarSearchResultItem{
			Type:       CalendarNotable,
			StartTime:  day.Date,
			EndTime:    day.Date,
			NotableDay: &day,
		})
	}
	sortSearchItems(items)

	total := len(items)
	return paginateSearch(items, offset, take), total, nil
}

// ─────────────────────────── timeline ───────────────────────────

// Timeline mirrors the retired C# GetTimeline: every status and presence
// activity of the account, newest first.
func (s *Service) Timeline(ctx context.Context, accountID uuid.UUID, offset, take int) ([]AccountTimelineItem, int, error) {
	statuses, err := s.deps.Accounts.ListStatusesForTimeline(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	activities, err := s.deps.Accounts.ListActivitiesForTimeline(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}

	items := make([]AccountTimelineItem, 0, len(statuses)+len(activities))
	for i := range statuses {
		status := statuses[i]
		created := modelTime(time.Time{})
		if status.CreatedAt != nil {
			created = *status.CreatedAt
		}
		items = append(items, AccountTimelineItem{
			Id:        status.Id,
			CreatedAt: created,
			EventType: TimelineStatusChange,
			Status:    &status,
		})
	}
	for i := range activities {
		activity := activities[i]
		created := modelTime(time.Time{})
		if activity.CreatedAt != nil {
			created = *activity.CreatedAt
		}
		items = append(items, AccountTimelineItem{
			Id:        activity.Id,
			CreatedAt: created,
			EventType: TimelineActivity,
			Activity:  &activity,
		})
	}
	sortTimeline(items)

	total := len(items)
	return paginateTimeline(items, offset, take), total, nil
}

// ─────────────────────────── helpers ───────────────────────────

func (s *Service) friendIDs(ctx context.Context, accountID uuid.UUID) ([]uuid.UUID, error) {
	ids, err := s.deps.Accounts.ListRelatedAccountIDs(ctx, accountID, model.RelationshipFriends, false)
	if err != nil {
		return nil, err
	}
	parsed := make([]uuid.UUID, 0, len(ids))
	for _, raw := range ids {
		if id, err := uuid.Parse(raw); err == nil {
			parsed = append(parsed, id)
		}
	}
	return parsed, nil
}

func (s *Service) friendSet(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]bool, error) {
	ids, err := s.friendIDs(ctx, accountID)
	if err != nil {
		return nil, err
	}
	set := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

func (s *Service) hydrateAccounts(ctx context.Context, events []UserCalendarEvent) {
	if len(events) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(events))
	seen := make(map[uuid.UUID]struct{}, len(events))
	for _, event := range events {
		id, err := uuid.Parse(event.AccountId)
		if err != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	accounts, err := s.deps.Accounts.GetAccountsByIDs(ctx, ids)
	if err != nil {
		s.log().Warn("hydrate calendar accounts", "error", err)
		return
	}
	byID := make(map[string]*model.Account, len(accounts))
	for i := range accounts {
		byID[accounts[i].Id] = &accounts[i]
	}
	for i := range events {
		if account, ok := byID[events[i].AccountId]; ok {
			events[i].Account = account
		}
	}
}

func dedupeEvents(events []UserCalendarEvent) []UserCalendarEvent {
	seen := make(map[string]struct{}, len(events))
	out := make([]UserCalendarEvent, 0, len(events))
	for _, event := range events {
		if _, ok := seen[event.Id]; ok {
			continue
		}
		seen[event.Id] = struct{}{}
		out = append(out, event)
	}
	return out
}

func sortEvents(events []UserCalendarEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		left, right := events[i].StartTime.Time(), events[j].StartTime.Time()
		if !left.Equal(right) {
			return left.Before(right)
		}
		return events[i].EndTime.Time().Before(events[j].EndTime.Time())
	})
}

func paginateEvents(events []UserCalendarEvent, offset, take int) []UserCalendarEvent {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(events) {
		return []UserCalendarEvent{}
	}
	end := offset + take
	if take < 0 || end > len(events) {
		end = len(events)
	}
	return events[offset:end]
}

func sortMergedEvents(events []MergedCalendarEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].StartTime.Time().Before(events[j].StartTime.Time())
	})
}

func sortSearchItems(items []CalendarSearchResultItem) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].StartTime.Time(), items[j].StartTime.Time()
		if !left.Equal(right) {
			return left.Before(right)
		}
		return items[i].EndTime.Time().Before(items[j].EndTime.Time())
	})
}

func paginateSearch(items []CalendarSearchResultItem, offset, take int) []CalendarSearchResultItem {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []CalendarSearchResultItem{}
	}
	end := offset + take
	if take < 0 || end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// sortCountdown mirrors the C# ordering: ongoing first, then future by
// ascending start, then past by descending start.
func sortCountdown(items []EventCountdownItem, now time.Time) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.IsOngoing != right.IsOngoing {
			return left.IsOngoing
		}
		leftFuture := !left.StartTime.Time().Before(now)
		rightFuture := !right.StartTime.Time().Before(now)
		if leftFuture != rightFuture {
			return leftFuture
		}
		if leftFuture {
			return left.StartTime.Time().Before(right.StartTime.Time())
		}
		return left.StartTime.Time().After(right.StartTime.Time())
	})
}

func paginateCountdown(items []EventCountdownItem, offset, take int) []EventCountdownItem {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []EventCountdownItem{}
	}
	end := offset + take
	if take < 0 || end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

func sortTimeline(items []AccountTimelineItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CreatedAt.Time().After(items[j].CreatedAt.Time())
	})
}

func paginateTimeline(items []AccountTimelineItem, offset, take int) []AccountTimelineItem {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []AccountTimelineItem{}
	}
	end := offset + take
	if take < 0 || end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

func attitudeName(attitude model.StatusAttitude) string {
	switch attitude {
	case model.StatusAttitudePositive:
		return "Positive"
	case model.StatusAttitudeNegative:
		return "Negative"
	default:
		return "Neutral"
	}
}

func statusTypeName(statusType model.StatusType) string {
	switch statusType {
	case model.StatusTypeBusy:
		return "Busy"
	case model.StatusTypeDoNotDisturb:
		return "DoNotDisturb"
	case model.StatusTypeInvisible:
		return "Invisible"
	default:
		return "Default"
	}
}

func holidayTypeName(holiday NotableHolidayType) string {
	switch holiday {
	case HolidayPublic:
		return "Public"
	case HolidayBank:
		return "Bank"
	case HolidaySchool:
		return "School"
	case HolidayAuthorities:
		return "Authorities"
	case HolidayOptional:
		return "Optional"
	case HolidayObservance:
		return "Observance"
	default:
		return "Public"
	}
}

func normalizeNullableString(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func containsUUID(values []uuid.UUID, wanted uuid.UUID) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
