package passkit

import (
	"archive/zip"
	"bytes"
	"crypto/sha1" // Apple's manifest.json is SHA-1 by specification.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"src.solsynth.dev/sosys/stargate/model"
)

// passField is a Passbook field.
type passField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// genericPass is the generic-style field block.
type genericPass struct {
	HeaderFields    []passField `json:"headerFields"`
	PrimaryFields   []passField `json:"primaryFields"`
	SecondaryFields []passField `json:"secondaryFields"`
	AuxiliaryFields []passField `json:"auxiliaryFields"`
	BackFields      []passField `json:"backFields"`
}

// barcodeJSON is both the legacy `barcode` object and one `barcodes` entry.
type barcodeJSON struct {
	Format          string `json:"format"`
	Message         string `json:"message"`
	MessageEncoding string `json:"messageEncoding"`
	AltText         string `json:"altText,omitempty"`
}

// passDocument is the pass.json payload.
type passDocument struct {
	FormatVersion              int               `json:"formatVersion"`
	PassTypeIdentifier         string            `json:"passTypeIdentifier"`
	SerialNumber               string            `json:"serialNumber"`
	TeamIdentifier             string            `json:"teamIdentifier"`
	OrganizationName           string            `json:"organizationName"`
	Description                string            `json:"description"`
	LogoText                   string            `json:"logoText,omitempty"`
	ForegroundColor            string            `json:"foregroundColor,omitempty"`
	BackgroundColor            string            `json:"backgroundColor,omitempty"`
	LabelColor                 string            `json:"labelColor,omitempty"`
	Style                      string            `json:"style"`
	WebServiceURL              string            `json:"webServiceURL"`
	AuthenticationToken        string            `json:"authenticationToken"`
	AssociatedStoreIdentifiers []int             `json:"associatedStoreIdentifiers,omitempty"`
	Generic                    genericPass       `json:"generic"`
	Barcode                    barcodeJSON       `json:"barcode"`
	Barcodes                   []barcodeJSON     `json:"barcodes"`
	UserInfo                   map[string]string `json:"userInfo"`
}

// buildPassDocument mirrors ApplePassService.BuildPassRequestAsync.
func buildPassDocument(settings Settings, pass *PassEntity, account *model.Account, profileURL string) passDocument {
	program := programLabel(account)
	document := passDocument{
		FormatVersion:       1,
		PassTypeIdentifier:  pass.PassTypeIdentifier,
		SerialNumber:        pass.SerialNumber,
		TeamIdentifier:      settings.TeamIdentifier,
		OrganizationName:    settings.OrganizationName,
		Description:         settings.Description,
		LogoText:            settings.LogoText,
		ForegroundColor:     settings.ForegroundColor,
		BackgroundColor:     settings.BackgroundColor,
		LabelColor:          settings.LabelColor,
		Style:               "generic",
		WebServiceURL:       settings.WebServiceURL,
		AuthenticationToken: pass.AuthenticationToken,
		Generic: genericPass{
			HeaderFields: []passField{
				{Key: "h1", Label: "USERNAME", Value: "@" + account.Name},
			},
			PrimaryFields: []passField{
				{Key: "p1", Label: "NAME", Value: displayName(account)},
			},
			SecondaryFields: []passField{
				{Key: "s1", Label: "MEMBER SINCE", Value: memberSince(account)},
			},
			AuxiliaryFields: []passField{
				{Key: "a1", Label: "STELLAR PROGRAM", Value: program},
			},
			BackFields: []passField{
				{Key: "b1", Label: "Terms", Value: settings.TermsText},
				{Key: "b2", Label: "Profile", Value: profileURL},
				{Key: "b3", Label: "Account ID", Value: account.Id},
			},
		},
		UserInfo: map[string]string{"id": account.Id},
	}
	barcode := barcodeJSON{
		Format:          "PKBarcodeFormatQR",
		Message:         profileURL,
		MessageEncoding: "iso-8859-1",
		AltText:         settings.BarcodeAltText,
	}
	document.Barcode = barcode
	document.Barcodes = []barcodeJSON{barcode}
	if settings.AssociatedStoreIdentifier != nil {
		document.AssociatedStoreIdentifiers = []int{*settings.AssociatedStoreIdentifier}
	}
	return document
}

func displayName(account *model.Account) string {
	profile := account.Profile
	var parts []string
	if profile != nil {
		for _, part := range []*string{profile.FirstName, profile.MiddleName, profile.LastName} {
			if part != nil && strings.TrimSpace(*part) != "" {
				parts = append(parts, strings.TrimSpace(*part))
			}
		}
	}
	if full := strings.TrimSpace(strings.Join(parts, " ")); full != "" {
		return full
	}
	if strings.TrimSpace(account.Nick) != "" {
		return account.Nick
	}
	return account.Name
}

func memberSince(account *model.Account) string {
	var created time.Time
	switch {
	case account.CreatedAt != nil:
		created = account.CreatedAt.Time()
	case account.Profile != nil && account.Profile.CreatedAt != nil:
		created = account.Profile.CreatedAt.Time()
	default:
		created = time.Now().UTC()
	}
	return fmt.Sprintf("%d", created.UTC().Year())
}

func programLabel(account *model.Account) string {
	subscription := account.PerkSubscription
	if subscription == nil {
		return "Standard"
	}
	if subscription.DisplayName != nil && strings.TrimSpace(*subscription.DisplayName) != "" {
		return strings.TrimSpace(*subscription.DisplayName)
	}
	if subscription.PerkLevel > 0 {
		return fmt.Sprintf("Level %d", subscription.PerkLevel)
	}
	if strings.TrimSpace(subscription.Identifier) != "" {
		return subscription.Identifier
	}
	return "Standard"
}

// buildManifest hashes every archive file except manifest.json/signature.
func buildManifest(files map[string][]byte) ([]byte, error) {
	manifest := make(map[string]string, len(files))
	for name, data := range files {
		if name == "manifest.json" || name == "signature" {
			continue
		}
		sum := sha1.Sum(data)
		manifest[name] = hex.EncodeToString(sum[:])
	}
	return json.Marshal(manifest)
}

// buildPassArchive assembles the .pkpass zip: assets + pass.json,
// manifest.json and the detached PKCS#7 signature.
func buildPassArchive(files map[string][]byte, material *signingMaterial) ([]byte, error) {
	if _, ok := files["pass.json"]; !ok {
		return nil, fmt.Errorf("pass archive is missing pass.json")
	}
	manifest, err := buildManifest(files)
	if err != nil {
		return nil, fmt.Errorf("build manifest: %w", err)
	}
	signature, err := signManifest(manifest, material)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(files)+2)
	for name := range files {
		names = append(names, name)
	}
	names = append(names, "manifest.json", "signature")
	sort.Strings(names)

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range names {
		var data []byte
		switch name {
		case "manifest.json":
			data = manifest
		case "signature":
			data = signature
		default:
			data = files[name]
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(time.Unix(0, 0))
		entry, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("create zip entry %s: %w", name, err)
		}
		if _, err := entry.Write(data); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("write zip entry %s: %w", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish pass archive: %w", err)
	}
	return buffer.Bytes(), nil
}
