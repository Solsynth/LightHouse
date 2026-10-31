package auth

import (
	"context"
	"errors"
	"strings"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// SudoValidator reports whether an auth session is currently elevated ("sudo",
// i.e. the account re-authenticated an auth-critical action).
// factorTypes lists the factor type names a client may use to elevate.
type SudoValidator interface {
	ValidateSudo(ctx context.Context, sessionID string) (bool, []string, error)
}

// ValidateSudo calls the remote DyAuthService.ValidateSudo RPC. The remote
// service is the source of truth for elevation: sessions are cached for an
// hour, so a cached elevation flag would go stale.
func (a *GrpcTokenAuthenticator) ValidateSudo(ctx context.Context, sessionID string) (bool, []string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return false, nil, errors.New("session id is empty")
	}

	client := gen.NewDyAuthServiceClient(a.conn)

	resp, err := client.ValidateSudo(ctx, &gen.DyValidateSudoRequest{SessionId: sessionID})
	if err != nil {
		return false, nil, err
	}
	return resp.GetValid(), resp.GetFactorTypes(), nil
}
