// DyBotAccountReceiverService port of Padlock Account/BotAccountReceiverGrpc.cs.
package grpcserver

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

type dyBotAccountReceiverService struct {
	gen.UnimplementedDyBotAccountReceiverServiceServer
	d Deps
}

func parseKeyID(raw string) (uuid.UUID, error) {
	return uuid.Parse(raw)
}

// CreateBotAccount mirrors BotAccountReceiverGrpc.CreateBotAccount: duplicate
// automated-id/name checks (InvalidOperationException surfaces as Unknown,
// exactly how gRPC C# maps unhandled exceptions), activation + default-group
// enrollment, and a cascade profile insert.
func (s *dyBotAccountReceiverService) CreateBotAccount(ctx context.Context, req *gen.DyCreateBotAccountRequest) (*gen.DyCreateBotAccountResponse, error) {
	automatedID, err := uuid.Parse(req.AutomatedId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid automated ID format")
	}
	if req.Account == nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	if _, err := uuid.Parse(req.Account.Id); err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	account := accountFromProto(req.Account)

	count, err := s.d.Store.CountAccountsByAutomatedID(ctx, automatedID)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, status.Error(codes.Unknown, "Automated ID has already been used.")
	}
	count, err = s.d.Store.CountAccountsByNameCI(ctx, account.Name)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, status.Error(codes.Unknown, "Account name has already been taken.")
	}

	automatedIDStr := automatedID.String()
	now := timeNow()
	account.AutomatedId = &automatedIDStr
	account.ActivatedAt = model.NewTime(now)
	account.IsSuperuser = false

	// The C# resolves picture/background ids through the drive service
	// (DyFileServiceClient); Stargate's grpcserver Deps carry no drive
	// client, so profile file references pass through as sent by the caller.
	if _, err := s.d.Store.UpsertDefaultGroupMember(ctx, account.Id, now); err != nil {
		return nil, err
	}
	if err := s.d.Store.InsertAccountWithProfile(ctx, account, now); err != nil {
		return nil, err
	}
	// The store's insert seeds a stub profile row; merge the request's
	// profile fields so bot account avatars/banners persist (the C# EF
	// cascade writes them on Add). The EF audit interceptor stamps
	// CreatedAt/UpdatedAt at insert time — mirror that for the response.
	account.CreatedAt = model.NewTime(now)
	account.UpdatedAt = model.NewTime(now)
	if account.Profile != nil {
		if err := s.applyProfile(ctx, account.Id, account.Profile); err != nil {
			return nil, err
		}
	}
	if err := s.reloadProfile(ctx, account); err != nil {
		return nil, err
	}

	return &gen.DyCreateBotAccountResponse{
		Bot: &gen.DyBotAccount{
			Account:     auth.AccountToProto(account),
			AutomatedId: account.Id, // mirrors the C# (it echoes the account id)
			CreatedAt:   toProtoTime(account.CreatedAt),
			UpdatedAt:   toProtoTime(account.UpdatedAt),
			IsActive:    true,
		},
	}, nil
}

// applyProfile merges the caller's profile fields into the account's profile
// row (the C# EF Update()/cascade writes the aggregate's profile columns;
// the store's bot helpers only maintain a stub row).
func (s *dyBotAccountReceiverService) applyProfile(ctx context.Context, accountID string, p *model.Profile) error {
	prof, err := s.d.Store.GetOrCreateAccountProfile(ctx, uuid.MustParse(accountID))
	if err != nil {
		return err
	}
	if p.FirstName != nil {
		prof.FirstName = p.FirstName
	}
	if p.MiddleName != nil {
		prof.MiddleName = p.MiddleName
	}
	if p.LastName != nil {
		prof.LastName = p.LastName
	}
	if p.Bio != nil {
		prof.Bio = p.Bio
	}
	if p.Gender != nil {
		prof.Gender = p.Gender
	}
	if p.Pronouns != nil {
		prof.Pronouns = p.Pronouns
	}
	if p.TimeZone != nil {
		prof.TimeZone = p.TimeZone
	}
	if p.Location != nil {
		prof.Location = p.Location
	}
	if p.Birthday != nil {
		prof.Birthday = p.Birthday
	}
	if p.LastSeenAt != nil {
		prof.LastSeenAt = p.LastSeenAt
	}
	if p.UsernameColor != nil {
		prof.UsernameColor = p.UsernameColor
	}
	if p.Verification != nil {
		prof.Verification = p.Verification
	}
	if p.ActiveBadge != nil {
		prof.ActiveBadge = p.ActiveBadge
	}
	if p.Picture != nil {
		prof.Picture = p.Picture
	}
	if p.Background != nil {
		prof.Background = p.Background
	}
	if p.Links != nil {
		prof.Links = p.Links
	}
	return s.d.Store.SaveProfile(ctx, prof)
}
func (s *dyBotAccountReceiverService) reloadProfile(ctx context.Context, account *model.Account) error {
	profile, err := s.d.Store.GetProfileByAccount(ctx, uuid.MustParse(account.Id))
	if err != nil {
		return err
	}
	account.Profile = profile
	return nil
}

// UpdateBotAccount mirrors BotAccountReceiverGrpc.UpdateBotAccount: the C#
// issues a blind EF Update() on the detached aggregate (no existence check),
// so the response echoes the submitted account.
func (s *dyBotAccountReceiverService) UpdateBotAccount(ctx context.Context, req *gen.DyUpdateBotAccountRequest) (*gen.DyUpdateBotAccountResponse, error) {
	if req.Account == nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	if _, err := uuid.Parse(req.Account.Id); err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	account := accountFromProto(req.Account)
	if err := s.d.Store.UpdateAccountWithProfile(ctx, account, timeNow()); err != nil {
		return nil, err
	}
	if account.Profile != nil {
		if err := s.applyProfile(ctx, account.Id, account.Profile); err != nil {
			return nil, err
		}
	}
	if err := s.reloadProfile(ctx, account); err != nil {
		return nil, err
	}

	return &gen.DyUpdateBotAccountResponse{
		Bot: &gen.DyBotAccount{
			Account:     auth.AccountToProto(account),
			AutomatedId: account.Id,
			CreatedAt:   toProtoTime(account.CreatedAt),
			UpdatedAt:   toProtoTime(account.UpdatedAt),
			IsActive:    true,
		},
	}, nil
}

// DeleteBotAccount mirrors BotAccountReceiverGrpc.DeleteBotAccount: soft
// deletes the account and its sessions. The response keeps Success unset,
// matching the C# which returns the default message.
func (s *dyBotAccountReceiverService) DeleteBotAccount(ctx context.Context, req *gen.DyDeleteBotAccountRequest) (*gen.DyDeleteBotAccountResponse, error) {
	automatedID, err := uuid.Parse(req.AutomatedId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid automated ID format")
	}
	account, err := s.d.Store.GetAccountByAutomatedID(ctx, automatedID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "Account not found")
		}
		return nil, err
	}
	if err := s.d.Store.SoftDeleteAccountAndSessions(ctx, account.Id, timeNow()); err != nil {
		return nil, err
	}
	return &gen.DyDeleteBotAccountResponse{}, nil
}

// GetApiKey mirrors BotAccountReceiverGrpc.GetApiKey.
func (s *dyBotAccountReceiverService) GetApiKey(ctx context.Context, req *gen.DyGetApiKeyRequest) (*gen.DyApiKey, error) {
	keyID, err := parseKeyID(req.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid API key ID format")
	}
	key, err := s.d.Auth.GetApiKey(ctx, keyID, nil)
	if err != nil || key.DeletedAt != nil {
		return nil, status.Error(codes.NotFound, "API key not found")
	}
	return apiKeyToProto(key), nil
}

// ListApiKey mirrors BotAccountReceiverGrpc.ListApiKey: the request carries
// the bot account's automated id.
func (s *dyBotAccountReceiverService) ListApiKey(ctx context.Context, req *gen.DyListApiKeyRequest) (*gen.DyGetApiKeyBatchResponse, error) {
	automatedID, err := uuid.Parse(req.AutomatedId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid automated ID format")
	}
	account, err := s.d.Store.GetAccountByAutomatedID(ctx, automatedID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "Account not found")
		}
		return nil, err
	}
	keys, err := s.d.Store.ListApiKeysByAccount(ctx, account.Id)
	if err != nil {
		return nil, err
	}
	response := &gen.DyGetApiKeyBatchResponse{}
	for i := range keys {
		response.Data = append(response.Data, apiKeyToProto(&keys[i]))
	}
	return response, nil
}

// CreateApiKey mirrors BotAccountReceiverGrpc.CreateApiKey: request.AccountId
// is the bot account's automated id, and the fresh Bot token is attached to
// the returned key. The current DyApiKey protobuf has no expiry field, so bot
// sessions are intentionally created without a session expiry.
func (s *dyBotAccountReceiverService) CreateApiKey(ctx context.Context, req *gen.DyApiKey) (*gen.DyApiKey, error) {
	automatedID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid automated ID format")
	}
	account, err := s.d.Store.GetAccountByAutomatedID(ctx, automatedID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "Account not found")
		}
		return nil, err
	}
	if strings.TrimSpace(req.Label) == "" {
		return nil, status.Error(codes.InvalidArgument, "Label is required")
	}
	key, err := s.d.Auth.CreateApiKey(ctx, account.Id, req.Label, nil, nil)
	if err != nil {
		return nil, err
	}
	token, err := s.d.Auth.IssueApiKeyToken(ctx, key)
	if err != nil {
		return nil, err
	}
	key.Key = &token
	return apiKeyToProto(key), nil
}

// UpdateApiKey mirrors BotAccountReceiverGrpc.UpdateApiKey: scoped to the
// account, and an empty label returns the key unchanged.
func (s *dyBotAccountReceiverService) UpdateApiKey(ctx context.Context, req *gen.DyApiKey) (*gen.DyApiKey, error) {
	keyID, err := parseKeyID(req.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid API key ID format")
	}
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	key, err := s.d.Auth.GetApiKey(ctx, keyID, &accountID)
	if err != nil || key.DeletedAt != nil {
		return nil, status.Error(codes.NotFound, "API key not found")
	}
	if strings.TrimSpace(req.Label) == "" {
		return apiKeyToProto(key), nil
	}
	key.Label = req.Label
	if err := s.d.Store.UpdateApiKeyLabel(ctx, key.Id, req.Label, timeNow()); err != nil {
		return nil, err
	}
	return apiKeyToProto(key), nil
}

// RotateApiKey mirrors BotAccountReceiverGrpc.RotateApiKey: rotates the
// backing session and issues a fresh Bot token.
func (s *dyBotAccountReceiverService) RotateApiKey(ctx context.Context, req *gen.DyGetApiKeyRequest) (*gen.DyApiKey, error) {
	keyID, err := parseKeyID(req.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid API key ID format")
	}
	key, err := s.d.Auth.GetApiKey(ctx, keyID, nil)
	if err != nil || key.DeletedAt != nil {
		return nil, status.Error(codes.NotFound, "API key not found")
	}
	key, err = s.d.Auth.RotateApiKeyToken(ctx, key)
	if err != nil {
		return nil, err
	}
	token, err := s.d.Auth.IssueApiKeyToken(ctx, key)
	if err != nil {
		return nil, err
	}
	key.Key = &token
	return apiKeyToProto(key), nil
}

// DeleteApiKey mirrors BotAccountReceiverGrpc.DeleteApiKey.
func (s *dyBotAccountReceiverService) DeleteApiKey(ctx context.Context, req *gen.DyGetApiKeyRequest) (*gen.DyDeleteApiKeyResponse, error) {
	keyID, err := parseKeyID(req.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid API key ID format")
	}
	key, err := s.d.Auth.GetApiKey(ctx, keyID, nil)
	if err != nil || key.DeletedAt != nil {
		return nil, status.Error(codes.NotFound, "API key not found")
	}
	if err := s.d.Auth.RevokeApiKeyToken(ctx, key); err != nil {
		return nil, err
	}
	return &gen.DyDeleteApiKeyResponse{Success: true}, nil
}
