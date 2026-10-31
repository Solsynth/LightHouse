package auth

import (
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// AuthResult holds the authenticated account and session returned by the auth service.
type AuthResult struct {
	Account *gen.DyAccount
	Session *gen.DyAuthSession
}

// TokenType classifies the kind of token extracted from a request.
type TokenType string

const (
	TokenTypeAuthKey TokenType = "AuthKey"
	TokenTypeApiKey  TokenType = "ApiKey"
)

// TokenInfo holds an extracted token string and its classified type.
type TokenInfo struct {
	Token string
	Type  TokenType
}
