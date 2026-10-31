package accountevents

import "time"

// Recurrence expansion mirrors AccountEventService.ExpandRecurringEvents and
// CalculateNextOccurrence. All arithmetic happens on UTC instants; range
// bounds are inclusive, like the C# comparisons.

// ExpandCalendarEventsForRange expands recurring events when the caller
// supplied a range; with no bounds the list is returned untouched.
func ExpandCalendarEventsForRange(events []UserCalendarEvent, startTime, endTime *time.Time) []UserCalendarEvent {
	if startTime == nil && endTime == nil {
		return events
	}
	rangeStart, rangeEnd := EventExpansionRange(startTime, endTime)
	return ExpandRecurringEvents(events, rangeStart, rangeEnd)
}

// EventExpansionRange mirrors GetEventExpansionRange: a one-sided request is
// capped at 366 days, and no bounds fall back to now + 366 days.
func EventExpansionRange(startTime, endTime *time.Time) (time.Time, time.Time) {
	if startTime != nil && endTime != nil {
		return *startTime, *endTime
	}
	if startTime != nil {
		return *startTime, startTime.Add(366 * 24 * time.Hour)
	}
	if endTime != nil {
		return endTime.Add(-366 * 24 * time.Hour), *endTime
	}
	now := time.Now().UTC()
	return now, now.Add(366 * 24 * time.Hour)
}

// ExpandRecurringEvents materialises every occurrence that overlaps the range.
func ExpandRecurringEvents(events []UserCalendarEvent, rangeStart, rangeEnd time.Time) []UserCalendarEvent {
	expanded := make([]UserCalendarEvent, 0, len(events))
	for _, event := range events {
		if event.Recurrence == nil || event.Recurrence.Frequency == RecurrenceNone {
			if !event.EndTime.Time().Before(rangeStart) && !event.StartTime.Time().After(rangeEnd) {
				expanded = append(expanded, event)
			}
			continue
		}
		expanded = append(expanded, GetRecurringEventOccurrences(event, rangeStart, rangeEnd)...)
	}
	return expanded
}

// GetRecurringEventOccurrences walks the pattern and returns the instances
// that overlap [rangeStart, rangeEnd].
func GetRecurringEventOccurrences(event UserCalendarEvent, rangeStart, rangeEnd time.Time) []UserCalendarEvent {
	occurrences := make([]UserCalendarEvent, 0)
	recurrence := event.Recurrence

	maxOccurrences := 365
	if recurrence.Occurrences != nil {
		maxOccurrences = *recurrence.Occurrences
	}
	actualEnd := rangeEnd
	if recurrence.EndDate != nil && recurrence.EndDate.Time().Before(rangeEnd) {
		actualEnd = recurrence.EndDate.Time()
	}

	currentStart := event.StartTime.Time()
	currentEnd := event.EndTime.Time()
	count := 0
	for currentStart.Before(actualEnd) && count < maxOccurrences {
		if !currentEnd.Before(rangeStart) && !currentStart.After(rangeEnd) {
			occurrences = append(occurrences, createOccurrence(event, currentStart, currentEnd))
		}
		count++
		currentStart, currentEnd = CalculateNextOccurrence(currentStart, currentEnd, *recurrence)
		if currentStart.IsZero() {
			break
		}
	}
	return occurrences
}

// CalculateNextOccurrence advances one recurrence step. A zero start means the
// pattern cannot advance (unknown frequency), mirroring the C# default branch.
func CalculateNextOccurrence(currentStart, currentEnd time.Time, recurrence RecurrencePattern) (time.Time, time.Time) {
	duration := currentEnd.Sub(currentStart)
	interval := recurrence.Interval
	if interval <= 0 {
		interval = 1
	}
	currentStart = currentStart.UTC()
	var nextStart time.Time
	switch recurrence.Frequency {
	case RecurrenceDaily:
		nextStart = currentStart.AddDate(0, 0, interval)
	case RecurrenceWeekly:
		if len(recurrence.DaysOfWeek) > 0 {
			currentDay := isoWeekday(currentStart)
			days := append([]int(nil), recurrence.DaysOfWeek...)
			sortInts(days)
			nextDay := 0
			for _, day := range days {
				if day > currentDay {
					nextDay = day
					break
				}
			}
			if nextDay == 0 {
				nextDay = days[0]
				daysUntilNext := (7 - currentDay) + nextDay
				nextStart = currentStart.AddDate(0, 0, daysUntilNext)
			} else {
				nextStart = currentStart.AddDate(0, 0, nextDay-currentDay)
			}
		} else {
			nextStart = currentStart.AddDate(0, 0, 7*interval)
		}
	case RecurrenceMonthly:
		currentDate := currentStart
		nextMonth := addMonthsClamped(currentDate, interval)
		dayOfMonth := currentDate.Day()
		if recurrence.DayOfMonth != nil {
			dayOfMonth = *recurrence.DayOfMonth
		}
		if maxDay := daysInMonth(nextMonth.Year(), nextMonth.Month()); dayOfMonth > maxDay {
			dayOfMonth = maxDay
		}
		nextStart = time.Date(nextMonth.Year(), nextMonth.Month(), dayOfMonth, 0, 0, 0, 0, time.UTC)
	case RecurrenceYearly:
		currentDate := currentStart
		nextYear := currentDate.Year() + interval
		targetMonth := int(currentDate.Month())
		if recurrence.MonthOfYear != nil {
			targetMonth = *recurrence.MonthOfYear
		}
		targetDay := currentDate.Day()
		if recurrence.DayOfMonth != nil {
			targetDay = *recurrence.DayOfMonth
		}
		if maxDay := daysInMonth(nextYear, time.Month(targetMonth)); targetDay > maxDay {
			targetDay = maxDay
		}
		nextStart = time.Date(nextYear, time.Month(targetMonth), targetDay, 0, 0, 0, 0, time.UTC)
	default:
		return time.Time{}, time.Time{}
	}
	return nextStart, nextStart.Add(duration)
}

func createOccurrence(event UserCalendarEvent, start, end time.Time) UserCalendarEvent {
	occurrence := event
	occurrence.StartTime = modelTime(start)
	occurrence.EndTime = modelTime(end)
	occurrence.Recurrence = nil
	occurrence.Tags = append([]string{}, event.Tags...)
	return occurrence
}

// ─────────────────────────── countdown expansion ───────────────────────────

func expandRecurringEventsForCountdown(events []UserCalendarEvent, fromTime time.Time, includePast bool) []UserCalendarEvent {
	expanded := make([]UserCalendarEvent, 0, len(events))
	for _, event := range events {
		if event.Recurrence == nil || event.Recurrence.Frequency == RecurrenceNone {
			if includePast || event.EndTime.Time().After(fromTime) {
				expanded = append(expanded, event)
			}
			continue
		}
		expanded = append(expanded, getRecurringEventOccurrencesForCountdown(event, fromTime, 3, includePast)...)
	}
	return expanded
}

func getRecurringEventOccurrencesForCountdown(event UserCalendarEvent, fromTime time.Time, maxOccurrences int, includePast bool) []UserCalendarEvent {
	recurrence := event.Recurrence
	occurrenceLimit := 10000
	if recurrence.Occurrences != nil && *recurrence.Occurrences < occurrenceLimit {
		occurrenceLimit = *recurrence.Occurrences
	}

	currentStart := event.StartTime.Time()
	currentEnd := event.EndTime.Time()
	selected := make([]UserCalendarEvent, 0, maxOccurrences)
	var latestPast *UserCalendarEvent
	count := 0
	for count < occurrenceLimit {
		if recurrence.EndDate != nil && currentStart.After(recurrence.EndDate.Time()) {
			break
		}
		occurrence := createOccurrence(event, currentStart, currentEnd)
		if !currentEnd.After(fromTime) {
			latestPast = &occurrence
		} else {
			if includePast && latestPast != nil && len(selected) == 0 {
				selected = append(selected, *latestPast)
			}
			selected = append(selected, occurrence)
			if len(selected) >= maxOccurrences {
				break
			}
		}
		count++
		currentStart, currentEnd = CalculateNextOccurrence(currentStart, currentEnd, *recurrence)
		if currentStart.IsZero() {
			break
		}
	}

	if len(selected) > 0 || !includePast {
		if len(selected) > maxOccurrences {
			return selected[:maxOccurrences]
		}
		return selected
	}
	if latestPast == nil {
		return nil
	}
	return []UserCalendarEvent{*latestPast}
}

// ─────────────────────────── time helpers ───────────────────────────

// isoWeekday returns the NodaTime IsoDayOfWeek value (Monday=1 .. Sunday=7).
func isoWeekday(t time.Time) int {
	day := int(t.Weekday())
	if day == 0 {
		return 7
	}
	return day
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// addMonthsClamped advances a date by months, clamping the day to the target
// month. Only the year/month are read from the result, so the clamped day is
// irrelevant; the helper keeps the arithmetic readable.
func addMonthsClamped(date time.Time, months int) time.Time {
	year := date.Year()
	month := int(date.Month()) - 1 + months
	year += month / 12
	month %= 12
	if month < 0 {
		month += 12
		year--
	}
	return time.Date(year, time.Month(month+1), 1, 0, 0, 0, 0, time.UTC)
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
