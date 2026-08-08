package auth

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

const authorizedAppsRegressionDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func authorizedAppsRegressionPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), authorizedAppsRegressionDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestAuthorizedAppAndAuthFactorInsertsGenerateIDs(t *testing.T) {
	pool := authorizedAppsRegressionPool(t)
	ctx := context.Background()
	accountID := uuid.NewString()
	appID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts
		(id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, accountID, "authorized_apps_regression_"+uuid.NewString()[:8], now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID) })

	st := store.New(pool)
	svc := NewAuthService(st, nil, nil, nil, nil, nil, nil, nil, nil)
	first, err := svc.UpsertAuthorizedAppAsync(ctx, accountID, appID, model.AuthorizedAppTypeOidc, nil, nil, []string{"openid", "profile"})
	if err != nil {
		t.Fatalf("create authorized app: %v", err)
	}
	if _, err := uuid.Parse(first.Id); err != nil {
		t.Fatalf("authorized app id = %q: %v", first.Id, err)
	}

	second, err := svc.UpsertAuthorizedAppAsync(ctx, accountID, appID, model.AuthorizedAppTypeOidc, nil, nil, []string{"openid"})
	if err != nil {
		t.Fatalf("update authorized app: %v", err)
	}
	if !slices.Equal(second.Scopes, []string{"openid", "profile"}) {
		t.Fatalf("authorized app scopes = %v, want existing scopes preserved", second.Scopes)
	}

	factor, err := st.InsertAuthFactor(ctx, &model.AuthFactor{
		AccountId:   accountID,
		Type:        model.AuthFactorTypePassword,
		Trustworthy: 1,
		Secret:      "regression-secret",
	})
	if err != nil {
		t.Fatalf("create auth factor: %v", err)
	}
	if _, err := uuid.Parse(factor.Id); err != nil {
		t.Fatalf("auth factor id = %q: %v", factor.Id, err)
	}
}
