package broadcast

import (
	"testing"
	"time"

	"src.solsynth.dev/sosys/stargate/model"
)

// statusFixture is a fully populated broadcast-visible status: every field the
// comparator inspects is set so each subtest can perturb exactly one of them.
func statusFixture() *model.AccountStatus {
	return &model.AccountStatus{
		Id:           "11111111-1111-1111-1111-111111111111",
		AccountId:    "22222222-2222-2222-2222-222222222222",
		Attitude:     model.StatusAttitudeNeutral,
		Type:         model.StatusTypeBusy,
		Label:        new("Working"),
		Symbol:       new("💼"),
		IsOnline:     true,
		IsIdle:       true,
		IdleSince:    model.NewTime(time.Date(2026, 8, 6, 6, 29, 50, 0, time.UTC)),
		IsCustomized: true,
	}
}

func TestStatusesEqual(t *testing.T) {
	base := statusFixture()

	if !StatusesEqual(base, statusFixture()) {
		t.Fatal("identical statuses must compare equal")
	}

	// A nil side never compares equal: the first status an account gets (or
	// the loss of its last one) always broadcasts.
	if StatusesEqual(nil, base) || StatusesEqual(base, nil) || StatusesEqual(nil, nil) {
		t.Fatal("a nil status must never compare equal")
	}

	// Read-time fields that friends can see but which are not in the C#
	// comparator must not suppress a broadcast.
	extra := statusFixture()
	extra.IsInvisible = true
	extra.IsNotDisturb = true
	extra.OnlineDevices = []model.OnlineDevice{{Id: "d1"}}
	extra.Meta = map[string]any{"k": "v"}
	if !StatusesEqual(base, extra) {
		t.Fatal("fields outside BroadcastEventHandler.StatusesEqual must not affect the comparison")
	}

	cases := []struct {
		name   string
		mutate func(*model.AccountStatus)
	}{
		{"attitude", func(s *model.AccountStatus) { s.Attitude = model.StatusAttitudePositive }},
		{"is_online", func(s *model.AccountStatus) { s.IsOnline = false }},
		{"is_idle", func(s *model.AccountStatus) { s.IsIdle = false }},
		{"idle_since", func(s *model.AccountStatus) {
			s.IdleSince = model.NewTime(time.Date(2026, 8, 6, 6, 30, 50, 0, time.UTC))
		}},
		{"idle_since_cleared", func(s *model.AccountStatus) { s.IdleSince = nil }},
		{"is_customized", func(s *model.AccountStatus) { s.IsCustomized = false }},
		{"label", func(s *model.AccountStatus) { s.Label = new("Offline") }},
		{"label_cleared", func(s *model.AccountStatus) { s.Label = nil }},
		{"symbol", func(s *model.AccountStatus) { s.Symbol = new("🌙") }},
		{"symbol_cleared", func(s *model.AccountStatus) { s.Symbol = nil }},
		{"type", func(s *model.AccountStatus) { s.Type = model.StatusTypeInvisible }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := statusFixture()
			tc.mutate(changed)
			if StatusesEqual(base, changed) {
				t.Fatalf("a differing %s must compare unequal", tc.name)
			}
		})
	}
}
