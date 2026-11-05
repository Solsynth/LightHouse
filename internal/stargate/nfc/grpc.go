package nfc

import (
	"context"
	"errors"
	"strings"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/auth"
)

// GRPCService is the DyNfcService port of Passport's NfcServiceGrpc.
type GRPCService struct {
	gen.UnimplementedDyNfcServiceServer
	Service *Service
}

// NewGRPCService builds the inbound gRPC service.
func NewGRPCService(service *Service) gen.DyNfcServiceServer {
	return &GRPCService{Service: service}
}

// ValidateNfcToken validates an SDM token for the login flow. Only tags with
// an owner can authenticate.
func (s *GRPCService) ValidateNfcToken(ctx context.Context, req *gen.DyValidateNfcTokenRequest) (*gen.DyValidateNfcTokenResponse, error) {
	if s.Service == nil || !s.Service.Available() {
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "INTERNAL_ERROR"}, nil
	}
	uidHex := req.GetUidHex()
	if uidHex == "" {
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "TAG_NOT_FOUND"}, nil
	}
	piccData, encData, cmac, isURL := ParseNfcInput(uidHex)
	if !isURL {
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "UNSUPPORTED_FORMAT"}, nil
	}
	result, err := s.Service.ValidateSun(ctx, piccData, encData, cmac, nil)
	if err != nil {
		if errors.Is(err, ErrReplay) {
			return &gen.DyValidateNfcTokenResponse{ErrorCode: "REPLAY"}, nil
		}
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "INTERNAL_ERROR"}, nil
	}
	if result == nil {
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "TAG_NOT_FOUND"}, nil
	}
	switch result.ClaimStatus {
	case ClaimNeedsAuth, ClaimUnclaimed:
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "TAG_UNCLAIMED"}, nil
	case ClaimPreAssignedMismatch:
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "TAG_PRE_ASSIGNED"}, nil
	}
	if result.Account == nil || result.Tag == nil || result.Tag.AccountID == nil {
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "TAG_UNCLAIMED"}, nil
	}
	if req.GetTagUid() != "" && !strings.EqualFold(req.GetTagUid(), result.Tag.UID) {
		return &gen.DyValidateNfcTokenResponse{ErrorCode: "TAG_NOT_FOUND"}, nil
	}
	return &gen.DyValidateNfcTokenResponse{
		IsValid:   true,
		AccountId: result.Tag.AccountID.String(),
		TagId:     result.Tag.ID.String(),
	}, nil
}

// ResolveNfcTag resolves an SDM tag and returns the owner's profile.
func (s *GRPCService) ResolveNfcTag(ctx context.Context, req *gen.DyResolveNfcTagRequest) (*gen.DyResolveNfcTagResponse, error) {
	if s.Service == nil || !s.Service.Available() {
		return &gen.DyResolveNfcTagResponse{ErrorCode: "INTERNAL_ERROR"}, nil
	}
	uidHex := req.GetUidHex()
	if uidHex == "" {
		return &gen.DyResolveNfcTagResponse{ErrorCode: "TAG_NOT_FOUND"}, nil
	}
	piccData, encData, cmac, isURL := ParseNfcInput(uidHex)
	if !isURL {
		return &gen.DyResolveNfcTagResponse{ErrorCode: "UNSUPPORTED_FORMAT"}, nil
	}
	result, err := s.Service.ValidateSun(ctx, piccData, encData, cmac, nil)
	if err != nil {
		if errors.Is(err, ErrReplay) {
			return &gen.DyResolveNfcTagResponse{ErrorCode: "REPLAY"}, nil
		}
		return &gen.DyResolveNfcTagResponse{ErrorCode: "INTERNAL_ERROR"}, nil
	}
	if result == nil {
		return &gen.DyResolveNfcTagResponse{ErrorCode: "TAG_NOT_FOUND"}, nil
	}
	switch result.ClaimStatus {
	case ClaimNeedsAuth, ClaimUnclaimed:
		return &gen.DyResolveNfcTagResponse{ErrorCode: "TAG_UNCLAIMED"}, nil
	case ClaimPreAssignedMismatch:
		return &gen.DyResolveNfcTagResponse{ErrorCode: "TAG_PRE_ASSIGNED"}, nil
	}
	if result.Account == nil {
		return &gen.DyResolveNfcTagResponse{ErrorCode: "TAG_UNCLAIMED"}, nil
	}
	return &gen.DyResolveNfcTagResponse{
		IsValid:  true,
		Account:  auth.AccountToProto(result.Account),
		Profile:  auth.ProfileToProto(result.Profile),
		IsFriend: result.IsFriend,
		Actions:  result.Actions,
	}, nil
}

// TokenProvider exposes the domain as the ports.NfcTokens provider so NFC
// login stops dialing Passport.
type TokenProvider struct {
	Service *Service
}

// Available reports whether the provider can validate tokens.
func (p *TokenProvider) Available() bool {
	return p != nil && p.Service != nil && p.Service.Available()
}

// Validate ports NfcServiceGrpc.ValidateNfcToken to the port contract,
// returning the owner account id and tag id.
func (p *TokenProvider) Validate(ctx context.Context, tagUID, uidHex string) (string, string, error) {
	if !p.Available() {
		return "", "", errors.New("nfc token provider is unavailable")
	}
	if uidHex == "" {
		return "", "", errors.New("nfc token rejected: TAG_NOT_FOUND")
	}
	piccData, encData, cmac, isURL := ParseNfcInput(uidHex)
	if !isURL {
		return "", "", errors.New("nfc token rejected: UNSUPPORTED_FORMAT")
	}
	result, err := p.Service.ValidateSun(ctx, piccData, encData, cmac, nil)
	if err != nil {
		if errors.Is(err, ErrReplay) {
			return "", "", errors.New("nfc token rejected: REPLAY")
		}
		return "", "", err
	}
	if result == nil {
		return "", "", errors.New("nfc token rejected: TAG_NOT_FOUND")
	}
	switch result.ClaimStatus {
	case ClaimNeedsAuth, ClaimUnclaimed:
		return "", "", errors.New("nfc token rejected: TAG_UNCLAIMED")
	case ClaimPreAssignedMismatch:
		return "", "", errors.New("nfc token rejected: TAG_PRE_ASSIGNED")
	}
	if result.Account == nil || result.Tag == nil || result.Tag.AccountID == nil {
		return "", "", errors.New("nfc token rejected: TAG_UNCLAIMED")
	}
	if tagUID != "" && !strings.EqualFold(tagUID, result.Tag.UID) {
		return "", "", errors.New("nfc token rejected: UID_MISMATCH")
	}
	return result.Tag.AccountID.String(), result.Tag.ID.String(), nil
}

var _ gen.DyNfcServiceServer = (*GRPCService)(nil)
