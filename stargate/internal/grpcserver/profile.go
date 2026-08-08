package grpcserver

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
)

// dyProfileService exposes the read-side profile service used by the C# fleet
// (including Messager). Account/profile ownership moved to Stargate, while
// Passport still owns badge mutation and board routes. Keep the account reads
// on both generated service names compatible: Messager calls DyProfileService,
// whereas some services call DyAccountService directly.
type dyProfileService struct {
	gen.UnimplementedDyProfileServiceServer
	d Deps
}

// GetAccount mirrors the profile-service account read and delegates to the
// shared account implementation, which hydrates account_profiles before
// serializing DyAccount.Profile.ActiveBadge.
func (s *dyProfileService) GetAccount(ctx context.Context, req *gen.DyGetAccountRequest) (*gen.DyAccount, error) {
	accounts := &dyAccountService{d: s.d}
	return accounts.GetAccount(ctx, req)
}

// GetProfile returns the Stargate-owned profile row, including the active badge
// reference converted by auth.ProfileToProto.
func (s *dyProfileService) GetProfile(ctx context.Context, req *gen.DyGetProfileRequest) (*gen.DyAccountProfile, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	profile, err := s.d.Store.GetOrCreateAccountProfile(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return auth.ProfileToProto(profile), nil
}
