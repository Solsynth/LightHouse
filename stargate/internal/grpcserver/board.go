// DyAuthorizedAppService port of Padlock Auth/BoardAuthServiceGrpc.cs.
package grpcserver

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// accountsProfileBoard is PermissionKeys.AccountsProfileBoard — the scope an
// app needs to be listed as a board app.
const accountsProfileBoard = "accounts.profile.board"

type dyAuthorizedAppService struct {
	gen.UnimplementedDyAuthorizedAppServiceServer
	d Deps
}

// QueryAuthorizedBoardApps mirrors BoardAuthServiceGrpc.QueryAuthorizedBoardApps:
// Oidc-typed authorized apps of the account whose scopes contain
// accounts.profile.board (case-insensitive), optional app-slug filter, and
// offset/take pagination (default take 20).
func (s *dyAuthorizedAppService) QueryAuthorizedBoardApps(ctx context.Context, req *gen.DyQueryAuthorizedBoardAppsRequest) (*gen.DyQueryAuthorizedBoardAppsResponse, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	oidcType := model.AuthorizedAppTypeOidc
	apps, err := s.d.Store.ListAuthorizedApps(ctx, accountID.String(), &oidcType)
	if err != nil {
		return nil, err
	}

	filtered := apps[:0:0]
	for i := range apps {
		app := &apps[i]
		hasScope := false
		for _, scope := range app.Scopes {
			if strings.EqualFold(scope, accountsProfileBoard) {
				hasScope = true
				break
			}
		}
		if !hasScope {
			continue
		}
		if req.AppSlug != "" && !strings.EqualFold(derefOrEmpty(app.AppSlug), req.AppSlug) {
			continue
		}
		filtered = append(filtered, *app)
	}

	totalCount := len(filtered)
	take := int(req.Take)
	if take <= 0 {
		take = 20
	}
	offset := int(req.Offset)
	if offset < 0 {
		offset = 0
	}

	response := &gen.DyQueryAuthorizedBoardAppsResponse{TotalCount: int32(totalCount)}
	for i := offset; i < len(filtered) && i < offset+take; i++ {
		app := &filtered[i]
		response.Apps = append(response.Apps, &gen.DyAuthorizedBoardAppDto{
			AppId:         app.AppId,
			AppSlug:       derefOrEmpty(app.AppSlug),
			AppName:       derefOrEmpty(app.AppName),
			PublisherName: "",
		})
	}
	return response, nil
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
