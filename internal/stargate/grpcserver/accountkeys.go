// Package grpcserver's account-key surface. The service is its own proto
// service (DyAccountKeyService) so the optional keys module can register or
// omit it without touching DyAccountService.
package grpcserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/accountkeys"
	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// AccountKeyDeps is the account-key service's dependency set. Auth, Store and
// Cfg are optional: without them the exchange RPC stays unregistered, while
// listing and validation keep working.
type AccountKeyDeps struct {
	Keys  *accountkeys.Store
	Auth  *auth.AuthService
	Store *store.Store
	Cfg   *config.Config
	Log   *slog.Logger
}

// RegisterAccountKeys mounts DyAccountKeyService. A nil Keys store leaves the
// service unregistered; the module passes its store only while enabled.
func RegisterAccountKeys(s *grpc.Server, deps AccountKeyDeps) {
	if deps.Keys == nil {
		return
	}
	gen.RegisterDyAccountKeyServiceServer(s, &dyAccountKeyService{deps: deps})
}

type dyAccountKeyService struct {
	gen.UnimplementedDyAccountKeyServiceServer
	deps AccountKeyDeps
}

// ListPublicKeys returns the account's keys, optionally filtered by kind.
func (s *dyAccountKeyService) ListPublicKeys(ctx context.Context, req *gen.DyListPublicKeysRequest) (*gen.DyListPublicKeysResponse, error) {
	accountID, err := parseKeyAccountID(req.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	response := &gen.DyListPublicKeysResponse{}
	if req.GetKind() == gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_UNSPECIFIED ||
		req.GetKind() == gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_SSH {
		keys, err := s.deps.Keys.ListSSHKeys(ctx, accountID)
		if err != nil {
			return nil, status.Error(codes.Internal, "Failed to list SSH keys")
		}
		for i := range keys {
			response.Keys = append(response.Keys, sshKeyToProto(&keys[i]))
		}
	}
	if req.GetKind() == gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_UNSPECIFIED ||
		req.GetKind() == gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_PGP {
		keys, err := s.deps.Keys.ListPGPKeys(ctx, accountID)
		if err != nil {
			return nil, status.Error(codes.Internal, "Failed to list PGP keys")
		}
		for i := range keys {
			response.Keys = append(response.Keys, pgpKeyToProto(&keys[i]))
		}
	}
	return response, nil
}

// ValidateSSHPublicKey resolves an offered SSH key to its account for a
// connection gate. purpose is echoed for audit; per-purpose policy is not
// enforced yet.
func (s *dyAccountKeyService) ValidateSSHPublicKey(ctx context.Context, req *gen.DyValidateSSHPublicKeyRequest) (*gen.DyValidateSSHPublicKeyResponse, error) {
	response := &gen.DyValidateSSHPublicKeyResponse{Purpose: req.GetPurpose()}
	key, fingerprint, err := s.resolveSSHKey(ctx, req.GetPublicKey(), req.GetFingerprint(), req.GetAccountId())
	if err != nil {
		if errors.Is(err, accountkeys.ErrNotFound) || errors.Is(err, accountkeys.ErrAmbiguous) {
			response.Valid = false
			if errors.Is(err, accountkeys.ErrAmbiguous) {
				response.Reason = "KEY_AMBIGUOUS"
			} else {
				response.Reason = "KEY_NOT_FOUND"
			}
			response.Fingerprint = fingerprint
			return response, nil
		}
		return nil, err
	}
	response.Valid = true
	response.AccountId = key.AccountId
	response.KeyId = key.Id
	response.KeyLabel = key.Label
	response.Fingerprint = key.Fingerprint
	return response, nil
}

// ExchangeSSHPublicKeyForSession validates an offered SSH key and mints a
// login session for its owner. The caller must present the deployment's
// exchange token: a public key is public material, so proof of possession
// comes from the gateway's SSH handshake, not from the key itself.
func (s *dyAccountKeyService) ExchangeSSHPublicKeyForSession(ctx context.Context, req *gen.DyExchangeSSHPublicKeyForSessionRequest) (*gen.DyExchangeSSHPublicKeyForSessionResponse, error) {
	if s.deps.Auth == nil || s.deps.Store == nil || s.deps.Keys == nil ||
		s.deps.Cfg == nil || s.deps.Cfg.Hosted.ExchangeToken == "" {
		return nil, status.Error(codes.Unavailable, "SSH key session exchange is not configured")
	}
	if subtle.ConstantTimeCompare([]byte(req.GetServiceToken()), []byte(s.deps.Cfg.Hosted.ExchangeToken)) != 1 {
		return nil, status.Error(codes.PermissionDenied, "Invalid exchange service token")
	}

	response := &gen.DyExchangeSSHPublicKeyForSessionResponse{Purpose: req.GetPurpose()}
	key, fingerprint, err := s.resolveSSHKey(ctx, req.GetPublicKey(), req.GetFingerprint(), "")
	if err != nil {
		if errors.Is(err, accountkeys.ErrNotFound) || errors.Is(err, accountkeys.ErrAmbiguous) {
			if errors.Is(err, accountkeys.ErrAmbiguous) {
				response.Reason = "KEY_AMBIGUOUS"
			} else {
				response.Reason = "KEY_NOT_FOUND"
			}
			response.Fingerprint = fingerprint
			return response, nil
		}
		return nil, err
	}

	punishment, err := s.deps.Store.GetActivePunishmentOverview(ctx, key.AccountId)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to check account status")
	}
	if punishment != nil &&
		(punishment.Type == model.PunishmentDisableAccount || punishment.Type == model.PunishmentBlockLogin) {
		response.Reason = "ACCOUNT_LOCKED"
		response.Fingerprint = key.Fingerprint
		return response, nil
	}

	deviceName := strings.TrimSpace(req.GetDeviceName())
	if deviceName == "" {
		deviceName = "Hosted Terminal"
	}
	device, err := s.deps.Auth.GetOrCreateDevice(ctx, key.AccountId,
		"termian-hosted-"+key.AccountId, &deviceName, model.ClientPlatformLinux)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to register hosted device")
	}
	session, err := s.deps.Auth.CreateSessionForOidc(ctx, s.deps.Keys.DB, key.AccountId,
		nil, nil, &device.Id, req.GetIpAddress(), req.GetUserAgent())
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to create hosted session")
	}
	pair, err := s.deps.Auth.CreateTokenPair(ctx, session)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to issue hosted session tokens")
	}

	now := time.Now().UTC()
	response.Valid = true
	response.AccountId = key.AccountId
	response.KeyId = key.Id
	response.KeyLabel = key.Label
	response.Fingerprint = key.Fingerprint
	response.AccessToken = pair.AccessToken
	response.RefreshToken = pair.RefreshToken
	response.ExpiresIn = int64(pair.AccessTokenExpiresAt.Sub(now).Seconds())
	response.RefreshExpiresIn = int64(pair.RefreshTokenExpiresAt.Sub(now).Seconds())
	if s.deps.Log != nil {
		s.deps.Log.Info("hosted SSH key session created",
			"account_id", key.AccountId, "key_id", key.Id, "purpose", req.GetPurpose())
	}
	return response, nil
}

// resolveSSHKey normalizes the offered key or fingerprint and resolves it to
// one live key, optionally restricted to an account. Invalid arguments and
// storage failures come back as gRPC status errors; a miss is
// accountkeys.ErrNotFound so callers can shape their own response.
func (s *dyAccountKeyService) resolveSSHKey(ctx context.Context, publicKey, fingerprint, accountID string) (*model.SSHKey, string, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint != "" && !strings.HasPrefix(fingerprint, "SHA256:") {
		fingerprint = "SHA256:" + fingerprint
	}
	if publicKey != "" {
		info, err := accountkeys.ParseSSHKey(publicKey)
		if err != nil {
			return nil, "", status.Error(codes.InvalidArgument, "Invalid SSH public key")
		}
		if fingerprint != "" && fingerprint != info.Fingerprint {
			return nil, "", status.Error(codes.InvalidArgument, "Public key and fingerprint do not match")
		}
		fingerprint = info.Fingerprint
	}
	if fingerprint == "" {
		return nil, "", status.Error(codes.InvalidArgument, "A public key or fingerprint is required")
	}
	accountFilter, err := optionalKeyAccountID(accountID)
	if err != nil {
		return nil, "", status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	key, err := s.deps.Keys.FindSSHKeyByFingerprint(ctx, fingerprint, accountFilter)
	if err != nil {
		if errors.Is(err, accountkeys.ErrNotFound) || errors.Is(err, accountkeys.ErrAmbiguous) {
			return nil, fingerprint, err
		}
		return nil, "", status.Error(codes.Internal, "Failed to look up SSH key")
	}
	return key, key.Fingerprint, nil
}

// ValidatePGPPublicKey is the same gate for PGP keys, keyed by the primary
// key fingerprint.
func (s *dyAccountKeyService) ValidatePGPPublicKey(ctx context.Context, req *gen.DyValidatePGPPublicKeyRequest) (*gen.DyValidatePGPPublicKeyResponse, error) {
	response := &gen.DyValidatePGPPublicKeyResponse{Purpose: req.GetPurpose()}
	fingerprint := normalizePGPFingerprint(req.GetFingerprint())
	if publicKey := req.GetPublicKey(); publicKey != "" {
		info, err := accountkeys.ParsePGPKey(publicKey)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "Invalid PGP public key")
		}
		if fingerprint != "" && fingerprint != info.Fingerprint {
			return nil, status.Error(codes.InvalidArgument, "Public key and fingerprint do not match")
		}
		fingerprint = info.Fingerprint
	}
	if fingerprint == "" {
		return nil, status.Error(codes.InvalidArgument, "A public key or fingerprint is required")
	}
	accountFilter, err := optionalKeyAccountID(req.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	key, err := s.deps.Keys.FindPGPKeyByFingerprint(ctx, fingerprint, accountFilter)
	if err != nil {
		if errors.Is(err, accountkeys.ErrNotFound) || errors.Is(err, accountkeys.ErrAmbiguous) {
			response.Valid = false
			if errors.Is(err, accountkeys.ErrAmbiguous) {
				response.Reason = "KEY_AMBIGUOUS"
			} else {
				response.Reason = "KEY_NOT_FOUND"
			}
			response.Fingerprint = fingerprint
			return response, nil
		}
		return nil, status.Error(codes.Internal, "Failed to look up PGP key")
	}
	response.Valid = true
	response.AccountId = key.AccountId
	response.KeyId = key.Id
	response.KeyLabel = key.Label
	response.Fingerprint = key.Fingerprint
	return response, nil
}

func sshKeyToProto(key *model.SSHKey) *gen.DyAccountPublicKey {
	return &gen.DyAccountPublicKey{
		Id:          key.Id,
		AccountId:   key.AccountId,
		Kind:        gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_SSH,
		Label:       key.Label,
		Algorithm:   key.Algorithm,
		PublicKey:   key.PublicKey,
		Fingerprint: key.Fingerprint,
		CreatedAt:   toProtoTime(key.CreatedAt),
		UpdatedAt:   toProtoTime(key.UpdatedAt),
	}
}

func pgpKeyToProto(key *model.PGPKey) *gen.DyAccountPublicKey {
	return &gen.DyAccountPublicKey{
		Id:          key.Id,
		AccountId:   key.AccountId,
		Kind:        gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_PGP,
		Label:       key.Label,
		Algorithm:   key.Algorithm,
		PublicKey:   key.PublicKey,
		Fingerprint: key.Fingerprint,
		PgpKeyId:    key.KeyId,
		UserIds:     key.UserIds,
		CreatedAt:   toProtoTime(key.CreatedAt),
		UpdatedAt:   toProtoTime(key.UpdatedAt),
	}
}

func normalizePGPFingerprint(raw string) string {
	raw = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(raw), " ", ""), ":", "")
	return strings.ToUpper(raw)
}

func parseKeyAccountID(raw string) (uuid.UUID, error) {
	return uuid.Parse(raw)
}

func optionalKeyAccountID(raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
