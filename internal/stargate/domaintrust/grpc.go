package domaintrust

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"
)

// GRPCService is the DyDomainBlockService port of Passport's
// DomainTrustServiceGrpc.
type GRPCService struct {
	gen.UnimplementedDyDomainBlockServiceServer
	Service *Service
}

// NewGRPCService builds the gRPC service.
func NewGRPCService(service *Service) gen.DyDomainBlockServiceServer {
	return &GRPCService{Service: service}
}

func (s *GRPCService) ValidateUrl(ctx context.Context, req *gen.DyValidateUrlRequest) (*gen.DyDomainValidationResult, error) {
	if s.Service == nil {
		return nil, status.Error(codes.Unavailable, "domain trust is not configured")
	}
	result, err := s.Service.ValidateURL(ctx, req.GetUrl())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyDomainValidationResult{
		IsAllowed:  result.IsAllowed,
		IsVerified: result.IsVerified,
	}
	if result.BlockReason != nil {
		response.BlockReason = *result.BlockReason
	}
	if result.MatchedSource != nil {
		response.MatchedSource = *result.MatchedSource
	}
	return response, nil
}

func (s *GRPCService) IsDomainBlocked(ctx context.Context, req *gen.DyDomainBlockCheckRequest) (*gen.DyDomainBlockCheckResult, error) {
	if s.Service == nil {
		return nil, status.Error(codes.Unavailable, "domain trust is not configured")
	}
	var port *int
	if req.GetPort() > 0 {
		value := int(req.GetPort())
		port = &value
	}
	rule, err := s.Service.FindMatchingRule(ctx, req.GetHost(), req.GetProtocol(), port)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyDomainBlockCheckResult{}
	if rule != nil && rule.TrustLevel == int(TrustBlocked) {
		response.IsBlocked = true
		if rule.Reason != nil {
			response.Reason = *rule.Reason
		}
	}
	return response, nil
}

// compile-time check that the service implements the generated interface.
var _ gen.DyDomainBlockServiceServer = (*GRPCService)(nil)
