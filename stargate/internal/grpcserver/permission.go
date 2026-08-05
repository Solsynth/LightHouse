// DyPermissionService port of Padlock Permission/PermissionServiceGrpc.cs.
// Evaluation mirrors PermissionService.cs: an actor's effective permissions
// are its direct nodes plus the nodes of every group it belongs to, blocked
// keys (PermissionModification punishments) deny even when granted, exact
// matches win over wildcard patterns (best of up to 100 candidates).
package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/store"
)

type dyPermissionService struct {
	gen.UnimplementedDyPermissionServiceServer
	d Deps
}

// convertActorType mirrors SnPermissionNode.ConvertProtoActorType
// (nil → Account).
func convertActorType(t *gen.DyPermissionNodeActorType) int {
	if t != nil && *t == gen.DyPermissionNodeActorType_DY_GROUP {
		return 1
	}
	return 0
}

// HasPermission mirrors PermissionServiceGrpc.HasPermission. Account actors
// resolve through the local permission service (blocked-key + wildcard
// aware); group actors resolve through the same evaluation with type=Group.
func (s *dyPermissionService) HasPermission(ctx context.Context, req *gen.DyHasPermissionRequest) (*gen.DyHasPermissionResponse, error) {
	nodeType := convertActorType(req.Type)
	has, err := s.hasPermission(ctx, req.Actor, req.Key, nodeType)
	if err != nil {
		s.d.Log.Error("permission check failed", "type", nodeType, "actor", req.Actor, "key", req.Key, "error", err)
		return nil, status.Error(codes.Internal, "Permission check failed")
	}
	return &gen.DyHasPermissionResponse{HasPermission: has}, nil
}

func (s *dyPermissionService) hasPermission(ctx context.Context, actor, key string, nodeType int) (bool, error) {
	if nodeType == 0 {
		if accountID, err := uuid.Parse(actor); err == nil {
			return s.d.Perm.HasPermission(ctx, accountID, key)
		}
	}
	now := timeNow()
	// The C# skips the punishment-block check for non-Account actors.
	if nodeType == 0 {
		blocked, err := s.d.Store.GetBlockedPermissionKeys(ctx, actor, now)
		if err != nil {
			return false, err
		}
		if isBlockedKey(blocked, key) {
			return false, nil
		}
	}
	raw, _, found, err := s.d.Store.FindPermissionNodeValue(ctx, actor, nodeType, key, now)
	if err != nil || !found {
		return false, err
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

// GetPermission mirrors PermissionServiceGrpc.GetPermission: returns the raw
// node value as a structpb.Value, or nil when the actor holds no node for the
// key (blocked keys included).
func (s *dyPermissionService) GetPermission(ctx context.Context, req *gen.DyGetPermissionRequest) (*gen.DyGetPermissionResponse, error) {
	nodeType := convertActorType(req.Type)
	now := timeNow()
	if nodeType == 0 {
		blocked, err := s.d.Store.GetBlockedPermissionKeys(ctx, req.Actor, now)
		if err != nil {
			s.d.Log.Error("failed to retrieve permission", "error", err)
			return nil, status.Error(codes.Internal, "Failed to retrieve permission")
		}
		if isBlockedKey(blocked, req.Key) {
			return &gen.DyGetPermissionResponse{}, nil
		}
	}
	raw, _, found, err := s.d.Store.FindPermissionNodeValue(ctx, req.Actor, nodeType, req.Key, now)
	if err != nil {
		s.d.Log.Error("failed to retrieve permission", "error", err)
		return nil, status.Error(codes.Internal, "Failed to retrieve permission")
	}
	if !found {
		return &gen.DyGetPermissionResponse{}, nil
	}
	value, err := jsonToProtoValue(raw)
	if err != nil {
		s.d.Log.Error("failed to parse permission value", "error", err)
		return nil, status.Error(codes.Internal, "Failed to retrieve permission")
	}
	return &gen.DyGetPermissionResponse{Value: value}, nil
}

// AddPermissionNode mirrors PermissionServiceGrpc.AddPermissionNode: creates
// an actor-scoped node (group_id NULL).
func (s *dyPermissionService) AddPermissionNode(ctx context.Context, req *gen.DyAddPermissionNodeRequest) (*gen.DyAddPermissionNodeResponse, error) {
	nodeType := convertActorType(req.Type)
	raw, err := protoValueToJSON(req.Value)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid permission value format")
	}
	node, err := s.d.Store.InsertPermissionNode(ctx, req.Actor, nodeType, req.Key, raw,
		tsToModel(req.ExpiredAt), tsToModel(req.AffectedAt))
	if err != nil {
		s.d.Log.Error("failed to add permission node", "error", err)
		return nil, status.Error(codes.Internal, "Failed to add permission node")
	}
	return &gen.DyAddPermissionNodeResponse{Node: permissionNodeToProto(node)}, nil
}

// AddPermissionNodeToGroup mirrors PermissionServiceGrpc.AddPermissionNodeToGroup.
func (s *dyPermissionService) AddPermissionNodeToGroup(ctx context.Context, req *gen.DyAddPermissionNodeToGroupRequest) (*gen.DyAddPermissionNodeToGroupResponse, error) {
	nodeType := convertActorType(req.Type)
	group, err := findPermissionGroup(s.d.Store, ctx, req.Group)
	if err != nil {
		return nil, err
	}
	raw, err := protoValueToJSON(req.Value)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid permission value format")
	}
	groupID, err := uuid.Parse(group.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "Permission group not found")
	}
	node, err := s.d.Store.PermissionNodeUpsert(ctx, groupID, req.Key, raw, req.Actor, nodeType,
		tsToModel(req.ExpiredAt), tsToModel(req.AffectedAt))
	if err != nil {
		s.d.Log.Error("failed to add permission node to group", "error", err)
		return nil, status.Error(codes.Internal, "Failed to add permission node to group")
	}
	return &gen.DyAddPermissionNodeToGroupResponse{Node: permissionNodeToProto(node)}, nil
}

// RemovePermissionNode mirrors PermissionServiceGrpc.RemovePermissionNode.
// The gRPC layer always resolves the optional proto type to a concrete actor
// type (nil → Account), so the filter is always applied.
func (s *dyPermissionService) RemovePermissionNode(ctx context.Context, req *gen.DyRemovePermissionNodeRequest) (*gen.DyRemovePermissionNodeResponse, error) {
	nodeType := convertActorType(req.Type)
	err := s.d.Store.RemovePermissionNode(ctx, req.Actor, req.Key, &nodeType, timeNow())
	if err != nil {
		s.d.Log.Error("failed to remove permission node", "error", err)
		return nil, status.Error(codes.Internal, "Failed to remove permission node")
	}
	return &gen.DyRemovePermissionNodeResponse{Success: true}, nil
}

// RemovePermissionNodeFromGroup mirrors
// PermissionServiceGrpc.RemovePermissionNodeFromGroup (type pinned to Group).
func (s *dyPermissionService) RemovePermissionNodeFromGroup(ctx context.Context, req *gen.DyRemovePermissionNodeFromGroupRequest) (*gen.DyRemovePermissionNodeFromGroupResponse, error) {
	group, err := findPermissionGroup(s.d.Store, ctx, req.Group)
	if err != nil {
		return nil, err
	}
	groupID, err := uuid.Parse(group.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "Permission group not found")
	}
	err = s.d.Store.RemovePermissionNodeFromGroup(ctx, groupID, req.Actor, req.Key, timeNow())
	if err != nil {
		s.d.Log.Error("failed to remove permission node from group", "error", err)
		return nil, status.Error(codes.Internal, "Failed to remove permission node from group")
	}
	return &gen.DyRemovePermissionNodeFromGroupResponse{Success: true}, nil
}

// findPermissionGroup mirrors PermissionServiceGrpc.FindPermissionGroupAsync:
// an unparseable or missing group id resolves to NotFound.
func findPermissionGroup(st *store.Store, ctx context.Context, group *gen.DyPermissionGroup) (*store.PermissionGroup, error) {
	if group == nil {
		return nil, status.Error(codes.NotFound, "Permission group not found")
	}
	groupID, err := uuid.Parse(group.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "Permission group not found")
	}
	g, err := st.PermissionGroupGet(ctx, groupID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "Permission group not found")
		}
		return nil, err
	}
	return g, nil
}

// permissionNodeToProto mirrors SnPermissionNode.ToProtoValue.
func permissionNodeToProto(n *store.PermissionNode) *gen.DyPermissionNode {
	proto := &gen.DyPermissionNode{
		Id:         n.Id,
		Actor:      n.Actor,
		Type:       actorTypeToProto(n.Type),
		Key:        n.Key,
		ExpiredAt:  toProtoTime(n.ExpiredAt),
		AffectedAt: toProtoTime(n.AffectedAt),
	}
	if len(n.Value) > 0 {
		if v, err := jsonToProtoValue(n.Value); err == nil {
			proto.Value = v
		}
	}
	if n.GroupId != nil {
		proto.GroupId = *n.GroupId
	}
	return proto
}

func actorTypeToProto(t int) gen.DyPermissionNodeActorType {
	if t == 1 {
		return gen.DyPermissionNodeActorType_DY_GROUP
	}
	return gen.DyPermissionNodeActorType_DY_ACCOUNT
}

// isBlockedKey mirrors PermissionService.IsPermissionBlocked: exact
// case-insensitive match or a '*' wildcard pattern match.
func isBlockedKey(blocked []string, key string) bool {
	lower := strings.ToLower(key)
	for _, pattern := range blocked {
		p := strings.ToLower(pattern)
		if p == lower {
			return true
		}
		if strings.Contains(p, "*") && wildcardMatch(p, lower) {
			return true
		}
	}
	return false
}

// wildcardMatch is a direct port of PermissionService.MatchesWildcard ('*'
// matches any run of characters).
func wildcardMatch(pattern, target string) bool {
	patternIndex, targetIndex := 0, 0
	wildcardIndex, wildcardTargetIndex := -1, -1
	for targetIndex < len(target) {
		if patternIndex < len(pattern) && pattern[patternIndex] != '*' && pattern[patternIndex] == target[targetIndex] {
			patternIndex++
			targetIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			wildcardIndex = patternIndex
			wildcardTargetIndex = targetIndex
			patternIndex++
			continue
		}
		if wildcardIndex < 0 {
			return false
		}
		patternIndex = wildcardIndex + 1
		wildcardTargetIndex++
		targetIndex = wildcardTargetIndex
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}
