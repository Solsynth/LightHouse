package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/store"
)

// TestApiKeyTokenSurvivesItsExpClaim pins the end-to-end behavior behind the
// bot-token 401s: an api_key token minted with an exp claim (as the pre-fix
// 30-day default did) must still authenticate while its backing session is
// valid, because for api_key tokens the session — not the JWT lifetime — is
// authoritative. A user token with the same past exp must still be rejected.
func TestApiKeyTokenSurvivesItsExpClaim(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), apiKeysDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	accountID := uuid.New()
	name := "token_exp_" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, name, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	defer func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM account_profiles WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM api_keys WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM auth_sessions WHERE account_id = $1`, accountID)
		_, _ = pool.Exec(bg, `DELETE FROM accounts WHERE id = $1`, accountID)
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO account_profiles
		(id, account_id, created_at, updated_at, experience, social_credits)
		VALUES ($1, $2, $3, $3, 0, 100)`, uuid.New(), accountID, now); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	key, err := (&AuthService{store: store.New(pool)}).CreateApiKey(ctx, accountID.String(), "expiry regression", nil, nil)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	js := &JWTService{
		issuer:       "solar-network",
		validIssuers: []string{"solar-network"},
		audience:     "solar-network",
		private:      priv,
		public:       &priv.PublicKey,
	}
	svc := NewTokenAuthService(store.New(pool), nil, js, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	past := now.Add(-time.Hour)
	mintClaims := func(use string) jwt.MapClaims {
		return jwt.MapClaims{
			"sub":     key.AccountId,
			"jti":     key.SessionId,
			"sid":     key.SessionId,
			ClaimType: use,
			"epoch":   "0",
			"iat":     now.Add(-48 * time.Hour).Unix(),
			"nbf":     now.Add(-48 * time.Hour).Unix(),
			"exp":     past.Unix(),
		}
	}
	mustSign := func(claims jwt.MapClaims) string {
		token, err := js.sign(claims, now, past)
		if err != nil {
			t.Fatalf("sign token: %v", err)
		}
		return token
	}

	apiClaims := mintClaims(TokenUseApiKey)
	apiClaims["api_key_id"] = key.Id
	apiClaims["account_id"] = key.AccountId
	ok, session, msg, use := svc.AuthenticateToken(ctx, mustSign(apiClaims), "")
	if !ok || session == nil {
		t.Fatalf("api_key token with past exp rejected: ok=%v msg=%q use=%q", ok, msg, use)
	}
	if use != TokenUseApiKey {
		t.Fatalf("token use = %q, want %q", use, TokenUseApiKey)
	}

	if ok, _, msg, _ := svc.AuthenticateToken(ctx, mustSign(mintClaims(TokenUseUser)), ""); ok || msg != MsgTokenExpired {
		t.Fatalf("user token with past exp: ok=%v msg=%q, want rejected with %q", ok, msg, MsgTokenExpired)
	}
}
