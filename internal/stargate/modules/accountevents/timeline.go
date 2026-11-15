package accounteventsmod

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"src.solsynth.dev/sosys/stargate/accountevents"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// pulsarTimelineTimeout bounds the cross-service read. The timeline is a
// public, unpaginated aggregation; a stalled Pulsar must not pin a request.
const pulsarTimelineTimeout = 10 * time.Second

// timelineAccounts supplies the account timeline's activity half from Pulsar.
// Pulsar owns presence rows; Stargate's presence_activities table is frozen
// legacy history that pulsar-migrate has already copied into active_sessions
// and scrobbles.
type timelineAccounts struct {
	*store.Store
	presence gen.DyPresenceServiceClient
	log      *slog.Logger
}

func newTimelineAccounts(store *store.Store, presence gen.DyPresenceServiceClient, log *slog.Logger) *timelineAccounts {
	if log == nil {
		log = slog.Default()
	}
	return &timelineAccounts{Store: store, presence: presence, log: log}
}

// presenceClient returns Pulsar's DyPresenceService client when the service is
// configured. A nil client leaves the timeline with statuses only.
func presenceClient(services *core.Services) gen.DyPresenceServiceClient {
	if services == nil || services.Clients == nil {
		return nil
	}
	return services.Clients.PulsarPresence
}

// ListActivitiesForTimeline reads live sessions and scrobble history from
// Pulsar. ListFriendsActivities is the only cross-service presence read the
// fleet exposes; its cursor widens the scrobble window, so an epoch cursor
// returns the account's full history instead of the default 24 hours.
// Per-type and per-response caps are raised because this endpoint, unlike a
// friend strip, has no cap of its own.
//
// A Pulsar outage yields no activity rows rather than falling back to the
// frozen Stargate table: that table stops at the migration and would serve
// stale sessions, including leases that were live when the migration ran.
func (a *timelineAccounts) ListActivitiesForTimeline(ctx context.Context, accountID uuid.UUID) ([]model.PresenceActivity, error) {
	if a.presence == nil {
		return []model.PresenceActivity{}, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, pulsarTimelineTimeout)
	defer cancel()
	response, err := a.presence.ListFriendsActivities(callCtx, &gen.DyListFriendsActivitiesRequest{
		AccountIds: []string{accountID.String()},
		MaxPerType: math.MaxInt32,
		Take:       math.MaxInt32,
		Cursor:     timestamppb.New(time.Unix(0, 0).UTC()),
	}, grpc.MaxCallRecvMsgSize(64<<20))
	if err != nil {
		a.log.Warn("account timeline: read pulsar activities", "account", accountID, "error", err)
		return []model.PresenceActivity{}, nil
	}

	activities := make([]model.PresenceActivity, 0, len(response.GetActivities()))
	seen := make(map[string]struct{}, len(response.GetActivities()))
	for _, raw := range response.GetActivities() {
		if raw == nil || raw.GetId() == "" {
			continue
		}
		if _, ok := seen[raw.GetId()]; ok {
			continue
		}
		seen[raw.GetId()] = struct{}{}
		activities = append(activities, model.ProtoToPresenceActivity(raw))
	}
	return activities, nil
}

var _ accountevents.AccountStore = (*timelineAccounts)(nil)
