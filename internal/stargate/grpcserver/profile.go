package grpcserver

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/auth"
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

// The batch and lookup reads carry the same contract as the single read:
// Passport's DyProfileService implemented them, and the fleet's shared
// RemoteAccountService (Messager, Sphere) calls them by name. Missing any of
// them leaves every hydrated account reference null on the wire.

func (s *dyProfileService) GetBotAccount(ctx context.Context, req *gen.DyGetBotAccountRequest) (*gen.DyAccount, error) {
	accounts := &dyAccountService{d: s.d}
	return accounts.GetBotAccount(ctx, req)
}

func (s *dyProfileService) GetAccountBatch(ctx context.Context, req *gen.DyGetAccountBatchRequest) (*gen.DyGetAccountBatchResponse, error) {
	accounts := &dyAccountService{d: s.d}
	return accounts.GetAccountBatch(ctx, req)
}

func (s *dyProfileService) GetBotAccountBatch(ctx context.Context, req *gen.DyGetBotAccountBatchRequest) (*gen.DyGetAccountBatchResponse, error) {
	accounts := &dyAccountService{d: s.d}
	return accounts.GetBotAccountBatch(ctx, req)
}

func (s *dyProfileService) LookupAccountBatch(ctx context.Context, req *gen.DyLookupAccountBatchRequest) (*gen.DyGetAccountBatchResponse, error) {
	accounts := &dyAccountService{d: s.d}
	return accounts.LookupAccountBatch(ctx, req)
}

func (s *dyProfileService) SearchAccount(ctx context.Context, req *gen.DySearchAccountRequest) (*gen.DyGetAccountBatchResponse, error) {
	accounts := &dyAccountService{d: s.d}
	return accounts.SearchAccount(ctx, req)
}

func (s *dyProfileService) ListAccounts(ctx context.Context, req *gen.DyListAccountsRequest) (*gen.DyListAccountsResponse, error) {
	accounts := &dyAccountService{d: s.d}
	return accounts.ListAccounts(ctx, req)
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

// ── relationship RPCs (owned by the relationships module) ────────────────
//
// These six methods carry the social graph. They live in relationship.go and
// are owned by the relationships module: when it is disabled the read service
// is nil and every method reports Unimplemented, so a deployment without the
// module serves exactly the unimplemented surface it did before the port.

func (s *dyProfileService) GetRelationship(ctx context.Context, req *gen.DyGetRelationshipRequest) (*gen.DyGetRelationshipResponse, error) {
	return newRelationshipRPCs(s.d).GetRelationship(ctx, req)
}

func (s *dyProfileService) HasRelationship(ctx context.Context, req *gen.DyGetRelationshipRequest) (*wrapperspb.BoolValue, error) {
	return newRelationshipRPCs(s.d).HasRelationship(ctx, req)
}

func (s *dyProfileService) ListFriends(ctx context.Context, req *gen.DyListRelationshipSimpleRequest) (*gen.DyListRelationshipSimpleResponse, error) {
	return newRelationshipRPCs(s.d).ListFriends(ctx, req)
}

func (s *dyProfileService) ListBlocked(ctx context.Context, req *gen.DyListRelationshipSimpleRequest) (*gen.DyListRelationshipSimpleResponse, error) {
	return newRelationshipRPCs(s.d).ListBlocked(ctx, req)
}

func (s *dyProfileService) ListMuted(ctx context.Context, req *gen.DyListRelationshipSimpleRequest) (*gen.DyListRelationshipSimpleResponse, error) {
	return newRelationshipRPCs(s.d).ListMuted(ctx, req)
}

func (s *dyProfileService) ListCloseFriends(ctx context.Context, req *gen.DyListRelationshipSimpleRequest) (*gen.DyListRelationshipSimpleResponse, error) {
	return newRelationshipRPCs(s.d).ListCloseFriends(ctx, req)
}

// ── account-status RPCs (owned by the presences module) ──────────────────
//
// The two status reads live in accountstatus.go and are owned by the presences
// module: when it is disabled the resolver is nil and both methods report
// Unimplemented.

func (s *dyProfileService) GetAccountStatus(ctx context.Context, req *gen.DyGetAccountRequest) (*gen.DyAccountStatus, error) {
	return newAccountStatusRPCs(s.d).GetAccountStatus(ctx, req)
}

func (s *dyProfileService) GetAccountStatusBatch(ctx context.Context, req *gen.DyGetAccountBatchRequest) (*gen.DyGetAccountStatusBatchResponse, error) {
	return newAccountStatusRPCs(s.d).GetAccountStatusBatch(ctx, req)
}
