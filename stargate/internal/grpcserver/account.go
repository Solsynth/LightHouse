// DyAccountService port of Padlock Account/AccountServiceGrpc.cs.
package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

type dyAccountService struct {
	gen.UnimplementedDyAccountServiceServer
	d Deps
}

// hydratePerks populates PerkSubscription/PerkLevel from the wallet service
// for each account, mirroring Padlock's PopulatePerkSubscriptionsAsync. A nil
// Token (or wallet client) degrades to no perk, matching the C# try/catch.
// Callers hydrate before AccountToProto so the wire contract carries
// perk_level — DysonFS derives storage base quota from it.

// hydrateProfiles attaches the account_profiles row (create-on-missing) to
// each account so the DyAccount proto carries the profile — the fleet's
// profile reads moved to Stargate with the account_profiles table.
func (s *dyAccountService) hydrateProfiles(ctx context.Context, accounts []model.Account) error {
	if len(accounts) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(accounts))
	for i := range accounts {
		ids = append(ids, uuid.MustParse(accounts[i].Id))
	}
	profiles, err := s.d.Store.GetProfilesByAccountIDs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range accounts {
		profile, ok := profiles[accounts[i].Id]
		if !ok {
			profile, err = s.d.Store.GetOrCreateAccountProfile(ctx, uuid.MustParse(accounts[i].Id))
			if err != nil {
				return err
			}
		}
		accounts[i].Profile = profile
	}
	return nil
}

func (s *dyAccountService) hydratePerks(ctx context.Context, accounts []model.Account) {
	if s.d.Token == nil || len(accounts) == 0 {
		return
	}
	for i := range accounts {
		s.d.Token.HydratePerk(ctx, &accounts[i])
	}
}

// GetAccount mirrors AccountServiceGrpc.GetAccount.
func (s *dyAccountService) GetAccount(ctx context.Context, req *gen.DyGetAccountRequest) (*gen.DyAccount, error) {
	accountID, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	account, err := s.d.Store.GetAccountByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "Account %s not found", req.Id)
		}
		return nil, err
	}
	if err := s.hydrateProfiles(ctx, []model.Account{*account}); err != nil {
		return nil, err
	}
	if s.d.Token != nil {
		s.d.Token.HydratePerk(ctx, account)
	}
	return auth.AccountToProto(account), nil
}

// GetBotAccount mirrors AccountServiceGrpc.GetBotAccount.
func (s *dyAccountService) GetBotAccount(ctx context.Context, req *gen.DyGetBotAccountRequest) (*gen.DyAccount, error) {
	automatedID, err := uuid.Parse(req.AutomatedId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid automated ID format")
	}
	account, err := s.d.Store.GetAccountByAutomatedID(ctx, automatedID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "Account with automated ID %s not found", req.AutomatedId)
		}
		return nil, err
	}
	if err := s.hydrateProfiles(ctx, []model.Account{*account}); err != nil {
		return nil, err
	}
	if s.d.Token != nil {
		s.d.Token.HydratePerk(ctx, account)
	}
	return auth.AccountToProto(account), nil
}

// GetAccountBatch mirrors AccountServiceGrpc.GetAccountBatch: invalid ids are
// skipped, matching the C# Guid.TryParse filter.
func (s *dyAccountService) GetAccountBatch(ctx context.Context, req *gen.DyGetAccountBatchRequest) (*gen.DyGetAccountBatchResponse, error) {
	ids := make([]uuid.UUID, 0, len(req.Id))
	for _, id := range req.Id {
		if parsed, err := uuid.Parse(id); err == nil {
			ids = append(ids, parsed)
		}
	}
	accounts, err := s.d.Store.GetAccountsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	s.hydratePerks(ctx, accounts)
	if err := s.hydrateProfiles(ctx, accounts); err != nil {
		return nil, err
	}
	response := &gen.DyGetAccountBatchResponse{}
	for i := range accounts {
		response.Accounts = append(response.Accounts, auth.AccountToProto(&accounts[i]))
	}
	return response, nil
}

// GetBotAccountBatch mirrors AccountServiceGrpc.GetBotAccountBatch.
func (s *dyAccountService) GetBotAccountBatch(ctx context.Context, req *gen.DyGetBotAccountBatchRequest) (*gen.DyGetAccountBatchResponse, error) {
	ids := make([]uuid.UUID, 0, len(req.AutomatedId))
	for _, id := range req.AutomatedId {
		if parsed, err := uuid.Parse(id); err == nil {
			ids = append(ids, parsed)
		}
	}
	accounts, err := s.d.Store.GetAccountsByAutomatedIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	s.hydratePerks(ctx, accounts)
	if err := s.hydrateProfiles(ctx, accounts); err != nil {
		return nil, err
	}
	response := &gen.DyGetAccountBatchResponse{}
	for i := range accounts {
		response.Accounts = append(response.Accounts, auth.AccountToProto(&accounts[i]))
	}
	return response, nil
}

// LookupAccountBatch mirrors AccountServiceGrpc.LookupAccountBatch.
func (s *dyAccountService) LookupAccountBatch(ctx context.Context, req *gen.DyLookupAccountBatchRequest) (*gen.DyGetAccountBatchResponse, error) {
	accounts, err := s.d.Store.GetAccountsByNames(ctx, req.Names)
	if err != nil {
		return nil, err
	}
	s.hydratePerks(ctx, accounts)
	if err := s.hydrateProfiles(ctx, accounts); err != nil {
		return nil, err
	}
	response := &gen.DyGetAccountBatchResponse{}
	for i := range accounts {
		response.Accounts = append(response.Accounts, auth.AccountToProto(&accounts[i]))
	}
	return response, nil
}

// SearchAccount mirrors AccountServiceGrpc.SearchAccount: an empty/whitespace
// query returns an empty batch; ILIKE + trigram search capped at 100 rows.
func (s *dyAccountService) SearchAccount(ctx context.Context, req *gen.DySearchAccountRequest) (*gen.DyGetAccountBatchResponse, error) {
	normalized := strings.TrimSpace(req.Query)
	if normalized == "" {
		return &gen.DyGetAccountBatchResponse{}, nil
	}
	accounts, err := s.d.Store.SearchAccounts(ctx, normalized, 100)
	if err != nil {
		return nil, err
	}
	s.hydratePerks(ctx, accounts)
	if err := s.hydrateProfiles(ctx, accounts); err != nil {
		return nil, err
	}
	response := &gen.DyGetAccountBatchResponse{}
	for i := range accounts {
		response.Accounts = append(response.Accounts, auth.AccountToProto(&accounts[i]))
	}
	return response, nil
}

// ListAccounts mirrors AccountServiceGrpc.ListAccounts: pageSize defaults to
// 50 (max 500), pageToken is a zero-based page number; the response carries
// total count and the next page token when the page is full.
func (s *dyAccountService) ListAccounts(ctx context.Context, req *gen.DyListAccountsRequest) (*gen.DyListAccountsResponse, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	page := 0
	if parsed, err := strconv.Atoi(req.PageToken); err == nil && parsed > 0 {
		page = parsed
	}
	accounts, total, err := s.d.Store.AdminListAccounts(ctx, req.Filter, req.OrderBy, int(pageSize), int(pageSize)*page)
	if err != nil {
		return nil, err
	}
	s.hydratePerks(ctx, accounts)
	if err := s.hydrateProfiles(ctx, accounts); err != nil {
		return nil, err
	}
	response := &gen.DyListAccountsResponse{TotalSize: int32(total)}
	if len(accounts) == int(pageSize) {
		response.NextPageToken = fmt.Sprintf("%d", page+1)
	}
	for i := range accounts {
		response.Accounts = append(response.Accounts, auth.AccountToProto(&accounts[i]))
	}
	return response, nil
}

// ListContacts mirrors AccountServiceGrpc.ListContacts. The type/verified
// filters are applied in Go over the account's contact rows (equivalent to
// the C# WHERE clauses).
func (s *dyAccountService) ListContacts(ctx context.Context, req *gen.DyListContactsRequest) (*gen.DyListContactsResponse, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	var ctype *int
	if req.Type != gen.DyAccountContactType_DY_ACCOUNT_CONTACT_TYPE_UNSPECIFIED {
		t := contactTypeFromProto(req.Type)
		ctype = &t
	}
	contacts, err := s.d.Store.ListContacts(ctx, accountID.String())
	if err != nil {
		return nil, err
	}
	response := &gen.DyListContactsResponse{}
	for i := range contacts {
		c := &contacts[i]
		if ctype != nil && c.Type != *ctype {
			continue
		}
		if req.VerifiedOnly && c.VerifiedAt == nil {
			continue
		}
		response.Contacts = append(response.Contacts, auth.ContactToProto(c))
	}
	return response, nil
}

// GetContactsByProvider mirrors AccountServiceGrpc.GetContactsByProvider:
// retained for compatibility, always returns an empty list.
func (s *dyAccountService) GetContactsByProvider(ctx context.Context, req *gen.DyGetContactsByProviderRequest) (*gen.DyListContactsResponse, error) {
	return &gen.DyListContactsResponse{}, nil
}

// GetContactsByAccount mirrors AccountServiceGrpc.GetContactsByAccount.
func (s *dyAccountService) GetContactsByAccount(ctx context.Context, req *gen.DyGetContactsByAccountRequest) (*gen.DyListContactsResponse, error) {
	return s.ListContacts(ctx, &gen.DyListContactsRequest{
		AccountId:    req.AccountId,
		Type:         gen.DyAccountContactType_DY_ACCOUNT_CONTACT_TYPE_UNSPECIFIED,
		VerifiedOnly: false,
	})
}

// ListAuthFactors mirrors AccountServiceGrpc.ListAuthFactors (ActiveOnly
// keeps enabled factors whose expiry is null or in the future).
func (s *dyAccountService) ListAuthFactors(ctx context.Context, req *gen.DyListAuthFactorsRequest) (*gen.DyListAuthFactorsResponse, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	factors, err := s.d.Store.ListAllFactors(ctx, accountID)
	if err != nil {
		return nil, err
	}
	now := timeNow()
	response := &gen.DyListAuthFactorsResponse{}
	for i := range factors {
		f := &factors[i]
		if req.ActiveOnly {
			if f.EnabledAt == nil {
				continue
			}
			if f.ExpiredAt != nil && !f.ExpiredAt.Time().After(now) {
				continue
			}
		}
		response.Factors = append(response.Factors, authFactorToProto(f))
	}
	return response, nil
}

// ResetPasswordFactor mirrors AccountServiceGrpc.ResetPasswordFactor: bcrypt
// the new password, create-or-reset the Password factor, and record the
// reset action log.
func (s *dyAccountService) ResetPasswordFactor(ctx context.Context, req *gen.DyResetPasswordFactorRequest) (*gen.DyAccountAuthFactor, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	if strings.TrimSpace(req.NewPassword) == "" {
		return nil, status.Error(codes.InvalidArgument, "New password is required")
	}
	exists, err := s.d.Store.AccountExists(ctx, accountID.String())
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "Account %s not found", req.AccountId)
	}
	hashed, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		return nil, err
	}
	factor, err := s.d.Store.AdminUpsertPasswordFactor(ctx, accountID, hashed, timeNow())
	if err != nil {
		return nil, err
	}
	if s.d.Logs != nil {
		_ = s.d.Logs.Create(ctx, accountID.String(), model.ActionLogAuthFactorResetPassword,
			map[string]any{"factor_type": "Password"}, "", "", nil, nil)
	}
	return authFactorToProto(factor), nil
}

// ListConnections mirrors AccountServiceGrpc.ListConnections.
func (s *dyAccountService) ListConnections(ctx context.Context, req *gen.DyListConnectionsRequest) (*gen.DyListConnectionsResponse, error) {
	accountID, err := uuid.Parse(req.AccountId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid account ID format")
	}
	var provider *string
	if strings.TrimSpace(req.Provider) != "" {
		v := req.Provider
		provider = &v
	}
	connections, err := s.d.Store.ListConnectionsWithTokens(ctx, accountID, provider)
	if err != nil {
		return nil, err
	}
	response := &gen.DyListConnectionsResponse{}
	for i := range connections {
		response.Connections = append(response.Connections, connectionToProto(&connections[i]))
	}
	return response, nil
}

// GetAccountByConnection mirrors AccountServiceGrpc.GetAccountByConnection.
func (s *dyAccountService) GetAccountByConnection(ctx context.Context, req *gen.DyGetAccountByConnectionRequest) (*gen.DyAccount, error) {
	if strings.TrimSpace(req.Provider) == "" {
		return nil, status.Error(codes.InvalidArgument, "Provider is required")
	}
	if strings.TrimSpace(req.ProvidedIdentifier) == "" {
		return nil, status.Error(codes.InvalidArgument, "Provided identifier is required")
	}
	connection, err := s.d.Store.GetConnectionByProviderAndIdentifier(ctx, req.Provider, req.ProvidedIdentifier)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "Connection %s:%s not found", req.Provider, req.ProvidedIdentifier)
		}
		return nil, err
	}
	account, err := s.d.Store.GetAccountByID(ctx, uuid.MustParse(connection.AccountId))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "Account for connection %s:%s not found", req.Provider, req.ProvidedIdentifier)
		}
		return nil, err
	}
	if err := s.hydrateProfiles(ctx, []model.Account{*account}); err != nil {
		return nil, err
	}
	if s.d.Token != nil {
		s.d.Token.HydratePerk(ctx, account)
	}
	return auth.AccountToProto(account), nil
}

// GetValidAccessToken mirrors AccountServiceGrpc.GetValidAccessToken. The
// C# refreshes the OAuth provider token through its OIDC services; those are
// Phase 7 territory, so — exactly like the C# "provider does not support
// refresh" fallback — the stored/current access token is used, persisted with
// a fresh last_used_at, and FailedPrecondition is returned when none exists.
func (s *dyAccountService) GetValidAccessToken(ctx context.Context, req *gen.DyGetValidAccessTokenRequest) (*gen.DyGetValidAccessTokenResponse, error) {
	connectionID, err := uuid.Parse(req.ConnectionId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid connection ID format")
	}
	connection, err := s.d.Store.GetConnectionFullByID(ctx, connectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "Connection not found")
		}
		return nil, err
	}

	refreshToken := connection.RefreshToken
	if strings.TrimSpace(req.RefreshToken) != "" {
		refreshToken = req.RefreshToken
	}
	accessToken := connection.AccessToken
	if req.CurrentAccessToken != nil && strings.TrimSpace(req.CurrentAccessToken.Value) != "" {
		accessToken = req.CurrentAccessToken.Value
	}
	_ = refreshToken // provider refresh not wired yet (see comment above)

	if strings.TrimSpace(accessToken) == "" {
		return nil, status.Error(codes.FailedPrecondition, "No valid access token available")
	}

	if err := s.d.Store.UpdateConnectionAccessToken(ctx, connection.Id, accessToken, timeNow()); err != nil {
		return nil, err
	}
	return &gen.DyGetValidAccessTokenResponse{AccessToken: accessToken}, nil
}

// ListSuperusers mirrors AccountServiceGrpc.ListSuperusers: the members of
// the superuser/root permission groups.
func (s *dyAccountService) ListSuperusers(ctx context.Context, req *emptypb.Empty) (*gen.DyListSuperusersResponse, error) {
	actors, err := s.d.Store.GetSuperuserActorIDs(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(actors))
	for _, a := range actors {
		if id, err := uuid.Parse(a); err == nil {
			ids = append(ids, id)
		}
	}
	accounts, err := s.d.Store.GetAccountsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateProfiles(ctx, accounts); err != nil {
		return nil, err
	}
	response := &gen.DyListSuperusersResponse{}
	for i := range accounts {
		response.Accounts = append(response.Accounts, auth.AccountToProto(&accounts[i]))
	}
	return response, nil
}

func contactTypeFromProto(t gen.DyAccountContactType) int {
	switch t {
	case gen.DyAccountContactType_DY_PHONE_NUMBER:
		return int(model.ContactTypePhoneNumber)
	case gen.DyAccountContactType_DY_ADDRESS:
		return int(model.ContactTypeAddress)
	default:
		return int(model.ContactTypeEmail)
	}
}
