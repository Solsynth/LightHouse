// Package grpcserver's account-key surface. The service is its own proto
// service (DyAccountKeyService) so the optional keys module can register or
// omit it without touching DyAccountService.
package grpcserver

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/accountkeys"
	"src.solsynth.dev/sosys/stargate/model"
)

// RegisterAccountKeys mounts DyAccountKeyService. A nil store leaves the
// service unregistered; the module passes its store only while enabled.
func RegisterAccountKeys(s *grpc.Server, keys *accountkeys.Store) {
	if keys == nil {
		return
	}
	gen.RegisterDyAccountKeyServiceServer(s, &dyAccountKeyService{keys: keys})
}

type dyAccountKeyService struct {
	gen.UnimplementedDyAccountKeyServiceServer
	keys *accountkeys.Store
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
		keys, err := s.keys.ListSSHKeys(ctx, accountID)
		if err != nil {
			return nil, status.Error(codes.Internal, "Failed to list SSH keys")
		}
		for i := range keys {
			response.Keys = append(response.Keys, sshKeyToProto(&keys[i]))
		}
	}
	if req.GetKind() == gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_UNSPECIFIED ||
		req.GetKind() == gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_PGP {
		keys, err := s.keys.ListPGPKeys(ctx, accountID)
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
	fingerprint := strings.TrimSpace(req.GetFingerprint())
	if fingerprint != "" && !strings.HasPrefix(fingerprint, "SHA256:") {
		fingerprint = "SHA256:" + fingerprint
	}
	if publicKey := req.GetPublicKey(); publicKey != "" {
		info, err := accountkeys.ParseSSHKey(publicKey)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "Invalid SSH public key")
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
	key, err := s.keys.FindSSHKeyByFingerprint(ctx, fingerprint, accountFilter)
	if err != nil {
		if errors.Is(err, accountkeys.ErrNotFound) {
			response.Valid = false
			response.Reason = "KEY_NOT_FOUND"
			response.Fingerprint = fingerprint
			return response, nil
		}
		return nil, status.Error(codes.Internal, "Failed to look up SSH key")
	}
	response.Valid = true
	response.AccountId = key.AccountId
	response.KeyId = key.Id
	response.KeyLabel = key.Label
	response.Fingerprint = key.Fingerprint
	return response, nil
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
	key, err := s.keys.FindPGPKeyByFingerprint(ctx, fingerprint, accountFilter)
	if err != nil {
		if errors.Is(err, accountkeys.ErrNotFound) {
			response.Valid = false
			response.Reason = "KEY_NOT_FOUND"
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
