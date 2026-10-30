// DyPresenceService, ported from Passport's PresenceServiceGrpc.
//
// Sphere's timeline service is the consumer: it asks for a page of recent
// presence activities for a set of accounts, capped per activity type so one
// account's music scrobbling cannot crowd out everyone else's. Ownership of
// the underlying tables moved to Stargate, so the RPC has to move with them.
package grpcserver

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

const (
	presenceDefaultMaxPerType = 3
	presenceDefaultTake       = 20
)

// presenceLookbackWindow bounds the scan. C# used a 24h window.
const presenceLookbackWindow = 24 * time.Hour

type dyPresenceService struct {
	gen.UnimplementedDyPresenceServiceServer
	d Deps
}

func newPresenceService(deps Deps) *dyPresenceService {
	return &dyPresenceService{d: deps}
}

// ListFriendsActivities mirrors PresenceServiceGrpc.ListFriendsActivities: the
// most recent activities per account inside the lookback window, capped at
// max_per_type per activity type, ordered by update time and paged by an
// updated_at cursor.
func (s *dyPresenceService) ListFriendsActivities(ctx context.Context, req *gen.DyListFriendsActivitiesRequest) (*gen.DyListFriendsActivitiesResponse, error) {
	if s == nil || s.d.Store == nil {
		return nil, status.Errorf(codes.Unimplemented, "method ListFriendsActivities not implemented")
	}

	accountIDs := parsePresenceAccountIDs(req.GetAccountIds())
	if len(accountIDs) == 0 {
		return &gen.DyListFriendsActivitiesResponse{}, nil
	}

	maxPerType := int(req.GetMaxPerType())
	if maxPerType <= 0 {
		maxPerType = presenceDefaultMaxPerType
	}
	take := int(req.GetTake())
	if take <= 0 {
		take = presenceDefaultTake
	}

	since := time.Now().UTC().Add(-presenceLookbackWindow)
	var cursor *time.Time
	if req.GetCursor() != nil {
		at := req.GetCursor().AsTime().UTC()
		cursor = &at
	}

	activities, err := s.d.Store.ListRecentPresenceActivities(ctx, accountIDs, since, cursor)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list presence activities: %v", err)
	}

	// Cap per type, then page. Grouping preserves the store's updated_at
	// ordering within each type, which is what the C# GroupBy().Take() did.
	perType := make(map[string]int, len(activities))
	limited := make([]model.PresenceActivity, 0, len(activities))
	for _, activity := range activities {
		key := ""
		if activity.Type != nil {
			key = *activity.Type
		}
		if perType[key] >= maxPerType {
			continue
		}
		perType[key]++
		limited = append(limited, activity)
	}
	if len(limited) > take {
		limited = limited[:take]
	}

	resp := &gen.DyListFriendsActivitiesResponse{}
	for i := range limited {
		resp.Activities = append(resp.Activities, presenceActivityToProto(&limited[i]))
	}
	if len(limited) == take && len(limited) > 0 {
		last := limited[len(limited)-1]
		if last.UpdatedAt != nil {
			resp.NextCursor = timestamppb.New(time.Time(*last.UpdatedAt).UTC())
		}
	}
	return resp, nil
}

func parsePresenceAccountIDs(ids []string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]struct{}, len(ids))
	for _, raw := range ids {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// presenceActivityToProto mirrors SnPresenceActivity.ToProtoValue, including
// the quirk that provider/reference_id/queryable_terms travel inside `meta`
// (older consumers read them from there) and the synthesized ended_at for an
// expired-but-not-ended activity.
func presenceActivityToProto(activity *model.PresenceActivity) *gen.DyPresenceActivity {
	now := time.Now().UTC()
	var leaseExpiresAt time.Time
	if activity.LeaseExpiresAt != nil {
		leaseExpiresAt = time.Time(*activity.LeaseExpiresAt).UTC()
	}
	isActive := leaseExpiresAt.After(now) && activity.DeletedAt == nil

	proto := &gen.DyPresenceActivity{
		Id:             activity.Id,
		AccountId:      activity.AccountId,
		LeaseMinutes:   int32(activity.LeaseMinutes),
		IsActive:       isActive,
		Visibility:     gen.DyPresenceVisibility(activity.Visibility),
		LeaseExpiresAt: timestamppb.New(leaseExpiresAt),
	}
	if activity.Type != nil {
		proto.Type = activity.Type
	}
	if activity.StartedAt != nil {
		proto.StartedAt = timestamppb.New(time.Time(*activity.StartedAt).UTC())
	}
	if activity.EndedAt != nil {
		proto.EndedAt = timestamppb.New(time.Time(*activity.EndedAt).UTC())
	}
	if activity.CatalogId != nil {
		proto.CatalogId = activity.CatalogId
	}
	if activity.CreatedAt != nil {
		proto.CreatedAt = timestamppb.New(time.Time(*activity.CreatedAt).UTC())
	}
	if activity.UpdatedAt != nil {
		proto.UpdatedAt = timestamppb.New(time.Time(*activity.UpdatedAt).UTC())
	}
	if activity.DeletedAt != nil {
		proto.DeletedAt = timestamppb.New(time.Time(*activity.DeletedAt).UTC())
	}

	for _, tag := range activity.Tags {
		proto.Tags = append(proto.Tags, &gen.DyPresenceTag{Slug: tag.Slug, Name: tag.Name})
	}

	proto.ManualId = activity.ManualId
	proto.Title = activity.Title
	proto.Subtitle = activity.Subtitle
	proto.Caption = activity.Caption
	proto.LargeImage = activity.LargeImage
	proto.SmallImage = activity.SmallImage
	proto.TitleUrl = activity.TitleUrl
	proto.SubtitleUrl = activity.SubtitleUrl

	fields := map[string]*structpb.Value{}
	for key, value := range activity.Meta {
		if converted, err := structpb.NewValue(value); err == nil {
			fields[key] = converted
		}
	}
	if activity.Provider != nil && strings.TrimSpace(*activity.Provider) != "" {
		fields["provider"] = structpb.NewStringValue(*activity.Provider)
	}
	if activity.ReferenceId != nil && strings.TrimSpace(*activity.ReferenceId) != "" {
		fields["reference_id"] = structpb.NewStringValue(*activity.ReferenceId)
	}
	if len(activity.QueryableTerms) > 0 {
		values := make([]*structpb.Value, 0, len(activity.QueryableTerms))
		for _, term := range activity.QueryableTerms {
			values = append(values, structpb.NewStringValue(term))
		}
		fields["queryable_terms"] = structpb.NewListValue(&structpb.ListValue{Values: values})
	}
	if len(fields) > 0 {
		proto.Meta = &structpb.Struct{Fields: fields}
	}

	if !isActive && activity.EndedAt == nil {
		end := leaseExpiresAt
		if activity.DeletedAt != nil {
			end = time.Time(*activity.DeletedAt).UTC()
		}
		proto.EndedAt = timestamppb.New(end)
	}
	return proto
}
