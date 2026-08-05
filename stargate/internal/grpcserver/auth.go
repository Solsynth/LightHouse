// DyAuthService port of Padlock Auth/AuthServiceGrpc.cs.
package grpcserver

import (
	"context"
	"errors"

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
