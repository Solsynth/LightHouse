package sessions

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

type presenceServer struct {
	gen.UnimplementedDyPresenceServiceServer
	core *core.Services
}

func newPresenceServer(core *core.Services) *presenceServer {
	return &presenceServer{core: core}
}

// ListFriendsActivities answers DyPresenceService.ListFriendsActivities by querying
// active playback sessions and recent scrobbles for the requested accounts.
func (s *presenceServer) ListFriendsActivities(ctx context.Context, req *gen.DyListFriendsActivitiesRequest) (*gen.DyListFriendsActivitiesResponse, error) {
	accountIDs := parseAccountUUIDs(req.GetAccountIds())
	if len(accountIDs) == 0 {
		return &gen.DyListFriendsActivitiesResponse{}, nil
	}

	maxPerType := int(req.GetMaxPerType())
	if maxPerType <= 0 {
		maxPerType = 3
	}
	take := int(req.GetTake())
	if take <= 0 {
		take = 20
	}

	now := time.Now().UTC()
	var activities []*gen.DyPresenceActivity

	// 1. Fetch live active sessions for these accounts
	var liveSessions []model.ActiveSession
	err := s.core.DB.WithContext(ctx).
		Where("account_id IN ? AND lease_expires_at > ?", accountIDs, now).
		Order("updated_at DESC").
		Find(&liveSessions).Error
	if err == nil {
		for i := range liveSessions {
			activities = append(activities, sessionToProto(&liveSessions[i]))
		}
	}

	// 2. If needed, supplement with recent scrobbles (within past 24 hours)
	since := now.Add(-24 * time.Hour)
	if req.GetCursor() != nil {
		since = req.GetCursor().AsTime().UTC()
	}

	var scrobbles []model.Scrobble
	err = s.core.DB.WithContext(ctx).
		Where("account_id IN ? AND played_at >= ?", accountIDs, since).
		Order("played_at DESC").
		Limit(take * 2).
		Find(&scrobbles).Error
	if err == nil {
		for i := range scrobbles {
			activities = append(activities, scrobbleToProto(&scrobbles[i]))
		}
	}

	// Cap per activity type per account, then limit to take
	typeAccountCount := make(map[string]int)
	limited := make([]*gen.DyPresenceActivity, 0, len(activities))

	for _, act := range activities {
		typeKey := act.AccountId + ":"
		if act.Type != nil {
			typeKey += *act.Type
		}
		if typeAccountCount[typeKey] >= maxPerType {
			continue
		}
		typeAccountCount[typeKey]++
		limited = append(limited, act)
		if len(limited) >= take {
			break
		}
	}

	resp := &gen.DyListFriendsActivitiesResponse{
		Activities: limited,
	}
	if len(limited) == take && len(limited) > 0 {
		last := limited[len(limited)-1]
		if last.UpdatedAt != nil {
			resp.NextCursor = last.UpdatedAt
		}
	}

	return resp, nil
}

func parseAccountUUIDs(rawIDs []string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(rawIDs))
	seen := make(map[uuid.UUID]bool)
	for _, raw := range rawIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func sessionToProto(sess *model.ActiveSession) *gen.DyPresenceActivity {
	now := time.Now().UTC()
	isActive := !sess.IsPaused && sess.LeaseExpiresAt.After(now)

	domain := sess.Domain
	title := sess.Title
	manualID := sess.ManualID

	proto := &gen.DyPresenceActivity{
		Id:             sess.ID.String(),
		AccountId:      sess.AccountID.String(),
		Type:           &domain,
		ManualId:       &manualID,
		Title:          &title,
		Subtitle:       sess.Subtitle,
		Caption:        sess.Caption,
		LargeImage:     sess.ArtworkURL,
		SmallImage:     sess.SmallImage,
		LeaseMinutes:   int32(sess.LeaseMinutes),
		LeaseExpiresAt: timestamppb.New(sess.LeaseExpiresAt),
		IsActive:       isActive,
		StartedAt:      timestamppb.New(sess.StartedAt),
		CreatedAt:      timestamppb.New(sess.CreatedAt),
		UpdatedAt:      timestamppb.New(sess.UpdatedAt),
		Visibility:     gen.DyPresenceVisibility_DY_PRESENCE_VISIBILITY_PUBLIC,
	}

	if sess.CatalogID != nil {
		catStr := sess.CatalogID.String()
		proto.CatalogId = &catStr
	}
	if sess.EndedAt != nil {
		proto.EndedAt = timestamppb.New(*sess.EndedAt)
	}

	if len(sess.Meta) > 0 {
		var metaMap map[string]any
		if err := json.Unmarshal(sess.Meta, &metaMap); err == nil {
			if s, err := structpb.NewStruct(metaMap); err == nil {
				proto.Meta = s
			}
		}
	}

	return proto
}

func scrobbleToProto(scrobble *model.Scrobble) *gen.DyPresenceActivity {
	domain := scrobble.Domain
	title := scrobble.Title
	manualID := "scrobble:" + scrobble.ID.String()

	proto := &gen.DyPresenceActivity{
		Id:         scrobble.ID.String(),
		AccountId:  scrobble.AccountID.String(),
		Type:       &domain,
		ManualId:   &manualID,
		Title:      &title,
		Subtitle:   scrobble.Subtitle,
		Caption:    scrobble.Caption,
		LargeImage: scrobble.ArtworkURL,
		IsActive:   false,
		StartedAt:  timestamppb.New(scrobble.PlayedAt),
		EndedAt:    timestamppb.New(scrobble.PlayedAt.Add(time.Duration(scrobble.DurationSeconds) * time.Second)),
		CreatedAt:  timestamppb.New(scrobble.CreatedAt),
		UpdatedAt:  timestamppb.New(scrobble.CreatedAt),
		Visibility: gen.DyPresenceVisibility_DY_PRESENCE_VISIBILITY_PUBLIC,
	}

	if scrobble.CatalogID != nil {
		catStr := scrobble.CatalogID.String()
		proto.CatalogId = &catStr
	}

	if len(scrobble.Meta) > 0 {
		var metaMap map[string]any
		if err := json.Unmarshal(scrobble.Meta, &metaMap); err == nil {
			if s, err := structpb.NewStruct(metaMap); err == nil {
				proto.Meta = s
			}
		}
	}

	return proto
}
