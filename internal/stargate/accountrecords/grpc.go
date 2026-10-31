package accountrecords

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// ExperienceGRPC serves DyExperienceService.
type ExperienceGRPC struct {
	gen.UnimplementedDyExperienceServiceServer
	Store *Store
}

// NewExperienceGRPC builds the experience gRPC receiver.
func NewExperienceGRPC(store *Store) *ExperienceGRPC {
	return &ExperienceGRPC{Store: store}
}

// AddRecord inserts an experience record, applies the perk multiplier and
// patches the profile, mirroring Passport's ExperienceServiceGrpc.
func (s *ExperienceGRPC) AddRecord(ctx context.Context, request *gen.DyAddExperienceRecordRequest) (*gen.DyExperienceRecord, error) {
	accountID, err := uuid.Parse(request.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid account id")
	}
	record, err := s.Store.AddExperience(ctx, accountID, request.GetReasonType(), request.GetReason(), request.GetDelta())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return experienceToProto(record), nil
}

// SocialCreditGRPC serves DySocialCreditService.
type SocialCreditGRPC struct {
	gen.UnimplementedDySocialCreditServiceServer
	Store *Store
}

// NewSocialCreditGRPC builds the social-credit gRPC receiver.
func NewSocialCreditGRPC(store *Store) *SocialCreditGRPC {
	return &SocialCreditGRPC{Store: store}
}

// AddRecord inserts a credit record.
func (s *SocialCreditGRPC) AddRecord(ctx context.Context, request *gen.DyAddSocialCreditRecordRequest) (*gen.DySocialCreditRecord, error) {
	accountID, err := uuid.Parse(request.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid account id")
	}
	var expiredAt *time.Time
	if request.GetExpiredAt() != nil {
		value := request.GetExpiredAt().AsTime()
		expiredAt = &value
	}
	record, err := s.Store.AddSocialCredit(ctx, accountID, request.GetReasonType(), request.GetReason(), request.GetDelta(), expiredAt)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return socialCreditToProto(record), nil
}

// GetSocialCredit returns the effective credit (base 100 plus prorated
// deltas).
func (s *SocialCreditGRPC) GetSocialCredit(ctx context.Context, request *gen.DyGetSocialCreditRequest) (*gen.DySocialCreditResponse, error) {
	accountID, err := uuid.Parse(request.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid account id")
	}
	amount, err := s.Store.EffectiveSocialCredit(ctx, accountID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.DySocialCreditResponse{Amount: amount}, nil
}

func experienceToProto(record *ExperienceRecord) *gen.DyExperienceRecord {
	out := &gen.DyExperienceRecord{
		Id:              record.Id.String(),
		ReasonType:      record.ReasonType,
		Reason:          record.Reason,
		Delta:           record.Delta,
		BonusMultiplier: record.BonusMultiplier,
		AccountId:       record.AccountId.String(),
	}
	if record.CreatedAt != nil {
		out.CreatedAt = timestamppb.New(time.Time(*record.CreatedAt))
	}
	if record.UpdatedAt != nil {
		out.UpdatedAt = timestamppb.New(time.Time(*record.UpdatedAt))
	}
	return out
}

func socialCreditToProto(record *SocialCreditRecord) *gen.DySocialCreditRecord {
	out := &gen.DySocialCreditRecord{
		Id:         record.Id.String(),
		ReasonType: record.ReasonType,
		Reason:     record.Reason,
		Delta:      record.Delta,
		AccountId:  record.AccountId.String(),
	}
	if record.CreatedAt != nil {
		out.CreatedAt = timestamppb.New(time.Time(*record.CreatedAt))
	}
	if record.UpdatedAt != nil {
		out.UpdatedAt = timestamppb.New(time.Time(*record.UpdatedAt))
	}
	return out
}
