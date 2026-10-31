package accountevents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"src.solsynth.dev/sosys/stargate/model"
)

// SeedService idempotently installs the Chinese notable days. Existing rows
// are matched by name and refreshed, never deleted, so user-created rows and
// soft-deleted seed rows survive a redeploy.
type SeedService struct {
	store *Store
	log   *slog.Logger
}

// NewSeedService builds the notable-day seeder.
func NewSeedService(store *Store, log *slog.Logger) *SeedService {
	if log == nil {
		log = slog.Default()
	}
	return &SeedService{store: store, log: log}
}

// EnsureSeeded mirrors NotableDaysSeedService.EnsureSeededAsync.
func (s *SeedService) EnsureSeeded(ctx context.Context) error {
	seedData := ChineseHolidays()
	if len(seedData) == 0 {
		return nil
	}
	existing, err := s.store.ListNotableDayEntities(ctx, "CN", false)
	if err != nil {
		return err
	}
	byName := make(map[string]*NotableDayEntity, len(existing))
	for i := range existing {
		byName[existing[i].Name] = &existing[i]
	}

	added, updated := 0, 0
	for i := range seedData {
		seed := seedData[i]
		current, ok := byName[seed.Name]
		if !ok {
			if err := s.store.CreateNotableDay(ctx, &seed); err != nil {
				return err
			}
			added++
			continue
		}
		target := storedNotableDayFromEntity(current)
		if applySeedData(target, &seed) {
			if err := s.store.UpdateNotableDay(ctx, target); err != nil {
				return err
			}
			updated++
		}
	}

	if added > 0 || updated > 0 {
		s.log.Info("seeded Chinese notable days", "added", added, "updated", updated)
	}
	return nil
}

// applySeedData copies the seed fields onto an existing row and reports
// whether anything changed.
func applySeedData(target, source *StoredNotableDay) bool {
	changed := false
	assign := func(dst **string, value *string) {
		if !equalOptional(*dst, value) {
			*dst = value
			changed = true
		}
	}
	assign(&target.Description, source.Description)
	assign(&target.LocalName, source.LocalName)
	assign(&target.LocalizableKey, source.LocalizableKey)
	assign(&target.RecurrencePattern, source.RecurrencePattern)

	if !target.StartDate.Time().Equal(source.StartDate.Time()) {
		target.StartDate = source.StartDate
		changed = true
	}
	if !target.EndDate.Time().Equal(source.EndDate.Time()) {
		target.EndDate = source.EndDate
		changed = true
	}
	if !tagSlicesEqual(target.Tags, source.Tags) {
		target.Tags = source.Tags
		changed = true
	}
	if !metaEqual(target.Meta, source.Meta) {
		target.Meta = source.Meta
		changed = true
	}
	if target.IsRecurring != source.IsRecurring {
		target.IsRecurring = source.IsRecurring
		changed = true
	}
	if target.IsPeriod != source.IsPeriod {
		target.IsPeriod = source.IsPeriod
		changed = true
	}
	if !stringSlicesEqual(target.HolidayDays, source.HolidayDays) {
		target.HolidayDays = source.HolidayDays
		changed = true
	}
	if !intPtrEqual(target.DisplayOrder, source.DisplayOrder) {
		target.DisplayOrder = source.DisplayOrder
		changed = true
	}
	return changed
}

func tagSlicesEqual(left, right []NotableDayTag) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func intPtrEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func metaEqual(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || canonicalMetaValue(value) != canonicalMetaValue(other) {
			return false
		}
	}
	return true
}

// canonicalMetaValue normalizes numbers so a meta map decoded from jsonb
// (float64) compares equal to the seed's int values.
func canonicalMetaValue(value any) string {
	switch typed := value.(type) {
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	case float32:
		if typed == float32(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// ChineseHolidays mirrors ChineseHolidaysSeed.GetChineseHolidays. Anchor
// dates are the 2024 occurrences; every row is recurring, so the anchor year
// is never used as a filter.
func ChineseHolidays() []StoredNotableDay {
	meta := func(priority int, lunar bool) map[string]any {
		values := map[string]any{"priority": priority}
		if lunar {
			values["calendar"] = "lunar"
		}
		return values
	}
	row := func(
		name, local, key, description string,
		start, end time.Time,
		tags []NotableDayTag,
		meta map[string]any,
		pattern string,
		isPeriod bool,
		order int,
	) StoredNotableDay {
		return StoredNotableDay{
			Name:              name,
			LocalName:         new(local),
			LocalizableKey:    new(key),
			Description:       new(description),
			StartDate:         model.Time(start),
			EndDate:           model.Time(end),
			IsAllDay:          true,
			Region:            "CN",
			Tags:              tags,
			Meta:              meta,
			IsRecurring:       true,
			RecurrencePattern: new(pattern),
			IsPeriod:          isPeriod,
			HolidayDays:       []string{},
			DisplayOrder:      new(order),
		}
	}
	date := func(year int, month time.Month, day int) time.Time {
		return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	}

	return []StoredNotableDay{
		row("Spring Festival", "春节", "SpringFestival", "Chinese New Year, the most important traditional festival in China",
			date(2024, 2, 10), date(2024, 2, 17),
			[]NotableDayTag{NotableHoliday, NotableFestival}, meta(1, true), "01-01", true, 1),
		row("Qingming Festival", "清明节", "QingmingFestival", "Traditional festival for honoring ancestors and spring outings",
			date(2024, 4, 4), date(2024, 4, 7),
			[]NotableDayTag{NotableHoliday, NotableFestival}, meta(1, false), "04-04", true, 2),
		row("Labour Day", "劳动节", "LabourDay", "International Workers' Day holiday",
			date(2024, 5, 1), date(2024, 5, 6),
			[]NotableDayTag{NotableHoliday}, meta(1, false), "05-01", true, 3),
		row("Dragon Boat Festival", "端午节", "DragonBoatFestival", "Traditional festival with dragon boat races and zongzi",
			date(2024, 6, 8), date(2024, 6, 11),
			[]NotableDayTag{NotableHoliday, NotableFestival}, meta(1, true), "05-05", true, 4),
		row("Mid-Autumn Festival", "中秋节", "MidAutumnFestival", "Traditional festival for moon gazing and eating mooncakes",
			date(2024, 9, 15), date(2024, 9, 18),
			[]NotableDayTag{NotableHoliday, NotableFestival}, meta(1, true), "08-15", true, 5),
		row("National Day", "国庆节", "NationalDay", "Celebration of the founding of the People's Republic of China",
			date(2024, 10, 1), date(2024, 10, 8),
			[]NotableDayTag{NotableHoliday}, meta(1, false), "10-01", true, 6),
		row("New Year's Day", "元旦", "NewYear", "New Year's Day holiday",
			date(2024, 1, 1), date(2024, 1, 4),
			[]NotableDayTag{NotableHoliday}, meta(1, false), "01-01", true, 7),
		row("Lunar New Year's Eve", "除夕", "LunarNewYearEve", "The evening before Chinese New Year, family reunion dinner",
			date(2024, 2, 9), date(2024, 2, 10),
			[]NotableDayTag{NotableFestival}, meta(2, true), "12-30", false, 8),
		row("Lantern Festival", "元宵节", "LanternFestival", "End of Chinese New Year celebrations, lanterns and tangyuan",
			date(2024, 2, 24), date(2024, 2, 25),
			[]NotableDayTag{NotableFestival}, meta(2, true), "01-15", false, 9),
		row("Arbor Day", "植树节", "ArborDay", "National Tree Planting Day",
			date(2024, 3, 12), date(2024, 3, 13),
			[]NotableDayTag{NotableEvent}, meta(3, false), "03-12", false, 10),
		row("Youth Day", "五四青年节", "YouthDay", "Commemoration of the May Fourth Movement",
			date(2024, 5, 4), date(2024, 5, 5),
			[]NotableDayTag{NotableEvent, NotableMemorial}, meta(3, false), "05-04", false, 11),
		row("Children's Day", "儿童节", "ChildrenDay", "International Children's Day",
			date(2024, 6, 1), date(2024, 6, 2),
			[]NotableDayTag{NotableEvent}, meta(3, false), "06-01", false, 12),
		row("Teachers' Day", "教师节", "TeachersDay", "A day to honor teachers",
			date(2024, 9, 10), date(2024, 9, 11),
			[]NotableDayTag{NotableEvent}, meta(3, false), "09-10", false, 13),
		row("Qixi Festival", "七夕节", "QixiFestival", "Chinese Valentine's Day, the Cowherd and Weaver Girl festival",
			date(2024, 8, 10), date(2024, 8, 11),
			[]NotableDayTag{NotableFestival}, meta(2, true), "07-07", false, 14),
		row("Double Ninth Festival", "重阳节", "DoubleNinthFestival", "Traditional festival for respecting the elderly",
			date(2024, 10, 11), date(2024, 10, 12),
			[]NotableDayTag{NotableFestival}, meta(2, true), "09-09", false, 15),
	}
}
