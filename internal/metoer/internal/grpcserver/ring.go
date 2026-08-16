// Package grpcserver implements Metoer's inbound gRPC servers — the
// DyRingService surface the C# fleet calls via services__ring__grpc__0 plus
// the static capability registry and the standard health service. Every
// method is a port of DysonNetwork.Ring/Services/RingServiceGrpc.cs against
// the pinned Golaunch generated interfaces (src.solsynth.dev/sosys/go/proto).
//
// Error mapping mirrors ASP.NET Core gRPC's default exception mapping:
// ArgumentException (the empty-notification guard) → InvalidArgument,
// everything else → Unknown. Invalid user ids and nil messages return
// errors instead of panicking (a panic would crash the process).
package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/metoer/internal/email"
	"src.solsynth.dev/sosys/metoer/internal/push"
	"src.solsynth.dev/sosys/metoer/internal/queue"
)

// Deps carries the services the DyRingService implementation needs.
type Deps struct {
	Queue *queue.Service
	Push  *push.Service
	Email *email.Service
	Log   *slog.Logger
}

// ringService is the DyRingService.DyRingServiceBase port.
type ringService struct {
	gen.UnimplementedDyRingServiceServer
	d Deps
}

// Register mounts DyRingService, DyCapabilitiesService, gRPC reflection and
// the standard gRPC health service on the given server.
func Register(s *grpc.Server, deps Deps) {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	gen.RegisterDyRingServiceServer(s, &ringService{d: deps})
	gen.RegisterDyCapabilitiesServiceServer(s, &dyCapabilitiesService{})
	reflection.Register(s)

	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, hs)
}

// SendEmail mirrors RingServiceGrpc.SendEmail.
func (s *ringService) SendEmail(ctx context.Context, req *gen.DySendEmailRequest) (*emptypb.Empty, error) {
	if req == nil || req.Email == nil {
		return nil, status.Error(codes.InvalidArgument, "email message is required")
	}
	if err := s.d.Queue.EnqueueEmail(ctx, req.Email.ToName, req.Email.ToAddress, req.Email.Subject, req.Email.Body); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// SendPushNotificationToUser mirrors RingServiceGrpc.SendPushNotificationToUser.
func (s *ringService) SendPushNotificationToUser(ctx context.Context, req *gen.DySendPushNotificationToUserRequest) (*emptypb.Empty, error) {
	if req == nil || req.Notification == nil {
		return nil, status.Error(codes.InvalidArgument, "notification is required")
	}
	userID, err := uuid.Parse(req.UserId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "user id is not a valid UUID")
	}
	appId := s.d.Push.ResolveAppId(notificationAppId(req.Notification), true)
	if err := s.sendNotification(ctx, userID, req.Notification, appId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// SendPushNotificationToUsers mirrors RingServiceGrpc.SendPushNotificationToUsers.
func (s *ringService) SendPushNotificationToUsers(ctx context.Context, req *gen.DySendPushNotificationToUsersRequest) (*emptypb.Empty, error) {
	if req == nil || req.Notification == nil {
		return nil, status.Error(codes.InvalidArgument, "notification is required")
	}
	// The C# materializes the id list first (userIds.Select(Guid.Parse)
	// .ToList()), so an invalid id fails before any send starts.
	userIDs := make([]uuid.UUID, 0, len(req.UserIds))
	for _, raw := range req.UserIds {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "user id is not a valid UUID")
		}
		userIDs = append(userIDs, id)
	}
	appId := s.d.Push.ResolveAppId(notificationAppId(req.Notification), true)
	var wg sync.WaitGroup
	errCh := make(chan error, len(userIDs))
	for _, userID := range userIDs {
		wg.Add(1)
		go func(userID uuid.UUID) {
			defer wg.Done()
			if err := s.sendNotification(ctx, userID, req.Notification, appId); err != nil {
				errCh <- err
			}
		}(userID)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}

// sendNotification runs the shared SendNotification mapping (the C#
// ArgumentException → InvalidArgument conversion).
func (s *ringService) sendNotification(ctx context.Context, userID uuid.UUID, notification *gen.DyPushNotification, appId string) error {
	err := s.d.Push.SendNotification(
		ctx,
		userID,
		notification.Topic,
		optionalString(notification.Title),
		optionalString(notification.Subtitle),
		optionalString(notification.Body),
		parseMeta(notification),
		optionalStringPtr(notification.ActionUri),
		notification.IsSilent,
		notification.IsSavable,
		appId,
		optionalStringValue(notification.PushType),
	)
	if errors.Is(err, push.ErrEmptyNotification) || errors.Is(err, push.ErrUnknownAppId) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return err
}

// UnsubscribePushNotifications mirrors RingServiceGrpc.UnsubscribePushNotifications.
func (s *ringService) UnsubscribePushNotifications(ctx context.Context, req *gen.DyUnsubscribePushNotificationsRequest) (*emptypb.Empty, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "device id is required")
	}
	if err := s.d.Push.UnsubscribeDevice(ctx, req.DeviceId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// notificationAppId mirrors the C# HasAppId check.
func notificationAppId(notification *gen.DyPushNotification) string {
	if notification == nil || notification.AppId == nil {
		return ""
	}
	return notification.GetAppId()
}

// optionalString mirrors the C# proto3 string pass-through: the raw value
// (possibly "") is forwarded as a pointer — the C# SendNotification
// receives "" (not null), which the empty-check distinguishes.
func optionalString(v string) *string { return &v }

// optionalStringValue mirrors the C# HasPushType check: the proto oneof is
// nil when unset.
func optionalStringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// optionalStringPtr mirrors the C# HasActionUri check (oneof).
func optionalStringPtr(v *string) *string { return v }

// parseMeta mirrors InfraObjectCoder.ConvertByteStringToObject<Dictionary<
// string, object?>>: empty/missing → empty dict.
func parseMeta(notification *gen.DyPushNotification) map[string]any {
	if notification == nil || len(notification.Meta) == 0 {
		return map[string]any{}
	}
	var meta map[string]any
	if err := json.Unmarshal(notification.Meta, &meta); err != nil {
		return map[string]any{}
	}
	if meta == nil {
		return map[string]any{}
	}
	return meta
}
