// Relationship RPCs of DyProfileService, ported from Passport's
// AccountServiceGrpc.ListFriends/ListBlocked/ListMuted/ListCloseFriends/
// GetRelationship/HasRelationship. They are the read half of the social graph
// the C# fleet depends on (Sphere timelines, Messager block checks, Wallet and
// Develop friend lookups all call these).
//
// This file deliberately does not live in profile.go: the profile service is
// mounted by the profiles module, while these RPCs belong to the relationships
// module. profile.go's dyProfileService delegates here, so a disabled
// relationships module leaves them Unimplemented.
package grpcserver

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/relationship"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// relationshipRPCs serves the six DyProfileService relationship methods.
type relationshipRPCs struct {
	service *relationship.Service
	store   *store.Store
	// hydrator builds the DyAccount payloads on a relationship row. It is the
	// same account implementation DyAccountService serves, so a relationship
	// read carries the identical account shape the fleet already parses.
	hydrator *dyAccountService
}

func newRelationshipRPCs(deps Deps) *relationshipRPCs {
	return &relationshipRPCs{
		service:  deps.Relationships,
		store:    deps.Store,
		hydrator: &dyAccountService{d: deps},
	}
}

// available reports whether the relationships module is mounted.
func (r *relationshipRPCs) available() bool {
	return r != nil && r.service != nil && r.service.Available()
}

// unimplemented mirrors the embedded UnimplementedDyProfileServiceServer
// message so a disabled module is indistinguishable from an unported method.
func (r *relationshipRPCs) unimplemented(method string) error {
	return status.Errorf(codes.Unimplemented, "method %s not implemented", method)
}

// GetRelationship mirrors AccountServiceGrpc.GetRelationship: one directed
// row with the optional status filter, hydrated with both accounts.
func (r *relationshipRPCs) GetRelationship(ctx context.Context, req *gen.DyGetRelationshipRequest) (*gen.DyGetRelationshipResponse, error) {
	if !r.available() {
		return nil, r.unimplemented("GetRelationship")
	}
	accountID, err := parseRelationshipID(req.AccountId)
	if err != nil {
		return nil, err
	}
	relatedID, err := parseRelationshipID(req.RelatedId)
	if err != nil {
		return nil, err
	}
	var statusFilter *model.RelationshipStatus
	if req.Status != nil {
		status := model.RelationshipStatus(*req.Status)
		statusFilter = &status
	}
	row, err := r.service.GetRelationship(ctx, accountID, relatedID, statusFilter, false, false)
	if err != nil {
		if err == store.ErrNotFound {
			// The C# returns an empty response (no relationship), not NotFound.
			return &gen.DyGetRelationshipResponse{}, nil
		}
		return nil, err
	}
	return &gen.DyGetRelationshipResponse{Relationship: r.toProtoRelationship(ctx, row)}, nil
}

// HasRelationship mirrors AccountServiceGrpc.HasRelationship, including the
// either-direction blocked shortcut and the strict status check.
func (r *relationshipRPCs) HasRelationship(ctx context.Context, req *gen.DyGetRelationshipRequest) (*wrapperspb.BoolValue, error) {
	if !r.available() {
		return nil, r.unimplemented("HasRelationship")
	}
	accountID, err := parseRelationshipID(req.AccountId)
	if err != nil {
		return nil, err
	}
	relatedID, err := parseRelationshipID(req.RelatedId)
	if err != nil {
		return nil, err
	}
	if req.EitherDirection && req.Status != nil &&
		model.RelationshipStatus(*req.Status) == model.RelationshipBlocked {
		blocked, err := r.service.IsBlockedEitherDirection(ctx, accountID, relatedID)
		if err != nil {
			return nil, err
		}
		return wrapperspb.Bool(blocked), nil
	}
	if req.Status == nil {
		exists, err := r.service.HasExisting(ctx, accountID, relatedID)
		if err != nil {
			return nil, err
		}
		return wrapperspb.Bool(exists), nil
	}
	has, err := r.service.HasStatus(ctx, accountID, relatedID, model.RelationshipStatus(*req.Status))
	if err != nil {
		return nil, err
	}
	return wrapperspb.Bool(has), nil
}

// ListFriends mirrors AccountServiceGrpc.ListFriends. The account_id variant
// lists the account's friends; the related_id variant lists the accounts that
// have it as their friend (isRelated). RelationshipFriends also matches
// CloseFriend rows, matching GetCachedRelationships.
func (r *relationshipRPCs) ListFriends(ctx context.Context, req *gen.DyListRelationshipSimpleRequest) (*gen.DyListRelationshipSimpleResponse, error) {
	return r.list(ctx, req, "ListFriends", model.RelationshipFriends, true)
}

// ListBlocked mirrors AccountServiceGrpc.ListBlocked.
func (r *relationshipRPCs) ListBlocked(ctx context.Context, req *gen.DyListRelationshipSimpleRequest) (*gen.DyListRelationshipSimpleResponse, error) {
	return r.list(ctx, req, "ListBlocked", model.RelationshipBlocked, true)
}

// ListMuted mirrors AccountServiceGrpc.ListMuted. The C# only accepts
// account_id; a related_id request is an InvalidArgument.
func (r *relationshipRPCs) ListMuted(ctx context.Context, req *gen.DyListRelationshipSimpleRequest) (*gen.DyListRelationshipSimpleResponse, error) {
	return r.list(ctx, req, "ListMuted", model.RelationshipMuted, false)
}

// ListCloseFriends mirrors AccountServiceGrpc.ListCloseFriends. Like
// ListMuted, the C# only accepts account_id.
func (r *relationshipRPCs) ListCloseFriends(ctx context.Context, req *gen.DyListRelationshipSimpleRequest) (*gen.DyListRelationshipSimpleResponse, error) {
	return r.list(ctx, req, "ListCloseFriends", model.RelationshipCloseFriend, false)
}

// list implements the four list RPCs. allowRelated reports whether the C#
// handler accepts the related_id variant; the two that do not mirror its
// "The account_id must be provided." error.
func (r *relationshipRPCs) list(ctx context.Context, req *gen.DyListRelationshipSimpleRequest, method string, status model.RelationshipStatus, allowRelated bool) (*gen.DyListRelationshipSimpleResponse, error) {
	if !r.available() {
		return nil, r.unimplemented(method)
	}
	accountID, relatedID, isRelated, err := relationIdentifier(req, allowRelated)
	if err != nil {
		return nil, err
	}
	target := accountID
	if isRelated {
		target = relatedID
	}
	ids, err := r.service.ListRelated(ctx, target, status, isRelated)
	if err != nil {
		return nil, err
	}
	return &gen.DyListRelationshipSimpleResponse{AccountsId: ids}, nil
}

// relationIdentifier resolves the request oneof into (accountID, relatedID,
// isRelated), reproducing the C# switch and its two distinct error messages.
func relationIdentifier(req *gen.DyListRelationshipSimpleRequest, allowRelated bool) (uuid.UUID, uuid.UUID, bool, error) {
	switch identifier := req.GetRelationIdentifier().(type) {
	case *gen.DyListRelationshipSimpleRequest_AccountId:
		id, err := uuid.Parse(identifier.AccountId)
		if err != nil {
			return uuid.Nil, uuid.Nil, false, status.Error(codes.InvalidArgument, "Invalid account ID format")
		}
		return id, uuid.Nil, false, nil
	case *gen.DyListRelationshipSimpleRequest_RelatedId:
		if !allowRelated {
			return uuid.Nil, uuid.Nil, false, status.Error(codes.InvalidArgument, "The account_id must be provided.")
		}
		id, err := uuid.Parse(identifier.RelatedId)
		if err != nil {
			return uuid.Nil, uuid.Nil, false, status.Error(codes.InvalidArgument, "Invalid account ID format")
		}
		return uuid.Nil, id, true, nil
	default:
		if !allowRelated {
			return uuid.Nil, uuid.Nil, false, status.Error(codes.InvalidArgument, "The account_id must be provided.")
		}
		return uuid.Nil, uuid.Nil, false, status.Error(codes.InvalidArgument, "The relationship identifier must be provided.")
	}
}

func parseRelationshipID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	return id, nil
}

// toProtoRelationship maps a row to DyRelationship, hydrating Account and
// Related through the same account implementation DyAccountService serves.
func (r *relationshipRPCs) toProtoRelationship(ctx context.Context, row *model.Relationship) *gen.DyRelationship {
	if row == nil {
		return nil
	}
	proto := &gen.DyRelationship{
		AccountId: row.AccountId,
		RelatedId: row.RelatedId,
		Status:    int32(row.Status),
		CreatedAt: toProtoTime(row.CreatedAt),
		UpdatedAt: toProtoTime(row.UpdatedAt),
	}
	if row.Alias != nil {
		proto.Alias = row.Alias
	}
	proto.Account = r.accountProto(ctx, row.AccountId)
	proto.Related = r.accountProto(ctx, row.RelatedId)
	return proto
}

// accountProto loads one account for a relationship row. A dangling account
// leaves the field nil (the C# throws only because both sides always exist;
// on the wire the optional field carries the same meaning).
func (r *relationshipRPCs) accountProto(ctx context.Context, id string) *gen.DyAccount {
	accountID, err := uuid.Parse(id)
	if err != nil || r.store == nil {
		return nil
	}
	account, err := r.store.GetAccountByID(ctx, accountID)
	if err != nil {
		return nil
	}
	if err := r.hydrator.hydrateProfile(ctx, account); err != nil {
		return auth.AccountToProto(account)
	}
	return auth.AccountToProto(account)
}
