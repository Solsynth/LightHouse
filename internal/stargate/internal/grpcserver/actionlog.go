// DyActionLogService port of Padlock Account/ActionLogServiceGrpc.cs.
package grpcserver

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

type dyActionLogService struct {
	gen.UnimplementedDyActionLogServiceServer
	d Deps
}

// CreateActionLog mirrors ActionLogServiceGrpc.CreateActionLog: meta values
// are converted from protobuf Values (null entries dropped), optional
// user-agent/ip/location/session are passed through, and failures surface as
// Internal "Failed to create action log".
func (s *dyActionLogService) CreateActionLog(ctx context.Context, req *gen.DyCreateActionLogRequest) (*gen.DyCreateActionLogResponse, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID")
	}

	meta := protoMetaToAny(req.Meta)
	for k, v := range meta {
		if v == nil {
			delete(meta, k)
		}
	}

	var sessionID *string
	if req.SessionId != nil && req.SessionId.Value != "" {
		if id, err := uuid.Parse(req.SessionId.Value); err == nil {
			v := id.String()
			sessionID = &v
		}
	}
	optStr := func(w *wrapperspb.StringValue) string {
		if w == nil || strings.TrimSpace(w.Value) == "" {
			return ""
		}
		return w.Value
	}
	var location *string
	if req.Location != nil && strings.TrimSpace(req.Location.Value) != "" {
		v := req.Location.Value
		location = &v
	}

	err = s.d.Logs.Create(ctx, accountID.String(), model.ActionLogType(req.Action), meta,
		optStr(req.UserAgent), optStr(req.IpAddress), location, sessionID)
	if err != nil {
		s.d.Log.Error("failed to create action log", "account_id", req.AccountId, "error", err)
		return nil, status.Error(codes.Internal, "Failed to create action log")
	}
	return &gen.DyCreateActionLogResponse{}, nil
}

// ListActionLogs mirrors ActionLogServiceGrpc.ListActionLogs: default
// ordering is created_at DESC ("createdat" requests ascending), the page
// token is a raw offset, and hasMore is detected by fetching one extra row.
func (s *dyActionLogService) ListActionLogs(ctx context.Context, req *gen.DyListActionLogsRequest) (*gen.DyListActionLogsResponse, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID")
	}
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	offset := 0
	if parsed, err := strconv.Atoi(req.PageToken); err == nil && parsed > 0 {
		offset = parsed
	}

	orderAsc := strings.ToLower(req.OrderBy) == "createdat"
	var logs []model.ActionLog
	var total int
	if orderAsc {
		// The store's paged action-log query is DESC-only; reuse the search
		// query (same filters, ascending) for the page and the DESC query
		// purely for the total count.
		_, total, err = s.d.Store.AdminListOwnActionLogs(ctx, accountID, req.Action, 0, 0)
		if err != nil {
			return nil, err
		}
		logs, err = s.d.Store.SearchActionLogs(ctx, &accountID, nil, nil, nil, false, offset, pageSize+1)
		if err != nil {
			return nil, err
		}
	} else {
		logs, total, err = s.d.Store.AdminListOwnActionLogs(ctx, accountID, req.Action, pageSize+1, offset)
		if err != nil {
			return nil, err
		}
	}

	response := &gen.DyListActionLogsResponse{TotalSize: int32(total)}
	if len(logs) > pageSize {
		logs = logs[:pageSize]
		response.NextPageToken = strconv.Itoa(offset + pageSize)
	}
	for i := range logs {
		response.ActionLogs = append(response.ActionLogs, actionLogToProto(&logs[i]))
	}
	return response, nil
}

// SearchActionLogs mirrors ActionLogServiceGrpc.SearchActionLogs: optional
// account, multi-action filter, created_at range, and created_at[,id]
// ordering (default ascending).
func (s *dyActionLogService) SearchActionLogs(ctx context.Context, req *gen.DySearchActionLogsRequest) (*gen.DySearchActionLogsResponse, error) {
	var accountID *uuid.UUID
	if req.AccountId != nil && strings.TrimSpace(req.AccountId.Value) != "" {
		id, err := uuid.Parse(req.AccountId.Value)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "Invalid account ID")
		}
		accountID = &id
	}

	actions := make([]string, 0, len(req.Actions))
	seen := make(map[string]struct{}, len(req.Actions))
	for _, a := range req.Actions {
		if strings.TrimSpace(a) == "" {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		actions = append(actions, a)
	}

	var createdAfter, createdBefore *time.Time
	if req.CreatedAfter != nil {
		t := req.CreatedAfter.AsTime()
		createdAfter = &t
	}
	if req.CreatedBefore != nil {
		t := req.CreatedBefore.AsTime()
		createdBefore = &t
	}

	orderDesc := strings.ToLower(req.OrderBy) == "createdat desc"

	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	offset := 0
	if parsed, err := strconv.Atoi(req.PageToken); err == nil && parsed > 0 {
		offset = parsed
	}

	logs, err := s.d.Store.SearchActionLogs(ctx, accountID, actions, createdAfter, createdBefore, orderDesc, offset, pageSize+1)
	if err != nil {
		s.d.Log.Error("failed to search action logs", "error", err)
		return nil, status.Error(codes.Internal, "Failed to search action logs")
	}

	response := &gen.DySearchActionLogsResponse{}
	if len(logs) > pageSize {
		logs = logs[:pageSize]
		response.NextPageToken = strconv.Itoa(offset + pageSize)
	}
	for i := range logs {
		response.ActionLogs = append(response.ActionLogs, actionLogToProto(&logs[i]))
	}
	return response, nil
}
