package profilectl

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// TestRelationshipSummaryViewer pins the viewer path over real HTTP: a
// bearer token attached to the request must populate middleware.CurrentUser,
// so the summary reports the viewer's mutual-friend count with the profile.
// It runs the same middleware chain the server uses, against the live DB and
// the repo's signing keys, and skips when either is unavailable.
func TestRelationshipSummaryViewer(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, searchDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	// TokenAuthService treats a nil cache as "no cache", so this test needs no
	// Redis: a valid access token still authenticates against the session row.
	var rc *redis.Client

	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
	privPath := filepath.Join(root, "Keys", "PrivateKey.pem")
	pubPath := filepath.Join(root, "Keys", "PublicKey.pem")
	if _, err := os.Stat(privPath); err != nil {
		t.Skipf("dev signing keys missing: %v", err)
	}

	cfg := config.Default()
	cfg.Auth.PublicKeyPath = pubPath
	cfg.Auth.PrivateKeyPath = privPath
	jwtSvc, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}

	db, err := gorm.Open(postgres.Open(searchDSN), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	tokenSvc := auth.NewTokenAuthService(st, rc, jwtSvc, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	suffix := uuid.NewString()[:8]
	viewerID, viewerName := uuid.New(), "viewer_"+suffix
	profileID, profileName := uuid.New(), "profile_"+suffix
	bridgeID, bridgeName := uuid.New(), "bridge_"+suffix
	ids := []uuid.UUID{viewerID, profileID, bridgeID}
	now := time.Now().UTC()
	for _, a := range []struct {
		id   uuid.UUID
		name string
	}{
		{viewerID, viewerName},
		{profileID, profileName},
		{bridgeID, bridgeName},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO accounts
			(id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, a.id, a.name, now); err != nil {
			t.Fatalf("seed account %s: %v", a.name, err)
		}
	}
	defer func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM account_relationships WHERE account_id = ANY($1) OR related_id = ANY($1)`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM account_profiles WHERE account_id = ANY($1)`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM accounts WHERE id = ANY($1)`, ids)
	}()

	// Viewer and profile share exactly one friend (bridge), which is what the
	// mutual-friend count must report; their own friendship is not mutual.
	friend := func(from, to uuid.UUID) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO account_relationships
			(account_id, related_id, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)`, from, to, int16(model.RelationshipFriends), now); err != nil {
			t.Fatalf("seed relationship: %v", err)
		}
	}
	friend(viewerID, profileID)
	friend(profileID, viewerID)
	friend(viewerID, bridgeID)
	friend(bridgeID, viewerID)
	friend(profileID, bridgeID)
	friend(bridgeID, profileID)

	// Mint a login session + access token for the viewer, mirroring the sudo
	// harness. TokenAuthService.AuthenticateToken must accept the resulting
	// token for the middleware to attach the session's account.
	sessionID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions
		(id, account_id, audiences, scopes, epoch, type, expired_at, created_at, updated_at, last_granted_at)
		VALUES ($1, $2, '[]', '[]', 0, 0, $3, $4, $4, $4)`,
		sessionID, viewerID, now.Add(time.Hour), now); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	viewer, err := st.GetAccountByID(ctx, viewerID)
	if err != nil {
		t.Fatalf("load viewer: %v", err)
	}
	session := &model.AuthSession{
		Id:            sessionID,
		AccountId:     viewerID.String(),
		Type:          model.SessionTypeLogin,
		Scopes:        []string{},
		Audiences:     []string{},
		Epoch:         0,
		ExpiredAt:     model.NewTime(now.Add(time.Hour)),
		LastGrantedAt: model.NewTime(now),
	}
	token, err := jwtSvc.CreateUserToken(session, viewer, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("create access token: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api", middleware.Auth(middleware.AuthDeps{
		Token: tokenSvc,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	deps := Deps{Store: st, Log: nil}
	RegisterPublicRelationships(api, deps)

	// Authenticated read: the viewer sees their own mutual-friend count with
	// the profile (one, because each accepted friendship writes both rows).
	authed := httptest.NewRequest(http.MethodGet, "/api/accounts/"+profileName+"/relationships", nil)
	authed.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, authed)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed summary: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := decodeSummary(t, rec.Body.Bytes()); got.MutualFriends == nil || *got.MutualFriends != 1 {
		t.Errorf("authed mutual = %+v, want 1", got.MutualFriends)
	}

	// The same profile read anonymously omits the mutual-friend field, so a
	// signed-out reader cannot infer the viewer's social graph.
	anon := httptest.NewRequest(http.MethodGet, "/api/accounts/"+profileName+"/relationships", nil)
	rec2 := httptest.NewRecorder()
	engine.ServeHTTP(rec2, anon)
	if rec2.Code != http.StatusOK {
		t.Fatalf("anon summary: got %d, want 200: %s", rec2.Code, rec2.Body.String())
	}
	if got := decodeSummary(t, rec2.Body.Bytes()); got.MutualFriends != nil {
		t.Errorf("anon mutual = %d, want omitted", *got.MutualFriends)
	}
}

// decodeSummary decodes a summary response, failing the test on a bad body.
func decodeSummary(t *testing.T, raw []byte) relationshipSummaryResponse {
	t.Helper()
	var response relationshipSummaryResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	return response
}
