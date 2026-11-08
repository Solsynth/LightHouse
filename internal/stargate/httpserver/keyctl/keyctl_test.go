package keyctl

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/accountkeys"
	"src.solsynth.dev/sosys/stargate/actionlog"
	"src.solsynth.dev/sosys/stargate/db"
	"src.solsynth.dev/sosys/stargate/dbtest"
	"src.solsynth.dev/sosys/stargate/httpserver/profilectl"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/migrate"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

type allowSudo struct{}

func (allowSudo) IsSudoElevated(context.Context, string) (bool, error) { return true, nil }
func (allowSudo) SudoFactorHint(context.Context, string) (string, error) {
	return "password", nil
}
func (allowSudo) RecordSudoFailure(context.Context, string, string, string, string) {}

func testDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	baseDSN := os.Getenv("STARGATE_TEST_DSN")
	if baseDSN == "" {
		t.Skip("STARGATE_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dsn, cleanup, err := dbtest.NewDatabase(ctx, baseDSN)
	if err != nil {
		t.Skipf("cannot create dedicated database: %v", err)
	}
	t.Cleanup(cleanup)
	database, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(database) })
	if err := migrate.Run(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(accountkeys.Models()...); err != nil {
		t.Fatal(err)
	}
	if err := accountkeys.Indexes(ctx, database); err != nil {
		t.Fatal(err)
	}
	return database
}

func testSSHLine(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
}

func testPGPBlock(t *testing.T) string {
	t.Helper()
	entity, err := openpgp.NewEntity("Key Test", "", "keys@example.com", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer, err := armor.Encode(&buffer, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := entity.Serialize(writer); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func request(t *testing.T, router *gin.Engine, account *model.Account, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	ctx := middleware.WithCurrentUser(req.Context(), account)
	ctx = middleware.WithCurrentSession(ctx, &model.AuthSession{Id: uuid.NewString()})
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	return value
}

func TestKeyRoutes(t *testing.T) {
	database := testDatabase(t)
	middleware.SetSudoChecker(allowSudo{})
	t.Cleanup(func() { middleware.SetSudoChecker(nil) })

	accountID := uuid.New()
	name := "keys_" + uuid.NewString()[:8]
	now := time.Now().UTC()
	if err := database.Exec(`INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES (?, ?, ?, 'en', 'US', false, ?, ?)`, accountID, name, name, now, now).Error; err != nil {
		t.Fatal(err)
	}
	account := &model.Account{Id: accountID.String(), Name: name, Nick: name}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys := accountkeys.NewStore(database)
	router := gin.New()
	Register(router.Group("/api"), Deps{
		Keys:     keys,
		Accounts: store.New(database),
		Logs:     actionlog.New(database),
		Log:      log,
	})

	// Add an SSH key, list it, reject the duplicate, rename, publish, delete.
	sshLine := testSSHLine(t)
	body, _ := json.Marshal(map[string]string{"label": "laptop", "public_key": sshLine})
	created := request(t, router, account, http.MethodPost, "/api/keys/ssh", string(body))
	if created.Code != http.StatusOK {
		t.Fatalf("create ssh: status %d body %s", created.Code, created.Body.String())
	}
	sshKey := decode[model.SSHKey](t, created)

	var logged int64
	if err := database.Model(&store.ActionLogEntity{}).
		Where("account_id = ? AND action = ?", accountID, model.ActionLogSSHKeyCreate).
		Count(&logged).Error; err != nil {
		t.Fatal(err)
	}
	if logged != 1 {
		t.Fatalf("ssh create wrote %d action logs, want 1", logged)
	}

	duplicate := request(t, router, account, http.MethodPost, "/api/keys/ssh", string(body))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate ssh: status %d body %s", duplicate.Code, duplicate.Body.String())
	}

	listed := request(t, router, account, http.MethodGet, "/api/keys/ssh", "")
	if listed.Code != http.StatusOK || len(decode[[]model.SSHKey](t, listed)) != 1 {
		t.Fatalf("list ssh: status %d body %s", listed.Code, listed.Body.String())
	}
	renameBody, _ := json.Marshal(map[string]string{"label": "workstation"})
	renamed := request(t, router, account, http.MethodPatch, "/api/keys/ssh/"+sshKey.Id, string(renameBody))
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename ssh: status %d body %s", renamed.Code, renamed.Body.String())
	}
	if got := decode[model.SSHKey](t, renamed); got.Label != "workstation" {
		t.Fatalf("label = %q, want workstation", got.Label)
	}

	public := request(t, router, account, http.MethodGet, "/api/accounts/"+name+"/keys", "")
	if public.Code != http.StatusOK {
		t.Fatalf("public keys: status %d body %s", public.Code, public.Body.String())
	}
	if got := decode[model.AccountKeys](t, public); len(got.SSH) != 1 || len(got.PGP) != 0 {
		t.Fatalf("public listing = %+v", got)
	}

	deleted := request(t, router, account, http.MethodDelete, "/api/keys/ssh/"+sshKey.Id, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete ssh: status %d body %s", deleted.Code, deleted.Body.String())
	}
	relisted := request(t, router, account, http.MethodGet, "/api/keys/ssh", "")
	if len(decode[[]model.SSHKey](t, relisted)) != 0 {
		t.Fatal("ssh key survived deletion")
	}

	// A deleted key may be re-added: the partial unique index frees the
	// fingerprint.
	readded := request(t, router, account, http.MethodPost, "/api/keys/ssh", string(body))
	if readded.Code != http.StatusOK {
		t.Fatalf("re-add ssh: status %d body %s", readded.Code, readded.Body.String())
	}

	// PGP: add, duplicate, list, delete.
	pgpBlock := testPGPBlock(t)
	pgpBody, _ := json.Marshal(map[string]string{"public_key": pgpBlock})
	pgpCreated := request(t, router, account, http.MethodPost, "/api/keys/pgp", string(pgpBody))
	if pgpCreated.Code != http.StatusOK {
		t.Fatalf("create pgp: status %d body %s", pgpCreated.Code, pgpCreated.Body.String())
	}
	pgpKey := decode[model.PGPKey](t, pgpCreated)
	if pgpKey.UserIds[0] != "Key Test <keys@example.com>" {
		t.Fatalf("pgp user ids = %q", pgpKey.UserIds)
	}
	if duplicate := request(t, router, account, http.MethodPost, "/api/keys/pgp", string(pgpBody)); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate pgp: status %d body %s", duplicate.Code, duplicate.Body.String())
	}
	if listed := request(t, router, account, http.MethodGet, "/api/keys/pgp", ""); len(decode[[]model.PGPKey](t, listed)) != 1 {
		t.Fatalf("list pgp: status %d body %s", listed.Code, listed.Body.String())
	}
	if deleted := request(t, router, account, http.MethodDelete, "/api/keys/pgp/"+pgpKey.Id, ""); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete pgp: status %d body %s", deleted.Code, deleted.Body.String())
	}

	// Invalid input never reaches the database.
	invalid, _ := json.Marshal(map[string]string{"public_key": "not a key"})
	if got := request(t, router, account, http.MethodPost, "/api/keys/ssh", string(invalid)); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid ssh: status %d body %s", got.Code, got.Body.String())
	}
}

// The anonymous listing hangs off /accounts/:name, which the profiles module
// already owns. Gin's tree must accept the child route beside the profile
// routes; a conflict would panic at boot.
func TestRoutesCoexistWithProfileSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api")
	profilectl.Register(api, profilectl.Deps{})
	Register(api, Deps{})
	for _, route := range router.Routes() {
		if route.Path == "/api/accounts/:name/keys" {
			return
		}
	}
	t.Fatal("public key listing route was not registered")
}
