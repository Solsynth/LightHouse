package passkit

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/digitorus/pkcs7"

	"src.solsynth.dev/sosys/stargate/model"
)

func TestSettingsFromMap(t *testing.T) {
	settings := SettingsFromMap(map[string]any{
		"passTypeIdentifier":        "pass.dev.solsynth.member",
		"teamIdentifier":            "TEAM123",
		"organizationName":          "Solsynth",
		"description":               "Member pass",
		"webServiceUrl":             "https://id.solsynth.dev/api/passkit/v1",
		"signingCertificatePath":    "/certs/signer.pem",
		"appleWwdrCertificatePath":  "/certs/wwdr.pem",
		"associatedStoreIdentifier": float64(42),
		"siteUrl":                   "https://solsynth.dev/",
	})
	if settings.PassTypeIdentifier != "pass.dev.solsynth.member" {
		t.Fatalf("passTypeIdentifier = %q", settings.PassTypeIdentifier)
	}
	if settings.WebServiceURL != "https://id.solsynth.dev/api/passkit/v1" {
		t.Fatalf("webServiceUrl = %q", settings.WebServiceURL)
	}
	if settings.ForegroundColor != "rgb(255, 255, 255)" {
		t.Fatalf("default foreground = %q", settings.ForegroundColor)
	}
	if settings.AssociatedStoreIdentifier == nil || *settings.AssociatedStoreIdentifier != 42 {
		t.Fatalf("associatedStoreIdentifier = %v", settings.AssociatedStoreIdentifier)
	}
	if missing := settings.MissingRequired(); len(missing) != 0 {
		t.Fatalf("MissingRequired() = %v, want none", missing)
	}

	empty := SettingsFromMap(nil)
	if missing := empty.MissingRequired(); len(missing) != 7 {
		t.Fatalf("empty MissingRequired() = %v, want 7 entries", missing)
	}
}

func TestBuildPassDocumentShape(t *testing.T) {
	settings := DefaultSettings()
	settings.PassTypeIdentifier = "pass.dev.solsynth.member"
	settings.TeamIdentifier = "TEAM123"
	settings.OrganizationName = "Solsynth"
	settings.Description = "Member pass"
	settings.WebServiceURL = "https://id.solsynth.dev/api/passkit/v1"
	nick := "LittleSheep"
	account := &model.Account{
		Id:        "7f1d4d2e-0f9a-4a1a-9f2e-2c3f6a1b0c11",
		Name:      "littlesheep",
		Nick:      nick,
		CreatedAt: model.NewTime(time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)),
		Profile: &model.Profile{
			CreatedAt: model.NewTime(time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
		PerkSubscription: &model.SnSubscriptionReferenceObject{
			Identifier: "stellar",
			PerkLevel:  2,
		},
	}
	pass := &PassEntity{
		SerialNumber:        account.Id,
		PassTypeIdentifier:  settings.PassTypeIdentifier,
		AuthenticationToken: "deadbeef",
	}
	document := buildPassDocument(settings, pass, account, "https://solsynth.dev/accounts/littlesheep")

	if document.Style != "generic" || document.FormatVersion != 1 {
		t.Fatalf("unexpected document header: %+v", document)
	}
	if document.Generic.PrimaryFields[0].Value != nick {
		t.Fatalf("primary field = %q, want %q", document.Generic.PrimaryFields[0].Value, nick)
	}
	if document.Generic.SecondaryFields[0].Value != "2021" {
		t.Fatalf("member since = %q, want 2021", document.Generic.SecondaryFields[0].Value)
	}
	if document.Generic.AuxiliaryFields[0].Value != "Level 2" {
		t.Fatalf("program = %q, want Level 2", document.Generic.AuxiliaryFields[0].Value)
	}
	if document.Barcode.Message != "https://solsynth.dev/accounts/littlesheep" ||
		document.Barcode.Format != "PKBarcodeFormatQR" {
		t.Fatalf("unexpected barcode: %+v", document.Barcode)
	}
	if document.UserInfo["id"] != account.Id {
		t.Fatalf("userInfo = %v", document.UserInfo)
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip["webServiceURL"] != settings.WebServiceURL {
		t.Fatalf("webServiceURL = %v", roundTrip["webServiceURL"])
	}
	if roundTrip["authenticationToken"] != "deadbeef" {
		t.Fatalf("authenticationToken = %v", roundTrip["authenticationToken"])
	}
}

func TestBuildPassArchiveSigned(t *testing.T) {
	directory := t.TempDir()
	signerPath := filepath.Join(directory, "signer.pem")
	wwdrPath := filepath.Join(directory, "wwdr.pem")
	writeIdentity(t, signerPath, "Pass Signer", 1)
	writeIdentity(t, wwdrPath, "Apple WWDR", 2)

	material, err := loadSigningMaterial(signerPath, "", wwdrPath)
	if err != nil {
		t.Fatalf("loadSigningMaterial: %v", err)
	}

	files := map[string][]byte{
		"pass.json":   []byte(`{"formatVersion":1}`),
		"icon.png":    []byte("icon-bytes"),
		"icon@2x.png": []byte("icon-2x-bytes"),
	}
	archive, err := buildPassArchive(files, material)
	if err != nil {
		t.Fatalf("buildPassArchive: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	contents := map[string][]byte{}
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		var buffer bytes.Buffer
		if _, err := buffer.ReadFrom(handle); err != nil {
			_ = handle.Close()
			t.Fatalf("read %s: %v", file.Name, err)
		}
		_ = handle.Close()
		contents[file.Name] = buffer.Bytes()
	}
	for _, required := range []string{"pass.json", "icon.png", "icon@2x.png", "manifest.json", "signature"} {
		if _, ok := contents[required]; !ok {
			t.Fatalf("archive is missing %s (has %v)", required, keys(contents))
		}
	}

	var manifest map[string]string
	if err := json.Unmarshal(contents["manifest.json"], &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	for name, data := range files {
		sum := sha1.Sum(data)
		if manifest[name] != hex.EncodeToString(sum[:]) {
			t.Fatalf("manifest[%s] = %q", name, manifest[name])
		}
	}

	signed, err := pkcs7.Parse(contents["signature"])
	if err != nil {
		t.Fatalf("parse signature: %v", err)
	}
	signed.Content = contents["manifest.json"]
	if err := signed.Verify(); err != nil {
		t.Fatalf("verify signature: %v", err)
	}
}

func TestBuildPassArchiveRequiresPassJSON(t *testing.T) {
	if _, err := buildPassArchive(map[string][]byte{"icon.png": []byte("x")}, &signingMaterial{}); err == nil {
		t.Fatal("buildPassArchive accepted an archive without pass.json")
	}
}

func TestBuildLastUpdatedTag(t *testing.T) {
	service := NewService(Deps{})
	account := &model.Account{
		Id:        "7f1d4d2e-0f9a-4a1a-9f2e-2c3f6a1b0c11",
		Name:      "littlesheep",
		Nick:      "LittleSheep",
		UpdatedAt: model.NewTime(time.Unix(1_700_000_000, 0).UTC()),
		Profile: &model.Profile{
			UpdatedAt: model.NewTime(time.Unix(1_700_000_100, 0).UTC()),
		},
		PerkSubscription: &model.SnSubscriptionReferenceObject{Identifier: "stellar", PerkLevel: 2},
	}
	tag := service.BuildLastUpdatedTag(account)
	if len(tag) != 64 {
		t.Fatalf("tag length = %d, want 64", len(tag))
	}
	if again := service.BuildLastUpdatedTag(account); again != tag {
		t.Fatal("BuildLastUpdatedTag is not deterministic")
	}
	account.Nick = "changed"
	if service.BuildLastUpdatedTag(account) == tag {
		t.Fatal("BuildLastUpdatedTag ignored a nickname change")
	}
}

func TestIsAuthorized(t *testing.T) {
	service := NewService(Deps{})
	pass := &PassEntity{AuthenticationToken: "abc123"}
	for header, want := range map[string]bool{
		"ApplePass abc123":   true,
		"applepass abc123":   true,
		"ApplePass  abc123 ": true,
		"ApplePass abc124":   false,
		"Bearer abc123":      false,
		"":                   false,
		"ApplePass":          false,
	} {
		if got := service.IsAuthorized(pass, header); got != want {
			t.Fatalf("IsAuthorized(%q) = %v, want %v", header, got, want)
		}
	}
}

func writeIdentity(t *testing.T, path, commonName string, serial int64) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	buffer := &bytes.Buffer{}
	if err := pem.Encode(buffer, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(buffer, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func keys(values map[string][]byte) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	return names
}
