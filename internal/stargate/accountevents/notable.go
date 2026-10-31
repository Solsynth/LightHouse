package accountevents

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NotableService resolves notable days/holidays into concrete occurrences for
// a requested solar year. It mirrors Passport's NotableDaysService.
//
// Lunar patterns use an approximation instead of a lunisolar calendar
// library: the pattern's lunar month/day is projected from the Chinese New
// Year date with the mean synodic month (29.5306 days). Leap months make the
// projection drift by up to a month in some years, and days adjacent to a
// month boundary can be off by one; this is documented and intentional.
type NotableService struct {
	store *Store
}

// NewNotableService builds the notable-day resolver.
func NewNotableService(store *Store) *NotableService { return &NotableService{store: store} }

// GetNotableDays returns every notable day of the region for the year:
// generated recurring occurrences plus the built-in global holidays.
func (n *NotableService) GetNotableDays(ctx context.Context, year int, regionCode string, tag *NotableDayTag, maxPriority *int) ([]NotableDay, error) {
	regionCode = NormalizeRegionCode(regionCode)
	entities, err := n.store.ListNotableDaysForYear(ctx, year, regionCode, tag)
	if err != nil {
		return nil, err
	}

	days := make([]NotableDay, 0, len(entities))
	for i := range entities {
		entity := &entities[i]
		switch {
		case entity.IsPeriod && entity.IsRecurring:
			days = append(days, generatePeriodDays(entity, year)...)
		case entity.IsRecurring:
			if day := generateRecurringDay(entity, year); day != nil {
				days = append(days, *day)
			}
		case entity.StartDate.UTC().Year() == year:
			days = append(days, attachOccurrenceKey(storedEntityToNotableDay(entity), regionCode))
		}
	}

	for _, globalDay := range GetGlobalHolidays(year, regionCode) {
		if tag != nil && !containsTag(globalDay.Tags, *tag) {
			continue
		}
		duplicate := false
		for _, day := range days {
			if day.Date.Time().Equal(globalDay.Date.Time()) && equalOptional(day.GlobalName, globalDay.GlobalName) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			days = append(days, globalDay)
		}
	}

	sortNotableDays(days)
	return ApplyPriorityFilter(days, maxPriority), nil
}

// GetGeneratedNotableDay resolves an occurrence key back to its generated day.
func (n *NotableService) GetGeneratedNotableDay(ctx context.Context, occurrenceKey string) (*NotableDay, error) {
	regionCode, date, ok := parseOccurrenceKey(occurrenceKey)
	if !ok {
		return nil, nil
	}
	days, err := n.GetNotableDays(ctx, date.Year(), regionCode, nil, nil)
	if err != nil {
		return nil, err
	}
	for i := range days {
		if days[i].OccurrenceKey != nil && *days[i].OccurrenceKey == occurrenceKey {
			return &days[i], nil
		}
	}
	return nil, nil
}

// ListStoredNotableDays is the raw /api/notable-days listing: anchored rows
// for the year, ordered by display order, optionally filtered by tag priority.
func (n *NotableService) ListStoredNotableDays(ctx context.Context, year int, regionCode string, tag *NotableDayTag, layer *int, offset, take int) ([]StoredNotableDay, int, error) {
	entities, err := n.store.ListNotableDaysForYear(ctx, year, NormalizeRegionCode(regionCode), tag)
	if err != nil {
		return nil, 0, err
	}
	days := make([]StoredNotableDay, 0, len(entities))
	for i := range entities {
		day := storedNotableDayFromEntity(&entities[i])
		if layer != nil && GetMetaPriority(day.Meta) > *layer {
			continue
		}
		days = append(days, *day)
	}
	total := len(days)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []StoredNotableDay{}, total, nil
	}
	end := offset + take
	if take < 0 || end > total {
		end = total
	}
	return days[offset:end], total, nil
}

// SearchNotableDays filters the generated days of every year in the range.
func (n *NotableService) SearchNotableDays(ctx context.Context, regionCode string, startTime, endTime *time.Time, query string, tag *NotableDayTag, offset, take int) ([]NotableDay, int, error) {
	regionCode = NormalizeRegionCode(regionCode)
	all := make([]NotableDay, 0)
	for _, year := range searchYears(startTime, endTime) {
		days, err := n.GetNotableDays(ctx, year, regionCode, tag, nil)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, days...)
	}

	filtered := all[:0]
	for _, day := range all {
		instant := day.Date.Time()
		if startTime != nil && instant.Before(*startTime) {
			continue
		}
		if endTime != nil && instant.After(*endTime) {
			continue
		}
		if strings.TrimSpace(query) != "" && !matchesQuery(day, query) {
			continue
		}
		filtered = append(filtered, day)
	}
	sortNotableDays(filtered)

	total := len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []NotableDay{}, total, nil
	}
	end := offset + take
	if take < 0 || end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}

// ApplyPriorityFilter drops days whose meta priority is above the layer.
func ApplyPriorityFilter(days []NotableDay, maxPriority *int) []NotableDay {
	if maxPriority == nil {
		return days
	}
	out := make([]NotableDay, 0, len(days))
	for _, day := range days {
		if GetMetaPriority(day.Meta) <= *maxPriority {
			out = append(out, day)
		}
	}
	return out
}

// GetMetaPriority mirrors NotableDaysService.GetMetaPriority: a missing or
// non-numeric "priority" sorts below everything (3).
func GetMetaPriority(meta map[string]any) int {
	if meta == nil {
		return 3
	}
	value, ok := meta["priority"]
	if !ok {
		return 3
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return 3
}

// NormalizeRegionCode lower-cases a region, defaulting to "us" like C#.
func NormalizeRegionCode(regionCode string) string {
	trimmed := strings.TrimSpace(regionCode)
	if trimmed == "" {
		return "us"
	}
	return strings.ToLower(trimmed)
}

// BuildOccurrenceKey mirrors NotableDaysService.BuildOccurrenceKey.
func BuildOccurrenceKey(regionCode string, date time.Time, sourceIdentity string) string {
	return fmt.Sprintf("%s|%s|%s", NormalizeRegionCode(regionCode), date.UTC().Format("2006-01-02"), normalizeSourceIdentity(sourceIdentity))
}

func attachOccurrenceKey(day NotableDay, regionCode string) NotableDay {
	key := BuildOccurrenceKey(regionCode, day.Date.Time(), sourceIdentity(day))
	day.OccurrenceKey = &key
	return day
}

func sourceIdentity(day NotableDay) string {
	if day.LocalizableKey != nil && strings.TrimSpace(*day.LocalizableKey) != "" {
		return *day.LocalizableKey
	}
	if day.GlobalName != nil && strings.TrimSpace(*day.GlobalName) != "" {
		return *day.GlobalName
	}
	if day.LocalName != nil {
		return *day.LocalName
	}
	return day.Date.Time().UTC().Format("2006-01-02")
}

func parseOccurrenceKey(occurrenceKey string) (string, time.Time, bool) {
	parts := strings.SplitN(strings.TrimSpace(occurrenceKey), "|", 3)
	if len(parts) != 3 {
		return "", time.Time{}, false
	}
	date, err := time.Parse("2006-01-02", parts[1])
	if err != nil {
		return "", time.Time{}, false
	}
	return NormalizeRegionCode(parts[0]), date.UTC(), true
}

func normalizeSourceIdentity(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "|", "-")
}

func storedEntityToNotableDay(entity *NotableDayEntity) NotableDay {
	day := NotableDay{
		Date:           modelTime(entity.StartDate),
		LocalName:      entity.LocalName,
		GlobalName:     new(entity.Name),
		LocalizableKey: entity.LocalizableKey,
		CountryCode:    new(entity.Region),
		Description:    entity.Description,
		Holidays:       holidayTypes(entity.Tags),
		Tags:           []NotableDayTag{},
	}
	decodeJSON(entity.Meta, &day.Meta)
	decodeJSON(entity.Tags, &day.Tags)
	if day.Tags == nil {
		day.Tags = []NotableDayTag{}
	}
	return day
}

func generateRecurringDay(entity *NotableDayEntity, year int) *NotableDay {
	resolved, ok := resolveRecurrenceDate(entity, year)
	if !ok {
		return nil
	}
	tags := decodeTags(entity.Tags)
	day := NotableDay{
		Date:           modelTime(resolved),
		LocalName:      firstNonNil(entity.LocalName, new(entity.Name)),
		GlobalName:     new(entity.Name),
		LocalizableKey: entity.LocalizableKey,
		CountryCode:    new(entity.Region),
		Description:    entity.Description,
		Holidays:       holidayTypes(entity.Tags),
		Tags:           tags,
	}
	decodeJSON(entity.Meta, &day.Meta)
	return &day
}

func generatePeriodDays(entity *NotableDayEntity, year int) []NotableDay {
	festival, ok := resolveRecurrenceDate(entity, year)
	if !ok {
		return nil
	}
	referenceYear := entity.StartDate.UTC().Year()
	referenceFestival, ok := resolveRecurrenceDate(entity, referenceYear)
	if !ok {
		referenceFestival = entity.StartDate.UTC()
	}
	startOffset := int(entity.StartDate.UTC().Sub(referenceFestival).Hours() / 24)
	endOffset := int(entity.EndDate.UTC().Sub(referenceFestival).Hours() / 24)

	adjustedStart := festival.AddDate(0, 0, startOffset)
	adjustedEnd := festival.AddDate(0, 0, endOffset)
	if !adjustedEnd.After(adjustedStart) {
		adjustedEnd = adjustedStart.Add(24 * time.Hour)
	}

	isHoliday := containsTag(decodeTags(entity.Tags), NotableHoliday)
	days := make([]NotableDay, 0)
	for current := adjustedStart; current.Before(adjustedEnd); current = current.AddDate(0, 0, 1) {
		day := NotableDay{
			Date:           modelTime(current),
			LocalName:      firstNonNil(entity.LocalName, new(entity.Name)),
			GlobalName:     new(entity.Name),
			LocalizableKey: entity.LocalizableKey,
			CountryCode:    new(entity.Region),
			Description:    entity.Description,
			Holidays:       []NotableHolidayType{},
			Tags:           decodeTags(entity.Tags),
		}
		decodeJSON(entity.Meta, &day.Meta)
		if isHoliday {
			day.Holidays = []NotableHolidayType{HolidayPublic}
		}
		days = append(days, attachOccurrenceKey(day, entity.Region))
	}
	return days
}

// resolveRecurrenceDate resolves "MM-DD" against the requested year.
func resolveRecurrenceDate(entity *NotableDayEntity, year int) (time.Time, bool) {
	if entity.RecurrencePattern == nil {
		return time.Time{}, false
	}
	parts := strings.Split(strings.TrimSpace(*entity.RecurrencePattern), "-")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	month, errMonth := strconv.Atoi(parts[0])
	day, errDay := strconv.Atoi(parts[1])
	if errMonth != nil || errDay != nil || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	if isLunarPattern(entity) {
		return resolveLunarDate(year, month, day)
	}
	if day > daysInMonth(year, time.Month(month)) {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

func isLunarPattern(entity *NotableDayEntity) bool {
	if entity.Meta == nil {
		return false
	}
	var meta map[string]any
	decodeJSON(entity.Meta, &meta)
	value, ok := meta["calendar"]
	if !ok {
		return false
	}
	calendar, ok := value.(string)
	return ok && strings.EqualFold(calendar, "lunar")
}

// chineseNewYear anchors the lunar projection. The table covers 2020..2040;
// years outside it fall back to the solar month/day (clamped), which is the
// same degenerate approximation the C# code produced when no lunar calendar
// conversion was available.
var chineseNewYear = map[int]time.Time{
	2020: time.Date(2020, 1, 25, 0, 0, 0, 0, time.UTC),
	2021: time.Date(2021, 2, 12, 0, 0, 0, 0, time.UTC),
	2022: time.Date(2022, 2, 1, 0, 0, 0, 0, time.UTC),
	2023: time.Date(2023, 1, 22, 0, 0, 0, 0, time.UTC),
	2024: time.Date(2024, 2, 10, 0, 0, 0, 0, time.UTC),
	2025: time.Date(2025, 1, 29, 0, 0, 0, 0, time.UTC),
	2026: time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC),
	2027: time.Date(2027, 2, 6, 0, 0, 0, 0, time.UTC),
	2028: time.Date(2028, 1, 26, 0, 0, 0, 0, time.UTC),
	2029: time.Date(2029, 2, 13, 0, 0, 0, 0, time.UTC),
	2030: time.Date(2030, 2, 3, 0, 0, 0, 0, time.UTC),
	2031: time.Date(2031, 1, 23, 0, 0, 0, 0, time.UTC),
	2032: time.Date(2032, 2, 11, 0, 0, 0, 0, time.UTC),
	2033: time.Date(2033, 1, 31, 0, 0, 0, 0, time.UTC),
	2034: time.Date(2034, 2, 19, 0, 0, 0, 0, time.UTC),
	2035: time.Date(2035, 2, 8, 0, 0, 0, 0, time.UTC),
	2036: time.Date(2036, 1, 28, 0, 0, 0, 0, time.UTC),
	2037: time.Date(2037, 2, 15, 0, 0, 0, 0, time.UTC),
	2038: time.Date(2038, 2, 4, 0, 0, 0, 0, time.UTC),
	2039: time.Date(2039, 1, 24, 0, 0, 0, 0, time.UTC),
	2040: time.Date(2040, 2, 12, 0, 0, 0, 0, time.UTC),
}

// meanSynodicMonth projects a lunar month/day onto the solar calendar.
const meanSynodicMonth = 29.5306

func resolveLunarDate(year, lunarMonth, lunarDay int) (time.Time, bool) {
	// A lunar date near the year boundary can belong to the previous lunar
	// year, so both anchors are tried, like C# ResolveLunarDate.
	for _, lunarYear := range []int{year, year - 1} {
		anchor, ok := chineseNewYear[lunarYear]
		if !ok {
			continue
		}
		offset := int(math.Round(float64(lunarMonth-1)*meanSynodicMonth + float64(lunarDay-1)))
		solar := anchor.AddDate(0, 0, offset)
		if solar.Year() == year {
			return solar, true
		}
	}

	if _, ok := chineseNewYear[year]; !ok {
		if lunarDay > daysInMonth(year, time.Month(lunarMonth)) {
			lunarDay = daysInMonth(year, time.Month(lunarMonth))
		}
		return time.Date(year, time.Month(lunarMonth), lunarDay, 0, 0, 0, 0, time.UTC), true
	}
	return time.Time{}, false
}

// GetGlobalHolidays mirrors NotableDaysService.GetGlobalHolidays.
func GetGlobalHolidays(year int, regionCode string) []NotableDay {
	global := make([]NotableDay, 0, 3)
	global = append(global, attachOccurrenceKey(NotableDay{
		Date:           modelTime(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)),
		LocalName:      new("New Year's Day"),
		GlobalName:     new("New Year's Day"),
		LocalizableKey: new("NewYear"),
		Holidays:       []NotableHolidayType{HolidayPublic},
		Tags:           []NotableDayTag{NotableHoliday},
	}, regionCode))

	global = append(global, attachOccurrenceKey(NotableDay{
		Date:           modelTime(time.Date(year, 12, 25, 0, 0, 0, 0, time.UTC)),
		LocalName:      new("Christmas"),
		GlobalName:     new("Christmas"),
		LocalizableKey: new("Christmas"),
		Holidays:       []NotableHolidayType{HolidayPublic},
		Tags:           []NotableDayTag{NotableHoliday},
	}, regionCode))

	anniversary := year - 2024
	global = append(global, attachOccurrenceKey(NotableDay{
		Date:           modelTime(time.Date(year, 3, 16, 0, 0, 0, 0, time.UTC)),
		LocalName:      new(fmt.Sprintf("Solar Network %d 周年", anniversary)),
		GlobalName:     new(fmt.Sprintf("Solar Network %dth anniversary", anniversary)),
		LocalizableKey: new("Anniversary"),
		Holidays:       []NotableHolidayType{},
		Tags:           []NotableDayTag{NotableAnniversary},
	}, regionCode))
	return global
}

func searchYears(startTime, endTime *time.Time) []int {
	if startTime != nil || endTime != nil {
		start := startTime
		end := endTime
		if start == nil {
			start = endTime
		}
		if end == nil {
			end = startTime
		}
		first, last := start.UTC().Year(), end.UTC().Year()
		if last < first {
			first, last = last, first
		}
		years := make([]int, 0, last-first+1)
		for year := first; year <= last; year++ {
			years = append(years, year)
		}
		return years
	}
	current := time.Now().UTC().Year()
	return []int{current, current + 1}
}

func matchesQuery(day NotableDay, query string) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return true
	}
	contains := func(value *string) bool {
		return value != nil && strings.Contains(strings.ToLower(*value), strings.ToLower(trimmed))
	}
	return contains(day.LocalName) || contains(day.GlobalName) || contains(day.LocalizableKey) || contains(day.Description)
}

func sortNotableDays(days []NotableDay) {
	sort.SliceStable(days, func(i, j int) bool {
		left, right := days[i].Date.Time(), days[j].Date.Time()
		if !left.Equal(right) {
			return left.Before(right)
		}
		return optionalString(days[i].GlobalName) < optionalString(days[j].GlobalName)
	})
}

func holidayTypes(rawTags []byte) []NotableHolidayType {
	tags := decodeTags(rawTags)
	if containsTag(tags, NotableHoliday) {
		return []NotableHolidayType{HolidayPublic}
	}
	return []NotableHolidayType{}
}

func decodeTags(raw []byte) []NotableDayTag {
	tags := []NotableDayTag{}
	decodeJSON(raw, &tags)
	if tags == nil {
		tags = []NotableDayTag{}
	}
	return tags
}

func containsTag(tags []NotableDayTag, tag NotableDayTag) bool {
	for _, candidate := range tags {
		if candidate == tag {
			return true
		}
	}
	return false
}

func equalOptional(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstNonNil(values ...*string) *string {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func normalizeTagName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
