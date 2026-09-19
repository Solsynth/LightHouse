// DyAuthService port of Padlock Auth/AuthServiceGrpc.cs.
package grpcserver

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

type dyAuthService struct {
	gen.UnimplementedDyAuthServiceServer
	d Deps
}

// Authenticate mirrors AuthServiceGrpc.Authenticate: validate the token via
// TokenAuthService, track authenticated activity, and return the session
// proto. Failures return valid=false + message — never an error status.
func (s *dyAuthService) Authenticate(ctx context.Context, req *gen.DyAuthenticateRequest) (*gen.DyAuthenticateResponse, error) {
	ip := ""
	if req.IpAddress != nil {
		ip = req.IpAddress.Value
	}
	valid, session, message, _ := s.d.Token.AuthenticateToken(ctx, req.Token, ip)
	if !valid || session == nil {
		msg := message
		if msg == "" {
			msg = "Authentication failed."
		}
		return &gen.DyAuthenticateResponse{Valid: false, Message: &msg}, nil
	}
	s.d.Auth.TrackAuthenticatedActivity(ctx, session, ip)
	return &gen.DyAuthenticateResponse{Valid: true, Session: auth.SessionToProto(session)}, nil
}

// ValidatePin mirrors AuthServiceGrpc.ValidatePin. The C# returns
// Valid=true when the account has no PIN factor enabled (the service throws
// InvalidOperationException, which the gRPC method converts into a success).
func (s *dyAuthService) ValidatePin(ctx context.Context, req *gen.DyValidatePinRequest) (*gen.DyValidateResponse, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	valid, err := s.d.Auth.ValidatePinCode(ctx, accountID.String(), req.Pin)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &gen.DyValidateResponse{Valid: true}, nil
		}
		return nil, err
	}
	return &gen.DyValidateResponse{Valid: valid}, nil
}

// ValidateCaptcha mirrors AuthServiceGrpc.ValidateCaptcha.
func (s *dyAuthService) ValidateCaptcha(ctx context.Context, req *gen.DyValidateCaptchaRequest) (*gen.DyValidateResponse, error) {
	valid, err := s.d.Auth.ValidateCaptcha(ctx, req.Token)
	if err != nil {
		return nil, err
	}
	return &gen.DyValidateResponse{Valid: valid}, nil
}

// GetOnlineDevices returns the account's devices with a live wsgateway
// connection, enriched with device identity and non-expired sessions.
func (s *dyAuthService) GetOnlineDevices(ctx context.Context, req *gen.DyGetOnlineDevicesRequest) (*gen.DyGetOnlineDevicesResponse, error) {
	accountID := ""
	if req != nil {
		accountID = strings.TrimSpace(req.AccountId)
	}
	if _, err := uuid.Parse(accountID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "account_id is required")
	}
	if s.d.Presence == nil {
		return nil, status.Error(codes.Unavailable, "device presence is not configured")
	}
	byAccount, err := s.d.Presence.ForAccounts(ctx, []string{accountID}, req.GetNamespace())
	if err != nil {
		if errors.Is(err, auth.ErrOnlineDevicesUnavailable) {
			return nil, status.Errorf(codes.Unavailable, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "resolve online devices: %v", err)
	}
	devices := byAccount[accountID]
	resp := &gen.DyGetOnlineDevicesResponse{Devices: make([]*gen.DyOnlineDevice, 0, len(devices))}
	for _, device := range devices {
		resp.Devices = append(resp.Devices, onlineDeviceToProto(device))
	}
	return resp, nil
}

// GetOnlineDevicesBatch returns the same per account. The response carries an
// entry for every requested account (empty list when offline).
func (s *dyAuthService) GetOnlineDevicesBatch(ctx context.Context, req *gen.DyGetOnlineDevicesBatchRequest) (*gen.DyGetOnlineDevicesBatchResponse, error) {
	if req == nil || len(req.AccountIds) == 0 {
		return nil, status.Error(codes.InvalidArgument, "account_ids is required")
	}
	if s.d.Presence == nil {
		return nil, status.Error(codes.Unavailable, "device presence is not configured")
	}
	byAccount, err := s.d.Presence.ForAccounts(ctx, req.AccountIds, req.GetNamespace())
	if err != nil {
		if errors.Is(err, auth.ErrOnlineDevicesUnavailable) {
			return nil, status.Errorf(codes.Unavailable, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "resolve online devices: %v", err)
	}
	resp := &gen.DyGetOnlineDevicesBatchResponse{Devices: make(map[string]*gen.DyOnlineDeviceList, len(byAccount))}
	for accountID, devices := range byAccount {
		list := &gen.DyOnlineDeviceList{Devices: make([]*gen.DyOnlineDevice, 0, len(devices))}
		for _, device := range devices {
			list.Devices = append(list.Devices, onlineDeviceToProto(device))
		}
		resp.Devices[accountID] = list
	}
	return resp, nil
}
