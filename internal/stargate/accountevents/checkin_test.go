package accountevents

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
)

type profileStub struct{ profile *model.Profile }

func (s profileStub) GetAccountByID(ctx context.Context, id uuid.UUID) (*model.Account, error) {
	return nil, nil
}
func (s profileStub) GetAccountByName(ctx context.Context, name string) (*model.Account, error) {
	return nil, nil
}
func (s profileStub) GetOrCreateAccountProfile(ctx context.Context, id uuid.UUID) (*model.Profile, error) {
	return s.profile, nil
}
func (s profileStub) GetAccountsByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Account, error) {
	return nil, nil
}
func (s profileStub) ListRelatedAccountIDs(ctx context.Context, id uuid.UUID, status model.RelationshipStatus, related bool) ([]string, error) {
	return nil, nil
}
func (s profileStub) ListStatusesBetween(ctx context.Context, id uuid.UUID, from, to time.Time) ([]model.AccountStatus, error) {
	return nil, nil
}
func (s profileStub) ListStatusesForTimeline(ctx context.Context, id uuid.UUID) ([]model.AccountStatus, error) {
	return nil, nil
}
func (s profileStub) ListActivitiesForTimeline(ctx context.Context, id uuid.UUID) ([]model.PresenceActivity, error) {
	return nil, nil
}

// TestIsBirthdayRespectsPreferredTimeZone pins the birthday detection to the
// profile-displayed calendar date (models.Time serializes as UTC) compared
// against "today" resolved in the account's preferred zone: a May 5 birthday
// must fire on May 5 local, including for western-hemisphere zones where
// shifting the stored UTC instant into the zone moves it to May 4.
func TestIsBirthdayRespectsPreferredTimeZone(t *testing.T) {
	birthday := model.NewTime(time.Date(2000, 5, 5, 0, 0, 0, 0, time.UTC))
	zone := "America/New_York"
	service := NewService(Deps{
		Accounts: profileStub{profile: &model.Profile{Birthday: birthday, TimeZone: &zone}},
		Now:      func() time.Time { return time.Unix(0, 0).UTC() },
	})

	cases := []struct {
		now   time.Time
		local string
		want  bool
	}{
		// May 5 08:00 EDT: birthday day in the profile zone.
		{time.Date(2000, 5, 5, 12, 0, 0, 0, time.UTC), "May 5 08:00 EDT", true},
		// May 5 22:00 EDT: still the local 5th.
		{time.Date(2000, 5, 6, 2, 0, 0, 0, time.UTC), "May 5 22:00 EDT", true},
		// May 4 23:00 EDT: the local 4th, not the birthday.
		{time.Date(2000, 5, 5, 3, 0, 0, 0, time.UTC), "May 4 23:00 EDT", false},
		// May 6 00:00 EDT: the local 6th.
		{time.Date(2000, 5, 6, 4, 0, 0, 0, time.UTC), "May 6 00:00 EDT", false},
	}
	for _, tc := range cases {
		if got := service.isBirthday(context.Background(), uuid.New(), tc.now); got != tc.want {
			t.Errorf("isBirthday at %s = %v, want %v", tc.local, got, tc.want)
		}
	}
}

// TestIsBirthdayFiresAfterLocalMidnight checks the eastern-hemisphere side:
// the detection must fire once the preferred zone crosses into the birthday
// date, not before.
func TestIsBirthdayFiresAfterLocalMidnight(t *testing.T) {
	birthday := model.NewTime(time.Date(2000, 5, 5, 0, 0, 0, 0, time.UTC))
	zone := "Asia/Shanghai"
	service := NewService(Deps{
		Accounts: profileStub{profile: &model.Profile{Birthday: birthday, TimeZone: &zone}},
	})

	if !service.isBirthday(context.Background(), uuid.New(), time.Date(2000, 5, 4, 18, 0, 0, 0, time.UTC)) {
		t.Error("isBirthday at May 5 02:00 CST should be true")
	}
	if service.isBirthday(context.Background(), uuid.New(), time.Date(2000, 5, 4, 14, 0, 0, 0, time.UTC)) {
		t.Error("isBirthday at May 4 22:00 CST should be false")
	}
}
