package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

// SingleUser is the stateless config-backed identity provider. It never reads
// or writes accounts, sessions, factors or challenges. Tokens are ordinary
// JWTs; refresh rotation and revocation are intentionally absent.
type SingleUser struct {
	cfg          *config.Config
	jwt          *JWTService
	account      model.Account
	username     string
	passwordHash []byte
	allowRefresh bool
}

// NewSingleUser builds the provider from [auth.singleUser].
func NewSingleUser(cfg *config.Config, jwt *JWTService) (*SingleUser, error) {
	if cfg == nil || !cfg.SingleUserEnabled() {
		return nil, errors.New("single-user auth provider is not selected")
	}
	su := cfg.Auth.SingleUser
	username := strings.TrimSpace(su.Username)
	if username == "" {
		return nil, errors.New("single-user auth requires a username")
	}
	hash := strings.TrimSpace(su.PasswordHash)
	if hash == "" {
		return nil, errors.New("single-user auth requires a bcrypt passwordHash")
	}
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return nil, fmt.Errorf("single-user passwordHash is not bcrypt: %w", err)
	}

	id := strings.TrimSpace(su.ID)
	if id == "" {
		// Derive a stable UUID from the username so tokens survive restarts.
		id = uuid.NewSHA1(uuid.NameSpaceOID, []byte("stargate:single-user:"+username)).String()
	} else if _, err := uuid.Parse(id); err != nil {
		return nil, fmt.Errorf("single-user id must be a UUID: %w", err)
	}

	nick := strings.TrimSpace(su.DisplayName)
	if nick == "" {
		nick = username
	}
	account := model.Account{
		Id: id, Name: username, Nick: nick, Language: "en", Region: "",
		IsSuperuser: su.Superuser,
	}
	return &SingleUser{
		cfg: cfg, jwt: jwt, account: account, username: username,
		passwordHash: []byte(hash), allowRefresh: su.AllowRefresh,
	}, nil
}

// Account returns a copy of the configured identity.
func (s *SingleUser) Account() model.Account {
	if s == nil {
		return model.Account{}
	}
	return s.account
}

// Matches reports whether a token subject belongs to this provider.
func (s *SingleUser) Matches(subject string) bool {
	return s != nil && strings.TrimSpace(subject) == s.account.Id
}

// Authenticate verifies the configured username and bcrypt password.
func (s *SingleUser) Authenticate(_ context.Context, username, password string) (*model.Account, error) {
	if s == nil {
		return nil, errors.New("single-user auth is not configured")
	}
	if strings.TrimSpace(username) != s.username {
		return nil, errors.New("invalid username or password")
	}
	if err := bcrypt.CompareHashAndPassword(s.passwordHash, []byte(password)); err != nil {
		return nil, errors.New("invalid username or password")
	}
	account := s.account
	return &account, nil
}

// IssueTokens mints a fresh stateless access/refresh pair. The synthetic
// session exists only in the token claims.
func (s *SingleUser) IssueTokens(_ context.Context) (*TokenPair, *model.AuthSession, error) {
	if s == nil || s.jwt == nil {
		return nil, nil, errors.New("single-user auth is not configured")
	}
	now := time.Now().UTC()
	session := &model.AuthSession{
		Id:        uuid.NewString(),
		AccountId: s.account.Id,
		Account:   &s.account,
		CreatedAt: model.NewTime(now),
		UpdatedAt: model.NewTime(now),
	}
	accessExpires := now.Add(s.cfg.AccessTokenLifetime())
	access, err := s.jwt.CreateUserToken(session, &s.account, accessExpires)
	if err != nil {
		return nil, nil, err
	}
	pair := &TokenPair{AccessToken: access, AccessTokenExpiresAt: accessExpires}
	if s.allowRefresh {
		refreshExpires := now.Add(s.cfg.RefreshTokenLifetime())
		refresh, err := s.jwt.CreateRefreshToken(session, refreshExpires)
		if err != nil {
			return nil, nil, err
		}
		pair.RefreshToken = refresh
		pair.RefreshTokenExpiresAt = refreshExpires
	}
	return pair, session, nil
}

// RefreshSessionAndIssueTokens satisfies middleware.TokenRenewer. It accepts
// only a refresh token signed for this identity; there is no rotation state.
func (s *SingleUser) RefreshSessionAndIssueTokens(_ context.Context, refreshToken string) (*TokenPair, *model.AuthSession, error) {
	if s == nil || !s.allowRefresh {
		return nil, nil, errors.New("single-user refresh is disabled")
	}
	valid, claims := s.jwt.ValidateJwt(strings.TrimSpace(refreshToken))
	if !valid {
		return nil, nil, errors.New("invalid refresh token")
	}
	if TokenUseOf(claims) != TokenUseRefresh {
		return nil, nil, errors.New("refresh token required")
	}
	subject, _ := claims["sub"].(string)
	if !s.Matches(subject) {
		return nil, nil, errors.New("refresh token does not belong to the single user")
	}
	return s.IssueTokens(context.Background())
}

// SessionFromToken synthesizes the session carried by a single-user access
// token. TokenAuthService calls this after signature and expiry validation.
func (s *SingleUser) SessionFromToken(sessionID string, claims map[string]any) *model.AuthSession {
	if s == nil {
		return nil
	}
	session := &model.AuthSession{
		Id:        sessionID,
		AccountId: s.account.Id,
		Account:   &s.account,
		CreatedAt: model.NewTime(time.Now().UTC()),
		UpdatedAt: model.NewTime(time.Now().UTC()),
	}
	if scopes, ok := claims["scope"].(string); ok && scopes != "" {
		session.Scopes = strings.Fields(scopes)
	}
	return session
}
