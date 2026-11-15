package accounteventsmod

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

type stubPresence struct {
	request  *gen.DyListFriendsActivitiesRequest
	response *gen.DyListFriendsActivitiesResponse
	err      error
}

func (s *stubPresence) ListFriendsActivities(_ context.Context, in *gen.DyListFriendsActivitiesRequest, _ ...grpc.CallOption) (*gen.DyListFriendsActivitiesResponse, error) {
	s.request = in
	return s.response, s.err
}

// TestTimelineReadsPulsarOnly pins the ownership boundary: the account
// timeline takes live sessions and scrobble history from Pulsar and widens the
// request past Pulsar's 24-hour scrobble window.
func TestTimelineReadsPulsarOnly(t *testing.T) {
	account := uuid.New()
	presence := &stubPresence{response: &gen.DyListFriendsActivitiesResponse{
		Activities: []*gen.DyPresenceActivity{
			{Id: uuid.NewString(), AccountId: account.String(), Title: new("fresh")},
		},
	}}
	accounts := newTimelineAccounts(nil, presence, nil)

	activities, err := accounts.ListActivitiesForTimeline(context.Background(), account)
	if err != nil {
		t.Fatalf("ListActivitiesForTimeline: %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("activities = %d, want the Pulsar row", len(activities))
	}
	if activities[0].Title == nil || *activities[0].Title != "fresh" {
		t.Fatalf("activities[0] = %+v, want the Pulsar row", activities[0])
	}
	if presence.request == nil {
		t.Fatal("Pulsar presence was not queried")
	}
	if presence.request.GetCursor() == nil || presence.request.GetCursor().AsTime().After(time.Unix(0, 0)) {
		t.Fatalf("cursor = %v, want the start of history", presence.request.GetCursor())
	}
	if presence.request.GetTake() <= 0 || presence.request.GetMaxPerType() <= 0 {
		t.Fatalf("caps = take %d, maxPerType %d; want uncapped", presence.request.GetTake(), presence.request.GetMaxPerType())
	}
}

// TestTimelineDoesNotFallBackToLegacy pins the degradation path: a presence
// outage must not resurrect the frozen table, which would show stale leases.
func TestTimelineDoesNotFallBackToLegacy(t *testing.T) {
	presence := &stubPresence{err: errors.New("unavailable")}
	accounts := newTimelineAccounts(nil, presence, nil)

	activities, err := accounts.ListActivitiesForTimeline(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ListActivitiesForTimeline: %v", err)
	}
	if len(activities) != 0 {
		t.Fatalf("activities = %d, want no legacy fallback", len(activities))
	}
}

// TestTimelineWithoutPulsarHasNoActivities pins the unconfigured case.
func TestTimelineWithoutPulsarHasNoActivities(t *testing.T) {
	accounts := newTimelineAccounts(nil, nil, nil)

	activities, err := accounts.ListActivitiesForTimeline(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ListActivitiesForTimeline: %v", err)
	}
	if len(activities) != 0 {
		t.Fatalf("activities = %d, want no legacy fallback", len(activities))
	}
}
