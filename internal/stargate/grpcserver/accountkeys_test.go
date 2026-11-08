package grpcserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/gorm"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/accountkeys"
	"src.solsynth.dev/sosys/stargate/auth"
	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/db"
	"src.solsynth.dev/sosys/stargate/dbtest"
	"src.solsynth.dev/sosys/stargate/geo"
	"src.solsynth.dev/sosys/stargate/migrate"
	"src.solsynth.dev/sosys/stargate/store"
)

func keyTestDatabase(t *testing.T) *gorm.DB {
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

func keyTestSSH(t *testing.T) *accountkeys.SSHKeyInfo {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	info, err := accountkeys.ParseSSHKey(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func keyTestPGP(t *testing.T) *accountkeys.PGPKeyInfo {
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
	info, err := accountkeys.ParsePGPKey(buffer.String())
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestAccountKeyService(t *testing.T) {
	database := keyTestDatabase(t)
	ctx := context.Background()
	accountID := uuid.New()
	name := "grpc_keys_" + uuid.NewString()[:8]
	now := time.Now().UTC()
	if err := database.Exec(`INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES (?, ?, ?, 'en', 'US', false, ?, ?)`, accountID, name, name, now, now).Error; err != nil {
		t.Fatal(err)
	}

	keys := accountkeys.NewStore(database)
	sshInfo := keyTestSSH(t)
	sshKey, err := keys.CreateSSHKey(ctx, accountID, "laptop", sshInfo)
	if err != nil {
		t.Fatal(err)
	}
	pgpInfo := keyTestPGP(t)
	pgpKey, err := keys.CreatePGPKey(ctx, accountID, "mail", pgpInfo)
	if err != nil {
		t.Fatal(err)
	}

	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	RegisterAccountKeys(server, AccountKeyDeps{Keys: keys})
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := gen.NewDyAccountKeyServiceClient(conn)

	listed, err := client.ListPublicKeys(ctx, &gen.DyListPublicKeysRequest{AccountId: accountID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Keys) != 2 {
		t.Fatalf("ListPublicKeys returned %d keys, want 2", len(listed.Keys))
	}
	for _, key := range listed.Keys {
		switch key.GetKind() {
		case gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_SSH:
			if key.GetFingerprint() != sshKey.Fingerprint || key.GetLabel() != "laptop" {
				t.Fatalf("ssh listing = %+v", key)
			}
		case gen.DyAccountPublicKeyKind_DY_ACCOUNT_PUBLIC_KEY_KIND_PGP:
			if key.GetPgpKeyId() != pgpKey.KeyId || key.GetUserIds()[0] != "Key Test <keys@example.com>" {
				t.Fatalf("pgp listing = %+v", key)
			}
		default:
			t.Fatalf("unexpected kind %v", key.GetKind())
		}
	}

	validated, err := client.ValidateSSHPublicKey(ctx, &gen.DyValidateSSHPublicKeyRequest{
		PublicKey: sshInfo.PublicKey,
		Purpose:   "git-receive-pack",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !validated.GetValid() || validated.GetAccountId() != accountID.String() ||
		validated.GetKeyId() != sshKey.Id || validated.GetPurpose() != "git-receive-pack" {
		t.Fatalf("ValidateSSHPublicKey = %+v", validated)
	}

	missing, err := client.ValidateSSHPublicKey(ctx, &gen.DyValidateSSHPublicKeyRequest{
		Fingerprint: "SHA256:unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	if missing.GetValid() || missing.GetReason() != "KEY_NOT_FOUND" {
		t.Fatalf("unknown SSH key = %+v", missing)
	}

	pgpValid, err := client.ValidatePGPPublicKey(ctx, &gen.DyValidatePGPPublicKeyRequest{
		Fingerprint: pgpInfo.Fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !pgpValid.GetValid() || pgpValid.GetAccountId() != accountID.String() || pgpValid.GetKeyId() != pgpKey.Id {
		t.Fatalf("ValidatePGPPublicKey = %+v", pgpValid)
	}

	if _, err := client.ValidateSSHPublicKey(ctx, &gen.DyValidateSSHPublicKeyRequest{
		PublicKey: "not a key",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid SSH key code = %v, want InvalidArgument", status.Code(err))
	}

	if _, err := client.ListPublicKeys(ctx, &gen.DyListPublicKeysRequest{AccountId: "not-a-uuid"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid account id code = %v, want InvalidArgument", status.Code(err))
	}
}

// keyTestJWT builds an RSA signing pair in a temp dir so the exchange test
// does not depend on the deployment's keys/ checkout.
func keyTestJWT(t *testing.T) (*auth.JWTService, *config.Config) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "PrivateKey.pem")
	publicPath := filepath.Join(dir, "PublicKey.pem")
	if err := os.WriteFile(privatePath, privatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, publicPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Auth.PrivateKeyPath = privatePath
	cfg.Auth.PublicKeyPath = publicPath
	cfg.Hosted.ExchangeToken = "test-exchange-token"
	svc, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc, cfg
}

func TestExchangeSSHPublicKeyForSession(t *testing.T) {
	database := keyTestDatabase(t)
	ctx := context.Background()
	accountID := uuid.New()
	name := "grpc_exchange_" + uuid.NewString()[:8]
	now := time.Now().UTC()
	if err := database.Exec(`INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
		VALUES (?, ?, ?, 'en', 'US', false, ?, ?)`, accountID, name, name, now, now).Error; err != nil {
		t.Fatal(err)
	}

	keys := accountkeys.NewStore(database)
	sshInfo := keyTestSSH(t)
	sshKey, err := keys.CreateSSHKey(ctx, accountID, "laptop", sshInfo)
	if err != nil {
		t.Fatal(err)
	}

	jwtSvc, cfg := keyTestJWT(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(database)
	authSvc := auth.NewAuthService(st, nil, cfg, geo.NewService(""), jwtSvc, auth.NewTokenAuthService(st, nil, jwtSvc, nil, nil, log), nil, nil, log)

	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	RegisterAccountKeys(server, AccountKeyDeps{Keys: keys, Auth: authSvc, Store: st, Cfg: cfg, Log: log})
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := gen.NewDyAccountKeyServiceClient(conn)

	if _, err := client.ExchangeSSHPublicKeyForSession(ctx, &gen.DyExchangeSSHPublicKeyForSessionRequest{
		ServiceToken: "wrong",
		PublicKey:    sshInfo.PublicKey,
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong exchange token code = %v, want PermissionDenied", status.Code(err))
	}

	missing, err := client.ExchangeSSHPublicKeyForSession(ctx, &gen.DyExchangeSSHPublicKeyForSessionRequest{
		ServiceToken: cfg.Hosted.ExchangeToken,
		Fingerprint:  "SHA256:unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	if missing.GetValid() || missing.GetReason() != "KEY_NOT_FOUND" {
		t.Fatalf("unknown SSH key exchange = %+v", missing)
	}

	exchanged, err := client.ExchangeSSHPublicKeyForSession(ctx, &gen.DyExchangeSSHPublicKeyForSessionRequest{
		ServiceToken: cfg.Hosted.ExchangeToken,
		PublicKey:    sshInfo.PublicKey,
		Purpose:      "termian-hosted",
		DeviceName:   "laptop",
		IpAddress:    "203.0.113.9",
		UserAgent:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !exchanged.GetValid() || exchanged.GetAccountId() != accountID.String() ||
		exchanged.GetKeyId() != sshKey.Id || exchanged.GetPurpose() != "termian-hosted" {
		t.Fatalf("ExchangeSSHPublicKeyForSession = %+v", exchanged)
	}
	if exchanged.GetAccessToken() == "" || exchanged.GetRefreshToken() == "" {
		t.Fatalf("exchange returned no tokens: %+v", exchanged)
	}
	if exchanged.GetExpiresIn() <= 0 || exchanged.GetRefreshExpiresIn() <= 0 {
		t.Fatalf("exchange returned non-positive lifetimes: %+v", exchanged)
	}
	// Hosted clients keep sessions alive through the challenge refresh
	// endpoint; the exchange must mint a token that endpoint accepts.
	if _, _, err := authSvc.RefreshSessionAndIssueTokens(ctx, exchanged.GetRefreshToken()); err != nil {
		t.Fatalf("exchanged refresh token was rejected: %v", err)
	}
}
