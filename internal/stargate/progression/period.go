package progression

import "time"

// PeriodKey and the next-reset instant for a quest schedule, mirroring
// ProgressionService.GetQuestPeriod. Periods are computed in the account's
// time zone, so "daily" means the account's local day.

// QuestPeriod returns the period key covering instant for the schedule and the
// instant the period rolls over.
func QuestPeriod(schedule QuestSchedule, instant time.Time, loc *time.Location) (string, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	zoned := instant.In(loc)
	switch schedule.RepeatabilityOrDefault() {
	case RepeatWeekly:
		// ISO weeks: the period starts on Monday.
		offset := (int(zoned.Weekday()) + 6) % 7
		start := zoned.AddDate(0, 0, -offset)
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
		next := start.AddDate(0, 0, 7)
		year, week := start.ISOWeek()
		return itoa4(year) + "-W" + itoa2(week), next
	case RepeatMonthly:
		start := time.Date(zoned.Year(), zoned.Month(), 1, 0, 0, 0, 0, loc)
		next := start.AddDate(0, 1, 0)
		return itoa4(start.Year()) + "-" + itoa2(int(start.Month())), next
	default:
		start := time.Date(zoned.Year(), zoned.Month(), zoned.Day(), 0, 0, 0, 0, loc)
		next := start.AddDate(0, 0, 1)
		return start.Format("2006-01-02"), next
	}
}

func itoa4(value int) string {
	out := make([]byte, 0, 4)
	for shift := 3; shift >= 0; shift-- {
		out = append(out, byte('0'+(value/intPow10(shift))%10))
	}
	return string(out)
}

func itoa2(value int) string {
	return string([]byte{byte('0' + value/10%10), byte('0' + value%10)})
}

func intPow10(exp int) int {
	result := 1
	for range exp {
		result *= 10
	}
	return result
}

// ResolveZone turns a stored IANA id into a location, falling back to UTC for
// unknown ids (ProgressionService.ResolveZone).
func ResolveZone(zoneID string) *time.Location {
	if zoneID == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(zoneID)
	if err != nil {
		return time.UTC
	}
	return loc
}

// SameDay reports whether two instants fall on the same local calendar day.
func SameDay(a, b time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}

// DaysBetween is the number of local calendar days from a to b (b later
// than a yields a positive count).
func DaysBetween(a, b time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	start := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	end := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(end.Sub(start).Hours() / 24)
}
