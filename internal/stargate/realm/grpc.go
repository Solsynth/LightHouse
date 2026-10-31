package realm

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/localization"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// GRPCDeps is everything the DyRealmService port needs.
type GRPCDeps struct {
	Service *Service
	Store   *Store
	// Accounts hydrates the member payloads the fleet reads (Messager's member
	// cards, Sphere's publisher listings).
	Accounts AccountLookup
	// Push sends the realm invitation notification.
	Push Push
}

// Push sends a notification to one account; it is the Ring push port.
type Push interface {
	SendToUser(ctx context.Context, accountID, title, body string) error
}

// grpcRealmService is the DyRealmService port of Passport's RealmServiceGrpc.
type grpcRealmService struct {
	gen.UnimplementedDyRealmServiceServer
	d GRPCDeps
}

// NewGRPCService builds the DyRealmService implementation.
func NewGRPCService(d GRPCDeps) gen.DyRealmServiceServer { return &grpcRealmService{d: d} }

func (s *grpcRealmService) GetRealm(ctx context.Context, req *gen.DyGetRealmRequest) (*gen.DyRealm, error) {
	var entity *RealmEntity
	var err error
	switch query := req.GetQuery().(type) {
	case *gen.DyGetRealmRequest_Id:
		if query.Id == "" {
			return nil, status.Error(codes.InvalidArgument, "Must provide either id or slug")
		}
		id, parseErr := uuid.Parse(query.Id)
		if parseErr != nil {
			return nil, status.Error(codes.InvalidArgument, "realm id must be a uuid")
		}
		entity, err = s.d.Store.GetRealm(ctx, id)
	case *gen.DyGetRealmRequest_Slug:
		if query.Slug == "" {
			return nil, status.Error(codes.InvalidArgument, "Must provide either id or slug")
		}
		entity, err = s.d.Store.GetRealmBySlug(ctx, query.Slug)
	default:
		return nil, status.Error(codes.InvalidArgument, "Must provide either id or slug")
	}
	if errors.Is(err, ErrNotFound) {
		return nil, status.Error(codes.NotFound, "Realm not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if _, err := s.d.Service.RefreshBoost(ctx, entity); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return realmToProto(entity), nil
}

func (s *grpcRealmService) GetRealmBatch(ctx context.Context, req *gen.DyGetRealmBatchRequest) (*gen.DyGetRealmBatchResponse, error) {
	ids := make([]uuid.UUID, 0, len(req.GetIds()))
	for _, raw := range req.GetIds() {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, id)
		}
	}
	entities, err := s.d.Store.GetRealmsByIDs(ctx, ids)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.d.Service.RefreshBoosts(ctx, entities); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyGetRealmBatchResponse{}
	for i := range entities {
		response.Realms = append(response.Realms, realmToProto(&entities[i]))
	}
	return response, nil
}

func (s *grpcRealmService) GetUserRealms(ctx context.Context, req *gen.DyGetUserRealmsRequest) (*gen.DyGetUserRealmsResponse, error) {
	accountID, err := uuid.Parse(req.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "account_id must be a uuid")
	}
	ids, err := s.d.Store.JoinedRealmIDs(ctx, accountID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyGetUserRealmsResponse{}
	for _, id := range ids {
		response.RealmIds = append(response.RealmIds, id.String())
	}
	return response, nil
}

func (s *grpcRealmService) GetPublicRealms(ctx context.Context, req *gen.DyGetPublicRealmsRequest) (*gen.DyGetPublicRealmsResponse, error) {
	entities, err := s.d.Store.ListPublicRealms(ctx, req.GetIsCommunity(), req.GetOrderBy(), int(req.GetTake()))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.d.Service.RefreshBoosts(ctx, entities); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyGetPublicRealmsResponse{}
	for i := range entities {
		response.Realms = append(response.Realms, realmToProto(&entities[i]))
	}
	return response, nil
}

func (s *grpcRealmService) SearchRealms(ctx context.Context, req *gen.DySearchRealmsRequest) (*gen.DyGetPublicRealmsResponse, error) {
	entities, err := s.d.Store.SearchPublicRealms(ctx, req.GetQuery(), int(req.GetLimit()))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.d.Service.RefreshBoosts(ctx, entities); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyGetPublicRealmsResponse{}
	for i := range entities {
		response.Realms = append(response.Realms, realmToProto(&entities[i]))
	}
	return response, nil
}

// SendInviteNotify pushes the localized invitation to the member's account.
func (s *grpcRealmService) SendInviteNotify(ctx context.Context, req *gen.DySendInviteNotifyRequest) (*emptypb.Empty, error) {
	member := req.GetMember()
	if member == nil {
		return nil, status.Error(codes.InvalidArgument, "member is required")
	}
	accountID, err := uuid.Parse(member.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "account_id must be a uuid")
	}
	account, err := s.d.Accounts.GetAccountByID(ctx, accountID)
	if err != nil || account == nil {
		return nil, status.Error(codes.NotFound, "Account not found")
	}
	if s.d.Push == nil {
		// No push provider: the invitation is still recorded, only the
		// notification is skipped (the C# service would have failed here).
		return &emptypb.Empty{}, nil
	}
	realmName := member.GetRealm().GetName()
	if realmName == "" {
		realmName = "Unknown Realm"
	}
	title := localization.Localize(account.Language, "realmInviteTitle", nil)
	body := localization.Localize(account.Language, "realmInviteBody", map[string]string{"realm": realmName})
	if err := s.d.Push.SendToUser(ctx, accountID.String(), title, body); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *grpcRealmService) IsMemberWithRole(ctx context.Context, req *gen.DyIsMemberWithRoleRequest) (*wrapperspb.BoolValue, error) {
	if len(req.GetRequiredRoles()) == 0 {
		return wrapperspb.Bool(false), nil
	}
	realmID, err := uuid.Parse(req.GetRealmId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "realm_id must be a uuid")
	}
	accountID, err := uuid.Parse(req.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "account_id must be a uuid")
	}
	required := make([]int, 0, len(req.GetRequiredRoles()))
	for _, role := range req.GetRequiredRoles() {
		required = append(required, int(role))
	}
	ok, err := s.d.Service.IsMemberWithRole(ctx, realmID, accountID, required...)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return wrapperspb.Bool(ok), nil
}

func (s *grpcRealmService) HasPermission(ctx context.Context, req *gen.DyHasRealmPermissionRequest) (*wrapperspb.BoolValue, error) {
	realmID, err := uuid.Parse(req.GetRealmId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "realm_id must be a uuid")
	}
	accountID, err := uuid.Parse(req.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "account_id must be a uuid")
	}
	ok, err := s.d.Service.HasPermission(ctx, realmID, accountID, req.GetPermission())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return wrapperspb.Bool(ok), nil
}

func (s *grpcRealmService) LoadMemberAccount(ctx context.Context, req *gen.DyLoadMemberAccountRequest) (*gen.DyRealmMember, error) {
	member := req.GetMember()
	if member == nil {
		return nil, status.Error(codes.InvalidArgument, "member is required")
	}
	rows := []RealmMemberEntity{{
		RealmID: mustUUID(member.GetRealmId()), AccountID: mustUUID(member.GetAccountId()),
	}}
	if found, err := s.d.Store.GetActiveMember(ctx, rows[0].RealmID, rows[0].AccountID); err == nil {
		rows[0] = *found
	} else if !errors.Is(err, ErrNotFound) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.hydrateNavigation(ctx, rows); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return memberToProto(&rows[0]), nil
}

func (s *grpcRealmService) LoadMemberAccounts(ctx context.Context, req *gen.DyLoadMemberAccountsRequest) (*gen.DyLoadMemberAccountsResponse, error) {
	incoming := req.GetMembers()
	rows := make([]RealmMemberEntity, 0, len(incoming))
	for _, member := range incoming {
		rows = append(rows, RealmMemberEntity{
			RealmID: mustUUID(member.GetRealmId()), AccountID: mustUUID(member.GetAccountId()),
		})
	}
	// Re-read the authoritative rows so a caller's stale payload is replaced.
	byKey := map[[2]uuid.UUID]*RealmMemberEntity{}
	for _, row := range rows {
		member, err := s.d.Store.GetActiveMember(ctx, row.RealmID, row.AccountID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		byKey[[2]uuid.UUID{row.RealmID, row.AccountID}] = member
		row = *member
	}
	actual := make([]RealmMemberEntity, 0, len(rows))
	for _, row := range rows {
		if member, ok := byKey[[2]uuid.UUID{row.RealmID, row.AccountID}]; ok {
			actual = append(actual, *member)
			continue
		}
		actual = append(actual, row)
	}
	if err := s.hydrateNavigation(ctx, actual); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyLoadMemberAccountsResponse{}
	for i := range actual {
		response.Members = append(response.Members, memberToProto(&actual[i]))
	}
	return response, nil
}

// hydrateNavigation fills the realm/label/account navigations of the member
// rows in a constant number of queries.
func (s *grpcRealmService) hydrateNavigation(ctx context.Context, rows []RealmMemberEntity) error {
	if len(rows) == 0 {
		return nil
	}
	realmIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		realmIDs = append(realmIDs, row.RealmID)
	}
	realms, err := s.d.Store.GetRealmsByIDs(ctx, realmIDs)
	if err != nil {
		return err
	}
	if err := s.d.Service.RefreshBoosts(ctx, realms); err != nil {
		return err
	}
	realmByID := make(map[uuid.UUID]*RealmEntity, len(realms))
	for i := range realms {
		realmByID[realms[i].ID] = &realms[i]
	}
	if err := HydrateMembers(ctx, s.d.Accounts, rows); err != nil {
		return err
	}
	for i := range rows {
		rows[i].Realm = realmByID[rows[i].RealmID]
	}
	return nil
}

// ─────────────────────────── converters ───────────────────────────

func realmToProto(entity *RealmEntity) *gen.DyRealm {
	if entity == nil {
		return nil
	}
	return &gen.DyRealm{
		Id: entity.ID.String(), Name: entity.Name, Slug: entity.Slug,
		IsCommunity: entity.IsCommunity, IsPublic: entity.IsPublic,
		Description:  entity.Description,
		Picture:      auth.CloudFileToProto(store.DecodeFileReference(entity.Picture)),
		Background:   auth.CloudFileToProto(store.DecodeFileReference(entity.Background)),
		Verification: verificationToProto(decodeVerification(entity.Verification)),
		AccountId:    entity.AccountID.String(),
		BoostPoints:  entity.BoostPoints.String(),
		BoostLevel:   int32(boostLevel(entity.BoostPoints.Decimal)),
	}
}

func memberToProto(row *RealmMemberEntity) *gen.DyRealmMember {
	if row == nil {
		return nil
	}
	member := &gen.DyRealmMember{
		AccountId: row.AccountID.String(), RealmId: row.RealmID.String(),
		Role: int32(row.Role), Nick: derefString(row.Nick), Bio: derefString(row.Bio),
		Experience:       int32(row.Experience),
		Level:            int32(realmLevelFromExp(row.Experience)),
		LevelingProgress: realmLevelingProgress(row.Experience),
	}
	if row.JoinedAt != nil {
		member.JoinedAt = timestamppb.New(*row.JoinedAt)
	}
	if row.LeaveAt != nil {
		member.LeaveAt = timestamppb.New(*row.LeaveAt)
	}
	if row.LabelID != nil {
		member.LabelId = row.LabelID.String()
	}
	if row.Realm != nil {
		member.Realm = realmToProto(row.Realm)
	}
	if row.Account != nil {
		member.Account = auth.AccountToProto(row.Account)
	}
	return member
}

func verificationToProto(mark *model.SnVerificationMark) *gen.DyVerificationMark {
	if mark == nil {
		return nil
	}
	return &gen.DyVerificationMark{
		Type: gen.DyVerificationMarkType(mark.Type), Title: derefString(mark.Title),
		Description: derefString(mark.Description), VerifiedBy: derefString(mark.VerifiedBy),
	}
}

func mustUUID(raw string) uuid.UUID {
	id, _ := uuid.Parse(raw)
	return id
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
